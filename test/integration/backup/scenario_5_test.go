//go:build integration

package backup

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/create"
	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/backup/retention"
)

// TestR_BAK_029_IncrementalAndRetention is scenario 5 of R-BAK-029, the criteria of R-BAK-006 and R-BAK-016 through the
// create pipeline with its pruning stage: a second backup with 10 new files copies exactly 10 blobs and references all of the
// source's; with keep_last 2 the run that creates the third backup deletes the first, B and C verify clean, and the blob that only
// the first referenced is gone from the pool.
func TestR_BAK_029_IncrementalAndRetention(t *testing.T) {
	in := newInstall(t)
	realCanary(t, in)
	ctx := context.Background()
	r := in.newRig(t)
	r.cfg.Retention.KeepLast = 2
	r.mutate = func(d *create.Deps) {
		d.Prune = &retention.Stage{
			Store: r.tgt, Layout: format.Layout{Prefix: r.cfg.Target.Prefix}, Policy: retention.Policy{KeepLast: 2},
			SweepInterval: time.Hour, Facts: d.Facts, Audit: &opsAudit{}, Clock: retention.RunSweepClock{Runs: d.Store.(backup.RunReader)},
		}
	}

	for i := range 20 {
		in.mustUpload(in.repo, fmt.Sprintf("a/%02d.bin", i), fmt.Sprintf("scenario five a %d", i))
	}
	onlyA := in.mustUpload(in.repo, "only-a.bin", "referenced by the first backup only")
	a, err := in.run(ctx, r, backup.CreateOptions{})
	if err != nil {
		t.Fatalf("first backup: %v", err)
	}
	if a.BlobsCopied != 21 {
		t.Fatalf("the first backup copied %d blobs, want 21", a.BlobsCopied)
	}
	in.deleteItem("only-a.bin")

	for i := range 10 {
		in.mustUpload(in.repo, fmt.Sprintf("b/%02d.bin", i), fmt.Sprintf("scenario five b %d", i))
	}
	time.Sleep(1100 * time.Millisecond) // backup IDs order by their second
	b, err := in.run(ctx, r, backup.CreateOptions{})
	if err != nil {
		t.Fatalf("second backup: %v", err)
	}
	if b.BlobsCopied != 10 {
		t.Fatalf("R-BAK-006: the second backup copied %d blobs, want exactly the 10 new ones", b.BlobsCopied)
	}
	mb := manifestOf(t, r.tgt, r.cfg, b.BackupID)
	if want := in.number(`SELECT count(DISTINCT blob_sha256) FROM trove.items WHERE blob_sha256 IS NOT NULL`); mb.Blobs.Referenced != want {
		t.Fatalf("R-BAK-006: referenced = %d, the source references %d", mb.Blobs.Referenced, want)
	}
	if !poolHas(t, r.tgt, r.cfg, fmtDigest(onlyA)) {
		t.Fatal("two backups are kept: the pool still holds the blob of the first")
	}

	in.mustUpload(in.repo, "c.bin", "scenario five c")
	time.Sleep(1100 * time.Millisecond)
	c, err := in.run(ctx, r, backup.CreateOptions{})
	if err != nil {
		t.Fatalf("third backup: %v", err)
	}
	if got := len(listManifests(t, r)); got != 2 {
		t.Fatalf("R-BAK-016: %d backups after the third run with keep_last 2, want 2", got)
	}
	for _, id := range []string{b.BackupID, c.BackupID} {
		if code := deepVerify(t, r, id); code != 0 {
			t.Fatalf("verify --deep %s exits %d", id, code)
		}
	}
	if poolHas(t, r.tgt, r.cfg, fmtDigest(onlyA)) {
		t.Fatal("R-BAK-017: the blob only the deleted backup referenced is still in the pool")
	}
	if !poolHas(t, r.tgt, r.cfg, fmtDigest(digestOf([]byte("scenario five a 0")))) {
		t.Fatal("a blob the kept backups reference left the pool")
	}
}
