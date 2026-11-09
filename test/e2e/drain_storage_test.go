//go:build e2e

package e2e

// R-STOR-046's drain criterion inside the drain test of R-HA-059 (E5.9): "one worker runs gc-full and another a retention run; both
// finish on the remaining worker replicas". The retention runs of R-STOR-040 are not built (they are the lifecycle work of the
// full scope), so the second half is judged on the jobs of the queue `default` that the drain test already runs, the probe jobs
// (drain_load_test.go: every probe job that moved finishes with attempt 1); the collection run is the real one.
//
// A full collection run that deletes blobs ends by waiting delete_settle (30 s) for the deleted rows to become removable
// (R-STOR-024), so a run with candidates is still in flight 30 s after it has found them. The profile `drain` collects without a
// grace period (deploy/dev/values-drain.yaml), so garbage that the test makes just before the drain is a candidate at once. The test
// starts the run until it is claimed by the worker that the drain evicts, and the drain starts while that run is in its wait: the
// precondition, a collection run in flight on the evicted worker, is forced and proved before the fault, not hoped for.

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	gcDrainFiles = 6
	gcDrainSize  = 64 << 10
)

type storageJobs struct {
	mu      sync.Mutex
	repo    string
	jobID   string
	first   string    // the worker that held the job when the drain was about to start
	seenAt  time.Time // when the test saw it running there
	garbage int
}

func newStorageJobs() *storageJobs { return &storageJobs{} }

func (x *storageJobs) prepare(t *testing.T) {
	t.Helper()
	adm := adminAPI(t)
	x.repo = "gcdrain-" + bakRun()
	ensureRepo(t, adm, x.repo)
}

func (x *storageJobs) begin(*testing.T, *variantDrain, time.Time) {}

// garbageBlobs uploads gcDrainFiles files of fresh bytes and deletes them again: their blobs are referenced by nothing.
func (x *storageJobs) garbageBlobs(t *testing.T, adm *api, round int) {
	t.Helper()
	for i := range gcDrainFiles {
		p := fmt.Sprintf("round-%02d/%02d.bin", round, i)
		if r, err := retrying(func() (reply, error) {
			return adm.send(http.MethodPut, filesAddr(x.repo)+"/"+p, randomBytes(gcDrainSize), "Content-Type", "application/octet-stream")
		}); err != nil || r.Status != 201 {
			t.Fatalf("put %s/%s: %v %s", x.repo, p, err, r)
		}
	}
	for i := range gcDrainFiles {
		p := fmt.Sprintf("round-%02d/%02d.bin", round, i)
		if r, err := retrying(func() (reply, error) { return adm.send(http.MethodDelete, filesAddr(x.repo)+"/"+p, nil) }); err != nil || r.Status/100 != 2 {
			t.Fatalf("delete %s/%s: %v %s", x.repo, p, err, r)
		}
	}
	x.garbage += gcDrainFiles
}

type gcJob struct {
	State      string         `json:"state"`
	Attempt    int            `json:"attempt"`
	Replica    *string        `json:"replica"`
	FinishedAt *time.Time     `json:"finishedAt"`
	Result     map[string]any `json:"result"`
}

func readGCJob(t *testing.T, adm *api, id string) gcJob {
	t.Helper()
	var j gcJob
	adm.get("/api/v1/system/jobs/"+id).want(t, "read the gc-full job", 200).JSON(t, &j)
	return j
}

func (j gcJob) ended() bool {
	return j.State == "succeeded" || j.State == "failed" || j.State == "cancelled"
}

func (j gcJob) replica() string {
	if j.Replica == nil {
		return ""
	}
	return *j.Replica
}

// startGC starts a full collection run, waiting for an active run to end first (409 names the active job).
func (x *storageJobs) startGC(t *testing.T, adm *api) string {
	t.Helper()
	var id string
	waitFor(t, "a full collection run to start", 3*time.Minute, 2*time.Second, func() bool {
		r, err := retrying(func() (reply, error) {
			return adm.send(http.MethodPost, "/api/v1/system/tasks/gc-full/run", map[string]any{})
		})
		if err != nil || (r.Status != 202 && r.Status != 409) {
			t.Fatalf("start gc-full: %v %s", err, r)
		}
		if r.Status == 409 {
			return false
		}
		var started struct {
			Job struct {
				ID string `json:"id"`
			} `json:"job"`
		}
		r.JSON(t, &started)
		id = started.Job.ID
		return id != ""
	})
	return id
}

