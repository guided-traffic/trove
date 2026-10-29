package blob

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
	"time"

	"github.com/guided-traffic/trove/internal/meta"
)

// OpenOptions select what Open returns.
type OpenOptions struct {
	// Range reads part of the blob. Ranged reads cannot be verified (R-STOR-013).
	Range *ByteRange
}

// Open opens the content of a blob for reading. size is the blob row's size.
//
// A full read is verified unless storage.download.verify is false
// (R-STOR-013, Q-STOR-6): the SHA-256 of the bytes is computed while they are
// read and the last storage.download.hold_back bytes are not released before the
// body has ended and the digest matched. On a mismatch the reader returns
// ErrCorrupt instead of the held bytes, so the caller aborts the response and
// the client sees a truncated transfer, never a complete corrupted file.
//
// Only evidence about the stored object marks the blob corrupt: a Content-Length
// different from the blob's size, or a body read to its end with another digest.
// A transport error aborts the same way but changes no status.
func (s *Store) Open(ctx context.Context, d Digest, size int64, o OpenOptions) (io.ReadCloser, error) {
	body, info, err := s.objects.Get(ctx, s.Key(d), o.Range)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	if o.Range != nil || !s.cfg.Download.Verify {
		return body, nil
	}
	if info.Size != size {
		_ = body.Close()
		s.markDamaged(ctx, d, "corrupt")
		return nil, fmt.Errorf("%w: the object has %d bytes, the blob row says %d", ErrCorrupt, info.Size, size)
	}
	return &verifyReader{
		src: body, store: s, ctx: ctx, digest: d, size: size,
		hash: sha256.New(), hold: int(s.cfg.Download.HoldBack),
		chunk: make([]byte, max(4096, int(s.cfg.Download.BufferSize))),
	}, nil
}

// markDamaged records a detection: content_status leaves 'ok', the counter
// grows and the hook (the audit event) runs. It only ever changes a live row
// that was ok, so a repair or a concurrent detection is not overwritten.
func (s *Store) markDamaged(ctx context.Context, d Digest, status string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	var changed int64
	err := s.db.Tx(ctx, func(tx meta.Tx) error {
		var err error
		changed, err = tx.Exec(ctx, sqlMarkDamaged, d.Bytes(), status)
		return err
	})
	if err != nil {
		s.log.Error("could not mark a blob damaged", "sha256", d.Hex(), "status", status, "error", err)
		return
	}
	if changed > 0 {
		s.metrics.damage(status)
		s.log.Error("stored object failed verification", "sha256", d.Hex(), "status", status)
		if s.onDamage != nil {
			s.onDamage(ctx, d, status)
		}
	}
}

// verifyReader hashes what it reads and withholds the last hold bytes.
type verifyReader struct {
	src    io.ReadCloser
	store  *Store
	ctx    context.Context
	digest Digest
	size   int64
	hash   hash.Hash
	hold   int

	chunk []byte // read buffer
	held  []byte // the withheld tail, at most hold bytes
	ready []byte // bytes cleared for the caller
	work  []byte
	total int64
	done  bool  // the source ended and the digest matched
	err   error // sticky
}

func (v *verifyReader) Read(p []byte) (int, error) {
	for {
		if len(v.ready) > 0 {
			n := copy(p, v.ready)
			v.ready = v.ready[n:]
			return n, nil
		}
		if v.err != nil {
			return 0, v.err
		}
		if v.done {
			return 0, io.EOF
		}
		n, err := v.src.Read(v.chunk)
		if n > 0 {
			v.hash.Write(v.chunk[:n])
			v.total += int64(n)
			// held + new bytes; everything but the last hold bytes is cleared.
			v.work = append(append(v.work[:0], v.held...), v.chunk[:n]...)
			if cut := len(v.work) - v.hold; cut > 0 {
				v.ready = v.work[:cut]
				v.held = append(v.held[:0], v.work[cut:]...)
			} else {
				v.held = append(v.held[:0], v.work...)
			}
		}
		switch {
		case err == nil:
		case errors.Is(err, io.EOF):
			if verr := v.finish(); verr != nil {
				v.err = verr
				v.ready = nil
			} else {
				v.ready = append(v.ready, v.held...) // work still backs ready
				v.held = v.held[:0]
				v.done = true
			}
		default:
			v.err = err // a transport error: abort, no status change
			v.ready = nil
		}
	}
}

// finish compares the complete body with the blob row.
func (v *verifyReader) finish() error {
	var sum Digest
	v.hash.Sum(sum[:0])
	if v.total == v.size && sum == v.digest {
		return nil
	}
	v.store.markDamaged(v.ctx, v.digest, "corrupt")
	return fmt.Errorf("%w: read %d bytes with SHA-256 %s, expected %d bytes with %s", ErrCorrupt, v.total, sum.Hex(), v.size, v.digest.Hex())
}

// Close closes the source.
func (v *verifyReader) Close() error { return v.src.Close() }
