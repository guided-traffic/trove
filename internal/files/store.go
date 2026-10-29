package files

import (
	"context"
	"errors"

	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

var errForeignTx = errors.New("files: the transaction is not one of internal/meta/postgres")

// queries returns the sqlc queries on the transaction behind tx. The package
// writes no SQL: its statements live in internal/meta/postgres/queries/files.sql.
func queries(tx meta.Tx) (*q.Queries, error) {
	ptx, ok := postgres.Unwrap(tx)
	if !ok {
		return nil, errForeignTx
	}
	return q.New(ptx), nil
}

// read runs fn on a transaction of the runner. Reads of the management API and
// of the transfer ask PostgreSQL every time (R-API-052).
func (s *Service) read(ctx context.Context, fn func(*q.Queries) error) error {
	return s.run.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		return fn(qs)
	})
}

// liveItem reads the live item at a path; found is false when there is none.
func (s *Service) liveItem(ctx context.Context, repoKey string, repoID int64, path string) (it Item, found bool, err error) {
	err = s.read(ctx, func(qs *q.Queries) error {
		rows, err := qs.GetLiveItem(ctx, q.GetLiveItemParams{RepoID: repoID, Path: path})
		if err != nil || len(rows) == 0 {
			return err
		}
		it, found = itemFrom(repoKey, rows[0]), true
		return nil
	})
	return it, found, err
}
