# Backup, restore and the revocation journal

Status: the contracts of the lane are built (phase 7, first step): the schema, the on-target format, the ports, the run record with
its lease, the configuration, the command tree, the API operations, the metric families, the audit events and the registries.
The operations themselves are not: `trove backup create|list|verify|delete|prune|restore`, `trove admin post-restore` and
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

## Conventions

- Test names carry requirement IDs (`R-BAK-010`), never decision IDs.
- A package of the lane that needs a new shared shape changes it in `format`, `target`, `fixup` or `journal` and says so in its
  change; it does not copy the shape.
- The AWS SDK is imported in `internal/blob/s3` only (which implements `target.Store` and imports the leaf `internal/backup/target`); pgx in `internal/meta/postgres` and `internal/cluster` only.
- Migrations of the lane start at 46 and are expand steps; a table gets its export class in the same change.
