# Repository model and the generic format

The repository model of [R-REPO-001 to R-REPO-013](../spec/repositories-and-proxy.md): classes, keys, the configuration
document, include and exclude patterns, validation, create, read, update, list and delete, and the per-replica snapshot
that every request path reads. Code: [internal/repository](../../internal/repository/) (domain),
[internal/formats](../../internal/formats/) with [formats/generic](../../internal/formats/generic/) (the format catalog
and the first format, [formats/generic.md](../spec/formats/generic.md)), the statements in
[queries/repository.sql](../../internal/meta/postgres/queries/repository.sql). The management API operations are thin handlers
over the functions below, in [repositories_api.go](../../internal/api/v1/repositories_api.go) (see "HTTP operations"); the
`x-trove-permission` declaration, the ETag/`If-Match` headers, the cursor and the idempotency key are the API layer's, everything
else is decided in the domain.
Milestone M1 implements the local class of the generic format only; remote and virtual repositories are specified
(R-REPO-017 to R-REPO-049, R-GENERIC-009, R-GENERIC-010) and not built.

## Files

| File | Responsibility |
|---|---|
| [repository.go](../../internal/repository/repository.go) | the shared contract: `Repository`, `Class`, `Lifecycle`, `Reader`, `KeyPattern`, `ReservedKeys()`; `Repository.Admits` |
| [document.go](../../internal/repository/document.go) | `Document` and `Settings` (the configuration document of R-REPO-004), `DecodeDocument` (strict: unknown and duplicate members), `MergePatch` (RFC 7396), `ConsumerView` |
| [validate.go](../../internal/repository/validate.go) | `validate`: every rule of R-REPO-008 in one pass; the error shape (one violation under its own code, several under `validation_failed`) |
| [key.go](../../internal/repository/key.go), [path.go](../../internal/repository/path.go) | `ValidKeyShape`, `IsReservedKey`; `CleanPath` (R-ARCH-005) |
| [pattern.go](../../internal/repository/pattern.go) | `CompilePattern`, `Pattern.Match`, `Admission` (include and exclude lists, folder admission) |
| [format.go](../../internal/repository/format.go) | `FormatInfo` and `FormatCatalog`: what the model needs of a format, without importing `internal/formats` |
| [store.go](../../internal/repository/store.go) | rows to `Repository` and `Document`; the sqlc `queries(tx)` seam; fail-closed load of an undecodable row |
| [service.go](../../internal/repository/service.go) | `Service`: `Create`, `Get`, `Replace`, `Patch`, `List`; `Precondition`, the `Hooks` seam (`OnUpdated`, `OnDelete`, `MultiHooks`); a stale version is `errs.Stale` |
| [delete.go](../../internal/repository/delete.go) | `Service.Delete`, the job kind `repository-delete` and its handler `RunDeleteJob` |
| [activity.go](../../internal/repository/activity.go) | `Activity`: the in-memory buffer of `last_activity_at` (R-REPO-012), flushed once a minute and at shutdown |
| [reader.go](../../internal/repository/reader.go) | `SnapshotReader` (the `Reader` implementation): the snapshot, the negative cache of unknown keys, the lookup of R-ARCH-011 step 2 |
| [codes.go](../../internal/repository/codes.go) | codes of other areas that the validation names as strings |
| [formats/formats.go](../../internal/formats/formats.go) | `Descriptor`, `Format`, `RequestDescriptor`, `Register`, `Lookup`, `Descriptors` |
| [formats/generic/generic.go](../../internal/formats/generic/generic.go) | the generic format: descriptor and request classification |
| [formats/all/all.go](../../internal/formats/all/all.go) | registers every format; the composition root imports it |
| [audit/events_repository.go](../../internal/audit/events_repository.go) | `repository.created`, `.updated`, `.deleted` |
| [app/wire_repository.go](../../internal/app/wire_repository.go) | the format catalog adapter, `wireRepository` (reader and service), the job kind registration |

## Request path: reader

`Reader.ByKey(ctx, key)` is what the files transfer, the format dispatcher and the grant index call per request
(R-ARCH-011 step 2). It never authorizes: callers decide first (R-AUTHZ-016) and only then look the repository up.

