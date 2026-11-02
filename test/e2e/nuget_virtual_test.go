//go:build e2e

package e2e

// The NuGet acceptance tests of a virtual repository (R-NUGET-019, R-NUGET-010, R-NUGET-018, R-NUGET-030): the real
// clients push through nuget-e2e's virtual repository into its deployment target, restore local packages and remote ones that
// the repository has not cached yet, and search; the documents are the same on every replica. The virtual repository of the
// test has the local repository nuget-e2e and a remote over the counting test upstream (upstreamRepos) as members.

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/nugetstub"
)

func TestNuGetVirtual(t *testing.T) {
	fx := nugetFx(t)
	s := nugetStub(t)
	repos := fx.upstreamRepos(t, "virtual", nil)
	s.resetAll(t)
	defer s.resetAll(t)
	all := nugetFeed(t, "localhost", repos.all)
	id := fx.pkgID("Virtual")

	t.Run("R-NUGET-019 R-NUGET-006 dotnet nuget push through the virtual repository lands in the deployment target", func(t *testing.T) {
		c := fx.client(t, 0, "virtual-push")
		c.config(all, true)
		file := c.place(id, "1.0.0", fx.nugetPack(t, id, "1.0.0", "pushed through the virtual repository"))
		c.mustPush("push through nuget-all", "dotnet", "nuget", "push", file, "--source", all, "--api-key", "az")
		for _, repo := range []string{nugetAuthKey, repos.all} {
			if got := fx.flatVersions(t, repo, id); len(got) != 1 || got[0] != "1.0.0" {
				t.Fatalf("%s lists %v, want [1.0.0]", repo, got)
			}
		}
		lower := strings.ToLower(id)
		fx.adm.get("/files/"+nugetAuthKey+"/"+lower+"/1.0.0/"+lower+".1.0.0.nupkg").want(t, "the stored .nupkg in the deployment target", 200)
	})

	t.Run("R-NUGET-019 R-NUGET-027 nuget push through the virtual repository, and the replay of the identical file", func(t *testing.T) {
		c := fx.client(t, 1, "virtual-exe-push")
		c.config(all, true)
		file := c.place(id, "1.1.0", fx.nugetPack(t, id, "1.1.0", "pushed by nuget.exe through the virtual repository"))
		for range 2 {
			c.mustPush("nuget.exe push through nuget-all", "nuget", "push", file, "-Source", all, "-ApiKey", fx.token, "-ConfigFile", "nuget.config", "-NonInteractive")
		}
		if got := fx.flatVersions(t, nugetAuthKey, id); len(got) != 2 {
			t.Fatalf("the deployment target lists %v, want both versions", got)
		}
	})

	t.Run("R-NUGET-018 R-NUGET-027 dotnet restore through the virtual repository: a local package and a remote one that is not cached yet", func(t *testing.T) {
		s.reset(t)
		c := fx.client(t, 2, "virtual-restore")
		c.config(all, true)
		c.exec("dotnet", "restore", c.consumer("local", id, "1.*"), "--no-cache")
		if !c.hasPackage(id, "1.1.0") {
			t.Fatal("the floating version 1.* of the local package did not resolve to 1.1.0")
		}
		c.exec("dotnet", "restore", c.consumer("remote", nugetDepRoot, "1.0.0"), "--no-cache")
		if !c.hasPackage(nugetDepRoot, "1.0.0") || !c.hasPackage(nugetDepLeaf, "1.0.0") {
			t.Fatal("the restore of the remote package and its dependency did not extract them")
		}
		if n := s.nugetCounts(t).Nupkg(nugetDepRoot, "1.0.0"); n != 1 {
			t.Fatalf("the upstream served %s %d times, want once", nugetDepRoot, n)
		}
	})

	t.Run("R-NUGET-018 R-NUGET-027 nuget install through the virtual repository: a remote package and a local one", func(t *testing.T) {
		c := fx.client(t, 0, "virtual-install")
		c.config(all, true)
		for _, p := range [][2]string{{nugetstub.NewtonsoftID, "13.0.3"}, {id, "1.0.0"}} {
			c.exec("nuget", "install", p[0], "-Version", p[1], "-Source", all, "-ConfigFile", "nuget.config",
				"-OutputDirectory", "installed", "-NonInteractive", "-DirectDownload", "-NoCache")
			if _, err := c.try("test", "-f", c.dir()+"/installed/"+p[0]+"."+p[1]+"/"+p[0]+"."+p[1]+".nupkg"); err != nil {
				t.Fatalf("nuget install of %s %s did not extract the package", p[0], p[1])
			}
		}
	})

	// R-NUGET-010: the search through the virtual repository lists the local package. The forwarded search of a remote
	// (R-NUGET-016, Q-NUGET-8) is not built (Q-NUGET-55): a remote answers from the rows of its cache, and a virtual
	// repository merges no remote member's hits yet (searchRemote contributes nothing), so the search of nuget-all does
	// not list a remote package. The test pins the two parts that exist and leaves the missing one to its package.
	t.Run("R-NUGET-010 R-NUGET-018 search lists the local package through the virtual repository and the cached remote package through the remote", func(t *testing.T) {
		c := fx.client(t, 1, "virtual-search")
		c.config(all, true)
		out := c.exec("dotnet", "package", "search", id, "--source", all, "--exact-match")
		if !strings.Contains(out, id) || !strings.Contains(out, "1.1.0") {
			t.Fatalf("search for the local package through the virtual repository:\n%s", out)
		}
		// the rows of a cached remote package are written after the fill, not by it
		path := "/api/nuget/" + repos.remote + "/v3/search?take=50&q=" + strings.ToLower(nugetDepLeaf)
		waitFor(t, "the search of the remote to list its cached package", time.Minute, time.Second, func() bool {
			r := newAPI(t).withToken(fx.token).get(path)
			return r.Status == 200 && strings.Contains(strings.ToLower(string(r.Body)), strings.ToLower(nugetDepLeaf))
		})
	})

	t.Run("R-NUGET-030 E3.6 the documents of the virtual repository are the same on every replica", func(t *testing.T) {
		fx.documentsOnEveryReplica(t, repos.all, id, "1.1.0")
		fx.documentsOnEveryReplica(t, repos.all, nugetDepRoot, "1.0.0")
	})

	// R-NUGET-019: without a deployment target the service index lists no publish resource and a push is refused with 403.
	t.Run("R-NUGET-019 without a deployment target the index lists no publish resource and a push answers 403", func(t *testing.T) {
		repoPath := "/api/v1/repositories/" + repos.all
		g := fx.adm.get(repoPath).want(t, "read the virtual repository", 200)
		patch := map[string]any{"virtual": map[string]any{"deployTarget": nil}}
		fx.adm.do(http.MethodPatch, repoPath, patch, "Content-Type", "application/merge-patch+json",
			"If-Match", g.Header.Get("ETag")).want(t, "remove the deployment target", 200)
		indexPath := "/api/nuget/" + repos.all + "/v3/index.json"
		waitFor(t, "every replica to list no publish resource", 30*time.Second, time.Second, func() bool {
			seen := map[string]bool{}
			for range 9 {
				r := fx.adm.get(indexPath)
				if r.Status != 200 || strings.Contains(string(r.Body), "PackagePublish") {
					return false
				}
				seen[r.Replica()] = true
			}
			return len(seen) >= 3
		})
		c := fx.client(t, 2, "virtual-no-target")
		c.config(all, true)
		other := fx.pkgID("VirtualNoTarget")
		file := c.place(other, "1.0.0", fx.nugetPack(t, other, "1.0.0", "never stored"))
		// the index lists no publish resource, so dotnet stops at the client; the API key route itself answers the 403
		fx.adm.do(http.MethodPut, "/api/nuget/"+repos.all+"/v3/package", "x", "X-NuGet-ApiKey", fx.token).want(t, "a push without a deployment target", 403)
		if out, err := c.dotnetPush(file, all, "az"); err == nil || pushedRE.MatchString(out) {
			t.Fatalf("dotnet nuget push to a virtual repository without a target succeeded:\n%s", out)
		}
		if got := fx.adm.get("/api/nuget/" + nugetAuthKey + "/v3/flatcontainer/" + strings.ToLower(other) + "/index.json"); got.Status != 404 {
			t.Fatalf("the refused package is visible: %s", got)
		}
	})
}
