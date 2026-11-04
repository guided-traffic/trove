//go:build e2e

package e2e

// Cold cache, concurrent clients (exit criterion E3.2, R-NUGET-015): three clients run `dotnet restore --no-cache` at once,
// each against another packages replica, and restore the reference solution of perf/nuget/reference (at least 100 packages in
// its restore graph) through a virtual repository over the local repository and a fresh remote on the counting test
// upstream. All three succeed, and the upstream records one request per .nupkg: the three replicas collapse their misses
// through the flight lock (Valkey runs; the closing run repeats the restore with Valkey stopped, nuget_closing_test.go).

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// referenceSolution reads the committed files of perf/nuget/reference (make nuget-reference-check proves them equal to a
// regeneration from the corpus of the test upstream), without build output.
func referenceSolution(t testing.TB) map[string][]byte {
	t.Helper()
	root := filepath.Join(cfg.repo, "perf", "nuget", "reference")
	files := map[string][]byte{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if strings.HasPrefix(rel, "obj") || strings.HasPrefix(rel, "bin") {
			return nil
		}
		b, err := os.ReadFile(path)
		files[filepath.ToSlash(rel)] = b
		return err
	})
	if err != nil || len(files) < 3 {
		t.Fatalf("the reference solution in %s: %d files, %v", root, len(files), err)
	}
	return files
}

// restoredPackages counts the packages a client extracted into its NUGET_PACKAGES.
func (c *nugetClient) restoredPackages() int {
	c.t.Helper()
	out := c.exec("find", c.packages(), "-name", "*.nupkg")
	return len(strings.Fields(out))
}

// coldRestoreOfReference runs the restore of E3.2 on three clients pinned to three replicas against a fresh remote and
// virtual repository, and checks the counters of the upstream. It is the test of E3.2 and, with Valkey stopped, of
// R-NUGET-031 (at most one request per replica for each .nupkg).
func coldRestoreOfReference(t *testing.T, fx *nugetFixture, purpose string, perPath int) {
	t.Helper()
	s := nugetStub(t)
	repos := fx.upstreamRepos(t, purpose, nil)
	s.resetAll(t)
	defer s.resetAll(t)
	feed := nugetFeed(t, "localhost", repos.all)
	webs := packagesReplicas(t, 3)
	files := referenceSolution(t)
	clients := make([]*nugetClient, 3)
	for i := range clients {
		nugetPin(t, i, webs[i].IP)
		defer nugetUnpin(t, i)
		c := fx.client(t, i, purpose+"-reference")
		for rel, b := range files {
			c.write(rel, b)
		}
		c.config(feed, true) // replaces the nuget.config of the reference solution: its source is the repository of this test
		clients[i] = c
	}
	s.reset(t)
	results := runTogether(len(clients), func(i int) restoreResult {
		out, err := clients[i].try("dotnet", "restore", "Reference.sln", "--no-cache")
		return restoreResult{out: out, err: err}
	})
	for i, r := range results {
		if r.err != nil {
			t.Fatalf("client %d: dotnet restore failed: %v\n%s", i, r.err, tailLines(r.out, 25))
		}
	}
	n := clients[0].restoredPackages()
	if n < 100 {
		t.Fatalf("the restore graph has %d packages, E3.2 wants at least 100", n)
	}
	for i, c := range clients {
		if got := c.restoredPackages(); got != n {
			t.Fatalf("client %d extracted %d packages, client 0 %d", i, got, n)
		}
	}
	counts := s.nugetCounts(t)
	perNupkg := map[string]int{}
	for _, e := range counts.Entries {
		if e.Kind == "nupkg" {
			perNupkg[e.Path] += e.Count
		}
	}
	if len(perNupkg) != n {
		t.Fatalf("the upstream served %d distinct .nupkg files, the clients extracted %d packages: %s", len(perNupkg), n, counts.Summary())
	}
	for path, c := range perNupkg {
		if c > perPath {
			t.Fatalf("the upstream served %s %d times, want at most %d: %s", path, c, perPath, counts.Summary())
		}
	}
}

func TestNuGetCollapse(t *testing.T) {
	fx := nugetFx(t)
	t.Run("R-NUGET-015 R-NUGET-030 E3.2 three clients restore the reference solution cold at once through nuget-all, one upstream request per .nupkg", func(t *testing.T) {
		coldRestoreOfReference(t, fx, "e32", 1)
	})
}
