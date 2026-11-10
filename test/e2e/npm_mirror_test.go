//go:build e2e

package e2e

// The npm side of the remote full mirror on kind (R-REPO-055, R-REPO-059; set `npm`, and the install-time ratio with TROVE_PERF_GATE=1 in the
// set `npm-ha-soak` of the nightly workflow): a pass runs while `npm ci` runs through the same remote on the three client-serving replicas,
// the pass in the pool of two processes that claim the queue `mirror` alone, and a client installs what the mirror stored while the test
// upstream is stopped. The NuGet side is nuget_mirror_test.go, the OCI one oci_mirror_test.go, the harness mirror_env_test.go.

import (
	"fmt"
	"net/http"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/npmstub"
)

// installBudget is the factor of R-REPO-055: the install through a remote that a pass fills takes at most this much of its time
// without the pass.
const installBudget = 1.2

// TestNPMMirror_R_REPO_055_ClientsFirst is the e2e criterion of R-REPO-055 and E6b.6 for npm; the NuGet criterion is
// TestNuGetMirror_R_REPO_055_ClientsFirst.
//
// The remote npm-mirror-<run> is a mirror of the npm test upstream under the public registry's name (the feed lists the 700
// packages of its corpus and the 300 extra ones the test published); maxConcurrentFetches is 8 and the mirror's concurrency 4, so
// B = 2 and the bound is 8 + 3 - 1 + 2 = 12. The baseline is `npm ci` of the reference project (707 locked packages) through a
// second remote of the same settings whose mirror is paused, so that its cache is as cold as the first one's and only the pass
// differs; the run with the pass starts once the pass has filled packages. The test upstream holds every tarball and document for
// 120 ms and 20 ms, so that fetches overlap.
func TestNPMMirror_R_REPO_055_ClientsFirst(t *testing.T) {
	e := mirrorFx(t)
	t.Run("npm", func(t *testing.T) { e.clientsFirstNPM(t) })
	t.Run("preemption", func(t *testing.T) { e.clientsFirstPreemption(t) })
	e.jobs.check(t)
}

// clientsFirstPairs is the number of pairs (an install without a pass, an install with one) of the npm measurement. The criterion is
// judged on the median of the installs with a pass against the best install without one: an install lasts 12 s, and a stall of the host
// (the cluster shares a machine; installs of 20 s and 23 s were measured among the 12 s ones) of a few seconds is more than the 20 % the
// criterion allows. The best baseline is the machine's speed without the pass, which noise cannot make look better. The number of pairs is
// fixed here and no pair is repeated.
const clientsFirstPairs = 3

