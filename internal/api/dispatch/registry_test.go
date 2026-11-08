package dispatch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/api/registry/route"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	_ "github.com/guided-traffic/trove/internal/formats/all" // registers the OCI format, whose routes the match reads
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
)

var (
	ociLocal = qname.Repo{Project: "web", Key: "docker-local"}
	ociHub   = qname.Repo{Project: "web", Key: "docker-hub"}
	ociOther = qname.Repo{Project: "web", Key: "team-files"}
)

// ociRig is the registry root on a router with the real OCI format (the work packages of its handlers answer 501 until they
// are built; the routing, the checks and the shapes are what is tested), repositories of the format and one of another format,
// and recording stand-ins for the three core routes.
type ociRig struct {
	t       *testing.T
	d       *Dispatcher
	h       http.Handler
	decider *fakeDecider
	repos   *fakeRepos
	labels  []string
	core    []string
}

func newOCIRig(t *testing.T) *ociRig {
	t.Helper()
	r := &ociRig{t: t, decider: &fakeDecider{}, repos: &fakeRepos{byKey: map[qname.Repo]repository.Repository{
		ociLocal:                            {ID: "1", Name: ociLocal, Class: repository.Local, Format: "oci"},
		ociHub:                              {ID: "2", Name: ociHub, Class: repository.Remote, Format: "oci"},
		ociOther:                            {ID: "3", Name: ociOther, Class: repository.Local, Format: "generic"},
		{Project: "web", Key: "docker-off"}: {ID: "4", Name: qname.Repo{Project: "web", Key: "docker-off"}, Class: repository.Local, Format: "oci", Disabled: true},
	}}}
	d, err := New(Options{
		Host: fakeHost{}, Decider: r.decider, Repos: r.repos,
		Label: func(_ context.Context, name string) { r.labels = append(r.labels, name) },
	})
	if err != nil {
		t.Fatal(err)
	}
	mark := func(name string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			r.core = append(r.core, name)
			w.WriteHeader(http.StatusOK)
		})
	}
	h, err := d.RegistryHandler(CoreRoutes{Version: mark("version"), Catalog: mark("catalog"), Token: mark("token")})
	if err != nil {
		t.Fatal(err)
	}
	r.d, r.h = d, h
	return r
}

// do sends a request to the registry handler; bound is the repository the host root is bound to.
func (r *ociRig) do(method, path string, bound qname.Repo) *httptest.ResponseRecorder {
	r.t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), method, path, nil)
	if !bound.IsZero() {
		req = req.WithContext(httpserver.WithResolved(req.Context(), httpserver.RequestInfo{RootRepository: bound}))
	}
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	return rec
}

