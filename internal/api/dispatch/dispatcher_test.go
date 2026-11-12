package dispatch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/guided-traffic/trove/internal/api/edge"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
)

// probe is a format with routes and its own error shape.
type probe struct {
	formats.Base
	handler formats.HandlerFunc
}

func (p probe) Descriptor() formats.Descriptor {
	return formats.Descriptor{
		Facts: catalog.Facts{
			ID: "probe", Classes: []formats.Class{formats.Local, formats.Remote}, Layout: "probe",
			ProtocolBasePath: "/api/probe/{project}/{key}/", PatternSubject: formats.SubjectPath,
		},
		ContractVersion: formats.ContractVersion,
	}
}

func (p probe) Routes() []formats.Route {
	return []formats.Route{
		{
			Name: "probe.doc.get", Method: http.MethodGet, Template: "/{package}", Examples: []string{"left-pad"},
			Action: formats.ActionRead, Kind: formats.Metadata, Handler: p.handler,
		},
		{
			Name: "probe.readme.get", Method: http.MethodGet, Template: "/{package}/-/readme", Examples: []string{"left-pad/-/readme"},
			Action: formats.ActionRead, Kind: formats.Metadata, Wiki: true, Handler: p.handler,
		},
		{
			Name: "probe.file.put", Method: http.MethodPut, Template: "/{package}/-/{file}", Examples: []string{"left-pad/-/x.tgz"},
			Action: formats.ActionDeploy, Kind: formats.Upload, Classes: []formats.Class{formats.Local}, Handler: p.handler,
		},
		{
			Name: "probe.tag.put", Method: http.MethodPut, Template: "/-/tag/{package}", Examples: []string{"-/tag/left-pad"},
			Action: formats.ActionDeploy, Kind: formats.Admin, Classes: []formats.Class{formats.Local}, Handler: p.handler,
		},
		{
			Name: "probe.rev.delete", Method: http.MethodDelete, Template: "/{package}/-rev/{rev}", Examples: []string{"left-pad/-rev/1-x"},
			Action: formats.ActionDelete, Kind: formats.Delete, Classes: []formats.Class{formats.Local}, Handler: p.handler,
		},
	}
}

func (p probe) Match(m, path string) (*formats.Route, formats.Params, error) {
	return formats.MatchRoutes(p.Routes(), m, path)
}

func (probe) Classify(formats.Path) formats.PathInfo { return formats.PathInfo{Kind: formats.Content} }

func (probe) PackageDocumentPath(string) (formats.Path, bool) {
	return formats.Path{}, false
}

func (probe) WriteError(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusInternalServerError
	code := "internal"
	if f, ok := formats.RefusalFrom(r); ok {
		w.Header().Set("X-Probe-Registry", f.Registry())
	}
	if e, ok := errs.As(err); ok {
		status, code = e.Status(), e.Code
		w.Header().Set("X-Probe-Detail", e.Detail)
		if len(e.Allow) > 0 {
			w.Header().Set("Allow", strings.Join(e.Allow, ","))
		}
		if e.Challenge != "" { // passed on as it comes, as npm and formats.Base do
			w.Header().Set("WWW-Authenticate", e.Challenge)
		}
	}
	w.Header().Set("X-Probe-Error", code)
	w.WriteHeader(status)
}

// bare is a format without routes: generic.
type bare struct{ formats.Base }

func (bare) Descriptor() formats.Descriptor {
	return formats.Descriptor{
		Facts:           catalog.Facts{ID: "bare", Classes: []formats.Class{formats.Local}, Layout: "bare", PatternSubject: formats.SubjectPath},
		ContractVersion: formats.ContractVersion,
	}
}
func (bare) Routes() []formats.Route { return nil }
func (bare) Match(m, p string) (*formats.Route, formats.Params, error) {
	return formats.MatchRoutes(nil, m, p)
}

func (bare) Classify(formats.Path) formats.PathInfo { return formats.PathInfo{Kind: formats.Content} }

func (bare) PackageDocumentPath(string) (formats.Path, bool) {
	return formats.Path{}, false
}

