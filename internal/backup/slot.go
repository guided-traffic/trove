package backup

import (
	"context"
	"errors"
	"time"
)

// AcquireSlot inserts the run row of a target-writing operation (R-BAK-010). When another create or delete run holds the
// slot:
//
//   - if that run's heartbeat is younger than 3 x the heartbeat interval it is alive, and the call returns a *BusyError (exit
//     3) that names it;
//   - otherwise its process has stopped renewing (OOMKilled, kill -9, a lost node): the call waits until that run's lease has
//     passed, polling every heartbeat interval for at most lease + heartbeat, and inserts again, which abandons the old
//     run. Without this wait the Job's retry pod would start seconds after the crash and exit 3.
//
// sleep is time.Sleep in production; tests pass a fake.
func AcquireSlot(ctx context.Context, store RunStore, r NewRun, t Timing, sleep func(context.Context, time.Duration) error) (Run, error) {
	if sleep == nil {
		sleep = sleepCtx
	}
	var deadline time.Time
	for {
		run, err := store.Begin(ctx, r)
		var taken *ErrSlotTaken
		if !errors.As(err, &taken) {
			return run, err
		}
		active, ok, now, aerr := store.Active(ctx)
		if aerr != nil {
			return Run{}, aerr
		}
		if !ok {
			continue // it ended between the insert and the read: try again
		}
		if now.Sub(active.HeartbeatAt) < 3*t.Heartbeat {
			return Run{}, &BusyError{Active: active}
		}
		if deadline.IsZero() {
			deadline = time.Now().Add(t.Lease + t.Heartbeat)
		}
		if time.Now().After(deadline) {
			return Run{}, &BusyError{Active: active}
		}
		if err := sleep(ctx, t.Heartbeat); err != nil {
			return Run{}, err
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	tm := time.NewTimer(d)
	defer tm.Stop()
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-tm.C:
		return nil
	}
}
