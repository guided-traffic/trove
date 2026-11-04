//go:build e2e

package e2e

// The NuGet acceptance tests of the local repositories (R-NUGET-027, exit criteria E3.1 Linux part, E3.5, E3.6): the real
// clients of nuget_clients_test.go (the .NET 10 SDK and nuget.exe on Mono) against trove in the scatter profile, so that
// consecutive requests of one client reach different packages replicas. Every scenario has a pod, a directory and a
// package ID of its own (nuget_env_test.go).

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// flatVersions reads the version list of the flat container (R-NUGET-008) with the publisher's token.
func (fx *nugetFixture) flatVersions(t testing.TB, repo, id string) []string {
	t.Helper()
	r := fx.adm.get("/api/nuget/"+repo+"/v3/flatcontainer/"+strings.ToLower(id)+"/index.json").want(t, "flat container list of "+id, 200)
	var l struct {
		Versions []string `json:"versions"`
	}
	r.JSON(t, &l)
	return l.Versions
}

// listedState reads the registration index of the SemVer 1 hive (R-NUGET-009) and returns the `listed` flag of every
// version it names.
func (fx *nugetFixture) listedState(t testing.TB, repo, id string) map[string]bool {
	t.Helper()
	r := fx.adm.get("/api/nuget/"+repo+"/v3/registration/"+strings.ToLower(id)+"/index.json").want(t, "registration of "+id, 200)
	var doc struct {
		Items []struct {
			Items []struct {
				CatalogEntry struct {
					Version string `json:"version"`
					Listed  *bool  `json:"listed"`
				} `json:"catalogEntry"`
			} `json:"items"`
		} `json:"items"`
	}
	r.JSON(t, &doc)
	out := map[string]bool{}
	for _, page := range doc.Items {
		for _, it := range page.Items {
			if it.CatalogEntry.Listed == nil {
				t.Fatalf("registration of %s: the entry of %s has no explicit listed flag (R-NUGET-007)", id, it.CatalogEntry.Version)
			}
			out[it.CatalogEntry.Version] = *it.CatalogEntry.Listed
		}
	}
	return out
}

// revoke revokes a token of the publisher and waits until every api and packages replica refuses it: the revocation reaches the
// replicas within the bound of R-AUTHZ-028, so the first requests after the call may still be answered with the old state.
func (fx *nugetFixture) revoke(t *testing.T, tok createdToken) {
	t.Helper()
	fx.adm.do(http.MethodDelete, "/api/v1/tokens/"+tok.ID, nil).want(t, "revoke the token", 200, 204)
	seen := map[string]bool{}
	defer awaitTokenRefusedOnPackagesPods(t, tok.Token)
	waitFor(t, "the revoked token is refused by every api replica", time.Minute, time.Second, func() bool {
		clear(seen)
		for range 9 {
			r, err := newAPI(t).withToken(tok.Token).send(http.MethodGet, "/api/v1/authz/me", nil)
			if err != nil || r.Status != 401 {
				return false
			}
			seen[r.Replica()] = true
		}
		return len(seen) >= 3
	})
}

// dotnetPush runs `dotnet nuget push` and returns its output and the error of a non-zero exit.
func (c *nugetClient) dotnetPush(file, feed, key string, extra ...string) (string, error) {
	c.t.Helper()
	return c.try(append([]string{"dotnet", "nuget", "push", file, "--source", feed, "--api-key", key}, extra...)...)
}

// ── push, replay, conflict, revoked key (R-NUGET-006, R-NUGET-012, E3.5) ─────

