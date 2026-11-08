//go:build integration

package files

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/formats/oci"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/qname"
)

// removalInTx removes the paths through the real item store (LockRemoval, the hook) in a transaction that waits for release before it
// commits. lockPkg also takes the package row first, as the sweep does for a name that has one.
func removalInTx(t *testing.T, f *ociFixture, name string, lockPkg bool, paths ...string) (locked, release chan struct{}, done chan error) {
	t.Helper()
	ctx := context.Background()
	repo, err := f.r.reader.ByName(ctx, qname.Repo{Project: "it", Key: "oci-local"})
	if err != nil {
		t.Fatal(err)
	}
	locked, release, done = make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- f.r.db.InTx(ctx, func(tx meta.Tx) error {
			if lockPkg {
				ptx, _ := postgres.Unwrap(tx)
				if _, err := ptx.Exec(ctx, `SELECT 1 FROM packages WHERE repo_id = $1 AND name = $2 FOR UPDATE`, repo.ID, name); err != nil {
					return err
				}
			}
			if err := f.r.items.RemoveIn(ctx, tx, items.RemoveSpec{Repo: items.RepoOf(repo), Paths: paths, Actor: audit.Actor{Kind: "system", Name: "removal"}}); err != nil {
				return err
			}
			close(locked)
			<-release
			return nil
		})
	}()
	return locked, release, done
}

func lockWait(t *testing.T, f *ociFixture) {
	t.Helper()
	waitFor(t, "a lock wait", 30*time.Second, func() bool {
		return f.count(`SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND datname = current_database()`) > 0
	})
	t.Logf("waiting: %s", f.e.text(`SELECT COALESCE(string_agg(left(regexp_replace(query, '\s+', ' ', 'g'), 90), ' | '), '') FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND datname = current_database()`))
}

// A removal that holds the rows of a layer before a real push of a manifest that names it, for a name without a package row (no package
// lock: the blob rows arbitrate, R-OCI-019): the push is refused and writes no rows.
func TestR_OCI_019_ARemovalThatWinsLeavesNoManifestOverAnUnlockedName(t *testing.T) {
	f := newOCIFixture(t)
	cfg, layer := f.blob("race", []byte("{}")), f.blob("race", []byte("layer"))
	if n := f.count(`SELECT count(*) FROM packages WHERE name = 'race'`); n != 0 {
		t.Fatalf("%d package rows before the push", n)
	}
	lp := oci.BlobPath("race", layer)
	locked, release, done := removalInTx(t, f, "race", false, lp)
	<-locked
	resCh := make(chan response, 1)
	go func() { resCh <- f.push("writer", "race", "1", ociManifest(cfg, layer), ociCT) }()
	lockWait(t, f)
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	res := <-resCh
	rows, refs := f.count(`SELECT count(*) FROM oci_manifests WHERE name = 'race'`), f.count(`SELECT count(*) FROM oci_references WHERE name = 'race'`)
	t.Logf("push %d %s; rows %d refs %d layer items %d", res.status, res.body, rows, refs, f.count(`SELECT count(*) FROM items WHERE path = $1`, lp))
	if res.status/100 == 2 || rows != 0 || refs != 0 {
		t.Fatal("DANGLING: a manifest naming a removed layer committed")
	}
}

// The same for a name that has a package row, which the removal locks first.
func TestR_OCI_019_ARemovalThatWinsLeavesNoManifestOverALockedName(t *testing.T) {
	f := newOCIFixture(t)
	cfg0, l0 := f.blob("race", []byte("{}")), f.blob("race", []byte("layer0"))
	wantOCI(t, f.push("writer", "race", "0", ociManifest(cfg0, l0), ociCT), 201, "")
	layer := f.blob("race", []byte("layer1"))
	lp := oci.BlobPath("race", layer)
	locked, release, done := removalInTx(t, f, "race", true, lp)
	<-locked
	resCh := make(chan response, 1)
	go func() { resCh <- f.push("writer", "race", "1", ociManifest(cfg0, layer), ociCT) }()
	lockWait(t, f)
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	res := <-resCh
	t.Logf("push %d %s", res.status, res.body)
	if res.status/100 == 2 {
		t.Fatal("DANGLING: a manifest naming a removed layer committed")
	}
}

