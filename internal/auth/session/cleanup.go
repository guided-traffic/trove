package session

import (
	"context"

	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// TaskSessionCleanup deletes expired login flows and command-line logins and
// session rows 30 days after their end (R-AUTH-043). It runs as a maintenance
// task on worker replicas, once per slot in the cluster.
const TaskSessionCleanup = "session-cleanup"

// cleanupSchedule is the default of maintenance.tasks.session-cleanup.
const cleanupSchedule = "*/10 * * * *"

// batch is the row limit of one statement, and so of one transaction
// (R-AUTH-043).
const batch = 1000

func init() {
	jobs.RegisterKind(TaskSessionCleanup, jobs.KindSpec{
		Queue: "maintenance", DedupMode: jobs.Exclusive, MaxAttempts: 3, Handler: runCleanup,
	})
	jobs.RegisterTask(TaskSessionCleanup, jobs.TaskSpec{Schedule: cleanupSchedule})
}

// CleanupResult counts what one run deleted.
type CleanupResult struct {
	LoginFlows int64 `json:"loginFlows"`
	CLILogins  int64 `json:"cliLogins"`
	Sessions   int64 `json:"sessions"`
}

func runCleanup(ctx context.Context, run *jobs.Run) error {
	res, err := Cleanup(ctx, run.DB())
	if err != nil {
		return err
	}
	return run.SetResult(res)
}

// Cleanup runs the three deletions in batches of at most 1000 rows, each its own
// transaction. Every statement is conditional on the row's state, so a run that
// overlaps another, or resumes on another worker, deletes nothing twice.
func Cleanup(ctx context.Context, db q.DBTX) (CleanupResult, error) {
	var res CleanupResult
	qs := q.New(db)
	steps := []struct {
		count *int64
		run   func(context.Context) (int64, error)
	}{
		{&res.LoginFlows, qs.DeleteExpiredLoginFlows},
		{&res.CLILogins, qs.DeleteExpiredCLILogins},
		{&res.Sessions, qs.DeleteOldSessions},
	}
	for _, st := range steps {
		for {
			if err := ctx.Err(); err != nil {
				return res, err
			}
			n, err := st.run(ctx)
			if err != nil {
				return res, err
			}
			*st.count += n
			if n < batch {
				break
			}
		}
	}
	return res, nil
}
