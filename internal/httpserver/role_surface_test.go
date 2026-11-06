package httpserver

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/guided-traffic/trove/internal/config"
)

func roleEndpoint(t *testing.T, surface []string, role string) *Endpoint {
	t.Helper()
	e, err := NewEndpoint(config.ResolvedEndpoint{Name: "default", Default: true, Surface: surface}, nil, ForRoleFamilies(config.RoleFamilies(role)))
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// TestQ_ARCH_298_TheEffectiveSurfaceIsTheSurfaceIntersectedWithTheRole covers the intersection of an endpoint's configured
// surface with the families of the process's role, the listener rule's input (R-EP-002, R-EP-005).
func TestQ_ARCH_298_TheEffectiveSurfaceIsTheSurfaceIntersectedWithTheRole(t *testing.T) {
	all := config.Surfaces()
	for name, tc := range map[string]struct {
		surface []string
		role    string
		want    []string
	}{
		"default on api":            {all, "api", []string{"ui", "api", "ci_exchange"}},
		"default on packages":       {all, "packages", []string{"packages", "files"}},
		"default on web":            {all, "web", all},
		"customers on api":          {[]string{"packages", "files", "api"}, "api", []string{"api"}},
		"customers on packages":     {[]string{"packages", "files", "api"}, "packages", []string{"packages", "files"}},
		"a packages endpoint, api":  {[]string{"packages"}, "api", nil},
		"an api endpoint, packages": {[]string{"api", "ci_exchange"}, "packages", nil},
		"any endpoint, worker":      {all, "worker", nil},
	} {
		t.Run("R-EP-005 "+name, func(t *testing.T) {
			e := roleEndpoint(t, tc.surface, tc.role)
			if got := e.Surface(); !slices.Equal(got, tc.want) {
				t.Errorf("surface = %v, want %v", got, tc.want)
			}
			if got := e.ConfiguredSurface(); !slices.Equal(got, tc.surface) {
				t.Errorf("configured surface = %v, want %v", got, tc.surface)
			}
		})
	}
}

// TestQ_ARCH_302_ServesRequestFollowsTheEffectiveSurface: the question the authentication stage asks for a request of a family
// the process does not serve.
func TestQ_ARCH_302_ServesRequestFollowsTheEffectiveSurface(t *testing.T) {
	api := roleEndpoint(t, config.Surfaces(), "api")
	packages := roleEndpoint(t, config.Surfaces(), "packages")
	for path, want := range map[string]struct{ api, packages bool }{
		"/":                               {true, false},
		"/ui/":                            {true, false},
		"/auth/oidc/corp/start":           {true, false},
		"/api/v1/system/ping":             {true, false},
		"/api/v1/auth/oidc/token":         {true, false},
		"/-/npm/v1/oidc/token/exchange/x": {true, false},
		"/files/it/generic-local/x":       {false, true},
		"/api/npm/it/npm-local/lodash":    {false, true},
		"/api/nuget/it/nuget-local/v3/index.json": {false, true},
		"/v2/":            {false, true},
		"/.well-known/x":  {false, true},
		"/-/v1/login":     {false, true},
		"/does-not-exist": {true, true}, // no family: the router decides
		"/metrics":        {true, true},
	} {
		if got := api.ServesRequest(path); got != want.api {
			t.Errorf("api ServesRequest(%s) = %v, want %v", path, got, want.api)
		}
		if got := packages.ServesRequest(path); got != want.packages {
			t.Errorf("packages ServesRequest(%s) = %v, want %v", path, got, want.packages)
		}
	}
	var nilEndpoint *Endpoint
	if nilEndpoint.ServesRequest("/ui/") {
		t.Error("a nil endpoint serves a family")
	}
}

// TestR_EP_005_ARoleNarrowedRouterAnswersTheOtherFamiliesLikeAnUnknownPath: through the router, the families outside the
// effective surface fall through to the catch-all, byte-identical apart from the request ID (R-ARCH-005).
func TestR_EP_005_ARoleNarrowedRouterAnswersTheOtherFamiliesLikeAnUnknownPath(t *testing.T) {
	for _, role := range []string{"api", "packages"} {
		e := roleEndpoint(t, config.Surfaces(), role)
		router := NewEndpointRouter(e)
		for _, p := range []string{
			"GET /ui/", "GET /api/v1/system/ping", "POST /api/v1/auth/oidc/token", "GET /files/{repo}/{path...}", "GET /api/npm/{key}/{rest...}",
		} {
			router.Handle(p, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }))
		}
		serve := func(method, path string) *httptest.ResponseRecorder {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
			return rec
		}
		unknown := serve("GET", "/does-not-exist")
		if unknown.Code != http.StatusNotFound {
			t.Fatalf("%s: unknown path %d", role, unknown.Code)
		}
		other := map[string][][2]string{
			"api":      {{"GET", "/files/it/rr/x"}, {"PUT", "/files/it/rr/x"}, {"GET", "/api/npm/it/k/lodash"}},
			"packages": {{"GET", "/ui/"}, {"GET", "/"}, {"GET", "/api/v1/system/ping"}, {"POST", "/api/v1/auth/oidc/token"}},
		}[role]
		for _, p := range other {
			got := serve(p[0], p[1])
			if got.Code != unknown.Code || got.Header().Get("Content-Type") != unknown.Header().Get("Content-Type") {
				t.Errorf("%s %s %s: %d %v; the unknown path %d %v", role, p[0], p[1], got.Code, got.Header(), unknown.Code, unknown.Header())
			}
		}
		own := map[string][][2]string{
			"api":      {{"GET", "/ui/"}, {"GET", "/api/v1/system/ping"}},
			"packages": {{"GET", "/files/it/rr/x"}, {"GET", "/api/npm/it/k/lodash"}},
		}[role]
		for _, p := range own {
			if got := serve(p[0], p[1]); got.Code != http.StatusTeapot {
				t.Errorf("%s %s %s: %d, want the handler's", role, p[0], p[1], got.Code)
			}
		}
	}
}
