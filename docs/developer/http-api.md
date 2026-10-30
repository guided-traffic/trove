# Management API: OpenAPI workflow

The contract is [api/openapi.yaml](../../api/openapi.yaml) (OpenAPI 3.1, R-API-002). Handlers live in
[internal/api/v1](../../internal/api/v1); the generated server interface and models in
[internal/api/v1/gen](../../internal/api/v1/gen).

## Workflow

1. Edit `api/openapi.yaml`. Every operation needs `operationId` (lowerCamelCase verb + noun), `tags`,
   `x-trove-permission` in a form the authorization step enforces (see below), and at least one problem
   response (`$ref` to the shared responses).
2. `go generate ./internal/api/...` (or `make generate`) does two things: it copies the document to
   `internal/api/v1/openapi.yaml`, which the binary embeds (`go:embed` cannot reach `../`), and it runs
   `go tool oapi-codegen -config cfg.yaml` (pinned by the `tool` directive of `go.mod`, models and
   `std-http-server` only) into `gen/api.gen.go`.
3. Implement the new method of `gen.ServerInterface` in `handlers.go`. An operation without a handler does
   not compile.
4. Run the tests. `TestDocument` fails on a stale embedded copy, an operation without a declaration, and
   a route that is registered but not documented (or the reverse); `TestAuthorizationStep` sends an anonymous
   request to every operation of the document and fails unless each non-public one answers 401.

`go generate` output is committed; `make generate-check` fails on a diff.

## Conventions

| Topic | Rule |
|---|---|
| Paths | Full path in the document (`/api/v1/system/ping`), so the generated ServeMux pattern is `GET /api/v1/system/ping` |
| Errors | `httpserver.Problem(w, r, err)` with an `*errs.Error`; never write an error body by hand. The wire members are those of `errs.Problem`: `type`, `title`, `status`, `detail`, `instance`, `code`, `requestId`, `errors` |
| JSON | `writeJSON` in `handlers.go`: `application/json; charset=utf-8`, UTC times with `Z`, empty collections as `[]`, "no value" as `null` |
| Caller | `httpserver.PrincipalFrom(ctx)`: kind, ID, name, groups and the credential that proved it (method, token ID and type, scope ceiling, endpoint binding); never read credentials from headers. Ask authorization with `edge.SubjectFrom(r)` (`v1.SubjectFrom(ctx)` delegates): it carries the principal, the epoch and the listener's endpoint, see [m1-contracts.md](m1-contracts.md) |
| Permission check | done before the handler by the authorization step of [authorize.go](../../internal/api/v1/authorize.go), from the operation's declaration; a handler adds a check only where the decision depends on the request body (R-API-008) |
| Authorization index | one per request (R-AUTHZ-015, Q-API-158): for a capability, a role or an action declaration the step calls `Beginner.Begin` ([access.go](../../internal/api/v1/access.go)) before it checks the declaration and stores the `edge.Access` holder in the request's context; the handler asks every further question through `edge.AccessFrom(ctx)` or `requestHolder`. See "One index per request" below |
| Base URL | `httpserver.InfoFrom(ctx).BaseURL`, never `Host` |
| Authorization table | `Document.Operations()` maps the route pattern to the declarations, each decoded into an `Operation.Requirement` at load |
| Registration | `Server.Register(router)` registers through `httpserver.Router` behind `authorizingMux`, which wraps every handler in the check of the operation its pattern names; the composition root then calls `Document.CheckRoutes(router.Patterns())` |

`x-trove-permission` forms the step enforces until the authz area extends it (R-API-008), and nothing else (`LoadDocument` refuses any other form,
so start-up stops before an operation can run with a declaration nobody checks):

| Declaration | Check | M0 operations |
|---|---|---|
| `{public: true}` | none | ping, the document, the problem catalog |
| `{authenticated: true}` | anonymous → 401 with `WWW-Authenticate: Bearer` | — |
| `{authenticated: true, publicSwitch: system.public_version}` | as above, unless the switch is on (`Options.PublicVersion`); the switch names are fixed in `publicSwitches()` | version |
| `{permission: <capability>}` | `Options.Access.Require(ctx, name)`: 401 for an anonymous caller who needs credentials, 403 without the capability; with `Access` nil: 401 anonymous, 403 otherwise; the name must be a capability of R-AUTHZ-010 | info, replicas (`settings:read`, `system.read` in M0) |
| `{role: <role>}` | `Access.Require(ctx, "role:<role>")`: the caller holds the system role | — |
| `{action: <action>, repository: query.<n> \| path.<n>[, package: …][, path: …]}` | `ActionAccess.RequireAction` decides the action on the coordinate built from the request (R-AUTHZ-015) | — |

