package app

import (
	"fmt"
	"net/http"
	"testing"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/httpserver"
)

func TestAPISurfaceRouteCheck(t *testing.T) {
	doc, err := v1.LoadDocument([]byte(`openapi: 3.1.0
info: {title: t, version: "1"}
paths:
  /api/v1/system/ping:
    get:
      operationId: getPing
      x-trove-permission: {public: true}
      responses: {"200": {description: ok}}
  /api/v1/auth/oidc/token:
    post:
      operationId: exchangeToken
      x-trove-permission: {public: true}
      responses: {"200": {description: ok}}
`))
	if err != nil {
		t.Fatal(err)
	}
	router := func(t *testing.T, surface ...string) *httpserver.Router {
		ep, err := httpserver.NewEndpoint(config.ResolvedEndpoint{Name: "default", Default: true, Surface: surface}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return httpserver.NewEndpointRouter(ep)
	}
	ok := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})

	t.Run("R-EP-002 CR2-4 a surface with api and without ci_exchange passes the route check", func(t *testing.T) {
		r := router(t, config.SurfaceUI, config.SurfaceAPI, config.SurfacePackages, config.SurfaceFiles)
		r.Handle("GET /api/v1/system/ping", ok)
		r.Handle("POST /api/v1/auth/oidc/token", ok)
		if len(r.Dropped()) == 0 {
			t.Fatal("setup: the surface must drop the exchange route")
		}
		if err := checkAPIRoutes(doc, r); err != nil {
			t.Errorf("a valid surface fails the route check: %v", err)
		}
	})

	t.Run("R-EP-002 CR2-4 a documented operation nobody registered still stops start-up", func(t *testing.T) {
		r := router(t, config.SurfaceUI, config.SurfaceAPI)
		r.Handle("GET /api/v1/system/ping", ok)
		if err := checkAPIRoutes(doc, r); err == nil {
			t.Error("a missing route must be reported")
		}
	})
}

func TestBootstrapAnonymousReadOnlyOnDefaultEndpoint(t *testing.T) {
	t.Run("R-EP-010 SA3 bootstrap anonymous read opens system.read on the default endpoint and not on a further one", func(t *testing.T) {
		cust := freePort(t)
		h := startEndpoints(t, "web", map[string]string{
			"TROVE_SYSTEM_BOOTSTRAP_ANONYMOUS_READ": "true",
			"TROVE_SERVER_BASE_URL":                 "http://trove.example.com",
			"TROVE_SERVER_ENDPOINTS": fmt.Sprintf(
				`{"customers":{"listen":%q,"base_url":"http://packages.example.com","surface":["api","packages"],"auth":{"anonymous":true}}}`, cust),
		})
		def := h.app.EndpointAddr("default")
		if code, body := hostGet(t, def, "trove.example.com", "/api/v1/system/replicas"); code != 200 {
			t.Fatalf("default endpoint: %d %s", code, body)
		}
		if code, body := hostGet(t, cust, "packages.example.com", "/api/v1/system/replicas"); code != 401 {
			t.Fatalf("further endpoint: %d %s, want 401", code, body)
		}
	})
}
