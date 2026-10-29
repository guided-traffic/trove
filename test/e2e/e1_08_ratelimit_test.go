//go:build e2e

package e2e

import (
	"encoding/base64"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/token"
)

// The defaults this test relies on (internal/config, docs/spec): api.rate_limits read 50/s burst 200 and write
// 10/s burst 50 (R-API-016), auth.rate_limit failures_per_address 300 and starts_per_address 30 per minute
// (R-AUTH-038), valkey.command_timeout 500 ms and circuit_breaker.failure_threshold 5 (R-ARCH-031). The dev
// profile raises only the anonymous class (values-dev.yaml), which no test here uses: the read and write classes
// are tested with tokens, the anonymous class has its bound in test/integration/api.
const (
	readBurst   = 200
	readRate    = 50
	writeBurst  = 50
	startsBurst = 30
	failBurst   = 300
)

// admittedBound is the most a sliding-window limiter admits in d: the burst of the window plus what the window's
// decay frees in that time (R-ARCH-034: at most `burst` per window W = burst / rate), plus a little for the
// request that is in flight when the clock is read.
func admittedBound(burst, rate int, d time.Duration) int {
	return burst + int(float64(rate)*d.Seconds()) + 10
}

func newToken(t testing.TB, adm *api, sa string) *api {
	t.Helper()
	return adm.withToken(newSAToken(t, adm, sa, 3600, map[string]any{"inherit": true}).Token)
}

func whoamiMk(int) (string, string, any, []string) {
	return http.MethodGet, "/api/v1/auth/whoami", nil, nil
}

