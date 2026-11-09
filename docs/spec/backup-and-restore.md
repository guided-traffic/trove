# Backup and restore

Status: implemented (phase 7); P1 parts not built: client-side encryption of the database files (R-BAK-027) and restore onto
an existing blob store (R-BAK-025)

This page specifies trove's own backup (INV-19): the commands of the `backup` role
(`trove backup create`, `list`, `verify`, `delete` and `restore`), what a backup contains and how it
is laid out in an S3 target, how the PostgreSQL snapshot is taken and exported, how the blobs that
snapshot references are copied into a content-addressed pool that all backups of a target share,
how garbage collection is kept from deleting a blob that a running backup still has to copy, the
Kubernetes CronJob with its configuration, exit codes, metrics and alerts, retention and the pool
sweep, restore into an empty installation and what a restore rebuilds, and encryption, credentials
and key escrow.

The chart side (the rendered CronJob and restore Job, their values, Secrets and volumes) is
[ha-and-helm.md](ha-and-helm.md) R-HA-060; this page states what the commands require from it.
Platform-level protection stays in [ha-and-helm.md](ha-and-helm.md) R-HA-023 to R-HA-026:
PostgreSQL point-in-time recovery, bucket versioning and replication, cross-site disaster recovery
and `trove admin post-restore`. It complements this page with a shorter recovery point, and it does
not replace the consistent, portable backup the owner requires. The garbage-collection protocol
this page hooks into is [storage.md](storage.md) R-STOR-024, the stored-secret key ring is
[architecture.md](architecture.md) R-ARCH-021, the Valkey keyspace is
[architecture.md](architecture.md) R-ARCH-030, and the revocation journal is
[ha-and-helm.md](ha-and-helm.md) R-HA-054.

