//go:build integration

package blob

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta"
)

// release removes the item at path; the trigger queues its blob.
func (p *pgEnv) release(tb testing.TB, path string) {
	tb.Helper()
	p.exec(tb, "DELETE FROM items WHERE path = $1", path)
}

// garbage uploads content to a path and deletes the item again: a live blob
// with no reference and a queue entry.
func garbage(tb testing.TB, r *replica, path string) (blob.Digest, []byte) {
	tb.Helper()
	data := randomBytes(2000)
	_, d, err := r.upload(context.Background(), path, data)
	if err != nil {
		tb.Fatal(err)
	}
	r.pg.release(tb, path)
	return d, data
}

func run(tb testing.TB, r *replica, o blob.RunOptions) blob.GCResult {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res, err := r.coll.Run(ctx, o)
	if err != nil {
		tb.Fatalf("collection run: %v (%+v)", err, res)
	}
	return res
}

func gone(tb testing.TB, e *s3Env, p *pgEnv, d blob.Digest) bool {
	tb.Helper()
	_, _, hasRow := p.state(tb, d)
	return !hasRow && !objectExists(tb, e, e.prefix, d)
}

func TestGarbageCollection(t *testing.T) {
	ctx := context.Background()
	setup := func(t *testing.T, mutate func(*config.Storage)) (*s3Env, *pgEnv, *replica) {
		e := newS3(t, "gc")
		p := newPG(t)
		return e, p, newReplica(t, e, p, mutate)
	}

	t.Run("R-STOR-024 an unreferenced blob is deleted, a referenced one is kept, and the queue drains", func(t *testing.T) {
		e, p, r := setup(t, nil)
		g, _ := garbage(t, r, "a/garbage.bin")
		_, kept, err := r.upload(ctx, "a/kept.bin", randomBytes(3000))
		if err != nil {
			t.Fatal(err)
		}
		// A blob that lost one of two references stays.
		shared := randomBytes(1500)
		_, s, _ := r.upload(ctx, "a/shared-1.bin", shared)
		if _, _, err := r.upload(ctx, "a/shared-2.bin", shared); err != nil {
			t.Fatal(err)
		}
		p.release(t, "a/shared-1.bin")

		res := run(t, r, blob.RunOptions{Mode: blob.GCIncremental})
		if res.Deleted != 1 || res.Kept < 1 || res.BytesReclaimed != 2000 || res.Errors != 0 {
			t.Fatalf("%+v", res)
		}
		if !gone(t, e, p, g) {
			t.Fatal("the garbage blob is still there")
		}
		for _, d := range []blob.Digest{kept, s} {
			if state, _, ok := p.state(t, d); !ok || state != "live" || !objectExists(t, e, e.prefix, d) {
				t.Fatalf("a referenced blob was damaged: %s %v", state, ok)
			}
		}
		if p.count(t, "blob_gc_queue") != 0 {
			t.Fatal("the queue still has entries")
		}
		if p.count(t, "blobs WHERE state = 'deleting'") != 0 {
			t.Fatal("a deleting row remains after the run's settle wait")
		}
	})

	t.Run("R-STOR-024 a deleted item's blob stays inside the grace period and goes after it", func(t *testing.T) {
		e, p, r := setup(t, func(c *config.Storage) { c.GC.GracePeriod = 2 * time.Second })
		g, _ := garbage(t, r, "g/x.bin")
		if res := run(t, r, blob.RunOptions{Mode: blob.GCIncremental}); res.Deleted != 0 || res.Candidates != 0 {
			t.Fatalf("inside the grace period: %+v", res)
		}
		if !objectExists(t, e, e.prefix, g) {
			t.Fatal("deleted inside the grace period")
		}
		time.Sleep(2200 * time.Millisecond)
		if res := run(t, r, blob.RunOptions{Mode: blob.GCIncremental}); res.Deleted != 1 {
			t.Fatalf("after the grace period: %+v", res)
		}
		if !gone(t, e, p, g) {
			t.Fatal("not collected after the grace period")
		}
	})

	t.Run("R-STOR-025 the full sweep finds an unreferenced blob that no queue entry names, in keyset order with a checkpoint", func(t *testing.T) {
		e, p, r := setup(t, func(c *config.Storage) { c.GC.BatchSize = 2 })
		var ds []blob.Digest
		for i := 0; i < 7; i++ {
			sp, err := r.store.Ingest(ctx, bytesReader(randomBytes(500+i)), blob.IngestOptions{ContentLength: int64(500 + i)})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.store.Commit(ctx, sp, nil); err != nil { // a live row, no item, no queue entry
				t.Fatal(err)
			}
			ds = append(ds, sp.SHA256)
			sp.Close()
		}
		_, kept, err := r.upload(ctx, "k.bin", randomBytes(900))
		if err != nil {
			t.Fatal(err)
		}
		if res := run(t, r, blob.RunOptions{Mode: blob.GCIncremental}); res.Deleted != 0 {
			t.Fatalf("the incremental run found blobs without a queue entry: %+v", res)
		}

		// Stop after the second batch; the cursor is the checkpoint.
		var cur blob.Cursor
		batches := 0
		cctx, cancel := context.WithCancel(ctx)
		first, err := r.coll.Run(cctx, blob.RunOptions{Mode: blob.GCFull, Checkpoint: func(c blob.Cursor) {
			cur = c
			if batches++; batches == 2 {
				cancel()
			}
		}})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("first run: %v %+v", err, first)
		}
		if cur.After == "" {
			t.Fatal("no checkpoint")
		}
		second := run(t, r, blob.RunOptions{Mode: blob.GCFull, Cursor: cur})
		if first.Deleted+second.Deleted != 7 {
			t.Fatalf("deleted %d + %d, want 7", first.Deleted, second.Deleted)
		}
		if second.Candidates > 3+1 { // the batches after the checkpoint and the kept blob
			t.Fatalf("the second run looked at %d candidates: it did not resume", second.Candidates)
		}
		for _, d := range ds {
			if !gone(t, e, p, d) {
				t.Fatalf("%s not collected", d)
			}
		}
		if state, _, ok := p.state(t, kept); !ok || state != "live" {
			t.Fatal("the referenced blob was collected")
		}
	})

	t.Run("R-STOR-025 a dry run reports what would be deleted and changes nothing", func(t *testing.T) {
		e, p, r := setup(t, nil)
		g, _ := garbage(t, r, "d.bin")
		res := run(t, r, blob.RunOptions{Mode: blob.GCIncremental, DryRun: true})
		if !res.DryRun || res.Deleted != 1 || res.BytesReclaimed != 2000 {
			t.Fatalf("%+v", res)
		}
		if state, _, ok := p.state(t, g); !ok || state != "live" || !objectExists(t, e, e.prefix, g) || p.count(t, "blob_gc_queue") != 1 {
			t.Fatal("a dry run changed something")
		}
	})

	t.Run("R-STOR-024 with deletions paused a run changes nothing and says so", func(t *testing.T) {
		e, p, r := setup(t, nil)
		g, _ := garbage(t, r, "p.bin")
		p.exec(t, "INSERT INTO system_state (key, value) VALUES ('deletions_paused_at', to_jsonb(now()::text))")
		res := run(t, r, blob.RunOptions{Mode: blob.GCIncremental})
		if !res.Paused || res.Deleted != 0 || !objectExists(t, e, e.prefix, g) {
			t.Fatalf("%+v", res)
		}
		if state, _, ok := p.state(t, g); !ok || state != "live" {
			t.Fatal("the row changed")
		}
		p.exec(t, "DELETE FROM system_state WHERE key = 'deletions_paused_at'")
		if res := run(t, r, blob.RunOptions{Mode: blob.GCIncremental}); res.Paused || res.Deleted != 1 {
			t.Fatalf("after resume: %+v", res)
		}
	})

	t.Run("R-STOR-024 a blob that a backup holds stays live with its queue entry, and goes once the hold ends", func(t *testing.T) {
		e, p, r := setup(t, nil)
		g, _ := garbage(t, r, "h.bin")
		var held sync.Map
		held.Store(g, true)
		r.coll.Hold = func(_ context.Context, _ meta.Tx, d blob.Digest) (bool, error) {
			_, ok := held.Load(d)
			return ok, nil
		}
		res := run(t, r, blob.RunOptions{Mode: blob.GCIncremental})
		if res.Held != 1 || res.Deleted != 0 || !objectExists(t, e, e.prefix, g) || p.count(t, "blob_gc_queue") != 1 {
			t.Fatalf("%+v", res)
		}
		held.Delete(g)
		if res := run(t, r, blob.RunOptions{Mode: blob.GCIncremental}); res.Deleted != 1 {
			t.Fatalf("after the hold: %+v", res)
		}
	})

	t.Run("R-STOR-024 a table added by a newer release keeps its blobs: the referencing tables come from the catalog", func(t *testing.T) {
		e, p, r := setup(t, nil)
		g, _ := garbage(t, r, "n.bin")
		p.exec(t, "CREATE TABLE future_refs (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, blob_sha256 bytea REFERENCES blobs (sha256))")
		p.exec(t, "INSERT INTO future_refs (blob_sha256) VALUES ($1)", g.Bytes())
		for _, mode := range []blob.GCMode{blob.GCIncremental, blob.GCFull} {
			res := run(t, r, blob.RunOptions{Mode: mode})
			if res.Deleted != 0 {
				t.Fatalf("%s deleted a blob referenced from the new table: %+v", mode, res)
			}
			if state, _, ok := p.state(t, g); !ok || state != "live" || !objectExists(t, e, e.prefix, g) {
				t.Fatalf("%s: the blob was damaged", mode)
			}
		}
		p.exec(t, "DELETE FROM future_refs")
		p.exec(t, "INSERT INTO blob_gc_queue (sha256, enqueued_at) VALUES ($1, now()) ON CONFLICT DO NOTHING", g.Bytes())
		if res := run(t, r, blob.RunOptions{Mode: blob.GCIncremental}); res.Deleted != 1 {
			t.Fatalf("after the reference went: %+v", res)
		}
	})

	t.Run("R-STOR-024 a reference to a deleting blob fails with SQLSTATE AS001", func(t *testing.T) {
		_, p, r := setup(t, nil)
		_, d, err := r.upload(ctx, "live.bin", randomBytes(100))
		if err != nil {
			t.Fatal(err)
		}
		p.release(t, "live.bin")
		p.exec(t, "UPDATE blobs SET state = 'deleting' WHERE sha256 = $1", d.Bytes())
		_, err = p.admin.Exec(ctx, "INSERT INTO items (repo_id, path, parent_path, kind, blob_sha256, size) VALUES ($1, 'x', '', 'file', $2, 100)", p.repo, d.Bytes())
		var pe *pgconn.PgError
		if !errors.As(err, &pe) || pe.Code != "AS001" {
			t.Fatalf("got %v", err)
		}
	})
}

