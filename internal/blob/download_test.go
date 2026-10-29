package blob_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"

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
	t.Run("R-STOR-013 an object of the wrong length is refused before any byte and marked corrupt", func(t *testing.T) {
		f, sp, _ := setup(t, nil)
		f.mem.Set(f.store.Key(sp.SHA256), data[:len(data)-1], f.mem.Now())
		_, err := f.store.Open(context.Background(), sp.SHA256, sp.Size, blob.OpenOptions{})
		if !errors.Is(err, blob.ErrCorrupt) || f.db.rows[sp.SHA256].status != "corrupt" {
			t.Fatalf("err=%v status=%s", err, f.db.rows[sp.SHA256].status)
		}
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
