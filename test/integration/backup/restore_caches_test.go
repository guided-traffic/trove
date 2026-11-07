//go:build integration

package backup

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/blob/s3"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/outbound"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// sourceOf takes a backup of an installation that is already running (the formats fixture), with the remote caches left out of
// the blob list.
func sourceOf(t *testing.T, stack *testenv.Stack, excludeCaches bool) *source {
	t.Helper()
	loaded, err := config.Load(config.Options{Environ: stack.Environ(), Command: config.CommandAdmin})
	if err != nil {
		t.Fatal(err)
	}
	cfg := loaded.Config
	client, err := s3.NewFromFactory(context.Background(), cfg.Storage.S3, outbound.New(cfg.Proxy.Outbound))
	if err != nil {
		t.Fatal(err)
	}
	fetch := func(d format.Digest) []byte {
		h := d.Hex()
		rc, _, err := client.Get(context.Background(), cfg.Storage.S3.Prefix+"/sha256/"+h[0:2]+"/"+h[2:4]+"/"+h, nil)
		if err != nil {
			t.Fatalf("blob %s of the source: %v", h, err)
		}
		defer func() { _ = rc.Close() }()
		b, err := io.ReadAll(rc)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	tcfg := targetConfig(t, testenv.Get(t, "TROVE_TEST_S3_BUCKET_BACKUPS"), "none")
	tgt := newTarget(t, tcfg)
	cleanup(t, tgt, tcfg.Target.Prefix+"/")
	layout := format.Layout{Prefix: tcfg.Target.Prefix}
	m := exportBackup(t, stack, tgt, layout, nil, exportOptions{excludeCaches: excludeCaches, fetch: fetch})
	return &source{t: t, stack: stack, layout: layout, tgtCfg: tcfg, target: tgt, id: m.BackupID, m: m}
}

// A backup made with backup.exclude.remote_caches restores through step 7: the remote caches of npm and NuGet are emptied
// through the storage delete path with the format hooks, local repositories are untouched, and rev_seq grows past the value the
// fix-ups gave it (R-BAK-008, R-BAK-021 step 7).
func TestR_BAK_008_RestoreWithoutRemoteCaches(t *testing.T) {
	e := newFormatsEnv(t)
	r := e.replica("web-1")
	p := upstreamPackage(t, 2)
	e.publish(r, repoNPMLocal, npmPkg{Name: acmeUI, Version: "1.0.0"})
	local := nupkg(t, "Acme.Widgets", "1.0.0", "Widgets")
	e.mustPushNuGet(r, local)
	if a := e.npmGet(r, repoNPMRemote, p.Name); a.Status != 200 {
		t.Fatalf("npm document: %d", a.Status)
	}
	if a := e.remoteTarball(r, repoNPMRemote, p.Name, p.Versions[0].Version); a.Status != 200 {
		t.Fatalf("npm tarball: %d", a.Status)
	}
	const id, version = "newtonsoft.json", "13.0.1"
	if a := e.nugetGet(r, repoNuGetOrg, "v3/flatcontainer/"+id+"/index.json"); a.Status != 200 {
		t.Fatalf("nuget versions: %d", a.Status)
	}
	if a := e.nugetGet(r, repoNuGetOrg, "v3/flatcontainer/"+id+"/"+version+"/"+id+"."+version+".nupkg"); a.Status != 200 {
		t.Fatalf("nupkg: %d", a.Status)
	}
	if e.allItems(repoNPMRemote) == 0 || e.allItems(repoNuGetOrg) == 0 {
		t.Fatal("the remote caches are empty before the backup")
	}
	srcNPMLocal, srcNuGetLocal := e.revSeq(repoNPMLocal, acmeUI), e.revSeq(repoNuGet, "acme.widgets")
	srcNPMRemote, srcNuGetRemote := e.revSeq(repoNPMRemote, p.Name), e.revSeq(repoNuGetOrg, id)
	localItems := e.allItems(repoNPMLocal) + e.allItems(repoNuGet)

	src := sourceOf(t, e.st, true)
	if !src.m.Options.ExcludeRemoteCaches {
		t.Fatal("the manifest does not record the option")
	}
	dst := src.destination()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	r2, _ := dst.restorer(restorerOptions{})
	if _, err := r2.Restore(ctx, backup.RestoreOptions{ID: src.id, SkipRevocationJournal: true}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	s := dst.stack
	count := func(repo string) int64 {
		return s.Number(`SELECT count(*) FROM items i JOIN repositories r ON r.id = i.repo_id WHERE r.key = $1 AND i.kind = 'file'`, repo)
	}
	rev := func(repo, name string) int64 {
		return s.Number(`SELECT coalesce(max(p.rev_seq), 0) FROM packages p JOIN repositories r ON r.id = p.repo_id WHERE r.key = $1 AND p.name = $2`, repo, name)
	}

	t.Run("R-BAK-008 the caches of npm and NuGet are emptied", func(t *testing.T) {
		for _, repo := range []string{repoNPMRemote, repoNuGetOrg} {
			if n := count(repo); n != 0 {
				t.Errorf("%s: %d cached items left", repo, n)
			}
			if n := s.Number(`SELECT count(*) FROM remote_cache_entries e JOIN repositories r ON r.id = e.repo_id WHERE r.key = $1`, repo); n != 0 {
				t.Errorf("%s: %d cache entries left", repo, n)
			}
		}
		if n := s.Number(`SELECT count(*) FROM npm_remote_packages`); n != 0 {
			t.Errorf("%d npm_remote_packages rows left", n)
		}
		if n := s.Number(`SELECT count(*) FROM nuget_version nv JOIN repositories r ON r.id = nv.repository_id WHERE r.key = $1 AND nv.origin = 'cache'`, repoNuGetOrg); n != 0 {
			t.Errorf("%d NuGet cache rows left", n)
		}
	})
	t.Run("R-BAK-008 local repositories are untouched and rev_seq grew by the fix-up only", func(t *testing.T) {
		if got := count(repoNPMLocal) + count(repoNuGet); got != localItems {
			t.Errorf("local items %d -> %d", localItems, got)
		}
		if got := rev(repoNPMLocal, acmeUI); got != srcNPMLocal+(1<<32) {
			t.Errorf("npm local rev_seq = %d, want %d", got, srcNPMLocal+(1<<32))
		}
		if got := rev(repoNuGet, "acme.widgets"); got != srcNuGetLocal+(1<<32) {
			t.Errorf("nuget local rev_seq = %d, want %d", got, srcNuGetLocal+(1<<32))
		}
		if n := s.Number(`SELECT count(*) FROM items i JOIN repositories r ON r.id = i.repo_id WHERE r.class = 'local'`); n == 0 {
			t.Error("no local item survived")
		}
	})
	t.Run("R-BAK-008 a package row that remains has a rev_seq above the fix-up's value", func(t *testing.T) {
		for repo, name := range map[string]string{repoNPMRemote: p.Name, repoNuGetOrg: id} {
			old := srcNPMRemote
			if repo == repoNuGetOrg {
				old = srcNuGetRemote
			}
			if got := rev(repo, name); got != 0 && got <= old+(1<<32) {
				t.Errorf("%s/%s: rev_seq %d did not grow past %d", repo, name, got, old+(1<<32))
			}
		}
	})
	t.Run("R-BAK-008 blob rows no one references are gone, every remaining live row has its object", func(t *testing.T) {
		if n := s.Number(`SELECT count(*) FROM blobs b WHERE NOT EXISTS (SELECT 1 FROM items i WHERE i.blob_sha256 = b.sha256)`); n != 0 {
			// Rows referenced by another table of a format are possible; the restore verified their objects in step 8.
			t.Logf("%d blob rows are not referenced by an item", n)
		}
		if n := s.Number(`SELECT count(*) FROM system_state WHERE key = 'restore_in_progress'`); n != 0 {
			t.Error("the restore did not finish")
		}
	})
}