func (e *mirrorEnv) clientsFirstNPM(t *testing.T) {
	s := stub(t)
	b := mirrorShare(mirrorConcurrency, mirrorMaxFetches)
	bound := mirrorBound(mirrorMaxFetches, mirrorServing, b)
	s.setMode(t, npmstub.Mode{DelayMS: map[string]int{"document": 20, "tarball": 120}})
	t.Cleanup(func() { s.setMode(t, npmstub.Mode{}) })

	// The readings of Q-REPO-236 run over the pairs: the primary's waits and the standby's lag, the runner's pressure and the events below.
	rd := startReadings(t)
	var bases, loads []time.Duration
	var key string
	// A client fetch pre-empts a background fetch that is in its upstream exchange when the client has to wait for a slot. Since the share of a
	// slot ends with the exchange (Q-REPO-235) the clients of the pairs rarely wait, so the counter of the pairs is logged; the rule itself is
	// proved with forced contention by the subtest preemption (Q-REPO-253).
	var preemptions float64
	maxOpen := 0
	for i := range clientsFirstPairs {
		last := i == clientsFirstPairs-1
		// the baseline: the mirror of the idle remote is paused, so no pass runs; both remotes start with an empty cache
		idle := e.create(t, e.npmBody(fmt.Sprintf("npm-mirror-idle%d-%s", i, e.run), true))
		rd.event(i, "baseline-created", map[string]any{"key": idle})
		rd.event(i, "baseline-ci-start", nil)
		base, n := e.npmCI(t, 0, idle, fmt.Sprintf("base%d", i))
		rd.event(i, "baseline-ci-end", map[string]any{"duration_ms": base.Dur.Milliseconds(), "exit": base.Code})
		if base.Code != 0 {
			t.Fatalf("the baseline npm ci through %s exited %d:\n%s", idle, base.Code, tailLines(base.Out, 30))
		}
		bases = append(bases, base.Dur)

		// the run with the pass: requested now, and it has filled packages before the install starts
		key = e.create(t, e.npmBody(fmt.Sprintf("npm-mirror%d-%s", i, e.run), false))
		rd.event(i, "loaded-created", map[string]any{"key": key})
		s.reset(t) // the counters and the maximum of open requests count from the pass's start
		rd.event(i, "pass-requested", nil)
		e.run_(t, key, false)
		st := e.awaitPassStarted(t, key, 5)
		if st.Pass.Method != "feed" {
			t.Errorf("the pass uses the method %q, want feed", st.Pass.Method)
		}
		pod, node := poolPodOfPass(t, st.Pass.Job.ID)
		if !strings.HasPrefix(pod, "trove-worker-"+mirrorPoolName+"-") {
			t.Errorf("the pass runs in %q (node %s), want a process of the pool %s", pod, node, mirrorPoolName)
		}
		rd.event(i, "pass-started", map[string]any{"pod": pod, "packages_done": st.Pass.PackagesDone})
		before := preempted(t, key)
		rd.event(i, "loaded-ci-start", nil)
		loaded, n2 := e.npmCI(t, 0, key, fmt.Sprintf("load%d", i))
		rd.event(i, "loaded-ci-end", map[string]any{"duration_ms": loaded.Dur.Milliseconds(), "exit": loaded.Code})
		after := preempted(t, key)
		end := e.status(t, key)
		if loaded.Code != 0 {
			t.Fatalf("npm ci through %s with a pass running exited %d:\n%s", key, loaded.Code, tailLines(loaded.Out, 30))
		}
		loads = append(loads, loaded.Dur)
		passDuring := "had ended"
		if end.Pass != nil {
			passDuring = "still ran"
		}
		t.Logf("pair %d: npm ci of %d packages without a pass %s, with the pass (%s in %s, %d packages filled when it started) %s (%.0f %%); the pass %s when it ended; trove_mirror_preempted_total %v -> %v",
			i, n, base.Dur.Round(time.Millisecond), st.Pass.Method, pod, st.Pass.PackagesDone, loaded.Dur.Round(time.Millisecond), float64(loaded.Dur)/float64(base.Dur)*100, passDuring, before, after)
		if n2 != n {
			t.Errorf("the install with the pass has %d packages, the baseline %d", n2, n)
		}
		preemptions += after - before
		if m := npmInflight(t).Max; m > maxOpen {
			maxOpen = m
		}
		if !last {
			// the pass of this pair is not needed any more: the repository goes, and the pass halts before its next package
			rd.event(i, "drop-start", nil)
			e.drop(t, key, idle)
			rd.event(i, "drop-end", nil)
			e.awaitNoPassRunning(t)
			rd.event(i, "no-pass-running", nil)
		}
	}
	bestBase, medLoad := slices.Min(bases), median(loads)
	ratio := float64(medLoad) / float64(bestBase)
	t.Logf("npm ci without a pass: %v, with a pass: %v; best without %s, median with %s: %.0f %% (budget %.0f %%)", bases, loads, bestBase.Round(time.Millisecond), medLoad.Round(time.Millisecond), ratio*100, installBudget*100)
	rd.event(-1, "ratio", map[string]any{"best_baseline_ms": bestBase.Milliseconds(), "median_loaded_ms": medLoad.Milliseconds(), "ratio": ratio, "budget": installBudget})
	rd.close()
	// One loaded install above the budget, whether the bound is enforced or only logged, keeps the cluster's logs while the pool's
	// processes (they ran the pass) and the PostgreSQL log with its slow statements still exist. No pass condition depends on it.
	if slices.ContainsFunc(loads, func(d time.Duration) bool { return float64(d) > installBudget*float64(bestBase) }) {
		captureClusterLogs(t)
	}
	// The time of an install is the CPU of the runner as much as trove's: per change the ratio is logged, and the nightly run
	// enforces it with TROVE_PERF_GATE=1, as the latency bounds of Q-ARCH-220 (Q-REPO-197). The bound on the upstream's open
	// requests and the pre-emptions, which clients first is about, are enforced in every run.
	switch {
	case ratio <= installBudget:
	case os.Getenv("TROVE_PERF_GATE") != "1":
		t.Logf("R-REPO-055: npm ci with the pass took %s at the median of %d installs, %.0f %% of the best install without it (%s), above %.0f %%; enforced with TROVE_PERF_GATE=1 (Q-REPO-197)", medLoad, clientsFirstPairs, ratio*100, bestBase, installBudget*100)
	default:
		t.Errorf("R-REPO-055: npm ci with the pass took %s at the median of %d installs, %.0f %% of the best install without it (%s), want at most %.0f %%", medLoad, clientsFirstPairs, ratio*100, bestBase, installBudget*100)
	}

	// the last pass goes on to its end through the same remote: every package stored once
	done := e.awaitComplete(t, key, 20*time.Minute)
	maxOpen = max(maxOpen, npmInflight(t).Max)
	t.Logf("trove_mirror_preempted_total grew by %v over the %d pairs; the test upstream held at most %d requests open while a pass ran (bound %d = %d + %d - 1 + %d)",
		preemptions, clientsFirstPairs, maxOpen, bound, mirrorMaxFetches, mirrorServing, b)
	if maxOpen > bound {
		t.Errorf("R-REPO-055: the test upstream held %d requests open at once, want at most maxConcurrentFetches + n - 1 + B = %d", maxOpen, bound)
	}
	sum := done.LastCompletePass
	t.Logf("the pass ended %s: %d packages (%d failed), %d items, %d bytes, method %s, kind %s", sum.Outcome, sum.Packages, sum.PackagesFailed, sum.ItemsStored, sum.BytesStored, sum.Method, sum.Kind)
	if sum.Outcome != "complete" || sum.PackagesFailed != 0 || sum.Packages < mirrorExtraPackages {
		t.Errorf("the pass ended %+v, want complete with no failed package and at least %d packages", *sum, mirrorExtraPackages)
	}
	extra := tarballCounts(t, "e2e-mirror-"+e.run+"-")
	for _, name := range e.extras {
		if n := extra["/"+name+"/-/"+name+"-1.0.0.tgz"]; n != 1 {
			t.Errorf("the test upstream served the tarball of %s %d times, want once", name, n)
			break
		}
	}
	e.mu.Lock()
	e.npmMirror = key
	e.mu.Unlock()
}

