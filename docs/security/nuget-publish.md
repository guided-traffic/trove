# NuGet publishing

Status: built (M3, phase 4): the push, the unlist, the relist and the delete of a NuGet package, the immutability of a version number
with its tombstones, and the journal entries of R-NUGET-033. The reads of package content and documents, search, symbols and the
remote and virtual classes of NuGet are built; the remote class has its own page, [nuget-remote.md](nuget-remote.md), the virtual
class [nuget-virtual.md](nuget-virtual.md), and the
exposure of documents and search on endpoints with rules is on [endpoints-and-exposure.md](endpoints-and-exposure.md). The symbol push is below.

This page describes what a caller can do to a local NuGet repository through the protocol and what stops a caller from doing more.
The authorization it relies on is on [permissions.md](permissions.md), the credentials on [tokens-and-credentials.md](tokens-and-credentials.md),
the raw `/files/` roads on [file-transfer.md](file-transfer.md), and the audit and the journal on [audit.md](audit.md). The requirements are
R-NUGET-005 to R-NUGET-007 and R-NUGET-032 in [spec/formats/nuget.md](../spec/formats/nuget.md); the code is
[internal/formats/nuget](../../internal/formats/nuget) and [internal/formatdata/nuget](../../internal/formatdata/nuget).

## The order of the checks of a push

`PUT <feed>/v3/package` decides in this order. A refusal stops the request at its step and nothing after it runs.

| Step | What is decided | Where | Answer |
|---|---|---|---|
| 1 | The caller holds `deploy` on the repository at all, before a byte of the body is read | the dispatcher's coarse check | 401, 403 |
| 2 | The body is spooled: the first part of a multipart form, or the raw body; at most 64 KiB of preamble before the first part; the upload size limit of the repository and the spool budget | [handlers_publish.go](../../internal/formats/nuget/handlers_publish.go) `packagePart`, the host's spool | 400, 413, 503 with `Retry-After` of at most 30 s |
| 3 | The archive and the `.nuspec` are read within the `nuget.archive` limits; the ID by the rule of a local repository (ASCII), the version, every dependency ID and range; a symbol package is refused | [publish.go](../../internal/formats/nuget/publish.go) `readIncoming`, [nupkg](../../internal/formats/nuget/nupkg), [nuspec](../../internal/formats/nuget/nuspec) | 400 `nuget_invalid_package`, `nuget_invalid_id`, `nuget_invalid_version`, `nuget_invalid_version_range` |
| 4 | `deploy` on the ID the package declares, at the path of its `.nupkg` (Q-NUGET-113), and the early, read-only form of the ownership rule | `Host.Authorize`, `Host.CheckOwnership` | 403 |
| 5 | The number: stored with the same SHA-512 is a replay; stored with other bytes, tombstoned, or reserved for other bytes is a conflict | `Store.VersionFacts`, `Store.Tombstone` | 201 without any write, or 409 `nuget_version_exists`, `nuget_version_deleted`, `nuget_version_reserved` |
| 6 | For the first version of an ID: whether a remote member of a containing virtual repository serves it | `Resolver.ShadowCheck` | 403 `shadows_remote_package`, 503 `shadow_check_unavailable` |
| 7 | The commit transaction: the package row is locked `FOR UPDATE`, then the versions and the tombstone are read, then the ownership rule decides again and claims the ID, then the rows and the audit events are written | [hooks.go](../../internal/formats/nuget/hooks.go) `OnCommit` | 409, 403, or 201 |

Steps 5 and 6 are decided outside any transaction and decided again at step 7: the early answer is an optimization and a better message, the
check inside the transaction is the one that protects the repository. A push that loses a race for the path of the package is read again
and answers what the number now says.

Step 3 checks the archive with a bounded scan of its central directory and then lets `archive/zip` open the `.nuspec`; what the scan checked is
what is read. An archive with another end-of-central-directory record after the one that ends it, which `archive/zip` would take instead, and an
entry whose data `archive/zip` would read from another offset than the one the scan checked in its local header are refused like any other
damaged archive (Q-NUGET-89; `TestR_NUGET_005_AnEndRecordAfterTheOneTheScanChoseIsRefused`). The same reader checks a package a remote caches.

## A version number is taken for good

