# npm registry

Status: specification — local repositories partly implemented (routes, classification, error shape, publish, the data port; the work packages of documents, dist-tags, unpublish and identity, and the remote and virtual classes, are not built)

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
  what it means. This holds for npm. pnpm 10, 11 and 12 retry a 501 answer to their audit `POST` after
  10 seconds and after a minute (measured, Q-NPM-49); through a forwarding repository pnpm 11 and 12
  get the provider's answer (R-NPM-038), and pnpm 10, which asks npm 6's audit endpoints, keeps the
  501 (Q-NPM-65). A metadata `PUT` that is not built answers 404 for the same reason as any other
  `PUT`.

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
| POST | `-/npm/v1/security/advisories/bulk` | R-NPM-038 | 501 | yes | via member |
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
- A decoded name with an empty, `.` or `..` segment, a NUL byte or another control character,
  a backslash, a `%`, more than one `/`, bytes that are not valid UTF-8, more than 512 bytes, or
  the single character `-` MUST be rejected with 400 before any lookup (Q-NPM-14). No npm name contains `%`; a name that still
  carries one after the single decoding step is a second layer of escapes aimed at an upstream
  ([repositories-and-proxy.md](../repositories-and-proxy.md) R-REPO-025).
- Names of **newly published** packages (R-NPM-007, R-NPM-036) MUST satisfy npm's rules for new
  names ([validate-npm-package-name](https://github.com/npm/validate-npm-package-name)): 1 to 214
  characters including the scope; lower case; URL-safe (each of scope and name unchanged by
  `encodeURIComponent`); none of `~'!()*`; neither the scope nor the name starts with `.`, `_` or
  `-` (Q-NPM-14); no leading or trailing whitespace; an unscoped name is not a Node.js core module
  name, `node_modules` or `favicon.ico`. Violations answer 400. The implementation is
  [internal/formats/npm/name](../../../internal/formats/npm/name/name.go).
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
| `POST -/npm/v1/security/advisories/bulk`, npm 6's `…/security/audits[/quick]` | — | `search`: forwarded, never cached (Q-NPM-62) | — | — |
| `-/ping`, `-/whoami`, login, logout, external | — | `other` | — | — |

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
  version at `<name>/-/<basename>-<version>.sigstore`, committed in the same transaction as the tarball (Q-NPM-58).
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
   The ownership rule of R-AUTHZ-018 is asked here too, before a byte of the body is read, as a
   read-only question that claims nothing (Q-NPM-56); the commit transaction decides again under
   the claim row lock, and that decision is the one that counts. A publish that finds a live
   version at the shadowing check and a first version at the commit, because an unpublish took
   the last version in between, answers 503 `shadow_check_unavailable` and the client retries
   (Q-REPO-118).
2. Stream the body through the upload pipeline ([storage.md](../storage.md) R-STOR-004): the
   attachment's base64 MUST be decoded straight into the spool while all digests are computed;
   the decoded tarball MUST NOT be held in memory. `npm.publish.max_tarball_size` caps the tarball,
   the body is capped at the base64 form of it plus the metadata budget of the scanner (768 KiB)
   and the base64 form of the largest provenance bundle (256 KiB, 342 KiB encoded), 1,110 KiB in
   all (Q-NPM-53); beyond: 413. From P1, admission also refuses a
   publish over the system capacity limit or the target repository's quota with 507
   `quota_exceeded` ([storage.md](../storage.md) R-STOR-029, R-NPM-005).
3. Validate (400 unless noted):
   - `name` equals the decoded path name and satisfies R-NPM-002;
   - `versions` holds exactly one entry, `dist-tags` exactly one entry pointing to it; the tag is
     not a valid SemVer range and matches `^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`;
   - the version is valid SemVer 2.0.0 and equal to its canonical form without build metadata
     (`semver.Parse` on the version after `semver.Clean`, Q-NPM-15);
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
   - further archive rules, so that two readers cannot resolve the archive differently (Q-NPM-18),
     each answering 400 `npm_bad_publish`: the file is one gzip member, with no byte after it, and
     the tar archive ends in nothing but zero padding; an entry is a regular file, a directory, a
     symbolic link or a hard link (no device, FIFO, sparse file or PAX global header); a name is
     valid UTF-8 without control characters; a symbolic link has a relative target that stays
     inside the package, a hard link names another entry below the same first component, no
     entry lies below a link, and no link target passes through another link of the archive
     (a link may name another link as the last component of its target; Q-NPM-55); no entry lies below a regular file, and no regular file has the
     install path of a folder; no top-level entry differs from `package.json` only in letter case
     (a case-insensitive file system would install it over the manifest);
   - reading stops at `npm.publish.max_unpacked_size` or `npm.publish.max_tar_entries` (413). Both
     bound a streaming pass that counts entries and sums their sizes without keeping content, and
     it additionally bounds what a stream can cost (Q-NPM-17): the decompressed bytes (a header
     that declares more than the limit is refused before its content is inflated, and a stream
     that inflates beyond it is cut off), the bytes of entry names kept, a name of more than
     1,024 bytes, and a gzip header of more than 64 KiB. `package.json` is read into memory only
     within the limits of [architecture.md](../architecture.md) R-ARCH-028: 1 MiB (413 when the
     header declares more, before a byte is read), a JSON nesting depth of 64, and a compression
     ratio of 100:1 (413). The whole archive may inflate to at most 100 times the compressed
     bytes the inflater has consumed plus 8 MiB (413; Q-NPM-54). The README is read up to 64 KiB,
     the rest is skipped;
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
       `1.2.3`), also for the `name` and `version` check above; a `name` or `version` of the
       tarball's `package.json` that differs from the request's answers 400
       `npm_manifest_mismatch` in `reject` and in `warn` mode alike (Q-NPM-19);
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

     Three conventions complete the steps (Q-NPM-19). A compared field that is absent, `null`,
     an empty object, an empty array or the empty string equals an absent one, and the
     `bundleDependencies` list is compared sorted and without duplicates: none of these changes
     what a client installs. A hosted-git range is written in the canonical form of
     hosted-git-info's `toString()` for `github`, `gitlab`, `bitbucket` and `sourcehut`
     (`user/repo` becomes `github:user/repo`, `https://github.com/user/repo` becomes
     `git+https://github.com/user/repo.git`, `git@github.com:user/repo.git` becomes
     `git+ssh://git@github.com/user/repo.git`); any other range stays as it is.

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
   A provenance bundle is a companion file of the same commit (Q-NPM-58): its hook runs after the tarball's, in the
   same transaction, and links the bundle to the version the tarball's hook inserted; a failure of either leaves neither.
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

Notes on the implementation (Q-NPM-20, Q-NPM-22, Q-NPM-24): the body is read by a scanner that does not assume the order of its members; outside the attachment data it
may hold 768 KiB, a provenance bundle 256 KiB. The bundle is committed with the tarball in one transaction (Q-NPM-58), so a publish with a bundle raises `rev_seq` once. An upload to an npm
repository through `/files/` is refused until R-NPM-036 exists. The answer of an acknowledged retry carries the current `rev`.

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
- A tarball with a symbolic link to `../../etc/passwd`, a hard link to `other/package.json`, an
  entry below a link, a PAX global header, a `Package.json` next to `package.json`, bytes after
  its gzip stream or data after its tar end blocks answers 400 `npm_bad_publish`; a tarball
  whose `package.json` declares 2 GiB answers 413 `npm_too_large` without reading or allocating
  it, and so does one whose `package.json` compresses by more than 100:1.

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
  PostgreSQL inside the request, without `_attachments`; it carries no ETag and `Cache-Control:
  no-store` (Q-NPM-26). On a virtual repository it returns the
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
  `maintainers` lists the distinct principal names that published the versions present, by name and
  without e-mail; it is informational (ownership is R-AUTHZ-018). The versions and the `time` map
  are in SemVer order, and no version object carries a `readme` (Q-NPM-31).
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
  across a restore. Every package document carries an ETag (the `?write=true` read of R-NPM-009
  excepted), which a client's HTTP cache sends back
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
    already reflects patterns, pin rules, claims, the caller's readable members, whether the caller
    may fill each remote member and the endpoint, Q-REPO-110), each contributing member's revision
    (local: `packages.rev_seq`, 0 when absent; remote: the stored document digest), for each
    remote member the versions whose tarballs are cached (the engine's `Merged.MergeFingerprint`
    covers these three), the `config_version` of the virtual repository and of each member (covering
    `latestStrategy`, `distTagStrategy`, `externalDependencies` and `serveRemovedVersions`), the
    public base URL and `npmRenderVersion`;
  - after a restore, `rev_seq` and `config_version` have grown by 2^32 (R-BAK-022, R-NPM-047), so
    no ETag of the old installation matches a restored document.
- Package documents MUST carry `Vary: Accept, Accept-Encoding` and `Cache-Control: private,
  max-age=<npm.document_max_age>, must-revalidate`. They are always `private`: a merged document
  depends on the caller's readable members (R-AUTHZ-009 rule 4), and a shared HTTP cache must
  never hand one caller's view to another.
- Tarballs MUST carry a strong `ETag` and `Cache-Control: private, max-age=31536000, immutable`.
  The ETag is the one of [storage.md](../storage.md) R-STOR-012, `"<sha256 hex>"`, the same on
  `/files/` and on the npm route; the response is always `private`, because the anonymous
  principal's read cannot be asked at response time (Q-NPM-28). Ranges and conditional requests on
  tarballs follow R-STOR-012.
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
  (R-NPM-003); both by `ServeCheck`, which reads the tombstone (Q-NPM-28). Deprecated versions are
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
  matching the clients' own check (`semver.ValidateTag`; strict range syntax, as `npm dist-tag add`
  tests it, Q-NPM-15).
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
- Every change increments `packages.rev_seq` and emits an audit event; a write that changes nothing
  (the tag points to that version already) answers 200 and does neither (Q-NPM-29).

Configuration (`npm` section of a local repository):

```yaml
npm:
  latestGuard: true   # default (proposed); a member of the repository document, validated against the format's schema (Q-NPM-59, Q-NPM-60)
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
| DELETE | `{name}/-/{file}.tgz/-rev/{rev}` | — | No-op for a version already removed by the `PUT` (npm sends it next); the revision is not compared (Q-NPM-30) | 200; 400 `npm_bad_publish` if the version is still live |
| DELETE | `{name}/-rev/{rev}` | — | Every live version deleted and tombstoned; all dist-tags removed | 200; 409 stale rev, unless every version is already removed (R-NPM-009) |

- On the `PUT` path trove applies removals only: dist-tags are recomputed by R-NPM-016, not taken
  from the body, and every other difference is ignored. A body without `versions` (npm's partial
  maintainer update, PKG-NPM-022) answers 400 `npm_bad_publish` with "maintainer lists are not
  supported; ownership is managed in trove".
- A raw `DELETE /files/<npm local>/<tarball path>` (R-STOR-018) unpublishes that version the same
  way (`OnItemsRemoved`). So does the `PUT` and the `DELETE` above: they remove the files of the
  versions through the package writer in the transaction that checked ownership and the revision,
  and the same hook writes the tombstone, the tags and the audit event (Q-NPM-27).
- A raw delete, through `/files/` or the items API, of a tarball, a package folder or a scope asks
  the ownership question for every package whose files it removes, under the package row lock in
  the removing transaction (or the one that queues the job `items-delete`), and needs `delete` on
  each file's package and version; a deleter who does not own one of the packages gets `403
  package_owned_by_other_owner` and nothing is removed ([authorization.md](../authorization.md)
  R-AUTHZ-018, Q-AUTHZ-161).
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
- Until deprecation is built, such a `PUT` answers 404 `not_found` as R-NPM-043 prescribes for an
  unimplemented `PUT`, so `npm deprecate` fails at once; a 501 would be retried by npm's fetch layer
  for 70 seconds (Q-NPM-49).

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
| GET | `-/ping` (query `write=true` accepted and ignored) | 200 `{}`; 401 for anonymous callers, also where the repository is anonymously readable (Q-NPM-29) | authenticated subject (R-AUTHZ-007) |

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
  records above (the `remote_url_map` rows included, Q-REPO-119) and the removal marks of R-NPM-026 are therefore written in the transaction that
  upserts the cache entry (R-REPO-026 step 5), after it has created (`INSERT … ON CONFLICT DO
  NOTHING`) and locked the package's `npm_remote_packages` row, and only when that upsert replaces
  the stored document. The stale-copy rule above is evaluated in that same transaction, under that
  lock, against the `doc_modified` of the document stored at that moment, not against the one
  that was stored when the fetch began: a fill whose document is older than one that another fill
  committed meanwhile is a stale copy by the time it commits. A document with a later `time.modified`
  replaces the stored one even when the stored entry was fetched later, and the cache entry follows
  it (Q-NPM-57, Q-REPO-120). When the core keeps the newer stored entry
  (R-REPO-026: documents of the same or an unknown `time.modified`), or the stale-copy rule applies, the fill keeps the stored document, renews its
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
  `dist.integrity`, else `dist.shasum` (SHA-1): only when the document announces no `sha512`, so a
  legacy or wrong `shasum` next to a valid `sha512` refuses nothing. With the default `verify-if-available` policy a
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
  (`listed = true`). With `true`, a document never lists a version the current upstream document
  lacks: the tarball of an unlisted version stays served on every route and a gone package keeps
  serving its cached tarballs, but the version does not reappear in documents (Q-NPM-35).
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
  serveRemovedVersions: false   # default (proposed); a member of the repository document (Q-NPM-59)
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
`no_deploy_target`; the decision point refuses a write to a virtual repository without a target
with 403 first, Q-NPM-36), with the target's permissions, patterns and ownership (R-AUTHZ-009 rule 5).
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
  latestStrategy: highest-member-latest   # default (proposed); highest-member-latest | newest-published; a member of the repository document (Q-NPM-59)
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
  `@acme/*` covers scope `@acme`, `acme-*` unscoped names with that prefix. They are limited to the two forms
  of R-REPO-023, an exact name or a literal followed by one trailing `*` or `**`, because only for those is
  "most specific wins" a total order: `@acme/util`, `@acme/*`, `acme-*`, `*` (every unscoped name) and `**` are
  valid, `@*/*` and `a*b` answer 422 `invalid_pattern`. The include and exclude lists of R-REPO-005 take the full
  name-pattern syntax.
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
  tag is `latest` unless the query parameter `distTag` names another. Until then `/files/` refuses
  every deploy into an npm repository, file or folder, with `409` `conflict` naming the npm
  protocol address, before the body is read, as [storage.md](../storage.md) R-STOR-007 describes
  ([architecture.md](../architecture.md) Q-ARCH-223).
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
Priority: **MVP** · Covers: PKG-NPM-049, PKG-NPM-059

Raised from P1 to MVP and built in M2 (Q-NPM-61). `POST -/npm/v1/security/advisories/bulk` (npm 7+
with a gzip body, pnpm 11 and later uncompressed, `yarn npm audit`); the body is an object of package
names, each with a list of versions:

- Mode `forward` (remote repositories, the default, Q-NPM-3): trove decompresses the body
  (`npm.audit.maxBodySize` as sent, ten times that decompressed, at most 100,000 names, 413
  `npm_too_large` beyond; a content coding other than gzip is 415, a body that is no such object
  400, Q-NPM-64) and forwards to the provider (default: the remote's upstream URL), with the
  remote's upstream credentials and `npm.audit.timeout`, through the egress rules of R-REPO-038,
  **only the names that the forwarding remote already holds a stored upstream document for** (an
  `npm_remote_packages` row, R-NPM-024, also one whose package document the core marked gone, since
  withdrawn malware carries advisories; one query for the whole body). Of those it also withholds
  every name that exists in any npm local repository of the installation (a package row or a
  tombstone), is covered by a namespace claim of any scope of any npm local repository, matches a
  pin rule of the requesting virtual repository (R-REPO-023), or is refused by the include and
  exclude patterns of the forwarding remote or of the requesting virtual repository (R-REPO-005,
  Q-NPM-63). A name the remote has fetched is one the upstream has already seen from this
  installation, so forwarding discloses which public packages a project combines but no name the
  upstream did not already know; a name it never fetched (a git or file dependency, a package
  installed from elsewhere, an internal name nobody claimed) is withheld. A `providerURL` other than
  the upstream learns the same set of names.
- When names were withheld, the response carries `npm-notice: trove audited <n> of <m>
  packages; <k> internal or not yet proxied names were not sent to the advisory provider`, and
  `trove_npm_audit_names_withheld_total{reason}` counts them by reason (`local`, `claimed`,
  `pinned`, `not_proxied`: each name once, by the first reason in this order, labelled with the
  forwarding remote, Q-NPM-63). Withheld names are absent from the answer, which clients read as "no
  advisories"; the notice is what tells the user the audit was partial, and only npm prints it on
  a successful answer ([H-302](../../security/data-and-secret-flow.md#h-302--pnpm-and-yarn-do-not-show-that-an-audit-was-partial)).
  When every name is withheld, trove answers `{}` without contacting the provider.
- The provider's 200 body is returned unchanged, with its `Content-Type` and `Content-Encoding`,
  `Cache-Control: no-store` and at most 128 MiB (`MaxDocumentBytes`); pnpm accepts only an object
  keyed by package name whose advisories carry a numeric `id` and a string `vulnerable_versions`,
  which the public registry's answer satisfies. Provider failure (no answer within the deadline, a
  refused connection, any status other than 200): 502, with the code of the failure
  (`upstream_timeout`, `egress_denied`, `upstream_error`, `upstream_auth_failed`, …).
- The forward is one request through the forwarding remote's upstream client
  ([repositories-and-proxy.md](../repositories-and-proxy.md) R-REPO-033, R-REPO-038, R-REPO-039):
  its egress rules, trust store, redirect rules and credential policy, which sends the remote's
  sealed upstream credentials only to the upstream origin and to `extraHosts` entries with
  `sendCredentials`, so a `providerURL` on another host gets none. It reads no cache entry, takes no
  fetch slot and neither reads nor moves the circuit (Q-NPM-62). `providerURL` is an `https` URL
  without user info, query or fragment (the repository document is shown in clear to every holder of
  `repositories:read-config`, H-303): no credential ever travels in it.
- A virtual repository forwards through its first remote member in resolution order (priority
  members first, R-REPO-022) that is enabled and whose mode is `forward`, whoever asks (Q-NPM-62).
- Mode `disabled`, every local repository, a virtual repository without a forwarding member, a
  remote that may not contact its upstream (offline, its own mode or `proxy.global_offline`,
  R-REPO-030, R-REPO-031), the cache view `<key>~cache`, and an endpoint that does not fill caches
  or has exposure rules (R-NPM-048) answer **501** `npm_audit_unavailable` with "security audit is
  not available on repository <key>; it is disabled or no upstream provides advisories". Why 501
  and not 404: the repository and the endpoint exist, and "not found" sends users looking for a
  wrong registry URL; 501 is the HTTP status for a function the server does not support; npm never
  retries a `POST`; `npm install` logs the audit failure and completes; `npm audit` and `pnpm audit`
  print status and text.
- npm 6's `-/npm/v1/security/audits/quick` and `-/npm/v1/security/audits` answer 501
  `npm_audit_unavailable`: with the text above where no repository forwards, else with a text that
  names the bulk endpoint (npm 7 to 10 ask the quick endpoint after a failed bulk request and print
  its answer). pnpm 10 asks only these two endpoints, so `pnpm audit` of pnpm 10 is not served; it
  retries each 501 after 10 seconds and a minute and fails after about 140 seconds (measured,
  Q-NPM-65).
- Permission: `read` on the repository and, for a virtual, on the forwarding member; a caller
  without `read` on the forwarding member gets the refusal of R-AUTHZ-016 and no provider is asked.
- The forward holds no PostgreSQL connection while it waits: its one query runs before the
  provider is called. Its deadline is `min(npm.audit.timeout, server.shutdown_timeout − 1 s)`,
  counted from the arrival of the request. npm never retries a `POST` (R-NPM-001), so a forward cut
  by its replica's shutdown would fail `npm audit`; with this deadline, a forward that is running
  when its replica starts to drain ends before the shutdown deadline
  ([architecture.md](../architecture.md) R-ARCH-024, R-NPM-046).

Configuration (`npm` section of a remote repository, validated by
[settings.schema.json](../../../internal/formats/npm/settings.schema.json)):

```yaml
npm:
  audit:
    mode: forward          # default; forward | disabled
    providerURL: null      # default: the remote's upstream URL; an https URL without user info, query or fragment
    timeout: 30s           # default; 1s to 300s
    maxBodySize: 1MiB      # default (Q-NPM-64); 1KiB to 10MiB, ten times that decompressed
