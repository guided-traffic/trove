package dispatch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
)

// rootProbe is a format that declares the registry root; its handler records what the dispatcher tells it of the endpoint.
type rootProbe struct {
	formats.Base
	seen *[]formats.EndpointFacts
}

func (rootProbe) Descriptor() formats.Descriptor {
	return formats.Descriptor{
		Facts: catalog.Facts{
			ID: "rootprobe", Classes: []formats.Class{formats.Local}, Layout: "rootprobe", RegistryRoot: true,
			ProtocolBasePath: catalog.RegistryRootBasePath, PatternSubject: formats.SubjectName,
		},
		ContractVersion: formats.ContractVersion,
	}
}

func (p rootProbe) Routes() []formats.Route {
	return []formats.Route{{
		Name: "rootprobe.get", Method: http.MethodGet, Template: "/{package}/get", Examples: []string{"app/get"}, PackageParam: "package",
		Action: formats.ActionRead, Kind: formats.Metadata,
		Handler: func(_ context.Context, rq *formats.Request, w http.ResponseWriter) error {
			*p.seen = append(*p.seen, rq.Endpoint)
			w.WriteHeader(http.StatusNoContent)
			return nil
		},
	}}
}

func (p rootProbe) Match(m, path string) (*formats.Route, formats.Params, error) {
	return formats.MatchRoutes(p.Routes(), m, path)
}

func (rootProbe) Classify(formats.Path) formats.PathInfo {
	return formats.PathInfo{Kind: formats.Content}
}

func (rootProbe) PackageDocumentPath(string) (formats.Path, bool) { return formats.Path{}, false }

func (rootProbe) WriteError(w http.ResponseWriter, _ *http.Request, _ error) {
	w.WriteHeader(http.StatusTeapot)
}

// EndpointFacts.Bound is the match's reading of the address (R-OCI-029): true for a registry request on a host bound to a
// repository of the format, false on an unbound host, false when the binding names a repository of another format (it behaves as no
// binding, as the match and the enforcement point read it) and false below /api/<format>/.
func TestEndpointFactsBoundIsTheMatchsReading(t *testing.T) {
	var seen []formats.EndpointFacts
	root := qname.Repo{Project: "web", Key: "root"}
	other := qname.Repo{Project: "web", Key: "other"}
	repos := &fakeRepos{byKey: map[qname.Repo]repository.Repository{
		root:                          {ID: "1", Name: root, Class: repository.Local, Format: "rootprobe"},
		other:                         {ID: "2", Name: other, Class: repository.Local, Format: "probe"},
		qr("local"):                   {ID: "3", Name: qr("local"), Class: repository.Local, Format: "probe"},
		{Project: "web", Key: "virt"}: {ID: "5", Name: qname.Repo{Project: "web", Key: "virt"}, Class: repository.Virtual, Format: "rootprobe"},
		{Project: "web", Key: "us"}:   {ID: "4", Name: qname.Repo{Project: "web", Key: "us"}, Class: repository.Local, Format: "rootprobe"},
	}}
	probeSeen := false
	p := probe{handler: func(_ context.Context, rq *formats.Request, w http.ResponseWriter) error {
		probeSeen = rq.Endpoint.Bound
		w.WriteHeader(http.StatusNoContent)
		return nil
	}}
	d, err := New(Options{
		Host: fakeHost{}, Decider: &fakeDecider{}, Repos: repos,
		Formats: func() []formats.Format { return []formats.Format{rootProbe{seen: &seen}, p} },
	})
	if err != nil {
		t.Fatal(err)
	}
	h, err := d.RegistryHandler(CoreRoutes{Version: http.NotFoundHandler(), Catalog: http.NotFoundHandler(), Token: http.NotFoundHandler()})
	if err != nil {
		t.Fatal(err)
	}
	serve := func(handler http.Handler, path string, bound qname.Repo) int {
		req := httptest.NewRequestWithContext(context.Background(), "GET", path, nil)
		if !bound.IsZero() {
			req = req.WithContext(httpserver.WithResolved(req.Context(), httpserver.RequestInfo{RootRepository: bound}))
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}
	for name, c := range map[string]struct {
		path  string
		bound qname.Repo
		want  bool
	}{
		"a bound host of the format":                     {"/v2/app/get", root, true},
		"an unbound host":                                {"/v2/web/us/app/get", qname.Repo{}, false},
		"a host bound to a repository of another format": {"/v2/web/us/app/get", other, false},
		"a host bound to a virtual repository":           {"/v2/app/get", qname.Repo{Project: "web", Key: "virt"}, true},
		"a host bound to a missing repository":           {"/v2/web/us/app/get", qname.Repo{Project: "web", Key: "ghost"}, false},
	} {
		seen = nil
		if code := serve(h, c.path, c.bound); code != http.StatusNoContent || len(seen) != 1 {
			t.Fatalf("%s: %d, handler calls %d", name, code, len(seen))
		}
		if seen[0].Bound != c.want {
			t.Errorf("%s: Endpoint.Bound = %v, want %v", name, seen[0].Bound, c.want)
		}
		// BoundRepo is the repository the host is bound to as the match read it (for a virtual one, the virtual repository, which a write is
		// redirected away from), and the zero value when the host is not bound for the registry.
		wantRepo := qname.Repo{}
		if c.want {
			wantRepo = c.bound
		}
		if seen[0].BoundRepo != wantRepo {
			t.Errorf("%s: Endpoint.BoundRepo = %v, want %v", name, seen[0].BoundRepo, wantRepo)
		}
	}
	// A request below /api/<format>/ on a bound host is never bound: the binding belongs to the registry root.
	probeSeen = true
	r := httptest.NewRequestWithContext(context.Background(), "GET", "/api/probe/it/local/left-pad", nil)
	r = r.WithContext(httpserver.WithResolved(r.Context(), httpserver.RequestInfo{RootRepository: root}))
	rec := httptest.NewRecorder()
	d.ServeHTTP(rec, r)
	if rec.Code != http.StatusNoContent || probeSeen {
		t.Errorf("/api/ request: %d, Endpoint.Bound = %v", rec.Code, probeSeen)
	}
}
