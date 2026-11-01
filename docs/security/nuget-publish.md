# NuGet publishing

Status: built (M3, phase 4): the push, the unlist, the relist and the delete of a NuGet package, the immutability of a version number
with its tombstones, and the journal entries of R-NUGET-033. The reads of package content and documents, search, symbols and the
remote and virtual classes of NuGet have no page yet; they are not built.

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
| 4 | `deploy` on the ID the package declares, and the early, read-only form of the ownership rule | `Host.Authorize`, `Host.CheckOwnership` | 403 |
| 5 | The number: stored with the same SHA-512 is a replay; stored with other bytes, tombstoned, or reserved for other bytes is a conflict | `Store.VersionFacts`, `Store.Tombstone` | 201 without any write, or 409 `nuget_version_exists`, `nuget_version_deleted`, `nuget_version_reserved` |
| 6 | For the first version of an ID: whether a remote member of a containing virtual repository serves it | `Resolver.ShadowCheck` | 403 `shadows_remote_package`, 503 `shadow_check_unavailable` |
| 7 | The commit transaction: the package row is locked `FOR UPDATE`, then the versions and the tombstone are read, then the ownership rule decides again and claims the ID, then the rows and the audit events are written | [hooks.go](../../internal/formats/nuget/hooks.go) `OnCommit` | 409, 403, or 201 |

Steps 5 and 6 are decided outside any transaction and decided again at step 7: the early answer is an optimization and a better message, the
check inside the transaction is the one that protects the repository. A push that loses a race for the path of the package is read again
and answers what the number now says.

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
  ends every run in a state with exactly one of the two.
- **A `reserved` tombstone** is what a restore's journal replay writes for a version that was published after the snapshot. It refuses other bytes
  with `nuget_version_reserved`, and it admits the one package whose SHA-512 it holds, deleting itself in that push's transaction. A protocol delete of
  that number in a `delete` repository turns it into a `removed` tombstone, so the journaled bytes are refused as well. The replay itself is not built.

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

## What the audit and the journal record

The events are written in the transaction of the change, so no change exists without its record (R-OPS-015): `package.published` with the file list and the
outcome of the namespace claim, `package.deleted`, `package.metadata_changed` with `change` of `unlist` or `relist`, the security event
`repository.remote_package_shadowed` for a first version published on the strength of `manage` over a package a remote member serves, and, from the item
store, `artifact.deployed` for the `.nupkg` and for the generated `.nuspec` and `artifact.deleted` for each removed file. The two package events
carry the journal entry of R-NUGET-033, `{"repository": <id>, "id": <lower id>, "version": <URL form>, "sha512": <hex>}`: identifiers and a digest, no token, no
principal name. A push of a cache fill (a remote class that does not exist yet) records no event.

## What this does not cover

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

Stage: the spool of `PUT <feed>/v3/package` and the reading of the package ([handlers_publish.go](../../internal/formats/nuget/handlers_publish.go)).

Subject: any principal that holds `deploy` on a local NuGet repository, for an ID it may not write, and any principal that is refused at the ownership rule.

Live today. The ID of a package is inside it, so the permission on the ID, the ownership of an ID that has an owner and the number are decided after the body
was spooled and the archive read; only the coarse `deploy` on the repository is decided before the first byte (R-NUGET-005 step 5). npm names the package in the
URL and decides before reading; NuGet's route has no ID in it.

What makes it real: a principal with `deploy` on the repository but no right to the ID sends large packages for IDs it may not write. Each costs the
replica the spool up to the repository's upload limit, the read of the central directory and the `.nuspec`, and ends in a 403, without a stored blob and
without a row. The cost is bounded by the upload size limit and the spool budget (503 with `Retry-After` of at most 30 s when it is exhausted); a repository where
`deploy` is granted to many principals widens it.
