package app

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/jobs"
)

// derived returns server.shutdown_timeout the way the loader derives it.
func derived(grace, drain time.Duration) time.Duration {
	c := config.Config{}
	c.Runtime.TerminationGracePeriod = grace
	c.Health.ShutdownDrainDelay = drain
	c.Finalize(map[string]config.Source{})
	return c.Server.ShutdownTimeout
}

func TestShutdownPlan(t *testing.T) {
	t0 := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	b := defaultBudget()

	t.Run("R-HA-011 grace 300 s and drain 15 s: a worker cancels its jobs at 275 s, not at 20 s, and exits before 300 s", func(t *testing.T) {
		timeout := derived(300*time.Second, 15*time.Second)
		if timeout != 280*time.Second {
			t.Fatalf("derived timeout %s, want 280s", timeout)
		}
		p := planShutdown(t0, 0, timeout, b) // a worker skips the drain delay
		if got := p.QuiesceAt.Sub(t0); got != 275*time.Second {
			t.Errorf("quiesce at %s, want 275s", got)
		}
		if got := p.ExitBy.Sub(t0); got >= 300*time.Second {
			t.Errorf("exit deadline %s is not before the grace period of 300s", got)
		}
	})

	t.Run("R-HA-011 a web replica with grace 300 s and drain 15 s exits before 300 s", func(t *testing.T) {
		p := planShutdown(t0, 15*time.Second, derived(300*time.Second, 15*time.Second), b)
		if got := p.ExitBy.Sub(t0); got >= 300*time.Second {
			t.Errorf("exit deadline %s is not before the grace period", got)
		}
		if got := p.ServerBy.Sub(t0); got != 295*time.Second {
			t.Errorf("server phase ends at %s, want 295s (grace - 5 s)", got)
		}
	})

	t.Run("R-ARCH-024 every phase lies inside the grace period and the phases are ordered, for the chart's grace periods and drain delays", func(t *testing.T) {
		for _, grace := range []time.Duration{10 * time.Second, 20 * time.Second, 45 * time.Second, 120 * time.Second, 300 * time.Second, time.Hour} {
			for _, drain := range []time.Duration{0, 5 * time.Second, 15 * time.Second} {
				timeout := derived(grace, drain)
				if grace-drain-config.ShutdownMargin < time.Second {
					continue // the loader refuses this configuration
				}
				for _, web := range []bool{true, false} {
					d := drain
					if !web {
						d = 0
					}
					p := planShutdown(t0, d, timeout, b)
					name := fmt.Sprintf("grace %s drain %s web=%v", grace, drain, web)
					chain := []time.Time{p.Start, p.ReleaseAt, p.QuiesceAt, p.ServerBy, p.CommitBy, p.CloseBy, p.ExitBy}
					for i := 1; i < len(chain); i++ {
						if chain[i].Before(chain[i-1]) {
							t.Errorf("%s: phase %d ends before phase %d: %v", name, i, i-1, chain)
						}
					}
					if p.ExitBy.Sub(t0) >= grace {
						t.Errorf("%s: exit at %s, grace %s", name, p.ExitBy.Sub(t0), grace)
					}
					// The commit grace, the close reserve and the ops reserve all fit between the end of
					// the server phase and the exit deadline.
					if need := p.CommitBy.Sub(p.ServerBy) + b.CloseReserve + b.OpsReserve; need > p.ExitBy.Sub(p.ServerBy) {
						t.Errorf("%s: commit grace + close + ops = %s exceeds the margin %s", name, need, p.ExitBy.Sub(p.ServerBy))
					}
					if p.CommitBy.Sub(p.ServerBy) > b.CommitGrace {
						t.Errorf("%s: commit grace %s exceeds the cap %s", name, p.CommitBy.Sub(p.ServerBy), b.CommitGrace)
					}
				}
			}
		}
	})

	t.Run("R-ARCH-024 a phase that runs long shortens the later ones: the plan is absolute", func(t *testing.T) {
		p := planShutdown(t0, 5*time.Second, 25*time.Second, b)
		// Whatever time the drain or the server phase used, ExitBy stays where it is.
		if p.ExitBy.Sub(t0) != 5*time.Second+25*time.Second+b.Margin-b.Slack {
			t.Errorf("exit deadline %s", p.ExitBy.Sub(t0))
		}
	})

	t.Run("R-ARCH-024 R-HA-011 the job worker is cancelled so that the release writes of its jobs get the whole settle deadline before the close deadline, as far as the shutdown timeout allows", func(t *testing.T) {
		if b.Release != jobs.SettleDeadline {
			t.Fatalf("the budget's release time %s is not the job worker's settle deadline %s", b.Release, jobs.SettleDeadline)
		}
		for _, c := range []struct {
			grace, drain time.Duration
			web          bool
		}{{300 * time.Second, 0, false}, {120 * time.Second, 15 * time.Second, true}, {45 * time.Second, 5 * time.Second, true}, {45 * time.Second, 0, false}} {
			d := c.drain
			if !c.web {
				d = 0
			}
			p := planShutdown(t0, d, derived(c.grace, c.drain), b)
			if got := p.CloseBy.Sub(p.ReleaseAt); got != jobs.SettleDeadline {
				t.Errorf("grace %s drain %s web=%v: the release writes get %s before the close deadline, want %s", c.grace, c.drain, c.web, got, jobs.SettleDeadline)
			}
			if !p.ReleaseAt.Before(p.QuiesceAt) {
				t.Errorf("grace %s: the worker is cancelled at %s, not before the engines at %s", c.grace, p.ReleaseAt.Sub(t0), p.QuiesceAt.Sub(t0))
			}
		}
		// A timeout too short for it cancels the worker when the drain ends and gives the writes what is left.
		p := planShutdown(t0, 5*time.Second, 8*time.Second, b)
		if p.ReleaseAt != t0.Add(5*time.Second) {
			t.Errorf("a short timeout cancels the worker at %s, want the end of the drain", p.ReleaseAt.Sub(t0))
		}
		if got := p.CloseBy.Sub(p.ReleaseAt); got != 8*time.Second+b.Margin-b.Slack-b.OpsReserve {
			t.Errorf("the release writes get %s", got)
		}
	})

	t.Run("R-ARCH-024 a shutdown timeout below the quiesce margin cancels running work at once", func(t *testing.T) {
		p := planShutdown(t0, 5*time.Second, 2*time.Second, b)
		if p.QuiesceAt.Before(t0.Add(5 * time.Second)) {
			t.Errorf("quiesce at %s precedes the end of the drain", p.QuiesceAt.Sub(t0))
		}
	})
}

