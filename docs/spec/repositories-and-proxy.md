# Repositories and pull-through proxy

Status: specification — not implemented

This page specifies trove's repository model and the management API around it: the
configuration document of a repository, keys and addresses, validation, create, read, update,
list and delete. It specifies how virtual repositories resolve requests, including protection
against dependency confusion through pin rules and namespace claims. And it specifies the
pull-through proxy of remote repositories end to end: settings and defaults, cache state in
PostgreSQL, collapsing of concurrent misses within and across replicas, metadata freshness,
negative caching, upstream failure handling and offline modes, upstream authentication and stored
secrets, client certificates, outbound HTTP proxies and CA bundles, egress address rules,
redirects, size limits, integrity verification, metadata URL rewriting, cache invalidation and the
settings that drive cache cleanup. Bytes, items and the operations on stored items (delete, copy,
move, properties, trash can) are specified in [storage.md](storage.md); who may do what in
[authorization.md](authorization.md); format-specific metadata in [formats/npm.md](formats/npm.md)
and [formats/nuget.md](formats/nuget.md).

Covers: [repositories.md](../features/repositories.md) (`REPO-*`),
[remote-repositories.md](../features/remote-repositories.md) (`RMT-*`), the cache-invalidation
entry ART-050 of [artifacts.md](../features/artifacts.md), the repository operation entries of
[api-content-operations.md](../features/api-content-operations.md) (`API-*`), BEYOND-028 of
[beyond-baseline.md](../features/beyond-baseline.md) and IAM-045 of
[identity-and-access.md](../features/identity-and-access.md).

## Where this page hands over

| Topic | Owner |
|---|---|
| Base tables (`repositories`, `items`, `blobs`, `packages`), upload and download pipeline, spool, delete, copy, move, properties, property sets, trash can, garbage collection, quotas, repository caps enforcement, the remote-cache cleanup job | [storage.md](storage.md) |
| Permission model, cache-fill rule, virtual-repository gate, capabilities and roles, projects, package ownership, existence hiding | [authorization.md](authorization.md) |
| Format plug-in interface in Go, job framework, `LISTEN/NOTIFY` plumbing, the stored-secret key | [architecture.md](architecture.md) |
| npm and NuGet: document classes, metadata merge, URL rewrite details, digest sources, version-removal tracking, format settings | [formats/npm.md](formats/npm.md), [formats/nuget.md](formats/nuget.md) |
| Public base URL and trusted proxies, metrics catalog, upstream log record, audit storage, cache-invalidation endpoint surface, configuration as code and `secretRef`, read-only maintenance mode | [operations-and-observability.md](operations-and-observability.md) |
| API conventions (error envelope, paging, job resource), UI | [api-and-ui.md](api-and-ui.md) |
| Helm values for CA bundles and secret mounts, egress `NetworkPolicy`, database connection budget | [ha-and-helm.md](ha-and-helm.md) |

## Terms

| Term | Meaning on this page |
|---|---|
| Canonical path | The repository-relative path a format plug-in assigns to a request or stored file: `/`-separated, no leading slash, no empty, `.` or `..` segment, percent-decoded exactly once. npm example: document `@acme/util`, tarball `@acme/util/-/util-1.4.0.tgz`. |
| Request kind | `artifact` (immutable binary), `metadata` (expiring document), `search`, `write` or `other`, assigned by the format plug-in (R-REPO-003). |
| Package name | The format's package identifier, normalized by the format plug-in (npm: as published, scoped names as `@scope/name`; NuGet: lower-cased with invariant-culture rules). |
| Name pattern | An exact package name, or a prefix ending in `*` (`@acme/*`, `acme-*`, `Acme.*`), compared after normalization. The syntax of NuGet's [package source mapping](https://learn.microsoft.com/en-us/nuget/consume-packages/package-source-mapping). Where several patterns match, the most specific one wins: an exact name beats any prefix, a longer prefix beats a shorter one. |
| Effective member list | A virtual repository's members after nested virtual repositories are expanded and duplicates removed (R-REPO-018). |
| Cache entry | What a remote repository stored for one canonical path and variant: an artifact, which is also an item of the repository ([storage.md](storage.md)), or a metadata document. |
| Fresh, expired, stale | A metadata entry is fresh until its expiry, expired afterwards; an expired entry served because the upstream was not or could not be asked is stale. Artifacts never expire. |
| Cache-fill check | The check that guards contacting the upstream: `read` plus the action named by the remote repository's `cacheFillAction` ([R-AUTHZ-008](authorization.md)). |
| Upstream origin | Scheme, host and port of the remote repository's upstream URL ([RFC 6454](https://www.rfc-editor.org/rfc/rfc6454)). |
| Secret field | A configuration field holding a credential. It takes `{"value": "…"}` or `{"secretRef": {…}}` and is never returned (R-REPO-033). |

Error codes on this page (`invalid_key`, `upstream_unavailable`, …) are carried in the error
envelope of [api-and-ui.md](api-and-ui.md) on `/api/v1` endpoints and in the error body the format
page prescribes on package-protocol endpoints. Denials (401, 403, 404 for hidden resources) follow
[R-AUTHZ-016](authorization.md). Examples use `https://trove.example.com` as the public base URL
and `$TROVE_TOKEN` as a trove token (INV-8).

## Repository model

### R-REPO-001 · Repository classes, format and immutable identity
Priority: **MVP** · Covers: REPO-001, REPO-002, REPO-016

- Every repository MUST have exactly one class, `local`, `remote` or `virtual` (INV-5), and exactly
  one format from the format catalog (R-REPO-003). Class, format and key are fixed at creation. An
  update that changes any of them MUST be refused with 422 `immutable_field`; there is no class
  conversion and no rename (rename is create, copy per [storage.md](storage.md), delete).
- A local repository stores what clients deploy. A remote repository stores what it fetched from its
  single upstream (R-REPO-024). A virtual repository stores no content and answers through its
  members (R-REPO-017, R-REPO-019).
- A deploy of content the format does not recognise as a package (for example a text file through
  `/files/` into an npm repository) MUST be passed to the format plug-in, which MAY refuse it with
  422 `not_a_package`. npm and NuGet refuse it: unindexed foreign content in a format repository is
  invisible to clients and only costs storage.
- Federated and release-set storage classes are not part of the model (see Deferred).

Data model. The base table `repositories (id, key, format, class, config jsonb, created_at,
updated_at)` is defined in [storage.md](storage.md) (R-STOR-003). This page owns the content of
`config` (R-REPO-004) except its `storage` member, and adds:

```sql
ALTER TABLE repositories
  ADD COLUMN lifecycle        text NOT NULL DEFAULT 'active'
                              CHECK (lifecycle IN ('active', 'deleting')),
  ADD COLUMN config_version   bigint NOT NULL DEFAULT 1,     -- ETag, R-REPO-011
  ADD COLUMN created_by       uuid,                          -- principal id
  ADD COLUMN updated_by       uuid,
  ADD COLUMN project_id       uuid REFERENCES project (id),  -- assignment: R-AUTHZ-026
  ADD COLUMN last_activity_at timestamptz,                   -- R-REPO-012
  ADD CONSTRAINT repositories_key_dots CHECK (key !~ '\.\.' AND key !~ '\.$');
```

`config` never contains secret values; they are in `repository_secrets` (R-REPO-033). References
that need referential integrity (members, deploy targets, pin targets, client certificates,
outbound proxies) are relational tables on this page.

Acceptance criteria:

- `POST /api/v1/repositories` with `{"key":"npm-local","class":"local","format":"npm"}` returns
  201; a following `PATCH` with `{"format":"nuget"}` returns 422 `immutable_field` at `/format`.
- A create with `"class":"federated"` returns 422 `unsupported_class`.
- `curl -X PUT --data-binary @notes.txt -H "Authorization: Bearer $TROVE_TOKEN"
  https://trove.example.com/files/npm-local/notes.txt` returns 422 `not_a_package`.

### R-REPO-002 · Repository keys and client addresses
Priority: **MVP** · Covers: REPO-004, REPO-005

- A key MUST match `^[a-z0-9][a-z0-9._-]{1,63}$` (INV-5: 2 to 64 characters, lower case, so it is
  valid for container formats too), MUST NOT contain `..` and MUST NOT end in `.` (trailing dots
  are stripped by some reverse proxies and Windows clients). Keys are unique across all classes.
  Violations answer 422 `invalid_key`.
- No suffix is reserved. The cache view of a remote repository is addressed with `~cache`
  (R-REPO-032); `~` cannot occur in a key, so the cache address never collides with a key and remote
  keys keep the full 64 characters.
- Repositories created by project administrators carry the project key as prefix
  (`<projectKey>-…`, [R-AUTHZ-026](authorization.md)).
- A key stays taken while its repository is being deleted and becomes free when the deletion job
  completes (R-REPO-013).
- Addresses of a repository (INV-6):

| Address | Purpose |
|---|---|
| `/api/<format>/<key>/…` | Package-manager protocol, e.g. `/api/npm/npm-all/`, `/api/nuget/nuget-all/v3/index.json` |
| `/api/<format>/<key>~cache/…`, `/files/<key>~cache/<path>` | Cache view of a remote repository (R-REPO-032) |
| `/files/<key>/<path>` | Raw file access ([storage.md](storage.md)) |
| `/api/v1/repositories/<key>` | Configuration |

- Recommended, not enforced: `<team>-<format>-<purpose>-<class>` for local and remote repositories
  (`billing-npm-release-local`); virtual repositories drop the class part (`billing-npm`).

Acceptance criteria:

- Creating repositories with the keys `Npm`, `a`, `-x`, `a..b`, `x.` and `npm~cache` each returns
  422 `invalid_key`; `npm-local` and `a1` are accepted.
- With remote `npm-remote` in place, `GET /api/npm/npm-remote~cache/lodash` reaches the cache view,
  and creating a repository named `npm-remote-cache` succeeds.

### R-REPO-003 · Format catalog and request classification
Priority: **MVP** · Covers: REPO-006

- Each format plug-in ([architecture.md](architecture.md)) MUST register: name; supported classes;
  layout name (R-REPO-006); protocol base path; default upstream URL; default metadata and
  not-found cache periods; the subject of include and exclude patterns (`name` or `path`,
  R-REPO-005); whether it supports priority members (R-REPO-022), pin rules and namespace claims
  (R-REPO-023), and repository caps (R-REPO-007); its package-name normalization.