A version number of a local repository is immutable for the life of the repository (Q-NUGET-6):

- **Other bytes** under a stored number are 409 `nuget_version_exists`, in any spelling of the version (`1.2.3`, `1.2.3.0`, `01.02.03`).
- **A removal writes a tombstone** in the transaction that removes the version: the protocol `DELETE` in `delete` mode, a raw or management delete of the
  `.nupkg` through `/files/`, and a move out of the repository. All of them take one road, the hook `OnItemsRemoved`
  ([hooks_remove.go](../../internal/formats/nuget/hooks_remove.go)), so no road can forget the tombstone. The tombstone holds the SHA-512 of the
  removed package, read from the blob row by the SHA-256 the removal carries (Q-NUGET-39). A tombstoned number refuses every push, identical bytes included.
- **Nothing removes a tombstone** except the restore of the version from the trash can (P1), the identical push against a tombstone of kind `reserved`
  (below), and the deletion of the repository. The statement `NuGetDeleteTombstone` removes only when the digest of the stored package equals the digest
  the tombstone holds.
- **A repeated push of identical bytes** answers 201 and writes nothing: no row, no revision, no audit event; the counter
  `trove_nuget_push_replayed_total` counts it. The caller needs `deploy` and the ownership of the ID for it like for any push, so the answer tells a
  caller nothing it does not hold the right to learn.
- **The lock order** is the package row first, the number second. A push racing a hard delete of the same number sees the version or its tombstone, never
  neither. The reads of the hook run in its own transaction (Q-NUGET-40); the integration test `TestR_NUGET_006_APushRacingAHardDeleteSeesTheVersionOrItsTombstone`
  ends every run in a state with exactly one of the two. A hard delete never waits inside its transaction for a folder that a push holds until its
  commit, so a push and a hard delete of another version of the same ID do not deadlock and neither fails when they collide repeatedly (Q-NUGET-88,
  `TestR_NUGET_007_APushAndAHardDeleteOfAnotherVersionNeverDeadlock`).
