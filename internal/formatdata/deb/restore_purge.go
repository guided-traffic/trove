package deb

import (
	"context"
	"fmt"

	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/qname"
)

// EraseFunc empties one remote repository's cached items through the storage delete path (R-STOR-018, the trash can bypassed). It is
// idempotent.
type EraseFunc func(ctx context.Context, repoID int64, name qname.Repo, format string) error

// PurgeRemoteCaches empties the cache of every Debian remote repository after a restore from a backup that left the remote caches out
// (R-BAK-008, R-DEB-023, H-630). Per repository it deletes the cached items through erase, then the repository's cache entries (the
// stored release documents and indexes among them) and the rows the remote derived from them: the stored release documents, the
// files they list, the parsed indexes and their entries. The release is fetched again on first use and the indexes are parsed again.
//
// It leaves local and virtual repositories alone: the repositories it reads are the remote ones, and it writes no tombstone, because
// tombstones belong to local repositories. A resumed restore runs it again; every step finds what is left.
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
		rs, err := qs.BackupRemoteRepositories(ctx, string(formatID))
		if err != nil {
			return err
		}
		for _, r := range rs {
			repos = append(repos, remote{id: r.ID, name: qname.Repo{Project: r.ProjectKey, Key: r.Key}})
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("deb remote caches: list the repositories: %w", err)
	}
	for _, r := range repos {
		if err := erase(ctx, r.id, r.name, string(formatID)); err != nil {
			return fmt.Errorf("deb remote caches: empty %s/%s: %w", r.name.Project, r.name.Key, err)
		}
		err := db.InTx(ctx, func(tx meta.Tx) error {
			qs, err := queries(tx)
			if err != nil {
				return err
			}
			if _, err := qs.BackupDeleteCacheEntries(ctx, r.id); err != nil {
				return err
			}
			return purgeRows(ctx, qs, r.id)
		})
		if err != nil {
			return fmt.Errorf("deb remote caches: drop the rows of %s/%s: %w", r.name.Project, r.name.Key, err)
		}
	}
	return nil
}

// formatID is the ID of the Debian format; the data port may not import the format.
const formatID = "deb"

// purgeRows deletes the rows of a remote's four cache tables. The entries go first, the tables that reference nothing after.
func purgeRows(ctx context.Context, qs *q.Queries, repoID int64) error {
	for _, del := range []func(context.Context, int64) error{
		qs.DebRemotePurgeEntries, qs.DebRemotePurgeIndexes, qs.DebRemotePurgeFiles, qs.DebRemotePurgeReleases,
	} {
		if err := del(ctx, repoID); err != nil {
			return err
		}
	}
	return nil
}
