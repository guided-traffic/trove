# Repositories and pull-through proxy

Status: specification — not implemented

This page specifies trove's repository model and the management API around it: the configuration
document of a repository, keys and addresses, validation, create, read, update, list and delete. It
specifies how virtual repositories resolve requests, including protection against dependency
confusion through pin rules, namespace claims and a shadowing check on first publishes. And it
specifies the pull-through proxy of remote repositories end to end: settings and defaults, cache
state in PostgreSQL, collapsing of concurrent misses within a replica and across replicas through a
Valkey lock, metadata freshness, negative caching in Valkey, upstream failure handling and offline
modes, upstream authentication and stored secrets, client certificates, outbound HTTP proxies and
CA bundles, egress address rules, redirects, size limits, integrity verification, metadata URL
rewriting, cache invalidation and the settings that drive cache cleanup. It states how all of this
behaves on n interchangeable replicas behind a Service without session affinity, while Valkey is
unavailable, and while a replica drains (R-REPO-049). Bytes, items and the operations on stored
items (delete, copy, move, properties, trash can) are specified in [storage.md](storage.md); who
may do what in [authorization.md](authorization.md); format-specific metadata in
[formats/npm.md](formats/npm.md) and [formats/nuget.md](formats/nuget.md).

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
| Format plug-in interface in Go, job framework, change areas and signals (R-ARCH-018), the Valkey usage map and client (R-ARCH-030, R-ARCH-031), `cluster.FlightLock` (R-ARCH-033), per-replica state rules (R-ARCH-032), process shutdown (R-ARCH-024), the stored-secret key | [architecture.md](architecture.md) |
| npm and NuGet: document classes, metadata merge, URL rewrite details, digest sources, version-removal tracking, format settings | [formats/npm.md](formats/npm.md), [formats/nuget.md](formats/nuget.md) |
| Public base URL and trusted proxies, metrics catalog, upstream log record, audit storage, cache-invalidation endpoint surface, configuration as code and `secretRef`, read-only maintenance mode | [operations-and-observability.md](operations-and-observability.md) |
| API conventions (error envelope, paging, job resource), UI | [api-and-ui.md](api-and-ui.md) |
| Helm values for CA bundles and secret mounts, egress `NetworkPolicy`, database connection budget, Valkey topology and degraded mode (R-HA-056, R-HA-057), termination grace period (R-HA-011), node drain (R-HA-059) | [ha-and-helm.md](ha-and-helm.md) |
| Export classes of this page's tables, leaving remote caches out of a backup, raising revision columns on restore | [backup-and-restore.md](backup-and-restore.md) (R-BAK-004, R-BAK-008, R-BAK-022) |

## Terms

| Term | Meaning on this page |
|---|---|
| Canonical path | The repository-relative path a format plug-in assigns to a request or stored file: `/`-separated, no leading slash, no empty, `.` or `..` segment, percent-decoded exactly once. npm example: document `@acme/util`, tarball `@acme/util/-/util-1.4.0.tgz`. |
| Request kind | `artifact` (immutable binary), `metadata` (expiring document), `search`, `write` or `other`, assigned by the format plug-in (R-REPO-003). |
| Package name | The format's package identifier, normalized by the format plug-in (npm: as published, scoped names as `@scope/name`; NuGet: the ASCII-lowercased ID for IDs entering local repositories (ASCII only); for cached upstream packages with legacy non-ASCII IDs, the ID segment of the client's request path ([formats/nuget.md](formats/nuget.md) R-NUGET-003)). |
| Routing name | A name the format plug-in derives for an artifact that belongs to no package, so that patterns, claims and pin rules can protect it (NuGet symbol files: the first segment of the symbol key without its last extension, lowercased). It is not a package name for anything else, authorization included (R-REPO-003). |
| Name pattern | A name glob of [R-AUTHZ-005](authorization.md), evaluated by the same matcher against the package name (or routing name) with the format's comparison rule there (npm case-sensitive, NuGet case-insensitive): `*` any run of characters except `/`, `**` any run including `/`, `?` one character except `/`, `\` makes the next character literal. An exact name and a prefix ending in `*` (`@acme/*`, `acme-*`, `Acme.*`), the syntax of NuGet's [package source mapping](https://learn.microsoft.com/en-us/nuget/consume-packages/package-source-mapping), are a subset of it and mean the same here, in permissions and in retention. Because `*` does not cross `/`, a bare `*` on npm matches every unscoped name and no scoped one; `@*/*` matches every scoped name and `**` every name. NuGet IDs contain no `/`, so `*` and `**` both match every ID. |
| Most specific wins | The routing precedence among claim and pin-rule patterns that match the same name (R-REPO-023); include and exclude lists need none (R-REPO-005). An exact name beats any pattern with a wildcard; a longer literal part beats a shorter one; with equal literal parts, a trailing `*` beats a trailing `**`. Claim and pin-rule patterns are therefore limited to the forms for which this order is total: an exact name, or a literal (possibly empty) followed by one trailing `*` or `**` (R-REPO-023). |
| Effective member list | A virtual repository's members after nested virtual repositories are expanded and duplicates removed (R-REPO-018). |
| Cache entry | What a remote repository stored for one canonical path and variant: an artifact, which is also an item of the repository ([storage.md](storage.md)), or a metadata document. |
| Fresh, expired, stale | A metadata entry is fresh until its expiry, expired afterwards; an expired entry served because the upstream was not or could not be asked is stale. Artifacts never expire. |
| Negative entry | A remembered upstream 404 or 410 for one canonical path and variant of a remote repository: a key in Valkey with a bounded copy in each replica's memory (R-REPO-028). It can make trove answer 404 and nothing else. |
| Flight | The fetch of one cache entry from the upstream. Concurrent requests for the same (repository, canonical path, variant) share one flight, within a replica and across replicas (R-REPO-026). The replica that fetches is the flight's holder; requests that wait for it are followers (same replica) or waiters (other replicas). |
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
  update that changes any of them MUST be refused with 422 `validation_failed` with `immutable` in `errors[]`; there is no class
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
  ADD COLUMN config_version   bigint NOT NULL DEFAULT 1,     -- ETag, R-REPO-011; R-BAK-022
  ADD COLUMN claims_version   bigint NOT NULL DEFAULT 1,     -- claims ETag, R-REPO-023; R-BAK-022
  ADD COLUMN created_by       uuid,                          -- principal id
  ADD COLUMN updated_by       uuid,
  ADD COLUMN project_id       uuid REFERENCES project (id),  -- assignment: R-AUTHZ-026
  ADD COLUMN last_activity_at timestamptz,                   -- R-REPO-012
  ADD CONSTRAINT repositories_key_dots CHECK (key !~ '\.\.' AND key !~ '\.$');
```

The M1 migration adds `lifecycle`, `config_version`, `created_by`, `updated_by`, `last_activity_at` and the key
constraint; `claims_version` and `project_id` come with the migrations of their requirements (Q-REPO-11).

`config` never contains secret values; they are in `repository_secrets` (R-REPO-033). References
that need referential integrity (members, deploy targets, pin targets, client certificates,
outbound proxies) are relational tables on this page.

`config_version` and `claims_version` are client-visible ETags, so both MUST be registered with
`meta.RegisterRevisionColumn`, and a restore raises them above every value the old installation
can have issued ([backup-and-restore.md](backup-and-restore.md) R-BAK-022). Otherwise a client
holding the ETag of a version written after the snapshot could pass `If-Match` against different
content after the restore, and a claims `PUT` could replace a claim set, `global` claims
included, that its author never saw (R-REPO-023).

Acceptance criteria:

- `POST /api/v1/repositories` with `{"key":"npm-local","class":"local","format":"npm"}` returns
  201; a following `PATCH` with `{"format":"nuget"}` returns 422 `validation_failed` with `immutable` in `errors[]` at `/format`.
- A create with `"class":"federated"` returns 422 `validation_failed` with `unsupported_class` in `errors[]`.
- `curl -X PUT --data-binary @notes.txt -H "Authorization: Bearer $TROVE_TOKEN"
  https://trove.example.com/files/npm-local/notes.txt` returns 422 `not_a_package`.

### R-REPO-002 · Repository keys and client addresses
Priority: **MVP** · Covers: REPO-004, REPO-005

- A key MUST match `^[a-z0-9][a-z0-9._-]{1,63}$` (INV-5: 2 to 64 characters, lower case, so it is
  valid for container formats too), MUST NOT contain `..` and MUST NOT end in `.` (trailing dots
  are stripped by some reverse proxies and Windows clients). Keys are unique across all classes.
  Violations answer 422 `validation_failed` with `invalid_key` in `errors[]`.
- No suffix is reserved. The cache view of a remote repository is addressed with `~cache`
  (R-REPO-032); `~` cannot occur in a key, so the cache address never collides with a key and remote
  keys keep the full 64 characters.
- Reserved names: `batch-create`, `batch-update`, `batch-get`, `batch-delete` and `quick-setup`
  are literal sub-paths of `/api/v1/repositories` (R-REPO-014, R-REPO-015) and MUST be rejected
  as keys with 422 `reserved_name` ([api-and-ui.md](api-and-ui.md) R-API-004, Q-API-7). The
  OpenAPI key schema lists them as `x-trove-reserved-names`.
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
  422 `validation_failed` with `invalid_key` in `errors[]`; `npm-local` and `a1` are accepted.
- Creating a repository with the key `batch-create` or `quick-setup` returns 422 `reserved_name`.
- With remote `npm-remote` in place, `GET /api/npm/npm-remote~cache/lodash` reaches the cache view,
  and creating a repository named `npm-remote-cache` succeeds.

### R-REPO-003 · Format catalog and request classification
Priority: **MVP** · Covers: REPO-006

- Each format plug-in ([architecture.md](architecture.md)) MUST register: name; supported classes;
  layout name (R-REPO-006); protocol base path; default upstream URL and the `extraHosts` entries
  it proposes for that upstream (R-REPO-024); the probe resource of the upstream test (R-REPO-024;
  npm: `-/ping`, empty for a format whose URL is the service index); default metadata and not-found cache periods; the
  subject of include and exclude patterns (`name` or `path`, R-REPO-005); whether it supports
  priority members (R-REPO-022), pin rules and namespace claims (R-REPO-023), and repository caps
  (R-REPO-007); its package-name normalization; and, for formats with package names, the
  canonical path of a package's package document, the document whose existence upstream stands for
  the package's existence (npm: the package document `n`; NuGet: the version list
  `{lower_id}/index.json`). R-REPO-029 and R-REPO-048 use that path.