```

Acceptance criteria:

- After `npm install` through `npm-all`, `npm audit` reports the advisories the public registry
  reports for the same lockfile; `pnpm audit` (pnpm 11 and later) and `yarn npm audit` likewise.
- The test provider's request log never contains `@acme/ui` or any other name present in
  `npm-local`, nor a name covered by a claim, nor a name `npm-remote` never fetched.
- A lockfile with one public package that `npm-remote` never fetched: `npm audit` prints the
  `npm-notice` naming 1 withheld name, and the provider does not see that name.
- `npm audit --registry …/npm-local/` fails with 501 and the error text; `npm install` against the
  same registry succeeds.
- A small gzip body that inflates beyond the limit answers 413.
- A `providerURL` with user info is refused with 422 when the repository document is written.
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
credentials beside the body (R-NPM-021); the uncompressed audit body and strict audit response of pnpm 11 and later
(R-NPM-038; pnpm 10 asks npm 6's audit endpoints, which answer 501, Q-NPM-65). Yarn 1 reads `.npmrc`, requests abbreviated documents and sends credentials only with
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

pnpm: the same lines in the user-level `~/.npmrc`, because pnpm 11 and 12 ignore a credential with an
environment variable in a project `.npmrc` (Q-NPM-47); in `pnpm-workspace.yaml`, for pnpm versions that know the settings:

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

**The package view of npm** (a part of R-NPM-041: the same screens show the snippets and the packages). The package screens
(api-and-ui.md R-API-029) read npm through the format's view adapter (Q-API-166). Versions
order by SemVer precedence, build metadata included as the last resort so that no two strings compare equal; a
pre-release is a version with a pre-release tag. The `metadata` of a version holds, from its stored manifest and
only where the member is of the documented type: `distTags` (the package's tags, limited to versions the caller
may read), `dependencies`, `devDependencies`, `peerDependencies`, `optionalDependencies` and `engines` (name to
string), `authors` (the `author` and the `contributors` as `Name <email> (url)`), `homepage`, `repositoryUrl` (the
`repository` string or its `url`), `bugsUrl` (the `bugs` string or its `url`) and `deprecated` (the stored
deprecation message). `licenses` is the `license` member (an SPDX expression, or the `type` of a legacy object) and
the `type`s of the legacy `licenses` list; `description` and `keywords` are the manifest's, the keywords
without blanks and non-string entries. `q` matches a description case-insensitively as a substring and `keyword` a
keyword exactly. A version tombstoned and not removed (a restore's replay, R-NPM-047) is withheld and appears
nowhere. The README is the stored README text of the version; the setup snippets of this page are rendered by the
API with the client ids `npm`, `pnpm`, `yarn` and `yarn-classic` (Q-UI-77).

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

- `npm token list`, `npm profile get`, `npm owner add someone @acme/ui`, `npm org ls acme`, `npm access get
  status @acme/ui` against `npm-all` fail within one second each, without retries in npm's
  verbose log. (`npm owner ls` is not on the list: it reads the maintainers of the package document
  and succeeds, Q-NPM-49.)

### R-NPM-044 · npm audit events and metrics
Priority: **MVP** · Covers: trove-only

Audit events ([operations-and-observability.md](../operations-and-observability.md), INV-16), with
actor, repository, package, version or tag, outcome and source address:

| Event | When |
|---|---|
| `npm.publish` | R-NPM-007, R-NPM-036; with integrity, the dist-tag and whether the latest guard applied; outcome `success` with the reason `retry_acknowledged` for an acknowledged retry (R-NPM-007 step 4, Q-NPM-25) |
| `npm.unpublish` | R-NPM-017, per version, with its integrity. The registry entry declares `journal: revocation` (R-OPS-014), so a restore re-applies it (R-NPM-047, Q-NPM-12) |
| `npm.reinstate` | R-NPM-047: `trove admin npm reinstate` put a withheld version back, with its integrity and the journal entry that withheld it |
| `npm.dist_tag.set`, `npm.dist_tag.delete` | R-NPM-016 |
| `npm.deprecate` | R-NPM-018, per version and message change |
| `npm.publish.manifest_mismatch` | R-NPM-007, in both modes |
| `npm.trusted_publishing.exchange` | R-NPM-022, with the matched identity mapping |
| `npm.remote.version_removed` | R-NPM-026 |

Login, logout and token events are R-AUTH-041's; integrity and egress events of remote fetches, and
the egress and TLS events of an audit forward (R-NPM-038), are repositories-and-proxy.md's. An audit
forward writes no audit event of its own.

Metrics (prefix `trove_`, INV-16; the label written `repository` below is `repo` in the label list of R-OPS-007, Q-NPM-25): `trove_npm_requests_total{class,operation,status}`,
`trove_npm_request_duration_seconds{class,operation}`,
`trove_npm_document_render_seconds{class,variant}`,
`trove_npm_virtual_version_conflicts_total{repository}`,
`trove_npm_removed_upstream_versions_total{repository}`,
`trove_npm_stale_upstream_documents_total{repository}`,
`trove_npm_audit_names_withheld_total{repository,reason}` (R-NPM-038; the repository is the forwarding remote, Q-NPM-63),
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
Test and Release on the commit that closes the phase repeats the scenario list with Valkey stopped (R-NPM-046); that run is the
workflow dispatched with `closing=true` on that commit (Q-NPM-46). The node-drain run of R-HA-059 carries npm load
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
- The stub (`test/npmstub`) serves over TLS only, counts every request per path (conditional and
  unconditional apart) and exposes the counters and the switches on a token-protected control API.
  Its data is a synthetic corpus of at least 600 packages, generated by code and byte-for-byte
  reproducible, plus a small set recorded from the public registry for the shapes the corpus cannot
  fake; neither contacts a network during a test run (Q-NPM-41, Q-NPM-42). The reference project
  of exit criterion E2.2 is `perf/npm/reference`: a `package.json` chosen from the corpus and a
  `package-lock.json` of at least 500 packages, resolved by the npm of the pinned Node image against
  the stub and committed; a check regenerates it and fails on any difference (Q-NPM-43).
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
- Two generations of the corpus give identical bytes and match a committed digest; the committed
  reference lockfile has at least 500 packages, each present in the corpus with the corpus's
  integrity; `make npm-reference-check` fails when a regeneration differs from the committed files.

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
- **Where.** A local document is filtered on its rows, after they are read and before it is
  rendered; a merged or rewritten document is filtered on its bytes (`FilterDocument`). One
  decision serves both, so a document lists the same versions and tags whatever class served it
  ([Q-NPM-38](#q-npm-38--where-a-document-is-filtered)). `FilterDocument` filters the package
  document (`{name}`, full and abbreviated) and the single-version document of a version
  (`{name}/{version}`); the dist-tag map and the version a tag names are read from the filtered
  package document, and any other document is refused rather than served unfiltered.
- **Dist-tags.** A tag other than `latest` is kept when the rules admit its name and the version it
  points to is exposed, and dropped otherwise; it is never moved to another version. `latest` is not
  subject to `distTags`: it keeps its version when that version is exposed, and otherwise moves to
  the highest exposed version that is not a pre-release, or to the highest exposed version when all
  are pre-releases, the rule of R-NPM-016. A document without `latest` gets one by the same rule
  ([Q-NPM-40](#q-npm-40--how-the-filter-reads-a-document-it-did-not-render)).
- **Top-level fields** (`description`, `readme`, `keywords`, `license`, `homepage`, `repository`,
  `bugs`, `author`) come from the version the filtered `latest` points to (R-NPM-010): when the
  filter moves `latest`, a field the new `latest` does not carry is removed, and the README is the
  one that version's entry carries, else none
  ([Q-NPM-39](#q-npm-39--the-readme-when-the-filter-moves-latest)); `maintainers` lists only the
  publishers of exposed versions, by name.
- **Other documents.** `GET -/package/{name}/dist-tags` returns the filtered tags. The
  single-version document (R-NPM-014, P1) answers 404 for a hidden version or a dropped tag, and a
  tag names the version of the filtered tags, so `{name}/latest` is the moved `latest`. Search
  (R-NPM-037, P1) returns only exposed packages, with the filtered `latest` as their version.
- **Tarballs** of hidden versions answer the 404 of R-EP-013; the version is the one R-NPM-015
  parses from the file name.
- **No writes.** `?write=true`, publish, dist-tag writes, deprecation and unpublish answer 403
  `endpoint_read_only` (R-EP-013).
- **Upstream contact.** On an endpoint with `cache_fill: false` (R-EP-019), the audit endpoint
  (R-NPM-038) answers 501 `npm_audit_unavailable`, as in mode `disabled`, because forwarding
  would contact an upstream. It does so on an endpoint with exposure rules as well, because the
  split into forwarded and withheld names tells which names a remote has fetched, the fact
  `Cache-Status` is hidden for there (Q-EP-60, Q-NPM-62).
- **Validators.** The ETag of R-NPM-013 includes the endpoint and its exposure fingerprint, so a
  rule change makes every cached copy of the document fail revalidation.
- **Version ranges** in rules use node-semver range syntax; `ValidateRange` refuses what npm's own
  range parser refuses in its strict mode, never the loose mode that dependency specifiers use, and
  pre-releases follow `prereleases`, not the range's pre-release rule: a range is evaluated with
  node-semver's `includePrerelease`, so `>=2.0.0 <4` contains `2.1.0-beta` and not `4.0.0-rc.1`,
  and the rule's `prereleases` flag decides whether a pre-release is exposed. A range of more than
  4096 bytes is refused (Q-NPM-16; `semver.ParseForExposure`).

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
  already fetched it from its upstream and no local repository, claim, pin rule or pattern marks it
  internal (R-NPM-038); see H-34. The forward runs through the remote's upstream client, so the
  egress rules apply and the remote's sealed credentials reach only the upstream origin and
  `sendCredentials` hosts; `providerURL` refuses user info, so no credential sits in the repository
  document (H-303). What remains open is [H-300](../../security/data-and-secret-flow.md#h-300--the-advisory-providers-answer-is-trusted-as-it-is)
  to [H-302](../../security/data-and-secret-flow.md#h-302--pnpm-and-yarn-do-not-show-that-an-audit-was-partial).
- **ID tokens stay on trove's host.** trove owns the host root of its base URL (R-OPS-005 refuses a
  base URL with a path), so the host-root exchange URL npm derives from the registry URL always
  reaches trove and never another service on a shared host (R-NPM-022).
- **Bounded parsing.** Publish bodies, tarball scans and audit bodies (as sent, decompressed, and
  by their number of names) are bounded before they are parsed (R-NPM-007, R-NPM-038, Q-NPM-64), and upstream documents by R-REPO-040, so one request cannot
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
- **Unpublish through `/files/`.** The unpublish routes ask the ownership question under the package row lock; a raw delete through `/files/` or the items API asks it too, for
  every package whose files it removes, in the removing transaction, before the removal hook unpublishes the versions (Q-NPM-27, [authorization.md](../authorization.md) Q-AUTHZ-161).
  `/files/` takes no file and no folder into an npm repository ([architecture.md](../architecture.md) Q-ARCH-223), so no raw path can block a version's tarball path.
- **Takedowns and restores.** Unpublishes made after a backup's snapshot are re-applied from the
  journal after a restore by withholding the version, which deletes nothing, so a forged journal
  entry cannot destroy restored content (R-NPM-047, Q-NPM-12). Upstream withdrawals made after the
  snapshot are re-checked against the upstream before a cached tarball is served (Q-NPM-13). What a
  restore still reverts is H-51.

## Decisions

Each question below was decided by the development agent from the listed options, unless its status names the owner; the owner reviews the choices at the walkthrough.

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

### Q-NPM-14 · Package-name rules the reference validator leaves open

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-NPM-002 names validate-npm-package-name as the rule for new names and lists what a lookup refuses,
but four points are left to the implementation ([internal/formats/npm/name](../../../internal/formats/npm/name/name.go)):
whether a looked-up name has a length bound and may hold control characters; whether the leading
character rule reaches the scope and the part after it; whether a leading `-` is refused; and what
the look-alike fold covers. Verified on 2026-10-30 by reading validate-npm-package-name 8.0.0 and
running it on 110 names: it tests a leading `.` and `_` on the whole string (so `@a/_b` and `@_a/b`
pass) and a leading `.` on the part after the scope, it refuses a leading `-`, and it counts capitals,
`~'!()*`, more than 214 characters and core-module names as reasons a name is not valid for new
packages. Not verified: whether the public registry accepts `@a/_b`; the validator is the client-side
rule, and trove cannot ask the registry.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Mirror the validator exactly | New names pass exactly when the validator says so; a lookup refuses only the R-NPM-002 list | No name a client's own check accepts is ever refused by trove | `@a/_b`, `@_a/b` and `@-a/b` pass although the unscoped `_b` does not: an accident of where the validator tests the start of the string, not a rule anyone chose. A lookup name can hold control characters and any length |
| (b) The validator, with the leading rule on every part, plus lookup hygiene | A new name's scope and name each start with a letter or digit; a lookup also refuses control characters, bytes that are not UTF-8, a name of more than 512 bytes and the single character `-` | One rule for scoped and unscoped names; a name that cannot be a path segment, a log line or a metric label never reaches a lookup; the bound keeps client-chosen names from growing cache and store keys | Refuses `@a/_b`-style names the client-side validator lets through (none known to exist; not verified) |
| (c) As (b), and lookups also refuse capitals | Legacy mixed-case names are not served | Names become uniformly lower case | Breaks `npm view JSONStream` through a remote, which R-NPM-002 requires |

