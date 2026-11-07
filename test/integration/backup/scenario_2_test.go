//go:build integration

package backup

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/blob"
)

// TestR_BAK_029_SourceBlobsCollectedAfterTheBackup is scenario 2 of R-BAK-029: after backup B every item is deleted and a full
// garbage collection with grace 0s runs, so that the source bucket holds none of B's blobs. Restoring B still succeeds, and the
// download check of scenario 1 passes: the pool, not the source, is what a restore reads.
func TestR_BAK_029_SourceBlobsCollectedAfterTheBackup(t *testing.T) {
	in := newInstall(t)
	realCanary(t, in)
	ctx := context.Background()
	var digests []blob.Digest
	for i := range 30 {
		digests = append(digests, in.mustUpload(in.repo, fmt.Sprintf("pkg/%02d.bin", i), fmt.Sprintf("scenario two content %d", i)))
	}
	r := in.newRig(t)
	res, err := in.run(ctx, r, backup.CreateOptions{NoPrune: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	in.exec(`DELETE FROM trove.items`)
	for _, d := range digests {
		in.waitGone(d)
	}
	for _, d := range digests {
		h := d.String()
		key := in.prefix + "/sha256/" + h[0:2] + "/" + h[2:4] + "/" + h
		deadline := time.Now().Add(30 * time.Second)
		for {
			rc, _, err := in.client.Get(ctx, key, nil)
			if err != nil {
				break
			}
			_ = rc.Close()
			if time.Now().After(deadline) {
				t.Fatalf("the source bucket still holds %s", key)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}

	dst := in.sourceOf(t, r, res.BackupID).destination()
	if _, err := dst.restore(backup.RestoreOptions{SkipRevocationJournal: true}); err != nil {
		t.Fatalf("restoring B after the source lost its blobs: %v", err)
	}
	if n := downloadsMatch(t, dst); n != len(digests) {
		t.Fatalf("%d blobs match, want %d", n, len(digests))
	}
	if code := deepVerify(t, r); code != 0 {
		t.Fatalf("verify --deep exits %d, want 0", code)
	}
}
