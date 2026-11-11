package ansible

import (
	"context"
	"strconv"
	"time"

	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The job kinds and the task of the Ansible Galaxy format (R-ANS-012, R-ANS-009, Q-ANS-26).
const (
	// ReindexJobKind rebuilds the version rows of a local repository from its archives (queue index, coalesce, key
	// "index:<repository id>", the key R-REPO-016 uses for a reindex).
	ReindexJobKind = "ansible-reindex"
	// DocsFillJobKind fetches the docs-blob document of one version of a remote repository (R-ANS-042; queue index, coalesce, key
	// "ansible-docs:<repository id>:<name>:<version>", see [DocsFillKey]).
	DocsFillJobKind = "ansible-docs-fill"
	// SweepTaskName is the periodic task that deletes the import tasks older than ansible.import_task_keep (queue maintenance, daily,
	// exclusive). The version a task describes stays.
	SweepTaskName = "ansible-import-task-sweep"
)

// SweepSchedule is the cron expression of the sweep: once a day; maintenance.tasks.ansible-import-task-sweep overrides it (R-STOR-025).
const SweepSchedule = "17 3 * * *"

// Timeouts of one attempt: the bound only stops a run that hangs.
const (
	reindexTimeout   = 30 * time.Minute
	docsFillTimeout  = 5 * time.Minute
	docsFillAttempts = 5
	sweepTimeout     = 5 * time.Minute
)

// Handlers are the handlers of the jobs the composition root builds: the reindex needs the reader of stored archives, which it owns.
type Handlers struct {
	// Reindex is the handler of ansible-reindex ([ReindexHandler]); nil answers the permanent not_implemented of a job nothing can run.
	Reindex jobs.Handler
	// DocsFill is the handler of ansible-docs-fill, built over the mirror host's upstream; nil answers the permanent not_implemented.
	DocsFill jobs.Handler
	// ImportTaskKeep is ansible.import_task_keep, the age at which the sweep deletes an import task.
	ImportTaskKeep time.Duration
}

// DefaultImportTaskKeep is ansible.import_task_keep while nothing sets it (R-ANS-009).
const DefaultImportTaskKeep = 720 * time.Hour

// RegisterJobs registers the kinds and the task in the registry the composition root freezes.
func RegisterJobs(r *jobs.Registry, h Handlers) {
	reindex := h.Reindex
	if reindex == nil {
		reindex = func(context.Context, *jobs.Run) error { return jobs.Permanent(errNotBuilt("the reindex")) }
	}
	docs := h.DocsFill
	if docs == nil {
		docs = func(context.Context, *jobs.Run) error { return jobs.Permanent(errNotBuilt("the documentation fetch")) }
	}
	keep := h.ImportTaskKeep
	if keep <= 0 {
		keep = DefaultImportTaskKeep
	}
	r.RegisterKind(ReindexJobKind, jobs.KindSpec{
		Queue: "index", DedupMode: jobs.Coalesce, MaxAttempts: 5, Timeout: reindexTimeout, Handler: reindex,
	})
	r.RegisterKind(DocsFillJobKind, jobs.KindSpec{
		Queue: "index", DedupMode: jobs.Coalesce, MaxAttempts: docsFillAttempts, Timeout: docsFillTimeout, Handler: docs,
	})
	r.RegisterKind(SweepTaskName, jobs.KindSpec{
		Queue: "maintenance", DedupMode: jobs.Exclusive, MaxAttempts: 3, Timeout: sweepTimeout, Handler: SweepHandler(keep),
	})
	r.RegisterTask(SweepTaskName, jobs.TaskSpec{Schedule: SweepSchedule, Mutating: true})
}

// DocsFillKey is the dedup key of the job ansible-docs-fill of a version (R-ANS-042).
func DocsFillKey(repoID int64, name, version string) string {
	return "ansible-docs:" + strconv.FormatInt(repoID, 10) + ":" + name + ":" + version
}

// SweepHandler is the handler of the task ansible-import-task-sweep: it deletes the import tasks older than keep and keeps the versions
// they describe (R-ANS-009). It reports the number it deleted as its result.
func SweepHandler(keep time.Duration) jobs.Handler {
	return func(ctx context.Context, run *jobs.Run) error {
		n, err := q.New(run.DB()).AnsibleSweepTasks(ctx, keep.Seconds())
		if err != nil {
			return err
		}
		return run.SetResult(map[string]int64{"deleted": n})
	}
}
