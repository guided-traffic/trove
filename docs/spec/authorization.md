# Authorization

Status: specification — not implemented

This page specifies how trove decides whether a request may go ahead once the caller is known.
It covers the principals, the resources a grant can name, the package-name and path patterns that
narrow a grant, what each of the six repository actions permits, the fixed set of system roles,
projects as the P1 unit of delegated administration, the evaluation algorithm, the treatment of
virtual and remote repositories, how a token's scope caps a principal's rights, package ownership,
the status code a refused request gets, how replicas keep their compiled permissions consistent,
what is audited, and the management API for all of it. How a caller proves its identity (OIDC
login, sessions, users, groups, service accounts, tokens and the scope structure) is specified in
[identity-and-authentication.md](identity-and-authentication.md). How a GitLab ID token is
verified, which identity mapping applies and how its grants are rendered is specified in
[gitlab-ci-oidc.md](gitlab-ci-oidc.md). How a request is classified and resolved through virtual
and remote repositories is specified in [repositories-and-proxy.md](repositories-and-proxy.md).
This page starts where those end: with an authenticated subject and a classified request.

Covers: this page draws on [identity-and-access.md](../features/identity-and-access.md)
(permission policies, actions, anonymous access, system roles, projects),
[access-tokens.md](../features/access-tokens.md) (scopes, transient subjects) and
[oidc-ci-integration.md](../features/oidc-ci-integration.md) (resource grants derived from CI
claims). It also covers the permission entries of
[api-admin-operations.md](../features/api-admin-operations.md),
[formats/npm.md](../features/formats/npm.md), [formats/nuget.md](../features/formats/nuget.md),
[artifacts.md](../features/artifacts.md), [ui-inventory.md](../features/ui-inventory.md) and
[authentication-providers.md](../features/authentication-providers.md).

## Model at a glance

| Term | Meaning on this page |
|---|---|
| Principal | Something a grant can name: a user, a group, a service account, a CI identity mapping, or the anonymous principal (R-AUTHZ-002). Principals live in the `principal` table of [identity-and-authentication.md](identity-and-authentication.md) (R-AUTH-016). |
| Subject | What authentication hands to authorization for one request: the principal set, the system roles the credential may exercise, the token scope and, for CI tokens, the frozen inline grants and the job's project identity. |
| Permission policy | A named, stored object: one resource set, and entries that give actions on that resource set to principals (R-AUTHZ-021). |
| Grant | One policy entry seen from the evaluator: principal × resource set × actions. CI inline grants ([gitlab-ci-oidc.md](gitlab-ci-oidc.md), R-CI-013) have the same shape. |
| Resource set | Repository selectors plus optional package-name and path patterns (R-AUTHZ-004, R-AUTHZ-005). Policies, token scopes and CI inline grants all use it. |
| Coordinate | The authorization view of the request descriptor of [repositories-and-proxy.md](repositories-and-proxy.md) (R-REPO-003): repository, class, format, request kind, canonical path, normalized package name, and the actions the operation needs. |
| Owner | An identity recorded as owner of a package name in an ownership domain (R-AUTHZ-018). |
| System role, capability | A fixed, instance-wide role and the management capabilities it bundles (R-AUTHZ-010). |

```text
 credential ──► authenticate ─────────► subject
                (identity-and-            principal set, roles, scope,
                 authentication.md,       CI inline grants, CI project identity
                 gitlab-ci-oidc.md)                │
                                                   ▼
 request ────► format plug-in ──► coordinate ──► evaluate (R-AUTHZ-015) ──► decision
               (R-REPO-003)       repo, class,    │ compiled grant index      │
                                  kind, path,     │ (R-AUTHZ-028)             ├─► status code (R-AUTHZ-016)
                                  name, actions   │ ownership rows            └─► audit event (R-AUTHZ-029)
                                                  │ (R-AUTHZ-018)
```

Decisions this page takes:

| Question | Decision | Requirement |
|---|---|---|
| Does any action imply another? | No. Presets expand to explicit action lists when a policy is written. | R-AUTHZ-001 |
| What is checked on a virtual repository? | `read` on the virtual repository as an endpoint gate; content actions on the member that holds or receives the content. | R-AUTHZ-009 |
| Does fetching an uncached item through a remote repository need a write-type action? | Configurable per remote repository (`cacheFillAction`); default `read`, `deploy` for frozen mirrors. | R-AUTHZ-008 |
| 401, 403 or 404? | 401 for callers without a credential; 404 for reads the caller may not make; 403 for refused writes and management operations. | R-AUTHZ-016 |
| Can a token scope grant rights? | Never; it only narrows. | R-AUTHZ-013 |
| Who may publish the next version of a package? | Its owners, or a holder of `manage` on it. The grants that allowed the first publish decide the owners. | R-AUTHZ-018 |
| Projects for delegated administration | P1, in a reduced form: repositories, project administrators, project-owned policies. Roles, environments and sharing stay P2. | R-AUTHZ-026 |

## Requirements

### R-AUTHZ-001 · Deny by default, allow-only grants
Priority: **MVP** · Covers: IAM-019

trove MUST refuse every operation that no grant, no system role (R-AUTHZ-010) and no project
administration right (R-AUTHZ-026) allows. A new installation has no permission policies; only
the break-glass administrator (INV-7, R-AUTH-014) can act until an administrator creates policies.

Grants MUST only add rights; there are no deny rules. A subject's rights can be narrowed only by:

1. exclude patterns inside the same resource set (R-AUTHZ-005);
2. the token scope (R-AUTHZ-013) and, for CI tokens, the caps and the triggering-user bound of
   [gitlab-ci-oidc.md](gitlab-ci-oidc.md) (R-CI-013, R-CI-024);
3. two checks that are not permissions and never grant anything: the endpoint gate of virtual
   repositories (R-AUTHZ-009) and package ownership (R-AUTHZ-018).

An exclude pattern in one policy MUST NOT remove a right that another policy grants.

Actions MUST be evaluated independently: holding one action never implies another at evaluation
time (`deploy` does not imply `read`, `manage` does not imply `deploy`). To keep common policies
short, the API and the UI accept presets in place of action lists. The server expands them when
the policy is written, so stored policies and all responses contain only the six actions of
R-AUTHZ-006.

| Preset | Expands to |
|---|---|
| `read` | `read` |
| `write` | `read`, `annotate`, `deploy` |
| `maintain` | `read`, `annotate`, `deploy`, `overwrite`, `delete` |
| `full` | `read`, `annotate`, `deploy`, `overwrite`, `delete`, `manage` |

Acceptance criteria:

- On a fresh installation, a user who has just logged in through OIDC and holds no grant runs
  `npm view lodash --registry https://trove.example.com/api/npm/npm-all/` and gets `E404`.
- `POST /api/v1/permission-policies` with an entry `"actions": ["write"]` answers 201, and the
  stored entry reads `"actions": ["annotate", "deploy", "read"]`.
- A principal holding only `deploy` on `npm-internal` can `npm publish` a new package there. Its
  `npm view` of that package through `npm-internal` gets `E404`, because `deploy` does not imply
  `read` (R-AUTHZ-016).

### R-AUTHZ-002 · Principals and the principal set of a request
Priority: **MVP** · Covers: IAM-013, TOK-008, TOK-015

Principal kinds and their reference syntax in this page's API:

| Kind | Reference | Defined in |
|---|---|---|
| User | `user:<username>` | [identity-and-authentication.md](identity-and-authentication.md) (R-AUTH-016) |
| Group | `group:<name>` | [identity-and-authentication.md](identity-and-authentication.md) (R-AUTH-018) |
| Service account | `sa:<name>` | [identity-and-authentication.md](identity-and-authentication.md) (R-AUTH-020) |
| CI identity mapping | `ci:<issuer>/<mapping>` | [gitlab-ci-oidc.md](gitlab-ci-oidc.md) (R-CI-009); mapping names are unique per CI issuer only, hence the issuer prefix |
| Anonymous | `anonymous` | [identity-and-authentication.md](identity-and-authentication.md) (R-AUTH-037), R-AUTHZ-003 |

- Policies, role bindings and owner records MUST reference principals by their immutable ID in
  the `principal` table, never by name. The API accepts and returns references by name and
  translates them. Renaming keeps the grants. Deleting a principal deletes its policy entries,
  role bindings and owner entries through the foreign keys (audited, R-AUTHZ-029). A principal
  created later under a reused name inherits nothing.
- For each request, the subject's principal set is computed from the credential:

| Credential | Principal set |
|---|---|
| UI session of user `u` | `user:u` plus every group of `u` |
| Personal access token (`trv_pat_`) of `u` | the same, resolved when the token is used |
| Service-account token (`trv_sat_`) of `s` | `sa:s` plus every group of `s` |
| CI token (`trv_cit_`) | `ci:<issuer>/<m>` for every mapping `m` that matched at exchange, plus the groups those mappings list (R-CI-013); the token additionally carries its frozen inline grants and, from P1, the service accounts of R-CI-024 |
| No credential | `anonymous`, only while anonymous access is enabled (R-AUTHZ-003) |

- "Every group of `u`" means the local groups `u` belongs to plus the groups synchronized from
  the identity provider's group claim (R-AUTH-008). Membership MUST be resolved when the request is
  evaluated, not when a token was issued; tokens carry no group lists. The membership cache and
  its invalidation belong to [identity-and-authentication.md](identity-and-authentication.md)
  (R-AUTH-023).
- There are no transient users. A CI token never stands for a user account and never picks up
  auto-join groups (R-AUTH-019, R-CI-013), which removes the catalog's widening of CI tokens
  through default groups (TOK-015, CIOIDC-014).
- A disabled or deleted principal's credentials are refused by authentication
  ([identity-and-authentication.md](identity-and-authentication.md)); authorization never sees them.

Acceptance criteria:

- Alice is in IdP group `team-web`, synchronized to trove group `team-web`, which holds `read`
  on `npm-internal`. After the IdP removes her from `team-web` and she logs in again, her existing
  personal access token no longer reads `npm-internal`, within the latency bound of R-AUTH-023.
- Group `everyone` is auto-join and holds `read` on `npm-all`. A CI token whose mappings list no
  groups gets 404 on `npm-all`.
- User `bob` is deleted, and a new user `bob` (different OIDC `sub`) logs in.
  `GET /api/v1/permission-policies?principal=user:bob` returns none of the old `bob`'s policies.

### R-AUTHZ-003 · Anonymous access
Priority: **MVP** · Covers: IAM-005, AUTHN-036

Whether anonymous access is on, and the hard limits that apply to it regardless of policies, are
specified in [identity-and-authentication.md](identity-and-authentication.md) (R-AUTH-037: YAML
ceiling `auth.anonymous.allowed`, runtime switch `anonymousAccess`, default off). This page adds
the rules for granting to `anonymous`:

- Policy entries for `anonymous` MUST contain only `read`. Any other action is rejected with 422
  `anonymous_write_action`.
- Policy entries for `anonymous` are allowed only in policies whose repository selectors are all
  explicit keys (R-AUTHZ-004). Pattern and class selectors are rejected with 422
  `anonymous_dynamic_selector`, so a repository created later is never exposed to anonymous
  callers by accident (IAM-005, IAM-020).
- `anonymous` never holds a system role, is never a project administrator and never becomes a
  package owner.
- Turning anonymous access off keeps the stored anonymous entries; they have no effect until it is
  turned on again.
- A repository with `require_authentication` set
  ([repositories-and-proxy.md](repositories-and-proxy.md)) refuses anonymous callers whatever the
  policies grant (R-AUTH-037).

Acceptance criteria:

- Anonymous access off, a policy grants `anonymous` `read` on `npm-public`:
  `npm view left-pad --registry https://trove.example.com/api/npm/npm-public/` without any
  `.npmrc` credential gets `E401`.
- Anonymous access on, same policy: the same command succeeds.
- `POST /api/v1/permission-policies` giving `anonymous` `["read", "deploy"]` answers 422
  `anonymous_write_action`; with the selector `@any-remote` it answers 422
  `anonymous_dynamic_selector`.

### R-AUTHZ-004 · Repository selectors
Priority: **MVP** · Covers: IAM-020

A resource set selects repositories through a list of selector strings. A repository is covered
when any selector matches it. Repository keys match `^[a-z0-9][a-z0-9._-]{1,63}$` (INV-5), so the
characters `*`, `?`, `@` and `:` never occur in a key and make the selector kinds unambiguous:

| Selector | Example | Matches |
|---|---|---|
| Key | `npm-internal` | That repository |
| Key pattern (contains `*` or `?`) | `npm-team-*` | Every current and future repository whose key matches the glob (`*` any run of characters, `?` one character) |
| Class | `@any-local`, `@any-remote`, `@any-virtual`, `@any` | Every current and future repository of that class (`@any`: every class) |
| Format filter (suffix `:<format>` on a pattern or class) | `@any-local:npm`, `npm-*:npm` | As before, restricted to repositories of that format |

- Pattern and class selectors are dynamic: a repository created later is covered as soon as the
  replicas have picked up the change (R-AUTHZ-028), without touching the policy.
- Key selectors MUST name an existing repository when the policy is written (422
  `unknown_repository` otherwise) and are stored by repository ID. Deleting a repository removes
  its key selectors (audited). A policy left without selectors stays stored, matches nothing and is
  reported with `"inert": true`. A repository created later under the same key is not covered.
- A format filter MUST name a format the server knows (422 `unknown_format`).
- Virtual repositories can be selected; R-AUTHZ-009 defines what a grant on one means. A key
  selector that names a virtual repository accepts only `read` and `manage`; other actions are
  rejected with 422 `virtual_action_not_applicable`. When a pattern or class selector covers
  virtual repositories, actions other than `read` and `manage` have no effect on them.
