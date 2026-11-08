package deb

import (
	"time"

	"github.com/guided-traffic/trove/internal/jobs"
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
	// [RunRefreshJob] (refresh.go).
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

// RegisterJobs registers the kinds and the task in the registry the composition root freezes. The handlers are functions of the files
// of the packages that own them; until those are built they answer a permanent not_implemented, and nothing enqueues the kinds.
func RegisterJobs(r *jobs.Registry) {
	r.RegisterKind(IndexJobKind, jobs.KindSpec{
		Queue: "index", DedupMode: jobs.Coalesce, MaxAttempts: 5, Timeout: indexTimeout, Handler: RunIndexJob,
	})
	r.RegisterKind(RemoteParseJobKind, jobs.KindSpec{
		Queue: "index", DedupMode: jobs.Coalesce, MaxAttempts: 5, Timeout: remoteParseTimeout, Handler: RunRemoteParseJob,
	})
	r.RegisterKind(RefreshTaskName, jobs.KindSpec{
		Queue: "maintenance", DedupMode: jobs.Exclusive, MaxAttempts: 3, Timeout: refreshTimeout, Handler: RunRefreshJob,
	})
	r.RegisterTask(RefreshTaskName, jobs.TaskSpec{Schedule: RefreshSchedule})
}
