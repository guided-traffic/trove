//go:build integration

package uploads

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/config"
)

// A run takes a bounded number of pages of the staging area and saves where it stopped, so that live sessions at the start of the listing
// do not keep the later directories from ever being reached (R-STOR-011 Expiry).
func TestR_STOR_011_SuccessiveSweepRunsProgressThroughTheStagingArea(t *testing.T) {
	w := newWorld(t)
	r := newReplica(t, w.e, w.p, nil)
	ctx := context.Background()
	up := config.StorageUploads{SessionTTL: time.Hour, SessionMaxAge: 20 * time.Second}
	// a directory that sorts after every session of the test
	ghost := "ffffffff-ffff-4fff-8fff-ffffffffffff"
	if err := w.e.raw.Put(ctx, blob.SessionKey(w.e.prefix, ghost, "tail-x"), bytes.NewReader([]byte("x")), 1, blob.Checksum{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(21 * time.Second)
	for range 4 {
		s, err := r.svc.Start(ctx, w.p.repo, w.sc)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.append(w.sc, s.ID, 0, random(10)); err != nil {
			t.Fatal(err)
		}
	}
	run := func() {
		t.Helper()
		c := blob.NewCollector(r.cfg.GC, r.cfg.S3.Prefix, w.e.raw, w.p.db, blob.WithCollectorLogger(quiet), blob.WithUploadSessions(up), blob.WithSessionStepLimits(1, 2))
		if _, err := c.Run(ctx, blob.RunOptions{Mode: blob.GCFull}); err != nil {
			t.Fatal(err)
		}
	}
	run() // two live directories
	if k := w.e.keys(t, blob.SessionPrefix(w.e.prefix, ghost)); len(k) == 0 {
		t.Fatal("the first run reached the ghost, so the test proves nothing")
	}
	for range 4 {
		run()
		if k := w.e.keys(t, blob.SessionPrefix(w.e.prefix, ghost)); len(k) == 0 {
			return
		}
	}
	t.Fatal("five runs did not reach the directory behind four live sessions")
}
