package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// DeleteJobKind is the job kind that removes a repository's items and then its
// row (R-REPO-013). It is exclusive per repository: a second delete while the job
// runs answers with the same job.
const DeleteJobKind = "repository-delete"

// DeleteJobSpec is the registration of the kind. The composition root registers
// it with the handler of its service (Service.RunDeleteJob), because the handler
// needs the service and this package holds no package-level state (R-ARCH-032).
func DeleteJobSpec(h jobs.Handler) jobs.KindSpec {
	return jobs.KindSpec{Handler: h, DedupMode: jobs.Exclusive}
}

// DeleteArgs are the arguments of a deletion job. The actor is kept so that the
// completion event names who asked.
type DeleteArgs struct {
	RepoID int64       `json:"repoId"`
	Key    string      `json:"key"`
	Actor  audit.Actor `json:"actor"`
}

// DeleteResult is the outcome of [Service.Delete].
type DeleteResult struct {
	// JobID names the job to poll; empty when the delete completed at once.
	JobID string
	// JobState is the state of that job when the delete answered: queued for a
	// new one, whatever the running job is for a repeated delete.
	JobState string
	// Done is true when the repository is gone (204); false means 202 with JobID.
	Done bool
}

// Delete implements R-REPO-013. detach asks that references from virtual
// repositories (members, pin rules, deploy targets) are removed in the same
// transaction instead of refusing with 409 repository_referenced; virtual
// membership arrives with R-REPO-017, so until then no repository is referenced
// and the flag changes nothing.
//
// The first transaction sets lifecycle = 'deleting': the repository is no longer
// resolvable or addressable, its key stays taken, and the job removes the items
// in batches and the row last. A repeated delete while the job runs answers with
// the same job. A virtual repository has no content and is removed at once.
func (s *Service) Delete(ctx context.Context, c Caller, key string, detach bool) (DeleteResult, error) {
	return s.DeleteIn(ctx, c, key, detach, "")
}

// DeleteIn is [Service.Delete] for a project administrator's delete (see
// [Service.DeleteTxIn]).
func (s *Service) DeleteIn(ctx context.Context, c Caller, key string, detach bool, delegate string) (DeleteResult, error) {
	var res DeleteResult
	err := s.run.InTx(ctx, func(tx meta.Tx) error {
		var err error
		res, err = s.DeleteTxIn(ctx, tx, c, key, detach, nil, delegate)
		return err
	})
	if err != nil {
		return DeleteResult{}, err
	}
	return res, nil
}

// DeleteTx is Delete in a transaction the caller owns, with the optional If-Match
// of R-API-012 (a delete honours it when it is sent): a version that differs from
// the stored one answers 412 and nothing changes.
func (s *Service) DeleteTx(ctx context.Context, tx meta.Tx, c Caller, key string, detach bool, pre *Precondition) (DeleteResult, error) {
	return s.DeleteTxIn(ctx, tx, c, key, detach, pre, "")
}

// DeleteTxIn is [Service.DeleteTx] for a delete that is a project administrator's:
// delegate is the key of the project it administers, "" for a holder of
// repositories:write-config. The project of the locked row is checked again here,
// so a repository that moved or was assigned between the handler's decision and the
// write answers 404 as an unknown one does (R-AUTHZ-026, Q-AUTHZ-184), before the
// precondition can tell anything about it. A virtual repository outside the project
// that lists the repository refuses the delete, detach or not (Q-REPO-129).
func (s *Service) DeleteTxIn(ctx context.Context, tx meta.Tx, c Caller, key string, detach bool, pre *Precondition, delegate string) (DeleteResult, error) {
	qs, err := queries(tx)
	if err != nil {
		return DeleteResult{}, err
	}
	rows, err := qs.LockRepositoryByKey(ctx, key)
	if err != nil {
		return DeleteResult{}, err
	}
	if len(rows) == 0 {
		return DeleteResult{}, notFound(key)
	}
	row := rows[0]
	project, err := checkProjectOfUpdate(ctx, qs, row, Write{Delegate: delegate})
	if err != nil {
		return DeleteResult{}, err
	}
	if pre != nil && !pre.Any && pre.Version != row.ConfigVersion {
		return DeleteResult{}, stale(row.ConfigVersion)
	}
	var res DeleteResult
	if Class(row.Class) == Virtual {
		return res, s.deleteNow(ctx, tx, qs, c, row, project, &res)
	}
	if Lifecycle(row.Lifecycle) == Active {
		// >>> M2 virtual
		if err := s.detachReferences(ctx, tx, qs, c, row, detach, delegate); err != nil {
			return DeleteResult{}, err
		}
		// <<< M2 virtual
		marked, err := qs.MarkRepositoryDeleting(ctx, q.MarkRepositoryDeletingParams{ID: row.ID, Actor: c.principalID()})
		if err != nil {
			return DeleteResult{}, err
		}
		if len(marked) == 0 {
			return DeleteResult{}, errors.New("repository: marking for deletion matched no row although the row was locked")
		}
		row = marked[0]
		r, _ := toRepository(row, s.cat)
		if err := s.recordAndNotify(ctx, tx, c, EventDeleted, r,
			eventDetails{Class: r.Class, Format: r.Format, Changes: []string{"lifecycle"}, Phase: "started", Project: project}, ChangeOpDeleting); err != nil {
			return DeleteResult{}, err
		}
	}
	if s.jobs == nil {
		return DeleteResult{}, errors.New("repository: no job queue, so a repository cannot be deleted")
	}
	job, err := s.jobs.Enqueue(ctx, tx, jobs.EnqueueSpec{
		Kind: DeleteJobKind, DedupKey: DeleteJobKind + ":" + strconv.FormatInt(row.ID, 10),
		Args:       DeleteArgs{RepoID: row.ID, Key: row.Key, Actor: c.Actor},
		TargetKind: "repository", TargetKey: row.Key, CreatedBy: c.principalID(),
	})
	if err != nil {
		return DeleteResult{}, err
	}
	res.JobID, res.JobState = job.Job.ID, job.Job.State
	return res, nil
}

