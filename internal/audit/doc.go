// Package audit is the event registry and the recording contract of audit
// events (R-OPS-014, R-OPS-015, R-OPS-016, R-OPS-017) and of the revocation
// journal flag (Q-OPS-22, R-HA-054).
//
// Every domain package imports it. It therefore imports nothing from the module
// but errs and meta, and it knows no principal type: an [Actor] is plain data.
//
// Two things live here for all areas:
//
//   - The event registry. An event type is registered once, in
//     events_<area>.go of this package (one file per work-package area, so
//     parallel work does not collide), with its category, audit mode, webhook
//     flag, journal flag and data schema. The registry is filled at
//     initialisation and frozen before the replica serves ([Freeze]).
//   - The [Recorder] interface. A transactional event is inserted in the
//     caller's transaction together with its revocation_outbox row when its type
//     carries the journal flag and the producer passes the journal entry. An
//     asynchronous event goes through a bounded buffer and never blocks a request.
//
// The implementation of the recorder (PostgreSQL insert, buffer, batch writer,
// log copy, deduplication, query API) belongs to work package WP1.7.
package audit
