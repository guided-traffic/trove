package items

import (
	"context"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/qname"
)

// EraseCache removes every item of one remote repository through the same removal as the delete of a subtree (R-STOR-018),
// permanently, with the trash can bypassed, and without a permission check: the restore of a backup that left the remote
// caches out empties the cache of each remote repository this way (R-BAK-008, R-BAK-021 step 7). The files go in
// batches of storage.delete.batch_size, each in a transaction of its own in which the storage hook of the repository's
// format learns of them (so the format's rows and packages.rev_seq follow), then the folders. It is idempotent and
// resumable: a call after an interrupted one removes what is left. The actor of the events is the nil actor.
func (s *Store) EraseCache(ctx context.Context, repoID int64, name qname.Repo, format string) error {
	a := DeleteArgs{RepoID: repoID, Key: name, Class: "remote", Format: format, Actor: audit.Actor{Kind: "system", Name: "restore"}, all: true}
	return s.runDelete(ctx, a, &deleteRun{name: name})
}
