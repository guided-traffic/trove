//go:build e2e

package e2e

// The NuGet acceptance tests of a remote repository (R-NUGET-014 to R-NUGET-017, exit criterion E3.1, Linux part): the
// real clients restore and install through a remote repository of the counting test upstream (deploy/dev/nugetstub.yaml,
// test/nugetstub). The upstream names two hosts in its service index, as nuget.org does; the repository admits the second
// through extraHosts. What the upstream saw is the evidence: the first request fills the cache, the second is served from it.
// Every test makes the repositories it needs (upstreamRepos) and removes them again, so the cache starts empty.

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/nugetstub"
)

const (
	// nugetDepRoot 1.0.0 depends on nugetDepLeaf 1.0.0 (the generated corpus of test/nugetstub: the second package
	// of its order depends on the first).
	nugetDepRoot = "Contoso.Core.Extensions"
	nugetDepLeaf = "Contoso.Core.Abstractions"
)

func TestNuGetRemote(t *testing.T) {
	fx := nugetFx(t)
	s := nugetStub(t)
	repos := fx.upstreamRepos(t, "remote", map[string]any{"availability": map[string]any{"mode": "online", "failureThreshold": 3, "circuitOpenSeconds": 5}})
	s.resetAll(t)
	defer s.resetAll(t)
	feed := nugetFeed(t, "localhost", repos.remote)
	apiHost, contentHost := nugetStubHosts(t)

	t.Run("R-NUGET-014 R-NUGET-015 R-NUGET-027 E3.1 dotnet restore through a remote fills the cache, the second restore is served from it", func(t *testing.T) {
		s.reset(t)
		first := fx.client(t, 0, "remote-fill")
		first.config(feed, true)
		first.exec("dotnet", "restore", first.consumer("p", nugetDepRoot, "1.0.0"), "--no-cache")
		for _, id := range []string{nugetDepRoot, nugetDepLeaf} {
			if !first.hasPackage(id, "1.0.0") {
				t.Fatalf("the restore did not extract %s 1.0.0", id)
			}
		}
		c := s.nugetCounts(t)
		if c.Nupkg(nugetDepRoot, "1.0.0") != 1 || c.Nupkg(nugetDepLeaf, "1.0.0") != 1 {
			t.Fatalf("the upstream should have served each .nupkg once for the first restore: %s", c.Summary())
		}
		// the service index spreads the resources over two hosts: the restore reached both through the repository
		for _, h := range []string{apiHost, contentHost} {
			if !slices.ContainsFunc(c.Hosts(), func(seen string) bool { return strings.HasPrefix(seen, h) }) {
				t.Fatalf("the upstream saw no request on %s, only %v", h, c.Hosts())
			}
		}
		// a second client with caches of its own, on another replica: nothing of the .nupkg files goes upstream again
		second := fx.client(t, 1, "remote-hit")
		second.config(feed, true)
		second.exec("dotnet", "restore", second.consumer("p", nugetDepRoot, "1.0.0"), "--no-cache")
		if !second.hasPackage(nugetDepRoot, "1.0.0") || !second.hasPackage(nugetDepLeaf, "1.0.0") {
			t.Fatal("the second restore did not extract the packages")
		}
		if again := s.nugetCounts(t); again.Nupkg(nugetDepRoot, "1.0.0") != 1 || again.Nupkg(nugetDepLeaf, "1.0.0") != 1 {
			t.Fatalf("the second restore reached the upstream for a .nupkg again: %s", again.Summary())
		}
	})

	t.Run("R-NUGET-014 R-NUGET-027 nuget install through a remote downloads the package from the upstream once", func(t *testing.T) {
		s.reset(t)
		c := fx.client(t, 2, "remote-exe")
		c.config(feed, true)
		c.exec("nuget", "install", nugetstub.NewtonsoftID, "-Version", "13.0.1", "-Source", feed, "-ConfigFile", "nuget.config",
			"-OutputDirectory", "installed", "-NonInteractive", "-DirectDownload", "-NoCache")
		if _, err := c.try("test", "-f", c.dir()+"/installed/Newtonsoft.Json.13.0.1/Newtonsoft.Json.13.0.1.nupkg"); err != nil {
			t.Fatal("nuget install did not extract the package")
		}
		if n := s.nugetCounts(t).Nupkg(nugetstub.NewtonsoftID, "13.0.1"); n != 1 {
			t.Fatalf("the upstream served the .nupkg %d times, want once", n)
		}
	})

	// R-NUGET-015: a package the repository has cached restores while the upstream is down; one it has not fails, and
	// restores once the upstream is back (the circuit of the remote opens after failures and closes again).
	t.Run("R-NUGET-015 R-NUGET-017 a down upstream: cached packages still restore, uncached ones fail and restore again after recovery", func(t *testing.T) {
		s.nugetMode(t, nugetstub.Mode{Down: nugetstub.DownUnavailable})
		defer s.nugetMode(t, nugetstub.Mode{})
		cached := fx.client(t, 0, "remote-down-cached")
		cached.config(feed, true)
		cached.exec("dotnet", "restore", cached.consumer("p", nugetDepRoot, "1.0.0"), "--no-cache")
		if !cached.hasPackage(nugetDepRoot, "1.0.0") {
			t.Fatal("the cached package did not restore while the upstream was down")
		}
		miss := fx.client(t, 1, "remote-down-miss")
		miss.config(feed, true)
		proj := miss.consumer("p", nugetstub.UtilID, "2.0.0")
		if out, err := miss.try("dotnet", "restore", proj, "--no-cache"); err == nil || miss.hasPackage(nugetstub.UtilID, "2.0.0") {
			t.Fatalf("the restore of an uncached package succeeded while the upstream was down:\n%s", out)
		}
		s.nugetMode(t, nugetstub.Mode{})
		waitFor(t, "the uncached package to restore after the upstream is back", 2*time.Minute, 5*time.Second, func() bool {
			out, err := miss.try("dotnet", "restore", proj, "--no-cache")
			if err != nil {
				t.Logf("restore after the recovery: %v\n%s", err, tailLines(out, 12))
			}
			return err == nil && miss.hasPackage(nugetstub.UtilID, "2.0.0")
		})
	})
}