// blockingComp ignores every context: the worst component a close phase can meet.
type blockingComp struct {
	name     string
	quiesced atomic.Int64 // unix nanos of the Quiesce call
	release  chan struct{}
}

func (c *blockingComp) Name() string                { return c.name }
func (c *blockingComp) Start(context.Context) error { return nil }
func (c *blockingComp) Stop(context.Context) error  { <-c.release; return nil }
func (c *blockingComp) Flush(context.Context) error { <-c.release; return nil }
func (c *blockingComp) Quiesce(context.Context)     { c.quiesced.Store(time.Now().UnixNano()) }
func (c *blockingComp) Settle(ctx context.Context) error {
	<-ctx.Done() // a job that runs until it is cancelled
	return ctx.Err()
}

// scaled is the production budget at one thousandth of the size.
func scaledBudget() shutdownBudget {
	return shutdownBudget{
		Drain:  100 * time.Millisecond,
		Margin: 800 * time.Millisecond, Slack: 100 * time.Millisecond, Quiesce: 600 * time.Millisecond,
		CommitGrace: 500 * time.Millisecond, CloseReserve: 200 * time.Millisecond,
		OpsReserve: 100 * time.Millisecond, Abandon: 50 * time.Millisecond, Release: 2 * time.Second,
	}
}

