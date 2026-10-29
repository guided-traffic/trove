package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
)

// stats summarises durations in milliseconds.
type stats struct {
	N   int     `json:"n"`
	P50 float64 `json:"p50_ms"`
	P99 float64 `json:"p99_ms"`
	Max float64 `json:"max_ms"`
}

func summarise(d []time.Duration) stats {
	if len(d) == 0 {
		return stats{}
	}
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	at := func(p float64) float64 {
		i := min(len(d)-1, int(float64(len(d))*p+0.5)-1)
		return float64(d[max(i, 0)].Microseconds()) / 1000
	}
	return stats{N: len(d), P50: at(0.5), P99: at(0.99), Max: at(1)}
}

// changeSample is one written change and the time its eviction took to reach the follower.
type changeSample struct {
	lat           time.Duration
	valkeyAtStart bool
	valkeyAtEnd   bool
	reached       bool
}

type jobSample struct {
	id            string
	enqueuedLocal time.Time
	valkey        bool
}

// loadCmd writes one change through peer A and enqueues one job per tick. Peer B
// follows the change log like a second web replica and measures when a cached
// value that the change names is evicted (P-13). The pick-up of the jobs (P-14)
// is the database's started_at minus its created_at, both from the database's
// clock.
func loadCmd(ctx context.Context, cfg *config.Config, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("load", flag.ContinueOnError)
	duration := fs.Duration("duration", time.Minute, "how long to write")
	every := fs.Duration("every", time.Second, "tick")
	if err := fs.Parse(args); err != nil {
		return err
	}
	a, err := openPeer(ctx, cfg, instance("e2e-writer"))
	if err != nil {
		return err
	}
	defer a.close()
	caches, err := cache.NewRegistry(nil, nil)
	if err != nil {
		return err
	}
	b, err := openPeer(ctx, cfg, instance("e2e-follower"), caches)
	if err != nil {
		return err
	}
	defer b.close()
	caches.SetEpochs(b.changes)
	return runLoad(ctx, cfg, a, b, caches, *duration, *every, out)
}

func runLoad(ctx context.Context, cfg *config.Config, a, b *peer, caches *cache.Registry, duration, every time.Duration, out io.Writer) error {
	followed := cache.Register[string, string](caches, "e2e_followed", cache.Options[string, string]{
		MaxEntries: 100000, SafetyTTL: time.Hour, Areas: []string{cluster.AreaCache},
		OnChange: func(c *cache.Cache[string, string], _ string, _ int, p json.RawMessage) {
			var v struct{ Key string }
			if json.Unmarshal(p, &v) == nil {
				c.Invalidate(v.Key)
			}
		},
	})

	var (
		mu           sync.Mutex
		changes      []changeSample
		jobsEnq      []jobSample
		changeErrs   int
		jobErrs      int
		firstErr     = map[string]string{}
		changeWG     sync.WaitGroup
		changesTotal int
	)
	noteErr := func(kind string, err error) {
		if _, ok := firstErr[kind]; !ok {
			firstErr[kind] = err.Error()
		}
	}
	_ = os.Remove(stopFile)
	end := time.Now().Add(duration)
	tick := time.NewTicker(every)
	defer tick.Stop()
	for n := 0; time.Now().Before(end); n++ {
		select {
		case <-ctx.Done():
		case <-tick.C:
		}
		if ctx.Err() != nil {
			break
		}
		if _, err := os.Stat(stopFile); err == nil {
			break
		}
		key := fmt.Sprintf("e2e-%d-%d", time.Now().UnixNano(), n)
		followed.Set(key, "old")
		startValkey := a.vk.Available() && b.vk.Available()
		t0 := time.Now()
		wctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := a.db.Tx(wctx, func(tx meta.Tx) error {
			return a.changes.Notify(tx, cluster.AreaCache, 1, map[string]string{"key": key})
		})
		cancel()
		mu.Lock()
		changesTotal++
		if err != nil {
			changeErrs++
			noteErr("change", err)
		}
		mu.Unlock()
		if err == nil {
			changeWG.Add(1)
			go func() {
				defer changeWG.Done()
				s := changeSample{valkeyAtStart: startValkey}
				for time.Since(t0) < 60*time.Second {
					if _, ok := followed.Get(key); !ok {
						s.reached, s.lat = true, time.Since(t0)
						break
					}
					time.Sleep(time.Millisecond)
				}
				s.valkeyAtEnd = a.vk.Available() && b.vk.Available()
				mu.Lock()
				changes = append(changes, s)
				mu.Unlock()
			}()
		}

		jctx, jcancel := context.WithTimeout(ctx, 20*time.Second)
		res, jerr := a.queue.EnqueueNow(jctx, jobs.EnqueueSpec{Kind: kindProbe, Args: probeArgs{Steps: 1, IntervalMS: 1}})
		jcancel()
		mu.Lock()
		if jerr != nil {
			jobErrs++
			noteErr("job", jerr)
		} else {
			jobsEnq = append(jobsEnq, jobSample{id: res.Job.ID, enqueuedLocal: time.Now(), valkey: a.vk.Available()})
		}
		mu.Unlock()
	}
	changeWG.Wait()

	// Let the last jobs run; then read the pick-up from the rows.
	var pickups, pickupsValkey, pickupsNoValkey []time.Duration
	unfinished := 0
	deadline := time.Now().Add(60 * time.Second)
	for _, js := range jobsEnq {
		for {
			j, ok, err := a.queue.Get(context.WithoutCancel(ctx), js.id)
			if err == nil && ok && j.Terminal() {
				d := j.StartedAt.Sub(j.CreatedAt)
				pickups = append(pickups, d)
				if js.valkey {
					pickupsValkey = append(pickupsValkey, d)
				} else {
					pickupsNoValkey = append(pickupsNoValkey, d)
				}
				break
			}
			if time.Now().After(deadline) {
				unfinished++
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
	}

	var all, withV, withoutV, mixed []time.Duration
	unreached := 0
	for _, c := range changes {
		if !c.reached {
			unreached++
			continue
		}
		all = append(all, c.lat)
		switch {
		case c.valkeyAtStart && c.valkeyAtEnd:
			withV = append(withV, c.lat)
		case !c.valkeyAtStart && !c.valkeyAtEnd:
			withoutV = append(withoutV, c.lat)
		default:
			mixed = append(mixed, c.lat)
		}
	}
	return printJSON(out, map[string]any{
		"host":   hostname(),
		"bounds": map[string]int64{"epoch_poll_interval_ms": cfg.Cluster.EpochPollInterval.Milliseconds(), "jobs_poll_interval_ms": cfg.Jobs.PollInterval.Milliseconds()},
		"changes": map[string]any{
			"written": changesTotal - changeErrs, "write_errors": changeErrs, "not_propagated": unreached,
			"all": summarise(all), "valkey_up": summarise(withV), "valkey_down": summarise(withoutV), "valkey_transition": summarise(mixed),
		},
		"jobs": map[string]any{
			"enqueued": len(jobsEnq), "enqueue_errors": jobErrs, "unfinished": unfinished,
			"all": summarise(pickups), "valkey_up": summarise(pickupsValkey), "valkey_down": summarise(pickupsNoValkey),
		},
		"first_errors": firstErr,
	})
}

func hostname() string { h, _ := os.Hostname(); return h }
