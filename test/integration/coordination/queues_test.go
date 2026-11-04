//go:build integration

package coordination

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/jobs"
)

// queueJobs is the jobs configuration of a worker with the queue settings of R-ARCH-016, over the shipped slots.
func queueJobs(queues, exclude []string) config.Jobs {
	j := defaultJobs()
	j.Workers.Vuln = 1
	j.Workers.Mirror = 2
	j.Queues, j.ExcludeQueues = queues, exclude
	return j
}

// TestJobQueues covers the claim over the queues of a process (R-ARCH-016, Q-ARCH-296, E6a.3): two workers with disjoint
// queue sets split the kinds, a sync on the queue vuln does not take the slot of maintenance, and a row of a kind whose
// queue moved is claimed by a process that claims the row's queue.
func TestJobQueues(t *testing.T) {
	e := newEnv(t)
	var mu sync.Mutex
	ranOn := map[string]string{} // job id -> replica
	gate := make(chan struct{})
	var gateOnce sync.Once
	openGate := func() { gateOnce.Do(func() { close(gate) }) }
	defer openGate()

	kinds := func(r *jobs.Registry, rep *replica) {
		record := func(ctx context.Context, run *jobs.Run) error {
			mu.Lock()
			ranOn[run.ID] = rep.name
			mu.Unlock()
			return nil
		}
		r.RegisterKind("it.q.index", jobs.KindSpec{Queue: "index", Handler: record})
		r.RegisterKind("it.q.gc", jobs.KindSpec{Queue: "maintenance", Handler: record})
		// The kind moved from maintenance to vuln with this release; a row of the old release is still in maintenance.
		r.RegisterKind("it.q.moved", jobs.KindSpec{Queue: "vuln", Handler: record})
		r.RegisterKind("it.q.sync", jobs.KindSpec{Queue: "vuln", Handler: func(ctx context.Context, run *jobs.Run) error {
			record(ctx, run)
			select {
			case <-gate:
			case <-ctx.Done():
			}
			return nil
		}})
	}
	only := e.newReplica("queues-index", replicaOpts{role: "worker", valkey: vkLive, kinds: kinds, jobs: queueJobs([]string{"index"}, nil)})
	rest := e.newReplica("queues-rest", replicaOpts{role: "worker", valkey: vkLive, kinds: kinds, jobs: queueJobs(nil, []string{"index"})})
	waitValkey(t, only)
	waitValkey(t, rest)
	ctx := context.Background()
	ran := func(id string) (string, bool) {
		mu.Lock()
		defer mu.Unlock()
		r, ok := ranOn[id]
		return r, ok
	}

	t.Run("R-ARCH-016 two workers with disjoint queue sets split the kinds", func(t *testing.T) {
		const n = 50
		idx, gc := make([]string, 0, n), make([]string, 0, n)
		for i := range n {
			a, err := rest.queue.EnqueueNow(ctx, jobs.EnqueueSpec{Kind: "it.q.index", Args: map[string]int{"i": i}})
			if err != nil {
				t.Fatal(err)
			}
			b, err := rest.queue.EnqueueNow(ctx, jobs.EnqueueSpec{Kind: "it.q.gc", Args: map[string]int{"i": i}})
			if err != nil {
				t.Fatal(err)
			}
			idx, gc = append(idx, a.Job.ID), append(gc, b.Job.ID)
		}
		for _, id := range append(append([]string{}, idx...), gc...) {
			e.waitState(only, id, jobs.StateSucceeded, 60*time.Second)
		}
		for _, id := range idx {
			if r, _ := ran(id); r != only.name {
				t.Fatalf("index job %s ran on %q, want %q", id, r, only.name)
			}
		}
		for _, id := range gc {
			if r, _ := ran(id); r != rest.name {
				t.Fatalf("maintenance job %s ran on %q, want %q", id, r, rest.name)
			}
		}
	})

	t.Run("R-ARCH-016 a running sync on the queue vuln does not delay a maintenance job", func(t *testing.T) {
		syncJob, err := only.queue.EnqueueNow(ctx, jobs.EnqueueSpec{Kind: "it.q.sync"})
		if err != nil {
			t.Fatal(err)
		}
		// The queue vuln belongs to the worker that claims everything but index.
		waitFor(t, "the sync starting", 10*time.Second, func() bool { _, ok := ran(syncJob.Job.ID); return ok })
		if r, _ := ran(syncJob.Job.ID); r != rest.name {
			t.Fatalf("the sync ran on %q, want %q", r, rest.name)
		}
		start := time.Now()
		gc, err := only.queue.EnqueueNow(ctx, jobs.EnqueueSpec{Kind: "it.q.gc"})
		if err != nil {
			t.Fatal(err)
		}
		e.waitState(only, gc.Job.ID, jobs.StateSucceeded, 10*time.Second)
		if d := time.Since(start); d > queueJobs(nil, nil).PollInterval+time.Second {
			t.Fatalf("the maintenance job took %s while a sync held the vuln slot", d)
		}
		openGate()
		e.waitState(only, syncJob.Job.ID, jobs.StateSucceeded, 10*time.Second)
	})

	t.Run("R-ARCH-016 a row of a kind whose queue moved runs on a process that claims the row's queue", func(t *testing.T) {
		res, err := only.queue.EnqueueNow(ctx, jobs.EnqueueSpec{Kind: "it.q.moved", Delay: 3 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		// The row an older release enqueued: the old queue of the kind.
		if _, err := e.admin.Exec(ctx, "UPDATE job SET queue = 'maintenance' WHERE id = $1", res.Job.ID); err != nil {
			t.Fatal(err)
		}
		j := e.waitState(only, res.Job.ID, jobs.StateSucceeded, 30*time.Second)
		if r, _ := ran(res.Job.ID); r != rest.name || j.Queue != "maintenance" {
			t.Fatalf("ran on %q in queue %q, want %q in maintenance", r, j.Queue, rest.name)
		}
	})
}