A route whose pattern has no operation in the document is denied to everyone with 403 (and `CheckRoutes`
stops start-up for it). The binding of path parameters runs after the check. R-API-008 defines the full
grammar; every form that `parseRequirement` accepts is enforced by `decide` in `authorize.go`.

### One index per request

A request decides on one authorization index (R-AUTHZ-015, Q-AUTHZ-159, Q-API-158). Where the index is taken depends on
the declaration:

| Declaration | Index |
|---|---|
| `permission`, `role` | the step begins (`Server.begin`) before `Access.Require`; `Beginner.Begin(ctx, permission, keys...)` returns the `edge.Access` and the step stores it with `edge.WithAccess` in the request it passes on |
| `action` | the same, with the repository of the request as the key, so a repository created on another replica is known to the index first (Q-AUTHZ-133) |
| `public`, `authenticated` | none: an operation that decides nothing must not start to answer 503 while the index is unconfirmed |

`v1.Access` stays the interface the step and the stand-ins of the tests use; `NewAccess(authz.Decider)` returns the one over
the decision point, whose `Require`, `Holds` and `RequireAction` resolve the holder from the context (`edge.Resolve`) and begin a
second index only for a request that reached them without the step. A handler that decides (the repository read and list,
check-exposure, the permission checks) calls `edge.Resolve` or `requestHolder(ctx, decider, keys...)`, and passes the context
that returns to everything after, so the `s.holds` questions it asks through `Access` use the same holder. `DeciderOf` takes
the per-request form out of a service typed as `authz.Authorizer`; a value without it answers 503, it never decides on an
index of its own.

Two questions stay outside it: the rate limit's administrator exemption runs in a chain stage before the step and asks the
decision point on its own index, and `GET /api/v1/authz/me` calls `Engine.Effective`, which has no `…On` form yet.

## The `/files/` family in the document

The raw file path is part of the document (R-API-002) but not of the generated interface: `exclude-tags: [files]` in
[gen/cfg.yaml](../../internal/api/v1/gen/cfg.yaml). `LoadDocument` keeps those operations apart: `Document.Operations()` holds the
management API (`/api/v1/`), `Document.FileOperations()` the `/files/` ones, and `CheckRoutes` compares the registered patterns
with both (the multi-segment wildcard `{path...}` of the router is the parameter `{path}` of the document). The step of
[authorize.go](../../internal/api/v1/authorize.go) does not run for them: the files service decides every request itself, because it decides per
coordinate and after the body is read (Q-API-33). `apitest.Validator` looks the route of a `/files/` answer up with a single
segment path, so the answers of the files handler are validated like every other, and it keeps an encoded `/` inside its
segment (a package name such as `@acme%2Ffrontend`, R-AUTHZ-019) the way the router matches it, escaped segment by segment
(`TestR_AUTHZ_019_AnEncodedSlashStaysInItsSegment`). The bridge between the two packages in the
unit tests: `TestRoutesAgreeWithTheDocument` in [internal/api/rawfiles](../../internal/api/rawfiles/rawfiles_test.go).

## Endpoints

The API is registered on every endpoint whose surface holds `api` (R-EP-005): the default endpoint always, a further
endpoint only by configuration (`surface: [packages, api]`). On any other endpoint `/api/v1/…` falls through to the
catch-all `404` problem, so `GET /api/v1/system/ping` on a package-only endpoint answers `404`, not `403`. A request on
an endpoint whose host check fails answers `421` `misdirected_request` before routing, whatever the path
([endpoints.md](endpoints.md)). The problem catalog lists the code (`GET /api/v1/problems/misdirected_request`).
`Document.CheckRoutes` runs for each endpoint that serves `api`.

## Operations

