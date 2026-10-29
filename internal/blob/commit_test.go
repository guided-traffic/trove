package blob_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/blob/blobtest"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
)

type fixture struct {
	store *blob.Store
	mem   *blobtest.Store
	db    *fakeDB
	dir   string
}

func newFixture(t *testing.T, mutate func(*config.Storage), opts ...blob.Option) *fixture {
	t.Helper()
	dir := t.TempDir()
	cfg := blobtest.Storage(t, dir)
	cfg.Upload.BlobWaitTimeout = 2 * time.Second
	if mutate != nil {
		mutate(&cfg)
	}
	mem := blobtest.New()
	mem.MinPartSize = 0
	db := newFakeDB()
	opts = append([]blob.Option{blob.WithWaitPoll(5 * time.Millisecond), blob.WithRows(db)}, opts...)
	s, err := blob.New(cfg, mem, db, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{store: s, mem: mem, db: db, dir: dir}
}

func (f *fixture) ingest(t *testing.T, data []byte) *blob.Spooled {
	t.Helper()
	sp, err := f.store.Ingest(context.Background(), bytes.NewReader(data), blob.IngestOptions{ContentLength: int64(len(data))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sp.Close() })
	return sp
}

func content(n int, seed byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7) + seed
	}
	return b
}

func noLink(context.Context, meta.Tx) error { return nil }

func TestCommitDeduplicates(t *testing.T) {
	t.Run("R-STOR-005 identical content written twice costs one PutObject and shares one blob row", func(t *testing.T) {
		f := newFixture(t, nil)
		data := content(5000, 1)
		var links int
		link := func(context.Context, meta.Tx) error { links++; return nil }
		first, err := f.store.Commit(context.Background(), f.ingest(t, data), link)
		if err != nil {
			t.Fatal(err)
		}
		second, err := f.store.Commit(context.Background(), f.ingest(t, data), link)
		if err != nil {
			t.Fatal(err)
		}
		if !first.Wrote || !first.Inserted || first.Deduplicated {
			t.Fatalf("first: %+v", first)
		}
		if second.Wrote || second.Inserted || !second.Deduplicated {
			t.Fatalf("second: %+v", second)
		}
		if f.mem.Count("put") != 1 || len(f.db.rows) != 1 || links != 2 {
			t.Fatalf("puts=%d rows=%d links=%d", f.mem.Count("put"), len(f.db.rows), links)
		}
		sp := f.ingest(t, data)
		if got, ok := f.mem.Data(f.store.Key(sp.SHA256)); !ok || !bytes.Equal(got, data) {
			t.Fatal("the object is not under its content address")
		}
	})
	t.Run("R-STOR-001 the object lies under the key derived from its SHA-256", func(t *testing.T) {
		f := newFixture(t, nil)
		sp := f.ingest(t, content(100, 2))
		if _, err := f.store.Commit(context.Background(), sp, nil); err != nil {
			t.Fatal(err)
		}
		keys := f.mem.Keys("")
		if len(keys) != 1 || keys[0] != blob.Key("trove", sp.SHA256) {
			t.Fatalf("keys: %v", keys)
		}
	})
}

type sqlStateErr struct{ code string }

func (e sqlStateErr) Error() string    { return "sqlstate " + e.code }
func (e sqlStateErr) SQLState() string { return e.code }

