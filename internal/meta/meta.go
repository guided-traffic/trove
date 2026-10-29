// Package meta holds what every layer shares about the metadata store without
// knowing PostgreSQL: the transaction interface that domain code is handed and
// the revision-column registry (R-ARCH-009, R-BAK-022).
//
// It is a leaf package. It imports nothing from the module and no driver, so
// formats and the domain stay free of SQL; internal/meta/postgres implements
// the interfaces.
package meta

import "context"

// Roles a replica or a command runs in (R-ARCH-001). The class of a connection
// follows from the role (R-ARCH-019).
const (
	RoleWeb    = "web"
	RoleWorker = "worker"
	RoleAll    = "all"
	RoleBackup = "backup"
)

// RunsWorkers reports whether a role runs singleton work and therefore holds a
// lease pool (R-ARCH-017): worker and all.
func RunsWorkers(role string) bool { return role == RoleWorker || role == RoleAll }

// Row is the result of a query that returns at most one row.
type Row interface {
	Scan(dest ...any) error
}

// Rows iterates the result of a query. Close must be called, also after an
// error; Err reports the error that ended the iteration.
type Rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close()
}

// Tx is one database transaction at READ COMMITTED (R-ARCH-019). It is only
// valid inside the function passed to the runner that created it.
//
// The statement methods exist for the infrastructure packages that own SQL
// (the stores of internal/meta/postgres and internal/cluster); the domain
// reaches data through those stores and passes the Tx on without using it.
type Tx interface {
	Exec(ctx context.Context, sql string, args ...any) (rowsAffected int64, err error)
	Query(ctx context.Context, sql string, args ...any) (Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) Row

	// Before registers fn to run after the transaction function returned nil
	// and before COMMIT, in registration order, on the same transaction. This
	// is where cluster.Notify writes the change log last, so that the row of a
	// change area is held for the commit only (R-ARCH-018 step 1). An error
	// rolls the transaction back.
	Before(fn func(ctx context.Context, tx Tx) error)
	// After registers fn to run once the transaction has committed, in
	// registration order. It does not run after a rollback, and a retried
	// transaction registers its hooks again. This is where signal areas are
	// published (R-ARCH-018).
	After(fn func())
}

// Runner runs a function in a transaction and retries serialization failures
// and deadlocks (R-ARCH-019). Host.InTx of the formats contract is this method.
type Runner interface {
	InTx(ctx context.Context, fn func(tx Tx) error) error
}
