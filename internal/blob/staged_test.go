package blob_test

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // the payload checksum of a segment
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/blob/blobtest"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta"
)

// stagedBlob puts data under a staging key and returns the Staged blob that names it with the digests of the whole content.
func stagedBlob(t *testing.T, f *fixture, data []byte, key string) *blob.Spooled {
	t.Helper()
	f.mem.Set(key, data, f.mem.Now())
	whole := f.ingest(t, data)
	return blob.Staged(whole.Digests, key)
}

func TestR_STOR_011_AStagedBlobIsCommittedByAServerSideCopy(t *testing.T) {
	ctx := context.Background()
	t.Run("the object write of the commit protocol is a copy to the content address", func(t *testing.T) {
		f := newFixture(t, nil)
		data := content(40_000, 3)
		key := blob.SessionKey("trove", "s1", "data")
		staged := stagedBlob(t, f, data, key)
		var linked int
		res, err := f.store.Commit(ctx, staged, func(context.Context, meta.Tx) error { linked++; return nil })
		if err != nil {
			t.Fatal(err)
		}
		if !res.Wrote || !res.Inserted || linked != 1 {
			t.Fatalf("result %+v, linked %d", res, linked)
		}
		if f.mem.Count("copy") != 1 || f.mem.Count("put") != 0 {
			t.Fatalf("copies %d, puts %d", f.mem.Count("copy"), f.mem.Count("put"))
		}
		if got, ok := f.mem.Data(f.store.Key(staged.SHA256)); !ok || !bytes.Equal(got, data) {
			t.Fatal("the content address does not hold the bytes")
		}
		if staged.StagedKey() != key {
			t.Fatalf("StagedKey = %q", staged.StagedKey())
		}
		if err := staged.Close(); err != nil {
			t.Fatalf("closing a staged blob: %v", err)
		}
	})
	t.Run("a blob that is stored already copies nothing, even if the staging object is gone", func(t *testing.T) {
		f := newFixture(t, nil)
		data := content(9_000, 4)
		if _, err := f.store.Commit(ctx, f.ingest(t, data), noLink); err != nil {
			t.Fatal(err)
		}
		staged := blob.Staged(f.ingest(t, data).Digests, "trove/_uploads/gone/data")
		res, err := f.store.Commit(ctx, staged, noLink)
		if err != nil {
			t.Fatal(err)
		}
		if res.Wrote || !res.Deduplicated || f.mem.Count("copy") != 0 {
			t.Fatalf("result %+v, copies %d", res, f.mem.Count("copy"))
		}
	})
	t.Run("a staging object that is gone is ErrStagingGone", func(t *testing.T) {
		f := newFixture(t, nil)
		staged := blob.Staged(f.ingest(t, content(100, 5)).Digests, "trove/_uploads/gone/data")
		_, err := f.store.Commit(ctx, staged, noLink)
		if !errors.Is(err, blob.ErrStagingGone) {
			t.Fatalf("err = %v", err)
		}
		if len(f.db.rows) != 0 {
			t.Fatalf("a row was inserted for content that was never stored")
		}
	})
	t.Run("a staged blob has no bytes to read", func(t *testing.T) {
		staged := blob.Staged(blob.Digests{Size: 10}, "k")
		if _, err := staged.Reader().Read(make([]byte, 1)); err == nil {
			t.Fatal("read from a staged blob")
		}
	})
}

