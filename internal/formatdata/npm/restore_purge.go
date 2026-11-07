package npm

import (
	"context"
	"fmt"

	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/qname"
)

// EraseFunc empties one remote repository's cached items through the storage delete path (R-STOR-018, the trash can
// bypassed), so the storage hook of the format updates its rows and packages.rev_seq grows. It is idempotent.
type EraseFunc func(ctx context.Context, repoID int64, name qname.Repo, format string) error

// PurgeRemoteCaches empties the cache of every npm remote repository after a restore from a backup that left the
// remote caches out (R-BAK-008, R-NPM-047). Per repository it deletes the cached items through erase, then the
// repository's cache entries (the stored documents among them) and the npm rows that describe those documents,
// `npm_remote_packages` with its `npm_remote_versions` rows (R-NPM-024), so the first request for a package fetches its
// document as on an empty remote and audit forwarding withholds the name until it has (R-NPM-038).
//
// It leaves local and virtual repositories alone: it reads the remote repositories of the format and nothing else. It
// writes no tombstone, because tombstones belong to local repositories. A resumed restore runs it again; every step
// finds what is left.
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
		rs, err := qs.BackupRemoteRepositories(ctx, "npm")
		if err != nil {
			return err
		}
		for _, r := range rs {
			repos = append(repos, remote{id: r.ID, name: qname.Repo{Project: r.ProjectKey, Key: r.Key}})
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("npm remote caches: list the repositories: %w", err)
	}
	for _, r := range repos {
		if err := erase(ctx, r.id, r.name, "npm"); err != nil {
			return fmt.Errorf("npm remote caches: empty %s/%s: %w", r.name.Project, r.name.Key, err)
		}
		err := db.InTx(ctx, func(tx meta.Tx) error {
			qs, err := queries(tx)
			if err != nil {
				return err
			}
			if _, err := qs.BackupDeleteCacheEntries(ctx, r.id); err != nil {
				return err
			}
			_, err = qs.BackupNPMDeleteRemotePackages(ctx, r.id)
			return err
		})
		if err != nil {
			return fmt.Errorf("npm remote caches: drop the rows of %s/%s: %w", r.name.Project, r.name.Key, err)
		}
	}
	return nil
}
