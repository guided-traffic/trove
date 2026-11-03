//go:build integration

package bastion

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/bastion/maintest"
)

// TestR_BAS_035_PerAddressLimits: requests per address are limited in two classes, a second address is not
// slowed down, and a refused miss answers 429 rate_limited while the same address's hits keep being served.
func TestR_BAS_035_PerAddressLimits(t *testing.T) {
	w := newWorld(t, worldOptions{})
	w.content()
	w.anonymousPolicy()
	w.setAnonymous(true)
	for i := 0; i < 16; i++ {
		w.publishNPM(fmt.Sprintf("miss-%d", i), "1.0.0")
	}
	doc := "/api/npm/" + npmAll + "/" + escapeNPM("@acme/sdk")

	for _, v := range []variant{{"no Valkey", bastionOptions{}}, {"Valkey, two replicas", bastionOptions{Valkey: true, Replicas: 2}}} {
		t.Run("requests/"+v.name, func(t *testing.T) {
			v.o.Settings = "  limits:\n    anonymous:\n      requests_per_second: 2\n      burst: 6\n    authenticated:\n      requests_per_second: 2\n      burst: 8\n" +
				"  blocking:\n    threshold: 100000\n"
			b := w.newBastion(t, v.o).wait()
			for _, class := range []struct {
				name  string
				tok   string
				burst int
			}{{"anonymous", "", 6}, {"authenticated", w.full.token, 8}} {
				addr := "198.51.100." + map[string]string{"anonymous": "111", "authenticated": "112"}[class.name]
				ok, limited := 0, 0
				for i := 0; i < 60; i++ {
					got := b.reps[0].do(t, "GET", doc, callOpts{token: class.tok, addr: addr})
					switch got.Status {
					case http.StatusOK:
						ok++
					case http.StatusTooManyRequests:
						limited++
						if got.Code() != "rate_limited" || got.Header.Get("Retry-After") == "" {
							t.Fatalf("%s: 429 %q, Retry-After %q", class.name, got.Code(), got.Header.Get("Retry-After"))
						}
					default:
						t.Fatalf("%s: %d %s", class.name, got.Status, got.Body)
					}
				}
				// burst, plus what the rate refills while the loop runs (a few seconds at most), divided over replicas
				if limited == 0 || ok > class.burst+2*10 {
					t.Fatalf("%s: %d admitted, %d limited of 60 (burst %d)", class.name, ok, limited, class.burst)
				}
				if got := b.reps[0].do(t, "GET", doc, callOpts{token: class.tok, addr: "203.0.113.200"}); got.Status != http.StatusOK {
					t.Fatalf("%s: a second address was slowed down: %d", class.name, got.Status)
				}
			}
		})
	}

	t.Run("misses", func(t *testing.T) {
		b := w.newBastion(t, bastionOptions{Settings: relaxed[:strings.Index(relaxed, "    misses:")] + "    misses:\n      authenticated_per_minute: 4\n      anonymous_per_minute: 3\n  blocking:\n    threshold: 100000\n"}).wait()
		r := b.reps[0]
		for _, class := range []struct {
			name, tok, addr string
			allowed, first  int
		}{{"authenticated", w.full.token, "198.51.100.121", 4, 0}, {"anonymous", "", "198.51.100.122", 3, 8}} {
			if got := r.do(t, "GET", doc, callOpts{token: class.tok, addr: class.addr}); got.Status != http.StatusOK { // a hit-to-be: one miss
				t.Fatalf("%s: %d", class.name, got.Status)
			}
			served, refused := 1, 0
			for i := 0; i < 8; i++ {
				got := r.do(t, "GET", tarballPath(npmAll, fmt.Sprintf("miss-%d", class.first+i), "1.0.0"), callOpts{token: class.tok, addr: class.addr})
				switch got.Status {
				case http.StatusOK:
					served++
				case http.StatusTooManyRequests:
					refused++
					if got.Code() != "rate_limited" {
						t.Fatalf("%s: code %q", class.name, got.Code())
					}
				default:
					t.Fatalf("%s: %d", class.name, got.Status)
				}
			}
			if served > class.allowed || refused == 0 {
				t.Fatalf("%s: %d misses served, %d refused (limit %d a minute)", class.name, served, refused, class.allowed)
			}
			if got := r.do(t, "GET", doc, callOpts{token: class.tok, addr: class.addr}); got.Status != http.StatusOK {
				t.Fatalf("%s: a hit of the address was refused with %d", class.name, got.Status)
			}
		}
		// Hits and the main installation: the refused misses never reached it.
		m := w.front.Mark()
		r.do(t, "GET", tarballPath(npmAll, "miss-7", "1.0.0"), callOpts{token: w.full.token, addr: "198.51.100.121"})
		if n := len(w.front.Forwards(m, "/api/")); n != 0 {
			t.Fatalf("a refused miss reached the main installation (%d requests)", n)
		}
	})
}