type fakeDecider struct {
	authz.Decider
	mu       sync.Mutex
	decision authz.Decision
	// by, when set, decides per addressed repository instead of decision.
	by    func(qname.Repo) authz.Decision
	asked []authz.Coordinate
	begun []string
}

func (d *fakeDecider) Begin(_ context.Context, _ authz.Subject, keys ...qname.Repo) (*authz.Index, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.begun = append(d.begun, qkeys(keys)...)
	return nil, nil
}

func (d *fakeDecider) DecideOn(_ context.Context, _ *authz.Index, _ authz.Subject, c authz.Coordinate) authz.Decision {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.asked = append(d.asked, c)
	if d.by != nil {
		return d.by(c.Repository)
	}
	return d.decision
}

type fakeRepos struct {
	repository.Reader
	mu     sync.Mutex
	byKey  map[qname.Repo]repository.Repository
	lookup []qname.Repo
}

func (r *fakeRepos) ByName(_ context.Context, name qname.Repo) (repository.Repository, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lookup = append(r.lookup, name)
	if repo, ok := r.byKey[name]; ok {
		return repo, nil
	}
	return repository.Repository{}, errs.E(errs.NotFound, errs.CodeRepositoryNotFound, "no")
}

// fakeHost is the Host the dispatcher talks to. Begin counts the requests it began and
// the finish functions that ran; a fakeHost without a counter begins nothing.
type fakeHost struct {
	formats.Host
	begun *hostCalls
}

type hostCalls struct{ began, finished atomic.Int32 }

func (h fakeHost) Begin(*formats.Request) func() {
	if h.begun == nil {
		return func() {}
	}
	h.begun.began.Add(1)
	return func() { h.begun.finished.Add(1) }
}

type noDeployResolver struct{ formats.Resolver }

func (noDeployResolver) DeployTarget(_ context.Context, rq *formats.Request) (*repository.Repository, error) {
	if rq.Repo.Name == qr("virtual-with-target") {
		return &repository.Repository{ID: "1", Name: qr("local"), Class: repository.Local, Format: "probe"}, nil
	}
	return nil, errs.E(errs.MethodNotAllowed, errs.CodeNoDeployTarget, "none")
}

func (h fakeHost) Resolver() formats.Resolver { return noDeployResolver{} }

type rig struct {
	t       *testing.T
	d       *Dispatcher
	router  *httpserver.Router
	decider *fakeDecider
	repos   *fakeRepos
	reached []string
	labels  []string
	calls   hostCalls
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, decider: &fakeDecider{}, repos: &fakeRepos{byKey: map[qname.Repo]repository.Repository{
		qr("local"):               {ID: "1", Name: qr("local"), Class: repository.Local, Format: "probe"},
		qr("remote"):              {ID: "2", Name: qr("remote"), Class: repository.Remote, Format: "probe"},
		qr("virt"):                {ID: "3", Name: qr("virt"), Class: repository.Virtual, Format: "probe"},
		qr("other"):               {ID: "4", Name: qr("other"), Class: repository.Local, Format: "bare"},
		qr("off"):                 {ID: "5", Name: qr("off"), Class: repository.Local, Format: "probe", Disabled: true},
		qr("dying"):               {ID: "6", Name: qr("dying"), Class: repository.Local, Format: "probe", Lifecycle: repository.Deleting},
		qr("virtual"):             {ID: "7", Name: qr("virtual"), Class: repository.Virtual, Format: "probe"},
		qr("virtual-with-target"): {ID: "8", Name: qr("virtual-with-target"), Class: repository.Virtual, Format: "probe"},
	}}}
	p := probe{handler: func(_ context.Context, rq *formats.Request, w http.ResponseWriter) error {
		view := ""
		if rq.CacheView {
			view = " cache-view"
		}
		r.reached = append(r.reached, rq.Route.Name+" "+rq.Repo.Name.String()+" "+rq.Params.Get("package")+view)
		w.WriteHeader(http.StatusNoContent)
		return nil
	}}
	d, err := New(Options{
		Host: fakeHost{begun: &r.calls}, Decider: r.decider, Repos: r.repos, Formats: func() []formats.Format { return []formats.Format{p, bare{}} },
		Label: func(_ context.Context, name string) { r.labels = append(r.labels, name) },
	})
	if err != nil {
		t.Fatal(err)
	}
	r.d, r.router = d, httpserver.NewClientRouter()
	d.Register(r.router)
	return r
}

