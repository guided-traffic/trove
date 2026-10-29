# Database layer

PostgreSQL access lives in [internal/meta/postgres](../../internal/meta/postgres/) (the only package with
`internal/cluster` that imports the driver, R-ARCH-002). [internal/meta](../../internal/meta/) is the
driver-free leaf the rest of the code sees: `meta.Tx`, `meta.Runner`, `meta.TxState` and the revision-column registry.
[internal/backup/tables](../../internal/backup/tables/) is the table registry of R-BAK-004.

## Connection classes

`postgres.Open(ctx, cfg config.Database, instance, role, log)` opens the pools of one replica. Every
connection is named `trove:<instance>:<class>` (`application_name`, cut to 63 bytes keeping the class).

| Class | Accessor | Role | Endpoint | Notes |
|---|---|---|---|---|
| `main` request pool | `Main()` | web, worker, all | `database.host` (direct or pooler) | `pool.max_conns - 1` connections; transaction-scoped, no session state |
| `main` control connection | `Control(ctx)`, `ControlPool()` | web, worker, all | control endpoint (`control_host/port/name`, else host) | pool of size 1; change-log poll, readiness, heartbeats only |
| `lease` | `LeasePool()` | worker, all (nil on web) | session host (`session_host/port`, else host) | `lease_pool_size` connections held with their session locks; no lifetime |
| `migrate` | `Migrate`, `Status` | `trove migrate` | session host | one connection, `statement_timeout` 0 |
| `backup` | not yet | `trove backup` | session host | M5 |

Behind `database.pooler.mode: transaction` the main and control pools send only `application_name` and
`TimeZone` as startup parameters and use pgx `QueryExecModeExec` (unnamed extended-protocol statements, no
per-connection statement cache), which works with and without `max_prepared_statements`. `statement_timeout`,
`lock_timeout`, `idle_in_transaction_session_timeout` and `search_path` then come from role defaults
(`ALTER ROLE <login> IN DATABASE <db> SET …`); `Open` reads them on both endpoints and refuses to start naming the
login and the setting. Direct and session-mode connections send all of them, plus `tcp_keepalives_*`, as startup
parameters, and wrap pgx's dial with client keepalive and `TCP_USER_TIMEOUT` (Linux).

`Open` checks, with the retry below: server version >= 16, `UTF8`, runtime role not superuser unless
`database.preflight.allow_superuser`, and on `worker`/`all` that the session endpoint keeps session state
(five `pg_backend_pid()` calls; a pooler that refuses the startup parameters gives the same
`ErrSessionState` message). It warns when the runtime role owns the schema (single-role mode).

Credentials: `username_file` and `password_file` are read again by `BeforeConnect` for every new connection.
The connection string carries neither.

## Transactions and retry

- `DB.InTx(ctx, func(meta.Tx) error)` is `meta.Runner` (`var _ meta.Runner = (*DB)(nil)`) and the one form
  callers use. `meta.Tx` is opaque (R-ARCH-008: formats never run SQL): it has `Before` (runs last inside the
  transaction; change-log writes, R-ARCH-018), `After` (once COMMIT succeeded), `OnRollback` (once when the attempt
  ends without a commit: failed body, failed `Before` hook, failed COMMIT) and a per-attempt value slot
  (`Value`/`SetValue`, a retried attempt starts empty). Hooks and slot are `meta.TxState`, which the unit-test
  fakes embed too (`RunBefore`, `RunAfter`, `RunRollback`).
- Stores reach SQL only through `postgres.Unwrap(tx)` (the pgx transaction) and sqlc (`q.New(ptx)`), as
  `internal/jobs` and `internal/blob` do. `DB.InPgxTx(ctx, func(pgx.Tx) error)` is the hook-less pgx form for this
  package and the retry tests. `DB.Tx` is an old name of `InTx` that stays only until the coordination integration
  tests and the e2e driver use `InTx`. `RequestTx` adds `statement_timeout` via `set_config(..., true)`.
