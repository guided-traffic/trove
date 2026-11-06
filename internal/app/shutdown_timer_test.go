package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// slowQuiescer quiesces for a while and ignores its context, like a handler that
// is slow to return; Settle returns at once, so the server phase ends early.
type slowQuiescer struct {
	name      string
	quiesceIn atomic.Int32
	quiesceTo atomic.Int32
	hold      time.Duration
	flush     time.Duration
}

func (c *slowQuiescer) Name() string                { return c.name }
func (c *slowQuiescer) Start(context.Context) error { return nil }
func (c *slowQuiescer) Stop(context.Context) error  { return nil }
func (c *slowQuiescer) Settle(context.Context) error {
	return nil
}
func (c *slowQuiescer) Flush(context.Context) error { time.Sleep(c.flush); return nil }
func (c *slowQuiescer) Quiesce(context.Context) {
	c.quiesceIn.Add(1)
	time.Sleep(c.hold)
	c.quiesceTo.Add(1)
}

func TestShutdownQuiesceTimer(t *testing.T) {
	startWorker := func(t *testing.T) *harness {
		// grace 9 s, drain 1 s -> derived timeout 3 s; the scaled budget quiesces at 2.4 s.
		h := start(t, "worker", map[string]string{
			"TROVE_RUNTIME_TERMINATION_GRACE_PERIOD": "9s", "TROVE_HEALTH_SHUTDOWN_DRAIN_DELAY": "1s", "TROVE_SERVER_SHUTDOWN_TIMEOUT": "0s",
		})
		h.app.budget = scaledBudget()
		return h
	}

	t.Run("R-ARCH-024 a quiesce that is still running when the server phase ends is waited for before the components close", func(t *testing.T) {
		h := startWorker(t)
		c := &slowQuiescer{name: "slow", hold: 1500 * time.Millisecond}
		h.app.comps = append(h.app.comps, c)
		// The server phase ends at once (nothing to settle); the quiesce is due at 2.4 s. Bring it forward.
		h.app.budget.Quiesce = 3 * time.Second
		h.app.budget.Release = 3 * time.Second
		_ = h.app.Shutdown(context.Background())
		if c.quiesceIn.Load() == 1 && c.quiesceTo.Load() != 1 {
			t.Fatalf("Shutdown returned while a quiesce was still running")
		}
	})

	t.Run("R-ARCH-024 a quiesce that was not due when the server phase ended never starts, also while the components flush", func(t *testing.T) {
		h := startWorker(t)
		c := &slowQuiescer{name: "late", flush: 800 * time.Millisecond}
		h.app.comps = append(h.app.comps, c)
		h.app.budget.Quiesce = 2600 * time.Millisecond // due 0.4 s after the signal, during the flush
		h.app.budget.Release = 2600 * time.Millisecond
		_ = h.app.Shutdown(context.Background())
		time.Sleep(3 * time.Second)
		if c.quiesceIn.Load() != 0 {
			t.Fatalf("Quiesce was called %d times after the server phase had ended", c.quiesceIn.Load())
		}
	})
}