func (r *rig) do(method, path string) *httptest.ResponseRecorder {
	r.t.Helper()
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	httpserver.Chain{Router: r.router}.Handler().ServeHTTP(rec, req)
	return rec
}

func TestR_ARCH_005_OnlyAFormatWithRoutesGetsAPrefix(t *testing.T) {
	r := newRig(t)
	if got := strings.Join(r.d.Prefixes(), ","); got != "probe" {
		t.Fatalf("prefixes = %s", got)
	}
	var patterns []string
	for _, p := range r.router.Patterns() {
		if strings.HasPrefix(p, "/api/") {
			patterns = append(patterns, p)
		}
	}
	want := "/api/probe/{projectKey}/{repoKey},/api/probe/{projectKey}/{repoKey}/{rest...}"
	if strings.Join(patterns, ",") != want {
		t.Fatalf("patterns = %v, want %s", patterns, want)
	}
	if rec := r.do("GET", "/api/bare/it/local/x"); rec.Code != 404 || rec.Header().Get("X-Probe-Error") != "" {
		t.Fatalf("a format without routes has no prefix: %d %v", rec.Code, rec.Header())
	}
}

func TestR_ARCH_005_TheRequestPathOfTheDispatcher(t *testing.T) {
	t.Run("a served request reaches the handler with the route, the repository and the params; the route is the label", func(t *testing.T) {
		r := newRig(t)
		if rec := r.do("GET", "/api/probe/it/local/left-pad"); rec.Code != 204 {
			t.Fatalf("answer %d", rec.Code)
		}
		if strings.Join(r.reached, ",") != "probe.doc.get it/local left-pad" || strings.Join(r.labels, ",") != "probe.doc.get" {
			t.Fatalf("reached %v, labels %v", r.reached, r.labels)
		}
		if got := r.decider.asked[0]; got.Repository != qr("local") || got.Kind != authz.KindMetadata || len(got.Actions) != 1 || got.Actions[0] != authz.Read || got.Path != "" || got.Package != "" {
			t.Fatalf("the coarse coordinate is the repository level: %+v", got)
		}
		if strings.Join(r.decider.begun, ",") != "local" {
			t.Fatalf("Begin made the key known: %v", r.decider.begun)
		}
	})
	t.Run("R-ARCH-005 an unclean path is 400 before the decision point is asked", func(t *testing.T) {
		r := newRig(t)
		for _, p := range []string{"/api/probe/it/local/%2e%2e", "/api/probe/it/local/a//b", "/api/probe/it/local/a/../b", "/api/probe/it/local//x"} {
			rec := r.do("GET", p)
			if rec.Code != 400 || rec.Header().Get("X-Probe-Error") != "invalid_path" {
				t.Errorf("%s: %d %v", p, rec.Code, rec.Header())
			}
		}
		if len(r.decider.asked) != 0 || len(r.repos.lookup) != 0 {
			t.Fatalf("asked %v, looked up %v", r.decider.asked, r.repos.lookup)
		}
	})
	t.Run("R-ARCH-011 a refusal of the coarse check comes before the repository lookup and before the handler", func(t *testing.T) {
		r := newRig(t)
		r.decider.decision = authz.Decision{Outcome: authz.DenyHidden}
		rec := r.do("GET", "/api/probe/it/local/left-pad")
		if rec.Code != 404 || rec.Header().Get("X-Probe-Error") != "not_found" {
			t.Fatalf("answer %d %v", rec.Code, rec.Header())
		}
		if len(r.repos.lookup) != 0 || len(r.reached) != 0 {
			t.Fatalf("looked up %v, reached %v", r.repos.lookup, r.reached)
		}
		r.decider.decision = authz.Decision{Outcome: authz.DenyVisible}
		if rec := r.do("PUT", "/api/probe/it/local/left-pad/-/x.tgz"); rec.Code != 403 {
			t.Fatalf("a write without deploy: %d", rec.Code)
		}
	})
	t.Run("an unknown route is 404 and a wrong method 405 with Allow, in the format's shape", func(t *testing.T) {
		r := newRig(t)
		if rec := r.do("GET", "/api/probe/it/local/a/b/c"); rec.Code != 404 || rec.Header().Get("X-Probe-Error") != "not_found" {
			t.Fatalf("answer %d %v", rec.Code, rec.Header())
		}
		rec := r.do("DELETE", "/api/probe/it/local/left-pad")
		if rec.Code != 405 || rec.Header().Get("Allow") != "GET,HEAD" {
			t.Fatalf("answer %d %v", rec.Code, rec.Header())
		}
	})
	t.Run("a key of the wrong shape is repository_not_found without a lookup", func(t *testing.T) {
		r := newRig(t)
		rec := r.do("GET", "/api/probe/it/NOPE/left-pad")
		if rec.Code != 404 || rec.Header().Get("X-Probe-Error") != "repository_not_found" || len(r.repos.lookup) != 0 {
			t.Fatalf("answer %d %v, looked up %v", rec.Code, rec.Header(), r.repos.lookup)
		}
	})
	t.Run("a repository of another format, a disabled one and one being deleted are not served", func(t *testing.T) {
		r := newRig(t)
		for key, want := range map[string]string{"other": "repository_not_found", "off": "repository_disabled", "dying": "repository_not_found", "ghost": "repository_not_found"} {
			rec := r.do("GET", "/api/probe/it/"+key+"/left-pad")
			if rec.Code != 404 || rec.Header().Get("X-Probe-Error") != want {
				t.Errorf("%s: %d %v, want %s", key, rec.Code, rec.Header(), want)
			}
		}
		if len(r.reached) != 0 {
			t.Fatalf("reached %v", r.reached)
		}
	})
	t.Run("a route the class does not serve is 405; a write to a virtual repository without a deploy target is no_deploy_target", func(t *testing.T) {
		r := newRig(t)
		rec := r.do("PUT", "/api/probe/it/remote/left-pad/-/x.tgz")
		if rec.Code != 405 || rec.Header().Get("X-Probe-Error") != "method_not_allowed" {
			t.Fatalf("remote: %d %v", rec.Code, rec.Header())
		}
		rec = r.do("PUT", "/api/probe/it/virtual/left-pad/-/x.tgz")
		if rec.Code != 405 || rec.Header().Get("X-Probe-Error") != "no_deploy_target" {
			t.Fatalf("virtual: %d %v", rec.Code, rec.Header())
		}
		if len(r.reached) != 0 {
			t.Fatalf("reached %v", r.reached)
		}
	})
}

