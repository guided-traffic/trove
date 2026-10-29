// Package jobs is the background job framework on PostgreSQL (R-ARCH-016): the
// queue in the job table, the claim loop that only worker replicas run, leases
// on running jobs with a heartbeat, the reaper, retries with backoff,
// checkpoints, exclusive job keys, the scheduler for periodic tasks with its one
// cron parser, and the release of running jobs at shutdown.
//
// A web replica creates a [Queue] and enqueues jobs, in the transaction of the
// change that causes them. Only a [Worker] claims jobs, and NewWorker refuses a
// role that does not run workers (R-ARCH-001), so a web replica's instance ID
// never appears in job.locked_by.
//
// Kinds are registered in code, at initialisation, with [RegisterKind]; the
// registry is frozen by [Freeze] before the replica serves. A kind's semantics
// belong to its owning package. Delivery is at least once, so handlers are
// idempotent and long ones checkpoint with [Run.SaveCheckpoint].
//
// All lease and schedule decisions use PostgreSQL's clock. The package imports
// no driver: statements are the sqlc queries of internal/meta/postgres/q with
// plain Go parameters.
package jobs
