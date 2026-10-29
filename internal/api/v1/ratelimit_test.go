package v1

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/httpserver"
)

func limitedChain(t *testing.T, cfg config.APIRateLimits, opts ...func(*RateLimitOptions)) (http.Handler, *prometheus.Registry) {
	t.Helper()
	reg := prometheus.NewRegistry()
	ls, err := cluster.NewLimiterSet(cluster.LimiterOptions{Registry: reg})
	if err != nil {
		t.Fatal(err)
	}
	o := RateLimitOptions{Config: cfg, Limiters: ls, Registry: reg}
	for _, f := range opts {
		f(&o)
	}
	limit, err := NewRateLimit(o)
	if err != nil {
		t.Fatal(err)
	}
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	return EdgeStage(CORS([]string{"https://portal.example.com"}), limit)(ok), reg
}

func tightLimits() config.APIRateLimits {
	return config.APIRateLimits{
		Enabled:   true,
		Read:      config.APIRateLimitRead{RequestsPerSecond: 1, Burst: 3},
		Write:     config.APIRateLimitWrite{RequestsPerSecond: 1, Burst: 2},
		Anonymous: config.APIRateLimitAnonymous{RequestsPerSecond: 1, Burst: 2},
	}
}

func asPrincipal(r *http.Request, p httpserver.Principal) *http.Request {
	return r.WithContext(httpserver.WithPrincipal(r.Context(), p))
}