func TestNuGetPush(t *testing.T) {
	fx := nugetFx(t)
	fx.withAnonymousRead(t)
	open, auth := nugetFeed(t, "localhost", nugetOpenKey), nugetFeed(t, "localhost", nugetAuthKey)

	// R-NUGET-012: the API key alone is the credential of the write; the feed answers the service index to the anonymous
	// request (anonymous read granted), so no Basic credential is configured.
	for i, tc := range []struct{ name, group, key string }{
		{"a plain token", "KeyPlain", fx.token},
		{"ci:<token>", "KeyCI", "ci:" + fx.token},
	} {
		t.Run("R-NUGET-012 E3.5 dotnet nuget push --api-key "+tc.name+" without credentials", func(t *testing.T) {
			id := fx.pkgID(tc.group)
			c := fx.client(t, i, "push-"+strings.ToLower(tc.group))
			c.config(open, false)
			file := c.place(id, "1.0.0", fx.nugetPack(t, id, "1.0.0", "pushed with "+tc.name))
			out, err := c.dotnetPush(file, open, tc.key)
			if err != nil || !pushedRE.MatchString(out) {
				t.Fatalf("push: %v\n%s", err, out)
			}
			if got := fx.flatVersions(t, nugetOpenKey, id); len(got) != 1 || got[0] != "1.0.0" {
				t.Fatalf("flat container versions = %v, want [1.0.0]", got)
			}
		})
	}

	// R-NUGET-012: the placeholder `az` is no token, so it is ignored and the Basic credentials of nuget.config
	// authenticate the push (the first attempt draws the 401 and the client retries with them).
	az := fx.pkgID("KeyAz")
	t.Run("R-NUGET-012 E3.5 dotnet nuget push --api-key az with the credentials in nuget.config", func(t *testing.T) {
		c := fx.client(t, 2, "push-az")
		c.config(auth, true)
		file := c.place(az, "1.0.0", fx.nugetPack(t, az, "1.0.0", "pushed with az"))
		out, err := c.dotnetPush(file, auth, "az")
		if err != nil || !pushedRE.MatchString(out) {
			t.Fatalf("push: %v\n%s", err, out)
		}
		if got := fx.flatVersions(t, nugetAuthKey, az); len(got) != 1 || got[0] != "1.0.0" {
			t.Fatalf("flat container versions = %v, want [1.0.0]", got)
		}
	})

	// R-NUGET-006 (Q-NUGET-6): the identical bytes of a stored version are accepted, whichever replica answers.
	t.Run("R-NUGET-006 E3.6 an identical push of a stored version exits 0 and changes nothing", func(t *testing.T) {
		for pod := range 3 {
			c := fx.client(t, pod, fmt.Sprintf("replay-%d", pod))
			c.config(auth, true)
			file := c.place(az, "1.0.0", fx.nugetPack(t, az, "1.0.0", "pushed with az"))
			out, err := c.dotnetPush(file, auth, "az")
			if err != nil || !pushedRE.MatchString(out) {
				t.Fatalf("replay from pod %d: %v\n%s", pod, err, out)
			}
		}
		if got := fx.flatVersions(t, nugetAuthKey, az); len(got) != 1 {
			t.Fatalf("flat container versions = %v after the replays, want one", got)
		}
	})

	t.Run("R-NUGET-006 a push of a stored version with other bytes fails with 409, and --skip-duplicate exits 0", func(t *testing.T) {
		c := fx.client(t, 1, "conflict")
		c.config(auth, true)
		file := c.place(az, "1.0.0", fx.nugetPack(t, az, "1.0.0", "rebuilt, other bytes"))
		c.mustRefuse("push of other bytes", "409", "dotnet", "nuget", "push", file, "--source", auth, "--api-key", "az")
		out, err := c.dotnetPush(file, auth, "az", "--skip-duplicate")
		if err != nil {
			t.Fatalf("--skip-duplicate: %v\n%s", err, out)
		}
	})

	// R-NUGET-012, INV-8: a revoked key is a 401, with nothing to fall back to. The Basic credentials of nuget.config are
	// valid in the first case and must not rescue the push; in the second the feed is anonymously readable and the
	// push still does not become anonymous.
	t.Run("R-NUGET-012 E3.5 a revoked token as API key fails with 401", func(t *testing.T) {
		tok := newSATokenWaiting(t, fx.adm, nugetPubSA, 3600, map[string]any{"inherit": true})
		fx.revoke(t, tok)
		id := fx.pkgID("Revoked")
		pkg := fx.nugetPack(t, id, "1.0.0", "never stored")
		for _, tc := range []struct{ name, feed string }{{"with valid Basic credentials", auth}, {"on the anonymously readable feed", open}} {
			c := fx.client(t, 0, "revoked-key")
			c.config(tc.feed, tc.feed == auth)
			file := c.place(id, "1.0.0", pkg)
			c.mustRefuse("push "+tc.name, "401", "dotnet", "nuget", "push", file, "--source", tc.feed, "--api-key", tok.Token)
		}
		if got := fx.adm.get("/api/nuget/" + nugetAuthKey + "/v3/flatcontainer/" + strings.ToLower(id) + "/index.json"); got.Status != 404 {
			t.Fatalf("the refused package is visible: %s", got)
		}
	})

	t.Run("R-NUGET-012 E3.5 a revoked token as the Basic password fails a restore with 401", func(t *testing.T) {
		tok := newSATokenWaiting(t, fx.adm, nugetPubSA, 3600, map[string]any{"inherit": true})
		fx.revoke(t, tok)
		c := fx.client(t, 1, "revoked-basic")
		c.token = tok.Token
		c.config(auth, true)
		proj := c.consumer("p", az, "1.0.0")
		c.mustRefuse("restore with a revoked password", "401", "dotnet", "restore", proj)
	})
}

