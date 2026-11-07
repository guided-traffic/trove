package backup

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/backup/format"
)

func TestExitCodes(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code int
		fail string
	}{
		{"R-BAK-013 success", nil, ExitOK, ""},
		{"R-BAK-013 hold_lost fails with 1", fmt.Errorf("copy: %w", ErrHoldLost), ExitFailed, CodeHoldLost},
		{"R-BAK-013 superseded fails with 1", ErrSuperseded, ExitFailed, CodeSuperseded},
		{"R-BAK-013 pool_damaged fails with 1", ErrPoolDamaged, ExitFailed, CodePoolDamaged},
		{"R-BAK-013 a live concurrent run is 3", &BusyError{Active: Run{ID: "x"}}, ExitBusy, CodeBusy},
		{"R-BAK-013 a foreign target is 4", fmt.Errorf("preflight: %w", format.ErrForeignTarget), ExitForeignTarget, CodeForeignTarget},
		{"R-BAK-013 verify findings are 5", ErrFindings, ExitFindings, CodeFindings},
		{"R-BAK-013 an unknown manifest format is 6", &format.UnknownFormatError{What: "manifest", Got: 2, Known: 1}, ExitPrecondition, CodeFormatUnknown},
		{"R-BAK-013 a stall is a temporary failure, 75", ErrStalled, ExitTemporary, CodeStalled},
		{"R-BAK-009 a restore in progress is 75", ErrRestoreInProgress, ExitTemporary, CodeRestoreInProgress},
		{"R-BAK-013 an explicit exit error wins", Failf(ExitUsage, CodeConfig, "backup.target.bucket is required"), ExitUsage, CodeConfig},
		{"R-BAK-013 anything else is a failure", errors.New("boom"), ExitFailed, CodeInternal},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ExitCode(c.err); got != c.code {
				t.Errorf("ExitCode = %d, want %d", got, c.code)
			}
			if got := ErrorCode(c.err); got != c.fail {
				t.Errorf("ErrorCode = %q, want %q", got, c.fail)
			}
		})
	}
	t.Run("R-BAK-013 the codes are the documented numbers", func(t *testing.T) {
		if ExitOK != 0 || ExitFailed != 1 || ExitUsage != 2 || ExitBusy != 3 || ExitForeignTarget != 4 || ExitFindings != 5 || ExitPrecondition != 6 || ExitTemporary != 75 {
			t.Fatal("exit codes drifted from R-BAK-013")
		}
	})
	t.Run("R-BAK-012 NotBuilt exits 1 and says so", func(t *testing.T) {
		_, err := NotBuilt{Operation: "create"}.Create(context.Background(), CreateOptions{})
		if !errors.Is(err, ErrNotBuilt) || ExitCode(err) != ExitFailed {
			t.Fatalf("err = %v, code %d", err, ExitCode(err))
		}
	})
}

func TestStateHelpers(t *testing.T) {
	t.Run("R-BAK-009 the active states are the ones of backup_run_one_active", func(t *testing.T) {
		for _, s := range []State{StateStarting, StateSnapshot, StateCopying, StateFinalizing, StatePruning} {
			if !s.Active() || s.Succeeded() {
				t.Errorf("%s", s)
			}
		}
		for _, s := range []State{StateCompleted, StateCompletedWithWarning, StateFailed, StateInterrupted, StateAbandoned} {
			if s.Active() {
				t.Errorf("%s is active", s)
			}
		}
		if !StateCompletedWithWarning.Succeeded() || StateFailed.Succeeded() {
			t.Error("Succeeded")
		}
	})
}

// fakeStore records heartbeats and can be told to refuse them.
type fakeStore struct {
	RunStore
	mu         sync.Mutex
	beats      []beat
	superseded bool
	begins     []error
	active     Run
	now        time.Time
	activeOK   bool
	// alwaysTaken makes every Begin find the slot taken.
	alwaysTaken bool
}

type beat struct {
	renew bool
	p     Progress
}

func (f *fakeStore) Heartbeat(_ context.Context, _ string, _ State, p Progress, _ *time.Time, renew bool, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.superseded {
		return ErrSuperseded
	}
	f.beats = append(f.beats, beat{renew, p})
	return nil
}

