package v1

import (
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// roleChain is the chain of the default endpoint of a role with the stages of this package at their places: the preflight
// before the authentication, the edge stage (headers, CORS, the general rate limits) at stage 7. The handlers answer 200,
// so a route of a served family is told apart from the router's 404.
func roleChain(t *testing.T, role string, token bool) http.Handler {
	t.Helper()
	ep, err := httpserver.NewEndpoint(config.ResolvedEndpoint{Name: "default", Default: true, Surface: config.Surfaces(), BaseURL: "http://trove.test"},
		nil, httpserver.ForRoleFamilies(config.RoleFamilies(role)))
	if err != nil {
		t.Fatal(err)
	}
	router := httpserver.NewEndpointRouter(ep)
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }
	for _, p := range []string{"/api/v1/system/ping", "/files/{key}/{path...}", "/api/npm/{key}/{rest...}", "/ui/"} {
		router.HandleFunc(p, ok)
	}
	res, err := httpserver.NewResolver(nil, "http://trove.test", nil, httpserver.ForEndpoint(ep))
	if err != nil {
		t.Fatal(err)
	}
	ls, err := cluster.NewLimiterSet(cluster.LimiterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	limit, err := NewRateLimit(RateLimitOptions{Config: tightLimits(), Limiters: ls})
	if err != nil {
		t.Fatal(err)
	}
	origins := []string{"https://portal.example.com"}
	return httpserver.Chain{
		Router: router, Resolver: res, Endpoint: ep,
		Authenticate: func(*http.Request) (httpserver.Principal, error) {
			if !token {
				return httpserver.AnonymousPrincipal(), nil
			}
			return httpserver.Principal{ID: "u-a", Name: "a", Kind: httpserver.KindUser, Credential: httpserver.Credential{Method: httpserver.MethodBearer, TokenID: "a"}}, nil
		},
		Preflight: Preflight(origins),
		RateLimit: EdgeStage(CORS(origins), limit),
	}.Handler()
}

// headersOf is the header of an answer without the fields that name the request.
func headersOf(rec *httptest.ResponseRecorder) http.Header {
	h := rec.Header().Clone()
	h.Del(httpserver.HeaderRequestID)
	h.Del("Content-Length") // the problem's instance member is the request path
	return h
}

// TestQ_ARCH_306_TheEdgeStagesAnswerAnUnservedFamilyLikeAnUnknownPath: on the default endpoint of a role, a request of a
// family the role does not serve gets neither the headers of the management API (R-API-017) nor a CORS answer or header
// (R-API-018) nor a rate-limit class (R-API-016): its status and header are those of the same request to /does-not-exist,
// for a caller with a token and an anonymous one, and for the preflight of an allowed origin (R-EP-005, Q-ARCH-306). On
// the role that serves the family, the same request still gets them.
func TestQ_ARCH_306_TheEdgeStagesAnswerAnUnservedFamilyLikeAnUnknownPath(t *testing.T) {
	type probe struct {
		method, path string
		hdr          []string
	}
	preflight := []string{"Origin", "https://portal.example.com", "Access-Control-Request-Method", "GET"}
	cors := []string{"Origin", "https://portal.example.com", "Authorization", "Bearer x"}
	probes := map[string][]probe{
		"packages": {
			{"GET", "/api/v1/system/ping", nil},
			{"GET", "/api/v1/x", nil},
			{"POST", "/api/v1/x", nil},
			{"OPTIONS", "/api/v1/system/ping", preflight},
			{"OPTIONS", "/api/v1/x", preflight},
			{"GET", "/api/v1/system/ping", cors},
			{"GET", "/ui/", nil},
		},
		"api": {
			{"GET", "/files/r/x", nil},
			{"PUT", "/files/r/x", nil},
			{"GET", "/api/npm/k/lodash", nil},
			{"GET", "/api/nuget/k/v3/index.json", nil},
			{"GET", "/v2/", nil},
			{"OPTIONS", "/files/r/x", preflight},
			{"GET", "/api/npm/k/lodash", cors},
		},
	}
	do := func(h http.Handler, p probe, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(p.method, "http://trove.test"+path, nil)
		for i := 0; i+1 < len(p.hdr); i += 2 {
			r.Header.Set(p.hdr[i], p.hdr[i+1])
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	for role, ps := range probes {
		for _, token := range []bool{false, true} {
			h := roleChain(t, role, token)
			for _, p := range ps {
				got, want := do(h, p, p.path), do(h, p, "/does-not-exist")
				if got.Code != want.Code || !maps.EqualFunc(headersOf(got), headersOf(want), slices.Equal) {
					t.Errorf("%s (token %v): %s %s %v = %d %v\nwant the unknown path's %d %v", role, token, p.method, p.path, p.hdr, got.Code, headersOf(got), want.Code, headersOf(want))
				}
			}
		}
	}

	// The control: where the family is served the rules apply, so the comparison above compares something.
	other := map[string]string{"api": "packages", "packages": "api"}
	for role, ps := range probes {
		h := roleChain(t, other[role], true)
		for _, p := range ps {
			got := do(h, p, p.path)
			switch {
			case p.method == http.MethodOptions && p.path != "/files/r/x" && got.Code != http.StatusNoContent:
				t.Errorf("%s: the preflight of %s answers %d", other[role], p.path, got.Code)
			case p.path == "/api/v1/system/ping" && p.method == http.MethodGet && got.Header().Get("Content-Security-Policy") == "":
				t.Errorf("%s: %s has no header of the management API: %v", other[role], p.path, got.Header())
			case p.method == http.MethodGet && p.path != "/ui/" && p.path != "/v2/" && got.Header().Get("RateLimit-Policy") == "":
				t.Errorf("%s: %s %s is not limited: %d %v", other[role], p.method, p.path, got.Code, got.Header())
			}
			if len(p.hdr) > 0 && p.hdr[0] == "Origin" && p.path == "/api/v1/system/ping" && got.Header().Get("Access-Control-Allow-Origin") == "" {
				t.Errorf("%s: %s %s from an allowed origin has no CORS header", other[role], p.method, p.path)
			}
		}
	}
}
