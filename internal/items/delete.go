package items

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"

	"github.com/guided-traffic/trove/internal/qname"

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
	RepoID int64      `json:"repoId"`
	Key    qname.Repo `json:"key"`
	// Class and Format are what a storage hook dispatches on; empty in a job
	// queued before they were recorded.
	Class  string      `json:"class,omitempty"`
	Format string      `json:"format,omitempty"`
	Path   string      `json:"path"`
	Actor  audit.Actor `json:"actor"`
	// MaxID is the highest item id the permission check saw. The job removes only
	// items up to it: an item that a deploy adds to the subtree afterwards was
	// never decided and stays (R-STOR-018). A job without it removes nothing
	// (Q-STOR-192).
	MaxID int64 `json:"maxId"`
	// Subject names the caller for the job's guard (JobGuard): who asked, with which credential, on which endpoint, and none of the
	// grants the request was decided with, which the guard reads again for every batch. A job without it removes nothing.
	Subject json.RawMessage `json:"subject"`
	// all removes every item of the subtree whatever its id, with no guard: EraseCache only, which no request asks for. A job's
	// arguments never carry it.
	all bool
}

// DeleteSpec is the removal of a file, or of a folder with its subtree.
type DeleteSpec struct {
	Repo repository.Repository
	// Path is the canonical path of the item, not the root.
	Path     string
	Actor    audit.Actor
	Source   audit.Source
	Endpoint string
	// Subject names the caller in the form the job guard decides for (see JobGuard); it is carried into the job.
	Subject json.RawMessage
	// Check is asked with the paths of the subtree, in batches of at most 1,000,
	// before anything is removed; an error stops the delete with nothing changed.
	// For a subtree removed at once it runs in the removing transaction, on the
	// rows that transaction removes; for a job, before the job is queued. The
	// store does not know why a path may not be deleted.
	Check func(ctx context.Context, paths []string) error
	// Extra runs in the transaction of the removal, or of queuing the job, after
	// Check and before anything is removed, with the canonical paths of the files
	// the removal takes away (those Check saw, in path order); an error stops the
	// delete with nothing changed. The generic layer records the allowed write with
	// it and asks the format about the files (R-AUTHZ-018: the ownership of the
	// packages they belong to).
	Extra func(ctx context.Context, tx meta.Tx, files []string) error
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
			if err := spec.Extra(ctx, tx, filePaths(rows)); err != nil {
				return err
			}
		}
		if err := s.removeRows(ctx, tx, qs, who{spec.Actor, spec.Source, spec.Endpoint}, repo, rows, scopeOf(rows)); err != nil {
			return err
		}
		return s.sweepAncestors(ctx, tx, qs, repo.ID, ParentOf(spec.Path))
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

