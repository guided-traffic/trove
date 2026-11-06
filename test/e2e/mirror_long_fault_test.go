//go:build e2e

package e2e

// The failure suite of R-HA-052 on a mirror pass (R-REPO-053, R-REPO-054, R-REPO-058, R-REPO-059; set `mirror-long`, nightly): the process
// that holds a pass lost in three ways (its pod force-deleted, its kind node stopped, its kind node frozen and thawed), an upstream that
// throttles the pass while the process is drained or killed, and a PostgreSQL switchover and a Valkey failover during a pass. Every
// scenario has a remote of its own over a hundred of the run's extra packages (the digit of the name picks the range), so that the
// content one scenario stores does not make the next one's fetches unnecessary, and the counting test upstream's counters and request
// log tell what each pass sent.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/npmstub"
)

const (
	// longPassNames is the number of packages of the remote of one scenario; longPassConcurrency its mirror.concurrency.
	longPassNames       = 100
	longPassConcurrency = 2
	// the delays of the test upstream: with two fetches at a time a pass of a hundred tarballs lasts about 100 s uninterrupted
	longTarballDelayMS  = 2000
	longDocumentDelayMS = 100
	// resumeWithin is the bound on the time from the loss of the process to the first progress of the pass in another one:
	// jobs.lease_duration (60 s) + the reaper's tick + the next mirror-dispatch (every minute) + the start of the job
	resumeWithin = 5 * time.Minute
)

// ── helpers ──────────────────────────────────────────────────────────────────

// requests is the log of the registry host's requests with their times and statuses (npmstub.RequestLog), oldest first.
func (s *stubControl) requests(t testing.TB) []npmstub.RequestRecord {
	t.Helper()
	var v npmstub.RequestLog
	if err := json.Unmarshal(s.send(t, http.MethodGet, "/-/stub/requests", nil), &v); err != nil {
		t.Fatal(err)
	}
	if v.Dropped > 0 {
		t.Fatalf("the test upstream's request log dropped %d requests", v.Dropped)
	}
	return v.Requests
}

// tryStatus reads the status of a mirror once and reports whether the edge answered it: during the loss of a node the edge
// still sends requests to the api pods on it for a while (R-HA-052 "node loss": tens of seconds), which is no failure of the pass.
func (e *mirrorEnv) tryStatus(key string) (mirrorStatus, bool) {
	r, err := e.adm.send(http.MethodGet, "/api/v1/repositories/"+key+"/mirror", nil)
	if err != nil || r.Status != http.StatusOK {
		return mirrorStatus{}, false
	}
	var s mirrorStatus
	if json.Unmarshal(r.Body, &s) != nil {
		return mirrorStatus{}, false
	}
	s.Raw = r.Body
	return s, true
}

// awaitCompleteTolerant is awaitComplete for a pass whose cluster is being broken: a status the edge does not answer is read again.
func (e *mirrorEnv) awaitCompleteTolerant(t testing.TB, key string, within time.Duration) mirrorStatus {
	t.Helper()
	var s mirrorStatus
	waitFor(t, "the pass of "+key+" to complete", within, 2*time.Second, func() bool {
		cur, ok := e.tryStatus(key)
		if !ok {
			return false
		}
		if cur.State == "failing" {
			t.Fatalf("the mirror of %s is failing: %s (%s)", key, cur.Errors.LastMessage, cur.Errors.LastCode)
		}
		s = cur
		return cur.Pass == nil && cur.LastCompletePass != nil
	})
	return s
}

// heldPass is a pass of a hundred packages that runs in a process of the pool, started for one scenario.
type heldPass struct {
	key              string
	names            []string
	stored, unfilled string // a package the pass has filled early, and the last of the pass's name order
	jobID, pod, node string
	doneAtStart      int64
}