- The same selector strings are used in token scopes (R-AUTHZ-013) and CI inline grants (R-CI-013),
  where templating may produce keys and patterns but never class selectors.

Acceptance criteria:

- A policy grants `group:developers` `read` on `["npm-team-*"]`. An administrator creates local
  repository `npm-team-x`; within 5 seconds a member of `developers` can `npm view` a package
  published there, without any change to the policy.
- Repository `npm-old` is named by key in policy `legacy`, then deleted and created again.
  `GET /api/v1/permission-policies/legacy` reports `"inert": true` and grants nothing on the new
  `npm-old`.

### R-AUTHZ-005 · Package-name and path patterns
Priority: **MVP** · Covers: IAM-021, PKG-NPM-003, PKG-NPM-047

A resource set narrows its repositories in two independent dimensions:

| Dimension | Matched against | Pattern syntax |
|---|---|---|
| `packages` | The normalized package name of the coordinate | Name glob: `*` any run of characters except `/`; `**` any run including `/`; `?` one character except `/`; `\` makes the next character literal, whatever it is |
| `paths` | The canonical path of the coordinate inside the repository | Path pattern: `*` any run within one segment; `**` as a whole segment, any number of segments; `?` one character within a segment; `\` as above |

Each dimension has an `include` and an `exclude` list. Matching rules:

- An omitted or empty `include` list leaves the dimension unrestricted: it matches coordinates with
  or without a value in that dimension, unless the value matches an `exclude` pattern.
- A non-empty `include` list requires the coordinate to have a value in that dimension that matches
  at least one include pattern and no exclude pattern. A coordinate without a package name (a raw
  file outside any package, a repository-level operation) therefore never matches a resource set
  whose `packages.include` is non-empty.
- Both dimensions MUST match. Exclude beats include within one resource set and nowhere else
  (R-AUTHZ-001).
- Repository-level coordinates (search, NuGet service index, repository listing) match every grant
  whose selectors cover the repository, regardless of its patterns. Results that list items are
  then filtered per item (R-AUTHZ-016).
- Limits: each pattern 1 to 1024 characters, at most 256 patterns per list. Invalid syntax is
  rejected with 422 `invalid_pattern`.

Normalized package names come from the format plug-in (R-REPO-003). For the MVP formats:

| Format | Normalized name | Comparison | Pattern examples |
|---|---|---|---|
| npm | `name` or `@scope/name`, after percent-decoding (`@acme%2fui` and `@acme/ui` are one name; [npm scopes](https://docs.npmjs.com/cli/using-npm/scope)) | Exact, case-sensitive. New names are lower case by npm's rules; legacy mixed-case names in remote caches compare as stored. | `@acme/*`: every package of scope `acme`, no unscoped package. `acme-*`: unscoped names with that prefix. `*`: every unscoped package. `@*/*`: every scoped package. `**`: every package. |
| NuGet | Package ID, lower-cased as [formats/nuget.md](formats/nuget.md) normalizes IDs | Case-insensitive: patterns are stored as written and lower-cased by the same rule for comparison | `Acme.*`: `Acme.Core`, `ACME.Core.Tests`. `acme.core`: exactly that ID in any spelling. |
| Other formats | Defined on each format's spec page; until then none | — | Path patterns only |

The prefix form `Acme.*` deliberately mirrors how NuGet's public gallery describes reserved ID
prefixes ([ID prefix reservation](https://learn.microsoft.com/en-us/nuget/nuget-org/id-prefix-reservation)).

Name patterns are the recommended narrowing for package formats: one name grant covers a package's
metadata, its files, its dist-tags and its deprecation state, so the trap where a path pattern
admits the tarball but not the separately stored metadata (PKG-NPM-047) cannot arise
([formats/npm.md](formats/npm.md) relies on this). Path patterns are matched against the paths
the format plug-in reports. When a policy with a non-empty `paths.include` covers an npm or NuGet
repository, the policy API returns the warning `path_patterns_on_package_format` (R-AUTHZ-021).

Preconditions on coordinates:

- Paths and names MUST be canonical before evaluation. A request whose path contains `.` or `..`
  segments, empty segments, NUL or backslashes, also after one round of percent-decoding, is
  rejected with 400 before authorization runs. Decoding happens exactly once. The parser is part
  of request classification ([architecture.md](architecture.md), R-REPO-003).
- For uploads into a repository whose format has package names, the name used for authorization
  MUST be the name the content declares (npm `package.json` `name`, NuGet `.nuspec` `id`), which is
  also the name the package is indexed under. If the name derived from the request path or URL
  differs from the declared name, the upload is rejected with 400 before it is stored. This applies
  to the package protocol and to the raw file endpoint `/files/<repoKey>/…` alike.

| Resource set | Coordinate | Match? |
|---|---|---|
| packages include `@acme/*` | npm `@acme/ui` | yes |
| packages include `@acme/*` | npm `@acme-tools/ui` | no |
| packages include `@acme/*`, exclude `@acme/secret-*` | npm `@acme/secret-keys` | no |
| packages include `Acme.*` | NuGet `ACME.Core` | yes |
| packages include `Acme.*` | NuGet `AcmeCore` | no |
| paths include `releases/**` | generic `releases/1.0/app.zip` | yes |
| packages include `@acme/*` | raw file `/files/npm-internal/readme.txt` (no package name) | no |
| packages include `a\*b` (a rendered CI template) | npm `a*b` / `ab` | yes / no |
| no include lists | anything in the selected repositories | yes |

Acceptance criteria:

- A policy grants `read` on `npm-internal` with `packages.include: ["@acme/*"]`. `npm view
  @acme/ui` and `npm install @acme/ui` succeed; `npm view @other/ui` gets `E404`.
- A policy grants `read` on `nuget-internal` with `packages.include: ["Acme.*"]`.
  `dotnet add package acme.core --source
  https://trove.example.com/api/nuget/nuget-internal/v3/index.json` succeeds.
- `curl -X PUT --data-binary @evil-1.0.0.tgz
  https://trove.example.com/files/npm-internal/@acme/ui/-/ui-9.9.9.tgz` with a tarball whose
  `package.json` names `@other/evil` answers 400 and stores nothing.
- `GET /files/npm-internal/%2e%2e/secret` answers 400.

### R-AUTHZ-006 · Repository actions
Priority: **MVP** · Covers: IAM-022

| Action | Local repository | Remote repository | Virtual repository |
|---|---|---|---|
| `read` | Download files; read package metadata and version lists; appear in search results and listings; read item details | Serve cached content; fetch uncached content when `cacheFillAction` is `read` (R-AUTHZ-008) | Use the repository as an endpoint (R-AUTHZ-009) |
| `annotate` | Change descriptive metadata that does not change what a client resolves or installs: item properties (P1, [storage.md](storage.md)), descriptions | Item properties on cached content | No effect |
| `deploy` | Create content that does not exist yet (a new file, a new package version), and change metadata that steers resolution: npm dist-tags and deprecation, NuGet relist | Fetch uncached content when `cacheFillAction` is `deploy` (R-AUTHZ-008) | No effect (writes are checked on the target, R-AUTHZ-009) |
| `overwrite` | Replace an existing file with different bytes; needed together with `deploy` | No effect | No effect |
| `delete` | Delete files, versions and whole packages (npm unpublish, NuGet delete) | Evict cached content | No effect |
| `manage` | Repository-scoped administration of content: package ownership (R-AUTHZ-019), metadata recalculation, restore from the trash can (P1), effective permissions of items (R-AUTHZ-024), delegated policy editing (R-AUTHZ-025) | Same, plus invalidating cached metadata | Invalidate the merged-metadata cache of the virtual repository |

Dist-tags, deprecation and relisting need `deploy`, not `annotate`: moving `latest`, deprecating a
version or relisting one changes what `npm install` or NuGet's version selection picks, which is a
release decision ([formats/npm.md](formats/npm.md) takes the same position for npm).

Repository configuration (creating, changing and deleting repositories) is not a repository action;
it needs a system role (R-AUTHZ-010, R-AUTHZ-012) or project administration (R-AUTHZ-026).

Compound operations:

| Operation | Required |
|---|---|
| Upload to a path that holds nothing | `deploy` |
| Upload to an existing path, identical SHA-256 | `deploy` (nothing changes; the format may still refuse the duplicate, as npm does for existing versions) |
| Upload to an existing path, different content | `deploy` and `overwrite`; formats with immutable versions refuse regardless of `overwrite` ([formats/npm.md](formats/npm.md)) |
| Delete | `delete` |
| Copy A → B (P1) | `read` on A; `deploy` on B, plus `overwrite` on B if the target exists |
| Move A → B (P1) | `read` and `delete` on A; `deploy` on B, plus `overwrite` on B if the target exists |

Acceptance criteria:

- With `read` and `deploy` but not `overwrite` on generic repository `files-internal`,
  `curl -T app.zip https://trove.example.com/files/files-internal/releases/app.zip` succeeds the
  first time, succeeds again with the same bytes, and answers 403 with different bytes.
- With `delete` but not `deploy` on `npm-internal`, `npm unpublish @acme/ui@1.0.0` succeeds and
  `npm publish` answers 403.

### R-AUTHZ-007 · Action map for npm and NuGet operations
Priority: **MVP** · Covers: PKG-NUGET-019, PKG-NUGET-022, PKG-NPM-015, PKG-NPM-019, PKG-NPM-021

The format plug-in turns each protocol request into a coordinate and its required actions. This
table is the authorization contract for the MVP formats; protocol details are on
[formats/npm.md](formats/npm.md) and [formats/nuget.md](formats/nuget.md), and new formats add
their table on their own spec page following the same rules. "Ownership" marks operations that
also pass the ownership check of R-AUTHZ-018.

npm:

| Client operation | Coordinate | Actions | Ownership |
|---|---|---|---|
| `npm view`, `npm install`: package document | name | `read` | — |
| Tarball download | name, path | `read` | — |
| `npm publish` of a new version (with its dist-tag) | name | `deploy` | yes |
| `npm publish` of an existing version | name | refused by the format (immutable versions) | — |
| `npm unpublish name@version`, `npm unpublish name` | name | `delete` | yes |
| `npm dist-tag add` / `rm` | name | `deploy` | yes |
| `npm deprecate` | name | `deploy` | yes |
| `npm search` | repository level; results filtered per name | `read` | — |
| npm metadata recalculation | repository level | `manage` | — |
| `npm whoami` | none | an authenticated subject | — |

A package document `PUT` is classified by comparing it with the stored document: an added version
needs `deploy`, a removed version `delete`, changed dist-tags or deprecation messages `deploy`. A
request that does several of these needs every action involved.

NuGet:

| Client operation | Coordinate | Actions | Ownership |
|---|---|---|---|
| Service index | repository level | `read` | — |
| Flat container (version list, package download), registrations | ID | `read` | — |
| Search, autocomplete | repository level; results filtered per ID | `read` | — |
| `dotnet nuget push` of a new version | ID | `deploy` | yes |
| Push of an existing version where the repository allows replacement | ID | `deploy`, `overwrite` | yes |
| `dotnet nuget delete` | ID | `delete`, whether the repository deletes or unlists | yes |
| Relist | ID | `deploy` | yes |
| Symbol package push (P1 on the format page) | ID | `deploy` | yes |

The NuGet API key header carries a credential (INV-8) and plays no further part in authorization.

Raw file access through `/files/<repoKey>/<path>` maps `GET`/`HEAD` to `read`, `PUT` to `deploy`
(plus `overwrite` when the path exists with different content) and `DELETE` to `delete`. In a
repository whose format has package names, the format plug-in classifies the path, so name
patterns and ownership apply to raw requests exactly as to protocol requests (R-AUTHZ-005).

Acceptance criteria:

- With `read` and `annotate` but not `deploy` on `@acme/ui`, `npm dist-tag add @acme/ui@1.2.0
  stable` gets `E403`. With `read` and `deploy`, the same command and `npm publish --tag next` of
  1.3.0 both succeed.
- With `deploy` but not `delete` on `nuget-internal`, `dotnet nuget delete Acme.Core 1.0.0
  --source https://trove.example.com/api/nuget/nuget-internal/v3/index.json --api-key
  "$TROVE_TOKEN" --non-interactive` answers 403.
- A user with `read` only on `Acme.*` in `nuget-internal` gets search results without any ID
  outside `acme.*`, and the total hit count counts only those.

### R-AUTHZ-008 · Remote repositories: who may populate the cache
Priority: **MVP** · Covers: IAM-023, PKG-NPM-038, PKG-NUGET-019

Every remote repository carries the setting `cacheFillAction` (a field of the remote repository in
[repositories-and-proxy.md](repositories-and-proxy.md)): `read` or `deploy`. The populate check of
the remote request path (R-REPO-025, step 9) is this setting.

- Serving cached content needs `read`.
- Fetching a path that is not in the cache, and refreshing a cached metadata document whose cache
  period has expired, need `read` plus the action named by `cacheFillAction`. Both count as
  populating: a refreshed document advertises versions whose files a read-only caller could not
  fetch.
- A caller who may read but not populate gets what the cache holds. A direct request for an
  uncached path answers 403 `populate_not_permitted` (the caller may read the repository, so 403
  reveals nothing, R-AUTHZ-016). An expired metadata document is served as it is, without
  contacting the upstream. Through a virtual repository the member counts as a miss, and npm
  restricts the remote member's document to versions whose tarballs are cached
  ([formats/npm.md](formats/npm.md)). Only populating requests record negative-cache entries.
- Evicting cached content needs `delete`; invalidating cached metadata needs `manage`.
- Remote repositories never accept uploads, whatever the grants
  ([repositories-and-proxy.md](repositories-and-proxy.md)).
- **No populating through the session cookie.** A request authenticated by the session cookie
  ([identity-and-authentication.md](identity-and-authentication.md) R-AUTH-001) MUST NOT pass the
  populate check: it gets what the cache holds, exactly like a caller without the populate action.
  The cookie accompanies top-level navigations from other sites and same-origin image loads, so a
  link or a README image could otherwise make an administrator's browser bring an
  attacker-chosen package into a frozen mirror. Package clients authenticate with tokens and are
  not affected; the UI shows cached content only.

Recommendation and reasoning: the default is `read`. Requiring a write-type action for every fetch
is the most frequent reason why one developer gets a package and another gets 404 from the same
proxy, and it forces administrators to hand out `deploy` on repositories nobody can deploy to.
`deploy` remains available for frozen mirrors: only principals given `deploy` (for example release
CI jobs) bring new third-party packages into the organization, and everyone else reads what is
already cached. Q-AUTHZ-4 records the alternative.

Configuration (per remote repository, through the repository API):

```json
{ "cacheFillAction": "read" }
```

`"read"` is the default (proposed); `"deploy"` is the frozen-mirror mode.

Acceptance criteria:

- Remote `npmjs` with `cacheFillAction: read`, Bob holds `read` on it: `npm view left-pad
  --registry https://trove.example.com/api/npm/npmjs/` succeeds with an empty cache.
- The same remote switched to `deploy`, Bob holds `read` only: `npm view is-odd` for a package
  not yet cached gets `E403` directly on `npmjs`, and `E404` through `npm-all`, and the outbound
  request log shows no upstream request. After a principal with `deploy` has fetched it, Bob's
  command succeeds.
- With `cacheFillAction: deploy`, Bob's request for a cached document that expired 10 minutes ago is
  answered from the cache, and no upstream request is made.
- Carol holds `read` and `deploy` on `npmjs` with `cacheFillAction: deploy`. `GET
  /api/npm/npmjs/is-odd` for the uncached package, carrying only Carol's session cookie, answers
  403 `populate_not_permitted`, and the test upstream records no request. The same request with
  Carol's personal access token fetches the package.

### R-AUTHZ-009 · Virtual repositories
Priority: **MVP** · Covers: IAM-024, PKG-NPM-042, PKG-NPM-045, PKG-NUGET-027

1. **Endpoint gate.** Every request addressed to a virtual repository requires `read` on the
   virtual repository itself. Without it, the request is refused before any member is consulted.
2. **Reads are decided per member.** During resolution (R-REPO-019 in
   [repositories-and-proxy.md](repositories-and-proxy.md)), a member is consulted only if the
   subject holds `read` on the member for that coordinate. Members the subject cannot read are
   skipped silently. Nested virtual repositories (P1) apply rule 1 at each level.
3. **Fetching through a virtual repository** is checked on the remote member with that member's
   `cacheFillAction` (R-AUTHZ-008).
4. **Merged metadata** (npm package documents, NuGet registrations, search results) MUST be built
   only from members, and items, the subject may read. A cached merged document (R-REPO-020) MUST
   NOT be served to a subject whose set of readable members for that package differs from the set
   it was built for. Pin rules and namespace claims (R-REPO-023) are applied independently of the
   caller's permissions, so a caller who cannot read an internal package gets 404 rather than a
   public package of the same name.
5. **Writes through a virtual repository** go to its deploy target (R-REPO-021 in
   [repositories-and-proxy.md](repositories-and-proxy.md)). Actions and ownership are checked on
   that target with the same coordinate, and the gate of rule 1 applies as well. Grants of
   `annotate`, `deploy`, `overwrite` or `delete` on the virtual repository itself have no effect.
6. **Anonymous callers** may use a virtual repository only if `anonymous` holds `read` on the
   virtual repository and on every member, recursively. Otherwise every anonymous request through
   it gets 401. The client then retries with its configured credentials and sees the full view,
   instead of an incomplete merge that it cannot tell apart from the real one.
7. A virtual repository appears in repository listings and in client setup instructions only for
   subjects holding `read` on it.

This deliberately differs from the catalog, where a virtual repository has no permissions of its
own (IAM-024). The gate lets administrators decide which aggregated endpoints a group uses, at the
cost of one extra selector (`@any-virtual:npm` covers all npm virtual repositories). Because
content is always decided on the member, rearranging the members of a virtual repository never
exposes content to someone who could not read it directly. Q-AUTHZ-1 keeps the alternative open;
[repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-019 step 1 applies the gate.

Acceptance criteria:

- Virtual `npm-all` has members `npm-internal` and `npmjs`. Alice holds `read` on `npm-all` and
  `npm-internal` only: `npm view left-pad --registry https://trove.example.com/api/npm/npm-all/`
  gets `E404`; after `read` on `npmjs` is added, it succeeds.
- `@acme/ui` 1.0.0 is in `npm-internal-a` (Alice may read it) and 2.0.0 in `npm-internal-b` (she
  may not); both are members of `npm-all`. `npm view @acme/ui versions` through `npm-all` shows
  only `1.0.0` to Alice. Carol, who may read both, sees both, also when Alice's request came first.
- Anonymous access is on, and `anonymous` holds `read` on `npm-all` and `npmjs` but not on
  `npm-internal`: an anonymous `npm view left-pad` through `npm-all` gets 401.
- Alice holds `read` on `npm-all` and `deploy` on `npm-internal` (the deploy target):
  `npm publish --registry https://trove.example.com/api/npm/npm-all/` succeeds. Without `read` on
  `npm-all`, the same command gets 403.

### R-AUTHZ-010 · System roles and capabilities
Priority: **MVP** · Covers: IAM-002, IAM-016, ADMAPI-001

System roles are a fixed set; trove has no custom roles. A role is a bundle of capabilities,
and every management operation names the capability it requires. `admin` is MVP; the other roles
are P1 (R-AUTHZ-011, R-AUTHZ-012). The capability names are fixed now so that the API names them
from the start. Capabilities that [identity-and-authentication.md](identity-and-authentication.md)
lists as system scope items (R-AUTH-025) use the same names; the others extend that list.

| Capability | `admin` | `security-admin` | `auditor` | `repository-admin` | `webhook-admin` |
|---|---|---|---|---|---|
| `content:bypass`: every repository action on every repository, ownership not checked | yes | — | — | — | — |
| `identities:read`, `identities:write`: users, groups, service accounts | yes, yes | yes, yes | yes, — | — | — |
| `policies:read`, `policies:write` | yes, yes | yes, yes | yes, — | — | — |
| `roles:read`, `roles:write` | yes, yes | yes, — | yes, — | — | — |
| `ci:read`, `ci:write`: CI issuers and identity mappings | yes, yes | yes, yes | yes, — | — | — |
| `tokens:read`, `tokens:revoke`: tokens of any principal | yes, yes | yes, yes | yes, — | — | — |
| `repositories:read-config`, `repositories:write-config` | yes, yes | yes, — | yes, — | yes, yes | — |
| `projects:read`, `projects:write` | yes, yes | yes, — | yes, — | yes, — | — |
| `webhooks:read`, `webhooks:write` | yes, yes | — | yes, — | — | yes, yes |
| `settings:read`, `settings:write` | yes, yes | yes, — | yes, — | — | — |
| `security-settings:write`: the authorization settings of this page | yes | yes | — | — | — |
| `audit:read` | yes | yes | yes | — | — |

- Roles are bound to users, groups and service accounts (R-AUTHZ-022). A role bound to a group
  applies to every member, including members synchronized from the identity provider. This is how
  an IdP group carries administrator rights; there is no administrator flag on users or groups
  (IAM-016, R-AUTH-008).
- Roles are never bound to `anonymous` or to CI identity mappings (R-CI-013: CI tokens never carry
  system administration and cannot call management endpoints). Such bindings are rejected with
  422 `role_not_allowed`.
- The break-glass administrator (R-AUTH-014) always holds `admin` without a role binding, so
  recovery never depends on the state it recovers.
- A UI session exercises every role of its user. A token exercises roles only as its scope allows
  (R-AUTHZ-013).
- `content:bypass` makes the subject pass every repository check and the ownership check. It does
  not bypass the canonicalization rules of R-AUTHZ-005.

Acceptance criteria:

- IdP group `platform-admins` is synchronized to trove group `platform-admins`, which is bound to
  `admin`. A member logs in through the UI and can create a repository. After the IdP removes the
  user from the group and the user logs in again, the same call answers 403.
- `PUT /api/v1/role-bindings/auditor/ci:gitlab-internal%2Facme-publish` answers 422
  `role_not_allowed`.

### R-AUTHZ-011 · Security administrator and auditor roles
Priority: **P1** · Covers: IAM-006, ADMAPI-041, TOK-014

- `security-admin` manages who may do what: users, groups, service accounts, permission policies,
  CI issuers and identity mappings, token listing and revocation for any principal, and the
  authorization settings of this page. It reads repository configuration and settings but cannot
  change them, and it holds no content rights through its role.
- Escalation guards for `security-admin`:
  - role bindings are `admin`-only (`roles:write`);
  - changing the membership of a local group that is bound to any system role, or deleting such a
    group, is `admin`-only (403 otherwise), so a security administrator cannot add itself to an
    administrator group;
  - users and service accounts bound directly to `admin` or `security-admin` can be read but not
    changed or deleted by a `security-admin`.
- `auditor` reads everything that is not secret: configuration, policies, role bindings,
  identities, CI mappings, token metadata (never token values), settings and the audit log. It
  changes nothing and holds no content rights through its role. Secrets are write-only for every
  caller anyway.
- Read-only automation (ADMAPI-041, TOK-014) uses a service account bound to `auditor` and a token
  whose scope lists the needed system items, for example `"system": ["policies:read"]`
  (R-AUTHZ-013, R-AUTH-025).

Acceptance criteria:

- A `security-admin` creates policy `team-web-publish` (201) and gets 403 on
  `PUT /api/v1/role-bindings/admin/user:mallory` and on adding itself to group `platform-admins`.
- A service-account token with scope `{"inherit": false, "system": ["policies:read"]}` of an
  `auditor` service account gets 200 on `GET /api/v1/permission-policies` and 403 on
  `GET /api/v1/users`.

### R-AUTHZ-012 · Repository administrator and webhook administrator roles
Priority: **P1** · Covers: IAM-007, ADMAPI-027

- `repository-admin` creates, changes and deletes repositories of every class, sets their
  authorization-related settings (`cacheFillAction`, `packageOwnership`, `ownershipDomain`,
  `hideExistence`, `require_authentication`) and writes upstream credentials (write-only). It
  assigns repositories to projects (R-AUTHZ-026). It holds no content rights through its role and
  cannot edit policies. Because content is decided on the member (R-AUTHZ-009), adding a repository
  to a virtual repository exposes nothing to anyone who could not already read it.
- `webhook-admin` manages webhook subscriptions (specified in
  [operations-and-observability.md](operations-and-observability.md)) within two limits: a
  subscription may cover only repositories named by key on which the webhook administrator holds
  `read`, and only content events (deploy, delete, annotate of items and packages).

Acceptance criteria:

- A `repository-admin` creates remote repository `pypi-org` (201) and gets 404 on
  `GET /files/pypi-org/simple/` until a policy grants it `read`.
- A `webhook-admin` without `read` on `npm-secret` gets 403 when creating a subscription for
  `npm-secret`.

### R-AUTHZ-013 · Token scope as a ceiling
Priority: **MVP** · Covers: TOK-007, TOK-008, TOK-009, TOK-010, TOK-012, ADMAPI-040

The scope structure (`inherit`, `admin`, `repositories`, `system`), its limits, its defaults and
who may request what are specified in [identity-and-authentication.md](identity-and-authentication.md)
(R-AUTH-024, R-AUTH-025). This requirement fixes how the evaluator applies a scope:

- **Ceiling, never grant.** An action on a coordinate is allowed for a token only if the subject's
  grants allow it and the scope admits it. A scope that names actions the subject does not hold
  yields nothing for them.
- **Repository part.** `inherit: true` admits every repository right of the subject. Otherwise
  each element of `repositories` is a resource set (selectors of R-AUTHZ-004, `packages` and
  `paths` of R-AUTHZ-005) plus `actions`, and the scope admits an action on a coordinate if an
  element matches the coordinate and lists the action.
- **Roles.** `admin: true` lets the token exercise every capability of `admin`, `content:bypass`
  included, if and only if the subject holds `admin` at use time. Otherwise the token's
  capabilities are the `system` items for which one of the subject's roles grants that
  capability. `content:bypass` is never a `system` item.
- **Use time.** Everything is evaluated when the token is used. A right the subject loses
  disappears from the token at once (within the bound of R-AUTHZ-028); a right it gains appears
  only as far as the scope admits it.
- **CI tokens** carry `{"inherit": true}` over their CI grants; the caps of R-CI-013 act as a
  further ceiling of the same kind.

Differences from the catalog, kept on purpose: a resource scope never grants rights the subject
does not have (TOK-012 lets administrators mint grants that no policy records; here every right
traces back to a policy, a role or a CI mapping, which keeps audit and explanation complete).
Group-only tokens (TOK-010) are tokens of a service account in those groups (R-AUTH-020). The
administrator scope (TOK-009) is `admin: true`.

Acceptance criteria:

- An administrator's personal access token with the default scope gets 403 on
  `POST /api/v1/repositories`; a token with `{"inherit": true, "admin": true}` gets 201.
- Alice (who may deploy `@web/*`) creates a token whose scope admits only `read` on
  `["npm-all", "@any-local:npm", "@any-remote:npm"]`. With it, `npm install` through `npm-all`
  succeeds and `npm publish` gets `E403`.
- Alice's group loses `deploy`; her token whose scope still admits `deploy` can no longer publish.

### R-AUTHZ-014 · CI tokens in evaluation
Priority: **MVP** · Covers: CIOIDC-013, CIOIDC-014, TOK-015

How mappings match, how inline grants are templated and escaped, and what the issuance record
stores are specified in [gitlab-ci-oidc.md](gitlab-ci-oidc.md) (R-CI-009, R-CI-013, R-CI-014,
R-CI-020). This requirement fixes how the evaluator treats a CI token:

- **Rights.** The union of: policies whose entries name one of the token's mappings
  (`ci:<issuer>/<mapping>`); policies naming the groups those mappings list; the frozen inline
  grants of the issuance record; and from P1 the service accounts of R-CI-024. This union is
  narrowed by the caps of R-CI-013 and, where configured, by the triggering user's own rights at
  use time (R-CI-024, P1).
- **Inline grants are resource sets.** Each rendered inline grant MUST have the shape of
  R-AUTHZ-004 and R-AUTHZ-005 (selectors, `packages` or `paths`, `actions`) plus its `ownership`
  value, so the same matcher evaluates policies, scopes and CI grants. A backslash produced by the
  escaping of R-CI-014 makes the next character literal (R-AUTHZ-005), so a claim value can never
  widen a pattern.
- **No `manage`.** CI tokens never hold `manage` (R-CI-013). A policy entry that gives a CI mapping
  `manage` is rejected with 422 `action_not_allowed`.
- **Project identity.** The issuance record's issuer and `project_id` give the token the owner
  identity `ci:<issuer>:project:<project_id>` (R-CI-015), which package ownership uses
  (R-AUTHZ-018).
- **Lifecycle.** Disabling or deleting a mapping or CI issuer revokes its tokens (R-CI-017).
  Editing a mapping leaves the frozen inline grants of issued tokens unchanged until they expire;
  policy and group grants follow live changes.
- A CI token never has a user identity, never receives auto-join groups (R-AUTHZ-002) and never
  holds a system role (R-AUTHZ-010).

Acceptance criteria:

- An inline grant `packages.include: ["@acme/{{ project_path | segment -1 }}"]`, rendered for an
  ID token of a generic OIDC issuer (R-CI-026) whose `project_path` claim is `team/ui*`, yields
  `@acme/ui\*`. It matches only the literal name `@acme/ui*`, so the token cannot publish
  `@acme/ui-kit`.
- Removing a mapping from a policy's entries makes the next `npm install` with an already issued
  token of that mapping fail, without a new exchange.
- `POST /api/v1/authz/explain` for a CI token lists its inline grants with
  `"source": {"kind": "ci-inline", "ordinal": …}`.

### R-AUTHZ-015 · Evaluation algorithm
Priority: **MVP** · Covers: IAM-019, IAM-024

Evaluation MUST behave like the following pseudo-code. Implementations may reorder or cache steps
as long as every decision is the same. Grant evaluation MUST NOT query PostgreSQL in the steady
state; it reads the compiled grant index of R-AUTHZ-028. Ownership rows are read inside the write
transaction (R-AUTHZ-018).

```go
// Decide runs for every coordinate of a request; the request proceeds only if every
// coordinate is allowed. The resolver of R-REPO-019 calls MemberUsable per member.
func Decide(sub Subject, c Coordinate) Decision {
    idx := index.Current()
    if idx.Stale() {
        return Deny(ReasonStale)                                    // R-AUTHZ-028, answered 503
    }
    if sub.Anonymous && !settings.AnonymousAccess() {               // R-AUTH-037
        return Deny(ReasonUnauthenticated)
    }
    if c.Repo.Class == Virtual {
        if !Allowed(sub, idx, Read, c.RepositoryLevel()) {
            return Deny(ReasonVirtualGate)
        }
        if sub.Anonymous && !idx.AnonymousReadsAllMembers(c.Repo) {
            return Deny(ReasonAnonymousPartialVirtual)              // answered 401
        }
        if c.Kind == Write {
            c = c.WithRepo(c.Repo.DeployTarget())                   // writes are decided on the target
        }
    }
    for _, a := range c.RequiredActions {                           // e.g. {deploy, overwrite}
        if !Allowed(sub, idx, a, c) {
            return Deny(reasonFor(sub, idx, a, c))                  // no grant, scope, cap, or user bound
        }
    }
    if c.MutatesPackage && !sub.Capabilities().Has(ContentBypass) &&
        (c.Repo.PackageOwnership == Enforce || bindsProject(sub, idx, c)) {
        return OwnershipCheck(sub, idx, c)                          // R-AUTHZ-018, repeated at commit
    }
    return Allow()
}

func Allowed(sub Subject, idx *Index, a Action, c Coordinate) bool {
    if sub.Capabilities().Has(ContentBypass) {                      // roles as admitted by the scope
        return true
    }
    if !sub.Scope.Admits(a, c) || !sub.Caps.Admits(a, c) {          // ceilings, R-AUTHZ-013
        return false
    }
    if !granted(sub, idx, a, c) {
        return false
    }
    if sub.TriggeringUser != nil {                                  // P1, R-CI-024: narrows only
        return granted(sub.TriggeringUser, idx, a, c)
    }
    return true
}

func granted(sub Subject, idx *Index, a Action, c Coordinate) bool {
    for _, p := range sub.Principals {                              // user, groups, sa, ci mappings, anonymous
        for _, g := range idx.Grants(c.Repo.ID, p) {                // selectors already expanded
            if g.Actions.Has(a) && g.Resources.Matches(c) {
                return true
            }
        }
    }
    for _, g := range sub.CIInlineGrants {                          // frozen at exchange, R-CI-013
        if g.Actions.Has(a) && g.Resources.Covers(c.Repo) && g.Resources.Matches(c) {
            return true
        }
    }
    return false
}

func (r ResourceSet) Matches(c Coordinate) bool {
    if c.Level == RepositoryLevel {
        return true                                                 // patterns ignored, R-AUTHZ-005
    }
    return r.Packages.Match(c.PackageName, c.HasName) && r.Paths.Match(c.Path, c.HasPath)
}

func (ps PatternSet) Match(v string, present bool) bool {
    if len(ps.Include) == 0 {
        return !present || !ps.Excluded(v)
    }
    return present && ps.Included(v) && !ps.Excluded(v)
}

// MemberUsable decides whether the resolver may consult a member of a virtual repository.
func MemberUsable(sub Subject, idx *Index, m Repo, c Coordinate) bool {
    mc := c.WithRepo(m)
    if !Allowed(sub, idx, Read, mc) {
        return false                                                // skipped silently
    }
    if m.Class == Remote && m.NeedsUpstream(mc) {                   // uncached, or expired metadata
        return Allowed(sub, idx, m.PopulateAction, mc)              // otherwise the cache only
    }
    return true
}
```

- Writes MUST be evaluated again when they commit (after a long upload), against the grant index
  and the ownership rows current at that moment. A refusal at commit discards the upload and
  answers 403.
- `reasonFor` returns `deny.scope` when a grant would allow the action but a ceiling does not,
  `deny.triggering-user` when only the triggering-user bound refuses, and `deny.no-grant`
  otherwise.
- Performance target: with 10,000 policies, 200 groups in a principal set and 1,000 repositories,
  one `Decide` call SHOULD take at most 1 ms at the 99th percentile on one CPU core, measured by
  `trove_authz_evaluation_seconds`.

Worked trace (repositories and policies of the team example in
[Worked examples](#worked-examples); Alice is in `developers` and `team-web`, Bob in
`developers`, Carol in no group, anonymous access off):

| # | Subject | Request | Steps | Result |
|---|---|---|---|---|
| 1 | Alice | `npm view @web/button` via `npm-all` | gate: `read` on `npm-all` from `npm-consumers`; member `npm-internal`: `read` from `npm-consumers`; found | 200 |
| 2 | Alice | `npm publish @web/button@1.3.0` via `npm-all` | gate passes; target `npm-internal`; `deploy` on `@web/button` from `team-web-publish`; owners `{group:team-web}` include one of Alice's groups | success |
| 3 | Alice | `npm publish @pay/core` via `npm-all` | gate passes; `deploy` on `@pay/core`: no grant | 403 |
| 4 | Bob | `npm publish @web/button@1.3.1` | `deploy`: no grant | 403 |
| 5 | Carol | `npm view left-pad` via `npm-all` | gate: no `read` on `npm-all` | 404 |
| 6 | no credential | `npm view left-pad` via `npm-all` | anonymous access off | 401 |
| 7 | Alice, read-only token | `npm publish @web/button@1.3.2` | grant allows `deploy`, the scope does not (`deny.scope`) | 403 |
| 8 | Alice | `npm view left-pad` via `npm-all`, not cached | `npm-internal`: not found; `npmjs`: `read` and `cacheFillAction: read` pass; fetched | 200 |

Acceptance criteria: each row of the trace is an automated end-to-end test against two replicas,
issued once on each replica.

### R-AUTHZ-016 · Denial responses and hidden existence
Priority: **MVP** · Covers: IAM-032, ADMAPI-002, PKG-NUGET-016, PKG-NPM-007

A refused request is answered as follows (first matching row wins):

| Situation | Status |
|---|---|
| Grant index stale (R-AUTHZ-028) | 503 |
| No credential: anonymous access off, or `anonymous` lacks what the request needs (reads and writes alike) | 401 with the challenge of R-AUTH-002 |
| Read-type request (request kinds `artifact`, `metadata`, `search`, R-REPO-003) without `read` on the coordinate; for a virtual repository, without `read` on it | 404 |
| Read on a remote repository that would need to populate, caller may read but not populate (R-AUTHZ-008) | 403 `populate_not_permitted` |
| Write-type request without a required action | 403, naming the missing action |
| Ownership refused (R-AUTHZ-018) | 403 `package_owned_by_other_owner` |
| Management operation without the required capability | 403 |

- The 401 challenge for each endpoint family is specified in R-AUTH-002 (no `otp` on npm
  endpoints). NuGet clients send credentials only after a 401, so anonymous requests are always
  challenged, never answered with 404. The challenge does not depend on whether the resource
  exists, so it reveals nothing.
- 404 instead of 403 for refused reads follows [RFC 9110 §15.5.4](https://www.rfc-editor.org/rfc/rfc9110#section-15.5.4),
  which allows a server to hide a forbidden resource. A hiding 404 MUST be indistinguishable from
  the 404 for a missing resource on the same endpoint (same body, same headers), and authorization
  MUST run before the existence lookup, so the response time does not depend on existence either.
- A refused write gets 403 whether or not the target exists, and the message names only the action
  and the coordinate the caller sent. Existence matters only once the caller holds `deploy` (to
  tell a new upload from an overwrite); a caller with `deploy` learns of existence through its own
  uploads anyway.
- Listings, search results and their counts contain only items the subject may read.
- On the management API, capability checks run before any lookup, so their 403 does not depend on
  whether the addressed object exists. Reads of a repository and its content
  (`/api/v1/repositories/{key}` and below) follow the table above. Readers without
  `repositories:read-config` get the reduced repository document of
  [repositories-and-proxy.md](repositories-and-proxy.md).
- Error bodies follow [api-and-ui.md](api-and-ui.md) on the management API and the format's
  conventions on protocol endpoints. Audit events carry the reason codes of R-AUTHZ-029.

Acceptance criteria:

- Carol (no grants) gets byte-identical 404 responses for `GET
  /api/npm/npm-internal/@web%2fbutton` (exists) and `GET /api/npm/npm-internal/@web%2fnope`
  (missing).
- Carol's `npm publish` of `@web/nope` to `npm-internal` gets `E403`, as does her publish of the
  existing `@web/button`.
- `dotnet restore` against `nuget-internal` without a credential gets 401 with a `Basic`
  challenge, retries with the credential from `nuget.config` and succeeds.
- `npm publish` without a credential against a repository on which `anonymous` holds `read` gets
  `E401`.
- Bob gets 403 on `GET /api/v1/users/does-not-exist` and on `GET /api/v1/users/alice` alike.

### R-AUTHZ-017 · Configurable existence hiding
Priority: **P1** · Covers: IAM-032

- `authz.hide_existence` (global) and the per-repository setting `hideExistence` (`inherit`,
  `true`, `false`) control the 404 row of R-AUTHZ-016. With `false`, a refused read gets 403 if
  the resource exists and 404 if it does not. This is meant for troubleshooting, not production.
- The anonymous 401 and the write-type 403 are not configurable.

Configuration:

```yaml
authz:
  hide_existence: true  # default (proposed)
```

Acceptance criteria: with `hideExistence: false` on `npm-internal`, Carol gets 403 for
`@web/button` (exists) and 404 for `@web/nope`; with the default she gets 404 for both.

### R-AUTHZ-018 · Package ownership
Priority: **MVP** · Covers: trove-only

Package ownership makes "a CI job may write the packages it produces" hold even when several
principals hold `deploy` on overlapping name patterns: the first publish claims the name for the
owner its grant designates, and later changes are reserved to the owners and to holders of
`manage`. The CI side (which grants claim for a GitLab project, and the CI owner identity) is
specified in [gitlab-ci-oidc.md](gitlab-ci-oidc.md) (R-CI-015); the store, the general rules and the
administration operations are specified here.

When ownership is checked:

- Every local repository whose format has package names carries `packageOwnership`: `enforce` or
  `off` (a field of the repository in [repositories-and-proxy.md](repositories-and-proxy.md)). For
  new npm and NuGet local repositories the default (proposed) is `enforce` (Q-AUTHZ-2).
- Independently of that setting, ownership is checked for every write that a CI inline grant with
  `ownership: bind_project` allows (R-CI-015).
- Ownership is recorded per ownership domain and normalized package name. In the MVP the domain is
  the repository; P1 adds shared domains (R-AUTHZ-020). Remote and virtual repositories have no
  ownership; writes through a virtual repository are checked on its deploy target.

Owner identities:

| Owner | Who counts as owner |
|---|---|
| `user:<name>` | That user |
| `group:<name>` | Every subject whose principal set contains the group |
| `sa:<name>` | That service account |
| `ci:<issuer>/<mapping>` | Every CI token issued through that mapping |
| `ci:<issuer>:project:<project_id>` | Every CI token whose issuance record names that issuer and GitLab project ID (R-CI-015) |

A subject's owner identities are its principal set plus, for a CI token, its project identity.

Rules:

1. **Mutating operations** (those marked "Ownership" in R-AUTHZ-007) on a package name in a
   checked domain require, besides the action, one of:
   - the name is unclaimed and has no stored version: the operation claims it (rule 2);
   - one of the subject's owner identities is an owner of the name;
   - the subject holds `manage` on the coordinate;
   - the subject holds `content:bypass`.

   Otherwise the operation gets 403 `package_owned_by_other_owner` (audit reason
   `deny.ownership`).
2. **Claim on first publish.** The owners of a newly claimed name are:
   - if a CI inline grant with `ownership: bind_project` allowed the write: the token's project
     identity only;
   - if `content:bypass` allowed it: the user or service account itself;
   - otherwise: the principals of every grant that allowed `deploy` on the coordinate. A grant to
     group `team-web` makes `group:team-web` the owner, so every team member may publish later
     versions. A policy entry or inline grant of a CI mapping without `bind_project` makes the
     mapping the owner.
3. **Unclaimed names with existing versions** (data imported before ownership existed, ownership
   switched on later, owners released) need `manage` for every mutating operation. A successful
   publish by a `manage` holder claims the name by rule 2. Nobody takes over an existing package
   merely by holding `deploy`.
4. **Deleting every version does not release the name.** Owners stay recorded; releasing is an
   explicit operation (R-AUTHZ-019).
5. **Concurrency.** The claim is an insert into `package_ownership` in the write transaction,
   guarded by the primary key. Of two concurrent first publishes, one commits the claim; the other
   then sees an owned name, is checked by rule 1 and is refused unless it is an owner. No advisory
   lock is needed.
6. **Principal deletion** removes the principal's owner entries. A name whose last owner
   disappears this way falls under rule 3.
7. **Display.** CI project owners keep `project_path` as a display value, updated on every publish
   (R-CI-015).

Data model:

```sql
CREATE TABLE package_ownership (
  domain        text NOT NULL,            -- 'repo:<repositories.id>' or 'domain:<name>' (R-AUTHZ-020)
  format        text NOT NULL,
  package_name  text NOT NULL,            -- normalized name (R-AUTHZ-005)
  state         text NOT NULL CHECK (state IN ('claimed', 'released')),
  claimed_at    timestamptz NOT NULL DEFAULT now(),
  claim_event   uuid NULL,                -- audit event of the claim (R-AUTHZ-029)
  PRIMARY KEY (domain, format, package_name)
);

CREATE TABLE package_owner (
  id            uuid PRIMARY KEY,
  domain        text NOT NULL,
  format        text NOT NULL,
  package_name  text NOT NULL,
  owner_kind    text NOT NULL CHECK (owner_kind IN ('principal', 'ci_project')),
  principal_id  uuid NULL REFERENCES principal (id) ON DELETE CASCADE,  -- user, group, sa, ci_mapping
  ci_issuer_id  uuid NULL REFERENCES ci_issuers (id) ON DELETE CASCADE, -- ci_project only
  ci_project_id text NULL CHECK (length(ci_project_id) <= 64),         -- ci_project only
  display       text NOT NULL,            -- name, or project_path of the latest publish
  reason        text NOT NULL CHECK (reason IN ('first-publish', 'added', 'pre-assigned')),
  added_by      uuid NULL REFERENCES principal (id) ON DELETE SET NULL,
  added_at      timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (domain, format, package_name)
    REFERENCES package_ownership (domain, format, package_name) ON DELETE CASCADE,
  CHECK ((owner_kind = 'principal')  = (principal_id IS NOT NULL)),
  CHECK ((owner_kind = 'ci_project') = (ci_issuer_id IS NOT NULL AND ci_project_id IS NOT NULL))
);
CREATE UNIQUE INDEX package_owner_unique ON package_owner
  (domain, format, package_name,
   coalesce(principal_id::text, ci_issuer_id::text || ':' || ci_project_id));
```

Acceptance criteria:

- GitLab projects 4711 (`acme/web/frontend`) and 4712 (`acme/mobile/frontend`) both render the
  allowed name `@acme/frontend` (see [Worked examples](#worked-examples)). Project 4711 publishes
  1.0.0 first; the pipeline of 4712 then runs `npm publish` of 1.0.1 and gets `E403` with
  `package_owned_by_other_owner`; 4711 publishes 1.0.1 successfully.
- Alice publishes `@web/new-lib` through the `team-web` grant; Dave, also in `team-web`, publishes
  the next version successfully.
- `@legacy/util` was imported with versions and no owner. Alice, holding `deploy` but not
  `manage`, gets 403 on `npm publish`; `lead-web`, holding `deploy` and `manage`, succeeds, after
  which the name is owned by rule 2.
- Two `npm publish` runs of one new name by different, non-co-owning principals, started at the
  same moment on different replicas: exactly one succeeds.

### R-AUTHZ-019 · Package ownership API
Priority: **MVP** · Covers: trove-only

The package name is one path segment, percent-encoded (`@acme%2Ffrontend`); the server MUST accept
an encoded `/` in that segment.

| Method and path | Purpose | Status | Required |
|---|---|---|---|
| `GET /api/v1/repositories/{repoKey}/packages/{packageName}/owners` | Read state and owners | 200; 404 per R-AUTHZ-016 | `read` on the coordinate |
| `POST /api/v1/repositories/{repoKey}/packages/{packageName}/owners` | Add an owner. On a name that is unclaimed and has no version, this pre-assigns it: the name is claimed for the given owner before anything is published. | 201; 409 already an owner; 422 `unknown_principal` | an owner, `manage` on the coordinate, or `content:bypass`; pre-assigning needs `manage` or `content:bypass` |
| `DELETE /api/v1/repositories/{repoKey}/packages/{packageName}/owners/{owner}` | Remove an owner | 204; 404; 409 `last_owner` unless `?release=true` | as for adding; `release=true` needs `manage` or `content:bypass` |
| `GET /api/v1/package-owners?owner={owner}&repository={repoKey}` | Names owned by an identity, for offboarding and transfers | 200, cursor paging per [api-and-ui.md](api-and-ui.md) | `policies:read`, or the caller is that owner (directly or as group member) |

```json
{
  "repository": "npm-internal",
  "domain": "npm-internal",
  "format": "npm",
  "package": "@acme/frontend",
  "state": "claimed",
  "owners": [
    {
      "owner": "ci:gitlab-internal:project:4711",
      "display": "acme/web/frontend",
      "reason": "first-publish",
      "addedAt": "2026-10-28T09:12:44Z",
      "addedBy": null
    }
  ]
}
```

- Add request: `{"owner": "group:team-web"}` or `{"owner": "ci:gitlab-internal:project:4712"}`.
- An owner may hand a package over (add the new owner, then remove itself). Removing the last owner
  without `release=true` is refused.
- CI tokens never change owners. Adding, removing and releasing answer 403 `ci_token_restricted`
  for a CI token, also when its project identity is an owner. A pipeline runs the install scripts
  of its dependencies, so a CI token must not be able to hand a package to another project; a
  transfer needs a person or a service account.
- npm's maintainer commands (PKG-NPM-022, P2) may later map onto these operations.

Acceptance criteria:

```sh
curl -sS -H "Authorization: Bearer $TROVE_TOKEN" \
  https://trove.example.com/api/v1/repositories/npm-internal/packages/@acme%2Ffrontend/owners
# 200, owners as above

curl -sS -X POST -H "Authorization: Bearer $LEAD_TOKEN" -H 'Content-Type: application/json' \
  -d '{"owner":"group:team-web"}' \
  https://trove.example.com/api/v1/repositories/npm-internal/packages/@acme%2Fnot-yet/owners
# 201: lead-web (manage on npm-internal) pre-assigns a name nobody has published yet
```

- Removing the only owner without `release=true` answers 409 `last_owner`.
- A CI token of project 4711, owner of `@acme/frontend`, gets 403 `ci_token_restricted` on
  `POST …/packages/@acme%2Ffrontend/owners` with `{"owner":"ci:gitlab-internal:project:4712"}`, and
  the owners stay unchanged.
- After the pre-assignment above, a CI job of any project whose grant pattern covers
  `@acme/not-yet` gets 403 `package_owned_by_other_owner` on `npm publish`.

### R-AUTHZ-020 · Shared ownership domains
Priority: **P1** · Covers: trove-only

- A local repository MAY set `ownershipDomain: <name>`. All local repositories of the same format
  with the same domain name share one set of ownership records (`domain = 'domain:<name>'`).
  Without the setting, the repository is its own domain.
- Joining a repository into a domain whose records conflict with the repository's own (the same
  name owned by different owners) is refused with 409 `ownership_conflict`, listing the conflicting
  names; an administrator resolves them through R-AUTHZ-019 first.
- Recommended use: repositories that hold the same packages at different maturity (for example
  `npm-dev` and `npm-release`) share a domain, so a name owned in one cannot be claimed by someone
  else in the other.
- Changing the setting requires `repositories:write-config`.

Acceptance criteria: `npm-dev` and `npm-release` share domain `acme-npm`. `@acme/ui` is owned by
`group:team-web` through a publish to `npm-dev`. A member of `team-mobile` holding `deploy` on
`npm-release` gets 403 `package_owned_by_other_owner` publishing `@acme/ui` there.

### R-AUTHZ-021 · Permission policy API
Priority: **MVP** · Covers: IAM-031, ADMAPI-020, ADMAPI-021, ADMAPI-022, ADMAPI-023, ADMAPI-024

Representation:

```json
{
  "name": "team-web-publish",
  "description": "Team web publishes and tags its own npm scope",
  "resourceType": "repository",
  "project": null,
  "resources": {
    "repositories": ["npm-internal"],
    "packages": { "include": ["@web/*"], "exclude": [] },
    "paths": { "include": [], "exclude": [] }
  },
  "entries": [
    { "principal": "group:team-web", "actions": ["annotate", "deploy", "read"] },
    { "principal": "user:lead-web", "actions": ["deploy", "manage", "read"] }
  ],
  "inert": false,
  "warnings": [],
  "version": 4,
  "createdAt": "2026-10-28T09:12:44Z",
  "createdBy": "user:root",
  "updatedAt": "2026-10-28T10:01:02Z",
  "updatedBy": "user:sec-alice"
}
```

- `name` matches `^[a-z0-9][a-z0-9._-]{0,127}$`; `description` has at most 1024 characters;
  `resourceType` is `repository` (MVP) or `build` (P1, R-AUTHZ-027); `project` is P1
  (R-AUTHZ-026). `entries` may be empty (a policy can be prepared before principals are added) and
  holds at most one entry per principal. `inert`, `warnings`, `version` and the timestamps are
  read-only.
- Each policy addresses exactly one resource type, so the catalog's per-section operations
  (ADMAPI-024) are ordinary operations on a policy. Per-repository patterns are separate policies.
- CI mappings reference policies by name (R-CI-013); the binding is the policy entry of the mapping,
  so editing either side changes the same row.
- Updates are full replacements guarded by optimistic concurrency: `GET` returns
  `ETag: "<version>"`, and `PUT` requires `If-Match`
  ([RFC 9110 §13.1.1](https://www.rfc-editor.org/rfc/rfc9110#section-13.1.1)), answering 428
  without it ([RFC 6585 §3](https://www.rfc-editor.org/rfc/rfc6585#section-3)) and 412 on a version
  mismatch.
- Every successful write bumps the authorization epoch (R-AUTHZ-028) in the same transaction, emits
  an audit event with before and after (R-AUTHZ-029), and returns the new epoch in the response
  header `Trove-Authz-Epoch`, so a client can wait for propagation.
- Malformed JSON answers 400. Semantic validation answers 422 with one of: `unknown_principal`,
  `unknown_repository`, `unknown_format`, `invalid_pattern`, `invalid_action`,
  `duplicate_principal`, `anonymous_write_action`, `anonymous_dynamic_selector`,
  `virtual_action_not_applicable`, `action_not_allowed`. Warnings do not block saving:
  `path_patterns_on_package_format`, `selector_matches_nothing`.
- The declarative bootstrap file (configuration as code, P1,
  [operations-and-observability.md](operations-and-observability.md)) uses the same
  representation.

| Method and path | Purpose | Status | Required |
|---|---|---|---|
| `GET /api/v1/permission-policies` | List. Filters: `principal=<ref>` (with `expandGroups=true`, the default, also policies granting to the principal's groups), `repository=<key>` (policies whose selectors cover it), `project=<key>`; cursor paging per [api-and-ui.md](api-and-ui.md) | 200; 422 unknown filter value | `policies:read`; P1: project administrators see their project's policies, delegated managers the policies they manage |
| `POST /api/v1/permission-policies` | Create | 201 with `Location`; 400; 409 name exists; 422 | `policies:write`; P1: project administrators for project policies |
| `GET /api/v1/permission-policies/{name}` | Read | 200 with `ETag`; 404 | as for list |
| `PUT /api/v1/permission-policies/{name}` | Replace (the name is immutable) | 200; 400; 404; 412; 422; 428 | as for create; P1: delegated managers per R-AUTHZ-025 |
| `DELETE /api/v1/permission-policies/{name}` | Delete (`If-Match` optional) | 204; 404; 412 | as for create |
| `POST /api/v1/authz/validate-policy` | Validate without storing; returns errors, warnings and the repositories the selectors currently cover | 200 | `policies:read` |

Acceptance criteria:

- Create, read, replace with a stale `If-Match` (412), replace with the current one (200) and delete
  (204), all through `curl`; each change appears as one audit event.
- After a `PUT` returns epoch `n`, a request to another replica observes the change once that
  replica's `trove_authz_epoch` reports at least `n`, and no later than 5 seconds after the `PUT`.
- `GET /api/v1/permission-policies?principal=user:alice` returns `team-web-publish` (through
  `group:team-web`); with `expandGroups=false` it does not.

### R-AUTHZ-022 · Role binding API
Priority: **MVP** · Covers: IAM-002, IAM-016, ADMAPI-001

| Method and path | Purpose | Status | Required |
|---|---|---|---|
| `GET /api/v1/roles` | The fixed roles and their capabilities (R-AUTHZ-010) | 200 | any authenticated subject |
| `GET /api/v1/role-bindings?role=&principal=` | List bindings | 200 | `roles:read` |
| `PUT /api/v1/role-bindings/{role}/{principalRef}` | Bind (idempotent) | 201 new, 200 existing; 409 `auto_join_admin_conflict`; 422 `role_not_allowed`, unknown role or principal | `roles:write` |
| `DELETE /api/v1/role-bindings/{role}/{principalRef}` | Unbind | 204; 404; 409 `last_admin` | `roles:write` |
| `GET /api/v1/authz/me` | The caller's effective capabilities, scope summary and principal set | 200 | any subject |

- `principalRef` is percent-encoded (`group:platform-admins`, `sa:ci-bot`).
- Binding any role to an auto-join group is refused with 409 `auto_join_admin_conflict`; the
  identity page refuses the reverse (R-AUTH-019).
- Removing the last `admin` binding is refused with 409 `last_admin` while the break-glass
  administrator is disabled.
- `isAdmin` and `adminVia` on user records (R-AUTH-016) are derived from these bindings.

Data model:

```sql
CREATE TABLE authz_role_binding (
  role          text NOT NULL CHECK (role IN
                  ('admin', 'security-admin', 'auditor', 'repository-admin', 'webhook-admin')),
  principal_id  uuid NOT NULL REFERENCES principal (id) ON DELETE CASCADE,
  granted_by    uuid NULL REFERENCES principal (id) ON DELETE SET NULL,
  granted_at    timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (role, principal_id)
);
```

Acceptance criteria: `PUT /api/v1/role-bindings/admin/group:platform-admins` by an administrator
answers 201, and repeating it answers 200. `DELETE` of the only `admin` binding while the
break-glass administrator is disabled answers 409.

### R-AUTHZ-023 · Permission checks for the caller
Priority: **MVP** · Covers: UI-005

The UI (INV-17) and scripts need to know what the caller may do, without learning anything about
other principals.

- `POST /api/v1/authz/check` evaluates up to 100 checks for the caller's own credential, scope
  included:

```json
{
  "checks": [
    { "repository": "npm-internal", "package": "@web/button", "action": "deploy" },
    { "repository": "npm-internal", "path": "@web/button/-/button-1.2.0.tgz", "action": "delete" },
    { "repository": "npm-all", "action": "read" }
  ]
}
```

```json
{ "results": [ { "allowed": true }, { "allowed": false }, { "allowed": true } ], "epoch": 1842 }
```

- A check on a repository the caller cannot see answers `allowed: false`, exactly as for a missing
  repository. Checks of package-mutating actions include the ownership rule.
- Any authenticated subject may call it; anonymous callers get 401.
- The UI hides controls whose check fails; the server enforces every operation regardless.

Acceptance criteria: for Alice the example returns `[true, false, true]`; for Carol it returns
`[false, false, false]`, also when `npm-internal` is replaced by a key that does not exist.

### R-AUTHZ-024 · Effective permissions and decision explanation
Priority: **P1** · Covers: IAM-030, ART-031

- `GET /api/v1/authz/effective?repository={key}&package={name}` (or `&path={path}`) lists every
  principal that holds actions on the coordinate, with the actions and the policies that grant
  them. CI mappings whose inline grant templates could render to the coordinate are listed with
  `"conditional": true` and the template, because their tokens' grants depend on claims. Required:
  `policies:read`, or `manage` on the coordinate (the answer is then limited to that coordinate).
- `POST /api/v1/authz/explain` evaluates a decision for a principal or a token ID and returns the
  trace:

```json
{
  "decision": "deny",
  "reason": "deny.ownership",
  "status": 403,
  "subject": {
    "principals": ["ci:gitlab-internal/acme-publish"],
    "projectIdentity": "ci:gitlab-internal:project:4712",
    "capabilities": [],
    "scope": { "inherit": true }
  },
  "coordinate": {
    "repository": "npm-internal", "class": "local", "format": "npm",
    "package": "@acme/frontend", "path": null, "actions": ["deploy"]
  },
  "steps": [
    { "check": "grant", "action": "deploy", "result": "allow",
      "source": { "kind": "ci-inline", "ordinal": 1, "ownership": "bind_project" } },
    { "check": "ownership", "result": "deny", "owners": ["ci:gitlab-internal:project:4711"] }
  ],
  "epoch": 1842
}
```

  Required: `policies:read`, plus `tokens:read` when a token ID is given. Explanations contain no
  token values and no claims beyond the issuance record (R-CI-020).

Acceptance criteria: the explanation of trace row 7 of R-AUTHZ-015 names reason `deny.scope`. The
effective permissions of `npm-internal` / `@web/button` list `group:team-web` with `annotate`,
`deploy` and `read` from `team-web-publish`.

### R-AUTHZ-025 · Delegated policy management
Priority: **P1** · Covers: IAM-029

- A principal that holds `manage` in a policy's entries is a delegated manager of that policy. It
  may read the policy and change its entries through `PUT /api/v1/permission-policies/{name}`,
  provided `name`, `description`, `resourceType`, `project` and `resources` stay unchanged
  (otherwise 403).
- What a delegated manager may grant depends on `authz.delegation.mode`:

| Mode | May grant |
|---|---|
| `own-actions` | only actions it holds in this policy itself, never `manage` |
| `any-but-manage` | any action except `manage` |
| `any` | any action including `manage` |

- A delegated manager MUST NOT change an entry whose principal is in its own principal set (403).
  It can neither widen its own rights, also not indirectly through one of its groups, nor lock
  itself out.
- A delegated manager MUST NOT add `anonymous` (403). The catalog's finer control over anonymous
  grants by non-administrators (IAM-034) is P2.
- For the previous two rules, its own principal set also contains every service account it owns
  or may create tokens for (R-AUTH-020, P1) and every CI mapping in a CI delegation whose delegate
  is in its principal set (R-CI-023). Adding or changing an entry for such a principal answers 403.
  A delegated manager therefore cannot route rights to itself through a credential it controls,
  which under `any-but-manage` or `any` would exceed what it holds.
- CI mappings can be added as principals, without `manage` (R-AUTHZ-014).

Configuration:

```yaml
authz:
  delegation:
    mode: own-actions   # default (proposed); own-actions | any-but-manage | any
```

Acceptance criteria: `lead-web` (holding `deploy`, `manage` and `read` in `team-web-publish`) adds
`user:dave` with `["deploy", "read"]` (200). It is refused (403) when adding `["delete"]` or
`["manage"]`, and when changing the `group:team-web` entry, because it is a member. Under
`any-but-manage`, `lead-web`, owner of service account `sa-web-release`, is refused (403) when
adding `sa:sa-web-release` with `["delete"]`.

### R-AUTHZ-026 · Projects for delegated administration
Priority: **P1** · Covers: IAM-051, IAM-052, IAM-057

Decision: projects are not part of the MVP. In the MVP, administration is centralized (system
roles), and delegation happens per policy (`manage`, R-AUTHZ-025, P1) and, for CI, per CI issuer
(R-CI-023, P1). Projects follow as P1 in a reduced form, so that a team can administer its own
repositories and their policies without a system administrator. The catalog's project roles,
global roles, environments, repository sharing and project quotas (IAM-053 to IAM-064, IAM-068)
remain P2.

- A project has a key matching `^[a-z][a-z0-9-]{1,31}$` (immutable), a display name, a description
  and two privilege switches set by an administrator: `managePolicies` (default on) and
  `manageRepositories` (default off).
- A repository belongs to at most one project. `admin` and `repository-admin` assign and unassign
  existing repositories; moving one between projects requires `?move=true`. Repositories created by
  project administrators (with `manageRepositories`) MUST have keys starting with `<projectKey>-`
  and are assigned to the project. Repository keys stay globally unique and URLs do not change
  (INV-5, INV-6).
- Project administrators are users and groups listed on the project. Within their project they:
  - hold `manage` on every project repository (package ownership, cache invalidation);
  - with `managePolicies`: create, change and delete project policies (`"project": "<key>"`),
    granting any action (`manage` included, except to CI mappings) to users, groups, service
    accounts and CI mappings, never to `anonymous`;
  - with `manageRepositories`: create, change and delete the project's repositories.

  They cannot bind system roles, cannot see global policies (unless they hold `policies:read`) and
  cannot assign repositories outside their project. CI mapping delegation stays with R-CI-023; an
  administrator can name a project's administrator group as the delegate there (Q-AUTHZ-7).
- Project policies may select only repositories of their project: key selectors must name project
  repositories (422 `repository_outside_project`), and pattern and class selectors are evaluated
  only against the project's repositories. Global policies keep applying to project repositories;
  the effect is the union.
- A project can be deleted only when no repository is assigned (409 `project_not_empty`). Its
  policies and project administrator entries are deleted with it.

Data model:

```sql
CREATE TABLE project (
  id                  uuid PRIMARY KEY,
  key                 text NOT NULL UNIQUE CHECK (key ~ '^[a-z][a-z0-9-]{1,31}$'),
  display_name        text NOT NULL CHECK (length(display_name) BETWEEN 1 AND 128),
  description         text NOT NULL DEFAULT '' CHECK (length(description) <= 1024),
  manage_policies     boolean NOT NULL DEFAULT true,
  manage_repositories boolean NOT NULL DEFAULT false,
  created_at          timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE project_admin (
  project_id    uuid NOT NULL REFERENCES project (id) ON DELETE CASCADE,
  principal_id  uuid NOT NULL REFERENCES principal (id) ON DELETE CASCADE,
  PRIMARY KEY (project_id, principal_id)
);
-- repositories.project_id uuid NULL REFERENCES project (id): a column of the repositories table,
-- owned by repositories-and-proxy.md. authz_policy.project_id: see the data model of R-AUTHZ-028.
```

API:

| Method and path | Purpose | Status | Required |
|---|---|---|---|
| `GET /api/v1/projects` | List | 200 | `projects:read` (all) or project administrator (own) |
| `POST /api/v1/projects` | Create | 201; 409 key exists; 422 | `projects:write` |
| `GET /api/v1/projects/{key}` | Read | 200; 404 | `projects:read` or one of its administrators |
| `PUT /api/v1/projects/{key}` | Replace display name, description, switches | 200; 404; 412 | `projects:write` |
| `DELETE /api/v1/projects/{key}` | Delete | 204; 404; 409 `project_not_empty` | `projects:write` |
| `PUT /api/v1/projects/{key}/admins/{principalRef}` | Add a project administrator | 201 or 200 | `projects:write` |
| `DELETE /api/v1/projects/{key}/admins/{principalRef}` | Remove a project administrator | 204 | `projects:write` |
| `PUT /api/v1/projects/{key}/repositories/{repoKey}` | Assign (`?move=true` to move) | 204; 409 assigned elsewhere | `repositories:write-config` |
| `DELETE /api/v1/projects/{key}/repositories/{repoKey}` | Unassign | 204 | `repositories:write-config` |

Acceptance criteria:

- Project `web` has administrator group `web-leads`; `web-npm` is in it, `npm-internal` is not. A
  member of `web-leads` creates a policy with `"project": "web"` on `web-npm` (201) and is refused
  on `npm-internal` (422 `repository_outside_project`).
- The same project administrator creating repository `npm-web-extra` gets 403 while
  `manageRepositories` is off. With the switch on, that key gets 422 (missing prefix), and
  `web-extra` succeeds.

### R-AUTHZ-027 · Build resources
Priority: **P1** · Covers: IAM-025

- Policies with `"resourceType": "build"` select build records by build name: `resources.builds`
  holds `include` and `exclude` lists of name globs (syntax of the `packages` dimension). Repository
  selectors and path patterns do not apply to build policies.
- Actions: `read` (view build records and their files), `annotate` (properties on build records),
  `deploy` (upload and promote build records), `delete`, and `manage` (delegated management of the
  policy, R-AUTHZ-025). `overwrite` is not applicable (422 `invalid_action`).
- Build records and their storage are specified with build metadata (P1; the owning spec page is
  not yet assigned, Q-AUTHZ-8). Token scopes, CI grants, the evaluation algorithm and audit apply
  unchanged, with the coordinate (build name, action).

Acceptance criteria: a policy that gives `ci:gitlab-internal/acme-publish` `read` and `deploy` on
build names `acme/**` lets that mapping's tokens upload a build record named `acme/web/frontend`,
and refuses `other/app` with 403.

### R-AUTHZ-028 · Compiled grant index and invalidation across replicas
Priority: **MVP** · Covers: trove-only

Every replica evaluates against an in-memory grant index and keeps it consistent with PostgreSQL
through an epoch counter, `LISTEN/NOTIFY` and polling (INV-12).

- **Index.** Built from policies (including entries that name CI mappings), role bindings,
  repositories (key, class, format, project, authorization settings), projects and the
  authorization settings: per repository ID, a map from principal ID to compiled grants (selectors
  expanded against the current repository list, patterns compiled). A new index is built on the
  side and swapped in atomically. Requests in flight finish on the index they started with, except
  for the commit-time re-check of writes (R-AUTHZ-015).
- **Epoch.** Every transaction that changes an input of the index increments `authz_epoch.value`
  and calls `pg_notify('trove_authz', '<new epoch>')`. PostgreSQL delivers a notification only if
  its transaction commits ([NOTIFY](https://www.postgresql.org/docs/current/sql-notify.html)), so no
  replica acts on a change that was rolled back.
- **Not in this index:** group memberships, principal status, tokens and sessions (cached and
  invalidated by [identity-and-authentication.md](identity-and-authentication.md), R-AUTH-023, on
  channel `trove_auth`); CI mappings and issuance records ([gitlab-ci-oidc.md](gitlab-ci-oidc.md));
  ownership rows (read in the write transaction).
- **Listening and polling.** Each replica listens on a dedicated connection and rebuilds when a
  notification carries a higher epoch. Independently, it reads the epoch every `poll_interval` and
  rebuilds if it is behind. After its listening connection reconnects, it rebuilds unconditionally.
- **Bound.** A committed change is in force on every replica within `poll_interval` in the worst
  case, and typically within one second through notifications.
- **Fail closed.** If a replica has not confirmed the current epoch for longer than `max_staleness`,
  it answers 503 to every request that needs authorization and reports not ready
  ([ha-and-helm.md](ha-and-helm.md)).
- **Metrics:** `trove_authz_epoch` (gauge), `trove_authz_index_rebuild_seconds` (histogram),
  `trove_authz_index_staleness_seconds` (gauge), `trove_authz_evaluation_seconds`
  (histogram), `trove_authz_decisions_total{outcome, reason, action}` (counter).
- **Rebuild cost:** building the index from 10,000 policies and 1,000 repositories SHOULD take at
  most 500 ms.

Data model:

```sql
CREATE TABLE authz_epoch (
  singleton  boolean PRIMARY KEY DEFAULT true CHECK (singleton),
  value      bigint NOT NULL
);
INSERT INTO authz_epoch (value) VALUES (1);
-- in every changing transaction:
--   UPDATE authz_epoch SET value = value + 1 RETURNING value;
--   SELECT pg_notify('trove_authz', <value>::text);

CREATE TABLE authz_policy (
  id             uuid PRIMARY KEY,
  name           text NOT NULL UNIQUE CHECK (name ~ '^[a-z0-9][a-z0-9._-]{0,127}$'),
  description    text NOT NULL DEFAULT '' CHECK (length(description) <= 1024),
  resource_type  text NOT NULL DEFAULT 'repository' CHECK (resource_type IN ('repository', 'build')),
  project_id     uuid NULL REFERENCES project (id) ON DELETE CASCADE,       -- P1
  version        bigint NOT NULL DEFAULT 1,                                 -- ETag
  created_at     timestamptz NOT NULL DEFAULT now(),
  created_by     uuid NULL REFERENCES principal (id) ON DELETE SET NULL,
  updated_at     timestamptz NOT NULL DEFAULT now(),
  updated_by     uuid NULL REFERENCES principal (id) ON DELETE SET NULL
);

CREATE TABLE authz_policy_selector (
  policy_id      uuid NOT NULL REFERENCES authz_policy (id) ON DELETE CASCADE,
  ordinal        smallint NOT NULL,
  kind           text NOT NULL CHECK (kind IN ('key', 'pattern', 'class')),
  repository_id  bigint NULL REFERENCES repositories (id) ON DELETE CASCADE,  -- kind = 'key'
  key_pattern    text NULL CHECK (length(key_pattern) <= 1024),             -- kind = 'pattern'
  repo_class     text NULL CHECK (repo_class IN ('local', 'remote', 'virtual', 'any')),
  format         text NULL,                                                 -- ':<format>' filter
  PRIMARY KEY (policy_id, ordinal),
  CHECK ((kind = 'key')     = (repository_id IS NOT NULL)),
  CHECK ((kind = 'pattern') = (key_pattern IS NOT NULL)),
  CHECK ((kind = 'class')   = (repo_class IS NOT NULL))
);

CREATE TABLE authz_policy_pattern (
  policy_id  uuid NOT NULL REFERENCES authz_policy (id) ON DELETE CASCADE,
  dimension  text NOT NULL CHECK (dimension IN ('package', 'path', 'build')),
  mode       text NOT NULL CHECK (mode IN ('include', 'exclude')),
  pattern    text NOT NULL CHECK (length(pattern) BETWEEN 1 AND 1024),
  PRIMARY KEY (policy_id, dimension, mode, pattern)
);

CREATE TABLE authz_policy_entry (
  policy_id     uuid NOT NULL REFERENCES authz_policy (id) ON DELETE CASCADE,
  principal_id  uuid NOT NULL REFERENCES principal (id) ON DELETE CASCADE,   -- anonymous included
  actions       text[] NOT NULL CHECK (cardinality(actions) > 0
                  AND actions <@ ARRAY['read','annotate','deploy','overwrite','delete','manage']),
  PRIMARY KEY (policy_id, principal_id)
);
```

Configuration:

```yaml
authz:
  cache:
    poll_interval: 5s   # default (proposed)
    max_staleness: 30s  # default (proposed)
```

Acceptance criteria (two replicas behind one Service):

- A policy is deleted through replica A. A request through replica B that depended on it is
  refused within 5 seconds, and within 1 second while notifications flow.
- Replica B's listening connection is ended with `pg_terminate_backend` on the PostgreSQL side. A
  policy change through A is still in force on B within 5 seconds.
- PostgreSQL is unreachable from replica B for 40 seconds. B answers 503 to package requests after
  30 seconds and reports not ready.

### R-AUTHZ-029 · Audit of authorization decisions
Priority: **MVP** · Covers: trove-only

Every authorization decision can be audited (INV-9). Storage, retention and export of audit
events belong to [operations-and-observability.md](operations-and-observability.md) (INV-16).
This requirement says which events authorization emits and what they contain.

| Event | When | Delivery |
|---|---|---|
| `authz.decision`, allowed write | every allowed `deploy`, `overwrite`, `delete`, `annotate` and `manage`, and every allowed populate | in the transaction of the change; if the audit row cannot be written, the change fails |
| `authz.decision`, refused write or management operation | every such refusal | asynchronous, buffered |
| `authz.decision`, read | refused reads with `authz.audit.reads` `denied` or `all`; allowed reads with `all` | asynchronous, buffered |
| `authz.policy.created`, `.updated`, `.deleted` | policy changes, with before and after | in the transaction of the change |
| `authz.role.bound`, `.unbound` | role bindings | in the transaction of the change |
| `authz.ownership.claimed`, `.added`, `.removed`, `.released` | ownership changes, CI claims included | in the transaction of the change |
| `authz.project.*` | project changes (P1) | in the transaction of the change |
| `authz.settings.changed` | changes to authorization settings stored in PostgreSQL | in the transaction of the change |

Fields of `authz.decision`:

| Field | Content |
|---|---|
| `id`, `time`, `replica`, `requestId` | Identity of the event and the request |
| `actor` | Principal reference and display name; for CI tokens also the mappings and the project identity |
| `credential` | Kind (`session`, `pat`, `sat`, `cit`, `none`) and token ID; never a token value |
| `ci` | For CI tokens, the issuance ID of R-CI-020, which links to `project_id`, `pipeline_id`, `job_id`, `ref` and `ref_protected` |
| `actions`, `repository`, `class`, `format`, `path`, `package` | The coordinate |
| `outcome`, `reason`, `status` | `allow` or `deny`, the reason code below, the HTTP status sent |
| `sources` | What allowed it: policy IDs and names, `ci-inline` with ordinal, `role:<name>`, `ownership` |
| `sourceIp`, `userAgent` | As seen after trusted-proxy processing ([architecture.md](architecture.md)) |

| Reason code | Meaning |
|---|---|
| `allow.grant` | A grant allowed it |
| `allow.role` | `content:bypass` or a capability allowed it |
| `deny.unauthenticated` | No credential, or anonymous access off |
| `deny.no-grant` | No grant for the action |
| `deny.scope` | A grant exists, but the token scope or the CI caps exclude it |
| `deny.triggering-user` | P1: the triggering user lacks the action (R-CI-024) |
| `deny.virtual-gate` | No `read` on the virtual repository |
| `deny.anonymous-partial-virtual` | Anonymous caller; a member is not readable anonymously |
| `deny.populate` | May read the remote repository but not populate it |
| `deny.ownership` | Action allowed, ownership refused |
| `deny.capability` | Management operation without the required capability |
| `deny.stale` | Grant index stale |

- Repeated anonymous 401 answers are aggregated so that scanners cannot flood the audit store: per
  source IP at most one event per second, plus `trove_authz_decisions_total`.
- Every event is also written to the structured log stream (INV-16).
- The CI page's events `ci.package_ownership.claimed` and `.conflict` (R-CI-020) describe the same
  facts from the CI side and carry the same request ID, so the two can be joined.

Configuration:

```yaml
authz:
  audit:
    reads: denied   # default (proposed); none | denied | all
```

Acceptance criteria:

- Trace row 2 of R-AUTHZ-015 produces one `authz.decision` with `outcome: allow` and `sources`
  naming `team-web-publish`; row 3 produces one with `outcome: deny`, `reason: deny.no-grant`,
  `status: 403`.
- With `reads: all`, `npm install` of one package produces events for the document and the tarball;
  with the default it produces none.
- When the audit table rejects inserts (in a test, by revoking `INSERT` from the application role),
  `npm publish` fails and no version is stored.

## Worked examples

### Team: read everything, write the own scope

Repositories: `npm-internal` (local, npm, `packageOwnership: enforce`), `npmjs` (remote of the
public npm registry, `cacheFillAction: read`), `npm-all` (virtual over both, deploy target
`npm-internal`). IdP groups `developers` and `team-web` are synchronized to trove groups of the
same names.

```json
[
  {
    "name": "npm-consumers",
    "description": "Every developer reads all npm repositories",
    "resources": { "repositories": ["@any-virtual:npm", "@any-local:npm", "@any-remote:npm"] },
    "entries": [{ "principal": "group:developers", "actions": ["read"] }]
  },
  {
    "name": "team-web-publish",
    "description": "Team web publishes, tags and deprecates its own scope",
    "resources": {
      "repositories": ["npm-internal"],
      "packages": { "include": ["@web/*"] }
    },
    "entries": [
      { "principal": "group:team-web", "actions": ["annotate", "deploy", "read"] },
      { "principal": "user:lead-web", "actions": ["deploy", "manage", "read"] }
    ]
  }
]
```

Developer setup (token with the default scope, from the client setup instructions):

```sh
npm config set registry https://trove.example.com/api/npm/npm-all/
npm config set //trove.example.com/api/npm/npm-all/:_authToken "$TROVE_TOKEN"
```

Outcome: every developer installs everything through `npm-all`. Members of `team-web` publish
`@web/*`; the first publish makes `group:team-web` the owner, so any team member may publish the
next version. Nobody else publishes under `@web/`. `lead-web` can additionally repair ownership,
for example after an import, and pre-assign names (R-AUTHZ-019). The trace of R-AUTHZ-015 walks
through the individual decisions.

### GitLab CI: read all internal npm packages, publish only project-derived names

Goal: every pipeline of GitLab group `acme` may install any internal npm package. Jobs on
protected refs may also publish names derived from their own project: `@<top-level group>/<project
name>` and `@<top-level group>/<project name>-*`.

Shared read access is a policy that names the mappings as principals:

```json
{
  "name": "ci-read-internal",
  "description": "CI pipelines read all npm repositories",
  "resources": { "repositories": ["@any-virtual:npm", "@any-local:npm", "@any-remote:npm"] },
  "entries": [
    { "principal": "ci:gitlab-internal/acme-publish", "actions": ["read"] },
    { "principal": "ci:gitlab-internal/acme-read", "actions": ["read"] }
  ]
}
```

The mappings use the condition, grant and template syntax of
[gitlab-ci-oidc.md](gitlab-ci-oidc.md) (R-CI-010, R-CI-013, R-CI-014) on a self-managed issuer
`gitlab-internal`. Path claims are used here for readability; pinning by group ID is limited by
GitLab's hierarchy (H-27 of that page). Because the only write comes from an inline grant with
`ownership: bind_project`, the unpinned `acme-publish` passes the `unpinned_write` rule (R-CI-010)
and is saved with the warning `unpinned_condition`; H-30 of that page records what path-only read
mappings still allow.

```json
[
  {
    "name": "acme-publish",
    "priority": 100,
    "match": { "all": [
      { "claim": "project_path", "glob": "acme/**" },
      { "claim": "ref_protected", "equals": true }
    ]},
    "grants": {
      "policies": ["ci-read-internal"],
      "inline": [
        { "repositories": ["npm-internal"],
          "packages": { "include": [
            "@{{ project_path | segment 0 | slug }}/{{ project_path | segment -1 | slug }}",
            "@{{ project_path | segment 0 | slug }}/{{ project_path | segment -1 | slug }}-*"
          ]},
          "actions": ["read", "deploy"],
          "ownership": "bind_project" }
      ]
    }
  },
  {
    "name": "acme-read",
    "priority": 900,
    "match": { "claim": "project_path", "glob": "acme/**" },
    "grants": { "policies": ["ci-read-internal"] }
  }
]
```

Rendered for three jobs:

| `project_path` | `project_id` | `ref_protected` | Mapping | Publishable names | Owner on first publish |
|---|---|---|---|---|---|
| `acme/web/frontend` | 4711 | `true` | `acme-publish` | `@acme/frontend`, `@acme/frontend-*` | `ci:gitlab-internal:project:4711` |
| `acme/mobile/frontend` | 4712 | `true` | `acme-publish` | `@acme/frontend`, `@acme/frontend-*` | `ci:gitlab-internal:project:4712` |
| `acme/web/frontend` | 4711 | `false` (merge request branch) | `acme-read` | none | none |

The derivation collides for 4711 and 4712, and package ownership resolves it: whichever project
publishes `@acme/frontend` first owns it, and the other gets 403 `package_owned_by_other_owner`
until an owner adds it as a co-owner (R-AUTHZ-019). If project 4711 is renamed and a new project
takes over its old path, the new project renders the same names but has a different `project_id`,
so it cannot publish versions of packages 4711 owns.

Job (the hidden job `.trove` that exchanges the ID token is the one in the examples of
[gitlab-ci-oidc.md](gitlab-ci-oidc.md)):

```yaml
publish-npm:
  extends: .trove
  image: node:24-bookworm                                # example
  rules:
    - if: $CI_COMMIT_REF_PROTECTED == "true"
  script:
    - |
      cat > .npmrc <<'EOF'
      registry=https://trove.example.com/api/npm/npm-all/
      //trove.example.com/api/npm/npm-all/:_authToken=${TROVE_TOKEN}
      EOF
    - npm ci
    - npm publish --registry https://trove.example.com/api/npm/npm-all/
```

Expected results:

- Project 4711 on `main` (protected): `npm ci` succeeds and `npm publish` of `@acme/frontend`
  succeeds; `npm publish` of `@acme/payments` gets `E403`.
- Project 4711 on an unprotected branch: `npm ci` succeeds; `npm publish` gets `E403`
  (`acme-read` grants only `read`).
- Project 4712 on `main` after 4711 has published: `npm publish` of `@acme/frontend` gets `E403`
  `package_owned_by_other_owner`; `@acme/frontend-cli`, still unclaimed, succeeds and is owned by
  4712.

## Deferred

<details>
<summary>Deferred catalog entries (25)</summary>

| ID | Priority | Reason |
|---|---|---|
| IAM-008 | Later | Scan-related roles; no scanning engine is planned. |
| IAM-026 | Later | Release-set resources; release sets are not planned soon. |
| IAM-027 | Later | Distribution-target resources; distribution to edge instances is not planned soon. |
| IAM-028 | Out of scope | CI pipeline-source resources; trove bundles no CI engine. |
| IAM-033 | P2 | Older policy interfaces with whole-policy replacement; the single policy API of R-AUTHZ-021 replaces them. |
| IAM-034 | P2 | Fine control over anonymous grants by non-administrators; until then, delegated managers and project administrators can never grant to `anonymous`. |
| IAM-053 | P2 | Project members through role assignment; P1 projects know only project administrators and project policies. |
| IAM-054 | P2 | Global roles for projects. |
| IAM-055 | P2 | Project roles. |
| IAM-056 | P2 | Role action catalog. |
| IAM-058 | P2 | Sharing repositories across projects. |
| IAM-059 | Later | Project-aware sharing through internal remote repositories. |
| IAM-060 | P2 | Per-project build repositories. |
| IAM-061 | P2 | Project storage quotas. |
| IAM-062 | P2 | Anonymous access inside projects; R-AUTHZ-026 refuses `anonymous` in project policies meanwhile. |
| IAM-063 | P2 | Environments. |
| IAM-064 | P2 | Environments on roles and repositories. |
| IAM-065 | Later | Project-scoped scan policies. |
| IAM-066 | Later | Migration from permission policies to projects. |
| IAM-068 | P2 | Project role and environment operations; the P1 subset (projects, administrators, repository assignment) is in R-AUTHZ-026. |
| ADMAPI-019 | P1 in the catalog, P2 here | Custom global role operations belong to the project-role model (IAM-054), which stays P2; the fixed system roles of R-AUTHZ-010 cover P1 needs. |
| ADMAPI-025 | P2 | Older policy interface with resource sections (same reason as IAM-033). |
| ADMAPI-026 | P2 | Oldest repository-only policy interface (same reason as IAM-033). |
| TOK-011 | P2 | Project-role token scopes; they follow project roles. |
| TOK-013 | P2 | Project membership read scopes; they follow project roles. |

</details>

## Security notes

- **Canonical coordinates.** Authorization is only as good as the coordinate it gets. Paths and
  names are decoded once, normalized, and rejected when they contain traversal or ambiguous segments
  before evaluation (R-AUTHZ-005). A parser that decoded twice, or let `@acme%2f..` through, would
  turn name grants into path traversal.
- **Declared name equals authorized name.** An upload is authorized against the name its content
  declares. Otherwise a principal limited to `@acme/*` could upload, under an `@acme/` path, a
  tarball that the indexer records as `@other/evil`, and so bypass name grants and ownership
  through the raw file endpoint (R-AUTHZ-005, R-AUTHZ-007).
- **Claim values cannot widen grants.** The escaping of R-CI-014 and the matcher's rule that a
  backslash makes the next character literal (R-AUTHZ-005) must agree, and the unit tests of both
  pages share the `a\*b` case.
- **Principals by ID.** Grants, role bindings and owners follow the principal's immutable ID, so a
  deleted and recreated user, group or mapping of the same name inherits nothing (R-AUTHZ-002).
- **Path-derived names and renamed projects.** INV-10's example derives names from `project_path`,
  which changes on rename or transfer. GitLab refuses ID tokens whose `sub` contains a reused
  project path, but other path claims carry no such guard (CIOIDC-034). Owners keyed by
  `project_id` keep a project that inherits an old path from publishing the previous project's
  packages.
- **Merged metadata isolation.** A merged npm document or NuGet registration cached for one subject
  can contain versions from members another subject cannot read; R-AUTHZ-009 rule 4 forbids serving
  it across differing readable-member sets. Dependency confusion through partially readable
  virtual repositories is closed by pin rules and namespace claims, which ignore the caller's
  permissions on purpose (R-REPO-023).
- **Session cookies do not populate.** The cookie rides along on cross-site top-level navigations
  and same-origin image loads, so cookie-authenticated requests never pass the populate check
  (R-AUTHZ-008).
- **CI tokens cannot transfer ownership.** Only people and service accounts add, remove or release
  owners (R-AUTHZ-019).
- **No existence oracle.** Refused reads get 404 that is byte-identical to "missing", authorization
  runs before the existence lookup, refused writes get 403 whatever exists, and search counts are
  filtered (R-AUTHZ-016).
- **Security administrators reach content.** `security-admin` holds no content rights through its
  role, but it writes policies and can therefore grant itself any content right. Every such change
  is audited in the transaction that makes it (R-AUTHZ-029); H-22 records the missing two-person
  rule.
- **Anonymous populating.** With anonymous access on, an anonymous `read` grant on a remote
  repository with `cacheFillAction: read` lets anyone make trove download arbitrary upstream
  packages: storage growth, upstream rate limits, possibly upstream cost. Where remote repositories
  are exposed anonymously, set `cacheFillAction: deploy` and grant `deploy` to authenticated groups
  only (R-AUTHZ-008).
- **Administrator power stays out of everyday tokens.** Tokens exercise roles only through
  `admin: true` or `system` items (R-AUTHZ-013). A leaked personal access token of an
  administrator therefore exposes that person's content rights, not the instance.

## Open questions

### Q-AUTHZ-1 · Endpoint gate on virtual repositories

Should using a virtual repository require `read` on it (R-AUTHZ-009)? The resolution algorithm of
[repositories-and-proxy.md](repositories-and-proxy.md) (R-REPO-019 step 1) applies the gate as
proposed here.

- (a) Explicit gate: `read` on the virtual repository plus member rights. One extra selector
  (`@any-virtual:npm`); administrators decide deliberately which aggregated endpoints a group uses.
- (b) Implied: anyone who can read at least one member can use the virtual repository. No extra
  grant, matching the catalog (IAM-024); the `@any-virtual` selector then serves only `manage`.
- (c) Sufficient: `read` on the virtual repository lets the subject read all members through it.
  Simplest to administer, but adding a private repository to a virtual repository silently exposes
  it.

Recommendation: (a). It keeps the property that matters (content is decided on the member, so the
silent exposure of (c) cannot happen) and makes the endpoint visible in policies, at the cost of
one selector.

### Q-AUTHZ-2 · Ownership enforcement default for new npm and NuGet local repositories

- (a) `enforce` by default.
- (b) `off` by default, opt-in per repository; only CI grants with `bind_project` check ownership.

Recommendation: (a). With (b), a human or service account holding `deploy` on an overlapping
pattern can still publish versions of a CI project's package, as long as that package was first
published by a grant without `bind_project`. And switching enforcement on later leaves every
existing name unclaimed (needs `manage`, R-AUTHZ-018 rule 3). The cost of (a) is that a user-level
grant makes that user the personal owner. Documentation therefore steers `deploy` grants to groups
and CI mappings.

### Q-AUTHZ-3 · Built-in principal for all authenticated users

- (a) No built-in principal; administrators use an IdP group that contains everyone.
- (b) A built-in `authenticated` principal covering users and service accounts, not CI tokens.
- (c) A built-in `authenticated` principal covering every authenticated subject, CI tokens included.

Recommendation: (b), as P1. Many installations want "every employee may read `npm-all`" without
maintaining an IdP group. Excluding CI tokens keeps their rights fully described by their mappings,
the same property R-AUTHZ-002 protects by refusing auto-join groups for CI.

### Q-AUTHZ-4 · Default populate action of remote repositories

- (a) `read` (proposed in R-AUTHZ-008).
- (b) `deploy`, as in the catalog (IAM-023).

Recommendation: (a). It is what users expect from a pull-through proxy and avoids the most common
inconsistent-404 support case. (b) stays available per repository as frozen-mirror mode.
[repositories-and-proxy.md](repositories-and-proxy.md) already follows (a): its settings table
(R-REPO-024) defaults `cacheFillAction` to `read`, and R-REPO-025 step 9 counts refreshing expired
metadata as populating, serving a caller who fails the check the stale entry instead, because a
refreshed document advertises versions the caller cannot fetch.

### Q-AUTHZ-5 · Default audit level for reads

- (a) `none`: no read events; refused reads appear only in `trove_authz_decisions_total`.
- (b) `denied` (proposed): refused reads are audited, allowed reads are not.
- (c) `all`: every read is audited, so every download is attributable.

Recommendation: (b) as the default. Refused reads are rare and signal probing. (c) multiplies audit
volume by the download rate, often by orders of magnitude, and should be a deliberate choice per
installation. The remaining gap is H-23.

### Q-AUTHZ-6 · Default `ownership` of CI inline grants that write

[gitlab-ci-oidc.md](gitlab-ci-oidc.md) (R-CI-013) defaults `ownership` to `none`.

- (a) Keep `none` as default; in a repository with `packageOwnership: enforce`, such a grant claims
  names for the mapping (R-AUTHZ-018 rule 2), so every project the mapping matches co-owns them.
- (b) Default to `bind_project` for inline grants with `deploy`, `overwrite` or `delete`, and
  require an explicit `none` to share ownership across projects.

Recommendation: (b). With (a), a group-wide mapping makes all its projects co-owners of everything
any of them publishes, which defeats per-project ownership without anyone noticing. Sharing on
purpose stays possible with an explicit `none`.

### Q-AUTHZ-7 · CI delegations and P1 projects

R-CI-023 delegates CI mapping management per CI issuer, because it assumed no project entity
before P2; this page introduces projects in P1.

- (a) Keep the two independent; an administrator may name a project's administrator group as the
  delegate of a CI delegation.
- (b) Let a CI delegation reference a project, so that its allowed repositories follow the
  project's repositories automatically.

Recommendation: (a) for P1, (b) when projects gain roles in P2. (a) needs no change to either page
and covers the onboarding case; (b) removes the duplicate repository list once projects are richer.

### Q-AUTHZ-8 · Spec page for build metadata

R-AUTHZ-027 defines build permissions, but no page in the spec page table owns build records (BLD,
P1).

- (a) Add a page `builds.md` (`R-BLD`) before P1 work starts.
- (b) Fold build records into [repositories-and-proxy.md](repositories-and-proxy.md).

Recommendation: (a). Build records have their own API, retention and promotion semantics. Folding
them into the repository page would make that page the owner of two unrelated models.

### Q-AUTHZ-9 · Shape of the repository part of a token scope

R-AUTH-024 shows a scope grant as `{"repositories": [...], "include": [...], "exclude": [...],
"actions": [...]}`, with a single pattern dimension. Policies (R-AUTHZ-021) and CI inline grants
(R-CI-013) carry `packages` and `paths` separately.

- (a) Scope grants use the resource set of this page: `repositories`, `packages`, `paths`,
  `actions`.
- (b) Keep the single `include`/`exclude` pair and interpret it as a name pattern for formats with
  package names and as a path pattern otherwise.

Recommendation: (a). One matcher and one validator then serve policies, scopes and CI grants. (b)
makes the meaning of a pattern depend on the repository's format, which a scope that spans several
formats cannot express.

## What this does not cover

### H-21 · No reservation of name prefixes

Ownership starts with the first publish or with an explicit pre-assignment of one exact name
(R-AUTHZ-019). Within its grants, any principal holding `deploy` on a pattern can still claim a name
that is not pre-assigned, including a name another team was about to use under the same prefix.
Narrow name patterns per team and per CI project limit this, and `manage` holders can reassign
owners afterwards. Reserving a whole prefix for an owner, comparable to NuGet's public ID prefix
reservation, is not specified.

### H-22 · No two-person rule for permission changes

A single `admin` or `security-admin` can grant any content right, including to itself, in one step.
Changes are audited in the same transaction, but nothing requires a second person to approve them
before they take effect.

### H-23 · Allowed downloads are not attributable by default

With the default `authz.audit.reads: denied`, a principal that reads within its rights leaves only
metrics and possibly log lines, no audit event. Exfiltration by an authorized reader cannot be
reconstructed from the audit store unless `reads: all` was set beforehand.
