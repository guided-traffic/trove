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
	// OnQuarantine is called for every row set aside because it cannot be encoded (the metric
	// trove_revocation_journal_quarantined_total counts them). Optional.
	OnQuarantine func(r journal.OutboxRow, err error)
	// Batch is the largest number of rows one claim reads; zero means [DefaultBatch].
	Batch int
	Log   *slog.Logger
}

// Quarantiner is an outbox that can set a row aside: the row leaves the queue and stays recorded where an operator and the
// post-restore find it. [Outbox] implements it.
type Quarantiner interface {
	Quarantine(ctx context.Context, r journal.OutboxRow, reason string) error
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
				body, key, err := s.encode(r)
				if err != nil {
					// One row that cannot become an object must not hold back the revocations behind it. It is set aside, not
					// dropped: it is counted, recorded in system_state and listed by post-restore, so the revocation it
					// recorded is known to be missing from the journal (audit finding L4). An outbox that cannot do that keeps
					// the row and fails, as before.
					q, ok := s.o.Outbox.(Quarantiner)
					if !ok {
						putErr = err
						break
					}
					if qerr := q.Quarantine(ctx, r, err.Error()); qerr != nil {
						putErr = fmt.Errorf("journal: quarantining row %s: %w", r.ID, qerr)
						break
					}
					s.o.Log.Error("journal: an outbox row cannot be encoded and was set aside", "id", r.ID, "event", r.Event, "error", err)
					if s.o.OnQuarantine != nil {
						s.o.OnQuarantine(r, err)
					}
					continue
				}
				if err := s.o.Store.Put(ctx, key, body); err != nil {
					putErr = fmt.Errorf("journal: writing %s: %w", key, err)
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

func (s *Shipper) encode(r journal.OutboxRow) (body []byte, key string, err error) {
	obj := journal.NewObject(r)
	body, err = obj.Encode()
	if err != nil {
		return nil, "", fmt.Errorf("journal: row %s cannot be encoded: %w", r.ID, err)
	}
	return body, journal.Key(s.o.BlobPrefix, obj), nil
}