// A release that commits while the collector holds the lock moves the queue
// entry forward; the collector must not delete the newer entry.
func TestGCKeepsAReleaseThatCommitsDuringTheRun(t *testing.T) {
	ctx := context.Background()
	t.Run("R-STOR-024 a release committed after the reference check leaves its queue entry for the next incremental run", func(t *testing.T) {
		e := newS3(t, "gcseen")
		p := newPG(t)
		r := newReplica(t, e, p, nil)

		// The blob has an old queue entry (its first reference went) and a new
		// reference (a second upload), so the collector finds a reference.
		data := randomBytes(1000)
		_, d, err := r.upload(ctx, "one.bin", data)
		if err != nil {
			t.Fatal(err)
		}
		p.release(t, "one.bin")
		if _, _, err := r.upload(ctx, "two.bin", data); err != nil {
			t.Fatal(err)
		}
		var before time.Time
		if err := p.admin.QueryRow(ctx, "SELECT enqueued_at FROM trove.blob_gc_queue WHERE sha256 = $1", d.Bytes()).Scan(&before); err != nil {
			t.Fatal(err)
		}

		paused, resume := make(chan struct{}), make(chan struct{})
		r.coll.Fault = func(point string) error {
			if point == "claim:checked" {
				close(paused)
				<-resume
			}
			return nil
		}
		done := make(chan blob.GCResult, 1)
		go func() {
			res, _ := r.coll.Run(ctx, blob.RunOptions{Mode: blob.GCIncremental})
			done <- res
		}()
		<-paused
		p.release(t, "two.bin") // commits while the collector's transaction is open
		close(resume)
		res := <-done
		if res.Deleted != 0 {
			t.Fatalf("the collector deleted a blob it had found referenced: %+v", res)
		}
		var after time.Time
		if err := p.admin.QueryRow(ctx, "SELECT enqueued_at FROM trove.blob_gc_queue WHERE sha256 = $1", d.Bytes()).Scan(&after); err != nil {
			t.Fatalf("the queue entry is gone: %v", err)
		}
		if !after.After(before) {
			t.Fatalf("the queue entry still has the old time %v (was %v)", after, before)
		}
		r.coll.Fault = nil
		if res := run(t, r, blob.RunOptions{Mode: blob.GCIncremental}); res.Deleted != 1 {
			t.Fatalf("the next incremental run did not delete the blob: %+v", res)
		}
		if !gone(t, e, p, d) {
			t.Fatal("not collected")
		}
	})
}