**Choice:** (b).

**Rationale:**
- The leading-character rule exists so a name cannot be taken for a hidden file, a private module or
  an option. Those readings apply to the scope and to the name alike, so (a)'s exemption of the
  second part is an accident; (b) states the rule once.
- The lookup bound of 512 bytes is not an npm rule. It is twice the longest name a new package may
  have and exists to bound key sizes; no upstream name longer than 214 characters is known.
  Control characters and invalid UTF-8 cannot belong to a package name that a client typed in a URL.
- The fold removes `-`, `_` and `.` from the whole canonical name, scope included, and lower-cases
  ASCII only. R-NPM-002 describes "the name" and the column `name_fold`; a scope that differs only
  by those characters is the same look-alike risk. Names that pass the new-name rules are ASCII, so
  nothing is lost, and an ASCII-only mapping is stable across PostgreSQL locales.
- The development agent would revisit this if a customer must publish a name that (b) refuses and the
  public registry is shown to accept it (relax the rule for the second part first), or if an
  upstream name longer than 512 bytes appears.

**Applied in the spec:** R-NPM-002 (the lookup list and the new-name rule). Code: `name.Parse`,
`name.ParseEscaped`, `name.ValidateNew`, `name.Fold`.

### Q-NPM-15 · Which version grammar applies where, and how identifiers compare

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-NPM-007 requires a published version to be valid SemVer 2.0.0 and equal to its canonical form, and
to be cleaned the way npm's `clean()` does before it is compared with the tarball's manifest.
node-semver, which every npm client calls, is more tolerant than the specification: its strict
parser trims the text and accepts a leading `v`; its loose parser accepts `=`, spaces, leading zeros
and a pre-release without a hyphen; its range test for a dist-tag name (R-NPM-016) is strict. Its
comparison turns a numeric pre-release identifier into a floating-point number, so two identifiers
above 2^53 can compare equal although they differ. Verified on 2026-10-30 against node-semver
7.8.5 (source read, and run on 40,000 version texts and 20,000 compared pairs: no difference except
as stated below).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) node-semver's strict grammar everywhere | One parser; `v1.2.3` and ` 1.2.3 ` are versions | Exactly what clients accept | A published version `v1.2.3` would have two texts for one version; the "equal to its canonical form" check of R-NPM-007 could not be stated |
| (b) Three entry points | `Parse`: SemVer 2.0.0 as written, for what trove decides (a published version after `Clean`, a tag's value). `ParseNode(s, loose)`: node-semver's own parse, for versions that arrive in upstream documents and file names. `Clean`: npm's `clean()`. Loose only on request, no caller yet | Each place uses the grammar its requirement states; loose mode exists for a later caller and is verified now | Three names to choose from; the developer page says which to use |
| (c) SemVer 2.0.0 only | An upstream version with a `v` is not a version | Simplest | A remote repository would drop upstream versions that every client installs |

Exact comparison: the pre-release rule of SemVer 2.0.0 compares numeric identifiers as numbers of any
size. The package compares them exactly (by length, then digit by digit) instead of converting to a
float. The two agree on every identifier up to 2^53; above it node-semver cannot order versions it
sees as different.

**Choice:** (b), with exact numeric comparison.

**Rationale:**
- A requirement states a grammar once; (b) gives each requirement the entry point that implements
  it, and no caller has to remember which string was already cleaned.
- Exact comparison keeps one total order consistent with SemVer 2.0.0 item 11, where a float
  conversion makes `1.0.0-9007199254740993` equal to `1.0.0-9007199254740992`. Nobody publishes such
  versions; the difference exists only so that sorting never depends on rounding.
- A numeric pre-release identifier of at least 2^53-1 keeps its text, as in node-semver; in loose mode
  its leading zeros are kept too, so a version never changes meaning between parses.
- The dist-tag test uses the strict range grammar because the client that sets a tag
  (`npm dist-tag add`) tests it strictly; a tag valid in one and rejected in the other would be set
  by one client and be unusable for another.
- The development agent would revisit this if an upstream registry serves versions the strict entry
  points refuse and clients install them (the remote would then need the loose entry point).

**Applied in the spec:** R-NPM-007 step 3 (the version check names `Clean` and `Parse`); R-NPM-016
(the tag rule names `ValidateTag`). Code: `semver.Parse`, `semver.ParseNode`, `semver.Clean`,
`semver.Compare`, `semver.ValidateTag`.

### Q-NPM-16 · Version ranges of exposure rules: strictness, pre-releases, bound

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-NPM-048 says `ValidateRange` refuses what npm's own range parser refuses, and that pre-releases
follow the rule's `prereleases` flag, not the range's pre-release rule. Three points are open: which
mode of npm's parser is "its own", how a range is evaluated so that the flag decides, and whether
a range has a size bound. Verified on 2026-10-30 against node-semver 7.8.5 and npm-package-arg
(`semver.validRange(spec, true)`, line 462 of `lib/npa.js` in the current release): npm itself
reads dependency specifiers in loose mode, and its loose mode drops a comparator it cannot parse
instead of refusing the range; for example `>=2.0.0 <4..0` is refused in strict mode and becomes
`>=2.0.0` in loose mode. Without `includePrerelease`, `>=2.0.0 <4` does not contain `2.1.0-beta`; with
it, it does, and `4.0.0-rc.1` stays outside because the x-range's ceiling is `<4.0.0-0`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Loose acceptance, node's pre-release rule | A rule accepts what a dependency specifier accepts | Same text as in a `package.json` | A typo silently widens a rule: the ceiling of `>=2.0.0 <4..0` is dropped, and everything from 2.0.0 up is exposed. With node's pre-release rule `prereleases: true` exposes nothing the range does not name with a pre-release, so the flag does not do what R-EP-012 says |
| (b) Strict acceptance, node's pre-release rule | Typos are refused | No silent widening | The `prereleases` flag still has no effect on `>=2.0.0 <4`, so a rule cannot expose `2.1.0-beta` by any setting |
| (c) Strict acceptance, evaluation with `includePrerelease`, the flag decides | A range is a numeric interval; whether a pre-release inside it is exposed is the rule's `prereleases` flag | One place decides pre-releases, as R-EP-012 states; typos are refused; the ceiling `<4` still excludes the pre-releases of 4.0.0 | A rule's range does not behave like the same text in `npm install` for pre-releases (it is wider); the admin UI must say so |

**Choice:** (c), with a bound of 4096 bytes on the range text, and acceptance checked in both option
sets (`semver.ParseForExposure`).

**Rationale:**
- Exposure is a ceiling (R-EP-012): a rule that exposes more than its author wrote is a leak, and a
  rule that is refused is a visible error. That is why (a) is out, whatever dependency specifiers do.
- R-EP-012 gives the rule two independent dimensions, `range` and `prereleases`. Only (c) makes both
  mean something.
- Desugaring differs between the two option sets only at the largest version numbers (an upper bound
  of `<=1.0.9007199254740991` needs 2^53 once pre-releases are included, which no version can
  represent), so a range is accepted only if it parses in both; the rule then never meets a range it
  can save and cannot evaluate.
- node-semver has no size bound. A rule is typed by an administrator; 4096 bytes is far above any
  rule and keeps parsing cost independent of what an API client sends. Only text that node-semver
  would also parse and is longer than that is refused.
- The development agent would revisit this if administrators need a range that only loose mode
  accepts (they can then be shown the strict form of it) or if the rule dialog shows the evaluated
  range and the difference to `npm install` for pre-releases turns out to surprise people.

**Applied in the spec:** R-NPM-048 (version ranges: strict mode, `includePrerelease`, the bound).
Code: `semver.Validate`, `semver.ParseForExposure`, `semver.Options`.

### Q-NPM-17 · Limits of the streaming tarball pass and of manifest reading

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-ARCH-028 gives the limits of an entry read into memory (1 MiB, 100:1) and lets a format add a
streaming pass with its own finite limits. R-NPM-007 names two of them. A stream can still cost
more than those two bound: a tiny gzip that inflates for minutes, a header that declares a size
the stream never delivers, a million-entry name table, a JSON document nested 100,000 deep.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The two limits of R-NPM-007 only | `max_unpacked_size` and `max_tar_entries` on the declared sizes and the entry count | Nothing to explain | The declared size is not what is inflated (a padded or PAX-heavy stream costs more); entry names are kept without a bound; a gzip header may be megabytes; no depth limit on JSON |
| (b) Those two plus a bound on every resource the pass touches | Decompressed bytes capped at `max_unpacked_size + max_tar_entries × 2 KiB + 1 MiB`; a declared size over the limit refused before inflation; names of at most 1,024 bytes and at most 32 MiB kept; a gzip header of at most 64 KiB; `package.json` 1 MiB, depth 64, 100:1 with a 128 KiB allowance; README 64 KiB; compressed bytes capped as the publish body is | Every byte count the pass can reach is finite, and the 2 GiB `package.json` of the conformance kit is refused by its header | Six constants to maintain; the ratio check is approximate |
| (c) Add a whole-stream compression ratio | (b) plus a ratio over all entries | Cuts an inflation bomb early | A package of generated or sparse data legitimately compresses by 1,000:1; `max_unpacked_size` already bounds the CPU a bomb gets, and the request deadline cuts it off |

**Choice:** (b).

**Rationale:** (b) is the only option where each of memory, CPU and bookkeeping has its own number,
so a limit that is reached names itself in the error (413 `npm_too_large`, or 400
`npm_bad_publish` for a JSON depth, which no honest client produces). The per-entry ratio applies
where R-ARCH-028 puts it, to the entry read into memory. Its allowance of 128 KiB covers
the inflater reading ahead of the entry (the compressed position is measured per entry, and a
small entry may already be paid for), and with the 1 MiB cap it still refuses a `package.json` of
zeros. The defaults of `max_unpacked_size` (2 GiB) and `max_tar_entries` (100,000) stay the
configuration's (R-NPM-007). The development agent would revisit the constants if a real package
of the public registry exceeds one of them, or if a bomb crafted below them costs a replica
more than its deadline allows.

**Applied in the spec:** R-NPM-007 step 3 (the bullet on reading limits), the acceptance
criterion on oversize and over-ratio `package.json`. Code: `internal/formats/npm/tarball`
(`Limits`) and `internal/formats/npm/manifest` (`Limits`).

### Q-NPM-18 · Tarball archives that two readers could resolve differently

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-NPM-007 refuses duplicates, unclean names, two first components and a non-regular
`package.json`. A tar archive offers more room for ambiguity than that: a global PAX header
changes later entries in some readers, a sparse file expands to zeros that no byte count
shows, links can redirect a later write, a file system without case sensitivity installs
`Package.json` over `package.json`, and a second gzip member or trailing bytes are read by some
decompressors and ignored by others.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The rules of R-NPM-007 only | Everything else is accepted if Go's reader accepts it | No false rejection | The manifest trove checks need not be the one a client installs |
| (b) Refuse every archive feature a reader could resolve differently, allow in-tree links | One gzip member and no trailing bytes; zero padding only after the tar end; no PAX global header, sparse file, device or FIFO; UTF-8 names without control characters; a link that stays inside the package; nothing below a link or a file; no case variant of `package.json` | The check and the install see one archive; each rule closes a named variant | A rare archive from an unusual packer is refused with a message that names the entry |
| (c) Refuse every link | No link of any kind | Simplest to explain | `npm pack` and Yarn write none, but other packers do, and pacote ignores links anyway; the refusal would break a package npm installs |

**Choice:** (b).

**Rationale:** The rule behind R-NPM-007 is that the validator must read what the installer
installs. (b) applies it to every feature the standard library reader resolves in a way another
reader may not (verified on 2026-10-30 in node-tar 7.5.16 `parse.js`: a global extended
header is passed to the header of every later entry, while Go's reader ignores it; the
other features follow the tar formats, not a client run). Links that stay inside the package are accepted because pacote skips them and a
refusal would change nothing for npm, while a link that leaves the package or a write through
a link is the shape of an extraction attack on the extractors that do follow links. The case
rule covers `package.json` only: a general case-fold collision check would refuse packages that
carry `Makefile` and `makefile`, which install differently on each platform but do not decide
what trove checks. Unicode normalization collisions are not checked (standard library only);
this is the residual of the rule. The development agent would revisit (b) if a gate-row client
publishes an archive it refuses.

**Applied in the spec:** R-NPM-007 step 3 (the bullet on further archive rules) and its
acceptance criterion. Code: `internal/formats/npm/tarball`.

### Q-NPM-19 · How the manifest normalization is built and verified

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Q-NPM-6 chose to compare both manifests after the union of the clients' steps and left their
implementation open. The steps must follow npm exactly where a difference would reject a
package that npm itself published, and must stay idempotent.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A port of the steps, replayed against npm's own `prepare()` | One function per step on a generic JSON tree; a committed corpus (`testdata/prepare/corpus.json`) is run through `@npmcli/package-json` by `generate.js` and the result stored; the test requires that npm's output and the raw `package.json` compare equal | The model is checked against the program that produces the request manifests; a missing step shows as a failing corpus case | The expectation file is generated outside the build and must be regenerated when npm changes a step |
| (b) A list of tolerated differences | Compare raw and subtract known differences by field | Little code | Every client change is a new exception; it does not model the added `node-gyp rebuild` script |
| (c) Run npm's code | Shell out to node | Exact | A JavaScript runtime in the replica; untrusted input into it |

**Choice:** (a).

**Rationale:** The corpus caught a difference while the code was written: npm lists the
folders below `directories.bin` as bin entries, which a port from reading the source had missed.
It runs on every `go test`, so a step that drifts from npm fails there. Beyond npm's steps, three conventions keep the comparison from rejecting what
installs identically: an empty or absent compared field is the same, the bundled list is
compared as a set, and a hosted-git range is written in the canonical form of
hosted-git-info for the four hosts the spec names (verified against its 9.0.3 on 80 ranges,
one of which, `github:u`, npm writes as `github:null/u` and trove leaves alone). A `name` or
`version` mismatch between request and tarball is `npm_manifest_mismatch` in both modes,
because it is not an install-relevant field that `warn` may accept but the identity of the
version. `hasInstallScript` is stored only when true, as the registry does, and
`_hasShrinkwrap` always. What is **not verified**: Yarn 1's steps (the `binding.gyp` rule and
`engines` arrays) follow the spec text, since no Yarn runs in the development environment;
`directories.bin` cannot see an empty folder of the source tree, which npm lists; the
gist host and ranges that need percent-decoding stay unchanged. The development agent would
revisit (a) if the fixture corpus of the gate rows shows a rejection no step explains, which is
the revisit condition of Q-NPM-6, or if more than a handful of steps are missing.

