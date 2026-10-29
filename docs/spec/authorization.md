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
| Principal | Something a grant can name: a user, a group, a service account, a CI identity mapping, or one of the built-in principals `anonymous` (R-AUTHZ-003) and `authenticated` (R-AUTHZ-030) (R-AUTHZ-002). Principals live in the `principal` table of [identity-and-authentication.md](identity-and-authentication.md) (R-AUTH-016). |
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
| What is checked on a virtual repository? | `read` on the virtual repository as an endpoint gate; content actions on the member that holds or receives the content (Q-AUTHZ-1). | R-AUTHZ-009 |
| Does fetching an uncached item through a remote repository need a write-type action? | Configurable per remote repository (`cacheFillAction`); default `read`, `deploy` for frozen mirrors (Q-AUTHZ-4). | R-AUTHZ-008 |
| 401, 403 or 404? | 401 for callers without a credential; 404 for reads the caller may not make; 403 for refused writes and management operations. | R-AUTHZ-016 |
| Does the endpoint a request arrives on change its rights? | Only downwards: an endpoint with exposure rules is a ceiling for every subject, administrators included, and answers hidden reads with 404 ([endpoints.md](endpoints.md) Q-EP-4, Q-EP-12). | R-AUTHZ-015, R-AUTHZ-016 |
| Can a token scope grant rights? | Never; it only narrows. Its repository part uses the policy resource set; its resource types are repository content, system capabilities and, from P1, build records (Q-AUTHZ-9, Q-AUTHZ-10). | R-AUTHZ-013 |
| How do "all signed-in people" get read access? | Through the built-in principal `authenticated`: every user, never service accounts or CI tokens, `read` only (Q-AUTHZ-3). | R-AUTHZ-030 |
| Who may publish the next version of a package? | Its owners, or a holder of `manage` on it. The grants that allowed the first publish decide the owners. Ownership is enforced by default in new npm and NuGet local repositories (Q-AUTHZ-2). | R-AUTHZ-018 |
| Whom does a writing CI inline grant make the owner? | The job's GitLab project (`bind_project`), unless the grant says `"ownership": "none"` (Q-AUTHZ-6). | R-AUTHZ-014 |
| Which reads are audited by default? | Refused reads only; allowed reads with `authz.audit.reads: all` (Q-AUTHZ-5). | R-AUTHZ-029 |
| Projects for delegated administration | P1, in a reduced form: repositories, project administrators, project-owned policies. Roles, environments and sharing stay P2. CI delegations stay independent of projects in P1 (Q-AUTHZ-7). | R-AUTHZ-026 |
| How do n replicas learn of a permission change? | Through the `authz` change area of [architecture.md](architecture.md) R-ARCH-018: the change log in PostgreSQL carries correctness, a Valkey message only starts the rebuild sooner. Valkey never holds a grant or a decision (Q-AUTHZ-11). | R-AUTHZ-028 |
| How long can a removed grant still work? | Typically 100 ms plus one index build; at most `cluster.epoch_poll_interval` plus 1 s on a replica whose change-log poll works, with or without Valkey; never longer than `authz.cache.max_staleness` (10 s) plus one build on any replica. A replica that cannot confirm its index waits like a request waits for the database, then answers 503 (Q-AUTHZ-13). | R-AUTHZ-028 |
| Can a client see its own permission change on whichever replica answers next? | Yes, by sending `Trove-Authz-Min-Epoch` with the epoch its write returned (Q-AUTHZ-12). | R-AUTHZ-032 |

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
3. the exposure of the endpoint the request arrived on ([endpoints.md](endpoints.md) R-EP-013): a
   ceiling of the same kind, which binds every subject, `content:bypass` included, and never grants;
4. three checks that are not permissions and never grant anything: the endpoint gate of virtual
   repositories (R-AUTHZ-009), package ownership (R-AUTHZ-018), and the shadowing check on the
   first publish of a name that a remote member already serves, which a `deploy` grant alone does
   not pass ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-048).

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
| Authenticated | `authenticated` | R-AUTHZ-030; a built-in row of the `principal` table like `anonymous` |

- Policies, role bindings and owner records MUST reference principals by their immutable ID in
  the `principal` table, never by name. The API accepts and returns references by name and
  translates them. Renaming keeps the grants. Deleting a principal deletes its policy entries,
  role bindings and owner entries through the foreign keys (audited, R-AUTHZ-029). A principal
  created later under a reused name inherits nothing.
- For each request, the subject's principal set is computed from the credential:

| Credential | Principal set |
|---|---|
| UI session of user `u` | `user:u` plus every group of `u`, plus `authenticated` |
| Personal access token (`trv_pat_`) of `u` | the same, resolved when the token is used |
| Service-account token (`trv_sat_`) of `s` | `sa:s` plus every group of `s`. Never `authenticated` (R-AUTHZ-030). |
| CI token (`trv_cit_`) | `ci:<issuer>/<m>` for every mapping `m` recorded as the token's mapping at exchange, plus the groups those mappings list (R-CI-013); the token additionally carries its frozen inline grants and, from P1, the service accounts of R-CI-024. For a cross-project subject token, a matched mapping without the `read_only` opt-in contributes nothing and is not recorded ([gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-012), so policies naming it never reach that token. Never `authenticated`. |
| No credential | `anonymous`, only while anonymous access is enabled (R-AUTHZ-003) |

- "Every group of `u`" means the local groups `u` belongs to plus the groups synchronized from
  the identity provider's group claim (R-AUTH-008). Membership MUST be resolved when the request is
  evaluated, not when a token was issued; tokens carry no group lists. The membership cache and
  its invalidation belong to [identity-and-authentication.md](identity-and-authentication.md)
  (R-AUTH-023).
- There are no transient users. A CI token never stands for a user account, never picks up
  auto-join groups (R-AUTH-019, R-CI-013) and never counts as `authenticated` (R-AUTHZ-030), which
  removes the catalog's widening of CI tokens through default groups (TOK-015, CIOIDC-014).
- A disabled or deleted principal's credentials are refused by authentication
  ([identity-and-authentication.md](identity-and-authentication.md)); authorization never sees them.

Acceptance criteria:

- Alice is in IdP group `team-web`, synchronized to trove group `team-web`, which holds `read`
  on `npm-internal`. After the IdP removes her from `team-web` and she logs in again, her existing
  personal access token no longer reads `npm-internal`, within the latency bound of R-AUTH-023.
- Group `everyone` is auto-join and holds `read` on `npm-all`, and `authenticated` holds `read` on
  `npm-all` as well. A CI token whose mappings list no groups and name no policy covering
  `npm-all` gets 404 on `npm-all`.
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
- A repository with `requireAuthentication: true`
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

### R-AUTHZ-030 · The `authenticated` principal
Priority: **MVP** · Covers: trove-only

The first policy of most installations is "every signed-in person may read the npm and NuGet
endpoints". trove offers a built-in principal for it. An installation does not need an
identity-provider group that contains everyone, nor auto-join groups (P1, R-AUTH-019), before the
first developer can install a package (Q-AUTHZ-3).

- `authenticated` is a built-in principal (reference `authenticated`), stored in the `principal`
  table like `anonymous` ([identity-and-authentication.md](identity-and-authentication.md),
  R-AUTH-016, R-AUTH-037). It always exists. It cannot be deleted or renamed, holds no tokens or
  sessions and cannot be a group member.
- It is in the principal set of every request that authenticates as a user (UI session, personal
  access token), as listed in R-AUTHZ-002. It is never in the principal set of a service-account
  token, of a CI token (R-AUTHZ-014) or of an anonymous request. Service accounts and CI mappings
  receive rights only from grants that name them or their groups (for CI tokens also their inline
  grants), the same rule that keeps them out of auto-join groups (R-AUTH-019).
- Policy entries for `authenticated` MUST contain only `read`. Any other action is rejected with
  422 `authenticated_write_action`. Pattern and class selectors are allowed, unlike for
  `anonymous`: covering repositories created later is what this principal is for, and every caller
  it admits is known to trove.
- `authenticated` never holds a system role (422 `role_not_allowed`, R-AUTHZ-010), is never a
  project administrator, and never becomes a package owner. Because it holds only `read`, rule 2
  of R-AUTHZ-018 never names it as the owner of a claim.
- Delegated managers (R-AUTHZ-025) and project administrators (R-AUTHZ-026) MUST NOT add or change
  an entry for `authenticated` (403). Opening content to the whole organization stays a decision
  of a holder of `policies:write`.
- Token scopes apply as usual (R-AUTHZ-013). A token whose scope does not admit a repository does
  not read it through `authenticated` either.

Acceptance criteria:

- Policy `everyone-reads-npm` gives `authenticated` `read` on `["@any-virtual:npm",
  "@any-remote:npm"]`. A user who has just logged in through OIDC and belongs to no group runs
  `npm view left-pad --registry https://trove.example.com/api/npm/npm-all/` and succeeds. The same
  command with a service-account token of `sa:ci-bot`, which has no grant of its own and no group
  that holds one, gets `E404`.