func TestShutdownBudgetEndToEnd(t *testing.T) {
	t.Run("R-HA-011 a worker with the derived timeout quiesces its jobs near the end of the timeout and exits inside the grace period, although a component ignores its context", func(t *testing.T) {
		// grace 7 s, drain 1 s -> derived timeout 1 s; a worker skips the drain.
		h := start(t, "worker", map[string]string{
			"TROVE_RUNTIME_TERMINATION_GRACE_PERIOD": "7s", "TROVE_HEALTH_SHUTDOWN_DRAIN_DELAY": "1s", "TROVE_SERVER_SHUTDOWN_TIMEOUT": "0s",
		})
		if got := h.app.cfg.Server.ShutdownTimeout; got != time.Second {
			t.Fatalf("derived timeout %s, want 1s", got)
		}
		h.app.budget = scaledBudget()
		comp := &blockingComp{name: "stuck", release: make(chan struct{})}
		t.Cleanup(func() { close(comp.release) })
		h.app.comps = append(h.app.comps, comp)

		begin := time.Now()
		err := h.app.Shutdown(context.Background())
		took := time.Since(begin)
		// timeout 1 s + margin 0.8 s - slack 0.1 s = 1.7 s, plus scheduling noise.
		if took > 1700*time.Millisecond+400*time.Millisecond {
			t.Errorf("Shutdown took %s, want it inside the plan of 1.7 s", took)
		}
		if took >= 7*time.Second {
			t.Errorf("Shutdown took %s, longer than the grace period", took)
		}
		if err == nil || !strings.Contains(err.Error(), "close phase overran") {
			t.Errorf("the stuck component is not reported: %v", err)
		}
		q := time.Unix(0, comp.quiesced.Load())
		if off := q.Sub(begin); off < 300*time.Millisecond || off > 700*time.Millisecond {
			t.Errorf("jobs quiesced %s after the signal, want near timeout - quiesce margin = 0.4 s", off)
		}
	})

	t.Run("R-ARCH-024 a request that is committing and never finishes is cut inside the commit grace, and the whole shutdown stays inside the plan", func(t *testing.T) {
		h := start(t, "web", map[string]string{"TROVE_HEALTH_SHUTDOWN_DRAIN_DELAY": "0s", "TROVE_SERVER_SHUTDOWN_TIMEOUT": "1s"})
		h.app.budget = scaledBudget()
		entered := make(chan struct{})
		h.app.router.HandleFunc("GET /test/commit", func(w http.ResponseWriter, r *http.Request) {
			httpserver.BeginCommit(r.Context())
			close(entered)
			<-r.Context().Done()
		})
		go func() {
			if resp, err := http.Get(h.client + "/test/commit"); err == nil {
				resp.Body.Close()
			}
		}()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("the request did not arrive")
		}
		begin := time.Now()
		_ = h.app.Shutdown(context.Background())
		if took := time.Since(begin); took > 1700*time.Millisecond+400*time.Millisecond {
			t.Errorf("Shutdown took %s, the commit grace pushed it past the plan of 1.7 s", took)
		}
	})
}

// releasingComp models the job worker: it is quiesced first, its handlers end at once and
// each settles with a release write that needs release to finish and, like Worker.Stop,
// is waited for in the close phase until its context ends. Quiesce runs on the shutdown's
// timer goroutine and Stop on the close phase's, so pending is made before the shutdown;
// the harness shuts the app down again at cleanup, so the write closes it once.
type releasingComp struct {
	blockingComp
	write    time.Duration
	started  atomic.Int64 // unix nanos of the Quiesce call
	finished atomic.Bool
	pending  chan struct{} // closed when the release write ends; made by the caller
	done     sync.Once
}

func (c *releasingComp) QuiescesFirst() {}
func (c *releasingComp) Quiesce(context.Context) {
	c.started.Store(time.Now().UnixNano())
	go func() {
		time.Sleep(c.write)
		c.finished.Store(true)
		c.done.Do(func() { close(c.pending) })
	}()
}
func (c *releasingComp) Flush(context.Context) error { return nil }
func (c *releasingComp) Stop(ctx context.Context) error {
	select {
	case <-c.pending:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("a release write still running at the end of the shutdown")
	}
}
func (c *releasingComp) Settle(ctx context.Context) error { <-ctx.Done(); return nil }

