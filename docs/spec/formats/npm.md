# npm registry

Status: specification — not implemented

This page specifies how trove speaks the npm registry protocol for local, remote and virtual
npm repositories: the endpoints below `/api/npm/<repoKey>/`, how the npm format plug-in classifies
requests, how package documents are generated, cached and merged, how publish, dist-tags,
deprecation and unpublish change state, how a remote repository fetches and validates npm
metadata, how a virtual repository merges members, and the client acceptance tests that gate the
format. It is the npm half of the format plug-in contract: generic semantics live on the owner
pages and are referenced, not repeated.

| Topic | Owner |
|---|---|
| Repository model, include/exclude patterns, virtual resolution, merged-metadata cache, deployment target, priority members, pin rules and namespace claims, every remote setting (cache periods, negative cache, refresh timeout, circuit, egress, redirects, size limits, integrity policy, URL rewriting, upstream credentials) | [repositories-and-proxy.md](../repositories-and-proxy.md) |
| Blob store, packages, versions and items tables, upload pipeline, trash can, garbage collection | [storage.md](../storage.md) |
| Credential sources, 401 challenges, tokens, anonymous access, npm web login, legacy login and logout | [identity-and-authentication.md](../identity-and-authentication.md) |
| Actions per npm operation, virtual gate, 403/404 decisions, package ownership | [authorization.md](../authorization.md) |
| GitLab ID-token validation and identity mappings | [gitlab-ci-oidc.md](../gitlab-ci-oidc.md) |
| Audit event storage, jobs, metrics conventions | [operations-and-observability.md](../operations-and-observability.md) |
| Client setup dialog, management API conventions | [api-and-ui.md](../api-and-ui.md) |
| Format plug-in interface, configured public base URL, trusted reverse proxies | [architecture.md](../architecture.md) |

Covers: every MVP and P1 entry of the npm format catalog
[features/formats/npm.md](../../features/formats/npm.md), and the npm-specific parts of
[features/remote-repositories.md](../../features/remote-repositories.md),
[features/repositories.md](../../features/repositories.md),
[features/formats/README.md](../../features/formats/README.md),
[features/identity-and-access.md](../../features/identity-and-access.md),
[features/access-tokens.md](../../features/access-tokens.md),
[features/oidc-ci-integration.md](../../features/oidc-ci-integration.md),
[features/search-and-query.md](../../features/search-and-query.md) and
[features/ui-inventory.md](../../features/ui-inventory.md).

Conventions on this page:

- `https://trove.example.com` is the public base URL *(example)*; `npm-local`, `npm-remote` and
  `npm-all` (virtual, members `npm-local` then `npm-remote`, deployment target `npm-local`) are
  example repository keys; `@acme` is an example internal scope; `${TROVE_TOKEN}` is a trove
  token (INV-8).
- The *registry URL* of a repository is `<public base URL>/api/npm/<repoKey>/`, always with a
  trailing slash. Paths in tables are relative to it unless marked **host root**.
- `{name}` is a package name; in request paths it may arrive as `@scope%2fname`, `%40scope%2fname`
  or `@scope/name` (R-NPM-002).
