# Database layer

PostgreSQL access lives in [internal/meta/postgres](../../internal/meta/postgres/) (the only package with
`internal/cluster` that imports the driver, R-ARCH-002). [internal/meta](../../internal/meta/) is the
driver-free leaf the rest of the code sees: `meta.Tx`, `meta.Runner` and the revision-column registry.
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

- `DB.InTx(ctx, func(pgx.Tx) error)` is the contract form (for sqlc: `q.New(tx)`). `DB.Tx(ctx, func(meta.Tx) error)`
  is the `meta.Runner` form with hooks: `Before` hooks run last inside the transaction (change-log writes,
  R-ARCH-018), `After` hooks once COMMIT succeeded. `RequestTx` adds `statement_timeout` via `set_config(..., true)`.
- READ COMMITTED. SQLSTATE 40001 and 40P01 repeat the function up to three times. Failover errors (08xxx except
  08P01, 57P01-03, 25006, 53300, closed or refused connections) repeat with exponential backoff until
  `retry.max_elapsed` and then become `errs.Unavailable` (`database_unavailable`, 503). A connection that proved
  wrong is destroyed, not returned to the pool. A COMMIT that loses its connection has an unknown outcome and is
  never repeated. `fn` must therefore be safe to run again.
- `Ready(ctx)` is the `database` readiness check: `SELECT NOT pg_is_in_recovery()` on the control connection.
  `CheckSchema(ctx, version)` is the `schema` check (below). `Stats()` feeds the pool metrics.

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

Not implemented yet: the audit-policy write and `audit_maintain_partitions()` (no audit tables), the
`database.backup_role` grants and the grants check (no backup tables), the restore mode of the runner (M5), and
the CI check against the latest release's files (no release yet).

### Compatibility gate

`postgres.Gate(SchemaState, RequiredSchema(), version)`: serves iff `applied >= requiredSchema` and
`compat_floor <= version`; `ErrSchemaBehind` means wait not ready, `*FloorError` means exit with
`schema requires trove >= <floor>`. The version `dev` passes every floor.

## Table registry

`tables.Register(name, class, Options{CycleBreak})` with classes `Data`, `Blobs`, `Transient`, `Schema`.
`postgres.CheckTableRegistry` reads the catalog and reports unregistered tables and foreign-key cycles among
exported tables without a `CycleBreak`. Tables of this package: `tables.go`. Tables created for other packages
by the first migrations: `tables_m0.go`; the owning package deletes its line there when it registers the table
itself (a duplicate panics at start-up).

## sqlc

`sqlc.yaml` (schema = migrations, output `q/`), `//go:generate go tool sqlc generate` in `doc.go`, run by
`make generate`. Catalog relations are written `pg_catalog.<name>`. Dynamic list queries belong in `listq/`,
which M1 creates with the first list endpoint.

## Integration tests

`test/integration/postgres/`, tag `integration`, run with the stack up:
`set -a; . .dev/integration/env; set +a; go test -tags integration -race ./test/integration/postgres/...`.
Every test creates its own database `trove_pg_<random>`. `stepsWith` injects extra or edited migration files.
The PgBouncer tests set role defaults with `ALTER ROLE` in a database of their own, because the pooler keeps
server connections per database. Not covered: a TLS handshake (the stack's PostgreSQL serves no TLS), and a
real PostgreSQL switchover (E0.13, e2e harness).
