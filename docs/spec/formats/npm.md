# npm registry

Status: specification — not implemented

This page specifies how trove speaks the npm registry protocol for local, remote and virtual
npm repositories: the endpoints below `/api/npm/<repoKey>/`, how the npm format plug-in classifies
requests, how package documents are generated, cached and merged, how publish, dist-tags,
deprecation and unpublish change state, how a remote repository fetches and validates npm
metadata, how a virtual repository merges members, how npm behaves when requests reach `web`
replicas at random, while Valkey is unavailable and across a backup and restore, and the client
acceptance tests that gate the format. It is the npm half of the format plug-in contract: generic
semantics live on the owner pages and are referenced, not repeated.

| Topic | Owner |
|---|---|
| Repository model, include/exclude patterns, virtual resolution, merged-metadata cache, deployment target, priority members, pin rules and namespace claims, every remote setting (cache periods, negative cache, refresh timeout, circuit, egress, redirects, size limits, integrity policy, URL rewriting, upstream credentials) | [repositories-and-proxy.md](../repositories-and-proxy.md) |
| Blob store, packages, versions and items tables, upload pipeline, trash can, garbage collection | [storage.md](../storage.md) |
| Credential sources, 401 challenges, tokens, anonymous access, npm web login, legacy login and logout | [identity-and-authentication.md](../identity-and-authentication.md) |
| Actions per npm operation, virtual gate, 403/404 decisions, package ownership | [authorization.md](../authorization.md) |
| GitLab ID-token validation and identity mappings | [gitlab-ci-oidc.md](../gitlab-ci-oidc.md) |
| Audit event storage, jobs, metrics conventions | [operations-and-observability.md](../operations-and-observability.md) |
| Client setup dialog, management API conventions | [api-and-ui.md](../api-and-ui.md) |
| Format plug-in interface, configured public base URL, trusted reverse proxies, process roles, in-process caches and the change log, Valkey usage and degradation, cross-replica request collapsing, no state between requests, graceful shutdown | [architecture.md](../architecture.md) |
| Backup contents, export classes, revision columns, restore fix-ups | [backup-and-restore.md](../backup-and-restore.md) |
| Node drain, role split and web pools, transaction-mode pooler, revocation journal | [ha-and-helm.md](../ha-and-helm.md) |
| Endpoints: per-endpoint hosts and base URLs, accepted credentials, exposure rules and the exposure adapter that R-NPM-048 implements | [endpoints.md](../endpoints.md) |

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
  `/files/<repoKey>/<path>` (INV-6) uses the same path, and is refused wherever the npm tarball
  route refuses the tarball: a tombstoned version (R-NPM-015, R-NPM-047) or a version removed
  upstream (R-NPM-026) answers 404 on `/files` and on the cache view as well. Package documents are
  generated and have no item.
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
  Yarn 2+ prints it only on error responses) is set only where this page says so (R-NPM-007,
  R-NPM-016, R-NPM-038).
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
- All npm state is in these PostgreSQL tables and in the blob store (INV-3, INV-4). Nothing npm
  needs is held only in Valkey or on a replica's disk; the replica-local spool holds the body of
  the request that is running and nothing else (INV-13, INV-18). Backup export classes and restore
  behavior of these tables are R-NPM-047.

Data model (sketch; `packages`, `package_versions`, `items`, `repositories` are storage.md's):

```sql
CREATE TABLE npm_packages (
  package_id     bigint      PRIMARY KEY REFERENCES packages (id) ON DELETE CASCADE,
  repo_id        bigint      NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
  name_fold      text        NOT NULL,            -- lower(name) without '-', '_', '.' (R-NPM-002)
                                                  -- no rev_seq here: packages.rev_seq (R-NPM-009)
  dist_tags      jsonb       NOT NULL DEFAULT '{}'::jsonb,  -- {"latest": "1.2.0", "next": "2.0.0-rc.1"}
  modified_at    timestamptz NOT NULL DEFAULT now(),
  -- search_vector tsvector: added with R-NPM-037 (P1) as an expand step (Q-ARCH-191)
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
  readme             text,                        -- R-NPM-010, extracted at publish, at most 64 KiB
  readme_filename    text,
  publish_tag        text        NOT NULL,        -- dist-tag the publish request carried (R-NPM-007 step 4)
  published_at       timestamptz NOT NULL         -- PostgreSQL clock (R-NPM-012)
);

CREATE TABLE npm_tombstones (                     -- outlives trash and permanent removal (R-NPM-008)
  repo_id        bigint      NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
  name           text        NOT NULL,
  version        text        NOT NULL,
  integrity      text        NOT NULL,
  source         text        NOT NULL CHECK (source IN ('unpublish', 'move', 'journal')),
                                                  -- 'journal': written by a restore's replay; the version
                                                  -- may still be stored, withheld (R-NPM-047)
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
   `npm_version_unpublished`. One exception, the **acknowledged retry** (Q-NPM-11): the request is
   a client's retry of a publish whose answer was lost when all of these hold for the existing,
   live version:
   - it was published by the same principal;
   - `now() − published_at` is less than `npm.publish.retry_window`, both on PostgreSQL's clock
     (R-NPM-012), never on the receiving replica's;
   - its stored tarball has the SHA-512 just computed;
   - the request carries a `.sigstore` attachment exactly when the version has a stored one, with
     the same SHA-256;
   - the request's dist-tag equals the `publish_tag` the original request carried.

   trove changes nothing (no row, no tag, no `rev_seq`, no object) and answers 201 as in step 6,
   with `npm-notice: <name>@<version> was already published by you at <time>; nothing changed`.
   Every fact the rule reads is in PostgreSQL, so the replica that receives the retry decides it
   exactly as the one that committed the original would (INV-18).
5. Commit through R-STOR-005. Inside the commit transaction (`OnCommit` hook): lock or create the
   package rows, re-check immutability (the acknowledged retry of step 4 included, so a retry that
   overtakes its slow original on another replica ends in 201 as well), insert `package_versions`,
   the item and `npm_versions` (with `publish_tag`, and the README of R-NPM-010), apply the tag
   under R-NPM-016 against the `latest` read under that lock, increment `packages.rev_seq`, set
   `published_at` and `modified_at` from PostgreSQL's clock after the lock is held (R-NPM-012),
   update the search vector, write the audit event (R-NPM-044). No correctness depends on a
   message to other replicas after the commit: every replica reads the new revision on its next
   document request (R-NPM-010), and merged entries check it (R-NPM-030). Since 2026-10-28 trove
   issues no `NOTIFY` ([architecture.md](../architecture.md) R-ARCH-018, Q-ARCH-15).
6. Answer 201 `{"ok": true, "id": "<name>", "rev": "<rev>"}` once the blob is durable and the
   transaction committed; npm ignores the body. A replica that shuts down never cuts a publish
   between its commit and this status line ([architecture.md](../architecture.md) R-ARCH-024).

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
succeed. This holds whichever replicas receive them, because the lock is a PostgreSQL row lock
taken in the commit transaction. The look-alike rule of R-NPM-002 is enforced by the
`UNIQUE (repo_id, name_fold)` constraint of `npm_packages`, not by a read before the insert: the
row is created with `INSERT … ON CONFLICT DO NOTHING` and then locked
([INSERT … ON CONFLICT](https://www.postgresql.org/docs/current/sql-insert.html#SQL-ON-CONFLICT)),
so of two concurrent first publishes of `acme-ui` and `acme_ui` through different replicas one
commits and the other answers 400 `npm_invalid_name`, and two concurrent first publishes of one
new name both succeed.

Configuration (`trove.yaml`):

```yaml
npm:
  publish:
    max_tarball_size: 256MiB       # default (proposed); bounded by storage.upload.max_size
    max_unpacked_size: 2GiB        # default (proposed)
    max_tar_entries: 100000        # default (proposed)
    manifest_consistency: reject   # default (proposed); reject | warn
    retry_window: 10m              # default (proposed); acknowledged retry of step 4, 0s turns it off (Q-NPM-11)
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
- Two concurrent publishes of `1.0.1` and `1.0.2` of one package, sent to two different replicas,
  both succeed. Two concurrent first publishes of `acme-ui` and `acme_ui`, sent to two different
  replicas, end in one 201 and one 400 `npm_invalid_name`, never in a 5xx.
- A fault-injecting edge resets the client connection of a committed `npm publish` of
  `@acme/ui@1.0.3` after trove has sent its status line. npm retries the `PUT`, the retry reaches
  another replica, and `npm publish` exits 0 with the `npm-notice`. `@acme/ui` has version `1.0.3`
  once, its `rev_seq` grew by one, and the audit log holds one `npm.publish` event with outcome
  `success` and one with outcome `retry_acknowledged`. The same request sent by another principal,
  with another tarball, with another dist-tag, without the `.sigstore` attachment the original
  carried, or after `npm.publish.retry_window` answers 409 `npm_version_exists`.
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
  lets formats refuse regardless). Lockfiles pin `integrity` per version. A client's retry of a
  publish whose answer was lost is not a second publish: R-NPM-007 step 4 acknowledges it only
  for the same bytes and the same principal, and changes nothing (Q-NPM-11).
- Unpublish writes an `npm_tombstones` row with the version's integrity. So does, from P1, a move
  of the version out of a local repository (R-STOR-019): the source repository's hook writes the
  row in the move transaction. Clients of the source repository may have cached the version, so a
  rebuilt version under the same number there would diverge exactly as after an unpublish; moving
  the version back is refused like a republish. A copy writes none, because the version stays.
  The row survives the trash can and permanent removal (R-STOR-022, R-STOR-023). No API, UI or
  `trove admin` operation removes a tombstone of a version whose bytes are gone, and a republish of
  identical bytes is refused like any other (Q-NPM-8). Only three operations remove a tombstone,
  each of them by bringing back the version's own stored bytes, never new ones: the trash-can
  restore below, `trove admin npm reinstate` for a version a restore's replay withheld (R-NPM-047),
  and the deletion of the whole repository.
- From P1, restoring a version from the trash can (R-STOR-023) brings back its original bytes;
  that is not a reuse. The restore removes the tombstone and the version reappears in documents.
  The trash can is P1 (R-STOR-022): in the MVP an unpublish deletes the version permanently, and
  only its tombstone remains.
- A restore of a backup (INV-19) returns the tombstones, like every other table, to the backup's
  snapshot. Unpublishes made after the snapshot are re-applied by the journal replay, which
  tombstones those versions again and withholds them (R-NPM-047, Q-NPM-12). The replay deletes
  nothing: in the MVP, where the trash can does not exist, a replayed deletion could not be undone.
  H-51 names what a restore still reverts.
