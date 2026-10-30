//go:build integration

package coordination

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
)

// measure reports the distribution of d; with fewer than 100 samples the p99 is
// the maximum, the strictest reading.
func report(t *testing.T, what string, d []time.Duration) (p50, p99 time.Duration) {
	t.Helper()
	p50, p99 = percentile(d, 0.5), percentile(d, 0.99)
	t.Logf("%s: n=%d p50=%s p99=%s max=%s", what, len(d), p50.Round(time.Microsecond), p99.Round(time.Microsecond), percentile(d, 1).Round(time.Microsecond))
	return p50, p99
}

// TestJobPickup is P-14: the time from the enqueue to the start of the handler.
func TestJobPickup(t *testing.T) {
	for _, mode := range []struct {
		name   string
		valkey vkMode
		poll   time.Duration
		bound  func(poll time.Duration) time.Duration
		n      int
		spread time.Duration
	}{
		// With Valkey the poll is set far above the bound, so only the signal
		// can explain a pick-up inside it.
		{"with Valkey", vkLive, 5 * time.Second, func(time.Duration) time.Duration { return time.Second }, 60, 250 * time.Millisecond},
		{"without Valkey", vkDead, time.Second, func(poll time.Duration) time.Duration { return poll + time.Second }, 40, 1000 * time.Millisecond},
	} {
		t.Run("R-ARCH-027 P-14 job pick-up "+mode.name, func(t *testing.T) {
			e := newEnv(t)
			var mu sync.Mutex
			started := map[string]time.Time{}
			kinds := func(r *jobs.Registry, rep *replica) {
				r.RegisterKind("it.pickup", jobs.KindSpec{Handler: func(ctx context.Context, run *jobs.Run) error {
					now := time.Now()
					mu.Lock()
					started[run.ID] = now
					mu.Unlock()
					return nil
				}})
			}
			jc := defaultJobs()
			jc.PollInterval = mode.poll
			web := e.newReplica("pick-web", replicaOpts{role: "web", valkey: mode.valkey, kinds: kinds, jobs: jc})
			w := e.newReplica("pick-worker", replicaOpts{role: "worker", valkey: mode.valkey, kinds: kinds, jobs: jc})
			if mode.valkey == vkLive {
				waitValkey(t, web)
				waitValkey(t, w)
				time.Sleep(300 * time.Millisecond) // the worker's Pub/Sub connection has subscribed
			}
			enqueued := map[string]time.Time{}
			for range mode.n {
				time.Sleep(time.Duration(rand.Int64N(int64(mode.spread))))
				t0 := time.Now()
				res, err := web.enqueueTx(context.Background(), jobs.EnqueueSpec{Kind: "it.pickup"})
				if err != nil {
					t.Fatal(err)
				}
				enqueued[res.Job.ID] = t0
			}
			waitFor(t, "every job to start", 20*time.Second, func() bool {
				mu.Lock()
				defer mu.Unlock()
				return len(started) == len(enqueued)
			})
			var lat []time.Duration
			mu.Lock()
			for id, t0 := range enqueued {
				lat = append(lat, started[id].Sub(t0))
			}
			mu.Unlock()
			_, p99 := report(t, "pick-up "+mode.name, lat)
			if bound := mode.bound(mode.poll); p99 > bound {
				t.Fatalf("p99 %s above the bound %s", p99, bound)
			}
		})
	}
}