- With the same policy, a CI token whose mappings grant nothing on `npm-all` gets `E404` there.
- `POST /api/v1/permission-policies` giving `authenticated` `["read", "deploy"]` answers 422
  `authenticated_write_action`. `PUT /api/v1/role-bindings/auditor/authenticated` answers 422
  `role_not_allowed`.

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
  repository `npm-team-x`; within `cluster.epoch_poll_interval` plus 1 s, also with Valkey
  stopped, a member of `developers` can `npm view` a package published there through any replica,
  without any change to the policy.
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
| Upload to an existing path, different content | `deploy` and `overwrite`; formats with immutable versions refuse regardless of `overwrite` ([formats/npm.md](formats/npm.md), [formats/nuget.md](formats/nuget.md)) |
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
also pass the ownership check of R-AUTHZ-018; "shadowing check" marks operations that also pass the
check of [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-048 when they give a local
repository the first version of a name (see "First version of a name" below).

npm:

| Client operation | Coordinate | Actions | Ownership |
|---|---|---|---|
| `npm view`, `npm install`: package document | name | `read` | — |
| Tarball download | name, path | `read` | — |
| `npm publish` of a new version (with its dist-tag) | name | `deploy`; for the first version of the name in the repository also the shadowing check | yes |
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
| `dotnet nuget push` of a new version | ID | `deploy`; for the first version of the ID in the repository also the shadowing check | yes |
| Push of an existing, deleted or moved-away version | ID | refused by the format (immutable versions with tombstones, [formats/nuget.md](formats/nuget.md) R-NUGET-006) | — |
| `dotnet nuget delete` | ID | `delete`, whether the repository deletes or unlists | yes |
| Relist | ID | `deploy` | yes |
| Symbol package push | ID | `deploy` | yes |

The NuGet API key header carries a credential (INV-8) and plays no further part in authorization.

**First version of a name.** A write that gives a local repository the first version of a name
passes the shadowing check of [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-048 in
addition to its actions and to ownership, whatever the repository's `packageOwnership`. When a
remote member of a virtual repository that contains the local repository serves the name, the
write also needs `manage` on the coordinate, `content:bypass`, or a manual namespace claim of the
local repository that covers the name (R-REPO-023); otherwise it gets 403 `shadows_remote_package`
(audit reason `deny.shadowing`, R-AUTHZ-029), and nothing is stored. While a remote member cannot
be asked, a caller without `manage` or `content:bypass` gets 503 `shadow_check_unavailable`
instead. Later versions of a name the repository already holds, and names any namespace claim of
the repository covers, are never checked. A namespace claim is not package ownership:
pre-assigning owners (R-AUTHZ-019) does not satisfy the check. From P1 the same applies to copy,
move and trash-can restore targets ([storage.md](storage.md) R-STOR-019, R-STOR-023); those
operations report a refusal per item. A symbol package push never runs the check, because its
version must already exist ([formats/nuget.md](formats/nuget.md) R-NUGET-020).

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
- **Anonymous populating is flagged.** When a policy gives `anonymous` `read` on a remote
  repository whose `cacheFillAction` is `read`, the policy API saves it and returns the warning
  `anonymous_populates_remote` (R-AUTHZ-021). Once anonymous access is on, every caller on the
  network can make trove download arbitrary upstream packages, and the warning makes that visible
  when the grant is made.

Reasoning (decided in Q-AUTHZ-4): the default is `read`. Requiring a write-type action for every
fetch is the most frequent reason why one developer gets a package and another gets 404 from the
same proxy, and it forces administrators to hand out `deploy` on repositories nobody can deploy to.
`deploy` remains available for frozen mirrors: only principals given `deploy` (for example release
CI jobs) bring new third-party packages into the organization, and everyone else reads what is
already cached.

Configuration (per remote repository, through the repository API):

```json
{ "cacheFillAction": "read" }
```

`"read"` is the default for every new remote repository; `"deploy"` is the frozen-mirror mode and is
chosen per repository.

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
- `POST /api/v1/permission-policies` giving `anonymous` `read` on `npmjs` (`cacheFillAction:
  read`) answers 201 with `"warnings": ["anonymous_populates_remote"]`, whether anonymous access is
  currently on or off; the same policy on a remote repository with `cacheFillAction: deploy`
  answers 201 without that warning.

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
8. **Unreadable members stay invisible.** Content that exists only in members the subject cannot
   read is answered through the virtual repository exactly like missing content (404), whatever
   `hideExistence` says for the virtual repository or the member (R-AUTHZ-017). The subject was
   never entitled to consult those members (rule 2), so their content does not exist for it.

This deliberately differs from the catalog, where a virtual repository has no permissions of its
own (IAM-024). Policies MAY name virtual repositories by key, by pattern and by class
(R-AUTHZ-004), restricted to `read` (the gate) and `manage` (metadata cache invalidation); the
catalog leaves open whether a policy may hold a virtual repository key, and trove allows it. The
gate lets administrators decide which aggregated endpoints a group uses, at the cost of one extra
selector (`@any-virtual:npm` covers all npm virtual repositories). Because content is always
decided on the member, rearranging the members of a virtual repository never exposes content to
someone who could not read it directly. Q-AUTHZ-1 records the decision;
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
| `endpoints:read`, `endpoints:write`: endpoints and their exposure rules ([endpoints.md](endpoints.md) R-EP-016) | yes, yes | yes, yes | yes, — | yes, — | — |

- Roles are bound to users, groups and service accounts (R-AUTHZ-022). A role bound to a group
  applies to every member, including members synchronized from the identity provider. This is how
  an IdP group carries administrator rights; there is no administrator flag on users or groups
  (IAM-016, R-AUTH-008).
- Roles are never bound to `anonymous`, to `authenticated` (R-AUTHZ-030) or to CI identity
  mappings (R-CI-013: CI tokens never carry system administration and cannot call management
  endpoints). Such bindings are rejected with 422 `role_not_allowed`.
- The break-glass administrator (R-AUTH-014) always holds `admin` without a role binding, so
  recovery never depends on the state it recovers.
- A UI session exercises every role of its user. A token exercises roles only as its scope allows
  (R-AUTHZ-013).
- `content:bypass` makes the subject pass every repository check and the ownership check. It does
  not bypass the canonicalization rules of R-AUTHZ-005.
- Read-only system status needs `settings:read`. That covers the replica registry, system
  information, scheduled tasks and jobs, lock diagnostics, the redacted effective configuration and
  the log levels ([operations-and-observability.md](operations-and-observability.md)), the
  encryption status and the backup runs of [backup-and-restore.md](backup-and-restore.md)
  R-BAK-014. Pages that call this permission
  `system.read` mean this capability. Taking and restoring backups is not an API operation: the
  `backup` role runs them as Kubernetes Jobs with credentials of its own (INV-19), so no capability
  of this table starts one.

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
  `hideExistence`, `requireAuthentication`) and writes upstream credentials (write-only). It
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

The scope structure (`inherit`, `admin`, `repositories`, `system` and, from P1, `builds`), its
limits, its defaults and who may request what are specified in
[identity-and-authentication.md](identity-and-authentication.md) (R-AUTH-024, R-AUTH-025). This
requirement fixes how the evaluator applies a scope:

- **Ceiling, never grant.** An action on a coordinate is allowed for a token only if the subject's
  grants allow it and the scope admits it. A scope is always intersected with the subject's own
  rights at use time; a scope that names actions the subject does not hold yields nothing for them.
- **Resource types (closed list, Q-AUTHZ-10).** A scope can name exactly the resource types the
  grant model has:

  | Scope part | Resource type | Priority |
  |---|---|---|
  | `repositories` | Repository content: the resource set of R-AUTHZ-004 and R-AUTHZ-005 plus actions | MVP |
  | `system` | System capabilities (R-AUTH-025, R-AUTHZ-010) | P1 |
  | `builds` | Build records: `{"builds": {"include": [...], "exclude": [...]}, "actions": [...]}` with the name globs of R-AUTHZ-027 | P1 |

  There is no project resource type before project roles exist (TOK-011, TOK-013, P2). There is
  no per-repository configuration type: repository configuration is the system capability pair
  `repositories:read-config` and `repositories:write-config` and applies instance-wide;
  per-team configuration rights come from projects (R-AUTHZ-026), not from tokens.
- **Repository part (Q-AUTHZ-9).** `inherit: true` admits every repository right and, from P1,
  every build right of the subject. Otherwise each element of `repositories` has the shape of a
  policy's resource set plus `actions`, and the scope admits an action on a coordinate if an
  element matches the coordinate and lists the action. A restricted scope without a `builds`
  element admits no build action.

  ```json
  {
    "inherit": false,
    "repositories": [
      {
        "repositories": ["npm-internal", "npm-team-*"],
        "packages": { "include": ["@web/*"], "exclude": [] },
        "paths": { "include": [], "exclude": [] },
        "actions": ["read", "deploy"]
      }
    ]
  }
  ```

  `packages` and `paths` are optional and follow the matching rules of R-AUTHZ-005, so one matcher
  and one validator serve policies, token scopes and CI inline grants. A scope element with
  top-level `include` or `exclude` fields is rejected with 400 `invalid_scope` (unknown field,
  R-AUTH-024).
- **Roles.** `admin: true` lets the token exercise every capability of `admin`, `content:bypass`
  included, if and only if the subject holds `admin` at use time. Otherwise the token's
  capabilities are the `system` items for which one of the subject's roles grants that
  capability. `content:bypass` is never a `system` item.
- **Use time.** Everything is evaluated when the token is used. A right the subject loses
  disappears from the token at once (within the bound of R-AUTHZ-028); a right it gains appears
  only as far as the scope admits it.
- **CI tokens** carry `{"inherit": true}` over their CI grants, unless the exchange request
  narrowed them with `scope` ([gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-007), in which case the
  token carries the translated repository grants as its scope (`{"inherit": false,
  "repositories": [...]}`, one element per scope item); the caps of R-CI-013 act as a further
  ceiling of the same kind.

Differences from the catalog, kept on purpose:

- A resource scope never grants rights the subject does not have. TOK-012 lets administrators
  mint grants that no policy records; here every right traces back to a policy, a role or a CI
  mapping, which keeps audit and explanation complete. The catalog leaves open whether a resource
  scope is intersected with the subject's rights; in trove it always is.
- trove has no group-scoped token and therefore no group-list syntax (TOK-010). A token's rights
  always come from its owner. Automation that should carry the rights of some groups uses a
  service account that is a member of those groups (R-AUTH-020) and a token of that service
  account. The catalog's open points about quoting a group list and about a singular keyword alias
  do not arise, and no transient subject is ever created.
- The administrator scope (TOK-009) is `admin: true`.

Acceptance criteria:

- An administrator's personal access token with the default scope gets 403 on
  `POST /api/v1/repositories`; a token with `{"inherit": true, "admin": true}` gets 201.
- Alice (who may deploy `@web/*`) creates a token whose scope admits only `read` on
  `["npm-all", "@any-local:npm", "@any-remote:npm"]`. With it, `npm install` through `npm-all`
  succeeds and `npm publish` gets `E403`.
- Alice's group loses `deploy`; within the bound of R-AUTHZ-028, her token whose scope still
  admits `deploy` can no longer publish through any replica.
- Alice's token with scope `{"repositories": [{"repositories": ["npm-internal"], "packages":
  {"include": ["@web/*"]}, "actions": ["read"]}]}` gets `@web/button` through `npm view` against
  `npm-internal` and `E404` for `@pay/core`, which her grants would let her read. Creating a token
  whose scope element carries `"include": ["@web/*"]` at top level answers 400 `invalid_scope`.

### R-AUTHZ-014 · CI tokens in evaluation
Priority: **MVP** · Covers: CIOIDC-013, CIOIDC-014, TOK-015

How mappings match, how inline grants are templated and escaped, and what the issuance record
stores are specified in [gitlab-ci-oidc.md](gitlab-ci-oidc.md) (R-CI-009, R-CI-013, R-CI-014,
R-CI-020). This requirement fixes how the evaluator treats a CI token:

- **Rights.** The union of: policies whose entries name one of the token's mappings
  (`ci:<issuer>/<mapping>`, the mappings recorded at exchange, R-AUTHZ-002); policies naming the groups those mappings list; the frozen inline
  grants of the issuance record; and from P1 the service accounts of R-CI-024. This union is
  narrowed by the caps of R-CI-013 and, where configured, by the triggering user's own rights at
  use time (R-CI-024, P1).
- **Inline grants are resource sets.** Each rendered inline grant MUST have the shape of
  R-AUTHZ-004 and R-AUTHZ-005 (selectors, `packages` or `paths`, `actions`) plus its `ownership`
  value, so the same matcher evaluates policies, scopes and CI grants. A backslash produced by the
  escaping of R-CI-014 makes the next character literal (R-AUTHZ-005), so a claim value can never
  widen a pattern.
- **Ownership default (Q-AUTHZ-6).** An inline grant whose `actions` contain `deploy`,
  `overwrite` or `delete` and that omits `ownership` MUST be saved with `"ownership":
  "bind_project"`; any other inline grant without the field is saved with `"ownership": "none"`.
  The default is applied when the mapping is saved and stored, so every read of the mapping, every
  issuance record and every explanation shows the effective value. A mapping that wants several
  GitLab projects to share the names they publish says `"ownership": "none"` explicitly; the
  mapping then becomes the owner of those names (R-AUTHZ-018 rule 2), and the save succeeds with
  the warning `shared_ownership` ([gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-010, R-CI-013).
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
- Removing a mapping from a policy's entries makes `npm install` with an already issued token of
  that mapping fail on every replica within the bound of R-AUTHZ-028, without a new exchange.
- `POST /api/v1/authz/explain` for a CI token lists its inline grants with
  `"source": {"kind": "ci-inline", "ordinal": …}`.
- A mapping saved with the inline grant `{"repositories": ["npm-internal"], "packages":
  {"include": ["@acme/tools"]}, "actions": ["read", "deploy"]}` reads back with `"ownership":
  "bind_project"`; a read-only inline grant reads back with `"ownership": "none"`. Two GitLab
  projects matched by that mapping publish `@acme/tools`: the first owns it, the second gets
  `E403` `package_owned_by_other_owner`.

### R-AUTHZ-015 · Evaluation algorithm
Priority: **MVP** · Covers: IAM-019, IAM-024

Evaluation MUST behave like the following pseudo-code. Implementations may reorder or cache steps
as long as every decision is the same. Grant evaluation MUST NOT query PostgreSQL in the steady
state; it reads the compiled grant index of R-AUTHZ-028 in the replica's memory, never Valkey.
Ownership rows are read inside the write transaction (R-AUTHZ-018).

```go
// Decide runs for every coordinate of a request; the request proceeds only if every
// coordinate is allowed. The resolver of R-REPO-019 calls MemberUsable per member.
func Decide(ctx context.Context, sub Subject, c Coordinate) Decision {
    // One index per request: the first call waits while the index is staler than
    // authz.cache.max_staleness or below Trove-Authz-Min-Epoch (R-AUTHZ-028, R-AUTHZ-032);
    // later calls of the same request return the same index. An epoch the database never
    // reached was answered 412 before the first Decide.
    idx, err := index.ForRequest(ctx)
    if err != nil {
        return Deny(ReasonStale)                                    // answered 503 authz_stale
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
    if !idx.Exposure(c.Endpoint).Admits(a, c.Addressed, c) {        // endpoints.md R-EP-013: every subject,
        return false                                                // decided on the addressed repository
    }
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
    for _, p := range sub.Principals {                              // user, groups, sa, ci mappings, anonymous, authenticated
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
        return c.Endpoint.CacheFill &&                              // endpoints.md R-EP-019
            Allowed(sub, idx, m.PopulateAction, mc)                 // otherwise the cache only
    }
    return true
}
```

- Writes MUST be evaluated again when they commit (after a long upload), against the grant index
  that the committing replica holds at that moment (R-AUTHZ-028) and the ownership rows read in
  the commit transaction. A refusal at commit discards the upload and answers 403. The re-check
  waits for a stale index like any decision; it never holds the commit transaction open while it
  waits (R-ARCH-019).
- Every request is decided on its own, on whichever replica receives it, and no decision is kept
  in a replica's memory for a later request (INV-18). In a protocol that spans several requests
  (chunked or resumable uploads, P1, [storage.md](storage.md) R-STOR-011), each request is
  authorized when it arrives, and the request that commits runs the commit-time re-check on its
  own replica.
- `reasonFor` returns `deny.exposure` when the endpoint's exposure refuses a read,
  `deny.endpoint_read_only` when it refuses a write ([endpoints.md](endpoints.md) R-EP-013),
  `deny.scope` when a grant would allow the action but a ceiling does not,
  `deny.triggering-user` when only the triggering-user bound refuses, and `deny.no-grant`
  otherwise.
- The coordinate carries the request's endpoint and its addressed repository, the one the client
  named; `WithRepo` changes neither, so exposure is decided on the addressed repository while grants
  are decided per member ([endpoints.md](endpoints.md) Q-EP-11). The exposure rules are part of the
  compiled index and follow its staleness rules (R-AUTHZ-028). On an endpoint with `exposure: all`
  the first check of `Allowed` admits everything, so every decision on the default endpoint stays as
  this page describes it.
- The shadowing check on the first version of a name (R-AUTHZ-007, R-REPO-048) is not part of
  `Decide`, because it may probe an upstream: it runs after `Decide` allowed the write and before
  the commit, without a database transaction held open. It asks `Allowed(sub, idx, Manage, c)`
  for the local repository, so a token's scope and the CI caps bound it like any action, and
  refuses with reason `deny.shadowing`.
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

Acceptance criteria: each row of the trace is an automated end-to-end test in the e2e profile
`scatter` of [architecture.md](architecture.md) R-ARCH-032 (three `web` replicas, every request
balanced round robin, no session affinity). The policies are written first. The test waits until
every replica reports their epoch in `trove_authz_epoch`, or it sends the epoch in
`Trove-Authz-Min-Epoch` where the client can (R-AUTHZ-032). Each row is then issued once on each
replica, with the same result.

### R-AUTHZ-016 · Denial responses and hidden existence
Priority: **MVP** · Covers: IAM-032, ADMAPI-002, PKG-NUGET-016, PKG-NPM-007

A refused request is answered as follows (first matching row wins):

| Situation | Status |
|---|---|
| Grant index not confirmed current within the wait of R-AUTHZ-028 | 503 `authz_stale` with `Retry-After` |
| No credential: anonymous access off, or `anonymous` lacks what the request needs (reads and writes alike) | 401 with the challenge of R-AUTH-002 |
| `Trove-Authz-Min-Epoch` names an epoch the database has not reached (R-AUTHZ-032) | 412 `authz_epoch_unknown` |
| Read-type request (request kinds `artifact`, `metadata`, `search`, R-REPO-003) without `read` on the coordinate; for a virtual repository, without `read` on it | 404 |
| Read-type request that the endpoint's exposure does not admit ([endpoints.md](endpoints.md) R-EP-013) | 404, byte-identical to a missing resource; R-AUTHZ-017 does not change it |
| Write-type request on an endpoint with exposure rules ([endpoints.md](endpoints.md) R-EP-013) | 403 `endpoint_read_only` |
| Read on a remote repository that would need to populate, caller may read but not populate (R-AUTHZ-008) | 403 `populate_not_permitted` |
| Write-type request without a required action | 403, naming the missing action |
| Ownership refused (R-AUTHZ-018) | 403 `package_owned_by_other_owner` |
| Shadowing check refused on the first version of a name ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-048, R-AUTHZ-007) | 403 `shadows_remote_package`; 503 `shadow_check_unavailable` with `Retry-After` while a remote member cannot be asked |
| Management operation without the required capability | 403 |

- The `503` for an unconfirmed index carries `Retry-After`, which with a `503` tells the client how
  long the service is expected to be unavailable
  ([RFC 9110 §10.2.3](https://www.rfc-editor.org/rfc/rfc9110#section-10.2.3)). It does not depend
  on the coordinate, so it reveals nothing about existence. npm retries `5xx` answers except on
  `POST` and streamed bodies ([make-fetch-happen](https://github.com/npm/make-fetch-happen/blob/main/lib/remote.js)),
  and with random distribution its retry usually reaches another replica.
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
- On a virtual repository the setting changes only the answer to a refused endpoint gate: with
  `false`, a caller without `read` on the virtual repository gets 403 instead of 404. Content in
  members the caller cannot read is answered as missing whatever the setting of the virtual
  repository or of the member (R-AUTHZ-009 rule 8), so the setting can never turn a virtual
  repository into an existence oracle for its members.

Configuration:

```yaml
authz:
  hide_existence: true  # default (proposed)
```

Acceptance criteria:

- With `hideExistence: false` on `npm-internal`, Carol gets 403 for `@web/button` (exists) and
  404 for `@web/nope`; with the default she gets 404 for both.
- With `hideExistence: false` on `npm-all` and on `npm-internal`, Bob, who may read `npm-all` and
  `npmjs` but not `npm-internal`, gets 404 through `npm-all` for `@web/button`, which exists only
  in `npm-internal`. Carol, without `read` on `npm-all`, gets 403 there.

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
  `off` (a field of the repository in [repositories-and-proxy.md](repositories-and-proxy.md)). A
  new npm or NuGet local repository MUST get `enforce` when the create request does not set the
  field (Q-AUTHZ-2); `off` is an explicit choice per repository.
- Independently of that setting, ownership is checked for every write that a CI inline grant with
  `ownership: bind_project` allows (R-CI-015). Writing inline grants carry `bind_project` unless
  they say otherwise (R-AUTHZ-014, Q-AUTHZ-6).
- Ownership applies only to coordinates with a package name. A write to a coordinate without one
  (a raw file outside any package, a repository of a format without package names) passes the
  ownership check, also under a `bind_project` grant.
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

   The first version of a name in a local repository must in addition pass the shadowing check of
   [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-048 (R-AUTHZ-007, "First version
   of a name"), also in a repository where ownership is not checked. When a remote member of a
   virtual repository that contains the local repository serves the name, the write also needs
   `manage` on the coordinate, `content:bypass`, or a manual namespace claim of the local
   repository that covers the name (R-REPO-023); otherwise it gets 403 `shadows_remote_package`
   (audit reason `deny.shadowing`). An unclaimed name (first bullet above) does not pass this check
   by itself, and a refused write creates no ownership claim. From P1 the same applies to copy,
   move and trash-can restore targets.
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
   guarded by the primary key. Of two concurrent first publishes, on one replica or on two, one
   commits the claim; the other then sees an owned name, is checked by rule 1 and is refused unless
   it is an owner. The claim runs as `INSERT … ON CONFLICT DO NOTHING`, followed by a read of the
   owners in the same transaction. Under `READ COMMITTED` the insert waits for the concurrent claim
   and then does nothing, and the following read sees that claim, because each statement takes a
   new snapshot ([transaction isolation](https://www.postgresql.org/docs/current/transaction-iso.html#XACT-READ-COMMITTED)).
   The publish therefore needs no retry. No advisory lock, no Valkey lock and no session state is
   needed, so the claim works through a transaction-mode pooler
   ([architecture.md](architecture.md) R-ARCH-019).
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
- A local npm repository and a local NuGet repository created without `packageOwnership` both read
  back `"local": {"packageOwnership": "enforce", …}`.
- `npm-all` = [`npm-internal`, `npmjs`]; the stub upstream of `npmjs` serves `lodash`. Service
  account `ci-bot` holds `deploy`, but not `manage`, on `npm-internal`. Its `npm publish` of
  `lodash@4.17.22` through `npm-all` gets `E403` `shadows_remote_package`, and no ownership claim
  for `lodash` exists afterwards. `lead-web` pre-assigns `sa:ci-bot` as owner of `lodash`
  (R-AUTHZ-019); the same publish is refused again with `shadows_remote_package`, because an owner
  record is not a namespace claim. After `lead-web` sets the manual namespace claim `lodash` on
  `npm-internal` (R-REPO-023), the publish succeeds.

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
  `precondition_required` without it ([RFC 6585 §3](https://www.rfc-editor.org/rfc/rfc6585#section-3))
  and 412 `precondition_failed` on a version mismatch. `If-Match: *` is the deliberate blind write
  ([api-and-ui.md](api-and-ui.md) R-API-012, Q-API-2).
- Every successful write notifies the `authz` change area in its transaction, which increments the
  authorization epoch (R-AUTHZ-028). It also emits an audit event with before and after
  (R-AUTHZ-029). The response carries the new epoch in the header `Trove-Authz-Epoch`. A later
  request names that epoch in `Trove-Authz-Min-Epoch` to see the change on whichever replica
  answers it (R-AUTHZ-032).
- Malformed JSON answers 400. Semantic validation answers 422 with one of: `unknown_principal`,
  `unknown_repository`, `unknown_format`, `invalid_pattern`, `invalid_action`,
  `duplicate_principal`, `anonymous_write_action`, `anonymous_dynamic_selector`,
  `authenticated_write_action`, `virtual_action_not_applicable`, `action_not_allowed`. Warnings do
  not block saving: `path_patterns_on_package_format`, `selector_matches_nothing`,
  `anonymous_populates_remote` (R-AUTHZ-008).
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

- Create, read, replace without `If-Match` (428, nothing stored), replace with a stale `If-Match`
  (412), replace with the current one (200) and delete (204), all through `curl`; each change
  appears as one audit event.
- A `PUT` returns epoch `n`. The next request through the Service with
  `Trove-Authz-Min-Epoch: n` observes the change on whichever replica answers it. Without the
  header, every replica observes it within 1 s (p99 over 100 trials) while Valkey is available, and
  within `cluster.epoch_poll_interval` plus 1 s while Valkey is stopped.
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
  repository. Checks of package-mutating actions include the ownership rule; the ownership rows of
  all checks of one call are read with one query, so a call costs at most one database round trip
  however many checks it holds.
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
- A delegated manager MUST NOT add `anonymous` or `authenticated` (403, R-AUTHZ-030). The
  catalog's finer control over anonymous grants by non-administrators (IAM-034) is P2.
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
    accounts and CI mappings, never to `anonymous` or `authenticated`;
  - with `manageRepositories`: create, change and delete the project's repositories.

  They cannot bind system roles, cannot see global policies (unless they hold `policies:read`) and
  cannot assign repositories outside their project. CI mapping delegation stays with R-CI-023 and
  is independent of projects in P1; an administrator can name a project's administrator group as
  the delegate there (Q-AUTHZ-7).
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
  version             bigint NOT NULL DEFAULT 1,                             -- ETag
  created_at          timestamptz NOT NULL DEFAULT now(),
  updated_at          timestamptz NOT NULL DEFAULT now(),
  updated_by          uuid NULL REFERENCES principal (id) ON DELETE SET NULL
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
| `GET /api/v1/projects/{key}` | Read | 200 with `ETag: "<version>"`; 404 | `projects:read` or one of its administrators |
| `PUT /api/v1/projects/{key}` | Replace display name, description, switches; `If-Match` required | 200 with the new `ETag`; 404; 412 `precondition_failed`; 422; 428 `precondition_required` | `projects:write` |
| `DELETE /api/v1/projects/{key}` | Delete (`If-Match` optional) | 204; 404; 409 `project_not_empty`; 412 | `projects:write` |
| `PUT /api/v1/projects/{key}/admins/{principalRef}` | Add a project administrator | 201 or 200 | `projects:write` |
| `DELETE /api/v1/projects/{key}/admins/{principalRef}` | Remove a project administrator | 204 | `projects:write` |
| `PUT /api/v1/projects/{key}/repositories/{repoKey}` | Assign (`?move=true` to move) | 204; 409 assigned elsewhere | `repositories:write-config` |
| `DELETE /api/v1/projects/{key}/repositories/{repoKey}` | Unassign | 204 | `repositories:write-config` |

- `PUT /api/v1/projects/{key}` follows [api-and-ui.md](api-and-ui.md) R-API-012 and Q-API-2:
  without `If-Match` it answers 428 and changes nothing, `If-Match: *` is the deliberate blind
  write, and a stale tag answers 412 with `currentEtag`. The privilege switches make this a grant
  change: a stale `PUT` could switch `manageRepositories` back on after another administrator
  turned it off. Adding or removing a project administrator and assigning a repository bind one
  pair and are exempt.

Acceptance criteria:

- Project `web` has administrator group `web-leads`; `web-npm` is in it, `npm-internal` is not. A
  member of `web-leads` creates a policy with `"project": "web"` on `web-npm` (201) and is refused
  on `npm-internal` (422 `repository_outside_project`).
- The same project administrator creating repository `npm-web-extra` gets 403 while
  `manageRepositories` is off. With the switch on, that key gets 422 (missing prefix), and
  `web-extra` succeeds.
- `PUT /api/v1/projects/web` with `{"manageRepositories": true, …}` and no `If-Match` answers 428
  and the switch stays off; with the `ETag` read before another administrator's `PUT` it answers
  412.

### R-AUTHZ-027 · Build resources
Priority: **P1** · Covers: IAM-025

- Policies with `"resourceType": "build"` select build records by build name: `resources.builds`
  holds `include` and `exclude` lists of name globs (syntax of the `packages` dimension). Repository
  selectors and path patterns do not apply to build policies.
- Actions: `read` (view build records and their files), `annotate` (properties on build records),
  `deploy` (upload and promote build records), `delete`, and `manage` (delegated management of the
  policy, R-AUTHZ-025). `overwrite` is not applicable (422 `invalid_action`).
- Build records, their API, retention and storage are specified on a dedicated spec page
  `builds.md` (requirement prefix `R-BLD`), written before P1 work on builds starts (Q-AUTHZ-8).
  CI grants, the evaluation algorithm and audit apply unchanged, with the coordinate (build name,
  action). Token scopes admit build actions through `inherit: true` or their `builds` part
  (R-AUTHZ-013).

Acceptance criteria: a policy that gives `ci:gitlab-internal/acme-publish` `read` and `deploy` on
build names `acme/**` lets that mapping's tokens upload a build record named `acme/web/frontend`,
and refuses `other/app` with 403.

### R-AUTHZ-028 · Compiled grant index and invalidation across replicas
Priority: **MVP** · Covers: trove-only

Every `web` and `worker` replica evaluates against an in-memory grant index. It keeps the index
consistent with PostgreSQL through the change areas of [architecture.md](architecture.md)
R-ARCH-018 (Q-AUTHZ-11). The change log in PostgreSQL carries correctness. A Valkey message only
makes a change take effect sooner. Until 2026-10-28 this requirement had its own `authz_epoch`
table and `LISTEN/NOTIFY` on a dedicated connection per replica; both are gone (Q-ARCH-15).

- **Index.** Built from:
  - policies, including entries that name CI mappings;
  - role bindings;
  - repositories: key, class, format, project, virtual membership and deploy target, and the
    authorization settings of R-AUTHZ-031's settings table;
  - projects and their administrators.

  For each repository ID, the index maps principal IDs to compiled grants: selectors expanded
  against the current repository list, patterns compiled. A new index is built on the side and
  swapped in atomically. Requests in flight finish on the index they started with, except for the
  commit-time re-check of writes (R-AUTHZ-015).
- **Who builds it.** Every `web` and `worker` replica (and `all`) builds the index during start-up
  step 8 of [architecture.md](architecture.md) R-ARCH-024, before it opens the client listener or
  starts job workers, so the `startup` check never passes without one. A job that decides for a
  subject (a P1 bulk copy, for example) decides on the `worker` replica that runs it. The `backup`
  role and `trove admin` write changes but evaluate nothing and build no index.
- **Consistent build.** A build reads every input in one `REPEATABLE READ READ ONLY` transaction on
  the main pool. Its first statement reads `epoch` and `changed_at` of the areas `authz` and
  `repo_config` from `cluster_epoch`. Every query of the transaction sees the snapshot taken at its
  first statement
  ([repeatable read](https://www.postgresql.org/docs/current/transaction-iso.html#XACT-REPEATABLE-READ)).
  The index is therefore the state of one moment, labelled with that moment's epochs and their
  `changed_at` values (the index's *label*). No build can combine a policy's entries from one
  commit with its selectors from another, which could grant the new entries on the old selectors.
  - A read-only transaction never meets a serialization failure.
  - It is transaction-scoped, so it works through a transaction-mode pooler.
  - Its context deadline and its `SET LOCAL statement_timeout` are `authz.cache.max_staleness`.
  - It holds its connection only for its queries, never across a wait (R-ARCH-019).
  - It runs on the request pool, like the epoch reads below, never on the replica's control
    connection (R-ARCH-019). Authorization work therefore never delays the change-log poll, the
    `database` check or the job heartbeats that readiness and job ownership depend on. When the
    request pool is exhausted, the build queues for a connection like a request. Requests that wait
    for a stale index hold no connection (below), so they never stand in its way.
- **Epoch.** The authorization epoch is the epoch of the change area `authz`
  (`cluster_epoch.epoch` of the row `area = 'authz'`). The following writes call
  `cluster.Notify("authz", …)` in their transaction, which increments it (R-ARCH-018):
  - every write to a policy, its selectors, patterns or entries;
  - every write to a role binding;
  - every write to a project or its administrators, and every project repository assignment
    (R-AUTHZ-026, P1), which as a repository change also notifies `repo_config`;
  - every write that `trove admin post-restore` or a restore's journal replay makes to these
    objects (R-AUTHZ-031).

  Repository changes notify `repo_config` ([repositories-and-proxy.md](repositories-and-proxy.md)),
  and the index depends on both areas. The payload names the changed object's type and ID, never a
  grant. A rollback leaves neither an epoch nor a message.
- **When a replica rebuilds.**
  - The change-log poll applies an epoch of `authz` or `repo_config` higher than the index's.
  - The poll flushes either area. Causes: a gap in the log, an unknown payload version, or an epoch
    that went backwards after a failover or a restore (R-ARCH-018,
    [ha-and-helm.md](ha-and-helm.md) R-HA-004). After a flush the next build is installed whatever
    its epochs.
  - **A reused epoch.** A PostgreSQL failover can lose committed transactions under asynchronous
    replication (R-HA-004). When later writes raise the area to the same number before this
    replica's first successful poll after the failover, the epoch never looks lower, yet it now
    stands for different changes: the index may hold a lost grant and miss a later removal. Every
    comparison of this page with the database epochs (the poll's reading of `cluster_epoch`, a
    message check, a `Trove-Authz-Min-Epoch` read) therefore compares `changed_at` as well. An area
    whose database epoch equals the index's but whose
    `changed_at` differs counts as flushed, and the next build is installed whatever its epochs.
    `changed_at` is written by the same statement that raises the epoch (R-ARCH-018), and two
    transactions separated by a failover never share it.
  - A Valkey message on `trove:<ks>:inv:v1` names one of the two areas with an epoch higher than
    the index's. Such a message only starts work early. It never advances the applied epoch and
    never confirms an index. The replica first reads the two epochs from `cluster_epoch` (one
    indexed query) and builds only if they differ from the index's label. These reads share the
    coalesced epoch read of R-AUTHZ-032, which starts at most every 50 ms per replica. A forged,
    duplicated or late message therefore never causes a build, and a flood of them causes at most
    20 small queries per second per replica.
  - The index is older than `authz.cache.safety_ttl`. This full rebuild, jittered by up to 10 %,
    bounds a defect in which some write path changed an input without notifying.

  Rebuilds are coalesced: at most one runs per replica and at most one waits behind it. A finished
  build is swapped in only if its epochs are not lower than those of the installed index, so a slow
  build never replaces a newer one.
- **Valkey holds nothing of authorization** (Q-ARCH-16, [architecture.md](architecture.md)
  R-ARCH-030). No grant, compiled index or decision is written to Valkey or read from it.
  Unavailable Valkey, Valkey that lost its data, and forged messages cost propagation speed only.
  They never make a replica refuse, never make it unready, and never let it decide on older grants
  than the change log allows.
- **Confirmation and staleness.** The index is *confirmed* at the start time of the most recent
  successful change-log poll, or of its own build, that found `epoch` and `changed_at` of `authz`
  and `repo_config` equal to the index's label. Its staleness is the time since then
  (`trove_authz_index_staleness_seconds`). Staleness grows when the poll cannot reach PostgreSQL
  (`cluster.ChangesFresh()`, R-ARCH-018) or when a needed build fails or waits for a connection.
  It never grows because Valkey is unavailable.
- **Bound.** A committed change is in force on a replica whose poll works within
  `cluster.epoch_poll_interval` plus the poll query plus one build. With the defaults and a request
  pool that has a free connection, that is at most 2 s, with Valkey or without it. While Valkey is
  available it is typically 100 ms plus one build ([architecture.md](architecture.md) P-13; the
  build is this page's addition). No replica decides on an index whose staleness exceeds
  `authz.cache.max_staleness`. A removed grant is therefore never honoured for longer than
  `authz.cache.max_staleness` plus one build after its commit, on any replica and under any
  failure, a PostgreSQL failover that lost commits included. With the defaults that is about
  10.5 s.
- **Drain.** A replica that received `SIGTERM` keeps its change-log poll and its rebuilds running
  until its last request has finished ([architecture.md](architecture.md) R-ARCH-024, shutdown
  step 6). The requests it still serves during the drain delay, and the commit-time re-checks of
  uploads that complete during the shutdown timeout, are therefore decided on a confirmed index
  while PostgreSQL answers, and a drain alone never sends them into the stale-index wait below.
- **Stale index (Q-AUTHZ-13).** A request that needs authorization may find the index staler than
  `authz.cache.max_staleness`. It then waits for a confirmation for as long as a request waits for
  an unreachable database: until its own deadline or `database.retry.max_elapsed`, whichever
  comes first ([ha-and-helm.md](ha-and-helm.md) R-HA-009). It holds no database connection while
  it waits. Afterwards it is decided on the confirmed index, or answered `503` `authz_stale` with
  `Retry-After` (reason `deny.stale`, R-AUTHZ-016, R-AUTHZ-029).
  - This rule gates no readiness. A replica whose poll fails leaves the Service through the
    `database` check and the `invalidation` check of R-ARCH-018, which depend on PostgreSQL only,
    so this page adds no check that could undercut the readiness window of
    [ha-and-helm.md](ha-and-helm.md) R-HA-008.
  - The default of `authz.cache.max_staleness` lies below that window (about 15 s), so a replica
    stops deciding on unconfirmed grants before it leaves the Service.
  - Start-up refuses a value below 3 × `cluster.epoch_poll_interval` and names the key, because a
    single slow poll must not stop decisions.
- **Not in this index:**
  - group memberships, principal status, tokens and sessions, which
    [identity-and-authentication.md](identity-and-authentication.md) R-AUTH-023 caches and
    invalidates through the area `auth`;
  - CI mappings and issuance records ([gitlab-ci-oidc.md](gitlab-ci-oidc.md), areas `ci_mappings`
    and `ci_issuer_keys`);
  - ownership rows, which are read in the write transaction, so no replica decides ownership on a
    cached copy.
- **One configuration for every replica.** The `authz.*` keys of this page come from the one
  configuration file that every role and pool shares ([ha-and-helm.md](ha-and-helm.md) R-HA-002,
  R-HA-062), and decisions depend on them (INV-18). A rollout that changes one of them runs old
  and new values side by side until it ends; the operator controls that window.
- **Metrics:**
  - `trove_authz_epoch` (gauge): the `authz` epoch of the installed index;
  - `trove_authz_index_rebuild_seconds` (histogram);
  - `trove_authz_index_rebuilds_total{trigger}` (counter): `startup`, `poll`, `message`, `flush`,
    `safety`, `min_epoch`;
  - `trove_authz_index_staleness_seconds` (gauge);
  - `trove_authz_stale_waits_total{outcome}` (counter): `decided`, `refused`;
  - `trove_authz_evaluation_seconds` (histogram);
  - `trove_authz_decisions_total{outcome, reason, action}` (counter).
- **Rebuild cost.** Building the index from 10,000 policies and 1,000 repositories SHOULD take at
  most 500 ms. Each change of `authz` or `repo_config` makes every replica build once, so with
  n replicas a change costs n builds. At administrative change rates that is small, and Q-AUTHZ-11
  names the point at which incremental updates replace full builds.
- **Completeness test.** A test runs every write operation of the policy, role-binding, project and
  repository APIs, of the journal replay and of the `trove admin` commands that write these tables
  (`service-account create --admin` binds a role,
  [identity-and-authentication.md](identity-and-authentication.md)) against a test database. It
  fails if an input table of the index changed in a transaction that advanced neither the `authz`
  epoch nor the `repo_config` epoch.

Data model. The authorization epoch is the `authz` row of `cluster_epoch`
([architecture.md](architecture.md) R-ARCH-018). The migration that introduces the area inserts
that row. There is no table `authz_epoch`. Every table of this page has the backup export class
`data` ([backup-and-restore.md](backup-and-restore.md) R-BAK-004). A restore raises the epoch by
2^32 through the registration of `cluster_epoch.epoch` (R-BAK-022), so every epoch a client holds
from before the restore lies below the restored one. `authz_policy.version` and `project.version`
are client-visible ETags (R-AUTHZ-021, R-AUTHZ-026), so both are registered with
`meta.RegisterRevisionColumn` as well. Otherwise, after a restore, a client holding the ETag of a
version written after the snapshot could pass `If-Match` against different content and overwrite a
policy it never saw.

```sql
-- the authorization epoch, written only by internal/cluster (R-ARCH-018); the index label is
--   SELECT area, epoch, changed_at FROM cluster_epoch WHERE area IN ('authz', 'repo_config');

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

M1 creates these tables without `project_id` and without the `project` tables; the project work adds the
column in its own migration (Q-AUTHZ-14).

Configuration:

```yaml
authz:
  cache:
    max_staleness: 10s  # default (proposed): no decision on an index unconfirmed for longer (Q-AUTHZ-13)
    safety_ttl: 10m     # default (proposed): full rebuild at least this often, jittered
# Until 2026-10-28 this section also had poll_interval (5s), and max_staleness defaulted to 30s.
# The poll is now cluster.epoch_poll_interval of architecture.md R-ARCH-018.
```

Acceptance criteria (three `web` replicas behind one Service without session affinity; the test
reaches replicas A and B directly where a criterion names them):

- A policy is deleted through replica A. A request through replica B that depended on it is refused
  within 1 s (p99 over 100 trials). With Valkey stopped, it is refused within
  `cluster.epoch_poll_interval` plus 1 s, and every replica's `/readyz` answers `200` with
  `"degraded":["valkey"]`.
- B's Pub/Sub connection is killed on the Valkey primary (`CLIENT KILL TYPE pubsub`). A policy
  change through A is in force on B within `cluster.epoch_poll_interval` plus 1 s.
- A fault-injection build drops every Valkey message of the `authz` area and injects forged ones
  with random epochs. Over 1,000 policy changes, no replica decides on a removed grant later than
  `cluster.epoch_poll_interval` plus 1 s after its commit. The forged messages do not raise
  `trove_authz_index_rebuilds_total{trigger="message"}`.
- A test hook blocks B's change-log poll while its request pool keeps working.
  - From `authz.cache.max_staleness` on, requests through B wait.
  - When the hook is lifted within `database.retry.max_elapsed`, the waiting requests succeed.
  - While the hook stays, they end with `503` `authz_stale` and `Retry-After`, and their audit
    events carry `deny.stale`.
  - No decision in that time uses an index staler than `authz.cache.max_staleness`.
- PostgreSQL is unreachable from B for 40 s.
  - B turns not ready through its `database` check within the window of
    [ha-and-helm.md](ha-and-helm.md) R-HA-008.
  - No request on B is decided on an index staler than `authz.cache.max_staleness`.
  - B becomes ready again without a restart. The first request B decides afterwards already
    honours a policy change made through A during the outage.
- A test build pauses a build on B after its first statement. A policy change commits through A,
  B's poll starts a second build, and that build is installed. The paused build then finishes and
  is discarded. `trove_authz_epoch` on B never decreases, and the change stays in force.
- A loop commits policy updates that replace entries and selectors together while B rebuilds
  continuously. Every index B installs grants exactly what a database snapshot at its epochs
  grants.
- A reused epoch: a grant to Carol commits through A as `authz` epoch `e`, and B builds from it.
  Valkey is then stopped and B's poll paused by a test hook. As a failover that lost the commit
  would, the test deletes the grant and that `cluster_change` row and resets the `authz` row of
  `cluster_epoch` to `e − 1` with its earlier `changed_at`. A removal of one of Alice's grants then
  commits through A as epoch `e` again. When the hook is lifted, B installs a new index
  within `cluster.epoch_poll_interval` plus 1 s (`trove_authz_index_rebuilds_total{trigger="flush"}`
  rises): Carol's request is refused and Alice's removed grant no longer works.
- During the request-pool saturation test of [ha-and-helm.md](ha-and-helm.md) R-HA-008, a policy
  change commits through a replica outside the test. `/readyz` stays `200` on every replica, no
  heartbeat is late, and no decision uses an index staler than `authz.cache.max_staleness`.
- A Valkey Sentinel failover (`SENTINEL FAILOVER`) runs during a loop of 100 policy changes through
  A. Every change is in force on B within `cluster.epoch_poll_interval` plus 1 s. From 10 s after
  the new primary is promoted, changes are again in force on B within 1 s (p99), because B
  re-subscribed on the new primary ([architecture.md](architecture.md) R-ARCH-031). `/readyz`
  answers `200` on every replica throughout.
- B receives `SIGTERM` while a policy change commits through A and an upload to B completes
  during B's shutdown timeout. The upload's commit-time re-check honours the change, and no
  request on B is answered `authz_stale`.
- Start-up with `authz.cache.max_staleness: 2s` and `cluster.epoch_poll_interval: 1s` exits
  non-zero and names `authz.cache.max_staleness`.

### R-AUTHZ-032 · Seeing one's own permission change on any replica
Priority: **MVP** · Covers: trove-only

The Kubernetes Service distributes requests at random and without session affinity (INV-12), so
the request that follows a permission change may reach a replica that has not applied it yet
(INV-18). Until 2026-10-28 this page promised that a client could wait for propagation by watching
a replica's epoch. Under random distribution a client cannot choose the replica that answers next,
so it now names the epoch in its request instead (Q-AUTHZ-12).

- **Epoch out.** The writes that notify the `authz` area (R-AUTHZ-028) answer with
  `Trove-Authz-Epoch: <n>`, the epoch their transaction produced:
  - policy writes (R-AUTHZ-021);
  - role bindings (R-AUTHZ-022);
  - project writes, including administrators and repository assignments (R-AUTHZ-026, P1).

  A repository assignment changes a column of the repositories table, so it notifies `repo_config`
  as well. `POST /api/v1/authz/check` (R-AUTHZ-023) and `POST /api/v1/authz/explain`
  (R-AUTHZ-024) report the epoch of the index that answered.
- **Epoch in.** A request on the client listener that authenticated (a valid session or token) may
  send `Trove-Authz-Min-Epoch: <n>`. Authentication runs first, so an invalid credential gets its
  `401` before the header is read. The replica then decides every coordinate of that request,
  member checks of virtual resolution included, on an index whose `authz` epoch is at least `n`.
  1. If the installed index already has epoch `n` or higher, nothing happens.
  2. Otherwise the replica reads the `authz` epoch from `cluster_epoch` with a read that starts
     after the request arrived. Concurrent requests share one read, and reads start at most every
     50 ms per replica. The read runs on the request pool (R-AUTHZ-028); when PostgreSQL does not
     answer, it is retried like any database call ([ha-and-helm.md](ha-and-helm.md) R-HA-009), and
     the request ends with `503` `authz_stale` and `Retry-After` when its budget runs out. If the
     database epoch is lower than `n`, the request is answered `412` `authz_epoch_unknown` at once
     ([RFC 9110 §15.5.13](https://www.rfc-editor.org/rfc/rfc9110#section-15.5.13): a condition in
     the request header fields evaluated to false).
  3. Otherwise the replica starts a build (trigger `min_epoch`, coalesced as in R-AUTHZ-028). The
     request waits for an index with epoch `n` or higher under the wait rule of R-AUTHZ-028, then
     proceeds, or is answered `503` `authz_stale` with `Retry-After`.
- A value that is not a decimal integer between 1 and 2^63 − 1 answers `400` `invalid_header`. A
  request without a credential ignores the header, so an anonymous caller cannot probe the epoch.
- The header covers grants that were added and grants that were removed alike. A script that
  removes a right and then checks that it is gone sends the epoch of the removal.
- **After a restore or a lost commit.** Epochs from before a restore lie below every restored epoch
  ([backup-and-restore.md](backup-and-restore.md) R-BAK-022), so they pass at once. A PostgreSQL
  failover can lose a committed transaction under asynchronous replication
  ([ha-and-helm.md](ha-and-helm.md) R-HA-004). The database epoch can then lie below a value the
  client was given, and `412` tells the client that its change is not in the database.
  - A client that gets `412` stops sending that epoch, because the database may take a long time to
    reach it, and reads the object again (the policy's or project's `ETag`). Otherwise every later
    request of the client would fail with `412`.
  - The `412` is a hint, not a proof. If other writes have already raised the epoch to `n` again,
    the header passes although the client's own change was lost. The object's `ETag` tells the
    client whether its change is there; the grant index itself never decides on a lost change
    (R-AUTHZ-028, "A reused epoch").
- **Who sends it.** The UI sends the header on every request that follows one of its own permission
  changes (INV-17), and follows the `412` rule above. Package clients do not know the header. For
  them, and for every client that omits it, the bound of R-AUTHZ-028 applies: 1 s while Valkey is
  available, and `cluster.epoch_poll_interval` plus 1 s while it is not.
- **Not covered.** A repository created through one replica may be covered by a pattern selector of
  an existing policy. Using it through another replica relies on the `repo_config` propagation of
  [architecture.md](architecture.md) R-ARCH-018, which [repositories-and-proxy.md](repositories-and-proxy.md)
  owns.
- Metric `trove_authz_min_epoch_total{outcome}`: `current`, `waited`, `unknown`, `timeout`.

Acceptance criteria (three `web` replicas behind the Service and Valkey stopped. For the test,
`cluster.epoch_poll_interval` is raised to 30 s, `cluster.epoch_max_staleness` to 90 s so that the
`invalidation` check keeps passing, and `authz.cache.max_staleness` to 120 s, so only the header
can explain a prompt result):

- An administrator gives `group:team-mobile` `deploy` on `files-internal`; the `PUT` answers
  `Trove-Authz-Epoch: n`. A member of `team-mobile` then runs 20 uploads
  `curl -T app-<i>.zip -H "Trove-Authz-Min-Epoch: n" https://trove.example.com/files/files-internal/app-<i>.zip`
  at once. All 20 succeed, and the access logs show all three replicas. The administrator then
  removes the grant (epoch `m`). Twenty uploads with `Trove-Authz-Min-Epoch: m` all answer `403`.
- `Trove-Authz-Min-Epoch` set to the current epoch plus 1,000 answers `412` `authz_epoch_unknown`
  within 100 ms. The value `abc` answers `400` `invalid_header`. A request without a credential
  that sends the header gets the same answer as without it, and one with a revoked token gets
  `401`, not `412`.
- 1,000 requests per second with an unknown epoch, spread over the replicas, cause at most
  20 epoch reads per second per replica (`pg_stat_statements`).

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
| `authz.settings.changed` | changes to authorization settings stored in PostgreSQL, today the authorization settings of a repository (R-AUTHZ-031), with before and after | in the transaction of the change |

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
| `deny.shadowing` | Action allowed; the first version would shadow a package a remote member serves (R-AUTHZ-007, [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-048) |
| `deny.capability` | Management operation without the required capability |
| `deny.stale` | Grant index not confirmed current within the wait of R-AUTHZ-028 |

- **Allowed populates.** The populate event belongs to the request that fetches from the upstream
  and is written in its cache-fill transaction. A request that joins a collapsed fetch, in its own
  replica or on another one through the Valkey flight lock (INV-11,
  [architecture.md](architecture.md) R-ARCH-033), fetched nothing and writes no populate event;
  its read is audited as a read. The number of populate events therefore follows the number of
  upstream fetches, not the number of `web` replicas or concurrent requesters.
- **Repeated anonymous 401 answers** are aggregated so that scanners cannot flood the audit store.
  Where [operations-and-observability.md](operations-and-observability.md) R-OPS-015 records
  anonymous denials at all (`audit.record_anonymous_denials`), they use that requirement's
  deduplication, with the key replica and client address (after trusted-proxy processing) instead
  of principal, action, repository and path.
  - The first 401 for an address is written at once. The repeats within
    `audit.denial_dedup_window` become one summary event with their exact count.
  - With n `web` replicas, one address yields at most n first events and n summaries per window,
    whatever its request rate and however many paths it probes. The sum of the counts is exact.
    `trove_authz_decisions_total` counts every answer as well.
  - The deduplication state stays in the replica's memory. An audit record never depends on
    Valkey (R-OPS-015, INV-3), and nobody who can write to Valkey can suppress one: a value read
    from Valkey may only make trove refuse ([architecture.md](architecture.md) R-ARCH-030 rule 2).
- Asynchronous events go through the buffer of R-OPS-015. A replica that drains writes its buffer
  and its open denial summaries before it exits (R-ARCH-024, shutdown step 6), so a drain or a
  rollout loses no event while PostgreSQL is reachable. A replica killed without `SIGTERM` loses
  what its buffer and its open summaries held: normally less than one second of refused-read and
  refused-write events, and at most `audit.async_buffer` events while PostgreSQL is slow. The
  transactional events of the table above are never lost this way.
- Every event is also written to the structured log stream (INV-16).
- The CI page's events `ci.package_ownership.claimed` and `.conflict` (R-CI-020) describe the same
  facts from the CI side and carry the same request ID, so the two can be joined.
- Allowed reads are audited only with `reads: all`. The default `denied` (Q-AUTHZ-5) keeps the
  audit volume proportional to refusals, not to downloads; H-23 records what that leaves out.
- `authz.role.unbound`, `authz.policy.deleted`, `authz.policy.updated`,
  `authz.ownership.removed`, `authz.ownership.released` and `authz.settings.changed` are flagged
  `journal: revocation` in the event registry. A restore can then not undo them: neither a
  point-in-time restore nor the restore of a trove backup
  (R-AUTHZ-031, [ha-and-helm.md](ha-and-helm.md) R-HA-054,
  [backup-and-restore.md](backup-and-restore.md) R-BAK-024). The flag is MVP since 2026-10-28,
  because the backup job of INV-19 makes restores an MVP path (Q-HA-9).

Configuration:

```yaml
authz:
  audit:
    reads: denied   # default (proposed); none | denied | all; decided in Q-AUTHZ-5
```

Acceptance criteria:

- Trace row 2 of R-AUTHZ-015 produces one `authz.decision` with `outcome: allow` and `sources`
  naming `team-web-publish`; row 3 produces one with `outcome: deny`, `reason: deny.no-grant`,
  `status: 403`.
- With `reads: all`, `npm install` of one package produces events for the document and the tarball;
  with the default it produces none.
- When the audit table rejects inserts (in a test, by revoking `INSERT` from the application role),
  `npm publish` fails and no version is stored.

### R-AUTHZ-031 · Revocation journal entries and the narrowing comparator
Priority: **MVP** · Covers: trove-only

A database restore to time *t* undoes every authorization change made after *t*. There are two
ways to restore, and both are MVP:
- a point-in-time restore ([ha-and-helm.md](ha-and-helm.md) R-HA-025);
- the restore of a trove backup, where *t* is the backup's `snapshotAt` and can lie up to one
  backup interval in the past (INV-19, [backup-and-restore.md](backup-and-restore.md) R-BAK-024).

[ha-and-helm.md](ha-and-helm.md) R-HA-054 writes access-bearing changes a second time to object
storage. `trove admin post-restore` and `trove backup restore` replay the removals among them;
replay never grants (Q-HA-9). R-HA-054 leaves three things to the owning page: which events are
journaled, what an entry carries, and the comparator `narrows(current, after)` of its update rule.
This requirement moved from P1 to MVP on 2026-10-28 with R-HA-054, because the backup job makes a
restore from a scheduled backup an MVP path. For this page:

| Audit event (R-AUTHZ-029), flagged `journal: revocation` | Entry carries | Replay |
|---|---|---|
| `authz.role.unbound` | Role; principal ID and reference | Remove the role binding. The `last_admin` guard of R-AUTHZ-022 does not apply; R-HA-054 reports when no `admin` binding remains |
| `authz.policy.deleted` | Policy ID and name | Delete the policy with its selectors, patterns and entries |
| `authz.policy.updated` | Policy ID and name; the access-bearing state before and after the change: `resourceType`, `project`, `resources`, `entries` | Update rule of R-HA-054 with the policy comparator below |
| `authz.ownership.removed` | Domain, format, normalized package name; the removed owner (principal ID, or CI issuer ID and GitLab project ID) | Remove that owner |
| `authz.ownership.released` | As for `.removed`, for the last owner | Remove that owner; the name's state is left as below |
| `authz.settings.changed` | Repository ID; the changed access settings, before and after | Update rule of R-HA-054 with the settings comparator below |

- **Identity by ID.** Entries name principals, repositories, CI issuers and policies by their
  immutable IDs, with names for display only (R-AUTHZ-002, R-AUTHZ-004). Replay and the
  comparators compare IDs, so a principal or repository created later under a reused name never
  matches an entry.
- **Released names stay closed.** Replay removes the recorded owner and never sets the state
  `released`: a released name without versions could be claimed by any `deploy` holder (R-AUTHZ-018
  rule 1), so setting it would grant. A name that replay leaves without owners stays `claimed` and
  needs `manage` for every mutating operation, as after rule 6 of R-AUTHZ-018, whether or not it
  holds versions.
- **Policy comparator.** `narrows(current, after)` for a policy answers true only when all of the
  following hold, and false otherwise:
  1. `resourceType` and `project` are equal.
  2. The repository selectors of `after` are a subset of those of `current`, compared as sets:
     key selectors by repository ID, pattern and class selectors with their format filter as
     stored. Glob containment is not evaluated, so replacing `npm-*` by `npm-team-*` does not count
     as narrowing.
  3. In each pattern dimension (`packages`, `paths`, and `builds` for build policies), the exclude
     list of `after` contains every exclude pattern of `current`; and when the include list of
     `current` is non-empty, the include list of `after` is non-empty and a subset of it. Patterns
     are compared as the matcher compares them (NuGet name patterns lower-cased, R-AUTHZ-005), and
     again without glob containment. An emptied `packages.include` or `paths.include` therefore
     never counts as narrowing, because R-AUTHZ-005 reads an empty include list as unrestricted.
  4. For every entry of `after`, `current` has an entry for the same principal ID whose actions
     contain the actions of the `after` entry.

  Under these conditions every (principal, coordinate, action) triple that `after` grants is
  granted by `current` as well, so true means that `after` grants nothing that `current` does not.
  The containment test of R-HA-054 case 2 ("the
  journaled before-state is contained in the after-state") is `narrows(after, before)` with the
  same comparator.
- **A suspended policy** has all entries removed. Its name, selectors and patterns stay, it reads
  back with `"entries": []` and grants nothing until an administrator adds entries again from the
  after-state that the replay prints. Suspension, like every write replay makes, notifies the
  `authz` change area (R-AUTHZ-028) and writes its audit event as any policy change does. Replay
  runs while no `web` or `worker` replica serves: during a restore both roles are at zero
  ([ha-and-helm.md](ha-and-helm.md) R-HA-060). It publishes no Valkey message, because the restore
  Job holds no Valkey credentials (R-BAK-022). Replicas started afterwards build their index from
  the replayed state (R-AUTHZ-028).
- **Settings comparator.** The access settings this page journals are the authorization settings
  of a repository:

  | Setting | Defined in | Order, most restrictive first |
  |---|---|---|
  | `cacheFillAction` | R-AUTHZ-008 | `deploy`, `read` |
  | `packageOwnership` | R-AUTHZ-018 | `enforce`, `off` |
  | `requireAuthentication` | [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-004, R-AUTHZ-003 | `true`, `false` |
  | `hideExistence` | R-AUTHZ-017 | `true`, `false`; `inherit` compares as the value of `authz.hide_existence` in force at replay |

  A change of any of them writes `authz.settings.changed` in the transaction of the repository
  update, next to the repository page's own event. `narrows(current, after)` answers true only
  when, for every setting the entry names, the value of `after` comes no later in the order than
  the current value. Suspension sets every setting the entry names to its most restrictive value.
- **Not journaled.** The `authz.*` settings of this page (R-AUTHZ-017, R-AUTHZ-025, R-AUTHZ-028,
  R-AUTHZ-029) are configuration-file keys ([api-and-ui.md](api-and-ui.md) R-API-037), so a
  restore does not change them; a setting that later moves into PostgreSQL joins the table above
  with its order. `ownershipDomain` (P1, R-AUTHZ-020) has no order, because whether a domain
  change narrows depends on the ownership records of both domains; a restore reverts it without
  replay, which is part of the residual gap of R-HA-054.

Acceptance criteria (the restored database holds the policies of the worked example "Team: read
everything, write the own scope"; `team-web-publish` gives `group:team-web` `["annotate",
"deploy", "read"]` on `npm-internal` with `packages.include: ["@web/*"]`):

- A journaled `authz.policy.updated` whose after-state gives `group:team-web` only `["read"]` and
  leaves the `user:lead-web` entry unchanged is reported `applied`, and a publish by a member of
  `team-web` gets `E403` afterwards.
- A journaled update whose before-state holds `["@web/*"]` and whose after-state empties
  `packages.include` is reported `skipped`; the policy keeps `["@web/*"]`. A journaled update
  whose after-state replaces `@web/*` by `@web/ui-*` is reported `suspended`: the policy reads back
  with `"entries": []`, Alice's `npm publish` of `@web/button` gets `E403`, and her `npm view
  @web/button` still succeeds through `npm-consumers`.
- A journaled `authz.ownership.released` for the only owner of `@web/new-lib`, which holds no
  version in the restored database, is reported `applied`. A `team-web` member holding `deploy`
  but not `manage` then gets `E403` on `npm publish` of `@web/new-lib`, and `lead-web` succeeds.
- `npmjs` is restored with `cacheFillAction: read`. A journaled `authz.settings.changed` whose
  after-state sets `deploy` is reported `applied`; one on `npm-internal` whose before-state is
  `packageOwnership: enforce` and after-state `off` is reported `skipped`.
- A trove backup is taken. Then `admin` is unbound from group `platform-admins` and `deploy` is
  removed from the `group:team-web` entry of `team-web-publish`. `trove backup restore` of that
  backup into an empty installation reports both entries `applied`. After the replicas start, a
  member of `platform-admins` gets 403 on `POST /api/v1/repositories`, and a member of
  `team-web` gets `E403` on `npm publish`, on every replica.

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
  packages: storage growth, upstream rate limits, possibly upstream cost. The policy API flags such
  a grant with the warning `anonymous_populates_remote`. The per-repository fetch budget
  `limits.maxConcurrentFetches` of [repositories-and-proxy.md](repositories-and-proxy.md)
  R-REPO-040 is shared by the whole installation, so it bounds concurrent upstream fetches of one
  repository whatever the replica count; it bounds concurrency, not volume, and the per-replica
  `proxy.max_concurrent_fetches` grows with the number of `web` replicas and with autoscaling.
  Where remote repositories are exposed anonymously, set `cacheFillAction: deploy` and grant
  `deploy` to authenticated groups only (R-AUTHZ-008).
- **Removed grants: a bounded window on every replica.** A removed grant stays usable on a replica
  until that replica has applied the change (R-AUTHZ-028). The window is typically 100 ms plus one
  index build while Valkey is available. With Valkey unavailable or its messages lost or forged, it
  is at most `cluster.epoch_poll_interval` plus 1 s. It is never longer than
  `authz.cache.max_staleness` plus one build, about 10.5 s by default, even on a replica cut off
  from PostgreSQL, because such a replica stops deciding instead of deciding on old grants. A
  PostgreSQL failover that lost commits does not extend it: an epoch number that later writes
  reuse is recognised by its `changed_at` (R-AUTHZ-028).
  - Who is affected: a holder of a valid credential who keeps using a right that an administrator
    has just removed.
  - What closes the window for a credential: revoking it ends it on the timeline of
    [identity-and-authentication.md](identity-and-authentication.md) R-AUTH-023, independently of
    the grant index.
  - Writes are re-checked at commit on the committing replica's index (R-AUTHZ-015), so the window
    applies to them as well.
- **Valkey grants nothing.** No grant, index or decision is kept in Valkey (R-AUTHZ-028,
  [architecture.md](architecture.md) Q-ARCH-16). Whoever can write under trove's Valkey prefix can
  make replicas run extra epoch queries, at most 20 per second per replica however many messages
  they forge. They cannot widen a permission, and they cannot suppress an audit event, because no
  audit decision reads Valkey (R-AUTHZ-029). By dropping messages they can delay a change by at
  most `cluster.epoch_poll_interval` plus 1 s, because the change log in PostgreSQL still applies
  it (H-49).
- **`authenticated` reads, nothing more.** The built-in principal for every signed-in person
  holds only `read`, never covers service accounts or CI tokens, so a leaked automation token
  carries only what was granted to it by name, and only holders of `policies:write` can grant to it
  (R-AUTHZ-030). It cannot become a package owner, so a grant to everyone never makes everyone
  co-owner of a name.
- **Administrator power stays out of everyday tokens.** Tokens exercise roles only through
  `admin: true` or `system` items (R-AUTHZ-013). A leaked personal access token of an
  administrator therefore exposes that person's content rights, not the instance.

## Decisions

Each question below was decided by the development agent from the listed options; the owner reviews the choices at the walkthrough.

### Q-AUTHZ-1 · Endpoint gate on virtual repositories

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Explicit gate | Using a virtual repository needs `read` on it; content is still decided on the member that holds or receives it (R-AUTHZ-009) | Which aggregated endpoints a group uses is a visible, audited policy decision; membership changes never widen access; the gate is one repository-level check | One extra selector per consumer policy (`@any-virtual:npm`); a forgotten gate shows up as 404 on the virtual endpoint |
| (b) Implied gate | Anyone who can read at least one member may use the virtual repository; a virtual repository carries no grant of its own, as in the catalog (IAM-024) | No extra grant | An endpoint cannot be withheld from a group that reads one member; listings and setup instructions show virtual repositories to everyone who reads any member; virtual keys in policies would serve only `manage` |
| (c) Sufficient gate | `read` on the virtual repository reads every member through it | One grant per endpoint, simplest to administer | Adding a private repository as a member silently exposes it to every reader of the virtual repository; breaks the per-member merge isolation of rule 4 and the promise of R-AUTHZ-012 that membership changes expose nothing |

**Choice:** (a) — `read` on a virtual repository is an endpoint gate and never a source of content rights.

**Rationale:** (c) fails the security bias outright: a `repository-admin`, who holds no content rights, could expose any repository by adding it as a member. (a) and (b) both decide content on the member, so the difference is only whether the endpoint itself is grantable. (a) makes it so at the cost of one selector, which the worked example's `npm-consumers` policy already carries. It gives the anonymous rule (rule 6) and the listing rule (rule 7) a clear basis, and it is the version that [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-019 step 1 already implements, so no other page moves. Clients see only the virtual URL either way, and a missing gate answers 404 like a missing repository (R-AUTHZ-016). The development agent would revisit this if forgotten gates turn out to be a recurring configuration mistake. The remedy would then be a policy-API warning when a policy grants `read` on every member of a virtual repository but not on the repository itself, not a change of model.

**Applied in the spec:** R-AUTHZ-009 already followed (a). Its closing paragraph now states that policies may name virtual repositories by key, pattern and class, restricted to `read` and `manage`, which settles the catalog's open point in IAM-024. It no longer calls the alternative open. New rule 8 in R-AUTHZ-009 and a new bullet and acceptance criterion in R-AUTHZ-017 fix that content in unreadable members is always answered as missing through a virtual repository, whatever `hideExistence` says; the setting changes only the answer to a refused gate.

### Q-AUTHZ-2 · Ownership enforcement default for new npm and NuGet local repositories

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `enforce` by default | New npm and NuGet local repositories check ownership on every mutating operation (R-AUTHZ-018) | Matches the public registries: [npm](https://docs.npmjs.com/cli/v11/commands/npm-owner) lets only a package's owners publish new versions, and [nuget.org](https://learn.microsoft.com/en-us/nuget/nuget-org/publish-a-package#manage-package-owners-on-nugetorg) makes the first publisher the owner. Overlapping name patterns between teams and CI projects are refused instead of silently mixed, and every name is claimed from its first publish | A grant to a single user makes that user the personal owner; hand-overs need an explicit co-owner; packages imported with versions arrive unclaimed, so only `manage` holders publish them until owners are assigned (rule 3) |
| (b) `off` by default | Only CI grants with `bind_project` check ownership; repositories opt in | No ownership handling in small installations | A human or service account holding `deploy` on an overlapping pattern can publish versions of a package that a non-`bind_project` grant published first. Switching on later leaves every existing name unclaimed, so only `manage` holders can publish them (rule 3) |
| (c) Record-only by default | Owners are recorded on first publish and conflicts are audited, but nothing is refused until an administrator switches to `enforce` | Names are already claimed when enforcement starts; conflicts can be observed first | A third mode to build, test and explain; no protection until someone acts, and a conflict found in the audit log has already shipped |

**Choice:** (a) — new npm and NuGet local repositories enforce ownership unless the create request says `off`.

**Rationale:** Under (b) only `bind_project` grants are checked. A person or service account holding `deploy` on a pattern that overlaps a CI project's names therefore publishes the next version of that project's package unchecked, and the owner constraint "GitLab CI jobs write the packages the job produces" holds only against other CI jobs. (a) closes that gap and protects every name from its first publish on. (c) records the same owners but refuses nothing until someone switches, so it costs a third mode without buying protection. Users of npm and NuGet already know an owner-only publish from the public registries, and a refusal arrives as an ordinary `E403` or HTTP 403 in npm, pnpm, Yarn, dotnet and nuget.exe. The personal-owner cost of (a) is contained: the worked examples grant `deploy` to groups and CI mappings, and owners or `manage` holders add co-owners through R-AUTHZ-019. The import cost is one owner assignment per imported name (R-AUTHZ-019), made once by a `manage` holder. The development agent would revisit this if the walkthrough shows personal ownership from user-level grants getting in the way, or if imports make rule 3 a recurring blocker. The refinements would then be a policy-API warning on user-level `deploy` grants in enforcing repositories, and an import option that assigns owners as packages arrive.

**Applied in the spec:** R-AUTHZ-018: the default is normative ("MUST get `enforce` when the create request does not set the field"), and `off` is an explicit per-repository choice. A new bullet states that coordinates without a package name are not subject to ownership. A new acceptance criterion covers the default for npm and NuGet. The decision table at the top of the page states the default.

### Q-AUTHZ-3 · Built-in principal for all authenticated users

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) No built-in principal | Administrators use an IdP group that contains everyone, or an auto-join group (P1, R-AUTH-019) | Nothing new to build | In the MVP there are no auto-join groups, so the first developer cannot install anything until the IdP emits an all-employees group claim; the most common first policy depends on IdP configuration outside trove |
| (b) `authenticated`, any action | Built-in principal for users and service accounts that can hold any action | Also covers "everyone may publish to a sandbox" | Under ownership enforcement a write through it would make `authenticated` the owner (R-AUTHZ-018 rule 2), so everyone co-owns the name; one careless entry opens writes to the whole organization |
| (c) `authenticated`, `read` only, users and service accounts | As (d), but service-account tokens count as authenticated too | Automation outside GitLab reads without a grant of its own | Every service account gains the organization-wide read grants without anyone granting them, also one created for SCIM provisioning or monitoring, whose tokens default to `inherit: true` (R-AUTH-024) and may be non-expiring (R-AUTH-026); breaks the rule of R-AUTH-019 that keeps service accounts out of auto-join groups |
| (d) `authenticated`, `read` only, users only | Built-in principal for every user (UI session, personal access token), never for service accounts or CI tokens; entries may hold only `read`; no roles, no ownership, not grantable by delegated managers or project administrators (R-AUTHZ-030) | Covers the motivating case on day one; cannot widen writes or ownership; the rights of every non-human credential trace back to a grant that names it, its groups or its CI mapping | Writes for everyone, and reads for every service account, still need a group (deliberately) |
| (e) `authenticated` including CI tokens | As (c), but CI tokens count as authenticated too | One rule for every credential | A job of any project on a trusted CI issuer would gain the organization-wide grants; CI rights would no longer be fully described by mappings, the property R-AUTHZ-002 protects by refusing auto-join groups for CI |

**Choice:** (d) — a read-only built-in principal `authenticated` that covers every user and no service account or CI token, delivered in the MVP.

**Rationale:** The MVP has no auto-join groups, so without a built-in principal (option (a)) a deny-by-default installation is unusable until the IdP is reconfigured, and that would be the first thing the walkthrough trips over. Limiting the principal to `read` keeps it out of the ownership model and makes it impossible to open writes to everyone by accident, which rules out (b); "everyone may write here" stays possible through a group, deliberately. What remains is which credentials count. In the MVP the principal stands in for an auto-join "everyone" group, and R-AUTH-019 never applies such groups to service accounts or CI identities. (c) would break that rule. A service account is created for one purpose, its tokens default to `inherit: true` and are the credentials most often stored in other systems, so a SCIM provisioning or monitoring token would read every internal package without anyone granting it. (e) would do the same for every GitLab job, against R-AUTHZ-002 and R-CI-013. (d) costs automation that should read everything one group membership. The name `authenticated` stays, because the question asks for "all authenticated users", and a misreading fails closed: a service account that someone assumed covered gets 404 and visibly needs a grant, never more access than intended. Unlike `anonymous`, pattern and class selectors are allowed, because covering repositories created later is the point and every caller is known. Delegated managers and project administrators cannot add it, so organization-wide exposure stays with `policies:write`. The cost is one built-in principal row, one element in the principal set and one validation rule. This refines INV-9 the way `anonymous` already does: `authenticated` acts as an implicit group of every user. The development agent would revisit this if a real write-for-everyone case appears, which would need an explicit owner rule first, or if installations routinely put every service account into one group; an opt-in built-in principal for service accounts would then be weighed.

**Applied in the spec:** New requirement R-AUTHZ-030 (MVP), with an acceptance criterion that a service-account token without its own grant gets `E404`. R-AUTHZ-002: principal kind table and principal sets (UI sessions and personal access tokens include `authenticated`; service-account tokens and CI tokens never do) plus an updated acceptance criterion. R-AUTHZ-010: role bindings to `authenticated` answer 422 `role_not_allowed`. R-AUTHZ-021: new 422 code `authenticated_write_action`. R-AUTHZ-025 and R-AUTHZ-026: delegated managers and project administrators cannot grant to it. The model table, the decision table, the pseudo-code comment of R-AUTHZ-015 and the security notes are updated to match.

### Q-AUTHZ-4 · Default populate action of remote repositories

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Revisited on 2026-10-28 after the owner added the Valkey, horizontal-scale, backup and storage-class constraints: the choice stands. The rationale claimed that the concurrency limits bound the rate of anonymous fetches. The load now needs several `web` replicas whose count autoscaling changes, so the per-replica limit `proxy.max_concurrent_fetches` grows with the replica count; the per-repository budget `limits.maxConcurrentFetches` is shared by the installation (R-REPO-040, [repositories-and-proxy.md](repositories-and-proxy.md) Q-REPO-8) and keeps its bound whatever the replica count. Cross-replica request collapsing (INV-11) merges concurrent fetches of the same path only, not fetches of different packages. The rationale and the security note now say so; the risk still needs the same two explicit administrator decisions.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `read` | Every caller who may read a remote repository may also make it fetch uncached content and refresh expired metadata (R-AUTHZ-008) | Behaves like the pull-through proxy users expect; no per-user inconsistency; no `deploy` grants on repositories that never accept uploads | With anonymous read on a remote repository, anyone can make trove download arbitrary upstream packages |
| (b) `deploy`, as in the catalog (IAM-023) | Only `deploy` holders populate; everyone else gets what is cached | Frozen-mirror semantics everywhere; new third-party packages enter only through chosen principals | `npm install` of an uncached package answers `E404` through the virtual repository for one developer and works for a colleague who holds `deploy`, the most common support case of such proxies; `deploy` must be handed out on repositories nobody deploys to |
| (c) Split by caller | `read` populates for authenticated callers, while anonymous callers never populate unless the repository opts in | Removes the anonymous download risk by default | Brings back the inconsistent 404s for exactly the anonymous mirror case that anonymous read is usually granted for; a hidden third rule beside `read` and `deploy` |

**Choice:** (a) — `cacheFillAction` defaults to `read`, `deploy` stays available per repository as frozen-mirror mode, and anonymous populating is flagged when it is granted.

**Rationale:** The pull-through proxy is an owner constraint, and (a) is the only option under which it behaves the same for every reader. The risk that (c) addresses needs two explicit administrator decisions: turning on the runtime switch `anonymousAccess`, which is off by default, and a policy that names the remote repository by key for `anonymous` (R-AUTH-037, R-AUTHZ-003); an operator can also forbid anonymous access outright through the YAML ceiling. The policy API now names the consequence at the moment the grant is made (warning `anonymous_populates_remote`). The concurrency limits of [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-040 bound the rate of anonymous fetches but not their volume. The per-repository budget `limits.maxConcurrentFetches` holds for the whole installation (Q-REPO-8); the per-replica `proxy.max_concurrent_fetches` grows with the number of `web` replicas. The development agent would revisit this if operators report storage growth or upstream rate-limit incidents caused by anonymous populating, or if autoscaling makes the per-replica bound meaningless in practice. (c) would then become an opt-in per remote repository, not the default.

**Applied in the spec:** R-AUTHZ-008: the reasoning paragraph is marked as decided, and the default is stated for every new remote repository. New bullet "Anonymous populating is flagged" with the warning `anonymous_populates_remote`, plus an acceptance criterion. R-AUTHZ-021 lists the new warning. The security note on anonymous populating mentions it. After the revisit: the rationale and the security note on anonymous populating state which concurrency bound is per replica and which is shared by the installation.

### Q-AUTHZ-5 · Default audit level for reads

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Revisited on 2026-10-28 after the owner added the Valkey, horizontal-scale, backup and storage-class constraints: the choice stands, and its grounds changed. The rationale relied on PostgreSQL being the only coordination store. Under INV-3, Valkey now holds shared ephemeral state, so the deduplication store that option (d) lacked exists. The options table and the rationale are updated. With the load spread over several `web` replicas, option (c) writes one audit row per download of the whole installation, which strengthens the case for (b). The aggregation of refused anonymous requests now has a bound that holds across n replicas without Valkey (R-AUTHZ-029).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `none` | No read events; refused reads appear only in `trove_authz_decisions_total` | Least audit volume | Probing (many refused reads of guessed names) leaves no attributable trace |
| (b) `denied` | Refused reads are audited, allowed reads are not | Refusals are rare and signal probing or misconfiguration; volume follows refusals, not traffic | An authorized reader's downloads are not attributable (H-23) |
| (c) `all` | Every read is audited | Every download attributable | One `npm ci` of a typical project issues hundreds of requests; audit rows in PostgreSQL (INV-16) then grow with the download rate and make the audit table the largest write load of an HA installation |
| (d) `denied` plus first-read sampling | Refused reads, plus one event per principal, package version and day, deduplicated with a `SET … NX` key in Valkey that expires after the day | Attribution of who fetched what, at bounded volume; a lost Valkey key only writes a duplicate event, never drops one | One Valkey round trip on every allowed read, the hottest path of the system. Whoever can write to Valkey can set a key in advance and so suppress the event, which makes the audit record depend on Valkey, against [operations-and-observability.md](operations-and-observability.md) R-OPS-015. While Valkey is unavailable, deduplication must fall back to per-replica memory, which multiplies events by the replica count, or to a PostgreSQL upsert per download, which brings back one write per read. Attribution of downloads is not an owner requirement |

**Choice:** (b) — refused reads are audited by default, and allowed reads only with `authz.audit.reads: all`.

**Rationale:** (b) keeps the security signal of refused reads while the audit store stays sized by refusals. That matters because PostgreSQL is the audit store and the system of record (INV-3, INV-16), every replica writes to it, and the load is spread over several `web` replicas by design (INV-12). (c) is one setting away for installations that must attribute every download, and H-23 says so. (d) is the attractive middle ground. With Valkey part of every installation, its deduplication no longer needs a database write per read, but it still puts a network round trip on every allowed read, and a key planted in Valkey would suppress an audit event, which R-OPS-015 rules out for every audit record. While Valkey is down, it degrades into either an event flood or that database write. Nothing the owner asked for needs it. The development agent would revisit this if a compliance requirement for download attribution appears. (d) would then deduplicate in each replica's memory, as R-OPS-015 does for denials, and accept up to one event per `web` replica, principal, version and day.

**Applied in the spec:** No change to the setting; R-AUTHZ-029 already defaulted to `denied`. A new bullet there states the decision and points to H-23. After the revisit: option (d) and the rationale reflect Valkey. R-AUTHZ-029 aggregates anonymous 401 events with the per-replica deduplication of [operations-and-observability.md](operations-and-observability.md) R-OPS-015, keyed by client address, so the bound grows with the replica count only, never with the scan rate. A Valkey limiter was considered for this and rejected: R-OPS-015 keeps audit state out of Valkey, and a counter in Valkey would let whoever can write to Valkey suppress audit events. R-AUTHZ-029 also states that allowed populates are audited once per upstream fetch, not once per collapsed requester, and what a drain and a crash do to buffered events.

### Q-AUTHZ-6 · Default `ownership` of CI inline grants that write

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `none` by default | A writing inline grant claims names for the mapping (R-AUTHZ-018 rule 2), so every project the mapping matches co-owns them | Sharing across projects needs no configuration | A group-wide mapping (`acme/**`) silently makes all its projects co-owners of everything any of them publishes, which defeats per-project ownership without anyone noticing |
| (b) `bind_project` by default for grants with `deploy`, `overwrite` or `delete` | The first publishing project owns the name; `"ownership": "none"` shares on purpose | Per-project ownership, the owner constraint's "packages the job produces", holds without extra configuration; sharing remains one explicit field away | A monorepo split across several GitLab projects must say `none` |
| (c) No default | A writing inline grant must state `ownership`; saving without it answers 422 | Nobody relies on a default they did not see | Friction on every mapping although the safe value is obvious; examples and configuration as code grow a mandatory field |

**Choice:** (b) — a writing inline grant that omits `ownership` is saved as `bind_project`, and any other inline grant as `none`.

**Rationale:** (b) makes the secure outcome the default and keeps it visible: the default is applied and stored at save time, so mapping reads, issuance records and explanations all show the effective value, and nothing is inferred at evaluation time. (a) fails silently in exactly the setup the CI examples use, a mapping that matches a whole GitLab group. (c) buys no safety over (b), because the default already is the safe value. Sharing stays visible too: an explicit `none` on a writing grant is saved with the warning `shared_ownership` ([gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-010), so the administrator sees the co-ownership at the moment of choosing it. Grants without a package name (paths only, formats without names) store the value without effect, because ownership needs a name (R-AUTHZ-018). This depends on claim-on-first-publish staying the CI acquisition rule (Q-CI-6 in [gitlab-ci-oidc.md](gitlab-ci-oidc.md)). The development agent would revisit this if many installations publish one package family from several GitLab projects; a mapping-level default could then be added.

**Applied in the spec:** R-AUTHZ-014: new bullet "Ownership default (Q-AUTHZ-6)", which also names the warning `shared_ownership` for an explicit `none`, and an acceptance criterion. R-AUTHZ-018: the "when ownership is checked" list refers to the default. The decision table at the top of the page states it. R-CI-013 on [gitlab-ci-oidc.md](gitlab-ci-oidc.md) already specifies the same default and the warning, so the two pages agree.

### Q-AUTHZ-7 · CI delegations and P1 projects

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Independent in P1 | CI delegations (R-CI-023) and projects (R-AUTHZ-026) stay separate; an administrator may name a project's administrator group as the delegate of a CI delegation | No change to either page; covers team onboarding; each feature ships and is tested on its own | The delegation repeats the repository list that the project already has |
| (b) Delegation references a project | The allowed repositories of a CI delegation follow the project's repositories automatically | No duplicate repository list | Couples two P1 features; assigning a repository to a project silently widens what the delegate may grant to CI jobs, without the administrator who set the delegation's bounds acting |
| (c) Project administrators are CI delegates | GitLab project or namespace IDs are recorded on the project, and its administrators manage CI mappings for its repositories | One delegation unit | Lets project administrators decide which GitLab projects act for them, a trust decision about CI identity that belongs to system administration; the largest change to R-CI-023 |

**Choice:** (a) — CI delegations stay independent of projects in P1.

**Rationale:** Both features are P1 and do not affect the MVP prototype. (a) keeps each bound explicit: what a CI delegate may grant is exactly what an administrator wrote into the delegation, and nothing widens it implicitly. The duplicate repository list is small and visible. The development agent would revisit this when projects gain roles in P2. A delegation could then reference a project, but its repository bound would be copied from the project when an administrator saves the delegation, not followed live, so the bound still changes only through an administrator's action.

**Applied in the spec:** R-AUTHZ-026 states that CI mapping delegation is independent of projects in P1. No change to [gitlab-ci-oidc.md](gitlab-ci-oidc.md) is needed.

### Q-AUTHZ-8 · Spec page for build metadata

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) New page `builds.md` (`R-BLD`) | Build records, their API, retention and promotion get their own page before P1 work on builds starts | One page per model; build records have their own resource type in policies and scopes (R-AUTHZ-027, R-AUTHZ-013), their own API and their own lifecycle | One more page to keep consistent |
| (b) Fold into [repositories-and-proxy.md](repositories-and-proxy.md) | Build records become a section of the repository page | No new page | Build records are selected by build name, not by repository; that page would own two unrelated models |
| (c) Fold into [storage.md](storage.md) | Build records as metadata rows plus blobs | Close to where their bytes live | Mixes an API and a permission model into the storage mechanism page |
| (d) Defer build records to P2 | No page now; build records, R-AUTHZ-027 and the retention rules that act on them (RET-023, RET-037) move to P2, as [storage.md](storage.md) Q-STOR-3 (c) proposes | Smaller P1 | `resourceType: build` and the `builds` scope part are already reserved (R-AUTHZ-021, R-AUTHZ-013); P1 retention rules lose the records they act on; it postpones the question of which page owns the model instead of answering it |

**Choice:** (a) — a dedicated page `builds.md` with prefix `R-BLD`, written before P1 work on builds starts.

**Rationale:** Build records have their own API, retention, promotion and permission resource type. Only (a) gives them one owner, and storage.md (Q-STOR-3) and api-and-ui.md (Q-API-5) ask the same question, so all three can converge on the same answer. (d) changes when builds are built, not who owns them, and the P1 retention rules depend on them. Builds stay P1, so the MVP prototype is unaffected either way. The development agent would revisit this if build information is cut down to properties on ordinary artifacts, in which case [storage.md](storage.md) would own it.

**Applied in the spec:** R-AUTHZ-027 names `builds.md` (`R-BLD`) as the owner and refers to the `builds` part of token scopes. Creating the page and its row in the page table of [README.md](README.md) is outside this page.

### Q-AUTHZ-9 · Shape of the repository part of a token scope

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The policy resource set | Each scope element is `repositories`, `packages` (`include`, `exclude`), `paths` (`include`, `exclude`), `actions` | One matcher and one validator serve policies, scopes and CI inline grants (R-AUTHZ-005, R-CI-013); the npm package-bound CI token of [formats/npm.md](formats/npm.md) already speaks of a package include pattern | Slightly more verbose JSON |
| (b) One `include`/`exclude` pair interpreted by format | The pair means package names in formats that have them and paths otherwise | Shorter | A scope that spans several formats (`@any-local`) cannot say which meaning applies; the same string means a name in one repository and a path in another |
| (c) Paths only, in the catalog's string grammar | `artifact:<repo>/<path>:<actions>` style elements | Familiar from the catalog (TOK-012) | Reintroduces the trap where a path pattern admits a tarball but not the separately stored metadata (PKG-NPM-047); a second grammar beside JSON |

**Choice:** (a) — a scope element is a policy resource set plus `actions`.

**Rationale:** (a) is the only option in which a pattern means the same thing wherever it appears. That matters most for scopes, which are often written by users and pasted into automation. It needs no code beyond what policies need. (b) is ambiguous by construction, and (c) brings back a trap the name patterns were introduced to close. Nothing is implemented yet, so there is no older shape to stay compatible with. The development agent would revisit this only if a client-facing scope string (for example a CLI flag) needs a compact form; that would be a serialization of (a), not a second model.

**Applied in the spec:** R-AUTHZ-013: the repository part is specified with a JSON example. Top-level `include` and `exclude` in a scope element are rejected with 400 `invalid_scope`. A new acceptance criterion covers both. [identity-and-authentication.md](identity-and-authentication.md) R-AUTH-024 still shows the single pair and needs the matching edit on that page.

### Q-AUTHZ-10 · Resource types a token scope can name

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Repository content and system items only | A scope has `repositories` and `system`; nothing else | Smallest structure | Build actions (P1) cannot be narrowed: a token either admits them all through `inherit` or none |
| (b) A closed set of typed parts that mirrors the grant model | `repositories` (MVP), `system` (P1), `builds` (P1); projects only once project roles exist (P2); repository configuration stays a system capability | Every resource type that can be granted can also be narrowed; validated by a fixed JSON schema; matches `resourceType` of policies | One more part per future resource type |
| (c) Open typed grammar as in the catalog | `<type>:<target>[/<sub-resource>]:<actions>` with types such as artifact, project and per-repository configuration | Could express "configure `npm-*` only" | A scope is a ceiling (R-AUTHZ-013), and no grant model stands behind per-repository configuration types (system roles are instance-wide, R-AUTHZ-010), so such items would either yield nothing or have to grant on their own, which trove forbids; a second grammar beside JSON |

**Choice:** (b) — a scope names exactly the resource types the grant model has, and it is always intersected with the subject's rights.

**Rationale:** The catalog leaves open which resource types a scope can name (repository content only, or also projects and per-repository configuration types) and whether a resource scope is intersected with the subject's own rights (TOK-012). Because a scope only narrows, a scope type is useful only where a grant type exists behind it. (b) follows that rule exactly and leaves no item that silently yields nothing. Per-team configuration rights come from projects (R-AUTHZ-026), not from tokens. Intersection with the subject's rights is not optional in trove: every right must trace back to a policy, a role or a CI mapping. The development agent would revisit this when project roles arrive in P2 and add a `projects` part for TOK-011 and TOK-013.

**Applied in the spec:** R-AUTHZ-013: new "Resource types" table (`repositories` MVP, `system` P1, `builds` P1), `inherit: true` covers build rights from P1, and a restricted scope without `builds` admits no build action. The "Differences from the catalog" list states that a scope is always intersected with the subject's rights, and that trove has no group-scoped token and so no group-list syntax (settling the open quoting and alias points of TOK-010). R-AUTHZ-027 refers to the `builds` part. R-AUTH-024 on [identity-and-authentication.md](identity-and-authentication.md) needs the P1 `builds` field.

### Q-AUTHZ-11 · Invalidation path of the grant index across n replicas

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Until 2026-10-28, R-AUTHZ-028 kept the index current through its own `authz_epoch` row, `NOTIFY` in
the writing transaction, a `LISTEN` connection per replica and a 5 s poll. The new constraints
change the frame:
- [architecture.md](architecture.md) Q-ARCH-15 removes `LISTEN/NOTIFY` everywhere, and Q-ARCH-14
  keeps `web` replicas free of session connections, so that PostgreSQL connections do not grow with
  the replica count (INV-12).
- INV-3 forbids that correctness depends on Valkey, and Q-ARCH-16 forbids reading a positive
  authorization result from Valkey.
- The load needs n replicas, which all hold the index.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Keep the own epoch with `LISTEN/NOTIFY` | As specified before | Delivery on commit | One session connection per `web` replica, which a transaction-mode pooler cannot carry and which grows with the replica count; contradicts Q-ARCH-15 |
| (b) Own epoch row and own poll, Valkey as fast path | `authz_epoch` stays; a second poll loop reads it | Correct from PostgreSQL alone | A second poll, a second ordering rule and a second readiness signal next to R-ARCH-018, for the same guarantee; writers serialize on the row for their whole transaction unless it copies R-ARCH-018's last-statement rule |
| (c) The `authz` change area of R-ARCH-018 | The authorization epoch is the `authz` area's epoch. The index depends on `authz` and `repo_config` and is built from one `REPEATABLE READ` snapshot that carries its epochs. A Valkey message starts a build only after an epoch query confirms it | One poll and one readiness signal for every cache. The area's row lock orders the epochs. No session connection. A lost message costs latency; a forged one costs one indexed query. Repository code needs no knowledge of authorization | Every change makes every replica build the whole index. `Trove-Authz-Epoch` does not cover repository changes, because they live in the second area |
| (d) Share the compiled index or decisions in Valkey | One build per change instead of n | Fewer builds and database reads | Whoever can write to Valkey could grant access (Q-ARCH-16). A loss of Valkey's data would cost correctness or force every replica to refuse, against INV-3 and INV-12 |
| (e) No index: query PostgreSQL per decision | Policies are read for every coordinate | Exact, no propagation window | Queries per request and per virtual member, at a load beyond one replica, on the system of record. The 1 ms target of R-AUTHZ-015 fails, and a short database hiccup fails every decision |

**Choice:** (c) — the grant index follows the `authz` and `repo_config` change areas, is built from one snapshot labelled with its epochs, and treats a Valkey message as a hint that an epoch query must confirm.

**Rationale:**
- (c) reuses the mechanism that R-ARCH-018 already establishes for tokens and the repository
  snapshot: one change log, one poll per replica and one `invalidation` readiness check. (a) breaks
  the connection model the owner's load requires. (b) builds the same guarantee a second time.
- The snapshot build makes the index a self-validating entry in R-ARCH-018's sense. It carries the
  epochs it was built from, so the rule that a fill never re-inserts an invalidated value holds by
  construction: an index with lower epochs is never swapped in. A build that read its tables at
  `READ COMMITTED` could combine a policy's new entries with its old selectors and grant more than
  either state.
- The label holds each area's `changed_at` next to its epoch. A PostgreSQL failover that lost
  commits can be followed by writes that reach the same epoch number again before a replica polls,
  and an epoch number alone would then confirm an index built from the lost history. Comparing
  `changed_at` costs nothing extra, because the same row is read.
- The build and the epoch reads run on the request pool, not on the control connection. A slow
  build can then never delay the poll and the heartbeats that readiness and job ownership depend
  on, which keeps INV-12's rule that load never takes replicas out of the Service.
- An epoch query before a message-triggered build keeps Valkey outside the trust path and caps
  what a forged message costs at one indexed query. That is the property H-49 assumes.
- Depending on two areas costs less than making every repository write also notify `authz`. The
  repository page needs no knowledge of authorization, and every build reads the repositories in
  the same snapshot anyway. The price is that R-AUTHZ-032's header covers permission-side changes
  only.
- The build cost is the visible trade-off. With 100 replicas and a 500 ms build, one change costs
  about 50 CPU seconds across the cluster and 100 snapshot reads, which is small at administrative
  change rates. (d) and (e) would remove it at the price of trust or of database load on every
  request.
- The development agent would revisit this if the summed `trove_authz_index_rebuild_seconds` of
  all replicas exceeds about 1 % of the `web` role's CPU, or if automation changes `repo_config`
  per pipeline run. The index would then apply incremental updates from the change payloads, and
  the full build would remain the fallback after a flush.

**Applied in the spec:**
- R-AUTHZ-028 is rewritten:
  - the area, the consistent snapshot build with its label of epochs and `changed_at`, the request
    pool as its connection, and the rebuild triggers including a reused epoch;
  - the epoch query before a message-triggered build, the coalescing and the monotonic swap;
  - the poll and the rebuilds running on through a drain;
  - the `safety_ttl` rebuild, the rule that Valkey holds nothing of authorization, and the
    completeness test;
  - the data model without `authz_epoch`, the new configuration and new acceptance criteria.
- R-AUTHZ-015: the index is read from memory, never from Valkey. R-AUTHZ-021: writes notify the
  area. R-AUTHZ-031: replay notifies the area and publishes nothing. The security notes and the
  decision table.
- Cross-page:
  - [architecture.md](architecture.md): the `authz` row of R-ARCH-018's area table; the isolation
    exception in R-ARCH-019; Q-ARCH-15's rationale; R-ARCH-018's poll recognising a reused epoch
    for every area, not only `authz`; R-ARCH-024 keeping the poll running until shutdown step 6.
  - [backup-and-restore.md](backup-and-restore.md) R-BAK-021 and R-BAK-022: `authz_epoch` goes.
  - [repositories-and-proxy.md](repositories-and-proxy.md): its writes notify `repo_config`.
  - ADR-057 in [docs/decisions.md](../decisions.md).

### Q-AUTHZ-12 · Seeing one's own permission change under random request distribution

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-AUTHZ-021 returned the new epoch in `Trove-Authz-Epoch` "so a client can wait for propagation".
The client was meant to watch a replica's epoch. Under INV-12 the Service sends each request to a
random replica without session affinity, so the replica that reports an epoch is not the one that
answers the next request (INV-18). A script that grants a right and uses it at once, the UI after a
permission change and trove's own e2e tests in the `scatter` profile
([architecture.md](architecture.md) R-ARCH-032) would see refusals for up to the propagation bound.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The bound only | Clients wait for the bound of R-AUTHZ-028 or retry | Nothing to build | Clients must sleep for the worst case (2 s without Valkey) or retry blindly. The promise of `Trove-Authz-Epoch` becomes empty |
| (b) Request header `Trove-Authz-Min-Epoch` | A request names the epoch it needs; any replica waits for an index at least that new, or answers `412` when the database never reached it | Opt-in and deterministic. Covers added and removed grants and the member checks of virtual resolution. Costs nothing for requests without it, and its database reads are coalesced | Only clients that send it benefit. It covers the `authz` area, not repository creation |
| (c) Re-check every refusal against the database epoch | A replica confirms a refusal by reading the current epoch, and rebuilds when it is behind | Transparent, package clients included | Covers only added grants that would end in a refusal; not removals, and not the member checks of virtual resolution, which are not refusals. Every refusal gets a database read, and scanners produce refusals at will. Strict read-your-writes needs each refusal to wait for a read that starts after it arrived |
| (d) Decide writes and management operations on PostgreSQL | The index serves reads only | Writes exact | Two evaluators that must agree; several queries per write; reads remain unsynchronized |
| (e) Session affinity for management clients | Route a client to the replica it wrote through | — | Forbidden by INV-12 and R-ARCH-032. A token used over several connections gains nothing |

**Choice:** (b) — a request may carry `Trove-Authz-Min-Epoch`, and every replica honours it.

**Rationale:**
- Under random distribution, the epoch has to travel with the request. (b) is the smallest
  mechanism that does that. It keeps the existing response header meaningful and gives the UI and
  automation exact behavior for added and removed grants alike.
- (c) is attractive because it is transparent. It covers the wrong half, though: a removal that
  has not arrived yet produces an allow, which no refusal re-check sees. And it turns refusals into
  database reads.
- Honouring the header only for callers with a credential keeps anonymous callers from probing the
  epoch. Answering `412` at once for an epoch the database never reached, and reading at most every
  50 ms per replica, keeps forged values cheap.
- Package clients do not need it in practice. A person publishes seconds after a grant change, and
  the bound is 1 s while Valkey is available.
- The development agent would revisit this in two cases. If automation reports refusals right
  after its own grants without using the header, (c) would be added for writes only. If repository
  creation followed by immediate use through a pattern grant becomes common, the `repo_config`
  epoch would join the same header.

**Applied in the spec:**
- New R-AUTHZ-032 (MVP).
- R-AUTHZ-021: the epoch header and an acceptance criterion with the request header.
- R-AUTHZ-016: the `412` row. R-AUTHZ-015: the acceptance criterion of the trace.
- R-AUTHZ-032: the header is read only after authentication succeeded; a client drops an epoch
  that answered `412`; a `412` is a hint, because a reused epoch can hide a lost change.
- The decision table.
- Cross-page: [api-and-ui.md](api-and-ui.md) (the two headers, the codes `invalid_header`,
  `authz_epoch_unknown` and `authz_stale`, the UI sending the header after its own permission
  changes and dropping an epoch that answered `412`);
  [operations-and-observability.md](operations-and-observability.md) (the metric
  `trove_authz_min_epoch_total`).

### Q-AUTHZ-13 · A replica whose grant index cannot be confirmed current

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Until 2026-10-28 a replica that had not confirmed the current epoch for 30 s answered `503` to every
request that needed authorization and reported not ready. The revised frame sets four rules:
- Readiness depends on PostgreSQL only, through the `database` and `invalidation` checks
  ([architecture.md](architecture.md) R-ARCH-018).
- No check may undercut the PostgreSQL readiness window of about 15 s
  ([ha-and-helm.md](ha-and-helm.md) R-HA-008). That page names authorization as a path that fails
  closed sooner by reading the change-log freshness itself.
- Requests ride out a PostgreSQL failover in their retry loop for up to `database.retry.max_elapsed`
  (30 s, R-HA-009).
- Valkey never makes a replica unready (INV-12).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) As before | 30 s; `503` at once; an own readiness gate | Known | The own gate duplicates `invalidation`. A removed grant can stay usable for 30 s on a replica cut off from the change log |
| (b) 10 s, `503` at once, no own gate | The same refusal with a shorter bound | A 10 s bound; simple | During a failover, new requests get `503` at once from 10 s on, while requests already inside their database retry loop ride it out. Clients with tightly spaced retries, such as NuGet's six tries one second apart, fail |
| (c) 10 s, wait like a database wait, no own gate | A request that finds the index staler than the bound waits for a confirmation within its deadline and `database.retry.max_elapsed`, then is decided or answered `503` | The security bound of (b). During a failover, requests behave as they do towards the database, which every request needs right after authorization anyway | Requests on a stale replica hold memory and a client connection, but no database connection, for up to the retry budget. A build failure on one replica makes its requests wait before they fail |
| (d) No bound of its own | Rely on readiness | Nothing to build | A replica leaving the Service still decides the requests already routed to it, and those an edge sends during endpoint propagation, on grants of any age. When every replica is stale, nothing stops decisions |

**Choice:** (c) — no decision is made on an index unconfirmed for longer than `authz.cache.max_staleness` (10 s); a request that meets such an index waits as it would for the database, then is decided or answered `503` `authz_stale`; authorization adds no readiness check.

**Rationale:**
- The security property is that no decision rests on an index older than the bound. (b) and (c)
  share it. (c) adds that a failover looks the same to a client whether the index or the next query
  notices it first, which matters with a load that keeps many requests in flight during every
  failover.
- 10 s is ten poll intervals, so a slow poll never stops decisions. It lies below the readiness
  window, as R-HA-008 expects of authorization. It cuts the worst window for a removed grant from
  about 30 s to about 10.5 s.
- An own readiness check would add nothing for PostgreSQL outages, which `database` and
  `invalidation` already cover, and at 10 s it would undercut R-HA-008's window. Valkey affects
  neither the bound nor readiness.
- Readiness does not see one case: a build that fails on one replica while its poll works. It shows
  in `trove_authz_index_staleness_seconds` and in `authz_stale` answers. A build that fails
  deterministically stalls every rollout, because start-up requires a successful build
  (R-AUTHZ-028).
- The development agent would revisit this if such replica-local build failures occur in practice.
  A gating check that fires only while the poll is fresh would then be added; it cannot undercut
  the PostgreSQL window.

**Applied in the spec:**
- R-AUTHZ-028: the bullets on confirmation and staleness, the bound and the stale index;
  `authz.cache.max_staleness` 10 s with a start-up floor; `poll_interval` removed; acceptance
  criteria for a blocked poll and a PostgreSQL outage.
- R-AUTHZ-015: `index.ForRequest` in the pseudo-code, one index per request. R-AUTHZ-016: `503` `authz_stale` with
  `Retry-After`. The security note on removed grants and the decision table.
- Cross-page: [ha-and-helm.md](ha-and-helm.md) R-HA-007 (the row "PostgreSQL unavailable beyond
  the retry budget"); ADR-057 in [docs/decisions.md](../decisions.md).

### Q-AUTHZ-14 · Policy tables in M1 without the project column

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

The sketch in R-AUTHZ-028 gives `authz_policy` a column `project_id` that references `project`, a P1
table (R-AUTHZ-026). M1 has no projects.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Create `project` and `project_admin` in M1 | The sketch as written | One migration | Two tables without a requirement or an API in M1, and a backup class and tests for them |
| (b) Leave the column out until the projects arrive (chosen) | `authz_policy` has no `project_id` in M1; the project work adds the column with `ADD COLUMN ... REFERENCES project (id)` in its own expand step | M1 holds only what it uses | One more migration in the P1 work |

**Choice:** (b). `resource_type` allows `'repository'` and `'build'`, and `authz_policy_pattern.dimension`
allows `'package'`, `'path'` and `'build'` from the start, because an expand step cannot widen a `CHECK`
(it would need `DROP CONSTRAINT`). `authz_policy_pattern.pattern` is limited to 2000 octets next to the
1024 characters of the sketch, so a pattern of multi-byte characters fails with a `CHECK` violation
instead of exceeding the size limit of a btree entry. `authz_policy_selector` rejects a format filter on a
key selector, as R-AUTHZ-004 allows the filter on patterns and classes only.

**Applied in the spec:** R-AUTHZ-028 data model (a note under the sketch).

### Q-AUTHZ-15 · Where the grant index reads the repositories from

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-AUTHZ-028 builds the index from policies, role bindings and repositories in one `REPEATABLE READ` snapshot
that carries the epochs of `authz` and `repo_config`. The repository area also offers `repository.Reader`, which
answers from its own in-memory snapshot of `repo_config`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `repository.Reader` | The build lists the repositories through the contract interface | No SQL on the repository table in authz; one decoding of the document | The reader's snapshot is a different moment than the build's snapshot, so an index could combine a policy with selectors expanded over another repository list; the label would not describe the content |
| (b) Rows in the build snapshot | The build selects `id, key, class, format, config` of active repositories in its own transaction and reads the five settings it needs (`requireAuthentication`, `hideExistence`, `virtual.members`, `virtual.deployTarget`, `remote.cacheFillAction`) from `config` | The index is the state of one moment, as the acceptance criteria require ("grants exactly what a database snapshot at its epochs grants"); no dependency on another replica-local cache being current | authz reads five members of the repository document and must follow it if their place moves |

**Choice:** (b). Validation of a policy (key selectors, `unknown_repository`, the listing behind `selector_matches_nothing`) uses `repository.Reader`, because a write is validated against the current repository list and checks nothing that a snapshot must agree with.

**Rationale:** The consistency of the build is a stated property of R-AUTHZ-028; a reader that is current to a different epoch breaks it. The five settings are decoded by one function (`parseRepoConfig` in `internal/authz/index.go`), and an unreadable document is treated as `requireAuthentication: true`, so a malformed row only removes rights. The development agent would revisit this if the repository area publishes a typed snapshot with an epoch, which the build could then take instead of the rows.

**Applied in the spec:** none (the requirement already says the repositories are read in the build's transaction). Developer page: [authorization.md](../developer/authorization.md).

### Q-AUTHZ-16 · The dev-profile bridge `system.bootstrap_anonymous_read` in the new engine

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

M0 ships `system.bootstrap_anonymous_read` (default `false`, refused with an `https` base URL) as the explicit switch that lets the dev profile and its load test read the replica registry before any identity exists. The permission it opens was `system.read`, now the capability `settings:read`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) An anonymous grant in the index | The engine treats the switch as a compiled grant of `settings:read` to `anonymous` | One decision point for everything | The engine would need the endpoint of the request in `Require`, which the domain package does not see, and a grant that appears in no policy and no audit trail |
| (b) A check in front of the engine | The composition root's `Access` answers `settings:read` for an anonymous caller on the default endpoint while that endpoint admits anonymous requests (R-EP-010), and asks the engine for everything else | The M0 semantics are kept as they were, including the endpoint condition; nothing in the engine or the index depends on a development switch; removing the bridge is deleting one type | A second place that can answer "allowed" |
| (c) Remove the switch | Identity makes it unnecessary | No bridge | The dev profile and the E0.13 load read the registry anonymously today; removing it belongs to the identity merge |

**Choice:** (b), in `internal/app/access.go` (`bridgeAccess`). It opens exactly `settings:read`, for anonymous callers, on the default endpoint, only while `system.bootstrap_anonymous_read` is true; it never opens a repository action, and every other permission and every authenticated caller go to the engine. The configuration check that refuses the switch with an `https` base URL stays.

**Rationale:** The bridge exists to make a development profile work and must not become a path inside the production engine. The condition that matters (the endpoint) is edge knowledge. The development agent would revisit this when the dev profile can sign in, at which point the switch and `bridgeAccess` are removed together.

**Applied in the spec:** none. The gap stays [H-63](../security/roles-and-trust-boundaries.md#h-63--anonymous-read-of-installation-facts-and-the-replica-list-until-identity-ships).

### Q-AUTHZ-17 · How `Trove-Authz-Min-Epoch` reaches the engine

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

`Subject.MinEpoch` exists in the contract, but nothing built it: `v1.SubjectFrom` converts a `httpserver.Principal`, which had no such field, and the files and format handlers call `SubjectFrom(PrincipalFrom(ctx))` without the request.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A field of `httpserver.Principal` | The composition root's authentication wrapper reads the header after authentication succeeded and stores the parsed value; `SubjectFrom` copies it | Every caller of `SubjectFrom` gets the header for free; "authenticated first, header second" is the order of the wrapper; an anonymous principal never carries it | A contract type gains a field |
| (b) A context value | A middleware stores the raw header | No contract change | A second implicit input, parsed in a place the chain does not own |
| (c) Each handler reads the header | | Local | Every handler must remember; the 400 for a malformed value is repeated |

**Choice:** (a). `app.withMinEpoch` wraps the authentication stage: a failed authentication answers its 401 first, an anonymous principal is returned untouched, and an authenticated one gets `MinEpoch` from `authz.ParseMinEpoch` (a malformed value answers 400 `invalid_header`).

**Rationale:** R-AUTHZ-032 puts the header behind authentication; the wrapper is the smallest place that is. The development agent would revisit this if more per-request authorization hints appear, which would then travel together in one struct.

**Applied in the spec:** none. Contract change recorded in [m1-contracts.md](../developer/m1-contracts.md).

### Q-AUTHZ-18 · Event names of the decision audit

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-AUTHZ-029 names one event, `authz.decision`, with three deliveries: transactional for an allowed write, asynchronous for a refusal and for reads. The event registry fixes the delivery per event type (R-OPS-014), and the contract of the milestone already names `authz.denied`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One type | `authz.decision` with a delivery chosen per record | One name, as in the requirement | The registry cannot express it; one type would have to be transactional or asynchronous for all |
| (b) One type per delivery | `authz.denied` (asynchronous: refused reads, writes and management operations), `authz.read.allowed` (asynchronous, only with `authz.audit.reads: all`), `authz.write.allowed` (transactional, recorded by the writer in the transaction of the change) | Fits the registry; the names say what happened; `authz.denied` is the contract's name | Three names for one fact in the requirement text |

**Choice:** (b). The fields of R-AUTHZ-029 are carried as `details` (reason, status, actions, repository, class, format, package, path, credential kind, endpoint, replica, epoch, sources); the actor is the event's actor. `authz.policy.created` is transactional; `authz.policy.updated`, `authz.policy.deleted` and `authz.role.unbound` are transactional and flagged `journal: revocation` with the entry required, as are `authz.settings.changed` (written by the repository update, R-AUTHZ-031) and the entries of R-AUTHZ-031; `authz.role.bound` is transactional without the flag. The ownership events wait for package ownership.

**Rationale:** the registry is the contract with audit export and webhooks (R-OPS-014). The development agent would revisit this if subscribers want one decision stream, which a view over the three types gives without a rename.

**Applied in the spec:** none; the names are in [events_authz.go](../../internal/audit/events_authz.go).

### Q-AUTHZ-19 · Where the denial deduplication lives

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-OPS-015 deduplicates `authz.denied` per replica, principal, action, repository and path within `audit.denial_dedup_window`, and writes a summary with the exact count when the window closes.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) In the recorder | The audit area collapses events of a type by a key it derives | One mechanism for `token.rejected` as well | The recorder would parse authz details to build a key |
| (b) In the producer | `internal/authz` keeps the open keys in memory, writes the first occurrence at once and one summary per window with `details.count`, and writes the open summaries when it stops | The key is known where the event is made; the audit area stays generic | A second implementation next to a recorder that may deduplicate, and `Event` has no ID, so a summary cannot carry `repeat_of` |

**Choice:** (b), at most 10,000 open keys per replica (past that a refusal is written without being collapsed). The window is `audit.denial_dedup_window`; zero turns it off. An anonymous refusal is keyed by repository and path, because the client address is not part of the subject.

**Rationale:** Nothing in the audit contract says the recorder deduplicates, and R-AUTHZ-029 puts the obligation on authorization. The development agent would revisit this when the audit area exposes a deduplicating recorder: the producer then passes the key and deletes its own state. The two gaps that follow are [H-101](../security/permissions.md#h-101--anonymous-denials-are-deduplicated-by-repository-and-path-not-by-client-address) and the missing `repeat_of`.

**Applied in the spec:** none.

### Q-AUTHZ-20 · Details of the pattern syntax that R-AUTHZ-005 leaves open

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Question | Options | Choice |
|---|---|---|
| Does `releases/**` match `releases` itself? | (a) yes, `**` is zero or more segments; (b) no, at least one | (a), as in common glob libraries; a file named `releases` is rare, and a stricter rule would make `a/**/c` fail to match `a/c` |
| May `**` be part of a path segment (`a**b/c`)? | (a) as a run of characters; (b) invalid | (b): R-AUTHZ-005 says "as a whole segment", and a pattern that reads like a name glob but behaves differently is the kind of surprise a grant must not have; `422 invalid_pattern` names it. In package-name patterns `**` is allowed anywhere |
| Does a package-name `*` cross a slash? | (a) yes; (b) no | (b), per the table of R-AUTHZ-005 (`@acme/*` must not match `@acme/a/b`; `*` matches one unscoped name) |
| Case folding | (a) all formats; (b) NuGet only | (b): the pattern and the name are lower-cased for repositories of the format `nuget`, decided per repository at the build |

**Rationale:** each choice is the narrower reading where the text is silent, because a grant that matches too much cannot be seen from the policy. The development agent would revisit the first row if a format page defines paths where a directory and a file share a name.

**Applied in the spec:** none.

### Q-AUTHZ-21 · What a virtual repository does with `manage` and with a refused gate on a write

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-AUTHZ-009 sends writes through a virtual repository to its deploy target and lets a policy give `manage` on a virtual repository (to invalidate its merged-metadata cache). R-AUTHZ-016 answers a refused write with 403 and a refused read with 404.

| Question | Options | Choice |
|---|---|---|
| Is a `manage` request on a virtual repository a write "through" it? | (a) yes, decided on the deploy target; (b) no, decided on the virtual repository | (b): a request is rerouted to the deploy target only when it asks for an action other than `read` and `manage` |
| What does a write answer when the caller lacks the gate? | (a) 404 as for a refused read; (b) 403 | (b): the acceptance criterion of R-AUTHZ-009 ("without `read` on `npm-all`, the same command gets 403") and the rule that a refused write is 403 whether or not the target exists |

**Rationale:** both follow the text where it is explicit and keep the answer independent of what exists. The development agent would revisit the first row if a format defines a `manage` operation that acts on content.

**Applied in the spec:** none.

### Q-AUTHZ-22 · The permission names of the management API

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

M0 declared `{permission: system.read}`. R-AUTHZ-010 says that pages calling it `system.read` mean the capability `settings:read`, and the action and role forms of R-API-008 need a place in `Require`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Keep the alias | `Require` accepts `system.read` for `settings:read` | The document needs no change | Two names for one capability; an undeclared alias is how a typo becomes a permission |
| (b) Migrate and remove the alias | `api/openapi.yaml` names the capability; `parseRequirement` refuses a `permission` that is not a capability of R-AUTHZ-010 | One name; a typo stops start-up (`LoadDocument`) | One edit of the document and of the tests that named the old string |

**Choice:** (b). The role form `{role: admin}` is checked by `Require` as `"role:admin"`: the subject holds the role (or is the break-glass administrator) and its credential exercises roles (a session, or a token with `admin: true`). The action form `{action: deploy, repository: query.repository, path: query.path}` is decided by `ActionAccess.RequireAction`, whose sources are `query.<name>` and `path.<name>`; a source the request lacks names no repository and is refused. `authz.LegacySystemRead` is removed.

**Rationale:** (b) makes the declaration language closed in both directions: every name that can be written is enforceable, every enforced form can be written. The development agent would revisit the source language when the first operation needs the request body (the target repository of a copy), which the spec leaves to the handler.

**Applied in the spec:** none; [m1-contracts.md](../developer/m1-contracts.md) no longer announces the alias.

### Q-AUTHZ-23 · `authz.hide_existence` in M1

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-AUTHZ-017 is P1, but the 404 row of R-AUTHZ-016, the repository members `hideExistence` and the configuration key `authz.hide_existence` are one switch with the answer the milestone already builds.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Leave the switch for P1 | 404 always | Less to test | The hook for the troubleshooting mode would have to be added across `Decide` later |
| (b) Implement the switch in the refusal | `authz.hide_existence` (default `true`) and the repository's `hideExistence` choose between the 404 and a 403 for a refused read of an existing repository; the endpoint exposure 404 and the anonymous 401 are not affected; a repository the index does not know is always 404 | The rule of R-AUTHZ-017 in one place (`Engine.refusal`); cheap | A switch that weakens the model exists in the build before its P1 test plan |

**Choice:** (b). The decision only chooses the status; whether the resource exists is known to the caller, which answers 404 for a missing one even with hiding off. Deployments keep the default.

**Rationale:** the decision point is where the status is made. The development agent would revisit this if the walkthrough prefers the switch to appear with P1.

**Applied in the spec:** none.

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