**Applied in the spec:** R-NPM-007 step 3 (the name and version bullet, the three
conventions after the step list). Code: `internal/formats/npm/manifest`; corpus:
`internal/formats/npm/manifest/testdata/prepare/`.

### Q-NPM-20 · How the publish body is read

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-NPM-007 step 2 forbids holding the decoded tarball in memory, and the base64 form of a 256 MiB
tarball is a JSON string of 341 MB. `encoding/json` returns a string as one value, so a decoder
holds all of it. The order in which libnpmpublish writes the members of the body was **not
verified**, and pnpm and Yarn build their own bodies.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `encoding/json` with a custom reader | Decode the document with `Decoder.Token` | Standard library | A string token is a whole string: the attachment is in memory before the first byte is decoded |
| (b) A scanner over the byte stream | `internal/formats/npm/publish`: every member is read within a byte budget and a depth limit, the value of `data` is decoded from base64 while it streams into the caller's writer | Memory is bounded by the buffers; the order of the members is not assumed (the manifest with its digests may follow the data) | About 400 lines of parser that must be fuzzed |
| (c) Require an order | Refuse a body whose metadata follows the attachments | A simple pass | Fails any client that writes the other order, which was not verified for four clients |

**Choice:** (b).

**Rationale:**
- It is the only option that holds R-NPM-007 step 2 for every client. `FuzzPublishScanner` and a test that
  streams a 96 MiB attachment and asserts that the scan allocated less than 16 MiB pin the property.
- The scanner is strict where two JSON readers could differ: a member twice, data after the document, a control
  character inside a string, an escape other than the ones a base64 string can carry (`\/` and `\u00XX`).
- The bounds: the body outside the attachment data is at most 768 KiB (the manifest with its README, the
  descriptors and the dist-tags), and a provenance bundle of at most 256 KiB adds 342 KiB of base64 outside that
  budget, so that R-NPM-007 step 2 adds 1,110 KiB to the base64 of the tarball (Q-NPM-53; the sum was
  once written as "below 1 MiB", which 768 KiB and 342 KiB do not give), nesting at most 64 levels, a member name at
  most 1,024 bytes, at most 4 attachments. A README that does not fit the budget makes the publish a 413.
- The tarball is spooled first and validated from the spool (`Inspect`'s way, the pass of `tarball.Validate` reading the
  spool), not validated while it streams: the digests a client announces may follow the data, and the contract's
  `Ingestor.Spool` takes a reader.
- The development agent would revisit the 768 KiB budget if a client sends manifests with larger READMEs
  (npm itself truncates none), and (c) never.

**Applied in the spec:** R-NPM-007 steps 2 and 3 (the order of the members is not assumed; the budget). Code:
`internal/formats/npm/publish`.

### Q-NPM-21 · How the format reaches the core and its own tables

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

The architecture test lets a format import the contract, `errs`, `config`, `observability`, `untrusted` and its own tree
(R-ARCH-008). npm needs more: it writes its own tables (D-1), records audit events in the transaction of a write, takes
the `npm.*` configuration and exports metrics, and a process may hold several replicas that each have their own.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Widen the allowlist | npm may import `audit`, `meta` and the metrics registry | No new seam | A format could then write SQL and every other format could ask for the same |
| (b) The data port carries it | Audit events are recorded by the data package in the transaction, the values cross in a leaf (`internal/formats/npm/rows`); settings and metrics are bound per Host by the composition root (`Format.Bind`) | The allowlist stays; one place per replica holds the replica's state; two replicas in one process do not share metrics | One more leaf package and one layer row |
| (c) Package variables | The format keeps its configuration and metrics in variables set at start-up | Shortest | R-ARCH-032 forbids package-level state, and two replicas in one test process would overwrite each other |

**Choice:** (b). `internal/formats/npm/rows` is a leaf (layer table row, no import of the module) that the format and
`internal/formatdata/npm` share; the data port persists what the format decided, so the SemVer order stays in the format
(the latest rule and the latest guard are computed there and the store receives the complete tag map).

**Rationale:**
- The audit event of a write is part of its transaction (R-OPS-015): the port is where the transaction is.
- Binding per Host is what the request already carries (`rq.Host`); a Host nobody bound runs on the defaults without
  metrics, which is what the conformance kit's Host is.
- Two contract details followed: `PackageWriter.AutoClaim` returns the `AutoClaimResult`, which the audit event of the publish
  records, and `SetupData` gained `RegistryKey` and `RequiresAuth`, because the keyed credential line of R-NPM-041 is the registry URL without its
  scheme and the always-auth switches depend on anonymous read.
- The development agent would revisit it if a second format needs the same, which would argue for a generic port base.

**Applied in the spec:** R-NPM-044 (the metric label is `repo`, the name R-OPS-007 allows; see Q-NPM-25). Code: `internal/formats/npm/{store.go,bind.go}`, `internal/formats/npm/rows`,
`internal/formatdata/npm`, `internal/app/wire_formats.go`; contract: [architecture.md](../architecture.md) R-ARCH-009.

### Q-NPM-22 · The provenance bundle of a publish

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. **Superseded by Q-NPM-58**: option (c) was built once the item store could commit several files in one transaction (Q-ARCH-228).

A publish with a provenance bundle stores two items, the tarball and `<name>/-/<basename>-<version>.sigstore` (R-NPM-006). The item
store commits one item per transaction, and a commit of an item runs the format's hook.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Tarball first, bundle second | The bundle's commit links it to the live version | The version becomes visible with the first commit, and nothing refused can leave an item; no path is held by an unfinished publish | If the second commit fails, the version exists without its bundle |
| (b) Bundle first | The tarball's commit links it | The version never exists without it | A publish that is refused (ownership, a conflict) after the first commit leaves an item at a path of a version that does not exist, and the next publish of that number with another bundle (re-signed bundles always differ) is refused |
| (c) A second transaction mechanism in the item store | Commit several items in one transaction | Atomic | Changes the item store for one format |

**Choice:** (a). The answer to the request is the 201 only after both commits; a failure of the second is a 500 that says the version is published and
its bundle is not stored, and a retry of that request is the 409 of R-NPM-008 (the retry conditions of step 4 name the bundle, so it does not match).

**Rationale:**
- A version without a bundle is the lesser harm; an item at a version's path that blocks the legitimate publish is the greater, and an attacker with `deploy` could plant it.
- The window is the time between two commits of one request, and there is no repair path in the MVP: the bundle is stored, not interpreted (PKG-NPM-024 is deferred).
- `rev_seq` grows twice for a publish with a bundle, once for each commit that changes the package rows.
- The development agent would revisit (a) when attestations are served (R-NPM-027), because then a half-published version is visible to `npm audit signatures`; (c) would be the answer.

**Applied in the spec:** R-NPM-007 steps 5 and 6. Code: `internal/formats/npm/{hooks.go,publish.go}`.