// deleteNow removes a repository that has no content: the row goes in the
// transaction that decided it. project is the key of its project, "" for none.
func (s *Service) deleteNow(ctx context.Context, tx meta.Tx, qs *q.Queries, c Caller, row q.Repository, project string, res *DeleteResult) error {
	if Lifecycle(row.Lifecycle) == Active {
		if _, err := qs.MarkRepositoryDeleting(ctx, q.MarkRepositoryDeletingParams{ID: row.ID, Actor: c.principalID()}); err != nil {
			return err
		}
	}
	r, _ := toRepository(row, s.cat)
	if err := s.onDelete(ctx, tx, c, r); err != nil {
		return err
	}
	if _, err := qs.DeleteRepositoryRow(ctx, row.ID); err != nil {
		return err
	}
	res.Done = true
	return s.recordAndNotify(ctx, tx, c, EventDeleted, r,
		eventDetails{Class: r.Class, Format: r.Format, Changes: []string{"lifecycle"}, Phase: "completed", Project: project}, ChangeOpDeleted)
}

// onDelete runs the delete hook in the transaction that removes the row.
func (s *Service) onDelete(ctx context.Context, tx meta.Tx, c Caller, r Repository) error {
	if s.hooks == nil {
		return nil
	}
	return s.hooks.OnDelete(ctx, tx, c, r)
}

// RunDeleteJob is the handler of [DeleteJobKind]. It is idempotent and resumable:
// every batch is a transaction of its own, a replica that dies or drains leaves
// the remaining items to the next attempt, and the row goes last, which frees the
// key (R-REPO-013). Progress is read from the job resource.
func (s *Service) RunDeleteJob(ctx context.Context, run *jobs.Run) error {
	var a DeleteArgs
	if err := json.Unmarshal(run.Args, &a); err != nil {
		return jobs.Permanent(fmt.Errorf("repository-delete: arguments: %w", err))
	}
	total, err := s.countItems(ctx, a.RepoID)
	if err != nil {
		return err
	}
	var done int64
	for {
		if err := ctx.Err(); err != nil {
			return context.Cause(ctx)
		}
		n, err := s.deleteBatch(ctx, a.RepoID)
		if err != nil {
			return err
		}
		done += n
		if err := run.SaveProgress(ctx, done, max(total, done), "items"); err != nil {
			return err
		}
		if n < int64(s.batch) {
			break
		}
	}
	return s.finishDelete(ctx, a)
}

func (s *Service) countItems(ctx context.Context, id int64) (int64, error) {
	var n int64
	err := s.run.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		n, err = qs.CountRepositoryItems(ctx, id)
		return err
	})
	return n, err
}

func (s *Service) deleteBatch(ctx context.Context, id int64) (int64, error) {
	var n int64
	err := s.run.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		n, err = qs.DeleteRepositoryItemsBatch(ctx, q.DeleteRepositoryItemsBatchParams{ID: id, Batch: int32(s.batch)}) //nolint:gosec // bounded by Options
		return err
	})
	return n, err
}

// finishDelete removes the row and records the completion in one transaction,
// which also tells every replica that the key is free and that the row is gone
// from the grant index (R-REPO-013: the row's removal calls Notify("repo_config")).
func (s *Service) finishDelete(ctx context.Context, a DeleteArgs) error {
	return s.run.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		rows, err := qs.GetRepositoryByID(ctx, a.RepoID)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil // an earlier attempt finished
		}
		r, _ := toRepository(rows[0], s.cat)
		project, err := projectKeyOf(ctx, qs, rows[0])
		if err != nil {
			return err
		}
		if err := s.onDelete(ctx, tx, Caller{Actor: a.Actor}, r); err != nil {
			return err
		}
		// >>> M2 virtual
		// The claims go with the row (ON DELETE CASCADE). Every replica drops them
		// from its index within the change-log bound; until then a global claim of
		// the deleted repository would keep refusing its names (R-REPO-013).
		claims, err := qs.CountRepositoryClaims(ctx, a.RepoID)
		if err != nil {
			return err
		}
		// <<< M2 virtual
		n, err := qs.DeleteRepositoryRow(ctx, a.RepoID)
		if err != nil {
			return err
		}
		if n == 0 {
			return errors.New("repository-delete: the row is not marked for deletion")
		}
		// >>> M2 virtual
		if claims > 0 {
			if err := s.notif.Notify(tx, cluster.AreaClaims, 1, claimsPayload{ID: r.ID, Key: r.Key, Op: "deleted"}); err != nil {
				return err
			}
		}
		// <<< M2 virtual
		return s.recordAndNotify(ctx, tx, Caller{Actor: a.Actor}, EventDeleted, r,
			eventDetails{Class: r.Class, Format: r.Format, Changes: []string{"lifecycle"}, Phase: "completed", Project: project}, ChangeOpDeleted)
	})
}