- The one place SQL text is assembled is [blobrefs.go](../../internal/meta/postgres/blobrefs.go): the collector's
  reference check and full-sweep anti-join are built from the foreign keys to `blobs` read from the catalog
  (R-STOR-024 rule 3), every identifier quoted with `pgx.Identifier`, no value in the text. `postgres.LoadBlobRefs`
  reads the catalog once; the collector does that once per batch.
- READ COMMITTED. SQLSTATE 40001 and 40P01 repeat the function up to three times. Failover errors (08xxx except
  08P01, 57P01-03, 25006, 53300, closed or refused connections) repeat with exponential backoff until
  `retry.max_elapsed` and then become `errs.Unavailable` (`database_unavailable`, 503). A connection that proved
  wrong is destroyed, not returned to the pool. A COMMIT that loses its connection has an unknown outcome and is
  never repeated. `fn` must therefore be safe to run again.
- `DB.Read(ctx, func(q.DBTX) error)` is the form for a read outside a transaction on a request path (a handler, the
  facts of `GET /system/info`). It has the retry, the bound and the error mapping of `InTx`: a failover error repeats
  on a new connection until `retry.max_elapsed`, the connection that failed is destroyed, and the failure that
  outlasts the bound is `database_unavailable` (503). Other errors come back unchanged. `fn` runs on one pooled
  connection without a transaction (several statements do not share a snapshot; use `InTx` for that) and must read
  every row before it returns. Do not call `db.Main()` for a handler read: the raw pool has no retry, and a
  failover turned into a `500` that way (found by the drain and failover harness, [e2e.md](e2e.md)). `SystemFacts`,
  `ServerVersion` and `SchemaState` go through it; `Main()` stays for the job queue, the registry and the gauge
  scrape, which have their own loops. Tests: `TestRead` in
  [test/integration/postgres/read_test.go](../../test/integration/postgres/read_test.go) (terminated backends, the
  bound, the facts reads).
- `Ready(ctx)` is the `database` readiness check: `SELECT NOT pg_is_in_recovery()` on the control connection.
  `CheckSchema(ctx, version)` is the `schema` check (below) and reads on the control connection; `SchemaState`
  (GET /system/info) reads on the main pool through `Read`, so a request never needs the control connection.
  `Ready` returns `ErrStandby` for a server in recovery and destroys that connection (the control pool holds one, so
  the next check connects again and finds the new primary); `Read` destroys the connection when `fn` panics;
  `classify` treats a failed TLS verification of the server (`x509` and `tls.CertificateVerificationError` inside the
  `ConnectError`) as `retryNo`, not as an outage; `IsOutage(err)` is `IsUnavailable` or `ErrStandby`, the
  errors that get the outage grace of the readiness checks (below). `CheckBinaryVersion(v)` reports whether the
  binary's version is `major.minor.patch` or `dev`; `postgresComponent` logs an ERROR at start for a version that is
  not (`checkBinaryVersion`, [version_check.go](../../internal/app/version_check.go)). `Stats()` feeds the pool metrics.

## Connection budget

`postgres.ConnectionBudget(cfg, webMax, workerMax, superuserReserved)` is the formula of R-HA-016. The migration
runner compares it with `max_connections` when `database.preflight.expected_*_replicas` are set (`enforce` fails,
`warn` logs). The backup share is a constant of 4 until `backup.database.parallel` exists.

## Migrations

Files: `internal/meta/postgres/migrations/NNNNN_<name>.sql`, embedded. Numbers run 1..N without a gap. A file starts
with header comments:

```sql
-- trove:phase expand            (or contract; exactly one)
-- trove:no-transaction          (one statement, e.g. CREATE INDEX CONCURRENTLY; must be idempotent)
-- trove:requires-app >= 0.4.0   (contract only, required there)
```

`ParseSteps` (unit-tested) rejects a missing or duplicate phase, a contract step without `requires-app`,
`requires-app` on an expand step, and `DROP`, `RENAME` or `SET NOT NULL` in an expand file.

### How to add a migration