// code reads the distribution specification's error code of an answer.
func ociCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Errors []struct {
			Code string `json:"code"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.Errors) != 1 {
		t.Fatalf("answer %d is no OCI error body: %v %q", rec.Code, err, rec.Body.String())
	}
	return body.Errors[0].Code
}

// R-OCI-001, unit level: the end-to-end proof is in test/integration/registry.
func TestAPathThatNamesNoReadableRepositoryIsNameUnknown(t *testing.T) {
	for name, c := range map[string]struct {
		path string
		deny authz.Outcome
	}{
		"fewer than three segments (one)":           {path: "/v2/web"},
		"fewer than three segments (two)":           {path: "/v2/web/docker-local"},
		"a repository key and nothing more":         {path: "/v2/web/docker-local/"},
		"a project that does not exist":             {path: "/v2/nope/docker-local/app/manifests/1"},
		"a repository that does not exist":          {path: "/v2/web/nope/app/manifests/1"},
		"a repository of another format":            {path: "/v2/web/team-files/app/manifests/1"},
		"a repository the caller cannot see":        {path: "/v2/web/docker-local/app/manifests/1", deny: authz.DenyHidden},
		"an upper-case project":                     {path: "/v2/Web/docker-local/app/manifests/1"},
		"an escaped slash in the project":           {path: "/v2/web%2Fx/docker-local/app/manifests/1"},
		"a core name used as a project":             {path: "/v2/_catalog/docker-local/app/manifests/1"},
		"the catalog with a trailing slash":         {path: "/v2/_catalog/"},
		"an escaped spelling of the catalog":        {path: "/v2/%5Fcatalog"},
		"an escaped spelling of the token endpoint": {path: "/v2/%5Ftoken"},
		"a path that is no route of the format":     {path: "/v2/web/docker-local/app/nothing"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newOCIRig(t)
			r.decider.decision = authz.Decision{Outcome: c.deny}
			rec := r.do("GET", c.path, qname.Repo{})
			if rec.Code != http.StatusNotFound || ociCode(t, rec) != "NAME_UNKNOWN" {
				t.Fatalf("%s: %d %s", c.path, rec.Code, rec.Body.String())
			}
			if v := rec.Header().Get(route.APIVersionHeader); v != "registry/2.0" {
				t.Fatalf("Docker-Distribution-API-Version %q", v)
			}
			if len(r.core) != 0 {
				t.Fatalf("a core handler served %v", r.core)
			}
		})
	}
}

// R-OCI-001, unit level: the end-to-end proof is in test/integration/registry.
func TestAnUncleanPathIsRefusedBeforeTheDecisionPoint(t *testing.T) {
	for _, p := range []string{"/v2/web/docker-local/a//b/manifests/1", "/v2/web/docker-local/a/../b/manifests/1", "/v2/web/docker-local/%2e%2e/manifests/1"} {
		r := newOCIRig(t)
		rec := r.do("GET", p, qname.Repo{})
		if rec.Code != http.StatusBadRequest || ociCode(t, rec) != "NAME_INVALID" {
			t.Errorf("%s: %d %s", p, rec.Code, rec.Body.String())
		}
		if len(r.decider.asked) != 0 || len(r.repos.lookup) != 0 {
			t.Errorf("%s: asked %v, looked up %v", p, r.decider.asked, r.repos.lookup)
		}
	}
}

// R-OCI-003, unit level: the end-to-end proof is in test/integration/registry.
func TestACoreRouteIsServedByItsMatchAndNothingElse(t *testing.T) {
	r := newOCIRig(t)
	for _, c := range [][2]string{{"/v2/", "version"}, {"/v2/_catalog", "catalog"}, {"/v2/_token", "token"}} {
		r.core = nil
		if rec := r.do("GET", c[0], qname.Repo{}); rec.Code != http.StatusOK || strings.Join(r.core, ",") != c[1] {
			t.Errorf("%s: %d, core %v, want %s", c[0], rec.Code, r.core, c[1])
		}
	}
	r.core = nil
	if rec := r.do("POST", "/v2/_token", qname.Repo{}); rec.Code != http.StatusOK || strings.Join(r.core, ",") != "token" {
		t.Errorf("the token service takes POST: %d %v", rec.Code, r.core)
	}
	// The same names spelled so that ServeMux would hand them to a literal pattern are no core route here.
	for _, p := range []string{"/v2/%5Fcatalog", "/v2/%5ftoken", "/v2/_catalog/x", "/v2//", "/v2/./_catalog"} {
		r.core = nil
		if rec := r.do("GET", p, qname.Repo{}); len(r.core) != 0 || rec.Code < 400 {
			t.Errorf("%s: served by %v (%d)", p, r.core, rec.Code)
		}
	}
	if len(r.labels) != 4 || r.labels[0] != route.NameVersion || r.labels[1] != route.NameCatalog || r.labels[2] != route.NameToken || r.labels[3] != route.NameToken {
		t.Fatalf("labels %v", r.labels)
	}
}

// The enforcement point and the handler read one match (R3-1): the route the dispatcher labels is the route RegistryTarget names,
// for every request, and a request the match refuses reaches no route.
// R-OCI-005, unit level: the end-to-end proof is in test/integration/registry.
func TestTheRouteServedIsTheRouteMatched(t *testing.T) {
	digest := "sha256:" + strings.Repeat("ab", 32)
	for _, c := range []struct {
		method, path string
		bound        qname.Repo
	}{
		{"GET", "/v2/", qname.Repo{}},
		{"HEAD", "/v2", qname.Repo{}},
		{"GET", "/v2/_catalog", qname.Repo{}},
		{"POST", "/v2/_catalog", qname.Repo{}},
		{"GET", "/v2/_token", qname.Repo{}},
		{"POST", "/v2/_token", qname.Repo{}},
		{"DELETE", "/v2/", qname.Repo{}},
		{"GET", "/v2/web/docker-local/app/manifests/1", qname.Repo{}},
		{"HEAD", "/v2/web/docker-local/app/manifests/1", qname.Repo{}},
		{"PUT", "/v2/web/docker-local/a/b/manifests/1", qname.Repo{}},
		{"DELETE", "/v2/web/docker-local/app/manifests/" + digest, qname.Repo{}},
		{"GET", "/v2/web/docker-local/app/manifests/" + digest, qname.Repo{}},
		{"POST", "/v2/web/docker-local/app/blobs/uploads/", qname.Repo{}},
		{"PATCH", "/v2/web/docker-local/app/blobs/uploads/abc", qname.Repo{}},
		{"GET", "/v2/web/docker-local/app/tags/list", qname.Repo{}},
		{"GET", "/v2/web/docker-local/app/referrers/" + digest, qname.Repo{}},
		{"GET", "/v2/web/docker-hub~cache/lib/alpine/blobs/" + digest, qname.Repo{}},
		{"GET", "/v2/web/docker-local/app/blobs/" + digest, ociHub}, // bound to a repository of the format: the whole path is the image
		{"GET", "/v2/library/alpine/manifests/3", ociHub},
		{"POST", "/v2/library/alpine/blobs/uploads/", ociHub},
		{"GET", "/v2/library/alpine/manifests/3", ociOther},
		{"GET", "/v2/web/docker-local/app/manifests/1", ociOther}, // bound to another format: no binding
		{"GET", "/v2/library/alpine/manifests/3", qname.Repo{Project: "web", Key: "ghost"}},
		{"TRACE", "/v2/web/docker-local/app/manifests/1", qname.Repo{}},
		{"GET", "/v2/web/docker-local/app/nothing", qname.Repo{}},
		{"GET", "/v2/web/docker-local/a//b/manifests/1", qname.Repo{}},
		{"GET", "/v2/%5Fcatalog", qname.Repo{}},
		{"GET", "/v2/web", qname.Repo{}},
	} {
		r := newOCIRig(t)
		target, matched := r.d.RegistryTarget(httptestRequest(c.method, c.path, c.bound))
		r.do(c.method, c.path, c.bound)
		switch {
		case !matched && len(r.labels) != 0:
			t.Errorf("%s %s (bound %v): the match refused it and the handler labelled %v", c.method, c.path, c.bound, r.labels)
		case matched && (len(r.labels) != 1 || r.labels[0] != target.Route):
			t.Errorf("%s %s (bound %v): the match names %s, the handler labelled %v", c.method, c.path, c.bound, target.Route, r.labels)
		}
	}
}

func httptestRequest(method, path string, bound qname.Repo) *http.Request {
	req := httptest.NewRequestWithContext(context.Background(), method, path, nil)
	if !bound.IsZero() {
		req = req.WithContext(httpserver.WithResolved(req.Context(), httpserver.RequestInfo{RootRepository: bound}))
	}
	return req
}

// R-OCI-029, unit level: the end-to-end proof is in test/integration/registry.
func TestABoundHostFixesTheRepositoryAndTheWholePathIsTheImage(t *testing.T) {
	t.Run("a host bound to a repository of the format serves the image path under it", func(t *testing.T) {
		r := newOCIRig(t)
		rec := r.do("GET", "/v2/library/alpine/manifests/3", ociHub)
		if rec.Code != http.StatusNotImplemented || len(r.labels) != 1 || r.labels[0] != "oci.manifest.get.tag" {
			t.Fatalf("%d, labels %v", rec.Code, r.labels)
		}
		if len(r.decider.asked) != 1 || r.decider.asked[0].Repository != ociHub {
			t.Fatalf("the decision point was asked %v", r.decider.asked)
		}
		got, ok := r.d.RegistryTarget(httptestRequest("GET", "/v2/library/alpine/manifests/3", ociHub))
		if !ok || !got.Bound || got.Repo != ociHub || got.Image != "library/alpine" {
			t.Fatalf("target %+v %v", got, ok)
		}
	})
	t.Run("the unbound spelling on a bound host names an image of the bound repository", func(t *testing.T) {
		r := newOCIRig(t)
		got, ok := r.d.RegistryTarget(httptestRequest("GET", "/v2/web/docker-hub/library/alpine/manifests/3", ociHub))
		if !ok || got.Repo != ociHub || got.Image != "web/docker-hub/library/alpine" {
			t.Fatalf("target %+v %v", got, ok)
		}
		rec := r.do("GET", "/v2/web/docker-hub/library/alpine/manifests/3", ociHub)
		if rec.Code != http.StatusNotImplemented {
			t.Fatalf("%d", rec.Code)
		}
	})
	t.Run("a binding to a repository of another format, a missing one or an unreadable one behaves as no binding", func(t *testing.T) {
		for name, bound := range map[string]qname.Repo{"another format": ociOther, "missing": {Project: "web", Key: "ghost"}} {
			r := newOCIRig(t)
			rec := r.do("GET", "/v2/library/alpine/manifests/3", bound)
			if rec.Code != http.StatusNotFound || ociCode(t, rec) != "NAME_UNKNOWN" || len(r.labels) != 0 {
				t.Errorf("%s: %d %s labels %v", name, rec.Code, rec.Body.String(), r.labels)
			}
			// The path keeps its unbound meaning.
			got, ok := r.d.RegistryTarget(httptestRequest("GET", "/v2/web/docker-local/app/manifests/1", bound))
			if !ok || got.Bound || got.Repo != ociLocal || got.Image != "app" {
				t.Errorf("%s: target %+v %v", name, got, ok)
			}
			if _, isBound, err := r.d.BoundRepository(httptestRequest("GET", "/v2/", bound)); isBound || err != nil {
				t.Errorf("%s: BoundRepository bound %v, error %v", name, isBound, err)
			}
		}
	})
	t.Run("a failed lookup of the binding is no match: a registry token is refused and the request answers the error", func(t *testing.T) {
		r := newOCIRig(t)
		r.d.o.Repos = failingRepos{}
		if _, ok := r.d.RegistryTarget(httptestRequest("GET", "/v2/library/alpine/manifests/3", ociHub)); ok {
			t.Fatal("a failed lookup matched")
		}
		if _, _, err := r.d.BoundRepository(httptestRequest("GET", "/v2/", ociHub)); err == nil {
			t.Fatal("BoundRepository hid the failed lookup")
		}
		if rec := r.do("GET", "/v2/library/alpine/manifests/3", ociHub); rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("the core routes are the host's own and name no repository", func(t *testing.T) {
		r := newOCIRig(t)
		if rec := r.do("GET", "/v2/", ociHub); rec.Code != http.StatusOK || len(r.core) != 1 || r.core[0] != "version" {
			t.Fatalf("%d %v", rec.Code, r.core)
		}
	})
}

type failingRepos struct{ repository.Reader }

func (failingRepos) ByName(context.Context, qname.Repo) (repository.Repository, error) {
	return repository.Repository{}, errs.E(errs.Unavailable, errs.CodeInternal, "the store is away")
}

// R-OCI-001, unit level: the end-to-end proof is in test/integration/registry.
func TestAClassThatDoesNotServeARouteAnswers405InTheNativeShape(t *testing.T) {
	r := newOCIRig(t)
	rec := r.do("POST", "/v2/web/docker-hub/library/alpine/blobs/uploads/", qname.Repo{})
	if rec.Code != http.StatusMethodNotAllowed || ociCode(t, rec) != "UNSUPPORTED" {
		t.Fatalf("a push to a remote repository: %d %s", rec.Code, rec.Body.String())
	}
	if rec := r.do("PATCH", "/v2/web/docker-local/app/manifests/1", qname.Repo{}); rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") == "" {
		t.Fatalf("a method the path has no route for: %d %v", rec.Code, rec.Header())
	}
}
