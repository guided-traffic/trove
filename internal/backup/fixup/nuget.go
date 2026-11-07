package fixup

import (
	"context"
	"errors"

	"github.com/guided-traffic/trove/internal/formatdata/nuget"
	"github.com/guided-traffic/trove/internal/meta"
)

// The NuGet purger (R-BAK-008, R-NUGET-033): after a restore from a backup that left the remote caches out, it empties
// every NuGet remote repository's cache through the storage delete path, which removes the cached version rows through
// the format's storage hook, and drops the repository's cache entries. It writes no tombstone, because tombstones
// belong to local repositories, and leaves local and virtual repositories alone.
func init() {
	RegisterPurger(CachePurger{
		Name: "nuget",
		Purge: func(ctx context.Context, db meta.Runner, env Env) error {
			if env.Items == nil {
				return errors.New("fixup: the NuGet purger needs the storage delete path (Env.Items)")
			}
			return nuget.PurgeRemoteCaches(ctx, db, env.Items.EraseCache)
		},
	})
}
