//go:build e2e

package e2e

// The additions of the full run of Test and Release on the commit that closes the phase (roadmap M3, E3.6, R-NUGET-027):
// R-NUGET-030 and R-NUGET-031 with Valkey stopped. The workflow starts them only when it is dispatched with closing=true;
// a run of them without TROVE_E2E_NUGET_CLOSING=1 is a failure, never a skip, so they cannot go missing from the closing
// run (deploy/dev/e2e.sh nuget-closing). The drain part of E3.6 moved to phase 7 (owner, 2026-11-01).

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/nugetstub"
)

func requireNuGetClosingRun(t testing.TB) {
	t.Helper()
	if os.Getenv("TROVE_E2E_NUGET_CLOSING") != "1" {
		t.Fatal("this test belongs to the closing run: start it with deploy/dev/e2e.sh nuget-closing (the closing=true dispatch of the workflow)")
	}
}

// pushRestoreLoop is the first criterion of R-NUGET-030: iterations of dotnet nuget push of a .nupkg with its .snupkg
// through a virtual repository, each followed at once by dotnet restore --no-http-cache of the pushed package through
// the same repository, on three clients at once (every request to the next replica). All succeed.
func pushRestoreLoop(t *testing.T, fx *nugetFixture, purpose string, perClient int) {
	t.Helper()
	repos := fx.upstreamRepos(t, purpose, nil)
	all := nugetFeed(t, "localhost", repos.all)
	clients := make([]*nugetClient, 3)
	for i := range clients {
		clients[i] = fx.client(t, i, purpose+"-loop")
		clients[i].config(all, true)
	}
	results := runTogether(len(clients), func(w int) restoreResult {
		c := clients[w]
		for k := range perClient {
			id := fx.pkgID(fmt.Sprintf("%s%d_%d", purpose, w, k))
			nupkg, snupkg := fx.nugetPackSymbols(t, w, id, "1.0.0", "push and restore loop")
			file := c.place(id, "1.0.0", nupkg)
			c.write(id+".1.0.0.snupkg", snupkg)
			out, err := c.dotnetPush(file, all, "az")
			if err == nil && len(pushedRE.FindAllString(out, -1)) < 2 {
				err = errors.New("the package and its symbol package were not both reported pushed")
			}
			if err != nil {
				return restoreResult{out: fmt.Sprintf("iteration %d: push of %s:\n%s", k, id, out), err: fmt.Errorf("push: %w", err)}
			}
			proj := c.consumer(fmt.Sprintf("consumer%d", k), id, "1.0.0") // a project of the package's own name would depend on itself
			out, err = c.try("dotnet", "restore", proj, "--no-http-cache")
			if err == nil && !c.hasPackage(id, "1.0.0") {
				err = errors.New("the restore extracted nothing")
			}
			if err != nil {
				return restoreResult{out: fmt.Sprintf("iteration %d: restore of %s:\n%s", k, id, out), err: fmt.Errorf("restore: %w", err)}
			}
		}
		return restoreResult{}
	})
	for w, r := range results {
		if r.err != nil {
			t.Fatalf("client %d: %v\n%s", w, r.err, tailLines(r.out, 25))
		}
	}
}

