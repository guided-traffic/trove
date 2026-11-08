package deb

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
)

// Jobs enqueues a job in the caller's transaction; *jobs.Queue implements it.
type Jobs interface {
	Enqueue(ctx context.Context, tx meta.Tx, e jobs.EnqueueSpec) (jobs.Result, error)
	// Running returns the job that runs under a dedup key.
	Running(ctx context.Context, tx meta.Tx, key string) (jobs.Job, bool, error)
}

// IndexJobArgs are the arguments of a job of kind [IndexJobKind] (R-DEB-010, R-DEB-014, Q-ARCH-384). The zero value is an automatic
// build of the suites whose change_seq exceeds their built_seq. The index builder reads them from the job; this file only writes them.
type IndexJobArgs struct {
	// Full asks for a build of every suite, also those nothing changed in (a restore's fix-up, a reindex).
	Full bool `json:"full,omitempty"`
	// Reindex says that the build was requested through the reindex endpoint (R-REPO-016). It implies Full, and it is what a second
	// request meets as 409 reindex_running while it waits or runs.
	Reindex bool `json:"reindex,omitempty"`
}

func (a IndexJobArgs) normalized() IndexJobArgs {
	a.Full = a.Full || a.Reindex
	return a
}

// EnqueueBuild enqueues an automatic build of the repository's changed suites in the caller's transaction, to run after delay
// (deb.index_delay for an upload, zero for a key change). A build that waits already is moved to the earlier run time and keeps its
// arguments, so a request for "now" never waits behind a delayed build (Q-ARCH-384); a running build is not touched and the new job
// waits behind it. It does nothing and returns nil when no queue is configured: a process that writes no rows needing a build (the
// restore's) has none.
func (s *Store) EnqueueBuild(ctx context.Context, tx meta.Tx, repoID int64, delay time.Duration) error {
	if s.o.Jobs == nil {
		return nil
	}
	_, err := s.o.Jobs.Enqueue(ctx, tx, buildSpec(repoID, delay, IndexJobArgs{}, nil, ""))
	return err
}

func buildSpec(repoID int64, delay time.Duration, args IndexJobArgs, merge func(json.RawMessage) (json.RawMessage, bool), by string) jobs.EnqueueSpec {
	id := strconv.FormatInt(repoID, 10)
	return jobs.EnqueueSpec{
		Kind: IndexJobKind, DedupKey: IndexDedupKey(id), Args: args.normalized(), Delay: delay, MergeArgs: merge,
		TargetKind: "repository", TargetKey: id, CreatedBy: by,
	}
}

// Reindex enqueues the full build the reindex endpoint asks for (R-REPO-016, Q-ARCH-384), in the caller's transaction. A build that
// waits automatically is upgraded to a full reindex and moved to now; the job it returns is that one. A reindex that meets a reindex
// that waits or runs enqueues nothing and reports the conflict with that job. A build that runs automatically does not conflict: the
// reindex waits as the one job behind it.
func (s *Store) Reindex(ctx context.Context, tx meta.Tx, repoID int64, by string) (job jobs.Job, conflict bool, err error) {
	if s.o.Jobs == nil {
		return jobs.Job{}, false, errNotBuilt("the index build queue")
	}
	// A running reindex is the one a request meets as a conflict; the key's job is looked up first because the enqueue below would
	// queue a second one behind it.
	if running, ok, err := s.activeReindex(ctx, tx, repoID); err != nil {
		return jobs.Job{}, false, err
	} else if ok {
		return running, true, nil
	}
	waitingReindex := false
	merge := func(waiting json.RawMessage) (json.RawMessage, bool) {
		var a IndexJobArgs
		_ = json.Unmarshal(waiting, &a) //nolint:errcheck // arguments we wrote; a malformed one is upgraded like an automatic build
		if a.Reindex {
			waitingReindex = true
			return waiting, false
		}
		out, _ := json.Marshal(IndexJobArgs{Full: true, Reindex: true}) //nolint:errcheck // a struct of two booleans marshals
		return out, true
	}
	res, err := s.o.Jobs.Enqueue(ctx, tx, buildSpec(repoID, 0, IndexJobArgs{Reindex: true}, merge, by))
	if err != nil {
		return jobs.Job{}, false, err
	}
	return res.Job, !res.Created && waitingReindex, nil
}

// activeReindex returns the reindex that runs for the repository, if one does.
func (s *Store) activeReindex(ctx context.Context, tx meta.Tx, repoID int64) (jobs.Job, bool, error) {
	j, ok, err := s.o.Jobs.Running(ctx, tx, IndexDedupKey(strconv.FormatInt(repoID, 10)))
	if err != nil || !ok {
		return jobs.Job{}, false, err
	}
	var a IndexJobArgs
	_ = json.Unmarshal(j.Args, &a) //nolint:errcheck // arguments we wrote; a malformed one is no reindex
	return j, a.Reindex, nil
}