func TestR_STOR_011_SessionKeys(t *testing.T) {
	for _, prefix := range []string{"trove", "", "a/b"} {
		id := "0b9f1b0e-1111-4222-8333-444455556666"
		key := blob.SessionKey(prefix, id, "tail-x")
		if !strings.HasPrefix(key, blob.SessionPrefix(prefix, id)) || !strings.HasPrefix(key, blob.UploadsPrefix(prefix)) {
			t.Fatalf("%q: key %q", prefix, key)
		}
		gotID, name, ok := blob.ParseSessionKey(prefix, key)
		if !ok || gotID != id || name != "tail-x" {
			t.Fatalf("%q: parsed %q %q %v", prefix, gotID, name, ok)
		}
		// a blob key is no session key
		if _, _, ok := blob.ParseSessionKey(prefix, blob.Key(prefix, blob.Digest{1})); ok {
			t.Fatalf("%q: a blob key parsed as a session key", prefix)
		}
	}
	for _, bad := range []string{"trove/_uploads/", "trove/_uploads/id", "trove/_uploads/id/", "trove/_uploads//x", "other/_uploads/id/x"} {
		if _, _, ok := blob.ParseSessionKey("trove", bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestR_STOR_011_IngestSegment(t *testing.T) {
	ctx := context.Background()
	newSpool := func(t *testing.T, threshold int64) *blob.Spool {
		t.Helper()
		cfg := blobtest.Storage(t, t.TempDir()).Spool
		cfg.MemoryThreshold = config.ByteSize(threshold)
		sp, err := blob.NewSpool(cfg)
		if err != nil {
			t.Fatal(err)
		}
		return sp
	}
	data := content(1000, 9)
	read := func(t *testing.T, s *blob.Spooled) []byte {
		t.Helper()
		b, err := io.ReadAll(s.Reader())
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	for _, threshold := range []int64{1 << 20, 100} { // in memory, and a spool file
		name := "in memory"
		if threshold == 100 {
			name = "in a spool file"
		}
		t.Run(name+": the prefix comes first and only the body goes through the tee", func(t *testing.T) {
			sp := newSpool(t, threshold)
			var teed []byte
			seg, err := sp.IngestSegment(ctx, bytes.NewReader(data[:300]), bytes.NewReader(data[300:]), 800, func(p []byte) error {
				teed = append(teed, p...)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if seg.Size != 800 || !bytes.Equal(read(t, seg), data[:800]) {
				t.Fatalf("segment of %d bytes differs from the first 800", seg.Size)
			}
			if !bytes.Equal(teed, data[300:800]) {
				t.Fatalf("the tee saw %d bytes, want the 500 of the body that were kept", len(teed))
			}
			sum := sha256.Sum256(data[:800])
			md := md5.Sum(data[:800]) //nolint:gosec // see imports
			if seg.SHA256 != sum || seg.MD5 != md {
				t.Fatal("the checksums of the segment are not those of its bytes")
			}
			if err := seg.Close(); err != nil {
				t.Fatal(err)
			}
			if sp.Reserved() != 0 {
				t.Fatalf("%d bytes of budget left", sp.Reserved())
			}
		})
		t.Run(name+": a body shorter than the segment ends it", func(t *testing.T) {
			sp := newSpool(t, threshold)
			seg, err := sp.IngestSegment(ctx, nil, bytes.NewReader(data[:123]), 800, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer seg.Close()
			if seg.Size != 123 || !bytes.Equal(read(t, seg), data[:123]) {
				t.Fatalf("segment of %d bytes", seg.Size)
			}
		})
		t.Run(name+": an error of the tee leaves nothing behind", func(t *testing.T) {
			sp := newSpool(t, threshold)
			stop := errors.New("stop")
			n := 0
			_, err := sp.IngestSegment(ctx, nil, bytes.NewReader(data), 800, func(p []byte) error {
				if n += len(p); n > 400 {
					return stop
				}
				return nil
			})
			if !errors.Is(err, stop) {
				t.Fatalf("err = %v", err)
			}
			if sp.Reserved() != 0 || len(dirEntries(t, sp.Dir())) != 0 {
				t.Fatalf("budget %d, files %v", sp.Reserved(), dirEntries(t, sp.Dir()))
			}
		})
	}
	t.Run("a read error of the body is returned and cleaned up", func(t *testing.T) {
		sp := newSpool(t, 100)
		boom := errors.New("connection reset")
		body := io.MultiReader(bytes.NewReader(data[:500]), errReader{boom})
		if _, err := sp.IngestSegment(ctx, nil, body, 800, nil); !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
		if sp.Reserved() != 0 || len(dirEntries(t, sp.Dir())) != 0 {
			t.Fatalf("budget %d, files %v", sp.Reserved(), dirEntries(t, sp.Dir()))
		}
	})
	t.Run("a cancelled context stops the segment", func(t *testing.T) {
		sp := newSpool(t, 100)
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := sp.IngestSegment(cctx, nil, bytes.NewReader(data), 800, nil); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	})
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}
