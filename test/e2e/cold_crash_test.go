//go:build e2e

package e2e

// A packages replica killed during a cold install (exit criterion E5.2, R-HA-052's pod crash with a cold cache): three clients
// install the reference project of their format through a virtual repository over a fresh remote on the counting test
// upstream, so that every package is a miss that a replica fetches and stores, and one replica is force-deleted while they
// run. The clients go through the edge, which hands each request to another replica (the scatter profile), so the dead
// replica is a victim of every client. The installs must exit 0 with the clients' own retries, and
// `trove admin storage verify` (run in a pod of the installation, as an operator would) must report no item without its
// object. The kill happens once the upstream has served a share of the packages and before it has served them all: a precondition
// the test proves, not hopes for; an upstream that answers with a delay keeps the install long enough for it.

import (
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/npmstub"
	"github.com/guided-traffic/trove/test/nugetstub"
)

const (
	// coldCrashNPMDelay and coldCrashNuGetDelay slow the tarballs and .nupkg files of the upstream down so that the three
	// clients are still installing when the replica dies.
	coldCrashNPMDelay   = 300 // ms per tarball
	coldCrashNuGetDelay = 400 // ms per .nupkg
	// coldCrashNPMKillAfter and coldCrashNuGetKillAfter are the number of packages the upstream has been asked for when the replica is deleted.
	coldCrashNPMKillAfter   = 100
	coldCrashNuGetKillAfter = 20
)

// requireStorageVerified runs `trove admin storage verify` in a pod of the installation and requires that it finds every
// object (R-HA-024): it exits 0, and its last line says that no blob is without its object.
func requireStorageVerified(t testing.TB) {
	t.Helper()
	out, err := troveAdmin(t, "", "storage", "verify")
	t.Logf("trove admin storage verify:\n%s", out)
	if err != nil {
		t.Fatalf("R-HA-024: trove admin storage verify did not exit 0: %v", err)
	}
	last := out
	if i := strings.LastIndex(strings.TrimSpace(out), "\n"); i >= 0 {
		last = strings.TrimSpace(out)[i+1:]
	}
	if !strings.Contains(last, " 0 without object, 0 of another size, 0 not checked, 0 items affected") {
		t.Fatalf("R-HA-024: the last line of trove admin storage verify is %q, want no blob without its object", last)
	}
}

// killOnePackagesPod force-deletes a random ready packages pod and returns it.
func killOnePackagesPod(t *testing.T) pod {
	t.Helper()
	var ready []pod
	for _, p := range livePods(t, selPackages) {
		if p.Ready {
			ready = append(ready, p)
		}
	}
	if len(ready) < 2 {
		t.Fatalf("%d ready packages pods, the test needs at least two to kill one", len(ready))
	}
	seed := uint64(time.Now().UnixNano())
	v := ready[rand.New(rand.NewPCG(seed, seed>>1)).IntN(len(ready))]
	t.Logf("force-deleting %s on %s (seed %d)", v.Name, v.Node, seed)
	forceDelete(t, v)
	return v
}