func (f *fakeStore) renewals() (renewed, plain int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, b := range f.beats {
		if b.renew {
			renewed++
		} else {
			plain++
		}
	}
	return
}

func (f *fakeStore) Begin(_ context.Context, r NewRun) (Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.alwaysTaken {
		return Run{}, &ErrSlotTaken{}
	}
	if len(f.begins) == 0 {
		return Run{ID: r.ID, State: StateStarting}, nil
	}
	err := f.begins[0]
	f.begins = f.begins[1:]
	if err != nil {
		return Run{}, err
	}
	return Run{ID: r.ID, State: StateStarting}, nil
}

func (f *fakeStore) Active(context.Context) (Run, bool, time.Time, error) {
	return f.active, f.activeOK, f.now, nil
}

func TestHandle(t *testing.T) {
	timing := Timing{Lease: time.Second, Heartbeat: 10 * time.Millisecond, Stall: 80 * time.Millisecond, Finalize: 10 * time.Millisecond}
	t.Run("R-BAK-013 the lease is renewed only while the run makes progress", func(t *testing.T) {
		f := &fakeStore{}
		h, ctx := StartHandle(context.Background(), f, Run{ID: "r", State: StateCopying}, Timing{Lease: time.Second, Heartbeat: 10 * time.Millisecond, Stall: time.Minute, Finalize: 10 * time.Millisecond}, nil)
		defer h.Stop()
		for range 5 {
			h.Update(func(p *Progress) { p.BlobsCopied++ })
			time.Sleep(12 * time.Millisecond)
		}
		time.Sleep(60 * time.Millisecond) // idle: heartbeats continue without renewing
		renewed, plain := f.renewals()
		if renewed == 0 || plain == 0 {
			t.Fatalf("renewed=%d plain=%d: want both progress heartbeats and idle ones", renewed, plain)
		}
		if ctx.Err() != nil {
			t.Fatalf("the run was cancelled: %v", context.Cause(ctx))
		}
	})
	t.Run("R-BAK-013 a run without progress for stall_timeout cancels itself with ErrStalled", func(t *testing.T) {
		f := &fakeStore{}
		h, ctx := StartHandle(context.Background(), f, Run{ID: "r", State: StateCopying}, timing, nil)
		defer h.Stop()
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
			t.Fatal("not cancelled")
		}
		if !errors.Is(context.Cause(ctx), ErrStalled) {
			t.Fatalf("cause = %v", context.Cause(ctx))
		}
	})
	t.Run("R-BAK-010 a heartbeat that matches no row cancels the run with ErrSuperseded", func(t *testing.T) {
		f := &fakeStore{superseded: true}
		h, ctx := StartHandle(context.Background(), f, Run{ID: "r", State: StateCopying}, Timing{Lease: time.Second, Heartbeat: 10 * time.Millisecond, Stall: time.Minute, Finalize: 10 * time.Millisecond}, nil)
		defer h.Stop()
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
			t.Fatal("not cancelled")
		}
		if !errors.Is(context.Cause(ctx), ErrSuperseded) {
			t.Fatalf("cause = %v", context.Cause(ctx))
		}
	})
}

