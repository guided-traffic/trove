# Backup, restore and the revocation journal

Status: built ([backup.md](../developer/backup.md)); of the P1 parts, client-side encryption of the table files (R-BAK-027) and
the restore onto an existing blob store (R-BAK-025) are not. The chart runs the backup only with `backup.enabled: true` (off by
default). The specification is
[backup-and-restore.md](../spec/backup-and-restore.md) and R-HA-054 in [ha-and-helm.md](../spec/ha-and-helm.md).

## Who holds what

| Principal | Holds | Never holds |
|---|---|---|
| `backup` pod | the target writer credential, a read-only blob login of its own (`secrets.backupSource`, or the backup ServiceAccount's workload identity), the `backup` database role (`SELECT` everywhere, writes on `backup_run` and `backup_blob_pin`) | Valkey credentials, the key ring, the runtime credential of the blob bucket, any right to write or delete there |
| restore Job | the target reader, a blob writer (the runtime credential), the journal reader (`restore.journalCredentials`: a credential that lists object versions), the owner database credential, the key ring | Valkey credentials |
| post-restore Job | the owner database credential, the blob store's keys, the journal reader (`postRestore.journalCredentials`) | Valkey credentials, the key ring, any target credential |
| `api`, `packages` and `worker` replicas (and the bridge's `web`) | `backup.expected_interval` and `backup.manual_run`, read access to `backup_run` through the API | any target credential |

The configuration keeps the split: `backup.target.credentials` is a section the chart renders into backup and restore pods only
([internal/config/backup.go](../../internal/config/backup.go)); the API reads rows, never the target
([backups_api.go](../../internal/api/v1/backups_api.go) answers from `backup_run`, `system_state` and the configuration).

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
| `secrets.backupSource` replaces `secrets.s3` in the backup scope; the config key `backup.source` makes `trove backup create` read the bucket with it | a compromised backup pod cannot write blobs or the journal (R-BAK-015); with a workload identity there is nothing to mount | `TestR_HA_030_BackupSecrets` |
| `restore.journalCredentials` and `postRestore.journalCredentials` render the credentials file that turns the listing of object versions on | a deletion or delete marker set with the runtime credential cannot hide a journal entry from a restore | `TestR_HA_060_RestoreJob`, `TestR_HA_025_PostRestoreJob` |
| the replica rule of `restore.enabled` also holds for `postRestore.enabled` | no replica serves the restored database before the clean-up has replayed the journal | `TestR_HA_025_PostRestoreJob` |
| Valkey Secrets mount in neither Job; the key ring only in the restore Job | a compromised backup pod holds no Valkey credential and no key ring (R-BAK-012) | `TestR_HA_030_BackupSecrets` |
| `secrets.databaseBackup` for the backup pod, the migration login for the restore Job | the backup pod can use the `backup` role (`database.backup_role`) and the owner credential reaches only the Job that needs it | `TestR_HA_030_BackupSecrets` |
| a ServiceAccount per Job, no token mounted | a workload identity maps each Job to a cloud role of its own; neither Job can call the Kubernetes API | `TestR_HA_060_BackupCronJob`, `TestR_HA_060_RestoreJob` |
| a NetworkPolicy per Job with `ingress: []` | no pod reaches a Job's pod | `TestR_HA_042_JobsAdmitNothing` |
| the Jobs take no global `extraEnv`, `extraVolumes` or `extraEnvFrom` | a variable or volume meant for the replicas does not reach the pods that hold the target's keys (Q-HA-149) | `TestR_HA_030_BackupSecrets` |

Verified in this repo: the render tests above. Not verified, because it needs a cluster: that a workload identity annotation maps the Job to
a role, and that a CNI enforces the empty ingress list (the profile's CNI is the only one the ingress policies are verified on, H-41). The
chart cannot see what an operator adds through `backup.extraEnv`, `backup.extraVolumes`, `restore.extraVolumes`, `postRestore.extraVolumes`, `backup.nodeSelector`, `extraObjects` or a mutating webhook of the cluster;
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
the pins and that a pin holds only its digest. Verified in this repo with the real collector, PostgreSQL and S3:
[create_hold_test.go](../../test/integration/backup/create_hold_test.go) shows that with a grace period of 0 a pinned blob stays live while
the run copies it and goes at the next collection, that a collection run ends at once under a global hold, that a run stalled past its
lease loses the hold and then fails with `hold_lost` without a manifest, and, over random interleavings of uploads, deletes,
collections and runs, that no completed backup lacks a listed digest in its pool (the test fails when the collector's check is
removed). Not verified: the proof for a collector that claims batches in one transaction, which this collector does not do.

## What a create run does to the source

The run reads the whole database in one `REPEATABLE READ READ ONLY` snapshot that it takes after the `all` hold has committed
([backup_snapshot.go](../../internal/meta/postgres/backup_snapshot.go)): the shared migration lock first, so no migration step runs while
the database is exported; then `ACCESS SHARE` on every table of the catalog before the first `SELECT`, which keeps `TRUNCATE` and
a rewriting `ALTER TABLE` from outside the runner away from the snapshot. The snapshot and the lock end with the last table, not
with the blob copy. Every connection of the run is named `trove:<pod>:backup` and goes to the session host; a standby is refused.
The statements assemble identifiers from the catalog with `pgx.Identifier`, never a value. The role writes only `backup_run` and
`backup_blob_pin`, and writes its audit events through `backup_record_event()`, which accepts the four events of the lane and
no other ([Q-BAK-50](../spec/backup-and-restore.md#q-bak-50--the-backup-login-writes-its-audit-events-through-a-function-not-through-insert-on-the-audit-trail)); a role that is no
longer `database.backup_role` loses what the migration granted it ([R-BAK-015](../spec/backup-and-restore.md#r-bak-015--credentials-and-privileges-of-the-backup-role)).

The target marker keeps a run from writing into another installation's target: it is read at the start and again before the
manifest, a mismatch is exit `4`, and a prefix with backups and no marker is refused as well. The marker is written only on an
empty prefix; adopting a restored installation's target is a compare in the process followed by a write
([H-556](../spec/backup-and-restore.md#h-556--adopting-a-target-and-writing-its-marker-are-not-atomic-on-a-store-without-conditional-writes)).
A blob whose row the installation calls damaged is listed as damaged and never copied, so the pool never gains a copy the
installation itself distrusts; the previous blob list is trusted only after its SHA-256 and entry count matched its manifest
([Q-BAK-54](../spec/backup-and-restore.md#q-bak-54--a-blob-whose-row-is-not-ok-is-listed-as-damaged-and-not-copied-and-the-previous-blob-list-is-verified-while-it-streams)).
A streamed blob is hashed before it becomes a pool object; a blob that does not hash to its digest is recorded as damaged and is
never copied. A blob copied inside the store is confirmed by size and, where the store reports one, its SHA-256.

Verified in this repo: [create_run_test.go](../../test/integration/backup/create_run_test.go) (connection count, lock timeout, the
rewrite refused during the export, the migration lock, partitioned `audit_event`),
[create_signal_test.go](../../test/integration/backup/create_signal_test.go) (the grants of the role, SIGTERM) and
[create_process_test.go](../../test/integration/backup/create_process_test.go) (the command's exit codes, `kill -9`). Not verified:
a standby (the stack has none; the mapping is unit tested) and the Kubernetes behaviour of the Job.

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
  stored version (it reports it `revived`). A reserved number admits only the identical `.nupkg`. The end state of a number does not depend on
  the order of its entries, whose times the writer chooses: an addition that meets a reservation for other bytes turns it into a removal and
  prints a warning naming both SHA-512s, so a forged entry with an earlier time cannot reserve a number for its bytes while the journal holds a
  genuine entry of it, where the replay sees the genuine object: it reads every version of a key (a versioned bucket with `journalCredentials`
  other than `runtime`) or the bucket refuses both overwrites and deletions of journal objects (for example object lock), whereas with the chart default the runtime credential overwrites or deletes the
  genuine object and the one forged entry left reserves the number for the forger ([H-553](../spec/backup-and-restore.md#h-553--a-journal-object-can-be-overwritten-with-the-runtime-credential)) ([Q-NUGET-112](../spec/formats/nuget.md#q-nuget-112--two-replayed-additions-of-one-version-that-name-different-bytes-leave-its-number-refusing-every-package)).
  A refused number does not come back
  ([H-585](nuget-publish.md#h-585--a-forged-nuget-journal-entry-takes-a-version-number-out-of-use-for-good)). The
  system principal of the restore (the nil UUID) is the actor of what the replay writes.
- **Remote-cache purgers** ([fixup](../../internal/backup/fixup/npm.go)) delete the cached items of the remote repositories of their format
  through the storage delete path and the cache entries and index rows of those repositories; they read the remote repositories of the
  format and nothing else, so a local repository, its tombstones and its reservations survive a purge.

### Replay rules by mechanism

- **Unreadable or forged objects.** [reader.go](../../internal/journal/replay/reader.go) lists the days from the restore point, reads every
  object and refuses one that does not decode, whose key is not its ID, whose key names another day than its time, or a newer version: it
  is listed in the output as unreadable and never replayed. An ID under two keys, and a key whose versions differ, are not
  dropped: every copy is replayed and flagged, because the handlers only remove or narrow, whereas dropping a copy lets the
  writer of a planted one choose which is believed (`TestR_HA_054_ReaderReplaysEveryCopyOfADuplicatedID`,
  `TestR_HA_054_APlantedCopyUnderAnotherDayDoesNotHideTheGenuineEntry`). A listing that fails ends the replay. The replay starts at
  the earlier of the operator's restore point and the newest audit event of the restored database, so a wrong `--restored-to`
  cannot skip revocations (`TestR_HA_054_TheReplayStartsAtTheNewestAuditEventWhenTheRestorePointLiesLater`).
- **Unknown events and panics.** An event without a handler is reported `skipped`; a handler that panics is rolled back and reported
  `skipped` ([replayer.go](../../internal/journal/replay/replayer.go)).
- **Comparators.** The policy, settings, role and anonymous-switch comparators answer true only when they can prove the after-state
  grants nothing beyond the current one, comparing IDs and patterns exactly as stored
  ([authz](../../internal/authz/journal_replay.go), [project](../../internal/project/journal_replay.go),
  [token](../../internal/auth/token/journal_replay.go)). Tested in `TestR_AUTHZ_031_PolicyComparator`,
  `TestR_AUTHZ_031_SettingsComparator`, `TestR_AUTHZ_031_RoleComparator` and, against PostgreSQL,
  `TestR_HA_054_ForgedEntriesCannotEscalate`.
- **Removals.** A deleted user, service account or CI owner is disabled, a moved repository is disabled and not moved, a released name
  stays claimed, a membership is never added or raised, the break-glass account is never touched. A disabled or deleted Bastion
  registration is disabled (Q-BAS-145), and a disabled or deleted repository is disabled, never deleted (Q-AUTHZ-238, Q-AUTHZ-239). An
  entry acts only on an object of its own kind: a `bastion.*` entry naming a user or a service account is `subject unknown`.
- **Tokens a disable ended.** A disable with `revokeTokens` and a deletion of a user, service account or registration journal one
  `token.revoked` per token besides the principal's own entry ([token_events.go](../../internal/auth/principal/token_events.go),
  Q-AUTH-198). Without them the replay disabled the restored principal, and an administrator who enabled it again brought every token
  back, a leaked one included; `TestR_AUTH_016_TokensADisableRevokedStayRevokedAfterARestore` enables the restored principals and finds
  every token refused. The entries are written in batches, so a principal with thousands of tokens cannot make the disable slow
  enough to hold its lock for long (14 statements for one token, 18 for 1,200).
- **Auto-join.** `group.updated` journals the flag before and after on every update; the replay clears a cleared flag and never sets
  one, so a forged entry can only stop new users from joining a group (Q-AUTH-198).
- **Narrowings of identity and repositories** (F-72; [principal](../../internal/auth/principal/journal_replay.go),
  [token](../../internal/auth/token/journal_replay.go) and [repository](../../internal/repository/journal_replay.go) replay handlers).
  Every replay of this group only removes or narrows, reads its entry strictly (a missing key, a wrong type or an over-long list is
  `skipped`) and, where re-applying a part could widen, fails closed. The journal objects live under the blob prefix, so whoever holds the
  runtime credential of the blob store can write them (R-HA-054); what such an entry can do is bounded by what the replay does,
  which is to deny or narrow, never to widen. [What a forged entry can deny](#what-a-forged-entry-can-deny-and-how-an-administrator-recovers)
  lists it per handler. An entry that holds a value the database refuses (a NUL byte, a text beyond a limit) is rolled back and
  reported `skipped` with its subject quoted, and the replay goes on.
  - *Identity.* `user.updated` names the providers whose pending link the update removed; the replay deletes the pending link of each
    from the user of the journaled ID and never a linked identity, so a forged entry can remove a pending link (which only withholds a
    login) and nothing else (Q-AUTH-205). `group.updated` with `renamedFrom` detaches the group of the journaled ID from group
    synchronization and deletes its `oidc:*` memberships; it never writes the name, so no entry can hand a group's grants to the members
    of a provider group of that name (Q-AUTH-206). That detach can lock out administrators who hold `admin` only through a provider
    group: the replay prints a line for each detached group that holds a role binding, naming the roles, and its admin check uses the definition of the last-admin
    guard (`IdentityAdminHolders`: an active user or service account holding `admin` directly or through a group) and prints `no enabled
    administrator is left` otherwise. Remaining limit: a service account counts as a holder (it can act, nobody signs in as it), and a
    holder without a usable identity still counts, so the check can stay silent although no person can sign in.
    Recovery: the break-glass sign-in, then `PATCH /api/v1/groups/<name>` with `{"syncDetached": false}`. Every string of these
    entries is checked before a statement runs (a provider ID against the syntax of the configuration, a group name), so a forged
    object cannot make a statement fail and stop the replay. `settings.auth.changed` carries both runtime token maxima; each is set to
    the smaller of the restored and the journaled value, `null` and a longer value change nothing, and the parts are read
    independently: a maximum under 60 seconds or over 100 years narrows nothing and does not drop the anonymous switch-off (Q-AUTH-207).
  - *Repositories* (R-REPO-061). `repository.narrowed` carries only the parts of an update that narrow (include and exclude patterns,
    `autoClaim`, virtual members, pin rules, a stronger `vulnerabilities` member) and `repository.claims_changed` the claims created or
    widened to `global`, the auto claim of a first publish included. The replay never adds a member, an include pattern or a member of a
    pin rule. A part that could widen disables the repository (`disabled: true`, outcome `suspended`, the entry printed): an include
    list that shares no pattern with the restored one (Q-REPO-247), a removal of a local member with a `members` claim while any
    other member stays (Q-REPO-248), a pin rule that a claim in effect or another rule overlaps (Q-REPO-249), a manual claim that overlaps one
    in effect together with it (Q-REPO-250). The follow-up scan of a stronger `vulnerabilities` member is a job row the handler writes
    in its own transaction (`jobs.EnqueueTx`, Q-HA-180).
  - *Tests.* Through the replayer against a restored copy, with hand-written entries and a second pass:
    `TestR_AUTH_016_ARemovedPendingLinkStaysRemovedAfterARestore`, `TestR_AUTH_018_AGroupRenamedAfterTheRestorePointIsDetached`,
    `TestR_AUTH_026_ALoweredMaximumStaysLoweredAfterARestore`, and the `TestR_REPO_061_…` tests of
    [journal_repository_narrowing_test.go](../../test/integration/backup/journal_repository_narrowing_test.go).
- **Coverage.** `TestR_HA_054_EveryJournaledEventHasAReplayHandler` fails for a journaled event without a handler in the `trove` binary.
- **Exposure rules** ([journal_replay_exposure.go](../../internal/authz/journal_replay_exposure.go), R-EP-020). A deletion deletes the rule;
  deleting a rule only removes exposure, because rules add up and exclude wins inside a rule only. An update is applied only when the
  comparator proves the after-state exposes nothing the restored rule does not: the same endpoint and repository, `enabled` and
  `prereleases` never switched on, excludes kept, includes only shrunk and never emptied, a range only added or kept as text (Q-EP-79).
  The entry acts only on the rule whose endpoint, name, rule ID and repository ID it names (Q-EP-77), and a state that does not read
  exactly as the rule service writes it, or that the API would refuse for the rule's repository, proves nothing and disables the rule
  (Q-EP-78). A forged entry can therefore disable or delete a rule, which withholds content, and never widen one. Tested in
  `TestR_EP_020_TheComparator` and, through the shipper and the replayer against PostgreSQL, `TestR_EP_020_ExposureEntriesReplayIntoTheRestoredDatabase`.
- **CI issuers and mappings** ([journal_replay.go](../../internal/ci/journal_replay.go), R-CI-020). A disable or deletion disables, never
  deletes, and revokes the object's unexpired CI tokens as the API does. Disabling a mapping that was enabled also disables every later
  enabled mapping of its tier, so no job falls through to a mapping that grants more (Q-CI-11): without that step a forged deletion of a
  read-only mapping placed above a publish mapping would grant merge request pipelines a deploy. An update is applied only when the
  comparator proves a pure narrowing over a record read in full; a change of a condition, a priority, `enabled` or the subject kinds,
  which decide a tier's first match, is never one. Tested in `TestR_CI_020_TheIssuerComparator`, `TestR_CI_020_TheMappingComparator` and
  `TestR_CI_020_IssuerAndMappingEntriesReplayIntoTheRestoredDatabase`, which also shows that a merge request pipeline gets
  `no_matching_mapping` after a replayed deletion of the shadowing mapping.
- **Replicas during a platform restore.** `trove admin post-restore` commits `system_state.post_restore_in_progress` in its first
  transaction and deletes it in its last, after the replay and `system.restored`; the schema check of
  [gate.go](../../internal/meta/postgres/gate.go) fails with `ErrRestoreInProgress` while it exists, so no pod, running or
  started later, serves the restored database's access before the replay (`TestR_HA_025_ReplicasAreHeldUntilThePostRestoreHasReplayed`).
  The replay writes change-log rows through the command's notifier, so pods learn of what it changed. A repeat for a restore
  point that completed changes nothing; a run after a failed one resumes at the replay. The chart renders the Job with the
  owner credential: it should be switched off after the restore, and it refuses replicas while it is on (the chart's part).
  A failed post-restore leaves the installation not ready until it is run again, on purpose.
- **A row the shipper cannot encode** is set aside (`system_state.journal_quarantined`, `trove_revocation_journal_quarantined_total`)
  and the rows behind it ship; post-restore prints the count, because the change that row recorded is not in the journal.
- **Declared bindings.** `trove migrate` applies two kinds of configuration-declared access: `authz.role_bindings` and `ci.issuers`
  (policies, repository settings and the authz settings are not written by it). A replayed unbinding is recorded (also when the row was
  already gone, and for every role of a replayed group deletion) by role and name in `system_state.journal_suppressed_bindings`; `ApplyDeclared` ([declared.go](../../internal/authz/declared.go)) then does not
  re-create the binding from an older configuration, logs it, and lists it in the outcome, until an administrator binds the role
  again through the API (Q-AUTHZ-235; `TestR_AUTHZ_010_DeclaredBindingRemovedByReplayIsNotBroughtBack`). The CI issuers have no such
  mark: [H-565](ci-federation.md#h-565--a-replayed-disable-of-a-configuration-declared-ci-issuer-is-undone-by-trove-migrate-when-the-restore-runs-with-an-older-configuration).
- **Credential.** The shipper writes with the runtime credential and deletes nothing. The reader of a restore uses the credentials file
  of [open.go](../../internal/journal/replay/open.go) and lists object versions with it.

Not verified, for the exposure and CI handlers: the restore-level runs (`npm view` on every replica of a restored installation, an
exchange against it), which are the end-to-end suite's; the handlers' outcomes, writes and change-area notes are verified against
PostgreSQL through the real shipper and replayer.

Not verified: that a delete marker is read through on a versioned bucket. The store implements it
([journalstore.go](../../internal/blob/s3/journalstore.go)); the integration stack's bucket is not versioned, so only the listing
path runs there.

### What a forged entry can deny, and how an administrator recovers

An entry written by a holder of the runtime blob credential is replayed like a genuine one. None of the handlers below widens
access; each can deny something. The replay prints one line per entry with its outcome; it prints the detail of a result only when the
result is `suspended` (for a repository: the repository, the parts, the part that suspended it and the whole entry) or `revived`.
An `applied` repository or claims result prints no detail beyond the names of the parts in the result itself, so the administrator
reads what an applied entry changed from the audit trail of the source and from the repository, not from the output. A line prints
the event name and the subject quoted, because both come from the object. Every recovery is a change that widens, which is never journaled, so a later restore does not undo it.

| Handler | The most a forged entry does | Recovery |
|---|---|---|
| `repository.narrowed` `excludeAdded` | Adds any exclude pattern (up to 100 per repository), for example `**`: the repository serves, fetches and resolves no name that matches | Remove the pattern with a repository update |
| `repository.narrowed` `include` | Replaces the restored include list by its intersection with the entry's, or disables the repository when none is shared: names outside both lists become unreachable | Edit the list; enable the repository |
| `repository.narrowed` `vulnerabilities` | Raises a member to the more blocking of both values, for example `onMissingData: block` and `enabled: true`: versions of an ecosystem whose advisory data is incomplete are refused (`vulnerability_data_missing`), and a rescan is queued | Set the member back; lowering it needs `repositories:write-config` (R-VULN-002) |
| `repository.narrowed` `membersRemoved`, `pinsAdded`, `pinMembersRemoved` | Takes members out of a virtual repository or its pin rules, or disables it: the names of a member that is gone are unreachable through that address | Add the members back; enable the repository |
| `repository.claims_changed` | Creates claims on a local repository (the limit of 1000 claims does not stop a replayed claim, and the replay warns about a repository above it): a broad `global` claim such as `**` makes the claimant the only source of every name of the format in every virtual repository and refuses every name at the remote repositories of the format; a manual claim that overlaps another repository's claim disables the claimant, whose claim stays in effect | Replace the claims (`PUT …/claims`, a `global` claim needs `repositories:write-config`) |
| `group.updated` with `renamedFrom` | Detaches any group from group synchronization and deletes its `oidc:*` memberships: the members the provider filled lose the group's grants until recovery | `PATCH /api/v1/groups/<name>` with `{"syncDetached": false}`; the next sign-in of each member refills the group |
| `user.updated` | Removes the pending identity link of any user for any provider: the provider's login no longer takes the user over | Add the link again with `PATCH /api/v1/users/<username>` |
| `settings.auth.changed` | Lowers each token lifetime maximum, to 60 seconds at the least: new tokens live that long, and anonymous access is switched off when the entry says so | Raise the maxima and switch anonymous access on in the settings |
| `vulnerability.finding.*` with `block: set` | Blocks any version of a repository of the restored database, creating the finding when it is missing: the version is refused until the finding is triaged. An entry is found by its ID only when repository, package version and advisory match; a finding that a replay created takes the stronger kind and severity of every later copy (each raise has a history row), a finding restored from the backup is never raised | Triage the finding; the first complete import after a restore also reconciles it |
| `package.published`, `package.deleted` of a local NuGet repository | Takes a version number that the restored database does not hold out of use: reserves it for the bytes the entry names, or refuses it for every package (a removal, or an addition that names other bytes than another entry of the version, Q-NUGET-112); the genuine package cannot come back under that number, not even byte-identically | None: no operation removes a tombstone, the package takes a new version number ([H-585](nuget-publish.md#h-585--a-forged-nuget-journal-entry-takes-a-version-number-out-of-use-for-good)) |
| `token.*`, `user.*`, `serviceaccount.*`, `bastion.*`, `ci.*`, `authz.*`, `endpoint.exposure.*`, `repository.disabled`, `repository.deleted`, `npm.unpublish` | Revokes, disables, deletes or narrows the object the entry names, if the restored database holds it | Create the token again, enable the object, restore the grant; an unpublished version is reinstated with `trove admin npm reinstate` |

## What a restore trusts

A restore trusts the manifest and the files of the backup it is told to restore (H-46). It checks formats, sizes, schema steps and the key
ring before it writes ([format](../../internal/backup/format) refuses a manifest of a higher format, `ExitPrecondition`), but a
consistent forgery by a writer to the target passes. The target marker ties a target to one installation and incarnation, so a second
installation cannot write into it (`Marker.Check`, exit 4).

## What a restore does to the database

A restore runs as the schema owner (check 6 refuses any other credential) on one session connection that holds the migration lock
exclusively until it ends ([migrate_restore.go](../../internal/meta/postgres/migrate_restore.go)). Its first write is the anchor, a
schema comment `trove-restore <backup-id>`; from then on `trove migrate` refuses the database and a replica stays unready while
`system_state` holds `restore_in_progress` ([gate.go](../../internal/meta/postgres/gate.go), `CheckSchema`: the marker is read after the
schema state, because the restore commits it with the step that makes the schema servable). The load disables the user triggers and
truncates every table in one transaction ([backup_load.go](../../internal/meta/postgres/backup_load.go)), which is the reason for three
guards:

- The tables, columns and sequences of the load come from the manifest only after they were compared with the schema the embedded
  migrations built; a name that is not in the catalog never reaches a statement, and each identifier is quoted (`pgx.Identifier`).
  A forged manifest can therefore choose rows, not objects (H-46 covers the rows).
- A table file is hashed while it streams and compared with the manifest's size and SHA-256 before the load commits
  ([source.go](../../internal/backup/restore/source.go)); a damaged file aborts the transaction.
- The target is only read. The blob store gets one probe object under `_probe/` and the objects of the backup; an object that is
  already there with the right size is kept, a streamed one is hashed before it becomes visible, and one copied server-side is
  checked by size only (H-558).

The restore holds no Valkey credential, and the fix-ups give the installation a new keyspace and incarnation, so entries of the old
installation are never read ([system_state.go](../../internal/backup/fixup/system_state.go)).

## What this does not cover

The gaps of the backup itself:

- [H-46](../spec/backup-and-restore.md#h-46--backup-integrity-is-not-authenticated): backup integrity is not authenticated.
- [H-47](../spec/backup-and-restore.md#h-47--the-backup-role-can-read-everything-and-delete-backups): the backup role can read everything and delete backups.
- [H-48](../spec/backup-and-restore.md#h-48--database-files-are-readable-to-every-reader-of-the-target-until-p1): database files are readable to every reader of the target.

The mechanisms of this page add:

- [H-37](../spec/ha-and-helm.md#h-37--a-database-restore-revives-revoked-credentials-and-removed-grants): a database restore revives revoked credentials and removed grants where the journal does not reach.
- [H-50](../spec/formats/nuget.md#h-50--a-restore-revives-nuget-versions-deleted-after-the-backups-snapshot-and-can-free-lost-version-numbers): a restore revives NuGet versions deleted after the backup's snapshot and can free lost version numbers.
- [H-51](../spec/formats/npm.md#h-51--a-restore-reverts-npm-metadata-changes-made-after-the-backups-snapshot): a restore reverts npm metadata changes made after the backup's snapshot.
- [H-585](nuget-publish.md#h-585--a-forged-nuget-journal-entry-takes-a-version-number-out-of-use-for-good): a forged NuGet journal entry takes a version number out of use for good.
- [H-553](../spec/backup-and-restore.md#h-553--a-journal-object-can-be-overwritten-with-the-runtime-credential): a journal object can be overwritten with the runtime credential.
- [H-554](../spec/backup-and-restore.md#h-554--a-compromised-backup-pod-can-hold-garbage-collection): a compromised backup pod can hold garbage collection.
- [H-557](../spec/backup-and-restore.md#h-557--the-post-restore-does-not-verify-the-blobs-and-a-replay-writes-no-audit-event-per-changed-object): post-restore does not verify the blobs, and a replay writes no audit event per changed object.
- [H-564](../spec/ha-and-helm.md#h-564--the-platform-check-reads-settings-not-outcomes): the platform check reads settings, not outcomes.
- [H-565](ci-federation.md#h-565--a-replayed-disable-of-a-configuration-declared-ci-issuer-is-undone-by-trove-migrate-when-the-restore-runs-with-an-older-configuration): a replayed disable of a configuration-declared CI issuer is undone by `trove migrate` when the restore runs with an older configuration.
- [H-555](../spec/backup-and-restore.md#h-555--the-run-history-is-only-as-trustworthy-as-the-backup-role): the run history is only as trustworthy as the backup role.
- [H-556](../spec/backup-and-restore.md#h-556--adopting-a-target-and-writing-its-marker-are-not-atomic-on-a-store-without-conditional-writes): adopting a target and writing its marker are not atomic on a store without conditional writes.
- [H-558](../spec/backup-and-restore.md#h-558--a-server-side-copy-into-the-blob-store-is-checked-by-size-not-by-content): a server-side copy into the blob store is checked by size, not by content.
- [H-569](#h-569--upstream-storage-and-format-settings-of-a-repository-come-back-with-a-restore): upstream, storage and format settings of a repository come back with a restore.
- [H-566](../spec/backup-and-restore.md#h-566--the-drill-uses-one-s3-login-for-the-blob-store-the-backup-target-and-the-restore): the drill uses one S3 login for the blob store, the backup target and the restore.
- [H-567](../spec/backup-and-restore.md#h-567--hand-written-journal-objects-are-tried-at-replay-level-not-through-the-restore-job-on-a-cluster): hand-written journal objects are tried at replay level, not through the restore Job on a cluster.
- [H-568](../spec/backup-and-restore.md#h-568--the-restore-across-releases-and-across-postgresql-major-versions-is-not-run-by-the-drill): the restore across releases and across PostgreSQL major versions is not run by the drill.

### H-569 · Upstream, storage and format settings of a repository come back with a restore

A review of the write paths that remove access (2026-11-07, F-72) found narrowings recorded in the audit trail without a journal
entry. They are journaled now and replayed by every restore: a pending identity link removed and a group renamed away from the name
the identity provider fills ([R-AUTH-041](../spec/identity-and-authentication.md#r-auth-041--authentication-audit-events),
Q-AUTH-205, Q-AUTH-206), a lowered token lifetime maximum (Q-AUTH-207), and a repository's narrowed patterns, `autoClaim`, virtual
members, pin rules, stronger `vulnerabilities` member and added claims
([R-REPO-061](../spec/repositories-and-proxy.md#r-repo-061--narrowings-of-a-repository-in-the-revocation-journal)). Where re-applying
one of them could widen what a repository serves, the replay disables the repository instead; that costs availability until an
administrator reviews the printed entry.

What a restore still reverts is the rest of the repository document, by
[Q-REPO-246](../spec/repositories-and-proxy.md#q-repo-246--patterns-auto-claims-virtual-members-pin-rules-and-the-vulnerabilities-member-are-journaled-upstream-storage-and-format-settings-are-not):
the rule is that a setting is journaled when its tightening decides which names a repository serves or which versions it withholds,
and that the replay can re-apply it as a removal. Settings of how trove reaches an upstream and what it accepts from it or from
clients are not, and some of them are security relevant: tightened after the restore point, they are loose again after a restore,
the audit events that recorded the tightening are gone with the restored database, and the operator finds them in the log stream of
the audit events or in his own records. A setting that was loosened after the restore point comes back tight, which costs
availability only.

- **Where a remote repository sends its credential and what it asks.** The upstream `url` (a remote re-pointed from an upstream that
  turned hostile), an `extraHosts` entry removed or switched to `sendCredentials: false`, `forwardAuthOnRedirect` switched off, a
  rotated upstream credential (R-REPO-024, R-REPO-039, R-REPO-043): after the restore the remote asks the earlier URL and sends the
  earlier credential to that host again. The restored credential itself comes back with the backup's sealed secrets.
- **What a remote repository accepts.** `integrity.checksumPolicy` raised to `require`, `integrity.blockUnexpectedContentTypes`
  switched on, `availability.mode: offline` (R-REPO-041, R-REPO-042, R-REPO-030), and the `mirror` section: after the restore the
  remote stores what the tightened setting refused, asks an upstream an administrator took offline, or mirrors again what was
  excluded. Real when the upstream was compromised after the restore point.
- **What a local repository accepts from clients.** `storage.checksumPolicy` raised to `verify-client` and lowered upload limits
  ([R-STOR-010](../spec/storage.md#r-stor-010--client-checksum-policy-and-digest-sidecar-files)): deploys the tightened policy
  refused are accepted again.
- **npm.** `serveRemovedVersions` switched off ([R-NPM-026](../spec/formats/npm.md#r-npm-026--versions-removed-upstream)): a remote
  serves the cached tarball of a version its upstream removed again. `latestGuard` switched on, and `latestStrategy` and
  `distTagStrategy` changed (R-NPM-030): a publish moves `latest` again, and a virtual repository merges the dist-tags as before.
  `audit.mode` switched from `forward` to `local` and `audit.providerURL` changed (R-NPM-038): the names a client asks about reach
  the earlier advisory provider again.
- **NuGet.** `forwardSearch` switched off (R-NUGET-016), `vulnerabilityInfo` switched to `off`, and `deleteMode` changed: the names
  a client searches for reach the upstream again, the service index lists the earlier vulnerability information, and a delete
  behaves as before.

Sessions are not on the list: a backup restore loads no session (R-BAK-004) and `trove admin post-restore` ends every session.