// concurrentColdRestores is the second criterion of R-NUGET-031: many concurrent restores of one uncached package on two
// replicas cause at most one upstream request per replica for its .nupkg when Valkey cannot collapse them. Three client
// pods, four scenarios each: the pods 0 and 2 are pinned to the first replica, the pod 1 to the second.
func concurrentColdRestores(t *testing.T, fx *nugetFixture, purpose string) {
	t.Helper()
	s := nugetStub(t)
	repos := fx.upstreamRepos(t, purpose, nil)
	s.resetAll(t)
	defer s.resetAll(t)
	feed := nugetFeed(t, "localhost", repos.all)
	webs := webReplicas(t, 3)
	for pod, web := range []pod{webs[0], webs[1], webs[0]} {
		nugetPin(t, pod, web.IP)
		defer nugetUnpin(t, pod)
	}
	const perPod = 4
	clients := make([]*nugetClient, 3*perPod)
	projects := make([]string, len(clients))
	for i := range clients {
		clients[i] = fx.client(t, i%3, fmt.Sprintf("%s-%d", purpose, i))
		clients[i].config(feed, true)
		projects[i] = clients[i].consumer("p", nugetstub.NewtonsoftID, "13.0.3")
	}
	s.reset(t)
	results := runTogether(len(clients), func(i int) restoreResult {
		out, err := clients[i].try("dotnet", "restore", projects[i], "--no-cache")
		return restoreResult{out: out, err: err}
	})
	for i, r := range results {
		if r.err != nil {
			t.Fatalf("restore %d failed: %v\n%s", i, r.err, tailLines(r.out, 15))
		}
	}
	if n := s.nugetCounts(t).Nupkg(nugetstub.NewtonsoftID, "13.0.3"); n < 1 || n > 2 {
		t.Fatalf("the upstream served the .nupkg %d times for %d concurrent restores on two replicas, want one or two", n, len(clients))
	}
}

func TestNuGetClosing(t *testing.T) {
	requireNuGetClosingRun(t)
	fx := nugetFx(t)

	// R-NUGET-016's claim criterion with Valkey stopped: within cluster.epoch_poll_interval plus 1 s (the bound allows one
	// more second, as E1.14 does); the case with Valkey is TestNuGetSearchClaimTiming of the set nuget.
	t.Run("R-NUGET-016 R-REPO-023 a claim removes the cached forwarded result on every replica with Valkey stopped within epoch_poll_interval plus 1 s", func(t *testing.T) {
		c := newClaimTiming(t, "claimstop")
		withValkeyStopped(t, func() {
			webReadyDegraded(t, true)
			// cached while Valkey is down, so the answer is still fresh (nuget.search.remote_result_ttl) when the claim lands
			c.cachedEverywhere("Valkey stopped")
			c.claimAndWait("Valkey stopped", 4*time.Second)
		})
		webReadyDegraded(t, false)
	})

	t.Run("R-NUGET-030 R-NUGET-031 E3.6 the documents are the same on every replica with Valkey stopped", func(t *testing.T) {
		withValkeyStopped(t, func() {
			webReadyDegraded(t, true)
			fx.replicaScenario(t, "ReplicasValkeyStopped")
		})
		webReadyDegraded(t, false)
	})

	// 51 iterations: 17 on each of the three clients
	t.Run("R-NUGET-030 E3.6 push with symbols and restore, 51 iterations through nuget-all, Valkey running", func(t *testing.T) {
		pushRestoreLoop(t, fx, "loopup", 17)
	})
	t.Run("R-NUGET-030 R-NUGET-031 E3.6 push with symbols and restore, 51 iterations through nuget-all, Valkey stopped", func(t *testing.T) {
		withValkeyStopped(t, func() {
			webReadyDegraded(t, true)
			pushRestoreLoop(t, fx, "loopdown", 17)
		})
		webReadyDegraded(t, false)
	})

	t.Run("R-NUGET-015 R-NUGET-031 E3.2 the cold restore of the reference solution with Valkey stopped asks the upstream at most once per replica for each .nupkg", func(t *testing.T) {
		withValkeyStopped(t, func() {
			webReadyDegraded(t, true)
			coldRestoreOfReference(t, fx, "e32down", 3)
		})
		webReadyDegraded(t, false)
	})
	t.Run("R-NUGET-031 concurrent restores of one uncached package on two replicas with Valkey stopped cost at most two upstream requests", func(t *testing.T) {
		withValkeyStopped(t, func() {
			webReadyDegraded(t, true)
			concurrentColdRestores(t, fx, "concdown")
		})
		webReadyDegraded(t, false)
	})
}