// A tag push that links to a stored manifest while a removal deletes the manifest: no tag is left over a manifest that is gone.
func TestR_OCI_018_ATagLinkRacingTheDeleteOfItsManifestLeavesNoDanglingTag(t *testing.T) {
	f := newOCIFixture(t)
	cfg, layer := f.blob("race2", []byte("{}")), f.blob("race2", []byte("layer2"))
	m := ociManifest(cfg, layer)
	d := shaOf(m)
	wantOCI(t, f.push("writer", "race2", d, m, ociCT), 201, "")
	mPath := oci.ManifestPath("race2", d)
	locked, release, done := removalInTx(t, f, "race2", true, mPath)
	<-locked
	resCh := make(chan response, 1)
	go func() { resCh <- f.push("writer", "race2", "1", m, ociCT) }()
	lockWait(t, f)
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	res := <-resCh
	tags := f.count(`SELECT count(*) FROM items WHERE path = $1`, oci.TagPath("race2", "1"))
	items := f.count(`SELECT count(*) FROM items WHERE path = $1`, mPath)
	rows := f.count(`SELECT count(*) FROM oci_manifests WHERE name = 'race2'`)
	t.Logf("tag push %d %s; tag items %d manifest items %d rows %d", res.status, res.body, tags, items, rows)
	if tags == 1 && (items == 0 || rows == 0) {
		t.Fatal("DANGLING tag")
	}
}

// A grant that excludes a path from delete is honoured by the registry route as it is by /files/ (R-AUTHZ-005).
func TestR_OCI_018_ADeleteOnTheRegistryRouteHonoursAPathExcludedFromDelete(t *testing.T) {
	f := newOCIFixture(t)
	f.e.user("deleter")
	f.e.grant(f.r, "oci-delete", []string{"oci-local"}, authz.PatternLists{Exclude: []string{"**/_tags/keep"}}, map[string][]string{
		"user:deleter": {"read", "delete"},
	})
	cfg, layer := f.blob("app", []byte("{}")), f.blob("app", []byte("layer"))
	m := ociManifest(cfg, layer)
	wantOCI(t, f.push("writer", "app", "keep", m, ociCT), 201, "")
	wantOCI(t, f.push("writer", "app", "other", m, ociCT), 201, "")
	time.Sleep(2 * time.Second) // the policy reaches the index
	viaFiles := f.r.do("deleter", http.MethodDelete, "/files/it/oci-local/app/_tags/keep", nil)
	viaRegistry := f.r.do("deleter", http.MethodDelete, "/api/oci/it/oci-local/app/manifests/keep", nil)
	t.Logf("/files/ DELETE: %d %s", viaFiles.status, viaFiles.body)
	t.Logf("registry DELETE: %d %s; tag items left %d", viaRegistry.status, viaRegistry.body, f.count(`SELECT count(*) FROM items WHERE path = 'app/_tags/keep'`))
	if viaFiles.status == http.StatusForbidden && viaRegistry.status == http.StatusAccepted {
		t.Error("BYPASS: the registry route deletes a tag the grant's path exclude withholds from delete")
	}
	if viaRegistry.status != http.StatusForbidden || f.count(`SELECT count(*) FROM items WHERE path = 'app/_tags/keep'`) != 1 {
		t.Errorf("the registry route answered %d for the excluded tag", viaRegistry.status)
	}
	// A delete by digest would remove the excluded tag in its cascade: refused as a whole (Q-OCI-76).
	byDigest := f.r.do("deleter", http.MethodDelete, "/api/oci/it/oci-local/app/manifests/"+shaOf(m), nil)
	if byDigest.status != http.StatusForbidden || f.count(`SELECT count(*) FROM items WHERE path = $1`, oci.ManifestPath("app", shaOf(m))) != 1 {
		t.Errorf("DELETE by digest answered %d %s: the cascade takes a tag the grant excludes", byDigest.status, byDigest.body)
	}
	// The tags list leaves the excluded tag out, as a pull of it is refused.
	list := f.r.do("deleter", http.MethodGet, "/api/oci/it/oci-local/app/tags/list", nil)
	if list.status != http.StatusOK || strings.Contains(string(list.body), "keep") || !strings.Contains(string(list.body), "other") {
		t.Errorf("tags list %d %s", list.status, list.body)
	}
}