Covers: the backup, import and export entries of
[operations-and-monitoring.md](../features/operations-and-monitoring.md): OPS-032 (scheduled system
backups), OPS-033 (component backup and the master key, together with ha-and-helm.md), OPS-034
(system export, as the backup format), OPS-035 (system import, as restore) and OPS-039 (instance
migration by export and import, as restore into a new installation). The catalog ranks them P2,
P1, P2, P2 and P2; the owner constraint of 2026-10-28 (INV-19) makes backup creation and restore
MVP. Repository export and import (OPS-036, OPS-037) and the identity-service export (OPS-038) are
not covered here (see [Deferred](#deferred)).

## Terms

| Term | Meaning on this page |
|---|---|
| Source | The installation being backed up: its PostgreSQL database and its blob store (`storage.s3`). |
| Target | The S3 location backups are written to: endpoint, bucket and prefix under `backup.target`. One target holds the backups of one installation. |
| Backup | One restorable unit with a backup ID: a manifest, one file per exported table and a blob list. Its blobs live in the pool. |
| Pool | The content-addressed blob store inside the target, shared by every backup of that target. |
| Run | One execution of `create`, `delete` or `verify`, recorded as a `backup_run` row in the source database; `create` and `delete` are the target-writing runs. |
| Snapshot | The `REPEATABLE READ` snapshot of the source database that a `create` run exports. |
| Hold, pin | What keeps garbage collection from deleting a blob that a running `create` still has to copy (R-BAK-007). |
| Incarnation | A random ID for one life of an installation's database. A restore starts a new incarnation; the installation ID stays. |

## Handover to other pages

| Topic | Owner | What this page relies on or contributes |
|---|---|---|
| PITR, bucket versioning, cross-site disaster recovery, `post-restore`, deletion pause | [ha-and-helm.md](ha-and-helm.md) R-HA-023 to R-HA-026 | Platform protection with a shorter recovery point. This page's backup is the portable, consistent copy of INV-19. |
| Garbage-collection protocol | [storage.md](storage.md) R-STOR-024 | This page adds one check to the per-blob protocol (R-BAK-007). |
| Stored-secret key ring | [architecture.md](architecture.md) R-ARCH-021 | Backups hold sealed values only; escrow is R-BAK-028. |
| Migrations, the runner and the migration lock | [architecture.md](architecture.md) R-ARCH-026, [ha-and-helm.md](ha-and-helm.md) R-HA-018, R-HA-019 | The export holds the migration lock shared (R-BAK-005). Restore migrates to the backup's step, loads, then migrates the rest (R-BAK-023). |
| Connection classes, session host | [architecture.md](architecture.md) R-ARCH-019, [ha-and-helm.md](ha-and-helm.md) R-HA-058 | Backup and restore use the `backup` class on the session host (R-BAK-005). |
| Valkey keyspace | [architecture.md](architecture.md) R-ARCH-030 | A restore writes a new keyspace ID and never touches Valkey (R-BAK-022). |
| Revocation journal | [ha-and-helm.md](ha-and-helm.md) R-HA-054 (MVP) | Replayed by every restore (R-BAK-024). |
| CronJob, restore Job, Secrets, scratch volume, chart values | [ha-and-helm.md](ha-and-helm.md) R-HA-060, R-HA-030, R-HA-031 | What the commands require from the Jobs (R-BAK-012); the `config.backup` keys are this page's (R-BAK-011). |
| NetworkPolicy, PrometheusRule, connection budget | [ha-and-helm.md](ha-and-helm.md) R-HA-042, R-HA-045, R-HA-016 | Alerts and connection counts are specified here (R-BAK-014, R-BAK-005) and rendered or counted there. |
| Metrics and audit catalogs | [operations-and-observability.md](operations-and-observability.md) R-OPS-008, R-OPS-016 | Families and events are listed in R-BAK-014. |
| Management API and UI | [api-and-ui.md](api-and-ui.md) R-API-050, R-UI-048 | Read-only run, summary and stored-backup endpoints (R-API-050) and the backups screen (R-UI-048). |

## Overview

```text
create (backup role, one pod)                                       target
────────────────────────────                                        ──────
1 insert backup_run; migration lock shared; hold 'all'  ─ PostgreSQL
2 LOCK TABLE … ACCESS SHARE; snapshot S                 ─ PostgreSQL
3 referenced digests in S, diff with previous list  ─────────────▶  backups/<id>/blobs.idx.zst
4 pin the digests to copy; hold 'all' → 'pinned'        ─ PostgreSQL
5 COPY (SELECT …) of every table in S, zstd  ────────────────────▶  backups/<id>/db/<table>.copy.zst
6 copy pinned blobs source → pool, unpin  ───────────────────────▶  pool/sha256/<h0h1>/<h2h3>/<hex>
7 hold 'none'; manifest (the commit point)  ─────────────────────▶  backups/<id>/manifest.json
8 retention and pool sweep  ─────────────────────────────────────▶  deletes

restore (backup role, one pod, into an empty installation; resumes itself after an interruption)
──────────────────────────────────────────────────────────────────────────────────────────────
1 check manifest, files, key ring, journal, empty database and blob prefix
2 migrate the empty database to the backup's schema step
3 copy every listed blob pool → blob prefix
4 load all tables in one transaction
5 migrate to the binary's schema; fix-ups (jobs, keyspace, incarnation, revisions)
6 purge remote caches if they were excluded; replay the revocation journal; verify; finish
```

## Backup format

### R-BAK-001 · What a backup contains
Priority: **MVP** · Covers: OPS-032, OPS-033, OPS-034

A backup MUST be restorable on its own, without the source database, the source blob store or
Valkey. It contains:

1. **Database.** Every row of every trove table in one transactionally consistent snapshot of the
   source database (R-BAK-005), except the transient tables of R-BAK-004, plus the sequence
   values.
2. **Blobs.** Every blob that a row of that snapshot references (R-BAK-006), as an object in the
   target's pool with the same SHA-256. With `backup.exclude.remote_caches: true`, blobs referenced
   only by remote-repository caches are left out (R-BAK-008).
3. **Manifest.** Versions, schema step, snapshot time, per-table checksums and row counts, the
   blob list, options, source damage found during the run and the key IDs the sealed values need
   (R-BAK-003).

Not in a backup, by design:

| Not included | Why | On restore |
|---|---|---|
| Valkey contents | Shared ephemeral state only; losing it costs performance, never data (INV-3) | Valkey starts empty under a new keyspace (R-BAK-022) |
| Stored-secret key ring | Key material never leaves Kubernetes Secrets (R-ARCH-021) | From the operator's escrow (R-BAK-028) |
| Configuration file, Helm values, Kubernetes Secrets | Part of the release, not of the database (INV-15) | From Git and the operator's secret store |
| Transient tables (UI sessions, login flows, upload sessions, replica registry, …) | Valid for minutes or for one process; some hold live secrets | Empty (R-BAK-004) |
| Staged upload chunks (`_uploads/`), probe objects (`_probe/`), the revocation journal (`_journal/`) | Not referenced by any snapshot row; the journal lives outside the database on purpose | Clients restart uploads; the journal stays in the source bucket (R-BAK-024) |
| Audit records written after the snapshot | They exist only in the log stream | From the log pipeline |

A backup is consistent: restoring it yields the state of the snapshot, and every reference
resolves to an object with the referenced digest. A blob that was already damaged in the source
when the backup ran is recorded and restored as damaged (R-BAK-006), so the restored installation
answers for it exactly as the source did (`503`, [ha-and-helm.md](ha-and-helm.md) R-HA-024).

Acceptance criteria:

- Scenario 1 of R-BAK-029 (backup during continuous publishing) passes.
- After a restore, `SELECT count(*) FROM ui_session` and `SELECT count(*) FROM upload_sessions`
  return 0, and the restore's log reports, for every exported table, a loaded row count equal to
  the manifest's `rows` (the check of R-BAK-021 step 4; later steps add rows, for example
  `system.restored`).

### R-BAK-002 · Target layout, backup ID and target marker
Priority: **MVP** · Covers: OPS-032

Objects trove writes below `backup.target.prefix`:

```text
<prefix>/
  trove-backup-target.json                     target marker
  pool/sha256/<h0h1>/<h2h3>/<h0…h63>           shared blob pool, same fan-out as R-STOR-001
  backups/<backup-id>/db/<table>.copy.zst      one file per exported table
  backups/<backup-id>/blobs.idx.zst            blob list
  backups/<backup-id>/manifest.json            written last
```

- **Backup ID.** `<yyyymmdd>T<hhmmss>Z-<4 lowercase hex>`: the UTC start time of the run and 16
  random bits, for example `20261028T020000Z-7f3a` *(example)*. Lexical order is chronological
  order.
- **Completeness.** A backup exists if and only if its `manifest.json` exists. The manifest is
  written last, in one `PutObject`, after every other object of the backup and every pool object
  it lists has been confirmed. A `backups/<id>/` directory without a manifest is incomplete: it is
  never restored, listed only with `--all`, and removed by retention (R-BAK-016).
- **Immutability.** No trove command modifies an object under `backups/<id>/` or `pool/`. A pool
  object is written only when the run does not know it to exist; a repeated write carries the
  same bytes, because the key is the content's SHA-256.
- **Blob list.** zstd-compressed UTF-8 text, one line per blob, `<64 lowercase hex> <size in
  bytes>\n`, sorted ascending by digest, without duplicates.
- **Table files.** zstd-compressed PostgreSQL `COPY` text format (tab delimiter, `\N` for null),
  without a header line. The manifest records each file's column list.
- **Target marker.** `{"format": 1, "installationId": "…", "incarnation": "…", "createdAt": "…",
  "adoptedAt": null}`. The first run on an empty prefix writes it. Every `create` and `delete` run
  reads it at start and again before it writes a manifest or deletes anything. If its
  `installationId` or `incarnation` differs from the source's (`system_state`, R-BAK-022), the run
  exits `4` without writing.
- **Adoption after a restore.** `trove backup create --adopt-target` (chart value
  `backup.adoptTarget`, [ha-and-helm.md](ha-and-helm.md) R-HA-060, cross-page) rewrites the marker
  for this incarnation only if the marker names the installation ID and incarnation that this
  installation was restored from (`system_state` key `restored_from`, R-BAK-022). It then sets
  `adoptedAt` and writes the audit event `system.backup.target_adopted`. The condition makes
  adoption a compare-and-swap: once one restored installation has adopted the target, the marker
  names that one's incarnation, so the old installation (exit `4`) and any second installation
  restored from the same backup (a drill) can no longer write to it, and leaving the value set is
  harmless. Without `restored_from`, or with a marker of another lineage, the flag exits `4`. A
  restored installation that shall back up into its predecessor's target sets the value once
  (R-BAK-021 step 9).
- **Empty prefix only.** The marker is written only on a prefix that holds nothing below `backups/` and `pool/`. A prefix that
  holds objects there but no marker was not written by this lane, and the run exits `4` (Q-BAK-25).
- **Separate failure domain.** At start, `create` MUST refuse with exit `2` a target on the same
  endpoint and bucket as the source blob store, unless `backup.target.allow_same_bucket: true`.
  With that setting the two prefixes still must not contain each other. A backup inside the blob
  bucket shares the bucket's failure modes, so the setting exists for single-bucket evaluation
  setups only and logs a warning on every run. The chart validates the same rule
  ([ha-and-helm.md](ha-and-helm.md) R-HA-029, R-HA-060, cross-page). The local development profile
  uses a second bucket of its S3 store (Q-BAK-9).

Acceptance criteria:

- After one `create`, `aws s3api head-object --bucket trove-backups --key
  trove/pool/sha256/<h0h1>/<h2h3>/<hex>` succeeds for the SHA-256 of a published tarball.
- A second `create` without new uploads writes no pool object (`aws s3 ls --recursive …/pool/`
  is unchanged) and a second manifest.
- A backup directory whose manifest was deleted by hand is absent from `trove backup list` and
  present in `trove backup list --all` as `incomplete`.
- Two installations configured with the same target: the second one's `create` exits `4` and
  names the marker's installation ID.
- Two installations restored from the same backup, both with `--adopt-target`: the first one's
  `create` adopts the target and completes, the second one's exits `4`, and so does the next
  `create` of the installation the backup was taken from.
- `backup.target.bucket` equal to `storage.s3.bucket` without `allow_same_bucket` makes `create`
  exit `2`.

### R-BAK-003 · Manifest
Priority: **MVP** · Covers: OPS-032, OPS-034

`manifest.json` (example, abridged):

```json
{
  "format": 1,
  "backupId": "20261028T020000Z-7f3a",
  "installationId": "0191c2d4-…",
  "incarnation": "0192f0a1-…",
  "trove": {"version": "1.0.0", "revision": "3f2c1a…"},
  "schema": {"step": 57, "steps": [{"step": 1, "name": "00001_core", "sha256": "…"}]},
  "postgres": {"serverVersion": "17.6", "isolation": "repeatable read"},
  "startedAt": "2026-10-28T02:00:00.412Z",
  "snapshotAt": "2026-10-28T02:00:01.087Z",
  "completedAt": "2026-10-28T02:41:17.903Z",
  "previousBackupId": "20261027T020000Z-1c9e",
  "options": {"excludeRemoteCaches": false},
  "source": {"storage": {"endpoint": "", "bucket": "trove-blobs", "prefix": "trove"},
             "revocationJournal": true},
  "tables": [
    {"name": "items", "class": "data", "file": "db/items.copy.zst",
     "columns": [["id", "bigint"], ["repo_id", "bigint"], ["path", "text"]],
     "rows": 1843021, "bytes": 912345678, "compressedBytes": 201234567, "sha256": "…"}
  ],
  "sequences": [{"name": "items_id_seq", "lastValue": 1843377, "isCalled": true}],
  "blobs": {"file": "blobs.idx.zst", "sha256": "…", "referenced": 412907, "referencedBytes": 1873402911744,
            "copied": 2114, "copiedBytes": 9123456789,
            "damaged": [{"sha256": "5be0c1d2…", "status": "missing"}]},
  "secrets": {"keyIds": ["2026-10"]},
  "encryption": {"sse": "aws:kms", "clientSide": null}
}
```

- `format` is an integer. A reader MUST refuse a manifest with a higher `format` than it knows
  (exit `6`) and MUST ignore unknown fields.
- `sha256`, `bytes` and `compressedBytes` of each file are computed by trove while the file
  streams; `rows` counts the rows `COPY` produced. `verify` and `restore` check them (R-BAK-019,
  R-BAK-020).
- `schema.steps` lists every applied migration step with its file SHA-256 from
  `schema_migration`, read inside the snapshot (R-BAK-023).
- `secrets.keyIds` is the set of key IDs referenced by sealed values and by `secret_key_canary`
  rows in the snapshot, found through the sealed-column registry of R-ARCH-021. Key material is
  never written.
- `blobs.damaged` lists digests whose source object was missing or had the wrong size during the
  run (R-BAK-006). They are in the blob list but not in the pool.
- `source` records where the source's blob store was (no credentials) and whether its revocation
  journal was enabled (`backup.revocation_journal.enabled`), so that a restore finds the journal
  it must replay (R-BAK-024).
- The manifest is the last object of a backup (R-BAK-002). It carries no signature; H-46 covers
  what that leaves open.

Acceptance criteria:

- A JSON Schema of the manifest (`internal/backup/format/manifest.schema.json`, Q-BAK-13) validates every manifest
  the test suite writes.
- A manifest edited to `"format": 2` makes `trove backup restore` and `verify` exit `6` and name
  the format.

## Database snapshot

### R-BAK-004 · Table registry and export classes
Priority: **MVP** · Covers: trove-only

Every table of trove's schema has an export class, registered in code by its owning package with
`backup.RegisterTable(name, class, backup.TableOptions{CycleBreak: …})`, the same pattern as the
sealed-column registry of R-ARCH-021.

| Class | Exported | On restore |
|---|---|---|
| `data` (default) | Every row of the snapshot | Loaded as exported |
| `blobs` | Only for `blobs`: the rows of digests referenced in the snapshot (R-BAK-006) | Loaded; unreferenced or deleting rows never exist after a restore |
| `transient` | Not exported | Empty |
| `schema` | Not exported; the manifest records the steps | Recreated by migrations (R-BAK-023) |

- The set of exported tables comes from the PostgreSQL catalog inside the snapshot: every ordinary
  and partitioned table in `database.schema`; partitions are exported through their parent. A
  table that the catalog shows but the registry does not know (for example one created by a newer
  migration than the running binary) is exported as `data`, and the run's result lists it under
  `unregisteredTables`. Nothing is skipped silently.
- `CycleBreak` names a nullable foreign-key column that restore loads as `NULL` and fills
  afterwards, for tables in a foreign-key cycle. Initial value: `package_versions.lead_item_id`
  ([storage.md](storage.md) R-STOR-003).
- Stored generated columns are left out of the exported column list, because `COPY FROM` cannot
  write them; the load recomputes them. Identity columns are exported.
- A migration test applies every embedded migration to an empty database and fails when a table
  is not registered, or when the foreign-key graph of the `data` and `blobs` tables has a cycle
  without a registered `CycleBreak`.

Initial `transient` and `schema` assignments. Every other table is `data`; an owning page that
moves one of its tables states it on its own page.

| Table | Owner | Class | Reason |
|---|---|---|---|
| `ui_session` | [identity-and-authentication.md](identity-and-authentication.md) | `transient` | Holds raw ID tokens for logout; a restore ends all sessions anyway |
| `oidc_login_flow`, `cli_login` | identity-and-authentication.md | `transient` | Ten-minute flows; `oidc_login_flow` holds PKCE verifiers in clear |
| `upload_sessions`, `upload_session_parts` | [storage.md](storage.md) R-STOR-011 | `transient` | Their staged bytes under `_uploads/` are not backed up; clients restart their uploads |
| `pending_sidecars` | [storage.md](storage.md) R-STOR-010 | `transient` | Declarations are valid for at most `storage.sidecars.pending_ttl` |
| `blob_gc_queue` | storage.md | `transient` | Candidates only; a restored database has no unreferenced blob rows |
| `cluster_change` | [architecture.md](architecture.md) R-ARCH-018 | `transient` | Change rows that only running replicas read; `cluster_epoch` stays `data` |
| `ci_issuer_keys` | [gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-003 | `transient` | A restored copy would accept keys the issuer removed (revoked) after the snapshot; the first exchange or `ci-keys-refresh` fetches the current set |
| `replica` | [operations-and-observability.md](operations-and-observability.md) R-OPS-003 | `transient` | Describes pods that do not exist after a restore |
| `support_bundle`, `support_bundle_part` | operations-and-observability.md R-OPS-027 | `transient` | Diagnostic archives with logs; their blobs are not referenced by any other row |
| `api_idempotency_keys` | [api-and-ui.md](api-and-ui.md) | `transient` | Retry protection for minutes |
| `backup_run`, `backup_blob_pin` | this page | `transient` | The target, not the database, is the record of backups |
| `vuln_advisory`, `vuln_affected`, `vuln_kev` | [vulnerability-scanning.md](vulnerability-scanning.md) R-VULN-026 (from M4b) | `transient` | Public advisory data that the next sync fetches again, hundreds of megabytes in every export otherwise (Q-VULN-13) |
| `vuln_source_state` | vulnerability-scanning.md R-VULN-026 (from M4b) | `transient` | After a restore every ecosystem is `empty`, so the next sync slot runs a full import, from the restored mirror repository in mode `repository` |
| `vuln_pending_evaluation`, `vuln_claimed_match` | vulnerability-scanning.md R-VULN-026 (from M4b) | `transient` | Derived from the advisory data; the full evaluation after the first import recomputes them |
| `mirror_state` | [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-058 (from phase 6b) | `data` | The cursor and summary of a mirror's last complete pass describe the cache of the same snapshot; R-BAK-022 clears the pass in progress (Q-REPO-147) |
| `mirror_pending` | repositories-and-proxy.md R-REPO-058 (from phase 6b) | `transient` | The names an interrupted pass had pending; the first pass after the restore rediscovers them from the restored cursor |
| `ui_assets` | [ha-and-helm.md](ha-and-helm.md) R-HA-020 (Q-HA-121) | `transient` | Names the UI files the replicas published to the blob store; every replica that serves the UI publishes its own build again at its next start or within the hour |
| `schema_migration`, `schema_state` | [ha-and-helm.md](ha-and-helm.md) R-HA-018 | `schema` | Recreated by the migrations of R-BAK-023 |

`ci_subject_token_uses` ([gitlab-ci-oidc.md](gitlab-ci-oidc.md)) stays `data` on purpose: it
keeps a single-use subject token used before the snapshot from being exchanged again after a
restore; tokens used after the snapshot are refused through the `system_state` key `restored_at`
([gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-018).

`vuln_finding` and `vuln_finding_event` ([vulnerability-scanning.md](vulnerability-scanning.md)
R-VULN-026, from M4b) stay `data`: triage is work people did and cannot be fetched again, and while
the advisory tables are empty after a restore no evaluation resolves a finding, so restored blocks
hold until the first complete import (R-VULN-011).

`trove migrate up` checks the registry after it has applied the migrations: it compares the tables of the schema with
`tables.Default()` and refuses with exit `3`, naming the unregistered tables, so an installation never runs with a
table that no backup classifies. The check is built in M0 (the `cmd/trove` binary links the blob, cluster and jobs
packages that own the tables).

Acceptance criteria:

- The migration test above runs in CI and fails on a test migration that adds an unregistered
  table.
- `trove migrate up` on a schema with an unregistered table exits `3` and names the table.

- A run against a schema with an extra table `x_new` (created by hand) exports it and lists it in
  `result.unregisteredTables`.

### R-BAK-005 · Consistent snapshot and table export
Priority: **MVP** · Covers: OPS-033, OPS-034

The export MUST read every table from one snapshot taken after the run's hold is committed
(R-BAK-007).

1. **Primary only, session host.** Every connection of the run belongs to the `backup` class of
   [architecture.md](architecture.md) R-ARCH-019 (`application_name` `trove:<pod>:backup`) and
   goes to the session host
   (`database.session_host`, or `database.host` when no transaction-mode pooler is configured;
   [ha-and-helm.md](ha-and-helm.md) R-HA-058), with the backup database credential (R-BAK-015). A
   transaction-mode pooler cannot carry the session lock and the hours-long snapshot transaction
   below. When the session host is a session-mode pooler, its idle-transaction timeout (PgBouncer
   `idle_transaction_timeout`) must be 0 or longer than the export, because the snapshot connection
   sits idle in its transaction while the export connections work. The run checks
   `SELECT pg_is_in_recovery()`; on a standby it exits `2`: the hold and the pins are writes, and
   the snapshot must follow the hold on the same timeline.
2. **Snapshot transaction.** On the snapshot connection:

   ```sql
   SET statement_timeout = 0;
   SET idle_in_transaction_session_timeout = 0;
   SET lock_timeout = '15min';                  -- backup.database.migration_wait
   SELECT pg_advisory_lock_shared(3, 0);        -- the migration lock, shared (R-HA-019, R-ARCH-017)
   SET lock_timeout = '60s';                    -- backup.database.lock_timeout
   -- control connection: hold = 'all', COMMIT (R-BAK-007 step 1)
   -- read the catalog's table list
   BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;
   LOCK TABLE <every table of the catalog list> IN ACCESS SHARE MODE;
   SELECT pg_export_snapshot(), clock_timestamp(), current_setting('server_version');
   ```

   - **Session settings.** The three `SET` statements above are the settings of the snapshot and export connections. They
     travel as startup parameters of those connections, because session-level `SET` is reserved for the migration runner
     ([architecture.md](architecture.md) R-ARCH-019), and the wait for the migration lock polls `pg_try_advisory_lock_shared`
     for at most `backup.database.migration_wait` (Q-BAK-23).
   - **Migrations.** `trove migrate` takes the same advisory lock exclusively
     ([ha-and-helm.md](ha-and-helm.md) R-HA-019), so no migration step runs while the database is
     exported, and the export never sees a half-applied migration. A running migration makes the
     run wait up to `backup.database.migration_wait`, then exit `75`. A migration started during
     an export waits up to `migrate.advisory_lock_wait` and then fails naming the backup; the lock
     is released as soon as the last table is exported, not at the end of the blob copy. The lock
     is taken before `BEGIN` and as a session lock, because a `SELECT` inside the transaction would
     fix the snapshot before the table locks.
   - The table locks come before the first `SELECT`, because a `REPEATABLE READ` transaction's view
     is frozen when its first `SELECT` begins ([LOCK](https://www.postgresql.org/docs/current/sql-lock.html)).
     Holding `ACCESS SHARE` on every table blocks `TRUNCATE` and table-rewriting `ALTER TABLE`
     from outside the migration runner, which are not MVCC-safe and would otherwise make a table
     appear empty to the snapshot
     ([MVCC caveats](https://www.postgresql.org/docs/current/mvcc-caveats.html)). `ACCESS SHARE`
     needs only `SELECT`.
   - The table list is read from the catalog before `BEGIN`, and again after the locks inside
     the snapshot; a difference (a table created or dropped by hand in between) aborts the attempt
     and the run retries it once.
   - **Isolation.** The snapshot transaction and the export transactions of step 3 run at
     `REPEATABLE READ READ ONLY`. They are the one stated exception to the `READ COMMITTED` rule of
     [architecture.md](architecture.md) R-ARCH-019; every other transaction of the run, the control
     connection's included, runs at `READ COMMITTED`.
   - A table-lock wait longer than `lock_timeout` ends the run with exit `75`.
   - Inside the snapshot the run reads the schema step and step checksums, the column list of
     every table, the key IDs of R-BAK-003 and the blob list of R-BAK-006.
3. **Parallel export.** `backup.database.parallel` further connections each run
   `BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY; SET TRANSACTION SNAPSHOT '<id>';` and then
   export tables, largest first. A snapshot can be imported only while the exporting transaction
   is open ([pg_export_snapshot](https://www.postgresql.org/docs/current/functions-admin.html#FUNCTIONS-SNAPSHOT-SYNCHRONIZATION),
   [SET TRANSACTION](https://www.postgresql.org/docs/current/sql-set-transaction.html)), so the
   snapshot connection stays open until the last table is exported, and no longer.
4. **One statement per table.** `COPY (SELECT <columns> FROM <table> [WHERE <class filter>]) TO
   STDOUT`, text format. The `SELECT` form is needed for partitioned tables, which plain
   `COPY <table> TO` does not read, and for the `blobs` filter
   ([COPY](https://www.postgresql.org/docs/current/sql-copy.html)). The text format is used
   because the binary format is less portable across PostgreSQL versions (same page).
5. **Streaming.** Each table streams through a zstd encoder (`backup.compression_level`) into a
   multipart upload of `backup.database.part_size` parts, while SHA-256, byte and row counts are
   computed. Nothing is written to local disk.
6. **Sequences.** After the tables, the run reads `last_value` and `is_called` of every sequence
   in the schema. Sequences are not transactional, so the values are at least as high as every
   value used by a row in the snapshot.

- The snapshot and the shared migration lock are held only while the database is exported; the
  blob copy runs on after them (R-BAK-009). While the snapshot is open, vacuum cannot remove rows
  deleted after it began, so the export time bounds that effect. Its duration is in the run's
  progress and in `trove_backup_snapshot_open_seconds`.
- A `helm upgrade` whose migration Job starts during an export fails after
  `migrate.advisory_lock_wait` and is repeated after the export. Operators schedule backups apart
  from upgrades; the upgrade preparation of [ha-and-helm.md](ha-and-helm.md) R-HA-021 runs its
  pre-upgrade backup to completion first.
- Connections per run: one control connection (run row, heartbeat, pins), one snapshot connection
  and `backup.database.parallel` export connections, `application_name` `trove:<pod>:backup`.
  With defaults that is 4, once per cluster, because only one run is active (R-BAK-010); the
  number does not depend on the replica count. A restore uses 1: the session connection that holds the
  migration lock and runs the migration runner, the load and the fix-ups (Q-BAK-31). Both belong in the connection budget of
  [ha-and-helm.md](ha-and-helm.md) R-HA-016 (cross-page).

Acceptance criteria:

- With `backup.database.parallel: 2`, `SELECT count(*) FROM pg_stat_activity WHERE
  application_name LIKE 'trove:%:backup'` never exceeds 4 during a run.
- A run against a standby endpoint exits `2` with a message naming `pg_is_in_recovery`.
- A test holds `ACCESS EXCLUSIVE` on `items` in another session for 90 s with
  `backup.database.lock_timeout: 60s`: the run exits `75` and writes no manifest.
- During an export, a test session runs `ALTER TABLE item_properties ALTER COLUMN value TYPE text`
  (a rewrite) with `lock_timeout = '5s'`; it fails with `55P03`, and the restored row count of
  `item_properties` equals the source's count at `snapshotAt`.
- `audit_event` (partitioned) is exported with every row of every partition.
- With `migrate.advisory_lock_wait: 10s`, `trove migrate up` started during an export exits
  non-zero without applying a step and names the backup; started during the blob copy, it
  succeeds. A run started while a test session holds `pg_advisory_lock(3, 0)` waits and exits `75`
  after `backup.database.migration_wait`.
- With the local profile's transaction-mode pooler (`dev-up POOLER=1`), every backup connection in
  `pg_stat_activity` comes from the session host.

## Blobs

### R-BAK-006 · Blob list and incremental copy into the pool
Priority: **MVP** · Covers: OPS-032, OPS-033

**Referenced set.** Inside the snapshot, the run reads the digests and sizes of every `blobs` row
referenced by at least one row of a `data` table, ordered by digest. The referencing tables come
from the catalog's foreign keys to `blobs`, the same rule garbage collection uses
([storage.md](storage.md) R-STOR-003, reference rule 3), so a table added by a newer migration is
included. A referenced row in state `deleting` contradicts R-STOR-024's invariant; the run then
fails with `invariant_violation` and writes no manifest.

**Incremental by digest.** Content addressing makes a blob immutable, so a blob in the pool never
needs to be copied again.

1. The *previous backup* is the newest complete backup in the target (any incarnation). Its
   blob list minus its `blobs.damaged` is known to be in the pool, because pool objects are
   deleted only when no complete backup lists them (R-BAK-017).
   The previous list is trusted only after it matched its manifest: while the merge streams it, the run hashes it and counts its
   entries, and a mismatch of the SHA-256 or the count fails the run with `pool_damaged` before the hold turns `pinned`
   (Q-BAK-54).
2. The run merges the referenced set (a sorted stream from the snapshot) with the previous list (a
   sorted stream from the target) and produces in one pass:
   - the new blob list, written to `backups/<id>/blobs.idx.zst`;
   - the *to-copy set*: referenced digests not in the previous list, bulk-inserted in batches into
     `backup_blob_pin` on the control connection (R-BAK-007, Q-BAK-17).
   Memory stays bounded; neither list is held in full.
3. Without a previous backup, the to-copy set is the whole referenced set.

**Copy.** For each pinned digest, `backup.copy.concurrency` at a time, the run writes
`pool/sha256/<h0h1>/<h2h3>/<hex>`:

- **Server-side copy** (`backup.copy.server_side: auto`, `always`, `never`). `CopyObject` for
  objects up to 5 GiB, `UploadPartCopy` parts of `backup.copy.part_size` above that
  ([CopyObject](https://docs.aws.amazon.com/AmazonS3/latest/API/API_CopyObject.html)). It needs
  `s3:GetObject` on the source for the target principal (same page). `auto` probes once per run
  with the first pinned digest and falls back to streaming on `AccessDenied`, a different endpoint,
  or a store that rejects cross-bucket copies. The copy request repeats the target's encryption
  settings (R-BAK-026). S3 can report a copy error inside a `200` response (same page); the SDK's
  error handling for that case MUST stay enabled, and the run confirms every copy with
  `HeadObject`.
- **Streaming copy.** Each part is a ranged `GetObject` from the source, piped into `UploadPart`
  (or one `PutObject` for objects up to `backup.copy.part_size`); no part is buffered in full.
  The run hashes the stream and aborts the upload when the SHA-256 differs from the digest.
- **Confirmation.** A pin is removed only after the target object exists with the expected size.
  Where the store reports a full-object SHA-256 checksum for the copy, it MUST equal the digest.
  Pins are deleted in batches of up to 1,000.
- **Existing objects.** `backup.copy.check_existing: auto` sends `HeadObject` before each copy only
  when the target holds an incomplete `backups/<id>/` directory newer than the previous backup (a
  resumed attempt finds the objects its predecessor copied). The decision is read from the target,
  not from `backup_run`, which is empty after a restore. `always` checks every time, which avoids
  extra object versions on a versioned target; `never` never checks. A repeated write is harmless
  either way, because a pool key is its content's digest.
- **Source damage.** A `404` from the source, or a size that differs from `blobs.size`, is checked
  against the source database with `READ COMMITTED`:
  - **A row that is not `ok`.** The snapshot reads `blobs.content_status` with the digest and size. A referenced blob whose status is
    `missing` or `corrupt` is listed in the blob list and under `blobs.damaged` with that status, and it is not copied, even when an
    earlier backup holds a pool object for it; the next backup copies it again once the repair has set the status back to `ok`
    (Q-BAK-54);
  - row `live`: the blob is damaged in the source (R-STOR-027). It goes into `blobs.damaged`, the
    run continues and ends `completed_with_warnings`;
  - row `deleting` or gone: the hold was lost (R-BAK-007). The run fails with `hold_lost` and
    writes no manifest.
- **Throttling.** `backup.copy.bytes_per_second` (0 = unlimited) caps the transfer rate per run;
  retries follow the store settings of R-STOR-033, with the target's own client.
- **Throughput.** One pod copies. A server-side copy moves the bytes inside the store, so the pod
  only issues requests; a streaming copy is bounded by the pod's network bandwidth. The drill
  measures both (R-BAK-029 scenario 9). Sharding one run's copy over several pods is Deferred.

Acceptance criteria:

- After a first backup, a second one with 10 new tarballs reports `blobs.copied: 10` and
  `referenced` equal to the source's referenced count.
- With source and target on the local profile's S3 store and one credential that can read both,
  `trove_backup_copied_objects_total{mode="server"}` grows and `{mode="stream"}` does not; with a
  target credential lacking `s3:GetObject` on the source, the run falls back to streaming and
  completes.
- A 6 GiB generic upload is copied with `UploadPartCopy` and `aws s3api head-object` on the pool
  key reports 6,442,450,944 bytes.
- `aws s3 rm` of one referenced blob object in the source, then `create`: the run ends
  `completed_with_warnings`, the manifest lists the digest under `blobs.damaged`, and the restored
  blob row has `content_status = 'missing'`.

### R-BAK-007 · Garbage-collection hold
Priority: **MVP** · Covers: trove-only

Garbage collection MUST NOT delete a blob that a running `create` still has to copy, whatever
`storage.gc.grace_period` is and however long the run takes (Q-BAK-2).

**State.** The run's row carries `hold` (`all`, `pinned`, `none`) and `lease_expires_at`
(R-BAK-009); `backup_blob_pin` holds the to-copy set. A hold counts only while its lease is valid.

| Phase | `hold` | Effect on garbage collection |
|---|---|---|
| From step 1, once the shared migration lock is held, until the pins are committed | `all` | No blob moves to `deleting` |
| Pins committed, until the last pin is removed | `pinned` | Pinned digests do not move to `deleting` |
| Copy finished, failed run, expired lease | `none` | No effect |

**Protocol of the run.**

1. Once the snapshot connection holds the shared migration lock (R-BAK-005), set the run row's
   `hold = 'all'` and commit (`READ COMMITTED`). The row was inserted at the start of the run with
   `hold = 'none'`, so a wait for a migration or for a stale run's lease (R-BAK-010) never holds
   back garbage collection.
2. Take the snapshot (R-BAK-005), after that commit.
3. Insert the to-copy set into `backup_blob_pin` and commit.
4. In a separate transaction, `UPDATE backup_run SET hold = 'pinned' WHERE id = $1 AND hold =
   'all'`.
5. Delete each pin after its copy is confirmed (R-BAK-006); set `hold = 'none'` when none is
   left.

**Check in garbage collection** ([storage.md](storage.md) R-STOR-024, cross-page). In the per-blob
transaction, after the reference check (step 2) and before the state change (step 3), as a
separate statement:

```sql
SELECT EXISTS (SELECT 1 FROM backup_run r
                WHERE r.hold = 'all' AND r.lease_expires_at > now())
    OR EXISTS (SELECT 1 FROM backup_blob_pin p JOIN backup_run r ON r.id = p.run_id
                WHERE p.sha256 = $1 AND r.hold = 'pinned' AND r.lease_expires_at > now());
```

If it returns true, the blob stays `live`, its `blob_gc_queue` entry stays, and
`trove_gc_candidates_held_total` grows. The next collection run considers it again. The orphan
sweep (R-STOR-026) needs no check: it deletes only objects without a blob row, and a row
disappears only through the protocol above.

- **Batch mode.** When the collector handles a batch in one transaction (R-STOR-024, "Deletion
  batching"), one hold-check statement covers the batch (`p.sha256 = ANY($1)`, returning the held
  digests), issued after the batch's last reference check. The proof below needs only that the
  hold check starts after the reference check of the same blob.
- **Skipping a run.** A collection run that finds a valid `all` hold before it claims candidates
  ends at once without work, so a global hold does not make the collector lock and re-check every
  candidate. The per-blob check above still guards the race; the early exit is an optimisation
  only.

**Why a completed backup is complete (safety).** A run writes its manifest only after every digest
of its to-copy set was confirmed in the pool, and every other listed digest is in the previous
backup's list, which the pool sweep never deletes while that backup exists (R-BAK-017). The
referenced set is read from the snapshot itself. Completeness therefore does not depend on the
hold at all.

**Why a running backup is not starved (liveness).** Let blob *X* be referenced in snapshot *S*.
*X* moves to `deleting` only in a collection transaction *G* that took `FOR UPDATE` on *X*'s row,
then found no reference in a statement started after the lock was granted (R-STOR-024 rule 2), and
then ran the hold check above as a later statement.

1. *G*'s hold check started before the run's `all` hold committed. Then *G*'s reference check
   started earlier still. It saw no reference, and no reference can be committed while *G* holds
   the row lock, because the guard trigger's `FOR SHARE` waits for *G* and then finds `deleting`.
   *S* is taken after the `all` hold committed, so *S* sees no reference to *X*. That contradicts
   the premise.
2. *G*'s hold check started after the `all` hold committed and before step 4. Under `READ
   COMMITTED` the statement sees `hold = 'all'` and keeps *X*.
3. *G*'s hold check started after step 4. The pins committed before step 4, so the statement sees
   *X*'s pin, unless *X* was not in the to-copy set (it is already in the pool) or its pin was
   removed after the copy. In every case the run no longer needs *X* from the source.

So, while the lease is valid, *X* stays in the source until the run has copied it.

**Lease.** The run renews `lease_expires_at = now() + backup.lease_duration` every
`backup.heartbeat_interval`, and only while it makes progress (R-BAK-013). A run that dies or
hangs leaves a hold that garbage collection ignores once the lease has expired; the next run marks
it `abandoned` (R-BAK-010). A stalled run whose lease
expired may find a blob gone; it then fails with `hold_lost` (R-BAK-006) and never writes a
manifest, so safety holds. A long global hold (`all`) delays collection only for the time it takes
to stream the referenced set and insert the pins; `TroveBackupGlobalHoldLong` alerts when that
takes longer than 30 minutes.

Acceptance criteria:

- With `storage.gc.grace_period: 0s` and a fault-injection pause after step 4 of a run, a package
  whose only blob is pinned is deleted and `POST /api/v1/system/tasks/gc/run` runs: the blob stays
  `live`, `trove_gc_candidates_held_total` grows by 1, and the run completes. The first collection
  after the run deletes the blob.
- With a pause between steps 1 and 3, a collection run deletes no blob at all.
- The backup pod is stopped with `kill -STOP` for longer than `backup.lease_duration`; a
  collection run then deletes a pinned, unreferenced blob; after `kill -CONT` the run ends
  `failed` with `hold_lost` (or `superseded`, R-BAK-010), and no manifest exists for it.
- A property test over random interleavings of uploads, deletes, collections and a run (the race
  harness of R-STOR-005 extended with a run) never produces a completed backup whose pool lacks a
  listed, undamaged digest, and never a run that fails with `hold_lost` while its lease was valid.

### R-BAK-008 · Excluding remote-repository caches
Priority: **MVP** · Covers: OPS-032

`backup.exclude.remote_caches: true` (default `false`; `create --exclude-remote-caches` for one
run) leaves out the blobs that only remote-repository caches reference, which an upstream can
deliver again.

- The database is exported in full as usual. Only the to-copy and blob lists change: a digest is
  listed only if a row other than a remote-repository cache row references it. Cache rows are the
  `items` of repositories with `class = 'remote'` and the rows of `remote_cache_entries`
  ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-025); a blob shared with a local
  repository is always included.
- The manifest records `options.excludeRemoteCaches: true`.
- Restore loads everything, then empties every remote repository's cache (R-BAK-021 step 7): it
  deletes the cached items of each remote repository through the storage delete path with the
  trash can bypassed ([storage.md](storage.md) R-STOR-018), so format hooks update their indexes
  and `rev_seq` grows; it deletes the repository's `remote_cache_entries` and the format's index
  rows of that repository's stored documents (npm: `npm_remote_packages`, whose
  `npm_remote_versions` rows cascade; [formats/npm.md](formats/npm.md) R-NPM-024, R-NPM-047); then
  it deletes the blob rows that are neither referenced nor in the pool. The restored remote
  repositories behave like freshly created ones with their configuration, and the first request
  fetches from the upstream.
- Negative entries need no step: since [architecture.md](architecture.md) Q-ARCH-16 they live in
  Valkey under the old keyspace, which a restore neither reads nor needs
  ([architecture.md](architecture.md) R-ARCH-030, R-BAK-022).
- Content that the upstream no longer serves (a removed package version) is lost with this
  option. The option is therefore off by default, and `create` logs a warning per run while it is
  on.

Acceptance criteria:

- With the option on, a backup of an installation whose `npm-remote` cached `lodash@4.17.21` holds
  no pool object for that tarball; after restore, `npm install lodash@4.17.21 --registry
  …/api/npm/web/npm-remote/` succeeds and the upstream stub records one tarball request.
- A tarball published to `npm-local` and also cached in `npm-remote` is in the pool.

## The backup job

### R-BAK-009 · `trove backup create`: phases and run record
Priority: **MVP** · Covers: OPS-032

`trove backup create [--trigger schedule|manual] [--exclude-remote-caches] [--no-prune]
[--adopt-target] [--json]` runs one backup in the `backup` role. It never listens on a client port,
never uses Valkey, and needs no local disk beyond 64 MiB of scratch space (R-BAK-012).

While `system_state` holds `restore_in_progress` (R-BAK-021), or the schema is below the binary's
required step, `create` writes nothing and exits `75`: it must never back up, prune or adopt from
a half-restored or half-migrated installation. The chart also renders the CronJob suspended while
`restore.enabled` is set ([ha-and-helm.md](ha-and-helm.md) R-HA-060); the check covers a run
started by hand.

```sql
CREATE TABLE backup_run (
  id               uuid        PRIMARY KEY,                      -- UUIDv7
  operation        text        NOT NULL CHECK (operation IN ('create', 'delete', 'verify')),
  backup_id        text,                                         -- R-BAK-002; NULL for delete runs of several backups
  state            text        NOT NULL CHECK (state IN ('starting', 'snapshot', 'copying', 'finalizing', 'pruning',
                                                         'completed', 'completed_with_warnings', 'failed', 'interrupted',
                                                         'abandoned')),
  hold             text        NOT NULL DEFAULT 'none' CHECK (hold IN ('all', 'pinned', 'none')),
  trigger          text        NOT NULL CHECK (trigger IN ('schedule', 'manual')),
  target           text        NOT NULL,                         -- 's3://<bucket>/<prefix>' and endpoint host; no credentials
  pod              text        NOT NULL,                         -- pod name (downward API)
  trove_version    text        NOT NULL,
  snapshot_at      timestamptz,
  progress         jsonb       NOT NULL DEFAULT '{}',            -- phase, tablesDone/Total, blobsToCopy, blobsCopied, bytesCopied
  result           jsonb,                                        -- manifest summary, retention outcome, warnings
  error            jsonb,                                        -- problem object (R-ARCH-023)
  started_at       timestamptz NOT NULL DEFAULT now(),
  heartbeat_at     timestamptz NOT NULL DEFAULT now(),
  lease_expires_at timestamptz NOT NULL,
  finished_at      timestamptz
);
CREATE UNIQUE INDEX backup_run_one_active ON backup_run ((1))
  WHERE operation IN ('create', 'delete')
    AND state IN ('starting', 'snapshot', 'copying', 'finalizing', 'pruning');
CREATE INDEX backup_run_hold ON backup_run (hold) WHERE hold <> 'none';

CREATE TABLE backup_blob_pin (
  run_id  uuid   NOT NULL REFERENCES backup_run (id) ON DELETE CASCADE,
  sha256  bytea  NOT NULL CHECK (octet_length(sha256) = 32),
  size    bigint NOT NULL,
  PRIMARY KEY (sha256, run_id)
);
CREATE INDEX backup_blob_pin_run ON backup_blob_pin (run_id, sha256);
```

`backup_blob_pin.sha256` deliberately has no foreign key to `blobs`: a pin is not a reference, and
it must not hold a blob row against the collector beyond the run's lease.

Phases of a `create` run:

| State | Work | `hold` |
|---|---|---|
| `starting` | Configuration check, run row, target preflight (reachable, writable, marker, encryption: R-BAK-002, R-BAK-026), shared migration lock | `none`, then `all` |
| `snapshot` | Locks and snapshot, referenced set, diff, blob list, pins | `all`, then `pinned` |
| `copying` | Table export (snapshot open) and blob copy, in parallel | `pinned`, then `none` |
| `finalizing` | Fenced lease check, spot check of the pool, manifest | `none` |
| `pruning` | Retention and pool sweep (R-BAK-016, R-BAK-017), skipped with `--no-prune` | `none` |
| `completed`, `completed_with_warnings` | Result and audit event written | `none` |
| `failed` | An error the run could not handle; error written, pins deleted | `none` |
| `interrupted` | `SIGTERM` (a drain, an eviction, a deleted pod); pins deleted; not a failure for alerting (R-BAK-012, R-BAK-014) | `none` |
| `abandoned` | Set by a later run when this run's lease expired (R-BAK-010) | `none` |

- **Progress.** The heartbeat writes `progress` with the phase, tables exported, blobs and bytes
  copied, pins left, and the expected totals.
- **Every operation heartbeats.** `verify` and `delete` runs renew their lease like `create`, so a
  dead run of any operation ends `abandoned` and never looks active forever.
- **Spot check.** Before the manifest, the run sends `HeadObject` for `backup.verify_sample`
  random digests of the blob list that it did not copy itself (they come from earlier backups);
  a missing object fails the run with `pool_damaged` and the advice to run `trove backup verify`.
- **Pruning failure.** The manifest is the commit point. A failure of the pruning phase after it ends the run
  `completed_with_warnings` with the cause in `result.warnings`, not `failed`; only a run that was taken over stops (Q-BAK-24).
- **History.** `backup_run` rows older than `backup.run_history` are deleted by the next run.
- `--json` prints the result object on standard output; the log stream (R-OPS-009) carries one
  record per phase change.

Acceptance criteria:

- During a run, `GET /api/v1/system/backups` shows it with state `copying` and growing
  `progress.blobsCopied`.
- A completed run's `result.backupId` names a manifest in the target, and `trove backup list`
  shows it.

### R-BAK-010 · One target-writing operation at a time
Priority: **MVP** · Covers: OPS-032

At most one `create` or `delete` run is active per installation (Q-BAK-4).

- The run row is inserted in a transaction that first ends expired runs:

  ```sql
  UPDATE backup_run SET state = 'abandoned', hold = 'none', finished_at = now(),
         error = '{"code": "lease_expired"}'
   WHERE state IN ('starting', 'snapshot', 'copying', 'finalizing', 'pruning')
     AND lease_expires_at < now();
  DELETE FROM backup_blob_pin WHERE run_id IN (SELECT id FROM backup_run WHERE state = 'abandoned');
  INSERT INTO backup_run (…) VALUES (…);
  ```

  A unique violation on `backup_run_one_active` (SQLSTATE `23505`) means another run holds the
  slot:
  - If that run belongs to the same Kubernetes Job as the starting pod (both rows carry the Job name, the
    pod's `job-name` label, which the chart passes as `TROVE_JOB_NAME`), its pod is gone: a Job with
    parallelism 1 runs one pod at a time and starts the retry pod only after the failed one has
    terminated. The command waits as in the next case, whatever the age of the heartbeat (Q-BAK-44).
  - Otherwise, if that run's `heartbeat_at` is younger than 3 × `backup.heartbeat_interval`, it is alive.
    The command exits `3`, names that run's ID, pod, state and start time, and writes nothing to the
    target. This is the answer to a run of another Job and to any run without a Job identity, such as
    a manual run of the command.
  - Otherwise that run's process has stopped renewing: a pod killed without `SIGTERM` handling
    (`OOMKilled`, `kill -9`, a lost node) or a run that lost the database in a failover. The
    command waits until that run's `lease_expires_at` has passed, polling every
    `heartbeat_interval` for at most `lease_duration` + `heartbeat_interval`, and then repeats the
    transaction above, which abandons the old run. Without this rule the Job's retry pod would
    start seconds after the crash, find the slot taken and exit `3`, which the pod failure policy
    turns into a failed Job (R-BAK-012). The Job controller creates the retry pod only after the
    failed one has terminated (`podReplacementPolicy: Failed`, the only value allowed with a pod
    failure policy), and the fencing rule below makes the takeover safe even if the old process
    still runs.
- The database decides, not Kubernetes. The CronJob's `concurrencyPolicy: Forbid` keeps the
  scheduler from starting a second Job, but a Job created by hand (`kubectl create job
  --from=cronjob/…`) bypasses it, and Kubernetes documents that a CronJob can create two Jobs for
  one schedule time ([CronJob](https://kubernetes.io/docs/concepts/workloads/controllers/cron-jobs/)).
- **Fencing.** Every heartbeat updates the row with `WHERE id = $1 AND state = $expected`; a
  heartbeat that matches no row (the run was abandoned) cancels the run, which exits `1` with
  `superseded`. Before the manifest the run executes
  `UPDATE backup_run SET state = 'finalizing' WHERE id = $1 AND state = 'copying' AND
  lease_expires_at > now() + 2 * backup.finalize_timeout`, and writes the manifest only if one
  row changed, with a request deadline of `backup.finalize_timeout`. The same check precedes every
  deletion of the pruning phase. Validation: `backup.finalize_timeout` × 2 < `backup.lease_duration`.
- Read-only commands (`list`, `verify`) are not limited; `verify` records a row with
  `operation = 'verify'`, which the index ignores.

Acceptance criteria:

- Two Jobs created from the CronJob within one second: one exits `0`, the other exits `3` with a
  message naming the first run; the target holds exactly one new `backups/<id>/` directory.
- A run whose pod was deleted with `--grace-period=0 --force`: after `backup.lease_duration`, the
  next run starts, the old row is `abandoned` and its pins are gone.
- `kill -9` of the backup process during `copying`: the Job's retry pod, started at once, waits for
  the lease, the old row becomes `abandoned`, the retry completes, and the Job ends `Complete`. A pod
  of another Job started at the same moment exits `3`.
- A `verify` pod deleted with `--grace-period=0 --force`: once its lease has expired, the next run
  of any operation marks its row `abandoned`.

### R-BAK-011 · Configuration
Priority: **MVP** · Covers: OPS-032

Keys under `backup` in `/etc/trove/trove.yaml` (INV-15); the chart renders them from
`config.backup` ([ha-and-helm.md](ha-and-helm.md) R-HA-031). This page owns their names: the
proposals `backup.skip` and `backup.retention.max_age` in R-HA-031 become
`backup.exclude.remote_caches` and `backup.retention.keep_within` (cross-page). The keys
`backup.recovery_window` and `backup.object_versioning` of [ha-and-helm.md](ha-and-helm.md)
R-HA-023 belong to the platform protection and are unrelated; `backup.revocation_journal`
(R-HA-054) is used by the restore's journal replay (R-BAK-024).

```yaml
backup:
  target:
    endpoint: ""                    # default (proposed): empty = AWS endpoint resolution for region
    region: eu-central-1            # example
    bucket: trove-backups           # example; required by every backup command
    prefix: trove                   # default (proposed); same validation as storage.s3.prefix (R-STOR-030)
    force_path_style: false         # default (proposed)
    allow_insecure_endpoint: false  # default (proposed)
    ca_file: ""                     # default (proposed): system trust store
    storage_class: ""               # default (proposed): bucket default; example: STANDARD_IA
    allow_same_bucket: false        # default (proposed); R-BAK-002
    credentials:                    # same keys and semantics as storage.s3.credentials (R-STOR-031)
      source: default               # default (proposed): default | webIdentity | static
      role_arn: ""                  # example: arn:aws:iam::210987654321:role/trove-backup-writer
      web_identity_token_file: ""
      sts_endpoint: ""
      access_key_id_file: ""        # example: /var/run/secrets/trove/backup-target/access-key-id (set by the chart, R-HA-030)
      secret_access_key_file: ""    # example: /var/run/secrets/trove/backup-target/secret-access-key
      session_token_file: ""
    sse:
      mode: s3                      # default (proposed): s3 | kms | none (R-BAK-026)
      kms_key_id: ""                # example: arn:aws:kms:eu-central-1:210987654321:key/9a8b…
      bucket_key: true              # default (proposed)
  database:
    parallel: 2                     # default (proposed); export connections besides the snapshot connection
    lock_timeout: 60s               # default (proposed); table-lock wait (R-BAK-005)
    migration_wait: 15m             # default (proposed); wait for a running migration's lock (R-BAK-005)
    part_size: 16MiB                # default (proposed); multipart part size of table files
  copy:
    concurrency: 16                 # default (proposed)
    server_side: auto               # default (proposed): auto | always | never
    part_size: 256MiB               # default (proposed); raised so that an object fits 10,000 parts
    check_existing: auto            # default (proposed): auto | always | never
    bytes_per_second: 0             # default (proposed): 0 = unlimited
  compression_level: 3              # default (proposed); zstd level 1–19
  exclude:
    remote_caches: false            # default (proposed); R-BAK-008
  retention:
    keep_last: 7                    # default (proposed); ≥ 1
    keep_within: 0s                 # default (proposed): 0s = count only; example: 720h
    sweep_interval: 168h            # default (proposed); full pool sweep at least this often (R-BAK-017)
  expected_interval: 24h            # default (proposed); drives TroveBackupStale and the API's and UI's overdue mark (R-BAK-014)
  manual_run:                       # read by web replicas for the UI's manual-run command (api-and-ui.md R-API-050, R-UI-048)
    namespace: ""                   # default (proposed): empty; the chart sets the release namespace; example: trove
    cronjob: ""                     # default (proposed): empty; the chart sets <fullname>-backup; example: trove-backup
  lease_duration: 5m                # default (proposed)
  heartbeat_interval: 15s           # default (proposed)
  stall_timeout: 30m                # default (proposed); a run without progress for this long cancels itself (R-BAK-013)
  finalize_timeout: 30s             # default (proposed); 2 × finalize_timeout < lease_duration
  verify_sample: 1000               # default (proposed); pool objects HEAD-checked before each manifest
  run_history: 2160h                # default (proposed); backup_run rows kept 90 days
```

- Validation at the start of every backup command; a violation exits `2` and names the key:
  `keep_last ≥ 1`; `heartbeat_interval × 4 ≤ lease_duration`; `finalize_timeout × 2 <
  lease_duration`; `database.migration_wait < stall_timeout`; `part_size ≥ 5MiB`; target prefix
  rules of R-STOR-030; R-BAK-002's bucket rule.
- The web and worker roles read `backup.expected_interval` (for metrics, the API and the UI) and
  `backup.manual_run` (for the UI's manual-run command), and never the target credentials
  (`backup.target.credentials`, R-BAK-015). The chart renders the target credentials only into
  backup and restore pods.
- `backup.manual_run.namespace` and `backup.manual_run.cronjob` default to empty; the chart sets
  them to the release namespace and `<fullname>-backup` ([ha-and-helm.md](ha-and-helm.md)
  R-HA-030, R-HA-060, cross-page; [api-and-ui.md](api-and-ui.md) Q-API-10). Empty values make the
  UI show placeholders (R-UI-048).
- Every key can be overridden by an environment variable per INV-15, for example
  `TROVE_BACKUP_TARGET_BUCKET`.

Acceptance criteria:

- `backup.heartbeat_interval: 2m` with `lease_duration: 5m` makes `trove backup create` exit `2`
  and name both keys.
- A web pod rendered by the chart has no file and no environment variable carrying the target
  credentials (`kubectl exec … -- ls /var/run/secrets` and `env` checks in the chart test).

### R-BAK-012 · Kubernetes CronJob, manual runs and restore Job
Priority: **MVP** · Covers: OPS-032, OPS-035

[ha-and-helm.md](ha-and-helm.md) R-HA-060 renders the backup CronJob `<fullname>-backup` (with
`backup.enabled: true`; the backup is off by default, Q-BAK-9), the restore Job `<fullname>-restore` (with
`restore.enabled: true`; an ordinary Job, not a Helm hook, [ha-and-helm.md](ha-and-helm.md)
Q-HA-18), their ServiceAccounts, Secrets and scratch volume; R-HA-031 holds the values. This
requirement states what the commands need from those Jobs. A value of R-HA-060 that contradicts
it is listed as a cross-page change.

| Need of the command | Job field | Why |
|---|---|---|
| Retries are decided by exit code | `restartPolicy: Never`; `podFailurePolicy` with `DisruptionTarget` → `Ignore` first, then exit codes `2`, `3`, `4` and `6` → `FailJob` | An eviction must not consume `backoffLimit`; a configuration error, a live concurrent run, a foreign target or a failed restore precondition cannot be fixed by a retry. `75` and `1` are retried. `podFailurePolicy` is stable since Kubernetes 1.31 and requires `restartPolicy: Never` ([Job](https://kubernetes.io/docs/concepts/workloads/controllers/job/)) |
| Room for retries | `backoffLimit` ≥ 2: `backup.backoffLimit` (default 2) for the CronJob's Jobs, `restore.backoffLimit` (default 2) for the restore Job, which takes the same pod failure policy | A crash or a database failover (exit `75`) is followed by a retry that resumes (R-BAK-010, R-BAK-021); a restore retry continues from its anchor (Q-BAK-10) |
| No wall-clock limit shorter than a run | `activeDeadlineSeconds` not rendered by default (`backup.activeDeadlineSeconds` and `restore.activeDeadlineSeconds` 0) | A first full backup of a large installation runs longer than any fixed limit (Q-BAK-2); a hung run cancels itself (R-BAK-013, Q-BAK-11) |
| Overlap | `concurrencyPolicy: Forbid` | First line of R-BAK-010; the database decides |
| Clean shutdown | `terminationGracePeriodSeconds` ≥ 30 | Time to end the run as `interrupted` (below) |
| Scratch | `TMPDIR` on `/tmp`, R-HA-060's only volume of the chart's own (the operator's `extraVolumes` of the Job's values come on top): an `emptyDir` with `backup.tmpSizeLimit` (default 256Mi), in backup and restore pods alike | Table files and blob copies stream; the commands use at most 64 MiB of it. No ReadWriteMany or persistent volume (INV-13) |
| Memory | `limits.memory` ≥ 512 MiB | Bounded by `database.parallel × 2 × database.part_size` plus copy buffers, about 100 MiB with defaults |
| Credentials | Target credentials only in backup and restore pods; no Valkey credentials in either; the key ring only in the restore pod | R-BAK-015; the restore never touches Valkey (R-BAK-022); R-BAK-020 check 3 needs the key ring |
| Autoscaler | Pod annotation `cluster-autoscaler.kubernetes.io/safe-to-evict: "false"` | Only the cluster autoscaler's scale-down reads it: consolidation does not restart a multi-hour run. `kubectl drain` and the Eviction API ignore it, so a drain never waits (below) |

- **Node drain.** No PodDisruptionBudget selects backup or restore pods, and they are evicted
  like any pod without one: a drain never waits for a backup. On `SIGTERM` the run stops copying,
  aborts its open multipart uploads (best effort), sets its row to `interrupted`, deletes its pins,
  sets `hold = 'none'` and exits `75` within the grace period. The Job then starts a new pod,
  whose run starts over with a new snapshot and skips the objects its predecessor already copied
  (`check_existing: auto`). A restore pod that is evicted resumes in the next pod (R-BAK-021).
- **Manual run.** `kubectl -n trove create job --from=cronjob/trove-backup trove-backup-manual-$(date +%s)`.
  The Job copies the CronJob's arguments, so the run records `trigger: schedule`; its `pod` column
  starts with the Job name, which tells manual runs apart. The UI shows this command filled from
  `backup.manual_run` (R-BAK-011) and keeps the Job name within 63 characters
  ([api-and-ui.md](api-and-ui.md) R-UI-048). No API endpoint starts a backup
  ([api-and-ui.md](api-and-ui.md) Q-API-10): the replicas hold no Kubernetes permissions (R-HA-033)
  and no target credentials.
- **Adoption.** The chart value `backup.adoptTarget` (default `false`) adds `--adopt-target` to the
  CronJob's arguments (R-BAK-002, cross-page R-HA-060, R-HA-031); `kubectl create job --from`
  cannot add an argument to a single run.
- **Restore Job** ([ha-and-helm.md](ha-and-helm.md) Q-HA-18). R-HA-060 renders it as an ordinary
  Job, not a `pre-install` or `pre-upgrade` hook, that runs `trove backup restore
  <restore.backupId>`. Helm does not wait for it, so a restore that takes many hours meets no Helm
  or GitOps timeout; the operator waits with `kubectl wait` (R-BAK-021). While `restore.enabled`
  is set:
  - both roles are at zero: the chart refuses a `web` or `worker` replica count above 0 or enabled
    autoscaling ([ha-and-helm.md](ha-and-helm.md) R-HA-029), so nothing writes to the installation
    during the restore;
  - no migration hook Job is rendered, because the restore applies the migrations itself, up to
    the backup's step before the load and the rest after it (R-BAK-021 steps 2 and 5, R-BAK-023);
  - the backup CronJob is rendered suspended.

  The Job runs as its own ServiceAccount `<fullname>-restore`, whose cloud identity holds the
  target reader, blob writer and journal reader rows of R-BAK-015; neither the backup account nor
  the replicas' account fits. It takes `restore.backoffLimit` (default 2) and the backup Job's pod
  failure policy (table above). `restore.skipRevocationJournal` (default `false`) adds
  `--skip-revocation-journal` (R-BAK-024).

Acceptance criteria:

- `helm template` with `backup.enabled: true` and no target bucket fails and names
  `config.backup.target.bucket` and `backup.enabled`; with the default `backup.enabled: false` it
  renders no CronJob and needs no target bucket.
- `kubectl drain --ignore-daemonsets --delete-emptydir-data` of the node running a backup pod
  finishes without waiting for it; the run ends `interrupted`, the eviction does not count against
  `backoffLimit` (a test with `backoffLimit: 0` still ends `Complete`), a new pod starts, its run
  completes, and `TroveBackupFailed` does not fire.
- In a pod of the CronJob, `trove backup create` exits `3` while another live run is active, and
  the Job ends `Failed` without a retry (`podFailurePolicy`).
- `trove backup create` started by hand while `restore_in_progress` is set exits `75` and writes
  nothing to the target.
- Draining the node of the restore pod during R-BAK-021 step 3 ends with the restore Job
  `Complete`, also with `restore.backoffLimit: 0`, and the `kubectl wait` of R-BAK-021 returning
  success.

### R-BAK-013 · Exit codes, interruption and resumption
Priority: **MVP** · Covers: OPS-032

| Code | Meaning | Commands |
|---|---|---|
| `0` | Success; also `completed_with_warnings` | all |
| `1` | Failed: an error the run could not handle (`hold_lost`, `superseded`, `pool_damaged`, store or database error after retries); details in the log and `backup_run.error` | all |
| `2` | Usage or configuration error | all |
| `3` | Refused: another target-writing run is alive (R-BAK-010) | `create`, `delete` |
| `4` | Refused: the target belongs to another installation or incarnation, or an adoption's condition does not hold | `create`, `delete` |
| `5` | Findings: `verify` found missing, damaged or mismatching objects | `verify` |
| `6` | Restore or format precondition not met (R-BAK-020; unknown manifest format) | `restore`, `verify` |
| `75` | Temporary failure: database or target unreachable, lock or migration wait timed out, restore in progress, stalled, interrupted by `SIGTERM`; retrying later is useful | all |

- A failed or interrupted `create` leaves objects under `backups/<id>/` without a manifest and
  possibly new pool objects. Both are harmless: the directory is incomplete and removed by
  retention; the pool objects are reused by the next run or removed by the pool sweep.
- A run never continues another run's snapshot, which ends with its transaction. Resumption is at
  the blob level: the next run copies only what is neither in the previous complete backup nor,
  with `check_existing: auto`, already in the pool.
- A PostgreSQL failover during a run breaks its connections; the run exits `75` and the Job
  retries within `backoffLimit`. The retry waits for the broken run's lease (R-BAK-010). The hold
  row may be lost with asynchronous replication; that affects only liveness (R-BAK-007).
- **Stall detection** (Q-BAK-11). The heartbeat renews the lease only while the run makes
  progress. Progress is any exported row batch, inserted pin batch, confirmed object or part
  copy, listing page or deletion batch, and any wait for a lock within its configured bound. A
  run without progress for `backup.stall_timeout` ends as `failed` with `stalled`, deletes its
  pins and exits `75`, so the Job retries it. A hung run therefore never holds pins, the slot of
  R-BAK-010 or the CronJob's `Forbid` for longer than `stall_timeout`, without a wall-clock limit
  that would also end a healthy long run. `verify` and `restore` apply the same rule.

Acceptance criteria:

- A run killed during `copying` and retried copies, on the retry, only digests whose pool objects
  are missing: the S3 store's request log shows no `CopyObject` or `PutObject` for objects already
  present.
- With `backup.stall_timeout: 1m` and a fault-injected target that accepts connections but never
  answers, the run ends `failed` with `stalled` after about one minute, its pins are gone, and the
  Job's next pod starts.
- Every row of the table is produced by a test that runs the command in the matching situation.

### R-BAK-014 · Run visibility: API, metrics, alerts and audit
Priority: **MVP** · Covers: OPS-032, OPS-012

**API** ([api-and-ui.md](api-and-ui.md) R-API-050; capability `settings:read`,
[authorization.md](authorization.md) R-AUTHZ-010):

| Method and path | Result |
|---|---|
| `GET /api/v1/system/backups` | Runs from `backup_run`, newest first, cursor-paged: `id`, `operation`, `backupId`, `state`, `trigger`, `pod`, `troveVersion`, `target`, `startedAt`, `heartbeatAt`, `leaseExpired`, `finishedAt`, `snapshotAt`, `progress`, `result`, `error` |
| `GET /api/v1/system/backups/{runId}` | One run |
| `GET /api/v1/system/backups/summary` | Status of the backup job: `expectedIntervalSeconds`, `lastSuccessAt`, `overdue`, the last failure, the active run, the stored backups and the retention that produced them (R-BAK-016), the target, the restore origin and the manual-run command (`backup.manual_run`, R-BAK-011) |

The replicas read only `backup_run`, `system_state` and their configuration; they never hold
target credentials, so the API shows runs and what the newest listing run recorded, not the
target's live contents (`trove backup list` does that). `leaseExpired` is true for a run in an
active state whose lease has passed (R-BAK-010). The UI ([api-and-ui.md](api-and-ui.md) R-UI-048)
shows the runs, the last success, the stored backups and an overdue mark after
1.5 × `backup.expected_interval`, the condition of `TroveBackupStale`.

**Metrics** (families for [operations-and-observability.md](operations-and-observability.md)
R-OPS-008). Gauges are read from PostgreSQL by every web and worker replica, so every replica
reports the same value and dashboards use `max` (R-OPS-007). A replica computes all `backup_run`
gauges in one evaluation of single-row reads on that small table (Q-BAK-35) and reuses the result
for 30 s, so the database load of these gauges grows with the replica count only by one cheap
evaluation per replica and interval; no gauge counts `backup_blob_pin`, which holds one row per blob during a first backup. Counters are
exported by the backup pod for the duration of its run on its operations port, for clusters that
scrape Jobs.

| Family | Type | Labels | Meaning |
|---|---|---|---|
| `trove_backup_last_success_timestamp_seconds` | gauge | — | `finished_at` of the newest `completed` or `completed_with_warnings` create run |
| `trove_backup_last_failure_timestamp_seconds` | gauge | — | The newest of: `finished_at` of a `failed` or `abandoned` create run, and `lease_expires_at` of a create run that is still in an active state with an expired lease. `interrupted` runs do not count: a drain is not a failure |
| `trove_backup_expected_interval_seconds` | gauge | — | `backup.expected_interval`, from the replica's configuration |
| `trove_backup_last_duration_seconds` | gauge | — | Duration of the newest successful create run |
| `trove_backup_last_database_bytes`, `trove_backup_last_blob_bytes`, `trove_backup_last_copied_bytes` | gauge | — | From the newest successful run's result |
| `trove_backup_running` | gauge | `operation` (`create`, `delete`), `state` | 1 for the operation and state of the active run with a valid lease, if any |
| `trove_backup_running_seconds` | gauge | — | Age of the active create or delete run with a valid lease, else 0 |
| `trove_backup_gc_hold` | gauge | `mode` (`all`, `pinned`) | 1 while a valid hold of that kind exists |
| `trove_backup_pinned_blobs` | gauge | — | Pins left, from the active run's `progress` (written by its heartbeat) |
| `trove_backup_snapshot_open_seconds` | gauge | — | Age of the active run's open snapshot, else 0 |
| `trove_backup_source_blobs_damaged` | gauge | — | `blobs.damaged` count of the newest successful run |
| `trove_backup_last_verify_timestamp_seconds`, `trove_backup_last_verify_findings` | gauge | — | Newest `verify` run |
| `trove_backup_copied_objects_total`, `trove_backup_copied_bytes_total` | counter | `mode` (`server`, `stream`) | Backup pod only |
| `trove_gc_candidates_held_total` | counter | — | Collection candidates kept by a backup hold (R-BAK-007; storage.md) |

**Alerts** (PrometheusRule of [ha-and-helm.md](ha-and-helm.md) R-HA-045). The names
`TroveBackupStale` and `TroveBackupFailed` are R-HA-045's; this page gives their expressions, and
R-HA-045 takes them over (cross-page):

| Alert | Expression (sketch) | For |
|---|---|---|
| `TroveBackupStale` | `time() - max(trove_backup_last_success_timestamp_seconds) > 1.5 * max(trove_backup_expected_interval_seconds)`, or `trove_backup_last_success_timestamp_seconds` absent for longer than `expected_interval` | 0m |
| `TroveBackupFailed` | `max(trove_backup_last_failure_timestamp_seconds) > max(trove_backup_last_success_timestamp_seconds)` | 0m |
| `TroveBackupRunningLong` | `max(trove_backup_running_seconds) > 0.5 * max(trove_backup_expected_interval_seconds)` | 0m |
| `TroveBackupGlobalHoldLong` | `max(trove_backup_gc_hold{mode="all"}) == 1` | 30m |
| `TroveBackupSourceDamage` | `max(trove_backup_source_blobs_damaged) > 0` | 0m |
| `TroveBackupVerifyFindings` | `max(trove_backup_last_verify_findings) > 0` | 0m |

- One threshold source. `TroveBackupStale` reads `backup.expected_interval` through its gauge, the
  same value that the summary returns as `expectedIntervalSeconds`. The API's `overdue` uses the
  same condition, older than 1.5 × the expected interval ([api-and-ui.md](api-and-ui.md)
  R-API-050), and the UI's overdue mark shows it, so API, UI and alert agree. The chart values of
  R-HA-031 therefore carry no separate staleness threshold.
- `TroveBackupFailed` needs no kube-state-metrics and resolves with the next success. R-HA-045's
  current expression, `kube_job_status_failed{…} > 0`, keeps firing for as long as the failed Job
  object is kept by `failedJobsHistoryLimit`, also after later runs succeeded (cross-page).
- Only with the backup. The chart renders these six alerts only while `backup.enabled` is true
  ([ha-and-helm.md](ha-and-helm.md) R-HA-045, Q-BAK-9): without the CronJob no run ever succeeds,
  and the clause of `TroveBackupStale` for a backup that never succeeded would fire on every
  installation that leaves the backup off.

**Audit events** (catalog of
[operations-and-observability.md](operations-and-observability.md) R-OPS-016, cross-page), actor
`system:backup`, written to the source database by the run:
`system.backup.completed` (`backupId`, `outcome`, `databaseBytes`, `blobsCopied`, `damaged`),
`system.backup.failed` (`backupId`, `code`), `system.backup.deleted` (`backupId`, `reason`:
`retention` or `manual`), `system.backup.target_adopted` (`previousIncarnation`). A restore writes
`system.restored` into the restored database with `source: "backup"`, `backupId` and
`snapshotAt` (R-BAK-021).

Acceptance criteria:

- After a successful run, `trove_backup_last_success_timestamp_seconds` on every web and worker
  replica equals the run's `finished_at` within 30 s plus one scrape.
- With `backup.expected_interval: 1h` and the CronJob suspended, `TroveBackupStale` fires 90
  minutes after the last success (rule test with `promtool test rules`).
- A run ended `interrupted` by a drain, followed by a successful retry, never makes
  `TroveBackupFailed` fire; a run that ends `failed` makes it fire until the next success
  (rule test).
- While a first backup with 400,000 pins runs, no `SELECT count(*) FROM backup_blob_pin` appears in
  `pg_stat_statements`.
- `GET /api/v1/system/backups` without `settings:read` answers `403`.

### R-BAK-015 · Credentials and privileges of the backup role
Priority: **MVP** · Covers: OPS-033

| Principal | Used by | Permissions | Never |
|---|---|---|---|
| Target writer | `create`, `delete` | On `<target bucket>/<prefix>/*`: `s3:GetObject`, `s3:PutObject`, `s3:DeleteObject`, `s3:AbortMultipartUpload`, `s3:ListMultipartUploadParts`; on the bucket: `s3:ListBucket` (conditioned on the prefix), `s3:ListBucketMultipartUploads`; for server-side copy additionally `s3:GetObject` on `<blob bucket>/<blob prefix>/sha256/*`; with SSE-KMS `kms:GenerateDataKey`, `kms:Decrypt` | `s3:DeleteObjectVersion`, `s3:BypassGovernanceRetention`, `s3:PutBucket*`, `s3:PutLifecycleConfiguration` |
| Target reader | `list`, `verify`, `restore` | `s3:GetObject` on the prefix, `s3:ListBucket`; with SSE-KMS `kms:Decrypt` | Any write |
| Source blob reader | `create` (streaming copy) | `s3:GetObject` on `<blob prefix>/sha256/*`; with SSE-KMS `kms:Decrypt` | Any write or delete on the blob bucket |
| Blob writer | `restore` | The runtime permissions of [storage.md](storage.md) R-STOR-031 on the new installation's prefix | — |
| Journal reader | `restore` (R-BAK-024) | `s3:GetObject` on `<source blob prefix>/_journal/*` and `s3:ListBucket` conditioned on that prefix, in the source's blob bucket; with versioning also `s3:ListBucketVersions` and `s3:GetObjectVersion` | Any write |
| Database: backup role | `create`, `delete`, `verify` | `SELECT` on every table of the schema; `INSERT`, `UPDATE`, `DELETE` on `backup_run` and `backup_blob_pin`; `EXECUTE` on `backup_record_event()` (Q-BAK-50) | DDL; DML on other tables; `INSERT` on `audit_event` |
| Database: owner | `restore` | The migration credential of [ha-and-helm.md](ha-and-helm.md) R-HA-014 | — |

- **Separation.** Target credentials exist only in backup and restore pods. Web and worker pods
  never receive them, so a compromised replica can neither read nor delete backups. The backup
  ServiceAccount maps to its own cloud identity under workload identity, and so does the restore
  Job's ServiceAccount `<fullname>-restore`, which holds the target reader, blob writer and journal
  reader rows above ([ha-and-helm.md](ha-and-helm.md) R-HA-060, Q-HA-18): the backup account may
  not write blobs, and the replicas' account has no access to the target. Neither pod holds Valkey
  credentials, and the backup pod holds no key ring (R-BAK-012).
- **Database role.** The backup pod's database credential is `secrets.databaseBackup`
  ([ha-and-helm.md](ha-and-helm.md) R-HA-030, R-HA-060). In two-role mode, the database
  administrator creates the login role behind it once (trove never creates roles, R-HA-014;
  cross-page):

  ```sql
  CREATE ROLE trove_backup LOGIN PASSWORD :'backup_password';
  GRANT CONNECT ON DATABASE trove TO trove_backup;
  GRANT USAGE ON SCHEMA trove TO trove_backup;
  ```

  When `database.backup_role` names it, `trove migrate` (run as the owner) grants the rest after
  applying the steps, idempotently on every run, so tables added by later migrations are covered:

  ```sql
  GRANT SELECT ON ALL TABLES IN SCHEMA trove TO trove_backup;
  GRANT SELECT ON ALL SEQUENCES IN SCHEMA trove TO trove_backup;
  GRANT INSERT, UPDATE, DELETE ON backup_run, backup_blob_pin TO trove_backup;
  GRANT EXECUTE ON FUNCTION backup_record_event(text, text, text, text, jsonb) TO trove_backup;
  ALTER DEFAULT PRIVILEGES IN SCHEMA trove GRANT SELECT ON TABLES TO trove_backup;
  ```

  The backup writes its audit events through `backup_record_event()`, a `SECURITY DEFINER` function of the schema owner that
  accepts the four events of R-BAK-014 with the outcome `success` or `failure` and writes them as the actor `system:backup`
  (Q-BAK-50); the login has no `INSERT` on `audit_event`, so a compromised backup pod cannot write any other event. The
  migration records the role it granted (`system_state` key `backup_role`) and, when `database.backup_role` names another
  role or none, revokes from the former role exactly what it granted.

  The default privileges cover tables the owner creates between two migration runs, such as the
  monthly `audit_event` partitions of `audit_maintain_partitions()`
  ([operations-and-observability.md](operations-and-observability.md) R-OPS-015), so the export's
  `LOCK TABLE`, which also locks every partition, never meets a partition the role cannot read.
  `SELECT` on the partitioned parent suffices for reading `audit_event`; `LOCK TABLE … IN ACCESS
  SHARE MODE` needs only `SELECT` (R-BAK-005). `create` checks these grants at start
  (`has_table_privilege`) and exits `2` naming a missing one. In single-role mode, or when no
  backup credential is configured, the run uses the runtime credential and logs a warning.
  Restore uses the owner credential, because it runs migrations and disables triggers
  (R-BAK-021).
- **Hardening of the target** (recommended, documented, not enforced): a bucket in another account
  or project than the blob bucket; S3 Versioning with Object Lock default retention of at least
  `keep_last × expected_interval`; a lifecycle rule that expires noncurrent versions after that.
  With Object Lock a simple `DELETE` from the prune only adds a delete marker, and a locked
  version cannot be deleted without `s3:BypassGovernanceRetention`
  ([Object Lock](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock.html)), so a
  compromised backup role cannot destroy backups within the lock period. Use
  `backup.copy.check_existing: always` there, so re-copies do not create extra versions. A
  lifecycle rule `AbortIncompleteMultipartUpload` (7 days, *example*) removes the parts of uploads
  that an evicted or killed run could not abort.

Acceptance criteria:

- With an IAM policy limited to the target-writer row, `create`, prune and `delete` succeed, and
  `aws s3api delete-object --version-id …` with the same credential fails with `AccessDenied`.
- With the `trove_backup` role, `create` succeeds and `psql -U trove_backup -c 'DELETE FROM
  trove.items'` fails with `42501`.
- A restore started with the `trove_backup` credential exits `6` and names the missing owner
  privilege.

## Retention

### R-BAK-016 · Retention and deletion of backups
Priority: **MVP** · Covers: OPS-032

- **Policy.** A complete backup is deleted when it is not among the `backup.retention.keep_last`
  newest complete backups of the target *and* is older than `backup.retention.keep_within`. The
  newest complete backup is never deleted by retention. Backups of earlier incarnations in the
  same target are subject to the same policy.
- **When.** At the end of every successful `create` (state `pruning`, unless `--no-prune`), and by
  `trove backup prune`. `trove backup delete <id> [--yes]` deletes one backup regardless of the
  policy; deleting the only complete backup needs `--allow-last`. Both are target-writing runs
  (R-BAK-010).
- **Order.** A backup is deleted manifest first, so it stops being a backup at once; then its other
  objects. Incomplete directories are deleted when they are older than the newest complete backup
  and do not belong to the active run.
- **Pool.** After any deletion, the pool sweep of R-BAK-017 runs in the same run.
- **Reporting.** The run's result carries `backupsDeleted` (IDs), `incompleteDeleted`,
  `poolObjectsDeleted` and `poolBytesReclaimed`; each deleted backup writes
  `system.backup.deleted`. Every create or delete run that lists the target also records in
  `result`:
  - `retention: {keepLast, keepWithinSeconds}`, as applied;
  - `retained`, the complete backups left after its deletions, each with `backupId`, `snapshotAt`,
    `troveVersion`, `schemaStep`, `databaseBytes`, `blobCount`, `blobBytes` and `damaged`.

  [api-and-ui.md](api-and-ui.md) R-API-050 shows the newest such record as `storedBackups` and
  `retention` (Q-API-11). A `create` with `--no-prune` skips the pruning phase, takes no retention
  listing and records neither field.

Acceptance criteria:

- With `keep_last: 2` and three complete backups A < B < C sharing blobs, the run that creates C
  deletes A; `trove backup verify B` and `verify C` report no findings; a blob referenced only by
  A is gone from the pool.
- `trove backup delete <newest>` without `--allow-last` on a target with one backup exits `2`.

### R-BAK-017 · Pool sweep
Priority: **MVP** · Covers: OPS-032

Deleting old backups MUST NOT break the shared blobs of newer ones.

1. After the deletions of R-BAK-016, the run lists the complete backups again and merges their
   blob lists (minus their `blobs.damaged`) into one sorted stream: the *keep set*.
2. It lists `pool/sha256/` with `ListObjectsV2`, which returns keys in lexicographic order for
   general purpose buckets ([ListObjectsV2](https://docs.aws.amazon.com/AmazonS3/latest/API/API_ListObjectsV2.html)),
   so the listing and the keep set merge in one pass.
3. A pool object whose digest is not in the keep set is deleted (`DeleteObjects`, up to 1,000 keys
   per request, or `DeleteObject`).
4. A key that arrives out of order, or does not match the pool layout, stops the sweep: nothing
   more is deleted, foreign keys are reported, and the run ends `completed_with_warnings`.
   Directory buckets, which do not list in order, are therefore not supported as targets.

- The sweep runs when the run deleted a backup or an incomplete directory, and otherwise at least
  every `backup.retention.sweep_interval`, so pool objects left by failed runs are reclaimed.
- **Why it is safe.** A pool object is deleted only if no remaining complete backup lists it. Every
  newer backup's list is part of the keep set, and manifests are immutable, so none of its objects
  is deleted. No `create` runs concurrently (R-BAK-010), so no run can rely on an object the sweep
  is about to delete. The keep set is read after the deletions of R-BAK-016, so a deleted backup
  protects nothing.

Acceptance criteria:

- A pool object written by hand under `pool/sha256/` with a valid key and no backup listing it is
  deleted by the next sweep; an object named `pool/sha256/junk` stops the sweep and is reported.
- The property test of R-BAK-007 extended with retention runs never produces a complete backup
  whose listed, undamaged digest is missing from the pool.

## Inspection

### R-BAK-018 · Listing backups
Priority: **MVP** · Covers: OPS-032

`trove backup list [--all] [--json]` reads the target (target-reader credential) and prints one
line per backup, newest first: ID, state (`complete`, or `incomplete` with `--all`), snapshot time,
trove version, schema step, database bytes, referenced blob count and bytes, blobs copied by that
run, options, key IDs, incarnation, and whether the backup is damaged (`blobs.damaged` not
empty). It writes nothing. `--json` prints the same as an array of objects.

Acceptance criteria:

- After two runs, `trove backup list --json | jq length` is 2 and the newest entry's `schemaStep`
  equals `trove migrate status`'s highest applied step.

### R-BAK-019 · Verifying backups
Priority: **MVP** · Covers: OPS-033

`trove backup verify <id> | --latest | --all [--deep] [--bytes-per-second N]` checks backups
without changing them:

- the manifest parses, has a known `format` and names the target's installation;
- every table file and the blob list exist, and their SHA-256 and sizes match the manifest
  (downloaded and hashed; table files are small next to the blobs);
- every digest of the blob list that is not in `blobs.damaged` has a pool object of the listed
  size (`HeadObject`);
- with `--deep`, every such pool object is downloaded and its SHA-256 recomputed, throttled to
  `--bytes-per-second`.

Findings are printed one per line (`missing`, `size_mismatch`, `digest_mismatch`,
`file_checksum_mismatch`) and stored in a `backup_run` row with `operation = 'verify'` when the
source database is reachable; the command exits `5` with findings, `0` without. Scheduled
verification is P1 (see [Deferred](#deferred)).

Acceptance criteria:

- `aws s3 rm` of one pool object listed by the newest backup: `trove backup verify --latest`
  exits `5` and prints `missing <digest>`; `trove_backup_last_verify_findings` reports 1.
- Overwriting a pool object with other bytes of the same length: `verify` exits `0`, `verify
  --deep` exits `5` with `digest_mismatch`.

## Restore

### R-BAK-020 · Restore preconditions
Priority: **MVP** · Covers: OPS-035

`trove backup restore <backup-id> [--check] [--skip-revocation-journal]
[--journal-credentials-file <path>]` restores into an empty installation (Q-BAK-7), and it resumes
an interrupted restore of the same backup on its own (Q-BAK-10). It first takes the migration lock
`pg_advisory_lock(3, 0)` on its session connection (R-BAK-021) and then, before writing anything
but check 7's probe object, MUST check, and exit `6` naming the first failed check:

1. The manifest exists, has a known `format`, and every file it names exists with the recorded
   size (`HeadObject`).
2. The binary embeds every migration step of `manifest.schema.steps` with the same SHA-256
   (R-BAK-023). Otherwise the message names `manifest.trove.version` as a release that can
   restore it.
3. The configured key ring holds every key ID of `manifest.secrets.keyIds`, and it opens every
   canary row of the backup's `secret_key_canary` file (read and decrypted before any load). The
   message is `key ring does not match backup: missing key <id>`, as R-ARCH-021's start-up check
   words it.
4. The database is empty: schema `database.schema` exists (R-HA-014; a missing schema fails this check) and contains no relation.
   **Resumption:** a schema that carries the restore anchor of the same backup ID (R-BAK-021
   step 2) is accepted whatever it contains. An anchor of another backup ID refuses and names both
   IDs.
5. The blob prefix is empty: `ListObjectsV2` of `<storage.s3.prefix>/` returns no key outside
   `_probe/`. When check 4 accepted an anchor, objects under `sha256/` are accepted too.
6. The database server's version is supported ([ha-and-helm.md](ha-and-helm.md) R-HA-013), and the
   credential owns the schema (owner credential).
7. The target and the blob store are reachable with the configured credentials: one `HeadObject`
   on the target, and one `PutObject` plus `DeleteObject` of `<prefix>/_probe/<pod>` on the blob
   store. A restore writes nothing to the target (Q-BAK-33).
8. **Revocation journal.** If `manifest.source.revocationJournal` is true, the journal under
   `<manifest.source.storage.prefix>/_journal/revocations/` in the source's blob bucket can be
   listed, with the blob-store credentials or those of `--journal-credentials-file` (journal
   reader, R-BAK-015). Otherwise the restore refuses unless `--skip-revocation-journal` is given
   (R-BAK-024).

`--check` runs these checks (it takes the lock too) and exits `0` or `6`, without changes.

Acceptance criteria:

- Restoring into a database with one row in `repositories` exits `6` and names check 4.
- Restoring with a key ring that lacks the backup's key ID exits `6` with the message above, and
  the database is still empty.
- A blob bucket with one foreign object under the prefix makes the restore exit `6` (check 5).
- A database anchored by a restore of another backup ID makes the restore exit `6` and name both
  IDs.
- With the source's journal prefix unreachable, the restore exits `6` naming check 8; with
  `--skip-revocation-journal` it proceeds, prints H-37's warning and records the skip in
  `system.restored`.

### R-BAK-021 · Restore procedure
Priority: **MVP** · Covers: OPS-035, OPS-039

Operator steps (Helm; the restore Job is the ordinary Job of [ha-and-helm.md](ha-and-helm.md)
R-HA-060, Q-HA-18):

```shell
# new, empty database (R-HA-014 provisioning) and an empty blob prefix; Secrets in place (R-BAK-028)
helm repo add trove https://guided-traffic.github.io/trove/
# <v>: the chart version to install; example 0.1.0
helm install trove trove/trove -n trove -f values.yaml --version <v> \
  --set web.replicaCount=0 --set web.autoscaling.enabled=false \
  --set worker.replicaCount=0 --set worker.autoscaling.enabled=false \
  --set highAvailability.enforce=false \
  --set restore.enabled=true --set restore.backupId=20261028T020000Z-7f3a   # example ID
kubectl -n trove wait --for=condition=complete job/trove-restore --timeout=24h
helm repo update trove
helm upgrade trove trove/trove -n trove -f values.yaml --version <v>   # normal values
```

- The first command renders the restore Job with both roles at zero, no migration Job and a
  suspended backup CronJob (R-BAK-012). Helm does not wait for the Job, so no Helm or GitOps
  timeout limits the restore. `kubectl wait --timeout` bounds only how long the operator's shell
  waits (the drill measures the restore's duration, R-BAK-029 scenario 9); when it passes, the Job
  keeps running and the same `kubectl wait` is repeated.
- The final `helm upgrade` with normal values turns `restore.enabled` off. It renders the migration
  Job again, which finds the schema at the binary's step (step 5) and applies nothing, and starts
  both roles on the restored data.
- **A Job that failed for good** (its retries used up, or a `FailJob` exit of R-BAK-012) is
  continued after the cause is fixed: `kubectl -n trove delete job trove-restore`, then the
  `helm upgrade` with the restore values (both roles at zero, `restore.enabled`) again. Helm creates
  the Job anew, and its pod resumes the restore (below).
- **Nothing overtakes the restore.** While `restore.enabled` is set, the chart refuses a `web` or
  `worker` replica count above zero or enabled autoscaling ([ha-and-helm.md](ha-and-helm.md)
  R-HA-029, R-HA-060), so an upgrade that would start the roles fails at rendering. Turning
  `restore.enabled` off before the Job completes deletes the Job and renders the migration Job,
  and `trove migrate` refuses an anchored database ([ha-and-helm.md](ha-and-helm.md) R-HA-019), so
  that upgrade fails too and the unfinished restore is resumed instead of being migrated past.

What `trove backup restore` does:

1. **Lock and checks.** Takes `pg_advisory_lock(3, 0)` on its session connection and holds it until
   step 9 ends; the migration runner runs on that connection, so it re-enters the lock. A second
   restore process (an old pod still terminating while the Job's next pod, or a Job created anew,
   starts) and a `trove migrate` started meanwhile wait for it; a restore waits up to
   `backup.database.migration_wait` and then exits `75`, and `trove migrate` waits up to
   `migrate.advisory_lock_wait` and refuses a database that still carries the anchor when it gets
   the lock ([ha-and-helm.md](ha-and-helm.md) R-HA-019). On `SIGTERM` a restore rolls back an
   open step-4 transaction, closes its connections, which releases the lock, and exits `75`. Then
   the checks of R-BAK-020. It writes nothing until step 2 starts.
2. **Anchor and schema.** Sets `COMMENT ON SCHEMA <database.schema> IS 'trove-restore <backup-id>'`,
   the anchor that makes the database this restore's from its first write on. Applies the embedded
   migrations up to the backup's step through the runner's internal mode (R-BAK-023). The runner's
   final `schema_state` update and the insert of `system_state` key `restore_in_progress` =
   `{"backupId": …, "phase": "blobs", "startedAt": …}` commit in one transaction, so a replica's
   schema check never finds a servable schema without the marker
   ([architecture.md](architecture.md) R-ARCH-024 step 3, R-ARCH-026's restore mode).
3. **Blobs.** Copies every digest of the blob list that is not in `blobs.damaged` from the pool to
   `<storage.s3.prefix>/sha256/<h0h1>/<h2h3>/<hex>`, server-side when the credentials allow it,
   otherwise streamed and hashed (R-BAK-006 rules, `backup.copy.*` settings). Existing objects of
   the right size are skipped, so a resumed restore copies only what is missing. A missing pool
   object stops the restore with exit `1` and the digest; `verify` would have reported it. Blobs
   come first, so that the load in step 4 commits references only to objects that exist
   (R-STOR-005's invariant).
4. **Load,** in one transaction as the owner, skipped when the marker's phase is already `loaded`
   or later:
   - `ALTER TABLE … DISABLE TRIGGER USER` on every table, so that the guard, release, usage-delta
     and audit triggers do not fire again for effects the data already contains;
   - one `TRUNCATE` of every table except `schema_migration` and `schema_state`. The migrations of
     step 2 seed rows (for example `system_state` `installation_id` and the `cluster_epoch` rows of
     [architecture.md](architecture.md) R-ARCH-018, the `authz` row among them), and the backup holds
     these tables as of the snapshot; loading on top of the seeds would fail on their primary
     keys. Transient tables are included, because `TRUNCATE` refuses a table that a table outside
     the statement references;
   - the audit partitions that the exported rows need are created through the owner's partition
     function ([operations-and-observability.md](operations-and-observability.md) R-OPS-015,
     cross-page);
   - `COPY <table> (<manifest columns>) FROM STDIN` per table in foreign-key order computed from
     the catalog; `CycleBreak` columns (R-BAK-004) are loaded as `NULL` and set afterwards from a
     temporary table; `COPY FROM` writes identity columns as given
     ([COPY](https://www.postgresql.org/docs/current/sql-copy.html));
   - every table's column list is compared with the manifest's; a difference aborts with exit `6`;
   - `setval` for every sequence from the manifest;
   - blob rows of `blobs.damaged` get `content_status = 'missing'`;
   - every table's row count is compared with the manifest's `rows` and logged;
   - `ENABLE TRIGGER USER`; `restore_in_progress` is inserted again with phase `loaded` (the
     `TRUNCATE` removed it); `COMMIT`.
5. **Schema forward.** Applies the binary's remaining migrations (R-BAK-023), so that the code of
   steps 6 to 8 runs against the schema it was written for.
6. **Fix-ups** of R-BAK-022, in one transaction that also sets the marker's phase to `fixed`, so a
   resumed restore never applies them twice.
7. **Remote caches.** If `options.excludeRemoteCaches`, every remote repository's cache is emptied
   as R-BAK-008 describes.
8. **Revocation journal and verification.** Replays the revocation journal (R-BAK-024) unless
   `--skip-revocation-journal` was given. Then: every `live` blob row with `content_status = 'ok'`
   has a copied object (a join against the copied digests); the canary check of R-ARCH-021
   passes; `trove migrate status` shows no pending step. A failure exits `1` and leaves the anchor
   and `restore_in_progress` in place.
9. **Finish,** in one transaction: deletes `restore_in_progress`, sets the `system_state` key
   `restored_at` to the transaction's time (`now()`, R-BAK-022), removes the anchor
   (`COMMENT ON SCHEMA … IS NULL`) and writes `system.restored` (R-BAK-014); then releases the
   lock. It prints the summary and the next steps: if this installation shall back up into the
   old target, set `backup.adoptTarget: true` for its CronJob (R-BAK-002); review the access
   changes made after `snapshotAt` (H-37, R-BAK-024) and the versions the journal replay reports
   as `revived` (R-BAK-024).

- **No serving during a restore.** While `system_state` holds `restore_in_progress`, a `web` or
  `worker` replica that starts against the database waits at its schema check, stays not ready
  and claims no jobs ([architecture.md](architecture.md) R-ARCH-024 step 3;
  [operations-and-observability.md](operations-and-observability.md) R-OPS-001). Before the marker
  exists, the schema is not servable, so the replica waits as well. The chart keeps replicas away
  during a restore anyway: it refuses a replica count above zero while `restore.enabled` is set
  ([ha-and-helm.md](ha-and-helm.md) R-HA-060), and `trove migrate` refuses an anchored database
  (R-HA-019), so neither a replica nor a migration overtakes the restore. The guard covers
  [ha-and-helm.md](ha-and-helm.md) R-HA-025's path and pods started by hand.
- **Resumption** (Q-BAK-10). A restore that ends without finishing (evicted by a drain, killed,
  its database connection lost in a failover, exit `1`) leaves the anchor. The Job's next pod
  (R-BAK-012), or the same command run again, resumes: check 4 accepts the anchor, the runner
  applies only missing steps (each step commits with its `schema_migration` row, R-ARCH-026),
  step 3 skips objects already copied, step 4 is one transaction and is skipped once committed,
  step 6 is guarded by the phase `fixed`, and steps 5, 7 and 8 are idempotent. There is no flag. A pod that finds no anchor and no marker, and `restored_from` naming this very backup, ends with exit `0` and "already restored" (it was killed after step 9's commit, Q-BAK-53). A
  restore of another backup into the same database refuses (check 4), and so does every restore
  after step 9, which removes the anchor. To start over instead, the operator drops and recreates
  the schema and empties the blob prefix.
- **What to do with the old installation.** If it still runs and writes backups to the target
  being restored from, suspend its backup CronJob (`backup.suspend: true`) before the restore
  starts: its retention could delete the backup being restored, and the restore would then stop
  at a missing pool object. Stop its web and worker Deployments before clients are switched over,
  so that no client writes to it after the cut-over. Keep its database and bucket until the
  restored installation is verified, then remove them as [ha-and-helm.md](ha-and-helm.md) R-HA-048
  describes. Never let the restored installation use the old blob prefix in the MVP (R-BAK-020
  check 5; P1: R-BAK-025). A shared Valkey is harmless, because the restored installation uses a
  new keyspace (R-BAK-022).
- **What clients see.** With the same `server.base_url`, tokens that existed at `snapshotAt` keep
  working unless the journal replay revokes them; tokens created later answer `401` and must be
  issued again; UI users sign in again; packages published after `snapshotAt` are absent. For
  NuGet, the journal replay reserves a lost version's number for its journaled bytes: the version
  can be published again only with its identical `.nupkg`, and any other build takes a new version
  number ([formats/nuget.md](formats/nuget.md) R-NUGET-033). NuGet versions deleted after
  `snapshotAt` are back; the replay reports them as `revived` and does not delete them, so the
  administrator must review that list and delete what has to stay deleted
  ([formats/nuget.md](formats/nuget.md) H-50). Clients that cached package documents after
  `snapshotAt` revalidate and get the restored state (R-BAK-022).
- A restore is also the migration path to a new cluster, a new PostgreSQL major version or another
  S3 store (OPS-039): the blob keys depend only on content, and the database part is version
  independent (R-BAK-023).
- [ha-and-helm.md](ha-and-helm.md) R-HA-025's `post-restore` belongs to platform PITR restores.
  This procedure performs the equivalent clean-up itself (R-BAK-022, R-BAK-024), except the
  deletion pause, which an empty blob prefix holding exactly the referenced blobs does not need.
  It needs no Valkey flush either: the new keyspace makes old entries unreachable (R-BAK-022,
  [ha-and-helm.md](ha-and-helm.md) R-HA-057 rule 7).

Acceptance criteria:

- The commands above restore the newest backup of the local profile into a second namespace with a
  fresh CloudNativePG cluster and a fresh SeaweedFS bucket; afterwards `npm view` and `dotnet
  package search` through the restored installation list exactly the versions in the manifest's
  snapshot.
- `kill -9` of the restore pod during step 2, again during step 3 and again during step 4, each
  time followed by the Job's next pod, ends with the same result as an uninterrupted restore.
- A second restore pod started while the first one runs waits on the migration lock, writes
  nothing, and exits `75` after `backup.database.migration_wait`; started after the first one has
  finished, it refuses with exit `6` (check 4, no anchor).
- A `web` replica started by hand against the database during step 2 becomes ready only after
  step 9, and `SELECT count(*) FROM replica` at the end of step 4 is 0.

### R-BAK-022 · State a restore rebuilds or invalidates
Priority: **MVP** · Covers: trove-only

A restore moves the installation back to `snapshotAt`. Derived state that outlives the old
database MUST NOT be mistaken for restored state (Q-BAK-5). Transient tables are empty after the
load; the other fix-ups run in one transaction after the schema is brought forward (R-BAK-021
step 6), so that columns added by later migrations are covered too. `restored_at` and the audit
event belong to the finishing transaction of step 9 instead, as the table states:

| State | Fix-up |
|---|---|
| Transient tables | Empty (R-BAK-004): every UI session and login flow ends; upload sessions are gone |
| `job` | Rows in `running` return to `queued` with `locked_by` and `locked_until` cleared and `attempt` unchanged; rows in `cancelling` become `cancelled`, because their cancellation was requested before the snapshot; scheduled slots keep their rows, so no slot runs twice |
| `system_state` `keyspace_id` | New random UUID. Every Valkey key and channel carries its first 12 hex digits as `<ks>` ([architecture.md](architecture.md) R-ARCH-030), so entries written by the old installation, including a shared Valkey's, are never read. Replicas read it once at start-up, and they stay unready while `restore_in_progress` is set ([architecture.md](architecture.md) R-ARCH-024 step 3), so none runs with the old keyspace after a restore |
| `system_state` `incarnation` | New random UUID (R-BAK-002) |
| `system_state` `restored_from` | `{"installationId", "incarnation", "backupId", "snapshotAt"}` of the backup; the condition of target adoption (R-BAK-002) |
| `system_state` `installation_id` | Kept: the installation keeps its identity for webhooks, support bundles and the target marker |
| Revision columns | Every column registered with `meta.RegisterRevisionColumn` grows by 2^32 |
| Storage usage counters | `storage_usage_recount()` recomputes `repository_usage` and `storage_usage_totals` from `items` and `blobs` and empties `storage_usage_deltas` ([storage.md](storage.md) R-STOR-003, R-STOR-047), because the `blobs` class filter drops rows that the exported physical totals still count |
| `mirror_state` (from phase 6b) | The pass in progress (`pass_*` columns), `pass_requested_*`, `rate_limited_until` and `bound_reached` are cleared and `next_pass_at` set to the restore time, so the first pass after the restore is due at once and continues from the last complete pass's cursor; a restore without remote caches (R-BAK-008) also clears `feed_cursor`, `last_full_pass_at` and `patterns_fingerprint`, so that pass is full ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-058, Q-REPO-147) |
| `system_state` `restored_at` | Set in the finishing transaction of R-BAK-021 step 9, not with the step-6 fix-ups, so it records when the restore finished and is in place before any replica starts serving. Readers: single-use CI issuers refuse subject tokens whose `iat` is not later than it plus `clock_skew` ([gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-018); npm serves a cached remote tarball only after its package document was validated after it ([formats/npm.md](formats/npm.md) R-NPM-025, Q-NPM-13) |
| Audit | `system.restored` is written when the restore finishes (R-BAK-021 step 9) |

- **Revision columns.** Values that clients hold as `ETag` or send as `If-Match` would otherwise
  repeat with different content after the restore: a package document whose `rev_seq` was 6 in
  the old installation after the snapshot, and is 6 again after two publishes in the restored one,
  would answer a stale client with `304`. Adding 2^32 puts every restored value above anything the
  old installation can have issued. Initial registry:
  - `packages.rev_seq` ([storage.md](storage.md) R-STOR-003; registered by
    [architecture.md](architecture.md) R-ARCH-009);
  - `cluster_epoch.epoch` ([architecture.md](architecture.md) R-ARCH-018), which also carries the
    authorization epoch, the `authz` row ([authorization.md](authorization.md) R-AUTHZ-028);
  - `repositories.config_version` and `repositories.claims_version`
    ([repositories-and-proxy.md](repositories-and-proxy.md)), and the revision of the
    proxy-offline setting (same page);
  - `authz_policy.version` and `project.version` ([authorization.md](authorization.md)
    R-AUTHZ-028);
  - `ci_issuers.version`, `ci_issuers.mappings_version`, `ci_mappings.version` and
    `ci_delegations.version` ([gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-001).

  A unit test fails on a `bigint` column named `rev_seq`, `version`, `*_version`, `revision` or
  `epoch` that is not registered.
- Blob ETags are digests and need no fix-up. Remote documents' ETags derive from stored upstream
  digests and need none either.
- Valkey needs no access during a restore: nothing is flushed, the new keyspace simply starts
  empty, and stale keys of the old keyspace expire by their TTLs, because every key carries an
  expiry (R-ARCH-030). The restore Job therefore holds no Valkey credentials.

Acceptance criteria:

- An npm client fetches a package document from the old installation after `snapshotAt` and keeps
  its `ETag`; after the restore and one publish to the same package, the same conditional request
  answers `200` with the restored document, not `304`.
- The restored installation, pointed at the old installation's Valkey, never reads a key written
  by the old installation (`MONITOR` in the test shows only keys with the new keyspace ID).
- A job that was `running` at the snapshot runs again after the restore and finishes; a job that
  was `cancelling` is `cancelled` and does not run; a scheduled slot completed before the snapshot
  is not enqueued again.

### R-BAK-023 · Schema and PostgreSQL version compatibility on restore
Priority: **MVP** · Covers: OPS-035, OPS-039

- A backup is restored by any trove binary that embeds every migration step of the backup with
  the same file SHA-256 (Q-BAK-8). The restore applies the steps up to the backup's step, loads
  the data into exactly the schema it was exported from, then applies the binary's later steps,
  expand and contract alike: no replica is registered, so R-HA-018's contract guard has nothing to
  wait for. Background data migrations that were running at the snapshot resume from their
  restored rows (R-HA-022).
- The migration runner MUST offer this "up to step N" mode to the restore; it is the runner's
  restore mode of [architecture.md](architecture.md) R-ARCH-026. It runs on a connection that
  already holds the migration lock, and it lets the caller add statements to the transaction of
  its final `schema_state` update (R-BAK-021 step 2). It is not exposed as a CLI flag of
  `trove migrate`.
- A binary older than the backup's schema step, or one whose migration files differ from the
  backup's checksums, refuses (R-BAK-020 check 2). From the first stable release on, applied
  migration files are checksummed and never edited (R-HA-018, R-ARCH-026); this page adds that
  they are not squashed or removed within a major, so every later release of a major can restore
  every earlier backup of it. A major release that drops old steps names, in its release notes,
  the last release that restores older backups. Before the first stable release a migration file
  may be edited or removed ([Q-ARCH-340](architecture.md#q-arch-340--no-backward-compatibility-until-the-first-stable-release)), so a later build restores an earlier
  backup only while the steps up to the backup's step are unchanged.
- PostgreSQL: the table files are `COPY` text and the schema is built by trove's migrations, so a
  backup taken on one supported major version restores onto any other supported one
  ([ha-and-helm.md](ha-and-helm.md) R-HA-013).

Acceptance criteria:

- A backup taken by release N−1 is restored by release N, and the restored installation passes
  the npm and NuGet client suites.
- A backup taken on PostgreSQL 16 is restored onto PostgreSQL 18 (CI matrix of R-HA-013).
- A binary built with one migration file edited refuses the restore with exit `6` and names the
  step.

### R-BAK-024 · Revocation journal replay after a restore
Priority: **MVP** · Covers: trove-only

A restore undoes every access removal made after `snapshotAt`, as a PITR restore does (H-37). The
revocation journal of [ha-and-helm.md](ha-and-helm.md) R-HA-054 is MVP, and R-HA-054 states that a
restore of a trove backup replays it, so every restore replays it at MVP; a backup restore must not
be weaker than a PITR restore.

- In R-BAK-021 step 8, before the verification, `trove backup restore` replays the journal
  entries that are newer than `manifest.snapshotAt − backup.revocation_journal.replay_margin`,
  with R-HA-054's replay rules and output, and writes `system.restore.journal_replayed` with the
  counts.
- **Where the journal is.** Under `<manifest.source.storage.prefix>/_journal/revocations/` in the
  source's blob bucket (R-BAK-003), not in the backup: it lives outside the database on purpose,
  and entries newer than the snapshot exist only there. The restore reads it with the journal
  reader of R-BAK-015: the blob-store credentials when they reach the source bucket, otherwise
  `--journal-credentials-file`. With bucket versioning and a credential that may list versions,
  delete markers do not hide entries (R-HA-054).
- **Version entries.** Besides access removals, the journal carries the version entries that
  format pages flag, and the replay applies them by each format's rules: NuGet's publishes and
  deletions on local repositories reserve lost version numbers for their journaled bytes or
  report a version as `revived` ([formats/nuget.md](formats/nuget.md) R-NUGET-033), and npm's
  unpublishes withhold the restored version ([formats/npm.md](formats/npm.md) R-NPM-047). Replay of
  version entries only restricts; it deletes nothing.
- **Repository entries.** The narrowings of a repository's content and its claims made after the snapshot
  ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-061) are re-applied to the restored repositories; where a
  part could widen, the repository is disabled and listed `suspended`. A `vuln-rescan` that such an entry enqueues runs when
  the workers start, after the restore.
- Replay is idempotent (R-HA-054), so a resumed restore may replay again.
- **Journal unavailable.** R-BAK-020 check 8 refuses the restore. `--skip-revocation-journal`
  (chart value `restore.skipRevocationJournal`) is for the disaster in which the source bucket is
  gone. The restore then prints H-37's warning and the manual procedure, together with the warning
  that NuGet version numbers published after the snapshot are not reserved
  ([formats/nuget.md](formats/nuget.md) R-NUGET-033 criterion, H-50), and `system.restored`
  records `journal: "skipped"`.

Acceptance criteria:

- A token revoked after the backup's snapshot is rejected with `401` after a restore, and the
  output lists `token.revoked … applied`.
- A restore whose Job is killed during the replay and resumed lists each entry once as `applied`
  and, on the second pass, as `already in effect`; the token stays revoked.
- A pending identity link removed, an exclude pattern added and a claim created after the snapshot are in effect after the
  restore, and a second replay reports each `already in effect`.

### R-BAK-025 · Restore onto an existing blob store
Priority: **P1** · Covers: OPS-039

`trove backup restore <id> --existing-blobs reuse` accepts a non-empty blob prefix, typically the
old installation's bucket after its database was lost. The database must still be empty.

- For each listed digest, an object of the right size at its key is kept (with `--deep`, also
  re-hashed); others are copied from the pool.
- The restore sets the deletion pause of [ha-and-helm.md](ha-and-helm.md) R-HA-025 and prints
  that the orphan sweep ([storage.md](storage.md) R-STOR-026, P1) removes objects the restored
  database does not reference once deletions are resumed.
- The old installation MUST be stopped first: its garbage collection would delete objects the
  restored database references. The restore cannot detect a running installation on another
  database and says so in its output.

Acceptance criteria:

- After the old installation's database is dropped, `--existing-blobs reuse` against its bucket
  copies no pool object for blobs already present, and a following orphan sweep removes objects
  written after `snapshotAt`.

## Encryption and keys

### R-BAK-026 · Encryption in transit and at rest on the target
Priority: **MVP** · Covers: OPS-033

- **In transit.** The target endpoint is `https://` unless `allow_insecure_endpoint: true`
  (R-STOR-030's rule). The source database connection follows R-HA-015's TLS settings.
- **At rest.** Every `PutObject`, `CreateMultipartUpload` and `CopyObject` to the target carries the
  configured server-side encryption: `s3` (SSE-S3), or `kms` with `kms_key_id` and S3 Bucket Keys
  ([SSE-KMS](https://docs.aws.amazon.com/AmazonS3/latest/userguide/UsingKMSEncryption.html)). A
  copy request without encryption headers would take the destination bucket's default, so trove
  always sends them ([CopyObject](https://docs.aws.amazon.com/AmazonS3/latest/API/API_CopyObject.html)).
  The preflight writes the marker (or a probe object) and checks the `ServerSideEncryption` that
  `HeadObject` reports; a mismatch exits `2`.
- `mode: none` is allowed for stores without server-side encryption. The run then logs a warning,
  and the result records `encryption.sse: none`; `trove backup list` shows it.
- The default is `s3`, unlike the blob store's `none` ([storage.md](storage.md) R-STOR-032): a
  backup is a complete copy of the database, usually kept in a second location, and SSE-S3 costs
  nothing on AWS S3. Stores that reject SSE-S3 headers fail the preflight with a message naming
  `backup.target.sse.mode`.

Acceptance criteria:

- With `mode: kms`, `aws s3api head-object` of a table file and of a pool object shows
  `ServerSideEncryption: aws:kms` and the configured key.
- Against the local profile's S3 store with `mode: s3`, either the run succeeds and the objects
  report SSE, or the preflight exits `2` naming the key; the local profile's values set the mode
  explicitly.

### R-BAK-027 · Client-side encryption of the database files
Priority: **P1** · Covers: trove-only

- With `backup.encryption.recipients_file` set, every table file and the blob list are encrypted
  to the listed [age](https://age-encryption.org/v1) X25519 recipients before upload, after
  compression. The manifest records `encryption.clientSide: "age"` and the recipients' public
  keys; it stays unencrypted so that `list` works.
- The backup role needs only the public recipients. `restore` and `verify` take
  `--identity-file`; without it they exit `6`.
- Pool objects stay unencrypted client-side: they are package content, deduplication across
  backups needs stable bytes, and server-side encryption covers them.
- The identity is escrowed like the key ring (R-BAK-028); a lost identity makes every backup
  encrypted to it unrestorable.

Acceptance criteria:

- With a recipients file, `aws s3 cp …/db/items.copy.zst - | zstd -d` fails, `age -d -i key.txt |
  zstd -d` prints `COPY` rows, and a restore with `--identity-file` succeeds.

### R-BAK-028 · Stored-secret key ring and key escrow
Priority: **MVP** · Covers: OPS-033

- A backup holds sealed values and never key material (R-ARCH-021). Without the key ring that
  sealed them, a restore stops before loading (R-BAK-020 check 3), and with it every upstream
  credential and webhook secret would be lost.
- **Escrow.** The operator MUST keep a copy of the key ring Secret
  (`secrets.encryption.existingSecret`, [ha-and-helm.md](ha-and-helm.md) R-HA-027) outside the
  cluster and outside the target's credential domain: a password manager or secret vault, or an
  offline copy. Storing it next to the backups would hand both halves to whoever reads the
  target.
- **Every key a retained backup needs.** The manifest lists `secrets.keyIds`; `trove backup list
  --json | jq -r '.[].keyIds[]' | sort -u` prints every key ID that retained backups need.
  After a rotation (R-HA-027) a retired key stays in escrow until no retained backup lists it.
- The same rule covers the age identity of R-BAK-027 (P1) and the database and S3 credentials the
  restore needs; the restore itself needs no secret that the release does not already have.
- `trove backup create` warns once per run when a retained backup lists a key ID that its own
  snapshot no longer uses: a reminder that escrow must still hold that retired key. The check
  compares key IDs only; the backup pod holds no key material (R-BAK-012).

Acceptance criteria:

- After a key rotation and one more backup, `trove backup list --json` shows the old key ID for
  the older backup and the new one for the newer; restoring the older backup with a ring that
  holds only the new key exits `6` naming the old key ID.

## Verification of the design

### R-BAK-029 · Backup and restore test suite
Priority: **MVP** · Covers: OPS-032, OPS-035

Go integration tests against PostgreSQL and the per-change S3 store of
[ha-and-helm.md](ha-and-helm.md) R-HA-051, plus an end-to-end drill in kind. The suite runs on
every change; the drill runs nightly and on dispatch as the leg `e2e-backup` of HA and Performance (nightly), a dispatch on the commit that closes the phase
([ha-and-helm.md](ha-and-helm.md) Q-HA-154, the owner's rule for long legs), as the "Restore drill, trove backup" row of [ha-and-helm.md](ha-and-helm.md) R-HA-052.
Scenarios 1, 2, 5, 7 and 10 also run per change as integration tests without a cluster (Q-BAK-47). The point-in-time
drill has no run in the pipeline.

1. **Consistency under load.** A loop publishes npm and NuGet packages, deletes some, empties the
   trash and runs garbage collection with `storage.gc.grace_period: 0s` every 10 s, while
   `trove backup create` runs. The backup is restored into a fresh database and bucket. Then, for
   every untrashed and trashed file item in the restored database, `GET /files/<projectKey>/<repoKey>/<path>`
   (or the trash API) returns bytes whose SHA-256 equals the item's digest; `npm view` lists
   exactly the versions in the restored database; and `trove backup verify --deep` exits `0`.
2. **Source blobs garbage-collected after the backup.** After backup B, every package is deleted
   and `gc-full` runs with grace `0s`, so the source bucket holds none of B's blobs. Restoring B
   succeeds and scenario 1's download check passes.
3. **Overlapping runs.** Two Jobs created from the CronJob at once: one exits `0`, the other `3`;
   the target holds one new backup.
4. **Garbage-collection hold and lease** as in R-BAK-007's criteria.
5. **Incremental and retention** as in R-BAK-006 and R-BAK-016.
6. **Restore across versions** as in R-BAK-023.
7. **Derived state** as in R-BAK-022.
8. **Drain and crash.** `kubectl drain` during a backup run and during a restore, and `kill -9`
   of each, as in R-BAK-010, R-BAK-012 and R-BAK-021: every Job ends `Complete` without manual
   steps.
9. **Recovery time.** The drill restores a reference data set (the medium profile of R-HA-050)
   and reports the restore duration per phase, and the backup's copy throughput per mode
   (`server`, `stream`); the numbers are measured, not promised.
10. **Revocations survive.** A token revoked and a user disabled after the backup's snapshot stay
    revoked and disabled after the restore (R-BAK-024).

Acceptance criteria:

- Every scenario above passes in CI; the drill publishes its timings as a job artifact of the run that closes the phase.

## Deferred

| ID | Priority | Item |
|---|---|---|
| OPS-036, OPS-037 | P2 | Repository export and import (a partial, per-repository copy); a backup restores whole installations only |
| OPS-038 | P2 | Identity-service configuration export; login providers are file configuration (INV-15) |
| OPS-032 (excluded repositories) | P2 | Excluding individual repositories from a backup; a virtual repository or permission that names an excluded one would restore dangling |
| trove-only | P1 | Scheduled `verify`, deep or sampled, as a second CronJob with its own alert |
| trove-only | P1 | Manifest signatures (H-46) |
| trove-only | P2 | Targets other than S3 (file system, Azure Blob, Google Cloud Storage native APIs) |
| trove-only | P2 | Restoring one repository or one package from a backup into a running installation |
| trove-only | P2 | Sharding one run's blob copy over several pods (an Indexed Job by digest range); one pod copies today (R-BAK-006 "Throughput") |

## Security notes

- **A backup is a full copy of the database.** It holds personal data (users, e-mail addresses,
  audit records with client addresses), token hashes, sealed secrets and the complete permission
  model. Protect the target like the database: separate account or project, server-side
  encryption, restricted readers (R-BAK-015, R-BAK-026). Transient tables with live secrets (UI
  sessions with raw ID tokens, PKCE verifiers) are never exported (R-BAK-004).
- **Credential separation.** Only backup and restore pods hold target credentials; a compromised
  web or worker replica cannot read or delete backups. The backup role in turn has read access to
  the whole database and blob store and write access to the target (H-47). Neither pod holds
  Valkey credentials.
- **Restore is a security event.** It undoes every access removal made after `snapshotAt`, as a
  PITR restore does (H-37). The restore ends all UI sessions, replays the revocation journal
  (R-BAK-024), writes `system.restored` and prints the warning. Skipping the replay takes an
  explicit flag and is recorded in the audit event.
- **Integrity, not authenticity.** Checksums in the manifest detect corruption; they do not stop a
  writer to the target from forging a consistent backup (H-46). Object Lock on the target and
  restricted writers are the mitigation until manifests are signed.
- **Keys are never in the backup** (R-BAK-028), so a stolen backup does not reveal upstream
  credentials or webhook secrets; it does reveal everything else listed above (H-48 until
  client-side encryption, P1).
- **Valkey is not backed up**, so nothing cached there (including authorization decisions) can
  leak through a backup or be revived by a restore (R-BAK-022).

## Decisions

Each question below was decided by the development agent from the listed options; the owner reviews the choices at the walkthrough.

### Q-BAK-1 · Form of the database part of a backup

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `pg_dump` with trove's exported snapshot | Ship `pg_dump`/`pg_restore` in the image; dump with `--snapshot=<id>` so the GC hold and the dump share one snapshot; restore with `pg_restore` | Proven tool, parallel dump and restore, schema included | `pg_dump` refuses servers newer than its own major version ([pg_dump](https://www.postgresql.org/docs/current/app-pgdump.html)), so the image needs the newest client binary and its libpq next to the static Go binary (R-ARCH-001); the dumped schema bypasses trove's migrations; row filters (only referenced blob rows, transient tables) and per-table checksums need extra passes |
| (b) trove-native `COPY` export in one `REPEATABLE READ READ ONLY` snapshot | Every table via `COPY (SELECT …) TO STDOUT` from one exported snapshot; restore builds the schema with trove's migrations and loads with `COPY FROM` | No extra binary; version-independent text format; the schema is always trove's own; filters, checksums, GC hold and table locks sit in one code path; parallel with imported snapshots | trove maintains the export and load code and the table registry; a migration test must keep the registry complete |
| (c) Platform PITR referenced by a manifest | The backup records a PITR timestamp of the platform (CloudNativePG, managed service) and copies only blobs | Smallest backup job; fast database restore on the same platform | Not portable across platforms or providers; the database part is not in the target, so the backup is not self-contained, which INV-19 requires; consistency with the blob copy depends on the platform's archive lag |
| (d) As (b), but `SERIALIZABLE READ ONLY DEFERRABLE` | The snapshot waits for a point free of serialization anomalies | Snapshot consistent with every later serial order | trove's writers run at `READ COMMITTED` (R-ARCH-019), so there are no serializable transactions whose anomalies it would avoid; it can delay the start indefinitely; PostgreSQL's own documentation calls it "not beneficial for a dump which is intended only for disaster recovery" ([pg_dump](https://www.postgresql.org/docs/current/app-pgdump.html)) |

**Choice:** (b) — a trove-native `COPY` export of every table from one `REPEATABLE READ READ ONLY`
snapshot, with the tables locked `ACCESS SHARE` before the snapshot, restored through trove's own
migrations.

**Rationale:** The consistency of (a) and (b) is the same, one MVCC snapshot. What decides is
everything around it: the GC hold must be committed before the snapshot and the pins derived from
it, the blob rows must be filtered to the referenced set, transient tables must be left out, and
every file needs a checksum. All of that is trove code under (a) as well, plus a second binary
whose major version must track the newest server. (b) also makes the restore independent of the
PostgreSQL major version and runs the schema through the same migrations as every installation,
which is the path the rest of the spec tests. (c) fails INV-19's "backup in S3" on its own and ties
the backup to one platform; it stays available as the complementary PITR of R-HA-023. The
development agent would revisit this if the drill (R-BAK-029 scenario 9) shows a load time that a
`pg_restore`-style parallel index build would cut by more than half, or if a table type appears
that `COPY` text cannot round-trip.

**Applied in the spec:** R-BAK-004 (registry), R-BAK-005 (snapshot, table locks and the shared
migration lock of [ha-and-helm.md](ha-and-helm.md) R-HA-019, export on the session host),
R-BAK-021 step 4 (load, including the `TRUNCATE` of migration-seeded rows), R-BAK-023 (version
independence).

### Q-BAK-2 · Keeping a running backup's blobs away from garbage collection

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Grace period longer than any backup | Require `storage.gc.grace_period` > the longest backup and abort runs that exceed it | No new mechanism | A first full backup of many terabytes takes longer than the 24 h default; tests run with grace `0s`; the bound is a guess that fails silently when wrong |
| (b) Pause all collection during a run | A run sets the deletion pause of R-HA-025 for its whole duration | Trivially correct | Reclamation stops for hours every day; a crashed run blocks collection until someone notices |
| (c) Global hold until pinned, then per-blob pins with a lease | Hold `all` from before the snapshot until the to-copy set is pinned, then only pinned digests are kept; a lease bounds a dead run's effect | Correct for any grace period and any run length (proof in R-BAK-007); the global phase lasts only as long as streaming the referenced set; pins shrink as copies finish; incremental runs pin only new blobs | One more table and one more statement in the collector's transaction; pins for a first full backup are one row per blob |
| (d) Snapshot-visibility hold | The release trigger records the removing transaction's ID; collection keeps a blob whose release is not visible in a running backup's snapshot | No pin rows | Depends on `blob_gc_queue` never losing a release: today's queue deletion in R-STOR-024 can, under `READ COMMITTED` re-evaluation, delete an entry another transaction just updated; the proof rests on transaction-ID visibility functions that are hard to test |

**Choice:** (c) — a global hold from before the snapshot until the to-copy set is pinned, then
per-blob pins, both under a lease.

**Rationale:** The safety of a completed backup does not depend on the hold at all: a run writes
its manifest only after every listed digest is confirmed in the pool. The hold's job is liveness,
that a run is not failed by collection, and (c) achieves it for every grace period with a proof
that uses only the row-lock argument R-STOR-024 already relies on. (a) is unsafe exactly where
backups matter most, large first backups. (b) is simple but turns every backup into a daily
reclamation stop. (d) avoids pin rows but moves the proof onto a queue invariant the storage page
does not guarantee today. The development agent would revisit this if pin inserts for the first
full backup of the reference data set take longer than five minutes, which would call for pinning
in key ranges.

**Applied in the spec:** R-BAK-007 (protocol, proof, lease), R-BAK-009 (`hold`,
`backup_blob_pin`), R-BAK-014 (`trove_backup_gc_hold`, `trove_gc_candidates_held_total`,
`TroveBackupGlobalHoldLong`); cross-page: storage.md R-STOR-024 gains the hold check.

### Q-BAK-3 · Blob layout in the target and incremental backups

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Full copy per backup | `backups/<id>/blobs/…` holds every referenced blob | Each backup is independent; deletion is a prefix delete | Storage and copy time grow with every backup; the catalog's own system backups have exactly this problem and limit themselves to small installations (OPS-032) |
| (b) Shared content-addressed pool plus a sorted blob list per backup | One pool; each backup lists its digests; a run copies only digests missing from the previous backup's list; retention deletes backups and then sweeps unreferenced pool objects | Incremental by construction, because a digest never changes content; a restore needs one backup's list and the pool; deletion is provably safe (R-BAK-017) | Deleting a backup frees space only through the sweep; a damaged pool object affects every backup that lists it, which `verify` detects |
| (c) No blob copy; rely on source versioning | The backup records object versions in the source bucket | No copy at all | The backup lives in the source bucket's failure domain; a deleted bucket or account takes the backups with it; not a backup in a configurable S3 target |
| (d) One archive per backup | A tar or zip per backup, like the catalog's export format | One object per backup | Not incremental; restore of one object needs the archive; multi-terabyte archives |

**Choice:** (b) — a shared content-addressed pool in the target, one sorted blob list per backup,
incremental by digest, mark-and-sweep retention.

**Rationale:** trove already identifies blobs by SHA-256 (INV-4), so the pool reuses the source's
layout and its immutability, and an incremental run is a sorted merge instead of a change log. A
restore still needs nothing but the target. (a) and (d) make daily backups of large installations
impractical, and (c) is not a separate copy. The cost of (b), a shared pool where one damaged
object hurts several backups, is covered by `verify` and the per-run spot check. The development
agent would revisit this if the sweep's full listing of the pool becomes the dominant cost of a
run (above about 50 million objects), which would call for per-backup delta lists.

**Applied in the spec:** R-BAK-002 (layout), R-BAK-006 (diff and copy), R-BAK-016, R-BAK-017
(retention and sweep).

### Q-BAK-4 · Where run state and the one-at-a-time rule live

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Session advisory lock | The run holds a PostgreSQL advisory lock for its duration | Released automatically when the session ends | Needs a session-mode connection for hours; gives other replicas no progress, state or hold to read; the collector would need the lock too |
| (b) Row with a partial unique index and a lease | `backup_run` with `backup_run_one_active`, heartbeat and lease | Works through any connection path; the same row carries progress, hold and result for metrics, API and the collector; abandoned runs are detected by lease | Needs the fencing rules of R-BAK-010 for a stalled process |
| (c) Lock object in the target | A conditional `PutObject` lock file in the target | Also excludes other installations | Conditional writes are not available on every S3-compatible store; invisible to the collector and the API |
| (d) CronJob `concurrencyPolicy: Forbid` only | Kubernetes prevents overlap | Nothing to build | Manual Jobs bypass it, and Kubernetes documents that a CronJob may create two Jobs for one time |

**Choice:** (b), with (d) as the first line — a `backup_run` row guarded by a partial unique index
and a lease; the CronJob still uses `Forbid`.

**Rationale:** INV-3 keeps mutual exclusion whose failure would corrupt data in PostgreSQL, and the
row is the one object that the collector, the API and the metrics all need anyway. (a) would add a
long session-bound connection and still need a row for visibility. (c) would only be a second,
store-dependent guard; the target marker (R-BAK-002) covers the cross-installation case at the
level the stores allow. The development agent would revisit this if a stalled run ever writes a
manifest after its lease expired, which would call for (c) on stores that support conditional
writes.

**Applied in the spec:** R-BAK-009 (table, `interrupted`, heartbeat for every operation),
R-BAK-010 (insert, abandonment, waiting for a stale run's lease instead of refusing, fencing),
R-BAK-012 (`Forbid`, exit `3` as `FailJob`).

### Q-BAK-5 · Derived state that outlives the old database

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Nothing | Rely on TTLs and self-validating keys | No work | Keys and `ETag`s built from `rev_seq` repeat with different content after a restore; a shared Valkey serves entries of the old installation; clients get wrong `304`s |
| (b) Flush Valkey during the restore | The restore deletes trove's keys | Valkey starts clean | Needs Valkey access and credentials in the restore Job; a still-running old installation refills it; does nothing for client-side `ETag`s |
| (c) New Valkey keyspace plus a revision offset | Every Valkey key carries `system_state.keyspace_id`, which a restore replaces; every registered revision column grows by 2^32 | Stale Valkey entries become unreachable without touching Valkey; client `ETag`s and `If-Match` values from the old installation can never match again | Every Valkey key carries one more segment; a registry of revision columns to keep complete |
| (d) An incarnation in every key and every `ETag` | Mix the incarnation into all derived identifiers | Covers future counters automatically | Changes every format page's `ETag` rules; every client revalidates everything after each restore, even unchanged documents |

**Choice:** (c) — a new Valkey keyspace ID and an offset of 2^32 on every registered revision column.

**Rationale:** The restore moves counters backwards, and two different consumers remember them:
Valkey (inside trove) and clients (outside). A keyspace ID handles the first without any Valkey
access from the restore Job, and it also isolates a restored installation from an old one that
still runs on the same Valkey. The offset handles the second with a fixed rule in one place, while
`ETag`s keep their format. (d) would reach the same result with edits on every format page. The
development agent would revisit this if a page adds a client-visible counter that does not fit the
naming rule of the registry test.

**Applied in the spec:** R-BAK-022 (fix-ups, registry, test); [architecture.md](architecture.md)
R-ARCH-030 carries the keyspace `<ks>` in every key and channel, and R-ARCH-009 and R-ARCH-018
register `packages.rev_seq` and `cluster_epoch.epoch`; [ha-and-helm.md](ha-and-helm.md) R-HA-057
rule 7 and R-HA-025's `post-restore` move to a new keyspace instead of a flush, and R-HA-060 and
R-HA-042 give the restore Job no Valkey credentials or egress. Cross-page: R-HA-025's
`post-restore` applies the revision offset as well.

### Q-BAK-6 · Encryption of backup contents

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Server-side encryption only | SSE-S3 or SSE-KMS on the target, default `s3` | No key for trove to manage; works with server-side copy | Anyone with read access to the target reads the database files |
| (b) (a) plus optional client-side encryption of the database files | age encryption of table files and the blob list to configured recipients (P1); pool objects stay server-side encrypted | The sensitive part (personal data, token hashes, permissions) is unreadable without an escrowed identity; the backup role needs only public keys; blobs stay deduplicated | One more secret to escrow; P1 |
| (c) Client-side encryption of everything | Pool objects encrypted too | Nothing readable in the target | No server-side copy; deduplication only with convergent encryption, which leaks equality; every restore decrypts terabytes |

**Choice:** (b) — server-side encryption with default `s3` at MVP; client-side encryption of the
database files to age recipients at P1.

**Rationale:** The database part is where the sensitive data is; package blobs are mostly content
that a registry serves to many readers anyway. Encrypting the small part client-side gives most of
the protection of (c) at none of its cost to copying and deduplication. At MVP, a default of SSE-S3
and a separate target account (R-BAK-015) are the protection, and H-48 records what stays open. The
development agent would revisit the P1 ranking if an installation must keep backups with a
provider that the owner does not trust with personal data.

**Applied in the spec:** R-BAK-026 (MVP), R-BAK-027 (P1), H-48.

### Q-BAK-7 · Restore target: empty installation only, or reuse of an existing blob store

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Empty database and empty blob prefix | The restore refuses anything else | Never mixes two installations' objects; no orphans afterwards; no risk from an old installation's collector | A restore copies every blob even when the old bucket survived |
| (b) Empty database, existing blob prefix reused | Present objects of the right size are kept; the rest is copied | Fast recovery when only the database was lost | Leaves orphans that need the orphan sweep (P1); an old installation still running would delete referenced objects |
| (c) In-place restore over a running installation | Replace data under live replicas | No new installation | Requires stopping everything anyway; mixes old and new state on failure |

**Choice:** (a) at MVP; (b) at P1 as `--existing-blobs reuse` (R-BAK-025).

**Rationale:** INV-19 asks for restore into an empty installation, and (a) has no state that two
installations could share by accident. (b) is valuable for the database-only disaster, but it
needs the orphan sweep and the deletion pause, both P1, so it ships with them. (c) adds risk for no
gain. The development agent would revisit the P1 ranking if the drill's restore time for the
reference data set exceeds the recovery time an installation commits to.

**Applied in the spec:** R-BAK-020 checks 4 and 5, R-BAK-021, R-BAK-025.

### Q-BAK-8 · Restore with a newer trove release

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Same release only | Restore requires the backup's exact version | Simplest | Operators must find and run an old image during an incident; old backups become hard to use |
| (b) Any release that embeds the backup's steps | Migrate to the backup's step, load, migrate forward | Uses the existing, tested migrations; any later release of the major restores any earlier backup | The runner needs an "up to step N" mode; a major that drops steps must say which release restores older backups |
| (c) Convert while loading | Load an old backup directly into the newest schema with per-table converters | One migration pass | A second, untested migration path per schema change |

**Choice:** (b) — any release whose embedded migrations contain every step of the backup with the
same checksums.

**Rationale:** Migrations are already append-only and checksummed (R-HA-018, R-ARCH-026), so the
data of step N always loads into the schema of step N, and the path forward is the one every
upgrade takes. (a) puts an avoidable obstacle into an incident, and (c) duplicates every
migration. The development agent would revisit this if a release has to squash migrations within a
major.

**Applied in the spec:** R-BAK-020 check 2, R-BAK-021 steps 2 and 5, R-BAK-023; cross-page:
architecture.md R-ARCH-026 (runner mode on a locked connection, with the caller's statements in
the final `schema_state` transaction).

### Q-BAK-9 · Backup CronJob off by default

Status: **Decided by the owner on 2026-11-07** — option (a), replacing the development agent's choice (b).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Off by default | `backup.enabled: false`; operators opt in | The minimal install stays minimal | An installation without backups is the default, which the owner constraint treats as a core requirement |
| (b) On by default, target required | Rendering fails without a target bucket unless backups are disabled explicitly | Nobody runs without backups by accident; the failure names the fix | One more value in the minimal install |
| (c) On by default, target in the blob bucket | Default target is a prefix of the blob bucket | Works without extra configuration | Same failure domain as the data (R-BAK-002); a bucket deletion takes the backups too |

**Choice:** (a) — the CronJob is off by default. `backup.enabled: true` turns it on, and only then
does rendering fail without `config.backup.target.bucket`.

**Rationale:** The owner decided that an installation opts in. The backup needs a bucket of its own,
which the operator has to provide anyway, so a fail-fast default forces every installation, preview
installations included, to configure or switch off something it did not ask for. Once an
installation turns the backup on, (b)'s rule holds: rendering fails without a target and names it.
(c) stays rejected: it would look like a backup without being one. The local development profile
sets `backup.enabled: true` with a second bucket of its S3 store and `allow_same_bucket` unset, so the
drill runs there and in every end-to-end set layered on it.

**Applied in the spec:** R-BAK-012 (the CronJob with `backup.enabled: true`, the render failure only
then). [ha-and-helm.md](ha-and-helm.md) R-HA-060, R-HA-029 and R-HA-031 render the CronJob only with
`backup.enabled`, fail without `config.backup.target.bucket` only then and leave the bucket out of
the minimal install; R-HA-045 renders the alerts of the backup runs only with the backup (R-BAK-014),
and R-HA-051 still creates the second bucket `trove-backups`. The chart side of this choice is
[Q-HA-17](ha-and-helm.md#q-ha-17--the-backup-cronjob-is-off-by-default), decided the same way.

### Q-BAK-10 · Resuming an interrupted restore

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

A restore of a large installation runs for hours, and a node drain may happen at any time
(INV-12). The question is what the next attempt does with a half-restored database.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) No retry | The restore Job has `backoffLimit: 0`; an interrupted restore leaves a database that is no longer empty, and the operator drops the schema, empties the blob prefix and starts again | Simplest; the "empty installation" check needs no exception | A drain during a multi-hour restore fails the restore Job and loses all progress; recovery needs manual database work during an incident |
| (b) Explicit `--resume` | The operator reruns with a flag that accepts the state the earlier attempt left | Resumption is a deliberate act | The Job's own retry pod runs without the flag and refuses, so a drain still fails the Job; the operator must edit the restore Job's arguments to continue |
| (c) Automatic resumption keyed to the backup ID | Step 2 writes a schema anchor naming the backup ID before its first change; any later attempt for the same backup ID continues where the last one stopped; any other attempt refuses | A drain or crash costs one pod restart; the Job's retry policy works as for the backup; the forgotten-`restore.enabled` protection stays, because a finished restore removes the anchor | The precondition has one documented exception; every step must be idempotent or guarded, which the procedure states step by step |

**Choice:** (c) — automatic resumption keyed to the backup ID through a schema anchor, with the
migration lock held for the whole restore.

**Rationale:** The owner requires that a node drain cause no outage. A restore is the moment when
the installation is already down, so losing hours of restore work to a routine drain prolongs the
outage directly. (c) makes the restore Job behave like the backup Job: evicted, restarted,
continued. The anchor is written before the first change, so there is no window in which a
half-written database lacks it, and the migration lock keeps a terminating old pod and its
successor from working at the same time. (b) does not help the case that matters, a retry by the
Job controller. The development agent would revisit this if a step of the procedure turns out not
to be idempotent and cannot be guarded by the marker's phase.

**Applied in the spec:** R-BAK-020 (check 4 exception, check 5), R-BAK-021 (lock, anchor, phases,
resumption), R-BAK-012 (restore Job failure policy); [ha-and-helm.md](ha-and-helm.md) R-HA-060
gives the restore Job `restore.backoffLimit: 2` and the backup Job's pod failure policy.

### Q-BAK-11 · Bounding a hung backup run

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

A run that hangs (a stuck connection, a store that accepts requests and never answers) keeps its
pins, the one-active slot of R-BAK-010 and the CronJob's `Forbid`, so no further backup starts.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Job `activeDeadlineSeconds` | Kubernetes kills the Job after a fixed time ([ha-and-helm.md](ha-and-helm.md) R-HA-060 proposed 21600 s) | Built in | Kills a healthy first backup of a large installation every time, because that takes longer than any fixed limit that still catches hangs quickly (Q-BAK-2's premise) |
| (b) Alert only | No limit; `TroveBackupRunningLong` and `TroveBackupStale` fire | Never kills a healthy run | A human must find and delete the pod; until then no backup runs |
| (c) Progress-based self-cancel | The heartbeat renews the lease only while the run makes progress; after `backup.stall_timeout` without progress the run fails as `stalled` and the Job retries | Catches hangs within minutes, whatever the run's total length; releases pins and the slot by itself | The progress signal must cover every phase, including bounded lock waits; one more setting |

**Choice:** (c) — the run cancels itself after `backup.stall_timeout` (30 minutes) without
progress; the Job has no `activeDeadlineSeconds` by default.

**Rationale:** Length is the wrong signal: a large first backup is long and healthy, a hung run can
be short and broken. Progress distinguishes them. The lease already expires when a process dies;
tying its renewal to progress extends the same mechanism to a process that lives but does
nothing, and the fencing of R-BAK-010 already handles a process that wakes up after its lease
ended. `TroveBackupRunningLong` stays as the alert for a run that progresses too slowly. The
development agent would revisit this if a phase is found whose work cannot report progress
within `stall_timeout`, for example a single server-side copy that takes longer.

**Applied in the spec:** R-BAK-013 (stall detection), R-BAK-011 (`stall_timeout`), R-BAK-012
(no `activeDeadlineSeconds`); [ha-and-helm.md](ha-and-helm.md) R-HA-060 and R-HA-031 set
`backup.activeDeadlineSeconds` and `restore.activeDeadlineSeconds` to 0, not rendered.

### Q-BAK-12 · The `backup` command in M0

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Implement `trove backup` in M0 | The create and restore paths are built with the foundation | The command works from the first release | The backup needs the table export classes and the revision registry of later milestones (M5); building it now would freeze formats that are not designed yet |
| (b) Leave the command out until M5 | `trove backup` is an unknown command | Nothing half-built | Scripts and the chart cannot reference the name until M5 |
| (c) Reserve the name; every `trove backup` subcommand returns "not implemented yet" and exits non-zero (chosen) | `cmd/trove/main.go` dispatches `backup` to `errNotImplemented` | The name and the exit code are fixed now; the chart and documentation can refer to it | A command that exists and does nothing |

**Choice:** (c).

**Rationale:** The subcommand names are part of the surface of [architecture.md](architecture.md) R-ARCH-001, and
M0 must not decide the backup format. A reserved name fixes the surface without a format. The development agent
would revisit this at the start of M5, when the command is implemented and the reservation ends.

**Applied in the spec:** no requirement text changed. The backup format and procedure of this page are unchanged;
the M0 state is the command's reservation only.

### Q-BAK-13 · Where the manifest schema lives

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `internal/backup/manifest.schema.json` | The path R-BAK-003 named | Matches the text | The package that owns the Go type is `internal/backup/format`; a schema one directory up cannot be embedded by it, so the type and its schema would drift |
| (b) `internal/backup/format/manifest.schema.json`, embedded by the package (chosen) | The schema sits next to `Manifest` and is exported as `format.ManifestSchema` | One package owns the type, its encoding and its schema; a test validates every manifest it writes against it | R-BAK-003 changes its path |

**Choice:** (b).

**Rationale:** The acceptance criterion of R-BAK-003 asks that a JSON Schema validates every manifest the suite writes. That only holds if the schema cannot be forgotten when the type changes, which an embed in the owning package guarantees. The development agent would revisit this if a tool outside Go needs the file at a stable URL.

**Applied:** R-BAK-003 acceptance criterion; `internal/backup/format/schema.go`, `internal/backup/format/format_test.go`.

### Q-BAK-14 · The package cut of the backup lane

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One package `internal/backup` | Format, ports, runner, registries and commands in one package | No import questions | Eight work packages edit one directory; the format, which the sweep, verify, restore and the create pipeline all read, has no seam of its own |
| (b) `format`, `target`, `fixup` and the runner as packages, the journal as `internal/journal` (chosen) | `internal/backup/format` (ID, layout, marker, manifest, blob list and its merge), `internal/backup/target` (the port of the S3 target and an in-memory fake in `targettest`), `internal/backup/fixup` (the restore's registry), `internal/backup` (exit codes, run states, run store, lease handle, ports, operation interfaces, metrics) and `internal/journal` | Each work package owns files in directories no other package edits; the shared shapes are fixed once and have tests; the journal is used by `trove admin post-restore` without the backup | More packages than one |

**Choice:** (b).

**Rationale:** The lane is built by parallel work packages. The only way they stay out of each other's files is shared types that sit in packages none of them owns after the contracts commit. `internal/journal` is separate because the journal is MVP on its own (R-HA-054), is replayed by two commands and has producers in every domain. The development agent would revisit this if a package of the cut grows a second reason to change.

**Applied:** [ADR-157](../decisions.md#adr-157--the-backup-lane-is-cut-into-ports-formats-and-registries-that-its-work-packages-fill-in-files-of-their-own); `internal/archtest/layers.go` (journal is a domain package); [docs/developer/backup.md](../developer/backup.md).

### Q-BAK-15 · The S3 implementation of the target port is built in `internal/blob/s3`

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `internal/backup/target` imports the AWS SDK | The adapter sits with the port | One place | R-ARCH-002 allows the SDK in `internal/blob/s3` only, and the architecture test would fail |
| (b) The port `target.Store` is a leaf package; the adapter is a file of `internal/blob/s3` that implements it (chosen) | `internal/backup/target` imports only the standard library, so `internal/blob/s3` (infrastructure) may import it; the adapter is a client for a second bucket with its own credentials, SSE and part sizes; `cmd/trove` joins it with the backup code | R-ARCH-002 holds; the target client is built by the package that already knows the SDK, its retry settings and its credential chain (R-STOR-031, R-STOR-033) | The adapter's work package edits a package of the storage layer; the port may not import a domain package |

**Choice:** (b).

**Rationale:** R-ARCH-002 names `blob/s3` as the only importer of the SDK, and the backup text of the same page says the target is reached through clients built by `blob/s3`. The port in the domain package lets every other work package test against `targettest.Mem`. The development agent would revisit this if a target other than S3 is added (Deferred), which would add a second adapter behind the same port.

**Applied:** `internal/backup/target/store.go`, `internal/backup/target/targettest`; the adapter is a work package of the lane ([docs/developer/backup.md](../developer/backup.md)).

### Q-BAK-16 · The restore's fix-ups are a registry, not a list in the restore

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A function in the restore that runs every fix-up | One file lists jobs, keyspace, revisions, usage counters, mirror state | Simple | Every later page that adds derived state edits the restore; the npm and NuGet remote-cache purge, the mirror state and the storage recount belong to other pages' owners |
| (b) A registry with an order and a name, filled by the owner of the state (chosen) | `fixup.Register` for step 6 (one transaction), `fixup.RegisterPurger` for the remote caches of step 7; the restore runs what is registered in order | The same pattern as the table registry (R-BAK-004) and the revision registry (R-ARCH-009); `trove admin post-restore` runs the same fix-ups; a format adds its purger in a file of its own | A registry is package-level state that is frozen at start-up (R-ARCH-032) |

**Choice:** (b).

**Rationale:** R-BAK-022 already says the revision registry is `meta.RegisterRevisionColumn` and that no second list exists. The same argument applies to every other restore fix-up. The development agent would revisit this if the order of two fix-ups starts to matter beyond the constants of `fixup.Order*`.

**Applied:** R-BAK-021 steps 6 and 7, R-BAK-022, R-BAK-008; `internal/backup/fixup`.

### Q-BAK-17 · The to-copy set is inserted in batches with `unnest`, not with `COPY FROM STDIN`

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `COPY backup_blob_pin FROM STDIN` as R-BAK-006 wrote it | The pgx copy protocol on the control connection | Fastest bulk path | The copy protocol is pgx API; only `internal/meta/postgres` may import pgx, so the domain package would need an export API for one statement |
| (b) `INSERT … SELECT unnest($digests), unnest($sizes)` in batches of a few thousand rows (chosen) | A generated query, `RunStore.AddPins` | No driver in the domain; the same transaction semantics; the batch size is a tuning constant | Slower than `COPY` by a small factor |

**Choice:** (b), with a way out: if the pin insert of the first full backup of the reference data set takes longer than five minutes (the revisit condition of Q-BAK-2), the create work package adds a `COPY` method to `internal/meta/postgres` behind `RunStore.AddPins`.

**Rationale:** The interface stays the same either way, so the way out costs one file. The development agent would revisit this when the drill (R-BAK-029 scenario 9) measures the pin phase.

**Applied:** R-BAK-006 step 2; `internal/meta/postgres/queries/backup.sql` (`InsertBackupPins`), `internal/backup/runstore.go`.

### Q-BAK-18 · The backup commands load their configuration as `CommandBackup`, and every failure maps to one exit code and one failure code

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Load as `CommandAdmin` | The admin command set | No new command | The bucket of the target would be optional, so a misconfigured Job would fail later and with exit 1, which the pod failure policy retries |
| (b) A command of its own, `config.CommandBackup`; `backup.target.bucket` is required for it; any configuration error exits 2 naming every key (chosen) | Needs the database, the blob store and the `backup` keys; no role, no Valkey, no key ring check except where the restore opens it | R-BAK-011's "a violation exits 2 and names the key" and R-BAK-012's `FailJob` on 2 hold from the first line of every command | One more command value |

**Choice:** (b). `internal/backup/exit.go` maps every error of the lane to one of the exit codes of R-BAK-013 (`ExitCode`) and one failure code (`ErrorCode`) for `backup_run.error` and `system.backup.failed`; a command returns the error and `cmd/trove` exits with the code.

**Rationale:** A mapping in one place is the only way eight work packages agree on what "temporary" means. The development agent would revisit this when a code needs a distinction the table of R-BAK-013 does not make.

**Applied:** R-BAK-011, R-BAK-013; `internal/config/backup.go` (`ValidateFor`, `validateBackupCoupling`), `cmd/trove/backup.go`.

### Q-BAK-19 · The run store, the lease handle and the slot rule are shared contracts, built once

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Each operation writes its own `backup_run` code | create, delete, verify, the API and the collector each query the table | No coordination | Four copies of the fencing rules; the rule that makes a stalled run safe would be tested in none of them |
| (b) `RunStore` (PostgreSQL implementation `PGStore`), `Handle` (lease, heartbeat, stall) and `AcquireSlot` in `internal/backup` (chosen) | The queries are in `queries/backup.sql`; the table has CHECK constraints that only a create run holds anything and that a finished run holds nothing | R-BAK-010 and R-BAK-013 are implemented and tested once; every operation, including `verify`, heartbeats the same way | A change to the lease rules touches a package every work package imports |

**Choice:** (b).

**Rationale:** The one-active rule, the fenced heartbeat and the wait for a stale run's lease are the part of the lane whose failure corrupts a backup or blocks all of them, so they get one implementation and an integration test (`test/integration/postgres/backup_runstore_test.go`). The development agent would revisit this if the heartbeat needs a second progress signal that `Handle.Tick` cannot carry.

**Applied:** R-BAK-007, R-BAK-009, R-BAK-010, R-BAK-013; `internal/backup/runstore.go`, `handle.go`, `slot.go`, `internal/meta/postgres/migrations/00046_backup.sql`.

### Q-BAK-27 · The S3 target is a second client of the blob store's client code, and a store without conditional writes gets a HEAD check

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A separate S3 client in the backup package | `internal/backup` imports the AWS SDK | Independent of the blob store's client | R-ARCH-002 allows the SDK in `internal/blob/s3` only; the retry classification, error mapping, stall detector and credential files would be written twice |
| (b) `s3.Target` in `internal/blob/s3` over a second `Client` built from `backup.target` (chosen) | `target.go`, `target_writer.go`, `target_copy.go` implement `target.Store`; the constructor maps `backup.target` to the object-store section and builds its own HTTP client, credential chain and retryer | One place for the SDK, one error classification, the rotation of credential files of R-STOR-031 applies unchanged | The transport and retry settings of the target are the storage.s3 defaults, since `backup.target` has no keys for them |

A conditional create sends `If-None-Match: *` (PutObject, CompleteMultipartUpload). A `412` is `ErrExists`. A store that answers `501`, `NotImplemented` or `400 InvalidArgument` is remembered for the life of the `Target`, and every later conditional create does a `HeadObject` first and writes unconditionally: not atomic, and `ErrConditionalUnsupported` is not returned. The marker and the pool never need the atomic form, because the one-run rule is held in PostgreSQL (Q-BAK-4) and every key below the target is immutable (R-BAK-002). A store that ignores the header silently gets the same guarantee as the fallback.

**Choice:** (b), with the HEAD fallback.

**Rationale:** The target needs the same classification of store answers as the blob store, and the backup lane must run against stores without conditional writes (older MinIO, Ceph, Garage). The development agent would revisit this when the lane needs an atomic create for correctness, which would make a store without conditional writes a configuration error.

**Applied:** R-BAK-002, R-BAK-015, R-BAK-026; `internal/blob/s3/target*.go`, `internal/backup/target/targettest/conformance.go`, `test/integration/backup/target_adapter_test.go`, `docs/developer/backup.md`, `docs/security/backup-and-restore.md`.

### Q-BAK-31 · The restore uses one database connection
### Q-BAK-28 · `trove backup list --json` is flat: `schemaStep` and `keyIds` stand at the top of each entry

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Two: the migration runner's session and a load connection | The text of R-BAK-005 | The load transaction is on a connection that never held a lock | Two sessions to drain on `SIGTERM`; the migration lock and the load would be two sessions of one process, so a kill between them could leave one of them alive for a moment |
| (b) One session connection for the lock, the runner, the load, the fix-ups and the finish (chosen) | `postgres.RestoreSession` | The lock lives exactly as long as the one connection, so a killed process releases it with its socket; the budget of R-HA-016 is met with room | Every phase of the restore is serialised on one connection, which they are anyway |

**Choice:** (b). `COPY FROM`, the temporary tables and the advisory lock are all session state; a pooled second connection would add nothing.
**Rationale:** The restore never runs two statements at once, so a second connection can only add a way to leak the lock. Revisit when the load is parallelised per table.
**Applied:** R-BAK-005 (connection count), R-BAK-021 step 1; `internal/meta/postgres/migrate_restore.go`.

### Q-BAK-32 · The load stages the audit table and the tables with a CycleBreak column in temporary tables
| (a) Mirror the manifest | `schema.step`, `secrets.keyIds` nested as in `manifest.json` | The acceptance criteria of R-BAK-018 and R-BAK-028 already say `schema.step` and `secrets.keyIds` | A listing line is a summary, not a manifest; `backup.Listing` (the contract every consumer built against) is already flat; the incomplete entries would carry empty objects |
| (b) Flat summary: `schemaStep`, `keyIds`, `incarnation`, `options`, `encryption` beside `backupId` and `complete` (chosen) | `backup.Listing` as it is | One shape for the CLI and the Go callers; fields that do not exist for an incomplete directory are omitted | The two acceptance criteria are reworded |

**Choice:** (b). `databaseBytes` is the sum of the table files' `compressedBytes` (what the database part occupies on the target); `listing.DatabaseBytes` is the one definition that the listing, the `retained` records and a create run's result share. A manifest that cannot be read (unreadable, naming another backup, or of a higher format) fails the whole listing: retention and the sweep could not tell what to keep, and a listing that hid such a backup would mislead. A higher format is exit 6.

**Rationale:** Nothing reads the nested form yet, and the flat form is what the contract already is. The development agent would revisit this if a consumer wants the manifest's own shape; then `--json --manifests` is the extension, not a change of this array.

**Applied:** R-BAK-018 and R-BAK-028 acceptance criteria; `internal/backup/listing`.

### Q-BAK-29 · A verify run stays in state `starting`; what verify and delete do with what they cannot judge

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Read the file twice | A first pass over `audit_event` finds the time range, the second copies | No temporary storage | Two downloads and two decompressions of the largest table; the timestamp text depends on the exporting session's settings |
| (b) Copy into a temporary table, let PostgreSQL read the range, insert from it (chosen) | `COPY` into `_restore_stage_<table>`, `audit_create_partitions(min, max)`, `INSERT ... OVERRIDING SYSTEM VALUE SELECT`; a CycleBreak table keeps its stage until every table is loaded, then `UPDATE ... FROM` matched on the primary key | PostgreSQL parses the timestamps; one download; the same path serves both cases | Temporary disk space of the size of the staged table for the length of the transaction |

**Choice:** (b). Only `audit_event` and the tables with a registered CycleBreak (`package_versions.lead_item_id`, `principal.created_by`) take this path; the others are copied directly.
**Rationale:** The cycle partner of a table may load before or after it, so the column can only be filled once every table exists. Revisit when a staged table is too large for the temporary tablespace.
**Applied:** R-BAK-004, R-BAK-021 step 4; `internal/meta/postgres/backup_load.go`, `internal/archtest/sqlrules.go` (the restore files may create temporary tables and assemble statements from catalog names).

### Q-BAK-33 · A restore writes nothing to the target; a missing schema fails check 4
| (a) Give verify phases | Map checking onto `copying` and `finalizing` | The UI shows movement | The states are the phases of a create run (R-BAK-009); a verify showing `copying` would make a reader think blobs move |
| (b) One state, progress by lease renewal (chosen) | The row is `operation = 'verify'`, state `starting` until it ends `completed` (also with findings) or `failed`; its `result` is the report (`verified`, `findings`), so `trove_backup_last_verify_findings` is the number of findings | No new states; the index ignores verify | A long deep verify reads "starting" in the run list |

**Choice:** (b), and for the cases the requirement text leaves open:

- A manifest that does not parse, a table file or blob list that is missing or differs, and a manifest naming another installation than the target marker are findings (`missing`, `size_mismatch`, `file_checksum_mismatch`; `detail` says why). A manifest of a higher format is exit 6 and ends the run `failed` with `format_unknown`. After an adoption (`adoptedAt` set) the installation comparison is skipped, because the marker then names the adopting installation and the backups of its predecessor stay in the target. A blob list that failed its check is not read, so its pool entries produce no findings.
- A digest that several verified backups list is checked once (`HeadObject`, with `--deep` one download) and reported for every backup that lists it, by merging the sorted lists. `--bytes-per-second` throttles the downloads of `--deep` across all workers; the table files are not throttled.
- `delete` and `prune` are `operation = 'delete'` runs; the move to `pruning` is their only phase. A refusal (unknown ID, the only complete backup without `--allow-last`, no marker on the target) is exit 2 and leaves a `failed` row with the code, after the slot is taken and before anything is written. A foreign marker is exit 4 and writes nothing.

**Rationale:** Each rule makes the report say what is wrong without a human reading code, and none of them changes what is written to the target. The development agent would revisit the verify state when the UI wants a progress bar for deep verification.

**Applied:** R-BAK-019, R-BAK-016; `internal/backup/verify`, `internal/backup/retention`, `backup.Finding.Detail`.

### Q-BAK-30 · The sweep builds its keep set only from blob lists it has proven, and a skipped or stopped sweep is a warning

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Probe the target with a `PutObject` and `DeleteObject` as the text says | The restore credential needs write access to the target | Matches the wording | A restore Job whose target credential is read-only (the better practice, R-BAK-015) fails check 7 |
| (b) `HeadObject` on the target, `PutObject` and `DeleteObject` of `_probe/<pod>` on the blob store only (chosen) | The target is only read; the probe object is where the restore writes | The restore needs no write privilege on the backups | A target whose reads work and whose writes would not is not detected, and the restore does not need them |

**Choice:** (b). A schema that does not exist fails check 4 with the statement that creates it (R-HA-014), because creating it is a write before step 2.
**Rationale:** The restore is the one command that must work when little else does, so it asks the target for the least. Revisit if a restore ever writes to the target.
**Applied:** R-BAK-020 checks 4 and 7.

### Q-BAK-34 · The journal and the blob store enter the restore as ports; the verification merges two sorted streams
| (a) Trust the lists | Merge the lists as they are and delete what they lack | One download per list | A list that is truncated at a line boundary or replaced would delete live pool objects of a retained backup; the damage is found by the next restore |
| (b) Prove the lists first (chosen) | Before the first delete request every retained backup's list is downloaded and hashed against `blobs.sha256`, and the entry count at the end of the merge must equal `blobs.referenced` | Silent truncation cannot reach the delete request; the cost is a second read of lists that are about 40 bytes per blob compressed | Two reads of every list per sweep |

**Choice:** (b). A list that fails the proof skips the sweep with a warning naming the backup, and the run ends `completed_with_warnings`: the backup the run wrote is complete and valid, and `trove backup verify` names the damaged list. A sweep that a foreign or out-of-order pool key stopped is also a warning, drops the batch not yet sent, and leaves `poolSweptAt` unset so that the next run sweeps again. A completed sweep records `poolSweptAt` in the result; "at least every `sweep_interval`" counts from the newest value among the last 50 successful create and delete runs (`retention.RunSweepClock`), and a pool that was never swept is swept. The sweep runs on an empty keep set (after `delete --allow-last`), which empties the pool: that is what the operator asked for.

**Rationale:** Retention deletes only what a verified manifest-and-list pair proves unreferenced, which is the property R-BAK-017 states. The development agent would revisit this if the lists grow so large that the second read matters; the manifest could then carry a per-list digest of the sorted entries to check in the same pass.

**Applied:** R-BAK-016, R-BAK-017; `internal/backup/retention/sweep.go`, `stage.go`, `marker.go`; `backup.Result.PoolSweptAt`.

### Q-BAK-36 · The backups screen refreshes as one load on the status screen's interval, and keeps pages the user has loaded

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Concrete clients in the restore package | The restore imports the journal reader and the S3 client | Fewer types | The AWS SDK may be imported in `internal/blob/s3` only (R-ARCH-002), and the replay work package would have to edit the restore |
| (b) `Session`, `BlobStore` and a `Journal` factory in `internal/backup/restore`; the verification of step 8 merges the live blob rows (digest order) with the blob list without the damaged digests (chosen) | cmd/trove joins the implementations; tests use fakes | Every phase is unit-tested without a database; the journal work package fills one function; the verification needs no temporary table and memory stays bounded | A port per collaborator |

**Choice:** (b). `restoreJournal` in `cmd/trove/backup_restore.go` is the one place the journal work package replaces.
**Rationale:** The checks and the resumption rules are the part of a restore that must not regress, and they are testable only against fakes. Revisit when a second implementation of a port appears.
**Applied:** R-BAK-020 check 8, R-BAK-021 step 8, R-BAK-024; `internal/backup/restore`, `cmd/trove/backup_restore.go`.
| (a) The screen's own faster interval while a run is active | 2 to 5 s polling of the summary and the active run | The blob count moves visibly | A second interval and pause rule beside the status screen's (R-UI-038); a running create changes its progress at the heartbeat, which is seconds apart anyway |
| (b) The status screen's interval and pause control; the summary and the active run are fetched together, and the first page of the list is reloaded unless the user has loaded further pages (chosen) | One `load` returns the summary and, when `active` is set, `GET /system/backups/{runId}` for its `progress`; "Refresh now" does the same | One rule for every status screen (R-UI-048 names it); the status and the progress of the active run never disagree; a list the user has paged into is not thrown away | A run is up to one interval behind; a user who wants it faster presses "Refresh now" |
| (c) Server-sent events | The API pushes progress | No polling | A streaming endpoint for a screen an administrator opens rarely; every replica would need the run's progress (R-API-050 reads PostgreSQL only) |

**Choice:** (b). The `summary` carries the active run's `id` only, so the screen reads the run once per load for the progress that R-UI-048 lists; an auto-refresh reloads the first page of the list only while the list holds at most one page.

**Rationale:** R-UI-048 says "refreshed with the status screen's interval and pause control", and the heartbeat is the resolution of the data. The development agent would revisit this when the summary carries the active run's progress itself, which would save the second request.

**Applied:** R-UI-048 (no text change; the behaviour was specified), `web/src/app/features/system/backups/backups-page.ts`.

### Q-BAK-37 · How the badge of a run reads: green only for a run that ended well, `interrupted` neutral, a lease-expired run as stopped

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One badge per state with the state's colour: green completed, red every ended state that is not completed | `interrupted` red | Two colours | A drain would look like a failure, against R-BAK-012 and R-UI-048 |
| (b) `completed` green; `completed_with_warnings` and a run whose lease has passed in the warning colour; `failed` and `abandoned` red; the five active states blue; `interrupted` neutral, with its sentence (chosen) | Icon and word on every badge; a lease-expired active run reads "Stopped" with "stopped without finishing; the next run marks it abandoned" | Follows the rule "green only for positive status" and the summary's failure rule (`interrupted` never counts; an expired lease does); the colour only reinforces the word | Seven distinct looks |

**Choice:** (b). The status uses the standard badge of the design language; the failure badge of "Last failure" repeats the summary's rule, so the screen shows a failure only where `TroveBackupFailed` would fire. The text of a stored `error.code` comes from the table of `web/src/app/core/http/problem-messages-backup.ts`, which holds one sentence for every failure code of `internal/backup/exit.go` (R-UI-042); a code the table does not know is shown as the code alone.

**Rationale:** The state of a run is the one fact an administrator reads first, and a false red trains people to ignore the colour. The development agent would revisit this when a state is added to R-BAK-009.

**Applied:** R-UI-048 (the screen's permission is named `settings:read`, as in R-API-050 and as the System status screen has it, not `system.read`), R-UI-042, `web/src/app/features/system/backups/backups-model.ts`, `backup-state.ts`.

### Q-BAK-35 · The gauges of the job are one evaluation of single-row reads per replica and 30 seconds

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One aggregate statement | A new query returns the newest success, failure, verify, the active runs and both holds in one round trip | Literally "one query" | `RunReader` is a contract the other packages build against; a statement that returns six facts needs its own row type and tests, and the API reads the same facts separately anyway |
| (b) One evaluation of the reader's single-row reads, reused for 30 s (chosen) | `gauges.Collector.Refresh` reads the newest success, the newest ended failure, the active runs, the newest verify, both holds and the outbox backlog, then keeps the values for 30 s | The reads are the ones the API and the collector's hold check already use; one rule set (`gauges.ReadJobs`) serves the gauges, the summary and the info block, so they agree by construction | Six to eight cheap statements per replica and interval instead of one |

**Choice:** (b). R-BAK-014 says "one evaluation of single-row reads on that small table" where it said "one query".

**Rationale:** The load that the requirement protects against is a cost that grows with the pins or with the scrape rate. Neither does: no read names the pin table (a test fails if a file of the package does), the pins left come from the active run's `progress` written by its heartbeat, the table is pruned to `backup.run_history`, and the evaluation runs once per `metrics.db_gauge_interval` with a 30 s reuse floor whatever the scrape rate. The development agent would revisit this if `pg_stat_statements` on a large installation shows the eight statements as a cost.

**Applied:** R-BAK-014 (metrics paragraph); `internal/backup/gauges/jobs.go`, `collector.go`.

### Q-BAK-49 · The backup reads the blob bucket with `backup.source.credentials` when `separate_credentials` is set

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Reuse `storage.s3.credentials` and let the chart mount different files at the same paths | The backup pod gets other bytes under the paths of the runtime credential | No new key | The shared `trove.yaml` would lie about which credential a pod holds; the path names the runtime Secret in every pod |
| (b) `backup.source.separate_credentials` and `backup.source.credentials` (the keys of `storage.s3.credentials`), used by `trove backup create` for the blob client (chosen) | The chart sets both when the blob credentials are static, with files of `secrets.backupSource` | The config says what the pod reads with; no change for a workload identity, where `separate_credentials` stays `false` | A key pair more; `separate_credentials` with `static` and a missing file exits 2 naming the key |

**Choice:** (b). Only `create` reads the source blobs; the restore writes the bucket and keeps `storage.s3.credentials`.

**Rationale:** R-BAK-015 gives the backup no right to write or delete in the blob bucket. The development agent would revisit this if `verify` or `delete` need to read the source bucket.

**Applied:** R-BAK-011, R-BAK-015; `internal/config/backup.go`, `cmd/trove/backup_create.go`.

### Q-BAK-41 · The summary finds the run that last listed the target among the newest runs of each operation

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The newest successful create or delete run | One `Last` per operation | Cheapest | A `create --no-prune` records neither `retention` nor `retained` (R-BAK-016), so the newest run would blank the stored backups until the next pruning run |
| (b) The newest 25 runs of each of the two operations, the one with the newest finish that recorded a retention (chosen) | `List` per operation, decode `result`, keep the newest with `retention` | A run without pruning or a failed run never hides the last listing; `asOf` and `runId` stay honest | Two small reads with 25 results decoded; more than 25 runs of an operation in a row without a listing leave both fields `null` |
| (c) A column on `backup_run` that marks the listing run | Migration and a write by the backup pod | Exact | A schema change for a display |

**Choice:** (b).

**Rationale:** The result already carries what is needed, and the lookback is bounded by the history that the table keeps anyway. Only a successful run counts: a failed run stores no result. The development agent would revisit this if the schedule ever runs far more often than pruning.

**Applied:** R-API-050 (`storedBackups`, `retention`); `internal/api/v1/backups_api.go` (`listedTarget`).

### Q-BAK-42 · The verify gauges come from the newest finished verify run and the findings its result stores

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Any finished verify run | Includes `interrupted` and `abandoned` runs | Simple | A drained verify would reset the findings to zero and move the timestamp without having checked anything |
| (b) `completed`, `completed_with_warnings` and `failed` (chosen) | A verify that exits 5 ends `failed` with its report in `result` (R-BAK-019); the findings are `len(result.findings)` | A verify that found something keeps the alert on until a clean verify runs; interrupted and abandoned runs leave the gauges alone | A `failed` verify that stored no report (an error before the checks) reads as zero findings with a new timestamp |

**Choice:** (b).

**Rationale:** `backup.VerifyReport` is the object the verify package stores, and its `findings` array is the count the alert needs. The development agent would revisit this when the verify package records an explicit findings count or distinguishes an aborted verify from a clean one.

**Applied:** R-BAK-014 (`trove_backup_last_verify_*`); `internal/backup/gauges/collector.go`.

### Q-BAK-43 · The restore's item store publishes nothing, refuses blob and job calls, and leaves the vulnerability hook out

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Real signals, blob store and queue | Valkey, a blob client with its guard and the job queue | Identical to a replica | Needs Valkey credentials and the worker's registry in a pod that must hold neither |
| (b) Stand-ins that do nothing or refuse (chosen) | Signals that publish nothing; a blob store and a job queue that return an error; no vulnerability hook | A removal of cached files needs none of them: it releases references in the database and calls the format hooks; any other use fails loudly instead of writing | A future hook that enqueues a job on removal would fail the restore at step 7 until it is wired |

**Choice:** (b). The change log is not written either: replicas that start after the restore read the epochs and tables from the database, and the fix-up raised `cluster_epoch` (R-BAK-022). The advisory tables are empty after a restore (R-VULN-026), so the vulnerability hook would evaluate nothing.
**Rationale:** A refusing stand-in turns a wrong assumption into a failed restore with a message, which the Job reports and an operator can act on. Revisit when a storage hook enqueues a job.
**Applied:** R-BAK-008, R-BAK-021 step 7; `internal/app/wire_restore_items.go`, `test/integration/backup/restore_caches_test.go`.

### Q-BAK-23 · The database half of the snapshot is pgx code in `internal/meta/postgres`, its session settings are startup parameters, and the backup role holds one pooled connection

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The `Snapshotter` implemented in `internal/meta/postgres` as the cut said | The pgx file implements the port of `internal/backup` | One file | `internal/meta/postgres` is infrastructure and cannot import the domain package that defines the port, its exit codes and `format.BlobIterator` |
| (b) `postgres.BackupSnapshot` with plain types in `internal/meta/postgres/backup_snapshot.go`; `backup.PGSnapshotter` adapts it and maps its errors to the exit codes (chosen) | The pgx half knows connections, locks, `COPY` and the catalog; the adapter in `internal/backup` (like `PGStore`) gives it the port's shapes | Layering holds; the snapshot protocol is in one file | Two files for one port |

**Choice:** (b). The session settings of R-BAK-005 step 2 are not issued as `SET` statements: the architecture test reserves session-level `SET` for the migration runner (R-ARCH-019), so `statement_timeout = 0`, `idle_in_transaction_session_timeout = 0` and `lock_timeout = backup.database.lock_timeout` are startup parameters of the snapshot and export connections, and the wait for the migration lock polls `pg_try_advisory_lock_shared(3, 0)` for at most `backup.database.migration_wait` instead of one `pg_advisory_lock_shared` under a 15-minute `lock_timeout` (the same effect, and the wait can name the holder). For the role `backup`, `postgres.Open` opens one pooled connection of class `backup` on the session host (it serves the run row, the heartbeat and the pins) instead of the request and control pools, so a run holds `2 + backup.database.parallel` connections, all named `trove:<pod>:backup`; export connections open on first use. A server in recovery is refused before the pool opens, by a probe with any target session attribute (exit `2`, `pg_is_in_recovery`).

**Rationale:** The criterion "never more than 4 connections of the class `backup`" holds only if the pod's own pool is one of them. The development agent would revisit this if a run needs a second control connection, for example to let the heartbeat bypass a long pin insert.

**Applied:** R-BAK-005; `internal/meta/postgres/backup_snapshot.go`, `db.go`, `internal/backup/pgsnapshot.go`.

### Q-BAK-24 · A failure of the pruning phase after the manifest is a warning of the run, not a failed backup

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The run fails (exit `1`) | Retention and the sweep are part of the run | A broken retention is loud | The Job retries and writes a second, identical backup; the first is complete and stays |
| (b) `completed_with_warnings`, exit `0`, the cause in `result.warnings` (chosen) | The manifest is the commit point; what follows cannot undo it | No duplicate backups; the API and the UI show the warning | A retention that never works is only a warning, visible on the screen and in the log |

**Choice:** (b). A run that was taken over (`ErrSuperseded`) still stops: it must not delete. The next run prunes again.

**Rationale:** The completeness of a backup does not depend on the pruning (R-BAK-007 safety). The development agent would revisit this if operators report storage growing unnoticed, which would call for an alert on a warning of the pruning phase.

**Applied:** R-BAK-009 (state `pruning`); `internal/backup/create/finalize.go`.

### Q-BAK-25 · A prefix that holds backups or pool objects but no marker is exit 4

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Write the marker whenever it is missing | Any run that finds no marker writes one | Self-healing after a deleted marker | A target someone else filled is claimed silently; two installations with the same prefix and no marker both claim it |
| (b) Write it only on an empty prefix; a prefix with objects below `backups/` or `pool/` and no marker is exit `4` naming the prefix (chosen) | R-BAK-002's "the first run on an empty prefix writes it" taken literally | A foreign target is never mixed into | An operator who deleted the marker by hand writes it again by hand |

**Choice:** (b).

**Rationale:** The marker is the only guard between two installations on one target (Q-BAK-4 leaves that case to it). The development agent would revisit this if restores of hand-copied targets turn out to be common.

**Applied:** R-BAK-002; `internal/backup/create/preflight.go`.

### Q-BAK-26 · The lease handle serialises a state change with a heartbeat

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Leave the handle as the contract commit built it | A heartbeat reads the state, then writes fenced on it | No change | A `Transition` or `Fence` that commits between the read and the write makes the heartbeat match no row, which reads as a superseded run: a healthy run cancels itself (found by the integration test of the backup role, once in several runs) |
| (b) One mutex around a state change (database write and assignment) and around a heartbeat's read and write (chosen) | `Handle.move` | The fence means what R-BAK-010 says | A heartbeat waits for a state change that is a single short statement |

**Choice:** (b).

**Rationale:** The fenced heartbeat is the mechanism that cancels a stalled run; it must not fire for the run's own transitions. A unit test moves the state 400 times against a 1 ms heartbeat. The development agent would revisit this if a transition ever becomes a long operation.

**Applied:** R-BAK-010; `internal/backup/handle.go`, `internal/backup/create/run_test.go`.

### Q-BAK-44 · A retry pod of the same Job takes over a dead run at once; any other pod meets the heartbeat rule

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-BAK-010 let a starting pod treat a run with a heartbeat younger than 3 × `heartbeat_interval` (45 s by default) as alive and exit `3`. A Job's retry pod after a `kill -9` or an OOM kill starts within seconds to a minute, so it would meet exactly that heartbeat, exit `3`, and the pod failure policy would fail the Job: the takeover the requirement describes would never happen for the case it was written for.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A shorter stale threshold | 3 × heartbeat becomes 1 × or a fixed few seconds | No new input | A run that is merely slow to heartbeat (a database stall, a GC pause) is declared dead and a second Job overlaps it; the fence still protects the manifest, but the overlap is the case exit `3` exists for |
| (b) The pod failure policy retries exit `3` | `3` leaves the `FailJob` list | No new input | Kubernetes' backoff starts at 10 s and is not configurable per Job, so the retry still meets a young heartbeat; and an independent overlapping Job would retry as well instead of failing at once |
| (c) The run row records its Kubernetes Job; the same Job waits, any other pod gets exit `3` (chosen) | `backup_run.job_name`, from `TROVE_JOB_NAME` (the pod's `job-name` label through the downward API) | A Job runs one pod at a time, so a run of the same Job whose pod is not the starting one is dead by construction; an overlapping independent Job or a manual run has another or no identity and still gets exit `3` | A column and an environment variable of the chart; a Job with parallelism above 1 would break the premise (the chart renders 1) |

**Choice:** (c). The starting pod of the same Job polls every `heartbeat_interval` until the dead run's lease has passed, for at most `lease_duration` + `heartbeat_interval`, then inserts its row, which abandons the old run and deletes its pins. If the lease does not pass in that time (a run that keeps heartbeating, which a single pod of the Job cannot do), the command exits `3`. The fencing of R-BAK-010 stays the safety net should the old process still run.

**Rationale:** The Job identity is the one fact that tells a retry from an overlap, and Kubernetes guarantees the premise. The wait for the lease is kept (instead of an immediate takeover) so that nothing changes for the fencing proof: the old run's hold ends by its lease as before. The development agent would revisit this if the Job is ever rendered with parallelism above 1.

**Applied:** R-BAK-010 (text and acceptance criterion); migration `00049_backup_run_job.sql`, `queries/backup.sql`, `internal/backup/slot.go`, `internal/backup/create`, `cmd/trove/backup_create.go`; chart `templates/_backup.tpl` (`TROVE_JOB_NAME`), `test/chart/backup_test.go`.

### Q-BAK-50 · The backup login writes its audit events through a function, not through INSERT on the audit trail

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `INSERT` on `audit_event` as before | The run uses the audit writer | The writer's log copy and registry checks | The login can write any event under any actor, including `authz.*` and `system.restored` |
| (b) A `SECURITY DEFINER` function that writes the four backup events as `system:backup` (chosen) | `backup_record_event(action, outcome, replica, reason, details)`; `EXECUTE` for the backup login and the runtime role, none for PUBLIC | The login can forge a backup event at most, and only with the outcomes `success` and `failure`; no new role, no second connection | The writer's registry validation of `details` is not applied (the function does not check the schema), and the log copy is a line of the backup process |
| (c) The run writes audit through the connection of another role | A second credential in the backup pod | Full writer | A second secret in the pod that holds the target credential: more to steal, not less |

**Choice:** (b). Migration `00050_backup_audit_function.sql`; `backup.PGEvents` is the writer of the create and delete runs. Two findings of the same audit are settled with it: grants given to a role that `database.backup_role` no longer names are revoked (the migration records the role in `system_state` and revokes exactly what it granted), and `ALTER DEFAULT PRIVILEGES` without `FOR ROLE` was verified to reach the tables and audit partitions that a non-superuser migration login creates later (it applies to the login that ran it, which is also the owner of the `SECURITY DEFINER` function that attaches partitions).

**Rationale:** The remaining forgery, a false `system.backup.completed`, is the one H-555 already names for the run record. The development agent would revisit this if the events gain fields that need the registry's schema check.

**Applied:** R-BAK-015; `internal/meta/postgres/backup_grants.go`, `internal/backup/pgevents.go`, `internal/backup/pgfacts.go`; `test/integration/backup/create_audit_test.go`.

### Q-BAK-51 · A server-side copy is bounded by a deadline derived from the object's size, ticks the run through a callback on the object reference, and a copy that outlasts its bound makes the caller stream

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The byte-idle watchdog judges a copy | The request is cancelled after `idle_transfer_timeout` without a body byte | Nothing new to configure | `CopyObject`, `UploadPartCopy` and the completion of a multipart upload have no body whose bytes could show progress; the store answers when the work is done, so every copy longer than 60 s was cancelled with a bare cancellation that `auto` did not read as "stream instead" |
| (b) A deadline of 2 minutes plus the size at 8 MiB/s per request; progress by callback; an expired bound is `ErrServerCopyUnavailable` (chosen) | `CopyObject` is bounded by the whole object, each `UploadPartCopy` by its part, the completion by an eighth of the object; the HTTP client of these requests has no response-header timeout; `ObjectRef.Progress(copied)` is called after each part and every 10 s while a request is outstanding; a bound that expires aborts the multipart upload within 10 s and returns `ErrServerCopyUnavailable` | The run's lease stays alive for as long as the copy is allowed to take; `auto` streams after a copy that cannot finish, `always` fails it; a stuck store cannot hold a run forever | The rate is an assumption: a store slower than 8 MiB/s inside itself falls back to streaming, which is slower still; `WithCopyBound` changes it in tests only |

Streaming uploads of unknown size take their part size from `PutOptions.SizeHint` (at least `ceil(size / 9,000)` rounded up to 1 MiB, an error up front above 5 TiB); without a hint the part size grows by the configured size every 1,000 parts, which reaches about 850 GiB instead of 156 GiB. A download's idle watchdog runs only while a read waits for the network, so a consumer that pauses cannot fail it. An abort of a multipart upload runs detached from a cancelled context but under a 10 s deadline.

**Choice:** (b).

**Rationale:** A copy is the one request the lane makes whose duration is the store's, not the network's; the only honest limit is a function of the size. The development agent would revisit the rate when a store is measured below it, and could then make it a key of `backup.copy`.

**Applied:** R-BAK-006, R-BAK-013, R-BAK-005; `internal/backup/target/store.go` (`ObjectRef.Progress`, `PutOptions.SizeHint`), `internal/blob/s3/target_copy.go`, `target_writer.go`, `idle.go`, `blobsink.go`, `client.go`.

### Q-BAK-52 · The pruning phase reports a listing page as progress, bounds every delete by `finalize_timeout`, and keeps blob-list decoders small

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Tick only on deletions | As built first | No change | A pool with many kept keys and few orphans lists for minutes without a tick; the lease is not renewed and the fence before the first delete batch fails as `superseded`, so the sweep never completes |
| (b) Tick per listing page, bound each delete, small decoders (chosen) | `listing.PageSize` (1,000 keys) is one tick in the pool listing, the scan of `backups/` and the listing of a backup directory; every delete request of the phase runs under `finalize_timeout`; blob lists are written with a 1 MiB zstd window | The fence margin of R-BAK-010 holds for the whole phase; a zombie delete run cannot finish a batch after a later run has taken the slot | The window shortens the match range of a list, which is sorted hex lines and does not need one |

**Choice:** (b). A delete that reaches its deadline is a temporary failure (exit 75), as is any database or target error that `backup.Classify` calls temporary; other errors stay exit 1. Measured: with the default window an open blob-list reader held about 10 MiB, so a sweep or `verify --all` over 100 retained backups held about 1 GB; with the 1 MiB window about 2.4 MiB (`TestR_BAK_017_OpenBlobListReadersStayInBoundedMemory`, 100 readers: 240 MB). Memory still grows with the number of retained backups.

**Rationale:** R-BAK-013 names a listing page as progress. The development agent would revisit the memory bound when retained backups reach the hundreds; the merge would then run in bounded groups of lists.

**Applied:** R-BAK-010, R-BAK-013, R-BAK-016, R-BAK-017; `internal/backup/retention`, `internal/backup/listing`, `internal/backup/classify.go`, `format.NewBlobListWriter`.

### Q-BAK-53 · A long statement of the restore is progress for a bound, the streaming sink is bounded in memory, and a finished restore of the same backup is not an error

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Progress only per file read and per phase | The text of R-BAK-013 for the restore as built | Nothing to add | A statement that reads no file (the insert from a stage table, a fix-up, `storage_usage_recount()`, a backfilling migration step, the deletion of unreferenced blob rows) can run longer than `backup.stall_timeout`; the watchdog cancels it, it rolls back, and the next pod repeats it forever |
| (b) A keepalive around exactly those statements, bounded by four times `backup.stall_timeout` (chosen) | `Restorer.whileTicking` reports progress while the statement runs, for at most the bound | A long but healthy statement is not a stall; a hung one still is, after the bound | The bound is a constant multiple, not a key |
| (c) No watchdog during the database phases | Hang detection only in the transfers | Simple | A hung database connection holds the migration lock and the Job for ever |

The streaming sink buffers an object whole only up to 8 MiB whatever `storage.s3.multipart.threshold` is, uploads larger objects in parts of `multipart.part_size`, and bounds the bytes all concurrent writes hold together by a budget of 256 MiB (a part always fits). A restore Job with 16 copy workers and a 2 GiB limit therefore cannot be pushed out of memory by many large blobs.

A pod killed after the commit of step 9 and before its exit leaves a database with no anchor, no marker and `restored_from.backupId` equal to the backup: the next pod ends with exit 0 and says "already restored". Without `restored_from` of that ID the database is a foreign one and check 4 refuses it as before. Errors of the object store are mapped like the database's: a transient one or an open circuit exits 75, a refused credential or bucket exits 2, the rest exits 1.

**Choice:** (b), the memory bound, the already-restored rule and the mapping.
**Rationale:** A restore that is retried by the Job must make progress on every attempt; each of the three cases would otherwise repeat for ever or fail a Job that succeeded. Revisit if the bound needs to be a key, or if a statement of the restore needs more than four times the stall timeout.
**Applied:** R-BAK-013, R-BAK-021; `internal/backup/restore/restore.go` (`whileTicking`, `alreadyRestored`, `classify`), `internal/meta/postgres/backup_load.go` (`LoadPlan.Keepalive`), `internal/blob/s3/blobsink.go`.

### Q-BAK-54 · A blob whose row is not ok is listed as damaged and not copied, and the previous blob list is verified while it streams

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Copy by digest only, as first built | Only a missing object or a wrong size is damage | Simple | A blob with `content_status = 'corrupt'` and the right size is copied and, via the previous list, never copied again: after the repair every later backup still points at the corrupt pool object and a restore serves it as `ok` |
| (b) The snapshot reads the status; a blob that is not `ok` goes to `blobs.damaged` with it and is not copied (chosen) | `format.BlobEntry.Status`; the diff skips the pin; the previous list's `damaged` entries are not "known", so the first backup after the repair copies the blob over the old pool object | The pool never gains a copy of a blob the installation itself calls damaged; the repair of R-STOR-027 reaches the backups | A backup of an installation with damaged blobs ends `completed_with_warnings` until they are repaired (it did already for a missing object) |

The same change checks the previous blob list the way the sweep checks the lists it merges (Q-BAK-30): the run hashes and counts it while the diff streams it, reads the rest after the merge, and fails with `pool_damaged` on a mismatch with the manifest's `blobs.sha256` and `blobs.referenced`, before the hold turns `pinned`.

**Choice:** (b) and the verification.

**Rationale:** The completeness of a backup rests on the previous list (R-BAK-007 safety); a list that is not the one its manifest names must not decide which blobs are skipped. The development agent would revisit this if the damaged set grows large enough that a per-run list in memory matters (it is the set of non-`ok` rows, normally empty).

**Applied:** R-BAK-006; `internal/backup/format/bloblist.go` (`Status`), `internal/meta/postgres/backup_snapshot.go` (`BlobStream`), `internal/backup/create/diff.go`.

### Q-BAK-55 · Shutdown and exit codes of the create run: aborts have a deadline, a stall decides its exit code, a throttled wait is progress

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) As first built | Aborts of open uploads run detached with no deadline; the error a cancelled stage ended with decides the exit code; a limiter sleeps silently; an unreadable marker is exit 1 | Nothing to change | A slow target keeps a run past the grace period of its pod, so it ends `abandoned` instead of `interrupted`; a stalled run whose last error carried an exit code of its own exits with that one and is not retried; a throttled copy longer than the lease loses it; a foreign object at the marker's key is retried instead of refused |
| (b) Each abort runs under `Deps.AbortTimeout` (8 s) and all of them in parallel; `ErrStalled` and `ErrSuperseded` decide the exit code (75 `stalled`, 1 `superseded`); the rate limiter ticks while it sleeps; a marker that does not decode is exit 4 (chosen) | | The run ends inside the grace period; the Job retries a stall; a throttled copy keeps its lease | A multipart upload whose abort timed out is left to the target's lifecycle rule (R-BAK-015) |

**Choice:** (b). The server-side copy's own progress is the callback of Q-BAK-51 (`ObjectRef.Progress`), set by the pipeline to tick the run's handle; the streaming copy passes the size of the object as `PutOptions.SizeHint`.

**Rationale:** R-BAK-013 defines progress as anything the run waits on within a bound, and R-BAK-012 gives the pod 30 s to end. The development agent would revisit the abort deadline if pods get a shorter grace period.

**Applied:** R-BAK-012, R-BAK-013, R-BAK-002; `internal/backup/create/create.go`, `run.go`, `util.go`, `copy.go`, `preflight.go`.

### Q-BAK-45 · The restore drill runs in a second namespace of the dev cluster

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-BAK-021 and R-HA-060 name the drill: "restore the newest backup of the local profile into a second namespace with a fresh CloudNativePG cluster and a fresh SeaweedFS bucket". The suite needs the pieces of a second installation and a choice of where they run.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A second kind cluster | A cluster of its own for the restored installation | Nothing is shared with the source | Two clusters on one runner; the target and the blob store would have to be reachable across the Docker network; the operators, the PKI and the images are installed twice |
| (b) A second namespace of the dev cluster, with a database, a Valkey and a bucket of its own (chosen) | `deploy/dev/restore-ns.sh`: CloudNativePG cluster, Valkey with Sentinels, an empty bucket in the dev SeaweedFS, a copy of the dev CA, of the S3 login and of the key ring | The operators and the PKI exist once; the restore Job, the migration Job and the roles are the chart's, installed with the sequence of R-BAK-021; about 25 s to set up | The S3 login and the key ring are copies of the source's (the key ring has to be: R-BAK-020 check 3); the object store is shared, so credential separation is not exercised (H-566) |
| (c) The same namespace with a second release | `helm install trove2` beside `trove` | Least setup | Names collide (`trove-db`, `trove-tls`), the NetworkPolicies of the source admit the new pods, and "empty database" would be a second database of the same cluster |

**Choice:** (b). The restored installation has one replica of each role, no edge and no identity provider; the test reaches it through port-forwards to its Services and a client pod runs `npm view` against its Service name.

**Rationale:** (b) is the shortest path that still exercises everything R-BAK-021 lists: the empty database with the schema and no relation, the empty blob prefix, Secrets in place, the Job as an ordinary Job and the final `helm upgrade`. The development agent would revisit it if a production-like separation of the credentials becomes part of the drill (H-566).

**Applied:** `deploy/dev/restore-ns.sh`, `restore-ns.yaml`, `values-restore.yaml`; [e2e.md](../developer/e2e.md#the-backup-tests).

### Q-BAK-46 · The drill judges files by their bytes: untrashed items through the API, every referenced blob through the objects of the restored bucket

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-BAK-029 scenario 1 says that every untrashed and trashed file item returns bytes whose SHA-256 equals its digest, "`GET /files/…` (or the trash API)". The trash can has no API yet (R-STOR-022 is P1), so a trashed item cannot be fetched.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Judge the untrashed items only | `GET /files/…` for each | Simple | The trashed item, whose blob the backup must keep, is not looked at |
| (b) `GET` for untrashed items, and a Job in the restored namespace that reads the object of every referenced blob from the restored bucket and hashes it (chosen) | The blob keys derive from the digests (R-STOR-001), so reading `sha256/<h0h1>/<h2h3>/<hex>` and hashing it is the check, whoever references the blob | Covers the trashed items and every other reference; independent of the API | The Job reads the bucket with the S3 login; a blob that is referenced twice is read once |
| (c) A debug route in the release image | A route that serves a trashed item | Direct | A route the product does not have, only for the test |

**Choice:** (b). The load marks a file item trashed with its `trashed_at` column (no API exists), empties the trash by deleting those rows after five seconds, and so the garbage collector with grace period 0s meets the blobs of trashed and of emptied items.

**Rationale:** The invariant of the backup is on blobs, not on routes: a blob that an item (trashed or not) references must be in the restored bucket with its digest. (b) checks exactly that. The development agent would revisit it when the trash API exists (R-STOR-022): then the trashed items are fetched through it as well.

**Applied:** `test/e2e/backup_drill_test.go` (`blobObjectsMatch`), `test/e2e/backup_env_test.go` (the load).

### Q-BAK-47 · The scenarios of R-BAK-029 that need no cluster run against the integration stack, the others on the cluster

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-BAK-029 lists ten scenarios and says "Go integration tests … plus an end-to-end drill in kind". The cut gave scenarios 1, 2, 5, 7 and 10 to the integration tier.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Everything on the cluster | Each scenario is a test of `test/e2e` | One place | A change that touches the lane would wait for a kind leg of 40 minutes to learn that scenario 5 broke |
| (b) Scenarios 1, 2, 5, 7 and 10 also as integration tests; 1, 3, 8, 9 and 10's journal parts on the cluster (chosen) | `test/integration/backup/scenario_*_test.go` use the real S3 store, PostgreSQL, the create pipeline, the restore and replicas started in the test process | Per-change feedback in seconds to a minute; each scenario is judged with the real adapter | Scenario 1 runs there with generic files, not packages (the drill adds npm and NuGet) |

**Choice:** (b). Scenario 4 (hold and lease) and 6 (restore across versions) are the criteria of R-BAK-007 and R-BAK-023 and keep their tests (`create_hold_test.go`; scenario 6 is H-568).

**Rationale:** The integration tier runs on every change and covers the logic; the cluster adds what only Kubernetes does: Jobs, pod failure policy, eviction, a second namespace. The development agent would revisit it if the integration stack gets a Kubernetes.

**Applied:** `test/integration/backup/scenario_{1,2,5,7,10}_test.go`, `scenario_env_test.go`; [backup.md](../developer/backup.md).

### Q-BAK-48 · The timings of scenario 9 are a JSON artifact of the drill, measured on the data of the drill

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Scenario 9 says the drill "reports the restore duration per phase, and the backup's copy throughput per mode (`server`, `stream`); the numbers are measured, not promised", and the acceptance criterion says the drill "publishes its timings as a job artifact".

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The test log | `t.Logf` | Nothing to build | Not an artifact; it is read by hand |
| (b) `backup-timings.json` in the artifact directory (chosen) | The drill writes the durations of the backup Job, of the restore Job with `phasesSeconds` of the restore's JSON output, of the final `helm upgrade` and of the drain tests; the leg uploads `.out/e2e/` | Machine-readable; one artifact per run, kept 14 days | The reference data set is small (the medium profile of R-HA-050 is not built on kind); the numbers are those of the data set the drill uses |
| (c) A metric | A gauge pushed somewhere | Trendable | A Job exits; nothing scrapes it |

**Choice:** (b). The reference data set is the drill's own; the medium profile of R-HA-050 belongs to the performance runs, not to the drill. The copy throughput per mode is measured by the Jobs of the drill with a bandwidth cap, so it is a bound check, not a throughput claim.

**Rationale:** The spec asks for measured numbers and an artifact; it does not ask for the medium profile on a laptop-class kind cluster. The development agent would revisit it when the nightly performance workflow runs the medium profile.

**Applied:** `test/e2e/backup_env_test.go` (`timings`), `.github/workflows/performance.yml` (job `e2e-backup`).

### Q-BAK-60 · E5.12's CI bullets run in the backup and restore of `TestBackupJournal`, with the issuer double started by the set, against a restored installation configured like its source

Status: Decided by the development agent — selected from the options below; the owner reviews it at the walkthrough.

Context: roadmap E5.12 asks that a replayed disable of a CI mapping also suspends every later enabled mapping of its tier
([gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-020, Q-CI-11) and that a single-use subject token issued before the restore finished
answers `subject_token_predates_restore` (R-CI-018, R-CI-029). The integration tier covers both against a restored database; on the
cluster they need the issuer double of `deploy/dev/oidcstub.yaml`, which the sets `full`, `access`, `access-long`, `ci` and `ci-ha`
start and `backup` does not. The second installation is configured by `values-restore.yaml`, whose base URL is
`https://localhost:8080` whatever port the source's edge has (8102 in the nightly leg `e2e-backup`), and which has no copy of the dev
CA at the path the source mounts it (`/var/run/secrets/trove-dev/keycloak-ca.crt`, named by the issuer's `caBundleRef`): a restored
issuer could neither accept the source's audience nor fetch its keys.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Stay at the integration tier | As today | Nothing to build | The restore Job's replay and the restored installation's exchange never run together |
| (b) A test of its own in the set `backup` | Its own backup and restore | Isolated | One more cycle of about 4 minutes |
| (c) In the cycle of `TestBackupJournal` (chosen) | The set starts the double (`TROVE_E2E_OIDCSTUB=1`). Before the snapshot: an issuer of the double with single use and a clock skew of 5 s, a read policy on `bak-npm`, four mappings of the system tier: M0 (priority 5, project R), M1 (10) and M2 (20, read and deploy) for project P, M3 (30, project Q); an exchange for P at the source matches M1. After the snapshot: M1 disabled, a token for R minted. After the restore: the replay lists M1 applied and M2 and M3 suspended, M0 stays enabled; the token minted before the restore finished answers `400 subject_token_predates_restore`; a token minted after `restored_at` plus the skew exchanges through M0, and its second use answers `subject_token_replayed`; a fresh token for P answers `no_matching_mapping` | One cycle; the setup is a few requests | `TestBackupJournal` grows by about a minute |
| (d) In the set `ci-ha` | The CI fixture is there | The double and the reference setup exist | The set has no backup harness and runs the scatter profile on a hosted runner; the second installation would join it |

**Choice:** (c). The second installation is configured like its source, as R-BAK-021 assumes: `restore-ns.sh` sets
`config.server.base_url` from the edge port of the cluster, as `up.sh` does, and `values-restore.yaml` mounts the dev CA from the
namespace's `trove-tls` at the source's path and lists it in `proxy.outbound.tls.ca_files`. The Jobs check the configured CA files at start, so
`values-restore.yaml` (and `values-dev.yaml`, where the backup runs) sets `backup.extraVolumes` and `restore.extraVolumes` with the mounts;
the global `extraVolumes` stay the Deployments' (Q-HA-149 holds). The vulnerability drill needs the same
trust for its first sync ([vulnerability-scanning.md](vulnerability-scanning.md) Q-VULN-127).

**Rationale:** the bullets are two more things a restore withholds, in the same cycle as the others of E5.12. The development agent
would revisit it if `TestBackupJournal` passes 15 minutes on the dev cluster: then the CI part moves to a test of its own (b).

**Applied:** `test/e2e/backup_journal_test.go`, `test/e2e/backup_ci_test.go`, `deploy/dev/e2e.sh` (set `backup`),
`deploy/dev/restore-ns.sh`, `deploy/dev/values-restore.yaml`; [e2e.md](../developer/e2e.md#the-backup-tests).

## What this does not cover

### H-46 · Backup integrity is not authenticated

Component: the target and `trove backup restore` (R-BAK-003, R-BAK-021). Subject: every principal
the restored database knows. A principal with write access to the target can replace a manifest,
table files and blob lists with a consistent forgery; the checksums are inside the manifest and
prove nothing about who wrote it. A restore of such a backup loads, for example, an extra `admin`
role binding or a token hash whose token the forger knows. Live in the MVP whenever someone other
than the backup role can write to the target, or the backup role's credential is compromised; it
becomes real at restore time. Mitigation until manifests are signed with a key kept outside the
target (Deferred): restrict writers to the backup role (R-BAK-015), enable Object Lock so existing
backups cannot be replaced, and restore only backups whose ID and time match the run history of
the source (`GET /api/v1/system/backups`) or the audit log stream.

### H-47 · The backup role can read everything and delete backups

Component: the backup pod and its credentials (R-BAK-015). Subject: all data of the installation.
The backup role reads every table (including sealed secrets, token hashes and personal data) and
every blob, and the target writer may delete objects in the target. A compromised backup image,
pod or credential can therefore exfiltrate the installation's data and destroy its backups. Live in
the MVP in every installation with backups enabled. Mitigation: a dedicated ServiceAccount and
cloud identity, restricted `get` on Secrets in the namespace, the egress NetworkPolicy of
[ha-and-helm.md](ha-and-helm.md) R-HA-042 for the backup pod (database, blob store and target
only) and Object Lock on the target so deletions only add delete markers. The release image is not signed
(owner decision of 2026-10-29), so no signature check stands behind the backup pod's image; see
[H-79](ha-and-helm.md#h-79--artifacts-are-unsigned-and-nothing-checks-their-origin).

### H-48 · Database files are readable to every reader of the target until P1

Component: table files in the target (R-BAK-026). Subject: personal data, audit records, token
hashes and the permission model. Server-side encryption is transparent to any principal with
`s3:GetObject` on the target (and `kms:Decrypt` with SSE-KMS), so whoever can read the target reads
the database. Live in the MVP; dormant once R-BAK-027 (P1) is configured, after which reading
needs the escrowed age identity. Mitigation in the MVP: a target in a separate account or project,
readers limited to the backup and restore identities, and SSE-KMS with a key policy that names
only those identities.

### H-558 · A server-side copy into the blob store is checked by size, not by content

Component: `trove backup restore` step 3 (R-BAK-021, Q-STOR-164). Subject: the content of every restored blob. A pool object that was
altered in place keeps its key, and when the blob store copies it server-side the restore confirms only that an object of the listed
size exists at the new key; the SHA-256 of the bytes is compared when the restore streams, not when the store copies. The tampered
bytes would be served until the blob is checked (R-STOR-027) or `trove backup verify --deep` hashes the pool. Live in the MVP for
every restore that finds the server-side copy available, whenever someone can write to the pool (H-46); the clients' own digest checks
(npm integrity, NuGet package hash) catch most of it. Mitigation: Object Lock on the target, and `trove backup verify --deep` before
a restore whose source is not trusted.

### H-556 · Adopting a target and writing its marker are not atomic on a store without conditional writes

Component: the target marker and `--adopt-target` (R-BAK-002). Subject: the lineage of the backups of an installation. The marker is written with a conditional create where the store supports it; adoption rewrites it after a compare in the process, with a plain write, because the target port has no write conditional on the version of the existing object. Two restored installations that adopt the same target at the same moment can both pass the compare, and the later write wins; the first one's next run then exits `4`. On a store that answers a conditional create with `501`, two installations that start on an empty prefix at the same moment can both write a marker. Live in the MVP only for a drill that restores one backup twice and starts both `create` runs together, or for two installations configured with one empty prefix on such a store; the loser is refused by the marker check before it writes its manifest. Mitigation: start adopting runs one after the other, use a prefix per installation, and run the target on a store with conditional writes.


### H-553 · A journal object can be overwritten with the runtime credential

Component: the revocation journal in the blob bucket (R-HA-054, R-BAK-024). Subject: the access removals and version reservations recorded after the last backup. The runtime S3 credential may put objects under `_journal/`, so a holder can overwrite an entry with other content or with bytes that do not decode. The replay reports an object it cannot decode and skips it; it applies a forged removal only as a removal, so no access is granted, but the genuine entry is lost: the revocation it recorded is undone by the restore. Live in the MVP in every installation whose journal bucket has no versioning; with versioning and a credential that lists versions, the original version remains and an administrator can recover it by hand. The replay no longer lets a copy or an overwrite choose which entry is believed: it replays every copy of an ID and, with version listing, every version of a key, and flags both (Q-HA-158). What remains is a deletion or an overwrite on a bucket that is not versioned, or a restore that has no credential that lists versions (the chart's restore and post-restore Jobs get one with `restore.journalCredentials` and `postRestore.journalCredentials` set to `default` or `static`; the default `runtime` lists no versions). Mitigation: bucket versioning with noncurrent expiry longer than `backup.recovery_window` on the journal prefix, and a bucket policy that denies `s3:PutObject` over an existing key where the store supports it.

### H-554 · A compromised backup pod can hold garbage collection

Component: `backup_run` and the hold of R-BAK-007. Subject: storage reclamation. The backup role may update its own run row, so a process that keeps heartbeating with progress keeps a valid lease and, with `hold = 'all'`, keeps the collector from moving any blob to `deleting`. Live in the MVP whenever the backup credential is compromised; `TroveBackupGlobalHoldLong` fires after 30 minutes and `TroveBackupRunningLong` after half an expected interval. Storage grows until the run is deleted by hand. Mitigation: the alerts, and the stall detection of R-BAK-013, which ends a run that makes no progress but not one that fakes it.

### H-555 · The run history is only as trustworthy as the backup role

Component: `backup_run`, the API of R-API-050 and the alerts of R-BAK-014. Subject: the statement "the last backup succeeded". The summary, the overdue mark and `TroveBackupStale` are computed from rows that the backup role writes, so a compromised role or a process with write access to the table can record a success for a backup that was never written. Live in the MVP. The check that does not depend on the table is `trove backup list` and `trove backup verify --latest`, which read the target. Mitigation: restrict the grant to the two tables (R-BAK-015), and run `verify` regularly (scheduled verification is P1, Deferred).

### H-557 · A replay writes no audit event per changed object

Component: `trove admin post-restore` and the journal replay (R-HA-025 step 4, R-HA-054). Subject: the restored installation after a platform restore. Post-restore runs `trove admin storage verify` over every live blob and prints the count of referenced blobs whose objects are missing (Q-HA-213); a check that could not run is a line of the output, and the operator then runs the command before resuming deletions. A replay records its changes in its output lines and in one `system.restore.journal_replayed` event, not in one audit event per object (Q-AUTHZ-232), so the audit trail of a restored installation does not say which policy a replay suspended; the output of the run does. Entries naming `ownershipDomain` and the other settings that live in the configuration file are not replayed (R-AUTHZ-031). Live in the MVP in every platform restore. Mitigation: keep the output of the post-restore run, and read the suspended lines.

### H-566 · The drill uses one S3 login for the blob store, the backup target and the restore

Component: the end-to-end suite and the dev profile (R-BAK-015, R-BAK-029). Subject: the separation of credentials the backup role is built on. The dev SeaweedFS has one identity, which `values-dev.yaml` and `values-restore.yaml` give to the blob store, the target and the restore Job. The drill therefore cannot show that a backup role without write access to the blob bucket completes, nor that a restore role with read access to the target and write access to the new prefix only completes. Live today in the suite only. The client-level separation is `TestR_BAK_015_TargetOwnCredentials` (integration). Mitigation: a second identity with a bucket policy per role in the dev SeaweedFS, which its identity model does not offer for this today.

### H-567 · Hand-written journal objects are tried at replay level, not through the restore Job on a cluster

Component: the end-to-end suite (E5.12, R-BAK-024, R-HA-054, R-AUTHZ-031). Subject: the claim that objects written into the journal by hand grant no principal any access. `TestR_HA_054_ForgedEntriesCannotEscalate` and the handler tests run forged entries through the replayer against PostgreSQL; the cluster drill replays only entries the shipper wrote. A Job that reads a bucket in which somebody planted an entry is not run on kind. Mitigation: the replayer applies a handler only to IDs it finds and never grants (ADR-158); the cluster run of the case waits for the planting of an entry from outside the pods.

### H-568 · The restore across releases and across PostgreSQL major versions is not run by the drill

Component: the end-to-end suite (R-BAK-023 criteria, R-BAK-029 scenario 6). Subject: that a backup taken by release N−1 is restored by release N and that a backup taken on PostgreSQL 16 restores onto 18. The drill backs up and restores with one image and one PostgreSQL version. The compatibility of the schema steps is checked by the checksums of R-BAK-020 check 2 and `TestR_BAK_023_*`/`TestR_BAK_020_Preconditions` (an edited migration refuses with exit 6); the CI matrix of R-HA-013 runs the integration tier on both versions but does not restore a backup across them. Before the first stable release a changed migration file needs a new database anyway (Q-ARCH-340), so a cross-release restore has no meaning yet.
