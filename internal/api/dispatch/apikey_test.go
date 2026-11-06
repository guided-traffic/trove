package dispatch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/repository"
)

// keyed is a format whose push, delete and relist routes declare the API-key header,
// the way NuGet's publish resource does (R-NUGET-012), beside a read on the relist path
// and a write that does not declare it.
type keyed struct{ formats.Base }

func (keyed) Descriptor() formats.Descriptor {
	return formats.Descriptor{
		Facts: catalog.Facts{
			ID: "keyed", Classes: []formats.Class{formats.Local, formats.Remote, formats.Virtual}, Layout: "keyed",
			ProtocolBasePath: "/api/keyed/{key}/", PatternSubject: formats.SubjectPath,
		},
		ContractVersion: formats.ContractVersion,
	}
}

func (keyed) Routes() []formats.Route {
	h := func(_ context.Context, _ *formats.Request, w http.ResponseWriter) error {
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	return []formats.Route{
		{
			Name: "keyed.index.get", Method: http.MethodGet, Template: "/index.json", Examples: []string{"index.json"},
			Action: formats.ActionRead, Kind: formats.Metadata, Handler: h,
		},
		{
			Name: "keyed.package.put", Method: http.MethodPut, Template: "/package", Examples: []string{"package"},
			Action: formats.ActionDeploy, Kind: formats.Upload, Classes: []formats.Class{formats.Local}, Handler: h, APIKey: true,
		},
		{
			Name: "keyed.version.get", Method: http.MethodGet, Template: "/package/{id}/{version}", Examples: []string{"package/a/1.0.0"},
			Action: formats.ActionRead, Kind: formats.Metadata, Handler: h,
		},
		{
			Name: "keyed.version.delete", Method: http.MethodDelete, Template: "/package/{id}/{version}", Examples: []string{"package/a/1.0.0"},
			Action: formats.ActionDelete, Kind: formats.Delete, Classes: []formats.Class{formats.Local}, Handler: h, APIKey: true,
		},
		{
			Name: "keyed.version.relist", Method: http.MethodPost, Template: "/package/{id}/{version}", Examples: []string{"package/a/1.0.0"},
			Action: formats.ActionDeploy, Kind: formats.Admin, Classes: []formats.Class{formats.Local}, Handler: h, APIKey: true,
		},
		{
			Name: "keyed.other.put", Method: http.MethodPut, Template: "/other/{name}", Examples: []string{"other/x"},
			Action: formats.ActionDeploy, Kind: formats.Upload, Classes: []formats.Class{formats.Local}, Handler: h,
		},
	}
}

func (k keyed) Match(m, p string) (*formats.Route, formats.Params, error) {
	return formats.MatchRoutes(k.Routes(), m, p)
}

func (keyed) Classify(formats.Path) formats.PathInfo { return formats.PathInfo{Kind: formats.Content} }

func (keyed) PackageDocumentPath(string) (formats.Path, bool) { return formats.Path{}, false }

// keyedRoutes are the names of the routes of keyed that declare the API key.
var keyedRoutes = []string{"keyed.package.put", "keyed.version.delete", "keyed.version.relist"}

// TestR_NUGET_012_TheDispatcherNamesTheRoutesThatTakeTheAPIKey is the seam of
// Q-AUTH-186: the authentication stage, which runs before the dispatcher, learns from
// the dispatcher whether a request reaches a route that declares the API-key header,
// and the answer is the route the dispatcher then serves. A path the dispatcher
// refuses before the match, a route without the declaration, a format without such a
// route and a path outside the format prefixes are no API-key route.
func TestR_NUGET_012_TheDispatcherNamesTheRoutesThatTakeTheAPIKey(t *testing.T) {
	var labels []string
	repos := &fakeRepos{byKey: map[string]repository.Repository{
		"local":  {ID: "1", Name: "local", Class: repository.Local, Format: "keyed"},
		"remote": {ID: "2", Name: "remote", Class: repository.Remote, Format: "keyed"},
		"virt":   {ID: "3", Name: "virt", Class: repository.Virtual, Format: "keyed"},
	}}
	d, err := New(Options{
		Host: fakeHost{}, Decider: &fakeDecider{}, Repos: repos,
		Formats: func() []formats.Format { return []formats.Format{keyed{}, probe{}} },
		Label:   func(_ context.Context, name string) { labels = append(labels, name) },
	})
	if err != nil {
		t.Fatal(err)
	}
	router := httpserver.NewClientRouter()
	d.Register(router)
	var asked []bool
	chain := httpserver.Chain{Router: router, Authenticate: func(r *http.Request) (httpserver.Principal, error) {
		// The stage's position: stage 6 of the chain, before the router.
		asked = append(asked, d.APIKeyRoute(r))
		return httpserver.AnonymousPrincipal(), nil
	}}.Handler()

	for _, c := range []struct {
		method, path string
		want         bool
	}{
		{"PUT", "/api/keyed/local/package", true},
		{"DELETE", "/api/keyed/local/package/Acme.Widgets/1.0.0", true},
		{"POST", "/api/keyed/local/package/Acme.Widgets/1.0.0", true},
		// The route is the format's, whatever the class: a push to a virtual
		// repository goes to its deploy target (R-NUGET-019) and takes the key there.
		{"PUT", "/api/keyed/virt/package", true},
		{"PUT", "/api/keyed/remote~cache/package", true},
		// The same path with a method of a route that does not declare it.
		{"GET", "/api/keyed/local/package/Acme.Widgets/1.0.0", false},
		{"HEAD", "/api/keyed/local/package/Acme.Widgets/1.0.0", false},
		{"GET", "/api/keyed/local/index.json", false},
		{"PUT", "/api/keyed/local/other/x", false},
		// Refused by the dispatcher before the match: a key of no valid shape, an empty
		// segment, a dot segment.
		{"PUT", "/api/keyed/NOPE/package", false},
		{"PUT", "/api/keyed/local//package", false},
		{"PUT", "/api/keyed/local/x/%2e%2e/package", false},
		// Passed by the dispatcher and refused by the match of the fixture's format: its
		// routes match a trailing slash strictly, and none takes PATCH (405).
		{"PUT", "/api/keyed/local/package/", false},
		{"PATCH", "/api/keyed/local/package", false},
		// A format without an API-key route, one without routes, and paths outside the prefixes.
		{"PUT", "/api/probe/local/left-pad/-/x.tgz", false},
		{"PUT", "/api/nosuch/local/package", false},
		{"PUT", "/files/local/package", false},
		{"POST", "/api/v1/tokens", false},
	} {
		labels, asked = nil, nil
		chain.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(c.method, c.path, nil))
		if len(asked) != 1 || asked[0] != c.want {
			t.Errorf("%s %s: APIKeyRoute %v, want %v", c.method, c.path, asked, c.want)
		}
		reached := len(labels) == 1 && slices.Contains(keyedRoutes, labels[0])
		if reached != c.want {
			t.Errorf("%s %s: the dispatcher matched %v; the stage was told %v", c.method, c.path, labels, c.want)
		}
	}
}

