package v1

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// TestR_API_018_PreflightIsAnsweredBeforeAuthentication: the chain refuses every
// request without credentials, and still answers the credential-less preflight of
// an allowed origin; a request that is not such a preflight stays refused.
func TestR_API_018_PreflightIsAnsweredBeforeAuthentication(t *testing.T) {
	router := httpserver.NewClientRouter()
	router.HandleFunc("GET /api/v1/x", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	res, err := httpserver.NewResolver(nil, "http://trove.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	h := httpserver.Chain{
		Router: router, Resolver: res,
		Authenticate: func(*http.Request) (httpserver.Principal, error) {
			return httpserver.Principal{}, errs.E(errs.Unauthenticated, errs.CodeUnauthenticated, "credentials are required")
		},
		Preflight: Preflight([]string{"https://portal.example.com"}),
	}.Handler()
	do := func(method, origin string, preflight bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1/x", nil)
		r.Header.Set("Origin", origin)
		if preflight {
			r.Header.Set("Access-Control-Request-Method", "GET")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	if rec := do("OPTIONS", "https://portal.example.com", true); rec.Code != 204 || rec.Header().Get("Access-Control-Allow-Origin") != "https://portal.example.com" {
		t.Fatalf("preflight: %d %v", rec.Code, rec.Header())
	}
	if rec := do("OPTIONS", "https://unlisted.example", true); rec.Code != 401 || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("unlisted origin: %d %v", rec.Code, rec.Header())
	}
	if rec := do("GET", "https://portal.example.com", false); rec.Code != 401 {
		t.Fatalf("a request that is no preflight opened: %d", rec.Code)
	}
}