func TestAcquireSlot(t *testing.T) {
	timing := Timing{Lease: 5 * time.Minute, Heartbeat: 15 * time.Second}
	now := time.Date(2026, 10, 28, 2, 0, 0, 0, time.UTC)
	noSleep := func(context.Context, time.Duration) error { return nil }
	t.Run("R-BAK-010 a live run makes the second one exit 3 and names the first", func(t *testing.T) {
		f := &fakeStore{
			begins: []error{&ErrSlotTaken{}}, activeOK: true, now: now,
			active: Run{ID: "first", Operation: OpCreate, State: StateCopying, Pod: "p", HeartbeatAt: now.Add(-10 * time.Second), StartedAt: now.Add(-time.Hour)},
		}
		_, err := AcquireSlot(context.Background(), f, NewRun{ID: "second"}, timing, noSleep)
		var busy *BusyError
		if !errors.As(err, &busy) || busy.Active.ID != "first" || ExitCode(err) != ExitBusy {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("R-BAK-010 a run that stopped renewing is waited for, then taken over", func(t *testing.T) {
		f := &fakeStore{
			begins: []error{&ErrSlotTaken{}, &ErrSlotTaken{}, nil}, activeOK: true, now: now,
			active: Run{ID: "dead", State: StateCopying, HeartbeatAt: now.Add(-10 * time.Minute)},
		}
		sleeps := 0
		run, err := AcquireSlot(context.Background(), f, NewRun{ID: "retry"}, timing, func(context.Context, time.Duration) error { sleeps++; return nil })
		if err != nil || run.ID != "retry" || sleeps != 2 {
			t.Fatalf("run=%+v err=%v sleeps=%d", run, err, sleeps)
		}
	})
	freshRun := func(job string) Run {
		return Run{ID: "first", Operation: OpCreate, State: StateCopying, JobName: job, HeartbeatAt: now.Add(-2 * time.Second), StartedAt: now.Add(-time.Hour)}
	}
	t.Run("R-BAK-010 a retry pod of the same Job waits for the dead run even though its heartbeat is young, then takes over", func(t *testing.T) {
		f := &fakeStore{begins: []error{&ErrSlotTaken{}, &ErrSlotTaken{}, nil}, activeOK: true, now: now, active: freshRun("trove-backup-1")}
		sleeps := 0
		run, err := AcquireSlot(context.Background(), f, NewRun{ID: "retry", JobName: "trove-backup-1"}, timing, func(context.Context, time.Duration) error { sleeps++; return nil })
		if err != nil || run.ID != "retry" || sleeps != 2 {
			t.Fatalf("run=%+v err=%v sleeps=%d", run, err, sleeps)
		}
	})
	t.Run("R-BAK-010 a run of another Job with a young heartbeat is exit 3 at once", func(t *testing.T) {
		f := &fakeStore{begins: []error{&ErrSlotTaken{}}, activeOK: true, now: now, active: freshRun("trove-backup-1")}
		_, err := AcquireSlot(context.Background(), f, NewRun{ID: "other", JobName: "trove-backup-2"}, timing, noSleep)
		if ExitCode(err) != ExitBusy {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("R-BAK-010 without a Job identity on either side a young heartbeat is exit 3 at once", func(t *testing.T) {
		for name, c := range map[string][2]string{"manual run after a Job": {"trove-backup-1", ""}, "Job after a manual run": {"", "trove-backup-1"}, "two manual runs": {"", ""}} {
			f := &fakeStore{begins: []error{&ErrSlotTaken{}}, activeOK: true, now: now, active: freshRun(c[0])}
			if _, err := AcquireSlot(context.Background(), f, NewRun{ID: "x", JobName: c[1]}, timing, noSleep); ExitCode(err) != ExitBusy {
				t.Fatalf("%s: err = %v", name, err)
			}
		}
	})
	t.Run("R-BAK-010 a same-Job wait is bounded by the lease and then exit 3", func(t *testing.T) {
		f := &fakeStore{begins: []error{&ErrSlotTaken{}}, activeOK: true, now: now, active: freshRun("j")}
		f.alwaysTaken = true
		n := 0
		_, err := AcquireSlot(context.Background(), f, NewRun{ID: "retry", JobName: "j"}, Timing{Lease: 30 * time.Millisecond, Heartbeat: 5 * time.Millisecond},
			func(ctx context.Context, d time.Duration) error { n++; time.Sleep(d); return nil })
		if ExitCode(err) != ExitBusy || n == 0 {
			t.Fatalf("err = %v after %d waits", err, n)
		}
	})
	t.Run("R-BAK-010 a verify or an open slot needs no wait", func(t *testing.T) {
		f := &fakeStore{}
		if run, err := AcquireSlot(context.Background(), f, NewRun{ID: "free"}, timing, noSleep); err != nil || run.ID != "free" {
			t.Fatalf("run=%+v err=%v", run, err)
		}
	})
}
