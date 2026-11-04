package jobs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/config"
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

func TestWorkerClaimsOnlyItsQueues(t *testing.T) {
	cfg := func(queues, exclude []string) config.Jobs {
		return config.Jobs{
			Workers: config.JobsWorkers{Default: 4, Maintenance: 1, Index: 2, Delivery: 8, Mirror: 2, Vuln: 1},
			Queues:  queues, ExcludeQueues: exclude,
		}
	}
	queues := func(c config.Jobs) ([]string, map[string]int) {
		w := &Worker{o: WorkerOptions{Config: c}}
		w.resolveQueues()
		return w.queues, w.slots
	}
	t.Run("R-ARCH-016 a worker restricted to queues claims only those, with their slots", func(t *testing.T) {
		got, slots := queues(cfg([]string{"vuln", "index"}, nil))
		if !slices.Equal(got, []string{"index", "vuln"}) || slots["index"] != 2 || slots["vuln"] != 1 || len(slots) != 2 {
			t.Fatalf("queues %v, slots %v", got, slots)
		}
	})
	t.Run("R-HA-062 the main worker claims every queue no pool lists", func(t *testing.T) {
		got, slots := queues(cfg(nil, []string{"vuln", "index"}))
		if !slices.Equal(got, []string{"default", "maintenance", "delivery", "mirror"}) || slots["vuln"] != 0 || slots["index"] != 0 {
			t.Fatalf("queues %v, slots %v", got, slots)
		}
	})
	t.Run("R-ARCH-016 without either setting a worker claims every queue", func(t *testing.T) {
		if got, _ := queues(cfg(nil, nil)); len(got) != 6 {
			t.Fatalf("queues %v", got)
		}
	})
	t.Run("R-ARCH-016 a queue without slots is not claimed even when listed", func(t *testing.T) {
		c := cfg([]string{"vuln", "index"}, nil)
		c.Workers.Vuln = 0
		if got, _ := queues(c); !slices.Equal(got, []string{"index"}) {
			t.Fatalf("queues %v", got)
		}
	})
}
