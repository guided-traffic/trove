package ansible

import (
	"context"
	"time"

	"github.com/guided-traffic/trove/internal/jobs"
)

// The job kinds and the task of the Ansible Galaxy format (R-ANS-012, R-ANS-009, Q-ANS-26).
const (
	// ReindexJobKind rebuilds the version rows of a local repository from its archives (queue index, coalesce, key
	// "index:<repository id>", the key R-REPO-016 uses for a reindex).
	ReindexJobKind = "ansible-reindex"
	// SweepTaskName is the periodic task that deletes the import tasks older than ansible.import_task_keep (queue maintenance, daily,
	// exclusive). The version a task describes stays.
	SweepTaskName = "ansible-import-task-sweep"
)

// SweepSchedule is the cron expression of the sweep: once a day; maintenance.tasks.ansible-import-task-sweep overrides it (R-STOR-025).
const SweepSchedule = "17 3 * * *"

// Timeouts of one attempt: the bound only stops a run that hangs.
const (
	reindexTimeout = 30 * time.Minute
	sweepTimeout   = 5 * time.Minute
)

// RegisterJobs registers the kinds and the task in the registry the composition root freezes. The handlers are the stubs of the
// skeleton: the reindex answers a permanent not_implemented (nothing enqueues it before the endpoint is built) and the sweep has
// nothing to delete before a task is stored. The local class replaces both in this file.
func RegisterJobs(r *jobs.Registry) {
	r.RegisterKind(ReindexJobKind, jobs.KindSpec{
		Queue: "index", DedupMode: jobs.Coalesce, MaxAttempts: 5, Timeout: reindexTimeout,
		Handler: func(context.Context, *jobs.Run) error { return jobs.Permanent(errNotBuilt("the reindex")) },
	})
	r.RegisterKind(SweepTaskName, jobs.KindSpec{
		Queue: "maintenance", DedupMode: jobs.Exclusive, MaxAttempts: 3, Timeout: sweepTimeout,
		Handler: func(context.Context, *jobs.Run) error { return nil },
	})
	r.RegisterTask(SweepTaskName, jobs.TaskSpec{Schedule: SweepSchedule, Mutating: true})
}
