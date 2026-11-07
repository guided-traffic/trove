package create

import (
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"math/rand/v2"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/format"
)

// listResult is what the diff pass produced.
type listResult struct {
	file       format.BlobsInfo // File, SHA256, Referenced, ReferencedBytes
	toCopy     int64
	toCopyByte int64
	sample     []format.Digest
	// damaged are the referenced blobs whose row is not ok in the snapshot (R-BAK-006 "Source damage"): listed, not copied.
	damaged []format.DamagedBlob
}

// statusIter remembers the referenced blobs whose row is not ok while the stream passes.
type statusIter struct {
	it  format.BlobIterator
	bad []format.DamagedBlob
}

func (s *statusIter) Next() (format.BlobEntry, error) {
	e, err := s.it.Next()
	if err == nil && e.Status != "" {
		s.bad = append(s.bad, format.DamagedBlob{SHA256: e.Digest.Hex(), Status: e.Status})
	}
	return e, err
}

// countIter counts the entries that pass.
type countIter struct {
	it format.BlobIterator
	n  int64
}

func (c *countIter) Next() (format.BlobEntry, error) {
	e, err := c.it.Next()
	if err == nil {
		c.n++
	}
	return e, err
}

// teeHash hashes what a reader delivers.
type teeHash struct {
	r io.Reader
	h hash.Hash
}

func (t *teeHash) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	t.h.Write(p[:n])
	return n, err
}

// sampler keeps a uniform sample of the digests that came from earlier backups (R-BAK-009 "Spot check"): a reservoir, so the
// blob list never has to be held. A digest the run copies itself is not a candidate; the diff tells which one it is only after
// the list callback, hence the pending entry.
type sampler struct {
	k       int
	seen    int64
	rng     *rand.Rand
	out     []format.Digest
	pending *format.Digest
}

func newSampler(k int) *sampler {
	var seed [16]byte
	_, _ = crand.Read(seed[:])
	return &sampler{k: k, rng: rand.New(rand.NewPCG(binary.LittleEndian.Uint64(seed[:8]), binary.LittleEndian.Uint64(seed[8:])))} //nolint:gosec // a sample, not a secret
}

func (s *sampler) listed(d format.Digest) {
	s.flush()
	s.pending = &d
}

func (s *sampler) copying() { s.pending = nil }

func (s *sampler) flush() {
	if s.pending == nil || s.k <= 0 {
		s.pending = nil
		return
	}
	d := *s.pending
	s.pending = nil
	s.seen++
	switch {
	case len(s.out) < s.k:
		s.out = append(s.out, d)
	default:
		if j := s.rng.Int64N(s.seen); j < int64(s.k) {
			s.out[j] = d
		}
	}
}

