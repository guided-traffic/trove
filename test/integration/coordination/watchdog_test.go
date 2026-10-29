//go:build integration

package coordination

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/observability/health"
)

// recordingWatchdog hands out the real health loops and remembers them by name.
type recordingWatchdog struct {
	h     *health.Health
	mu    sync.Mutex
	loops map[string]*health.Loop
}

func (w *recordingWatchdog) Loop(name string, max time.Duration) *health.Loop {
	l := w.h.Loop(name, max)
	w.mu.Lock()
	w.loops[name] = l
	w.mu.Unlock()
	return l
}

func (w *recordingWatchdog) loop(name string) *health.Loop {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.loops[name]
}

// TestClaimLoopWatchdog is the fault-injection criterion of R-ARCH-016: the claim
// statement is held by pg_sleep for longer than health.watchdog_timeout; each
// claim ends with a deadline error, jobs.claim keeps recording progress and
// liveness stays up.
func TestClaimLoopWatchdog(t *testing.T) {
	e := newEnv(t)
	wd := &recordingWatchdog{h: health.New(config.Health{CheckInterval: time.Second, CheckTimeout: time.Second, FailureThreshold: 2, SuccessThreshold: 1, WatchdogTimeout: 2 * time.Second}), loops: map[string]*health.Loop{}}
	ready := func() bool { return true }

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, e.cfg, "wd-worker", "worker", quiet)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	reg := jobs.NewRegistry()
	reg.RegisterKind("it.wd", jobs.KindSpec{Handler: func(context.Context, *jobs.Run) error { return nil }})
	jc := defaultJobs()
	jc.PollInterval = 200 * time.Millisecond
	w, err := jobs.NewWorker(jobs.WorkerOptions{
		Role: "worker", UID: uuidV4(), Main: db.Main(), Control: db.ControlPool(), Registry: reg, Config: jc,
		Ready: ready, Watchdog: wd, WatchdogTimeout: 2 * time.Second, Log: quiet,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Stop(context.Background()) }()

	// Every update of a job now takes 4 s, twice the watchdog bound.
	e.exec(`CREATE FUNCTION trove.slow_claim() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(4); RETURN NEW; END $$`)
	e.exec(`CREATE TRIGGER slow_claim BEFORE UPDATE ON trove.job FOR EACH ROW EXECUTE FUNCTION trove.slow_claim()`)
	e.exec(`INSERT INTO job (id, kind) VALUES ('00000000-0000-7000-8000-00000000c1a1', 'it.wd')`)

	t.Run("R-ARCH-016 a claim held past the watchdog bound ends with a deadline error and the loop keeps recording progress", func(t *testing.T) {
		loop := wd.loop("jobs.claim")
		if loop == nil {
			t.Fatal("the claim loop is not watched as jobs.claim")
		}
		start := time.Now()
		last := loop.Last()
		advances := 0
		for time.Since(start) < 7*time.Second {
			time.Sleep(100 * time.Millisecond)
			if now := loop.Last(); now.After(last) {
				advances++
				if now.Sub(last) > 2*time.Second {
					t.Fatalf("the loop went %s without progress, above the watchdog bound of 2s", now.Sub(last))
				}
				last = now
			}
		}
		if advances < 3 {
			t.Fatalf("the loop recorded progress only %d times in 7 s of blocked claims", advances)
		}
		snap := wd.h.Snapshot()
		if len(snap.Failing) != 0 {
			t.Fatalf("failing checks: %v", snap.Failing)
		}
		if j := e.scalar(`SELECT count(*) FROM job WHERE id = '00000000-0000-7000-8000-00000000c1a1' AND state = 'queued' AND attempt = 0`); j != 1 {
			t.Fatal("the job was claimed although every claim hit its deadline")
		}
	})

	t.Run("R-ARCH-016 once the statement is fast again the job is claimed", func(t *testing.T) {
		e.exec(`DROP TRIGGER slow_claim ON trove.job`)
		waitFor(t, "the job to succeed", 15*time.Second, func() bool {
			return e.scalar(`SELECT count(*) FROM job WHERE id = '00000000-0000-7000-8000-00000000c1a1' AND state = 'succeeded'`) == 1
		})
	})
}
