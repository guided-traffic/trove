// Package backup is the domain of trove's own backup and restore (INV-19, docs/spec/backup-and-restore.md): the contracts
// that the commands of the `backup` role are built on, and the small parts every command shares.
//
// What is here (the lane's work packages build on it and do not change it without a decision; docs/developer/backup.md
// lists the owners):
//
//   - exit.go: the exit codes of R-BAK-013 and the errors that map to them.
//   - state.go: the operations, states, holds and triggers of a run (R-BAK-009).
//   - runstore.go: the run record in PostgreSQL, the one-at-a-time rule and the fenced transitions (R-BAK-007,
//     R-BAK-009, R-BAK-010), over the queries in internal/meta/postgres/queries/backup.sql.
//   - handle.go: the lease, heartbeat and stall detection of a running operation (R-BAK-010, R-BAK-013).
//   - ports.go: the interfaces to the source database and the blob store that the create pipeline plugs into.
//   - runner.go: the operation interfaces (Creator, Lister, Verifier, Deleter, Pruner, Restorer), their options and
//     results; cmd/trove wires one implementation of each.
//   - metrics.go: the metric families of R-BAK-014, registered once.
//
// The subpackages: format (the on-target format), target (the port of the S3 target and an in-memory fake), tables (the
// export classes of R-BAK-004) and fixup (the registry of restore fix-ups). The revocation journal is its own package,
// internal/journal.
//
// Layering (R-ARCH-002): backup is a domain package. It reaches PostgreSQL through internal/meta/postgres and its
// generated queries, and the S3 target and the blob store through interfaces whose implementations live in
// internal/blob/s3, the only package that imports the AWS SDK.
package backup