func TestR_REPO_032_TheCacheViewIsTheRemoteRepositoryAddressedWithAMark(t *testing.T) {
	r := newRig(t)
	if rec := r.do("GET", "/api/probe/it/remote~cache/left-pad"); rec.Code != 204 {
		t.Fatalf("answer %d", rec.Code)
	}
	if strings.Join(r.reached, ",") != "probe.doc.get it/remote left-pad cache-view" {
		t.Fatalf("reached %v: the request is decided and served on the remote repository, marked as the cache view", r.reached)
	}
	if got := r.decider.asked[0].Repository; got != qr("remote") {
		t.Fatalf("the coarse check asked for %q", got)
	}
	r.reached = nil
	if rec := r.do("GET", "/api/probe/it/remote/left-pad"); rec.Code != 204 || strings.Join(r.reached, ",") != "probe.doc.get it/remote left-pad" {
		t.Fatalf("the plain address: %d %v", rec.Code, r.reached)
	}
	for _, key := range []string{"local~cache", "virt~cache", "ghost~cache", "~cache"} {
		rec := r.do("GET", "/api/probe/it/"+key+"/left-pad")
		if rec.Code != 404 || rec.Header().Get("X-Probe-Error") != "repository_not_found" {
			t.Errorf("%s: %d %v", key, rec.Code, rec.Header())
		}
	}
	// A write to the cache view is a write to the remote repository: its class refuses it.
	if rec := r.do("PUT", "/api/probe/it/remote~cache/left-pad/-/x.tgz"); rec.Code != 405 {
		t.Errorf("a write to the cache view: %d", rec.Code)
	}
}

