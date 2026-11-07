package ship

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// DB is what the outbox needs of the database; *postgres.DB implements it.
type DB interface {
	meta.Runner
	Read(ctx context.Context, fn func(q.DBTX) error) error
}

// Outbox is [journal.Outbox] over revocation_outbox (R-HA-054). The shipper job holds a singleton lease, so a plain read in
// id order is enough (queries/backup.sql, ClaimRevocationOutbox).
type Outbox struct{ db DB }

var _ journal.Outbox = (*Outbox)(nil)

// NewOutbox builds the outbox over a database.
func NewOutbox(db DB) *Outbox { return &Outbox{db: db} }

// Claim implements [journal.Outbox].
func (o *Outbox) Claim(ctx context.Context, limit int) ([]journal.OutboxRow, error) {
	var rows []q.ClaimRevocationOutboxRow
	err := o.db.Read(ctx, func(d q.DBTX) (err error) {
		rows, err = q.New(d).ClaimRevocationOutbox(ctx, int32(min(limit, 1000))) //nolint:gosec // bounded above
		return err
	})
	if err != nil {
		return nil, err
	}
	out := make([]journal.OutboxRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, journal.OutboxRow{ID: r.ID, At: r.At.Time.UTC(), Event: r.Event, Entry: json.RawMessage(r.Entry)})
	}
	return out, nil
}

// Delete implements [journal.Outbox].
func (o *Outbox) Delete(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	err := o.db.InTx(ctx, func(tx meta.Tx) error {
		ptx, ok := postgres.Unwrap(tx)
		if !ok {
			return errors.New("the transaction is not a PostgreSQL transaction")
		}
		_, err := q.New(ptx).DeleteRevocationOutboxRows(ctx, ids)
		return err
	})
	if err != nil {
		return fmt.Errorf("deleting revocation_outbox rows: %w", err)
	}
	return nil
}

// Pending implements [journal.Outbox]: the number of rows and the age of the oldest (trove_revocation_journal_pending).
func (o *Outbox) Pending(ctx context.Context) (int64, time.Duration, error) {
	var (
		n      int64
		oldest []time.Time
	)
	err := o.db.Read(ctx, func(d q.DBTX) error {
		qs := q.New(d)
		var err error
		if n, err = qs.CountRevocationOutbox(ctx); err != nil {
			return err
		}
		ts, err := qs.OldestRevocationOutbox(ctx)
		for _, t := range ts {
			oldest = append(oldest, t.Time)
		}
		return err
	})
	if err != nil || n == 0 || len(oldest) == 0 {
		return n, 0, err
	}
	return n, max(time.Since(oldest[0]), 0), nil
}
