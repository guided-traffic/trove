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

type joinKey struct{}

// Join returns a context in which a [Joinable] database runs its transactions and
// reads inside tx instead of opening its own. The management API uses it to make
// an idempotent create one transaction with its idempotency row (R-API-014): the
// domain call keeps its code, and its change, its audit event and the row commit
// or roll back together. A call that the outer transaction retries runs again
// from the start.
func Join(ctx context.Context, tx meta.Tx) context.Context {
	return context.WithValue(ctx, joinKey{}, tx)
}

// Joinable wraps a [DB] so that [Join] takes effect. Without a joined
// transaction in the context it is the wrapped database.
type Joinable struct{ DB }

// InTx runs fn in the joined transaction, or in a new one of the wrapped database.
func (j Joinable) InTx(ctx context.Context, fn func(tx meta.Tx) error) error {
	if tx, ok := ctx.Value(joinKey{}).(meta.Tx); ok {
		return fn(tx)
	}
	return j.DB.InTx(ctx, fn)
}

// Read runs fn on the joined transaction, so that a read after a write of the same
// call sees the write, or on a pooled connection of the wrapped database.
func (j Joinable) Read(ctx context.Context, fn func(q.DBTX) error) error {
	if tx, ok := ctx.Value(joinKey{}).(meta.Tx); ok {
		ptx, ok := postgres.Unwrap(tx)
		if !ok {
			panic("principal: the joined transaction does not come from the PostgreSQL store")
		}
		return fn(ptx)
	}
	return j.DB.Read(ctx, fn)
}