// filePaths are the paths of the files among the rows, in their order.
func filePaths(rows []subtreeRow) []string {
	var out []string
	for _, r := range rows {
		if r.Kind == KindFile {
			out = append(out, r.Path)
		}
	}
	return out
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
func (s *Store) removeRows(ctx context.Context, tx meta.Tx, qs *q.Queries, w who, repo RepoRef, rows []subtreeRow, scope func(context.Context, string) (bool, error)) error {
	var fileIDs []int64
	var folders []subtreeRow
	for _, r := range rows {
		if r.Kind == KindFile {
			fileIDs = append(fileIDs, r.ID)
		} else {
			folders = append(folders, r)
		}
	}
	paths := make(map[int64]string, len(fileIDs))
	for _, r := range rows {
		if r.Kind == KindFile {
			paths[r.ID] = r.Path
		}
	}
	for i := 0; i < len(fileIDs); i += 5000 {
		if l, ok := s.hooks.(RemovalLocker); ok {
			chunk := make([]string, 0, min(5000, len(fileIDs)-i))
			for _, id := range fileIDs[i:min(i+5000, len(fileIDs))] {
				chunk = append(chunk, paths[id])
			}
			if err := l.LockRemoval(ctx, tx, repo, chunk); err != nil {
				return err
			}
		}
		removed, err := qs.DeleteItemsByID(ctx, q.DeleteItemsByIDParams{Ids: fileIDs[i:min(i+5000, len(fileIDs))], RepoID: repo.ID})
		if err != nil {
			return err
		}
		ev := RemovedEvent{Repo: repo, Reason: "delete", Actor: actorID(w.actor), By: w.actor, Source: w.source, InScope: scope, Items: make([]Removed, len(removed))}
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
			if err := s.recordDeleted(ctx, tx, w, repo.Name, r.Path, r.BlobSha256, r.Size.Int64); err != nil {
				return err
			}
		}
	}
	sort.Slice(folders, func(i, j int) bool { return folders[i].Path > folders[j].Path })
	removing := make(map[string]bool, len(folders))
	for _, f := range folders {
		removing[f.Path] = true
	}
	for _, f := range folders {
		if _, err := s.removeEmptied(ctx, tx, qs, repo.ID, f.Path, true, removing); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) recordDeleted(ctx context.Context, tx meta.Tx, w who, repoKey qname.Repo, path string, digest []byte, size int64) error {
	raw, err := json.Marshal(map[string]any{"path": path, "sha256": hex.EncodeToString(digest), "size": size})
	if err != nil {
		return err
	}
	return s.rec.RecordTx(ctx, tx, audit.Event{
		Type: audit.EventArtifactDeleted, Actor: w.actor, Outcome: audit.Success, Source: w.source, Endpoint: w.endpoint,
		Subject: audit.Subject{Kind: "repository", Name: repoKey.String()}, Repository: repoKey.String(), Project: repoKey.Project, Path: path, Details: raw,
	})
}

// removeFolderIfEmpty deletes a folder when no live item is below it. It takes the
// row FOR UPDATE first, so a deploy under it (which holds the row FOR SHARE) is
// waited for, and looks for children in a statement of its own, which then sees
// them. An explicit folder stays unless force is set (the caller named it). It
// waits, so it runs only in a transaction that holds no lock a deploy waits for
// after its folders: a removing transaction uses removeEmptied (Q-NUGET-88).
func removeFolderIfEmpty(ctx context.Context, qs *q.Queries, rid int64, path string, force bool) (bool, error) {
	rows, err := qs.LockFolderExclusive(ctx, q.LockFolderExclusiveParams{RepoID: rid, Path: path})
	if err != nil || len(rows) == 0 {
		return false, err
	}
	return removeLockedFolder(ctx, qs, rid, path, rows[0].ID, rows[0].ExplicitFolder, force)
}

// removeLockedFolder deletes the folder whose row this transaction locked when no live
// item is below it; an explicit folder stays unless force is set.
func removeLockedFolder(ctx context.Context, qs *q.Queries, rid int64, path string, id int64, explicit, force bool) (bool, error) {
	if explicit && !force {
		return false, nil
	}
	has, err := qs.FolderHasChildren(ctx, q.FolderHasChildrenParams{RepoID: rid, Path: path})
	if err != nil || has {
		return false, err
	}
	n, err := qs.DeleteItemByIDOne(ctx, id)
	return n == 1, err
}

// cleanAncestors removes the implicit folders above a deleted item that have no
// other child, bottom up; an explicit folder stays (R-STOR-003). It waits for the
// folders' locks: see removeFolderIfEmpty.
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
	var files []string
	err := s.read(ctx, func(qs *q.Queries) error {
		rows, err := subtree(ctx, qs, repo.ID, spec.Path, false)
		if err != nil {
			return err
		}
		maxID = 0
		for _, r := range rows {
			maxID = max(maxID, r.ID)
		}
		files = filePaths(rows)
		return checkRows(ctx, spec.Check, rows)
	})
	if err != nil {
		return DeleteResult{}, err
	}
	if maxID == 0 {
		// The subtree emptied between the count and the listing: there is nothing to remove and no job to queue (Q-STOR-192).
		return DeleteResult{Done: true}, nil
	}
	var res DeleteResult
	err = s.run.InTx(ctx, func(tx meta.Tx) error {
		// The job removes only items up to MaxID, and decides each of its batches again on the subject that asked, an insert in
		// flight at this listing included (Q-STOR-190).
		if spec.Extra != nil {
			if err := spec.Extra(ctx, tx, files); err != nil {
				return err
			}
		}
		job, err := s.jobs.Enqueue(ctx, tx, jobs.EnqueueSpec{
			Kind: DeleteJobKind, DedupKey: DeleteJobKind + ":" + strconv.FormatInt(repo.ID, 10) + ":" + spec.Path,
			Args:       DeleteArgs{RepoID: repo.ID, Key: repo.Name, Class: repo.Class, Format: repo.Format, Path: spec.Path, Actor: spec.Actor, MaxID: maxID, Subject: spec.Subject},
			TargetKind: "item", TargetKey: repo.Name.String() + "/" + spec.Path, CreatedBy: actorID(spec.Actor),
		})
		if err != nil {
			return err
		}
		res = DeleteResult{JobID: job.Job.ID, JobState: job.Job.State}
		return nil
	})
	return res, err
}

