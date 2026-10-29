# Audit trail

Status: implemented in M1 for the writer, the append-only grants, the partition and retention maintenance, the revocation
journal row and the store-side query. The management API operations that expose the query and the export are wave-2 work and are
not built yet; the permission check `audit.read` belongs to them (see [H-123](#h-123--the-store-has-no-permission-check-of-its-own)).

This page says who can write audit events, who can change or delete them, where else a copy lives, and what the journal row of
the revocation journal contains. The mechanism is described for contributors in [developer/audit.md](../developer/audit.md);
the requirements are R-OPS-014 to R-OPS-017 in [operations-and-observability.md](../spec/operations-and-observability.md).

## Who writes, who reads, who can change

| Party | Writes events | Reads events | Changes or deletes an event | Changes retention |
|---|---|---|---|---|
| A `web` or `worker` replica (runtime role, two-role mode) | yes, through the parent table | yes (the store) | no: `UPDATE`, `DELETE` and `TRUNCATE` on `audit_event` are revoked, and the role holds nothing on the partitions | no: `audit_policy` is `SELECT` only |
| The migration Job (schema owner) | no | yes | yes (it owns the table) | yes: it writes `audit.retention_days` on every run |
| A replica in single-role mode | yes | yes | yes: it owns the schema ([H-71](privilege-footprint.md#h-71--single-role-mode-gives-the-runtime-login-ddl-rights)) | yes: it writes the row itself at start-up |
| A superuser | yes | yes | yes | yes |

The revokes are in [00006_audit.sql](../../internal/meta/postgres/migrations/00006_audit.sql); the runtime role keeps `INSERT` and
`SELECT` on `audit_event`, `SELECT` on `audit_policy` and `EXECUTE` on `audit_maintain_partitions()`. The integration test
[privileges_test.go](../../test/integration/audit/privileges_test.go) runs the writer through the runtime role's own pool and checks
that every `UPDATE`, `DELETE` and `TRUNCATE` on the table, on its current partition and on `audit_policy`, and the call of
`audit_create_partitions()`, fail with `42501`.

The runtime role cannot shorten retention, but the partition maintenance it may call drops partitions older than the stored
value. The function is `SECURITY DEFINER` with a `search_path` fixed by the migration, and it reads the retention from
`audit_policy`, not from its caller; so a compromised replica can call it, and it deletes only what the owner's value already
allowed.

## Retention, who writes it, and what a replica does when it differs

`trove migrate up` writes `audit.retention_days` into `audit_policy` when the value differs, then calls the maintenance function,
under the migration lock ([audit.go](../../internal/meta/postgres/audit.go), `syncAuditPolicy`). A lowered value therefore drops
the now-expired partitions at the next migrate, which the chart runs on every upgrade. A replica reconciles at start-up
([store.go](../../internal/audit/store.go), `SyncRetention`): when the configured value differs from the stored one it logs a warning
that names `trove migrate`, and in single-role mode it writes the row itself. `0` keeps every partition. A value below 90 logs a
warning in the migrate Job and in every replica.

## Where an event can be lost, and where else it is

- A transactional event is in the transaction of its change. The insert failing fails the change, so no change exists without its
  record, and a rolled-back change leaves neither the event nor its journal row nor its log copy
  ([recorder.go](../../internal/audit/recorder.go), `RecordTx`; the log copy is an `After` hook).
- An asynchronous event is prepared on the request path and written in batches. While PostgreSQL is away the writer keeps its batch
  and retries; the buffer (`audit.async_buffer`) fills behind it; an event that finds it full is written to the log stream and
  counted in `trove_audit_events_dropped_total`. At shutdown the writer flushes under the shutdown deadline and writes what is left
  to the log stream.
- An event that reached the table is also written as a `log: audit` record after the commit. For an event that never reached the
  table, the log record is the only copy, which is why shipping the stream out of trove matters ([H-39](#h-39--audit-records-are-not-tamper-evident)).

## The revocation journal row

For an event type flagged `journal: revocation` the writer inserts a `revocation_outbox` row in the same transaction: the event's
`id`, its `occurred_at` as `at`, its registry name and the producer's entry. A flagged type with a required entry fails the
transaction when the producer passes none, so a producer bug cannot silently drop a revocation; an entry on an unflagged type is an
error, so one cannot be silently ignored either. The entry is whatever the producer supplies; the writer checks that it is a JSON
object and stores it as it is. Producers keep secrets and token hashes out of it ([H-121](#h-121--event-details-and-journal-entries-are-not-checked-for-secrets)).
Reading the outbox and shipping it to object storage is the journal writer of a later milestone and is not built here.

## What the writer does with attacker-influenced text

The user agent and the path of an event come from the request. The writer cuts each text column to 1 or 4 KiB on a character
boundary and removes NUL bytes, so a hostile header cannot make a row large or make the insert fail. An actor or token ID that is not a
UUID is not stored in the `uuid` column; the actor's name is. A client address is stored only when it parses as an IP address.
Details larger than 1 MiB are refused; at that size the producer is the bug.

## What this does not cover

### H-39 · Audit records are not tamper-evident

In two-role mode, trove's runtime role can only insert and read audit events, and it cannot shorten
their retention (R-OPS-015). Nothing makes the records tamper-evident, though:

- the schema owner and any superuser can alter or delete rows;
- in single-role mode, trove's own role is the schema owner, so a compromised replica can too;
- the runtime role can insert forged events in either mode.

Integrity against those holders rests on shipping the `log: audit` stream to a store outside
trove's control, for example object storage with object lock or a SIEM. Neither trove nor the
chart enforces that (Q-OPS-4). The gap is live from the first release for every installation that
does not ship logs externally. For single-role installations it also covers a compromised replica.
It becomes real for anyone who obtains the owner credential of the migration Job, a superuser
login, or, in single-role mode, the runtime credential.

### H-121 · Event details and journal entries are not checked for secrets

Stage: `RecordTx` and `Record` ([row.go](../../internal/audit/row.go), `toRow`; [recorder.go](../../internal/audit/recorder.go), `journalEntry`).

Live today. The writer checks that details and a journal entry are JSON objects and bounds their size; it does not look inside. A
producer that puts a token value, a hash, a password or an authorization code into `details` or into the entry stores it in
`audit_event` or `revocation_outbox` and, for the log copy, writes it to the log stream, where only the logger's pattern scrubber
([redact.go](../../internal/observability/redact.go)) stands in the way for trove-format tokens. Subject: every secret a
producer might hold at the moment it records an event. The data schemas of the registry list the allowed keys but are not enforced at
insert.

What makes it real: a producer change that adds a field carrying a credential. The stored copy and the exported copy are not scrubbed;
the log copy is scrubbed for trove token patterns only.

### H-122 · A crashed replica loses the buffered asynchronous events and its open summaries

Stage: the asynchronous writer ([async.go](../../internal/audit/async.go)).

Live today for every asynchronous event type (`authz.denied`, `token.rejected`, the login events, `artifact.downloaded`). The buffer,
the batch in flight and the open deduplication windows live in process memory. A process killed without `SIGTERM` loses up to
`audit.async_buffer` plus one batch of events that were not yet in the table, and the repeat counts of windows that were still open;
the sum of `details.count` for a burst is then low by the repeats that had not been summarised. Their log copy was written only for
events that reached the table or were dropped, so a killed replica leaves no log record of the buffered ones either.

What makes it real: an OOM kill or node loss during a burst of denials. Transactional events are not affected.

### H-123 · The store has no permission check of its own

Stage: `Store.Query` and `Store.Get` ([store.go](../../internal/audit/store.go)).

Dormant until the management API operations of the audit area exist (wave 2). The store reads every event for whoever calls it; the
`audit.read` permission of R-OPS-017 and the audit of a single-event read are the handler's to enforce. The only read that the
requirement audits, the export, is recorded by the handler through the registered `audit.exported` type.

What makes it live: a handler that calls the store without the operation declaring `x-trove-permission` for `audit.read`. Every
management operation carries that declaration by convention ([http-api.md](../developer/http-api.md)); nothing in the store enforces it.