// A drained collector sends no delete afterwards and hands its claims over.
func TestGCDrain(t *testing.T) {
	ctx := context.Background()
	t.Run("R-STOR-024 a cancelled collector sends nothing after its calls return, records what it did and hands the rest over", func(t *testing.T) {
		e := newS3(t, "gcdrain")
		p := newPG(t)
		r := newReplica(t, e, p, func(c *config.Storage) {
			c.GC.BatchDelete = true
			c.GC.DeleteSettle = 300 * time.Millisecond
			c.GC.HandoverDelay = 6 * time.Second
		})
		var ds []blob.Digest
		for i := 0; i < 3; i++ {
			d, _ := garbage(t, r, "dr/"+string(rune('a'+i)))
			ds = append(ds, d)
		}
		block, called := make(chan struct{}), make(chan struct{}, 1)
		r.obj.mu.Lock()
		r.obj.blockMany, r.obj.manyCalled = block, called
		r.obj.mu.Unlock()

		cctx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { _, err := r.coll.Run(cctx, blob.RunOptions{Mode: blob.GCIncremental}); done <- err }()
		<-called // the DeleteObjects call is in flight, the rows are claimed
		claimedAt := time.Now()
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("run: %v", err)
		}
		callsAtCancel := r.obj.deleteMany.Load() + r.obj.deletes.Load()
		time.Sleep(300 * time.Millisecond)
		if r.obj.deleteMany.Load()+r.obj.deletes.Load() != callsAtCancel {
			t.Fatal("the collector sent a delete after it returned")
		}
		for _, d := range ds {
			if state, _, ok := p.state(t, d); !ok || state != "deleting" {
				t.Fatalf("%s: %s %v, want a deleting row handed over", d, state, ok)
			}
			if !objectExists(t, e, e.prefix, d) {
				t.Fatal("the object was deleted although the call was cancelled")
			}
		}
		// Handed over: state_changed_at lies handover_delay - delete_settle in the past.
		var lagMs int64
		if err := p.admin.QueryRow(ctx, `SELECT (extract(epoch FROM now() - min(state_changed_at)) * 1000)::bigint FROM trove.blobs WHERE state = 'deleting'`).Scan(&lagMs); err != nil {
			t.Fatal(err)
		}
		if want := (6*time.Second - 300*time.Millisecond).Milliseconds(); lagMs < want-200 {
			t.Fatalf("state_changed_at is %d ms old, want at least %d ms (handed over)", lagMs, want-200)
		}

		// The run that continues finishes the rows within 2 x delete_settle plus a pick-up.
		r2 := newReplica(t, e, p, func(c *config.Storage) {
			c.GC.DeleteSettle = 300 * time.Millisecond
			c.GC.HandoverDelay = 6 * time.Second
		})
		start := time.Now()
		res := run(t, r2, blob.RunOptions{Mode: blob.GCIncremental})
		if took := time.Since(start); took > 2*300*time.Millisecond+2*time.Second {
			t.Fatalf("the continuing run took %v", took)
		}
		for _, d := range ds {
			if !gone(t, e, p, d) {
				t.Fatalf("%s not finished: %+v (since the drain %v)", d, res, time.Since(claimedAt))
			}
		}
	})
}

