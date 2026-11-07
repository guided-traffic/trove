// Package ship writes the rows of revocation_outbox to the journal in object storage (R-HA-054 "Outbox and shipping").
package ship

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/guided-traffic/trove/internal/journal"
)

// DefaultBatch is the number of rows one claim reads.
const DefaultBatch = 100

// Options configure [New].
type Options struct {
	Outbox journal.Outbox
	// Store receives the objects. Nil with Discard set is the journal switched off.
	Store journal.Store
	// BlobPrefix is storage.s3.prefix: the objects go below <prefix>/_journal/revocations/.
	BlobPrefix string
	// Discard deletes the rows without writing an object: backup.revocation_journal.enabled is false, and a row must not
	// pile up for a journal nobody reads.
	Discard bool
	// Batch is the largest number of rows one claim reads; zero means [DefaultBatch].
	Batch int
	Log   *slog.Logger
}

// Shipper is the [journal.Shipper] over an outbox and a store.
type Shipper struct{ o Options }

var _ journal.Shipper = (*Shipper)(nil)

// New builds a shipper. It panics without an outbox or, when not discarding, without a store: both are wiring errors.
func New(o Options) *Shipper {
	if o.Outbox == nil || (o.Store == nil && !o.Discard) {
		panic("ship: New needs an outbox and a store")
	}
	if o.Batch <= 0 {
		o.Batch = DefaultBatch
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	return &Shipper{o: o}
}

// Ship writes the outbox in id order, one object per row, and deletes each row after its object was written. A row whose
// object was written and whose deletion did not commit is written again under the same key with the same bytes. Ship stops
// at the first failure: the rows written before it are already deleted, the rest stay for the next run. It returns the
// number of rows shipped and never deletes an object.
func (s *Shipper) Ship(ctx context.Context) (int, error) {
	total := 0
	for {
		rows, err := s.o.Outbox.Claim(ctx, s.o.Batch)
		if err != nil {
			return total, fmt.Errorf("journal: reading the outbox: %w", err)
		}
		if len(rows) == 0 {
			return total, nil
		}
		done := make([]string, 0, len(rows))
		var putErr error
		for _, r := range rows {
			if !s.o.Discard {
				if putErr = s.put(ctx, r); putErr != nil {
					break
				}
			}
			done = append(done, r.ID)
		}
		if len(done) > 0 {
			if err := s.o.Outbox.Delete(ctx, done); err != nil {
				return total, fmt.Errorf("journal: deleting shipped rows: %w", err)
			}
			total += len(done)
		}
		if putErr != nil {
			return total, putErr
		}
		if len(rows) < s.o.Batch {
			return total, nil
		}
	}
}

func (s *Shipper) put(ctx context.Context, r journal.OutboxRow) error {
	obj := journal.NewObject(r)
	body, err := obj.Encode()
	if err != nil {
		return fmt.Errorf("journal: row %s cannot be encoded: %w", r.ID, err)
	}
	key := journal.Key(s.o.BlobPrefix, obj)
	if err := s.o.Store.Put(ctx, key, body); err != nil {
		return fmt.Errorf("journal: writing %s: %w", key, err)
	}
	return nil
}
