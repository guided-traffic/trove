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
- `q.Pipeline(ctx, tx, steps...)` ([pipeline.go](../../internal/meta/postgres/q/pipeline.go)) runs the statements of several steps
  in one round trip (`store.Eval.Pipeline` for the vulnerability store): a step calls the generated `Queries` methods it is given and
  keeps their results; the statements are recorded in a first pass, sent as one pgx batch on the transaction and answered to a
  second pass of the same steps. A step is therefore a pure function of its arguments, issues the same statements whatever the
  answers were, and none of its statements depends on the result of another step of the same pipeline; a step that issues another
  statement the second time is refused. Use it for independent statements on a path that is measured in round trips (Q-VULN-82); a
  statement that needs an earlier answer is the next pipeline.
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

Files: `internal/meta/postgres/migrations/NNNNN_<name>.sql`, embedded. Numbers run 1..N without a gap. Until the first
stable release there is no backward compatibility ([Q-ARCH-340](../spec/architecture.md#q-arch-340--no-backward-compatibility-until-the-first-stable-release), `CLAUDE.md`): a migration file may be edited, a step
may drop, rename or tighten anything, and nothing has to keep the previous release working against the new schema. The
expand/contract rules of the files are switched off for that (`EnforceMigrationPhases = false` in
[steps.go](../../internal/meta/postgres/steps.go); how to switch them on: [Switched off until the first stable release](releasing.md#switched-off-until-the-first-stable-release)), and so is the
CI check that no migration on `main` changes (the job `compat`). A file may start with header comments, each of them optional
while the rules are off:

```sql
-- trove:phase contract          (or expand; at most one; a file without it is an expand step)
-- trove:no-transaction          (one statement, e.g. CREATE INDEX CONCURRENTLY; must be idempotent)
-- trove:requires-app >= 0.4.0   (contract only, required there)
-- trove:requires-background <name>   (contract only, optional, once per background migration it depends on)
```

`ParseSteps` (unit-tested in [steps_test.go](../../internal/meta/postgres/steps_test.go)) applies the switch. In both modes it
rejects an unknown or a second phase, a contract step without `requires-app`, `requires-app` or `requires-background` on a
step that is not `-- trove:phase contract`, a `requires-background` that names no single migration or names one twice,
an unknown `trove:` header, a `no-transaction` file with more than one statement and a file without a statement. With the rules
on it also rejects a file without a phase header and `DROP`, `RENAME` or `SET NOT NULL` in an expand file, and
`RequiredSchema()` is the newest expand step; with them off, a file without a phase header is an expand step, the statements
are not looked at by phase, and `RequiredSchema()` is the highest embedded number, so a replica serves only once every
migration of its binary is applied. `TestParseSteps` runs the strict rules and `TestParseStepsRelaxed` the relaxed ones,
whatever the switch says, so neither rots. A contract step is opt-in while the rules are off: the runner refuses it while a
live replica runs a release below its `requires-app` (the contract guard), and once applied it raises `compat_floor`.

### How to add a migration

1. Create the next number. A header is needed only for `no-transaction` or a contract step. Unqualified names: the runner
   sets `search_path`.
2. New table: register its export class in the owning package (`tables.Register`); a `bigint` revision column
   (`rev_seq`, `revision`, `epoch`, `*_version`) needs `meta.RegisterRevisionColumn`. The integration tests
   `TestTableRegistry` and `TestRevisionColumns` fail otherwise.
3. A table with a blob reference: column `blob_sha256`, FK to `blobs`, index, both triggers
   (`TestBlobReferenceRules`).
4. Backfills are background data migrations (R-HA-022), not migration steps; the first is `data_migration_nuget_semver2`
   ([nuget.md](nuget.md#the-recomputation-of-is_semver2-q-nuget-93)).
5. A step may drop, rename or tighten what it needs; nothing has to stay for the previous release. Mark it
   `-- trove:phase contract` with `requires-app` only when the runner shall refuse it while older replicas are live.
   A contract step that removes what a background data migration converts names it with `requires-background <name>`; the
   guard refuses the step until the migration has succeeded ([Background data migrations](#background-data-migrations), Q-HA-115).
6. Static queries for the new tables go to `queries/*.sql`, then `make generate`.
   A test that injects a migration of its own names it with `nextStep(n, name)`
   ([env_test.go](../../test/integration/postgres/env_test.go)), which numbers the file after the embedded ones, so
   it needs no edit when a milestone adds migrations.
7. `trove migrate up` checks the migrated schema against the table registry of its own binary
   (`postgres.VerifyTableRegistry`, exit 3 naming the table); [cmd/trove/registry.go](../../cmd/trove/registry.go)
   links every package that owns tables. A new owning package is added there.

### How to fix a migration that is on `main`

Until the first stable release, fix the file itself ([Q-ARCH-340](../spec/architecture.md#q-arch-340--no-backward-compatibility-until-the-first-stable-release)). The CI checks that compare the migration files
with a release and with the history of `main` are switched off with the job `compat`; `make compat-check` would still report
the edit.

1. Edit the file so that it creates what the schema should be. Removing or renumbering a file renumbers every file after it:
   the numbers must still run 1..N without a gap.
2. Run `make generate` (sqlc reads the migration files as its schema) and the integration tier, which migrates an empty
   database.
3. A database that applied the earlier version of the file is refused by the runner's checksum check (`verifyApplied`:
   `migration <name>.sql was changed after this database applied it (checksum mismatch)`, exit status 1, nothing applied), so
   the migration Job fails and `helm upgrade` stops before the Deployments change. Recreate that database; the rule adds no
   repair path (`CLAUDE.md`). A replica does not compare checksums: started on such a database by other means
   (`migrations.mode: disabled`), it serves once `schema_state.applied` reaches its `RequiredSchema()`, whatever version of the
   file the database applied.

From the first stable release on, compatibility comes first again; with the job `compat` switched on, a migration on `main`
is then fixed forward with a new file, as
[Q-ARCH-283](../spec/architecture.md#q-arch-283--is-a-migration-on-main-immutable-and-what-does-the-check-hold-to) decided
before Q-ARCH-340 switched it off.

`sqlc` reads the migration files as its schema. `job_state` is created inside a `DO` block on purpose: sqlc
then maps it to a Go string instead of generating enum helpers that import `database/sql/driver` (forbidden,
R-ARCH-026).

### Runner (`postgres.Migrate`, `trove migrate up`)

Preflight, schema creation if missing, `pg_try_advisory_lock(3, 0)` polled for up to `migrate.advisory_lock_wait`
(the timeout names the holder from `pg_stat_activity`), refusal of a half-restored database (exit 3), checksum
check, plan (out-of-order steps fail), contract guard (exit 3 when a live replica is below `requires-app`;
live means a `replica.heartbeat_at` within `system.heartbeat_stale_after`; also exit 3 while a background migration the step
names in `requires-background` has not succeeded), then per step one transaction:
`SET LOCAL lock_timeout`, the file as one argument-less `Exec`, the `schema_migration` row, `schema_state`.
Lock timeouts (55P03) retry `migrate.ddl_lock_retries` times. Exit codes: 0, 1 error, 2 usage, 3 refusal.
`trove migrate status` takes no lock. Output: JSON log on stderr, JSON summary on stdout.

### The migration report (`postgres.Report`, `trove migrate status`, `GET /api/v1/system/migrations`)

`postgres.Report(ctx, db, opts)` ([migrate.go](../../internal/meta/postgres/migrate.go)) reads `schema_state`, the applied
steps, the steps the binary embeds that are not applied (each with `requiresApp` and `requiresBackground`, what the guard
checks), the background migrations of `MigrateOptions.Background` with their states, and the gate's verdict. It reads only and
takes no lock, through any `q.DBTX`: `trove migrate status` (`postgres.Status`) runs it on its session connection as the schema
owner, and the `api` role runs it on its pool as the runtime role for `GET /api/v1/system/migrations` (`migrationReport` in
[migration_report.go](../../internal/app/migration_report.go); the runtime role reads `schema_migration` and `schema_state`,
R-HA-018). One code answers both, so the endpoint and the command cannot disagree (R-HA-018, R-HA-020, R-HA-021 step 4).
The endpoint leaves out `servable` and `verdict`: a replica that answers passed the gate.

### Background data migrations

A backfill runs as the job kind `data_migration_<name>` on a worker (R-HA-022). R-HA-022's table `background_migration` and its
pause and resume operations are not built; until they are, a migration is declared by the package that runs it as a
`postgres.BackgroundMigration` ([background.go](../../internal/meta/postgres/background.go)): its name, the release that
introduced it and the `system_state` key that records its completion (Q-HA-114). `postgres.ReadBackground` derives the state:

| Records | State |
|---|---|
| the completion key exists | `succeeded` |
| the newest job of the kind is `running` or `cancelling` | `running` |
| the newest job is `failed` | `failed` |
| no job, a `queued` one, a `cancelled` one, or a run that ended without the key (it waited for older replicas) | `pending` |

`processed` and `estimatedTotal` are the newest job's progress (an estimate of 0 is none); a completed migration's count can come
from its completion record (`BackgroundMigration.Processed`), which outlives the job rows. `startedAt` is the first start of
any run of the kind (`BackgroundMigrationJob` in [migration.sql](../../internal/meta/postgres/queries/migration.sql)).

`app.BackgroundMigrations()` ([migration_report.go](../../internal/app/migration_report.go)) lists the declarations of the binary;
`trove migrate` passes them to the guard and the report, the `api` role to the report. Today it holds one, `nuget_semver2`
(`nugetdata.Semver2Background`, [nuget.md](nuget.md#the-recomputation-of-is_semver2-q-nuget-93)). How to add one:

1. Run it as a job kind `data_migration_<name>` that records its completion in a `system_state` key (as
   [semver2.go](../../internal/formatdata/nuget/semver2.go) does), and start it only once every live replica runs its release
   (`postgres.ReplicasUpTo`).
2. Declare it next to the job (`Name`, `Release`, `DoneKey`, optionally `Processed`) and add it to `app.BackgroundMigrations()`.
   `TestBackgroundMigrations` ([migration_report_test.go](../../internal/app/migration_report_test.go)) checks that the
   declarations are valid and that every `requires-background` of the embedded steps names one of them.
3. The contract step that removes the old form, one minor later, carries `-- trove:requires-background <name>`.

The runner also writes `audit.retention_days` into `audit_policy` (when it differs) and calls `audit_maintain_partitions()` after the
steps, also when none was pending (`MigrateOptions.AuditRetentionDays`, [audit.go](../../internal/meta/postgres/audit.go); Q-OPS-39,
[audit.md](audit.md)). `trove migrate` passes the configured value; a caller that leaves the option nil (a test with steps of its own) leaves
the row alone.

Last, still under the migration lock and on the same connection, the runner calls `MigrateOptions.Declared` with a
`MigrationSession` ([migrate_session.go](../../internal/meta/postgres/migrate_session.go)), also when no step was pending:
`trove migrate` passes `app.DeclaredState`, which applies the role bindings of `authz.role_bindings` in one transaction
(R-AUTHZ-010, [authorization.md](authorization.md#role-bindings-from-the-configuration)). Its error fails the run with exit
status 1; nil skips it.

Not implemented yet: the `database.backup_role` grants and the grants check (no backup tables), and the restore mode of the runner (M5).
The CI checks of the migration files, against the latest release and against the history of `main`, are in place and
switched off until the first stable release with the job `compat` ([Compatibility](releasing.md#compatibility), Q-ARCH-340);
`make compat-check` runs them by hand.

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

Migrations 00006 to 00012 create every table of milestone M1 at once (00013 adds the revision columns of the identity resources), so the work packages of M1 start from one
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
| [00009_api](../../internal/meta/postgres/migrations/00009_api.sql) | api (WP1.6) | `api_idempotency_keys`, `user_preferences`, `secret_key_canary`; `job.ran_by` and two job indexes |
| [00010_repositories](../../internal/meta/postgres/migrations/00010_repositories.sql) | repository (WP1.4) | columns of `repositories` |
| [00011_authz](../../internal/meta/postgres/migrations/00011_authz.sql) | authz (WP1.3) | `authz_policy`, `authz_policy_selector`, `authz_policy_pattern`, `authz_policy_entry`, `authz_role_binding` |
| [00012_endpoints](../../internal/meta/postgres/migrations/00012_endpoints.sql) | endpoints (WP1.9) | `endpoint_exposure_rule` |
| [00013_identity_revisions](../../internal/meta/postgres/migrations/00013_identity_revisions.sql) | identity (Q-AUTH-50) | `principal.revision` and `auth_settings.revision` (registered revision columns, R-BAK-022) and the triggers that bump a principal's revision when a member of its representation changes |

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
| `user_group` | The group record | `principal_id` (PK, kind `group`), `auto_join`, `sync_detached` (set by the replay of a rename, cleared by `PATCH` with `false`; migration 00052), `origin` | data |
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

Beyond the tables, 00009 adds to `job` the column `ran_by` (uuid, no foreign key: the replica row may be pruned) and the
indexes `job_created (created_at, id)` and `job_kind_created (kind, created_at, id)`. `ClaimJobs` copies `locked_by` into
`ran_by`, so a finished job still says which replica ran it (Q-OPS-46); the job list sorts by the two indexes
(R-API-010). Each replica seeds the `secret_key_canary` row of every key ID of its ring at start-up and opens the rows of
the IDs it holds (`verifyKeyRingCanaries`, Q-ARCH-39). The query file is
[api.sql](../../internal/meta/postgres/queries/api.sql); the one statement that cannot be fixed SQL, the count of sealed
values per key ID over the registered columns, is in [listq](../../internal/meta/postgres/listq/sealed.go), the confined
builder the architecture test allows to assemble SQL text.

### 00010 · Repositories (WP1.4)

Columns added to `repositories`: `lifecycle` (`active`, `deleting`), `config_version` (ETag, registered),
`created_by`, `updated_by` (plain uuid), `last_activity_at`, and the constraint `repositories_key_dots`. The row of the
change area `repo_config` is seeded. `claims_version` is added by 00015 (M2) and `project_id` by 00019 (projects, Q-REPO-11). The authorization
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

## M2 tables

Migrations 00014 to 00016 create every table of milestone M2 at once, before any query that uses them, so that
the generated `q/models.go` changes in one place and the later work packages add `queries/<area>.sql` only
(Q-ARCH-190). All three are `-- trove:phase expand` files without a backfill, a `no-transaction` header or a
`requires-app`: a v0.3.x replica never reads the new tables and keeps serving against them, and the M2 binary's
`RequiredSchema()` is 16. Every table is registered as data in
[tables_m2.go](../../internal/meta/postgres/tables_m2.go); the package that owns a table takes the line over in the same
change. `repositories.claims_version` is registered there with `meta.RegisterRevisionColumn`, and the sealed column
`repository_secrets.sealed` in [secret_columns.go](../../internal/repository/secret_columns.go). The change areas
`claims`, `cache_invalidation`, `upstream_state` and `metadata_changed` are declared in
[areas.go](../../internal/cluster/areas.go) between the `>>> M2 schema` markers (Q-ARCH-194).

| Migration | Area (work package) | Tables and columns | Requirements |
|---|---|---|---|
| [00014_remote_proxy](../../internal/meta/postgres/migrations/00014_remote_proxy.sql) | remote engine (WP2.2) | `remote_cache_entries`, `remote_url_map`, `remote_upstream_state`, `remote_cache_invalidations`, `repository_secrets`; seeds the `cluster_epoch` rows of `cache_invalidation` and `upstream_state` | R-REPO-025, R-REPO-029, R-REPO-033, R-REPO-043, R-REPO-045 |
| [00015_virtual_claims_ownership](../../internal/meta/postgres/migrations/00015_virtual_claims_ownership.sql) | virtual engine, ownership (WP2.3, WP-OWN) | `repositories.claims_version`, `virtual_members`, `virtual_deploy_targets`, `namespace_claims`, `virtual_pin_rules`, `virtual_pin_members`, `package_ownership`, `package_owner`; seeds the row of `claims` | R-REPO-017, R-REPO-021, R-REPO-023, R-AUTHZ-018 |
| [00016_npm](../../internal/meta/postgres/migrations/00016_npm.sql) | npm (WP2.4, WP2.5) | `npm_packages`, `npm_versions`, `npm_tombstones`, `npm_remote_packages`, `npm_remote_versions` | R-NPM-006, R-NPM-008, R-NPM-024 |

Where a migration differs from the specification's sketch, a decision says why: the surrogate `id` of
`remote_cache_entries` (Q-REPO-86), `remote_cache_invalidations.folded_at` (Q-REPO-87), the omitted `search_vector`
and the unconstrained `package_owner.ci_issuer_id` (Q-ARCH-191). The other additions are `CHECK`s only: non-negative
positions and counters, the length of `last_failure`, `deprecated` and `readme`, the JSON-pointer form of
`repository_secrets.field` and the `domain` form of `package_ownership`.

Rules that hold for them:

- **Blob references.** `remote_cache_entries.blob_sha256` follows the reference rule of R-STOR-003: indexed, guard
  trigger, delete and update release triggers. The update trigger joins on `id`, which is why the column exists. A
  refreshed metadata document that replaces the blob therefore queues the old blob for collection, and so does the
  delete of an entry or of its repository. `npm_remote_packages.doc_sha256` is a digest, not a reference: no foreign
  key to `blobs`.
- **What cascades and what restricts.** Every table that names a repository by `repo_id` or `virtual_id` cascades on its
  delete. A repository that is a member (`virtual_members.member_id`), a deploy target (`virtual_deploy_targets.target_id`)
  or a pin member (`virtual_pin_members.member_id`) cannot be deleted while it is listed: the foreign key is
  `ON DELETE RESTRICT` and the error is SQLSTATE `23503`. The service validates first and answers 409 (R-REPO-013); the
  database is the backstop for a concurrent change.
- **Tombstones survive.** `npm_tombstones` references only `repositories`: the version row, the item and the package row
  may go (unpublish, permanent removal, trash retention) and the tombstone stays (R-NPM-008).
- **One look-alike per repository.** `UNIQUE (repo_id, name_fold)` on `npm_packages` is what decides the race of two
  concurrent first publishes of `acme-ui` and `acme_ui` (R-NPM-007): the loser's insert fails with `23505`.
- **One claim, one owner entry.** The primary key of `package_ownership` decides the race of two first publishes of a
  name: the claim is an `INSERT … ON CONFLICT DO NOTHING`, and the loser's insert waits for the winner and returns no row
  (R-AUTHZ-018 rule 5, [authorization.md](authorization.md#package-ownership)). `package_owner_unique` keeps one entry per
  principal or CI project and name (an owner added twice is 409 `already_exists`). `ci_issuer_id` has no foreign key until
  M4 adds it `NOT VALID` and validates it (Q-ARCH-191).
- **Ownership of a deleted repository.** `package_ownership.domain = 'repo:<id>'` carries no foreign key: the hook
  `authz.OwnershipHook` removes the rows in the transaction of the deletion job that removes the repository row
  (R-AUTHZ-018, Q-AUTHZ-156). Repository ids come from an identity column and are never reused.
- **Revision columns.** `repositories.claims_version` is the ETag of the claims resource (R-REPO-023). The claims service takes
  the row lock (`LockRepositoryByKey`), compares the version and bumps it with `BumpClaimsVersion` in the same transaction;
  the auto claim of a first publish bumps it with the same statement ([virtual.md](virtual.md)).

A package that finds that it needs another column or constraint asks for a follow-up expand step (`00017`) and never
edits a migration of another work package. Query files by owner: `proxy.sql`, `invalidation.sql`, `remote_settings.sql`,
`virtual.sql`, `claims.sql`, `ownership.sql`, `packages.sql`, `npm_local.sql`, `npm_remote.sql`.

## Table registry

`tables.Register(name, class, Options{CycleBreak})` with classes `Data`, `Blobs`, `Transient`, `Schema`.
`postgres.CheckTableRegistry` reads the catalog and reports unregistered tables and foreign-key cycles among
exported tables without a `CycleBreak`. Tables of this package: `tables.go`. Tables created for other packages
by the first migrations: `tables_m0.go`, by the M1 migrations: `tables_m1.go` and by the M2 migrations: `tables_m2.go`; the owning package deletes its line there when it registers the table
itself (a duplicate panics at start-up). The tables of the M3 migration are registered by their owner, the NuGet data port
([tables.go](../../internal/formatdata/nuget/tables.go)); `cmd/trove/registry.go` links that package so that `trove migrate` checks the
migrated schema against a registry that holds them, and the integration tests that compare the schema with the registry import it
(`test/integration/postgres/owners_test.go`).

## OCI tables, upload sessions and the key grammar

Five migrations of phase T-16 ([oci.md](oci.md)). [00054_upload_sessions](../../internal/meta/postgres/migrations/00054_upload_sessions.sql) and
[00055_oci](../../internal/meta/postgres/migrations/00055_oci.sql) are expand steps that create tables no earlier release knows; [00057_upload_session_tails](../../internal/meta/postgres/migrations/00057_upload_session_tails.sql) is a named contract step that replaces the two tail columns of 00054, which is in no release, by the table of segments and adds `upload_sessions.item_id` (Q-STOR-174); [00058_upload_session_indexes](../../internal/meta/postgres/migrations/00058_upload_session_indexes.sql) is a contract step as well, replacing the cleanup index of 00054 by one on `(state, last_activity_at)` and adding the index on `(repo_id, principal_id)` that the cap on open sessions counts with (Q-STOR-176):

| Table | Export class | Registered in | Meaning |
|---|---|---|---|
| `upload_sessions`, `upload_session_parts`, `upload_session_tails` | `transient` | [tables_uploads.go](../../internal/meta/postgres/tables_uploads.go) | The multi-request upload sessions of R-STOR-011 with the repository written to (`repo_id`), the repository the client addressed (`addressed_repo_id`) and the format's `binding` (the OCI image name); the statements are [uploads.sql](../../internal/meta/postgres/queries/uploads.sql) (run by [internal/uploads](../../internal/uploads/) and the session step of the collection run, [gc_uploads.go](../../internal/blob/gc_uploads.go)); the lease of a claim and the age of a session are the database's `now()`, see [oci.md](oci.md#upload-sessions-r-stor-011) |
| `oci_manifests`, `oci_references` | `data` | [tables.go](../../internal/formatdata/oci/tables.go) | A row per manifest of an image name and the references of a manifest to manifests and blobs of the same name (R-OCI-007); no blob column, so they never hold a blob against garbage collection |

[00056_key_grammar](../../internal/meta/postgres/migrations/00056_key_grammar.sql) is a **contract** step (`requires-app >= 0.1.0`, so the compatibility floor does not move: pre-alpha, no replica of an older release has to keep serving): it
replaces the checks `repositories_key_check` and `project_key_check` (and drops `repositories_key_dots`, which the grammar says by itself)
with the OCI component grammar for every format (Q-OCI-3) and validates them in the same step. A key the earlier grammar allowed and this one
refuses (`docker-`, `a.-b`, `web-`) stops the migration; the operator renames or removes that repository or project first. The checks are
added `NOT VALID` and validated with `VALIDATE CONSTRAINT`, which lets reads and writes through. The statements the data port reads are in
[oci.sql](../../internal/meta/postgres/queries/oci.sql); `test/integration/postgres/oci_test.go` pins the tables, the phases and both grammars at the database.

## Debian tables (00060 to 00063)

Four expand migrations of phase T-17 ([deb.md](deb.md)); the numbers are the next free ones, because the runner needs numbers without a gap.
Every table is export class `data`, registered in [tables_deb.go](../../internal/formatdata/deb/tables_deb.go).

| Table | Migration | What it holds |
|---|---|---|
| `deb_suites`, `deb_binaries`, `deb_coordinates`, `deb_generations`, `deb_tombstones` | [00060_deb](../../internal/meta/postgres/migrations/00060_deb.sql) | The suites of a local or virtual repository with `change_seq` and `built_seq` (revision columns), a row per stored package file with its normalized control paragraph, the suites and components a file is listed in, the published and retained generations, and the marks of package files the journal replay withheld (R-DEB-008, R-DEB-032). No blob column: the items hold the blobs |
| `deb_signing_keys` | [00061_deb_signing_keys](../../internal/meta/postgres/migrations/00061_deb_signing_keys.sql) | The signing keys of a repository: one `primary` and one `next` at most (partial unique indexes), the fingerprint unique in every state, `private_key` sealed with the stored-secret key ring (the sealed column `formatdata/deb.SigningKeys`, row key `id`) and NULL exactly in state `retired` (R-DEB-015) |
| `deb_remote_releases`, `deb_remote_indexes`, `deb_remote_entries` | [00062_deb_remote](../../internal/meta/postgres/migrations/00062_deb_remote.sql) | A remote's stored release documents with the verdict of their verification, its parsed indexes and their entries; cache rows (R-DEB-023). `GetLiveItem` reads the digest of `deb_remote_entries` for the kind `deb_remote` and the mark of `deb_tombstones` for `deb_local` ([files.sql](../../internal/meta/postgres/queries/files.sql)) |
| `deb_binaries` (constraint) | [00063_deb_binaries_detached](../../internal/meta/postgres/migrations/00063_deb_binaries_detached.sql) | Drops the foreign key from `deb_binaries.item_id` to `items`: the item store deletes the item rows of a permanent removal before the format's hook runs, and the hook needs the file's row to raise the suites and journal the deletion; it deletes the row itself |

`test/integration/postgres/deb_test.go` pins the tables, the phases, the constraints and the item lookup's facts.

## Session withdrawal (00059)

[00059_session_withdrawal](../../internal/meta/postgres/migrations/00059_session_withdrawal.sql) is an expand step that adds
`ui_session.withdrawn_at` and `ui_session.successor_id` with a partial index on the successor (Q-AUTHZ-262). The withdrawal statements
of [login.sql](../../internal/meta/postgres/queries/login.sql) and `IdentityEndSessions` mark the live sessions they end, and the
`WithdrawEnded…` statements that follow them mark the ended ones; `EndSessionReplaced` and `EndSessionBySecret` name the successor of a
re-authentication and of a sign-in in the same browser; only `SessionStandingChain`, the delete job's read, reads either column. The table stays `transient`; a restore ends and withdraws every session.

## M3 tables

Migration [00017_nuget](../../internal/meta/postgres/migrations/00017_nuget.sql) creates every table of the NuGet format
at once, as an expand file without a backfill, a `no-transaction` header or a `requires-app`; a release that does not
know NuGet never reads the tables. The M3 binary's `RequiredSchema()` is 17. Every table is registered as data in
[tables.go](../../internal/formatdata/nuget/tables.go) of the package that owns it. The statements the data port needs at minimum are in
[nuget_local.sql](../../internal/meta/postgres/queries/nuget_local.sql); the values that cross the port are in
[rows](../../internal/formats/nuget/rows/rows.go), a leaf by the rule of R-ARCH-002 (`internal/formats/<id>/rows` imports
only `errs` and the standard library; `archtest.CheckLayers` enforces it).

| Migration | Area (work package) | Tables | Requirements |
|---|---|---|---|
| [00017_nuget](../../internal/meta/postgres/migrations/00017_nuget.sql) | NuGet (WP3.S) | `nuget_version` (with the `search_doc` GIN index and the latest-version index), `nuget_tombstone`, `nuget_symbol_package`, `nuget_symbol_file` (P1) | R-NUGET-005, R-NUGET-006, R-NUGET-020, R-NUGET-021, R-NUGET-033 |
| [00018_role_binding_source](../../internal/meta/postgres/migrations/00018_role_binding_source.sql) | authorization | `authz_role_binding.source` (`api` by default, `configuration` for `authz.role_bindings`), an expand step; a binary of the previous release inserts `api` | R-AUTHZ-010, Q-AUTHZ-170 |
| [00019_projects](../../internal/meta/postgres/migrations/00019_projects.sql) to [00025_projects_validate](../../internal/meta/postgres/migrations/00025_projects_validate.sql) | projects (phase 5a) | `project`, `project_admin`, `authz_project_policy_entry`; `project_id` on `repositories`, `service_account` and `authz_policy`; five guard triggers (seven steps, see below) | R-AUTHZ-026, R-HA-018, Q-AUTHZ-174 to Q-AUTHZ-176, Q-AUTHZ-203 |

There is no trigram index and no `CREATE EXTENSION` (Q-NUGET-12). `search_doc` is built by `NuGetInsertVersion` with the
`simple` configuration (Q-NUGET-18). The checks and indexes beyond the specification's sketch are Q-NUGET-19. The
journal of Q-NUGET-10 is a set of objects, not a table.

## sqlc

`sqlc.yaml` (schema = migrations, output `q/`), `//go:generate go tool sqlc generate` in `doc.go`, run by
`make generate`. Catalog relations are written `pg_catalog.<name>`. Dynamic list queries belong in `listq/`,
which M1 creates with the first list endpoint.

Overrides: `jsonb` generates `json.RawMessage` (parameters, columns and rows), not `[]byte`. Behind a transaction-mode pooler the main pool
runs in pgx's exec mode, which sends no parameter types and encodes a `[]byte` as `bytea` text; a `$n::jsonb` cast then fails with
`22P02` (Q-ARCH-348). A new `jsonb` query needs nothing; a hand-written statement passes `json.RawMessage` or a string cast through
`::text::jsonb`. `TestTransactionPooler` in `test/integration/postgres` runs generated `jsonb` queries through PgBouncer in the default
integration run.

## Integration tests

`test/integration/postgres/`, tag `integration`, run with the stack up:
`set -a; . .dev/integration/env; set +a; go test -tags integration -race ./test/integration/postgres/...`.
Every test creates its own database `trove_pg_<random>`. `stepsWith` injects extra or edited migration files.
The M1 schema is checked by [m1_test.go](../../test/integration/postgres/m1_test.go) (export classes, seeded rows,
the integrity triggers, the audit partitions and privileges, a schema that is not called `trove`), the M2 schema by
[m2_test.go](../../test/integration/postgres/m2_test.go) (export classes, the expand-only steps, the seeded and registered
areas, the revision registration, and the constraints named in the section above, each under its requirement ID). The PgBouncer tests set role defaults with `ALTER ROLE` in a database of their own, because the pooler keeps
server connections per database. The stack serves TLS only (verify-full from the host);
[test/integration/tls](../../test/integration/tls) rewrites a copy of its CA in place and shows that the next new
connection uses it. Not covered: client-certificate authentication against the real server (the stack requests no
client certificate; the unit tests prove the per-connection reload against a fake server that answers the SSLRequest),
and a real PostgreSQL switchover (E0.13, e2e harness).

## CI tables (00026 to 00031)

Expand steps of beta phase 5 ([ci-oidc.md](ci-oidc.md)): 00026 creates `ci_issuers`, `ci_issuer_keys` (transient in backups), `ci_mappings` (tier `system`, `project:<key>` with
`project_id`, or `delegation:<uuid>`; the principal of kind `ci_mapping`), `ci_subject_token_uses` (the replay set) and `ci_token_issuances` (no key on `token_id`, issuer
`ON DELETE CASCADE`, Q-CI-20), seeds the epoch rows of `ci_mappings` and `ci_issuer_keys`, and adds `package_owner_ci_issuer_id_fkey` `NOT VALID`; 00027 builds its index
`CONCURRENTLY`; 00028 validates it. 00029 to 00031 add the indexes of the issuance statements (Q-CI-53), each a single `CREATE INDEX CONCURRENTLY` in its own no-transaction step (R-HA-018): `ci_issuances_issued` (the list without a filter), `ci_issuances_issuer_issued` (the list of one issuer) and `ci_issuances_contributions` (a GIN on `grants -> 'contributions'` for a project's list). There is no index on `audit_event.token_id`: the table is partitioned, `CREATE INDEX CONCURRENTLY` is refused on a partitioned table, and the usage summary reads a window of the partitions' primary key. The plans are checked on a seeded table by [ci_issuance_plans_test.go](../../test/integration/postgres/ci_issuance_plans_test.go). These steps add indexes only, so nothing is registered in [tables_ci.go](../../internal/meta/postgres/tables_ci.go) and no export class changes.

## Vulnerability tables (00035, 00036, 00037, 00041, 00042)

Expand steps of beta phase 6 ([vulnerabilities.md](vulnerabilities.md)): 00035 creates the eight tables of the specification's data models (`vuln_source_state`, `vuln_advisory` with
the added column `cwe_ids`, `vuln_affected`, `vuln_pending_evaluation`, `vuln_kev`, `vuln_claimed_match`, `vuln_finding`, `vuln_finding_event`) and seeds the epoch row of the area
`vuln_data`; 00036 builds `packages_name` on `packages (name)` `CONCURRENTLY` as a step of its own (Q-VULN-24); 00037 adds the indexes of `vuln_finding` that the foreign keys that are `ON DELETE SET NULL` and the retention task need (`version_id`, `package_id`, a partial `resolved_at`) in one transactional step (Q-VULN-64). 00041 sets `fastupdate = off` on the GIN index `vuln_advisory_aliases` and merges its pending list, so that the closure's lookups during an import do not read the list (Q-VULN-91). 00042 sets the statistics target of the column `ecosystem` of `vuln_advisory`, `vuln_affected`, `vuln_sync_touched` and `vuln_pending_evaluation` to 0: a sample taken while only NuGet was stored estimated `ecosystem = 'npm'` at one row and made the planner pick a scan of the whole ecosystem per row (Q-VULN-97; F-57). `vuln_finding` and `vuln_finding_event` are data in backups, the other six
tables transient, and `vuln_finding.revision` is a revision column ([tables_vuln.go](../../internal/meta/postgres/tables_vuln.go)). The queries are in one file per work package
(`vuln_sync.sql`, `vuln_evaluate.sql`, `vuln_enforce.sql`, `vuln_audits.sql`, `vuln_api.sql`, `vuln_ops.sql`). Tested by
[vuln_test.go](../../test/integration/postgres/vuln_test.go).

## The UI's shared asset store (00043)

An expand step of F-58 ([web-ui.md](web-ui.md#the-shared-asset-store), R-HA-020, Q-HA-120 to Q-HA-123):
[00043_ui_assets](../../internal/meta/postgres/migrations/00043_ui_assets.sql) creates `ui_assets`, one row per hashed file of a UI
build that a replica published: the primary key `(locale, path)`, `blob_sha256` with the reference rule of R-STOR-003 (indexed, guard
trigger, delete and update release triggers, the surrogate `id` for the update trigger), `size`, `published_by` (the publisher's
version and commit), `published_at` and `seen_at`. CHECKs keep the locale to the locale-code form and the path to `[A-Za-z0-9_./-]`
without a dot segment; the hashed pattern itself is the code's (`webui.IsHashed`). The statements compare a row's `blob_sha256` with the
replica's own digest: `seen_at` is renewed only by a replica whose build has the row's bytes (`TouchUIAssets`, rows renewed in the last
half hour are left alone), and `InsertUIAsset` replaces a row with other bytes only when its blob is not live and `ok` or it was not
renewed for 2 hours (Q-UI-142), and `PruneUIAssets` deletes the rows not renewed for 7 days, which queues their blobs for the collector. The table is transient in
backups ([tables_ui.go](../../internal/meta/postgres/tables_ui.go)): a restore leaves it empty and the replicas publish again. The
statements are in [ui_assets.sql](../../internal/meta/postgres/queries/ui_assets.sql); a list of files travels as one array of
`<locale>/<path>` names, because sqlc has no multi-array `unnest`. Tested by
[uiassets_test.go](../../test/integration/uiassets/uiassets_test.go) and `TestBlobReferenceRules`.

## Migrations of the Bastion (Q-BAS-48)

Beta phase 5b ([bastion.md](bastion.md)) adds three steps. A registration is a principal of the kind `bastion` plus a row of `bastion`,
the way a service account is a principal plus a `service_account` row; `principal.revision` is its ETag.

| Step | Class | Content |
|---|---|---|
| [00032_bastion](../../internal/meta/postgres/migrations/00032_bastion.sql) | expand | the tables `bastion` (kind guard `AI001`, a revision trigger on the description) and `bastion_contact` (last contact per registration and replica, R-BAS-010; transient in backups); the check `principal_bastion_name` (`^[a-z][a-z0-9-]{0,19}$`, added `NOT VALID`); the `api_token_kind` guard admits the kind |
| [00033_bastion_kinds](../../internal/meta/postgres/migrations/00033_bastion_kinds.sql) | **contract**, `requires-app >= 0.1.0` | replaces the checks `principal_kind_check` (adds `bastion`) and `api_token_type_check` (adds `bst`), `NOT VALID`. The step only loosens, so no release is made unable to serve and the floor of a fresh database is `0.1.0`; it is a contract step because the rules classify every `DROP` of a constraint there |
| [00034_bastion_validate](../../internal/meta/postgres/migrations/00034_bastion_validate.sql) | expand | `VALIDATE CONSTRAINT` of the three checks |

The guards of `authz_role_binding` and `group_membership` name the kinds they admit and `bastion` is none of them (`AI001`). The
feed needs no new index: it reads `audit_event` by `(occurred_at, id)`, the primary key, within a window of minutes (an index on the
partitioned table cannot be built `CONCURRENTLY`). The queries are in three files of their own, so the work packages never edit a
shared one: [bastion_registration.sql](../../internal/meta/postgres/queries/bastion_registration.sql) (package 1),
[bastion_contact.sql](../../internal/meta/postgres/queries/bastion_contact.sql) (package 2 writes, 1 and 8 read) and
[bastion_feed.sql](../../internal/meta/postgres/queries/bastion_feed.sql) (package 2). Registrations of the export classes are in
[tables_bastion.go](../../internal/meta/postgres/tables_bastion.go). Tested by
[bastion_test.go](../../test/integration/postgres/bastion_test.go).

## Projects tables (00019 to 00025)

Migrations 00019 to 00025 hold the project model (R-AUTHZ-026, Q-AUTHZ-206 to Q-AUTHZ-209, Q-REPO-212), edited in place before the
first stable release; they keep the shape of expand steps that R-HA-018 asks of a step that touches tables that already exist
(Q-AUTHZ-203):

| Step | Content |
|---|---|
| [00019_projects](../../internal/meta/postgres/migrations/00019_projects.sql) | the new tables `project` and `project_member`; `project_id` added to `repositories` (`NOT NULL`), `service_account` and `authz_policy` with their foreign keys `NOT VALID`; the triggers `project_member_kind` (`AI001`), `AI002`, `AI003` (the key of a project and the stored names of its service accounts and policies), `AI004` |
| [00020](../../internal/meta/postgres/migrations/00020_projects_repositories_index.sql) to [00022](../../internal/meta/postgres/migrations/00022_projects_policy_index.sql) | one `CREATE INDEX CONCURRENTLY IF NOT EXISTS` per file (`-- trove:no-transaction`, one statement): `repositories_project_key` (unique, `(project_id, key)`), `service_account_project`, `authz_policy_project` |
| [00023](../../internal/meta/postgres/migrations/00023_projects_policy_unique.sql) | `CREATE UNIQUE INDEX CONCURRENTLY` on `authz_policy (id, project_id)` |
| [00024](../../internal/meta/postgres/migrations/00024_projects_policy_entries.sql) | `ADD CONSTRAINT … UNIQUE USING INDEX` for `authz_policy_id_project` and `repositories_project_key` (no scan), the entry table with its composite foreign key, the triggers `AI005`, `AI006` and `AI007` (`repositories_move_guard`: a repository its project holds by ID, a project policy's key selector or a virtual repository's member list, pin rule or deployment target, does not change `project_id`; `authz_policy_selector_project`: a project policy's key selector names a repository of its project) |
| [00025](../../internal/meta/postgres/migrations/00025_projects_validate.sql) | `VALIDATE CONSTRAINT` of the three foreign keys of 00019 |

The runner applies every step under `SET lock_timeout = migrate.ddl_lock_timeout`; a no-transaction step drops an `INVALID`
index of its name that a failed attempt left before it retries. The new tables are empty and take their indexes and keys
inline. The [test](../../test/integration/postgres/projects_test.go) checks that no foreign key is left `NOT VALID`, that the indexes
are valid, that the unique constraints exist and every guard below.

The queries are in [project.sql](../../internal/meta/postgres/queries/project.sql) and
[authz.sql](../../internal/meta/postgres/queries/authz.sql); the registrations (`project`, `project_member`,
`authz_project_policy_entry`, and `project.version` as a revision column) are in
[tables_projects.go](../../internal/meta/postgres/tables_projects.go). The page [projects.md](projects.md) lists who uses what.

| Table or column | Purpose | Notes | Class |
|---|---|---|---|
| `project` | A project: key (immutable, `AI003`), display name, description, `version` (ETag) | `version` moves with the PUT fields only (Q-AUTHZ-185) | data |
| `project_member` | The members of a project: users and groups with the role `reader`, `developer`, `maintainer` or `owner` (Q-AUTHZ-207) | primary key `(project_id, principal_id)`; trigger `principal_kind_guard` (`AI001`) refuses other kinds; cascade on both sides; the partial index `project_member_owner` serves the last-Owner count | data |
| `repositories.project_id` | The project of a repository; the key is unique within it (`repositories_project_key`) | `NOT NULL`; foreign key without cascade: the project cannot be deleted while a repository belongs to it; changed by a move only | (column) |
| `service_account.project_id` | The project of a project service account | no cascade; trigger `AI003`: the stored name is `<key>/<name>`, an instance-wide one has no `/`, the project never changes | (column) |
| `authz_policy.project_id` | The project of a project policy | `ON DELETE CASCADE`; trigger `AI003` on the stored name `<key>/<name>`; `UNIQUE (id, project_id)` lets the entry table name the project | (column) |
| `authz_project_policy_entry` | The grants of project policies | composite foreign key `(policy_id, project_id)` allows rows of project policies only (Q-AUTHZ-174) | data |

Guards that the database enforces whatever the API checked. Together with the earlier ones, the SQLSTATE classes are:

| SQLSTATE | Guard | What it refuses |
|---|---|---|
| `AI001` | `principal_builtin_guard`, `principal_kind_guard` | a change of a built-in principal; a row that names a principal of the wrong kind, among them `project_member` (only users and groups) and `authz_role_binding` |
| `AI002` | `group_member_not_project_account` on `group_membership` | a project service account as a group member |
| `AI003` | `project_key_immutable`, `service_account_project_guard`, `authz_policy_project_guard` | a change of a project's key; a stored name of a project's service account or policy that is not `<key>/<name>` of its project, a `/` in the name of an instance-wide one, a change of their project |
| `AI004` | `authz_policy_entry_global_only` on `authz_policy_entry` | a row for a project policy |
| `AI005` | `project_account_principal_guard` on `authz_role_binding`, `authz_policy_entry` and `authz_project_policy_entry` | a project service account as the principal of a role binding or of an entry of a global policy, and of an entry of a policy of another project (Q-AUTHZ-175); a global service account and the project's own accounts pass |
| `AI006` | `virtual_same_project_guard` on `virtual_members`, `virtual_deploy_targets`, `virtual_pin_members` | a member, pin member or deployment target of another project than the virtual repository's (Q-AUTHZ-206); a move that would leave one is refused by the service (409 `repository_in_use`); the function locks both repositories `FOR SHARE` before it reads their projects (00045, Q-REPO-234) |

Tested in [projects_test.go](../../test/integration/postgres/projects_test.go).

## Member and selector writes lock their repositories (00045)

[00045_virtual_member_locks_repository](../../internal/meta/postgres/migrations/00045_virtual_member_locks_repository.sql)
replaces `virtual_same_project_guard` (`AI006`): it locks the referenced repository and the virtual repository `FOR SHARE`,
in ID order, before it reads their projects. A member, pin member or deployment target written beside a move of either
repository then waits for it and is refused (`AI006`), and a move that meets a write in flight waits and is refused by
`AI007`, whatever locks the Go path took (Q-REPO-234, R-REPO-060). The same migration replaces `policy_selector_project_guard`
(`AI007`) so that it locks the selected repository `FOR SHARE` before it reads its project. The integration test
[move_guard_race_test.go](../../test/integration/postgres/move_guard_race_test.go) drives both orders with raw SQL on two connections.

## Replica runtime (00044) and the overview statements (Q-OPS-175)

[00044_replica_runtime](../../internal/meta/postgres/migrations/00044_replica_runtime.sql) is an expand step that adds
`replica.runtime jsonb NOT NULL DEFAULT '{}'`: the measurement each heartbeat writes of the period since the previous one
([coordination.md](coordination.md#replica-registry)). A column with a constant default changes the catalog only and
rewrites no row; the step runs under the runner's `lock_timeout` and retries like every step. A binary of the previous
release neither writes nor reads the column (its `UpsertReplica` names the columns it knows, and sqlc expands the
`r.*` of `ListReplicas` into the columns of its own schema), so its rows keep `'{}'`, which the new release lists as no
runtime. `replica` is transient in backups ([replica_tables.go](../../internal/cluster/replica_tables.go)); the column changes no
export class. Tested by `TestR_OPS_003_ReplicaRuntimeColumn` ([overview_test.go](../../test/integration/postgres/overview_test.go)).

The counts of `GET /api/v1/system/overview` are five static statements in
[overview.sql](../../internal/meta/postgres/queries/overview.sql); they add no table, column or index. What each counts and
what it costs is in [http-api.md](http-api.md#system-overview-r-ops-004-q-ops-175); `TestR_OPS_004_OverviewStatements`
runs them against a migrated schema.
