package ansible

import (
	"context"
	"fmt"

	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/qname"
)

// EraseFunc empties one remote repository's cached items through the storage delete path (R-STOR-018, the trash can bypassed). It is
// idempotent.
type EraseFunc func(ctx context.Context, repoID int64, name qname.Repo, format string) error

// PurgeRemoteCaches empties the cache of every Ansible remote repository after a restore from a backup that left the remote caches
// out (R-BAK-008): per repository the cached items through erase, then the repository's cache entries (the stored documents among
// them) and the digests recorded in ansible_upstream_versions, so that the first request for a version fetches its documents and
// records its digest as on an empty remote. Local and virtual repositories are not touched. A resumed restore runs it again; every
// step finds what is left.
func PurgeRemoteCaches(ctx context.Context, db meta.Runner, erase EraseFunc) error {
	type remote struct {
		id   int64
		name qname.Repo
	}
	var repos []remote
	err := db.InTx(ctx, func(tx meta.Tx) error {
		repos = repos[:0]
		qs, err := txQueries(tx)
		if err != nil {
			return err
		}
		rs, err := qs.BackupRemoteRepositories(ctx, "ansible")
		if err != nil {
			return err
		}
		for _, r := range rs {
			repos = append(repos, remote{id: r.ID, name: qname.Repo{Project: r.ProjectKey, Key: r.Key}})
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("ansible remote caches: list the repositories: %w", err)
	}
	for _, r := range repos {
		if err := erase(ctx, r.id, r.name, "ansible"); err != nil {
			return fmt.Errorf("ansible remote caches: empty %s/%s: %w", r.name.Project, r.name.Key, err)
		}
		err := db.InTx(ctx, func(tx meta.Tx) error {
			qs, err := txQueries(tx)
			if err != nil {
				return err
			}
			if _, err := qs.BackupDeleteCacheEntries(ctx, r.id); err != nil {
				return err
			}
			if _, err = qs.BackupAnsibleDeleteUpstreamVersions(ctx, r.id); err != nil {
				return err
			}
			// The documentation rows are derived from the stored documents that went (R-ANS-044).
			if _, err = qs.AnsibleDocsPurgeVersions(ctx, r.id); err != nil {
				return err
			}
			_, err = qs.AnsibleDocsPurgeFetches(ctx, r.id)
			return err
		})
		if err != nil {
			return fmt.Errorf("ansible remote caches: forget the documents of %s/%s: %w", r.name.Project, r.name.Key, err)
		}
	}
	return nil
}