- The MVP formats are `npm` and `nuget`, each with all three classes (INV-14).
- For every package-protocol and `/files/` request the plug-in MUST produce a request descriptor:
  canonical path; request kind; normalized package name and version where the request concerns a
  package, or a routing name the format derives for an artifact that belongs to no package (NuGet
  symbol files: the first segment of the symbol key without its last extension, lowercased;
  [formats/nuget.md](formats/nuget.md) R-NUGET-015, Q-NUGET-7); a variant for content-negotiated
  metadata (npm's abbreviated document, for example); for artifacts, the expected digests when the
  format's metadata announces them (R-REPO-041).
- Patterns, claims and pin rules see the routing name; authorization does not. No other rule
  treats a routing name as a package name: it creates no package rows and is not subject to gone
  packages (R-REPO-029) or the shadowing check (R-REPO-048).
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
- Creating a repository with `"format":"cobol"` returns 422 `validation_failed` with `unknown_format` in `errors[]`.

### R-REPO-004 · Configuration document and common settings
Priority: **MVP** · Covers: REPO-010, REPO-016, REPO-017, REPO-028, RMT-030

A repository is one JSON document. It is the request body of create (R-REPO-009), the response of
read (R-REPO-010) and the object body of configuration as code
([R-OPS-029](operations-and-observability.md)).

| Member | Meaning | Default (proposed) | Owner |
|---|---|---|---|
| `key`, `class`, `format` | R-REPO-001, R-REPO-002 | — (required) | this page |
| `description` | Public description, shown to everyone who can read the repository | `""` (≤ 2000 characters) | this page |
| `adminNotes` | Notes shown only to holders of `repositories:read-config` | `""` (≤ 8000 characters) | this page |
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
  answers 422 `validation_failed` with `not_applicable` in `errors[]`.
- Pin rules, namespace claims, extra hosts and upstream credentials are generic members of this
  page (`virtual.pinning`, the claims API, `remote.extraHosts`, `remote.authMode`,
  `remote.username`, `remote.password`) for every format (Q-REPO-5). A format section MUST NOT
  define members for these mechanisms. A format page adds semantics (npm: which account name the
  public registry expects with `basic`), proposes defaults through the format catalog (NuGet:
  `*.nuget.org` as an extra host without credentials) and MAY narrow the generic rules, for
  example forbid `sendCredentials: true`; it never widens them. Format settings live under a
  member named after the format (`npm`, `nuget`), never under a generic name.
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
- `GET /api/v1/repositories/npm-local` by a caller without `repositories:read-config` contains no
  `adminNotes`.

### R-REPO-005 · Include and exclude patterns
Priority: **MVP** · Covers: REPO-011, RMT-034

- A path or package is admitted when it matches at least one include pattern (an empty include list
  admits everything) and no exclude pattern.
- The subject of the patterns is declared by the format (R-REPO-003):
  - `name` (npm, NuGet): patterns are name patterns (see Terms) matched against the normalized
    package name of the request. A routing name the format derives for a package-less artifact
    (R-REPO-003; NuGet symbol files) counts as the package name here. Only requests whose
    descriptor carries neither a package name nor a routing name (service index, search, ping)
    are admitted as package-less. One pattern therefore covers a package's documents and files
    alike, which avoids the trap of path patterns that admit the tarball but not the document.
  - Name patterns are matched by the matcher of [R-AUTHZ-005](authorization.md), the same code
    that evaluates permission and retention patterns ([storage.md](storage.md) Q-STOR-9), so a
    pattern copied between them selects the same names. On npm, `*` does not cross `/`: a bare
    `*` covers unscoped names only, `@*/*` covers every scoped name and `**` every name. A list
    that contains `*` on an npm repository but neither `**` nor `@*/*` returns the warning
    `pattern_unscoped_only` (R-REPO-008), because `excludePatterns: ["*"]` would otherwise look
    like "exclude everything" while scoped names, usually the internal ones, still reach the
    upstream. Include and exclude lists have no precedence among their patterns; most specific
    wins applies only to claims and pin rules (R-REPO-023).
  - `path` (generic and path-based formats, when they arrive): the path patterns of
    [R-AUTHZ-005](authorization.md), evaluated by the same matcher against the canonical path
    (Ant-style, [Apache Ant](https://ant.apache.org/manual/dirtasks.html#patterns)): `?` one
    character except `/`, `*` within one segment, `**` any number of segments, `\` escapes.
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
- Saving `excludePatterns: ["*"]` on `npm-remote` returns the warning `pattern_unscoped_only`;
  afterwards `npm view lodash` through it fails with `E404` and the stub records no request, while
  `npm view @partner/sdk` reaches the stub. With `["**"]` both fail and the stub records no request.
- A unit test runs the R-AUTHZ-005 matcher's test table through the routing code path and gets the
  same results.

### R-REPO-006 · Layouts (minimal)
Priority: **MVP** · Covers: REPO-012, REPO-046, REPO-047

- Each format has exactly one built-in layout, named like the format (`npm`, `nuget`). A layout is
  code in the format plug-in, not configuration: it defines the canonical path scheme and derives
  module information (package name, version, file role) from a canonical path.
- The `layout` member is read-only and equals the format's layout. A create that names another
  layout answers 422 `validation_failed` with `unsupported_layout` in `errors[]`.
- Module information MUST be recorded on items (the package and version rows of
  [storage.md](storage.md)), because the package view, cleanup and latest-version resolution need
  it.
- Later formats bring their own built-in layouts (a Maven 2 layout with the Maven family, a flat
  layout with the generic format). User-defined layouts, pattern tokens, integration-revision
  expressions and path translation between layouts are deferred (REPO-048 to REPO-052).

Acceptance criteria:

- `GET /api/v1/repositories/nuget-local` returns `"layout":"nuget"`; a create with
  `"layout":"maven-2"` for an npm repository returns 422 `validation_failed` with `unsupported_layout` in `errors[]`.
- After `npm publish` of `@acme/util@1.4.0`, the tarball item belongs to package `@acme/util`,
  version `1.4.0`.

### R-REPO-007 · Repository caps
Priority: **P1** · Covers: REPO-025, REPO-027

The caps are settings here; [R-STOR-044](storage.md) enforces them after each push, and the format
plug-in's version-group hook decides what a group is.

| Member (`local.caps.…`) | Meaning | Default (proposed) | Constraint |
|---|---|---|---|
| `maxVariantsPerGroup` | Distinct snapshot or pre-release variants kept per version group (format-defined) | `0` (no cap) | `0` or ≥ 2 |
| `maxTagsPerImage` | Distinct tag names kept per image (container formats) | `0` (no cap) | ≥ 0 |
| `keptTagRevisions` | Earlier revisions kept per tag name when a tag is overwritten (container formats) | `0` | ≥ 0 |

- All three caps are counts, never ages (RET-035): distinct variants per version group, distinct
  tag names per image, kept revisions per tag name. Federated repositories are not part of the
  MVP (INV-5); when they arrive, their caps use the same count semantics, not an age in days.
- When a cap is exceeded, the oldest entries beyond it are removed ([R-STOR-044](storage.md)).
  "Oldest" is the earliest creation of the entry's current version: a variant's deploy time, and
  for a tag the time the tag name last received a push, so a re-pushed tag counts as new and a tag
  that keeps receiving pushes is never the oldest. No tag name is exempt, `latest` included.
- Removals by caps go to the trash can when it is enabled ([R-STOR-022](storage.md)), for
  snapshots and tags alike, and are audited like any delete. When a tag is removed, its kept
  revisions go with it.
- A cap the format does not support answers 422 `validation_failed` with `not_applicable` in `errors[]`.
- A variant cap of 1 is refused with 422 `out_of_range`: an in-flight multi-file deploy would remove
  its own siblings.
- The container API version and the legacy manifest switch of REPO-027 belong to the container
  format page.

Acceptance criteria:

- `PATCH` with `{"local":{"caps":{"maxVariantsPerGroup":1}}}` returns 422 `out_of_range`; with `5`
  it is stored and read back.
- On an npm repository, `maxTagsPerImage: 3` returns 422 `validation_failed` with `not_applicable` in `errors[]`.
- With the container format and `maxTagsPerImage: 3` on `docker-local`, pushing `latest`, `t1`,
  `t2`, re-pushing `latest`, then pushing `t3` removes `t1`, not `latest`, and `t1` is in the trash
  can ([R-STOR-044](storage.md)).

## Repository management API

All operations below are part of the OpenAPI document at `/api/v1/openapi.json` (INV-6). Every
change writes an audit event (INV-16; actor, action, repository, outcome; secrets as fingerprints)
in the same transaction as the change and calls `cluster.Notify("repo_config", …)` with the
repository id in that transaction ([architecture.md](architecture.md) R-ARCH-018). Replicas MUST
apply a change within 2 seconds of commit (INV-12), with or without Valkey: the PostgreSQL change
log brings it within `cluster.epoch_poll_interval`, and Valkey's message within milliseconds.

Two in-memory structures depend on the area: every replica's repository snapshot (R-REPO-049) and
the grant index of [R-AUTHZ-028](authorization.md), which is built from each repository's key,
class, format, project, virtual membership, deploy target and authorization settings. The writes
that change an input of the index MUST therefore call `cluster.Notify("repo_config", …)` in their
own transaction, wherever they run:

- every repository create, including batch create, quick setup and configuration-as-code apply
  (R-REPO-009, R-REPO-014, R-REPO-015); key, class and format change only through create and
  delete (R-REPO-001);
- every repository delete: the first transaction, which sets `lifecycle = 'deleting'` and with
  `detach=true` removes the repository from member lists, pin rules and deploy targets, and the
  job transaction that removes the row (R-REPO-013);
- every change of the virtual membership (R-REPO-017, R-REPO-018), the deploy target (R-REPO-021),
  the project assignment ([R-AUTHZ-026](authorization.md), P1) or one of the authorization
  settings `cacheFillAction`, `packageOwnership`, `hideExistence` and `requireAuthentication`
  ([R-AUTHZ-031](authorization.md)).

The payload names the repository id, never a grant. R-AUTHZ-028's completeness test runs every
write operation of this page's API against a test database and fails when an input of the index
changed in a transaction that advanced neither the `authz` nor the `repo_config` epoch. Without
the notification, a replica would keep deciding on the old member list or the old
`cacheFillAction` until the index's `authz.cache.safety_ttl` rebuild.

Capability and action names are those of [authorization.md](authorization.md) (R-AUTHZ-010): `repositories:read-config`
(roles `admin`, `security-admin`, `auditor`, `repository-admin`), `repositories:write-config` (roles `admin`,
`repository-admin`), `settings:read` and `settings:write`, repository actions `read`, `deploy`,
`delete`, `manage`.

### R-REPO-008 · Configuration validation
Priority: **MVP** · Covers: REPO-053, REPO-054, RMT-002

The server MUST validate every create and update against all rules below in one pass and report
every violation (JSON pointer, code, message) in one 422 response. Malformed JSON and unknown
members answer 400 `malformed_request`: a configuration API that silently drops a misspelt setting
leaves the operator believing it is in effect.

| Rule | Code |
|---|---|
| Key syntax (R-REPO-002), project prefix for project administrators | `invalid_key` |
| Key is a reserved name (R-REPO-002) | `reserved_name` |
| Key taken, also by a repository being deleted | `already_exists` (409) |
| Class or format unknown, or the format does not support the class | `unsupported_class`, `unknown_format` |
| Class, format or key changed on update | `immutable` |
| Pattern syntax or limits (R-REPO-005); claim or pin-rule pattern outside the forms R-REPO-023 admits | `invalid_pattern` |
| Member missing, being deleted, of another format, duplicated, or the virtual repository itself | `invalid_member` |
| Nesting cycle or depth over the limit (R-REPO-018) | `virtual_cycle`, `virtual_too_deep` |
| Deploy target not a local repository in the effective member list (R-REPO-021) | `invalid_deploy_target` |
| Pin rule member not in the effective member list (R-REPO-023) | `invalid_pin_member` |
| Upstream URL not absolute `http`/`https`, or carrying user info or a fragment (R-REPO-024) | `invalid_upstream_url` |
| Numeric setting outside its range (R-REPO-024 and others) | `out_of_range` |
| `remote.sourceNetwork` not a CIDR prefix (R-REPO-024) | `invalid_source_network` |
| Referenced client certificate or outbound proxy missing | `unknown_reference` |
| Secret field with both `value` and `secretRef`, or a `secretRef` outside the rules of R-OPS-029 | `invalid_secret` |
| Custom header name or value invalid (R-REPO-036) | `invalid_header` |
| Member not applicable to class or format | `not_applicable` |

Warnings do not block a save; they are returned in a `warnings` list, for example
`dependency_confusion_unprotected` (R-REPO-023), `claim_overlaps` (R-REPO-023),
`pattern_unscoped_only` (R-REPO-005), `plain_http_upstream` or `source_network_single_address`
(R-REPO-024).

Acceptance criteria:

- A create of a virtual npm repository whose members are a NuGet repository and a missing key
  returns one 422 response with two `invalid_member` entries.
- A create with `"remote":{"url":"https://user:pw@registry.example.com/"}` returns 422
  `invalid_upstream_url`.
- A create with the misspelt member `"excludePattern"` returns 400 `malformed_request`.

### R-REPO-009 · Create a repository
Priority: **MVP** · Covers: REPO-053, RMT-002, API-052

| | |
|---|---|
| Request | `POST /api/v1/repositories`, body: the configuration document; omitted settings take their defaults, format defaults included |
| Success | 201, `Location: /api/v1/repositories/<key>`, `ETag: "<config_version>"`, body: the stored document as R-REPO-010 returns it, plus `warnings` |
| Errors | 400 `malformed_request`; 401; 403; 409 `already_exists`; 422 (R-REPO-008) |
| Permission | `repositories:write-config`; or project administrator of a project with `manageRepositories`, for keys `<projectKey>-…`, which are assigned to that project ([R-AUTHZ-026](authorization.md)) |

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
- Repeating the call returns 409 `already_exists`.
- The same call with a token lacking `repositories:write-config` returns 403 and creates nothing.

### R-REPO-010 · Read a repository configuration
Priority: **MVP** · Covers: REPO-055, REPO-056, API-057, API-058

| | |
|---|---|
| Request | `GET /api/v1/repositories/{key}` |
| Success | 200, `ETag: "<config_version>"`, body: the full document or the consumer view |
| Errors | 401; 404 per [R-AUTHZ-016](authorization.md) |
| Permission | `repositories:read-config` (or project administrator of the repository's project): full document; `read` on the repository: consumer view (Q-REPO-20) |

- The document contains only members that apply to the repository's class and format; the
  catalog's format-filtered read is therefore the only read.
- Secret fields are never returned: an inline secret reads as `{"set": true}`, a reference reads back
  as the `secretRef` it is (a file name or variable name is not secret).
- The consumer view contains `key`, `class`, `format`, `description`, `layout` and the client
  addresses, as [R-AUTHZ-016](authorization.md) defines it. It omits upstream settings, notes, pins,
  claims and members: an internal registry host or another team's repository key is information
  too.

Acceptance criteria:

- With `repositories:read-config`, `GET /api/v1/repositories/nuget-remote` returns `remote.url` and
  `"password": {"secretRef": {"file": "nuget-upstream-token"}}`; for an inline secret it returns
  `{"set": true}` and never the value.
- A user holding only `read` on `nuget-remote` receives the consumer view without a `remote`
  member; a user holding nothing on it receives 404.

### R-REPO-011 · Update a repository configuration
Priority: **MVP** · Covers: REPO-054, RMT-002, API-054

| | |
|---|---|
| Request | `PATCH /api/v1/repositories/{key}` with `Content-Type: application/merge-patch+json` ([RFC 7396](https://www.rfc-editor.org/rfc/rfc7396)); or `PUT /api/v1/repositories/{key}` with a complete document (omitted settings revert to defaults). `If-Match: "<config_version>"` required (428 without it; `If-Match: *` for a deliberate blind write). |
| Success | 200, new `ETag`, body: the stored document plus `warnings` |
| Errors | 400; 401; 403; 404; 409 managed by configuration as code ([R-OPS-029](operations-and-observability.md)); 412 `precondition_failed` with `currentEtag` ([api-and-ui.md](api-and-ui.md) R-API-009); 422 (R-REPO-008); 428 `precondition_required` |
| Permission | as create (R-REPO-009); the authorization members (`cacheFillAction`, `packageOwnership`, `ownershipDomain`, `hideExistence`) per [R-AUTHZ-012](authorization.md) |

- Merge-patch semantics: arrays (members, patterns, headers, pin rules) are replaced as a whole;
  `null` removes an optional setting; an omitted secret field stays unchanged; a secret field set to
  `null` clears it.
- `If-Match` is required on `PATCH` and `PUT` ([api-and-ui.md](api-and-ui.md) R-API-012,
  Q-API-2). Without it the update MUST be refused with 428 `precondition_required` and nothing
  changes; a stale version MUST be refused with 412 `precondition_failed`; `If-Match: *` passes
  for a repository that exists and is the explicit blind write. The arrays above are replaced as a
  whole, so a stale write of a virtual repository's `members` could put a remote member ahead of
  the local one again, a dependency-confusion path (R-REPO-023). The UI sends the `ETag` it loaded
  and never `If-Match: *` ([api-and-ui.md](api-and-ui.md) R-UI-042), so two administrators editing
  the same repository cannot overwrite each other unnoticed.
- Requests, examples and acceptance criteria on this page that `PATCH` or `PUT` a repository send
  the `If-Match` the caller read, unless they say otherwise.
- A change of `remote.url`, `extraHosts`, `authMode`, `username`, `password`, `clientCertificate`
  or `headers` MUST invalidate all metadata and negative entries of the repository (scope `all`,
  path empty, R-REPO-045): what the old upstream said is no evidence about the new one. Cached
  artifacts are kept.
- `PUT` never creates a repository; a missing key answers 404.

Acceptance criteria:

- `PATCH` with `{"remote":{"cache":{"metadataTtlSeconds":600}}}` changes only that setting; the
  next `GET` shows it and a new `ETag`.
- Two `PATCH` calls with the same `If-Match` value: the first returns 200, the second 412 with
  `currentEtag` set to the first call's new `ETag`.
- `curl -X PATCH -H "Authorization: Bearer $TROVE_TOKEN" -H 'Content-Type: application/merge-patch+json' -d '{"description":"npm proxy"}' https://trove.example.com/api/v1/repositories/npm-remote`
  without `If-Match` returns 428 `precondition_required` and the next `GET` shows the old
  `ETag`; the same call with `-H 'If-Match: *'` returns 200.
- After a change of `remote.url`, the next `npm view lodash` through the repository causes an
  upstream request although the cached document was fresh.

### R-REPO-012 · List repositories
Priority: **MVP** · Covers: REPO-007, REPO-057, REPO-058, API-060, API-061

| | |
|---|---|
| Request | `GET /api/v1/repositories?class=&format=&q=&state=&project=&view=&limit=&cursor=` |
| Success | 200, `{"items": [...], "nextCursor": "…"}` (paging per [api-and-ui.md](api-and-ui.md) R-API-010, Q-API-6) |
| Errors | 400 invalid filter; 401 |
| Permission | any caller, anonymous included; the result contains only repositories on which the caller holds `read` ([R-AUTHZ-016](authorization.md)), and every repository, those being deleted included, for a holder of `repositories:read-config` (Q-REPO-19); `view=full` requires `repositories:read-config` |

- Filters: `class` and `format` take comma-separated lists; `q` matches key and description
  case-insensitively; `project` filters by project key; `state=inactive` returns disabled
  repositories, offline remote repositories and remote repositories whose circuit is open
  (R-REPO-029).
- `view=summary` (default) returns per repository: key, class, format, description, client address,
  `disabled`, `lastActivityAt`, project, and for remote repositories the availability (`online`,
  `offline`, `assumed-offline`). Holders of `repositories:read-config` also get the upstream URL.
- `view=full` returns complete documents (the catalog's "all configurations").
- Class and format values are spelled as in configuration documents (lower case).
- `lastActivityAt` is the latest upload to or download from the repository. Each replica MUST
  aggregate activity in memory and write it at most once per minute per repository, so downloads do
  not turn into database writes. No request reads that buffer; it is flushed at shutdown
  ([architecture.md](architecture.md) R-ARCH-024 step 6), and a crash loses at most one minute of
  activity of that replica (R-REPO-049).

Acceptance criteria:

- `curl -s -H "Authorization: Bearer $TROVE_TOKEN"
  'https://trove.example.com/api/v1/repositories?class=remote&format=npm'` returns only npm remote
  repositories, each with an availability state.
- An anonymous caller, with anonymous read granted on `npm-all` only, sees exactly `npm-all`.
- `view=full` without `repositories:read-config` returns 403.

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
  of `repositories:read-config`. With `detach=true`, the same transaction removes it from every
  member list, pin rule and deploy-target reference.
- A background job ([architecture.md](architecture.md)) removes items permanently, bypassing the
  trash can ([storage.md](storage.md), R-STOR-018), and removes cache entries, URL mappings, claims
  and invalidations in batches. A batch that removes claims calls `cluster.Notify("claims", …)`
  with the repository id ([architecture.md](architecture.md) R-ARCH-018), so every replica drops
  them from its in-memory trie within 2 seconds (R-REPO-023); without that, a `global` claim of
  the deleted repository would keep refusing its names on every replica until that replica
  restarts. The repository's negative entries in Valkey are left to expire: their
  keys carry the repository id, which an identity column never hands out again (R-REPO-028). The job
  runs on the `worker` role and is resumable after a replica restart. The repository row goes last,
  which frees the key; that transaction calls `cluster.Notify("repo_config", …)`, because the row
  is an input of the grant index (Repository management API above).
- Job progress (items total and removed, errors) is read from the job resource.

Acceptance criteria:

- Deleting `npm-local` while `npm-all` lists it returns 409 naming `npm-all`; with `detach=true` it
  returns 202, `npm-all` no longer lists it, and `npm view @acme/util --registry
  …/api/npm/npm-local/` returns `E404` at once.
- After the job completes, a new repository named `npm-local` can be created and is empty.
- Killing the `worker` replica that runs the job does not prevent completion: another `worker`
  replica resumes it.

### R-REPO-014 · Batch operations
Priority: **P1** · Covers: REPO-059, REPO-061, REPO-062, REPO-064, API-053, API-055, API-059, API-064

| Request | Semantics | Success | Permission |
|---|---|---|---|
| `POST /api/v1/repositories/batch-create` `{"repositories": [doc, …]}` | All or nothing, one transaction; members may reference repositories created in the same batch | 201 with the stored documents | as create |
| `POST /api/v1/repositories/batch-update` `{"updates": [{"key", "ifMatch", "patch"}, …]}` | All or nothing, one transaction, merge patch per entry; `ifMatch` required per entry | 200 with the stored documents | as update |
| `POST /api/v1/repositories/batch-get` `{"keys": [...]}` | R-REPO-010 per key | 200 `{"repositories": {"<key>": doc}, "missing": [...]}` | as read, per key |
| `POST /api/v1/repositories/batch-delete` `{"keys": [...], "detach": false}` | Best effort per key, in request order | 202 with one result per key (job reference or error) | as delete |

- The actions are sub-path verbs ([api-and-ui.md](api-and-ui.md) R-API-004, Q-API-7); their names
  are reserved repository keys (R-REPO-002). All of them accept `Idempotency-Key`
  ([api-and-ui.md](api-and-ui.md) R-API-014).
- Every `batch-update` entry MUST carry `ifMatch` ([api-and-ui.md](api-and-ui.md) R-API-012): the
  `config_version` the caller read, or `*` to overwrite whatever is stored, so a blind write is an
  explicit choice. An entry without it fails the batch with 428 `precondition_required` naming its
  index; a stale value fails it with 412.
- The batch size is limited by `repositories.batch_limit`; larger batches answer 400
  `batch_too_large`. A failing create or update entry answers 422 (or 404, 412, 428) naming the
  index of every failing entry.
- A key the caller may not see is reported under `missing`, like a key that does not exist.

Configuration:

```yaml
repositories:
  batch_limit: 100  # default (proposed)
```

Acceptance criteria:

- A `batch-create` of `npm-local`, `npm-remote` and a virtual `npm-all` over both succeeds in one
  call; the same call with an invalid third entry creates nothing.
- A `batch-get` of 101 keys returns 400 `batch_too_large`.
- A `batch-update` of two entries, the second without `ifMatch`, returns 428 naming index 1 and
  changes nothing; with `"ifMatch": "*"` on the second entry it succeeds.

### R-REPO-015 · Quick setup of a repository set
Priority: **P1** · Covers: REPO-008

| | |
|---|---|
| Request | `POST /api/v1/repositories/quick-setup` `{"prefix": "billing", "formats": ["npm", "nuget"]}` |
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
| Errors | 404; 405 for formats or classes without derived metadata; 409 `reindex_running` naming the queued or running job |
| Permission | `repositories:write-config`, or `manage` on the repository |

- The format plug-in rebuilds the repository's derived metadata from its items. npm and NuGet
  metadata (npm package documents, NuGet registrations and version lists) is rendered per request
  from package rows ([architecture.md](architecture.md) R-ARCH-012), so there are no stored
  documents to rebuild: for them a reindex rebuilds the package and version rows from the
  repository's items, incrementing `rev_seq` on every package row it changes, and the next request
  renders from the rebuilt rows. A format with a job-built repository-wide index (the exception
  of R-ARCH-012) also rebuilds that index. On a virtual repository the operation drops its
  merged-metadata cache (R-REPO-020) on every replica.
- **The drop travels through a change area.** The transaction that records a virtual
  repository's reindex calls `cluster.Notify("cache_invalidation", …)` with the virtual
  repository's id ([architecture.md](architecture.md) R-ARCH-018). A replica that applies a
  `cache_invalidation` change naming a virtual repository drops that repository's merged entries.
  The drop therefore reaches every replica at its next change-log poll, also one that missed the
  Valkey message and while Valkey is unavailable. The signal area `metadata_changed` of R-REPO-020
  is not enough here: it is delivered at most once, and not at all without Valkey.
  [formats/npm.md](formats/npm.md) R-NPM-035 relies on this.
- A reindex is an `exclusive` job of [architecture.md](architecture.md) R-ARCH-016 with the key
  `index:<repository id>`: while one is queued or running, a new request answers 409
  `reindex_running` naming it. Automatic rebuilds of job-built indexes (R-ARCH-012) are `coalesce`
  jobs on the same key, so a manual reindex and an automatic rebuild of one repository never run
  at the same time. Neither takes an advisory-lock lease: leases carry registered numbers only
  (R-ARCH-017). Clients are served the previous metadata until the rebuilt metadata commits.

Acceptance criteria:

- After the package and version rows of `@acme/util` in `npm-local` are deleted in a test
  database, `npm view @acme/util` fails; after a reindex job completes, it succeeds with all
  versions.
- A second `POST …/repositories/npm-local/reindex` while the first job is queued or running
  returns 409 `reindex_running` naming that job.
- With Valkey stopped, the merged document of `lodash` is cached on replica B, and a reindex of
  `npm-all` is requested on replica A: after the job completes, the next `npm view lodash` through
  `npm-all` on B, sent `cluster.epoch_poll_interval` plus 1 s later, is merged again
  (`trove_cache_requests_total{cache="merged_metadata",result="miss"}` increments on B).

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
  repositories; virtual members arrive with R-REPO-018 and are refused with 422 `validation_failed` with `invalid_member` in `errors[]`
  until then.
- Members and their order can change without touching client configuration; a change takes effect
  on all replicas within 2 seconds and drops the repository's merged-metadata cache (R-REPO-020).
- Recommended practice, shown in the UI: clients use a virtual repository; local members come first.

Data model:

```sql
CREATE TABLE virtual_members (
  virtual_id bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
  position   integer NOT NULL CHECK (position >= 0),
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
  explanation (R-REPO-019) of both replicas within 2 seconds, also with Valkey stopped.

### R-REPO-018 · Nested virtual repositories
Priority: **P1** · Covers: REPO-034

- A virtual repository MAY list virtual repositories of the same format as members.
- The effective member list MUST be computed by depth-first expansion in member order, keeping only
  the first occurrence of each repository. It is recomputed whenever a repository on the way
  changes and is returned read-only as `virtual.effectiveMembers`, each entry with the chain of
  virtual repositories it was reached through.
- Nesting depth is limited by `repositories.max_virtual_depth`; deeper compositions answer 422
  `virtual_too_deep`. A cycle MUST be refused at save time with 422 `virtual_cycle`. Membership
  changes MUST be serialized with the transaction-scoped advisory lock `pg_advisory_xact_lock(4, 1)`
  (category `config`, object type 1 = virtual membership; [architecture.md](architecture.md)
  R-ARCH-017), so two concurrent updates cannot form a cycle that neither transaction sees.
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
  can read, so the header discloses nothing new. On an endpoint with exposure rules the header is
  never sent, because members are not exposed there ([endpoints.md](endpoints.md) R-EP-018).
- **Resolution explanation.** `GET /api/v1/repositories/{key}/resolution?path=<client path>`
  evaluates steps 1 to 6 for the calling principal without transferring content and without
  contacting upstreams, and returns per member the decision and its reason (`disabled`,
  `not_admitted`, `pinned-out`, `claimed-elsewhere`, `refused-claimed`, `no-read`, `no-cache-fill`,
  `hit`, `miss`, `negative`, `offline`, `circuit-open`). Members the caller cannot read appear only
  as a count (`"unreadableMembers": 2`) unless the caller holds `repositories:read-config`.
  Permission: the gate of step 1.

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
- Merged documents MAY be cached per replica in memory (registered as the cache `merged_metadata`
  of [architecture.md](architecture.md) R-ARCH-018), and only there: a merged document is
  served to clients, so it is never stored in or read from Valkey
  ([architecture.md](architecture.md) R-ARCH-030, Q-ARCH-16). The cache key MUST include the virtual
  repository, the canonical path, the variant, the endpoint of the request
  ([endpoints.md](endpoints.md) R-EP-018) and a SHA-256 fingerprint of the ordered list of
  members that took part after permission filtering: two callers who see different members never
  share an entry ([R-AUTHZ-009](authorization.md) rule 4); callers who see the same members do.
- A merged entry expires at the earlier of `mergedMetadataTtlSeconds` after merging and the earliest
  expiry of the contributing remote entries, so staleness does not add up across the two layers.
- **Local members: `rev_seq` check.** Every merged entry MUST record the `rev_seq` of every
  eligible local member, recorded as 0 when the member does not hold the package, and every request
  that finds the entry MUST compare them with the current values in one `RevSeqs` query; any
  difference makes the entry a miss ([architecture.md](architecture.md) R-ARCH-015, Q-ARCH-9). The
  check sits in the core's merged-metadata cache, not in a format's merge, and is a MUST for every
  format. A version published to, deleted from or restored into a local member is therefore
  visible through the virtual repository on every replica as soon as its commit has returned,
  whatever the period and whether or not a notification has arrived. The 0 for an absent package
  covers the first local publish of a name that so far came only from a remote member.
- **Early eviction.** A change of a member's metadata (publish, delete, restore, remote refresh
  with new content) also signals the area `metadata_changed` with repository id and package-name
  hash, which drops the affected merged entries on all replicas. It is a signal area of
  [architecture.md](architecture.md) R-ARCH-018: the signal leaves through the replica's post-commit
  coalescer to Valkey Pub/Sub (Q-ARCH-8), at most once, and not at all while Valkey is unavailable.
  It only evicts early. Visibility of local changes never depends on it (the `rev_seq` check above),
  and a remote refresh made on another replica reaches a merged entry at the latest when that entry
  expires, which is never later than the contributing remote entries. Claim changes reach the merged
  entries through their change area `claims` (R-REPO-023), whose bound does not depend on Valkey.
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
  --registry …/api/npm/npm-all/` on the other replica lists `1.5.0` immediately, also with Valkey
  stopped.
- With Valkey stopped and `local.autoClaim: false` on `npm-local` (so no new claim changes
  the member set), the merged document of `lodash`, served so far only by `npm-remote`, is cached
  on replica B; a `manage` holder publishes `lodash@99.0.0` to `npm-local` on replica A
  (R-REPO-048); the next `npm view lodash versions` through `npm-all` on B lists `99.0.0`.
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
- Support is a format capability (`priorityMembers` in the format catalog, R-REPO-003) and holds for
  every protocol the format serves. npm and NuGet (v3) support it.
- A protocol added to a supporting format later (NuGet v2, P2) MUST apply priority members exactly
  as the format's existing protocols do; a repository never honours priority on one of its
  protocols and ignores it on another.
- Formats not in trove yet (the catalog's exclusion lists name Chef, Debian, CocoaPods, Cargo and
  others) do not support priority members until their format page declares the capability. A
  container format page MUST define how priority interacts with tag resolution before it declares
  the capability.
- `priorityMember: true` on a virtual repository, or on a repository whose format lacks the
  capability, answers 422 `validation_failed` with `not_applicable` in `errors[]`.

Acceptance criteria:

- `npm-local` (`local.autoClaim: false`, no claims) is a priority member holding `util@1.0.0`,
  published by a `manage` holder (R-REPO-048); the stub upstream holds `util@9.9.9`: `npm view
  util versions --registry …/api/npm/npm-all/` lists only `1.0.0`; after the local package is
  deleted, it lists the upstream versions. (With the default `autoClaim: true`, the claim of
  R-REPO-023 keeps answering 404 after the deletion.)
- `nuget-all` = [`nuget-org`, `nuget-partner`]; `nuget-partner` (remote, priority member) serves
  `Partner.Sdk` `2.0.0`, the stub behind `nuget-org` serves `Partner.Sdk` `9.0.0`: the version list
  `GET …/api/nuget/nuget-all/v3/flatcontainer/partner.sdk/index.json` contains only `2.0.0`.
- `PATCH /api/v1/repositories/npm-all` with `{"priorityMember": true}` returns 422
  `not_applicable`.

### R-REPO-023 · Dependency-confusion protection: pin rules and namespace claims
Priority: **MVP** · Covers: BEYOND-028

Dependency confusion ([Birsan 2021](https://medium.com/@alex.birsan/dependency-confusion-4a5d60fec610))
lets a public package with an internal package's name, usually with a higher version, win
resolution. Tier order alone protects only names that currently exist internally; a name that is
not published yet, was deleted or is misspelt falls through to the remote members. trove closes
this on the server with two mechanisms, both using name patterns (see Terms; most specific wins).
Claim and pin-rule patterns are matched by the R-AUTHZ-005 matcher like every name pattern, but
are limited to an exact name or a literal (possibly empty) followed by one trailing `*` or `**`,
the forms for which most specific wins is a total order; any other pattern answers 422
`invalid_pattern`. A bare `*` therefore claims or pins every unscoped npm name and no scoped one.

**Namespace claims (local repositories).** A local repository holds a list of claimed name
patterns.

- With `local.autoClaim: true` (default (proposed) for formats that support claims), the first
  deploy of a package name that no claim of that repository covers creates an exact claim with
  scope `members` in the same transaction. Claims are not removed when packages are deleted: the
  deleted case is exactly when protection matters.
- An auto claim MUST NOT take a name from another repository. When a claim of another repository
  that is in effect in a virtual repository containing the deploying repository (directly or
  through nesting) covers the name, the deploy proceeds without creating the auto claim, and its
  audit event records `autoClaim: skipped`. In those virtual repositories the name keeps resolving
  from the claiming repository. Otherwise the exact auto claim, being the most specific match,
  would let any `deploy` holder of one team's repository take over a name that another team
  claims in a shared virtual repository. Taking a name over on purpose is a manual claim, which
  needs `manage` and is reported as `claim_overlaps`.
- Claims for names that do not exist yet are created explicitly; this reserves a scope or prefix
  before its first publish.
- **Claim scope.** Every claim has a scope. `members` (the default, and the only scope an auto
  claim gets) is in effect only in virtual repositories that contain the claiming repository,
  directly or through nesting. `global` is in effect in every virtual repository of the format and
  is the only scope that remote refusal honours. Creating or widening a `global` claim MUST require
  `repositories:write-config`; `manage` on the repository suffices for `members` claims. Without
  this split, one deploy of `react` into any local repository, even a personal sandbox that no
  virtual repository contains, would make `react` unresolvable in every virtual repository and,
  through remote refusal, in every remote repository of the format.
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
  ([R-AUTHZ-018](authorization.md)); claims decide where a name resolves from; whether a first
  publish may shadow a package that a remote member serves is decided by R-REPO-048.

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
  format, keyed by each pattern's literal part; candidates on the path are confirmed by the
  R-AUTHZ-005 matcher, so `*` stops at `/`), loaded at start and updated through the change area
  `claims` ([architecture.md](architecture.md) R-ARCH-018); a lookup costs no database query. A
  claim change, including the auto claim of a name's first publish, is in force on every replica
  within 2 seconds of its commit, with or without Valkey. The auto claim writes the area once per
  name and repository, never per upload of a later version.
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
  position         integer NOT NULL CHECK (position >= 0),
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
| `GET /api/v1/repositories/{key}/claims` | 200 `{"claims": [{"pattern": "@acme/*", "source": "manual", "scope": "global", "createdAt": …}]}`, `ETag: "<claims_version>"` | `read` on the repository |
| `PUT /api/v1/repositories/{key}/claims` | `{"claims": [{"pattern": "@acme/*", "scope": "global"}]}`; `scope` defaults to `members`; replaces the manual claims; auto claims stay unless listed under `"removeAuto"`; `If-Match: "<claims_version>"` required → 200 with the new `ETag`; 412 `precondition_failed` when stale; 428 `precondition_required` without it | `manage` on the repository for `members` claims; `repositories:write-config` when the request adds or keeps a `global` claim |
| `PATCH /api/v1/repositories/{key}` | `{"virtual": {"pinning": {"rules": [...]}}}` with `If-Match: "<config_version>"` (R-REPO-011) | as update |

- The claims of a repository are one revisioned resource: `claims_version` increases with every
  change of its claims, manual or automatic (R-REPO-048), in the transaction that makes it. A `PUT`
  replaces the manual claim set as a whole, so a stale one could drop a `global` claim another
  administrator just added and reopen a dependency-confusion path; it therefore requires `If-Match`
  ([api-and-ui.md](api-and-ui.md) R-API-012, Q-API-2), with `If-Match: *` as the deliberate blind
  write. A 412 after an automatic claim was added means: read again, then write.

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
- `autoClaim: true` and `left-pad@1.0.0` published to `npm-local` by a holder of `manage` on it
  (R-REPO-048); the stub holds `left-pad@2.0.0`.
  `npm install left-pad --registry …/api/npm/npm-all/` installs `1.0.0`. After the local package is
  deleted, the install fails with `E404` instead of installing `2.0.0`. A user with `read` on
  `npm-all` but not on `npm-local` gets `E404`, not `2.0.0`.
- `nuget-local` claims `Acme.*` with scope `global`: `dotnet add package Acme.Internal` against
  `nuget-remote` directly fails with a not-found error, and the stub records no request.
- `npm-sandbox` (local, `autoClaim: true`, member of no virtual repository) receives a publish of
  `react@99.0.0`. The claim reads back with scope `members`; `npm view react versions --registry
  …/api/npm/npm-all/` still lists the upstream versions, and `npm-remote` addressed directly still
  serves `react`.
- A principal holding `manage` on `npm-local` but not `repositories:write-config` gets 403 on
  `PUT …/claims` with a `global` claim, and 200 with the same pattern as a `members` claim (both
  with the current `If-Match`).
- `PUT …/repositories/npm-local/claims` without `If-Match` answers 428 and changes no claim. After
  a publish to `npm-local` with `autoClaim: true` adds an automatic claim, a `PUT` with the `ETag`
  read before the publish answers 412 with the new tag as `currentEtag`.
- A pin rule `{"packages": ["@partner/*"], "members": ["npm-partner-remote"]}` in `npm-all` resolves
  `@partner/sdk` from that remote even though `npm-local` claims `@partner/*`.
- `npm-team-a` (manual claim `@acme/*`, scope `members`) and `npm-team-b` are both members of
  `npm-all`. A holder of `deploy` on `npm-team-b` publishes `@acme/util@9.9.9` there: the publish
  succeeds, `GET …/repositories/npm-team-b/claims` lists no claim for `@acme/util`, and `npm view
  @acme/util versions --registry …/api/npm/npm-all/` lists only the versions in `npm-team-a`.

### R-REPO-048 · Shadowing check on the first publish of a name
Priority: **MVP** · Covers: BEYOND-028

Local members form the first resolution tier, and the first publish of a name creates a claim
(R-REPO-023). A publish of a name that a remote member already serves therefore replaces the public
package for every client of the virtual repositories that contain the local repository, and hides
the versions that exist only upstream (H-10). Such a publish MUST be the deliberate decision of a
`manage` holder, never the side effect of a `deploy` grant (Q-REPO-6).

**When the check runs.** On every write that would give a local repository L, whose format
supports claims (`pinsAndClaims`, R-REPO-003), a version of the normalized package name n: a
deploy addressed to L directly or through a virtual repository's deploy target (R-REPO-021), and,
from P1, a copy, move or trash-can restore into L ([storage.md](storage.md) R-STOR-019,
R-STOR-023). It runs when both hold:

- L holds no version of n, and
- no claim of L, manual or automatic, of either scope, covers n.

Item operations report a refusal per item (`shadows_remote_package`, `shadow_check_unavailable`)
and leave that item unchanged. Without them, a `deploy` holder could publish a name into a
repository that no virtual repository contains, where nothing is exposed, and copy it into L
afterwards.

Later versions of a name L already holds or claims are never checked, so a public package that
someone registers after an internal name's first publish cannot block the internal team's
releases. With `autoClaim: true` the check therefore runs once per name and repository.

**Exposure set.** The pairs (V, R) where V is an active virtual repository whose effective member
list contains L (R-REPO-018) and R is a remote member of V that R-REPO-019 steps 2 and 3, evaluated
for n on stored configuration only, would keep together with L: L and R are enabled and admitted by
their own patterns and by those of every virtual repository on their chains; if a pin rule of V
selects n, it names both L and R; and no claim in effect in V covers n (where one does, remote
members are not consulted for n, so nothing remote is shadowed). An empty exposure set (L is in no
virtual repository, or no remote member would be consulted for n) lets the publish proceed without
any upstream request.

**Whether R serves n.** Decided without regard to the caller and without the cache-fill check
(R-AUTHZ-008), on the canonical path of n's package document, which the format registers
(R-REPO-003; npm: the package document `n`; NuGet: the version list `{lower_id}/index.json`,
R-NUGET-015):

1. A stored metadata entry for that path (any variant), fresh or expired and not marked upstream
   gone (R-REPO-029): served.
2. A stored entry marked upstream gone: not served. Negative entries (R-REPO-028) are not
   consulted. They live in Valkey, and a value read from Valkey may make trove refuse, never admit
   a write ([architecture.md](architecture.md) R-ARCH-030); "not served" admits a `deploy` holder's
   publish. A path for which R holds only a negative entry goes on to step 3.
3. R offline (R-REPO-030, R-REPO-031) and nothing stored: not served; no upstream request.
4. R's circuit open (R-REPO-029) and nothing stored: undetermined; no upstream request.
5. Otherwise a probe: one `GET` of that path through R's fetch client, with its egress rules, TLS
   verification, credentials, redirect rules, size limits and fetch slots (R-REPO-033 to
   R-REPO-040), bounded by R's timeouts. 2xx: served. 404 or 410: not served. Timeout, 401, 403,
   429, 5xx and every other failure: undetermined. Probe outcomes count toward R's circuit like any
   other upstream request. The probe stores nothing, neither an entry nor a negative entry: under
   `cacheFillAction: deploy` a stored document would advertise versions on behalf of a publisher
   who need not hold that action.

The probes of one publish run concurrently. They run after the format has read the declared
identity and fine-grained authorization has passed (R-ARCH-014 steps 4 and 5), because the outcome
depends on whether the caller holds `manage`, and before the commit (step 6); for item operations,
before the operation's transaction. No database transaction is held open during a probe.

**Outcome.**

| Result over the exposure set | Caller holds `manage` on the coordinate in L, or `content:bypass` | Otherwise |
|---|---|---|
| Empty, or no R serves n | proceed | proceed |
| At least one R serves n | proceed; security audit event `repository.remote_package_shadowed` (package, L, the pairs (V, R), actor) in the commit transaction | 403 `shadows_remote_package`; nothing stored, no claim created |
| None serves n, at least one undetermined | proceed; the publish's audit event records `shadowCheck: undetermined` | 503 `shadow_check_unavailable` with `Retry-After` (the remaining circuit-open time, else 30 s); nothing stored |

- The check comes in addition to `deploy` and package ownership ([R-AUTHZ-018](authorization.md));
  it never grants anything. A refusal is audited as a denial with reason `deny.shadowing`.
- An unreachable upstream fails closed: it must not be a way around the check. Only first publishes
  of names outside L's claims are affected while a remote member's upstream is down.
- **Approval in advance.** A manual claim of L that covers n satisfies the check, because the check
  runs only when no claim of L covers n. A `members` claim needs `manage` on L (R-REPO-023), so it
  is the same decision taken ahead of time. This is the path for CI: a `manage` holder claims
  `lodash` (or `@acme/*`) on `npm-patches`, after which a release job holding only `deploy`
  publishes there.
- The error body names the package and the virtual repositories in which the publish would shadow a
  remote package, limited to those the caller holds `read` on. It never names remote members.
- The check reads L's committed versions and claims before the commit transaction and needs no
  lock. A concurrent first publish of the same name can at worst make the check run once more than
  needed; an uncommitted publish never causes it to be skipped.
- Membership and other configuration changes do not re-run the check for names a local member
  already holds (H-10).

Acceptance criteria (`npm-all` = [`npm-local`, `npm-remote`], `nuget-all` = [`nuget-local`,
`nuget-remote`], counting stub upstreams; `ci-bot` holds `deploy` but not `manage` on the local
repositories, `lead` holds both; `packageOwnership: off` on the local repositories, so ownership
plays no part):

- The stub serves `lodash`. `ci-bot` runs `npm publish` of `lodash@4.17.22` through `npm-all`: it
  fails with `E403` and a message naming `shadows_remote_package`; `npm-local` holds no `lodash`
  and no claim for it; `npm view lodash version --registry …/api/npm/npm-all/` still shows the
  upstream version.
- `lead` publishes the same tarball: it succeeds, and a `repository.remote_package_shadowed` event
  names `npm-local`, `npm-all` and `npm-remote`. `ci-bot` then publishes `lodash@4.17.23`, which
  succeeds, and the stub records no request for `lodash` during that publish.
- `lead` sets the manual claim `left-pad` (scope `members`) on `npm-local`; `ci-bot` then publishes
  `left-pad@9.0.0` successfully although the stub serves `left-pad`.
- `ci-bot` publishes `acme-brand-new@1.0.0`, which the stub does not know: it succeeds after exactly
  one stub request for the name, and no negative entry exists afterwards, neither in the replica's
  memory nor in Valkey.
- The stub serves `acme-brand-new-3`, and a negative entry for its package document is written into
  Valkey by hand with a current creation time. `ci-bot`'s publish of `acme-brand-new-3@1.0.0`
  answers 403 `shadows_remote_package` after one stub request.
- `npm-sandbox` (member of no virtual repository): `ci-bot` publishes `react@99.0.0`; it succeeds
  and the stub records no request.
- The circuit of `npm-remote` is open and nothing is stored for `brand-new-2`: `ci-bot`'s publish
  of it answers 503 `shadow_check_unavailable` with `Retry-After`; `lead`'s succeeds.
- `dotnet nuget push Newtonsoft.Json.99.0.0.nupkg --source …/api/nuget/nuget-all/v3/index.json
  --api-key "$CI_BOT_TOKEN"` fails with 403 while the stub serves `newtonsoft.json`.
- P1, with copy: `ci-bot` publishes `lodash@4.17.22` to `npm-sandbox` (succeeds, nothing exposed)
  and then copies it to `npm-local` with `POST /api/v1/content/copy`: the item is reported with
  `shadows_remote_package`, and `npm-local` holds no `lodash`.

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
| `extraHosts` | Further hosts that upstream metadata or redirects may point to: `[{"pattern": "*.nuget.org", "sendCredentials": false}]` | `[]`; a format proposes entries for its well-known upstream, applied when `url` is omitted or equals it (Q-REPO-91) | ≤ 20 | — | R-REPO-043 |
| `authMode` | `none`, `basic`, `bearer` | `none` | | RMT-005, RMT-006 | R-REPO-033 |
| `username` | Upstream account | `""` | ≤ 256 | RMT-005 | R-REPO-033 |
| `password` | Password or token; secret field | unset | ≤ 8 KiB | RMT-005 | R-REPO-033 |
| `clientCertificate` | Name of a stored client certificate | `null` | | RMT-007 | R-REPO-034 |
| `headers` | Extra request headers | `[]` | ≤ 32 | RMT-009 | R-REPO-036 |
| `forwardAuthOnRedirect` | Send credentials to other origins on redirects | `false` | | RMT-012 | R-REPO-039 |
| `outboundProxy` | `{"mode": "default"\|"named"\|"direct", "name": …}` | `{"mode": "default"}` | | RMT-016 | R-REPO-037 |
| `connectTimeoutMs` | TCP and TLS connection establishment | `10000` | 1000–120000 | RMT-015 | here |
| `readTimeoutMs` | Longest time without receiving a byte | `15000` | 1000–600000 | RMT-015 | here |
| `sourceNetwork` | Network prefix from which each replica takes its own local address for upstream connections (Q-REPO-9) | `null` (kernel choice) | an IPv4 or IPv6 CIDR prefix | RMT-015 | here |
| `cacheFillAction` | `read` or `deploy` | `read` | | IAM-023 | [R-AUTHZ-008](authorization.md) |
| `cache.storeArtifacts` | `false` = pass-through | `true` | | RMT-017 | R-REPO-044 |
| `cache.metadataTtlSeconds` | Metadata period | format default (npm: `7200`, NuGet: `300`) | 0–2592000 | RMT-018 | R-REPO-027 |
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
| `limits.maxConcurrentFetches` | Upstream fetches of this repository in flight across all replicas, shared out per replica (Q-REPO-8) | `64` | 1–4096 | — | R-REPO-040 |

</details>

- `url` is stored normalized (scheme and host lower case, default port removed, an empty path as
  `/`). An omitted `url` takes the format's default upstream; a format without one makes it required
  (422 `invalid_upstream_url`). The upstream request for a canonical path is derived from it by the
  format plug-in. A plain `http` URL is accepted with the warning `plain_http_upstream`. A URL that
  points at a denied address is accepted too: the refusal is `egress_denied` at the first request
  (R-REPO-038).
- The stored section is canonical: every member of the table is present, with the default where the
  request omitted it, and a read returns exactly that. `password` reads `null`, `{"set": true}` or its
  reference (R-REPO-033); `sourceNetwork` reads as the network (`10.99.0.77/24` is stored as
  `10.99.0.0/24`).
- Until their requirements are built, `clientCertificate` (R-REPO-034), `headers` (R-REPO-036),
  `outboundProxy.mode: named` (R-REPO-037), `cache.storeArtifacts: false` (R-REPO-044) and a
  `cache.unusedRetentionHours` above 0 (R-REPO-046) are refused with 422 `not_applicable`: a setting
  that is accepted and not enforced would leave the operator believing it is in effect.
  `outboundProxy.mode: direct` is built and means no proxy (Q-REPO-91).
- `sourceNetwork` names a prefix, never a single address: replicas are interchangeable, and every
  pod has its own address ([Kubernetes network model](https://kubernetes.io/docs/concepts/services-networking/):
  "Each pod in a cluster gets its own unique cluster-wide IP address"), so one stored address could
  fit one pod at most (Q-REPO-9). Each replica binds the repository's upstream connections to the
  first of its own interface addresses inside the prefix, enumerated at start and every 60 s.
  - A replica without such an address answers the repository's fetches with 502
    `source_network_unavailable`. The failure does not count toward the circuit, which all replicas
    share (R-REPO-029); it is logged at most once per minute and repository and counted in
    `trove_remote_source_network_unavailable_total{repo}` (R-REPO-047). There is no fallback to the
    kernel's choice, which could leave through an interface the upstream's firewall rejects.
  - A value that is not a CIDR prefix answers 422 `invalid_source_network`. A prefix that holds a
    single address (`/32`, `/128`) is stored with the warning `source_network_single_address`,
    because it can fit one pod only.
  - The enumeration happens when the client is built and on the first dial after the last one is 60 s old;
    a failing enumeration keeps the last good address, and one that finds no address in the prefix drops
    it (Q-ARCH-198).
  - The upstream test reports the local address the probing replica bound, or
    `source_network_unavailable`.

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
| `POST /api/v1/repositories/{key}/upstream-test` | Probe the saved upstream configuration | 200 with the result below | `repositories:write-config` |
| `POST /api/v1/upstream-tests` | Probe an unsaved `remote` section (body: `format` and `remote`); secret values in the body are used once and not stored | 200 with the result below | `repositories:write-config` |

```json
{
  "reachable": true,
  "httpStatus": 200,
  "authenticated": true,
  "durationMs": 184,
  "tls": { "peerSubject": "CN=registry.npmjs.org", "notAfter": "2027-03-01T00:00:00Z" },
  "outboundProxy": null,
  "sourceAddress": null,
  "error": null
}
```

The probe requests the format's probe resource (npm: the registry ping; NuGet: the service index,
whose required resources [formats/nuget.md](formats/nuget.md) checks) through the same client,
egress rules, proxy and credentials that real fetches use (Q-REPO-90).

- The answer is always 200 for a repository that can be tested: an upstream that fails is a result
  with `reachable: false` and the reason in `error`. `reachable` is true when the upstream answered
  with an HTTP response, whatever its status; `authenticated` is false for 401 and 403 and `null`
  when there was no response; `tls` describes the leaf certificate of the host that answered and is
  `null` over plain `http`; `outboundProxy` is `null` until named proxies exist.
- `error` is `null` or `{"code", "message"}` with one of `dns_error`, `connection_refused`,
  `timeout`, `tls_untrusted`, `egress_denied`, `source_network_unavailable`,
  `upstream_secret_unavailable`, `offline`, `too_many_redirects`, `insecure_redirect` and
  `request_failed`. The message names the host and never repeats the URL, whose query may carry a
  token, or a secret.
- A repository in `availability.mode: offline` and every repository while `proxy.global_offline` is on
  are not contacted: the result is `reachable: false` with `offline` (R-REPO-030, R-REPO-031).
- The test reads and writes no circuit state, counts nothing toward it and leaves cache entries
  alone (R-REPO-029). It runs on the replica that received it; with a `sourceNetwork`,
  `sourceAddress` is the address that replica bound.
- `POST /api/v1/repositories/{key}/upstream-test` answers 404 `repository_not_found` for a missing key
  and 400 `not_applicable` for a local or virtual repository. `POST /api/v1/upstream-tests` checks
  the section like a create (422 with every violation, 400 for a misspelt member); a secret value in
  it is used for the probe and stored nowhere; `{"set": true}` is 422 `invalid_secret`, because it
  refers to a stored value.

Acceptance criteria:

- A remote created with only `url` reads back every member of the table with its default.
- The upstream test against `https://registry.npmjs.org/` returns `reachable: true`; against an
  unknown host it returns `reachable: false` with a DNS error and leaves the circuit state alone.
- `"connectTimeoutMs": 50` returns 422 `out_of_range`.
- Two replicas each have a secondary interface in `10.99.0.0/24` (`10.99.0.11`, `10.99.0.12`), and
  the remote has `"sourceNetwork": "10.99.0.0/24"`: the stub records requests from both addresses
  and from no other. A third replica without such an interface answers 502
  `source_network_unavailable`, and the circuit stays closed on all three.
- `"sourceNetwork": "10.99.0.11"` returns 422 `invalid_source_network`; `"10.99.0.11/32"` is stored
  with the warning `source_network_single_address`.
- A create with `"url": "http://registry.internal.example/"` is saved with the warning
  `plain_http_upstream`; `"clientCertificate": "mtls"` returns 422 `not_applicable`.
- The upstream test of a repository in offline mode sends no request and reports `offline`; the test
  of a local repository returns 400 `not_applicable`.

### R-REPO-025 · Remote request path
Priority: **MVP** · Covers: RMT-001, RMT-033

For a request with descriptor d (R-REPO-003) to remote repository R by caller c, the server MUST
execute these steps in order:

1. R disabled → 404 `repository_disabled`.
2. d not admitted by R's patterns, or d's package name refused because of a claim (R-REPO-023) →
   404, no upstream request.
3. c lacks `read` on R for d → 401, 403 or 404 per [R-AUTHZ-016](authorization.md).
4. Look up the cache entry (R, path, variant). An entry marked upstream gone, and every entry of a
   package that is gone from R, counts as absent here and in every later step (R-REPO-029; gone
   packages below). An artifact entry, or a metadata entry that is fresh and not invalidated
   (R-REPO-045), is served: `Cache-Status: trove; hit; ttl=<seconds>`. A format MAY require a
   current package document before it serves a cached artifact. npm does: a cached tarball is
   served only while its package document's entry is unexpired and validated after
   `system_state.restored_at`; otherwise the request first revalidates the document under these
   steps (one flight, R-REPO-026) and then applies the format's removal marks to the outcome
   ([formats/npm.md](formats/npm.md) R-NPM-025, Q-NPM-13; R-REPO-029).
5. Request through the cache view, or with `Cache-Control: only-if-cached` → R-REPO-032.
6. R offline, global offline (R-REPO-031), or read-only maintenance mode with `cacheFill: false`
   ([R-OPS-024](operations-and-observability.md)) → serve an existing entry as stale, otherwise 404
   `not_cached_offline`.
7. R's circuit open (R-REPO-029) → serve an existing entry as stale, otherwise 503
   `upstream_assumed_offline` with `Retry-After`.
8. A valid negative entry (R-REPO-028; the replica's L1 first, then Valkey) → 404,
   `Cache-Status: trove; hit; detail=negative`. While Valkey is unavailable, only the L1 answers.
9. c fails the cache-fill check ([R-AUTHZ-008](authorization.md)), which every caller fails on an
   endpoint with `cache_fill: false` ([endpoints.md](endpoints.md) R-EP-019) → serve an existing
   expired entry as stale without contacting the upstream, otherwise 403 `populate_not_permitted`
   (R-AUTHZ-008; through a virtual repository the member counts as a miss). No negative entry is
   written.
10. The collapsing protocol (R-REPO-026). Only the request that fetches, once it holds the flight
    or goes on without the cross-replica lock, takes the fetch slots of R-REPO-040; then the fetch,
    classification of the outcome (R-REPO-029), verification (R-REPO-041, R-REPO-042), storage and
    response.

- **Gone packages** (R-REPO-029). A request whose descriptor names a package that is gone from R,
  other than the request for that package's package document, MUST answer 404 after step 3,
  without serving a stored entry and without an upstream request for its path. One exception keeps
  the mark self-healing for clients that request artifacts without documents: when the package
  document has no valid negative entry (it expired or was invalidated, R-REPO-045), the request
  first refreshes the package document under these steps (one flight, R-REPO-026) and waits for
  it; a 2xx clears the mark and the request continues at step 4, any other outcome answers 404.
  The cache view never refreshes and answers 404. The package document request itself follows the
  steps; its stored entry counts as absent, so its negative entry answers (step 8) or the upstream
  is asked.
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
  digest, so a fresh hit costs no object-store round trip. Which digest a request serves is always
  read from `remote_cache_entries`; the key is the content address, so this cache can never make
  one replica answer differently from another (INV-18, R-REPO-049). It is never shared through
  Valkey ([architecture.md](architecture.md) R-ARCH-030).

Data model:

```sql
CREATE TABLE remote_cache_entries (
  id                     bigint GENERATED ALWAYS AS IDENTITY UNIQUE,     -- joins the old and new rows of the blob release trigger (Q-REPO-86)
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
  upstream_gone_at       timestamptz,            -- metadata: upstream answered 404/410 (R-REPO-029)
  last_used_at           timestamptz NOT NULL,   -- metadata use, written in batches (R-REPO-046)
  PRIMARY KEY (repo_id, path, variant),
  CHECK ((kind = 'artifact') = (item_id IS NOT NULL)),
  CHECK ((kind = 'artifact') = (expires_at IS NULL)),
  CHECK (kind = 'metadata' OR upstream_gone_at IS NULL)
);
CREATE INDEX remote_cache_entries_blob ON remote_cache_entries (blob_sha256);       -- every blob reference is indexed (R-STOR-003)
CREATE INDEX remote_cache_entries_item ON remote_cache_entries (item_id) WHERE item_id IS NOT NULL;
CREATE INDEX remote_cache_entries_unused ON remote_cache_entries (repo_id, last_used_at)
  WHERE kind = 'metadata';
CREATE INDEX remote_cache_entries_gone ON remote_cache_entries (repo_id)
  WHERE upstream_gone_at IS NOT NULL;                 -- loads the gone packages (R-REPO-029)
-- Triggers blob_ref_guard, blob_ref_release and blob_ref_release_update as on items (storage.md R-STOR-003).
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
- With the S3 store stopped, an uncached tarball is still installed through `npm-remote` and no cache entry
  is written.
- A unit test of the upstream URL builder turns the canonical segment `%2e%2e` into `%252e%252e`
  and `a b` into `a%20b`; no input produces an upstream path that decodes to a `..` segment.

### R-REPO-026 · Collapsing concurrent misses (single-flight and Valkey flight lock)
Priority: **MVP** · Covers: trove-only

Concurrent requests for the same uncached or expired entry MUST cause one upstream fetch per
cluster, not one per request (INV-11). Since 2026-10-28 the cross-replica part is a lock in Valkey
behind `cluster.FlightLock` ([architecture.md](architecture.md) R-ARCH-033, Q-ARCH-6), not a
PostgreSQL advisory lock. The protocol:

1. **Flight key.** (repository id, canonical path, variant). `<f>`, the lowercase hex SHA-256 of
   `repository id 0x00 path 0x00 variant` (the id in decimal digits), names the flight in Valkey: the lock
   `trove:<ks>:lock:fetch:v1:<f>` and the outcome channel `trove:<ks>:fill:v1:<f>`
   ([architecture.md](architecture.md) R-ARCH-030). The full hash leaves no collision case
   (Q-ARCH-7).
2. **In-process.** Requests on one replica join one flight
   ([singleflight](https://pkg.go.dev/golang.org/x/sync/singleflight)). The leader runs steps 3 to 6;
   followers wait for its result and then read the stored entry, or receive its error.
3. **Cross-replica lock.** The leader calls `FlightLock.Acquire` and never talks to Valkey itself.
   `Acquire` tries once: `SET trove:<ks>:lock:fetch:v1:<f> <token> NX PX 30000`, with the TTL
   `cluster.flight_lock_ttl` (30 s by default) and a token unique to this acquisition
   ([architecture.md](architecture.md) R-ARCH-033).
   If it acquires the flight, it repeats the checks of R-REPO-025 steps 4 to 8, because another
   replica may have just stored the entry, recorded an upstream 404 as a negative entry or opened
   the circuit. If one of them decides, the leader answers from it and releases the flight.
   Otherwise it takes its fetch slots (R-REPO-040) and fetches. While the fetch, verification and
   commit run, `FlightLock` extends the hold every third of `cluster.flight_lock_ttl` with a
   compare-and-set on its token ([SET … IFEQ](https://valkey.io/commands/set/)).
4. **Waiting.** If another replica holds the flight, the leader calls `FlightLock.Wait`: it
   subscribes to the outcome channel, checks the lock once more, and returns on the outcome, when
   the lock is gone, or after `proxy.lock_poll_interval_ms`, after which it checks again. Polling
   stays mandatory, because Pub/Sub delivers at most once
   ([Pub/Sub](https://valkey.io/topics/pubsub/)). After `Wait` returns, the leader repeats the
   checks of R-REPO-025 steps 4 to 8: a valid entry is served, a valid negative entry answers 404,
   an open circuit answers as step 7 says. Only when none of them decides does it call `Acquire`
   again (the holder failed, or its lock expired). Without the negative-entry check, every waiting
   replica would ask the upstream once more, one after another, for a path the holder just found
   missing. With `notFoundTtlSeconds: 0` nothing records the miss, and that is what happens.
   - Waiting is bounded by `proxy.fetch_wait_timeout_seconds`. When the bound ends without a valid
     entry, the leader fetches without the cross-replica lock instead of failing, counted in
     `trove_flight_lock_bypass_total{reason="wait_timeout"}`.
   - For expired metadata, the refresh timeout of R-REPO-027 ends the client's wait earlier: the
     client is served the stale entry. The flight keeps waiting, detached like a fetch, until
     `proxy.fetch_wait_timeout_seconds`, and then refreshes without the lock (`wait_timeout`). A
     slow refresh that another replica holds is therefore not duplicated within that bound, and a
     held lock cannot keep a document stale beyond it.
   - A lock that is held too long, or written by someone else ([architecture.md](architecture.md)
     H-49), can therefore delay a fetch or a refresh by at most `proxy.fetch_wait_timeout_seconds`,
     never fail a request.
5. **Commit and release.** The blob is committed through [storage.md](storage.md); the entry upsert
   runs in one short transaction, the cache-fill transaction, which also writes the populate audit
   event (below). An upstream 404 or 410 is recorded as a negative entry
   (R-REPO-028) before the release. After the entry has committed, after the negative entry has
   been written, or after the fetch has failed, `FlightHold.Release` deletes the lock only if it
   still carries this hold's token ([DELIFEQ](https://valkey.io/commands/delifeq/)) and publishes
   `stored` or `failed` on the outcome channel. A woken waiter therefore finds the committed entry
   or the negative entry through step 4's checks. The leader's own client receives the withheld
   tail (R-REPO-041) only after the entry has committed. No PostgreSQL connection is held and no
   transaction is open while acquiring, waiting, transferring or extending
   ([architecture.md](architecture.md) R-ARCH-019).
6. **Holder loss.** A holder that shuts down releases with `failed` (shutdown bullet below). A
   holder that dies without releasing (process killed, node lost) frees the flight when its key
   expires, after at most `cluster.flight_lock_ttl`; waiters acquire it at their next check and
   fetch. Neither case depends on a database session or on TCP keepalive.

- **Valkey unavailable.** `Acquire` and `Wait` return at once (the replica's Valkey breaker,
  [architecture.md](architecture.md) R-ARCH-031), and the leader fetches under its in-process
  single-flight only (`reason="valkey_unavailable"`): at most one upstream fetch per replica and
  flight. No request fails because Valkey failed (INV-12).
- **The lock is an optimization, not a correctness condition** (INV-11). A lock lost in a Sentinel
  failover, a bypass and an unavailable Valkey all lead to concurrent fetches of one entry, and
  these are correct: blob commits are idempotent by content address, and the entry upsert keeps the
  newer `fetched_at` for metadata and refuses a different digest for an artifact (R-REPO-041).
  - Such duplicates are the normal degraded mode now, not a rare race, so the losing side is
    specified. A leader whose artifact upsert is refused because another flight committed a
    different digest MUST abort its own client before the withheld tail, as on an integrity
    failure (R-REPO-041). It MUST serve its followers and waiters from the committed entry and
    write the security event `proxy.upstream_digest_changed`.
  - No client completes a download whose bytes differ from the stored artifact.
- **Detached fetch.** The leader's fetch runs detached from its initiating client: a client that
  disconnects does not abort a fetch others wait for. The leader's own client MAY receive the body
  while it is fetched, under the withheld-tail rule of R-REPO-041; followers and waiters are served
  from the stored entry.
- **Shutdown and drain** (Q-REPO-7). From `SIGTERM` on, a replica leads no new cross-replica
  flight.
  - A request on a draining replica joins a flight that another replica holds, as a waiter (steps 4
    and 5), but never calls `Acquire`. When no other replica holds the flight, it fetches under its
    in-process single-flight only (`reason="draining"`).
  - Fetches it already leads keep running detached and commit as usual. Those still running 5 s
    before the shutdown deadline are cancelled and released with `failed`
    ([architecture.md](architecture.md) R-ARCH-024 step 4), so waiters on other replicas take over
    at once.
  - A cancelled fetch leaves no entry: its spool file is the pod's own scratch space (INV-13), and
    no other replica continues a partial body.
- **Populate audit** ([authorization.md](authorization.md) R-AUTHZ-029). Only the request that
  fetches from the upstream writes the transactional populate event (`authz.decision`, allowed
  populate), in its cache-fill transaction (step 5); if the event cannot be written, the fill
  fails. A request that joins a flight, in-process (step 2) or as a waiter through the Valkey
  flight lock (step 4), fetched nothing and writes none; its read is audited as a read. A request
  that goes on without the cross-replica lock (`valkey_unavailable`, `wait_timeout`, `draining`)
  fetches and writes one. The number of populate events therefore follows the number of upstream
  fetches, not the number of replicas or concurrent requesters.
- Every response served from a collapsed flight carries `collapsed` in `Cache-Status`.
- The protocol opens no PostgreSQL connection of its own, so it adds nothing to the connection
  budget of [ha-and-helm.md](ha-and-helm.md) R-HA-016.

Configuration:

```yaml
proxy:
  lock_poll_interval_ms: 1000      # default (proposed)
  fetch_wait_timeout_seconds: 120  # default (proposed)
# The lock's TTL is cluster.flight_lock_ttl (30 s, architecture.md R-ARCH-033).
```

Acceptance criteria (two replicas, counting stub upstream that delays every response by 2 s):

- 100 concurrent requests for the same uncached tarball, 50 per replica: the stub records exactly
  one `GET`; all 100 responses are 200 with identical bytes; at least 99 carry `collapsed` or `hit`;
  exactly one allowed populate event exists for the tarball's path. Meanwhile `pg_locks` shows no
  advisory lock of category 2 (`fetch`, retired, [architecture.md](architecture.md) R-ARCH-017) and
  none held by a connection of class `main` (R-ARCH-019), and `pg_stat_activity` shows no trove
  connection `idle in transaction`.
- The stub streams a 200 MiB artifact slowly, and the leader's pod is killed with `SIGKILL`
  mid-transfer. A waiter on the other replica acquires the flight within `cluster.flight_lock_ttl`
  plus `proxy.lock_poll_interval_ms`, fetches, and completes every waiting request. No entry for a
  partial body exists at any time.
- The same with `kubectl delete pod` and a transfer that outlasts the leader's shutdown deadline:
  the waiter acquires within `proxy.lock_poll_interval_ms` of the leader's `failed`, and the
  leader's own client, retrying through the Service, is served by the other replica.
- A request reaches a replica during its drain delay for a path that no flight holds: no lock key
  of that replica appears in Valkey (`valkey-cli GET trove:<ks>:lock:fetch:v1:<f>`), the request is
  served, and `trove_flight_lock_bypass_total{reason="draining"}` increments.
- With Valkey stopped, all 100 requests of the first criterion succeed, and the stub records at
  most one `GET` per replica.
- With every message on the outcome channels dropped by a fault proxy, the first criterion still
  records exactly one `GET`, and the waiters complete within the stub's delay plus one poll
  interval.
- 100 concurrent requests, 50 per replica, for a path the stub answers with 404: the stub records
  exactly one `GET`, and all 100 responses are 404.
- A lock key written by hand with a foreign token, and `proxy.fetch_wait_timeout_seconds: 5`: a
  request for that path is served after about 5 s, and
  `trove_flight_lock_bypass_total{reason="wait_timeout"}` increments.
- The same foreign lock key, written without expiry, on an expired metadata document, with
  `metadataRefreshTimeoutSeconds: 2` and `proxy.fetch_wait_timeout_seconds: 5`: the request is
  served the stale document after about 2 s, the stub records one conditional request about 5 s
  after the request arrived, and the next request is a fresh hit.
- With Valkey stopped, the stub serves different bytes, and no announced digest, to two concurrent
  fetches of one artifact on two replicas: exactly one cache entry exists, every completed client
  response matches it, and a `proxy.upstream_digest_changed` event exists.
- A test over `internal/proxy` fails on an import of the Valkey client and on any `pg_advisory*` or
  `pg_try_advisory*` call; the proxy reaches the lock only through `cluster.FlightLock`.

### R-REPO-027 · Metadata freshness and refresh timeout
Priority: **MVP** · Covers: RMT-018, RMT-019

- A metadata entry is fresh for `metadataTtlSeconds` after `validated_at`; `0` means revalidate on
  every request. The upstream's own `Cache-Control` is ignored for this decision, so behavior follows
  the configuration alone.
- Revalidation MUST be conditional when the entry has validators: `If-None-Match` with the stored
  ETag, else `If-Modified-Since` ([RFC 9110, section 13](https://www.rfc-editor.org/rfc/rfc9110#section-13)).
  A 304 updates `validated_at` and `expires_at` without a body transfer. Because revalidation is a
  header exchange, collapsed across replicas (R-REPO-026) and bounded by the fetch limits
  (R-REPO-040), a period of `0` MUST be accepted; there is no minimum, unlike the catalog's 30
  seconds (Q-REPO-2).
- An entry validated after a request first looked it up counts as fresh for that request, whatever
  the period: its `validated_at` differs from the value the request read in R-REPO-025 step 4, or
  the entry was absent then. Both values come from PostgreSQL, so no replica clock takes part, and
  a waiter on a replica whose clock runs ahead is served like any other. Collapsed followers and
  cross-replica waiters that re-read the entry (R-REPO-026 steps 2 to 4) are therefore served by
  the flight they waited for; without this rule a period of `0` would make every waiter fetch
  again after the holder released the lock.
- If a refresh has not completed within `metadataRefreshTimeoutSeconds`, the stale entry MUST be
  served (`Cache-Status: trove; fwd=stale; detail=served-stale`); the refresh continues in the
  background, bounded by the read timeout, and updates the entry when it completes. This is the
  stale-if-error behavior of [RFC 5861](https://www.rfc-editor.org/rfc/rfc5861) applied to slow
  upstreams. The background refresh is the flight's fetch and keeps its lock (R-REPO-026). No later
  request depends on it: one that arrives before it completes finds the entry still expired and
  joins the flight or starts a new one, on whichever replica it reaches. On shutdown it is
  cancelled like any other fetch ([architecture.md](architecture.md) R-ARCH-024 step 4) and stores
  nothing.
- Format defaults: npm 7200 s; NuGet 300 s for the service index, version lists, registrations
  and vulnerability data ([formats/nuget.md](formats/nuget.md) Q-NUGET-2). The not-found default
  stays 1800 s for both formats (R-REPO-028).

Acceptance criteria (`metadataTtlSeconds: 60`):

- Two `npm view lodash` within 60 s: one upstream request. After 61 s: one conditional request with
  `If-None-Match`; the stub answers 304; the response carries `Cache-Status: trove; fwd=stale;
  fwd-status=304`.
- The stub delays the document by 90 s: the client receives the stale document after about 60 s;
  once the stub has answered, the next request is a fresh hit with the new content.
- With `metadataTtlSeconds: 0`, a `PATCH` is accepted; each of five sequential `npm view lodash`
  causes one conditional upstream request, and 20 concurrent ones, 10 per replica on two replicas,
  while the stub delays its answer by 1 s, cause one.

### R-REPO-028 · Negative cache
Priority: **MVP** · Covers: RMT-020

- An upstream 404 or 410 for (R, path, variant) MUST be remembered for `notFoundTtlSeconds`; during
  that period requests answer 404 without contacting the upstream. `0` disables negative caching. No
  other status is cached negatively (R-REPO-029). Only requests that passed the cache-fill check
  reach the upstream, so only they create entries.
- **Where entries live** ([architecture.md](architecture.md) Q-ARCH-16). A negative entry is shared
  ephemeral state, not a record (INV-3).
  - It is the Valkey key `trove:<ks>:neg:v1:<repo id>:<f>`, with `<f>` as in R-REPO-026, and the
    value `{"s": <404 or 410>, "c": <time the upstream request was sent, Unix ms>}`
    ([architecture.md](architecture.md) R-ARCH-030).
  - It is written with `SET … PX <notFoundTtlSeconds × 1000>` after the upstream has answered,
    outside every database transaction, and before the flight's holder releases the flight, so
    waiters on other replicas find it (R-REPO-026 steps 4 and 5).
  - Each replica keeps a bounded L1 copy of the entries it wrote or read
    (`proxy.negative_l1_entries`, least recently used out). A lookup asks the L1 first, then
    Valkey, and copies a Valkey hit into the L1.
  - Until 2026-10-28 negative entries were rows of the PostgreSQL table `remote_negative_entries`,
    with a cleanup job. Both are gone: the entries are small, written on the hot path and disposable,
    which is the state INV-3 assigns to Valkey.
- **Readers validate.** An entry counts only while all of the following hold:
  - `c` lies between now minus the repository's current `notFoundTtlSeconds` and now plus 5 s;
  - no invalidation of scope `negative` or `all` that covers its path (R-REPO-045) was created
    later than `c` minus 5 s.

  A malformed entry is ignored and deleted, and the key's own expiry is not trusted. The 5 s are the
  clock allowance of R-ARCH-030, applied in the invalidation's favour: an entry written in the
  seconds after an invalidation costs one more upstream request, while an entry from before it
  never survives it as long as replica and database clocks differ by less than 5 s. Because `c` is
  the time the request was sent, an upstream answer that crossed an invalidation does not survive
  it either.
- **Refusal only.** A negative entry can make trove answer 404 and nothing else. Nothing on this page
  admits a write, grants access or serves content because of one (R-ARCH-030). In particular, the
  shadowing check does not read negative entries (R-REPO-048).
- **Valkey unavailable**: negative caching continues per replica in the L1 alone, so each replica
  asks the upstream at most once per path and period ([ha-and-helm.md](ha-and-helm.md) R-HA-057).
  **Valkey lost its data**: one more upstream request per path. Neither case changes an answer that
  the upstream would give.
- Negative entries are never deleted to invalidate them: invalidations take effect in the readers
  (R-REPO-045), and a change of the upstream identity is such an invalidation (R-REPO-011). The
  entries of a deleted repository expire on their own; repository ids come from an identity column
  and are never handed out again (R-REPO-013).
- Requests refused by patterns or claims never reach the upstream and create no negative entry.
- Negative entries are not part of a backup. A restore starts without any, because it moves the
  installation to a new keyspace ([backup-and-restore.md](backup-and-restore.md) R-BAK-022).

Configuration:

```yaml
proxy:
  negative_l1_entries: 100000  # default (proposed), per replica; about 10 MiB
```

Acceptance criteria:

- `npm view does-not-exist-4711` twice through `npm-remote`: one upstream request; the second
  response carries `Cache-Status: trove; hit; detail=negative`. With two replicas, the second request
  on the other replica causes no upstream request either.
- The stub then publishes the package; within the period it stays invisible; after an invalidation
  with scope `negative` (R-REPO-045), the next request on either replica reaches the stub and
  succeeds.
- With Valkey stopped, ten sequential requests for the missing path, five per replica, cause at most
  two upstream requests.
- `valkey-cli FLUSHALL` after the first request: the next request reaches the stub once and answers
  404.
- A negative entry written into Valkey with a creation time older than an invalidation of its path
  is ignored, and the request reaches the upstream.

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
- **Upstream gone** (Q-REPO-3). When the upstream answers 404 or 410 for a metadata document that
  is stored, the entry MUST be marked `upstream_gone_at` instead of being deleted, a negative entry
  is written and the audit event `proxy.upstream_gone` is emitted on the first such answer. The
  document then answers 404 on the remote address, on its cache view and as a contribution to
  virtual merges. A later 2xx for the document clears the mark.
- **Gone packages.** When the marked document is the package document of package n (the path the
  format registers, R-REPO-003), n is gone from R: every request to R whose descriptor names n
  MUST answer 404, cached artifacts included, on R's address, on its cache view and as a member of
  every virtual repository (cache tier and upstream tier alike), and R asks its upstream for
  nothing of n but the package document (R-REPO-025, gone packages). Cached items are not deleted:
  they stay listed in the management item API and the cache entry list (R-REPO-032), where an
  operator can delete them or copy them into a local repository, which is how a build keeps a
  package the upstream withdrew legitimately. A 2xx for the package document clears the mark and
  serves everything again.
- Each replica MUST hold the gone packages of every remote repository in memory (they are few),
  loaded at start from the marked package-document entries. The marking or clearing transaction
  calls `cluster.Notify("upstream_state", …)` with the repository's id
  ([architecture.md](architecture.md) R-ARCH-018), and every replica then reloads that repository's
  gone packages; a replica whose change log has a gap reloads all of them. Serving a cached artifact
  therefore costs no extra query, and a mark reaches every replica within 2 s, with or without
  Valkey. `upstream_state` is a change area, not a signal area ([architecture.md](architecture.md)
  R-ARCH-018): gone marks and circuit transitions are rare, and only a change area keeps the 2 s
  bound while Valkey is unavailable.
- A format that tracks removal per version replaces both rules with its own: npm hides removed
  versions and their tarballs, and gone packages, unless `npm.serveRemovedVersions` is on
  ([formats/npm.md](formats/npm.md), R-NPM-026).
  - npm reads the core's `upstream_gone_at` of the package document's cache entry in the lookup
    that serves each request, and keeps no copy of it: neither the in-memory gone packages above
    nor a mark of its own.
  - npm serves a cached artifact only after its package document is current: unexpired, and
    validated after `system_state.restored_at`; otherwise the request revalidates the document
    first, in one flight ([formats/npm.md](formats/npm.md) R-NPM-025, Q-NPM-13; R-REPO-025 step 4).
    A lockfile install, which never requests documents, therefore sees a takedown within one
    metadata period, and while the upstream is reachable a restore cannot make a version servable
    again that the upstream withdrew after the snapshot.

  Formats that do not track versions leave a version withdrawn from a package that still exists
  reachable by exact path (H-5).
- **Circuit.** Each remote repository has a row in `remote_upstream_state`, in PostgreSQL, because
  every replica must take the same decision ([architecture.md](architecture.md) R-ARCH-030).
  - A counted failure increments `consecutive_failures` atomically (`UPDATE … RETURNING`), and only
    while the circuit is closed (`WHERE open_until IS NULL OR open_until <= now()`). At
    `failureThreshold`, `open_until` is set to now plus `circuitOpenSeconds`.
  - The writing replica takes the state from `RETURNING` at once. An `UPDATE` that returns no row
    found the circuit already open: that replica reads `open_until` and holds the open state at
    once, without waiting for the change log. The opening and the closing transaction call
    `cluster.Notify("upstream_state", …)` with the repository's id, so every replica holds the
    state in memory within 2 s, with or without Valkey, and reloads all of it after a gap in its
    change log. A burst of failures on many replicas therefore stops writing as soon as each replica has
    seen the opening, at the latest after its first statement that matches no row. The number of
    counted failures in flight at once is bounded by the fetch budget of R-REPO-040 in any case.
  - While open, no request reaches the upstream (R-REPO-025 step 7). After `open_until`, the first
    request on each replica probes, and the replica's other requests answer as while open until
    that probe ends; success closes the circuit for all replicas, failure reopens it. With n
    replicas, at most n probes run per period.
  - A success writes only when the counter is non-zero, so healthy traffic causes no writes.
    `circuitOpenSeconds: 0` disables the circuit.
- Any success resets the counter, so a flaky link cannot keep a repository offline. The catalog's ten
  background retries with growing pauses are replaced by this threshold and probe.

Data model:

```sql
CREATE TABLE remote_upstream_state (
  repo_id              bigint PRIMARY KEY REFERENCES repositories (id) ON DELETE CASCADE,
  consecutive_failures integer NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
  open_until           timestamptz,
  last_failure_at      timestamptz,
  last_failure         text CHECK (length(last_failure) <= 1024),
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
- `nuget-remote` has `Acme.Gone` `1.0.0` cached; after its period, the stub answers 404 for the
  version list: `GET …/v3/flatcontainer/acme.gone/index.json` and `GET
  …/v3/flatcontainer/acme.gone/1.0.0/acme.gone.1.0.0.nupkg` return 404 through `nuget-remote`,
  through `nuget-remote~cache` and through `nuget-all` on both replicas within 2 s; the stub records
  no request for the `.nupkg`; a `proxy.upstream_gone` event exists; and the `.nupkg` item is
  still listed by the management item API.
- The stub then serves `Acme.Gone` again. After the negative period, a request for the `.nupkg`
  alone causes one stub request for the version list, clears the mark and is served.

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
- The runtime switch is a database-backed setting with a revision ([api-and-ui.md](api-and-ui.md)
  R-API-012). The changing transaction calls `cluster.Notify("upstream_state", …)`
  ([architecture.md](architecture.md) R-ARCH-018), so the switch is effective on all replicas
  within 2 s, with or without Valkey. It is the kill switch for an incident such as a compromised upstream, which is
  why it does not wait for a configuration rollout, and why its bound must not depend on Valkey.
- `GET` returns `ETag: "<revision>"`, and `PUT` requires `If-Match` (R-API-012, Q-API-2): 428
  `precondition_required` without it, 412 `precondition_failed` when stale. The guard matters most
  when the switch is turned off: an administrator working from a view loaded before a second
  incident cannot end the new one unnoticed. To turn the switch on in an incident without a
  preceding `GET`, a script sends `If-Match: *`, the explicit blind write; the UI never does
  (R-UI-042).

| Method and path | Body | Response | Permission |
|---|---|---|---|
| `GET /api/v1/system/proxy-offline` | — | `{"enabled": false, "fromConfiguration": false, "reason": "", "changedAt": …, "changedBy": …}`, `ETag: "<revision>"` | `settings:read` |
| `PUT /api/v1/system/proxy-offline` | `{"enabled": true, "reason": "upstream incident 2026-10-28"}`, `If-Match` required | 200 with the new `ETag`; 409 `offline_by_configuration`; 412 `precondition_failed`; 428 `precondition_required`; audit `proxy.global_offline_changed` | `settings:write` |

Data model (one row; it arrives with this requirement):

```sql
CREATE TABLE proxy_offline_switch (
  singleton  boolean     PRIMARY KEY DEFAULT true CHECK (singleton),
  enabled    boolean     NOT NULL DEFAULT false,
  reason     text        NOT NULL DEFAULT '',
  revision   bigint      NOT NULL DEFAULT 1,      -- ETag (R-API-012); revision column (R-BAK-022)
  updated_at timestamptz NOT NULL DEFAULT now(),  -- changedAt
  updated_by uuid                                 -- changedBy
);
```

`revision` MUST be registered with `meta.RegisterRevisionColumn`, so a restore raises it above
every `ETag` the old installation can have issued ([backup-and-restore.md](backup-and-restore.md)
R-BAK-022). Otherwise a `PUT` carrying an `ETag` read after the snapshot could pass `If-Match`
against the restored switch, which may hold a different state, and turn it off unnoticed.

Configuration:

```yaml
proxy:
  global_offline: false  # default (proposed)
```

Acceptance criteria:

- `curl -X PUT -H "Authorization: Bearer $ADMIN_TOKEN" -H 'Content-Type: application/json' -H 'If-Match: *' -d '{"enabled":true,"reason":"upstream incident 2026-10-28"}' https://trove.example.com/api/v1/system/proxy-offline`:
  within 2 s no replica sends upstream requests, also with Valkey stopped; cached installs still
  succeed, and `GET` reports `enabled: true` with the reason and a new `ETag`.
- A `PUT` with `enabled: false` and the `ETag` read before that change answers 412 and the switch
  stays on; the same `PUT` without `If-Match` answers 428.
- With `proxy.global_offline: true` in the configuration, a `PUT` with `enabled: false` and the
  current `If-Match` returns 409 `offline_by_configuration`.

### R-REPO-032 · Cache view and cached content
Priority: **MVP** · Covers: RMT-003

- `<key>~cache` addresses the cache view of remote repository `<key>` on the protocol and raw-file
  paths (R-REPO-002). It MUST serve stored entries only, never contact the upstream, ignore expiry
  and negative entries, and answer 404 for anything not stored, for documents marked upstream
  gone and for every entry of a gone package (R-REPO-029). It needs `read` on the remote
  repository and is the address for clients that must not trigger upstream checks.
- A request to the remote address with `Cache-Control: only-if-cached` MUST be answered from storage
  or with 504, as [RFC 9111, section 5.2.1.7](https://www.rfc-editor.org/rfc/rfc9111#section-5.2.1.7)
  prescribes.
- Cached artifacts are items: they are listed, carry their upstream URL, can be deleted (permanently,
  no trash can), annotated and copied to a local repository through the item operations of
  [storage.md](storage.md) (R-STOR-018 to R-STOR-020), for example to keep a package that
  disappeared upstream. Items of a gone package (R-REPO-029) stay listed and copyable; only
  serving them to clients stops.
- Metadata entries are listed for diagnosis by `GET /api/v1/repositories/{key}/cache/entries?pathPrefix=&kind=&limit=&cursor=`
  (path, variant, kind, fetched, validated, expires, upstream gone, last used, upstream URL), in the
  `items` and `nextCursor` envelope of [api-and-ui.md](api-and-ui.md) R-API-010, in the byte order of
  path and variant; permission `repositories:read-config` or `manage` on the repository, the
  exposure ceiling of the endpoint binding both. `kind` is `metadata` (the default) or `artifact`,
  `pathPrefix` limits the list to the entries that an invalidation of that prefix would cover
  (R-REPO-045), and a repository that is not remote answers `400 not_applicable` (Q-OPS-155).

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
  true`; redirects follow R-REPO-039. A format page MAY forbid more; neither MVP format does
  ([formats/npm.md](formats/npm.md) R-NPM-023 restates this rule, and the NuGet format proposes
  its `*.nuget.org` extra host with `sendCredentials: false`).
- **Secret fields** (`password`, sensitive header values, client-certificate private keys, outbound
  proxy passwords) take one of:
  - `{"value": "…"}`: accepted through the API only, never in startup files
    ([R-OPS-029](operations-and-observability.md)); sealed at once;
  - `{"secretRef": {"file": "<name>"}}` or `{"secretRef": {"env": "TROVE_CAC_<NAME>"}}`: resolved
    on every replica at use time under the rules of R-OPS-029 (a file directly under
    `config_as_code.secrets_dir`, only variables with the `TROVE_CAC_` prefix), so trove's own
    secrets can never be routed to an upstream. A changed file takes effect on each replica within
    10 s of the change, which keeps R-REPO-033's bound of 60 s (Q-REPO-89). The file content has its
    trailing line break removed and holds at most 8 KiB.
- Secret values MUST never be returned by any API, log line, audit event or error message. Reads show
  `{"set": true}` for sealed values and the reference itself for references. Clients that re-submit a
  document they read therefore cannot blank or leak a secret: `{"set": true}` keeps the stored value,
  an omitted or `null` secret removes it, and `{"set": true}` for a repository that has no stored
  value is 422 `invalid_secret` (Q-REPO-88). A new value reads like the old one, so a write that
  carries one moves `config_version` all the same: the clients built with the old value are replaced
  and the `ETag` changes.
- **Sealing** (Q-REPO-1). Inline values MUST be sealed with the one stored-secret mechanism of
  [architecture.md](architecture.md) R-ARCH-021 (IAM-045; the chart side is
  [ha-and-helm.md](ha-and-helm.md) R-HA-027). This page defines no key, format or rotation of its
  own. It relies on these properties of R-ARCH-021 and its tests check them for repository
  secrets: AES-256-GCM with a fresh 96-bit random nonce per value; associated data binding the
  ciphertext to table, primary key (here `repo_id` and `field`) and column, so a sealed value
  copied into another row or field does not decrypt; a key identifier in the sealed form, so keys
  rotate by re-sealing (the `secrets-reencrypt` task) while old keys still decrypt.
- A secret that cannot be unsealed or resolved makes fetches of that repository fail with 502
  `upstream_secret_unavailable`, before any request is sent, and is logged when the repository's
  client is built; other repositories are unaffected. A replica does not check every repository at
  start-up ([H-280](../security/data-and-secret-flow.md#h-280--a-secret-that-cannot-be-resolved-is-found-at-its-first-use)); the upstream
  test reports it at any time.
- The walk of the task `secrets-reencrypt` over this column reseals each value under the active key with the same row key, and counts a value that does not open as failed without touching it (`repository.ResealSecrets`).
- URLs in logs and audit events are written without user info and with query values redacted.

Data model:

```sql
CREATE TABLE repository_secrets (
  repo_id    bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
  field      text NOT NULL CHECK (field ~ '^/[A-Za-z0-9/._~-]{1,255}$'),  -- JSON pointer, e.g. '/remote/password', '/remote/headers/X-Api-Key'
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
- Copying the `sealed` value of `npm-partner`'s `/remote/password` row into the row of another
  repository makes that repository's fetches fail with 502 `upstream_secret_unavailable`.
- `{"secretRef": {"env": "TROVE_DATABASE_PASSWORD"}}` is refused with 422 `invalid_secret`.
- With `authMode: bearer`, the stub records `Authorization: Bearer <token>` and no Basic header.
- `PATCH` with `{"remote": {"password": {"value": "new"}}}` moves `config_version` and the next
  request, on every replica, carries the new token; `{"remote": {"password": null}}` removes the
  row; sending back a document that was read changes nothing.
- A `secretRef` file that disappears after the client was built fails the repository's next fetches
  with 502 `upstream_secret_unavailable` and sends no request; it works again within the refresh
  interval after the file is back.

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
  open connections are not torn down. The reload is built in M0: the outbound clients read their trust
  store again at most every 10 s on use (`DefaultTrustCheckEvery`), so the bound holds with margin, and the
  object store's `storage.s3.ca_file` takes the same path ([R-STOR-030](storage.md)). The mechanism is
  described in [security/transport-encryption.md](../security/transport-encryption.md).
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
  169.254.169.254 cloud metadata endpoints), the IPv6 metadata address `fd00:ec2::254`, the metadata
  addresses `100.100.100.200` and `168.63.129.16`, the deprecated IPv4-compatible range `::/96`, multicast and
  broadcast, and the IPv4-mapped IPv6 forms of all of these (ranges per the IANA
  [IPv4](https://www.iana.org/assignments/iana-ipv4-special-registry/) and
  [IPv6](https://www.iana.org/assignments/iana-ipv6-special-registry/) special-purpose registries).
- Before any CIDR rule runs: a zoned IPv6 address (`fe80::1%eth0`, `::1%1`, any address with a zone) is refused,
  whatever `allow_cidrs` says, because a zone scopes an address to an interface of this host and no prefix
  contains a zoned address ([Q-REPO-10](#q-repo-10--zoned-ipv6-and-the-nat64-prefix-in-the-egress-rules)).
  An IPv4-mapped IPv6 address is checked as the IPv4 address it maps to. An address of the NAT64 prefix
  `64:ff9b::/96` that no rule decides is checked as the IPv4 address in its last 32 bits, so
  `64:ff9b::a9fe:a9fe` is refused like `169.254.169.254`. The NAT64 prefix is the well-known one; a
  network-specific prefix is not checked ([H-78](../security/egress.md#h-78--the-nat64-check-knows-only-the-well-known-prefix)).
- Private ranges ([RFC 1918](https://www.rfc-editor.org/rfc/rfc1918)), shared address space and
  unique-local IPv6 are permitted by default, unlike for webhooks
  ([R-OPS-042](operations-and-observability.md)): remote URLs are set by repository administrators,
  and internal registries are the common case. The Helm chart's egress `NetworkPolicy` would be the
  second layer ([ha-and-helm.md](ha-and-helm.md)); the chart renders none yet
  ([H-41](operations-and-observability.md#h-41--egress-is-unrestricted-and-ingress-enforcement-is-verified-on-one-cni-only)).
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
        - ::/96
        - 100.100.100.200/32
        - 168.63.129.16/32
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
- The client certificate follows the same rule as the credentials: it is presented only on connections to
  those hosts. The first request to a host that is neither the upstream origin nor a `sendCredentials`
  host never carries credentials, also with `forwardAuthOnRedirect`, which widens redirect hops only
  (Q-ARCH-199).
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
- **Fetch slots.** Two limits apply to every upstream fetch, shadowing-check probes included
  (R-REPO-048):
  - `proxy.max_concurrent_fetches` protects the replica itself and counts per replica.
  - `limits.maxConcurrentFetches` protects the upstream. It is a budget for the whole installation
    (Q-REPO-8), divided among the replicas without coordination between them. Each replica allows
    `ceil(maxConcurrentFetches ÷ n)` fetches of the repository at a time. n is the number of live
    replicas that serve client traffic (`web`, or `all` in the local profile) in the replica registry
    ([operations-and-observability.md](operations-and-observability.md) R-OPS-003), at least 1,
    refreshed with the registry heartbeat; it is the count the rate-limit fallback of
    [architecture.md](architecture.md) R-ARCH-034 uses.
  - The cluster-wide total therefore stays within `maxConcurrentFetches + n − 1` whatever the
    replica count, once every replica has read the current n, which after a scale event takes one
    heartbeat. A budget smaller than n admits one fetch per replica. Valkey plays no part, so the
    bound holds while Valkey is unavailable.
- A slot is taken by the request that fetches, after it holds the flight or has gone on without
  the cross-replica lock (R-REPO-026 step 3), and returned when its fetch ends. Requests that fetch
  outside a flight, shadowing-check probes and pass-through artifact requests (R-REPO-044), take
  theirs directly. In-process followers and cross-replica waiters take none, so a replica's share
  is never spent on waiting for another replica's transfer. A fetch that finds no free slot waits up to
  `proxy.queue_timeout_seconds`, then answers 503 with `Retry-After: 5` and releases its flight with
  `failed`. This bounds the burst a large invalidation can send to the upstream.
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
- With `limits.maxConcurrentFetches: 4` on two replicas and a slow stub, 20 concurrent requests for
  distinct uncached tarballs, 10 per replica, keep at most 4 upstream connections open in total and
  at most 2 per replica. After a third replica has registered, the same load keeps at most 6 open
  (`ceil(4 ÷ 3)` = 2 per replica).
- With `limits.maxConcurrentFetches: 4` on two replicas, replica A leads two slow fetches. Replica B
  receives requests for the same two tarballs and for two other uncached tarballs: B fetches the
  other two at once, without a queue wait, because waiting for A's flights takes no slot.

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
  the final 64 KiB until verification has passed and, when the artifact is stored, until its entry
  has committed: a duplicate flight on another replica may have committed different bytes first,
  and the upsert then refuses this one (R-REPO-026). On a mismatch it MUST abort the client connection
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
  URLs and trusted proxies), which on a further endpoint is that endpoint's response base URL
  ([endpoints.md](endpoints.md) R-EP-003). The stored upstream document stays unchanged, so a base URL change needs
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
  with 502 `foreign_host_denied` without a request. `remote.extraHosts` is the only host list;
  formats have none of their own and may only narrow it (Q-REPO-5).
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
- A format whose plug-in requires stored artifacts refuses the setting with 422 `validation_failed` with `not_applicable` in `errors[]`.
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
  segments: `@acme` covers `@acme/util` but not `@acmecorp/util`. A prefix is empty or a sequence of
  segments without a leading or trailing slash, without an empty segment, without `.` or `..` as a
  segment, valid UTF-8 without control characters, and at most 256 bytes; anything else answers `422
  validation_failed` with the pointer `/path` (Q-REPO-100).
- **Mechanism.** An invalidation is a row (repository, prefix, scope, time). Its time is taken inside
  the inserting transaction as its first statement, not at the start of the transaction (Q-REPO-101).
  A metadata entry is invalid when an invalidation of its repository with a covering prefix and a
  matching scope is newer than its `validated_at`. A negative entry is invalid under the rule of R-REPO-028: its time is its
  creation time `c`, compared with a 5 s allowance in the invalidation's favour.
  - Each replica keeps the invalidations in memory, loaded at start. The inserting transaction
    calls `cluster.Notify("cache_invalidation", …)` with the repository id
    ([architecture.md](architecture.md) R-ARCH-018), so an invalidation is in force on every
    replica within milliseconds with Valkey and within `cluster.epoch_poll_interval` without.
    The change carries the whole row, so a replica applies it without a query, and a request
    asks the in-memory set only: the cost of a lookup is the number of segments of the path,
    whatever the number of rows (Q-REPO-102; the payload: Q-OPS-154).
    Until 2026-10-28 this was `NOTIFY trove_cache_invalidation`; trove issues no `NOTIFY` any
    more ([architecture.md](architecture.md) Q-ARCH-15).
  - A background job on the `worker` role later folds each invalidation into the metadata entries
    validated before it, in batches, setting `expires_at` to the invalidation time and, when the
    last batch is done, `folded_at` on the row (Q-REPO-87). An entry
    refreshed after the invalidation keeps its own expiry, and no entry gets a later expiry than it
    had. Invalidating a million entries is therefore one insert, not a long `UPDATE`. The task is
    `proxy-invalidation-fold` ([operations-and-observability.md](operations-and-observability.md)
    R-OPS-019, Q-OPS-153); Q-REPO-103 has the batching and the last check before a deletion.
  - Negative entries live in Valkey and are never deleted to invalidate them. Readers apply the
    invalidation instead, so a row of scope `negative` or `all` MUST stay until no negative entry it
    covers can still count: 86,405 s after its creation, the largest `notFoundTtlSeconds` plus the
    clock allowance. A row of scope `metadata` is deleted once it is folded and one hour has passed
    since the fold, after one more fold pass that finds an entry a fill stored late (Q-REPO-103).
  - The job deletes a row of scope `negative` or `all` once it is folded and that period has
    passed (after the same last fold pass). Its deletions call `cluster.Notify("cache_invalidation", …)` with the repository id
    too, so every replica drops deleted rows from memory. The job deletes no negative entry: they
    are in Valkey and expire on their own (R-REPO-028). A replica never drops a row the database still
    holds: an `all` row whose fold is late would otherwise let unfolded metadata entries count as
    fresh again. The in-memory set therefore stays bounded by the rows in PostgreSQL, however often
    invalidations are made.
  - The `invalidated` count is the number of covered metadata entries at the time of the call, those
    marked upstream gone included; negative entries in Valkey are not counted, because counting them
    would need a scan of the keyspace, so the scope `negative` answers 0 (Q-REPO-101).
- Each entry is refreshed on its next request under R-REPO-027. A failed refresh serves the stale
  copy, which can show an old package list; the UI therefore shows the circuit state next to the
  result and tells the operator to verify the refresh.
- On a virtual repository, the merged entries under the prefix are dropped (R-REPO-020), on every
  replica through `cluster.Notify("cache_invalidation", …)` with the virtual repository's id and
  the prefix, as for a reindex (R-REPO-016); on a local repository the call answers 400.

Data model:

```sql
CREATE TABLE remote_cache_invalidations (
  id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  repo_id    bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
  prefix     text COLLATE "C" NOT NULL,   -- '' = whole repository
  scope      text NOT NULL CHECK (scope IN ('metadata', 'negative', 'all')),
  created_at timestamptz NOT NULL DEFAULT now(),
  created_by uuid,
  folded_at  timestamptz                  -- set by the fold job (Q-REPO-87)
);
CREATE INDEX remote_cache_invalidations_repo ON remote_cache_invalidations (repo_id);
CREATE INDEX remote_cache_invalidations_unfolded ON remote_cache_invalidations (created_at) WHERE folded_at IS NULL;
```

Acceptance criteria:

- With 1,000,000 cached metadata entries, an invalidation with an empty path returns within one second,
  and the next `npm view` of any cached package causes a conditional upstream request on every
  replica. With Valkey stopped, the same holds for requests that start `cluster.epoch_poll_interval`
  plus 1 s after the call returned.
- With `notFoundTtlSeconds: 86400`, a negative entry for `@acme/late` exists, and `@acme` is
  invalidated with scope `negative`. After the background job has run and a test clock has moved one
  hour ahead, the entry still does not count, and the next request reaches the upstream.
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
  writes. No request reads that buffer; it is flushed at shutdown, and a crash loses at most one
  batch, which the entry's next use records again (R-REPO-049).
- To keep an item permanently, copy it to a local repository.

Acceptance criteria:

- With `unusedRetentionHours: 1`, a tarball last downloaded two hours ago through `npm-all` is
  removed by a cleanup run and fetched from the upstream again on the next install; a tarball
  installed ten minutes ago stays.

## Behavior across replicas

### R-REPO-049 · Repository and proxy state on n replicas, without Valkey and during a drain
Priority: **MVP** · Covers: trove-only

The Kubernetes Service hands every request to a random `web` replica, without session affinity
(INV-12; kube-proxy "select[s] a backend Pod at random" by default,
[virtual IPs](https://kubernetes.io/docs/reference/networking/virtual-ips/)). Valkey may be
unavailable or lose its data at any time (INV-3), and any replica may be drained (INV-12). This
requirement lists every piece of state that this page keeps outside PostgreSQL and S3 and fixes how
it behaves in each of these cases. A new kind needs a row here, and in
[architecture.md](architecture.md) R-ARCH-032, before it ships.

| State | Where | Across replicas | Valkey unavailable | Valkey lost its data | Drain or crash of the replica |
|---|---|---|---|---|---|
| Repository snapshot, claims and pin rules, gone packages, circuit state, global offline switch, cache invalidations (R-REPO-008 to R-REPO-023, R-REPO-029, R-REPO-031, R-REPO-045) | In-process copies of PostgreSQL rows | Change areas `repo_config`, `claims`, `upstream_state`, `cache_invalidation` of R-ARCH-018: within 2 s of the commit | Within `cluster.epoch_poll_interval` | As unavailable | Nothing to hand over |
| Upstream clients with their connection pools (R-REPO-024, R-REPO-033) | In-process cache `proxy_upstream_clients`, one client per repository id and `config_version` (Q-REPO-89) | Change area `repo_config` evicts the repository's clients; a changed configuration or secret is a new key, so a stale client is not reached | Within `cluster.epoch_poll_interval` | As unavailable | Ends with the process; `secretRef` files are read by each replica itself, 10 s apart |
| Cache entries, gone marks, URL mappings, stale copies (R-REPO-025, R-REPO-029, R-REPO-043) | PostgreSQL and S3 | One copy, read by every replica | Unaffected | Unaffected | Unaffected |
| Negative entries (R-REPO-028) | Valkey, plus a bounded L1 per replica | Shared through Valkey | L1 only: one upstream request per replica, path and period | One more upstream request per path | L1 ends with the process |
| Flights (R-REPO-026) | In-process single-flight, lock in Valkey | One upstream fetch per entry; waiters re-check entry, circuit and negative entry before they fetch | One fetch per replica and entry | A duplicate fetch | Leads no new flight once draining; releases with `failed` 5 s before the shutdown deadline; after a crash the lock expires within `cluster.flight_lock_ttl` |
| Merged metadata (R-REPO-020), metadata bytes (R-REPO-025) | In-process caches | Self-validating: `rev_seq`, member fingerprint, blob digest; a virtual repository's reindex or invalidation drops its entries through the change area `cache_invalidation` (R-REPO-016, R-REPO-045) | The early-eviction signal is lost; a remote refresh made elsewhere appears when the merged entry expires; a reindex or invalidation applies at the next change-log poll | As unavailable | Ends with the process |
| Fetch slots (R-REPO-040) | Per-replica counters | Shares of a cluster budget, from the replica registry | Unaffected | Unaffected | Ends with the process |
| Activity and use buffers: `lastActivityAt` (R-REPO-012), metadata `last_used_at` (R-REPO-046) | Per-replica, write-only | No request reads them | Unaffected | Unaffected | Flushed at shutdown; a crash loses at most one batch |
| Detached fetches and background refreshes (R-REPO-026, R-REPO-027) | In-process | Visible to others only through the committed entry | Unaffected | Unaffected | Cancelled 5 s before the shutdown deadline; nothing partial stored |

Rules:

- **Valkey grants nothing.** This page reads two kinds of value from Valkey: negative entries, which
  can only make a remote path answer 404 for their checked lifetime, and flight locks, which can
  only delay a fetch or a refresh, by at most `proxy.fetch_wait_timeout_seconds`; a client that
  asked for expired metadata is served the stale copy meanwhile (R-REPO-026 step 4). No value read from
  Valkey admits a write, grants access or is served to a client (R-ARCH-030); R-REPO-048 therefore
  ignores negative entries.
- **No request depends on an earlier one's replica** (INV-18). Every protocol of this page that
  spans requests keeps its state in PostgreSQL:
  - `If-Match` revisions: `config_version`, `claims_version`, the revision of the proxy-offline
    switch (R-REPO-011, R-REPO-023, R-REPO-031), all three registered as revision columns, so a
    restore raises them ([backup-and-restore.md](backup-and-restore.md) R-BAK-022);
  - job references for delete and reindex (R-REPO-013, R-REPO-016);
  - paging cursors ([api-and-ui.md](api-and-ui.md) R-API-010);
  - idempotency keys of batch and quick-setup calls ([api-and-ui.md](api-and-ui.md) R-API-014).

  A client that resumes a cut download with `Range` is served by any replica, because cache entries
  are shared and the ETag is the content address.
- **Bounds hold without Valkey.** Every bound this page promises across replicas holds while Valkey
  is unavailable: 2 s for configuration, claims, circuit, gone packages and the global offline
  switch, and immediate visibility of local publishes through virtual repositories (R-REPO-020).
  Valkey only makes them faster.
- **No shared volume.** A cache fill spools to the pod's own scratch space ([storage.md](storage.md),
  INV-13). No replica continues another replica's partial transfer, and none needs to.
- **Backup.** Every table of this page has the export class `data`
  ([backup-and-restore.md](backup-and-restore.md) R-BAK-004). Negative entries and flight locks are
  in Valkey and are not backed up (INV-19).
  - **Restore without remote caches** ([backup-and-restore.md](backup-and-restore.md) R-BAK-008). A
    backup made with `backup.exclude.remote_caches` holds every table but not the blobs that only
    remote caches reference. Its restore loads everything and then empties each remote
    repository's cache: it deletes the repository's cached items through the storage delete path
    with the trash can bypassed ([storage.md](storage.md) R-STOR-018), so format hooks update
    their indexes and `rev_seq` grows. It deletes the repository's `remote_cache_entries` rows,
    gone marks included, and then the blob rows that are neither referenced nor in the backup's
    pool. The restored remote repositories then behave like freshly created ones with their
    configuration, and the first request for each path fetches from the upstream.
  - Every restore, with or without remote caches, starts without negative entries, and nothing
    has to delete any: they live in Valkey under the old keyspace, which the restored installation
    never reads, and the restore Job holds no Valkey credentials (R-REPO-028, R-BAK-022).
- **Connections.** Nothing on this page holds a PostgreSQL session or a connection across a wait on
  a client, an upstream or Valkey; the proxy's database work is transaction-scoped and runs through
  a transaction-mode pooler ([architecture.md](architecture.md) R-ARCH-019).

Acceptance criteria (e2e profile `scatter` of [architecture.md](architecture.md) R-ARCH-032: three
`web` replicas, every request balanced round robin, the edge's keep-alive to trove off; counting
stub upstream):

- `npm view does-not-exist-4711` three times through `npm-remote`: the stub records one request.
  With Valkey stopped, it records at most three.
- With Valkey stopped, `nuget-remote` has `Acme.Gone` `1.0.0` cached, and after its period the stub
  answers 404 for the version list: within 2 s every replica answers 404 for the cached `.nupkg`
  (R-REPO-029).
- `npm ci` of a fixture with 400 dependencies through `npm-all` on a cold cache, with
  `valkey-cli FLUSHALL` run twice during the install: the install succeeds, and the stub records at
  most three requests for any one path.
- With Valkey stopped for the whole e2e client suite in this profile, the suite passes, and every
  replica stays ready with `"degraded":["valkey"]`.
- `kubectl drain` of the node of one `web` replica during an `npm ci` loop on a cold cache through
  `npm-all`, while the stub delays every tarball by 2 s: every `npm ci` exits `0` with npm's default
  retries, and no cache entry exists for a partial body.

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
| `trove_flight_lock_bypass_total` | counter | `reason`: `valkey_unavailable` ([architecture.md](architecture.md) R-ARCH-033, which defines the metric), `wait_timeout` and `draining` (R-REPO-026). Until 2026-10-28 this row was `trove_remote_lock_fallbacks_total`, for the exhausted lock pool. |
| `trove_remote_source_network_unavailable_total` | counter | `repo` (R-REPO-024: the replica has no address in `sourceNetwork`) |
| `trove_remote_fetch_queue_wait_seconds` | histogram | `repo` |
| `trove_remote_integrity_failures_total` | counter | `repo`, `reason` (`mismatch`, `missing`, `digest_changed`) |
| `trove_remote_egress_denied_total` | counter | `repo` |
| `trove_virtual_resolutions_total` | counter | `repo`, `source` (`local`, `cache`, `upstream`, `none`) |
| `trove_client_certificate_expiry_timestamp_seconds` | gauge | `name` |
| `trove_shadow_checks_total` | counter | `repo` (the local repository), `outcome` (`no-exposure`, `clear`, `shadowing-approved`, `refused`, `undetermined-approved`, `unavailable`; R-REPO-048) |

- Every upstream request writes one record of the `upstream` log
  ([R-OPS-011](operations-and-observability.md)); this page adds the fields `flight_role`
  (`leader`, `follower`, `waiter`) and `cache_status`.
- Audit events (INV-16): `repository.created`, `repository.updated`, `repository.deleted`,
  `repository.cache_invalidated`, `repository.claims_changed`, `proxy.global_offline_changed`,
  `proxy.client_certificate_changed`, `proxy.outbound_proxy_changed`. Security events:
  `proxy.integrity_mismatch`, `proxy.upstream_digest_changed`, `proxy.tls_untrusted`,
  `proxy.egress_denied`, `proxy.upstream_gone`, `repository.remote_package_shadowed` (R-REPO-048).
  Shadowing-check probes write `upstream` log records with `flight_role: probe`.

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
| One local IP address for upstream connections | A network prefix; each replica takes its own address from it (R-REPO-024, Q-REPO-9) | Replicas are interchangeable, and every pod has its own address |

## Security notes

- **Dependency confusion** is closed on the server by namespace claims and pin rules (R-REPO-023),
  evaluated on stored configuration, never on the caller's permissions, and enforced for remote
  repositories addressed directly as well for `global` claims. R-AUTHZ-009 of
  [authorization.md](authorization.md) relies on it for virtual repositories that a caller can
  read only in part. Auto claims stay within the virtual repositories that contain the claiming
  repository, so one publish into a sandbox cannot take a public name away from everyone (H-9).
- **Shadowing a public package is a `manage` decision.** The first publish of a name that a remote
  member of a containing virtual repository serves needs `manage` or a prior manual claim, and fails
  closed when the upstream cannot be asked; a broad `deploy` grant or a leaked CI token can no
  longer replace a public package for every consumer (R-REPO-048, H-10). The check also covers
  copy, move and restore into a local repository, and an auto claim never takes a name from
  another repository's claim (R-REPO-023).
- **Upstream takedowns reach the cache.** A package whose package document the upstream removed is
  no longer served from the cache, artifacts included, also to lockfile clients that request
  artifacts by exact path (R-REPO-029). Version-level removals depend on the format (H-5).
- **One pattern matcher.** Routing, permission and retention patterns are evaluated by the same
  R-AUTHZ-005 code, so a pattern copied between them cannot silently select something else
  (R-REPO-005). On npm a bare `*` does not cover scoped names; a save that relies on it returns
  `pattern_unscoped_only`, because `excludePatterns: ["*"]` would otherwise still send scoped
  internal names upstream.
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
- **Kill switch:** the global offline switch stops all upstream traffic within seconds (R-REPO-031),
  also while Valkey is unavailable, because it travels through the PostgreSQL change log.
- **Valkey grants nothing on this page.** The proxy reads negative entries and flight locks from
  Valkey. A negative entry can only make a remote path answer 404, and a flight lock can only delay
  a fetch or a metadata refresh until `proxy.fetch_wait_timeout_seconds`, after which the replica
  fetches without it; meanwhile expired metadata is served stale, so a held lock cannot freeze a
  package list (R-REPO-026, R-REPO-028). Cache entries, stale copies and merged documents come from PostgreSQL,
  S3 or the replica's own memory. The shadowing check ignores negative entries, so a forged entry
  cannot let a `deploy` holder shadow a public package (R-REPO-048). What remains is the denial of
  service that [architecture.md](architecture.md) H-49 describes.
- **Duplicate fetches never mix bytes.** Lost or bypassed flight locks lead to concurrent fetches;
  when they return different bytes for one artifact, the first commit wins, the other leader aborts
  its client before the tail, which every leader withholds until its entry has committed, and a
  security event is written (R-REPO-026, R-REPO-041).
- **Explanations do not leak member keys** to callers who cannot read those members (R-REPO-019).

## Decisions

Each question below was decided by the development agent from the listed options; the owner reviews the choices at the walkthrough.

### Q-REPO-1 · Who specifies the stored-secret mechanism (IAM-045)?

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) This page | R-REPO-033 specifies key handling, the sealed format and rotation for repository secrets | Everything about upstream credentials in one place | Client-certificate keys, outbound-proxy passwords and webhook secrets (R-OPS-042) need the same mechanism; a second specification means a second key, a second rotation procedure and a second backup item |
| (b) [architecture.md](architecture.md) for every stored secret | R-ARCH-021 owns the key ring, the sealed format `v1:<key id>:…`, rotation and the start-up canary; this page states what repository secrets rely on and uses it | One key ring, one rotation job, one backup item; R-ARCH-021 and the chart side (R-HA-027) already specify it | Every replica holds the key (H-8) |
| (c) References only | Secret fields accept only `secretRef`; nothing is sealed in PostgreSQL | No key to manage | Credentials cannot be entered through the API or UI at all; every new upstream token needs a Secret change and a file on every replica |
| (d) trove writes Kubernetes Secrets | Values entered through the API are written by trove into Kubernetes Secrets and read back at use time | No key ring of trove's own; the cluster's encryption at rest applies | trove needs RBAC to create and read Secrets in its namespace, so a compromised replica reads every Secret there, the database password included; the Kubernetes API becomes a stateful dependency, which INV-3 rules out |
| (e) External key management service | Values are sealed and opened by an external service (a cloud KMS or a secrets engine's transit API); trove holds no key | The key never sits on a replica, which closes H-8 | Every unseal becomes a call to a further stateful dependency beside PostgreSQL and S3, which INV-3 rules out for the baseline; an outage of that service stops every authenticated upstream fetch; many installations have no such service |

**Choice:** (b) — R-ARCH-021 is the one stored-secret mechanism for every stored secret, and
R-REPO-033 only uses it.

**Rationale:** (b) is the only option that keeps one key, one rotation and one backup procedure
while still allowing credentials to be typed into the API and UI. (a) would duplicate a
security-critical primitive, and two implementations of sealing drift apart. (c) gives up entering
credentials through the API, and with it through the UI, which uses only the API (INV-17). (d)
widens trove's privilege footprint to reading Secrets, which is worse than H-8. (e) is the only
option that removes H-8, but it makes a further service a hard dependency of every authenticated
fetch, and it does not need a mechanism of its own: a key ring whose keys an external service
unwraps fits into R-ARCH-021 as an additional key source. The development agent would revisit this
if an installation must keep the key off the replicas; (e) would then arrive as that key source,
not as a second mechanism.

**Applied in the spec:** R-REPO-033: the "Sealing" bullet says that inline values MUST be sealed
with R-ARCH-021 and that this page defines no key, format or rotation of its own. It lists the
properties this page relies on as checks (checked against R-ARCH-021: AES-256-GCM, 96-bit random
nonce, associated data of table, primary key and column, key identifier in the sealed form,
`secrets-reencrypt`), among them the binding to `repo_id` and `field`. A new acceptance criterion:
a sealed value copied into another repository's row fails with 502 `upstream_secret_unavailable`.

### Q-REPO-2 · Lower bound of the metadata period

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Revisited on 2026-10-28 after the owner added the Valkey, horizontal-scale, backup and storage-class constraints: the choice stays (a). Cross-replica collapse now runs through a Valkey lock (R-REPO-026), so while Valkey is unavailable a repository set to `0` sends up to one conditional request per replica and flight instead of one per cluster; the per-repository fetch limit became a cluster budget (Q-REPO-8), so the bound on the upstream no longer grows with the replica count. The rationale names both. R-REPO-027's rule that serves collapsed waiters under `0` now compares database values instead of a replica clock.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Allow `0` | `metadataTtlSeconds: 0` revalidates on every request | Teams that need a just-published upstream version get it at once; revalidation is a conditional header exchange | More upstream round trips and request latency for repositories set to `0` |
| (b) Minimum 30 s | Values below 30 are refused, as in the catalog | Familiar bound; caps request volume per document | Up to 30 s of forced staleness; protects the upstream from nothing that collapsing and the fetch limits do not already prevent |
| (c) `0` only with validators | `0` applies when the upstream sends `ETag` or `Last-Modified`; otherwise a 30 s floor | Prevents repeated full transfers from upstreams without validators | The same setting behaves differently per upstream; one more branch to build and test, for a rare case in which the operator asked for fresh data anyway |

**Choice:** (a) — `0` is accepted and means "revalidate on every request"; there is no minimum.

**Rationale:** Revalidation is conditional, so a 304 costs headers only. Concurrent requests
collapse into one flight per cluster through the Valkey lock, and into one per replica while Valkey
is unavailable (R-REPO-026). The per-repository fetch budget, shared out over the replicas, the
per-replica fetch limit (R-REPO-040) and the circuit (R-REPO-029) bound the load. Both default upstreams send
validators (checked on 2026-10-28: the npm registry's package documents and nuget.org's version
lists answer with `ETag` and `Last-Modified`), so (c) would add a branch for upstreams nobody uses
by default. A floor therefore protects nothing and only takes freshness away from the operator. The
development agent would revisit this if upstreams answer 429 to trove because of repositories set
to `0`; a floor or a save-time warning would then be added.

**Applied in the spec:** R-REPO-027 says that a period of `0` MUST be accepted, with the reasons
(R-REPO-026, R-REPO-040). It now also says that an entry validated after a request first looked it
up is fresh for that request; without that rule, every cross-replica waiter of R-REPO-026 would
fetch again under `0`, so the collapse that justifies this option would not hold. On 2026-10-28
the rule changed from "validated after the request arrived" to a comparison of the `validated_at`
values the request read, because the waiters it serves run on other replicas, whose clocks need
not agree with the database's. Its acceptance criterion
covers five sequential requests (five conditional upstream requests) and 20 concurrent ones on two
replicas (one). R-REPO-024 and R-REPO-027 now list the NuGet format default of 300 s that
[formats/nuget.md](formats/nuget.md) decided (Q-NUGET-2), where they had still said 7200 s. The
catalog deviation row stays.

### Q-REPO-3 · Generic default when the upstream removes a cached document

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Mark gone, hide the document, keep artifacts | The document is marked `upstream_gone` and answers 404 (also on the cache view and in merges); cached artifacts stay served by exact path | Builds pinned to a legitimately removed package keep working; least to build | Lockfile clients request artifacts by exact path (`npm ci`, R-NPM-025), so the cache keeps handing out what the upstream took down to every project that already pinned it; [formats/npm.md](formats/npm.md) rejected the same behavior for npm for that reason (Q-NPM-2) |
| (b) Mark the package gone, hide its document and every artifact | A 404 or 410 for a package's package document (R-REPO-003) marks the package gone: its documents and artifacts, cached or not, answer 404 on the remote, its cache view and in virtual repositories; items stay stored and copyable; formats that track versions replace the rule | A takedown reaches lockfile clients too; needs only what the generic layer has (the descriptor's package name and the registered package-document path); the same stance as npm's decided default | A build that needs a legitimately removed package breaks until someone copies it into a local repository; a spurious upstream 404 hides cached artifacts until the negative entry expires or is invalidated |
| (c) Keep serving the stale document | The upstream 404 is treated like an outage | Maximum availability | Keeps advertising and serving packages after their takedown |
| (d) Debounce | The mark is set only after a second 404 at least one negative period later; the stale document is served until then | Survives a transient upstream 404 | Delays every takedown by `notFoundTtlSeconds` (30 min by default), while a transient 404 already heals when its negative entry expires and can be cleared at once by an invalidation (R-REPO-045) |
| (e) (b) with a per-remote availability switch | As (b), plus a generic `remote.cache.serveUpstreamRemoved` that keeps serving | Availability-first remotes can opt out, as npm remotes can with `serveRemovedVersions` | A second switch with overlapping meaning next to npm's own, or a rename on the npm page; copying the package into a local repository already covers the legitimate case, per package instead of per remote |

**Choice:** (b) — a 404 or 410 for a package's package document marks the package gone, and the
remote stops serving its documents and artifacts, cached ones included, until the upstream serves
it again; formats that track removal per version replace the rule.

**Rationale:** The upstream is the authority on its own content, and a removal is mostly a
takedown: nuget.org does not let owners delete packages and deletes them itself only for
potentially harmful content or infringement
([deleting packages](https://learn.microsoft.com/en-us/nuget/nuget-org/policies/deleting-packages)).
The decisive client fact is that lockfile clients fetch artifacts by exact path. (a) therefore
keeps the payload flowing to every project that pinned it, which is why the npm page decided
against that behavior (Q-NPM-2). A generic default weaker than the one format that decided would
come back with every later format whose clients fetch by path. The earlier argument against (b),
that only the format knows which artifacts belong to a package, does not hold: every request
descriptor carries the normalized package name (R-REPO-003), and the format registers the
package-document path, which R-REPO-048 needs anyway. The availability cost is bounded: the mark
heals by itself after the negative period or at once through an invalidation, and a build that
needs a legitimately removed package keeps it by copying it into a local repository, which also
records that decision. (c) and (d) keep serving takedowns. (e) adds a second switch for a case that
copying covers. The development agent would revisit this if spurious upstream 404s cause outages
in practice, or if operators of formats other than npm ask for a per-remote opt-out; that would
then be one generic switch with npm's folded into it.

**Applied in the spec:** This review changed the choice from (a) to (b). R-REPO-003: formats
register the package-document path. R-REPO-025: step 4 treats entries of gone packages as absent,
and a new "Gone packages" bullet answers 404 after step 3 without an upstream request; when the
package document's negative entry is no longer valid, an artifact request first refreshes the
document, so the mark heals for clients that never request documents. The data model allows the
mark only on metadata entries and adds a partial index for loading. R-REPO-029: "Upstream gone" is
split into the document rule and a "Gone packages" rule; replicas hold the gone packages in memory,
reloaded through `cluster.Notify("upstream_state", …)`, the change area of
[architecture.md](architecture.md) R-ARCH-018, which needs neither `NOTIFY` nor Valkey (until
2026-10-28 the `NOTIFY` channel `trove_upstream_state`); npm's replacement is restated, and since
2026-10-28 it says that npm reads `upstream_gone_at` per request and serves a cached artifact only
after its package document is current ([formats/npm.md](formats/npm.md) Q-NPM-13). The NuGet acceptance
criterion now expects 404 for the cached `.nupkg` on every address and a self-healing request for
the `.nupkg` alone. R-REPO-032: the cache view answers 404 for gone packages, and their items stay
listed and copyable. Security notes: new bullet. H-5: narrowed to versions withdrawn from a package
that still exists, and renamed accordingly.

### Q-REPO-4 · Scope of remote refusal for claimed names

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) System-wide, `global` claims only | Every remote repository of the format refuses `global`-claimed names, also when addressed directly; `proxy.claims.remote_refusal: true`; pin rules route a claimed name to a named remote | Closes the direct-access path where internal names leak to public registries; only `repositories:write-config` can create the effect | A remote addressed directly cannot serve a globally claimed name at all |
| (b) Inside containing virtual repositories only | Refusal applies only where a claiming member is next to the remote | No effect outside the virtual repositories that hold the name | A forgotten `.npmrc` or a hard-coded CI source pointing at the remote still sends the internal name upstream and installs the public package |
| (c) System-wide with a per-remote exemption | As (a), plus `remote.honourGlobalClaims: false`, settable with `repositories:write-config`, which exempts one remote | A partner remote addressed directly can serve a claimed scope | One setting on a public-registry remote reopens the leak for every client that addresses it; the partner case already works through a pin rule in a virtual repository; one more security setting to audit per remote |

**Choice:** (a) — every remote repository of the format refuses `global`-claimed names, also when
addressed directly, unless a pin rule of the requesting virtual repository names that remote.

**Rationale:** Clients that point straight at a remote repository are where dependency confusion
happens in practice. The request itself discloses the internal name upstream, and the answer is the
attacker's package. (b) leaves exactly that path open. (c) closes it by default but lets a single
setting reopen it for every client of a remote, while the legitimate case it serves, a partner
registry that owns a claimed scope, already works through a pin rule. Because refusal follows only
`global` claims, which need `repositories:write-config` (R-REPO-023), a publish can never cause it
as a side effect. The development agent would revisit this if installations need a directly
addressed remote to serve a globally claimed name; (c) would then be added, and the system switch
would not be turned off.

**Applied in the spec:** No change; the requirement text already follows this option (R-REPO-023,
"Remote refusal"; R-REPO-025 step 2; the `proxy.claims.remote_refusal` default).

### Q-REPO-5 · Generic or format-level placement of pins, extra hosts and upstream authentication

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Generic members | `virtual.pinning`, the claims API, `remote.extraHosts`, `remote.authMode`, `remote.username`, `remote.password` on this page for every format; format pages add semantics, propose defaults and may narrow | One validator, one `secretRef` syntax (R-OPS-029), uniform configuration-as-code documents; claims are cross-format (BEYOND-028) | A format with an unusual host model has to fit `extraHosts` or handle it in its protocol handshake |
| (b) Format-level members | `npm.pinned`, `npm.remote.tarballHosts`, `npm.upstreamAuth` with `secret://` references, and the same again per format | Each format page is self-contained | One mechanism implemented and validated once per format; a second reference syntax; a pattern copied between formats lands in a different member |
| (c) Split | Pins and credentials generic; extra hosts format-level, because the format knows which hosts its upstream needs | Host knowledge stays with the format | The egress-relevant settings of one remote sit in two sections, so a security review has to read both; formats already contribute host defaults through the format catalog |

**Choice:** (a) — pins, claims, extra hosts and upstream credentials are generic members on this
page. Format pages add semantics, may narrow these members and propose defaults, but never define
their own members for them.

**Rationale:** The mechanisms are identical across formats. Implementing them once means one code
path to secure and one place to audit, and configuration as code stays uniform. Formats keep their
knowledge through the format catalog, whose registration now includes the extra hosts a format
proposes for its default upstream (NuGet: `*.nuget.org` without credentials, R-NUGET-014), and
through semantics on their own pages (npm: which account name the public registry expects with
`basic`, R-NPM-023). The development agent would revisit this when a format needs a host or
credential model that `extraHosts` and `authMode` cannot express. A token realm of a container
registry is the expected case, and it belongs in the plug-in's handshake (R-REPO-033).

**Applied in the spec:** R-REPO-004: new bullet; the mechanisms are generic members, format
sections MUST NOT define their own, and format settings live under a member named after the format.
This review corrected its example and the one in R-REPO-033: both claimed that npm sends
credentials only to the upstream URL's own host, but [formats/npm.md](formats/npm.md) R-NPM-023
sends them to the upstream origin and to `extraHosts` with `sendCredentials: true`; the bullets now
name the real format contributions. R-REPO-003: the format registration includes the proposed
`extraHosts`. R-REPO-043: "`remote.extraHosts` is the only host list". The format pages are aligned
through the cross-page consequences.

### Q-REPO-6 · Local publish of a name that a remote member already serves (H-10)

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Revisited on 2026-10-28 after the owner added the Valkey, horizontal-scale, backup and storage-class constraints: the choice stays (b). Negative entries moved to Valkey (R-REPO-028), and a value read from Valkey may only make trove refuse, never admit a write ([architecture.md](architecture.md) R-ARCH-030). The serve decision of R-REPO-048 therefore no longer reads negative entries: a name for which a remote holds only a negative entry is probed. "Stored state" in option (e) now means PostgreSQL entries only.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Refuse | The first publish of a name that a remote member of a containing virtual repository serves is refused for everyone; shadowing on purpose needs a manual claim created beforehand | Strongest default | Two steps even for an administrator; a patched fork of a public package, a legitimate case, always needs a separate claim first |
| (b) Require `manage` | Such a first publish needs `manage` on the coordinate (or `content:bypass`), or a manual claim of the local repository that covers the name, which needs `manage` itself; otherwise 403 `shadows_remote_package`; unreachable upstreams fail closed | A `deploy` grant alone, a broad grant or a leaked CI token, can no longer replace a public package; the deliberate case stays one step for a `manage` holder and is audited; CI is approved in advance through a claim | One upstream probe per new name and exposed remote member, which also tells that upstream the name; first publishes of new names answer 503 while such an upstream is unreachable |
| (c) Warn only | The publish succeeds; a security audit event, a metric and a response warning are emitted | No friction, no upstream dependency at publish time | Detects the attack after every consumer has resolved the replaced package |
| (d) Package-name patterns on deploy grants (state before this decision) | Only narrow `deploy` grants limit which names a principal can publish | Nothing to build | Safety depends on every grant being narrow; broad grants are common, and nothing tells the administrator that a grant is too broad |
| (e) As (b), decided from stored state only | "Serves" is decided only from the remote's stored entries; no probe | No upstream request and no 503 at publish time | A name nobody has resolved through the remote yet, or whose entries the cache cleanup removed (R-REPO-046), passes unchecked, so a `deploy` holder can still take a public name before its first consumer adopts it |

**Choice:** (b) — the first publish of a name that a remote member of a containing virtual
repository serves requires `manage` (or a prior manual claim), is audited, and fails closed when the
upstream cannot be asked (R-REPO-048).

**Rationale:** The harm in H-10 is that a `deploy` grant, the right that CI jobs and broad team
grants hold, silently becomes the power to replace a public package for every consumer. (b) turns
that into an explicit `manage` decision without blocking the legitimate case of patched forks. (a)
buys no protection that (b) lacks, since a `manage` holder can create the claim in the step before,
and it adds that step to every fork. (c) only reports damage that has already happened, and (d) is
the state that left H-10 live. (e) avoids the probe but leaves open exactly the names that matter
next: public packages a team is about to adopt. The check runs only on the first version of a name
outside the repository's claims. Squatting a public name after an internal first publish therefore
cannot block internal releases, and pre-claiming an internal prefix (`@acme/*`, `Acme.*`) removes
the check, and with it the probe, for that prefix. Probes store nothing and ignore the caller's
cache-fill right, so a frozen mirror stays frozen. A check that only publish ran could be bypassed:
publish into a repository that no virtual repository contains, then copy into the shared one; and
an auto claim, being an exact name, would take a name from another team's prefix claim. Both are
closed in the requirement text. The development agent would revisit this if collisions between
internal unscoped names and public names make the check fire often, or if probe latency becomes a
problem for publishing pipelines; a per-repository opt-out for repositories that hold forks by
design would then be weighed.

**Applied in the spec:** New requirement R-REPO-048 (MVP, covers BEYOND-028). It defines when the
check runs, the exposure set, the stored-state-first serve decision with a non-storing probe, the
outcome table (403 `shadows_remote_package`, 503 `shadow_check_unavailable`), approval in advance
through a manual claim, the error body and the acceptance criteria. This review extended it to the
P1 item operations that give a local repository its first version of a name (copy, move, trash-can
restore; refusals reported per item, with an acceptance criterion), and corrected the probe timing
to after R-ARCH-014 steps 4 and 5 and before step 6, because the outcome depends on the caller's
`manage`. R-REPO-003: formats register the package-document path the probe uses. R-REPO-023: an
auto claim is not created when another repository's claim in effect in a containing virtual
repository covers the name (new bullet and acceptance criterion); R-REPO-022 and R-REPO-023
acceptance criteria that publish a name the stub already serves now publish as a `manage` holder.
R-REPO-047: metric `trove_shadow_checks_total`, security event
`repository.remote_package_shadowed`, and probe log records. Security notes: new bullet. H-10:
rewritten to the remaining gap, with configuration changes and the disclosure of new names to
upstreams added in this review. On 2026-10-28: R-REPO-048 step 2 consults stored gone marks only,
no negative entries, with a new acceptance criterion in which a hand-written Valkey entry does not
let `ci-bot` publish; the security notes say so.

### Q-REPO-7 · Remote fetches on a replica that is shutting down

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

A node drain, a rolling upgrade or a scale-down sends `SIGTERM` to a `web` replica that may hold
flights other replicas wait for (R-REPO-026). The replica keeps serving for
`health.shutdown_drain_delay` (5 s by default) and then gets `server.shutdown_timeout`, derived from
a 120 s grace period ([architecture.md](architecture.md) R-ARCH-024,
[ha-and-helm.md](ha-and-helm.md) R-HA-011). A fill spools to the pod's own scratch space, and no
volume is shared between pods (INV-13), so no other replica can continue a partial transfer. The
owner requires a drain without outage when the setup is sized for it.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Finish, or cancel at the deadline | Running fetches continue detached and are cancelled 5 s before the shutdown deadline, released with `failed`; a draining replica starts and holds new flights like any other | Nothing to build beyond R-ARCH-024 | A flight first requested on the draining replica during its drain delay makes waiters on healthy replicas depend on a pod that is leaving; if it is cut, they start over |
| (b) (a), and a draining replica holds no new cross-replica flight | Running fetches as in (a). From `SIGTERM` on, a request on the draining replica joins a flight another replica holds, as a waiter, but never acquires the lock; with no flight held elsewhere, it fetches under its in-process single-flight only | Waiters on healthy replicas never start to depend on a pod that is leaving; one flag in the proxy, one bypass reason in the metric | During the drain delay, a path first requested on the draining replica and on another replica is fetched twice |
| (c) Cancel every fetch at `SIGTERM` | Release `failed` at once; waiters take over | The fastest hand-over | Throws away fetches that would have finished within the grace period, which for npm and NuGet is nearly every fetch; the draining replica's own clients are cut early |
| (d) Hand partial transfers over | The holder writes its partial body to S3 as a multipart upload, and a waiter continues with an upstream `Range` request | No transfer starts over | Needs `Range` support and validators that pin the bytes at every upstream; unverified partial bodies sit in S3; a large mechanism for MVP artifact sizes |
| (e) Fetch on the `worker` role | Misses become jobs; `web` replicas wait for them | `web` drains never touch a fetch | Adds queue latency to every miss; `worker` replicas drain too, with their own grace period, so the problem moves rather than goes |

**Choice:** (b) — a draining replica keeps finishing the fetches it holds until 5 s before its
deadline, then cancels them with `failed`, and from `SIGTERM` on it holds no new cross-replica
flight.

**Rationale:**
- npm tarballs and NuGet packages finish well within the grace period, so cancelling early (c) only
  loses work that was about to complete.
- A cut transfer cannot continue on another replica without shared storage, and ReadWriteMany is
  excluded (INV-13). (d) would rebuild that over S3 for artifacts that do not need it. (e) moves
  the drain problem to another role and slows every miss.
- (b) costs one flag. It confines duplicate fetches to the drain delay, and it removes the case in
  which waiters on healthy replicas lose progress to a drain they had no part in. A flight that
  started before `SIGTERM` can still be cut, and its waiters then take over at once instead of after
  the lock's TTL.
- Client facts that bound the damage: npm retries twice by default and allows 5 minutes per
  request; NuGet makes up to six tries and waits 100 s for response headers
  ([npm config](https://docs.npmjs.com/cli/v11/using-npm/config),
  [NuGet `HttpSourceRequest`](https://source.dot.net/NuGet.Protocol/HttpSource/HttpSourceRequest.cs.html)).
  A waiter whose holder was cut acquires the flight within one poll interval and fetches again, well
  inside both.
- Revisit when container-image layers or large generic files (P1) show flights cut at shutdown in
  `trove_http_transfers_cut_total` and the `draining` bypass count. (d) for those formats, or a
  dedicated Deployment with a longer grace period ([architecture.md](architecture.md) Q-ARCH-18),
  is then the next step.

**Applied in the spec:** R-REPO-026: the "Shutdown and drain" bullet, the bypass reason `draining`,
and acceptance criteria for a graceful delete that outlasts the deadline and for a request during
the drain delay. R-REPO-027: the background refresh is cancelled at shutdown and stores nothing.
R-REPO-049: the drain column of its table and a drain acceptance criterion. R-REPO-047: the bypass
reasons. Cross-page: [architecture.md](architecture.md) R-ARCH-033 (the reasons `draining` and
`wait_timeout`; `Acquire` is not called while draining) and R-ARCH-024 step 1;
[ha-and-helm.md](ha-and-helm.md) R-HA-011.

### Q-REPO-8 · Upstream fetch limits when the replica count changes

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

`limits.maxConcurrentFetches` counted per replica (32 by default), so the upstream saw the
configured value times the number of `web` replicas. Under the new constraints that number grows
with the load and changes with autoscaling, while the tolerance of an upstream such as a public
registry does not.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Per replica | The earlier rule | No coordination; upstream capacity grows with trove's | The limit an operator sets is multiplied by a replica count that autoscaling changes; 20 replicas send 640 concurrent fetches to one upstream at the old default |
| (b) A cluster budget shared out per replica | Each replica allows `ceil(budget ÷ n)` fetches, with n the live client-serving replicas in the replica registry | One meaning whatever the replica count; no shared state, no Valkey round trip, no slot that a killed replica leaks; reuses the count that the rate-limit fallback already keeps (R-ARCH-034) | Rounding up admits up to n − 1 extra fetches, and a budget below n admits one per replica; after a scale event the shares lag by one heartbeat; one busy replica may queue while another has free slots |
| (c) A cluster-wide semaphore in Valkey | Slots held as Valkey keys with a TTL; (b) as the fallback | Exact bound while Valkey is up | Needs a slot set without scripts, on the command set R-ARCH-031 grants (`GET`, `SET`, `DELIFEQ`, `INCR`, `EXPIRE`, `MULTI`, `EXEC`): one round trip per slot tried, slots of killed holders blocked until their TTL, and one more Valkey dependency on every fetch, to protect a bound that (b) already keeps approximately |
| (d) A cluster-wide rate limit | The R-ARCH-034 limiter on fetch starts per second | Reuses the limiter | Limits starts, not connections: a slow upstream piles up open fetches anyway; the per-replica slots would still be needed |

**Choice:** (b) — `limits.maxConcurrentFetches` is a budget for the installation, shared out as
`ceil(budget ÷ n)` per replica from the replica registry; the default becomes 64 for the cluster.
`proxy.max_concurrent_fetches` stays per replica, because it protects the replica.

**Rationale:**
- A limit that changes with the replica count is not a limit an operator can set; this is the
  argument of [architecture.md](architecture.md) Q-ARCH-17 for rate limits, and it holds here for
  the same reason.
- (b) keeps the bound within `budget + n − 1` with nothing shared. The overshoot is small against
  what (a) allowed, and it holds while Valkey is unavailable.
- (c)'s exactness is not worth a Valkey call and a leak case on every fetch for a bound whose
  purpose is to stop bursts, not to meter. (d) does not bound open connections.
- Random distribution by the Service evens out the per-replica shares; an uneven share costs a
  queued fetch of up to `proxy.queue_timeout_seconds`, and collapsed requests take no slot.
- The default of 64 for the cluster is a proposal, not a measurement. It is below the earlier
  per-replica default times three replicas (96), but it does not grow to 640 at 20 replicas.
- Revisit if upstreams answer 429 to trove under the reference load (the budget is then too high,
  or (d) is added), if cold-cache builds queue on fetch slots (the default is then too low), or if
  budgets below the replica count become common ((c) is then the exact form).

**Applied in the spec:** R-REPO-040: fetch slots with the per-replica share, the bound, the queue
and a new acceptance criterion with two and three replicas. Only the request that fetches takes a
slot, after it holds the flight (R-REPO-025 step 10, R-REPO-026 step 3), so a replica's small share
is not spent on waiting for another replica's transfer; a second acceptance criterion checks that.
R-REPO-024: the settings table row
(default 64, range 1–4096). R-REPO-049: the fetch-slot row. Q-REPO-2: its rationale.
Cross-page: [operations-and-observability.md](operations-and-observability.md) R-OPS-003 (the
replica registry count is read by the proxy as well).

### Q-REPO-9 · Source address for upstream connections with interchangeable replicas

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

RMT-015 lets a remote repository pick the local address of its upstream connections, for a host
with several interfaces and a firewall that admits one of them. R-REPO-024 stored that as one
address, `sourceAddress`, in the repository configuration, which every replica shares. Every pod
gets its own IP address ([Kubernetes network model](https://kubernetes.io/docs/concepts/services-networking/)),
and every role runs at least two replicas (INV-12).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One address (earlier) | `sourceAddress: 10.99.0.11` | Simple; the catalog's form | Fits one pod at most. Every other replica fails every fetch of the repository, and because the circuit is shared, its failures open the circuit for all replicas |
| (b) A network prefix | `sourceNetwork: 10.99.0.0/24`; each replica binds the first of its own addresses inside the prefix; a replica without one answers 502 `source_network_unavailable`, not counted toward the circuit | One configuration for all replicas (INV-2); keeps the catalog case of multi-homed pods, for example a secondary network to a partner's VPN; fails closed per replica | A dialer that enumerates interfaces; an error code and a metric |
| (c) Drop the setting | The platform decides the source: routes, an egress gateway, NAT | Nothing to build | Loses RMT-015's case where one destination is reachable through several interfaces and the kernel's choice is the wrong one |
| (d) A per-replica address in file configuration | Each pod's configuration names its own address | Exact addresses | Replicas of a role differ in configuration, against INV-2, and a rescheduled pod gets another address, so the mapping breaks on the next drain |

**Choice:** (b) — `remote.sourceNetwork` takes a CIDR prefix; each replica takes its own address in
it, and a replica without one fails that repository's fetches closed, without opening the shared
circuit.

**Rationale:**
- (a) cannot work with two replicas, and it turns one replica's local problem into an outage of
  the repository on every replica through the shared circuit.
- (d) breaks the rule that replicas of a role are identical and interchangeable, and node drains
  reschedule pods to new addresses.
- (c) is honest, but secondary networks exist exactly where this setting matters, and the dialer
  already has a hook per connection for the egress rules (R-REPO-038).
- (b) states the intent ("leave through this network") in a form that every replica can satisfy. A
  replica that cannot does not fall back to the kernel's choice, which could reach the upstream
  through an interface its firewall should never see.
- Revisit if no installation uses the setting by the time the P1 formats ship; (c) would then
  remove it.

**Applied in the spec:** R-REPO-024: the member is `sourceNetwork`, its bullet, the upstream-test
field `sourceAddress` and two acceptance criteria. R-REPO-008: `invalid_source_network`, and the
warning `source_network_single_address`. R-REPO-047: the metric
`trove_remote_source_network_unavailable_total`. Deliberate deviations: new row. Cross-page:
[operations-and-observability.md](operations-and-observability.md) metrics catalog (the new
counter). No other spec page names `sourceAddress` (checked on 2026-10-28).

### Q-REPO-10 · Zoned IPv6 and the NAT64 prefix in the egress rules

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

An audit found two ways past the CIDR rules: an IPv6 address with a zone is in no prefix, so no deny rule sees it, and an
address in the NAT64 prefix can stand for an internal IPv4 address that the deny list protects.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Refuse a zoned IPv6 address before the CIDR rules, and check the IPv4 address of the well-known NAT64 prefix as well | two fixed checks; the deny list grows by four ranges | closes both paths with no configuration; the allow list cannot open a zoned address | a network-specific NAT64 prefix stays unchecked until a setting exists |
| (b) Strip the zone and check the address | the zone is dropped and the rules run on the bare address | keeps the rules as they are | a zone can name a different interface; the bare address may be allowed where the scoped one is not |
| (c) Make the NAT64 prefix a setting, with no default | the operator names the prefix | exact for every network | a cluster without the setting is unprotected by default |

**Choice:** (a). The deny list adds `::/96`, `100.100.100.200/32` and `168.63.129.16/32`, the deprecated IPv4-compatible range and
two metadata addresses of other clouds. A configurable prefix (c) is the next step if a network uses another one.

**Rationale:** A zoned address is a local-host address and no remote repository needs one, so refusing it costs nothing. The NAT64
check follows the translation of the well-known prefix, which is the one the cluster DNS64 setups use by default. The development
agent would revisit this when a network uses a different NAT64 prefix in a supported setup.

**Applied in the spec:** R-REPO-038 (the zoned and NAT64 rules, the deny list); the default of
`proxy.outbound.address_policy.deny_cidrs` in [config/outbound.go](../../internal/config/outbound.go) and in
[security/egress.md](../security/egress.md).

### Q-REPO-11 · Repository columns added in M1

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-REPO-001 adds six columns and a constraint to `repositories` in one `ALTER TABLE`. Two of them
belong to requirements outside M1: `claims_version` (the claims ETag, R-REPO-023, M2) and `project_id`
(R-AUTHZ-026, P1, a foreign key to a table that does not exist yet).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) All six columns in M1 | One migration as sketched | Matches the sketch | `project_id` cannot be created without the projects table; `claims_version` would be an ETag column no code writes |
| (b) The M1 columns now, the others with their requirements (chosen) | `lifecycle`, `config_version`, `created_by`, `updated_by`, `last_activity_at` and `repositories_key_dots` in `00010_repositories.sql` | No unused column; each later column is an expand step (`ADD COLUMN` with a default) of the work that needs it | The M2 and P1 work each add a migration |

**Choice:** (b). `created_by` and `updated_by` stay plain `uuid` columns as in the sketch, so a
repository outlives its creator. `repositories.config_version` is registered with
`meta.RegisterRevisionColumn`; `claims_version` registers with the migration that adds it.

**Applied in the spec:** R-REPO-001 data model (a note under the `ALTER TABLE`).

### Q-REPO-12 · The error code of a 422 with one or several violations

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-REPO-002 and R-REPO-009 say "returns 422 `invalid_key`"; R-REPO-008 says every violation comes in one
response; R-API-009 shows a 422 whose `code` is `validation_failed` and whose `errors[]` carry the details.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Always `validation_failed`, the specific code only in `errors[]` | One top-level code | Matches the example of R-API-009 | The acceptance criteria that say "422 `invalid_key`" would need a lookup into `errors[]` |
| (b) A single violation under its own code, several under `validation_failed` (chosen) | `errors[]` always lists every violation with its code; the top-level `code` is the violation's own when there is exactly one | Both readings hold: a client matching on `invalid_key` or `immutable_field` sees it at the top, a client iterating `errors[]` sees every violation | The top-level code depends on how many violations there are |

**Choice:** (b). Superseded by [Q-REPO-84](#q-repo-84--a-violation-answers-422-validation_failed-a-body-error-400-malformed_request): the top-level code is `validation_failed` whatever the number of violations.

**Rationale:** The acceptance criteria of this page read as top-level codes, and a client that created
one key wrongly wants exactly that. The development agent would revisit this if the API area settles on
one rule for every resource; the repository package then follows it in `validation.err`.

**Applied in the spec:** none (the criteria already read this way); `internal/repository/validate.go`.

### Q-REPO-13 · Management reads come from PostgreSQL, the list filters in the domain

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Read and list from the replica's snapshot | One source for requests and management | No query per management read | A management read could be stale for the poll interval, against R-API-052 |
| (b) Read and list from PostgreSQL; filter and page over one read of the table (chosen) | `Service.Get` and `Service.List` query the table; the snapshot serves request paths only | Meets R-API-052 and Q-API-12; the number of repositories of an installation is small, so no dynamic query is needed | A list reads the whole table on every call |
| (c) A dynamic SQL list query (`listq`, R-ARCH-019) | Filter and keyset in SQL | Scales to very many repositories | Machinery for a table that holds dozens to hundreds of rows |

**Choice:** (b). `List` returns items sorted by key with a keyset `After` and a `Limit`, plus `HasMore`; the cursor
format is the API layer's (R-API-010).

**Rationale:** (c) would be the answer if installations reached tens of thousands of repositories. The
development agent would revisit this then.

**Applied in the spec:** none.

### Q-REPO-14 · One snapshot entry, flushed by every `repo_config` change

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One cache entry for the whole table, flushed on any change (chosen) | The change payload names the repository id (R-REPO-008), but the cache ignores it and the next read rebuilds | Trivially correct, including a missed or forged hint; one query rebuilds it | A change anywhere costs one rebuild on every replica |
| (b) One cache entry per key, evicted by the id in the payload | Finer eviction | Fewer rebuilds | The per-key set must also handle list calls and unknown keys; a payload that does not match a key needs a fallback flush |

**Choice:** (a). The payload is `{"id", "key", "op"}` so that a later consumer can evict by id.

**Rationale:** Repository changes are administrative and rare (R-ARCH-018 keeps change areas for such writes). The
development agent would revisit this if an installation changed repositories at a rate where rebuilds show in
the metrics.

**Applied in the spec:** none.

### Q-REPO-15 · A repository deletion records two events and joins a running job

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One `repository.deleted` event when the job removes the row | Past tense matches the fact | One event | The decision (who, when) and the state change of the first transaction would have no event; R-REPO-008 requires an event for every change |
| (b) `repository.deleted` with `phase: started` in the first transaction and `phase: completed` in the last (chosen) | The first names the caller who asked; the second carries the same actor from the job arguments | Every committed change has its event in its own transaction | Two events per deletion |

**Choice:** (b). A repeated delete while the job runs records nothing and answers with the same job (the job kind is
exclusive per repository id). `detach` is accepted and has nothing to detach until virtual membership exists
(R-REPO-017); a virtual repository, which has no content, is removed in the first transaction with the `completed` phase.

**Rationale:** The development agent would revisit this if the audit area wants distinct event names for the two phases.

**Applied in the spec:** R-REPO-013 reads unchanged; `internal/audit/events_repository.go` declares the `phase` property.

### Q-REPO-16 · The shape of the stored document

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-REPO-004 lists the members; it leaves open how an omitted setting, a `null`, an unknown member and the
members owned by other pages are stored.

| Question | Choice |
|---|---|
| What does `repositories.config` hold? | The settings: every member but `key`, `class`, `format` and the derived `layout`, with defaults filled in, so that what is stored and what is read back are one form |
| Unknown or duplicate member | 400 `invalid_document` at every level, also on a merge-patch result |
| `hideExistence` | a string: `"inherit"`, `"true"` or `"false"` (the contract's `Repository.HideExistence`) |
| `null` in a merge patch | removes the member, which reads back as its default |
| Members that other pages own (`storage`, `remote`, `virtual`) | carried as JSON objects without looking inside, until the page that owns them is implemented (`storage` for M1: a JSON object; `remote` and `virtual` cannot occur, because the generic format supports only the local class) |
| An update that changes no stored member | writes nothing: same `config_version`, no event, no change row |
| `PUT` with an omitted `key`, `class` or `format` | unchanged (a different value is `immutable_field`) |
| Members that cannot apply to a format | `not_applicable` in the same pass, decided by the format descriptor |

**Rationale:** A misspelt member must never be dropped silently (R-REPO-008). A no-op that moves the ETag would
make a client that re-sends an unchanged document race with itself. The development agent would revisit the
treatment of `storage` when R-STOR-003's settings are implemented.

**Applied in the spec:** none.

### Q-REPO-17 · The pattern matcher lives in `internal/repository`

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-REPO-005 requires the matcher of R-AUTHZ-005. Layers (R-ARCH-002) let `authz` import `repository` but not the
reverse.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The matcher in `internal/repository`, exported as `CompilePattern`; authz calls it (chosen) | One implementation for both pages | "A pattern copied between them selects the same names" holds by construction | The authorization area depends on a function that lives in another package |
| (b) A leaf package `internal/pattern` | A neutral home | No dependency on the repository package | One more package; R-ARCH-003 would need a line |
| (c) Two copies | Each page owns its matcher | No coupling | The two drift apart |

**Choice:** (a), with a test table (path and name subjects) that carries the cases of R-AUTHZ-005.

**Rationale:** The development agent would move the matcher to a leaf package (b) if a third consumer appears, such as
retention (R-STOR-043).

**Applied in the spec:** none.

### Q-REPO-18 · The wait for an unknown key is `repositories.lookup_wait`

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-ARCH-011 step 2 bounds the wait of a lookup "like a wait for the database (R-HA-009)" and names no key.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A key of its own, default 5 s (chosen) | `repositories.lookup_wait` | Visible and tunable; a poll that cannot finish answers 503 with `Retry-After: 1` | One more key |
| (b) Reuse the database statement timeout | No new key | Nothing to document | The timeout bounds a statement, not a poll that waits on the control connection |

**Choice:** (a).

**Rationale:** 5 s is two orders above the poll's normal duration and below the client timeouts of npm and dotnet.
The development agent would revisit it if R-HA-009 gets a named bound.

**Applied in the spec:** R-ARCH-011 reads unchanged; the key is in [docs/developer/repositories.md](../developer/repositories.md).

### Q-REPO-19 · A holder of repositories:read-config sees every repository in the list

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-REPO-012 says the list contains only repositories on which the caller holds `read`, and `view=full` is the catalog of all configurations.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Only readable repositories, for everyone | The list is filtered by `read` whoever asks | One rule | The repository administrator holds no content rights (R-AUTHZ-012), so the administration screen would list nothing; `view=full` would show a subset of the configurations |
| (b) Every repository for a holder of `repositories:read-config`, the readable ones for everyone else (chosen) | The capability reads configuration instance-wide (R-AUTHZ-013 "no per-repository configuration type") | The role that administers repositories sees them; repositories being deleted are visible to it, as R-REPO-013 says | A second rule in the list |

**Choice:** (b).

**Rationale:** Seeing that a repository exists and what it is configured as is what the capability grants; it grants no access to content. The development agent would revisit this if a project administrator, who holds the capability for the repositories of one project only (R-AUTHZ-026), needs the list narrowed to them.

**Applied in the spec:** R-REPO-012 (permission row).

### Q-REPO-20 · The consumer view needs `read`, and the ETag names the view

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-REPO-010 gives the consumer view to a caller who holds "any repository action", and R-API-012 says the ETag carries the view where the representation depends on it.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Any of the six actions | The handler decides each action until one is allowed | As written | Up to six decisions, five of them refusals that are audited as `authz.denied`; an action other than `read` does not let the caller see the repository elsewhere (R-AUTHZ-016 answers 404 without `read`) |
| (b) `read` (chosen) | One read-type decision, the row of R-AUTHZ-016 | One decision, one audit rule; a caller who may read gets the view and a caller who may not gets the 404 of every other read | A caller with only `deploy` gets 404 on the configuration read, as on `/files/` reads |

**Choice:** (b). The tag of the full document is `"<config_version>"`, the tag of the consumer view `"<config_version>-consumer"`; the answer of a write carries the first.

**Rationale:** The tag of a view that the caller cannot write through never has to be accepted as an `If-Match`. A caller holding only a write action on a repository has no use for the consumer view; the development agent would revisit this if one appears.

**Applied in the spec:** R-REPO-010 (permission row).

### Q-REPO-21 · A misspelt or malformed member of a repository document answers 400 invalid_document with its pointer

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-REPO-008 says 400 `invalid_document` for malformed JSON and unknown members; R-API-003 says 400 `malformed_request` with the pointer of the member in `errors[0]` for every body.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `malformed_request` for every body | The conventions of the API own the error | One code for every resource | R-REPO-008 and the acceptance criteria of R-GENERIC-001 name `invalid_document` |
| (b) `invalid_document` with the pointer (chosen) | The repository document keeps its code and gains the pointer of the convention | Both pages are right: the status is 400, the code is the one the repository page documents, and `errors[0].pointer` names the member | The contract tests of the conventions need the code as a parameter (`apitest.Resource.MalformedCode`) |

**Choice:** (b). Superseded by [Q-REPO-84](#q-repo-84--a-violation-answers-422-validation_failed-a-body-error-400-malformed_request): the body error is 400 `malformed_request` with the pointer, and the repository code is not a top-level code any more.

**Rationale:** The code is what a client of the configuration API matches on. The development agent would revisit this if the conventions page makes the code of a body error uniform.

**Applied in the spec:** R-REPO-008 (the first paragraph reads unchanged; the pointer is the addition).

### Q-REPO-22 · The list filters: repeated or comma-separated values, and a project that selects nothing

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-REPO-012 takes `class` and `format` as comma-separated lists, R-API-011 as repeated parameters, and the `project` filter needs projects, which M1 does not have.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One form | Comma-separated only, or repeated only | A single rule | Contradicts one of the two pages |
| (b) Both forms; `project` selects nothing (chosen) | Values are split at commas and repeats are joined; a project key selects the repositories of the project, and there are none | The documented examples work; the parameter exists from the first release, so a client does not change when projects arrive | A filter that always answers an empty list |

**Choice:** (b).

**Rationale:** Accepting the parameter keeps the contract of the list stable; an unknown project key is not an error in a list (R-API-011). The development agent would revisit this when the projects table exists.

**Applied in the spec:** The operation `listRepositories` in api/openapi.yaml describes it.

### Q-REPO-23 · last_activity_at is flushed once a minute by a constant interval

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-REPO-012 asks for a write at most once per minute per repository and replica and a flush at shutdown, and names no configuration key.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A key `repositories.activity_flush_interval` | The interval is tunable | Tests and operators can change it | A second interval whose lower bound is the requirement; one more key to document |
| (b) A constant of one minute (chosen) | `repository.DefaultActivityInterval`; the tests construct the buffer with a shorter interval | The requirement is the interval; nothing to misconfigure | No operator control |

**Choice:** (b).

**Rationale:** "At most once per minute" is a ceiling the requirement fixes, so a tunable would only offer values that break it. The development agent would revisit this if an installation needs a lower write rate.

**Applied in the spec:** R-REPO-012 reads unchanged; [docs/developer/repositories.md](../developer/repositories.md) names the constant.

### Q-REPO-84 · A violation answers 422 validation_failed, a body error 400 malformed_request

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-API-009 fixes the top-level code of a 422 as `validation_failed` and of an unreadable or unknown-member body as
`malformed_request`; the repository page had its own top-level codes for a single violation (Q-REPO-12), for a misspelt member
(Q-REPO-21) and for a taken key (`repository_exists`), while the identity and policy areas used `conflict` and the code of the first
problem. Codes are a stable contract (R-ARCH-025), so one rule must hold for every resource.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The page keeps its codes | Q-REPO-12 and Q-REPO-21 stay | The criteria read as before | A client needs one rule per resource; the contract tests carry overrides (`apitest.Resource.MalformedCode`, `ImmutableCode`) |
| (b) R-API-009 everywhere (chosen) | 409 `already_exists`; 422 `validation_failed` with every violation, each with its own code, in `errors[]`; 400 `malformed_request` with the pointer of the member; `reserved_name` stays the top-level code of a reserved key, because R-API-004 defines it so | One rule; the overrides are gone | The criteria of this page read "422 `validation_failed` with `invalid_key` in `errors[]`" |

**Choice:** (b). The codes `repository_exists` and `immutable_field` are removed from the registry; a change to an immutable member
is the field code `immutable` of R-API-013. `invalid_document` stays as the field code of a document that the domain cannot read
at all (no pointer is known).

**Rationale:** a client that matches the top-level code of every 422 on one value, and the field codes in `errors[]`, needs no
resource-specific knowledge. The development agent would revisit this if clients need the specific code without reading `errors[]`.

**Applied in the spec:** the acceptance criteria and the code table of R-REPO-002, R-REPO-003, R-REPO-008, R-REPO-009, R-REPO-010
and R-REPO-011 of this page; Q-REPO-12 and Q-REPO-21 are marked superseded; R-API-009 ([Q-API-136](api-and-ui.md#q-api-136--one-rule-for-the-top-level-code-of-an-error)).

### Q-REPO-85 · The integer keys of `proxy.*` keep the unit in their name

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the configuration framework asks for `time.Duration` and `ByteSize` instead of an integer with a unit in its name ([../developer/configuration.md](../developer/configuration.md#adding-a-key)). This page names `proxy.lock_poll_interval_ms`, `proxy.fetch_wait_timeout_seconds` and `proxy.queue_timeout_seconds` as integers, and its acceptance criteria, the failure-mode analysis (H-49) and [architecture.md](architecture.md) R-ARCH-033 quote the names and the values (`proxy.fetch_wait_timeout_seconds: 5`).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Keep the names and integer values | `lock_poll_interval_ms: 1000`, `fetch_wait_timeout_seconds: 120`, `queue_timeout_seconds: 10`; the code reads them through `LockPollInterval()`, `FetchWaitTimeout()` and `QueueTimeout()` | Every quoted name, value and criterion stays true; the generated environment names do not change | Three keys break the framework's convention; a value is not self-describing without its name |
| (b) Rename to durations | `lock_poll_interval: 1s`, `fetch_wait_timeout: 2m`, `queue_timeout: 10s` | Follows the convention | Rewrites this page, R-ARCH-033, H-49 and every criterion that quotes a key; the work packages that were briefed with the old names diverge |

**Choice:** (a), with the ranges `lock_poll_interval_ms` 50 to 60000, `fetch_wait_timeout_seconds` 1 to 3600, `queue_timeout_seconds` 0 to 600, and the cross-field rule that the poll interval is below the wait timeout. The byte sizes (`max_artifact_bytes`, `max_metadata_bytes`, `merged_metadata_cache_bytes`) are `ByteSize` and accept a plain integer, so their quoted values stay valid. Keys of the npm section that the spec writes without a unit in the name (`npm.document_max_age`, `npm.publish.retry_window`) are durations: `document_max_age: 0s`.

**Rationale:** The names are a contract that criteria and a failure analysis quote, and a rename buys consistency at the price of rewriting them. The development agent would revisit this when the three keys are touched for another reason; the change is then a rename with a deprecation alias (the loader rejects unknown keys, so an alias needs a code change).

**Applied in the spec:** [formats/npm.md](formats/npm.md) R-NPM-013 (`document_max_age: 0s`); [../developer/configuration.md](../developer/configuration.md) names the exception.

### Q-REPO-86 · `remote_cache_entries` has a surrogate `id` so the blob release trigger applies unchanged

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: an entry whose blob is replaced (a refreshed metadata document) must enqueue the replaced blob for collection ([storage.md](storage.md) R-STOR-003). `blob_ref_release_update()` joins the old and the new rows of an `UPDATE` on the column `id`; the primary key of the entry is `(repo_id, path, variant)`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A surrogate `id bigint GENERATED ALWAYS AS IDENTITY UNIQUE` | The existing function applies; `TestBlobReferenceRules` recognises the table unchanged | The smallest change; no second copy of the release logic | One more column and its unique index (8 bytes per entry) |
| (b) A second release function | Joins on `(repo_id, path, variant)` | No extra column | Two functions to keep in step; the migration test has to learn a second shape |
| (c) A generic function | Join columns passed in `TG_ARGV` | One function for every shape | Rewrites a function that `items` and every later table depend on |

**Choice:** (a). The primary key stays `(repo_id, path, variant)`; nothing references `id`.

**Rationale:** The rule "every table with a blob reference installs both triggers" is checked by catalog, and (a) keeps one implementation of it. The development agent would revisit this if the entry table grows so large that the second index matters; then (c) with `TG_ARGV` replaces it.

**Applied in the spec:** the data model of R-REPO-025 (the column, the blob and item indexes, the trigger note).

### Q-REPO-87 · Invalidation rows record their fold in `folded_at`

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-045 keeps a row of scope `negative` or `all` for 86,405 s, and deletes it only "once it is folded and that period has passed". Without a record of the fold the job cannot tell a folded `all` row waiting for its period from one whose fold is still running, and readers must keep applying the unfolded ones.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A column `folded_at timestamptz` | NULL until the last batch of the fold has run; a partial index serves the job's scan of unfolded rows | One row carries its whole life; the job is restartable by the column alone | One more column |
| (b) A table of fold progress | One row per invalidation with a cursor | Records progress inside a fold | A second table and a join for a flag |
| (c) Decide by age alone | A row older than the period counts as folded | No schema | A late fold (a stopped worker) would drop the row early and let unfolded metadata entries count as fresh again, which the page forbids |

**Choice:** (a). The job sets `folded_at` when the fold of the row is complete; scope `metadata` rows are deleted at once, `negative` and `all` rows when `folded_at` is set and 86,405 s have passed since `created_at`.

**Rationale:** The page's own rule, that a replica never drops a row the database still holds before its fold, needs the flag. The development agent would revisit this if folds become resumable inside one row (then a cursor column joins it).

**Applied in the spec:** the data model and the fold bullet of R-REPO-045.

### Q-REPO-88 · A secret field reads {"set": true} and keeps its value in repository_secrets

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-033 stores a secret field's sealed value or reference in `repository_secrets` and says reads show `{"set": true}` for sealed values and the reference itself for references, so that a client re-submitting what it read neither blanks nor leaks the secret. The merge patch of R-REPO-011 is applied to the document that was read, so what the document holds for a secret decides what a patch can express. A new value reads exactly like the old one.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The marker and the reference stay in `repositories.config`; the rows hold the values | `password` is `{"set": true}` or `{"secretRef": …}` in the stored section; a request `{"value": …}` is sealed into the row and the marker written; `{"set": true}` keeps the row; absent or `null` removes it | One source for every read, the snapshot and the diff; a re-submitted document is byte-equal and writes nothing; a merge patch needs no special case | The reference exists in two places (the document and the row), written in one transaction; a secret change must move `config_version` by itself, because the marker does not change |
| (b) The document carries nothing; reads join the rows | The stored section has no `password`; every read adds it from `repository_secrets` | One place per reference | Every read, the snapshot and the diff need a second query; a re-submitted document without `password` would delete the secret, the leak-proof round trip the page asks for is lost |
| (c) A revision number of the secret in the document | `password: {"set": true, "revision": 3}` | The change is visible in the document | A member the spec does not name; a re-submitted document must carry the number or reset it |

**Choice:** (a). A write that carries a new value or a reference counts as a change even when the stored section reads the same, adds `remote` to the recorded changes and moves `config_version` (so the `ETag` changes and the clients are rebuilt). A `{"set": true}` next to a `value` or `secretRef`, which is what a merge patch of a read document produces, is the marker overridden by the new form; `value` and `secretRef` together are 422 `invalid_secret`.

**Rationale:** The page asks that a document which was read can be sent back, and that works only if the read form is a complete, stable form of the stored one. The development agent would revisit this when a second secret field is built (the sensitive header values of R-REPO-036, the client-certificate key): the same shape then applies per field pointer, and a revision member becomes worth it only if clients need to see a rotation in the document.

**Applied in the spec:** R-REPO-024 (the canonical section), R-REPO-033 (the bullet on re-submitted documents and the version).

### Q-REPO-89 · Upstream clients are cached per repository and version; secret files are read at use

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: a remote repository's HTTP client owns a connection pool and is built from the settings, the unsealed password and the hop policy. It is per-replica state (R-ARCH-032, R-REPO-049) and needs a row in both tables. R-REPO-033 says a changed `secretRef` file takes effect within 60 s. The plan keyed the client by `(repository id, config_version, secret-file fingerprint)`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Key by repository id and `config_version`; the credential reads references at use | The client holds a secret object that reads the file or the variable again when its value is 10 s old (1 s after a failure) and fails the request with 502 `upstream_secret_unavailable` when it cannot | A rotated file changes nothing in the pool; no fingerprint to compute per request; the key is the one the ETag already is | Each replica opens the file at most every 10 s per repository |
| (b) Add a file fingerprint to the key | Every lookup stats the referenced files; a changed file builds a new client | The client is immutable | A stat per lookup; a rotation throws the connection pool away; a variable has no fingerprint |
| (c) Build a client per request | No cache | No state | A TLS handshake per request |

**Choice:** (a). The cache `proxy_upstream_clients` is registered in the area `repo_config`, bounded to 2,048 clients, with `cache.snapshot_safety_ttl` as its `SafetyTTL`; a change of the repository evicts its entries, and the idle connections of the client a newer version replaced are closed. A refusal to resolve a secret is remembered for 5 s, so a flood of requests for a repository with a missing file costs one resolution per interval.

**Rationale:** The version key makes a stale client unreachable even if an eviction is missed, and the use-time read gives R-REPO-033's bound without touching the pool. The development agent would revisit this if reading a mounted file every 10 s shows up in a profile; the interval is a constant (`upstream.RefreshInterval`).

**Applied in the spec:** R-REPO-033 (the bound of 10 s), R-REPO-049 (the row), [architecture.md](architecture.md) R-ARCH-032 (the cache).

### Q-REPO-90 · The upstream test is a probe of the format's resource that answers 200 with a result

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-024 defines two operations and the shape of a result, and leaves open what the probe requests, what counts as "reachable" and "authenticated", how an unreachable upstream is answered, and what the test does for a repository that must not contact its upstream.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One GET of the format's probe resource through the repository's own client; every outcome is a 200 with a result | `reachable` is true for any HTTP response, `authenticated` is false for 401 and 403, `error` carries a stable code for a failure; offline repositories are not contacted | The test exercises exactly what a fetch does (credentials, egress rules, source network, redirects); an administrator reads one shape | A 500 from the upstream is "reachable" |
| (b) A transport failure is a 502 | The test answers like a fetch | Familiar status | The UI cannot tell "the test failed" from "the upstream is down"; the DNS case of the acceptance criterion is a result, not an error |
| (c) The test ignores offline | It always sends the request | Works while preparing to go online | The page says offline MUST stop all upstream traffic of the repository |

**Choice:** (a). The probe resource is a facts entry of the format catalog (`ProbePath`: npm `-/ping`; empty means the URL itself), requested with GET under the path of the upstream URL; at most 64 KiB of the body are read. A test of a repository with `availability.mode: offline`, or while `proxy.global_offline` is on, reports `offline` and sends nothing. The operation needs `repositories:write-config`, answers 404 for a missing key and 400 `not_applicable` for a repository that is not remote, and reads and writes no circuit state.

**Rationale:** A probe that shares the fetch path cannot report a configuration that fetches do not use. The development agent would revisit the offline rule if administrators need to test before switching a repository on; a separate `force` member would then be the way, not a silent exception.

**Applied in the spec:** R-REPO-003 (the registration), R-REPO-024 (the result, its codes and the criteria).

### Q-REPO-91 · What an omitted remote member takes, and which members are refused until they are built

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-024 lists a default for every member but says formats "propose entries for well-known upstreams" for `extraHosts`, and a URL "required"; three members, the client certificate, the headers and the named proxy, belong to P1 requirements, and two more members (pass-through, unused cleanup) to P1 behavior.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Proposals only for the well-known upstream; a P1 member is refused with `not_applicable` until it is built | `extraHosts` takes the format's entries when `url` is omitted or equals the format's upstream; `clientCertificate`, `headers`, `outboundProxy.mode: named`, `cache.storeArtifacts: false`, `cache.unusedRetentionHours` above 0 are 422 `not_applicable`; `outboundProxy.mode: direct` works | No host is trusted for a private upstream that was never meant to reach it; no accepted setting is silently ignored | An operator who sets a P1 member gets a refusal now instead of a no-op |
| (b) Always propose the format's hosts | `extraHosts` defaults to the proposal whatever the URL | Simpler default | A private NuGet feed would allow `*.nuget.org` redirects it never asked for |
| (c) Accept P1 members and ignore them | Stored, not enforced | Documents are portable to a later version | A header or a certificate that is not sent looks configured |

**Choice:** (a). The same rule makes `url` required for a format without a default upstream, normalizes an empty path to `/`, and, for the cache periods of a format that has none (no metadata documents), takes 0. The update path calls a hook, `RemoteDeps.OnUpstreamChanged`, in its transaction when `url`, `extraHosts`, `authMode`, `username`, `password`, `clientCertificate` or `headers` changed, where R-REPO-011 asks for the invalidation of R-REPO-045; the hook is bound by the cache invalidation.

**Rationale:** Refusing is the only behavior that keeps the stored document and the enforced behavior equal. The development agent would revisit it when the P1 requirements are built; each refusal is one line in `validateRemote` and goes with its requirement.

**Applied in the spec:** R-REPO-024 (the `extraHosts` row and the bullets on `url` and the refusals).

### Q-REPO-100 · The prefix of an invalidation is whole segments, validated up front

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-045 defines when a prefix covers a path, and the call takes the prefix from a request body. A prefix such as `/@acme`, `@acme/` or `a//b` can never cover a canonical path, and the prefix travels in the change payload, whose limit is 1 KiB.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Accept any string | The matcher compares as written | No rule to document | The operator's typo is a silent no-op that still answers 200; the payload is unbounded |
| (b) Normalise | Trim leading and trailing slashes | Forgiving | `a/` and `a` become one thing; a client cannot tell what was recorded |
| (c) Validate | Empty, or segments without a leading or trailing slash, an empty segment or a dot segment; valid UTF-8 without control characters; at most 256 bytes; otherwise `422 validation_failed` with the pointer `/path` | A mistake is answered, not stored; the payload is bounded | A client that sends a trailing slash must learn the rule |

**Choice:** (c). The entry list applies the same rule to `pathPrefix`, as a query parameter with `400`.

**Rationale:** the longest npm package name is 214 characters, so 256 bytes covers the name rule of the MVP formats with the scope. The development agent would revisit the limit when a format with longer paths is added (Maven coordinates), by raising it together with the payload encoding, which carries the prefix as base64url (Q-OPS-154).

**Applied in the spec:** the prefix bullet of R-REPO-045; R-OPS-021.

### Q-REPO-101 · The row is stamped inside its transaction, and the count is of metadata entries only

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-045 compares the row's time with the `validated_at` of an entry. The column default `now()` is the start of the transaction, and the transaction also counts up to a million entries.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `DEFAULT now()` | The time of the start of the transaction | Nothing to write | A fill that validates an entry while the transaction runs gets a `validated_at` after the row's time and escapes an invalidation that commits after it |
| (b) `clock_timestamp()` in the insert | The time of the statement; the insert is the first statement and the count follows it | The window shrinks to the rest of the transaction | Still the database's clock, not the commit's |
| (c) The application's clock | The replica stamps the row | One clock per replica | Replica clocks differ; the page compares with times that the database wrote |

**Choice:** (b). The count is of the metadata entries under the prefix (kind `metadata`, those marked gone included, no artifact), and the scope `negative` answers 0 without a query.

**Rationale:** a row can only err by the length of one count in the invalidation's disfavour, and the 5 s allowance of R-REPO-028 covers the negative entries that the replicas time themselves. The development agent would revisit this if the proxy engine stamps `validated_at` with a replica clock instead of the database's: the comparison then needs the same allowance as the negative entries.

**Applied in the spec:** the mechanism and count bullets of R-REPO-045.

### Q-REPO-102 · The matcher keeps the newest time per prefix and applies a row from its payload

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: every request for a cached document asks whether an invalidation covers it. The rows are few (bounded by the table and its retention) but the questions are many, and R-OPS-021 forbids a database round trip per request.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A list of rows, scanned per request | Compare with every row of the repository | Trivial | The cost of a request grows with the number of invalidations |
| (b) A map from prefix to the newest creation time, per scope class | A lookup tries the empty prefix, each leading segment and the path: one map access per segment | Constant in the number of rows; no allocation; immutable snapshots need no lock | A change rebuilds the repository's maps (rare) |
| (c) A trie of segments | Walk the path | The same bound | More code for the same answer |

**Choice:** (b). A replica loads every row at start (a failure stops the start-up), applies a new row from its change payload without a query, and reloads a repository after a deletion notice, every repository after a flush of the area, and all of them every `cache.snapshot_safety_ttl`. A reload merges the rows that arrived by payload while its query ran. A row leaves memory only when a reload no longer finds it in the database.

**Rationale:** a lookup takes about 30 ns without an allocation with 50,000 rows (benchmark in the package). The safety reload covers the rows that the cascade of a repository deletion removed without a notice. The development agent would revisit this if the rows of a single repository grow into the millions, which the retention of R-REPO-045 should prevent.

**Applied in the spec:** the in-memory bullet of R-REPO-045.

### Q-REPO-103 · The fold never extends an expiry, does not skip locked rows and checks once more before a deletion

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-045 folds a row into the entries validated before it by setting `expires_at` to the invalidation time, and deletes a `metadata` row once it is folded. Two cases are left open. An entry that already expires before the row would get a later expiry from a literal `SET expires_at = <time>`. A fill that was in flight when the row committed can store an entry with a `validated_at` before the row after the fold ran, and the deleted row can no longer invalidate it.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The literal statement | `SET expires_at = <time>` for the entries validated before the row | As written | Revives an entry that had expired |
| (b) `LEAST` and a condition | Only entries with `validated_at` before and `expires_at` after the row, set to the lesser, in batches of 5,000 locked in key order, each batch in its own transaction | Idempotent; shortens only; a run is resumable by the columns alone | Waits for a locked row instead of skipping it |
| (c) `SKIP LOCKED` | Leave a locked entry for the next run | Never waits | A skipped entry stays fresh in the database once the row is gone |
| Deleting a folded `metadata` row | (i) at once; (ii) one hour after the fold, with one more fold pass before the delete | (ii) finds the late fill | (ii) holds the row for an hour |

**Choice:** (b) with (ii): a folded `metadata` row is deleted one hour after `folded_at`, and every row is folded once more immediately before its deletion.

**Rationale:** an hour is far above the 60 s default of `cache.metadataRefreshTimeoutSeconds` and its 600 s limit, which bound how long a request waits for a fill. A fold of one million entries takes about 70 s on the integration stack (200 batches); the call that created the row does not wait for it. The development agent would revisit the hour if a fill can outlive it, for example through a detached refresh with a longer limit.

**Applied in the spec:** the fold and deletion bullets of R-REPO-045.

## What this does not cover

### H-4 · Malicious content from a legitimate upstream

Verification proves only that the bytes match what the upstream announced. A malicious package
published to a public registry is fetched, cached and served like any other: there is no scanning, no
vetting of new packages and no minimum-age rule (the catalog's scanning area is Later). Claims and pin
rules stop name confusion, not malicious public packages. `cacheFillAction: deploy` narrows who can
bring new packages in.

### H-5 · Version removals reach cached artifacts only where the format tracks versions

Component: upstream-gone handling of remote repositories (R-REPO-029). Subject: cached artifacts of
a version that the upstream withdrew while the package itself stays, in formats that do not track
removal per version; in the MVP that is NuGet. Live with the MVP. When a whole package disappears
upstream, R-REPO-029 stops serving its document and its cached artifacts. When only one version
disappears from a package document that still exists, the generic layer cannot tell which artifacts
the refreshed document no longer covers: clients that resolve through the document stop seeing the
version, but a request by exact path, through the remote, its cache view or a virtual repository,
still receives the cached artifact. npm tracks versions (R-NPM-026) and is not affected. What makes
it real: an upstream that deletes single versions (nuget.org deletes packages only for potentially
harmful content or infringement,
[deleting packages](https://learn.microsoft.com/en-us/nuget/nuget-org/policies/deleting-packages)),
and a client or script that downloads by exact path. An operator has to find and delete such items
by hand. Copies that clients downloaded earlier are never recalled.

### H-6 · Remote repositories reach private networks

The egress rules permit private ranges by default, because internal registries are common. A holder
of `repositories:write-config` can point a remote repository or the upstream test at an internal
HTTP service and read its responses through the cache. With an outbound proxy, trove checks the
target name's resolution, but the proxy connects on its own. The mitigations are the egress
`NetworkPolicy` and narrowed `deny_cidrs`; there is no per-repository egress scope.

### H-7 · Claimed names are observable

Claims and pin rules ignore the caller's permissions by design (R-REPO-023). A caller with `read` on
a virtual repository can learn that an internal name or prefix is claimed by comparing a 404 with the
public package of the same name. Names leak; contents do not.

### H-8 · The stored-secret key sits on every replica

Every replica holds the key that unseals upstream credentials, client-certificate keys and proxy
passwords: `web` replicas make upstream requests, and every replica opens the key ring's start-up
canary ([architecture.md](architecture.md) R-ARCH-021). Exec access to a trove pod, or read access
to the key's Secret, yields all of them. Adding `web` replicas for load adds copies of the key, not
new holders of a different kind. Valkey never holds a secret or a value derived from one
([architecture.md](architecture.md) R-ARCH-030). Integration with an external key management service
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

Component: virtual resolution (R-REPO-019, tier order), auto claims (R-REPO-023) and the shadowing
check (R-REPO-048). Subject: clients of virtual repositories that contain a local repository next
to remote members. Local members form the first resolution tier, and the first publish of a name
claims it, so a local `lodash@4.17.22` replaces the public `lodash` for every client of those
virtual repositories and hides the versions that exist only upstream.

Narrowed in the MVP by R-REPO-048 (Q-REPO-6). The first publish of a name that a remote member
serves needs `manage` or a prior manual claim, and the check fails closed while the upstream cannot
be asked. A `deploy` grant alone, however broad, and a leaked CI token can no longer do it. What
remains, live with the MVP:

- **Deliberate shadowing.** A `manage` holder, or a `deploy` holder inside a manual claim, still
  replaces the public package for every client. This is by design, since patched forks are
  legitimate. It is audited (`repository.remote_package_shadowed`), not prevented.
- **Configuration changes are not checked.** The check runs at publish time only. A local
  repository that collected names while no containing virtual repository had remote members, a
  sandbox for example, shadows without any decision once it is added to a virtual repository with
  remote members. The same happens when a remote member is added next to it, re-enabled, or its
  patterns are widened, and when a local repository whose auto claims are exact names joins a
  virtual repository in which another repository claims a prefix covering them (R-REPO-023 skips
  the auto claim only for claims in effect at publish time). These changes need
  `repositories:write-config`. What makes it real: adding sandbox or team repositories, or new
  remotes, to a shared virtual repository.
- **Offline remotes count as not serving.** A remote in offline mode (R-REPO-030, R-REPO-031) that
  holds no stored document of a name does not count. A name published while it is offline shadows
  that remote's package once it is back online.
- **Existence oracle.** The 403 `shadows_remote_package` tells a `deploy` holder that some remote
  member of a containing virtual repository serves a name, also when it cannot read that remote (a
  private partner registry, for example). Names leak; contents and remote keys do not, as in H-7.
- **Probes disclose new names upstream.** A first publish of a name outside every claim in effect
  sends one request for that name to the upstream of each exposed remote member, public
  registries included. The name of an internal package that was never resolved through trove
  before thereby reaches the upstream operator once. Claiming the internal prefix beforehand
  (`@acme/*`, `Acme.*`) removes the probe: with a claim of the publishing repository the check does
  not run, and with a claim in effect in the virtual repository nothing there is exposed.