func (x *storageJobs) beforeDrain(t *testing.T, d *variantDrain) {
	t.Helper()
	adm := adminAPI(t)
	evicted := map[string]bool{}
	for _, p := range livePods(t, selMainWorker) {
		if p.Node == d.Node {
			evicted[p.Name] = true
		}
	}
	if len(evicted) == 0 {
		t.Fatalf("no main worker pod runs on %s: the drain evicts no worker", d.Node)
	}
	// gc-full may run only one at a time (409): a run that the other worker claimed is cancelled, then the next one is started with
	// fresh garbage. Each round has the same chance to land on the evicted worker.
	for round := 1; round <= 16; round++ {
		x.garbageBlobs(t, adm, round)
		id := x.startGC(t, adm)
		var j gcJob
		waitFor(t, "the gc-full job to be claimed", 30*time.Second, 250*time.Millisecond, func() bool {
			j = readGCJob(t, adm, id)
			return j.replica() != "" || j.ended()
		})
		if evicted[j.replica()] && j.State == "running" {
			x.mu.Lock()
			x.jobID, x.first, x.seenAt = id, j.replica(), time.Now()
			x.mu.Unlock()
			t.Logf("R-STOR-046: gc-full job %s runs on %s, which the drain of %s evicts (round %d)", id, j.replica(), d.Node, round)
			return
		}
		// the other worker holds the run (in the first run of E5.9 five of six rounds went to the same worker): stop it, so that the next round
		// does not wait for its settle time of 30 s
		t.Logf("R-STOR-046: gc-full job %s ran on %q (%s), not on the evicted worker %v; cancelling it (round %d)", id, j.replica(), j.State, evicted, round)
		if r, err := retrying(func() (reply, error) { return adm.send(http.MethodPost, "/api/v1/system/jobs/"+id+"/cancel", nil) }); err != nil || r.Status/100 != 2 {
			t.Logf("R-STOR-046: cancel of %s: %v %s", id, err, r)
		}
		waitFor(t, "the gc-full job to end", 3*time.Minute, time.Second, func() bool {
			j = readGCJob(t, adm, id)
			return j.ended()
		})
	}
	t.Fatalf("R-STOR-046: no gc-full job was claimed by the evicted worker (%v) in 16 rounds", evicted)
}

func (x *storageJobs) verify(t *testing.T, d *variantDrain, firstEviction, drainEnd time.Time) {
	t.Helper()
	adm := adminAPI(t)
	x.mu.Lock()
	id, first, seenAt := x.jobID, x.first, x.seenAt
	x.mu.Unlock()
	if id == "" {
		t.Error("R-STOR-046: no gc-full job was in flight on the evicted worker")
		return
	}
	var j gcJob
	waitFor(t, "the gc-full job to end", 5*time.Minute, 2*time.Second, func() bool {
		j = readGCJob(t, adm, id)
		return j.ended()
	})
	t.Logf("R-STOR-046: gc-full job %s: %s, attempt %d, last on %q (first on %s, seen running at %s), finished %v, first eviction %s, result %v",
		id, j.State, j.Attempt, j.replica(), first, seenAt.Format("15:04:05"), j.FinishedAt, firstEviction.Format("15:04:05"), j.Result)
	if j.State != "succeeded" {
		t.Errorf("R-STOR-046: the gc-full job ended %s, want succeeded", j.State)
	}
	if j.Attempt != 1 {
		t.Errorf("R-STOR-046: the gc-full job is on attempt %d, want 1 (a release at shutdown does not count an attempt)", j.Attempt)
	}
	if j.replica() == first || j.replica() == "" || !strings.Contains(j.replica(), "-worker-") {
		t.Errorf("R-STOR-046: the gc-full job finished on %q, want a worker other than the evicted %s", j.replica(), first)
	}
	// the drain met the job in flight: it was still running when the pod that held it got its deletion timestamp, so its release is
	// what moved it
	d.watch.mu.Lock()
	evictedAt, ok := d.watch.deleted[first]
	d.watch.mu.Unlock()
	switch {
	case !ok:
		t.Errorf("R-STOR-046: the worker %s that held the gc-full job was not evicted", first)
	case j.FinishedAt == nil || j.FinishedAt.Before(evictedAt.Add(-2*time.Second)):
		t.Errorf("R-STOR-046: the gc-full job finished at %v, before %s was evicted at %s: the drain did not meet it in flight", j.FinishedAt, first, evictedAt.Format("15:04:05.000"))
	}
}
