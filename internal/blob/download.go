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
// A transport error aborts the same way but changes no status. A store that answers
// that the object does not exist marks the blob missing and returns ErrNotFound
// (R-HA-024, R-STOR-027).
//
// The reader is charged to the memory of the request of ctx (WithMemory) from
// before the object store is asked until it is closed: DownloadCost of the
// object, or RangeCost of the range.
func (s *Store) Open(ctx context.Context, d Digest, size int64, o OpenOptions) (io.ReadCloser, error) {
	undo := func() {}
	if s.memory != nil {
		cost := DownloadCost(s.cfg, size)
		if o.Range != nil {
			cost = RangeCost(s.cfg, size, *o.Range)
		}
		undo = s.memory(ctx, cost)
	}
	body, info, err := s.objects.Get(ctx, s.Key(d), o.Range)
	if err != nil {
		undo()
		if errors.Is(err, ErrNotFound) {
			s.markMissing(ctx, d)
		}
		return nil, mapStoreErr(err)
	}
	if o.Range != nil || !s.cfg.Download.Verify {
		return &chargedBody{ReadCloser: body, undo: undo}, nil
	}
	if info.Size >= 0 && info.Size != size { // -1: the response did not say; the digest check at the end decides
		_ = body.Close()
		undo()
		s.markDamaged(ctx, d, "corrupt")
		return nil, fmt.Errorf("%w: the object has %d bytes, the blob row says %d", ErrCorrupt, info.Size, size)
	}
	return &verifyReader{
		src: body, store: s, ctx: ctx, digest: d, size: size, undo: undo,
		hash: sha256.New(), hold: int(s.cfg.Download.HoldBack),
		capacity: verifyBuffer(int(s.cfg.Download.BufferSize), int(s.cfg.Download.HoldBack), size),
	}, nil
}

// chargedBody gives the memory of a reader back when it is closed.
type chargedBody struct {
	io.ReadCloser
	undo func()
}

func (c *chargedBody) Close() error {
	err := c.ReadCloser.Close()
	c.undo()
	return err
}

// verifyBuffer is the length of a verified download's one buffer:
// storage.download.buffer_size, at least twice the hold-back so that a refill
// clears at least as much as it keeps, and no more than the object plus the byte
// that would show it is longer (Q-ARCH-241).
func verifyBuffer(bufferSize, hold int, size int64) int {
	c := max(bufferSize, 2*hold, 4096)
	if size < int64(c) {
		c = int(size) + 1
	}
	return c
}

