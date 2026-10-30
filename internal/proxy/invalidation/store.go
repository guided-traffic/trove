package invalidation

import (
	"context"
	"errors"

	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

var errForeignTx = errors.New("invalidation: the transaction is not one of internal/meta/postgres")

// queries returns the sqlc queries on the transaction behind tx. The package writes
// no SQL: its statements live in internal/meta/postgres/queries/invalidation.sql.
func queries(tx meta.Tx) (*q.Queries, error) {
	ptx, ok := postgres.Unwrap(tx)
	if !ok {
		return nil, errForeignTx
	}
	return q.New(ptx), nil
}

// loadRows reads the rows of one repository, or of all repositories for repo 0.
func loadRows(ctx context.Context, run meta.Runner, repo int64) ([]row, error) {
	var out []row
	err := run.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		out = out[:0]
		if repo == 0 {
			rs, err := qs.InvalidationListAll(ctx)
			if err != nil {
				return err
			}
			for _, r := range rs {
				out = append(out, row{id: r.ID, repoID: r.RepoID, prefix: r.Prefix, scope: r.Scope, createdUs: r.CreatedUs})
			}
			return nil
		}
		rs, err := qs.InvalidationListRepository(ctx, repo)
		if err != nil {
			return err
		}
		for _, r := range rs {
			out = append(out, row{id: r.ID, repoID: r.RepoID, prefix: r.Prefix, scope: r.Scope, createdUs: r.CreatedUs})
		}
		return nil
	})
	return out, err
}
