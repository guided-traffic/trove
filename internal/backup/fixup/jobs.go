package fixup

import (
	"context"
	"errors"

	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

func init() {
	Register(Fixup{Name: "jobs", Order: OrderJobs, Apply: resetJobs})
}

// queries returns the sqlc queries on the fix-up transaction.
func queries(tx meta.Tx) (*q.Queries, error) {
	ptx, ok := postgres.Unwrap(tx)
	if !ok {
		return nil, errors.New("fixup: not a PostgreSQL transaction")
	}
	return q.New(ptx), nil
}

// resetJobs is the job row of the table in R-BAK-022: a job that was running at the snapshot runs again, one that was being
// cancelled is cancelled, and scheduled slots keep their rows so that no slot runs twice.
func resetJobs(ctx context.Context, tx meta.Tx, _ Env) error {
	qs, err := queries(tx)
	if err != nil {
		return err
	}
	_, err = qs.RestoreResetJobs(ctx)
	return err
}
