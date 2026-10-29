package app

import (
	"net/http"
	"slices"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/oidc"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/httpserver"
)

func TestLoginRoutes(t *testing.T) {
	routes := []string{oidc.RouteStart, oidc.RouteCallback, oidc.RouteLogout, oidc.RouteLocalLogin}
	ok := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	router := func(t *testing.T, surface ...string) *httpserver.Router {
		ep, err := httpserver.NewEndpoint(config.ResolvedEndpoint{Name: "default", Default: true, Surface: surface}, nil)
		if err != nil {
			t.Fatal(err)
		}
		r := httpserver.NewEndpointRouter(ep)
		for _, p := range routes {
			r.Handle(p, ok)
		}
		if v := httpserver.CheckSurface(r.Patterns(), ep); len(v) > 0 {
			t.Fatalf("CheckSurface: %v", v)
		}
		return r
	}

	t.Run("R-AUTH-005 R-EP-005 the login routes pass the route-table and surface checks of the default endpoint", func(t *testing.T) {
		r := router(t, config.SurfaceUI, config.SurfaceAPI, config.SurfacePackages, config.SurfaceFiles)
		if v := httpserver.CheckRouteTable(r.Patterns(), nil); len(v) > 0 {
			t.Fatalf("CheckRouteTable: %v", v)
		}
		for _, p := range routes {
			if !slices.Contains(r.Patterns(), p) {
				t.Errorf("%s is not registered", p)
			}
			if httpserver.FamilyOf(p) != config.SurfaceUI {
				t.Errorf("%s belongs to %s, want ui", p, httpserver.FamilyOf(p))
			}
		}
	})

	t.Run("R-EP-005 an endpoint without the ui surface drops the login routes", func(t *testing.T) {
		r := router(t, config.SurfaceAPI, config.SurfacePackages)
		for _, p := range routes {
			if slices.Contains(r.Patterns(), p) || !slices.Contains(r.Dropped(), p) {
				t.Errorf("%s is served on an endpoint without ui", p)
			}
		}
	})

	t.Run("R-AUTH-013 GET /auth/logout answers 405 so that links and image tags cannot log out", func(t *testing.T) {
		r := router(t, config.SurfaceUI, config.SurfaceAPI)
		rec := newRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/auth/logout", nil)
		r.ServeHTTP(rec, req)
		if rec.code != http.StatusMethodNotAllowed || rec.h.Get("Allow") != "POST" {
			t.Fatalf("GET /auth/logout: %d, Allow %q", rec.code, rec.h.Get("Allow"))
		}
	})
}

// recorder is a minimal ResponseWriter for router tests.
type recorder struct {
	h    http.Header
	code int
}

func newRecorder() *recorder                    { return &recorder{h: http.Header{}} }
func (r *recorder) Header() http.Header         { return r.h }
func (r *recorder) Write(b []byte) (int, error) { return len(b), nil }
func (r *recorder) WriteHeader(code int)        { r.code = code }
