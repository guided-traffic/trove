# API and UI

Status: specification — not implemented

This page specifies trove's management HTTP API and the Angular UI built on it. Part A fixes the
conventions every management endpoint follows: base path and versioning, the OpenAPI 3.1 contract,
JSON rules, authentication and CSRF handling on the API, problem-details errors, cursor
pagination, filtering, optimistic concurrency, idempotency keys, long-running work, cluster-wide
rate limits, response headers, requests that span several replicas, and when a write becomes
visible on the other replicas. Part B is the endpoint surface of the MVP and P1: one table per
resource family with method, path, purpose and required permission, pointing at the page that owns
the resource's semantics and payloads. Part C specifies the UI: routes, screens, sign-in,
permission-driven rendering, client setup, the permission explainer, the CI claim tester, the
system status with roles, Valkey and backups, accessibility, internationalization, state
management and testing. What a resource *means* (a
repository, a permission policy, an identity mapping, a token scope) and its payload fields are
owned by the page named in the [ownership table](README.md#pages); this page fixes how resources
are exposed over HTTP, keeps the surface consistent across those pages, and specifies how the UI
presents them. Package-manager protocols (`/api/<format>/<repoKey>/…`) belong to the format pages;
health probes and metrics on the operations listener belong to
[operations-and-observability.md](operations-and-observability.md).

Covers: the operation catalogs [api-content-operations.md](../features/api-content-operations.md)
and [api-admin-operations.md](../features/api-admin-operations.md), and the screen inventory
[ui-inventory.md](../features/ui-inventory.md). Single entries are drawn from
[events-and-extensibility.md](../features/events-and-extensibility.md) (EVT-034, EVT-070, EVT-072),
[artifacts.md](../features/artifacts.md) (ART-027, ART-028, ART-031, ART-040, ART-041, ART-048,
ART-049), [identity-and-access.md](../features/identity-and-access.md) (IAM-030, IAM-031,
IAM-032), [authentication-providers.md](../features/authentication-providers.md) (AUTHN-037 to
AUTHN-040), [access-tokens.md](../features/access-tokens.md) (TOK-026),
[oidc-ci-integration.md](../features/oidc-ci-integration.md) (CIOIDC-024),
[operations-and-monitoring.md](../features/operations-and-monitoring.md) (OPS-008, OPS-023,
OPS-032),
[search-and-query.md](../features/search-and-query.md) (SRCH-048, SRCH-058),
[repositories.md](../features/repositories.md) (REPO-007, REPO-008),
[ha-and-deployment.md](../features/ha-and-deployment.md) (HA-016) and
[beyond-baseline.md](../features/beyond-baseline.md) (BEYOND-030).

## Reading this page

- Part A (R-API-001 to R-API-019, R-API-051, R-API-052): conventions. Part B (R-API-020 to
  R-API-050): endpoint surface. Part C (R-UI-001 to R-UI-048): UI.
- Every `web` replica answers every request, and the Kubernetes Service picks one at random without
  session affinity (INV-2, INV-12). "A request" on this page therefore never means "the same
  replica as the previous request" (INV-18, R-API-051).
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
`servers[0].url`, which is the public base URL. The "public, documented endpoints" of INV-17 are
exactly the operations in this document plus the browser sign-in flow under `/auth/` (Q-API-4).

Every operation MUST declare:

| Element | Rule |
|---|---|
| `operationId` | lowerCamelCase verb + noun (`createRepository`), stable for the life of v1; generated clients and audit events use it |
| `tags` | The resource family of Part B |
| `security` | The accepted credential kinds of R-API-005 |
| `x-trove-permission` | The repository action or role that admits the caller, machine-readable, e.g. `{"action": "deploy", "repository": "query.repository", "path": "query.path"}` or `{"role": "admin"}`; R-API-008 enforces from it |
| `x-trove-system-scope` | The token system scope item that admits the caller instead of a role, where one exists (R-AUTH-025) |
| `x-trove-recent-auth` | `true` for operations that require recent authentication through a session (R-API-007) |
| `x-trove-idempotency-key` | `optional` on creating and job-starting POSTs and on DELETEs that answer 202 with a job (R-API-014) |
| `x-trove-if-match` | `required` on PUT and PATCH of a resource that carries a revision ETag (R-API-012) |
| Responses | Success schema with an example; every possible 4xx and 5xx as a reference to the shared problem schema (R-API-009) |
| Pagination | List operations reference the shared cursor parameters and envelope (R-API-010) |

Schemas use JSON Schema 2020-12, the default dialect of OpenAPI 3.1. Code is derived from the
document spec-first (Q-API-1):

- **Go.** [oapi-codegen](https://github.com/oapi-codegen/oapi-codegen) (v2.8 or later, the first
  line with OpenAPI 3.1 support; pinned through the `tool` directive of R-ARCH-003) generates the
  request and response types (`models`) and the `net/http` server interface (`std-http-server`)
  into `internal/api/v1/gen/`. Handlers implement that interface, so an operation without a handler
  does not compile. The strict-server variant is not used: handlers set `ETag`, `Location`, `Link`
  and `Retry-After` themselves and stream exports and job answers. Operations tagged `files`
  (`/files/…`) are excluded from generation; their handler is the raw-file path of R-ARCH-011, and
  the route-parity check (item 5 below) covers it.
- **TypeScript.** [openapi-typescript](https://openapi-ts.dev/) (7.x, pinned in the lockfile)
  generates types into `web/src/app/api/generated/`. Thin hand-written clients per resource family
  in `web/src/app/api/` build `HttpClient` calls and `httpResource()` request descriptors with them
  (R-UI-046). No generated Angular service layer is used.
- **Authorization declarations.** At start-up the server reads `x-trove-permission`,
  `x-trove-system-scope`, `x-trove-recent-auth` and `x-trove-if-match` from the embedded document
  into a table keyed by route pattern. The shared step of R-API-008 looks up the matched pattern
  (`http.Request.Pattern`) there. A registered `/api/v1/` route without an entry stops start-up, so
  no operation can run without a declared permission.
- **3.1 constructs.** A fixture document `api/testdata/constructs.yaml` contains every JSON Schema
  construct the contract uses (`type` arrays with `null`, `oneOf` with `discriminator`, `const`,
  `additionalProperties: false`). CI generates Go and TypeScript code from it, decodes and
  re-encodes its examples through the Go types, and type-checks them as constants of the
  TypeScript types. A construct the Go generator mishandles is written in an
  equivalent form that it handles (for example `enum: [x]` for `const: x`); Go types are never
  written by hand.
- **Format enums.** The document carries the enum of format IDs and one generated format enum per
  repository class, listing the formats whose `Descriptor().Classes` contain that class
  ([architecture.md](architecture.md) R-ARCH-008, R-ARCH-010). Both come from the format registry,
  never from a hand-maintained list, and the unit test of R-ARCH-010 fails on any difference
  between them and the registry.

CI MUST:

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
- Starting the server with a test build that registers an `/api/v1/` route missing from the
  embedded document fails start-up and names the route.
- CI generates code from `api/testdata/constructs.yaml`; every example in it survives a decode and
  re-encode through the generated Go types unchanged and compiles as a constant of the generated
  TypeScript type.

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

- Actions on a whole collection use the same sub-path form (`POST /api/v1/repositories/batch-create`,
  `POST /api/v1/trash-can/purge`); colon methods (`repositories:batchCreate`) are not used (Q-API-7).
- **Reserved names.** A literal segment at the position where a collection's key or name sits
  (`/users/me`, `/webhooks/test`, `/webhooks/deliveries`, `/repositories/batch-create`) is a
  reserved name of that collection. Where the literal and the `{key}` route share a method, the
  router prefers the literal and a resource with that name is unreachable (`GET /webhooks/deliveries`);
  where they do not, one URL would still name two things, and the next route added with a shared
  method would silently shadow the resource. The owning page rejects it on create (422 `reserved_name`), and names
  that come from an identity provider follow that page's reserved-username rule. The key or name
  schema in the OpenAPI document lists the reserved names as `x-trove-reserved-names`; a CI check
  derives every literal sibling of a `{key}` segment from the document and fails when one is
  missing from that list. Collections addressed by server-generated identifiers (tokens, jobs) need
  no reservation.
- A path or `path` parameter is percent-decoded once. Empty segments, `.` and `..`, NUL, control
  characters and invalid UTF-8 answer 400 `invalid_path` (the format conformance kit of R-ARCH-010
  applies the same rules to protocol paths).
- A wrong method answers 405 with an `Allow` header. `OPTIONS` is answered only for CORS
  (R-API-018).

Acceptance criteria:

- A create answers 201 whose `Location` header equals the URL on which a subsequent GET succeeds.
- `GET /api/v1/repositories/npm-local/items?path=a/../b` answers 400 `invalid_path`.
- `POST /api/v1/repositories` with key `batch-create` answers 422 `reserved_name`.

### R-API-005 · Caller authentication on the management API
Priority: **MVP** · Covers: API-001, ADMAPI-001

| Credential | Presented as | Typical caller |
|---|---|---|
| trove token (`trv_pat_`, `trv_sat_`, `trv_cit_`, INV-8) | `Authorization: Bearer <token>` | Scripts, CI jobs, tooling |
| The same token as Basic password | `Authorization: Basic` with any username | Tools that only speak Basic, `curl -u` on `/files` |
| UI session | `__Host-trv_session` cookie (INV-8, Q-AUTH-2) plus CSRF protection (R-API-006) | The Angular UI |
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

The mechanism (per-session token in the `__Host-trv_csrf` cookie, `X-CSRF-Token` header, origin and
fetch-metadata checks, failure code `csrf_failed`) is R-AUTH-012 in
[identity-and-authentication.md](identity-and-authentication.md). This page fixes where it
applies and how the UI uses it:

- It applies to every request authenticated by the session cookie with an unsafe method (POST,
  PUT, PATCH, DELETE) under `/api/v1/` and `/files/`, and to `POST /auth/logout`.
- Requests authenticated by an `Authorization` header are exempt, because they carry no ambient
  credential.
- No GET under `/api/v1/` changes state; sign-out is a POST.
- The UI configures Angular's XSRF support with
  `withXsrfConfiguration({cookieName: '__Host-trv_csrf', headerName: 'X-CSRF-Token'})`
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
| Command-line login | Approving or denying an `npm login` or a device-code login (P1; R-AUTH-034, R-AUTH-036) |
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
SCIM errors (R-API-046). The OAuth token endpoints answer OAuth error responses
([RFC 6749 §5.2](https://www.rfc-editor.org/rfc/rfc6749#section-5.2), `application/json`) instead:
the CI exchange `POST /api/v1/auth/oidc/token` (R-API-035, with `reason` and `requestId`) and the
device token endpoint `POST /api/v1/auth/device/token` (P1, R-AUTH-036 in
[identity-and-authentication.md](identity-and-authentication.md)).

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
| 400 | `invalid_header` | A trove request header with an invalid value, today `Trove-Authz-Min-Epoch` that is not a decimal integer between 1 and 2^63 − 1 (R-API-052, [authorization.md](authorization.md) R-AUTHZ-032); the `detail` names the header |
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
| 412 | `authz_epoch_unknown` | `Trove-Authz-Min-Epoch` names an authorization epoch the database has not reached, for example because a PostgreSQL failover lost the commit that returned it (R-API-052, R-AUTHZ-032); the client drops that epoch and re-reads the object it changed |
| 413 | `payload_too_large` | R-API-017 |
| 415 | `unsupported_media_type` | Wrong `Content-Type` |
| 422 | `validation_failed` | Semantically invalid input; `errors[]` lists the fields |
| 422 | `idempotency_key_reused` | R-API-014 |
| 422 | `reserved_name` | A create uses a name reserved by a route (R-API-004) |
| 428 | `precondition_required` | `If-Match` required (R-API-012) |
| 429 | `rate_limited` | R-API-016; `Retry-After` set |
| 500 | `internal` | Unexpected failure (R-ARCH-023) |
| 502 | `upstream_error` | A synchronous action that calls an upstream or issuer failed |
| 503 | `unavailable` | A dependency is unreachable or maintenance mode refuses the write; `Retry-After` set. An unavailable Valkey never causes it: every use of Valkey has a fallback ([architecture.md](architecture.md) R-ARCH-031) |
| 503 | `authz_stale` | The replica could not confirm a grant index that is current, or at least as new as `Trove-Authz-Min-Epoch`, within the wait of [authorization.md](authorization.md) R-AUTHZ-028; the request was refused and changed nothing; `Retry-After` set (R-AUTHZ-016, R-AUTHZ-032) |

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

- Parameters: `limit` (default 50, maximum 500) and `cursor`, with these names on every list
  endpoint of every page; there are no per-page variants such as `pageSize` and `pageToken`
  (Q-API-6).
- Response envelope, the same on every list endpoint (`items` and `nextCursor`, never a
  resource-named array), plus `Link: <…>; rel="next"`
  ([RFC 8288](https://www.rfc-editor.org/rfc/rfc8288)) when more items exist:

```json
{"items": [], "nextCursor": "djEuZXlKcklqb2libkJ0TFdGc2JDSjk"}
```

- The cursor is opaque: a versioned base64url encoding of the sort-key values of the last item and
  a fingerprint of the filters and sort order. A cursor that does not decode, or that was issued
  for other filters or another sort, answers 400 `invalid_cursor`. A cursor grants nothing: every
  page is filtered by the caller's permissions again.
- The cursor is self-contained (INV-18). Each page is one keyset query in its own transaction, and
  the cursor holds everything the next query needs, so the next page may come from any `web`
  replica. There is no server-side cursor (`WITH HOLD` cursors are forbidden by R-ARCH-019 in
  [architecture.md](architecture.md)), no stored result set and no per-replica key. The filter
  fingerprint is a SHA-256 over a canonical form of the filters the request actually uses, so a
  release that adds an optional filter does not change the fingerprint of requests that do not use
  it.
- Cursor versions follow expand/contract, because the pages of one traversal may alternate between
  replicas of release N and N-1 during a rolling upgrade (INV-3, R-HA-020 in
  [ha-and-helm.md](ha-and-helm.md)). A release that introduces a new cursor version decodes it but
  still emits the previous one; the next minor release emits it. Every release therefore decodes
  every version the previous minor release emits, and a traversal never fails because a rollout is
  in progress. A cursor of an older version answers 400 `invalid_cursor`, and the client restarts
  the traversal.
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
- In the e2e profile `scatter` ([architecture.md](architecture.md) R-ARCH-032: three `web`
  replicas, round robin per request), paging the 120 groups with `limit=1` yields 120 distinct
  names, each page from another replica than the one before (`X-Trove-Replica`).
- A test drives one traversal with `limit=1` through a release N-1 and a release N replica in
  turn, where the test build of N adds a cursor version; it ends without `invalid_cursor`.

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
- `If-Match` is REQUIRED on every PUT and PATCH of a resource that carries a revision ETag
  (Q-API-2); without it the answer is 428 `precondition_required`
  ([RFC 6585 §3](https://www.rfc-editor.org/rfc/rfc6585#section-3)) whose `detail` names the
  header. A client that deliberately overwrites whatever is stored sends `If-Match: *`, so a blind
  write is always an explicit choice. The OpenAPI document marks these operations with
  `x-trove-if-match: required` (R-API-002). There is no switch to turn the rule off.
- Exempt, because they cannot lose another writer's change or carry no revision: DELETE (honors
  `If-Match` when sent), membership and assignment edits that send a delta or bind one pair
  (`{"add": [...], "remove": [...]}`, role bindings, project administrators and repository
  assignments), write-only secret slots, item properties, and file content on `/files`.
- The UI sends the ETag it loaded on every write and never sends `If-Match: *` on its own
  (R-UI-042).
- GET supports `If-None-Match` and answers 304.
- **Revision columns survive a restore without repeating.** Every column whose value is served as
  an `ETag` or compared with `If-Match` or `If-None-Match` (`revision`, `version`,
  `config_version`, …, whatever name the owning page gives it) is registered with
  `meta.RegisterRevisionColumn`, the revision-column registry of `internal/meta`
  ([backup-and-restore.md](backup-and-restore.md) R-BAK-022). A restore adds 2^32 to every
  registered column, so each restored value lies above every value the old installation can have
  issued. An `If-Match` taken before a restore, including one for a version written after the
  backup's snapshot, therefore never matches different content of the same number, and a stale
  `PUT` cannot overwrite a record its sender never saw, for example a permission policy or a CI
  mapping another administrator has narrowed since. This generalizes what
  [gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-001 states for the CI tables and
  [authorization.md](authorization.md) for `authz_policy.version` and `project.version`. This
  page's own revisioned table, `user_preferences` (R-API-021), is registered too. R-BAK-022's unit
  test fails on an unregistered `bigint` column named `rev_seq`, `*_version`, `revision` or
  `epoch`; an owning page that names a revision column otherwise registers it all the same.
- File content on `/files` uses the SHA-256 ETag of [storage.md](storage.md); `If-None-Match: *` on PUT makes a
  deploy create-only (412 when the path exists), so CI can publish without the risk of replacing a
  released file.

Data model (convention for every configuration table; the tables belong to their pages):

```sql
-- update pattern; zero rows while the key exists means 412; $2 is NULL only for If-Match: *
UPDATE <table>
   SET ..., revision = revision + 1, updated_at = now(), updated_by = $actor
 WHERE <key column> = $1 AND ($2::bigint IS NULL OR revision = $2)
RETURNING revision;
```

Acceptance criteria:

- Two clients GET the same repository (`ETag: "4"`) and PATCH it with `If-Match: "4"`; one
  answers 200 with `ETag: "5"`, the other 412 with `currentEtag` `"5"`.
- The same PATCH without `If-Match` answers 428 `precondition_required` and changes nothing; with
  `If-Match: *` it answers 200.
- A script over the OpenAPI document asserts that every PUT and PATCH whose resource's GET declares
  an `ETag` response header carries `x-trove-if-match: required`, unless it is in the exempt list
  above.
- `curl -X PUT -T app.tgz -H 'If-None-Match: *' -u "ci:$TROVE_TOKEN" "$TROVE_URL/files/generic-local/app/1.0/app.tgz"`
  answers 201 the first time and 412 the second time.
- A repository at `ETag: "4"` is backed up, then patched twice on the old installation
  (`ETag: "6"`). After a restore of that backup into a new installation and two patches there, a
  `PATCH` with `If-Match: "6"` answers 412, and `currentEtag` is above 2^32. The same holds for a
  permission policy.

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
- `{"class": "local"}` sent with a matching `If-Match` to a remote repository answers 422 with
  pointer `/class`.

### R-API-014 · Idempotency keys for creates
Priority: **MVP** · Covers: trove-only

POST endpoints that create a resource or start a job, and DELETE endpoints that answer 202 with a
job (R-API-015), accept the `Idempotency-Key` request header of the IETF draft
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
  conflict) for at most 1 second (`SET LOCAL lock_timeout`, below the 5 s statement timeout of
  R-ARCH-019 in [architecture.md](architecture.md), so the wait never ends as a
  `database_timeout`), then replays the outcome or answers 409 `idempotency_in_progress` with
  `Retry-After: 1`, as the draft asks for a request still in progress. The wait holds a main-pool
  connection, so it is kept short: a client that sends the same key many times at once cannot
  occupy a replica's request pool, and the duplicates count against the `write` limit of R-API-016.
  A create transaction finishes far within the second; a first request that takes longer is
  answered for its duplicates by the 409, and their retries replay its outcome.
- The row is the only state, and it lives in PostgreSQL, so a retry behaves the same on every
  replica (INV-18). A first request whose replica is cut off during a drain or crash rolls back
  and stores nothing, and its retry, on whichever replica it reaches, runs again.
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
- A test build holds the first request's transaction open for 3 s. 50 concurrent duplicates with
  the same key each answer 409 `idempotency_in_progress` with `Retry-After: 1` within 1.5 s, and
  `pg_stat_activity` shows no trove connection waiting on a lock (`wait_event_type = 'Lock'`) for
  longer than 1.5 s. A retry after the first request committed replays its outcome.

### R-API-015 · Long-running work as jobs
Priority: **MVP** · Covers: ADMAPI-002, API-063

Work that can outlast a request (repository deletion, reindexing, garbage collection, large copy,
move or restore, configuration apply, retention runs, support bundles, storage summary refresh)
answers 202 with `Location` of the job and the job representation as body. Jobs, their states and
their API (`GET /api/v1/system/jobs`, `GET /api/v1/system/jobs/{id}`,
`POST /api/v1/system/jobs/{id}/cancel`) are R-ARCH-016 in [architecture.md](architecture.md)
(model) and R-OPS-019 in [operations-and-observability.md](operations-and-observability.md) (API).
Jobs survive replica restarts and are visible from every replica. The `web` replica that answers
202 only enqueues the job; `worker` replicas run it (INV-2, R-ARCH-001). Draining or losing the
`web` replica therefore loses nothing, and a drained `worker` releases the job to another one
(R-ARCH-016, R-ARCH-024). While no `worker` replica is live, jobs stay `queued`, and the system
status screen says so (R-UI-038). Owning pages MAY additionally expose a domain view of their runs
(garbage-collection runs, retention runs) that references the job.

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
- Deleting the `worker` pod that runs the job, gracefully or with `--grace-period=0 --force`, lets
  another `worker` replica finish it. Deleting the `web` pod that answered 202 right after the
  answer changes nothing: the job still ends in `succeeded`.

### R-API-016 · Rate limits
Priority: **MVP** · Covers: BEYOND-030

Rate limits on the management API give every caller a fair share of the service, so one runaway
script or UI tab cannot take the capacity other callers need. They count across the whole
installation (Q-API-3): a bucket admits the same budget whichever `web` replica a request reaches
and however many replicas run, so a configured value means the same at three replicas and at
thirty. Protecting one replica's own CPU and memory is the job of `server.max_in_flight`
([architecture.md](architecture.md) R-ARCH-005), which stays per replica. The limits for login,
token validation and token revocation are R-AUTH-038 in
[identity-and-authentication.md](identity-and-authentication.md); those of the CI exchange are
R-CI-021 in [gitlab-ci-oidc.md](gitlab-ci-oidc.md); the class a further endpoint applies to every
request on it, package protocols included, is R-EP-015 in [endpoints.md](endpoints.md). This
requirement adds the general classes for `/api/v1/` and `/files/`:

| Class | Applies to | Bucket key | Limiter name |
|---|---|---|---|
| `read` | Safe methods | Principal (token ID, or session user) | `api_read` |
| `write` | Unsafe methods | Principal | `api_write` |
| `anonymous` | Requests without credentials | Client address (R-ARCH-006, R-OPS-005) | `api_anonymous` |

- **Limiter.** Each class is a limiter of R-ARCH-034 ([architecture.md](architecture.md)) with
  scope `cluster`: a sliding-window counter in Valkey with the window `W = burst ÷ rate`, which
  admits at most `burst` requests per window. With the defaults below, `W` is 4 s for `read`, 5 s
  for `write` and 4 s for `anonymous`. Bucket keys reach Valkey only as hashes (R-ARCH-030). One
  check is one pipelined round trip, bounded by `valkey.command_timeout`.
- **Refused requests count too.** The limiter increments the window's counter before it decides
  (R-ARCH-034), so a `429` uses up budget like an admitted request. A caller that keeps sending
  while refused stays refused until its rate drops below the limit; a caller that waits for
  `Retry-After` gets the configured rate. A retry loop that ignores `Retry-After` therefore cannot
  win back a share of the budget by hammering.
- **Valkey unavailable.** While a replica cannot reach Valkey (its circuit breaker is open, or a
  call failed), the limiter answers from a local bucket with rate `rate ÷ n` and burst
  `max(1, burst ÷ n)`, where `n` is the number of live `web` replicas in the replica registry, at
  least 1, refreshed with the heartbeat (R-ARCH-034). The cluster-wide budget then holds
  approximately; a client whose keep-alive connection stays on one replica gets about `1/n` of it.
  `trove_ratelimit_fallback{limiter}` is 1 while this lasts. A limiter never fails a request because Valkey failed, and never delays it by
  more than `valkey.command_timeout`. H-43 records what stays open.
- **Answer.** An exhausted bucket answers 429 `rate_limited` with `Retry-After`: the whole seconds
  after which the next request would be admitted if the caller sent nothing more, computed from the
  same counters, at least 1.
- **Fields.** Every answer on a limited route carries the `RateLimit-Policy` and `RateLimit` fields
  of [draft-ietf-httpapi-ratelimit-headers](https://datatracker.ietf.org/doc/draft-ietf-httpapi-ratelimit-headers/)
  (draft-11 of 23 May 2026, checked 2026-10-28; the syntax follows the draft until it is published):
  `RateLimit-Policy: "<class>";q=<burst>;w=<W>` and
  `RateLimit: "<class>";r=<available>;t=<seconds until the current window ends>`, where
  `available` is `burst` minus the window's estimate, at least 0. During the fallback, `q` and `r`
  describe the replica's share. The draft does not address several servers; it lets the available
  quota change between responses and tells clients not to expect the same fields on the next
  response, which covers a request that reaches a replica in fallback.
- The one guessable credential, the break-glass password, is counted cluster-wide in PostgreSQL by
  R-AUTH-038 in [identity-and-authentication.md](identity-and-authentication.md), because a lockout
  must survive a loss of Valkey's data. Administrators are not exempt by default. Rejections are
  counted in `trove_api_rate_limited_total{class}`.

Configuration (each value is the budget of one bucket across the whole installation):

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
- With the defaults, a client that sends 100 reads per second with one token for 10 s and ignores
  `Retry-After` is admitted at most 250 times, because its refused requests keep the window full
  after the first burst. A second client with another token that waits for each `Retry-After`
  before it sends again is admitted more often over the same 10 s.
- In the e2e profile `scatter` (three `web` replicas, round robin per request), 1,000
  `GET /api/v1/repositories` with one token within 2 s are admitted at most 300 times, the same
  bound as against one replica. With Valkey stopped, the same run is admitted at most 300 times,
  and `trove_ratelimit_fallback{limiter="api_read"}` is 1 on every replica.
- With the defaults, a `200` answer of `GET /api/v1/repositories` carries
  `RateLimit-Policy: "read";q=200;w=4`.

### R-API-017 · Request limits, timeouts and response headers
Priority: **MVP** · Covers: trove-only

- JSON bodies are limited to `api.max_request_body_bytes`; YAML for configuration as code to
  `api.max_configuration_body_bytes`. Larger bodies answer 413. Uploads on `/files` are limited by
  [storage.md](storage.md), not by this rule.
- A synchronous `/api/v1/` handler MUST finish within 30 seconds; work that can take longer is a
  job (R-API-015). Server-wide timeouts are owned by [architecture.md](architecture.md). The bound
  is far below the `web` shutdown timeout the chart derives from the grace period (110 s with the
  defaults, R-HA-011 in [ha-and-helm.md](ha-and-helm.md)), so a node drain never cuts a management
  request that keeps it. Two kinds of response may last longer, and R-API-051 states how each
  survives a drain: event streams, which a draining replica ends at once so the client reconnects
  elsewhere, and streamed downloads whose length depends on the data (the audit export of
  R-API-039; from P1 retention reports and the support-bundle download), which a client resumes or
  repeats when a drain cuts them.
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
`If-Match`, `If-None-Match`, `Idempotency-Key`, `Trove-Authz-Min-Epoch`. Exposed response headers:
`ETag`, `Location`, `Link`, `Retry-After`, `RateLimit`, `RateLimit-Policy`, `X-Request-Id`,
`Deprecation`, `Sunset`, `Trove-Authz-Epoch` (R-API-052).

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

### R-API-051 · Requests that span replicas
Priority: **MVP** · Covers: trove-only

The Service distributes requests at random and without session affinity (INV-12), so consecutive
requests of one client, a UI tab included, reach different `web` replicas, and any of them may be
draining. No management request may depend on state that an earlier request left in one replica's
memory or on its local disk (INV-18, R-ARCH-032 in [architecture.md](architecture.md)). Every
management protocol that spans requests keeps its state in PostgreSQL or in what the client sends
back:

| Protocol | Where its state lives | Rule |
|---|---|---|
| Paged lists (R-API-010) | In the cursor | One keyset query per page; no server-side cursor, no stored result set; versions follow expand/contract |
| Idempotent creates and job starts (R-API-014) | `api_idempotency_keys` in PostgreSQL | A retry replays, answers 409 `idempotency_in_progress`, or runs again on whichever replica it reaches |
| Long-running work (R-API-015) | Job rows | Enqueued by the answering `web` replica, run by `worker` replicas |
| Optimistic concurrency (R-API-012) | Revision columns in PostgreSQL | Every replica computes the same `ETag`; a restore lifts every revision column, so an `ETag` from before it never matches |
| Seeing one's own grant change (R-API-052) | The epoch the client sends in `Trove-Authz-Min-Epoch`, checked against `cluster_epoch` in PostgreSQL | Any replica waits for a grant index at least that new, or answers `412` when the database never reached it |
| UI sessions, CSRF tokens, recent authentication, browser sign-in | Session and login-flow rows ([identity-and-authentication.md](identity-and-authentication.md) R-AUTH-006, R-AUTH-010, R-AUTH-011, R-AUTH-012) | A CSRF token or a re-authentication obtained through one replica counts on every other |
| Command-line login approval and polling (P1) | The `cli_login` row (R-AUTH-034, R-AUTH-036) | The UI's 2 s refresh (R-UI-004) and the client's polls may each reach another replica |
| Event streams (`text/event-stream`) | The event `id` | Below |
| A resumed audit export | The client's `from` and the event IDs it already holds | Below |

- **Event streams.** An endpoint that answers `text/event-stream`
  ([Server-Sent Events](https://html.spec.whatwg.org/multipage/server-sent-events.html)), today
  only the webhook delivery log of R-API-041 (P1), follows these rules:
  - Every event carries an `id` that encodes its position in an order the owning page defines in
    PostgreSQL, and a request with `Last-Event-ID` continues after that position on any replica.
    The position must not skip a committed event. A position taken from a sequence must allow for
    transactions that commit out of sequence order, which [architecture.md](architecture.md)
    Q-ARCH-15 describes, for example by re-reading a short window behind the position and dropping
    events already sent by record and version.
  - New events reach the stream from PostgreSQL. The stream queries PostgreSQL at least once every
    5 s, takes a main-pool connection for each query only and holds none between them
    (R-ARCH-019), so open streams never add database connections, however many there are. It
    never waits for an event held in another replica's memory. An owning page MAY register a
    signal area of R-ARCH-018 that wakes its streams earlier; signals are delivered at most once,
    so the poll alone must meet the stream's contract, and a stream works without Valkey.
  - When `health.shutdown_drain_delay` has passed (between steps 2 and 3 of the shutdown in
    R-ARCH-024), the replica sends `retry: 1000` and ends every open stream. By then the proxies
    have stopped routing to it, so the reconnect reaches another replica. A browser's
    `EventSource` re-establishes a connection whose body ended and sends the last `id` as
    `Last-Event-ID`, as the WHATWG processing model specifies. Without this, a stream would hold
    the draining replica until `server.shutdown_timeout` and then be cut.
  - A stream also ends after `api.event_stream_max_age`, so that streams move onto replicas added
    since they opened.
- **Streamed downloads.** A download whose length depends on the data (the audit export
  `/api/v1/audit/events/export`; from P1 retention reports and the support-bundle download) may
  outlast `server.shutdown_timeout` and is then cut at the deadline (Q-ARCH-18 in
  [architecture.md](architecture.md)). These rules make the cut harmless:
  - The handler reads in keyset batches in the export's order, each one query on a main-pool
    connection that it returns before it writes the batch to the client (R-ARCH-019). A slow
    reader therefore holds no database connection and no long transaction, and concurrent exports
    do not grow the connection count.
  - An answer whose length is not known in advance is sent with chunked transfer coding on
    HTTP/1.1 or as an HTTP/2 stream. A cut answer then lacks the final chunk
    ([RFC 9112 §7.1](https://www.rfc-editor.org/rfc/rfc9112#section-7.1)) or the end of its
    stream, and the client sees an incomplete response instead of a short file.
  - The audit export is ordered by occurrence time and event ID, and every line carries both. A
    client that was cut resumes on any replica with `from` set to the occurrence time of the last
    complete line (`from` is inclusive, R-API-011) and drops the lines whose `id` it already has.
    Other streamed downloads are repeated whole.
  - Batches are separate snapshots. An event whose transaction commits after the batch that
    covers its occurrence time is not in that export; a range whose `to` lies further in the past
    than the longest write transaction is complete.
- **No affinity, anywhere.** Neither the API nor the UI sets a cookie, header or token whose
  purpose is to reach the same replica again. `X-Trove-Replica` (R-OPS-002) is diagnostic only;
  the UI shows it in error details and never sends it back.

Configuration:

```yaml
api:
  event_stream_max_age: 10m   # default (proposed)
```

Acceptance criteria:

- In the e2e profile `scatter`, the Playwright journeys of R-UI-047 pass, every list operation of
  the OpenAPI document is walked to its last page with `limit=1` over a fixture with at least
  three items each, and an `Idempotency-Key` replayed through another replica returns the stored
  outcome with `Idempotency-Replayed: true`.
- (P1, with R-API-041) A subscription with `recordSuccessfulDeliveries` delivers 100 events while a
  client follows `GET /api/v1/webhooks/deliveries?follow=true`, and the serving `web` pod is
  deleted gracefully in the middle. The client reconnects to another replica with
  `Last-Event-ID`, and receives for each of the 100 delivery records an event with its final state.
  Events for versions written within the re-read window before the cut may arrive a second time;
  the client drops them by `id` and `changeSeq`, and after that no record and version appears
  twice and none is missing. The deleted pod's stream ends within
  `health.shutdown_drain_delay` plus 1 s.
- An audit export of 200,000 events read with `curl --limit-rate 200K` is cut by deleting the
  serving pod. curl reports an incomplete transfer. A second request with `from` set to the last
  complete line's `occurredAt`, with already received IDs dropped, completes the export; the union
  holds each event of the range exactly once. During the slow read, `pg_stat_activity` shows no
  connection of that pod in state `idle in transaction` and none held for longer than one batch
  query.

### R-API-052 · Visibility of a management write on other replicas
Priority: **MVP** · Covers: trove-only

A client that writes through one replica and then calls another must know what the second replica
sees (Q-API-12). The propagation mechanism is R-ARCH-018 in [architecture.md](architecture.md);
this requirement states the contract for API clients.

- **Committed means durable.** A 2xx answer to a write means the change and its audit event are
  committed on the PostgreSQL primary (R-API-019). It survives a PostgreSQL failover when the
  platform replicates synchronously, as [ha-and-helm.md](ha-and-helm.md) R-HA-013 recommends;
  without that, a failover can lose the last acknowledged writes, and no replica can tell.
- **Management reads see every committed write at once.** A `GET` under `/api/v1/` of a
  configuration resource or a list of them reads the resource from PostgreSQL, never from an
  in-process cache, so it shows the change and its new `ETag` on every replica as soon as the write
  has answered. What the caller is allowed to see, and reads that are enforcement decisions about
  the caller (`/api/v1/auth/whoami`, `/api/v1/authz/me`, `/api/v1/authz/check`), follow the next
  bullet.
- **Enforcement follows within a bound.** The effect of a change on authentication,
  authorization, package protocols and raw file access (repository snapshot, grant index, token
  and session caches, CI mappings, settings) applies on the answering replica at once and on every
  other replica within the propagation bound of R-ARCH-018: typically milliseconds while Valkey is
  available, at most `cluster.epoch_poll_interval` plus one query and the cache rebuild without it
  (R-ARCH-018 tests 2 s with the defaults). Revocations of tokens and sessions and the disabling of
  a principal are in force everywhere within `auth.revocation_bound` (2 s by default), with or
  without Valkey; a replica that cannot prove a fresh change-log poll answers from PostgreSQL or
  with `503`, never from a stale cache (R-AUTH-023 in
  [identity-and-authentication.md](identity-and-authentication.md)).
- **A new repository is usable at once.** A repository key that a replica's snapshot does not know
  is looked up in PostgreSQL before the replica answers `404`; when the key exists, the replica
  runs one change-log poll and waits until the caches it invalidates are rebuilt, the grant index
  of [authorization.md](authorization.md) R-AUTHZ-028 included, so the request is resolved and
  authorized against a state that knows the repository ([architecture.md](architecture.md)
  R-ARCH-011 step 2, cross-page). A key that does not match the pattern of INV-5 answers `404`
  without a query; concurrent lookups of one key share one query, and concurrent polls share one
  poll. A script that creates a repository and then publishes to it through the Service
  therefore never meets a `404` from a replica that has not heard of it yet. This matters because
  npm does not retry a `404` (its fetch layer retries `408`, `420`, `429` and `5xx` only), so a
  bounded delay would fail the publish instead of slowing it down. The one exception: a key that
  was requested and missed on a replica shortly before it was created may still answer `404` there
  within the bound of the previous bullet, because repeated misses for one key are answered from a
  short-lived negative entry that the creation's change evicts. The negative entries are a
  registered, size-bounded per-replica cache (R-ARCH-018), so a flood of requests for unknown keys
  costs at most one indexed query per distinct key and entry lifetime, as an unknown token does
  (R-AUTH-023), and never more memory than the cache's bound.
- Deletions and changes of existing repositories follow that bound: for up to that bound, another
  replica may still serve a deleted repository. Grant changes follow it too for every client that
  does not send the header of the next bullet; such a client may meet a refusal of an action a new
  policy allows, or an allow of one a removed grant refused, and waits for the bound or retries.
- **Waiting for one's own permission change** ([authorization.md](authorization.md) R-AUTHZ-032,
  Q-AUTHZ-12). Two headers carry the authorization epoch, the epoch of the `authz` change area
  (R-AUTHZ-028), so that a client can see its own grant change on whichever replica answers next,
  without affinity:
  - Response header `Trove-Authz-Epoch: <n>`: sent by the writes that change grants, which are
    policy writes and role bindings (R-API-033) and, from P1, project writes with their
    administrators and repository assignments (R-API-047). `n` is the epoch the write's transaction
    produced.
  - Request header `Trove-Authz-Min-Epoch: <n>`: honoured on every authenticated request on the
    client listener, `/api/v1/`, `/files/` and the package protocols alike. It is read only after
    authentication succeeded, so an invalid or revoked credential gets its `401` first, and a
    request without a credential ignores it, which keeps anonymous callers from probing the epoch.
    The replica then decides the request on a grant index whose epoch is at least `n`, waiting for
    a build if needed, and answers `412` `authz_epoch_unknown` when the database has
    not reached `n`, `503` `authz_stale` with `Retry-After` when the wait runs out, and `400`
    `invalid_header` for a value that is not a decimal integer between 1 and 2^63 − 1 (R-API-009).
  - The header covers added and removed grants alike. It does not cover a new repository reached
    through a pattern grant, which the lookup of the previous bullets handles.
  - A `412` is a hint, not a proof: the client stops sending that epoch, because the database may
    take long to reach it, and re-reads the changed object; its `ETag` tells whether the change is
    stored (R-API-012). Epochs from before a restore lie below every restored one and pass at once
    ([backup-and-restore.md](backup-and-restore.md) R-BAK-022).
  - The OpenAPI document (R-API-002) declares `Trove-Authz-Min-Epoch` as an optional header
    parameter of every authenticated operation, and `Trove-Authz-Epoch` on the responses of the
    writes above. The UI sends the request header after its own permission changes (R-UI-042).

Acceptance criteria:

- In the e2e profile `scatter` with Valkey stopped and `cluster.epoch_poll_interval: 10s` (a test
  setting that makes a stale snapshot visible), 100 runs of `POST /api/v1/repositories` creating
  `npm-new-<n>` followed at once by `npm publish --registry "$TROVE_URL/api/npm/npm-new-<n>/"`
  through the Service all succeed on the first attempt.
- In the same profile, a `PATCH` of a repository followed at once by a `GET` through another
  replica returns the new `ETag` in 100 of 100 trials.
- With Valkey running, a policy change made through one replica is enforced by every replica
  within 1 s (p99 over 100 trials), as R-ARCH-018's criterion measures.
- In the same profile, with an existing policy that grants `deploy` on `npm-*` to the publishing
  token, the publish of the first criterion succeeds although the replica it reaches learns of the
  new repository only through the lookup. 10,000 requests for random unknown keys on one replica
  cause at most 10,000 repository lookups and no change-log poll beyond the scheduled ones.
- In the profile `scatter` with the test settings of [authorization.md](authorization.md)
  R-AUTHZ-032's acceptance criteria (Valkey stopped, a long change-log poll interval), a
  `PUT /api/v1/permission-policies/{name}` that grants a group `deploy` on a local repository
  answers `Trove-Authz-Epoch: n`; 20 uploads of a member to `/files/…` through the Service with
  `Trove-Authz-Min-Epoch: n` all succeed on the first attempt. The value `abc` answers `400`
  `invalid_header`, the current epoch plus 1,000 answers `412` `authz_epoch_unknown`, and the same
  header without a credential gets the answer it would get without the header.

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
| `/api/v1/auth/cli-logins/…`, `/api/v1/auth/device/…` | Command-line login approval and device flow (P1; screens in R-UI-004) | R-AUTH-034, R-AUTH-036 | [identity-and-authentication.md](identity-and-authentication.md) |
| `/api/v1/ci/…`, `/api/v1/auth/oidc/token` | CI identity | R-API-035 | [gitlab-ci-oidc.md](gitlab-ci-oidc.md) |
| `/api/v1/settings/…`, `/api/v1/system/banner`, `/api/v1/system/maintenance`, `/api/v1/system/proxy-offline` | Settings | R-API-037 | the page per setting |
| `/api/v1/system/…` | Version, information, replicas, jobs, tasks, diagnostics, cache flush (P1), stored-secret key ring, backup runs | R-API-038, R-API-024, R-API-045, R-API-049, R-API-050 | [operations-and-observability.md](operations-and-observability.md), [architecture.md](architecture.md), [backup-and-restore.md](backup-and-restore.md) |
| `/api/v1/audit/…` | Audit events | R-API-039 | [operations-and-observability.md](operations-and-observability.md) |
| `/api/v1/storage/…` (summary, damaged-blobs; storage tasks run through `/api/v1/system/tasks/…`) | Garbage collection, orphan sweep, scrub, storage summary, damaged blobs | R-API-040 | [storage.md](storage.md) |
| `/api/v1/webhooks…` | Webhooks (P1) | R-API-041 | [operations-and-observability.md](operations-and-observability.md) |
| `/api/v1/builds…` | Build information (P1) | R-API-042 | the page `builds.md` (prefix `R-BLD`), written before P1 build work starts (Q-API-5) |
| `/api/v1/retention/…` | Retention and cleanup (P1) | R-API-043 | [storage.md](storage.md) |
| `/api/v1/config/…` | Configuration as code (P1) | R-API-044 | [operations-and-observability.md](operations-and-observability.md) |
| `/api/v1/scim/v2/…` | SCIM (P1) | R-API-046 | [identity-and-authentication.md](identity-and-authentication.md) |
| `/api/v1/projects…` | Projects (P1) | R-API-047 | [authorization.md](authorization.md) |
| `/api/v1/endpoints…` | Endpoints (read only) and their exposure rules, `check-exposure` | R-EP-016 | [endpoints.md](endpoints.md) |
| `/api/v1/problems/{code}`, `/api/v1/openapi.json`, `/api/v1/openapi.yaml` | Contract | R-API-009, R-API-002 | this page |

Routes are registered on the HTTP stack of R-ARCH-005 with method-qualified patterns. The browser
login flow under `/auth/` is not part of `/api/v1/`; the exchange endpoint
`/api/v1/auth/oidc/token` is fixed by INV-10.

**One path per function for maintenance work** (Q-API-9). Every singleton maintenance task of
R-OPS-019 in [operations-and-observability.md](operations-and-observability.md) is started only
through `POST /api/v1/system/tasks/{name}/run` and stopped only through
`POST /api/v1/system/jobs/{id}/cancel`, and its runs are listed by
`GET /api/v1/system/jobs?kind=<task>`. The page that owns a task's semantics defines its
parameters and job result, MAY add a read-only domain view of its runs that references the job,
and owns the read paths of its results (`GET /api/v1/storage/summary` and
`GET /api/v1/storage/damaged-blobs` in [storage.md](storage.md)). No page adds a second start,
stop or refresh path for a task. Per-resource actions (`/repositories/{key}/reindex`,
`/storage/damaged-blobs/{sha256}/recheck`) and the runs of retention policies, which have their own
run model with queueing and per-policy runs ([storage.md](storage.md) R-STOR-041), are not
singleton tasks and keep their resource paths.

Acceptance criteria:

- The route-parity check of R-API-002 passes.
- A request to every prefix in the table without credentials answers 401 or the documented
  anonymous result, never 500.
- Each function in this table has exactly one path in the OpenAPI document: the document contains
  no `/api/v1/maintenance/…` start or stop path, no `POST /api/v1/storage/summary/refresh` and no
  `/api/v1/system/storage/summary`, and `POST /api/v1/system/tasks/gc/run` starts a run.

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
`If-Match`. `user_preferences.revision` is the `ETag` and is registered with
`meta.RegisterRevisionColumn` (R-API-012, [backup-and-restore.md](backup-and-restore.md)
R-BAK-022).

Data model:

```sql
CREATE TABLE user_preferences (
  user_id            uuid PRIMARY KEY REFERENCES app_user (principal_id) ON DELETE CASCADE,  -- Q-API-15
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
| GET | `/api/v1/formats` | Format descriptors with classes (generated from `Descriptor().Classes`), clients and settings schema (R-ARCH-008, R-ARCH-010) | authenticated; anonymous where anonymous access is on |
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
  answers 201; then `npm view lodash version --registry "$TROVE_URL/api/npm/npm-remote/"`, sent
  at once through the Service to whichever replica it reaches, prints a version (R-API-052).
- A reader without `repositories:read-config` gets no upstream configuration in the answer.

### R-API-023 · Repository batch operations and quick setup
Priority: **P1** · Covers: API-053, API-055, API-064

Owned by [repositories-and-proxy.md](repositories-and-proxy.md):

| Method | Path | Purpose | Permission |
|---|---|---|---|
| POST | `/api/v1/repositories/batch-create` | Create several repositories, all or nothing | as create |
| POST | `/api/v1/repositories/batch-update` | Merge-patch several repositories with per-entry `ifMatch` (required per entry, R-API-012), all or nothing | as update |
| POST | `/api/v1/repositories/batch-get` | Read several configurations | as read, per key |
| POST | `/api/v1/repositories/batch-delete` | Delete several repositories, best effort; 202 with per-key job references | as delete |
| POST | `/api/v1/repositories/quick-setup` | Create local, remote and virtual repositories per format from a prefix (R-UI-023) | as create |

The sub-path verbs follow R-API-004 (Q-API-7); `batch-create`, `batch-update`, `batch-get`,
`batch-delete` and `quick-setup` are therefore reserved repository keys. All of them accept
`Idempotency-Key` (R-API-014).

Acceptance criteria:

- A `batch-create` whose third document is invalid answers 422 with a pointer into that document
  and creates nothing.

### R-API-024 · Repository maintenance actions
Priority: **MVP** · Covers: API-028, API-040, API-041, API-043, API-045, API-050

The requirement is MVP because cache invalidation is:
[operations-and-observability.md](operations-and-observability.md) R-OPS-021 and
[repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-045 are MVP, and
invalidation is the one way to cut a wrong or forged negative entry in Valkey short without a
Valkey credential (H-49 in [architecture.md](architecture.md)). The other rows keep the priority
of their owners:

| Method | Path | Purpose | Permission | Priority | Owner |
|---|---|---|---|---|---|
| POST | `/api/v1/repositories/{key}/reindex` | Rebuild the format's index and metadata from stored packages (npm package documents, NuGet indexes; later Maven metadata, PyPI simple index, Helm `index.yaml`); 202 with a job; 409 when the format has no index | `manage` | P1 | R-OPS-022, format pages |
| POST | `/api/v1/repositories/{key}/cache/invalidate` | Mark cached metadata (`scope` `metadata`), cached misses (`negative`) or both (`all`) of a remote or virtual repository under a path prefix as stale, on every replica within `cluster.epoch_poll_interval` plus 1 s, with or without Valkey; binaries stay cached (INV-11); `400` on a local repository | `manage`, or `admin` | MVP | R-OPS-021, [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-045 |
| GET | `/api/v1/repositories/{key}/cache/entries` | Cached metadata entries for diagnosis | `repositories:read-config` or `manage` | MVP | [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-032 |
| POST | `/api/v1/system/caches/flush` | Installation-wide: flush every registered in-process cache on every `web` and `worker` replica and invalidate the negative entries of every remote repository; deletes no Valkey key; a synchronous action, not a task of R-OPS-019 | `admin` | P1 | R-OPS-047 |

Acceptance criteria:

- After a test removes a computed npm package document, `POST …/npm-local/reindex` restores it and
  `npm view @team-a/ui versions` lists every stored version.
- `POST …/npm-remote/cache/invalidate` followed by `npm view lodash` makes trove fetch the package
  document from the upstream again (visible in the upstream mock's request log). With Valkey
  stopped, the same holds on another replica within `cluster.epoch_poll_interval` plus 1 s.
- The same invalidation with the token of a user who holds `read` and `delete` but not `manage` on
  `npm-remote` answers 403.

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

`keys` follows the repeated-parameter rule of R-API-011 (`keys=qa.status&keys=build.number`), like
every other multi-value parameter, so one parser and the generated clients' array form (OpenAPI
`style: form`, `explode: true`) serve every endpoint; a comma-separated list is not accepted.

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
  image_policy_sha256 bytea    NOT NULL,       -- hash of ui.readme.external_image_origins (R-UI-008)
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
  401 on every replica within 2 s (R-AUTH-023, R-API-052).

### R-API-032 · Access tokens
Priority: **MVP** · Covers: ADMAPI-039, ADMAPI-040, ADMAPI-043, ADMAPI-044, ADMAPI-045, ADMAPI-046

Owned by [identity-and-authentication.md](identity-and-authentication.md) (token model, scope
grammar, lifetimes, revocation):

| Method | Path | Purpose | Permission |
|---|---|---|---|
| POST | `/api/v1/tokens` | Create a personal token for the caller | user with session (recent authentication) or a PAT under the derivation rule |
| POST | `/api/v1/service-accounts/{name}/tokens` | Create a service-account token | `admin`; owners from P1; recent authentication for sessions |
| GET | `/api/v1/tokens` | List own tokens; all with `tokens:read` | `self`, `tokens:read` |
| GET | `/api/v1/tokens/{id}`, `/api/v1/tokens/current` | Read one token | `{id}`: owner, `tokens:read`; `current`: the presenting token itself, CI tokens included ([gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-013) |
| DELETE | `/api/v1/tokens/{id}`, `/api/v1/tokens/current` | Revoke | `{id}`: owner, owners of the service account (P1), `tokens:revoke`, `admin`; `current`: the presenting token itself, of every type, CI tokens (`trv_cit_`) included (R-AUTH-029; [gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-013) |
| DELETE | `/api/v1/users/me/tokens` | Revoke all own tokens | `self` |
| DELETE | `/api/v1/service-accounts/{name}/tokens` | Revoke all tokens of a service account | `admin` |
| POST | `/api/v1/tokens/revoke` | Revoke by value | anyone, no authentication |
| POST | `/api/v1/tokens/{id}/rotate`, `/api/v1/tokens/current/rotate` | Rotate (P1, R-AUTH-030); CI tokens cannot be rotated | PAT: its owner only, through a session with recent authentication or a token of the owner under the derivation rule; SAT: `admin`, owners of the service account from P1, or a SAT of the account under the derivation rule; `current/rotate`: the token itself, only if `selfRotatable`. `tokens:revoke` alone never rotates (R-AUTH-030) |

API conventions that apply: the secret appears only in the create and rotate answers, which are
`no-store`, uncompressed (R-API-017) and never stored for idempotency replay (R-API-014).

Acceptance criteria:

- `curl -s -X POST -H "Authorization: Bearer $TROVE_TOKEN" -H 'Content-Type: application/json' -d '{"description":"t","ttlSeconds":3600}' "$TROVE_URL/api/v1/tokens"`
  returns a value starting with `trv_pat_` (field names per
  [identity-and-authentication.md](identity-and-authentication.md)); `GET /api/v1/tokens/{id}`
  never contains it.
- `POST /api/v1/tokens/revoke` without credentials and with a valid token value revokes it.
- `DELETE /api/v1/tokens/current` with a `trv_cit_` token answers 204, and the same token then gets
  401 on every replica within 2 s (R-AUTH-023, R-API-052).
- A token whose scope is `{"system": ["tokens:revoke"]}` gets 403 `forbidden` from
  `POST /api/v1/tokens/{id of another user's PAT}/rotate` and 204 from `DELETE` of the same ID
  (P1, with rotation).

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

Every successful write of a policy or a role binding answers with `Trove-Authz-Epoch`, which a
later request names in `Trove-Authz-Min-Epoch` to see the change on any replica (R-API-052,
R-AUTHZ-032).

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
pasted tokens in the browser (R-UI-036). The exchange speaks OAuth to OAuth clients: its answers
carry `Cache-Control: no-store` and the field names of RFC 8693, and its errors are OAuth error
responses ([RFC 6749 §5.2](https://www.rfc-editor.org/rfc/rfc6749#section-5.2),
`Content-Type: application/json`, `Cache-Control: no-store`) with `error` and
`error_description` plus trove's members `reason` (left out under
`ci.exchange.expose_reason: false`) and `requestId`, not problem details (R-API-009). A rejected
subject token answers 400 `invalid_request` ([gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-008,
Q-CI-8).

Acceptance criteria:

- In a GitLab CI job using the snippet of `GET …/snippets/gitlab-ci?mapping=team-a-protected&format=npm`,
  the exchange succeeds and `npm publish` of `@team-a/ui` succeeds.
- An exchange with an expired ID token answers 400 with `Content-Type: application/json`,
  `Cache-Control: no-store`, `error` `invalid_request`, `reason` `token_expired` and a `requestId`
  equal to the `X-Request-Id` header, and no `type` or `title` member.
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
| GET | `/api/v1/system/info` | Installation facts: version, base URL and source, maintenance mode, replicas per role (live, ready, degraded, nodes) and per version, database and schema versions, Valkey installation facts (mode, primary name, number of configured Sentinels, TLS mode, key prefix, keyspace, degraded replicas), backup status | `system.read` |
| GET | `/api/v1/system/replicas` | Replicas with version, roles, node, state, heartbeat, readiness checks and each replica's view of Valkey (state, reason, circuit, primary, Sentinels reachable and configured, server version, last primary change) | `system.read` |
| GET | `/api/v1/system/tasks`, POST `/api/v1/system/tasks/{name}/run` | Scheduled tasks and manual runs (R-OPS-019) | `system.read`; `admin` to run |

**M0 stand-in until authentication ships (M1).** No principal holds `system.read` yet. The configuration
key `system.bootstrap_anonymous_read` (default `false`; `true` in the development profile) lets anonymous
callers read `/api/v1/system/info` and `/api/v1/system/replicas`. A request that presents an
`Authorization` header gets `401`. Startup refuses the key when `server.base_url` is an `https` origin other
than localhost, and a warning is logged when it is set. The key is removed with the identity work
(Q-API-14).
| GET | `/api/v1/system/jobs`, `/api/v1/system/jobs/{id}`, POST `…/cancel` | Jobs (R-OPS-019, R-API-015) | `system.read`; `admin` to cancel |

Liveness, readiness and Prometheus metrics stay on the operations listener (INV-6) and are not
duplicated under `/api/v1/`; this covers ADMAPI-069 and API-033 by reference.

The replica registry and the system information are owned by
[operations-and-observability.md](operations-and-observability.md) R-OPS-003 and R-OPS-004, which
define their shapes; this page adopts them unchanged and lists what the system status screen
(R-UI-038, Q-UI-7) reads. A disagreement between this list and those requirements is settled in
their favour.

- **Per replica** (`/system/replicas`, R-OPS-003):
  - `roles`: `["web"]`, `["worker"]`, or `["all"]` for the development role (INV-2); from P1 also
    the pool of [ha-and-helm.md](ha-and-helm.md) R-HA-062;
  - `state` (`starting`, `ready`, `unready`, `draining`) and `lost`;
  - `node`, the Kubernetes node name from the downward API (`spec.nodeName` through the
    environment variable `TROVE_NODE_NAME`), `null` outside Kubernetes;
  - `checks`, the status of each gating and reported check (`pass`, `fail`, `degraded`), and
    `degraded`, the reported checks that fail (R-OPS-001);
  - `valkey`, the replica's view of Valkey as one nested object:

    | Field | Content |
    |---|---|
    | `state` | `available` or `degraded` |
    | `reason` | `null`, or the reason of the `valkey` check in R-OPS-001's vocabulary: an error kind of `trove_valkey_errors_total` (`timeout`, `connection`, `readonly`, `auth`, `tls`, `oom`), `no_primary` or `unsupported_version` |
    | `circuit` | The state of the replica's Valkey circuit breaker ([ha-and-helm.md](ha-and-helm.md) R-HA-057), for example `closed` or `open` |
    | `primary` | The primary address (host and port) the replica's client currently uses; `null` while it has none |
    | `sentinelsReachable`, `sentinelsConfigured` | The Sentinels this replica reaches, and the number configured |
    | `serverVersion` | The Valkey server version the replica's client sees |
    | `lastPrimaryChangeAt` | The replica's last change of primary |

  No credential, ACL user name or key appears. The nested object replaces the flat fields
  `valkeyPrimary`, `valkeyPrimarySince` and `valkeySentinelsReachable` of an earlier revision of
  this page, and R-OPS-001's reasons replace `unreachable`, `circuit_open` and `rejected`: an open
  circuit is `circuit: "open"`, not a reason.
- **Per installation** (`/system/info`, R-OPS-004):
  - `replicas.live`, `replicas.ready`, `replicas.versions`, and `replicas.roles.<role>` with
    `live`, `ready`, `degraded` and `nodes` for each role; this replaces `replicas.byRole`;
  - the `valkey` block with installation facts only: `mode`, `primaryName`, `sentinels` (the
    number configured), `tls`, `keyPrefix`, `keyspace` and `degradedReplicas`. `serverVersion` and
    each replica's client state are not in it; they live in the registry above. No Sentinel
    address and no credential appears in either answer;
  - the `backup` block: `expectedInterval` (in R-OPS-004's documented duration form, for example
    `"24h"`), `lastSuccessAt`, `overdue` and `lastRunState`, read from `backup_run`. `overdue`
    follows the rule of R-API-050 (1.5 × the expected interval), so this block, the backup summary
    of R-API-050, the UI and `TroveBackupStale` agree.
- **Drain safety.** The screen decides whether a role is drain-safe from the items of
  `/system/replicas`: its ready replicas that are not `lost`, and their distinct nodes (R-UI-038).
  `replicas.roles.<role>.nodes` is the installation-wide summary for scripts and monitoring.

Acceptance criteria:

- With two `web` and two `worker` replicas running, `GET /api/v1/system/replicas` lists all four
  with their roles and nodes, and `GET /api/v1/system/info` reports `replicas.roles.web.live` and
  `replicas.roles.worker.live` as `2`; the system status screen (R-UI-038) renders from these
  answers, the task and job API, R-API-049 and R-API-050 without other endpoints.
- A replica started with the development role lists `"roles": ["all"]`.
- With Valkey stopped, every item lists `valkey` under `degraded`, with `valkey.state`
  `degraded` and `valkey.reason` `connection` or `timeout`, within `health.failure_threshold` ×
  `valkey.health_check_interval` plus `system.heartbeat_interval` plus 1 s; every item's `state`
  stays `ready`; and `/system/info` reports `valkey.degradedReplicas` equal to `replicas.live`.
- With three Sentinels configured and two of them stopped, every item's
  `valkey.sentinelsReachable` is 1 and `valkey.sentinelsConfigured` 3 within
  `valkey.sentinel.refresh_interval` plus `system.heartbeat_interval`, while `valkey.state` stays
  `available` and the `valkey` check `pass`, because the primary still answers.
- No answer of `/system/info` contains `serverVersion` under `valkey`, and the OpenAPI document
  contains none of `valkeyPrimary`, `valkeyPrimarySince`, `valkeySentinelsReachable` and
  `replicas.byRole`.

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

Semantics owned by [storage.md](storage.md); starting and stopping use the task and job endpoints
of R-OPS-019 (one path per function, Q-API-9 and R-API-020):

| Method | Path | Purpose | Permission |
|---|---|---|---|
| POST | `/api/v1/system/tasks/gc/run`, `/api/v1/system/tasks/gc-full/run` | Start an incremental or a full collection run (`dryRun`); 202 with the job; 409 with the active job while any collection run is queued or running | `admin` |
| GET | `/api/v1/system/jobs?kind=gc`, `?kind=gc-full`, `/api/v1/system/jobs/{id}` | Runs; each job's result carries the counts and reclaimed bytes defined by [storage.md](storage.md) | `system.read` |
| POST | `/api/v1/system/jobs/{id}/cancel` | Stop a run after the current batch | `admin` |
| GET | `/api/v1/storage/summary` | Blob and repository usage (P1, R-STOR-028) | `storage.read` or `admin` |
| POST | `/api/v1/system/tasks/storage-summary/run` | Recompute; 202 with the job (P1) | `admin` |
| POST | `/api/v1/system/tasks/orphan-sweep/run`, `/api/v1/system/tasks/scrub/run` | Start an orphan sweep or an integrity scrub (P1, R-STOR-026, R-STOR-027) | `admin` |
| GET | `/api/v1/storage/damaged-blobs` | Damaged blobs found by the scrub, with their referencing items (P1, R-STOR-027) | `storage.read` or `admin` |
| POST | `/api/v1/storage/damaged-blobs/{sha256}/recheck` | Re-run both checks for one blob; a per-resource action, not a task (P1, R-STOR-027) | `admin` |

Acceptance criteria:

- `curl -X POST -H "Authorization: Bearer $ADMIN_TOKEN" -H 'Content-Type: application/json' -d '{"dryRun":true}' "$TROVE_URL/api/v1/system/tasks/gc-full/run"`
  answers 202; `POST …/system/tasks/gc/run` while it runs answers 409 with the same job; after the
  job ends, `GET /api/v1/system/jobs?kind=gc-full` lists it with its result counts.

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

Build information gets its own spec page, `builds.md` with prefix `R-BLD`, written before P1
build work starts (Q-API-5); it owns the record schema, the mapping of CI job names to
`{buildName}`, promotion and retention of runs, and uses the build permissions of R-AUTHZ-027 in
[authorization.md](authorization.md). Until it exists, this page reserves the surface so that the
paths do not move, and the MVP implements none of it. `GET`, `PUT` and `DELETE` of
`/api/v1/builds/{buildName}/retention` are already specified by [storage.md](storage.md) R-STOR-043.

| Method | Path | Purpose | Permission |
|---|---|---|---|
| GET | `/api/v1/builds` | Build names; `q`, `sort` | read on the build |
| GET | `/api/v1/builds/{buildName}/runs` | Runs, newest first | read on the build |
| PUT | `/api/v1/builds/{buildName}/runs/{number}` | Upload a build record; replacing an existing run needs `overwrite` | deploy on the build |
| GET | `/api/v1/builds/{buildName}/runs/{number}` | A run; `latest` as number alias; `compareTo={number}` adds a diff | read on the build |
| DELETE | `/api/v1/builds/{buildName}/runs/{number}` | Delete; `deleteArtifacts=true` also deletes linked artifacts (202) | delete on the build |
| POST | `/api/v1/builds/{buildName}/runs/{number}/promote` | Set status, optionally copy or move artifacts and dependencies to a target repository, set properties; `dryRun` | deploy on the build, plus the permissions of copy and move |
| GET | `/api/v1/builds/{buildName}/runs/{number}/artifacts` | Artifacts with download URLs; optional `pattern` (repeat) | read on the build |
| GET, PUT, DELETE | `/api/v1/builds/{buildName}/retention` | Retention of runs ([storage.md](storage.md) R-STOR-043): `GET` reads the stored parameters (404 when none); `PUT` stores them and applies them at once through a `build-retention` job, answering 202 with the job, or with `"wait": true` waiting up to `retention.build_retention_wait` and then answering 200 with the job result if it finished, otherwise 202; `DELETE` removes the parameters (204) and no run | `GET`: read on the build; `PUT`, `DELETE`: delete on the build |

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
list) with `/{id}`, `/{id}/download` and DELETE. Leases and the other advisory locks live in
PostgreSQL ([architecture.md](architecture.md) R-ARCH-017), so one query answers for the whole
cluster. The flight locks that collapse remote fetches live in Valkey (R-ARCH-033), last at most
`cluster.flight_lock_ttl` past their holder's last extension and carry only hashes, so the lock
list does not show them; their metrics (`trove_flight_lock_lost_total`,
`trove_flight_lock_bypass_total`) are the diagnostic. R-OPS-026 lists the PostgreSQL category
`fetch` as retired, and R-ARCH-017 keeps its number reserved.

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
(see Deferred). Project writes, administrators and repository assignments included, answer with
`Trove-Authz-Epoch` (R-API-052, R-AUTHZ-032).

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

### R-API-049 · Stored-secret key-ring status
Priority: **MVP** · Covers: HA-016

The key ring that seals stored secrets is configured through mounted files (R-ARCH-021 in
[architecture.md](architecture.md)) and rotated in steps (R-HA-027 in
[ha-and-helm.md](ha-and-helm.md)). This requirement makes its state readable through the API, so
the UI, monitoring and an operator can check and finish a rotation without a shell in a pod, which
would expose the key files themselves (Q-API-8). `trove admin secrets status` and this endpoint
share one implementation in `internal/secrets`; this page owns the payload.

| Method | Path | Purpose | Permission |
|---|---|---|---|
| GET | `/api/v1/system/encryption` | Key IDs, sealed-value counts per key ID, canary state, the key IDs every live replica holds, last re-encryption | `system.read` |
| POST | `/api/v1/system/tasks/secrets-reencrypt/run` | Start the re-encryption of R-ARCH-021 as the manual task `secrets-reencrypt` (no schedule; its job kind has the same name) through the task trigger of R-OPS-019 (Q-API-9); 202 with the job; 409 with the running job; 409 `key_ring_incompatible` while `ringsCompatible` is false | `admin` |

```json
{
  "answeredBy": "trove-web-6f9c7d8b5-x2v4q",
  "activeKeyId": "2026-10",
  "keys": [
    {"id": "2026-10", "active": true, "sealedValues": 14, "canary": "ok"},
    {"id": "2026-04", "active": false, "sealedValues": 3, "canary": "ok"}
  ],
  "replicas": [
    {"name": "trove-web-6f9c7d8b5-x2v4q", "role": "web", "activeKeyId": "2026-10", "keyIds": ["2026-04", "2026-10"]},
    {"name": "trove-worker-7d4b9c6f8-q8n2m", "role": "worker", "activeKeyId": "2026-10", "keyIds": ["2026-04", "2026-10"]}
  ],
  "ringsCompatible": true,
  "lastReencryption": {"jobId": "01933b2a-7c41-7d1e-9a52-3f0e4b8c2d11", "state": "succeeded", "finishedAt": "2026-10-28T12:40:02.118Z"}
}
```

- `activeKeyId` and `keys[].canary` are the answering replica's view, and `answeredBy` names that
  replica, as in R-OPS-004. Requests reach replicas at random, so during a rotation two refreshes
  can show different values; the UI therefore shows each replica's active key from `replicas` and
  labels the top-level values with `answeredBy`. `canary` is `ok`, `failed` or `absent` (no canary
  row for that key ID yet). `keys` lists the union of the answering
  replica's ring and every key ID that sealed values reference. `sealedValues` counts sealed values
  per key ID over every sealed column registered with `internal/secrets`, the same registry the
  re-encryption job walks.
- `replicas` comes from the replica registry (R-OPS-003 in
  [operations-and-observability.md](operations-and-observability.md)): each `web` and `worker`
  replica publishes its role, the key IDs of its ring and its active key ID with its heartbeat.
  Both roles open sealed values (`web` for upstream credentials on remote fetches, `worker` for
  webhook secrets and re-encryption), so both count. Lost replicas are left out. Backup and restore
  pods do not register (R-HA-003 in [ha-and-helm.md](ha-and-helm.md)); a backup copies sealed
  values without the ring, and a restore checks the ring itself
  ([backup-and-restore.md](backup-and-restore.md) R-BAK-020).
- `ringsCompatible` is true when every live replica's ring holds every key ID that is active on
  any live replica or referenced by a sealed value. False is the state in which a value sealed by
  one replica cannot be opened by another, for example when step 2 of R-HA-027 rolls out before
  step 1 reached every replica.
- The answer contains key IDs only: never key material, key fingerprints, ciphertext or plaintext.
- The re-encryption task refuses to start while `ringsCompatible` is false, because it would seal
  values that some replica cannot open. It needs no recent authentication: it changes no grant and
  reveals nothing. The request is audited like every write (R-API-019).
- The check is as fresh as the registry: a replica that started within the last heartbeat interval
  may not be listed yet. The start-up canary of R-ARCH-021 stays the hard guard; this endpoint and
  the task's refusal catch the mixed-ring state of a running cluster, which the canary cannot see.
- Monitoring reads the same state as metrics on the operations listener:
  `trove_secrets_sealed_values{key_id}` and `trove_secrets_key_rings_compatible` (0 or 1), defined
  by [operations-and-observability.md](operations-and-observability.md).

Acceptance criteria:

- With replica A running on the old ring and replica B started afterwards with a new key active
  that A's ring lacks, `GET /api/v1/system/encryption` shows `ringsCompatible: false`, and
  `POST /api/v1/system/tasks/secrets-reencrypt/run` answers 409 `key_ring_incompatible`.
- After the rotation steps of R-HA-027 and a successful re-encryption run, `keys` reports
  `sealedValues: 0` for the old key ID, the same count that `trove admin secrets status` prints.
- No answer of the endpoint contains the base64 content of any key file.

### R-API-050 · Backup runs, status and stored backups
Priority: **MVP** · Covers: OPS-032

The backup job (INV-19) runs in the `backup` role as a Kubernetes CronJob; its runs, holds and
retention are [backup-and-restore.md](backup-and-restore.md) (R-BAK-009 to R-BAK-017). This
requirement owns the read-only API through which the UI (R-UI-038, R-UI-048) and scripts see them.
Every value comes from PostgreSQL (`backup_run`, `system_state`) and the replica's configuration;
`web` replicas hold no target credential and never read the target (R-BAK-015, Q-API-11).

| Method | Path | Purpose | Permission |
|---|---|---|---|
| GET | `/api/v1/system/backups` | Runs, newest first; filters `operation` (`create`, `delete`, `verify`), `state`, `trigger`, `startedFrom`, `startedTo`; cursor-paged (R-API-010), `includeTotal` allowed | `system.read` |
| GET | `/api/v1/system/backups/{runId}` | One run | `system.read` |
| GET | `/api/v1/system/backups/summary` | Status of the backup job: last success, overdue, last failure, active run, stored backups and the retention that produced them, target, restore origin, manual-run command | `system.read` |

No endpoint starts, deletes, verifies or restores a backup, in any priority (Q-API-10); a manual
run is `kubectl create job --from=cronjob/<name>`, which R-UI-048 shows. `{runId}` is a
server-generated UUID, so `summary` needs no reservation (R-API-004). The `backup` block of
`/api/v1/system/info` (R-OPS-004, R-API-038) repeats the last success, the overdue mark and the
state of the newest run in short form, under the same overdue rule.

Run (an item of the list):

```json
{
  "id": "01933c4e-2f10-7b3a-9c1d-5e8f7a6b4c21",
  "operation": "create",
  "backupId": "20261028T020000Z-7f3a",
  "state": "copying",
  "trigger": "schedule",
  "pod": "trove-backup-29885880-x7k2q",
  "troveVersion": "0.4.0",
  "target": "s3://trove-backups/trove",
  "startedAt": "2026-10-28T02:00:04.112Z",
  "heartbeatAt": "2026-10-28T02:41:19.870Z",
  "leaseExpired": false,
  "finishedAt": null,
  "snapshotAt": "2026-10-28T02:00:09.448Z",
  "progress": {"phase": "copying", "tablesDone": 41, "tablesTotal": 58, "blobsToCopy": 1204, "blobsCopied": 830, "bytesCopied": 9123456789},
  "result": null,
  "error": null
}
```

- The fields are the columns of `backup_run` (R-BAK-009) in the names of R-API-003. `target` is
  the column as recorded (bucket, prefix and endpoint host, never a credential). `pod` tells a run
  started by hand from a scheduled one, because a Job created with
  `kubectl create job --from=cronjob/…` carries the name its creator chose (R-BAK-012).
- `leaseExpired` is true for a run in an active state whose `lease_expires_at` has passed: its
  process has stopped, and the next run will mark it `abandoned` (R-BAK-010). The API never changes
  the row itself.
- `error` is the stored problem object; the UI translates its `code` (R-UI-042).

Summary:

```json
{
  "expectedIntervalSeconds": 86400,
  "lastSuccessAt": "2026-10-27T02:47:31.009Z",
  "overdue": false,
  "lastFailure": null,
  "active": {"id": "01933c4e-2f10-7b3a-9c1d-5e8f7a6b4c21", "operation": "create", "state": "copying", "startedAt": "2026-10-28T02:00:04.112Z", "leaseExpired": false},
  "target": "s3://trove-backups/trove",
  "retention": {"keepLast": 7, "keepWithinSeconds": 0},
  "storedBackups": {
    "asOf": "2026-10-27T02:47:31.009Z",
    "runId": "01933a1b-8c22-7e41-b0f3-2a6d9e4c7b10",
    "items": [
      {"backupId": "20261027T020000Z-1c9e", "snapshotAt": "2026-10-27T02:00:08.913Z", "troveVersion": "0.4.0", "schemaStep": 42, "databaseBytes": 183500800, "blobCount": 51234, "blobBytes": 412316860416, "damaged": false}
    ]
  },
  "restoredFrom": null,
  "manualRun": {"namespace": "trove", "cronJob": "trove-backup"}
}
```

- `expectedIntervalSeconds` is `backup.expected_interval`, the value behind
  `trove_backup_expected_interval_seconds` and the alert `TroveBackupStale` (R-BAK-014).
- `lastSuccessAt` is `finished_at` of the newest `completed` or `completed_with_warnings` create
  run. `overdue` is true when it is older than 1.5 × the expected interval, the condition on which
  `TroveBackupStale` fires, so API, UI and alert agree. A mark at 1 × would flap on every
  schedule whose runs vary in length: a daily run that finishes a few minutes later than the day
  before is older than 24 h for those minutes. With no successful run at all, `lastSuccessAt` and
  `overdue` are `null`, and the UI shows "No backup yet" as a warning.
- `lastFailure` (`runId`, `at`, `code`) is the newest `failed` or `abandoned` create run, or an
  active create run whose lease has expired, when it is newer than the last success; `interrupted`
  runs never count, because a drain is not a failure. This is the same rule as
  `trove_backup_last_failure_timestamp_seconds` and `TroveBackupFailed`.
- `storedBackups` and `retention` come from the newest create or delete run that listed the
  target: the complete backups it found after its own deletions, and the `keep_last` and
  `keep_within` it applied (Q-API-11; the run result's `retained` and `retention`, cross-page to
  R-BAK-016). `asOf` and `runId` name that run, so the view says how old it is. Backups written or
  removed outside trove appear only after the next run; `trove backup list` reads the target
  itself (R-BAK-018). `null` until such a run exists.
- `restoredFrom` is `system_state.restored_from` (`installationId`, `incarnation`, `backupId`,
  `snapshotAt`) after a restore (R-BAK-022), otherwise `null`.
- `manualRun` holds the namespace and the CronJob name that the chart renders into the
  configuration (`backup.manual_run`, cross-page), so the UI can show the exact command; each is
  `null` when unset.

```yaml
backup:
  manual_run:
    namespace: ""        # default (proposed): empty; the chart sets the release namespace, example: trove
    cronjob: ""          # default (proposed): empty; the chart sets <fullname>-backup, example: trove-backup
```

Acceptance criteria:

- During a run, `GET /api/v1/system/backups?state=copying` lists it with growing
  `progress.blobsCopied` (the criterion of R-BAK-009).
- With `backup.retention.keep_last: 1`, after two completed runs A and B, the summary's
  `storedBackups.items` lists B's backup only, and `storedBackups.runId` is B.
- A backup pod deleted with `--grace-period=0 --force` during `copying`: once its lease has passed,
  the run shows `leaseExpired: true`, and the summary's `lastFailure` names it, until a later run
  succeeds.
- A run ended `interrupted` by a drain, followed by a successful retry, never appears as
  `lastFailure`.
- With `backup.expected_interval: 1h` and a test clock, `overdue` is `false` 80 minutes after the
  last success and `true` at 91 minutes, the same moments at which a `promtool` test of
  `TroveBackupStale` is silent and fires.
- `GET /api/v1/system/backups/summary` without `system.read` answers 403. No answer of these
  endpoints contains a value of the target's credentials (a test greps for the fixture's access
  key ID and secret).
- The OpenAPI document contains no POST, PUT, PATCH or DELETE operation under
  `/api/v1/system/backups`.

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
  `Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self' 'nonce-<n>'; img-src 'self' data:<image origins>; font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'; require-trusted-types-for 'script'; trusted-types angular angular#bundler`,
  `X-Content-Type-Options: nosniff`, `Referrer-Policy: same-origin`,
  `Cross-Origin-Opener-Policy: same-origin`. `<image origins>` is empty by default. It holds the
  exact origins of `ui.readme.external_image_origins` (R-UI-008, Q-UI-6) and, when
  `server.content_base_url` is set (P1, R-ARCH-029 in [architecture.md](architecture.md)), that
  content origin, because README images that the renderer generates for package files are
  redirected there. Each origin is preceded by a space; no wildcard is ever emitted.
  `connect-src` stays `'self'` with or without a content origin, because the UI never reads package
  content with `fetch` or XHR (below). Without the `angular#unsafe-bypass` policy, a value marked
  trusted through `bypassSecurityTrust*` cannot reach a sink that Trusted Types guard
  ([Angular security guide](https://angular.dev/best-practices/security)), and without
  `angular#components` the CDK cannot insert SVG markup. Sign-in navigates to
  `/auth/…` and the identity provider by top-level navigation, which `form-action` does not govern.
- The UI's own code and assets come from nothing but trove's origin (fonts and icons self-hosted,
  no analytics, no CDN), so it works in air-gapped installations; the only foreign requests are
  README images from origins an operator listed (R-UI-008). From P1, with `server.content_base_url`
  set, package content opened or shown in the browser loads from trove's content origin
  (R-ARCH-029); that origin is trove's own, not a foreign one.
- Links to package content (R-ARCH-007, R-ARCH-029 in [architecture.md](architecture.md)):
  - The UI never assembles a `/files/` or `/api/<format>/` URL from strings. Downloads, "Copy
    download URL" and file links use the `downloadUrl` of the item or the version file
    (R-API-025, R-API-029); README images are the URLs the server's renderer generated through
    `LinkBuilder` (R-UI-008); registry and feed URLs come from the client setup answer
    (R-API-030). Moving content to a content origin (P1) therefore changes no UI code.
  - The UI never reads package content with `fetch` or XHR. Package content reaches the browser
    only as a navigation or an `img` load, so the content origin needs no CORS and `connect-src`
    stays `'self'`.
  - The upload of the deploy dialog (R-UI-012, P1) is not a content link but the API operation
    `PUT /files/{repoKey}/{path}` of R-API-025. It always targets the UI's own origin, because the
    content origin serves only `GET` and `HEAD` (R-ARCH-029), and it is built in one place, the
    `files` client in `web/src/app/api/`, like every other API call.
- Component library (Q-UI-1): Angular Material and the Angular CDK in the release line of the
  Angular major in use (22.x, pinned once in the lockfile with the rest of Angular), plus
  `@angular/aria` for the widgets Material lacks (the tree of R-UI-010 and the listboxes of the
  member editor, R-UI-022). No other component
  suite is added. Material is themed through its design tokens (`mat.theme`,
  [Angular Material theming](https://material.angular.dev/guide/theming)) mapped to trove's CSS
  custom properties for both themes. Icons come from a self-hosted icon font used through
  ligatures (`font-src 'self'`); SVG icon registration through `MatIconRegistry` is not used,
  because it takes URLs only as `SafeResourceUrl` values from a security bypass (banned by
  R-UI-046) and inserts the SVG through the Trusted Types policy `angular#components`, which this
  CSP does not list.
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
- A Playwright test records every request of a full journey (with no image origins and no content
  origin configured) and finds none to a foreign origin and no `securitypolicyviolation` event.
- The same journey shows no `fetch` or XHR request to a `/files/` or `/api/<format>/` path other than
  the upload of R-UI-012.
- With `server.content_base_url: https://trove-content.example.net` (# example, P1), the CSP's
  `img-src` names `https://trove-content.example.net`, and a README with an image from its own
  package renders that image without a `securitypolicyviolation` event.
- `grep -rn "bypassSecurityTrust\|addSvgIcon" web/src` finds nothing.
- The production build fails when the initial bundle exceeds the budget.

### R-UI-002 · Application shell
Priority: **MVP** · Covers: UI-001, UI-004

- Header: product name, global search (P1, R-UI-015), project filter (P1, R-UI-041), help link
  (`ui.help_url`), user menu. A configurable server name is P2 on
  [operations-and-observability.md](operations-and-observability.md).
- Navigation with two areas: **Packages and files** (packages, browser, search, trash, builds) and
  **Administration** (shown when `/api/v1/authz/me` reports any administrative capability). The
  sections are the routes of R-UI-003.
- Layout and density (Q-UI-2): the navigation is a left side bar below the header, permanently
  visible at viewport widths of 1024 CSS px and more, and a drawer opened from a menu button in the
  header below that width. Tables, lists and form fields use compact density (Angular Material
  density scale `-2`) on every screen; interactive targets stay at least 24 × 24 CSS px
  (R-UI-044). Screen layouts that switch between one and two columns use CSS container queries on
  the content area, so collapsing the side bar never breaks them.
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
- At 1280 CSS px the side bar is visible without interaction; at 800 CSS px it is hidden and the
  header's menu button opens it as a drawer that traps focus until closed.

### R-UI-003 · Route map and deep links
Priority: **MVP** · Covers: UI-002

Every screen and every object has a bookmarkable URL under `/ui/{locale}`. Filters, sort order,
selected tab and selected version live in query parameters.

<details>
<summary>Route table (28)</summary>

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
| `/admin/endpoints`, `/admin/endpoints/{endpoint}`, `/admin/endpoints/{endpoint}/rules/new`, `/admin/endpoints/{endpoint}/rules/{name}` | Endpoints and exposure rules | [endpoints.md](endpoints.md) R-EP-017 |
| `/admin/system`, `/admin/system/jobs`, `/admin/system/backups` | System status, jobs, backups | R-UI-038, R-UI-048 |
| `/admin/settings/auth`, `/admin/settings/general`, `/admin/settings/sign-in` (P1), `/admin/settings/storage` (P1) | Settings | R-UI-031 to R-UI-034 |
| `/admin/projects…` | Projects (P1) | R-UI-041 |
| `/login` (`?local=1`), `/logged-out` | Sign-in, signed out | R-UI-004 |
| `/cli-login/{publicId}` | Command-line login approval of `npm login` (P1; R-AUTH-034) | R-UI-004 |
| `/device` (`?code=`) | Device-code login approval for other command-line tools (P1; R-AUTH-036) | R-UI-004 |
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
  `redirectTo`, or to `/logged-out`.
- `/logged-out` is where the post-logout redirect URI `<baseURL>/ui/logged-out` lands. R-AUTH-003
  and R-AUTH-013 register that URI at the identity provider, and the locale redirect of R-UI-001
  takes it to `/ui/{locale}/logged-out`. Because the URI is registered at the IdP, the route name
  is a contract: it does not follow the UI wording, whose visible text stays "Signed out". The
  page needs no session. A 401 from `whoami` on this route does not navigate to `/login`, and the
  page never sends the browser to an identity provider on its own; otherwise auto-redirect would
  sign the user straight back in while the IdP session is still alive. The page offers "Sign in
  again", which opens `/login`.
- Sign-out in one tab signs out every other tab of the same browser (`BroadcastChannel`).
- Command-line login approval (P1). The flows, the `cli_login` record and the texts are owned by
  R-AUTH-034 and R-AUTH-036 in [identity-and-authentication.md](identity-and-authentication.md).
  Both screens need a session; without one the UI starts the sign-in with `return_to` set to the
  screen. Approve and deny send the CSRF header and need recent authentication (R-API-007).
  - `/cli-login/{publicId}` is the `loginUrl` that `npm login` opens. It renders
    `GET /api/v1/auth/cli-logins/{publicId}`: the client kind and registry URL; the requesting
    address, user agent and request time; the resulting token's lifetime and scope; the warning to
    approve only a login the user just started; a prominent warning when the browser's address
    differs from the requesting address; and every refused poll with its address, its time and
    the text of R-AUTH-034. While the screen is open it refreshes the record every 2 seconds, so a
    refusal before or after the decision appears without a reload; leaving the screen stops the
    refresh. "Approve" (with an optional narrower scope or shorter lifetime) and "Deny" are final.
  - `/device` is the `verification_uri` of the device flow; `verification_uri_complete` adds
    `?code=`. The screen takes the user code as R-AUTH-036 requires, then shows the same record
    plus the tool's `client_id`. When the tool asked for `self_rotatable=true`, the screen shows
    that request, explains that the token could then renew itself, and lets the approver decline
    it; the issued PAT records the outcome as `selfRotatable`.

Acceptance criteria:

- With one provider and auto-redirect, opening `/ui/en/packages` unauthenticated lands at the
  identity provider and, after login, back on `/ui/en/packages`.
- Signing out in tab A shows `/logged-out` in tab B within two seconds.
- With one provider, auto-redirect on, anonymous access off and `rp_initiated_logout` false,
  signing out lands on `/ui/{locale}/logged-out` and stays there. The browser sends no request to
  the identity provider until the user selects "Sign in again".
- (P1) With `/ui/en/cli-login/{publicId}` open, a poll of the login's `doneUrl` from outside the
  starting network appears on the screen as a refused poll with its address within 4 seconds,
  without a reload.
- (P1) On `/ui/en/device`, approving a request with `self_rotatable=true` and declining the
  self-rotation issues a PAT whose `POST /api/v1/tokens/current/rotate` answers 403.

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
- Layout (Q-UI-3): the header spans the full width. When the content area is at least 960 CSS px
  wide, two columns follow: the tabs on the left, the install panel (R-UI-009) on the right, which
  stays in view while the README scrolls (`position: sticky`). Narrower, one column: header,
  install panel, then the tabs, so the install command never sits below a long README.
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
- At a viewport of 390 CSS px the install command is visible without scrolling past the README; at
  1440 CSS px it stays visible after scrolling to the end of a long README.

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
| `img` | `src` per the image policy (Q-UI-6): an `https` image whose origin is listed in `ui.readme.external_image_origins` is kept (the UI document's `Referrer-Policy: same-origin` of R-UI-001 keeps the page URL, which names the package, from reaching that host); every other external image is replaced by a link "External image: <alt>"; `data:` sources dropped; sources that resolve to trove's own origin, relative or absolute, dropped as well, except image URLs the renderer itself generates for files embedded in the package, so a README cannot make the viewer's browser send authenticated requests to package or file endpoints ([authorization.md](authorization.md) R-AUTHZ-008). The renderer builds those URLs only through `LinkBuilder` ([architecture.md](architecture.md) R-ARCH-007), never from strings, so from P1 a content origin (R-ARCH-029) receives them without a renderer change; `alt`, `width`, `height` |
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
- Libraries (pinned once in `go.mod`): [goldmark](https://github.com/yuin/goldmark) for Markdown
  and [bluemonday](https://github.com/microcosm-cc/bluemonday) for sanitizing.
- External image origins (Q-UI-6) are exact `https` origins (scheme, host, optional port; no
  path, no wildcard), validated at start-up; an invalid entry stops start-up. The same list feeds
  the sanitizer and the CSP `img-src` of R-UI-001, so the two can never disagree. Because the
  rendered HTML depends on the list, the README cache of R-API-029 stores the list's hash and
  re-renders when it changes.
- Package images (P1, R-ARCH-029): with `server.content_base_url` set, a browser's request for an
  image the renderer generated is redirected to the content origin, which the CSP `img-src` of
  R-UI-001 then lists. The list of external image origins above is unaffected; the content origin
  is never entered there.

Configuration:

```yaml
ui:
  readme:
    max_source_bytes: 524288          # default (proposed), 512 KiB
    external_image_origins: []        # default (proposed): every external image becomes a link
    # external_image_origins: ["https://img.shields.io", "https://gitlab.example.com"]   # example
```

Acceptance criteria:

- A fixture corpus of XSS vectors (script tags, event-handler attributes, `javascript:` links, SVG
  with script, `<form>`, `<base>`, `<style>`, `id="__proto__"`) rendered by the server contains none
  of them in executable form; a Playwright test displays each fixture and observes no dialog, no
  request to a foreign origin and no CSP violation.
- A README with a badge image from `https://img.shields.io` shows "External image: build status" as
  a link while `external_image_origins` is empty; with that origin listed, the image loads, the
  UI's CSP names exactly that origin in `img-src`, and an image from an unlisted host in the same
  README is still a link.
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
  type-ahead), built on the tree pattern of `@angular/aria` (R-UI-001, Q-UI-1).

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
| Invalidate cache (scope metadata, negative or all, R-API-024) | Remote or virtual repository | `manage` | MVP |
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
- Uploads use `PUT /files/…` on the UI's own origin, built by the `files` client of
  `web/src/app/api/` (R-UI-001), never by the dialog. Because the fetch backend reports no upload
  progress, uploads go through an XHR-based `HttpClient` instance (`withXhr()`) to show progress.
  This is the only XHR use for `/files/`, and it writes; the UI never reads package content with
  `fetch` or XHR (R-UI-001).
- Files up to 256 MiB are hashed in the browser (Web Crypto SHA-256) and sent with `Repr-Digest`;
  larger files rely on the server's own digest.
- Several files upload one after another with per-file status; "Cancel" aborts the current one.
- An upload cut without an answer (a drain that reaches the shutdown deadline, R-API-051, Q-ARCH-18
  in [architecture.md](architecture.md)) has stored nothing ([storage.md](storage.md) R-STOR-004).
  The dialog sends it once more from the start through the Service, from the `File` object it
  still holds; a raw deploy of identical content to the same path is idempotent. If the retry
  answers that the path exists, the dialog compares the item's SHA-256 with the digest it computed
  and reports success when they match.

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
     For npm, the Publish tab also holds the GitLab CI snippet in its two labelled variants
     (R-UI-017).
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

GitLab CI publishing. The Publish tab shows the GitLab CI snippet of
[formats/npm.md](formats/npm.md) R-NPM-041 in two variants, each labelled with the client versions
it serves, so the user picks by the client in the job image:

| Variant | Label | ID token | Priority |
|---|---|---|---|
| Native exchange | "npm 11.5.1 and later, pnpm 11 and 12, Yarn 4" | `NPM_ID_TOKEN` with `aud: "npm:<host>"` (here `npm:trove.example.com`); the client exchanges it itself (R-NPM-022) | P1, with R-NPM-022 |
| Generic exchange | "npm 10, pnpm 10 on npm 10, Yarn 1" | `TROVE_ID_TOKEN` with `aud` set to the base URL; a pipeline step exchanges it at `POST /api/v1/auth/oidc/token` ([gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-007), and the job revokes the token as its last step | MVP |

The dialog offers both variants for every installation and never chooses between them by the base
URL's path: `server.base_url` is an origin without a path (R-OPS-005 in
[operations-and-observability.md](operations-and-observability.md)), so trove owns the host root
that npm's native exchange posts to ([formats/npm.md](formats/npm.md) Q-NPM-1). Until R-NPM-022
ships, the native variant is not shown.

Acceptance criteria (CI end-to-end, `node:24` container, real token in `TROVE_TOKEN`):

- With the project `.npmrc`, `npm view <package> version` and `npm install <package>` succeed;
  `npm publish` of a fixture to `npm-local` succeeds.
- With `.yarnrc.yml`, `yarn add <package>` (Yarn Berry) succeeds; `pnpm add <package>` succeeds with
  the `.npmrc`.
- The generic-exchange variant, pasted into a GitLab CI job on a `node:22` image whose ID token a
  CI identity mapping accepts, publishes a fixture to `npm-local` and ends with the CI token
  revoked. From P1, the native variant does the same on `node:lts`.

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
- Row actions: edit, browse, client setup, reindex (P1), invalidate cache (remote and virtual),
  delete. Delete requires typing the key, offers the `detach` choice of
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
- The format step offers only the formats whose `classes` in `GET /api/v1/formats` contain the
  class chosen in the first step. Those `classes` are generated from each format's
  `Descriptor().Classes` (R-ARCH-008, R-ARCH-010 in [architecture.md](architecture.md)); the UI
  holds no list of formats per class of its own (FMT-001). A create that names another class still
  fails on the server with 422.
- Settings forms render from the format's settings JSON Schema (R-ARCH-004, R-ARCH-008) into the
  format member of the repository document that is named after the format (`npm`, `nuget`;
  [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-004), never into a generic
  `formatSettings` member. A format adds a panel only for interactions a schema cannot express.
- NuGet local repositories: the settings view, in the wizard's review and on the edit form, shows
  the effective `nuget.deleteMode`, `unlist` (the default) or `delete`, with one sentence on what
  `dotnet nuget delete` and `nuget delete` do in this repository: unlist the version, or delete it
  for good with a tombstone ([formats/nuget.md](formats/nuget.md) R-NUGET-007). Deletes through
  the UI and the management API are hard deletes whatever the mode (R-NUGET-007).
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
- With a test build that registers a format whose `Classes` lack `Virtual`, choosing the class
  `virtual` does not offer that format, and choosing `local` does.
- The edit form of `nuget-local` without a `nuget.deleteMode` shows "unlist"; after
  `PATCH /api/v1/repositories/nuget-local` with `If-Match` and `{"nuget": {"deleteMode": "delete"}}`
  it shows "delete".

### R-UI-022 · Virtual repository member editor
Priority: **MVP** · Covers: UI-032

- Two lists: available (same format, excluding the repository itself and anything that would create
  a cycle) and selected (ordered). Buttons Add, Remove, Move up, Move down, Move to top work with
  keyboard and single pointer (WCAG 2.5.7); drag and drop is an optional extra. Both lists are
  listboxes of `@angular/aria` (R-UI-001, Q-UI-1).
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
`POST /api/v1/repositories/quick-setup` ([repositories-and-proxy.md](repositories-and-proxy.md))
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

- Disabling a user with "revoke tokens" makes that user's API calls with a token fail with 401 on
  every replica within 2 s (R-API-052), and the audit log shows the change.

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
- Layout (Q-UI-4): one page for create and edit, no step-by-step wizard. The sections run down the
  left with an in-page table of contents; the preview panel is a sticky right column while the
  content area is at least 960 CSS px wide and a collapsible region above the save bar when it is
  narrower. The explainer (R-UI-028, P1) is its own screen, reached from the editor through "Test
  this policy" (saved policies only, once the explainer ships), which opens it in a new tab with the policy's first principal and
  repository filled in, so the draft in the editor stays untouched.
- Editor sections, following the policy model of
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
- Missing group claim (R-AUTH-008 in [identity-and-authentication.md](identity-and-authentication.md)):
  while the record carries `groupClaimMissing` (`{"provider", "reason"}`), a notice next to the
  groups names the provider and the reason (`absent`, `wrong_type` or `overage`). It says that the
  last login through that provider carried no usable group claim, so the groups synchronized from
  it may be missing (the default `clear` removes them) and the identity provider's group mapping
  needs an administrator. The notice is `role="status"` text, not a dialog, and disappears after a
  login that delivers the claim.
- What the profile does not have (settles UI-053's open point on password changes): no unlock step,
  because the session already proves the login (R-AUTH-017 in
  [identity-and-authentication.md](identity-and-authentication.md)); no password section, because
  users have no password in trove (INV-7) and the break-glass password is changed by replacing its
  Kubernetes Secret (R-AUTH-014); no editable email or display name, because both come from the
  identity provider at every login (R-AUTH-016 maps the catalog's "profile updatable" option to
  this). Nobody changes a password through trove. An administrator's correction of email or
  display name through `PATCH /api/v1/users/{username}` (R-API-031) lasts until the user's next
  login. SSH keys and linked accounts are P2 on the identity page and not shown.
- Personal tokens: list with description, scope summary, created, expires and last used; tokens
  expiring within 7 days are marked. Create dialog: description (required), expiry (presets and a
  custom date, bounded by R-AUTH-040), scope (all own permissions, or restricted to repositories and
  actions; grammar of R-AUTH-024). Recent authentication applies. The result is shown once with a
  copy button and the notice that it cannot be shown again. Revoke and rotate with confirmation;
  rotate is P1 and, like creation, needs recent authentication (R-AUTH-030).
- Sessions (`/api/v1/users/me/sessions`): list with created, last seen, address, user agent and
  "this session"; end one, or end all others.

Acceptance criteria:

- A created token works in `curl -H "Authorization: Bearer …" "$TROVE_URL/api/v1/auth/whoami"`;
  reopening the profile never shows it again; revoking it makes the same call answer 401 on every
  replica within 2 s (R-API-052).
- Ending all other sessions signs out a second browser.
- The profile page contains no password field, no unlock control and no editable email or display
  name; after the identity provider changes the user's email, the next login shows the new value.
- After a login whose ID token and userinfo carry no group claim, the profile shows the notice with
  the provider and `absent`; after a later login that carries the claim, the notice is gone.

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
timeouts, client switches including Basic, the command-line login poll binding mode, SCIM state).
YAML-pinned and ceiling-bounded values show their source. Hiding unauthorized resources
(`authz.hide_existence`) is shown read-only. Save sends `PATCH` with `If-Match`; recent
authentication applies.

The status block shows the two poll-binding warnings that the answer carries (R-AUTH-034,
R-AUTH-040 in [identity-and-authentication.md](identity-and-authentication.md)) as warnings next to
the mode, each with icon and text:

- mode `off`: any poll of an approved `npm login` receives the token, wherever it comes from;
- mode not `off` while `server.trusted_proxies` is empty: behind an edge proxy every poll carries
  the proxy's address, so the binding cannot tell the user's network from anyone else's.

Acceptance criteria:

- Turning anonymous access on lets an unauthenticated `npm view` read a package that a policy grants
  to the anonymous principal.
- With the default poll binding and an empty `server.trusted_proxies`, the screen shows the
  trusted-proxy warning; with `poll_binding.mode: off`, it shows the `off` warning.

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

The screen is MVP because every endpoint it renders from is MVP (the replica registry API of
R-OPS-003, R-OPS-004, the task and job API of R-OPS-019, R-API-049, R-API-050), and it is the only
UI view of the replicas per role, of Valkey, of the backup job, of the scheduled tasks and of the
key ring. It needs a little backend work beyond those endpoints: the registry's `node` and its
nested `valkey` view (R-OPS-003, adopted by R-API-038), the per-role counts and the `backup` block
of the system information (R-OPS-004), and the backup summary of R-API-050, which are MVP because
INV-12 makes roles, drains and Valkey MVP and INV-19 the backup job (Q-UI-7).

- Cards from `GET /api/v1/system/info` and `/system/replicas`: version and revision; base URL and
  source; maintenance mode; database (server version, schema version against the supported ones);
  scheduled tasks (`/system/tasks`: next run, last outcome) with "Run now", which runs on a
  `worker` replica; recent jobs (`/system/jobs`) with cancel.
- **Replicas by role** (Q-UI-7). One group per role (`web`, `worker`; a replica with roles
  `["all"]` of the development role forms a group `all`; from P1 one group per pool of R-HA-062),
  headed with the role's `live`, `ready` and `degraded` counts from `replicas.roles` of
  `/system/info`, each listing its replicas with name, node, version, state (`starting`, `ready`,
  `unready`, `draining`, `lost`), heartbeat age and Valkey view. Each group states whether the role
  is drain-safe right now: its `ready` replicas, not `lost`, run on at least two distinct nodes, so
  draining any one node leaves a ready replica (INV-12, R-HA-059). The group shows, with icon and
  text:
  - "Fewer than two ready replicas": a node drain or the loss of one pod interrupts this role;
  - "All ready replicas on one node": a drain of that node interrupts this role (shown only when
    every replica reports a node);
  - for `worker`, "No live worker replica": jobs and scheduled tasks are queued but nothing runs
    them (R-API-015);
  - while replicas of two versions are live, an information notice naming both versions, because N
    and N-1 run side by side during every rolling upgrade (INV-3); it is not a warning.

  The screen says what it cannot see: disruption budgets, free capacity on other nodes and whether
  the remaining replicas carry the peak (the N + 1 rule of R-HA-059), because trove holds no
  Kubernetes permissions (R-HA-033).
- Readiness checks per replica, from the `checks` of `/system/replicas` (R-OPS-001, R-OPS-003; Q-OPS-2
  in [operations-and-observability.md](operations-and-observability.md)), in two kinds that the
  screen never merges:
  - **failing**: a gating check fails (for example `database`, `schema` or `invalidation`) and the
    replica is `unready`; shown as an error with the check names;
  - **degraded**: the replica is `ready` while a reported check fails. Two checks can be in this
    state; `/readyz` answers `200` and lists them under `degraded`, and
    `trove_health_check_passing{name=…}` shows `0`:
    - `objectStore`, after the first readiness unless `health.require_object_store` is set: blob
      requests on that replica fail with a retryable `503`, everything else keeps working;
    - `valkey`, whenever Valkey is unreachable from that replica ([architecture.md](architecture.md)
      R-ARCH-031, [ha-and-helm.md](ha-and-helm.md) Q-HA-13): no request fails, but remote fetches
      are collapsed per replica only, the negative cache is per replica, rate limits fall back to
      per-replica shares of the budget (R-API-016), and changes reach that replica through the
      change-log poll instead of within milliseconds.

    Each is shown as a warning that names the check and says, in the words above, what it means.

  Both kinds use icon plus text, never colour alone (R-UI-044). The page header summarises the
  service as "all ready", "degraded" or "replicas unready"; a role that is not drain-safe adds
  "not drain-safe" to the summary.
- **Valkey card**, from the `valkey` block of `/system/info` (installation facts: mode, primary
  name, configured Sentinels, TLS mode, key prefix and keyspace) and each replica's nested `valkey`
  view of `/system/replicas` (R-API-038): per replica its state with the reason in R-OPS-001's
  vocabulary, each reason translated, the circuit (an open circuit is shown as such), the primary
  address, the reachable against the configured Sentinels and the server version. The card
  summarises with icon and text:
  - "Available on every replica", or "Degraded on k of n replicas" with the reasons, where k is
    `valkey.degradedReplicas` and n is `replicas.live`;
  - "Replicas disagree about the primary" when replicas whose `valkey.state` is `available` report
    different `valkey.primary` addresses for longer than `valkey.sentinel.refresh_interval` plus
    one heartbeat: a replica bound to a demoted primary, the case R-ARCH-031's reconciliation
    bounds;
  - "Sentinels unreachable" when a replica's `valkey.sentinelsReachable` is below a majority of its
    `valkey.sentinelsConfigured`, the condition on which `TroveValkeySentinelsDegraded` fires
    ([ha-and-helm.md](ha-and-helm.md) R-HA-007): no failover can happen while it lasts, so the next
    primary failure would leave trove degraded until someone acts. The `valkey` check stays `pass`
    meanwhile, because the primary still answers (R-HA-056 step 6);
  - the time of the last primary change (the newest `valkey.lastPrimaryChangeAt` of the
    replicas);
  - the server version, or each version with its replicas when the replicas report different ones,
    as an information notice.
- **Backups card**, from `GET /api/v1/system/backups/summary` (R-API-050): the last success and its
  age, an "Overdue" mark under the rule of R-API-050 (1.5 × the expected interval), "No backup
  yet", the last failure when it is newer than the last success, the active run with its phase and
  progress, and a link to the backups screen (R-UI-048).
- Jobs (`/admin/system/jobs`): the list of `GET /api/v1/system/jobs` with filters for `kind` and
  `state`. The state filter offers every job state of R-ARCH-016: `queued`, `running`,
  `succeeded`, `failed`, `cancelling`, `cancelled`. A cancelled job shows `cancelling` until it
  stops at its next checkpoint (R-OPS-019), and "Cancel" is offered only for `queued` and
  `running` jobs.
- A "Stored-secret keys" card from `GET /api/v1/system/encryption` (R-API-049): the active key ID
  and the key IDs of each replica, sealed values per key ID, the canary state labelled with the
  replica that reported it, and whether the rings are compatible (icon plus text). A key ID that is no longer active but still seals values is marked
  "re-encryption pending" with "Re-encrypt now" for administrators (the task trigger of
  R-API-049), disabled with its reason while the rings are incompatible. The card names the next
  rotation step of R-HA-027 that the state allows, for example "No values left under 2026-04: its
  key file can be removed."
- Automatic refresh every 15 seconds with a visible pause control (WCAG 2.2.2).

Acceptance criteria:

- Stopping one of two replicas changes its status on the screen within 40 seconds without a reload.
- After a rotation to a new key, the card shows the old key ID as "re-encryption pending"; after
  "Re-encrypt now" finishes, it shows zero values under the old key ID and the removal hint.
- Blocking object storage after both replicas became ready shows both as degraded with
  `objectStore` within 40 seconds, and neither as unready (default `health.require_object_store`).
- Cancelling a running `gc-full` job lists it under the state filter `cancelling` until it stops,
  then under `cancelled`.
- With two `web` and two `worker` replicas, stopping Valkey shows every replica as degraded with
  `valkey` and the explanation within 40 seconds, the header reads "degraded", no replica is shown
  unready, and the Valkey card reads "Degraded on 4 of 4 replicas".
- `SENTINEL FAILOVER trove` updates the last primary change on the Valkey card within 40 seconds,
  and every replica reports the new primary.
- Scaling `worker` to 0 shows "No live worker replica" within `system.heartbeat_stale_after` plus
  15 seconds. A test that pins both `web` replicas to one node shows "All ready replicas on one
  node" and "not drain-safe" in the header.
- With `backup.expected_interval: 1h` and the last successful backup two hours old (test clock),
  the Backups card shows "Overdue"; at 80 minutes it does not.
- With two of three Sentinels stopped, the Valkey card shows "Sentinels unreachable" within
  40 seconds, while every replica's `valkey` check stays `pass` and the header does not read
  "degraded".

### R-UI-039 · Webhooks
Priority: **P1** · Covers: UI-046, UI-047, EVT-034

List; editor (key, URL, handler kind standard or templated, for templated handlers the method,
the body template and header templates of R-OPS-041 and named write-only secrets, otherwise the
signing secret with rotation; event types from `/api/v1/webhooks/event-types`, repository
selectors, outbound proxy, custom headers, enabled); "Test" with the receiver's status, latency
and body excerpt; status (`/{key}/status`); deliveries with request and response details and
"Redeliver". Project-scoped subscriptions and a project column come with EVT-022 (P2). Semantics:
[operations-and-observability.md](operations-and-observability.md).

Template validation (R-OPS-041, Q-OPS-11 there): a template is checked when the subscription is
saved or tested unsaved, and a refusal answers `422`. The editor shows each error at the template
field it belongs to (`errors[].pointer`, R-API-009) and in the error summary, with the server's
text, which names the rejected construct: `define`, `template` or `block`; a `range` inside
another `range`; a `range` over a literal, a function result or a variable; more than 16 `range`
actions in one template, header templates included; the `call` function; a reference to an
unknown secret; a template that fails to render against the sample event of a subscribed type.
The editor does not check templates itself, so its rules never drift from the server's.

Acceptance criteria:

- "Test" against a receiver that answers 500 shows the status and body excerpt.
- Saving a template with 17 `range` actions keeps the editor open and shows, at the template
  field, the error naming the 16-range cap; a template with `{{ define "x" }}` shows an error
  naming `define`.

### R-UI-040 · Builds
Priority: **P1** · Covers: UI-028, UI-029

Builds list, runs list, run details (number, start, duration, CI link, status; tabs modules,
artifacts, dependencies, version control, environment with values masked by the server, raw JSON),
and promotion with dry run (R-API-042).

Acceptance criteria: defined on `builds.md` (Q-API-5), which is written before P1 build work
starts.

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
- 412 `precondition_failed`: a conflict dialog "Changed by someone else since you opened it",
  showing their version against yours field by field, with "Reload (discard mine)", "Overwrite"
  (re-sends with the new ETag after showing the diff) and "Copy my changes".
- **Own permission changes** (INV-17, R-API-052, [authorization.md](authorization.md)
  R-AUTHZ-032). After a write that answers `Trove-Authz-Epoch` (a policy, a role binding, from P1
  a project), the session store (R-UI-046) keeps the highest such epoch of the tab, and the
  interceptor sends it as `Trove-Authz-Min-Epoch` on every request that follows, so the next
  screen, the `authz/me` and `authz/check` refresh included, shows the change on whichever replica
  answers. The epoch lives in memory only, never in browser storage, and is cleared on 401 and on
  sign-out.
  - 412 `authz_epoch_unknown`: the UI drops that epoch at once (otherwise every later request
    would fail the same way), re-reads the changed object by its `ETag` (R-API-012), and tells the
    user that the change may not have been saved, showing the object as now stored; when the
    stored object matches the user's draft, as for a retried write below, the message says that
    the change is stored. A GET that met the `412` is sent again without the header; any other
    request is offered again with "Retry".
  - 503 `authz_stale`: a GET is retried once after `Retry-After`; any other request shows the 503
    notice below with "Retry".
- 401 `reauthentication_required`: a dialog explains why, keeps the draft of non-secret forms in
  session storage keyed by route, and starts the re-authentication flow of R-AUTH-011; after return
  the draft is restored and the action can be repeated. Forms holding secrets are not kept.
- 429: idempotent GETs retry once after `Retry-After`; otherwise a message with the wait time.
- The UI sends an `Idempotency-Key` on every request that accepts one (R-API-014): creates, job
  starts and job-starting deletes. The key is generated when the form or dialog opens and kept
  until it closes.
- A network error without an answer (for example a request cut at the end of a replica's drain,
  R-API-051) is retried once through the Service, which picks another replica: a GET as it is; a
  request that carries an `Idempotency-Key` with the same key (R-API-014), so it cannot create or
  start twice; a PUT or PATCH with the same `If-Match`. A retried write that answers 412 because
  its first attempt did commit is recognised by comparing the current resource with the user's
  draft: when they match, the UI reports success instead of opening the conflict dialog. A retried
  DELETE that answers 404 counts as done. Any other unsafe request (a POST action without an
  idempotency key, such as a revocation or a rotation) is not retried automatically: the UI says
  that the action may or may not have run, reloads the affected resource, and offers "Retry".
- 409 `idempotency_in_progress`: the UI waits `Retry-After` and sends the same request with the same
  key once more, which replays the first request's outcome.
- 409 `token_already_issued` on a retried token creation: the first attempt created a token whose
  secret was lost with its answer. The UI revokes it (`DELETE /api/v1/tokens/{tokenId}`), creates
  the token again with a new key, and shows only the new one, so no unseen token stays valid.
- 503: a page-level notice with "Retry"; in maintenance mode the notice shows the operator's
  message.
- Confirmations use a polite live region (`role="status"`), errors `role="alert"`; notifications
  with actions stay until dismissed.
- Jobs show a progress component that polls the job (R-API-015) and announces progress at most every
  10 seconds.

Acceptance criteria:

- A forced 412 `precondition_failed` in a Playwright test opens the conflict dialog, and
  "Overwrite" stores the user's version.
- After saving a policy in the e2e profile `scatter`, the network log shows
  `Trove-Authz-Min-Epoch` with the epoch of the `PUT` on the requests that follow it. A test proxy
  that answers one of them `412` `authz_epoch_unknown` makes the UI show "may not have been saved",
  re-read the policy, and send no `Trove-Authz-Min-Epoch` on later requests until its next
  permission change; the conflict dialog does not open.
- An unknown `code` shows the request ID.
- A test proxy resets the connection of the wizard's `POST /api/v1/repositories` after the server
  has committed it. The UI retries with the same `Idempotency-Key`, reports success, and exactly one
  repository exists. The same with a `PATCH` of the edit form reports success without the conflict
  dialog.
- The same proxy resets the connection of a token creation after the commit. The UI ends with one
  displayed token; `GET /api/v1/tokens` lists it and the first token as revoked.
- The same proxy resets a `POST /api/v1/users/{username}/disable`. The UI sends it once only and
  shows the user's state as reloaded afterwards.

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
- Mechanism (Q-UI-5): Angular's compile-time i18n (`@angular/localize`,
  [Angular i18n](https://angular.dev/guide/i18n)) with one build per locale, served at `/ui/en/` and
  `/ui/de/` (R-UI-001). No runtime translation library is used; switching the language reloads the
  route under the other locale.
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
- The clients use `HttpClient` with the types openapi-typescript generates (R-API-002, Q-API-1),
  so the interceptors (CSRF,
  problem mapping, request ID) apply to every call, and no component calls `/api/v1` directly.
- URL state (filters, sort, tab, version) lives in query parameters and reaches components as signal
  inputs through the router's component input binding; changing a filter navigates with
  `replaceUrl`.
- Shared state of a feature lives in an injectable store service exposing read-only signals and
  methods; no third-party state library in the MVP. A session store holds `whoami`, `authz/me`, the
  check cache and the authorization epoch of the tab's last own permission change (R-UI-042), and
  is reset on 401, on 403 for a cached decision, and on sign-out; a reset on 403 keeps the epoch,
  so the reload it causes still carries it. A permission change made in the tab also reloads
  `authz/me` and clears the check cache, with that epoch sent.
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
| End-to-end | Playwright against the chart-installed build in the local profile of [ha-and-helm.md](ha-and-helm.md) R-HA-051: two `web` and two `worker` pods, PostgreSQL with a standby, Valkey with a replica and three Sentinels, an S3-compatible store (SeaweedFS), the backup CronJob, plus a test OIDC provider and a test CI token issuer | Journeys: sign-in, client setup followed by real `npm` and `dotnet` commands, repository wizard, policy edit, claim tester, token create and revoke, audit entry, system status and backups screens | Smoke on every pull request; full nightly, once in the `scatter` profile (three `web` replicas, round robin per request, R-ARCH-032), once with Valkey stopped, and once while the node drain of R-HA-059 variant A runs, where no journey may fail |
| Accessibility | axe-core in Playwright, plus the manual pass of R-UI-044 | Every route × theme × locale | Every pull request; manual per release |
| Internationalization | Build with missing-translation error | Both locales | Every pull request |
| Security | README XSS corpus; any `securitypolicyviolation` event fails an end-to-end test | Renderer, CSP | Every pull request |

- End-to-end tests prepare data through the API, not through the UI (INV-17), and run in Chromium on
  pull requests and, in the full run of Test and Release on the commit that closes the phase, in Chromium, Firefox and WebKit.
- Line coverage of services and pure logic at least 80 % (# default (proposed)).
- Test tool versions are pinned once in the lockfile and used by local runs and CI alike.

Acceptance criteria:

- The CI workflow runs every tier at the stated cadence, and a deliberately broken snippet template
  fails the end-to-end client-setup test.

### R-UI-048 · Backups
Priority: **MVP** · Covers: OPS-032

`/admin/system/backups`, for callers with `system.read`, renders R-API-050. It shows the backup job
of INV-19 and changes nothing: backups are started by the CronJob or by hand with `kubectl`,
configured through the chart and the configuration file, and restored by a Helm hook Job
([backup-and-restore.md](backup-and-restore.md) R-BAK-011, R-BAK-012; Q-API-10).

- **Status.** Last success with its age, the expected interval, "Overdue" (R-API-050's rule) or
  "No backup yet", and
  the last failure with its translated `code` when it is newer than the last success. A run in
  progress shows its phase (`starting`, `snapshot`, `copying`, `finalizing`, `pruning`), tables
  exported, blobs and bytes copied against the totals, its pod and its start time, refreshed with
  the status screen's interval and pause control (R-UI-038). A run whose lease has expired is
  shown as "stopped without finishing; the next run marks it abandoned".
- **Runs.** The list of `GET /api/v1/system/backups` with filters for operation and state and
  "Load more": start, operation, pod, state, backup ID, snapshot time, duration, database bytes,
  blobs copied and warnings. State labels carry icon and text. `interrupted` reads "interrupted by
  a drain or eviction; the Job retries it" and is not shown as a failure (R-BAK-012). A detail panel
  shows the result and the error.
- **Stored backups.** The complete backups that the newest listing run found in the target, with
  backup ID, snapshot time, trove version, schema step, database and blob sizes and a "damaged"
  mark, headed "as recorded by run <id> at <time>" and the note that `trove backup list` reads the
  target itself (Q-API-11). Next to it, read-only: the target, `keep_last` and `keep_within` as
  that run applied them, and the expected interval.
- **Start a backup now.** No button starts one. A panel shows the command with a copy button,
  filled from `manualRun`:
  `kubectl -n trove create job --from=cronjob/trove-backup trove-backup-manual-<timestamp>`
  (# example), with `<timestamp>` (Unix seconds) filled in by the browser when the panel opens.
  A Job name may have at most 63 characters
  ([Job](https://kubernetes.io/docs/concepts/workloads/controllers/job/)), so the browser shortens
  the CronJob part when a long release name would exceed it. Without `manualRun`, the namespace
  and CronJob name appear as placeholders. The panel says that the run appears in the list once its
  pod has started, that it records `trigger: schedule` and is told apart by its pod name
  (R-BAK-012), and that one backup runs at a time (R-BAK-010).
- **Restore.** When `restoredFrom` is set, a notice names the backup ID and snapshot time this
  installation was restored from. Restoring is a Helm operation on an empty installation
  (R-BAK-020, R-BAK-021); the screen links to the operator documentation and offers no restore
  action.

Acceptance criteria:

- During a run, the screen shows phase `copying` and a growing count of copied blobs without a
  reload.
- A run ended `interrupted` by a drain, then completed by the Job's next pod, shows the first as
  interrupted, not failed, and the status shows no failure.
- After two runs with `keep_last: 1`, "Stored backups" lists one backup, recorded by the second
  run.
- The network log of a visit to the screen, including a click on the copy button, contains no
  request with a method other than GET or HEAD.
- With `manualRun.cronJob` set to a 52-character name, the copied command, run with `kubectl`,
  creates the Job, and its name has at most 63 characters.

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
- ADMAPI-083 · System and repository import and export — P2 as API operations. The whole-installation
  backup and restore are MVP, but as `trove backup create` and `trove backup restore` in the
  `backup` role (INV-19, [backup-and-restore.md](backup-and-restore.md)); the API only reports
  their runs (R-API-050, Q-API-10)
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
  sanitizer, and a CSP with Trusted Types and no inline scripts (R-UI-008, R-UI-001). External
  images are blocked unless an operator lists their exact origin, which then also appears in the
  CSP (Q-UI-6); this keeps viewer addresses and internal package names away from hosts nobody
  chose.
- **Package content in the browser.** The UI builds no content URL from strings and never reads
  package content with `fetch` or XHR (R-UI-001); content reaches the browser only by navigation or
  `img`, under the content headers that the core sets (R-ARCH-007). From P1, an optional content
  origin (R-ARCH-029) keeps the session cookie away from that content, and the UI's CSP names that
  origin in `img-src` only, never in `connect-src`.
- **Lost updates of grants.** PUT and PATCH of revisioned resources require `If-Match`, with
  `If-Match: *` as the explicit blind write (R-API-012), so a stale editor or script cannot
  silently revert a permission policy, a CI identity mapping or the member order of a virtual
  repository. A restore lifts every revision column (R-API-012), so an `If-Match` taken before it
  cannot match a record its sender never saw.
- **Key-ring status without pod access.** Rotation of the stored-secret key ring can be checked and
  finished through the API (R-API-049), which reports key IDs and counts only; operators need no
  shell in a pod, where the key files are readable.
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
- **Authorization epoch header.** `Trove-Authz-Min-Epoch` is read only after authentication
  succeeded, and ignored without a credential, so an anonymous caller can neither probe the epoch
  nor make a replica wait (R-API-052). A forged high value costs one coalesced epoch read and
  answers `412` at once ([authorization.md](authorization.md) R-AUTHZ-032). Neither a `412` nor a
  header that passes proves whether a change is stored; the object's `ETag` does.
- **Rate limits across replicas.** The general limits count per caller across all replicas in
  Valkey (R-API-016), so adding replicas does not multiply a caller's budget. Valkey's content can
  raise or lower a budget but never grants access; a Valkey outage falls back to per-replica shares
  (H-43, H-49 in [architecture.md](architecture.md)). The break-glass lockout stays in PostgreSQL.
- **Backups through the API.** The API and UI only read backup runs from PostgreSQL. `web` replicas
  never hold the backup target's credentials, and no endpoint starts, deletes or restores a backup
  (R-API-050, Q-API-10), so neither a hijacked session nor a compromised `web` replica can read or
  destroy backups through trove, and the chart grants trove no Kubernetes permission that could
  start a pod with the backup role's identity.
- **Status payloads.** The system status answers show internal facts (node names, the Valkey
  primary's address, the backup target's bucket and prefix) to `system.read` holders only, and
  never credentials, ACL user names or key material (R-API-038, R-API-050).

## Decisions

Each question below was decided by the development agent from the listed options; the owner reviews the choices at the walkthrough.

### Q-API-1 · OpenAPI generation approach

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Spec-first; generated Go server interface and TypeScript types | `api/openapi.yaml` is written by hand. [oapi-codegen](https://github.com/oapi-codegen/oapi-codegen) generates Go models and the `net/http` server interface; [openapi-typescript](https://openapi-ts.dev/) generates TypeScript types; thin hand-written Angular clients use them | The contract can be reviewed before code exists; drift between document and server, and between document and UI, is a compile error; a missing handler does not compile; routing stays on `ServeMux` (R-ARCH-005) and the UI on `httpResource()` (R-UI-046) | oapi-codegen calls its 3.1 support "initial" (v2.8.0, [release notes](https://github.com/oapi-codegen/oapi-codegen/releases)), so a construct may need an equivalent spelling |
| (b) Spec-first; generated Angular client | As (a) on the Go side; the Angular side is a generated `HttpClient` service layer (for example [ng-openapi-gen](https://github.com/cyclosproject/ng-openapi-gen)) | Less hand-written client code | The generator's output decides how every service looks and is built around observables, while R-UI-046 reads server state through `httpResource()` and signals; one more generator to move in step with every Angular major |
| (c) Code-first | Go types and handler registrations produce the document (for example [Huma](https://huma.rocks/)) | No hand-written YAML; the document cannot lag behind the code | Inverts INV-6's "source of truth": the contract changes as a side effect of code changes; handlers depend on a framework instead of `net/http` |
| (d) Spec-first; types only | Generated Go and TypeScript types; routes and parameter binding written by hand, guarded by the route-parity check of R-API-002 | No dependence on a generator's server code | Parameter parsing and route registration by hand for every operation of Part B; a forgotten handler shows up in CI, not at compile time |

**Choice:** (a) — the hand-written OpenAPI 3.1 document generates Go models and the `net/http` server interface through oapi-codegen and TypeScript types through openapi-typescript, and thin hand-written Angular clients use them.

**Rationale:** For one developer building the whole surface of Part B, compile errors are the cheapest guard against drift, and (a) produces them on both sides while keeping two choices already made elsewhere: `net/http.ServeMux` on the server and signals with `httpResource()` in the UI. (b) would put a generated, observable-based service layer between the UI and those primitives; (c) gives up the reviewable contract INV-6 asks for; (d) costs hand-written parsing for every operation and gains nothing over (a) as long as the generator handles the constructs. The young 3.1 support is contained without hand-written types: a fixture document with every construct the contract uses is generated in CI, and a construct the generator mishandles is written in an equivalent form (`enum: [x]` for `const: x`). Permission enforcement does not depend on the generator at all: the authorization step reads the declarations from the embedded document by matched route pattern, and a route without one stops start-up. The strict-server variant is left out because handlers set `ETag`, `Location`, `Link` and `Retry-After` themselves and stream answers. The development agent revisits this if the fixture shows a construct with no equivalent form, or if the generator's 3.1 line stops moving; the fallback is (d), with the document and the TypeScript side unchanged.

**Applied in the spec:** R-API-002 now specifies the generation pipeline (oapi-codegen `models` and `std-http-server` into `internal/api/v1/gen/`, `/files` operations excluded; openapi-typescript into `web/src/app/api/generated/`), the start-up table of authorization declarations keyed by `http.Request.Pattern` that fails closed, the 3.1 construct fixture, and two new acceptance criteria. R-UI-046 names openapi-typescript. The layout changes (`internal/api/v1/gen/`, `web/src/app/api/generated/`, the generator pins) are applied in [architecture.md](architecture.md) R-ARCH-003 and R-ARCH-004 (Q-ARCH-1 there).

### Q-API-2 · Is If-Match mandatory on writes?

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Optional unless the owning page requires it | Optional by default; an owning page may require it (permission policies do); the switch `api.require_if_match` makes it mandatory everywhere | Scripts can overwrite without thinking about it | Each owning page has to recognize which of its fields are dangerous to lose; two behaviors to test |
| (b) Required on PUT and PATCH of revisioned resources; `If-Match: *` as the deliberate blind write | One rule for every resource with a revision ETag; 428 without the header; DELETE and delta-style edits exempt | No lost update wherever a full document or a whole array is written; a blind write stays possible but is explicit; the rule is derived from the document, with no switch | Every script that writes configuration sends one more header; curl examples get longer |
| (c) Required only on grant-defining resources | Mandatory for permission policies, CI issuers and identity mappings, and authentication settings; optional elsewhere | Friction only where a reverted change re-opens access | The list is a judgement call that misses cases: a stale write of a virtual repository's `members` (arrays are replaced whole, R-API-013) can put a remote member ahead of the local one again, a dependency-confusion path |

**Choice:** (b) — `If-Match` is required on every PUT and PATCH of a resource that carries a revision ETag, `If-Match: *` is the explicit blind write, and DELETE and delta-style edits are exempt.

**Rationale:** In trove a lost update is a security problem more often than it looks: besides permission policies, the member order of a virtual repository and the conditions of a CI identity mapping decide which package a client receives and who may publish. (a) and (c) depend on someone recognizing each such field; (b) makes the safe behavior the default for all of them, and the UI already sends the header everywhere. The argument against a mandatory header, scripts that overwrite on purpose, is answered by `If-Match: *` ([RFC 9110 §13.1.1](https://www.rfc-editor.org/rfc/rfc9110#section-13.1.1)), which keeps blind writes possible and makes them visible in the request. DELETE is exempt because a deletion means to remove whatever version exists; delta edits (`add`/`remove` lists, role bindings, assignments) are exempt because they cannot overwrite another writer's change. A script over the document checks the rule, and dropping the switch leaves one behavior to test. The development agent revisits this if a client tool that matters to the owner cannot send request headers.

**Applied in the spec:** R-API-012 states the rule, the exempt list and the 428 answer ([RFC 6585](https://www.rfc-editor.org/rfc/rfc6585#section-3)), and gains two acceptance criteria; the configuration key `api.require_if_match` is removed. R-API-002 adds the operation marker `x-trove-if-match`. R-API-013's acceptance criterion and R-API-023's `batch-update` send the precondition. The security notes gain "Lost updates of grants". Pages that own revisioned resources get the 428 answer as a cross-page edit.

### Q-API-3 · Cluster-wide rate limiting

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Revisited on 2026-10-28 after the owner added the Valkey, horizontal-scale, backup and storage-class constraints: the choice moves from (a), per-replica buckets by design, to the new option (e), cluster-wide counters in Valkey with a per-replica fallback, the limiter of [architecture.md](architecture.md) R-ARCH-034 (Q-ARCH-17 there); H-43 now covers only the fallback and lost counters.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Per replica, in memory, for every class | Token buckets in each replica's memory, as R-API-016 and the identity and CI pages specified until 2026-10-28 | No shared state and no round trip per request | The cluster-wide budget is the configured value times a replica count that grows with the load and changes with autoscaling; the Service spreads one client's requests over every replica, so the client gets that multiple; the answer depends on the replica a request reaches (INV-18) |
| (b) Shared PostgreSQL counters for unauthenticated classes, per replica for the rest | Login starts, revoke-by-value, the CI exchange and anonymous requests counted in PostgreSQL | A cluster-wide ceiling for callers without credentials | A database write on every unauthenticated request, so a flood lands on the system of record |
| (c) Shared PostgreSQL counters for every class | Every API request updates a counter row | One exact cluster-wide limit | A write per request on the hottest path and row contention on the counter of a busy token; the limiter becomes a denial-of-service lever against PostgreSQL |
| (d) Per replica, sized as cluster limit ÷ live replica count | Each replica divides the configured budget by the replica count of the registry | Approximates a cluster-wide limit without shared state | kube-proxy balances connections, not requests: a client whose keep-alive connection stays on one replica gets 1/n of its budget; small bursts round down to nothing; the limit moves with every scale event |
| (e) Cluster scope in Valkey, per-replica fallback | A sliding-window counter per bucket in Valkey (R-ARCH-034); while Valkey is unavailable, (d) as the fallback | One budget at any replica count and the same answer on every replica; no write on PostgreSQL; Valkey is part of every production topology (INV-12) | One pipelined Valkey round trip per limited request; the sliding window approximates; during a Valkey outage the fallback has (d)'s weaknesses |

**Choice:** (e) — the classes `read`, `write` and `anonymous` count cluster-wide in Valkey through the limiter of R-ARCH-034 and fall back to per-replica shares of the budget while Valkey is unavailable.

**Rationale:**
- The earlier choice rested on two premises that no longer hold. The classes were to protect the
  resources of one replica, and shared counters would have written to PostgreSQL. Under the owner's
  constraints the load exceeds one replica, the replica count follows the load, and the Service
  spreads one client's requests over all replicas. Under (a), a budget of 50 reads per second would
  mean 150 at three replicas and 1,500 at thirty: not a value an operator can set. Valkey is now
  part of every production topology and takes the counter writes off PostgreSQL, which removes the
  reason (b) and (c) were rejected for.
- Replica protection does not need these classes: `server.max_in_flight` stays per replica
  (R-ARCH-005) and is the right unit for CPU and memory.
- The fallback divides by the live `web` replicas, so a Valkey outage keeps the budget near its
  configured value instead of multiplying it, and the identity and CI pages use the same limiter,
  so one rule covers every class.
- Credential guessing is still no argument either way: tokens are 256-bit random values, and the
  break-glass lockout counts in PostgreSQL (R-AUTH-038) because it must survive a loss of Valkey's
  data.
- The development agent revisits this if the limiter's round trip shows up in P-2
  ([architecture.md](architecture.md) R-ARCH-027): read classes would then check a local bucket
  first and ask Valkey only near the limit. A per-tenant quota (BEYOND-030, P2) that needs exact
  counts would get a limiter of its own.

**Applied in the spec:** R-API-016 is rewritten: purpose (fair share, not replica protection), the limiter names, the sliding window and its fallback at the budget ÷ live `web` replicas, refused requests counting against the window as R-ARCH-034 specifies, `Retry-After` and the `RateLimit` fields with sliding-window values, and acceptance criteria across three replicas with and without Valkey and for a client that ignores `Retry-After`. The earlier statement that the buckets are kept in memory per replica by design is removed. H-43 is narrowed to Valkey outages and to counters lost in a failover; tampering with the counters is H-49 in [architecture.md](architecture.md). The security notes gain "Rate limits across replicas". No configuration key changes; the values are now cluster-wide budgets. Cross-page: none beyond those of Q-ARCH-17 in [architecture.md](architecture.md), which already names R-API-016, this question and H-43.

### Q-API-4 · INV-17's wording and byte transfers on `/files`

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Amend INV-17's wording | INV-17 says: the UI uses only public, documented endpoints (the operations of the OpenAPI document, `/api/v1/…` and `/files/…`, plus the browser sign-in flow under `/auth/…`) and no private UI endpoint | States the purpose, every UI action is scriptable, instead of a path prefix; matches what R-API-002 documents | Touches an invariant |
| (b) Mirror byte transfer under `/api/v1/` | `…/items/content?path=` duplicates `/files` | INV-17's text stays | A second code path for the largest transfers (ranges, digests, streaming) with nothing gained; two URLs for the same bytes |
| (c) Keep INV-17's text and define its reading on this page | R-API-002 says what "public `/api/v1` API" means | No invariant edit | Someone who reads INV-17 alone concludes that the UI's use of `/files` and `/auth` violates it; invariant and page disagree in their words |

**Choice:** (a) — INV-17 is amended to "public, documented endpoints: the operations of the OpenAPI document (`/api/v1/…`, `/files/…`) and the browser sign-in flow under `/auth/…`; no private UI endpoints".

**Rationale:** INV-17 exists so that everything the UI does can be scripted; `/files` is described in the same OpenAPI document and is as scriptable as `/api/v1`, and the UI cannot sign in without the browser flow under `/auth/`, which R-AUTH-005 and R-AUTH-013 own. (b) builds a second byte path for the heaviest traffic only to satisfy a wording; (c) leaves an invariant that its own pages contradict, which is how invariants stop being read. The development agent revisits this only if the UI ever needs an endpoint the document does not describe, which this wording forbids.

**Applied in the spec:** R-API-002 refers to the amended INV-17. The invariant text itself is a cross-page edit to [README.md](README.md) (INV-17), with the matching sentence in ADR-028.

### Q-API-5 · Owning page for build information

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A new page `builds.md` with prefix `R-BLD` | Owns the record schema, the mapping of CI job names (which contain `/`) to `{buildName}`, promotion and run retention; uses the build permissions of R-AUTHZ-027 | One owner for one model; [authorization.md](authorization.md) (Q-AUTHZ-8) and [storage.md](storage.md) (Q-STOR-3) reach the same answer | One more page to write before P1 build work |
| (b) Fold it into [gitlab-ci-oidc.md](gitlab-ci-oidc.md) | GitLab CI is the main producer of build records | Close to the producer | Mixes CI authentication with a resource model that changes for other reasons; other CI systems upload builds too |
| (c) Fold it into [operations-and-observability.md](operations-and-observability.md) | Treat build records as operational data | No new page | Build records have permissions, promotion and retention of their own; operations would own an unrelated model |
| (d) Fold it into [storage.md](storage.md) | Storage already specifies build retention (RET-023, RET-037) | Retention mechanics sit next to the records | Storage would own two models; promotion and permissions are not storage concerns |

**Choice:** (a) — build information gets its own page `builds.md` (`R-BLD`), written before P1 build work starts; until then R-API-042 reserves the paths.

**Rationale:** Build records are a model with their own lifecycle: upload, promotion with copy or move, permissions and retention. Each of (b) to (d) would make a page the owner of two models that change for different reasons, and the three pages that raised the gap all concluded the same. Build information is P1, so the MVP prototype needs none of it; reserving the paths now keeps them from being taken by something else. The development agent revisits this if build information moves to P2, in which case the page waits with it.

**Applied in the spec:** R-API-020 names `builds.md` as owner; R-API-042 states the page's scope, that the MVP implements none of it, and that the paths are reserved; R-UI-040 points its acceptance criteria to that page. Adding the page to the page table is a cross-page edit to [README.md](README.md).

### Q-API-6 · Pagination parameter names

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `limit` and `cursor` everywhere | Every list endpoint uses R-API-010's names and its envelope (`items`, `nextCursor`) | Already used by every page except [repositories-and-proxy.md](repositories-and-proxy.md) and by the job API; one page changes | None beyond that one page's edit |
| (b) `pageSize` and `pageToken` everywhere | Every list endpoint follows the repository list | Familiar from some public API style guides | Every other page, R-API-010, the job API and the UI's list handling change |

**Choice:** (a) — `limit` and `cursor`, with the `items` and `nextCursor` envelope, on every list endpoint.

**Rationale:** Uniform names let the UI and generated clients page through every list with one helper; (a) gets there by changing one page, (b) by changing all others. The development agent would revisit this only if a client ecosystem trove must serve required other names, and none does: package-manager protocols keep their own paging outside `/api/v1/`.

**Applied in the spec:** R-API-010 states that the names and the envelope are the same on every list endpoint, with no per-page variants. The repository list's `pageSize`, `pageToken` and `nextPageToken` are a cross-page edit to [repositories-and-proxy.md](repositories-and-proxy.md).

### Q-API-7 · Syntax of custom methods

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Sub-path verbs everywhere, with reserved names | `POST /api/v1/repositories/batch-create`, `/quick-setup`; any literal segment in a key position is a reserved name of that collection | One form for every action on every page; plain `ServeMux` patterns; the reserved-name rule also fixes literals that already shadow keys elsewhere | Five repository keys become reserved |
| (b) Colon methods for collection actions | `POST /api/v1/repositories:batchCreate`; sub-paths for item actions | Collection actions never share a segment with keys | Two syntaxes for one concept; the shadowing on other collections remains; legal in a path segment ([RFC 3986](https://www.rfc-editor.org/rfc/rfc3986#section-3.3)) but unusual in route tables and curl commands |
| (c) A batch resource | `POST /api/v1/repository-batches` with an `operation` field | No verbs in paths | Operations with different semantics (all-or-nothing 200 for create and update, best-effort 202 for delete) behind one operation and one discriminated `oneOf` body; quick setup does not fit |
| (d) Collection actions behind a segment the key grammar excludes | `POST /api/v1/repositories/-/batch-create`; repository keys start with `[a-z0-9]` (INV-5), so `-` can never be a key | No repository key is reserved | An unusual URL shape found nowhere else in `/api/v1/`; it does not help the literals that already sit in key positions (`/users/me`, `/webhooks/deliveries`), so the reserved-name rule is needed anyway |

**Choice:** (a) — every action, on an item or on a collection, is a kebab-case sub-path verb, and literal segments in a key position are reserved names.

**Rationale:** The decisive point is that the collision (b) and (d) avoid for repositories exists anyway on other collections: `GET /api/v1/webhooks/deliveries` makes a webhook keyed `deliveries` unreachable, and `/api/v1/users/me` a user named `me`, because the router prefers the literal. A reserved-name rule is therefore needed whatever this question decides; once it exists, the only advantage of (b) and (d) is that five repository names stay free, and the price is a second URL shape. (c) merges operations whose answers differ in status and atomicity. Declaring the reserved names in the OpenAPI document and deriving the literal siblings in CI turns a silent shadowing bug into a failing check. The development agent revisits this if an import from another system must keep a repository key that collides with a reserved verb; (d) is then the fallback for repositories alone.

**Applied in the spec:** R-API-004 now uses the sub-path form for collection actions and adds the reserved-name rule (422 `reserved_name`, `x-trove-reserved-names`, a CI check, an acceptance criterion); R-API-009 lists `reserved_name`; R-API-023 and R-UI-023 use `/batch-create`, `/batch-update`, `/batch-get`, `/batch-delete` and `/quick-setup`. The cross-page edits are the repository paths and reserved keys in [repositories-and-proxy.md](repositories-and-proxy.md), and the reserved names `me` for users, `test`, `deliveries` and `event-types` for webhooks, and `disable-all` for retention policies on their owning pages.

### Q-API-8 · Stored-secret key-ring status through the API

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

An earlier revision of this page listed `GET /api/v1/system/encryption`; it was dropped because R-ARCH-021 offers key-ring status only through `trove admin secrets status`. This question decides whether the UI and monitoring get that status through the API.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Command line only | `trove admin secrets status`, as R-ARCH-021 specifies | Nothing new to build | Checking a rotation needs a shell in a pod, where the key files themselves are readable; no UI view; nothing alerts when two replicas' rings disagree |
| (b) Read-only status endpoint, metrics and a UI card; re-encryption through the task trigger | `GET /api/v1/system/encryption` with key IDs, counts, canary state and every replica's key IDs; `trove_secrets_*` metrics; the `secrets-reencrypt` task started through `POST /api/v1/system/tasks/{name}/run` and refused while the rings are incompatible | A rotation can be checked and finished without pod access; monitoring can alert on incompatible rings and on values left under a retired key; the API refuses the one step that would break decryption | One endpoint, two metrics and one card to build; replicas publish their key IDs |
| (c) Metrics only | Gauges per key ID on the operations listener | Monitoring without a new API | No UI view; no guarded way to start re-encryption; operators still need a pod shell to finish a rotation |
| (d) Key management through the API | Upload a new key and set the active key through the API | Rotation without touching Kubernetes | Key material travels through the API and must rest somewhere: in PostgreSQL it sits next to the values it protects, in a Secret written by trove it needs write access to Secrets; both break the separation ADR-032 relies on |
| (e) Endpoint and guarded task in the MVP, metrics and UI card in P1 | (b) split by priority | The smallest MVP that still removes the pod shell and guards re-encryption | The metrics and the card read the payload the endpoint already computes, so the split saves little; without the gauge nothing alerts on a mixed-ring cluster, the state in which upstream credentials stop decrypting on some replicas |

**Choice:** (b) — a read-only key-ring status endpoint with metrics and a card on the system status screen, and re-encryption started through the task trigger, which refuses to run while the replicas' rings are incompatible.

**Rationale:** Today an operator follows the four rotation steps of R-HA-027 and checks two of them with `trove admin secrets status`, which in Kubernetes means `kubectl exec` into a pod, a right that also reads the key files. (b) exposes exactly the state a rotation needs (key IDs, counts, which replica holds which key) and nothing secret. It also covers a case the command cannot see: a running replica whose ring lacks a key that another replica has since made active cannot open the values sealed with it. The command sees only its own pod; the endpoint sees every replica through the registry, and the re-encryption task refuses to start in that state. (c) gives alerts but no guarded action; (d) moves key material into the system it protects; (e) saves only two gauges and one card, because both render the payload the endpoint already computes. The whole of (b) is MVP because the key ring and its rotation are MVP (R-ARCH-021, R-HA-027), the per-key counting already exists for `trove admin secrets status`, and the system status screen that shows the card is MVP (R-UI-038). The development agent revisits this when a key source backed by an external key management service arrives (ADR-032 names it as a later option), whose status would extend this payload.

**Applied in the spec:** New requirement R-API-049 (MVP, payload, rules, acceptance criteria), which starts re-encryption as the manual task `secrets-reencrypt` whose job kind carries the same name; R-API-020 lists it; R-UI-038 gains the "Stored-secret keys" card with "Re-encrypt now" and states why the screen is MVP; the security notes gain "Key-ring status without pod access"; the page's Covers list adds HA-016. Cross-page edits: the endpoint, the shared implementation and the job kind renamed from `secrets_reencrypt` to `secrets-reencrypt` in [architecture.md](architecture.md) R-ARCH-016 and R-ARCH-021; the replica registry columns, the manual task `secrets-reencrypt` and the two metrics in [operations-and-observability.md](operations-and-observability.md); the status check in the rotation steps of [ha-and-helm.md](ha-and-helm.md) R-HA-027; the MVP status of the UI status screen in R-OPS-003 and in [ha-and-helm.md](ha-and-helm.md) R-HA-003 and Q-HA-12, which still call it P1; the HA-016 row of the coverage checklist.

### Q-API-9 · One path for starting, stopping and reading maintenance work

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

The owning pages specified two paths for the same functions: [storage.md](storage.md) starts and stops garbage collection through `/api/v1/maintenance/gc-runs` and refreshes the storage summary at `/api/v1/storage/summary/refresh`, while [operations-and-observability.md](operations-and-observability.md) starts both through `/api/v1/system/tasks/{name}/run` and reads the summary at `/api/v1/system/storage/summary`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Domain paths only | storage.md's start, stop and refresh paths; the task trigger refuses these tasks | Each owning page shapes its own API | The system status screen and scripts need a special case per task; two ways to stop work (domain stop and job cancel) |
| (b) Generic paths only | Start through `/system/tasks/{name}/run`, stop through `/system/jobs/{id}/cancel`, results only in job results and under `/system/` | One mechanism, no domain views | Loses the domain view (counts and reclaimed bytes per run, history with filters) that a generic job result serves poorly |
| (c) Generic start and stop, domain read views | Start and stop through the task and job endpoints of R-OPS-019; the owning page keeps read-only views of runs and results under its own prefix | One way to start and stop every task, so "Run now" on the status screen works for all of them; domain data stays with its owner; matches R-API-015's "domain view that references the job" | Two pages change paths |

**Choice:** (c) — every singleton maintenance task starts only through `POST /api/v1/system/tasks/{name}/run` and stops only through job cancel, and owning pages keep read-only domain views.

**Rationale:** Start and stop are the same operation for every task, with the same mutual exclusion and the same 409 when a run is active (R-OPS-019); one implementation serves every task and the system status screen needs no special case. What differs per task is the result, and that belongs to the owning page, which is what R-API-015 already allows. (a) duplicates the trigger per task; (b) pushes domain data into generic job results. The task trigger already takes task parameters (`{"dryRun": true}`), and storage.md models incremental and full collection as two tasks with one schedule each, so nothing is lost; [storage.md](storage.md) reached the same answer in Q-STOR-10. The rule covers singleton tasks only: retention policies run per policy with their own queue, run states and reports (R-STOR-041), which the generic task list does not model, so their run and stop paths stay on [storage.md](storage.md). The development agent revisits this if a task needs a start contract the generic trigger cannot express.

**Applied in the spec:** R-API-020 replaces the "until review settles them" paragraph with the rule, its scope (singleton tasks; per-resource actions and retention runs keep their paths) and an acceptance criterion; R-API-040 starts garbage collection (tasks `gc` and `gc-full`, as [storage.md](storage.md) now defines them), the orphan sweep, the scrub and the storage-summary refresh through the task trigger, stops runs through job cancel, lists runs through `GET /api/v1/system/jobs?kind=…`, and keeps `GET /api/v1/storage/summary` and `GET /api/v1/storage/damaged-blobs` as the read paths; R-API-049 starts re-encryption the same way. Cross-page edits to [storage.md](storage.md), [operations-and-observability.md](operations-and-observability.md) and the GC acceptance criterion of [ha-and-helm.md](ha-and-helm.md) R-HA-002.

### Q-API-10 · Starting a backup from the API or the UI

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

The owner requires a configurable backup job, comparable to GitLab's, that writes a consistent backup to S3 when it runs (INV-19). It runs as a CronJob of the `backup` role, and only backup and restore pods hold the target's credentials ([backup-and-restore.md](backup-and-restore.md) R-BAK-015). The chart renders no RBAC for trove, and trove never calls the Kubernetes API ([ha-and-helm.md](ha-and-helm.md) R-HA-033). The catalog's backup screen (OPS-032) edits schedules and starts backups from the UI. The question is whether trove's API and UI may start a backup.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) No start endpoint; the UI shows the command | Runs and status are read-only (R-API-050); the UI shows `kubectl create job --from=cronjob/<name>` filled with the namespace and the CronJob's name | No new privilege anywhere; a manual run is the same Job as a scheduled one and lands in the same run list. GitLab's chart documents the same model: a `kubectl exec … backup-utility` command for a manual backup and `gitlab.toolbox.backups.cron.*` values for scheduled ones ([GitLab chart backups](https://docs.gitlab.com/charts/backup-restore/backup/), checked 2026-10-28) | Starting a backup needs `kubectl` access to the namespace |
| (b) `web` replicas create the Job through the Kubernetes API | A Role with `create` on Jobs and `get` on the CronJob for the replicas' ServiceAccount, a mounted token, a "Back up now" button | One click | Kubernetes' RBAC guidance states that permission to create workloads in a namespace implicitly grants access to the Secrets, ConfigMaps and PersistentVolumes that a pod there can mount, and the API access of any ServiceAccount there ([RBAC good practices](https://kubernetes.io/docs/concepts/security/rbac-good-practices/#workload-creation)). A compromised `web` pod could start a pod with the backup ServiceAccount and read the target credentials and the database owner Secret. Breaks R-HA-033 |
| (c) A request row picked up by a backup-role poller | The API inserts a request in PostgreSQL; a short-interval CronJob, or a long-running backup Deployment, starts a run when it finds one | No Kubernetes permission for the replicas | A pod every few minutes only to read a table, or a permanently running pod holding target credentials, against R-BAK-015's short-lived holders; the CronJob of INV-13 turns into a dispatcher; the start waits up to the poll interval |
| (d) The `worker` role runs backups as jobs | Backups become ordinary jobs | The job framework exists | Every `worker` pod would hold the target credentials and read access to every table (H-47 would grow to the whole role); a run of several hours on a Deployment pod conflicts with the drain rules of R-HA-059 |

**Choice:** (a) — the API and UI start, delete, verify and restore no backup, in any priority; the UI shows the `kubectl` command for a manual run, filled with the namespace and the CronJob's name that the chart renders into the configuration.

**Rationale:** The boundary of R-BAK-015 is what makes the backups trustworthy: only short-lived backup pods hold the target's credentials, and a compromised replica can neither read nor delete backups. (b) and (d) move that boundary into the pods that face the network, and (c) keeps it at the price of a polling dispatcher for a convenience that one copyable command already gives. Whoever runs the backup schedule already works with `kubectl` or Helm in that namespace. The schedule is a chart value of the CronJob and the retention a configuration key of the `backup` role (R-BAK-011), so the UI shows both read-only for the same reason. The owner's requirement, a configurable job that produces a consistent backup in S3 when it runs, is met by the CronJob and its values; nothing in it asks for a button. The development agent revisits this if installations need backups started by people without access to the namespace, for example as a step of a change ticket: (c) with a request row and a short-interval CronJob is then the next step, because it keeps the credentials in backup pods.

**Applied in the spec:** R-API-050 (read-only endpoints, `manualRun` in the summary, the `backup.manual_run` keys, an acceptance criterion against write operations), R-UI-048 (the command panel and no write control), R-UI-038's Backups card, the Deferred entry ADMAPI-083 and the security notes. Cross-page: [backup-and-restore.md](backup-and-restore.md) R-BAK-011 (`web` and `worker` also read `backup.manual_run`) and [ha-and-helm.md](ha-and-helm.md) R-HA-030 and R-HA-060 (the chart renders `config.backup.manual_run` with the release namespace and `<fullname>-backup`).

### Q-API-11 · Where the API's list of stored backups comes from

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-BAK-014 gives the API the run records only, and `trove backup list` reads the target with the target-reader credential. An operator in the UI wants to know which backups exist and what the retention kept, without target credentials in `web` pods.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Runs only | The UI lists create runs; which backups still exist is left to the reader | Nothing new | Retention's effect is invisible; `backup_run` rows older than `backup.run_history` disappear while their backups may still be kept by `keep_within` |
| (b) `web` replicas read the target | A target-reader credential in every `web` pod | The live truth | Every `web` pod could read every backup, a full copy of the database with token hashes and personal data (H-48); breaks R-BAK-015 |
| (c) Each target-writing run records what it found | After its deletions, a create or delete run stores in its result the complete backups left in the target and the retention it applied | No new credential and no new table; the run already lists the complete backups to apply retention (R-BAK-016) and to build the sweep's keep set (R-BAK-017); the view states its own age | As fresh as the last create or delete run; a backup removed outside trove shows until the next run |
| (d) A catalog table kept by the backup role | One row per backup in the target, updated by every run | Survives history pruning by design; queryable | A second copy of what (c) records, with its own consistency rules, grants for the backup role and a backup export class |

**Choice:** (c) — the newest create or delete run that listed the target is the source of `storedBackups` and `retention` in R-API-050.

**Rationale:** The question is how to show the target's state without giving `web` pods a key to it, and the backup run is the one place that already sees the target with the right credential at the right moment, right after it applied retention. Recording that list costs one field in a row the run writes anyway, and the newest run is never pruned, because history pruning removes only old rows. A view that is a day old is honest when it says so, and when runs stop, the overdue mark and `TroveBackupStale` are the signal that matters. (b) trades the credential separation for freshness, (d) duplicates (c) with more machinery, and (a) leaves retention unverifiable from the UI. The development agent revisits this if frequent backups with a long `keep_within` make the recorded list grow beyond about 1,000 entries per run, where (d) becomes cheaper than repeating the list in every result.

**Applied in the spec:** R-API-050 (`storedBackups` with `asOf` and `runId`, `retention`, acceptance criteria), R-UI-048 ("Stored backups", headed with the recording run). Cross-page: [backup-and-restore.md](backup-and-restore.md) R-BAK-016 ("Reporting": the result also carries `retention` with `keepLast` and `keepWithinSeconds`, and `retained`, the complete backups left after the run's deletions with ID, snapshot time, trove version, schema step, database bytes, blob count, blob bytes and the damaged flag) and R-BAK-014 (its API table points to R-API-050, and `expectedInterval` and `lastSuccessAt` move from the list answer into the summary as `expectedIntervalSeconds` and `lastSuccessAt`, because R-API-010 gives every list the same envelope and R-API-003 puts the unit into a duration's name).

### Q-API-12 · Visibility of a management write on the other replicas

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Revisited on 2026-10-28 after the owner added the Valkey, horizontal-scale, backup and storage-class constraints: the choice moves from (c) to the new option (e). Under random distribution, [authorization.md](authorization.md) Q-AUTHZ-12 gave grant changes a request header, `Trove-Authz-Min-Epoch`, which is option (b) restricted to the `authz` area; this page registers both headers and the codes `invalid_header`, `authz_epoch_unknown` and `authz_stale`, and a client that changed grants no longer has to wait for the bound.

Requests reach `web` replicas at random (INV-12), and each replica answers package and file requests from an in-process repository snapshot that learns of changes through the change log ([architecture.md](architecture.md) R-ARCH-018): within milliseconds with Valkey, within the poll interval without it. A script that creates a repository through one replica and publishes to it in the next step can reach a replica that does not know the repository yet. npm answers that `404` with a failure, because its fetch layer retries `408`, `420`, `429` and `5xx` only (`make-fetch-happen`, `lib/remote.js`, checked 2026-10-28).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Document the bound | Changes apply everywhere within the propagation bound; clients wait or retry | Nothing to build | npm and NuGet clients cannot be told to wait; a create-then-publish script fails intermittently, more often during a Valkey outage |
| (b) A consistency token | Writes answer with the change area's epoch; a request carrying `Trove-Min-Change-Epoch` waits up to 2 s until its replica has applied it | Exact read-your-writes for every kind of change, without affinity | Package clients cannot send the header, so the most common case stays open; one more header in the contract |
| (c) Management reads from PostgreSQL, an unknown repository key checked in PostgreSQL, the bound for the rest | API reads of configuration never come from a cache; a key the snapshot lacks is looked up before `404`, and a found key brings the snapshot up to date | Creation, the case package clients meet, is visible at once on every replica; management reads are exact; nothing for clients to learn | A request for an unknown key costs one indexed query, as an unknown token does (R-AUTH-023); changes to existing grants and repositories still follow the bound |
| (d) Session affinity | The edge keeps a client on one replica | Read-your-writes on that replica | Forbidden by INV-12 and R-ARCH-032; it defeats random load sharing and breaks at every drain |
| (e) (c), plus (b) for grant changes only | As (c); in addition, grant writes answer `Trove-Authz-Epoch`, and an authenticated request carrying `Trove-Authz-Min-Epoch` is decided on a grant index at least that new, or answered `412` when the database never reached the epoch ([authorization.md](authorization.md) R-AUTHZ-032) | (c)'s strengths; the UI and automation see their own grant changes, added or removed, on whichever replica answers next; one header with one owner instead of a token per change area | Two headers and three codes in the contract; package clients still rely on the bound for grant changes |

**Choice:** (e) — management reads come from PostgreSQL, an unknown repository key is looked up in PostgreSQL before `404`, a client that changed grants may name the authorization epoch in `Trove-Authz-Min-Epoch`, and every other effect on enforcement follows the propagation bound of R-ARCH-018, which R-API-052 states as the contract.

**Rationale:** The bound is short and well defined, so the only cases worth extra machinery are those where a client cannot wait. Package clients are that case, and what they meet in practice is a repository created a moment earlier by a setup script or a pipeline. (c) closes exactly that case with a lookup the replica would otherwise answer from its snapshot, while changes to existing configuration keep the bound, which R-ARCH-018 tests. Management reads cost little and are made by administrators and scripts that compare `ETag`s, so reading them from PostgreSQL makes `GET` after `PATCH` exact on every replica. The second case that cannot simply wait is a client that changes grants and uses them in the next step: the UI after a policy edit, a provisioning pipeline, and trove's own end-to-end tests in the `scatter` profile. Under random distribution it cannot pick the replica that answers next, so the epoch has to travel with the request, and [authorization.md](authorization.md) Q-AUTHZ-12 made that the `authz` area's header; (e) adopts it here instead of a general token, because the grant index is the one enforcement cache whose change a management client acts on at once. A general token for every area, (b), still helps scripts but not package clients, and (d) is ruled out. The development agent revisits this if scripts that create a repository and use it at once through a pattern grant fail in practice: the `repo_config` epoch then joins the same header, as Q-AUTHZ-12 foresees.

**Applied in the spec:** New requirement R-API-052; R-API-022's acceptance criterion sends its `npm view` through the Service; the acceptance criteria of R-API-031, R-API-032, R-UI-024 and R-UI-029 name the 2 s revocation bound, which [identity-and-authentication.md](identity-and-authentication.md) R-AUTH-023 already states as `auth.revocation_bound`. Cross-page: [architecture.md](architecture.md) R-ARCH-011 step 2 (a key that matches INV-5's pattern but is unknown to the snapshot is looked up in PostgreSQL before `404 repository_not_found`; a found key triggers an immediate change-log poll, coalesced with concurrent ones, and the request waits until the caches it invalidates, the grant index of R-AUTHZ-028 included, are rebuilt; repeated misses for one key are answered from a size-bounded per-replica negative cache that every `repo_config` change evicts), with R-ARCH-018 noting the poll a request may trigger and R-ARCH-032's state table gaining the negative cache. The revisit adds: R-API-052's bullet "Waiting for one's own permission change" with the two headers and an acceptance criterion; R-API-009's codes `invalid_header` (400), `authz_epoch_unknown` (412) and `authz_stale` (503 with `Retry-After`); R-API-018's CORS header lists; the epoch notes in R-API-033 and R-API-047; R-UI-042's handling of the header and of the two codes, with an acceptance criterion; R-UI-046's session store holding the epoch. No cross-page edit beyond those that [authorization.md](authorization.md) Q-AUTHZ-12 already lists.

### Q-UI-1 · Component library

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Angular Material, the CDK and `@angular/aria` | Material for standard widgets, the CDK for overlays and drag and drop, `@angular/aria` for widgets Material lacks | Released with every Angular major, so it moves with INV-1's pin; accessible tables, dialogs, menus and form fields from day one; theming through design tokens | Needs token work so trove does not look like a stock Material app; SVG icons through `MatIconRegistry` need a `bypassSecurityTrust*` value and insert markup through the CDK's own Trusted Types policy `angular#components`, which the CSP of R-UI-001 does not list, so icons come from a font |
| (b) A third-party suite (for example PrimeNG) | One large component set, including tree tables | More widgets out of the box | Release cadence decoupled from Angular majors, so an Angular upgrade waits for the vendor; another large dependency whose CSP and Trusted Types behavior must be verified |
| (c) Own components on the CDK and `@angular/aria` only | Every widget built in the project | Full control over look and markup | Weeks of widget work (tables, dialogs, date pickers, form fields) before the first screen, and each widget is an accessibility risk to test |

**Choice:** (a) — Angular Material and the CDK in the release line of the Angular major, plus `@angular/aria` for the file tree and the member editor's lists, themed through design tokens and with font icons.

**Rationale:** The first prototype has to work largely after one development pass, so the widget layer must be ready, accessible and upgradeable with Angular itself. (a) is the only option that is all three: it ships in lockstep with Angular (one version of every dependency), it has been built against Angular's own accessibility patterns, and `@angular/aria` fills the gaps the file browser and the member editor need. (b) adds a vendor whose majors trail Angular's; (c) spends the prototype on widgets. The one conflict with the strict CSP of R-UI-001 is SVG icon registration: `MatIconRegistry` takes URLs only as `SafeResourceUrl` values from a security bypass, which R-UI-046's lint rule bans, and inserts the SVG through the Trusted Types policy `angular#components` (verified in `@angular/cdk` 22.2.5), which the CSP's `trusted-types` list does not name. A self-hosted icon font used through ligatures needs neither, and widening the CSP for icons would weaken the README defense for a cosmetic gain. The development agent revisits this if the owner rejects the look at the walkthrough and token theming cannot fix it; the fallback is (c) for the affected widgets on the same CDK base.

**Applied in the spec:** R-UI-001 gains the component-library bullet (Material and CDK in the Angular release line, `@angular/aria` tree and listbox, token theming, icon font, no SVG icon registration with the reason) and an acceptance criterion; R-UI-010 builds the tree and R-UI-022 the member lists on `@angular/aria`.

### Q-UI-2 · Visual direction of the shell (example screenshot requested)

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Left side bar that collapses into a drawer, compact density, layouts by viewport breakpoints | Side bar pinned from 1024 CSS px, a drawer below; two-column screens switch on media queries over the viewport width | Suits administrators scanning long lists; keeps the many sections reachable (R-UI-003 has 27 routes); media queries are the familiar tool | A screen's breakpoint has to subtract the side bar's width, which depends on whether it is pinned, so every two-column screen carries the shell's layout logic |
| (b) Top navigation, comfortable spacing | Areas as tabs in the header, default density | Calm look for occasional users | Two areas with many sections do not fit a top bar; long lists need more scrolling |
| (c) Left side bar that collapses into a drawer, compact density, layouts by container query | As (a), but two-column screens switch on the width of the content area | (a)'s strengths, usable down to 320 CSS px (WCAG 1.4.10), and no screen knows the shell's state | Container queries need current browsers, which R-UI-001 already requires |
| (d) Navigation rail | A narrow rail of icons with short labels that expands into the side bar on wide screens | More content width between 1024 and 1440 CSS px | A rail holds a handful of destinations, not two areas with 25 routes; a second navigation design to build and test, and icon-led navigation for an audience that scans by name |

**Choice:** (c) — a left side bar pinned from 1024 CSS px and a drawer below, compact density (Material density `-2`) everywhere, and one- or two-column layouts switched by container queries on the content area.

**Rationale:** Most screens are lists and editors used by administrators and developers who come back daily, which favors density and a visible navigation over a spacious top bar. Pinning the side bar only where there is room keeps the reflow requirement without a second navigation design, which (d) would need. Container queries ([MDN](https://developer.mozilla.org/en-US/docs/Web/CSS/CSS_containment/Container_queries)) are supported by every browser version R-UI-001 supports and make each screen's layout depend on the space it actually has, so collapsing or pinning the side bar never breaks a screen; under (a) every two-column screen would have to know the side bar's state. Density `-2` keeps targets above the 24 CSS px of WCAG 2.5.8. The owner's example screenshot is no longer a precondition: the walkthrough takes its place, and a screenshot brought there adjusts tokens and spacing, not this structure. The development agent revisits this if the walkthrough shows that developers, not administrators, dominate use and want more spacing; density is one token.

**Applied in the spec:** R-UI-002 gains the layout and density bullet and an acceptance criterion for both widths.

### Q-UI-3 · Layout of the package details page (example screenshot requested)

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Install panel on top of the main column, key facts in a right column | The install panel sits between the header and the tabs at every width; on wide content areas a right column lists versions, repositories, licenses and downloads | One placement of the install command at every width; the facts stay beside the README | The install command scrolls away while a long README is read, and the right column repeats facts the header already shows |
| (b) One column at every width | Key facts and install command at the top, tabs below | One layout to build and test | On wide screens the install command scrolls away while the README is read; wasted width |
| (c) Two columns from 960 CSS px of content width, one column below with the install panel above the tabs | Sticky install panel beside the tabs on wide content areas; on narrow ones the install command comes before the README | The most frequent action stays visible at every width | Two layouts to test |

**Choice:** (c) — two columns with a sticky install panel when the content area is at least 960 CSS px wide, otherwise one column with the install panel directly below the header.

**Rationale:** Copying the install command is the most frequent action on this page (R-UI-009), and the README is its longest content. (c) keeps the command in view next to a long README on wide screens and puts it before the README on narrow ones; (a) shows it first but loses it after the first scroll, and (b) wastes the width a wide screen offers. The previous text placed it below the tabs on narrow screens, where it would sit under the whole README. The switch uses the container query of Q-UI-2, so the side bar's state does not matter. The owner's example screenshot is no longer a precondition; the walkthrough takes its place. The development agent revisits this if the walkthrough shows that the facts in the header deserve the right column more than the install panel.

**Applied in the spec:** R-UI-007's install-panel bullet is replaced by the layout rule (install panel above the tabs on narrow content areas), with a new acceptance criterion at 390 and 1440 CSS px.

### Q-UI-4 · Layout of the policy editor and the explainer (example screenshot requested)

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One page with sections and a sticky preview; explainer as its own screen | Create and edit on one page; the explainer at `/admin/permissions/explain`, linked from the editor | Policies are edited more often than created, and one page shows the whole grant at once; the explainer serves every entry point (users, groups, no-access state, claim tester) the same way | A long page for large policies, mitigated by the in-page table of contents |
| (b) Wizard for policies, explainer as a side panel in the editor | Steps for principals, resources, actions; an explain panel beside them | Guides first-time creators | Changing one action means stepping through every step; the explainer evaluates stored policies (R-API-034), so a panel next to an unsaved draft would explain a state the user does not see |
| (c) Wizard for create, one page for edit | Two editors for one document | Guidance on create, overview on edit | Two UIs for the same document to build, test and keep in step |

**Choice:** (a) — one page with sections, an in-page table of contents and a sticky preview fed by validation, and the explainer as its own screen opened from the editor in a new tab.

**Rationale:** A permission policy is reviewed far more often than written, and the dangerous mistakes (a wrong pattern, an extra action) are visible only when principals, resources and actions are seen together; a wizard hides two of the three at every step. The live preview of `validate-policy` gives the guidance a wizard would, on the draft itself. The explainer stays a separate screen because it answers questions about stored policies; opening it in a new tab keeps the editor's draft intact. The owner's example screenshot is no longer a precondition; the walkthrough takes its place. The development agent revisits this if the walkthrough shows that first-time policy creation is the common case.

**Applied in the spec:** R-UI-027 gains the layout bullet (one page for create and edit, sticky preview or collapsible region, "Test this policy" opening the explainer in a new tab for saved policies once the P1 explainer ships).

### Q-UI-5 · Internationalization mechanism

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `@angular/localize`, compile time, one build per locale | Builds under `/ui/en/` and `/ui/de/`; switching the language reloads | Angular's own mechanism, no extra dependency to keep in step with majors; a missing German text fails the build; ICU messages built in; no runtime cost | A locale prefix in UI URLs; a reload on language change; one more UI build in the binary per locale |
| (b) A runtime translation library (for example Transloco) | One build; translations loaded as JSON; switch without reload | Instant language switch; one build in the binary | A dependency outside Angular's release line; keys are strings, so a typo compiles, and missing keys are found only by a separate extraction tool in CI, not by the compiler; lookups at runtime |
| (c) `@angular/localize` with translations loaded at run time | One build; `loadTranslations()` before bootstrap | One build with Angular's message format | The build-time check of missing translations has to be rebuilt as a separate step; locale data registered by hand; still reloads on switch |

**Choice:** (a) — Angular's compile-time i18n with one build per locale under `/ui/{locale}/`.

**Rationale:** The decisive property is that a missing translation is a build failure, not a bug a German user finds; with two locales from the MVP on and an autonomous developer adding screens, that check is worth more than switching without a reload, which users do rarely. (a) also adds no dependency (INV-1's one-version rule stays easy) and costs nothing at run time; the serving rules for `/ui/{locale}/` already exist in R-ARCH-004 and R-UI-001. (c) keeps Angular's format but gives up the compiler's check; (b) gives up both and needs an extra CI tool to approximate the check. The development agent revisits this if the number of locales grows to where per-locale builds noticeably slow the release build or bloat the binary.

**Applied in the spec:** R-UI-045 states the mechanism as decided (no runtime translation library; switching reloads the route under the other locale).

### Q-UI-6 · External images in READMEs

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Block always | Every external image becomes a link with its alt text | No third-party request ever; the simplest CSP | Badges and diagrams from an internal GitLab or badge server never show, even where the operator trusts that host |
| (b) Allow every `https` image | `img-src https:` in the CSP | READMEs look as on public registries | Every viewer's address reaches any host a package author names, including tracking pixels in public packages served through remote repositories |
| (c) Proxy images through trove | trove fetches and serves the image with size and type limits | Viewer addresses stay inside | trove fetches URLs chosen by README authors from inside the network: a server-side request forgery surface controlled by package authors |
| (d) Block by default; operator allowlist of exact origins mirrored into the CSP | `ui.readme.external_image_origins` lists trusted origins; the sanitizer keeps images from them and the CSP allows exactly them | Air-gapped and cautious installations lose nothing; others enable exactly the hosts they trust; the browser enforces the same list | One configuration key; badges of public packages from unlisted hosts still show as links |

**Choice:** (d) — external images are blocked by default, and an operator can list exact `https` origins whose images are shown; the same list drives the sanitizer and the CSP `img-src`.

**Rationale:** The useful case for README images in a company registry is internal: badges and diagrams from the company's own GitLab or badge service. (d) serves that case without (b)'s leak of viewer addresses and package names to every host a package author picks, and without (c)'s request-forgery surface. Feeding the sanitizer and the CSP from one list means a sanitizer bug cannot load an image from an unlisted host, and the cross-origin request carries no page URL because UI documents use `Referrer-Policy: same-origin`. The previous recommendation, allowing every `https` image as an operator option, is dropped as too coarse. The development agent revisits this if operators ask for path-level restrictions, which CSP source expressions could express.

**Applied in the spec:** R-UI-008: the `img` rule, the origin-validation and cache bullet, the configuration key `ui.readme.external_image_origins` replacing `external_images`, and the acceptance criterion; R-UI-001: `img-src` takes the listed origins and the foreign-request rule names the exception; R-API-029: the README cache stores the hash of the list; the security notes describe the rule.

### Q-UI-7 · Roles, drain readiness and Valkey on the system status screen

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Since 2026-10-28 every installation runs separate `web` and `worker` Deployments, needs at least two replicas per role on different nodes for a drain without outage, and runs on Valkey with Sentinel, which can degrade trove without making a replica unready (INV-2, INV-12). The status screen of R-UI-038 showed a flat list of replicas and treated `objectStore` as the only degraded check. The question is what the screen shows about roles, drains and Valkey, and from which data.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Readiness checks only | Each replica shows its checks, `valkey` among the degraded ones | No new data | Nobody sees from the UI that a role has one ready replica left, that both `web` pods share a node, or that replicas disagree about the Valkey primary |
| (b) Registry-based role and Valkey view | The registry carries role, node (downward API `spec.nodeName`) and each replica's Valkey view (its `valkey` check, the primary it uses, the Sentinels it reaches); the screen groups by role, states drain safety per role and summarises Valkey | Shows the two conditions of a safe drain that trove can observe, and the Valkey states an operator must act on, from data the replicas already have; the catalog's status screen lists a node per service instance (OPS-008) | Two more registry columns per heartbeat; the screen cannot see disruption budgets or free capacity |
| (c) Read Kubernetes objects | The replicas read Deployments, PodDisruptionBudgets and nodes through the Kubernetes API | Desired replicas, PDB state and node capacity | Needs a ServiceAccount token and read RBAC, which R-HA-033 rules out, for data that `kubectl` and the cluster's monitoring already show |
| (d) Leave it to dashboards | The screen links to Prometheus and Grafana | Nothing to build in the UI | Installations without dashboards see nothing, and the status screen keeps claiming "all ready" while a role is one pod from an outage |

**Choice:** (b) — the replica registry gains the node name and the number of Sentinels each replica reaches, next to the Valkey primary and check it already records, and the status screen groups replicas by role, states drain safety per role, and summarises Valkey from the replicas' views.

**Rationale:** The owner requires a drain without outage "when the setup is designed for it", and the screen is where an operator without cluster access sees whether it currently is. Two of the conditions are visible to trove: at least two ready replicas per role, and on different nodes. The node name costs one environment variable from the downward API (`spec.nodeName` is offered as an environment variable only, [downward API](https://kubernetes.io/docs/concepts/workloads/pods/downward-api/), checked 2026-10-28), and every replica already computes its Valkey state, circuit, Sentinel reachability and primary for the metrics of R-HA-057. Showing the primary each replica uses exposes the one Valkey fault that metrics aggregated with `max` hide: a replica still bound to a demoted primary. (c) needs Kubernetes permissions that trove deliberately lacks, and (d) leaves the UI silent about the requirement the owner stressed. The screen states what it cannot see (budgets, capacity, the N + 1 rule). The development agent revisits this if trove ever gets read access to the Kubernetes API for another reason, which would let the screen show desired replicas and budgets too.

**Applied in the spec:** R-UI-038 (replicas by role with drain safety, the `valkey` degraded check, the Valkey card, the Backups card, the header's "not drain-safe", new acceptance criteria, and the MVP note that names the added registry fields), R-API-038 (the registry and system-information fields the screen reads, adopted unchanged from [operations-and-observability.md](operations-and-observability.md) R-OPS-003 and R-OPS-004, which own their shapes: per replica `roles` (`["all"]` for the development role), `state`, `node`, `checks`, `degraded` and the nested `valkey` view with `state`, `reason` in R-OPS-001's vocabulary, `circuit`, `primary`, `sentinelsReachable`, `sentinelsConfigured`, `serverVersion` and `lastPrimaryChangeAt`; per installation `replicas.roles.<role>` with `live`, `ready`, `degraded` and `nodes`, the `valkey` block of installation facts and the `backup` block). An earlier revision of this block named flat fields (`valkeyPrimary`, `valkeyPrimarySince`, `valkeySentinelsReachable`), `replicas.byRole` and other reason names; the choice is unchanged, only the field shapes follow their owner. Cross-page: [operations-and-observability.md](operations-and-observability.md) R-OPS-003 (column `node` and the `valkey` view column); [ha-and-helm.md](ha-and-helm.md) R-HA-030 (the chart sets `TROVE_NODE_NAME` from `spec.nodeName` for `web` and `worker` pods), R-HA-057 (`trove_valkey_sentinels_reachable` counts the Sentinels that answer a periodic probe of all configured ones) and Q-HA-12 (the status screen now needs these registry fields and the backup summary, so "adds no backend work" no longer holds).

### Q-API-13 · Problem member name, problem type, status of validation errors and error codes

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Absolute problem type on the public base URL | `type` is `https://<base url>/api/v1/problems/<code>` | Resolves to a page of the installation | Differs for every installation and for every alias of the host; cannot be compared across installations |
| (b) Relative reference `/api/v1/problems/<code>` served by trove from the embedded code registry (chosen) | `type` is the same on every installation; `GET /api/v1/problems/{code}` describes it | Identical everywhere; resolves on every base URL | Served by trove only |
| (c) `about:blank` with the code as a member | The RFC 9457 default | No catalog to serve | No description of the code for clients |

Request-ID member (chosen with the API conventions, `requestId`): (a) `request_id` in snake case, as the logs
name it; (b) `requestId` in camel case, as the rest of the API's JSON (R-API-003) (chosen). The member carries
the value of the `X-Request-Id` header.

Status of a validation failure: (a) 400 for every `invalid` error; (b) 400 for a request that cannot be parsed
or that names an unknown field, and 422 when the body parsed and the fields are invalid (chosen).

Codes (chosen): `malformed_request` for the 400 of an unparsable request (not `invalid_request`, which is the
OAuth form of [gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-008); `server_busy` for a replica at its concurrency
bound (not `too_many_requests_in_flight`); `database_timeout` for a statement that exceeded its limit, `503` with
`Retry-After` (R-ARCH-019).

**Choice:** (b) for the type, `requestId` for the member, the 400/422 split, and the three codes above.

**Rationale:** A client matches on `code` and on `type` across installations, so both must be the same
everywhere, and the API's JSON is camel case in every other member. The split keeps a parse error apart from a
field error without a second status family. The development agent would revisit the codes before any of them
is released, because a code is never renamed within an API major version (R-ARCH-025).

**Applied in the spec:** R-ARCH-023 names `requestId` (it said `request_id`, which contradicted R-API-009 and the
API's JSON); R-API-009 already states the type, the status split and the extension members. `malformed_request` is
in R-API-009's generic table; `server_busy` and `database_timeout` are named by their owning pages
([architecture.md](architecture.md) R-ARCH-005 and R-ARCH-019, R-API-014). Decision record:
[ADR-126](../decisions.md#adr-126--problem-details-with-a-relative-type-a-requestid-member-and-named-error-codes).

### Q-API-14 · Stand-in for `system.read` until authentication ships

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) No anonymous read; the development profile reads the replica list with a credential | No credential can be valid before M1 | No exposure | The development profile and its load test have no credential to read with |
| (b) `system.bootstrap_anonymous_read`, default `false`; `true` only where configured (chosen) | Anonymous callers read `/api/v1/system/info` and `/api/v1/system/replicas`. A presented `Authorization` header gets `401` (`bootstrapAuthenticate`). Startup refuses `true` with an `https` `server.base_url` other than localhost. | Off by default; the dev profile and its load test work; the refusal keeps it away from a public origin | Anyone who reaches the client listener of a development installation reads the replica list |
| (c) A fixed development principal that holds every permission | A stand-in identity in code | Works with every route | The stand-in becomes the authorization model by accident |

**Choice:** (b). The key is removed with the identity work at M1.

**Rationale:** The replica list and the installation facts are operator data, but the M0 dev profile must show them
to a load test. The refusal of the `https` origin keeps the switch away from anything that is not a development
installation, and the warning in the log says so at each start. The development agent would revisit the key when
M1 ships, where it is deleted rather than kept.

**Applied in the spec:** R-API-038 states the M0 stand-in and its refusal (paragraph under the endpoint table).

### Q-UI-8 · Bundle budget, CSP nonce and inline code

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) No budget | The build reports sizes only | No failing build | The initial bundle grows unnoticed |
| (b) Raw-size guard in `angular.json`: warning at 800 kB, error at 1500 kB (chosen as the first line) | Angular's budget on the build output | Fails on a gross regression with no extra tooling | Raw sizes say little about what a browser downloads |
| (c) Compressed budget of what `index.html` loads, checked by `web/scripts/check-budget.mjs` (400 kB, chosen as the rule of R-UI-001) | The script reads `index.html`, follows the files it loads and sums their gzip size; `build:embed` runs it after the build | The number of R-UI-001 is what is enforced | A script to keep in step with Angular's output names |

Nonce: `index.html` carries the literal `TROVE_CSP_NONCE` on the root element as `ngCspNonce`; the server replaces it with the
per-response nonce of the CSP. Inline code: critical-CSS inlining and font inlining are off in `angular.json` (`inlineCritical: false`,
`fonts.inline: false`), so no inline code needs the nonce.

**Choice:** (b) and (c) together, and the nonce with inlining off, as stated.

**Rationale:** The guard catches a gross regression at once; the compressed budget is the number that R-UI-001 states
for the user's download. Turning inlining off keeps the strict CSP of R-UI-001 without a hash for each inline block.
The development agent would revisit the raw guard if Angular's budgets change their meaning, and the compressed budget
when a route is loaded lazily and no longer part of the initial bundle.

**Applied in the spec:** R-UI-001 states the 400 kB compressed budget (unchanged); the mechanism is the choice of this block.

### Q-UI-9 · Test tiers of the web UI at M0

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Every tier of R-UI-047 at M0, including browser mode, contract tests and Playwright | The full table from the first commit | Nothing deferred | The M0 shell has no journeys to test in a browser, so the tiers would test nothing yet |
| (b) Unit and component tiers at M0 on jsdom through Angular's `unit-test` builder; browser mode, contract, Playwright and axe with the M1 screens (chosen) | The M0 table has two tiers | The tiers that exist test real logic; the rest arrive with the screens they test | R-UI-047's table describes M1 and later for the rest |
| (c) Unit tests only | Nothing else at M0 | Smallest | No component test of the shell |

**Choice:** (b).

**Rationale:** Browser-based tests need screens. Writing them against the M0 shell would test placeholders and cost
the build time that the coverage evidence needs. The development agent would revisit this when the first screen
of M1 lands, which is when the browser tiers and axe start.

**Applied in the spec:** R-UI-047 is unchanged; its table describes the full target, and this block states the M0 subset.

### Q-API-15 · Target of `user_preferences.user_id`

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

The sketch in R-API-021 declares `user_id uuid PRIMARY KEY REFERENCES users(id)`. No table `users`
exists: users are the `principal` and `app_user` rows of R-AUTH-016, keyed by `principal_id`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `app_user (principal_id)` (chosen) | The preferences row goes with the user | A user and nothing else can have preferences; deleting a user deletes them | none |
| (b) `principal (id)` | Any principal | Looser | Service accounts and CI mappings would be allowed rows that R-API-021 answers 403 for |

**Choice:** (a), `ON DELETE CASCADE`.

**Applied in the spec:** R-API-021 data model.

## What this does not cover

### H-43 · General API rate limits are per replica

Component: the limiter of the management API (R-API-016) on top of R-ARCH-034 in
[architecture.md](architecture.md). Subject: every caller of `/api/v1/` and `/files/`, by token,
session user or client address. Since 2026-10-28 the `read`, `write` and `anonymous` buckets count
cluster-wide in Valkey (Q-API-3), so the configured budget no longer multiplies with the replica
count. What remains is narrower than the heading: counting per replica while Valkey is unavailable,
and counters lost in a failover. Live in the MVP in these cases:

- **Valkey outage.** While a replica cannot reach Valkey, its buckets are local, with the budget
  divided by the live `web` replicas of the registry. If only some replicas lose Valkey, the others
  still count the full budget in Valkey, so a client that spreads its requests gets up to one share
  more than the budget per cut-off replica. After a scale-up during an outage, every replica
  divides by the old count until its next registry read, for at most `system.heartbeat_interval`.
- **Counters lost after a failover.** A Sentinel failover that loses recent writes resets the
  counters it lost, which admits at most one extra window per bucket (R-ARCH-034). A Valkey
  restart without persistence has the same effect. trove's own cache flush leaves the counters
  alone ([operations-and-observability.md](operations-and-observability.md) R-OPS-047).

Tampering is not part of this gap: whoever can write under trove's Valkey prefix can delete
counters and lift every budget, or raise them and refuse a caller with `429`, and that is H-49 in
[architecture.md](architecture.md), which covers every value trove keeps in Valkey.

What makes it real: a client that wants more throughput than its budget, for example to scrape
package metadata, and that can time its traffic to a Valkey outage or failover. It does not weaken
credential guessing: tokens are 256-bit random values, and the break-glass lockout counts failures
in PostgreSQL (R-AUTH-038), which survives a loss of Valkey's data. Per-replica in-flight limits (`server.max_in_flight`) still protect each replica in
every case.

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