1. Create the next number as an expand file. Unqualified names: the runner sets `search_path`.
2. New table: register its export class in the owning package (`tables.Register`); a `bigint` revision column
   (`rev_seq`, `revision`, `epoch`, `*_version`) needs `meta.RegisterRevisionColumn`. The integration tests
   `TestTableRegistry` and `TestRevisionColumns` fail otherwise.
3. A table with a blob reference: column `blob_sha256`, FK to `blobs`, index, both triggers
   (`TestBlobReferenceRules`).
4. Never edit an applied file (checksum). Backfills are background data migrations, not migration steps.
5. Contract steps ship one minor after the release that stopped using the object, with `requires-app`.
6. Static queries for the new tables go to `queries/*.sql`, then `make generate`.
   A test that injects a migration of its own names it with `nextStep(n, name)`
   ([env_test.go](../../test/integration/postgres/env_test.go)), which numbers the file after the embedded ones, so
   it needs no edit when a milestone adds migrations.
7. `trove migrate up` checks the migrated schema against the table registry of its own binary
   (`postgres.VerifyTableRegistry`, exit 3 naming the table); [cmd/trove/registry.go](../../cmd/trove/registry.go)
   links every package that owns tables. A new owning package is added there.

`sqlc` reads the migration files as its schema. `job_state` is created inside a `DO` block on purpose: sqlc
then maps it to a Go string instead of generating enum helpers that import `database/sql/driver` (forbidden,
R-ARCH-026).

### Runner (`postgres.Migrate`, `trove migrate up`)

Preflight, schema creation if missing, `pg_try_advisory_lock(3, 0)` polled for up to `migrate.advisory_lock_wait`
(the timeout names the holder from `pg_stat_activity`), refusal of a half-restored database (exit 3), checksum
check, plan (out-of-order steps fail), contract guard (exit 3 when a live replica is below `requires-app`;
live means a `replica.heartbeat_at` within `system.heartbeat_stale_after`), then per step one transaction:
`SET LOCAL lock_timeout`, the file as one argument-less `Exec`, the `schema_migration` row, `schema_state`.
Lock timeouts (55P03) retry `migrate.ddl_lock_retries` times. Exit codes: 0, 1 error, 2 usage, 3 refusal.
`trove migrate status` takes no lock. Output: JSON log on stderr, JSON summary on stdout.

