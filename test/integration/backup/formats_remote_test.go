//go:build integration

package backup

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/backup/fixup"
	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/npmstub/corpus"
)

// upstreamPackage returns a plain package of the upstream's corpus with at least n versions.
func upstreamPackage(t testing.TB, n int) *corpus.Package {
	t.Helper()
	for _, p := range corpus.Default().Packages {
		if !strings.HasPrefix(p.Name, "@") && len(p.Versions) >= n && len(p.Versions) < 30 {
			return p
		}
	}
	t.Fatal("the corpus has no such package")
	return nil
}

func (e *fenv) remoteTarball(r *testenv.Replica, repo, name, version string) testenv.Answer {
	e.t.Helper()
	return e.st.Do(r, http.MethodGet, npmPath(repo, name)+"/-/"+corpus.TarballFile(name, version), e.tokReader, nil)
}

// TestR_NPM_025_DocumentsValidatedBeforeTheRestoreAreNotCurrent: a package document validated before
// system_state.restored_at counts as not current, so the first tarball request after a restore revalidates it and
// re-applies the removal marks (Q-NPM-13). The restored rows still list the version the upstream withdrew after the
// snapshot; the tarball is not served on that basis.
func TestR_NPM_025_DocumentsValidatedBeforeTheRestoreAreNotCurrent(t *testing.T) {
	e := newFormatsEnv(t)
	r := e.replica("web-1")
	p := upstreamPackage(t, 2)
	v := p.Versions[0].Version
	docs := func() int { return e.npm.Counts().Documents(p.Name) }

	if a := e.npmGet(r, repoNPMRemote, p.Name); a.Status != 200 {
		t.Fatalf("document: %d %.200s", a.Status, a.Body)
	}
	if a := e.remoteTarball(r, repoNPMRemote, p.Name, v); a.Status != 200 {
		t.Fatalf("tarball: %d", a.Status)
	}
	if docs() != 1 {
		t.Fatalf("%d upstream document requests, want 1", docs())
	}
	// The upstream drops the version after the snapshot. Inside the metadata period the cached document is current and
	// the cached tarball is served without a question.
	if err := e.npm.Drop(p.Name, v); err != nil {
		t.Fatal(err)
	}
	if a := e.remoteTarball(r, repoNPMRemote, p.Name, v); a.Status != 200 || docs() != 1 {
		t.Fatalf("before the restore: tarball %d, %d document requests; want a hit without a question", a.Status, docs())
	}

	// The restore finishes: restored_at (an RFC 3339 time, as the finishing transaction of R-BAK-021 step 9 writes it) is later
	// than every validated_at of the restored rows.
	time.Sleep(20 * time.Millisecond)
	e.st.Exec(`INSERT INTO system_state (key, value) VALUES ('restored_at', to_jsonb(to_char(now() AT TIME ZONE 'utc', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')))
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`)

	a := e.remoteTarball(r, repoNPMRemote, p.Name, v)
	if a.Status != http.StatusNotFound {
		t.Errorf("tarball after the restore = %d (%d bytes), %d document requests, want 404 for the version the upstream withdrew", a.Status, len(a.Body), docs())
	}
	if docs() != 2 {
		t.Errorf("%d upstream document requests, want 2: the first request after the restore revalidates the document", docs())
	}
	// A version the upstream still has is served after one more revalidation at most.
	other := p.Versions[1].Version
	if a := e.remoteTarball(r, repoNPMRemote, p.Name, other); a.Status != 200 {
		t.Errorf("a version the upstream still serves = %d", a.Status)
	}
	if docs() != 2 {
		t.Errorf("%d document requests: the revalidated document is current again", docs())
	}
}

// purge runs the registered purger of a format the way the restore's step 7 does.
func (e *fenv) purge(r *testenv.Replica, format string) {
	e.t.Helper()
	for _, p := range fixup.Purgers() {
		if p.Name == format {
			if err := p.Purge(context.Background(), e.db, fixup.Env{ExcludeRemoteCaches: true, Items: r.App.Items()}); err != nil {
				e.t.Fatalf("purge %s: %v", format, err)
			}
			return
		}
	}
	e.t.Fatalf("no purger %q", format)
}

func (e *fenv) cacheEntries(repo string) int64 {
	e.t.Helper()
	return e.st.Number(`SELECT count(*) FROM remote_cache_entries e JOIN repositories r ON r.id = e.repo_id WHERE r.key = $1`, repo)
}