// startHeldPass creates the remote of the scenario over the range `digit` of the run's extra packages, sets the test upstream's mode,
// starts a pass and waits until it has filled `filled` packages. It names the process that holds the pass and requires the other
// process of the pool to be on another node.
func (e *mirrorEnv) startHeldPass(t *testing.T, label string, digit int, mode npmstub.Mode, filled int64) *heldPass {
	t.Helper()
	s := stub(t)
	hp := &heldPass{names: e.extras[digit*longPassNames : (digit+1)*longPassNames]}
	body := e.npmBody("npm-mirror-"+label+"-"+e.run, false)
	body["includePatterns"] = []string{fmt.Sprintf("e2e-mirror-%s-%d*", e.run, digit)}
	body["remote"].(map[string]any)["mirror"] = map[string]any{"concurrency": longPassConcurrency, "maxVersionsPerPackage": 1}
	hp.key = e.create(t, body)
	hp.stored, hp.unfilled = hp.names[0], hp.names[len(hp.names)-1]
	t.Cleanup(func() {
		s.setMode(t, npmstub.Mode{})
		e.drop(t, hp.key)
	})
	s.setMode(t, mode)
	s.reset(t)

	e.run_(t, hp.key, false)
	st := e.awaitPassStarted(t, hp.key, filled)
	if st.Pass.Method != "feed" {
		t.Errorf("the pass uses the method %q, want feed", st.Pass.Method)
	}
	if st.Pass.NamesDiscovered != longPassNames {
		t.Fatalf("the pass discovered %d names, want the %d of the range %d*", st.Pass.NamesDiscovered, longPassNames, digit)
	}
	hp.jobID, hp.doneAtStart = st.Pass.Job.ID, st.Pass.PackagesDone
	hp.pod, hp.node = poolPodOfPass(t, hp.jobID)
	if !strings.HasPrefix(hp.pod, "trove-worker-"+mirrorPoolName+"-") {
		t.Fatalf("the pass runs in %q, want a process of the pool %s", hp.pod, mirrorPoolName)
	}
	for _, p := range livePods(t, selMirrorPool) {
		if p.Name != hp.pod && p.Node == hp.node {
			t.Fatalf("both processes of the pool run on %s: a loss of the node would take the other one as well", hp.node)
		}
	}
	if cur, ok := e.tryStatus(hp.key); !ok || cur.Pass == nil || cur.State != "running" {
		t.Fatalf("the pass is not running any more: the upstream is too fast for the cluster")
	}
	t.Logf("the pass (job %s) runs in %s on %s, %d of %d names filled", hp.jobID, hp.pod, hp.node, st.Pass.PackagesDone, st.Pass.NamesDiscovered)
	return hp
}

// install installs name@1.0.0 through the remote on npm client pod 0 and reports a failure as a message ("" when it worked).
func (e *mirrorEnv) install(t *testing.T, hp *heldPass, label, name string) string {
	t.Helper()
	r, has := e.npmInstall(t, 0, hp.key, label, name, "1.0.0")
	if r.Code != 0 || !has {
		return fmt.Sprintf("npm install %s exited %d (node_modules has it: %v): %s", name, r.Code, has, tailLines(r.Out, 8))
	}
	return ""
}

// assertFetchedOnce checks the counters of the test upstream after a pass that was moved between processes: every tarball of the
// range was served, none more than twice, and at most `longPassConcurrency` twice (the fetches in flight when the process was lost,
// which the next attempt repeats, plus whatever the caller allows); the tarball of `exempt` (also fetched by a client) is not counted against that bound.
func assertFetchedOnce(t *testing.T, hp *heldPass, exempt string, atMostTwice int) {
	t.Helper()
	counts := tarballCounts(t, hp.names[0][:len(hp.names[0])-3])
	twice, missing := 0, 0
	for _, name := range hp.names {
		n := counts["/"+name+"/-/"+name+"-1.0.0.tgz"]
		switch {
		case n == 0:
			missing++
		case name == exempt && n <= 2:
		case n == 2:
			twice++
		case n > 2:
			t.Errorf("the test upstream served the tarball of %s %d times", name, n)
		}
	}
	t.Logf("the test upstream served %d tarballs of the range: %d never, %d twice (the fetches in flight when the process was lost)", len(counts), missing, twice)
	if missing > 0 {
		t.Errorf("the test upstream never served %d of the %d tarballs", missing, len(hp.names))
	}
	if twice > atMostTwice {
		t.Errorf("R-REPO-053: %d tarballs were served twice, want at most %d", twice, atMostTwice)
	}
}

// assertSummary checks the end of the pass: complete, every package of the range counted exactly once, none failed.
func assertSummary(t *testing.T, done mirrorStatus) {
	t.Helper()
	sum := done.LastCompletePass
	t.Logf("the pass ended %s: %d packages (%d failed), %d items", sum.Outcome, sum.Packages, sum.PackagesFailed, sum.ItemsStored)
	if sum.Outcome != "complete" || sum.PackagesFailed != 0 || sum.Packages != longPassNames {
		t.Errorf("the pass ended %+v, want complete with %d packages (each counted once: the old fence writes nothing) and none failed", *sum, longPassNames)
	}
}

