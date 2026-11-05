//go:build e2e

package e2e

// R-REPO-055 on kind (set `mirror`, R-REPO-059): a pass runs while `npm ci` and `dotnet restore` run through the same remotes on the
// three client-serving replicas, the pass in the pool of two processes that claim the queue `mirror` alone.

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/npmstub"
	"github.com/guided-traffic/trove/test/nugetstub"
)

// installBudget is the factor of R-REPO-055: the install through a remote that a pass fills takes at most this much of its time
// without the pass.
const installBudget = 1.2

// TestMirror_R_REPO_055_ClientsFirst is the e2e criterion of R-REPO-055 and E6b.6, for npm and for NuGet.
//
// npm: the remote npm-mirror-<run> is a mirror of the npm test upstream under the public registry's name (the feed lists the 700
// packages of its corpus and the 300 extra ones the test published); maxConcurrentFetches is 8 and the mirror's concurrency 4, so
// B = 2 and the bound is 8 + 3 - 1 + 2 = 12. The baseline is `npm ci` of the reference project (707 locked packages) through a
// second remote of the same settings whose mirror is paused, so that its cache is as cold as the first one's and only the pass
// differs; the run with the pass starts once the pass has filled packages. The test upstream holds every tarball and document for
// 120 ms and 20 ms, so that fetches overlap.
func TestMirror_R_REPO_055_ClientsFirst(t *testing.T) {
	e := mirrorFx(t)
	t.Run("npm", func(t *testing.T) { e.clientsFirstNPM(t) })
	t.Run("nuget", func(t *testing.T) { e.clientsFirstNuGet(t) })
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

	var bases, loads []time.Duration
	var key string
	// A client fetch pre-empts the background fetches that are running when it has to wait, and whether the pass has one in flight at that
	// moment depends on where it is in its cycle (one pair of the dozen that were measured showed none), so the counter is judged over the pairs.
	var preemptions float64
	maxOpen := 0
	for i := range clientsFirstPairs {
		last := i == clientsFirstPairs-1
		// the baseline: the mirror of the idle remote is paused, so no pass runs; both remotes start with an empty cache
		idle := e.create(t, e.npmBody(fmt.Sprintf("npm-mirror-idle%d-%s", i, e.run), true))
		base, n := e.npmCI(t, 0, idle, fmt.Sprintf("base%d", i))
		if base.Code != 0 {
			t.Fatalf("the baseline npm ci through %s exited %d:\n%s", idle, base.Code, tailLines(base.Out, 30))
		}
		bases = append(bases, base.Dur)

		// the run with the pass: requested now, and it has filled packages before the install starts
		key = e.create(t, e.npmBody(fmt.Sprintf("npm-mirror%d-%s", i, e.run), false))
		s.reset(t) // the counters and the maximum of open requests count from the pass's start
		e.run_(t, key, false)
		st := e.awaitPassStarted(t, key, 5)
		if st.Pass.Method != "feed" {
			t.Errorf("the pass uses the method %q, want feed", st.Pass.Method)
		}
		pod, node := poolPodOfPass(t, st.Pass.Job.ID)
		if !strings.HasPrefix(pod, "trove-worker-"+mirrorPoolName+"-") {
			t.Errorf("the pass runs in %q (node %s), want a process of the pool %s", pod, node, mirrorPoolName)
		}
		before := preempted(t, key)
		loaded, n2 := e.npmCI(t, 0, key, fmt.Sprintf("load%d", i))
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
			e.drop(t, key, idle)
			e.awaitNoPassRunning(t)
		}
	}
	bestBase, medLoad := slices.Min(bases), median(loads)
	ratio := float64(medLoad) / float64(bestBase)
	t.Logf("npm ci without a pass: %v, with a pass: %v; best without %s, median with %s: %.0f %% (budget %.0f %%)", bases, loads, bestBase.Round(time.Millisecond), medLoad.Round(time.Millisecond), ratio*100, installBudget*100)
	if preemptions == 0 {
		t.Errorf("R-REPO-055: trove_mirror_preempted_total did not grow in any of the %d pairs: no client fetch took a background slot", clientsFirstPairs)
	}
	if ratio > installBudget {
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

// awaitNoPassRunning waits until no job mirror-pass is queued or running: the pass of a repository that was deleted has halted.
func (e *mirrorEnv) awaitNoPassRunning(t *testing.T) {
	t.Helper()
	waitFor(t, "the pass of the deleted mirror to halt", 3*time.Minute, 2*time.Second, func() bool {
		return sql(t, `SELECT count(*) FROM trove.job WHERE kind = 'mirror-pass' AND state IN ('queued', 'running', 'cancelling')`) == "0"
	})
}

func (e *mirrorEnv) clientsFirstNuGet(t *testing.T) {
	s := nugetStub(t)
	b := mirrorShare(mirrorConcurrency, mirrorMaxFetches)
	bound := mirrorBound(mirrorMaxFetches, mirrorServing, b)
	s.nugetMode(t, nugetstub.Mode{Catalog: true, DelayMS: map[string]int{"nupkg": 100}})
	t.Cleanup(func() { s.nugetMode(t, nugetstub.Mode{}) })

	key := e.create(t, e.nugetBody(t, "nuget-mirror-"+e.run))
	e.run_(t, key, false)
	st := e.awaitPassStarted(t, key, 5)
	t.Logf("the pass runs (%s, %d names so far, %d packages filled) when dotnet restore starts", st.Pass.Method, st.Pass.NamesDiscovered, st.Pass.PackagesDone)
	if st.Pass.Method != "feed" {
		t.Errorf("the pass uses the method %q, want feed (the catalog)", st.Pass.Method)
	}
	s.reset(t)
	before := preempted(t, key)
	start := time.Now()
	out, n, err := e.restoreReference(t, 0, key, "load")
	took := time.Since(start)
	after := preempted(t, key)
	open := nugetInflight(t)
	t.Logf("dotnet restore of %d packages with the pass: %s; the test upstream held at most %d requests open (bound %d); trove_mirror_preempted_total %v -> %v",
		n, took.Round(time.Millisecond), open.Max, bound, before, after)
	if err != nil {
		t.Fatalf("dotnet restore through %s with a pass running: %v\n%s", key, err, tailLines(out, 25))
	}
	if n < 100 {
		t.Errorf("the restore graph has %d packages, want at least 100", n)
	}
	if open.Max > bound {
		t.Errorf("R-REPO-055: the NuGet test upstream held %d requests open at once, want at most %d", open.Max, bound)
	}
	done := e.awaitComplete(t, key, 20*time.Minute)
	sum := done.LastCompletePass
	t.Logf("the pass ended %s: %d packages (%d failed), %d items, %d bytes, method %s", sum.Outcome, sum.Packages, sum.PackagesFailed, sum.ItemsStored, sum.BytesStored, sum.Method)
	if sum.Outcome != "complete" || sum.PackagesFailed != 0 || sum.Packages < 100 {
		t.Errorf("the pass ended %+v, want complete with no failed package", *sum)
	}
	e.mu.Lock()
	e.nugetMirror = key
	e.mu.Unlock()
}
