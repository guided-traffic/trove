package cache

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/guided-traffic/trove/internal/bastion/metrics"
)

// TestR_BAS_032_WithoutASigningKeyTheVolumeKeepsPlainEntriesAndStillVerifiesTheBytes: the configuration allows
// no key with the volume backend and no Valkey (R-BAS-032 requires it only with the bucket backend or Valkey), so
// the cache takes a nil signer (Q-BAS-98): the entries are plain JSON, the bytes are still verified against their
// content address while they are served, and a replaced object is still caught.
func TestR_BAS_032_WithoutASigningKeyTheVolumeKeepsPlainEntriesAndStillVerifiesTheBytes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "volume")
	m, err := metrics.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(context.Background(), testConfig(dir), nil, Options{Metrics: m})
	if err != nil {
		t.Fatalf("a cache without a signer: %v", err)
	}
	ctx := context.Background()
	data := payload(11, 400)
	f, err := c.NewFill(EntryArtifact)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := f.Commit(ctx, digest(data)); err != nil {
		t.Fatal(err)
	}
	e := Entry{Kind: EntryArtifact, SHA256: digest(data), Size: int64(len(data)), Repository: "npm-all", Status: 200}
	if err := c.StoreEntry(ctx, "/k", e); err != nil {
		t.Fatal(err)
	}
	rec, err := os.ReadFile(filepath.Join(dir, "entries", KeyHash("/k")[:2], KeyHash("/k")+".json"))
	if err != nil || !bytes.HasPrefix(rec, []byte("{")) || bytes.Contains(rec, []byte("\n")) {
		t.Fatalf("the record of an unsigned entry is plain JSON: %q %v", rec, err)
	}
	got, _, err := c.LookupEntry(ctx, "/k")
	if err != nil || got.SHA256 != e.SHA256 {
		t.Fatalf("lookup: %+v %v", got, err)
	}
	rc, err := c.Open(ctx, "/k", got)
	if err != nil {
		t.Fatal(err)
	}
	if b, err := io.ReadAll(rc); err != nil || !bytes.Equal(b, data) {
		t.Fatalf("read: %d bytes, %v", len(b), err)
	}
	_ = rc.Close()

	// A replaced object is caught by the digest, key or no key.
	bad := append([]byte(nil), data...)
	bad[0] ^= 0xff
	obj, _ := objectKey(EntryArtifact, e.SHA256)
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(obj)), bad, 0o640); err != nil {
		t.Fatal(err)
	}
	rc, err = c.Open(ctx, "/k", got)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(rc); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("a replaced object was served: %v", err)
	}
}

// TestR_BAS_021_AKeptFillCanStillAnswerAfterTheCommitWasRefused: Keep leaves the spooled bytes readable after
// Commit, so that a caller whose backend refused the write can still answer (R-BAS-030); Abort releases the file.
func TestR_BAS_021_AKeptFillCanStillAnswerAfterTheCommitWasRefused(t *testing.T) {
	r := newRig(t)
	fl := &Fault{}
	r.back.s = &faultStore{store: r.back.s, f: fl}
	data := payload(12, 300)

	f, err := r.NewFill(EntryArtifact)
	if err != nil {
		t.Fatal(err)
	}
	f.Keep()
	if _, err := f.Write(data); err != nil {
		t.Fatal(err)
	}
	fl.SetFailPut(true)
	if err := f.Commit(context.Background(), digest(data)); err == nil {
		t.Fatal("the commit succeeded on a backend that refuses writes")
	}
	b, err := io.ReadAll(f.Reader())
	if err != nil || !bytes.Equal(b, data) {
		t.Fatalf("the kept bytes: %d bytes, %v", len(b), err)
	}
	f.Abort()
	if left, _ := os.ReadDir(r.back.s.tmpDir()); len(left) != 0 {
		t.Fatalf("%d spool files left after Abort", len(left))
	}

	// Without Keep a commit removes the spool file, as before.
	fl.SetFailPut(false)
	g, err := r.NewFill(EntryArtifact)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = g.Write(data)
	if err := g.Commit(context.Background(), digest(data)); err != nil {
		t.Fatal(err)
	}
	if left, _ := os.ReadDir(r.back.s.tmpDir()); len(left) != 0 {
		t.Fatalf("%d spool files left after Commit", len(left))
	}
}
