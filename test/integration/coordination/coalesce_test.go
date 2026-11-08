//go:build integration

package coordination

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/jobs"
)

// TestR_ARCH_016_ACoalescedEnqueueAdjustsTheWaitingJob: the enqueue of a coalesce kind that finds a waiting job under its key moves it
// to an earlier run time and merges its arguments into it (Q-ARCH-384), so "without delay" never waits behind a delayed job and a
// request for a full rebuild upgrades the incremental one that waits. A running job is never touched; the other modes are unchanged.
func TestR_ARCH_016_ACoalescedEnqueueAdjustsTheWaitingJob(t *testing.T) {
	e := newEnv(t)
	var ran atomic.Int64
	argsSeen := make(chan string, 8)
	kinds := func(r *jobs.Registry, rep *replica) {
		r.RegisterKind("it.move", jobs.KindSpec{DedupMode: jobs.Coalesce, Handler: func(ctx context.Context, run *jobs.Run) error {
			ran.Add(1)
			argsSeen <- string(run.Args)
			return nil
		}})
		r.RegisterKind("it.long", jobs.KindSpec{DedupMode: jobs.Coalesce, Handler: func(ctx context.Context, run *jobs.Run) error {
			select {
			case <-time.After(3 * time.Second):
			case <-ctx.Done():
			}
			return nil
		}})
		r.RegisterKind("it.excl", jobs.KindSpec{DedupMode: jobs.Exclusive, Handler: func(context.Context, *jobs.Run) error { return nil }})
	}
	web := e.newReplica("coalesce-web", replicaOpts{role: "web", valkey: vkLive, kinds: kinds})
	e.newReplica("coalesce-worker", replicaOpts{role: "worker", valkey: vkLive, kinds: kinds})
	waitValkey(t, web)
	ctx := context.Background()

	upgrade := func(waiting json.RawMessage) (json.RawMessage, bool) {
		var a map[string]any
		_ = json.Unmarshal(waiting, &a)
		if a["full"] == true {
			return waiting, false
		}
		a["full"] = true
		out, _ := json.Marshal(a)
		return out, true
	}
	runIn := func(id string) float64 {
		return float64(e.scalar(`SELECT extract(epoch FROM run_at - now())::bigint FROM job WHERE id = $1::uuid`, id))
	}

	t.Run("an enqueue with an earlier run time moves the waiting job there", func(t *testing.T) {
		first, err := web.enqueueTx(ctx, jobs.EnqueueSpec{Kind: "it.move", DedupKey: "index:1", Delay: time.Hour, Args: map[string]any{"full": false}})
		if err != nil || !first.Created {
			t.Fatalf("first: %+v, %v", first, err)
		}
		if d := runIn(first.Job.ID); d < 3500 {
			t.Fatalf("the first job runs in %.0f s, want about an hour", d)
		}
		// A later request does not push it back.
		later, err := web.enqueueTx(ctx, jobs.EnqueueSpec{Kind: "it.move", DedupKey: "index:1", Delay: 2 * time.Hour})
		if err != nil || later.Created || later.Moved || later.Job.ID != first.Job.ID {
			t.Fatalf("later: %+v, %v", later, err)
		}
		if d := runIn(first.Job.ID); d < 3500 || d > 3601 {
			t.Fatalf("a later request changed the run time to %.0f s", d)
		}
		// An earlier one moves it: the same job, now due.
		start := time.Now()
		moved, err := web.enqueueTx(ctx, jobs.EnqueueSpec{Kind: "it.move", DedupKey: "index:1"})
		if err != nil || moved.Created || !moved.Moved || moved.Merged || moved.Job.ID != first.Job.ID {
			t.Fatalf("moved: %+v, %v", moved, err)
		}
		if got := e.scalar(`SELECT count(*) FROM job WHERE kind = 'it.move' AND dedup_key = 'index:1' AND state = 'queued'`); got > 1 {
			t.Fatalf("%d jobs wait under the key", got)
		}
		// The worker takes it at once, not after an hour, with the arguments it had.
		select {
		case args := <-argsSeen:
			if args != `{"full": false}` && args != `{"full":false}` {
				t.Errorf("the job ran with %s", args)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("the moved job did not run")
		}
		if time.Since(start) > 15*time.Second {
			t.Errorf("the moved job waited %s", time.Since(start))
		}
		e.waitState(web, first.Job.ID, jobs.StateSucceeded, 10*time.Second)
	})

	t.Run("MergeArgs upgrades the waiting job, and only that", func(t *testing.T) {
		auto, err := web.enqueueTx(ctx, jobs.EnqueueSpec{Kind: "it.move", DedupKey: "index:2", Delay: time.Hour, Args: map[string]any{"full": false}})
		if err != nil || !auto.Created {
			t.Fatal(err)
		}
		up, err := web.enqueueTx(ctx, jobs.EnqueueSpec{Kind: "it.move", DedupKey: "index:2", Delay: time.Hour, Args: map[string]any{"full": true}, MergeArgs: upgrade})
		if err != nil || up.Created || !up.Merged || up.Moved || up.Job.ID != auto.Job.ID {
			t.Fatalf("upgrade: %+v, %v", up, err)
		}
		var args map[string]any
		if err := json.Unmarshal(e.job(web, auto.Job.ID).Args, &args); err != nil || args["full"] != true {
			t.Errorf("args %v, %v", args, err)
		}
		// Asking again changes nothing.
		again, err := web.enqueueTx(ctx, jobs.EnqueueSpec{Kind: "it.move", DedupKey: "index:2", Delay: time.Hour, MergeArgs: upgrade})
		if err != nil || again.Created || again.Merged || again.Moved || again.Job.ID != auto.Job.ID {
			t.Fatalf("again: %+v, %v", again, err)
		}
		// The upgrade and the move together.
		both, err := web.enqueueTx(ctx, jobs.EnqueueSpec{Kind: "it.move", DedupKey: "index:3", Delay: time.Hour, Args: map[string]any{"full": false}})
		if err != nil {
			t.Fatal(err)
		}
		bm, err := web.enqueueTx(ctx, jobs.EnqueueSpec{Kind: "it.move", DedupKey: "index:3", MergeArgs: upgrade})
		if err != nil || bm.Created || !bm.Merged || !bm.Moved || bm.Job.ID != both.Job.ID {
			t.Fatalf("both: %+v, %v", bm, err)
		}
		// A kind that is not coalesce refuses MergeArgs.
		if _, err := web.enqueueTx(ctx, jobs.EnqueueSpec{Kind: "it.excl", MergeArgs: upgrade}); err == nil {
			t.Error("an exclusive kind took MergeArgs")
		}
	})

	t.Run("a running job is not touched: the enqueue waits as the one job behind it", func(t *testing.T) {
		running, err := web.enqueueTx(ctx, jobs.EnqueueSpec{Kind: "it.long", DedupKey: "index:4", Args: map[string]any{"n": 1}})
		if err != nil || !running.Created {
			t.Fatal(err)
		}
		e.waitState(web, running.Job.ID, jobs.StateRunning, 10*time.Second)
		behind, err := web.enqueueTx(ctx, jobs.EnqueueSpec{Kind: "it.long", DedupKey: "index:4", Args: map[string]any{"n": 2}, MergeArgs: upgrade})
		if err != nil || !behind.Created || behind.Job.ID == running.Job.ID || behind.Merged || behind.Moved {
			t.Fatalf("behind: %+v, %v", behind, err)
		}
		if got := e.job(web, running.Job.ID).Args; string(got) == "" || e.scalar(`SELECT count(*) FROM job WHERE dedup_key = 'index:4' AND state IN ('queued','running')`) != 2 {
			t.Errorf("running args %s", got)
		}
		// A third meets the waiting one and is merged into it, not queued.
		third, err := web.enqueueTx(ctx, jobs.EnqueueSpec{Kind: "it.long", DedupKey: "index:4", MergeArgs: func(w json.RawMessage) (json.RawMessage, bool) {
			return json.RawMessage(`{"n":3}`), true
		}})
		if err != nil || third.Created || !third.Merged || third.Job.ID != behind.Job.ID {
			t.Fatalf("third: %+v, %v", third, err)
		}
		if got := string(e.job(web, behind.Job.ID).Args); got != `{"n": 3}` && got != `{"n":3}` {
			t.Errorf("waiting args %s", got)
		}
		e.waitState(web, behind.Job.ID, jobs.StateSucceeded, 30*time.Second)
	})

	t.Run("the exclusive mode is unchanged: an earlier request returns the existing job as it is", func(t *testing.T) {
		a, err := web.enqueueTx(ctx, jobs.EnqueueSpec{Kind: "it.excl", DedupKey: "x:1", Delay: time.Hour})
		if err != nil || !a.Created {
			t.Fatal(err)
		}
		b, err := web.enqueueTx(ctx, jobs.EnqueueSpec{Kind: "it.excl", DedupKey: "x:1"})
		if err != nil || b.Created || b.Moved || b.Merged || b.Job.ID != a.Job.ID {
			t.Fatalf("b: %+v, %v", b, err)
		}
		if d := runIn(a.Job.ID); d < 3500 {
			t.Errorf("the exclusive job was moved to %.0f s", d)
		}
	})
}