func TestR_NPM_029_EveryKindOfWriteToAVirtualRepositoryGoesToItsDeployTarget(t *testing.T) {
	r := newRig(t)
	for _, c := range []struct{ method, path, route string }{
		{"PUT", "/api/probe/it/virtual-with-target/left-pad/-/x.tgz", "probe.file.put"},
		{"PUT", "/api/probe/it/virtual-with-target/-/tag/left-pad", "probe.tag.put"},
		{"DELETE", "/api/probe/it/virtual-with-target/left-pad/-rev/1-x", "probe.rev.delete"},
	} {
		r.reached = nil
		if rec := r.do(c.method, c.path); rec.Code != 204 {
			t.Errorf("%s %s: %d %v", c.method, c.path, rec.Code, rec.Header())
		}
		if len(r.reached) != 1 || !strings.HasPrefix(r.reached[0], c.route+" it/local ") {
			t.Errorf("%s %s reached %v, want %s on the target", c.method, c.path, r.reached, c.route)
		}
	}
	for _, c := range []struct{ method, path string }{
		{"PUT", "/api/probe/it/virtual/-/tag/left-pad"}, {"DELETE", "/api/probe/it/virtual/left-pad/-rev/1-x"},
	} {
		if rec := r.do(c.method, c.path); rec.Code != 405 || rec.Header().Get("X-Probe-Error") != "no_deploy_target" {
			t.Errorf("%s %s without a target: %d %v", c.method, c.path, rec.Code, rec.Header())
		}
	}
	// The refusal of a class names the class, so that a format words it itself.
	if rec := r.do("DELETE", "/api/probe/it/remote/left-pad/-rev/1-x"); rec.Code != 405 {
		t.Errorf("a delete on a remote repository: %d", rec.Code)
	}
}

// TestR_ARCH_023_TheChainsRefusalsOnAPrefixAreTheFormatsShape: the authentication
// stage, the host check and the in-flight limit render through the prefix's error
// shape; the same stage on another path keeps problem+json (Q-ARCH-214).
func TestR_ARCH_023_TheChainsRefusalsOnAPrefixAreTheFormatsShape(t *testing.T) {
	r := newRig(t)
	r.router.Handle("/api/v1/ping", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	h := httpserver.Chain{
		Router: r.router,
		Authenticate: func(*http.Request) (httpserver.Principal, error) {
			return httpserver.Principal{}, errs.E(errs.Unauthenticated, errs.CodeInvalidToken, "bad", errs.WithChallenge("Bearer"))
		},
	}.Handler()
	for path, wantShape := range map[string]bool{"/api/probe/it/local/left-pad": true, "/api/v1/ping": false} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 401 {
			t.Fatalf("%s: %d", path, rec.Code)
		}
		if got := rec.Header().Get("X-Probe-Error") == "invalid_token"; got != wantShape {
			t.Errorf("%s: format shape = %v, want %v (%v)", path, got, wantShape, rec.Header())
		}
		if !wantShape {
			var p map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || p["code"] != "invalid_token" {
				t.Errorf("%s: not a problem body: %s", path, rec.Body)
			}
		}
	}
	if len(r.reached) != 0 {
		t.Fatalf("a refused request reached %v", r.reached)
	}
}

// TestR_AUTH_002_ARefusalOfTheDecisionPointCarriesTheChallengeOfThePackageRoute: the decision point refuses a caller without a
// credential with the generic Bearer challenge; a format that passes the error's challenge on answers a package client with Basic,
// which it answers with its configured credentials, and a browser with Bearer (Q-AUTH-235).
func TestR_AUTH_002_ARefusalOfTheDecisionPointCarriesTheChallengeOfThePackageRoute(t *testing.T) {
	r := newRig(t)
	r.decider.decision = authz.Decision{Outcome: authz.DenyUnauthenticated, Reason: authz.ReasonUnauthenticated}
	for _, c := range []struct {
		name, fetch, want string
	}{
		{"a package client", "", `Basic realm="trove"`},
		{"a browser", "navigate", "Bearer"},
	} {
		t.Run("R-AUTH-002 "+c.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/probe/it/local/left-pad", nil)
			if c.fetch != "" {
				req.Header.Set("Sec-Fetch-Mode", c.fetch)
			}
			rec := httptest.NewRecorder()
			httpserver.Chain{Router: r.router}.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") != c.want {
				t.Fatalf("%d, WWW-Authenticate %q, want 401 with %q", rec.Code, rec.Header().Get("WWW-Authenticate"), c.want)
			}
		})
	}
	if len(r.reached) != 0 {
		t.Fatalf("a refused request reached %v", r.reached)
	}
}

