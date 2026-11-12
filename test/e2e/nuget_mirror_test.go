//go:build e2e

package e2e

// The NuGet side of the remote full mirror on kind (R-REPO-055, R-REPO-059; set `nuget`): a pass runs while `dotnet restore` runs through
// the same remote on the three client-serving replicas, the pass in the pool of two processes that claim the queue `mirror` alone, and a
// client restores what the mirror stored while the test upstream is stopped. The npm side is npm_mirror_test.go.

import (
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/nugetstub"
)

// TestNuGetMirror_R_REPO_055_ClientsFirst is the e2e criterion of R-REPO-055 and E6b.6 for NuGet: the mirror of the NuGet test upstream (the
// catalog, maxConcurrentFetches 8, the mirror's concurrency 4, so the bound of open requests is 12) fills while `dotnet restore` runs through
// the same remote; the restore succeeds, the upstream never holds more requests open than the bound, and the pass ends complete.
func TestNuGetMirror_R_REPO_055_ClientsFirst(t *testing.T) {
	e := mirrorFx(t)
	e.clientsFirstNuGet(t)
	e.jobs.check(t)
}

func (e *mirrorEnv) clientsFirstNuGet(t *testing.T) {
	s := nugetStub(t)
	b := mirrorShare(mirrorConcurrency, mirrorMaxFetches)
	bound := mirrorBound(mirrorMaxFetches, mirrorServing, b)
	s.nugetMode(t, nugetstub.Mode{Catalog: true, DelayMS: map[string]int{"nupkg": 100}})
	t.Cleanup(func() { s.nugetMode(t, nugetstub.Mode{}) })

	key := e.create(t, e.nugetBody(t, "nuget-mirror-"+e.run))
	t.Cleanup(func() {
		// A test that stops before it hands the mirror over leaves none for the tests after it, and needNuGetMirror builds one
		// again under the same key: a repository left behind would refuse that with 409 and fail them for this test's cause.
		e.mu.Lock()
		handed := e.nugetMirror == key
		e.mu.Unlock()
		if !handed {
			e.drop(t, key)
		}
	})
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

// needNuGetMirror builds the mirror the tests after the clients-first one read: a run of one of them alone has none yet.
func (e *mirrorEnv) needNuGetMirror(t *testing.T) string {
	t.Helper()
	e.mu.Lock()
	have := e.nugetMirror != ""
	e.mu.Unlock()
	if !have {
		e.clientsFirstNuGet(t)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.nugetMirror
}

// TestNuGetMirror_R_REPO_059_RestoreWithTheUpstreamStopped (E6b.3): after a complete pass of the mirror, the NuGet test upstream is scaled to
// zero and `dotnet restore` of a mirrored version through the same remote succeeds with an empty client cache.
func TestNuGetMirror_R_REPO_059_RestoreWithTheUpstreamStopped(t *testing.T) {
	e := mirrorFx(t)
	key := e.needNuGetMirror(t)
	markStubStopped("nugetstub", true)
	stubScale(t, "nugetstub", 0)
	t.Cleanup(func() {
		stubScale(t, "nugetstub", 1)
		markStubStopped("nugetstub", false)
	})
	c := e.nuget.client(t, 0, "mirror-stopped")
	const id, version = "Newtonsoft.Json", "13.0.3" // the newest version of the ID in the test upstream's corpus
	c.config(nugetFeed(t, "localhost", key), true)
	file := c.consumer("consumer", id, version)
	start := time.Now()
	out, err := c.try("dotnet", "restore", file, "--no-cache")
	if err != nil || !c.hasPackage(id, version) {
		t.Fatalf("dotnet restore of %s %s through %s with the upstream stopped: %v\n%s", id, version, key, err, tailLines(out, 25))
	}
	t.Logf("dotnet restore of %s %s with the upstream stopped: %s", id, version, time.Since(start).Round(time.Millisecond))
	if strings.Contains(out, "warn") {
		t.Logf("restore output: %s", tailLines(out, 8))
	}
	e.jobs.check(t)
}