// TestR_BAS_036_FailedAuthenticationTimeout: the failure that brings the address to max_failures starts the
// timeout; the next credential without a usable answer is refused 429 auth_timeout and never sent to the main
// installation; a credential with a usable answer keeps working; another address is unaffected.
func TestR_BAS_036_FailedAuthenticationTimeout(t *testing.T) {
	w := newWorld(t, worldOptions{})
	w.content()
	for _, v := range []variant{{"no Valkey", bastionOptions{}}, {"Valkey, two replicas", bastionOptions{Valkey: true, Replicas: 2}}} {
		t.Run(v.name, func(t *testing.T) {
			v.o.Settings = relaxed + "  failed_auth:\n    max_failures: 3\n    window: 10m\n    timeout: 15m\n  blocking:\n    threshold: 100000\n"
			b := w.newBastion(t, v.o).wait()
			path := tarballPath(npmAll, "@acme/sdk", "1.0.0")
			addr, other := "198.51.100.131", "198.51.100.132"
			// $FULL is used from the address before: its answer is usable.
			if got := b.reps[0].do(t, "GET", path, callOpts{token: w.full.token, addr: addr}); got.Status != http.StatusOK {
				t.Fatalf("$FULL: %d", got.Status)
			}
			for i := 0; i < 3; i++ {
				r := b.reps[i%len(b.reps)]
				got := r.do(t, "GET", path, callOpts{token: maintest.GenerateTokenWithPrefix(t, "trv_pat_"), addr: addr})
				if got.Status != http.StatusUnauthorized {
					t.Fatalf("unknown token %d: %d", i+1, got.Status)
				}
			}
			m := w.front.Mark()
			for _, r := range b.reps {
				got := r.do(t, "GET", path, callOpts{token: maintest.GenerateTokenWithPrefix(t, "trv_pat_"), addr: addr})
				if got.Status != http.StatusTooManyRequests || got.Code() != "auth_timeout" || got.Header.Get("Retry-After") == "" {
					t.Fatalf("%s: unknown token in the timeout: %d %s", r.name, got.Status, got.Code())
				}
			}
			if n := len(w.front.Forwards(m, "/api/")) + len(w.front.Kind(m, "check")); n != 0 {
				t.Fatalf("%d requests for an unverified credential reached the main installation during the timeout", n)
			}
			for _, r := range b.reps {
				if got := r.do(t, "GET", path, callOpts{token: w.full.token, addr: addr}); got.Status != http.StatusOK {
					t.Fatalf("%s: $FULL from the address in the timeout: %d", r.name, got.Status)
				}
				if got := r.do(t, "GET", path, callOpts{token: maintest.GenerateTokenWithPrefix(t, "trv_pat_"), addr: other}); got.Status != http.StatusUnauthorized {
					t.Fatalf("%s: another address got %d, want the main installation's 401", r.name, got.Status)
				}
			}
			// An anonymous request from the address stays an anonymous request (refused: the switch is off).
			if got := b.reps[0].do(t, "GET", path, callOpts{addr: addr}); got.Status != http.StatusUnauthorized || got.Code() == "auth_timeout" {
				t.Fatalf("anonymous from the address in the timeout: %d %s", got.Status, got.Code())
			}
		})
	}
}

// TestR_BAS_037_BlockingOfFloodingAddresses: an address that collects more refusals than the threshold in the
// window is blocked (429 address_blocked with Retry-After, one log line per block, not one per request); with
// Valkey the block holds on every replica, without it each replica blocks by its own count (H-389); another
// address is unaffected.
func TestR_BAS_037_BlockingOfFloodingAddresses(t *testing.T) {
	w := newWorld(t, worldOptions{})
	w.content()
	w.anonymousPolicy()
	w.setAnonymous(true)
	doc := "/api/npm/" + npmAll + "/" + escapeNPM("@acme/sdk")
	for _, v := range []variant{{"no Valkey", bastionOptions{Replicas: 2}}, {"Valkey", bastionOptions{Valkey: true, Replicas: 2}}} {
		t.Run(v.name, func(t *testing.T) {
			v.o.Settings = "  limits:\n    anonymous:\n      requests_per_second: 1\n      burst: 2\n  blocking:\n    threshold: 10\n    window: 1m\n    duration: 15m\n"
			b := w.newBastion(t, v.o).wait()
			flood := "198.51.100.141"
			blocked := 0
			for i := 0; i < 60 && blocked == 0; i++ {
				got := b.reps[0].do(t, "GET", doc, callOpts{addr: flood})
				if got.Status == http.StatusTooManyRequests && got.Code() == "address_blocked" {
					blocked++
					if got.Header.Get("Retry-After") == "" {
						t.Fatal("address_blocked without Retry-After")
					}
				}
			}
			if blocked == 0 {
				t.Fatal("the flooding address was not blocked")
			}
			// Even a credentialed request of the address is refused at step 2.
			if got := b.reps[0].do(t, "GET", doc, callOpts{token: w.full.token, addr: flood}); got.Code() != "address_blocked" {
				t.Fatalf("a credentialed request of a blocked address: %d %s", got.Status, got.Code())
			}
			other := b.reps[1].do(t, "GET", doc, callOpts{token: w.full.token, addr: flood})
			if v.o.Valkey {
				if other.Code() != "address_blocked" {
					t.Fatalf("with Valkey the block does not hold on the other replica: %d %s", other.Status, other.Code())
				}
			} else if other.Code() == "address_blocked" {
				t.Fatalf("without Valkey a replica blocked by another's count")
			}
			if got := b.reps[0].do(t, "GET", doc, callOpts{token: w.full.token, addr: "198.51.100.142"}); got.Status != http.StatusOK {
				t.Fatalf("another address: %d", got.Status)
			}
			if n := strings.Count(b.reps[0].logs.String(), "bastion address blocked"); n != 1 {
				t.Fatalf("%d log lines for the block, want one", n)
			}
			if sumMetric(b.reps[0].metrics(t), "trove_bastion_blocks_total", `reason="refusals"`) != 1 {
				t.Fatal("trove_bastion_blocks_total{reason=\"refusals\"} is not 1")
			}
		})
	}
}
