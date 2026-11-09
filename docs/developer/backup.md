# Backup, restore and the revocation journal

Status: the contracts of the lane are built (phase 7, first step): the schema, the on-target format, the ports, the run record with
its lease, the configuration, the command tree, the API operations, the metric families, the audit events and the registries.
`create`, `list`, `verify`, `delete` and `prune` are built; the other operations answer "not built yet" (exit 1) until their work
packages replace the constructors. The
specification is [backup-and-restore.md](../spec/backup-and-restore.md); the decisions behind the cut are
[ADR-157](../decisions.md#adr-157--the-backup-lane-is-cut-into-ports-formats-and-registries-that-its-work-packages-fill-in-files-of-their-own)
and [ADR-158](../decisions.md#adr-158--a-journal-object-is-an-envelope-around-the-producers-entry-and-replay-is-a-registry-of-handlers).

## What to have in your head first

- **A backup is a manifest plus files in an S3 target.** The manifest is written last and is the commit point; a pool of
  blobs addressed by SHA-256 is shared by all backups of a target; the database is exported table by table from one
  `REPEATABLE READ` snapshot. Nothing about this is spelled outside [internal/backup/format](../../internal/backup/format).
- **The database decides who may write the target.** One `create` or `delete` run is active ([Q-BAK-4](../spec/backup-and-restore.md#q-bak-4--where-run-state-and-the-one-at-a-time-rule-live));
  the run row carries the lease, the progress and the garbage-collection hold. `RunStore`, `Handle` and `AcquireSlot` are the
  only code that writes it.
- **A failure is an exit code.** [internal/backup/exit.go](../../internal/backup/exit.go) maps every error to the codes of
  R-BAK-013; the Job's pod failure policy reads them (2, 3, 4 and 6 are final, 75 and 1 are retried).
- **Everything that varies by domain is a registry** filled in a file of the owning package: table classes
  (`tables.Register`), revision columns (`meta.RegisterRevisionColumn`), restore fix-ups (`fixup.Register`), remote-cache purgers
  (`fixup.RegisterPurger`), journal replay handlers (`journal.Register`), audit events with the journal flag.

## Layout

```text
cmd/trove/
  backup.go               dispatcher, backupEnv, exit mapping, output helpers (shared, stable)
  backup_create.go        create                      -> newCreator
  backup_list.go          list                        -> newLister
  backup_verify.go        verify                      -> newVerifier
  backup_delete.go        delete, prune               -> newDeleter, newPruner
  backup_restore.go       restore                     -> newRestorer
  admin_postrestore.go    admin post-restore          -> newPostRestorer
  admin_npm.go            admin npm reinstate         -> npmReinstate (built)
internal/backup/
  listing/                the scan of a target (complete or incomplete), the blob-list reader that checks itself, `backup.Lister`; fixture/ writes test backups
  retention/              policy, the pruning stage, the pool sweep, delete and prune runs, the marker check
  verify/                 `backup.Verifier`, the shared-digest merge, the download throttle
  runstoretest/           in-memory `RunStore` for the tests of the operations that write run rows
  doc.go exit.go state.go ports.go runner.go metrics.go      the contracts
  runstore.go handle.go slot.go                              the run record, the lease, the one-active rule
  format/                 backup ID, layout, marker, manifest (+ JSON Schema), blob list and its merge
  target/                 the port of the S3 target, a leaf package (standard library only) so that internal/blob/s3 implements it; targettest/ is an in-memory implementation for tests
  fixup/                  the registry of restore fix-ups and remote-cache purgers
  tables/                 the table registry (R-BAK-004)
internal/journal/         object, ports, replay handlers and the update rule, metrics
internal/config/backup.go the backup.* keys (R-BAK-011) and the journal keys (R-HA-054)
internal/meta/postgres/
  migrations/00046_backup.sql   backup_run, backup_blob_pin, storage_usage_recount()
  queries/backup.sql            run record, pins, hold check, outbox
  tables_backup.go              export classes of the two tables
internal/audit/events_backup.go the six audit events of the lane
internal/backup/gauges/   the shared reading of the job (ReadJobs, Overdue, LastFailure) and the Collector that sets the gauges
  gaugestest/             an in-memory backup.RunReader for tests
internal/api/v1/backups_api.go  the three read-only operations and the backup block of /system/info
internal/app/wire_backup_gauges.go  registers the lane's families, the gauge source and the API's sources
```

## Responsibilities

| File | Responsibility |
|---|---|
| [format/id.go](../../internal/backup/format/id.go) | `ID`, `NewID`, `ParseID`: `<yyyymmdd>T<hhmmss>Z-<4 hex>` (R-BAK-002) |
| [format/layout.go](../../internal/backup/format/layout.go) | `Digest`, `Layout`: every key below the target prefix, `ParsePool`, `ParseBackupKey` |
| [format/marker.go](../../internal/backup/format/marker.go) | the target marker, `Check` (exit 4), the adoption compare-and-swap |
| [format/manifest.go](../../internal/backup/format/manifest.go) | `Manifest` and its types, `Encode`, `DecodeManifest` (refuses a higher format: exit 6), `Validate` |
| [format/manifest.schema.json](../../internal/backup/format/manifest.schema.json) | the JSON Schema a test validates every manifest against |
| [format/bloblist.go](../../internal/backup/format/bloblist.go) | `BlobListWriter`/`Reader` (zstd text), `Diff` (referenced vs known: new list and to-copy set), `Union` (the keep set), `SumWriter` |
| [target/store.go](../../internal/backup/target/store.go) | `target.Store`: Head, Get, Put, Writer, List (ordered), Delete, Copy, Probe; the errors |
| [target/targettest/conformance.go](../../internal/backup/target/targettest/conformance.go) | `Conformance`: the contract suite the in-memory target and the S3 adapter both run |
| [blob/s3/target.go](../../internal/blob/s3/target.go) | `s3.Target`, `NewTarget`: Head, Get, Put (conditional create), List, Delete, Probe, `CheckSSE` |
| [blob/s3/target_writer.go](../../internal/blob/s3/target_writer.go) | the multipart streaming `Writer` |
| [blob/s3/target_copy.go](../../internal/blob/s3/target_copy.go) | `Copy`: `CopyObject`, or `UploadPartCopy` above the single-copy limit |
| [fixup/fixup.go](../../internal/backup/fixup/fixup.go) | `Register`, `RegisterPurger`, `Fixups`, `Purgers`; the orders of the core fix-ups |
| [exit.go](../../internal/backup/exit.go) | exit codes, failure codes, `ExitCode`, `ErrorCode`, `BusyError`, sentinel errors |
| [state.go](../../internal/backup/state.go) | `Operation`, `State`, `Hold`, `Trigger`, `Progress`, `Run`, `NewRun` |
| [runstore.go](../../internal/backup/runstore.go) | `RunStore`/`RunReader` and `PGStore` over `queries/backup.sql` |
| [handle.go](../../internal/backup/handle.go) | `Handle`: heartbeat, lease renewal only on progress, stall and supersession cancel the context, `Fence` |
| [slot.go](../../internal/backup/slot.go) | `AcquireSlot`: exit 3 for a live run, wait for a stale run's lease (R-BAK-010) |
| [ports.go](../../internal/backup/ports.go) | `Snapshotter`/`Snapshot`, `Facts`, `BlobSource`, `BlobStateReader`, `BlobSink` |
| [runner.go](../../internal/backup/runner.go) | `Creator`, `Lister`, `Verifier`, `Deleter`, `Pruner`, `PruneStage`, `Restorer`, `PostRestorer`, options, results, `NotBuilt` |
| [metrics.go](../../internal/backup/metrics.go) | the families of R-BAK-014 and `Copied` |
| [journal/object.go](../../internal/journal/object.go) | `Object`, `Encode`, `Decode`, `Key`, `ParseKey`, `DayPrefixes` |
| [journal/ports.go](../../internal/journal/ports.go) | `Store`, `Outbox`, `Shipper`, `Reader` |
| [journal/replay.go](../../internal/journal/replay.go) | outcomes, `Handler` registry, `Narrower[T]`, `ApplyUpdate[T]`, `Summary`, `Replayer` |

## Architecture

`create` runs in the `backup` pod: it inserts the run row (`AcquireSlot`), sets the hold, opens the snapshot through a
`Snapshotter`, writes the blob list and the table files through `target.Store`, copies the pinned blobs from a `BlobSource` and
finishes with the manifest. `Handle` heartbeats for the whole time and cancels the run's context when the run is superseded or
stalled. `restore` runs in the restore Job as the owner: it checks, anchors, migrates to the backup's step, copies blobs from the
pool to a `BlobSink`, loads, migrates forward, runs the fix-ups of `fixup.Fixups()` in one transaction, the purgers of
`fixup.Purgers()`, replays the journal through a `journal.Replayer`, verifies and finishes. Both read the same `format` package.

The revocation journal is independent of the backup: the audit writer inserts a `revocation_outbox` row in the transaction of the
change, a worker job (`journal.Shipper`) writes `journal.Object`s to the blob bucket, and a replay (`journal.Replayer`) applies the
handlers that the domains registered. `trove backup restore` and `trove admin post-restore` run the same replay.

### The S3 target adapter

`s3.NewTarget(ctx, cfg config.Backup, f *outbound.Factory, opts ...s3.TargetOption) (*s3.Target, error)` is how `cmd/trove` turns the
configuration into a `target.Store`; `NewTargetWithClient` takes an HTTP client from `internal/outbound` instead of a factory. The
`Target` builds its own client from `backup.target` only (endpoint, region, `ca_file`, credential chain, encryption): it never
reads `storage.s3`, so the runtime blob-store credential cannot reach it (R-BAK-015, Q-STOR-165). `Endpoint()` and `Bucket()` give
the same-bucket check of R-BAK-002 what it compares against the source.

- **Keys** are full object keys; the caller joins `backup.target.prefix` (`format.Layout`). The adapter adds nothing.
- **Writer** collects `backup.database.part_size` bytes, uploads each full part with `UploadPart` and creates the multipart upload
  only when a second part follows; an object that fits one part is one `PutObject`. `Close` returns the object as `HeadObject` reports
  it and sets `SHA256` from the stream; `Head` reports `SHA256` only for a single-request object (a multipart checksum is composite).
- **Conditional create** (`PutOptions.IfAbsent`) sends `If-None-Match: *`; on a store that rejects it the adapter checks with a
  `HeadObject` first (Q-BAK-27). `ErrConditionalUnsupported` is not returned by this adapter.
- **Delete** sends 1,000 keys per request and accepts any number of keys; a missing key is no error.
- **Copy** is `CopyObject` up to the single-copy limit (5 GiB) and `UploadPartCopy` parts of `backup.copy.part_size` above it, both
  with the target's encryption headers. A source on another endpoint, `AccessDenied` or `NotImplemented` return
  `target.ErrServerCopyUnavailable`; the caller decides by `backup.copy.server_side` whether to stream. Tests lower the limit and the
  parts with `s3.WithSingleCopyLimit` and `s3.WithCopyPartSize`, and the writer's part with `s3.WithWritePartSize`.
- **Liveness** (Q-BAK-51, Q-STOR-168). A copy request has no body, so the byte-idle watchdog cannot judge it: each request is
  bounded by `2 min + size / 8 MiB/s` (`WithCopyBound`), runs on a client without a response-header timeout, and a bound that
  expires returns `ErrServerCopyUnavailable` after aborting the multipart upload within 10 s. `ObjectRef.Progress(copied)` is called
  after each part and every 10 s meanwhile (from an adapter goroutine; wire it to `Handle.Tick`). `PutOptions.SizeHint` sizes the
  parts of `Writer`; pass the blob's size from a streaming copy. A download's idle watchdog runs only during a read.
- **Probe** writes, reads back, heads and deletes one object and returns the Head result. `CheckSSE(o)` compares it with
  `backup.target.sse` and names `backup.target.sse.mode` on a mismatch; a store that rejects the encryption headers fails the probe
  with the same hint (R-BAK-026). The stack's SeaweedFS has no KMS and answers an SSE-S3 write with a 500, so the integration tests
  use mode `none` there and `s3` against a store that encrypts (`workingSSE` in the test).
- Conformance: `targettest.Conformance` runs against `targettest.Mem` and against the adapter
  ([target_adapter_test.go](../../test/integration/backup/target_adapter_test.go), `make test-integration`).

## How to add

**A journal handler for an event of a domain.**

1. Flag the event type with `Journal: audit.JournalRevocation` in `internal/audit/events_<area>.go` (already done for the events of
   R-HA-054's table) and pass the entry with the audit event in the transaction of the change.
2. In a file `journal_replay.go` of the package that owns the object, call `journal.Register(journal.Handler{Domain, Events, Apply})`
   from `init`. `Apply` runs in its own transaction as the owner, must be idempotent and must never grant or delete.
3. For an update event implement `journal.Narrower[T]` for the object's access-bearing state and call `journal.ApplyUpdate`.
4. Test with the cases of the owning page's acceptance criteria: `applied`, `already in effect`, `subject unknown`, `skipped`,
   `suspended`.

**A restore fix-up.** Add a file to `internal/backup/fixup/` that calls `fixup.Register(fixup.Fixup{Name, Order, Apply})` from
`init`; the transaction is the one of step 6. A remote-cache purge of a format uses `fixup.RegisterPurger`.

**A table.** `tables.Register(name, class, options)` in the package that owns it, in the same change as its migration; the migration
test fails otherwise (R-BAK-004).

**A metric or an audit event of the lane.** The families are in `backup.NewMetrics` and `journal.NewMetrics`; the events are in
`internal/audit/events_backup.go`. Names are a public contract (R-OPS-008, R-OPS-014).

## Tests

- Unit: `go test ./internal/backup/... ./internal/journal/...`. `targettest.Mem` is the in-memory target (ordered listings,
  conditional create, request counts); `format` has golden shapes and a schema check.
- Integration: [test/integration/postgres/backup_runstore_test.go](../../test/integration/postgres/backup_runstore_test.go) runs the run
  store, the hold check and `storage_usage_recount()` against the shared PostgreSQL (`make test-integration`).
- Command wiring: `TestBackupCommands` in [cmd/trove/main_test.go](../../cmd/trove/main_test.go).
- The scenarios of R-BAK-029 as a suite: `test/integration/backup/scenario_*_test.go` run scenarios 1, 2, 5, 7 and 10 against the shared stack
  with the real S3 store ([scenario_env_test.go](../../test/integration/backup/scenario_env_test.go) has the helpers: `sourceOf`,
  `deepVerify`, `downloadsMatch`). Scenario 1 (`TestR_BAK_029_ConsistencyUnderLoad`) runs a publish, delete, trash and collect loop with grace
  0s while `create` runs and judges the restored installation by the objects its items reference; scenario 2 collects every source blob after
  the backup; scenario 5 runs three backups with `keep_last 2` through the pruning stage; scenario 7 serves npm from the old and the restored
  installation and checks that the old `ETag` gets a `200`; scenario 10 ships the journal, kills the restore during the replay and resumes it.
  The cluster side (the drill into a second namespace, overlapping Jobs, drain and `kill -9`, E5.12) is the `backup` set of the end-to-end suite
  ([e2e.md](e2e.md#the-backup-tests)).

## Restore

`trove backup restore` is [internal/backup/restore](../../internal/backup/restore) over four ports; `cmd/trove/backup_restore.go` joins them.

| File | Responsibility |
|---|---|
| [restore/restore.go](../../internal/backup/restore/restore.go) | `Restorer`: the lock, the checks, the nine steps, the stall watchdog, the exit mapping, the summary |
| [restore/checks.go](../../internal/backup/restore/checks.go) | checks 1 to 8 of R-BAK-020, each an exit 6 that names its number |
| [restore/blobs.go](../../internal/backup/restore/blobs.go) | step 3: workers, server-side copy with the fall-back to streaming, the blob list's SHA-256 |
| [restore/source.go](../../internal/backup/restore/source.go) | a table file as a reader that verifies size and SHA-256 on `Close`; the canary rows of check 3 |
| [restore/ports.go](../../internal/backup/restore/ports.go) | `Session` (implemented by `postgres.RestoreSession`), `BlobStore` (`s3.BlobSink`), `Journal` |
| [meta/postgres/migrate_restore.go](../../internal/meta/postgres/migrate_restore.go) | `RestoreSession`: the lock, `MigrateTo` (the runner's restore mode, R-BAK-023), the marker |
| [meta/postgres/backup_load.go](../../internal/meta/postgres/backup_load.go) | the load transaction: order from the foreign keys, stage tables, `COPY`, sequences, damaged blobs |
| [meta/postgres/backup_restore_state.go](../../internal/meta/postgres/backup_restore_state.go) | inspection of check 4, anchor, phases, `Fix`, `Finish`, the statements of the revision and blob fix-ups |
| [fixup/](../../internal/backup/fixup) `jobs.go system_state.go revisions.go storage_usage.go mirror_state.go` | the core fix-ups of R-BAK-022 |
| [blob/s3/blobsink.go](../../internal/blob/s3/blobsink.go) | the blob sink: server-side copy from the target, streaming with hashing, the prefix listing and the probe |

**Phases and resumption.** The marker `restore_in_progress` carries the phase: `blobs` (set with the last schema step), `loaded` (set by
the load transaction), `fixed` (set with the fix-ups). A step is skipped when the phase is past it, and steps 5, 7 and 8 are idempotent.
`Deps.Hook` names the points (`step2:anchored`, `step2:migrated:<n>`, `step3:copied:<n>`, `load:<table>`, `step5` to `step9`); the tests
stop the restore there, and the kill tests run the binary.

**The journal.** `restoreJournal` in `cmd/trove/backup_restore.go` opens the reader of the source's bucket and prefix with
`replay.OpenJournal` and builds the replayer over the restore's session; `Reachable` (check 8) lists today's day prefix.

**The remote-cache purge (step 7).** The purgers need `fixup.Env.Items`, the storage delete path. `Deps.Items` builds it on the restore's
session; `app.RestoreItems` ([wire_restore_items.go](../../internal/app/wire_restore_items.go)) composes the item store with the format hooks
and nothing else ([Q-ARCH-347](../spec/architecture.md), [Q-BAK-43](../spec/backup-and-restore.md)). A hook that enqueues a job or reads a blob
on removal needs its stand-in replaced there. Test: `restore_caches_test.go`.

**Tests.** Unit: `go test ./internal/backup/restore/ ./internal/meta/postgres/ -run 'R_BAK'` (the checks table, the phases, the load order).
Integration, against the shared stack: `restore_full_test.go` (the end-to-end criteria and the derived state), `restore_checks_test.go`
(every check, the edited migration, the lock), `restore_kill_test.go` (`kill -9` at steps 2, 3 and 4 with the `trove` binary built into
`.out/restore-it`, and the exit codes of the command). A backup is built by hand from the `format` package (`restore_fixture_test.go`).
## The backups screen

`/admin/system/backups` (R-UI-048) renders the three operations of R-API-050 and sends nothing but `GET`. It lives with the other
system screens in [web/src/app/features/system/backups/](../../web/src/app/features/system/backups/).

| File | Responsibility |
|---|---|
| [api/backups-api.ts](../../web/src/app/api/backups-api.ts) | `BackupsApi` (`runs`, `run`, `summary`) over the generated shapes; no method that is not a GET |
| [api/testing/fakes-backups.ts](../../web/src/app/api/testing/fakes-backups.ts) | `FakeBackupsApi`, `sampleRun`, `sampleSummary` for the component specs |
| [backups-page.ts](../../web/src/app/features/system/backups/backups-page.ts), `.html`, `.scss` | the screen: status, run in progress, runs with filters (`operation`, `state` query parameters) and "Load more", the open run, stored backups, restore notice; refresh on the status screen's interval (Q-BAK-36) |
| [backups-model.ts](../../web/src/app/features/system/backups/backups-model.ts) | pure functions: `stateView` (badge tone, icon, word and hint per state, Q-BAK-37), durations, result accessors, `manualRunCommand` |
| [backup-state.ts](../../web/src/app/features/system/backups/backup-state.ts) | the state as the standard badge |
| [manual-run-panel.ts](../../web/src/app/features/system/backups/manual-run-panel.ts) | the `kubectl` command with the copy button; placeholders while `manualRun` is empty |
| [core/http/problem-messages-backup.ts](../../web/src/app/core/http/problem-messages-backup.ts) | one translated sentence per failure code of [exit.go](../../internal/backup/exit.go) (R-UI-042) |

- The Job name of the manual command is `<cronjob>-manual-<unix seconds>`; `manualRunCommand` cuts the CronJob part to 45 characters
  (and drops a trailing separator) so that the name has at most 63. `--from=cronjob/…` keeps the full name.
- A new failure code in `exit.go` needs a case in `describeBackupProblem` and a German unit in `messages.de.xlf`; the build fails
  on a missing translation, and `backups-page.spec.ts` lists the codes it expects to be translated.
- Tests: `backups-page.spec.ts` (component tier, fixtures) and the journey [16-backups.spec.ts](../../web/e2e/tests/16-backups.spec.ts),
  which asserts that every request of the visit is a `GET`. The states that need a cluster (a run in progress, an interrupted
  run, a lease-expired run) are covered by fixtures in the component tier and by the backup suite of `test/e2e`.

## The chart

The CronJob `<fullname>-backup` runs `trove backup create --trigger schedule`, the Job `<fullname>-restore` runs `trove backup restore <id>`
(with `restore.enabled`); both read the shared `trove.yaml`, run as a ServiceAccount of their own and take the exit codes of
[exit.go](../../internal/backup/exit.go) as their failure policy (2, 3, 4 and 6 fail the Job, an eviction is ignored, 75 and 1 are
retried). Both are rendered only with their switch, `backup.enabled` (off by default, Q-HA-17) and `restore.enabled`. The alerts of R-BAK-014
(with `backup.enabled` only) and those of the journal are rules of the chart over the metric names of [metrics.go](../../internal/backup/metrics.go) and
[journal/metrics.go](../../internal/journal/metrics.go): a renamed family fails `TestPrometheusRule`. What the chart renders, the Secrets of
each Job and the tests are in [helm-chart.md](helm-chart.md#the-backup-and-restore-jobs).

## The platform check

The task `backup-check` (R-HA-023, `40 5 * * *`, queue `maintenance`) runs [platform.Checker](../../internal/backup/platform/platform.go) and stores
its report in `system_state` under `backup_check`; every replica sets `trove_backup_check_ok{check}` from that row in its database gauges
(`exportBackupCheck` in [wire_backup_check.go](../../internal/app/wire_backup_check.go), Q-OPS-185). Only what `backup.recovery_window` and
`backup.object_versioning` declare is checked (`archive_mode`, `grace_period`, `object_versioning`, `noncurrent_expiry`); a setting the
credential may not read is unknown and has no series (`s3.ErrProtectionUnknown`). `trove admin backup check` runs the same checks on
demand and exits 5 when one fails. The bucket reads are `VersioningStatus` and `NoncurrentExpiryDays` of
[internal/blob/s3/protection.go](../../internal/blob/s3/protection.go). Tests: `TestR_HA_023_*` in `internal/backup/platform` and
`test/integration/backup/platform_check_test.go`.

## Conventions

- Test names carry requirement IDs (`R-BAK-010`), never decision IDs.
- A package of the lane that needs a new shared shape changes it in `format`, `target`, `fixup` or `journal` and says so in its
  change; it does not copy the shape.
- The AWS SDK is imported in `internal/blob/s3` only (which implements `target.Store` and imports the leaf `internal/backup/target`); pgx in `internal/meta/postgres` and `internal/cluster` only.
- Migrations of the lane start at 46 and are expand steps; a table gets its export class in the same change.

## Target operations: list, verify, delete, prune

Code: [listing](../../internal/backup/listing), [retention](../../internal/backup/retention), [verify](../../internal/backup/verify);
wiring in [cmd/trove/backup_targetops.go](../../cmd/trove/backup_targetops.go) and the three command files. Decisions:
[Q-BAK-28, Q-BAK-29, Q-BAK-30](../spec/backup-and-restore.md#q-bak-28--trove-backup-list---json-is-flat-schemastep-and-keyids-stand-at-the-top-of-each-entry).

| File | Responsibility |
|---|---|
| [listing/scan.go](../../internal/backup/listing/scan.go) | `Dirs` (one listing, no manifest read), `Scan` (newest first, manifests read and validated), `ReadManifest`, `DatabaseBytes` |
| [listing/bloblist.go](../../internal/backup/listing/bloblist.go) | `CheckBlobList` (SHA-256 against the manifest), `OpenBlobList` (decoder that fails on a wrong entry count), `HashObject` |
| [listing/lister.go](../../internal/backup/listing/lister.go) | `Lister`, `Describe` (manifest to `backup.Listing`) |
| [retention/policy.go](../../internal/backup/retention/policy.go) | `Policy.Expired`: not among the newest `keep_last` and older than `keep_within`; the newest is never selected |
| [retention/stage.go](../../internal/backup/retention/stage.go) | `Stage` implements `backup.PruneStage`: marker check, deletions (manifest first, fenced), incomplete directories, sweep when due, `retained`, escrow reminder |
| [retention/sweep.go](../../internal/backup/retention/sweep.go) | `Sweeper`: proves the lists, merges keep set and pool listing in one pass, deletes in batches of 1,000 behind the fence |
| [retention/runner.go](../../internal/backup/retention/runner.go) | `Runner`: the `delete` run (`Delete`, `Pruner()`): reads the facts, takes the slot, heartbeats, ends the row |
| [retention/marker.go](../../internal/backup/retention/marker.go) | `CheckMarker` (exit 4 / exit 2), `RunSweepClock` (reads `poolSweptAt` from recorded results) |
| [verify/verify.go](../../internal/backup/verify/verify.go) | `Verifier`: manifest and file checks, then the pool check over all selected backups at once |

How the create pipeline calls the stage: build a `retention.Stage` (store, layout, policy, `SweepInterval`, `Facts`, `Audit`, `Clock`)
and call `Prune(ctx, handle, &result)` in state `pruning`. The stage fills `Retention`, `Retained`, the deletion counters and
`PoolSweptAt`, appends warnings and sets `Outcome` to `completed_with_warnings` when there are any; `result.BackupID` must be the run's
own ID (its directory is never an "incomplete" one, and its key IDs are what the escrow reminder compares with). An error means nothing
further was deleted.

**How to add a check to verify.** Add the finding in `checkFiles` (a file of the backup) or `checkObject` (a pool object), give it a
`backup.FindingKind` only if the four of R-BAK-019 do not fit, and add a case to `verify_test.go`.

**Tests.** Unit tests run against `targettest.Mem`; `listing/fixture` writes whole backups (real digests, a valid manifest) and
`runstoretest.Mem` stands in for the run table. A store wrapper that reorders `List` produces the out-of-order key of R-BAK-017.
[test/integration/backup/target_ops_test.go](../../test/integration/backup/target_ops_test.go) runs retention, sweep, list, verify and
delete through the real adapter against the stack's S3 store (encryption mode `none`: the stack's store rejects SSE-S3).

Not built: a Facts implementation for the delete runs reads `installation_id`, `incarnation` and the `restore_in_progress` key in
[backup_targetops.go](../../cmd/trove/backup_targetops.go) and does not check the schema step; the create package's `Facts` replaces it.
R-BAK-027 (client-side encryption) is P1 and left: `trove backup list` prints the field when a manifest carries it.

## The read side: API, info block and gauges

One reading of `backup_run` serves every replica-side view, so the API, the UI, the info block and the alerts agree
([Q-BAK-35](../spec/backup-and-restore.md#q-bak-35--the-gauges-of-the-job-are-one-evaluation-of-single-row-reads-per-replica-and-30-seconds)).

| File | Responsibility |
|---|---|
| [gauges/jobs.go](../../internal/backup/gauges/jobs.go) | `ReadJobs`: newest success, newest failure (an ended `failed`/`abandoned` create run, or an active create run whose lease expired; `interrupted` never), the active runs and the database's clock. `Jobs.Overdue` (1.5 x the expected interval, `nil` without a success), `Jobs.LastFailure` (only when newer than the last success), `Jobs.ActiveRun` |
| [gauges/collector.go](../../internal/backup/gauges/collector.go) | `Collector.Refresh`: one evaluation that sets `backup.Metrics` and `journal.Metrics`, reused for `gauges.Reuse` (30 s); a failed read keeps the previous values and is retried at the next call. The last-success and last-failure families are reset first, so they have no series until the event exists |
| [gauges/gaugestest/mem.go](../../internal/backup/gauges/gaugestest/mem.go) | `Mem`: an in-memory `RunReader` with the ordering, filters and keyset of the SQL, a database clock and a call counter |
| [../../internal/api/v1/backups_api.go](../../internal/api/v1/backups_api.go) | `ListBackupRuns`, `GetBackupRun`, `GetBackupSummary`, `backupInfo` (the `backup` block of `GET /system/info`), `BackupServices` (the options: reader, expected interval, `manual_run`, the `restored_from` reader) |
| [../../internal/app/wire_backup_gauges.go](../../internal/app/wire_backup_gauges.go) | `laneMetrics` (registers `backup.NewMetrics` and `journal.NewMetrics` once per replica), `registerBackupGauges` (the database gauge source `backup`), `backupAPI` (the options of the API) |

- **Where the numbers come from.** The pins left (`trove_backup_pinned_blobs`) are `progress.pinsLeft` of the active run, written by
  its heartbeat; no read names the pin table, and a test of the package fails if one does. The duration is `finished_at - started_at`;
  the sizes and the damaged count are the newest successful create run's `result`.
- **Clocks.** A lease is compared with the database's clock, which `RunReader.ActiveRuns` returns
  ([Q-API-206](../spec/api-and-ui.md#q-api-206--a-runs-leaseexpired-and-the-overdue-mark-are-judged-on-the-databases-clock)). A list
  or a single run reads it only when a run of the answer is active.
- **The stored backups** of the summary are the `retained` list of the newest successful create or delete run that recorded a
  `retention`, found among the newest 25 runs of each operation (Q-BAK-41).
- **Who registers the families.** `App.laneMetrics` is the one place; the worker job that ships the revocation journal takes
  `journal.Metrics` from it, because a second `journal.NewMetrics` on the registry panics.
- **Tests.** Unit: `go test ./internal/backup/gauges/... ./internal/api/v1/ -run 'R_API_050|R_OPS_004|R_BAK_014'`. Integration:
  [test/integration/backup/api_gauges_test.go](../../test/integration/backup/api_gauges_test.go) inserts real `backup_run` rows and
  checks the collector over `PGStore` (stepping its clock over the 30 s reuse) and a replica's API, info block and `/metrics`.

## npm and NuGet in backups and restores

The format parts of the lane live in the data packages of the formats and register themselves from `init`; the composition roots
link them (`internal/backup/fixup` imports both, `internal/app` binds them).

| File | Responsibility |
|---|---|
| [formatdata/npm/journal_replay.go](../../internal/formatdata/npm/journal_replay.go) | the handler of `npm.unpublish` (R-NPM-047, Q-NPM-12): writes the `journal` tombstone with the entry's ID, applies the dist-tag rule (`semver.TagsAfterRemoval`), raises `rev_seq`; deletes nothing |
| [formatdata/npm/reinstate.go](../../internal/formatdata/npm/reinstate.go) | `Store.Reinstate`: removes a `journal` tombstone only while the version is stored with the tombstone's integrity, gives a package without `latest` one, raises `rev_seq`, writes `npm.reinstate` |
| [formatdata/npm/restore_purge.go](../../internal/formatdata/npm/restore_purge.go) | `PurgeRemoteCaches`: per npm remote repository the storage delete path, then `remote_cache_entries` and `npm_remote_packages` (R-BAK-008) |
| [formatdata/nuget/journal_replay.go](../../internal/formatdata/nuget/journal_replay.go) | the handler of `package.published` / `package.deleted` (R-NUGET-033): the table of reserved / removed tombstones and `revived`, row by row in `published()` and `deleted()`; `absentStep` is the decision for an absent number as a function of its state alone, so that the end state of a number does not depend on the order of its entries, and an addition or a removal that names other bytes than a reservation turns it into `removed` with a warning (Q-NUGET-112) |
| [formatdata/nuget/restore_purge.go](../../internal/formatdata/nuget/restore_purge.go) | `PurgeRemoteCaches` for NuGet: no tombstone, no index rows beyond the cache entries |
| [backup/fixup/npm.go](../../internal/backup/fixup/npm.go), [nuget.go](../../internal/backup/fixup/nuget.go) | the `fixup.RegisterPurger` registrations |
| [meta/postgres/queries/backup_formats.sql](../../internal/meta/postgres/queries/backup_formats.sql) | the statements of all of the above |
| [cmd/trove/admin_npm.go](../../cmd/trove/admin_npm.go) | `trove admin npm reinstate <projectKey>/<repoKey> <name>@<version>` |
| [backup/tables/classes_test.go](../../internal/backup/tables/classes_test.go) | reads the export-class table of R-STOR-047 from the spec page and compares it with the registrations; the format tables are all `data` |

- **The storage delete path of a purger.** A purger gets `fixup.Env.Items`, a `fixup.CacheEraser`; the restore sets it to the item store
  (`App.Items()`, [wire_files.go](../../internal/app/wire_files.go)), whose `EraseCache` ([items/erase.go](../../internal/items/erase.go))
  removes every file of a remote repository in batches, each in a transaction in which the format's storage hook runs (so the cached
  version rows go and `packages.rev_seq` grows), then the folders. A purger does not delete items itself.
- **`system_state.restored_at` is an RFC 3339 string.** `packages.RestoredAt` parses it with `time.RFC3339Nano`; the finishing transaction
  of the restore must write that form (`to_jsonb(to_char(now() AT TIME ZONE 'utc', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'))`), not PostgreSQL's
  text form. A value that does not parse makes the document revalidation fail and the stored marks decide (the "upstream unreachable"
  branch of `BeforeArtifactWith`), which hides the restore from npm's tarball gate.
- **Tests.** Unit: the entry validation of both handlers, the registrations, the table classes. Integration
  ([test/integration/backup/formats_*_test.go](../../test/integration/backup)): every outcome of both replay tables against the shared
  PostgreSQL with a replica serving the protocol routes on top, the producer's entry read by the handler, `trove admin npm reinstate` run
  as a built binary, the purgers against the npm and NuGet test upstreams, and the restore bound of R-NPM-025. The restore-level client
  runs (`npm`, `dotnet`) are the end-to-end suite's.

## The revocation journal (package D)

| File | Responsibility |
|---|---|
| [journal/ship/ship.go](../../internal/journal/ship/ship.go), [outbox.go](../../internal/journal/ship/outbox.go) | `Shipper` (id order, row deleted after its object, stops at the first failure, `Discard` when the journal is off) and the `Outbox` over the queries of `backup.sql` |
| [journal/replay/reader.go](../../internal/journal/replay/reader.go) | `Reader`: day prefixes, forged-key checks, sorted by time and ID |
| [journal/replay/replayer.go](../../internal/journal/replay/replayer.go) | `Replayer`: one transaction per entry, panic and unknown event become `skipped`, summary event |
| [journal/replay/open.go](../../internal/journal/replay/open.go) | `LoadCredentials`, `OpenJournal` (shared with the restore) |
| [blob/s3/journalstore.go](../../internal/blob/s3/journalstore.go) | `JournalStore`: put, list (with versions on request), get |
| [jobs/journal.go](../../internal/jobs/journal.go), [app/wire_journal.go](../../internal/app/wire_journal.go) | the job kind `revocation-journal-ship` and the timer that enqueues it |
| `journal_replay.go` in `internal/auth/token`, `internal/auth/principal`, `internal/authz`, `internal/project` | the handlers and comparators of the identity and authorization events |
| [backup/postrestore](../../internal/backup/postrestore/postrestore.go), [cmd/trove/admin_postrestore.go](../../cmd/trove/admin_postrestore.go) | `trove admin post-restore`: replica rows, deletion pause, sessions, the fix-ups of orders jobs, system state and revisions, `restored_at`, replay, `system.restored` |

After the audit of the journal: the reader returns every copy and every version (`journal.AllVersions`, `Object.Warning`), the replayer
starts at `min(t, newest audit event) - margin` (`Options.Newest`), the shipper quarantines a row that cannot be encoded
(`ship.Quarantiner`, `OnQuarantine`), and `postrestore` keeps two `system_state` keys: `post_restore_in_progress` (the gate,
`postgres.PostRestoreMarkerKey`) and `post_restore_done` (the last restore point that completed). Marks of declared bindings are
`<role>|<kind>:<name>` entries; `authz.suppressionKey` is the one place that builds them, and the group handler in `principal`
writes the same form by hand (keep them equal).

Queries are in [journal.sql](../../internal/meta/postgres/queries/journal.sql). A handler takes IDs only, applies a removal as recorded
or runs `journal.ApplyUpdate` with its comparator, and returns a `Result`; `ci.token.revoked` is replayed by the token package because
`RecordCIRevoked` lives there. The gauge `trove_revocation_journal_pending` is the gauge package's (`Outbox.Pending`). Tests:
`go test ./internal/journal/... ./internal/authz ./internal/project ./internal/auth/token` and the integration files
`test/integration/backup/journal_*_test.go`. Post-restore refuses to run when no fix-up is registered for the orders it needs, so it
works only together with the restore package's fix-ups.

### Producers that a review found without an entry

| Change | Entry | Replay | Code |
|---|---|---|---|
| A Bastion registration disabled or deleted | `bastion.disabled`, `bastion.deleted`: `{principalId, name}` | disable the registration (`replayDisableAccount` with `KindBastion`) | [registrations.go](../../internal/bastionmain/registrations.go), [principal/journal_replay.go](../../internal/auth/principal/journal_replay.go) (Q-BAS-145) |
| A user or service account disabled with `revokeTokens`, or deleted | one `token.revoked` `{tokenId, reason}` per token, besides the principal's own entry | the token handler, as for every revocation | `principal.RevokeAllTokensTx` and `RecordTokensRevoked` in [token_events.go](../../internal/auth/principal/token_events.go) (Q-AUTH-198) |
| A repository disabled or deleted | `repository.disabled`, `repository.deleted` (both phases): `{repositoryId, key}` | disable the repository (`replayRepositoryDisabled`, the `disableRepository` step that `repository.moved` uses too) | [repository/service.go](../../internal/repository/service.go) (`recordDisabled`, `recordAndNotify`), [project/journal_replay.go](../../internal/project/journal_replay.go) (Q-AUTHZ-238, Q-AUTHZ-239) |
| A repository's patterns, `autoClaim`, members, pin rules or `vulnerabilities` member narrowed | `repository.narrowed`, beside `repository.updated`, only when the update narrows: `{repositoryId, key}` and the parts that narrowed (`include`, `excludeAdded`, `autoClaim`, `membersRemoved`, `pinsAdded`, `pinMembersRemoved`, `vulnerabilities`) | the parts in order (R-REPO-061 steps 1 to 7); a part that could widen disables the repository (`replayer.suspend`); the rescan is a job row written by `jobs.EnqueueTx` | [repository/narrowed.go](../../internal/repository/narrowed.go), [repository/journal_replay.go](../../internal/repository/journal_replay.go) (Q-REPO-245 to Q-REPO-249, Q-HA-180) |
| A repository's claims changed, the auto claim of a first publish included | `repository.claims_changed`, on every change: `{repositoryId, key, source, added}` with the claims created or widened to `global` (empty for a removal) | create or widen each claim; an overlapping manual claim disables its repository (`claimReplay`) | [repository/claims.go](../../internal/repository/claims.go) (`recordClaims`, `diffClaims`), [repository/routing.go](../../internal/repository/routing.go) (`AutoClaim`), [repository/journal_replay.go](../../internal/repository/journal_replay.go) (Q-REPO-250) |
| A user's pending identity link removed | `user.updated`, on every update: `{principalId, name, pendingLinksRemoved}` | delete the pending links of those providers, never a linked identity (`replayUserUpdated`) | [principal/manager.go](../../internal/auth/principal/manager.go), [principal/journal_replay.go](../../internal/auth/principal/journal_replay.go) (Q-AUTH-205) |
| A group renamed | `group.updated` gains `renamedFrom`, the name before | the group keeps its restored name, is detached from group synchronization (`sync_detached`) and loses its `oidc:` memberships (`replayRenameDetach`) | [principal/groups.go](../../internal/auth/principal/groups.go), [principal/journal_replay.go](../../internal/auth/principal/journal_replay.go) (Q-AUTH-206) |
| A token lifetime maximum lowered | `settings.auth.changed` gains the two maxima before and after | each maximum takes the smaller value (`null` and a longer value change nothing; a value under 60 s is malformed); a stored default above it is lowered to it | [token/settings.go](../../internal/auth/token/settings.go), [token/journal_replay.go](../../internal/auth/token/journal_replay.go) (Q-AUTH-207) |
| A group's auto-join flag cleared | `group.updated`, on every update: `{principalId, name, autoJoin: {before, after}}` | the update rule with `autoJoinOrder`: clear, never set | [groups.go](../../internal/auth/principal/groups.go), [principal/journal_replay.go](../../internal/auth/principal/journal_replay.go) (Q-AUTH-198) |

- **Cost.** A revoke-all writes its events through `audit.RecordAllTx`: one `InsertAuditEvents` and one `InsertRevocationOutbox` per 500
  events. `TestR_AUTH_016_ADisableCostsTheSameStatementsForOneTokenAndForThousands` counts 14 statements for a disable with one token
  and 18 for one with 1,200.
- **Every journaled event has a handler.** `TestR_HA_054_EveryJournaledEventHasAReplayHandler` in [cmd/trove](../../cmd/trove/journal_handlers_test.go)
  walks the audit registry in the binary that replays; an event that is flagged and has no handler fails it, unless `handlerPending`
  lists it with its gap (today none).
- **The Bastion feed.** Every new journaled event is classified in [feed.go](../../internal/bastionmain/feed.go): `repository.disabled`
  and `repository.deleted` drop every answer (`grants`), `repository.narrowed` and a `repository.claims_changed` that added members
  claims yield `content`, an added `global` claim `grants` (Q-BAS-146), the registration events, `group.updated` and `user.updated` yield none.
- **Tests.** Unit: `TestR_OPS_015_RecordAllTxBatchesWhereTheRecorderCan`, `TestR_OPS_015_RecordAllTxChecksEveryEventBeforeItWrites`,
  `TestR_AUTH_019_AutoJoinOrder`, `TestR_AUTH_019_TheUpdateRuleClearsAndNeverSetsTheFlag`, `TestR_AUTH_041_GroupUpdatedEntriesAreReadStrictly`,
  `TestR_BAS_004_ARegistrationEntryWithoutAnIDIsSkipped`, `TestR_AUTH_029_TheEventOfARevokedToken`,
  `TestR_AUTHZ_031_RepositoryEntriesAreReadStrictly`. Integration, through the services the API uses, the shipper and the replayer into
  a copy of the database made at *t* ([journal_producers_test.go](../../test/integration/backup/journal_producers_test.go)):
  `TestR_AUTH_016_TokensADisableRevokedStayRevokedAfterARestore` (enabling the restored principals again brings no token back),
  `TestR_BAS_004_ADisabledOrDeletedRegistrationStaysDisabledAfterARestore`, `TestR_AUTHZ_031_ADisabledOrDeletedRepositoryStaysDisabledAfterARestore`,
  `TestR_AUTH_019_AClearedAutoJoinStaysClearedAfterARestore`.


## Exposure and CI in the journal (package E)

| File | Responsibility |
|---|---|
| [authz/journal_replay_exposure.go](../../internal/authz/journal_replay_exposure.go) | the handler of `endpoint.exposure.updated` and `.deleted` (R-EP-020): the rule found by endpoint and name and acted on only with the entry's rule ID and repository ID (Q-EP-77), `exposureNarrower` (rules 1 to 4, package patterns folded with the format's name rule, no range or glob containment, Q-EP-79), the strict reading of a journaled state and the API's validation of an after-state (Q-EP-78), the writes through the rule's row and the change area `authz`; `JournalReplaySubject`, the caller of a replayed write that goes through a service |
| [ci/journal_replay.go](../../internal/ci/journal_replay.go) | the handlers of `ci.issuer.*` and `ci.mapping.*` updates, disables and deletions (R-CI-020): `issuerNarrower` and `mappingNarrower` over the records the producers write (`readStrict`), a deleted issuer or mapping disabled through the services' own transaction helpers (`writeUpdate`, `revokeTokens`, `setStatus`, `bump`, `applyBindings`), and `fallThrough` (Q-CI-11): every later enabled mapping of the tier suspended, each returned as a further result |
| [ci/restored.go](../../internal/ci/restored.go) | `predatesRestore`, the restore cut-off of R-CI-018, and the note of R-CI-029's restore column |
| [vuln/triage/journal_replay.go](../../internal/vuln/triage/journal_replay.go) | the handler of the download blocks (`vulnerability.finding.status_changed` and `.reopened` with `block: set`, R-VULN-013, R-VULN-026): the entry is `vuln.BlockEntry` ([block_entry.go](../../internal/vuln/block_entry.go)), which a triage and every block the evaluation sets write (`setsBlock`, `cause` in [evaluate/blocks.go](../../internal/vuln/evaluate/blocks.go), Q-VULN-130); the finding is found by ID when it has the entry's repository, ecosystem, package and version, then by the table's unique key, and is created when the restored repository can hold it; the block is set again in the shape both download gates read (linked to the stored version, or resolved with `version_removed` while blocked, which the miss and a commit count until the ecosystem has synced since the restore, Q-VULN-131, Q-VULN-132); a lift after the restore point is not journaled (Q-VULN-114); the statements of [journal_vuln.sql](../../internal/meta/postgres/queries/journal_vuln.sql) and the finding's history row |
| [journal/replay.go](../../internal/journal/replay.go) | `Result.Also` and `Result.Flat`: an entry's further results; `Summary.Add` counts each, the replayer prints each on a line of its own |

- **What a replayed write does.** The handlers write the object with the statements the API uses, under the lock it takes, and notify
  the change areas through `journal.Env` (`Env.Notify`; a restore without a change log passes no notifier and nothing is notified). They
  write no audit event (Q-AUTHZ-232); the CI services' helpers run with `audit.Nop`, so neither a disable nor its token revocation
  records `ci.*` or `ci.token.revoked`, and nothing re-enters the journal.
- **Validation before comparison.** The CI handlers merge a journaled after-state into the restored object and run the API's
  validation of a save on it (`validIssuer`, `validMapping`, with the `ci.token` ceiling left open); a record the API would refuse
  makes the update rule suspend (Q-CI-68). A lifetime bound of 0 or less reads as unbounded, as the writer stores it, and migration
  `ci_bounds` refuses a negative skew and a bound of zero in the schema (Q-CI-69).
- **The format catalog.** The exposure handler reads the name rule and the range syntax of a rule's format from `catalog.Default()`. The
  binary that replays must register the formats (`internal/formats/all`, which the `trove` binary links); without them every update of
  an exposure rule is suspended, never applied.
- **Tests.** Unit: `TestR_EP_020_TheComparator`, `TestR_EP_020_JournaledStatesAreReadStrictly`, `TestR_CI_020_TheIssuerComparator`,
  `TestR_CI_020_TheMappingComparator`, `TestR_CI_020_JournaledRecordsAreReadStrictly`, `TestR_CI_020_ADisabledMappingSuspendsTheLaterMappingsOfItsTier`,
  `TestR_CI_018_TheRestoreCutOff`, `TestR_CI_029_CIStateAcrossARestore`, and the printing of further results in
  `internal/journal` and `internal/journal/replay`. Integration ([journal_exposure_replay_test.go](../../test/integration/backup/journal_exposure_replay_test.go),
  [journal_ci_replay_test.go](../../test/integration/backup/journal_ci_replay_test.go)): a source database changed after *t* through the
  services the API uses, its outbox shipped by the journal's shipper, and the replayer applying the entries to a copy of the database made
  at *t* with `CREATE DATABASE … TEMPLATE` ([journal_ci_twin_test.go](../../test/integration/backup/journal_ci_twin_test.go)); a second
  replay, hand-written forged entries and a replay without a notifier. The restore-level runs (an `npm view` on every replica, a merge
  request pipeline exchanging against the restored installation) are the end-to-end suite's.

## The platform layer and the point-in-time drill

The platform layer of R-HA-023 (WAL archive and base backups through the Barman Cloud plugin) is part of the dev stack behind `TROVE_PITR=1`, and the
point-in-time restore of R-HA-025 is drilled by the set `backup-pitr`. Nothing of it is code of the product except the commands the drill calls
(`trove admin post-restore`, [internal/backup/postrestore](../../internal/backup/postrestore)) and the chart's post-restore Job; the dev stack side is
[deploy/dev/pitr.sh](../../deploy/dev/pitr.sh), [pitr.yaml](../../deploy/dev/pitr.yaml) and [pitr-restore.yaml](../../deploy/dev/pitr-restore.yaml). What the drill does, its stages
and what it leaves out: [e2e.md](e2e.md#the-point-in-time-restore-drill). `trove admin storage restore-versions` and `maintenance resume-deletions` do not exist
([H-767](../spec/ha-and-helm.md#h-767--the-commands-of-r-ha-025-steps-5-and-7-do-not-exist)): the drill rolls the bucket back with the S3 API and clears the deletion pause with SQL.

## The create pipeline

`trove backup create` is [internal/backup/create](../../internal/backup/create), written against the ports of `internal/backup` and
`target.Store`; [cmd/trove/backup_create.go](../../cmd/trove/backup_create.go) joins PostgreSQL, the blob store and the S3 target and
opens the connections when the run starts, with the run's context (a SIGTERM ends it).

| File | Responsibility |
|---|---|
| [create.go](../../internal/backup/create/create.go) | `Deps`, `New`, `Create`: facts, grants, the run row, `finish` (completed, warnings, failed, interrupted; audit events; hold and pins released by `Finish`) |
| [run.go](../../internal/backup/create/run.go) | the phase machine, the snapshot phase with the `all` hold in the `onLocked` callback, cleanup of open uploads and the snapshot |
| [preflight.go](../../internal/backup/create/preflight.go) | the probe and the encryption check (R-BAK-026), the marker, adoption, the listing that finds the previous backup |
| [diff.go](../../internal/backup/create/diff.go) | the merge of the referenced set with the previous list: new blob list, pins in batches, the reservoir of the spot check, hold `all` to `pinned` |
| [export.go](../../internal/backup/create/export.go) | table export with `database.parallel` workers into zstd and a multipart upload, sequences, release of the lock, close of the snapshot |
| [copy.go](../../internal/backup/create/copy.go) | the blob copy: pages through the pins, server-side or streaming with the digest check, damage versus lost hold, batched pin deletion |
| [finalize.go](../../internal/backup/create/finalize.go) | the fences, the spot check, the manifest, the pruning stage |
| [util.go](../../internal/backup/create/util.go) | error classification (R-BAK-013), the rate limiter, a small error group |

The database side: [backup_snapshot.go](../../internal/meta/postgres/backup_snapshot.go) (pgx: the backup role's pool, the shared migration
lock, `LOCK TABLE`, the snapshot, the export connections, `COPY`, the referenced-blob stream) and the adapters in
`internal/backup` (`pgsnapshot.go`, `pgfacts.go`, `blobsource.go`) that give it the shapes and exit codes of the ports
([Q-BAK-23](../spec/backup-and-restore.md#q-bak-23--the-database-half-of-the-snapshot-is-pgx-code-in-internalmetapostgres-its-session-settings-are-startup-parameters-and-the-backup-role-holds-one-pooled-connection)).
Its queries are [backup_snapshot.sql](../../internal/meta/postgres/queries/backup_snapshot.sql). The collector's side of the hold is in
[blob/gc.go](../../internal/blob/gc.go) (`claim`, `globalHold`).

A retry pod after a `kill -9` meets the dead run's young heartbeat. `backup_run.job_name` (from `TROVE_JOB_NAME`, which the chart sets
from the pod's `job-name` label on the backup Job) tells `AcquireSlot` that the active run belongs to the same Job, whose pod is gone,
so it waits for the lease instead of answering exit 3; a run of another Job or without a Job name still gets exit 3 at once
([Q-BAK-44](../spec/backup-and-restore.md#q-bak-44--a-retry-pod-of-the-same-job-takes-over-a-dead-run-at-once-any-other-pod-meets-the-heartbeat-rule)).

The diff does three more things: a referenced blob whose row is not `ok` is listed and recorded as damaged with its status but
never pinned, the previous blob list is hashed and counted while it streams and compared with its manifest before the hold turns
`pinned`, and a server-side copy carries `ObjectRef.Progress` so that a long copy inside the store ticks the run
([Q-BAK-54](../spec/backup-and-restore.md#q-bak-54--a-blob-whose-row-is-not-ok-is-listed-as-damaged-and-not-copied-and-the-previous-blob-list-is-verified-while-it-streams),
[Q-BAK-55](../spec/backup-and-restore.md#q-bak-55--shutdown-and-exit-codes-of-the-create-run-aborts-have-a-deadline-a-stall-decides-its-exit-code-a-throttled-wait-is-progress)).

The order that matters, with `Deps.Fault` points for tests between the steps: shared migration lock, hold `all` committed
(`hold-all`), table locks and snapshot (`snapshot`), pins inserted and committed (`pins-committed`), hold `pinned`
(`hold-pinned`), copy, last pin gone and hold `none` (`copied`), fence (`finalizing`), manifest. A pause at a point is how the
integration tests hold a run between two steps of R-BAK-007.

Tests: `go test -race ./internal/backup/...` (fakes: an in-memory run store with the fencing, a fake snapshotter, `targettest.Mem`);
the integration files `test/integration/backup/create_*_test.go` run the pipeline and the real binary against PostgreSQL and the S3
store: the hold criteria and the property test over random interleavings (`create_hold_test.go`), the snapshot criteria
(`create_run_test.go`), the copy criteria (`create_copy_test.go`), exit codes, `kill -9` and a second Job (`create_process_test.go`),
SIGTERM and the backup role (`create_signal_test.go`), and a restore of a backup the pipeline wrote (`create_roundtrip_test.go`).
The stack's SeaweedFS rejects SSE-S3, so they run with `backup.target.sse.mode: none`. Nothing in the stack is a standby: the refusal
of a standby is covered by the error mapping unit tests only. The kind drain and crash criteria are the end-to-end suite's.
