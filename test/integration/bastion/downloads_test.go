//go:build integration

package bastion

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"
)

// TestR_BAS_016_R_BAS_039_DownloadsServedFromTheCacheAreAuditedAtTheMainInstallation: a cold run is recorded by
// the main installation as the caller's own downloads (no origin); a warm run served from the cache is reported
// by the Bastion and recorded as artifact.downloaded with origin bastion, the caller's token ID and address and
// the content address, within two flush intervals; no download is counted twice.
func TestR_BAS_016_R_BAS_039_DownloadsServedFromTheCacheAreAuditedAtTheMainInstallation(t *testing.T) {
	w := newWorld(t, worldOptions{})
	const n = 6
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("dl-lib-%d", i)
		w.publishNPM(names[i], "1.0.0")
	}
	for _, v := range []variant{variants()[0], variants()[2]} {
		t.Run(v.name, func(t *testing.T) {
			b := w.newBastion(t, v.o).wait()
			r := b.reps[len(b.reps)-1]
			addr := "203.0.113.55"
			if v.name == "volume" {
				addr = "203.0.113.54"
			}
			c := callOpts{token: w.full.token, addr: addr}
			// Each variant works on its own caller address, so the audit rows of the runs can be told apart.
			count := func(origin string) int64 {
				return w.st.Number(`SELECT count(*) FROM audit_event WHERE action = 'artifact.downloaded' AND host(source_ip) = $1
					AND path LIKE '%.tgz' AND token_id = $2::uuid AND `+origin, addr, w.full.id)
			}
			const viaBastion, direct = "details->>'origin' = 'bastion'", "details->>'origin' IS NULL"

			for _, name := range names {
				if got := r.do(t, "GET", tarballPath(npmAll, name, "1.0.0"), c); got.Status != http.StatusOK {
					t.Fatalf("cold %s: %d", name, got.Status)
				}
			}
			testWait(t, "the main installation's record of the cold run", func() bool { return count(direct) >= n })
			if got := count(viaBastion); got != 0 {
				t.Fatalf("the cold run was reported by the Bastion as well: %d events", got)
			}

			m := w.front.Mark()
			for _, name := range names {
				if got := r.do(t, "GET", tarballPath(npmAll, name, "1.0.0"), c); got.Status != http.StatusOK {
					t.Fatalf("warm %s: %d", name, got.Status)
				}
			}
			if len(w.front.Artifacts(m)) != 0 {
				t.Fatal("the warm run fetched an artifact")
			}
			started := time.Now()
			testWait(t, "the Bastion's report of the warm run", func() bool { return count(viaBastion) >= n })
			t.Logf("reported after %v", time.Since(started).Round(time.Millisecond))
			if got := count(viaBastion); got != n {
				t.Fatalf("%d events with origin bastion, want %d", got, n)
			}
			if got := count(direct); got != n {
				t.Fatalf("%d direct events, want %d (no download is counted twice)", got, n)
			}
			if got := w.st.Number(`SELECT count(*) FROM audit_event WHERE action = 'artifact.downloaded' AND host(source_ip) = $1
				AND details->>'origin' = 'bastion' AND details->>'bastion' = 'partners' AND coalesce(details->>'replica', '') <> '' AND length(details->>'sha256') = 64`, addr); got != n {
				t.Fatalf("%d events carry the registration, the replica and the content address, want %d", got, n)
			}
		})
	}
}

// TestR_BAS_025_TheBudgetAndCollapsedMisses: a flood of distinct cold names reaches the main installation with at
// most max_concurrent requests in flight and the rest is told 503 bastion_busy; many requests for one cold
// artifact cost one request per replica, and one in total with Valkey and the bucket backend.
func TestR_BAS_025_TheBudgetAndCollapsedMisses(t *testing.T) {
	w := newWorld(t, worldOptions{})
	const names = 12
	for i := 0; i < names; i++ {
		w.publishNPM(fmt.Sprintf("flood-%d", i), "1.0.0")
	}
	w.publishNPM("collapse-me", "1.0.0")
	slow := func(req *http.Request) {
		if kindOf(req) == "forward" {
			time.Sleep(400 * time.Millisecond)
		}
	}

	t.Run("the budget", func(t *testing.T) {
		b := w.newBastion(t, bastionOptions{Settings: relaxed, MainSettings: "    max_concurrent: 2\n    queue_timeout: 100ms\n"}).wait()
		r := b.reps[0]
		w.front.SetGate(slow)
		defer w.front.SetGate(nil)
		w.front.ResetPeak()
		var wg sync.WaitGroup
		var mu sync.Mutex
		codes := map[int]int{}
		for i := 0; i < names; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				got := r.do(t, "GET", tarballPath(npmAll, fmt.Sprintf("flood-%d", i), "1.0.0"), callOpts{token: w.full.token, addr: "198.51.100.101"})
				mu.Lock()
				codes[got.Status]++
				mu.Unlock()
				if got.Status == http.StatusServiceUnavailable && got.Code() != "bastion_busy" {
					t.Errorf("503 with code %q", got.Code())
				}
			}()
		}
		wg.Wait()
		if p := w.front.Peak(); p > 2 {
			t.Fatalf("%d forwarded reads were in flight at the main installation, max_concurrent is 2", p)
		}
		if codes[http.StatusServiceUnavailable] == 0 || codes[http.StatusOK] == 0 {
			t.Fatalf("answers: %v (some served, some busy expected)", codes)
		}
	})

	for _, v := range []variant{{"volume, one replica", bastionOptions{Backend: "volume"}}, {"bucket and Valkey, two replicas", bastionOptions{Backend: "bucket", Valkey: true, Replicas: 2}}} {
		t.Run("collapsing/"+v.name, func(t *testing.T) {
			v.o.Settings = relaxed
			b := w.newBastion(t, v.o).wait()
			w.front.SetGate(slow)
			defer w.front.SetGate(nil)
			path := tarballPath(npmAll, "collapse-me", "1.0.0")
			m := w.front.Mark()
			var wg sync.WaitGroup
			for i := 0; i < 20; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					r := b.reps[i%len(b.reps)]
					if got := r.do(t, "GET", path, callOpts{token: w.full.token, addr: "198.51.100.102"}); got.Status != http.StatusOK {
						t.Errorf("request %d: %d", i, got.Status)
					}
				}()
			}
			wg.Wait()
			got := len(w.front.Forwards(m, path))
			want := len(b.reps)
			if v.o.Valkey && v.o.Backend == "bucket" {
				want = 1 // the fetch lock of the shared state makes the second replica wait (R-BAS-034)
			}
			if got != want {
				t.Fatalf("20 requests for one cold artifact cost %d requests at the main installation, want %d", got, want)
			}
		})
	}
}
