package principal

import (
	"context"

	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// DB is the database the identity packages use: transactions with the
// change-log and audit hooks, and reads with the retry of R-ARCH-019.
// *postgres.DB implements it.
type DB interface {
	meta.Runner
	// Read runs fn on a pooled connection with the failover retry.
	Read(ctx context.Context, fn func(q.DBTX) error) error
}

// Notifier attaches a change of a change area to the writing transaction
// (R-ARCH-018). *cluster.Changes implements it.
type Notifier interface {
	Notify(tx meta.Tx, area string, version int, payload any) error
}

// Queries returns the sqlc queries bound to the transaction. Only the identity
// stores call it; the transaction must come from the PostgreSQL store.
func Queries(tx meta.Tx) *q.Queries {
	ptx, ok := postgres.Unwrap(tx)
	if !ok {
		panic("principal: the transaction does not come from the PostgreSQL store")
	}
	return q.New(ptx)
}
