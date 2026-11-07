package create

import (
	"context"
	crand "crypto/rand"
	"encoding/binary"
	"fmt"
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
	refs, err := r.snap.Referenced(ctx)
	if err != nil {
		return err
	}
	var known format.BlobIterator = &format.EntryList{}
	if r.prev != nil {
		rc, _, err := r.d.Target.Get(ctx, r.layout.BlobList(r.prev.id))
		if err != nil {
			return classify(fmt.Errorf("%w: the blob list of %s: %w", backup.ErrPoolDamaged, r.prev.id, err))
		}
		defer rc.Close()
		rd, err := format.NewBlobListReader(rc)
		if err != nil {
			return fmt.Errorf("%w: the blob list of %s: %w", backup.ErrPoolDamaged, r.prev.id, err)
		}
		defer rd.Close()
		skip := map[string]bool{}
		for sha := range r.prev.manifest.DamagedSet() {
			skip[sha] = true
		}
		known = format.Without(rd, skip)
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
			smp.listed(e.Digest)
			r.h.Tick()
			return lw.Add(e)
		},
		func(e format.BlobEntry) error {
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
		toCopy: pinned, toCopyByte: pinnedBytes, sample: smp.out,
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