| Operation | Path | Access (`x-trove-permission`) |
|---|---|---|
| `getPing` | `GET /api/v1/system/ping` | public; `200 OK` while the replica serves (ready, or draining after it was ready, Q-OPS-30), `503 UNAVAILABLE` otherwise |
| `getVersion` | `GET /api/v1/system/version` | authenticated, or anonymous with `system.public_version` |
| `getSystemInfo` | `GET /api/v1/system/info` | `settings:read` |
| `listReplicas` | `GET /api/v1/system/replicas` | `settings:read` |
| `getOpenApiJson` / `getOpenApiYaml` | `GET /api/v1/openapi.json`, `.yaml` | public; `servers[0].url` is the public base URL |
| `getProblemType` | `GET /api/v1/problems/{code}` | public; resolves the `type` URI of a problem |
| `listJobs` | `GET /api/v1/system/jobs` | authenticated; `settings:read` sees every job, any other caller the jobs it started (R-API-015) |
| `getJob` | `GET /api/v1/system/jobs/{id}` | authenticated; as above, a job the caller may not see answers `404`; `Retry-After: 2` while it has not ended |
| `cancelJob` | `POST /api/v1/system/jobs/{id}/cancel` | authenticated; `settings:write` or the caller who started the job (`403` for a reader who did not), `409` for a finished job |
| `listTasks` | `GET /api/v1/system/tasks` | `settings:read` |
| `runTask` | `POST /api/v1/system/tasks/{name}/run` | `settings:write`; optional `Idempotency-Key`; `202` with the job, `409` with the running job |
| `getEncryptionStatus` | `GET /api/v1/system/encryption` | `settings:read` |
| `listFormats` | `GET /api/v1/formats` | public; 401 for an anonymous caller while anonymous access is off ([repositories.md](repositories.md#http-operations)) |
| `listRepositories`, `getRepository` | `GET /api/v1/repositories`, `/{key}` | public; the handler decides what the caller sees (R-AUTHZ-016) |
| `listPackages`, `lookupPackage`, `getPackage`, `listPackageVersions`, `getPackageVersion`, `getPackageVersionReadme` | `GET /api/v1/packages`, `/lookup`, `/{packageId}`, `/{packageId}/versions`, `/{packageId}/versions/{version}`, `…/readme` | public; the read model filters by what the caller may read (Q-API-162, Q-API-165); [packages.md](packages.md) |
| `getClientSetup` | `GET /api/v1/repositories/{repoKey}/client-setup` | `{action: read, repository: path.repoKey}`; the publish snippets also need `deploy` on the target ([packages.md](packages.md#client-setup)) |
| `createRepository`, `replaceRepository`, `updateRepository`, `deleteRepository` | `POST`, `PUT`, `PATCH`, `DELETE` on `/api/v1/repositories` and `/{key}` | `repositories:write-config` |
| `getRepositoryClaims`, `putRepositoryClaims` | `/api/v1/repositories/{repoKey}/claims` | `{action: read\|manage, repository: path.repoKey}`; a global claim also needs `repositories:write-config`, asked in the handler ([virtual.md](virtual.md)) |
| `getItem`, `deleteItem`, `listChildren`, `getItemStats` | `/api/v1/repositories/{repoKey}/items`, `/children`, `/stats` | `{action: read\|delete, repository: path.repoKey, path: query.path}` ([files.md](files.md)) |
| `downloadFile`, `headFile`, `deployFile`, `deleteFile` | `GET`, `HEAD`, `PUT`, `DELETE /files/{repoKey}/{path}` | `{action: read\|deploy\|delete, …}`, in the document but decided by the files handler |

`bridgeAccess` ([access.go](../../internal/app/access.go)) puts the dev-profile switch in front of the authorization
engine ([authorization.md](authorization.md)): `system.bootstrap_anonymous_read` opens `settings:read` for `GET /api/v1/system/info` and `GET /api/v1/system/replicas` (Q-AUTHZ-71) to anonymous callers on the **default endpoint only**, and only
while that endpoint admits anonymous requests; a further endpoint that serves `api` answers 401 (Q-OPS-36). The route
check at start compares the document with the registered and the dropped patterns of each endpoint that serves `api`.

## Packages and client setup operations

The contract of R-API-029 and R-API-030 is in the document's block `M2 packages` (tag `packages`, the `PackageSummary`, `PackageList`,
`PackageVersionSummary`, `PackageVersionList`, `PackageVersion`, `PackageFile`, `PackageReadme`, `ClientSetup` and `ClientSnippet` schemas, the parameters
`PackageId` and `PackageVersionPath`). The admin UI is generated from it. The decisions are Q-API-159 to Q-API-168. The implementation, the read model
behind it, the README renderer and the setup snippets are described in [packages.md](packages.md); this page keeps the conventions the handlers rely on.

[packages_api.go](../../internal/api/v1/packages_api.go) implements the seven operations on `Options.Packages` (`PackageServices`, filled by
[wire_packages.go](../../internal/app/wire_packages.go)). A replica without the read model (the shell profile, no database, no decision point) answers
them `503 unavailable`: never an empty list, because an empty list says "no package". Every operation is declared `public` (Q-API-162) and
decides in the handler; the client setup declares `{action: read, repository: path.repoKey}`.

- the lists use `ParseList`: the sorts and filters of the document, a cursor bound to the filter fingerprint, an unknown parameter or sort answering
  `400`; the cursor values are the sort values of the last item and the tie-breakers (name and format; version);
- the summary, the details and the README answer a strong `ETag` through `NotModified`; the summary and the details hash the bytes they send, the README
  hashes its inputs so that a request that holds the tag renders nothing;
- `GET /api/v1/formats` carries the `baseUrl` member (Q-UI-75), filled from the request's resolved information only when its source is the configuration.

## Conventions every resource inherits (WP1.6)

The helpers live in [internal/api/v1](../../internal/api/v1). A handler calls them; it never re-implements a rule.

| Rule | Helper | File | Notes |
|---|---|---|---|
| Strict JSON body: content type (415), size (413), unknown or duplicate member, wrong type and a nesting deeper than 64 levels (400 `malformed_request` with `errors[0].pointer`, R-API-003, R-API-017, Q-API-75) | `ReadBody`, `DecodeJSON`, `StrictUnmarshal` | [decode.go](../../internal/api/v1/decode.go) | The request type is the response type, so a body obtained by GET can be sent back and read-only members are accepted and ignored. Member names compare exactly. |
| Cursor pagination, filters, sort (R-API-010, R-API-011) | `ParseList`, `ListSpec`, `ListQuery`, `NewPage`, `MapPage`, `WritePage` | [paging.go](../../internal/api/v1/paging.go) | `limit` default `api.page_size_default`, clamped to `api.page_size_max` (Q-API-16). An unknown parameter or sort field is 400; a cursor for other filters or another sort is 400 `invalid_cursor`. |
| `ETag` from a revision column, `If-Match`, `If-None-Match` (R-API-012) | `ETag`, `SetETag`, `ParseIfMatch`, `expectedRevision`, `Precondition.Revision/Matches`, `Failed`, `NotModified`; `errs.Stale`, `errs.AnyRevision` | [etag.go](../../internal/api/v1/etag.go), [revision.go](../../internal/errs/revision.go) | 428 without `If-Match` on PUT/PATCH (the binder, below); 412 with `currentEtag`, from the one `errs.Stale`. |
| JSON merge patch (R-API-013) | `ReadMergePatch`, `MergePatch`, `MergePatcher`, `ApplyMergePatch`, `CheckImmutable` | [patch.go](../../internal/api/v1/patch.go) | RFC 7396, the one implementation (Q-API-157): numbers keep their literal (an integer beyond 2^53 survives), a duplicate member and a nesting beyond 64 levels are 400, `ReadMergePatch` refuses a body that is not an object (`null` included). An immutable member that differs is 422 `validation_failed` with its pointer, compared by literal. An area that stores the resource takes the merge as a function: `MergePatcher(patch)` is the `repository.Patcher`. |
| Idempotency keys, stored in PostgreSQL (R-API-014, R-API-051) | `Idempotency.Do`, `IdempotentOp`, `Outcome` | [idempotency.go](../../internal/api/v1/idempotency.go) | The row is inserted in the transaction of the change. |
| Item paths and reserved names (R-API-004) | `CheckItemPath`, `CheckReservedName` | [path.go](../../internal/api/v1/path.go) | The rules of `repository.CleanPath` on the decoded path: no backslash, at most 4096 bytes and 255 per segment, the length checked first. `invalid_path` is registered by the files area. |
| Rate limits, CORS, response headers (R-API-016, R-API-017, R-API-018) | `NewRateLimit`, `CORS`, `EdgeStage` | [ratelimit.go](../../internal/api/v1/ratelimit.go), [cors.go](../../internal/api/v1/cors.go) | The chain's stage 7; wired by [wire_api.go](../../internal/app/wire_api.go). |
| A value set in YAML cannot be changed through the API | `errs.CodeManagedByConfig` (409 `managed_by_config`) | [codes.go](../../internal/errs/codes.go) | R-API-037. M1 has no settings resource of its own; the owner of a settings document uses the code (Q-API-19). |

Problem extensions: `errs.WithExtension(name, value)` adds a member (`currentEtag`, `job`, `tokenId`); `errs.WithBadRequest()`
keeps a 400 that lists fields; `errs.Unprocessable` (422 without fields) and `errs.PreconditionRequired` (428) are kinds.

### One mechanism for `If-Match` (Q-API-28, Q-API-42)

- A `PUT` or `PATCH` that needs the header declares `x-trove-if-match: required` and references the shared parameter
  `#/components/parameters/IfMatch` (required) and no other `If-Match`; a `DELETE` references `IfMatchOptional`. The generated
  binder refuses a request without a required header before the handler, `bindError` turns that into the 428 of R-API-012,
  and a handler never checks the header again: it reads `expectedRevision(pre)` (the revision, `errs.AnyRevision` for `*`,
  0 for a tag the server never issued) and passes it to the write.
- The write is conditional on that revision in the same statement or under the row lock, and a mismatch is `errs.Stale(what,
  current)`: 412 with `currentEtag` and the cause `*errs.StaleRevision`. The domain packages return it themselves
  (repository, authz policies and rules, the principal manager, the authentication settings); `Failed` is the same error for
  a handler that compares in memory first. A merge patch computes on the revision it read and writes conditional on that
  one, also for `*` (policies, exposure rules).
- [ifmatch_test.go](../../internal/api/v1/ifmatch_test.go) reads the document: every `PUT` and `PATCH` requires the header with
  the shared parameter or is on the short exemption list (membership deltas, role bindings, file content), and every required
  operation answers 428 without the header.

### Recent authentication (R-API-007, Q-API-40)

`x-trove-recent-auth: true` on an operation is read into `Operation.RecentAuth` when the document loads (any other value stops
start-up). `Server.authorize` ([authorize.go](../../internal/api/v1/authorize.go)) calls `requireRecentAuth` once `decide`
allowed the caller, so a caller who may not act gets 403 and not an invitation to sign in again. It looks the session up
(`SessionService.Validate`), compares `authenticated_at` with `auth.sessions.recent_auth_window` and answers 401
`reauthentication_required`; a token is not asked; a replica without a session store answers 503. No handler checks it. The
operations that declare it are listed in `recentAuthOperations` in
[recent_auth_test.go](../../internal/api/v1/recent_auth_test.go); adding a declaration without extending that list fails the test.

### Authorization epoch of a write

`authz.HeaderEpoch` is the header name. A plain answer sets it with `setEpoch`; an idempotent create gives its outcome
`Finish: finishEpoch(&epoch)` ([authz_api.go](../../internal/api/v1/authz_api.go)), so the stored outcome and every replay carry
the epoch of the first answer (Q-API-44).

### Idempotency in detail

`Idempotency.Do(w, r, body, IdempotentOp{Run: ...})` is the whole interface. Without the header it runs `Run` in a
transaction of its own. With it:

1. One transaction: `SET LOCAL lock_timeout` (1 s, for the claim only), `INSERT ... ON CONFLICT (principal_id, idem_key)`; a row past
   `expires_at` is recycled. The principal must be authenticated and have a UUID.
2. Row inserted: `Run(ctx, tx)` does the change and returns an `Outcome`; the status, headers and body are stored in the
   same transaction, and the body is read back in the database's rendering so the first answer and every replay are
   byte-identical (Q-API-17).
3. The claim runs under a short `lock_timeout` that is restored to the session value right after the insert. The operation runs
   behind a savepoint: a refusal (below 500) rolls back to it, drops the hooks the operation registered (`meta.TxState.Mark` and
   `Discard`), stores the 4xx outcome on the claimed row and commits, so a duplicate that waited finds the stored answer
   (Q-API-137).
4. Row exists: a different method, route or body is 422 `idempotency_key_reused`; the stored outcome is replayed with
   `Idempotency-Replayed: true`.
5. A duplicate that arrives while the first transaction is open blocks on the row for the lock timeout, then answers 409
   `idempotency_in_progress` with `Retry-After: 1`.
6. `Run` returning an `*errs.Error` below 500 is stored as above (replayed like any outcome); any other error rolls everything
   back and stores nothing.
7. A response with a secret sets `Outcome.SecretID`: the body is sent to this client and never stored; a replay calls
   `IdempotentOp.SecretReplay` (the identity area answers 409 `token_already_issued` with `tokenId`).
8. A value that exists only after the commit hooks ran, such as the epoch of a write (`Trove-Authz-Epoch`, raised by the change
   log just before `COMMIT`), is set by `Outcome.Finish`. `Do` registers it as a `Before` hook after the ones `Run` registered, so
   it sees their results, and the stored outcome, and with it every replay, carries the value.

The task `idempotency-cleanup` ([cleanup.go](../../internal/api/v1/cleanup.go), hourly `13 * * * *`) deletes expired rows.

## How to add a management resource

The worked example is the widget resource of the contract tests,
[test/integration/api/widgets_test.go](../../test/integration/api/widgets_test.go): a keyed collection with list, get,
create, update (merge patch and PUT) and delete. Follow its shape; the widgets are served by a hand-written router, so
they call `pre.Require()` themselves, where a resource behind the generated wrapper relies on the binder (below). The steps:

1. **Table.** Migration with `revision bigint NOT NULL DEFAULT 1`, `created_at`, `updated_at`; a composite index
   `(sort field, id)` for every sortable field (R-API-010). Register the revision column with
   `meta.RegisterRevisionColumn` and the table in `internal/backup/tables` ([database.md](database.md)).
2. **Queries** in `internal/meta/postgres/queries/<area>.sql`: a keyset list query, `Get`, `Insert`, `Update`, `Delete`
   (below). Identifiers and times travel as text and are cast in SQL; a filter that is not sent is an empty array, never a
   nil slice (a nil slice is SQL `NULL` and matches nothing).
3. **Document.** Add the paths and schemas in your `>>> M1 <area>` markers of [api/openapi.yaml](../../api/openapi.yaml):
   - every operation: `operationId`, `tags`, `x-trove-permission`, the success response with an example, and the problem
     responses it can produce: `$ref: "#/components/responses/…"` for `BadRequest`, `Unauthenticated`, `Forbidden`,
     `NotFound`, `Conflict`, `PreconditionFailed`, `PreconditionRequired`, `Unprocessable`, `PayloadTooLarge`,
     `UnsupportedMediaType`, `Internal`, `Unavailable`, and **`TooManyRequests` on every operation** (the rate-limit stage can
     answer 429 anywhere; `TestEveryOperationCanBeRateLimited` fails otherwise);
   - lists: parameters `$ref: "#/components/parameters/Limit"` and `Cursor`, the filters and `sort`, response header `Link`,
     and a list schema `{items: [...], nextCursor: [string, "null"]}` (copy `JobList`);
   - `PUT`/`PATCH`: `x-trove-if-match: required`, the shared parameter `IfMatch` (no inline `If-Match`), responses 412 and 428 (a
     missing header is answered 428 by `bindError`, because the generated wrapper rejects a missing required header before the
     handler runs; `TestR_API_012_OneMechanismForAMissingIfMatch` checks the declaration); `DELETE` references `IfMatchOptional`;
     the `GET` and the write answer the `ETag` header (`#/components/headers/ETag`); `PATCH` takes `application/merge-patch+json`;
   - an operation that mints a credential or widens permissions adds `x-trove-recent-auth: true` (the shared step enforces it);
   - creating `POST` and `DELETE` with 202: `x-trove-idempotency-key: optional`, parameter `IdempotencyKey`, response header
     `Idempotency-Replayed`, and `Location` on 201/202;
   - a collection whose key sits next to a literal route (`/users/me`): list the literals as `x-trove-reserved-names` in the
     key schema; `TestOpenAPIDocument` derives them from the paths and fails on a missing one.
   Run `make generate`.
4. **Handlers** in `internal/api/v1/<resource>_api.go` (they implement the generated method; the authorization step has run):

   ```go
   // list
   lq, err := ParseList(r, ListSpec{Filters: []string{"color"}, Sorts: []string{"name", "createdAt"}, DefaultSort: "name"}, s.o.API.Config)
   rows, err := store.List(ctx, lq.Filter, lq.After, lq.FetchLimit())      // one keyset query, in the sort order, id last
   page := NewPage(lq, rows, func(r Row) []string { return []string{r.Name, r.ID} })   // the key the store compares
   WritePage(w, r, MapPage(page, toRepresentation))

   // get
   tag := ETag(rep.Revision, "")                  // "<revision>-<view>" where the representation depends on the caller
   if NotModified(w, r, tag) { return }
   SetETag(w, rep.Revision, ""); writeJSON(w, r, 200, rep)

   // create: strict body, validation, reserved name, then the change inside the idempotent operation
   body, err := ReadBody(w, r, MediaJSON, limit); err = StrictUnmarshal(body, &in)
   err = CheckReservedName("/key", in.Key, reserved...)
   err = s.idem.Do(w, r, body, IdempotentOp{Run: func(ctx context.Context, tx meta.Tx) (Outcome, error) {
       ...insert, record the audit event with RecordTx...
       return Outcome{Status: 201, Body: rep, Header: map[string]string{"Location": base + path, "ETag": ETag(rep.Revision, "")}}, nil
   }})

   // update (PATCH): 428 first, then the patch on the current representation, immutable members, one UPDATE with the revision
   pre, err := ParseIfMatch(r)                      // the header is there: the binder answered 428 otherwise
   raw, err := ReadMergePatch(w, r, limit)
   next, err := ApplyMergePatch(current, raw); err = CheckImmutable(current, next, "key", "format")
   expected := expectedRevision(pre)               // errs.AnyRevision for If-Match: *
   // UPDATE t SET ..., revision = revision + 1 WHERE key = $1 AND ($2::bigint < 0 OR revision = $2) RETURNING ...
   // zero rows: the resource is gone (404) or changed: return errs.Stale("The widget", currentRevision)

   // delete: honours If-Match when sent (expectedRevision), 204 or 202 with a job
   ```
   A change that also writes an audit event, enqueues a job or notifies a change area does so in the same transaction
   (`RecordTx`, `Queue.Enqueue(ctx, tx, …)`, `cluster.Notify`).
5. **Read from PostgreSQL.** A management `GET` never reads an in-process cache (R-API-052); the answer shows a write that
   committed on another replica at once. No state between requests lives in a process (R-ARCH-032): the cursor holds the
   position, the idempotency row the outcome, the revision column the version.
6. **Tests.** Fill an `apitest.Resource` and call `apitest.RunContract` with a `Do` that spreads requests over replicas: it
   checks pagination (`limit=1` walk, `invalid_cursor`), ETag with `If-Match`, merge patch and idempotency keys on your
   resource (E1.6). Wrap the handler in `apitest.Validator.Handler` so every answer is validated against the document, and
   assert `Uncovered(...)` is empty for your operations.

## Contract tests and response validation

[apitest](../../internal/api/v1/apitest) is test support (no production package imports it):

| Function | Purpose |
|---|---|
| `LoadDocument(spec)` | Parses and validates the document as OpenAPI 3.1 (kin-openapi supports the 3.1 constructs the document uses: type arrays with `null`, `oneOf` with a null branch). |
| `New(t, spec)`, `Validator.Handler(t, next)` | Wraps a handler; every answer is matched to its operation by method and path, and status (an undeclared status fails), headers and body are validated against the response definition. |
| `Validator.Check`, `Recorder` | Validate one answer. |
| `Validator.Uncovered(ids...)` | The operations no answer reached; a test that owns operations asserts it is empty. |
| `Resource.RevisionMember` | The member that grows with every write (default `revision`); the suite does not count it as a change when it checks that a PUT or a patch left the other members alone. |
| `ReservedNameProblems(spec)` | The R-API-004 check on `x-trove-reserved-names`. |
| `RunContract(t, Resource)` | The convention tests on any collection (E1.6): create/Location, strict members, `limit=1` traversal, cursors, ETag/304, `If-Match` 428/412/200/`*`, merge patch, PUT round trip, idempotency replay. A resource whose owning page names other codes sets `MalformedCode` (unknown member) and `ImmutableCode` (immutable member), and `AltPatch` when a patch that changes nothing writes nothing (the repository resource, Q-REPO-21). |

`TestResponsesMatchTheDocument` ([openapi_test.go](../../internal/api/v1/openapi_test.go)) sends every `GET` operation of the
document, anonymous and authenticated, through the chain and validates the answers. The integration tests of
[test/integration/api](../../test/integration/api) validate the system operations across several in-process replicas that
share one database and one Valkey.

## Jobs, tasks and the key-ring status

- **Jobs** ([jobs_api.go](../../internal/api/v1/jobs_api.go)): rows of `job`, read from PostgreSQL by every replica. The list
  is newest first over `(created_at, id)` (indexes `job_created`, `job_kind_created`), filtered by `kind` and `state`
  (repeat the parameter). `replica` is the name of the replica that claimed the job (`job.ran_by`, set by the claim and kept
  when the job ends, Q-OPS-46); `triggeredBy` the principal.
- **Tasks** ([tasks_api.go](../../internal/api/v1/tasks_api.go)): `jobs.Registry.ResolveTasks` merges the registered tasks with
  `maintenance.tasks`. A manual-only task (`jobs.TaskSpec.ManualOnly`) has no schedule and no `maintenance.tasks` entry
  (an entry stops start-up and names the key). `POST …/run` checks the optional parameters against the type the task
  declares in `Services.TaskArgs` (`blob.GCJobArgs` for `gc` and `gc-full`), enqueues in a transaction with the
  `system.task_triggered` event, and answers `409` with the existing job when the exclusive key (`gc` for both collection
  tasks) is held. A `web` replica only enqueues; the integration test `TestGarbageCollectionTrigger` shows the run on the
  `worker`.
- **Key-ring status** ([encryption_api.go](../../internal/api/v1/encryption_api.go)): `secrets.KeyRing.StatusWithCounts` (shared with
  `trove admin secrets status`) for the answering replica, the sealed counts from
  [listq](../../internal/meta/postgres/listq/sealed.go) over the registered columns, the rings of the other replicas from
  the replica registry, and the canary rows of `secret_key_canary`. `RingsCompatible` is the rule; the trigger of
  `secrets-reencrypt` refuses with `409 key_ring_incompatible` while it is false.
  At start-up `verifyKeyRingCanaries` ([wire_api.go](../../internal/app/wire_api.go)) refuses a ring that lacks a key that sealed
  values reference, seeds the canary of every key ID that has none, and refuses a ring that cannot open an existing canary.
  The job body of `secrets-reencrypt` ([wire_api.go](../../internal/app/wire_api.go), `registerSecretsReencrypt`) calls
  `repository.ResealSecrets` for the one registered sealed column (`repository_secrets.sealed`) and records
  `columns`, `resealed`, `current` and `failed` as its result; it fails permanently when `failed` is above zero (a value the
  ring of this replica cannot open is left as it is), and fails permanently, naming the column, for a column registered later
  without a walk of its own. The first feature that registers another column writes its walk (the sealed counter in `listq`
  is the pattern).

## Rate limits, CORS and response headers

The chain's stage 7 is `v1.EdgeStage(CORS, rate limit)` built in [wire_api.go](../../internal/app/wire_api.go) and set as
`a.m1.rateLimit`. Order: the security headers of R-API-017 on every `/api/v1/` answer (`Cache-Control: no-store` unless the
handler sets another, `Content-Security-Policy`, `Referrer-Policy`; `X-Content-Type-Options` and `X-Request-Id` come from the
chain), then CORS, then the limiter, so a preflight is answered before it counts. The preflight of an allowed origin is
answered earlier still, by `Chain.Preflight` (`v1.Preflight`, between the address stage and authentication): a browser sends
it without credentials, and authentication would refuse it (R-API-018, Q-API-138). `CORS` keeps the same answer for a chain
without that stage.

The classes are `api_read`, `api_write` and `api_anonymous` ([ratelimit.go](../../internal/api/v1/ratelimit.go)); they apply to
`/api/v1/` and `/files/` only. The mechanism is [the cluster limiter](valkey.md#rate-limit-counters). Every answer on a limited
route carries `RateLimit-Policy` and `RateLimit`; a refusal is `429 rate_limited` with `Retry-After` and counts in
`trove_api_rate_limited_total{class}`. Enabling `api.rate_limits.exempt_admins` needs the authz area's `content:bypass`
check (`IsAdmin` in `RateLimitOptions`); without the authorizer nobody is exempt.

**Consequence for tests and the development profile.** The defaults apply to anonymous callers too (5 requests per second
per client address, burst 20). A test or script that polls `/api/v1/system/replicas` through `system.bootstrap_anonymous_read`
faster than that gets `429`; set `api.rate_limits.enabled: false` (`TROVE_API_RATE_LIMITS_ENABLED=false`) in such an
environment.

## Not yet in M1

`/api/v1/storage/gc-runs` (the domain view of runs) is not built: Q-API-9 chose one path per function, the job list filters by
`kind` and carries each run's result, and no M1 screen needs more (Q-API-20). The audit event types are registered in
[events_api.go](../../internal/audit/events_api.go); the recorder is the audit area's, and the composition root passes
`audit.Nop` until it is merged (`auditRecorder` in `wire_api.go`). Response compression (R-API-017) and the 30 s bound on a
synchronous handler are not implemented; the server-wide timeouts of R-ARCH-005 apply.