// TestR_NUGET_012_AnEndpointWithoutThePackageSurfaceHasNoAPIKeyRoute: the router of an
// endpoint whose surface does not hold packages has no format prefix (R-EP-002), so the
// header is no credential there either.
func TestR_NUGET_012_AnEndpointWithoutThePackageSurfaceHasNoAPIKeyRoute(t *testing.T) {
	d, err := New(Options{
		Host: fakeHost{}, Decider: &fakeDecider{}, Repos: &fakeRepos{},
		Formats: func() []formats.Format { return []formats.Format{keyed{}} },
	})
	if err != nil {
		t.Fatal(err)
	}
	for surface, want := range map[string]bool{config.SurfacePackages: true, config.SurfaceFiles: false} {
		ep, err := httpserver.NewEndpoint(config.ResolvedEndpoint{Name: "e", Surface: []string{surface}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		var got []bool
		router := httpserver.NewEndpointRouter(ep)
		d.Register(router)
		httpserver.Chain{Router: router, Endpoint: ep, Authenticate: func(r *http.Request) (httpserver.Principal, error) {
			got = append(got, d.APIKeyRoute(r))
			return httpserver.AnonymousPrincipal(), nil
		}}.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("PUT", "/api/keyed/local/package", nil))
		if len(got) != 1 || got[0] != want {
			t.Errorf("surface %s: APIKeyRoute %v, want %v", surface, got, want)
		}
	}
}
