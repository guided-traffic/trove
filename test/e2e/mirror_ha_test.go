//go:build e2e

package e2e

// R-REPO-058 on kind (set `mirror`): the status from every replica, two processes that claim the queue `mirror`, and a pass with
// Valkey stopped.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/npmstub"
)

// needMirrors makes the tests after the clients-first one independent of it: a run of one of them alone builds the mirrors first.
func (e *mirrorEnv) needMirrors(t *testing.T) {
	t.Helper()
	e.mu.Lock()
	haveNPM, haveNuGet := e.npmMirror != "", e.nugetMirror != ""
	e.mu.Unlock()
	if !haveNPM {
		e.clientsFirstNPM(t)
	}
	if !haveNuGet {
		e.clientsFirstNuGet(t)
	}
}

// canonical is a JSON document with its members in a fixed order, so two answers can be compared as bytes.
func canonical(t testing.TB, raw []byte) string {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(v)
	return string(out)
}

// sameStatusOnEveryReplica reads the status of a repository through each of the three api pods, each on a port-forward of its own, and
// requires one answer. The packages pods, the client-serving replicas, serve the package routes only after the cut into roles
// (R-ARCH-001): the answer of one of them is logged, and a mirror's status is read where the management API is.
func sameStatusOnEveryReplica(t *testing.T, e *mirrorEnv, key string) mirrorStatus {
	t.Helper()
	addrs, stop := apiForwards(t)
	defer stop()
	if len(addrs) != 3 {
		t.Fatalf("%d api pods, want 3", len(addrs))
	}
	var first mirrorStatus
	var want string
	for i, addr := range addrs {
		s := mirrorStatusAt(t, apiAtPod(t, addr, adminToken(t)), key)
		got := canonical(t, s.Raw)
		if i == 0 {
			first, want = s, got
			continue
		}
		if got != want {
			t.Errorf("R-REPO-058: the status of %s differs between api pod 0 and api pod %d:\n%s\n%s", key, i, want, got)
		}
	}
	// the packages pods: what they answer is logged
	paddrs, pstop := packagesForwards(t)
	defer pstop()
	if len(paddrs) > 0 {
		r := apiAtPod(t, paddrs[0], adminToken(t)).getOverForward(reposBase + key + "/mirror")
		t.Logf("a packages pod answers GET %s%s/mirror with %d", reposBase, key, r.Status)
	}
	return first
}

// TestMirror_R_REPO_058_StatusFromEveryReplica: the status operation answers the same on every replica that serves it, after the
// complete passes of the clients-first test, for the npm and the NuGet mirror.
func TestMirror_R_REPO_058_StatusFromEveryReplica(t *testing.T) {
	e := mirrorFx(t)
	e.needMirrors(t)
	for _, key := range []string{e.npmMirror, e.nugetMirror} {
		s := sameStatusOnEveryReplica(t, e, key)
		if s.Mode != "mirror" || s.Pass != nil || s.LastCompletePass == nil || s.LastCompletePass.Outcome != "complete" {
			t.Errorf("the status of %s is %s", key, s.Raw)
		}
	}
	e.jobs.check(t)
}

