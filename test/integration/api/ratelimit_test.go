//go:build integration

package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/valkey-io/valkey-go"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/api/v1/apitest"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

var keepAlive = &http.Client{Timeout: 10 * time.Second}

// burstOf sends total GET requests with one token, spread over the replicas,
// from 16 goroutines, and returns how many were admitted and the time it took.
func burstOf(t *testing.T, nodes []*node, token string, total int) (admitted, refused int, elapsed time.Duration, missingRetryAfter int) {
	t.Helper()
	var next, ok, no, noRetry atomic.Int64
	var wg sync.WaitGroup
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
				req.Header.Set("X-Test-Principal", token+":"+token)
				res, err := keepAlive.Do(req)
				if err != nil {
					t.Error(err)
					return
				}
				_ = res.Body.Close()
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

func gaugeValue(t *testing.T, reg *prometheus.Registry, name, label, value string) (float64, bool) {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == label && l.GetValue() == value {
					return m.GetGauge().GetValue(), true
				}
			}
		}
	}
	return 0, false
}

// TestClusterWideRateLimit is the acceptance of R-ARCH-034 and R-API-016: three
// web replicas, one token, 1,000 requests within 2 s admit at most 300, with
// Valkey available and with it unavailable, and a second token is not slowed down.
func TestClusterWideRateLimit(t *testing.T) {
	val := apitest.New(t, v1.EmbeddedYAML())
	t.Run("R-ARCH-034 R-API-016 with Valkey: at most 300 admitted, 429 with Retry-After, the second token unaffected", func(t *testing.T) {
		e := newEnv(t)
		nodes := []*node{
			e.newNode("web-1", nodeOpts{valkey: true, liveN: 3, val: val}), e.newNode("web-2", nodeOpts{valkey: true, liveN: 3, val: val}),
			e.newNode("web-3", nodeOpts{valkey: true, liveN: 3, val: val}),
		}
		admitted, refused, elapsed, noRetry := burstOf(t, nodes, uuidV4(), 1000)
		t.Logf("admitted %d, refused %d in %s", admitted, refused, elapsed)
		if elapsed > 2*time.Second {
			t.Fatalf("the burst took %s; the bound holds for 2 s", elapsed)
		}
		if admitted > 300 || admitted < 150 || noRetry != 0 || refused != 1000-admitted {
			t.Fatalf("admitted %d refused %d without Retry-After %d", admitted, refused, noRetry)
		}
		other := uuidV4()
		req, _ := http.NewRequest("GET", nodes[1].url()+"/api/v1/system/version", nil)
		req.Header.Set("X-Test-Principal", other+":"+other)
		res, err := keepAlive.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != 200 || res.Header.Get("RateLimit-Policy") != `"read";q=200;w=4` {
			t.Fatalf("second token: %d policy %q", res.StatusCode, res.Header.Get("RateLimit-Policy"))
		}
		for _, n := range nodes {
			if v, ok := gaugeValue(t, n.reg, "trove_ratelimit_fallback", "limiter", "api_read"); !ok || v != 0 {
				t.Errorf("%s: trove_ratelimit_fallback = %v (%v) with Valkey running", n.name, v, ok)
			}
		}
	})

	for _, mode := range []string{"the client is closed", "no Sentinel answers"} {
		t.Run("R-ARCH-034 R-API-016 with Valkey unavailable ("+mode+"): at most 300 admitted and the fallback gauge is 1", func(t *testing.T) {
			e := newEnv(t)
			var nodes []*node
			for _, name := range []string{"web-1", "web-2", "web-3"} {
				nodes = append(nodes, e.newNodeWithDeadValkey(name, mode == "the client is closed", val))
			}
			admitted, refused, elapsed, noRetry := burstOf(t, nodes, uuidV4(), 1000)
			t.Logf("admitted %d, refused %d in %s", admitted, refused, elapsed)
			if elapsed > 2*time.Second {
				t.Fatalf("the burst took %s; the bound holds for 2 s", elapsed)
			}
			if admitted > 300 || admitted < 90 || noRetry != 0 {
				t.Fatalf("admitted %d refused %d without Retry-After %d", admitted, refused, noRetry)
			}
			for _, n := range nodes {
				if v, ok := gaugeValue(t, n.reg, "trove_ratelimit_fallback", "limiter", "api_read"); !ok || v != 1 {
					t.Errorf("%s: trove_ratelimit_fallback{limiter=api_read} = %v (%v), want 1", n.name, v, ok)
				}
			}
		})
	}
}

// newNodeWithDeadValkey builds a web replica whose Valkey client is unusable: it
// is closed after start (closeIt), or it never connected (a Sentinel that does
// not answer, Q-HA-13).
func (e *env) newNodeWithDeadValkey(name string, closeIt bool, val *apitest.Validator) *node {
	if closeIt {
		n := e.newNode(name, nodeOpts{valkey: true, liveN: 3, val: val})
		n.vk.Close()
		return n
	}
	n := e.newNode(name, nodeOpts{valkey: false, liveN: 3, val: val, deadValkey: true})
	return n
}

// TestRateLimitAddsNoDelayOnceTheCircuitIsOpen is the third criterion of
// R-ARCH-034: with latency in front of Valkey, the limiter adds at most
// valkey.command_timeout to at most failure_threshold checks, and nothing once
// the breaker is open.
func TestRateLimitAddsNoDelayOnceTheCircuitIsOpen(t *testing.T) {
	e := newEnv(t)
	n := e.newNode("web-1", nodeOpts{valkey: true, liveN: 1, limits: &config.APIRateLimits{
		Enabled: true, Read: config.APIRateLimitRead{RequestsPerSecond: 1000, Burst: 100000},
		Write: config.APIRateLimitWrite{RequestsPerSecond: 10, Burst: 50}, Anonymous: config.APIRateLimitAnonymous{RequestsPerSecond: 5, Burst: 20},
	}})
	v := testenv.ValkeyConfig(t)
	primary := n.vk.Status().Primary
	ca, err := os.ReadFile(v.CAFile)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca)
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

	token := uuidV4()
	get := func() time.Duration {
		req, _ := http.NewRequest("GET", n.url()+"/api/v1/system/version", nil)
		req.Header.Set("X-Test-Principal", token+":"+token)
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
	for i := 0; i < 12 && n.vk.Available(); i++ {
		if d := get(); d > 400*time.Millisecond {
			slow++
			if d > 900*time.Millisecond {
				t.Fatalf("check %d waited %s, more than valkey.command_timeout", i, d)
			}
		}
	}
	if n.vk.Available() {
		t.Fatalf("Valkey is still available under the pause: %+v", n.vk.Status())
	}
	t.Logf("%d delayed checks before the client turned unavailable: %+v", slow, n.vk.Status())
	if slow > 5 {
		t.Fatalf("%d checks were delayed, want at most failure_threshold (5)", slow)
	}
	for i := range 20 {
		if d := get(); d > 100*time.Millisecond {
			t.Fatalf("request %d took %s with the circuit open", i, d)
		}
	}
}
