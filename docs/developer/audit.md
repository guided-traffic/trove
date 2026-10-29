# Audit trail

Package [internal/audit](../../internal/audit): the event registry, the recorder, the revocation journal row, the store-side query
and the partition and retention maintenance. Requirements: R-OPS-014 (registry), R-OPS-015 (recording), R-OPS-016 (catalog),
R-OPS-017 (query) in [operations-and-observability.md](../spec/operations-and-observability.md); the journal in R-HA-054
([ha-and-helm.md](../spec/ha-and-helm.md)). Decisions: Q-OPS-38 to Q-OPS-44. The security view is
[security/audit.md](../security/audit.md).

The package is a domain package. The contract files (`registry.go`, `event.go`) import only `meta`; the implementation also uses
`meta/postgres` and its sqlc package `q`, as the job queue does. Every other domain package takes `audit.Recorder` and registers
its event types here.

## Files

| File | Responsibility |
|---|---|
| [registry.go](../../internal/audit/registry.go), [schema.go](../../internal/audit/schema.go) | `EventType`, `Register` (panics on a programming error), `Freeze`, `Lookup`, `Types`; the rules of R-OPS-014 and the bounded-array check of the data schema |
| [event.go](../../internal/audit/event.go) | `Event`, `Actor`, `Subject`, `Source`, `Outcome`, `Recorder`, `Nop` |
| [events_audit.go](../../internal/audit/events_audit.go), `events_<area>.go` | the types of one area; the audit area registers `audit.exported` |
| [recorder.go](../../internal/audit/recorder.go) | `Writer`, `New`, `RecordTx` (the transactional path), the journal rule, the PostgreSQL sink |
| [async.go](../../internal/audit/async.go) | `Record` (the asynchronous path), the bounded buffer, the batch loop, the backoff, `Flush` |
| [dedup.go](../../internal/audit/dedup.go) | deduplication of `authz.denied` and `token.rejected`, the switches for anonymous denials and downloads |
| [row.go](../../internal/audit/row.go) | `Event` to table row: validation, truncation, UUID and address handling, the error sentinels |
| [logcopy.go](../../internal/audit/logcopy.go) | the `log: audit` record |
| [store.go](../../internal/audit/store.go) | `Store`: `Query`, `Get`, `Maintain`, `PartitionsReady`, `Prepare`, `SyncRetention`, `CheckExportRange` |
| [uuid.go](../../internal/audit/uuid.go) | UUIDv7 for event IDs and the time they carry |
| [../api/v1/audit_api.go](../../internal/api/v1/audit_api.go), [../api/v1/audit_source.go](../../internal/api/v1/audit_source.go) | The three management operations; the recorder wrapper that adds the request's source |
| [../meta/postgres/queries/audit.sql](../../internal/meta/postgres/queries/audit.sql) | every statement; generated code in `q/audit.sql.go` |
| [../meta/postgres/audit.go](../../internal/meta/postgres/audit.go) | `IsDataError`, `IsPrivilegeError`, and the migrate runner's retention write |
| [../app/wire_audit.go](../../internal/app/wire_audit.go) | start-up step 4 and the shutdown flush |

## Model

An event is a row of `audit_event` ([00006_audit.sql](../../internal/meta/postgres/migrations/00006_audit.sql)): partitioned by month on
`occurred_at`, primary key `(occurred_at, id)`, no foreign keys. `id` is a UUIDv7 the producer's clock makes; `occurred_at` is the
transaction's `now()` for a transactional event and the producer's time for an asynchronous one (Q-OPS-43). The columns, and which field of
`audit.Event` fills each, are:

| Column | From | Note |
|---|---|---|
| `action`, `category` | the registry entry of `Event.Type` | the event cannot name another category |
| `outcome` | `Event.Outcome` | `success` (the default when empty), `failure`, `denied` |
| `actor_type`, `actor_name` | `Actor.Kind`, `Actor.Name` | `break_glass` is stored as `breakglass` |
| `actor_id`, `token_id` | `Actor.ID`, `Actor.TokenID` | stored only when a UUID |
| `auth_method` | `Actor.AuthMethod` | |
| `source_ip`, `user_agent`, `trace_id` | `Source` | the address may carry a port; not an IP: null |
| `replica` | `Options.Replica` | the instance name |
| `endpoint`, `repository`, `path`, `reason` | `Endpoint`, `Repository`, `Path`, `Reason` | a repository subject fills `repository` when empty |
| `resource_type`, `resource_id` | `Subject.Kind`, `Subject.ID` (else `Subject.Name`) | |
| `details` | `Event.Details` | a JSON object; `{}` when empty; never a secret |