func TestShutdownReleaseWrites(t *testing.T) {
	t.Run("R-ARCH-024 R-HA-011 a job's release write gets the full settle deadline inside the grace period: the worker is quiesced early enough that the write ends before the close deadline", func(t *testing.T) {
		// grace 9 s, drain 1 s -> derived timeout 3 s; a worker skips the drain. The scaled
		// budget's settle deadline is 2 s: the write needs most of it.
		h := start(t, "worker", map[string]string{
			"TROVE_RUNTIME_TERMINATION_GRACE_PERIOD": "9s", "TROVE_HEALTH_SHUTDOWN_DRAIN_DELAY": "1s", "TROVE_SERVER_SHUTDOWN_TIMEOUT": "0s",
		})
		if got := h.app.cfg.Server.ShutdownTimeout; got != 3*time.Second {
			t.Fatalf("derived timeout %s, want 3s", got)
		}
		h.app.budget = scaledBudget()
		comp := &releasingComp{blockingComp: blockingComp{name: "jobs", release: make(chan struct{})}, write: h.app.budget.Release - 200*time.Millisecond, pending: make(chan struct{})}
		t.Cleanup(func() { close(comp.release) })
		h.app.comps = append(h.app.comps, comp)

		begin := time.Now()
		err := h.app.Shutdown(context.Background())
		if err != nil {
			t.Errorf("the release write was cut by the shutdown: %v", err)
		}
		if !comp.finished.Load() {
			t.Error("the release write did not finish before the process stopped waiting for it")
		}
		if took := time.Since(begin); took > 3*time.Second+600*time.Millisecond+400*time.Millisecond {
			t.Errorf("Shutdown took %s, past the plan", took)
		}
	})
}

func TestStopOrder(t *testing.T) {
	t.Run("R-ARCH-024 signals stop first, then the others in reverse start order", func(t *testing.T) {
		var order []string
		mk := func(n string) Component { return &recComp{name: n, log: &order} }
		sig := firstCloser{closer{name: "signals", close: func() {}}}
		comps := []Component{mk("postgres"), mk("blobs"), sig, mk("valkey"), mk("changes"), mk("registry"), mk("jobs")}
		var got []string
		for _, c := range stopOrder(comps) {
			got = append(got, c.Name())
		}
		want := "signals,jobs,registry,changes,valkey,blobs,postgres"
		if strings.Join(got, ",") != want {
			t.Fatalf("stop order %v, want %s", got, want)
		}
	})
}

// stalledDrainer is a component whose Drain does not return, as a database call on a connection that never answers does,
// and which records that the close phase reached its Flush.
type stalledDrainer struct {
	name    string
	release chan struct{}
	flushed atomic.Bool
}

func (c *stalledDrainer) Name() string                { return c.name }
func (c *stalledDrainer) Start(context.Context) error { return nil }
func (c *stalledDrainer) Stop(context.Context) error  { return nil }
func (c *stalledDrainer) Drain(context.Context)       { <-c.release }
func (c *stalledDrainer) Flush(context.Context) error { c.flushed.Store(true); return nil }

// R-ARCH-024 step 1, R-HA-011 (Q-ARCH-460): a drain that waits on the database is waited for until the end of the drain step
// and no longer. Without the bound the call timeout of the drain (10 s) came out of the budget of every later phase, the close
// phase started past its deadline and was abandoned before it flushed anything.
func TestShutdownDrainStepIsBoundedByThePlan(t *testing.T) {
	for _, tc := range []struct {
		role  string
		drain string
	}{{"web", "300ms"}, {"worker", "0s"}} {
		t.Run(tc.role+" with a drain that never returns the close phase still flushes inside the plan", func(t *testing.T) {
			h := start(t, tc.role, map[string]string{"TROVE_HEALTH_SHUTDOWN_DRAIN_DELAY": tc.drain, "TROVE_SERVER_SHUTDOWN_TIMEOUT": "1s"})
			h.app.budget = scaledBudget()
			comp := &stalledDrainer{name: "stalled", release: make(chan struct{})}
			t.Cleanup(func() { close(comp.release) })
			h.app.comps = append(h.app.comps, comp)

			begin := time.Now()
			err := h.app.Shutdown(context.Background())
			took := time.Since(begin)
			if err != nil {
				t.Errorf("Shutdown = %v, want a clean exit", err)
			}
			if !comp.flushed.Load() {
				t.Error("the close phase never reached the flush: the drain took the time of the later phases")
			}
			drain, _ := time.ParseDuration(tc.drain)
			if limit := drain + time.Second + 800*time.Millisecond + 400*time.Millisecond; took > limit {
				t.Errorf("Shutdown took %s, want it inside the plan of %s", took, limit)
			}
			if !strings.Contains(h.logs.String(), "components were still draining at the end of the drain step") {
				t.Errorf("the drain that did not return is not named in the log:\n%s", h.logs.String())
			}
		})
	}
}
