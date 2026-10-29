package v1

import (
	"context"

	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// KindIdempotencyCleanup is the job kind and task name that deletes the expired
// rows of api_idempotency_keys (R-API-014, R-OPS-019).
const KindIdempotencyCleanup = "idempotency-cleanup"

// The task is registered at initialisation like every package's kinds; the
// composition root copies the default registry into its own (jobs.Default).
func init() {
	jobs.RegisterKind(KindIdempotencyCleanup, jobs.KindSpec{
		Queue: "maintenance", DedupMode: jobs.Exclusive, MaxAttempts: 3,
		Handler: func(ctx context.Context, run *jobs.Run) error {
			n, err := q.New(run.DB()).DeleteExpiredIdempotencyKeys(ctx)
			if err == nil {
				err = run.SetResult(map[string]int64{"deleted": n})
			}
			return err
		},
	})
	jobs.RegisterTask(KindIdempotencyCleanup, jobs.TaskSpec{Schedule: "13 * * * *"})
}
