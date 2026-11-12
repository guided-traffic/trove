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
[virtual.md](virtual.md); the pull-through engine is built too ([proxy.md](proxy.md)). Which requirements of R-REPO-024 to R-REPO-049 it covers is in the [coverage checklist](../coverage/README.md).

## Files

| File | Responsibility |
|---|---|
| [repository.go](../../internal/repository/repository.go) | the shared contract: `Repository`, `Class` (the catalog's), `Lifecycle`, `Reader`, `ReservedKeys()`; `Repository.Admits` |
| [document.go](../../internal/repository/document.go) | `Document` (its read-only `project`, R-REPO-001) and `Settings` (the configuration document of R-REPO-004, with the settings member of each format that has some, `formatMembers`), `DecodeDocument` (strict: unknown and duplicate members), `ConsumerView`, `canonicalObject` (the stored form of a format member), `IsReservedMember` |
| [validate.go](../../internal/repository/validate.go) | `validate`: every rule of R-REPO-008 in one pass; the error shape (one violation under its own code, several under `validation_failed`) |
| [key.go](../../internal/repository/key.go), [path.go](../../internal/repository/path.go) | `IsReservedKey` and the key check of a create (the grammar of a key is `qname.ValidKey`, the only source; [qname](../../internal/qname/qname.go) names the pair); `CleanPath` (R-ARCH-005) |
| [pattern.go](../../internal/repository/pattern.go) | `CompilePattern`, `PatternSubject` (the catalog's), `Pattern.Match`, `Admission` (include and exclude lists, folder admission). Matching allocates nothing for the sizes in use and refuses an input without the pattern's literal lead first ([Q-EP-64](../spec/endpoints.md#q-ep-64--pattern-matching-in-the-decision-path-allocates-nothing-and-a-literal-lead-refuses-before-any-state-is-built)); [pattern_alloc_test.go](../../internal/repository/pattern_alloc_test.go) holds the former matcher as the reference and a fuzz target |
| [store.go](../../internal/repository/store.go) | rows to `Repository` and `Document`; the sqlc `queries(tx)` seam; fail-closed load of an undecodable row |
| [service.go](../../internal/repository/service.go) | `Service`: `Create`, `Get`, `Replace`, `Patch`, `List`; `Precondition`, the `Hooks` seam (`OnUpdated`, `OnDelete`, `MultiHooks`); `ChangeEviction`, the reader of a `repo_config` payload for the caches keyed by version; a stale version is `errs.Stale` |
| [narrowed.go](../../internal/repository/narrowed.go) | the writer of `repository.narrowed` (R-REPO-061): `Service.narrowing` compares the stored document and rows of an update with the new ones (`includeNarrowed` with `patternCovers`, which keeps a widening include edit out of the journal, `addedPatterns`, `lostRefs`, `pinNarrowings`, the `weakensVulnerabilities` check with its arguments swapped) and `recordNarrowed` records the event beside `repository.updated` in the update's transaction, with the entry (`narrowedEntry`) as the journal entry |
| [journal_replay.go](../../internal/repository/journal_replay.go) | the replay handler of `repository.narrowed` and `repository.claims_changed` (domain `repository`, registered in `init`): `readNarrowed` and `readClaims` (the strict readers), `replayer` (the locked repository as the restored database holds it) with one method per step of R-REPO-061 (`include`, `excludes`, `autoClaim`, `membersRemoved`, `pinMembersRemoved`, `pinsAdded`, `vulnerabilities`) and `claimReplay` for the claims; the rows are written with `writeVirtual`, the rescan with `jobs.EnqueueTx` |
| [delete.go](../../internal/repository/delete.go) | `Service.Delete`, the job kind `repository-delete` and its handler `RunDeleteJob` |
| [activity.go](../../internal/repository/activity.go) | `Activity`: the in-memory buffer of `last_activity_at` (R-REPO-012), flushed once a minute and at shutdown |
| [reader.go](../../internal/repository/reader.go) | `SnapshotReader` (the `Reader` implementation): the snapshot indexed by `qname.Repo`, the negative cache `repo_key_misses` of unknown names, the lookup of R-ARCH-011 step 2 |
| [project.go](../../internal/repository/project.go) | the project side of a create (R-AUTHZ-026, Q-REPO-212): `lockProject` (locks the project `FOR SHARE`, so a concurrent delete of the project counts the new repository; 404 for a project that does not exist), `projectKeys` |
| [codes.go](../../internal/repository/codes.go) | codes of other areas that the validation names as strings |
| [remote.go](../../internal/repository/remote.go) | `RemoteSettings` and its parts, `SecretField`, `SecretRef`, the strict request form (`remoteInput`, `checkRemoteShape`), `Repository.Remote()`, `upstreamIdentityChanged` |
| [validate_remote.go](../../internal/repository/validate_remote.go) | `validateRemote`: defaults from the catalog, ranges, `normalizeUpstreamURL`, the warnings, the refusal of the P1 members, the secret fields into `pendingSecret`s |
| [secrets_store.go](../../internal/repository/secrets_store.go) | `SealSecret`, `OpenSecret`, `SecretStore` (rows for the clients), `RemoteDeps` and `Service.Bind`, `writeSecrets`, `ResealSecrets` |
| [upstream.go](../../internal/repository/upstream.go) | `UpstreamProber`, `UpstreamTestResult`, `Service.UpstreamTest(ctx, name)`, `Service.UpstreamTestUnsaved(ctx, body)` |
| [formats/catalog/catalog.go](../../internal/formats/catalog/catalog.go) | the format catalog (a leaf): `Facts`, `Names`, `Exposure`, `Entry`, `Registry`, `Default` |
| [formats/formats.go](../../internal/formats/formats.go) | `Descriptor` (the catalog's `Facts` plus the contract's own members), `Format`, `Register` (validates and fills the catalog), `Lookup`, `Descriptors`, and the aliases of the catalog's types; the contract is described in [formats.md](formats.md) |
| [formats/generic/generic.go](../../internal/formats/generic/generic.go) | the generic format: descriptor and request classification |
| [formats/all/all.go](../../internal/formats/all/all.go) | registers every format; the composition root imports it |
| [audit/events_repository.go](../../internal/audit/events_repository.go) | `repository.created`, `.updated`, `.deleted` (details `class`, `format`, `changes`, `phase`, and `project` for a repository of a project), `.claims_changed` (journaled, with the claims created or widened to `global`), `.narrowed` (journaled) and `.disabled` |
| [app/wire_repository.go](../../internal/app/wire_repository.go) | `wireRepository` (reader and service, both on `catalog.Default()`), the job kind registration |
| [app/wire_proxy.go](../../internal/app/wire_proxy.go) | `wireProxy`: the upstream clients, and `Service.Bind` with the key ring and the prober |
| [queries/remote_settings.sql](../../internal/meta/postgres/queries/remote_settings.sql) | the statements on `repository_secrets` |

## Request path: reader

`Reader.ByName(ctx, name qname.Repo)` is what the files transfer, the format dispatcher and the grant index call per request
(R-ARCH-011 step 2). It never authorizes: callers decide first (R-AUTHZ-016) and only then look the repository up.

`Reader.ByID(ctx, id int64)` is what a running job holds its repository by (R-REPO-060): the mirror pass resolves its repository by the id in its arguments, at its start and before every package, because a move changes the name and never the id. The name it finds is for logs, audit events and metrics only. An id the snapshot lacks makes the replica poll the change log once and rebuild before it answers `repository_not_found`, so a job queued by the replica that created the repository is not lost.

1. A name whose project or key is outside its syntax (`qname.Repo.Valid`) is `repository_not_found` without a query. The same key in two projects is two repositories; `Reader.ListProject(ctx, project)` lists one project.
2. The snapshot is one cache entry (`repositories`, registered on the replica's `cache.Registry`) holding the whole table.
   Any change of the change area `repo_config` drops it, and the next call rebuilds it with the fill rule of R-ARCH-018
   (a load that overlapped a change is not stored). `cache.snapshot_safety_ttl` bounds its age.
3. A name the snapshot does not hold is looked up in PostgreSQL, concurrent lookups of one name sharing one query. If it
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
change back, R-OPS-015) and the change-log row (`Changes.Notify` on `repo_config`, payload `{id, key, op, version}` (`key` is the qualified name) with the
`config_version` the write left, never a grant; the ops that end a repository are `ChangeOpDeleting` and `ChangeOpDeleted`; a cache keyed by repository and version reads the payload with `ChangeEviction`). A handler calls, after it has authorized:

| Operation | Function | Notes |
|---|---|---|
| `POST /projects/{projectKey}/repositories` | `Create(ctx, caller, project, body)`, `CreateTx(ctx, tx, caller, project, body)` | the repository is created in `project` (404 if it does not exist); `409 already_exists` also for a key being deleted, in that project only (`UNIQUE (project_id, key)`); never an upsert; a `project` member of the body that names another project is 422 `immutable` at `/project`; `CreateTx` runs in the transaction of an idempotency key |
| `GET …/repositories/{repoKey}` | `Get(ctx, name)` | from PostgreSQL (R-API-052); the document carries `project`; `Document.WithoutAdminNotes`, `Document.ConsumerView` (with `project`) for the narrower views |
| `PATCH` | `Patch(ctx, caller, name, pre, apply Patcher)` | `apply` turns the stored document, as JSON, into the patched one under the row lock; the handler passes `v1.MergePatcher(patch)`, the API's one RFC 7396 merge (Q-API-157), and the result is read strictly like a PUT body; `pre` is `MatchVersion(n)` or `MatchAny()`; 428 is the handler's, because the header is |
| `PUT` | `Replace(ctx, caller, name, pre, body)` | omitted settings revert to defaults; an omitted `project`, `key`, `class` or `format` means "unchanged"; one that differs is 422 `immutable` |
| `GET /repositories`, `GET /projects/{projectKey}/repositories` | `List(ctx, filter)` | sorted by project key, then key, in byte order (`ListRepositories` sorts `COLLATE "C"`, because `After`, a `qname.Repo`, is compared in Go byte by byte; R-REPO-012, H-404), keyset paging (`After`, `Limit`, `HasMore`), filters of R-REPO-012 (`Project` keeps the repositories of one project; an unknown key keeps none); every reader fills `Repository.Name.Project` |
| `DELETE` | `Delete(ctx, caller, name, detach)`, `DeleteTx(ctx, tx, …, pre)` | `DeleteResult{JobID, JobState, Done}`: 202 with the job, or 204 for a repository without content; `pre` is the optional `If-Match` (412 on a stale version); `detach` edits the virtual repositories of the project, the only ones that can list the repository (R-REPO-013) |

Errors are `*errs.Error`: `malformed_request` (400, field code `invalid_document` when no pointer is known), `validation_failed` (422, the codes of R-REPO-008 in `Fields`), `already_exists`
(409), `repository_not_found` (404), `precondition_failed` (412, with `*StaleVersion` as the cause carrying the current
version for `currentEtag`). An update that changes nothing writes nothing: same version, no event, no change row.
`Hooks.OnDelete` runs in the transaction that removes the row, just before the row goes (the job's `finishDelete`, and
`deleteNow` for a virtual repository): `authz.ExposureHook` ([exposure_hook.go](../../internal/authz/exposure_hook.go))
records the exposure rules the foreign key takes with it (`endpoint.exposure.deleted`, cause `repository_deleted`, Q-EP-25);
`repository.MultiHooks` composes the hooks, and a hook error fails the deletion. `Hooks.OnUpdated` runs in the update's
transaction with the repository before and after; the authorization area uses it
to record `authz.settings.changed` for `cacheFillAction`, `packageOwnership` and `hideExistence`
(R-AUTHZ-031): `authz.SettingsHook` ([settings.go](../../internal/authz/settings.go)), which `wireRepository` sets as `Options.Hooks`.
It records only the settings that changed, before and after, with a journal entry, and nothing for another change.

### The levels of a repository

`visibility.artifacts` and `visibility.wiki` are columns of the row, members of the document (`Document.Visibility`), never part of
`repositories.config`. [visibility.go](../../internal/repository/visibility.go) decides what a write stores (`resolveLevels`): on a
create an absent artifacts level is the lower of the installation's default (`visibility.Default`, read in the transaction) and the
project's level, an absent wiki level the lower of that and the artifacts level; on an update an absent member keeps the stored level,
also in a `PUT`. The project is locked `FOR SHARE` before the repository (`lockProject` on a create, `lockForUpdate` on an update;
the lowering of a project locks it `FOR UPDATE`, so a write
sees the new level or waits). A level above the project is 422 `visibility_above_project` at `/visibility/artifacts`, a wiki above the
artifacts `visibility_wiki_above_artifacts` at `/visibility/wiki`, a level that is none `invalid_value`, `public` while
`Options.PublicAvailable` says no is 409 `public_not_allowed`. A level that rises calls `Caller.RaiseGate` (the handler's recent
authentication) before anything is stored (Q-AUTHZ-281); a remote repository whose artifacts become `public` with `cacheFillAction`
read carries the warning `public_populates_remote`. The update's `changes` list names `visibility` and `authz.SettingsHook` records the
levels with the other access settings (`visibility.artifacts`, `visibility.wiki`, cause `request`); the replay reads the same flat keys
(`replaySettings` in [journal_replay.go](../../internal/authz/journal_replay.go)) and sets the columns with `JournalRepositorySetLevels`.

### Narrowings in the revocation journal

`recordUpdate` records `repository.updated`, then `repository.narrowed` when `Service.narrowing` finds a part that narrows
(R-REPO-061, Q-REPO-245), then `repository.disabled` when the update set `disabled`: all three in the update's transaction, so a
failing audit insert rolls the update back. `update` hands it the rows of a virtual repository before and after (`curRel`,
`rel`; nil for another class). The entry carries only the parts that narrowed, by repository ID for members, so the replay
re-applies a removal to whatever the restored database holds and never the widening half of a mixed update. `Create` never
records it. `recordClaims` ([claims.go](../../internal/repository/claims.go)) writes the `repository.claims_changed` entry
(`claimsEntry`) on every claims change, the auto claim of a first publish ([routing.go](../../internal/repository/routing.go))
included; `diffClaims` returns the created and widened claims separately (`journaled`) from the `added` list of the event's
details, which keeps the narrowings to `members` too.

The replay ([journal_replay.go](../../internal/repository/journal_replay.go)) locks the repository (`JournalRepositoryLock`),
reads its stored document as a map and as `Settings`, applies the parts in the order of R-REPO-061, writes the document with
`JournalRepositorySetConfig` (which raises `config_version`) and tells `repo_config` and `authz`, and `claims` when pin rules
or claims changed. A part that fails closed calls `replayer.suspend`: it sets `disabled`, and a repository that is disabled
already stays as it is and reports nothing, which is what makes a second replay `already in effect`. The auto-claim cover rule
is `ownClaimCovers` and `otherClaimCovers` in routing.go, shared with `Routing.AutoClaim`. The statements are in
[journal_repository.sql](../../internal/meta/postgres/queries/journal_repository.sql). The restore commands build no job registry,
so the rescan of a strengthened `vulnerabilities` member is written by `jobs.EnqueueTx` with `vuln.RescanKindSpec()`, the spec
that `internal/app/wire_vuln_eval.go` registers the kind with (Q-HA-180); the workers that start after the restore find the row
by polling `jobs.poll_interval`.

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
([virtual.md](virtual.md) "Deleting a repository"). Only virtual repositories of the repository's own project can list it (the
triggers AI006 guarantee it), so `detachReferences` edits the virtual repositories of that project and of no other. The events of a repository
carry `project` in their details and the qualified name as their subject; the delete paths resolve the project from the row (`projectKeyOf`).

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
- **Upstream test.** `UpstreamTest(ctx, name)` reads the saved repository from PostgreSQL and hands it to the prober;
  `UpstreamTestUnsaved(ctx, body)` runs the whole validation for `{format, remote}` and hands the result, with the inline password
  apart, to the prober. The prober is `upstream.Clients` ([proxy.md](proxy.md)).
- **An Owner's section.** The rules for a caller without `repositories:write-config` (R-REPO-033, Q-REPO-127, Q-REPO-128) are the API
  layer's decision; the codes `secret_value_required` and `outbound_path_restricted` are field errors and also the top-level code
  (`isTopLevelCode` in validate.go).

How to add a setting to the section: add the member to `RemoteSettings` and to `remoteInput`, give it its default and its range in
`validateRemote`, add it to the OpenAPI schema `RemoteSettings` (inside the `M2 proxy` markers), and a row to the table of R-REPO-024. A
secret member also needs a field pointer, a `pendingSecret` and a row key in `secrets_store.go`.

## HTTP operations

The handlers are the API layer's ([http-api.md](http-api.md)); the paths below are the repository model's ([R-REPO-002](../spec/repositories-and-proxy.md)):
a repository is addressed inside its project.

| Operation | Notes |
|---|---|
| `GET /api/v1/repositories`, `GET /api/v1/projects/{projectKey}/repositories` | `ListFilter.Project`; keyset cursor over (project key, key); the instance-wide list carries the project of each item |
| `POST /api/v1/projects/{projectKey}/repositories` | `CreateTx` in the transaction of the `Idempotency-Key`; 201 with `Location`, `ETag: "1"`, the document (with `project`) and `warnings`; 409 `already_exists` for a taken key of that project |
| `GET`, `PUT`, `PATCH`, `DELETE …/repositories/{repoKey}` | `Get`, `Replace`, `Patch`, `Delete` with a `qname.Repo`; hidden and missing are the same 404 |
| `POST …/repositories/{repoKey}/upstream-test`, `POST …/upstream-tests` | `UpstreamTest(ctx, name)`, `UpstreamTestUnsaved(ctx, body)` |
| `GET`, `POST …/repositories/{repoKey}/mirror[/run]` | [mirror.md](mirror.md) |

The `project` member of the document is read-only: a create takes the project of the address, and a body that names another project is
422 `immutable` at `/project`; a document that was read can be sent back. The ETag of the full view is `"<config_version>"`, of the
consumer view `"<config_version>-consumer"`.

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
4. If it has settings, give the format a JSON Schema (`catalog.Facts.SettingsSchema`) and add them to `repository.Settings` as a member named after the format (a strict
   document refuses a member the struct does not know) with an entry in `formatMembers`; `validateFormatSettings` runs the schema, so `validate.go` needs no change
   ([formats.md](formats.md#format-settings), Q-REPO-122). A setting that applies to one class says so in the schema (`x-trove-classes`).
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
| unit | [internal/repository](../../internal/repository/) | path canonicalisation, keys, patterns and admission, document decoding, merge patch (RFC 7396 examples), validation (R-REPO-002, -005, -006, -008, R-GENERIC-001, -002, -007, -008); `remote_test.go`: the defaults, ranges, URL normalization, warnings, refusals and secret forms of the remote section (R-REPO-024, R-REPO-033); |
| unit | [internal/formats](../../internal/formats/) and below | registry rules; the generic descriptor and classification (R-GENERIC-001, -003) |
| integration | [test/integration/repository](../../test/integration/repository/) | create, read, update, list, delete against PostgreSQL with audit and change-log rows; the snapshot, the lookup and the poll bound on two replicas without Valkey (R-REPO-009 to -013, R-REPO-049, R-ARCH-011); `remote_test.go`: the remote settings, the sealed secrets (nothing in any table, a copied value, a rotation), the upstream test and the invalidation hook, against a TLS test upstream (R-REPO-024, R-REPO-033, R-REPO-038); `list_order_test.go`: pages of hyphenated keys in byte order under the stack's `en_US.utf8` database (`TestR_REPO_012_ListPagesEveryKeyOnceInByteOrder`); `project_events_test.go`: `project` in the `repository.*` events; `project_rules_test.go`: the same key in two projects, a virtual repository that lists only its own project's repositories, the AI006 backstop |
| integration | [test/integration/repository/journal_narrowed_test.go](../../test/integration/repository/journal_narrowed_test.go) | the writer of R-REPO-061 through `Service`: which update records `repository.narrowed` with which parts, that a description, a widening, a refused and a failed update record none, and the entry of every claims change |
| integration | [test/integration/backup/journal_repository_narrowing_test.go](../../test/integration/backup/journal_repository_narrowing_test.go) | the replay of both events into the copy of a database made at *t*, one test per step of R-REPO-061 with a restored state that differs, the fail-closed cases, the rescan row and a worker that takes it by polling, and the second replay applying nothing |
| integration | [test/integration/api](../../test/integration/api/) (`upstream_test.go`) | both upstream-test operations over HTTP on a real database, every answer validated against the OpenAPI document |
| integration | [test/integration/files](../../test/integration/files/) (`repository_api_test.go`) | the repository API over HTTP on two replicas: the contract suite of E1.6 (`apitest.RunContract`: cursor walk, ETag with If-Match, merge patch, idempotency keys), validation, permissions, the consumer view, list visibility, the delete job on a worker, an idempotent create replayed through the other replica |

Run the integration area from the checkout root with the environment of the shared stack:
`go test -count=1 -tags integration ./test/integration/repository/...`.

## Decisions

[Q-REPO-12 to Q-REPO-23](../spec/repositories-and-proxy.md#decisions), [Q-REPO-88 to Q-REPO-91](../spec/repositories-and-proxy.md#q-repo-88--a-secret-field-reads-set-true-and-keeps-its-value-in-repository_secrets) (the remote settings and their secrets) and [Q-GENERIC-1 to Q-GENERIC-5](../spec/formats/generic.md#decisions).