// diff is steps 3 and 4 of the hold protocol of R-BAK-007 around the merge of R-BAK-006: the referenced set of the snapshot is
// merged with the previous backup's list in one pass that writes the new blob list and inserts the to-copy set as pins, in
// batches that commit one by one. Then the hold turns from 'all' to 'pinned', in a transaction of its own.
func (r *runState) diff(ctx context.Context) error {
	rawRefs, err := r.snap.Referenced(ctx)
	if err != nil {
		return err
	}
	refs := &statusIter{it: rawRefs}
	var known format.BlobIterator = &format.EntryList{}
	var prevHash *teeHash
	var prevCount *countIter
	if r.prev != nil {
		rc, _, err := r.d.Target.Get(ctx, r.layout.BlobList(r.prev.id))
		if err != nil {
			return classify(fmt.Errorf("%w: the blob list of %s: %w", backup.ErrPoolDamaged, r.prev.id, err))
		}
		defer rc.Close()
		prevHash = &teeHash{r: rc, h: sha256.New()}
		rd, err := format.NewBlobListReader(prevHash)
		if err != nil {
			return fmt.Errorf("%w: the blob list of %s: %w", backup.ErrPoolDamaged, r.prev.id, err)
		}
		defer rd.Close()
		prevCount = &countIter{it: rd}
		skip := map[string]bool{}
		for sha := range r.prev.manifest.DamagedSet() {
			skip[sha] = true
		}
		known = format.Without(prevCount, skip)
	}

	w, err := r.newWriter(ctx, r.layout.BlobList(r.id))
	if err != nil {
		return classify(err)
	}
	sum := format.NewSumWriter(w)
	lw, err := format.NewBlobListWriter(sum, r.d.Cfg.CompressionLevel)
	if err != nil {
		return err
	}
	smp := newSampler(r.d.Cfg.VerifySample)
	var batch []format.BlobEntry
	var pinned, pinnedBytes int64
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := r.d.Store.AddPins(ctx, r.runID, batch); err != nil {
			return classify(fmt.Errorf("insert the pins: %w", err))
		}
		pinned += int64(len(batch))
		batch = batch[:0]
		n := pinned
		r.h.Update(func(p *backup.Progress) { p.BlobsToCopy, p.PinsLeft = n, n })
		return nil
	}
	err = format.Diff(refs, known,
		func(e format.BlobEntry) error {
			if e.Status == "" {
				smp.listed(e.Digest)
			}
			r.h.Tick()
			return lw.Add(e)
		},
		func(e format.BlobEntry) error {
			if e.Status != "" {
				return nil // damaged in the source: listed, not copied, so that the first backup after the repair copies it
			}
			smp.copying()
			pinnedBytes += e.Size
			batch = append(batch, e)
			if len(batch) >= r.d.PinBatch {
				return flush()
			}
			return nil
		})
	if err != nil {
		return err
	}
	smp.flush()
	if prevCount != nil {
		if err := r.verifyPreviousList(prevHash, prevCount); err != nil {
			return err
		}
	}
	if err := flush(); err != nil {
		return err
	}
	if err := lw.Close(); err != nil {
		return err
	}
	obj, err := w.Close(ctx)
	r.forget(w)
	if err != nil {
		return classify(fmt.Errorf("write the blob list: %w", err))
	}
	if obj.Size != sum.Bytes() {
		return fmt.Errorf("the target stored %d bytes of the blob list, %d were sent", obj.Size, sum.Bytes())
	}
	n, bytes := lw.Count()
	r.list = listResult{
		file:   format.BlobsInfo{File: format.BlobListName, SHA256: sum.SHA256(), Referenced: n, ReferencedBytes: bytes},
		toCopy: pinned, toCopyByte: pinnedBytes, sample: smp.out, damaged: refs.bad,
	}
	r.res.BlobCount, r.res.BlobBytes = n, bytes

	// Step 3 is done: every pin is committed. Step 4, in a separate transaction.
	if err := r.fault(ctx, PointPinsCommitted); err != nil {
		return err
	}
	to := backup.HoldPinned
	if pinned == 0 {
		to = backup.HoldNone
	}
	if err := r.h.SetHold(ctx, backup.HoldAll, to); err != nil {
		return err
	}
	return r.fault(ctx, PointHoldPinned)
}

// verifyPreviousList reads the rest of the previous backup's blob list and compares its SHA-256 and entry count with the
// manifest, so that the diff trusts the list only if it is the one the manifest names (Q-BAK-30 asks the same of the sweep).
// It runs before the hold turns 'pinned'.
func (r *runState) verifyPreviousList(h *teeHash, c *countIter) error {
	for {
		_, err := c.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("%w: the blob list of %s: %w", backup.ErrPoolDamaged, r.prev.id, err)
		}
	}
	if _, err := io.Copy(io.Discard, h); err != nil {
		return fmt.Errorf("%w: the blob list of %s: %w", backup.ErrPoolDamaged, r.prev.id, err)
	}
	want := r.prev.manifest.Blobs
	if got := hex.EncodeToString(h.h.Sum(nil)); got != want.SHA256 || c.n != want.Referenced {
		return fmt.Errorf("%w: the blob list of %s has %d entries and the SHA-256 %s, its manifest says %d and %s",
			backup.ErrPoolDamaged, r.prev.id, c.n, got, want.Referenced, want.SHA256)
	}
	return nil
}
