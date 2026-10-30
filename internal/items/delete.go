package items

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/repository"
)

// DeleteJobKind is the job kind that removes a large subtree in batches
// (R-STOR-018). It is exclusive per repository and path: a second delete of the
// same subtree while the job runs answers with the same job.
const DeleteJobKind = "items-delete"

// DeleteJobSpec is the registration of the kind. The composition root registers
// it with the handler of its store (Store.RunDeleteJob), because the handler
// needs the store and this package holds no package-level state (R-ARCH-032).
func DeleteJobSpec(h jobs.Handler) jobs.KindSpec {
	return jobs.KindSpec{Handler: h, DedupMode: jobs.Exclusive}
}

// DeleteArgs are the arguments of a deletion job. The actor is kept so that the
// events name who asked.
type DeleteArgs struct {
	RepoID int64  `json:"repoId"`
	Key    string `json:"key"`
	// Class and Format are what a storage hook dispatches on; empty in a job
	// queued before they were recorded.
	Class  string      `json:"class,omitempty"`
	Format string      `json:"format,omitempty"`
	Path   string      `json:"path"`
	Actor  audit.Actor `json:"actor"`
	// MaxID is the highest item id the permission check saw. The job removes only
	// items up to it: an item that a deploy adds to the subtree afterwards was
	// never decided and stays (R-STOR-018). Zero (a job enqueued before the field
	// existed) bounds nothing.
	MaxID int64 `json:"maxId,omitempty"`
}

// DeleteSpec is the removal of a file, or of a folder with its subtree.
type DeleteSpec struct {
	Repo repository.Repository
	// Path is the canonical path of the item, not the root.
	Path     string
	Actor    audit.Actor
	Source   audit.Source
	Endpoint string
	// Check is asked with the paths of the subtree, in batches of at most 1,000,
	// before anything is removed; an error stops the delete with nothing changed.
	// For a subtree removed at once it runs in the removing transaction, on the
	// rows that transaction removes; for a job, before the job is queued. The
	// store does not know why a path may not be deleted.
	Check func(ctx context.Context, paths []string) error
	// Extra runs in the transaction of the removal, or of queuing the job, after
	// Check; the generic layer records the allowed write with it.
	Extra func(ctx context.Context, tx meta.Tx) error
}

// DeleteResult is the outcome of a delete: Done when the subtree was removed,
// else the job that removes it.
type DeleteResult struct {
	JobID    string
	JobState string
	Done     bool
}

// Delete implements the removal of R-STOR-018: every item of the subtree is
// checked (DeleteSpec.Check) before anything is removed. A subtree of at most
// storage.delete.max_sync_items items is removed at once, with its events and
// the empty implicit folders above it; a larger one by the job [DeleteJobKind]
// in batches of storage.delete.batch_size. Items are deleted permanently: the
// trash can is P1 (R-STOR-022), and the blobs go to the collection through the
// statement trigger of the items table. The storage hooks learn of the removed
// files in the removing transaction.
func (s *Store) Delete(ctx context.Context, spec DeleteSpec) (DeleteResult, error) {
	if spec.Path == "" {
		return DeleteResult{}, errs.E(errs.Invalid, errs.CodeInvalidPath,
			"The repository root cannot be deleted as an item; delete the repository instead.")
	}
	repo := RepoOf(spec.Repo)
	lo, hi := subtreeBounds(spec.Path)
	var count int64
	if err := s.read(ctx, func(qs *q.Queries) error {
		var err error
		count, err = qs.CountSubtree(ctx, q.CountSubtreeParams{RepoID: repo.ID, Path: spec.Path, Lo: lo, Hi: hi})
		return err
	}); err != nil {
		return DeleteResult{}, err
	}
	if count > int64(s.cfg.Delete.MaxSyncItems) {
		return s.enqueueDelete(ctx, repo, spec)
	}
	err := s.run.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		rows, err := subtree(ctx, qs, repo.ID, spec.Path, false)
		if err != nil {
			return err
		}
		// The check and the removal see the same rows, inside one transaction.
		if err := checkRows(ctx, spec.Check, rows); err != nil {
			return err
		}
		if spec.Extra != nil {
			if err := spec.Extra(ctx, tx); err != nil {
				return err
			}
		}
		if err := s.removeRows(ctx, tx, qs, who{spec.Actor, spec.Source, spec.Endpoint}, repo, rows); err != nil {
			return err
		}
		return cleanAncestors(ctx, qs, repo.ID, ParentOf(spec.Path))
	})
	if err != nil {
		return DeleteResult{}, err
	}
	s.touch(repo.ID)
	return DeleteResult{Done: true}, nil
}

