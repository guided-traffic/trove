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

Not implemented yet: the audit-policy write and `audit_maintain_partitions()` (no audit tables), the
`database.backup_role` grants and the grants check (no backup tables), the restore mode of the runner (M5), and
the CI check against the latest release's files (no release yet).

### Privileges of the runtime role

Migration 00001 grants `trove_app` (when that role exists) SELECT, INSERT, UPDATE, DELETE on every later table.
Migration 00005 revokes INSERT, UPDATE, DELETE, TRUNCATE on `schema_migration` and `schema_state` again, so a SQL
injection cannot raise `applied` or lower `compat_floor`. `system_state` stays writable on purpose: the runtime sets
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
server connections per database. The stack serves TLS only (verify-full from the host);
[test/integration/tls](../../test/integration/tls) rewrites a copy of its CA in place and shows that the next new
connection uses it. Not covered: client-certificate authentication against the real server (the stack requests no
client certificate; the unit tests prove the per-connection reload against a fake server that answers the SSLRequest),
and a real PostgreSQL switchover (E0.13, e2e harness).
