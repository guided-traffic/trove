package create

import (
	"context"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/meta"
)

// TxEvents writes each audit event in a transaction of its own on the source database (R-BAK-014: the events are
// transactional types, written by the run). The backup role holds INSERT on audit_event for this (R-BAK-015).
type TxEvents struct {
	DB       meta.Runner
	Recorder audit.Recorder
}

// Record implements Events.
func (t TxEvents) Record(ctx context.Context, ev audit.Event) error {
	return t.DB.InTx(ctx, func(tx meta.Tx) error { return t.Recorder.RecordTx(ctx, tx, ev) })
}
