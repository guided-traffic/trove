package jobs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestForgetKeepsTheNewRun(t *testing.T) {
	t.Run("R-ARCH-016 a finished run does not delete the entry of the run that replaced it", func(t *testing.T) {
		w := &Worker{running: map[string]*runningJob{}, perQueue: map[string]int{"default": 2}}
		old := &runningJob{job: Job{ID: "j1", Queue: "default", Attempt: 1}}
		fresh := &runningJob{job: Job{ID: "j1", Queue: "default", Attempt: 1}}
		w.running["j1"] = fresh // the job was released and claimed again while old settled
		w.forget(old)
		if w.running["j1"] != fresh {
			t.Fatal("the finished run removed the entry of the new run")
		}
		if w.perQueue["default"] != 1 {
			t.Fatalf("perQueue = %d, want 1: each run frees its own slot", w.perQueue["default"])
		}
		w.forget(fresh)
		if len(w.running) != 0 {
			t.Fatal("the entry stayed after its own run finished")
		}
	})
}

func TestWriteWithRetry(t *testing.T) {
	w := &Worker{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	t.Run("R-HA-005 a failed settle write is retried a bounded number of times, then given up", func(t *testing.T) {
		calls := 0
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := w.writeWithRetry(ctx, log, "complete", func() error { calls++; return errors.New("connection reset") })
		if err == nil || calls != settleAttempts {
			t.Fatalf("calls = %d, err = %v; want %d calls and the last error", calls, err, settleAttempts)
		}
	})
	t.Run("R-HA-005 a write that succeeds on a retry ends the retries", func(t *testing.T) {
		calls := 0
		err := w.writeWithRetry(context.Background(), log, "release", func() error {
			calls++
			if calls < 3 {
				return errors.New("failover")
			}
			return nil
		})
		if err != nil || calls != 3 {
			t.Fatalf("calls = %d, err = %v", calls, err)
		}
	})
	t.Run("R-HA-005 the deadline cuts the retries short", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		calls := 0
		err := w.writeWithRetry(ctx, log, "fail", func() error { calls++; return errors.New("down") })
		if err == nil || calls >= settleAttempts {
			t.Fatalf("calls = %d, err = %v", calls, err)
		}
	})
}
