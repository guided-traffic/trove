//go:build integration

package blob

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/guided-traffic/trove/internal/blob"
)

// invariant fails if a committed reference points at a blob that is not live
// with its object in the bucket (R-STOR-005, R-STOR-024).
func invariant(tb testing.TB, e *s3Env, p *pgEnv, referenced []blob.Digest) {
	tb.Helper()
	for _, d := range referenced {
		state, status, ok := p.state(tb, d)
		if !ok || state != "live" || status != "ok" {
			tb.Fatalf("referenced blob %s is %q/%q (row %v)", d, state, status, ok)
		}
		if !objectExists(tb, e, e.prefix, d) {
			tb.Fatalf("referenced blob %s has no object", d)
		}
	}
}

// refSet is the digests with a committed reference, safe for concurrent use.
type refSet struct {
	mu sync.Mutex
	m  map[blob.Digest]bool
}

func (s *refSet) add(d blob.Digest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[blob.Digest]bool{}
	}
	s.m[d] = true
}

func (s *refSet) list() []blob.Digest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]blob.Digest, 0, len(s.m))
	for d := range s.m {
		out = append(out, d)
	}
	return out
}

func uploadRetrying(ctx context.Context, r *replica, path string, data []byte) (blob.Digest, error) {
	for {
		_, d, err := r.upload(ctx, path, data)
		if busy(err) && ctx.Err() == nil {
			continue
		}
		return d, err
	}
}

// TestGCKilledAtRandomPoints kills the collector at random points of its run
// (by an error that ends it without any bookkeeping, as a process kill does, or
// by cancelling it, as a drain does), starts a new one, and checks after every
// round that no referenced blob was deleted. At the end the garbage must be gone.
func TestGCKilledAtRandomPoints(t *testing.T) {
	t.Run("R-STOR-024 a collector killed at random points never deletes a referenced blob and the garbage eventually goes", func(t *testing.T) {
		ctx := context.Background()
		e := newS3(t, "gckill")
		p := newPG(t)
		up := newReplica(t, e, p, nil)

		var refs refSet
		for i := 0; i < 10; i++ {
			d, err := uploadRetrying(ctx, up, fmt.Sprintf("keep/%d", i), randomBytes(1500+i))
			if err != nil {
				t.Fatal(err)
			}
			refs.add(d)
		}
		type garbageBlob struct {
			digest blob.Digest
			data   []byte
		}
		var gb []garbageBlob
		for i := 0; i < 30; i++ {
			d, data := garbage(t, up, fmt.Sprintf("trash/%d", i))
			gb = append(gb, garbageBlob{d, data})
		}

		// Some garbage comes back while the collectors are being killed: the same
		// content uploaded again must find a live row or a gone row, never a
		// reference to a blob whose object a collector deleted.
		stop := make(chan struct{})
		var bg sync.WaitGroup
		bg.Add(1)
		go func() {
			defer bg.Done()
			rng := rand.New(rand.NewPCG(7, 7))
			for n := 0; ; n++ {
				select {
				case <-stop:
					return
				case <-time.After(time.Duration(20+rng.IntN(60)) * time.Millisecond):
				}
				g := gb[rng.IntN(len(gb))]
				d, err := uploadRetrying(ctx, up, fmt.Sprintf("again/%d", n), g.data)
				if err != nil {
					t.Errorf("re-upload: %v", err)
					return
				}
				refs.add(d)
			}
		}()

		rng := rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 1))
		killed, cancelled, completed := 0, 0, 0
		var later []blob.Digest // garbage made during the rounds, so that every round has work
		for round := 0; round < 40; round++ {
			for i := 0; i < 6; i++ {
				d, _ := garbage(t, up, fmt.Sprintf("later/%d/%d", round, i))
				later = append(later, d)
			}
			cfg := up.cfg
			cfg.GC.BatchDelete = rng.IntN(2) == 0
			// A fresh collector is a fresh process: it knows none of its predecessor's claims.
			c := blob.NewCollector(cfg.GC, cfg.S3.Prefix, up.obj, p.db, blob.WithCollectorLogger(quiet))
			rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			stopAt, mode := 1+rng.IntN(30), rng.IntN(3)
			var calls atomic.Int32
			c.Fault = func(point string) error {
				if int(calls.Add(1)) != stopAt {
					return nil
				}
				switch mode {
				case 0:
					killed++
					return errors.New("killed at " + point)
				case 1:
					cancelled++
					cancel()
				}
				return nil
			}
			mo := blob.GCIncremental
			if rng.IntN(3) == 0 {
				mo = blob.GCFull
			}
			_, _ = c.Run(rctx, blob.RunOptions{Mode: mo})
			cancel()
			if mode == 2 || int(calls.Load()) < stopAt {
				completed++
			}
			invariant(t, e, p, refs.list())
		}
		close(stop)
		bg.Wait()
		t.Logf("40 rounds: %d killed, %d cancelled, %d ran to the end", killed, cancelled, completed)
		if killed+cancelled < 15 {
			t.Fatalf("only %d of 40 runs were stopped: the test does not exercise the recovery", killed+cancelled)
		}

		// Let the last rows be finished by collectors that are not killed.
		deadline := time.Now().Add(60 * time.Second)
		for {
			run(t, up, blob.RunOptions{Mode: blob.GCFull})
			run(t, up, blob.RunOptions{Mode: blob.GCIncremental})
			garbageLeft := p.count(t, `blobs b WHERE NOT EXISTS (SELECT 1 FROM items i WHERE i.blob_sha256 = b.sha256)`)
			if garbageLeft == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%d unreferenced blob rows remain after 60 s", garbageLeft)
			}
			time.Sleep(300 * time.Millisecond)
		}
		invariant(t, e, p, refs.list())
		referenced := map[blob.Digest]bool{}
		for _, d := range refs.list() {
			referenced[d] = true
		}
		for _, g := range gb {
			if referenced[g.digest] {
				continue
			}
			if !gone(t, e, p, g.digest) {
				t.Errorf("garbage %s is still there", g.digest)
			}
		}
		for _, d := range later {
			if !gone(t, e, p, d) {
				t.Errorf("garbage %s is still there", d)
			}
		}
		if n := len(e.keys(t)); n != int(p.count(t, "blobs")) {
			t.Fatalf("%d objects in the bucket, %d blob rows", n, p.count(t, "blobs"))
		}
	})
}

