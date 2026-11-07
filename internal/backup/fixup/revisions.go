package fixup

import (
	"context"

	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
)

// RevisionStep is what a restore adds to every revision column: values that clients hold as ETag or send as If-Match would
// otherwise repeat with different content (R-BAK-022).
const RevisionStep int64 = 1 << 32

func init() {
	Register(Fixup{Name: "revisions", Order: OrderRevisions, Apply: raiseRevisions})
}

func raiseRevisions(ctx context.Context, tx meta.Tx, _ Env) error {
	_, err := postgres.RaiseRevisionColumns(ctx, tx, meta.RevisionColumns(), RevisionStep)
	return err
}