// TestE1_08RateLimits is exit criterion E1.8: R-AUTH-038's thresholds against Basic, Bearer and the login flow with
// requests spread over three web replicas, R-API-016's limits with 429 and Retry-After, and R-ARCH-034's
// cluster-wide counting across three replicas, the same bound with Valkey stopped and no added delay with the
// circuit open.
func TestE1_08RateLimits(t *testing.T) {
	useScatter(t)
	adm := adminAPI(t)
	ensureSA(t, adm, "e2e-rl")
	// A service account holds a limited number of active tokens; this test mints many.
	revokeTokens := func() { adm.do(http.MethodDelete, "/api/v1/service-accounts/e2e-rl/tokens", nil) }
	revokeTokens()
	t.Cleanup(revokeTokens)
	if got := len(readyWebPods(t)); got != 3 {
		t.Fatalf("%d ready web pods, want 3", got)
	}

	t.Run("R-API-016 E1.8 the read class answers 429 with Retry-After after the burst and a second token is not slowed", func(t *testing.T) {
		a, b := newToken(t, adm, "e2e-rl"), newToken(t, adm, "e2e-rl")
		rs, dur := blast(a, 400, 32, whoamiMk)
		st := countStatus(rs)
		t.Logf("400 reads with one token in %s over %v: %v", dur.Round(time.Millisecond), sortedKeys(replicaSet(rs, 200)), st)
		if st[429] == 0 {
			t.Fatalf("no 429 after %d reads in %s: %v", len(rs), dur, st)
		}
		if st[200]+st[429] != len(rs) {
			t.Errorf("answers other than 200 and 429: %v", st)
		}
		if st[200] > admittedBound(readBurst, readRate, dur) {
			t.Errorf("%d admitted in %s, more than burst %d plus the decay", st[200], dur, readBurst)
		}
		for _, r := range rs {
			if r.Status != 429 {
				continue
			}
			if secs, err := strconv.Atoi(r.Header.Get("Retry-After")); err != nil || secs < 1 {
				t.Fatalf("a 429 without a usable Retry-After: %v %s", r.Header, r)
			}
			if r.Code() != "rate_limited" {
				t.Fatalf("429 code %q, want rate_limited: %s", r.Code(), r)
			}
			if !strings.Contains(r.Header.Get("RateLimit-Policy"), `"read"`) || !strings.Contains(r.Header.Get("RateLimit"), `"read"`) {
				t.Fatalf("429 without the RateLimit fields of class read: %v", r.Header)
			}
			break
		}
		b.get("/api/v1/auth/whoami").want(t, "a second token right after", 200)
	})

	t.Run("R-API-016 E1.8 the write class answers 429 with Retry-After after its burst and is separate from the read class", func(t *testing.T) {
		a := newToken(t, adm, "e2e-rl")
		check := map[string]any{"checks": []map[string]string{{"repository": "generic-local", "action": "read"}}}
		rs, dur := blast(a, 120, 16, func(int) (string, string, any, []string) {
			return http.MethodPost, "/api/v1/authz/check", check, nil
		})
		st := countStatus(rs)
		t.Logf("120 writes with one token in %s: %v", dur.Round(time.Millisecond), st)
		if st[429] == 0 {
			t.Fatalf("no 429 after 120 writes: %v", st)
		}
		if st[200] > admittedBound(writeBurst, 10, dur) {
			t.Errorf("%d writes admitted in %s, more than burst %d plus the decay", st[200], dur, writeBurst)
		}
		for _, r := range rs {
			if r.Status == 429 {
				if r.Header.Get("Retry-After") == "" || !strings.Contains(r.Header.Get("RateLimit-Policy"), `"write"`) {
					t.Fatalf("a write 429 without Retry-After or the write policy: %v", r.Header)
				}
				break
			}
		}
		a.get("/api/v1/auth/whoami").want(t, "a read with the token whose write budget is gone", 200)
	})

	// 1,000 requests with one token round robin over the three replicas within about 2 s (R-ARCH-034).
	oneToken := func(t *testing.T, name string, tok *api, workers int) (admitted int, dur time.Duration, rs []reply) {
		t.Helper()
		rs, dur = blast(tok, 1000, workers, whoamiMk)
		st := countStatus(rs)
		rep := replicaSet(rs, 200)
		t.Logf("%s: 1000 requests with one token in %s: %v, admitted by %v", name, dur.Round(time.Millisecond), st, rep)
		return st[200], dur, rs
	}

	t.Run("R-ARCH-034 R-API-016 E1.8 limits count cluster-wide: 1000 requests with one token over three replicas are admitted at most burst plus decay times, not three bursts", func(t *testing.T) {
		tok := newToken(t, adm, "e2e-rl")
		n, dur, rs := oneToken(t, "edge, round robin", tok, 96)
		if n > admittedBound(readBurst, readRate, dur) {
			t.Errorf("%d admitted in %s, want at most %d (one cluster-wide budget); three replicas counting alone would admit up to %d", n, dur, admittedBound(readBurst, readRate, dur), 3*readBurst)
		}
		if n < readBurst/2 {
			t.Errorf("only %d admitted: the limiter is tighter than its burst %d", n, readBurst)
		}
		if got := len(replicaSet(rs, 200)); got != 3 {
			t.Errorf("the admitted requests reached %d replicas, want all 3", got)
		}
		one := newToken(t, adm, "e2e-rl")
		addrs, stop := podForwards(t)
		defer stop()
		single := newAPI(t, withRoundRobin(addrs[0])).withToken(one.token)
		n1, dur1, _ := oneToken(t, "one replica", single, 12)
		if n1 > admittedBound(readBurst, readRate, dur1) {
			t.Errorf("one replica admitted %d in %s, want at most %d", n1, dur1, admittedBound(readBurst, readRate, dur1))
		}
	})

	// Latency of sequential reads, the same method with Valkey up and down.
	sequential := func(t *testing.T, n int) []time.Duration {
		t.Helper()
		var ds []time.Duration
		var tok *api
		for i := 0; i < n; i++ {
			if i%40 == 0 {
				tok = newToken(t, adm, "e2e-rl")
			}
			r := tok.get("/api/v1/auth/whoami")
			ds = append(ds, r.Dur)
		}
		return ds
	}

	t.Run("R-ARCH-034 R-API-016 E1.8 with Valkey stopped the same bound holds, every replica falls back to its share and adds no delay", func(t *testing.T) {
		baseline := sequential(t, 120)
		p95Up := percentile(append([]time.Duration(nil), baseline...), 0.95)
		withValkeyStopped(t, func() {
			// The breaker opens after 5 failed calls per replica; send until every replica reports the fallback.
			probe := newToken(t, adm, "e2e-rl")
			waitFor(t, "trove_ratelimit_fallback{limiter=\"api_read\"} = 1 on every web pod", 2*time.Minute, time.Second, func() bool {
				for i := 0; i < 12; i++ {
					probe.get("/api/v1/auth/whoami")
				}
				for _, p := range readyWebPods(t) {
					if metricsOf(t, p)[`trove_ratelimit_fallback{limiter="api_read"}`] != 1 {
						return false
					}
				}
				return true
			})
			webReadyDegraded(t, true)

			tok := newToken(t, adm, "e2e-rl")
			n, dur, _ := oneToken(t, "Valkey stopped", tok, 96)
			if n > admittedBound(readBurst, readRate, dur) {
				t.Errorf("with Valkey stopped %d were admitted in %s, want at most %d", n, dur, admittedBound(readBurst, readRate, dur))
			}
			if n < 3 {
				t.Errorf("with Valkey stopped only %d were admitted: every replica has a share of at least one", n)
			}
			for _, p := range readyWebPods(t) {
				if v := metricsOf(t, p)[`trove_ratelimit_fallback{limiter="api_read"}`]; v != 1 {
					t.Errorf("%s trove_ratelimit_fallback{limiter=api_read} = %v, want 1", p, v)
				}
			}

			down := sequential(t, 120)
			p95Down, maxDown := percentile(append([]time.Duration(nil), down...), 0.95), percentile(down, 1)
			t.Logf("sequential reads: p95 %s with Valkey up, p95 %s and max %s with the circuit open", p95Up.Round(time.Millisecond), p95Down.Round(time.Millisecond), maxDown.Round(time.Millisecond))
			if p95Down > p95Up+100*time.Millisecond {
				t.Errorf("p95 %s with the circuit open against %s with Valkey up: the limiter adds a delay", p95Down, p95Up)
			}
			if maxDown >= 500*time.Millisecond {
				t.Errorf("a request took %s with the circuit open, valkey.command_timeout is 500ms", maxDown)
			}
		})
		webReadyDegraded(t, false)
	})

	t.Run("R-ARCH-034 E1.8 a Valkey that answers a second late costs at most command_timeout on at most failure_threshold checks per replica and nothing once the circuit is open", func(t *testing.T) {
		waitFor(t, "the circuit closed on every web pod", 2*time.Minute, time.Second, func() bool {
			for _, p := range readyWebPods(t) {
				if metricsOf(t, p)["trove_valkey_circuit_open"] != 0 {
					return false
				}
			}
			return true
		})
		// CLIENT PAUSE holds every command of every client for the time (4 s, below the Sentinels' 5 s), the
		// stand-in for a link with latency in front of Valkey: the calls neither succeed nor fail at once, they
		// wait for valkey.command_timeout (500 ms). Three clients send in turn, so every replica sees a few
		// checks at a time; the window is longer than the pause.
		tok := newToken(t, adm, "e2e-rl")
		paused := make(chan struct{})
		go func() {
			defer close(paused)
			valkeyPrimaryCLI(t, "client", "pause", "4000", "all")
		}()
		type sample struct{ at, dur time.Duration }
		var mu sync.Mutex
		var samples []sample
		start := time.Now()
		var wg sync.WaitGroup
		for w := 0; w < 3; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for time.Since(start) < 9*time.Second {
					at := time.Since(start)
					r, err := tok.send(http.MethodGet, "/api/v1/auth/whoami", nil)
					if err != nil {
						continue
					}
					mu.Lock()
					samples = append(samples, sample{at, r.Dur})
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		<-paused
		slow, fastAfter := 0, 0
		var firstSlow, lastSlow time.Duration
		for _, s := range samples {
			if s.dur >= 400*time.Millisecond {
				if slow == 0 {
					firstSlow = s.at
				}
				slow++
				lastSlow = s.at
			}
		}
		for _, s := range samples {
			if s.at > lastSlow && s.dur < 150*time.Millisecond {
				fastAfter++
			}
		}
		t.Logf("%d requests in 9 s; %d took 400 ms or more (first at +%s, last at +%s); %d fast ones after the last slow one",
			len(samples), slow, firstSlow.Round(time.Millisecond), lastSlow.Round(time.Millisecond), fastAfter)
		const threshold, replicas, clients = 5, 3, 3
		if slow == 0 {
			t.Error("no request was held: the pause did not reach the limiter")
		}
		// failure_threshold held checks per replica, and the checks already in flight on the other clients when the
		// circuit opens.
		if bound := threshold*replicas + clients*replicas; slow > bound {
			t.Errorf("%d requests were held by the limiter, want at most %d (failure_threshold %d per replica plus those in flight)", slow, bound, threshold)
		}
		if fastAfter < 30 {
			t.Errorf("only %d fast requests after the last held one: the circuit did not stay open for the rest of the pause", fastAfter)
		}
		waitFor(t, "the circuit closed again", 2*time.Minute, time.Second, func() bool {
			for _, p := range readyWebPods(t) {
				if metricsOf(t, p)["trove_valkey_circuit_open"] != 0 {
					return false
				}
			}
			return true
		})
	})

	// The brute-force thresholds (R-AUTH-038) count per client address. The edge address is the host behind the
	// ingress and every other test shares it, so these requests go to the replicas through port-forwards, whose
	// address (127.0.0.1) is a bucket of its own, round robin over the three pods.
	t.Run("R-AUTH-038 E1.8 brute force against Bearer and Basic: 400 random well-formed tokens are validated at most 300 times, the rest get 429, and a recently used token still works on all three replicas", func(t *testing.T) {
		pw := secretValue(t, "trove-keycloak", "alice-password")
		bro := newBrowser(t)
		bro.oidcLogin("alice", pw)
		pat := bro.do(http.MethodPost, "/api/v1/tokens", map[string]any{"description": "e2e brute force", "ttlSeconds": 3600, "scope": map[string]any{"inherit": true}})
		pat.want(t, "alice's PAT", 201)
		var c createdToken
		pat.JSON(t, &c)

		addrs, stop := podForwards(t)
		defer stop()
		direct := newAPI(t, withRoundRobin(addrs...))
		// A successful validation marks the token in Valkey; the burst must not lock it out on the other replicas.
		direct.withToken(c.Token).get("/api/v1/auth/whoami").want(t, "the PAT before the burst", 200)

		burst := func(t *testing.T, label string) (valid, limited int, limitedKinds map[string]int) {
			t.Helper()
			limitedKinds = map[string]int{}
			start := time.Now()
			for i := 0; i < 400; i++ {
				raw, err := token.Generate(token.TypePAT)
				if err != nil {
					t.Fatal(err)
				}
				var r reply
				kind := "bearer"
				if i%2 == 1 {
					kind = "basic"
					r = direct.do(http.MethodGet, "/api/v1/auth/whoami", nil, "Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("any:"+raw)))
				} else {
					r = direct.do(http.MethodGet, "/api/v1/auth/whoami", nil, "Authorization", "Bearer "+raw)
				}
				switch r.Status {
				case 401:
					valid++
				case 429:
					limited++
					limitedKinds[kind]++
					if secs, err := strconv.Atoi(r.Header.Get("Retry-After")); err != nil || secs < 1 {
						t.Fatalf("%s: a 429 without Retry-After: %v", label, r.Header)
					}
				default:
					t.Fatalf("%s: request %d answered %s", label, i, r)
				}
			}
			t.Logf("%s: 400 random well-formed tokens in %s: validated (401) %d, throttled (429) %d %v", label, time.Since(start).Round(time.Millisecond), valid, limited, limitedKinds)
			return valid, limited, limitedKinds
		}

		diag := func(label string) {
			for _, p := range readyWebPods(t) {
				for series, v := range metricsOf(t, p) {
					if strings.HasPrefix(series, "trove_ratelimit_decisions_total{limiter=\"auth.credential_failures\"") || strings.HasPrefix(series, "trove_ratelimit_fallback{limiter=\"auth.credential_failures\"") || series == "trove_valkey_circuit_open" || series == "trove_valkey_available" {
						t.Logf("%s %s %s = %v", label, p, series, v)
					}
				}
			}
		}
		diag("before the burst:")
		valid, limited, kinds := burst(t, "Valkey up")
		diag("after the burst:")
		// failures_per_address 300 per minute: the window slides while the loop runs.
		if bound := failBurst + 8; valid > bound || valid < failBurst-20 {
			t.Errorf("%d tokens were validated, want about the threshold %d (at most %d)", valid, failBurst, bound)
		}
		if limited == 0 || kinds["bearer"] == 0 || kinds["basic"] == 0 {
			t.Errorf("throttled %d (%v): both Bearer and Basic must reach 429", limited, kinds)
		}
		seen := map[string]bool{}
		for i := 0; i < 9; i++ {
			r := direct.withToken(c.Token).get("/api/v1/auth/whoami")
			if r.Status != 200 {
				t.Errorf("the recently validated PAT during the burst, request %d = %s, want 200", i, r)
			}
			seen[r.Replica()] = true
		}
		if len(seen) != 3 {
			t.Errorf("the PAT was checked on %v, want all three replicas", sortedKeys(seen))
		}
		fresh, _ := token.Generate(token.TypePAT)
		if r := direct.withToken(fresh).get("/api/v1/auth/whoami"); r.Status != 429 {
			t.Errorf("an unknown token while the address is throttled = %s, want 429", r)
		}
	})

	t.Run("R-AUTH-038 E1.8 the login flow: 40 starts from one address over three replicas get 429 from the 31st on", func(t *testing.T) {
		addrs, stop := podForwards(t)
		defer stop()
		direct := newAPI(t, withRoundRobin(addrs...))
		var got []int
		seen := map[string]bool{}
		for i := 0; i < 40; i++ {
			r := direct.get("/auth/oidc/keycloak/start")
			got = append(got, r.Status)
			seen[r.Replica()] = true
			if r.Status == 429 && r.Header.Get("Retry-After") == "" {
				t.Errorf("start %d: a 429 without Retry-After", i+1)
			}
		}
		t.Logf("statuses of 40 starts: %v, replicas %v", got, sortedKeys(seen))
		for i, s := range got {
			want := 302
			if i >= startsBurst {
				want = 429
			}
			if s != want {
				t.Errorf("start %d answered %d, want %d", i+1, s, want)
			}
		}
		if len(seen) != 3 {
			t.Errorf("the starts reached %v, want all three replicas", sortedKeys(seen))
		}
	})

	t.Run("R-AUTH-038 E1.8 with Valkey stopped the thresholds hold: at most 300 validations, the login flow limit and trove_ratelimit_fallback 1 on each replica", func(t *testing.T) {
		withValkeyStopped(t, func() {
			addrs, stop := podForwards(t)
			defer stop()
			direct := newAPI(t, withRoundRobin(addrs...))
			// Warm the breakers: a few validations per replica, each of which consults the limiter first.
			for i := 0; i < 60; i++ {
				raw, _ := token.Generate(token.TypePAT)
				direct.withToken(raw).get("/api/v1/auth/whoami")
			}
			valid, limited := 0, 0
			for i := 0; i < 400; i++ {
				raw, _ := token.Generate(token.TypePAT)
				switch r := direct.withToken(raw).get("/api/v1/auth/whoami"); r.Status {
				case 401:
					valid++
				case 429:
					limited++
				default:
					t.Fatalf("request %d answered %s", i, r)
				}
			}
			t.Logf("Valkey stopped: after 60 warm-up requests, 400 more: validated %d, throttled %d", valid, limited)
			if valid+60 > failBurst+8 {
				t.Errorf("%d validations with Valkey stopped (60 warm-up included), want at most %d", valid+60, failBurst+8)
			}
			if limited == 0 {
				t.Error("no 429 with Valkey stopped")
			}
			for _, p := range readyWebPods(t) {
				if v := metricsOf(t, p)[`trove_ratelimit_fallback{limiter="auth.credential_failures"}`]; v != 1 {
					t.Errorf("%s trove_ratelimit_fallback{limiter=auth.credential_failures} = %v, want 1", p, v)
				}
			}

			var got []int
			for i := 0; i < 40; i++ {
				got = append(got, direct.get("/auth/oidc/keycloak/start").Status)
			}
			ok := 0
			for _, s := range got {
				if s == 302 {
					ok++
				}
			}
			t.Logf("Valkey stopped: statuses of 40 starts %v", got)
			if ok > startsBurst+3 || ok < startsBurst-4 {
				t.Errorf("%d of 40 starts admitted with Valkey stopped, want about %d (each replica's share of the burst)", ok, startsBurst)
			}
			sort.Ints(got)
			if got[len(got)-1] != 429 {
				t.Error("no start was refused with Valkey stopped")
			}
		})
	})
}