### Q-NPM-23 · Dist-tag decisions, the latest guard setting and what `warn` stores

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-NPM-016 puts the latest rule and the latest guard in the transaction of the write, and R-NPM-007 says the stored manifest stays the request's. Two
statements need an owner: who computes the tags, and what is stored in `warn` mode (the configuration description said the tarball's values).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The store computes | SQL compares versions | Next to the data | SemVer in SQL, or a second implementation of the order |
| (b) The format computes | `tagAfterPublish` and `tagsAfterRemoval` use `semver` on the tags read under the package row lock; the store writes the map | One order, tested as a table | The format reads the tags (`Store.Tags`) before it writes them |

**Choice:** (b). `warn` accepts the publish, records `npm.publish.manifest_mismatch` and stores the request's manifest, as R-NPM-007 says; the
description of `npm.publish.manifest_consistency` is corrected. `npm.latestGuard` is on for every repository: the repository document has no
format section yet, so the setting of R-NPM-016 cannot be written, and a published version never moves `latest` backwards. *Update:* the format section exists (Q-NPM-59), and the guard is a setting of local repositories, on by default.

**Rationale:**
- The guard exists to stop a maintenance release from becoming `latest`; leaving it on until the setting exists errs towards the safe side, and the
  rows' `LatestKept` flag is in the audit event, so the day the setting exists nothing changes for existing data.
- Storing the tarball's values in `warn` would change what the document lists against what the publisher sent.
- The development agent would revisit it when a format section of the repository document is built (not planned). It is built (Q-NPM-59).

**Applied in the spec:** R-NPM-016 (the guard is always on until the setting exists), R-NPM-007 (the description of `warn`). Code: `internal/formats/npm/tags.go`.

### Q-NPM-24 · What an npm repository takes through `/files/` and what `Inspect` answers

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

An item that reaches an npm repository through the raw view would be a file no document lists, at a path R-NPM-008 cannot protect. Separately, the
conformance kit asks `Inspect` for 413 or 422 and the core's `untrusted` limits (10,000 entries, 64 MiB read into memory) are lower than npm's
publish limits (100,000 entries, 2 GiB unpacked).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Accept raw uploads | The hook ignores an item that comes without a publish | Nothing to build | A tarball can bypass every rule of R-NPM-007 and R-NPM-008 |
| (b) Refuse them | `OnCommit` of an upload without the publish's payload is 400 `npm_bad_publish` until the raw deploy of R-NPM-036 is built | R-NPM-008 holds without exception | `/files/` cannot add to an npm repository |
| (c) Inspect with the publish limits | One limit set | One set | Fails the kit's crafted inputs, and a caller of `Inspect` gets 2 GiB |

**Choice:** (b), and `Inspect` uses the limits of the inspection (`untrusted`, tightened by the descriptor: 1 MiB `package.json`, depth 64, ratio 100:1) and answers 422
`content_refused` where the publish answers 400 `npm_bad_publish` and 413 where a bound is crossed. A publish validates with `npm.publish.*`, which are the format's own.

**Rationale:** R-NPM-007 states 400 for a malformed or ambiguous archive, so the publish keeps it; `Inspect` is the contract's path for content that is not a publish, which the kit
holds to 413 or 422. The development agent would revisit (b) with R-NPM-036. Since [architecture.md](../architecture.md) Q-ARCH-223 the generic layer refuses a deploy through
`/files/`, file or folder, with `409` before the body is read; the refusal of `OnCommit` stays for any other road that commits an upload without a publish.

**Applied in the spec:** R-NPM-007 (a note after the processing steps), R-NPM-036. Code: `internal/formats/npm/{hooks.go,publish.go,format.go}`.

### Q-NPM-25 · The shape of refusals, audit outcomes and metric labels

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Four statements of the page cannot be built as written: R-NPM-001 and R-NPM-043 want 501 for a `POST`, and the error model has no 501; R-NPM-007 and R-NPM-044 name
the outcome `retry_acknowledged`, and an audit outcome is `success`, `failure` or `denied`; R-NPM-044 names a metric label `repository`, and
R-OPS-007 allows `repo`; R-NPM-005 names the log fields `npm-command` and `npm-session`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Bend the platform | A fourth audit outcome, the label `repository`, a free status in errors | Matches the text | Changes the audit table, the label list and the error kinds for one format |
| (b) Fit the page to the platform | A new error kind `NotImplemented` (501); outcome `success` with the reason `retry_acknowledged`; the label `repo`; the log fields as named | The platform's rules stay | The page is corrected in three places |

**Choice:** (b). `errs.NotImplemented` is the only addition to the error model. The unimplemented endpoints share one matcher, so each family answers GET, PUT and DELETE with 404 and POST with 501
by four routes instead of a route per path; a 404 carries `not_found`, a 501 `npm_not_implemented`. The audit event of the acknowledged retry is `npm.publish` with outcome `success` and
reason `retry_acknowledged`. The metric label is `repo`, capped by the repository limit of the metrics section.

**Rationale:** The statuses and names clients see do not change. The label limiter (`CappedLabels`) has no component that feeds it the repository keys yet, so every `repo` value is `_other` today;
the format asks the limiter and is correct once it is fed.

**Applied in the spec:** R-NPM-005 (501 stands for the not-implemented kind), R-NPM-007 step 4, R-NPM-044 (outcome, label). Code: `internal/errs`, `internal/audit/events_npm.go`, `internal/formats/npm/{routes.go,bind.go}`.

### Q-NPM-26 · How a package document is rendered, cached and validated

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: Q-NPM-10 fixed where documents are cached (per replica, in process, under keys every replica reads from PostgreSQL). Left open was what the cached bytes are: R-NPM-010
puts absolute `dist.tarball` links into the document, and the response base URL differs per endpoint and host (R-EP-003). The rendition cache of the core keys by base URL, endpoint and exposure
fingerprint already ([architecture.md](../architecture.md) R-ARCH-018, `formats.RenditionKey`).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Cache the rendered bytes with their links | The key holds the base URL, the endpoint and the exposure fingerprint; the bytes hold the absolute links | A hit is a copy of bytes; the validator and the key are the same tuple (R-NPM-013); nothing is rewritten per request | One entry per base URL and endpoint of a package revision |
| (b) Cache the bytes with a placeholder for the registry URL and substitute at serve time | The key has no base URL | One entry for all base URLs | A second pass over megabytes on every hit; a placeholder can collide with package content; cached bytes are not the served bytes |
| (c) Render on every request | No rendition cache for npm | No cache state | 40 to 60 ms per request for 1,000 versions against 3 ms; the load of Yarn 2+, which revalidates documents on every install, falls on the renderer |

**Choice:** (a). Rendering is a pure function of one snapshot: the revision, the tags, the versions and the README of the version `latest` points to are read in one statement
(`NPMLocalDocument`), because an entry keyed by a revision must hold the rows of that revision. Each request reads `packages.rev_seq` (one indexed lookup), derives the ETag from it without
rendering, answers `304` on a match, and otherwise takes the rendering from the cache or renders and stores it. When the rows read are newer than the lookup, the rendering is keyed and
validated by the revision of its own rows. The cache holds the identity form; the gzip form is made from it once and cached under the encoding. An entry starts with the
`time.modified` of its rows (8 bytes), so `Last-Modified` costs no query on a hit. A document above an eighth of `cache.rendition_bytes` is rendered per request, as the core's cache rule says.
`GET {name}?write=true` is read from PostgreSQL in the request and never cached; it carries `Cache-Control: no-store` and no ETag, because a client that writes needs the revision of the
moment and must not revalidate it.

**Rationale:** (a) keeps the one tuple of R-NPM-013 for the validator and the key, and the links in the bytes are those the base URL of the request produced. Measured on the development machine:
rendering 1,000 versions of 2.7 MiB takes 21 ms (benchmark `BenchmarkR_NPM_010_RenderThousandVersions`); over the integration stack a miss takes 37 to 62 ms, a hit 3 ms, a `304` less than that.
The development agent would revisit this if the reference load test (R-HA-050) shows memory of the rendition cache as the limit, since every base URL and endpoint multiplies the entries of a hot package.

**Applied in the spec:** R-NPM-009 (the write-read has no validator), R-NPM-010 (one snapshot), R-NPM-013 (the cached bytes hold the links; the key holds the base URL; the gzip form shares the validator).
Code: `internal/formats/npm/{handlers_document.go,document/}`, `internal/formatdata/npm/read.go`.

### Q-NPM-27 · How an unpublish removes a version

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-NPM-017 deletes each removed version "through storage.md R-STOR-018" and tombstones it; R-NPM-008 wants the tombstone to outlive the version and its items; a raw delete of a tarball
through `/files/` must unpublish "the same way" (`OnItemsRemoved`). The format contract had no way for a format's route to remove items, and the item store removes in a transaction of its own.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The route removes the files through the package writer, in its transaction; `OnItemsRemoved` does the rest | New `PackageWriter.DeleteItems`; the item store removes the files in the caller's transaction and calls the hook there | One transaction holds the ownership check, the revision compare, the removal of the files, the tombstone, the tags and the audit event; a raw delete takes the same road | One more method in the contract (`internal/formats`, `internal/formathost`, the kit) and one in the item store |
| (b) The route tombstones and deletes the version row; the files stay stored | Only `Tombstone` and `TrashOrDeleteVersion` | No contract change | The bytes of every unpublished version stay in the object store and in `/files/` listings; R-STOR-018 is not met; storage usage never falls |
| (c) The route calls the item store's own delete first | `Items.Delete` in its transaction, then the format's | No contract change | Two transactions: the ownership check and the revision compare are not atomic with the removal, and a hook that locks the package row waits for the route's lock |

**Choice:** (a). `PackageWriter.DeleteItems(repo, paths...)` removes the files permanently (folders go with their subtree, empty implicit folders above them are cleaned) as the actor of the
request, and `OnItemsRemoved` runs for them in the same transaction with a writer bound to no request. For npm, the removal of a tarball is the unpublish of its version: tags are recomputed
without it (R-NPM-016), the tombstone is written with the integrity of the version row, the version row is deleted (trashed when the reason is `trash`, P1) and `rev_seq` is raised once, with
the audit event and the journal entry of Q-NPM-12. The removal of a bundle alone raises the revision and removes no version. A version that is stored and not in the document (withheld by a
replay) loses its row and keeps its tombstone: this is the administrator's confirmation of R-NPM-047. A removal that no principal asked for records the nil UUID as the actor of the tombstone,
because the column is a non-null UUID; the journal entry then carries it as `actorId`. A route that removed all files of a version but found the version still live, because the files were not
there, removes the version all the same.

**Rationale:** (a) is the only option in which the rule of R-NPM-017 "ownership and the current `{rev}` inside the write transaction" and the rule of R-NPM-008 "the tombstone is written with the
removal" hold together, and it gives `/files/` the road for free. The item store's hook already ran inside the removal transaction, so the hook of a format needed no new place to run. The
contract is not frozen before the end of M3 (Q-ARCH-213), so the method was added to every implementation of the writer in the same change. Ownership for a raw delete is the generic layer's
(a coordinate with a package name is checked there); the hook decides none.
The development agent would revisit this if a second format needs to remove items outside a package it owns, or when the trash can (R-STOR-022) lands and restores need the path of the removal.

**Applied in the spec:** R-NPM-017 (the removal is one transaction), R-NPM-008, [architecture.md](../architecture.md) R-ARCH-009 (the writer's method). Code: `internal/items/delete.go`
(`RemoveIn`), `internal/formathost/writer.go`, `internal/formats/hooks.go`, `internal/formats/formattest/store.go`, `internal/formats/npm/{handlers_unpublish.go,hooks_remove.go}`.

### Q-NPM-28 · The tarball response and the serve-time check

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-NPM-013 asks tarballs for the strong ETag `"sha256:<hex>"` and `Cache-Control: max-age=31536000, immutable`, `public` only if the anonymous principal may read the package. R-STOR-012 fixes
the ETag of every stored file at `"<sha256 hex>"` and the core sets `Cache-Control: no-cache` (`private, no-cache` for an authenticated request). R-NPM-015 has the tarball route and `/files/`
refuse tombstoned and withheld versions.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Keep the core's ETag; set `private, max-age=31536000, immutable` on the status line | The format wraps the response writer and sets Cache-Control for 200, 206 and 304 | One validator on `/files/` and on the npm route (the acceptance criterion of R-NPM-015 wants the same ETag on both); clients keep a tarball a year without a request | The format overrides a header of the core's serving |
| (b) Rewrite the ETag to `"sha256:<hex>"` | As the page says | Matches the page | Two validators for one file; the core's conditional handling compares the other form |
| (c) Keep both headers of the core | Nothing set | No override | Every install revalidates every tarball |
| (d) `public` when the anonymous principal may read | A decision per response for the anonymous subject | Shared caches may keep public packages | The host has no call for another subject's decision; a policy change would not reach a cache that holds the response |

**Choice:** (a), always `private`. The ETag stays `"<sha256 hex>"`, which is strong and the same on every route and replica. `ServeCheck` reads the tombstone through the data port (one indexed
lookup per served file, no state in the replica) and answers the 404 of a missing resource for the tarball and the bundle of a tombstoned version. It runs for local repositories only; the
remote class adds its own marks.

**Rationale:** Immutability under a number is the property clients and proxies can use; `private` keeps a shared cache from handing a file to a caller the policies exclude. (d) would need a
decision that nothing outside the decision point should make.
The development agent would revisit `public` if the host gains a way to ask what the anonymous principal may read.

**Applied in the spec:** R-NPM-013 (the tarball bullet), R-NPM-015, R-NPM-047. Code: `internal/formats/npm/{handlers_tarball.go,servecheck.go}`.

### Q-NPM-29 · Dist-tag writes and the identity endpoints

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-NPM-016 and R-NPM-019 leave open what a tag write that changes nothing does, what the body is, and who counts as authenticated for `ping`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A write that changes nothing writes nothing | A tag already at the version answers 200, raises no revision, writes no audit event | `rev_seq` and the validator move only with a change; a retry is harmless | A caller cannot use a tag write to force a new validator |
| (b) Every write raises the revision | | Simple | A retried `npm dist-tag add` invalidates every cached document and writes an audit event for nothing |

**Choice:** (a). The body is a JSON string holding a canonical version (400 `npm_invalid_version` otherwise); the tag follows `semver.ValidateTag` (400 `npm_invalid_tag`); the version must be one
the document holds (400 `npm_invalid_tag`); the answer is `{"ok": true}`. The ownership question is asked under the package row lock with the action `deploy` and `firstVersion=false`, before the
version is looked up, so a caller who does not own the name gets 403 whatever the body says. A write never creates a package: an unknown name is 404 before the transaction takes any lock.
`ping` and `whoami` need an identity: an anonymous request, and one whose token the authentication stage no longer accepts (it is anonymous to the chain), get 401 with the challenge of R-NPM-004,
also on a repository that anonymous callers may read. Both answer `Cache-Control: no-store`, and `ping` ignores its query.

**Rationale:** R-NPM-019 names the "authenticated subject" for both endpoints and the acceptance criterion needs `npm ping` to fail after a revocation on every replica; an endpoint that answers
200 to the anonymous subject could not.
The development agent would revisit this if a client is found that pings an anonymously readable registry without credentials and treats a 401 as a failure.

**Applied in the spec:** R-NPM-016 (a write that changes nothing), R-NPM-019 (anonymous callers). Code: `internal/formats/npm/{handlers_disttags.go,handlers_identity.go}`.

### Q-NPM-30 · The rules of the unpublish requests

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-NPM-009 and R-NPM-017 define the stale-revision rule and the three requests; the details that decide behavior under retries and replays are open.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Decide on the document's versions under the lock | The baseline of a `PUT` is the versions the document holds (not trashed, not tombstoned), read under the package row lock; the revision is compared only when something would be removed | A stale retry that removes nothing is a 200 on every replica; a withheld version (stored and tombstoned) is never removed by a client's `PUT`, which does not list it | Two reads under the lock |
| (b) Decide on the stored versions | The baseline is every version row | One read | A client's `PUT` would remove a withheld version it never saw; an administrator's confirmation (R-NPM-047) would be an accident |
| (c) Compare the revision first | Stale means 409 | Simple | The retry of a request whose answer was lost fails (R-NPM-009) |

**Choice:** (a). In detail: the body is read as a stream and only the keys of `versions` are kept (a document of 1,000 versions is megabytes; 128 MiB bounds it, 413 beyond); a body without a
`versions` object, a duplicate `versions` member or a version the document does not hold is 400 `npm_bad_publish`; the answer is `{"ok": true, "rev": "<current rev>"}`, the new one after a
removal. `DELETE {name}/-/{file}.tgz/-rev/{rev}` compares no revision, because it changes nothing: 200 when the version is not in the document, 400 `npm_bad_publish` when it is. A package that
has no row is 404 and none is created. The package row stays after the last version, so `rev_seq` never decreases.

**Rationale:** (a) is what makes the retry rule of R-NPM-009 true on any replica, and it keeps the replay's withholding (R-NPM-047) from being undone by an ordinary client.
The development agent would revisit the 400 for a still-published version if a client is found that sends the tarball `DELETE` before the document `PUT`.

**Applied in the spec:** R-NPM-009, R-NPM-017 (the answers). Code: `internal/formats/npm/handlers_unpublish.go`.

### Q-NPM-31 · What the documents derive from the rows

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-NPM-010 lists the members of the full document and where they come from; a few derivations are not spelled out.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Derive from the rows, in a fixed order, without per-version READMEs | `versions` and `time` in SemVer order; `deprecated` from the column; `readme` of the version `latest` points to (the manifest's, else the tarball's) and no `readme` in any version object; `maintainers` from the `_npmUser` of the versions present, by name; `time.created` from `packages.created_at` | The bytes depend on the rows alone, so every replica renders the same document; 1,000 versions do not carry up to 64 MiB of READMEs | A client that reads `versions[v].readme` finds none (the public registry strips it as well) |
| (b) Keep what the stored manifests hold | Publish order, READMEs as published | No derivation | The bytes depend on the order of the rows and on what each client sent |

**Choice:** (a). The abbreviated document has the members of R-NPM-011 and `dist` complete; the single-version document is the entry of the full document of the same revision, found by a
canonical number or by a dist-tag (a tag is never a valid version, so the two cannot be mistaken); an unknown reference is 404. A version whose `deprecated` column is empty has no `deprecated`
member, whatever the stored manifest says.

**Rationale:** Equal bytes on every replica are what lets the ETag be a function of the inputs (R-NPM-013).
The development agent would revisit the README rule if a client is found that reads it from the version objects.

**Applied in the spec:** R-NPM-010, R-NPM-011, R-NPM-014. Code: `internal/formats/npm/document/`.

### Q-NPM-32 · Where the stale-copy rule and the newer-entry rule are decided

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-NPM-024 wants the stored document and its records to describe one document whichever of two fills commits last. The engine reads the entry before the fetch, so a first fill that a concurrent fill overtook has seen no entry, and the entry lock alone does not serialize the records.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) In the engine's upsert, the format is told | The core compares `fetched_at`, the format receives `Replaces` | The core owns the comparison | A fill that saw no entry never learns that one appeared |
| (b) In the data port, after the lock of `npm_remote_packages` | `ApplyFill` locks the package record, reads the cache entry as it is now, and reports `Superseded` (the stored entry was fetched later) or `Stale` (older `time.modified`); `OnFill` keeps the stored document for both (the order of the two rules: Q-NPM-57) | Decides on what is stored at that moment, in the fill transaction, with the same comparison as the upsert | The format repeats the core's `fetched_at` comparison; the engine renews the entry that exists instead of trusting `Replaces` |

**Choice:** (b). `FillEvent` gains `FetchedAt`; the engine renews the stored entry whenever the format keeps it and a row exists to renew.

**Rationale:**
- The two-replica test with Valkey stopped, in both orders, failed under (a): 7 versions listed, 1 unlisted and an `npm.remote.version_removed` event, because the older answer overtook a first fill.
- Revisit if the core takes over the comparison for formats that record per-document facts.

**Applied in the spec:** R-NPM-024 (the two-replica criteria hold in both orders). Code: `internal/formatdata/npm/remote.go`, `internal/proxy/metadata.go`.

### Q-NPM-33 · How tarballs meet the removal marks and the currency of the document

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-NPM-025 and R-NPM-026 apply the marks on every road to a stored tarball: the npm route, `/files/`, the cache view and the tiers of a virtual repository (Q-NPM-13).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Only the npm route | The handler checks the marks | Simple | `/files/` and virtual tiers serve withdrawn versions |
| (b) `ServeCheck` for the stored marks, an artifact gate of the remote adapter for the roads that may ask the upstream | `ServeCheck` reads `TarballFacts` (one statement) and refuses an unlisted version or a gone package unless `serveRemovedVersions`; `BeforeArtifact` first revalidates a document that is not current (expired, or validated before the last restore) in one flight, and returns the announced digests | Every road applies the marks; only roads that may ask the upstream do | Two entry points into one rule |

**Choice:** (b). The gate also runs for the cached tier of a remote member of a virtual repository. When the upstream cannot be asked and the version has a record, the stored marks decide.

**Rationale:**
- The marks are PostgreSQL state, so a loss of Valkey's data changes nothing (INV-3).
- Revisit when a second format needs a gate: the interface then moves into the contract proper.

**Applied in the spec:** R-NPM-025, R-NPM-026. Code: `internal/formats/npm/remote.go`, `servecheck.go`, `internal/formathost/remote.go`.

### Q-NPM-34 · Tarball addresses of merged documents

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-NPM-025 and R-REPO-043 put every `dist.tarball` on the serving repository's registry URL. The merged-metadata cache is shared by callers who reach a virtual repository through different hosts.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Merge with the serving URL | A cache entry per base URL | No rewrite after the merge | The key multiplies by endpoints; a host can leak between callers |
| (b) Merge with a neutral address, rewrite after the cache | Local members render with `urn:trove:npm-tarball:`, the merge keeps each member's entry, the handler rewrites every address from the request's base URL | One cached merge for every endpoint; no host in cached bytes | A rewrite per rendering, itself cached by the rendition cache |

**Choice:** (b). The rendition key and the ETag carry the merge fingerprint, the base URL, the endpoint and the exposure fingerprint; the synthetic `_rev` is `1-` and 32 hex characters of the fingerprint.

**Rationale:**
- Revisit if the rewrite shows in the render histogram.

**Applied in the spec:** R-NPM-025, R-NPM-030, R-NPM-013. Code: `internal/formats/npm/virtual.go`, `handlers_remote.go`, `handlers_virtual.go`.

### Q-NPM-35 · serveRemovedVersions and the document (D-16), and where the setting is read

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-NPM-026 says unlisted versions stay served with `true`; Q-NPM-5 stores only the current upstream document, which has no manifest for a dropped version. The repository document has no format section yet (Q-NPM-23).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Tarballs stay served, documents do not list the version | The cached tarball of an unlisted version, and those of a gone package, are served on every route; a document never lists what the current upstream document lacks | Lockfile installs keep working; no second representation | `npm view` does not show the version |
| (b) Keep each unlisted manifest | A second copy in `npm_remote_versions`, merged back | Documents list it | Rejected by Q-NPM-5 |

**Choice:** (a). The npm member of the stored repository document (`npm.serveRemovedVersions`, `npm.latestStrategy`, `npm.distTagStrategy`) is read as it is, with defaults when absent. *Update:* the repository service accepts and validates the member now (Q-NPM-59).

**Rationale:**
- Revisit when the repository document gets a format section: validation and the OpenAPI schema join, the reading stays. They joined (Q-NPM-59).

**Applied in the spec:** R-NPM-026, R-NPM-031. Code: `internal/formats/npm/settings.go`.

### Q-NPM-36 · Writes to remote and virtual repositories

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-NPM-001 answers 405 for a write to a remote repository, R-NPM-029 routes every write through a virtual repository to its deployment target.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Handlers check the class | Every write handler refuses | No dispatcher change | Twelve checks, one forgotten |
| (b) Write routes list `Classes: local`; the dispatcher redirects every write kind | The 405 is the dispatcher's; npm's `WriteError` words it `npm_class_not_writable` for a remote repository; a virtual repository's writes (publish, tag, `-rev`) go to the target | One place, and the route table says it | A write the decision point refuses first is a 403, not a 405 |

**Choice:** (b). A caller without `deploy` on a remote repository gets 403 before the class is looked at (R-AUTHZ-016); a virtual repository without a target is refused by the decision point (403) before the dispatcher could answer `no_deploy_target`.

**Rationale:**
- R-NPM-029 said 405; the decision point decides first (R-AUTHZ-009 rule 5). Written back.
- Revisit if the decision point should let a write reach the class check.

**Applied in the spec:** R-NPM-001, R-NPM-005, R-NPM-029. Code: `routes.go`, `errors.go`, `internal/api/dispatch/dispatcher.go`.

### Q-NPM-37 · Remote refusal and the cache view in the host

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-REPO-023 makes a remote repository refuse a name a global claim covers, also when addressed directly; R-REPO-032 serves `<key>~cache` from storage only.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) In the engine | The engine asks the routing | One place | The engine learns claims |
| (b) In `Host.Authorize` for reads of a remote repository, and in the dispatcher for the address | `Authorize` asks `RemoteRefused` and answers the 404 of a missing resource; the dispatcher strips `~cache`, requires a remote repository and sets `Request.CacheView` | Before any cache lookup; the engine stays free of claims | A format that skips Authorize skips the refusal |

