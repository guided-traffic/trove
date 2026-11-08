//go:build integration

package mirror

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/mirror"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// slowGate holds every fill back by d, until the context ends.
func slowGate(d time.Duration) func(ctx context.Context, path string) error {
	return func(ctx context.Context, _ string) error {
		select {
		case <-time.After(d):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// blockAt holds the fill of one path back until the context ends or the returned release is called.
func blockAt(path string) (gate func(ctx context.Context, p string) error, release func()) {
	ch := make(chan struct{})
	var once sync.Once
	return func(ctx context.Context, p string) error {
			if p != path {
				return nil
			}
			select {
			case <-ch:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}, func() {
			once.Do(func() { close(ch) })
		}
}

// R-REPO-053: the process that runs a pass loses its lease: the next dispatch after the lease expiry enqueues a new job that
// ends the pass from the pending names, and a pending-row delete sent afterwards under the old fence changes no row.
func TestR_REPO_053_ALostLeaseIsContinuedByTheNextDispatchAndTheOldFenceWritesNothing(t *testing.T) {
	w := newWorld(8)
	gate, _ := blockAt(pathOf("pkg-003"))
	w.setGate(gate)
	e := newEnv(t, w, "mirror", nil, nil)
	rep := e.start("rep-a", "all")
	e.dispatch(rep)
	e.waitFills(3, 60*time.Second)
	oldJob := e.text(`SELECT pass_job_id::text FROM mirror_state WHERE repo_id = $1`)
	oldHolder := e.text(`SELECT pass_holder::text FROM mirror_state WHERE repo_id = $1`)
	if oldJob == "" {
		t.Fatal("the running pass has no fence")
	}
	// The test stops the heartbeat's effect: the row of the job belongs to nobody and its lease has run out.
	e.st.Exec(`UPDATE job SET locked_by = gen_random_uuid(), locked_until = now() - interval '1 second' WHERE kind = 'mirror-pass' AND state = 'running'`)
	testenv.WaitFor(t, "the job to end", 30*time.Second, func() bool { return e.activePasses() == 0 })
	if n := e.st.Number(`SELECT count(*) FROM job WHERE kind = 'mirror-pass' AND state = 'failed'`); n != 1 {
		t.Fatalf("the reaper did not end the job with MaxAttempts 1 (%d failed)", n)
	}
	// The next dispatch enqueues a new job for the pass in progress; it takes the fence over and holds at pkg-005.
	gate2, release := blockAt(pathOf("pkg-005"))
	w.setGate(gate2)
	e.dispatch(rep)
	testenv.WaitFor(t, "the new job to take the fence over", 30*time.Second, func() bool {
		return e.text(`SELECT pass_job_id::text FROM mirror_state WHERE repo_id = $1`) != oldJob
	})
	e.waitFills(5, 30*time.Second)
	before := e.snapshot()
	err := e.store.PackageDone(context.Background(), mirror.Fence{RepoID: e.repoN, JobID: oldJob, Holder: oldHolder}, mirror.PackageResult{Name: "pkg-007"})
	if !errors.Is(err, mirror.ErrFenceLost) {
		t.Fatalf("a delete under the old fence: %v", err)
	}
	if after := e.snapshot(); after != before {
		t.Fatalf("a write under the old fence changed rows:\n%s\n%s", before, after)
	}
	release()
	e.waitOutcome("complete", 60*time.Second)
	for _, n := range w.names {
		if got := w.times(pathOf(n)); got != 1 {
			t.Errorf("%s stored %d times", n, got)
		}
	}
	if left := e.pending(); len(left) != 0 {
		t.Fatalf("pending names left: %v", left)
	}
}

// R-REPO-053: a process that is shut down while it runs a pass releases the job with its attempt unchanged; a second process
// that claims the queue takes the pass over and ends it, and every artifact is stored once apart from the package in progress.
func TestR_REPO_053_AShutdownReleasesTheJobAndASecondEngineEndsThePass(t *testing.T) {
	w := newWorld(8)
	gate, _ := blockAt(pathOf("pkg-003"))
	w.setGate(gate)
	e := newEnv(t, w, "mirror", nil, nil)
	a := e.start("rep-a", "all", "TROVE_SERVER_SHUTDOWN_TIMEOUT=8s")
	e.dispatch(a)
	e.waitFills(3, 60*time.Second)
	if n := e.st.Number(`SELECT attempt FROM job WHERE kind = 'mirror-pass'`); n != 1 {
		t.Fatalf("attempt of the running job: %d", n)
	}
	if err := a.Stop(); err != nil {
		t.Fatal(err)
	}
	if got := e.st.Text(`SELECT state::text || '|' || attempt::text FROM job WHERE kind = 'mirror-pass'`); got != "queued|0" {
		t.Fatalf("the released job is %q, want queued with its attempt unchanged (queued|0)", got)
	}
	if n := e.st.Number(`SELECT count(*) FROM mirror_state WHERE repo_id = $1 AND pass_started_at IS NOT NULL`, e.repoN); n != 1 {
		t.Fatal("the release cleared the pass")
	}
	if n := e.st.Number(`SELECT consecutive_failures FROM mirror_state WHERE repo_id = $1`, e.repoN); n != 0 {
		t.Fatal("the release counted a failure")
	}
	w.setGate(nil)
	e.start("rep-b", "all")
	e.waitOutcome("complete", 60*time.Second)
	waitLatestPassJob(t, e.st, "succeeded", 30*time.Second)
	for _, n := range w.names {
		if got := w.times(pathOf(n)); got != 1 {
			t.Errorf("%s stored %d times", n, got)
		}
	}
}

// R-REPO-053: pausing the mirror during a pass ends it with stopped before its next package and keeps it; resuming continues it
// from the pending names.
func TestR_REPO_053_APauseStopsThePassAndAResumeContinuesIt(t *testing.T) {
	w := newWorld(30)
	w.setGate(slowGate(150 * time.Millisecond))
	e := newEnv(t, w, "mirror", nil, nil)
	rep := e.start("rep-a", "all")
	e.dispatch(rep)
	e.waitFills(3, 60*time.Second)
	e.setDocument(rep, remoteDoc("mirror", nil, settings{"paused": true}))
	e.waitOutcome("stopped", 30*time.Second)
	stopped := w.storedCount()
	if stopped >= 30 {
		t.Fatal("the pass was not stopped by the pause")
	}
	if n := e.st.Number(`SELECT count(*) FROM mirror_state WHERE repo_id = $1 AND pass_started_at IS NOT NULL`, e.repoN); n != 1 {
		t.Fatal("a pause cleared the pass")
	}
	if left := e.pending(); len(left) == 0 {
		t.Fatal("a pause deleted the pending names")
	}
	// Nothing runs while it is paused: the dispatch enqueues no pass.
	e.dispatch(rep)
	time.Sleep(2 * time.Second)
	if e.activePasses() != 0 || w.storedCount() != stopped {
		t.Fatal("a paused mirror stored or enqueued something")
	}
	e.setDocument(rep, remoteDoc("mirror", nil, settings{"paused": false}))
	e.waitMirror(rep, "the resume", func(s repository.RemoteSettings) bool { return !s.Mirror.Paused })
	w.setGate(nil)
	e.dispatch(rep)
	testenv.WaitFor(t, "the pass to complete after the resume", 60*time.Second, func() bool {
		return e.text(`SELECT COALESCE(last_pass->>'outcome', '') FROM mirror_state WHERE repo_id = $1`) == "complete"
	})
	for _, n := range w.names {
		if got := w.times(pathOf(n)); got != 1 {
			t.Errorf("%s stored %d times", n, got)
		}
	}
}

// R-REPO-053, Q-REPO-148: switching the mode to pullThrough during a pass ends it with stopped and clears it; the stored content
// stays.
func TestR_REPO_053_ASwitchToPullThroughClearsThePass(t *testing.T) {
	w := newWorld(30)
	w.setGate(slowGate(150 * time.Millisecond))
	e := newEnv(t, w, "mirror", nil, nil)
	rep := e.start("rep-a", "all")
	e.dispatch(rep)
	e.waitFills(3, 60*time.Second)
	e.setDocument(rep, remoteDoc("pullThrough", nil, nil))
	e.waitOutcome("stopped", 30*time.Second)
	if n := e.st.Number(`SELECT count(*) FROM mirror_state WHERE repo_id = $1 AND pass_started_at IS NULL AND pass_job_id IS NULL`, e.repoN); n != 1 {
		t.Fatal("a mode change did not clear the pass")
	}
	if left := e.pending(); len(left) != 0 {
		t.Fatalf("pending names survived the mode change: %v", left)
	}
	if w.storedCount() == 0 {
		t.Fatal("what the mirror stored is gone")
	}
}

// R-REPO-053 stops: a pass whose repository went offline ends with offline without a request and without a failure; it stays
// for the repository to come back.
func TestR_REPO_053_AnOfflineRepositoryEndsThePassWithoutAFailure(t *testing.T) {
	w := newWorld(3)
	e := newEnv(t, w, "mirror", nil, nil)
	rep := e.start("rep-a", "all", "TROVE_PROXY_GLOBAL_OFFLINE=true")
	e.dispatch(rep)
	e.waitOutcome("offline", 60*time.Second)
	if w.namesCalls.Load() != 0 || w.storedCount() != 0 {
		t.Fatal("an offline pass sent a request")
	}
	if n := e.st.Number(`SELECT consecutive_failures FROM mirror_state WHERE repo_id = $1`, e.repoN); n != 0 {
		t.Fatalf("offline counted %d failures", n)
	}
	if got := e.jobResult("succeeded")["outcome"]; got != "offline" {
		t.Fatalf("job result %v", got)
	}
}
