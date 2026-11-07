package nuget

import (
	"context"
	"fmt"

	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/qname"
)

// EraseFunc empties one remote repository's cached items through the storage delete path (R-STOR-018, the trash can
// bypassed), so the storage hook of the format removes the cached version rows and packages.rev_seq grows. It is
// idempotent.
type EraseFunc func(ctx context.Context, repoID int64, name qname.Repo, format string) error

// PurgeRemoteCaches empties the cache of every NuGet remote repository after a restore from a backup that left the
// remote caches out (R-BAK-008, R-NUGET-033). Per repository it deletes the cached items through erase, which removes the
// `origin = 'cache'` version rows through the storage hook, then the repository's cache entries, the stored upstream
// documents among them. The upstream service index is fetched again on first use.
//
// It leaves local and virtual repositories alone and writes no tombstone, because tombstones belong to local
// repositories (R-NUGET-006). NuGet keeps no other index rows of a remote repository's documents. A resumed restore runs
// it again; every step finds what is left.
func PurgeRemoteCaches(ctx context.Context, db meta.Runner, erase EraseFunc) error {
	type remote struct {
		id   int64
		name qname.Repo
	}
	var repos []remote
	err := db.InTx(ctx, func(tx meta.Tx) error {
		repos = repos[:0]
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		rs, err := qs.BackupRemoteRepositories(ctx, "nuget")
		if err != nil {
			return err
		}
		for _, r := range rs {
			repos = append(repos, remote{id: r.ID, name: qname.Repo{Project: r.ProjectKey, Key: r.Key}})
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("nuget remote caches: list the repositories: %w", err)
	}
	for _, r := range repos {
		if err := erase(ctx, r.id, r.name, "nuget"); err != nil {
			return fmt.Errorf("nuget remote caches: empty %s/%s: %w", r.name.Project, r.name.Key, err)
		}
		err := db.InTx(ctx, func(tx meta.Tx) error {
			qs, err := queries(tx)
			if err != nil {
				return err
			}
			_, err = qs.BackupDeleteCacheEntries(ctx, r.id)
			return err
		})
		if err != nil {
			return fmt.Errorf("nuget remote caches: drop the cache entries of %s/%s: %w", r.name.Project, r.name.Key, err)
		}
	}
	return nil
}