// fakeRun is the part of the job framework's handle that RunJob uses.
type fakeRun struct {
	checkpoint []byte
	saved      []blob.Cursor
	result     any
	failSave   error
}

func (f *fakeRun) LoadCheckpoint(v any) (bool, error) {
	if f.checkpoint == nil {
		return false, nil
	}
	return true, json.Unmarshal(f.checkpoint, v)
}

func (f *fakeRun) SaveCheckpoint(_ context.Context, v any) error {
	if f.failSave != nil {
		return f.failSave
	}
	f.saved = append(f.saved, v.(blob.Cursor))
	b, _ := json.Marshal(v)
	f.checkpoint = b
	return nil
}

func (f *fakeRun) SetResult(v any) error { f.result = v; return nil }

func TestGCJobHandler(t *testing.T) {
	ctx := context.Background()
	t.Run("R-STOR-025 the job handler runs the sweep, saves its keyset position after every batch and stores the result", func(t *testing.T) {
		e := newS3(t, "gcjob")
		p := newPG(t)
		r := newReplica(t, e, p, func(c *config.Storage) { c.GC.BatchSize = 2 })
		var ds []blob.Digest
		for i := 0; i < 5; i++ {
			d, _ := garbage(t, r, "j/"+string(rune('a'+i)))
			ds = append(ds, d)
		}
		run := &fakeRun{}
		if err := r.coll.RunJob(ctx, blob.GCFull, json.RawMessage(`{"dryRun":true}`), run); err != nil {
			t.Fatal(err)
		}
		res := run.result.(blob.GCResult)
		if !res.DryRun || res.Deleted != 5 || len(run.saved) != 3 {
			t.Fatalf("%+v, %d checkpoints", res, len(run.saved))
		}
		for _, d := range ds {
			if !objectExists(t, e, e.prefix, d) {
				t.Fatal("a dry run deleted an object")
			}
		}
		// The second attempt starts at the checkpoint: a released job continues.
		run2 := &fakeRun{checkpoint: run.checkpoint}
		if err := r.coll.RunJob(ctx, blob.GCFull, nil, run2); err != nil {
			t.Fatal(err)
		}
		if res := run2.result.(blob.GCResult); res.Candidates != 0 {
			t.Fatalf("the continued run looked at %d candidates after the last checkpoint", res.Candidates)
		}
		// From the start it collects.
		run3 := &fakeRun{}
		if err := r.coll.RunJob(ctx, blob.GCFull, nil, run3); err != nil {
			t.Fatal(err)
		}
		if res := run3.result.(blob.GCResult); res.Deleted != 5 {
			t.Fatalf("%+v", res)
		}
		for _, d := range ds {
			if !gone(t, e, p, d) {
				st, _, ok := p.state(t, d)
				t.Fatalf("%s not collected: row %v %s, object %v, result %+v", d, ok, st, objectExists(t, e, e.prefix, d), res)
			}
		}
	})
	t.Run("R-STOR-025 a lost lease (the checkpoint cannot be saved) ends the run", func(t *testing.T) {
		e := newS3(t, "gcjoblease")
		p := newPG(t)
		r := newReplica(t, e, p, func(c *config.Storage) { c.GC.BatchSize = 1 })
		for i := 0; i < 4; i++ {
			garbage(t, r, "l/"+string(rune('a'+i)))
		}
		lost := errors.New("lease lost")
		err := r.coll.RunJob(ctx, blob.GCFull, nil, &fakeRun{failSave: lost})
		if !errors.Is(err, lost) {
			t.Fatalf("got %v", err)
		}
		if p.count(t, "blobs") == 0 {
			t.Fatal("the run kept deleting after it lost its lease")
		}
	})
}

