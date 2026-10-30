//go:build integration

package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/valkey-io/valkey-go"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

var keepAlive = &http.Client{Timeout: 10 * time.Second}

// burstOf sends total GET requests with one token, spread over the replicas,
// from 16 goroutines, and returns how many were admitted and the time it took.
// Every answer, a refusal included, is checked against the document.
func burstOf(t *testing.T, e *env, nodes []*node, token string, total int) (admitted, refused int, elapsed time.Duration, missingRetryAfter int) {
	t.Helper()
	var next, ok, no, noRetry atomic.Int64
	var wg sync.WaitGroup
	val := e.stack.Validator()
	start := time.Now()
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1)) - 1
				if i >= total {
					return
				}
				req, _ := http.NewRequest("GET", nodes[i%len(nodes)].url()+"/api/v1/system/version", nil)
				req.Header.Set("Authorization", "Bearer "+token)
				res, err := keepAlive.Do(req)
				if err != nil {
					t.Error(err)
					return
				}
				body, _ := io.ReadAll(res.Body)
				_ = res.Body.Close()
				check, _ := http.NewRequest("GET", "http://"+testenv.DefaultHost+"/api/v1/system/version", nil)
				if _, err := val.Check(check, res.StatusCode, res.Header, body); err != nil {
					t.Errorf("response validation: %v\n%s", err, body)
				}
				switch res.StatusCode {
				case 200:
					ok.Add(1)
				case 429:
					no.Add(1)
					if n, _ := strconv.Atoi(res.Header.Get("Retry-After")); n < 1 {
						noRetry.Add(1)
					}
				default:
					t.Errorf("status %d", res.StatusCode)
				}
			}
		}()
	}
	wg.Wait()
	return int(ok.Load()), int(no.Load()), time.Since(start), int(noRetry.Load())
}

// fallback is the value of trove_ratelimit_fallback{limiter="api_read"} on a
// replica; -1 if the series is absent.
func fallback(t *testing.T, n *node) float64 {
	t.Helper()
	return testenv.MetricValue(n.Metrics(t), `trove_ratelimit_fallback{limiter="api_read"}`)
}

// startWebs starts three web replicas and waits until each has counted all three:
// the limiters divide their per-replica share by the number of live replicas,
// which a replica reads once per heartbeat interval (replicaCount).
func (e *env) startWebs(t *testing.T) []*node {
	t.Helper()
	var nodes []*node
	for _, name := range []string{"web-1", "web-2", "web-3"} {
		nodes = append(nodes, e.newNode(name, nodeOpts{}))
	}
	waitFor(t, "three live web replicas", 10*time.Second, func() bool {
		return e.scalar(`SELECT count(*) FROM replica WHERE 'web' = ANY(roles) AND heartbeat_at >= now() - interval '3 seconds'`) == 3
	})
	// The count is not observable from outside; two heartbeat intervals (1 s each
	// in the harness) are enough for every replica to have read three.
	time.Sleep(2500 * time.Millisecond)
	return nodes
}

// TestClusterWideRateLimit is the acceptance of R-ARCH-034 and R-API-016: three
// web replicas, one token, 1,000 requests within 2 s admit at most 300, with
// Valkey available and with it unavailable, and a second token is not slowed down.
func TestClusterWideRateLimit(t *testing.T) {
	t.Run("R-ARCH-034 R-API-016 with Valkey: at most 300 admitted, 429 with Retry-After, the second token unaffected", func(t *testing.T) {
		e := newEnv(t, true, rateLimitsOn...)
		nodes := e.startWebs(t)
		admitted, refused, elapsed, noRetry := burstOf(t, e, nodes, e.alice.token, 1000)
		t.Logf("admitted %d, refused %d in %s", admitted, refused, elapsed)
		if elapsed > 2*time.Second {
			t.Fatalf("the burst took %s; the bound holds for 2 s", elapsed)
		}
		if admitted > 300 || admitted < 150 || noRetry != 0 || refused != 1000-admitted {
			t.Fatalf("admitted %d refused %d without Retry-After %d", admitted, refused, noRetry)
		}
		req, _ := http.NewRequest("GET", nodes[1].url()+"/api/v1/system/version", nil)
		req.Header.Set("Authorization", "Bearer "+e.bob.token)
		res, err := keepAlive.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != 200 || res.Header.Get("RateLimit-Policy") != `"read";q=200;w=4` {
			t.Fatalf("second token: %d policy %q", res.StatusCode, res.Header.Get("RateLimit-Policy"))
		}
		for _, n := range nodes {
			if v := fallback(t, n); v != 0 {
				t.Errorf("%s: trove_ratelimit_fallback = %v with Valkey running", n.Name, v)
			}
		}
	})

	for _, mode := range []string{"the client is closed", "no Sentinel answers"} {
		t.Run("R-ARCH-034 R-API-016 with Valkey unavailable ("+mode+"): at most 300 admitted and the fallback gauge is 1", func(t *testing.T) {
			e := newEnv(t, mode == "the client is closed", rateLimitsOn...)
			nodes := e.startWebs(t)
			if mode == "the client is closed" {
				for _, n := range nodes {
					n.App.Valkey().Close()
				}
			}
			admitted, refused, elapsed, noRetry := burstOf(t, e, nodes, e.alice.token, 1000)
			t.Logf("admitted %d, refused %d in %s", admitted, refused, elapsed)
			if elapsed > 2*time.Second {
				t.Fatalf("the burst took %s; the bound holds for 2 s", elapsed)
			}
			if admitted > 300 || admitted < 90 || noRetry != 0 {
				t.Fatalf("admitted %d refused %d without Retry-After %d", admitted, refused, noRetry)
			}
			for _, n := range nodes {
				if v := fallback(t, n); v != 1 {
					t.Errorf("%s: trove_ratelimit_fallback{limiter=api_read} = %v, want 1", n.Name, v)
				}
			}
		})
	}
}