// RunDeleteJob is the handler of [DeleteJobKind]. The request that queued it decided every item up to DeleteArgs.MaxID; the job
// decides every batch again, through the guard the composition root installs ([JobGuard]), on what the subject that asked holds when
// the batch begins: the batch's own items, the package ownership of its files and what a storage hook cascades to (Q-STOR-190). A
// job without a subject or without a bound removes nothing (Q-STOR-192). A refusal, or a principal or credential that was withdrawn,
// fails the job without a further attempt, keeps what is left and records artifact.delete_stopped (Q-STOR-193); a repository that is
// gone or being deleted ends the job as done, because the repository's own deletion removes the rest (Q-STOR-191). It is idempotent
// and resumable: every batch is a transaction of its own and the files go before the folders, so a replica that dies or drains leaves
// the remainder to the next attempt, which lists what is left (R-STOR-018: a crash resumes with the remainder). The storage hooks
// learn of each batch of files in its transaction.
func (s *Store) RunDeleteJob(ctx context.Context, run *jobs.Run) error {
	var a DeleteArgs
	if err := json.Unmarshal(run.Args, &a); err != nil {
		return jobs.Permanent(fmt.Errorf("items-delete: arguments: %w", err))
	}
	if len(a.Subject) == 0 || a.MaxID <= 0 {
		return jobs.Permanent(errors.New("items-delete: the job names no subject that asked for it or no highest item id it may remove; it removes nothing"))
	}
	st := &deleteRun{run: run, name: a.Key}
	err := s.runDelete(ctx, a, st)
	switch {
	case errors.Is(err, ErrRepositoryGone):
		return nil
	case err != nil && jobs.IsPermanent(err) && refused(err):
		s.recordStopped(ctx, run.ID, a, st, err)
	}
	return jobFailure(ctx, err)
}

// deleteRun is the state of one run of the delete: the items removed so far, the name the repository had at the last batch, and
// the job whose progress it reports (nil for EraseCache).
type deleteRun struct {
	run   *jobs.Run
	done  int64
	total int64
	name  qname.Repo
}

// progress reports the items removed so far.
func (st *deleteRun) progress(ctx context.Context) error {
	if st.run == nil {
		return nil
	}
	return st.run.SaveProgress(ctx, st.done, max(st.total, st.done), "items")
}

// runDelete removes the subtree of a in batches: the files, then the folders.
func (s *Store) runDelete(ctx context.Context, a DeleteArgs, st *deleteRun) error {
	batch := max(s.cfg.Delete.BatchSize, 1)
	lo, hi := subtreeBounds(a.Path)
	if err := s.read(ctx, func(qs *q.Queries) error {
		var err error
		st.total, err = qs.CountSubtree(ctx, q.CountSubtreeParams{RepoID: a.RepoID, Path: a.Path, Lo: lo, Hi: hi})
		return err
	}); err != nil {
		return err
	}
	for { // the files, in batches; the rows are gone after each, so the next list starts over
		if err := ctx.Err(); err != nil {
			return context.Cause(ctx)
		}
		n, err := s.deleteFileBatch(ctx, a, st, lo, hi, batch)
		if err != nil {
			return err
		}
		st.done += n
		if err := st.progress(ctx); err != nil {
			return err
		}
		if n < int64(batch) {
			break
		}
	}
	return s.deleteFolders(ctx, a, st, lo, hi, batch)
}