// median of a list of durations.
func median(d []time.Duration) time.Duration {
	c := append([]time.Duration(nil), d...)
	sort.Slice(c, func(i, j int) bool { return c[i] < c[j] })
	return c[len(c)/2]
}

// clientsFirstPreemption is the pre-emption half of the e2e criterion of R-REPO-055, with the contention forced (Q-REPO-253): a pass runs
// through a remote of the private test upstream while the stub holds every tarball 1.5 s (the delay is keyed by the kind of request for
// the whole stub, not per remote: it holds while this subtest runs, the subtests run one after another and each resets the stub), so the
// pass's background fetches sit in their upstream exchange; then more distinct tarballs are requested through the three client-serving
// replicas than the repository's budget lets through. The clients that find no slot raise mirror_yield and the pool's processes pre-empt
// the pass's fetches: trove_mirror_preempted_total grows, and every client is served.
//
// The margin against the queue timeout (10 s): the binding limit is the repository's budget maxConcurrentFetches = 8, so 24 requests
// make 3 waves of about 1.55 s (a tarball and, if the replica asks for it, a document of 20 ms); the last request waits about
// 3.1 s for a slot, 6.9 s short of the timeout, while 24 > 8 keeps the contention certain.
func (e *mirrorEnv) clientsFirstPreemption(t *testing.T) {
	s := stub(t)
	const passNames, burst = 60, 24
	var names []string
	for i := 0; i < passNames+burst; i++ {
		name := fmt.Sprintf("e2e-preempt-%s-%03d", e.run, i)
		s.publish(t, name, "1.0.0", nil)
		names = append(names, name)
	}
	s.setMode(t, npmstub.Mode{DelayMS: map[string]int{"document": 20, "tarball": 1500}})
	t.Cleanup(func() { s.setMode(t, npmstub.Mode{}) })

	key := e.create(t, e.npmNamesBody("npm-mirror-preempt-"+e.run, names))
	// the remote and its pass go at the end of the subtest, a failure included, so that no pass runs on into the next test
	t.Cleanup(func() {
		e.drop(t, key)
		e.awaitNoPassRunning(t)
	})
	e.run_(t, key, false)
	st := e.awaitPassStarted(t, key, 2)
	t.Logf("the pass runs (%s, %d packages filled) when the clients arrive", st.Pass.Method, st.Pass.PackagesDone)
	// The first signal of a waiting client is the one that counts: the pass then starts nothing for 30 s (the yield window), so it must
	// find the pass's two background fetches in their exchange. The burst starts when the stub holds two requests open.
	waitFor(t, "the pass's two background fetches to be in their upstream exchange", 30*time.Second, 50*time.Millisecond, func() bool { return npmInflight(t).Open >= 2 })
	before := preempted(t, key)

	// the last names: the pass takes the names in order and is far from them
	asked := names[passNames:]
	a := e.adm.withToken(e.token)
	statuses := make([]int, len(asked))
	var wg sync.WaitGroup
	for i, name := range asked {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := a.send(http.MethodGet, "/api/npm/"+repoRef(key)+"/"+name+"/-/"+name+"-1.0.0.tgz", nil)
			if err == nil {
				statuses[i] = r.Status
			}
		}()
	}
	wg.Wait()
	var after float64
	waitFor(t, "trove_mirror_preempted_total to grow", 30*time.Second, time.Second, func() bool {
		after = preempted(t, key)
		return after > before
	})
	t.Logf("%d client fetches of tarballs the pass had not stored: trove_mirror_preempted_total %v -> %v", len(asked), before, after)
	for i, code := range statuses {
		if code != http.StatusOK {
			t.Errorf("R-REPO-055: the client fetch of %s answered %d, want 200", asked[i], code)
		}
	}
}

