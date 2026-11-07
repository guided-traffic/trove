//go:build integration

package backup

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/backup"
)

// TestR_BAK_029_ConsistencyUnderLoad is scenario 1 of R-BAK-029 without a cluster: a loop publishes files, deletes some, trashes
// some, empties the trash and runs garbage collection with grace_period 0s, while `create` runs. The backup is restored into a
// fresh database and bucket. Every file item of the restored database, trashed or not, then has an object whose SHA-256 is its
// digest, and `verify --deep` exits 0. The cluster drill runs the same scenario with npm and NuGet packages
// (test/e2e/backup_drill_test.go).
func TestR_BAK_029_ConsistencyUnderLoad(t *testing.T) {
	in := newInstall(t)
	realCanary(t, in)
	ctx := context.Background()
	for i := range 300 {
		in.mustUpload(in.repo, fmt.Sprintf("seed/%03d.bin", i), fmt.Sprintf("seed content %d", i))
	}

	r := in.newRig(t)
	r.cfg.Copy.Concurrency = 1 // a copy that takes a while, so that the loop runs during it
	var (
		wg          sync.WaitGroup
		stop        = make(chan struct{})
		published   int
		deleted     int
		trashed     int
		collections int
		failure     error
		mu          sync.Mutex
	)
	fail := func(err error) {
		mu.Lock()
		if failure == nil {
			failure = err
		}
		mu.Unlock()
	}
	rng := rand.New(rand.NewPCG(7, 11))
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			path := fmt.Sprintf("live/%05d.bin", i)
			if _, err := in.upload(ctx, in.repo, path, []byte(fmt.Sprintf("loop content %d %d", i, rng.IntN(1<<30)))); err != nil {
				fail(fmt.Errorf("publish %s: %w", path, err))
				return
			}
			mu.Lock()
			published++
			mu.Unlock()
			switch i % 3 {
			case 0:
				if _, err := in.admin.Exec(ctx, `DELETE FROM trove.items WHERE id = (SELECT id FROM trove.items WHERE trashed_at IS NULL ORDER BY random() LIMIT 1)`); err != nil {
					fail(err)
					return
				}
				deleted++
			case 1:
				if _, err := in.admin.Exec(ctx, `UPDATE trove.items SET trashed_at = now() WHERE id = (SELECT id FROM trove.items WHERE trashed_at IS NULL ORDER BY random() LIMIT 1)`); err != nil {
					fail(err)
					return
				}
				trashed++
			}
			if i%15 == 14 {
				if _, err := in.admin.Exec(ctx, `DELETE FROM trove.items WHERE trashed_at IS NOT NULL AND random() < 0.5`); err != nil {
					fail(err)
					return
				}
			}
			if i%4 == 3 {
				if _, err := in.collect(ctx); err != nil {
					fail(fmt.Errorf("collect: %w", err))
					return
				}
				mu.Lock()
				collections++
				mu.Unlock()
			}
			time.Sleep(15 * time.Millisecond)
		}
	}()

	time.Sleep(500 * time.Millisecond) // the loop runs before the backup starts
	mu.Lock()
	before := [2]int{published, collections}
	mu.Unlock()
	res, err := in.run(ctx, r, backup.CreateOptions{NoPrune: true})
	mu.Lock()
	during := [2]int{published - before[0], collections - before[1]}
	mu.Unlock()
	close(stop)
	wg.Wait()
	if failure != nil {
		t.Fatal(failure)
	}
	if err != nil {
		t.Fatalf("create under load: %v", err)
	}
	t.Logf("during the run: %d publishes, %d collections", during[0], during[1])
	t.Logf("loop: %d published, %d deleted, %d trashed, %d collections; backup %s copied %d blobs", published, deleted, trashed, collections, res.BackupID, res.BlobsCopied)
	if during[1] < 2 || during[0] < 8 {
		t.Fatalf("the load did not run during the backup: %d publishes, %d collections", during[0], during[1])
	}

	src := in.sourceOf(t, r, res.BackupID)
	dst := src.destination()
	if _, err := dst.restore(backup.RestoreOptions{SkipRevocationJournal: true}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	n := downloadsMatch(t, dst)
	if n == 0 {
		t.Fatal("the restored database has no file item")
	}
	t.Logf("%d distinct blobs of the restored items match their digests", n)
	if code := deepVerify(t, r); code != 0 {
		t.Fatalf("verify --deep exits %d, want 0", code)
	}
}