// ── restore and the reads of the clients (R-NUGET-008, R-NUGET-009, R-NUGET-010, E3.5) ──

func TestNuGetRestore(t *testing.T) {
	fx := nugetFx(t)
	auth := nugetFeed(t, "localhost", nugetAuthKey)
	id := fx.pkgID("Restore")

	setup := fx.client(t, 0, "restore-setup")
	setup.config(auth, true)
	for _, v := range []string{"1.0.0", "1.1.0", "2.0.0-beta.1"} {
		file := setup.place(id, v, fx.nugetPack(t, id, v, "restore "+v))
		setup.mustPush("push "+v, "dotnet", "nuget", "push", file, "--source", auth, "--api-key", "az")
	}

	// E3.5: a restore with the token as Basic password, from the nuget.config of the snippets.
	t.Run("R-NUGET-027 E3.5 dotnet restore of a pinned version, the token as Basic password", func(t *testing.T) {
		c := fx.client(t, 1, "restore-pinned")
		c.config(auth, true)
		proj := c.consumer("p", id, "1.0.0")
		c.exec("dotnet", "restore", proj)
		if !c.hasPackage(id, "1.0.0") {
			t.Fatal("the restore did not extract the package")
		}
	})

	t.Run("R-NUGET-008 R-NUGET-009 dotnet restore resolves a floating version to the newest stable one", func(t *testing.T) {
		c := fx.client(t, 2, "restore-floating")
		c.config(auth, true)
		proj := c.consumer("p", id, "1.*")
		c.exec("dotnet", "restore", proj, "--no-cache")
		if !c.hasPackage(id, "1.1.0") || c.hasPackage(id, "1.0.0") || c.hasPackage(id, "2.0.0-beta.1") {
			t.Fatal("the floating version 1.* did not resolve to exactly 1.1.0")
		}
	})

	t.Run("R-NUGET-009 dotnet restore of a prerelease asks for it by name", func(t *testing.T) {
		c := fx.client(t, 0, "restore-prerelease")
		c.config(auth, true)
		proj := c.consumer("p", id, "2.0.0-beta.1")
		c.exec("dotnet", "restore", proj, "--no-cache")
		if !c.hasPackage(id, "2.0.0-beta.1") {
			t.Fatal("the prerelease was not restored")
		}
	})

	t.Run("R-NUGET-009 a version the repository does not hold fails the restore with NU1102", func(t *testing.T) {
		c := fx.client(t, 1, "restore-missing")
		c.config(auth, true)
		proj := c.consumer("p", id, "9.0.0")
		c.mustRefuse("restore of a missing version", "NU1102", "dotnet", "restore", proj, "--no-cache")
	})

	t.Run("R-NUGET-010 dotnet package search finds the package; the prerelease is not offered without --prerelease", func(t *testing.T) {
		c := fx.client(t, 2, "search")
		c.config(auth, true)
		out := c.exec("dotnet", "package", "search", id, "--source", auth, "--exact-match")
		if !strings.Contains(out, id) || !strings.Contains(out, "1.1.0") || strings.Contains(out, "2.0.0-beta.1") {
			t.Fatalf("search output:\n%s", out)
		}
		out = c.exec("dotnet", "package", "search", id, "--source", auth, "--exact-match", "--prerelease")
		if !strings.Contains(out, "2.0.0-beta.1") {
			t.Fatalf("search --prerelease output:\n%s", out)
		}
	})

	t.Run("R-NUGET-009 dotnet list package --outdated reads the newest version from the registration", func(t *testing.T) {
		c := fx.client(t, 0, "list-outdated")
		c.config(auth, true)
		proj := c.consumer("p", id, "1.0.0")
		c.exec("dotnet", "restore", proj)
		out := c.exec("dotnet", "list", proj, "package", "--outdated", "--source", auth)
		if !strings.Contains(out, id) || !strings.Contains(out, "1.1.0") {
			t.Fatalf("dotnet list package --outdated:\n%s", out)
		}
		out = c.exec("dotnet", "list", proj, "package")
		if !strings.Contains(out, id) || !strings.Contains(out, "1.0.0") {
			t.Fatalf("dotnet list package:\n%s", out)
		}
	})
}