// Two runs that read the same stale deleting row must not both delete its
// object: the second would delete an object an uploader re-created in between.
func TestGCTakeoverIsExclusive(t *testing.T) {
	ctx := context.Background()
	t.Run("R-STOR-024 a run that read a stale row but lost the takeover deletes nothing, so an object re-created meanwhile survives", func(t *testing.T) {
		e := newS3(t, "gctakeover")
		p := newPG(t)
		r1 := newReplica(t, e, p, nil)
		r2 := newReplica(t, e, p, nil)
		up := newReplica(t, e, p, nil)

		d, data := garbage(t, up, "t/a.bin")
		// A collector claimed the row and died: the object is still there and the
		// row is older than handover_delay.
		p.exec(t, "UPDATE blobs SET state = 'deleting', state_changed_at = now() - interval '1 hour' WHERE sha256 = $1", d.Bytes())
		p.exec(t, "DELETE FROM blob_gc_queue WHERE sha256 = $1", d.Bytes())

		reached, proceed := make(chan struct{}), make(chan struct{})
		r1.coll.Fault = func(point string) error {
			if point == "finish:delete" {
				close(reached)
				<-proceed
			}
			return nil
		}
		done := make(chan error, 1)
		go func() {
			_, err := r1.coll.Run(ctx, blob.RunOptions{Mode: blob.GCIncremental})
			done <- err
		}()
		<-reached // r1 has read the stale row and not yet taken it over

		run(t, r2, blob.RunOptions{Mode: blob.GCIncremental}) // takes it over, deletes, drops the row
		if !gone(t, e, p, d) {
			t.Fatal("setup: the second run did not finish the row")
		}
		// An uploader writes the same content again: a new object and a new row.
		if _, _, err := up.upload(ctx, "t/b.bin", data); err != nil {
			t.Fatal(err)
		}

		close(proceed)
		if err := <-done; err != nil {
			t.Fatalf("first run: %v", err)
		}
		if state, _, ok := p.state(t, d); !ok || state != "live" {
			t.Fatalf("the re-created row is %q (present %v)", state, ok)
		}
		if !objectExists(t, e, e.prefix, d) {
			t.Fatal("the first run deleted the object an uploader re-created")
		}
	})
}