func TestCommitProtocol(t *testing.T) {
	t.Run("R-STOR-005 a reference rejected by the guard (AS001) restarts the protocol", func(t *testing.T) {
		f := newFixture(t, nil)
		var calls int
		link := func(context.Context, meta.Tx) error {
			calls++
			if calls == 1 {
				return sqlStateErr{"AS001"}
			}
			return nil
		}
		res, err := f.store.Commit(context.Background(), f.ingest(t, content(10, 3)), link)
		if err != nil || calls != 2 {
			t.Fatalf("res=%+v err=%v calls=%d", res, err, calls)
		}
	})
	t.Run("R-STOR-005 a failure after the object write queues the digest for collection", func(t *testing.T) {
		f := newFixture(t, nil)
		sp := f.ingest(t, content(10, 4))
		boom := errors.New("constraint violation")
		_, err := f.store.Commit(context.Background(), sp, func(context.Context, meta.Tx) error { return boom })
		if !errors.Is(err, boom) {
			t.Fatalf("got %v", err)
		}
		if !f.db.queue[sp.SHA256] || len(f.db.rows) != 0 {
			t.Fatalf("queue=%v rows=%d", f.db.queue, len(f.db.rows))
		}
	})
	t.Run("R-STOR-005 after commit_attempts rounds the request fails with 503 and Retry-After", func(t *testing.T) {
		f := newFixture(t, func(c *config.Storage) { c.Upload.CommitAttempts = 3 })
		f.db.conflict = true
		_, err := f.store.Commit(context.Background(), f.ingest(t, content(10, 5)), noLink)
		var e *errs.Error
		if !errors.Is(err, blob.ErrCommitAttempts) || !errors.As(err, &e) || e.Kind != errs.Unavailable || e.RetryAfter <= 0 {
			t.Fatalf("got %#v", err)
		}
	})
	t.Run("R-STOR-005 an upload that meets a deleting row waits for the row to go and writes the object after", func(t *testing.T) {
		f := newFixture(t, nil)
		sp := f.ingest(t, content(10, 6))
		f.db.rows[sp.SHA256] = &fakeRow{state: "deleting", status: "ok"}
		var putsWhenGone int
		f.db.onExists = func(n int) {
			if n == 3 {
				putsWhenGone = f.mem.Count("put")
				delete(f.db.rows, sp.SHA256)
			}
		}
		res, err := f.store.Commit(context.Background(), sp, noLink)
		if err != nil {
			t.Fatal(err)
		}
		if putsWhenGone != 0 || !res.Wrote || !res.Inserted || f.mem.Count("put") != 1 {
			t.Fatalf("puts when the row went=%d res=%+v puts=%d", putsWhenGone, res, f.mem.Count("put"))
		}
		if f.db.rows[sp.SHA256].state != "live" {
			t.Fatal("the new row is not live")
		}
	})
	t.Run("R-STOR-005 a row that stays deleting past blob_wait_timeout answers 503", func(t *testing.T) {
		f := newFixture(t, func(c *config.Storage) { c.Upload.BlobWaitTimeout = 50 * time.Millisecond })
		sp := f.ingest(t, content(10, 7))
		f.db.rows[sp.SHA256] = &fakeRow{state: "deleting", status: "ok"}
		_, err := f.store.Commit(context.Background(), sp, noLink)
		if !errors.Is(err, blob.ErrBlobBusy) || errs.KindOf(err) != errs.Unavailable {
			t.Fatalf("got %v", err)
		}
		if f.mem.Count("put") != 0 {
			t.Fatal("an object was written for a digest being deleted")
		}
	})
	t.Run("R-STOR-005 a live row with damaged content gets its object written again and is marked ok", func(t *testing.T) {
		f := newFixture(t, nil)
		data := content(64, 8)
		sp := f.ingest(t, data)
		f.db.rows[sp.SHA256] = &fakeRow{state: "live", status: "missing", size: 64}
		res, err := f.store.Commit(context.Background(), sp, noLink)
		if err != nil || !res.Wrote || res.Inserted {
			t.Fatalf("res=%+v err=%v", res, err)
		}
		if f.db.rows[sp.SHA256].status != "ok" {
			t.Fatalf("status %s", f.db.rows[sp.SHA256].status)
		}
		if got, _ := f.mem.Data(f.store.Key(sp.SHA256)); !bytes.Equal(got, data) {
			t.Fatal("the object was not rewritten")
		}
	})
	t.Run("R-STOR-005 an object older than commit_deadline is written again before its row is inserted", func(t *testing.T) {
		var calls atomic.Int32
		base := time.Now()
		clock := func() time.Time {
			if calls.Add(1) <= 1 {
				return base
			}
			return base.Add(2 * time.Hour)
		}
		f := newFixture(t, nil, blob.WithClock(clock))
		// now() runs once when the first write is recorded and then at each
		// canInsert test: the second reading is past the deadline.
		res, err := f.store.Commit(context.Background(), f.ingest(t, content(10, 9)), noLink)
		if err != nil {
			t.Fatal(err)
		}
		if f.mem.Count("put") != 2 || !res.Inserted {
			t.Fatalf("puts=%d res=%+v", f.mem.Count("put"), res)
		}
	})
	t.Run("R-STOR-005 the row of an object is inserted only within half of delete_settle after the write", func(t *testing.T) {
		// delete_settle is 30 s in the default configuration, so the window is 15 s:
		// a collector that dropped a row between our write and our insert deleted the
		// key we wrote at least delete_settle before the drop, which is out of reach
		// of an insert inside the window.
		for _, tc := range []struct {
			name    string
			elapsed time.Duration
			puts    int
		}{
			{"inside", 10 * time.Second, 1},
			{"outside", 20 * time.Second, 2},
		} {
			var calls atomic.Int32
			base := time.Now()
			clock := func() time.Time {
				if calls.Add(1) <= 1 {
					return base
				}
				return base.Add(tc.elapsed)
			}
			f := newFixture(t, nil, blob.WithClock(clock))
			res, err := f.store.Commit(context.Background(), f.ingest(t, content(10, 40)), noLink)
			if err != nil {
				t.Fatal(err)
			}
			if f.mem.Count("put") != tc.puts || !res.Inserted {
				t.Errorf("%s: puts=%d res=%+v, want %d puts", tc.name, f.mem.Count("put"), res, tc.puts)
			}
		}
	})
	t.Run("R-STOR-036 an object store outage fails the commit with 503 and leaves no row", func(t *testing.T) {
		f := newFixture(t, nil)
		f.mem.SetFault(func(op, _ string) error {
			if op == "put" {
				return &blob.StoreError{Op: "PutObject", Status: 503, Kind: blob.KindTransient}
			}
			return nil
		})
		_, err := f.store.Commit(context.Background(), f.ingest(t, content(10, 10)), noLink)
		if errs.KindOf(err) != errs.Unavailable || len(f.db.rows) != 0 {
			t.Fatalf("err=%v rows=%d", err, len(f.db.rows))
		}
	})
	t.Run("R-STOR-005 a deduplication hit needs no object store", func(t *testing.T) {
		f := newFixture(t, nil)
		data := content(10, 11)
		if _, err := f.store.Commit(context.Background(), f.ingest(t, data), noLink); err != nil {
			t.Fatal(err)
		}
		f.mem.SetFault(func(string, string) error { return errors.New("object store is down") })
		res, err := f.store.Commit(context.Background(), f.ingest(t, data), noLink)
		if err != nil || !res.Deduplicated {
			t.Fatalf("res=%+v err=%v", res, err)
		}
	})
}