// waitPoolWhole waits until the pool has two ready pods, the serving roles have settled and the database has both instances.
func waitPoolWhole(t *testing.T) {
	t.Helper()
	waitFor(t, "two ready pods of the pool mirror", 8*time.Minute, 3*time.Second, func() bool { return len(readyPodsOf(t, selMirrorPool)) == 2 })
	waitFor(t, "both PostgreSQL instances ready", 8*time.Minute, 3*time.Second, func() bool {
		return strings.TrimSpace(kn(t, "get", "cluster", "trove-db", "-o", "jsonpath={.status.readyInstances}")) == "2"
	})
	waitServingSettled(t)
}

// ── the process of the pass lost ─────────────────────────────────────────────

// lossOf is one way to lose the process that holds a pass.
type lossOf struct {
	title, label string
	digit        int
	// inject loses the process and returns the step that undoes it (nil when nothing is to undo).
	inject func(t *testing.T, hp *heldPass) (restore func())
	// clientsWhole says whether the path of the clients is whole again, so that installs through the remote must succeed: always
	// with a lost pod of the pool (which serves no client); with a lost node once the endpoints of its serving pods are gone
	// (R-HA-052 "node loss": requests to the lost pods fail until then).
	clientsWhole func(t *testing.T, hp *heldPass) bool
}

// TestMirrorLong_R_REPO_053_WorkerLostMidPass: the process of the pool that holds a pass of a hundred packages is lost after the pass
// has filled about twenty-five. The lease runs out (jobs.lease_duration), the reaper ends the job, the next mirror-dispatch enqueues
// a new one, a process that claims the queue takes the fence over and continues from the pending names (R-REPO-053 "Crash and
// failover"). Pass conditions: the pass progresses in another process within resumeWithin and ends `complete` with each of its
// hundred packages counted once (what a write of the lost process under the old fence would have added is not there); the test
// upstream served every tarball and at most `concurrency` of them twice; every `npm install` of a stored version through the same
// remote during the loss succeeds (R-REPO-055: clients do not wait for the pool); the pool has two ready pods again.
func TestMirrorLong_R_REPO_053_WorkerLostMidPass(t *testing.T) {
	e := mirrorFx(t)
	for _, l := range []lossOf{
		{
			title: "R-REPO-053 R-HA-052 pod crash: delete --grace-period=0 --force of the process that holds the pass", label: "lost-crash", digit: 0,
			inject: func(t *testing.T, hp *heldPass) func() {
				kn(t, "delete", "pod", hp.pod, "--grace-period=0", "--force")
				return nil
			},
			clientsWhole: func(*testing.T, *heldPass) bool { return true },
		},
		{
			title: "R-REPO-053 R-HA-052 node loss: docker stop of the kind node of the process that holds the pass, then docker start", label: "lost-stop", digit: 1,
			inject:       func(t *testing.T, hp *heldPass) func() { return loseNode(t, hp, "stop", "--time", "1") },
			clientsWhole: nodeEndpointsGone,
		},
		{
			title: "R-REPO-053 R-REPO-058 R-HA-052 node frozen: docker pause of the kind node, thawed after another process took the pass over (the old fence writes nothing)", label: "lost-pause", digit: 2,
			inject:       func(t *testing.T, hp *heldPass) func() { return loseNode(t, hp, "pause") },
			clientsWhole: nodeEndpointsGone,
		},
	} {
		t.Run(l.title, func(t *testing.T) { e.runLostWorker(t, l) })
	}
}

// loseNode stops or freezes the kind node (the container has the node's name) and returns the step that starts or thaws it again;
// the step also runs at the end of the test, so that a failed test leaves the node running. The PostgreSQL primary is moved away
// from the node first (its disruption budget and the loss of the primary are the scenarios of their own).
func loseNode(t *testing.T, hp *heldPass, verb string, args ...string) func() {
	t.Helper()
	avoidPrimary(t, hp.node)
	undo := map[string]string{"stop": "start", "pause": "unpause"}[verb]
	var once sync.Once
	restore := func() {
		once.Do(func() {
			if out, err := exec.Command("docker", undo, hp.node).CombinedOutput(); err != nil {
				t.Errorf("docker %s %s: %v: %s", undo, hp.node, err, out)
			}
			if out, err := kubectl("wait", "--for=condition=Ready", "node/"+hp.node, "--timeout=8m"); err != nil {
				t.Errorf("node %s did not become ready: %v: %s", hp.node, err, out)
			}
		})
	}
	t.Cleanup(restore)
	if out, err := exec.Command("docker", append([]string{verb}, append(args, hp.node)...)...).CombinedOutput(); err != nil {
		t.Fatalf("docker %s %s: %v: %s", verb, hp.node, err, out)
	}
	t.Logf("docker %s %s done", verb, hp.node)
	return restore
}