- Remote and virtual repositories serve upstream versions as they are; changed upstream digests
  for a cached version are handled by R-REPO-041.

Acceptance criteria:

- A second `npm publish` of `@acme/ui@1.2.0` with another tarball, by another principal, or after
  `npm.publish.retry_window` fails with 409 `npm_version_exists`.
- After `npm unpublish @acme/ui@1.2.0`, publishing `1.2.0` fails with 409
  `npm_version_unpublished`, also after the item is permanently removed from the trash;
  publishing `1.2.1` succeeds. The same holds for a principal with `overwrite`.
- From P1, restoring the unpublished version from the trash makes
  `npm view @acme/ui@1.2.0 dist.integrity` print the original integrity.
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
- **A stale revision whose request changes nothing succeeds.** When `{rev}` (or a body's `_rev`,
  R-NPM-018) is stale but applying the request to the current rows would change nothing (every
  version it removes is already removed, every deprecation it sets is already set), trove answers
  as for a successful write, with the current `_rev`, and writes nothing. npm retries a `PUT` or
  `DELETE` whose answer was lost (Q-NPM-11), and the retry reaches any replica with the revision of
  the attempt that already committed; without this rule it would fail with 409 although its
  effect is in force. No change is lost by it, because the request changes nothing.
- `GET {name}?write=true` MUST return the full document regardless of `Accept`, read from
  PostgreSQL inside the request, without `_attachments`. On a virtual repository it returns the
  deployment target's own document (R-NPM-029). A read-modify-write client (`npm deprecate`,
  `npm unpublish`) may send its read and its write to different replicas; both see the same
  `packages.rev_seq`, because neither reads it from a replica's memory (INV-18).
- Remote documents carry the upstream's `_rev` if any; virtual documents carry a synthetic `_rev`
  derived from the merge fingerprint of R-NPM-013, so every replica gives one merged document the
  same `_rev`. Neither can be written through.

Acceptance criteria:

- `curl '…/npm-local/@acme%2fui?write=true'` with an abbreviated `Accept` header returns
  `Content-Type: application/json` and a `_rev`.
- `PUT …/@acme%2fui/-rev/<old rev>` after a newer publish answers 409.
- `npm unpublish @acme/ui@1.0.0` succeeds; replaying its `PUT …/-rev/<rev>` with the same body
  through another replica answers 200 with the current `_rev` and changes nothing.

### R-NPM-010 · Full package document
Priority: **MVP** · Covers: PKG-NPM-025, PKG-NPM-029, FMT-011

- Local package documents are computed from the rows of R-NPM-006; clients never upload one.
- A publish, unpublish, deprecation or dist-tag change MUST be visible on every replica as soon as
  its request has returned 2xx. Each document request reads the package's `packages.rev_seq` (one
  indexed lookup, R-ARCH-012) and renders, or reuses the rendering for, that revision from the
  replica's in-process rendition cache (R-ARCH-018, Q-NPM-10). No message between replicas is
  involved: a cached local document is keyed by its revision and has no invalidation to miss, so a
  CI job that publishes and immediately installs through any replica never sees the old document,
  with or without Valkey.
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
  manifest or, if absent, from the tarball's first top-level `README*` file, cut at 64 KiB. The
  README is extracted once, at publish (R-NPM-007 step 5, `npm_versions.readme`), so a rendering
  on any replica reads rows only and never fetches a tarball from the object store.
  `maintainers` lists the distinct principal names that published the versions present, without
  e-mail; it is informational (ownership is R-AUTHZ-018).
- An unknown package, or one without a live version, answers 404.

Acceptance criteria:

