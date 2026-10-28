# API and UI

Status: specification — not implemented

This page specifies trove's management HTTP API and the Angular UI built on it. Part A fixes the
conventions every management endpoint follows: base path and versioning, the OpenAPI 3.1 contract,
JSON rules, authentication and CSRF handling on the API, problem-details errors, cursor
pagination, filtering, optimistic concurrency, idempotency keys, long-running work, rate limits
and response headers. Part B is the endpoint surface of the MVP and P1: one table per resource
family with method, path, purpose and required permission, pointing at the page that owns the
resource's semantics and payloads. Part C specifies the UI: routes, screens, sign-in,
permission-driven rendering, client setup, the permission explainer, the CI claim tester,
accessibility, internationalization, state management and testing. What a resource *means* (a
repository, a permission policy, an identity mapping, a token scope) and its payload fields are
owned by the page named in the [ownership table](README.md#pages); this page fixes how resources
are exposed over HTTP, keeps the surface consistent across those pages, and specifies how the UI
presents them. Package-manager protocols (`/api/<format>/<repoKey>/…`) belong to the format pages;
health probes and metrics on the operations listener belong to
[operations-and-observability.md](operations-and-observability.md).

Covers: the operation catalogs [api-content-operations.md](../features/api-content-operations.md)
and [api-admin-operations.md](../features/api-admin-operations.md), and the screen inventory
[ui-inventory.md](../features/ui-inventory.md). Single entries are drawn from
[events-and-extensibility.md](../features/events-and-extensibility.md) (EVT-070, EVT-072),
[artifacts.md](../features/artifacts.md) (ART-027, ART-028, ART-031, ART-040, ART-041, ART-048,
ART-049), [identity-and-access.md](../features/identity-and-access.md) (IAM-030, IAM-031,
IAM-032), [authentication-providers.md](../features/authentication-providers.md) (AUTHN-037 to
AUTHN-040), [access-tokens.md](../features/access-tokens.md) (TOK-026),
[oidc-ci-integration.md](../features/oidc-ci-integration.md) (CIOIDC-024),
[operations-and-monitoring.md](../features/operations-and-monitoring.md) (OPS-008, OPS-023),
[search-and-query.md](../features/search-and-query.md) (SRCH-048, SRCH-058),
[repositories.md](../features/repositories.md) (REPO-007, REPO-008) and
[beyond-baseline.md](../features/beyond-baseline.md) (BEYOND-030).

## Reading this page

- Part A (R-API-001 to R-API-019): conventions. Part B (R-API-020 to R-API-048): endpoint surface.
  Part C (R-UI-001 to R-UI-047): UI.
- Path notation: `{key}` is one path segment. Every path is relative to the public base URL
  (R-OPS-005 in [operations-and-observability.md](operations-and-observability.md)).
- Permission column vocabulary: the repository actions of INV-9 (`read`, `annotate`, `deploy`,
  `overwrite`, `delete`, `manage`) on a repository and optionally a path or package; `admin` is the
  system administrator role of [authorization.md](authorization.md); a `word:word` value is a
  system scope item of a token (R-AUTH-025 in
  [identity-and-authentication.md](identity-and-authentication.md), for example `policies:read`);
  `self` means the caller acts on its own resource; `authenticated` is any authenticated principal;
  `anonymous` means the endpoint answers without credentials. Delegations defined by the owning
  pages (project administrators, CI issuer delegates, delegated policy managers) extend these
  columns without changing the paths.
- Payload sketches appear only for resources this page owns. For every other resource the owning
  page holds the normative fields; the OpenAPI document (R-API-002) is where the pages meet, and a
  disagreement between pages is a review finding, not a choice for the implementer.
- Examples use `TROVE_URL=https://trove.example.com` (# example) and a token in the
  environment variable `TROVE_TOKEN`.

## Part A — API conventions

### R-API-001 · Base path, versions and compatibility
Priority: **MVP** · Covers: API-001, ADMAPI-002, EVT-072

Every management endpoint MUST live under `/api/v1/` (INV-6). The major version in the path is
the only version selector; trove MUST NOT version through media types or request headers. The
compatibility promise is the one of R-ARCH-025 in [architecture.md](architecture.md); this
requirement makes it concrete for the API.

| Allowed within v1 | Not allowed within v1 (needs `/api/v2/`) |
|---|---|
| New endpoints, new optional request fields and query parameters | Removing or renaming a field, endpoint or parameter |
| New response fields | Changing a field's type or meaning |
| New values of enums documented as open; new problem `code` values | Making an optional input mandatory |
| New link relations and response headers | Changing the status code of an existing condition |
| Relaxing validation | Tightening validation of input that was accepted before |

- Clients MUST ignore unknown response fields. Every enum is documented as open or closed in the
  OpenAPI document; the UI handles open enums with a default branch.
- A breaking change ships as `/api/v2/` side by side with v1. v1 then stays available for at least
  12 months and at least two minor releases after v2 is generally available, whichever is longer.
- A deprecated operation is marked `deprecated: true` in the OpenAPI document. Its responses carry
  `Deprecation` ([RFC 9745](https://www.rfc-editor.org/rfc/rfc9745), structured date such as
  `@1798761600`), `Sunset` ([RFC 8594](https://www.rfc-editor.org/rfc/rfc8594)) once the removal
  date is fixed, and `Link: <…>; rel="deprecation"` pointing at the migration note in the release
  notes. Calls are counted in `trove_api_deprecated_requests_total{operation}` (INV-16).
- An operation MAY be marked experimental with `x-trove-stability: experimental`. It lives under
  `/api/v1/` but may change in any minor release; the UI uses it only behind a feature switch.

Acceptance criteria:

- `curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TROVE_TOKEN" "$TROVE_URL/api/v1/repositories"`
  prints `200`; the same request to `/api/v0/repositories` and `/api/v3/repositories` prints
  `404` with a problem body.
- An integration test registers a fixture operation marked deprecated with a sunset date and
  asserts the `Deprecation`, `Sunset` and `Link` headers and the metric increment.
- The breaking-change check of R-API-002 fails a pull request that renames a v1 response field.

### R-API-002 · OpenAPI 3.1 document as the contract
Priority: **MVP** · Covers: EVT-070

One [OpenAPI 3.1](https://spec.openapis.org/oas/v3.1.1.html) document, `api/openapi.yaml`
(R-ARCH-003), describes the whole public management API: every `/api/v1/` operation and the raw
file path `/files/…` (R-API-025). Package-manager protocol endpoints are not in it; they follow
their ecosystem's specification. The document is served at `/api/v1/openapi.json` (INV-6) and
`/api/v1/openapi.yaml` without authentication; it holds no instance data except
`servers[0].url`, which is the public base URL. INV-17's "public `/api/v1` API" means exactly the
operations in this document (Q-API-4).

Every operation MUST declare:

| Element | Rule |
|---|---|
| `operationId` | lowerCamelCase verb + noun (`createRepository`), stable for the life of v1; generated clients and audit events use it |
| `tags` | The resource family of Part B |
| `security` | The accepted credential kinds of R-API-005 |
| `x-trove-permission` | The repository action or role that admits the caller, machine-readable, e.g. `{"action": "deploy", "repository": "query.repository", "path": "query.path"}` or `{"role": "admin"}`; R-API-008 enforces from it |
| `x-trove-system-scope` | The token system scope item that admits the caller instead of a role, where one exists (R-AUTH-025) |
| `x-trove-recent-auth` | `true` for operations that require recent authentication through a session (R-API-007) |
| `x-trove-idempotency-key` | `optional` on creating POSTs (R-API-014) |
| Responses | Success schema with an example; every possible 4xx and 5xx as a reference to the shared problem schema (R-API-009) |
| Pagination | List operations reference the shared cursor parameters and envelope (R-API-010) |

Schemas use JSON Schema 2020-12, the default dialect of OpenAPI 3.1. How code is derived from the
document is decided here (Q-API-1; R-ARCH-003 pins whatever generator is chosen); the
recommendation is spec-first. CI MUST:

1. lint the document;
2. compare it with the document of the latest release and fail on a breaking change in v1
   (R-ARCH-025; a diff tool such as [oasdiff](https://github.com/oasdiff/oasdiff));
3. regenerate the Go and TypeScript code derived from it and fail on `git diff --exit-code`;
4. validate every response produced in the Go integration tests against the document (a 3.1
   capable validator such as [libopenapi-validator](https://github.com/pb33f/libopenapi-validator));
5. compare the routes registered on the router with the document's operations in both directions,
   so no endpoint exists undocumented and no documented endpoint is missing.

Every generator and validator version is pinned once (Go tools through the `tool` directive in
`go.mod`, TypeScript tools through the lockfile), and local builds and CI use the same pins.

Acceptance criteria:

- `curl -s "$TROVE_URL/api/v1/openapi.json" | jq -r .openapi` prints a value starting with
  `3.1.`.
- The route-parity check of item 5 fails when a handler is registered without a document entry.
- A script over the document asserts that every operation has `operationId`, a permission or
  scope declaration, and at least one problem response.

### R-API-003 · JSON representation rules
Priority: **MVP** · Covers: API-001

| Aspect | Rule |
|---|---|
| Media type | Request and response bodies are `application/json; charset=utf-8`; errors `application/problem+json` (R-API-009). A JSON endpoint receiving another `Content-Type` answers 415. Exceptions are named where they apply: `application/merge-patch+json` (R-API-013), raw bytes on `/files`, YAML for configuration as code, `text/csv` and JSON Lines exports, `application/scim+json`, `text/event-stream`, `text/plain` snippets. |
| Field names | ASCII lowerCamelCase (`createdAt`, `upstreamUrl`). Enum values are lowercase strings as the owning page defines them. Fields whose names are fixed by an external standard keep them: the OAuth fields of the CI exchange ([RFC 8693](https://www.rfc-editor.org/rfc/rfc8693)), SCIM attributes ([RFC 7643](https://www.rfc-editor.org/rfc/rfc7643)), the RFC 9457 problem members. |
| Query parameters | lowerCamelCase, same names as the JSON fields they filter (`createdFrom`, `pathPrefix`). |
| Configuration keys | Not JSON: YAML configuration keys are snake_case (R-ARCH-020), for example `api.page_size_max`. |
| Times | [RFC 3339](https://www.rfc-editor.org/rfc/rfc3339) in UTC with `Z` (`2026-10-28T14:03:11.123Z`). Input accepts any RFC 3339 offset and is normalized. |
| Durations, sizes | Integers with the unit in the name (`ttlSeconds`, `sizeBytes`), or the owning page's documented form. No pre-formatted strings such as "3.4 GB". |
| Booleans | `true` / `false`, in query strings too (`?recursive=true`); `1` / `0` are rejected. |
| Absent values | Every documented response field is present; "no value" is `null`; empty collections are `[]`. |
| Unknown input | Strict decoding: an unknown field or a duplicate key answers 400 with a JSON pointer to it. Read-only fields (`createdAt`, `revision`, …) are accepted and ignored, so a body obtained by GET can be sent back. |
| Secrets | Write-only. Responses never contain them; they show whether one is stored (`{"set": true}` or a reference), as the owning page defines. On PUT an omitted secret keeps its stored value, so GET-modify-PUT never wipes it; an explicit `null` clears it. |
| URLs | Absolute URLs in responses (`downloadUrl`, `Location`, client URLs) come from the base URL rules of R-OPS-005, never from unvalidated request headers. |

Acceptance criteria:

- `POST /api/v1/repositories` with a body containing `"upsteamUrl"` answers 400, `code`
  `malformed_request`, `errors[0].pointer` `/remote/upsteamUrl`.
- For every configuration resource, a test GETs it, PUTs the body back unchanged with `If-Match`,
  and asserts that the stored state, secrets included, is unchanged.

### R-API-004 · Resource naming, identifiers and methods
Priority: **MVP** · Covers: API-001, ADMAPI-002

- Collections are plural kebab-case nouns (`/api/v1/permission-policies`, `/api/v1/trash-can/items`).
- Keyed resources are addressed by the key or name the owning page defines (repositories by key,
  users by username, groups, service accounts, policies, CI issuers and mappings by name, webhooks
  and retention policies by key). Resources without a natural key (tokens, jobs, trash entries,
  audit events, packages, support bundles) are addressed by a server-generated identifier that
  clients treat as opaque.
- Item paths inside a repository are passed as the query parameter `path` on `/api/v1/` routes
  (`/api/v1/repositories/{repoKey}/items?path=…`, [storage.md](storage.md)) and as the path tail
  on `/files/{repoKey}/{path}`.
- Methods:

| Method | Use | Success |
|---|---|---|
| GET, HEAD | Read; MUST NOT change state | 200 (304 with `If-None-Match`) |
| POST on a collection | Create | 201 with the resource and `Location` |
| PUT on an item | Replace the full representation | 200 with the resource (201 where the owning page defines PUT as idempotent create, e.g. role bindings) |
| PATCH on an item | JSON Merge Patch (R-API-013) | 200 with the resource |
| DELETE on an item | Remove | 204, or 202 with a job (R-API-015) |
| POST `{collection-or-item}/{verb}` | A non-CRUD action with a kebab-case verb (`/reindex`, `/restorations`, `/test`, `/keys/refresh`) | 200 with a result, or 202 with a job |

- The custom-method form `POST /api/v1/repositories:batchCreate` used by
  [repositories-and-proxy.md](repositories-and-proxy.md) deviates from the sub-path form above;
  Q-API-7 decides which one v1 uses.
- A path or `path` parameter is percent-decoded once. Empty segments, `.` and `..`, NUL, control
  characters and invalid UTF-8 answer 400 `invalid_path` (the format conformance kit of R-ARCH-010
  applies the same rules to protocol paths).
- A wrong method answers 405 with an `Allow` header. `OPTIONS` is answered only for CORS
  (R-API-018).

Acceptance criteria:

- A create answers 201 whose `Location` header equals the URL on which a subsequent GET succeeds.
- `GET /api/v1/repositories/npm-local/items?path=a/../b` answers 400 `invalid_path`.

### R-API-005 · Caller authentication on the management API
Priority: **MVP** · Covers: API-001, ADMAPI-001

| Credential | Presented as | Typical caller |
|---|---|---|
| trove token (`trv_pat_`, `trv_sat_`, `trv_cit_`, INV-8) | `Authorization: Bearer <token>` | Scripts, CI jobs, tooling |
| The same token as Basic password | `Authorization: Basic` with any username | Tools that only speak Basic, `curl -u` on `/files` |
| UI session | `trv_session` cookie (INV-8) plus CSRF protection (R-API-006) | The Angular UI |
| None | — | Anonymous caller, where anonymous access is enabled (R-AUTH-040) and a policy grants it |

- When an `Authorization` header is present, the session cookie is ignored for that request.
- Invalid, expired or revoked credentials answer 401; the server MUST NOT fall back to anonymous
  access when a credential was presented.
- Access tokens and ID tokens of an identity provider are never accepted as credentials (INV-8).
  The only endpoint that takes an ID token is the CI exchange (R-API-035), as a body parameter.
- 401 answers under `/api/v1/` carry `WWW-Authenticate: Bearer realm="trove"`
  ([RFC 6750](https://www.rfc-editor.org/rfc/rfc6750)) and no `Basic` challenge, so browsers never
  open their native credential prompt for UI requests. `/files` adds the `Basic` challenge
  (R-ARCH-011) except for requests carrying `Sec-Fetch-Site: same-origin`.
- Credential verification, session lifetime and binding, and the scope model are owned by
  [identity-and-authentication.md](identity-and-authentication.md). Every handler and audit event
  knows the principal, the credential kind and the token or session identifier.

Acceptance criteria:

- `curl -s -o /dev/null -w '%{http_code}' -H 'Authorization: Bearer trv_pat_invalid' "$TROVE_URL/api/v1/auth/whoami"`
  prints `401`, also with anonymous access enabled.
- `curl -s -u "anyname:$TROVE_TOKEN" "$TROVE_URL/api/v1/auth/whoami"` names the token owner.
- A Playwright test with an expired session sees no browser credential dialog.

### R-API-006 · CSRF protection on the management API
Priority: **MVP** · Covers: trove-only

The mechanism (per-session token in the `trv_csrf` cookie, `X-CSRF-Token` header, origin and
fetch-metadata checks, failure code `csrf_failed`) is R-AUTH-012 in
[identity-and-authentication.md](identity-and-authentication.md). This page fixes where it
applies and how the UI uses it:

- It applies to every request authenticated by the session cookie with an unsafe method (POST,
  PUT, PATCH, DELETE) under `/api/v1/` and `/files/`, and to `POST /auth/logout`.
- Requests authenticated by an `Authorization` header are exempt, because they carry no ambient
  credential.
- No GET under `/api/v1/` changes state; sign-out is a POST.
- The UI configures Angular's XSRF support with
  `withXsrfConfiguration({cookieName: 'trv_csrf', headerName: 'X-CSRF-Token'})`
  ([Angular API](https://angular.dev/api/common/http/withXsrfConfiguration)) and calls the API with
  relative URLs, which is the case in which Angular attaches the header to unsafe requests.

Acceptance criteria:

- With a valid session cookie and no `X-CSRF-Token`, `POST /api/v1/tokens` answers 403
  `csrf_failed`; with the header it succeeds.
- The same request with `Authorization: Bearer $TROVE_TOKEN` and no CSRF header succeeds.
- A Playwright test submits a form from a page on a second origin to `/api/v1/tokens` while the
  session cookie is present; no token is created.

### R-API-007 · Recent authentication for sensitive actions
Priority: **MVP** · Covers: trove-only

A stolen or forgotten-open session must not be enough to mint long-lived credentials or widen
permissions. The mechanism (re-authentication round trip through the identity provider, answer
401 `reauthentication_required`) is R-AUTH-011 in
[identity-and-authentication.md](identity-and-authentication.md). This page names the API
operations that require it when called with a session; the OpenAPI document marks them with
`x-trove-recent-auth: true`:

| Operation family | Operations |
|---|---|
| Tokens | Creating and rotating personal and service-account tokens (R-API-032) |
| Permissions | Creating, replacing and deleting permission policies; binding and unbinding system roles (R-API-033) |
| CI identity | Creating, replacing and deleting CI issuers, identity mappings and delegations (R-API-035) |
| Settings | Changing authentication settings (R-API-037) |
| Repositories | Deleting a repository (R-API-022) |
| Configuration | Applying configuration as code (R-API-044) |

Token-authenticated callers are not affected; their token scope bounds them.

Acceptance criteria:

- With a session whose last interactive authentication is older than the window of R-AUTH-011,
  `DELETE /api/v1/repositories/npm-old` answers 401 `reauthentication_required`; after the
  re-authentication round trip the same request succeeds.

### R-API-008 · Authorization declaration and denial responses
Priority: **MVP** · Covers: ADMAPI-001, ADMAPI-041, IAM-032

- Each operation's required permission is declared in the OpenAPI document (R-API-002) and
  enforced by one shared step that calls the evaluator of [authorization.md](authorization.md)
  before the handler runs. Handlers add checks only where the decision depends on the request body
  (for example the target repository of a copy).
- Administrative read operations are reachable by a token carrying the matching system scope item
  instead of the administrator role (`identities:read`, `policies:read`, `settings:read`,
  `audit:read`, `repositories:read-config`, `tokens:read`; R-AUTH-025), so read-only automation
  needs no administrator rights.
- Denials:

| Situation | Answer |
|---|---|
| No credential, and anonymous access does not grant the operation | 401 `unauthenticated` with challenge |
| Authenticated, permission missing, target visible to the caller | 403 `forbidden`, with the extension `requiredPermission` (the action and target the caller asked for, nothing more) |
| Authenticated, target is a repository, item or package the caller cannot read, and hiding is on | 404 `not_found`, identical to a missing target |
| Administrative collections (users, groups, settings, …) without permission | 403; the existence of these endpoints is not secret |

- Hiding is `authz.hide_existence` with its per-repository override (R-AUTHZ-017 in
  [authorization.md](authorization.md)).
- List and search endpoints filter silently: they never answer 403 because some items are
  invisible, and no count includes invisible items.

Acceptance criteria:

- A user with `read` on `npm-a` only: `GET /api/v1/repositories` lists `npm-a` alone;
  `GET /api/v1/repositories/npm-b` answers 404 with hiding on and 403 with it off.
- A token carrying `identities:read` can `GET /api/v1/users` and gets 403 on
  `POST /api/v1/users/{username}/disable`.

### R-API-009 · Errors as problem details
Priority: **MVP** · Covers: API-001, ADMAPI-002

Every 4xx and 5xx answer under `/api/v1/`, `/auth/` and `/files/` is
`application/problem+json` as defined by [RFC 9457](https://www.rfc-editor.org/rfc/rfc9457).
The Go error model behind it is R-ARCH-023 in [architecture.md](architecture.md).
Package-manager protocol endpoints keep their ecosystem's error formats (format pages); SCIM keeps
SCIM errors (R-API-046).

| Member | Content |
|---|---|
| `type` | The relative URI `/api/v1/problems/<code>` (R-ARCH-023). trove serves a human-readable description of every code there from the embedded code registry, so the URI resolves on every installation and is identical across installations. |
| `title` | Fixed English summary per `code` |
| `status` | The HTTP status |
| `detail` | Instance-specific English explanation |
| `instance` | The request path |
| `code` | Extension: stable lowercase `snake_case` identifier from the code registry; clients match on it |
| `requestId` | Extension: equals the `X-Request-Id` response header (R-API-019) |
| `errors` | Extension, optional: `[{"pointer": "/key", "code": "pattern", "detail": "…"}]` with [RFC 6901](https://www.rfc-editor.org/rfc/rfc6901) JSON pointers into the request body, for field-level problems |

`detail` MUST NOT contain stack traces, SQL, internal host names, file-system paths, object keys,
secret values or an upstream's raw error body (R-ARCH-023). 5xx answers carry a generic `detail`;
the full error is logged under the same `requestId`. The UI translates by `code` (R-UI-042,
R-UI-045); API text stays English.

```json
{
  "type": "/api/v1/problems/validation_failed",
  "title": "Request validation failed",
  "status": 422,
  "detail": "1 field is invalid.",
  "instance": "/api/v1/repositories",
  "code": "validation_failed",
  "requestId": "4bf92f3577b34da6a3ce929d0e0e4736",
  "errors": [
    {"pointer": "/key", "code": "pattern", "detail": "must match ^[a-z0-9][a-z0-9._-]{1,63}$"}
  ]
}
```

<details>
<summary>Generic codes (owning pages add resource-specific ones)</summary>

| Status | `code` | When |
|---|---|---|
| 400 | `malformed_request` | Unparsable JSON, wrong JSON types, unknown or duplicate field, unknown query parameter |
| 400 | `invalid_path` | Item path violates R-API-004 |
| 400 | `invalid_cursor` | Cursor not issued by the server, or issued for other filters or sort (R-API-010) |
| 400 | `filter_required` | A search without any selective filter (R-API-028) |
| 401 | `unauthenticated` | Missing, invalid, expired or revoked credential |
| 401 | `reauthentication_required` | R-API-007 |
| 403 | `forbidden` | Permission missing |
| 403 | `csrf_failed` | R-API-006 |
| 404 | `not_found` | Target missing, or hidden (R-API-008) |
| 405 | `method_not_allowed` | Wrong method, or the action does not apply to the repository class |
| 406 | `not_acceptable` | No acceptable representation |
| 409 | `already_exists` | Create with an existing key or name |
| 409 | `conflict` | State conflict; owning pages define specific codes (`project_not_empty`, `last_admin`, …) |
| 409 | `idempotency_in_progress`, `token_already_issued` | R-API-014 |
| 412 | `precondition_failed` | `If-Match` does not match; extension `currentEtag` |
| 413 | `payload_too_large` | R-API-017 |
| 415 | `unsupported_media_type` | Wrong `Content-Type` |
| 422 | `validation_failed` | Semantically invalid input; `errors[]` lists the fields |
| 422 | `idempotency_key_reused` | R-API-014 |
| 428 | `precondition_required` | `If-Match` required (R-API-012) |
| 429 | `rate_limited` | R-API-016; `Retry-After` set |
| 500 | `internal` | Unexpected failure (R-ARCH-023) |
| 502 | `upstream_error` | A synchronous action that calls an upstream or issuer failed |
| 503 | `unavailable` | A dependency is unreachable or maintenance mode refuses the write; `Retry-After` set |

Success statuses used: 200, 201 (with `Location`), 202 (with `Location` of the job), 204, 304.
207 is not used; partial failures of bulk work are reported in the job result or the per-item
results of the owning page.

</details>

Acceptance criteria:

- An invalid repository key answers 422 with `Content-Type: application/problem+json`, `code`
  `validation_failed` and `errors[0].pointer` `/key`.
- `curl -s "$TROVE_URL/api/v1/problems/validation_failed"` answers 200 with a description.
- A test injects a database error and asserts that the 500 body contains no SQL and no Go type
  names, and that the log line with the same `requestId` contains the cause.

### R-API-010 · Cursor pagination
Priority: **MVP** · Covers: ADMAPI-003

Every list endpoint is paginated by keyset (cursor) pagination; SCIM is the only exception.

- Parameters: `limit` (default 50, maximum 500) and `cursor`.
- Response envelope, plus `Link: <…>; rel="next"` ([RFC 8288](https://www.rfc-editor.org/rfc/rfc8288))
  when more items exist:

```json
{"items": [], "nextCursor": "djEuZXlKcklqb2libkJ0TFdGc2JDSjk"}
```

- The cursor is opaque: a versioned base64url encoding of the sort-key values of the last item and
  a fingerprint of the filters and sort order. A cursor that does not decode, or that was issued
  for other filters or another sort, answers 400 `invalid_cursor`. A cursor grants nothing: every
  page is filtered by the caller's permissions again. After an upgrade that changes the encoding
  version, old cursors answer 400 and the client restarts.
- Every sort order ends with the item's unique identifier as tie-breaker, so a traversal returns
  no duplicates and skips nothing among items that do not change during it; items created or
  deleted meanwhile may or may not appear.
- Totals are not returned by default. Administrative lists whose content does not depend on the
  caller's permissions (users, groups, policies, audit events, jobs) MAY accept
  `includeTotal=true` and add `total`.
- Each sortable field has a composite index `(field, id)` in the owning table.

Configuration:

```yaml
api:
  page_size_default: 50    # default (proposed)
  page_size_max: 500       # default (proposed)
```

Acceptance criteria:

- With 120 groups, paging `GET /api/v1/groups?limit=50` yields three pages with 120 distinct
  names; `nextCursor` is `null` on the last page, which has no `Link` header.
- Reusing a cursor with a different `sort` answers 400 `invalid_cursor`.
- A cursor obtained by an administrator, replayed by a user without read on those repositories,
  returns only repositories that user can read.

### R-API-011 · Filtering and sorting
Priority: **MVP** · Covers: API-001

| Form | Meaning | Example |
|---|---|---|
| `field=value` | Equality | `format=npm` |
| `field=a&field=b` | OR within one field: repeat the parameter (values may contain commas, so no comma lists) | `format=npm&format=nuget` |
| Different fields | AND | `format=npm&class=remote` |
| `q=text` | Case-insensitive substring over the fields the endpoint documents | `q=logging` |
| `<field>Prefix=text` | Prefix match, for type-ahead | `namePrefix=@team-a/` |
| `<field>From` / `<field>To` | Half-open range: `From` inclusive, `To` exclusive | `createdFrom=2026-10-01T00:00:00Z` |
| `sort=field` / `sort=-field` | Ascending / descending, one field from the documented set | `sort=-createdAt` |

An unknown query parameter or sort field answers 400 `malformed_request`. Sparse field selection
is not offered in v1.

Acceptance criteria:

- `GET /api/v1/repositories?format=npm&class=remote&sort=-createdAt` returns only remote npm
  repositories, newest first.
- `GET /api/v1/repositories?fromat=npm` answers 400 naming `fromat`.

### R-API-012 · Optimistic concurrency with ETag and If-Match
Priority: **MVP** · Covers: trove-only

- Every mutable configuration resource carries an integer revision that increases with every
  change (the owning page names the column, e.g. `config_version` for repositories). GET returns a
  strong `ETag: "<revision>"`; where the representation depends on the caller's view (for example
  repository configuration with or without `repositories:read-config`), the tag includes the view:
  `"<revision>-<view>"`.
- PUT, PATCH and DELETE accept `If-Match` ([RFC 9110 §13.1.1](https://www.rfc-editor.org/rfc/rfc9110#section-13.1.1)).
  A mismatch answers 412 `precondition_failed` with the extension `currentEtag`. `If-Match: *`
  passes when the resource exists.
- `If-Match` is optional by default. An owning page MAY make it mandatory for its resource (428
  without it; [authorization.md](authorization.md) does so for permission-policy PUT), and the
  operator switch `api.require_if_match` makes it mandatory everywhere (Q-API-2). The UI always
  sends it.
- GET supports `If-None-Match` and answers 304.
- File content on `/files` uses the SHA-256 ETag of [storage.md](storage.md); `If-None-Match: *` on PUT makes a
  deploy create-only (412 when the path exists), so CI can publish without the risk of replacing a
  released file.

Data model (convention for every configuration table; the tables belong to their pages):

```sql
-- update pattern; zero rows while the key exists means 412
UPDATE <table>
   SET ..., revision = revision + 1, updated_at = now(), updated_by = $actor
 WHERE <key column> = $1 AND ($2::bigint IS NULL OR revision = $2)
RETURNING revision;
```

Configuration:

```yaml
api:
  require_if_match: false   # default (proposed); see Q-API-2
```

Acceptance criteria:

- Two clients GET the same repository (`ETag: "4"`) and PATCH it with `If-Match: "4"`; one
  answers 200 with `ETag: "5"`, the other 412 with `currentEtag` `"5"`.
- `curl -X PUT -T app.tgz -H 'If-None-Match: *' -u "ci:$TROVE_TOKEN" "$TROVE_URL/files/generic-local/app/1.0/app.tgz"`
  answers 201 the first time and 412 the second time.

### R-API-013 · Partial updates with JSON Merge Patch
Priority: **MVP** · Covers: ADMAPI-007, ADMAPI-016, ADMAPI-024

- Where an owning page offers PATCH, its body is JSON Merge Patch
  ([RFC 7396](https://www.rfc-editor.org/rfc/rfc7396)) with
  `Content-Type: application/merge-patch+json`: a present member replaces, `null` removes the
  member or resets it to its default, an absent member stays unchanged, an array is replaced as a
  whole. JSON Patch (RFC 6902) is not offered.
- Collections edited member by member have their own endpoints instead of array patches (group
  members and user group memberships take `{"add": [...], "remove": [...]}`, R-API-031).
- A permission policy is changed by PUT of the whole document with `If-Match`
  ([authorization.md](authorization.md)); together with the ETag this covers "change one part of a
  policy without silently losing the rest" (ADMAPI-024) without per-section endpoints.
- Immutable fields (`key`, `format`, `class`, names the owner declares immutable) in a patch answer
  422 when they differ from the stored value and are ignored when equal.
- The answer is the full updated resource with its new ETag.

Acceptance criteria:

- `curl -X PATCH -H 'Content-Type: application/merge-patch+json' -H 'If-Match: "3"' -d '{"description": null}' "$TROVE_URL/api/v1/repositories/npm-remote"`
  answers 200 with `"description": null` and `ETag: "4"`.
- `{"class": "local"}` sent to a remote repository answers 422 with pointer `/class`.

### R-API-014 · Idempotency keys for creates
Priority: **MVP** · Covers: trove-only

POST endpoints that create a resource or start a job accept the `Idempotency-Key` request header
of the IETF draft
[The Idempotency-Key HTTP Header Field](https://datatracker.ietf.org/doc/draft-ietf-httpapi-idempotency-key-header/)
(draft-07 at the time of writing; revisited when it becomes an RFC). Clients SHOULD send a UUID;
the server accepts 1 to 255 printable ASCII characters. The header is optional.

- Scope: the authenticated principal plus the key. The UI generates a key when a create form
  opens, so a double click or a retry after a network error creates one resource.
- The first request is processed; its outcome (status, `Location`, body) is stored in the same
  database transaction as the change and kept for `api.idempotency_retention`.
- A later request with the same key, method, route and body hash answers with the stored outcome
  and the header `Idempotency-Replayed: true`. Stored 4xx outcomes are replayed too; a 5xx rolls
  the transaction back and stores nothing, so a retry runs again.
- Same key with a different method, route or body: 422 `idempotency_key_reused`.
- Concurrent duplicate: the second request blocks on the row inserted by the first (primary-key
  conflict) for at most 5 seconds, then replays the outcome or answers 409
  `idempotency_in_progress`.
- Responses that contain a secret are never stored. A replayed token creation answers 409
  `token_already_issued` with the extension `tokenId`, so the client can revoke that token and
  retry with a new key; the secret is not reproduced.

Data model:

```sql
CREATE TABLE api_idempotency_keys (
  principal_id     uuid        NOT NULL,
  idem_key         text        NOT NULL CHECK (char_length(idem_key) BETWEEN 1 AND 255),
  method           text        NOT NULL,
  route            text        NOT NULL,          -- route pattern plus path parameters
  request_sha256   bytea       NOT NULL,          -- over the canonical request body
  response_status  smallint    NOT NULL,
  response_headers jsonb       NOT NULL DEFAULT '{}',
  response_body    jsonb,                         -- NULL when the response carried a secret
  secret_issued_id text,                          -- token id for token creations
  created_at       timestamptz NOT NULL DEFAULT now(),
  expires_at       timestamptz NOT NULL,
  PRIMARY KEY (principal_id, idem_key)
);
CREATE INDEX api_idempotency_keys_expiry ON api_idempotency_keys (expires_at);
```

Expired rows are deleted hourly by a scheduled task of R-OPS-019 on the job framework of R-ARCH-016.

Configuration:

```yaml
api:
  idempotency_retention: 24h   # default (proposed)
```

Acceptance criteria:

- Two identical `POST /api/v1/repositories` requests with the same `Idempotency-Key` answer 201
  twice with identical bodies; the second carries `Idempotency-Replayed: true`; one repository
  exists.
- The same key with a different body answers 422 `idempotency_key_reused`.
- A replayed `POST /api/v1/tokens` answers 409 `token_already_issued` with `tokenId`, and no
  table row contains the token secret.

### R-API-015 · Long-running work as jobs
Priority: **MVP** · Covers: ADMAPI-002, API-063

Work that can outlast a request (repository deletion, reindexing, garbage collection, large copy,
move or restore, configuration apply, retention runs, support bundles, storage summary refresh)
answers 202 with `Location` of the job and the job representation as body. Jobs, their states and
their API (`GET /api/v1/system/jobs`, `GET /api/v1/system/jobs/{id}`,
`POST /api/v1/system/jobs/{id}/cancel`) are R-ARCH-016 in [architecture.md](architecture.md)
(model) and R-OPS-019 in [operations-and-observability.md](operations-and-observability.md) (API).
Jobs survive replica restarts and are visible from every replica. Owning pages MAY additionally
expose a domain view of their runs (garbage-collection runs, retention runs) that references the
job.

Rules this page adds:

- A 202 answer always carries `Location` pointing at a resource the caller may poll; while the
  work runs, polling answers carry `Retry-After` with a suggested interval.
- Callers who are not administrators may read and cancel the jobs they started (in addition to the
  administrator access of R-OPS-019).
- Partial failures appear in the job result as a list of at most 100 entries plus the true failure
  count.

Acceptance criteria:

- `DELETE /api/v1/repositories/npm-old` on a repository with 10,000 items answers 202; polling
  `Location` ends in `succeeded`; afterwards `GET /api/v1/repositories/npm-old` answers 404.
- Killing the replica that runs the job lets another replica finish it.

### R-API-016 · Rate limits
Priority: **MVP** · Covers: BEYOND-030

Rate limits on the management API protect replica resources from one runaway script or UI tab.
The limits for login, token validation and token revocation are R-AUTH-038 in
[identity-and-authentication.md](identity-and-authentication.md); those of the CI exchange are
R-CI-021 in [gitlab-ci-oidc.md](gitlab-ci-oidc.md). This requirement adds the general classes for
`/api/v1/` and `/files/`:

| Class | Applies to | Bucket key |
|---|---|---|
| `read` | Safe methods | Principal (token ID, or session user) |
| `write` | Unsafe methods | Principal |
| `anonymous` | Requests without credentials | Client address (R-ARCH-006, R-OPS-005) |

An exhausted bucket answers 429 `rate_limited` with `Retry-After` (seconds) and the
`RateLimit-Policy` and `RateLimit` fields of
[draft-ietf-httpapi-ratelimit-headers](https://datatracker.ietf.org/doc/draft-ietf-httpapi-ratelimit-headers/)
(draft-11 at the time of writing; the syntax follows the draft until it is published). Buckets are
kept in memory per replica, like those of the identity and CI pages (Q-API-3, H-43).
Administrators are not exempt by default. Rejections are counted in
`trove_api_rate_limited_total{class}`.

Configuration:

```yaml
api:
  rate_limits:
    enabled: true                                      # default (proposed)
    exempt_admins: false                                # default (proposed)
    read:      {requests_per_second: 50, burst: 200}     # default (proposed)
    write:     {requests_per_second: 10, burst: 50}      # default (proposed)
    anonymous: {requests_per_second: 5,  burst: 20}      # default (proposed)
```

Acceptance criteria:

- A loop of 300 `GET /api/v1/repositories` with one token receives 429 answers after the burst,
  each with `Retry-After`; a second token is not slowed down.

### R-API-017 · Request limits, timeouts and response headers
Priority: **MVP** · Covers: trove-only

- JSON bodies are limited to `api.max_request_body_bytes`; YAML for configuration as code to
  `api.max_configuration_body_bytes`. Larger bodies answer 413. Uploads on `/files` are limited by
  [storage.md](storage.md), not by this rule.
- A synchronous `/api/v1/` handler MUST finish within 30 seconds; work that can take longer is a
  job (R-API-015). Server-wide timeouts are owned by [architecture.md](architecture.md).
- Response headers on `/api/v1/`: `Cache-Control: no-store` (personal data and secrets must not be
  cached by browsers or intermediaries), `X-Content-Type-Options: nosniff`,
  `Content-Security-Policy: default-src 'none'; frame-ancestors 'none'` (a JSON answer can never
  render as a page), `Referrer-Policy: no-referrer`, `X-Request-Id`. Content responses on
  `/files` carry the content headers of R-ARCH-007 instead.
- JSON answers are compressed when the client accepts it, except answers that contain a secret
  (token creation and rotation), which are sent uncompressed so that their length cannot leak the
  secret through compression.

Configuration:

```yaml
api:
  max_request_body_bytes: 1048576          # default (proposed), 1 MiB
  max_configuration_body_bytes: 4194304    # default (proposed), 4 MiB
```

Acceptance criteria:

- `curl -sD - -o /dev/null -H "Authorization: Bearer $TROVE_TOKEN" "$TROVE_URL/api/v1/auth/whoami"`
  shows the four security headers and `X-Request-Id`.
- A 2 MiB JSON body answers 413 `payload_too_large`.

### R-API-018 · Cross-origin access
Priority: **MVP** · Covers: trove-only

The UI is same-origin; CORS is off by default and the server emits no `Access-Control-Allow-*`
headers. `api.cors.allowed_origins` (exact origins, no wildcards) enables CORS for requests that
carry an `Authorization` header. `Access-Control-Allow-Credentials` is never sent, so a session
cookie is never usable cross-origin. Allowed request headers: `Authorization`, `Content-Type`,
`If-Match`, `If-None-Match`, `Idempotency-Key`. Exposed response headers: `ETag`, `Location`,
`Link`, `Retry-After`, `RateLimit`, `RateLimit-Policy`, `X-Request-Id`, `Deprecation`, `Sunset`.

```yaml
api:
  cors:
    allowed_origins: []                                 # default (proposed)
    # allowed_origins: ["https://portal.example.com"]   # example
```

Acceptance criteria:

- A preflight from `https://unlisted.example` gets no `Access-Control-Allow-Origin`.
- With `https://portal.example.com` listed, its preflight is answered and the actual response
  carries no `Access-Control-Allow-Credentials`.

### R-API-019 · Audit and request correlation
Priority: **MVP** · Covers: OPS-023

- Every mutating request under `/api/v1/` and `/files/`, successful or denied, produces an audit
  event (INV-16) with actor, credential kind and token ID (never the secret), action
  (`operationId` or the owning page's action name), target, outcome, source and `requestId`, and
  for permission policies, role bindings and identity mappings the before and after of the grants.
  Event schema, storage, retention and the log stream are owned by
  [operations-and-observability.md](operations-and-observability.md); this page requires that no
  management change commits without its event.
- Denied reads are audited; successful reads are not, except reads of the audit log itself and an
  administrator's listing of other principals' tokens.
- `X-Request-Id` carries the request's trace ID (R-ARCH-023, R-OPS-018) on every answer and in every problem.

Acceptance criteria:

- `POST /api/v1/repositories` produces an audit event whose `requestId` equals the response's
  `X-Request-Id`.
- A rejected policy change (403) produces an audit event with outcome `denied`.

## Part B — Endpoint surface

### R-API-020 · Resource map and routing
Priority: **MVP** · Covers: trove-only

| Path prefix | Family | Requirement | Semantics and payloads |
|---|---|---|---|
| `/api/v1/auth/whoami`, `/api/v1/auth/login-options`, `/api/v1/authz/me`, `/api/v1/authz/check`, `/api/v1/users/me…` | Current caller | R-API-021 | [identity-and-authentication.md](identity-and-authentication.md), [authorization.md](authorization.md) |
| `/api/v1/formats`, `/api/v1/repositories…`, `/api/v1/upstream-tests` | Repositories | R-API-022 to R-API-024, R-API-030 | [repositories-and-proxy.md](repositories-and-proxy.md), [architecture.md](architecture.md) |
| `/api/v1/repositories/{repoKey}/items`, `/children`, `/stats`, `/properties`; `/files/…` | Items and files | R-API-025, R-API-026 | [storage.md](storage.md) |
| `/api/v1/content/…`, `/api/v1/trash-can/…` | Copy, move, trash | R-API-027 | [storage.md](storage.md) |
| `/api/v1/search/…` | Search | R-API-028 | this page; indexes in [storage.md](storage.md) |
| `/api/v1/packages…` | Packages across repositories | R-API-029 | this page; package metadata in [storage.md](storage.md) and format pages |
| `/api/v1/users`, `/api/v1/groups`, `/api/v1/service-accounts`, `/api/v1/tokens` | Identities and tokens | R-API-031, R-API-032 | [identity-and-authentication.md](identity-and-authentication.md) |
| `/api/v1/permission-policies`, `/api/v1/roles`, `/api/v1/role-bindings`, `/api/v1/authz/…` | Authorization | R-API-033, R-API-034 | [authorization.md](authorization.md) |
| `/api/v1/ci/…`, `/api/v1/auth/oidc/token` | CI identity | R-API-035 | [gitlab-ci-oidc.md](gitlab-ci-oidc.md) |
| `/api/v1/settings/…`, `/api/v1/system/banner`, `/api/v1/system/maintenance`, `/api/v1/system/proxy-offline` | Settings | R-API-037 | the page per setting |
| `/api/v1/system/…` | Version, information, jobs, tasks, diagnostics | R-API-038, R-API-045 | [operations-and-observability.md](operations-and-observability.md), [architecture.md](architecture.md) |
| `/api/v1/audit/…` | Audit events | R-API-039 | [operations-and-observability.md](operations-and-observability.md) |
| `/api/v1/maintenance/…`, `/api/v1/storage/summary` | Garbage collection, storage summary | R-API-040 | [storage.md](storage.md) |
| `/api/v1/webhooks…` | Webhooks (P1) | R-API-041 | [operations-and-observability.md](operations-and-observability.md) |
| `/api/v1/builds…` | Build information (P1) | R-API-042 | open, Q-API-5 |
| `/api/v1/retention/…` | Retention and cleanup (P1) | R-API-043 | [storage.md](storage.md) |
| `/api/v1/config/…` | Configuration as code (P1) | R-API-044 | [operations-and-observability.md](operations-and-observability.md) |
| `/api/v1/scim/v2/…` | SCIM (P1) | R-API-046 | [identity-and-authentication.md](identity-and-authentication.md) |
| `/api/v1/projects…` | Projects (P1) | R-API-047 | [authorization.md](authorization.md) |
| `/api/v1/problems/{code}`, `/api/v1/openapi.json`, `/api/v1/openapi.yaml` | Contract | R-API-009, R-API-002 | this page |

Routes are registered on the HTTP stack of R-ARCH-005 with method-qualified patterns. The browser
login flow under `/auth/` is not part of `/api/v1/`; the exchange endpoint
`/api/v1/auth/oidc/token` is fixed by INV-10.

The owning pages diverge on a few paths for the same function. Until review settles them, this
page lists the semantic owner's path and names the alternative: garbage collection
(`/api/v1/maintenance/gc-runs` in [storage.md](storage.md) against
`/api/v1/system/tasks/gc/run` of R-OPS-020 in
[operations-and-observability.md](operations-and-observability.md)), and the storage summary
(`/api/v1/storage/summary` in [storage.md](storage.md) against `/api/v1/system/storage/summary` of
R-OPS-023).

Acceptance criteria:

- The route-parity check of R-API-002 passes.
- A request to every prefix in the table without credentials answers 401 or the documented
  anonymous result, never 500.
- After review, each function in this table has exactly one path in the OpenAPI document.

### R-API-021 · Current caller and UI preferences
Priority: **MVP** · Covers: trove-only

| Method | Path | Purpose | Permission | Owner |
|---|---|---|---|---|
| GET | `/api/v1/auth/whoami` | Principal and credential of the caller | authenticated | R-AUTH in [identity-and-authentication.md](identity-and-authentication.md) |
| GET | `/api/v1/auth/login-options` | Providers, auto-redirect, break-glass visibility, anonymous access; the sign-in screen renders only from it | anonymous | [identity-and-authentication.md](identity-and-authentication.md) |
| GET | `/api/v1/users/me` | Own user record | authenticated user | [identity-and-authentication.md](identity-and-authentication.md) |
| GET | `/api/v1/users/me/sessions`, DELETE `/api/v1/users/me/sessions…` | Own sessions; end one or all | `self` | R-AUTH-010 in [identity-and-authentication.md](identity-and-authentication.md) |
| GET | `/api/v1/authz/me` | Effective capabilities, scope summary and principal set; drives navigation | authenticated | R-AUTHZ in [authorization.md](authorization.md) |
| POST | `/api/v1/authz/check` | Up to 100 permission checks for the caller's own credential; drives per-control rendering | authenticated | R-AUTHZ-023 in [authorization.md](authorization.md) |
| GET, PATCH | `/api/v1/users/me/preferences` | UI preferences | `self` | this page |

Preferences are owned here:

```json
{"locale": "de", "theme": null, "keyboardShortcuts": false, "projectFilter": null, "revision": 2}
```

`locale` is `en`, `de` or `null` (browser decides); `theme` is `light`, `dark` or `null` (system);
`projectFilter` is a project key or `null` (P1, R-UI-041). PATCH takes a merge patch with
`If-Match`.

Data model:

```sql
CREATE TABLE user_preferences (
  user_id            uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  locale             text CHECK (locale IN ('en', 'de')),
  theme              text CHECK (theme IN ('light', 'dark')),
  keyboard_shortcuts boolean NOT NULL DEFAULT false,
  project_filter     text,
  revision           bigint  NOT NULL DEFAULT 1,
  updated_at         timestamptz NOT NULL DEFAULT now()
);
```

Acceptance criteria:

- `curl -s -H "Authorization: Bearer $TROVE_TOKEN" "$TROVE_URL/api/v1/auth/whoami" | jq -r .credential.type`
  names the token type.
- `PATCH /api/v1/users/me/preferences` with `{"locale": "de"}` makes the next visit of `/ui/`
  open the German UI (R-UI-045).
- Service-account and CI tokens get 403 on `/api/v1/users/me/preferences`.

### R-API-022 · Repositories
Priority: **MVP** · Covers: API-052, API-054, API-057, API-058, API-059, API-060, API-061, API-063

Semantics, payloads and status codes: [repositories-and-proxy.md](repositories-and-proxy.md).

| Method | Path | Purpose | Permission |
|---|---|---|---|
| GET | `/api/v1/formats` | Format descriptors with classes, clients and settings schema (R-ARCH-008, R-ARCH-010) | authenticated; anonymous where anonymous access is on |
| GET | `/api/v1/repositories` | List; filters `class`, `format`, `q`, `state`, `project`; `view` | readable repositories; configuration fields only with `repositories:read-config` or `manage` |
| POST | `/api/v1/repositories` | Create | `admin` or `repository-admin` role; project administrators per R-AUTHZ-026 |
| GET | `/api/v1/repositories/{key}` | Read; full configuration for `manage` and `repositories:read-config` | `read` |
| PUT, PATCH | `/api/v1/repositories/{key}` | Replace or merge patch, with `If-Match` | as create, or `manage` |
| DELETE | `/api/v1/repositories/{key}?detach=` | Delete configuration and content; 202 with a job | as create; recent authentication |
| POST | `/api/v1/repositories/{key}/upstream-test` | Probe the saved upstream configuration | as update |
| POST | `/api/v1/upstream-tests` | Probe an unsaved remote configuration | as create |
| GET | `/api/v1/repositories/{key}/resolution?path=` | Explain how a virtual repository resolves a client path | `read` |

Mapping of catalog operations: API-058 (format-filtered configuration) is the regular read, which
contains only fields that apply to the class and format; API-059 (configurations by key) and
API-060 (all configurations) are the list with `view` and the batch read of R-API-023.

Acceptance criteria:

- Creating an npm remote repository with
  `curl -s -X POST -H "Authorization: Bearer $TROVE_TOKEN" -H 'Content-Type: application/json' -d @npm-remote.json "$TROVE_URL/api/v1/repositories"`
  answers 201; then `npm view lodash version --registry "$TROVE_URL/api/npm/npm-remote/"`
  prints a version.
- A reader without `repositories:read-config` gets no upstream configuration in the answer.

### R-API-023 · Repository batch operations and quick setup
Priority: **P1** · Covers: API-053, API-055, API-064

Owned by [repositories-and-proxy.md](repositories-and-proxy.md):

| Method | Path | Purpose | Permission |
|---|---|---|---|
| POST | `/api/v1/repositories:batchCreate` | Create several repositories, all or nothing | as create |
| POST | `/api/v1/repositories:batchUpdate` | Merge-patch several repositories with per-entry `ifMatch`, all or nothing | as update |
| POST | `/api/v1/repositories:batchGet` | Read several configurations | as read, per key |
| POST | `/api/v1/repositories:batchDelete` | Delete several repositories, best effort; 202 with per-key job references | as delete |
| POST | `/api/v1/repositories:quickSetup` | Create local, remote and virtual repositories per format from a prefix (R-UI-023) | as create |

The path syntax is subject to Q-API-7. All of them accept `Idempotency-Key` (R-API-014).

Acceptance criteria:

- A `batchCreate` whose third document is invalid answers 422 with a pointer into that document
  and creates nothing.

### R-API-024 · Repository maintenance actions
Priority: **P1** · Covers: API-028, API-040, API-041, API-043, API-045, API-050

| Method | Path | Purpose | Permission | Owner |
|---|---|---|---|---|
| POST | `/api/v1/repositories/{key}/reindex` | Rebuild the format's index and metadata from stored packages (npm package documents, NuGet indexes; later Maven metadata, PyPI simple index, Helm `index.yaml`); 202 with a job; 409 when the format has no index | `manage` | R-OPS-022, format pages |
| POST | `/api/v1/repositories/{key}/cache/invalidate` | Mark cached metadata of a remote repository under a path prefix as stale; binaries stay cached (INV-11) | `manage` or `delete` | R-OPS-021, [repositories-and-proxy.md](repositories-and-proxy.md) |
| GET | `/api/v1/repositories/{key}/cache/entries` | Cached metadata entries for diagnosis | `manage` | [repositories-and-proxy.md](repositories-and-proxy.md) |

Acceptance criteria:

- After a test removes a computed npm package document, `POST …/npm-local/reindex` restores it and
  `npm view @team-a/ui versions` lists every stored version.
- `POST …/npm-remote/cache/invalidate` followed by `npm view lodash` makes trove fetch the package
  document from the upstream again (visible in the upstream mock's request log).

### R-API-025 · Items and raw file transfer
Priority: **MVP** · Covers: API-003, API-004, API-005, API-013, API-016, API-022, ART-027, ART-028

Semantics, payloads and status codes: [storage.md](storage.md) (R-STOR-007 and the item
requirements) and the request paths of R-ARCH-011 and R-ARCH-014 in
[architecture.md](architecture.md).

| Method | Path | Purpose | Permission |
|---|---|---|---|
| GET | `/api/v1/repositories/{repoKey}/items?path=` | Metadata of a file or folder (digests, size, media type, creation, download statistics) | `read` on the path |
| GET | `/api/v1/repositories/{repoKey}/children?path=&recursive=&maxDepth=&folders=` | Paginated children | `read` on the path |
| GET | `/api/v1/repositories/{repoKey}/stats?path=` | Download statistics | `read` on the path |
| DELETE | `/api/v1/repositories/{repoKey}/items?path=` | Delete a file or folder | `delete` on the path |
| GET, HEAD | `/files/{repoKey}/{path}` | Download, `Range`, conditional requests, `Repr-Digest` | `read` |
| PUT | `/files/{repoKey}/{path}` | Raw deploy; `Repr-Digest` verified; trailing `/` with empty body creates a folder | `deploy`; `overwrite` to replace different content |
| DELETE | `/files/{repoKey}/{path}` | Same as the item DELETE | `delete` |

API conventions that apply on top:

- A remote repository's items answer from the cache only; the metadata routes never trigger an
  upstream fetch.
- npm and NuGet repositories refuse raw deploys (the format answers with a reason pointing to
  `npm publish` or `dotnet nuget push`), because a raw upload would bypass their metadata
  generation.

Acceptance criteria:

- After `npm publish` of `@team-a/ui@1.4.0` to `npm-local`,
  `GET /api/v1/repositories/npm-local/items?path=@team-a/ui/-/ui-1.4.0.tgz` returns the SHA-256
  that `shasum -a 256 ui-1.4.0.tgz` prints.
- `curl -s -X PUT -T app.tgz -u "ci:$TROVE_TOKEN" -H "Repr-Digest: sha-256=:$(openssl dgst -sha256 -binary app.tgz | base64):" "$TROVE_URL/files/generic-local/app/1.0/app.tgz"`
  answers 201 (generic format, P1); `curl -s -r 0-99 -u "ci:$TROVE_TOKEN" "$TROVE_URL/files/generic-local/app/1.0/app.tgz" | wc -c`
  prints `100`.
- Paging the children of a folder with 1,000 files at `limit=100` returns ten pages.

### R-API-026 · Deploy by checksum and item properties
Priority: **P1** · Covers: API-007, API-009, API-010, API-011, API-014

Owned by [storage.md](storage.md):

| Method | Path | Purpose | Permission |
|---|---|---|---|
| PUT | `/files/{repoKey}/{path}` with `Trove-Deploy-By-Checksum: ?1` and `Repr-Digest`, empty body | Create a file from content already stored; 404 when the content is unknown **or** not readable by the caller (R-STOR-008) | `deploy` on the target and `read` on an item holding the content |
| GET | `/api/v1/repositories/{repoKey}/properties?path=&keys=` | Read properties | `read` |
| PATCH | `/api/v1/repositories/{repoKey}/properties?path=&recursive=` | Merge patch of properties | `annotate` |
| DELETE | `/api/v1/repositories/{repoKey}/properties?path=&keys=&recursive=` | Remove properties | `annotate` |

The comma-separated `keys` parameter of [storage.md](storage.md) deviates from the repeated
parameter rule of R-API-011; review aligns one of them.

Acceptance criteria:

- `curl -X PATCH -H 'Content-Type: application/merge-patch+json' -d '{"qa.status": "passed"}' "$TROVE_URL/api/v1/repositories/generic-local/properties?path=app/1.0/app.tgz"`
  answers 200, and `GET /api/v1/search/items?property=qa.status%3Dpassed` finds the file.
- A user without read on any item holding the content gets 404 for a deploy by checksum, and the
  same answer for a random digest.

### R-API-027 · Copy, move and trash can
Priority: **P1** · Covers: API-023, API-024, API-111

Owned by [storage.md](storage.md):

| Method | Path | Purpose | Permission |
|---|---|---|---|
| POST | `/api/v1/content/copy` | Copy a file or folder to another path or repository; dry run | `read` on the source, `deploy` on the target (`overwrite` when replacing) |
| POST | `/api/v1/content/move` | Move a file or folder; dry run | as copy, plus `delete` on the source |
| GET | `/api/v1/trash-can/items` | Trash entries the caller may see | visibility rule of [storage.md](storage.md) |
| POST | `/api/v1/trash-can/restorations` | Restore by IDs, by deletion job or by source path, optionally to another target | as [storage.md](storage.md) |
| DELETE | `/api/v1/trash-can/items` | Purge entries | `delete` or `manage` on the original path |
| POST | `/api/v1/trash-can/purge` | Empty the trash can; 202 | `admin` |

Acceptance criteria:

- A copy of `@team-a/ui/-/ui-1.4.0.tgz` from `npm-local` to `npm-release` makes
  `npm view @team-a/ui@1.4.0 --registry "$TROVE_URL/api/npm/npm-release/"` succeed.
- A deleted file appears in `GET /api/v1/trash-can/items?repository=generic-local`, and after a
  restoration it downloads again with the same SHA-256.

### R-API-028 · Artifact search
Priority: **P1** · Covers: API-092, API-095, API-096, API-098, API-099, API-100, API-101

No other page specifies the search operations; this page does. `GET /api/v1/search/items` returns
item descriptions as defined in [storage.md](storage.md), with cursor pagination.

| Parameter | Meaning |
|---|---|
| `name` | Case-insensitive substring of the file name |
| `pathGlob` | Glob over the path: `*` within a segment, `**` across segments, `?` one character |
| `checksum` | Lowercase hex; the length selects MD5 (32), SHA-1 (40), SHA-256 (64) or SHA-512 (128) |
| `property` | `key=value`, or `key` alone for any value; repeat for AND |
| `repository`, `class`, `format` | Restrict the scope; a virtual repository expands to its members |
| `createdFrom`, `createdTo`, `modifiedFrom`, `modifiedTo`, `lastDownloadedFrom`, `lastDownloadedTo` | Date ranges (R-API-011) |
| `notDownloadedSince` | Never downloaded, or last downloaded before this time |
| `sort` | `name`, `createdAt`, `modifiedAt` and their descending forms |

At least one of `name`, `pathGlob`, `checksum`, `property` or a date filter is required (400
`filter_required`). Results are filtered by `read`. The indexes the search needs (trigram index on
names, B-tree indexes on digests, property index) are defined in [storage.md](storage.md); the SQL
predicate form of the permission filter comes from [authorization.md](authorization.md).

Acceptance criteria:

- `GET /api/v1/search/items?checksum=<sha256 of app.tgz>` returns every readable copy of the file.
- `GET /api/v1/search/items?pathGlob=app/**/app.tgz&repository=generic-local` returns
  `app/1.0/app.tgz`.
- A file in a repository the caller cannot read never appears, and the result count does not
  change when such a file is added.

### R-API-029 · Packages, versions and README
Priority: **MVP** · Covers: ART-048, ART-049, API-094, API-109, API-110

This page owns the cross-repository package view the UI needs; package and version metadata are
stored as [storage.md](storage.md) and the format pages define. (The npm checksum lookup
`/api/v1/npm/{repoKey}/packages/{name}/versions/{version}` of [formats/npm.md](formats/npm.md) and
the package-ownership endpoints of [authorization.md](authorization.md) are per repository and
remain as their owners define them.)

| Method | Path | Purpose | Permission |
|---|---|---|---|
| GET | `/api/v1/packages` | Packages; filters `format`, `repository`, `q` (name and description), `namePrefix`, `keyword`, `createdFrom` / `createdTo` (first version); `sort` `name`, `-updatedAt` | visible versions (below); anonymous where allowed |
| GET | `/api/v1/packages/lookup?format=npm&name=@team-a/ui` | Resolve a name to the package | as list |
| GET | `/api/v1/packages/{packageId}` | Package summary | as list |
| GET | `/api/v1/packages/{packageId}/versions` | Versions; filters `prerelease`, `repository`; `sort` `-publishedAt`, `-version` | as list |
| GET | `/api/v1/packages/{packageId}/versions/{version}` | Version details | as list |
| GET | `/api/v1/packages/{packageId}/versions/{version}/readme` | Sanitized README HTML | as list |

- A package is identified by format plus normalized name (npm names as stored; NuGet IDs
  case-insensitive, displayed in their original casing). Version strings are percent-encoded in the
  path.
- A version is visible to a caller who holds `read` on a repository containing it, with the
  package-name narrowing of INV-9 applied. A package is listed when at least one version is
  visible, and `latestVersion` is computed from visible versions only, so a hidden newer version
  leaks neither its number nor its existence. The filter runs in SQL.
- A virtual repository in the `repository` filter expands to its members. Remote repositories
  contribute only cached versions; this endpoint never queries an upstream.
- Version details contain `metadata` (a format-specific object whose schema the format page owns),
  `repositories` (`[{"key", "class", "publishedAt"}]`), `files`
  (`[{"path", "sizeBytes", "sha256", "downloadUrl"}]`) and `downloadCount`.
- README answer: `{"contentType": "text/markdown", "html": "<h1>…</h1>", "truncated": false, "externalImagesBlocked": 2}`;
  404 when the version has none. Rendering and sanitizing follow R-UI-008; README extraction (npm
  `readme` field or `README.md` in the tarball, NuGet `<readme>` file in the package) is owned by
  the format pages.
- Formats added in P1 use the same endpoints: Maven coordinates as `name=group:artifact`
  (API-094), container images as packages with tags as versions (API-109, API-110).

```json
{
  "id": "pkg_01JBQ6V7S3K9",
  "format": "npm",
  "name": "@team-a/ui",
  "description": "Shared UI components",
  "latestVersion": "1.4.0",
  "keywords": ["ui", "components"],
  "repositories": ["npm-local", "npm-all"],
  "updatedAt": "2026-10-20T09:12:44.512Z"
}
```

Data model:

```sql
CREATE TABLE package_readme_cache (
  version_id       uuid PRIMARY KEY,           -- references the version table of storage.md
  source_sha256    bytea       NOT NULL,
  renderer_version integer     NOT NULL,       -- bumped when renderer or sanitizer policy changes
  html             text        NOT NULL,
  truncated        boolean     NOT NULL,
  rendered_at      timestamptz NOT NULL DEFAULT now()
);
```

Acceptance criteria:

- After `npm publish` of three versions of `@team-a/ui`,
  `GET /api/v1/packages?format=npm&q=team-a` lists the package with `latestVersion` `1.4.0`.
- A user who can read only the repository containing `1.3.0` sees `latestVersion` `1.3.0`.
- `GET …/versions/1.4.0/readme` for a README containing `<script>` returns HTML without it.

### R-API-030 · Client setup snippets
Priority: **MVP** · Covers: UI-023

`GET /api/v1/repositories/{key}/client-setup?client=` returns the snippets of a repository
(permission `read`; publish snippets only with `deploy` on the target). This page owns the
endpoint; the format registry of R-ARCH-010 in [architecture.md](architecture.md) feeds it, and
the format pages contribute the templates. Rules the UI relies on:

- The server fills the public base URL, the repository key, the package-manager endpoint URL of
  INV-6, the scheme-less endpoint path (npm's per-registry credential key) and the caller's name.
  With `package` and `version` the install snippets are specific to that version.
- The server never fills a token. Snippets contain the placeholder `{{token}}`, replaced in the
  browser (R-UI-016), or the environment-variable reference of the client's syntax
  (`${TROVE_TOKEN}` for npm and Yarn, `%TROVE_TOKEN%` for `nuget.config`) when the request
  carries `credentials=env`. The variable name is `ui.client_setup.token_env_var`.
- Every snippet has a stable `id`; the human-readable explanation lives in the UI, translated per
  `id` (R-UI-045).
- A virtual repository without a default deployment target has no publish snippets.
- Values substituted into shell commands (package names, versions, repository keys) MUST be
  single-quoted, with embedded single quotes escaped, unless they match
  `^[A-Za-z0-9@/._+:=-]+$`; values substituted into XML snippets MUST be XML-escaped. Legacy npm
  names served through remote repositories may contain `'`, `(`, `)`, `!` and `*`, which a shell
  interprets when a user pastes the command.

Configuration:

```yaml
ui:
  client_setup:
    token_env_var: TROVE_TOKEN   # default (proposed)
    token_ttl: 720h               # default (proposed), expiry of tokens created by the setup dialog
```

Acceptance criteria:

- The answer for `npm-all`, client `npm`, `credentials=env` contains
  `//trove.example.com/api/npm/npm-all/:_authToken=${TROVE_TOKEN}`.
- No answer of this endpoint, for any format, client and credential mode, matches
  `trv_(pat|sat|cit)_` (R-ARCH-010 kit check 8).
- The npm install snippet for version `1.0.0` of a legacy remote package named `a(b)` reads
  `npm install 'a(b)@1.0.0'`.

### R-API-031 · Users, groups and service accounts
Priority: **MVP** · Covers: ADMAPI-004, ADMAPI-005, ADMAPI-006, ADMAPI-007, ADMAPI-008, ADMAPI-009, ADMAPI-012, ADMAPI-013, ADMAPI-014, ADMAPI-015, ADMAPI-016, ADMAPI-017, ADMAPI-018

Owned by [identity-and-authentication.md](identity-and-authentication.md); users are addressed by
username:

| Method | Path | Purpose | Permission |
|---|---|---|---|
| GET | `/api/v1/users` | List; filters `q`, `status`, `origin`, `group`, `admin` | `admin` or `identities:read` |
| POST | `/api/v1/users` | Pre-provision a user linked to an identity-provider identity | `admin` |
| GET | `/api/v1/users/{username}` | Read | `admin`, `identities:read` |
| PATCH | `/api/v1/users/{username}` | Change display name, email, identities | `admin` |
| POST | `/api/v1/users/{username}/disable`, `/enable` | Disable (optionally revoking tokens) or enable | `admin` |
| DELETE | `/api/v1/users/{username}` | Delete | `admin` |
| PATCH | `/api/v1/users/{username}/groups` | `{"add": [...], "remove": [...]}` on manual memberships | `admin` |
| GET, DELETE | `/api/v1/users/{username}/sessions` | List or end a user's sessions | `admin` |
| DELETE | `/api/v1/users/{username}/tokens` | Revoke all tokens of a user | `admin` |
| POST | `/api/v1/users/{breakGlassName}/unlock` | Unlock the locked break-glass account | `admin` |
| GET, POST | `/api/v1/groups` | List and create | `admin`; reads also `identities:read` |
| GET, PATCH, DELETE | `/api/v1/groups/{name}` | Read, change (including rename), delete | as list |
| GET, PATCH | `/api/v1/groups/{name}/members` | Members with their sources; add and remove manual members | as list |
| GET, POST | `/api/v1/service-accounts` | List and create | `admin`; reads also `identities:read`; owners from P1 |
| GET, PATCH, DELETE | `/api/v1/service-accounts/{name}` | Read, change, delete | as list |
| POST | `/api/v1/service-accounts/{name}/disable`, `/enable` | Disable or enable | `admin` |

System roles of users and groups are role bindings (R-API-033), not user fields. There are no user
passwords (INV-7); the lockout of ADMAPI-012 exists only for the break-glass account (R-AUTH-038).

Acceptance criteria:

- `GET /api/v1/users?q=jdoe` as administrator returns the user; as a non-administrator without
  `identities:read`, 403.
- `POST /api/v1/users/jdoe/disable` with `{"revokeTokens": true}` makes `jdoe`'s tokens fail with
  401.

### R-API-032 · Access tokens
Priority: **MVP** · Covers: ADMAPI-039, ADMAPI-040, ADMAPI-043, ADMAPI-044, ADMAPI-045, ADMAPI-046

Owned by [identity-and-authentication.md](identity-and-authentication.md) (token model, scope
grammar, lifetimes, revocation):

| Method | Path | Purpose | Permission |
|---|---|---|---|
| POST | `/api/v1/tokens` | Create a personal token for the caller | user with session (recent authentication) or a PAT under the derivation rule |
| POST | `/api/v1/service-accounts/{name}/tokens` | Create a service-account token | `admin`; owners from P1; recent authentication for sessions |
| GET | `/api/v1/tokens` | List own tokens; all with `tokens:read` | `self`, `tokens:read` |
| GET | `/api/v1/tokens/{id}`, `/api/v1/tokens/current` | Read one token | owner, `tokens:read` |
| DELETE | `/api/v1/tokens/{id}`, `/api/v1/tokens/current` | Revoke | owner, `tokens:revoke`, `admin` |
| DELETE | `/api/v1/users/me/tokens` | Revoke all own tokens | `self` |
| DELETE | `/api/v1/service-accounts/{name}/tokens` | Revoke all tokens of a service account | `admin` |
| POST | `/api/v1/tokens/revoke` | Revoke by value | anyone, no authentication |
| POST | `/api/v1/tokens/{id}/rotate`, `/api/v1/tokens/current/rotate` | Rotate | as revoke; recent authentication for sessions |

API conventions that apply: the secret appears only in the create and rotate answers, which are
`no-store`, uncompressed (R-API-017) and never stored for idempotency replay (R-API-014).

Acceptance criteria:

- `curl -s -X POST -H "Authorization: Bearer $TROVE_TOKEN" -H 'Content-Type: application/json' -d '{"description":"t","ttlSeconds":3600}' "$TROVE_URL/api/v1/tokens"`
  returns a value starting with `trv_pat_` (field names per
  [identity-and-authentication.md](identity-and-authentication.md)); `GET /api/v1/tokens/{id}`
  never contains it.
- `POST /api/v1/tokens/revoke` without credentials and with a valid token value revokes it.

### R-API-033 · Permission policies and system roles
Priority: **MVP** · Covers: ADMAPI-020, ADMAPI-021, ADMAPI-022, ADMAPI-023, ADMAPI-024, IAM-031

Owned by [authorization.md](authorization.md):

| Method | Path | Purpose | Permission |
|---|---|---|---|
| GET | `/api/v1/permission-policies` | List; filters `principal` (with `expandGroups`), `repository`, `project` | `policies:read`; delegated managers see their policies |
| POST | `/api/v1/permission-policies` | Create | `policies:write`; project administrators for project policies (P1); recent authentication |
| GET | `/api/v1/permission-policies/{name}` | Read with `ETag` | as list |
| PUT | `/api/v1/permission-policies/{name}` | Replace; `If-Match` required (428) | as create; delegated managers per R-AUTHZ-025 (P1) |
| DELETE | `/api/v1/permission-policies/{name}` | Delete | as create |
| POST | `/api/v1/authz/validate-policy` | Validate without storing; errors, warnings, covered repositories | `policies:read` |
| GET | `/api/v1/roles` | The fixed system roles and their capabilities | authenticated |
| GET | `/api/v1/role-bindings` | List bindings; filters `role`, `principal` | `roles:read` |
| PUT, DELETE | `/api/v1/role-bindings/{role}/{principalRef}` | Bind (idempotent) or unbind; 409 `last_admin` | `roles:write`; recent authentication |

Acceptance criteria:

- After creating a policy that grants group `team-a` `read` and `deploy` on `npm-team-a` for
  `@team-a/**`, a member of `team-a` can `npm publish` `@team-a/ui` there and gets 403 for
  `@team-b/x`.
- Unbinding the last administrator answers 409 `last_admin`.

### R-API-034 · Effective permissions and decision explanation
Priority: **P1** · Covers: API-008, IAM-030, ART-031

Owned by [authorization.md](authorization.md) (R-AUTHZ-024):

| Method | Path | Purpose | Permission |
|---|---|---|---|
| GET | `/api/v1/authz/effective?repository=&package=` or `&path=` | Every principal holding actions on a coordinate, with the granting policies; CI mappings whose templates could render to it marked `conditional` | `policies:read`, or `manage` on the coordinate |
| POST | `/api/v1/authz/explain` | Decision trace for a principal or token on a coordinate | `policies:read` (plus `tokens:read` for a token) |

The UI explainer (R-UI-028) and the effective-permissions tab (R-UI-010) render these answers.
The explanation comes from the evaluator that enforces decisions, run in trace mode; nothing is
reimplemented for display.

Acceptance criteria:

- A property-based test generates random policies, principals and coordinates and asserts that
  `decision` of `explain` always equals the enforcement result of the same request.

### R-API-035 · CI identity
Priority: **MVP** · Covers: ADMAPI-028, ADMAPI-029, ADMAPI-030, ADMAPI-031

Owned by [gitlab-ci-oidc.md](gitlab-ci-oidc.md):

| Method | Path | Purpose | Permission |
|---|---|---|---|
| GET, POST | `/api/v1/ci/issuers` | List and create trusted CI issuers | `admin`; recent authentication for writes |
| GET, PUT, DELETE | `/api/v1/ci/issuers/{name}` | Read, replace, delete (`?cascade=true` with mappings) | `admin` |
| GET | `/api/v1/ci/issuers/{name}/keys` | Cached key set and fetch status | `admin` |
| POST | `/api/v1/ci/issuers/{name}/keys/refresh` | Discovery and key refresh now | `admin` |
| GET, POST | `/api/v1/ci/issuers/{issuer}/mappings` | List in evaluation order; create | `admin`; delegates (P1) |
| GET, PUT, DELETE | `/api/v1/ci/issuers/{issuer}/mappings/{name}` | Read, replace, delete | as above |
| POST | `/api/v1/ci/issuers/{issuer}/evaluate` | Dry run against a claim set (R-UI-036) | as above |
| GET | `/api/v1/ci/issuers/{issuer}/snippets/{kind}?mapping=&format=` | Ready-to-paste CI snippet, `text/plain` (R-UI-035) | as above |
| POST, DELETE | `/api/v1/ci/issuers/{issuer}/delegations…` | Delegate mapping management for a scope to a group (P1; covers project-scoped mappings, ADMAPI-030) | `admin` |
| GET | `/api/v1/ci/issuances`, `/api/v1/ci/issuances/{tokenId}` | Issued CI tokens with job context | `admin`, `tokens:read` |
| POST | `/api/v1/ci/issuances/revoke` | Revoke matching CI tokens | `admin`, `tokens:revoke` |
| POST | `/api/v1/auth/oidc/token` | Exchange a CI ID token for a `trv_cit_` token (INV-10) | anonymous; the ID token is the proof |

API conventions that apply: the evaluation endpoint takes claims, never a token, and the UI decodes
pasted tokens in the browser (R-UI-036); the exchange answers carry `Cache-Control: no-store` and
use the OAuth field names of RFC 8693 alongside problem details, as the owning page defines.

Acceptance criteria:

- In a GitLab CI job using the snippet of `GET …/snippets/gitlab-ci?mapping=team-a-protected&format=npm`,
  the exchange succeeds and `npm publish` of `@team-a/ui` succeeds.
- `POST …/evaluate` with a claim set that matches no mapping answers 200 with no matched mapping and
  the failing condition per candidate.

### R-API-036 · Format helper operations handed to format pages
Priority: **P1** · Covers: API-046, API-166

- PyPI JSON project metadata (API-046) is served by the PyPI protocol handler on its protocol path
  (`/api/pypi/{key}/…`, INV-6) in the shape of the [PyPI JSON API](https://docs.pypi.org/api/json/),
  not under `/api/v1/`; owned by the future PyPI format page.
- Container image promotion (API-166) is reserved as
  `POST /api/v1/repositories/{key}/images/promote` with
  `{"image", "tag", "targetRepository", "targetImage", "targetTag", "copy"}` (`tag: null` = all
  tags; permissions as copy and move in R-API-027); owned by the future Docker/OCI format page.
- Maven metadata calculation (API-043) and Helm index recalculation (API-050) are `reindex`
  (R-API-024).

Acceptance criteria: defined on the owning format pages; this page requires that the reserved
paths are used for nothing else.

### R-API-037 · Settings surface
Priority: **MVP** · Covers: ADMAPI-042, ADMAPI-047, ADMAPI-050, ADMAPI-055, ADMAPI-071

Runtime settings are owned by the page whose behavior they change. This index is what the
settings screens (R-UI-031 to R-UI-034) call:

| Setting | Where | Owner |
|---|---|---|
| Anonymous access, token default and maximum lifetimes, expiry notices (ADMAPI-042, ADMAPI-047) | `GET`, `PATCH /api/v1/settings/auth`, bounded by YAML ceilings | R-AUTH-040 |
| Break-glass lockout (ADMAPI-050), client authentication switches including Basic (ADMAPI-055) | YAML; read-only in `GET /api/v1/settings/auth` | R-AUTH-038, R-AUTH-040 |
| Public base URL (ADMAPI-071) | YAML `server.base_url`; read-only in `GET /api/v1/system/info` | R-OPS-005 |
| Hiding unauthorized resources | YAML `authz.hide_existence`, per-repository override | R-AUTHZ-017 |
| Trash can, storage behavior | `GET`, `PUT /api/v1/settings/storage` | [storage.md](storage.md) |
| Global offline mode | `GET`, `PUT /api/v1/system/proxy-offline` | [repositories-and-proxy.md](repositories-and-proxy.md) |
| Outbound proxies, client certificates | `/api/v1/system/outbound-proxies…`, `/api/v1/system/client-certificates…` | [repositories-and-proxy.md](repositories-and-proxy.md) |
| Banner | `GET`, `PUT /api/v1/system/banner` | [operations-and-observability.md](operations-and-observability.md) |
| Maintenance (read-only) mode | `GET`, `PUT /api/v1/system/maintenance` | [operations-and-observability.md](operations-and-observability.md) |

Rule for all settings documents: a value set in YAML or its environment override (INV-15) is shown
with its source and cannot be changed through the API (409), so operators who manage settings
through Helm keep control.

Acceptance criteria:

- `PATCH /api/v1/settings/auth` with a PAT maximum above the YAML ceiling answers 400
  `exceeds_ceiling`.
- With `server.base_url` set, `GET /api/v1/system/info` reports `baseUrlSource` `config`.

### R-API-038 · System information, version and jobs
Priority: **MVP** · Covers: ADMAPI-069, ADMAPI-070, API-033, OPS-008

Owned by [operations-and-observability.md](operations-and-observability.md) and
[architecture.md](architecture.md):

| Method | Path | Purpose | Permission |
|---|---|---|---|
| GET | `/api/v1/system/version` | Version and revision | per [operations-and-observability.md](operations-and-observability.md) |
| GET | `/api/v1/system/info` | Installation facts: version, base URL and source, maintenance mode, replicas, database and schema versions | `system.read` |
| GET | `/api/v1/system/replicas` | Replicas with version, roles, heartbeat | `system.read` |
| GET | `/api/v1/system/tasks`, POST `/api/v1/system/tasks/{name}/run` | Scheduled tasks and manual runs (R-OPS-019) | `system.read`; `admin` to run |
| GET | `/api/v1/system/jobs`, `/api/v1/system/jobs/{id}`, POST `…/cancel` | Jobs (R-OPS-019, R-API-015) | `system.read`; `admin` to cancel |

Liveness, readiness and Prometheus metrics stay on the operations listener (INV-6) and are not
duplicated under `/api/v1/`; this covers ADMAPI-069 and API-033 by reference.

Acceptance criteria:

- With two replicas running, `GET /api/v1/system/replicas` lists both; the system status screen
  (R-UI-038) renders from these answers without other endpoints.

### R-API-039 · Audit events
Priority: **MVP** · Covers: OPS-023

Owned by [operations-and-observability.md](operations-and-observability.md):
`GET /api/v1/audit/events` (filters `from`, `to`, `action`, `category`, `outcome`, `actor`,
`repository`, resource), `GET /api/v1/audit/events/{id}`, and
`GET /api/v1/audit/events/export` (`format=jsonl` or `csv`). Permission `audit:read` or `admin`.
Reading the audit log is itself audited (R-API-019).

Acceptance criteria:

- After creating a permission policy, the audit log screen (R-UI-037) filtered by that policy shows
  the event with the granted actions.

### R-API-040 · Garbage collection and storage summary
Priority: **MVP** · Covers: API-002, API-113, API-031, API-032

Owned by [storage.md](storage.md):

| Method | Path | Purpose | Permission |
|---|---|---|---|
| POST | `/api/v1/maintenance/gc-runs` | Start a run (`mode` incremental or full, `dryRun`); 409 while one runs | `admin` |
| GET | `/api/v1/maintenance/gc-runs`, `/api/v1/maintenance/gc-runs/{id}` | Runs with counts and reclaimed bytes | `admin` |
| POST | `/api/v1/maintenance/gc-runs/{id}/stop` | Stop after the current batch | `admin` |
| GET | `/api/v1/storage/summary` | Blob and repository usage (P1) | `admin` or the storage read scope |
| POST | `/api/v1/storage/summary/refresh` | Recompute; 202 (P1) | `admin` |

The divergent paths of other pages for the same functions are listed in R-API-020.

Acceptance criteria:

- `curl -X POST -H "Authorization: Bearer $ADMIN_TOKEN" -H 'Content-Type: application/json' -d '{"mode":"full","dryRun":true}' "$TROVE_URL/api/v1/maintenance/gc-runs"`
  answers 202; a second start while it runs answers 409.

### R-API-041 · Webhooks
Priority: **P1** · Covers: API-148, API-149, API-150, API-151, API-152, API-153, API-154, API-155, ADMAPI-027

Owned by [operations-and-observability.md](operations-and-observability.md):

| Method | Path | Purpose |
|---|---|---|
| GET, POST | `/api/v1/webhooks` | List and create subscriptions |
| GET, PUT, DELETE | `/api/v1/webhooks/{key}` | Read, replace, delete |
| POST | `/api/v1/webhooks/{key}/test`, `/api/v1/webhooks/test` | Test a stored or unsaved subscription |
| GET | `/api/v1/webhooks/{key}/status` | Last delivery, pending and failed counts |
| POST | `/api/v1/webhooks/{key}/reactivate` | Reactivate a suspended subscription |
| PUT | `/api/v1/webhooks/{key}/secrets/{name}`, POST `…/signing-secret/rotate` | Write-only secrets |
| GET | `/api/v1/webhooks/deliveries`, `/deliveries/{id}`; `?follow=true` as server-sent events | Delivery records |
| POST | `/api/v1/webhooks/deliveries/{id}/redeliver` | Redeliver |
| GET | `/api/v1/webhooks/event-types`, `/event-types/{type}/schema` | Event catalog |

Permissions (system administrator, the webhook management capability, owners) are defined on the
owning page.

Acceptance criteria:

- A subscription for package-published events on `npm-local` delivers one request to a test
  receiver after `npm publish`, and the delivery appears in `/api/v1/webhooks/deliveries`.

### R-API-042 · Build information
Priority: **P1** · Covers: API-107, API-156, API-157, API-158, API-159, API-160, API-162, API-163, API-165

No spec page owns build information yet (Q-API-5). This page reserves its surface so that the
owner can fill in record schema and permissions without moving paths. `PUT
/api/v1/builds/{buildName}/retention` is already used by [storage.md](storage.md).

| Method | Path | Purpose | Permission |
|---|---|---|---|
| GET | `/api/v1/builds` | Build names; `q`, `sort` | read on the build |
| GET | `/api/v1/builds/{buildName}/runs` | Runs, newest first | read on the build |
| PUT | `/api/v1/builds/{buildName}/runs/{number}` | Upload a build record; replacing an existing run needs `overwrite` | deploy on the build |
| GET | `/api/v1/builds/{buildName}/runs/{number}` | A run; `latest` as number alias; `compareTo={number}` adds a diff | read on the build |
| DELETE | `/api/v1/builds/{buildName}/runs/{number}` | Delete; `deleteArtifacts=true` also deletes linked artifacts (202) | delete on the build |
| POST | `/api/v1/builds/{buildName}/runs/{number}/promote` | Set status, optionally copy or move artifacts and dependencies to a target repository, set properties; `dryRun` | deploy on the build, plus the permissions of copy and move |
| GET | `/api/v1/builds/{buildName}/runs/{number}/artifacts` | Artifacts with download URLs; optional `pattern` (repeat) | read on the build |
| GET, PUT | `/api/v1/builds/{buildName}/retention` | Retention of runs | delete on the build ([storage.md](storage.md)) |

Acceptance criteria:

- Uploading a build record for run `42` and then `GET …/runs/latest/artifacts` lists the record's
  artifacts with download URLs that resolve.

### R-API-043 · Retention and cleanup policies
Priority: **P1** · Covers: ADMAPI-085, ADMAPI-086, ADMAPI-088

Owned by [storage.md](storage.md): `/api/v1/retention/policies` (list, create),
`/api/v1/retention/policies/{key}` (read, replace, delete), `…/enable`, `…/disable`,
`…/runs` (start with `dryRun`), `/api/v1/retention/policies/disable-all`,
`/api/v1/retention/runs` (list and filters), `/api/v1/retention/runs/{runId}` with `…/stop`,
`…/report` (`text/csv`) and `…/items`, and `/api/v1/retention/runs/stop-all`. Permissions on the
owning page.

Acceptance criteria:

- A dry run of a policy "keep the last 2 versions" on a package with 5 versions reports 3
  candidates and deletes nothing; the real run deletes 3.

### R-API-044 · Configuration as code
Priority: **P1** · Covers: ADMAPI-072, ADMAPI-073, ADMAPI-084

Owned by [operations-and-observability.md](operations-and-observability.md):
`GET /api/v1/config/export`, `POST /api/v1/config/diff` (plan) and `POST /api/v1/config/apply`
(with `expectedPlanHash`; recent authentication for sessions). The identity export of ADMAPI-084
is the export restricted to groups, service accounts, permission policies and CI identity.

Acceptance criteria:

- Exporting from installation A and applying to an empty installation B yields identical exports,
  secrets excluded.

### R-API-045 · Diagnostics
Priority: **P1** · Covers: ADMAPI-078, ADMAPI-080, ADMAPI-081

Owned by [operations-and-observability.md](operations-and-observability.md):
`GET /api/v1/system/locks`; `GET /api/v1/system/log-levels`,
`PUT` and `DELETE /api/v1/system/log-levels/override`; `/api/v1/system/support-bundles` (create,
list) with `/{id}`, `/{id}/download` and DELETE. Because every replica's locks live in PostgreSQL
(INV-12), one query answers for the whole cluster.

Acceptance criteria:

- While a garbage-collection run holds its lease, `GET /api/v1/system/locks` shows it with the
  holding replica.

### R-API-046 · SCIM provisioning
Priority: **P1** · Covers: ADMAPI-035

Owned by [identity-and-authentication.md](identity-and-authentication.md). The SCIM 2.0 service
provider ([RFC 7643](https://www.rfc-editor.org/rfc/rfc7643),
[RFC 7644](https://www.rfc-editor.org/rfc/rfc7644)) lives under `/api/v1/scim/v2/`. It is the one
exception to R-API-003 (SCIM attribute names), R-API-009 (SCIM error bodies, RFC 7644 §3.12) and
R-API-010 (SCIM index paging).

Acceptance criteria:

- A SCIM client creates a user and a group with that member through `/api/v1/scim/v2/Users` and
  `/Groups`; `GET /api/v1/groups/{name}/members` lists the user.

### R-API-047 · Projects
Priority: **P1** · Covers: ADMAPI-063, ADMAPI-064, ADMAPI-065, ADMAPI-067

Owned by [authorization.md](authorization.md) (R-AUTHZ-026, reduced project model):
`/api/v1/projects` (list, create), `/api/v1/projects/{key}` (read, replace, delete when empty),
`/api/v1/projects/{key}/admins/{principalRef}` (add and remove project administrators, which
covers the user and group membership operations of ADMAPI-064 and ADMAPI-065 in the reduced
model), and `/api/v1/projects/{key}/repositories/{repoKey}` (assign with `?move=true`, unassign).
List endpoints of repositories, permission policies, webhooks and retention policies accept
`project=` when projects exist. Project roles, global custom roles and environments are P2 there
(see Deferred).

Acceptance criteria:

- A project administrator of `team-a` with `managePolicies` creates a project policy (201) and
  gets 403 for a global policy.

### R-API-048 · Package ownership surface
Priority: **P1** · Covers: trove-only

Package ownership (who may publish new versions of a package name) is owned by
[authorization.md](authorization.md):
`GET`, `POST /api/v1/repositories/{repoKey}/packages/{packageName}/owners`,
`DELETE …/owners/{owner}` and `GET /api/v1/package-owners`. The package details screen
(R-UI-007) shows the owners of a package in a repository where the caller holds `read`, and offers
the owner changes to callers holding `manage`. Package names containing `/` (npm scopes) are
percent-encoded in `{packageName}` (`%40team-a%2Fui`); the router decodes the segment once.

Acceptance criteria:

- `curl -s -H "Authorization: Bearer $TROVE_TOKEN" "$TROVE_URL/api/v1/repositories/npm-local/packages/%40team-a%2Fui/owners"`
  answers 200 for a reader of `npm-local`.

## Part C — UI

### R-UI-001 · Technology baseline and delivery
Priority: **MVP** · Covers: UI-058

- Angular 22 (INV-1) in the workspace layout of R-ARCH-004: standalone components only, zoneless
  change detection (the default since v21), `OnPush` (the default since v22), strict TypeScript
  and strict templates, `HttpClient` on the fetch backend (the default since v22) except for
  uploads (R-UI-012). Checked on 2026-10-28 against v22 release coverage: `resource()` and
  `httpResource()`, Signal Forms and `@angular/aria` are stable or generally available in v22.
- Serving (R-ARCH-004): hashed assets with `Cache-Control: public, max-age=31536000, immutable`,
  `index.html` with `Cache-Control: no-cache` and the SPA fallback for every non-file path. This
  page adds:
  - the UI lives under `/ui/{locale}/` (R-UI-045); `/` redirects to `/ui/`, and `/ui/` as well as
    any `/ui/<route>` without a locale redirects to the same route under the preferred locale, so
    links such as `/ui/login?local=1` of [identity-and-authentication.md](identity-and-authentication.md)
    keep working;
  - `index.html` gets a per-response CSP nonce as the `ngCspNonce` attribute of the root element,
    which Angular applies to the styles it inserts.
- Headers for UI documents:
  `Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self' 'nonce-<n>'; img-src 'self' data:; font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'; require-trusted-types-for 'script'; trusted-types angular angular#bundler`,
  `X-Content-Type-Options: nosniff`, `Referrer-Policy: same-origin`,
  `Cross-Origin-Opener-Policy: same-origin`. Without the `angular#unsafe-bypass` policy, every
  `bypassSecurityTrust*` call fails at runtime
  ([Angular security guide](https://angular.dev/best-practices/security)). Sign-in navigates to
  `/auth/…` and the identity provider by top-level navigation, which `form-action` does not govern.
- The UI requests nothing from any origin other than trove's own (fonts and icons self-hosted, no
  analytics, no CDN), so it works in air-gapped installations.
- Supported browsers (UI-058): the last two major versions of Chrome, Edge, Firefox and Safari on
  the desktop, and the current Firefox ESR. The list is published in the user documentation; other
  browsers are unsupported.
- Bundle budget: initial JavaScript at most 400 kB compressed (# default (proposed)); every feature
  is a lazily loaded route (R-ARCH-004).
- Light and dark theme through CSS custom properties, following `prefers-color-scheme` unless the
  user's preference (R-API-021) overrides it.
- Every npm dependency is pinned once in the lockfile; CI installs with `npm ci`.

Acceptance criteria:

- `curl -sI "$TROVE_URL/ui/en/"` shows the CSP with a nonce that differs between two requests.
- A Playwright test records every request of a full journey and finds none to a foreign origin and
  no `securitypolicyviolation` event.
- The production build fails when the initial bundle exceeds the budget.

### R-UI-002 · Application shell
Priority: **MVP** · Covers: UI-001, UI-004

- Header: product name, global search (P1, R-UI-015), project filter (P1, R-UI-041), help link
  (`ui.help_url`), user menu. A configurable server name is P2 on
  [operations-and-observability.md](operations-and-observability.md).
- Navigation with two areas: **Packages and files** (packages, browser, search, trash, builds) and
  **Administration** (shown when `/api/v1/authz/me` reports any administrative capability). The
  sections are the routes of R-UI-003.
- User menu: profile and tokens, client setup, quick repository setup (P1; only when repositories
  may be created), language, theme, keyboard shortcuts on or off, sign out.
- The banner of `GET /api/v1/system/banner` above the content, and a notice when maintenance mode
  is active; banner text has URLs linked and nothing else interpreted; `role="region"` with an
  accessible name.
- A "Skip to main content" link is the first focusable element.
- Before the session expires, a non-modal dialog warns and offers to sign in again (WCAG 2.2.1);
  timeouts are those of R-AUTH-011.

Acceptance criteria:

- A user without administrative capabilities sees no Administration area; an administrator does.
- With the keyboard only, the first Tab focuses the skip link, and Enter moves focus to the main
  heading.

### R-UI-003 · Route map and deep links
Priority: **MVP** · Covers: UI-002

Every screen and every object has a bookmarkable URL under `/ui/{locale}`. Filters, sort order,
selected tab and selected version live in query parameters.

<details>
<summary>Route table (25)</summary>

| Route | Screen | Requirement |
|---|---|---|
| `/packages` | Packages list (start page) | R-UI-006 |
| `/packages/{format}/{name…}` (`?version=`) | Package details | R-UI-007 |
| `/browse/{repo}/{path…}` | File tree with selection | R-UI-010 |
| `/search` (`?type=&q=&…`) | Search results (P1) | R-UI-015 |
| `/trash` | Trash can (P1) | R-UI-014 |
| `/builds`, `/builds/{build}`, `/builds/{build}/{number}` | Builds (P1) | R-UI-040 |
| `/setup` (`?repository=&client=`) | Client setup | R-UI-016 |
| `/profile`, `/profile/tokens`, `/profile/sessions` | Profile, personal tokens, sessions | R-UI-029 |
| `/integrations/webhooks`, `/integrations/webhooks/{key}` | Webhooks (P1) | R-UI-039 |
| `/admin/repositories`, `/admin/repositories/new`, `/admin/repositories/{key}` | Repositories | R-UI-020, R-UI-021 |
| `/admin/repositories/quick-setup` | Quick setup (P1) | R-UI-023 |
| `/admin/users`, `/admin/users/{username}` | Users | R-UI-024 |
| `/admin/groups`, `/admin/groups/{name}` | Groups | R-UI-025 |
| `/admin/service-accounts`, `/admin/service-accounts/{name}` | Service accounts | R-UI-026 |
| `/admin/permissions`, `/admin/permissions/new`, `/admin/permissions/{name}` | Permission policies | R-UI-027 |
| `/admin/permissions/explain` (`?principal=&action=&repository=&path=&package=`) | Explainer (P1) | R-UI-028 |
| `/admin/roles` | System role bindings | R-UI-024 |
| `/admin/tokens` | Token administration | R-UI-030 |
| `/admin/ci`, `/admin/ci/{issuer}`, `/admin/ci/{issuer}/mappings/{mapping}`, `/admin/ci/{issuer}/test` | CI identity, claim tester | R-UI-035, R-UI-036 |
| `/admin/audit` | Audit log | R-UI-037 |
| `/admin/system`, `/admin/system/jobs` | System status, jobs | R-UI-038 |
| `/admin/settings/auth`, `/admin/settings/general`, `/admin/settings/sign-in` (P1), `/admin/settings/storage` (P1) | Settings | R-UI-031 to R-UI-034 |
| `/admin/projects…` | Projects (P1) | R-UI-041 |
| `/login` (`?local=1`), `/signed-out` | Sign-in, signed out | R-UI-004 |
| anything else | Not found, with a search field | — |

</details>

- Routes with slash-containing parameters (npm scoped names, file paths) use a router `UrlMatcher`
  that captures the remaining segments.
- Route guards only shape navigation; the API decides (R-UI-005). When a screen's first API call
  answers 403, the screen shows a "no access" state with the missing permission and, for callers
  with `policies:read`, a link to the explainer.
- Every route sets a page title ("Packages – trove") through a `TitleStrategy`.

Acceptance criteria:

- Opening `/ui/en/packages/npm/@team-a/ui?version=1.3.0` in a fresh tab shows that version.
- Copying the URL of a filtered packages list into another browser reproduces the filters.

### R-UI-004 · Sign-in, session and sign-out
Priority: **MVP** · Covers: UI-006, AUTHN-037, AUTHN-038

The flow and its security properties are owned by
[identity-and-authentication.md](identity-and-authentication.md); the UI side:

- The UI never shows a username and password form for regular users (INV-7).
- On load the UI calls `GET /api/v1/auth/whoami`. Success starts the application. 401 with
  anonymous access off navigates to `/login?returnTo=<current route>`; with anonymous access on
  (from `login-options`), the application runs anonymously with a "Sign in" button.
- `/login` renders only from `GET /api/v1/auth/login-options`: one button per provider, each
  navigating (full page) to the provider's `startUrl` with `return_to` set to the UI route. With
  `autoRedirect` set, the page navigates at once. The break-glass form appears when `localLogin`
  is true or the URL carries `?local=1`, and posts to the local login endpoint of the owning page.
  A login notice is P2 ([operations-and-observability.md](operations-and-observability.md)).
- A 401 during use (expired or revoked session) opens a dialog "Your session has ended" with "Sign
  in again"; editors with unsaved changes first offer "Copy draft as JSON".
- Sign-out: `POST /auth/logout` with the CSRF header, then navigation to the returned
  `redirectTo`, or to `/signed-out`.
- Sign-out in one tab signs out every other tab of the same browser (`BroadcastChannel`).

Acceptance criteria:

- With one provider and auto-redirect, opening `/ui/en/packages` unauthenticated lands at the
  identity provider and, after login, back on `/ui/en/packages`.
- Signing out in tab A shows `/signed-out` in tab B within two seconds.

### R-UI-005 · Permission-driven rendering
Priority: **MVP** · Covers: UI-005

- Authorization is enforced by the API alone; the UI adapts what it shows for convenience.
- Data: capabilities from `GET /api/v1/authz/me` for navigation and global actions; per-control
  decisions from `POST /api/v1/authz/check` (up to 100 checks per call, batched per screen, cached
  for 60 seconds, dropped on any 403 and on sign-out).
- Primary actions on a screen that the caller lacks are shown disabled with the reason ("Needs
  deploy on npm-local"); menu entries the caller lacks are hidden. Lists show what the API returns.
- Self-lockout guards are shown before the API refuses them: the last administrator binding cannot
  be removed, a user cannot disable themselves.
- An authenticated user who can read nothing sees an empty state that names their identity and
  groups, so they can ask for access.

Acceptance criteria:

- A user without `deploy` on `npm-local` sees the deploy action disabled with its reason; forcing
  the request from the browser console answers 403.

### R-UI-006 · Packages list
Priority: **MVP** · Covers: UI-008, ART-048

- Start page, backed by R-API-029. Filters: format (multi-select), repository (multi-select), text
  (`q`), keyword, created period (last 7, 30, 90 days, custom). Applied filters appear as removable
  chips and live in the URL.
- Table: name with format label (icon plus text, never icon alone), latest version, one-line
  description, updated (relative, with the absolute time in a `<time datetime>` element), number of
  repositories. Sort by name or recently updated.
- Paging with an explicit "Load more" button; no infinite scroll.
- Tabs: all packages; recently viewed (last 10 opened, browser storage keyed by user); saved views
  (at most 8, browser storage; rename, delete, make default). Browser storage access is wrapped so
  that blocked storage only hides these tabs.
- Each row is a link, so it opens in a new tab with the usual browser gestures.
- Empty states: no packages at all (link to client setup); no match (clear filters).

Acceptance criteria:

- After publishing three npm packages and one NuGet package, `format=nuget` shows one row, and
  reloading the page keeps the filter.
- With browser storage disabled, the page works and the two browser-storage tabs are hidden.

### R-UI-007 · Package details and versions
Priority: **MVP** · Covers: UI-009, ART-049

- `/packages/{format}/{name…}` resolves through `GET /api/v1/packages/lookup`.
- Header: name, format, selected version (default: the latest stable visible version; the latest
  pre-release when no stable one exists), published time, licenses, download count, the
  repositories that contain it.
- Version selector: combobox with type-to-filter and an "include pre-releases" switch that follows
  the format's version rules (npm SemVer pre-release tags, NuGet SemVer 2.0 pre-release labels). An
  "All versions" tab lists versions with published time, size, repositories and downloads.
- Tabs: README (default when present, R-UI-008); Metadata (format-specific: npm dist-tags and
  dependency kinds, NuGet target frameworks and dependency groups, authors, project URL as a text
  link with an external-link marker); Files (files of the version with size, SHA-256 copy button,
  download); Repositories (each with "show in browser"); Owners (R-API-048, P1); Builds (P1).
- Install panel (R-UI-009) beside the tabs, below them on narrow screens.
- Opening the page records it in "recently viewed".
- Links taken from package metadata (npm `homepage`, `repository.url`, `bugs.url`; NuGet
  `projectUrl`, `licenseUrl`, the `repository` URL) MUST be rendered as links only when their
  scheme is `http` or `https`, with `rel="nofollow noopener noreferrer"`; any other value is shown
  as text. Package authors set these values, and Angular's URL sanitizer is a second layer, not
  the rule.

Acceptance criteria:

- For `@team-a/ui` with versions `1.3.0`, `1.4.0` and `1.5.0-beta.1`, the page opens on `1.4.0`;
  switching on pre-releases offers `1.5.0-beta.1`.
- The Files tab's SHA-256 equals `shasum -a 256` of the downloaded tarball.
- A package whose `homepage` is `javascript:alert(1)` shows the value as text, and no `a` element
  carries it.

### R-UI-008 · Sanitized README rendering
Priority: **MVP** · Covers: UI-009

Rendering happens on the server (R-API-029), so one renderer and one sanitizer policy serve every
client.

- Markdown is CommonMark plus GitHub-style tables, strikethrough, autolinks and task lists. Raw HTML
  inside Markdown passes through the sanitizer.
- Sanitizer allowlist (normative minimum):

| Allowed | Rule |
|---|---|
| Elements | `h1`–`h6`, `p`, `br`, `hr`, `blockquote`, `ul`, `ol`, `li`, `dl`, `dt`, `dd`, `pre`, `code`, `kbd`, `samp`, `em`, `strong`, `del`, `ins`, `sup`, `sub`, `a`, `img`, `table`, `thead`, `tbody`, `tfoot`, `tr`, `th`, `td`, `details`, `summary`, and disabled task-list checkboxes |
| `href` on `a` | Schemes `http`, `https`, `mailto`, and in-page `#` fragments; relative links that cannot be resolved are reduced to their text; links get `rel="nofollow noopener noreferrer"` and no `target` |
| `img` | `src` per the image policy (Q-UI-6; default: external images blocked and replaced by a link "External image: <alt>"); `data:` sources dropped; sources that resolve to trove's own origin, relative or absolute, dropped as well, except image URLs the renderer itself generates for files embedded in the package, so a README cannot make the viewer's browser send authenticated requests to package or file endpoints ([authorization.md](authorization.md) R-AUTHZ-008); `alt`, `width`, `height` |
| `id`, `name` | Kept only when prefixed `user-content-` (prevents DOM clobbering) |
| `class` | Only `language-*` on `code` |
| Table attributes | `align` with `left`, `center`, `right`; `colspan`, `rowspan` |
| `open` on `details` | Kept |

  Everything else is removed: `script`, `style`, `iframe`, `object`, `embed`, `form`, `input` (except
  the checkboxes above), `svg`, `math`, `base`, `meta`, `link`, `style` attributes, event-handler
  attributes, and `javascript:`, `vbscript:` and `data:` URLs.
- Limits: source at most `ui.readme.max_source_bytes`, rendered HTML at most 1 MiB, rendering at most
  2 seconds; beyond that the README is truncated with a notice. These limits come on top of the
  limits for untrusted package content of R-ARCH-028.
- The client binds the HTML with `[innerHTML]`, so Angular's sanitizer runs a second time; the
  Trusted Types CSP of R-UI-001 is the third layer.
- In-page links to `#user-content-…` scroll within the README; code blocks scroll horizontally.
- Library candidates (pinned once in `go.mod`): [goldmark](https://github.com/yuin/goldmark) for
  Markdown and [bluemonday](https://github.com/microcosm-cc/bluemonday) for sanitizing.

Configuration:

```yaml
ui:
  readme:
    max_source_bytes: 524288     # default (proposed), 512 KiB
    external_images: block      # default (proposed); allow | block, see Q-UI-6
```

Acceptance criteria:

- A fixture corpus of XSS vectors (script tags, event-handler attributes, `javascript:` links, SVG
  with script, `<form>`, `<base>`, `<style>`, `id="__proto__"`) rendered by the server contains none
  of them in executable form; a Playwright test displays each fixture and observes no dialog, no
  request to a foreign origin and no CSP violation.
- A README with a badge image from an external host shows "External image: build status" as a link
  while `externalImages` is `block`.
- A README containing `![x](/api/npm/npm-remote/left-pad)` or
  `<img src="https://trove.example.com/files/generic-local/x.png">` renders without an `img`
  element for either source, and viewing it causes no request to those paths.

### R-UI-009 · Install command panel
Priority: **MVP** · Covers: UI-010

- Client picker per format (npm, pnpm, Yarn; .NET CLI, NuGet CLI, `PackageReference` XML); a
  repository picker when the version exists in more than one repository (default: a virtual
  repository containing it, else the first local one).
- The command comes from the install snippets of R-API-030 with package and version. A copy button
  copies it; a live region announces "Copied".
- npm and NuGet install commands carry no credentials; the panel links to client setup for
  configuring the client once.
- Commands longer than three lines are collapsed with an expander.

Examples: `npm install @team-a/ui@1.4.0`, `dotnet add package Contoso.Logging --version 2.3.1`,
`<PackageReference Include="Contoso.Logging" Version="2.3.1" />`.

Acceptance criteria:

- Copying the npm command for `@team-a/ui@1.4.0` and running it in a project configured by the
  client setup installs that version.

### R-UI-010 · File tree browser
Priority: **MVP** · Covers: UI-011, ART-040, ART-041

- Two panes: tree and details; stacked with back navigation on narrow screens. The selection is the
  URL (`/browse/{repo}/{path…}`); a deep link expands the ancestors.
- Tree root: the readable repositories, sortable by class (virtual, local, remote) and key,
  filterable by key text, format and class. Children load lazily through
  `/api/v1/repositories/{repoKey}/children` (R-API-025) in pages of `ui.browser.page_size` with a
  "Load more" entry.
- A virtual repository's children are its members in resolution order, labelled "resolves from";
  the tree does not merge member contents. A remote repository shows its cached content, labelled
  as cache.
- Details tabs: General (path, kind, size, creation, digests with copy buttons, media type, download,
  link to the package version when the file belongs to one); Statistics (downloads); Properties (P1,
  R-API-026); Effective permissions (P1, R-API-034, for callers with `policies:read` or `manage`);
  API (the item's JSON and the equivalent `curl` command, INV-17).
- The tree follows the WAI-ARIA tree pattern (roles `tree` and `treeitem`, `aria-expanded`,
  `aria-level`, `aria-setsize` and `aria-posinset` with lazy loading, arrow keys, Home and End,
  type-ahead), built on the component library of Q-UI-1.

Configuration:

```yaml
ui:
  browser:
    page_size: 200   # default (proposed)
```

Acceptance criteria:

- With the keyboard only, a user expands `npm-local`, reaches `@team-a/ui/-/ui-1.4.0.tgz`, opens its
  details and downloads it.
- A folder with 1,000 files shows the first page and loads the rest with "Load more".

### R-UI-011 · Item actions
Priority: **MVP** · Covers: UI-012, UI-016

| Action | Applies to | Needs | Priority |
|---|---|---|---|
| Download | File | `read` | MVP |
| Copy path, copy download URL | File, folder | `read` | MVP |
| Delete | File, folder | `delete` | MVP |
| Client setup | Repository | `read` | MVP |
| Reindex | Repository | `manage` | P1 |
| Invalidate cache | Remote repository | `manage` or `delete` | P1 |
| Copy, move | File, folder | R-API-027 | P1 |
| Edit properties | File, folder | `annotate` | P1 |
| Deploy | Toolbar | `deploy` | P1 |

- Actions are reached through an actions button on the node and through the keyboard (context-menu
  key, Shift+F10), never through right-click alone.
- Deleting a file asks for confirmation naming the path. Deleting a folder shows the number of
  items; above 100 items the user types the folder name to confirm. When the trash can is enabled,
  the dialog says the items go there for the retention period. Folder deletion shows the job's
  progress (R-UI-042).

Acceptance criteria:

- Deleting a folder with 150 items requires typing its name; the tree no longer shows it after the
  job succeeds.

### R-UI-012 · Deploy dialog
Priority: **P1** · Covers: UI-013

- Target repository: those where the caller holds `deploy` and raw deploys are accepted
  (R-API-025). For npm and NuGet repositories the dialog explains native publishing and links to
  the publish tab of the client setup.
- Fields: target path (prefilled with the selected folder), files (a drop zone plus a "Choose files"
  button, so dragging is never required), overwrite (only with `overwrite`).
- The size limit `ui.deploy.max_upload_bytes` is checked before the upload starts; larger files are
  pointed to native clients or `curl`.
- Uploads use `PUT /files/…`. Because the fetch backend reports no upload progress, uploads go
  through an XHR-based `HttpClient` instance (`withXhr()`) to show progress.
- Files up to 256 MiB are hashed in the browser (Web Crypto SHA-256) and sent with `Repr-Digest`;
  larger files rely on the server's own digest.
- Several files upload one after another with per-file status; "Cancel" aborts the current one.

Configuration:

```yaml
ui:
  deploy:
    max_upload_bytes: 104857600   # default (proposed), 100 MiB; 0 = unlimited
```

Acceptance criteria:

- Uploading a 10 MiB file shows progress and ends with the file in the tree; a file above the limit
  is refused before any request.

### R-UI-013 · Copy and move dialog
Priority: **P1** · Covers: UI-014

- Target repository picker (same format; local, or virtual with a default deployment target;
  `deploy` required), target path (default: the source path) with a live preview of the resulting
  full path, conflict handling, "Dry run" showing the report of `/api/v1/content/copy` or `/move`,
  then "Copy" or "Move".
- Large operations show job progress (R-UI-042).

Acceptance criteria:

- The preview shows `npm-release/@team-a/ui/-/ui-1.4.0.tgz` before execution, and that is where the
  file ends up.

### R-UI-014 · Trash can
Priority: **P1** · Covers: UI-018

- `/trash` and, when the trash can is enabled and the caller sees entries, a last "Trash can" node in
  the tree.
- Table: path, original repository, deletion time and actor, time until purge; filters by repository
  and path.
- Restore dialog: original location by default, optional other repository and path, conflict
  handling (`/api/v1/trash-can/restorations`). Purge of selected entries with confirmation. "Empty
  trash can" (administrators) requires typing `empty`.

Acceptance criteria:

- A deleted file appears in the trash can and downloads again after restore.

### R-UI-015 · Search
Priority: **P1** · Covers: UI-019, UI-020, SRCH-048, SRCH-058

- Header search: type selector (packages by default, files, builds when enabled) and a query field;
  Enter opens `/search?type=…&q=…`.
- Package suggestions from two characters on (`namePrefix`, five results, 250 ms debounce) as an
  ARIA combobox with listbox; Escape closes it.
- File search form: name, path glob, checksum, property, repositories, date ranges, "not downloaded
  since"; each field maps to one parameter of R-API-028 and to the URL.
- Results table: path, repository, size, modified; actions "show in browser", download, copy URL.
- From the browser, a search starts scoped to the selected repository and folder (a removable scope
  chip).
- Ctrl+K (Cmd+K on macOS) focuses the search. The single-key shortcut `/` works only when the user
  turned keyboard shortcuts on (WCAG 2.1.4).

Acceptance criteria:

- Searching files by the SHA-256 of a known tarball lists it with a working "show in browser" link.
- With shortcuts off, typing `/` in the page body does not move focus.

### R-UI-016 · Client setup dialog
Priority: **MVP** · Covers: UI-021, UI-022, UI-023

- Route `/setup`, reachable from the user menu, a repository row, a repository node in the browser,
  the package details and empty states.
- One page with progressive steps:
  1. format (npm and NuGet in the MVP; further formats appear as they ship, from
     `GET /api/v1/formats`);
  2. client (npm, pnpm, Yarn Berry, Yarn classic; .NET CLI, NuGet CLI, Visual Studio,
     `nuget.config`);
  3. repository: readable repositories of that format, virtual repositories marked as recommended,
     publish availability shown per repository;
  4. credentials, one of:
     - **Reference an environment variable** (default): snippets reference `TROVE_TOKEN`
       (R-API-030 `credentials=env`); no secret appears on screen; a link leads to creating a token.
     - **Create a token for this setup**: creates a personal token (R-API-032) described as "Client
       setup: <client>, <repository>, <date>", scoped to the chosen repository with `read` (plus
       `deploy` when the user wants the publish tab and holds `deploy`), expiring after
       `ui.client_setup.token_ttl` (bounded by the PAT maximum of R-AUTH-040). Recent authentication
       applies. The token is shown once and embedded in the snippets.
     - **Use a token I already have**: a password-type field with a reveal toggle; pasting is
       allowed. The value stays in the browser; the UI checks its prefix and its CRC32 checksum
       (INV-8) locally and never sends it to the server.
  5. tabs Configure, Install, Publish with the snippets; each snippet shows its target file name
     and has a copy button; whole configuration files (`.npmrc`, `nuget.config`) can be downloaded.
- Anonymous viewers of anonymously readable repositories skip the credentials step and get snippets
  without authentication lines.
- The browser replaces `{{token}}` in the component only; the composed text is never stored in a
  service, browser storage or the URL and is discarded when the route is left (R-UI-043).
- A virtual repository without a default deployment target shows, instead of the publish tab, an
  explanation and a link to its local members.

Acceptance criteria:

- Playwright: choosing npm, `npm-all` and "Create a token for this setup", downloading `.npmrc` and
  running `npm install @team-a/ui` with it in a `node:24` container succeeds.
- The default choice produces a `.npmrc` containing the literal `${TROVE_TOKEN}`.
- Pasting a token with a wrong checksum shows a validation error and sends no request.

### R-UI-017 · npm client instructions
Priority: **MVP** · Covers: UI-024

Snippets for `npm-all` at `https://trove.example.com` (# example). The registry URL keeps its
trailing slash; the credential key is the same URL without scheme, which is how npm matches
credentials to a registry ([npmrc](https://docs.npmjs.com/cli/v11/configuring-npm/npmrc)); npm
expands `${…}` in `.npmrc`. The protocol behind the URLs is owned by
[formats/npm.md](formats/npm.md), which contributes these templates.

Project `.npmrc` for npm, pnpm and Yarn classic:

```ini
registry=https://trove.example.com/api/npm/npm-all/
//trove.example.com/api/npm/npm-all/:_authToken=${TROVE_TOKEN}
```

Scoped variant (only `@team-a` packages from trove):

```ini
@team-a:registry=https://trove.example.com/api/npm/npm-all/
//trove.example.com/api/npm/npm-all/:_authToken=${TROVE_TOKEN}
```

User-level configuration through the CLI (the shell expands the variable, so the token is written
into `~/.npmrc`):

```shell
npm config set registry=https://trove.example.com/api/npm/npm-all/
npm config set "//trove.example.com/api/npm/npm-all/:_authToken=${TROVE_TOKEN}"
```

Yarn Berry `.yarnrc.yml` ([yarnrc](https://yarnpkg.com/configuration/yarnrc); Yarn expands
`${…}`):

```yaml
npmRegistryServer: "https://trove.example.com/api/npm/npm-all/"
npmAlwaysAuth: true
npmAuthToken: "${TROVE_TOKEN}"
```

| Client | Install | Publish (local repository, or virtual with default deployment target) |
|---|---|---|
| npm | `npm install <package>` | `npm publish --registry https://trove.example.com/api/npm/npm-local/`, or `"publishConfig": {"registry": "…/api/npm/npm-local/"}` in `package.json` |
| pnpm | `pnpm add <package>` | `pnpm publish` with the same `publishConfig` |
| Yarn Berry | `yarn add <package>` | `npmPublishRegistry: "…/api/npm/npm-local/"` in `.yarnrc.yml`, then `yarn npm publish` |
| Yarn classic | `yarn add <package>` | `yarn publish --registry …/api/npm/npm-local/` |

Acceptance criteria (CI end-to-end, `node:24` container, real token in `TROVE_TOKEN`):

- With the project `.npmrc`, `npm view <package> version` and `npm install <package>` succeed;
  `npm publish` of a fixture to `npm-local` succeeds.
- With `.yarnrc.yml`, `yarn add <package>` (Yarn Berry) succeeds; `pnpm add <package>` succeeds with
  the `.npmrc`.

### R-UI-018 · NuGet client instructions
Priority: **MVP** · Covers: UI-025

.NET CLI (`--store-password-in-clear-text` is required on Linux and macOS, where NuGet cannot
encrypt stored passwords):

```shell
dotnet nuget add source "https://trove.example.com/api/nuget/nuget-all/v3/index.json" \
  --name trove --username "<username>" --password "${TROVE_TOKEN}" --store-password-in-clear-text
```

Repository `nuget.config`. `%VAR%` references work on every operating system, and `<clear />`
removes other sources, so every package resolves through trove (the virtual repository includes a
nuget.org remote) and a same-named package on a public source cannot slip in
([nuget.config reference](https://learn.microsoft.com/en-us/nuget/reference/nuget-config-file)):

```xml
<?xml version="1.0" encoding="utf-8"?>
<configuration>
  <packageSources>
    <clear />
    <add key="trove" value="https://trove.example.com/api/nuget/nuget-all/v3/index.json" />
  </packageSources>
  <packageSourceCredentials>
    <trove>
      <add key="Username" value="<username>" />
      <add key="ClearTextPassword" value="%TROVE_TOKEN%" />
    </trove>
  </packageSourceCredentials>
</configuration>
```

NuGet CLI: `nuget sources add -Name trove -Source "https://trove.example.com/api/nuget/nuget-all/v3/index.json" -Username "<username>" -Password "<token>"`.
Visual Studio: add a package source with name and service-index URL in the NuGet package manager
options; at the credential prompt enter any user name and the token as password.

| Task | Command |
|---|---|
| Install | `dotnet add package <id> --version <version>`; `dotnet restore` |
| Publish | `dotnet nuget push <id>.<version>.nupkg --source "https://trove.example.com/api/nuget/nuget-local/v3/index.json" --api-key "${TROVE_TOKEN}"` (the token travels as `X-NuGet-ApiKey`, INV-8; a `.snupkg` next to the package is pushed with it, [formats/nuget.md](formats/nuget.md)) |

The username is not checked (INV-8); the snippet fills in the caller's name for readability.
PowerShell and Chocolatey instructions need NuGet v2, which is P2 (INV-14), and are not offered in
the MVP.

Acceptance criteria (CI end-to-end, .NET SDK container, real token in `TROVE_TOKEN`):

- With the generated `nuget.config`, `dotnet restore` of a fixture project that references a package
  from `nuget-local` and one from nuget.org succeeds.
- `dotnet nuget push` of a fixture package to `nuget-local` succeeds, and the package is listed by
  `GET /api/v1/packages?format=nuget`.

### R-UI-019 · Instructions for further formats
Priority: **P1** · Covers: UI-026

Each format page that ships contributes snippet templates (R-API-030) and acceptance commands for
its native clients (for example `docker login`, Maven `settings.xml` with `${env.TROVE_TOKEN}`,
`pip` index configuration, `GOPROXY` with `.netrc`, `helm repo add` and OCI registry login). The
dialog is data-driven: a new format needs a format label, an icon and translated snippet texts, no
new screen.

Acceptance criteria: per format, on its page; the dialog lists a format as soon as its templates
are registered.

### R-UI-020 · Repository list
Priority: **MVP** · Covers: UI-030, REPO-007

- Table: key, format, class, description, client URL with copy button, created. Filters: format,
  class, text. Sort by key or creation. "Recently viewed" (browser storage, 20).
- Row actions: edit, browse, client setup, reindex (P1), invalidate cache (remote, P1), delete.
  Delete requires typing the key, offers the `detach` choice of
  [repositories-and-proxy.md](repositories-and-proxy.md) when other repositories reference it, and
  shows that deletion runs as a job.
- "Create repository" (when the caller may create repositories) opens the wizard.

Acceptance criteria:

- Filtering by `format=npm` and `class=remote` shows only npm remote repositories; deleting one shows
  the job's progress until it disappears.

### R-UI-021 · Repository creation wizard and edit form
Priority: **MVP** · Covers: UI-031

- Steps: class, format (cards from `GET /api/v1/formats`), basic settings, class-specific settings,
  review.
- Settings forms render from the format's settings JSON Schema (R-ARCH-004, R-ARCH-008); a format
  adds a panel only for interactions a schema cannot express.
- Key: live check of the INV-5 pattern and of availability (a `GET` of the key answering 404).
- Remote: upstream presets per format (`https://registry.npmjs.org/`,
  `https://api.nuget.org/v3/index.json`), write-only credentials, metadata and negative cache
  periods, offline switch (INV-11), and "Test connection" (`POST /api/v1/upstream-tests`) with the
  result inline.
- Virtual: member editor (R-UI-022) and default deployment target.
- Review: summary plus "Show as API request" with the JSON body and the equivalent `curl` command
  (INV-17).
- Create sends the `Idempotency-Key` generated when the wizard opened.
- Edit form: the same sections as tabs; key, class and format read-only; save sends PATCH with
  `If-Match`; 412 opens the conflict dialog (R-UI-042); unsaved changes are guarded on navigation.
- Server field errors (`errors[].pointer`) are shown at their fields and in an error summary.

Acceptance criteria:

- Creating `npm-remote` with the npm preset and "Test connection" succeeds; double-clicking "Create"
  creates one repository.
- Saving an edit after another administrator saved first opens the conflict dialog.

### R-UI-022 · Virtual repository member editor
Priority: **MVP** · Covers: UI-032

- Two lists: available (same format, excluding the repository itself and anything that would create
  a cycle) and selected (ordered). Buttons Add, Remove, Move up, Move down, Move to top work with
  keyboard and single pointer (WCAG 2.5.7); drag and drop is an optional extra.
- An "Effective resolution order" view flattens nested virtual repositories per
  [repositories-and-proxy.md](repositories-and-proxy.md); `GET …/resolution?path=` explains a sample
  path.
- Default deployment target: picker over the selected local repositories, or none.
- A live region announces each change ("npm-remote moved to position 2 of 3").
- A cycle reported by the server (422 with pointer) is shown on the offending member.

Acceptance criteria:

- With the keyboard only, a user adds two members, reorders them and saves; the API shows that order.

### R-UI-023 · Quick repository setup
Priority: **P1** · Covers: UI-033, REPO-008

Administrators pick formats and a prefix; the preview shows the keys that
`POST /api/v1/repositories:quickSetup` ([repositories-and-proxy.md](repositories-and-proxy.md))
will create (local, remote with the public upstream preset, virtual over both), then creates them in
one request and offers client setup for the virtual repository.

Acceptance criteria:

- Quick setup for npm with prefix `team-a` creates the three repositories of the owner's naming
  rule, and `npm install` through the virtual one fetches a public package.

### R-UI-024 · Users and system roles
Priority: **MVP** · Covers: UI-035, UI-036

- List: username, display name, email, origin (identity provider, administrator-created, SCIM,
  break-glass), status, last login; filters text, status, origin, group, administrator.
- Detail: identities (provider and subject, read-only and copyable); attributes from the identity
  provider, read-only and labelled as such; status with "Disable" (choice to revoke tokens) and
  "Enable"; group memberships (identity-provider-sourced read-only with a badge, manual ones
  editable); system roles through role bindings (R-API-033) with the last-administrator guard; the
  user's tokens (list, revoke all) and sessions (list, end all); "Explain permissions" (P1); "Audit"
  (audit log filtered on this actor). The break-glass account shows its lock state and "Unlock".
- "Add user" pre-provisions a user linked to an identity-provider identity (R-API-031). No password
  fields exist.
- `/admin/roles` lists the fixed roles of `GET /api/v1/roles` with their bindings and offers bind
  and unbind.

Acceptance criteria:

- Disabling a user with "revoke tokens" makes that user's next API call with a token fail with 401,
  and the audit log shows the change.

### R-UI-025 · Groups
Priority: **MVP** · Covers: UI-037

- List: name, description, origin, member count.
- Editor: name (rename allowed where the owner allows it), description, auto-join, members with
  their sources (manual, identity provider, SCIM) where only manual ones are editable, role bindings
  of the group (read-only with link to `/admin/roles`), the policies naming the group (read-only
  links), "Explain" (P1).
- A group managed by SCIM is read-only with the reason shown.

Acceptance criteria:

- Adding a manual member through the editor makes `GET /api/v1/groups/{name}/members` list the user
  with source `manual`.

### R-UI-026 · Service accounts
Priority: **MVP** · Covers: trove-only

- List and editor: name, description, owners (P1), groups, status with disable and enable.
- Tokens tab: issue (scope and expiry as in R-UI-029, one-time display, recent authentication), list,
  revoke one or all.
- A hint explains when to use one: CI systems without OIDC exchange, long-running automation.

Acceptance criteria:

- A token issued on the Tokens tab authenticates `npm view` against a repository the service account
  may read.

### R-UI-027 · Permission policy list and editor
Priority: **MVP** · Covers: UI-039, UI-040

- List: name, description, principal summary ("3 groups, 1 CI mapping"), resources summary, actions;
  filters by principal (picker), repository and project.
- Editor sections on one page with an in-page table of contents, following the policy model of
  [authorization.md](authorization.md): general (name on create only, description); principals
  (users, groups, service accounts, CI identity mappings, anonymous where allowed) through
  searchable pickers; resources with a live "covers 4 repositories" preview; include and exclude
  patterns with syntax help and a tester field ("Does `@team-a/ui` match?"); actions with a one-line
  explanation each.
- Preview panel fed by `POST /api/v1/authz/validate-policy` (debounced 500 ms): covered repositories,
  errors, warnings.
- Save shows a diff against the loaded revision (principals, resources and actions added or removed),
  then sends PUT with `If-Match`; recent authentication is handled as in R-UI-042.
- Delegated managers (P1) see only the entries the API lets them change.

Acceptance criteria:

- Creating the policy of R-API-033's acceptance test through the editor yields the same stored
  document as creating it through the API.
- The diff before saving lists exactly the added principal when only one was added.

### R-UI-028 · Effective-permission explainer
Priority: **P1** · Covers: IAM-030

- `/admin/permissions/explain` for callers with `policies:read`; the item details tab of R-UI-010
  for repository managers.
- Question form: "Why can [principal] [action] on [repository] [path or package]?", principal kinds
  user, group, service account and CI identity mapping, or a token ID.
- Answer from `POST /api/v1/authz/explain` (R-API-034):
  - decision as icon plus text ("Allowed" or "Denied"), never colour alone, with the reason code;
  - the trace steps as an ordered list (for example: grant from policy `team-web-publish` via group
    `team-web` → ownership check → denied, owned by project 4711), each step linking to the policy,
    mapping or owner list it names;
  - the scope intersection when a token was given.
- "Copy as text" and "Copy as JSON" for support tickets.
- Entry points: user and group details, the policy editor ("Test this policy"), the no-access state
  (R-UI-003), the claim tester.

Acceptance criteria:

- For the deny example of R-AUTHZ-024, the screen names the ownership step and its owners.
- A screen reader reads the steps as an ordered list in evaluation order.

### R-UI-029 · Profile, personal tokens and sessions
Priority: **MVP** · Covers: UI-053, AUTHN-039, AUTHN-040

- Profile (`GET /api/v1/users/me`): identity (read-only, from the identity provider), groups with
  source, system roles, preferences (language, theme, keyboard shortcuts; R-API-021).
- Personal tokens: list with description, scope summary, created, expires and last used; tokens
  expiring within 7 days are marked. Create dialog: description (required), expiry (presets and a
  custom date, bounded by R-AUTH-040), scope (all own permissions, or restricted to repositories and
  actions; grammar of R-AUTH-024). Recent authentication applies. The result is shown once with a
  copy button and the notice that it cannot be shown again. Revoke and rotate with confirmation.
- Sessions (`/api/v1/users/me/sessions`): list with created, last seen, address, user agent and
  "this session"; end one, or end all others.

Acceptance criteria:

- A created token works in `curl -H "Authorization: Bearer …" "$TROVE_URL/api/v1/auth/whoami"`;
  reopening the profile never shows it again; revoking it makes the same call answer 401.
- Ending all other sessions signs out a second browser.

### R-UI-030 · Token administration
Priority: **MVP** · Covers: UI-041, TOK-026

- All tokens (`tokens:read`): type, owner, description, scope, created, expires, last used, created
  via, state; filters type, owner, state, expiring before, unused since; revoke single and selected
  with confirmation.
- CI tokens come from `/api/v1/ci/issuances` with job context (project, pipeline, job, mapping) and
  "Revoke matching" by project or pipeline.
- "Revoke by value": a field to paste a leaked token; the value is sent once to
  `POST /api/v1/tokens/revoke` and cleared.

Acceptance criteria:

- Filtering "unused since 90 days" lists a token never used since its creation 91 days ago (test
  clock).

### R-UI-031 · Authentication settings
Priority: **MVP** · Covers: UI-043

`/admin/settings/auth` renders `GET /api/v1/settings/auth`: anonymous access (with a link to what
the anonymous principal can read), token default and maximum lifetimes per token type, expiry
notices, and the read-only status block (providers and discovery state, break-glass state, session
timeouts, client switches including Basic, SCIM state). YAML-pinned and ceiling-bounded values show
their source. Hiding unauthorized resources (`authz.hide_existence`) is shown read-only. Save sends
`PATCH` with `If-Match`; recent authentication applies.

Acceptance criteria:

- Turning anonymous access on lets an unauthenticated `npm view` read a package that a policy grants
  to the anonymous principal.

### R-UI-032 · Sign-in protection and anonymous access
Priority: **P1** · Covers: UI-038, UI-042

- Break-glass lockout state and threshold (read-only, R-AUTH-038) with "Unlock".
- A summary of what the anonymous principal can read (policies naming it, from
  `GET /api/v1/permission-policies?principal=anonymous`).
- Multi-factor authentication, password expiry and password rules belong to the identity provider
  (INV-7); the screen says so instead of offering controls.

Acceptance criteria:

- After the configured number of failed break-glass logins the account shows as locked, and
  "Unlock" unlocks it.

### R-UI-033 · General settings
Priority: **MVP** · Covers: UI-044

`/admin/settings/general` shows the public base URL and its source (`GET /api/v1/system/info`,
read-only, R-OPS-005), the banner (`/api/v1/system/banner`: enabled, severity, plain text) and
maintenance mode (`/api/v1/system/maintenance`). Server name, date format, logo and login notice are
P2 on [operations-and-observability.md](operations-and-observability.md).

Acceptance criteria:

- An enabled banner appears on every screen for every user within one minute after saving.

### R-UI-034 · Storage and proxy settings
Priority: **P1** · Covers: UI-045

`/admin/settings/storage` renders `/api/v1/settings/storage` (trash can and the other runtime
storage settings of [storage.md](storage.md)) and the global offline mode of
`/api/v1/system/proxy-offline` (confirmation: every remote repository stops fetching), and links to
outbound proxies and client certificates ([repositories-and-proxy.md](repositories-and-proxy.md)).
The UI-only limits (`ui.deploy.max_upload_bytes`, `ui.browser.page_size`) are shown read-only.

Acceptance criteria:

- With offline mode on, `npm view` of an uncached public package through `npm-remote` fails while
  cached packages still install.

### R-UI-035 · CI identity
Priority: **MVP** · Covers: UI-049, CIOIDC-024

- Issuers: name, type, issuer URL, audiences, number of mappings, key status (last fetch or error)
  with "Refresh keys".
- Issuer editor per [gitlab-ci-oidc.md](gitlab-ci-oidc.md); creation validates discovery and keys.
- Mappings per issuer in evaluation order, with conditions summary (`namespace_path in [4711]`,
  `ref_protected = true`), grants summary and maximum token lifetime.
- Mapping editor: conditions with suggestions of GitLab claim names (`project_path`,
  `namespace_path`, `ref`, `ref_type`, `ref_protected`, `environment`, `pipeline_source`,
  `user_login`, …); grants; options; "Test with claims" opens the claim tester prefilled. Warnings
  returned on save are shown.
- Per mapping, "CI snippet" shows the text of `GET /api/v1/ci/issuers/{issuer}/snippets/gitlab-ci`
  for the chosen format (npm or NuGet) with a copy button. This covers the GitLab counterpart of
  CIOIDC-024 ([GitLab ID tokens](https://docs.gitlab.com/ci/secrets/id_token_authentication/)).
- Delegations (P1) and issuances (R-UI-030) are tabs of the issuer.

Acceptance criteria:

- The snippet copied from the screen, used in a GitLab CI job of project `team-a/ui` on a protected
  branch against the test environment, publishes `@team-a/ui`.

### R-UI-036 · Claim tester
Priority: **MVP** · Covers: trove-only

- `/admin/ci/{issuer}/test`. Input: a text area accepting claims as JSON or a compact JWT.
- A pasted JWT is decoded in the browser (base64url header and payload, no signature check). The UI
  replaces the text with the pretty-printed claims, discards the token, and states that only the
  claims are used. If `exp` lies in the future, a warning says the token is still a valid credential
  until then.
- "Insert sample GitLab claims" fills a realistic payload (`iss`, `aud`, `sub` such as
  `project_path:team-a/app:ref_type:branch:ref:main`, `namespace_id`, `namespace_path`,
  `project_id`, `project_path`, `ref`, `ref_type`, `ref_protected`, `pipeline_source`,
  `environment`, `user_login`, `job_id`, `iat`, `nbf`, `exp`) that the user edits, as JSON or in a
  form view.
- "Evaluate" calls `POST /api/v1/ci/issuers/{issuer}/evaluate` and shows the decision trace of the
  owning page: the matched mapping or "No mapping matches; the exchange would fail", every
  candidate with each condition's expected and actual value and a pass or fail mark (icon plus
  text), the resulting grants with templates resolved, the token that would be issued.
- "Explain resulting permissions" opens the explainer (P1) with the mapping as principal.
- Claims are never put into browser storage and are gone when the route is left.

Acceptance criteria:

- Pasting a signed test ID token shows its claims, and the network log shows that only the claims
  JSON was sent.
- Changing `ref_protected` to `false` switches the result to no match and marks that condition.

### R-UI-037 · Audit log
Priority: **MVP** · Covers: OPS-023

- Table from `GET /api/v1/audit/events`: time, actor, action (translated label), target, outcome,
  source address; filters time range (presets and custom), actor, action, category, repository,
  outcome; "Load more".
- Detail panel with the full event and links to actor and target.
- Times in the user's locale with a UTC toggle; export of the current filter as CSV or JSON Lines
  (`/api/v1/audit/events/export`).

Acceptance criteria:

- Changing a policy and filtering the audit log by it shows the event with the before and after of
  the grants.

### R-UI-038 · System status
Priority: **MVP** · Covers: OPS-008

- Cards from `GET /api/v1/system/info` and `/system/replicas`: version and revision; base URL and
  source; maintenance mode; database (server version, schema version against the supported ones);
  replicas (id, version, roles, heartbeat age, status) with a mixed-version warning; scheduled tasks
  (`/system/tasks`: next run, last outcome) with "Run now"; recent jobs (`/system/jobs`) with
  cancel.
- Automatic refresh every 15 seconds with a visible pause control (WCAG 2.2.2).

Acceptance criteria:

- Stopping one of two replicas changes its status on the screen within 40 seconds without a reload.

### R-UI-039 · Webhooks
Priority: **P1** · Covers: UI-046, UI-047

List; editor (key, URL, event types from `/api/v1/webhooks/event-types`, repository selectors,
write-only secrets with "Replace" and signing-secret rotation, custom headers, enabled); "Test" with
the receiver's status, latency and body excerpt; status (`/{key}/status`); deliveries with request
and response details and "Redeliver". Semantics:
[operations-and-observability.md](operations-and-observability.md).

Acceptance criteria:

- "Test" against a receiver that answers 500 shows the status and body excerpt.

### R-UI-040 · Builds
Priority: **P1** · Covers: UI-028, UI-029

Builds list, runs list, run details (number, start, duration, CI link, status; tabs modules,
artifacts, dependencies, version control, environment with values masked by the server, raw JSON),
and promotion with dry run (R-API-042).

Acceptance criteria: defined with the owning page of Q-API-5.

### R-UI-041 · Projects
Priority: **P1** · Covers: UI-003, UI-050, UI-051

Follows the reduced project model of R-AUTHZ-026 in [authorization.md](authorization.md):

- A project filter in the header appears when projects exist and the caller administers at least
  one (or is administrator); the selection is stored as `projectFilter` (R-API-021) and filters the
  repository, policy, webhook and retention lists through `project=`.
- Projects overview: key, display name, administrators, number of repositories, switches.
- Create and edit dialog (administrators): key (`^[a-z][a-z0-9-]{1,31}$`, immutable), display name,
  description, `managePolicies`, `manageRepositories`, administrators (users and groups), assigned
  repositories. Delete is offered only for projects without repositories.

Acceptance criteria:

- An administrator creates project `team-a` with one repository and one administrator group; a
  member of that group sees the project in the filter and can create a project policy.

### R-UI-042 · Errors, conflicts and feedback
Priority: **MVP** · Covers: trove-only

- One HTTP interceptor (R-ARCH-004 `core/http`) maps problem `code` values to translated messages and
  `errors[].pointer` to form controls. Unknown codes show the English `detail` and the `requestId`
  with a copy button ("give this ID to your administrator").
- 412: a conflict dialog "Changed by someone else since you opened it", showing their version against
  yours field by field, with "Reload (discard mine)", "Overwrite" (re-sends with the new ETag after
  showing the diff) and "Copy my changes".
- 401 `reauthentication_required`: a dialog explains why, keeps the draft of non-secret forms in
  session storage keyed by route, and starts the re-authentication flow of R-AUTH-011; after return
  the draft is restored and the action can be repeated. Forms holding secrets are not kept.
- 429: idempotent GETs retry once after `Retry-After`; otherwise a message with the wait time.
- 503: a page-level notice with "Retry"; in maintenance mode the notice shows the operator's
  message.
- Confirmations use a polite live region (`role="status"`), errors `role="alert"`; notifications
  with actions stay until dismissed.
- Jobs show a progress component that polls the job (R-API-015) and announces progress at most every
  10 seconds.

Acceptance criteria:

- A forced 412 in a Playwright test opens the conflict dialog, and "Overwrite" stores the user's
  version.
- An unknown `code` shows the request ID.

### R-UI-043 · Secrets in the browser
Priority: **MVP** · Covers: trove-only

- Token values (created or pasted) live only in component-local signals. They never enter shared
  services, the URL, `localStorage`, `sessionStorage`, IndexedDB, the browser history, console output
  or error reports, and are cleared when their dialog closes or the route changes.
- Secret inputs are `type="password"` with a reveal toggle, `autocomplete="off"` and
  `spellcheck="false"`; copying uses the Clipboard API on an explicit click only.
- Write-only fields (upstream passwords, webhook secrets) show whether a value is set and a "Replace"
  action; they are never prefilled.
- Configuration files with an embedded token are offered for download through a Blob URL that is
  revoked right after the click.
- API answers carry `Cache-Control: no-store` (R-API-017), which keeps secret-bearing answers out of
  the browser's caches.

Acceptance criteria:

- After creating a token and closing the dialog, a test inspects `localStorage`, `sessionStorage`,
  IndexedDB, the URL history and the console log and finds no `trv_` value.

### R-UI-044 · Accessibility
Priority: **MVP** · Covers: trove-only

Every screen meets [WCAG 2.2](https://www.w3.org/TR/WCAG22/) level AA. Design rules that follow from
specific criteria:

| Criterion | Rule in trove |
|---|---|
| 1.3.1 Info and relationships | Data tables are `<table>` with `<th scope>`; landmarks; one `h1` per screen, no skipped heading levels |
| 1.4.1 Use of color; 1.4.3 and 1.4.11 contrast | Status, decision and diff marks carry icon and text; contrast verified in both themes |
| 1.4.10 Reflow | Usable at 320 CSS px; code blocks and wide tables scroll inside their own region |
| 1.4.13 Content on hover or focus | Tooltips are dismissible and hoverable; no information exists only in a tooltip |
| 2.1.1 Keyboard | Tree, member editor, reordering, file selection ("Choose files" button) and action menus work by keyboard |
| 2.1.4 Character key shortcuts | Single-key shortcuts are off by default (R-UI-015) |
| 2.2.1 Timing adjustable; 2.2.2 Pause | Session expiry warning (R-UI-002); auto-refresh can be paused (R-UI-038) |
| 2.4.2 Page titled; 2.4.3 Focus order | Title per route; focus moves to the new `h1` on navigation and into dialogs on open, and returns to the trigger on close |
| 2.4.11 Focus not obscured | `scroll-padding-top` keeps focused elements clear of the sticky header |
| 2.5.7 Dragging movements | Every drag interaction has a button alternative (R-UI-022) |
| 2.5.8 Target size | Interactive targets at least 24 × 24 CSS px |
| 3.3.1 and 3.3.3 Errors | Field errors as text next to the field plus a summary at the top |
| 3.3.7 Redundant entry | The wizard never asks twice for what was entered in an earlier step |
| 3.3.8 Accessible authentication | No cognitive tests; pasting allowed in every token field; login happens at the identity provider |
| 4.1.2 Name, role, value; 4.1.3 Status messages | Custom widgets built on `@angular/aria` or the CDK accessibility patterns ([Angular Aria](https://angular.dev/guide/aria/overview)); status messages through live regions |

Process:

- Automated checks with axe-core in Playwright
  ([Playwright accessibility testing](https://playwright.dev/docs/accessibility-testing)) on every
  route, in both themes and both locales; violations of impact "serious" or "critical" fail the
  build, others are triaged.
- Before each release, a manual screen-reader pass (NVDA with Firefox, VoiceOver with Safari) of
  five journeys: sign in; find a package and copy its install command; client setup with a new
  token; create a remote repository; edit a policy.

Acceptance criteria:

- The axe run reports zero serious or critical violations on all routes.
- The five manual journeys are recorded as passed in the release checklist.

### R-UI-045 · Internationalization
Priority: **MVP** · Covers: trove-only

- English (source and default) and German ship with the MVP. Every UI text is translatable; no
  sentence is concatenated from fragments; counts use ICU plural and select messages ("1 Version",
  "3 Versionen").
- Mechanism per Q-UI-5; recommended: Angular's compile-time i18n (`@angular/localize`,
  [Angular i18n](https://angular.dev/guide/i18n)) with one build per locale, served at `/ui/en/` and
  `/ui/de/` (R-UI-001).
- Locale choice: the user's preference (R-API-021), then `Accept-Language`, then
  `ui.default_locale`. Switching the language in the user menu stores the preference and reloads the
  same route under the other locale.
- Dates, numbers, file sizes (binary units such as MiB with the locale's decimal separator) and
  relative times are formatted with Angular's locale data and `Intl`.
- API texts stay English; the UI translates problem codes (R-UI-042) and shows the English `detail`
  for unknown ones.
- Not translated: user content (READMEs, descriptions), identifiers, and snippet code, which is
  marked `translate="no"`.
- `<html lang>` matches the locale (WCAG 3.1.1). Layouts tolerate 35 % longer German text.
- A missing translation fails the build (`i18nMissingTranslation: error`).
- An English–German glossary of product terms (for example "permission policy" →
  "Berechtigungsrichtlinie"; "repository" stays "Repository") is kept with the translation files.

Configuration:

```yaml
ui:
  default_locale: en      # default (proposed)
  locales: [en, de]      # default (proposed)
```

Acceptance criteria:

- A browser with `Accept-Language: de-DE` opening `/ui/` lands on `/ui/de/packages` with German
  texts and German number formatting.
- Removing one German translation unit makes the build fail.

### R-UI-046 · State management with signals
Priority: **MVP** · Covers: trove-only

Builds on the workspace rules of R-ARCH-004 (standalone, zoneless, signals; generated types and
thin hand-written clients in `src/app/api/`):

- Signals are the reactive state primitive in components and services. RxJS is used only where
  Angular APIs emit observables (router events, `HttpClient` internals) and is converted with
  `toSignal` at that edge.
- Server state is read with `httpResource()` (stable since v22,
  [Angular guide](https://angular.dev/guide/http/http-resource)) keyed by signals (route parameters,
  filters), using request descriptors built by the clients in `src/app/api/`. Mutations call those
  clients from a feature service and then reload the affected resources. There is no global cache of
  server data.
- The clients use `HttpClient` with the generated types (Q-API-1), so the interceptors (CSRF,
  problem mapping, request ID) apply to every call, and no component calls `/api/v1` directly.
- URL state (filters, sort, tab, version) lives in query parameters and reaches components as signal
  inputs through the router's component input binding; changing a filter navigates with
  `replaceUrl`.
- Shared state of a feature lives in an injectable store service exposing read-only signals and
  methods; no third-party state library in the MVP. A session store holds `whoami`, `authz/me` and the
  check cache, and is reset on 401, on 403 for a cached decision, and on sign-out.
- Forms use Signal Forms (stable since v22,
  [Angular guide](https://angular.dev/guide/forms/signals/overview)) with validators derived from the
  OpenAPI constraints (patterns, lengths) where the generator emits them.
- `computed` and `linkedSignal` derive state; `effect` is reserved for side effects at the boundary
  to non-Angular APIs (browser storage, focus, clipboard).
- Lint rules enforce: no `subscribe(` in components, no `NgZone`, no `bypassSecurityTrust*`, no direct
  `localStorage` access outside the storage wrapper.

Acceptance criteria:

- The lint configuration contains the four rules and CI fails on a violation.
- Changing a filter on the packages list issues exactly one API request and updates the URL.

### R-UI-047 · Testing
Priority: **MVP** · Covers: trove-only

| Tier | Tool | Scope | When |
|---|---|---|---|
| Unit | Vitest through Angular's unit-test builder | Pure logic: token substitution, JWT decoding, token checksum validation, pattern preview, problem mapping, stores | Every pull request |
| Component | Vitest browser mode with `TestBed`; API answers from the OpenAPI examples | Screens and widgets including keyboard behavior | Every pull request |
| Contract | TypeScript compile against the generated types; mock answers validated against the document | Drift between UI and API | Every pull request |
| End-to-end | Playwright against the real binary with PostgreSQL, an S3-compatible store (MinIO), a test OIDC provider and a test CI token issuer | Journeys: sign-in, client setup followed by real `npm` and `dotnet` commands, repository wizard, policy edit, claim tester, token create and revoke, audit entry | Smoke on every pull request; full nightly |
| Accessibility | axe-core in Playwright, plus the manual pass of R-UI-044 | Every route × theme × locale | Every pull request; manual per release |
| Internationalization | Build with missing-translation error | Both locales | Every pull request |
| Security | README XSS corpus; any `securitypolicyviolation` event fails an end-to-end test | Renderer, CSP | Every pull request |

- End-to-end tests prepare data through the API, not through the UI (INV-17), and run in Chromium on
  pull requests and in Chromium, Firefox and WebKit nightly.
- Line coverage of services and pure logic at least 80 % (# default (proposed)).
- Test tool versions are pinned once in the lockfile and used by local runs and CI alike.

Acceptance criteria:

- The CI workflow runs every tier at the stated cadence, and a deliberately broken snippet template
  fails the end-to-end client-setup test.

## Deferred

P2, Later and Out-of-scope entries of the three catalog pages this page draws on, plus P1 entries
whose owning page defers them. None has a requirement here.

<details>
<summary>P1 catalog entries deferred by an owning page</summary>

- ADMAPI-019 · Global role operations — custom global roles are P2 per R-AUTHZ-026; the fixed system
  roles are listed by `GET /api/v1/roles` (R-API-033)
- ADMAPI-066 · Project role operations — P2 per R-AUTHZ-026 (reduced project model)
- ADMAPI-068 · Environment operations — P2 per R-AUTHZ-026

</details>

<details>
<summary>Content operations (API)</summary>

- API-006 · Last-modified item under a path — P2
- API-012 · Compute SHA-256 for legacy items — P2 (trove computes SHA-256 on every upload, INV-4)
- API-015 · Deploy and extract an archive — P2
- API-017 · Download a single entry from an archive — P2
- API-018 · Download all artifacts of a build as an archive — P2
- API-019 · Download a folder or repository as an archive — P2
- API-020 · Server-side download (cache warm-up) — P2
- API-021 · RubyGems version list — P2
- API-025 · Create a signed download URL — P2
- API-026 · Rotate the signed-URL key — P2
- API-029 · List background tasks — P2 (the job list of R-API-015 covers trove's own jobs)
- API-030 · Rebalance redundant storage — P2 (not applicable to S3, INV-4)
- API-034 · Puppet module and release listing — P2
- API-035 · Source-control repository listing (tags, branches, refs) — P2
- API-036 · Source-control snapshot archives — P2
- API-037 · Single file from a source-control tag or branch — P2
- API-038 · Recalculate the index of other formats — P2
- API-039 · Recalculate RPM (YUM) metadata — P2
- API-042 · Calculate a Maven index — P2
- API-044 · Generate a Maven POM — P2
- API-047 · Recalculate Debian metadata — P2
- API-048 · Recalculate coordinates of cached Debian packages — P2
- API-049 · Create a Debian snapshot — P2
- API-051 · Relocate misplaced CRAN archives — P2
- API-056 · Check whether repositories exist — P2
- API-062 · Shared remote repository configuration (deprecated) — P2
- API-065 · List repositories connectable from a project — P2
- API-066 · List project-scoped consumers of a repository — P2
- API-067 · Convert a local repository to federated — Later
- API-068 · Convert federated repositories back to local — Later
- API-069 · List federated repositories with configuration drift — Later
- API-070 · Recover exhausted federation queues — Later
- API-072 · Full synchronization of a federated repository — Later
- API-073 · Propagate creation time during full sync — Later
- API-074 · Resynchronize a member's configuration — Later
- API-075 · Remove a member from all federations — Later
- API-076 · Federation connection details of a repository — Later
- API-077 · Federation state list and summary — Later
- API-078 · Prioritize a federated repository — Later
- API-079 · Legacy federation status (built-in synchronization) — Later
- API-080 · Failed binary transfer tasks — Later
- API-081 · Instance trust pairing — Later
- API-082 · Replication configuration document — P2
- API-083 · Get, set, update and delete a repository's replication — P2
- API-084 · Replication status of a path — P2
- API-085 · Run replication now — P2
- API-086 · Create, replace or update multi-push replication — P2
- API-087 · Enable or disable replications in bulk — P2
- API-088 · Event-based pull replication channels — P2
- API-089 · Block and unblock replication globally — P2
- API-090 · Checksum (metadata-only) replication — P2
- API-091 · Query language execution — P2
- API-093 · Archive entry search (deprecated) — Later
- API-097 · Bad or missing client checksum search — P2
- API-102 · Builds that use an artifact as dependency — P2
- API-103 · License search — Later
- API-104 · Version list search — P2
- API-105 · Latest version by layout — P2
- API-106 · Latest version by properties — P2
- API-108 · Parent manifest lists of a container image — P2
- API-112 · Remove redundant trash directory records — P2
- API-114 · List and read stored release sets — Later
- API-115 · Delete a source release-set version — Later
- API-116 · Import a release-set version from an archive — Later
- API-117 · Retention of first-generation release sets — Later
- API-118 · Distribution service metrics — Later
- API-119 · Distribution target rules — Later
- API-120 · Read release sets in the distribution service — Later
- API-121 · Create, update and delete release sets — Later
- API-122 · Sign a release-set version — Later
- API-123 · Trigger a vulnerability scan of a release-set version — Later
- API-124 · First-generation distribution status — Later
- API-125 · Distribute a first-generation release-set version — Later
- API-126 · Delete a first-generation release-set version from edge instances — Later
- API-127 · Abort a first-generation distribution — Later
- API-128 · Create, sign and distribute in one request — Later
- API-129 · List registered edge instances — Later
- API-130 · Distribute a second-generation release-set version — Later
- API-131 · Abort, remote-delete and track second-generation distributions — Later
- API-132 · Change the priority of a running distribution — Later
- API-133 · Export and import release sets for disconnected sites — Later
- API-134 · Release-set signing key — Later
- API-135 · GPG keys for artifact signatures — Later
- API-137 · Destination permissions — Later
- API-138 · Stuck distributions and emergency actions — Later
- API-139 · Redistribute recent release sets to one edge instance — Later
- API-140 · Distribution service health and settings — Later
- API-141 · Execute a named plugin execution — Later
- API-142 · Download plugin source — Later
- API-143 · List plugin executions and staging strategies — Later
- API-144 · Get a build staging strategy — Later
- API-145 · Execute a plugin-defined build promotion — Later
- API-146 · Reload plugins — Later
- API-147 · Tail server logs over the API — P2 (logs go to the log stream, INV-16)
- API-161 · Append modules to a build run — P2
- API-164 · Rename a build — P2
- Out of scope: API-027 (push to a discontinued external distribution service), API-071
  (reconnect to an operator-run federation service), API-136 (distribution service tokens)

</details>

<details>
<summary>Administration operations (ADMAPI)</summary>

- ADMAPI-010 · Change a user's password — P2 (no user passwords, INV-7)
- ADMAPI-011 · Password expiry operations — P2
- ADMAPI-025 · Older permission interface with resource sections — P2
- ADMAPI-026 · Oldest repository-only permission interface — P2
- ADMAPI-032 · SAML configuration operations — P2
- ADMAPI-033 · Directory (LDAP) connection operations — P2
- ADMAPI-034 · Directory group settings, search and import operations — P2
- ADMAPI-036 · Trusted reverse-proxy login configuration — P2
- ADMAPI-037 · Crowd connector configuration — Later
- ADMAPI-038 · Cloud IAM role binding operations — P2
- ADMAPI-048 · Create a pairing token — Later
- ADMAPI-049 · Legacy API key operations — Later
- ADMAPI-051 · Password expiration policy and bulk expiry — P2
- ADMAPI-052 · Encrypted client password — P2
- ADMAPI-053 · Activate and deactivate secret encryption — P2
- ADMAPI-054 · Password complexity policy — P2
- ADMAPI-056 · Trust-root certificates of the token issuer — P2
- ADMAPI-057 · External secret-manager connections — P2
- ADMAPI-058 · Default package-signing key (older interface) — P2
- ADMAPI-059 · Signing key pairs — P2
- ADMAPI-060 · Key-pair assignment to repositories — P2
- ADMAPI-061 · Trusted public keys of other services — Later
- ADMAPI-062 · Installed TLS certificates — P2
- ADMAPI-074 · Verify an outbound connection — P2 (the upstream tests of R-API-022 cover remote repositories)
- ADMAPI-075 · Reverse-proxy configuration generator — P2 (the Helm chart's Ingress takes this role, INV-13)
- ADMAPI-076 · SHA-256 migration task — Later
- ADMAPI-077 · Re-index paths in the package metadata service — P2
- ADMAPI-079 · Upstream curation availability setting — Later
- ADMAPI-082 · Support bundles of the distribution service — Later
- ADMAPI-083 · System and repository import and export — P2
- ADMAPI-087 · Release-set cleanup policy operations — Later
- ADMAPI-089 · Package archive policy operations — Later
- ADMAPI-090 · Older artifact-level archive operations — Later
- ADMAPI-091 · Hook definition operations — Later
- ADMAPI-092 · Hook event kinds — Later
- ADMAPI-093 · Execute, test and rerun hooks — Later
- ADMAPI-094 · Hook discovery metadata — Later
- ADMAPI-095 · Hook service health probes — Later
- ADMAPI-096 · Central instance registry — Later
- ADMAPI-097 · Topology operations — Later
- ADMAPI-098 · Entity synchronization monitoring — Later
- ADMAPI-099 · Identity federation targets on one instance — Later
- ADMAPI-100 · Central identity federation configuration — Later
- ADMAPI-101 · Curation policy federation — Later
- ADMAPI-102 · Central console system operations — Later
- ADMAPI-109 · Application lifecycle service operations — Later
- ADMAPI-110 · Policy, rule, template and evaluation operations — Later
- ADMAPI-111 · Lifecycle stage operations — Later
- ADMAPI-112 · Evidence operations — Later
- ADMAPI-113 · Release set operations — Later
- ADMAPI-114 · Deprecated identity operations of the repository service — Later
- Out of scope: ADMAPI-103 (license installation), ADMAPI-104 (license pools), ADMAPI-105 (network
  tunnel to a hosted instance), ADMAPI-106 (entitlement connector), ADMAPI-107 (hosted account
  portal), ADMAPI-108 (hosted log collection)

</details>

<details>
<summary>UI inventory (UI)</summary>

- UI-007 · First-run onboarding wizard — P2
- UI-015 · Folder download dialog — P2
- UI-017 · Delete-versions dialog — P2
- UI-027 · Federated repository instructions — Later
- UI-034 · Property sets screen — P2 (the API exists in [storage.md](storage.md))
- UI-048 · Webhook troubleshooting tab — P2 (R-UI-039 shows deliveries; filters and grouping later)
- UI-052 · Project storage quota indicators — P2
- UI-054 · Federation status dashboard — Later
- UI-056 · Project members screen — P2
- UI-057 · Global roles and project roles screens — P2
- UI-061 · Federation monitoring screens of the older embedded synchronization mode — Later
- Server name, date format, logo and login notice of UI-044 and UI-006 — P2 on
  [operations-and-observability.md](operations-and-observability.md)
- Out of scope: UI-055 (account portal dashboard of a hosted service), UI-059 (site launcher
  across hosted sites), UI-060 (in-product AI assistant panel)

</details>

## Security notes

- **Session versus token.** Session-authenticated writes need the CSRF token, fetch-metadata and
  origin checks of R-AUTH-012 (R-API-006); token-authenticated requests ignore the cookie
  (R-API-005). CORS never allows credentials (R-API-018). Minting tokens and changing grants through
  a session requires recent interactive authentication (R-API-007), so a hijacked session cannot
  quietly produce long-lived credentials.
- **README rendering** has three layers: server-side sanitizing with a strict allowlist, Angular's
  sanitizer, and a CSP with Trusted Types and no inline scripts (R-UI-008, R-UI-001). External images
  are blocked by default (Q-UI-6), which keeps viewer addresses and internal package names from
  reaching third parties.
- **Token secrets** appear once, in answers that are `no-store`, uncompressed and never stored for
  idempotency replay (R-API-014, R-API-017, R-API-032), and never leave component state in the
  browser (R-UI-043). Client setup defaults to environment-variable references and generates
  repository-scoped, short-lived tokens (R-UI-016).
- **Claim tester.** The browser decodes pasted ID tokens and sends claims only (R-UI-036); the owning
  page's evaluation endpoint takes claims, not tokens (R-API-035).
- **Information disclosure.** Unreadable repositories, items and packages answer like missing ones
  when hiding is on (R-API-008); `latestVersion` ignores hidden versions (R-API-029); deploy by
  checksum answers the same for missing and unreadable content (R-API-026). The explainer and
  effective permissions are limited to `policies:read` and repository managers (R-API-034).
- **Error bodies** never carry stack traces, SQL, internal host names, secrets or raw upstream errors
  (R-API-009).
- **Dependency confusion.** The NuGet snippet clears other package sources (R-UI-018); the scoped npm
  variant routes a scope to trove (R-UI-017).

## What this does not cover

### H-43 · General API rate limits are per replica

The `read`, `write` and `anonymous` buckets of R-API-016 live in each replica's memory. Behind a load
balancer the effective limit is the configured limit times the number of replicas, and a client that
spreads requests across replicas gets more than one replica's budget. The same applies to the
identity and CI limits on their pages. A cluster-wide limiter needs shared counters in PostgreSQL
(INV-3 rules out Redis), weighed in Q-API-3.

### H-44 · Credentials displayed in the browser are outside trove's control

A token shown once, embedded in a snippet or copied to the clipboard (R-UI-016, R-UI-029) is readable
by browser extensions with page access, by clipboard managers and history tools, and by anyone
watching the screen. trove reduces exposure (environment-variable references by default,
repository-scoped short-lived setup tokens, no storage, Blob URLs revoked at once), but cannot prevent
capture once the value is on screen or in the clipboard.

### H-45 · Hiding unauthorized resources is not timing-safe

With hiding on, a missing and an unreadable repository, item or package give the same status and body
(R-API-008), but the server takes different code paths, and the response time can tell them apart.
Equalizing response times is not specified; an attacker able to measure many requests can still learn
whether a key or path exists.

## Open questions

### Q-API-1 · OpenAPI generation approach

INV-6 makes the OpenAPI 3.1 document the source of truth; R-ARCH-003 leaves the choice of
generators to this page and pins whatever is chosen; R-ARCH-004 keeps generated TypeScript types in
`src/app/api/` next to thin hand-written clients per resource family.

- **A. Spec-first, generated Go server interfaces, generated TypeScript types.** `api/openapi.yaml`
  is written by hand. Go request and response types and server interfaces for `net/http` are
  generated (for example [oapi-codegen](https://github.com/oapi-codegen/oapi-codegen), whose v2.8
  release notes announce OpenAPI 3.1 support; the extent was not verified against trove's
  document). TypeScript types are generated (for example
  [openapi-typescript](https://github.com/openapi-ts/openapi-typescript)); thin clients per resource
  family call `HttpClient` with them.
- **B. Spec-first, generated Angular client.** As A, but the Angular side is a generated
  `HttpClient`-based client (for example [ng-openapi-gen](https://github.com/cyclosproject/ng-openapi-gen),
  whose README states OpenAPI 3.1 support since 1.0; not verified here).
- **C. Code-first.** Go types and handler registrations generate the document (for example
  [Huma](https://huma.rocks/), which emits OpenAPI 3.1).

Recommendation: **A**, confirmed by a spike that generates from the first real `api/openapi.yaml`
and checks the 3.1 constructs trove uses (`type` arrays with `null`, `const`, `oneOf` with
discriminator); if the Go generator mishandles them, the fallback is hand-written Go types validated
by the response check of R-API-002 item 4. Spec-first lets the contract be reviewed as a contract
before code exists, and generated types on both sides turn drift into a compile error. A matches the
workspace layout of R-ARCH-004 and keeps interceptors and `httpResource()` request descriptors under
trove's control; B adds a generator whose output style decides how every service looks. C inverts
INV-6's "source of truth" and ties the API shape to a framework.

### Q-API-2 · Is If-Match mandatory on writes?

- **A. Optional by default, mandatory where an owner says so** (R-API-012): authorization requires it
  for policy PUT; the UI always sends it; `api.require_if_match` makes it mandatory everywhere.
- **B. Mandatory for every PUT, PATCH and DELETE** of configuration resources.

Recommendation: **A.** Scripts and configuration-as-code tools often do blind updates on purpose, and
configuration apply has its own plan hash. Where a lost update is dangerous (permission policies), the
owner makes the header mandatory; the UI is safe either way.

### Q-API-3 · Cluster-wide rate limiting

- **A. Per replica, in memory** (R-API-016 and the identity and CI pages): no database load; the
  effective limit scales with the replica count (H-43).
- **B. Shared counters in PostgreSQL for the unauthenticated classes** (login, token revocation, CI
  exchange), per replica for the rest.
- **C. Shared counters for every class.**

Recommendation: **A for the MVP, B in P1.** The `read` and `write` classes protect replica resources,
which per-replica buckets do correctly. The unauthenticated classes guard against guessing and abuse,
where the limit should hold cluster-wide, and their volume is small enough that a counter update per
request is cheap. C adds a database write to every API request.

### Q-API-4 · INV-17's wording and byte transfers on `/files`

INV-17 says the UI uses only the public `/api/v1` API; R-ARCH-004 repeats it. The UI also downloads
and (P1) uploads bytes through `/files/…` (R-API-025), which INV-6 defines as the raw file path.
R-API-002 describes `/files` in the same OpenAPI document, so "public `/api/v1` API" reads as "the API
the OpenAPI document describes".

- **A. Amend INV-17's wording** to "only public, documented endpoints (the OpenAPI document, including
  `/files`), never private UI endpoints".
- **B. Mirror byte transfer under `/api/v1/`** (`…/items/content?path=`), duplicating `/files`.

Recommendation: **A.** INV-17's purpose is that every UI action is scriptable, and `/files` is as
public and scriptable as `/api/v1`; a mirrored byte path adds a second code path for the largest
transfers without any gain.

### Q-API-5 · Owning page for build information

Build information is P1 in the catalog and R-API-042 reserves its endpoints, but no spec page owns
the record schema, build permissions or the mapping of CI job names (which contain `/`) to
`{buildName}`. [formats/npm.md](formats/npm.md) notes the same gap.

- **A. A new spec page `builds.md`** with prefix `R-BLD`.
- **B. Add it to [gitlab-ci-oidc.md](gitlab-ci-oidc.md)**, since GitLab CI is the main producer.
- **C. Add it to [operations-and-observability.md](operations-and-observability.md).**

Recommendation: **A.** Build information is a resource model with its own permissions, retention and
promotion, not part of CI authentication or of operations; putting it into either page would mix
concerns that change for different reasons.

### Q-API-6 · Pagination parameter names

[repositories-and-proxy.md](repositories-and-proxy.md) lists repositories with `pageSize` and
`pageToken`; every other page uses `limit` and `cursor` (R-API-010).

- **A. `limit` and `cursor` everywhere.**
- **B. `pageSize` and `pageToken` everywhere.**

Recommendation: **A**, because it is already used by every other page and by the job API of
R-ARCH-020, so only one page changes.

### Q-API-7 · Syntax of custom methods

[repositories-and-proxy.md](repositories-and-proxy.md) uses colon custom methods
(`POST /api/v1/repositories:batchCreate`, `:quickSetup`); every other page uses sub-path verbs
(`/reindex`, `/keys/refresh`, `/restorations`, `/stop`), as R-API-004 specifies.

- **A. Sub-path verbs** (`POST /api/v1/repositories/batch-create`, `/quick-setup`).
- **B. Colon methods** for collection-level actions, sub-paths for item-level actions.

Recommendation: **A.** One form for all actions is easier to route, document and remember; colons in
paths are legal but are percent-encoded by some clients and proxies, and Go's `ServeMux` treats them
as literal segment text, so `repositories:batchCreate` and a repository named `batchcreate` sit in
the same segment space.

### Q-UI-1 · Component library

- **A. Angular Material with the CDK and `@angular/aria`.**
- **B. A third-party suite** (for example PrimeNG).
- **C. Own components on the CDK and `@angular/aria` only.**

Recommendation: **A.** It is released in lockstep with Angular majors (one version of every
dependency, upgraded together), ships accessible tables, dialogs, menus and form controls, and
`@angular/aria` (generally available since v22) covers widgets Material lacks, such as the file tree
and the reorderable list. B adds a vendor whose release cadence is decoupled from Angular; C costs
months of widget work before the first screen.

### Q-UI-2 · Visual direction of the shell (example screenshot requested)

The density and layout of the shell (navigation placement, table density, header content) shape every
screen.

- **A. Left navigation, compact tables**, suited to administrators scanning long lists.
- **B. Top navigation, comfortable spacing**, suited to occasional users.

Recommendation: **A** until decided, because most screens are lists. The owner is asked for an example
screenshot of an application whose shell and table density should serve as the model.

### Q-UI-3 · Layout of the package details page (example screenshot requested)

- **A. Two columns:** tabs (README, metadata, files, repositories) on the left, install panel and key
  facts on the right.
- **B. One column:** key facts and install command at the top, tabs below.

Recommendation: **A** on wide screens with B as the narrow-screen fallback, because the install
command is the most frequent action and stays visible while reading the README. The owner is asked for
an example screenshot of a package page whose layout should serve as the model.

### Q-UI-4 · Layout of the policy editor and the explainer (example screenshot requested)

- **A. Single page with sections and a sticky preview panel** (R-UI-027), explainer as its own screen.
- **B. Step-by-step wizard** for policies, explainer embedded as a side panel in the editor.

Recommendation: **A**, because policies are edited more often than created and a single page shows the
whole grant at once; the explainer stays reachable from the editor. The owner is asked for an example
screenshot of a permission editor and of an explanation view whose presentation should serve as the
model.

### Q-UI-5 · Internationalization mechanism

- **A. `@angular/localize`, compile-time:** one build per locale under `/ui/en/` and `/ui/de/`;
  switching the language reloads.
- **B. A runtime translation library:** one build, language switch without reload, translations loaded
  as JSON.

Recommendation: **A.** It is Angular's own mechanism (no additional dependency to keep in step with
Angular majors), translations are checked at build time (a missing German text fails the build), ICU
messages are built in, and there is no runtime cost. The price is a locale prefix in UI URLs, a reload
on language change and roughly one additional UI bundle in the binary. R-ARCH-004 already lays out
the workspace and the serving code for localized builds under `/ui/{locale}/`, so B would change
that requirement as well.

### Q-UI-6 · External images in READMEs

- **A. Block** (default of R-UI-008): replaced by a link with the alt text.
- **B. Allow `https` images** directly, with `img-src` widened in the CSP.
- **C. Proxy** images through trove with size and type limits.

Recommendation: **A by default, B as an operator option** (`ui.readme.external_images: allow`). Loading
external images reveals viewers' addresses and the names of internal packages to third parties and
widens the CSP; many installations are air-gapped, where images would not load anyway. C makes trove
fetch URLs chosen by a README author from inside the network, a server-side request forgery surface
controlled by package authors rather than administrators.