// who is the actor of a removal and where the request came from.
type who struct {
	actor    audit.Actor
	source   audit.Source
	endpoint string
}

type subtreeRow struct {
	ID     int64
	Path   string
	Kind   string
	Digest []byte
	Size   int64
}

// subtree reads the live items at a path and below it, in path order, in keyset
// batches.
func subtree(ctx context.Context, qs *q.Queries, rid int64, path string, filesOnly bool) ([]subtreeRow, error) {
	lo, hi := subtreeBounds(path)
	var out []subtreeRow
	after := ""
	for {
		rows, err := qs.ListSubtreeBatch(ctx, q.ListSubtreeBatchParams{
			RepoID: rid, Path: path, Lo: lo, Hi: hi, After: after, FilesOnly: filesOnly, RowLimit: 1000,
		})
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			out = append(out, subtreeRow{ID: r.ID, Path: r.Path, Kind: r.Kind, Digest: r.BlobSha256, Size: r.Size.Int64})
		}
		if len(rows) < 1000 {
			return out, nil
		}
		after = rows[len(rows)-1].Path
	}
}

// checkRows asks the caller's check about every item of the subtree, 1,000 paths
// at a time (R-STOR-018).
func checkRows(ctx context.Context, check func(context.Context, []string) error, rows []subtreeRow) error {
	if check == nil {
		return nil
	}
	const batch = 1000
	for i := 0; i < len(rows); i += batch {
		part := rows[i:min(i+batch, len(rows))]
		paths := make([]string, len(part))
		for j, r := range part {
			paths[j] = r.Path
		}
		if err := check(ctx, paths); err != nil {
			return err
		}
	}
	return nil
}

