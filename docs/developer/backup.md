# Backup, restore and the revocation journal

Status: the contracts of the lane are built (phase 7, first step): the schema, the on-target format, the ports, the run record with
its lease, the configuration, the command tree, the API operations, the metric families, the audit events and the registries.
The operations themselves are not: `trove backup create|restore`, `trove admin post-restore` and
`trove admin npm reinstate` are wired and answer "not built yet" (exit 1) until their work packages replace the constructors. The
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
  admin_npm.go            admin npm reinstate         -> npmReinstate
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
internal/api/v1/backups_api.go  the three read-only operations (stubs that answer "no runs yet")
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