1. A key that cannot be a key (`ValidKeyShape`) is `repository_not_found` without a query.
2. The snapshot is one cache entry (`repositories`, registered on the replica's `cache.Registry`) holding the whole table.
   Any change of the change area `repo_config` drops it, and the next call rebuilds it with the fill rule of R-ARCH-018
   (a load that overlapped a change is not stored). `cache.snapshot_safety_ttl` bounds its age.
3. A key the snapshot does not hold is looked up in PostgreSQL, concurrent lookups of one key sharing one query. If it
   does not exist, the miss goes into `repo_key_misses` (bounded by `cache.unknown_repo_keys`, 60 s, flushed by every
   `repo_config` change, filled under the same fill rule). If it exists, the replica runs `Changes.PollNow` bounded by
   `repositories.lookup_wait`, drops the snapshot and answers from the rebuilt one; a poll that cannot finish in time
   answers `503 unavailable` with `Retry-After`, a row that is in PostgreSQL but still not in the snapshot is answered
   from that row (PostgreSQL decides).
4. A repository being deleted is returned with `Lifecycle` `deleting`; the caller treats it as not addressable.

A row whose stored document does not decode, or whose patterns do not compile, is loaded `Disabled` and logged: it takes
no part in resolution, download or deploy.

Everything here works without Valkey: the PostgreSQL change log brings a change to the other replicas within
`cluster.epoch_poll_interval`. [test/integration/repository](../../test/integration/repository/) runs two replicas without
a Valkey client.

## Writes: service

Every write is one transaction: the row, the audit event (`audit.Recorder.RecordTx`, so a failing audit insert rolls the
change back, R-OPS-015) and the change-log row (`Changes.Notify` on `repo_config`, payload `{id, key, op}`, never a
grant). A handler calls, after it has authorized:

| Operation | Function | Notes |
|---|---|---|
| `POST /repositories` | `Create(ctx, caller, body)`, `CreateTx(ctx, tx, …)` | `409 already_exists` also for a key being deleted; never an upsert; `CreateTx` runs in the transaction of an idempotency key |
| `GET /repositories/{key}` | `Get(ctx, key)` | from PostgreSQL (R-API-052); `Document.WithoutAdminNotes`, `Document.ConsumerView` for the narrower views |
| `PATCH` | `Patch(ctx, caller, key, pre, patch)` | RFC 7396 applied to the stored document; `pre` is `MatchVersion(n)` or `MatchAny()`; 428 is the handler's, because the header is |
| `PUT` | `Replace(ctx, caller, key, pre, body)` | omitted settings revert to defaults; an omitted `key`, `class` or `format` means "unchanged" |
| `GET /repositories` | `List(ctx, filter)` | sorted by key, keyset paging (`After`, `Limit`, `HasMore`), filters of R-REPO-012 |
| `DELETE` | `Delete(ctx, caller, key, detach)`, `DeleteTx(ctx, tx, …, pre)` | `DeleteResult{JobID, JobState, Done}`: 202 with the job, or 204 for a repository without content; `pre` is the optional `If-Match` (412 on a stale version) |

Errors are `*errs.Error`: `malformed_request` (400, field code `invalid_document` when no pointer is known), `validation_failed` (422, the codes of R-REPO-008 in `Fields`), `already_exists`
(409), `repository_not_found` (404), `precondition_failed` (412, with `*StaleVersion` as the cause carrying the current
version for `currentEtag`). An update that changes nothing writes nothing: same version, no event, no change row.
`Hooks.OnDelete` runs in the transaction that removes the row, just before the row goes (the job's `finishDelete`, and
`deleteNow` for a virtual repository): `authz.ExposureHook` ([exposure_hook.go](../../internal/authz/exposure_hook.go))
records the exposure rules the foreign key takes with it (`endpoint.exposure.deleted`, cause `repository_deleted`, Q-EP-25);
`repository.MultiHooks` composes the hooks, and a hook error fails the deletion. `Hooks.OnUpdated` runs in the update's
transaction with the repository before and after; the authorization area uses it
to record `authz.settings.changed` for `cacheFillAction`, `packageOwnership`, `requireAuthentication` and `hideExistence`
(R-AUTHZ-031): `authz.SettingsHook` ([settings.go](../../internal/authz/settings.go)), which `wireRepository` sets as `Options.Hooks`.
It records only the settings that changed, before and after, with a journal entry, and nothing for another change.

### Delete

`Delete` locks the row, sets `lifecycle = 'deleting'` (version + 1), records `repository.deleted` with
`phase: started`, notifies `repo_config` and enqueues the job `repository-delete` (exclusive per repository id, so a
repeat answers with the same job), all in one transaction. The job (`Service.RunDeleteJob`) deletes the items in
transactions of `storage.delete.batch_size`, reports progress, then removes the row, notifies and records the second
event (`phase: completed`) in the last transaction, which frees the key. It is idempotent and resumable. The kind is
registered in `app.jobRegistry` through `registerRepositoryJobs`; its handler finds the service at run time because the
registry is built and frozen before the repository component is wired. A virtual repository has no content and is
removed in the first transaction. `detach` has nothing to detach until virtual membership exists (R-REPO-017).

## HTTP operations

| Operation | Handler | Declaration | Notes |
|---|---|---|---|
| `GET /api/v1/formats` | `ListFormats` | `public` | the descriptors of `formats.Descriptors()`; 401 for an anonymous caller while anonymous access is off |
| `GET /api/v1/repositories` | `ListRepositories` | `public` | filters `class`, `format`, `q`, `state`, `project`, `view`; keyset cursor over the key; a holder of `repositories:read-config` sees every repository the endpoint exposes (`authz.ExposureFilter`, R-EP-014), the others the readable ones (`Authorizer.Readable`, Q-REPO-19); both through `visibleKeys`; `view=full` needs the capability |
| `POST /api/v1/repositories` | `CreateRepository` | `repositories:write-config` | strict body, `Idempotency-Key` through `Idempotency.Do`, `CreateTx` in its transaction; 201 with `Location`, `ETag: "1"`, the document and `warnings` |
| `GET /api/v1/repositories/{key}` | `GetRepository` | `public` | the capability holder passes the exposure ceiling of the endpoint (`visibleKeys`) and reads from PostgreSQL; the others pass the decision first (the unknown key is looked up and the index caught up before it, Q-ARCH-40) and get the consumer view; hidden and missing are `authz.NotFound()` |
| `PUT`, `PATCH /api/v1/repositories/{key}` | `ReplaceRepository`, `UpdateRepository` | `repositories:write-config`, `x-trove-if-match: required` | 428 before the body is read; the domain applies the merge patch and returns `*StaleVersion`, which becomes 412 with `currentEtag` |
| `DELETE /api/v1/repositories/{key}` | `DeleteRepository` | `repositories:write-config` | optional `If-Match` and `Idempotency-Key`; 202 with the job reference or 204 |

The representation ([repositoryRep](../../internal/api/v1/repositories_api.go)) is the `repository.Document` plus `lifecycle`,
`createdAt`, `updatedAt`, `lastActivityAt` and, on a write, `warnings`. The input drops those members before the strict decode, so a
body obtained by GET can be sent back (Q-API-34). A body error is `400 malformed_request` with the pointer of the member (Q-REPO-84).
The ETag of the full view is `"<config_version>"`, of the consumer view `"<config_version>-consumer"`. A read of one repository adds the
activity time while there is one, `"<config_version>.<microseconds>"` (`repositoryETag`), because `lastActivityAt` changes without a new
version and a strong tag follows the representation; a write answers the plain tag and accepts both (`repositoryPrecondition` strips the
suffix, `withoutActivity`) (Q-API-156).

## Formats

The format catalog is `formats.Register` at compile time (`formats/all`). `repository.FormatInfo` is derived from the
descriptor by `formatCatalog` in `app/wire_repository.go`; the model validates class, layout and the members that only
some formats have (`PackageNames`, `PriorityMembers`, `PinsAndClaims`, `Caps`) against it. The generic format sets none
of them, so `priorityMember`, `local.packageOwnership`, `ownershipDomain`, `autoClaim` and `local.caps` answer
`not_applicable` (R-GENERIC-008).

`formats.Format` is a skeleton: `Descriptor()` and `Describe(method, path, folder)`, the request descriptor of R-REPO-003.
The routes, storage hooks, remote and virtual adapters and the conformance kit of R-ARCH-008 and R-ARCH-010 join it with
the first format that needs them (npm, M2), bumping `ContractVersion`. A format imports nothing but `internal/formats`,
`errs`, `config` and `observability` (archtest R-ARCH-008); `formats/all` may import every format.

### How to add a format

1. Write its spec page under `docs/spec/formats/` (prefix, Covers, Decisions, What this does not cover) and add the page
   to the table in [spec/README.md](../spec/README.md).
2. Add `internal/formats/<id>` implementing `formats.Format` with a `Descriptor` whose `Classes` list only what is
   implemented; add a line to the tree of R-ARCH-003.
3. Register it in [formats/all/all.go](../../internal/formats/all/all.go).
4. Add its settings to `repository.Settings` as a member named after the format (a strict document refuses a member the
   struct does not know) and its validation to `validate.go`.
5. Name the requirement IDs in the tests; run `make coverage` and `go run ./internal/tools/coverage -links`.

## Paths and patterns

`CleanPath(escaped)` takes the repository-relative request path as received (still percent-encoded), decodes it exactly
once and refuses with `400 invalid_path`: a malformed escape, invalid UTF-8, an empty, `.` or `..` segment, NUL, a
backslash or any control character, more than 4096 bytes in total or 255 in a segment (Q-GENERIC-2). One trailing slash is
reported as `Folder`. Decoding first and splitting after means `%2e%2e` and `..%2f` are as dead as `..`.

`CompilePattern(subject, p)` implements the syntax of R-AUTHZ-005 for both subjects. The package owns the matcher because
`authz` imports `repository`, not the other way round; the authorization area should call `CompilePattern` rather than
keep a second copy (R-REPO-005: "a pattern copied between them selects the same names"). `Admission` holds both lists of
a repository, compiled once when the snapshot is built; `Repository.Admits(path, folder)` is what the files transfer calls.

## Configuration

| Key | Default | Meaning |
|---|---|---|
| `repositories.lookup_wait` | `5s` | how long a lookup of a key that PostgreSQL knows and the snapshot does not waits for the change-log poll before it answers 503 |
| `cache.snapshot_safety_ttl`, `cache.unknown_repo_keys` | existing | the snapshot's age bound; the size of the negative cache of unknown keys |
| `storage.delete.batch_size` | existing | items per transaction of the deletion job |

## Tests

| Tier | Where | Proves |
|---|---|---|
| unit | [internal/repository](../../internal/repository/) | path canonicalisation, keys, patterns and admission, document decoding, merge patch (RFC 7396 examples), validation (R-REPO-002, -005, -006, -008, R-GENERIC-001, -002, -007, -008) |
| unit | [internal/formats](../../internal/formats/) and below | registry rules; the generic descriptor and classification (R-GENERIC-001, -003) |
| integration | [test/integration/repository](../../test/integration/repository/) | create, read, update, list, delete against PostgreSQL with audit and change-log rows; the snapshot, the lookup and the poll bound on two replicas without Valkey (R-REPO-009 to -013, R-REPO-049, R-ARCH-011) |
| integration | [test/integration/files](../../test/integration/files/) (`repository_api_test.go`) | the repository API over HTTP on two replicas: the contract suite of E1.6 (`apitest.RunContract`: cursor walk, ETag with If-Match, merge patch, idempotency keys), validation, permissions, the consumer view, list visibility, the delete job on a worker, an idempotent create replayed through the other replica |

Run the integration area from the checkout root with the environment of the shared stack:
`go test -count=1 -tags integration ./test/integration/repository/...`.

## Decisions

[Q-REPO-12 to Q-REPO-23](../spec/repositories-and-proxy.md#decisions) and [Q-GENERIC-1 to Q-GENERIC-5](../spec/formats/generic.md#decisions).
