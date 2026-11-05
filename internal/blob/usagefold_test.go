package blob

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/meta"
)

type noDB struct{}

func (noDB) InTx(context.Context, func(meta.Tx) error) error { return errors.New("no database") }

func newTestFolder(t *testing.T, interval time.Duration, iterate func(context.Context) (int, error)) *UsageFolder {
	t.Helper()
	f, err := NewUsageFolder(UsageFolderOptions{DB: noDB{}, Interval: interval, WatchdogTimeout: 20 * time.Second, CallTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	f.iterate = iterate
	t.Cleanup(func() { _ = f.Stop(context.Background()) })
	return f
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(2 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestR_STOR_003_FoldLoopRunsAtOnceWhileTheBatchIsFull(t *testing.T) {
	var calls atomic.Int32
	// An interval of an hour: only the "next iteration at once" rule can produce the second and third call.
	f := newTestFolder(t, time.Hour, func(context.Context) (int, error) {
		if calls.Add(1) < 3 {
			return UsageFoldBatch, nil
		}
		return 7, nil
	})
	if err := f.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	eventually(t, "three iterations", func() bool { return calls.Load() == 3 })
	time.Sleep(30 * time.Millisecond)
	if n := calls.Load(); n != 3 {
		t.Errorf("%d iterations: a partial batch must wait for the interval", n)
	}
}

func TestR_STOR_003_FoldLoopWaitsTheIntervalAfterAPartialBatchAndAfterAFailure(t *testing.T) {
	var calls atomic.Int32
	var stamps [4]atomic.Int64
	f := newTestFolder(t, 40*time.Millisecond, func(context.Context) (int, error) {
		n := calls.Add(1)
		if n <= 4 {
			stamps[n-1].Store(time.Now().UnixNano())
		}
		if n == 2 {
			return 0, errors.New("boom")
		}
		return UsageFoldBatch - 1, nil
	})
	if err := f.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	eventually(t, "four iterations", func() bool { return calls.Load() >= 4 })
	for i := 1; i < 4; i++ {
		if gap := time.Duration(stamps[i].Load() - stamps[i-1].Load()); gap < 35*time.Millisecond {
			t.Errorf("iteration %d began %v after the one before, want at least the interval (a failure is repeated at the next interval)", i+1, gap)
		}
	}
}

func TestR_STOR_003_FoldIterationCarriesADeadlineBelowTheWatchdogBound(t *testing.T) {
	got := make(chan time.Duration, 1)
	f := newTestFolder(t, time.Hour, func(ctx context.Context) (int, error) {
		dl, ok := ctx.Deadline()
		if !ok {
			got <- -1
		} else {
			got <- time.Until(dl)
		}
		return 0, nil
	})
	if err := f.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if d := <-got; d <= 0 || d > 10*time.Second {
		t.Errorf("iteration deadline %v, want within (0, 10s] for a watchdog bound of 20s (R-ARCH-016)", d)
	}
}

func TestR_STOR_003_DrainedFolderStartsNoIterationAndQuiesceRollsBackTheOneInFlight(t *testing.T) {
	var calls atomic.Int32
	started, cancelled := make(chan struct{}), make(chan struct{})
	f := newTestFolder(t, 10*time.Millisecond, func(ctx context.Context) (int, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-ctx.Done() // a transaction running until the worker is cut
			close(cancelled)
			return 0, ctx.Err()
		}
		return 0, nil
	})
	if err := f.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	<-started
	f.Drain(t.Context())
	f.Quiesce(t.Context())
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("the iteration in flight was not cancelled")
	}
	if err := f.Settle(t.Context()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	if n := calls.Load(); n != 1 {
		t.Errorf("%d iterations: a drained folder starts no new one", n)
	}
}

func TestR_STOR_003_NewUsageFolderRefusesMissingParts(t *testing.T) {
	if _, err := NewUsageFolder(UsageFolderOptions{Interval: time.Second}); err == nil {
		t.Error("no database accepted")
	}
	if _, err := NewUsageFolder(UsageFolderOptions{DB: noDB{}}); err == nil {
		t.Error("no interval accepted")
	}
}