- **A `reserved` tombstone** is what a restore's journal replay writes for a version that was published after the snapshot
  ([formatdata/nuget/journal_replay.go](../../internal/formatdata/nuget/journal_replay.go), `published`). It refuses other bytes
  with `nuget_version_reserved`, and it admits the one package whose SHA-512 it holds, deleting itself in that push's transaction. A protocol delete of
  that number in a `delete` repository turns it into a `removed` tombstone, so the journaled bytes are refused as well. The replay takes the entries in
  the order of their time, which the writer of an object chooses, and its end state does not depend on that order: a `package.published` or `package.deleted` entry that meets
  a `reserved` tombstone for other bytes turns it into a `removed` tombstone and prints a warning naming both SHA-512s, so a number stays reserved only
  for a digest that no other entry of the version contradicts, and an entry with an earlier time cannot take the number for its bytes
  ([Q-NUGET-112](../spec/formats/nuget.md#q-nuget-112--two-replayed-additions-of-one-version-that-name-different-bytes-leave-its-number-refusing-every-package);
  `TestR_NUGET_033_TwoAdditionsThatNameDifferentBytesLeaveTheNumberRefusedInEitherOrder`,
  `TestR_NUGET_033_AForgedAdditionWithAnEarlierTimeCannotTakeTheNumberOfAGenuineOne`). What such an entry still costs is the number
  ([H-585](#h-585--a-forged-nuget-journal-entry-takes-a-version-number-out-of-use-for-good)).

## Unlist, relist and delete

| Request | `deleteMode: unlist` (default) | `deleteMode: delete` | Needs |
|---|---|---|---|
| `DELETE <feed>/v3/package/{id}/{version}` | unlists; a repeat is 204 and writes nothing | removes the files and the version, writes the tombstone; a repeat is 204 | `delete` on the ID and the ownership |
| the same for a tombstoned number | 204, nothing changes | 204, nothing changes (a `reserved` tombstone becomes `removed`) | the same, decided first |
| the same for a number the repository never held | 404 | 404 | `delete` on the ID |
| `POST <feed>/v3/package/{id}/{version}` | relists; a repeat is 200 | the same; 404 for a removed version | `deploy` on the ID and the ownership |

The permission and the ownership are decided before the 204 for a tombstoned number is chosen, so the 204 tells nothing to a caller who could not delete the
version. The route takes the package row lock before it reads the version, and the ownership is asked inside that transaction. An ID without a package
row and without a tombstone is 404 before any lock is taken, so a delete never creates a package row.

An unlisted version stays downloadable: the serve check ([servecheck.go](../../internal/formats/nuget/servecheck.go)) refuses a file whose number is tombstoned
and a file of a version the repository holds no NuGet row for, and serves the rest (Q-NUGET-37).

## The symbol push

`PUT <feed>/v3/symbolpackage` (R-NUGET-020) is an API-key route like the push and decides in the same order as steps 1 to 4 above, then:

| Step | What is decided | Where | Answer |
|---|---|---|---|
| 5 | The version the symbols belong to is stored and live in this repository; a deleted, tombstoned or never pushed number is the same answer, after the permission and the ownership, so a caller who may not write the ID learns nothing of its versions | `Store.VersionFacts` | 404 |
| 6 | The symbol package of the version: the same bytes are a replay, other bytes are a conflict, because symbols must match the binaries of the immutable package | `Items().Stat` at the `.snupkg` path, SHA-256 | 201 without a write, or 409 `nuget_symbol_package_exists` |
| 7 | The commit transaction locks the package row, finds the live version again, decides the ownership again, links the item to the version and writes the row and the job | `commitSymbols` | 404, 403, 409 or 201 |

The content is checked before any of this by `symbols.Validate`: only the entry extensions `.pdb`, `.nuspec`, `.xml`, `.psmdcp`, `.rels` and `.p7s` (so no assembly can be
smuggled in as "symbols"), and each `.pdb` starts with a Portable or a Windows PDB signature. The signature check inflates 32 bytes of an entry and refuses an entry
beyond the compression ratio of the archive rules, so a crafted `.pdb` costs no more than a crafted `.nuspec`. Nothing is extracted: the job `nuget-symbol-index` only records the
state of the package until the symbol server (R-NUGET-021) ships. The symbol push reads its body in full before the permission on the ID is decided, like the package push
([H-334](#h-334--a-push-is-read-in-full-before-the-permission-on-its-id-can-be-decided)).

## What the audit and the journal record

The events are written in the transaction of the change, so no change exists without its record (R-OPS-015): `package.published` with the file list and the
outcome of the namespace claim, `package.deleted`, `package.metadata_changed` with `change` of `unlist` or `relist`, the security event
`repository.remote_package_shadowed` for a first version published on the strength of `manage` over a package a remote member serves, and, from the item
store, `artifact.deployed` for the `.nupkg` and for the generated `.nuspec` and `artifact.deleted` for each removed file. The two package events
carry the journal entry of R-NUGET-033, `{"repository": <id>, "id": <lower id>, "version": <URL form>, "sha512": <hex>}`: identifiers and a digest, no token, no
principal name. The cache fill of a remote repository is no push and records none of these events.

## What this does not cover

The gaps of the spec pages whose mechanism this page describes:

- [H-36](../spec/formats/nuget.md#h-36--package-signatures-are-neither-verified-nor-produced): package signatures are neither verified nor produced.
- [H-50](../spec/formats/nuget.md#h-50--a-restore-revives-nuget-versions-deleted-after-the-backups-snapshot-and-can-free-lost-version-numbers): a restore revives NuGet versions deleted after the backup's snapshot and can free lost version numbers.

### H-333 · Unlisting is not a withdrawal: a holder of `deploy` relists

Stage: `POST <feed>/v3/package/{id}/{version}` and the unlist of `DELETE` in a repository whose `deleteMode` is `unlist`
([handlers_delete.go](../../internal/formats/nuget/handlers_delete.go)).

Subject: every local NuGet repository in the default mode; every version of every ID.

Live today. Unlisting needs `delete` and relisting needs `deploy`, both with the ownership of the ID (R-NUGET-007). A person with `deploy` on an ID can
therefore list again what an owner with `delete` unlisted, and the version was downloadable the whole time: unlisting hides a version from search and
leaves it in the version list and in the flat container.

What makes it real: an unlist used as a first response to a vulnerable or malicious version. It reduces who finds the version by search and does not
stop a restore that resolves it, and a second principal with `deploy` can undo it. A withdrawal that holds is the hard delete, which writes a tombstone and
removes the files; it is a different permission and a repository setting (`deleteMode: delete`), and the version number is gone for good.

### H-334 · A push is read in full before the permission on its ID can be decided

Stage: the spool of `PUT <feed>/v3/package` and of `PUT <feed>/v3/symbolpackage`, and the reading of the package ([handlers_publish.go](../../internal/formats/nuget/handlers_publish.go), [handlers_symbols.go](../../internal/formats/nuget/handlers_symbols.go)).

Subject: any principal that holds `deploy` on a local NuGet repository, for an ID it may not write, and any principal that is refused at the ownership rule.

Live today. The ID of a package is inside it, so the permission on the ID, the ownership of an ID that has an owner and the number are decided after the body
was spooled and the archive read; only the coarse `deploy` on the repository is decided before the first byte (R-NUGET-005 step 5). npm names the package in the
URL and decides before reading; NuGet's route has no ID in it.

What makes it real: a principal with `deploy` on the repository but no right to the ID sends large packages for IDs it may not write. Each costs the
replica the spool up to the repository's upload limit, the read of the central directory and the `.nuspec`, and ends in a 403, without a stored blob and
without a row. The cost is bounded by the upload size limit and the spool budget (503 with `Retry-After` of at most 30 s when it is exhausted); a repository where
`deploy` is granted to many principals widens it.

### H-585 · A forged NuGet journal entry takes a version number out of use for good

Stage: the replay of `package.published` and `package.deleted` entries of local NuGet repositories after a restore
([formatdata/nuget/journal_replay.go](../../internal/formatdata/nuget/journal_replay.go), `published` and `deleted`;
[R-NUGET-033](../spec/formats/nuget.md#r-nuget-033--nuget-data-in-backups-and-restores), Q-NUGET-10, Q-NUGET-112), and the push's
tombstone check ([hooks.go](../../internal/formats/nuget/hooks.go), `tombstoneVerdict`).

Subject: every local NuGet repository after a restore; the version numbers the restored database does not hold, those published after the
restore point (which the restore lost) and those never published. Live whenever a restore replays the journal.

A journal object is untrusted input: whoever holds the runtime S3 credential can write one (R-HA-054). A forged `package.deleted` turns a
reservation into a `removed` tombstone or writes one, and a forged `package.published` that names other bytes than the genuine entry of the same
version, in either order of their times, leaves the number refusing every package (Q-NUGET-112). Either way the genuine package can no longer
come back under its number, not even as the byte-identical rebuild that a reservation admits, and a client that pinned the version (a lock file,
an exact reference) gets 404 until it moves to a new version. A forged `package.published` for a number that no entry and no row names reserves
it for the forger's bytes; a holder of `deploy` and the ownership of the ID could push those under that unused number without the entry as well,
so it costs the number, not the content of a number clients resolved. Nothing removes a tombstone except the identical push against a `reserved` one, the restore
from the trash can (P1) and the deletion of the repository (R-NUGET-006), so there is no recovery short of a new version number. The replay
prints a line for every entry and a warning where an addition or a removal meets a `reserved` tombstone for other bytes; an addition that
arrives after the number is already `removed` is silent, so the order decides whether a forged addition is reported, not what the number ends
as. It never deletes a stored version and never replaces bytes.

What makes it real: the runtime S3 credential of the installation (every replica holds it), a restore that replays the journal, and a pipeline
that rebuilds a version the restore lost byte-identically and expects its number back. Where the genuine entry is missing from the journal the
replay reads (not shipped when the old database stopped, or deleted or overwritten with the runtime credential where the replay reads no object
versions), a forged entry is the only one for the number and reserves it for its bytes; the number would otherwise be free, which
[H-50](../spec/formats/nuget.md#h-50--a-restore-revives-nuget-versions-deleted-after-the-backups-snapshot-and-can-free-lost-version-numbers)
and [H-553](../spec/backup-and-restore.md#h-553--a-journal-object-can-be-overwritten-with-the-runtime-credential) record. What would close it: journal entries
authenticated with a key the runtime S3 credential does not give, or a reversible, reviewed way for an administrator to give a refused number
back to the package they confirm.
