//go:build integration

package files

import (
	"context"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/formats/oci"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/qname"
)

// removalHoldingTheLock (a regression test of the review of the manifest push) runs, in a transaction of its own, what the sweep (or a DELETE) does: it takes the package row lock of the
// name first, removes the paths through the real item store (the real storage hook runs), then waits for release before it commits.
func removalHoldingTheLock(t *testing.T, f *ociFixture, name string, paths ...string) (locked, release chan struct{}, done chan error) {
	t.Helper()
	ctx := context.Background()
	repo, err := f.r.reader.ByName(ctx, qname.Repo{Project: "it", Key: "oci-local"})
	if err != nil {
		t.Fatal(err)
	}
	locked, release, done = make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- f.r.db.InTx(ctx, func(tx meta.Tx) error {
			ptx, _ := postgres.Unwrap(tx)
			if _, err := ptx.Exec(ctx, `INSERT INTO packages (repo_id, name, display_name) VALUES ($1, $2, $2) ON CONFLICT DO NOTHING`, repo.ID, name); err != nil {
				return err
			}
			if _, err := ptx.Exec(ctx, `SELECT 1 FROM packages WHERE repo_id = $1 AND name = $2 FOR UPDATE`, repo.ID, name); err != nil {
				return err
			}
			if err := f.r.items.RemoveIn(ctx, tx, items.RemoveSpec{
				Repo: items.RepoOf(repo), Paths: paths, Actor: audit.Actor{Kind: "system", Name: "removal"},
			}); err != nil {
				return err
			}
			close(locked)
			<-release
			return nil
		})
	}()
	return locked, release, done
}

func waitForALockWait(t *testing.T, f *ociFixture) {
	t.Helper()
	waitFor(t, "a backend waiting for a lock", 30*time.Second, func() bool {
		return f.count(`SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND datname = current_database()`) > 0
	})
	t.Logf("waiting query: %s", f.e.text(`SELECT COALESCE(string_agg(left(query, 120), ' | '), '') FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND datname = current_database()`))
}

// The sweep holds the package lock and has removed the layer; a real manifest push that passed its pre-check outside the transaction
// waits at EnsureLocked and must then find the layer gone (MANIFEST_BLOB_UNKNOWN), never commit a manifest row naming it.
func TestR_OCI_013_ARemovalHoldingThePackageLockRefusesAPushThatNamesTheRemovedLayer(t *testing.T) {
	f := newOCIFixture(t)
	cfg, layer := f.blob("race", []byte("{}")), f.blob("race", []byte("layer"))
	layerPath := oci.BlobPath("race", layer)
	locked, release, done := removalHoldingTheLock(t, f, "race", layerPath)
	<-locked
	m := ociManifest(cfg, layer)
	resCh := make(chan response, 1)
	go func() { resCh <- f.push("writer", "race", "1", m, ociCT) }()
	waitForALockWait(t, f)
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	res := <-resCh
	rows := f.count(`SELECT count(*) FROM oci_manifests WHERE name = 'race'`)
	refs := f.count(`SELECT count(*) FROM oci_references WHERE name = 'race'`)
	layers := f.count(`SELECT count(*) FROM items WHERE path = $1 AND trashed_at IS NULL`, layerPath)
	t.Logf("push: %d %s; manifest rows %d, references %d, layer items %d", res.status, res.body, rows, refs, layers)
	if res.status/100 == 2 || rows != 0 || refs != 0 {
		t.Fatalf("a manifest naming a swept layer was committed")
	}
}

// A tag push that links to a stored manifest races a DELETE of that manifest by digest (simulated with the real item store and hook under
// the package lock). The tag push passed its Stat and its Link source lookup before the delete committed.
func TestR_OCI_013_ATagLinkedToAManifestThatIsRemovedMeanwhileCommitsTheManifestWithIt(t *testing.T) {
	f := newOCIFixture(t)
	cfg, layer := f.blob("race2", []byte("{}")), f.blob("race2", []byte("layer2"))
	m := ociManifest(cfg, layer)
	d := shaOf(m)
	wantOCI(t, f.push("writer", "race2", d, m, ociCT), 201, "")
	mPath := oci.ManifestPath("race2", d)
	locked, release, done := removalHoldingTheLock(t, f, "race2", mPath)
	<-locked
	resCh := make(chan response, 1)
	go func() { resCh <- f.push("writer", "race2", "1", m, ociCT) }()
	waitForALockWait(t, f)
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	res := <-resCh
	tags := f.count(`SELECT count(*) FROM items WHERE path = $1 AND trashed_at IS NULL`, oci.TagPath("race2", "1"))
	manifests := f.count(`SELECT count(*) FROM items WHERE path = $1 AND trashed_at IS NULL`, mPath)
	rows := f.count(`SELECT count(*) FROM oci_manifests WHERE name = 'race2'`)
	versions := f.count(`SELECT count(*) FROM package_versions pv JOIN packages p ON p.id = pv.package_id WHERE p.name = 'race2' AND pv.trashed_at IS NULL`)
	t.Logf("tag push: %d %s; tag items %d, manifest items %d, manifest rows %d, live versions %d", res.status, res.body, tags, manifests, rows, versions)
	// The removal won: the push either answers an error and leaves no tag, or commits the manifest with the tag; a tag that points at
	// a manifest with no item and no row is the one outcome that is wrong.
	if res.status/100 == 2 && (tags != 1 || manifests != 1 || rows != 1 || versions != 1) {
		t.Errorf("DANGLING: tag items %d, manifest items %d, rows %d, versions %d after a push that answered %d", tags, manifests, rows, versions, res.status)
	}
	if res.status/100 != 2 && tags != 0 {
		t.Errorf("a refused push left a tag")
	}
	if res.status/100 == 2 {
		wantOCI(t, f.pull("reader", "race2", "1"), 200, "")
		wantOCI(t, f.pull("reader", "race2", d), 200, "")
	}
}
