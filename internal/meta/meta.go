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

// Tx is one database transaction at READ COMMITTED (R-ARCH-019). It is only
// valid inside the function passed to the runner that created it.
//
// Tx is opaque on purpose: it has no way to run SQL, so a format or any other
// caller that is handed one cannot (R-ARCH-008). It carries what the code
// around a transaction needs: hooks that run around the commit and a slot for
// per-transaction state. The packages that own SQL (internal/meta/postgres,
// internal/cluster, the stores of internal/blob and internal/jobs) reach the
// transaction behind it with postgres.Unwrap and run sqlc queries on it.
type Tx interface {
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
	// OnRollback registers fn to run when this attempt ends without a commit:
	// the function or a Before hook failed, COMMIT failed, or the attempt was
	// cut short. It runs once per attempt, after the rollback, and never after
	// a commit. State that must not outlive the attempt is released here.
	OnRollback(fn func())
	// Value returns what SetValue stored for key in this attempt, or nil. The
	// slot belongs to the attempt: a retried transaction starts empty. key
	// should be a value of a package-private type so that packages cannot
	// collide.
	Value(key any) any
	// SetValue stores v for key in this attempt.
	SetValue(key, v any)
}

// Runner runs a function in a transaction and retries serialization failures
// and deadlocks (R-ARCH-019). Host.InTx of the formats contract is this method.
type Runner interface {
	InTx(ctx context.Context, fn func(tx Tx) error) error
}