// markMissing records a read the store answered with "no such object": evidence about the object, as the 404 of the
// scrub's existence check is (R-STOR-027). The row says missing from then on, every reader of the blob sees it in
// PostgreSQL, and a commit of the same content writes the object again (R-STOR-005, R-HA-024).
//
// A commit of the same content may have written the object again between the read and the mark; the mark, which
// changes only an ok row, would then undo that repair. So the object is looked for once more after a mark, and a row
// whose object is there again is ok (Q-STOR-151). Whichever order the repair and the mark take, the row ends ok when
// the object is there: a repair after the look sets ok itself.
func (s *Store) markMissing(ctx context.Context, d Digest) {
	if !s.markDamaged(ctx, d, "missing") {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if _, err := s.objects.Head(ctx, s.Key(d)); err != nil {
		return // gone, or the store cannot say: the mark stays
	}
	if err := s.db.InTx(ctx, func(tx meta.Tx) error { return s.rows.MarkOK(ctx, tx, d) }); err != nil {
		s.log.Error("could not mark a blob ok whose object is there again", "sha256", d.Hex(), "error", err)
		return
	}
	s.log.Info("the object of a blob that a read found missing is there again", "sha256", d.Hex())
}

// MissingRecheckInterval is how long a blob marked missing is answered from PostgreSQL alone before a read asks the object
// store again (Q-STOR-153): the Retry-After a client is given for a missing object, so that the client that retries is the one
// whose read looks. Per blob and across replicas, the store is asked at most once per interval.
const MissingRecheckInterval = 30 * time.Second

// RecheckMissing looks for the object of a blob marked missing once more, so that a mark does not outlive the loss it
// records (Q-STOR-153): an object that is back (an operator restored it, or the store answered 404 for objects that exist,
// as with a wrong storage.s3.prefix that was corrected) turns the row ok, and the caller serves it. At most one read per blob
// and MissingRecheckInterval asks the store, whichever replica: the read whose statement moves the row's checked_at, the time
// of its last existence check (R-STOR-027); every other read is refused from PostgreSQL as before (R-STOR-012). It reports
// whether the row is ok now; a blob that is not marked missing, an object that is still gone, one of another size and a store
// that cannot answer are false, and the interval starts again.
func (s *Store) RecheckMissing(ctx context.Context, d Digest, size int64) bool {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	var claimed bool
	if err := s.db.InTx(ctx, func(tx meta.Tx) error {
		var err error
		claimed, err = s.rows.ClaimRecheck(ctx, tx, d, MissingRecheckInterval)
		return err
	}); err != nil {
		s.log.Warn("could not claim the recheck of a blob marked missing", "sha256", d.Hex(), "error", err)
		return false
	}
	if !claimed {
		return false
	}
	info, err := s.objects.Head(ctx, s.Key(d))
	switch {
	case errors.Is(err, ErrNotFound):
		return false
	case err != nil:
		s.log.Warn("could not recheck a blob marked missing", "sha256", d.Hex(), "error", err)
		return false
	case info.Size >= 0 && info.Size != size:
		s.log.Error("the object of a blob marked missing is back with another size", "sha256", d.Hex(), "size", info.Size, "want", size)
		return false
	}
	if err := s.db.InTx(ctx, func(tx meta.Tx) error { return s.rows.MarkOK(ctx, tx, d) }); err != nil {
		s.log.Error("could not mark a blob ok whose object is there again", "sha256", d.Hex(), "error", err)
		return false
	}
	s.log.Info("the object of a blob marked missing is there again", "sha256", d.Hex())
	return true
}

// markDamaged records a detection: content_status leaves 'ok', the counter
// grows and the hook (the audit event) runs. It only ever changes a live row
// that was ok, so a repair or a concurrent detection is not overwritten. It
// reports whether the row changed.
func (s *Store) markDamaged(ctx context.Context, d Digest, status string) bool {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	var changed bool
	err := s.db.InTx(ctx, func(tx meta.Tx) error {
		var err error
		changed, err = s.rows.MarkDamaged(ctx, tx, d, status)
		return err
	})
	if err != nil {
		s.log.Error("could not mark a blob damaged", "sha256", d.Hex(), "status", status, "error", err)
		return false
	}
	if changed {
		s.metrics.damage(status)
		msg := "stored object failed verification"
		if status == "missing" {
			msg = "the object of a blob is missing from the object store"
		}
		s.log.Error(msg, "sha256", d.Hex(), "status", status)
		if s.onDamage != nil {
			s.onDamage(ctx, d, status)
		}
	}
	return changed
}

// verifyReader hashes what it reads and withholds the last hold bytes. It reads
// into one pooled buffer: buf[off:ready] is cleared and not yet handed out,
// buf[ready:k] is the withheld tail, which the next refill moves to the front.
type verifyReader struct {
	src      io.ReadCloser
	undo     func()
	store    *Store
	ctx      context.Context
	digest   Digest
	size     int64
	hash     hash.Hash
	hold     int
	capacity int

	buf           *[]byte // from Buffer on the first read, back with Close
	off, ready, k int
	total         int64
	done          bool  // the source ended and the digest matched
	err           error // sticky
}

func (v *verifyReader) Read(p []byte) (int, error) {
	for {
		if v.off < v.ready {
			n := copy(p, (*v.buf)[v.off:v.ready])
			v.off += n
			return n, nil
		}
		if v.err != nil {
			return 0, v.err
		}
		if v.done {
			return 0, io.EOF
		}
		v.fill()
	}
}

// WriteTo writes the cleared bytes straight from the buffer, so a download needs
// no copy buffer of its own.
func (v *verifyReader) WriteTo(w io.Writer) (int64, error) {
	var written int64
	for {
		if v.off < v.ready {
			n, err := w.Write((*v.buf)[v.off:v.ready])
			v.off += n
			written += int64(n)
			if err != nil {
				return written, err
			}
			continue
		}
		if v.err != nil {
			return written, v.err
		}
		if v.done {
			return written, nil
		}
		v.fill()
	}
}

// fill moves the withheld tail to the front and reads until the buffer is full
// or the source ended. Then everything but the last hold bytes is cleared, or
// everything once the digest matched. A body longer than the blob has nothing
// more cleared; it is read to its end, where finish decides.
func (v *verifyReader) fill() {
	if v.buf == nil {
		v.buf = Buffer(v.capacity)
	}
	b := (*v.buf)[:v.capacity]
	v.k = copy(b, b[v.ready:v.k])
	v.off, v.ready = 0, 0
	for v.k < len(b) {
		n, err := v.src.Read(b[v.k:])
		if n > 0 {
			v.hash.Write(b[v.k : v.k+n])
			v.total += int64(n)
			v.k += n
		}
		switch {
		case err == nil:
		case errors.Is(err, io.EOF):
			if v.err = v.finish(); v.err == nil {
				v.ready, v.done = v.k, true
			}
			return
		default:
			v.err = err // a transport error: abort, no status change
			return
		}
	}
	if v.total > v.size {
		v.k = 0
		return
	}
	v.ready = max(0, v.k-v.hold)
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

// Close closes the source and releases the buffer.
func (v *verifyReader) Close() error {
	Release(v.buf)
	v.buf, v.off, v.ready, v.k = nil, 0, 0, 0
	if v.err == nil {
		v.err = errReadAfterClose
	}
	err := v.src.Close()
	if v.undo != nil {
		v.undo()
	}
	return err
}

var errReadAfterClose = errors.New("blob: read after close")
