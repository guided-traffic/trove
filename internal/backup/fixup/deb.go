package fixup

import (
	"context"
	"errors"

	"github.com/guided-traffic/trove/internal/formatdata/deb"
	"github.com/guided-traffic/trove/internal/meta"
)

// The Debian fix-up and purger of a restore.
//
// The fix-up (R-DEB-032, R-BAK-022): a restore enqueues a full build of every local and virtual Debian repository. The job rows of the
// snapshot may be gone or stale, the journal replay raised change_seq of what it withheld, and the first build after the restore dates
// its Release later than every earlier one (R-DEB-010), so clients take the rebuilt indexes. The builds run when the workers start.
//
// The purger (R-BAK-008, R-DEB-023, H-630): after a restore from a backup that left the remote caches out, it empties every Debian remote
// repository's cache through the storage delete path and drops the repository's cache entries and the rows the remote derived from its
// releases: deb_remote_releases, deb_remote_files, deb_remote_indexes and deb_remote_entries. Local and virtual repositories are not
// touched.
func init() {
	Register(Fixup{
		Name: "deb-index", Order: OrderFormats,
		Apply: func(ctx context.Context, tx meta.Tx, _ Env) error { return deb.EnqueueFullBuilds(ctx, tx) },
	})
	RegisterPurger(CachePurger{
		Name: "deb",
		Purge: func(ctx context.Context, db meta.Runner, env Env) error {
			if env.Items == nil {
				return errors.New("fixup: the Debian purger needs the storage delete path (Env.Items)")
			}
			return deb.PurgeRemoteCaches(ctx, db, env.Items.EraseCache)
		},
	})
}
