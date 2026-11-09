// Package sysstate holds the two settings that live in system_state and that every replica reads: the system banner (R-OPS-025) and
// the read-only maintenance mode (R-OPS-024). A write goes to PostgreSQL with its audit event and a note in the change area cache
// in one transaction; every replica keeps a cached copy that the note flushes, bounded by a safety TTL, so a change is in force
// everywhere within the poll of the change log whether Valkey is available or not.
//
// The package knows nothing of HTTP. The management API (internal/api/v1) reads and writes through [Store]; the places that refuse a
// write in read-only mode (the management API's shared step, the format dispatcher, the files service, the scheduler) ask
// [Store.RefuseWrite] or [Store.Maintenance].
//
// This is not the deletion pause of R-HA-025 (internal/maintenance, `trove admin maintenance pause-deletions`): that flag stops only
// garbage collection, the orphan sweep, retention and cap enforcement, and is set by a restore; the read-only mode refuses client
// writes and is set by an administrator through the API.
package sysstate