// ── nuget.exe on Mono: push, install, delete (R-NUGET-027) ───────────────────

func TestNuGetExe(t *testing.T) {
	fx := nugetFx(t)
	auth := nugetFeed(t, "localhost", nugetAuthKey)
	id := fx.pkgID("Exe")
	pkg := fx.nugetPack(t, id, "1.0.0", "pushed by nuget.exe")
	c := fx.client(t, 1, "exe")
	c.config(auth, true)
	file := c.place(id, "1.0.0", pkg)

	t.Run("R-NUGET-027 R-NUGET-006 nuget push -ApiKey with the credentials in nuget.config", func(t *testing.T) {
		c.mustPush("nuget.exe push", "nuget", "push", file, "-Source", auth, "-ApiKey", fx.token, "-ConfigFile", "nuget.config", "-NonInteractive")
		if got := fx.flatVersions(t, nugetAuthKey, id); len(got) != 1 || got[0] != "1.0.0" {
			t.Fatalf("flat container versions = %v, want [1.0.0]", got)
		}
	})

	t.Run("R-NUGET-006 nuget push of the identical file exits 0, of other bytes fails with 409", func(t *testing.T) {
		c.mustPush("nuget.exe replay", "nuget", "push", file, "-Source", auth, "-ApiKey", fx.token, "-ConfigFile", "nuget.config", "-NonInteractive")
		other := c.place(id+".other", "1.0.0", fx.nugetPack(t, id, "1.0.0", "other bytes"))
		c.mustRefuse("nuget.exe push of other bytes", "409", "nuget", "push", other, "-Source", auth, "-ApiKey", fx.token, "-ConfigFile", "nuget.config", "-NonInteractive")
	})

	t.Run("R-NUGET-008 nuget install downloads the package with the token as Basic password", func(t *testing.T) {
		c.exec("nuget", "install", id, "-Version", "1.0.0", "-Source", auth, "-ConfigFile", "nuget.config", "-OutputDirectory", "installed", "-NonInteractive", "-DirectDownload", "-NoCache")
		if _, err := c.try("test", "-f", c.dir()+"/installed/"+id+".1.0.0/"+id+".1.0.0.nupkg"); err != nil {
			t.Fatal("nuget install did not extract the package")
		}
	})

	t.Run("R-NUGET-010 nuget search finds the package", func(t *testing.T) {
		out := c.exec("nuget", "search", id, "-Source", auth, "-NonInteractive")
		if !strings.Contains(out, id+" | 1.0.0") {
			t.Fatalf("nuget search:\n%s", out)
		}
	})

	t.Run("R-NUGET-007 nuget delete unlists by default: the search drops the version, the flat container keeps it", func(t *testing.T) {
		c.exec("nuget", "delete", id, "1.0.0", "-Source", auth, "-ApiKey", fx.token, "-ConfigFile", "nuget.config", "-NonInteractive")
		waitFor(t, "the search no longer offers the unlisted version", 30*time.Second, time.Second, func() bool {
			out, _ := c.try("nuget", "search", id, "-Source", auth, "-NonInteractive")
			return !strings.Contains(out, id+" | 1.0.0")
		})
		if got := fx.flatVersions(t, nugetAuthKey, id); len(got) != 1 {
			t.Fatalf("flat container versions = %v, an unlisted version stays downloadable", got)
		}
		if state := fx.listedState(t, nugetAuthKey, id); state["1.0.0"] {
			t.Fatalf("registration state %v, want listed false", state)
		}
	})
}

// ── unlist, relist, delete with the .NET CLI (R-NUGET-007) ────────────────────