func TestTransfer(t *testing.T) {
	multipart := func(c *config.Storage) {
		c.S3.Multipart.Threshold = 10 << 10
		c.S3.Multipart.PartSize = 4 << 10
		c.S3.Multipart.Concurrency = 3
		c.Spool.MemoryThreshold = 0
		c.S3.PayloadChecksum = "none"
	}
	t.Run("R-STOR-006 content up to the threshold is one PutObject", func(t *testing.T) {
		f := newFixture(t, multipart)
		if _, err := f.store.Commit(context.Background(), f.ingest(t, content(10<<10, 1)), noLink); err != nil {
			t.Fatal(err)
		}
		if f.mem.Count("put") != 1 || f.mem.Count("create_multipart") != 0 {
			t.Fatalf("puts=%d multipart=%d", f.mem.Count("put"), f.mem.Count("create_multipart"))
		}
	})
	t.Run("R-STOR-006 content above the threshold goes up in parts read from the spool file", func(t *testing.T) {
		f := newFixture(t, multipart)
		data := content(10<<10+1, 2)
		sp := f.ingest(t, data)
		if sp.InMemory() {
			t.Fatal("the test needs a spool file")
		}
		if _, err := f.store.Commit(context.Background(), sp, noLink); err != nil {
			t.Fatal(err)
		}
		// 10241 bytes in 4096-byte parts: 3 parts, the last of 2049 bytes.
		if f.mem.Count("put") != 0 || f.mem.Count("upload_part") != 3 || f.mem.Count("complete_multipart") != 1 {
			t.Fatalf("puts=%d parts=%d completes=%d", f.mem.Count("put"), f.mem.Count("upload_part"), f.mem.Count("complete_multipart"))
		}
		if got, _ := f.mem.Data(f.store.Key(sp.SHA256)); !bytes.Equal(got, data) {
			t.Fatal("the assembled object differs from the content")
		}
		if f.mem.OpenUploads() != 0 {
			t.Fatal("an upload is still open")
		}
	})
	t.Run("R-STOR-006 a failed part aborts the multipart upload and writes no row", func(t *testing.T) {
		f := newFixture(t, multipart)
		var n atomic.Int32
		f.mem.SetFault(func(op, _ string) error {
			if op == "upload_part" && n.Add(1) == 2 {
				return &blob.StoreError{Op: "UploadPart", Status: 500, Kind: blob.KindTransient}
			}
			return nil
		})
		_, err := f.store.Commit(context.Background(), f.ingest(t, content(20<<10, 3)), noLink)
		if err == nil {
			t.Fatal("the commit succeeded")
		}
		if f.mem.Count("abort_multipart") != 1 || f.mem.OpenUploads() != 0 || len(f.mem.Keys("")) != 0 || len(f.db.rows) != 0 {
			t.Fatalf("aborts=%d open=%d keys=%v rows=%d", f.mem.Count("abort_multipart"), f.mem.OpenUploads(), f.mem.Keys(""), len(f.db.rows))
		}
	})
	t.Run("R-STOR-006 a cancelled request aborts its multipart upload", func(t *testing.T) {
		f := newFixture(t, multipart)
		ctx, cancel := context.WithCancel(context.Background())
		f.mem.SetFault(func(op, _ string) error {
			if op == "upload_part" {
				cancel()
			}
			return nil
		})
		if _, err := f.store.Commit(ctx, f.ingest(t, content(20<<10, 4)), noLink); err == nil {
			t.Fatal("the commit succeeded")
		}
		if f.mem.OpenUploads() != 0 {
			t.Fatal("the multipart upload was left open")
		}
	})
	t.Run("R-STOR-006 a failed complete aborts the upload too", func(t *testing.T) {
		f := newFixture(t, multipart)
		f.mem.SetFault(func(op, _ string) error {
			if op == "complete_multipart" {
				return &blob.StoreError{Op: "CompleteMultipartUpload", Status: 500, Kind: blob.KindTransient}
			}
			return nil
		})
		if _, err := f.store.Commit(context.Background(), f.ingest(t, content(20<<10, 5)), noLink); err == nil {
			t.Fatal("the commit succeeded")
		}
		if f.mem.OpenUploads() != 0 {
			t.Fatal("the multipart upload was left open")
		}
	})
	t.Run("R-STOR-006 at most storage.s3.multipart.concurrency parts are in flight", func(t *testing.T) {
		f := newFixture(t, multipart)
		var inflight, peak atomic.Int32
		f.mem.SetFault(func(op, _ string) error {
			if op == "upload_part" {
				n := inflight.Add(1)
				for {
					p := peak.Load()
					if n <= p || peak.CompareAndSwap(p, n) {
						break
					}
				}
				time.Sleep(10 * time.Millisecond)
				inflight.Add(-1)
			}
			return nil
		})
		if _, err := f.store.Commit(context.Background(), f.ingest(t, content(40<<10, 6)), noLink); err != nil {
			t.Fatal(err)
		}
		if peak.Load() > 3 || peak.Load() < 2 {
			t.Fatalf("peak parallel parts %d, want 2 or 3", peak.Load())
		}
	})
	t.Run("R-STOR-006 the replica-wide part limit holds across uploads", func(t *testing.T) {
		f := newFixture(t, func(c *config.Storage) {
			multipart(c)
			c.S3.Multipart.Concurrency = 4
			c.S3.Multipart.MaxConcurrentParts = 2
		})
		var inflight, peak atomic.Int32
		f.mem.SetFault(func(op, _ string) error {
			if op == "upload_part" {
				n := inflight.Add(1)
				for {
					p := peak.Load()
					if n <= p || peak.CompareAndSwap(p, n) {
						break
					}
				}
				time.Sleep(10 * time.Millisecond)
				inflight.Add(-1)
			}
			return nil
		})
		done := make(chan error, 2)
		for i := 0; i < 2; i++ {
			sp := f.ingest(t, content(40<<10, byte(10+i)))
			go func() { _, err := f.store.Commit(context.Background(), sp, noLink); done <- err }()
		}
		for i := 0; i < 2; i++ {
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		}
		if peak.Load() > 2 {
			t.Fatalf("peak parallel parts %d, want at most 2", peak.Load())
		}
	})
	t.Run("R-STOR-006 every request carries the configured payload checksum and the store verifies it", func(t *testing.T) {
		for _, alg := range []string{"sha256", "md5", "none"} {
			f := newFixture(t, func(c *config.Storage) { multipart(c); c.S3.PayloadChecksum = alg })
			if _, err := f.store.Commit(context.Background(), f.ingest(t, content(1<<10, 7)), noLink); err != nil {
				t.Fatalf("%s single: %v", alg, err)
			}
			if _, err := f.store.Commit(context.Background(), f.ingest(t, content(20<<10, 8)), noLink); err != nil {
				t.Fatalf("%s multipart: %v", alg, err)
			}
		}
	})
}