func (e *fenv) allItems(repo string) int64 {
	e.t.Helper()
	return e.st.Number(`SELECT count(*) FROM items i JOIN repositories r ON r.id = i.repo_id WHERE r.key = $1 AND i.kind = 'file'`, repo)
}

func TestR_BAK_008_TheNPMPurgerEmptiesRemoteCachesAndLeavesLocalRepositoriesAlone(t *testing.T) {
	e := newFormatsEnv(t)
	r := e.replica("web-1")
	p := upstreamPackage(t, 2)
	v := p.Versions[0].Version
	integrity := e.publish(r, repoNPMLocal, npmPkg{Name: acmeUI, Version: "1.0.0"})
	e.publish(r, repoNPMLocal, npmPkg{Name: acmeUI, Version: "1.1.0"})
	// A tombstone of the local repository must survive the purge: tombstones belong to local repositories.
	if res := e.replay("npm.unpublish", e.unpublishEntry(repoNPMLocal, acmeUI, "1.1.0", e.st.Text(`SELECT nv.integrity FROM npm_versions nv JOIN package_versions pv ON pv.id = nv.version_id WHERE pv.version = '1.1.0'`)), time.Now()); res.Outcome != "applied" {
		t.Fatalf("withhold: %+v", res)
	}
	if a := e.npmGet(r, repoNPMRemote, p.Name); a.Status != 200 {
		t.Fatalf("document: %d", a.Status)
	}
	if a := e.remoteTarball(r, repoNPMRemote, p.Name, v); a.Status != 200 {
		t.Fatalf("tarball: %d", a.Status)
	}
	remotePkgRev := func() int64 { return e.revSeq(repoNPMRemote, p.Name) }
	localRev, localItems := e.revSeq(repoNPMLocal, acmeUI), e.allItems(repoNPMLocal)
	tombstones := e.st.Number(`SELECT count(*) FROM npm_tombstones`)
	packagesRev := remotePkgRev()
	if e.allItems(repoNPMRemote) == 0 || e.cacheEntries(repoNPMRemote) == 0 || e.st.Number(`SELECT count(*) FROM npm_remote_packages`) != 1 {
		t.Fatalf("the remote cache is empty before the purge")
	}
	if e.st.Number(`SELECT count(*) FROM npm_remote_versions`) == 0 {
		t.Fatal("no npm_remote_versions rows before the purge")
	}
	docs, tarballs := e.npm.Counts().Documents(p.Name), e.npm.Counts().Tarballs(p.Name)

	e.purge(r, "npm")

	if n := e.allItems(repoNPMRemote); n != 0 {
		t.Errorf("%d cached items left", n)
	}
	if n := e.cacheEntries(repoNPMRemote); n != 0 {
		t.Errorf("%d cache entries left", n)
	}
	if n := e.st.Number(`SELECT count(*) FROM npm_remote_packages`); n != 0 {
		t.Errorf("%d npm_remote_packages rows left", n)
	}
	if n := e.st.Number(`SELECT count(*) FROM npm_remote_versions`); n != 0 {
		t.Errorf("%d npm_remote_versions rows left: they cascade with the package rows", n)
	}
	if got := remotePkgRev(); got <= packagesRev {
		t.Errorf("packages.rev_seq of the cached package %d -> %d: it must grow (the removal went through the storage hook)", packagesRev, got)
	}
	if n := e.st.Number(`SELECT count(*) FROM package_versions pv JOIN packages p ON p.id = pv.package_id JOIN repositories r ON r.id = p.repo_id WHERE r.key = $1`, repoNPMRemote); n != 0 {
		t.Errorf("%d cached version rows left", n)
	}
	t.Run("R-BAK-008 local repositories and their tombstones are left alone", func(t *testing.T) {
		if got := e.revSeq(repoNPMLocal, acmeUI); got != localRev {
			t.Errorf("local rev_seq %d -> %d", localRev, got)
		}
		if got := e.allItems(repoNPMLocal); got != localItems {
			t.Errorf("local items %d -> %d", localItems, got)
		}
		if got := e.st.Number(`SELECT count(*) FROM npm_tombstones`); got != tombstones {
			t.Errorf("tombstones %d -> %d: the purge wrote or removed one", tombstones, got)
		}
		if a := e.npmGet(r, repoNPMLocal, acmeUI+"/-/ui-1.0.0.tgz"); a.Status != 200 || integrityOf(a.Body) != integrity {
			t.Errorf("a local tarball after the purge = %d", a.Status)
		}
	})
	t.Run("R-BAK-008 the purge is repeatable", func(t *testing.T) {
		e.purge(r, "npm")
		if n := e.cacheEntries(repoNPMRemote); n != 0 {
			t.Errorf("%d cache entries after the second purge", n)
		}
	})
	t.Run("R-BAK-008 the first request afterwards fetches from the upstream as on an empty remote", func(t *testing.T) {
		if a := e.npmGet(r, repoNPMRemote, p.Name); a.Status != 200 {
			t.Fatalf("document: %d", a.Status)
		}
		if a := e.remoteTarball(r, repoNPMRemote, p.Name, v); a.Status != 200 {
			t.Fatalf("tarball: %d", a.Status)
		}
		if got := e.npm.Counts().Documents(p.Name); got != docs+1 {
			t.Errorf("%d document requests, want %d", got, docs+1)
		}
		if got := e.npm.Counts().Tarballs(p.Name); got != tarballs+1 {
			t.Errorf("%d tarball requests, want %d", got, tarballs+1)
		}
	})
}

