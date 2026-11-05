//go:build e2e

package e2e

// The long test of the mirror (R-REPO-058, R-REPO-059, E6b.6; owner rule of 2026-11-01: drain, failover and soak run in the nightly
// workflow, on dispatch and locally, never in Test and Release): `kubectl drain` of the node of the process that runs a pass of 300
// packages.

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/npmstub"
)

// TestMirrorLong_R_REPO_058_DrainOfTheNodeOfAPass: the mirror is a remote of the public registry's name (the feed) whose include
// pattern admits the 300 extra packages of the run; the test upstream holds each tarball for a second, so that the pass (two fetches at
// a time, 150 s) outlasts the grace period of the worker pool. Once the pass has filled 40 packages, the node of the pool's process
// that runs it is drained (`kubectl drain`, deploy/dev/drain.sh). The pass ends in the other process of the pool, complete, with every
// package; the test upstream served every tarball, and no more than `concurrency` of them twice (the fetches in flight at the drain,
// which the shutdown cancels and the next attempt repeats).
func TestMirrorLong_R_REPO_058_DrainOfTheNodeOfAPass(t *testing.T) {
	e := mirrorFx(t)
	s := stub(t)
	const concurrency = 2
	body := e.npmBody("npm-mirror-drain-"+e.run, false)
	body["includePatterns"] = []string{"e2e-mirror-" + e.run + "-*"}
	remote := body["remote"].(map[string]any)
	remote["mirror"] = map[string]any{"concurrency": concurrency, "maxVersionsPerPackage": 1}
	key := e.create(t, body)
	s.setMode(t, npmstub.Mode{DelayMS: map[string]int{"document": 100, "tarball": 1000}})
	t.Cleanup(func() { s.setMode(t, npmstub.Mode{}) })
	s.reset(t)

	e.run_(t, key, false)
	st := e.awaitPassStarted(t, key, 40)
	if st.Pass.Method != "feed" {
		t.Errorf("the pass uses the method %q, want feed", st.Pass.Method)
	}
	jobID := st.Pass.Job.ID
	podName, node := poolPodOfPass(t, jobID)
	if !strings.HasPrefix(podName, "trove-worker-"+mirrorPoolName+"-") {
		t.Fatalf("the pass runs in %q, want a process of the pool %s", podName, mirrorPoolName)
	}
	var others []string
	for _, p := range livePods(t, selMirrorPool) {
		if p.Name != podName {
			others = append(others, p.Name+" on "+p.Node)
			if p.Node == node {
				t.Fatalf("both processes of the pool run on %s: a drain would take the other one as well", node)
			}
		}
	}
	t.Logf("the pass (job %s) runs in %s on %s, %d of %d names filled; the other process: %v", jobID, podName, node, st.Pass.PackagesDone, st.Pass.NamesDiscovered, others)
	avoidPrimary(t, node)
	if cur := e.status(t, key); cur.Pass == nil || cur.State != "running" {
		t.Fatalf("the pass is not running any more before the drain started: %s: the upstream is too fast for the cluster", cur.Raw)
	}

	drainDone := make(chan error, 1)
	var drainOut []byte
	drainStart := time.Now()
	go func() {
		cmd := exec.Command(filepath.Join(cfg.repo, "deploy/dev/drain.sh"), node)
		cmd.Dir = cfg.repo
		var err error
		drainOut, err = cmd.CombinedOutput()
		drainDone <- err
	}()

	// The job is read throughout: the process that holds it, its attempt, its state.
	type sample struct{ state, attempt, replica string }
	read := func() (sample, bool) {
		primary, err := kubectl("-n", cfg.namespace, "get", "cluster", "trove-db", "-o", "jsonpath={.status.currentPrimary}")
		if err != nil || strings.TrimSpace(primary) == "" {
			return sample{}, false
		}
		out, err := kubectl("-n", cfg.namespace, "exec", strings.TrimSpace(primary), "-c", "postgres", "--", "psql", "-U", "postgres", "-d", "trove", "-At", "-F", "|", "-c",
			`SELECT j.state, j.attempt, coalesce(r.name, '') FROM trove.job j LEFT JOIN trove.replica r ON r.instance_uid = j.locked_by WHERE j.id = '`+jobID+`'`)
		f := strings.Split(strings.TrimSpace(out), "|")
		if err != nil || len(f) != 3 {
			return sample{}, false
		}
		return sample{f[0], f[1], f[2]}, true
	}
	first, _ := read()
	holders := map[string]bool{podName: true}
	queuedAgain, drained := false, false
	var last sample
	deadline := time.Now().Add(25 * time.Minute)
	for time.Now().Before(deadline) {
		if !drained {
			select {
			case err := <-drainDone:
				drained = true
				t.Logf("drain.sh returned after %s (error %v):\n%s", time.Since(drainStart).Round(time.Second), err, strings.TrimSpace(string(drainOut)))
				if err != nil {
					t.Fatalf("drain.sh: %v", err)
				}
			default:
			}
		}
		cur, ok := read()
		if !ok {
			time.Sleep(time.Second)
			continue
		}
		last = cur
		if cur.replica != "" {
			holders[cur.replica] = true
		}
		queuedAgain = queuedAgain || cur.state == "queued"
		if cur.state == "succeeded" || cur.state == "failed" || cur.state == "cancelled" {
			break
		}
	}
	if !drained {
		if err := <-drainDone; err != nil {
			t.Fatalf("drain.sh: %v\n%s", err, drainOut)
		}
	}
	t.Logf("the job ran in %v, was seen queued again: %v, attempt %s before the drain and %s at the end, state %s", holders, queuedAgain, first.attempt, last.attempt, last.state)
	if last.state != "succeeded" {
		t.Fatalf("the job of the drained pass ended %q: %s", last.state, sql(t, `SELECT coalesce(error::text, '') FROM trove.job WHERE id = '`+jobID+`'`))
	}
	if len(holders) < 2 {
		t.Errorf("only %v held the job: the pass did not end in another process of the pool", holders)
	}
	if last.attempt != first.attempt {
		t.Errorf("the job's attempt was %s before the drain and %s at its end: a release at shutdown does not count an attempt", first.attempt, last.attempt)
	}

	done := e.awaitComplete(t, key, 10*time.Minute)
	sum := done.LastCompletePass
	t.Logf("the pass ended %s: %d packages (%d failed), %d items", sum.Outcome, sum.Packages, sum.PackagesFailed, sum.ItemsStored)
	if sum.Outcome != "complete" || sum.PackagesFailed != 0 || sum.Packages != mirrorExtraPackages {
		t.Errorf("the pass ended %+v, want complete with %d packages and none failed", *sum, mirrorExtraPackages)
	}
	counts := tarballCounts(t, "e2e-mirror-"+e.run+"-")
	twice, missing := 0, 0
	for _, name := range e.extras {
		switch n := counts["/"+name+"/-/"+name+"-1.0.0.tgz"]; {
		case n == 0:
			missing++
		case n == 2:
			twice++
		case n > 2:
			t.Errorf("the test upstream served the tarball of %s %d times", name, n)
		}
	}
	t.Logf("the test upstream served %d tarballs: %d never, %d twice (the fetches in flight at the drain)", len(counts), missing, twice)
	if missing > 0 {
		t.Errorf("the test upstream never served %d of the %d tarballs", missing, len(e.extras))
	}
	if twice > concurrency {
		t.Errorf("R-REPO-058: %d tarballs were served twice, want at most concurrency = %d", twice, concurrency)
	}
	waitServingSettled(t)
	if got := fmt.Sprint(len(readyPodsOf(t, selMirrorPool))); got != "2" {
		t.Errorf("%s ready pods of the pool mirror after the drain, want 2", got)
	}
	e.jobs.check(t)
}
