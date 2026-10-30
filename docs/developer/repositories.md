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
Milestone M1 implements the local class of the generic format only. Since M2 the remote settings (R-REPO-024, R-REPO-033),
their secrets and the upstream test are built for any format that registers the remote class (see "Remote settings"), and the
virtual class (members, deploy target, pin rules, claims and their routing) is built and described in
[virtual.md](virtual.md); the pull-through engine and the rest of R-REPO-024 to R-REPO-049 are specified and not built.

## Files

| File | Responsibility |
|---|---|
| [repository.go](../../internal/repository/repository.go) | the shared contract: `Repository`, `Class` (the catalog's), `Lifecycle`, `Reader`, `KeyPattern`, `ReservedKeys()`; `Repository.Admits` |
| [document.go](../../internal/repository/document.go) | `Document` and `Settings` (the configuration document of R-REPO-004), `DecodeDocument` (strict: unknown and duplicate members), `ConsumerView` |
| [validate.go](../../internal/repository/validate.go) | `validate`: every rule of R-REPO-008 in one pass; the error shape (one violation under its own code, several under `validation_failed`) |
| [key.go](../../internal/repository/key.go), [path.go](../../internal/repository/path.go) | `ValidKeyShape`, `IsReservedKey`; `CleanPath` (R-ARCH-005) |
| [pattern.go](../../internal/repository/pattern.go) | `CompilePattern`, `PatternSubject` (the catalog's), `Pattern.Match`, `Admission` (include and exclude lists, folder admission) |
| [store.go](../../internal/repository/store.go) | rows to `Repository` and `Document`; the sqlc `queries(tx)` seam; fail-closed load of an undecodable row |
| [service.go](../../internal/repository/service.go) | `Service`: `Create`, `Get`, `Replace`, `Patch`, `List`; `Precondition`, the `Hooks` seam (`OnUpdated`, `OnDelete`, `MultiHooks`); a stale version is `errs.Stale` |
| [delete.go](../../internal/repository/delete.go) | `Service.Delete`, the job kind `repository-delete` and its handler `RunDeleteJob` |
| [activity.go](../../internal/repository/activity.go) | `Activity`: the in-memory buffer of `last_activity_at` (R-REPO-012), flushed once a minute and at shutdown |
| [reader.go](../../internal/repository/reader.go) | `SnapshotReader` (the `Reader` implementation): the snapshot, the negative cache of unknown keys, the lookup of R-ARCH-011 step 2 |
| [codes.go](../../internal/repository/codes.go) | codes of other areas that the validation names as strings |
| [remote.go](../../internal/repository/remote.go) | `RemoteSettings` and its parts, `SecretField`, `SecretRef`, the strict request form (`remoteInput`, `checkRemoteShape`), `Repository.Remote()`, `upstreamIdentityChanged` |
| [validate_remote.go](../../internal/repository/validate_remote.go) | `validateRemote`: defaults from the catalog, ranges, `normalizeUpstreamURL`, the warnings, the refusal of the P1 members, the secret fields into `pendingSecret`s |
| [secrets_store.go](../../internal/repository/secrets_store.go) | `SealSecret`, `OpenSecret`, `SecretStore` (rows for the clients), `RemoteDeps` and `Service.Bind`, `writeSecrets`, `ResealSecrets` |
| [upstream.go](../../internal/repository/upstream.go) | `UpstreamProber`, `UpstreamTestResult`, `Service.UpstreamTest`, `Service.UpstreamTestUnsaved` |
| [formats/catalog/catalog.go](../../internal/formats/catalog/catalog.go) | the format catalog (a leaf): `Facts`, `Names`, `Exposure`, `Entry`, `Registry`, `Default` |
| [formats/formats.go](../../internal/formats/formats.go) | `Descriptor` (the catalog's `Facts` plus the contract's own members), `Format`, `Register` (validates and fills the catalog), `Lookup`, `Descriptors`, and the aliases of the catalog's types; the contract is described in [formats.md](formats.md) |
| [formats/generic/generic.go](../../internal/formats/generic/generic.go) | the generic format: descriptor and request classification |
| [formats/all/all.go](../../internal/formats/all/all.go) | registers every format; the composition root imports it |
| [audit/events_repository.go](../../internal/audit/events_repository.go) | `repository.created`, `.updated`, `.deleted`, `.claims_changed` |
| [app/wire_repository.go](../../internal/app/wire_repository.go) | `wireRepository` (reader and service, both on `catalog.Default()`), the job kind registration |
| [app/wire_proxy.go](../../internal/app/wire_proxy.go) | `wireProxy`: the upstream clients, and `Service.Bind` with the key ring and the prober |
| [queries/remote_settings.sql](../../internal/meta/postgres/queries/remote_settings.sql) | the statements on `repository_secrets` |

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
| `PATCH` | `Patch(ctx, caller, key, pre, apply Patcher)` | `apply` turns the stored document, as JSON, into the patched one under the row lock; the handler passes `v1.MergePatcher(patch)`, the API's one RFC 7396 merge (Q-API-157), and the result is read strictly like a PUT body; `pre` is `MatchVersion(n)` or `MatchAny()`; 428 is the handler's, because the header is |
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
removed in the first transaction. A local or remote repository that a virtual repository lists answers 409
`repository_referenced` unless `detach` is set, in which case the same transaction removes the references
([virtual.md](virtual.md) "Deleting a repository").

## Remote settings

The `remote` section of a remote repository (R-REPO-024) is validated in the same pass as the rest of the document and stored in
`repositories.config` in a canonical form; the sealed values live in `repository_secrets`.

- **Decode.** `DecodeDocument` keeps `remote` as raw JSON and checks its shape strictly (`checkRemoteShape`): an unknown or duplicate
  member, or a wrong type, is `400 malformed_request`. A merge patch reaches the same check, because the service reads the patched
  document like a PUT body.
- **Validate.** `validate` calls `validateRemote` for the class remote. It reads the section into `remoteInput` (every member a pointer,
  so an omitted member takes its default), applies the defaults of the format's catalog entry (`DefaultUpstreamURL`,
  `DefaultExtraHosts` for that upstream only, `DefaultMetadataTTLSeconds`, `DefaultNotFoundTTLSeconds`), checks every range and
  enumeration, and **replaces `d.Remote` with the canonical section**: every member present, the URL normalized, the secret fields
  reduced to `{"set": true}` or their reference. The plaintext of a request goes into `Settings.pending` and never into a JSON form
  (`SecretField.MarshalJSON` cannot write it). The warnings (`plain_http_upstream`, `source_network_single_address`) go to
  `validation.warnings` and come back on the write.
- **Store.** After the row is written, `Service.writeSecrets` runs in the same transaction: it seals a new value under the row key
  `(repository id, field)`, stores a reference, checks that a `{"set": true}` has a sealed row to keep, and deletes the rows of the
  fields the document no longer has. `update` treats a pending value or reference as a change even when the section reads the same,
  so `config_version` moves and the client cache (keyed by it) rebuilds (Q-REPO-88). `toDocument` renders the section from the typed
  settings again, because PostgreSQL's `jsonb` reorders members and a document that is sent back must be byte-equal to the stored one.
- **Read model.** `toRepository` parses the stored section into `Repository.remote`; `Repository.Remote()` returns it. A section that
  does not load disables the repository, like an undecodable `config`.
- **Seams.** `Service.Bind(RemoteDeps{Ring, Prober, OnUpstreamChanged})` is called by `wireProxy`. Without `Ring` an inline secret
  answers 503; without `Prober` the upstream test does. `OnUpstreamChanged` runs in the update's transaction when `url`, `extraHosts`,
  `authMode`, `username`, `clientCertificate`, `headers` or a secret changed (R-REPO-011), with the caller of the update. `wireProxy` binds it
  to `invalidation.Service.InvalidateTx`, which records the invalidation of scope `all` and an empty prefix, its change-log row and its audit
  event in that transaction: the cached answers of the old upstream are never in force with the new settings, and a failure of the
  invalidation rolls the update back.
- **Upstream test.** `UpstreamTest(ctx, key)` reads the saved repository from PostgreSQL and hands it to the prober;
  `UpstreamTestUnsaved(ctx, body)` runs the whole validation for `{format, remote}` and hands the result, with the inline password
  apart, to the prober. The prober is `upstream.Clients` ([proxy.md](proxy.md)).

How to add a setting to the section: add the member to `RemoteSettings` and to `remoteInput`, give it its default and its range in
`validateRemote`, add it to the OpenAPI schema `RemoteSettings` (inside the `M2 proxy` markers), and a row to the table of R-REPO-024. A
secret member also needs a field pointer, a `pendingSecret` and a row key in `secrets_store.go`.

## HTTP operations

| Operation | Handler | Declaration | Notes |
|---|---|---|---|
| `GET /api/v1/formats` | `ListFormats` | `public` | the descriptors of `formats.Descriptors()`; 401 for an anonymous caller while anonymous access is off |
| `GET /api/v1/repositories` | `ListRepositories` | `public` | filters `class`, `format`, `q`, `state`, `project`, `view`; keyset cursor over the key; a holder of `repositories:read-config` sees every repository the endpoint exposes (`edge.Access.Exposed`, R-EP-014), the others the readable ones (`edge.Access.Readable`, Q-REPO-19); both through `visibleKeys`, on the one index the handler begins with `requestHolder` (Q-API-158); `view=full` needs the capability |
| `POST /api/v1/repositories` | `CreateRepository` | `repositories:write-config` | strict body, `Idempotency-Key` through `Idempotency.Do`, `CreateTx` in its transaction; 201 with `Location`, `ETag: "1"`, the document and `warnings` |
| `GET /api/v1/repositories/{key}` | `GetRepository` | `public` | the capability holder passes the exposure ceiling of the endpoint (`visibleKeys`) and reads from PostgreSQL; the others pass the decision first (`edge.Access.Decide`; the unknown key is looked up and the index caught up when the holder begins with the key, Q-ARCH-40) and get the consumer view; hidden and missing are `authz.NotFound()` |
| `PUT`, `PATCH /api/v1/repositories/{key}` | `ReplaceRepository`, `UpdateRepository` | `repositories:write-config`, `x-trove-if-match: required` | 428 before the body is read; the domain applies the merge patch and returns `*StaleVersion`, which becomes 412 with `currentEtag` |
| `DELETE /api/v1/repositories/{key}` | `DeleteRepository` | `repositories:write-config` | optional `If-Match` and `Idempotency-Key`; 202 with the job reference or 204 |
| `POST /api/v1/repositories/{key}/upstream-test` | `TestRepositoryUpstream` | `repositories:write-config` | [upstream_api.go](../../internal/api/v1/upstream_api.go); `Service.UpstreamTest`; always 200 with a result for a remote repository, 404, 400 `not_applicable` |
| `POST /api/v1/upstream-tests` | `TestUpstream` | `repositories:write-config` | `Service.UpstreamTestUnsaved`; the body is `{format, remote}`; 422 with every violation, 400 for a misspelt member |

The representation ([repositoryRep](../../internal/api/v1/repositories_api.go)) is the `repository.Document` plus `lifecycle`,
`createdAt`, `updatedAt`, `lastActivityAt` and, on a write, `warnings`. The input drops those members before the strict decode, so a
body obtained by GET can be sent back (Q-API-34). A body error is `400 malformed_request` with the pointer of the member (Q-REPO-84).
The ETag of the full view is `"<config_version>"`, of the consumer view `"<config_version>-consumer"`. A read of one repository adds the
activity time while there is one, `"<config_version>.<microseconds>"` (`repositoryETag`), because `lastActivityAt` changes without a new
version and a strong tag follows the representation; a write answers the plain tag and accepts both (`repositoryPrecondition` strips the
suffix, `withoutActivity`) (Q-API-156).

## Formats

The format catalog is [internal/formats/catalog](../../internal/formats/catalog/catalog.go), a leaf: `formats.Register`
(called at compile time by `formats/all`) puts the facts of a format into `catalog.Default()`, and the model reads them there.
`ReaderOptions.Catalog` and `Options.Catalog` take a `*catalog.Registry`; nil is the process registry, so a test passes a
registry of its own (see `testCatalog` in [validate_test.go](../../internal/repository/validate_test.go)). There is no
mirror of the facts in this package or in the composition root. The model validates class, layout and the members that only
some formats have (`PackageNames`, `PriorityMembers`, `PinsAndClaims`, `Caps`) against the entry. The decision point reads
the same entry for the package-name rule and the exposure facts (`catalog.Names`, `catalog.Exposure`). The generic format sets none
of them, so `priorityMember`, `local.packageOwnership`, `ownershipDomain`, `autoClaim` and `local.caps` answer
`not_applicable` (R-GENERIC-008). `OwnershipDefault` is the `packageOwnership` of a format's local repositories while their
document sets none (npm and NuGet: `enforce`): `applyOwnershipDefault` in [validate.go](../../internal/repository/validate.go)
stores it on every create and update, so a replace or a merge patch that drops the member reverts it to the default and `off`
is always explicit (R-AUTHZ-018, Q-AUTHZ-155). `ownershipDomain` answers `not_applicable` for every format until shared
ownership domains (R-AUTHZ-020, P1) exist.

`formats.Format` is the full contract of R-ARCH-008: routes, `Classify` (the request descriptor of R-REPO-003, as `PathInfo`),
storage hooks, the remote and virtual adapters, the exposure adapter and the error shape; [formats.md](formats.md) describes it,
its registry and the host. A format imports nothing but `internal/formats`, `internal/formats/catalog`,
`internal/formats/untrusted`, `errs`, `config` and `observability` (archtest R-ARCH-008); `formats/all` may import every format.

### How to add a format

The ordered checklist with the contract's details is in [formats.md](formats.md#how-to-add-a-format); the repository side of it:

1. Write its spec page under `docs/spec/formats/` (prefix, Covers, Decisions, What this does not cover) and add the page
   to the table in [spec/README.md](../spec/README.md).
2. Add `internal/formats/<id>` implementing `formats.Format` with a `Descriptor` whose `Classes` list only what is
   implemented; a format with package names declares `Names` (the one normaliser: grants, token scopes, exposure rules
   and repository patterns fold names and patterns with it) and, to be exposable on an endpoint, an `Exposure`; add a
   line to the tree of R-ARCH-003.
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
`authz` imports `repository`, not the other way round; the authorization area calls `CompilePattern` rather than
keeping a second copy (R-REPO-005: "a pattern copied between them selects the same names"). `Admission` holds both lists of
a repository, compiled once when the snapshot is built; `Repository.Admits(path, folder)` is what the files transfer calls.
For a format whose patterns match names, `toRepository` folds each pattern with the format's name rule (`foldAll`) before it
compiles the lists, as a grant folds its package patterns, so a NuGet repository's `Acme.*` admits `acme.core`. The
routing of virtual repositories asks the same `Admission` ([virtual.md](virtual.md)), and `TestRoutingRunsTheMatcherTable` runs the
name-pattern table of R-AUTHZ-005 through it.

## Configuration

| Key | Default | Meaning |
|---|---|---|
| `repositories.lookup_wait` | `5s` | how long a lookup of a key that PostgreSQL knows and the snapshot does not waits for the change-log poll before it answers 503 |
| `repositories.max_virtual_members` | `100` | the most members one virtual repository lists (R-REPO-017) |
| `cache.snapshot_safety_ttl`, `cache.unknown_repo_keys` | existing | the snapshot's age bound; the size of the negative cache of unknown keys |
| `storage.delete.batch_size` | existing | items per transaction of the deletion job |

## Tests

| Tier | Where | Proves |
|---|---|---|
| unit | [internal/repository](../../internal/repository/) | path canonicalisation, keys, patterns and admission, document decoding, merge patch (RFC 7396 examples), validation (R-REPO-002, -005, -006, -008, R-GENERIC-001, -002, -007, -008); `remote_test.go`: the defaults, ranges, URL normalization, warnings, refusals and secret forms of the remote section (R-REPO-024, R-REPO-033) |
| unit | [internal/formats](../../internal/formats/) and below | registry rules; the generic descriptor and classification (R-GENERIC-001, -003) |
| integration | [test/integration/repository](../../test/integration/repository/) | create, read, update, list, delete against PostgreSQL with audit and change-log rows; the snapshot, the lookup and the poll bound on two replicas without Valkey (R-REPO-009 to -013, R-REPO-049, R-ARCH-011); `remote_test.go`: the remote settings, the sealed secrets (nothing in any table, a copied value, a rotation), the upstream test and the invalidation hook, against a TLS test upstream (R-REPO-024, R-REPO-033, R-REPO-038) |
| integration | [test/integration/api](../../test/integration/api/) (`upstream_test.go`) | both upstream-test operations over HTTP on a real database, every answer validated against the OpenAPI document |
| integration | [test/integration/files](../../test/integration/files/) (`repository_api_test.go`) | the repository API over HTTP on two replicas: the contract suite of E1.6 (`apitest.RunContract`: cursor walk, ETag with If-Match, merge patch, idempotency keys), validation, permissions, the consumer view, list visibility, the delete job on a worker, an idempotent create replayed through the other replica |

Run the integration area from the checkout root with the environment of the shared stack:
`go test -count=1 -tags integration ./test/integration/repository/...`.

## Decisions

[Q-REPO-12 to Q-REPO-23](../spec/repositories-and-proxy.md#decisions), [Q-REPO-88 to Q-REPO-91](../spec/repositories-and-proxy.md#q-repo-88--a-secret-field-reads-set-true-and-keeps-its-value-in-repository_secrets) (the remote settings and their secrets) and [Q-GENERIC-1 to Q-GENERIC-5](../spec/formats/generic.md#decisions).
