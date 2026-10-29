//go:build integration

package coordination

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/jobs"
)

// everyMinute is a schedule source with two tasks that fire every minute.
type everyMinute struct{ blocked atomic.Bool }

func (s *everyMinute) Schedules(context.Context) ([]jobs.TaskSchedule, error) {
	out := []jobs.TaskSchedule{{Name: "it-minutely", Kind: "it.tick", Schedule: "* * * * *"}}
	if s.blocked.Load() {
		out = append(out, jobs.TaskSchedule{Name: "it-shared", Kind: "it.tick", Schedule: "* * * * *", DedupKey: "it-shared-key"})
	}
	return out, nil
}

func TestScheduler(t *testing.T) {
	e := newEnv(t)
	src := &everyMinute{}
	var ran atomic.Int64
	kinds := func(r *jobs.Registry, rep *replica) {
		r.RegisterKind("it.tick", jobs.KindSpec{Handler: func(ctx context.Context, run *jobs.Run) error { ran.Add(1); return nil }})
	}
	jc := defaultJobs()
	jc.Retention = time.Second // shorter than the test: only the exception keeps the latest slot
	var workers []*replica
	for _, n := range []string{"sched-a", "sched-b", "sched-c"} {
		workers = append(workers, e.newReplica(n, replicaOpts{role: "worker", kinds: kinds, jobs: jc, sources: []jobs.ScheduleSource{src}}))
	}
	web := e.newReplica("sched-web", replicaOpts{role: "web", kinds: kinds})

	holders := func() []int64 {
		rows, err := e.admin.Query(context.Background(), `SELECT pid FROM pg_locks WHERE locktype = 'advisory' AND objsubid = 2 AND classid = 1 AND objid = $1 AND granted`, cluster.LeaseScheduler.Number())
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []int64
		for rows.Next() {
			var p int64
			_ = rows.Scan(&p)
			out = append(out, p)
		}
		return out
	}

	t.Run("R-ARCH-017 three workers: exactly one holds the scheduler lease", func(t *testing.T) {
		waitFor(t, "a scheduler", 10*time.Second, func() bool { return len(holders()) == 1 })
		time.Sleep(1500 * time.Millisecond)
		if h := holders(); len(h) != 1 {
			t.Fatalf("pg_locks shows %d holders of (1, 1)", len(h))
		}
		// Only one replica lists the lease.
		listed := 0
		for _, w := range workers {
			if len(w.leases.Held()) == 1 && w.leases.Held()[0].Name == "scheduler" {
				listed++
			}
		}
		if listed != 1 {
			t.Fatalf("%d replicas report the scheduler lease", listed)
		}
	})

	t.Run("R-ARCH-016 a slot is enqueued exactly once, also across a lease hand-over", func(t *testing.T) {
		waitFor(t, "the first slot", 10*time.Second, func() bool { return e.scalar(`SELECT count(*) FROM job WHERE task = 'it-minutely'`) >= 1 })
		pid := holders()[0]
		moved := time.Now()
		e.exec(`SELECT pg_terminate_backend($1)`, pid) // the scheduler's session dies mid-run
		waitFor(t, "a scheduler again", 10*time.Second, func() bool { h := holders(); return len(h) == 1 && h[0] != pid })
		t.Logf("scheduler lease back %s after pg_terminate_backend (lease_retry_interval = %s)", time.Since(moved).Round(10*time.Millisecond), defaultCluster().LeaseRetryInterval)
		time.Sleep(4 * time.Second) // several ticks on every worker, with retention running every second
		if dup := e.scalar(`SELECT count(*) FROM (SELECT task, scheduled_for FROM job WHERE task IS NOT NULL GROUP BY 1, 2 HAVING count(*) > 1) d`); dup != 0 {
			t.Fatalf("%d slots were enqueued twice", dup)
		}
		slots := e.scalar(`SELECT count(DISTINCT scheduled_for) FROM job WHERE task = 'it-minutely'`)
		if slots < 1 || slots > 2 { // two when the test crosses a minute boundary
			t.Fatalf("%d slots for a ~15 s window", slots)
		}
		if ran.Load() < 1 {
			t.Fatal("no scheduled job ran")
		}
		// The slot's job is the latest, and retention (1 s) did not delete it: the
		// exception that makes job_task_slot keep refusing the slot.
		if n := e.scalar(`SELECT count(*) FROM job WHERE task = 'it-minutely' AND scheduled_for = (SELECT max(scheduled_for) FROM job WHERE task = 'it-minutely')`); n != 1 {
			t.Fatalf("the job of the latest slot is gone or doubled: %d", n)
		}
	})

	t.Run("R-ARCH-016 retention deletes finished jobs but keeps the job of each task's latest slot", func(t *testing.T) {
		e.exec(`INSERT INTO job (id, kind, state, finished_at) VALUES ('00000000-0000-7000-8000-0000000000a1', 'it.tick', 'succeeded', now() - interval '2 hours')`)
		e.exec(`INSERT INTO job (id, kind, state, finished_at, task, scheduled_for) VALUES ('00000000-0000-7000-8000-0000000000a2', 'it.tick', 'succeeded', now() - interval '2 hours', 'it-monthly', now() - interval '40 days')`)
		waitFor(t, "retention", 10*time.Second, func() bool {
			return e.scalar(`SELECT count(*) FROM job WHERE id = '00000000-0000-7000-8000-0000000000a1'`) == 0
		})
		if e.scalar(`SELECT count(*) FROM job WHERE id = '00000000-0000-7000-8000-0000000000a2'`) != 1 {
			t.Fatal("the job of a task's only slot was deleted: its slot could be enqueued a second time")
		}
	})

	t.Run("R-ARCH-016 a slot refused by an exclusive key is offered again after the blocking job ended", func(t *testing.T) {
		// A running job holds the key that the task shares.
		e.exec(`INSERT INTO job (id, kind, state, attempt, dedup_key, dedup_mode, locked_by, locked_until, started_at)
		        VALUES ('00000000-0000-7000-8000-0000000000b1', 'it.tick', 'running', 1, 'it-shared-key', 'exclusive', gen_random_uuid(), now() + interval '1 hour', now())`)
		src.blocked.Store(true)
		time.Sleep(3 * time.Second) // several ticks
		if n := e.scalar(`SELECT count(*) FROM job WHERE task = 'it-shared'`); n != 0 {
			t.Fatalf("the slot was enqueued although the key was held: %d", n)
		}
		e.exec(`UPDATE job SET state = 'succeeded', finished_at = now(), locked_by = NULL, locked_until = NULL WHERE id = '00000000-0000-7000-8000-0000000000b1'`)
		waitFor(t, "the postponed slot", 10*time.Second, func() bool { return e.scalar(`SELECT count(*) FROM job WHERE task = 'it-shared'`) == 1 })
	})

	t.Run("R-ARCH-024 a draining worker releases the scheduler lease at once", func(t *testing.T) {
		holder := holders()[0]
		var drained *replica
		for _, w := range workers {
			if len(w.leases.Held()) == 1 {
				drained = w
			}
		}
		if drained == nil {
			t.Fatal("no replica reports the lease")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		drained.worker.Drain(ctx)
		if h := holders(); len(h) == 1 && h[0] == holder {
			t.Fatal("the session still holds the lock after Drain returned")
		}
		waitFor(t, "another worker scheduling", 10*time.Second, func() bool { return len(holders()) == 1 })
		_ = web
	})
}