// removeRows deletes the files by id, tells the storage hook, writes their
// events, and removes the folders of the rows deepest first, each only if it is
// empty then: a child that a concurrent deploy added keeps its folder (and is not
// removed unchecked).
func (s *Store) removeRows(ctx context.Context, tx meta.Tx, qs *q.Queries, w who, repo RepoRef, rows []subtreeRow) error {
	var fileIDs []int64
	var folders []subtreeRow
	for _, r := range rows {
		if r.Kind == KindFile {
			fileIDs = append(fileIDs, r.ID)
		} else {
			folders = append(folders, r)
		}
	}
	for i := 0; i < len(fileIDs); i += 5000 {
		removed, err := qs.DeleteItemsByID(ctx, q.DeleteItemsByIDParams{Ids: fileIDs[i:min(i+5000, len(fileIDs))], RepoID: repo.ID})
		if err != nil {
			return err
		}
		ev := RemovedEvent{Repo: repo, Reason: "delete", Actor: actorID(w.actor), Items: make([]Removed, len(removed))}
		for j, r := range removed {
			ev.Items[j] = Removed{Item: Ref{ID: r.ID, Repo: repo, Path: r.Path}, Size: r.Size.Int64, Version: r.VersionID}
			copy(ev.Items[j].Digest[:], r.BlobSha256)
		}
		if len(removed) > 0 {
			if err := s.hooks.OnItemsRemoved(ctx, tx, ev); err != nil {
				return err
			}
		}
		for _, r := range removed {
			if err := s.recordDeleted(ctx, tx, w, repo.Key, r.Path, r.BlobSha256, r.Size.Int64); err != nil {
				return err
			}
		}
	}
	sort.Slice(folders, func(i, j int) bool { return folders[i].Path > folders[j].Path })
	for _, f := range folders {
		if _, err := removeFolderIfEmpty(ctx, qs, repo.ID, f.Path, true); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) recordDeleted(ctx context.Context, tx meta.Tx, w who, repoKey, path string, digest []byte, size int64) error {
	raw, err := json.Marshal(map[string]any{"path": path, "sha256": hex.EncodeToString(digest), "size": size})
	if err != nil {
		return err
	}
	return s.rec.RecordTx(ctx, tx, audit.Event{
		Type: audit.EventArtifactDeleted, Actor: w.actor, Outcome: audit.Success, Source: w.source, Endpoint: w.endpoint,
		Subject: audit.Subject{Kind: "repository", Name: repoKey}, Repository: repoKey, Path: path, Details: raw,
	})
}

// removeFolderIfEmpty deletes a folder when no live item is below it. It takes the
// row FOR UPDATE first, so a deploy under it (which holds the row FOR SHARE) is
// waited for, and looks for children in a statement of its own, which then sees
// them. An explicit folder stays unless force is set (the caller named it).
func removeFolderIfEmpty(ctx context.Context, qs *q.Queries, rid int64, path string, force bool) (bool, error) {
	rows, err := qs.LockFolderExclusive(ctx, q.LockFolderExclusiveParams{RepoID: rid, Path: path})
	if err != nil || len(rows) == 0 {
		return false, err
	}
	if rows[0].ExplicitFolder && !force {
		return false, nil
	}
	has, err := qs.FolderHasChildren(ctx, q.FolderHasChildrenParams{RepoID: rid, Path: path})
	if err != nil || has {
		return false, err
	}
	n, err := qs.DeleteItemByIDOne(ctx, rows[0].ID)
	return n == 1, err
}

// cleanAncestors removes the implicit folders above a deleted item that have no
// other child, bottom up; an explicit folder stays (R-STOR-003).
func cleanAncestors(ctx context.Context, qs *q.Queries, rid int64, parent string) error {
	for parent != "" {
		gone, err := removeFolderIfEmpty(ctx, qs, rid, parent, false)
		if err != nil || !gone {
			return err
		}
		parent = ParentOf(parent)
	}
	return nil
}

// enqueueDelete checks the whole subtree and enqueues the job: the check
// completes before the first removal (R-STOR-018).
func (s *Store) enqueueDelete(ctx context.Context, repo RepoRef, spec DeleteSpec) (DeleteResult, error) {
	if s.jobs == nil {
		return DeleteResult{}, errs.E(errs.Unavailable, errs.CodeUnavailable, "A subtree this large is deleted by a job, and this replica has no job queue.")
	}
	var maxID int64
	err := s.read(ctx, func(qs *q.Queries) error {
		rows, err := subtree(ctx, qs, repo.ID, spec.Path, false)
		if err != nil {
			return err
		}
		maxID = 0
		for _, r := range rows {
			maxID = max(maxID, r.ID)
		}
		return checkRows(ctx, spec.Check, rows)
	})
	if err != nil {
		return DeleteResult{}, err
	}
	var res DeleteResult
	err = s.run.InTx(ctx, func(tx meta.Tx) error {
		if spec.Extra != nil {
			if err := spec.Extra(ctx, tx); err != nil {
				return err
			}
		}
		job, err := s.jobs.Enqueue(ctx, tx, jobs.EnqueueSpec{
			Kind: DeleteJobKind, DedupKey: DeleteJobKind + ":" + strconv.FormatInt(repo.ID, 10) + ":" + spec.Path,
			Args:       DeleteArgs{RepoID: repo.ID, Key: repo.Key, Class: repo.Class, Format: repo.Format, Path: spec.Path, Actor: spec.Actor, MaxID: maxID},
			TargetKind: "item", TargetKey: repo.Key + "/" + spec.Path, CreatedBy: actorID(spec.Actor),
		})
		if err != nil {
			return err
		}
		res = DeleteResult{JobID: job.Job.ID, JobState: job.Job.State}
		return nil
	})
	return res, err
}

// RunDeleteJob is the handler of [DeleteJobKind]. It decides nothing: the request
// that queued it decided every item up to DeleteArgs.MaxID. It is idempotent and
// resumable: every batch is a transaction of its own and the files go before the
// folders, so a replica that dies or drains leaves the remainder to the next
// attempt, which lists what is left (R-STOR-018: a crash resumes with the
// remainder). The storage hooks learn of each batch of files in its transaction.
func (s *Store) RunDeleteJob(ctx context.Context, run *jobs.Run) error {
	var a DeleteArgs
	if err := json.Unmarshal(run.Args, &a); err != nil {
		return jobs.Permanent(fmt.Errorf("items-delete: arguments: %w", err))
	}
	batch := max(s.cfg.Delete.BatchSize, 1)
	lo, hi := subtreeBounds(a.Path)
	var total int64
	if err := s.read(ctx, func(qs *q.Queries) error {
		var err error
		total, err = qs.CountSubtree(ctx, q.CountSubtreeParams{RepoID: a.RepoID, Path: a.Path, Lo: lo, Hi: hi})
		return err
	}); err != nil {
		return err
	}
	var done int64
	for { // the files, in batches; the rows are gone after each, so the next list starts over
		if err := ctx.Err(); err != nil {
			return context.Cause(ctx)
		}
		n, err := s.deleteFileBatch(ctx, a, lo, hi, batch)
		if err != nil {
			return err
		}
		done += n
		if err := run.SaveProgress(ctx, done, max(total, done), "items"); err != nil {
			return err
		}
		if n < int64(batch) {
			break
		}
	}
	return s.deleteFolders(ctx, run, a, lo, hi, batch, done, total)
}

func (s *Store) deleteFileBatch(ctx context.Context, a DeleteArgs, lo, hi string, batch int) (int64, error) {
	var n int64
	repo := RepoRef{ID: a.RepoID, Key: a.Key, Class: a.Class, Format: a.Format}
	err := s.run.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		rows, err := qs.ListSubtreeBatch(ctx, q.ListSubtreeBatchParams{
			RepoID: a.RepoID, Path: a.Path, Lo: lo, Hi: hi, FilesOnly: true, MaxID: a.MaxID, RowLimit: int32(batch), //nolint:gosec // storage.delete.batch_size
		})
		if err != nil {
			return err
		}
		items := make([]subtreeRow, len(rows))
		for i, r := range rows {
			items[i] = subtreeRow{ID: r.ID, Path: r.Path, Kind: r.Kind, Digest: r.BlobSha256, Size: r.Size.Int64}
		}
		n = int64(len(items))
		return s.removeRows(ctx, tx, qs, who{actor: a.Actor}, repo, items)
	})
	return n, err
}

