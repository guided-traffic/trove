//go:build integration

package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/create"
	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/backup/target/targettest"
	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/config"
)

// waitGone waits until the blob row is gone (collected and settled).
func (in *install) waitGone(d blob.Digest) {
	in.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := in.blobState(d); !ok {
			return
		}
		if _, err := in.collect(context.Background()); err != nil {
			in.t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	st, _ := in.blobState(d)
	in.t.Fatalf("blob %s is still %q after 30 s", d, st)
}

// TestR_BAK_007_AHeldBlobIsNotCollectedUntilTheRunHasCopiedIt is the first and second criterion: with a grace period of 0 and a
// pause after step 4, a blob whose only reference is gone stays live, trove_gc_candidates_held_total counts it, and the run
// completes; the first collection after the run deletes it. With a pause between steps 1 and 3 a collection run deletes nothing.
func TestR_BAK_007_AHeldBlobIsNotCollectedUntilTheRunHasCopiedIt(t *testing.T) {
	t.Run("pinned: the blob stays live, the run completes, the next collection deletes it", func(t *testing.T) {
		in := newInstall(t)
		x := in.mustUpload(in.repo, "x.bin", "only-blob")
		in.mustUpload(in.repo, "keep.bin", "keeper")
		r := in.newRig(t)
		var held int
		r.mutate = func(d *create.Deps) {
			d.Fault = func(ctx context.Context, point string) error {
				if point != create.PointHoldPinned {
					return nil
				}
				in.deleteItem("x.bin")
				res, err := in.coll.Run(ctx, blob.RunOptions{Mode: blob.GCIncremental})
				if err != nil {
					return err
				}
				held = res.Held
				if res.Deleted != 0 || res.HoldSkipped {
					return fmt.Errorf("collection result %+v: a pinned hold keeps the pinned digest only", res)
				}
				return nil
			}
		}
		res, err := in.run(context.Background(), r, backup.CreateOptions{NoPrune: true})
		if err != nil {
			t.Fatal(err)
		}
		if held != 1 {
			t.Fatalf("held = %d, want 1", held)
		}
		if !poolHas(t, r.tgt, r.cfg, fmtDigest(x)) || res.BlobsCopied != 2 {
			t.Fatalf("the held blob must be in the pool: copied %d", res.BlobsCopied)
		}
		in.waitGone(x)
	})
	t.Run("all: a collection run between steps 1 and 3 deletes no blob at all", func(t *testing.T) {
		in := newInstall(t)
		in.mustUpload(in.repo, "x.bin", "only-blob")
		garbage := in.mustUpload(in.repo, "g.bin", "garbage-before")
		in.deleteItem("g.bin") // unreferenced before the run: not in the snapshot, nothing pins it
		r := in.newRig(t)
		var skipped bool
		r.mutate = func(d *create.Deps) {
			d.Fault = func(ctx context.Context, point string) error {
				if point != create.PointPinsCommitted {
					return nil
				}
				if got := scalarText(in, `SELECT hold FROM trove.backup_run`); got != "all" {
					return fmt.Errorf("hold = %s, want all", got)
				}
				res, err := in.collect(ctx)
				skipped = res.HoldSkipped
				if err == nil && res.Deleted != 0 {
					err = fmt.Errorf("deleted %d blobs under a global hold", res.Deleted)
				}
				return err
			}
		}
		if _, err := in.run(context.Background(), r, backup.CreateOptions{NoPrune: true}); err != nil {
			t.Fatal(err)
		}
		if !skipped {
			t.Fatal("the collection run must end at once under a valid 'all' hold")
		}
		if _, ok := in.blobState(garbage); !ok {
			t.Fatal("the unreferenced blob was collected under the hold")
		}
		in.waitGone(garbage)
	})
}

// TestR_BAK_007_AStalledRunLosesItsHoldAndFailsWithHoldLost: a run whose lease expired no longer holds anything back; the
// collector deletes a pinned, unreferenced blob, and the run, once it moves again, fails with hold_lost and writes no manifest.
func TestR_BAK_007_AStalledRunLosesItsHoldAndFailsWithHoldLost(t *testing.T) {
	in := newInstall(t)
	x := in.mustUpload(in.repo, "x.bin", "only-blob")
	r := in.newRig(t)
	r.cfg.LeaseDuration = 3 * time.Second
	r.cfg.HeartbeatInterval = 300 * time.Millisecond
	r.cfg.FinalizeTimeout = 500 * time.Millisecond
	r.mutate = func(d *create.Deps) {
		d.Fault = func(ctx context.Context, point string) error {
			if point != create.PointHoldPinned {
				return nil
			}
			// kill -STOP: nothing moves, so the lease is not renewed.
			in.deleteItem("x.bin")
			time.Sleep(r.cfg.LeaseDuration + 800*time.Millisecond)
			in.waitGone(x)
			return nil
		}
	}
	_, err := in.run(context.Background(), r, backup.CreateOptions{NoPrune: true})
	if exit(err) != backup.ExitFailed || backup.ErrorCode(err) != backup.CodeHoldLost {
		t.Fatalf("err = %v (exit %d, %s), want hold_lost", err, exit(err), backup.ErrorCode(err))
	}
	if keys := listManifests(t, r); len(keys) != 0 {
		t.Fatalf("a manifest exists: %v", keys)
	}
	if st := scalarText(in, `SELECT state FROM trove.backup_run`); st != "failed" {
		t.Fatalf("state = %s", st)
	}
}

// TestR_BAK_007_ARandomInterleavingNeverLosesABlobOfACompletedBackup is the property test of the hold protocol: uploads (including
// re-uploads of content that was just deleted), deletes, collections with a grace period of 0 and a series of runs interleave
// at random, with random pauses at every step of the protocol. A completed backup never lacks a listed, undamaged digest in its
// pool, and no run fails with hold_lost while its lease was valid (the lease is a minute; the runs last seconds).
func TestR_BAK_007_ARandomInterleavingNeverLosesABlobOfACompletedBackup(t *testing.T) {
	seed := uint64(time.Now().UnixNano())
	t.Logf("seed %d", seed)
	in := newInstall(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	contents := make([][]byte, 14)
	for i := range contents {
		contents[i] = bytes.Repeat([]byte{byte('a' + i)}, 700+i)
	}
	var mu sync.Mutex
	live := map[string]bool{}
	var seq atomic.Int64
	for i := 0; i < 6; i++ {
		p := fmt.Sprintf("seed/%d", i)
		in.mustUpload(in.repo, p, string(contents[i]))
		live[p] = true
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	worker := func(name string, fn func(rng *rand.Rand)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rng := rand.New(rand.NewPCG(seed, uint64(len(name))+uint64(seq.Add(1))))
			for {
				select {
				case <-stop:
					return
				case <-time.After(time.Duration(rng.IntN(25)) * time.Millisecond):
				}
				fn(rng)
			}
		}()
	}
	for i := 0; i < 2; i++ {
		worker("upload", func(rng *rand.Rand) {
			p := fmt.Sprintf("u/%d", seq.Add(1))
			for {
				_, err := in.upload(ctx, in.repo, p, contents[rng.IntN(len(contents))])
				if err == nil {
					mu.Lock()
					live[p] = true
					mu.Unlock()
					return
				}
				if !errors.Is(err, blob.ErrBlobBusy) && !errors.Is(err, blob.ErrCommitAttempts) {
					if ctx.Err() == nil {
						t.Errorf("upload: %v", err)
					}
					return
				}
			}
		})
	}
	// Churn: content that is referenced for a few milliseconds and then garbage, so that a blob is in a snapshot and
	// collectable right after it (the case the hold exists for).
	for i := 0; i < 2; i++ {
		worker("churn", func(rng *rand.Rand) {
			p := fmt.Sprintf("c/%d", seq.Add(1))
			data := make([]byte, 600)
			for j := range data {
				data[j] = byte(rng.IntN(256))
			}
			if _, err := in.upload(ctx, in.repo, p, data); err != nil {
				if ctx.Err() == nil && !errors.Is(err, blob.ErrBlobBusy) && !errors.Is(err, blob.ErrCommitAttempts) {
					t.Errorf("churn upload: %v", err)
				}
				return
			}
			time.Sleep(time.Duration(rng.IntN(60)) * time.Millisecond)
			if _, err := in.web.Main().Exec(context.Background(), `DELETE FROM trove.items WHERE path = $1`, p); err != nil && ctx.Err() == nil {
				t.Errorf("churn delete: %v", err)
			}
		})
	}
	worker("delete", func(rng *rand.Rand) {
		mu.Lock()
		var victim string
		for p := range live {
			victim = p
			if rng.IntN(3) == 0 {
				break
			}
		}
		delete(live, victim)
		mu.Unlock()
		if victim != "" {
			if _, err := in.web.Main().Exec(context.Background(), `DELETE FROM trove.items WHERE path = $1`, victim); err != nil && ctx.Err() == nil {
				t.Errorf("delete: %v", err)
			}
		}
	})
	worker("collect", func(rng *rand.Rand) {
		mode := blob.GCIncremental
		if rng.IntN(3) == 0 {
			mode = blob.GCFull
		}
		if _, err := in.coll.Run(ctx, blob.RunOptions{Mode: mode}); err != nil && ctx.Err() == nil {
			t.Errorf("collect: %v", err)
		}
	})

	mem := targettest.New()
	r := in.newRig(t)
	mem.SSE = map[string]string{"s3": "AES256", "none": ""}[r.cfg.Target.SSE.Mode]
	r.tgt = mem
	r.cfg.Copy.ServerSide = "never"
	frng := rand.New(rand.NewPCG(seed, 99))
	r.mutate = func(d *create.Deps) {
		d.Fault = func(_ context.Context, _ string) error {
			time.Sleep(time.Duration(frng.IntN(40)) * time.Millisecond)
			return nil
		}
	}
	completed := 0
	for round := 0; round < 6; round++ {
		res, err := in.run(ctx, r, backup.CreateOptions{NoPrune: true})
		if err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("round %d: the run failed with %v (%s): no run may fail while its lease is valid", round, err, backup.ErrorCode(err))
		}
		completed++
		checkPool(t, mem, r.cfg, res.BackupID)
	}
	close(stop)
	wg.Wait()
	if completed != 6 {
		t.Fatalf("completed %d runs", completed)
	}
}

// checkPool is the safety property: every digest of the blob list that the manifest does not list as damaged is in the pool.
// Nothing damages a source object in this test, so a damaged entry is a failure too.
func checkPool(t *testing.T, mem *targettest.Mem, cfg config.Backup, id string) {
	t.Helper()
	l := format.Layout{Prefix: cfg.Target.Prefix}
	raw, ok := mem.Data(l.Manifest(format.ID(id)))
	if !ok {
		t.Fatalf("no manifest for %s", id)
	}
	m, err := format.DecodeManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Blobs.Damaged) != 0 {
		t.Fatalf("damaged blobs in a run nothing damaged: %+v", m.Blobs.Damaged)
	}
	list, ok := mem.Data(l.BlobList(format.ID(id)))
	if !ok {
		t.Fatalf("no blob list for %s", id)
	}
	rd, err := format.NewBlobListReader(bytes.NewReader(list))
	if err != nil {
		t.Fatal(err)
	}
	defer rd.Close()
	entries, err := format.ReadAll(rd)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(entries)) != m.Blobs.Referenced {
		t.Fatalf("the list has %d entries, the manifest says %d", len(entries), m.Blobs.Referenced)
	}
	for _, e := range entries {
		if _, ok := mem.Data(l.Pool(e.Digest)); !ok {
			t.Fatalf("backup %s lists %s but the pool lacks it", id, e.Digest.Hex())
		}
	}
}