// TestRaceWithDeletesAndCollection is the race test of R-STOR-005 with
// collection running: uploads of the same content to random paths, deletes of
// those items and collection with grace_period 0s in a loop, on several
// replicas. It runs TROVE_TEST_BLOB_RACE_SECONDS seconds (default 15; the
// specification's 10 minutes are for a soak run).
func TestRaceWithDeletesAndCollection(t *testing.T) {
	t.Run("R-STOR-005 uploads, deletes and collection with grace_period 0s leave every item's blob live with its object", func(t *testing.T) {
		secs := 15
		if v, err := strconv.Atoi(os.Getenv("TROVE_TEST_BLOB_RACE_SECONDS")); err == nil && v > 0 {
			secs = v
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(secs)*time.Second)
		defer cancel()
		e := newS3(t, "race-gc")
		p := newPG(t)
		const replicas = 3
		var rs []*replica
		for i := 0; i < replicas; i++ {
			rs = append(rs, newReplica(t, e, p, nil))
		}
		contents := make([][]byte, 5)
		for i := range contents {
			contents[i] = randomBytes(30000 + i)
		}

		var uploads, deletes, runs, conflicts atomic.Int64
		var wg sync.WaitGroup
		fatal := make(chan error, 64)
		for i := 0; i < 12; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				rng := rand.New(rand.NewPCG(uint64(i), 99))
				for ctx.Err() == nil {
					path := fmt.Sprintf("r/%d", rng.IntN(150))
					_, _, err := rs[i%replicas].upload(context.Background(), path, contents[rng.IntN(len(contents))])
					var pe *pgconn.PgError
					switch {
					case err == nil:
						uploads.Add(1)
					case errors.As(err, &pe) && pe.Code == "23505": // the path is taken: an overwrite conflict, not our subject
						conflicts.Add(1)
					case busy(err):
					default:
						fatal <- err
						return
					}
				}
			}(i)
		}
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				rng := rand.New(rand.NewPCG(uint64(100+i), 99))
				for ctx.Err() == nil {
					tag, err := p.db.Main().Exec(context.Background(), "DELETE FROM items WHERE path = $1", fmt.Sprintf("r/%d", rng.IntN(150)))
					if err != nil {
						fatal <- err
						return
					}
					deletes.Add(tag.RowsAffected())
					time.Sleep(time.Duration(rng.IntN(15)) * time.Millisecond)
				}
			}(i)
		}
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				for n := 0; ctx.Err() == nil; n++ {
					mode := blob.GCIncremental
					if n%4 == 3 {
						mode = blob.GCFull
					}
					cctx, ccancel := context.WithTimeout(context.Background(), time.Minute)
					_, err := rs[i].coll.Run(cctx, blob.RunOptions{Mode: mode})
					ccancel()
					if err != nil {
						fatal <- err
						return
					}
					runs.Add(1)
				}
			}(i)
		}
		wg.Wait()
		close(fatal)
		for err := range fatal {
			t.Fatal(err)
		}
		t.Logf("%d uploads, %d deletes, %d collection runs, %d path conflicts in %d s", uploads.Load(), deletes.Load(), runs.Load(), conflicts.Load(), secs)
		if uploads.Load() == 0 || runs.Load() == 0 {
			t.Fatal("the test did nothing")
		}

		// Every item's blob is live and its object downloads with the right digest.
		rows, err := p.admin.Query(context.Background(), "SELECT DISTINCT blob_sha256, size FROM items")
		if err != nil {
			t.Fatal(err)
		}
		type ref struct {
			d    blob.Digest
			size int64
		}
		var items []ref
		for rows.Next() {
			var sha []byte
			var r ref
			if err := rows.Scan(&sha, &r.size); err != nil {
				t.Fatal(err)
			}
			copy(r.d[:], sha)
			items = append(items, r)
		}
		rows.Close()
		for _, it := range items {
			if state, status, ok := p.state(t, it.d); !ok || state != "live" || status != "ok" {
				t.Fatalf("an item references blob %s which is %q/%q (row %v)", it.d, state, status, ok)
			}
			body, err := rs[0].store.Open(context.Background(), it.d, it.size, blob.OpenOptions{})
			if err != nil {
				t.Fatalf("blob %s: %v", it.d, err)
			}
			got, err := io.ReadAll(body)
			body.Close()
			if err != nil || sha256.Sum256(got) != [32]byte(it.d) {
				t.Fatalf("blob %s does not download intact: %v", it.d, err)
			}
		}
		for _, r := range rs {
			r.spoolEmpty(t)
		}
	})
}