// deleteFolders removes the folders of the subtree deepest first, each in a
// transaction of its own batch, and then the implicit folders above it.
func (s *Store) deleteFolders(ctx context.Context, run *jobs.Run, a DeleteArgs, lo, hi string, batch int, done, total int64) error {
	var folders []string
	after := ""
	err := s.read(ctx, func(qs *q.Queries) error {
		folders = folders[:0]
		after = ""
		for {
			rows, err := qs.ListSubtreeBatch(ctx, q.ListSubtreeBatchParams{
				RepoID: a.RepoID, Path: a.Path, Lo: lo, Hi: hi, After: after, MaxID: a.MaxID, RowLimit: 1000,
			})
			if err != nil {
				return err
			}
			for _, r := range rows {
				folders = append(folders, r.Path)
			}
			if len(rows) < 1000 {
				return nil
			}
			after = rows[len(rows)-1].Path
		}
	})
	if err != nil {
		return err
	}
	sort.Sort(sort.Reverse(sort.StringSlice(folders)))
	for i := 0; i < len(folders); i += batch {
		if err := ctx.Err(); err != nil {
			return context.Cause(ctx)
		}
		part := folders[i:min(i+batch, len(folders))]
		err := s.run.InTx(ctx, func(tx meta.Tx) error {
			qs, err := queries(tx)
			if err != nil {
				return err
			}
			for _, p := range part {
				if _, err := removeFolderIfEmpty(ctx, qs, a.RepoID, p, true); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		done += int64(len(part))
		if err := run.SaveProgress(ctx, done, max(total, done), "items"); err != nil {
			return err
		}
	}
	return s.run.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		return cleanAncestors(ctx, qs, a.RepoID, ParentOf(a.Path))
	})
}

// RemoveSpec is the removal of files inside a transaction of the caller.
type RemoveSpec struct {
	Repo RepoRef
	// Paths are canonical paths of files, or of folders whose subtree goes; a path
	// that holds nothing is skipped.
	Paths    []string
	Actor    audit.Actor
	Source   audit.Source
	Endpoint string
}

// RemoveIn removes the files at the paths of spec permanently, in the transaction
// of the caller, as [Store.Delete] removes a subtree: the storage hook learns of
// the removed files in this transaction, their events are written, and the
// implicit folders that stay empty go. The caller decided every path before (a
// format's route has authorized the action on its package); this checks nothing,
// and it never queues a job, so the caller bounds the number of paths.
func (s *Store) RemoveIn(ctx context.Context, tx meta.Tx, spec RemoveSpec) error {
	qs, err := queries(tx)
	if err != nil {
		return err
	}
	var rows []subtreeRow
	seen := map[int64]bool{}
	for _, p := range spec.Paths {
		if p == "" {
			return errs.E(errs.Invalid, errs.CodeInvalidPath, "The repository root cannot be removed as an item.")
		}
		found, err := subtree(ctx, qs, spec.Repo.ID, p, false)
		if err != nil {
			return err
		}
		for _, r := range found {
			if !seen[r.ID] {
				seen[r.ID] = true
				rows = append(rows, r)
			}
		}
	}
	if len(rows) == 0 {
		return nil
	}
	if err := s.removeRows(ctx, tx, qs, who{spec.Actor, spec.Source, spec.Endpoint}, spec.Repo, rows); err != nil {
		return err
	}
	for _, p := range spec.Paths {
		if err := cleanAncestors(ctx, qs, spec.Repo.ID, ParentOf(p)); err != nil {
			return err
		}
	}
	return nil
}
