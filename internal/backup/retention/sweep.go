package retention

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/backup/listing"
	"github.com/guided-traffic/trove/internal/backup/target"
)

// deleteBatch is the most keys one delete request carries (DeleteObjects).
const deleteBatch = 1000

// ErrSweepStopped is the verdict of a sweep that met a key it must not trust: a key outside the pool layout or out of
// order. Nothing more is deleted (R-BAK-017 step 4).
var ErrSweepStopped = errors.New("pool sweep stopped")

// SweepReport is what a sweep did.
type SweepReport struct {
	Deleted int64
	Bytes   int64
	// Stopped is set with the key that stopped the sweep and why.
	Stopped string
}

// Sweeper deletes the pool objects that no remaining complete backup lists (R-BAK-017).
type Sweeper struct {
	Store  target.Store
	Layout format.Layout
	// Fence runs before every delete request (R-BAK-010); an error stops the sweep.
	Fence func(context.Context) error
	// Tick reports progress to the run's heartbeat.
	Tick func()
	// DeleteTimeout is the deadline of every delete request; zero means none.
	DeleteTimeout time.Duration
}

// deleteWithin sends one delete request under a deadline.
func deleteWithin(ctx context.Context, st target.Store, d time.Duration, keys []string) error {
	if d > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}
	return st.Delete(ctx, keys)
}

// Sweep merges the blob lists of the given complete backups (minus their blobs.damaged) into the keep set and lists the pool
// once, in key order, deleting every object the keep set lacks. The caller passes the complete backups of a fresh scan taken
// after its deletions, so a deleted backup protects nothing.
//
// Before the first delete request every list is downloaded and compared with its manifest's SHA-256: a list that does not
// match fails the sweep with [listing.ErrBlobList] and nothing is deleted. A pool key that does not match the pool layout
// exactly, or does not follow its predecessor, ends the sweep without a further delete (the pending batch included), and the
// report says which key; the error is nil. Directory buckets, which do not list in order, are not targets.
func (s *Sweeper) Sweep(ctx context.Context, complete []listing.Backup) (SweepReport, error) {
	var rep SweepReport
	for _, b := range complete {
		if err := listing.CheckBlobList(ctx, s.Store, s.Layout, b.Manifest); err != nil {
			return rep, fmt.Errorf("the pool was not swept: %w", err)
		}
		s.tick()
	}
	its := make([]format.BlobIterator, 0, len(complete))
	for _, b := range complete {
		bl, err := listing.OpenBlobList(ctx, s.Store, s.Layout, b.Manifest)
		if err != nil {
			return rep, fmt.Errorf("the pool was not swept: blob list of backup %s: %w", b.ID, err)
		}
		defer bl.Close()
		its = append(its, bl.Without(b.Manifest))
	}
	keep := format.Union(its...)

	var head format.BlobEntry
	headOK := false
	advance := func() error {
		e, err := keep.Next()
		switch {
		case errors.Is(err, io.EOF):
			headOK = false
			return nil
		case err != nil:
			return err
		}
		head, headOK = e, true
		return nil
	}
	if err := advance(); err != nil {
		return rep, fmt.Errorf("the pool was not swept: %w", err)
	}

	var batch []string
	var batchBytes int64
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if s.Fence != nil {
			if err := s.Fence(ctx); err != nil {
				return err
			}
		}
		if err := deleteWithin(ctx, s.Store, s.DeleteTimeout, batch); err != nil {
			return fmt.Errorf("deleting pool objects: %w", err)
		}
		rep.Deleted += int64(len(batch))
		rep.Bytes += batchBytes
		batch, batchBytes = batch[:0], 0
		s.tick()
		return nil
	}

	var prev format.Digest
	havePrev := false
	listed := 0
	err := s.Store.List(ctx, s.Layout.PoolPrefix(), func(o target.Object) error {
		if listed++; listed%listing.PageSize == 0 {
			s.tick() // a listing page is progress even when it holds nothing to delete (R-BAK-013)
		}
		d, ok := s.Layout.ParsePool(o.Key)
		switch {
		case !ok:
			rep.Stopped = fmt.Sprintf("%s is not a pool key", o.Key)
			return ErrSweepStopped
		case havePrev && !prev.Less(d):
			rep.Stopped = fmt.Sprintf("%s does not follow %s", o.Key, s.Layout.Pool(prev))
			return ErrSweepStopped
		}
		prev, havePrev = d, true
		for headOK && head.Digest.Less(d) {
			if err := advance(); err != nil {
				return err
			}
		}
		if headOK && head.Digest == d {
			return nil
		}
		batch = append(batch, o.Key)
		batchBytes += o.Size
		if len(batch) >= deleteBatch {
			return flush()
		}
		return nil
	})
	switch {
	case errors.Is(err, ErrSweepStopped):
		return rep, nil
	case err != nil:
		return rep, fmt.Errorf("sweeping the pool: %w", err)
	}
	if err := flush(); err != nil {
		return rep, fmt.Errorf("sweeping the pool: %w", err)
	}
	return rep, nil
}

func (s *Sweeper) tick() {
	if s.Tick != nil {
		s.Tick()
	}
}
