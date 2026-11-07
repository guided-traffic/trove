# Backup, restore and the revocation journal

Status: the contracts are built ([backup.md](../developer/backup.md)); the operations are not. This page describes the mechanisms
as the contracts fix them and marks what a later work package still has to build. The specification is
[backup-and-restore.md](../spec/backup-and-restore.md) and R-HA-054 in [ha-and-helm.md](../spec/ha-and-helm.md).

## Who holds what

| Principal | Holds | Never holds |
|---|---|---|
| `backup` pod | the target writer credential, a blob reader, the `backup` database role (`SELECT` everywhere, writes on `backup_run` and `backup_blob_pin`) | Valkey credentials, the key ring, the right to write blobs |
| restore Job | the target reader, a blob writer, the journal reader, the owner database credential, the key ring | Valkey credentials |
| `web` and `worker` replicas | `backup.expected_interval` and `backup.manual_run`, read access to `backup_run` through the API | any target credential |

The configuration keeps the split: `backup.target.credentials` is a section the chart renders into backup and restore pods only
([internal/config/backup.go](../../internal/config/backup.go)); the API reads rows, never the target
([backups_api.go](../../internal/api/v1/backups_api.go) answers from `backup_run` once built).

The target client ([target.go](../../internal/blob/s3/target.go)) is built from `backup.target` alone and signs every request with
`backup.target.credentials` or the default chain of the backup pod's environment; it has no path to `storage.s3.credentials`, so the
runtime blob-store credential never signs a request to the target (R-BAK-015; `TestR_BAK_015_TargetSignsWithItsOwnCredential`,
`TestR_BAK_015_TargetOwnCredentials`). `backup.target.ca_file` is the target's whole trust store, and a plain-http endpoint is refused
without `allow_insecure_endpoint` (R-BAK-026). Every write, multipart start and copy repeats `backup.target.sse`; the preflight
compares what the store reports with it (`Target.CheckSSE`). A store without a conditional create gets a `HeadObject` check before the
marker is written, which is not atomic; the one-run rule in PostgreSQL, not the store, keeps two runs apart (Q-BAK-27).

## The run record and the hold

`backup_run` is the one place that says which backup operation is active. A partial unique index admits one `create` or `delete`
run; the lease and the fenced heartbeat stop a stalled process from writing after another run took over
([runstore.go](../../internal/backup/runstore.go), [handle.go](../../internal/backup/handle.go)). The table's CHECK constraints refuse a
hold on any run but a `create` and a finished run that still holds ([00046_backup.sql](../../internal/meta/postgres/migrations/00046_backup.sql)).

The hold keeps garbage collection from deleting a blob a running `create` still has to copy. It is a liveness mechanism: a completed
backup is complete whatever the hold did, because the manifest is written only after every listed digest is confirmed in the pool
(R-BAK-007). A hold counts only while its lease is valid, so a dead run's hold ends by itself. Verified in this repo: the integration
test [backup_runstore_test.go](../../test/integration/postgres/backup_runstore_test.go) shows that an expired lease ends the hold and
the pins and that a pin holds only its digest. Not verified, because the collector does not call the check yet: that a collection run
keeps a held blob live.

## The revocation journal

A restore undoes every access removal made after its point in time. The journal records them a second time outside PostgreSQL, in the
transaction of the change (`revocation_outbox`, [audit.sql](../../internal/meta/postgres/queries/audit.sql)), and a replay re-applies the
removals. A journal object is untrusted input: anyone who holds the runtime S3 credential can write one ([object.go](../../internal/journal/object.go)
decodes and validates it; the handlers check it against the restored database). Replay never grants access: a removal only removes, an
update is applied only when the comparator proves that it grants nothing beyond the current state, and the generic rule fails closed
([replay.go](../../internal/journal/replay.go), `ApplyUpdate`, tested in `TestApplyUpdate`).

## What a restore trusts

A restore trusts the manifest and the files of the backup it is told to restore (H-46). It checks formats, sizes, schema steps and the key
ring before it writes ([format](../../internal/backup/format) refuses a manifest of a higher format, `ExitPrecondition`), but a
consistent forgery by a writer to the target passes. The target marker ties a target to one installation and incarnation, so a second
installation cannot write into it (`Marker.Check`, exit 4).

## What this does not cover

The gaps of the backup itself are [H-46](../spec/backup-and-restore.md#h-46--backup-integrity-is-not-authenticated),
[H-47](../spec/backup-and-restore.md#h-47--the-backup-role-can-read-everything-and-delete-backups) and
[H-48](../spec/backup-and-restore.md#h-48--database-files-are-readable-to-every-reader-of-the-target-until-p1). The mechanisms of this page add:

- [H-553](../spec/backup-and-restore.md#h-553--a-journal-object-can-be-overwritten-with-the-runtime-credential): a journal object can be overwritten with the runtime credential.
- [H-554](../spec/backup-and-restore.md#h-554--a-compromised-backup-pod-can-hold-garbage-collection): a compromised backup pod can hold garbage collection.
- [H-555](../spec/backup-and-restore.md#h-555--the-run-history-is-only-as-trustworthy-as-the-backup-role): the run history is only as trustworthy as the backup role.
