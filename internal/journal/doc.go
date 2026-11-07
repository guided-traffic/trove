// Package journal is the revocation journal (R-HA-054): every access-bearing change, and every change that withdraws or
// reserves a package version where a format page asks for it, is recorded a second time outside PostgreSQL, and a restore
// replays the removals and reservations among the entries newer than its point in time. Replay never grants access and
// never deletes content.
//
// The package fixes the shared shapes and seams, so that the owners of the domains build their parts in files of their own:
//
//   - object.go: the journal object, its JSON encoding and its object key. The outbox row (revocation_outbox, written by
//     internal/audit in the transaction of the change) becomes one object per entry under
//     <storage.s3.prefix>/_journal/revocations/<yyyy>/<mm>/<dd>/<id>.json.
//   - ports.go: the object-store port, the outbox port, and the shipper and reader interfaces.
//   - replay.go: the outcomes of a replay, the [Handler] registry (one handler per event name, registered by the domain
//     that owns the event), the update rule of R-HA-054 as the generic [ApplyUpdate] over a domain's [Narrower], and the
//     [Replayer] that the restore and `trove admin post-restore` run.
//
// A domain adds replay for its events in its own file with [Register]: identity (token, user, service account, group),
// authorization (role binding, policy, project, ownership, settings, repository move), CI issuers and mappings, endpoint
// exposure, npm unpublish, NuGet versions and the vulnerability block. No domain edits another's file and there is no
// shared switch.
//
// The producer side is internal/audit: an event type registered with Journal: JournalRevocation carries an entry (a JSON
// object of IDs and states, never a secret, a token hash or a name used for matching) that audit.RecordTx inserts into
// revocation_outbox in the same transaction. The entry is the producer's; this package wraps it in the envelope.
package journal