func TestR_BAK_008_TheNuGetPurgerEmptiesRemoteCachesAndWritesNoTombstone(t *testing.T) {
	e := newFormatsEnv(t)
	r := e.replica("web-1")
	local := nupkg(t, "Acme.Widgets", "1.0.0", "Widgets")
	e.mustPushNuGet(r, local)
	const id, version = "newtonsoft.json", "13.0.1"
	if a := e.nugetGet(r, repoNuGetOrg, "v3/flatcontainer/"+id+"/index.json"); a.Status != 200 {
		t.Fatalf("version list: %d %.200s", a.Status, a.Body)
	}
	if a := e.nugetGet(r, repoNuGetOrg, "v3/flatcontainer/"+id+"/"+version+"/"+id+"."+version+".nupkg"); a.Status != 200 {
		t.Fatalf("nupkg: %d", a.Status)
	}
	cacheRows := func() int64 {
		return e.st.Number(`SELECT count(*) FROM nuget_version nv JOIN repositories r ON r.id = nv.repository_id WHERE r.key = $1 AND nv.origin = 'cache'`, repoNuGetOrg)
	}
	if cacheRows() == 0 || e.allItems(repoNuGetOrg) == 0 || e.cacheEntries(repoNuGetOrg) == 0 {
		t.Fatalf("the remote cache is empty before the purge: %d rows, %d items, %d entries", cacheRows(), e.allItems(repoNuGetOrg), e.cacheEntries(repoNuGetOrg))
	}
	localItems, localRev := e.allItems(repoNuGet), e.revSeq(repoNuGet, "acme.widgets")
	tombstones := e.st.Number(`SELECT count(*) FROM nuget_tombstone`)
	cachedRev := e.revSeq(repoNuGetOrg, id)
	lists := e.nug.Counts().VersionLists(id)

	e.purge(r, "nuget")

	if cacheRows() != 0 || e.allItems(repoNuGetOrg) != 0 || e.cacheEntries(repoNuGetOrg) != 0 {
		t.Errorf("left after the purge: %d rows, %d items, %d entries", cacheRows(), e.allItems(repoNuGetOrg), e.cacheEntries(repoNuGetOrg))
	}
	if got := e.revSeq(repoNuGetOrg, id); got <= cachedRev {
		t.Errorf("rev_seq of the cached package %d -> %d: it must grow", cachedRev, got)
	}
	if got := e.st.Number(`SELECT count(*) FROM nuget_tombstone`); got != tombstones {
		t.Errorf("tombstones %d -> %d: a remote cache purge writes none (R-NUGET-006)", tombstones, got)
	}
	if e.allItems(repoNuGet) != localItems || e.revSeq(repoNuGet, "acme.widgets") != localRev {
		t.Errorf("the local repository changed: items %d -> %d, rev %d -> %d", localItems, e.allItems(repoNuGet), localRev, e.revSeq(repoNuGet, "acme.widgets"))
	}
	if a := e.nugetGet(r, repoNuGet, "v3/flatcontainer/acme.widgets/1.0.0/acme.widgets.1.0.0.nupkg"); a.Status != 200 || sha512hex(a.Body) != sha512hex(local) {
		t.Errorf("the local package after the purge = %d", a.Status)
	}
	e.purge(r, "nuget")
	if a := e.nugetGet(r, repoNuGetOrg, "v3/flatcontainer/"+id+"/index.json"); a.Status != 200 {
		t.Fatalf("version list after the purge: %d", a.Status)
	}
	if got := e.nug.Counts().VersionLists(id); got != lists+1 {
		t.Errorf("%d version list requests, want %d: the first request fetches as on an empty remote", got, lists+1)
	}
}
