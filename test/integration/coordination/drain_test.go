//go:build integration

package coordination

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/jobs"
)

type startInfo struct {
	replica string
	resumed bool
	cursor  int
	attempt int
}

// counterValue reads a counter series from a replica's registry.
func counterValue(t *testing.T, reg *prometheus.Registry, name string, labels map[string]string) float64 {
	t.Helper()
	fams, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fams {
		if f.GetName() != name {
			continue
		}
	metric:
		for _, m := range f.GetMetric() {
			for k, v := range labels {
				found := false
				for _, l := range m.GetLabel() {
					if l.GetName() == k && l.GetValue() == v {
						found = true
					}
				}
				if !found {
					continue metric
				}
			}
			return m.GetCounter().GetValue()
		}
	}
	return 0
}

// TestDrainResumesFromCursor is E0.14 at the level of two replicas: a job that
// saves its cursor every 100 ms is released by the graceful shutdown of its
// worker and finishes on the other worker from its cursor, its attempt count
// unchanged (R-ARCH-016, R-ARCH-024).
func TestDrainResumesFromCursor(t *testing.T) {
	e := newEnv(t)
	const total = 40
	starts := make(chan startInfo, 8)
	var mu sync.Mutex
	finished := map[string]int{}
	kinds := func(r *jobs.Registry, rep *replica) {
		r.RegisterKind("it.cursor", jobs.KindSpec{MaxAttempts: 3, Handler: func(ctx context.Context, run *jobs.Run) error {
			var st struct{ N int }
			resumed, err := run.LoadCheckpoint(&st)
			if err != nil {
				return err
			}
			starts <- startInfo{replica: rep.name, resumed: resumed, cursor: st.N, attempt: run.Attempt}
			for st.N < total {
				select {
				case <-ctx.Done():
					// The shutdown: persist the cursor, then return.
					_ = run.SaveCheckpoint(context.WithoutCancel(ctx), st)
					return ctx.Err()
				case <-time.After(100 * time.Millisecond):
				}
				st.N++
				if err := run.SaveCheckpoint(ctx, st); err != nil {
					return err
				}
			}
			mu.Lock()
			finished[rep.name] = st.N
			mu.Unlock()
			return nil
		}})
	}
	web := e.newReplica("drain-web", replicaOpts{role: "web", kinds: kinds})
	w := map[string]*replica{
		"drain-a": e.newReplica("drain-a", replicaOpts{role: "worker", kinds: kinds}),
		"drain-b": e.newReplica("drain-b", replicaOpts{role: "worker", kinds: kinds}),
	}
	res, err := web.enqueueTx(context.Background(), jobs.EnqueueSpec{Kind: "it.cursor"})
	if err != nil {
		t.Fatal(err)
	}
	id := res.Job.ID

	var first startInfo
	select {
	case first = <-starts:
	case <-time.After(15 * time.Second):
		t.Fatal("the job did not start")
	}
	if first.resumed || first.attempt != 1 {
		t.Fatalf("first start: %+v", first)
	}
	claimer, other := w[first.replica], "drain-a"
	if first.replica == "drain-a" {
		other = "drain-b"
	}

	cursor := func() int {
		var n int
		err := e.admin.QueryRow(context.Background(), `SELECT coalesce((checkpoint->>'N')::int, 0) FROM job WHERE id = $1::uuid`, id).Scan(&n)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal(err)
		}
		return n
	}
	waitFor(t, "the cursor to move", 10*time.Second, func() bool { return cursor() >= 8 })

	t.Run("R-ARCH-016 the graceful shutdown releases the job with its cursor and without counting the attempt", func(t *testing.T) {
		claimer.stop() // Drain, Settle, Quiesce, Stop: the order of R-ARCH-024
		var state string
		var attempt int
		var lockedBy *string
		if err := e.admin.QueryRow(context.Background(), `SELECT state, attempt, locked_by::text FROM job WHERE id = $1::uuid`, id).Scan(&state, &attempt, &lockedBy); err != nil {
			t.Fatal(err)
		}
		if state != jobs.StateQueued || attempt != 0 || lockedBy != nil {
			t.Fatalf("after the release: state %s attempt %d locked_by %v, want queued, 0 (the interrupted attempt is not counted), nil", state, attempt, lockedBy)
		}
		if c := cursor(); c < 8 || c >= total {
			t.Fatalf("checkpoint at the release = %d", c)
		}
		if n := counterValue(t, claimer.reg, "trove_jobs_completed_total", map[string]string{"kind": "it.cursor", "outcome": "released"}); n != 1 {
			t.Fatalf("trove_jobs_completed_total{outcome=\"released\"} = %v, want 1", n)
		}
	})

	released := cursor()
	t.Run("E0.14 the job finishes on the other worker from its cursor with attempt unchanged", func(t *testing.T) {
		var second startInfo
		select {
		case second = <-starts:
		case <-time.After(15 * time.Second):
			t.Fatal("the other worker did not pick the job up")
		}
		if second.replica != other || !second.resumed || second.cursor != released {
			t.Fatalf("second start %+v, want a resume on %s at cursor %d", second, other, released)
		}
		if second.attempt != first.attempt {
			t.Fatalf("attempt %d after the drain, was %d: a drain must not cost an attempt", second.attempt, first.attempt)
		}
		j := e.waitState(web, id, jobs.StateSucceeded, 30*time.Second)
		if j.Attempt != 1 {
			t.Fatalf("final attempt = %d, want 1", j.Attempt)
		}
		mu.Lock()
		defer mu.Unlock()
		if finished[other] != total {
			t.Fatalf("finished = %v", finished)
		}
		t.Logf("released at cursor %d on %s, resumed and finished on %s, attempt %d", released, first.replica, other, j.Attempt)
	})
}