// TestRateLimitAddsNoDelayOnceTheCircuitIsOpen is the third criterion of
// R-ARCH-034: with latency in front of Valkey, the limiter adds at most
// valkey.command_timeout to at most failure_threshold checks, and nothing once
// the breaker is open.
func TestRateLimitAddsNoDelayOnceTheCircuitIsOpen(t *testing.T) {
	e := newEnv(t, true, append(rateLimitsOn,
		"TROVE_API_RATE_LIMITS_READ_REQUESTS_PER_SECOND=1000", "TROVE_API_RATE_LIMITS_READ_BURST=100000",
		// The client's timings the criterion is stated for: a command that waits at
		// most 500 ms, five failures that open the circuit, a probe every 500 ms.
		"TROVE_VALKEY_COMMAND_TIMEOUT=500ms", "TROVE_VALKEY_CIRCUIT_BREAKER_FAILURE_THRESHOLD=5",
		"TROVE_VALKEY_CIRCUIT_BREAKER_OPEN_DURATION=2s", "TROVE_VALKEY_HEALTH_CHECK_INTERVAL=500ms")...)
	n := e.newNode("web-1", nodeOpts{})
	vk := n.App.Valkey()
	v := testenv.ValkeyConfig(t)
	primary := vk.Status().Primary
	ca, err := os.ReadFile(v.CAFile)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca)
	// The first request of a token validates it from PostgreSQL and marks it in
	// Valkey (R-AUTH-038); that marker write is not the limiter's, so it happens
	// before the pause.
	warm, _ := http.NewRequest("GET", n.url()+"/api/v1/system/version", nil)
	warm.Header.Set("Authorization", "Bearer "+e.alice.token)
	res, err := keepAlive.Do(warm)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("the first request of the token: %v %v", res, err)
	}
	_ = res.Body.Close()
	admin, err := valkey.NewClient(valkey.ClientOption{
		InitAddress: []string{primary}, Username: v.AdminUsername, Password: v.AdminPassword,
		TLSConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, DisableCache: true, ForceSingleClient: true, DisableRetry: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	ctx := context.Background()
	if err := admin.Do(ctx, admin.B().Arbitrary("CLIENT", "PAUSE", "3000", "WRITE").Build()).Error(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Do(context.Background(), admin.B().Arbitrary("CLIENT", "UNPAUSE").Build()).Error() }()

	get := func() time.Duration {
		req, _ := http.NewRequest("GET", n.url()+"/api/v1/system/version", nil)
		req.Header.Set("Authorization", "Bearer "+e.alice.token)
		start := time.Now()
		res, err := keepAlive.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatalf("a limiter must not fail a request because Valkey failed: %d", res.StatusCode)
		}
		return time.Since(start)
	}
	slow := 0
	for i := 0; i < 12 && vk.Available(); i++ {
		if d := get(); d > 400*time.Millisecond {
			slow++
			if d > 900*time.Millisecond {
				t.Fatalf("check %d waited %s, more than valkey.command_timeout", i, d)
			}
		}
	}
	if vk.Available() {
		t.Fatalf("Valkey is still available under the pause: %+v", vk.Status())
	}
	t.Logf("%d delayed checks before the client turned unavailable: %+v", slow, vk.Status())
	if slow > 5 {
		t.Fatalf("%d checks were delayed, want at most failure_threshold (5)", slow)
	}
	for i := range 20 {
		if d := get(); d > 100*time.Millisecond {
			t.Fatalf("request %d took %s with the circuit open", i, d)
		}
	}
}
