package packages

import (
	"context"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
)

// RestoredAt returns the time of the last restore (system_state key restored_at,
// R-BAK-022), the zero time while none happened. A format compares the validation
// time of a stored document with it, so a document validated before a restore is
// revalidated (Q-NPM-13).
func RestoredAt(ctx context.Context, run meta.Runner) (time.Time, error) {
	var raw []string
	err := run.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		raw, err = qs.ReadRestoredAt(ctx)
		return err
	})
	if err != nil || len(raw) == 0 {
		return time.Time{}, err
	}
	t, err := time.Parse(time.RFC3339Nano, raw[0])
	if err != nil {
		return time.Time{}, errs.E(errs.Internal, errs.CodeInternal, "system_state restored_at is not a time", errs.WithCause(err))
	}
	return t.UTC(), nil
}
