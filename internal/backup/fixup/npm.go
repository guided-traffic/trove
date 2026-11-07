package fixup

import (
	"context"
	"errors"

	"github.com/guided-traffic/trove/internal/formatdata/npm"
	"github.com/guided-traffic/trove/internal/meta"
)

// The npm purger (R-BAK-008, R-NPM-047): after a restore from a backup that left the remote caches out, it empties
// every npm remote repository's cache through the storage delete path and drops the repository's cache entries and
// npm_remote_packages rows (the npm_remote_versions rows cascade). Local and virtual repositories are not touched.
func init() {
	RegisterPurger(CachePurger{
		Name: "npm",
		Purge: func(ctx context.Context, db meta.Runner, env Env) error {
			if env.Items == nil {
				return errors.New("fixup: the npm purger needs the storage delete path (Env.Items)")
			}
			return npm.PurgeRemoteCaches(ctx, db, env.Items.EraseCache)
		},
	})
}
