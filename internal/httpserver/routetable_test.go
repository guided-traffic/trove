package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRouteTable(t *testing.T) {
	noop := func(http.ResponseWriter, *http.Request) {}

	t.Run("R-HA-041 the fixed rows alone pass", func(t *testing.T) {
		if v := CheckRouteTable(NewClientRouter().Patterns(), nil); len(v) != 0 {
			t.Fatalf("violations: %v", v)
		}
	})

	t.Run("R-HA-041 the planned client routes pass", func(t *testing.T) {
		r := NewClientRouter()
		for _, p := range []string{
			"/api/v1/", "GET /api/v1/system/ping", "/api/npm/{repoKey}/{rest...}", "/files/{repoKey}/{path...}",
			"/auth/", "/ui/", "POST /-/npm/v1/oidc/token/exchange/package/{name...}",
		} {
			r.HandleFunc(p, noop)
		}
		if v := CheckRouteTable(r.Patterns(), nil); len(v) != 0 {
			t.Fatalf("violations: %v", v)
		}
	})

	t.Run("R-HA-041 a fixture plug-in that registers GET /v2/x fails the test and passes again once removed", func(t *testing.T) {
		r := NewClientRouter()
		r.HandleFunc("GET /v2/x", noop) // ServeMux accepts it: /v2/x does not conflict with anything
		v := CheckRouteTable(r.Patterns(), nil)
		if len(v) != 1 || !strings.Contains(v[0], "GET /v2/x") {
			t.Fatalf("violations: %v", v)
		}
		if v := CheckRouteTable(NewClientRouter().Patterns(), nil); len(v) != 0 {
			t.Fatalf("after removing the fixture: %v", v)
		}
	})

	t.Run("R-HA-041 every spelling below /v2 is caught, whatever method or host prefix", func(t *testing.T) {
		for _, p := range []string{"/v2", "/v2/", "GET /v2/x", "/v2/{name...}", "PUT example.com/v2/x", "example.com/v2/", "HEAD /v2/{name}/manifests/{ref}"} {
			if v := CheckRouteTable([]string{p}, nil); len(v) != 1 {
				t.Errorf("%q: violations %v", p, v)
			}
		}
	})

	t.Run("R-HA-041 a wildcard as the first segment is caught because it matches below /v2/", func(t *testing.T) {
		for _, p := range []string{"/{seg}/x", "GET /{seg}", "/{rest...}", "POST host.example/{a}/b"} {
			if v := CheckRouteTable([]string{p}, nil); len(v) != 1 {
				t.Errorf("%q: violations %v", p, v)
			}
		}
	})

	t.Run("R-HA-041 the OCI format's own patterns are exempt", func(t *testing.T) {
		if v := CheckRouteTable([]string{"/v2/", "GET /v2/{name...}"}, []string{"/v2/", "GET /v2/{name...}"}); len(v) != 0 {
			t.Fatalf("violations: %v", v)
		}
	})

	t.Run("R-HA-041 names that merely start with v2 are not reserved", func(t *testing.T) {
		if v := CheckRouteTable([]string{"/v2x/", "/v20/y", "/api/v2/"}, nil); len(v) != 0 {
			t.Fatalf("violations: %v", v)
		}
	})

	t.Run("R-HA-041 /v2/ and everything below it answer 404 with a problem body", func(t *testing.T) {
		h := Chain{Router: NewClientRouter()}.Handler()
		for _, path := range []string{"/v2/", "/v2", "/v2/docker-all/manifests/latest"} {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
			if w.Code != 404 {
				t.Errorf("%s: %d", path, w.Code)
				continue
			}
			if p := problemOf(t, w); p.Code != "not_found" {
				t.Errorf("%s: %+v", path, p)
			}
		}
	})
}