// TestUploadMeetsDeletingRow: an upload of content whose row a crashed collector
// left in state deleting waits for the row to go and then writes the object again.
func TestUploadMeetsDeletingRow(t *testing.T) {
	t.Run("R-STOR-024 an upload that finds the row deleting succeeds after the collection finishes and the object exists afterwards", func(t *testing.T) {
		ctx := context.Background()
		e := newS3(t, "deleting")
		p := newPG(t)
		r := newReplica(t, e, p, nil)
		d, data := garbage(t, r, "old.bin")

		r.coll.Fault = func(point string) error {
			if point == "claim:committed" {
				return errors.New("killed after the claim")
			}
			return nil
		}
		if _, err := r.coll.Run(ctx, blob.RunOptions{Mode: blob.GCIncremental}); !errors.Is(err, blob.ErrFault) {
			t.Fatalf("got %v", err)
		}
		if state, _, _ := p.state(t, d); state != "deleting" {
			t.Fatalf("the row is %s, want deleting", state)
		}

		type result struct {
			res blob.CommitResult
			err error
			at  time.Duration
		}
		start := time.Now()
		out := make(chan result, 1)
		go func() {
			res, _, err := r.upload(ctx, "again.bin", data)
			out <- result{res, err, time.Since(start)}
		}()
		select {
		case got := <-out:
			t.Fatalf("the upload finished while the row was deleting: %+v", got)
		case <-time.After(500 * time.Millisecond):
		}
		// A healthy collector finishes the row once handover_delay has passed.
		c2 := blob.NewCollector(r.cfg.GC, r.cfg.S3.Prefix, r.obj, p.db, blob.WithCollectorLogger(quiet))
		for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(200 * time.Millisecond) {
			if _, err := c2.Run(ctx, blob.RunOptions{Mode: blob.GCIncremental}); err != nil {
				t.Fatal(err)
			}
			if _, _, ok := p.state(t, d); !ok || time.Now().After(deadline) {
				break
			}
		}
		got := <-out
		if got.err != nil || !got.res.Wrote || !got.res.Inserted {
			t.Fatalf("%+v", got)
		}
		if state, status, ok := p.state(t, d); !ok || state != "live" || status != "ok" || !objectExists(t, e, e.prefix, d) {
			t.Fatalf("after the upload: %s/%s row %v", state, status, ok)
		}
	})
}