- `npm publish` followed directly by `npm view @acme/ui@<new version> version`, in the `scatter`
  profile of [architecture.md](../architecture.md) R-ARCH-032 (three `web` replicas, every request
  to another one, no session affinity), prints the new version in 100 of 100 runs, with Valkey
  running and with Valkey stopped.
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
- Local times (`published_at`, `modified_at`, hence `time` and `Last-Modified`) come from
  PostgreSQL's clock, never from the clock of the replica that received the publish. They are
  taken with `clock_timestamp()` after the package row lock is held, not with `now()`, which is
  the start of the transaction
  ([current date/time](https://www.postgresql.org/docs/current/functions-datetime.html#FUNCTIONS-DATETIME-CURRENT)):
  two writes of one package serialize on that lock, so their times follow their commit order even
  when the later one started first. Every write that increments `packages.rev_seq` (publish,
  unpublish, dist-tag change, deprecation, reindex, a restore's replay) sets `modified_at` to
  `greatest(modified_at + 1 ms, clock_timestamp())`, so `time.modified` never moves backwards.
  Versions published through different replicas are therefore ordered by one clock, which
  release-age policies and the `newest-published` strategies of R-NPM-031 compare, and a
  downstream proxy's stale-copy check (R-NPM-024 run against trove) never mistakes a newer
  document for an older one.
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
  `If-None-Match` with 304 ([RFC 9110 §13](https://www.rfc-editor.org/rfc/rfc9110#section-13)).
  The ETag is derived from inputs, so a 304 needs no rendering: local (repository, package,
  `packages.rev_seq`, variant, public base URL, rendering settings); remote (stored document
  digest, serving address, variant, settings, as R-REPO-043 requires); virtual (merge fingerprint,
  variant); and, for every class, the endpoint of the request and its exposure fingerprint
  (R-NPM-048, [endpoints.md](../endpoints.md) R-EP-018). The in-process rendition and
  merged-metadata caches key their entries by the same tuple, so a replica never serves cached
  bytes under a validator computed from other inputs.
- **`If-Modified-Since` never produces a 304 on a package document.** It is ignored when
  `If-None-Match` is present, as RFC 9110 §13.1.3 requires, and also when it arrives alone. A
  document's date does not move with every change of its bytes: a restore returns
  `time.modified` to the snapshot while clients may hold later dates from the old installation,
  and a member, permission or setting change, or a new `npmRenderVersion`, changes the bytes
  without a new date. A date could therefore validate bytes that differ, on one replica or
  across a restore. Every package document carries an ETag, which a client's HTTP cache sends back
  as `If-None-Match` (Yarn 2+ does, R-NPM-042; npm and pnpm revalidate through the HTTP caches of
  their fetch layers, PKG-NPM-032); a client that sends only a date receives 200, which costs
  bytes, never correctness.
  Tarballs keep date validation (R-STOR-012), because their bytes never change.
- **The same validator on every replica.** The Service sends a conditional request to any `web`
  replica, usually not the one that issued the ETag (INV-18). Every input of an ETag is therefore
  a value each replica reads from PostgreSQL or from the shared configuration, plus
  `npmRenderVersion`, a constant of the plug-in that a release increments whenever it changes the
  bytes rendered from the same inputs. No input is per replica: no cache-entry time, no replica
  identity, no random value. Every replica issues the same ETag for the same bytes and answers 304
  only when it would serve those bytes ([RFC 9110 §8.8.3](https://www.rfc-editor.org/rfc/rfc9110#section-8.8.3));
  during a rolling upgrade ([ha-and-helm.md](../ha-and-helm.md) R-HA-020), replicas of two
  releases that render differently never share an ETag. In detail:
  - "rendering settings" are the `npm` keys of `trove.yaml` that change rendered bytes
    (`npm.abbreviated_time`) and the `config_version` of the serving repository, which covers its
    repository settings;
  - the *merge fingerprint* of a virtual document is: the member fingerprint of R-REPO-020 (which
    already reflects patterns, pin rules, claims and the caller's readable members), each
    contributing member's revision (local: `packages.rev_seq`, 0 when absent; remote: the stored
    document digest), the `config_version` of the virtual repository and of each member (covering
    `latestStrategy`, `distTagStrategy`, `externalDependencies` and `serveRemovedVersions`), the
    public base URL and `npmRenderVersion`;
  - after a restore, `rev_seq` and `config_version` have grown by 2^32 (R-BAK-022, R-NPM-047), so
    no ETag of the old installation matches a restored document.
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
  document_max_age: 0s # default (proposed), a duration (Q-REPO-85); clients revalidate every time, which a 304 makes cheap
```

Acceptance criteria:

- A second Yarn 4 `yarn install` with a warm Yarn cache receives 304 for every package document.
- After `npm dist-tag add`, a conditional request with the old ETag returns 200 with a new ETag.
- A request with `If-Modified-Since` set to the document's own `Last-Modified` and no
  `If-None-Match` answers 200; the same request with a matching `If-None-Match` and an
  `If-Modified-Since` in the past answers 304.
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
  conditional requests); trashed or tombstoned versions answer 404, a version that a restore's
  replay withheld (stored, tombstoned, R-NPM-047) included, and so does `/files` for their items
  (R-NPM-003); deprecated versions are served normally.
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
  sets `latest`; when the version `latest` points to is unpublished, trashed or withheld
  (R-NPM-047), `latest` moves to the highest remaining non-pre-release version, or the highest
  version if all are pre-releases; other tags pointing to it are removed.
- Every tag change, by a publish, a tag write or a removal, runs under the package row lock of
  R-NPM-007 step 5 and applies the guard and the rules above to the values read under that lock.
  Concurrent publishes and tag writes through different replicas therefore cannot leave a package
  without `latest`, or with a tag that points to a removed version.
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
| PUT | `{name}/-rev/{rev}` | full document without the versions to remove | Each live version absent from `versions` is deleted through [storage.md](../storage.md) R-STOR-018 (to the trash can from P1, permanently in the MVP) and tombstoned | 200 `{"ok": true, "rev": "<new rev>"}`; 400 if `versions` is missing or names a version trove does not hold; 409 stale rev, unless the request changes nothing (R-NPM-009) |
| DELETE | `{name}/-/{file}.tgz/-rev/{rev}` | — | No-op for a version already removed by the `PUT` (npm sends it next) | 200; 400 if the version is still live |
| DELETE | `{name}/-rev/{rev}` | — | Every live version deleted and tombstoned; all dist-tags removed | 200; 409 stale rev, unless every version is already removed (R-NPM-009) |

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
- If the body carries `_rev`, it MUST equal the current revision (409 otherwise), unless the
  request changes nothing (R-NPM-009).
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
- `npm ping` with a valid token succeeds. After the token is revoked through another replica,
  `npm ping` fails with `E401` on every replica within 2 s
  ([architecture.md](../architecture.md) R-ARCH-018, R-AUTH-023), with Valkey running and with
  Valkey stopped.

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
- The start, the approval page's requests and every poll are separate requests that the Service
  spreads over the `web` replicas, and with web pools ([ha-and-helm.md](../ha-and-helm.md)
  R-HA-062) over several Deployments: the start lies below the registry URL, the poll under
  `/auth/cli/`. Their only shared state is the `cli_login` row in PostgreSQL (INV-18, R-AUTH-034),
  so each request may reach a different replica. The start is limited cluster-wide (R-AUTH-038,
  [architecture.md](../architecture.md) R-ARCH-034).
- One approval yields one token, whichever replicas the polls reach: the poll that mints the token
  moves the row from `approved` to `consumed` in the minting transaction, on the condition that it
  is still `approved`, and a concurrent poll on another replica then finds it consumed and gets 404
  (R-AUTH-034).

Acceptance criteria:

- `npm login --registry=https://trove.example.com/api/npm/npm-all/ --auth-type=web`, `pnpm login
  --auth-type=web` and `yarn npm login --web-login` (Yarn 4) each complete after approval in the
  browser and write a working token into `~/.npmrc` or `~/.yarnrc.yml`; also in the `scatter`
  profile of [architecture.md](../architecture.md) R-ARCH-032, where the access logs show the
  start, the approval page's requests and the polls on all three replicas.
- After an approval, two polls of the same `doneUrl` sent at the same moment to two replicas yield
  one 200 with a token and one 404, and the user owns one new PAT.

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
- `npm logout` removes the token from `~/.npmrc`, and within 2 s the token answers 401 on every
  replica (R-AUTH-023, [architecture.md](../architecture.md) R-ARCH-018).

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
- Both request variants use the flight key of the `full` entry (R-REPO-026). Concurrent
  abbreviated and full requests for one package therefore collapse into one upstream fetch: across
  replicas through the flight lock in Valkey (INV-11, [architecture.md](../architecture.md)
  R-ARCH-033), within a replica through its single-flight while Valkey is unavailable.
- A received document MUST pass these checks before it replaces the stored one, otherwise 502
  `npm_invalid_upstream_document` and the previous entry stays: valid JSON within
  `maxMetadataBytes` (R-REPO-040); `name` equals the requested name; every `versions[v].version`
  equals `v` and every `versions[v].name` equals the name; every `dist.tarball` is an absolute
  `http` or `https` URL; every `dist.integrity` parses as SRI.
- A received document whose `time.modified` is older than that of the stored document is a stale
  copy (a lagging CDN edge or mirror). It does not replace the stored document, marks no version
  removed (R-NPM-026), renews the stored entry's freshness as a 304 would, and increments
  `trove_npm_stale_upstream_documents_total`. Without this rule, one stale answer would hide every
  version published after it until the next refresh. The stored value used for the comparison
  (`npm_remote_packages.doc_modified`) is capped at the time trove stored that document, taken from
  PostgreSQL's clock in the storing transaction, so neither a document dated in the future (a
  skewed upstream clock) nor a replica with a fast clock can block every later refresh. When either
  document lacks a parseable `time.modified`, the rule does not apply and the received document
  replaces the stored one.
- When a document is stored, the plug-in records per version the facts that tarball serving
  needs, so no tarball request parses a large document: upstream digests, upstream time, and the
  tarball URL in `remote_url_map` when it is not the upstream base plus the canonical path
  (R-REPO-043). The same pass records versions that disappeared (R-NPM-026).
- **One stored document, one set of records, also under duplicate fills.** Two fills of one
  document run at once whenever the cross-replica lock was lost in a Valkey failover or Valkey is
  unavailable (R-ARCH-033), and the fill that commits last may carry the older document. The
  records above and the removal marks of R-NPM-026 are therefore written in the transaction that
  upserts the cache entry (R-REPO-026 step 5), after it has created (`INSERT … ON CONFLICT DO
  NOTHING`) and locked the package's `npm_remote_packages` row, and only when that upsert replaces
  the stored document. The stale-copy rule above is evaluated in that same transaction, under that
  lock, against the `doc_modified` of the document stored at that moment, not against the one
  that was stored when the fetch began: a fill whose document is older than one that another fill
  committed meanwhile is a stale copy by the time it commits. When the core keeps the newer stored entry
  (R-REPO-026), or the stale-copy rule applies, the fill keeps the stored document, renews its
  freshness and changes no npm row. The stored document and its records then describe the same
  document, the newest one received, whichever fill commits last, so a lost Valkey lock costs one
  upstream request and never a tarball 404 for a version the served document lists (INV-3).
- Upstream 404 for a document enters the negative cache (R-REPO-028), which lives in Valkey
  behind a bounded per-replica L1 ([architecture.md](../architecture.md) R-ARCH-030).
- The rows of `npm_remote_packages` and `npm_remote_versions` describe the remote's stored
  documents. Where the core deletes a remote repository's cache entries instead of expiring them
  (the restore of a backup that excluded remote caches, R-BAK-008), these rows of that repository
  go with them (R-NPM-047).

Data model (sketch; `remote_cache_entries` and `remote_url_map` are repositories-and-proxy.md's):

```sql
CREATE TABLE npm_remote_packages (
  repo_id        bigint      NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
  name           text        NOT NULL,
  doc_sha256     bytea       NOT NULL,            -- digest of the stored upstream document
  doc_modified   timestamptz,                     -- its time.modified, capped at the store time
                                                  -- (PostgreSQL clock); NULL if absent or unparseable
  -- search_vector tsvector: added with R-NPM-037 (P1) as an expand step (Q-ARCH-191)
  PRIMARY KEY (repo_id, name)
);
-- "Gone from the upstream" is not an npm column: it is the core's upstream_gone_at on the package
-- document's cache entry (R-REPO-029), set and cleared by the core alone (R-NPM-026).

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
- 50 concurrent `npm view react` requests, abbreviated and full mixed, over three replicas on a
  cold cache cause one upstream request; with Valkey stopped, at most three.
- With Valkey stopped, two replicas fill `react` at the same time. The test upstream answers the
  first with a newer document and the second with an older one, delayed so that the older one
  commits last. Afterwards the stored document is the newer one, `npm_remote_versions` lists
  exactly its versions as `listed`, every tarball it lists installs, and no
  `npm.remote.version_removed` event was written. The same holds when the older answer is the
  first to arrive and the newer one commits last.
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
- **A cached tarball waits for a current document** (Q-NPM-13). A cached tarball is served only
  while its package document's cache entry is current: not expired, and validated (a 200 or 304
  from the upstream, `validated_at`) after the last restore of the installation
  (`system_state.restored_at`, [backup-and-restore.md](../backup-and-restore.md) R-BAK-022), both on
  PostgreSQL's clock. Otherwise the request first revalidates the document (a conditional request
  in one flight, R-REPO-026) and then applies the marks of R-NPM-026 to the outcome. Without this,
  a lockfile install, which never requests documents, would keep receiving a version the upstream
  has withdrawn, and after a restore the restored marks of a version withdrawn later would decide
  until somebody requested its document. When the revalidation cannot reach the upstream (circuit
  open, offline, a failure of R-REPO-029), the stored marks decide, as stale serving does. The
  check is one more indexed row in the lookup that serves the tarball, on any replica, and costs at
  most one conditional upstream request per package and metadata period.
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
- The test upstream drops `evil@1.0.1`, whose tarball is cached, and the metadata period passes.
  `npm ci` with a lockfile pinning `evil@1.0.1` (no document request) fails with 404, the test
  upstream records one conditional document request, and `npm_remote_versions` marks the version
  unlisted. Repeated within the period, the upstream records no request.

### R-NPM-026 · Versions removed upstream
Priority: **MVP** · Covers: trove-only

Public registries remove malicious versions and packages. A cache that keeps serving them after
the upstream removed them preserves exactly what the upstream took down.

- When a refreshed document no longer lists a version, the version is marked `listed = false`.
  When the upstream answers 404 or 410 for a stored document, the core marks the package
  document's cache entry `upstream_gone_at` (R-REPO-029); npm keeps no second copy of that mark.
- With `npm.serveRemovedVersions: false` (the default, Q-NPM-2), unlisted versions disappear
  from served documents and their tarballs answer 404 even if cached, also for lockfile-driven
  requests; a gone package answers 404. Both hold on every route that serves the tarball: the npm
  routes, `/files` and the cache view (R-NPM-003). With `true` they stay served (availability over
  takedown) and are marked in the cache view; this replaces the core's gone-package rule for npm,
  as R-REPO-029 provides. A version that the upstream lists again is served again
  (`listed = true`).
- A stale upstream copy (R-NPM-024, older `time.modified`) never marks a version removed.
- A build that needs a version the upstream withdrew legitimately keeps it by publishing the
  tarball to a local repository (R-NPM-036) or by setting `serveRemovedVersions: true` on that
  remote.
- Each `listed` transition writes the audit event `npm.remote.version_removed`, so operators can
  find the clients that downloaded a version later removed (download audit is R-AUTHZ-029's). The
  event is written by the transaction that changes the mark, and a mark changes only in a fill that
  replaces the stored document (R-NPM-024), so duplicate fills on several replicas write it once.
  A gone package is recorded by the core's `proxy.upstream_gone` event on the first such answer
  (R-REPO-029).
- The marks are PostgreSQL state (`npm_remote_versions.listed`, the core's `upstream_gone_at`),
  not negative-cache entries, and every replica applies them as soon as they are committed: a
  tarball request reads its version's `listed` flag and the package document's cache entry (its
  `upstream_gone_at`, `expires_at` and `validated_at`) in the lookup that serves it, and a document
  request reads that entry anyway (R-REPO-025 step 4). Neither is cached per replica or read from
  Valkey, so a loss of Valkey's data never makes a removed version servable again (INV-3). A
  restore of a backup can return the marks to its snapshot; the next tarball request revalidates
  the document first (R-NPM-025, Q-NPM-13).
- The delay between an upstream removal and its effect on documents and tarballs is bounded by the
  remote's metadata period: a merged entry never outlives the remote entry it was built from
  (R-REPO-020, R-NPM-032), and a document or tarball request after the period refreshes the
  document first (R-NPM-025). See H-33.

Configuration (`npm` section of a remote repository):

```yaml
npm:
  serveRemovedVersions: false   # default (proposed)
```

Acceptance criteria:

- The test upstream drops `evil@1.0.1` from its document; after the period, `npm view evil
  versions` through trove no longer lists it and its cached tarball answers 404, through
  `/files/npm-remote/evil/-/evil-1.0.1.tgz` as well and on every replica; the audit log has the
  event. After `valkey-cli FLUSHALL`, the tarball still answers 404. With
  `serveRemovedVersions: true` the tarball is still served.
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

Caching is R-REPO-020: the merged-metadata cache in each replica's memory, never in Valkey
([architecture.md](../architecture.md) Q-ARCH-16, Q-NPM-10), keyed by the fingerprint of the
permission-filtered member list, with an expiry no later than that of the contributing remote
entries. The core's merged-metadata cache also compares the package's `packages.rev_seq` in every
eligible local member, recorded as 0 for a member that does not hold the package
([architecture.md](../architecture.md) R-ARCH-015, one `RevSeqs` query per request), so a publish,
including the first local publish of a name that so far came only from a remote member, is
visible through the virtual on every replica as soon as it has returned, without any message
between replicas. A remote refresh with new content evicts merged entries early through the
`metadata_changed` signal over Valkey ([architecture.md](../architecture.md) R-ARCH-018); its
delivery is at most once, nothing waits for it, and without it a merged entry ends with the remote
entry it was built from. The check needs no npm code; npm's writes only have to increment the
generic column (R-NPM-006).

Acceptance criteria:

- `npm view lodash versions` through `npm-all` lists the upstream versions.
- `acme-internal@1.0.0` in `npm-local` (published by a `manage` holder, R-REPO-048),
  `acme-internal@99.0.0` in the test upstream: `npm install acme-internal` through `npm-all`
  installs `1.0.0`, for a caller who cannot read `npm-local` it answers 404 (R-REPO-023), and
  neither install causes an upstream request for the name.
- A version present in both members with different bytes installs the local bytes, and the
  conflict counter increments.
- Publishing to `npm-local` and immediately reading through `npm-all` on the other replica shows
  the new version in 100 of 100 runs, with Valkey running and with Valkey stopped.

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
drops the merged cache (R-REPO-016) on every replica through a change area of
[architecture.md](../architecture.md) R-ARCH-018, so the drop reaches replicas that miss the Valkey
message at the next change-log poll; on a remote it answers 405.

The rebuild runs on a `worker` replica (INV-2) and reads each tarball into that replica's scratch
space, outside any transaction: no transaction is open while a tarball is read from the object
store. It walks packages in `packages.id` order and writes each batch's changes in one short
transaction that also stores the last package ID of the batch in the job's `checkpoint`
([architecture.md](../architecture.md) R-ARCH-016), so the checkpoint never runs ahead of committed
work. A worker that is drained releases the job, a lost one lets its lock expire, and another
worker continues after the checkpoint; repeating a package is harmless, because the rebuild is
idempotent and increments `rev_seq` only for a package whose values changed.

Acceptance criteria:

- After the `file_count` column of all versions is cleared in a test database, a reindex restores
  them and `npm view @acme/ui dist.fileCount` is correct again.
- A reindex of 10,000 packages runs while the worker pod that holds it is deleted. The job
  finishes on the other worker with `attempt` unchanged, and the packages before the persisted
  checkpoint are not read again.

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
  R-NPM-024, also one whose package document the core marked gone, since withdrawn malware carries
  advisories; one query for the whole body). Of those it also withholds
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
- The forward holds no PostgreSQL connection while it waits: its one query runs before the
  provider is called. Its deadline is `min(npm.audit.timeout, server.shutdown_timeout − 1 s)`.
  npm never retries a `POST` (R-NPM-001), so a forward cut by its replica's shutdown would fail
  `npm audit`; with this deadline, a forward that is running when its replica starts to drain ends
  before the shutdown deadline ([architecture.md](../architecture.md) R-ARCH-024, R-NPM-046).

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
- The test provider answers after 20 s. The `web` pod that forwards an `npm audit` is deleted
  1 s after the request arrived; `npm audit` succeeds, and the kubelet events show no `SIGKILL`.

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
  `If-None-Match` and `If-Modified-Since`, so full-document rendering and 304 handling are on its
  hot path; trove answers the `If-None-Match` and ignores the date (R-NPM-013).
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
| `npm.publish` | R-NPM-007, R-NPM-036; with integrity, the dist-tag and whether the latest guard applied; outcome `retry_acknowledged` for an acknowledged retry (R-NPM-007 step 4) |
| `npm.unpublish` | R-NPM-017, per version, with its integrity. The registry entry declares `journal: revocation` (R-OPS-014), so a restore re-applies it (R-NPM-047, Q-NPM-12) |
| `npm.reinstate` | R-NPM-047: `trove admin npm reinstate` put a withheld version back, with its integrity and the journal entry that withheld it |
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
`trove_npm_audit_names_withheld_total{repository,reason}` (R-NPM-038),
`trove_npm_publish_retries_acknowledged_total{repository}` (R-NPM-007),
`trove_npm_tarball_document_revalidations_total{repository,outcome}` (R-NPM-025). Each replica exports its
own counts; dashboards sum them over the replicas of a role. Access logs record `npm-command` and
`npm-session`.

Acceptance criteria:

- Each acceptance test that performs one of the operations above finds exactly one matching audit
  event, whichever replica served it.

### R-NPM-045 · Client acceptance matrix
Priority: **MVP** · Covers: FMT-002, FMT-003

The npm format is accepted when the acceptance criteria of this page pass end to end with real
clients against the Helm chart with its role split (three `web` and two `worker` replicas,
[ha-and-helm.md](../ha-and-helm.md) R-HA-002, Q-HA-16), PostgreSQL, Valkey with Sentinel in the
reference topology of R-HA-056, an S3-compatible store and a deterministic test upstream registry.
The edge runs in the `scatter` profile of [architecture.md](../architecture.md) R-ARCH-032, so
consecutive requests of one client reach different `web` replicas (INV-18). A green full run of
Test and Release on the commit that closes the phase repeats the scenario list with Valkey stopped (R-NPM-046); the node-drain run of R-HA-059 carries npm load
of its own.

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
  The pin is the npm alias of the row in `test/e2e/clients/package.json` with its
  `package-lock.json` (Q-NPM-44); all eight clients run on the one Node.js of `web/.nvmrc`, which
  every row's `engines` accepts (Q-NPM-45). Renovate raises the aliases within their major and never
  opens a major; changing the rows is a deliberate edit of the table above and of that file.
- The test upstream is a stub registry with recorded documents and tarballs and switches for the
  failure cases of R-NPM-024 to R-NPM-026 and R-NPM-033; the full run of Test and Release on the commit that closes the phase runs the read-only subset
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

- The CI job `e2e-npm` runs the scenario list for every row in the `scatter` profile and fails the
  build on any failure. Its run with Valkey stopped, in the full run of Test and Release on the commit that closes the phase, does the same.

## Replicas, Valkey and backups

### R-NPM-046 · npm across replicas: no state between requests, Valkey degradation, drain
Priority: **MVP** · Covers: trove-only

The Kubernetes Service sends each request to any `web` replica, without session affinity
(INV-12, INV-18, [architecture.md](../architecture.md) R-ARCH-032). Every npm flow that spans
requests keeps its state in PostgreSQL or in the request itself:

| Flow | Requests | State between the requests | Where it lives |
|---|---|---|---|
| Publish, then install in the next CI step | `PUT {name}`, then document and tarball `GET`s | The new version | The commit; every document request reads `packages.rev_seq` (R-NPM-010, R-NPM-030) |
| Read-modify-write: unpublish, deprecate, `-rev` writes | `GET {name}?write=true`, then `PUT` or `DELETE …/-rev/{rev}`; an unpublish also reads again and deletes the tarball | `_rev` | `packages.rev_seq`, read inside each request (R-NPM-009) |
| A publish retried after a lost answer | The same `PUT` twice | Whether the first attempt committed | The version's publisher, publish time, integrity, provenance digest and `publish_tag` (R-NPM-007 step 4) |
| A revisioned write retried after a lost answer | The same `PUT` or `DELETE …/-rev/{rev}` twice | Whether its effect is in force | The package rows, compared inside the retry (R-NPM-009) |
| Conditional revalidation | `GET` with `If-None-Match` | The validator | Computed from PostgreSQL and configuration, the same on every replica; dates are not used (R-NPM-013) |
| Lockfile install (`npm ci`) | Parallel tarball `GET`s without documents | Expected digests, upstream URLs, removal marks, document freshness | `npm_remote_versions` and the cache entries (R-NPM-024 to R-NPM-026) |
| Remote miss from many clients | Concurrent `GET`s on several replicas | The running upstream fetch | The flight lock in Valkey; the entry in PostgreSQL (R-NPM-024) |
| Trusted publishing (P1) | Exchange `POST`, then the publish `PUT` | The issued token | Token row (INV-8, R-NPM-022) |
| Web login (P1) | Start `POST`, approval page requests, polls `GET /auth/cli/…` | The login | `cli_login` row (R-NPM-020, R-AUTH-034) |
| Search pages (P1) | `GET -/v1/search?from=…` | The offset | The request (`from`) |
| Legacy login, logout, whoami, ping, audit | One request each | — | — |

Rules:

- The npm plug-in keeps no per-replica state of its own. Its documents are cached only in the
  core's in-process caches (the rendition cache and the merged-metadata cache, R-ARCH-018), under
  keys every replica reads from PostgreSQL (Q-NPM-10). Its only local files are the spool of the
  request or job that is running, on the pod's `emptyDir` or generic ephemeral volume (INV-13); no
  npm state needs a persistent or shared volume. A new kind of per-replica state needs a row in
  R-ARCH-032's table first.
- No npm request holds a PostgreSQL connection or transaction while it receives or sends a body,
  or waits for an upstream, an advisory provider or Valkey. The publish body is spooled and checked
  before step 5 of R-NPM-007 opens its transaction; shadowing probes run outside it
  ([architecture.md](../architecture.md) R-ARCH-014); remote fetches follow R-ARCH-013; audit
  forwarding runs its one query first (R-NPM-038). npm code uses no PostgreSQL session state
  (format packages do not import the driver, R-ARCH-002), so npm runs unchanged behind the
  transaction-mode pooler of [ha-and-helm.md](../ha-and-helm.md) R-HA-058.
- npm uses Valkey only through the core: the flight lock for remote documents and tarballs, the
  negative cache for upstream 404s, the `metadata_changed` signal that evicts merged entries
  early, and the cluster-scoped limiters of the web-login start (R-AUTH-038) and the token
  exchanges (R-CI-021). No document, tarball URL, integrity value or authorization result that npm
  serves is read from Valkey (Q-ARCH-16). While Valkey is unavailable, every npm operation works;
  what changes is:

  | Mechanism | With Valkey | Without Valkey (degraded, R-ARCH-030) |
  |---|---|---|
  | Remote document or tarball miss | One upstream fetch per cluster and flight | At most one per replica and flight |
  | Upstream 404 of a document | Remembered cluster-wide for `notFoundTtlSeconds` | Remembered per replica (L1) |
  | Merged document after a remote refresh | Evicted on every replica within milliseconds | Rebuilt when the merged entry expires, no later than the remote entry it was built from (R-REPO-020) |
  | Rendered or merged document after a local write | Exact on every replica through `rev_seq` | The same |
  | Removal marks and takedowns (R-NPM-026) | Read from PostgreSQL by every request | The same |
  | Web-login start and exchange limits | Cluster-wide | A per-replica share of the limit (R-ARCH-034) |

- **Valkey loses its data** (a Sentinel failover that drops acknowledged writes, a flush, a
  restart; R-ARCH-030). npm sees what a short outage shows: a flight lock that vanished lets a
  second replica fetch the same document or tarball, which R-NPM-024's duplicate-fill rule and
  content addressing make harmless; negative entries are gone, so the upstream is asked again;
  limiter windows restart, which allows at most one extra window of web-login starts and
  exchanges. No npm row, mark or document is lost. npm holds no Valkey connection of its own; the
  core finds the new primary through Sentinel and follows the failover (R-ARCH-031).
- **Node drain** ([ha-and-helm.md](../ha-and-helm.md) R-HA-059). Each npm request either ends
  within the shutdown timeout or is safe to repeat on another replica:
  - document and tarball `GET`s are repeated by the clients' own retries (npm and pnpm
    `fetch-retries`; Yarn 2+ `httpRetry`, default 3, [Yarn settings](https://yarnpkg.com/configuration/yarnrc));
    npm repeats the whole request rather than resuming with `Range` (R-ARCH-024), which any
    replica serves. A remote fetch that the draining replica leads keeps running and commits, or
    is released with `failed` 5 s before the deadline so a waiter on another replica takes over
    (R-REPO-026). Yarn 1's retry behavior was not verified; the drain test of R-HA-059 runs npm;
  - a publish whose body has arrived is not cut between its commit and its status line
    (R-ARCH-024), and a retry of a publish whose answer was lost elsewhere is acknowledged
    (R-NPM-007 step 4, Q-NPM-11). A revisioned unpublish or deprecation whose answer was lost
    succeeds on its retry when its effect is in force (R-NPM-009). Dist-tag writes, legacy login
    and logout are idempotent. Two cases remain in which an answer lost outside trove makes the
    client report a failure, and running the command again succeeds: a dist-tag removal, whose
    retry answers 404 because the tag is gone (`npm dist-tag ls` shows the result), and a `POST`
    that npm never retries (the web-login start and the trusted-publishing exchange, both P1);
  - the audit forward ends before the shutdown deadline (R-NPM-038), because npm never retries a
    `POST`;
  - a web-login poll (P1) is a short read of `cli_login` and a `GET`, which npm's fetch layer
    retries after a connection error. A poll that minted the token cannot be answered again,
    because the secret is never stored (R-AUTH-034): when its answer is lost outside trove, the
    retry gets 404, npm falls back to the legacy prompt, where a PAT can be entered (R-NPM-021),
    and the minted token stays unused until it expires or is revoked;
  - the reindex job (P1) is released with its checkpoint and continues on another `worker`
    replica (R-NPM-035).
- **Web pools** ([ha-and-helm.md](../ha-and-helm.md) R-HA-062, P1). A pool for `/api/npm/` serves
  every registry URL. The host-root exchange prefix `/-/npm/v1/oidc/token/exchange/package/`
  (R-NPM-022) and the web-login poll under `/auth/cli/` (R-NPM-020) stay on the main `web`
  Deployment unless the pool lists them. Every `web` replica serves every route (INV-2), so both
  placements work.

Acceptance criteria:

- In the `scatter` profile (three `web` replicas, round robin per request, upstream keep-alive
  off), the scenario list of R-NPM-045 passes for every gate row.
- The same run with Valkey stopped passes, every replica's `/readyz` answers `200` with
  `"degraded":["valkey"]`, and the test upstream records at most one request per replica for each
  document or tarball the run fetches.
- In the `scatter` profile, `npm unpublish @acme/ui@1.0.0` succeeds, and the access logs show its
  requests on three different replicas.
- `curl -sI …/npm-all/@acme%2fui` through replica A returns ETag `E`; the request with
  `If-None-Match: E` through B and through C answers `304`. After a publish of `@acme/ui` through
  C, the same conditional request through A answers `200` with a new ETag. The same holds for
  `…/npm-local/@acme%2fui` and for `…/npm-remote/lodash` after an upstream change.
- A test build of the plug-in with `npmRenderVersion` incremented serves next to the current
  build: a conditional request with an ETag from one build, answered by the other, answers `200`.
- With the pooler profile of R-HA-058, the scenario list of R-NPM-045 passes for the npm 11 gate
  row.
- While a 200 MiB `npm publish` arrives over a link limited to 10 MiB/s, `pg_stat_activity` shows
  no session of the receiving replica in state `idle in transaction`.
- `valkey-cli FLUSHALL` during the scenario list, and a Sentinel failover forced with
  `SENTINEL FAILOVER` during another run, fail no client operation.
- A fault-injecting edge drops the answer of a committed `npm unpublish` `PUT`; npm's retry reaches
  another replica and `npm unpublish` exits 0.

### R-NPM-047 · npm data in backups and restores
Priority: **MVP** · Covers: trove-only

A backup holds a transactionally consistent snapshot of PostgreSQL and every blob it references
(INV-19, [backup-and-restore.md](../backup-and-restore.md)). For npm this means:

- **Export classes** (R-BAK-004). `npm_packages`, `npm_versions`, `npm_tombstones`,
  `npm_remote_packages` and `npm_remote_versions` are all `data`. None holds transient state.
  `npm_tombstones` in particular must come back with every restore, because it is what refuses a
  reused version number (R-NPM-008).
- **Revision columns** (R-BAK-022). npm keeps no counter of its own. `_rev`, document ETags and the
  merge fingerprint derive from `packages.rev_seq` and `repositories.config_version`, both
  registered revision columns, so no `_rev` or ETag of the old installation matches a restored
  document (R-NPM-013). npm adds no entry to the registry.
- **Valkey.** Nothing npm needs after a restore is in Valkey; the restored installation starts
  with an empty keyspace (R-ARCH-030).
- **A restore returns npm local repositories to the backup's `snapshotAt`:**
  - versions published after it are gone, and their numbers can be published again. A client
    whose lockfile pinned such a version fails with `EINTEGRITY` if other bytes are published
    under that number; it never installs them silently;
  - unpublishes made after it are re-applied by the journal replay of
    [ha-and-helm.md](../ha-and-helm.md) R-HA-054, which every restore runs
    ([backup-and-restore.md](../backup-and-restore.md) R-BAK-024; Q-NPM-12). The `npm.unpublish`
    entry carries the repository ID, the package name, the version, its integrity and the actor's
    ID, and no secret. Replay **withholds** the version: when the restored repository holds it live
    with that integrity, replay writes its tombstone (`source = 'journal'`), applies the dist-tag
    rule of R-NPM-016 and increments `packages.rev_seq`, in one transaction (`applied`). The items
    and their bytes stay stored, but no route serves them: documents omit the version, the
    tarball route and `/files` answer 404 for it, and no virtual repository takes the version from
    this member (R-NPM-003, R-NPM-015).
    A tombstoned version is `already in effect`; a version the restored repository does not hold,
    or holds with another integrity, is `subject unknown`, and nothing changes. Replay deletes
    nothing, neither permanently nor to the trash can, so a forged or mistaken entry costs
    availability until an administrator acts, never integrity, access or data. A replayed deletion
    would be irreversible in the MVP, because the trash can is P1 ([storage.md](../storage.md)
    R-STOR-022, R-STOR-023);
  - the replay output lists every withheld version. An administrator confirms a withdrawal by
    deleting the version's items through the management item API (R-STOR-018; permanently in the
    MVP, to the trash can from P1; the tombstone stays), or reverses it with `trove admin npm reinstate <repoKey> <name>@<version>`. The
    reinstate removes a `journal` tombstone only while the version is stored with the tombstone's
    integrity, so it brings back the same bytes and never admits new ones (Q-NPM-8); it increments
    `rev_seq` and writes `npm.reinstate` (R-NPM-044). A withheld version counts toward the
    repository's storage like a live one;
  - dist-tag moves and deprecations made after it are undone (H-51).
- **A restore returns npm remote repositories to the snapshot as well**, removal marks included. A
  version the upstream withdrew after the snapshot is `listed` again in the restored rows. Its
  cached tarball is not served on that basis: every package document validated before
  `system_state.restored_at` counts as not current, so the first tarball request for the package
  revalidates the document and re-applies the marks (R-NPM-025, Q-NPM-13). While the upstream
  cannot be reached, the restored marks decide (H-51).
- **Remote caches left out of a backup** (R-BAK-008). The npm rows of a remote repository describe
  its stored documents (R-NPM-024). The restore step that deletes a remote repository's cache
  entries also deletes that repository's `npm_remote_packages` rows, and their
  `npm_remote_versions` rows cascade. The first request for a package then fetches its document as
  on an empty remote (R-NPM-025), and audit forwarding withholds a name until the restored remote
  has fetched it again (R-NPM-038).

Acceptance criteria:

- `@acme/ui@1.4.0` is published, a backup is created, `npm unpublish @acme/ui@1.4.0` runs, and
  `backup.revocation_journal.ship_interval` passes. After the restore of that backup with its
  journal replay, `npm view @acme/ui versions` does not list `1.4.0`, its tarball answers 404 on
  the npm route, through `npm-all` and on `/files/npm-local/@acme/ui/-/ui-1.4.0.tgz`, publishing
  `1.4.0` again answers 409 `npm_version_unpublished`, and the replay output lists the entry as
  `applied`. `trove admin npm reinstate npm-local @acme/ui@1.4.0` brings the version back with its
  original integrity.
- A hand-written `npm.unpublish` journal object for a live version with its correct integrity is
  replayed: the version is withheld, nothing is deleted, and a reinstate restores it.
- `evil@1.0.1` is cached in `npm-remote`, a backup is created, and the test upstream then drops the
  version. After the restore, `npm ci` with a lockfile pinning `evil@1.0.1` fails with 404, and the
  test upstream records one document request.
- With `backup.exclude.remote_caches: true`, `npm_remote_packages` holds no row of `npm-remote`
  after the restore, and `npm ci` of a lockfile through `npm-remote` succeeds with one document
  request per package to the test upstream.
- R-BAK-022's criterion (an ETag kept from the old installation answers `200` after the restore
  and one publish) passes for a full, an abbreviated and a merged document.

## Endpoints

### R-NPM-048 · npm documents on an endpoint with exposure rules
Priority: **MVP** · Covers: trove-only

The npm plug-in implements the exposure adapter of [endpoints.md](../endpoints.md) R-EP-018. On an
endpoint with `exposure: rules`, after the merge of a virtual repository and the rewrite of a remote
document and before compression:

- **Versions.** `versions` and `time` of the full and the abbreviated document list only the
  versions the endpoint exposes (R-EP-012). A package without an exposed version answers 404, like
  an unknown package. `time.created` and `time.modified` keep the values of the unfiltered document,
  because `modified` must never move backwards for a downstream proxy (R-NPM-012); what that reveals
  is [endpoints.md](../endpoints.md) H-60.
- **Dist-tags.** A tag other than `latest` is kept when the rules admit its name and the version it
  points to is exposed, and dropped otherwise; it is never moved to another version. `latest` is not
  subject to `distTags`: it keeps its version when that version is exposed, and otherwise moves to
  the highest exposed version that is not a pre-release, or to the highest exposed version when all
  are pre-releases, the rule of R-NPM-016.
- **Top-level fields** (`description`, `readme`, `keywords`, `license`, `homepage`, `repository`,
  `bugs`, `author`) come from the version the filtered `latest` points to (R-NPM-010);
  `maintainers` lists only the publishers of exposed versions.
- **Other documents.** `GET -/package/{name}/dist-tags` returns the filtered tags. The
  single-version document (R-NPM-014, P1) answers 404 for a hidden version or a dropped tag. Search
  (R-NPM-037, P1) returns only exposed packages, with the filtered `latest` as their version.
- **Tarballs** of hidden versions answer the 404 of R-EP-013; the version is the one R-NPM-015
  parses from the file name.
- **No writes.** `?write=true`, publish, dist-tag writes, deprecation and unpublish answer 403
  `endpoint_read_only` (R-EP-013).
- **Upstream contact.** On an endpoint with `cache_fill: false` (R-EP-019), the audit endpoint
  (R-NPM-038, P1) answers 501 `npm_audit_unavailable`, as in mode `disabled`, because forwarding
  would contact an upstream.
- **Validators.** The ETag of R-NPM-013 includes the endpoint and its exposure fingerprint, so a
  rule change makes every cached copy of the document fail revalidation.
- **Version ranges** in rules use node-semver range syntax; `ValidateRange` refuses what npm's own
  range parser refuses, and pre-releases follow `prereleases`, not the range's pre-release rule.

Acceptance criteria (fixture of [endpoints.md](../endpoints.md) R-EP-012, with `latest` on 2.1.0 and
`next` on 2.1.0-rc.1 in `npm-internal`):

- On `customers`, `npm view @acme/sdk versions` prints `[ '2.0.0', '2.1.0' ]`,
  `npm view @acme/sdk dist-tags` prints `{ latest: '2.1.0' }`, and the abbreviated document lists the
  same versions; through `default` every version and both tags are listed.
- After `npm dist-tag add @acme/sdk@1.9.0 latest` on `default`, the document on `customers` has
  `latest: '2.1.0'`, the highest exposed release, and `npm install @acme/sdk` there installs 2.1.0.
- `npm install @acme/sdk@1.9.0` on `customers` fails with `E404`, and `npm ci` with a lockfile
  that resolves 2.0.0 succeeds with npm, pnpm and both Yarn lines of R-NPM-045.
- After a rule update that hides 2.0.0, a conditional request with the earlier ETag on any replica
  answers 200 with a document without 2.0.0.

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
  (R-NPM-026), on every route including `/files`, so a pull-through cache does not keep
  distributing malware the upstream withdrew. A cached tarball waits for a current document once
  the metadata period has passed or a restore happened (R-NPM-025, Q-NPM-13), so lockfile installs
  and restored marks do not bypass a takedown while the upstream is reachable.
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
- **Valkey stays outside npm's trust path.** No document, tarball URL, integrity value or
  authorization result that npm serves is read from Valkey (R-NPM-046, Q-NPM-10,
  [architecture.md](../architecture.md) Q-ARCH-16). Whoever can write to Valkey can at most hide a
  remote package for a negative-cache period (H-49) or cause extra upstream requests; takedown
  marks live in PostgreSQL (R-NPM-026).
- **Validators across replicas.** ETags are computed from shared inputs plus the plug-in's
  rendering version (R-NPM-013), so no replica answers 304 for bytes it would not serve, and
  replicas of two releases never share a validator during a rolling upgrade. Dates never validate
  a package document, so a restore cannot turn a client's old `If-Modified-Since` into a 304.
- **Acknowledged publish retries change nothing.** The retry rule of R-NPM-007 step 4 answers 201
  only for the same principal, identical bytes and the same dist-tag, inside a short window, and
  writes no row, so it cannot be used to replace a version or move a tag (Q-NPM-11). A stale
  revision succeeds only for a request that changes nothing (R-NPM-009), so it cannot overwrite a
  concurrent change.
- **Takedowns and restores.** Unpublishes made after a backup's snapshot are re-applied from the
  journal after a restore by withholding the version, which deletes nothing, so a forged journal
  entry cannot destroy restored content (R-NPM-047, Q-NPM-12). Upstream withdrawals made after the
  snapshot are re-checked against the upstream before a cached tarball is served (Q-NPM-13). What a
  restore still reverts is H-51.

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

Revisited on 2026-10-28 after the owner added the Valkey, horizontal-scale, backup and storage-class constraints: the choice is unchanged. Its premise, that hiding reaches lockfile installs, now holds on n replicas and across a restore: every request reads the marks from PostgreSQL, the gone mark is the core's single `upstream_gone_at`, the stale-copy cap uses PostgreSQL's clock, and Q-NPM-13 makes a cached tarball wait for a current document.

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
lists the new metric. Since 2026-10-28: R-NPM-024 caps the stored value at the store time on
PostgreSQL's clock and evaluates the rule inside the storing transaction; R-NPM-026 reads the marks
per request, uses the core's gone mark instead of an npm column, and applies them on `/files` and
the cache view; R-NPM-025 and R-NPM-047 carry Q-NPM-13.

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
job's wall time per release becomes the bottleneck; the middle pnpm major would then move out of the
per-change run first.

**Applied in the spec:** R-NPM-045 rewritten with the rule, today's rows and their reasons, pin
maintenance, and per-row "not applicable" marking. R-NPM-004, R-NPM-007 and R-NPM-040 refer to the
gate rows instead of single versions.

### Q-NPM-5 · Upstream representation for remote repositories

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Revisited on 2026-10-28 after the owner added the Valkey, horizontal-scale, backup and storage-class constraints: the choice is unchanged and gains two arguments. One stored representation means one flight per package across replicas, because both request variants collapse into the `full` entry, and one set of npm records that a duplicate fill under a lost Valkey lock cannot split from the stored document. With (b), two entries refreshed by different replicas at different times could disagree, and consecutive requests of one client, which now reach different replicas, would see both views. R-NPM-024 states both rules.

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
`_npmUser`. Since 2026-10-28, R-NPM-024 also states that both variants use the flight key of the
`full` entry, and that the npm records and removal marks are written in the cache-entry
transaction, only when it replaces the stored document, with the stale-copy rule evaluated in that
transaction against the document stored at that moment; R-NPM-026 writes its audit event once.

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

Revisited on 2026-10-28 after the owner added the Valkey, horizontal-scale, backup and storage-class constraints: the choice is unchanged. A restore of a backup (INV-19) returns `npm_tombstones` to the backup's snapshot and would so remove every tombstone written after it, which is exactly the purge this decision rules out. Q-NPM-12 re-applies those unpublishes through the journal after a restore; H-51 names what a restore still reverts.

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
tombstone and that a republish of identical bytes is refused. Since 2026-10-28, R-NPM-008 also
states how a backup restore treats tombstones and points to R-NPM-047. The acknowledged retry of
Q-NPM-11 is not a republish: it applies only to a live version, never to a tombstone, and changes
nothing. `trove admin npm reinstate` (R-NPM-047) is not a purge either: it removes only a tombstone
that a restore's replay wrote for a version that is still stored with that integrity, and so
brings back the same bytes, as a trash-can restore does. R-NPM-008 marks the trash-can restore as
P1, because the trash can is (R-STOR-022).

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

### Q-NPM-10 · Where npm documents are cached when n replicas serve them

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Requests reach `web` replicas at random, and the load needs several of them (INV-12, INV-18). npm
documents are the most expensive responses trove renders: the full document of a large upstream
package is tens of megabytes (`next`: 31.5 MB unpacked, measured for Q-NPM-5), and Yarn 2+
revalidates full documents on every install (R-NPM-042). INV-3 lists shared caches among Valkey's
uses; [architecture.md](../architecture.md) Q-ARCH-16 decided for every format that rendered and
merged documents stay in process. This block records the npm check of that line.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Per replica, self-validating keys | Rendered local documents, abbreviated forms of remote documents and merged documents live in each replica's rendition and merged-metadata caches, keyed by `rev_seq`, stored document digest and merge fingerprint; ETags are computed from the same inputs | Exact on every replica without any message; a revalidation (`304`) costs one indexed query on any replica and no rendering; Valkey's content cannot change what a client installs | Each replica renders each package revision once, so n renders per revision; caches are cold after a rollout |
| (b) Shared second level in Valkey | Rendered and merged documents stored in Valkey and read by every replica | One render per revision per cluster | Whoever can write to Valkey could serve a changed `dist.tarball` or `integrity` (ruled out by Q-ARCH-16); values of megabytes against R-ARCH-030's 1 KiB limit and Valkey's network; losing Valkey turns into a render burst on every replica at once |
| (c) Renderings in object storage | Each rendering stored once in S3 under repository, package, revision, variant and settings, read by every replica | Shared, in trove's own store, survives restarts | One object write per publish and refresh, plus garbage collection of superseded renderings; an S3 GET costs tens of milliseconds, about what rendering a typical document costs; a second copy of what the rows already say |

**Choice:** (a) — npm documents are cached per replica in process, under keys that every replica
reads from PostgreSQL, and the ETag is computed from the same inputs.

**Rationale:**
- Random distribution hurts a cache only where its key depends on the replica. Under (a) none
  does: each request reads `rev_seq` or the entry's digest from PostgreSQL, so every replica gives
  the same answer and the same validator, with or without Valkey.
- The cost of n renders is small at npm's write rates. An internal package published every 10 s,
  with ten `web` replicas, costs at most one render per replica per publish, and R-NPM-010's target
  is 200 ms for 1,000 versions. A remote document is rendered again per replica only after a
  refresh, by default every 7,200 s.
- Repeat installs mostly revalidate, and the ETag needs no rendering (R-NPM-013). (b) and (c) would
  mostly save work that (a) does not do.
- (b) would put Valkey into the supply chain, which Q-ARCH-16 rules out. (c) is the only shared
  tier that could be acceptable, and it buys little until rendering shows up in profiles.
- The development agent would revisit this if the reference load test (R-HA-050) shows document
  rendering (P-4, P-5 of R-ARCH-027) as the limit on scale-out, or cold caches after a rollout as a
  latency problem; (c), with the digest of each rendering checked on read, would then be the next
  step.

**Applied in the spec:** R-NPM-010 and R-NPM-030 name the in-process caches and drop the
notification wording; R-NPM-010 stores the README at publish, so no rendering reads the object
store. R-NPM-013 requires the same validator on every replica, defines its inputs, including
`npmRenderVersion` and the merge fingerprint, keys the caches by the same tuple, and stops dates
from validating a document: a date is the one validator input that a restore moves backwards and
that settings and member changes leave unchanged. R-NPM-012 takes local times from PostgreSQL's
clock after the row lock. R-NPM-009 derives the virtual `_rev` from that fingerprint. R-NPM-046
lists these caches as npm's only per-replica state. The Security notes follow.

### Q-NPM-11 · A publish retried after its answer was lost

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

npm's fetch layer retries every request other than a `POST` after a connection error and on 408,
420, 429 and any 5xx ([make-fetch-happen `lib/remote.js`](https://github.com/npm/make-fetch-happen/blob/main/lib/remote.js),
read on 2026-10-28). A publish `PUT` whose answer is lost is therefore sent again, and the Service
delivers the retry to any replica. [architecture.md](../architecture.md) R-ARCH-024 keeps trove's
own shutdown from cutting a publish between its commit and its status line. An answer can still be
lost after trove has sent it: at an edge proxy instance on a drained node, through a reset between
edge and client, or by npm's `fetch-timeout` on a large publish whose commit completed. R-NPM-008
then answered the retry with 409, although the publish had succeeded.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Always 409 | Rely on R-ARCH-024 for the cuts trove makes itself | Simplest; every 409 means "this version existed before you asked" | A publish that succeeded fails its CI job when the answer is lost outside trove; the rerun fails with 409 again and needs a version bump |
| (b) Acknowledge an identical retry | Same principal, identical tarball and provenance bundle, same dist-tag, within `npm.publish.retry_window` (10 min) on PostgreSQL's clock: 201 with an `npm-notice`, nothing changed | A lost answer costs nothing; the decision reads PostgreSQL only, so any replica takes it alike (INV-18); no state changes, so R-NPM-008's guarantee stands | A rerun of the same pipeline within the window, with a reproducible build, succeeds instead of failing with 409; one more column (`publish_tag`) |
| (c) Acknowledge any identical republish | No principal or time condition | Reruns become idempotent too | Removes the loud signal that a version already exists; another principal would "publish" what someone else released |
| (d) Idempotency key from a request header | Match retries by `npm-session` | Exact for npm | Not verified that pnpm 11 and 12 or Yarn 4 send it; R-NPM-005 keeps `npm-*` headers out of decisions; a header is only the client's claim |

**Choice:** (b) — a retry by the same principal with identical bytes and the same dist-tag within
`npm.publish.retry_window` is answered 201 with an `npm-notice` and changes nothing.

**Rationale:**
- The owner requires drains without outage, and a publish reported as failed although it
  succeeded is an outage for the pipeline that ran it. R-ARCH-024 closes the case trove causes; (b)
  closes the rest without touching integrity.
- Every fact (b) reads (publisher, `published_at`, integrity, the stored provenance digest,
  `publish_tag`) was committed with the original, and the window is measured on PostgreSQL's clock,
  so the replica that receives the retry decides as any other would. The same check runs again
  under the package row lock, so a retry that overtakes its slow original ends in 201 as well.
- The dist-tag is part of the identity because a 201 that says "nothing changed" must not answer a
  request that asked for a different tag: `npm publish --tag next` of bytes just published as
  `latest` is not a retry, and answering it 201 would let automation believe `next` points to the
  version. It answers 409 as before.
- The same lost answer hits the revisioned writes of unpublish and deprecation, which npm retries
  too. There the retry is recognised by its effect, without a window: a stale `{rev}` whose request
  would change nothing succeeds (R-NPM-009). It cannot lose a concurrent change, because it makes
  none.
- (b) writes nothing: no version, no tag, no `rev_seq`, no object. It cannot replace bytes, move a
  tag or revive a tombstoned version, because it applies to live versions only. Lockfiles and
  Q-NPM-8 are untouched.
- The window covers npm's retry schedule (two retries, after 10 s and 60 s, each attempt bounded by
  the five-minute `fetch-timeout`) with margin, and keeps a rerun hours later loud. (c) gives that
  up. (d) depends on a header the other clients may not send.
- The development agent would revisit this if `trove_npm_publish_retries_acknowledged_total` shows
  acknowledgements long after the original (reruns rather than retries), which would argue for a
  shorter window, or if a client is found that retries a publish with a changed body.

**Applied in the spec:** R-NPM-007 step 4 (the rule and its five conditions), step 5 (the
re-check under the lock, `publish_tag`), step 6 (the reference to R-ARCH-024), the setting
`npm.publish.retry_window` and an acceptance criterion; R-NPM-006 (`npm_versions.publish_tag`);
R-NPM-008 (a retry is not a second publish; its acceptance criterion is qualified); R-NPM-009,
R-NPM-017 and R-NPM-018 (a stale revision that changes nothing succeeds); R-NPM-005 (the
`npm-notice` list); R-NPM-044 (outcome `retry_acknowledged` and its metric); R-NPM-046; the
Security notes. Cross-page: [architecture.md](../architecture.md) R-ARCH-024 step 5.

### Q-NPM-12 · Unpublishes made after a restored backup's snapshot

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

INV-19 makes backup and restore a core function. A restore returns every table to the backup's
`snapshotAt`, `npm_tombstones` included, so an unpublish made between the snapshot and the loss of
the old database is undone: the version is live again and its tombstone is gone. Unpublish is how
a version that leaked a secret or carried malicious code is withdrawn, and with a daily backup the
window is up to a day. For access removals, the revocation journal of
[ha-and-helm.md](../ha-and-helm.md) R-HA-054 solves the same problem, and it takes further event
types through a flag in the event registry (R-OPS-014). R-HA-054 keeps a mistaken or forged entry
reversible: a deleted user is disabled on replay, not deleted. The trash can is P1
([storage.md](../storage.md) R-STOR-022), so in the MVP every deletion is permanent.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Document, repeat by hand | A gap item; operators repeat the unpublishes from the audit log stream | No new mechanism | A manual step after a disaster, easy to miss; until it runs, the withdrawn version installs again |
| (b) Journal `npm.unpublish`, replay deletes | The event carries `journal: revocation`; replay deletes the version and tombstones it when the restored repository holds it live with the journaled integrity | Automatic on both restore paths; the restored installation holds exactly what the old one held; one row in R-HA-054 | Without the trash can (MVP) the deletion is permanent, so a forged or mistaken entry destroys a version that the backup had restored, with no way back short of another restore. Against R-HA-054's reversibility rule |
| (c) Journal `npm.unpublish`, replay withholds | As (b), but replay only tombstones the version: its items stay stored and no route serves them; `trove admin npm reinstate` reverses it, deleting the items confirms it | Automatic on both restore paths; replay deletes nothing, so a forged or mistaken entry is reversible in the MVP as well, as R-HA-054 requires; a withdrawn version stops installing at once | A withheld version occupies storage until an administrator deletes it; one admin subcommand; `/files` must apply npm's tombstone (R-NPM-003); the flag's definition widens from access removals to content withdrawals; dist-tag moves and deprecations stay reverted (H-51) |
| (d) Journal every npm change | Publishes, dist-tag moves and deprecations too, replayed after the restore | Brings back the whole npm state of the lost interval | Replaying a publish adds content, which a replay must never do; blobs published after the snapshot are not in the backup; the journal would become a second system of record |

**Choice:** (c) — `npm.unpublish` is journaled, and replay withholds a restored version that is
live with the journaled integrity: it tombstones the version, deletes nothing, and an administrator
confirms or reverses each withdrawal.

**Rationale:**
- A withdrawn version is what a restore must not bring back. (c) keeps that property without a
  manual step, with a mechanism both restore paths already run: from the replay on, no client
  installs the version, on any route.
- Replay stays inside the journal's rules. The integrity check ties an entry to one build, a
  version the restored database does not hold is left alone, and nothing is deleted, so a mistaken
  or forged entry costs availability until an administrator reinstates the version, never data.
  (b) would need the trash can to undo a forged entry, but the trash can is P1; in the MVP (b)
  would let whoever can write journal objects with the runtime S3 credential delete restored
  versions for good, in the one procedure meant to recover from damage.
- The reinstate does not reopen Q-NPM-8: it brings back the bytes the tombstone names, which are
  still stored, as a trash-can restore does, and never admits other bytes.
- (d) would make replay add content the backup does not hold. (a) leaves a security property to an
  operator's memory after a disaster.
- Tombstone-only entries for versions the restored database does not hold were considered and left
  out. A forged entry could then burn version numbers for good, while the case they cover (a
  version published and unpublished after the snapshot) is narrow and fails loudly with
  `EINTEGRITY` if its number is reused.
- [formats/nuget.md](nuget.md) Q-NUGET-10 answers the same question for NuGet, and both replays
  delete nothing in the MVP for the same reason: the trash can is P1 ([storage.md](../storage.md)
  R-STOR-022, R-STOR-023), so a replayed deletion could not be undone. npm's replay therefore never
  deletes a version or moves it to the trash before this decision is revisited. The two formats
  still differ in two points. For a version the restored repository holds live, npm withholds it
  (no route serves it until an administrator reinstates or deletes it), while NuGet keeps serving
  it and reports it `revived`. NuGet reserves the numbers of versions lost with the snapshot for
  their journaled bytes, which npm leaves out (previous bullet). npm withholds because unpublish is
  npm's path for withdrawing a leaked secret or malicious code, and a withheld version is as
  reversible as a revived one. One rule for both formats is an item for the owner's walkthrough;
  until it is settled, each page states its own rule.
- The development agent would revisit this if restore drills or incident reports show dist-tag
  moves or deprecations used as withdrawals (H-51); `npm.dist_tag.set` would then join the journal
  with an update rule. It would also revisit the withholding once the trash can (P1) is on by
  default, where replay could move the version to the trash instead, which a trash restore
  reverses; NuGet's Q-NUGET-10 waits for the same point. It would revisit both points above if the
  walkthrough settles one rule for both formats.

**Applied in the spec:** R-NPM-047 (entry content, the withholding replay, `trove admin npm
reinstate`, acceptance criteria); R-NPM-006 (`npm_tombstones.source`); R-NPM-003 and R-NPM-015
(no route serves a tombstoned version, `/files` included); R-NPM-044 (the flag on `npm.unpublish`,
the event `npm.reinstate`); R-NPM-008 (the replay deletes nothing); the revisit of Q-NPM-8; H-51;
the Security notes. Cross-page: [formats/nuget.md](nuget.md) Q-NUGET-10 (the same question for
NuGet, one rule for both formats at the walkthrough), R-OPS-014 (the flag's definition covers content withdrawals), R-HA-054 (a row for
`npm.unpublish`), and [architecture.md](../architecture.md) R-ARCH-008 (a serve-time check that
lets a format refuse an item on `/files`).

### Q-NPM-13 · Removal marks after a restore and for cached tarballs

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

A restore returns `npm_remote_versions` and the core's gone marks to the backup's snapshot like
every other table (INV-19). A version the upstream withdrew after the snapshot is `listed` again,
and its cached tarball, which the backup holds, would be served until a refresh of its document
marks it again. Lockfile installs (`npm ci`) request tarballs without documents, so they never
trigger that refresh. The same blind spot exists without a restore: a cached tarball was served on
its stored marks however old the document was, so a takedown reached lockfile installs only after
someone else requested the document, which Q-NPM-2 assumed would happen.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Leave it to document requests | Marks change only when a document request refreshes the document | No extra upstream request | A lockfile-only workload keeps installing a withdrawn version indefinitely; after a restore, every takedown made since the snapshot is undone until the document is requested |
| (b) Journal `npm.remote.version_removed` | The removal event joins the revocation journal; replay sets `listed = false` again | Re-applies removals after a restore even while the upstream is unreachable | Only covers restores; a version the upstream listed again after its removal stays hidden after the replay, because a later `304` does not rewrite the records (R-NPM-024); forged entries could hide public versions until the upstream document changes; one journal object per removal |
| (c) A cached tarball waits for a current document | A cached tarball is served only while its package document is unexpired and was validated after the last restore; otherwise the request revalidates the document first, in one flight | The upstream's current state decides, on every replica, after a restore and for lockfile installs alike; nothing new to journal or forge; reads two PostgreSQL values the request already touches | One conditional upstream request per package and metadata period for lockfile-only traffic; while the upstream is unreachable, the stored or restored marks decide |

**Choice:** (c) — a cached tarball is served only while its package document's cache entry is
current: unexpired and validated after `system_state.restored_at`. Otherwise the request
revalidates the document first.

**Rationale:**
- The upstream is the authority on what it withdrew. (c) asks it, as a document request would,
  instead of keeping a second record of its decisions that can go stale (b).
- The decision reads `expires_at`, `validated_at` and `system_state.restored_at`, all on
  PostgreSQL's clock, so every replica decides the same way (INV-18), with or without Valkey.
- The cost is what a document request already pays, once per package and period, collapsed across
  replicas by the flight lock; with the default period of 7,200 s it is negligible next to the
  tarball traffic itself.
- While the upstream is unreachable, refusing every cached tarball would turn an upstream outage
  into an install outage. (c) keeps stale serving and names the residual in H-51.
- The development agent would revisit this if
  `trove_npm_tarball_document_revalidations_total` shows the revalidations as a noticeable share of
  upstream traffic, or if an upstream outage right after a restore turns out to be a common case;
  (b) would then be added for the restore case only.

**Applied in the spec:** R-NPM-025 (the rule, its cost and an acceptance criterion); R-NPM-026
(marks read per request; the delay bound); R-NPM-047 (remote repositories after a restore, an
acceptance criterion); R-NPM-044 (the metric); the revisit of Q-NPM-2; H-33 and H-51; the Security
notes. Cross-page: [backup-and-restore.md](../backup-and-restore.md) R-BAK-022 (formats read
`system_state.restored_at`).

### Q-NPM-44 · How the end-to-end suite gets the eight clients

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-NPM-045 needs npm 10 to 12, pnpm 10 to 12, Yarn 4 and Yarn 1 at exact versions, against
the `scatter` profile in the dev cluster, with the base URL and the TLS behavior of the setup
snippets (R-NPM-041): `https://localhost:<port>` verified against the dev CA.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One pinned image, clients in cluster pods behind a forwarder | `test/e2e/clients/package.json` holds one npm alias per row, `package-lock.json` the integrity; the image has a wrapper per row and a TCP forwarder on `127.0.0.1:<base port>` that relays to the edge or one `web` pod; the dev CA is mounted | Exact, locked versions on any host and in CI; the snippets run verbatim and the certificate is verified for `localhost`; a client can be pinned to one replica; nothing is installed on the host | An image to build and load into kind; the clients do not run on the host's network stack |
| (b) Clients on the host | Install the eight clients on the developer's machine or the runner and call the edge on `localhost:8080` | No image, no forwarder | Eight installs per machine, drift between laptops and CI, a second place for the pins; no way to pin a client to one replica |
| (c) Fetch at run time | Corepack, `npx` or the clients' own self-update download the version when a test starts | Nothing to build | Downloads during the test (network, supply chain, flakiness), versions not locked by integrity, the scheduled raise of the pins has no file to change |

**Choice:** (a).

**Rationale:** The matrix is only meaningful when every row is the version the rule names, and when a
change to one pin is visible as a diff of one file. (a) is the only option where the version, its
integrity and the network path are fixed before the test starts; the forwarder keeps the setup
snippets byte for byte what a user pastes. The development agent would revisit this if kind's node
network ever makes the forwarder a bottleneck for the install loops of the drain run (R-HA-059), or
if a client gains a certificate rule that `NODE_EXTRA_CA_CERTS` does not reach.

**Applied in the spec:** R-NPM-045 (where the pins live).

### Q-NPM-45 · The Node.js the clients run on

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: npm, pnpm 10, pnpm 11, Yarn 4 and Yarn 1 run on Node.js; pnpm 12 ships a native binary.
Verified on 2026-10-30 from the packages' `engines`: npm 10.9.9 `^18.17.0 || >=20.5.0`, npm 11.21.1
`^20.17.0 || >=22.9.0`, npm 12.2.1 `^22.22.2 || ^24.15.0 || >=26.0.0`, pnpm 10.34.9 `>=18.12`,
pnpm 11.28.9 `>=22.13`, pnpm 12.16.1 `>=18`, Yarn 4.18.1 `>=18.12.0`, Yarn 1.22.22 `>=4.0.0`. The
repository's Node.js is 24.22.2 (`web/.nvmrc`).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One Node.js, the repository's | One image `FROM node:<web/.nvmrc>-trixie-slim`, pinned by digest in `tools/versions.env`; `make versions-check` compares the tag with `web/.nvmrc` | One Node pin for the repository; every `engines` range accepts 24.22.2; one image, one build; the glibc variant most CI images use | A row runs on a Node it was not bundled with (npm 10 normally meets Node 22) |
| (b) The bundled Node per row | Node 22 for npm 10, Node 24 for npm 11, Node 26 for npm 12, and so on | Each npm runs where users run it | Three or more Node pins and images; the version-per-tool rule is broken for Node; the registry-visible behavior of the clients does not depend on the Node minor |
| (c) Alpine variant | As (a) on musl | Smaller image | pnpm 12 would run its musl binary, which is not the one most users run; the image is a test tool whose size does not matter |

**Choice:** (a), the digest-pinned `node:24.22.2-trixie-slim`. The image removes the `npm`,
`npx` and `corepack` that come with Node.js, so the only npm in it is the three npm rows.

**Rationale:** No row needs a different Node: all eight `--version` calls ran on 24.22.2 in the
image, and npm 12 (`^24.15.0`) and pnpm 12 (native, `>=18`) accept it. The development agent would
revisit this if a scenario fails on a row only on Node 24 and passes on the Node the row is bundled
with; that row then gets its own image tag and its own pin.

**Applied in the spec:** R-NPM-045 (the Node.js of the clients).

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
remote's metadata period and the document refresh, which the next document or tarball request
after the period triggers (R-NPM-025). Until then, and for every client that installed it
earlier, the version stays reachable; while the upstream is unreachable, the stored marks keep
deciding. trove does not scan package content; curation and scanning are separate capabilities.

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

### H-51 · A restore reverts npm metadata changes made after the backup's snapshot

Component: the restore of a backup ([backup-and-restore.md](../backup-and-restore.md) R-BAK-021)
together with the journal replay ([ha-and-helm.md](../ha-and-helm.md) R-HA-054) and the
revalidation of remote documents (R-NPM-025). Subject: the consumers of npm local and remote
repositories after a restore. Live in the MVP whenever a backup is restored (INV-19). The replay
re-applies unpublishes by withholding the versions (R-NPM-047, Q-NPM-12) and nothing else of npm.
What a restore still reverts or leaves behind:

- a dist-tag moved after the snapshot points back to its old version, for example `latest` moved
  off a broken or malicious release that was not unpublished;
- from P1 (R-NPM-018), a deprecation set after the snapshot is gone;
- a version published after the snapshot is lost, and its number can be published again with other
  bytes, because the replay reserves no lost number (Q-NPM-12); clients that pinned the lost
  version detect that as `EINTEGRITY`;
- an unpublish that had not been shipped to the journal when the old database stopped (normally
  less than `backup.revocation_journal.ship_interval`), that was made while the journal was
  disabled, or that a restore with `--skip-revocation-journal` did not replay (R-BAK-024), is
  undone;
- from P1, a tombstone written by a move out of the repository (R-NPM-008), which is not journaled;
- a withheld version's bytes stay in the blob store until an administrator deletes its items, so a
  version unpublished because it leaked a secret is unreachable for clients but still readable to
  object-store principals ([storage.md](../storage.md) H-11) until then;
- a version an upstream withdrew after the snapshot is served from the cache again while that
  upstream cannot be reached after the restore, because the restored marks decide until the
  package document is revalidated (R-NPM-025, Q-NPM-13).

What makes it real: a harmful version withdrawn by a tag move or a deprecation instead of an
unpublish, followed by a restore to a snapshot older than the withdrawal; or a restore while the
upstream is unreachable. Mitigation: withdraw a harmful version by unpublishing it; after a
restore, repeat the `npm.dist_tag.set`, `npm.dist_tag.delete` and `npm.deprecate` events of the
audit log stream between the snapshot and the restore, and delete or reinstate every version the
replay output lists as withheld.