func TestNuGetUnlistRelistDelete(t *testing.T) {
	fx := nugetFx(t)
	auth, hard := nugetFeed(t, "localhost", nugetAuthKey), nugetFeed(t, "localhost", nugetHardKey)
	id := fx.pkgID("Unlist")

	push := fx.client(t, 0, "unlist-setup")
	push.config(auth, true)
	for _, v := range []string{"1.0.0", "1.1.0"} {
		file := push.place(id, v, fx.nugetPack(t, id, v, "unlist "+v))
		push.mustPush("push "+v, "dotnet", "nuget", "push", file, "--source", auth, "--api-key", "az")
	}
	searchShows := func(c *nugetClient, version string) bool {
		out, err := c.try("dotnet", "package", "search", id, "--source", auth, "--exact-match")
		return err == nil && strings.Contains(out, version)
	}

	t.Run("R-NUGET-007 dotnet nuget delete unlists by default; a project pinned to the version still restores", func(t *testing.T) {
		c := fx.client(t, 1, "unlist")
		c.config(auth, true)
		c.exec("dotnet", "nuget", "delete", id, "1.1.0", "--source", auth, "--api-key", "az", "--non-interactive")
		waitFor(t, "the search drops the unlisted version", 30*time.Second, time.Second, func() bool { return !searchShows(c, "1.1.0") })
		if !searchShows(c, "1.0.0") {
			t.Fatal("the search lost the listed version as well")
		}
		if got := fx.flatVersions(t, nugetAuthKey, id); len(got) != 2 {
			t.Fatalf("flat container versions = %v, want both", got)
		}
		if state := fx.listedState(t, nugetAuthKey, id); state["1.1.0"] || !state["1.0.0"] {
			t.Fatalf("registration listed flags = %v, want 1.1.0 false and 1.0.0 true", state)
		}
		proj := c.consumer("p", id, "[1.1.0]")
		c.exec("dotnet", "restore", proj, "--no-cache")
		if !c.hasPackage(id, "1.1.0") {
			t.Fatal("the unlisted version did not restore")
		}
		// the delete the client repeats, or a second developer's, exits 0 as well
		c.exec("dotnet", "nuget", "delete", id, "1.1.0", "--source", auth, "--api-key", "az", "--non-interactive")
	})

	t.Run("R-NUGET-007 relist brings the version back into the search", func(t *testing.T) {
		c := fx.client(t, 2, "relist")
		c.config(auth, true)
		path := "/api/nuget/" + nugetAuthKey + "/v3/package/" + strings.ToLower(id) + "/1.1.0"
		for range 2 { // a second relist answers 200 again
			fx.adm.do(http.MethodPost, path, nil, "X-NuGet-ApiKey", fx.token).want(t, "relist", 200)
		}
		waitFor(t, "the search offers the relisted version", 30*time.Second, time.Second, func() bool { return searchShows(c, "1.1.0") })
		if state := fx.listedState(t, nugetAuthKey, id); !state["1.1.0"] {
			t.Fatalf("registration listed flags = %v after the relist", state)
		}
	})

	t.Run("R-NUGET-007 R-NUGET-006 deleteMode delete: the version is gone, its number is tombstoned", func(t *testing.T) {
		hid := fx.pkgID("Hard")
		c := fx.client(t, 1, "hard")
		c.config(hard, true)
		var pkgs [][]byte
		for _, v := range []string{"1.0.0", "1.1.0"} {
			pkg := fx.nugetPack(t, hid, v, "hard "+v)
			pkgs = append(pkgs, pkg)
			file := c.place(hid, v, pkg)
			c.mustPush("push "+v, "nuget", "push", file, "-Source", hard, "-ApiKey", fx.token, "-ConfigFile", "nuget.config", "-NonInteractive")
		}
		c.exec("nuget", "delete", hid, "1.1.0", "-Source", hard, "-ApiKey", fx.token, "-ConfigFile", "nuget.config", "-NonInteractive")
		nupkg := "/api/nuget/" + nugetHardKey + "/v3/flatcontainer/" + strings.ToLower(hid) + "/1.1.0/" + strings.ToLower(hid) + ".1.1.0.nupkg"
		if r := fx.adm.get(nupkg); r.Status != 404 {
			t.Fatalf("the .nupkg of the deleted version: %s, want 404", r)
		}
		if got := fx.flatVersions(t, nugetHardKey, hid); len(got) != 1 || got[0] != "1.0.0" {
			t.Fatalf("flat container versions = %v, want [1.0.0]", got)
		}
		// a delete repeated after the first committed answers 204 (R-NUGET-032), also for the dotnet client
		c.exec("dotnet", "nuget", "delete", hid, "1.1.0", "--source", hard, "--api-key", fx.token, "--non-interactive")
		// the tombstone refuses the number, the identical bytes included
		file := c.place(hid, "1.1.0", pkgs[1])
		c.mustRefuse("push of a tombstoned version", "409", "dotnet", "nuget", "push", file, "--source", hard, "--api-key", "az")
		// and a dotnet restore of it fails: nothing serves the version
		c.mustRefuse("restore of a deleted version", "NU1102", "dotnet", "restore", c.consumer("p", hid, "[1.1.0]"), "--no-cache")
		// the next number is free
		file = c.place(hid, "1.2.0", fx.nugetPack(t, hid, "1.2.0", "hard 1.2.0"))
		c.mustPush("push 1.2.0", "dotnet", "nuget", "push", file, "--source", hard, "--api-key", "az")
	})
}
