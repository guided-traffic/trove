package deb

import (
	"context"
	"strconv"
	"time"

	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The job kinds and the task of the Debian format (R-DEB-010, R-DEB-023, R-DEB-026, Q-DEB-6, Q-DEB-33).
const (
	// IndexJobKind builds the indexes of a local or virtual repository (queue index, coalesce, key "index:<repository id>", the key
	// R-REPO-016 uses for a reindex). Its handler is [RunIndexJob] (build.go).
	IndexJobKind = "deb-index"
	// RemoteParseJobKind parses a remote's stored Packages or Sources index into rows (queue index, coalesce per repository, release
	// directory and index path). Its handler is [RunRemoteParseJob] (remote_parse.go).
	RemoteParseJobKind = "deb-remote-parse"
	// RefreshTaskName is the periodic task that enqueues what is due: retention of a repository, the check of a virtual repository, a
	// remote's housekeeping (queue maintenance, every minute, exclusive). It deletes nothing itself (Q-DEB-33). Its handler is
	// [runRefresh] (refresh.go).
	RefreshTaskName = "deb-index-refresh"
)

// IndexDedupKey is the dedup key of the index job of a repository: "index:<repository id>".
func IndexDedupKey(repoID string) string { return "index:" + repoID }

// RefreshSchedule is the cron expression of the task: every minute; maintenance.tasks.deb-index-refresh overrides it (R-STOR-025).
const RefreshSchedule = "* * * * *"

// Timeouts of one attempt. A build holds no transaction while it renders, compresses, signs or writes blobs, and a drain or a crash
// releases or expires the job (R-DEB-010); the bounds only stop a run that hangs. The parse job has its own timeout of ten minutes
// (R-DEB-034).
const (
	indexTimeout       = 30 * time.Minute
	remoteParseTimeout = 10 * time.Minute
	refreshTimeout     = 2 * time.Minute
)

// RemoteHandlers are the handlers of the jobs the remote class runs: the parse of an index and the deb-index job of a remote
// repository. The composition root builds them over the jobs' runtime ([RemoteJobs]) when it has one; a nil handler answers the
// permanent not_implemented of a job nothing can run.
type RemoteHandlers struct {
	Parse jobs.Handler
	Index jobs.Handler
	// Refresh is the remote part of the periodic task deb-index-refresh (RemoteJobs.Refresh).
	Refresh jobs.Handler
}

// RegisterJobs registers the kinds and the task in the registry the composition root freezes. The job deb-index serves two classes:
// the handler looks at the repository the job is for and hands a remote repository to the remote's handler (re-verification and
// housekeeping), every other to the index build.
func RegisterJobs(r *jobs.Registry, remote RemoteHandlers) {
	notBuilt := func(what string) jobs.Handler {
		return func(context.Context, *jobs.Run) error { return jobs.Permanent(errNotBuilt(what)) }
	}
	parse, index, refresh := remote.Parse, remote.Index, remote.Refresh
	if parse == nil {
		parse = notBuilt("the parse of a remote index")
	}
	if index == nil {
		index = notBuilt("the housekeeping of a remote")
	}
	if refresh == nil {
		refresh = func(context.Context, *jobs.Run) error { return nil } // a replica without the runtime claims no job; the task runs where jobs run
	}
	r.RegisterKind(IndexJobKind, jobs.KindSpec{
		Queue: "index", DedupMode: jobs.Coalesce, MaxAttempts: 5, Timeout: indexTimeout, Handler: classHandler(index),
	})
	r.RegisterKind(RemoteParseJobKind, jobs.KindSpec{
		Queue: "index", DedupMode: jobs.Coalesce, MaxAttempts: 5, Timeout: remoteParseTimeout, Handler: parse,
	})
	r.RegisterKind(RefreshTaskName, jobs.KindSpec{
		Queue: "maintenance", DedupMode: jobs.Exclusive, MaxAttempts: 3, Timeout: refreshTimeout, Handler: runRefresh(refresh),
	})
	r.RegisterTask(RefreshTaskName, jobs.TaskSpec{Schedule: RefreshSchedule})
}

// classHandler is the handler of deb-index: a remote repository's job is the remote's, any other the build's.
func classHandler(remote jobs.Handler) jobs.Handler {
	return func(ctx context.Context, run *jobs.Run) error {
		id, err := strconv.ParseInt(run.TargetKey, 10, 64)
		if err == nil {
			row, qerr := q.New(run.DB()).DebRemoteRepoMeta(ctx, id)
			if qerr == nil && row.Class == "remote" {
				return remote(ctx, run)
			}
		}
		return RunIndexJob(ctx, run)
	}
}
