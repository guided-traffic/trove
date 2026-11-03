package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"os"
	"sync"

	"github.com/guided-traffic/trove/internal/bastion/metrics"
)

// Fill is a fill in progress (R-BAS-021): the bytes of a response are written to it while they are streamed to
// the caller, in a temporary file of the volume or of the pod, and hashed as they come. It is committed under
// its content address only after the caller knows the SHA-256 matches; until then nothing is visible to a
// reader. A fill that is not committed is aborted, which removes its file.
type Fill struct {
	c     *Cache
	kind  EntryKind
	limit int64

	mu   sync.Mutex
	f    *os.File
	name string
	h    hash.Hash
	n    int64
	done bool
}

// NewFill starts a fill for the bytes of an artifact or a shared document. The fill refuses bytes past
// bastion.cache.max_object_bytes (artifact) or bastion.cache.documents_max_bytes (document) with
// [ErrTooLarge]; the caller then passes the answer through and keeps nothing.
func (c *Cache) NewFill(kind EntryKind) (*Fill, error) {
	limit := int64(c.cfg.MaxObjectBytes)
	if kind == EntryDocument {
		limit = int64(c.cfg.DocumentsMaxBytes)
	}
	f, err := os.CreateTemp(c.back.s.tmpDir(), "fill-*")
	if err != nil {
		return nil, c.back.fail(metrics.OpPut, err)
	}
	return &Fill{c: c, kind: kind, limit: limit, f: f, name: f.Name(), h: sha256.New()}, nil
}

// Write implements io.Writer.
func (f *Fill) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.done {
		return 0, os.ErrClosed
	}
	if f.n+int64(len(p)) > f.limit {
		return 0, ErrTooLarge
	}
	n, err := f.f.Write(p)
	f.h.Write(p[:n])
	f.n += int64(n)
	if err != nil {
		return n, f.c.back.fail(metrics.OpPut, err)
	}
	return n, nil
}

// Size is the number of bytes written.
func (f *Fill) Size() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
}

// SHA256 is the digest of the bytes written so far, in hex.
func (f *Fill) SHA256() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return hex.EncodeToString(f.h.Sum(nil))
}

// Commit stores the bytes under their content address, if their SHA-256 is sha; otherwise it aborts the fill
// and returns [ErrMismatch]. The fill is finished either way. The entry that points at the bytes is written by
// the caller after a successful commit ([Cache.StoreEntry]).
func (f *Fill) Commit(ctx context.Context, sha string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.done {
		return os.ErrClosed
	}
	f.done = true
	defer func() { _ = os.Remove(f.name) }()
	if err := f.f.Close(); err != nil {
		return f.c.back.fail(metrics.OpPut, err)
	}
	got := f.h.Sum(nil)
	if hex.EncodeToString(got) != sha {
		return fmt.Errorf("%w: the fill has SHA-256 %s, expected %s", ErrMismatch, hex.EncodeToString(got), sha)
	}
	return f.c.back.commitFile(WithArea(ctx, f.kind), sha, f.n, f.name, got)
}

// Abort discards the fill. It is safe to call after Commit.
func (f *Fill) Abort() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.done {
		return
	}
	f.done = true
	_ = f.f.Close()
	_ = os.Remove(f.name)
}