// needNPMMirror builds the mirror the tests after the clients-first one read: a run of one of them alone has none yet.
func (e *mirrorEnv) needNPMMirror(t *testing.T) string {
	t.Helper()
	e.mu.Lock()
	have := e.npmMirror != ""
	e.mu.Unlock()
	if !have {
		e.clientsFirstNPM(t)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.npmMirror
}

// TestNPMMirror_R_REPO_059_RestoreWithTheUpstreamStopped (E6b.3): after a complete pass of the mirror, the npm test upstream is scaled to
// zero and `npm install` of a mirrored version through the same remote succeeds with an empty client cache. A stub scaled to zero
// loses its dataset; the test restores it (seedNPM) when it ends.
func TestNPMMirror_R_REPO_059_RestoreWithTheUpstreamStopped(t *testing.T) {
	e := mirrorFx(t)
	key := e.needNPMMirror(t)
	markStubStopped("npmstub", true)
	stubScale(t, "npmstub", 0)
	t.Cleanup(func() {
		stubScale(t, "npmstub", 1)
		markStubStopped("npmstub", false)
		e.seedNPM(t)
	})
	for i, name := range e.extras[:3] {
		r, has := e.npmInstall(t, i, key, "stopped", name, "1.0.0")
		if r.Code != 0 || !has {
			t.Errorf("npm install %s@1.0.0 through %s with the upstream stopped: exit %d, installed %v:\n%s", name, key, r.Code, has, tailLines(r.Out, 20))
			continue
		}
		t.Logf("npm install %s@1.0.0 with the upstream stopped: %s", name, r.Dur.Round(time.Millisecond))
	}
	e.jobs.check(t)
}
