//go:build e2e

package e2e

// The long test of the OCI mirror (R-REPO-058, R-OCI-026, R-OCI-035; owner rule of 2026-11-01): `kubectl drain` of the node of the process that
// runs a pass of an OCI mirror while docker pulls through the same remote. The set is deploy/dev/e2e.sh oci-long.

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/ocistub"
)

// TestOCILong_R_REPO_058_DrainOfTheNodeOfAMirrorPass: the mirror keeps 400 tags of bulk/tags, each held 300 ms by the test upstream, so that the pass
// outlasts the grace period of the pool. Once it has stored a package, the node of the pool's process that runs it is drained. The pass ends
// complete in the other process; a docker pull through the remote during the drain succeeds; no manifest was fetched more than twice (the fetches
// in flight at the drain, which the shutdown cancels and the next attempt repeats).
func TestOCILong_R_REPO_058_DrainOfTheNodeOfAMirrorPass(t *testing.T) {
	e := mirrorFx(t)
	s := ociStub(t)
	fx := &ociFixture{adm: e.adm, token: e.token, run: e.run} // the service account and the run of the mirror environment
	host := ociRegistryHost(t)
	const concurrency = 2
	s.resetAll(t)
	s.ociMode(t, ocistub.Mode{Catalog: true, DelayMS: map[string]int{ocistub.ClassManifest: 300}})
	t.Cleanup(func() { s.resetAll(t) })

	body := e.ociMirrorBody(t, "oci-mirror-drain-"+e.run, 400)
	body["remote"].(map[string]any)["mirror"] = map[string]any{"concurrency": concurrency, "maxVersionsPerPackage": 400}
	key := e.create(t, body)
	t.Cleanup(func() { e.drop(t, key); e.awaitNoPassRunning(t) })
	s.reset(t)
	e.run_(t, key, false)
	st := e.awaitPassRunning(t, key)
	jobID := st.Pass.Job.ID
	podName, node := poolPodOfPass(t, jobID)
	if !strings.HasPrefix(podName, "trove-worker-"+mirrorPoolName+"-") {
		t.Fatalf("the pass runs in %q, want a process of the pool %s", podName, mirrorPoolName)
	}
	for _, p := range livePods(t, selMirrorPool) {
		if p.Name != podName && p.Node == node {
			t.Fatalf("both processes of the pool run on %s: a drain would take the other one as well", node)
		}
	}
	t.Logf("the pass (job %s) runs in %s on %s, %d packages filled", jobID, podName, node, st.Pass.PackagesDone)
	avoidPrimary(t, node)
	if cur := e.status(t, key); cur.Pass == nil || cur.State != "running" {
		t.Fatalf("the pass is not running any more before the drain started: %s", cur.Raw)
	}

	drainDone := make(chan error, 1)
	var drainOut []byte
	go func() {
		cmd := exec.Command(filepath.Join(cfg.repo, "deploy/dev/drain.sh"), node)
		cmd.Dir = cfg.repo
		var err error
		drainOut, err = cmd.CombinedOutput()
		drainDone <- err
	}()

	// docker pulls through the remote while the node drains
	c := fx.client(t, 0, ociDocker, "mirror-drain-pull")
	c.token = e.token
	c.login(host)
	for _, img := range []string{"alpine:3", "busybox:latest"} {
		name, tag, _ := strings.Cut(img, ":")
		c.pull(fx.image(t, key, "library/"+name, tag))
	}

	if err := <-drainDone; err != nil {
		t.Fatalf("drain.sh: %v\n%s", err, drainOut)
	}
	done := e.awaitComplete(t, key, 25*time.Minute)
	sum := done.LastCompletePass
	t.Logf("the pass ended %s: %d packages (%d failed), %d items", sum.Outcome, sum.Packages, sum.PackagesFailed, sum.ItemsStored)
	if sum.Outcome != "complete" || sum.PackagesFailed != 0 {
		t.Errorf("the pass ended %+v, want complete with no failed package", *sum)
	}
	if after := sql(t, `SELECT coalesce(r.name, '') FROM trove.job j LEFT JOIN trove.replica r ON r.instance_uid = j.locked_by WHERE j.id = '`+jobID+`'`); after == podName {
		t.Errorf("the job ended in %s, the process that was drained", after)
	}
	twice := 0
	for _, en := range s.ociCounts(t).Entries {
		if en.Kind == ocistub.KindManifest && en.Method == "GET" && en.Name == "bulk/tags" {
			switch {
			case en.Count > 2:
				t.Errorf("the test upstream served the manifest %s:%s %d times", en.Name, en.Reference, en.Count)
			case en.Count == 2:
				twice++
			}
		}
	}
	if twice > concurrency {
		t.Errorf("R-REPO-058: %d manifests were served twice, want at most concurrency = %d", twice, concurrency)
	}
	waitServingSettled(t)
	e.jobs.check(t)
}
