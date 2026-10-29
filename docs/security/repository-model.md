# Repository model, paths and replica state

Status: the model, the path rules and the snapshot of milestone M1 are implemented. The authorization of a repository
request (who may read or deploy) is a separate perspective and has no page yet; this page covers what the repository
model itself decides and what a replica believes about it.

This page describes three mechanisms of [internal/repository](../../internal/repository/) that have a security
relation: the canonical path every repository request passes before authorization, the include and exclude patterns
that act as routing rules, and the per-replica snapshot that tells a request path whether a repository exists, is
disabled or wants authentication.

## Canonical paths

Every repository-relative path passes `repository.CleanPath` ([path.go](../../internal/repository/path.go)) before any
authorization runs (R-ARCH-005). The function decodes the percent-escapes exactly once and only then looks at the
segments, so an escaped traversal (`%2e%2e`, `..%2f`, a doubly escaped dot that stays literal) cannot reach a
comparison as anything but what it is after the one decode. It refuses, with `400 invalid_path`: a malformed escape,
bytes that are not UTF-8, an empty, `.` or `..` segment, NUL, a backslash, any control character (C0, DEL and C1), a
path above 4096 bytes and a segment above 255. A path is a file or a folder, never both, which the items store enforces
([storage.md](../spec/storage.md) R-STOR-007). The path that authorization, the include and exclude patterns, the
item lookup and the object key derivation see is therefore one string, and none of them re-decodes it. The object key
of a blob is derived from the content digest only ([blob-store.md](../developer/blob-store.md)); no path reaches it.

## Patterns as routing rules

`includePatterns` and `excludePatterns` of a repository ([pattern.go](../../internal/repository/pattern.go)) apply to
every caller, administrators included (R-REPO-005): a deploy outside the patterns answers `403 not_admitted`, a read
or delete of a path outside them answers `404` as if it did not exist. The matcher is compiled when the snapshot is
built and cannot fail at request time; a stored list that does not compile marks the repository disabled (below).
Patterns are not permissions: a path that the patterns admit is still subject to the grants of the caller.

## The snapshot and the configuration document

The snapshot ([reader.go](../../internal/repository/reader.go)) holds, per repository, the settings that request paths
enforce: `disabled`, `requireAuthentication`, `hideExistence`, the compiled patterns and the lifecycle. It is an
in-process copy of PostgreSQL rows, rebuilt in full when a change of the area `repo_config` reaches the replica
(Valkey message at once, the change-log poll within `cluster.epoch_poll_interval` otherwise) and at the latest after
`cache.snapshot_safety_ttl`. Valkey grants nothing and carries no copy of it (R-REPO-049).

- **Fail closed on a bad row.** A row whose stored document does not decode, or whose patterns do not compile, is loaded
  disabled and logged. It cannot be written by the service, which validates first; it could appear through a manual
  edit or a restore of a damaged dump.
- **Strict documents.** A configuration document with an unknown or duplicate member is refused (400
  `invalid_document`), so a misspelt `requireAuthentication` can never be dropped silently and leave the operator
  believing a repository demands authentication.
- **Every change is audited with the change.** The audit event is inserted in the transaction of the write
  (R-OPS-015), so no repository exists, changes or disappears without its record. Until the audit recorder is wired
  into the composition root, the service is built with a recorder that discards events; that is a wiring state, not a
  property of the design.
- **Lookup of unknown keys.** A key that cannot be a key never reaches the database; any other unknown key costs one
  indexed query per distinct key and entry lifetime (`cache.unknown_repo_keys`, 60 s), so a flood of requests for
  invented keys cannot turn into a query flood (R-ARCH-011).
- **Deletion.** From the first transaction of a deletion the repository is `deleting`: it is not resolvable by clients,
  its key stays taken, and the items go in batches before the row (R-REPO-013). A deleted key can be created again and
  is empty.

## What this does not cover

### H-132 · A replica enforces the previous repository settings until its snapshot is rebuilt

Component: the repository snapshot (R-REPO-049). Subject: `disabled`, `requireAuthentication`, `hideExistence` and the
include and exclude patterns of a repository whose settings were just changed. Live with milestone M1. The replica that
handled the write applies the change at once; every other replica applies it when the change-log poll or the Valkey
message reaches it, at most `cluster.epoch_poll_interval` plus one query and the rebuild without Valkey (2 s with the
defaults, [architecture.md](../spec/architecture.md) R-ARCH-018). Within that window another replica still serves a
repository that was just disabled, answers an anonymous read on a repository that was just set to require
authentication, or admits a path that a pattern now excludes. The grant index of the authorization area has the same
bound for grants. What makes it real: a change made to stop an exposure that is under way, while a client keeps
requesting through a replica that has not polled yet.

### H-133 · Paths are compared byte for byte, without Unicode normalisation

Component: `CleanPath` and the items store. Subject: lookalike paths. Live with milestone M1. `Report.pdf`, `report.pdf`
and two spellings of `é` (one precomposed code point, one letter plus a combining accent) are four different paths, and a
generic repository stores all of them. A person reading a listing, or a pattern written for one spelling, can mistake one
for another. The rule is deliberate: trove never rewrites what a client sent, and an include or exclude pattern
therefore matches exactly the bytes it is written with. What makes it real: a repository whose uploaders are not
trusted and whose consumers pick files by reading names; the remedy is to grant `deploy` only to accounts that
publish.