// nodeEndpointsGone reports that the node of the pass is not ready and none of the serving pods on it is: the edge no longer sends
// requests there.
func nodeEndpointsGone(t *testing.T, hp *heldPass) bool {
	t.Helper()
	ready, err := kubectl("get", "node", hp.node, "-o", `jsonpath={.status.conditions[?(@.type=="Ready")].status}`)
	if err != nil || strings.TrimSpace(ready) == "True" {
		return false
	}
	for _, p := range livePods(t, selServing) {
		if p.Node == hp.node && p.Ready {
			return false
		}
	}
	return true
}

func (e *mirrorEnv) runLostWorker(t *testing.T, l lossOf) {
	s := stub(t)
	hp := e.startHeldPass(t, l.label, l.digit, npmstub.Mode{DelayMS: map[string]int{"document": longDocumentDelayMS, "tarball": longTarballDelayMS}}, 10)
	// a client asks for the last package of the pass, which it has not reached: the remote serves it from the upstream (R-REPO-055)
	if msg := e.install(t, hp, l.label, hp.unfilled); msg != "" {
		t.Fatalf("before the loss: %s", msg)
	}

	restore := l.inject(t, hp)
	injectedAt := time.Now()
	time.Sleep(3 * time.Second) // what the lost process had in flight is over or gone
	mark := len(s.requests(t))
	var atLoss int64
	waitFor(t, "a status after the loss", 2*time.Minute, time.Second, func() bool {
		cur, ok := e.tryStatus(hp.key)
		if ok && cur.Pass != nil {
			atLoss = cur.Pass.PackagesDone
		}
		return ok && cur.Pass != nil
	})
	t.Logf("%d of %d packages filled when the process was lost", atLoss, longPassNames)
	if atLoss >= longPassNames-20 {
		t.Fatalf("the pass had %d packages left when its process was lost: too little to show a continuation", longPassNames-atLoss)
	}

	jobs := map[string]bool{hp.jobID: true}
	var newHolder string
	var resumedAfter time.Duration
	installs := 0
	var installFailures []string
	nextInstall := time.Now()
	last := atLoss
	waitFor(t, "the pass to continue in another process", resumeWithin+10*time.Minute, time.Second, func() bool {
		if cur, ok := e.tryStatus(hp.key); ok && cur.Pass != nil {
			if cur.Pass.Job != nil {
				jobs[cur.Pass.Job.ID] = true
				if newHolder == "" && cur.Pass.Job.ID != hp.jobID {
					newHolder, _ = poolPodOfPass(t, cur.Pass.Job.ID)
				}
			}
			if cur.Pass.PackagesDone < last {
				t.Errorf("the pass's counter went from %d to %d packages: a write under a lost fence", last, cur.Pass.PackagesDone)
			}
			last = max(last, cur.Pass.PackagesDone)
			if resumedAfter == 0 && last > atLoss {
				resumedAfter = time.Since(injectedAt)
				t.Logf("the pass progressed again %s after the loss (%d packages), job(s) %v", resumedAfter.Round(time.Second), last, keysOf(jobs))
			}
		}
		if time.Now().After(nextInstall) && l.clientsWhole(t, hp) {
			installs++
			if msg := e.install(t, hp, l.label, hp.stored); msg != "" {
				installFailures = append(installFailures, msg)
			}
			nextInstall = time.Now().Add(5 * time.Second)
		}
		return resumedAfter != 0 && last >= atLoss+10
	})
	if resumedAfter > resumeWithin {
		t.Errorf("R-REPO-053: the pass progressed again %s after the loss, want within %s", resumedAfter.Round(time.Second), resumeWithin)
	}
	if after := s.requests(t)[mark:]; len(after) == 0 {
		t.Errorf("the test upstream saw no request after the loss, but the pass progressed")
	}
	if restore != nil {
		restore()
	}

	done := e.awaitCompleteTolerant(t, hp.key, 10*time.Minute)
	assertSummary(t, done)
	assertFetchedOnce(t, hp, hp.unfilled, longPassConcurrency)
	if len(jobs) < 2 {
		t.Errorf("only job(s) %v ran the pass: it did not continue in a new job", keysOf(jobs))
	}
	if newHolder == "" || newHolder == hp.pod {
		t.Errorf("the pass continued in %q, want another process than %q", newHolder, hp.pod)
	}
	t.Logf("the lost job %s is %q; the pass continued in %s; %d installs of a stored version through the remote during the loss, %d failed",
		hp.jobID, sql(t, `SELECT state FROM trove.job WHERE id = '`+hp.jobID+`'`), newHolder, installs, len(installFailures))
	if installs == 0 {
		t.Errorf("no install ran during the loss: the clients' path was never whole")
	}
	for _, msg := range installFailures {
		t.Errorf("R-REPO-055: a client install through the remote failed during the loss of the pass's process: %s", msg)
	}
	waitPoolWhole(t)
	e.jobs.check(t)
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ── the upstream throttles while the process moves ───────────────────────────

// TestMirrorLong_R_REPO_054_ThrottledUpstreamAcrossAMove: the test upstream rate-limits the pass's tarball requests while the process
// that runs it is drained or killed. The answers are recorded with their arrival times by the upstream itself (the request log of the
// counting stub). Pass conditions: after an answer that names a wait, the upstream sees no request of the mirror (document or tarball)
// until the wait is over, however the pass moved in the meantime (R-REPO-054: the end of every wait is stored in
// `rate_limited_until`, so that a job released in the middle of it does not resume at once in another process, Q-REPO-187); the number
// of throttled answers stays small (no burst after the move); the pass ends `complete` once the upstream allows again, with every
// tarball served; a client install of a stored version through the remote during the wait succeeds without a request to the upstream.
func TestMirrorLong_R_REPO_054_ThrottledUpstreamAcrossAMove(t *testing.T) {
	e := mirrorFx(t)
	t.Run("R-REPO-054 R-REPO-053 R-HA-059 429 with Retry-After 30 while the node of the pass's process is drained", func(t *testing.T) {
		th := npmstub.Throttle{Kind: npmstub.Throttle429, Class: npmstub.KindTarball, PeriodMS: 45000, RetryAfterSeconds: 30}
		e.runThrottled(t, "throttle-drain", 0, th, http.StatusTooManyRequests, 6, func(t *testing.T, hp *heldPass) func() {
			avoidPrimary(t, hp.node)
			done := make(chan error, 1)
			var out []byte
			go func() {
				cmd := exec.Command(filepath.Join(cfg.repo, "deploy/dev/drain.sh"), hp.node)
				cmd.Dir = cfg.repo
				var err error
				out, err = cmd.CombinedOutput()
				done <- err
			}()
			return func() {
				if err := <-done; err != nil {
					t.Errorf("drain.sh: %v\n%s", err, out)
				}
			}
		})
	})
	t.Run("R-REPO-054 R-REPO-053 R-HA-052 503 SlowDown (no Retry-After) while the pass's process is killed", func(t *testing.T) {
		th := npmstub.Throttle{Kind: npmstub.Throttle503SlowDown, Class: npmstub.KindTarball, Count: 8}
		e.runThrottled(t, "throttle-crash", 1, th, http.StatusServiceUnavailable, 12, func(t *testing.T, hp *heldPass) func() {
			kn(t, "delete", "pod", hp.pod, "--grace-period=0", "--force")
			return nil
		})
	})
}

func (e *mirrorEnv) runThrottled(t *testing.T, label string, digit int, th npmstub.Throttle, status, atMostThrottled int, move func(*testing.T, *heldPass) func()) {
	s := stub(t)
	delays := map[string]int{"document": longDocumentDelayMS, "tarball": longTarballDelayMS}
	hp := e.startHeldPass(t, label, digit, npmstub.Mode{DelayMS: delays}, 20)

	// the upstream starts to throttle; the pass is told, and the move begins when the first answer has gone out
	s.setMode(t, npmstub.Mode{DelayMS: delays, Throttle: &th})
	waitFor(t, "the upstream's first throttled answer", 2*time.Minute, 500*time.Millisecond, func() bool {
		for _, r := range s.requests(t) {
			if r.Status == status {
				return true
			}
		}
		return false
	})
	// R-REPO-054: a client install of a version the pass stored through the remote, while the mirror waits, costs the upstream nothing
	before := len(s.requests(t))
	if msg := e.install(t, hp, label, hp.stored); msg != "" {
		t.Errorf("R-REPO-054: during the wait: %s", msg)
	}
	clientSent := s.requests(t)[before:]
	for _, r := range clientSent {
		if r.Name == hp.stored {
			t.Errorf("R-REPO-054: the install of the stored %s sent %s %s to the upstream", hp.stored, r.Method, r.Path)
		}
	}
	wait := move(t, hp)
	t.Logf("the pass's process %s on %s moved at the first %d answer", hp.pod, hp.node, status)

	done := e.awaitCompleteTolerant(t, hp.key, 15*time.Minute)
	if wait != nil {
		wait()
	}
	assertSummary(t, done)

	log := s.requests(t)
	throttled := 0
	for _, r := range log {
		if r.Status == status {
			throttled++
		}
	}
	violations := requestsInsideWaits(log, status, time.Second)
	t.Logf("the upstream saw %d requests, %d of them answered %d (limit %d); %d inside a wait it had asked for", len(log), throttled, status, atMostThrottled, len(violations))
	for _, v := range violations {
		t.Errorf("R-REPO-054: %s", v)
	}
	if throttled == 0 || throttled > atMostThrottled {
		t.Errorf("R-REPO-054: %d throttled answers, want 1 to %d (a burst after the move would send more)", throttled, atMostThrottled)
	}
	// a throttled answer is a request for the tarball too: the name it hit is asked again after the wait
	assertFetchedOnce(t, hp, "", throttled+longPassConcurrency)
	waitPoolWhole(t)
	e.jobs.check(t)
}

// requestsInsideWaits returns a description of every request that reached the upstream during the wait an earlier throttled answer
// asked for. A throttled answer names its wait by Retry-After; without one the wait is at least one second (R-REPO-054, "a wait is at
// least one second"), of which `tolerance` is not held against the pass (clocks of two processes, a request already on its way).
// Requests that arrived within 500 ms after the answer were sent before the wait began (fetches in flight, answered by the same
// window).
func requestsInsideWaits(log []npmstub.RequestRecord, status int, tolerance time.Duration) []string {
	const inFlight = 500 * time.Millisecond
	var out []string
	for _, a := range log {
		if a.Status != status {
			continue
		}
		wait, tol := time.Duration(a.RetryAfterSeconds)*time.Second, tolerance
		if a.RetryAfterSeconds == 0 {
			wait, tol = time.Second, 200*time.Millisecond
		}
		from, until := a.Done.Add(inFlight), a.Done.Add(wait-tol)
		for _, r := range log {
			if r.At.After(from) && r.At.Before(until) {
				out = append(out, fmt.Sprintf("%s %s arrived %s after the %d answer, whose wait is %s", r.Method, r.Path, r.At.Sub(a.Done).Round(time.Millisecond), status, wait))
			}
		}
	}
	return out
}

// ── the database and Valkey fail over during a pass ──────────────────────────

// TestMirrorLong_R_REPO_053_PostgresSwitchoverAndValkeyFailoverDuringAPass: a PostgreSQL switchover (deploy/dev/failover.sh) at about
// a quarter and a Valkey failover (valkey-failover.sh) at about a half of a pass of a hundred packages. The processes stay; the
// statements are retried (R-HA-009, R-REPO-053 "Crash and failover") and the signals and flights recover (R-HA-057). Pass conditions:
// the same job ends the pass `complete` with each package counted once, and the upstream served every tarball exactly once: a lost
// commit of a deleted pending row fills the package again, which stores nothing twice (R-REPO-026) and so asks the upstream for
// nothing.
func TestMirrorLong_R_REPO_053_PostgresSwitchoverAndValkeyFailoverDuringAPass(t *testing.T) {
	e := mirrorFx(t)
	t.Run("R-REPO-053 R-HA-052 R-HA-057 switchover of PostgreSQL, then planned failover of Valkey", func(t *testing.T) {
		hp := e.startHeldPass(t, "failover", 2, npmstub.Mode{DelayMS: map[string]int{"document": longDocumentDelayMS, "tarball": longTarballDelayMS}}, 15)
		script(t, "failover.sh")
		var mid int64
		waitFor(t, "45 packages after the switchover", 5*time.Minute, time.Second, func() bool {
			cur, ok := e.tryStatus(hp.key)
			if ok && cur.Pass != nil {
				mid = cur.Pass.PackagesDone
			}
			return ok && mid >= 45
		})
		t.Logf("%d packages filled after the switchover of PostgreSQL", mid)
		script(t, "valkey-failover.sh")
		done := e.awaitCompleteTolerant(t, hp.key, 10*time.Minute)
		assertSummary(t, done)
		assertFetchedOnce(t, hp, "", 0)
		waitPoolWhole(t)
		e.jobs.check(t)
	})
}