func TestDownloadVerificationOnTheStore(t *testing.T) {
	t.Run("Q-STOR-6 an object overwritten with other bytes of the same length is detected, marked corrupt and repaired by the next upload", func(t *testing.T) {
		ctx := context.Background()
		e := newS3(t, "verify")
		p := newPG(t)
		r := newReplica(t, e, p, nil)
		data := randomBytes(2 << 20)
		_, d, err := r.upload(ctx, "v.bin", data)
		if err != nil {
			t.Fatal(err)
		}
		bad := bytes.Clone(data)
		bad[len(bad)/3] ^= 0xff
		if err := e.raw.Put(ctx, blob.Key(e.prefix, d), bytes.NewReader(bad), int64(len(bad)), blob.Checksum{}); err != nil {
			t.Fatal(err)
		}
		body, err := r.store.Open(ctx, d, int64(len(data)), blob.OpenOptions{})
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(body)
		body.Close()
		if !errors.Is(err, blob.ErrCorrupt) {
			t.Fatalf("the download of a corrupted object ended with %v", err)
		}
		if len(got) >= len(data) {
			t.Fatalf("a complete corrupted file was delivered (%d bytes)", len(got))
		}
		if _, status, _ := p.state(t, d); status != "corrupt" {
			t.Fatalf("content_status = %s", status)
		}
		// R-STOR-005 repair: the same content again rewrites the object.
		res, _, err := r.upload(ctx, "v2.bin", data)
		if err != nil || !res.Wrote {
			t.Fatalf("repair upload: %+v %v", res, err)
		}
		if _, status, _ := p.state(t, d); status != "ok" {
			t.Fatalf("content_status after the repair = %s", status)
		}
		body, err = r.store.Open(ctx, d, int64(len(data)), blob.OpenOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if all, err := io.ReadAll(body); err != nil || !bytes.Equal(all, data) {
			t.Fatalf("download after the repair: %v", err)
		}
		body.Close()
	})
}

// The blob tier needs no Valkey. The tests above read no TROVE_TEST_VALKEY_*
// variable; this one checks that the code under test cannot reach the client.
func TestNoValkeyDependency(t *testing.T) {
	t.Run("R-STOR-046 nothing below internal/blob depends on the Valkey client or the cluster package", func(t *testing.T) {
		out, err := exec.Command("go", "list", "-deps", "github.com/guided-traffic/trove/internal/blob/...").CombinedOutput()
		if err != nil {
			t.Fatalf("go list: %v\n%s", err, out)
		}
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, "valkey") || strings.HasSuffix(line, "/internal/cluster") {
				t.Errorf("dependency %s", line)
			}
		}
	})
}
