package fixup

import (
	"context"

	"github.com/guided-traffic/trove/internal/formatdata/deb"
	"github.com/guided-traffic/trove/internal/meta"
)

// The Debian fix-up (R-DEB-032, R-BAK-022): a restore enqueues a full build of every local and virtual Debian repository. The job rows
// of the snapshot may be gone or stale, the journal replay raised change_seq of what it withheld, and the first build after the
// restore dates its Release later than every earlier one (R-DEB-010), so clients take the rebuilt indexes. The builds run when the
// workers start.
func init() {
	Register(Fixup{
		Name: "deb-index", Order: OrderFormats,
		Apply: func(ctx context.Context, tx meta.Tx, _ Env) error { return deb.EnqueueFullBuilds(ctx, tx) },
	})
}
