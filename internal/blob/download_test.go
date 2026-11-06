package blob_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/config"
)

func TestDownloadVerification(t *testing.T) {
	data := content(1<<20, 21)
	setup := func(t *testing.T, mutate func(*config.Storage)) (*fixture, *blob.Spooled, *atomic.Int32) {
		t.Helper()
		var damaged atomic.Int32
		f := newFixture(t, func(c *config.Storage) {
			c.Download.HoldBack = 64 << 10
			c.Download.BufferSize = 32 << 10
			if mutate != nil {
				mutate(c)
			}
		}, blob.WithDamageHook(func(context.Context, blob.Digest, string) { damaged.Add(1) }))
		sp := f.ingest(t, data)
		if _, err := f.store.Commit(context.Background(), sp, noLink); err != nil {
			t.Fatal(err)
		}
		return f, sp, &damaged
	}
	t.Run("R-STOR-013 an intact object downloads completely", func(t *testing.T) {
		f, sp, _ := setup(t, nil)
		r, err := f.store.Open(context.Background(), sp.SHA256, sp.Size, blob.OpenOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if got := readAll(t, r); !bytes.Equal(got, data) {
			t.Fatal("the download differs")
		}
		if f.db.rows[sp.SHA256].status != "ok" {
			t.Fatal("status changed")
		}
	})
	t.Run("R-ARCH-005 Q-ARCH-328 Q-ARCH-330 Open charges what the reader will hold until it is closed: a whole object by DownloadCost, a range by its length", func(t *testing.T) {
		var charged []int64
		var open int
		f := newFixture(t, func(c *config.Storage) {
			c.Download.HoldBack = 64 << 10
			c.Download.BufferSize = 32 << 10
			c.S3.HTTP.ReceiveWindow = 256 << 10
		}, blob.WithMemory(func(_ context.Context, n int64) func() {
			charged = append(charged, n)
			open++
			return func() { open-- }
		}))
		sp := f.ingest(t, data)
		if _, err := f.store.Commit(context.Background(), sp, noLink); err != nil {
			t.Fatal(err)
		}
		r, err := f.store.Open(context.Background(), sp.SHA256, sp.Size, blob.OpenOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if open != 1 {
			t.Fatalf("%d readers charged while one is open", open)
		}
		_ = r.Close()
		if open != 0 {
			t.Fatalf("%d readers still charged after Close", open)
		}
		// The buffer is twice the hold-back, 128 KiB, and a 1 MiB object fills the 256 KiB window.
		if want := int64(128<<10 + 256<<10); len(charged) != 1 || charged[0] != want {
			t.Fatalf("charged %v, want [%d]", charged, want)
		}
		// A README read: many ranged opens one after the other, each a few KiB of a 1 MiB object.
		charged = nil
		for i := range 200 {
			rr, err := f.store.Open(context.Background(), sp.SHA256, sp.Size, blob.OpenOptions{Range: &blob.ByteRange{Offset: int64(i) * 4096, Length: 4096}})
			if err != nil {
				t.Fatal(err)
			}
			if open != 1 {
				t.Fatalf("range %d: %d readers charged", i, open)
			}
			_ = rr.Close()
		}
		if open != 0 {
			t.Fatalf("%d ranged readers still charged", open)
		}
		for _, n := range charged {
			if want := int64(4<<10 + 4<<10); n != want { // a 4 KiB buffer and the 4 KiB range
				t.Fatalf("a 4 KiB range was charged %d, want %d", n, want)
			}
		}
		// A refused or failed open takes its charge off again.
		if _, err := f.store.Open(context.Background(), blob.Digest{1}, 10, blob.OpenOptions{}); err == nil {
			t.Fatal("an absent object opened")
		}
		if open != 0 {
			t.Fatalf("%d readers charged after a failed open", open)
		}
	})
	t.Run("Q-STOR-6 a corrupted object of the same length is detected, the held-back bytes are never released and the blob is marked corrupt", func(t *testing.T) {
		f, sp, damaged := setup(t, nil)
		bad := bytes.Clone(data)
		bad[len(bad)/2] ^= 0xff
		f.mem.Set(f.store.Key(sp.SHA256), bad, f.mem.Now())
		r, err := f.store.Open(context.Background(), sp.SHA256, sp.Size, blob.OpenOptions{})
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(r)
		r.Close()
		if !errors.Is(err, blob.ErrCorrupt) {
			t.Fatalf("read error: %v", err)
		}
		if len(got) > len(data)-(64<<10) {
			t.Fatalf("%d of %d bytes were released, the last 64 KiB must be held back", len(got), len(data))
		}
		if f.db.rows[sp.SHA256].status != "corrupt" || damaged.Load() != 1 {
			t.Fatalf("status %s damaged hook %d", f.db.rows[sp.SHA256].status, damaged.Load())
		}
	})
	t.Run("R-HA-024 R-STOR-027 an object that is gone is ErrNotFound and marks its blob missing once; a commit of the same content writes it again", func(t *testing.T) {
		f, sp, damaged := setup(t, nil)
		if err := f.mem.Delete(context.Background(), f.store.Key(sp.SHA256)); err != nil {
			t.Fatal(err)
		}
		for _, o := range []blob.OpenOptions{{}, {Range: &blob.ByteRange{Offset: 0, Length: 10}}} {
			if _, err := f.store.Open(context.Background(), sp.SHA256, sp.Size, o); !errors.Is(err, blob.ErrNotFound) {
				t.Fatalf("open %+v: %v, want ErrNotFound", o, err)
			}
		}
		if f.db.rows[sp.SHA256].status != "missing" || damaged.Load() != 1 {
			t.Fatalf("status %s, damaged hook %d: one detection, whatever the number of reads", f.db.rows[sp.SHA256].status, damaged.Load())
		}
		// The repair of R-STOR-027: an upload of identical content is no deduplication while the row is missing.
		again := f.ingest(t, data)
		res, err := f.store.Commit(context.Background(), again, noLink)
		if err != nil || !res.Wrote || res.Deduplicated {
			t.Fatalf("commit %+v, %v: the object must be written again", res, err)
		}
		if f.db.rows[sp.SHA256].status != "ok" {
			t.Fatalf("status %s after the repair", f.db.rows[sp.SHA256].status)
		}
		r, err := f.store.Open(context.Background(), sp.SHA256, sp.Size, blob.OpenOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if got := readAll(t, r); !bytes.Equal(got, data) {
			t.Fatal("the repaired object differs")
		}
	})
	t.Run("Q-STOR-151 a read that found no object does not undo a repair that wrote it again before the mark", func(t *testing.T) {
		f, sp, damaged := setup(t, nil)
		// The read misses the object, which a commit of the same content writes again before the mark: the store answers
		// the read "no such object" and the look after the mark finds it.
		f.mem.SetFault(func(op, _ string) error {
			if op == "get" {
				return blob.ErrNotFound
			}
			return nil
		})
		if _, err := f.store.Open(context.Background(), sp.SHA256, sp.Size, blob.OpenOptions{}); !errors.Is(err, blob.ErrNotFound) {
			t.Fatalf("open: %v", err)
		}
		if f.db.rows[sp.SHA256].status != "ok" || damaged.Load() != 1 {
			t.Fatalf("status %s, damaged hook %d: the detection counts, the row ends ok", f.db.rows[sp.SHA256].status, damaged.Load())
		}
	})
	t.Run("Q-STOR-153 a blob marked missing asks the store again once per interval, and an object that is back makes it ok", func(t *testing.T) {
		f, sp, _ := setup(t, nil)
		clock := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
		f.db.now = func() time.Time { return clock }
		key := f.store.Key(sp.SHA256)
		_ = f.mem.Delete(context.Background(), key)
		if _, err := f.store.Open(context.Background(), sp.SHA256, sp.Size, blob.OpenOptions{}); !errors.Is(err, blob.ErrNotFound) {
			t.Fatalf("open: %v", err)
		}
		heads := func() int { return f.mem.Count("head") }
		before := heads()
		// Still gone, inside the interval: no look at the store.
		if f.store.RecheckMissing(context.Background(), sp.SHA256, sp.Size) || heads() != before {
			t.Fatalf("a recheck inside the interval asked the store (%d heads)", heads()-before)
		}
		// The object is back, still inside the interval: the mark stands and the store is not asked.
		f.mem.Set(key, data, f.mem.Now())
		clock = clock.Add(blob.MissingRecheckInterval - time.Second)
		if f.store.RecheckMissing(context.Background(), sp.SHA256, sp.Size) || heads() != before {
			t.Fatal("a recheck inside the interval asked the store")
		}
		// Gone again, after the interval: one look, the mark stands and the interval starts again.
		_ = f.mem.Delete(context.Background(), key)
		clock = clock.Add(2 * time.Second)
		if f.store.RecheckMissing(context.Background(), sp.SHA256, sp.Size) || heads() != before+1 || f.db.rows[sp.SHA256].status != "missing" {
			t.Fatalf("after the interval: %d heads, status %s", heads()-before, f.db.rows[sp.SHA256].status)
		}
		if f.store.RecheckMissing(context.Background(), sp.SHA256, sp.Size) || heads() != before+1 {
			t.Fatal("the interval did not start again")
		}
		// Back with another size: not ok.
		f.mem.Set(key, data[:10], f.mem.Now())
		clock = clock.Add(blob.MissingRecheckInterval)
		if f.store.RecheckMissing(context.Background(), sp.SHA256, sp.Size) || f.db.rows[sp.SHA256].status != "missing" {
			t.Fatal("an object of another size made the row ok")
		}
		// Back unchanged, after the interval: ok, and the read serves it.
		f.mem.Set(key, data, f.mem.Now())
		clock = clock.Add(blob.MissingRecheckInterval)
		if !f.store.RecheckMissing(context.Background(), sp.SHA256, sp.Size) || f.db.rows[sp.SHA256].status != "ok" {
			t.Fatalf("status %s after the object came back", f.db.rows[sp.SHA256].status)
		}
		r, err := f.store.Open(context.Background(), sp.SHA256, sp.Size, blob.OpenOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if got := readAll(t, r); !bytes.Equal(got, data) {
			t.Fatal("the object that came back differs")
		}
		// A row that is ok is never rechecked.
		if f.store.RecheckMissing(context.Background(), sp.SHA256, sp.Size) {
			t.Fatal("an ok row was rechecked")
		}
	})
	t.Run("R-STOR-013 an object of the wrong length is refused before any byte and marked corrupt", func(t *testing.T) {
		f, sp, _ := setup(t, nil)
		f.mem.Set(f.store.Key(sp.SHA256), data[:len(data)-1], f.mem.Now())
		_, err := f.store.Open(context.Background(), sp.SHA256, sp.Size, blob.OpenOptions{})
		if !errors.Is(err, blob.ErrCorrupt) || f.db.rows[sp.SHA256].status != "corrupt" {
			t.Fatalf("err=%v status=%s", err, f.db.rows[sp.SHA256].status)
		}
	})
	t.Run("R-STOR-013 an unknown object size is not damage: the digest at the end of the body decides", func(t *testing.T) {
		f, sp, damaged := setup(t, nil)
		f.mem.UnknownSize = true
		r, err := f.store.Open(context.Background(), sp.SHA256, sp.Size, blob.OpenOptions{})
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if got := readAll(t, r); !bytes.Equal(got, data) {
			t.Fatal("the download differs")
		}
		if f.db.rows[sp.SHA256].status != "ok" || damaged.Load() != 0 {
			t.Fatalf("status %s, damaged hook %d", f.db.rows[sp.SHA256].status, damaged.Load())
		}
		// A body that is damaged is still caught, by its digest.
		bad := bytes.Clone(data)
		bad[10] ^= 0xff
		f.mem.Set(f.store.Key(sp.SHA256), bad, f.mem.Now())
		r, err = f.store.Open(context.Background(), sp.SHA256, sp.Size, blob.OpenOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadAll(r); !errors.Is(err, blob.ErrCorrupt) {
			t.Fatalf("read error: %v", err)
		}
		r.Close()
	})
	t.Run("R-STOR-013 a transport error aborts the download but leaves the status alone", func(t *testing.T) {
		f, sp, damaged := setup(t, nil)
		boom := errors.New("connection reset by peer")
		f.mem.SetGetBody(func(rc io.ReadCloser) io.ReadCloser {
			return &failingBody{rc: rc, after: 300 << 10, err: boom}
		})
		r, err := f.store.Open(context.Background(), sp.SHA256, sp.Size, blob.OpenOptions{})
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.ReadAll(r)
		r.Close()
		if !errors.Is(err, boom) || errors.Is(err, blob.ErrCorrupt) {
			t.Fatalf("got %v", err)
		}
		if f.db.rows[sp.SHA256].status != "ok" || damaged.Load() != 0 {
			t.Fatal("a network fault changed the blob's status")
		}
		f.mem.SetGetBody(nil)
		r, err = f.store.Open(context.Background(), sp.SHA256, sp.Size, blob.OpenOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if got := readAll(t, r); !bytes.Equal(got, data) {
			t.Fatal("the next download differs")
		}
	})
	t.Run("R-STOR-013 verification can be turned off", func(t *testing.T) {
		f, sp, _ := setup(t, func(c *config.Storage) { c.Download.Verify = false })
		bad := bytes.Clone(data)
		bad[0] ^= 1
		f.mem.Set(f.store.Key(sp.SHA256), bad, f.mem.Now())
		r, err := f.store.Open(context.Background(), sp.SHA256, sp.Size, blob.OpenOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if got := readAll(t, r); !bytes.Equal(got, bad) {
			t.Fatal("unexpected content")
		}
	})
	t.Run("R-STOR-013 a ranged read is returned as stored and is not verified", func(t *testing.T) {
		f, sp, _ := setup(t, nil)
		r, err := f.store.Open(context.Background(), sp.SHA256, sp.Size, blob.OpenOptions{Range: &blob.ByteRange{Offset: 10, Length: 100}})
		if err != nil {
			t.Fatal(err)
		}
		if got := readAll(t, r); !bytes.Equal(got, data[10:110]) {
			t.Fatal("range differs")
		}
	})
	t.Run("R-STOR-013 WriteTo, which a download streams through, sends the whole intact object and of a corrupted one never the held-back tail", func(t *testing.T) {
		f, sp, _ := setup(t, nil)
		r, err := f.store.Open(context.Background(), sp.SHA256, sp.Size, blob.OpenOptions{})
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if n, err := r.(io.WriterTo).WriteTo(&out); err != nil || n != int64(len(data)) || !bytes.Equal(out.Bytes(), data) {
			t.Fatalf("wrote %d bytes, err %v", n, err)
		}
		r.Close()
		bad := bytes.Clone(data)
		bad[len(bad)-1] ^= 0xff
		f.mem.Set(f.store.Key(sp.SHA256), bad, f.mem.Now())
		r, err = f.store.Open(context.Background(), sp.SHA256, sp.Size, blob.OpenOptions{})
		if err != nil {
			t.Fatal(err)
		}
		out.Reset()
		n, err := r.(io.WriterTo).WriteTo(&out)
		r.Close()
		if !errors.Is(err, blob.ErrCorrupt) || n > int64(len(data)-(64<<10)) || !bytes.Equal(out.Bytes(), data[:n]) {
			t.Fatalf("wrote %d of %d bytes, err %v", n, len(data), err)
		}
	})
	t.Run("R-STOR-013 a reader that asks for one byte at a time gets the object through the reader's own buffer", func(t *testing.T) {
		f, sp, _ := setup(t, nil)
		r, err := f.store.Open(context.Background(), sp.SHA256, sp.Size, blob.OpenOptions{})
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		got, err := io.ReadAll(iotest.OneByteReader(r))
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("got %d bytes, err %v", len(got), err)
		}
		if _, err := r.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
			t.Fatalf("after the end: %v", err)
		}
	})
	t.Run("R-STOR-013 a body longer than the blob row releases nothing past the row's end and is marked corrupt", func(t *testing.T) {
		f, sp, damaged := setup(t, nil)
		f.mem.UnknownSize = true
		f.mem.Set(f.store.Key(sp.SHA256), append(bytes.Clone(data), content(300<<10, 23)...), f.mem.Now())
		r, err := f.store.Open(context.Background(), sp.SHA256, sp.Size, blob.OpenOptions{})
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(r)
		r.Close()
		if !errors.Is(err, blob.ErrCorrupt) || len(got) > len(data) || !bytes.Equal(got, data[:len(got)]) {
			t.Fatalf("got %d bytes, err %v", len(got), err)
		}
		if f.db.rows[sp.SHA256].status != "corrupt" || damaged.Load() != 1 {
			t.Fatalf("status %s, damaged hook %d", f.db.rows[sp.SHA256].status, damaged.Load())
		}
	})
	t.Run("R-STOR-013 a read after Close fails instead of touching the released buffer", func(t *testing.T) {
		f, sp, _ := setup(t, nil)
		r, err := f.store.Open(context.Background(), sp.SHA256, sp.Size, blob.OpenOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.Read(make([]byte, 10)); err != nil {
			t.Fatal(err)
		}
		r.Close()
		if n, err := r.Read(make([]byte, 10)); n != 0 || err == nil {
			t.Fatalf("read after close: %d, %v", n, err)
		}
	})
	t.Run("R-STOR-013 an object shorter than the hold-back is held until the digest matched", func(t *testing.T) {
		f := newFixture(t, func(c *config.Storage) { c.Download.HoldBack = 64 << 10 })
		small := content(100, 22)
		sp := f.ingest(t, small)
		if _, err := f.store.Commit(context.Background(), sp, noLink); err != nil {
			t.Fatal(err)
		}
		bad := bytes.Clone(small)
		bad[5] ^= 1
		f.mem.Set(f.store.Key(sp.SHA256), bad, f.mem.Now())
		r, err := f.store.Open(context.Background(), sp.SHA256, sp.Size, blob.OpenOptions{})
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(r)
		if !errors.Is(err, blob.ErrCorrupt) || len(got) != 0 {
			t.Fatalf("got %d bytes, err %v", len(got), err)
		}
	})
}

type failingBody struct {
	rc    io.ReadCloser
	after int
	read  int
	err   error
}

func (b *failingBody) Read(p []byte) (int, error) {
	if b.read >= b.after {
		return 0, b.err
	}
	n, err := b.rc.Read(p[:min(len(p), b.after-b.read)])
	b.read += n
	return n, err
}
func (b *failingBody) Close() error { return b.rc.Close() }
