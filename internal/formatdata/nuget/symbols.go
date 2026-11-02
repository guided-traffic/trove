package nuget

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// IndexJobKind is the job kind that indexes one symbol package (Q-NUGET-11, R-NUGET-021):
// queue index, so the packages of one repository spread over every worker replica, and
// exclusive per symbol package (dedup key IndexJobKind + ":" + version id, R-ARCH-016).
const IndexJobKind = "nuget-symbol-index"

// Job states the indexing job records as its result.
const (
	// IndexGone: the version or its symbol package was removed before the job ran.
	IndexGone = "gone"
	// IndexDone: the symbol package carries indexed_at already, set by an earlier run.
	IndexDone = "indexed"
	// IndexStored: the symbol package is stored and no key was extracted from it. This build
	// ships the push and the job, not the symbol server (R-NUGET-021 is P1): the job leaves
	// indexed_at unset, so the release that ships the server indexes this package.
	IndexStored = "stored"
)

// IndexArgs are the arguments of an indexing job.
type IndexArgs struct {
	VersionID    int64 `json:"version_id"`
	RepositoryID int64 `json:"repository_id"`
}

// IndexResult is what the indexing job records (jobs.Run.SetResult) for the job API.
type IndexResult struct {
	State string `json:"state"`
	// Files is the number of symbol files the run extracted and stored.
	Files int `json:"files"`
}

// IndexJobSpec is the registration of the indexing kind.
func IndexJobSpec() jobs.KindSpec {
	return jobs.KindSpec{Queue: "index", DedupMode: jobs.Exclusive, Handler: RunIndexJob}
}

func init() { jobs.RegisterKind(IndexJobKind, IndexJobSpec()) }

// Jobs is the part of the job queue the store uses: an enqueue in the caller's transaction,
// so the job exists exactly if the symbol package commits (R-ARCH-016). *jobs.Queue
// implements it.
type Jobs interface {
	Enqueue(ctx context.Context, tx meta.Tx, e jobs.EnqueueSpec) (jobs.Result, error)
}

// enqueueIndex enqueues the indexing job of one symbol package in the transaction of its
// push. An enqueue that meets a queued or running job of the package returns that job.
func (s *Store) enqueueIndex(ctx context.Context, tx meta.Tx, versionID, repoID int64) error {
	_, err := s.o.Jobs.Enqueue(ctx, tx, jobs.EnqueueSpec{
		Kind: IndexJobKind, DedupKey: IndexJobKind + ":" + strconv.FormatInt(versionID, 10),
		Args: IndexArgs{VersionID: versionID, RepositoryID: repoID},
	})
	return err
}

// RunIndexJob is the handler of the indexing kind (Q-NUGET-11). It is idempotent, as every
// handler is: it reads the state of the symbol package and records it. A package that is
// gone, or indexed already, is a success with nothing to do.
//
// The extraction of the symbol files and their keys belongs to the symbol server
// (R-NUGET-021, P1); until it ships, a run records that the package is stored and leaves
// indexed_at unset, so that the release which ships the server finds the package pending
// and indexes it (Q-NUGET-60).
func RunIndexJob(ctx context.Context, run *jobs.Run) error {
	var args IndexArgs
	if err := jsonArgs(run.Args, &args); err != nil {
		return jobs.Permanent(err)
	}
	res, err := q.New(run.DB()).NuGetSymbolPackageForIndex(ctx, args.VersionID)
	if err != nil {
		return err
	}
	out := IndexResult{State: IndexStored}
	switch {
	case len(res) == 0:
		out.State = IndexGone
	case res[0].IndexedAt.Valid:
		out.State = IndexDone
	}
	return run.SetResult(out)
}

func jsonArgs(raw []byte, into *IndexArgs) error {
	if err := json.Unmarshal(raw, into); err != nil {
		return errs.E(errs.Internal, errs.CodeInternal, "nuget-symbol-index: the arguments are unreadable", errs.WithCause(err))
	}
	if into.VersionID <= 0 {
		return errs.E(errs.Internal, errs.CodeInternal, "nuget-symbol-index: the arguments name no version")
	}
	return nil
}