func TestNewRefusesADispatcherWithoutItsParts(t *testing.T) {
	for name, o := range map[string]Options{
		"host":    {Decider: &fakeDecider{}, Repos: &fakeRepos{}},
		"decider": {Host: fakeHost{}, Repos: &fakeRepos{}},
		"repos":   {Host: fakeHost{}, Decider: &fakeDecider{}},
	} {
		if _, err := New(o); err == nil {
			t.Errorf("a dispatcher without %s", name)
		}
	}
}

// fakeCeiling answers the fingerprint and the view the dispatcher hands a format.
type fakeCeiling struct{ view *authz.ExposureView }

func (fakeCeiling) ExposureFingerprint(_ *authz.Index, endpoint string, repoKey qname.Repo) string {
	return "sha256:" + endpoint + "/" + repoKey.Key
}

func (c fakeCeiling) View(*authz.Index, string, qname.Repo) *authz.ExposureView { return c.view }

// TestR_EP_018_TheEndpointFactsCarryTheView: the view and the fingerprint a format
// receives come from the same ceiling and the same index; an endpoint that does not
// restrict hands no view, never a nil pointer in a non-nil interface.
func TestR_EP_018_TheEndpointFactsCarryTheView(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/probe/it/local/x", nil)
	access := func(t *testing.T, endpoint string) *edge.Access {
		t.Helper()
		a, err := edge.Resolve(req.Context(), &fakeDecider{}, authz.Subject{Name: "bot", Endpoint: endpoint}, qr("local"))
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	t.Run("R-EP-018 a restricting endpoint: the view and the fingerprint of the addressed repository", func(t *testing.T) {
		v := &authz.ExposureView{}
		d := &Dispatcher{o: Options{Ceiling: fakeCeiling{view: v}}}
		facts := d.endpointFacts(req, access(t, "customers"), qr("local"), qname.Repo{})
		if facts.Exposure != formats.ExposureView(v) || facts.ExposureFingerprint != "sha256:customers/local" || facts.Name != "customers" {
			t.Fatalf("facts %+v", facts)
		}
	})
	t.Run("R-EP-018 mode all: no view", func(t *testing.T) {
		d := &Dispatcher{o: Options{Ceiling: fakeCeiling{}}}
		if facts := d.endpointFacts(req, access(t, "default"), qr("local"), qname.Repo{}); facts.Exposure != nil {
			t.Fatalf("a view in mode all: %#v", facts.Exposure)
		}
	})
	t.Run("R-EP-018 no endpoint (a job's subject): no view and no fingerprint", func(t *testing.T) {
		d := &Dispatcher{o: Options{Ceiling: fakeCeiling{view: &authz.ExposureView{}}}}
		if facts := d.endpointFacts(req, access(t, ""), qr("local"), qname.Repo{}); facts.Exposure != nil || facts.ExposureFingerprint != "" {
			t.Fatalf("facts %+v", facts)
		}
	})
}

func TestR_ARCH_011_TheDispatcherBeginsTheRequestOnTheHostAndFinishesItOnEveryPath(t *testing.T) {
	r := newRig(t)
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/probe/it/local/left-pad"},          // served
		{"PUT", "/api/probe/it/remote/left-pad/-/x.tgz"}, // refused by the class
		{"GET", "/api/probe/it/local/a/b/c"},             // refused by the match: never begun
	} {
		r.do(c.method, c.path)
	}
	if began, finished := r.calls.began.Load(), r.calls.finished.Load(); began != 2 || finished != began {
		t.Fatalf("began %d, finished %d: the dispatcher begins a request after the checks and finishes every one it began", began, finished)
	}
}