## Registry

`Register` is called from the `init` of `events_<area>.go` and panics on: an invalid name, unknown category or mode, a duplicate, a
journal flag on a type that is not transactional, an entry rule without the flag, an `optional` entry outside the shared types
(`package.published`, `package.deleted`), a schema that is not a JSON object or has an array without `maxItems` of at most 1,000, or a
registration after `Freeze`. `Freeze` runs in `freezeRegistries` of the composition root. Names are a public contract (R-OPS-014): never
reuse or rename one without the alias rule.

## Transactional and asynchronous recording

```text
RecordTx(ctx, tx, ev)                       Record(ctx, ev)
  registry lookup, mode, journal rule         registry lookup, switches (anonymous denials, downloads)
  toRow; INSERT audit_event (now())           toRow (producer time), UUIDv7
  [INSERT revocation_outbox, same tx]         dedup: first / repeat / summary
  tx.After: log copy, counter                 non-blocking send into the buffer (async_buffer)
  any error fails the caller's transaction      full: log copy + trove_audit_events_dropped_total
                                              loop: batch of 500 or every second, one INSERT
                                              written: log copy + trove_audit_events_total
```

- `RecordTx` is for `Transactional` types and returns an error for anything else; a `None` type is a no-op. The caller returns the
  error from its transaction function, which rolls the change back. Errors are the sentinels `ErrUnknownType`, `ErrWrongMode`,
  `ErrJournalEntryRequired`, `ErrJournalNotAllowed`, `ErrInvalidEvent`, or the PostgreSQL error.
- `Record` is for `Async` types and never returns anything. A bad call (an unregistered or transactional type, an invalid event) is
  logged at error level and skipped, because the request must not fail.
- The batch statement takes the rows as one JSON array of text values, so it is one statement of one shape ([audit.sql](../../internal/meta/postgres/queries/audit.sql),
  `InsertAuditEvents`); `ON CONFLICT DO NOTHING` makes a retry after an ambiguous failure harmless.
- While PostgreSQL does not answer, the writer keeps the batch and retries with a backoff of 0.5 s doubling to 10 s; Record keeps
  returning at once, and events that find the buffer full go to the log stream and the dropped counter. A batch the table refuses for
  its data (class 22/23) is split, and only the refused rows are dropped (Q-OPS-41).
- `Flush(ctx)` at shutdown (the `Flusher` phase, step 6): stops intake, writes the buffer and every open deduplication summary, and gives
  up at the context's deadline, writing the rest to the log stream. After `Flush` an event goes to the log stream only.

## Journal row

An event type with `Journal: audit.JournalRevocation` and `Entry` required (the default) must come with `Event.Journal`, a JSON object
of IDs and states, never a secret or a hash. `RecordTx` then inserts the `revocation_outbox` row with the event's `id`, its
`occurred_at` as `at` and the registry name as `event`, in the same transaction. An optional-entry type writes a row only when the
producer passes an entry. The shape of the entry belongs to the owning page; the writer checks only that it is an object. Shipping the
rows is the journal writer of a later milestone: it reads `revocation_outbox` in `id` order and needs nothing the row does not carry.

## Deduplication

`authz.denied` and `token.rejected` are the only deduplicated types, matched by name ([dedup.go](../../internal/audit/dedup.go)):

| Type | Key | Window |
|---|---|---|
| `authz.denied` | actor ID (else name), `details.required_action`, repository, path | `audit.denial_dedup_window` |
| `authz.denied`, anonymous | client address | the same; recorded only with `audit.record_anonymous_denials` |
| `token.rejected` | `Subject.ID` (else `details.token_id`; else the client address) | `audit.token_rejection_dedup_window` |

The first occurrence is written at once with `details.count` 1; repeats are counted in memory; the closed window yields one summary
with `details.count` = the repeats and `details.repeat_of` = the first event's ID, in a new event with the first one's fields. A window
of 0 turns deduplication off. The table holds at most `async_buffer` open windows (Q-OPS-42). `artifact.downloaded` is recorded only with
`audit.record_downloads`. A producer therefore passes `details.required_action` with every `authz.denied`.

## Query

