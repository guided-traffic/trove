// Package audit is the event registry, the recorder and the query side of the
// audit trail (R-OPS-014, R-OPS-015, R-OPS-016, R-OPS-017) and the home of the
// revocation journal flag (Q-OPS-22, R-HA-054).
//
// Every domain package imports it for the registry and the [Recorder] contract,
// and it knows no principal type: an [Actor] is plain data. The contract files
// (registry.go, event.go) import nothing from the module but meta; the
// implementation files also use meta/postgres and its sqlc package, like the job
// queue does.
//
// What lives here:
//
//   - The event registry. An event type is registered once, in
//     events_<area>.go of this package (one file per work-package area, so
//     parallel work does not collide), with its category, audit mode, webhook
//     flag, journal flag and data schema. The registry is filled at
//     initialisation and frozen before the replica serves ([Freeze]).
//   - The [Writer], the implementation of [Recorder]. A transactional event is
//     inserted in the caller's transaction together with its revocation_outbox
//     row when its type carries the journal flag and the producer passes the
//     journal entry. An asynchronous event goes through a bounded buffer and a
//     batch writer and never blocks or fails a request; authz.denied and
//     token.rejected are deduplicated first. Every stored event also appears in
//     the log stream (log: audit).
//   - The [Store]: the query by time range and filters with keyset pagination
//     (the handler of GET /api/v1/audit/events is a thin layer on top), the
//     lookup by id, and the partition and retention maintenance.
package audit
