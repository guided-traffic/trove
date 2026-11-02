package items

import (
	"context"

	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The folders a removal empties (R-STOR-003, R-STOR-018, Q-NUGET-88).
//
// A deploy holds every ancestor folder of its path FOR SHARE from its link step to its commit
// and takes the locks of its storage hook (the package row of a format) after them. A removal
// takes the package rows first: a format's route locks the row before it removes the files of
// a version through Store.RemoveIn, and the generic layer locks the rows of the removed files'
// packages in DeleteSpec.Extra. If the removing transaction then waited for the FOR UPDATE lock
// of a folder it emptied, a push of one version and the removal of another version of the same
// package would wait for each other in a circle: the push holds the folder and waits for the
// package row, the removal holds the package row and waits for the folder (40P01).
//
// So a removing transaction never waits for a folder. It takes a folder's lock only when the
// lock is free (FOR UPDATE SKIP LOCKED) and leaves a folder another transaction holds to a
// transaction of its own that runs after the commit, holds no other lock and may therefore
// wait. The holder is a deploy that puts a child below the folder, or another removal; the
// transaction after the commit finds the folder as the holder left it and removes it when it is
// still empty, then walks up as the removal would have. The removal of the job items-delete
// takes its folders in transactions that hold nothing else and keeps waiting for them.

// heldKey is the slot of a removing transaction that lists the folders it found held.
type heldKey struct{}

// heldFolder is a folder a removing transaction found held by another transaction.
type heldFolder struct {
	repo  int64
	path  string
	force bool
	// removing are the folders of the subtree the removal deletes (nil for the implicit
	// folders above a removed item): the sweep removes an ancestor of path that is one of them
	// as the removal would have, explicit or not, once nothing is left below it.
	removing map[string]bool
}

// removeEmptied is removeFolderIfEmpty in a removing transaction: it never waits for the
// folder's lock. A folder another transaction holds stays for now and is removed after the
// commit if it is still empty then; it is reported as not removed, so a caller that walks up
// stops below it.
func (s *Store) removeEmptied(ctx context.Context, tx meta.Tx, qs *q.Queries, rid int64, path string, force bool, removing map[string]bool) (bool, error) {
	rows, err := qs.LockFolderExclusiveFree(ctx, q.LockFolderExclusiveFreeParams{RepoID: rid, Path: path})
	if err != nil {
		return false, err
	}
	if len(rows) == 0 {
		live, err := qs.FolderLive(ctx, q.FolderLiveParams{RepoID: rid, Path: path})
		if err != nil || !live {
			return false, err
		}
		s.afterCommit(ctx, tx, heldFolder{repo: rid, path: path, force: force, removing: removing})
		return false, nil
	}
	return removeLockedFolder(ctx, qs, rid, path, rows[0].ID, rows[0].ExplicitFolder, force)
}

// sweepAncestors is cleanAncestors in a removing transaction: the implicit folders above a
// removed item that have no other child go, bottom up, without waiting for a lock.
func (s *Store) sweepAncestors(ctx context.Context, tx meta.Tx, qs *q.Queries, rid int64, parent string) error {
	for parent != "" {
		gone, err := s.removeEmptied(ctx, tx, qs, rid, parent, false, nil)
		if err != nil || !gone {
			return err
		}
		parent = ParentOf(parent)
	}
	return nil
}

// removedOrGone removes a folder that is empty and reports left when it is still there (a child
// keeps it, or it is explicit and force is not set); a folder that is gone is not left.
func removedOrGone(ctx context.Context, qs *q.Queries, rid int64, path string, force bool) (left bool, err error) {
	gone, err := removeFolderIfEmpty(ctx, qs, rid, path, force)
	if err != nil || gone {
		return false, err
	}
	return qs.FolderLive(ctx, q.FolderLiveParams{RepoID: rid, Path: path})
}

// afterCommit notes a held folder in the transaction's slot and, for the first one, registers
// the sweep that runs once the transaction committed. A retried attempt starts with an empty
// slot and registers again; a rolled-back one sweeps nothing.
func (s *Store) afterCommit(ctx context.Context, tx meta.Tx, f heldFolder) {
	held, _ := tx.Value(heldKey{}).(*[]heldFolder)
	if held == nil {
		held = new([]heldFolder)
		tx.SetValue(heldKey{}, held)
		// The sweep belongs to the committed removal, not to the request that asked for it.
		bg := context.WithoutCancel(ctx)
		tx.After(func() { s.sweepHeld(bg, *held) })
	}
	*held = append(*held, f)
}

// sweepHeld removes the folders a committed removal found held, each in a transaction of its
// own that waits for the holder, then walks up as the removal would have: the folders of the
// deleted subtree go when nothing is left below them, explicit ones included, and above it the
// implicit folders without another child. A held folder the holder removed itself (a
// concurrent delete) is gone already; the walk goes on from it, because the removal found its
// ancestors kept only by it. A failure (a lock_timeout behind a long deploy) is logged: the
// folder stays until a later removal below it or a delete of it takes it.
func (s *Store) sweepHeld(ctx context.Context, folders []heldFolder) {
	for _, f := range folders {
		err := s.run.InTx(ctx, func(tx meta.Tx) error {
			qs, err := queries(tx)
			if err != nil {
				return err
			}
			if left, err := removedOrGone(ctx, qs, f.repo, f.path, f.force); err != nil || left {
				return err
			}
			parent := ParentOf(f.path)
			for ; parent != "" && f.removing[parent]; parent = ParentOf(parent) {
				if left, err := removedOrGone(ctx, qs, f.repo, parent, true); err != nil || left {
					return err
				}
			}
			return cleanAncestors(ctx, qs, f.repo, parent)
		})
		if err != nil {
			s.log.Warn("a folder that a removal emptied was not removed after its commit; it stays until a later removal takes it",
				"repo", f.repo, "path", f.path, "cause", err)
		}
	}
}