`Store.Query(ctx, Filter, Page)` returns `Result{Items, Next}`; `Next` is the keyset position `(OccurredAt, ID)` of the last item, to
pass back as `Page.After`. Newest first by default; `Page.Oldest` is the export order. A page is one short read on the main pool, with no
transaction held between pages, which is what the export needs (R-OPS-017). Defaults: `From` = the last 24 hours, `To` open, `Limit` 100,
at most 1000. `Filter.Action` is an exact name or a prefix ending in `.*` (the prefix is matched on the whole dotted name, so `a.b.*`
does not match `a.bc.d`). `Filter.Actor` is an ID or a name. `Get(ctx, id)` reads only the partitions around the time the UUIDv7
carries. `CheckExportRange(from, to)` is the export's range check (both bounds, at most 400 days, code `invalid_audit_range`). The
handlers are a thin layer ([below](#management-operations)).

## Management operations

[audit_api.go](../../internal/api/v1/audit_api.go) serves the store. The three operations declare `permission: audit:read`
(R-AUTHZ-010; the R-OPS-017 text calls it `audit.read`), which the authorization step enforces before the handler; the store has
no check of its own ([H-123](../security/audit.md) is closed by that, and by `TestR_OPS_017_PermissionAndFilters`).

| operationId | Method and path | Permission | Notes |
|---|---|---|---|
| `listAuditEvents` | `GET /api/v1/audit/events` | `audit:read` | newest first; filters `from`, `to`, `action` (exact or `prefix.*`), `category`, `outcome`, `actor`, `repository`, `resourceType`, `resourceId`, `traceId`; `limit` 1 to 1,000, default 100; cursor over `(occurredAt, id)` with the fingerprint of the filters |
| `getAuditEvent` | `GET /api/v1/audit/events/{id}` | `audit:read` | `404` for an unknown ID and for a string that is not an event ID |
| `exportAuditEvents` | `GET /api/v1/audit/events/export` | `audit:read` | `from` and `to` required (`400 invalid_audit_range`), at most 400 days; `format=jsonl` (default) or `csv`; oldest first |

How the pieces fit:

- The page limits are the audit ones (`audit.DefaultLimit`, `audit.MaxLimit`), passed to `ParseList` through a copy of the API
  configuration; `Store.Query` does its own `limit + 1` read, so the handler builds `Page` itself and encodes the cursor with
  `ListQuery.EncodeCursor([occurredAt, id])`. A cursor issued for other filters is `400 invalid_cursor`.
- The export first commits `audit.exported` through `Server.recorder().RecordTx` in a transaction of its own (`recordExport`), then
  loops `Store.Query(..., Oldest: true)` in batches of 1,000 and writes each batch with a flush. No transaction is open between
  batches. A failing batch panics with `http.ErrAbortHandler`, which the chain re-raises, so the chunked answer ends without its
  terminating chunk and the client sees a cut export. The document gives the streamed media types no schema (a line-delimited stream
  is not one JSON document); the JSON Lines line is `AuditEvent`.
- `from` and `to` of the export are declared optional in the document although required, so the generated wrapper does not answer
  its generic 400 before `audit.CheckExportRange` answers `invalid_audit_range`.
- The CSV encoder prefixes a cell that would open as a formula with an apostrophe (`csvSafe`).

### The source of an event

[audit_source.go](../../internal/api/v1/audit_source.go) (`WithRequestSource`) wraps the recorder at the composition root
(`startAudit` sets `a.m1.recorder` to the wrapper). An event recorded with a request context gets the client address, the trace
ID, user agent and the endpoint when its producer left them empty, so a domain package never passes the request down (Q-OPS-49). The source comes from `httpserver.SourceFrom(ctx)` (the chain keeps the User-Agent in `RequestInfo`); the actor of a request comes from `v1.ActorFrom(httpserver.Principal)`, which fills the token ID and the authentication method, and a principal ID in an actor passes `principal.IDOrEmpty` (Q-OPS-152). The deduplication
of an anonymous refusal keys on that address, and the filter `traceId` finds an event by the `X-Request-Id` the client received.

## Partitions and retention

| Where | What | Code |
|---|---|---|
| `trove migrate up` | writes `audit.retention_days` into `audit_policy` when it differs, then calls `audit_maintain_partitions()` | `postgres.syncAuditPolicy`, `MigrateOptions.AuditRetentionDays` |
| start-up step 4 (`bootstrap`) | `Store.Prepare`: reconcile the retention (warn, naming `trove migrate`, when the runtime role may not write it), call the function, require the current and next month's partitions; retries with backoff while it fails, so the replica stays unready | `App.startAudit` |
| the `audit-retention` task | calls `Store.Maintain` daily (`a.m1.auditStore`) | registered by the jobs area (R-OPS-019) |
| a restore | `audit_create_partitions(from, to)` as the schema owner | the restore Job (M5) |

## How an area adds an event type

1. Add the name to the domain table of [m1-contracts.md](m1-contracts.md#audit-event-naming) if it is new: `<domain>.<event>` or
   `<domain>.<object>.<event>`, lower snake case, past tense last.
2. In `internal/audit/events_<area>.go` call `Register` from `init`: category, mode (`Transactional` for state changes, `Async` for
   access), the webhook flag, the data schema (every array with `maxItems` of at most 1,000). Add `Journal: audit.JournalRevocation` when
   the event removes access, updates an access-bearing object or withdraws content a restore would revive; leave `Entry` empty for the
   required default.
3. At the change, call `rec.RecordTx(ctx, tx, audit.Event{...})` inside the transaction function and return its error. Fill the actor
   from the principal, the source from the request, `Subject`, `Repository` and `Path` where they apply, `Details` per the schema
   (`changes` for an update, a secret only as `set`/`changed`/`cleared`), and `Journal` for a flagged type (before and after state
   without secrets for an update).
4. For an access event call `rec.Record(ctx, ev)`; it cannot fail. Pass `details.required_action` for `authz.denied`.
5. Test: an integration test that triggers the action and finds exactly one event with `actor_type`, `outcome` and the schema's keys
   (R-OPS-016 acceptance), and for a flagged type one `revocation_outbox` row with the event's `id`. Use `audit.Nop{}` in unit tests that
   do not look at the event, or a fake `Recorder` that keeps the events.

The composition root holds the recorder as `a.m1.recorder` (set at step 4) and the store as `a.m1.auditStore`.

## Tests

| Where | What |
|---|---|
| [internal/audit](../../internal/audit) `*_test.go` | `journal_list_test.go` reads the table of R-HA-054 from the specification and checks that every event it names is registered with the journal flag, transactional and with a required entry (the events of areas that do not exist yet are on an explicit pending list, and a registered one must leave it); registry rules, row mapping, the writer against a fake sink (batching, backoff, drop on a full buffer, a refused row, flush at a deadline), deduplication, the switches |
| [test/integration/audit](../../test/integration/audit) | `tx_test.go` rollback, journal row, fault injection, ties; `async_test.go` batching, the locked table, the lost connection, a refused row, dedup over replicas; `partitions_test.go` start-up, retention, migrate; `query_test.go` ordering, keyset, filters, `Get`; `privileges_test.go` the runtime role |
| [test/integration/login/e1_5_test.go](../../test/integration/login/e1_5_test.go) | E1.5 across all the producers it names (login, failed login, token creation and revocation, policy change, denied deploy) through the real routes, found by the query and in the log stream ([login.md](login.md)) |
| [test/integration/audit/api_test.go](../../test/integration/audit/api_test.go) on [test/integration/stack](../../test/integration/stack) | E1.5 through HTTP on three replicas (policy changes and a denied deploy found by the query and present in the log stream), the permission on all three operations, the filter validation, a limit=1 walk across replicas with a write between pages, the correct count and `repeat_of` of denial summaries and the anonymous key by address, the export (2,300 events over three batches in both formats, `audit.exported` first, a slow reader holding no transaction, bad ranges, an export that cannot be recorded), and `authz.settings.changed` from a real repository update |
| [internal/api/v1/audit_api_test.go](../../internal/api/v1/audit_api_test.go) | the filter and cursor parsing, the representation, the CSV encoder, `WithRequestSource` |

Run: `set -a; . .dev/integration/env; set +a; go test -count=1 -tags integration ./test/integration/audit/...` (see [build-and-test.md](build-and-test.md)).

## Not built here

The `audit-retention` task registration (jobs area); resuming an export on the server side (the client resumes with an inclusive `from`);
the journal writer that ships `revocation_outbox` (M5); the registration of the catalog events other than `audit.exported` (each area, R-OPS-016);
the check that every event listed in R-HA-054 is registered, flagged and required, which needs every area's registry file and therefore runs
after the merge.
