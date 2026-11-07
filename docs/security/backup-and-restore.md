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

## How the chart keeps the split

The chart gives each Job its own pod template and the Secrets of its scope only ([helm-chart.md](../developer/helm-chart.md#the-backup-and-restore-jobs),
[_secrets.tpl](../../deploy/helm/trove/templates/_secrets.tpl)):

| Mechanism | Effect | Test |
|---|---|---|
| `secrets.backupTarget` and `secrets.backupTargetCA` mount in the scopes `backup` and `restore` | no `api`, `packages` or `worker` pod and not the migration Job has a file or a variable with the target's credentials; the shared `trove.yaml` names the paths and nothing exists under them there | `TestR_HA_030_BackupSecrets` (every value set) |
| Valkey Secrets mount in neither Job; the key ring only in the restore Job | a compromised backup pod holds no Valkey credential and no key ring (R-BAK-012) | `TestR_HA_030_BackupSecrets` |
| `secrets.databaseBackup` for the backup pod, the migration login for the restore Job | the backup pod can use the `backup` role (`database.backup_role`) and the owner credential reaches only the Job that needs it | `TestR_HA_030_BackupSecrets` |
| a ServiceAccount per Job, no token mounted | a workload identity maps each Job to a cloud role of its own; neither Job can call the Kubernetes API | `TestR_HA_060_BackupCronJob`, `TestR_HA_060_RestoreJob` |
| a NetworkPolicy per Job with `ingress: []` | no pod reaches a Job's pod | `TestR_HA_042_JobsAdmitNothing` |
| the Jobs take no global `extraEnv`, `extraVolumes` or `extraEnvFrom` | a variable or volume meant for the replicas does not reach the pods that hold the target's keys (Q-HA-149) | `TestR_HA_030_BackupSecrets` |

Verified in this repo: the render tests above. Not verified, because it needs a cluster: that a workload identity annotation maps the Job to
a role, and that a CNI enforces the empty ingress list (the profile's CNI is the only one the ingress policies are verified on, H-41). The
chart cannot see what an operator adds through `backup.extraEnv`, `backup.nodeSelector`, `extraObjects` or a mutating webhook of the cluster;
a Secret placed there reaches the Job as the operator wrote it.

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

## Deleting from the target

Retention, `delete` and the pool sweep are the only code that removes objects from the target, and only under the run's lease: each
delete request is preceded by the fenced lease check ([retention/stage.go](../../internal/backup/retention/stage.go), `deleteBackup`,
and `Sweeper.Fence`), and the target marker is compared with the installation ID and incarnation first (`CheckMarker`), so a second
installation configured with the same target exits 4 before it deletes anything. The sweep deletes a pool object only if no remaining
complete backup lists it, and it builds that set only from blob lists whose SHA-256 and entry count match their manifest
(`Sweeper.Sweep`, `TestR_BAK_017_CorruptBlobListSkipsTheSweepWithAWarning`); a pool key outside the layout or out of order ends the
sweep without a further delete. The manifest's own SHA-256 entries are in the same trust domain as the lists (H-46).

`trove backup create` warns when a retained backup lists a key ID the new backup no longer uses (`EscrowWarnings`): it compares
IDs only and says nothing about whether the key is in escrow. The listing prints `encryption` per backup, `none` included.

### What the format handlers of the journal do with an entry

The npm and NuGet handlers ([npm](../../internal/formatdata/npm/journal_replay.go), [NuGet](../../internal/formatdata/nuget/journal_replay.go))
treat an entry as a claim about the restored database and act only on a local repository of their format, with a complete entry:
a missing member, an oversized value or a digest that is not the expected shape is skipped before any statement runs.

- **npm withholds, it does not delete** ([R-NPM-047](../spec/formats/npm.md#r-npm-047--npm-data-in-backups-and-restores)). A forged or mistaken
  `npm.unpublish` entry can only write a `journal` tombstone, and only for a version the repository stores with exactly the integrity the
  entry names; the version row, its items and its bytes stay. The cost is availability until an administrator runs `trove admin npm
  reinstate`, which removes only a `journal` tombstone and only while the version is still stored with the tombstone's integrity, so it
  brings back the same bytes and never admits new ones (Q-NPM-8). A tombstone of a client's unpublish is not reinstated. The version is
  refused on every road that serves it: the version and package documents, the tarball route, `/files`, and the virtual repositories
  that list the member ([servecheck.go](../../internal/formats/npm/servecheck.go), `TestR_NPM_047_ReplayWithholdsALiveVersionAndNoRouteServesIt`).
  The actor the entry names is stored only when it is a UUID; the tombstone records the entry's ID, which the reinstate event repeats.
- **NuGet reserves and keeps removed numbers removed** ([R-NUGET-033](../spec/formats/nuget.md#r-nuget-033--nuget-data-in-backups-and-restores)).
  A forged `package.published` entry can burn a version number that no version holds (a `reserved` tombstone), and a forged `package.deleted`
  can turn a reservation into a removal or write a `removed` tombstone; both cost the number, never data, and the replay never deletes a
  stored version (it reports it `revived`). A reserved number admits only the identical `.nupkg`. The system principal of the restore (the
  nil UUID) is the actor of what the replay writes.
- **Remote-cache purgers** ([fixup](../../internal/backup/fixup/npm.go)) delete the cached items of the remote repositories of their format
  through the storage delete path and the cache entries and index rows of those repositories; they read the remote repositories of the
  format and nothing else, so a local repository, its tombstones and its reservations survive a purge.

### Replay rules by mechanism

- **Unreadable or forged objects.** [reader.go](../../internal/journal/replay/reader.go) lists the days from the restore point, reads every
  object and refuses one that does not decode, whose key is not its ID, whose key names another day than its time, a duplicate ID, or
  a newer version: it is listed in the output as unreadable and never replayed. A listing that fails ends the replay.
- **Unknown events and panics.** An event without a handler is reported `skipped`; a handler that panics is rolled back and reported
  `skipped` ([replayer.go](../../internal/journal/replay/replayer.go)).
- **Comparators.** The policy, settings, role and anonymous-switch comparators answer true only when they can prove the after-state
  grants nothing beyond the current one, comparing IDs and patterns exactly as stored
  ([authz](../../internal/authz/journal_replay.go), [project](../../internal/project/journal_replay.go),
  [token](../../internal/auth/token/journal_replay.go)). Tested in `TestR_AUTHZ_031_PolicyComparator`,
  `TestR_AUTHZ_031_SettingsComparator`, `TestR_AUTHZ_031_RoleComparator` and, against PostgreSQL,
  `TestR_HA_054_ForgedEntriesCannotEscalate`.
- **Removals.** A deleted user, service account or CI owner is disabled, a moved repository is disabled and not moved, a released name
  stays claimed, a membership is never added or raised, the break-glass account is never touched.
- **Credential.** The shipper writes with the runtime credential and deletes nothing. The reader of a restore uses the credentials file
  of [open.go](../../internal/journal/replay/open.go) and lists object versions with it.

Not verified: that a delete marker is read through on a versioned bucket. The store implements it
([journalstore.go](../../internal/blob/s3/journalstore.go)); the integration stack's bucket is not versioned, so only the listing
path runs there.

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
- [H-557](../spec/backup-and-restore.md#h-557--the-post-restore-does-not-verify-the-blobs-and-a-replay-writes-no-audit-event-per-changed-object): post-restore does not verify the blobs, and a replay writes no audit event per changed object.
- [H-555](../spec/backup-and-restore.md#h-555--the-run-history-is-only-as-trustworthy-as-the-backup-role): the run history is only as trustworthy as the backup role.