// refused reports whether err is a refusal of the job's guard: a refused path or a withdrawn principal or credential.
func refused(err error) bool {
	k := errs.KindOf(err)
	return k == errs.Forbidden || k == errs.Unauthenticated
}

// jobFailure is the error the job reports for err (Q-STOR-193): a refusal and the job's own permanent errors keep their text, which
// the job's error shows; every other error, an errs.Error of a store and a database's text among them, is reported with a generic
// text, and its cause stays in the replica's log, which the worker writes. A cancellation or a lost lease passes as it is.
func jobFailure(ctx context.Context, err error) error {
	if err == nil || ctx.Err() != nil || jobs.IsPermanent(err) {
		return err
	}
	return errs.E(errs.Unavailable, errs.CodeUnavailable, "The delete job could not complete this attempt; it is tried again.", errs.WithCause(err))
}

// recordStopped records artifact.delete_stopped for a job its guard stopped (Q-STOR-193): the subject that asked as the actor, the
// reason code, the job, the root path and the items removed before it stopped.
func (s *Store) recordStopped(ctx context.Context, jobID string, a DeleteArgs, st *deleteRun, err error) {
	code := errs.CodeForbidden
	if e, ok := errs.As(err); ok {
		code = e.Code
	}
	raw, mErr := json.Marshal(map[string]any{"path": a.Path, "reason": code, "job": jobID, "removed": st.done})
	if mErr != nil {
		return
	}
	s.rec.Record(ctx, audit.Event{
		Type: audit.EventArtifactDeleteStopped, Actor: a.Actor, Outcome: audit.Denied, Reason: code,
		Subject:    audit.Subject{Kind: "repository", Name: st.name.String()},
		Repository: st.name.String(), Project: st.name.Project, Path: a.Path, Details: raw,
	})
}

func (s *Store) deleteFileBatch(ctx context.Context, a DeleteArgs, st *deleteRun, lo, hi string, batch int) (int64, error) {
	guard, err := s.batchGuard(ctx, a)
	if err != nil {
		return 0, err
	}
	var n int64
	repo := RepoRef{ID: a.RepoID, Name: st.name, Class: a.Class, Format: a.Format}
	if guard != nil && !guard.Name.IsZero() {
		repo.Name, st.name = guard.Name, guard.Name
	}
	err = s.run.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		rows, err := qs.ListSubtreeBatch(ctx, q.ListSubtreeBatchParams{
			RepoID: a.RepoID, Path: a.Path, Lo: lo, Hi: hi, FilesOnly: true, Bounded: !a.all, MaxID: a.MaxID,
			RowLimit: int32(batch), //nolint:gosec // storage.delete.batch_size
		})
		if err != nil {
			return err
		}
		items := make([]subtreeRow, len(rows))
		for i, r := range rows {
			items[i] = subtreeRow{ID: r.ID, Path: r.Path, Kind: r.Kind, Digest: r.BlobSha256, Size: r.Size.Int64}
		}
		n = int64(len(items))
		if guard != nil && n > 0 {
			if err := guard.files(ctx, tx, items); err != nil {
				return err
			}
			ctx = WithCascadeGuard(ctx, func(ctx context.Context, paths []string) error { return refusal(guard.Check(ctx, paths)) })
		}
		return s.removeRows(ctx, tx, qs, who{actor: a.Actor}, repo, items, jobScope(qs, a))
	})
	return n, err
}