- The MVP formats are `npm` and `nuget`, each with all three classes (INV-14).
- For every package-protocol and `/files/` request the plug-in MUST produce a request descriptor:
  canonical path; request kind; normalized package name and version where the request concerns a
  package; a variant for content-negotiated metadata (npm's abbreviated document, for example);
  for artifacts, the expected digests when the format's metadata announces them (R-REPO-041).
- A path the plug-in cannot classify MUST be treated as `metadata`. Misclassifying a binary as
  metadata costs a refetch after the metadata period; misclassifying metadata as immutable freezes
  package lists for good.

API:

| Method and path | Response | Permission |
|---|---|---|
| `GET /api/v1/formats` | 200, list of format descriptors | any caller; anonymous where anonymous access is on |

```json
{
  "formats": [
    {
      "name": "npm",
      "classes": ["local", "remote", "virtual"],
      "layout": "npm",
      "protocolBasePath": "/api/npm/{key}/",
      "defaultUpstreamUrl": "https://registry.npmjs.org/",
      "defaultMetadataTtlSeconds": 7200,
      "defaultNotFoundTtlSeconds": 1800,
      "patternSubject": "name",
      "priorityMembers": true,
      "pinsAndClaims": true,
      "caps": false
    }
  ]
}
```

Acceptance criteria:

- `curl -s https://trove.example.com/api/v1/formats` lists `npm` and `nuget`, each with the
  classes `local`, `remote` and `virtual`.
- Creating a repository with `"format":"cobol"` returns 422 `unknown_format`.

### R-REPO-004 · Configuration document and common settings
Priority: **MVP** · Covers: REPO-010, REPO-016, REPO-017, REPO-028, RMT-030

A repository is one JSON document. It is the request body of create (R-REPO-009), the response of
read (R-REPO-010) and the object body of configuration as code
([R-OPS-029](operations-and-observability.md)).

| Member | Meaning | Default (proposed) | Owner |
|---|---|---|---|
| `key`, `class`, `format` | R-REPO-001, R-REPO-002 | — (required) | this page |
| `description` | Public description, shown to everyone who can read the repository | `""` (≤ 2000 characters) | this page |
| `adminNotes` | Notes shown only to holders of `repositories:read` | `""` (≤ 8000 characters) | this page |
| `includePatterns`, `excludePatterns` | R-REPO-005 | `[]` (everything), `[]` | this page |
| `disabled` | Takes no part in resolution, download or deploy | `false` | this page |
| `requireAuthentication` | Anonymous requests are challenged even where anonymous read is granted | `false` | this page |
| `priorityMember` | R-REPO-022; local and remote only | `false` | this page |
| `hideExistence` | `inherit`, `true`, `false` | `inherit` | [R-AUTHZ-017](authorization.md) |
| `layout` | R-REPO-006, read-only | the format's layout | this page |
| `project` | Project key, read-only; assigned through the project API | `null` | [R-AUTHZ-026](authorization.md) |
| `local` | `packageOwnership`, `ownershipDomain` ([R-AUTHZ-018](authorization.md), [R-AUTHZ-020](authorization.md)); `autoClaim` (R-REPO-023); `caps` (R-REPO-007) | see owners | mixed |
| `remote` | R-REPO-024; `cacheFillAction` within it is [R-AUTHZ-008](authorization.md) | — | this page |
| `virtual` | R-REPO-017 | — | this page |
| `storage` | Upload size, quota, presigned downloads, client checksum policy | see owner | [storage.md](storage.md) |
| `npm`, `nuget`, … | Format settings, under a member named after the format | see owner | format page |

- A class section that does not match the class (for example `virtual` on a local repository)
  answers 422 `not_applicable`.
- `disabled: true` MUST make every package-protocol and `/files/` request to the repository answer
  404 `repository_disabled`, MUST remove it from virtual resolution (R-REPO-019) and MUST leave the
  management API working. The repository list shows it as inactive (R-REPO-012).
- `requireAuthentication: true` MUST answer an anonymous request with 401 and the challenge of
  [R-AUTHZ-016](authorization.md), even when the anonymous principal holds `read`. It is offered
  for every format and class, not only the catalog's NuGet and Maven cases.

Acceptance criteria:

- With `disabled: true` on `npm-local`, `npm view @acme/util --registry
  https://trove.example.com/api/npm/npm-local/` fails with `E404`, while the same command against
  the virtual repository `npm-all` still succeeds when another member holds the package.
- With anonymous read granted and `requireAuthentication: true` on `npm-local`, an unauthenticated
  `curl -i https://trove.example.com/api/npm/npm-local/@acme%2futil` returns 401 with a `Basic`
  challenge.
- `GET /api/v1/repositories/npm-local` by a caller without `repositories:read` contains no
  `adminNotes`.

### R-REPO-005 · Include and exclude patterns
Priority: **MVP** · Covers: REPO-011, RMT-034

- A path or package is admitted when it matches at least one include pattern (an empty include list
  admits everything) and no exclude pattern.
- The subject of the patterns is declared by the format (R-REPO-003):
  - `name` (npm, NuGet): patterns are name patterns (see Terms) matched against the normalized
    package name of the request. Requests that concern no package (service index, search, ping)
    are admitted. One pattern therefore covers a package's documents and files alike, which avoids
    the trap of path patterns that admit the tarball but not the document.
  - `path` (generic and path-based formats, when they arrive): Ant-style patterns
    ([Apache Ant](https://ant.apache.org/manual/dirtasks.html#patterns)) matched against the
    canonical path: `?` one character except `/`, `*` within one segment, `**` any number of
    segments.
- Patterns MUST apply to: resolution of downloads; virtual membership (a member is consulted only
  for admitted requests); upstream fetching (a remote repository never asks its upstream for
  anything it does not admit, also when addressed directly); deploys to a local repository
  (refused with 403 `not_admitted`); copy and move targets ([storage.md](storage.md)).
- Patterns MUST apply to every caller on package-protocol and `/files/` requests, administrators
  included: they are routing rules, and an administrator's build must not fetch an internal name
  from a public upstream either. The management item API shows administrators items outside the
  patterns (stored before a pattern change, for example) so they can be removed.
- Changing patterns MUST NOT delete stored items.
- Limits: at most 100 patterns per list, each at most 1024 characters; violations answer 422
  `invalid_pattern`.

Acceptance criteria:

- Remote `npm-remote` with `excludePatterns: ["@acme/*"]`: `npm view @acme/util --registry
  https://trove.example.com/api/npm/npm-remote/` fails with `E404`, and a counting stub upstream
  records no request.
- Local `npm-local` with `includePatterns: ["@acme/*"]`: `npm publish` of `left-pad` fails with 403
  `not_admitted`; publishing `@acme/left-pad` succeeds.

### R-REPO-006 · Layouts (minimal)
Priority: **MVP** · Covers: REPO-012, REPO-046, REPO-047

- Each format has exactly one built-in layout, named like the format (`npm`, `nuget`). A layout is
  code in the format plug-in, not configuration: it defines the canonical path scheme and derives
  module information (package name, version, file role) from a canonical path.
- The `layout` member is read-only and equals the format's layout. A create that names another
  layout answers 422 `unsupported_layout`.
- Module information MUST be recorded on items (the package and version rows of
  [storage.md](storage.md)), because the package view, cleanup and latest-version resolution need
  it.
- Later formats bring their own built-in layouts (a Maven 2 layout with the Maven family, a flat
  layout with the generic format). User-defined layouts, pattern tokens, integration-revision
  expressions and path translation between layouts are deferred (REPO-048 to REPO-052).

Acceptance criteria:

- `GET /api/v1/repositories/nuget-local` returns `"layout":"nuget"`; a create with
  `"layout":"maven-2"` for an npm repository returns 422 `unsupported_layout`.
- After `npm publish` of `@acme/util@1.4.0`, the tarball item belongs to package `@acme/util`,
  version `1.4.0`.

### R-REPO-007 · Repository caps
Priority: **P1** · Covers: REPO-025, REPO-027

The caps are settings here; [R-STOR-044](storage.md) enforces them after each push, and the format
plug-in's version-group hook decides what a group is.

| Member (`local.caps.…`) | Meaning | Default (proposed) | Constraint |
|---|---|---|---|
| `maxVariantsPerGroup` | Distinct snapshot or pre-release variants kept per version group (format-defined) | `0` (no cap) | `0` or ≥ 2 |
| `maxTagsPerImage` | Distinct tags kept per image (container formats) | `0` (no cap) | ≥ 0 |
| `keptTagRevisions` | Earlier revisions kept when a tag is overwritten (container formats) | `0` | ≥ 0 |

- A cap the format does not support answers 422 `not_applicable`.
- A variant cap of 1 is refused with 422 `out_of_range`: an in-flight multi-file deploy would remove
  its own siblings.
- The container API version and the legacy manifest switch of REPO-027 belong to the container
  format page.

Acceptance criteria:

- `PATCH` with `{"local":{"caps":{"maxVariantsPerGroup":1}}}` returns 422 `out_of_range`; with `5`
  it is stored and read back.
- On an npm repository, `maxTagsPerImage: 3` returns 422 `not_applicable`.

## Repository management API

All operations below are part of the OpenAPI document at `/api/v1/openapi.json` (INV-6). Every
change writes an audit event (INV-16; actor, action, repository, outcome; secrets as fingerprints)
in the same transaction as the change and signals the other replicas with
`NOTIFY trove_repo_config` carrying the repository id; replicas MUST apply a change within 2
seconds of commit (INV-12). Capability and action names are those of
[authorization.md](authorization.md): `repositories:read` and `repositories:write` (roles `admin`,
`repository-admin`), `settings:read` and `settings:write`, repository actions `read`, `deploy`,
`delete`, `manage`.

### R-REPO-008 · Configuration validation
Priority: **MVP** · Covers: REPO-053, REPO-054, RMT-002

The server MUST validate every create and update against all rules below in one pass and report
every violation (JSON pointer, code, message) in one 422 response. Malformed JSON and unknown
members answer 400 `invalid_document`: a configuration API that silently drops a misspelt setting
leaves the operator believing it is in effect.

| Rule | Code |
|---|---|
| Key syntax (R-REPO-002), project prefix for project administrators | `invalid_key` |
| Key taken, also by a repository being deleted | `repository_exists` (409) |
| Class or format unknown, or the format does not support the class | `unsupported_class`, `unknown_format` |
| Class, format or key changed on update | `immutable_field` |
| Pattern syntax or limits (R-REPO-005) | `invalid_pattern` |
| Member missing, being deleted, of another format, duplicated, or the virtual repository itself | `invalid_member` |
| Nesting cycle or depth over the limit (R-REPO-018) | `virtual_cycle`, `virtual_too_deep` |
| Deploy target not a local repository in the effective member list (R-REPO-021) | `invalid_deploy_target` |
| Pin rule member not in the effective member list (R-REPO-023) | `invalid_pin_member` |
| Upstream URL not absolute `http`/`https`, or carrying user info or a fragment (R-REPO-024) | `invalid_upstream_url` |
| Numeric setting outside its range (R-REPO-024 and others) | `out_of_range` |
| Referenced client certificate or outbound proxy missing | `unknown_reference` |
| Secret field with both `value` and `secretRef`, or a `secretRef` outside the rules of R-OPS-029 | `invalid_secret` |
| Custom header name or value invalid (R-REPO-036) | `invalid_header` |
| Member not applicable to class or format | `not_applicable` |

Warnings do not block a save; they are returned in a `warnings` list, for example
`dependency_confusion_unprotected` (R-REPO-023), `claim_overlaps` (R-REPO-023) or
`plain_http_upstream` (R-REPO-024).

Acceptance criteria:

- A create of a virtual npm repository whose members are a NuGet repository and a missing key
  returns one 422 response with two `invalid_member` entries.
- A create with `"remote":{"url":"https://user:pw@registry.example.com/"}` returns 422
  `invalid_upstream_url`.
- A create with the misspelt member `"excludePattern"` returns 400 `invalid_document`.

### R-REPO-009 · Create a repository
Priority: **MVP** · Covers: REPO-053, RMT-002, API-052

| | |
|---|---|
| Request | `POST /api/v1/repositories`, body: the configuration document; omitted settings take their defaults, format defaults included |
| Success | 201, `Location: /api/v1/repositories/<key>`, `ETag: "<config_version>"`, body: the stored document as R-REPO-010 returns it, plus `warnings` |
| Errors | 400 `invalid_document`; 401; 403; 409 `repository_exists`; 422 (R-REPO-008) |
| Permission | `repositories:write`; or project administrator of a project with `manageRepositories`, for keys `<projectKey>-…`, which are assigned to that project ([R-AUTHZ-026](authorization.md)) |

- Create is never an upsert. Configuration, relational rows, sealed secrets and the audit event are
  written in one transaction.
- Responses are JSON, not plain-text confirmations.

```json
{
  "key": "nuget-remote",
  "class": "remote",
  "format": "nuget",
  "description": "Proxy of nuget.org",
  "remote": {
    "url": "https://api.nuget.org/v3/index.json",
    "authMode": "basic",
    "username": "proxy",
    "password": { "secretRef": { "file": "nuget-upstream-token" } },
    "cacheFillAction": "read"
  }
}
```

Acceptance criteria:

- `curl -sS -X POST -H "Authorization: Bearer $TROVE_TOKEN" -H 'Content-Type: application/json'
  --data @npm-remote.json https://trove.example.com/api/v1/repositories` returns 201, and a `GET`
  of the `Location` shows `metadataTtlSeconds: 7200` and `notFoundTtlSeconds: 1800` from the npm
  format defaults.
- Repeating the call returns 409 `repository_exists`.
- The same call with a token lacking `repositories:write` returns 403 and creates nothing.

### R-REPO-010 · Read a repository configuration
Priority: **MVP** · Covers: REPO-055, REPO-056, API-057, API-058

| | |
|---|---|
| Request | `GET /api/v1/repositories/{key}` |
| Success | 200, `ETag: "<config_version>"`, body: the full document or the consumer view |
| Errors | 401; 404 per [R-AUTHZ-016](authorization.md) |
| Permission | `repositories:read` (or project administrator of the repository's project): full document; any repository action on the repository: consumer view |

- The document contains only members that apply to the repository's class and format; the
  catalog's format-filtered read is therefore the only read.
- Secret fields are never returned: an inline secret reads as `{"set": true}`, a reference reads back
  as the `secretRef` it is (a file name or variable name is not secret).
- The consumer view contains `key`, `class`, `format`, `description`, `layout` and the client
  addresses, as [R-AUTHZ-016](authorization.md) defines it. It omits upstream settings, notes, pins,
  claims and members: an internal registry host or another team's repository key is information
  too.

Acceptance criteria:

- With `repositories:read`, `GET /api/v1/repositories/nuget-remote` returns `remote.url` and
  `"password": {"secretRef": {"file": "nuget-upstream-token"}}`; for an inline secret it returns
  `{"set": true}` and never the value.
- A user holding only `read` on `nuget-remote` receives the consumer view without a `remote`
  member; a user holding nothing on it receives 404.

### R-REPO-011 · Update a repository configuration
Priority: **MVP** · Covers: REPO-054, RMT-002, API-054

| | |
|---|---|
| Request | `PATCH /api/v1/repositories/{key}` with `Content-Type: application/merge-patch+json` ([RFC 7396](https://www.rfc-editor.org/rfc/rfc7396)); or `PUT /api/v1/repositories/{key}` with a complete document (omitted settings revert to defaults). Optional `If-Match: "<config_version>"`. |
| Success | 200, new `ETag`, body: the stored document plus `warnings` |
| Errors | 400; 401; 403; 404; 409 managed by configuration as code ([R-OPS-029](operations-and-observability.md)); 412 `version_mismatch`; 422 (R-REPO-008) |
| Permission | as create (R-REPO-009); the authorization members (`cacheFillAction`, `packageOwnership`, `ownershipDomain`, `hideExistence`) per [R-AUTHZ-012](authorization.md) |

- Merge-patch semantics: arrays (members, patterns, headers, pin rules) are replaced as a whole;
  `null` removes an optional setting; an omitted secret field stays unchanged; a secret field set to
  `null` clears it.
- With `If-Match`, a stale version MUST be refused with 412. The UI MUST always send `If-Match`, so
  two administrators editing the same repository cannot overwrite each other unnoticed.
- A change of `remote.url`, `extraHosts`, `authMode`, `username`, `password`, `clientCertificate`
  or `headers` MUST invalidate all metadata and negative entries of the repository (scope `all`,
  path empty, R-REPO-045): what the old upstream said is no evidence about the new one. Cached
  artifacts are kept.
- `PUT` never creates a repository; a missing key answers 404.

Acceptance criteria:

- `PATCH` with `{"remote":{"cache":{"metadataTtlSeconds":600}}}` changes only that setting; the
  next `GET` shows it and a new `ETag`.
- Two `PATCH` calls with the same `If-Match` value: the first returns 200, the second 412.
- After a change of `remote.url`, the next `npm view lodash` through the repository causes an
  upstream request although the cached document was fresh.

### R-REPO-012 · List repositories
Priority: **MVP** · Covers: REPO-007, REPO-057, REPO-058, API-060, API-061

| | |
|---|---|
| Request | `GET /api/v1/repositories?class=&format=&q=&state=&project=&view=&pageSize=&pageToken=` |
| Success | 200, `{"repositories": [...], "nextPageToken": "…"}` (paging per [api-and-ui.md](api-and-ui.md)) |
| Errors | 400 invalid filter; 401 |
| Permission | any caller, anonymous included; the result contains only repositories on which the caller holds `read` ([R-AUTHZ-016](authorization.md)); `view=full` requires `repositories:read` |

- Filters: `class` and `format` take comma-separated lists; `q` matches key and description
  case-insensitively; `project` filters by project key; `state=inactive` returns disabled
  repositories, offline remote repositories and remote repositories whose circuit is open
  (R-REPO-029).
- `view=summary` (default) returns per repository: key, class, format, description, client address,
  `disabled`, `lastActivityAt`, project, and for remote repositories the availability (`online`,
  `offline`, `assumed-offline`). Holders of `repositories:read` also get the upstream URL.
- `view=full` returns complete documents (the catalog's "all configurations").
- Class and format values are spelled as in configuration documents (lower case).
- `lastActivityAt` is the latest upload to or download from the repository. Replicas MUST aggregate
  activity in memory and write it at most once per minute per repository, so downloads do not turn
  into database writes.

Acceptance criteria:

- `curl -s -H "Authorization: Bearer $TROVE_TOKEN"
  'https://trove.example.com/api/v1/repositories?class=remote&format=npm'` returns only npm remote
  repositories, each with an availability state.
- An anonymous caller, with anonymous read granted on `npm-all` only, sees exactly `npm-all`.
- `view=full` without `repositories:read` returns 403.

### R-REPO-013 · Delete a repository
Priority: **MVP** · Covers: REPO-063, RMT-002, API-063

| | |
|---|---|
| Request | `DELETE /api/v1/repositories/{key}?detach=true\|false` |
| Success | 204 for a virtual repository (no content); 202 with a job reference for local and remote repositories; repeated while the job runs: 202 with the same job |
| Errors | 401; 403; 404; 409 `repository_referenced` naming the referencing virtual repositories, when `detach` is false; 409 managed by configuration as code |
| Permission | as create (R-REPO-009) |

- The first transaction MUST set `lifecycle = 'deleting'`. From then on the repository is not
  resolvable, not addressable by clients, absent from virtual resolution and listed only to holders
  of `repositories:read`. With `detach=true`, the same transaction removes it from every member
  list, pin rule and deploy-target reference.
- A background job ([architecture.md](architecture.md)) removes items permanently, bypassing the
  trash can ([storage.md](storage.md), R-STOR-018), and removes cache entries, negative entries, URL
  mappings, claims and invalidations in batches. It is resumable after a replica restart. The
  repository row goes last, which frees the key.
- Job progress (items total and removed, errors) is read from the job resource.

Acceptance criteria:

- Deleting `npm-local` while `npm-all` lists it returns 409 naming `npm-all`; with `detach=true` it
  returns 202, `npm-all` no longer lists it, and `npm view @acme/util --registry
  …/api/npm/npm-local/` returns `E404` at once.
- After the job completes, a new repository named `npm-local` can be created and is empty.
- Killing the replica that runs the job does not prevent completion: another replica resumes it.

### R-REPO-014 · Batch operations
Priority: **P1** · Covers: REPO-059, REPO-061, REPO-062, REPO-064, API-053, API-055, API-059, API-064

| Request | Semantics | Success | Permission |
|---|---|---|---|
| `POST /api/v1/repositories:batchCreate` `{"repositories": [doc, …]}` | All or nothing, one transaction; members may reference repositories created in the same batch | 201 with the stored documents | as create |
| `POST /api/v1/repositories:batchUpdate` `{"updates": [{"key", "ifMatch", "patch"}, …]}` | All or nothing, one transaction, merge patch per entry | 200 with the stored documents | as update |
| `POST /api/v1/repositories:batchGet` `{"keys": [...]}` | R-REPO-010 per key | 200 `{"repositories": {"<key>": doc}, "missing": [...]}` | as read, per key |
| `POST /api/v1/repositories:batchDelete` `{"keys": [...], "detach": false}` | Best effort per key, in request order | 202 with one result per key (job reference or error) | as delete |

- The batch size is limited by `repositories.batch_limit`; larger batches answer 400
  `batch_too_large`. A failing create or update entry answers 422 (or 404, 412) naming the index of
  every failing entry.
- A key the caller may not see is reported under `missing`, like a key that does not exist.

Configuration:

```yaml
repositories:
  batch_limit: 100  # default (proposed)
```

Acceptance criteria:

- A `batchCreate` of `npm-local`, `npm-remote` and a virtual `npm-all` over both succeeds in one
  call; the same call with an invalid third entry creates nothing.
- A `batchGet` of 101 keys returns 400 `batch_too_large`.

### R-REPO-015 · Quick setup of a repository set
Priority: **P1** · Covers: REPO-008

| | |
|---|---|
| Request | `POST /api/v1/repositories:quickSetup` `{"prefix": "billing", "formats": ["npm", "nuget"]}` |
| Success | 201 with the created documents |
| Errors | 409 when any generated key exists (nothing created); 422 invalid prefix or format |
| Permission | as create (a project administrator's prefix must start with the project key) |

- Per format, one transaction creates `<prefix>-<format>-local`, `<prefix>-<format>-remote`
  (upstream: the format's default upstream URL) and the virtual `<prefix>-<format>` with members
  local then remote, the local repository as deploy target, and `autoClaim` on for the local
  repository (R-REPO-023).
- The prefix MUST be a valid key fragment of at most 20 characters.

Acceptance criteria:

- Quick setup with prefix `billing` and format `npm` creates `billing-npm-local`,
  `billing-npm-remote` and `billing-npm`; `npm install lodash --registry
  https://trove.example.com/api/npm/billing-npm/` succeeds, and `npm publish` to the same registry
  lands in `billing-npm-local`.

### R-REPO-016 · Recalculate a repository's index
Priority: **P1** · Covers: REPO-009

| | |
|---|---|
| Request | `POST /api/v1/repositories/{key}/reindex` |
| Success | 202 with a job reference |
| Errors | 404; 405 for formats or classes without derived metadata; 409 `reindex_running` naming the running job |
| Permission | `repositories:write`, or `manage` on the repository |

- The format plug-in rebuilds the repository's derived metadata (npm package documents, NuGet
  registrations and version lists) from its items. On a virtual repository the operation drops its
  merged-metadata cache (R-REPO-020).
- At most one reindex per repository runs at a time (advisory-lock lease, INV-12). Clients are
  served the previous metadata until the rebuilt metadata commits.

Acceptance criteria:

- After the derived metadata rows of `npm-local` are deleted in a test database, `npm view
  @acme/util` fails; after a reindex job completes, it succeeds with all versions.

## Virtual repositories

### R-REPO-017 · Virtual repository membership
Priority: **MVP** · Covers: REPO-033

| Member (`virtual.…`) | Meaning | Default (proposed) | Constraint |
|---|---|---|---|
| `members` | Ordered member keys | `[]` (warning `no_members`) | ≤ `repositories.max_virtual_members`; same format; no duplicates |
| `deployTarget` | R-REPO-021 | `null` | local repository in the effective member list |
| `mergedMetadataTtlSeconds` | R-REPO-020 | `7200` | 0–86400 |
| `pinning` | R-REPO-023 | `{"rules": []}` | R-REPO-023 |
| `effectiveMembers` | R-REPO-018, read-only | — | — |

- Members MUST be of the virtual repository's format. In the MVP they are local or remote
  repositories; virtual members arrive with R-REPO-018 and are refused with 422 `invalid_member`
  until then.
- Members and their order can change without touching client configuration; a change takes effect
  on all replicas within 2 seconds and drops the repository's merged-metadata cache (R-REPO-020).
- Recommended practice, shown in the UI: clients use a virtual repository; local members come first.

Data model:

```sql
CREATE TABLE virtual_members (
  virtual_id bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
  position   integer NOT NULL,
  member_id  bigint NOT NULL REFERENCES repositories (id) ON DELETE RESTRICT,
  PRIMARY KEY (virtual_id, position),
  UNIQUE (virtual_id, member_id),
  CHECK (virtual_id <> member_id)
);
CREATE INDEX virtual_members_member ON virtual_members (member_id);

CREATE TABLE virtual_deploy_targets (
  virtual_id bigint PRIMARY KEY REFERENCES repositories (id) ON DELETE CASCADE,
  target_id  bigint NOT NULL REFERENCES repositories (id) ON DELETE RESTRICT
);
```

Configuration:

```yaml
repositories:
  max_virtual_members: 100 # default (proposed)
```

Acceptance criteria:

- A virtual `npm-all` with members `npm-local`, `npm-remote` serves `npm install lodash --registry
  https://trove.example.com/api/npm/npm-all/`.
- With two replicas, a `PATCH` that reverses the member order is visible in the resolution
  explanation (R-REPO-019) of both replicas within 2 seconds.

### R-REPO-018 · Nested virtual repositories
Priority: **P1** · Covers: REPO-034

- A virtual repository MAY list virtual repositories of the same format as members.
- The effective member list MUST be computed by depth-first expansion in member order, keeping only
  the first occurrence of each repository. It is recomputed whenever a repository on the way
  changes and is returned read-only as `virtual.effectiveMembers`, each entry with the chain of
  virtual repositories it was reached through.
- Nesting depth is limited by `repositories.max_virtual_depth`; deeper compositions answer 422
  `virtual_too_deep`. A cycle MUST be refused at save time with 422 `virtual_cycle`. Membership
  changes MUST be serialized with a transaction-scoped advisory lock (`pg_advisory_xact_lock`) on
  one fixed key, so two concurrent updates cannot form a cycle that neither transaction sees.
- A nested virtual repository keeps its rules for its subtree: a request reaches a member only if
  every virtual repository on the member's chain admits it (R-REPO-005) and the caller passes each
  one's gate ([R-AUTHZ-009](authorization.md)); each virtual repository's pin rules restrict its own
  subtree (R-REPO-023). Deploy targets and merged-metadata periods of nested virtual repositories
  are ignored; those of the requested virtual repository apply.

Configuration:

```yaml
repositories:
  max_virtual_depth: 5 # default (proposed)
```

Acceptance criteria:

- `npm-all` contains `npm-team` (virtual over `npm-team-local`) and `npm-remote`;
  `effectiveMembers` lists `npm-team-local` then `npm-remote`, with `npm-team` in the chain of the
  first.
- Updating `npm-team` to include `npm-all` returns 422 `virtual_cycle`; two concurrent updates that
  together would form a cycle leave exactly one committed.

### R-REPO-019 · Virtual resolution algorithm
Priority: **MVP** · Covers: REPO-035, REPO-036

For a request to virtual repository V with descriptor d (R-REPO-003) by caller c, the server MUST:

1. Apply the gate of [R-AUTHZ-009](authorization.md): c holds `read` on V; an anonymous caller
   additionally needs `read` on every member, otherwise 401. Answer 404 if V is disabled or V's
   patterns do not admit d.
2. Take V's effective member list in order; drop members that are disabled, being deleted, or whose
   patterns (or those of a virtual repository on their chain) do not admit d.
3. Apply pin rules and namespace claims (R-REPO-023). They depend on the package name and on stored
   configuration only, never on c's permissions.
4. Drop members on which c does not hold `read` for d. Unreadable members are skipped silently; they
   never turn the answer into 403 ([R-AUTHZ-009](authorization.md)).
5. Order the remaining members stably: priority members first (R-REPO-022, P1), else list order.
6. Resolve by kind:
   - `artifact`, in three tiers, first hit wins: (a) local members; (b) remote members' caches,
     without contacting any upstream; (c) remote members' upstreams through the remote request path
     (R-REPO-025), only for members on which c passes the cache-fill check. A latest-version request
     (format-defined: a request whose answer is "the newest version") is the exception: all members,
     upstreams included, are consulted and the newest version wins (REPO-036).
   - `metadata`: collect the document from every remaining member that knows the package (remote
     members through R-REPO-025, which refreshes expired entries for callers that pass the
     cache-fill check) and merge them (R-REPO-020).
   - `search` and `other`: format page.
   - `write`: route to the deploy target (R-REPO-021).
7. When nothing was found: answer 404 if every consulted member answered "not found"; otherwise
   answer with the failure of the first member that failed transiently (502, 503 or 504 per
   R-REPO-029), because "not found" would be a false statement about an item that may exist.

- Artifact responses MUST carry `Trove-Resolved-From: <member key>`; the member is one the caller
  can read, so the header discloses nothing new.
- **Resolution explanation.** `GET /api/v1/repositories/{key}/resolution?path=<client path>`
  evaluates steps 1 to 6 for the calling principal without transferring content and without
  contacting upstreams, and returns per member the decision and its reason (`disabled`,
  `not-admitted`, `pinned-out`, `claimed-elsewhere`, `refused-claimed`, `no-read`, `no-cache-fill`,
  `hit`, `miss`, `negative`, `offline`, `circuit-open`). Members the caller cannot read appear only
  as a count (`"unreadableMembers": 2`) unless the caller holds `repositories:read`. Permission: the
  gate of step 1.

```json
{
  "repository": "npm-all",
  "descriptor": { "path": "@acme/util/-/util-1.4.0.tgz", "kind": "artifact", "package": "@acme/util" },
  "decidedBy": { "claim": "@acme/*", "claimedBy": ["npm-local"] },
  "members": [
    { "key": "npm-local", "class": "local", "decision": "hit" },
    { "key": "npm-remote", "class": "remote", "decision": "pinned-out" }
  ],
  "unreadableMembers": 0
}
```

Acceptance criteria:

- `left-pad@1.0.0` exists in `npm-local` and in the stub upstream of `npm-remote`; members are
  ordered remote first: `npm install left-pad@1.0.0 --registry …/api/npm/npm-all/` downloads the
  local tarball (`Trove-Resolved-From: npm-local`), because local members form the first tier.
- A caller with `read` on `npm-all` and `npm-remote` only receives the upstream package and no error
  from `npm-local`.
- On `npm-remote` with `cacheFillAction: deploy`, a caller with `read` but without `deploy` gets 404
  for a tarball not yet cached, and the explanation shows `no-cache-fill` for `npm-remote`.
- With the stub upstream down and nothing cached, the request returns 502, not 404.

### R-REPO-020 · Merged metadata and its cache
Priority: **MVP** · Covers: REPO-037

- For metadata requests, a virtual repository MUST answer with a document merged from the members of
  R-REPO-019 step 6. Merge rules per document type are on the format pages
  ([formats/npm.md](formats/npm.md), [formats/nuget.md](formats/nuget.md)).
- For every version, the merged document MUST take the version entry from the member that tier
  resolution would pick for that version's artifact, so the digest a client reads and the bytes it
  downloads come from the same member.
- Merged documents MAY be cached per replica in memory. The cache key MUST include the virtual
  repository, the canonical path, the variant and a SHA-256 fingerprint of the ordered list of
  members that took part after permission filtering: two callers who see different members never
  share an entry ([R-AUTHZ-009](authorization.md) rule 4); callers who see the same members do.
- A merged entry expires at the earlier of `mergedMetadataTtlSeconds` after merging and the earliest
  expiry of the contributing remote entries, so staleness does not add up across the two layers.
- A change of a member's metadata (publish, delete, restore, remote refresh with new content, claim
  change) MUST drop the affected merged entries on all replicas (`NOTIFY trove_metadata_changed`
  with repository id and package-name hash). A version published to a local member is visible
  through the virtual repository at once, whatever the period.
- A remote member that fails transiently and has no stored copy is left out of the merge; the result
  MUST NOT be cached, and the response carries `Cache-Status: trove; detail=partial`
  (R-REPO-047). Pin rules and claims keep internal names independent of remote members, so a partial
  merge cannot switch a package's origin.

Configuration:

```yaml
proxy:
  merged_metadata_cache_bytes: 268435456  # default (proposed), per replica
```

Acceptance criteria:

- After `npm publish` of `@acme/util@1.5.0` to `npm-local`, `npm view @acme/util versions
  --registry …/api/npm/npm-all/` on the other replica lists `1.5.0` immediately.
- User A (read on both members) and user B (read on `npm-local` only) request the same document in
  turn; B's response contains no version that exists only upstream.

### R-REPO-021 · Default deployment target
Priority: **MVP** · Covers: REPO-038

- `virtual.deployTarget` names a local repository in the effective member list. Without one, the
  virtual repository is read-only: writes answer 405 `no_deploy_target`, and format pages leave the
  publish resources out of their service documents.
- A write through the virtual address (format publish, `PUT /files/<virtual>/<path>`) MUST be
  executed as if addressed to the target: the target's patterns, format rules, ownership and
  permissions apply ([R-AUTHZ-009](authorization.md) rule 5). Format writes such as npm deprecate and
  dist-tag changes or NuGet delete, unlist and relist are routed to the target as the format pages
  define.
- A raw `DELETE /files/<virtual>/<path>` answers 405 ([storage.md](storage.md)).
- An update that removes the target from the members without changing the target answers 422
  `invalid_deploy_target`.

Acceptance criteria:

- `npm publish --registry https://trove.example.com/api/npm/npm-all/` stores the package in
  `npm-local`; `dotnet nuget push Acme.Widgets.1.0.0.nupkg --source
  https://trove.example.com/api/nuget/nuget-all/v3/index.json --api-key "$TROVE_TOKEN"` stores it
  in `nuget-local`.
- Without a target, `npm publish` to `npm-all` fails with `E405`.

### R-REPO-022 · Priority members
Priority: **P1** · Covers: REPO-018, RMT-037

- `priorityMember: true` on a local or remote repository MUST (a) place it ahead of non-priority
  members within each resolution tier of every virtual repository that contains it, and (b) restrict
  metadata merging: if any priority member holds the package, only priority members contribute.
- Whether a priority member holds the package MUST be decided without regard to the caller's
  permissions, for the reason given in R-REPO-023.
- Pin rules and claims are evaluated first and are not overridden by priority. They are the
  recommended control; priority members exist for configurations that rely on them.
- Support is a format capability (R-REPO-003); npm and NuGet support it, which settles the catalog's
  open question for NuGet.

Acceptance criteria:

- `npm-local` is a priority member holding `util@1.0.0`, the stub upstream holds `util@9.9.9`:
  `npm view util versions --registry …/api/npm/npm-all/` lists only `1.0.0`; after the local
  package is deleted, it lists the upstream versions.

### R-REPO-023 · Dependency-confusion protection: pin rules and namespace claims
Priority: **MVP** · Covers: BEYOND-028

Dependency confusion ([Birsan 2021](https://medium.com/@alex.birsan/dependency-confusion-4a5d60fec610))
lets a public package with an internal package's name, usually with a higher version, win
resolution. Tier order alone protects only names that currently exist internally; a name that is
not published yet, was deleted or is misspelt falls through to the remote members. trove closes
this on the server with two mechanisms, both using name patterns (see Terms; most specific wins).

**Namespace claims (local repositories).** A local repository holds a list of claimed name
patterns.

- With `local.autoClaim: true` (default (proposed) for formats that support claims), the first
  deploy of a package name that no claim of that repository covers creates an exact claim with
  scope `members` in the same transaction. Claims are not removed when packages are deleted: the
  deleted case is exactly when protection matters.
- Claims for names that do not exist yet are created explicitly; this reserves a scope or prefix
  before its first publish.
- **Claim scope.** Every claim has a scope. `members` (the default, and the only scope an auto
  claim gets) is in effect only in virtual repositories that contain the claiming repository,
  directly or through nesting. `global` is in effect in every virtual repository of the format and
  is the only scope that remote refusal honours. Creating or widening a `global` claim MUST require
  `repositories:write`; `manage` on the repository suffices for `members` claims. Without this
  split, one deploy of `react` into any local repository, even a personal sandbox that no virtual
  repository contains, would make `react` unresolvable in every virtual repository and, through
  remote refusal, in every remote repository of the format.
- **In a virtual repository**, for a package name n without an applicable pin rule: if a claim in
  effect in V covers n (a `global` claim of any local repository of the format, or a `members`
  claim of a member of V), only the local members of V whose claims are the most specific match for
  n are consulted; if V has no such member, the answer is 404. Remote members are not asked: no
  cache lookup, no upstream request, no negative-cache entry.
- **Remote refusal.** With `proxy.claims.remote_refusal: true`, every remote repository of the format,
  also when addressed directly, answers 404 for a name covered by a `global` claim and does not
  serve cached copies of it, unless the request comes through a virtual repository whose pin rule
  names that remote repository for n.
- Overlapping claims of different repositories are allowed and reported as the warning
  `claim_overlaps`; under most-specific matching, a team that claims `@acme/billing-*` takes those
  names from a broader `@acme/*` claim. Who may publish a name is decided by package ownership
  ([R-AUTHZ-018](authorization.md)); claims decide where a name resolves from.

**Pin rules (virtual repositories).** `virtual.pinning.rules` is a list of
`{"packages": [name patterns], "members": [member keys]}`.

- For a package name n, the most specific pattern over all rules of V selects its rule; only that
  rule's members are consulted for every request about n (documents, artifacts, latest-version,
  search results). When none of them has the package, the answer is 404.
- Rule members MUST be in V's effective member list. A rule may name a remote member, for a partner
  registry that owns a scope; that is the only way a claimed name reaches a remote repository.
- Pin rules take precedence over claims; claims over priority members (R-REPO-022).

**Independence from the caller.** Both mechanisms MUST be evaluated on stored configuration only,
never on the caller's permissions. If they honoured permissions, a caller who cannot read the
internal member would fall through to the public package of the same name and receive exactly the
confused dependency; such a caller receives 404 instead (H-7 names the remaining disclosure).

- Each replica keeps all claims and pin rules of all repositories in memory (a prefix trie per
  format), loaded at start and reloaded on `NOTIFY trove_claims`; a lookup costs no database
  query.
- On create or update of a virtual repository that has local and remote members, no pin rule, and
  local members without `autoClaim` and without claims, the response carries the warning
  `dependency_confusion_unprotected`.

Data model:

```sql
CREATE TABLE namespace_claims (
  repo_id    bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
  pattern    text NOT NULL CHECK (length(pattern) BETWEEN 1 AND 256),
  source     text NOT NULL CHECK (source IN ('manual', 'auto')),
  scope      text NOT NULL DEFAULT 'members' CHECK (scope IN ('members', 'global')),
  created_at timestamptz NOT NULL DEFAULT now(),
  created_by uuid,
  PRIMARY KEY (repo_id, pattern),
  CHECK (source = 'manual' OR scope = 'members')
);

CREATE TABLE virtual_pin_rules (
  virtual_id       bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
  position         integer NOT NULL,
  package_patterns text[] NOT NULL CHECK (cardinality(package_patterns) BETWEEN 1 AND 50),
  PRIMARY KEY (virtual_id, position)
);

CREATE TABLE virtual_pin_members (
  virtual_id bigint NOT NULL,
  position   integer NOT NULL,
  member_id  bigint NOT NULL REFERENCES repositories (id) ON DELETE RESTRICT,
  PRIMARY KEY (virtual_id, position, member_id),
  FOREIGN KEY (virtual_id, position) REFERENCES virtual_pin_rules ON DELETE CASCADE
);
```

API:

| Method and path | Body / response | Permission |
|---|---|---|
| `GET /api/v1/repositories/{key}/claims` | 200 `{"claims": [{"pattern": "@acme/*", "source": "manual", "scope": "global", "createdAt": …}]}` | `read` on the repository |
| `PUT /api/v1/repositories/{key}/claims` | `{"claims": [{"pattern": "@acme/*", "scope": "global"}]}`; `scope` defaults to `members`; replaces the manual claims; auto claims stay unless listed under `"removeAuto"` | `manage` on the repository for `members` claims; `repositories:write` when the request adds or keeps a `global` claim |
| `PATCH /api/v1/repositories/{key}` | `{"virtual": {"pinning": {"rules": [...]}}}` (R-REPO-011) | as update |

Configuration:

```yaml
proxy:
  claims:
    remote_refusal: true  # default (proposed), Q-REPO-4
```

Limits: at most 1000 claims per repository and 200 pin rules per virtual repository; patterns at
most 256 characters.

Acceptance criteria (counting stub upstream behind `npm-remote` and `nuget-remote`):

- `npm-local` claims `@acme/*` with scope `global`. The stub publishes `@acme/util@99.0.0`: `npm view @acme/util
  versions --registry …/api/npm/npm-all/` lists only local versions; `npm view @acme/brand-new`
  fails with `E404`; `npm view @acme/brand-new --registry …/api/npm/npm-remote/` fails with `E404`
  as well; the stub records no request for either name.
- `autoClaim: true` and `left-pad@1.0.0` published to `npm-local`; the stub holds `left-pad@2.0.0`.
  `npm install left-pad --registry …/api/npm/npm-all/` installs `1.0.0`. After the local package is
  deleted, the install fails with `E404` instead of installing `2.0.0`. A user with `read` on
  `npm-all` but not on `npm-local` gets `E404`, not `2.0.0`.
- `nuget-local` claims `Acme.*` with scope `global`: `dotnet add package Acme.Internal` against
  `nuget-remote` directly fails with a not-found error, and the stub records no request.
- `npm-sandbox` (local, `autoClaim: true`, member of no virtual repository) receives a publish of
  `react@99.0.0`. The claim reads back with scope `members`; `npm view react versions --registry
  …/api/npm/npm-all/` still lists the upstream versions, and `npm-remote` addressed directly still
  serves `react`.
- A principal holding `manage` on `npm-local` but not `repositories:write` gets 403 on
  `PUT …/claims` with a `global` claim, and 200 with the same pattern as a `members` claim.
- A pin rule `{"packages": ["@partner/*"], "members": ["npm-partner-remote"]}` in `npm-all` resolves
  `@partner/sdk` from that remote even though `npm-local` claims `@partner/*`.

## Remote repositories (pull-through proxy)

### R-REPO-024 · Remote repository settings
Priority: **MVP** · Covers: RMT-001, RMT-002, RMT-004, RMT-015

A remote repository proxies exactly one upstream and caches what clients ask for; nothing is fetched
ahead of time (INV-11). Clients cannot deploy into it. Every setting below is a member of the
`remote` section; omitted members take the default, format defaults come from the format catalog
(R-REPO-003).

<details>
<summary>All remote settings</summary>

| Member (`remote.…`) | Meaning | Default (proposed) | Range | Catalog | Spec |
|---|---|---|---|---|---|
| `url` | Upstream base URL (NuGet: the service index URL) | — (required) | absolute `http`/`https`, no user info, no fragment | RMT-004 | here |
| `extraHosts` | Further hosts that upstream metadata or redirects may point to: `[{"pattern": "*.nuget.org", "sendCredentials": false}]` | `[]`; formats propose entries for well-known upstreams | ≤ 20 | — | R-REPO-043 |
| `authMode` | `none`, `basic`, `bearer` | `none` | | RMT-005, RMT-006 | R-REPO-033 |
| `username` | Upstream account | `""` | ≤ 256 | RMT-005 | R-REPO-033 |
| `password` | Password or token; secret field | unset | ≤ 8 KiB | RMT-005 | R-REPO-033 |
| `clientCertificate` | Name of a stored client certificate | `null` | | RMT-007 | R-REPO-034 |
| `headers` | Extra request headers | `[]` | ≤ 32 | RMT-009 | R-REPO-036 |
| `forwardAuthOnRedirect` | Send credentials to other origins on redirects | `false` | | RMT-012 | R-REPO-039 |
| `outboundProxy` | `{"mode": "default"\|"named"\|"direct", "name": …}` | `{"mode": "default"}` | | RMT-016 | R-REPO-037 |
| `connectTimeoutMs` | TCP and TLS connection establishment | `10000` | 1000–120000 | RMT-015 | here |
| `readTimeoutMs` | Longest time without receiving a byte | `15000` | 1000–600000 | RMT-015 | here |
| `sourceAddress` | Local IP address for upstream connections | `null` (kernel choice) | an address of the pod | RMT-015 | here |
| `cacheFillAction` | `read` or `deploy` | `read` | | IAM-023 | [R-AUTHZ-008](authorization.md) |
| `cache.storeArtifacts` | `false` = pass-through | `true` | | RMT-017 | R-REPO-044 |
| `cache.metadataTtlSeconds` | Metadata period | format default (npm and NuGet: `7200`) | 0–2592000 | RMT-018 | R-REPO-027 |
| `cache.metadataRefreshTimeoutSeconds` | Wait for a refresh before serving stale | `60` | 1–600 | RMT-019 | R-REPO-027 |
| `cache.notFoundTtlSeconds` | Negative-cache period | format default (npm and NuGet: `1800`) | 0–86400 | RMT-020 | R-REPO-028 |
| `cache.maxStaleSeconds` | Upper bound for serving stale metadata, `0` = unbounded | `0` | 0–31536000 | — | R-REPO-029 |
| `cache.unusedRetentionHours` | Remove cache content unused this long, `0` = keep | `0` | 0–87600 | RMT-021 | R-REPO-046 |
| `integrity.checksumPolicy` | `verify-if-available`, `require`, `ignore`, `pass-through` | `verify-if-available` | | RMT-022 | R-REPO-041 |
| `integrity.blockUnexpectedContentTypes` | Refuse error pages in place of content | `true` | | RMT-023 | R-REPO-042 |
| `integrity.allowedContentTypes` | Exemptions from the blocked list | `[]` | ≤ 20 | RMT-023 | R-REPO-042 |
| `availability.mode` | `online` or `offline` | `online` | | RMT-030 | R-REPO-030 |
| `availability.failureThreshold` | Consecutive failures that open the circuit | `5` | 1–100 | RMT-029 | R-REPO-029 |
| `availability.circuitOpenSeconds` | Assumed-offline period, `0` = never | `300` | 0–86400 | RMT-029 | R-REPO-029 |
| `limits.maxArtifactBytes` | Per-artifact limit, `null` = system default | `null` | | — | R-REPO-040 |
| `limits.maxMetadataBytes` | Per-document limit, `null` = system default | `null` | | — | R-REPO-040 |
| `limits.maxConcurrentFetches` | Upstream fetches in flight per replica | `32` | 1–1024 | — | R-REPO-040 |

</details>

- `url` is stored normalized (scheme and host lower case, default port removed). The upstream request
  for a canonical path is derived from it by the format plug-in. A plain `http` URL is accepted with
  the warning `plain_http_upstream`.
- `sourceAddress` MUST be an address of the pod; otherwise every fetch fails and the circuit opens,
  which the upstream test reveals.

Data model (scalar settings live in `repositories.config`; references and secrets are relational):

```sql
CREATE TABLE remote_references (
  repo_id                 bigint PRIMARY KEY REFERENCES repositories (id) ON DELETE CASCADE,
  client_certificate_name text REFERENCES client_certificates (name) ON DELETE RESTRICT,
  outbound_proxy_name     text REFERENCES outbound_proxies (name) ON DELETE RESTRICT
);
```

API, besides R-REPO-009 to R-REPO-013:

| Method and path | Purpose | Response | Permission |
|---|---|---|---|
| `POST /api/v1/repositories/{key}/upstream-test` | Probe the saved upstream configuration | 200 with the result below | `repositories:write` |
| `POST /api/v1/upstream-tests` | Probe an unsaved `remote` section (body: `format` and `remote`); secret values in the body are used once and not stored | 200 with the result below | `repositories:write` |

```json
{
  "reachable": true,
  "httpStatus": 200,
  "authenticated": true,
  "durationMs": 184,
  "tls": { "peerSubject": "CN=registry.npmjs.org", "notAfter": "2027-03-01T00:00:00Z" },
  "outboundProxy": null,
  "error": null
}
```

The probe requests the format's probe resource (npm: the registry ping; NuGet: the service index,
whose required resources [formats/nuget.md](formats/nuget.md) checks) through the same client,
egress rules, proxy and credentials that real fetches use.

Acceptance criteria:

- A remote created with only `url` reads back every member of the table with its default.
- The upstream test against `https://registry.npmjs.org/` returns `reachable: true`; against an
  unknown host it returns `reachable: false` with a DNS error and leaves the circuit state alone.
- `"connectTimeoutMs": 50` returns 422 `out_of_range`.

### R-REPO-025 · Remote request path
Priority: **MVP** · Covers: RMT-001, RMT-033

For a request with descriptor d (R-REPO-003) to remote repository R by caller c, the server MUST
execute these steps in order:

1. R disabled → 404 `repository_disabled`.
2. d not admitted by R's patterns, or d's package name refused because of a claim (R-REPO-023) →
   404, no upstream request.
3. c lacks `read` on R for d → 401, 403 or 404 per [R-AUTHZ-016](authorization.md).
4. Look up the cache entry (R, path, variant). An artifact entry, or a metadata entry that is fresh
   and not invalidated (R-REPO-045), is served: `Cache-Status: trove; hit; ttl=<seconds>`.
5. Request through the cache view, or with `Cache-Control: only-if-cached` → R-REPO-032.
6. R offline, global offline (R-REPO-031), or read-only maintenance mode with `cacheFill: false`
   ([R-OPS-024](operations-and-observability.md)) → serve an existing entry as stale, otherwise 404
   `not_cached_offline`.
7. R's circuit open (R-REPO-029) → serve an existing entry as stale, otherwise 503
   `upstream_assumed_offline` with `Retry-After`.
8. A valid negative entry (R-REPO-028) → 404, `Cache-Status: trove; hit; detail=negative`.
9. c fails the cache-fill check ([R-AUTHZ-008](authorization.md)) → serve an existing expired entry
   as stale without contacting the upstream, otherwise 403 `populate_not_permitted`
   (R-AUTHZ-008; through a virtual repository the member counts as a miss). No negative entry is
   written.
10. Concurrency limits (R-REPO-040), then the fetch through the collapsing protocol (R-REPO-026),
    classification of the outcome (R-REPO-029), verification (R-REPO-041, R-REPO-042), storage and
    response.

- `HEAD` is answered from the cache when an entry exists; otherwise the upstream is asked with `HEAD`
  under steps 1 to 10, and nothing is stored.
- `Range` requests are served from a stored artifact ([storage.md](storage.md)). For an uncached
  artifact the whole artifact is fetched first (one flight), then the range is served.
- Client query parameters are not forwarded unless the format plug-in maps them (search queries).
- The upstream request URL MUST be built from `url` and the canonical path by percent-encoding
  every path segment again ([RFC 3986, section 3.3](https://www.rfc-editor.org/rfc/rfc3986#section-3.3):
  `%` becomes `%25`; separators inside a name, such as npm's scoped-name `/`, follow the format
  page). A canonical path is never concatenated into an upstream URL as it is. It has been decoded
  once (Terms), so a client that sent `%252e%252e` would otherwise make trove request `..` from
  the upstream, with the upstream credentials of R-REPO-033.
- Fetched content is committed through the upload pipeline of [storage.md](storage.md) (spool,
  hashing, deduplication, R-STOR-004 and R-STOR-005). Artifacts become items of R (with the item's
  `upstream_url` set): they can be browsed, annotated, copied and deleted. Metadata documents are
  cache entries only; their bytes are blobs as well (INV-4), counted as references by garbage
  collection, but they are not items.
- **Object store unavailable** ([R-STOR-036](storage.md)): an artifact miss is streamed to the client
  without storing it, under the pass-through rules of R-REPO-044 (verification included); a
  refreshed metadata document is served but not stored, and entries are not updated.
- Replicas SHOULD keep recently served metadata bytes in a bounded in-memory cache keyed by blob
  digest, so a fresh hit costs no object-store round trip.

Data model:

```sql
CREATE TABLE remote_cache_entries (
  repo_id                bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
  path                   text COLLATE "C" NOT NULL,
  variant                text NOT NULL DEFAULT '',
  kind                   text NOT NULL CHECK (kind IN ('artifact', 'metadata')),
  item_id                bigint REFERENCES items (id) ON DELETE CASCADE,  -- artifacts (storage.md)
  blob_sha256            bytea NOT NULL REFERENCES blobs (sha256),       -- a blob reference
  upstream_url           text NOT NULL,     -- final URL after redirects, without user info
  upstream_etag          text,
  upstream_last_modified timestamptz,
  fetched_at             timestamptz NOT NULL,   -- last 200 from the upstream
  validated_at           timestamptz NOT NULL,   -- last 200 or 304
  expires_at             timestamptz,            -- NULL for artifacts
  upstream_gone_at       timestamptz,            -- upstream answered 404/410 (R-REPO-029)
  last_used_at           timestamptz NOT NULL,   -- metadata use, written in batches (R-REPO-046)
  PRIMARY KEY (repo_id, path, variant),
  CHECK ((kind = 'artifact') = (item_id IS NOT NULL)),
  CHECK ((kind = 'artifact') = (expires_at IS NULL))
);
CREATE INDEX remote_cache_entries_unused ON remote_cache_entries (repo_id, last_used_at)
  WHERE kind = 'metadata';
```

Acceptance criteria:

- First `npm install lodash@4.17.21 --registry …/api/npm/npm-remote/`: the stub upstream records one
  document and one tarball request; the response carries `Cache-Status: trove; fwd=uri-miss;
  fwd-status=200; stored`. A second install records no upstream request and carries `hit`.
- With `cacheFillAction: deploy`, a user with `read` only installs `lodash@4.17.21` (cached) and
  gets `E403` (`populate_not_permitted`) for `lodash@4.17.20` (not cached); the stub records no
  request.
- `curl -I …/files/npm-remote/express/-/express-4.21.2.tgz` for an uncached tarball returns the
  upstream's headers and leaves no cache entry.
- With MinIO stopped, an uncached tarball is still installed through `npm-remote` and no cache entry
  is written.
- A unit test of the upstream URL builder turns the canonical segment `%2e%2e` into `%252e%252e`
  and `a b` into `a%20b`; no input produces an upstream path that decodes to a `..` segment.

### R-REPO-026 · Collapsing concurrent misses (single-flight and advisory lock)
Priority: **MVP** · Covers: trove-only

Concurrent requests for the same uncached or expired entry MUST cause one upstream fetch per
cluster, not one per request (INV-11). The protocol:

1. **Flight key.** (repository id, canonical path, variant). The cross-replica lock is the
   two-key advisory lock of category `fetch` ([architecture.md](architecture.md) R-ARCH-017,
   `classid` 2); its lock id (`objid`) is the first 4 bytes of SHA-256 over
   `repository-id 0x00 path 0x00 variant`, read as a signed big-endian 32-bit integer. A collision
   only serializes two unrelated fetches.
2. **In-process.** Requests on one replica join one flight
   ([singleflight](https://pkg.go.dev/golang.org/x/sync/singleflight)). The leader runs steps 3 to 6;
   followers wait for its result and then read the stored entry, or receive its error.
3. **Cross-replica lock.** The leader takes a connection from a dedicated lock pool and calls
   `pg_try_advisory_lock(2, <lock id>)`, a session-level lock
   ([PostgreSQL advisory locks](https://www.postgresql.org/docs/current/explicit-locking.html#ADVISORY-LOCKS)).
   If acquired, it re-reads the entry (another replica may have just finished) and serves it if now
   valid; otherwise it fetches.
4. **Waiting.** If the lock is held elsewhere, the leader waits for `NOTIFY trove_proxy_fetch`
   carrying the lock id ([NOTIFY](https://www.postgresql.org/docs/current/sql-notify.html), received
   through the replica's one listener connection) and retries `pg_try_advisory_lock` every
   `proxy.lock_poll_interval_ms` in case a notification was lost. Holding the lock, it re-reads the
   entry: present → serve; absent (the holder failed) → fetch itself. Waiting is bounded by
   `proxy.fetch_wait_timeout_seconds`, and for expired metadata additionally by the refresh timeout,
   after which the stale entry is served (R-REPO-027).
5. **Commit.** The blob is committed through [storage.md](storage.md); the entry upsert and
   `NOTIFY trove_proxy_fetch, '<lock id>'` run in one short transaction, so the notification is
   delivered only after commit. Then `pg_advisory_unlock` releases the lock and the connection
   returns to the pool. No database transaction is held open while bytes are transferred.
6. **Crash safety.** If the holder dies, PostgreSQL ends its session and releases the lock; waiters
   acquire it at their next poll and fetch.

- The lock is an optimization, not a correctness condition: blob commits are idempotent by content
  address, and the entry upsert keeps the newer `fetched_at` for metadata and refuses a different
  digest for an artifact (R-REPO-041). If the lock pool stays exhausted for 100 ms, the leader MUST
  fetch without the cross-replica lock (counted in a metric) rather than queue.
- The leader's fetch runs detached from its initiating client: a client that disconnects does not
  abort a fetch others wait for. The leader's own client MAY receive the body while it is fetched,
  under the withheld-tail rule of R-REPO-041; followers and waiters are served from the stored
  entry.
- Every response served from a collapsed flight carries `collapsed` in `Cache-Status`.
- The lock pool adds `proxy.lock_pool_size` connections plus one listener connection per replica to
  the database connection budget ([ha-and-helm.md](ha-and-helm.md)).

Configuration:

```yaml
proxy:
  lock_pool_size: 16             # default (proposed), connections per replica
  lock_poll_interval_ms: 1000    # default (proposed)
  fetch_wait_timeout_seconds: 120  # default (proposed)
```

Acceptance criteria (two replicas, counting stub upstream that delays every response by 2 s):

- 100 concurrent requests for the same uncached tarball, 50 per replica: the stub records exactly
  one `GET`; all 100 responses are 200 with identical bytes; at least 99 carry `collapsed` or `hit`.
- The stub streams a 200 MiB artifact slowly and the leader's pod is killed mid-transfer: the other
  replica's waiter acquires the lock within the poll interval plus connection teardown, fetches, and
  completes every waiting request; no entry for a partial body exists at any time.
- With `proxy.lock_pool_size: 0`, all requests still succeed and the stub records at most one `GET` per
  replica.

### R-REPO-027 · Metadata freshness and refresh timeout
Priority: **MVP** · Covers: RMT-018, RMT-019

- A metadata entry is fresh for `metadataTtlSeconds` after `validated_at`; `0` means revalidate on
  every request. The upstream's own `Cache-Control` is ignored for this decision, so behavior follows
  the configuration alone.
- Revalidation MUST be conditional when the entry has validators: `If-None-Match` with the stored
  ETag, else `If-Modified-Since` ([RFC 9110, section 13](https://www.rfc-editor.org/rfc/rfc9110#section-13)).
  A 304 updates `validated_at` and `expires_at` without a body transfer. Because revalidation is a
  header exchange, collapsed across replicas, a period of `0` is allowed instead of the catalog's
  30-second minimum (Q-REPO-2).
- If a refresh has not completed within `metadataRefreshTimeoutSeconds`, the stale entry MUST be
  served (`Cache-Status: trove; fwd=stale; detail=served-stale`); the refresh continues in the
  background, bounded by the read timeout, and updates the entry when it completes. This is the
  stale-if-error behavior of [RFC 5861](https://www.rfc-editor.org/rfc/rfc5861) applied to slow
  upstreams.
- Format defaults: npm and NuGet 7200 seconds; a format page may set another default in the format
  catalog (the NuGet page weighs 300 seconds for version lists).

Acceptance criteria (`metadataTtlSeconds: 60`):

- Two `npm view lodash` within 60 s: one upstream request. After 61 s: one conditional request with
  `If-None-Match`; the stub answers 304; the response carries `Cache-Status: trove; fwd=stale;
  fwd-status=304`.
- The stub delays the document by 90 s: the client receives the stale document after about 60 s;
  once the stub has answered, the next request is a fresh hit with the new content.

### R-REPO-028 · Negative cache
Priority: **MVP** · Covers: RMT-020

- An upstream 404 or 410 for (R, path, variant) MUST be remembered for `notFoundTtlSeconds`; during
  that period requests answer 404 without contacting the upstream. `0` disables negative caching. No
  other status is cached negatively (R-REPO-029). Only requests that passed the cache-fill check
  reach the upstream, so only they create entries.
- Negative entries are ignored once an invalidation covering their path is newer (R-REPO-045) and are
  removed by an invalidation with scope `negative` or `all`, by a change of the upstream identity
  (R-REPO-011) and by expiry.
- Requests refused by patterns or claims never reach the upstream and create no negative entry.
- Expired rows are deleted by a periodic singleton job (advisory-lock lease, INV-12). The table MAY
  be `UNLOGGED`: its content is disposable, and losing it after a crash or failover only costs
  upstream requests.

Data model:

```sql
CREATE TABLE remote_negative_entries (
  repo_id         bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
  path            text COLLATE "C" NOT NULL,
  variant         text NOT NULL DEFAULT '',
  upstream_status smallint NOT NULL CHECK (upstream_status IN (404, 410)),
  created_at      timestamptz NOT NULL DEFAULT now(),
  expires_at      timestamptz NOT NULL,
  PRIMARY KEY (repo_id, path, variant)
);
CREATE INDEX remote_negative_entries_expiry ON remote_negative_entries (expires_at);
```

Acceptance criteria:

- `npm view does-not-exist-4711` twice through `npm-remote`: one upstream request; the second
  response carries `Cache-Status: trove; hit; detail=negative`.
- The stub then publishes the package; within the period it stays invisible; after an invalidation
  with scope `negative` (R-REPO-045) the next request reaches the stub and succeeds.

### R-REPO-029 · Upstream failure handling and the assumed-offline circuit
Priority: **MVP** · Covers: RMT-029, RMT-033

Outcome classification:

| Upstream outcome | Counts toward the circuit | Metadata request | Artifact request |
|---|---|---|---|
| 2xx | resets it | verify, store, serve | verify, store, serve |
| 304 | resets it | refresh validity, serve stored | — |
| 404, 410 | no | negative entry; a stored document is marked `upstream_gone` (below) | negative entry; 404 |
| 401, 403 | no | stale if stored, else 502 `upstream_auth_failed` | 502 `upstream_auth_failed` |
| 429 | yes; `Retry-After` (capped at `circuitOpenSeconds`) opens the circuit at once | stale if stored, else 503 with `Retry-After` | 503 with `Retry-After` |
| 500, 502, 503, 504 | yes | stale if stored, else 502 `upstream_error` | 502 `upstream_error` |
| connection, DNS or TLS handshake failure (not certificate verification) | yes | stale if stored, else 502 `upstream_unreachable` | 502 `upstream_unreachable` |
| connect or read timeout | yes | stale if stored, else 504 `upstream_timeout` | 504 `upstream_timeout` |
| certificate verification, egress refusal, redirect violation, other 4xx, size limit, integrity failure | no | stale if stored, else 502 with the specific code | 502 with the specific code |
| unexpected content type (R-REPO-042) | yes | stale if stored, else 502 | 502 |

- Stale serving is bounded by `cache.maxStaleSeconds` when that is not `0`; beyond it the failure is
  returned.
- One immediate retry after 250 ms MUST be made for a connection failure that occurred before any
  request byte was sent; no other automatic retry happens inside a client request.
- **Upstream gone.** When the upstream answers 404 or 410 for a document that is stored, the entry is
  marked `upstream_gone_at` instead of being deleted, a negative entry is written and the audit
  event `proxy.upstream_gone` is emitted. By default the document then answers 404 and cached
  artifacts stay served. Formats that track removal per version decide more precisely: npm hides
  removed versions and their tarballs unless `serveRemovedVersions` is on
  ([formats/npm.md](formats/npm.md), R-NPM-027). Q-REPO-3 weighs the generic default.
- **Circuit.** Each remote repository has a row in `remote_upstream_state`. A counted failure
  increments `consecutive_failures` atomically (`UPDATE … RETURNING`); at `failureThreshold`,
  `open_until` is set to now plus `circuitOpenSeconds` and `NOTIFY trove_upstream_state` informs
  all replicas, which hold the state in memory and reload it when their listener reconnects. While
  open, no request reaches the upstream (R-REPO-025 step 7). After `open_until`, the first request on
  each replica probes; success closes the circuit for all replicas, failure reopens it. A success
  writes only when the counter is non-zero, so healthy traffic causes no writes.
  `circuitOpenSeconds: 0` disables the circuit.
- Any success resets the counter, so a flaky link cannot keep a repository offline. The catalog's ten
  background retries with growing pauses are replaced by this threshold and probe.

Data model:

```sql
CREATE TABLE remote_upstream_state (
  repo_id              bigint PRIMARY KEY REFERENCES repositories (id) ON DELETE CASCADE,
  consecutive_failures integer NOT NULL DEFAULT 0,
  open_until           timestamptz,
  last_failure_at      timestamptz,
  last_failure         text,
  last_success_at      timestamptz
);
```

Acceptance criteria (`failureThreshold: 5`, `circuitOpenSeconds: 300`):

- The stub answers 503 five times in a row; the sixth request for an expired, stored document is
  served stale without an upstream request, on both replicas; a request for an uncached path
  returns 503 `upstream_assumed_offline` with `Retry-After`; `trove_remote_upstream_up` is 0 on
  both replicas within 2 s.
- After 300 s the next request probes; the stub answers 200; later requests reach the upstream
  normally.
- The stub answers 401: the client receives 502 `upstream_auth_failed`, the circuit stays closed,
  nothing is negatively cached.

### R-REPO-030 · Offline mode per repository
Priority: **MVP** · Covers: RMT-030

- `availability.mode: offline` MUST stop all upstream traffic of the repository. Stored entries are
  served regardless of expiry, expired metadata with `Cache-Status: trove; hit;
  detail=served-stale`; uncached requests answer 404 `not_cached_offline`. A 404 rather than a 5xx
  is deliberate: the operator decided the item is not available here, and clients should not retry.
- In virtual resolution an offline member contributes its cache tier and stored metadata only.
- Offline repositories appear in the inactive view (R-REPO-012).

Acceptance criteria:

- With `npm-remote` offline: `npm install lodash@4.17.21` (cached) succeeds; `npm install
  express@4.21.2` (uncached) fails with `E404`; the stub records no request.

### R-REPO-031 · Global offline switch
Priority: **P1** · Covers: RMT-031

- A system-wide switch MUST put every remote repository into the behavior of R-REPO-030, whatever its
  own mode. It is effective when either the configuration key `proxy.global_offline` is true or the
  runtime switch is on; the configuration key cannot be overridden through the API.
- The runtime switch is a database-backed setting, propagated with `NOTIFY` and effective on all
  replicas within 2 s. It is the kill switch for an incident such as a compromised upstream, which is
  why it does not wait for a configuration rollout.

| Method and path | Body | Response | Permission |
|---|---|---|---|
| `GET /api/v1/system/proxy-offline` | — | `{"enabled": false, "fromConfiguration": false, "reason": "", "changedAt": …, "changedBy": …}` | `settings:read` |
| `PUT /api/v1/system/proxy-offline` | `{"enabled": true, "reason": "upstream incident 2026-10-28"}` | 200; audit `proxy.global_offline_changed` | `settings:write` |

Configuration:

```yaml
proxy:
  global_offline: false  # default (proposed)
```

Acceptance criteria:

- `PUT …/proxy-offline` with `enabled: true`: within 2 s no replica sends upstream requests, cached
  installs still succeed, and `GET` reports `enabled: true` with the reason.
- With `proxy.global_offline: true` in the configuration, a `PUT` with `enabled: false` returns 409
  `offline_by_configuration`.

### R-REPO-032 · Cache view and cached content
Priority: **MVP** · Covers: RMT-003

- `<key>~cache` addresses the cache view of remote repository `<key>` on the protocol and raw-file
  paths (R-REPO-002). It MUST serve stored entries only, never contact the upstream, ignore expiry
  and negative entries, and answer 404 for anything not stored. It needs `read` on the remote
  repository and is the address for clients that must not trigger upstream checks.
- A request to the remote address with `Cache-Control: only-if-cached` MUST be answered from storage
  or with 504, as [RFC 9111, section 5.2.1.7](https://www.rfc-editor.org/rfc/rfc9111#section-5.2.1.7)
  prescribes.
- Cached artifacts are items: they are listed, carry their upstream URL, can be deleted (permanently,
  no trash can), annotated and copied to a local repository through the item operations of
  [storage.md](storage.md) (R-STOR-018 to R-STOR-020), for example to keep a package that
  disappeared upstream.
- Metadata entries are listed for diagnosis by `GET /api/v1/repositories/{key}/cache/entries?pathPrefix=&kind=&pageToken=`
  (path, kind, fetched, validated, expires, upstream gone, last used, upstream URL); permission
  `repositories:read` or `manage` on the repository.

Acceptance criteria:

- `npm view lodash --registry …/api/npm/npm-remote~cache/` returns the stored document even when it
  has expired, and the stub records no request; for an uncached package it fails with `E404`.
- `curl -H 'Cache-Control: only-if-cached' …/files/npm-remote/express/-/express-4.21.2.tgz` for an
  uncached tarball returns 504.

### R-REPO-033 · Upstream authentication and stored secrets
Priority: **MVP** · Covers: RMT-005, RMT-006, IAM-045

- `authMode: basic` MUST send `Authorization: Basic` with `username` and `password`
  ([RFC 7617](https://www.rfc-editor.org/rfc/rfc7617)) preemptively on every request to the upstream
  origin. `bearer` MUST send `Authorization: Bearer <password>`
  ([RFC 6750](https://www.rfc-editor.org/rfc/rfc6750)) and ignore `username`. `none` sends no
  credentials. Token handshakes that a protocol defines itself (container registry token exchange)
  belong to the format plug-in; format-specific rules (which username the public npm registry
  expects) are on the format pages.
- Credentials go only to the upstream origin and to `extraHosts` entries with `sendCredentials:
  true`; redirects follow R-REPO-039. A format page MAY forbid more (npm sends them only to the
  upstream URL's own host).
- **Secret fields** (`password`, sensitive header values, client-certificate private keys, outbound
  proxy passwords) take one of:
  - `{"value": "…"}`: accepted through the API only, never in startup files
    ([R-OPS-029](operations-and-observability.md)); sealed at once;
  - `{"secretRef": {"file": "<name>"}}` or `{"secretRef": {"env": "TROVE_CAC_<NAME>"}}`: resolved
    on every replica at use time under the rules of R-OPS-029 (a file directly under
    `config_as_code.secrets_dir`, only variables with the `TROVE_CAC_` prefix), so trove's own
    secrets can never be routed to an upstream. A changed file takes effect within 60 s.
- Secret values MUST never be returned by any API, log line, audit event or error message. Reads show
  `{"set": true}` for sealed values and the reference itself for references. Clients that re-submit a
  document they read therefore cannot blank or leak a secret.
- **Sealing** (requirements on the stored-secret mechanism of IAM-045, specified in
  [architecture.md](architecture.md) R-ARCH-021, with the chart side in [ha-and-helm.md](ha-and-helm.md)
  R-HA-027; Q-REPO-1):
  AES-256-GCM with a fresh 96-bit random nonce per value; associated data binding the ciphertext to
  table, row and field, so a sealed value copied into another row does not decrypt; a key identifier
  in the sealed form, so keys can be rotated by re-sealing with a new active key while old keys
  still decrypt.
- A secret that cannot be unsealed or resolved makes fetches of that repository fail with 502
  `upstream_secret_unavailable` and is reported at startup; other repositories are unaffected.
- URLs in logs and audit events are written without user info and with query values redacted.

Data model:

```sql
CREATE TABLE repository_secrets (
  repo_id    bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
  field      text NOT NULL,      -- JSON pointer, e.g. '/remote/password', '/remote/headers/X-Api-Key'
  sealed     text,               -- inline value, sealed (R-ARCH-021 format v1:<key id>:<base64>)
  secret_ref jsonb,              -- {"file": …} or {"env": …}
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (repo_id, field),
  CHECK ((sealed IS NULL) <> (secret_ref IS NULL))
);
```

Acceptance criteria:

- A remote with `authMode: basic` in front of a private stub registry: `npm install @partner/sdk`
  succeeds; the stub records `Authorization: Basic …` on every request to its origin; the read shows
  `"password": {"set": true}`.
- A `pg_dump` of the database contains no plaintext of the configured token.
- `{"secretRef": {"env": "TROVE_DATABASE_PASSWORD"}}` is refused with 422 `invalid_secret`.
- With `authMode: bearer`, the stub records `Authorization: Bearer <token>` and no Basic header.

### R-REPO-034 · Client TLS certificates for upstreams
Priority: **P1** · Covers: RMT-007

- The system keeps named client certificates: a PEM bundle with the certificate chain and an
  unencrypted private key (PKCS #8, PKCS #1 or SEC 1). Password-protected keys are refused with 422
  `encrypted_key_unsupported`, a key that does not match the leaf certificate with 422
  `key_mismatch`.
- A remote repository selects one by name; it is presented only on connections made for that
  repository to its upstream origin and its `extraHosts`.
- The private key is a secret field (R-REPO-033) and never returned.
- Deleting a certificate in use answers 409 `certificate_in_use` naming the repositories.
- Expiry is exported as `trove_client_certificate_expiry_timestamp_seconds{name}` so alerts can
  fire before upstream access breaks.

Data model:

```sql
CREATE TABLE client_certificates (
  name               text PRIMARY KEY CHECK (name ~ '^[a-z0-9][a-z0-9._-]{0,63}$'),
  certificate_pem    text NOT NULL,
  private_key        text  NOT NULL,   -- sealed (R-ARCH-021 format)
  subject            text NOT NULL,
  issuer             text NOT NULL,
  not_before         timestamptz NOT NULL,
  not_after          timestamptz NOT NULL,
  sha256_fingerprint text NOT NULL,
  created_at         timestamptz NOT NULL DEFAULT now(),
  created_by         uuid
);
```

API:

| Method and path | Body | Response | Permission |
|---|---|---|---|
| `GET /api/v1/system/client-certificates` | — | 200, name, subject, issuer, validity, fingerprint, referencing repositories | `settings:read` |
| `POST /api/v1/system/client-certificates` | `{"name": "partner-mtls", "pem": {"value": "-----BEGIN CERTIFICATE-----…"}}` | 201; 409 name exists; 422 | `settings:write` |
| `DELETE /api/v1/system/client-certificates/{name}` | — | 204; 404; 409 `certificate_in_use` | `settings:write` |

Acceptance criteria:

- A stub upstream that requires client certificates rejects the remote without one (502
  `upstream_tls_failed`) and serves it once `clientCertificate` names a valid certificate; the
  upstream test reports the presented subject.
- Uploading an encrypted PEM returns 422 `encrypted_key_unsupported`.

### R-REPO-035 · Trusted CA bundles
Priority: **P1** · Covers: RMT-008

- Outbound TLS for remote repositories (upstreams, extra hosts, redirect targets, outbound proxies
  reached over TLS) MUST verify the server certificate against the system roots, if enabled, plus
  every certificate in `proxy.outbound.tls.ca_files`. No per-repository switch disables verification.
- The bundle files are mounted from Kubernetes Secrets or ConfigMaps ([ha-and-helm.md](ha-and-helm.md))
  and MUST be reloaded within 60 s of a change without a restart; new connections use the new pool,
  open connections are not torn down.
- A verification failure answers 502 `upstream_tls_untrusted`, is logged as a security event and does
  not count toward the circuit (R-REPO-029): a certificate that suddenly fails to verify may be an
  interception attempt, not an outage.
- TLS 1.2 is the minimum version.

Configuration:

```yaml
proxy:
  outbound:
    tls:
      use_system_roots: true                        # default (proposed)
      ca_files: []                                  # default (proposed)
      # ca_files: [/etc/trove/ca/corp-root.pem]   # example
      min_version: "1.2"                            # default (proposed)
```

Acceptance criteria:

- A remote to a stub whose certificate comes from a private CA fails with 502
  `upstream_tls_untrusted`; after the CA file is added to the mounted Secret, a request within 60 s
  succeeds without a pod restart.

### R-REPO-036 · Custom upstream request headers
Priority: **P1** · Covers: RMT-009

- `headers` is a list of `{"name", "value", "sensitive"}` sent on every request to the upstream
  origin. Names MUST be RFC 9110 tokens; values MUST NOT contain CR, LF or NUL; at most 32 headers of
  at most 8 KiB each.
- Refused names: `Host`, `Content-Length`, `Transfer-Encoding`, `Connection`, `Upgrade`, `TE`,
  `Trailer`, `Keep-Alive`, `Proxy-Authorization`, `Proxy-Connection`, `Cookie`. `Authorization` is
  accepted only with `authMode: none` and is then always sensitive.
- A sensitive value is a secret field (R-REPO-033).
- Custom headers are treated like credentials: they are not sent to other origins on redirects unless
  `forwardAuthOnRedirect` is on (R-REPO-039).

Acceptance criteria:

- With `{"name": "X-Api-Key", "value": {"value": "k-123"}, "sensitive": true}` the stub records the
  header on every request; the read shows `"value": {"set": true}`; a redirect to a second stub host
  carries no such header.
- `{"name": "Host", …}` returns 422 `invalid_header`.

### R-REPO-037 · Outbound HTTP proxies
Priority: **P1** · Covers: RMT-016, RMT-048, RMT-049

- The system keeps named outbound proxy definitions: proxy URL (`http://` or `https://`, host and
  port), optional username and password (a secret field, sent as `Proxy-Authorization: Basic` to the
  proxy only, never to targets), host patterns that bypass the proxy, and the scopes it is the
  default for: `repositories` (remote repositories) and `system` (every other service that uses the
  system outbound proxy, such as webhooks in [R-OPS-042](operations-and-observability.md) or GitLab
  discovery in [gitlab-ci-oidc.md](gitlab-ci-oidc.md)). At most one definition is the default per
  scope.
- A remote repository's `outboundProxy.mode` is resolved per request: `direct` → no proxy, whatever
  the defaults; `named` → that definition; `default` → the definition that is default for
  `repositories`, else, with `proxy.outbound.proxy_from_environment: true`, the `HTTPS_PROXY`,
  `HTTP_PROXY` and `NO_PROXY` variables ([ProxyFromEnvironment](https://pkg.go.dev/net/http#ProxyFromEnvironment)),
  else direct. Because the default is resolved per request, changing it affects every repository in
  `default` mode at once and never rewrites stored configurations.
- NTLM proxy authentication is not supported; such proxies are used through an authenticating helper
  proxy (CNTLM, for example) configured as a plain HTTP proxy.
- Deleting a definition that a repository names answers 409 `proxy_in_use`.

Data model:

```sql
CREATE TABLE outbound_proxies (
  name           text PRIMARY KEY CHECK (name ~ '^[a-z0-9][a-z0-9._-]{0,63}$'),
  proxy_url      text NOT NULL,
  username       text NOT NULL DEFAULT '',
  password       text,                 -- sealed (R-ARCH-021 format), or NULL with a reference in password_ref
  password_ref   jsonb,
  bypass_hosts   text[] NOT NULL DEFAULT '{}',
  default_scopes text[] NOT NULL DEFAULT '{}',   -- 'repositories', 'system'
  created_at     timestamptz NOT NULL DEFAULT now(),
  created_by     uuid
);
-- one default per scope: enforced in the writing transaction
```

API:

| Method and path | Response | Permission |
|---|---|---|
| `GET /api/v1/system/outbound-proxies` | 200, list with `password: {"set": true}` or the reference, and referencing repositories | `settings:read` |
| `POST /api/v1/system/outbound-proxies` | 201; 409 name exists or scope already has a default; 422 | `settings:write` |
| `PATCH /api/v1/system/outbound-proxies/{name}` | 200 (merge patch) | `settings:write` |
| `DELETE /api/v1/system/outbound-proxies/{name}` | 204; 409 `proxy_in_use` | `settings:write` |

```json
{
  "name": "corp-egress",
  "proxyUrl": "http://egress.corp.example:3128",
  "username": "trove",
  "password": { "secretRef": { "file": "corp-egress-password" } },
  "bypassHosts": ["*.corp.example"],
  "defaultScopes": ["repositories", "system"]
}
```

Configuration:

```yaml
proxy:
  outbound:
    proxy_from_environment: false # default (proposed)
```

Acceptance criteria:

- With `corp-egress` as default for `repositories`, a new remote in `default` mode reaches its
  upstream through the proxy (the proxy log shows a `CONNECT`); a remote in `direct` mode does not.
- Removing the default role from `corp-egress` makes `default`-mode remotes go direct without any
  change to them.

### R-REPO-038 · Egress address rules for remote repositories
Priority: **MVP** · Covers: trove-only

- Every connection made for a remote repository (upstream, extra hosts, redirect targets, URLs taken
  from upstream metadata, outbound proxies) MUST check each resolved IP address inside the dialer's
  `Control` hook, after DNS resolution and before connecting, so a DNS answer that changes between
  check and use cannot pass. Addresses in `allow_cidrs` are permitted even inside a denied range;
  addresses in a denied range are refused; everything else is permitted.
- Denied by default: loopback, unspecified, link-local (IPv4 and IPv6, which includes the
  169.254.169.254 cloud metadata endpoints), the IPv6 metadata address `fd00:ec2::254`, multicast and
  broadcast, and the IPv4-mapped IPv6 forms of all of these (ranges per the IANA
  [IPv4](https://www.iana.org/assignments/iana-ipv4-special-registry/) and
  [IPv6](https://www.iana.org/assignments/iana-ipv6-special-registry/) special-purpose registries).
- Private ranges ([RFC 1918](https://www.rfc-editor.org/rfc/rfc1918)), shared address space and
  unique-local IPv6 are permitted by default, unlike for webhooks
  ([R-OPS-042](operations-and-observability.md)): remote URLs are set by repository administrators,
  and internal registries are the common case. The Helm chart's egress `NetworkPolicy` is the second
  layer ([ha-and-helm.md](ha-and-helm.md)).
- A refused connection answers 502 `egress_denied`, is logged as a security event and does not count
  toward the circuit.
- Through an outbound proxy, trove resolves and checks the target name itself before sending, but
  the proxy connects on its own (H-6).

Configuration:

```yaml
proxy:
  outbound:
    address_policy:
      deny_cidrs:                 # default (proposed): the ranges listed above
        - 127.0.0.0/8
        - ::1/128
        - 0.0.0.0/8
        - ::/128
        - 169.254.0.0/16
        - fe80::/10
        - fd00:ec2::254/128
        - 224.0.0.0/4
        - ff00::/8
        - 255.255.255.255/32
      allow_cidrs: []             # default (proposed)
      # allow_cidrs: [127.0.0.1/32]  # example: a local sidecar registry
```

Acceptance criteria:

- A remote with upstream `http://169.254.169.254/latest/` returns 502 `egress_denied` on the first
  request, and the upstream test reports the same.
- A stub document that points an artifact at `http://127.0.0.1:9000/x.tgz` cannot make the server
  connect there (502 `egress_denied`).
- A host name that resolves to `10.20.0.5` is reachable by default; `::ffff:127.0.0.1` is refused.

### R-REPO-039 · Upstream redirects
Priority: **MVP** · Covers: RMT-012

- Redirects (301, 302, 303, 307, 308; [RFC 9110, section 15.4](https://www.rfc-editor.org/rfc/rfc9110#section-15.4))
  MUST be followed up to `proxy.max_redirects` hops; more answer 502 `too_many_redirects`.
- A redirect from `https` to `http` MUST be refused with 502 `insecure_redirect`.
- Every hop passes the egress rules (R-REPO-038) and TLS verification (R-REPO-035). A format page MAY
  restrict redirect targets further (npm: only hosts it accepts for tarballs).
- Credentials, the client certificate and custom headers MUST be sent only to the upstream origin and
  to `extraHosts` with `sendCredentials: true`. With `forwardAuthOnRedirect: true` they also go to the
  other origins of the redirect chain. The default matters: many registries redirect downloads to
  pre-signed object-storage URLs, which reject an extra `Authorization` header and must not receive
  the credential anyway.
- The entry is stored under the original request key; the final URL is recorded as `upstream_url`.
  Redirect responses themselves are not cached.

Configuration:

```yaml
proxy:
  max_redirects: 5  # default (proposed)
```

Acceptance criteria:

- A stub answers the tarball request with 302 to a second stub host: the tarball is cached and the
  second host records no `Authorization` header; with `forwardAuthOnRedirect: true` it does.
- Six chained redirects return 502 `too_many_redirects`; a redirect to `http://` returns 502
  `insecure_redirect`.

### R-REPO-040 · Size and concurrency limits
Priority: **MVP** · Covers: trove-only

- An artifact larger than the effective `maxArtifactBytes`, or a metadata document larger than
  `maxMetadataBytes`, MUST be refused: at once when `Content-Length` announces it, otherwise by
  aborting when the limit is crossed. Nothing is stored; the client gets 502
  `upstream_item_too_large`; nothing is negatively cached, so raising the limit takes effect at once.
  The repository value applies when set, else the system value.
- At most `limits.maxConcurrentFetches` upstream fetches per repository and
  `proxy.max_concurrent_fetches` per replica are in flight. Further fetches wait up to
  `proxy.queue_timeout_seconds`, then answer 503 with `Retry-After: 5`. Collapsed requests
  (R-REPO-026) take no slot. This bounds the burst a large invalidation can send to the upstream.
- Spool admission and its failure answer are those of [R-STOR-004](storage.md).

Configuration:

```yaml
proxy:
  max_artifact_bytes: 10737418240 # default (proposed), 10 GiB
  max_metadata_bytes: 268435456   # default (proposed), 256 MiB
  max_concurrent_fetches: 256     # default (proposed), per replica
  queue_timeout_seconds: 10       # default (proposed)
```

Acceptance criteria:

- With `limits.maxArtifactBytes: 1048576`, a 2 MiB tarball returns 502 `upstream_item_too_large` and
  leaves no item; after the limit is raised, the same request succeeds at once.
- With `limits.maxConcurrentFetches: 2` and a slow stub, 10 concurrent requests for distinct uncached
  tarballs keep at most 2 upstream connections open per replica.

### R-REPO-041 · Integrity verification against upstream digests
Priority: **MVP** · Covers: RMT-022

- The format plug-in supplies the digests the upstream announces for an artifact (npm: the `dist`
  integrity value, [Subresource Integrity](https://www.w3.org/TR/SRI/) form, else its SHA-1; NuGet:
  per [formats/nuget.md](formats/nuget.md); content-addressed protocols: the digest in the reference).
  Digests are computed while spooling ([R-STOR-002](storage.md)) and compared at the end.

| `integrity.checksumPolicy` | Behavior |
|---|---|
| `verify-if-available` (default) | A mismatch refuses the artifact. Without an announced digest the computed digests are recorded and the artifact is accepted. |
| `require` | Also refuses an artifact without an announced SHA-256 or stronger digest. |
| `ignore` | Announced digests are not compared. |
| `pass-through` | As `ignore`; for formats with separate checksum files, the upstream's checksum files are served unchanged. |

- A format page MAY make its rule stricter than the policy and say so (npm always verifies,
  R-NPM-026). Digests that are part of a protocol's addressing are always verified.
- **Withheld tail.** The leader MAY stream the body to its own client while fetching, but MUST withhold
  the final 64 KiB until verification has passed. On a mismatch it MUST abort the client connection
  without completing the message (HTTP/1.1: close before the announced length or the final chunk;
  HTTP/2: reset the stream), so no client receives a complete response with unverified content
  ([RFC 9112, section 8](https://www.rfc-editor.org/rfc/rfc9112#section-8)). Nothing is stored;
  collapsed followers get 502 `upstream_integrity_mismatch`; a security event is logged; nothing is
  negatively cached.
- **Immutable stays immutable.** When refreshed metadata announces, for an artifact already cached, a
  digest different from the cached one, the server MUST keep the cached artifact, MUST NOT replace it,
  and MUST write the security audit event `proxy.upstream_digest_changed` naming repository, path and
  both digests. Clients then fail their own integrity check loudly instead of silently receiving
  different bytes for the same version; an operator resolves the case by deleting the cached item.
  Package clients assume that one package ID and version always stand for the same content
  ([NuGet implementation guide](https://learn.microsoft.com/en-us/nuget/api/implementation-guide)).

Acceptance criteria:

- The stub serves a tarball whose bytes do not match the document's `integrity` value: `npm install`
  fails, no item exists afterwards, and `trove_remote_integrity_failures_total` increments.
- With `require`, an artifact whose metadata announces only SHA-1 is refused with 502
  `upstream_digest_missing`.
- The stub changes the `integrity` of an already cached version: the cached tarball keeps being served
  unchanged, `npm ci` fails with `EINTEGRITY`, and a `proxy.upstream_digest_changed` event exists.

### R-REPO-042 · Unexpected content types
Priority: **MVP** · Covers: RMT-023

- With `integrity.blockUnexpectedContentTypes: true`, a response whose media type is in
  `proxy.blocked_content_types` MUST be refused with 502 `upstream_content_type_blocked` and not stored,
  unless the format plug-in declares that type expected for the request (HTML index pages of the PyPI
  simple API, for example) or the repository lists it in `integrity.allowedContentTypes`.
- The refusal counts toward the circuit (R-REPO-029): an HTML page in place of a package is nearly
  always a captive portal, a login page or an intermediary's error page, which is an availability
  problem.

Configuration:

```yaml
proxy:
  blocked_content_types: # default (proposed)
    - text/html
    - application/xhtml+xml
```

Acceptance criteria:

- The stub answers a tarball request with 200 and `Content-Type: text/html`: the client receives 502
  `upstream_content_type_blocked`, no item is stored, and the failure counter increments.

### R-REPO-043 · Metadata URL rewriting
Priority: **MVP** · Covers: trove-only

- In every metadata document served, the format plug-in MUST rewrite the URLs of resources clients
  fetch next (npm tarball URLs; NuGet resource, registration and package content URLs) so they point
  to the address the document was requested through: the remote repository, its cache view, or the
  virtual repository. Other URLs (home pages, source links) stay unchanged. Artifacts are never
  modified.
- Rewriting MUST happen when a document is served, not when it is stored, using the base URL of
  [operations-and-observability.md](operations-and-observability.md) (including its additional base
  URLs and trusted proxies). The stored upstream document stays unchanged, so a base URL change needs
  no cache flush and one host name's URLs never leak into another's responses.
- A rewritten path maps back to its upstream URL as follows: if the upstream URL equals the one the
  format derives from `url` and the canonical path, nothing is recorded; otherwise the mapping is
  recorded in `remote_url_map` when the document is parsed. The server MUST NOT derive an upstream
  host from a client request path: no client can choose the host trove fetches from.
- A document MUST record mappings only for canonical paths of its own package. The format builds
  the path from the package identity the document was fetched for (name or ID, version, file name),
  never from the path of the upstream URL, and ignores and counts an entry that names another
  package. A document of package A can therefore neither create nor replace the mapping of an
  artifact of package B.
- A recorded URL on a host that is neither the upstream origin nor in `extraHosts` MUST be refused
  with 502 `foreign_host_denied` without a request. (npm's `tarballHosts` setting is the npm-specific
  form of `extraHosts`; Q-REPO-5.)
- The ETag of a rewritten document is derived from the stored document's digest and the serving
  address, so client conditional requests work.

Data model:

```sql
CREATE TABLE remote_url_map (
  repo_id      bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
  path         text COLLATE "C" NOT NULL,
  upstream_url text NOT NULL,
  recorded_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (repo_id, path)
);
```

Acceptance criteria:

- `npm view lodash dist.tarball --registry https://trove.example.com/api/npm/npm-all/` prints
  `https://trove.example.com/api/npm/npm-all/lodash/-/lodash-4.17.21.tgz`.
- A request with `Host: evil.example` returns a document whose tarball URLs still name
  `trove.example.com`, and the next client sees the same.
- A stub document whose tarball lives on `cdn.partner.example` fails with 502 `foreign_host_denied`
  until `extraHosts` contains `cdn.partner.example`; afterwards the tarball is fetched from there and
  served under the trove path.
- A stub document for package `evil` whose version entry names a tarball URL of `lodash` records a
  mapping only for `evil`'s own canonical path; `remote_url_map` holds no row for any `lodash`
  path afterwards.

### R-REPO-044 · Pass-through mode
Priority: **P1** · Covers: RMT-017

- With `cache.storeArtifacts: false`, artifacts MUST be streamed from the upstream to the client
  without being stored; verification and the withheld tail (R-REPO-041) still apply. Metadata is
  cached as usual, because rewriting and freshness depend on it. Negative caching, the circuit and
  offline modes apply; uncached artifacts are unavailable while the upstream cannot be asked.
- Artifact requests are not collapsed across clients (nothing is shared afterwards), and no items are
  created, so browsing, properties, copy and cleanup do not apply to them.
- A format whose plug-in requires stored artifacts refuses the setting with 422 `not_applicable`.
- Use case: a replica group next to another trove instance that already caches the upstream.

Acceptance criteria:

- With pass-through on, two installs of the same tarball cause two upstream artifact requests and
  leave no item; the package document is fetched once within its period.

### R-REPO-045 · Cache invalidation
Priority: **MVP** · Covers: RMT-026, ART-050

The operation surface is [R-OPS-021](operations-and-observability.md): `POST
/api/v1/repositories/{key}/cache/invalidate` with `{"path": "<prefix>", "scope": "metadata" |
"negative" | "all"}`, answer `200 {"invalidated": n}`, permission `manage` on the repository
([R-AUTHZ-008](authorization.md)), audit `repository.cache_invalidated`. It is needed in the MVP,
because RMT-026 is. This requirement specifies its semantics and mechanism.

- Invalidation MUST make the covered metadata entries (scope `metadata`), the covered negative entries
  (`negative`) or both (`all`) count as expired, in constant time regardless of cache size. Artifacts
  are never invalidated, and nothing is deleted.
- A prefix covers a canonical path when it is empty, equal to the path, or equal to its leading
  segments: `@acme` covers `@acme/util` but not `@acmecorp/util`.
- **Mechanism.** An invalidation is a row (repository, prefix, scope, time). A metadata entry is
  invalid when an invalidation of its repository with a covering prefix and a matching scope is newer
  than its `validated_at` (for negative entries: than their `created_at`). Each replica keeps the
  invalidations in memory, loaded at start and updated through `NOTIFY trove_cache_invalidation`, so
  the effect is immediate on all replicas. A background job later folds each invalidation into the
  entries in batches (setting `expires_at` to the invalidation time, deleting covered negative
  entries) and then deletes the row. Invalidating a million entries is therefore one insert, not a
  long `UPDATE`. The `invalidated` count is the number of covered entries at the time of the call.
- Each entry is refreshed on its next request under R-REPO-027. A failed refresh serves the stale
  copy, which can show an old package list; the UI therefore shows the circuit state next to the
  result and tells the operator to verify the refresh.
- On a virtual repository, the merged entries under the prefix are dropped (R-REPO-020); on a local
  repository the call answers 400.

Data model:

```sql
CREATE TABLE remote_cache_invalidations (
  id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  repo_id    bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
  prefix     text COLLATE "C" NOT NULL,   -- '' = whole repository
  scope      text NOT NULL CHECK (scope IN ('metadata', 'negative', 'all')),
  created_at timestamptz NOT NULL DEFAULT now(),
  created_by uuid
);
CREATE INDEX remote_cache_invalidations_repo ON remote_cache_invalidations (repo_id);
```

Acceptance criteria:

- With 1,000,000 cached metadata entries, an invalidation with an empty path returns within one second,
  and the next `npm view` of any cached package causes a conditional upstream request on every
  replica.
- Invalidating `@acme` refreshes `@acme/util` on its next request but not `@acmecorp/util`.
- With the stub down, a request after invalidation returns the stale document with
  `detail=served-stale`.

### R-REPO-046 · Unused cache cleanup setting
Priority: **P1** · Covers: RMT-021

- `remote.cache.unusedRetentionHours` (`0`: keep forever) is the per-repository setting that the
  remote-cache cleanup job of [R-STOR-045](storage.md) applies. That job, its schedule and its manual
  start belong to [storage.md](storage.md); removal is permanent (no trash can), since the upstream
  can supply the content again.
- Use of a cached artifact is its item's last download, or its creation if it was never downloaded
  ([R-STOR-015](storage.md)). Downloads through a virtual repository and through the cache view MUST
  count as downloads of the member's item.
- The same job MUST remove metadata entries whose `last_used_at` is older than the period. Replicas
  record metadata use in memory and write `last_used_at` in batches at most once per minute, and only
  when the stored value is more than one hour older, so serving documents causes no per-request
  writes.
- To keep an item permanently, copy it to a local repository.

Acceptance criteria:

- With `unusedRetentionHours: 1`, a tarball last downloaded two hours ago through `npm-all` is
  removed by a cleanup run and fetched from the upstream again on the next install; a tarball
  installed ten minutes ago stays.

## Observability

### R-REPO-047 · Proxy and resolution observability
Priority: **MVP** · Covers: trove-only

- Every response of a remote or virtual repository MUST carry a `Cache-Status` header
  ([RFC 9211](https://www.rfc-editor.org/rfc/rfc9211)) with the cache name `trove`:

| Situation | Header value |
|---|---|
| Fresh hit | `trove; hit; ttl=<seconds left>` |
| Miss, fetched and stored | `trove; fwd=uri-miss; fwd-status=200; stored` |
| Expired, revalidated | `trove; fwd=stale; fwd-status=304` (or `fwd-status=200; stored`) |
| Stale served after a failed or slow refresh | `trove; fwd=stale; fwd-status=<status, if any>; detail=served-stale` |
| Stale served without asking (offline, circuit open, no cache-fill right) | `trove; hit; detail=served-stale` |
| Negative entry | `trove; hit; detail=negative` |
| Collapsed onto another request's fetch | additional parameter `collapsed` |
| Partial merge (R-REPO-020) | `trove; detail=partial` |

- Metrics: the remote-repository metrics of [operations-and-observability.md](operations-and-observability.md)
  apply as defined there (`trove_remote_cache_lookups_total`, `trove_remote_upstream_requests_total`,
  `trove_remote_upstream_request_duration_seconds`, `trove_remote_upstream_up`, which is 0 while
  the circuit of R-REPO-029 is open, and `trove_remote_collapsed_requests_total`). This page adds,
  with the same label conventions:

| Metric | Type | Labels |
|---|---|---|
| `trove_remote_lock_fallbacks_total` | counter | — (R-REPO-026: fetch without the cross-replica lock) |
| `trove_remote_fetch_queue_wait_seconds` | histogram | `repo` |
| `trove_remote_integrity_failures_total` | counter | `repo`, `reason` (`mismatch`, `missing`, `digest_changed`) |
| `trove_remote_egress_denied_total` | counter | `repo` |
| `trove_virtual_resolutions_total` | counter | `repo`, `source` (`local`, `cache`, `upstream`, `none`) |
| `trove_client_certificate_expiry_timestamp_seconds` | gauge | `name` |

- Every upstream request writes one record of the `upstream` log
  ([R-OPS-011](operations-and-observability.md)); this page adds the fields `flight_role`
  (`leader`, `follower`, `waiter`) and `cache_status`.
- Audit events (INV-16): `repository.created`, `repository.updated`, `repository.deleted`,
  `repository.cache_invalidated`, `repository.claims_changed`, `proxy.global_offline_changed`,
  `proxy.client_certificate_changed`, `proxy.outbound_proxy_changed`. Security events:
  `proxy.integrity_mismatch`, `proxy.upstream_digest_changed`, `proxy.tls_untrusted`,
  `proxy.egress_denied`, `proxy.upstream_gone`.

Acceptance criteria:

- `curl -sI …/api/npm/npm-remote/lodash` shows a `Cache-Status` header of the documented form on the
  first and on the second request.
- After a stub outage opens the circuit, `trove_remote_upstream_up{repo="npm-remote"}` is 0 on
  every replica within 2 s.

## Deferred

<details>
<summary>Deferred catalog entries (39)</summary>

| ID | Priority | One line |
|---|---|---|
| REPO-014 | P2 | Sharing a repository with other projects. |
| REPO-015 | P2 | A remote repository connected to a shared repository of another project. |
| REPO-019 | P2 | Per-repository download redirect; the mechanism is R-STOR-014 in [storage.md](storage.md). |
| REPO-021 | Later | Indexing repositories for a vulnerability and license scanner. |
| REPO-032 | P2 | Local settings of further formats; each arrives with its format page. |
| REPO-039 | P2 | Virtual repositories fetching remote content on behalf of other instances. |
| REPO-042 | P2 | Signing keys on virtual repositories. |
| REPO-044 | P2 | Virtual settings of further formats; each arrives with its format page. |
| REPO-048 | P2 | Layout path patterns with tokens and custom tokens. |
| REPO-049 | P2 | Integration-revision expressions of layouts. |
| REPO-050 | P2 | Layout management (create, duplicate, test). |
| REPO-051 | P2 | Different layouts per class and path translation between them. |
| REPO-052 | P2 | Layout-based maintenance (snapshot cleanup, path conversion). |
| REPO-060 | P2 | Existence check for repositories of given classes. |
| REPO-065 | P2 | Deprecated shared-configuration read of a remote repository. |
| REPO-066 | Later | Immutable release-set storage repositories. |
| REPO-067 | P2 | Guided NuGet normalization migration of existing repositories. |
| REPO-068 | P2 | A system-wide repository search order outside virtual repositories. |
| RMT-010 | P2 | Static query parameters on every upstream request. |
| RMT-011 | P2 | Forwarding client query parameters to the upstream. |
| RMT-013 | P2 | A cookie store per remote repository. |
| RMT-014 | P2 | Turning off URL normalization of upstream requests. |
| RMT-024 | P2 | Skipping `HEAD` requests to the upstream. |
| RMT-025 | P2 | Eager background fetch of companion artifacts (Maven family). |
| RMT-027 | P2 | Deprecated cache pre-population operations. |
| RMT-028 | P2 | Download redirect for cached artifacts (with REPO-019). |
| RMT-032 | P2 | The unspecified hard-fail switch; R-REPO-029 makes failure behavior explicit instead. |
| RMT-035 | P2 | A cache layout that differs from the upstream layout. |
| RMT-038 | P2 | Listing upstream folders that are not cached. |
| RMT-039 | P2 | Sharing a remote configuration with all authenticated users. |
| RMT-040 | P2 | Chained remote repositories pointing at another trove instance, with extra exchanges. |
| RMT-041 | P2 | Reporting downloads back to a chained source. |
| RMT-042 | P2 | Synchronizing properties from a chained source. |
| RMT-043 | P2 | Detecting deletion at a chained source. |
| RMT-044 | P2 | Uncached retrieval for chained requests through virtual repositories. |
| RMT-045 | P2 | Searching through a chained remote. |
| RMT-046 | P2 | Parallel ranged download of large files from a chained source. |
| RMT-047 | P2 | Project-scoped remote repositories. |
| ART-044 | P2 | Browsing uncached remote content (with RMT-038). |

</details>

Out of scope: REPO-020 (download redirect through a CDN operated as part of a hosted service).

## Handed over

MVP and P1 entries of the covered catalog pages that this page does not specify:

| IDs | Owner |
|---|---|
| ART-003, ART-021 to ART-026, ART-032 to ART-034, ART-036 to ART-038 (properties, property sets, copy, move, delete, trash can) | [storage.md](storage.md), R-STOR-009 and R-STOR-018 to R-STOR-023 |
| IAM-023, IAM-024 (cache fills, access through virtual repositories) | [authorization.md](authorization.md), R-AUTHZ-008 and R-AUTHZ-009 |
| REPO-013, REPO-045 (projects; environments are P2) | [authorization.md](authorization.md), R-AUTHZ-026 and R-AUTHZ-009 |
| REPO-029, REPO-040 | [formats/npm.md](formats/npm.md) |
| REPO-030, REPO-031 | [formats/nuget.md](formats/nuget.md) |
| REPO-022, REPO-023, REPO-024, REPO-026, REPO-041, RMT-036 | the Maven-family format page, not yet in the page table |
| REPO-027 (container API version, legacy manifests), REPO-043 | the container format page, not yet in the page table; the tag caps of REPO-027 are R-REPO-007 |
| REPO-003 | the generic format page, not yet in the page table |
| UI part of REPO-007 | [api-and-ui.md](api-and-ui.md) |

## Deliberate deviations from the catalog

| Catalog behavior | trove | Why |
|---|---|---|
| Create and update answer plain text; duplicates and validation errors answer 400 | JSON; 409 for an existing key; 422 listing every violation (R-REPO-008, R-REPO-009) | Machine-readable, one round trip to fix a document |
| Configuration reads return every setting the system knows | Only members of the class and format (R-REPO-010) | No dead fields to misread |
| Unknown members in configuration documents tolerated | Refused with 400 (R-REPO-008) | A misspelt setting must not be silently ignored |
| Cache view through a reserved key suffix; shorter remote keys | `<key>~cache`; no reserved suffix, 64 characters for every class (R-REPO-002, R-REPO-032) | `~` cannot occur in keys |
| Patterns do not apply to administrators | They apply to every package-protocol request (R-REPO-005) | Routing safety must not depend on who runs the build |
| Patterns are paths for every format | Name patterns for npm and NuGet (R-REPO-005) | One pattern covers documents and files |
| Metadata period minimum 30 s | `0` allowed (R-REPO-027, Q-REPO-2) | Conditional, collapsed revalidation is cheap |
| Ten background retries before a repository counts as offline | Consecutive-failure threshold and probe (R-REPO-029) | Deterministic, shared across replicas |
| Repository delete answers with a report (200, 207) | Asynchronous job, 202 (R-REPO-013) | Deleting a large repository outlives an HTTP request |
| Non-package content accepted into format repositories | Refused for npm and NuGet (R-REPO-001) | Unindexed content is invisible to clients |
| NTLM proxies; proxy credentials reused for redirect target hosts | Basic proxy authentication only; proxy credentials go to the proxy only (R-REPO-037) | Smaller credential exposure |

## Security notes

- **Dependency confusion** is closed on the server by namespace claims and pin rules (R-REPO-023),
  evaluated on stored configuration, never on the caller's permissions, and enforced for remote
  repositories addressed directly as well for `global` claims. R-AUTHZ-009 of
  [authorization.md](authorization.md) relies on it for virtual repositories that a caller can
  read only in part. Auto claims stay within the virtual repositories that contain the claiming
  repository, so one publish into a sandbox cannot take a public name away from everyone
  (H-9, H-10).
- **Upstream paths are re-encoded.** The once-decoded canonical path is percent-encoded again
  before it reaches an upstream, and documents map only their own package's paths (R-REPO-025,
  R-REPO-043).
- **Cache fills** follow [R-AUTHZ-008](authorization.md). With the default `cacheFillAction: read`
  and anonymous read on a remote repository, anyone can make trove download arbitrary upstream
  packages; the concurrency limits (R-REPO-040) bound the rate, not the volume.
- **Merged metadata never crosses permission boundaries:** its cache key contains the fingerprint of
  the members the caller can read (R-REPO-020).
- **No client-chosen upstream hosts.** Upstream URLs come from configuration or from recorded upstream
  metadata, never from request paths (R-REPO-043); every connection passes the egress rules at dial
  time (R-REPO-038); redirects cannot downgrade to plain HTTP and carry no credentials to other
  origins by default (R-REPO-039).
- **Secrets** are write-only and either sealed or referenced; references cannot reach trove's own
  secrets (R-REPO-033). Logs carry redacted URLs.
- **Integrity:** announced digests are verified before a client can receive a complete response, and
  a changed digest for a cached version raises a security event instead of replacing content
  (R-REPO-041). TLS verification cannot be switched off per repository (R-REPO-035).
- **Cache poisoning through `Host`:** rewriting uses the configured base URLs at serve time
  (R-REPO-043).
- **Kill switch:** the global offline switch stops all upstream traffic within seconds (R-REPO-031).
- **Explanations do not leak member keys** to callers who cannot read those members (R-REPO-019).

## Open questions

### Q-REPO-1 · Who specifies the stored-secret mechanism (IAM-045)?

- (a) This page, beyond the requirements already in R-REPO-033.
- (b) [architecture.md](architecture.md) specifies the key, its rotation and the sealing format;
  repository secrets, webhook secrets and every other stored secret use it.
- (c) References only (`secretRef`), no sealed values in PostgreSQL.

Recommendation: (b), with R-REPO-033's sealing rules as input. Operations already assumes the key
lives in architecture.md, webhooks need the same mechanism, and one key means one rotation and one
backup procedure. (c) would make credentials entered in the UI impossible without file access on
every replica.

### Q-REPO-2 · Lower bound of the metadata period

- (a) Allow `0` (revalidate every request), as written in R-REPO-027.
- (b) A minimum of 30 s as in the catalog, accepting smaller values in imported configurations.

Recommendation: (a). Conditional revalidation is a header exchange, collapsed across replicas and
bounded by the per-repository concurrency limit; a minimum protects the upstream from nothing those
mechanisms do not already prevent, and it denies teams that need new upstream versions at once.

### Q-REPO-3 · Generic default when the upstream removes a cached document

- (a) Mark it `upstream_gone`, answer 404 for the document, keep serving cached artifacts; formats
  with version tracking hide removed versions (as written in R-REPO-029; npm does, R-NPM-026).
- (b) Also refuse cached artifacts of that package in every format.
- (c) Keep serving the stale document.

Recommendation: (a). Removals are often takedowns of malicious packages, so (c) would keep
advertising them. (b) cannot be applied without knowing which artifacts belong to which version,
which only the format knows; formats that know it (npm) already go further. Builds that need a
removed package keep it on purpose by copying it into a local repository.

### Q-REPO-4 · Scope of remote refusal for claimed names

- (a) System-wide: every remote repository of the format refuses claimed names, also when addressed
  directly (as written, `proxy.claims.remote_refusal: true`).
- (b) Only inside virtual repositories that contain a claiming member.

Recommendation: (a). Clients that point at a remote repository directly (a forgotten `.npmrc`, a CI
image with a hard-coded source) are exactly where an internal name leaks to the public registry. The
switch exists for installations that proxy a partner registry owning a name trove also claims;
pin rules cover that case without turning the switch off. (a) applies to `global` claims only
(R-REPO-023): system-wide refusal is an administrative decision, never the side effect of a publish.

### Q-REPO-5 · Generic or format-level placement of pins, extra hosts and upstream authentication

- (a) Generic members on this page (`virtual.pinning`, `remote.extraHosts`, `remote.authMode`,
  `remote.password`), with format pages adding only format semantics.
- (b) Format-level members as written on the npm page (`npm.pinned`, `npm.remote.tarballHosts`,
  `npm.upstreamAuth` with `secret://` references).

Recommendation: (a). The mechanisms are the same for every format (BEYOND-028 names claims as a
cross-format mechanism, and NuGet expects the generic one), configuration-as-code documents stay
uniform, and one `secretRef` form ([R-OPS-029](operations-and-observability.md)) avoids a second
reference syntax. The npm page's member names are to be aligned in review.

## What this does not cover

### H-4 · Malicious content from a legitimate upstream

Verification proves only that the bytes match what the upstream announced. A malicious package
published to a public registry is fetched, cached and served like any other: there is no scanning, no
vetting of new packages and no minimum-age rule (the catalog's scanning area is Later). Claims and pin
rules stop name confusion, not malicious public packages. `cacheFillAction: deploy` narrows who can
bring new packages in.

### H-5 · Upstream removals reach cached artifacts only where the format tracks versions

When a registry removes a package, R-REPO-029 stops serving the cached document. Cached artifacts
stay and are served to clients that request them by exact path, which is what lockfiles do, unless
the format tracks removal per version (npm does, R-NPM-026; NuGet does not yet). An operator has to
find and delete such items by hand.

### H-6 · Remote repositories reach private networks

The egress rules permit private ranges by default, because internal registries are common. A holder
of `repositories:write` can point a remote repository or the upstream test at an internal HTTP
service and read its responses through the cache. With an outbound proxy, trove checks the target
name's resolution, but the proxy connects on its own. The mitigations are the egress
`NetworkPolicy` and narrowed `deny_cidrs`; there is no per-repository egress scope.

### H-7 · Claimed names are observable

Claims and pin rules ignore the caller's permissions by design (R-REPO-023). A caller with `read` on
a virtual repository can learn that an internal name or prefix is claimed by comparing a 404 with the
public package of the same name. Names leak; contents do not.

### H-8 · The stored-secret key sits on every replica

Every replica holds the key that unseals upstream credentials, client-certificate keys and proxy
passwords, because every replica makes upstream requests. Exec access to a trove pod, or read
access to the key's Secret, yields all of them. Integration with an external key management service
is not specified.

### H-9 · Member-scoped claims do not protect other virtual repositories

Component: namespace claims (R-REPO-023). Subject: a virtual repository V2 of the same format that
does not contain the local repository holding a name, and remote repositories addressed directly.
Live with the MVP. Auto claims have scope `members`, so a name published only to `npm-team-x` is
protected in every virtual repository that contains `npm-team-x`, but V2 still resolves the name
from its remote members, and with it a public package of the same name. Clients configured against
V2, or against a remote repository directly, receive that package. What makes it real: several
virtual repositories with different local members, and internal names that nobody turned into a
`global` claim. The warning `dependency_confusion_unprotected` covers only virtual repositories
whose local members have no claims at all.

### H-10 · Deploy on a local member shadows public packages for every consumer

Component: virtual resolution (R-REPO-019, tier order) together with auto claims (R-REPO-023).
Subject: any principal holding `deploy` without a package-name pattern on a local member of a widely
used virtual repository. Live with the MVP. Local members form the first resolution tier, and the
first publish of a name claims it for the members of every virtual repository that contains the
repository. Publishing `lodash@4.17.22` there therefore replaces the public `lodash` for every
client of those virtual repositories, and the claim also hides the versions that exist only
upstream. Package ownership (R-AUTHZ-018) does not stop this, because the name has no owner in that
repository. What makes it real: broad `deploy` grants (`**`, or no `packages.include`) on a local
repository that a shared virtual repository contains. Name patterns on deploy grants and `global`
claims for internal prefixes are the mitigations. No rule refuses a local publish of a name that a
remote member already serves.