**Choice:** (b). The cache view is the remote repository itself, marked; its tarball addresses name `<key>~cache`.

**Rationale:**
- Revisit when a second remote format exists.

**Applied in the spec:** R-NPM-033, R-REPO-032, R-REPO-043. Code: `internal/formathost/host.go`, `internal/api/dispatch/dispatcher.go`.

### Q-NPM-38 · Where a document is filtered

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-NPM-048 filters a document after the merge of a virtual repository and the rewrite of a remote document, and the exposure
adapter of R-EP-018 filters bytes (`FilterDocument`). A local document is rendered from rows (Q-NPM-26), and its rendition is cached
under a key that holds the endpoint and the exposure fingerprint already.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Rows for local documents, bytes for merged and rewritten ones, one decision | `exposeRows` filters the rows between the read and the rendering; `FilterDocument` filters the bytes of the other classes; both call `exposedVersions` and `exposedTags` | The local renderer stays the one place that writes `time`, `maintainers` and the top-level fields, so a filtered and an unfiltered local document follow the same rules; the README of the moved `latest` can come from its rows; no second parse of a local document | Two applications of the rule (a unit test renders the rows both ways and compares) |
| (b) Bytes for every class | Render, then `FilterDocument` | One application | Parses and re-encodes every local miss on a rules endpoint; the top-level fields of a moved `latest` can only come from what the rendered version object carries |
| (c) Rows everywhere | Merge and rewrite produce rows | One application | Merged and rewritten documents are bytes by design (D-8, Q-NPM-5); the remote and virtual classes would need a second representation |

**Choice:** (a). `FilterDocument` filters the package document and the single-version document of a version; the dist-tag map and the
version a tag names are read from the filtered package document, because only it knows where `latest` moved; any other path is refused
with an internal error rather than served unfiltered. A single-version request for a hidden version is answered before any lookup; a
tag request on a rules endpoint reads the whole document, filters it and takes the tag's version from the filtered tags.

**Rationale:** (a) keeps one renderer and one decision, and the test that renders the fixture's rows through both paths and compares
the parsed documents holds the two applications together. The development agent would revisit this if the remote or virtual handlers
need a filter of the rows as well (then (c) for them), or if profiling shows the extra parse of (b) is negligible and the README of a
moved `latest` is dropped anyway.

**Applied in the spec:** R-NPM-048 ("Where", "Other documents"). Code: `exposeRows`, `FilterDocument`, `exposedVersions`,
`exposedTags` in [exposure.go](../../../internal/formats/npm/exposure.go); the marked blocks of the document, dist-tag and tarball
handlers.

### Q-NPM-39 · The README when the filter moves latest

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-NPM-010 takes the README of a package from the version `latest` points to, and the local document reads it from the column
of that one version only (Q-NPM-31: no README per version). When the filter moves `latest` to another version, the row holds the
README of a version the endpoint may hide; a merged or rewritten document carries one top-level README only.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The README the new `latest`'s entry carries, else none | The README of the former `latest` is never shown; a local document shows one only when the moved `latest`'s manifest has one | Nothing of a hidden version is shown; no new query; one rule for every class | `npm view` on the endpoint shows no README until the stored `latest` is exposed again |
| (b) Read the README of the new `latest` from its row | An indexed lookup of one column when the filter moved `latest` | The README is the right one for local documents | A query and a store method for a cosmetic field; remote and virtual documents still have none for the moved `latest` |
| (c) Keep the former `latest`'s README | Nothing changes | Simplest | Shows the text of a version the rules hide, which is exactly what exposure exists to prevent |

**Choice:** (a). The same holds for every top-level field of R-NPM-010: a field the moved `latest` lacks is removed.

**Rationale:** The README is presentation, never what an install reads; (c) leaks and (b) buys presentation with a query on the hot
path. The development agent would revisit this if customers rely on the README of an endpoint in a registry browser, then (b) for
local documents.

**Applied in the spec:** R-NPM-048 ("Top-level fields"). Code: `exposeRows` clears the row's README when `latest` moved;
`filterPackageDocument` replaces or removes the fields.

### Q-NPM-40 · How the filter reads a document it did not render

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: `FilterDocument` runs on merged and rewritten documents whose bytes come from an upstream. JSON allows a member twice, and npm
clients parse with `JSON.parse`, which takes the last value; upstream documents carry members trove does not render (`users`,
`contributors`), `time` entries of unpublished versions and an `unpublished` record, and may lack `latest` or carry a tag that is not a
string.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Read like the client, filter what names versions, keep the rest | A repeated member counts with its last value at the place of its first; `versions`, `time` (all keys except `created` and `modified` are versions) and `dist-tags` are filtered; a tag whose value is no string is dropped; a missing `latest` is computed; members the filter does not know stay | No duplicate can carry an unfiltered copy past the filter; nothing a client installs from is dropped | A member the filter does not know is passed on as it came |
| (b) Refuse a document with a duplicate member | 500 for such a document | Strict | A remote's document becomes unservable on a rules endpoint for a JSON detail the client accepts |
| (c) Decode into a generic tree and re-encode | One decoder | Simple | Member order and number formatting change; the bytes of a 30 MB document are held twice as a tree (D-8) |

**Choice:** (a).

**Rationale:** The filter must see the document the client will see; `JSON.parse` semantics make that true for duplicates. Unknown
members are package-level, not version-level, in every document form of the registry API; the development agent would revisit this
when an upstream is found whose package-level member names versions, which would then be filtered too.

**Applied in the spec:** R-NPM-048 ("Dist-tags"). Code: `readMembers`, `filterPackageDocument` in
[exposure.go](../../../internal/formats/npm/exposure.go).

### Q-NPM-41 · Where the test upstream gets its packages

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

The counting test upstream (R-NPM-045) must serve at least 600 packages for exit criterion E2.2
(a reference lockfile of at least 500), scoped and unscoped names, prereleases, dist-tags, a few
documents with a thousand versions (the P-4 sizes), and the provenance shapes of R-NPM-027. The
same bytes must come out of every run, because the lockfile pins their integrity values.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Real packages | Download the packages named by a lockfile into a cache and serve them | Real shapes everywhere | Needs the network in CI or a large committed cache; install scripts and licences of third parties; the public registry changes, so the bytes and the lockfile drift |
| (b) Fully synthetic | Generate every document and tarball | Offline, small, deterministic, no install scripts | Cannot fake `_npmUser.trustedPublisher`, `dist.attestations`, `dist.signatures` or a legacy capitalised name faithfully |
| (c) Synthetic corpus plus a small recorded set | The corpus of (b) for scale, 700 packages from a seeded generator; five trimmed real documents with their real tarballs (`left-pad`, `JSONStream`, `lodash`, `react`, `sigstore`, 1.4 MB) for the shapes | Offline and byte-stable where scale matters; real shapes where fidelity matters | Two sources to describe; the recorded set is a snapshot taken by hand |

**Choice:** (c). The corpus is Go code (`test/npmstub/corpus`) over a seeded generator whose output
is fixed by its own algorithm, not by the standard library's. Tarballs have sorted entries, fixed
times and owners and stored (uncompressed) gzip blocks, so a toolchain change cannot alter a byte.
Two generations must give identical bytes, and their digest is committed. The recorded set is
written by `npmstub record` from a live registry, by hand only, and is served as recorded except
that every `dist.tarball` points at the stub; published, dropped and integrity-faulted changes apply
to corpus packages only.

**Rationale:**
- E2.2 and the load tests need volume and stability, which only a generated set gives without a
  network. No synthetic package has an install script, so no run executes foreign code.
- Stored gzip blocks make the integrity values independent of the Go version; a generator change
  shows as a digest change in `TestCorpusIsByteStable`, which is the signal to regenerate the lockfile.
- The recorded shapes are few and fixed; five packages cover the trust-policy, provenance, legacy
  name and widely used cases without growing the repository by more than 1.4 MB.
- The development agent would revisit this if a client scenario needs a shape none of the five
  documents has, or if the recorded snapshot ages so that a client rejects it.