func TestR_NUGET_001_ARefusalKnowsTheRepositoryTheClientAddressed(t *testing.T) {
	r := newRig(t)
	for path, want := range map[string]string{
		"/api/probe/it/local/a/b/c":      "/api/probe/it/local/",        // no route
		"/api/probe/it/remote~cache/x/y": "/api/probe/it/remote~cache/", // the cache view as addressed
		"/api/probe/it/ghost/left-pad":   "/api/probe/it/ghost/",        // an unknown key of a valid shape
	} {
		rec := r.do("GET", path)
		if got := rec.Header().Get("X-Probe-Registry"); got != want {
			t.Errorf("%s: registry %q, want %q (%d)", path, got, want, rec.Code)
		}
	}
	if rec := r.do("GET", "/api/probe/it/NOPE/left-pad"); rec.Header().Get("X-Probe-Registry") != "" {
		t.Errorf("a key of the wrong shape is no repository to link to: %q", rec.Header().Get("X-Probe-Registry"))
	}
}

func TestR_NUGET_001_ARepositoryOfAnotherFormatSaysSo(t *testing.T) {
	r := newRig(t)
	rec := r.do("GET", "/api/probe/it/other/left-pad")
	if rec.Code != 404 || rec.Header().Get("X-Probe-Error") != "repository_not_found" {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	if got := rec.Header().Get("X-Probe-Detail"); got != "The repository is not a probe repository." {
		t.Fatalf("detail %q", got)
	}
	if got := r.do("GET", "/api/probe/it/ghost/left-pad").Header().Get("X-Probe-Detail"); got == "The repository is not a probe repository." {
		t.Fatal("a missing repository says it is of another format")
	}
}

func (r *fakeRepos) ListProject(context.Context, string) ([]repository.Repository, error) {
	return nil, nil
}

// rooted is a format with routes that declares the registry root: its address is /v2/<project>/<key>/, so the dispatcher gives it no
// prefix below /api/ (Q-OCI-4). Its mount at /v2/ is the work package of the registry root.
type rooted struct{ probe }

func (rooted) Descriptor() formats.Descriptor {
	d := probe{}.Descriptor()
	d.ID, d.Layout, d.ProtocolBasePath, d.RegistryRoot = "rooted", "rooted", catalog.RegistryRootBasePath, true
	return d
}

func TestR_ARCH_005_TheRegistryRootFormatGetsNoPrefixBelowAPI(t *testing.T) {
	r := newRig(t)
	d, err := New(Options{
		Host: fakeHost{begun: &r.calls}, Decider: r.decider, Repos: r.repos,
		Formats: func() []formats.Format { return []formats.Format{probe{}, rooted{}} },
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(d.Prefixes(), ","); got != "probe" {
		t.Fatalf("prefixes = %s", got)
	}
}

// TestR_AUTHZ_034_ARouteThatServesOnlyWikiContentIsDecidedWithThePartWiki: the coarse check of the dispatcher asks the part of
// the route, so a caller who reads the artifacts and not the wiki is refused before the handler runs (R-AUTHZ-034).
func TestR_AUTHZ_034_ARouteThatServesOnlyWikiContentIsDecidedWithThePartWiki(t *testing.T) {
	r := newRig(t)
	r.decider.decision = authz.Decision{Outcome: authz.Allow}
	for _, c := range []struct {
		path string
		part authz.Part
	}{
		{"/api/probe/it/local/left-pad", authz.PartArtifacts},
		{"/api/probe/it/local/left-pad/-/readme", authz.PartWiki},
	} {
		r.decider.asked = nil
		if rec := r.do("GET", c.path); rec.Code != 204 {
			t.Fatalf("%s: %d", c.path, rec.Code)
		}
		if len(r.decider.asked) == 0 || r.decider.asked[0].Part != c.part {
			t.Errorf("%s was decided with %+v, want the part %v", c.path, r.decider.asked, c.part)
		}
	}
	r.decider.decision = authz.Decision{Outcome: authz.DenyHidden, Reason: authz.ReasonWiki}
	r.reached = nil
	if rec := r.do("GET", "/api/probe/it/local/left-pad/-/readme"); rec.Code != 404 || len(r.reached) != 0 {
		t.Errorf("a refused wiki read: %d, handler reached %v", rec.Code, r.reached)
	}
}