// TestChangePropagation is P-13: the time from the commit of a change on one
// replica until another replica's cache no longer serves the old value.
func TestChangePropagation(t *testing.T) {
	for _, mode := range []struct {
		name   string
		valkey vkMode
		poll   time.Duration
		bound  func(poll time.Duration) time.Duration
		n      int
		spread time.Duration
	}{
		// 200 samples, so the p99 that P-13 bounds is the second largest and not the
		// maximum: one scheduling outlier of a shared CI runner must not stand for
		// the 99th percentile.
		{"with Valkey", vkLive, 2 * time.Second, func(time.Duration) time.Duration { return 100 * time.Millisecond }, 200, 120 * time.Millisecond},
		{"without Valkey", vkDead, time.Second, func(poll time.Duration) time.Duration { return poll + time.Second }, 30, 400 * time.Millisecond},
	} {
		t.Run("R-ARCH-027 P-13 change propagation "+mode.name, func(t *testing.T) {
			e := newEnv(t)
			cc := defaultCluster()
			cc.EpochPollInterval = mode.poll
			a := e.newReplica("chg-a", replicaOpts{role: "web", valkey: mode.valkey, clu: cc})
			b := e.newReplica("chg-b", replicaOpts{role: "web", valkey: mode.valkey, clu: cc})
			if mode.valkey == vkLive {
				waitValkey(t, a)
				waitValkey(t, b)
				time.Sleep(300 * time.Millisecond)
			}
			// B caches per key and evicts the key a change names.
			cfg := cache.Register[string, string](b.caches, "it_cfg", cache.Options[string, string]{
				MaxEntries: 1000, SafetyTTL: time.Minute, Areas: []string{"cache"},
				OnChange: func(c *cache.Cache[string, string], _ string, _ int, p json.RawMessage) {
					var v struct{ Key string }
					if json.Unmarshal(p, &v) == nil {
						c.Invalidate(v.Key)
					}
				},
			})
			var lat []time.Duration
			for i := range mode.n {
				key := "k" + time.Now().Format("150405.000000") + string(rune('a'+i%26))
				cfg.Set(key, "old")
				time.Sleep(time.Duration(rand.Int64N(int64(mode.spread))))
				// P-13 runs from the commit: the writer's own transaction (its statements
				// and the COMMIT on PostgreSQL) is not part of it. The stamp is the first
				// After hook, so it is taken once the commit returned and before the
				// change is published.
				var t0 time.Time
				err := a.db.InTx(context.Background(), func(tx meta.Tx) error {
					tx.After(func() { t0 = time.Now() })
					return a.changes.Notify(tx, "cache", 1, map[string]string{"key": key})
				})
				if err != nil {
					t.Fatal(err)
				}
				for {
					if _, ok := cfg.Get(key); !ok {
						break
					}
					if time.Since(t0) > 10*time.Second {
						t.Fatalf("change %d never reached replica B", i)
					}
					time.Sleep(500 * time.Microsecond)
				}
				lat = append(lat, time.Since(t0))
			}
			_, p99 := report(t, "propagation "+mode.name, lat)
			if bound := mode.bound(mode.poll); p99 > bound {
				t.Fatalf("p99 %s above the bound %s", p99, bound)
			}
			// The poll applied the same changes afterwards: B's applied epoch caught up.
			waitFor(t, "B's applied epoch", 5*time.Second, func() bool {
				return b.changes.Applied("cache") == a.changes.Applied("cache") || b.changes.Applied("cache") >= int64(mode.n)
			})
		})
	}
}

// TestLateKeyspace shows that a replica which could not read the keyspace ID at
// start-up subscribes as soon as it is set, without a restart: until then only
// the poll carries changes.
func TestLateKeyspace(t *testing.T) {
	e := newEnv(t)
	cc := defaultCluster()
	cc.EpochPollInterval = 5 * time.Second
	jc := defaultJobs()
	jc.PollInterval = 5 * time.Second
	kinds := func(r *jobs.Registry, rep *replica) {
		r.RegisterKind("it.late", jobs.KindSpec{Handler: func(context.Context, *jobs.Run) error { return nil }})
	}
	a := e.newReplica("late-a", replicaOpts{role: "web", valkey: vkLive, clu: cc, lateKeyspace: true, kinds: kinds})
	w := e.newReplica("late-w", replicaOpts{role: "worker", valkey: vkLive, clu: cc, jobs: jc, lateKeyspace: true, kinds: kinds})
	b := e.newReplica("late-b", replicaOpts{role: "web", valkey: vkLive, clu: cc, lateKeyspace: true})

	t.Run("R-ARCH-018 without a keyspace ID the replica starts and polls; with it the fast path begins", func(t *testing.T) {
		for _, r := range []*replica{a, w, b} {
			if err := r.vk.SetKeyspaceID(e.ks); err != nil {
				t.Fatal(err)
			}
		}
		for _, r := range []*replica{a, w, b} {
			waitValkey(t, r)
		}
		held := cache.Register[string, string](b.caches, "it_late", cache.Options[string, string]{MaxEntries: 10, SafetyTTL: time.Minute, Areas: []string{"cache"}})
		held.Set("k", "old")
		// The subscription is retried by the poll loop, whose interval here is 5 s.
		waitFor(t, "the change messages to be subscribed", 15*time.Second, func() bool {
			held.Set("k", "old")
			if err := a.db.InTx(context.Background(), func(tx meta.Tx) error { return a.changes.Notify(tx, "cache", 1, map[string]string{"x": "y"}) }); err != nil {
				t.Fatal(err)
			}
			time.Sleep(150 * time.Millisecond)
			_, ok := held.Get("k")
			return !ok
		})
	})

	t.Run("R-ARCH-016 the job wake-up is subscribed late as well", func(t *testing.T) {
		var lat []time.Duration
		for range 5 {
			t0 := time.Now()
			res, err := a.enqueueTx(context.Background(), jobs.EnqueueSpec{Kind: "it.late"})
			if err != nil {
				t.Fatal(err)
			}
			e.waitState(a, res.Job.ID, jobs.StateSucceeded, 12*time.Second)
			lat = append(lat, time.Since(t0))
		}
		// The first jobs may wait for a poll (5 s) while the subscription is not there yet; the last ones must not.
		if last := lat[len(lat)-1]; last > time.Second {
			t.Fatalf("the last pick-up took %s with the poll at 5 s: the wake-up is not subscribed", last)
		}
	})
}