func TestPartSize(t *testing.T) {
	const mib = 1 << 20
	cases := []struct {
		name             string
		size, configured int64
		want             int64
	}{
		{"configured size is enough", 300 * mib, 16 * mib, 16 * mib},
		{"exactly 10000 parts", 10000 * 16 * mib, 16 * mib, 16 * mib},
		{"one byte over raises it to the next MiB multiple", 10000*16*mib + 1, 16 * mib, 17 * mib},
		{"a huge object", 5 << 40, 16 * mib, 525 * mib},
	}
	for _, c := range cases {
		t.Run("R-STOR-006 part size: "+c.name, func(t *testing.T) {
			got := blob.PartSize(c.size, c.configured)
			if got != c.want || (c.size+got-1)/got > 10000 {
				t.Fatalf("got %d (%d parts), want %d", got, (c.size+got-1)/got, c.want)
			}
		})
	}
}

func TestSpoolIsCleanedAfterCommit(t *testing.T) {
	t.Run("R-STOR-004 closing the spooled body after the commit leaves the spool empty", func(t *testing.T) {
		f := newFixture(t, func(c *config.Storage) { c.Spool.MemoryThreshold = 0 })
		sp, err := f.store.Ingest(context.Background(), strings.NewReader(strings.Repeat("x", 4096)), blob.IngestOptions{ContentLength: 4096})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.Commit(context.Background(), sp, noLink); err != nil {
			t.Fatal(err)
		}
		sp.Close()
		if f.store.Spool().Reserved() != 0 {
			t.Fatal("budget not returned")
		}
	})
}