- External references: npm's registry documentation
  ([REGISTRY-API.md](https://github.com/npm/registry/blob/main/docs/REGISTRY-API.md),
  [package-metadata.md](https://github.com/npm/registry/blob/main/docs/responses/package-metadata.md)),
  [npmrc](https://docs.npmjs.com/cli/configuring-npm/npmrc),
  [npm config](https://docs.npmjs.com/cli/using-npm/config),
  [pnpm settings](https://pnpm.io/settings),
  [Yarn settings](https://yarnpkg.com/configuration/yarnrc),
  [SemVer 2.0.0](https://semver.org/spec/v2.0.0.html),
  [Subresource Integrity](https://www.w3.org/TR/SRI/),
  [RFC 9110](https://www.rfc-editor.org/rfc/rfc9110). Client behavior stated on this page was
  checked against the linked client sources on 2026-10-28.

## Protocol surface

### R-NPM-001 · Registry endpoint, routing and repository classes
Priority: **MVP** · Covers: PKG-NPM-001, PKG-NPM-002, PKG-NPM-056, REPO-005

Every npm repository, whatever its class, MUST answer the npm registry protocol under its registry
URL `/api/npm/<repoKey>/` (INV-6). A request for a repository key that does not exist, or whose
format is not npm, answers 404 with the npm error body (R-NPM-005).

- The router MUST work on the escaped request path (Go: `URL.EscapedPath()`), never on a path in
  which `%2f` has already become `/` or that has been cleaned, so `@scope%2fname` and
  `@scope/name` reach the same handler (R-NPM-002).
- A first path segment of `-` is reserved for protocol operations (`-/ping`, `-/whoami`, `-/v1/…`,
  `-/package/…`, `-/user/…`, `-/npm/v1/…`, `-/external/…`). Anything else is a package name: one
  segment for an unscoped name; for a name starting with `@`, two segments or one segment
  containing `%2f`.
- Behind the name the suffixes are: none (package document), `/{version-or-tag}` (single-version
  document), `/-/{file}.tgz` (tarball), `/-rev/{rev}` and `/-/{file}.tgz/-rev/{rev}` (revisioned
  writes).
- `GET` on the registry URL itself answers 200 `{}`. Every `GET` route also answers `HEAD`.
- One prefix at the **host root** of the public base URL belongs to the npm plug-in (INV-6,
  Q-NPM-1): `/-/npm/v1/oidc/token/exchange/package/` (R-NPM-022, P1). npm builds that URL against
  the host root of the registry URL, not its path, so it cannot live below `/api/npm/<repoKey>/`;
  it needs no repository key, because the exchange is bound to a package by its path. The same
  exchange is also served below every registry URL (`-/npm/v1/oidc/token/exchange/package/{name}`),
  where Yarn 4 and current pnpm releases post it. trove owns the host root of `server.base_url`,
  which is an origin without a path ([operations-and-observability.md](../operations-and-observability.md)
  R-OPS-005). Any other host-root path, npm's attestation path `/-/npm/v1/attestations/…` included,
  answers the generic 404 of [architecture.md](../architecture.md) R-ARCH-005; attestations will be
  served below the registry URL (R-NPM-027).
- An operation the repository class cannot perform answers 405 with a reason (for example a
  publish to a remote repository).
- Endpoints npm defines but trove does not implement answer **404** for `GET`, `PUT` and
  `DELETE`, and **501** for `POST`. npm's fetch layer retries `GET` and `PUT` on any 5xx and never
  retries `POST` ([make-fetch-happen](https://github.com/npm/make-fetch-happen/blob/main/lib/remote.js)),
  so a 5xx on a `GET` would cost the user retries, while 501 on a `POST` costs nothing and says
  what it means.

<details>
<summary>Endpoint map</summary>

| Method | Path | Requirement | local | remote | virtual |
|---|---|---|---|---|---|
| GET, HEAD | `{name}` | R-NPM-010, R-NPM-011 | yes | yes | merged |
| GET, HEAD | `{name}/{version-or-tag}` | R-NPM-014 (P1) | yes | yes | merged |
| GET, HEAD | `{name}/-/{basename}-{version}.tgz` | R-NPM-015 | yes | yes | resolved |
| PUT | `{name}` with a `.tgz` attachment | R-NPM-007 | yes | 405 | deployment target |
| PUT | `{name}` without a `.tgz` attachment | R-NPM-018 (P1) | yes | 405 | deployment target |
| PUT | `{name}/-rev/{rev}` | R-NPM-017 | yes | 405 | deployment target |
| DELETE | `{name}/-rev/{rev}`, `{name}/-/{file}.tgz/-rev/{rev}` | R-NPM-017 | yes | 405 | deployment target |
| GET | `-/package/{name}/dist-tags` | R-NPM-016 | yes | yes | merged |
| PUT, DELETE | `-/package/{name}/dist-tags/{tag}` | R-NPM-016 | yes | 405 | deployment target |
| GET | `-/ping`, `-/whoami` | R-NPM-019 | yes | yes | yes |
| POST | `-/v1/login` | R-NPM-020 (P1) | yes | yes | yes |
| PUT, GET | `-/user/org.couchdb.user:{username}` | R-NPM-021 (P1) | yes | yes | yes |
| DELETE | `-/user/token/{token}` | R-NPM-021 (P1) | yes | yes | yes |
| GET | `-/v1/search` | R-NPM-037 (P1) | yes | cached only | merged |
| POST | `-/npm/v1/security/advisories/bulk` | R-NPM-038 (P1) | 501 | yes | via member |
| POST | `-/npm/v1/security/audits/quick`, `-/npm/v1/security/audits` | R-NPM-038 | 501 | 501 | 501 |
| GET | `-/external/{remoteKey}/{path}` | R-NPM-034 (P1) | 404 | 404 | yes |
| POST | `/-/npm/v1/oidc/token/exchange/package/{name}` (**host root**) | R-NPM-022 (P1) | — | — | — |
| POST | `-/npm/v1/oidc/token/exchange/package/{name}` | R-NPM-022 (P1) | yes | yes | yes |
| GET | `-/npm/v1/keys`, `-/npm/v1/attestations/…`, `-/npm/v1/tokens…`, `-/npm/v1/user`, `-/org/…`, `-/team/…`, `-/package/{name}/access`, `-/package/{name}/visibility`, `-/package/{name}/collaborators`, `-/all` | Deferred | 404 | 404 | 404 |
| POST | `-/stage/package/{name}` | Deferred | 501 | 501 | 501 |

The web-login poll URL lives outside the registry URL (R-NPM-020).

</details>

Acceptance criteria:

- `curl -s https://trove.example.com/api/npm/npm-all/` answers 200 `{}`;
  `curl -s -o /dev/null -w '%{http_code}' https://trove.example.com/api/npm/does-not-exist/lodash`
  prints `404`.
- `npm view lodash version` with `--registry` set to `…/npm-local/` (holding lodash, published
  there by a `manage` holder because the test upstream serves it,
  [repositories-and-proxy.md](../repositories-and-proxy.md) R-REPO-048), `…/npm-remote/` and
  `…/npm-all/` succeeds in each case.
- `npm publish --registry https://trove.example.com/api/npm/npm-remote/` fails with `E405` and
  a message naming the repository as remote.
- A `HEAD` on a package document returns the same `ETag` as the `GET`.

### R-NPM-002 · Package names and scoped-name encoding
Priority: **MVP** · Covers: PKG-NPM-003, PKG-NPM-004

- Every path that carries a package name MUST accept the scope separator as `/`, `%2f` and `%2F`,
  and a leading `%40` as `@`. The name is percent-decoded exactly once and then matched exactly
  and case-sensitively; legacy upstream names with capitals are served unchanged. The normalized
  name (scoped as `@scope/name`) is the package name of the request descriptor
  ([repositories-and-proxy.md](../repositories-and-proxy.md) R-REPO-003) and the coordinate of
  [authorization.md](../authorization.md) R-AUTHZ-005.
- A decoded name with an empty, `.` or `..` segment, a NUL byte, a backslash, a `%` or more than
  one `/` MUST be rejected with 400 before any lookup. No npm name contains `%`; a name that still
  carries one after the single decoding step is a second layer of escapes aimed at an upstream
  ([repositories-and-proxy.md](../repositories-and-proxy.md) R-REPO-025).
- Names of **newly published** packages (R-NPM-007, R-NPM-036) MUST satisfy npm's rules for new
  names ([validate-npm-package-name](https://github.com/npm/validate-npm-package-name)): 1 to 214
  characters including the scope; lower case; URL-safe (each of scope and name unchanged by
  `encodeURIComponent`); none of `~'!()*`; no leading `.` or `_`; no leading or trailing
  whitespace; not a Node.js core module name; not `node_modules` or `favicon.ico`. Violations
  answer 400.
- A new name that equals an existing name of the same local repository once both are lower-cased
  and stripped of `-`, `_` and `.`, without being that name, MUST be rejected with 400. This keeps
  look-alikes from appearing next to an internal package; remote and virtual repositories serve
  upstream names unchanged.

Acceptance criteria:

- `curl …/npm-all/@acme%2fui`, `…/npm-all/@acme/ui` and `…/npm-all/%40acme%2Fui` return
  byte-identical bodies with the same `ETag`, also through the Helm chart's Ingress.
- `npm publish` of `Acme-UI` fails with 400; publishing `acme_ui` fails with 400 when `acme-ui`
  exists in the target repository.
- `npm view JSONStream version` through `npm-remote` succeeds.
- `curl …/npm-remote/a%2525b` answers 400, and the test upstream records no request.

### R-NPM-003 · Request classification
Priority: **MVP** · Covers: PKG-NPM-034, PKG-NPM-047

The npm plug-in produces the request descriptor of
[repositories-and-proxy.md](../repositories-and-proxy.md) R-REPO-003 for every npm request. This
table is the normative answer to "which npm paths are metadata":

| Request | Canonical path | Kind | Variant | Package, version |
|---|---|---|---|---|
| `GET {name}` | `<name>` | `metadata` | `full` or `abbreviated` (R-NPM-011); `?write=true` is `write-read` and never cached | name |
| `GET {name}/{version-or-tag}` | `<name>/<version-or-tag>` | `metadata` | `full` | name, version if canonical |
| `GET {name}/-/{basename}-{version}.tgz` | `<name>/-/<basename>-<version>.tgz` | `artifact` | — | name, version; expected digests from the serving document (R-NPM-025) |
| `GET -/package/{name}/dist-tags` | `<name>` | `metadata` | `dist-tags` | name |
| publish, metadata `PUT`, `-rev` writes, dist-tag `PUT`/`DELETE`, tarball `DELETE` | `<name>` or the tarball path | `write` | — | name, version where present |
| `GET -/v1/search` | — | `search` | — | — |
| `-/ping`, `-/whoami`, login, logout, audit, external | — | `other` | — | — |

Consequences:

- Repository include/exclude patterns (R-REPO-005) have the `name` subject on npm: they match the
  normalized package name, not the path. `@acme/*` admits the document, every tarball and the
  dist-tags of every `@acme` package; `acme-*` admits unscoped `acme-…` packages. Permission
  patterns (R-AUTHZ-005) use the same package-name syntax. Either way one rule covers a package's
  metadata and files, so the catalog's trap of a policy that admits the tarball but not the
  separately stored metadata (PKG-NPM-047) cannot occur.
- Every stored tarball is an item at its canonical path (storage.md); raw access through
  `/files/<repoKey>/<path>` (INV-6) uses the same path. Package documents are generated and have
  no item.
- The package document's canonical path `<name>` is the package-document path the npm catalog
  entry registers (R-REPO-003, written `n` there; R-NPM-023): upstream gone-marking (R-REPO-029)
  and the shadowing check's stored-state lookup and probe (R-REPO-048) use it.

Acceptance criteria:

- The resolution trace (`GET /api/v1/repositories/npm-all/resolution?path=@acme/ui/-/ui-1.2.0.tgz`)
  reports kind `artifact` and package `@acme/ui`; for `path=@acme/ui` it reports `metadata`.
- With `excludePatterns: ["@acme/**"]` on `npm-remote`, neither `npm view @acme/ui` nor a tarball
  request for an `@acme` package reaches the test upstream.

### R-NPM-004 · Credentials, challenges and permissions on npm endpoints
Priority: **MVP** · Covers: PKG-NPM-005, PKG-NPM-006, PKG-NPM-007, TOK-033, TOK-034, IAM-005, IAM-021, IAM-022, IAM-023, IAM-024

Authentication is [identity-and-authentication.md](../identity-and-authentication.md) R-AUTH-001,
R-AUTH-002, R-AUTH-033 and R-AUTH-037: a token as `Bearer` (npm `_authToken`, Yarn
`npmAuthToken`) or as Basic password with any username (npm keyed `_auth` or `username` plus
`_password`, Yarn `npmAuthIdent`); 401 with `WWW-Authenticate: Basic realm="trove"`, never with
the element `otp` or `ipaddress` (npm and pnpm switch to a one-time-password prompt or an
IP-restriction message on those). Authorization is [authorization.md](../authorization.md):
R-AUTHZ-007 is the normative action map for npm operations (`read`; `deploy` for a new version;
`delete` for unpublish; `deploy` for dist-tags and deprecation, because both change what clients
install; ownership checks per
R-AUTHZ-018), R-AUTHZ-008 the cache-fill rule, R-AUTHZ-009 the virtual gate and per-member reads,
R-AUTHZ-016 the 401/403/404 choice. This requirement adds what npm clients need on top:

- Clients attach credentials only to URLs under the registry URL they were configured with
  (npm and pnpm: longest `//host/path/:` key that prefixes the request URL; Yarn 1: same prefix
  plus `always-auth` or a scoped package; Yarn 2+: `npmAlwaysAuth`, or a scoped package). Every
  URL trove places into an npm response (tarball URLs, rewritten external dependencies) MUST
  therefore lie under the registry URL of the repository that served the response, built from the
  configured public base URL ([architecture.md](../architecture.md)), never from a `Host` header
  that did not come through a trusted reverse proxy.
- Client setup instructions (R-NPM-041) MUST emit only keyed credential forms and add
  `always-auth=true` (Yarn 1) or `npmAlwaysAuth: true` (Yarn 2+) when the repository is not
  anonymously readable.
- The tag cleanup that follows an unpublish (R-NPM-016) is part of the `delete` and needs no
  `deploy`.

Acceptance criteria:

- With `registry=https://trove.example.com/api/npm/npm-all/` and
  `//trove.example.com/api/npm/npm-all/:_authToken=${TROVE_TOKEN}` in `.npmrc`, `npm install
  lodash` succeeds against a repository without anonymous read; without the second line it fails
  with `E401` and never prompts for a one-time password.
- `curl -u anything:${TROVE_TOKEN} …/npm-all/lodash` returns 200.
- A request with `Host: evil.example` gets a document whose `dist.tarball` URLs all start with
  `https://trove.example.com/api/npm/`.
- The acceptance criteria of R-AUTHZ-007 to R-AUTHZ-009 pass with every gate row of R-NPM-045.

### R-NPM-005 · Error bodies and protocol headers
Priority: **MVP** · Covers: PKG-NPM-060

- Every error response on npm endpoints MUST carry `Content-Type: application/json` and
  `{"error": "<human-readable text>", "message": "<same text>", "code": "<code>"}`. npm prints
  `error` first ([npm-registry-fetch](https://github.com/npm/npm-registry-fetch/blob/main/lib/errors.js)),
  other clients read `message`; the status is authoritative.
- `code` carries the code the owning page defines (`credentials_required`, `invalid_token`,
  `path_not_admitted`, `no_deploy_target`, `populate_not_permitted`,
  `upstream_integrity_mismatch`, `egress_denied`, `foreign_host_denied`, `shadows_remote_package`,
  `shadow_check_unavailable`, …); the table lists the npm-specific codes and the generic
  `quota_exceeded`, whose npm behavior this page fixes:

| Status | `code` | When |
|---|---|---|
| 400 | `npm_invalid_name`, `npm_invalid_version`, `npm_invalid_tag`, `npm_bad_publish`, `npm_manifest_mismatch` | R-NPM-002, R-NPM-007, R-NPM-016 |
| 405 | `npm_class_not_writable` | write to a remote repository |
| 409 | `npm_version_exists`, `npm_version_unpublished`, `npm_stale_rev` | R-NPM-008, R-NPM-009 |
| 413 | `npm_too_large` | R-NPM-007 limits |
| 501 | `npm_audit_unavailable`, `npm_not_implemented` | R-NPM-038, unimplemented `POST` endpoints |
| 502 | `npm_invalid_upstream_document` | R-NPM-024 |
| 507 | `quota_exceeded` (generic, [storage.md](../storage.md) R-STOR-029, P1) | A publish or raw deploy would exceed the system capacity limit or the target repository's quota (storage.md Q-STOR-7). The `error` text names the limit: the repository key or the system limit. npm retries the publish `PUT` on a `5xx` twice by default (after 10 s and 60 s), so `npm publish` reports it after about 70 s |

- `npm-notice` (a response header npm prints on any response,
  [npm-registry-fetch](https://github.com/npm/npm-registry-fetch/blob/main/lib/check-response.js);
  Yarn 2+ prints it only on error responses) is set only where this page says so (R-NPM-016,
  R-NPM-038).
- Request headers `npm-command`, `npm-session`, `npm-scope`, `npm-in-ci` and `npm-auth-type` are
  logged (R-NPM-044) and never used for decisions.

Acceptance criteria:

- `npm publish` of an existing version prints the trove error text, not a bare `409 Conflict`.
- A contract test over all npm routes finds no `otp` element in any `WWW-Authenticate` header and
  no error response without the three body fields.

## Local repositories

### R-NPM-006 · Storage model
Priority: **MVP** · Covers: PKG-NPM-033, PKG-NPM-034, REPO-029, FMT-008

- A published version is a `packages` row, a `package_versions` row and one item, the tarball at
  its canonical path, as lead item ([storage.md](../storage.md) R-STOR-003). Tarball bytes live
  only in the blob store (INV-4). A provenance bundle sent with a publish is a second item of the
  version at `<name>/-/<basename>-<version>.sigstore`.
- npm-owned state lives in the tables below. Package documents are not stored; they are rendered
  from these rows (R-NPM-010) and cached (R-NPM-013).
- The package's revision counter is `rev_seq` on storage.md's generic `packages` row
  ([architecture.md](../architecture.md) R-ARCH-009), not an npm column. npm keeps no second
  counter: the core reads only the generic column for read-your-writes and for the merged-metadata
  check (R-ARCH-012, R-ARCH-015), so every npm write that changes a package increments
  `packages.rev_seq` in its transaction.
- Because an npm file can only ever live on its canonical path, REPO-029's strict layout is
  permanently on and has no setting. The signing-key settings of REPO-029 are deferred with
  PKG-NPM-037.
- The format properties `npm.name`, `npm.version` and, for scoped names, `npm.scope` are exposed
  read-only on every stored or cached tarball (FMT-008), derived from these rows.

Data model (sketch; `packages`, `package_versions`, `items`, `repositories` are storage.md's):

```sql
CREATE TABLE npm_packages (
  package_id     bigint      PRIMARY KEY REFERENCES packages (id) ON DELETE CASCADE,
  repo_id        bigint      NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
  name_fold      text        NOT NULL,            -- lower(name) without '-', '_', '.' (R-NPM-002)
                                                  -- no rev_seq here: packages.rev_seq (R-NPM-009)
  dist_tags      jsonb       NOT NULL DEFAULT '{}'::jsonb,  -- {"latest": "1.2.0", "next": "2.0.0-rc.1"}
  modified_at    timestamptz NOT NULL DEFAULT now(),
  search_vector  tsvector,                        -- R-NPM-037
  UNIQUE (repo_id, name_fold)
);

CREATE TABLE npm_versions (
  version_id         bigint      PRIMARY KEY REFERENCES package_versions (id) ON DELETE CASCADE,
  manifest           jsonb       NOT NULL,        -- stored manifest (R-NPM-007), without dist.tarball
  integrity          text        NOT NULL,        -- 'sha512-<base64>'
  shasum             char(40)    NOT NULL,        -- SHA-1 hex
  deprecated         text,                        -- NULL = not deprecated
  file_count         integer     NOT NULL,
  unpacked_size      bigint      NOT NULL,
  has_install_script boolean     NOT NULL,
  has_shrinkwrap     boolean     NOT NULL,
  published_at       timestamptz NOT NULL
);

CREATE TABLE npm_tombstones (                     -- outlives trash and permanent removal (R-NPM-008)
  repo_id        bigint      NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
  name           text        NOT NULL,
  version        text        NOT NULL,
  integrity      text        NOT NULL,
  unpublished_at timestamptz NOT NULL,             -- time of the unpublish or the move out
  unpublished_by uuid        NOT NULL,             -- its actor
  PRIMARY KEY (repo_id, name, version)
);
```

Acceptance criteria:

- After `npm publish` of `@acme/ui@1.2.0` to `npm-local`,
  `curl -sI https://trove.example.com/files/npm-local/@acme/ui/-/ui-1.2.0.tgz` returns 200 and
  the item API shows properties `npm.name=@acme/ui`, `npm.version=1.2.0`, `npm.scope=@acme`.

### R-NPM-007 · Publish
Priority: **MVP** · Covers: PKG-NPM-015, PKG-NPM-057

`PUT {name}` whose `_attachments` holds a `.tgz` entry publishes one version (`npm publish`,
`pnpm publish`, Yarn 1 `yarn publish`, Yarn 2+ `yarn npm publish`; request shape from
[libnpmpublish](https://github.com/npm/cli/blob/latest/workspaces/libnpmpublish/lib/publish.js)):

```json
{
  "_id": "@acme/ui",
  "name": "@acme/ui",
  "dist-tags": { "latest": "1.2.0" },
  "versions": {
    "1.2.0": {
      "name": "@acme/ui", "version": "1.2.0", "dependencies": { "react": "^19.0.0" },
      "dist": { "integrity": "sha512-…", "shasum": "…", "tarball": "http://…/@acme/ui/-/@acme/ui-1.2.0.tgz" }
    }
  },
  "access": null,
  "_attachments": {
    "@acme/ui-1.2.0.tgz": { "content_type": "application/octet-stream", "data": "<base64>", "length": 48213 },
    "@acme/ui-1.2.0.sigstore": { "content_type": "application/vnd.dev.sigstore.bundle+json;version=0.3", "data": "…", "length": 5012 }
  }
}
```

Processing:

1. Authorize per R-AUTHZ-007 (`deploy` and ownership) on the target: the local repository, or a
   virtual repository's deployment target (R-NPM-029). Remote repositories answer 405. The
   target's include patterns apply (R-REPO-005, 403 `path_not_admitted`). The first publish of a
   name that a remote member of a containing virtual repository serves is subject to the
   shadowing check of [repositories-and-proxy.md](../repositories-and-proxy.md) R-REPO-048
   (403 `shadows_remote_package`, 503 `shadow_check_unavailable`); the canonical path it checks
   and probes is the package document `<name>`, npm's registered package-document path (R-NPM-003).
2. Stream the body through the upload pipeline ([storage.md](../storage.md) R-STOR-004): the
   attachment's base64 MUST be decoded straight into the spool while all digests are computed;
   the decoded tarball MUST NOT be held in memory. `npm.publish.max_tarball_size` caps the tarball,
   the body is capped at 4/3 of it plus 1 MiB; beyond: 413. From P1, admission also refuses a
   publish over the system capacity limit or the target repository's quota with 507
   `quota_exceeded` ([storage.md](../storage.md) R-STOR-029, R-NPM-005).
3. Validate (400 unless noted):
   - `name` equals the decoded path name and satisfies R-NPM-002;
   - `versions` holds exactly one entry, `dist-tags` exactly one entry pointing to it; the tag is
     not a valid SemVer range and matches `^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`;
   - the version is valid SemVer 2.0.0 and equal to its canonical form without build metadata;
   - exactly one attachment ends in `.tgz`, its decoded length equals `length`; at most one more,
     ending in `.sigstore` (stored, not interpreted; PKG-NPM-024 is deferred);
   - `dist.integrity` and `dist.shasum`: at least one present; every `sha512` entry of the SRI
     value and the SHA-1 MUST match the computed digests (mismatch: 409, R-STOR-004);
   - the tarball is a gzip-compressed tar. An entry's *install path* is its name without the first
     path component, exactly as npm's extractor computes it: pacote extracts with node-tar's
     `strip: 1`, which removes the first component literally, without normalizing `./` or empty
     components ([pacote](https://github.com/npm/pacote/blob/main/lib/fetcher.js),
     [node-tar](https://github.com/isaacs/node-tar/blob/main/src/unpack.ts)). Every entry name MUST
     be clean: relative, no backslash, no empty, `.` or `..` component (a directory entry may end
     in one `/`); all entries MUST share one first component. Exactly one entry has the
     install path `package.json`; it MUST be a regular file whose `name` and `version` equal the
     manifest's. A path rule violation, a second entry with an install path already seen, or a
     `package.json` that is a symbolic link, a hard link or another non-regular entry answers 400
     `npm_bad_publish`. Why duplicates are refused: node-tar writes entries in archive order,
     serialized per path, and replaces an existing file (it rewrites a reusable regular file in
     place, otherwise unlinks it first) unless its `keep` option is set; pacote does not set
     `keep`. npm therefore installs the *last* of duplicate entries, while a validator may read the
     first and would check a manifest other than the installed one. Verified on 2026-10-28 in the
     sources npm 11.17.0 ships (pacote 21.5.1 `lib/fetcher.js` extract options: `strip: 1`, no
     `keep`, link entries filtered out; node-tar 7.5.16 `unpack.js`). Because pacote skips link
     entries, npm would not install a symbolic-link `package.json` at all. Why unclean names are
     refused rather than normalized: node-tar's `strip` splits the name at `/` and drops the first
     element, so npm installs `./package/package.json` as `package/package.json` inside the
     package, not as its manifest, and a validator that normalized the name would again check a
     different file. pnpm and Yarn use their own extractors (not verified here); refusing every
     duplicate and every unclean name keeps the check independent of any extractor's
     normalization. npm's and Yarn 2+'s packers write clean
     `package/…` names (pacote's `prefix: 'package/'`, Yarn's `ppath.join('package', file)`);
     the publish criteria below confirm it for every gate row;
   - reading stops at `npm.publish.max_unpacked_size` or `npm.publish.max_tar_entries` (413). Both
     bound a streaming pass that counts entries and sums their sizes without keeping content;
     `package.json` and every other entry read into memory stay within the limits of
     [architecture.md](../architecture.md) R-ARCH-028;
   - manifest consistency (`npm.publish.manifest_consistency`, Q-NPM-6): `dependencies`,
     `optionalDependencies`, `peerDependencies`, `peerDependenciesMeta`, `bundleDependencies`,
     `bin`, `os`, `cpu`, `engines` and the scripts `preinstall`, `install`, `postinstall` MUST be
     equal in the request manifest and the tarball's `package.json`. Clients send a normalized
     manifest while the tarball carries the raw file: npm runs `@npmcli/package-json` `fix()` and
     then `prepare()` on a directory, and `prepare()` alone on a tarball, which is how pnpm 10
     publishes (it packs and runs `npm publish <tarball>`); Yarn 1 applies its own normalization
     ([npm publish](https://github.com/npm/cli/blob/latest/lib/commands/publish.js),
     [normalize.js](https://github.com/npm/package-json/blob/main/lib/normalize.js), verified in
     npm 11.17.0 with `@npmcli/package-json` 7.0.5, pnpm 10.34.9 and Yarn 1.22.22). trove therefore
     applies the union of these steps, each idempotent, to **both** sides and compares the results;
     the stored manifest stays the request's (below). The steps that touch the compared fields:
     - `name` trimmed and `version` cleaned as semver's loose `clean()` does (`v1.2.3` becomes
       `1.2.3`), also for the `name` and `version` check above;
     - `bundledDependencies` renamed to `bundleDependencies`; `true` expanded to the names of
       `dependencies`; an object reduced to its keys; `false`, an empty list and absence equal;
       non-string members dropped; a bundled name missing from `dependencies` added there with
       range `*`;
     - `bin` given as a string (key: the name) or an array (keys: base names) turned into an
       object; keys reduced to their base name; targets made relative and cleaned the way npm does
       (backslashes to `/`, `./`, `..` and repeated `/` resolved); non-string or empty entries
       dropped; `directories.bin` expanded from the tarball's entries when `bin` is absent;
     - in `scripts`, non-string entries dropped and a leading `node_modules/.bin/` (also with `./`
       or backslashes) removed; an `install` script `node-gyp rebuild` added when the tarball
       holds a top-level `*.gyp` file, there is neither `install` nor `preinstall` and `gypfile` is
       not `false` (npm), or when it holds a top-level `binding.gyp` and there is no `install`
       (Yarn 1, which ignores `preinstall` and `gypfile`);
     - `dependencies` and `optionalDependencies` given as a string or array turned into objects;
       in `dependencies`, non-string ranges removed and hosted-git shorthands (`github:`,
       `gitlab:`, `bitbucket:`, `sourcehut:`, `user/repo`) expanded to npm's canonical form.
       Optional dependencies are not copied into `dependencies`: `@npmcli/package-json` no longer
       does that;
     - `engines` given as an array of `"<engine> <range>"` strings turned into an object (Yarn 1).

     With `warn` a mismatch is audited and accepted. A false rejection found later is fixed by
     adding the missing step, never by dropping the field from the check. A step may only erase
     differences that change nothing a client installs or runs, apart from the `node-gyp rebuild`
     script above, which builds the package's own `binding.gyp` and is visible in the document.
4. Immutability (R-NPM-008): existing version 409 `npm_version_exists`, tombstone 409
   `npm_version_unpublished`.
5. Commit through R-STOR-005. Inside the commit transaction (`OnCommit` hook): lock or create the
   package rows, re-check immutability, insert `package_versions`, the item and `npm_versions`,
   apply the tag under R-NPM-016, increment `packages.rev_seq`, update `modified_at` and the
   search vector, write the audit event (R-NPM-044); after commit `NOTIFY` for cache eviction
   (INV-12).
6. Answer 201 `{"ok": true, "id": "<name>", "rev": "<rev>"}` once the blob is durable and the
   transaction committed; npm ignores the body.

Stored manifest = the request's version manifest with server-controlled fields replaced:

- removed: `_npmUser`, `_npmOperationalInternal`, `_rev`, `_revisions`, `_attachments`, every
  other `_`-prefixed key except `_id`, `_nodeVersion` and `_npmVersion`, and `dist.tarball`,
  `dist.signatures`, `dist.attestations`;
- set by trove: `_id` (`name@version`), `_npmUser` (`{"name": "<principal name>"}`, no e-mail),
  `dist.integrity`, `dist.shasum`, `dist.fileCount`, `dist.unpackedSize`, `hasInstallScript`
  (`preinstall`, `install` or `postinstall` present, or a top-level `binding.gyp`) and
  `_hasShrinkwrap` (top-level `npm-shrinkwrap.json`). `dist.tarball` is added when rendering;
  `access` is ignored, because permission policies decide visibility.

Concurrent publishes of different versions of one package serialize on the package row and both
succeed.

Configuration (`trove.yaml`):

```yaml
npm:
  publish:
    max_tarball_size: 256MiB       # default (proposed); bounded by storage.upload.max_size
    max_unpacked_size: 2GiB        # default (proposed)
    max_tar_entries: 100000        # default (proposed)
    manifest_consistency: reject   # default (proposed); reject | warn
```

Acceptance criteria:

- `npm publish`, `pnpm publish`, `yarn npm publish` (Yarn 2+) and `yarn publish` (Yarn 1.22), in
  every gate row of R-NPM-045, each publish a scoped and an unscoped package to `npm-local` and
  through `npm-all`; `npm view <pkg>@<version> dist.integrity` equals the integrity the client
  computed.
- A fixture corpus with one package per normalization step above (string `bin` on a scoped name,
  `directories.bin`, `bundledDependencies: true`, a bundled name absent from `dependencies`,
  `version: "v1.0.0"`, a `binding.gyp` without `install` script, a `binding.gyp` with a
  `preinstall` script, a `node_modules/.bin/` script prefix, a `github:` dependency, an
  `optionalDependencies` entry also listed in `dependencies`) publishes without rejection from
  every gate row.
- A request whose `dist.integrity` does not match the attachment stores nothing; one whose
  manifest omits a dependency the tarball's `package.json` declares answers 400
  `npm_manifest_mismatch`.
- A request manifest carrying `_npmUser.trustedPublisher` is stored with `_npmUser` =
  `{"name": "<publisher>"}`.
- Two concurrent publishes of `1.0.1` and `1.0.2` of one package both succeed.
- A tarball with two `package/package.json` entries, the second adding a `postinstall` script,
  answers 400 `npm_bad_publish`; so do one whose `package/package.json` is a symbolic link, one
  whose entries are named `./package/…`, and one holding both `package/package.json` and
  `other/package.json`.

### R-NPM-008 · Version immutability and tombstones
Priority: **MVP** · Covers: PKG-NPM-016

- A name and version once published to a local repository MUST never be accepted again with
  other bytes in that repository: not by a second publish, not after unpublish or a move out, not
  by a raw upload, not by a copy or move into the repository (P1, refused per item,
  [storage.md](../storage.md) R-STOR-019), not for a principal holding `overwrite` (R-AUTHZ-006
  lets formats refuse regardless). Lockfiles pin `integrity` per version.
- Unpublish writes an `npm_tombstones` row with the version's integrity. So does, from P1, a move
  of the version out of a local repository (R-STOR-019): the source repository's hook writes the
  row in the move transaction. Clients of the source repository may have cached the version, so a
  rebuilt version under the same number there would diverge exactly as after an unpublish; moving
  the version back is refused like a republish. A copy writes none, because the version stays.
  The row survives the trash can and permanent removal (R-STOR-022, R-STOR-023). No API, UI or
  `trove admin` operation removes a tombstone, and a republish of identical bytes is refused like
  any other (Q-NPM-8); only the restore below and the deletion of the whole repository remove one.
- Restoring a version from the trash can brings back its original bytes; that is not a reuse. The
  restore removes the tombstone and the version reappears in documents.
- Remote and virtual repositories serve upstream versions as they are; changed upstream digests
  for a cached version are handled by R-REPO-041.

Acceptance criteria:

- A second `npm publish` of `@acme/ui@1.2.0` fails with 409 `npm_version_exists`.
- After `npm unpublish @acme/ui@1.2.0`, publishing `1.2.0` fails with 409
  `npm_version_unpublished`, also after the item is permanently removed from the trash;
  publishing `1.2.1` succeeds. The same holds for a principal with `overwrite`.
- Restoring the unpublished version from the trash makes `npm view @acme/ui@1.2.0 dist.integrity`
  print the original integrity.
- After moving `@acme/ui@1.3.0` from `npm-local` to another local repository (P1), publishing a
  rebuilt `1.3.0` to `npm-local` fails with 409 `npm_version_unpublished`.

### R-NPM-009 · Document revisions and read-for-write
Priority: **MVP** · Covers: PKG-NPM-057

- A local package document carries `_rev` = `<rev_seq>-<first 32 hex characters of
  SHA-256(repository id, package id, rev_seq)>`; clients treat it as opaque. `rev_seq` is the
  package's `packages.rev_seq` ([storage.md](../storage.md), [architecture.md](../architecture.md)
  R-ARCH-009), the one counter the core also reads; npm keeps no counter of its own.
- Every path containing `-rev/{rev}` MUST compare `{rev}` with the current revision inside the
  write transaction and answer 409 `npm_stale_rev` on mismatch, so two read-modify-write clients
  cannot lose each other's changes.
- `GET {name}?write=true` MUST return the full document regardless of `Accept`, read from
  PostgreSQL inside the request, without `_attachments`. On a virtual repository it returns the
  deployment target's own document (R-NPM-029).
- Remote documents carry the upstream's `_rev` if any; virtual documents carry a synthetic `_rev`
  derived from their cache fingerprint. Neither can be written through.

Acceptance criteria:

- `curl '…/npm-local/@acme%2fui?write=true'` with an abbreviated `Accept` header returns
  `Content-Type: application/json` and a `_rev`.
- `PUT …/@acme%2fui/-rev/<old rev>` after a newer publish answers 409.

### R-NPM-010 · Full package document
Priority: **MVP** · Covers: PKG-NPM-025, PKG-NPM-029, FMT-011

- Local package documents are computed from the rows of R-NPM-006; clients never upload one.
- A publish, unpublish, deprecation or dist-tag change MUST be visible on every replica as soon as
  its request has returned 2xx. Each document request reads the package's `packages.rev_seq` (one
  indexed lookup, R-ARCH-012) and renders, or reuses the rendering for, that revision. `NOTIFY`
  evicts early but is not relied on, so a CI job that publishes and immediately installs through
  any replica never sees the old document.
- The full document is served for `Accept: application/json`, a missing `Accept`, and
  `?write=true`:

```json
{
  "_id": "@acme/ui",
  "_rev": "7-3f2a9c0d4e5b6a7c8d9e0f1a2b3c4d5e",
  "name": "@acme/ui",
  "description": "Acme UI kit",
  "dist-tags": { "latest": "1.2.0", "next": "2.0.0-rc.1" },
  "versions": {
    "1.2.0": {
      "name": "@acme/ui", "version": "1.2.0", "_id": "@acme/ui@1.2.0",
      "dependencies": { "react": "^19.0.0" },
      "hasInstallScript": false,
      "_npmUser": { "name": "alice" },
      "dist": {
        "tarball": "https://trove.example.com/api/npm/npm-local/@acme/ui/-/ui-1.2.0.tgz",
        "integrity": "sha512-…", "shasum": "…", "fileCount": 42, "unpackedSize": 123456
      }
    }
  },
  "time": { "created": "2026-09-01T08:00:00.000Z", "modified": "2026-10-28T09:12:44.512Z", "1.2.0": "2026-10-28T09:12:44.512Z" },
  "maintainers": [ { "name": "alice" } ],
  "readme": "# Acme UI …",
  "readmeFilename": "README.md"
}
```

- `versions` and `time` list every version that is not trashed or tombstoned.
- `dist.tarball` is `<registry URL of the serving repository><name>/-/<basename>-<version>.tgz`
  with an unencoded scope separator: the conventional shape that Yarn 2+ recognises (R-NPM-042).
- Top-level `description`, `keywords`, `license`, `homepage`, `repository`, `bugs` and `author`
  come from the version `latest` points to; `readme` and `readmeFilename` from that version's
  manifest or, if absent, from the tarball's first top-level `README*` file, cut at 64 KiB.
  `maintainers` lists the distinct principal names that published the versions present, without
  e-mail; it is informational (ownership is R-AUTHZ-018).
- An unknown package, or one without a live version, answers 404.

Acceptance criteria:

- `npm publish` followed directly by `npm view @acme/ui@<new version> version`, with two replicas
  behind the Service and no session affinity, prints the new version in 100 of 100 runs.
- `npm view @acme/ui readme` shows the README of the version tagged `latest`.
- The full document of a package with 1,000 versions renders in under 200 ms on a miss and is
  served from cache in under 10 ms *(targets, proposed)*.

### R-NPM-011 · Abbreviated document and content negotiation
Priority: **MVP** · Covers: PKG-NPM-026

- When `Accept` gives `application/vnd.npm.install-v1+json` a higher quality value than
  `application/json` (npm, pnpm and Yarn 1 send
  `application/vnd.npm.install-v1+json; q=1.0, application/json; q=0.8, */*`), trove MUST serve
  the abbreviated document with `Content-Type: application/vnd.npm.install-v1+json`; otherwise the
  full document with `Content-Type: application/json`. pnpm decides from `Content-Type` which form
  it received, so label and body must agree.
- Abbreviated document: top-level `name`, `modified`, `dist-tags`, `versions`, and with
  `npm.abbreviated_time` the `time` map (R-NPM-012). Per version: `name`, `version`, `dist`
  (complete, including `signatures` and `attestations`) and, where present, `deprecated`,
  `dependencies`, `optionalDependencies`, `devDependencies`, `bundleDependencies`,
  `peerDependencies`, `peerDependenciesMeta`, `acceptDependencies`, `bin`, `directories`,
  `engines`, `cpu`, `os`, `libc`, `funding`, `_hasShrinkwrap`, `hasInstallScript`
  ([abbreviated format](https://github.com/npm/registry/blob/main/docs/responses/package-metadata.md#abbreviated-metadata-format);
  `libc` is not in npm's list and is ignored by clients that do not read it).
- Remote repositories derive the abbreviated form from the stored full document (R-NPM-024);
  virtual repositories from the merged one.

Configuration (`trove.yaml`):

```yaml
npm:
  abbreviated_time: true  # default (proposed): add the time map to abbreviated documents
```

Acceptance criteria:

- `curl -H 'Accept: application/vnd.npm.install-v1+json; q=1.0, application/json; q=0.8, */*'`
  returns `Content-Type: application/vnd.npm.install-v1+json`, no `readme`, no `_rev`; with
  `Accept: application/json` it returns the full document.
- `npm install`, `pnpm install` and Yarn 1 `yarn install` of a 200-package tree through `npm-all`
  issue only abbreviated document requests (access log).

### R-NPM-012 · Publish times and release-age policies
Priority: **MVP** · Covers: PKG-NPM-027

npm (`before`, `min-release-age`), pnpm (`minimumReleaseAge`, `trustPolicy`) and Yarn 2+
(`npmMinimalAgeGate`) decide on the client, from `time[<version>]`, whether a version is old
enough to install. trove cannot override them; it MUST make them work:

- Full documents of every class MUST contain `time.created`, `time.modified` and an ISO 8601 UTC
  timestamp with milliseconds for every listed version: the commit time of the publish for local
  versions, the upstream's value for remote versions, the winning member's value for virtual
  versions (R-NPM-030). A remote version without an upstream time is listed without one; trove
  never invents upstream times.
- With `npm.abbreviated_time`, abbreviated documents carry the same map, so pnpm with
  `registrySupportsTimeField` (pnpm versions that know the setting;
  [pnpm settings](https://pnpm.io/settings/other#registrysupportstimefield)) applies its check
  without full documents.
- Client setup instructions (R-NPM-041) emit the client's exemption list for the patterns of the
  virtual repository's pin rules and of the manual namespace claims in effect in it (R-NPM-033:
  `global` claims of any npm local repository, `members` claims of its local members; automatic
  claims are exact names and are not listed): internal packages are typically installed minutes
  after their publish. npm `min-release-age-exclude[]=@acme/*`, pnpm
  `minimumReleaseAgeExclude`, Yarn 2+ `npmPreapprovedPackages` where supported.

Acceptance criteria:

- With `min-release-age=1` in `.npmrc`, `npm install @acme/ui@latest` right after a publish does
  not pick the new version; with `min-release-age-exclude[]=@acme/*` added it does.
- pnpm with `minimumReleaseAge: 1440` and `registrySupportsTimeField: true` resolves a 200-package
  tree through `npm-all` with abbreviated requests only and refuses a version published 5 minutes
  earlier.
- For a remote package, `npm view lodash time --json` through trove equals the upstream's map.

### R-NPM-013 · Conditional requests and cache headers
Priority: **MVP** · Covers: PKG-NPM-032

- Package documents MUST carry a weak `ETag` and `Last-Modified` (= `time.modified`) and answer
  `If-None-Match` and `If-Modified-Since` with 304
  ([RFC 9110 §13](https://www.rfc-editor.org/rfc/rfc9110#section-13)). The ETag is derived from
  inputs, so a 304 needs no rendering: local (repository, package, `packages.rev_seq`, variant,
  public base URL, rendering settings); remote (stored document digest, serving address, variant,
  settings, as R-REPO-043 requires); virtual (merge fingerprint, variant).
- Package documents MUST carry `Vary: Accept, Accept-Encoding` and `Cache-Control: private,
  max-age=<npm.document_max_age>, must-revalidate`. They are always `private`: a merged document
  depends on the caller's readable members (R-AUTHZ-009 rule 4), and a shared HTTP cache must
  never hand one caller's view to another.
- Tarballs MUST carry a strong `ETag` (`"sha256:<hex>"`) and `Cache-Control: max-age=31536000,
  immutable`, `public` only if the anonymous principal may read the package there, else `private`.
  Ranges and conditional requests on tarballs follow [storage.md](../storage.md) R-STOR-012.
- Responses MAY be compressed per `Accept-Encoding`.

Configuration (`trove.yaml`):

```yaml
npm:
  document_max_age: 0 # default (proposed), seconds; clients revalidate every time, which a 304 makes cheap
```

Acceptance criteria:

- A second Yarn 4 `yarn install` with a warm Yarn cache receives 304 for every package document.
- After `npm dist-tag add`, a conditional request with the old ETag returns 200 with a new ETag.
- No package document response in the test suite carries `Cache-Control: public`.

### R-NPM-014 · Single-version document
Priority: **P1** · Covers: PKG-NPM-028

`GET {name}/{version-or-tag}` returns one version's manifest exactly as it appears in the full
document of the same repository (same `dist`, same `deprecated`). `{version-or-tag}` is a
canonical version or a dist-tag of that document (merged for virtual repositories); otherwise 404.
Headers as R-NPM-013; permission `read`.

Acceptance criteria:

- `curl …/npm-all/@acme%2fui/latest` returns the manifest of the version `npm view @acme/ui
  dist-tags.latest` prints; `…/@acme%2fui/9.9.9` returns 404 with the npm error body.

### R-NPM-015 · Tarball download
Priority: **MVP** · Covers: PKG-NPM-062

- `{file}` MUST be `<basename>-<version>` with `<basename>` the name without scope; the version is
  what remains after removing `<basename>-` and `.tgz` and MUST be canonical. Otherwise 404.
- Local: the item at the canonical path, streamed per R-STOR-012 (`HEAD`, single ranges,
  conditional requests); trashed or tombstoned versions answer 404; deprecated versions are
  served normally.
- Remote: R-NPM-025. Virtual: resolved by R-REPO-019; the member that serves the tarball is the
  one whose version entry the merged document carries (R-REPO-020, R-NPM-030), so the bytes match
  the advertised `dist.integrity`. npm, pnpm and Yarn abort with `EINTEGRITY` on any mismatch.
- Download redirects to the object store (REPO-019) are P2 and owned by storage.md.

Acceptance criteria:

- `npm ci` with a lockfile whose `resolved` URLs point at `npm-all`, with an empty client cache,
  installs local and remote packages without requesting any package document.
- `curl -sI …/@acme%2fui/-/ui-1.2.0.tgz` and `…/@acme/ui/-/ui-1.2.0.tgz` return 200 with the same
  strong ETag; `…/@acme/ui/-/other-1.2.0.tgz` returns 404.

### R-NPM-016 · Dist-tags and the `latest` rule
Priority: **MVP** · Covers: PKG-NPM-021, PKG-NPM-031

| Method | Path | Request | Response | Permission (R-AUTHZ-007) |
|---|---|---|---|---|
| GET | `-/package/{name}/dist-tags` | — | 200 `{"latest": "1.2.0", "next": "2.0.0-rc.1"}`; 404 unknown | `read` |
| PUT | `-/package/{name}/dist-tags/{tag}` | JSON string, e.g. `"1.2.0"` | 200 `{"ok": true}`; 400 `npm_invalid_tag` or version not live; 404 unknown package | `deploy`, ownership |
| DELETE | `-/package/{name}/dist-tags/{tag}` | — | 200 `{"ok": true}`; 400 for `latest`; 404 unknown tag | `deploy`, ownership |

- Tag names follow R-NPM-007's rule; a tag that is a valid SemVer range (`1.x`, `v1.4`) is rejected,
  matching the clients' own check.
- `latest` cannot be deleted; it can be moved to any live version, older ones included (moving it
  back after a broken release is legitimate).
- A publish applies the tag of its body. **Latest guard** (`npm.latestGuard`): when a publish
  carries `latest` and the new version is lower than the current `latest`, or is a pre-release
  while the current `latest` is not, the version is stored, `latest` stays, and the 201 carries
  `npm-notice: latest stays at <version>; move it with npm dist-tag add`. npm 11 refuses such
  implicit tags on the client
  ([npm source](https://github.com/npm/cli/blob/latest/lib/commands/publish.js)); pnpm and Yarn
  do not, and a maintenance release from an old branch would otherwise silently become `latest`.
- A package with live versions MUST always have `latest`: a first publish under another tag also
  sets `latest`; when the version `latest` points to is unpublished or trashed, `latest` moves to
  the highest remaining non-pre-release version, or the highest version if all are pre-releases;
  other tags pointing to it are removed.
- Remote repositories serve the upstream's tags; writes answer 405. Virtual repositories merge
  (R-NPM-031) and route writes (R-NPM-029).
- Every change increments `packages.rev_seq` and emits an audit event.

Configuration (`npm` section of a local repository):

```yaml
npm:
  latestGuard: true   # default (proposed)
```

Acceptance criteria:

- `npm dist-tag add @acme/ui@1.1.0 latest`, `npm dist-tag ls @acme/ui`, `npm dist-tag rm @acme/ui
  next`; `yarn npm tag add @acme/ui@1.1.0 stable`, `yarn npm tag list @acme/ui`, `yarn npm tag
  remove @acme/ui stable`; `pnpm dist-tag add @acme/ui@1.1.0 beta` behave as specified.
- `npm dist-tag rm @acme/ui latest` fails with 400.
- `yarn npm publish` of `1.1.5` after `2.0.0` leaves `latest` at `2.0.0`; the 201 response
  carries the `npm-notice` header and the `npm.publish` audit event records the guard. Yarn 2+
  prints `npm-notice` only on error responses
  ([npmHttpUtils.ts](https://github.com/yarnpkg/berry/blob/master/packages/plugin-npm/sources/npmHttpUtils.ts)),
  so the test reads the header, not Yarn's output.

### R-NPM-017 · Unpublish
Priority: **MVP** · Covers: PKG-NPM-019

npm's unpublish sequence ([npm unpublish](https://docs.npmjs.com/cli/commands/npm-unpublish),
[libnpmpublish](https://github.com/npm/cli/blob/latest/workspaces/libnpmpublish/lib/unpublish.js))
maps to three operations. Each needs `delete` and ownership (R-AUTHZ-007) and the current `{rev}`
(R-NPM-009):

| Method | Path | Body | Effect | Response |
|---|---|---|---|---|
| PUT | `{name}/-rev/{rev}` | full document without the versions to remove | Each live version absent from `versions` is deleted through [storage.md](../storage.md) R-STOR-018 (to the trash can) and tombstoned | 200 `{"ok": true, "rev": "<new rev>"}`; 400 if `versions` is missing or names a version trove does not hold; 409 stale rev |
| DELETE | `{name}/-/{file}.tgz/-rev/{rev}` | — | No-op for a version already removed by the `PUT` (npm sends it next) | 200; 400 if the version is still live |
| DELETE | `{name}/-rev/{rev}` | — | Every live version deleted and tombstoned; all dist-tags removed | 200; 409 stale rev |

- On the `PUT` path trove applies removals only: dist-tags are recomputed by R-NPM-016, not taken
  from the body, and every other difference is ignored. A body without `versions` (npm's partial
  maintainer update, PKG-NPM-022) answers 400 `npm_bad_publish` with "maintainer lists are not
  supported; ownership is managed in trove".
- A raw `DELETE /files/<npm local>/<tarball path>` (R-STOR-018) unpublishes that version the same
  way (`OnItemsRemoved`).
- Remote repositories answer 405; their cached items are removed through the cache API of
  [repositories-and-proxy.md](../repositories-and-proxy.md). Virtual repositories route to the
  deployment target (R-NPM-029). No time window applies.

Acceptance criteria:

- `npm unpublish @acme/ui@1.0.0` removes exactly that version; `npm view @acme/ui versions` and
  `npm view @acme/ui dist-tags` no longer show it.
- `npm unpublish @acme/ui --force` removes the package; `npm view @acme/ui` answers 404.
- `pnpm unpublish @acme/ui@1.0.1` behaves like npm.
- A principal with `deploy` but not `delete` gets `E403`.

### R-NPM-018 · Deprecate and undeprecate
Priority: **P1** · Covers: PKG-NPM-020

`npm deprecate <pkg>@<range> "<message>"` reads the document with `?write=true` and sends it back
as `PUT {name}` without a `.tgz` attachment, with `versions[v].deprecated` set
([npm deprecate](https://docs.npmjs.com/cli/commands/npm-deprecate)).

- Such a `PUT` is a metadata update. trove applies exactly one kind of change from it:
  `versions[v].deprecated` of live versions (a non-empty string sets the message; an empty string,
  or a missing key on a deprecated version, clears it). Versions absent from the body and all other
  fields are left unchanged.
- If the body carries `_rev`, it MUST equal the current revision (409 otherwise).
- Messages are at most 1,024 characters (400 beyond).
- Permission per R-AUTHZ-007: `deploy` and ownership (Q-NPM-9). Virtual repositories route
  to the deployment target; remote repositories answer 405.
- The message appears in full, abbreviated and single-version documents.

Acceptance criteria:

- `npm deprecate @acme/ui@"<1.2.0" "use 1.2.x"` marks every lower version; `npm install
  @acme/ui@1.1.0` prints the warning; the same command with `""` clears it.
- A crafted metadata `PUT` that also changes `dependencies` leaves them unchanged.

## Identity and login

### R-NPM-019 · whoami and ping
Priority: **MVP** · Covers: PKG-NPM-011, PKG-NPM-058

| Method | Path | Response | Permission |
|---|---|---|---|
| GET | `-/whoami` | 200 `{"username": "<name>"}`, the name rendered per R-AUTH-033 (user name, `sa:<name>`, `ci:<CI issuer name>/<mapping name>`); 401 for anonymous callers | authenticated subject (R-AUTHZ-007) |
| GET | `-/ping` (query `write=true` accepted and ignored) | 200 `{}` | authenticated subject (R-AUTHZ-007) |

Acceptance criteria:

- `npm whoami`, `pnpm whoami` and `yarn npm whoami` against `npm-all` print the principal name;
  without credentials npm fails with `E401`.
- `npm ping` with a valid token succeeds; with a revoked token it fails with `E401`.

### R-NPM-020 · Web login
Priority: **P1** · Covers: PKG-NPM-008, PKG-NPM-058

The flow, its data model and its settings are
[identity-and-authentication.md](../identity-and-authentication.md) R-AUTH-034. The npm side:

- `POST -/v1/login` exists on every npm repository of every class and answers as R-AUTH-034 step 1
  (`loginUrl` under `/ui/cli-login/`, `doneUrl` under `/auth/cli/`, both absolute `https`).
- With web login switched off, the start answers 404, which makes npm and pnpm fall back to the
  legacy flow (R-NPM-021); [npm-profile](https://github.com/npm/npm-profile/blob/main/lib/index.js)
  treats any 4xx or 500 on the start that way.
- Yarn 2+ uses web login only with `--web-login` for registries other than the public ones; the
  same endpoints serve it.

Acceptance criteria:

- `npm login --registry=https://trove.example.com/api/npm/npm-all/ --auth-type=web`, `pnpm login
  --auth-type=web` and `yarn npm login --web-login` (Yarn 4) each complete after approval in the
  browser and write a working token into `~/.npmrc` or `~/.yarnrc.yml`.

### R-NPM-021 · Legacy login and logout
Priority: **P1** · Covers: PKG-NPM-009, PKG-NPM-010

The flows are R-AUTH-035: `PUT -/user/org.couchdb.user:{name}` with an existing PAT or SAT as
password returns that same token (201), never 409, never a new token; `DELETE -/user/token/{token}`
revokes the token when it equals the presented credential and always answers 200. The npm side:

- Both routes exist on every npm repository of every class.
- `GET -/user/org.couchdb.user:{name}` answers 200 `{"_id": "org.couchdb.user:<name>", "name":
  "<name>"}` only for the caller's own rendered name and 404 otherwise, so the endpoint cannot
  enumerate users.
- `yarn npm login` (Yarn 2+ without `--web-login`) and pnpm's legacy login, which adds Basic
  credentials to the body, use the same route.
- The logout path carries a token; its redaction is R-AUTH-001.

Acceptance criteria:

- `yarn npm login` (Yarn 4) with any user name and a PAT as password writes `npmAuthToken`;
  `yarn npm whoami` prints the PAT's owner.
- `npm login --auth-type=legacy` with a wrong password fails with `E401` and the error text
  "use an access token as password".
- `npm logout` removes the token from `~/.npmrc`, and the token answers 401 afterwards.

### R-NPM-022 · Trusted publishing through CI OIDC exchange
Priority: **P1** · Covers: PKG-NPM-013, CIOIDC-018, CIOIDC-030

npm, pnpm and Yarn 4, run inside GitLab CI with a GitLab ID token in `NPM_ID_TOKEN`, post that
token to an exchange endpoint and use the returned token for the rest of the command. Every failure
is silent: the command goes on with whatever credential is configured. Verified on 2026-10-28 in
the published packages:

| Client | Commands that exchange | Where the exchange is posted | Audience check on `NPM_ID_TOKEN` |
|---|---|---|---|
| npm 11.5.1 and later (none in 10.9.9) | `npm publish`; `npm dist-tag` from 11.21.1 and 12.2.1 (not in 11.20.0 or 12.0.0) | **host root**: `new URL('/-/npm/v1/oidc/token/exchange/package/<escaped name>', registry)` ([oidc.js](https://github.com/npm/cli/blob/latest/lib/utils/oidc.js)) | none |
| pnpm 10 | none of its own; it packs and runs `npm publish <tarball>`, so npm's row applies | as npm | as npm |
| pnpm 11 and 12 | `pnpm publish`; `pnpm dist-tag` from 12.12.1 | below the registry URL from 11.28.7 and 12.15.0 (`new URL('-/npm/v1/oidc/token/exchange/package/<escaped name>', registry)`), the host root before | `aud` must equal or contain `npm:<registry host name>`, from 11.28.6 and 12.12.1 |
| Yarn 4 (4.18.1 checked) | `yarn npm publish`, only when neither `npmAuthToken` nor `npmAuthIdent` is configured | below the registry URL: `<registry>/-/npm/v1/oidc/token/exchange/package/<escaped name>`, scope separator as `%2f` | none |

- trove MUST serve `POST /-/npm/v1/oidc/token/exchange/package/{name}` at the host root of the
  public base URL and `POST -/npm/v1/oidc/token/exchange/package/{name}` below the registry URL of
  every npm repository of every class (R-NPM-001; INV-6 lists the host-root prefix, Q-NPM-1). Both
  routes run the same handler and issue the same token; the registry-relative route uses neither
  the repository's class nor its permissions. The ID token arrives as `Authorization: Bearer <ID
  token>`: npm sends the registry's credential to any URL on the registry's host
  ([npm-registry-fetch auth.js](https://github.com/npm/npm-registry-fetch/blob/main/lib/auth.js)),
  pnpm and Yarn set the header themselves. Like `POST /api/v1/auth/oidc/token`, both routes are
  exchange endpoints: the ID token is their input, not a credential of the authentication stage
  ([identity-and-authentication.md](../identity-and-authentication.md) R-AUTH-001 exempts them).
- trove owns the host root of `server.base_url`, which is an origin without a path
  ([operations-and-observability.md](../operations-and-observability.md) R-OPS-005, INV-6). The
  exchange URL npm derives from a registry URL on that host therefore always reaches trove. npm
  posts the ID token whenever `NPM_ID_TOKEN` is set in GitLab CI, so this is what keeps it from
  reaching another service on a shared host.
- Validation and identity mappings are exactly those of the generic exchange
  ([gitlab-ci-oidc.md](../gitlab-ci-oidc.md), INV-10); this is a second entry point into the same
  code, not a second trust path. The audience is checked against the CI issuer's
  `allowed_audiences`, whose default holds the base URL and `npm:<host name of the base URL>`
  ([gitlab-ci-oidc.md](../gitlab-ci-oidc.md)). The second value is the audience pnpm requires on
  `NPM_ID_TOKEN` and the one npm requests itself on GitHub Actions
  (`npm:${new URL(registry).hostname}`); like the base URL, it names this installation and no
  other recipient. The setup instructions use it, so one GitLab job works with npm, pnpm and Yarn
  (R-NPM-041).
- The issued `trv_cit_` token is **package-bound**: its scope (R-AUTHZ-013) carries one grant
  with the package include pattern equal to `{name}` and the actions `read` and `deploy`, so the
  token can publish and tag that one package and nothing else. `{name}` MUST satisfy the rules for
  new names of R-NPM-002 (400 otherwise) and is inserted as a literal pattern, escaped as
  [gitlab-ci-oidc.md](../gitlab-ci-oidc.md) R-CI-014 escapes claim values, so a path such as
  `@acme%2F*` cannot widen the token to a whole scope. If the mapping's grants do not allow
  `deploy` on `{name}` in any npm local repository, the answer is 403 and no token is issued.
- Response 201 `{"token": "trv_cit_…"}`; errors 400 malformed, 401 invalid ID token, 403 no
  matching grant. Lifetime: `npm.trusted_publishing.token_ttl`, bounded by every term of R-CI-016
  (mapping, CI issuer, global ceiling, remaining ID-token lifetime).
- trove MUST NOT set `_npmUser.trustedPublisher` on such publishes while it serves no provenance
  attestations (PKG-NPM-024, deferred): pnpm's trust policy treats that field together with
  provenance as the strongest trust level.
- Clients without a native exchange (npm 10, pnpm 10 on top of npm 10, Yarn 1) publish from GitLab
  CI through the generic exchange as a pipeline step (R-NPM-041).

Configuration (`trove.yaml`):

```yaml
npm:
  trusted_publishing:
    enabled: true     # default (proposed)
    token_ttl: 15m    # default (proposed)
```

Acceptance criteria:

- A GitLab job with `id_tokens: { NPM_ID_TOKEN: { aud: "npm:trove.example.com" } }`, the
  registry set to `https://trove.example.com/api/npm/npm-all/`, no configured token, and a
  mapping that grants `deploy` on `@acme/*` publishes `@acme/ui` with `npm publish` (npm 11 and 12
  gate rows, host-root route), `pnpm publish` (pnpm 11 and 12 rows, registry-relative route) and
  `yarn npm publish` (Yarn 4 row, registry-relative route); the audit log shows the exchange and
  the publish under the CI identity each time.
- The same job publishing `left-pad` fails, and no token was issued.
- The returned token gets 404 or 403 on any package other than `{name}`.
- `POST /-/npm/v1/oidc/token/exchange/package/@acme%2F*`, and the same path below
  `…/api/npm/npm-all/`, answer 400 and issue no token.
- An ID token whose only audience is `https://other.example.com` answers 401 on both routes.

## Remote repositories

### R-NPM-023 · Upstream registries and credentials
Priority: **P1** · Covers: PKG-NPM-040, RMT-006

Remote settings are [repositories-and-proxy.md](../repositories-and-proxy.md) R-REPO-024; the
format catalog entry for npm (MVP, like R-REPO-003) proposes `https://registry.npmjs.org/` as
default upstream, 7,200 s metadata period and 1,800 s negative period, priority members and pins
and claims supported (`pinsAndClaims`), and registers the package document `<name>` (R-NPM-003;
`n` in R-REPO-003) as the package-document path that R-REPO-029 and R-REPO-048 use. Upstream
credentials and extra hosts are the generic `remote.*` members of R-REPO-024; npm defines no
members of its own for them (repositories-and-proxy.md Q-REPO-5). npm-specific guidance:

- For a private npm upstream, `remote.authMode: bearer` with the npm token in `remote.password` is
  the form every npm registry accepts; with `basic`, the public registry requires in
  `remote.username` the real account name that owns the token (R-REPO-033 stores either).
- Credentials go only to the upstream origin and `remote.extraHosts` entries with
  `sendCredentials: true` (R-REPO-033).
- The upstream test probes `-/ping` (R-REPO-024).

Acceptance criteria:

- A remote pointing at a test registry that requires a bearer token installs a private package;
  with a wrong token `npm install` fails with a 502 naming the remote, and the test registry saw
  the credential only on requests to its own host.

### R-NPM-024 · Package document fetch and validation
Priority: **MVP** · Covers: PKG-NPM-038, PKG-NPM-039, RMT-001, RMT-018, RMT-019, RMT-020, RMT-026

The remote request path, freshness, refresh timeout, negative cache, circuit, offline modes,
invalidation and single-flight are R-REPO-025 to R-REPO-031 and R-REPO-045, without npm
exceptions. This requirement fixes what an npm remote fetches and how it is checked.

- For a metadata request of either variant, the remote fetches and stores only the **full**
  document from the upstream (`GET <upstream>/<name with %2f>`, `Accept: application/json`,
  `Accept-Encoding: gzip`, conditional per R-REPO-027) as one cache entry with variant `full`; the
  abbreviated variant is derived from it (Q-NPM-5). `time` and `_npmUser` exist only in the full
  form (the public registry's abbreviated document carries per version only `name`, `version`,
  `dist` and install fields, checked 2026-10-28), release-age and trust policies need them, and two
  independently refreshed representations could disagree about which versions exist.
- A received document MUST pass these checks before it replaces the stored one, otherwise 502
  `npm_invalid_upstream_document` and the previous entry stays: valid JSON within
  `maxMetadataBytes` (R-REPO-040); `name` equals the requested name; every `versions[v].version`
  equals `v` and every `versions[v].name` equals the name; every `dist.tarball` is an absolute
  `http` or `https` URL; every `dist.integrity` parses as SRI.
- A received document whose `time.modified` is older than that of the stored document is a stale
  copy (a lagging CDN edge or mirror). It does not replace the stored document, marks no version
  removed (R-NPM-026), renews the stored entry's freshness as a 304 would, and increments
  `trove_npm_stale_upstream_documents_total`. Without this rule, one stale answer would hide every
  version published after it until the next refresh. The stored value used for the comparison is
  capped at the time trove received that document, so a document dated in the future (a skewed
  upstream clock) cannot block every later refresh. When either document lacks a parseable
  `time.modified`, the rule does not apply and the received document replaces the stored one.
- When a document is stored, the plug-in records per version the facts that tarball serving
  needs, so no tarball request parses a large document: upstream digests, upstream time, and the
  tarball URL in `remote_url_map` when it is not the upstream base plus the canonical path
  (R-REPO-043). The same pass records versions that disappeared (R-NPM-026).
- Upstream 404 for a document enters the negative cache (R-REPO-028).

Data model (sketch; `remote_cache_entries` and `remote_url_map` are repositories-and-proxy.md's):

```sql
CREATE TABLE npm_remote_packages (
  repo_id        bigint      NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
  name           text        NOT NULL,
  doc_sha256     bytea       NOT NULL,            -- digest of the stored upstream document
  upstream_gone  boolean     NOT NULL DEFAULT false,  -- upstream answered 404 after caching (R-NPM-026)
  search_vector  tsvector,                        -- R-NPM-037
  PRIMARY KEY (repo_id, name)
);

CREATE TABLE npm_remote_versions (
  repo_id        bigint      NOT NULL,
  name           text        NOT NULL,
  version        text        NOT NULL,
  integrity      text,                            -- upstream dist.integrity
  shasum         text,                            -- upstream dist.shasum
  published_at   timestamptz,                     -- upstream time[version]
  listed         boolean     NOT NULL DEFAULT true,  -- false once the upstream document drops it
  PRIMARY KEY (repo_id, name, version),
  FOREIGN KEY (repo_id, name) REFERENCES npm_remote_packages (repo_id, name) ON DELETE CASCADE
);
```

Acceptance criteria:

- `npm view react versions` through `npm-remote` causes one upstream request; repeated within the
  period, none; after it, one conditional request answered with 304.
- 50 concurrent `npm view react` requests over two replicas on a cold cache cause one upstream
  request.
- With the upstream unreachable and the document expired, `npm install react` succeeds from the
  stale document and cached tarballs.
- A test upstream whose document `name` differs from the request produces 502 and the previously
  stored document keeps being served.

### R-NPM-025 · Tarball URLs and tarball fetching
Priority: **MVP** · Covers: PKG-NPM-038, PKG-NPM-062, RMT-022

- In every document a remote serves, each `dist.tarball` is rewritten at serving time to the
  conventional URL under the **serving** repository's registry URL,
  `<registry URL><name>/-/<basename>-<v>.tgz`; through a virtual repository the virtual's URL is
  used (R-REPO-043). Clients then fetch every tarball through trove with credentials, and Yarn 2+
  keeps lockfiles registry-agnostic (R-NPM-042). `dist.attestations.url` and every other URL pass
  through unchanged (R-NPM-027).
- A tarball request for a version not yet cached finds the upstream URL and expected digests from
  the records of R-NPM-024. If the document is not stored or does not list the version, the plug-in
  first fetches or refreshes the document; still unknown: 404. This serves lockfile-driven installs
  (`npm ci`), which request tarballs without documents.
- Expected digests handed to the integrity check (R-REPO-041): the `sha512` entries of
  `dist.integrity`, else `dist.shasum` (SHA-1). With the default `verify-if-available` policy a
  mismatch refuses the tarball with 502 `upstream_integrity_mismatch`; `ignore` and `pass-through`
  SHOULD NOT be used on npm remotes, because clients would then fail with `EINTEGRITY` on bytes
  trove had cached for good.
- Hosts other than the upstream origin need `remote.extraHosts` (R-REPO-043, `foreign_host_denied`);
  every connection passes the egress policy (R-REPO-038) and the redirect rules (R-REPO-039).
  Cached tarballs never expire.

Acceptance criteria:

- `npm view lodash@4.17.21 dist.tarball` through `npm-remote` prints
  `https://trove.example.com/api/npm/npm-remote/lodash/-/lodash-4.17.21.tgz`; through `npm-all`
  the `npm-all` URL.
- A test upstream serving a tarball that does not match its integrity makes `npm install` fail
  with 502 and leaves no item; once fixed, a retry succeeds.
- `npm ci` with a lockfile against an empty remote cache installs every package.

### R-NPM-026 · Versions removed upstream
Priority: **MVP** · Covers: trove-only

Public registries remove malicious versions and packages. A cache that keeps serving them after
the upstream removed them preserves exactly what the upstream took down.

- When a refreshed document no longer lists a version, the version is marked `listed = false`;
  when the upstream answers 404 for a stored document, the package is marked `upstream_gone`.
- With `npm.serveRemovedVersions: false` (the default, Q-NPM-2), unlisted versions disappear
  from served documents and their tarballs answer 404 even if cached, also for lockfile-driven
  requests; an `upstream_gone` package answers 404. With `true` they stay served (availability
  over takedown) and are marked in the cache view. A version that the upstream lists again is
  served again (`listed = true`).
- A stale upstream copy (R-NPM-024, older `time.modified`) never marks a version removed.
- A build that needs a version the upstream withdrew legitimately keeps it by publishing the
  tarball to a local repository (R-NPM-036) or by setting `serveRemovedVersions: true` on that
  remote.
- Each transition writes the audit event `npm.remote.version_removed`, so operators can find the
  clients that downloaded a version later removed (download audit is R-AUTHZ-029's).
- The delay between an upstream removal and its effect is bounded by the metadata period plus, for
  virtual repositories, the merged-metadata expiry (R-REPO-020); see H-33.

Configuration (`npm` section of a remote repository):

```yaml
npm:
  serveRemovedVersions: false   # default (proposed)
```

Acceptance criteria:

- The test upstream drops `evil@1.0.1` from its document; after the period, `npm view evil
  versions` through trove no longer lists it and its cached tarball answers 404; the audit log
  has the event. With `serveRemovedVersions: true` the tarball is still served.
- The test upstream then answers one refresh with an older copy of the document (lower
  `time.modified`) that lacks the newest version: trove keeps listing that version, and
  `trove_npm_stale_upstream_documents_total` increments.

### R-NPM-027 · Signatures, attestations and publisher fields
Priority: **MVP** · Covers: PKG-NPM-038

- Remote repositories MUST pass through unchanged `dist.signatures`, `dist.attestations`
  (including `url`), `dist.fileCount`, `dist.unpackedSize`, `_npmUser` (including
  `trustedPublisher` and `approver`), `_hasShrinkwrap`, `hasInstallScript`, `deprecated`, and every
  field the plug-in does not rewrite. pnpm's `trustPolicy` reads `_npmUser.trustedPublisher`,
  `_npmUser.approver` and `dist.attestations.provenance` together with `time`, and refuses a
  version whose trust evidence is weaker than that of earlier versions
  ([pnpm source](https://github.com/pnpm/pnpm/blob/main/pnpm11/resolving/npm-resolver/src/trustChecks.ts));
  dropping the fields would trip that policy, forging them would defeat it.
- Local repositories never take these fields from clients (R-NPM-007).
- Virtual repositories take all of them from the member whose version entry wins (R-NPM-030),
  never combining two members within one version.
- Serving signing keys and attestations is deferred (PKG-NPM-024, PKG-NPM-037); see H-32. Until
  then `dist.attestations.url` passes through unchanged, so `npm audit signatures`, which keeps
  only the URL's path and resolves it against the registry URL (pacote 21.5.1 `lib/registry.js`,
  verified), asks trove's host root and gets 404. When PKG-NPM-024 ships, trove rewrites the URL
  to `<serving registry URL>-/npm/v1/attestations/<name>@<version>`; its path then lies below the
  registry URL, so the request reaches the serving repository with its permissions, and no
  host-root route is needed (Q-NPM-1).

Acceptance criteria:

- For a package published upstream with provenance (for example `pnpm`), the full document through
  `npm-remote` and through `npm-all` has the upstream's `_npmUser` and `dist.attestations` for every
  version.
- `pnpm install` with `trustPolicy: no-downgrade` succeeds through `npm-all` for a tree that passes
  the policy against the public registry.

## Virtual repositories

### R-NPM-028 · npm capabilities of the virtual and priority mechanisms
Priority: **P1** · Covers: REPO-018, RMT-037

The npm plug-in declares support for priority members (R-REPO-022) and for pin rules and
namespace claims (R-REPO-023, catalog member `pinsAndClaims`) in the format catalog (R-REPO-003).
Both take effect in the merge of R-NPM-030 and in tarball resolution exactly as specified there;
npm adds nothing to them.

Acceptance criteria:

- `GET /api/v1/formats` lists npm with `"priorityMembers": true` and `"pinsAndClaims": true`.
- A priority remote that holds `widget` makes `npm-all` serve only its versions of `widget`.

### R-NPM-029 · Writes through the deployment target
Priority: **MVP** · Covers: PKG-NPM-045, REPO-038

Writes through a virtual repository go to `virtual.deployTarget`
([repositories-and-proxy.md](../repositories-and-proxy.md) R-REPO-021; without a target 405
`no_deploy_target`), with the target's permissions, patterns and ownership (R-AUTHZ-009 rule 5).
npm specifics:

- Publish, metadata `PUT`, `-rev` writes, tarball `DELETE` and dist-tag `PUT`/`DELETE` are all
  routed; responses keep the virtual's registry URL in every URL they contain.
- `GET {name}?write=true` on a virtual returns the deployment target's own document, with tarball
  URLs under the virtual's registry URL, so read-modify-write clients (`npm deprecate`, `npm
  unpublish`) never send merged upstream versions back; a package absent from the target answers
  404 there.

Acceptance criteria:

- With `.npmrc` pointing only at `npm-all`, `npm publish`, `npm dist-tag add`, `npm deprecate`
  and `npm unpublish` of `@acme/ui` all act on `npm-local`.
- `npm deprecate lodash@"<5" "x"` through `npm-all` (lodash only upstream) fails with 404 and
  changes nothing.

### R-NPM-030 · Merged package document
Priority: **MVP** · Covers: PKG-NPM-042, REPO-033, REPO-035, REPO-036, REPO-037

Member selection is R-REPO-019 steps 1 to 5: patterns on the canonical path, pin rules and
namespace claims of local members (R-REPO-023, decided without regard to the caller), per-member
`read` (R-AUTHZ-009), priority order. The npm merge over the remaining members:

1. **Versions**: union over the members that know the package. For each version the entry comes
   from the member that tier resolution would pick for that version's tarball (R-REPO-019): local
   members in order, then remote members that hold the tarball in cache, then remote members in
   order. That member supplies the complete manifest, `dist`, `deprecated`, `_npmUser` and
   `time[version]`; `dist.tarball` is rewritten to the virtual's registry URL.
2. **Dist-tags**: R-NPM-031; tags pointing to a version absent from the merge are dropped.
3. **time**: `created` = earliest and `modified` = latest of the contributing members' values; one
   entry per version from its winning member.
4. **Top-level fields** (`description`, `readme`, `keywords`, `license`, `repository`,
   `homepage`, `bugs`, `author`, `maintainers`) come from the member whose version `latest` points
   to.
5. `_id` and `name` are the requested name; `_rev` is synthetic; `_attachments` and `users` are
   omitted.
6. A version present in two members with different `integrity` increments
   `trove_npm_virtual_version_conflicts_total` and is logged with both members, a signal of
   shadowing or of a confusion attempt.
7. npm has no separate latest-version request kind: every document request is a merge over all
   selected members including upstreams (REPO-036), and `GET {name}/latest` reads the merged
   dist-tags.

With automatic namespace claims on (`local.autoClaim`, R-REPO-023 default), a package present in a
claiming local member never mixes
with upstream versions, so the textbook confusion attack (public `acme-internal@99.0.0` next to an
internal `acme-internal@1.0.0`) cannot make the public version `latest`, and pnpm's trust policy
never sees local versions without trust evidence next to upstream versions with it.

Caching is R-REPO-020 (fingerprint of the permission-filtered member list, expiry no later than
the contributing remote entries, `NOTIFY` on member changes). The core's merged-metadata cache
also compares the package's `packages.rev_seq` in every eligible local member, recorded as 0 for a
member that does not hold the package ([architecture.md](../architecture.md) R-ARCH-015, one
`RevSeqs` query per request), so a publish, including the first local publish of a name that so far
came only from a remote member, is visible through the virtual on every replica before the
`NOTIFY` arrives. The check needs no npm code; npm's writes only have to increment the generic
column (R-NPM-006).

Acceptance criteria:

- `npm view lodash versions` through `npm-all` lists the upstream versions.
- `acme-internal@1.0.0` in `npm-local` (published by a `manage` holder, R-REPO-048),
  `acme-internal@99.0.0` in the test upstream: `npm install acme-internal` through `npm-all`
  installs `1.0.0`, for a caller who cannot read `npm-local` it answers 404 (R-REPO-023), and
  neither install causes an upstream request for the name.
- A version present in both members with different bytes installs the local bytes, and the
  conflict counter increments.
- Publishing to `npm-local` and immediately reading through `npm-all` on the other replica shows
  the new version in 100 of 100 runs.

### R-NPM-031 · Dist-tag merge
Priority: **MVP** · Covers: PKG-NPM-043, PKG-NPM-031

- `latest` (`npm.latestStrategy`): `highest-member-latest` takes the highest SemVer among the
  contributing members' own `latest` values, non-pre-release values preferred. A member keeps
  authority over its `latest` (an upstream that moved `latest` back is respected) while a newer
  `latest` elsewhere wins. `newest-published` takes the member `latest` with the newest `time`.
- Other tags (`npm.distTagStrategy`): `member-order` takes the value of the first contributing
  member in resolution order; `highest-semver` the highest among the members' values;
  `newest-published` the newest by `time`.
- `GET -/package/{name}/dist-tags` on a virtual returns the merged tags.

Configuration (`npm` section of a virtual repository):

```yaml
npm:
  latestStrategy: highest-member-latest   # default (proposed); highest-member-latest | newest-published
  distTagStrategy: member-order           # default (proposed); member-order | highest-semver | newest-published
```

Acceptance criteria:

- Two local members with `latest` `1.4.0` and `2.0.0` of one package: the virtual's `latest` is
  `2.0.0`; a third member's `latest` of `3.0.0-rc.1` does not win.
- A `beta` tag present in both members resolves to the first member's value with `member-order`
  and to the higher version with `highest-semver`.

### R-NPM-032 · Virtual metadata cache period
Priority: **MVP** · Covers: PKG-NPM-044, REPO-037

The catalog's virtual metadata cache period is `virtual.mergedMetadataTtlSeconds` (R-REPO-020,
default 7,200 s). For npm it applies to merged package documents, dist-tag documents and
single-version documents; tarballs are never subject to it; local member changes bypass it
(R-NPM-030); and a merged entry never outlives the remote entries it was built from, so the
virtual period does not add to the remote metadata period.

Acceptance criteria:

- With `mergedMetadataTtlSeconds: 600` and a remote metadata period of 60 s, a version published
  to the test upstream is visible through `npm-all` within about 60 s.
- With `mergedMetadataTtlSeconds: 0`, every request re-merges; a publish to `npm-local` is
  visible immediately for any value.

### R-NPM-033 · Pin rules and global namespace claims for npm
Priority: **MVP** · Covers: PKG-NPM-042, REPO-011, RMT-034

Dependency-confusion protection is [repositories-and-proxy.md](../repositories-and-proxy.md)
R-REPO-023 (pin rules and namespace claims with scope `members` or `global`, automatic claims
included) together with R-REPO-005 patterns. This page uses R-REPO-023's terms: a *global
namespace claim* is a claim with scope `global`. npm applies them as follows:

- Pin-rule and claim patterns are npm package-name patterns matched against the normalized name:
  `@acme/*` covers scope `@acme`, `acme-*` unscoped names with that prefix.
- A pinned or claimed name restricts every npm request about the package: documents of both
  variants, single-version documents, tarballs, dist-tags and search hits (R-NPM-037). Audit
  forwarding withholds every name covered by a claim of any scope or a pin rule (R-NPM-038).
- Recommended configuration for an organization that owns `@acme`: a global namespace claim
  `@acme/*` on the npm local repository that publishes it (every virtual repository consults only
  that member for `@acme` names, and with remote refusal no remote ever fetches them, also when
  addressed directly); where several local repositories publish `@acme` packages, a pin rule
  `@acme/*` listing them on each virtual repository.
- The client setup instructions derive release-age exemptions from these patterns (R-NPM-012).

Acceptance criteria:

- Global namespace claim `@acme/*` on `npm-local` (remote refusal on, R-REPO-023), the test
  upstream publishes `@acme/new-thing@1.0.0`: `npm install @acme/new-thing` through `npm-all` and
  through `npm-remote` fails with 404, and the upstream records no request. After
  `@acme/new-thing@0.1.0` is published to `npm-local`, the install through `npm-all` picks
  `0.1.0`.
- `npm search scope:acme` through `npm-all` returns no upstream hit.

### R-NPM-034 · External dependency rewrite
Priority: **P1** · Covers: PKG-NPM-046, REPO-040

Dependencies that are not registry packages make clients fetch from the internet directly. A
virtual npm repository can rewrite some of them so the client fetches through trove.

- The setting names generic remote repositories (one upstream each, INV-5) that may serve external
  dependencies; the allow list is exactly their upstream URLs. A dependency is rewritten only if
  its URL starts with one of them.
- Rewritten forms in P1: `http`/`https` tarball URLs in `dependencies`, `optionalDependencies` and
  `peerDependencies` of served manifests (all variants). The value becomes
  `<virtual registry URL>-/external/<remoteKey>/<rest of the URL after the remote's upstream URL>`,
  which lies under the registry URL, so credentials are sent and npm's same-host exception for URL
  dependencies applies.
- Git dependencies stay unchanged in P1 (Q-NPM-7): trove cannot reproduce a git install without
  running the dependency's `prepare` script, and branch references are mutable.
- `GET -/external/{remoteKey}/{path}` serves the file through that generic remote with its normal
  caching, permissions and egress rules; a remote not named in the setting answers 404.
- The rewrite is part of the merged document and its fingerprint.

Configuration (`npm` section of a virtual repository):

```yaml
npm:
  externalDependencies:
    enabled: false               # default (proposed)
    remotes: [ext-tarballs]      # example; generic remote repositories
```

Acceptance criteria:

- A package depending on `"widget": "https://downloads.example.org/widget-1.0.0.tgz"`, with
  generic remote `ext-tarballs` → `https://downloads.example.org/`, installs through `npm-all`;
  the lockfile records the trove URL; the second install is served from cache.
- A dependency on another host stays unchanged.

## Search, audit and maintenance

### R-NPM-035 · Recalculate npm metadata
Priority: **P1** · Covers: PKG-NPM-030, FMT-012, REPO-009

`POST /api/v1/repositories/{key}/reindex` ([repositories-and-proxy.md](../repositories-and-proxy.md)
R-REPO-016) on an npm local repository runs the npm rebuild as a job: for every live version it
re-reads the tarball from the blob store, recomputes `file_count`, `unpacked_size`,
`has_install_script`, `has_shrinkwrap` and the readme source, re-checks the dist-tag invariants of
R-NPM-016, rebuilds the search vector, and increments `packages.rev_seq` of every changed package.
It never changes a stored integrity value; a tarball whose bytes no longer match it is reported,
and blob damage is handled by storage.md's integrity checks. On a virtual repository the reindex
drops the merged cache (R-REPO-016); on a remote it answers 405.

Acceptance criteria:

- After the `file_count` column of all versions is cleared in a test database, a reindex restores
  them and `npm view @acme/ui dist.fileCount` is correct again.

### R-NPM-036 · Deploy a tarball without the npm client
Priority: **P1** · Covers: PKG-NPM-017, PKG-NPM-018, ART-006

- From P1, `PUT /files/<npm local>/<path>` with a `.tgz` body (also used by the UI deploy dialog)
  is a publish: name and version come from the tarball's `package.json`, which is also the
  manifest, and R-NPM-007 (without the manifest comparison), R-NPM-008 and R-NPM-016 apply. The
  tag is `latest` unless the query parameter `distTag` names another. Until then the plug-in
  refuses raw deploys with 409, as [storage.md](../storage.md) R-STOR-007 describes.
- `<path>` MUST equal the canonical path derived from the tarball; otherwise 400 naming the
  canonical path. This is PKG-NPM-018's path enforcement, always on.
- Any other file type in an npm repository answers 400. Virtual repositories route to the
  deployment target; remote repositories answer 405. Permission as publish.

Acceptance criteria:

- `curl -T ui-1.3.0.tgz -H "Authorization: Bearer ${TROVE_TOKEN}"
  https://trove.example.com/files/npm-local/@acme/ui/-/ui-1.3.0.tgz` answers 201 and `npm
  install @acme/ui@1.3.0` installs it; uploading the same file to `…/npm-local/misc/ui.tgz` answers
  400 naming `@acme/ui/-/ui-1.3.0.tgz`.

### R-NPM-037 · Search
Priority: **P1** · Covers: PKG-NPM-048, SRCH-050

`GET -/v1/search?text=<query>&size=<n>&from=<n>` (`npm search`, `pnpm search`):

```json
{
  "objects": [
    {
      "package": {
        "name": "@acme/ui", "scope": "acme", "version": "1.2.0", "description": "Acme UI kit",
        "keywords": ["ui"], "date": "2026-10-28T09:12:44.512Z",
        "links": { "npm": "https://trove.example.com/ui/packages/npm-all/@acme/ui" },
        "publisher": { "username": "alice" }, "maintainers": [ { "username": "alice" } ]
      },
      "score": { "final": 1.0, "detail": { "quality": 1.0, "popularity": 1.0, "maintenance": 1.0 } },
      "searchScore": 1.0
    }
  ],
  "total": 1,
  "time": "2026-10-28T10:00:00.000Z"
}
```

- `size` defaults to 20, maximum 250; `from` defaults to 0; `quality`, `popularity` and
  `maintenance` are accepted and ignored.
- Qualifiers `scope:<scope>` and `keywords:<k1,k2>` MUST be honoured; other qualifiers are free
  text.
- Index: PostgreSQL full-text over name, description and keywords of each package's `latest`
  version (`search_vector` of R-NPM-006 and R-NPM-024), updated inside the publish, unpublish and
  dist-tag transactions, so a package is searchable as soon as its publish returns. Remote
  repositories search their stored documents only and never send the query upstream. Virtual
  repositories search the members selected as in R-NPM-030, deduplicate by name in member order,
  and rank by text relevance, returned as score.
- Permission: `read`, results filtered per name (R-AUTHZ-007).

Acceptance criteria:

- `npm search acme --registry https://trove.example.com/api/npm/npm-all/` lists `@acme/ui`
  immediately after its publish; `npm search scope:acme` lists only `@acme` packages; a caller
  without `read` on `npm-local` gets no hit from it; the test upstream receives no search request.

### R-NPM-038 · Security audit endpoints
Priority: **P1** · Covers: PKG-NPM-049, PKG-NPM-059

`POST -/npm/v1/security/advisories/bulk` (npm 7+ with a gzip body, pnpm uncompressed, `yarn npm
audit`):

- Mode `forward` (remote repositories, the default, Q-NPM-3): trove decompresses the body
  (`npm.audit.maxBodySize` compressed, ten times that decompressed, 413 beyond) and forwards to
  the provider (default: the remote's upstream URL), with the remote's upstream credentials and
  `npm.audit.timeout`, through the egress rules of R-REPO-038, **only the names that the
  forwarding remote already holds a stored upstream document for** (an `npm_remote_packages` row,
  R-NPM-024, also one marked `upstream_gone`, since withdrawn malware carries advisories; one
  query for the whole body). Of those it also withholds
  every name that exists in any npm local repository of the installation, is covered by a
  namespace claim of any scope of any npm local repository, or matches a pin rule of the
  requesting virtual repository (R-REPO-023). A name the remote has fetched is one the upstream
  has already seen from this installation, so forwarding discloses which public packages a
  project combines but no name the upstream did not already know; a name it never fetched (a git
  or file dependency, a package installed from elsewhere, an internal name nobody claimed) is
  withheld. A `providerURL` other than the upstream learns the same set of names.
- When names were withheld, the response carries `npm-notice: trove audited <n> of <m>
  packages; <k> internal or not yet proxied names were not sent to the advisory provider`, and
  `trove_npm_audit_names_withheld_total{reason}` counts them by reason (`not_proxied`, `local`,
  `claimed`, `pinned`). Withheld names are absent from the answer, which clients read as "no
  advisories"; the notice is what tells the user the audit was partial. When every name is
  withheld, trove answers `{}` without contacting the provider.
- The provider's 200 body is returned unchanged; pnpm accepts only an object keyed by package
  name whose advisories carry a numeric `id` and a string `vulnerable_versions`, which the public
  registry's answer satisfies. Provider failure: 502.
- A virtual repository forwards through its first remote member in resolution order whose mode is
  `forward`.
- Mode `disabled`, every local repository, and a virtual repository without a forwarding member
  answer **501** `npm_audit_unavailable` with "security audit is not available on repository
  <key>; it is disabled or no upstream provides advisories". Why 501 and not 404: the repository
  and the endpoint exist, and "not found" sends users looking for a wrong registry URL; 501 is the
  HTTP status for a function the server does not support; npm never retries a `POST`; `npm install`
  logs the audit failure and completes; `npm audit` and `pnpm audit` print status and text.
- npm 6's `-/npm/v1/security/audits/quick` and `-/npm/v1/security/audits` answer 501.
- Permission: `read` on the repository and, for a virtual, on the forwarding member.

Configuration (`npm` section of a remote repository):

```yaml
npm:
  audit:
    mode: forward          # default (proposed); forward | disabled
    providerURL: null      # default (proposed): the remote's upstream URL
    timeout: 30s           # default (proposed)
    maxBodySize: 10MiB     # default (proposed)
```

Acceptance criteria:

- After `npm install` through `npm-all`, `npm audit` reports the advisories the public registry
  reports for the same lockfile; `pnpm audit` and `yarn npm audit` likewise.
- The test provider's request log never contains `@acme/ui` or any other name present in
  `npm-local`, nor a name covered by a claim, nor a name `npm-remote` never fetched.
- A lockfile with one public package that `npm-remote` never fetched: `npm audit` prints the
  `npm-notice` naming 1 withheld name, and the provider does not see that name.
- `npm audit --registry …/npm-local/` fails with 501 and the error text; `npm install` against the
  same registry succeeds.
- A small gzip body that inflates beyond the limit answers 413.

### R-NPM-039 · Build-tool integration surface
Priority: **P1** · Covers: PKG-NPM-055

trove ships no wrapper around npm, pnpm or Yarn. Build tooling uses the native clients plus one
read-only lookup for per-dependency checksums:

| Method | Path | Response | Permission |
|---|---|---|---|
| GET | `/api/v1/npm/{repoKey}/packages/{name}/versions/{version}` | 200 `{"repository": "npm-local", "name": "@acme/ui", "version": "1.2.0", "integrity": "sha512-…", "shasum": "…", "sha256": "…", "size": 48213, "publishedAt": "…", "publishedBy": "alice"}`; for a virtual, `repository` is the member whose entry wins | `read` |

The snippets of R-NPM-041 are available through the client setup API of
[api-and-ui.md](../api-and-ui.md). Recording build metadata itself belongs to the build-metadata
capability, which has no spec page yet.

Acceptance criteria:

- For every entry of a `package-lock.json` resolved through `npm-all`, the lookup returns a SHA-512
  equal to the lockfile's `integrity`.

## Clients

### R-NPM-040 · Client compatibility: pnpm and Yarn
Priority: **MVP** · Covers: PKG-NPM-051, PKG-NPM-052

pnpm needs, beyond the shared protocol: the abbreviated document labelled by `Content-Type`
(R-NPM-011); `time` in abbreviated documents (R-NPM-012); `_npmUser` and `dist.attestations`
passed through (R-NPM-027) and never mixed across members (R-NPM-030); legacy login with Basic
credentials beside the body (R-NPM-021); the uncompressed audit body and strict audit response
(R-NPM-038). Yarn 1 reads `.npmrc`, requests abbreviated documents and sends credentials only with
`always-auth=true` or for scoped packages (R-NPM-004); `yarn publish` sends npm's publish request.

Acceptance criteria:

- Every pnpm gate row of R-NPM-045: `pnpm install` of a workspace with 3 internal and 300 public
  packages through `npm-all` succeeds with an empty store and again with a warm store and
  `--frozen-lockfile`; `pnpm publish`, `pnpm dist-tag add`, `pnpm unpublish`, `pnpm whoami`,
  `pnpm search` and `pnpm audit` behave as specified.
- Yarn 1.22: `yarn install` with `always-auth=true` against a repository without anonymous read
  succeeds for scoped and unscoped packages; `yarn publish` publishes.

### R-NPM-041 · Client setup instructions
Priority: **MVP** · Covers: PKG-NPM-050, PKG-NPM-054, UI-024, FMT-006

The client setup dialog and its API belong to [api-and-ui.md](../api-and-ui.md); this requirement
fixes the npm content. Snippets use the selected repository's registry URL with a trailing slash,
the keyed credential form only, the public base URL, and a token newly created in the dialog or a
placeholder.

<details>
<summary>Snippets per client</summary>

npm (`.npmrc` in the project, `~/.npmrc`, or `%USERPROFILE%\.npmrc` on Windows):

```ini
registry=https://trove.example.com/api/npm/npm-all/
//trove.example.com/api/npm/npm-all/:_authToken=${TROVE_TOKEN}
; only when pin rules or manual namespace claims apply (R-NPM-012):
min-release-age-exclude[]=@acme/*
```

npm interactive and scoped variant:

```shell
npm login --registry=https://trove.example.com/api/npm/npm-all/ --auth-type=web
npm login --registry=https://trove.example.com/api/npm/npm-acme/ --auth-type=web --scope=@acme
npm install lodash
npm publish
```

pnpm: the same `.npmrc` lines; in `pnpm-workspace.yaml`, for pnpm versions that know the settings:

```yaml
registrySupportsTimeField: true
minimumReleaseAgeExclude:
  - "@acme/*"
```

Yarn 2 and later (`.yarnrc.yml`):

```yaml
npmRegistryServer: "https://trove.example.com/api/npm/npm-all/"
npmAlwaysAuth: true          # only when the repository is not anonymously readable
npmAuthToken: "${TROVE_TOKEN}"
```

Yarn 2 and later, a scope bound to another repository:

```yaml
npmScopes:
  acme:
    npmRegistryServer: "https://trove.example.com/api/npm/npm-acme/"
    npmAuthToken: "${TROVE_TOKEN}"
```

Yarn 1: the npm `.npmrc` lines plus `always-auth=true` when the repository is not anonymously
readable.

Publishing target in `package.json`:

```json
{ "publishConfig": { "registry": "https://trove.example.com/api/npm/npm-all/" } }
```

GitLab CI with trusted publishing (R-NPM-022, P1), for npm 11.5.1 and later (the `node:lts` image
bundles npm 11), pnpm 11 and 12, and Yarn 4. The audience `npm:<host>` is the one pnpm requires;
npm and Yarn accept any:

```yaml
publish:
  image: node:lts
  id_tokens:
    NPM_ID_TOKEN:
      aud: "npm:trove.example.com"
  script:
    - npm config set registry https://trove.example.com/api/npm/npm-all/
    - npm publish            # or: pnpm publish; Yarn 4: yarn npm publish with npmRegistryServer set
```

GitLab CI for clients without a native exchange (npm 10, pnpm 10 on top of npm 10, Yarn 1): the
generic exchange of [gitlab-ci-oidc.md](../gitlab-ci-oidc.md) R-CI-007 as a pipeline step, with the
ID token in `TROVE_ID_TOKEN`, so a newer npm in the same job does not also exchange it:

```yaml
publish:
  image: node:22
  id_tokens:
    TROVE_ID_TOKEN:
      aud: "https://trove.example.com"
  script:
    - |
      export TROVE_TOKEN="$(printf '%s' "$TROVE_ID_TOKEN" | curl -sSf \
        --data-urlencode 'grant_type=urn:ietf:params:oauth:grant-type:token-exchange' \
        --data-urlencode 'subject_token_type=urn:ietf:params:oauth:token-type:id_token' \
        --data-urlencode 'subject_token@-' \
        https://trove.example.com/api/v1/auth/oidc/token \
        | node -e 'process.stdout.write(JSON.parse(require("fs").readFileSync(0, "utf8")).access_token)')"
      unset TROVE_ID_TOKEN
    - npm config set registry https://trove.example.com/api/npm/npm-all/
    # npm expands ${TROVE_TOKEN} at run time; the token itself is never written to disk.
    - npm config set //trove.example.com/api/npm/npm-all/:_authToken '${TROVE_TOKEN}'
    - npm publish
    - printf 'Authorization: Bearer %s\n' "$TROVE_TOKEN" | curl -sS -X DELETE -H @- https://trove.example.com/api/v1/tokens/current
```

</details>

- Snippets MUST NOT append a token to the `registry=` value and MUST NOT emit an unkeyed `_auth`
  or `_authToken`.
- The package details screen shows `npm install <name>@<version>`, `pnpm add <name>@<version>` and
  `yarn add <name>@<version>`.

Acceptance criteria:

- Each snippet, with the placeholder replaced by a valid token, makes its client (matrix of
  R-NPM-045) install `lodash` and `@acme/ui` and publish a test package through `npm-all`.
- A lint test over the generated snippets fails if any contains `registry=…:_authToken` or an
  unkeyed credential line.

### R-NPM-042 · Yarn Berry tarball URLs and lockfiles
Priority: **MVP** · Covers: PKG-NPM-053

- Yarn 2+ reads `.yarnrc.yml`, requests **full** documents and revalidates them with
  `If-None-Match` and `If-Modified-Since` (R-NPM-013), so full-document rendering and 304 handling
  are on its hot path.
- Yarn 2+ treats `dist.tarball` as conventional only if it equals
  `<registry>/<name>/-/<basename>-<version>.tgz` (scope separator encoded or not), and then writes
  a registry-agnostic lockfile; any other URL is pinned verbatim. trove's tarball URLs are
  conventional whenever `npmRegistryServer` uses the public base URL, which the setup instructions
  guarantee.
- Yarn 2+ requests scoped tarballs with `%2f` and retries with `/`; both work (R-NPM-002).
- `yarn npm login` uses the legacy flow unless `--web-login` is given (R-NPM-021, R-NPM-020);
  `yarn npm tag add|remove|list` uses the dist-tag endpoints; `yarn npm audit` the bulk audit
  endpoint.

Acceptance criteria (Yarn 4):

- `yarn install` through `npm-all` produces a `yarn.lock` that contains no trove URL; switching
  `npmRegistryServer` to `npm-remote` and running `yarn install --immutable` succeeds.
- `yarn npm publish`, `yarn npm tag add`, `yarn npm login`, `yarn npm whoami` and `yarn npm audit`
  behave as specified.

## Operations and verification

### R-NPM-043 · Unimplemented npm endpoints answer predictably
Priority: **MVP** · Covers: PKG-NPM-058, PKG-NPM-060

Every npm endpoint listed as deferred or out of scope (see "Deferred") MUST answer as R-NPM-001
prescribes (404 for `GET`/`PUT`/`DELETE`, 501 for `POST`) with the npm error body, so clients fail
fast with a clear message: `npm token list` and `npm profile get` report the operation as not
supported, `npm owner add` fails on the partial `PUT` (R-NPM-017), `npm publish --stage` fails
with 501, and `npm publish --provenance` needs `--access public` because the visibility lookup
answers 404.

Acceptance criteria:

- `npm token list`, `npm profile get`, `npm owner ls @acme/ui`, `npm org ls acme`, `npm access get
  status @acme/ui` against `npm-all` fail within one second each, without retries in npm's
  verbose log.

### R-NPM-044 · npm audit events and metrics
Priority: **MVP** · Covers: trove-only

Audit events ([operations-and-observability.md](../operations-and-observability.md), INV-16), with
actor, repository, package, version or tag, outcome and source address:

| Event | When |
|---|---|
| `npm.publish` | R-NPM-007, R-NPM-036; with integrity and whether the latest guard applied |
| `npm.unpublish` | R-NPM-017, per version |
| `npm.dist_tag.set`, `npm.dist_tag.delete` | R-NPM-016 |
| `npm.deprecate` | R-NPM-018, per version and message change |
| `npm.publish.manifest_mismatch` | R-NPM-007, in both modes |
| `npm.trusted_publishing.exchange` | R-NPM-022, with the matched identity mapping |
| `npm.remote.version_removed` | R-NPM-026 |

Login, logout and token events are R-AUTH-041's; integrity and egress events of remote fetches are
repositories-and-proxy.md's.

Metrics (prefix `trove_`, INV-16): `trove_npm_requests_total{class,operation,status}`,
`trove_npm_request_duration_seconds{class,operation}`,
`trove_npm_document_render_seconds{class,variant}`,
`trove_npm_virtual_version_conflicts_total{repository}`,
`trove_npm_removed_upstream_versions_total{repository}`,
`trove_npm_stale_upstream_documents_total{repository}`,
`trove_npm_audit_names_withheld_total{repository,reason}` (R-NPM-038). Access logs record
`npm-command` and
`npm-session`.

Acceptance criteria:

- Each acceptance test that performs one of the operations above finds exactly one matching audit
  event.

### R-NPM-045 · Client acceptance matrix
Priority: **MVP** · Covers: FMT-002, FMT-003

The npm format is accepted when the acceptance criteria of this page pass end to end with real
clients against two replicas behind the Helm chart's Service and Ingress, with PostgreSQL, an
S3-compatible store and a deterministic test upstream registry.

The gate rows follow a rule, so the gate tracks what users run (Q-NPM-4):

- **npm**: every major that is bundled with a Node.js release line in active or maintenance LTS,
  plus the major npm's `latest` dist-tag points to.
- **pnpm**: every major whose newest release is at most 6 months old.
- **Yarn Berry**: the major `@yarnpkg/cli-dist`'s `latest` points to, plus the previous major
  while its newest release is at most 6 months old.
- **Yarn Classic**: 1.22.x, always (INV-14).

Rows on 2026-10-28, verified against the public registry's dist-tags and publish times, the
Node.js release index (`https://nodejs.org/dist/index.json`, bundled npm per release) and the
[Node.js release schedule](https://github.com/nodejs/Release/blob/main/schedule.json) (22 in
maintenance until 2027-04-30, 24 in maintenance, 26 active LTS from 2026-10-28):

| Client | Version under test | Why it is a row | Role |
|---|---|---|---|
| npm | 10.x (`10.9.9`) | Bundled with Node.js 22 (maintenance LTS) | gate |
| npm | 11.x (`11.21.1`) | Bundled with Node.js 24 (`11.19.1`) and 26 (`11.20.0`) | gate |
| npm | 12.x (`12.2.1`) | `latest` | gate |
| pnpm | 10.x (`10.34.9`, released 2026-10-25) | Maintained | gate |
| pnpm | 11.x (`11.28.9`, released 2026-10-27) | Maintained | gate |
| pnpm | 12.x (`12.16.1`, `latest`) | Maintained | gate |
| Yarn Berry | 4.x (`4.18.1`, `latest`) | `latest`; Yarn 3's newest release (`3.8.7`, 2024-12-04) is older than 6 months | gate |
| Yarn Classic | 1.22.x (`1.22.22`) | INV-14 | gate |

- Each row pins one exact version once in the test harness; local runs and CI read the same pin.
  A scheduled job raises the pins within each major and re-evaluates the rule at every release;
  a major that leaves the rule stays as a non-blocking row for one more release, then is removed.
- The test upstream is a stub registry with recorded documents and tarballs and switches for the
  failure cases of R-NPM-024 to R-NPM-026 and R-NPM-033; a nightly job runs the read-only subset
  against `https://registry.npmjs.org`.
- Scenario list per client: configure from the R-NPM-041 snippets; install scoped and unscoped,
  local and remote packages through `npm-all`; publish; dist-tag add, list, remove; deprecate and
  unpublish (npm, pnpm); whoami; login (web where supported, legacy otherwise); audit; search
  (npm, pnpm); lockfile reinstall with an empty client cache.
- A scenario that depends on a client feature a row's version lacks (for example npm's
  `min-release-age`, or the native trusted-publishing exchange, which npm 10 and Yarn 1 lack and
  which the npm 10, pnpm 10 and Yarn 1 rows therefore test through the generic-exchange snippet of
  R-NPM-041) is marked not applicable for that row in the harness, with the reason; it is never
  silently skipped.

Acceptance criteria:

- The CI job `e2e-npm` runs the scenario list for every row and fails the build on any failure.

## Deferred

| Catalog ID | Priority | Note |
|---|---|---|
| PKG-NPM-012 | P2 | One-time-password challenge on writes. Users sign in through OIDC, where the identity provider enforces a second factor; trove never emits `otp` (R-NPM-004). |
| PKG-NPM-014 | P2 | `npm token` and `npm profile` endpoints; 404 until then (R-NPM-043); tokens are managed through trove's API and UI. |
| PKG-NPM-022 | P2 | `npm owner` maintainer changes; the partial `PUT` is rejected (R-NPM-017); ownership is R-AUTHZ-018/019. |
| PKG-NPM-023 | Later | Staged publishing (`npm publish --stage`); the `POST` answers 501. |
| PKG-NPM-024 | P2 | Provenance bundles: stored on publish (R-NPM-006), served later below the registry URL at `-/npm/v1/attestations/<name>@<version>`, with `dist.attestations.url` rewritten to it (R-NPM-027, Q-NPM-1, H-32); 404 until then. |
| PKG-NPM-035 | P2 | Strict layout setting; trove stores only canonical paths, so it is permanently on (R-NPM-006). |
| PKG-NPM-036 | P2 | Strict-layout conversion; nothing to convert. Relevant only to an import from another system. |
| PKG-NPM-037 | P2 | Registry signatures and the keys endpoint for `npm audit signatures`, with signing keys on local (REPO-029) and virtual (REPO-042) repositories. |
| PKG-NPM-041 | P2 | FHIR package registry as upstream, with its rule against mixing it with ordinary npm members. |
| PKG-NPM-061 | Out of scope | Public-registry account endpoints (organizations, teams, access, visibility, collaborators, hooks, stars); 404. |

## Security notes

- **Manifest confusion.** Clients install the dependencies the registry document lists, not those
  inside the tarball. Rejecting publishes whose manifest disagrees with the tarball on dependency,
  binary and install-script fields (R-NPM-007) keeps document and tarball in step.
- **Forged trust evidence.** `_npmUser`, `dist.signatures` and `dist.attestations` are set only by
  trove or an upstream, never taken from a publishing client (R-NPM-007, R-NPM-027); pnpm's trust
  policy relies on them.
- **Per-caller documents.** Merged documents depend on the caller's readable members; documents
  are always `Cache-Control: private` (R-NPM-013) so no shared HTTP cache hands one caller's view
  to another, in addition to R-AUTHZ-009 rule 4.
- **Takedown propagation.** Versions an upstream removes stop being served from cache by default
  (R-NPM-026), so a pull-through cache does not keep distributing malware the upstream withdrew.
- **Audit forwarding** sends a dependency name to the provider only if the forwarding remote has
  already fetched it from its upstream and no local repository, claim or pin rule marks it
  internal (R-NPM-038); see H-34.
- **ID tokens stay on trove's host.** trove owns the host root of its base URL (R-OPS-005 refuses a
  base URL with a path), so the host-root exchange URL npm derives from the registry URL always
  reaches trove and never another service on a shared host (R-NPM-022).
- **Bounded parsing.** Publish bodies, tarball scans and gzip audit bodies are bounded before they
  are parsed (R-NPM-007, R-NPM-038), and upstream documents by R-REPO-040, so one request cannot
  exhaust a replica's memory or spool.
- **Encoded names.** Names are decoded once and validated before any lookup (R-NPM-002); storage is
  content-addressed, so a name never becomes a file-system path.

## Decisions

Each question below was decided by the development agent from the listed options; the owner reviews the choices at the walkthrough.

### Q-NPM-1 · Host-root routes next to INV-6

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Verified on 2026-10-28 in the published client packages. npm (11.5.1 and later) posts the
trusted-publishing exchange to the host root of the registry URL:
`new URL('/-/npm/v1/oidc/token/exchange/package/…', registry)`
([oidc.js](https://github.com/npm/cli/blob/latest/lib/utils/oidc.js)). Yarn 4.18.1, pnpm 11.28.7
and later, and pnpm 12.15.0 and later post the same exchange *below* the registry URL. pnpm 11.28.6
and 12.12.1 and later use `NPM_ID_TOKEN` only when its `aud` is `npm:<registry host name>`. For
attestations, pacote keeps only the path of `dist.attestations.url` and resolves it against the
registry URL (pacote 21.5.1 `lib/registry.js`). A URL whose path lies below the registry URL
therefore reaches the repository, and only the upstream's unchanged URL points at the host root.
INV-6 and other pages already assume that trove owns its host root: the session cookie is
`__Host-trv_session` with `Path=/` (Q-AUTH-2), `GET /{$}` redirects to `/ui/` (R-ARCH-005), the
chart routes the whole host (R-HA-037), and `/v2/` is reserved for OCI (Q-HA-4).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Two host-root prefixes | `/-/npm/v1/oidc/token/exchange/package/` and `/-/npm/v1/attestations/` at the host root, nothing below the registry URL | npm's native GitLab flow works; neither route needs a repository key | Yarn 4 and current pnpm post the exchange below the registry URL and fail silently; a host-root attestation route would have to guess the repository and its permissions from `name@version` |
| (b) No host-root routes | GitLab jobs call `POST /api/v1/auth/oidc/token` (R-CI-007) and write the token into `.npmrc` | INV-6 unchanged; works with every client version | A native client feature of npm, pnpm and Yarn becomes pipeline boilerplate in every project |
| (c) Host-root prefixes plus a path-prefix fallback | (a); when `server.base_url` has a path, the setup instructions emit (b) with the ID token in `TROVE_ID_TOKEN` | Covers an installation below a path prefix of a shared host | That deployment mode exists nowhere else in the spec, and is unsafe with the decided session cookie: `__Host-trv_session` with `Path=/` would go to every service on the shared host. A second snippet, a startup warning and a test for an unsupported mode; Yarn and pnpm still fail |
| (d) Every entry point the clients use | The exchange at the host root (npm) and below every registry URL (Yarn, pnpm), one handler. trove owns its host root: `server.base_url` is an origin without a path (R-OPS-005). Attestations are served later below the registry URL, through a rewritten `dist.attestations.url`. The generic exchange stays as the snippet for clients without a native exchange | npm, pnpm and Yarn work natively; one host-root prefix instead of two; attestations keep their repository context; the ID-token leak to a co-hosted service cannot arise from a valid configuration | Two routes for one handler; a base URL with a path is refused at startup instead of tolerated |

**Choice:** (d) — serve the exchange at npm's host-root path and below every registry URL, make
host-root ownership explicit through `server.base_url`, and serve attestations later below the
registry URL instead of reserving a host-root prefix for them.

**Rationale:** The clients fix the paths. Verified in the published packages, they disagree: npm
uses the host root, Yarn 4 and current pnpm use the registry URL. Only (d) lets all three INV-14
clients use their native exchange; (b) works everywhere but gives that feature up for all of them.
(a) and (c) leave Yarn and pnpm posting the ID token to an unimplemented route and publishing
without credentials. (c) also builds a fallback for a deployment mode that INV-6, the
`__Host-` session cookie, `/v2/` and the chart all rule out. Its security worry, npm sending the ID
token to a co-hosted service, is closed more simply and for every client by refusing a base URL
with a path. The host-root attestation prefix is dropped, and (b)'s former "Against" claim that
attestations can never be served without it does not hold: pacote resolves only the path of
`dist.attestations.url`, so a URL rewritten under the registry URL is fetched there, with the
repository known for permissions and upstream selection. A host-root route would know neither. The
audience `npm:<host>` is added to the issuer default because pnpm requires it, and npm requests it
itself on GitHub Actions (R-CI-025). Like the base URL, it names this installation only. The
development agent would revisit this if npm moves its exchange below the registry URL (the
host-root prefix could then go), or if path-prefixed deployments become a requirement (that needs
cookie, UI and OCI work first; (c)'s fallback would come back with it).

**Applied in the spec:** R-NPM-001: one host-root prefix plus the registry-relative exchange,
host-root ownership through R-OPS-005, the attestation path left to the generic 404; endpoint map
row added and the attestation row moved below the registry URL. R-NPM-022: the verified client
table (commands, URL, audience check per version), both routes as one handler with R-AUTH-001's
exemption, host-root ownership instead of the path-prefix fallback, the `npm:<host>` audience, and
acceptance criteria for npm, pnpm and Yarn and for a foreign audience. The startup warning and the
path-prefix criterion are removed. R-NPM-027 and the Deferred row PKG-NPM-024: attestations below
the registry URL through a rewritten URL. R-NPM-041: the native snippet uses `aud:
"npm:trove.example.com"`, and the generic-exchange snippet now serves clients without a native
exchange. R-NPM-045: the not-applicable rule names those rows. The Security notes, H-31 and H-32
follow. Cross-page: INV-6 on [README.md](../README.md), R-OPS-005, R-AUTH-001, the CI issuer
audience default, R-ARCH-005, R-HA-037 and Q-HA-4.

### Q-NPM-2 · Serve versions the upstream removed

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Hide by default | `serveRemovedVersions: false`: a version the upstream no longer lists disappears from documents, and its cached tarball answers 404 | A takedown reaches every client, including lockfile installs | A build pinned to a legitimately withdrawn version breaks until the tarball is published locally or the switch is set |
| (b) Serve by default | `serveRemovedVersions: true`: whatever was once cached stays served | Maximum availability; no build ever breaks because of the upstream | The proxy keeps distributing exactly what the upstream withdrew, which is mostly malware |
| (c) Hide from documents, serve cached tarballs | New resolutions no longer pick the version; requests by exact tarball URL still succeed | Lockfile builds keep working; ranges stop resolving to the version | `npm ci` fetches tarballs by URL without reading documents, so every project that already pinned a malicious version keeps installing it |

**Choice:** (a) — hide removed versions and their tarballs by default, with the per-remote switch for availability-first repositories.

**Rationale:** Most upstream removals are takedowns, and the client path that matters after a
takedown is the lockfile: `npm ci` asks for tarballs by URL (R-NPM-025), so (c) keeps the payload
flowing to every project that already pinned it, and (b) to everyone. The cost of (a) is a loud 404
with two documented fixes. To keep (a) from producing false removals, a stale upstream copy (older
`time.modified`, as a lagging CDN edge serves) never marks a version removed. The development agent
would revisit this if `trove_npm_removed_upstream_versions_total` shows removals that the upstream
reverses within one period (a sign of false positives the stale-copy rule misses), or once a
curation capability can quarantine versions with an allow list.

**Applied in the spec:** R-NPM-024 adds the stale-copy rule and the metric
`trove_npm_stale_upstream_documents_total`. The stored `time.modified` is capped at its receive
time, so one future-dated document cannot freeze the cache and with it every later takedown; the
rule is skipped when a document has no `time.modified`. R-NPM-026 states the default, covers
lockfile requests explicitly, re-lists a version the upstream lists again, names the fixes for a
legitimately withdrawn version, and gains an acceptance criterion for the stale copy. R-NPM-044
lists the new metric.

### Q-NPM-3 · Audit forwarding on by default

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Forward, minus recognised internal names | Default `forward`; names present in local repositories, claims or pin rules are removed, everything else is sent | Complete audits for every lockfile | An internal name that nothing marks as internal (a git dependency's package name, a package from another registry) is disclosed to the provider |
| (b) Disabled by default | `disabled`; operators opt in per remote | Nothing leaves the installation | `npm audit` fails and `npm install` reports an audit error on every run; developers lose a security signal by default |
| (c) Forward only names already proxied | Default `forward`; send only names the forwarding remote has fetched from its upstream, minus names local repositories, claims or pin rules mark internal; an `npm-notice` header reports withheld names | No name leaves that the upstream has not already seen from this installation; audits stay on | A lockfile resolved elsewhere is audited partially until its packages have passed through trove; one extra query per audit |

**Choice:** (c) — forward by default, but only names the forwarding remote already holds a document for, and say so when names were withheld.

**Rationale:** (b) trades a security signal for a disclosure that (c) shows is avoidable. (a) depends
on recognising every internal name, and the only names it can recognise are those trove has been
told about; (c) inverts the test so the property (a) argues for, "no new names leave", holds by
construction: a name the remote has fetched is already known to the upstream. The
partial-audit cost is small in practice (any install through trove caches every public package of
the tree), and it is visible: npm prints `npm-notice` on every response (verified in
[check-response.js](https://github.com/npm/npm-registry-fetch/blob/main/lib/check-response.js)).
Withdrawn packages stay forwardable, because malware advisories are exactly what an audit should
find. The development agent would revisit this if `trove_npm_audit_names_withheld_total{reason="not_proxied"}`
stays high for real users, which would call for an opt-in per remote to forward unproxied names.

**Applied in the spec:** R-NPM-038 rewritten: the proxied-names rule, withholding of names covered
by any claim scope, the `npm-notice` text, the `{}` answer when every name is withheld, and two new
acceptance criteria. R-NPM-005 lists R-NPM-038 as a user of `npm-notice`. R-NPM-033 and R-NPM-044
(metric `trove_npm_audit_names_withheld_total`) follow. The Security notes and H-34 describe the
remaining disclosure.

### Q-NPM-4 · Current client majors in the acceptance gate

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Verified on 2026-10-28: npm `latest` is `12.2.1`, the 11 line `11.21.1`, the 10 line `10.9.9`
(published 2026-07-29); pnpm `12.16.1`, `11.28.9` and `10.34.9` were all published in the last week;
Yarn Berry `4.18.1`, Yarn 3's newest release is from 2024-12-04. Node.js 22 (maintenance LTS until
2027-04-30) bundles npm `10.9.9`, Node.js 24 bundles `11.19.1`, Node.js 26 (active LTS from
2026-10-28) bundles `11.20.0`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Fixed list with the 12 lines | Gate on npm 11 and 12, pnpm 10 and 12, Yarn 4, Yarn 1.22 | Covers the majors new installations get; six rows | Misses npm 10, which every Node.js 22 image runs, and pnpm 11, which still ships; the list goes stale with each release |
| (b) Fixed list, 12 lines non-blocking | Gate on npm 11, pnpm 10, Yarn 4, Yarn 1.22; npm 12 and pnpm 12 as non-blocking rows | Cheapest; fewest flaky failures | A registry regression for the clients new installations use does not block a release |
| (c) Rule-based rows | npm: every major bundled with a Node.js LTS line plus `latest`; pnpm: every major released within 6 months; Yarn Berry: `latest` plus the previous major while released within 6 months; Yarn 1.22 always. Today: npm 10, 11, 12; pnpm 10, 11, 12; Yarn 4; Yarn 1.22 | Gates what users actually run; stays correct as Node.js lines end and majors appear, without a spec change | Eight rows instead of four to six, run in parallel on the same scenario list; scenarios that a row's client cannot run must be marked per row |

**Choice:** (c) — derive the gate rows from Node.js LTS bundling and release recency, resolved today to npm 10, 11, 12, pnpm 10, 11, 12, Yarn 4 and Yarn 1.22.

**Rationale:** The verified data shows that any fixed list written today is already wrong in one
direction: (a) leaves out npm 10 although it is the npm of every `node:22` CI image until April 2027,
and (b) leaves out the majors new installations get. A rule keeps "conformant with the real clients"
true over time, and the extra rows are CI minutes, not development effort. Each row still pins one
exact version (no unpinned tool versions). The development agent would revisit this if the npm
job's wall time per release becomes the bottleneck; the middle pnpm major would then move to nightly
first.

**Applied in the spec:** R-NPM-045 rewritten with the rule, today's rows and their reasons, pin
maintenance, and per-row "not applicable" marking. R-NPM-004, R-NPM-007 and R-NPM-040 refer to the
gate rows instead of single versions.

### Q-NPM-5 · Upstream representation for remote repositories

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Full document only | Fetch and store the full document; derive the abbreviated one (R-NPM-024) | One stored view of which versions exist; `time` and `_npmUser` always present for release-age and trust policies; one conditional request per refresh | More bytes on a cold cache for packages with large documents |
| (b) Both variants | Fetch the abbreviated document for abbreviated requests and the full one for full requests, storing both | Smaller transfers for install traffic (on the wire about 15 to 20 % less for the largest documents measured) | Two independently refreshed entries can disagree about which versions exist; the abbreviated entry cannot serve `time` or `_npmUser`, so release-age and trust checks need the full one anyway |

**Choice:** (a) — fetch and store only the full document and derive the abbreviated form.

**Rationale:** The public registry's abbreviated document carries no `time` map and no `_npmUser`
(checked on 2026-10-28), so (b) cannot serve pnpm's `registrySupportsTimeField` or trust policy from
its abbreviated entry and ends up fetching both anyway, with the added risk of two entries
disagreeing. The bandwidth cost of (a) is small where it counts, on the wire. Measured on
2026-10-28 with gzip, `next` transfers 2.4 MB full against 2.0 MB abbreviated (31.5 MB against
25.7 MB unpacked), and `@types/node` 1.4 MB against 1.1 MB (11.2 MB against 2.3 MB unpacked). Both
are far below the 256 MiB `max_metadata_bytes` default of R-REPO-040, and a refresh is a
conditional request. The development agent would revisit this if upstream transfer volume or
document parse time shows up as a bottleneck in the fetch and render metrics.

**Applied in the spec:** No change to the rule; the requirement text already follows this option.
R-NPM-024 now states the verified fact that the upstream abbreviated form lacks `time` and
`_npmUser`.

### Q-NPM-6 · Manifest consistency: verify or derive

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Verify and reject | Compare the install-relevant fields of the request manifest with the tarball's `package.json` after applying the clients' publish-time normalization steps to both sides; 400 on mismatch, `warn` mode as escape hatch | Closes manifest confusion; a normalization gap shows up as a visible 400 naming the field | Needs trove to port the normalization steps that touch the compared fields, for npm and Yarn 1 |
| (b) Derive from the tarball | Ignore the request manifest; build the stored manifest from `package.json` | No comparison logic | Needs every client's full normalization reproduced exactly, and silently changes what the publisher sent when it is not |
| (c) Overlay without rejecting | Take the compared fields from the tarball, everything else from the request | No false rejections | The same normalization dependency as (b) for those fields, failing silently: for example dropping npm's implicit `install: node-gyp rebuild` changes what installs |

**Choice:** (a) — verify and reject, comparing both sides after the union of the clients'
normalization steps for the compared fields, spelled out in R-NPM-007.

**Rationale:** Verified on 2026-10-28 in npm 11.17.0 (`@npmcli/package-json` 7.0.5), pnpm 10.34.9
and Yarn 1.22.22. npm publishes a manifest normalized by `fix()` and `prepare()`, pnpm 10 inherits
npm's `prepare()` through `npm publish <tarball>`, and Yarn 1 applies its own rules, while the
packed `package.json` stays the raw file. Any option has to model these normalizations. (a) is the
only one where an incomplete model fails loudly and is fixed by adding a step. (b) and (c) would
store a manifest that differs from what installs. The previous list held only npm's `prepare()`
steps and would have rejected ordinary packages. npm adds a bundled name that is missing from
`dependencies` with range `*`, and cleans `v1.2.3` to `1.2.3`. Yarn 1 adds `install: node-gyp
rebuild` for a `binding.gyp` even when a `preinstall` exists. Applying the union to both sides
keeps every step idempotent and client-independent. pnpm 11 and 12 and Yarn 2+ build the publish
body from the packed manifest, so they agree with the tarball by construction (not verified line
by line; the fixture corpus confirms it per gate row). The development agent would revisit this if
the fixture corpus or field reports show rejections that no missing normalization step explains.

**Applied in the spec:** R-NPM-007 states which client normalizes how, applies the union of steps
to both sides, and lists the steps: name and version cleaning; bundle dependencies, including the
`*` entry for bundled names; `bin` and `directories.bin`; script cleaning and both gyp rules;
dependency shapes and hosted-git shorthands; and Yarn 1's `engines` arrays. It notes that optional
dependencies are no longer copied into `dependencies`, and states which differences a step may
erase. The fixture-corpus criterion covers the new steps.

### Q-NPM-7 · Git dependencies in the external dependency rewrite

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) HTTP(S) tarball URLs only | P1 rewrites only `http`/`https` tarball dependencies (R-NPM-034); git dependencies stay unchanged | What trove serves is byte-for-byte what the client would have fetched | Git dependencies still reach the internet directly |
| (b) Also commit-pinned hosted-git dependencies | Rewrite git dependencies pinned to a full commit SHA to the host's archive download | More external traffic goes through trove | npm installs a git dependency by cloning, running its `prepare` script and applying its `files` rules; an archive skips all three, so packages that build on install silently get different content |

**Choice:** (a) — rewrite only HTTP(S) tarball URLs in P1.

**Rationale:** (b) changes what gets installed for exactly the packages that build on install, and
branch references cannot be cached safely at all. Correctness of what a client installs outranks
covering more traffic. The development agent would revisit this in P2 for commit-pinned
dependencies whose archive's `package.json` has no `prepare` script, which trove can check before
rewriting.

**Applied in the spec:** No change; the requirement text already follows this option.

### Q-NPM-8 · Purging tombstones

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) No purge | A tombstone outlives trash and permanent removal; only a restore from the trash or deleting the repository removes it | Lockfile integrity can never be broken by a version reuse | A version number withdrawn by mistake after the trash is emptied stays unusable; the next patch version serves |
| (b) Administrator purge | A system-administrator-only operation removes a tombstone, with an audit event | Recovers from mistakes | Reopens the integrity hazard for whoever holds the role; a reused version installs different code under a lockfile that pinned the old integrity |
| (c) Republish of identical bytes | A publish whose integrity equals the tombstone's is accepted | Integrity is preserved, so lockfiles stay valid | A rerun of an old release pipeline silently re-releases a version someone withdrew on purpose (a leaked secret, a broken build), which a 409 would have stopped |

**Choice:** (a) — no purge and no identical-bytes exception in MVP and P1.

**Rationale:** (c) is the serious alternative, because it keeps the integrity guarantee, but it
removes the second thing a tombstone does: making a withdrawal stick against automation that
republishes. The deliberate path back, a restore from the trash, already exists while the trash
holds the version, and after that the next patch version costs nothing. (b) makes the guarantee
depend on a role. The development agent would revisit this if an import from another system needs
to bring tombstoned versions back.

**Applied in the spec:** R-NPM-008 states that no API, UI or `trove admin` operation removes a
tombstone and that a republish of identical bytes is refused.

### Q-NPM-9 · `annotate` or `deploy` for dist-tags and deprecation

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Both operations change what clients install: `npm install <pkg>` follows `latest`, and npm's
version picker avoids deprecated versions whenever a non-deprecated one satisfies the range, so
deprecating `1.2.4` makes `^1.2.0` resolve to `1.2.3`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `annotate` | Dist-tags and deprecation need `annotate` plus ownership | Lets metadata tooling manage tags without publish rights | A principal given `annotate` for property tooling can redirect what every client installs |
| (b) `deploy` | Moving dist-tags and deprecation need `deploy` plus ownership; `annotate` stays for item properties | `deploy` already means "may change what gets installed"; one action for every install-changing write | Tag-management tooling needs `deploy` on the package |

**Choice:** (b) — `deploy` plus ownership for dist-tags and deprecation.

**Rationale:** Both operations redirect installs, so they belong to the action that already
authorizes changing what gets installed; ownership narrows who holds it per package.
[authorization.md](../authorization.md) R-AUTHZ-006 and R-AUTHZ-007, which own the action map,
require exactly this, and this page follows them. The development agent would revisit this only if
a tag-only workflow appears that cannot be served by a package-scoped `deploy` grant.

**Applied in the spec:** No change; R-NPM-004, R-NPM-016 and R-NPM-018 already require `deploy` per
R-AUTHZ-007.

## What this does not cover

### H-31 · Trusted publishing widens the set of credentials that can publish

The npm exchange routes (R-NPM-022) let every GitLab job whose identity mapping grants `deploy` on
a package publish it without a stored token. The token is package-bound and short-lived, but the
decision rests entirely on the identity mappings of [gitlab-ci-oidc.md](../gitlab-ci-oidc.md); a
mapping that matches too many projects or unprotected refs lets those jobs publish. trove does
not add a second check (such as protected-ref-only publishing) on top of the mapping.

### H-32 · Registry signatures and provenance cannot be verified through trove

Until PKG-NPM-024 and PKG-NPM-037 are implemented, trove serves no signing keys and no
attestations. `npm audit signatures` against trove cannot verify the upstream signatures and
provenance it passes through (R-NPM-027), and locally published packages carry neither. pnpm's
trust policy still works on the passed-through fields, but trove does not check them
cryptographically on the way through.

### H-33 · Takedown delay and earlier downloads

When an upstream removes a malicious version, trove stops serving it (R-NPM-026) only after the
remote's metadata period and the document refresh. Until then, and for every client that
installed it earlier, the version stays reachable. trove does not scan package content;
curation and scanning are separate capabilities.

### H-34 · Audit forwarding discloses the dependency set

Component: audit forwarding (R-NPM-038). Subject: the dependency set of every project that runs
`npm audit`, `pnpm audit` or `yarn npm audit` against a repository whose remote forwards (the
default). Live once R-NPM-038 (P1) ships. trove forwards only names its remote has already fetched
from the upstream and that no local repository, claim or pin rule marks internal, so no internal
name the upstream has not seen leaves the installation. What still leaves it: the combination of
public names and versions per project, which tells the provider (by default the upstream registry)
what a project is built from, and, with a `providerURL` other than the upstream, the same names to a
second party. An internal name that once reached the upstream (an earlier confusion attempt that
found a public package of the same name) counts as fetched and is forwarded unless a claim covers
it. What makes it real: projects whose dependency combination is itself confidential.