Not implemented yet: the runner's per-run write of `audit.retention_days` into `audit_policy` and its call of
`audit_maintain_partitions()` (the tables, the functions and the seeded row exist since migration 00006, see
[Access-core tables](#access-core-tables-m1); the write belongs to the audit work, WP1.7, because the runner has no
`audit` configuration), the `database.backup_role` grants and the grants check (no backup tables), the restore mode
of the runner (M5), and the CI check against the latest release's files (no release yet).

### Privileges of the runtime role

Migration 00001 grants `trove_app` (when that role exists) SELECT, INSERT, UPDATE, DELETE on every later table.
Migration 00005 revokes INSERT, UPDATE, DELETE, TRUNCATE on `schema_migration` and `schema_state` again, so a SQL
injection cannot raise `applied` or lower `compat_floor`. Migration 00006 does the same for the audit trail: the
runtime role keeps INSERT and SELECT on `audit_event`, SELECT on `audit_policy` and EXECUTE on
`audit_maintain_partitions()`, and holds nothing on the partitions (Q-OPS-4, R-OPS-015). `system_state` stays writable on purpose: the runtime sets
`deletions_paused_at` and later keys of its own (restore markers are written by restore and migrate). The runtime
preflight (`checkBookkeeping`) refuses a login that can write the bookkeeping once step 5 is applied (a later GRANT
or a restored dump re-opened it), unless it owns the tables or is a superuser. The integration test creates the
cluster-wide role `trove_app` and never drops it.

TLS: `database.ssl_mode: require` without `ssl_root_cert_file` needs `allow_insecure` (it encrypts but accepts any
certificate); `verify-ca` without a CA file is refused (`config.Database.TLSProblem`, used by the load and by
`Open`); `ssl_cert_file` and `ssl_key_file` are set together.

The TLS files are read again for every new connection (R-HA-015, R-ARCH-035), by `dbTLS` in
[connect.go](../../internal/meta/postgres/connect.go): the connection string names no `sslrootcert`, `sslcert` or
`sslkey` (pgx would read them once, at parse time), and `BeforeConnect` re-reads the files through a
`tlsfiles.Reloader` and sets the material on the connection's own TLS configuration and on each fallback of a host
list. `verify-full` is crypto/tls's verification (chain and host name) against the CA pool read for this connection;
`verify-ca` and `require` with a CA file verify the chain against that pool in `VerifyConnection`; the client pair is
presented through `GetClientCertificate`. A file that does not load at `Open` is a configuration error; a file that
breaks later keeps the last good material, is logged and counted (`trove_tls_reload_errors_total{kind="database"}`).
Every pool of a `DB` shares one `dbTLS`; `postgres.WithTLSMetrics` passes the families, see [tls.md](tls.md).

### Compatibility gate

`postgres.Gate(SchemaState, RequiredSchema(), version)`: serves iff `applied >= requiredSchema` and
`compat_floor <= version`; `ErrSchemaBehind` means wait not ready, `*FloorError` means exit with
`schema requires trove >= <floor>`. The version `dev` passes every floor.

## Access-core tables (M1)

Migrations 00006 to 00012 create every table of milestone M1 at once, so the work packages of M1 start from one
schema and their foreign keys resolve. The migrations hold no queries: each work package adds its own
`queries/*.sql`. The columns are the specification's sketches; where the migration differs, a decision (Q-ID) on the
owning spec page says why. Every table is registered in [tables_m1.go](../../internal/meta/postgres/tables_m1.go)
(export class, `CycleBreak`, revision columns). The package that owns a table takes the registration over and deletes
the line there in the same change. The tables of the sealed-secret registry (`internal/secrets`) and the change areas
of `internal/cluster` are not registered by this file.

Rules that hold for all of them:

- **Keys.** Identifiers of identity, authorization and endpoint objects are `uuid` (UUIDv7 from the application, no
  default). Repositories keep their `bigint` identity. A secret is never a column: tokens, sessions, flows and the
  CSRF value are `sha256 bytea` with a `CHECK (octet_length(...) = 32)`.
- **Change areas.** `00007` seeds the `cluster_epoch` row of `auth`, `00010` the row of `repo_config`, `00011` the row
  of `authz`. The Go side (`cluster.RegisterArea`, owner and payload version) is the owning package's. A write that
  changes an input of the credential caches notifies `auth`; a write that changes an input of the grant index
  notifies `authz` or `repo_config`; exposure rules notify `authz` (R-EP-012).
- **Integrity in the database** (Q-AUTH-16). SQLSTATE `AI001` comes from `principal_builtin_guard` (the built-in rows
  `anonymous` and `authenticated`, ids `00000000-0000-0000-0000-000000000001` and `...0002`, cannot be changed or
  deleted) and from `principal_kind_guard` (a membership, token, session, role binding or subtype row names a
  principal of the wrong kind). The API validates first and answers with the specified 4xx; `AI001` means a writer
  skipped that check.
- **Revision columns** (R-API-012): `repositories.config_version`, `authz_policy.version`,
  `endpoint_exposure_rule.revision`, `user_preferences.revision`. Update with
  `SET revision = revision + 1 ... WHERE key = $1 AND ($2::bigint IS NULL OR revision = $2)`.
- **sqlc types.** `timestamptz` generates `pgtype.Timestamptz`, `uuid` `pgtype.UUID`, `inet` `*netip.Addr`, `interval`
  `pgtype.Interval`, `jsonb` `[]byte`. Packages above the infra layer may not import the driver types
  (R-ARCH-002): a store in `internal/meta/postgres` converts, or `sqlc.yaml` gets an `overrides` entry for the column
  (see `blob_gc_queue.enqueued_at`).
- **Cleanup tasks** delete by the indexes the migrations create for them (`*_expires`, `ui_session_ended`,
  `api_token_revoked`, `api_idempotency_keys_expiry`).

| Migration | Area (work package) | Tables |
|---|---|---|
| [00006_audit](../../internal/meta/postgres/migrations/00006_audit.sql) | audit (WP1.7) | `audit_event`, `audit_policy`, `revocation_outbox` |
| [00007_identity](../../internal/meta/postgres/migrations/00007_identity.sql) | identity (WP1.2) | `principal`, `app_user`, `user_group`, `group_membership`, `service_account`, `api_token`, `auth_settings` |
| [00008_login](../../internal/meta/postgres/migrations/00008_login.sql) | login (WP1.1) | `user_identity`, `ui_session`, `oidc_login_flow`, `cli_login`, `auth_lockout` |
| [00009_api](../../internal/meta/postgres/migrations/00009_api.sql) | api (WP1.6) | `api_idempotency_keys`, `user_preferences`, `secret_key_canary` |
| [00010_repositories](../../internal/meta/postgres/migrations/00010_repositories.sql) | repository (WP1.4) | columns of `repositories` |
| [00011_authz](../../internal/meta/postgres/migrations/00011_authz.sql) | authz (WP1.3) | `authz_policy`, `authz_policy_selector`, `authz_policy_pattern`, `authz_policy_entry`, `authz_role_binding` |
| [00012_endpoints](../../internal/meta/postgres/migrations/00012_endpoints.sql) | endpoints (WP1.9) | `endpoint_exposure_rule` |

Files (WP1.5) need no migration: `items`, `item_properties`, `item_download_stats`, `blobs` and the usage counters
of 00002 and 00003 carry R-STOR-007, R-STOR-012, R-STOR-013, R-STOR-016 and R-STOR-018. Jobs (R-OPS-019, R-OPS-020)
are the `job` table of 00004: `created_by` is the starter, `created_at` the enqueue time, `locked_by` the replica.

### 00006 · Audit and revocation journal (WP1.7)

| Table | Purpose | Key columns other areas reference | Class |
|---|---|---|---|
| `audit_event` | Audit events (R-OPS-015, R-OPS-016), range-partitioned by month (UTC) on `occurred_at`; append-only for the runtime role | `PRIMARY KEY (occurred_at, id)`; `id` is the event's UUIDv7 and the `id` of its `revocation_outbox` row; `action`, `category`, `outcome`, `actor_type`, `actor_id`, `token_id`, `repository`, `resource_type`, `resource_id`, `trace_id`, `details jsonb`. No foreign keys: an event outlives what it names | data |
| `audit_policy` | One row (`id = 1`): `retention_days`, owner-written, seeded with 365 | `retention_days` | data |
| `revocation_outbox` | The revocation journal (R-HA-054): written in the transaction of the change, shipped and deleted by a worker job | `id` (= event id), `at` (= `occurred_at`), `event`, `entry jsonb` | data |

Functions (owner-run `SECURITY DEFINER`, fixed `search_path` set by the migration): `audit_maintain_partitions()`
creates the partitions of the current and the next three months and drops those past `retention_days`, serialised
on the `audit_policy` row, `lock_timeout` 2 s, skipping a partition it cannot lock; the runtime role may execute
it. `audit_create_partitions(range_from, range_to)` is the restore's companion and owner-only.
`audit_attach_partition(instant)` is their shared helper. Partitions are named `audit_event_YYYYMM`, built
standalone and attached (`SHARE UPDATE EXCLUSIVE` on the parent only). The migration creates the first four
partitions; replicas and the `audit-retention` task keep them ahead (Q-OPS-38). Tests:
[m1_test.go](../../test/integration/postgres/m1_test.go) (partitions, privileges, retention, the lock timeout under a
long reader).

### 00007 · Identity (WP1.2)

| Table | Purpose | Key columns other areas reference | Class |
|---|---|---|---|
| `principal` | Every principal kind in one table (R-AUTH-016): `user`, `group`, `service_account`, `anonymous`, `authenticated`, `ci_mapping` | `id uuid`, `kind`, `name` (`UNIQUE (kind, name)`), `status`; the two built-in rows are seeded. Every other table that names a principal references `principal (id)` | data (`CycleBreak created_by`) |
| `app_user` | The user record | `principal_id` (PK, kind `user`), `origin`, `is_break_glass` (at most one true), `scim_external_id`, `last_interactive_auth_at`, `disabled_reason` | data |
| `user_group` | The group record | `principal_id` (PK, kind `group`), `auto_join`, `origin` | data |
| `group_membership` | Memberships with a source | `(group_id, member_id, source)`; member is a user or a service account; source `manual`, `auto`, `scim` or `oidc:<providerId>` | data |
| `service_account` | The service account record | `principal_id` (PK, kind `service_account`) | data |
| `api_token` | PAT, SAT and CIT; only `secret_sha256` is stored (R-AUTH-023) | `id`, `type`, `secret_sha256` (unique, 32 bytes), `principal_id`, `scope jsonb`, `expires_at`, `revoked_at`, `revocation_reason`, `rotated_from` and `derived_from` (plain uuid, Q-AUTH-15), `rejection_logged_at`, `expiry_notified_at`, `ci_context` | data |
| `auth_settings` | The runtime authentication settings, one seeded row (R-AUTH-040); NULL means the YAML value | `anonymous_access`, `pat_*_ttl`, `sat_*_ttl`, `expiry_notice` | data |

`service_account_owner` (P1) and `scim` columns beyond `scim_external_id` come with their requirements.

### 00008 · Login state (WP1.1)

| Table | Purpose | Key columns other areas reference | Class |
|---|---|---|---|
| `user_identity` | The (issuer, subject) link of an OIDC user; a NULL `subject` is a pending link | `user_id` (to `app_user`), `provider_id`, `issuer`, `subject`, `UNIQUE (issuer, subject)`, `UNIQUE (user_id, provider_id)` | data |
| `ui_session` | Server-side UI sessions with the SHA-256 of the cookie secret and of the CSRF value | `id`, `secret_sha256`, `principal_id` (kind `user`), `authenticated_at`, `last_seen_at`, `idle_timeout`, `absolute_expires_at`, `ended_at`, `end_reason` | transient |
| `oidc_login_flow` | The state of a browser login; deleted with `DELETE ... RETURNING` on the callback | `state_sha256` (PK), `provider_id`, `browser_binding_sha256`, `code_verifier`, `purpose`, `reauth_session_id`, `expires_at` | transient |
| `cli_login` | Command-line logins (R-AUTH-034, R-AUTH-036; P1, created now for the pending count, Q-AUTH-17) | `id`, `public_id_sha256`, `poll_secret_sha256`, `status`, `decided_by`, `expires_at` | transient |
| `auth_lockout` | The break-glass lockout and rotation hold, the only limiter counted in PostgreSQL (R-AUTH-038); one row `account_key = 'break-glass'` | `failures`, `window_started_at`, `locked_until`, `password_changed_at` | data |

Provider configuration, the break-glass password and the other limiters (credential failures, flow starts, wrong
device codes) have no table: the first two are YAML and mounted files (R-AUTH-004, R-AUTH-014), the limiters are
counters in Valkey with per-replica fallback buckets (R-AUTH-038, Q-AUTH-11).

### 00009 · Management API conventions (WP1.6)

| Table | Purpose | Key columns other areas reference | Class |
|---|---|---|---|
| `api_idempotency_keys` | The stored outcome of an idempotent create (R-API-014), inserted in the transaction of the change | `(principal_id, idem_key)`, `method`, `route`, `request_sha256`, `response_*`, `secret_issued_id`, `expires_at` | transient |
| `user_preferences` | UI preferences of a user (R-API-021) | `user_id` (to `app_user`), `revision` (ETag, registered) | data |
| `secret_key_canary` | The known text sealed under each key ID, for the key-ring status (R-API-049) | `key_id`, `ciphertext` | data |

### 00010 · Repositories (WP1.4)

Columns added to `repositories`: `lifecycle` (`active`, `deleting`), `config_version` (ETag, registered),
`created_by`, `updated_by` (plain uuid), `last_activity_at`, and the constraint `repositories_key_dots`. The row of the
change area `repo_config` is seeded. `claims_version` and `project_id` come later (Q-REPO-11). The authorization
settings of a repository (`cacheFillAction`, `packageOwnership`, `requireAuthentication`, `hideExistence`) stay in
`config` (R-REPO-004).

### 00011 · Authorization (WP1.3)

| Table | Purpose | Key columns other areas reference | Class |
|---|---|---|---|
| `authz_policy` | A permission policy (R-AUTHZ-021) | `id`, `name` (unique), `resource_type`, `version` (ETag, registered) | data |
| `authz_policy_selector` | Repository selectors (R-AUTHZ-004) | `(policy_id, ordinal)`, `kind` (`key`, `pattern`, `class`), `repository_id` (to `repositories`, cascade), `key_pattern`, `repo_class`, `format` | data |
| `authz_policy_pattern` | Package-name and path patterns (R-AUTHZ-005) | `(policy_id, dimension, mode, pattern)` | data |
| `authz_policy_entry` | Grants: a principal and its expanded actions | `(policy_id, principal_id)`, `actions text[]` of the six actions | data |
| `authz_role_binding` | A system role bound to a user, group or service account (R-AUTHZ-022) | `(role, principal_id)`, `granted_by` | data |

There is no epoch table: the authorization epoch is the `authz` row of `cluster_epoch`. The role `admin` of the
break-glass account has no row (R-AUTH-014).

### 00012 · Endpoint exposure (WP1.9)

| Table | Purpose | Key columns other areas reference | Class |
|---|---|---|---|
| `endpoint_exposure_rule` | Exposure rules of an endpoint (R-EP-012); the endpoints themselves are configuration | `id`, `UNIQUE (endpoint, name)`, `repo_id` (to `repositories`, cascade), `enabled`, `packages`, `paths`, `versions`, `prereleases`, `dist_tags`, `revision` (ETag, registered) | data |

## Table registry

`tables.Register(name, class, Options{CycleBreak})` with classes `Data`, `Blobs`, `Transient`, `Schema`.
`postgres.CheckTableRegistry` reads the catalog and reports unregistered tables and foreign-key cycles among
exported tables without a `CycleBreak`. Tables of this package: `tables.go`. Tables created for other packages
by the first migrations: `tables_m0.go`, and by the M1 migrations: `tables_m1.go`; the owning package deletes its line there when it registers the table
itself (a duplicate panics at start-up).

## sqlc

`sqlc.yaml` (schema = migrations, output `q/`), `//go:generate go tool sqlc generate` in `doc.go`, run by
`make generate`. Catalog relations are written `pg_catalog.<name>`. Dynamic list queries belong in `listq/`,
which M1 creates with the first list endpoint.

## Integration tests

`test/integration/postgres/`, tag `integration`, run with the stack up:
`set -a; . .dev/integration/env; set +a; go test -tags integration -race ./test/integration/postgres/...`.
Every test creates its own database `trove_pg_<random>`. `stepsWith` injects extra or edited migration files.
The M1 schema is checked by [m1_test.go](../../test/integration/postgres/m1_test.go) (export classes, seeded rows,
the integrity triggers, the audit partitions and privileges, a schema that is not called `trove`). The PgBouncer tests set role defaults with `ALTER ROLE` in a database of their own, because the pooler keeps
server connections per database. The stack serves TLS only (verify-full from the host);
[test/integration/tls](../../test/integration/tls) rewrites a copy of its CA in place and shows that the next new
connection uses it. Not covered: client-certificate authentication against the real server (the stack requests no
client certificate; the unit tests prove the per-connection reload against a fake server that answers the SSLRequest),
and a real PostgreSQL switchover (E0.13, e2e harness).
