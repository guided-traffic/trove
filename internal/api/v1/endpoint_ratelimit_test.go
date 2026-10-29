package v1

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/httpserver"
)

func TestR_EP_015_EndpointRateLimitClass(t *testing.T) {
	ls, err := cluster.NewLimiterSet(cluster.LimiterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	n := 1
	class := config.EndpointRateLimit{RequestsPerSecond: 1, Burst: 3, Key: "principal"}
	lim := ls.NewLimiterShared(EndpointLimiterName("ext-a"), cluster.ScopeCluster, class.RequestsPerSecond, class.Burst, func() int { return n })
	res, err := httpserver.NewResolver(nil, "https://packages.example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	// The chain resolves the client address from the peer and takes the caller
	// from X-Test-Token, so the stage sees what it sees in production.
	stage := func(endpoint string, c config.EndpointRateLimit) http.Handler {
		router := httpserver.NewClientRouter()
		router.HandleFunc("GET /api/npm/{rest...}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
		return httpserver.Chain{
			Router: router, Resolver: res,
			Authenticate: func(r *http.Request) (httpserver.Principal, error) {
				id := r.Header.Get("X-Test-Token")
				return httpserver.Principal{ID: "sa-" + id, Kind: httpserver.KindServiceAccount, Credential: httpserver.Credential{Method: httpserver.MethodBearer, TokenID: id}}, nil
			},
			RateLimit: NewEndpointRateLimit(EndpointRateLimitOptions{Endpoint: endpoint, Class: "ext-a", Limit: c, Limiter: lim}),
		}.Handler()
	}
	customers, partners := stage("customers", class), stage("partners", class)
	byAddr := stage("addr", config.EndpointRateLimit{RequestsPerSecond: 1, Burst: 3, Key: "address"})
	tokenOf := func(id string) string { return id }
	do := func(h http.Handler, tok, addr string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/npm/npm-customers/x", nil)
		r.Header.Set("X-Test-Token", tok)
		if addr == "" {
			addr = "198.51.100.1"
		}
		r.RemoteAddr = addr + ":40000"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}

	t.Run("R-EP-015 the limiter is endpoint_<class> with underscores", func(t *testing.T) {
		if lim.Name() != "endpoint_ext_a" || EndpointLimiterName("external") != "endpoint_external" {
			t.Fatal(lim.Name())
		}
	})
	t.Run("R-EP-015 a package request is limited after the burst with 429, Retry-After and the endpoint class fields", func(t *testing.T) {
		for i := range 3 {
			if rec := do(customers, tokenOf("a"), ""); rec.Code != 200 {
				t.Fatalf("request %d: %d", i, rec.Code)
			}
		}
		rec := do(customers, tokenOf("a"), "")
		if rec.Code != 429 || rec.Header().Get("Retry-After") == "" || !strings.Contains(rec.Body.String(), `"code":"rate_limited"`) {
			t.Fatalf("%d %v %s", rec.Code, rec.Header(), rec.Body)
		}
		if p := rec.Header().Get("RateLimit-Policy"); !strings.HasPrefix(p, `"endpoint:ext-a";q=`) {
			t.Fatalf("RateLimit-Policy %q", p)
		}
		if p := rec.Header().Get("RateLimit"); !strings.HasPrefix(p, `"endpoint:ext-a";r=0;t=`) {
			t.Fatalf("RateLimit %q", p)
		}
	})
	t.Run("R-EP-015 a second token is not slowed down, nor the first token on another endpoint of the class", func(t *testing.T) {
		if rec := do(customers, tokenOf("b"), ""); rec.Code != 200 {
			t.Fatalf("second token: %d", rec.Code)
		}
		if rec := do(partners, tokenOf("a"), ""); rec.Code != 200 {
			t.Fatalf("first token on partners: %d", rec.Code)
		}
	})
	t.Run("R-EP-015 key address buckets by the client address whatever the token", func(t *testing.T) {
		for i := range 3 {
			if rec := do(byAddr, tokenOf(string(rune('c'+i))), "203.0.113.9"); rec.Code != 200 {
				t.Fatalf("request %d: %d", i, rec.Code)
			}
		}
		if rec := do(byAddr, tokenOf("z"), "203.0.113.9"); rec.Code != 429 {
			t.Fatalf("a fourth token from the same address: %d", rec.Code)
		}
		if rec := do(byAddr, tokenOf("z"), "203.0.113.10"); rec.Code != 200 {
			t.Fatalf("another address: %d", rec.Code)
		}
	})
	t.Run("R-EP-015 without Valkey the share divides by the replicas that serve the endpoint", func(t *testing.T) {
		n = 3
		admitted := 0
		for range 10 {
			if do(customers, tokenOf("shared"), "").Code == 200 {
				admitted++
			}
		}
		if admitted != 1 {
			t.Fatalf("%d admitted with a burst of 3 shared by 3 replicas, want 1", admitted)
		}
	})
}