func TestRateLimitStage(t *testing.T) {
	token := func(id string) httpserver.Principal {
		return httpserver.Principal{ID: "u-" + id, Name: id, Kind: httpserver.KindUser, Credential: httpserver.Credential{Method: httpserver.MethodBearer, TokenID: id}}
	}
	do := func(h http.Handler, method, path string, p httpserver.Principal) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, asPrincipal(httptest.NewRequest(method, path, nil), p))
		return rec
	}

	t.Run("R-API-016 the burst is admitted, then 429 with Retry-After; a second token is not slowed down", func(t *testing.T) {
		h, _ := limitedChain(t, tightLimits())
		var last *httptest.ResponseRecorder
		for i := range 4 {
			last = do(h, "GET", "/api/v1/repositories", token("a"))
			if i < 3 && last.Code != 200 {
				t.Fatalf("request %d: %d", i, last.Code)
			}
		}
		if last.Code != 429 {
			t.Fatalf("4th request: %d", last.Code)
		}
		ra, err := strconv.Atoi(last.Header().Get("Retry-After"))
		if err != nil || ra < 1 {
			t.Fatalf("Retry-After = %q", last.Header().Get("Retry-After"))
		}
		if !strings.Contains(last.Body.String(), `"code":"rate_limited"`) || !strings.HasPrefix(last.Header().Get("Content-Type"), "application/problem+json") {
			t.Fatalf("%s %s", last.Header().Get("Content-Type"), last.Body.String())
		}
		if do(h, "GET", "/api/v1/repositories", token("b")).Code != 200 {
			t.Fatal("a second token was slowed down")
		}
	})

	t.Run("R-API-016 every answer carries RateLimit-Policy and RateLimit", func(t *testing.T) {
		h, _ := limitedChain(t, tightLimits())
		rec := do(h, "GET", "/api/v1/repositories", token("a"))
		pol, rl := rec.Header().Get("RateLimit-Policy"), rec.Header().Get("RateLimit")
		if !strings.HasPrefix(pol, `"read";q=`) || !strings.Contains(pol, ";w=") || !strings.HasPrefix(rl, `"read";r=`) || !strings.Contains(rl, ";t=") {
			t.Fatalf("RateLimit-Policy %q RateLimit %q", pol, rl)
		}
	})

	t.Run("R-API-016 with the defaults the policy of a read is q=200;w=4", func(t *testing.T) {
		cfg := config.APIRateLimits{
			Enabled:   true,
			Read:      config.APIRateLimitRead{RequestsPerSecond: 50, Burst: 200},
			Write:     config.APIRateLimitWrite{RequestsPerSecond: 10, Burst: 50},
			Anonymous: config.APIRateLimitAnonymous{RequestsPerSecond: 5, Burst: 20},
		}
		h, _ := limitedChain(t, cfg)
		// The default policy is announced as the cluster policy; the limiter without
		// Valkey runs the fallback, whose share is the whole budget with one replica.
		if pol := do(h, "GET", "/api/v1/x", token("a")).Header().Get("RateLimit-Policy"); pol != `"read";q=200;w=4` {
			t.Fatalf("policy = %q", pol)
		}
		if pol := do(h, "POST", "/api/v1/x", token("a")).Header().Get("RateLimit-Policy"); pol != `"write";q=50;w=5` {
			t.Fatalf("policy = %q", pol)
		}
		if pol := do(h, "GET", "/api/v1/x", httpserver.AnonymousPrincipal()).Header().Get("RateLimit-Policy"); pol != `"anonymous";q=20;w=4` {
			t.Fatalf("policy = %q", pol)
		}
	})

	t.Run("R-API-016 reads and writes are separate classes per principal", func(t *testing.T) {
		h, _ := limitedChain(t, tightLimits())
		for range 3 {
			do(h, "GET", "/api/v1/x", token("a"))
		}
		if do(h, "GET", "/api/v1/x", token("a")).Code != 429 {
			t.Fatal("reads not limited")
		}
		if do(h, "POST", "/api/v1/x", token("a")).Code != 200 {
			t.Fatal("a write was refused with the read budget used up")
		}
	})

	t.Run("R-API-016 anonymous requests are limited per client address", func(t *testing.T) {
		h, _ := limitedChain(t, tightLimits())
		from := func(addr string) *httptest.ResponseRecorder {
			rec := httptest.NewRecorder()
			r := httptest.NewRequest("GET", "/api/v1/system/ping", nil)
			r.RemoteAddr = addr + ":1234"
			h.ServeHTTP(rec, asPrincipal(r, httpserver.AnonymousPrincipal()))
			return rec
		}
		// Without the chain's address stage every request has the zero address, so
		// this checks the class and the 429 only.
		var last *httptest.ResponseRecorder
		for range 3 {
			last = from("10.0.0.1")
		}
		if last.Code != 429 || !strings.HasPrefix(last.Header().Get("RateLimit-Policy"), `"anonymous";`) {
			t.Fatalf("%d %v", last.Code, last.Header())
		}
	})

	t.Run("R-API-016 only /api/v1/ and /files/ are limited here", func(t *testing.T) {
		h, _ := limitedChain(t, tightLimits())
		for range 10 {
			if rec := do(h, "GET", "/ui/index.html", token("a")); rec.Code != 200 || rec.Header().Get("RateLimit") != "" {
				t.Fatalf("%d %v", rec.Code, rec.Header())
			}
		}
		for range 3 {
			do(h, "GET", "/files/repo/a", token("f"))
		}
		if do(h, "GET", "/files/repo/a", token("f")).Code != 429 {
			t.Fatal("/files/ is not limited")
		}
	})

	t.Run("R-API-016 exempt_admins lets an administrator pass; enabled=false turns the limits off", func(t *testing.T) {
		cfg := tightLimits()
		cfg.ExemptAdmins = true
		h, _ := limitedChain(t, cfg, func(o *RateLimitOptions) {
			o.IsAdmin = func(r *http.Request) bool { return httpserver.PrincipalFrom(r.Context()).Name == "root" }
		})
		for range 10 {
			if do(h, "GET", "/api/v1/x", token("root")).Code != 200 {
				t.Fatal("an administrator was limited")
			}
		}
		off := tightLimits()
		off.Enabled = false
		h2, _ := limitedChain(t, off)
		for range 10 {
			if do(h2, "GET", "/api/v1/x", token("a")).Code != 200 {
				t.Fatal("limits are off")
			}
		}
	})

	t.Run("R-API-016 refusals are counted by class", func(t *testing.T) {
		h, reg := limitedChain(t, tightLimits())
		for range 5 {
			do(h, "GET", "/api/v1/x", token("a"))
		}
		mfs, _ := reg.Gather()
		found := false
		for _, mf := range mfs {
			if mf.GetName() == "trove_api_rate_limited_total" {
				found = true
				got := map[string]float64{}
				for _, m := range mf.GetMetric() {
					got[m.GetLabel()[0].GetValue()] = m.GetCounter().GetValue()
				}
				if got["read"] != 2 || got["write"] != 0 {
					t.Fatalf("rejected = %v, want read 2", got)
				}
			}
		}
		if !found {
			t.Fatal("trove_api_rate_limited_total missing")
		}
	})

	t.Run("R-API-017 /api/v1/ answers carry the security headers", func(t *testing.T) {
		h, _ := limitedChain(t, tightLimits())
		rec := do(h, "GET", "/api/v1/x", token("a"))
		if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Referrer-Policy") != "no-referrer" ||
			rec.Header().Get("Content-Security-Policy") != "default-src 'none'; frame-ancestors 'none'" {
			t.Fatalf("%v", rec.Header())
		}
		if ui := do(h, "GET", "/ui/", token("a")); ui.Header().Get("Content-Security-Policy") != "" {
			t.Fatal("the API headers leak onto the UI")
		}
	})

	t.Run("R-API-018 a preflight is answered before it counts against a limit", func(t *testing.T) {
		h, _ := limitedChain(t, tightLimits())
		for range 20 {
			rec := httptest.NewRecorder()
			r := httptest.NewRequest("OPTIONS", "/api/v1/repositories", nil)
			r.Header.Set("Origin", "https://portal.example.com")
			r.Header.Set("Access-Control-Request-Method", "GET")
			h.ServeHTTP(rec, asPrincipal(r, httpserver.AnonymousPrincipal()))
			if rec.Code != 204 {
				t.Fatalf("preflight: %d", rec.Code)
			}
		}
		if do(h, "GET", "/api/v1/system/ping", httpserver.AnonymousPrincipal()).Code != 200 {
			t.Fatal("preflights used up the anonymous budget")
		}
	})
}