// untilServed polls count until it reaches n, and fails when every client has finished before: the kill must meet running
// installs. It returns the count read at the moment it reached n.
func untilServed(t *testing.T, what string, n int, finished func() bool, count func() int) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Minute)
	for {
		c := count()
		if c >= n {
			return c
		}
		if finished() {
			t.Fatalf("every client finished its install with only %d %s requested of the upstream, before the %d at which the replica is deleted: the delay of the upstream does not keep the install running", c, what, n)
		}
		if time.Now().After(deadline) {
			t.Fatalf("the upstream was asked for only %d %s in 10 minutes, the replica is deleted at %d", c, what, n)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func TestColdCrash(t *testing.T) {
	t.Run("R-HA-052 R-HA-024 R-NPM-045 R-REPO-026 E5.2 npm ci of the reference lockfile through npm-all, cold, on three clients while a packages replica is force-deleted: every npm ci exits 0, storage verify finds every object", func(t *testing.T) {
		fx := npmFx(t)
		requireScatter(t, fx)
		troveSettled(t, 3, 3, 4)
		repos := fx.upstreamRepos(t, "e52", nil)
		s := stub(t)
		s.resetAll(t)
		s.setMode(t, npmstub.Mode{DelayMS: map[string]int{"tarball": coldCrashNPMDelay}})
		defer s.resetAll(t)
		pkgJSON, lock, packages := referenceLockfile(t, repos.all)
		if packages < 500 {
			t.Fatalf("the reference lockfile resolves %d packages, E5.2 needs at least 500", packages)
		}
		const clientCount = 3
		clients := make([]*npmClient, clientCount)
		dirs := make([]string, clientCount)
		for i := range clients {
			c := newNPMClient(t, fx, "npm11", i)
			c.registryKey = repos.all
			c.tag += "-e52"
			clients[i] = c
			dirs[i] = c.project("cold-crash-ci", nil)
			c.configure(dirs[i])
			c.write(dirs[i]+"/package.json", pkgJSON)
			c.write(dirs[i]+"/package-lock.json", lock)
			c.sh(dirs[i], `rm -rf "`+c.home()+`/npm-cache" node_modules`)
		}
		s.reset(t)

		results := make([]npmRun, clientCount)
		done := make(chan int, clientCount)
		for i := range clients {
			go func() {
				results[i] = clients[i].exec(dirs[i], nil, "npm11", "ci")
				done <- i
			}()
		}
		finished := 0
		tarballs := func() int {
			n := 0
			for _, e := range s.counts(t).Entries {
				if e.Kind == npmstub.KindTarball {
					n += e.Count
				}
			}
			return n
		}
		served := untilServed(t, "tarballs", coldCrashNPMKillAfter, func() bool {
			for {
				select {
				case <-done:
					finished++
				default:
					return finished == clientCount
				}
			}
		}, tarballs)
		victim := killOnePackagesPod(t)
		t.Logf("deleted %s after the upstream had served %d of %d tarballs, %d of %d npm ci still running", victim.Name, served, packages, clientCount-finished, clientCount)
		if finished > 0 {
			t.Errorf("%d npm ci finished before the replica was deleted", finished)
		}
		for ; finished < clientCount; finished++ {
			<-done
		}
		for i, r := range results {
			t.Logf("client %d: npm ci exited %d after %s", i, r.Code, r.Dur.Round(time.Millisecond))
			if r.Code != 0 {
				t.Errorf("client %d: npm ci exited %d after %s:\n%s", i, r.Code, r.Dur.Round(time.Millisecond), tailLines(r.Out, 25))
			}
		}
		t.Logf("test upstream: %s", s.counts(t).Summary())
		troveSettled(t, 3, 3, 4)
		requireStorageVerified(t)
	})

	t.Run("R-HA-052 R-HA-024 R-NUGET-015 R-NUGET-030 E5.2 dotnet restore --no-cache of the reference solution through nuget-all, cold, on three clients while a packages replica is force-deleted: every restore exits 0, storage verify finds every object", func(t *testing.T) {
		fx := nugetFx(t)
		troveSettled(t, 3, 3, 4)
		s := nugetStub(t)
		repos := fx.upstreamRepos(t, "e52", nil)
		s.resetAll(t)
		s.nugetMode(t, nugetstub.Mode{DelayMS: map[string]int{nugetstub.ClassNupkg: coldCrashNuGetDelay}})
		defer s.resetAll(t)
		feed := nugetFeed(t, "localhost", repos.all)
		files := referenceSolution(t)
		const clientCount = 3
		clients := make([]*nugetClient, clientCount)
		for i := range clients {
			nugetUnpin(t, i) // the clients go through the edge: a pinned one would be bound to the replica that dies
			c := fx.client(t, i, "e52-reference")
			for rel, b := range files {
				c.write(rel, b)
			}
			c.config(feed, true)
			clients[i] = c
		}
		s.reset(t)

		results := make([]restoreResult, clientCount)
		durations := make([]time.Duration, clientCount)
		done := make(chan int, clientCount)
		for i := range clients {
			go func() {
				start := time.Now()
				out, err := clients[i].try("dotnet", "restore", "Reference.sln", "--no-cache")
				results[i], durations[i] = restoreResult{out: out, err: err}, time.Since(start)
				done <- i
			}()
		}
		finished := 0
		nupkgs := func() int {
			n := 0
			for _, e := range s.nugetCounts(t).Entries {
				if e.Kind == nugetstub.KindNupkg {
					n += e.Count
				}
			}
			return n
		}
		served := untilServed(t, ".nupkg files", coldCrashNuGetKillAfter, func() bool {
			for {
				select {
				case <-done:
					finished++
				default:
					return finished == clientCount
				}
			}
		}, nupkgs)
		victim := killOnePackagesPod(t)
		t.Logf("deleted %s after the upstream had served %d .nupkg files, %d of %d restores still running", victim.Name, served, clientCount-finished, clientCount)
		if finished > 0 {
			t.Errorf("%d restores finished before the replica was deleted", finished)
		}
		for ; finished < clientCount; finished++ {
			<-done
		}
		for i, r := range results {
			t.Logf("client %d: dotnet restore finished after %s, error %v", i, durations[i].Round(time.Millisecond), r.err)
			if r.err != nil {
				t.Errorf("client %d: dotnet restore failed: %v\n%s", i, r.err, tailLines(r.out, 25))
			}
		}
		n := clients[0].restoredPackages()
		if n < 100 {
			t.Errorf("the restore graph has %d packages, the reference solution has at least 100", n)
		}
		for i, c := range clients {
			if got := c.restoredPackages(); got != n {
				t.Errorf("client %d extracted %d packages, client 0 %d", i, got, n)
			}
		}
		t.Logf("test upstream: %s", s.nugetCounts(t).Summary())
		troveSettled(t, 3, 3, 4)
		requireStorageVerified(t)
	})
}