// deleteFolders removes the folders of the subtree deepest first, each in a
// transaction of its own batch, and then the implicit folders above it.
func (s *Store) deleteFolders(ctx context.Context, a DeleteArgs, st *deleteRun, lo, hi string, batch int) error {
	var folders []string
	after := ""
	err := s.read(ctx, func(qs *q.Queries) error {
		folders = folders[:0]
		after = ""
		for {
			rows, err := qs.ListSubtreeBatch(ctx, q.ListSubtreeBatchParams{
				RepoID: a.RepoID, Path: a.Path, Lo: lo, Hi: hi, After: after, Bounded: !a.all, MaxID: a.MaxID, RowLimit: 1000,
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
		if err := s.checkFolders(ctx, a, st, part); err != nil {
			return err
		}
		err = s.run.InTx(ctx, func(tx meta.Tx) error {
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
		st.done += int64(len(part))
		if err := st.progress(ctx); err != nil {
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

// checkFolders decides a batch of folders of the job before its transaction: a folder holds no file of its own, so only its path is
// decided; the files below it were, in their batches.
func (s *Store) checkFolders(ctx context.Context, a DeleteArgs, st *deleteRun, part []string) error {
	guard, err := s.batchGuard(ctx, a)
	if err != nil || guard == nil {
		return err
	}
	if !guard.Name.IsZero() {
		st.name = guard.Name
	}
	return refusal(guard.Check(ctx, part))
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
	// InScope is the scope of the removal this one belongs to, when it is a cascade of a storage hook (RemovedEvent.InScope): the hooks of
	// the cascaded events judge a reference against the whole removal and not only against the paths of the cascade. Nil: the paths alone.
	InScope func(ctx context.Context, path string) (bool, error)
	// Check decides the files the removal takes, the subtree of a folder included, before
	// any is removed; an error refuses the whole removal. Nil decides nothing: the caller
	// decided every path before.
	Check func(ctx context.Context, files []string) error
}

// RemoveIn removes the files at the paths of spec permanently, in the transaction
// of the caller, as [Store.Delete] removes a subtree: the storage hook learns of
// the removed files in this transaction, their events are written, and the
// implicit folders that stay empty go. The caller decided the paths it names (a
// format's route has authorized the action on its package); spec.Check, when set,
// decides every file the removal takes, so a folder's files are decided one by one.
// It never queues a job, so the caller bounds the number of paths.
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
	if spec.Check != nil {
		files := make([]string, 0, len(rows))
		for _, r := range rows {
			if r.Kind == "file" {
				files = append(files, r.Path)
			}
		}
		if err := spec.Check(ctx, files); err != nil {
			return err
		}
	}
	if err := s.removeRows(ctx, tx, qs, who{spec.Actor, spec.Source, spec.Endpoint}, spec.Repo, rows, joinScope(scopeOf(rows), spec.InScope)); err != nil {
		return err
	}
	for _, p := range spec.Paths {
		if err := s.sweepAncestors(ctx, tx, qs, spec.Repo.ID, ParentOf(p)); err != nil {
			return err
		}
	}
	return nil
}

// scopeOf is the scope of a removal whose rows are all known: the files among them.
func scopeOf(rows []subtreeRow) func(context.Context, string) (bool, error) {
	files := make(map[string]bool, len(rows))
	for _, r := range rows {
		if r.Kind == KindFile {
			files[r.Path] = true
		}
	}
	return func(_ context.Context, p string) (bool, error) { return files[p], nil }
}

// jobScope is the scope of the batches of the job `items-delete`: the files of the subtree that existed when the delete was
// authorized (DeleteArgs.MaxID). Each batch is a transaction of its own, so the later batches are read from the database.
func jobScope(qs *q.Queries, a DeleteArgs) func(context.Context, string) (bool, error) {
	lo, hi := subtreeBounds(a.Path)
	return func(ctx context.Context, p string) (bool, error) {
		return qs.ItemInRemovalScope(ctx, q.ItemInRemovalScopeParams{RepoID: a.RepoID, ItemPath: p, Path: a.Path, Lo: lo, Hi: hi, Bounded: !a.all, MaxID: a.MaxID})
	}
}

// joinScope is the scope of a cascade: its own files, then the scope of the removal it belongs to.
func joinScope(own, outer func(context.Context, string) (bool, error)) func(context.Context, string) (bool, error) {
	if outer == nil {
		return own
	}
	return func(ctx context.Context, p string) (bool, error) {
		if in, err := own(ctx, p); err != nil || in {
			return in, err
		}
		return outer(ctx, p)
	}
}

// JobGuard opens the guard of one batch of the delete job for the subject the job carries ([DeleteArgs.Subject]): it reads what that
// subject holds now - whether its principal is still active and its credential was not withdrawn, its groups, its token's scope - and
// returns the decisions of the batch on it (Q-STOR-190, Q-AUTHZ-260). The composition root installs it ([Store.SetJobGuard]) once the
// files service exists. An error of kind Forbidden or Unauthenticated ends the job: a refusal and a withdrawal do not change by trying
// again. Any other error (a database that does not answer, an index that is not current) fails the attempt, and the queue retries it.
type JobGuard func(ctx context.Context, a DeleteArgs) (BatchGuard, error)

// ErrRepositoryGone is what a [JobGuard] answers when the job's repository no longer exists or is being deleted: the job ends as
// done, because the repository's own deletion removes what is left (Q-STOR-191).
var ErrRepositoryGone = errors.New("items: the repository of the delete job is gone")

// BatchGuard is what one batch of the delete job asks before it removes anything, all on the one standing the guard read.
type BatchGuard struct {
	// Name is the repository's name when the batch began: a moved repository is decided, and named in its events, where it is now
	// (Q-STOR-191). The zero name keeps the name the job carries.
	Name qname.Repo
	// Check decides `delete` on paths for the subject: the batch's own items before they are removed, and every path a storage hook
	// cascades to while they are (the cascade of an OCI manifest takes its tags, Q-OCI-76).
	Check func(ctx context.Context, paths []string) error
	// Removal asks the format about the files the batch removes, in its transaction, as the request asked about the whole subtree
	// (the package ownership of R-AUTHZ-018, Q-AUTHZ-161); nil asks nothing.
	Removal func(ctx context.Context, tx meta.Tx, files []string) error
}

// files decides the files of a batch of the job in its transaction: `delete` on each, then what the format asks about them.
func (g *BatchGuard) files(ctx context.Context, tx meta.Tx, rows []subtreeRow) error {
	paths := make([]string, len(rows))
	for i, r := range rows {
		paths[i] = r.Path
	}
	if err := refusal(g.Check(ctx, paths)); err != nil {
		return err
	}
	if g.Removal == nil {
		return nil
	}
	return refusal(g.Removal(ctx, tx, filePaths(rows)))
}

// SetJobGuard installs the guard of the delete job; it is set once, before the worker runs.
func (s *Store) SetJobGuard(g JobGuard) { s.jobGuard.Store(&g) }

// batchGuard opens the guard of one batch of the job: nil for EraseCache, which no request asks for. A job is never run unguarded: on
// a replica without a guard it fails, and so does a job that names no subject. ErrRepositoryGone passes through unmarked.
func (s *Store) batchGuard(ctx context.Context, a DeleteArgs) (*BatchGuard, error) {
	if a.all {
		return nil, nil
	}
	if len(a.Subject) == 0 {
		return nil, jobs.Permanent(errors.New("items-delete: the job names no subject that asked for it; it removes nothing"))
	}
	g := s.jobGuard.Load()
	if g == nil {
		return nil, jobs.Permanent(errors.New("items-delete: the job carries the subject that asked for it, and this replica has no guard that decides for it"))
	}
	bg, err := (*g)(ctx, a)
	if errors.Is(err, ErrRepositoryGone) {
		return nil, err
	}
	if err != nil {
		return nil, refusal(err)
	}
	if bg.Check == nil {
		return nil, jobs.Permanent(errors.New("items-delete: the guard of the job decides nothing"))
	}
	return &bg, nil
}

// refusal marks what a further attempt of the job would not change: a refused path, and a principal or credential that was withdrawn.
func refusal(err error) error {
	switch errs.KindOf(err) {
	case errs.Forbidden, errs.Unauthenticated:
		return jobs.Permanent(err)
	}
	return err
}