**Applied in the spec:** R-NPM-045 (the stub's data and its acceptance criterion).

### Q-NPM-42 · How the test upstream is reached and controlled

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Go integration tests run the stub in the test process; the e2e suite runs it as one pod; a
person can run it as a container. All of them need the counters, the failure switches and a way to
offer or drop a version at run time, and the owner requires every listener to be TLS.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Plain HTTP in tests, TLS in the container | The library listens on plain HTTP for in-process tests | Least setup in tests | Two code paths; the in-process tests would not exercise trove's upstream TLS verification, and the owner's rule is not met by the test tooling |
| (b) TLS always; the control API on the same listener below `/-/stub/`, behind a bearer token | One listener; the library has no plain-HTTP start function; without a token the API does not exist | One code path; trove's outbound TLS verification is exercised in every tier; the in-process tests call Go methods and need no token | Control requests share the port with the registry (no package name can start with `/-/stub/`, and they are not counted) |
| (c) A second listener for control | Registry port and control port | The registry port is exactly a registry | A second certificate, port and Service; more to configure in kind |

**Choice:** (b). `Server.Start` refuses a configuration without a certificate and raises the minimum
to TLS 1.2; the container refuses to start without `-tls-cert`/`-tls-key` or `-self-signed`, reads
the certificate again when its files change (a rotation needs no restart), and enables the control
API only when a token file is given. Counters are kept per method, escaped path, conditionality and
credential scheme (never the credential), so `%2f` and `/` spellings of a scoped name stay apart
while `Counts.Documents(name)` adds them.

**Rationale:**
- One path means a test cannot pass in a mode production never uses.
- The counters are in memory in one process, so the kind deployment has one replica; a second
  replica would split the counts and E2.2's "exactly one" could not be read.
- Keeping the control API off by default makes a stub started without a token a plain registry
  that nobody can steer.
- The development agent would revisit this if a scenario needs the registry and its control on
  different network paths (for example to test a NetworkPolicy), which (c) would give.

**Applied in the spec:** R-NPM-045 (TLS-only stub with a control API).

### Q-NPM-43 · Which npm writes the reference lockfile, and how it is kept honest

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

`perf/npm/reference/package-lock.json` is the lockfile exit criterion E2.2 installs three times at
once. A lockfile written by whichever npm a developer has installed would differ per machine, and
the repository pins one Node (`web/.nvmrc`).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The host's npm | Run `npm install --package-lock-only` locally | No container | Differs per machine; contradicts one version per tool |
| (b) The npm bundled in the pinned Node image, in a container, against the stub in a container | `tools/versions.env` pins `E2E_CLIENTS_NODE_IMAGE` by digest, with a tag equal to `web/.nvmrc`; a script resolves with `--package-lock-only --ignore-scripts` on a throwaway Docker network and CA; a Go test checks the committed lockfile against the corpus without Node | One writer, pinned once; reproducible on any machine with Docker; offline | Needs Docker for regeneration |
| (c) A separately pinned npm installed into the image | Install an exact npm version at run time | Independent of the Node image's bundled npm | A second pin for the same tool family and a network fetch at generation time |

**Choice:** (b). The script `perf/npm/reference.sh` refuses an image whose Node version differs
from `web/.nvmrc`. `make npm-reference` writes the files, `make npm-reference-check` regenerates and
fails on any difference, and `TestReferenceLockfileMatchesTheCorpus` (no Docker, no Node) verifies
that the project is the corpus's reference project and that every locked package is in the
corpus with the corpus's integrity and the dev profile's `npm-all` address.

**Rationale:**
- The lockfile has one writer version: the npm of the Node every other tool in the repository uses.
- The `resolved` addresses are those of the dev profile (`https://localhost:8080/api/npm/npm-all/`),
  produced by the stub's `-public-url`, so the e2e suite installs the file unmodified.
- The Go test makes every `make test` notice a drift between corpus and lockfile; the Docker check
  catches a different npm output and belongs in CI.
- The development agent would revisit this if the clients image of the client matrix pins a
  different Node than `web/.nvmrc`, since both then read one key and would have to be split.

**Applied in the spec:** R-NPM-045 (the reference project and its acceptance criterion).

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
repository's Node.js is 26.13.0 (`web/.nvmrc`).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One Node.js, the repository's | One image `FROM node:<web/.nvmrc>-trixie-slim`, pinned by digest in `tools/versions.env`; `make versions-check` compares the tag with `web/.nvmrc` | One Node pin for the repository; every `engines` range accepts 26.13.0; one image, one build; the glibc variant most CI images use | A row runs on a Node it was not bundled with (npm 10 normally meets Node 22) |
| (b) The bundled Node per row | Node 22 for npm 10, Node 24 for npm 11, Node 26 for npm 12, and so on | Each npm runs where users run it | Three or more Node pins and images; the version-per-tool rule is broken for Node; the registry-visible behavior of the clients does not depend on the Node minor |
| (c) Alpine variant | As (a) on musl | Smaller image | pnpm 12 would run its musl binary, which is not the one most users run; the image is a test tool whose size does not matter |

**Choice:** (a), the digest-pinned `node:26.13.0-trixie-slim` (Node.js 26 is the active LTS line since
2026-10-28; the pin moved from 24.22.2 with the Node.js 26 update). The image removes the `npm`,
`npx` and `corepack` that come with Node.js, so the only npm in it is the three npm rows.

**Rationale:** No row needs a different Node: all eight `--version` calls ran on 26.13.0 in the
image (verified 2026-10-30), and npm 12 (`^22.22.2 || ^24.15.0 || >=26.0.0`) and pnpm 12 (native,
`>=18`) accept it. The development agent would
revisit this if a scenario fails on a row only on Node 24 and passes on the Node the row is bundled
with; that row then gets its own image tag and its own pin.

**Applied in the spec:** R-NPM-045 (the Node.js of the clients).

### Q-NPM-46 · What marks the run that closes the phase

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the roadmap (M2, E2.1, E2.2, E2.6) and R-NPM-045 reserve three things for "the full run of Test
and Release on the commit that closes the phase": the scenario list and the cold-cache install with Valkey
stopped, the read-only subset against `https://registry.npmjs.org`, and a fuzz run of `Match` and
`Inspect` of at least 30 minutes. The workflow ran on `push` to `main`, on pull requests and on
`workflow_dispatch` without inputs; nothing told a run on the closing commit from any other.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A dispatch input `closing` | `workflow_dispatch` with the boolean input `closing`; the extra legs run only when it is true; the ref of the run is the closing commit | Explicit and chosen by a person; the run is a full run (every job of the workflow runs on a dispatch); no commit or tag convention to keep; the public registry and 30 minutes of fuzzing never run by accident | Someone has to start it; a run on `push` never carries the legs |
| (b) Every push to `main` | The legs run on each push | Nothing to remember | 30 minutes of fuzzing and a dependency on the public registry on every change; the Valkey-stopped leg doubles the e2e time of every change |
| (c) A tag or branch naming the phase | A `phase-*` tag starts the legs | Visible in the history | A second trigger in the workflow, a naming rule, and a tag that must be pushed before the run exists |
| (d) A marker in the commit message | The legs run when the head commit says so | In the history | A commit message that decides what CI runs; squash merges lose it; a pull request run could carry it by accident |

**Choice:** (a). The jobs are `e2e-npm` (its matrix gains the leg `npm-closing`) and `fuzz-npm` (three legs of 30
minutes: `FuzzR_ARCH_028_Match`, `FuzzR_ARCH_028_Inspect`, `FuzzMatch`), both in
[release.yml](../../../.github/workflows/release.yml); the legs are the make targets
`make e2e SET=npm-closing` and `make fuzz-npm`. A dispatch does not release (Semantic Release runs on `push`
only), so the closing run is a pure verification of the commit.

**Rationale:** The closing run is a decision about a commit, and a person makes it. The development
agent would revisit this if the owner wants the closing run to start itself when the last work package
of a phase merges; the trigger is then a `workflow_run` or a label, and the legs stay as they are.

**Applied in the spec:** R-NPM-045 (the closing run), roadmap M2 (E2.1, E2.6). Code:
[release.yml](../../../.github/workflows/release.yml), [e2e.sh](../../../deploy/dev/e2e.sh) (sets `npm`, `npm-closing`).

### Q-NPM-47 · Where the matrix puts the snippets and what it adds through the environment

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: E2.8 requires the configure step to use the snippets of R-NPM-041 as they are. The clients
need more than the snippets give: a cache directory of their own, no telemetry, Yarn 4's release-age
quarantine (`npmMinimalAgeGate`, which refuses a package published a moment ago), and a place for the
file. The first run showed that pnpm 11 and 12 ignore a credential with an environment variable in a
project `.npmrc` (their warning: "environment variables are not expanded in registry credentials that
come from a project .npmrc"), and that pnpm 10 runs an `npm` from its `PATH` for `whoami`, `view` and the
upload of `publish`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Snippet verbatim at the place its client reads it; everything else through the environment | The matrix renders the template of `SetupTemplates`, writes it to the file the template names (`~/.npmrc` for pnpm), and sets `HOME`, cache directories, `YARN_NPM_MINIMAL_AGE_GATE=0` and the like as environment variables of the command; pnpm 10 gets the npm 11 row as its `npm` | The snippet users paste is what is tested, byte for byte; a client setting that a snippet should carry shows up as a failing scenario, not as a hidden edit | The environment is a second place that configures the clients (small, in one function) |
| (b) Edit the snippets until every client works | Append settings to the files | Fewer environment variables | The matrix would test a configuration no user is told to write |
| (c) Skip the pnpm rows' credential | Run pnpm anonymously | Nothing to place | The repository requires authentication (the realistic case); the row would test nothing |

**Choice:** (a). The pnpm snippet therefore names `~/.npmrc` (`internal/formats/npm/setup.go`, test
`TestR_NPM_041_PnpmCredentialsLiveInTheUserFile`), the clients image has the shim
`test/e2e/clients/shim/npm` that only `bin/pnpm10` puts on its `PATH`, and the scenario of Yarn 4's
publish installs its package directory first because `yarn npm publish` needs the package in the lockfile
of its project.

**Rationale:** (a) keeps the snippet the unit under test. The development agent would revisit the `~/.npmrc`
choice if pnpm accepts an environment variable in a project file again, and the shim if the pnpm 10 row
leaves the rule of R-NPM-045.

**Applied in the spec:** R-NPM-041 (the pnpm line), R-NPM-045 (scenario table).

### Q-NPM-48 · How the suite pins a client to a replica

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: E2.2, E2.3, E2.9 and E2.10 name the replica a client talks to; the scatter profile otherwise
spreads requests. The client pods run the forwarder `fwd` on the base URL's port, so the clients cannot
tell where their bytes go.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A target file in the forwarder | `fwd -target-file /tmp/target`: while the file holds `host:port` the forwarder relays there instead of the edge; the test writes the pod IP of a web replica and empties the file afterwards | No pod restart, no second base URL, the certificate is the replica's (it names `localhost`); one pod serves any replica | Changes the forwarder (a few lines, unit-tested) |
| (b) One pod per replica started by the script | The target is fixed at pod creation | No forwarder change | Pod IPs change whenever the web pods do; the pods would have to be recreated for each test |
| (c) The clients talk to a port-forward on the host | `kubectl port-forward` per replica | No pod changes | The clients run in pods, not on the host |

**Choice:** (a). A scenario a client cannot run is `not applicable`, with the reason in the log line, in the
report (`.out/e2e/npm-<run>.json`) and in [e2e.md](../../developer/e2e.md#the-matrix-and-what-is-not-applicable); no scenario is skipped
for any other reason. While the remote and virtual classes and the exposure adapter did not exist, a scenario
that needed them failed as pending unless an environment switch reported it as such; the switch and its probes
were removed when WP2.5 and WP2.9 merged.

**Rationale:** The matrix is only a gate when a client that cannot run a scenario says so, and nothing else
goes missing quietly. The development agent would revisit (a) if the kind network makes the forwarder the
bottleneck of the drain run (R-HA-059).

**Applied in the spec:** R-NPM-045 (a scenario a row cannot run is reported, never silently skipped).

### Q-NPM-49 · What the P1 scenarios assert, and what the first run changed

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the scenario list has login, audit, search and deprecate, which are P1: their endpoints answer
as R-NPM-043 prescribes. The first run against the built image found four places where the registry or the
page did not match what the clients do:

1. `npm deprecate` waited 70 seconds: the metadata `PUT` of a deprecation answered 501, which npm's fetch
   layer retries for a `PUT` (R-NPM-001 says exactly that).
2. `pnpm audit` (pnpm 10, 11 and 12) retries a 501 answer to its audit `POST` after 10 seconds and again
   after a minute; the premise of R-NPM-001 that a 501 on a `POST` "costs nothing" holds for npm only.
3. `npm owner ls` succeeds (it reads the maintainers of the document), so it cannot be a command that
   "fails within one second" as the criterion of R-NPM-043 says; `npm owner add` is the command that fails.
4. A client that logs in anonymously (pnpm, Yarn 4, Yarn 1) gets the 401 of the authentication gate at a
   repository that requires authentication, where npm, which sends its credential, gets the 404 or 501 of the
   endpoint.
5. Found when the matrix ran through `npm-all`: pnpm 11 and 12 read the package with a plain `GET` and send its
   `_rev` back in `PUT {name}/-rev/{rev}`. On a virtual repository that `_rev` is the merge's synthetic one
   (R-NPM-009: "neither can be written through"), so the write answers 409 `npm_stale_rev`, which the client retries
   for 70 seconds. npm and pnpm 10 (whose unpublish is npm's) use `?write=true`, which returns the deployment
   target's own `_rev` (R-NPM-029). R-NPM-017 and R-NPM-040 say "`pnpm unpublish` behaves like npm" without
   saying through which repository.
6. A virtual repository is read through the members the caller can read (R-AUTHZ-009 rule 2) and accepts only `read`
   and `manage` in a policy: a token that reads a virtual repository and not its members sees an empty merge and
   404, and a policy that grants `deploy` on a virtual repository is refused. The end-to-end fixtures grant
   `read` on the virtual repository and its members, and the write actions on `npm-local`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Fix 1, document 2 to 6 | The deprecating `PUT` answers 404 `not_found`; the scenarios assert the answer each client can produce (404, 501, or the 401 of the gate), and for `pnpm audit` only the 501 it reports; the criterion names `npm owner add`; the unpublish of pnpm 11 and 12 is run against the deployment target (`npm-local`), the others through `npm-all` | The registry follows its own rule where it can; the suite states what clients do | `pnpm audit` keeps its 70 seconds until audit is built (P1) |
| (b) Assert the spec text as written | Every P1 command must fail within seconds without a retry | One rule | Fails on facts about the clients that trove does not control |
| (c) Skip the P1 scenarios | Not run | Fast | The criteria R-NPM-043 and E2.1 name them |

**Choice:** (a).

**Rationale:** A `PUT` that is not implemented is the 404 the page already prescribes; a 5xx there
cost a user 70 seconds. The client behaviors are facts to record, not to hide. The development agent would
revisit 2 when audit is built (R-NPM-038): the 501 then no longer occurs.

Revisited 2026-10-31, when audit was built: point 2 holds where trove answers 501, and only there.
Through a forwarding repository pnpm 11 and 12 get the provider's answer and retry nothing. A
repository without a forwarder (a local repository, mode `disabled`, a virtual repository without a
forwarding remote member) still answers 501, which pnpm retries for 70 seconds per request. pnpm 10
asks npm 6's audit endpoints, which answer 501 everywhere, and reports after about 140 seconds
(Q-NPM-65). The choice stays (a); the scenario now asserts the advisories for every row but pnpm 10
and Yarn 1, and the 501 for pnpm 10.

**Applied in the spec:** R-NPM-018 (the answer until it is built), R-NPM-001 (the 501 note), R-NPM-043 (the
criterion). Code: `internal/formats/npm/handlers_deprecate.go`, `TestR_NPM_043_DeprecateIsRefusedWithoutRetries`.
Open, for the owner: pnpm 11 and 12 cannot unpublish through a virtual repository as the design stands (point 5); letting a
`-rev` write through a virtual repository accept the merge's `_rev` would make it work for packages that exist only in the
deployment target, and would need a rule for versions that only a remote member holds (the `PUT` names them, R-NPM-017 answers 400).

### Q-NPM-50 · A package or version of the install snippet that a client would read as an option

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the client setup of [api-and-ui.md](../api-and-ui.md) R-API-030 puts `package` and `version` into the install snippets
of npm, pnpm and Yarn (R-NPM-041). The shell quoting let a value that starts with `-` through bare, and the package was not
validated as a name: `package=--registry=https://evil.example` gave `npm install --registry=https://evil.example`, a command
a user pastes from trove's own UI, which a link to the package page can prefill. Quoting does not help by itself: the shell
removes the quotes, and the client still reads `-…` as an option. `name.Parse`, the rule that looks names up, accepts a legacy
name that starts with `-` (only a new name may not, R-NPM-002).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Refuse a `package` or `version` that starts with `-`; validate `package` with the format's name rule; quote a value that starts with `-` | `400 malformed_request` before any snippet is built; the package is normalized by the format's one name rule (R-AUTHZ-005); `shellQuote` never leaves a leading `-` bare | No snippet of trove ever hands a client an option it did not offer; a value that is no name of the format never reaches a template | A legacy package whose name starts with `-` gets no install snippet |
| (b) Insert `--` before the package in the templates | `npm install -- <package>` | Every legacy name works | npm, pnpm and Yarn treat `--` differently; the templates of R-NPM-041 change for a case that has no known package |
| (c) Quote only | The review's minimum | Small | The client still reads the option |

**Choice:** (a). The `version` has no format rule of its own in the setup (a version, a tag or a range may be asked for), so
only its first character is refused; the package passes the format's `Names.Normalize`, and the normalized name is what the
snippet holds.

**Rationale:** an install command is a command a user runs as written; the server must not be the one that turns a parameter into
an option. The development agent would revisit (b) if a package whose name starts with `-` turns up in a customer's registry.

**Applied in the spec:** [api-and-ui.md](../api-and-ui.md) R-API-030 (the refusals), R-NPM-041. Code:
`internal/api/v1/packages_api.go` (`clientSetup`), `internal/api/v1/setup_snippets.go` (`shellQuote`).

### Q-NPM-53 · The publish body cap is the sum of the limits of its parts

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-NPM-007 step 2 capped the body at the base64 form of the tarball limit plus 1 MiB, and Q-NPM-20 said that 768 KiB of metadata plus the base64 form of a 256 KiB provenance bundle (342 KiB) stay below that MiB. The sum is 1,110 KiB. A tarball within about 85 KiB of `max_tarball_size`, published with a full-size bundle and a large manifest, was cut by the body reader and answered 413 although each part was within its own limit.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The slack is derived from the two limits: `DefaultMaxMetadataBytes` plus the base64 form of `maxSigstoreBytes` | The cap is the sum of what the parts may hold | Cannot drift from the limits; the unit test pins the sum | The documented slack is 1,110 KiB, not a round number |
| (b) Lower the metadata budget to about 680 KiB | The round 1 MiB holds | Spec text stays | Refuses manifests with READMEs that Q-NPM-20 chose to accept |
| (c) Raise the slack to a round 2 MiB | Room to spare | Simple | A cap that says nothing about its parts |

**Choice:** (a). `bodySlack` in `internal/formats/npm/publish.go` is the metadata budget plus the base64 form of the bundle limit; the spec sentence of Q-NPM-20 and R-NPM-007 step 2 state 1,110 KiB.

**Rationale:** a cap that is smaller than the sum of its limits refuses valid requests, and one that is larger admits bytes no limit allows. The development agent would revisit the choice if the metadata budget or the bundle limit becomes configurable: the slack then follows the configured values.

**Applied in the spec:** R-NPM-007 step 2, Q-NPM-20 (the bounds).

### Q-NPM-54 · The whole archive has an inflation ratio cap

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the validation pass bounded the inflated bytes by `max_unpacked_size` (2 GiB by default) and the ratio of `package.json` only. A publish of about 2 MiB of compressed zeros made the server inflate and hash 2 GiB, and it could be repeated without limit (S-4).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A cap of 100:1 over all inflated bytes against the compressed bytes consumed, with 8 MiB of slack | The attacker pays 1 MiB of upload for every 100 MiB of inflation, and a small archive is never judged by its ratio | Needs no configuration, `Limits.MaxTotalRatio` lets a caller tighten or lift it, the pass already counts both | A package of more than 100:1 (a large zero-filled file) is refused with 413 |
| (b) 1,000:1 | Admits nearly everything deflate can express | Refuses nothing legitimate | A DEFLATE stream cannot exceed about 1,032:1, so it limits nothing |
| (c) A configuration key | The operator chooses | Flexible | A security limit that an operator can weaken per installation; one more key to document |

**Choice:** (a). The check is in the inflating reader of `internal/formats/npm/tarball`: output bytes against 100 times the compressed bytes the inflater has consumed, plus 8 MiB. The streaming memory test lifts the cap explicitly, because it feeds 64 MiB of zeros.

**Rationale:** the inflater has consumed at least n/1,032 compressed bytes for n output bytes, so the check is sound for every byte it reports. The development agent would revisit the choice when a legitimate package is refused: the answer is a measured default, or a key, not a removal.

**Applied in the spec:** R-NPM-007 step 3 (the resource bounds).

### Q-NPM-55 · A link target may not pass through another link

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the link check cleaned a symbolic link target lexically against the entry's folder. `package/sub/l2 -> ..` stays inside the package, and `package/l1 -> sub/l2/../..` cleans to the package folder, but a file system follows `l2` first and ends two levels above it (S-6). A hard link whose name passes through a symbolic link is the same case.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) After the walk, refuse a link whose target passes through a link entry before its last component | Every resolution step is a folder of the archive | No chain can leave the package; a link to a link stays valid | Needs the whole archive, so it runs at the end of the pass |
| (b) Refuse every link | Nothing to check | Simplest | pacote skips link entries anyway, but other extractors and existing packages use them |
| (c) Resolve chains and compare | Exact | Accepts more | A resolver of its own to get wrong |

**Choice:** (a). `walker.linkChains` in `internal/formats/npm/tarball/tarball.go` walks each target component by component against the kinds of the archive's entries. The bookkeeping of the targets counts against `MaxPathBytes`.

**Rationale:** the rule is local to the archive, and a target that passes through a link has no lexical meaning. The development agent would revisit the choice if a real package is refused for a chain of links that stays inside the package.

**Applied in the spec:** R-NPM-007 step 3 (the further archive rules).

### Q-NPM-56 · The ownership rule is asked before the body is read

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: a publish was authorized on the name before the body, but the ownership rule of R-AUTHZ-018 ran only in the commit transaction, after the body was spooled, scanned and validated. A caller who holds `deploy` and does not own the name could make the server do that work without end (S-4).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A read-only question to the host before the body: `Host.CheckOwnership`; the commit decides again | A refusal costs no spool; nothing is locked or claimed early | The commit stays the decision that counts; the rule is one function | A second read of the claim rows per publish |
| (b) Take the claim lock before the body | The decision is final early | One question | A lock held while a body streams (R-NPM-046) |
| (c) Rate limit only | The classes of R-API-016 | Nothing to build | A caller within the limit still costs a full validation per request |

**Choice:** (a). The question is asked as a change of the package, not as a claim (`Publish` false): a claim needs a designated owner that only the commit can know, so the early answer never refuses what the commit would allow. `FirstVersion` comes from the live versions read just before.

**Rationale:** the early answer is advisory by construction. The development agent would revisit the choice if the extra read shows in the cost of a publish.

**Applied in the spec:** R-NPM-007 step 1.

### Q-NPM-57 · time.modified orders two documents before the fetch time does

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: Q-NPM-32 reported `Superseded` before it looked at `time.modified`. With Valkey stopped, two replicas fill one document; when the upstream answers the older copy with a later response (a lagging edge), that fill has the later `fetched_at` and commits first, and the newer document that was fetched earlier was discarded as superseded. The stored document listed one version fewer than the upstream, and the URL of the missing version was empty. It showed in about one run in four of the two-replica test in the order where the older answer arrives first.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `time.modified` first: older is `Stale`, later replaces the stored document even when the stored entry was fetched later (`Newer`); only equal or unknown `time.modified` is ordered by fetch time (`Superseded`) | The order of the content decides when the content has one | Matches the stale-copy rule's own reason (a lagging edge); the newest document wins in both orders | The format's answer must reach the upsert (Q-REPO-120) |
| (b) Fetch time first, as before | No change | One order everywhere | The newest document can lose to an older one that arrived later |
| (c) Compare the digests of the documents | No clock | Needs no ordering field | A digest has no order |

**Choice:** (a). `ApplyFill` reports `Newer`, `OnFill` answers "replace whatever the fetch time", the engine's upsert follows and records the URL map of that document.

**Rationale:** a stale answer that overtakes a newer one is exactly the case the stale-copy rule exists for; ordering it by fetch time made the rule depend on which response was slow. The development agent would revisit the choice if upstreams that send a `time.modified` that does not grow with their content show up.

**Applied in the spec:** R-NPM-024 (the paragraph on one stored document), Q-NPM-32 (the order of the two rules).

### Q-NPM-58 · The provenance bundle commits with the tarball, in one transaction

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: Q-NPM-22 committed the tarball first and the bundle second, because the item store committed one file per transaction. The tarball's commit made the version visible, and a failure of the bundle's commit left the version published without it and answered 500. R-ARCH-009
says everything one client request writes goes through one transaction, and Q-ARCH-228 gave the item store the means: companion files in the commit of the lead.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Two commits (Q-NPM-22 (a)) | As built | No change | A version without its bundle is visible until a retry, which answers 409 (the bundle is part of the retry facts) |
| (b) The bundle is a companion of the tarball's commit | `formats.Commit.Companions`: one transaction, the tarball's hook first, then the bundle's, which links the item to the version the tarball's hook created | The window is gone; a refused bundle leaves nothing | The bundle's hook runs in the same transaction as the tarball's |
| (c) Bundle first (Q-NPM-22 (b)) | The tarball's commit links it | The version never exists without it | An item at a version's path that no version owns, plantable by a `deploy` holder |

**Choice:** (b). A bundle that cannot be stored (its path holds other content, the version is not live in the transaction) rolls the tarball back with it: no version, no package rows, no claim, no audit event, no `rev_seq`. The writer raises
`rev_seq` once per package per transaction, so a publish with a bundle raises it once (it was twice). A retry whose tarball is identical to the stored one writes nothing, the bundle included, and is decided from the committed facts as before (R-NPM-008).

**Rationale:** it is the choice Q-NPM-22 named as the answer when attestations are served, made earlier because the window was live for `npm publish --provenance`. The development agent would revisit it if the bundle ever has to be read before it is stored (R-NPM-027): the validation would then run
before the commit, outside the transaction, as the tarball's does.

**Applied in the spec:** R-NPM-006, R-NPM-007 steps 5 and 6 and the notes on the implementation; Q-NPM-22 is superseded. Code: `internal/formats/npm/{publish.go,hooks.go}`; tests: `test/integration/npm/provenance_atomic_test.go`.

### Q-NPM-59 · npm's settings are the `npm` member of the repository document, with a schema and four settings

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the repository document had no format member (Q-NPM-23, Q-NPM-35), so `latestGuard` could not be set, and `serveRemovedVersions`, `latestStrategy` and `distTagStrategy` could be set only by writing the row. Q-ARCH-229 and Q-REPO-122 gave every format that member and the validation.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The settings the code reads, now | `latestGuard` (local), `serveRemovedVersions` (remote), `latestStrategy` and `distTagStrategy` (virtual), each with its class in `x-trove-classes` and its default | The schema says what is in effect; nothing is accepted that is ignored (R-REPO-008) | The rest of the spec's `npm` sections (`externalDependencies`, `audit`, trusted publishing) are not settable yet |
| (b) Every setting the spec names | Schema members for features that are not built | One schema | A setting that is accepted and does nothing |
| (c) Leave the member to SQL | — | Nothing to build | The review's finding |

**Choice:** (a). The schema is [settings.schema.json](../../../internal/formats/npm/settings.schema.json); the settings of the features that arrive later join the schema in the change that builds them. A setting that the document leaves out takes its default (guard on, removed versions hidden, `highest-member-latest`, `member-order`).
The format reads its member from the stored document of the request's repository (`settingsOf`), and `npm_remote.sql` reads `serveRemovedVersions` from the same stored member.

**Rationale:** the format reads one stored document and the service validates it, so the two cannot disagree about a value. The development agent would revisit it when a setting must be validated against another repository (the `remotes` of `externalDependencies` must exist): the schema would then be followed by a hook of the format (roadmap M3, validation of remote configuration).

**Applied in the spec:** R-NPM-016, R-NPM-026 and R-NPM-031 (the `Configuration` blocks); Q-NPM-23 and Q-NPM-35 (the revisit condition they name is met). Code: `internal/formats/npm/{settings.go,settings.schema.json}`; tests: `test/integration/npm/settings_test.go`.

### Q-NPM-60 · The latest guard is applied by a publish, and only by a publish

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-NPM-016 puts the guard on a publish that carries `latest`; a dist-tag write moves `latest` to any live version deliberately ("moving it back after a broken release is legitimate"). With the setting readable, the question is which writes read it.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The publish reads it | The handler reads `npm.latestGuard` once and the hook applies it with the tags read under the package row lock | The guard is about an implicit move; the tag write is explicit | The hook has no request, so the value travels in the commit's payload |
| (b) Every write that can move `latest` reads it | The dist-tag handlers as well | One rule | The deliberate move would be refused with the guard on, which R-NPM-016 says it must not be |

**Choice:** (a). The removal rules (`latest` moves to the highest remaining non-pre-release version when its version goes) are not a setting and always apply.

**Rationale:** the setting names what a publish may do. The development agent would revisit it if a client's tag write is found to move `latest` unintentionally (none does).

**Applied in the spec:** R-NPM-016 (the configuration block). Code: `internal/formats/npm/{publish.go,hooks.go}`.

### Q-NPM-61 · The security audit is part of the beta

Status: **Decided by the owner on 2026-10-31.**

Context: R-NPM-038 was P1 and planned for M6; until then the audit endpoints answered 501 (R-NPM-043), and the matrix
asserted that 501. The teams that will use the beta run `npm audit` routinely, in CI and by hand, and an audit that
always fails takes a security signal away from every one of them.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Keep R-NPM-038 at P1 (M6) | The beta answers 501 | Smaller M2 | `npm audit` fails for every user of the beta; `pnpm audit` waits 70 seconds first (Q-NPM-49) |
| (b) Raise R-NPM-038 to MVP and build it in phase 3 (M2) | The audit forward of Q-NPM-3 ships with the npm format | The teams keep their audits | M2 grows by one requirement |

**Choice:** (b).

**Rationale:** the owner's decision: the teams use `npm audit` heavily, so it belongs in the beta. The development
agent records no revisit condition; the priority is the owner's.

**Applied in the spec:** R-NPM-038 (priority MVP); [roadmap](../../coverage/roadmap.md) M2 scope and E2.1, M6 without
R-NPM-038; [BETA.md](../../projektplan/BETA.md) phase 3; the coverage pages regenerated.

### Q-NPM-62 · Which repository forwards an audit, and how it reaches the provider

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-NPM-038 names the forwarding member of a virtual repository ("the first remote member in resolution order
whose mode is `forward`") and the permission on it, but not what happens when the caller cannot read that member, nor
where a forward is impossible besides mode `disabled`, nor how the request reaches the provider: the remote engine had
only cached reads and fetches, and the format contract had no call that sends a request outside the cache.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The first enabled forwarding member, whoever asks; `read` on it or the refusal of R-AUTHZ-016. 501 also for the cache view, an offline remote and an endpoint without cache fills or with exposure rules. One contract call, `RemoteFetcher.Exchange`, through the remote's upstream client, outside the cache, without fetch slot or circuit. The routes are `Volatile` reads | One provider per virtual repository, whoever asks; one decision, audited when it refuses; the egress rules, the trust store and the credential policy of the remote apply unchanged | A caller who reads the virtual repository but not its forwarding member gets a refusal instead of an audit |
| (b) The first forwarding member the caller can read, skipped silently | An audit for more callers | The provider depends on who asks; the question needs a non-audited member check the contract does not have |
| (c) The forward through the engine's fetch path (slots and circuit) | One path for all upstream traffic | The provider may be another host than the upstream: its failures would open the upstream's circuit, and a fetch slot would be held while the provider thinks |

**Choice:** (a). The routes `npm.audit.bulk` and `npm.audit.legacy` are of kind `Volatile` with action `read` and every
class: a write kind would send a virtual repository's audit to its deployment target, and a content kind is for bytes trove
renders or stores. `Classify` says `volatile` for the two paths, so the descriptor of R-NPM-003 is `search`, not `other`:
the kind that is passed through and never cached. On an endpoint with exposure rules the split into forwarded and withheld
names tells which names a remote has fetched, which R-EP-018 hides by removing `Cache-Status` (Q-EP-60), so the audit is
501 there. `Exchange` answers `ErrOffline` for an offline remote and the format answers 501.

**Rationale:** the egress rules and the credential policy are the reason to go through the remote's client at all, and
`Exchange` keeps them without the cache semantics a provider request does not have. Requiring `read` on the forwarding
member follows R-NPM-038's permission line literally and matches the finding that a virtual repository needs `read` on its
members anyway (Q-NPM-49 point 6). The development agent would revisit (a) if installations grant `read` on virtual
repositories without their remote members on purpose; (b) would then need a non-audited member question in the contract.

**Applied in the spec:** R-NPM-038 (the forwarding member, the 501 cases, the forward), R-NPM-003 (the audit row),
R-NPM-048 (exposure rules), [repositories-and-proxy.md](../repositories-and-proxy.md) R-REPO-038 (the exchange is a
connection of the remote). Code: `internal/formats/remote.go` (`Exchange`, `ErrOffline`), `internal/proxy/exchange.go`,
`internal/formathost/remote.go`, `internal/formats/npm/{routes.go,match.go,format.go,handlers_audit.go}`.

### Q-NPM-63 · How the forwarded set is decided, and how withheld names are counted

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-NPM-038 asks for one query for the whole body and names four reasons for the metric. A name can have several
reasons; claims and pin rules are patterns that the routing matches in memory; and an exclude pattern added after a remote
fetched a name leaves its record behind.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One statement decides every fact: the record of the forwarding remote, a package row or tombstone in any npm local repository, a claim of any scope of an npm local repository, a pin rule of the requesting virtual repository. The patterns of the forwarding remote and of the virtual repository refuse as `not_proxied`. One reason per name, in the order `local`, `claimed`, `pinned`, `not_proxied`; the metric is labelled with the forwarding remote | The committed state at the moment of the audit, also on a replica whose routing index lags; `not_proxied` counts exactly the names an opt-in to forward unproxied names would add (the revisit condition of Q-NPM-3) | The claim pattern match exists twice, in the routing index and in SQL |
| (b) The record in SQL, claims and pins from the replica's routing index | One matcher | A contract addition for the index; a claim made on another replica a moment ago is not seen |
| (c) Every reason counted | Complete counts | The counts no longer add up to the withheld names of the notice |

**Choice:** (a). The SQL match relies on npm's name rule: a stored npm pattern holds no escape, because an npm name holds
no backslash (R-NPM-002), so a pattern is an exact name, or a literal and one trailing `*` (nothing after it holds `/`) or
`**` (R-REPO-023). A name that is no npm name is `not_proxied` without a query.

**Rationale:** withholding has to be at least as strict as routing, so it reads what is committed rather than what a
replica's index holds. A name kept away from a remote by its patterns is one the remote does not proxy, whatever it fetched
before the pattern was set. The development agent would revisit the duplicated matcher if claim or pin patterns gain a form
beyond the two of R-REPO-023; `TestR_NPM_038_TheForwardedSetThroughARemoteAndAVirtualRepository` covers both forms.

**Applied in the spec:** R-NPM-038 (the patterns, the order of the reasons, the label), R-NPM-044 (the label). Code:
`internal/meta/postgres/queries/npm_remote.sql` (`NPMAuditNames`), `internal/formats/npm/handlers_audit.go` (`auditSplit`,
`withheldReason`), `internal/formats/npm/bind.go`.

### Q-NPM-64 · The bounds of an audit body

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-NPM-038 proposed `maxBodySize: 10MiB` as sent and ten times that decompressed. The body is decoded in memory to
decide the set, so the decompressed size is what a request costs, and a body of many tiny names costs more than its bytes.
A lockfile of 10,000 packages sends about 500 KiB uncompressed (pnpm) and about 100 KiB as gzip (npm).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The proposed 10 MiB | 10 MiB as sent, 100 MiB decompressed | Room for any lockfile | Up to 100 MiB decompressed per request on a `web` replica, for every caller with `read`, anonymous ones included where anonymous read is on |
| (b) Default 1 MiB, settable from 1 KiB to 10 MiB, ten times that decompressed, at most 100,000 names; gzip or no coding (415 otherwise); an object of version lists (400 otherwise) | About twenty times the largest lockfile in plain JSON; at most 10 MiB decompressed by default; the names bound the query's parameter | A monorepo beyond about 20,000 packages that audits with pnpm needs a larger setting |
| (c) Spool the body to disk and decide in two passes | Memory bounded by the names | A second pass and a spool file for a request that is small in practice |

**Choice:** (b). The settings are strings of the forms the schema allows (`30s`, `1MiB`), within the schema constructs the
settings form renders.

**Rationale:** the default fits every realistic lockfile with a margin and keeps a burst of audits from exhausting a
replica's memory; an installation that needs more raises it per remote. The development agent would revisit it if
`npm_too_large` answers to audits show up in real use, or if a memory bound per replica for all request bodies arrives
(then (c) or a shared budget).

**Applied in the spec:** R-NPM-038 (the bounds and the configuration block; the default was 10 MiB, proposed). Code:
`internal/formats/npm/{settings.go,settings.schema.json,handlers_audit.go}`; the residual cost is
[H-301](../../security/data-and-secret-flow.md#h-301--an-audit-body-is-held-in-memory).

### Q-NPM-65 · npm 6's audit endpoints and pnpm 10

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: measured on 2026-10-31 with the pinned clients against the bulk endpoint: npm 10, 11 and 12, pnpm 11 and 12 and
Yarn 4 use `-/npm/v1/security/advisories/bulk` and print the advisories. pnpm 10.34.9 (a gate row) does not: it posts npm 6's
audit tree to `-/npm/v1/security/audits/quick` and then to `-/npm/v1/security/audits`, and retries each 501 after 10 seconds
and a minute; it fails after 2 minutes 20 seconds. npm 7 to 10 ask the quick endpoint after a failed bulk request and
print its answer.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) npm 6's endpoints answer 501 `npm_audit_unavailable` | R-NPM-038 as written: the text of the bulk endpoint where no repository forwards, else a text that names the bulk endpoint | One protocol to keep right; npm 10's audit at `npm-local` prints the criterion's 501 and text | `pnpm audit` of pnpm 10 is not served and costs 140 seconds |
| (b) Serve npm 6's protocol by translation | Bulk names from the audit tree to the provider, an npm 6 report with findings and paths back | pnpm 10 audits | A second protocol with a second answer format, the paths of every finding computed from the tree, for one client major that leaves the gate rule within months |
| (c) 404 for npm 6's endpoints | pnpm 10 fails at once with its own hint | Fast | Breaks the rule of R-NPM-001 for `POST`; npm 10 at `npm-local` would print a 404 instead of the 501 the criterion asks for |

**Choice:** (a). pnpm 10's audit scenario asserts the 501 it reports.

**Rationale:** pnpm 10 leaves the gate when its newest release is six months old (R-NPM-045), and pnpm 11 and 12 speak the
bulk protocol. The answer pnpm 10 prints says which clients trove serves. The development agent would revisit (b) if users
of the beta stay on pnpm 10 past its gate row, or if another supported client speaks npm 6's protocol.

**Applied in the spec:** R-NPM-038 (npm 6's endpoints, pnpm 10), R-NPM-040, R-NPM-001 (the pnpm note), Q-NPM-49 (point 2
revisited). Code: `internal/formats/npm/handlers_audit.go` (`handleAuditLegacy`); `test/e2e/npm_matrix_test.go`
(`scenarioAudit`).

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
default). Live since R-NPM-038 was built (M2). trove forwards only names its remote has already fetched
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