// TestMirror_R_REPO_058_TwoClaimantsOneJobPerRepository: the two processes of the pool run the task mirror-dispatch every minute
// beside the requests of a run (R-REPO-056) that arrive at the same moment from three callers; six repositories are due at the first
// tick. The job table, read every 400 ms from before the first request until every pass has ended, never holds two unfinished
// mirror-pass jobs of one repository (the sampler of the environment), and every pass ends complete.
func TestMirror_R_REPO_058_TwoClaimantsOneJobPerRepository(t *testing.T) {
	e := mirrorFx(t)
	s := stub(t)
	s.setMode(t, npmstub.Mode{DelayMS: map[string]int{"tarball": 300}})
	t.Cleanup(func() { s.setMode(t, npmstub.Mode{}) })
	const repos = 6
	var keys []string
	for i := 0; i < repos; i++ {
		keys = append(keys, e.create(t, e.npmNamesBody(fmt.Sprintf("npm-mirror-dedup%d-%s", i, e.run), e.extras[100+3*i:103+3*i])))
	}
	startedAt := time.Now()
	claimed0 := claimedBy(t, "mirror-dispatch")
	statuses := map[int]int{}
	var mu sync.Mutex
	round := func() {
		var wg sync.WaitGroup
		for _, key := range keys {
			for range 3 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					r, err := e.adm.send(http.MethodPost, reposBase+key+"/mirror/run", map[string]any{})
					mu.Lock()
					defer mu.Unlock()
					if err != nil {
						statuses[0]++
						return
					}
					statuses[r.Status]++
				}()
			}
		}
		wg.Wait()
	}
	// three rounds, at the creation and across two dispatch ticks
	for i := 0; i < 3; i++ {
		round()
		if i < 2 {
			time.Sleep(62 * time.Second)
		}
	}
	for _, key := range keys {
		sum := e.awaitComplete(t, key, 5*time.Minute).LastCompletePass
		if sum.Outcome != "complete" || sum.Packages < 3 || sum.PackagesFailed != 0 {
			t.Errorf("the pass of %s ended %+v", key, *sum)
		}
	}
	ticks := sql(t, fmt.Sprintf(`SELECT count(*) FROM trove.job WHERE kind = 'mirror-dispatch' AND created_at > now() - interval '%d seconds'`, int(time.Since(startedAt).Seconds())+5))
	claimed1 := claimedBy(t, "mirror-dispatch")
	t.Logf("run requests answered %v over %s; the task mirror-dispatch was enqueued %s times in that time; claimed by the pool's processes: before %v, after %v",
		statuses, time.Since(startedAt).Round(time.Second), ticks, claimed0, claimed1)
	if statuses[http.StatusAccepted] == 0 {
		t.Errorf("no request of a run was accepted: %v", statuses)
	}
	for code := range statuses {
		if code != http.StatusAccepted && code != http.StatusConflict {
			t.Errorf("a request of a run was answered %d: %v", code, statuses)
		}
	}
	if ticks == "0" || ticks == "1" {
		t.Errorf("the dispatch ticked %s times in %s, want at least two", ticks, time.Since(startedAt).Round(time.Second))
	}
	e.jobs.check(t)
}

// TestMirror_R_REPO_058_ValkeyStoppedForAWholePass: Valkey and its Sentinels are down from before the mirror is created until its pass
// has ended. The pass is the one the dispatch starts for a new mirror (R-REPO-050: within a minute of the mode switch); no run is
// requested, because a request after that pass would start a second one. The pass completes (the dispatch, the fence, the cursors and
// the bounds are in PostgreSQL; the yield signal is the only thing Valkey carried), the test upstream serves each tarball once, and the
// status is the same on every replica that serves it.
func TestMirror_R_REPO_058_ValkeyStoppedForAWholePass(t *testing.T) {
	e := mirrorFx(t)
	s := stub(t)
	names := e.extras[200:240]
	var key string
	withValkeyStopped(t, func() {
		servingReadyDegraded(t, true)
		s.reset(t)
		key = e.create(t, e.npmNamesBody("npm-mirror-valkey-"+e.run, names))
		done := e.awaitComplete(t, key, 5*time.Minute)
		sum := done.LastCompletePass
		t.Logf("with Valkey stopped the pass ended %s: %d packages (%d failed), %d items, method %s", sum.Outcome, sum.Packages, sum.PackagesFailed, sum.ItemsStored, sum.Method)
		if sum.Outcome != "complete" || sum.PackagesFailed != 0 || sum.Packages != int64(len(names)) {
			t.Errorf("the pass ended %+v, want complete with %d packages", *sum, len(names))
		}
		done = e.awaitFolded(t, key, 2*time.Minute)
		same := sameStatusOnEveryReplica(t, e, key)
		if canonical(t, same.Raw) != canonical(t, done.Raw) {
			t.Errorf("the status changed between two reads:\n%s\n%s", done.Raw, same.Raw)
		}
	})
	counts := tarballCounts(t, "e2e-mirror-"+e.run+"-")
	for _, name := range names {
		if n := counts["/"+name+"/-/"+name+"-1.0.0.tgz"]; n != 1 {
			t.Errorf("the test upstream served the tarball of %s %d times, want once", name, n)
		}
	}
	// Valkey is back: the replicas settle and the status is unchanged
	waitServingSettled(t)
	servingReadyDegraded(t, false)
	sameStatusOnEveryReplica(t, e, key)
	e.jobs.check(t)
}
