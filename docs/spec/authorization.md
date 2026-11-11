# Authorization

Status: specification — not implemented

This page specifies how trove decides whether a request may go ahead once the caller is known.
It covers the principals, the resources a grant can name, the package-name and path patterns that
narrow a grant, what each of the six repository actions permits, the fixed set of system roles,
projects, which hold repositories and give their members roles, the visibility levels `private`, `internal` and `public` of
the installation, a project and a repository with the wiki of a repository, internal and external users, the evaluation
algorithm, the treatment of
virtual and remote repositories, how a token's scope caps a principal's rights, package ownership,
the status code a refused request gets, how replicas keep their compiled permissions consistent,
what is audited, and the management API for all of it. How a caller proves its identity (OIDC
login, sessions, users, groups, service accounts, tokens and the scope structure) is specified in
[identity-and-authentication.md](identity-and-authentication.md). How a GitLab ID token is
verified, which identity mapping applies and how its grants are rendered is specified in
[gitlab-ci-oidc.md](gitlab-ci-oidc.md). How a request is classified and resolved through virtual
and remote repositories is specified in [repositories-and-proxy.md](repositories-and-proxy.md).
This page starts where those end: with an authenticated subject and a classified request.

Every repository belongs to exactly one project and is addressed inside it (R-AUTHZ-026). Examples use the
repositories of project `web` and call them by their key where only that project is involved; addresses and
selectors of instance contexts carry the project (`/api/npm/web/npm-all/`, `web/npm-internal`, R-AUTHZ-004).

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
| Principal | Something a grant can name: a user, a group, a service account or a CI identity mapping (R-AUTHZ-002). Principals live in the `principal` table of [identity-and-authentication.md](identity-and-authentication.md) (R-AUTH-016). A request without a credential runs as the subject `anonymous` (R-AUTHZ-003), which no grant names. |
| Visibility level | `private`, `internal` or `public`: who reads besides the members of the project and the principals a grant names. A default of the installation, a level of each project, and two levels of each repository, one for its artifacts and one for its wiki; the only way to open content to every internal user or to everyone (R-AUTHZ-033, R-AUTHZ-035). |
| Internal user | A user acting through a UI session or a personal access token who is not flagged external and whose request did not come through a Bastion (R-AUTHZ-030). Service accounts, CI tokens, Bastion registrations and the anonymous subject are never internal. |
| Artifacts, wiki | The two parts of a repository: what package clients resolve, install and download, and the documentation of its packages that trove serves or shows apart from their files (R-AUTHZ-034). A coordinate names its part (`Part`). |
| Subject | What authentication hands to authorization for one request: the principal set, the system roles the credential may exercise, whether the credential carries project roles (a UI session or a personal access token, Q-AUTHZ-215), the token scope and, for CI tokens, the frozen inline grants and the job's project identity. |
| Permission policy | A named, stored object: one resource set, and entries that give actions on that resource set to principals (R-AUTHZ-021). |
| Grant | One policy entry seen from the evaluator: principal × resource set × actions. CI inline grants ([gitlab-ci-oidc.md](gitlab-ci-oidc.md), R-CI-013) have the same shape. |
| Resource set | Repository selectors plus optional package-name and path patterns (R-AUTHZ-004, R-AUTHZ-005). Policies, token scopes and CI inline grants all use it. |
| Coordinate | The authorization view of the request descriptor of [repositories-and-proxy.md](repositories-and-proxy.md) (R-REPO-003): repository (its project and key), class, format, request kind, canonical path, normalized package name, the part (artifacts or wiki, R-AUTHZ-034), and the actions the operation needs. |
| Project, member, role | A project holds repositories, each in exactly one project, and its own policies, service accounts and CI mappings; users and groups are its members, each with one of the roles `reader`, `developer`, `maintainer`, `owner`, which grant actions on the project's repositories and, for an Owner, its administration (R-AUTHZ-026). |
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
| How do "all signed-in people" or everyone get read access? | Through a visibility level, never a policy entry: `internal` reads for every internal user, `public` for every request. The anonymous switch, the principal `authenticated` and `requireAuthentication` are gone (Q-AUTHZ-263, [ADR-192](../decisions.md#adr-192--visibility-levels-replace-the-anonymous-switch-and-the-principal-authenticated)). | R-AUTHZ-033, R-AUTHZ-035 |
| How do the installation, a project and a repository combine? | The installation's default is copied when a project or repository is created; a repository is never more open than its project and its wiki never more open than its artifacts; lowering a project lowers its repositories (Q-AUTHZ-264 to Q-AUTHZ-266). | R-AUTHZ-033 |
| Who is internal? | A user through a session or a personal token who is not flagged external by an administrator and does not come through a Bastion; never a service account, a CI token or a Bastion registration (Q-AUTHZ-268). | R-AUTHZ-030 |
| What is the wiki, and what does a caller get who reads the artifacts but not the wiki? | The documentation trove serves or shows apart from the files; a route that serves only wiki content refuses the read, a document that embeds it leaves the members out (Q-AUTHZ-270, Q-AUTHZ-271). | R-AUTHZ-034 |
| Who may publish the next version of a package? | Its owners, or a holder of `manage` on it. The grants that allowed the first publish decide the owners. Ownership is enforced by default in new npm and NuGet local repositories (Q-AUTHZ-2). | R-AUTHZ-018 |
| Whom does a writing CI inline grant make the owner? | The job's GitLab project (`bind_project`), unless the grant says `"ownership": "none"` (Q-AUTHZ-6). | R-AUTHZ-014 |
| Which reads are audited by default? | Refused reads only; allowed reads with `authz.audit.reads: all` (Q-AUTHZ-5). | R-AUTHZ-029 |
| Projects for delegated administration | MVP (beta phase 5a): every repository belongs to exactly one project and is addressed inside it; users and groups are members with the roles Reader, Developer, Maintainer and Owner; a project's policies, service accounts and CI mappings are named within it (Q-AUTHZ-206 to Q-AUTHZ-209, Q-REPO-212). Environments and sharing stay P2; CI delegations per issuer stay P1 (Q-AUTHZ-7). | R-AUTHZ-026 |
| How do n replicas learn of a permission change? | Through the `authz` change area of [architecture.md](architecture.md) R-ARCH-018: the change log in PostgreSQL carries correctness, a Valkey message only starts the rebuild sooner. Valkey never holds a grant or a decision (Q-AUTHZ-11). | R-AUTHZ-028 |
| How long can a removed grant still work? | Typically 100 ms plus one index build; at most `cluster.epoch_poll_interval` plus 1 s on a replica whose change-log poll works, with or without Valkey; never longer than `authz.cache.max_staleness` (10 s) plus one build on any replica. A replica that cannot confirm its index waits like a request waits for the database, then answers 503 (Q-AUTHZ-13). | R-AUTHZ-028 |
| Can a client see its own permission change on whichever replica answers next? | Yes, by sending `Trove-Authz-Min-Epoch` with the epoch its write returned (Q-AUTHZ-12). | R-AUTHZ-032 |

## Requirements

### R-AUTHZ-001 · Deny by default, allow-only grants
Priority: **MVP** · Covers: IAM-019

trove MUST refuse every operation that no grant, no system role (R-AUTHZ-010), no project
role (R-AUTHZ-026) and no visibility level (R-AUTHZ-033) allows. A new installation has no permission policies and the
default level `private`; only the break-glass administrator (INV-7, R-AUTH-014) can act until an administrator creates
projects, members or policies.

Grants, project roles and visibility levels MUST only add rights; there are no deny rules. A subject's rights can be
narrowed only by:

1. exclude patterns inside the same resource set (R-AUTHZ-005);
2. the token scope (R-AUTHZ-013) and, for CI tokens, the caps and the triggering-user bound of
   [gitlab-ci-oidc.md](gitlab-ci-oidc.md) (R-CI-013, R-CI-024), except for a read that a `public` level admits, which needs
   no credential at all (R-AUTHZ-035);
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
  `npm view lodash --registry https://trove.example.com/api/npm/web/npm-all/` and gets `E404`.
- `POST /api/v1/permission-policies` with an entry `"actions": ["write"]` answers 201, and the
  stored entry reads `"actions": ["annotate", "deploy", "read"]`.
- A principal holding only `deploy` on `web/npm-internal` can `npm publish` a new package there. Its
  `npm view` of that package through `npm-internal` gets `E404`, because `deploy` does not imply
  `read` (R-AUTHZ-016).

### R-AUTHZ-002 · Principals and the principal set of a request
Priority: **MVP** · Covers: IAM-013, TOK-008, TOK-015

Principal kinds and their reference syntax in this page's API:

| Kind | Reference | Defined in |
|---|---|---|
| User | `user:<username>` | [identity-and-authentication.md](identity-and-authentication.md) (R-AUTH-016) |
| Group | `group:<name>` | [identity-and-authentication.md](identity-and-authentication.md) (R-AUTH-018) |
| Service account | `sa:<name>` for an account of no project, `sa:<projectKey>/<name>` for a project's | [identity-and-authentication.md](identity-and-authentication.md) (R-AUTH-020); a name is unique within its project or among the accounts of no project (Q-AUTHZ-209) |
| CI identity mapping | `ci:<issuer>/<mapping>` for the system tier, `ci:<issuer>/<projectKey>/<mapping>` for a project's tier | [gitlab-ci-oidc.md](gitlab-ci-oidc.md) (R-CI-009); mapping names are unique per CI issuer and tier only, hence the issuer and the project ([Q-AUTHZ-214](#q-authz-214--a-projects-ci-mappings-are-named-within-the-project-like-its-policies-and-service-accounts)) |
| Bastion | `bastion:<name>` | [bastion.md](bastion.md) R-BAS-004 (phase 5b): owns Bastion tokens only; never holds a grant, a role, an owner record or a membership (`422 principal_not_allowed`) |

- `anonymous`, the subject of a request without a credential (R-AUTHZ-003), and `authenticated` are no principals a
  policy entry, a role binding, a project membership or an owner record can name: such a reference answers 422
  `principal_not_allowed` with a detail that names the visibility levels of R-AUTHZ-033, the one way to give everyone or
  every internal user `read` ([Q-AUTHZ-279](#q-authz-279--a-policy-entry-a-binding-a-membership-or-an-owner-record-that-names-anonymous-or-authenticated-is-refused)).
  The principal kind `authenticated` and its built-in row no longer exist; the built-in row of `anonymous` stays as the actor
  of a request without a credential and is refused by every table that grants
  ([identity-and-authentication.md](identity-and-authentication.md) R-AUTH-016).

- Policies, role bindings, project memberships and owner records MUST reference principals by their immutable ID in
  the `principal` table, never by name. The API accepts and returns references by name and
  translates them. Renaming keeps the grants. Deleting a principal deletes its policy entries,
  role bindings, memberships and owner entries through the foreign keys (audited, R-AUTHZ-029). A principal
  created later under a reused name inherits nothing.
- For each request, the subject's principal set is computed from the credential:

| Credential | Principal set |
|---|---|
| UI session of user `u` | `user:u` plus every group of `u`; carries the project roles of `u`; internal unless `u` is flagged external (R-AUTHZ-030) |
| Personal access token (`trv_pat_`) of `u` | the same, resolved when the token is used; carries the project roles of `u` within its scope |
| Service-account token (`trv_sat_`) of `s` | `sa:s` plus every group of `s` (a project's account has none). Never internal (R-AUTHZ-030), never a project role. |
| CI token (`trv_cit_`) | `ci:<issuer>/<m>` for every mapping `m` recorded as the token's mapping at exchange, plus the groups those mappings list (R-CI-013); the token additionally carries its frozen inline grants and, from P1, the service accounts of R-CI-024. For a cross-project subject token, a matched mapping without the `read_only` opt-in contributes nothing and is not recorded ([gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-012), so policies naming it never reach that token. Never internal. |
| No credential | none: the subject `anonymous`, which reads only what a `public` level admits, on an endpoint that admits requests without a credential (R-AUTHZ-003) |

- "Every group of `u`" means the local groups `u` belongs to plus the groups synchronized from
  the identity provider's group claim (R-AUTH-008). Membership MUST be resolved when the request is
  evaluated, not when a token was issued; tokens carry no group lists. The membership cache and
  its invalidation belong to [identity-and-authentication.md](identity-and-authentication.md)
  (R-AUTH-023).
- A user's project roles are those of its own memberships and of its groups' memberships, the highest per project
  (R-AUTHZ-026), resolved like its groups when the request is evaluated. Only a UI session and a personal access token
  carry them; a service-account token and a CI token never do
  ([Q-AUTHZ-215](#q-authz-215--a-members-role-acts-through-its-session-and-personal-tokens-within-their-scope-and-service-accounts-are-not-members)).
- There are no transient users. A CI token never stands for a user account, never picks up
  auto-join groups (R-AUTH-019, R-CI-013) and never counts as internal (R-AUTHZ-030), which
  removes the catalog's widening of CI tokens through default groups (TOK-015, CIOIDC-014).
- A disabled or deleted principal's credentials are refused by authentication
  ([identity-and-authentication.md](identity-and-authentication.md)); authorization never sees them.
- A job that continues a request after it ended (the delete job of [storage.md](storage.md) R-STOR-018) carries the
  principal, the credential and the endpoint of the request and no principal set: before each step it reads the
  principal's status and groups, and the token's scope and bundle, as they are then, and stops when the principal is no
  longer active or the credential was withdrawn ([Q-AUTHZ-260](#q-authz-260--a-job-carries-who-asked-and-reads-what-that-subject-holds-for-every-batch),
  [Q-AUTHZ-261](#q-authz-261--a-withdrawn-credential-stops-a-job-a-credential-that-reached-its-end-of-life-does-not)).

Acceptance criteria:

- Alice is in IdP group `team-web`, synchronized to trove group `team-web`, which holds `read`
  on `web/npm-internal`. After the IdP removes her from `team-web` and she logs in again, her existing
  personal access token no longer reads `npm-internal`, within the latency bound of R-AUTH-023.
- Group `everyone` is auto-join and holds `read` on `web/npm-all`, and the artifacts of `web/npm-all` are `internal`
  (R-AUTHZ-033). A CI token whose mappings list no groups and name no policy covering `npm-all` gets 404 on `npm-all`.
- `POST /api/v1/permission-policies` with an entry for `authenticated` or for `anonymous` answers 422
  `principal_not_allowed`.
- User `bob` is deleted, and a new user `bob` (different OIDC `sub`) logs in.
  `GET /api/v1/permission-policies?principal=user:bob` returns none of the old `bob`'s policies.

### R-AUTHZ-003 · Requests without a credential
Priority: **MVP** · Covers: IAM-005, AUTHN-036

A request that presents no credential runs as the subject `anonymous`. The visibility levels of R-AUTHZ-033 are the
one way to open content to it; the runtime switch `anonymousAccess`, its ceiling `auth.anonymous.allowed` and the repository
field `requireAuthentication` are gone ([Q-AUTHZ-263](#q-authz-263--visibility-levels-replace-the-anonymous-switch-the-principal-authenticated-and-requireauthentication),
[ADR-192](../decisions.md#adr-192--visibility-levels-replace-the-anonymous-switch-and-the-principal-authenticated)).

- The anonymous subject reads exactly what a `public` level admits (R-AUTHZ-035): the artifacts of a repository whose
  artifacts are `public`, and the wiki of one whose wiki is `public` (R-AUTHZ-034). Nothing else reads for it.
- `anonymous` is no principal that a policy entry, a role binding, a project membership or an owner record can name: such a
  reference answers 422 `principal_not_allowed`, with a detail that names the visibility levels
  ([Q-AUTHZ-279](#q-authz-279--a-policy-entry-a-binding-a-membership-or-an-owner-record-that-names-anonymous-or-authenticated-is-refused)).
- It never writes, never holds a role or a capability and never becomes a package owner; a level admits only `read`.
- An endpoint whose `auth.anonymous` is `false` admits no request without a credential ([endpoints.md](endpoints.md) R-EP-010):
  there a `public` level opens nothing. The default endpoint's `auth.anonymous` is the operator's ceiling of public content
  ([identity-and-authentication.md](identity-and-authentication.md) R-AUTH-037,
  [Q-AUTH-232](identity-and-authentication.md#q-auth-232--the-default-endpoints-authanonymous-is-the-operators-ceiling-of-public-content-and-the-runtime-switch-goes)).
- A refused request of the anonymous subject answers 401 with the challenge of R-AUTH-002, reads and writes alike
  (R-AUTHZ-016), so a client retries with its configured credentials. Inside a repository whose artifacts are `public`, a
  missing package is 404.
- Through a virtual repository the anonymous subject reads only when the virtual repository and every member, recursively,
  have `public` artifacts (R-AUTHZ-009 rule 6).

Acceptance criteria:

- Project `web` and its local repository `npm-public` are `public`: `npm view left-pad --registry
  https://trove.example.com/api/npm/web/npm-public/` without any `.npmrc` credential succeeds; the same command against
  `npm-internal`, whose artifacts are `internal`, gets `E401`.
- `npm publish` without a credential to `npm-public` gets `E401`.
- `POST /api/v1/permission-policies` with an entry for `anonymous` answers 422 `principal_not_allowed`.
- With `endpoints.default.auth.anonymous: false`, the anonymous `npm view` of the first criterion gets `E401`, and
  `PUT /api/v1/projects/web/visibility` with `{"level": "public"}` answers 409 `public_not_allowed`.

### R-AUTHZ-030 · Internal and external users
Priority: **MVP** · Covers: trove-only

The level `internal` admits every *internal* subject, the people of the organization that runs trove, as GitLab's internal
level admits every signed-in user who is not external (owner, 2026-11-11). A subject is internal exactly when all of the
following hold ([Q-AUTHZ-268](#q-authz-268--a-subject-is-internal-when-it-is-a-user-through-a-session-or-a-personal-token-not-flagged-external-and-not-through-a-bastion)):

1. it acts as a user through a UI session or a personal access token (R-AUTHZ-002);
2. the user is not flagged `external` ([identity-and-authentication.md](identity-and-authentication.md) R-AUTH-044), as the
   authentication stage resolved the flag for the request, with the user's status and groups;
3. the request did not reach the main installation through a Bastion (`Subject.ViaBastion`, [bastion.md](bastion.md)
   R-BAS-008, [Q-BAS-148](bastion.md#q-bas-148--a-request-through-a-bastion-never-reads-by-the-level-internal)).

- Never internal: a service-account token, a project's service accounts included (the technical accounts of a project's
  external parties, Q-AUTHZ-169), a CI token, a Bastion registration and the anonymous subject. They read what their grants
  give (a CI token also its inline grants) and what is `public`. This keeps the rule that the rights of every non-human
  credential trace back to a grant that names it, its groups or its CI mapping (R-AUTH-019, R-CI-013).
- An external user reads what its grants and its project roles give and what is `public`: it sees only what it is a member of
  or what is public (owner, 2026-11-11). A membership gives it the project's repositories with its role like any member.
- The token scope applies to a read by `internal` like to a grant (R-AUTHZ-013): a personal access token whose scope does not
  admit a repository does not read it by its level either.
- `GET /api/v1/authz/me` reports `"internal": true` or `false` for the caller (R-AUTHZ-022); the principal list no longer holds
  `authenticated`.

Acceptance criteria:

- The artifacts of `web/npm-internal` are `internal`. A user who has just logged in through OIDC, belongs to no group and
  holds no grant runs `npm view @web/button --registry https://trove.example.com/api/npm/web/npm-internal/` with a personal
  access token and succeeds. The same command with a token of `sa:web/partner`, which has no grant, gets `E404`, and so does a
  CI token whose mappings grant nothing on `npm-internal`.
- An administrator flags that user external (`PATCH /api/v1/users/alice` with `{"external": true}`). Within
  `auth.revocation_bound` her next `npm view` with the same token gets `E404` on every replica, also with Valkey stopped,
  and `GET /api/v1/repositories` no longer lists `web/npm-internal`. Made a `reader` of `web`, she reads it again.
- Through a Bastion, the internal user's token reads `web/npm-public` and gets `404` for `web/npm-internal`.

### R-AUTHZ-033 · Visibility levels of the installation, a project and a repository
Priority: **MVP** · Covers: IAM-062

The owner's task T-18 (2026-11-11): private, internal and public as in his GitLab, a default per installation, a setting per
project and per repository, and on a repository the wiki set apart from the artifacts. A level names who reads besides the
members of the project and the principals a grant names:

| Level | Reads besides members and grants | Order |
|---|---|---|
| `private` | nobody | most restrictive |
| `internal` | every internal subject (R-AUTHZ-030) | |
| `public` | every request, also one without a credential (R-AUTHZ-003) | most open |

Where a level is set and who sets it ([Q-AUTHZ-264](#q-authz-264--a-level-is-stored-on-its-object-and-the-installations-default-is-copied-when-a-project-or-a-repository-is-created),
[Q-AUTHZ-267](#q-authz-267--an-administrator-sets-the-level-of-a-project-its-owner-sets-the-levels-of-its-repositories-within-it)):

| Object | Value | Set by | Default when created |
|---|---|---|---|
| The installation | `defaultLevel` of `/api/v1/settings/visibility` ([api-and-ui.md](api-and-ui.md) R-API-056) | `security-settings:write` | `private` |
| A project | `visibility`, read with the project, set through `PUT /api/v1/projects/{projectKey}/visibility` (R-API-057) and in the body of `POST /api/v1/projects` | `projects:write` | the installation's `defaultLevel` |
| A repository | `visibility.artifacts` and `visibility.wiki` of the repository document ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-004) | the project's Owner, or `repositories:write-config` | both: the lower of the installation's `defaultLevel` and the project's level |

1. **Stored, not inherited.** A level is stored on its object. Nothing inherits a level when a request is decided; the
   installation's default is read once, in the transaction that creates a project or a repository.
2. **Never more open than the container** ([Q-AUTHZ-265](#q-authz-265--a-repository-is-never-more-open-than-its-project-and-lowering-a-project-lowers-its-repositories),
   [Q-AUTHZ-266](#q-authz-266--the-wiki-of-a-repository-is-never-more-open-than-its-artifacts)). A repository's artifacts level
   is never above its project's level, and its wiki level never above its artifacts level. A write that would break either
   answers 422 `validation_failed` with `visibility_above_project` or `visibility_wiki_above_artifacts` at the member in
   `errors[]`; the database refuses the state as well (SQLSTATE `AI008`, a check constraint for the wiki).
3. **Lowering cascades, raising does not.** Lowering a project's level lowers, in the same transaction, every repository of the
   project whose artifacts or wiki level lies above the new level to it; each lowered repository raises its configuration
   version and writes `authz.settings.changed` (R-AUTHZ-031) with the cause `project`. Raising a project's level raises
   nothing. A move of a repository to another project ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-060)
   lowers its levels to the target project's level the same way, in the move's transaction.
4. **Who opens.** The project's level is the administrator's decision; the Owner chooses its repositories' levels up to it
   and cannot change the project's. A write that raises a level needs recent authentication ([api-and-ui.md](api-and-ui.md)
   R-API-007).
5. **`public` needs an anonymous door.** `public` can be set only while the default endpoint admits requests without a
   credential (`endpoints.default.auth.anonymous`, [endpoints.md](endpoints.md) R-EP-010); otherwise every write that sets it
   answers 409 `public_not_allowed`, and `GET /api/v1/settings/visibility` reports `"publicAvailable": false`. A level that
   was set to `public` before the endpoint's setting changed stays stored, opens nothing without a credential while the
   endpoint refuses such requests, and is listed by `GET /api/v1/settings/visibility` (`publicObjects`).
6. **Read only.** A level never grants a write, a role, a capability or an owner record; it is `read` for its audience on its
   part of the repository (R-AUTHZ-035).
7. **Every class.** Local, remote and virtual repositories have both levels. A virtual repository's artifacts level is the
   gate of R-AUTHZ-009 rule 1 for its audience; the content it serves is decided on each member's own level.

Data model ([Q-AUTHZ-276](#q-authz-276--the-levels-are-columns-and-the-database-holds-the-rules-of-the-container)), migration
`00071_visibility.sql`:

```sql
CREATE FUNCTION visibility_rank(level text) RETURNS integer
    LANGUAGE sql IMMUTABLE STRICT
    AS $$ SELECT CASE level WHEN 'private' THEN 0 WHEN 'internal' THEN 1 WHEN 'public' THEN 2 END $$;

ALTER TABLE project ADD COLUMN visibility text NOT NULL DEFAULT 'private'
    CHECK (visibility IN ('private', 'internal', 'public'));

ALTER TABLE repositories
    ADD COLUMN visibility_artifacts text NOT NULL DEFAULT 'private'
        CHECK (visibility_artifacts IN ('private', 'internal', 'public')),
    ADD COLUMN visibility_wiki      text NOT NULL DEFAULT 'private'
        CHECK (visibility_wiki IN ('private', 'internal', 'public')),
    ADD CONSTRAINT repositories_wiki_within_artifacts
        CHECK (visibility_rank(visibility_wiki) <= visibility_rank(visibility_artifacts));

-- A deferred constraint trigger on repositories (INSERT, UPDATE OF visibility_artifacts, project_id) and on project
-- (UPDATE OF visibility) raises SQLSTATE AI008 at commit when a repository's artifacts level lies above its project's.

CREATE TABLE visibility_settings (
    singleton      boolean     PRIMARY KEY DEFAULT true CHECK (singleton),
    default_level  text        NOT NULL DEFAULT 'private' CHECK (default_level IN ('private', 'internal', 'public')),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    updated_by     uuid        REFERENCES principal (id) ON DELETE SET NULL
);
INSERT INTO visibility_settings (singleton) VALUES (true);

-- The repository document loses requireAuthentication: UPDATE repositories SET config = config - 'requireAuthentication'.
```

Acceptance criteria:

- With `defaultLevel: internal`, an administrator creates project `team-a`, which reads back `"visibility": "internal"`; its
  Owner creates the local repository `npm-local`, which reads back `{"artifacts": "internal", "wiki": "internal"}`.
- In `team-a`, a merge patch `{"visibility": {"artifacts": "public"}}` answers 422 with `visibility_above_project`, and
  `{"visibility": {"artifacts": "internal", "wiki": "public"}}` 422 with `visibility_wiki_above_artifacts`.
- `web` is `public` and so are both levels of `npm-public`. `PUT /api/v1/projects/web/visibility` with `{"level":
  "internal"}` answers 200; `npm-public` then reads back `{"artifacts": "internal", "wiki": "internal"}`, the audit trail holds
  one `authz.project.visibility_changed` and one `authz.settings.changed` for each lowered repository, and an anonymous `npm
  view` through `npm-public` gets `E401` on every replica within `cluster.epoch_poll_interval` plus 1 s. Raising `web` to
  `public` again leaves `npm-public` `internal`.
- The Owner of `web` gets 403 on `PUT /api/v1/projects/web/visibility`; a direct `UPDATE` that sets a repository's artifacts
  above its project's level fails at commit with SQLSTATE `AI008`.

### R-AUTHZ-034 · The wiki of a repository
Priority: **MVP** · Covers: trove-only

Every repository has two parts with a level each (R-AUTHZ-033): its **artifacts**, what package clients resolve, install and
download (packages, versions, their files, and the documents and searches that describe them), and its **wiki**, the
documentation of its packages that trove serves or shows apart from their files
([Q-AUTHZ-270](#q-authz-270--the-wiki-is-the-documentation-trove-serves-or-shows-apart-from-the-files-never-a-file-inside-an-artifact)).
This requirement is the one place that says what is wiki; a format page marks its own surfaces with a reference to it.

| Format | Wiki content | Surfaces |
|---|---|---|
| npm | The README of a package ([formats/npm.md](formats/npm.md) R-NPM-010): `readme` and `readmeFilename` of the full package document | Those two members of the full package document, local, remote and virtual (the abbreviated document carries neither); the README operation and `hasReadme` of R-API-029 |
| NuGet | The README file a package names ([formats/nuget.md](formats/nuget.md) R-NUGET-024, Q-NUGET-48) | The route `…/flatcontainer/{lower_id}/{lower_version}/readme` from P1; the README operation and `hasReadme` of R-API-029 |
| Ansible Galaxy | The README and the documentation of a collection version | The README operation and `hasReadme` of R-API-029, and the documentation surfaces that [formats/ansible.md](formats/ansible.md) lists as wiki content (T-19) |
| OCI | none: a registry serves no documentation | — |
| Debian | none ([formats/debian.md](formats/debian.md): there is no README) | — |
| Generic | none: every file is an artifact | — |

- **Not wiki.** The description, keywords, licenses, authors, links and release notes, every member a client reads to resolve,
  install or list a package, and every file inside an artifact: a README inside an npm tarball, a `.nupkg` or a collection
  archive is read with the artifact (H-880 in [security/visibility.md](../security/visibility.md)).
- **One check.** A wiki read is decided by the decision point on the coordinate with `Part: wiki` (R-AUTHZ-035): by a grant,
  a role or a capability that gives `read` on the coordinate, as for the artifacts, or by the repository's wiki level. Since
  the wiki is never more open than the artifacts, a caller who reads the wiki reads the artifacts. Code asks it in exactly
  three forms ([Q-AUTHZ-272](#q-authz-272--the-wiki-check-is-one-coordinate-part-asked-through-a-route-flag-a-host-question-and-a-document-extension)):

  | Form | Asked by | Answer when refused |
  |---|---|---|
  | `formats.Route.Wiki: true` on a protocol route that serves only wiki content | the dispatcher's coarse check, before the handler | the refusal of a read (R-AUTHZ-016) |
  | `Host.WikiReadable(ctx, rq, formats.Target{Package, Version})` | a handler that puts a wiki member into a document whose artifacts it serves | `false`: the handler leaves the member out; nothing is audited |
  | `x-trove-part: wiki` on a management operation ([api-and-ui.md](api-and-ui.md) R-API-058), and `authz.ReadScope.MayReadWiki` for a wiki member of a management answer | the shared authorization step and the handler | the 404 of a missing resource; the member is absent |

- **What a caller gets who reads the artifacts but not the wiki** ([Q-AUTHZ-271](#q-authz-271--a-caller-who-reads-the-artifacts-but-not-the-wiki-gets-the-refusal-of-a-read-on-a-wiki-route-and-documents-without-the-wiki-members)):

  | Where | Answer |
  |---|---|
  | A protocol route that serves only wiki content | 404, byte-identical to a missing resource; 401 with the challenge for a request without a credential (R-AUTHZ-016) |
  | A protocol document that embeds wiki members | The members are absent: not empty, not a placeholder; every other member is unchanged |
  | The management API | The README and documentation operations answer the 404 of a missing README; `hasReadme` is `false`, and T-19's `metadata.docs` is absent |
  | The UI | The README tab and the documentation tabs are not shown (R-UI-057) |
  | A search over wiki text (T-19's documentation search) | Hits only from packages whose wiki the caller may read; a search over names and descriptions is an artifacts read |

- **The format kit holds every format to it** ([architecture.md](architecture.md) R-ARCH-010): a route with `Wiki: true` is a read
  (`Metadata` or `Computed`, action `read`); for each format with wiki content a kit test fetches every surface of this table
  as a caller who reads the artifacts and not the wiki and finds no wiki byte in any answer.

Acceptance criteria:

- `web/npm-public` has `public` artifacts and an `internal` wiki, and `@web/button` 1.0.0 has a README. Without a credential,
  `npm install @web/button` succeeds and `npm view @web/button readme` prints nothing, because the full package document has no
  `readme` member; with an internal user's token the same `npm view` prints the README. `GET
  /api/v1/packages/{id}/versions/1.0.0` answers `"hasReadme": false` to the anonymous caller and `true` to the internal user,
  and `…/readme` 404 and 200.
- A NuGet local repository with the same levels: an anonymous `GET …/flatcontainer/acme.widgets/1.5.0/readme` answers 401 with
  the `Basic` challenge, and the same request with an internal user's token 200.
- The format kit fails for a test format whose route serves README bytes without `Wiki: true`.

### R-AUTHZ-035 · How a visibility level enters the decision
Priority: **MVP** · Covers: IAM-005

A level is a source of `read` beside grants, roles and capabilities
([Q-AUTHZ-269](#q-authz-269--a-level-reads-beside-the-grants-the-token-scope-narrows-internal-and-nothing-narrows-public-but-the-endpoint)).
`Allowed` of R-AUTHZ-015 asks it after the endpoint's ceiling and before the grants:

```go
// leveled reports whether the repository's level for the part of the coordinate admits the subject.
func leveled(sub Subject, idx *Index, a Action, c Coordinate) bool {
    if a != Read {
        return false                                               // a level admits read only
    }
    switch c.Repo.Level(c.Part) {                                  // artifacts or wiki, R-AUTHZ-034
    case Public:
        return true                                                // every subject; no scope, no CI cap
    case Internal:
        return sub.Internal() && sub.Scope.Admits(Read, c)         // R-AUTHZ-030, R-AUTHZ-013
    }
    return false
}
```

1. **Union.** A level adds `read`, grants, roles and capabilities add what they add, and none takes away what another gives.
2. **The endpoint first.** The exposure of the endpoint (R-EP-013) binds every read by a level: a `public` repository that an
   endpoint with rules does not expose answers 404 there.
3. **Ceilings.** The token scope narrows a read by `internal` like a grant. A read by `public` needs no credential, so neither
   the token scope, nor the caps of a CI token, nor the triggering-user bound of R-CI-024 narrows it: a client whose token is
   scoped elsewhere reads public content as it would without the token. An invalid credential is still refused with 401 and
   never read as anonymous (R-AUTH-001).
4. **Path-scoped grants.** A level admits every package and every path of its part; a grant narrowed by package or path
   patterns adds nothing to what a caller reads by a level, and keeps giving what it gives where no level reaches.
5. **Virtual repositories** ([Q-AUTHZ-274](#q-authz-274--a-virtual-repository-has-levels-of-its-own-and-the-anonymous-subject-still-needs-every-member-public)).
   The gate of R-AUTHZ-009 rule 1 passes by a grant or a role on the virtual repository or by its
   artifacts level; each member is decided on its own level (rule 2), so a member the caller may not read is skipped. Rule 6
   keeps the anonymous subject to virtual repositories whose every member is `public`. A merged document takes its wiki
   members from the member that supplies them and decides them on that member.
6. **Remote repositories.** A read by a level passes the populate check of R-AUTHZ-008 when the repository's
   `cacheFillAction` is `read`; setting the artifacts of such a remote repository `public` answers with the warning
   `public_populates_remote` ([Q-AUTHZ-278](#q-authz-278--a-public-remote-repository-is-filled-by-anonymous-callers-and-the-write-that-opens-it-warns)).
7. **Read shapes and listings.** A repository whose artifacts level admits the subject, on an endpoint that exposes everything
   and, for `internal`, under a scope that does not narrow, has the read shape `ReadAll` (`authz.ReadScope`). Listings,
   searches and their counts, the package API, the repository list and the project list include what a level admits
   ([Q-AUTHZ-273](#q-authz-273--every-listing-and-every-feature-that-asks-for-read-counts-a-read-by-a-level)); so does every
   feature that asks for `read` on a repository (the vulnerability findings of R-VULN-019, client setup and guides, the
   repository's consumer view). `content:metadata-read` reads the wiki of every repository.
8. **Projects.** A project is visible to a caller its level admits, besides its members and the holders of `projects:read`:
   `GET /api/v1/projects` lists it without a role and `GET /api/v1/projects/{projectKey}` reads it; its members list stays
   with its members and `projects:read` ([Q-AUTHZ-277](#q-authz-277--a-project-is-seen-by-whom-its-level-admits-and-its-members-stay-with-its-members)).
9. **Explanation and audit.** A read allowed by a level has the reason `allow.visibility` and the source `visibility:<level>`
   (R-AUTHZ-024, R-AUTHZ-029); a wiki read refused where the artifacts read is allowed has the reason `deny.wiki`.

Worked rows, in project `web` (`public`): `npm-public` (artifacts `public`, wiki `internal`), `npm-internal` (both `internal`),
`npm-private` (both `private`), the virtual `npm-all` (`internal`) over all three; Erin is a user flagged external with
`read` on `npm-private`; Frank is an internal user without grants; `sa:web/partner` holds nothing:

| # | Subject | Request | Steps | Result |
|---|---|---|---|---|
| 1 | no credential | `npm view @web/button` via `npm-public` | artifacts `public` | 200, no `readme` |
| 2 | no credential | `npm view @web/button` via `npm-all` | gate: artifacts `internal`, refused | 401 |
| 3 | Frank | `npm view @web/button` via `npm-all` | gate by `internal`; `npm-public` and `npm-internal` read, `npm-private` skipped | 200, with `readme` |
| 4 | Erin | `npm view @web/lib` via `npm-private` | grant | 200 |
| 5 | Erin | `npm view @web/button` via `npm-internal` | external, no grant | 404 |
| 6 | `sa:web/partner` | `npm view @web/button` via `npm-public` | artifacts `public` | 200, no `readme` |
| 7 | Frank, token scoped to `mobile/*` | `npm view @web/button` via `npm-internal` | `internal`, the scope refuses | 404 |
| 8 | Frank, token scoped to `mobile/*` | `npm view @web/button` via `npm-public` | `public`, no scope consulted | 200, no `readme` |

Acceptance criteria: each row is an automated test of `internal/authz` on the R-AUTHZ fixture, and rows 1, 3, 5 and 6 run with
the npm client in the e2e set `npm` ([developer/e2e.md](../developer/e2e.md)).

### R-AUTHZ-036 · Visibility changes: operations, audit, journal and propagation
Priority: **MVP** · Covers: trove-only

| Change | Operation | Audit event | Journal |
|---|---|---|---|
| The installation's default | `PATCH /api/v1/settings/visibility` (R-API-056) | `settings.visibility.changed`, before and after | no: it decides only objects created later |
| A project's level | `PUT /api/v1/projects/{projectKey}/visibility` (R-API-057) | `authz.project.visibility_changed`: project ID and key, the level before and after, the lowered repositories | `journal: revocation`, comparator below |
| A repository's levels | the repository document (R-REPO-004); the cascade of R-AUTHZ-033 rule 3 | `authz.settings.changed` with `visibility.artifacts` and `visibility.wiki` before and after, and `cause` (`request`, `project`, `move`) | `journal: revocation`, the settings comparator of R-AUTHZ-031 |
| A user's `external` flag | `PATCH /api/v1/users/{username}` ([identity-and-authentication.md](identity-and-authentication.md) R-AUTH-044) | `user.external_changed` | `journal: revocation`: setting it narrows |

- **Level comparator.** `narrows(current, after)` for `authz.project.visibility_changed` answers true only when the project
  exists in the restored database and `after.level` comes no later than the current level in the order `private`,
  `internal`, `public`. Replay sets the project's level and cascades to its repositories as rule 3 of R-AUTHZ-033 says.
- **Propagation** ([Q-AUTHZ-275](#q-authz-275--a-level-reaches-every-replica-through-the-grant-index-the-external-flag-through-the-principal-cache)):

  | What changed | Written with | Reaches | Bound |
  |---|---|---|---|
  | A project's level and the cascade | `cluster.Notify("authz", …)` and, for each lowered repository, `cluster.Notify("repo_config", …)`, in the writing transaction | The compiled grant index of every `api`, `packages` and `worker` replica (R-AUTHZ-028) | R-AUTHZ-028: typically 100 ms plus one build, at most `cluster.epoch_poll_interval` plus 1 s, never above `authz.cache.max_staleness` plus one build |
  | A repository's levels | `cluster.Notify("repo_config", …)` with the new configuration version | The grant index; the rendition caches of documents with wiki members, whose keys and ETags carry the caller's wiki answer per contributing repository (R-NPM-013, R-REPO-020) | As above |
  | A user's `external` flag | the change area `auth` (R-AUTH-023) | The principal, session and token caches that hand the flag to the stage | `auth.revocation_bound` |
  | The installation's default | `visibility_settings`, read in the creating transaction | nothing cached | immediate |
  | `publicContent` of `login-options` and `anonymousReads` of a Bastion's handshake | computed from the grant index | the next read | as the grant index |

  Not inputs: the namespace-claims index (R-REPO-023), because claims and pin rules apply independently of the caller
  (R-AUTHZ-009 rule 4); a CI token's frozen bundle (R-CI-013), because levels are read when a request is decided and never
  frozen into a token; and the Bastion's kept answers, which its feed reaches ([bastion.md](bastion.md) R-BAS-015).
- The completeness test of R-AUTHZ-028 runs the operations of this table and fails when one changes an input of the index in a
  transaction that advanced neither the `authz` nor the `repo_config` epoch. Every operation of the table answers with
  `Trove-Authz-Epoch` (R-AUTHZ-032).

Acceptance criteria:

- A trove backup is taken while `web` is `public`. Then `web` is lowered to `internal` and `alice` is flagged external. `trove
  backup restore` reports both journal entries `applied`; after the replicas start, an anonymous `npm view` through
  `npm-public` gets `E401` and alice's token gets `E404` on `npm-internal`.
- A journaled `authz.project.visibility_changed` from `internal` to `public` is reported `skipped`.
- `PUT /api/v1/projects/web/visibility` to `private` on one replica with Valkey stopped: a request on another replica is
  decided on the new level within `cluster.epoch_poll_interval` plus 1 s.

### R-AUTHZ-004 · Repository selectors
Priority: **MVP** · Covers: IAM-020

A resource set selects repositories through a list of selector strings. A repository is covered
when any selector matches it. Every repository belongs to exactly one project and its key is unique
within the project (R-AUTHZ-026, [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-002), so
a selector names the project wherever the context does not fix it
([Q-AUTHZ-216](#q-authz-216--repository-selectors-name-the-project-in-instance-contexts-and-a-bare-key-inside-a-project)).
Project keys match `^[a-z][a-z0-9]*(?:-+[a-z0-9]+)*$` (2 to 32 characters) and repository keys `^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*$` (2 to 64, INV-5), so
the characters `/`, `*`, `?`, `@` and `:` never occur in a key and make the selector kinds unambiguous.

**Instance contexts** select repositories of any project: global policies, token scopes (R-AUTHZ-013), the
system tier of CI mappings ([gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-013), exposure rules and host-root
bindings ([endpoints.md](endpoints.md) R-EP-006, R-EP-012) and configuration keys that name repositories.

| Selector | Example | Matches |
|---|---|---|
| Key (both parts literal) | `web/npm-internal` | That repository: repository `npm-internal` of project `web` |
| Key pattern (`*` or `?` in either part) | `web/npm-team-*`, `*/npm-remote` | Every current and future repository whose project key matches the first glob and whose key matches the second (`*` any run of characters, `?` one character; neither crosses `/`) |
| Class | `@any-local`, `@any-remote`, `@any-virtual`, `@any` | Every current and future repository of that class in every project (`@any`: every class) |
| Format filter (suffix `:<format>` on a pattern or class) | `@any-local:npm`, `*/*:npm` | As before, restricted to repositories of that format |

**Project contexts** select repositories of one project only: project policies (R-AUTHZ-026), the project tier
of CI mappings (R-CI-013), the members and the deployment target of a virtual repository
([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-017, R-REPO-021). A selector there is a bare key
(`npm-internal`), a key pattern (`npm-team-*`), a class (`@any-local`) or either with a format filter
(`@any-local:npm`, `npm-*:npm`), and it is evaluated against the repositories of the project and of no other.

- A bare key or key pattern in an instance context is refused, never read as a selector that matches nothing:
  422 `invalid_pattern` in a policy, a CI mapping and an exposure rule, 400 `invalid_scope` in a token scope
  ([identity-and-authentication.md](identity-and-authentication.md) R-AUTH-024), `scope_malformed` in the
  exchange's `scope` (R-CI-007). A selector with a `/` in a project context is refused with 422
  `invalid_pattern` (a virtual repository's member with 422 `invalid_member`): a project's objects name no
  repository of another project.
- Pattern and class selectors are dynamic: a repository created later is covered as soon as the
  replicas have picked up the change (R-AUTHZ-028), without touching the policy.
- Key selectors MUST name an existing repository when the policy is written (422
  `unknown_repository` otherwise; in a project context a key of another project cannot be written at all) and are
  stored by repository ID. The write binds the key to the repository the database holds in its own transaction, under a
  share lock of the policy's project and of the repository row, never through a replica's cached view of another
  moment, so a policy write and a move of the repository run one after the other: a key the project no longer holds
  is `unknown_repository`, and a selector written first refuses the move (R-REPO-060). The database refuses a project
  policy's key selector on a repository of another project (SQLSTATE `AI007`); the guard locks the repository `FOR SHARE` before it reads its project, so a selector write that skipped the locks above and a move meet in either order and one is refused (Q-REPO-234). A key selector of an instance context keeps naming its repository when the repository
  moves to another project ([repositories-and-proxy.md](repositories-and-proxy.md#r-repo-060--move-a-repository-to-another-project)
  R-REPO-060) and reads back with the new project. Deleting a repository removes its key selectors (audited). A
  policy left without selectors stays stored, matches nothing and is reported with `"inert": true`. A repository
  created later under the same project and key is not covered.
- A format filter MUST name a format the server knows (422 `unknown_format`).
- Virtual repositories can be selected; R-AUTHZ-009 defines what a grant on one means. A key
  selector that names a virtual repository accepts only `read` and `manage`; other actions are
  rejected with 422 `virtual_action_not_applicable`. When a pattern or class selector covers
  virtual repositories, actions other than `read` and `manage` have no effect on them.
- The same selector strings are used in token scopes (R-AUTHZ-013) and CI inline grants (R-CI-013),
  where templating may produce keys and patterns but never class selectors, and a rendered value never
  inserts a `/` (R-CI-014).

Acceptance criteria:

- A global policy grants `group:developers` `read` on `["web/npm-team-*"]`. An administrator creates local
  repository `npm-team-x` in project `web`; within `cluster.epoch_poll_interval` plus 1 s, also with Valkey
  stopped, a member of `developers` can `npm view` a package published there
  (`--registry https://trove.example.com/api/npm/web/npm-team-x/`) through any replica, without any change to the
  policy. A repository `npm-team-y` of project `mobile` is not covered.
- A global policy with the selector `*/npm-remote` covers `web/npm-remote` and `mobile/npm-remote`; one with the
  selector `npm-internal` answers 422 `invalid_pattern`, and so does a policy of project `web` with the selector
  `mobile/npm-internal`.
- Repository `npm-old` of project `web` is named by key (`web/npm-old`) in global policy `legacy`, then deleted and
  created again. `GET /api/v1/permission-policies/legacy` reports `"inert": true` and grants nothing on the new
  `web/npm-old`.

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
- On a format with package names, a coordinate that is not repository-level and carries no package
  name (a path the format places in no package, such as an npm scope folder) does not match a
  `packages` dimension that holds any pattern, include or exclude: nothing shows that it lies outside
  the excluded names (Q-AUTHZ-160). A raw path under `/files/` and in the items API carries the
  package and the version its format gives it, so an npm tarball is decided on its package
  ([endpoints.md](endpoints.md) R-EP-014).
- A coordinate that is not repository-level and carries no path does not match a `paths` dimension that holds any
  pattern, include or exclude (Q-AUTHZ-250): nothing shows that what it stands for lies outside the excluded paths. The
  path of a protocol request is the canonical path of the document or file it serves or writes, which the format plug-in
  states with the package (R-AUTHZ-007); a question about a package as a whole that the core asks itself (a row of a
  listing or a search, the ownership of a name, a check of R-AUTHZ-023 that names a package and no path) is decided on
  the canonical path of the package's document, the format's package-document path
  ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-029). The conformance kit fails a format whose handler
  authorizes a package without a path ([architecture.md](architecture.md) R-ARCH-010 check 13).
- Both dimensions MUST match. Exclude beats include within one resource set and nowhere else
  (R-AUTHZ-001).
- Repository-level coordinates (search, NuGet service index, repository listing) match every grant
  whose selectors cover the repository, regardless of its patterns. Results that list items are
  then filtered per item (R-AUTHZ-016), a package on the path of its package document.
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
the format plug-in reports: on a protocol route the canonical path of the document or file the route serves or writes,
so the protocol and `/files/` decide one file on one path (Q-AUTHZ-250). A path pattern that admits a package of an npm,
NuGet or OCI repository has to cover its package document and its files: `@acme/**` covers `@acme/ui` and
`@acme/ui/-/ui-1.0.0.tgz`, NuGet's `acme.core/**` covers `acme.core/index.json`,
`acme.core/.registration/index.json` and the files below `acme.core/<version>/`, and OCI's `team/app/**` covers the tags list document
`team/app/_taglist` and the items below `team/app/_tags/`, `_manifests/` and `_blobs/` (Q-OCI-140). A pattern finer than a package's folder
(`<name>/-/**`, `acme.core/1.0.0/**`, a single file) decides some of a package's paths and not others. A version is
therefore decided twice: on the package document's path and on the path of the file that holds the version's content (npm's
tarball, NuGet's `.nupkg`, OCI's tag `<name>/_tags/<tag>`; the format's `VersionPather`, [architecture.md](architecture.md)
R-ARCH-008, Q-AUTHZ-258), and it may be read
only if both allow it (Q-AUTHZ-255). A pattern that hides a version's file hides the version wherever the package is shown:
a local package document lists only the versions the caller may read (a `latest` that is hidden moves to the highest
version left, and the README of a `latest` that moved goes with it), a version document and a registration leaf of a hidden
version are missing, the search and the package screens count and show only readable versions, and the endpoint's
exposure view decides a version the same way (Q-EP-85). A write is decided on every file it writes or removes, and a
version the caller may not read is not the caller's to change (Q-AUTHZ-256). Remote and virtual documents are filtered by
the endpoint's rules only ([permissions.md](../security/permissions.md) H-655). When a policy with any path pattern, include or exclude, covers an
npm or NuGet repository, the policy API returns the warning `path_patterns_on_package_format` (R-AUTHZ-021,
Q-AUTHZ-253). NuGet stores lower-case paths (R-NUGET-005), and the path dimension of a NuGet repository is compared in
lower case, pattern and path alike, whenever the pattern was written and whatever selector brings it to the repository
(Q-AUTHZ-254): `Acme.Secret/**` excludes `acme.secret`. A policy, an exposure rule, a token scope and a CI inline grant
whose resource set covers a NuGet repository when it is written refuse a path pattern with an upper-case letter, 422
`invalid_pattern` (400 `invalid_scope` for a token scope) at the pattern, so its writer sees the pattern as it is applied
(Q-AUTHZ-253).

Preconditions on coordinates:

- Paths and names MUST be canonical before evaluation. A request whose path contains `.` or `..`
  segments, empty segments, NUL or backslashes, also after one round of percent-decoding, is
  rejected with 400 before authorization runs. Decoding happens exactly once. The parser is part
  of request classification ([architecture.md](architecture.md), R-REPO-003).
- For uploads into a repository whose format has package names, the name used for authorization
  MUST be the name the content declares (npm `package.json` `name`, NuGet `.nuspec` `id`), which is
  also the name the package is indexed under. If the name derived from the request path or URL
  differs from the declared name, the upload is rejected with 400 before it is stored. This applies
  to the package protocol and to the raw file endpoint `/files/<projectKey>/<repoKey>/…` alike.

| Resource set | Coordinate | Match? |
|---|---|---|
| packages include `@acme/*` | npm `@acme/ui` | yes |
| packages include `@acme/*` | npm `@acme-tools/ui` | no |
| packages include `@acme/*`, exclude `@acme/secret-*` | npm `@acme/secret-keys` | no |
| packages include `Acme.*` | NuGet `ACME.Core` | yes |
| packages include `Acme.*` | NuGet `AcmeCore` | no |
| paths include `releases/**` | generic `releases/1.0/app.zip` | yes |
| packages include `@acme/*` | raw file `/files/web/npm-internal/readme.txt` (no package name) | no |
| packages exclude `@secret/*` | raw file `/files/web/npm-internal/@secret/x/-/x-1.0.0.tgz` (package `@secret/x`) | no |
| packages exclude `@secret/*` | raw folder `/files/web/npm-internal/@acme/` (no package name, Q-AUTHZ-160) | no |
| packages include `a\*b` (a rendered CI template) | npm `a*b` / `ab` | yes / no |
| paths exclude `@secret/**` | npm protocol: the package document of `@secret/x` (path `@secret/x`), its tarball (`@secret/x/-/x-1.0.0.tgz`) | no |
| paths exclude `@secret/**` | npm protocol: the package document of `@acme/ui` (path `@acme/ui`) | yes |
| paths exclude `@secret/**` | a coordinate that names the package `@acme/ui` and no path (Q-AUTHZ-250) | no |
| paths include `acme.core/**` | NuGet protocol: a registration leaf of `acme.core` (path `acme.core/.registration/index.json`) | yes |
| paths exclude `hidden/-/hidden-2.0.0.tgz` | npm: the package document of `hidden` (path `hidden`) | yes, without version 2.0.0 (Q-AUTHZ-255) |
| paths exclude `hidden/-/hidden-2.0.0.tgz` | a listing row `hidden@2.0.0` (paths `hidden` and `hidden/-/hidden-2.0.0.tgz`) | no |
| paths exclude `acme.secret/2.0.0/**` | NuGet: the version list and the registrations of `acme.secret`, the search | yes, without version 2.0.0 |
| paths exclude `**/*.sigstore` (with `deploy`) | npm: a publish that attaches a provenance bundle (paths `<name>`, the tarball, the bundle) | no, the whole publish (Q-AUTHZ-256) |
| no include lists | anything in the selected repositories | yes |

Acceptance criteria:

- A policy grants `read` on `web/npm-internal` with `packages.include: ["@acme/*"]`. `npm view
  @acme/ui` and `npm install @acme/ui` succeed; `npm view @other/ui` gets `E404`.
- A policy grants `read` on `web/nuget-internal` with `packages.include: ["Acme.*"]`.
  `dotnet add package acme.core --source
  https://trove.example.com/api/nuget/web/nuget-internal/v3/index.json` succeeds.
- A policy grants `read` and `deploy` on `web/npm-internal` with `paths.exclude: ["@secret/**"]`. `npm view @secret/x`
  and the download of its tarball get `E404` through the protocol as through `/files/`, and `npm publish` of
  `@secret/x` gets `E403`; `npm install @acme/ui` succeeds. With `paths.include: ["@acme/**"]`
  instead, `npm install @acme/ui` succeeds and `npm view @other/ui` gets `E404`; the same holds for a token whose scope
  carries the pattern, and for NuGet's flat container, registrations, search and push
  (`TestR_AUTHZ_005_PathPatternsDecideTheNPMProtocolOnCanonicalPaths`,
  `TestR_AUTHZ_005_PathPatternsDecideTheNuGetProtocolOnCanonicalPaths`).
- A policy grants `read` on `web/npm-internal` with `paths.exclude: ["hidden/-/hidden-2.0.0.tgz"]`. `npm view hidden`
  lists 1.0.0 alone with `latest` at 1.0.0 and without the README of 2.0.0, `npm view hidden@2.0.0` gets `E404`, and the
  package screen shows 1.0.0 as the latest and answers 404 for the README of 2.0.0; a caller without the pattern sees both
  versions from the same replica, before and after. With `paths.exclude: ["acme.secret/2.0.0/**"]` on NuGet, the version
  list, the registrations and the search leave 2.0.0 out (`TestR_AUTHZ_005_AVersionPathPatternHidesTheVersionFromNPMDocuments`,
  `TestR_AUTHZ_005_AVersionPathPatternHidesTheVersionFromNuGetDocuments`).
- A policy grants `read`, `deploy` and `delete` with `paths.exclude: ["**/*.sigstore"]`: `npm publish --provenance`
  gets `E403` and stores nothing; with `paths.exclude: ["**/*.snupkg"]` on NuGet, a delete of a version (deleteMode
  `delete`) gets 403 and removes nothing, and so does one whose version folder holds a file a pattern excludes that the
  route does not name (Q-AUTHZ-257) (`TestR_AUTHZ_005_AnNPMWriteIsDecidedOnEveryFileItWritesOrRemoves`,
  `TestR_AUTHZ_005_ANuGetWriteIsDecidedOnEveryFileItWritesOrRemoves`).
- A caller whose `read` grant hides `keep@1.0.0` and who holds `deploy` and `delete` meets that version in every npm write
  as one the package does not hold: a revisioned `PUT` that names it and a dist-tag `PUT` that points a tag at it answer the
  400 of a missing version, the tarball `DELETE` of npm's unpublish answers the 200 of a removed one, a `DELETE` of a tag at
  it the 404 of an unknown tag; a dist-tag `PUT` that would move a tag at it answers 403 and moves nothing (Q-NPM-84,
  `TestR_AUTHZ_005_TheTarballDeleteOfAnUnpublishAnswersAHiddenVersionAsAMissingOne`); a publish under a tag at it stores
  the new version and leaves the tag, with a notice that does not name 1.0.0 (Q-NPM-85,
  `TestR_NPM_016_APublishLeavesATagAtAVersionThePublisherMayNotReadWhereItIs`).
- A `read` grant on the OCI repository `web/docker-local` with `paths.exclude: ["team/app/_tags/v2"]` hides the tag `v2` of
  `team/app` from the version list of the package API, as the tags list of `/v2/` leaves it out, and lists `v1`; a caller
  without the pattern sees both (Q-AUTHZ-258, `TestR_AUTHZ_005_APathPatternOnATagHidesItFromThePackageAPI`).
- `curl -X PUT --data-binary @evil-1.0.0.tgz
  https://trove.example.com/files/web/npm-internal/@acme/ui/-/ui-9.9.9.tgz` with a tarball whose
  `package.json` names `@other/evil` answers 400 and stores nothing.
- `GET /files/web/npm-internal/%2e%2e/secret` answers 400.

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
it needs a system role (R-AUTHZ-010, R-AUTHZ-012) or the role `owner` in the repository's project (R-AUTHZ-026).

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
  `curl -T app.zip https://trove.example.com/files/web/files-internal/releases/app.zip` succeeds the
  first time, succeeds again with the same bytes, and answers 403 with different bytes.
- With `delete` but not `deploy` on `npm-internal`, `npm unpublish @acme/ui@1.0.0` succeeds and
  `npm publish` answers 403.

### R-AUTHZ-007 · Action map for npm, NuGet and OCI operations
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
| `npm view`, `npm install`: package document, version document, dist-tags | name; path `<name>`, the package document they are part of; each version listed also on its tarball's path (Q-AUTHZ-255) | `read` | — |
| Tarball download | name; the tarball's path `<name>/-/<file>` | `read` | — |
| `npm publish` of a new version (with its dist-tag) | name; path `<name>` before the body is read, the tarball's path once the body names the version, and the provenance bundle's path `<name>/-/<base>-<version>.sigstore` when the body attaches one (Q-AUTHZ-256) | `deploy`; for the first version of the name in the repository also the shadowing check | yes |
| `npm publish` of an existing version | name | refused by the format (immutable versions) | — |
| `npm unpublish name@version`, `npm unpublish name` | name; path `<name>`, the tarball's and the bundle's path of every version it removes, and the tarball's path for the tarball `DELETE`; a version the caller may not read is not removed (Q-AUTHZ-256) | `delete` | yes |
| `npm dist-tag add` / `rm` | name; path `<name>`; a tag at a version the caller may not read answers as at a missing one | `deploy` | yes |
| `npm deprecate` | name; path `<name>` | `deploy` | yes |
| `npm search` | repository level; results filtered per name, on the path `<name>` | `read` | — |
| npm metadata recalculation | repository level | `manage` | — |
| `npm whoami` | none | an authenticated subject who can read the repository (Q-AUTHZ-167) | — |

The npm package document is written by two requests, and the path says which. The revisioned `PUT
{name}/-rev/{rev}` (the first request of `npm unpublish name@version`) removes the versions its
document lacks and needs `delete`; the dist-tags in that document are not a write of tags, they
follow the rule of an unpublish ([formats/npm.md](formats/npm.md) R-NPM-017). Dist-tag writes have
their own endpoints and need `deploy`. The unrevisioned `PUT {name}` is a publish (`deploy`);
without an attachment it is the metadata update of `npm deprecate`, which is not built and answers
404 whatever the actions (R-NPM-043); once it is built it needs `deploy` (Q-AUTHZ-166).

NuGet:

| Client operation | Coordinate | Actions | Ownership |
|---|---|---|---|
| Service index | repository level | `read` | — |
| Flat container (version list, package download), registrations | ID; the canonical path of the document or file (R-NUGET-005): `{id}/index.json`, `{id}/{version}/{file}`, and `{id}/.registration/index.json` for every registration document; each version listed also on its `.nupkg`'s path, and a `.nuspec` of a version whose `.nupkg` is refused is missing (Q-AUTHZ-255) | `read` | — |
| Search, autocomplete | repository level; results filtered per ID and version, on the path of its version list `{id}/index.json` and the version's `.nupkg` | `read` | — |
| `dotnet nuget push` of a new version | ID; the paths of the version's `.nupkg` and of the generated `.nuspec` (Q-AUTHZ-256) | `deploy`; for the first version of the ID in the repository also the shadowing check | yes |
| Push of an existing, deleted or moved-away version | ID | refused by the format (immutable versions with tombstones, [formats/nuget.md](formats/nuget.md) R-NUGET-006) | — |
| `dotnet nuget delete` | ID; the path of the version's `.nupkg`, and in deleteMode `delete` also of its `.nuspec` and `.snupkg`; a version the caller may not read answers as a missing one (Q-AUTHZ-256) | `delete`, whether the repository deletes or unlists | yes |
| Relist | ID; the path of the version's `.nupkg`; a version the caller may not read answers as a missing one | `deploy` | yes |
| Symbol package push | ID; the path of the version's `.snupkg` | `deploy` | yes |

The NuGet API key header carries a credential (INV-8) and plays no further part in authorization.

OCI (P1; the coordinate is the repository, the image name as the package and, for a tag, the tag as the version; protocol details on
[formats/oci.md](formats/oci.md) R-OCI-006). The last column is the action of the distribution token protocol a registry token must
grant for the operation: the token service grants it by the coarse check of the row's action, and the grant is a ceiling only, every
request is decided again (R-OCI-005, Q-OCI-6):

| Client operation | Coordinate | Actions | Ownership | Token scope action |
|---|---|---|---|---|
| Version check `/v2/` | none | a valid credential, or none (answered 401) | — | — |
| Catalog | repository level; results filtered per name | `read` | — | `registry:catalog:*` |
| Manifest or blob `GET`/`HEAD`, tags list, referrers | name (and tag) | `read`; a remote fill also its `cacheFillAction` (R-AUTHZ-008) | — | `pull` |
| Start, continue, finish, query or cancel a blob upload | name | `deploy` | yes | `push` |
| Mount a blob from another name | target name; source repository and name | `deploy` on the target, `read` on the source | yes (target) | `push` on the target, `pull` on the source |
| Push a manifest by digest, or under a tag that does not exist or already points at the same digest | name, tag | `deploy`; for the first version of the name in the repository also the shadowing check | yes | `push` |
| Push a manifest under a tag that points at another digest | name, tag | `deploy` and `overwrite` | yes | `push` |
| `DELETE` a tag, a manifest or a blob | name | `delete` | yes | `delete` |

The per-row test of the OCI table is `TestR_AUTHZ_007_OCIActionMap`, with the integration tests of the format; a row the format does not
serve yet answers its `501` until the handler of the row is built.

The path in the coordinate column is what a grant's, a token scope's and an exposure rule's path patterns are matched
against (R-AUTHZ-005): the canonical path of the document or file the operation serves or writes, as the format's
classification reports it ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-003), so the protocol decides a
file on the path `/files/` decides it on. An operation on the package as a whole is decided on its package document's
path. A new format's table names the path of every row ([architecture.md](architecture.md) R-ARCH-010 check 13 fails a
handler that authorizes a package without one, Q-AUTHZ-250).

**Per-format notes and tests (E3.4).** Every row of the two tables has one test per format, or a note below, each
holding a principal with exactly the action of the row (plus `read`, which a client needs to see
the repository) against one who lacks it: `TestR_AUTHZ_007_NPMActionMap`
([test/integration/npm/actions_test.go](../../test/integration/npm/actions_test.go)) and
`TestR_AUTHZ_007_NuGetActionMap`
([test/integration/nuget/actions_test.go](../../test/integration/nuget/actions_test.go)). The
repositories of these tests have `packageOwnership: off`, so that only the action is shown;
ownership and the shadowing check have their own tests (R-AUTHZ-018, R-REPO-048). A row that does
not apply to a format, or that the format does not serve yet, is recorded here and not tested for
the action:

| Row | npm | NuGet |
|---|---|---|
| Dist-tag add and remove, deprecate | applies; deprecate is not built: the metadata update answers 404 with every action (R-NPM-043), and the test pins that answer so that it fails the day deprecation is built | not applicable: NuGet has neither dist-tags nor deprecation |
| Search | not built: `-/v1/search` answers 404 (R-NPM-043); pinned the same way | applies |
| Autocomplete | not applicable | not built (P1, R-NUGET-011): answers 501 `nuget_not_implemented` with every action; pinned the same way |
| Metadata recalculation | not built: the endpoint (R-REPO-016, R-API-024) is not in the API document before M7 | no row in the NuGet table; the NuGet reindex (R-NUGET-025) is covered by R-REPO-016 |
| `npm whoami` (and `npm ping`) | applies: an authenticated subject who may read the repository (Q-AUTHZ-167) | not applicable |
| Unlist, relist, symbol package push | not applicable | applies; the delete row is tested in a repository of each `deleteMode` |
| Push of an existing, deleted or moved-away version | applies: 409 `npm_version_exists` | applies: 409 `nuget_version_exists`, `nuget_version_deleted`; the `reserved` tombstone of a restore and a moved-away version arrive with P1 operations and have no test here |

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

Raw file access through `/files/<projectKey>/<repoKey>/<path>` maps `GET`/`HEAD` to `read`, `PUT` to `deploy`
(plus `overwrite` when the path exists with different content) and `DELETE` to `delete`. In a
repository whose format has package names, the format plug-in classifies the path, so name
patterns and ownership apply to raw requests exactly as to protocol requests (R-AUTHZ-005).

Acceptance criteria:

- With `read` and `annotate` but not `deploy` on `@acme/ui`, `npm dist-tag add @acme/ui@1.2.0
  stable` gets `E403`. With `read` and `deploy`, the same command and `npm publish --tag next` of
  1.3.0 both succeed.
- With `deploy` but not `delete` on `nuget-internal`, `dotnet nuget delete Acme.Core 1.0.0
  --source https://trove.example.com/api/nuget/web/nuget-internal/v3/index.json --api-key
  "$TROVE_TOKEN" --non-interactive` answers 403.
- A user with `read` only on `Acme.*` in `nuget-internal` gets search results without any ID
  outside `acme.*`, and the total hit count counts only those.

**Ansible Galaxy.** The table of [formats/ansible.md](formats/ansible.md) R-ANS-007 follows the same rules: discovery, collection,
version list, version, download and import task are `read` (the package and the archive path); `POST v3/artifacts/collections/` and an
upload through `/files/` are `deploy` on `<ns>.<name>` and the archive path with ownership and the shadowing check of a first publish;
the role routes are `read` on the repository, because a role document has no package name a grant can name; the removal of a version is
`delete` and the clearing of a tombstone `manage`. `TestR_AUTHZ_007_AnsibleActionMap` has a subtest per row.

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
- **A mirror populates as the system.** A remote in mode `mirror` fills its cache in background passes without a caller
  ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-053): the decision to mirror is the populate decision, so
  setting the mode needs `repositories:write-config` (Q-REPO-142), and each fill writes the populate event with the system
  as actor and `reason: mirror` (Q-REPO-145). `cacheFillAction` keeps applying to clients.
- Remote repositories never accept uploads, whatever the grants
  ([repositories-and-proxy.md](repositories-and-proxy.md)).
- **No populating through the session cookie.** A request authenticated by the session cookie
  ([identity-and-authentication.md](identity-and-authentication.md) R-AUTH-001) MUST NOT pass the
  populate check: it gets what the cache holds, exactly like a caller without the populate action.
  The cookie accompanies top-level navigations from other sites and same-origin image loads, so a
  link or a README image could otherwise make an administrator's browser bring an
  attacker-chosen package into a frozen mirror. Package clients authenticate with tokens and are
  not affected; the UI shows cached content only.
- **Public populating is flagged.** A read by a visibility level passes the populate check like a read by a grant
  (R-AUTHZ-035). When a write sets the artifacts of a remote repository whose `cacheFillAction` is `read` to `public`, or
  sets `cacheFillAction: read` on a remote repository whose artifacts are `public`, the repository API saves it and
  returns the warning `public_populates_remote`: every caller on the network can then make trove download arbitrary
  upstream packages, and the warning makes that visible when the repository is opened
  ([Q-AUTHZ-278](#q-authz-278--a-public-remote-repository-is-filled-by-anonymous-callers-and-the-write-that-opens-it-warns)).
- **One check, asked two ways.** The check is `MayPopulate` of R-AUTHZ-015: the session cookie and
  an endpoint with `cache_fill: false` ([endpoints.md](endpoints.md) R-EP-019) refuse first, then
  `read` and the action of `cacheFillAction`; a `cacheFillAction` that names neither refuses. The
  endpoint's setting travels in the subject with the endpoint's name (Q-AUTHZ-153). Where a refusal
  is answered from the cache (an expired document served stale, a virtual member counted as a
  miss) the check is a question and nothing is audited; where the request has nothing to serve,
  the refusal is 403 `populate_not_permitted` and is audited like a refused write (`deny.populate`,
  R-AUTHZ-029), whatever `authz.audit.reads` says.

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
  --registry https://trove.example.com/api/npm/web/npmjs/` succeeds with an empty cache.
- The same remote switched to `deploy`, Bob holds `read` only: `npm view is-odd` for a package
  not yet cached gets `E403` directly on `npmjs`, and `E404` through `npm-all`, and the outbound
  request log shows no upstream request. After a principal with `deploy` has fetched it, Bob's
  command succeeds.
- With `cacheFillAction: deploy`, Bob's request for a cached document that expired 10 minutes ago is
  answered from the cache, and no upstream request is made.
- Carol holds `read` and `deploy` on `npmjs` with `cacheFillAction: deploy`. `GET
  /api/npm/web/npmjs/is-odd` for the uncached package, carrying only Carol's session cookie, answers
  403 `populate_not_permitted`, and the test upstream records no request. The same request with
  Carol's personal access token fetches the package.
- In the `public` project `web`, a merge patch `{"visibility": {"artifacts": "public"}}` on `npmjs` (`cacheFillAction:
  read`) answers 200 with `"warnings": ["public_populates_remote"]`; the same patch on a remote repository with
  `cacheFillAction: deploy` answers 200 without that warning.

### R-AUTHZ-009 · Virtual repositories
Priority: **MVP** · Covers: IAM-024, PKG-NPM-042, PKG-NPM-045, PKG-NUGET-027

1. **Endpoint gate.** Every request addressed to a virtual repository requires `read` on the
   virtual repository itself. Without it, the request is refused before any member is consulted.
2. **Reads are decided per member.** During resolution (R-REPO-019 in
   [repositories-and-proxy.md](repositories-and-proxy.md)), a member is consulted only if the
   subject holds `read` on the member for that coordinate. Members the subject cannot read are
   skipped silently. Nested virtual repositories (P1) apply rule 1 at each level.
   A token scope that admits `read` on the virtual repository admits these member reads for the
   request that went through it (R-AUTHZ-013, Q-AUTHZ-163).
3. **Fetching through a virtual repository** is checked on the remote member with that member's
   `cacheFillAction` (R-AUTHZ-008).
4. **Merged metadata** (npm package documents, NuGet registrations, search results) MUST be built
   only from members, and items, the subject may read. A cached merged document (R-REPO-020) MUST
   NOT be served to a subject whose set of readable members for that package differs from the set
   it was built for. Pin rules and namespace claims (R-REPO-023) are applied independently of the
   caller's permissions, so a caller who cannot read an internal package gets 404 rather than a
   public package of the same name. A listing of the members' rows for the virtual repository (a
   merged search) decides each row as a read through it, so its exposure (Q-EP-11) and the lift of
   rule 2 apply as they do to its documents (Q-AUTHZ-168).
   A format whose merged index is one signed file for every caller (the Debian format, Q-DEB-19) raises the gate for
   that file: it needs `read` without package restriction on every member (R-DEB-027), because rule 2 cannot
   apply to a file that is not built per caller. A caller who lacks it gets 403 `deb_members_unreadable`, an anonymous
   caller 401 (rule 6).
5. **Writes through a virtual repository** go to its deploy target (R-REPO-021 in
   [repositories-and-proxy.md](repositories-and-proxy.md)). Actions and ownership are checked on
   that target with the same coordinate, and the gate of rule 1 applies as well. Grants of
   `annotate`, `deploy`, `overwrite` or `delete` on the virtual repository itself have no effect.
6. **Anonymous callers** may use a virtual repository only if the artifacts of the virtual repository and of every
   member, recursively, are `public` (R-AUTHZ-033). Otherwise every anonymous request through
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
(R-AUTHZ-004), and a project role covers them like every repository of its project (R-AUTHZ-026), restricted to `read` (the gate) and `manage` (metadata cache invalidation); the
catalog leaves open whether a policy may hold a virtual repository key, and trove allows it. The
gate lets administrators decide which aggregated endpoints a group uses, at the cost of one extra
selector (`@any-virtual:npm` covers all npm virtual repositories). A virtual repository's members are repositories of
its own project ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-017), so a gate and its members are
always decided within one project. Because content is always
decided on the member, rearranging the members of a virtual repository never exposes content to
someone who could not read it directly. Q-AUTHZ-1 records the decision;
[repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-019 step 1 applies the gate.

Acceptance criteria:

- Virtual `npm-all` has members `npm-internal` and `npmjs`. Alice holds `read` on `npm-all` and
  `npm-internal` only: `npm view left-pad --registry https://trove.example.com/api/npm/web/npm-all/`
  gets `E404`; after `read` on `npmjs` is added, it succeeds.
- `@acme/ui` 1.0.0 is in `npm-internal-a` (Alice may read it) and 2.0.0 in `npm-internal-b` (she
  may not); both are members of `npm-all`. `npm view @acme/ui versions` through `npm-all` shows
  only `1.0.0` to Alice. Carol, who may read both, sees both, also when Alice's request came first.
- The artifacts of `npm-all` and `npmjs` are `public`, those of `npm-internal` are `internal`: an anonymous `npm view
  left-pad` through `npm-all` gets 401.
- Alice holds `read` on `npm-all` and `deploy` on `npm-internal` (the deploy target):
  `npm publish --registry https://trove.example.com/api/npm/web/npm-all/` succeeds. Without `read` on
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
| `endpoints:read`, `endpoints:write`: endpoints and their exposure rules ([endpoints.md](endpoints.md) R-EP-016), and Bastion registrations with their tokens ([bastion.md](bastion.md) R-BAS-004) | yes, yes | yes, yes | yes, — | yes, — | — |
| `vulnerabilities:triage`, from M4b: change the status of vulnerability findings, which sets, lifts or restores download blocks (a block on a confirmed finding, the dismissal of a malicious one); effective only together with `read` on the finding's repository or `content:metadata-read` ([vulnerability-scanning.md](vulnerability-scanning.md) R-VULN-013, Q-VULN-8, Q-VULN-20) | yes | yes | yes | — | — |
| `content:metadata-read`: read the metadata of every repository (package lists, versions, documents, search, package pages, vulnerability findings), never an artifact (owner, [Q-AUTHZ-172](#q-authz-172--the-auditor-reads-every-repositorys-metadata-and-triages-but-downloads-nothing)) | yes | — | yes | — | — |

- Roles are bound to users, groups and service accounts (R-AUTHZ-022). A role bound to a group
  applies to every member, including members synchronized from the identity provider. This is how
  an IdP group carries administrator rights; there is no administrator flag on users or groups
  (IAM-016, R-AUTH-008).
- Roles are never bound to `anonymous`, to `authenticated` (R-AUTHZ-002) or to CI identity
  mappings (R-CI-013: CI tokens never carry system administration and cannot call management
  endpoints). Such bindings are rejected with 422 `role_not_allowed`.
- The break-glass administrator (R-AUTH-014) always holds `admin` without a role binding, so
  recovery never depends on the state it recovers.
- **Bindings from the configuration (owner, [Q-AUTHZ-170](#q-authz-170--system-role-bindings-declared-in-the-configuration)).**
  `authz.role_bindings` in the system configuration file (rendered by the chart from its `config` values) declares role
  bindings, so that a GitOps repository decides which IdP group administers trove:

  ```yaml
  authz:
    role_bindings:
      - role: admin                          # one of the system roles of this table that is built
        group: gh-wds18-k8s-main-admins      # example; or user: <name> or service_account: <name>, exactly one
  ```

  - `trove migrate` (the chart's pre-install and pre-upgrade Job) applies them in one transaction: each declared binding
    exists afterwards with `source = 'configuration'`, and a configuration binding that is no longer declared is removed;
    both are audited (`authz.role.bound`, `authz.role.unbound`) with the actor `system:configuration`. No `web` or `worker` replica
    writes them.
  - A declared group that does not exist yet is created as a local group, so the binding holds before the first login
    of a member; the login provider's group sync (R-AUTH-008) then writes its memberships into that group. A declared
    user or service account that does not exist stops `trove migrate` with an error that names it.
  - A role that is not built yet, or `anonymous` / `authenticated` as principal, stops `trove migrate`
    (`role_not_allowed`), as the API refuses them.
  - The API lists configuration bindings with `source: "configuration"`; `DELETE` on one answers 409
    `role_binding_managed_by_configuration`, and the UI shows it without the remove control. Bindings made through the
    API stay as they are, beside the declared ones.
  - The rules of the API bind a declaration as they bind a request ([Q-AUTHZ-171](#q-authz-171--a-configuration-binding-follows-the-rules-of-the-api)):
    `PUT` of a binding the configuration keeps answers 200 and leaves it to the configuration; a declared binding the API
    made before is taken over (its `source` turns to `configuration`, audited with `previousSource: "api"`); a declared
    auto-join group stops `trove migrate` (`auto_join_admin_conflict`); and a removal that would leave no active user or
    service account holding `admin` while the break-glass administrator is disabled stops it (`last_admin`), naming the
    binding. A principal deleted through the API takes its configuration bindings with it like any other (R-AUTHZ-002);
    the next `trove migrate` creates a declared group again and stops for a declared user or service account.
  - Whoever can change membership of the declared IdP group administers trove: with a GitHub team behind Dex, the team's
    maintainers decide who is an administrator.
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
- `authz.role_bindings: [{role: admin, group: gh-ops}]` and no group `gh-ops`: after `trove migrate` the group exists and
  is bound to `admin`; a user whose IdP groups map to `gh-ops` logs in and can create a repository. `DELETE
  /api/v1/role-bindings/admin/group:gh-ops` answers 409. With the entry removed, the next `trove migrate` unbinds it and
  the same user's next request needs no new login to be refused (R-AUTHZ-032).

### R-AUTHZ-011 · Security administrator and auditor roles
Priority: **MVP** · Covers: IAM-006, ADMAPI-041, TOK-014

The `auditor` role is MVP (owner, [Q-AUTHZ-172](#q-authz-172--the-auditor-reads-every-repositorys-metadata-and-triages-but-downloads-nothing));
`security-admin` stays P1.

- `security-admin` manages who may do what: users, groups, service accounts, permission policies,
  CI issuers and identity mappings, token listing and revocation for any principal, and the
  authorization settings of this page. It reads repository configuration and settings but cannot
  change them, and it holds no content rights through its role.
- Escalation guards for `security-admin`:
  - role bindings are `admin`-only (`roles:write`);
  - changing a local group that is bound to any system role (its membership from either side,
    its name, description or auto-join flag) or deleting it needs `roles:write` (403 otherwise),
    so a security administrator can neither add itself to an administrator group nor rename one
    so that the identity provider's group sync fills it (Q-AUTHZ-69);
  - a user or service account that holds any system role, directly or through a group, can be
    read but not changed, disabled, enabled or deleted without `roles:write`; for a user that
    includes its identity links, which the next login would claim. Ending its sessions and
    revoking its tokens stay with `identities:write` and `tokens:revoke` (Q-AUTHZ-69);
  - a token exercises `roles:write` only when its scope holds it (R-AUTHZ-013), so a token of an
    administrator scoped to `identities:write` is refused the same way.
- `security-admin` also triages vulnerability findings (`vulnerabilities:triage`,
  [vulnerability-scanning.md](vulnerability-scanning.md) R-VULN-013, available from M4b and to this
  role when it ships). Triage needs `read` on the finding's repository as well, which the role does
  not carry, so a security administrator triages only where a policy grants it `read`. A dismissal
  lifts a block, and a block set in triage refuses a version, for every consumer of the repository
  ([H-312](vulnerability-scanning.md#h-312--a-dismissal-lifts-a-block-for-every-consumer-of-the-repository)).
- `auditor` reads everything that is not secret, instance-wide: configuration, policies, role bindings,
  identities, projects, CI mappings, token metadata (never token values), settings, the audit log and, through
  `content:metadata-read`, the metadata of every repository: package lists, versions, the format's documents (an npm
  package document, a NuGet version list or registration), search and the package pages, and from M4b the
  vulnerability findings. It downloads no artifact (a tarball, a `.nupkg`, a raw file: `KindArtifact` is refused),
  uploads, publishes, deletes and changes nothing, with one exception: from M4b it triages vulnerability findings
  instance-wide (`vulnerabilities:triage`), the security metadata of R-VULN-013. Exposure ceilings of an endpoint apply
  to it as to everyone. It can be bound to a group through `authz.role_bindings` (R-AUTHZ-010). Secrets are write-only
  for every caller anyway.
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
  `hideExistence`, `visibility`, R-AUTHZ-033) and writes upstream credentials (write-only), in every
  project, and moves repositories between projects (R-AUTHZ-026,
  [repositories-and-proxy.md](repositories-and-proxy.md#r-repo-060--move-a-repository-to-another-project) R-REPO-060). It holds no content rights through its role and
  cannot edit policies. Because content is decided on the member (R-AUTHZ-009), adding a repository
  to a virtual repository exposes nothing to anyone who could not already read it.
- `webhook-admin` manages webhook subscriptions (specified in
  [operations-and-observability.md](operations-and-observability.md)) within two limits: a
  subscription may cover only repositories named by project and key on which the webhook administrator holds
  `read`, and only content events (deploy, delete, annotate of items and packages).

Acceptance criteria:

- A `repository-admin` creates remote repository `pypi-org` in project `web` (201) and gets 404 on
  `GET /files/web/pypi-org/simple/` until a policy grants it `read`.
- A `webhook-admin` without `read` on `web/npm-secret` gets 403 when creating a subscription for
  `web/npm-secret`.

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

  There is no project resource type (TOK-011, TOK-013, P2): a project role reaches a personal
  access token through the token's `repositories` part like any grant, the reading of a project
  and its members through an element that names the project or the system item `projects:read`,
  and the Owner's administration only through `admin: true` (R-AUTHZ-026,
  [Q-AUTHZ-215](#q-authz-215--a-members-role-acts-through-its-session-and-personal-tokens-within-their-scope-and-service-accounts-are-not-members)).
  There is no per-repository configuration type: repository configuration is the system capability pair
  `repositories:read-config` and `repositories:write-config` and applies to every project;
  per-team configuration rights come from the Owner's role (R-AUTHZ-026), not from tokens.
- **Repository part (Q-AUTHZ-9).** `inherit: true` admits every repository right and, from P1,
  every build right of the subject. Otherwise each element of `repositories` has the shape of a
  policy's resource set plus `actions`, its selectors those of an instance context (`web/npm-internal`,
  `web/npm-*`, `@any-local:npm`, R-AUTHZ-004), and the scope admits an action on a coordinate if an
  element matches the coordinate and lists the action. A restricted scope without a `builds`
  element admits no build action.

  A scope element that admits `read` on a virtual repository also admits `read` on the members
  that resolution consults on behalf of that repository (Q-AUTHZ-163): the check of a member
  carries the virtual repository the client addressed (`Addressed`; the populate check of a remote
  member too, Q-AUTHZ-165), and when that repository reaches the member and an element that names
  it by its exact name (`web/npm-all`) admits `read` on it for the same package and path, the scope admits the member.
  A class selector or a key glob that covers the virtual repository does not lift (Q-AUTHZ-164).
  The member addressed directly, and every action other than `read`, are decided by the scope as
  before; the subject's own grants are evaluated first and are never widened.

  ```json
  {
    "inherit": false,
    "repositories": [
      {
        "repositories": ["web/npm-internal", "web/npm-team-*"],
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
  capability. `content:bypass` is never a `system` item. `content:metadata-read` is one, and the
  reads it allows are content reads, so the repository part admits them as it admits a grant's
  (ceiling, never grant; Q-AUTHZ-172): `{"inherit": true, "system": ["content:metadata-read"]}`
  reads the metadata of every repository, a restricted scope only of the repositories it names.
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
  `POST /api/v1/projects/web/repositories`; a token with `{"inherit": true, "admin": true}` gets 201.
- Alice (who may deploy `@web/*`) creates a token whose scope admits only `read` on
  `["web/npm-all", "@any-local:npm", "@any-remote:npm"]`. With it, `npm install` through `npm-all`
  succeeds and `npm publish` gets `E403`.
- Alice's group loses `deploy`; within the bound of R-AUTHZ-028, her token whose scope still
  admits `deploy` can no longer publish through any replica.
- Alice's token with scope `{"repositories": [{"repositories": ["web/npm-internal"], "packages":
  {"include": ["@web/*"]}, "actions": ["read"]}]}` gets `@web/button` through `npm view` against
  `npm-internal` and `E404` for `@pay/core`, which her grants would let her read. Creating a token
  whose scope element carries `"include": ["@web/*"]` at top level, or the bare key `npm-internal`,
  answers 400 `invalid_scope`.
- Dave is a `developer` of `web` and holds no grant. His personal access token with the default scope publishes to
  `web/npm-internal`; his token whose scope admits only `read` on `["web/npm-all"]` installs through `npm-all` and
  gets `E403` on `npm publish`; a service-account token never reaches the role.

### R-AUTHZ-014 · CI tokens in evaluation
Priority: **MVP** · Covers: CIOIDC-013, CIOIDC-014, TOK-015

How mappings match, how inline grants are templated and escaped, and what the issuance record
stores are specified in [gitlab-ci-oidc.md](gitlab-ci-oidc.md) (R-CI-009, R-CI-013, R-CI-014,
R-CI-020). This requirement fixes how the evaluator treats a CI token:

- **Rights.** The union of: policies whose entries name one of the token's mappings
  (`ci:<issuer>/<mapping>` or, for a project's tier, `ci:<issuer>/<projectKey>/<mapping>`; the mappings recorded at
  exchange, R-AUTHZ-002); policies naming the groups those mappings list; the frozen inline
  grants of the issuance record; and from P1 the service accounts of R-CI-024. This union is
  narrowed by the caps of R-CI-013 and, where configured, by the triggering user's own rights at
  use time (R-CI-024, P1).
- **Inline grants are resource sets.** Each rendered inline grant MUST have the shape of
  R-AUTHZ-004 and R-AUTHZ-005 (selectors, `packages` or `paths`, `actions`) plus its `ownership`
  value, so the same matcher evaluates policies, scopes and CI grants. The selectors of a system-tier grant are those
  of an instance context, those of a project tier's grant of a project context, evaluated against the project's
  repositories only (R-AUTHZ-004, R-CI-013). A backslash produced by the
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
  (R-AUTHZ-018). The token carries them, the frozen inline grants, the caps and the mappings in its cache entry
  (`api_token.ci_context`, read with the token for authentication), so the evaluator queries nothing for them
  (R-AUTHZ-015, [gitlab-ci-oidc.md](gitlab-ci-oidc.md) Q-CI-17).
- **Lifecycle.** Disabling or deleting a mapping or CI issuer revokes its tokens (R-CI-017).
  Editing a mapping leaves the frozen inline grants of issued tokens unchanged until they expire;
  policy and group grants follow live changes.
- A CI token never has a user identity, never receives auto-join groups (R-AUTHZ-002), never
  holds a system role (R-AUTHZ-010) and never a project role (R-AUTHZ-026).

Acceptance criteria:

- An inline grant `packages.include: ["@acme/{{ project_path | segment -1 }}"]`, rendered for an
  ID token of a generic OIDC issuer (R-CI-026) whose `project_path` claim is `team/ui*`, yields
  `@acme/ui\*`. It matches only the literal name `@acme/ui*`, so the token cannot publish
  `@acme/ui-kit`.
- Removing a mapping from a policy's entries makes `npm install` with an already issued token of
  that mapping fail on every replica within the bound of R-AUTHZ-028, without a new exchange.
- `POST /api/v1/authz/explain` for a CI token lists its inline grants with
  `"source": {"kind": "ci-inline", "ordinal": …}`.
- A system-tier mapping saved with the inline grant `{"repositories": ["web/npm-internal"], "packages":
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
// Begin takes the index a request decides on, once (Q-AUTHZ-159): it waits while the index is
// staler than authz.cache.max_staleness or below Trove-Authz-Min-Epoch (R-AUTHZ-028,
// R-AUTHZ-032), after making the names (project and key) of the repositories the request names
// known to it (R-ARCH-011 step 2). An epoch the database never reached is answered 412; a stale
// index 503 authz_stale.
func Begin(ctx context.Context, sub Subject, names ...qname.Repo) (*Index, error) { /* index.ForRequest */ }

// DecideOn runs for every coordinate of a request on the index Begin returned; the request
// proceeds only if every coordinate is allowed. The resolver of R-REPO-019 calls MemberUsable
// per member. Every other per-request question has the same form (ReadableOn, ExposedOn,
// PathsAllowedOn, RequireOn, HoldsOn, CheckOn), so one request is answered on one index.
func DecideOn(ctx context.Context, idx *Index, sub Subject, c Coordinate) Decision {
    if c.Repo.Class == Virtual {
        if !Allowed(sub, idx, Read, c.RepositoryLevel()) {
            return Deny(ReasonVirtualGate)                          // answered 401 to the anonymous subject
        }
        if sub.Anonymous && !idx.PublicThroughout(c.Repo) {         // every member public, recursively (rule 6)
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
    // Ownership (R-AUTHZ-018) reads rows, so it is not decided here: the writer runs
    // OwnershipCheckTx in its commit transaction (Q-AUTHZ-154).
    return Allow()
}

// OwnershipCheckTx runs in the writer's commit transaction, after DecideOn allowed the actions and
// under the writer's package row lock; w says whether the write publishes a version and whether
// the package held one before (R-AUTHZ-018 rules 1 to 3, 5 and 6; Q-AUTHZ-154).
func OwnershipCheckTx(tx Tx, sub Subject, idx *Index, c Coordinate, w Write) Decision {
    r := c.Repo.WriteTarget()                                       // a virtual repository's deploy target
    if r.Class != Local || !c.HasName || !(r.PackageOwnership == Enforce || bindsProject(sub, idx, c)) {
        return Allow()
    }
    state, owners := tx.LockClaim(r.Domain(), c.PackageName)        // FOR UPDATE; no row is "unclaimed"
    switch {
    case sub.Capabilities().Has(ContentBypass):
    case len(owners) > 0 && (sub.OwnsAny(owners) || Allowed(sub, idx, Manage, c)):
    case len(owners) == 0 && state != Claimed && w.FirstVersion:   // rule 1: free to the holder of the action
    case len(owners) == 0 && Allowed(sub, idx, Manage, c):         // rules 3 and 6
    default:
        return Deny(ReasonOwnership)                                // 403 package_owned_by_other_owner
    }
    if w.Publish && len(owners) == 0 {
        return tx.Claim(r.Domain(), c.PackageName, ClaimOwners(sub, idx, c)) // rules 2 and 5
    }
    return Allow()
}

func Allowed(sub Subject, idx *Index, a Action, c Coordinate) bool {
    if !idx.Exposure(sub.Endpoint).Admits(a, c.Addressed, c) {      // endpoints.md R-EP-013: every subject,
        return false                                                // decided on the addressed repository
    }
    if sub.Capabilities().Has(ContentBypass) {                      // roles as admitted by the scope
        return true
    }
    if leveled(sub, idx, a, c) {                                    // visibility levels, R-AUTHZ-035: public is
        return true                                                 // read without a scope, internal within it
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
    if sub.CarriesRoles {                                           // UI session or personal token, Q-AUTHZ-215
        if idx.RoleOf(sub, c.Repo.Project).Actions().Has(a) {       // highest over the user and its groups,
            return true                                             // R-AUTHZ-026; source role:<projectKey>:<role>
        }
    }
    for _, p := range sub.Principals {                              // user, groups, sa, ci mappings
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
        return MayPopulate(sub, idx, m, mc)                         // otherwise the cache only
    }
    return true
}

// MayPopulate is the populate check of R-AUTHZ-008 (Q-AUTHZ-153).
func MayPopulate(sub Subject, idx *Index, m Repo, mc Coordinate) bool {
    if sub.Credential == SessionCookie {                            // never through the session cookie
        return false
    }
    if sub.Endpoint != "" && !sub.CacheFill {                       // the endpoint's cache_fill, endpoints.md R-EP-019
        return false
    }
    return Allowed(sub, idx, Read, mc) && Allowed(sub, idx, m.PopulateAction, mc)
}
```

- Writes MUST be evaluated again when they commit (after a long upload), against the grant index
  that the committing replica holds at that moment (R-AUTHZ-028) and the ownership rows read in
  the commit transaction. A refusal at commit discards the upload and answers 403. The re-check
  waits for a stale index like any decision; it never holds the commit transaction open while it
  waits (R-ARCH-019). `/files/` decides a deploy again after its body (Q-ARCH-208); a format's
  writes are decided again by the host before their transaction opens, and the ownership check in
  that transaction decides on the new index ([architecture.md](architecture.md) Q-ARCH-225).
- Every request is decided on its own, on whichever replica receives it, and no decision is kept
  in a replica's memory for a later request (INV-18). In a protocol that spans several requests
  (chunked or resumable uploads, P1, [storage.md](storage.md) R-STOR-011), each request is
  authorized when it arrives, and the request that commits runs the commit-time re-check on its
  own replica.
- `reasonFor` returns `deny.exposure` when the endpoint's exposure refuses a read,
  `deny.endpoint_read_only` when it refuses a write ([endpoints.md](endpoints.md) R-EP-013),
  `deny.scope` when a grant would allow the action but a ceiling does not,
  `deny.triggering-user` when only the triggering-user bound refuses, and `deny.no-grant`
  otherwise. "Grant" includes the actions of a project role (R-AUTHZ-026).
- The subject carries the endpoint the request arrived on (`Subject.Endpoint`, set by the one
  conversion from the listener's endpoint, never by a caller; [endpoints.md](endpoints.md) Q-EP-20),
  with that endpoint's `cache_fill` (`Subject.CacheFill`, Q-AUTHZ-153), and
  the coordinate the addressed repository, the one the client named; `WithRepo` changes neither,
  so exposure is decided on the addressed repository while grants are decided per member
  ([endpoints.md](endpoints.md) Q-EP-11). A decision keeps its endpoint's ceiling on whatever
  context it runs, and a subject without an endpoint (a job, a command) has no ceiling. The exposure rules are part of the
  compiled index and follow its staleness rules (R-AUTHZ-028). On an endpoint with `exposure: all`
  the first check of `Allowed` admits everything, so every decision on the default endpoint stays as
  this page describes it.
- The shadowing check on the first version of a name (R-AUTHZ-007, R-REPO-048) is not part of
  `Decide`, because it may probe an upstream: it runs after `Decide` allowed the write and before
  the commit, without a database transaction held open. It asks `Allowed(sub, idx, Manage, c)`
  for the local repository, so a token's scope and the CI caps bound it like any action, and
  refuses with reason `deny.shadowing`.
- Performance target: with 10,000 policies, 200 groups in a principal set, 1,000 repositories and 100 projects,
  one `Decide` call SHOULD take at most 1 ms at the 99th percentile on one CPU core, measured by
  `trove_authz_evaluation_seconds`.

Worked trace (repositories and policies of the team example in
[Worked examples](#worked-examples), all in project `web`; Alice is in `developers` and `team-web`, Bob in
`developers`, Carol in no group, Dave in no group and a `reader` of `web`; every level `private`):

| # | Subject | Request | Steps | Result |
|---|---|---|---|---|
| 1 | Alice | `npm view @web/button` via `npm-all` | gate: `read` on `npm-all` from `npm-consumers`; member `npm-internal`: `read` from `npm-consumers`; found | 200 |
| 2 | Alice | `npm publish @web/button@1.3.0` via `npm-all` | gate passes; target `npm-internal`; `deploy` on `@web/button` from `team-web-publish`; owners `{group:team-web}` include one of Alice's groups | success |
| 3 | Alice | `npm publish @pay/core` via `npm-all` | gate passes; `deploy` on `@pay/core`: no grant | 403 |
| 4 | Bob | `npm publish @web/button@1.3.1` | `deploy`: no grant | 403 |
| 5 | Carol | `npm view left-pad` via `npm-all` | gate: no `read` on `npm-all` | 404 |
| 6 | no credential | `npm view left-pad` via `npm-all` | the artifacts of `npm-all` are not `public` | 401 |
| 7 | Alice, read-only token | `npm publish @web/button@1.3.2` | grant allows `deploy`, the scope does not (`deny.scope`) | 403 |
| 8 | Alice | `npm view left-pad` via `npm-all`, not cached | `npm-internal`: not found; `npmjs`: `read` and `cacheFillAction: read` pass; fetched | 200 |
| 9 | Dave | `npm view @web/button` via `npm-all` | gate: `read` on `npm-all` from `role:web:reader`; member `npm-internal`: `read` from the role; found | 200 |
| 10 | Dave | `npm publish @web/button@1.3.3` via `npm-all` | gate passes; target `npm-internal`; `deploy`: no grant, and the role `reader` holds only `read` | 403 |

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
| No credential, and no `public` level admits what the request needs (reads and writes alike) | 401 with the challenge of R-AUTH-002 |
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
  MUST run before the existence lookup of the item or package. The one lookup that precedes the
  decision is the repository lookup of [architecture.md](architecture.md) R-ARCH-011 step 2, which a
  repository created through another replica needs (R-API-052); what this page promises is the
  bytes, and the response time of the first request for a name (project and key) that a replica has never seen is
  not promised (Q-AUTHZ-152, H-155 in [file-transfer.md](../security/file-transfer.md)).
- A refused write gets 403 whether or not the target exists, and the message names only the action
  and the coordinate the caller sent. Existence matters only once the caller holds `deploy` (to
  tell a new upload from an overwrite); a caller with `deploy` learns of existence through its own
  uploads anyway.
- A project the subject is no member of, and in which it may read no repository, answers exactly like a project that
  does not exist: on protocol and `/files/` addresses like a missing repository, and with 404 below
  `/api/v1/projects/{projectKey}` (R-AUTHZ-026). The project segment of an address never tells whether the project
  exists.
- Listings, search results and their counts contain only items the subject may read.
- On the management API, capability checks run before any lookup, so their 403 does not depend on
  whether the addressed object exists. Reads of a repository and its content
  (`/api/v1/projects/{projectKey}/repositories/{repoKey}` and below) follow the table above. Readers without
  `repositories:read-config` who are not an Owner of the project get the reduced repository document of
  [repositories-and-proxy.md](repositories-and-proxy.md).
- Error bodies follow [api-and-ui.md](api-and-ui.md) on the management API and the format's
  conventions on protocol endpoints. Audit events carry the reason codes of R-AUTHZ-029.

Acceptance criteria:

- Carol (no grants, no membership) gets byte-identical 404 responses for `GET
  /api/npm/web/npm-internal/@web%2fbutton` (exists), `GET /api/npm/web/npm-internal/@web%2fnope`
  (missing) and `GET /api/npm/nope/npm-internal/@web%2fbutton` (no such project).
- Carol's `npm publish` of `@web/nope` to `npm-internal` gets `E403`, as does her publish of the
  existing `@web/button`.
- `dotnet restore` against `nuget-internal` without a credential gets 401 with a `Basic`
  challenge, retries with the credential from `nuget.config` and succeeds.
- `npm publish` without a credential against a repository whose artifacts are `public` gets
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
  field (Q-AUTHZ-2); `off` is an explicit choice per repository. The default is a fact of the format
  in the format catalog, and it is stored with the document: a replace that omits the field, or a
  merge patch that sets it to `null`, reverts it to the default like every omitted setting
  (R-REPO-011, Q-AUTHZ-155).
- Independently of that setting, ownership is checked for every write that a CI inline grant with
  `ownership: bind_project` allows (R-CI-015). Writing inline grants carry `bind_project` unless
  they say otherwise (R-AUTHZ-014, Q-AUTHZ-6).
- Ownership applies only to coordinates with a package name. A write to a coordinate without one
  (a raw file outside any package, a repository of a format without package names) passes the
  ownership check, also under a `bind_project` grant.
- A delete through `/files/` or the items API ([storage.md](storage.md) R-STOR-018) is a mutating
  operation on every package whose files it removes: a file, a package folder or a whole scope. The
  question is asked for each of those packages, in name order under its package row lock, in the
  transaction that removes the files or that queues the job removing them, before anything is
  removed; one refusal refuses the whole delete (Q-AUTHZ-161). The job asks it again for the files
  of each of its batches, in the batch's transaction, on the caller as it stands then; a refusal
  there fails the job and keeps what is left ([storage.md](storage.md) Q-STOR-190).
- Ownership is recorded per ownership domain and normalized package name. In the MVP the domain is
  the repository (`repo:<repositories.id>`); P1 adds shared domains (R-AUTHZ-020), and until then
  `ownershipDomain` is refused with 422 `not_applicable` rather than ignored (Q-AUTHZ-155). Remote
  and virtual repositories have no ownership; writes through a virtual repository are checked on
  its deploy target.
- The check reads rows, so it runs in the writer's commit transaction, after the actions were
  decided and under the writer's package row lock, never in the decision of the actions
  (R-AUTHZ-015 `OwnershipCheckTx`, Q-AUTHZ-154). The writer states whether the operation publishes
  a version and whether the package held a stored version before it.

Owner identities:

| Owner | Who counts as owner |
|---|---|
| `user:<name>` | That user |
| `group:<name>` | Every subject whose principal set contains the group |
| `sa:<name>`, `sa:<projectKey>/<name>` | That service account; a project's account owns names only in its project's repositories (R-AUTHZ-026) |
| `ci:<issuer>/<mapping>`, `ci:<issuer>/<projectKey>/<mapping>` | Every CI token issued through that mapping |
| `ci:<issuer>:project:<project_id>` | Every CI token whose issuance record names that issuer and GitLab project ID (R-CI-015) |

A subject's owner identities are its principal set plus, for a CI token, its project identity.

Rules:

1. **Mutating operations** (those marked "Ownership" in R-AUTHZ-007) on a package name in a
   checked domain require, besides the action, one of:
   - the name is unclaimed (no claim, or released) and has no stored version; a publish claims it
     (rule 2), any other operation leaves it unclaimed (Q-AUTHZ-154);
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
   - a project role that allowed `deploy` (R-AUTHZ-026) makes the member through which the role came an owner: the
     group whose membership carries the role, or the user for its own membership; a project role is never an owner
     identity of its own.
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
   owners in the same transaction. The check reads the claim row with `FOR UPDATE` first, so the
   check and the claim of one write and every owner change of the name (R-AUTHZ-019) run one after
   the other; a name without a row has nothing to lock, and the primary key decides
   (Q-AUTHZ-154). Under `READ COMMITTED` the insert waits for the concurrent claim
   and then does nothing, and the following read sees that claim, because each statement takes a
   new snapshot ([transaction isolation](https://www.postgresql.org/docs/current/transaction-iso.html#XACT-READ-COMMITTED)).
   The publish therefore needs no retry. No advisory lock, no Valkey lock and no session state is
   needed, so the claim works through a transaction-mode pooler
   ([architecture.md](architecture.md) R-ARCH-019).
6. **Principal deletion** removes the principal's owner entries. A name whose last owner
   disappears this way stays claimed and falls under rule 3, whether or not it holds versions: every
   mutating operation needs `manage`, and a publish by a `manage` holder claims it again
   (R-AUTHZ-031 "Released names stay closed").
7. **Display.** CI project owners keep `project_path` as a display value, updated on every publish
   (R-CI-015).
8. **Repository deletion** removes the ownership rows of the repository's domain in the transaction
   that deletes the repository row ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-013,
   Q-AUTHZ-156); the domain is text and carries no foreign key.

Data model:

```sql
CREATE TABLE package_ownership (
  domain        text NOT NULL             -- 'repo:<repositories.id>' or 'domain:<name>' (R-AUTHZ-020);
    CHECK (domain ~ '^(repo:[0-9]+|domain:.+)$'),  -- no foreign key: rule 8 removes a repository's rows
  format        text NOT NULL,
  package_name  text NOT NULL,            -- normalized name (R-AUTHZ-005)
  state         text NOT NULL CHECK (state IN ('claimed', 'released')),
  claimed_at    timestamptz NOT NULL DEFAULT now(),
  claim_event   uuid NULL,                -- the claim's ID, carried by its audit event (R-AUTHZ-029, Q-AUTHZ-158)
  PRIMARY KEY (domain, format, package_name)
);

CREATE TABLE package_owner (
  id            uuid PRIMARY KEY,
  domain        text NOT NULL,
  format        text NOT NULL,
  package_name  text NOT NULL,
  owner_kind    text NOT NULL CHECK (owner_kind IN ('principal', 'ci_project')),
  principal_id  uuid NULL REFERENCES principal (id) ON DELETE CASCADE,  -- user, group, sa, ci_mapping
  ci_issuer_id  uuid NULL,                                            -- ci_project only; the foreign key to
                                                                      -- ci_issuers (ON DELETE CASCADE) is added
                                                                      -- NOT VALID and validated in M4 (Q-ARCH-191)
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
CREATE INDEX package_owner_principal ON package_owner (principal_id) WHERE principal_id IS NOT NULL;
CREATE INDEX package_owner_added_by  ON package_owner (added_by) WHERE added_by IS NOT NULL;
```

Acceptance criteria:

- GitLab projects 4711 (`acme/web/frontend`) and 4712 (`acme/mobile/frontend`) both render the
  allowed name `@acme/frontend` (see [Worked examples](#worked-examples)). Project 4711 publishes
  1.0.0 first; the pipeline of 4712 then runs `npm publish` of 1.0.1 and gets `E403` with
  `package_owned_by_other_owner`; 4711 publishes 1.0.1 successfully.
- Alice publishes `@web/new-lib` through the `team-web` grant; Dave, also in `team-web`, publishes
  the next version successfully. Erin, a `developer` of `web` through her own membership and in no group, publishes
  `@web/erin-lib` and becomes its owner as `user:erin`.
- `@legacy/util` was imported with versions and no owner. Alice, holding `deploy` but not
  `manage`, gets 403 on `npm publish`; `lead-web`, holding `deploy` and `manage`, succeeds, after
  which the name is owned by rule 2.
- Two `npm publish` runs of one new name by different, non-co-owning principals, started at the
  same moment on different replicas: exactly one succeeds.
- A local npm repository and a local NuGet repository created without `packageOwnership` both read
  back `"local": {"packageOwnership": "enforce", …}`.
- `npm-all` = [`npm-internal`, `npmjs`], all of project `web`; the stub upstream of `npmjs` serves `lodash`. Service
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
| `GET /api/v1/projects/{projectKey}/repositories/{repoKey}/packages/{packageName}/owners` | Read state and owners | 200; 404 per R-AUTHZ-016 | `read` on the coordinate |
| `POST /api/v1/projects/{projectKey}/repositories/{repoKey}/packages/{packageName}/owners` | Add an owner. On a name that is unclaimed and has no version, this pre-assigns it: the name is claimed for the given owner before anything is published. | 201; 403; 409 `already_exists` (already an owner); 422 `unknown_principal` | an owner, `manage` on the coordinate, or `content:bypass`; pre-assigning needs `manage` or `content:bypass` |
| `DELETE /api/v1/projects/{projectKey}/repositories/{repoKey}/packages/{packageName}/owners/{owner}` | Remove an owner | 204; 403; 404; 409 `last_owner` unless `?release=true` | as for adding; `release=true` needs `manage` or `content:bypass` |
| `GET /api/v1/package-owners?owner={owner}&repository={projectKey}/{repoKey}` | Names owned by an identity, for offboarding and transfers: the entries that name the identity itself, not those of its groups | 200, cursor paging per [api-and-ui.md](api-and-ui.md) | `policies:read`, or the caller is that owner (directly or as group member); such a caller sees only names of repositories it may read (R-AUTHZ-016) |

```json
{
  "repository": "web/npm-internal",
  "domain": "web/npm-internal",
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
  without `release=true` is refused. With `release=true` the name is `released`: it is unclaimed
  again, so rule 1 and rule 3 of R-AUTHZ-018 decide its next publish. The owners are read under the
  claim row's lock, so two removals of the last two owners, on one replica or on two, run one after
  the other and the second answers 409 `last_owner`.
- A name with no owner left to ask (unclaimed, released, or claimed after its last owner was
  deleted) takes an owner only from `manage` or `content:bypass`; on an unclaimed name with
  versions this claims it with the reason `added`, without versions with `pre-assigned`.
- The owner identities that can be added are those of R-AUTHZ-018's table: `user:`, `group:`,
  `sa:` (with or without a project), `ci:<issuer>/<mapping>` and `ci:<issuer>/<projectKey>/<mapping>`, and from M4
  `ci:<issuer>:project:<id>` (R-CI-015); a service account of another project than the repository's is 422
  `principal_outside_project` (R-AUTHZ-026); every other
  reference is 422 `unknown_principal` (Q-AUTHZ-157), and `anonymous` or `authenticated` is 422 `principal_not_allowed`
  (Q-AUTHZ-279).
  The permission is decided before the reference is looked up.
- Adding and removing are writes: a caller who may not change the owners gets 403 whether or not the
  repository or the name exists (R-AUTHZ-016). Through a session they need recent authentication
  ([api-and-ui.md](api-and-ui.md) R-API-007), like a policy change: adding an owner widens who may
  publish a name. An added owner answers 201 with `Location` of the name's owners, the resource a GET
  then shows it in. Where ownership does not exist (a remote or virtual
  repository, a format without package names) a caller holding `manage` gets 404. Reading the owners
  is a read: 404 for a caller without `read`, byte-identical to a missing name; an unclaimed name
  reads back `"state": "unclaimed"` with no owners.
- CI tokens never change owners. Adding, removing and releasing answer 403 `ci_token_restricted`
  for a CI token, also when its project identity is an owner. A pipeline runs the install scripts
  of its dependencies, so a CI token must not be able to hand a package to another project; a
  transfer needs a person or a service account.
- **An owner's narrowed token.** Being an owner is a fact about the principal, so a caller that
  changes owners as an owner through a token whose scope does not inherit its rights (`inherit:
  false`, also with `admin: true`) counts as an owner only where an element of the scope lists
  `manage`, the action of package ownership (R-AUTHZ-006), on the repository and the name; elsewhere
  it gets the 403 of any refused owner change. A session and an inheriting token keep the owner path.
  `manage` and `content:bypass` are decided by the decision point with the scope as before (Q-AUTHZ-240).
- npm's maintainer commands (PKG-NPM-022, P2) may later map onto these operations.

Acceptance criteria:

```sh
curl -sS -H "Authorization: Bearer $TROVE_TOKEN" \
  https://trove.example.com/api/v1/projects/web/repositories/npm-internal/packages/@acme%2Ffrontend/owners
# 200, owners as above

curl -sS -X POST -H "Authorization: Bearer $LEAD_TOKEN" -H 'Content-Type: application/json' \
  -d '{"owner":"group:team-web"}' \
  https://trove.example.com/api/v1/projects/web/repositories/npm-internal/packages/@acme%2Fnot-yet/owners
# 201: lead-web (manage on npm-internal) pre-assigns a name nobody has published yet
```

- Removing the only owner without `release=true` answers 409 `last_owner`.
- A member of `team-web`, an owner of `@acme/scoped`, adds an owner through a token with the scope
  `{"repositories": [{"repositories": ["web/npm-internal"], "packages": {"include": ["@acme/scoped"]},
  "actions": ["manage"]}]}` (201); through a token whose scope lists only `deploy` there it gets 403
  and the owners stay unchanged; through a session or a token with `inherit: true` it adds one (201).
- A CI token of project 4711, owner of `@acme/frontend`, gets 403 `ci_token_restricted` on
  `POST …/packages/@acme%2Ffrontend/owners` with `{"owner":"ci:gitlab-internal:project:4712"}`, and
  the owners stay unchanged.
- After the pre-assignment above, a CI job of any project whose grant pattern covers
  `@acme/not-yet` gets 403 `package_owned_by_other_owner` on `npm publish`.

### R-AUTHZ-020 · Shared ownership domains
Priority: **P1** · Covers: trove-only

- A local repository MAY set `ownershipDomain: <name>`. All local repositories of the same project and format
  with the same domain name share one set of ownership records (`domain = 'domain:<projectKey>/<name>'`); no domain
  spans two projects, because no repository is shared (Q-AUTHZ-206).
  Without the setting, the repository is its own domain.
- Joining a repository into a domain whose records conflict with the repository's own (the same
  name owned by different owners) is refused with 409 `ownership_conflict`, listing the conflicting
  names; an administrator resolves them through R-AUTHZ-019 first.
- Recommended use: repositories that hold the same packages at different maturity (for example
  `npm-dev` and `npm-release`) share a domain, so a name owned in one cannot be claimed by someone
  else in the other.
- Changing the setting requires `repositories:write-config`.

Acceptance criteria: `npm-dev` and `npm-release` of project `web` share domain `acme-npm`. `@acme/ui` is owned by
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
    "repositories": ["web/npm-internal"],
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

- `name` matches `^[a-z0-9][a-z0-9._-]{0,127}$` and is unique among the policies of no project, or among the policies
  of its project: the same name may exist in two projects and once among the global policies
  ([Q-AUTHZ-209](#q-authz-209--a-projects-policies-and-service-accounts-are-named-within-the-project-administrators-keep-instance-wide-ones)).
  `description` has at most 1024 characters; `resourceType` is `repository` (MVP) or `build` (P1, R-AUTHZ-027);
  `project` is `null` for a global policy and the key of its project for a project policy (R-AUTHZ-026). It is
  read-only: the route a policy is created under decides it (`/api/v1/permission-policies` or
  `/api/v1/projects/{projectKey}/policies`), and it is fixed at creation; a body that names another project is 422
  `immutable` ([Q-AUTHZ-186](#q-authz-186--the-project-of-a-policy-is-fixed-at-creation)). The selectors of a global
  policy are those of an instance context (`web/npm-internal`, `*/npm-*`, `@any-local:npm`), those of a project
  policy those of a project context (bare keys, R-AUTHZ-004). `entries` may be empty (a policy can be prepared before principals are added) and
  holds at most one entry per principal. `inert`, `warnings`, `version` and the timestamps are
  read-only.
- Each policy addresses exactly one resource type, so the catalog's per-section operations
  (ADMAPI-024) are ordinary operations on a policy. Per-repository patterns are separate policies.
- CI mappings reference policies by name (R-CI-013); the binding is the policy entry of the mapping,
  so editing either side changes the same row.
- Updates are guarded by optimistic concurrency: `GET` returns
  `ETag: "<version>"`, and `PUT` and `PATCH` require `If-Match`
  ([RFC 9110 §13.1.1](https://www.rfc-editor.org/rfc/rfc9110#section-13.1.1)), answering 428
  `precondition_required` without it ([RFC 6585 §3](https://www.rfc-editor.org/rfc/rfc6585#section-3))
  and 412 `precondition_failed` on a version mismatch. `If-Match: *` is the deliberate blind write
  ([api-and-ui.md](api-and-ui.md) R-API-012, Q-API-2). A `PATCH` is applied to the current
  representation and written like a `PUT` conditional on the version it was computed from, so
  even `If-Match: *` never merges over a concurrent change (it answers 412 and the client retries).
- Every successful write notifies the `authz` change area in its transaction, which increments the
  authorization epoch (R-AUTHZ-028). It also emits an audit event with before and after
  (R-AUTHZ-029). The response carries the new epoch in the header `Trove-Authz-Epoch`. A later
  request names that epoch in `Trove-Authz-Min-Epoch` to see the change on whichever replica
  answers it (R-AUTHZ-032).
- Malformed JSON answers 400. Semantic validation answers 422 with one of: `unknown_principal`,
  `unknown_repository`, `unknown_format`, `invalid_pattern`, `invalid_action`,
  `duplicate_principal`, `principal_not_allowed` (an entry for `anonymous` or `authenticated`, Q-AUTHZ-279),
  `virtual_action_not_applicable`, `action_not_allowed`; `invalid_pattern` also for a
  bare key in a global policy and for a selector with a `/` in a project policy (R-AUTHZ-004), and for a path pattern
  with an upper-case letter in a policy that covers a repository of a format with lower-case paths (NuGet, Q-AUTHZ-253);
  for a project policy also `principal_outside_project` (R-AUTHZ-026). Warnings do not block saving:
  `path_patterns_on_package_format` (any path pattern, include or exclude, over a repository of a format with package
  names, R-AUTHZ-005) and `selector_matches_nothing`.
- The declarative bootstrap file (configuration as code, P1,
  [operations-and-observability.md](operations-and-observability.md)) uses the same
  representation.

| Method and path | Purpose | Status | Required |
|---|---|---|---|
| `GET /api/v1/permission-policies` | List every policy, global and of every project; each item carries `project`. Filters: `principal=<ref>` (with `expandGroups=true`, the default, also policies granting to the principal's groups), `repository=<projectKey>/<repoKey>` (policies whose selectors cover it), `project=<key>`; cursor paging per [api-and-ui.md](api-and-ui.md) | 200; 422 unknown filter value | `policies:read`; P1: delegated managers the policies they manage |
| `POST /api/v1/permission-policies` | Create a policy of no project | 201 with `Location`; 400; 409 name exists among the global policies; 422 | `policies:write` |
| `GET /api/v1/permission-policies/{name}` | Read the policy of no project of that name | 200 with `ETag`; 404 | as for list |
| `PUT /api/v1/permission-policies/{name}` | Replace (the name is immutable) | 200; 400; 404; 412; 422; 428 | as for create; P1: delegated managers per R-AUTHZ-025 |
| `PATCH /api/v1/permission-policies/{name}` | Change members by JSON merge patch (R-API-013); an array such as `entries` is replaced as a whole (Q-AUTHZ-26) | as for `PUT` | as for `PUT` |
| `DELETE /api/v1/permission-policies/{name}` | Delete (`If-Match` optional) | 204; 404; 412 | as for create |
| `GET /api/v1/projects/{projectKey}/policies` | List the project's policies. Filters: `principal=<ref>`, `repository=<repoKey>`; cursor paging | 200; 404 | `policies:read` or an Owner of the project (R-AUTHZ-026) |
| `POST /api/v1/projects/{projectKey}/policies` | Create a policy of the project | 201 with `Location`; 400; 404; 409 name exists in the project; 422 | `policies:write` or an Owner of the project |
| `GET`, `PUT`, `PATCH`, `DELETE /api/v1/projects/{projectKey}/policies/{name}` | Read, replace, merge patch and delete a policy of the project, as for a global policy | as for a global policy | as for the project's list (read) and create (writes); P1: delegated managers per R-AUTHZ-025 |
| `POST /api/v1/authz/validate-policy` | Validate without storing; returns errors, warnings and the repositories the selectors currently cover; a policy of a project names it in `project` | 200 | `policies:read`; an Owner of the named project for a policy of it |

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
- `POST /api/v1/permission-policies` with the selector `npm-internal` answers 422 `invalid_pattern`; with
  `web/npm-internal` it answers 201. A policy `team-web-publish` created with
  `POST /api/v1/projects/web/policies` and the selector `npm-internal` exists beside it, reads back with
  `"project": "web"`, and `GET /api/v1/permission-policies?project=web` lists it and not the global one.

### R-AUTHZ-022 · Role binding API
Priority: **MVP** · Covers: IAM-002, IAM-016, ADMAPI-001

| Method and path | Purpose | Status | Required |
|---|---|---|---|
| `GET /api/v1/roles` | The fixed roles and their capabilities (R-AUTHZ-010) | 200 | any authenticated subject |
| `GET /api/v1/role-bindings?role=&principal=` | List bindings | 200 | `roles:read` |
| `PUT /api/v1/role-bindings/{role}/{principalRef}` | Bind (idempotent) | 201 new, 200 existing (a binding from the configuration stays one, Q-AUTHZ-171); 409 `auto_join_admin_conflict`; 422 `role_not_allowed`, unknown role or principal | `roles:write` |
| `DELETE /api/v1/role-bindings/{role}/{principalRef}` | Unbind | 204; 404; 409 `last_admin`; 409 `role_binding_managed_by_configuration` for a binding from the configuration (R-AUTHZ-010) | `roles:write` |
| `GET /api/v1/authz/me` | The caller's effective capabilities, scope summary, principal set, whether it is internal (`internal`, R-AUTHZ-030) and its project memberships with their roles (`projects`: `[{"key": "web", "role": "owner"}]`, R-AUTHZ-026) | 200 | any subject |

- `principalRef` is percent-encoded (`group:platform-admins`, `sa:ci-bot`, `sa:web%2Fpartner`).
- Binding any role to an auto-join group is refused with 409 `auto_join_admin_conflict`; the
  identity page refuses the reverse (R-AUTH-019).
- A change that would leave no active user or service account holding `admin`, directly or
  through a group, is refused with 409 `last_admin` while the break-glass administrator is
  disabled: unbinding `admin`, disabling or deleting a holder, deleting its group, removing it
  from its group. A binding on an empty group or on a disabled user keeps nobody in charge and
  does not count. These changes take one lock first, so two of them on different replicas run
  one after the other and the second sees what the first left (Q-AUTHZ-70).
- `isAdmin` and `adminVia` on user records (R-AUTH-016) are derived from these bindings.

Data model:

```sql
CREATE TABLE authz_role_binding (
  role          text NOT NULL CHECK (role IN
                  ('admin', 'security-admin', 'auditor', 'repository-admin', 'webhook-admin')),
  principal_id  uuid NOT NULL REFERENCES principal (id) ON DELETE CASCADE,
  granted_by    uuid NULL REFERENCES principal (id) ON DELETE SET NULL,
  granted_at    timestamptz NOT NULL DEFAULT now(),
  source        text NOT NULL DEFAULT 'api' CHECK (source IN ('api', 'configuration')),  -- Q-AUTHZ-170
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
    { "repository": "web/npm-internal", "package": "@web/button", "action": "deploy" },
    { "repository": "web/npm-internal", "path": "@web/button/-/button-1.2.0.tgz", "action": "delete" },
    { "repository": "web/npm-all", "action": "read" }
  ]
}
```

```json
{ "results": [ { "allowed": true }, { "allowed": false }, { "allowed": true } ], "epoch": 1842 }
```

- `repository` is the qualified name `<projectKey>/<repoKey>`. A check on a repository the caller cannot see answers
  `allowed: false`, exactly as for a missing repository or project. A check that names a package and no path asks about
  the package as a whole and is decided on the path of its package document, as a listing row is (R-AUTHZ-005,
  Q-AUTHZ-250). A check that names a path is decided on the coordinate `/files/` decides the path on: the package and the
  version the repository's format gives it (R-EP-014). It answers `allowed: false` whatever the grants say when the path
  is not canonical, or when it also names a package that is not the package of its path (Q-AUTHZ-252). Checks of
  package-mutating actions include the ownership rule; the ownership rows of
  all checks of one call are read with one query, so a call costs at most one database round trip
  however many checks it holds.
- Any authenticated subject may call it; anonymous callers get 401.
- The UI hides controls whose check fails; the server enforces every operation regardless.

Acceptance criteria: for Alice the example returns `[true, false, true]`; for Carol it returns
`[false, false, false]`, also when `web/npm-internal` is replaced by a name whose key or project does not exist.

### R-AUTHZ-024 · Effective permissions and decision explanation
Priority: **P1** · Covers: IAM-030, ART-031

- `GET /api/v1/authz/effective?repository={projectKey}/{repoKey}&package={name}` (or `&path={path}`) lists every
  principal that holds actions on the coordinate, with the actions and the policies that grant
  them; the members of the repository's project are listed with their role as the source
  (`role:<projectKey>:<role>`, R-AUTHZ-026). CI mappings whose inline grant templates could render to the coordinate are listed with
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
    "repository": "web/npm-internal", "class": "local", "format": "npm",
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
  token values and no claims beyond the issuance record (R-CI-020). A step that a project role allowed names
  `"source": {"kind": "role", "project": "web", "role": "developer"}`.

Acceptance criteria: the explanation of trace row 7 of R-AUTHZ-015 names reason `deny.scope`. The
effective permissions of `web/npm-internal` / `@web/button` list `group:team-web` with `annotate`,
`deploy` and `read` from `team-web-publish`, and a `developer` of `web` with `annotate`, `deploy` and `read` from
`role:web:developer`.

### R-AUTHZ-025 · Delegated policy management
Priority: **P1** · Covers: IAM-029

- A principal that holds `manage` in a policy's entries is a delegated manager of that policy. It
  may read the policy and change its entries through `PUT /api/v1/permission-policies/{name}`, or
  `PUT /api/v1/projects/{projectKey}/policies/{name}` for a policy of a project, provided `name`, `description`,
  `resourceType`, `project` and `resources` stay unchanged (otherwise 403).
- What a delegated manager may grant depends on `authz.delegation.mode`:

| Mode | May grant |
|---|---|
| `own-actions` | only actions it holds in this policy itself, never `manage` |
| `any-but-manage` | any action except `manage` |
| `any` | any action including `manage` |

- A delegated manager MUST NOT change an entry whose principal is in its own principal set (403).
  It can neither widen its own rights, also not indirectly through one of its groups, nor lock
  itself out.
- A delegated manager MUST NOT add `anonymous` or `authenticated`: nobody can (422 `principal_not_allowed`, Q-AUTHZ-279).
  Opening content to every internal user or to everyone is a visibility level (R-AUTHZ-033), and the catalog's finer control
  over anonymous grants by non-administrators (IAM-034) stays P2.
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
adding `sa:sa-web-release` with `["delete"]`; the same holds for a policy of project `web` and its account
`sa:web/release`.

### R-AUTHZ-026 · Projects for delegated administration
Priority: **MVP** · Covers: IAM-051, IAM-052, IAM-057

The owner wants projects in the beta (2026-11-02), with project service accounts
([Q-AUTHZ-169](#q-authz-169--a-technical-account-for-a-project-belongs-to-the-project)); BETA.md places them in phase 5a,
before GitLab CI OIDC. In his review of 2026-11-06 he decided the model this requirement states: every repository belongs to
exactly one project and none is shared ([Q-AUTHZ-206](#q-authz-206--every-repository-belongs-to-exactly-one-project)), it is
addressed inside its project ([Q-REPO-212](repositories-and-proxy.md#q-repo-212--a-repository-is-addressed-inside-its-project-and-no-repository-is-shared)),
users and groups are members of a project with one of four roles
([Q-AUTHZ-207](#q-authz-207--a-user-or-group-is-a-member-of-a-project-with-a-role-and-the-role-grants-the-projects-repositories),
[Q-AUTHZ-208](#q-authz-208--four-project-roles-reader-developer-maintainer-owner)), and a project's policies, service
accounts and CI mappings are named within the project
([Q-AUTHZ-209](#q-authz-209--a-projects-policies-and-service-accounts-are-named-within-the-project-administrators-keep-instance-wide-ones),
[Q-AUTHZ-214](#q-authz-214--a-projects-ci-mappings-are-named-within-the-project-like-its-policies-and-service-accounts)).

A project is the unit in which a team holds and administers its repositories, their policies and the technical accounts
of its partners without a system administrator. Delegation per policy (`manage`, R-AUTHZ-025) and per CI issuer
(R-CI-023) stay P1. The catalog's custom project roles, global roles, environments, repository sharing and project quotas
(IAM-053 to IAM-064, IAM-068) remain P2: trove's project roles are the four fixed ones below, and no repository is shared.

**The project.**

- A project has a key matching `^[a-z][a-z0-9]*(?:-+[a-z0-9]+)*$` with 2 to 32 characters, a path component of every image reference
  ([Q-OCI-3](formats/oci.md#q-oci-3--repository-and-project-keys-follow-the-oci-component-grammar-for-every-format)) (unique and immutable), a display name (1 to 128 characters) and a
  description (at most 1024). It has no switches: what its Owner may do is fixed by the role (Q-AUTHZ-208).
- An administrator of trove (`projects:write`) creates and deletes projects; the Owner changes the display name and the
  description.
- Every repository belongs to exactly one project. The project is named in the address of the create request and of
  every later operation (`/api/v1/projects/<projectKey>/repositories/<repoKey>`, `/api/<format>/<projectKey>/<repoKey>/…`,
  [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-002, R-REPO-009), and a repository changes project only
  through a move ([R-REPO-060](repositories-and-proxy.md#r-repo-060--move-a-repository-to-another-project)). Its key is
  unique within the project: `web` and `mobile` can each have `npm-remote`. A virtual repository lists only repositories
  of its own project, a remote repository of a public registry exists in each project that needs it (the blob store
  deduplicates, INV-4), and a project's policies, CI mappings and deployment targets name only its repositories
  (R-AUTHZ-004).
- A project holds its repositories, its members, its policies, its service accounts and the CI mappings of its tier
  `project:<projectKey>` ([gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-009). The names of its policies, service accounts
  and CI mappings are unique among the objects of the project, and among those of no project; the same name may exist in
  two projects and once instance-wide. There is no prefix rule and no overlap rule between keys and names.

**Members and roles.**

- Users and groups are members of a project with one role each: `reader`, `developer`, `maintainer` or `owner`. A service
  account, a CI mapping, a Bastion principal, `anonymous`, `authenticated` and an auto-join group are never members (422
  `principal_not_allowed`): every user is in an auto-join group, so it would make every user a member, which is what
  the level `internal` says (R-AUTHZ-033) ([Q-AUTHZ-200](#q-authz-200--a-global-policy-never-takes-a-projects-prefix-and-a-project-policy-names-no-auto-join-group)).
- Each role holds what the role before it holds:

| Role | On every repository of the project | In the project |
|---|---|---|
| `reader` | `read` | Reads the project, its members and its repositories (the consumer view of R-REPO-010) |
| `developer` | `read`, `annotate`, `deploy` | As `reader` |
| `maintainer` | every action: also `overwrite`, `delete`, `manage` | As `reader` |
| `owner` | every action | Administers the project: members and their roles, repositories, policies, service accounts, CI mappings, settings (table below) |

- A user's role in a project is the highest role among its own membership and the memberships of its groups (R-AUTHZ-002,
  resolved when the request is evaluated); a user that is no member and in no member group has no role there. The role's
  actions are decided from the membership, never from a stored grant; they add to what global and project policies grant
  (the union, R-AUTHZ-015), and the explanation names them as source `role:<projectKey>:<role>` (R-AUTHZ-024). A `deploy`
  through a role makes the publishing identity the owner as a grant does (R-AUTHZ-018).
- A role reaches every credential through which the member acts as itself, the UI session and its personal access tokens,
  intersected with the token's scope like any grant (R-AUTHZ-013). Outside the content, a token is a member, with its role,
  only of the projects its scope reaches: every project for `inherit`, `admin: true` or the system item `projects:read`,
  otherwise those one of its `repositories` elements names (a key of the project, a pattern whose project glob matches
  it, or a class), whatever the element's actions. The project list, the read of a project and its members, `GET
  /authz/me` and the `404` of a project the caller is no member of read these memberships. The Owner's administration, its
  reads included, is exercised only through a UI session or a token with `admin: true`, as for a system role. Service-account and CI tokens
  never carry a role; they reach a project's repositories through its policies
  ([Q-AUTHZ-215](#q-authz-215--a-members-role-acts-through-its-session-and-personal-tokens-within-their-scope-and-service-accounts-are-not-members)).
- A project keeps at least one Owner: removing or demoting the last one answers 409 `last_owner` unless the caller holds
  `projects:write`, whose holder may leave a project without an Owner and administers it alone
  ([Q-AUTHZ-217](#q-authz-217--the-last-owner-of-a-project-is-removed-or-demoted-only-by-an-administrator-of-trove)).
  Two Owners who remove each other at once: exactly one removal commits and the other Owner remains. The other request is
  refused as what its caller is when it is decided: with 409 `last_owner` while the index it is decided on still holds the
  caller's role, and with the `404` of a non-member (R-AUTHZ-016) once the first removal, which took the caller's own
  membership, is in force there.

What the Owner may do, within its project and nowhere else:

| Area | What it allows |
|---|---|
| Members | Add users and groups, change their roles, remove them; look up users and groups by a name prefix ([Q-AUTHZ-182](#q-authz-182--how-a-project-administrator-finds-the-users-and-groups-it-names)) |
| Repositories | Create, change and delete the project's repositories of every class: local, remote and virtual ([Q-AUTHZ-173](#q-authz-173--a-project-administrator-creates-remote-repositories)); read their full configuration (as `repositories:read-config` does, never a secret value); test the saved and the unsaved upstream of a remote; request a pass of a mirror (R-REPO-056); move a repository to another project of which it is Owner too (R-REPO-060). A remote's upstream stays subject to the outbound address rules of the installation ([security/egress.md](../security/egress.md)). An Owner without `repositories:write-config` gives an upstream secret as a value, never as a reference of the installation, and a stored one does not follow a new address ([Q-REPO-127](repositories-and-proxy.md#q-repo-127--a-project-administrator-gives-an-upstream-secret-as-a-value-and-a-stored-one-stays-with-its-hosts), 422 `secret_value_required`); it takes the installation's outbound path ([Q-REPO-128](repositories-and-proxy.md#q-repo-128--a-project-administrators-remote-repository-takes-the-installations-outbound-path), 422 `outbound_path_restricted`); and it keeps `remote.mode`, `remote.mirror`, the include and exclude lists, `remote.url` and `remote.extraHosts` of a mirror as stored (Q-REPO-193), pauses and resumes it, or turns the mode back to `pullThrough` ([Q-REPO-142](repositories-and-proxy.md#q-repo-142--who-may-switch-a-remote-to-mirror), 422 `mirror_restricted`) |
| Policies | Create, change and delete the project's policies, granting any action (`manage` included, except to CI mappings) to users, groups, the project's service accounts, service accounts of no project and CI mappings, never to an auto-join group; validate a policy of the project |
| Visibility | Set the levels of the project's repositories, artifacts and wiki, up to the project's level (R-AUTHZ-033); the project's own level is the administrator's |
| Service accounts | Create, change, disable, enable and delete the project's service accounts; issue and revoke their tokens within the bounds below ([Q-AUTHZ-177](#q-authz-177--what-a-project-administrator-may-do-with-service-accounts-and-their-tokens)) |
| CI mappings | Manage the mappings of the tier `project:<projectKey>`, dry-run them and read the issuances of tokens through them, with the bounds of a project policy: the project's repositories only, its policies only, no groups, never `manage` ([gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-019) |
| Settings | Change the display name and the description; read the project's level |

An Owner cannot bind system roles, see policies of no project (unless it holds `policies:read`), see or change anything of
a project beyond what its role there allows, create or delete projects, create users, groups or service accounts of no
project, or change the project's level (R-AUTHZ-033). Delegation of CI mappings to a group
outside projects stays with R-CI-023 (P1).

**Project policies.**

- A project policy is addressed under its project (`/api/v1/projects/<projectKey>/policies/<name>`, R-AUTHZ-021); its
  project is fixed at creation ([Q-AUTHZ-186](#q-authz-186--the-project-of-a-policy-is-fixed-at-creation)) and reads
  back as `"project": "<projectKey>"`.
- Its selectors are of a project context (R-AUTHZ-004): bare keys, key patterns and classes evaluated against the
  project's repositories only. A key that names no repository of the project answers 422 `unknown_repository`, a selector
  with a `/` 422 `invalid_pattern`. Global policies keep applying to the project's repositories; the effect is the union
  of global policies, project policies and roles.
- Entries of a project policy may name users, groups, service accounts of no project, CI mappings and the service accounts
  of the same project. A service account of another project is refused (422 `principal_outside_project`; to an Owner
  without `policies:read` it answers `unknown_principal` as a name that does not exist,
  [Q-AUTHZ-197](#q-authz-197--what-a-project-administrators-policy-list-and-validation-tell-about-other-objects)).
  An auto-join group is refused (422 `principal_outside_project`): every user is a member, so it would stand in for
  the level `internal` ([Q-AUTHZ-200](#q-authz-200--a-global-policy-never-takes-a-projects-prefix-and-a-project-policy-names-no-auto-join-group)).
- The grants of a project policy are stored in their own table, `authz_project_policy_entry`
  ([Q-AUTHZ-174](#q-authz-174--the-grants-of-a-project-policy-live-where-the-previous-release-does-not-read-them),
  [ADR-145](../decisions.md#adr-145--the-grants-of-a-project-policy-are-stored-where-the-previous-release-does-not-read-them)).

**Project service accounts** (owner, [Q-AUTHZ-169](#q-authz-169--a-technical-account-for-a-project-belongs-to-the-project),
[Q-AUTHZ-209](#q-authz-209--a-projects-policies-and-service-accounts-are-named-within-the-project-administrators-keep-instance-wide-ones)).

- A service account with a project is a *project service account*. Its project is fixed at creation, its name is unique
  within the project, it is addressed as `/api/v1/projects/<projectKey>/service-accounts/<name>` and referenced as
  `sa:<projectKey>/<name>` ([identity-and-authentication.md](identity-and-authentication.md) R-AUTH-020). An Owner of the
  project creates it, as does a holder of `identities:write`. A service account of no project is created only by a holder
  of `identities:write`, with its tokens.
- It is granted only through the policies of its project (and, where R-AUTHZ-018 asks for an owner, owns packages only in
  its project's repositories): it is refused as an entry of a global policy or of another project's policy, as a package
  owner outside its project (422 `principal_outside_project`), as a role binding (422 `role_not_allowed`), as a group
  member (422 `project_account_not_groupable`) and as a project member (422 `principal_not_allowed`), so no grant of a
  global group, no role and no system role ever reaches it
  ([Q-AUTHZ-175](#q-authz-175--a-project-service-account-is-granted-by-its-projects-policies-only)).
- Owners issue its tokens (`trv_sat_`, endpoint-bound ones included) and revoke them. A token an Owner issues is never
  non-expiring, lives at most as long as the installation's maximum for service-account tokens, has `admin: false` and
  names no `system` item (R-AUTH-020, R-AUTH-027, Q-AUTHZ-177).
- It can be disabled (all its tokens fail with `principal_disabled`) and deleted (its tokens go) by an Owner of the
  project and by `identities:write`.
- A project with a service account is not empty and cannot be deleted.

**What others see.** `admin`, `security-admin`, `repository-admin` and `auditor` hold `projects:read`: they see every
project with its members, their roles and counts. The `auditor` sees every project, its policies, service accounts and
repositories read-only and changes nothing. Only `admin` holds `projects:write`: it creates, changes and deletes projects
and adds, changes and removes the members of every project, the first Owner among them. The capabilities act in every
project as they do outside projects: `repositories:write-config` (`admin`, `repository-admin`) creates, changes, deletes and
moves repositories of every project, `policies:write` writes the policies of every project, `identities:write` the service
accounts of every project. `GET /api/v1/projects` lists every project to a holder of `projects:read` and otherwise the
projects the caller is a member of, each with the caller's role. `GET /api/v1/authz/me` lists the caller's memberships with
their roles (`projects`: `[{"key": "web", "role": "owner"}]`), which is what the UI reads to show the project screens without
any system capability ([R-UI-041](api-and-ui.md#r-ui-041--projects)). A project the caller is no member of and cannot read
through `projects:read` answers 404 on every route below it, exactly like a project that does not exist; a member whose role
is too low gets 403.

**Deleting a project.** Only a holder of `projects:write` deletes a project, and only when no repository, in any lifecycle
state, belongs to it, no service account belongs to it and no CI mapping has its tier (409 `project_not_empty`). Its
policies are deleted with it, each through the policy store (audited and journaled like any policy deletion), and its
member entries go ([Q-AUTHZ-185](#q-authz-185--versions-delete-order-the-epoch-and-the-journal-of-project-writes)).
Each deletion carries the version read before it as its `If-Match` and names the project, so a policy that took the name in
between in another project or instance-wide is never removed ([Q-AUTHZ-204](#q-authz-204--a-project-delete-passes-the-version-it-read-as-the-if-match-of-each-policy-deletion),
[Q-AUTHZ-199](#q-authz-199--a-delegates-policy-write-is-checked-under-the-policy-lock-and-its-project-lookups-run-in-the-writes-transaction)).
Everything a client can cause is refused before the first policy is deleted; the delete is not one transaction
([Q-AUTHZ-192](#q-authz-192--a-project-delete-is-not-one-transaction),
[H-398](#h-398--a-failed-project-delete-can-leave-the-project-without-some-of-its-policies)).

**The declaration.** An operation that members may call declares `permission: <capability>`, `project: path.projectKey`
and `projectRole: owner` or `projectRole: reader` (R-API-008). The shared step admits the holder of the capability, or a
member of the project the path names whose role is at least the declared one (`reader`: any member; `owner`: an Owner,
through a credential that may administer). A project the caller is no member of answers 404, a role below the declared one
403. Operations on repository content declare their action with `repository: path.repoKey` and `project: path.projectKey`,
and the engine decides them with roles, policies and the token scope (R-AUTHZ-015).

Data model:

```sql
CREATE TABLE project (
  id            uuid PRIMARY KEY,
  key           text NOT NULL UNIQUE CHECK (length(key) BETWEEN 2 AND 32 AND key ~ '^[a-z][a-z0-9]*(?:-+[a-z0-9]+)*$'),
  display_name  text NOT NULL CHECK (length(display_name) BETWEEN 1 AND 128),
  description   text NOT NULL DEFAULT '' CHECK (length(description) <= 1024),
  version       bigint NOT NULL DEFAULT 1,                                   -- ETag
  created_at    timestamptz NOT NULL DEFAULT now(),
  created_by    uuid NULL REFERENCES principal (id) ON DELETE SET NULL,
  updated_at    timestamptz NOT NULL DEFAULT now(),
  updated_by    uuid NULL REFERENCES principal (id) ON DELETE SET NULL
);

CREATE TABLE project_member (                                                -- users and groups
  project_id    uuid NOT NULL REFERENCES project (id) ON DELETE CASCADE,
  principal_id  uuid NOT NULL REFERENCES principal (id) ON DELETE CASCADE,
  role          text NOT NULL CHECK (role IN ('reader', 'developer', 'maintainer', 'owner')),
  PRIMARY KEY (project_id, principal_id)
);
-- repositories.project_id uuid NOT NULL REFERENCES project (id)               (no ON DELETE: the delete counts first)
--   CONSTRAINT repositories_project_key UNIQUE (project_id, key)             (the key is unique within its project)
-- service_account.project_id uuid NULL REFERENCES project (id)               (immutable; stored name `<projectKey>/<name>`)
-- authz_policy.project_id uuid NULL REFERENCES project (id) ON DELETE CASCADE (immutable; stored name `<projectKey>/<name>`)
-- ci_mappings.project_id: the project tier, R-CI-009 (its principal is named `<issuer>/<projectKey>/<name>`)
-- authz_project_policy_entry: the grants of project policies; see the data model of R-AUTHZ-028.
```

A repository can move, so it is held as the pair of its project and its key; a policy, a service account and a CI mapping
never change project, so the stored name of a project's policy and service account, and the principal name of its CI
mapping, carry the project key and a `/`, which no key and no name contains, and the existing uniqueness of the name
columns holds for the project's objects and for those of no project at once. The migration
is `00019_projects.sql`, changed in place: there is no installation to migrate ([CLAUDE.md](../../CLAUDE.md)). Besides the
tables and columns it holds the guards that the database enforces whatever the API checked: members are users or groups,
a project service account is no group member (SQLSTATE `AI002`), its stored name carries its project's key and its project
never changes (`AI003`), and the grants of a project policy cannot be written to the table of global grants (`AI004`).

API:

| Method and path | Purpose | Status | Required |
|---|---|---|---|
| `GET /api/v1/projects` | List with the caller's role and counts | 200 | `projects:read` (all) or a member (its own) |
| `POST /api/v1/projects` | Create; `owners` names the users and groups that become its first Owners in the same transaction ([Q-AUTHZ-229](#q-authz-229--a-project-create-names-its-first-owners)) | 201; 409 `already_exists`; 404 for an owner that does not exist; 422 (`principal_not_allowed` for an owner that is no user or group) | `projects:write` |
| `GET /api/v1/projects/{projectKey}` | Read; `If-None-Match` is ignored, the body is never answered with 304 ([Q-AUTHZ-202](#q-authz-202--a-read-of-a-project-is-never-answered-with-304)) | 200 with `ETag: "<version>"`; 404 | `projects:read` or a member |
| `PUT /api/v1/projects/{projectKey}` | Replace display name and description; `If-Match` required | 200 with the new `ETag`; 404; 412 `precondition_failed`; 422; 428 `precondition_required` | `projects:write` or an Owner |
| `DELETE /api/v1/projects/{projectKey}` | Delete (`If-Match` optional) | 204; 404; 409 `project_not_empty` (repositories, service accounts or CI mappings); 412 | `projects:write` |
| `GET /api/v1/projects/{projectKey}/members` | The members with their roles | 200; 404 | `projects:read` or a member |
| `PUT /api/v1/projects/{projectKey}/members/{principalRef}` | Add a member or change its role: `{"role": "developer"}` | 201 added, 200 changed or unchanged; 404 unknown project or principal; 409 `last_owner`; 422 `principal_not_allowed` or an unknown role | `projects:write` or an Owner |
| `DELETE /api/v1/projects/{projectKey}/members/{principalRef}` | Remove a member | 204; 404; 409 `last_owner` | `projects:write` or an Owner |
| `GET /api/v1/projects/{projectKey}/principals?q=&kind=&limit=` | Users and groups whose name starts with `q` (2 characters at least, 20 at most): kind and name only | 200; 404; 422 | `identities:read` or an Owner |

Operations of other pages below a project, each declared with `project: path.projectKey` (the declarations are in
`api/openapi.yaml`):

| Operation | Declaration | The member's case |
|---|---|---|
| `GET/POST /projects/{projectKey}/repositories`, `GET/PUT/PATCH/DELETE …/repositories/{repoKey}` and its sub-resources, `POST …/{repoKey}/move`, `POST /projects/{projectKey}/upstream-tests` ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-009 to R-REPO-013, R-REPO-060) | `repositories:read-config` or `repositories:write-config`, `projectRole: owner`; the list `projectRole: reader` | Writes and the full document: an Owner; the list and the consumer view: every member, as far as `read` reaches |
| `GET/POST /projects/{projectKey}/policies`, `GET/PUT/PATCH/DELETE …/policies/{name}` (R-AUTHZ-021) | `policies:read` or `policies:write`, `projectRole: owner` | An Owner |
| `POST /authz/validate-policy` with `"project": "<projectKey>"` | `policies:read`; the handler admits an Owner of the named project | An Owner |
| `/projects/{projectKey}/service-accounts[/{name}[/disable\|/enable\|/tokens]]` ([identity-and-authentication.md](identity-and-authentication.md) R-AUTH-020) | `identities:read`, `identities:write` or `tokens:revoke`, `projectRole: owner` | An Owner, with the token rules above |
| `/projects/{projectKey}/ci/…` ([gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-019) | `ci:read` or `ci:write`, `projectRole: owner` | An Owner |
| `GET /tokens`, `GET/DELETE /tokens/{id}` | `authenticated` | Also the tokens of the project's service accounts, for an Owner |

- `PUT /api/v1/projects/{projectKey}` follows [api-and-ui.md](api-and-ui.md) R-API-012 and Q-API-2: without `If-Match` it
  answers 428 and changes nothing, `If-Match: *` is the deliberate blind write, and a stale tag answers 412 with
  `currentEtag`. Adding, changing and removing a member bind one pair and are exempt; they do not move the project's
  version (Q-AUTHZ-185). A write that changes nothing (the principal already had the role) records no event and answers the
  current epoch ([Q-AUTHZ-193](#q-authz-193--what-an-administrator-write-that-changes-nothing-does)).
- Every write above answers `Trove-Authz-Epoch` (R-AUTHZ-032). The writes of a project and its members, and the creation
  and deletion of project service accounts, are audited in the transaction of the change (R-AUTHZ-029):
  `authz.project.created`, `.updated`, `.deleted`, `.member_added`, `.member_changed` and `.member_removed`; a move is
  `repository.moved` in the source project and `repository.moved_in` in the target project (R-REPO-060). Every event of a project's object carries the member `project`. A removed member, a
  lowered role and a move narrow rights and are journaled as narrowings (R-AUTHZ-031).
- Recent authentication (R-API-007) is required for the project writes that change who may act: replace, delete, add,
  change and remove a member, and a move.

Acceptance criteria:

- Project `web` has group `web-leads` as `owner` and group `web-devs` as `developer`; repositories `npm` and `npm-all`
  belong to `web`, `npm-internal` to `mobile`, and no policy names `web-devs`. A member of `web-devs` publishes through
  `https://trove.example.com/api/npm/web/npm/` with a personal access token, gets `E403` for `npm unpublish` (`delete`),
  and `E404` through `https://trove.example.com/api/npm/mobile/npm-internal/`.
- A member of `web-leads` creates policy `npm-read` with `POST /api/v1/projects/web/policies` on the selector `npm` (201);
  the selector `npm-internal`, which names no repository of `web`, answers 422 `unknown_repository`, and the selector
  `mobile/npm-internal` 422 `invalid_pattern`. Policy `npm-read` of project `mobile` and a global policy `npm-read` exist
  beside it.
- The same Owner creates repository `extra` with `POST /api/v1/projects/web/repositories` (201) while project `mobile` has
  an `extra` too; a virtual repository `all` of `web` with member `extra` succeeds. A member of `web-devs` creating a
  repository gets 403, a user that is no member of `web` gets 404 on `GET /api/v1/projects/web`.
- The Owner adds `user:dave` as `maintainer` (201) and changes him to `reader` (200); adding `sa:web/partner` or
  `authenticated` as a member answers 422 `principal_not_allowed`. Removing `group:web-leads`, the only Owner, answers 409
  `last_owner` to its member and 204 to an administrator of trove.
- The Owner's personal access token without `admin: true` reads `web/npm` and gets 403 on
  `PUT /api/v1/projects/web/members/user:erin`; a token with `{"inherit": true, "admin": true}` gets 201.
- The Owner creates service account `partner` in `web` (201, `sa:web/partner`), and an administrator creates the service
  account `partner` of no project (201, `sa:partner`): two accounts. `sa:web/partner` as a member of a group (422
  `project_account_not_groupable`), as a role binding (422 `role_not_allowed`) and as an entry of a global policy (422
  `principal_outside_project`) is refused; as an entry of `npm-read` of `web` it is accepted, and a token of it reads
  `web/npm` and nothing else.
- The Owner issues a token for `sa:web/partner` with a lifetime of 30 days bound to the endpoint `partners` (201). A
  non-expiring token, and a token whose scope names `system: ["settings:read"]` or has `admin: true`, are refused. It
  revokes the token, and the next request with it answers 401.
- Deleting `web` answers 409 `project_not_empty` while `extra` exists, still while only `sa:web/partner` remains, and 204
  when both are gone; its policies and member entries go with it.
- An Owner of `web` calling `GET /api/v1/projects` sees `web` with the role `owner` and no other project,
  `GET /api/v1/projects/docs` answers 404 as for a project that does not exist, and `GET /api/v1/authz/me` lists
  `{"key": "web", "role": "owner"}`. The `auditor` lists every project and gets 403 on `PUT /api/v1/projects/web`.
- An Owner looks up `q=al` and gets users and groups that start with it, 20 at most, kind and name only; `q=a` answers 422.

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
  - repositories: project and key, class, format, virtual membership and deploy target, and the
    authorization settings of R-AUTHZ-031's settings table;
  - projects with their visibility level, and their members with their roles (users and groups, R-AUTHZ-026,
    R-AUTHZ-033);
  - the artifacts and wiki levels of every repository (R-AUTHZ-033), read from their columns in the same snapshot.

  Project rules enter the build in two places. Each project's members are kept with their roles
  (`project_member`), and a request's role in a project is the highest over the user and its groups, decided from
  that map, never from a grant per repository. A project policy covers only the repositories of its project: its bare
  selectors are expanded against the project's repositories, and the memoized result of a pattern or class selector
  is keyed by the selector and the context, so a global `*/npm-*` result is never reused for a project policy's
  `npm-*`. The grants of project policies are read from `authz_project_policy_entry` (Q-AUTHZ-174).

  For each repository ID, the index maps principal IDs to compiled grants: selectors expanded
  against the current repository list, patterns compiled. Repositories are found by their name, the pair of
  project key and repository key (`qname.Repo`), never by a key alone. A new index is built on the side and
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
  - every write to a project or its members, and every move of a repository to another project
    (R-AUTHZ-026, [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-060), which as a repository change
    also notifies `repo_config`;
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
  name           text NOT NULL UNIQUE                                       -- `<projectKey>/<name>` for a project's
                   CHECK (name ~ '^([a-z][a-z0-9-]{1,31}/)?[a-z0-9][a-z0-9._-]{0,127}$'),  -- policy (R-AUTHZ-026)
  description    text NOT NULL DEFAULT '' CHECK (length(description) <= 1024),
  resource_type  text NOT NULL DEFAULT 'repository' CHECK (resource_type IN ('repository', 'build')),
  project_id     uuid NULL REFERENCES project (id) ON DELETE CASCADE,       -- NULL: a global policy; added by 00019
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
  key_pattern    text NULL CHECK (length(key_pattern) <= 1024),             -- kind = 'pattern': `projectGlob/keyGlob`
                                                                            -- in a global policy, a bare glob in a
                                                                            -- project policy (R-AUTHZ-004)
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
  principal_id  uuid NOT NULL REFERENCES principal (id) ON DELETE CASCADE,   -- never anonymous (SQLSTATE AI009)
  actions       text[] NOT NULL CHECK (cardinality(actions) > 0
                  AND actions <@ ARRAY['read','annotate','deploy','overwrite','delete','manage']),
  PRIMARY KEY (policy_id, principal_id)
);

-- the grants of project policies (00019, Q-AUTHZ-174): a table the previous release does not read
CREATE TABLE authz_project_policy_entry (
  policy_id     uuid NOT NULL,
  project_id    uuid NOT NULL,
  principal_id  uuid NOT NULL REFERENCES principal (id) ON DELETE CASCADE,
  actions       text[] NOT NULL CHECK (cardinality(actions) > 0
                  AND actions <@ ARRAY['read','annotate','deploy','overwrite','delete','manage']),
  PRIMARY KEY (policy_id, principal_id),
  FOREIGN KEY (policy_id, project_id) REFERENCES authz_policy (id, project_id) ON DELETE CASCADE
);
```

M1 creates these tables without `project_id` and without the `project` tables; the project work adds the
column in its own migration (`00019_projects.sql`, Q-AUTHZ-14), together with `authz_project_policy_entry`. A policy
whose `project_id` is set keeps its entries in that table only, and a trigger refuses a row for it in
`authz_policy_entry` (SQLSTATE `AI004`).

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
  - project writes, including members and their roles, and the move of a repository (R-AUTHZ-026, R-REPO-060).

  A move changes a column of the repositories table, so it notifies `repo_config`
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

- An administrator gives `group:team-mobile` `deploy` on `web/files-internal`; the `PUT` answers
  `Trove-Authz-Epoch: n`. A member of `team-mobile` then runs 20 uploads
  `curl -T app-<i>.zip -H "Trove-Authz-Min-Epoch: n" https://trove.example.com/files/web/files-internal/app-<i>.zip`
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
| `authz.project.created`, `.updated`, `.deleted`, `.member_added`, `.member_changed`, `.member_removed` | project and membership changes, with the epoch and the role before and after; a deletion, a removed member and a lowered role are journaled | in the transaction of the change |
| `repository.moved`, `repository.moved_in` | the move of a repository to another project ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-060), with the source and the target project and the epoch: `repository.moved` in the source project under the former name, journaled; `repository.moved_in` in the target project under the new name, not journaled ([Q-REPO-233](repositories-and-proxy.md#q-repo-233--a-move-is-recorded-in-the-audit-of-both-projects)) | in the transaction of the change |
| `authz.settings.changed` | changes to authorization settings stored in PostgreSQL, today the authorization settings of a repository (R-AUTHZ-031), with before and after | in the transaction of the change |

Fields of `authz.decision`:

| Field | Content |
|---|---|
| `id`, `time`, `replica`, `requestId` | Identity of the event and the request |
| `actor` | Principal reference and display name; for CI tokens also the mappings and the project identity |
| `credential` | Kind (`session`, `pat`, `sat`, `cit`, `none`) and token ID; never a token value |
| `ci` | For CI tokens, the issuance ID of R-CI-020, which links to `project_id`, `pipeline_id`, `job_id`, `ref` and `ref_protected` |
| `actions`, `project`, `repository`, `class`, `format`, `path`, `package` | The coordinate; `repository` is the qualified name `<projectKey>/<repoKey>` and `project` its project key |
| `outcome`, `reason`, `status` | `allow` or `deny`, the reason code below, the HTTP status sent |
| `sources` | What allowed it: policy IDs and names, `ci-inline` with ordinal, `role:<name>` for a system role, `role:<projectKey>:<role>` for a project role, `ownership` |
| `sourceIp`, `userAgent` | As seen after trusted-proxy processing ([architecture.md](architecture.md)) |

| Reason code | Meaning |
|---|---|
| `allow.grant` | A grant or a project role allowed it |
| `allow.role` | `content:bypass` or a capability allowed it |
| `allow.visibility` | A visibility level allowed the read; the source is `visibility:internal` or `visibility:public` (R-AUTHZ-035) |
| `deny.unauthenticated` | No credential, and no `public` level admits the request |
| `deny.no-grant` | No grant for the action |
| `deny.scope` | A grant exists, but the token scope or the CI caps exclude it |
| `deny.triggering-user` | P1: the triggering user lacks the action (R-CI-024) |
| `deny.virtual-gate` | No `read` on the virtual repository |
| `deny.anonymous-partial-virtual` | Anonymous caller; a member's artifacts are not `public` |
| `deny.wiki` | The artifacts may be read, the wiki may not (R-AUTHZ-034) |
| `deny.populate` | May read the remote repository but not populate it |
| `deny.ownership` | Action allowed, ownership refused |
| `deny.shadowing` | Action allowed; the first version would shadow a package a remote member serves (R-AUTHZ-007, [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-048) |
| `deny.capability` | Management operation without the required capability |
| `deny.stale` | Grant index not confirmed current within the wait of R-AUTHZ-028 |

- **Refused populates and ownership.** A refused populate that is answered 403
  (`deny.populate`) and a refused ownership check (`deny.ownership`) are recorded like refused
  writes, whatever `authz.audit.reads` says: both refuse a change. A populate refusal that is
  answered from the cache, and a member that virtual resolution skips, are not refusals and record
  nothing (R-AUTHZ-008, R-AUTHZ-009 rule 2).
- **Ownership events.** `authz.ownership.claimed` names the owners the claim recorded and the claim
  ID stored in `package_ownership.claim_event`; `.added` names the owner and its reason
  (`added`, `pre-assigned`); `.removed` and `.released` carry the journal entry of R-AUTHZ-031
  (Q-AUTHZ-158).
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
| `authz.project.deleted` | Project ID and key | Delete the project when it holds no repository, no service account and no CI mapping in the restored database; otherwise remove all its members, and report it |
| `authz.project.member_removed` | Project ID and key; the principal's ID and reference | Remove the membership |
| `authz.project.member_changed` | Project ID and key; the principal's ID and reference; the role before and after | Update rule of R-HA-054 with the role comparator below |
| `authz.project.visibility_changed` | Project ID and key; the level before and after (R-AUTHZ-036) | Update rule of R-HA-054 with the level comparator of R-AUTHZ-036; an applied entry lowers the project's repositories as R-AUTHZ-033 rule 3 says |
| `repository.moved` | Repository ID; the source and the target project, IDs and keys | When the restored database holds the repository in the source project, disable it (`disabled: true`, [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-004) and report it `suspended`; `already in effect` when it is in the target project; otherwise `subject unknown` |
| `repository.disabled` | Repository ID; its qualified name for display | Disable the repository (`disabled: true`); `already in effect` when the restored database holds it disabled or being deleted; `subject unknown` when it holds no repository of the ID. Enabling a repository narrows nothing and is not journaled ([Q-AUTHZ-238](#q-authz-238--disabling-a-repository-is-journaled-as-repositorydisabled-with-a-handler-of-its-own)) |
| `repository.deleted` | As for `repository.disabled`; both phases of a deletion (R-REPO-013) carry it | As for `repository.disabled`: the repository is disabled, never deleted, so replay deletes no content and an administrator deletes it again ([Q-AUTHZ-239](#q-authz-239--a-deleted-repository-is-journaled-and-replayed-as-disabled)) |
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
- **Role comparator.** `narrows(current, after)` for a membership answers true only when the membership exists in the
  restored database and the role of `after` is not higher than the current one, in the order `reader`, `developer`,
  `maintainer`, `owner`: a replay never adds a member or raises a role. The display name and the description of a
  project are not access-bearing and are not journaled.
- **A move is not replayed as a move.** Moving the repository again would give the target project's members what the
  restored database does not give them, and leaving it would give the source project's members back what the move
  took; the disabled repository grants nothing to either, and an administrator moves it again (R-REPO-060) and
  enables it.
- **Policy comparator.** `narrows(current, after)` for a policy answers true only when all of the
  following hold, and false otherwise:
  1. `resourceType` and `project` are equal.
  2. The repository selectors of `after` are a subset of those of `current`, compared as sets:
     key selectors by repository ID, pattern and class selectors with their format filter as
     stored. Only what a kind stores is compared: the key and the class that the journal carries
     next to a key selector are display (the class is the repository's), so a policy update that the
     restored database already holds is `already in effect` and is not suspended. Glob containment
     is not evaluated, so replacing `npm-*` by `npm-team-*` does not count
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
  | `visibility.artifacts` | R-AUTHZ-033 | `private`, `internal`, `public` |
  | `visibility.wiki` | R-AUTHZ-033, R-AUTHZ-034 | `private`, `internal`, `public` |
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
  member of `platform-admins` gets 403 on `POST /api/v1/projects/web/repositories`, and a member of
  `team-web` gets `E403` on `npm publish`, on every replica.
- A journaled `authz.project.member_removed` of `group:web-devs` from `web` is reported `applied`, and a member of
  `web-devs` whom no policy grants gets `E404` through `https://trove.example.com/api/npm/web/npm-all/` afterwards. A
  journaled `authz.project.member_changed` whose after-state raises `user:dave` from `reader` to `developer` is
  reported `skipped`.

## Worked examples

### Team: read everything, write the own scope

Repositories of project `web`: `npm-internal` (local, npm, `packageOwnership: enforce`), `npmjs` (remote of the
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
      "repositories": ["web/npm-internal"],
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
npm config set registry https://trove.example.com/api/npm/web/npm-all/
npm config set //trove.example.com/api/npm/web/npm-all/:_authToken "$TROVE_TOKEN"
```

Outcome: every developer installs everything through `npm-all`. Members of `team-web` publish
`@web/*`; the first publish makes `group:team-web` the owner, so any team member may publish the
next version. Nobody else publishes under `@web/`. `lead-web` can additionally repair ownership,
for example after an import, and pre-assign names (R-AUTHZ-019). The trace of R-AUTHZ-015 walks
through the individual decisions.

The two policies are global, because `npm-consumers` covers the npm repositories of every project. Inside the
project the same team can instead be given roles: `group:team-web` as `developer` and `user:lead-web` as
`maintainer` of `web` read, publish and repair every repository of the project without any policy; the policy
`team-web-publish` stays the way to narrow publishing to `@web/*` (R-AUTHZ-026).

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
        { "repositories": ["web/npm-internal"],
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
      registry=https://trove.example.com/api/npm/web/npm-all/
      //trove.example.com/api/npm/web/npm-all/:_authToken=${TROVE_TOKEN}
      EOF
    - npm ci
    - npm publish --registry https://trove.example.com/api/npm/web/npm-all/
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
<summary>Deferred catalog entries (24)</summary>

| ID | Priority | Reason |
|---|---|---|
| IAM-008 | Later | Scan-related roles; no scanning engine is planned. |
| IAM-026 | Later | Release-set resources; release sets are not planned soon. |
| IAM-027 | Later | Distribution-target resources; distribution to edge instances is not planned soon. |
| IAM-028 | Out of scope | CI pipeline-source resources; trove bundles no CI engine. |
| IAM-033 | P2 | Older policy interfaces with whole-policy replacement; the single policy API of R-AUTHZ-021 replaces them. |
| IAM-034 | P2 | Fine control over anonymous grants by non-administrators; no policy names `anonymous` (Q-AUTHZ-279), and the Owner of a project opens its repositories only up to the level an administrator gave the project (R-AUTHZ-033). |
| IAM-053 | P2 | Project members through the assignment of custom roles; the projects of R-AUTHZ-026 have members with four fixed roles and project policies. |
| IAM-054 | P2 | Global roles for projects. |
| IAM-055 | P2 | Custom project roles; the four fixed roles of R-AUTHZ-026 (Q-AUTHZ-208) cover the MVP. |
| IAM-056 | P2 | Role action catalog. |
| IAM-058 | P2 | Sharing repositories across projects; no repository is shared (Q-AUTHZ-206). |
| IAM-059 | Later | Project-aware sharing through internal remote repositories. |
| IAM-060 | P2 | Per-project build repositories. |
| IAM-061 | P2 | Project storage quotas. |
| IAM-063 | P2 | Environments. |
| IAM-064 | P2 | Environments on roles and repositories. |
| IAM-065 | Later | Project-scoped scan policies. |
| IAM-066 | Later | Migration from permission policies to projects. |
| IAM-068 | P2 | Custom project role and environment operations; the MVP subset (projects, members with fixed roles, the move of a repository) is in R-AUTHZ-026. |
| ADMAPI-019 | P1 in the catalog, P2 here | Custom global role operations belong to the project-role model (IAM-054), which stays P2; the fixed system roles of R-AUTHZ-010 cover P1 needs. |
| ADMAPI-025 | P2 | Older policy interface with resource sections (same reason as IAM-033). |
| ADMAPI-026 | P2 | Oldest repository-only policy interface (same reason as IAM-033). |
| TOK-011 | P2 | Project-role token scopes; a token reaches a project role only through its repository scope (R-AUTHZ-013, Q-AUTHZ-215). |
| TOK-013 | P2 | Project membership read scopes; a token reaches a project role only through its repository scope (R-AUTHZ-013, Q-AUTHZ-215). |

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
- **Principals by ID.** Grants, role bindings, memberships and owners follow the principal's immutable ID, so a
  deleted and recreated user, group or mapping of the same name inherits nothing (R-AUTHZ-002).
- **The project is part of every name.** A global selector, a token scope and an exposure rule name the project
  (`web/npm-internal`), and a bare key there is refused, so a repository that another project creates under the same
  key is never covered by accident; a project's own objects take bare keys that resolve in that project only
  (R-AUTHZ-004). A project the caller cannot see answers like one that does not exist (R-AUTHZ-016).
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
- **Public populating.** A remote repository with `public` artifacts and `cacheFillAction: read` lets anyone make trove
  download arbitrary upstream packages: storage growth, upstream rate limits, possibly upstream cost. The repository API
  flags the write that opens it with the warning `public_populates_remote`. The per-repository fetch budget
  `limits.maxConcurrentFetches` of [repositories-and-proxy.md](repositories-and-proxy.md)
  R-REPO-040 is shared by the whole installation, so it bounds concurrent upstream fetches of one
  repository whatever the replica count; it bounds concurrency, not volume, and the per-replica
  `proxy.max_concurrent_fetches` grows with the number of `web` replicas and with autoscaling.
  Where remote repositories are public, set `cacheFillAction: deploy` and grant `deploy` to named groups only
  (R-AUTHZ-008).
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
- **Levels read, nothing more.** A visibility level gives only `read` (R-AUTHZ-033): it never makes anybody a package owner,
  never covers a write and never reaches a service account or a CI token through `internal`, so a leaked automation token
  carries only what was granted to it by name, and what is `public`. Only an administrator opens a project; its Owner opens
  repositories within it (R-AUTHZ-033 rule 4). The levels and their gaps are the subject of
  [security/visibility.md](../security/visibility.md).
- **Administrator power stays out of everyday tokens.** Tokens exercise roles only through
  `admin: true` or `system` items (R-AUTHZ-013). A leaked personal access token of an
  administrator therefore exposes that person's content rights, not the instance. The same holds for a project: an
  Owner administers it only through a session or an `admin: true` token, so its leaked everyday token carries the
  role's content actions, not the project's members and policies (R-AUTHZ-026).

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

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. **Superseded by [Q-AUTHZ-263](#q-authz-263--visibility-levels-replace-the-anonymous-switch-the-principal-authenticated-and-requireauthentication)**: the level `internal` reads for every internal user (R-AUTHZ-030, R-AUTHZ-033), and the principal `authenticated` is gone; the reasons of (d) carry over to who is internal (Q-AUTHZ-268).

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

**Applied in the spec:** none. The gap stays [H-63](../security/roles-and-trust-boundaries.md#h-63--anonymous-read-of-installation-facts-and-the-replica-list-while-the-bootstrap-switch-is-on). Narrowed by Q-AUTHZ-71: the bridge opens the system information and the replica list, not every operation of `settings:read`.

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

**Rationale:** Nothing in the audit contract says the recorder deduplicates, and R-AUTHZ-029 puts the obligation on authorization. The development agent would revisit this when the audit area exposes a deduplicating recorder: the producer then passes the key and deletes its own state. The two gaps that followed were H-101 (closed by Q-OPS-49) and the missing `repeat_of`. **Superseded by [Q-AUTHZ-24](#q-authz-24--the-denial-deduplication-lives-in-the-audit-writer-only):** the audit writer deduplicates alone, and both gaps are closed.

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

### Q-AUTHZ-24 · The denial deduplication lives in the audit writer only

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Q-AUTHZ-19 put the deduplication of `authz.denied` in the producer and said it would be revisited when the audit area deduplicates in its recorder. It does (R-OPS-015, `internal/audit/dedup.go`), so two stages ran on the same events. Checked with the code, the second stage corrupted the first: the producer sent no `repository`, `path` or `details.required_action`, so the writer's key reduced to the actor and collapsed every refusal of one principal into one; and the producer's own summary (count n, no `repeat_of`) arrived inside the writer's window and was counted as one repeat, so the stored summary said 1 instead of n.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Keep both stages | The producer collapses, the writer collapses again | No change | The stored count is wrong and different refusals share a key, as found |
| (b) The writer alone | The producer sends every refusal and fills the columns and the detail the writer keys on; the producer's deduplication state and its flush at shutdown go | One mechanism for `authz.denied` and `token.rejected`; the summary carries the first event's ID as `repeat_of` (Q-AUTHZ-19 could not); an anonymous refusal is keyed by the client address, which the request source gives it (Q-OPS-49) | Every refusal crosses the recorder call, which is cheap and never blocks |
| (c) The producer alone | Switch the writer's rule for `authz.denied` off | Key known where the event is made | A second implementation next to a recorder that deduplicates; no `repeat_of` |

**Choice:** (b). An `authz.denied` event carries `Repository`, `Path` (the path, else the package name), the outcome `denied` (it was `failure`, which `outcome=denied` could not find, Q-OPS-40) and `details.required_action` (the actions joined by a comma, or the capability for a management operation). The count of a summary is exact: the first event has `count` 1 and `repeat_of` unset, the summary has the repeats and the first event's ID. The integration test `TestR_OPS_017_DenialSummariesStayCorrect` shows it over a window.

**Rationale:** one deduplication per type means one place where the sum of the counts can be wrong. The development agent would revisit this if a producer other than authorization needs a key the writer cannot derive from the event's columns and details.

**Applied in the spec:** Q-AUTHZ-19 is superseded by this block; R-AUTHZ-029 already requires the key replica and client address, which now holds; H-101 is closed in [security/permissions.md](../security/permissions.md).

### Q-AUTHZ-25 · One pattern compiler

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-AUTHZ-005 and R-REPO-005 specify one syntax. Wave 1 built it twice: `repository.CompilePattern` (a segment matcher) and, in `internal/authz`, a translation to regular expressions.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Two compilers | Each package keeps its own | No change | Two implementations of one syntax drift; a pattern valid in a repository's include list could be invalid in a policy |
| (b) `repository.CompilePattern` | `internal/authz` compiles and matches with it; it keeps the limits of R-AUTHZ-005 (1,024 characters, 256 patterns), the case folding of NuGet names and the include and exclude semantics of a resource set | One syntax; the 284 cases of E1.3 and the pattern table pass unchanged; the matcher has no regular expression, so no pattern can make it slow | `authz` imports one more function of `repository`, which it imports already |

**Choice:** (b).

**Rationale:** Q-STOR-9 decided that permissions and retention share one syntax; this makes the code say so. The development agent would revisit this if a format defines a pattern subject the repository package cannot express.

**Applied in the spec:** none.

### Q-AUTHZ-26 · A policy also accepts a merge patch

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-AUTHZ-021 offers `PUT` only, and R-API-013 says a policy is changed by `PUT` of the whole document. The policy editor changes one member at a time (the description, one entry), and the convention suite of E1.6 (`apitest.RunContract`) tests merge patch on every collection.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `PUT` only | As written | No change | The editor and scripts send the whole document to change one member, and the suite's merge-patch tests do not apply |
| (b) `PUT` and `PATCH` | `PATCH` takes `application/merge-patch+json`, is applied to the current representation and written like a `PUT` conditional on the version it was computed from | The same rules, validation, epoch and audit event; no second code path in the service | One more operation |

**Choice:** (b). `PATCH` needs `If-Match` (428 without it); `If-Match: *` is accepted but the write is still conditional on the version the patch was computed from, because merging over a change the client never saw would be the lost update that `If-Match` exists to prevent.

**Rationale:** R-API-013 itself says a resource that offers `PATCH` takes a merge patch; the sentence about policies was written before the editor's needs were known. The development agent would revisit this if clients start to rely on `PATCH` for arrays, which merge patch cannot edit member by member.

**Applied in the spec:** R-AUTHZ-021: the `PATCH` row and the concurrency bullet; R-API-013: the sentence on policies.

### Q-AUTHZ-27 · The effective-permissions views and the explainer are not in M1

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

The beta plan lists "Permission policy editor with 'why can X do Y' explainer" under phase 2. The roadmap's M1 scope names R-AUTHZ-021 to R-AUTHZ-023 and not R-AUTHZ-024 (`GET /api/v1/authz/effective`, `POST /api/v1/authz/explain`, P1); E1.7 lists the policy editor and no explainer; R-UI-028 is P1.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Not built in M1 | The two operations arrive with R-AUTHZ-024 | The M1 scope as written; the trace of R-AUTHZ-024 includes ownership and CI template steps that do not exist yet | The editor has no "why" |
| (b) A reduced explain | `POST /authz/explain` for a user, group or service account on a coordinate, from `Decision.Reason` and `Decision.Sources` | A visible "why" earlier | A second shape of an answer that R-AUTHZ-024 will replace; a trace without the steps its acceptance criteria name |

**Choice:** (a). The policy editor of M1 gets its preview from `POST /authz/validate-policy` (errors, warnings and the repositories the selectors cover) and the caller's own view from `POST /authz/check`; the engine already returns the reason code and the granting policies of a decision, which is what the explainer will render. The sentence in the beta plan needs the owner's decision at the walkthrough: either the explainer moves to M1 (a contained addition on `Engine`, no schema change) or the plan line is split.

**Rationale:** building to a P1 requirement with a partial trace would be rewritten when ownership and CI templates arrive. The development agent would revisit this at the walkthrough.

**Applied in the spec:** none.

### Q-AUTHZ-28 · The shapes the requirements leave open

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Question | Choice |
|---|---|
| The code of 409 for a policy name that exists | `already_exists`, the code of every create (R-API-004); Wave 1 answered `conflict` |
| `project` in a policy and as a list filter | `null` and absent in M1; present since the projects of R-AUTHZ-026 (Q-AUTHZ-174 to Q-AUTHZ-185) |
| What `POST /authz/validate-policy` answers for a policy with problems | `200` with `valid: false`, `errors` (pointer, code, detail), `warnings` and `covers`; the editor shows the list. Malformed JSON is `400`. `create` answers `422` for the same problems |
| The shape of `GET /roles` | The five fixed roles with their capabilities in the list envelope; `nextCursor` is always `null` and no `limit` or `cursor` is accepted |
| The body of `PUT /role-bindings/{role}/{principalRef}` | None. The answer is the binding (`role`, `principal`, `grantedAt`, `grantedBy`); `201` new, `200` existing |
| How the lists are paged | Policies by name and bindings by role and principal, in memory over a read of PostgreSQL per call; the cursor holds the sort key of the last item. A page assembles the warnings of its policies only. The cost grows with the number of policies (a few hundred are expected); a keyset query joins the list if an installation outgrows it |
| `GET /authz/me` | `roles`, `capabilities`, `principals`, `scope` (`null` for a credential without a ceiling, else `inherit`, `admin`, `system`, `grants`), `projects` (the projects the caller administers, `[]` otherwise; Q-AUTHZ-181) and `epoch`; the members the shell reads (`capabilities`, `principals`) keep their names |
| `POST /authz/check` | At most 100 checks (`422 validation_failed` beyond), an unknown action `422 invalid_action`, no credential `401`; the answer carries the epoch of the index that decided all of them |
| Which operations carry `Trove-Authz-Epoch` | Policy create, replace, patch and delete, role bind and unbind (an existing binding answers the current epoch). A replay of an idempotent create answers the epoch of the first answer |

**Rationale:** each is the reading that keeps the answer identical on every replica and the representation the one the requirement shows. The development agent would revisit the paging of the policy list when a policy count above a thousand is seen.

**Applied in the spec:** none.

### Q-AUTHZ-69 · What `identities:write` may change on a principal that holds a system role

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Every role binding is `roles:write`, but a membership is a binding in effect: a `security-admin`, or a token scoped to
`identities:write`, could add a principal to a group bound to `admin`, add a pending identity link to an administrator and
claim it at the next login, or rename an administrator group so the identity provider's group sync fills it.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) R-AUTHZ-011 as written | Groups bound to a role: membership and delete; users and service accounts bound directly to `admin` or `security-admin` | Narrow | Leaves the identity link of a user who is administrator through a group, the rename of a group, and every holder of the other roles open |
| (b) Every change of a role holder needs `roles:write` (chosen) | A group that carries any role (membership from either side, name, description, auto-join, delete); a user or service account that holds any role directly or through a group (profile, identity links, disable, enable, delete) | One rule a reviewer can check: who holds a role is changed only by who may bind roles | A `security-admin` cannot correct the e-mail address of an administrator |

**Choice:** (b). The check runs in the transaction of the change and asks the decision point for `roles:write` only when the
principal it changes holds a role, so the refusal (403 naming `roles:write`) and its `authz.denied` event appear only where
they apply. Ending sessions and revoking tokens stay outside it: `tokens:revoke` covers every principal (R-AUTHZ-010), and
both only take access away. The admin command runs with full rights.

**Rationale:** taking over the holder of any role reaches a capability the actor lacks (`repository-admin` holds
`repositories:write-config`, which `security-admin` does not). The development agent would revisit the scope when owners of
service accounts (P1) or SCIM-managed groups arrive.

**Applied in the spec:** R-AUTHZ-011 (the escalation guards).

### Q-AUTHZ-70 · What `last_admin` counts and which changes it guards

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

The guard counted `admin` bindings and ran in the unbind only. Deleting or disabling the last administrator, deleting its
group or removing it from the group bypassed it; a binding on an empty group counted as an administrator; two unbinds on
different replicas each saw one binding left and both committed.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Bindings, the unbind only | As before | Simple | The bypasses above |
| (b) Active holders, every change that can end the role (chosen) | Active users and service accounts that hold `admin` directly or through a group; the unbind, disable and delete of a user or service account, the delete of a group and the removal of a member take a lock on the `admin` binding rows first and count afterwards in a statement of their own | The guard means what it says: someone can still act as administrator | A change that touches an administrator waits for another such change |
| (c) Also refuse the identity provider's group sync | The login that drops the last administrator group membership is refused | Complete | The identity provider is authoritative for its groups; refusing a login does not keep the person in charge |

**Choice:** (b). The sync of R-AUTH-008 stays outside; R-HA-054 reports an installation without an administrator.

**Rationale:** the break-glass administrator is the recovery path, and the guard keeps an installation from losing its
last administrator while that path is closed. The development agent would revisit (c) if the owner wants the sync to keep a
last membership.

**Applied in the spec:** R-AUTHZ-022.

### Q-AUTHZ-71 · The bootstrap bridge opens two operations, not a capability

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Q-AUTHZ-16 kept the dev-profile bridge `system.bootstrap_anonymous_read` as "exactly `settings:read`". Since M1 that
capability also covers the scheduled tasks, the encryption status and the authentication settings, while the decision on the
switch in [api-and-ui.md](api-and-ui.md) and H-63 name only the system information and the replica list.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The capability | As Q-AUTHZ-16 | No path knowledge in the bridge | The switch opens whatever `settings:read` comes to cover |
| (b) The two operations (chosen) | `GET /api/v1/system/info` and `GET /api/v1/system/replicas`; the composition root's endpoint stage hands the request path to the bridge | What the switch was introduced for, and no more | The bridge knows two paths |

**Choice:** (b), in `internal/app/access.go`.

**Rationale:** a development switch must not widen with the API. The development agent would revisit this when the switch is
removed (Q-AUTHZ-16).

**Applied in the spec:** Q-AUTHZ-16 (a note on its choice).

### Q-AUTHZ-152 · The time of the repository lookup before the decision is not promised

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-AUTHZ-016 asks that authorization run before the existence lookup so that the time does not depend on existence; R-ARCH-011 step 2
(Q-ARCH-40) looks the repository up first, because a repository created through another replica is unknown to its grant index until
the change-log poll. A key the replica has not seen costs one PostgreSQL query (and, when the row exists, a wait for the index); a
key it holds, hidden or not, costs a map lookup.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Lookup only for callers who could be allowed | Skip it when the subject holds no grant anywhere | No cost for a stranger | The creator on another replica is such a caller until the poll: R-API-052 is broken for exactly the caller it is for |
| (b) The same cost for every key | A query for every request | No difference | A database round trip per request; the negative cache (`cache.unknown_repo_keys`) is the point of the design |
| (c) Pad the answer | A delay for the cheap path | Narrows the gap | Not constant time, and a delay per request |
| (d) Promise the bytes, record the time (chosen) | R-AUTHZ-016 amended; H-155 names the residual | Honest, and R-API-052 stays | A caller who measures the first answer for a fresh key can tell that the key is unknown to the replica |

**Choice:** (d). The difference exists for the first request for a key per `cache.unknown_repo_keys` lifetime (60 s) and replica;
status, headers and body are identical in every case, which the hidden-existence test checks byte for byte.

**Rationale:** (a) and (c) trade the guarantee the lookup exists for against a timing difference of a few milliseconds that
network jitter hides; (b) removes the cache. The development agent would revisit it if probing for repository names is seen, then with
(c) on the first request for an unknown key.

**Applied in the spec:** R-AUTHZ-016, the hiding rule.

### Q-AUTHZ-132 · Identity and repository writes that change grant-index inputs call an authz-owned hook

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-AUTHZ-028 requires every write to an input of the grant index to move the authz epoch. Deleting or renaming a principal, and
deleting a repository, change the index (rows go with the foreign keys; policy representations show names) without that.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The identity and repository areas notify `authz` themselves | They know the tables | No hook | Two more owners of the area's payload and of the policy versions |
| (b) An authz-owned hook called in their transactions (chosen) | `authz.PrincipalHook` (`principal.AuthzHook`) before the delete and after a rename; `authz.PolicyHook` (`repository.Hooks`) before a repository row is removed. Each raises the version of every policy it changes and notifies the area | One owner; the ETag of a policy changes when its representation does | A hook interface in `internal/auth/principal` |

**Choice:** (b). A principal that no policy entry and no role binding names moves nothing.

**Rationale:** the policy version is the ETag of the representation (R-AUTHZ-021). The development agent would revisit this if
projects (R-AUTHZ-026) add more tables that name principals.

**Applied in the spec:** R-AUTHZ-028's completeness test covers identity and repository writes (`TestR_AUTHZ_028_IdentityAndRepositoryWritesAdvanceTheEpoch`).

### Q-AUTHZ-133 · The decision point looks an unknown repository up itself

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

A repository created through another replica is not in the grant index of this one until the change log delivered it
(R-API-052). The lookup and catch-up existed twice (files service, repository API) and was missing from the shared action step.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Each caller looks up | As before | No new dependency | Missing for getItem, deleteItem, listChildren, getItemStats |
| (b) `authz.Engine` holds the `repository.Reader` (chosen) | `Decide` and `Begin(ctx, sub, keys...)` call `Know` for a key the index lacks: the snapshot lookup, then the catch-up to the database's epochs; the result is not used, so authorization stays before existence | One place; both copies are gone | The engine depends on the reader |

**Choice:** (b).

**Rationale:** a decision that must see the repository should not depend on the caller remembering. The development agent would
revisit this when the engine and the reader share a request-scoped handle (architecture review item 5). That condition is met by
Q-AUTHZ-159: the request's holder begins with the keys and learns later ones through `Know`.

**Applied in the spec:** none (R-ARCH-011 step 2 and R-API-052 are met by it).

### Q-AUTHZ-134 · A change message starts a build of its own, a capability probe records nothing, the anonymous switch is read once

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. **Superseded in part by [Q-AUTHZ-263](#q-authz-263--visibility-levels-replace-the-anonymous-switch-the-principal-authenticated-and-requireauthentication)**: the anonymous switch of (3) is gone; a request without a credential is decided on the levels of the index it began on.

Three smaller decisions of the decision point. (1) The hint of a change message (this replica's own commit included) requested a
`poll` build, which is skipped when the index equals the applied state; the poll applies a replica's own write only later, so the
writer kept the old grants. (2) The rate-limit exemption of administrators and the handlers that choose between two answers called
`Require`, which records `authz.denied` for a caller who was not refused anything. (3) The anonymous switch was read per coordinate
with `context.Background()` and an error became 401.

**Choice:** (1) the hint requests a `message` build that is skipped only when the index label equals the epochs the hint read;
(2) `Engine.Holds` / `v1.Holds` answer without an audit event, with `authz.ContentBypass`; (3) `Options.AnonymousAccess` takes the
request's context and is read once per request, an error is 503 `authz_stale`.

**Rationale:** each follows from R-AUTHZ-028, R-AUTHZ-029 and R-AUTH-037 as written. The development agent would revisit (2) if a
probe must be counted.

**Applied in the spec:** the `message` trigger of `trove_authz_index_rebuilds_total` is used as R-OPS-008 lists it.

### Q-AUTHZ-159 · A request takes its index once and carries it in a holder

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-AUTHZ-015 says "one index per request" (`index.ForRequest`), but only `Begin` and `DecideOn` honoured it. `Decide`,
`Readable`, `Require`, `Check`, `Exposed` and `PathsAllowed` each took the installed index at their own moment, so the shared step
of the management API and the handler behind it, or a listing and the deploy that follows it, could be answered on two indexes
and disagree about one request after a swap (R-AUTHZ-028).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A holder in the request context for the management API, the index in the request for formats | `authz.Decider` offers the `…On` form of every question; `edge.Access` holds the subject, the index and the decider; the shared step begins once and stores it with `edge.WithAccess`; the format dispatcher passes the index explicitly | The generated handlers keep their fixed signatures; losing the holder costs a second index and never a ceiling, because the ceiling travels in the subject (Q-EP-20) | A context value that a handler can lose |
| (b) Explicit only | Every handler receives the index | No context value | A wrapper type around every generated handler |
| (c) The engine keeps the index in the context | `ForRequest(ctx)` as the pseudo-code reads | One lookup | The decision point reads request state from a context it does not own, and a detached context has none |

**Choice:** (a). `Decider` is `Begin`, `Know`, `DecideOn`, `ReadableOn`, `ExposedOn`, `PathsAllowedOn`, `RequireOn`, `HoldsOn`,
`CheckOn` and `RecordAllowed`; `*Engine` implements it. `Authorizer` and `Prober` stay as the forms of a caller without a request
to share an index with, a job or a command: each call is `Begin` and the matching `…On` form. `ReadableOn`, `PathsAllowedOn`,
`RequireOn` and `CheckOn` return an error because the anonymous switch of R-AUTH-037 is read on the request's context inside the
form and an unreadable switch is 503, not an empty list. `Know` advances a request to a newer index when it learns a key the
index does not hold (R-ARCH-011 step 2); the index only moves forward.

**Rationale:** one request, one index is a consistency property: two decisions of a request never disagree because the grants
changed between them. It is not a security property, so it may live in a context value. The development agent would revisit this
if a request fans out so widely that a shared holder becomes contended, or if `Authorizer` keeps a caller that is a request
(then it moves to the holder).

**Applied in the spec:** R-AUTHZ-015 (the pseudo-code begins once and decides with `DecideOn`); Q-AUTHZ-133 (its revisit
condition, the request-scoped handle, is met).

### Q-AUTHZ-153 · The endpoint's cache-fill setting travels in the subject, and the populate check is one function asked two ways

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: `MemberUsable` of R-AUTHZ-015 read `sub.Endpoint.CacheFill`, but `Subject.Endpoint` is the endpoint's name
(Q-EP-20). The populate check of R-AUTHZ-008 is asked by the remote engine, which answers a refusal from the cache or with
403, and by the virtual resolver, for which a refused member is a miss; both decide in collapsed flights and fanned-out
goroutines on contexts the request does not own.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A parameter of the question | `MayPopulate(idx, sub, c, endpointCacheFill)`, filled by every caller from the request | No change to the subject | Every caller carries the flag next to the subject; one that passes `true` by mistake contacts the upstream from an endpoint that must not |
| (b) A field of the subject, set by the conversion that sets the endpoint | `Subject.CacheFill`, set by `edge.SubjectOf` from the same `httpserver.Endpoint`; only `internal/authz` and `internal/api/edge` build a subject (archtest R-EP-013) | One source; it travels into every goroutine with the subject, as the exposure ceiling does; the zero value refuses | One more field on the subject |
| (c) The engine looks the setting up by name | An `Options` member holding the configured endpoints | No subject field | A second copy of the endpoint configuration; an unwired option fills everywhere or nowhere |

**Choice:** (b). The check is one function asked two ways: `Decider.MayPopulateOn` is a question that audits nothing (the
stale path, `MemberUsableOn`), `Decider.PopulateOn` counts a refusal and audits it as a refused write with `deny.populate`
(403 `populate_not_permitted`). The session cookie is recognised by the session ID or the credential method, and refuses
before any grant is looked at. A subject without an endpoint (a job, a command) has no cache-fill ceiling, as it has no
exposure ceiling.

**Rationale:** Q-EP-20 put the endpoint into the subject because decisions run on contexts the request does not own; the
endpoint's cache-fill setting has the same problem and travels the same way. The development agent would revisit this when
authorization needs a third property of the endpoint (the subject should then carry an endpoint value instead of fields), or
when endpoint settings change at runtime, which a long-lived flight would not see.

**Applied in the spec:** R-AUTHZ-008 ("One check, asked two ways"); R-AUTHZ-015 (`MayPopulate`, the subject paragraph);
R-AUTHZ-029 (refused populates); [endpoints.md](endpoints.md) R-EP-019.

### Q-AUTHZ-154 · Ownership is decided in the commit transaction, on facts the writer states

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the pseudo-code of R-AUTHZ-015 ran an ownership check inside `DecideOn`, which reads memory only, while R-AUTHZ-018
reads the rows inside the write transaction. Rule 1 needs to know whether the name holds a stored version, rules 2 and 3 who
claims; the formats contract wraps the check for a writer that holds the commit transaction and the package row lock (C-14 of
the M2 plan).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) An early check in `DecideOn` and the commit check | Read the rows outside a transaction before the upload, then again at commit | Refuses before the body arrives | `DecideOn` would query PostgreSQL, which R-AUTHZ-015 forbids in the steady state; the early answer can be wrong and is repeated anyway |
| (b) Only in the commit transaction | `Ownership.CheckTx` and `ClaimTx` (`EnforceTx` runs both), after `DecideOn` and under the writer's package row lock; the writer states `OwnershipWrite{Publish, FirstVersion}` | Decided on the rows the commit writes against; npm spools and validates the body before it decides anyway (R-NPM-007), so (a) saves no upload | A non-owner's upload is refused after it arrived |
| (c) The check counts the versions itself | A query of `package_versions` inside the check | No input from the writer | The answer depends on whether the writer inserted the new version before it asked |

**Choice:** (b), with these rules: the claim row is read `FOR UPDATE`, no row is unclaimed and `released` counts as unclaimed;
only a publish claims, so a dist-tag or an unpublish on an unclaimed name without versions passes rule 1 and leaves the name
unclaimed (a holder of `delete` alone would designate no owner by rule 2 and close the name); a name claimed without owners
needs `manage` whatever its versions (rule 6, R-AUTHZ-031); a publish that would claim but whose grants designate no owner is
refused instead of recording an empty claim; a claim that loses the insert race re-reads under the lock and is decided again,
at most three times. A refusal is 403 `package_owned_by_other_owner`, audited as `authz.denied` with `deny.ownership`.
`ClaimTx` takes only the check, so the claim is made on the subject and index the check decided on.

**Rationale:** the decision point stays in memory, and the decision that can block a write is made on the rows the write
commits against, as R-AUTHZ-015 already demands for the re-check at commit. The development agent would revisit this when a
format accepts a large body it does not need to decide (chunked uploads of R-STOR-011, P1): an advisory early check outside a
transaction could then save the transfer, and would never replace the commit check.

**Applied in the spec:** R-AUTHZ-015 (`OwnershipCheckTx` replaces the check in `DecideOn`); R-AUTHZ-018 (the bullet on where
the check runs, rule 1, the lock in rule 5, rule 6).

### Q-AUTHZ-155 · The ownership settings of a repository: the format's default whenever the member is absent, no shared domain before R-AUTHZ-020

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: Q-AUTHZ-2 makes `enforce` the default of new npm and NuGet local repositories, and R-AUTHZ-018 reads it back from the
document. The repository model must not name a format (R-ARCH-008), and the decision point reads the setting from its index.
`ownershipDomain` (R-AUTHZ-020) is P1, but the M1 validation accepted it for every format with package names.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The default at create only | The create stores `enforce` when the body omits it | The literal wording of Q-AUTHZ-2 | A replace without the member (R-REPO-011: omitted settings revert to defaults) or a merge patch with `null` stores none, which the check reads as `off`: enforcement ends without anyone choosing `off` |
| (b) A catalog fact, stored whenever the member is absent | `catalog.Facts.OwnershipDefault`; the validation of every create and update stores it when the document sets none | `off` is always explicit; the stored document says what is in force, and `authz.settings.changed` journals every change of it | An update of a document stored without the member writes it (none exist: no format with package names was registered before) |
| (c) The check reads an absent member as the default | No stored value | No write | The read-back fails; two places know the default |

For `ownershipDomain`: (i) ignore it, every repository is its own domain; (ii) build shared domains now, without the
conflict check of a join (409 `ownership_conflict`); (iii) refuse it with 422 `not_applicable` until R-AUTHZ-020 is built.

**Choice:** (b) and (iii). An unreadable repository document makes the index read the setting as `enforce`, failing closed as
`requireAuthentication` does.

**Rationale:** (a) and (i) both leave an operator believing something is in force that is not, which is the argument of
R-REPO-008 against silently dropped settings; (ii) would let a join hand names to the domain's owners without anyone deciding
it. The development agent would revisit (iii) when R-AUTHZ-020 is built, and (b) if a format needs a default that depends on
more than the format.

**Applied in the spec:** R-AUTHZ-018 ("When ownership is checked"); [repositories-and-proxy.md](repositories-and-proxy.md)
R-REPO-003 (the fact) and the settings table of R-REPO-004.

### Q-AUTHZ-156 · The ownership rows of a deleted repository go in the transaction that deletes its row

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: `package_ownership.domain` is text (`repo:<id>` or `domain:<name>`), so no foreign key removes the rows of a deleted
repository.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) An authorization hook on the repository deletion | `authz.OwnershipHook.OnDelete` deletes the rows of `repo:<id>` in the transaction that removes the row (the last step of the deletion job, or the immediate removal of a virtual repository) | One transaction; the pattern of `PolicyHook` and `ExposureHook` (Q-AUTHZ-132) | The composition root must wire one more hook |
| (b) Leave the rows | Repository IDs are never reused | Nothing to build | Tables and backups keep growing with rows nothing can reach |
| (c) A periodic cleanup task | A worker task deletes the rows of missing repositories | Decoupled from the deletion | A second mechanism with a window |

**Choice:** (a). No audit event per row: `repository.deleted` covers them.

**Rationale:** IDs come from an identity column and are never handed out again, so this is hygiene and backup size, not an
access fix. The development agent would revisit this with R-AUTHZ-020: a shared domain must not go with one of its
repositories.

**Applied in the spec:** R-AUTHZ-018 (rule 8, the data model); [repositories-and-proxy.md](repositories-and-proxy.md)
R-REPO-013.

### Q-AUTHZ-157 · The owner operations: who may change owners, which identities can own, what the answers reveal

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-AUTHZ-019 names the permissions and the statuses, but leaves open the code of "409 already an owner", the answer to
a refused change on a repository or name that does not exist, references that name no owner identity, CI project owners
before the CI issuers of M4 exist, and what the list by owner shows to a caller without `policies:read`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Read first | Every operation decides `read` first and answers every refusal 404 | One rule for all four operations | A refused write would be 404 for a missing repository and 403 for an existing one, against R-AUTHZ-016 |
| (b) Writes answer as writes, reads as reads | Adding and removing: 401 for anonymous, 403 `ci_token_restricted` for a CI token, both before any lookup; 403 naming `manage` for a caller who may not change the owners, the same for a missing repository or name; 404 only for a caller holding `manage` where ownership does not exist. Reading: `read`, else 404 | Neither status depends on existence for a refused caller | Two rules |

**Choice:** (b). Further: 409 is `already_exists`, the code of a create of an existing resource (R-API-004); the permission
is decided before the owner reference is resolved, so a refused caller learns nothing about principals; `anonymous`,
`authenticated` and, until M4, `ci:<issuer>:project:<id>` are 422 `unknown_principal`; releasing the last owner sets the state
`released`; the list by owner shows the entries that name the identity itself, all of them to a holder of `policies:read` and
only those of readable repositories to the identity or a member of the group. Adding and removing need recent authentication
through a session (R-API-007), because adding an owner widens who may publish a name as a policy entry does; an added owner
answers 201 with `Location` of the name's owners, the URL on which a GET shows it (R-API-004), since R-AUTHZ-019 has no read
of one owner. The declarations of the four operations decide what they can before the handler (a repository-level `read`
for the owners view, an authenticated caller for the rest) and the handler decides the name, normalized with the format's
name rule, on the request's index.

**Rationale:** R-AUTHZ-016 fixes the answers of reads and writes, and these operations are one of each. The development agent
would revisit the list when offboarding needs the names a principal owns through its groups, and the CI project owners when
the CI issuers of M4 exist.

**Applied in the spec:** R-AUTHZ-019 (the table and the rules below it); [api-and-ui.md](api-and-ui.md) R-API-007 (the row
"Package ownership").

### Q-AUTHZ-158 · The audit of ownership changes and the claim's event ID

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-AUTHZ-029 names `authz.ownership.claimed`, `.added`, `.removed` and `.released`, R-AUTHZ-031 the journal entries of
the last two. `package_ownership.claim_event` is "the audit event of the claim", but the audit recorder assigns the ID of an
event itself and returns none.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A claim ID in both places | The claim gets its own UUIDv7, stored in `claim_event` and carried as `claim` in the event's details | No change to the audit contract; the row and the event join on it | The column holds a claim ID, not the event's row ID |
| (b) The recorder returns the event ID | `RecordTx` answers the ID it assigned | The column holds what its name says | A contract change for every producer of every area |
| (c) Leave `claim_event` empty | — | Nothing to build | The claim cannot be traced to its event |

**Choice:** (a). The details: `claimed` names the repository, domain, format, package, the claim ID, the reason and the
owners; `added` the owner, its principal ID and the reason (`added` or `pre-assigned`); `removed` and `released` the owner and
its principal ID, with the journal entry of R-AUTHZ-031 (domain, format, normalized name, principal ID). A refusal is
`authz.denied` with `deny.ownership`.

**Rationale:** the link is what an investigation needs, and (a) gives it without touching the audit contract. The development
agent would revisit this if the recorder starts returning event IDs for another reason.

**Applied in the spec:** R-AUTHZ-018 (the data model), R-AUTHZ-029 ("Ownership events").

### Q-AUTHZ-160 · A raw path of a format with package names is decided on its package, and a path without one fails a name set

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: `/files/` and the items API decided an npm item on its path alone: the coordinate of
`@secret/x/-/x-1.0.0.tgz` carried no package, so a grant with `packages.exclude: ["@secret/*"]` read, listed and deleted
it (an empty include list passes a coordinate without a value), a version rule of an exposure could not see the version,
and a name pattern of the repository (`@x/*`) never matched the path `@x/y/-/…`, so excluded content was served
(R-REPO-005). The format's classification existed (`FilesSeam.Classify`) and nothing called it.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Classify before the decision, and fail closed without a package | The generic layer looks the repository up in the snapshot (the one lookup before a decision, R-ARCH-011 step 2), asks the format for the package and version of the path and decides on them; a listing and a subtree are decided per path with a classifier handed to `PathsAllowedOn`; the items operations' shared step completes its coordinate the same way; the repository's name patterns are matched against the package. A package dimension with any pattern does not match a non-repository coordinate of a format with package names that carries no package | One coordinate for a path whichever road reaches it; a path the format could not place is refused, not admitted | A scope folder (`@acme/`) names no package and is closed to a grant that narrows names |
| (b) Classify only | As (a) without the rule for a coordinate without a package | Scope folders stay listable under an exclude-only grant | A path a classifier misses (a future layout, a file stored before a rule) passes every exclude-only grant again |
| (c) A coarse check, then a fine one | Decide repository-level first, resolve, classify, decide again | No lookup before the first decision | Two decisions per request, two audit events of a refused write, and a 403 that names the repository instead of the coordinate |

**Choice:** (a). `files.Service.locate` reads the repository from the snapshot and keeps the outcome of the lookup until the
decision allowed the request, so a missing repository and a hidden one answer alike; the coordinate carries `Package` and
`Version` from `Formats.Classify`; `authz.Classifier` completes each path of `PathsAllowedOn`; `files.Service.Classified`
completes the coordinate the shared step of `getItem`, `deleteItem`, `listChildren` and `getItemStats` decides on; and the
patterns of a format whose subject is `name` are matched against the package, a path without one being package-less and
admitted (R-REPO-005). In `globSet.match`, a dimension with a name rule (a format with package names) and any pattern fails a
coordinate without a value; paths, versions and tags keep the rule of R-AUTHZ-005, and so does a generic repository.

**Rationale:** the ownership, the exposure and the patterns of a package format all act on names; a road that hands the
decision point a path alone makes every one of them blind, and a rule that fails closed keeps the next such road from
reopening it. The price is the listing of a scope folder for a grant that narrows names, which an include list already paid;
packages stay reachable by their own folders and paths. The development agent would revisit this if a format gains paths
that hold content of several packages, or if browsing scope folders under an exclude-only grant becomes a need (then the
format would classify a scope as a name prefix).

**Applied in the spec:** R-AUTHZ-005 (a coordinate without a package of a format with package names; the order of R-AUTHZ-016,
whose one lookup before a decision is the repository's, is kept), [endpoints.md](endpoints.md) R-EP-014, [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-005. Code:
`internal/authz/{pattern.go,paths.go}`, `internal/files/{files.go,formats.go,download.go,children.go,delete.go,deploy.go}`,
`internal/formathost/hooks.go` (`FilesSeam.Classify`), `internal/api/v1/authorize.go` (`Server.classified`).

### Q-AUTHZ-161 · A removal of format content through `/files/` and the items API asks the ownership question

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. **Superseded in part by [storage.md Q-STOR-190](storage.md#q-stor-190--the-delete-job-decides-every-batch-again-on-what-the-subject-that-asked-holds-then)**: the delete job asks the ownership question again for the files of every batch, on the caller as it stands then, so the window of H-154 for an insert in flight is closed.

Context: the npm routes that change a package ask the ownership question under the package row lock; a delete through
`/files/` or the items API removed a tarball, a package folder or a whole scope (through the job `items-delete` above
`storage.delete.max_sync_items`), and the removal hook that unpublishes the versions runs with a writer bound to no request,
which cannot ask. A holder of `delete` who owns nothing could unpublish another team's versions for good.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A format seam in the removing transaction | `files.Formats.CheckRemoval` gets the files the removal takes away, with the request's index and subject; the host classifies them, locks every package row in name order and asks `Ownership.EnforceTx` with `delete`, in the transaction that removes the files or queues their job, before anything goes | Every road of a removal asks once, where the rows are; the job carries what the request decided, as it carries the `delete` decisions | One package lock and one ownership read per package of the subtree |
| (b) Ask in the removal hook | Bind the hook's writer to the request | The question sits next to the unpublish | The job's batches run with no request; the hook runs after rows are gone |
| (c) The job path needs `manage` or `content:bypass` | Decide on the index alone for a large subtree | No row read for the job | An owner cannot remove a large package of its own through `/files/`; two rules for one question |

**Choice:** (a). `items.DeleteSpec.Extra` receives the paths of the files of the subtree (those `Check` saw, up to `MaxID` for
a job); the generic layer records the allowed write and calls `CheckRemoval`; `formathost.FilesSeam.CheckRemoval` asks for a
local repository of a format with package names, one question per package in name order under `EnsureLocked`, with
`OwnershipWrite{}` (a removal is no publish and no first version), and refuses when no ownership rule is bound or no index
is given. A folder that holds a package of another owner is refused whole (`403 package_owned_by_other_owner`), and a
refused job is never queued. A file the format places in no package, and a folder alone, ask nothing.

**Rationale:** (a) keeps the rule of R-AUTHZ-018 ("in the writer's commit transaction, under the package row lock") for every
road that removes content, without a second rule for large subtrees. The job decides nothing, so what the request asked
holds for it, with the window of H-154 for an insert in flight. The development agent would revisit this when the trash can
(R-STOR-022) turns a delete into a recoverable move, or if the cost of very large scopes in one transaction is measured.

**Applied in the spec:** R-AUTHZ-018 ("Which writes are checked"), [storage.md](storage.md) R-STOR-018,
[formats/npm.md](formats/npm.md) R-NPM-017. Code: `internal/items/delete.go`, `internal/files/{formats.go,delete.go}`,
`internal/formathost/hooks.go`, `internal/formats/npm/hooks_remove.go`.

### Q-AUTHZ-162 · The items API deletes outside the patterns and in a disabled repository for `content:bypass` only

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: `DELETE /api/v1/repositories/{key}/items` ignored the repository's include and exclude patterns and its disabled
flag for every caller, so every holder of `delete` had a road past the routing rules that `/files/` applies; R-REPO-005 says
the management API shows *administrators* items outside the patterns so they can be removed.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Strict for every caller but `content:bypass` | The patterns and the disabled flag apply as under `/files/`; a holder of `content:bypass` deletes outside them | One rule for every road a non-administrator takes; the cleanup R-REPO-005 names stays | An operator with `delete` and without the role cleans up through an administrator |
| (b) Strict for everyone | No exception | Simplest | The items stored before a pattern change cannot be removed without changing the patterns back |
| (c) As before | Non-strict for everyone | — | The road around the routing rules stays |

**Choice:** (a). The handler asks `HoldsOn(content:bypass)` on the request's index and passes `Strict` to the files service
otherwise. Reads of the items API stay as they are: they show items outside the patterns to whoever may read them. Package
ownership applies to every caller; `content:bypass` passes it as it passes every ownership check.

**Rationale:** the patterns are routing rules for every caller (R-REPO-005), and the exception exists for one task, which is
an administrator's. The development agent would revisit this if a capability for maintenance work below `admin` is
introduced.

**Applied in the spec:** [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-005, [storage.md](storage.md) R-STOR-018.
Code: `internal/api/v1/items_api.go` (`DeleteItem`), `internal/files/delete.go`.

### Q-AUTHZ-163 · A scope on a virtual repository covers its members when they are read through it

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the token that **Create a token for this setup** (R-UI-016) mints for a virtual repository holds `read` on that repository only. R-AUTHZ-009 decides the content of a read on the member that holds it, and R-AUTHZ-013 intersects every decision with the scope, so every member refused the token and each package of the virtual repository answered 404. The token could pass the gate and read nothing.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The evaluator treats a scope element that admits `read` on a virtual repository as admitting `read` on the members that resolution consults on behalf of that repository | In `allowedAction`, a `read` whose coordinate carries `Addressed` = a virtual repository that reaches the member (directly or through nested virtual repositories) is admitted when the scope admits `read` on that virtual repository for the same package and path | One rule for every token and every client; no copy of the members in the scope that goes stale; the members stay configuration the token's creator need not see; the intersection with the subject's grants is untouched | The reach of the token follows the membership of the virtual repository, which an administrator changes |
| (b) The setup page writes the members into the scope | The scope lists `npm-all` and each member | No change to the evaluator | A copy that is stale as soon as a member is added or removed; the page needs the member list, which is configuration (`repositories:read-config`), and a reader without that right could not create a working token |
| (c) A new scope field, such as `through: ["npm-all"]` | A scope names the virtual repositories whose members it covers | Explicit | A schema and an API change for what R-AUTHZ-009 already means by reading through a virtual repository; every client of the scope API would have to know |

**Choice:** (a), narrowed by Q-AUTHZ-164 to elements that list the exact key of the virtual repository, with the addressed repository carried on every member request (Q-AUTHZ-165). Only `read` is lifted: a virtual repository serves reads from its members, and writes go to the deploy target, which a scope names itself (the setup page adds the target with `deploy` when the user asks for it). Only a request that reaches the member through the virtual repository is lifted: the coordinate must carry the addressed virtual repository and that repository must reach the member. The member addressed directly, a repository that is no member, and every action other than `read` are decided by the scope as before. The element's `packages` and `paths` still narrow, and the grants of the subject are evaluated first and unchanged: the lift removes a refusal by the scope and grants nothing, so a token never reads more than its owner reads through the same virtual repository.

**Rationale:** the scope of R-AUTHZ-013 is a ceiling of the same kind as the exposure of an endpoint, which also names the repository the client addressed (Q-EP-11) and not its members. Naming the virtual repository is naming the endpoint a client uses. The development agent revisits this if scopes gain a way to name a repository by what it resolves through for another purpose, or if membership changes need to be bound to token lifetimes.

**Applied in the spec:** R-AUTHZ-013 (repository part), R-AUTHZ-009 (rule 2). Security: [tokens-and-credentials.md](../security/tokens-and-credentials.md). Code: `internal/authz/decide.go` (`scopeAdmitsThrough`, `reaches`). Tests: `TestR_AUTHZ_013_AScopeOnAVirtualRepositoryCoversItsMembersWhenReadThroughIt`, `TestR_AUTHZ_013_ASetupTokenOfAVirtualRepositoryReadsThroughIt`.

### Q-AUTHZ-164 · Only a scope element that names the virtual repository by its exact key lifts

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: Q-AUTHZ-163 lets a scope that admits `read` on a virtual repository admit its members for a request that went through it. A scope element names repositories by key, key glob or class selector (R-AUTHZ-004), so `@any-virtual` or `npm-*` would lift for every virtual repository they cover, including ones created after the token.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Exact key only | Only an element whose `repositories` list holds the key of the virtual repository lifts | What the setup page creates; a token reaches the members of the repositories it names and of none that merely match a pattern | A scope of `@any-virtual` finds no package through a virtual repository (it names the endpoint, not the content) |
| (b) Any selector | As Q-AUTHZ-163 first stated | Uniform | A pattern or class widens a token to members of repositories created later |

**Choice:** (a). `scopeAdmitsThrough` filters the elements to those that list the key and evaluates only them.

**Rationale:** a pattern in a scope is written for the repositories that exist when it is read, and a lift that follows it makes the reach of the token depend on repositories nobody had in mind. The development agent revisits this if a client needs a scope by class to read through virtual repositories; the scope would then list the keys.

**Applied in the spec:** R-AUTHZ-013, Q-AUTHZ-163. Code: `internal/authz/decide.go` (`scopeAdmitsThrough`). Test: `TestR_AUTHZ_013_AScopeOnAVirtualRepositoryCoversItsMembersWhenReadThroughIt`.

### Q-AUTHZ-165 · A member request carries the virtual repository it was made for

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the lift of Q-AUTHZ-163 needs `Addressed` on the coordinate of every decision about a member. The member read check (`MemberUsableOn`) set it; the populate check of a remote member (`MayPopulateOn` in the remote engine, also when `Gate` revalidates a stale member document) was built from the member request without it, so an uncached tarball of a remote member was refused for a token scoped to the virtual repository (authz.denied, then 404 or 403).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `Via` on `formats.Request` | The virtual tiers copy the request for the member and set `Via` to the addressed repository; `populateCoordinate` turns it into `Addressed` | One field, set at the one place that builds a member request; the exposure decision on that path follows the addressed repository as Q-EP-11 says | A new field of the request |
| (b) The populate check looks the virtual repository up | The engine infers it | No new field | Guessing: a member can belong to several virtual repositories |

**Choice:** (a). `Via` stays the outermost repository when virtual repositories nest (R-REPO-018).

**Rationale:** the decision needs the repository the client named, which only the request knows. The development agent revisits this if member requests are built at more than one place.

**Amended 2026-11-01 (Q-ARCH-252):** the revisit condition was met (the tiers of the virtual engine, the npm audit forwarder and the re-target of a write-intent read each built a member request, and the read check of the forwarder's member carried no addressed repository, so a token scoped to the virtual repository by its exact key could not run `npm audit` through it). The field is now unexported (`via`); a member request is made only by `Request.Member(m)`, which keeps the outermost addressed key and refuses a repository that is no member; `Request.Addressed()` is the one reader, and both coordinate builders (`coordinate()` for `Host.Authorize`, `populateCoordinate`) put it into `Coordinate.Addressed`. A read that stands for a write (`GET {name}?write=true`, R-NPM-029) is derived with `Request.WriteTarget`, which carries no addressed key: the scope must name the deployment target itself. `virtual.RemoteCall.Via` is `RemoteCall.Addressed`.

**Applied in the spec:** R-AUTHZ-009, R-AUTHZ-013. Code: `internal/formats/route.go` (`Request.Addressed`, `Member`, `WriteTarget`; was `Request.Via`), `internal/formathost/virtual.go` (`forMember`), `internal/formathost/host.go` (`coordinate`), `internal/formathost/remote.go` (`populateCoordinate`). Tests: `TestR_AUTHZ_013_AMemberRequestCarriesTheOutermostAddressedKeyIntoBothCoordinates`, `TestR_AUTHZ_013_ASetupTokenRunsTheAuditThroughTheVirtualRepository`, `TestR_AUTHZ_013_ThePopulateCheckOfARemoteMemberFollowsTheAddressedVirtualRepository`, `TestR_AUTHZ_013_ASetupTokenFillsAndReadsARemoteMemberThroughTheVirtualRepository`.

### Q-AUTHZ-166 · The path classifies a write to an npm package document

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-AUTHZ-007 said a package document `PUT` is classified by comparing it with the stored document (an added version `deploy`, a removed version `delete`, changed dist-tags or deprecation messages `deploy`, several of them every action). The E3.4 tests found that the format does not compare: `PUT {name}/-rev/{rev}` is the unpublish route (action `delete`, it removes the versions the document lacks and ignores the document's dist-tags), `PUT {name}` is the publish route (`deploy`), and a `PUT {name}` without an attachment is the metadata update of `npm deprecate`, which answers 404 until it is built (R-NPM-043).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The path classifies | The route fixes the action: revisioned `PUT` is `delete`, unrevisioned `PUT` is `deploy`, tags and deprecation never ride on the unpublish route | What npm sends is exactly two request shapes, each with one purpose; no request can need two actions, so no partial-grant case exists | A client that sends a hand-made document to the revisioned route cannot move a tag with it (it never could) |
| (b) Compare with the stored document | Diff the document against the stored one and require every action involved | Matches the sentence the spec carried | The body must be read and diffed before the check; the revisioned route would write tags, a second way to change them next to the dist-tag endpoints; more code on the one path that must stay a pure removal |

**Choice:** (a).

**Rationale:** the comparison was never built and nothing needs it: npm unpublish, dist-tag and publish each have their own request shape. The development agent revisits this if deprecation is built on the revisioned route or a client is found that changes tags through the document.

**Applied in the spec:** R-AUTHZ-007 (the paragraph on the package document `PUT`, the per-format notes). Tests: `TestR_AUTHZ_007_NPMActionMap` (the cases for unpublish, dist-tag add and remove, and the revisioned document `PUT`).

### Q-AUTHZ-167 · npm whoami and ping need a subject that can read the repository

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-AUTHZ-007 and R-NPM-019 gave `npm whoami` (and `npm ping`) the requirement "an authenticated subject" and no action. The route is registered with the action `read`, so an authenticated subject without `read` on the repository gets 404 (R-AUTHZ-016), an anonymous caller 401.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Authenticated and able to read the repository | Keep the route as built; the spec says so | A subject who may not read a repository does not learn from `whoami` or `ping` that it exists (R-AUTHZ-016); a client that can install or publish can always read | `npm whoami` against a repository the subject cannot read fails with `E404` instead of printing the name |
| (b) Any authenticated subject | The route needs no action | `whoami` works against any repository URL | Any valid token confirms which repository keys exist; a second code path that skips the repository check |

**Choice:** (a).

**Rationale:** the only subjects that use the endpoint are npm clients configured for a repository they use, and those hold `read` at least (`npm publish` and `npm install` need it). The development agent revisits this if a client is found that calls `whoami` before it holds any grant.

**Applied in the spec:** R-AUTHZ-007 (npm table), [formats/npm.md](formats/npm.md) R-NPM-019. Tests: `TestR_AUTHZ_007_NPMActionMap` (the `whoami` and `ping` cases).

### Q-AUTHZ-168 · A listing through a virtual repository decides its rows as reads through it

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the read scope of a listing (`authz.ReadScoper.ReadScopeOn`, [api-and-ui.md](api-and-ui.md) R-API-029) classifies repositories and decides single rows with `MayRead`. It built each row's coordinate without `Addressed`. The package screens list across repositories, and there each repository is decided on its own. NuGet's merged search (R-NUGET-018) lists the rows of a virtual repository's members for the virtual repository: decided on the members, the exposure of the members applied instead of the virtual repository's (Q-EP-11), and a token scoped to the virtual key (Q-AUTHZ-163) found nothing, while the documents of the same repository were decided through it (the code review and the security audit of 2026-11-02).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `ReadScopeOn` takes the addressed repository; `""` decides each key on its own; a key the addressed repository is not and does not reach is `ReadNone` (chosen) | `MayRead` sets `Coordinate.Addressed`; the formats' `Resolver.ReadScope` passes `rq.Addressed()`; the package screens and the endpoint check pass `""` | `MayRead` is `DecideOn` for the coordinate a request through the virtual repository has, so the exposure of the addressed repository and the lift apply as they do to a document; the frozen `Resolver` signature stays | Every caller of `ReadScopeOn` chooses the addressed repository |
| (b) A second method for a listing through a virtual repository | `ReadScopeThrough(idx, sub, virtual, keys)` | The existing callers stay untouched | Two near-equal methods that must agree with `DecideOn` |
| (c) Leave the decision point as it is and let the format filter | The format applies the virtual repository's view | No change here | The lift can only be applied by the decision point; every format would repeat Q-EP-11 |

**Choice:** (a). The shape of a key the addressed repository reaches is classified as before: an endpoint with rules or a scope with elements makes it `ReadRows`, so no row is admitted without `MayRead`. A key that is neither the addressed repository nor reached by it (`Index.reaches`) is `ReadNone`, and `MayRead` answers false for it: the listing asked about a read no client made through that repository.

**Rationale:** the addressed repository is a fact of the request (Q-AUTHZ-165), and a listing that belongs to a request must be decided the way the request's documents are. The development agent revisits this when nested virtual repositories arrive: the reach walk then needs the visited set of H-327, and `Addressed` stays the outermost repository.

**Applied in the spec:** R-AUTHZ-009 rule 4; [formats/nuget.md](formats/nuget.md) R-NUGET-018 step 1 (Q-NUGET-75). Code: `internal/authz/visible.go` (`ReadScopeOn`, `readScope.MayRead`, `Index.addressedReaches`), `internal/formathost/virtual.go` (`ReadScope`), `internal/api/v1/packages_api.go` and `endpoints_api.go` (pass `""`). Tests: `TestQ_AUTHZ_168_AListingThroughAVirtualRepositoryAppliesItsExposure`, `TestQ_AUTHZ_168_AListingThroughAVirtualRepositoryLiftsATokenScopeThatNamesIt`, `TestQ_AUTHZ_168_ARepositoryTheAddressedOneDoesNotReachIsNotListed`, `TestQ_AUTHZ_168_AReadScopeThroughAVirtualRepositoryAgreesWithTheDecisionPoint`, `TestR_API_029_BridgeReadScope`.

### Q-AUTHZ-169 · A technical account for a project belongs to the project

Status: **Decided by the owner on 2026-11-02** — option (a).

Context: the owner wants project administrators who create technical accounts for external parties and grant them fine
or coarse access to chosen repositories of their project. R-AUTH-020 lets only an administrator create service accounts;
owners who manage a service account's tokens are P1, and R-AUTHZ-026 lets a project administrator grant to service
accounts but not create them.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Project service accounts (chosen) | A project administrator creates, disables and deletes service accounts of the project, named `<projectKey>-…`; it issues and revokes their tokens (endpoint-bound tokens included); they are granted only through the project's policies; a project with service accounts is not empty | No system administrator per partner; what a project creates never reaches beyond the project; the partner is one identity that the audit names and that can be disabled | A `project` attribute on service accounts and its rules |
| (b) Global service accounts with project administrators as owners | An administrator creates the account and names an owner who manages its tokens | The owner model of R-AUTH-020 exists (P1) | Every new partner needs a system administrator |
| (c) No accounts, project tokens on roles | Project administrators mint short tokens without an identity | Least model | No identity to audit or disable per partner |

**Choice:** (a), the owner's decision on the development agent's recommendation.

**Rationale:** external partners need an identity that can be disabled and that the audit names, and the project
administrator creates it without a detour; nothing a project administrator creates acts outside the project.

**Applied in the spec:** R-AUTHZ-026 (project service accounts and the rules that follow from them, Q-AUTHZ-174 to
Q-AUTHZ-185); R-AUTH-020.

### Q-AUTHZ-170 · System role bindings declared in the configuration

Status: **Decided by the owner on 2026-11-02**.

Context: the owner's IdP group `gh-wds18-k8s-main-admins` (a GitHub team behind Dex) is synchronized into trove, and it
should make its members administrators. Role bindings existed only as objects of the API, and the installation is
provisioned from a GitOps repository; configuration documents (R-OPS-029, P1) are not built.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `authz.role_bindings` in the system configuration, applied by `trove migrate`, read-only in the API and the UI (chosen) | The pattern of Q-CI-16 for CI issuers | GitOps for who administers trove; a binding holds before the first login | A second source of bindings beside the API |
| (b) Bind once through the API as the break-glass administrator | No code | Works today | Not in Git; lost with a restore into an empty installation |

**Choice:** (a), the owner's request ("a configuration option in Helm to make an OIDC group the administrator").

**Rationale:** the owner's decision; it follows Q-CI-16. The development agent records no revisit condition.

**Applied in the spec:** R-AUTHZ-010 (bindings from the configuration, one acceptance criterion).

### Q-AUTHZ-171 · A configuration binding follows the rules of the API

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: Q-AUTHZ-170 says that `trove migrate` applies `authz.role_bindings`, that the API refuses `DELETE` of a
configuration binding and lists it with its source. It leaves open what `PUT` answers for such a binding, what happens to a
binding the API made that is declared later, and whether the guards of R-AUTHZ-022 (`auto_join_admin_conflict`,
`last_admin`) bind `trove migrate` as they bind a request.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The rules of the API bind both ways (chosen) | `PUT` of a configuration binding answers 200 and leaves its source; a declared binding the API made is taken over; a declared auto-join group and the removal of the last active administrator stop `trove migrate` with an error that names the entry | `PUT` stays idempotent for scripts that ensure a binding; no Git commit can lock every administrator out or put a role on everyone; the Job fails before the release starts, where the change was made | A GitOps change that drops the old administrator group together with an empty new one fails until a member of the new group has signed in or break-glass is on |
| (b) The configuration wins | `trove migrate` removes the last administrator, or turns auto-join off on a declared group | The declaration always applies | One commit locks administration out, or silently changes a group's membership rule |
| (c) `PUT` answers 409 like `DELETE` | Every write of a configuration binding is refused | One rule for both methods | A client that ensures a binding fails although the binding exists as asked |

**Choice:** (a).

**Rationale:** `PUT` asks for a state that holds, so 200 is the honest answer, and leaving the source keeps the binding
the configuration's to remove. Taking a declared API binding over makes the declaration the one record of that binding
without a second row. The guards exist because a single change must not leave the installation without an administrator
or give a role to every member of an auto-join group; a change that arrives through Git is such a change too, and it
fails in the migration Job, before any replica runs the new release. The development agent revisits this if an
installation needs to hand administration over from one IdP group to another in one release without break-glass.

**Applied in the spec:** R-AUTHZ-010 (the rules of the API bind a declaration), R-AUTHZ-022 (the `PUT` and `DELETE` rows,
the `source` column). Tests: `TestR_AUTHZ_010_BindingsFromTheConfiguration`, `TestR_AUTHZ_022_DeclaredRemovalKeepsAnAdministrator`.

### Q-AUTHZ-172 · The auditor reads every repository's metadata and triages, but downloads nothing

Status: **Decided by the owner on 2026-11-02**.

Context: the `auditor` role read configuration, identities and the audit log but held no content rights, so it saw no
package and no version; triage of vulnerability findings belonged to `security-admin` and needed `read` on the finding's
repository, which includes downloads. The owner: the auditor has instance-wide access to all projects, metadata and
above all vulnerabilities, takes part in triage instance-wide, may change nothing except security metadata, and can
neither download, upload nor change artifacts; the role is bound to a group through the Helm configuration.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A capability `content:metadata-read` for metadata of every repository, `KindArtifact` refused; `vulnerabilities:triage` for the auditor, effective with metadata read; the role MVP (chosen) | The decision point allows `read` on metadata, documents and search through the capability and never on an artifact | The authorization already tells metadata from artifacts (`KindMetadata`, `KindSearch`, `KindArtifact`); one capability, no per-repository policy to keep current | Metadata of every repository is visible to the role, internal names included |
| (b) Policies granting `read` on every repository to an auditor group | No new capability | Uses what exists | `read` includes downloads, and new repositories need the policy extended |

**Choice:** (a), the owner's definition of the role.

**Rationale:** the owner's decision. The development agent records no revisit condition.

**Applied in the spec:** R-AUTHZ-010 (the capability `content:metadata-read`, the triage row), R-AUTHZ-011 (the auditor
paragraph, its priority).

### Q-AUTHZ-173 · A project administrator creates remote repositories

Status: **Decided by the owner on 2026-11-02**.

Context: `manageRepositories` lets a project administrator create the project's repositories. A remote repository opens
an outbound path to an upstream of the creator's choice, so the question was whether the switch covers remote
repositories or whether they stay with system administrators.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `manageRepositories` covers every class, remote included (chosen) | A project administrator creates local, remote and virtual repositories of the project | Teams set up their own proxies of public registries without a system administrator | A new outbound path per remote; bounded by the installation's outbound address rules |
| (b) Remote repositories stay with system administrators | A separate switch or `repositories:write-config` | Every new upstream is seen by an administrator | Every team needs an administrator for each proxy |

**Choice:** (a), the owner's decision ("ja klar").

**Rationale:** the owner's decision. The outbound address rules of the installation (allow and deny lists, private-address
refusal) apply to every remote, whoever created it. The development agent records no revisit condition.

**Applied in the spec:** R-AUTHZ-026 (`manageRepositories` covers every class).

### Q-AUTHZ-174 · The grants of a project policy live where the previous release does not read them

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the released 0.9.x binaries build their grant index from `authz_policy`, `authz_policy_selector`,
`authz_policy_pattern` and `authz_policy_entry` and know no `project_id`. During a rolling upgrade they run next to the
new replicas for as long as the rollout lasts (or longer after a rollback). A project policy whose pattern or class
selector such a replica reads as global would grant on every repository of the installation what the project
administrator wrote for the project: a grant beyond the project, issued by a delegate. Only a contract step raises the
compatibility floor of the schema (R-HA-018), and nothing in an expand step stops an old binary from reading a row.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The grants of a project policy in a table of their own (chosen) | `authz_project_policy_entry` holds the entries of project policies; `authz_policy_entry` refuses a row for a project policy (trigger, SQLSTATE `AI004`). A 0.9.x replica reads a project policy as a policy without entries: it covers nobody | Fail closed by the schema, not by a rule the code keeps; a rollback after project policies exist stays safe; a 0.9.x write of a project policy fails instead of making it global | Two entry tables: the build, the policy read, the write and the principal cleanup read both |
| (b) No project policy until no 0.9.x replica runs | The write of a project policy checks the replica registry (`ReplicasUpTo`, as R-HA-022 does for a data migration) | One table | A replica that starts later, or a rollback, reads existing project policies as global; the check runs at write time only |
| (c) Accept the window | Project policies are rare during a rollout | No work | A delegate's grant beyond its project, in a security feature, on every old replica |
| (d) Store project selectors in a form the old build cannot compile | A key pattern the old builder logs and skips | One table | A skipped policy is an error log per build on every old replica, and a pattern is a contract between two releases kept by accident |

**Choice:** (a).

**Rationale:** the old build skips what it cannot grant on and grants only through entries, so removing the entries from
its view removes the policy from its grants without any cooperation. The database makes both halves binding: the entry
table's composite foreign key to `(policy_id, project_id)` allows a row for a project policy only, and the trigger on the
old table allows none. Project administrators' own `manage` and the project service accounts need no such step: an old
replica ignores `project_admin` (the administrator gets no `manage` there, which refuses more) and a project service
account holds no grant on it except through project policies. What the window costs is availability, not
security: on an old replica a project's rights are absent. The development agent revisits this when no installation can
still run 0.9.x, and then merges the two tables in a contract step.

**Applied in the spec:** R-AUTHZ-026 (project policies), R-AUTHZ-028 (data model, index inputs), R-HA-018 (no change).
Code: `internal/meta/postgres/migrations/00019_projects.sql`, `queries/authz.sql` (`AuthzBuildEntries`,
`AuthzPolicyEntries`, `AuthzInsertProjectEntry`), [ADR-145](../decisions.md#adr-145--the-grants-of-a-project-policy-are-stored-where-the-previous-release-does-not-read-them).

### Q-AUTHZ-175 · A project service account is granted by its project's policies only

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: Q-AUTHZ-169 says a project service account is granted only through the project's own policies. Service accounts
can today be group members (R-AUTH-018, R-AUTH-020), named in any policy, bound to roles and owners of packages. A
membership in a global group, an entry in a global policy or a role binding would give a project's account rights a
project administrator never granted, and a project administrator could not see them.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Refuse every path outside the project's policies (chosen) | A project service account is no group member (422 `project_account_not_groupable`, trigger `AI002`), no role holder (`role_not_allowed`, as for any non-bindable principal), no entry of a global policy or of another project's policy and no package owner outside its project (422 `principal_outside_project`); the database refuses the role binding, the global entry and a foreign project's entry with `AI005` ([Q-AUTHZ-203](#q-authz-203--the-migration-of-projects-is-expand-class-in-seven-steps-and-the-database-refuses-a-project-account-as-a-global-principal)) | What the project administrator sees in its policies is everything the account can do; no global group changes it | One more rule in five places |
| (b) Allow groups, let the administrator of trove take care | Only policies and roles are limited | Fewer rules | A group the project administrator cannot see widens the account; the account's grants are no longer the project's |
| (c) Allow global policies naming the account | An administrator of trove may grant | Flexible | An account created by a delegate then has rights the delegate cannot review |

**Choice:** (a). A project policy may name a global service account (a shared robot): it narrows nothing of the global
account's own rights, and widens only the project's repositories, which the project administrator controls.

**Rationale:** the account is the project's; its grants are the project's policies. An administrator who wants an account
with rights beyond a project creates a global service account. Revisit when sharing between projects (IAM-053 and the
catalog's repository sharing, P2) arrives: that is where a cross-project grant of an account belongs.

**Applied in the spec:** R-AUTHZ-026 (project service accounts); R-AUTH-020 (properties); R-AUTHZ-010 (role bindings,
`role_not_allowed`). Code: migration `00019_projects.sql` (trigger functions), `authz` policy validation, `authz`
`bindable`, `principal` group membership guard, the package owner operations.

### Q-AUTHZ-176 · Names of a project are prefixed with its key, and a global name never takes a project prefix

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. **Superseded by [Q-AUTHZ-209](#q-authz-209--a-projects-policies-and-service-accounts-are-named-within-the-project-administrators-keep-instance-wide-ones) and [Q-REPO-212](repositories-and-proxy.md#q-repo-212--a-repository-is-addressed-inside-its-project-and-no-repository-is-shared)**: a repository key is unique within its project and the names of a project's policies, service accounts and CI mappings are unique within the project, so no name carries a prefix and no overlap rule is left.

Context: R-AUTHZ-026 requires the keys of repositories a project administrator creates to start with `<projectKey>-`.
The names of project policies and project service accounts are chosen by the same delegate, in global namespaces, and
projects `web` and `web-app` would let the administrator of `web` create `web-app-…` names that read as the other
project's. An administrator of trove may also create a global service account `web-bot` next to project `web`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One rule: names are `<key>-…`, keys do not overlap, global accounts do not take project prefixes (chosen) | Project repositories, policies and service accounts start with `<key>-` (422 `project_prefix_required`); a project key is no dash-prefix of another key (409 `project_key_overlap`); a global service account name or a new global policy name starting with an existing project's key and a hyphen is refused (422 `name_reserved_by_project`, for policies [Q-AUTHZ-200](#q-authz-200--a-global-policy-never-takes-a-projects-prefix-and-a-project-policy-names-no-auto-join-group)), and a new project whose key and hyphen start the name of a global account is refused (409 `project_key_overlap`) | A name tells its project; a delegate cannot squat or impersonate another namespace; no oracle on global policy names (a project policy name is always in the project's own space) | Projects `web` and `web-app` cannot coexist |
| (b) Prefix for repositories only | The text of R-AUTHZ-026 as it stood | Less to explain | A delegate creates a global policy named like another team's, or a service account `admin-ci`, and a 409 answers whether a global name exists |
| (c) Prefix without reservation | Names are prefixed, keys may overlap | Simple | Two projects own overlapping name spaces; `web-app-x` is created by either |

**Choice:** (a). An existing repository keeps its key when it is assigned: only creation by a project administrator needs
the prefix. A global repository an administrator creates is unrestricted.

**Rationale:** a service account's name is its only link to its project in the audit log of other systems, and it cannot
be reassigned, so the rule is enforced at creation and by a trigger. The development agent revisits the key overlap rule
when an installation needs `web` and `web-app` side by side; the remedy is then a longer, structured separator.

**Applied in the spec:** R-AUTHZ-026 (the project, project policies, project service accounts); R-AUTHZ-021 (`project` and
the name rule). Code: `internal/project` (key overlap), `authz` policy validation, `internal/auth/principal`
(`checkNewServiceAccount`), `internal/repository` (create), migration trigger `AI003`.

### Q-AUTHZ-177 · What a project administrator may do with service accounts and their tokens

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: Q-AUTHZ-169 gives project administrators the creation, disabling and deletion of project service accounts and the
issue and revocation of their tokens. Open: which of the two switches gates it, what a token they issue may be, and what
stays possible when a switch is off.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `managePolicies` gates what widens; narrowing stays open (chosen) | Creating and enabling an account, issuing a token: `managePolicies`. Disabling, deleting, revoking tokens, changing the description, reading: administration only. A token a project administrator issues is never non-expiring (`Admin: false` of the token service), lives at most as long as the installation's maximum for service-account tokens (`auth.tokens.sat.max_ttl` and its runtime setting, R-AUTH-040), has `admin: false` and no `system` item (422 `invalid_scope`); `inherit`, repository grants and an `endpoints` binding are allowed | An account is the addressee of grants, so it belongs to the grants side; what only reduces is never blocked; the token rules need no new setting | A project with `managePolicies` off cannot onboard a partner |
| (b) A third switch `manageAccounts` | Its own switch | Finest control | A third privilege the owner did not ask for, in every UI, API and test |
| (c) No switch, every administrator | Simplest | None to explain | An administrator the installation allowed to read only creates credentials |
| (d) Capabilities in the scope allowed | Tokens may carry `system` items | Flexible | A project administrator could mint a credential for management operations the account's grants never gave |

**Choice:** (a).

**Rationale:** the grants of the account are the project's policies, which `managePolicies` governs; an account without a
switch to create it is rarely wanted, and a switch that can be turned off must not trap what exists: a stolen
credential is revoked, an account removed, whatever the switches say. The scope rules mirror the derivation rule of
R-AUTH-027 (a token never carries more than its owner holds): the account holds repository rights only. A recent
authentication is required of a session that issues a token (R-API-007). The development agent revisits the single
switch when an installation wants to let a team issue tokens but not create accounts.

**Applied in the spec:** R-AUTHZ-026 (project service accounts, the table of operations), R-AUTH-020, R-AUTH-027.

### Q-AUTHZ-178 · A virtual repository of a project administrator stays inside the project

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: a project administrator with `manageRepositories` creates virtual repositories (Q-AUTHZ-173). Members and the
deploy target are rows of the virtual repository (R-REPO-017). Content is decided on the member, never on the virtual
repository (R-AUTHZ-009), so a foreign member cannot be read through it by someone who may not read it, and a write
through the virtual repository needs `deploy` on the target. Open: may a project's virtual repository contain repositories
the project does not own, or write into one?

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Members and deploy target of a project administrator's write must be project repositories, the members already stored excepted (chosen) | A write by a project administrator without `repositories:write-config` adds only project repositories to the member list and sets a deploy target in the project: 422 `repository_outside_project`, for an unknown key the same. Members that an administrator of trove placed stay when the list keeps them | A project can never reference what it cannot see; the answer is no oracle on other repositories; the shared public proxy stays an administrator's decision to add | A team that wants its virtual to include the shared `npmjs` proxy asks an administrator once, or creates its own remote (Q-AUTHZ-173) |
| (b) Any repository | Only the content decision limits | Teams compose freely | Existence of every repository key can be tested by a delegate; a member the delegate cannot see changes what the project's clients receive, and its shadowing check names it |
| (c) Any repository the project administrator may read | Reads are decided per member | Reasonable | A project administrator holds no read through its role; the member list would depend on grants that change |

**Choice:** (a).

**Rationale:** the check is one comparison per new member in the write transaction. An existing member outside the project
is never "new", so an administrator's composition survives edits by the project administrator (and cannot be extended by
it). The development agent revisits this when repository sharing between projects (P2) exists: a shared repository is then
a legitimate member.

**Applied in the spec:** R-AUTHZ-026 (the `manageRepositories` right), R-REPO-017 (note).

### Q-AUTHZ-179 · A repository that leaves a project leaves its policies inert

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. **Superseded by [Q-REPO-217](repositories-and-proxy.md#q-repo-217--a-repository-moves-to-another-project-through-an-operation-of-its-own-and-only-while-nothing-of-its-project-names-it)**: a move is refused while a key selector of a project policy names the repository, so no project policy keeps a selector outside its project.

Context: project policies may select repositories of their project by key (R-AUTHZ-026). A key selector stores the
repository by ID. When the repository is unassigned or moved, the old project's policies still hold that selector.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The selector stays and covers nothing; the policy warns (chosen) | A project policy covers a repository only while the repository belongs to the policy's project (checked when the index is built). The stored selector stays; the read of the policy reports `selector_outside_project`; assigning the repository to the project again brings the grant back | One check in the build; nothing is deleted behind a project administrator's back; the revocation takes effect with the assignment's epoch on every replica; the policy's journal state is unchanged | A moved-away repository's grants come back if it returns to the old project |
| (b) The assignment deletes such selectors | The write edits the project's policies | The state is explicit | An assignment (by a repository-admin) silently edits another delegate's policies, and each edited policy must be journaled |
| (c) Unassigning is refused while a project policy selects it by key | 409 | Explicit | A repository cannot leave a project without first editing its policies, by a delegate who may be gone |

**Choice:** (a).

**Rationale:** it is the behaviour of a selector whose repository is deleted (R-AUTHZ-004: the policy stays and reports
itself inert), applied to a repository that merely left the project, and it fails closed. Pattern and class selectors need
no case: they are evaluated against the project's repositories only. The development agent revisits it if the return of a
grant after a move back proves surprising; the remedy is then (b) with a journaled edit.

**Applied in the spec:** R-AUTHZ-026 (project policies), R-AUTHZ-021 (warnings).

### Q-AUTHZ-180 · A declaration admits project administrators in a form of its own, and the handler narrows

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. **Superseded in part by [Q-AUTHZ-208](#q-authz-208--four-project-roles-reader-developer-maintainer-owner) and [Q-REPO-212](repositories-and-proxy.md#q-repo-212--a-repository-is-addressed-inside-its-project-and-no-repository-is-shared)**: the declaration names the project from the path and the least role (`project: path.projectKey`, `projectRole`), and the shared step narrows to that project itself.

Context: the shared authorization step decides every operation from its `x-trove-permission` declaration (R-API-008). A
project administrator holds no capability, and which project an operation addresses is often known only to the handler
(a service account's name, a repository key in a body). Open: how the document says "capability X, or administrator of
the addressed project".

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `{permission: <cap>, projectAdmin: <right>}` and a narrowing helper (chosen) | The step admits a holder of the capability, or a caller that administers at least one project, whatever the declared right (`any`, `managePolicies`, `manageRepositories`; see the amendment below); the handler narrows with `requireProject`, `requireProjectOfRepository` or `delegatedScope`. The capability is asked as a question first, so a project administrator causes no denial event | The document says it; the contract tests and the UI read the declaration; a refused caller is refused before any lookup; no handler forgets the first check | The step is coarse: a project administrator of `web` passes the step for an operation on `docs` and is refused by the handler (404) |
| (b) `authenticated: true` and a handler check | As `createServiceAccountToken` does | No new form | The document no longer says who may call; a forgotten check is an open operation |
| (c) A resolver in the step per operation | The step names the project source (`path.key`, a lookup of a repository's project) | Exact | A source for each case (body, a service account's project) and a second lookup before the handler |

**Choice:** (a), [ADR-146](../decisions.md#adr-146--an-operation-admits-project-administrators-through-a-declaration-of-its-own).

**Rationale:** the existing forms stay as they are; a declaration with the new member is a superset of the capability
form, so a build that does not know the member refuses the document at start-up (`parseRequirement`). The refusal rules
(R-AUTHZ-016) are fixed in one place: unknown or not administered project 404, administered with the switch off 403
`project_switch_off`. The development agent revisits this when a third party-facing right needs a per-object source.

**Applied in the spec:** R-API-008 (the form), R-AUTHZ-026 (the table of operations). Code: `internal/api/v1/authorize.go`,
`project_scope.go`, `internal/authz/project.go`.

**Amendment (Decided by the development agent, 2026-11-02; the owner reviews it at the walkthrough): the step asks for
administration, not for the right.** As first built, the step admitted a caller only if one of its projects held the
declared right. A caller whose only project had the switch off then got 403 `forbidden` ("The permission
identities:write is required."), while a caller that also administered another project with the switch on got 403
`project_switch_off` for the same request: the answer depended on projects the request did not address. Now the step
for `managePolicies` and `manageRepositories` admits every caller that administers at least one project (any right), as
`any` always did. The handler narrows: a project the caller does not administer answers 404 (a policy body naming one,
422 `unknown_project`, as before), the addressed project's switch off 403 `project_switch_off`, and every delegated
operation answers it from its narrowing helper. A caller that administers no project is still refused by the step with
the capability's 403 and its audit event. Revisit when an operation needs the right known before the handler runs.

### Q-AUTHZ-181 · /authz/me names the projects the caller administers

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. **Superseded in part by [Q-AUTHZ-207](#q-authz-207--a-user-or-group-is-a-member-of-a-project-with-a-role-and-the-role-grants-the-projects-repositories)**: `/authz/me` lists the caller's memberships with their roles, not the projects it administers with their switches.

Context: the UI shows the admin area from the capabilities of `GET /authz/me` (R-UI-040 ff.). A project administrator holds
none, yet needs the project screens.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A member `projects` (chosen) | `[{key, managePolicies, manageRepositories}]`, ordered by key, from the same index as the rest of the answer; `[]` for a caller that administers none and for a token without `admin: true` | One request; the epoch of the answer is the epoch of the projects; the screens hide actions whose switch is off | Another member of a stable answer |
| (b) The UI calls `GET /projects` | Reuse | No new member | Two requests that may answer on two indexes; a 403/404 probe decides navigation |

**Choice:** (a).

**Rationale:** navigation and action visibility need what the credential may exercise now, from the index that decides. The
development agent records no revisit condition.

**Applied in the spec:** R-AUTHZ-022 (`GET /authz/me`), R-UI-041, R-AUTHZ-026.

### Q-AUTHZ-182 · How a project administrator finds the users and groups it names

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: a project policy names users and groups. Listing them needs `identities:read`, which a project administrator does
not hold, and giving it would expose every identity, e-mail address and membership of the installation.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A lookup bounded to the project administrator's need (chosen) | `GET /projects/{key}/principals?q=&kind=&limit=`: active users and groups whose name starts with `q` (2 characters at least), at most 20, the kind and the name only; for `identities:read` or an administrator of the project with `managePolicies` | Names are found by what the administrator knows; nothing but kind and name leaves; cheap | Names of users and groups can be enumerated by prefix (H-392) |
| (b) Free text in the policy form, no lookup | The administrator types a reference; an unknown one answers 422 `unknown_principal` | No new operation | The same enumeration through 422, without paging bound, and typing names blindly |
| (c) `identities:read` for project administrators | Full lists | Simple | Every identity of the installation, with e-mail, visible to every team lead |

**Choice:** (a). Besides it, a project administrator reads: the project, its administrators, its policies, the full
configuration of its repositories, its service accounts and their tokens' metadata. Nothing else of the global data.

**Rationale:** the question is "which principals exist" for the grant it is about to write, not "who is who". The
development agent revisits it when the identity provider's directory can answer the lookup (the IdP then owns the answer).

**Applied in the spec:** R-AUTHZ-026 (the API table, the right table). Gap: [H-392](#h-392--a-project-administrator-can-enumerate-user-and-group-names).

### Q-AUTHZ-183 · Who administers a project and through which credentials

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-AUTHZ-026 says "users and groups". Open: whether a service account or a CI mapping may administer, and whether
a token exercises the right.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Users and groups; sessions and `admin: true` tokens (chosen) | A project administrator is a user, or a member of a listed group (group memberships as the authentication stage resolved them). A service account, a CI mapping, `anonymous` and `authenticated` are refused (422; trigger `AI001`). A token exercises the right only with `admin: true`, as it exercises a system role (R-AUTHZ-010) | A machine never administers, so a stolen CI or service-account token changes no grant; a token narrowed to repository rights stays narrow | A script that administers a project needs an `admin: true` token of a user |
| (b) Any principal | Service accounts too | Automation | A credential that grants rights to itself |
| (c) Every token of the user | Tokens inherit | Convenient | A narrowed token could still administer |

**Choice:** (a).

**Rationale:** one rule for system roles and project rights keeps the token model whole (R-AUTHZ-013). The group
memberships come from the identity provider as for roles; whoever controls the group controls the project (H-394).

**Applied in the spec:** R-AUTHZ-026, R-AUTHZ-013 (no change).

### Q-AUTHZ-184 · The repository operations of a project administrator

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. **Superseded by [Q-REPO-212](repositories-and-proxy.md#q-repo-212--a-repository-is-addressed-inside-its-project-and-no-repository-is-shared)**: the repository operations are addressed under the project (`/api/v1/projects/{projectKey}/repositories/{repoKey}…`), option (b).

Context: `repositories:write-config` and `repositories:read-config` are installation-wide capabilities. A project
administrator needs their effect on its repositories only: create, read the whole document, change, delete, test the
upstream, and see them in listings.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The same operations, narrowed by the handler (chosen) | `POST /repositories` names `project`; replace, patch, delete and the two upstream tests act on repositories whose project the caller administers with `manageRepositories` (404 otherwise, as for a repository the caller may not see); listings show the project's repositories with the full document; the read of one returns the full document to its project's administrators | No second family of routes; the answers of an outside repository do not differ from an unknown one | The handler of each operation carries the narrowing |
| (b) A project-scoped route family (`/projects/{key}/repositories…`) | Separate paths | Obvious | A second set of every repository operation, and two documents for one repository |

**Choice:** (a). A repository that moves out of the project while a project administrator edits it answers 404 on the
next request, because the decision is made on the request's index. The `project` of an existing repository is read-only
on replace and patch.

**Rationale:** the repository model stays one. The development agent records no revisit condition.

**Applied in the spec:** R-AUTHZ-026 (table of operations), R-REPO-009 (permission row).

### Q-AUTHZ-185 · Versions, delete order, the epoch and the journal of project writes

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the project's `version` is its ETag (R-API-012); deleting a project cascades to policies; every write notifies an
area; project events take access away.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The version covers the fields of the PUT; the delete removes the policies first through the policy store; project writes carry the epoch; removals are journaled (chosen) | `version` moves with display name, description and the two switches, not with administrators or assignments. `DELETE` counts repositories and accounts under the project's row lock, deletes each policy through `authz.Service` (audit and journal as for any policy deletion), then the project; the foreign keys are the safety net. Every write answers `Trove-Authz-Epoch`. `authz.project.updated` (a switch turned off), `.deleted`, `.admin_removed` and `.repository_unassigned` are flagged `journal: revocation`; a move is an unassignment and an assignment in one transaction | A stale `PUT` fails only for the fields it overwrites; every revocation is journaled; the policy deletions are in the audit log | A delete of a project with many policies is many events |
| (b) The version moves with every write | Any change moves the tag | Simplest | Adding an administrator makes the next `PUT` of the switches fail with 412 for no reason of its own |
| (c) Rely on the cascade | `ON DELETE CASCADE` only | Less code | The deletions are neither audited nor journaled, so a restore brings the policies back |

**Choice:** (a).

**Rationale:** the ETag protects the switches (R-AUTHZ-026); the journal exists so that a restore does not undo a
revocation (R-AUTHZ-031). The replay rules are in R-HA-054 and R-AUTHZ-031. The development agent records no revisit
condition.

**Applied in the spec:** R-AUTHZ-026, R-AUTHZ-029, R-AUTHZ-031, R-HA-054 (table rows).

### Q-AUTHZ-186 · The project of a policy is fixed at creation

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-AUTHZ-021 says the project of a policy is fixed at creation and gives no answer for a `PUT` or a merge patch
that carries another `project` (or none) for an existing policy. A project policy's entries are in
`authz_project_policy_entry`, a global policy's in `authz_policy_entry` (Q-AUTHZ-174), so a change of project is a move
between tables.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Immutable; a change is 422 `immutable` at `/project` (chosen) | The service compares the requested project with the stored one before it applies the rules of the new project, in the write transaction again under the row lock; the merge patch is refused by the handler's immutability check, as a changed `name` is | The same answer as for `name`; no window in which the entries sit in the wrong table; a delegate cannot widen a project policy into a global one | A team that wants to hand a policy to another project writes a new one and deletes the old |
| (b) A `PUT` may move the policy | The entries move between the tables | One call | The move is a revocation and a grant in one write, journaled as an update with two projects; a project administrator would write into a project it was not administering when the policy was read |
| (c) Immutable, refused with 409 | A conflict, not a validation error | None | The state is not in conflict, the input is invalid |

**Choice:** (a).

**Rationale:** the project decides which table holds the grants, which repositories the policy covers and who may write it;
none of these may change under a delegate. The development agent revisits it when a hand-over of a policy between
projects is asked for; the remedy is then an explicit operation, not a change of the member.

**Applied in the spec:** R-AUTHZ-021 (the members), R-AUTHZ-026 (project policies). Code: `Service.Update`,
`PatchPermissionPolicy` (`internal/authz/store.go`, `internal/api/v1/authz_api.go`).

### Q-AUTHZ-187 · What a project administrator sees and is refused on the policy operations

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. **Superseded in part by [Q-REPO-212](repositories-and-proxy.md#q-repo-212--a-repository-is-addressed-inside-its-project-and-no-repository-is-shared) and [Q-AUTHZ-209](#q-authz-209--a-projects-policies-and-service-accounts-are-named-within-the-project-administrators-keep-instance-wide-ones)**: a project's policies are addressed under the project, so the step narrows to the path's project and no list needs `project=`.

Context: the policy operations declare `policies:read` or `policies:write` and `projectAdmin` (Q-AUTHZ-180); the shared
step admits a caller that administers any project with the right, and the handler must narrow to the project the request
names. The document gives no `404` for the list, and the contract page names a few cases only.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Narrow in the handler; the answer never tells a foreign project from an unknown one (chosen) | List without `project=<key>`: 403 `forbidden`; with a project the caller does not administer or that does not exist: 403 `forbidden`. Get of a global policy or of another project's policy: 404. Replace, patch, delete of another project's policy: 404; of a global policy: 403. Create and validate with a project the caller does not administer or that does not exist: 422 `unknown_project` at `/project`; without a project: 403. A switch that is off: 403 `project_switch_off` | Every refusal is in the document today; no answer tells whether a project or a policy outside the caller's reach exists, except that a write of a global policy is 403 (the declaration already told the caller that policies exist) | A project administrator sees 403, not 404, on the list of a project it does not administer |
| (b) 404 for the list of a foreign project | As for a single policy | One rule for "not yours" | The list operation documents no 404; adding one regenerates the TypeScript types another package owns |
| (c) The policy service decides on the caller | `Service` takes the caller's projects | One place | The service becomes caller-aware for one kind of caller; the validation (`Validate`) is also a read for others |

**Choice:** (a).

**Rationale:** the handler already holds the request's index through `delegatedScope`, and the service stays a function of
its input. A caller that administers no project with the switch is refused at the declaration with `forbidden`; the
handler's `project_switch_off` is therefore reachable only for a caller that administers another project with the switch
on, which is the case that needs the explanation. The development agent revisits (b) when the list gains a `404` for
another reason.

**Applied in the spec:** R-AUTHZ-021 (operations table), R-AUTHZ-026. Code: `narrowPolicyList`, `narrowPolicyRead`,
`narrowPolicyWrite`, `narrowPolicyWriteOf`, `narrowPolicyValidate` in `internal/api/v1/authz_api.go`.

### Q-AUTHZ-188 · A project policy that names anonymous or authenticated is a principal outside the project

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. **Superseded by [Q-AUTHZ-279](#q-authz-279--a-policy-entry-a-binding-a-membership-or-an-owner-record-that-names-anonymous-or-authenticated-is-refused)**: no policy, global or of a project, names `anonymous` or `authenticated`; both answer 422 `principal_not_allowed`.

Context: R-AUTHZ-026 says a project policy never grants to `anonymous` or `authenticated` and names no code. The existing
codes of the entry rules are `anonymous_write_action` and `authenticated_write_action`, which refuse an action beyond `read`,
not the principal.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `principal_outside_project` at `/entries/<i>/principal` (chosen) | The same code as a service account of another project: the principal is not one the project may name | No new code; one rule for "who a project may name"; the entry is refused before its actions are looked at | The name suggests a project, not a built-in principal |
| (b) The existing write-action codes | `read` only entries would pass | None | The refusal is the principal, whatever the action |
| (c) A new code | `principal_not_allowed` | Exact | A new code in the registry and the document for one rule |

**Choice:** (a).

**Rationale:** the rule is "a project policy grants to users, groups, service accounts of its project or global ones, and CI
mappings"; every entry outside that set is a principal outside the project. The development agent revisits it with the
projects' anonymous access (IAM-062, P2), which would lift the rule.

**Applied in the spec:** R-AUTHZ-026 (project policies), R-AUTHZ-030. Code: `Service.projectRule`
(`internal/authz/validate.go`).

### Q-AUTHZ-191 · An assignment moves the repository's configuration version and tells both areas

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the project is a member of the repository's document (`project`), and the assignment writes `repositories.project_id`.
R-AUTHZ-026 names the two change areas an assignment notifies and says nothing of the repository's `config_version`, its ETag
(R-API-012) and the version a cache keyed by it compares (Q-NUGET-94).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The assignment moves `config_version` and notifies `repo_config` with op `updated` (chosen) | The statement that sets `project_id` also does `config_version = config_version + 1`; the `repo_config` payload is the repository package's own (`id`, `key`, `op`, `version`) | A client that read the repository before the assignment gets 412 on a write and no 304 for a document that has another `project`; replicas refresh their snapshot of repositories (which carries `project_id`) the way they do for any change | Caches keyed by the version drop the repository's entries of older versions on an assignment, which is rare |
| (b) The assignment leaves `config_version` | Only `project_id` changes | No cache drop | The ETag of the document stays while the document changes; a conditional GET answers 304 for a stale `project` |
| (c) A version of its own for the project member | A second revision column | Precise | A second ETag on one resource |

**Choice:** (a). The op is `updated`: the repository package's vocabulary is the event names without `repository.`, and the
only ops with a meaning of their own are `deleting` and `deleted`.

**Rationale:** correctness of the conditional requests outweighs one cache refill on a rare, administrator-only write. The
development agent revisits it when the repository package gives assignments an op of their own.

**Applied in the spec:** R-AUTHZ-026 (assignment); code in `internal/project` and `ProjectAssignRepository`.

### Q-AUTHZ-192 · A project delete is not one transaction

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: Q-AUTHZ-185 deletes the policies one by one through the policy store so that each deletion is audited and journaled,
then the project. The policy store opens a transaction of its own per deletion.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Read and refuse first, delete the policies, then one closing transaction that locks, counts again and deletes the project (chosen) | A read refuses an unknown project, a stale `If-Match` and a project that is not empty before any policy is touched; the closing transaction repeats the checks under the project's row lock, and a policy created meanwhile sends the delete round again (three rounds at most) | Uses the policy store unchanged; a refused delete deletes nothing in every case a client can cause; the foreign keys of repositories and service accounts remain the safety net | A failure between the policy deletions and the closing transaction (a crash, a repository assigned in the gap) leaves a project without some of its policies (H-398) |
| (b) A policy-store method on the caller's transaction | One transaction | Atomic | A change in the policy store, which another work package owns |
| (c) Rely on the cascade | `ON DELETE CASCADE` only | Less code | Q-AUTHZ-185 (c): unaudited, unjournaled deletions |

**Choice:** (a).

**Rationale:** every refusal a client can provoke comes before the first policy deletion; what is left is a crash or a race
in a window of milliseconds, and its result (a project with fewer policies than it had) takes access away and never grants.
The development agent revisits it when the policy store offers a deletion on the caller's transaction.

**Applied in the spec:** R-AUTHZ-026 (deleting a project). Gap: [H-398](#h-398--a-failed-project-delete-can-leave-the-project-without-some-of-its-policies).

### Q-AUTHZ-193 · What an administrator write that changes nothing does

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: `PUT /projects/{key}/admins/{principalRef}` answers 201 or 200 (R-AUTHZ-026); assigning a repository to the project it
already belongs to answers success.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A no-op is not a write (chosen) | It records no audit event, notifies no area and moves no epoch; it answers the current `Trove-Authz-Epoch`, so that a client that waits for the epoch of its request does not wait for a change that never comes | The audit log holds changes only; an idempotent client is not punished | The header of a no-op is the epoch of the last change, not of the request |
| (b) A no-op records an event and raises the epoch | Every call is an event | Every call visible | Noise; a retry loop floods the log and invalidates the indexes of every replica |

**Choice:** (a). A user or group named in the reference is read in the transaction of the write, not from the principal
store's cache: the answer is the one the write commits against.

**Rationale:** the audit log and the change log exist for changes (R-AUTHZ-029, R-AUTHZ-028).

**Applied in the spec:** R-AUTHZ-026 (API table).

### Q-AUTHZ-194 · Project creation is serialised, and the overlap rule reads one state

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. **Superseded by [Q-AUTHZ-209](#q-authz-209--a-projects-policies-and-service-accounts-are-named-within-the-project-administrators-keep-instance-wide-ones)**: no overlap rule between project keys and names is left, so the unique key is the only check of a creation.

Context: the overlap rule of Q-AUTHZ-176 compares a new key with every other key and with the names of global service
accounts; two creates of `web` and `web-app` would each pass it against the state the other has not committed.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) An advisory lock on the key space (chosen) | Every create takes the transaction-scoped configuration lock of its own object type (`cluster.ConfigProjects`, R-ARCH-017) before it checks the rule | Exactly one of two overlapping creates wins, whichever replica answers; no schema change | Creates of projects queue behind each other, a rare administrator write |
| (b) A database constraint | An exclusion constraint on the key prefix | Declarative | It cannot express the rule against service account names; a schema change |
| (c) Check without a lock | The state of the read | Simplest | The race |

**Choice:** (a). The lock covers project creation only; a global service account created in the same instant is H-399.

**Rationale:** project creation is rare and administrator-only, so the queue costs nothing.

**Applied in the spec:** R-AUTHZ-026 (the key rule). Gap: [H-399](#h-399--a-global-service-account-and-a-project-key-created-at-the-same-instant-can-overlap).

### Q-AUTHZ-195 · What a project administrator gets for a request that names no project it may use

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. **Superseded in part by [Q-REPO-212](repositories-and-proxy.md#q-repo-212--a-repository-is-addressed-inside-its-project-and-no-repository-is-shared) and [Q-AUTHZ-208](#q-authz-208--four-project-roles-reader-developer-maintainer-owner)**: every request below a project names it in the path, and the switches and `project_switch_off` are gone.

Context: the shared step admits a project administrator to an operation by capability or by a project right; the handler narrows. Open: the answer when the request names no project (a create without `project`, a list of service accounts without `project=`), and the order of the answers for a project that is unknown, unadministered or switched off.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) 403 `forbidden` without a project; 404 for a project it does not administer; 403 `project_switch_off` for one it administers; one 403 for a token issue that is none of its business (chosen) | The caller learns what to send, never whether a foreign project, repository or account exists | No existence oracle; one rule for every operation of the family | A 403 where a 422 would also read well |
| (b) 422 for a missing project | A validation error | Reads like a form | A 422 is answered only after the caller's right is known; the same request from a caller without any right is a 403 |

**Choice:** (a).

**Rationale:** R-AUTHZ-016 asks for answers that do not tell what the caller may not see. The development agent records no revisit condition.

**Applied in the spec:** R-AUTHZ-026 (the declaration paragraph). Code: `internal/api/v1/repositories_api.go`, `internal/api/v1/groups_api.go`, `internal/api/v1/tokens_api.go`, `internal/api/v1/upstream_api.go`.

### Q-AUTHZ-196 · The manage of a project administrator is decided from the administration, not from a grant per repository

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. **Superseded in part by [Q-AUTHZ-208](#q-authz-208--four-project-roles-reader-developer-maintainer-owner)**: `manage` on the project's repositories comes from the role of a Maintainer or an Owner, decided from the membership.

Context: the index materialised one grant `project:<key>` per project repository and administrator principal, keyed by the
principal ID of the administrator. The decision matches a grant for every principal of the request's set, and that set holds the
user, its groups and, for a service account, the account and its groups. So a global service account that is a member of an
administrator group held `manage` on every repository of the project through its tokens, and a user's token without
`admin: true` held it while `GET /authz/me` named no project for it. Both contradict [Q-AUTHZ-183](#q-authz-183--who-administers-a-project-and-through-which-credentials)
and R-AUTHZ-026 (a project is administered through a session or a token with `admin: true`, by users and groups).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Decide `manage` from the administration (chosen) | The index holds no grant for it. `manage` on a repository of a project is allowed when the subject administers that project by the rule of the three project questions (`projectInfo.administeredBy`: a user or a member of a listed group, a session or a token with `admin: true`); the decision names `project:<key>` as its source. The token's repository scope still applies, as for a grant | One rule for the questions and the decision, so they cannot disagree; no P×R grants, so the build and the index memory no longer grow with projects times repositories | `manage` is no entry of the grant map any more; the consumers go through the decision, which they did already |
| (b) Keep the grants and filter them in the decision | The decision skips a `project:` grant unless the subject administers | A smaller change | Two places hold the rule; the grants stay a trap for the next reader of the map |
| (c) Key the grants by credential | Grants for sessions only | No change in the decision | A token with `admin: true` needs a second kind of grant; a group still expands |

**Choice:** (a).

**Rationale:** every consumer of `manage` (the decision, package ownership, cache invalidation, claims, the virtual shadow check,
`Effective`) asks the engine's decision, so none reads a grant directly; `ReadScope` and the read shapes look at `read` only,
which a project administrator never holds by this rule. The development agent records no revisit condition.

**Applied in the spec:** R-AUTHZ-026 (the right table). Code: `internal/authz/decide.go` (`grantedBy`), `project.go`
(`administeredBy`, `administersRepo`, `ProjectsOn`), `index.go` (`addProjectAdminGrants` removed).

### Q-AUTHZ-197 · What a project administrator's policy list and validation tell about other objects

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: a caller that holds `policies:read` only as a project administrator lists the policies of one project. The filters
`principal` and `repository` answered 422 for a name that does not exist and an empty list for one that does, so the filter tested
which principals and repositories exist; `expandGroups` (default on) tested the group memberships of any user; and a project
service account of another project in a validated project policy answered `principal_outside_project` where a name that does not
exist answers `unknown_principal`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The same answer for what does not exist and what is not the caller's (chosen) | For a project administrator an unknown principal or repository in a filter answers 200 with an empty list, as one of another project does; the groups of a principal are not expanded, and `expandGroups=true` is 403 `forbidden`; another project's service account in a project policy answers `unknown_principal`. A caller that holds the capability keeps the 422 answers and `principal_outside_project` | No oracle for existence or membership; no new code | `expandGroups=true` is refused although the parameter exists |
| (b) Refuse the filters for a project administrator | 403 for `principal`, `repository`, `expandGroups` | Simplest to explain | The UI filters a project's policies by principal |
| (c) Leave it | The answers as they were | No work | Existence and membership tests for any user, group, repository and project account |

**Choice:** (a). The malformed reference stays 422 for every caller, as it tells nothing about existence.

**Rationale:** R-AUTHZ-016 asks for answers that do not tell what the caller may not see. The development agent records no
revisit condition.

**Applied in the spec:** R-AUTHZ-021 (the filters), R-AUTHZ-026 (the entries). Code: `internal/authz/policy_list.go`
(`ListFilter.Delegated`), `validate.go` (`projectRule`, `AsDelegate`), `internal/api/v1/authz_api.go` (`narrowPolicyList`).

### Q-AUTHZ-198 · A project service account outside its project is refused in every path and skipped by the build

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: [Q-AUTHZ-175](#q-authz-175--a-project-service-account-is-granted-by-its-projects-policies-only) refuses a project service account as a group member (trigger `AI002`), as a role binding and as an entry of a global policy or another project's policy, in the write. Two paths did not: the role bindings declared in `authz.role_bindings` (a `service_account` that belongs to a project), and the build, which compiled whatever the tables held.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Refuse in the declaration and fail closed in the build (chosen) | The declaration is refused with `role_not_allowed` like the API; `BuildIndex` skips an entry of a global policy or of another project's policy that names a project service account, and a role binding of one, and logs each skip at error level. The two entry tables and the role binding table refuse such a row with SQLSTATE `AI005` (a trigger of the project work package); the writes map it to 422 `principal_outside_project` and `role_not_allowed`. The snapshot carries the account's project on each entry and binding row | A row that got there by SQL, by a rollout or by a restore grants nothing | A skip is a log line, not a metric (`BuildIndex` is a pure function without an engine) |
| (b) Write-time refusals only | The state before | No work | A row from another path grants the account |
| (c) A metric as well | A counter of skipped rows | Alertable | A registry in a pure function; the log carries the policy and the principal |

**Choice:** (a).

**Rationale:** skipping only removes rights, as for every row that cannot be compiled. The development agent revisits the log line when an installation needs to alert on it; the remedy is a counter on the engine's install step.

**Applied in the spec:** R-AUTHZ-026 (project service accounts). H-397 is closed by the trigger `AI005` ([Q-AUTHZ-203](#q-authz-203--the-migration-of-projects-is-expand-class-in-seven-steps-and-the-database-refuses-a-project-account-as-a-global-principal)), which refuses such a row from any writer, a replica of the previous release included. Code: `internal/authz/declared.go` (`resolveOne`, `declare`), `index.go`, `store.go` (`writeChildren`), `bindings.go`, `queries/authz.sql` (`AuthzBuildEntries`, `AuthzBuildBindings`).

### Q-AUTHZ-199 · A delegate's policy write is checked under the policy lock, and its project lookups run in the write's transaction

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the handler narrows a project administrator to the project of a stored policy before the write, and the write compared only the project of the body. A policy deleted and created again under the same name (a global one, or another project's) between the two was then rewritten or deleted by the delegate. And the project and account lookups of a policy write opened a transaction of their own per lookup, also once per entry of a listed page: a second connection per lookup, and a create racing a project delete reached the foreign key (500 instead of 422).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The delegate's project goes into the write; the lookups use the writer's queries (chosen) | `authz.AsDelegate(project)` is a write option of `Create`, `Update`, `Delete` and `Validate`; `Update` and `Delete` compare the stored policy's project with it after `AuthzLockPolicy` (a global policy 403, another project's 404). `Update` resolves inside its transaction, after the lock; `CreateIn` resolves on the caller's transaction; `Annotate` reads a page in one transaction. A foreign key violation on the insert is 422 `unknown_project` | No window between the narrowing and the write; one connection per write | `Update` holds its transaction while it validates |
| (b) Lock the project row as well | `FOR SHARE` on the project in every write | No FK window at all | A lock per policy write against the project service, which takes `FOR UPDATE`; the FK already refuses |
| (c) Leave it | The state before | No work | The race; a connection per lookup |

**Choice:** (a). The key of the project in the write option, not a flag. The same check serves a caller that is no delegate:
`authz.ExpectProject(project)` (nil for a global policy) makes `Update` and `Delete` answer 404 for a policy that does not belong to
the project, so the delete of a project, which removes the policies it listed by name and version, never removes a policy that was
deleted and created again under the same name in another project or as a global one.

**Rationale:** the rules of the service read the database, so the check under the lock is the same as the one in the handler on a state that cannot change under it. The development agent records no revisit condition.

**Applied in the spec:** R-AUTHZ-026 (project policies). Gap: [H-396](../security/permissions.md#h-396--a-project-administrators-policy-write-is-decided-on-the-replicas-index) is unchanged (the index staleness). Code: `internal/authz/store.go` (`Update`, `Delete`, `delegateOwns`, `CreateIn`), `validate.go` (`WriteOption`, `txProjects`), `policy_list.go` (`Annotate`).

### Q-AUTHZ-200 · A global policy never takes a project's prefix, and a project policy names no auto-join group

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. **Superseded in part by [Q-AUTHZ-209](#q-authz-209--a-projects-policies-and-service-accounts-are-named-within-the-project-administrators-keep-instance-wide-ones)**: a global policy may take any name; the refusal of an auto-join group in a project policy stays.

Context: [Q-AUTHZ-176](#q-authz-176--names-of-a-project-are-prefixed-with-its-key-and-a-global-name-never-takes-a-project-prefix) promises "no oracle on global policy names" and refuses global service account names with a project's prefix, but a global policy could still be named `web-…`; a delegate's create of `web-x` then answered 409 `already_exists` for a global policy of that name. Separately, an auto-join group holds every user that signs in, so an entry for it in a project policy stands in for `authenticated`, which a project policy never names ([Q-AUTHZ-188](#q-authz-188--a-project-policy-that-names-anonymous-or-authenticated-is-a-principal-outside-the-project)).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Reserve the prefix for new global policies; refuse auto-join groups in project policies, whatever the action (chosen) | A new global policy named `<key>-…` of an existing project is 422 `name_reserved_by_project` (create and validate; an update of a policy that already has such a name is left alone); an auto-join group in a project policy is 422 `principal_outside_project`, and the build skips such an entry should a group turn auto-join afterwards | One rule for the names of all three objects; `authenticated` and the auto-join group are treated alike | A global policy `web-…` that exists before the project `web` stays (H-405) |
| (b) Correct the text of Q-AUTHZ-176 | Say that global policies are exempt | No code | The 409 oracle stays |
| (c) Refuse auto-join groups for write actions only | `read` stays open | Smaller change | `authenticated` is refused for read, so the group would be a way around it |

**Choice:** (a).

**Rationale:** the refusal of `authenticated` in a project policy is for a reason that holds for read as well: what a project grants it grants to people it named. The development agent records no revisit condition.

**Applied in the spec:** R-AUTHZ-026 (project policies, entries), [Q-AUTHZ-176](#q-authz-176--names-of-a-project-are-prefixed-with-its-key-and-a-global-name-never-takes-a-project-prefix) (option (a) reads "a global service account or policy name"). Gap: [H-405](#h-405--a-global-policy-that-predates-a-project-keeps-the-projects-prefix). Code: `internal/authz/validate.go` (`resolveProject`, `projectRule`), `index.go`, `queries/authz.sql` (`AuthzProjectKeyPrefixing`, `AuthzBuildEntries`).

### Q-AUTHZ-201 · A project administrator's refusals are audited as the denial of the capability

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: a project administrator passes the shared step through the project declaration and is refused by the handler's narrowing (a project it does not administer, a switch that is off, a repository of another project). No layer audited that: the engine audits the capability checks and the decisions on repositories, and the refusals of the narrowing are neither.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The narrowing records the capability's denial (chosen) | A refusal of a narrowing asks the access for the operation's capability once more, as a caller that holds none; the engine answers and audits `authz.denied` as for any capability refusal (the dedup window, `audit.record_anonymous_denials`), with the permission, the reason and the credential | The rules of R-AUTHZ-029 apply unchanged; no new event type | The event names the capability, not the project |
| (b) A new event `authz.project.denied` | The project and the right in the details | More precise | A second event family for the same fact; the schema and the table of events grow |
| (c) Leave it | No event | No work | A delegate's probing of other projects leaves no trace |

**Choice:** (a).

**Rationale:** the refusal is a denied request like the other; its details are those of the capability. The project is in the request log. The development agent revisits it when an installation asks to filter the denials by project (H-406).

**Applied in the spec:** R-AUTHZ-029 (the denied events). Gap: [H-406](#h-406--the-audit-event-of-a-refused-project-administrator-does-not-name-the-project). Code: `internal/api/v1/project_scope.go` (`DelegatedScope.noteRefusal`, `refuseProject`), `authz_api.go`.

### Q-AUTHZ-202 · A read of a project is never answered with 304

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the ETag of a project is its `version` (R-API-012), which by Q-AUTHZ-185 moves with the fields a `PUT` replaces and not
with administrators or assignments. The body of `GET /projects/{key}` holds administrators and counts, so a client that sent the
tag as `If-None-Match` got `304` for a body that had changed.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The read does not honour `If-None-Match` and always answers the whole body; the tag stays for `If-Match` (chosen) | `getProject` has no 304 response and no `If-None-Match` parameter in the OpenAPI description; the `ETag` header is still sent | A client can never hold a stale body behind a valid tag; the `PUT` precondition keeps its meaning (version) | A project read is never cheaper than the body, which is small |
| (b) A validator that covers the whole body | A second tag derived from the body (or a counter that every administrator change and assignment moves) | Conditional reads work | Either a second tag next to `version`, which a client must not confuse with the `If-Match` one, or the version moves with administrators, which Q-AUTHZ-185 (b) rejects |

**Choice:** (a).

**Rationale:** a project is read by a few administrators and the UI; saving a small body is not worth a second validator.
Revisit when a client polls the project at a rate where the body matters.

**Applied in the spec:** R-AUTHZ-026 (the read). Code: `internal/api/v1/projects_api.go`, `api/openapi.yaml`.

### Q-AUTHZ-203 · The migration of projects is expand-class in seven steps, and the database refuses a project account as a global principal

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: migration 00019 (not released when this was decided) built indexes and a unique constraint on existing tables inside
one transaction and added the foreign keys already valid, against R-HA-018's expand class. Q-AUTHZ-175 also left the refusal of a
project service account as the principal of a role binding or of a global entry to the code alone.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Seven steps: tables, columns, triggers and the foreign keys `NOT VALID` (00019); one `CREATE INDEX CONCURRENTLY` step per index (00020 to 00023); the unique constraint from its index, the entry table and the guards (00024); `VALIDATE CONSTRAINT` (00025) (chosen) | The step shape of R-HA-018: no scan under a long lock, every step idempotent or transactional | The lock on `repositories`, `service_account` and `authz_policy` is short at every step; a failed index build leaves an `INVALID` index the runner drops | Seven files for one feature; the composite foreign key waits for the unique constraint, so the entry table is created in 00024 |
| (b) Keep one file | As before | One file | A rollout blocks writes of repositories while an index is built and a foreign key validated |
| (c) Leave the unique constraint inline | `ADD CONSTRAINT ... UNIQUE` | Fewer steps | It builds the index under `ACCESS EXCLUSIVE` on `authz_policy` |

The guard of Q-AUTHZ-175 is a trigger function with SQLSTATE `AI005`: a project service account is refused as the principal of
`authz_role_binding` and of `authz_policy_entry`, and as the principal of an `authz_project_policy_entry` of another project's
policy. A global service account and the project's own accounts pass. The API answers 422 before it gets there.

**Choice:** (a), with the guard.

**Rationale:** a rolling upgrade must not hold writes (R-HA-018), and the rule that keeps a delegate's account inside its project
should not depend on one code path. The development agent records no revisit condition.

**Applied in the spec:** R-HA-018 (no text change), R-AUTHZ-026 (data model). Code: `internal/meta/postgres/migrations/00019_projects.sql`
to `00025_projects_validate.sql`; the guards are listed in [database.md](../developer/database.md#projects-tables-00019-to-00025).

### Q-AUTHZ-204 · A project delete passes the version it read as the If-Match of each policy deletion

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the delete (Q-AUTHZ-192) removed the policies of a project by name, with a blind write. A policy deleted by someone else
after the first read, and a global policy created under the freed name, would have been removed in its place.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The deletion carries the version read (chosen) | `ProjectPolicyNames` returns name and version; a `412` from the policy store marks the round as changed and the delete reads again (three rounds at most); a `404` is skipped | Closes the window in every case where the versions differ; no change to the policy store | A global policy at the same version as the project policy it replaced is still removed (H-407) |
| (b) A store method that deletes by id or by project | `Delete(name, project)` | Closes it fully | A change in the policy store, which another work package owns |
| (c) Keep the blind write | As before | Nothing | A global policy can be removed by a project delete |

**Choice:** (a).

**Rationale:** it removes the window for every policy that was edited and for every case where the global policy differs in
version, without touching the store. Since [Q-AUTHZ-199](#q-authz-199--a-delegates-policy-write-is-checked-under-the-policy-lock-and-its-project-lookups-run-in-the-writes-transaction) the deletion also names the project
(`authz.ExpectProject`), which closes the last case, a never-edited policy replaced at version 1 (formerly H-407).

**Applied in the spec:** R-AUTHZ-026 (deleting a project). Code: `internal/project/crud.go` (`Delete`); test
`TestR_AUTHZ_026_DeleteSparesAGlobalPolicyOfTheSameName` (versions 1 and 2).

### Q-AUTHZ-205 · An assignment names the owner it expects, and the write compares it under its locks

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. **Superseded by [Q-REPO-217](repositories-and-proxy.md#q-repo-217--a-repository-moves-to-another-project-through-an-operation-of-its-own-and-only-while-nothing-of-its-project-names-it)**: assignment and unassignment are gone; a repository changes project only through the move, which names its source in the path and its target in the body.

Context: a move (`?move=true`) took the repository from whichever project held it. The project dialog reads the owners right
before the assignments and asks when one changed, which leaves the window between that read and the request: another
administrator's move could be overwritten by a move the person agreed to for a different owner.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Query parameter `from` on the assignment, compared under the locks (chosen) | `?from=<key>` names the expected owner, `?from=` none; absent: no expectation. A different owner answers 409 `project_owner_changed` with `currentProject` | No new operation or body; an old client keeps working; the comparison uses the owner read after the project locks, so it is the state the write acts on | The client has to send it to be protected |
| (b) `If-Match` on the repository's `config_version` | The ETag of the repository document guards the move | One mechanism for all repository writes | The version moves with every configuration change, so an unrelated edit refuses a move that is still right; the assignment is exempt from `If-Match` by design (Q-AUTHZ-191) |
| (c) `move=<key>` instead of `move=true` | The value of `move` is the owner moved from | One parameter | Breaks the boolean the API shipped; no way to say "expected unassigned" for a plain assignment |
| (d) Keep the UI-side re-read only | As before | Nothing | The window stays |

**Choice:** (a). The parameter is validated as a project key or empty (400 otherwise). The comparison runs in `Assign` after
`lockProjects` has taken the row locks of the projects involved and read the repository's project again, before the target is
loaded and before the no-op check: a repository that already belongs to the target still answers 409 for a wrong `from`.
When the owner changes between the first read and the locks, the existing 409 `repository_assigned` ("assigned by another
request meanwhile") is answered; `ProjectAssignRepository` still moves only from the owner it read. Nothing is written when
the answer is 409.

**Rationale:** the only state a move may rely on is the one read under the locks, and the caller's expectation is a fact about
that state, not about the document. An absent parameter keeps every existing client and script working. The dialog sends the
owner it read for every assignment, and on 409 `project_owner_changed` (or `repository_assigned`) reads the owners again and
asks about the owner that holds the repository now. Revisit when other repository writes (the document `PUT`, a delete) need
the same expectation, or when a client cannot be changed and needs the check made mandatory.

**Applied in the spec:** R-AUTHZ-026 (the assignment and the API table). Code: `internal/project/members.go` (`Assign`,
`expectOwner`), `internal/api/v1/projects_api.go`, `api/openapi.yaml` (`from`, `currentProject`), `internal/errs/codes.go`
(`project_owner_changed`), `web/src/app/features/projects/project-dialog.ts`; test
`TestR_AUTHZ_026_AssignNamesTheExpectedOwner`.

### Q-AUTHZ-206 · Every repository belongs to exactly one project

Status: **Decided by the owner on 2026-11-06.** Built.

Context: in the review of 2026-11-06 the owner expected repositories always to belong to a project; R-AUTHZ-026 made the
project optional ("at most one"), a frame for delegated administration beside repositories that belong to none. trove is
pre-alpha: no installation runs and nobody uses it, and until the first stable release no change keeps an older version,
client or stored state working (owner, the same day; [CLAUDE.md](../../CLAUDE.md)).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A built-in project per installation | Held by the system administrators, not deletable; a repository created without a project goes into it | A request that names no project still works | A project that is not a team's; it carries the idea of repositories that belong to everybody |
| (b) No built-in project (chosen) | A repository is created in a project and stays in one; `repositories.project_id` is required | The model is strict: a project holds its repositories, nothing belongs to everybody | A request that names no project is refused, which is intended |
| (c) Required in the UI only | The data model stays optional | Cheap | The API keeps creating repositories without a project: the rule is claimed, not held |

**Choice:** (b), the owner's. There are no shared repositories: a proxy of a public registry and a virtual repository are
repositories of one project like any other, and a virtual repository lists only repositories of its own project. How a
repository is addressed inside its project is
[Q-REPO-212](repositories-and-proxy.md#q-repo-212--a-repository-is-addressed-inside-its-project-and-no-repository-is-shared).

**Consequences:** the schema makes `project_id` required (changed in place, no migration path: no installation exists);
creating a repository names its project in the API and the UI; unassignment goes; a move to another project stays and
changes the repository's address, which contains its project (Q-REPO-212); deleting a project stays possible only when it
is empty; an administrator of trove creates a repository in any project, a project administrator in its own.

**Applied in the spec:** R-AUTHZ-026 (the project, its repositories, deleting a project), R-AUTHZ-004 (selectors of a
project), R-AUTHZ-009; [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-001, R-REPO-002, R-REPO-004,
R-REPO-009, R-REPO-013, R-REPO-017, R-REPO-021, R-REPO-060 (the move, [Q-REPO-217](repositories-and-proxy.md#q-repo-217--a-repository-moves-to-another-project-through-an-operation-of-its-own-and-only-while-nothing-of-its-project-names-it));
[storage.md](storage.md) R-STOR-003 (`project_id` required); [README.md](README.md) INV-5; [ADR-156](../decisions.md#adr-156--projects-own-their-repositories-members-hold-roles-names-are-scoped-to-the-project).

### Q-AUTHZ-207 · A user or group is a member of a project with a role, and the role grants the project's repositories

Status: **Decided by the owner on 2026-11-06.** Built.

Context: the owner expects a user to be in few projects and to choose among them. trove has no notion of a user being in a
project: a project names only its administrators (R-AUTHZ-026), every other caller reaches a project's repositories through
permission policies, and the project filter of the top bar is offered to holders of `projects:read` and project
administrators only, so an ordinary user sees no project at all.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Explicit membership with a role (chosen) | Users and groups are members of a project with a role; the role grants access to the project's repositories; project policies stay for finer exceptions; the project administrator becomes the highest role | "Your projects" is a defined set, cheap to read; the common case needs no policy; the reference's model | Membership and roles join the policies in the authorization model |
| (b) Derived from policies | A user is in every project where any policy grants it something | No new concept | Opaque ("why do I see this project?"), costly to compute, and a global policy with `@any` puts everybody in every project |
| (c) Everybody sees every project | Selection by search only | Simple | No short list of one's own projects; every project name is visible to everybody |

**Choice:** (a), the owner's. The roles and what each grants are [Q-AUTHZ-208](#q-authz-208--four-project-roles-reader-developer-maintainer-owner).

**Applied in the spec:** R-AUTHZ-026 (members and roles), R-AUTHZ-001, R-AUTHZ-002 (the credentials that carry a
role), R-AUTHZ-013, R-AUTHZ-015 (the role as a source of actions), R-AUTHZ-022 (`/authz/me` lists memberships),
R-AUTHZ-024; [api-and-ui.md](api-and-ui.md) R-API-047, R-UI-041; the credentials are
[Q-AUTHZ-215](#q-authz-215--a-members-role-acts-through-its-session-and-personal-tokens-within-their-scope-and-service-accounts-are-not-members);
[ADR-156](../decisions.md#adr-156--projects-own-their-repositories-members-hold-roles-names-are-scoped-to-the-project).

### Q-AUTHZ-208 · Four project roles: Reader, Developer, Maintainer, Owner

Status: **Decided by the owner on 2026-11-06.** Built.

Context: a member of a project has a role ([Q-AUTHZ-207](#q-authz-207--a-user-or-group-is-a-member-of-a-project-with-a-role-and-the-role-grants-the-projects-repositories)).
The actions on a repository are `read`, `annotate`, `deploy`, `overwrite`, `delete` and `manage` (the action table of
R-AUTHZ-001); the administration of a project is its members, repositories, policies, service accounts and settings.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Three roles: Reader, Developer, Maintainer | Maintainer holds every action and the administration | Few roles | Who may delete versions may also change members |
| (b) Four roles: Reader, Developer, Maintainer, Owner (chosen) | Maintainer holds the content actions, Owner the administration | A release owner can delete versions without changing members | One role more |
| (c) The reference's five roles | Guest, Reporter, Developer, Maintainer, Owner | Familiar | A guest role has no meaning for a package store |

**Choice:** (b), the owner's. Each role holds what the role before it holds:

| Role | On every repository of the project | In the project |
|---|---|---|
| Reader | `read` | sees the project, its repositories and packages |
| Developer | `read`, `annotate`, `deploy` | |
| Maintainer | every action: also `overwrite`, `delete`, `manage` | |
| Owner | every action | members and their roles, repositories, permission policies, service accounts, settings; today's project administrator |

The Owner administers the project on its own (owner, the same day): it adds and removes members and changes their roles,
and nothing an administrator of trove sets narrows that, so the two switches of today's project (`managePolicies`,
`manageRepositories`, R-AUTHZ-026) go. Creating and deleting a project stays with an administrator of trove.

Outside the roles (owner, the same day): an administrator of trove (`admin`) and an auditor (`auditor`) see every project, as
both hold `projects:read` today; an administrator of trove creates projects and adds members with their roles, the first
Owner among them. The auditor
gets an instance-wide view of vulnerabilities to search and triage
([Q-UI-130](api-and-ui.md#q-ui-130--the-auditor-has-an-instance-wide-vulnerability-dashboard-in-the-admin-area)).

**Applied in the spec:** R-AUTHZ-026 (the role table, the Owner's table, what others see, the declaration),
R-AUTHZ-031 (member changes in the journal); [api-and-ui.md](api-and-ui.md) R-API-008 (`projectRole`), R-API-047,
R-UI-041; [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-009 to R-REPO-013, R-REPO-056;
[gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-019; the last Owner is
[Q-AUTHZ-217](#q-authz-217--the-last-owner-of-a-project-is-removed-or-demoted-only-by-an-administrator-of-trove);
[ADR-156](../decisions.md#adr-156--projects-own-their-repositories-members-hold-roles-names-are-scoped-to-the-project).

### Q-AUTHZ-209 · A project's policies and service accounts are named within the project; administrators keep instance-wide ones

Status: **Decided by the owner on 2026-11-06.** Built.

Context: a project's permission policies and service accounts carry the project key as a prefix, because their names are unique
in the installation ([Q-AUTHZ-176](#q-authz-176--names-of-a-project-are-prefixed-with-its-key-and-a-global-name-never-takes-a-project-prefix));
the prefix needs overlap rules (`project_key_overlap`, `name_reserved_by_project`, H-399). A repository is now addressed inside
its project and its key is unique within it
([Q-REPO-212](repositories-and-proxy.md#q-repo-212--a-repository-is-addressed-inside-its-project-and-no-repository-is-shared)).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Unique within the project, no prefix; instance-wide ones beside them (chosen) | A project's policy or service account is named within its project and addressed under it (`/api/v1/projects/<key>/service-accounts/<name>`, `…/policies/<name>`); an administrator of trove still creates service accounts and policies of no project, with their tokens, addressed as today | One naming rule for everything a project holds; the prefix and its overlap checks go; instance-wide automation keeps its accounts | Every place that names such an account or policy says which project, or that it has none |
| (b) Keep the prefix | Names stay unique in the installation with `<key>-` | Less change | Two naming rules side by side: repositories per project, policies and accounts global |

**Choice:** (a), with the owner's addition: administrators keep issuing instance-wide service accounts and their tokens. A name
is unique among the objects of its project, and among those of no project; the same name may exist in two projects and once
instance-wide. Instance-wide listings show the project, or none, in a column; an audit event names the project with the name.

**Applied in the spec:** R-AUTHZ-021 (the names and routes of policies), R-AUTHZ-026 (project policies and service
accounts), R-AUTHZ-002 (the references `sa:<projectKey>/<name>`); [identity-and-authentication.md](identity-and-authentication.md)
R-AUTH-020; [api-and-ui.md](api-and-ui.md) R-API-031, R-API-033; the CI mappings of a project follow in
[Q-AUTHZ-214](#q-authz-214--a-projects-ci-mappings-are-named-within-the-project-like-its-policies-and-service-accounts);
Q-AUTHZ-176 is marked superseded; [ADR-156](../decisions.md#adr-156--projects-own-their-repositories-members-hold-roles-names-are-scoped-to-the-project).

### Q-AUTHZ-214 · A project's CI mappings are named within the project like its policies and service accounts

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the owner named a project's policies and service accounts within the project and removed their `<projectKey>-`
prefix ([Q-AUTHZ-209](#q-authz-209--a-projects-policies-and-service-accounts-are-named-within-the-project-administrators-keep-instance-wide-ones)).
The CI mappings of a project's tier `project:<projectKey>` are a third kind of named object of a project
([gitlab-ci-oidc.md](gitlab-ci-oidc.md) Q-CI-26): their names are unique per CI issuer, carry the same prefix, which the
create checks before the name is looked up (Q-CI-51), and are referenced as `ci:<issuer>/<name>`. The owner's decision does
not name them. The prefix and its overlap checks (`project_key_overlap`, `project_prefix_required`) can only go when no
object of a project keeps a prefixed name.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Named within the project (chosen) | A mapping of the tier `project:<projectKey>` is unique per issuer within that tier, has no prefix, is referenced as `ci:<issuer>/<projectKey>/<name>` and stored as `<projectKey>/<name>`; a mapping of the system tier stays `ci:<issuer>/<name>` | One naming rule for everything a project holds; the prefix, its check before the lookup and every overlap rule go; two projects can both have a mapping `release` | A reference has three parts in a project's tier and two in the system tier |
| (b) Keep the prefix for CI mappings only | As today | No change to the CI references | The overlap rule between project keys and names stays alive for one kind of object; an Owner names policies and accounts one way and mappings another |
| (c) Unique per issuer, no prefix, no project in the reference | `ci:<issuer>/<name>` for every tier | Short references | Two projects cannot both name a mapping `release`, and a name taken by one project tells another that it exists, which is what the prefix was introduced to avoid |

**Choice:** (a). [Q-CI-26](gitlab-ci-oidc.md#q-ci-26--project-administrators-manage-the-ci-mappings-of-their-project) is
superseded in part.

**Rationale:** the prefix existed to keep names unique per issuer across projects without a coordination step (Q-CI-26).
The same reason led the owner to name policies and accounts within the project; keeping the prefix for mappings would keep
the overlap rule, its lock and its gaps (H-399) for one kind of object. The project is already part of a mapping's identity
(its tier and `project_id`), so the reference only says what the row says. The development agent would revisit this if
mappings had to move between projects (then a stored name that carries the project is wrong, as for repositories), or if a
consumer of principal references (the explanation trace, the Bastion's feed) could not take a third part.

**Applied in the spec:** R-AUTHZ-002 (the reference table), R-AUTHZ-026 (the project's objects);
[gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-007, R-CI-009, R-CI-019, R-CI-020 (names and references), Q-CI-26 and Q-CI-51
(marked); [identity-and-authentication.md](identity-and-authentication.md) R-AUTH-016, R-AUTH-033 (the rendered name).

### Q-AUTHZ-215 · A member's role acts through its session and personal tokens within their scope, and service accounts are not members

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: a user or group is a member of a project with a role
([Q-AUTHZ-207](#q-authz-207--a-user-or-group-is-a-member-of-a-project-with-a-role-and-the-role-grants-the-projects-repositories),
[Q-AUTHZ-208](#q-authz-208--four-project-roles-reader-developer-maintainer-owner)). Open is which credentials carry the
role. Today a grant reaches every credential of its principal and the token's scope narrows it (R-AUTHZ-013); a project
administrator exercised its administration only through a UI session or a token with `admin: true`, as a system role is
([Q-AUTHZ-183](#q-authz-183--who-administers-a-project-and-through-which-credentials)); a project service account is kept
out of groups and system roles so that no broad grant reaches it
([Q-AUTHZ-175](#q-authz-175--a-project-service-account-is-granted-by-its-projects-policies-only)).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The role's actions reach the session and the personal access tokens within their scope; the Owner's administration needs a session or an `admin: true` token; service accounts are no members (chosen) | A role is decided like a grant for content; administering keeps the bar of the system roles; machines are granted through project policies | `npm install` and `npm publish` with a personal access token work from the role without a policy, and the token's scope narrows them; a leaked read-scoped token of an Owner cannot change members; a machine's rights are named per account and audited | An Owner needs a second kind of token for scripted administration |
| (b) Every credential of the member carries the whole role, administration included | One rule | Simple | A personal access token scoped to `read` could still change members and policies: a project would be administered with a weaker credential than the installation |
| (c) Roles only in UI sessions; tokens need a policy | No token reaches a role | Tokens stay as today | The common case, a developer who installs and publishes with a token, needs a policy per project, which is what roles were meant to spare (Q-AUTHZ-207) |
| (d) Service accounts as members too | People and machines in one list | One mechanism | A role hands a machine every repository of the project, and a project account would be reached by what Q-AUTHZ-175 keeps away from it |

**Choice:** (a). The source of a role's actions in the explanation is `role:<projectKey>:<role>`; the token's scope
intersects it as it intersects a grant. Outside the content, where no repository coordinate is decided (the project
list, a project and its members, `GET /authz/me`, the `404` of a hidden project), the scope reaches a project when it is
`inherit`, `admin: true` or lists the system item `projects:read`, or when one of its `repositories` elements names the
project, whatever the element's actions: a token scoped to `web/npm-internal` with `deploy` reads project `web` and its
members, and no other project of its owner.

**Rationale:** a role is a set of grants the project decides, so it should reach what a grant reaches, under the same
ceiling. Administration changes who may act, which the installation already guards with `admin: true` for the system
roles; the project gets the same bar, so an Owner's leaked everyday token cannot add members. Keeping service accounts and
CI tokens out of roles keeps every machine's rights in a policy that names it. The development agent would revisit this if
Owners need automation that administers a project (then an administering project service account, a decision of its own),
or if a token should carry a role but not the grants of global policies. Reading a project is no repository action, so it
cannot be intersected element by element; naming the project in an element is the narrowest form a scope already has, and
the system item `projects:read` is the one a script that only lists its projects uses. The development agent would revisit this part when token
scopes gain a project resource type (TOK-011, P2).

**Applied in the spec:** R-AUTHZ-026 (members and roles, what a token sees of its projects), R-AUTHZ-013 (the scope over a role);
[api-and-ui.md](api-and-ui.md) R-API-008 (the declaration); [identity-and-authentication.md](identity-and-authentication.md)
R-AUTH-024 (what a scope narrows).

### Q-AUTHZ-216 · Repository selectors name the project in instance contexts and a bare key inside a project

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: a repository key is unique within its project
([Q-REPO-212](repositories-and-proxy.md#q-repo-212--a-repository-is-addressed-inside-its-project-and-no-repository-is-shared)).
Selectors (R-AUTHZ-004) name repositories by key, key glob or class in global and project policies, token scopes, CI inline
grants, exposure rules, host-root bindings, virtual members and deployment targets. In a context that spans projects a bare
key no longer names one repository.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Two contexts (chosen) | Instance contexts take `projectGlob/keyGlob[:format]` or a class for every project; project contexts take bare keys, key globs and classes; `*` and `?` never cross `/`; a bare key in an instance context and a `/` in a project context are refused | What a selector covers is visible in it; a selector never silently matches nothing; one grammar for policies, scopes, CI grants, exposure and configuration | Every global selector names the project; a class within one project (`web/@any-local`) cannot be written in an instance context |
| (b) A bare key in an instance context matches that key in every project | `npm-internal` covers `web/npm-internal` and `mobile/npm-internal` | Short selectors | A policy written for one team's repository covers another team's repository of the same key, created later by someone else: a grant widened by a name |
| (c) Selector objects (`{"project": "web", "key": "npm-*"}`) | Structured instead of a string | No string grammar | Token scopes, CI templates, the configuration file and the UI use strings; two representations of one thing |
| (d) A glob that crosses `/` | `web*` or `**` reach into the key | One glob | `web*` covers `web/x` and `webshop/x`; the project boundary becomes a matter of quoting |

**Choice:** (a). A key selector, both parts literal, must name an existing repository and is stored by its ID; it keeps
naming the repository when it moves. The refusal is 422 `invalid_pattern` where selectors are validated as patterns
(policies, CI mappings, exposure rules), 400 `invalid_scope` in a token scope and `scope_malformed` in the CI exchange,
which keep the error each surface already answers for a malformed selector.

**Rationale:** a selector that silently matches nothing is the failure a grammar change must not produce: an
administrator who writes `npm-internal` in a global policy has to learn at once that it names no repository. Naming the
project in every instance-wide selector makes a grant's reach readable without knowing which projects hold which keys,
and keeping bare keys inside a project leaves the common project policy as short as before. The development agent would
revisit this when a global policy needs every repository of one class in one project (then `web/@any-local`), or when
selectors must cover projects by a property other than their key.

**Applied in the spec:** R-AUTHZ-004, R-AUTHZ-013 (examples), R-AUTHZ-026 (project policies);
[repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-017, R-REPO-021; [gitlab-ci-oidc.md](gitlab-ci-oidc.md)
R-CI-007, R-CI-013, R-CI-014; [identity-and-authentication.md](identity-and-authentication.md) R-AUTH-024;
[endpoints.md](endpoints.md) R-EP-006, R-EP-012; [operations-and-observability.md](operations-and-observability.md)
R-OPS-007; [storage.md](storage.md) R-STOR-037, R-STOR-038.

### Q-AUTHZ-217 · The last Owner of a project is removed or demoted only by an administrator of trove

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the Owner administers its project on its own, members included, and an administrator of trove creates projects
and names the first Owner ([Q-AUTHZ-208](#q-authz-208--four-project-roles-reader-developer-maintainer-owner)). Open is
what happens when a write would leave a project without an Owner: an Owner who removes or demotes itself or the other
Owners. The installation guards its own administration the same way (`last_admin`, R-AUTHZ-022, Q-AUTHZ-70).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) 409 `last_owner`, unless the caller holds `projects:write` (chosen) | The removal or demotion of the last membership with the role `owner` is refused for an Owner; an administrator of trove may do it and administers the project alone | A project does not lose its administration by a slip; the administrator, who named the first Owner, can still take a departed person's role away | An Owner who wants to leave names another Owner first |
| (b) Allowed; the administrators of trove remain | No special case | Simple | The team loses its administration until a system administrator acts, the opposite of the delegation the project exists for |
| (c) Refused for everyone | An invariant of the data | No project without an Owner | An administrator cannot remove a departed person's last Ownership without inventing another Owner first |

**Choice:** (a). The count is of membership rows with the role `owner`, a group among them, whatever the group's members:
the write reads it under the project's row lock, so two Owners who remove each other at the same instant run one after
the other, exactly one removal commits and one Owner remains. The second request is refused as what its caller is when it is
decided. Admission is decided on the request's index like every write (R-AUTHZ-028): an index that still holds the caller's
role admits it, and the count under the lock answers 409 `last_owner`; an index that already holds the first removal knows
the caller as no member, and the request answers the `404` of a non-member (R-AUTHZ-016). The member write does not read the
caller's role again under the lock.

**Rationale:** the rule protects the delegation, not the data: the project is administered by its team, and only the
administrator who can name a new Owner may leave it without one. Counting rows keeps the check a property of what the
project itself states. An Owner group that its identity provider empties leaves the project without an acting Owner, as
an identity-provider group bound to a system role can (H-394); the administrators of trove remain. The development agent
would revisit this if projects end up unadministered that way in practice; then the count would follow `last_admin`
(active members only, Q-AUTHZ-70). The loser of the race is refused either way and the invariant holds under the lock;
reading the caller's role again there would make the answer the `404` in every case, but only for member writes, while
every other write of an Owner or an administrator of trove is decided on the index of its request. The development agent would revisit that if
writes decided on a stale role turn out to matter beyond this race; then every administering write reads the caller's role
under the lock of what it changes.

**Applied in the spec:** R-AUTHZ-026 (members and roles, the API table); [api-and-ui.md](api-and-ui.md) R-API-047,
R-UI-041.

### Q-AUTHZ-229 · A project create names its first Owners

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: an administrator of trove creates a project and names the first Owner
([Q-AUTHZ-208](#q-authz-208--four-project-roles-reader-developer-maintainer-owner)); the member operation adds members to a project
that exists. Between a create and the first member write a project exists that no team administers, and a client that fails between
the two leaves it so; the administrator of trove is no member of the teams it creates projects for.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `owners` on the create (chosen) | The create body names users and groups; they become Owners in the transaction of the create, each recorded as `authz.project.member_added` | One request, one transaction: the project is administered from its first epoch; the same checks as the member write | The create has one more member |
| (b) Only the member operation | Create, then `PUT …/members/{ref}` | No new member | A project without an Owner between two requests; two epochs, two audit transactions for one intent |
| (c) The creator becomes Owner | The administrator of trove is the first Owner | No input | The administrator is no member of the team; removing itself later meets the last-Owner rule it does not need |

**Choice:** (a). A reference that is no user or group, an auto-join group or a principal that does not exist refuses the whole create,
with the answers of the member write (422 `principal_not_allowed`, 404). An empty or absent `owners` creates a project without
members, as before.

**Rationale:** the delegation exists so that a team administers its project; a create that hands it over atomically leaves no window in
which only the administrators of trove can act. The development agent would revisit this if creates came from automation that names
owners by an identity-provider attribute, then the owners might be resolved at the first sign-in instead.

**Applied in the spec:** R-AUTHZ-026 (the API table). Code: `project.Input.Owners`, `firstOwners` in
[crud.go](../../internal/project/crud.go). The management API reads the member from the create body once the document declares it
writable on create.

### Q-AUTHZ-230 · The comparators of R-AUTHZ-031 are `journal.Narrower[T]` implementations in the owning packages

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One comparator package for every object | `internal/journal` knows policies, memberships and settings | One place | The comparators are the owning page's; the journal would import the authorization model, and the authorization engine would import the journal's rule |
| (b) The generic `journal.Narrower[T]` and `journal.ApplyUpdate[T]`; each owner implements `Narrows(current, after T) bool` for its type in a file of its own (chosen) | Policy, role and settings comparators live in `internal/authz` and `internal/project`, the exposure comparator beside the exposure service in `internal/authz` (R-EP-020), and the issuer and mapping comparators in `internal/ci` | The rule that picks apply, skip or suspend is tested once; each comparator is tested with the cases of its page | Four comparator types to keep conservative |

**Choice:** (b). A comparator answers true only when it can prove that `after` grants nothing `current` does not; every doubt is false (R-HA-054).

**Rationale:** This is the division R-AUTHZ-031 and R-HA-054 already state in prose. The development agent would revisit this if two pages need the same comparator over one type.

**Applied:** R-AUTHZ-031, R-HA-054 update rule; `internal/journal/replay.go`.

### Q-AUTHZ-234 · A format's replay names the entry it applied on what it writes, and takes no actor it cannot verify

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

A replay handler of a format (npm withholds a version, NuGet reserves a number) changes durable state on the strength of a journal object that anyone with the runtime S3 credential can write (R-HA-054). `trove admin npm reinstate` reverses a withdrawal and writes `npm.reinstate`, which R-NPM-044 says names the journal entry that withheld the version. The question is what the replay leaves behind that ties the two together, and whose name the written rows carry.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Nothing beyond the tombstone | The reinstate event names the version and its integrity only; the link to the entry is the replay output | No schema change | The audit stream cannot tell which entry a reversal answers, and the replay output is not kept; R-NPM-044's text would have to drop the member |
| (b) The entry's ID on the tombstone, the actor only as a verified identifier (chosen) | `npm_tombstones.journal_entry` (uuid, null unless `source = 'journal'`, an expand step); `unpublished_at` and `unpublished_by` hold the unpublish's time and, when it is a UUID, its actor, else the nil UUID. NuGet's tombstones carry the system principal of the restore (the nil UUID) | The reinstate event repeats the ID, so the audit stream links withdrawal and reversal; a forged actor name cannot enter a column; one nullable column | One migration |
| (c) An audit event per replayed entry | The handler writes `npm.unpublish` / `package.deleted` events itself | The audit stream shows each application | The event is journal-flagged: it would enter the outbox again and ship a second object for one change; the replay summary and `system.restore.journal_replayed` already count every entry |

**Choice:** (b).

**Rationale:** The entry is untrusted input, so a handler stores from it only what it can verify: an identifier of the shape the column takes, a digest of the expected length, and a subject the restored database holds. The entry's own ID is not a claim about the world, only a pointer to the object, so it can be stored. (c) would feed the journal from the journal. The development agent would revisit this if the replay output is kept as an audit record of its own (then the ID is redundant with it).

**Applied:** R-NPM-006 (`npm_tombstones.journal_entry`), R-NPM-047, R-NPM-044 (`npm.reinstate` carries `journal_entry`), R-NUGET-033; `internal/meta/postgres/migrations/00048_npm_tombstone_journal_entry.sql`, `internal/formatdata/npm/journal_replay.go`, `reinstate.go`, `internal/formatdata/nuget/journal_replay.go`.

### Q-AUTHZ-232 · What a replay writes besides the object it changes, and how the comparators compare

Status: Decided by the development agent — selected from the options below; the owner reviews it at the walkthrough.

| Option | Behaviour | Cost |
|---|---|---|
| (a) The replay changes the object and notifies the change areas; it writes one `system.restore.journal_replayed` per run and no per-object audit event; comparators compare IDs and patterns exactly as stored (no case folding, no glob containment, no defaults guessed) | A replayed change cannot re-enter the journal; a comparator can only err towards suspending | A suspended policy has no audit event of its own, only the replay line |
| (b) One audit event per changed object | Complete trail | Every event type is flagged with a required journal entry, so each replayed change would ship a new journal object |

**Choice:** (a). The settings comparator treats a `hideExistence` of `inherit` as the most restrictive value when it is the current value and the least restrictive when it is the after-value, so it never proves a narrowing. A repository setting that the document does not set is "cannot tell". A deleted user, service account, CI token owner is disabled, never deleted; a group is deleted.
**Rationale:** R-AUTHZ-031 says suspension "writes its audit event as any policy change does"; that event carries a required journal entry, which would ship an object for every replayed change. The replay lines and the single summary event are the record. Revisit when audit events may be written without an entry.
**Applied:** R-AUTHZ-031 comparators in `internal/authz/journal_replay.go`, `internal/project/journal_replay.go`, `internal/auth/token/journal_replay.go`; the identity entries of `group.members.changed` and `user.groups.synced` carry IDs now (`internal/auth/principal`).

### Q-AUTHZ-235 · A binding that a replay removed is not re-created by trove migrate

Status: Decided by the development agent — selected from the options below; the owner reviews it at the walkthrough.

| Option | Behaviour | Cost |
|---|---|---|
| (a) The replay marks the removed binding in `system_state`; `ApplyDeclared` skips a marked binding that does not exist, logs it and reports it, and an API bind clears the mark | Fail-closed, no new table, no new command; a restore with an older configuration cannot bring the access back | A binding the configuration declares again on purpose stays unapplied until it is bound through the API |
| (b) `trove migrate` fails when the configuration declares what the journal removed | Loud | Blocks the final `helm upgrade` of a restore |
| (c) Record it as a gap only | No code | The owner's rule: a restore never brings back removed access |

**Choice:** (a). Only the declared role bindings and the CI issuers are written by `trove migrate`; the issuers stay with H-565.
**Rationale:** the mark is written in the transaction of the replayed removal and read in the transaction of `ApplyDeclared`, both as the owner. Revisit when the CI issuers get the same mark.
**Applied:** `internal/authz/declared.go`, `bindings.go`, `journal_replay.go`, `internal/app/migrate_declared.go`, `queries/journal.sql`.

### Q-AUTHZ-236 · The suppression mark of a declared binding is keyed by role and name and written for every replayed unbinding

Status: Decided by the development agent — selected from the options below; the owner reviews it at the walkthrough.

| Option | Behaviour | Cost |
|---|---|---|
| (a) Mark `<role>|<kind>:<name>` for every replayed `authz.role.unbound`, whatever the row count, and for every role of a group that `group.deleted` removes (all roles when the group is not in the database) | Covers a binding added and removed after the restore point and a declared group that `ApplyDeclared` creates again under a new ID | A forged unbind entry suppresses a declared binding until an administrator binds it (fail-closed) |
| (b) Keep the mark by principal ID and only on a deleted row (Q-AUTHZ-235 as first built) | Smaller | A binding absent from the snapshot, and a recreated group, escape (audit finding M4) |

**Choice:** (a). `ApplyDeclared` and the API bind use the same key (`suppressionKey` in `internal/authz/declared.go`).
**Rationale:** The restore never brings back removed access; the cost lands on the rare case that the configuration declares a binding the journal removed, and the way out is one API bind.
**Applied:** `internal/authz/journal_replay.go`, `internal/auth/principal/journal_replay.go`, `internal/authz/declared.go`, `bindings.go`.

### Q-AUTHZ-237 · A repository setting without an order is suspended around, not skipped by a panic

Status: Decided by the development agent — selected from the options below; the owner reviews it at the walkthrough.

| Option | Behaviour | Cost |
|---|---|---|
| (a) Settings without an access order are left as they are and named in the output; the ordered ones are suspended; the outcome is `suspended` | A needed suspension is never turned into a skip | The unordered setting needs an administrator |
| (b) Leave the recovered panic to report `skipped` | No code | An entry that should fail closed is skipped (audit finding L1) |

**Choice:** (a).
**Rationale:** The comparator already answers false for a setting without an order; the suspension now has no hole.
**Applied:** `internal/authz/journal_replay.go` (`replaySettings`).

### Q-AUTHZ-238 · Disabling a repository is journaled as `repository.disabled` with a handler of its own

Status: Decided by the development agent — selected from the options below; the owner reviews it at the walkthrough.

A repository with `disabled: true` takes no part in resolution, download or deploy (R-REPO-004), yet the authorization settings
hook journaled only `requireAuthentication`, `hideExistence`, `cacheFillAction` and `packageOwnership`: a restore to a point before
a disable enabled the repository again, and nothing replayed it.

| Option | Behaviour | Cost |
|---|---|---|
| (a) `disabled` as a fifth setting of `authz.settings.changed`, order `true`, `false`, under the settings comparator | No new event; the Bastion feed already maps the event | The update rule's comparator, its suspension and its writer learn a setting that is a plain removal; a disable rides on the narrowing proof of the other settings of the same entry |
| (b) A transactional event `repository.disabled` that an update records next to `repository.updated` when it sets `disabled`, with the entry `{repositoryId, key}`; the replay disables the repository by ID (chosen) | A removal applied as recorded, like `user.disabled`: `applied`, `already in effect` (disabled, or being deleted), `subject unknown`; enabling records only `repository.updated` and is never replayed | One more event name; the Bastion feed reads it (`grants`, Q-BAS-120) |
| (c) Flag `repository.updated` with an entry on every update | One event | A journal object for every configuration change of every repository, almost all of which narrow nothing |

**Choice:** (b). The handler is the project package's, beside the replay of `repository.moved`, and writes the document through the
same `disableRepository` step (`internal/project/journal_replay.go`).
**Rationale:** a disable is a removal, and R-HA-054 applies removals as recorded; a comparator is for updates whose direction has
to be proved. A forged entry can only disable a repository, which withholds content and grants nothing. Revisit when the settings
table of this requirement gains a member whose suspension has to name `disabled` with it.
**Applied:** R-AUTHZ-031 table, R-HA-054 table, R-BAS-015 table; `internal/audit/events_repository.go`, `internal/repository/service.go`
(`recordDisabled`), `internal/project/journal_replay.go`, `internal/bastionmain/feed.go`.

### Q-AUTHZ-239 · A deleted repository is journaled and replayed as disabled

Status: Decided by the development agent — selected from the options below; the owner reviews it at the walkthrough.

A deletion ends every access to a repository (R-REPO-013), and a restore to a point before it brings the repository back with the
grants of its policies and its project.

| Option | Behaviour | Cost |
|---|---|---|
| (a) Not journaled: a restore restores the repository | Nothing to build | An administrator who deleted a repository to withdraw what it served finds it served again after a restore |
| (b) `repository.deleted` carries the entry `{repositoryId, key}` in both phases; the replay disables the repository (chosen) | The rule of R-HA-054 for deleted principals: disabled, never deleted, so a mistaken or forged entry stays reversible and replay deletes no content; a repository the restored database holds in deletion is `already in effect` | Every deletion ships one or two journal objects |
| (c) The replay deletes the repository again | The restored state equals the state before the restore | Replay would delete content, which R-HA-054 forbids, and a forged entry could destroy a repository |

**Choice:** (b).
**Rationale:** the disabled repository grants nothing, and an administrator deletes it again or enables it. Revisit when the backup
keeps a repository's deletion in the manifest itself.
**Applied:** R-AUTHZ-031 table, R-HA-054 table and replay rules, R-BAS-015 table; `internal/audit/events_repository.go`,
`internal/repository/service.go` (`recordAndNotify`), `internal/project/journal_replay.go`.

### Q-AUTHZ-240 · An owner changes the owners of a name through a narrowed token only where its scope lists manage on the name

Status: Decided by the development agent — selected from the options below; the owner reviews it at the walkthrough.

Context: an owner change (R-AUTHZ-019) is admitted to `manage` on the coordinate, to `content:bypass`, and to an owner of
the name. The first two are decided by the decision point and so under the token's scope; the owner test compares the
principal IDs of the subject with the owners and never looked at the scope. A token made for another repository, or for one
system capability, could therefore add and remove the owners of every name its principal owns, and an added owner may
publish the name where it holds `deploy` (R-AUTHZ-018). This is the class of Q-API-214 (a narrowed token acting on its
owner's standing by identity), on a rule of this page.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The owner path of a narrowed token needs an element of its scope that lists `manage` on the repository and the name (chosen) | `scopeReachesName` in `changeAllowed` asks `scopeAdmits` for `manage` on the name, the scope part of the decision point without the grants | `manage` is the action R-AUTHZ-006 names for package ownership, so the scope of a token says in its own terms that it may administer the owners of these names; a publish token (`deploy`) cannot hand its owner's names over | An owner who scripts owner changes needs a token that lists `manage` on the names, or an inheriting one |
| (b) The scope must list `deploy` on the name | A token that may publish the name may change its owners | Release pipelines that publish could also transfer | A leaked publish token, the most common token, could hand the name to another principal; ownership exists to stop exactly that |
| (c) The scope must reach the repository with any action | The weakest reading of "reaches" | Fewest refusals | A read-only token of the repository could transfer every name its owner owns there |
| (d) Owner changes need a session or an inheriting token | The rule of Q-API-214 | One rule | A narrowed token with `manage` on the name could not use it, although the scope names exactly that right |

**Choice:** (a). The owner path is unchanged for a session and an inheriting token; `manage` held through the grants and
`content:bypass` are decided as before; a name with no owner to ask still needs `manage` or `content:bypass`. A refusal is
the 403 of any refused owner change, before the owner reference is looked up, and counted and audited as every refused
owner change is (`refuseChange`). The rule applies to the owner path of the change operations only: a publish already needs
`deploy` through the decision point with the scope, and reading the owners stays a read.

**Rationale:** the scope is a promise that a token does only what it was made for; handing a name to another principal is
the ownership administration that `manage` names, and an owner's standing does not widen what a token may do. The
development agent would revisit (b) if publishing pipelines turn out to need to transfer names as part of a release.

**Applied:** R-AUTHZ-019 (the narrowed token, acceptance); `internal/authz/ownership.go` (`changeAllowed`, `scopeReachesName`),
`api/openapi.yaml` (`addPackageOwner`), [permissions.md](../security/permissions.md). Tests:
`TestR_AUTHZ_019_ANarrowedTokenChangesOwnersOnlyWhereItsScopeListsManage` (unit and integration).

### Q-AUTHZ-250 · A protocol request is decided on the canonical path of what it serves or writes, and a package without a path fails a path set

Status: Decided by the development agent — selected from the options below; the owner reviews it at the walkthrough.

Context: finding F-82 (a penetration tester's reading of 2026-11-08, reproduced the same day by
`TestR_AUTHZ_005_PathPatternsDecideTheNPMProtocolOnCanonicalPaths` and `…NuGetProtocolOnCanonicalPaths` before the fix).
Every npm and NuGet handler handed `Host.Authorize` a `Target` with the package and no path, the dispatcher's coarse check
is repository-level, and the path dimension let a coordinate without a path pass a set without an include list. A grant or a
token scope with `read` and `paths.exclude: ["acme.secret/**"]` therefore read the version list, the registrations, the
`.nupkg` and the `.nuspec` of `acme.secret` through the protocol, found it in the search and pushed new versions of it,
while `/files/` refused the same principal the same file; a set with `paths.include: ["acme.open/**"]` refused even
`acme.open` through the protocol. The listing scope did the same: `unrestrictedPackages` read an exclude-only path set as no
restriction, so the search counted the excluded ID. R-AUTHZ-005 says path patterns are matched against the paths the format
reports, and R-AUTHZ-007 already named a path for the npm tarball download, which the handler did not pass.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The format states the canonical path, the decision point fails closed without one, and the core decides its own package questions on the package document's path (chosen) | Every `Target` a handler builds carries the canonical path of the document or file (the npm document `<name>` for the document and its parts, the tarball path; NuGet's `{id}/index.json`, `{id}/.registration/index.json`, `{id}/{version}/{file}`); `resourceSet.matches` refuses a non-repository coordinate without a path once the path set holds any pattern; a listing row, the ownership of a name and a check of R-AUTHZ-023 that names a package and no path are decided on the format's package-document path, which the catalog now carries; kit check 13 fails a handler that authorizes a package without a path, a path Classify does not place in the package, a served file it did not authorize at its path, or a package's request it authorized repository-level | One coordinate per file whichever road reaches it; a format that forgets the path refuses every caller with path patterns instead of admitting them, and the kit says so before a release; listings and the protocol agree | Every format names a path per target; a path pattern on a package format has to cover the document and the files |
| (b) The core derives the path from the request | The host fills `Target.Path` from `rq.Path` or a new contract method that maps a route to its canonical path | Formats change nothing | A write's path is known only after the body (the declared ID and version); a protocol path is not a canonical path (NuGet's `v3/flatcontainer/…`); a required `Format` method raises `ContractVersion`, and an optional one leaves a format that lacks it deciding without a path |
| (c) Refuse path patterns on package formats | A policy, a scope or a CI grant with `paths` that covers an npm or NuGet repository is 422 | No path to state | A selector covers repositories of several formats and of formats created later, so the refusal cannot be decided when the grant is written; breaks the model R-AUTHZ-021 warns about instead |
| (d) Fail closed only | Keep the formats as they are; a package coordinate without a path fails every path set | Smallest change | Path patterns become useless on package formats: an exclude refuses every package, as an include already did |

**Choice:** (a). The npm handlers decide the package document, the version document, the dist-tags, a dist-tag write, a
whole unpublish and a publish before its body on `<name>`, and a tarball, the tarball `DELETE` of an unpublish and a
publish once the body named the version on the tarball path (Q-NPM-80). The NuGet handlers decide the version list on
`{id}/index.json`, every registration document on `{id}/.registration/index.json`, a file on its path, and a push, a
delete, a relist and a symbol push on the version's `.nupkg` or `.snupkg` (Q-NUGET-113). `catalog.Entry.DocumentPath`
(ADR-169) gives the decision point the package-document path of a format; `Engine.atPackage` completes the coordinates of
`ReadScope.MayRead`, of the ownership checks and operations (`CheckTx`, `PrecheckWrite`, `manages`, `Owners`,
`scopeReachesName`) and of `CheckOn` when they name a package and no path, and nowhere else: a format's target is decided
as the format states it. `unrestrictedPackages` counts path excludes too, so a grant with any path pattern makes a listing
decide row by row. Repository-level coordinates keep matching every grant on the repository (R-AUTHZ-005).

**Rationale:** the patterns of a grant are only as good as the coordinate they are matched against, and the protocol is
the road a client takes most; a path the format leaves out has to cost the caller, not the policy. Deriving the path in the
core would hide the format that forgot it and cannot know a write's path; refusing path patterns would decide on
repositories that do not exist yet. Deciding the core's package questions on the package document keeps a listing equal to
the document a client would fetch. The development agent would revisit this if a format serves a document that spans
packages under one path (then its rows need paths of their own), or if an operation that removes several files has to be
decided per file (H-586, which Q-AUTHZ-256 decided that way).

**Applied:** R-AUTHZ-005 (the rule for a coordinate without a path, the paths a format reports, examples, acceptance),
R-AUTHZ-007 (the path of every row), R-AUTHZ-023; [architecture.md](architecture.md) R-ARCH-010 check 13;
[formats/npm.md](formats/npm.md) Q-NPM-80, [formats/nuget.md](formats/nuget.md) Q-NUGET-113. Code:
`internal/authz/{pattern.go,package_path.go,visible.go,ci.go,ownership.go,decide.go}`, `internal/formats/catalog/catalog.go`,
`internal/formats/formats.go` (`CatalogEntry`), the handlers of `internal/formats/npm` and `internal/formats/nuget`,
`internal/formats/formattest/checks_authz.go`. Tests: `TestR_AUTHZ_005_APathPatternDecidesAPackageOnItsPath`,
`TestR_AUTHZ_005_Patterns`, the two integration tests above, `TestR_ARCH_010_Defects` (the defects
`authorizes-without-path`, `authorizes-repository-level`, `authorizes-document-for-file`,
`authorizes-without-path-off-local`).

### Q-AUTHZ-251 · An exposure rule's view and check-exposure decide a package on its document's path

Status: Decided by the development agent — selected from the options below; the owner reviews it at the walkthrough.

Context: the second penetration test of F-82 (2026-11-08). After Q-AUTHZ-250 the endpoint ceiling met the path of every
protocol request, but the view a format filters its documents with (`ExposureView.Package`, `Version`, `Tag`) and
`POST /api/v1/endpoints/{endpoint}/check-exposure` evaluated a package without a path. On an npm repository an exposure
rule with `paths.include: ["open-pkg/**"]` therefore hid `open-pkg` entirely on the endpoint (the view refused the
document and the tarball's version although the ceiling admitted them), and a rule with `paths.exclude:
["secret-pkg/**"]` was enforced on the protocol while check-exposure reported `packageExposed: true` for `secret-pkg`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The view and check-exposure decide a package, its versions and its tags on the package document's path, and a rule's path set fails a package item without one (chosen) | `ExposureView` carries the format's package-document path (`catalog.Entry.DocumentPath`, through the ceiling's catalog); check-exposure judges the package, the versions and the tags on it; `compiledRule.evaluate` uses `matchPath` | The view, the ceiling, a listing row and the preview agree on one path; a rule that names a package's folder exposes the package | A path rule finer than a package's folder hides files and leaves the document listing them (H-646, closed by Q-EP-85) |
| (b) The view ignores path patterns | Only the ceiling applies them | No path needed | The preview and the documents disagree with what the ceiling serves |
| (c) Refuse path patterns on package formats in exposure rules | 422 for a rule with paths on npm or NuGet | Nothing to decide | Breaks what R-EP-012 allows and the warning `path_patterns_on_package_format` already covers |

**Choice:** (a). The view's `Package`, `Version` and `Tag` and check-exposure's package, version and tag verdicts are
evaluated on `DocumentPath(name)`; a path the check names is judged on that path, as before.

**Rationale:** one rule is one promise; its preview and its documents must say what its ceiling does. The development agent
would revisit this if a format's documents span packages under one path.

**Applied:** R-AUTHZ-005, [endpoints.md](endpoints.md) R-EP-012 and R-EP-018 (the view and the preview); code
`internal/authz/{exposure.go,exposure_service.go}`. Tests: `TestR_EP_018_TheViewDecidesAPackageOnItsDocumentPath`,
`TestR_EP_018_PathRulesDecideAnNPMPackageOnItsDocumentPath` (integration).

### Q-AUTHZ-252 · A permission check that names a path is decided on its path's package, and a path of another package answers false

Status: Decided by the development agent — selected from the options below; the owner reviews it at the walkthrough.

Context: the second penetration test of F-82. `POST /api/v1/authz/check` handed the decision point the package and the
path a caller sent as they were. A check of `{package: "@sec/x", path: "open/-/open-1.0.0.tgz"}` was decided on the path
of another package and answered `allowed: true` for a package the caller's path exclude hides; a check that named a path
alone was decided without the package its format gives it, which a name pattern refuses (Q-AUTHZ-160). The answer grants
nothing, but the UI and scripts act on it.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Classify the path as the items operations do; a named package that is not the path's, or a path that is not canonical, answers false (chosen) | `Server.checkedPath` takes the package and version of `files.Service.ClassOf`; the named package is normalized by the format's name rule and compared | The check answers what `/files/` and the protocol decide | A check with a path of no package and a named package is false, even on a format without package names |
| (b) Ignore the named package when a path is given | The path's package decides | Never false by mismatch | A caller who asks about two things gets an answer about one |
| (c) Refuse the request with 422 | The body is invalid | Explicit | One bad check out of 100 fails the whole call |

**Choice:** (a). A check with a path and no package takes the path's package; one with both is false unless the normalized
name equals the path's package; a path `CheckItemPath` refuses is false.

**Rationale:** a check is a prediction of a decision; it must be the decision the request would get. The development agent
would revisit (c) if callers need to tell a malformed check from a refused one.

**Applied:** R-AUTHZ-023; code `internal/api/v1/authz_api.go` (`checkedPath`, `samePackage`), `internal/files/formats.go`
(`ClassOf`). Test: the R-AUTHZ-023 case of `TestR_AUTHZ_005_PathPatternsDecideTheNPMProtocolOnCanonicalPaths`.

### Q-AUTHZ-253 · Path patterns over a format with lower-case paths are lower case, and every path pattern on a package format is warned about

Status: Decided by the development agent — selected from the options below; the owner reviews it at the walkthrough.

Context: the second penetration test of F-82. NuGet stores `{lower_id}/{lower_version}/…` (R-NUGET-005) and the path
dimension is compared as written, so a grant with `paths.exclude: ["Acme.Secret/**"]` excluded nothing and the caller
read `acme.secret`; the policy API took the pattern without a word. The warning `path_patterns_on_package_format` was given
for an include only, although an exclude finer than a package's folder is the shape that hides files and leaves their
contents in the documents (H-647, closed by Q-AUTHZ-255).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Refuse an upper-case path pattern where the resource set covers a repository of a format with lower-case paths; warn on every path pattern on a package format (chosen) | `catalog.Facts.LowerCasePaths` (NuGet); the policy validation refuses from the repositories it covers, an exposure rule from its repository, a token scope and a CI inline grant through `Engine.UpperCasePath` on the installed index; `path_patterns_on_package_format` for include and exclude | The writer learns at once | A selector that comes to cover a NuGet repository later, and a CI template whose rendered value has upper-case letters, are not refused (closed by folding, Q-AUTHZ-254) |
| (b) Fold path patterns on such formats | Compare lower-cased | Also covers later repositories and templates | A different matching rule per format; the future `.symbols/<entry path>` keeps its file names' case (R-NUGET-021, P1): chosen in addition by Q-AUTHZ-254, which folds both sides |
| (c) Warn only | A warning on upper case | No refusal | An exclude that does nothing is saved |

**Choice:** (a). The refusal is 422 `invalid_pattern` at the pattern for a policy, an exposure rule and a CI inline grant,
400 `invalid_scope` for a token scope; letters inside a template (`{{…}}`) are judged when it is rendered. The warning is
given whatever the pattern's shape: telling a folder pattern from a finer one needs the format's layout of files, which
the catalog does not carry. The third penetration test of F-82 reproduced what the refusal cannot see (a policy with the
selector `it/later-*` saved before the NuGet repository existed, a token with that scope, then the repository: the
excluding token read `acme.secret`); Q-AUTHZ-254 closes it by folding the dimension where it is compiled, and the refusal
stays as feedback to whoever writes the pattern.

**Rationale:** a pattern written in a case the format does not store is a mistake its writer should see when writing it;
how a stored pattern matches is Q-AUTHZ-254's. The development agent would revisit the refusal if `.symbols/<entry path>`
(R-NUGET-021, P1) makes mixed-case path patterns useful on NuGet.

**Applied:** R-AUTHZ-005, R-AUTHZ-021, [endpoints.md](endpoints.md) R-EP-012; ADR-169 (the catalog fact); code
`internal/authz/{validate.go,path_case.go,exposure_service.go}`, `internal/api/v1/{tokens_api.go,project_accounts_api.go,ci_mappings_api.go,ci_project_api.go}`,
`internal/formats/nuget/format.go`. Tests: `TestR_AUTHZ_005_AnUpperCasePathPatternOnALowerCaseFormatIsRefused`,
`TestR_AUTHZ_005_AnInlineGrantsUpperCasePathPatternOnNuGetIsRefused` (integration), the token-scope case of
`TestR_AUTHZ_005_PathPatternsDecideTheNuGetProtocolOnCanonicalPaths`.

### Q-AUTHZ-254 · The path dimension of a format with lower-case paths is compared in lower case, pattern and path alike

Status: Decided by the development agent — selected from the options below; the owner reviews it at the walkthrough.

Context: the third penetration test of F-82 (2026-11-08) reproduced the residual of Q-AUTHZ-253. A global policy with the
selector `it/later-*` and `paths.exclude: ["Acme.Secret/**"]` was saved while no repository matched the selector (201,
only `selector_matches_nothing`), a token with that scope was created, and then the NuGet repository `later-ng`: the
"excluding" token read `acme.secret/index.json` (200). A refusal can only see the repositories that exist when a grant is
written; a CI template's rendered value is not seen at all. Q-AUTHZ-253 had rejected folding for one reason: the
planned `{lower_id}/{lower_version}/.symbols/<entry path>` (R-NUGET-021, P1) keeps the case of the file names inside a
symbol package.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Fold pattern and path to lower case where the dimension is compiled for a format with lower-case paths (chosen) | `pathFoldIn` gives `compileSet` the case fold for a policy's grants (per format, in the index), a token scope's element and a CI inline grant (per repository, at the decision) and an exposure rule (per repository); `globSet.match` folds the path it compares | The result depends neither on when a grant was written nor on the selector or template that brought it; a mixed-case `.symbols/<entry path>` still matches, case-insensitively, as NuGet and the Windows debuggers that ask for symbols treat file names | Two entries that differ in case only cannot be told apart by a pattern |
| (b) Fold the pattern only | Lower-case the pattern, compare the path as stored | Exact for every path stored today | A mixed-case `.symbols` entry would match no folded pattern, the revisit condition of Q-AUTHZ-253 |
| (c) Keep the refusal alone | As Q-AUTHZ-253 | No change to matching | The reproduced residual stays: an exclude that a later repository makes useless |

**Choice:** (a). The fold applies to the path dimension of a repository whose format declares `catalog.Facts.LowerCasePaths`
(NuGet) and to nothing else: npm and generic paths keep their case. The write-time refusal of Q-AUTHZ-253 stays as feedback.
The revisit condition of Q-AUTHZ-253 is answered: folding both sides keeps `.symbols/<entry path>` matchable, and a pattern
names a symbol file in any case, which is how its clients name it.

**Rationale:** a stored grant must mean the same whichever repository its selector meets; folding where the dimension is
compiled is the one place every road passes. The development agent would revisit this if a format with lower-case paths
gains paths whose case carries meaning.

**Applied:** R-AUTHZ-005, Q-AUTHZ-253, [endpoints.md](endpoints.md) R-EP-012; `catalog.Facts.LowerCasePaths` (ADR-169);
code `internal/authz/{pattern.go,index.go,decide.go,ci.go,exposure.go,exposure_service.go}`. Tests: the folding cases of
`TestR_AUTHZ_005_AnUpperCasePathPatternOnALowerCaseFormatIsRefused`, the upper-case case of
`TestR_AUTHZ_005_PathPatternsDecideTheNuGetProtocolOnCanonicalPaths` (integration).

### Q-AUTHZ-255 · A version is decided on its file's path as well as on the package document's, and every place that shows a package shows only the versions the caller may read

Status: Decided by the development agent — selected from the options below; the owner reviews it at the walkthrough.

Context: finding F-83 (the penetration test of the F-82 fix, 2026-11-08, reproduced the same day by
`TestR_AUTHZ_005_AVersionPathPatternHidesTheVersionFromNPMDocuments`, `…FromNuGetDocuments` and
`TestR_EP_018_AVersionPathRuleHidesTheVersionFromNPMDocuments` before the change). Q-AUTHZ-250 decided a package, a listing
row and a document on the package document's path. A path pattern finer than a package's folder therefore hid the files it
named and left what they hold everywhere the package is shown: with `paths.exclude: ["hidden/-/hidden-2.0.0.tgz"]` the
tarball answered 404 while the package document listed 2.0.0 with its manifest and the README, `hidden/2.0.0` answered 200,
the package API named 2.0.0 the latest and served its README; with `paths.exclude: ["acme.secret/2.0.0/**"]` the NuGet
version list, the registrations and the search listed 2.0.0 with its description (`q=TOPSECRET` found it). An exposure
rule's path exclude of one tarball did the same on its endpoint (H-646).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The format names the file of a version, the decision point decides a version on it as well, and the documents filter for the caller (chosen) | `formats.VersionPather` (npm's tarball, NuGet's `.nupkg`) reaches the decision point as `catalog.Entry.VersionPath`; `ReadScope.MayRead(repo, pkg, version)` decides the document's path and the version's file's path, and allows only if both do; `ReadScope.VersionsByPath` says whether the caller's own grants (a grant, the token scope or a CI contribution with a path pattern) decide versions one by one; a local document of such a caller is filtered through `formats.CallerVersions`, and its rendition key and validator carry the fingerprint of the hidden versions | Every place that shows a version - document, single-version document, dist-tags, registration, search, package screens, findings - agrees with the download of its file; a caller without path patterns pays nothing | A document request of a caller with path patterns costs the version list of the revision once per replica, and its renditions are cached per set of hidden versions; a listing decides an allowed version twice (two evaluations, no statement) for such a caller and on an endpoint with rules, which doubles the decisions of H-299 and H-335 for them |
| (b) Refuse path patterns finer than a package's folder on package formats | 422 for `<name>/-/**`, `<id>/<version>/**`, a single file | No version-level decision | The layout of a format's files is not in the catalog, so "finer than a folder" cannot be told apart from a folder pattern when a grant is written; a selector covers repositories created later (the argument of Q-AUTHZ-250 (c)); the shape is legitimate (hide one leaked release) |
| (c) Filter documents only, keep listings on the document's path | The handlers filter; `MayRead` stays as it is | Smaller change | The package screens, the search and the findings keep showing the hidden version; two answers for one version |
| (d) Decide a version on its file's path alone | `MayRead` with a version asks the file's path only | One decision per version | A pattern that admits the files and not the document (`acme.core/1.0.0/**` alone) would list versions of a package whose document the caller may not read |

**Choice:** (a). `Engine.atVersion` completes a coordinate with a version and no path with the version's file's path;
`readScope.MayRead` asks it after the document's path when the subject's grants or the endpoint's rules can tell the two
apart (`readsByPath`, `exposesEverything`). `VersionIndependent` is false for a scope whose classified repositories are
read by path. A local npm document (full, abbreviated, single-version, dist-tags, the document of `?write=true`) and a local
NuGet version list and registration document filter with the caller's view; the rendition key and the validator carry
`formats.HiddenFingerprint` of the hidden versions of the revision (empty, and so shared with every caller, when nothing is
hidden), and a rendering of rows read at another revision than the one fingerprinted is served and not kept; the version
list of a revision is kept in the rendition cache under that revision (`formats.LiveVersions`), so a cached document of such a
caller still costs one statement (R-ARCH-027). A package whose every version is hidden answers like a missing one. A
NuGet `.nuspec` of a hidden version is a missing file. The exposure view decides a version on both paths too (Q-EP-85).
Kit check 13 fails a format whose package document lists a version whose file the caller may not read.

**Rationale:** a document entry is a copy of what the version's file holds, so a version may be shown exactly when its file
may be read; deciding it on both paths keeps the document rule of Q-AUTHZ-250 and adds the file. Keying a rendition by the
hidden versions instead of by the caller keeps the cache key a function of what was rendered, so no inventory of a caller's
inputs can be incomplete. The development agent would revisit this if a format's version has content in more than one file
that a document shows (then `VersionPather` names several), or if the per-caller renditions crowd the cache in practice.

**Applied:** R-AUTHZ-005 (the rule, examples, acceptance), [architecture.md](architecture.md) R-ARCH-008 (the contract),
R-ARCH-010 check 13, R-ARCH-027 (the budget of a document of such a caller), [endpoints.md](endpoints.md) Q-EP-85,
[formats/npm.md](formats/npm.md) Q-NPM-82, [formats/nuget.md](formats/nuget.md) Q-NUGET-120,
[vulnerability-scanning.md](vulnerability-scanning.md) Q-VULN-81 (what the decision looks at); ADR-174. Code:
`internal/authz/{package_path.go,visible.go,ci.go,exposure.go,exposure_service.go}`, `internal/formats/{formats.go,versions.go,virtual.go}`,
`internal/formats/catalog/catalog.go`, `internal/formats/npm/{versions.go,handlers_document.go,handlers_disttags.go,handlers_virtual.go,format.go}`,
`internal/formats/nuget/{documents.go,handlers_flat.go,handlers_registration.go,handlers_delete.go,match.go}`,
`internal/formats/formattest/{checks_authz.go,host.go}`. Tests: `TestR_AUTHZ_005_AVersionIsDecidedOnItsFilesPath`,
`TestR_EP_018_TheViewDecidesAVersionOnItsFilesPath`, the two integration tests above, `TestR_ARCH_010_Defects`
(`lists-hidden-versions`), `TestR_ARCH_027_ADocumentOfACallerWithPathPatternsCostsOneStatement` (the budget).

### Q-AUTHZ-256 · A write is decided on every file it writes or removes, whether or not the file is there, and a version the caller may not read is not the caller's to change

Status: Decided by the development agent — selected from the options below; the owner reviews it at the walkthrough.

Context: finding F-83 part 2 (H-586, reproduced the same day by `TestR_AUTHZ_005_AnNPMWriteIsDecidedOnEveryFileItWritesOrRemoves`
and `…ANuGetWriteIsDecidedOnEveryFileItWritesOrRemoves`). A protocol write was decided on one path and wrote or removed more:
an npm revisioned `PUT` and a package unpublish removed a version's tarball the caller's grant excluded (`/files/` refused
the same `DELETE`), a publish stored a provenance bundle under an excluded `**/*.sigstore`, a NuGet delete removed an excluded
`.snupkg` and `.nuspec`, a push stored an excluded generated `.nuspec`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Decide every path the operation can write or remove, before anything is written, whether or not a file is there; the first refusal refuses the whole operation; a version the caller may not read is left alone (chosen) | npm publish: the tarball and, with a bundle, the bundle; npm unpublish (both forms): the tarball and the bundle of every removed version; NuGet push: the `.nupkg` and the generated `.nuspec`; NuGet delete in mode delete: the `.nupkg`, the `.nuspec` and the `.snupkg`. A batch of paths is decided with `Host.PathsAllowed` (no audit event) and refused with the `Authorize` of the first path not held. An unpublish keeps the versions the caller may not read; a dist-tag at such a version, a NuGet delete, unlist or relist of one answer as for a missing version | `/files/` and the protocol refuse the same change of the same file; the answer tells nothing of which files exist; a caller cannot change what it cannot see | A grant that excludes one kind of file (`**/*.snupkg`) refuses every delete of a version, also one without that file |
| (b) Decide only the files that exist | The paths of the stored items of the operation | A version without a bundle can be unpublished under `**/*.sigstore` | The refusal says that the file exists, which the caller may not read: an existence oracle |
| (c) Refuse an operation whose paths a pattern finer than the folder splits | Decide the folder | Simple | Needs the format's layout in the decision point; same result as (a) for the common case |

**Choice:** (a). The npm unpublish decides in its transaction, because the versions it removes are read under the package
row lock; the decisions ask no statement and are made on the index the commit-time decision moved the request to
(`Host.Write`, R-AUTHZ-015), as every other decision of the transaction. The others decide before the body is stored or the
transaction opens. A version the caller's grants hide is not removed by an unpublish of the whole package either: the
caller's unpublish removes what the caller sees, and the package keeps the rest (the answers: [formats/npm.md](formats/npm.md)
Q-NPM-84). What a route names is decided by the route; what a folder it removes holds besides is decided by the host
(Q-AUTHZ-257).

**Rationale:** a change of a file is a change of that file, whatever route carries it; deciding every possible path keeps the
answer independent of what exists. The development agent would revisit (b) if a grant shape that excludes one kind of file
proves common for callers who delete.

**Applied:** R-AUTHZ-005 (examples, acceptance), [formats/npm.md](formats/npm.md) Q-NPM-83, [formats/nuget.md](formats/nuget.md)
Q-NUGET-121, [architecture.md](architecture.md) R-ARCH-008 (`Host.PathsAllowed`); ADR-174. Code: `internal/formats/npm/{publish.go,handlers_unpublish.go,paths.go}`,
`internal/formats/nuget/{handlers_publish.go,handlers_delete.go}`, `internal/formathost/host.go` (`PathsAllowed`). Tests: the two
integration tests above.

### Q-AUTHZ-257 · Every file a format's removal takes is decided for the caller, the files of a folder it names included

Status: Decided by the development agent — selected from the options below; the owner reviews it at the walkthrough.

Context: the code review of F-83. NuGet's hard delete removes the folder `{id}/{version}` through the package writer and decides
three files of it (the `.nupkg`, the `.nuspec`, the `.snupkg`, Q-NUGET-121). A folder can hold more: the embedded files of
R-NUGET-024 (`.embedded/*`) and symbol files (`.symbols/**`) are paths the classification places in the version, and no local
route stores them today. A file of the folder that the route does not name was removed without a decision, a removal `/files/`
refuses file by file. The items layer's `RemoveIn` decided nothing ("the caller decided every path before"). The remover of the
OCI work package guards what a storage hook cascades to, not the files of a folder a route names.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The host decides every file a removal of a request takes, in the transaction, before any is removed (chosen) | `items.RemoveSpec.Check` receives the files of the removal, the subtree of a folder included; the writer of `Host.Write` sets it to `removalAllowed`: `Host.PathsAllowed` for `delete`, and the first file refused refuses the whole removal with that file's audited refusal (`Authorize`) | Holds every format to it, whatever its folders hold later; the decision follows what exists, read in the transaction that removes it | A refusal of a file that exists tells that it exists; a file the route already decided is decided again |
| (b) The NuGet route names a fixed list of a version's files | `.nupkg`, `.nuspec`, `.snupkg`, the three embedded files | No host change | A symbol file has an arbitrary path; a file a later route stores under the folder is missed again |
| (c) Delete the named files instead of the folder | Remove what the route decided | Exact | Leaves behind what the folder holds besides, a version half removed |

**Choice:** (a). A route keeps deciding the paths it names, whether or not they exist (Q-AUTHZ-256), so its answer does not
depend on what exists; the host's check adds the files the route does not name. The refusal is audited as every refusal of a
write is, and the guard of a storage hook's cascade (`cascadeAllowed`, Q-OCI-76), which answered with an unaudited 403, now
refuses through the same function: a refused file of a folder and a refused file of a cascade answer and audit alike.

**Rationale:** the removal's own list is the only complete list of what it takes. The development agent would revisit the
existence signal if a format stores files under a version's folder that a caller who may delete the version must not learn of.

**Applied:** R-AUTHZ-005 (acceptance), [formats/nuget.md](formats/nuget.md) R-NUGET-013, Q-NUGET-121. Code: `internal/items/delete.go`
(`RemoveSpec.Check`), `internal/formathost/{host.go,writer.go}` (`removalAllowed`). Test: the folder case of
`TestR_AUTHZ_005_ANuGetWriteIsDecidedOnEveryFileItWritesOrRemoves`.

### Q-AUTHZ-258 · A format whose paths place files in versions names the file of a version, and the kit holds every such format to it

Status: Decided by the development agent — selected from the options below; the owner reviews it at the walkthrough.

Context: the code review of F-83 (M3). The fifth rule of kit check 13 ran only for a format that implements `VersionPather`, so
a format without it passed. OCI had none: `catalog.Entry.VersionPath` was nil for it, so the package screens
(`packages/view/service.go`) decided an image's versions, its tags, on the tags list `<name>/_taglist` alone (OCI has no
vulnerability adapter, so no vulnerability report lists a tag), and a path pattern that hides one tag (`team/app/_tags/v2`) left the tag listed there while the tags list
of `/v2/` left it out (Q-OCI-140). The OCI fixture also had no package document that named a stored version, so even with the
interface the rule would have judged nothing (a tags list seeded in the data port's twin and not through a push has no package
rows).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The fifth rule is mandatory, and OCI names its tag path (chosen) | The kit fails a format with package names whose fixture places a path in a version (a path case, or a content case's canonical path, that `Classify` gives a version) and that has no `VersionPather`; it fails a fixture that serves package documents and judges no version; OCI's `VersionPath` is `TagPath`, the item that holds the manifest a tag points at; its fixture gains the tags list of a pushed tag | Every format that has versions with files is held to the rule, the next one included; the package screens and the tags list decide a tag on one path | A fixture has to seed a document through the format's own writes |
| (b) Keep the rule opt-in | A format chooses | No fixture work | A format that forgets the interface passes, as OCI did |
| (c) The manifest path as OCI's version file | `<name>/_manifests/<digest>` | The bytes themselves | A version is a tag, and a pattern names tags; the tag's item is the one a pull by tag is decided on |

**Choice:** (a). The test format without `VersionPather` fails check 13 and no other check.

**Rationale:** a rule a format can skip by omission is no rule; the classification already says which paths belong to versions, so
the kit knows when the interface is missing. The development agent would revisit (c) if OCI versions become digests.

**Applied:** [architecture.md](architecture.md) R-ARCH-010 (check 13, acceptance), R-AUTHZ-005, [formats/oci.md](formats/oci.md)
(the package version). Code: `internal/formats/formattest/checks_authz.go` (`versionsHaveAFile`, the count of judged versions),
`internal/formats/formattest/testformat/format.go` (`WithoutVersionPath`), `internal/formats/oci/paths.go` (`VersionPath`), the
OCI fixture. Tests: `TestR_ARCH_010_Defects` (the format without `VersionPather`, `versions-outside-rows` for a fixture that judges no version), `TestR_ARCH_010_Conformance` of OCI,
`TestR_AUTHZ_005_APathPatternOnATagHidesItFromThePackageAPI` (integration, red without OCI's `VersionPath`).

### Q-AUTHZ-260 · A job carries who asked, and reads what that subject holds for every batch

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

The delete job of [storage.md](storage.md) R-STOR-018 decides every batch again on the caller ([storage.md](storage.md)
Q-STOR-190). It needs a subject outside any request. The job stored the request's whole subject (`authz.EncodeSubject`): the
groups the stage resolved, the token's scope, a CI token's frozen bundle, the endpoint. Decided again on that, a batch kept the
authority of the moment of the request.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The whole subject of the request (as built) | The job decodes the stored subject and decides on it | No read per batch | The groups, the scope and the bundle are frozen; a disabled principal and a revoked token are not seen at all |
| (b) Who asked, the credential and the endpoint; the rest read again per batch (chosen) | `authz.JobSubject` holds the principal (kind, ID, name), the token ID or the session ID, the method and the endpoint with its cache-fill policy; each batch reads the standing (`authz.Standing`): the principal from the principal cache (status, groups), the token or the session by its ID from PostgreSQL (the token's scope and a CI token's bundle from its row) | Every input the authentication stage reads at a request is read again at the same freshness; the job holds no authority of its own | One principal lookup and one row read per batch |
| (c) Keep the credential and run the authentication stage again | Store the token's hash or the session's and validate it per batch | The stage's code unchanged | A credential, or the key to its cache entry, in a job's arguments (INV-8); the session's CSRF and path rules have no meaning without a request |

**Choice:** (b). The subject a batch decides on is built in `internal/authz` from the two halves (`JobSubject.Subject`), so the
architecture test of R-EP-013 still finds no subject built elsewhere. What is fixed by design is read from where it is fixed: a
token's scope does not change for the token, and a CI token's inline grants, caps and contributing mappings are frozen at the
exchange (Q-CI-17), so both come from the token's row, which says the same as the request did; the state of a contributing
mapping is the index's, as for a request (Q-CI-47). What is live is read live: the principal's status, kind, name and groups,
the token's revocation and the session's end or withdrawal (Q-AUTHZ-262). The batch's subject takes the principal's current name,
because an ownership is decided by name (R-AUTHZ-018) and a rename by the login's username sync (R-AUTH-007) must not keep the old
name's packages inside the job; a principal whose kind changed is refused. The principal comes through the cache of R-AUTH-023 under
its freshness gate, so a disable, a rename or a membership change reaches the next batch within `auth.revocation_bound`; the token
and the session rows are read by ID and never cached, so their revocation is seen at once. The endpoint and its cache-fill policy stay the request's: the job
decides under the request's exposure ceiling (R-EP-013, H-166 in [endpoints-and-exposure.md](../security/endpoints-and-exposure.md)). An anonymous caller has no
principal and no credential to read; the decision point reads the runtime switch of R-AUTH-037 for each batch. A subject that
names no credential and is not anonymous cannot be asked again and is refused. `authz.EncodeSubject` and
`Engine.PathsAllowedFor` are removed.

**Rationale:** the authentication stage resolves the groups and checks the credential when a request is evaluated
(R-AUTHZ-002, R-AUTH-023); a step of a job is evaluated later, so it reads them then. Storing only references keeps a job's
arguments free of authority and of secrets. The development agent would revisit (b) if another job decides for a request and
needs a credential this reader does not know.

**Applied in the spec:** R-AUTHZ-002 (the bullet on jobs); [storage.md](storage.md) R-STOR-018, Q-STOR-190; [identity-and-authentication.md](identity-and-authentication.md) R-AUTH-023 (the bound for the job). Code:
`internal/authz/jobsubject.go`, `internal/auth/standing.go` (`JobStanding`), `internal/auth/token/standing.go`,
`internal/auth/session/standing.go`, `internal/api/edge/access.go` (`Access.JobSubject`), `internal/files/delete.go`. Tests:
`TestJobSubject`, `TestJobStanding`, `TestR_STOR_018_TheDeleteJobDecidesEveryBatchOnTheStandingOfItsSubject`.

### Q-AUTHZ-261 · A withdrawn credential stops a job, a credential that reached its end of life does not

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Q-AUTHZ-260 reads the credential that queued a job for every batch. A request with a credential that ended is refused,
whatever ended it (R-AUTH-023). A job is not a request: it was accepted while the credential lived, and it runs later, for
seconds or, behind other jobs, longer.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Every end of the credential stops the job | An expired token and a session ended for any reason refuse as a revoked token does | One rule with R-AUTH-023 | A CI token (minutes to an hour) that expires while its job waits stops a delete nobody withdrew; a session timeout, a sign-in elsewhere that evicts the session and a re-authentication stop the user's own delete |
| (b) Only the principal's status counts | The job ignores its credential | Nothing to read but the principal | A revoked token keeps deleting; the answer to a leaked token is its revocation, not the disabling of its owner |
| (c) A withdrawal stops the job, an end by time does not (chosen) | Stops it: a token revoked for any reason (by its owner or an administrator, a rotation without overlap, a revoked parent, a deleted mapping) or deleted; a session ended by a sign-out, the end of a user's other sessions, an administrator's end, a disable or a changed break-glass password, or whose row is gone, or whose end reason this build does not know; a principal that is not active or no longer exists. Does not: a token past its expiry (also the end of a rotation's overlap); a session past its idle or absolute deadline, evicted by the session limit or replaced by a re-authentication | Someone's decision that the credential must stop acting stops it; the job finishes where nobody decided anything | A user's own sign-out stops the user's delete; a job may outlive the credential's expiry (H-659 in [file-transfer.md](../security/file-transfer.md)) |

**Choice:** (c). The reasons are those of the session row (R-AUTH-010, `ui_session.end_reason`) and the token row (`revoked_at`
set). A session that ended by time, by the session limit or by a re-authentication is still reached by a later withdrawal: the
withdrawals mark the sessions they reach, ended ones included, and a re-authentication names its successor (Q-AUTHZ-262). The
refusals are the 401 a request would get now: `invalid_token` for a revoked token, `session_expired` for a withdrawn session,
`principal_disabled` for a principal that is not active; a credential whose row is gone is refused with a reason of its own
([storage.md](storage.md) Q-STOR-194); the job's error carries them with the reason. A token or a session that names another
principal than the job's subject is refused like a withdrawn one.

**Rationale:** an expiry and a timeout are no one's decision about a credential, and the request it authenticated was accepted
while it lived; a revocation, a sign-out and a disable are decisions that the credential, or its principal, must stop acting,
and the job is that credential acting. The sign-out joins the withdrawals because the end of a user's other sessions, the answer
to a session that is not the user's, records the same reason (R-AUTH-010) and the two cannot be told apart. The development agent
would revisit the sign-out if the end of other sessions gets a reason of its own.

**Applied in the spec:** R-AUTHZ-002 (the bullet on jobs); [storage.md](storage.md) R-STOR-018, Q-STOR-190. Code:
`internal/auth/session/standing.go` (`stillStands`), `internal/auth/token/standing.go`, `internal/auth/standing.go`, the query
`SessionStandingChain`. Tests: `TestStanding`, `TestJobStanding`, `TestR_STOR_018_TheDeleteJobReadsTheSessionThatAskedFromItsRow`,
`TestR_STOR_018_TheDeleteJobDecidesEveryBatchOnTheStandingOfItsSubject` (an expired token lets the job finish).

### Q-AUTHZ-262 · A withdrawal reaches a session that had ended already, and a re-authentication hands its job to its successor

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Q-AUTHZ-261 keeps a job running when its session ended by time, by the session limit or by a re-authentication. Every statement that
withdraws sessions matched live rows only (`ended_at IS NULL`), so such a session could no longer be withdrawn: a user who
re-authenticated and then signed out, a user whose evicted session queued the delete and who then ended the other sessions, or an
administrator who changed the break-glass password after a stolen break-glass session idled out, stopped nothing.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Document it in H-659 with the remedies | Disable the principal, remove the grants, cancel the job | No change | The answers a user and an administrator reach for do not work for exactly the sessions that ended quietly |
| (b) A withdrawal marker and the re-authentication's successor (chosen) | `ui_session.withdrawn_at`, set by every withdrawal on every session it reaches, ended ones included (a sign-out, the end of one or of the other sessions, an administrator's end, a disable, the end of every session, a break-glass password change or rotation); `ui_session.successor_id`, set when a re-authentication replaces a session; `session.Standing` follows the successors and refuses a withdrawn session at any link | Every withdrawal of Q-AUTHZ-261 holds for every session, whatever ended it before | Two columns (migration 00059); a chain of successors, read recursively |
| (c) Withdraw by principal and time | A per-principal "sessions withdrawn at" that the reader compares with a session's creation | One column | A sign-out of one session would withdraw every session of the principal |

**Choice:** (b). Every withdrawal ends the live sessions in its scope with the marker and then, in a second statement of the same
transaction, marks the sessions in its scope that had ended already: under READ COMMITTED the second statement reads anew, so a
session that ended while the first statement waited for its row (a deadline, an eviction, a re-authentication) is marked too. The
end of one session, of the other sessions and of every session of a principal take the principal's row lock first, as a sign-in
and a re-authentication do, so no session of the principal starts or is replaced in between. The end of every session marks the
ended ones in batches of 1,000 (`WithdrawEndedSessions`), also after a restore. The end of the other sessions keeps only the kept
session itself and withdraws the sessions it replaced, by a sign-in or a re-authentication, with the rest: an ended predecessor
authenticates nothing, so keeping it would keep only the delete jobs queued with it, and those are what a stolen copy of the
browser's earlier session carries. The price is deliberate: a user's own delete jobs queued before a sign-in or a re-authentication in
the same browser stop when the user ends the other sessions, which is an action taken for security. Without that action a chain of
replacements carries a job on as before. The end of every break-glass session takes the break-glass principal's row lock first, as
a break-glass sign-in does. A sign-in of the same principal in the same browser (the fixation rule of R-AUTH-010) names its
successor on the session it ends, as a re-authentication does, and continues it; the session of another principal it ends, one
planted in the browser, is withdrawn. An eviction, a timeout and a re-authentication set no marker. `auth.session.ended` names the
sessions a withdrawal ended (`session_ids`) and, apart, those it withdrew after they had ended (`withdrawn_ids`), and is written when
there is either; the end of one own session answers that it found a session when it withdrew one that had ended, so a repeated end of
an own session that had ended by time, by the session limit or by a re-authentication answers `204` once and `404` after that. One statement reads a session's chain of successors,
up to 1,000 links past the first (a link exists only while the replaced session lives, so a chain grows by one link per sign-in or
re-authentication of a live session); a longer chain is withdrawn, a missing link is purged ([storage.md](storage.md) Q-STOR-194).
A session that a re-authentication replaced before migration 00059 has no successor and stands like one that ended by time; such rows
are gone 30 days after their end.

**Rationale:** a withdrawal is a decision about the sessions it reaches, not only about the live ones; recording it on the row is
what lets a later reader honour it. The re-authenticated session is the same browser's continuation, so the job of its predecessor
lives and ends with it. The development agent would revisit it if sessions get an end reason of their own for the end of other
sessions (Q-AUTHZ-261).

**Applied in the spec:** [identity-and-authentication.md](identity-and-authentication.md) R-AUTH-010 (the data model and the
withdrawal of ended sessions); [storage.md](storage.md) R-STOR-018. Code: migration 00059, `internal/meta/postgres/queries/login.sql`
(`EndSessionsByID`, `EndSessionReplaced`, `EndSessionBySecret`, `EndSessionsOfPrincipal`, `WithdrawEndedSessionsOfPrincipal`,
`EndAllSessions`, `WithdrawEndedSessions`, `EndBreakGlassSessions`, `WithdrawEndedBreakGlassSessions`, `EndSessionOfPrincipal`,
`WithdrawEndedSessionOfPrincipal`, `EndSessionsOfPrincipalExcept`, `WithdrawEndedSessionsOfPrincipalExcept`, `SessionStandingChain`),
`internal/meta/postgres/queries/identity.sql` (`IdentityEndSessions`), `internal/auth/session/{standing.go,store.go,end.go}`,
`internal/backup/postrestore/postrestore.go`, `internal/auth/principal/{manager.go,journal_replay.go}`. Tests:
`TestR_STOR_018_TheDeleteJobReadsTheSessionThatAskedFromItsRow` (a re-authentication then a sign-out, a sign-in in the same browser
then a sign-out, a planted session of another principal, the end of one own session that had ended, an eviction then the end of the
other sessions, an administrator's end of an idle session, a break-glass session that idled out then a password change), the unit
test `TestStanding` (the chain).

### Q-AUTHZ-263 · Visibility levels replace the anonymous switch, the principal `authenticated` and `requireAuthentication`

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the owner's task T-18 (2026-11-11) asks for the levels private, internal and public as in his GitLab, with one
mechanism, not a second one beside the anonymous switch. Before it, three mechanisms opened content: the runtime switch
`anonymousAccess` with its ceiling `auth.anonymous.allowed` together with policy entries for `anonymous` (R-AUTH-037,
R-AUTHZ-003), the built-in principal `authenticated` in policy entries (R-AUTHZ-030, Q-AUTHZ-3), and the repository field
`requireAuthentication`, which closed again what the first opened.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Levels replace all three (chosen) | A level on the project and on each part of a repository is the only way to open content to every internal user or to everyone; no policy names `anonymous` or `authenticated`; the switch, its ceiling and `requireAuthentication` go | One mechanism, the owner's; what is open is read from the object itself, not from the union of policies; the UI shows it where the object is | Every test, fixture and screen that used the switch or the principals changes (pre-alpha: allowed) |
| (b) Levels compile into policy entries | A level writes or removes entries for `anonymous` and `authenticated` | The decision point stays as it is | Two places that say the same thing and can disagree; a policy edit could open what a level closed |
| (c) Levels beside the switch | The anonymous switch stays as an installation-wide master switch | Familiar | The second mechanism the owner ruled out |

**Choice:** (a).

**Rationale:** the owner's rule "one mechanism" decides between (a) and (c), and (b) is two mechanisms under one name. With
(a) the question "who can read this repository" has its answer on the repository and its project, and the evaluator adds one
read source next to grants and roles (R-AUTHZ-035), which is cheaper than any policy entry because it needs no principal set.
The operator keeps a lever without a runtime switch: the default endpoint's `auth.anonymous` (Q-AUTH-232). The development
agent would revisit this if the owner wants per-principal anonymous exceptions that a level cannot express.

**Applied in the spec:** R-AUTHZ-003 and R-AUTHZ-030 rewritten; R-AUTHZ-033 to R-AUTHZ-036 new; R-AUTHZ-001, R-AUTHZ-002,
R-AUTHZ-008, R-AUTHZ-009, R-AUTHZ-010, R-AUTHZ-012, R-AUTHZ-015, R-AUTHZ-016, R-AUTHZ-019, R-AUTHZ-021, R-AUTHZ-025,
R-AUTHZ-026, R-AUTHZ-028, R-AUTHZ-029, R-AUTHZ-031; [identity-and-authentication.md](identity-and-authentication.md)
R-AUTH-016, R-AUTH-037, R-AUTH-040, R-AUTH-044; [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-004;
[ADR-192](../decisions.md#adr-192--visibility-levels-replace-the-anonymous-switch-and-the-principal-authenticated).
Q-AUTHZ-3 is superseded.

### Q-AUTHZ-264 · A level is stored on its object, and the installation's default is copied when a project or a repository is created

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Stored levels, the default copied at create (chosen) | Each project and each repository holds its levels; the installation's default is read once, when the object is created | As GitLab: a project's visibility is its own; what a request reads is one column; a change of the default never opens an existing object | Changing the default does not change existing objects (by design) |
| (b) An `inherit` value | A repository may say `inherit` and follow its project live | One switch opens a whole project | A project raised by an administrator would open every inheriting repository at once, including those whose Owner never chose it; the effective level is computed, not read |
| (c) Inherit by default, override per object | (b) as the default | Fewer stored values | The same surprise as (b), as the default |

**Choice:** (a).

**Rationale:** raising a level must be a decision on the object that becomes more open; (b) and (c) let a decision on the
project open repositories nobody looked at. Lowering is where inheritance would help, and Q-AUTHZ-265 gets that effect by a
cascade. The development agent would revisit this if administrators routinely raise a project and then every repository one by
one; a bulk action would come before an inherit value.

**Applied in the spec:** R-AUTHZ-033 (the table and rule 1).

### Q-AUTHZ-265 · A repository is never more open than its project, and lowering a project lowers its repositories

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Bound by the project; lowering cascades in one transaction (chosen) | A repository's artifacts level is at most its project's; lowering a project lowers every repository above the new level in the same transaction | One action closes a project for good; stored values never claim more than is in effect; the database can hold the rule | Raising the project back does not restore the repositories' former levels |
| (b) Bound by the project; lowering is refused while a repository is above | GitLab's answer for a group with more open projects | Nothing changes behind the administrator's back | Closing a project in a hurry needs a write per repository first |
| (c) No bound; the effective level is the lower of the two | Store anything, decide the minimum | No cascade | A stored `public` that reads back but is not in effect, and that becomes effective again when the project is raised |

**Choice:** (a). The owner's GitLab says a project may not be more open than its group; the bound is kept, and the cascade
replaces GitLab's refusal.

**Rationale:** lowering a project is the action of someone closing something, often in a hurry, so it must take effect in one
write and on every replica within the bound of R-AUTHZ-028. (c) keeps a latent `public` that a later raise would silently make
effective again, which is the surprise (a) avoids. The cascade is audited per repository and journaled, so a restore replays
it. A move to another project lowers in the same way. The development agent would revisit this if the owner prefers GitLab's
refusal.

**Applied in the spec:** R-AUTHZ-033 (rules 2 and 3, the data model, `AI008`); [repositories-and-proxy.md](repositories-and-proxy.md)
R-REPO-060.

### Q-AUTHZ-266 · The wiki of a repository is never more open than its artifacts

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Wiki at most artifacts (chosen) | `visibility.wiki` ≤ `visibility.artifacts` | Every wiki surface may assume that the caller reads the package, so no documentation route tells a package name, a version or a README to someone who cannot see the package; the owner's example ("artifacts public, wiki internal") fits | Public documentation of internal packages is not possible |
| (b) Independent levels, both bound by the project | As GitLab's project features | Also "documentation public, packages internal" | Every documentation route needs its own existence rules for a package the caller cannot see; a README names what the caller may not list |

**Choice:** (a).

**Rationale:** the owner's example is the restrictive direction. With (b) the wiki routes would have to answer for packages that
are hidden from the same caller, which turns the documentation into an oracle for names (R-AUTHZ-016). The development agent
would revisit this if the owner wants public documentation of internal packages; the wiki routes would then need the existence
rules of R-AUTHZ-016 on their own.

**Applied in the spec:** R-AUTHZ-033 (rule 2, the check constraint), R-AUTHZ-034.

### Q-AUTHZ-267 · An administrator sets the level of a project, its Owner sets the levels of its repositories within it

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Project by `projects:write`, repositories by the Owner or `repositories:write-config` up to the project, the default by `security-settings:write` (chosen) | The project's level is the administrator's ceiling, the Owner chooses within it | Keeps the rule of Q-AUTHZ-3 that opening content to the whole organization is an administrator's decision, and still lets a team choose per repository | An Owner who wants an internal project asks an administrator |
| (b) The Owner sets everything | GitLab's default | No administrator involved | A project Owner opens content to the internet on its own |
| (c) Only administrators set any level | Every level by `projects:write` or `repositories:write-config` | Central | Owners cannot keep a repository private inside an internal project, which is the most common narrowing |

**Choice:** (a). A write that raises a level needs recent authentication (R-API-007).

**Rationale:** GitLab lets an administrator restrict the levels non-administrators may choose; (a) is that restriction made the
default, and it matches what trove decided for `authenticated` before (Q-AUTHZ-3: only `policies:write` opened content to
everyone). The Owner keeps every narrowing and every opening up to the administrator's ceiling. The development agent would
revisit this if projects are created mostly by their teams, which would call for a setting of restricted levels as in GitLab.

**Applied in the spec:** R-AUTHZ-033 (the table, rule 4), R-AUTHZ-026 (the Owner's table); [api-and-ui.md](api-and-ui.md)
R-API-007, R-API-056, R-API-057.

### Q-AUTHZ-268 · A subject is internal when it is a user through a session or a personal token, not flagged external and not through a Bastion

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.
The user flag is the owner's (2026-11-11); the task leaves open whether a project's technical accounts of external parties and
the Bastion's partners count as external.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Users through a session or a PAT, without the flag, not through a Bastion (chosen) | Service accounts, project service accounts, CI tokens, Bastion registrations and the anonymous subject are never internal; a request through a Bastion never reads by `internal` | Every non-human credential reads only what names it or what is public, as `authenticated` did; the internet-facing door never serves the organization's internal content to a leaked user token | An employee who uses the Bastion from outside reads only what names him |
| (b) Project service accounts external, other service accounts internal | Only the partners' accounts are external | Instance automation reads internal content without a grant | A monitoring or SCIM token reads every internal package (the reason of Q-AUTHZ-3 (c)) |
| (c) As (a), but a request through a Bastion is judged by its user | The Bastion is only a channel | Employees read internal content from outside | A leaked personal token reads everything internal from the internet through a cache shared with partners |

**Choice:** (a). The technical accounts of a project's external parties (phase 5a) are service accounts and never internal; the
Bastion's partners present service-account tokens of their projects (R-BAS-004's fixture), and a user who presents a personal
token through a Bastion reads there what names it and what is public ([bastion.md](bastion.md)
[Q-BAS-148](bastion.md#q-bas-148--a-request-through-a-bastion-never-reads-by-the-level-internal)).

**Rationale:** "internal" is the organization's own people at their own doors. A service account is created for one purpose and
its token is the credential most often stored elsewhere, which is why `authenticated` never covered it; the same reason holds for
`internal`. The Bastion exists to serve external parties on the internet, so the level that means "everyone inside" does not
reach through it. The development agent would revisit the Bastion part if the owner wants employees to read internal content
through it.

**Applied in the spec:** R-AUTHZ-030, R-AUTHZ-002; [bastion.md](bastion.md) R-BAS-004, R-BAS-008.

### Q-AUTHZ-269 · A level reads beside the grants; the token scope narrows `internal`, and nothing narrows `public` but the endpoint

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A read source in the union; the scope narrows `internal`, not `public`; the endpoint first (chosen) | R-AUTHZ-035 | A client whose token is scoped elsewhere still installs public packages, as it would without the token; `internal` stays a credential's right, bounded like one | A narrowly scoped token reads more than its scope names, but only what anyone reads |
| (b) The scope narrows both | A level is read under the scope like a grant | Uniform | A CI job with a scoped token gets 404 for a public package that the same request without a token gets; npm sends the token to every URL of the registry host, so the job cannot drop it |
| (c) A level replaces the grants | Members read by their roles only where the level is `private` | One source per repository | Contradicts the union of R-AUTHZ-001 |

**Choice:** (a).

**Rationale:** the token scope protects what a credential can reach; a public read needs no credential, so a scope protects
nothing there and only breaks clients. The endpoint's exposure is different: it is a ceiling for every subject including the
anonymous one, so it binds levels as it binds grants (R-EP-013). The development agent would revisit this if a scoped token
must be kept from public content for a reason other than access, which would be a property of the client, not of the scope.

**Applied in the spec:** R-AUTHZ-035, R-AUTHZ-015 (`Allowed`), R-AUTHZ-001.

### Q-AUTHZ-270 · The wiki is the documentation trove serves or shows apart from the files, never a file inside an artifact

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) What trove serves or shows apart from the files (chosen) | npm's `readme` members, NuGet's README route, the README operation, T-19's documentation | Every surface is one trove controls; a file the client downloads stays an artifact | A README inside a tarball reaches whoever downloads it (H-880) |
| (b) Also the README file inside an artifact | Redact or refuse the file | The wiki would be really hidden | Changes the bytes of an artifact: every digest, signature and lockfile integrity check breaks |
| (c) Every metadata member that is prose (description, release notes) | Hide them with the README | Wider | Descriptions are what a client shows in its list and search; hiding them breaks `npm search`, the NuGet gallery views and the repository list |

**Choice:** (a), with the table of R-AUTHZ-034 per format: npm the `readme` and `readmeFilename` of the full package document
and the README operation; NuGet the README route and the README operation; Ansible Galaxy the README operation and the
documentation surfaces of T-19; OCI, Debian and generic none.

**Rationale:** an artifact's bytes are fixed by its digest, so (b) is impossible without breaking every client that verifies.
(c) hides what clients need to choose a package. The gap that remains is recorded honestly as H-880. The development agent would
revisit this when a format adds documentation trove generates (T-19 does), which joins the table.

**Applied in the spec:** R-AUTHZ-034; [formats/npm.md](formats/npm.md) R-NPM-010, [formats/nuget.md](formats/nuget.md)
R-NUGET-024, [formats/oci.md](formats/oci.md), [formats/debian.md](formats/debian.md).

### Q-AUTHZ-271 · A caller who reads the artifacts but not the wiki gets the refusal of a read on a wiki route, and documents without the wiki members

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Route: refusal of a read; document: members absent; UI: tab absent (chosen) | R-AUTHZ-034's table | A client that reads the document keeps working (npm prints nothing for a missing `readme`); a wiki route answers like a missing one, so it tells nothing; a client without credentials gets the 401 it can answer | The caller is not told why there is no README |
| (b) A redacted document | `readme: "(hidden)"` | Explicit | A value that is not the package's README, cached and shown by clients as if it were |
| (c) 403 on the document | Refuse the whole packument | Clear | Breaks `npm install` for a caller who may install, the opposite of the owner's example |

**Choice:** (a).

**Rationale:** the owner's example demands that the artifacts keep working, which rules out (c); (b) puts invented content into
a document that clients cache and display. An absent member is what a package without a README has, so every client already
handles it. The UI follows: no tab, no request (R-UI-057). The development agent would revisit this if users ask why a README is
missing often enough to show a hint.

**Applied in the spec:** R-AUTHZ-034; [api-and-ui.md](api-and-ui.md) R-API-029, R-UI-057.

### Q-AUTHZ-272 · The wiki check is one coordinate part, asked through a route flag, a host question and a document extension

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.
This is the seam with T-19 (the documentation of Ansible Galaxy collections): its routes and members are wiki content and use
these forms; it defines no check of its own.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `Coordinate.Part` in the decision point; `Route.Wiki`, `Host.WikiReadable`, `x-trove-part: wiki` and `ReadScope.MayReadWiki` as the only ways to ask (chosen) | One evaluation, three entry points by situation | The route flag decides before the handler and audits like every read; the question needs no audit for a member left out; the document extension names the management operations that serve wiki content, for their handlers and a test; the kit can check each | Five names to learn |
| (b) A new action `wiki` | Grants name it | Fine-grained grants | Every policy, role and preset changes; the owner asked for levels, not for an action |
| (c) Each format decides in its handlers | No contract change | Easy start | Nothing makes a new handler ask; the T-19 draft's own gate is one function per format |

**Choice:** (a). The Go contracts are in [visibility-work-packages.md](../projektplan/visibility-work-packages.md) WP0.

**Rationale:** grants stay as they are (a `read` covers both parts), so the only new input is the level of the part; a
coordinate field carries it through every path that already decides a read, listings included. The three forms match the three
situations a format meets, and the kit test of R-AUTHZ-034 makes a forgotten form visible. The development agent would revisit
this if a format needs a part that is neither artifacts nor wiki.

**Applied in the spec:** R-AUTHZ-034; [api-and-ui.md](api-and-ui.md) R-API-058; [architecture.md](architecture.md) R-ARCH-010.

### Q-AUTHZ-273 · Every listing and every feature that asks for `read` counts a read by a level

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A read by a level is a read everywhere (chosen) | Repository lists, the package API, search, counts, vulnerability findings, setup snippets | What a caller can download it can also find; one rule | An internal user's repository list grows with every internal repository |
| (b) Levels only on the protocol routes | Lists show what grants give | Short lists | A caller can install what it cannot find; the UI breaks for the public visitor |

**Choice:** (a). A search over wiki text is a wiki read; a search over names and descriptions an artifacts read.

**Rationale:** R-AUTHZ-016 already requires that listings contain exactly what a caller may read; a level that reads but does not
list would break it in the other direction. `authz.ReadScope` gives a `ReadAll` shape to a repository a level opens, so the
filter stays one SQL predicate per repository (Q-API-165). The development agent records no revisit condition.

**Applied in the spec:** R-AUTHZ-035 rule 7; [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-012;
[api-and-ui.md](api-and-ui.md) R-API-029.

### Q-AUTHZ-274 · A virtual repository has levels of its own, and the anonymous subject still needs every member public

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The virtual repository's artifacts level opens its gate; members by their own levels; rule 6 for the anonymous subject (chosen) | R-AUTHZ-035 rule 5 | Content is always decided on the member (Q-AUTHZ-1); an anonymous client never sees an incomplete merge it cannot tell from the real one | A public virtual repository over one internal member answers 401 to anonymous callers |
| (b) The gate's level is the lowest of its members' | Computed | No separate setting | Adding a member changes who may use the endpoint |
| (c) Drop rule 6 for public virtual repositories | Anonymous callers get the public part | Simpler | The dependency-confusion risk rule 6 closes: a public package of the name an internal member holds |

**Choice:** (a).

**Rationale:** rule 6 exists so that a client without credentials is challenged instead of resolving a name from the public
member that an internal member also holds; the levels do not change that risk. Members keep their own levels, so rearranging
members never opens content (R-AUTHZ-009). The development agent would revisit this if a public virtual repository over internal
members turns out to be a common wish, which would need namespace claims that the anonymous subject honours first.

**Applied in the spec:** R-AUTHZ-009 rule 6, R-AUTHZ-035 rule 5, R-AUTHZ-015.

### Q-AUTHZ-275 · A level reaches every replica through the grant index, the external flag through the principal cache

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Levels in the grant index (`authz`, `repo_config`), the flag with the principal (`auth`) (chosen) | Each travels with what already carries its kind of fact | No new change area and no new cache; the bounds are the existing ones; a disabled user and an external user take effect on the same path | Two bounds to state |
| (b) The flag in the grant index as a set of external users | The index reads `app_user.external` | One cache | Every flag change rebuilds the index of every replica; the index would hold one entry per user |
| (c) A cache of its own for the levels | A change area `visibility` | Independent | A third bound and a third consistency rule |

**Choice:** (a). The namespace-claims index and a CI token's frozen bundle are no inputs; the rendition caches key on the wiki
answer per contributing repository.

**Rationale:** the grant index is built from projects and repositories in one snapshot already (R-AUTHZ-028), so a level costs a
column there. The flag is a user attribute resolved with the user's status and groups (R-AUTH-023), which is the path a disable
takes and which the job subject re-reads per batch (Q-AUTHZ-260). The development agent records no revisit condition.

**Applied in the spec:** R-AUTHZ-036, R-AUTHZ-028; [identity-and-authentication.md](identity-and-authentication.md) R-AUTH-044.

### Q-AUTHZ-276 · The levels are columns, and the database holds the rules of the container

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Columns on `project` and `repositories`, a check for the wiki, a deferred constraint trigger for the project (chosen) | Migration `00071_visibility.sql` | The cascade is one `UPDATE`; the index reads a column, not the document; every writer (API, configuration as code, journal replay, move) is held to the rule by the database | The repository document maps two columns to one member |
| (b) Members of the repository's JSON document | As `hideExistence` | No mapping | The rule "never above the project" needs a lock and a JSON read per writer; the cascade rewrites documents |

**Choice:** (a). The repository document shows them as `visibility` (R-REPO-004); the trigger raises `AI008`.

**Rationale:** the guard of Q-AUTH-16 and the project guards of migration 00019 show that trove puts cross-row rules into the
database when several writers exist. The development agent records no revisit condition.

**Applied in the spec:** R-AUTHZ-033 (data model).

### Q-AUTHZ-277 · A project is seen by whom its level admits, and its members stay with its members

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Listed and readable for its audience; the members list stays with members and `projects:read` (chosen) | `GET /api/v1/projects` lists visible projects without a role; `membership=true` lists only the caller's | An internal user who is a member of nothing still finds the internal projects whose content it reads; no user enumeration for the whole organization | A non-member does not see who maintains a project |
| (b) As GitLab: the members of a visible project are visible | The members list for every caller who sees the project | Transparent | Every internal user, and on a public project everyone, enumerates users and groups (H-392 in a wider form) |
| (c) Projects only for members | As before | Nothing new | The start page is empty for an internal user who reads half the installation |

**Choice:** (a).

**Rationale:** what a caller reads it should be able to navigate to, so the project of a readable repository is visible, which
R-AUTHZ-016 already says for a repository read by a grant. The members list names people and groups; nothing in the levels needs
it. The development agent would revisit this if the owner wants GitLab's member pages for internal projects.

**Applied in the spec:** R-AUTHZ-035 rule 8; [api-and-ui.md](api-and-ui.md) R-API-047, R-UI-041.

### Q-AUTHZ-278 · A public remote repository is filled by anonymous callers, and the write that opens it warns

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The populate check as for a grant; warning `public_populates_remote` on the repository write (chosen) | R-AUTHZ-008 | The pull-through proxy behaves the same for every reader (Q-AUTHZ-4); the decision is made visible where it is made | Anyone can make trove fetch upstream content |
| (b) Never populate for the anonymous subject | Cache only | No upstream traffic caused from outside | Inconsistent 404s for a public proxy, the reason Q-AUTHZ-4 rejected (c) |

**Choice:** (a); the warning `anonymous_populates_remote` of the policy API goes with the policy entries for `anonymous`.

**Rationale:** Q-AUTHZ-4 decided that a remote repository behaves alike for every reader and that frozen mirrors use
`cacheFillAction: deploy`; the levels change who reads, not that decision. The development agent would revisit this with Q-AUTHZ-4.

**Applied in the spec:** R-AUTHZ-008, R-AUTHZ-035 rule 6, Security notes.

### Q-AUTHZ-279 · A policy entry, a binding, a membership or an owner record that names `anonymous` or `authenticated` is refused

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) 422 `principal_not_allowed` everywhere, the detail naming the levels; the row of `authenticated` deleted, the row of `anonymous` kept as the actor and refused by every granting table (chosen) | One answer for every write path, the database included (`AI009`) | A client that still writes such an entry learns what replaced it; no grant can reopen a closed repository | The old reason codes `anonymous_write_action`, `anonymous_dynamic_selector` and `authenticated_write_action` disappear |
| (b) `unknown_principal` | As for a name that does not exist | Nothing to explain | Hides that the concept moved |

**Choice:** (a). Migration `00073_principal_builtins.sql` deletes the entries that name either, deletes the row and the kind
`authenticated`, and adds the guard.

**Rationale:** the refusal must hold on every path (API, configuration as code, journal replay, `trove migrate` role bindings),
which the database guard makes certain, as for the project rules of Q-AUTHZ-203. The development agent records no revisit
condition.

**Applied in the spec:** R-AUTHZ-002, R-AUTHZ-003, R-AUTHZ-019, R-AUTHZ-021, R-AUTHZ-025, R-AUTHZ-026;
[identity-and-authentication.md](identity-and-authentication.md) R-AUTH-016.

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

### H-392 · A project administrator can enumerate user and group names

An Owner of a project looks up users and groups by a name prefix of two characters or more, 20
results per call, kind and name only (Q-AUTHZ-182), to name its members and the principals of its policies. Repeated calls list every active user and group name of the
installation, which is also what an `unknown_principal` answer on a policy write reveals. The general rate limit
(R-API-016) bounds the speed, the 2-character minimum and the limit of 20 the width of one call, and nothing but kind and
name is returned. Names of people stay visible to every Owner of the installation; a deployment where
that is unacceptable names few Owners when it creates a project.

### H-393 · A project administrator with `manageRepositories` probes the addresses the outbound rules allow

This is [H-282](../security/egress.md#h-282--the-upstream-test-tells-a-holder-of-repositorieswrite-config-what-it-sees-of-an-address) for a second kind of caller. An Owner of a project creates remote repositories (Q-AUTHZ-173) and tests saved and unsaved upstream configurations
(`POST /repositories/{key}/upstream-test`, `POST /upstream-tests`). The result names why a probe failed (`dns_error`,
`connection_refused`, `timeout`, `egress_denied`, …), so the caller learns, one probe at a time, which hosts and ports
the installation's network reaches among those the address rules allow. The rules ([security/egress.md](../security/egress.md))
apply to every remote, who created it; they are checked at connect time, and what they allow is reachable and measurable
by a delegate. An installation that does not want this narrows the allow list or names few Owners when it creates a project.

### H-394 · Whoever controls a project administrator group administers the project

A group that is a member of a project is filled by the identity provider (R-AUTH-008) or by an administrator of trove. A person who can add
a member to that group at the provider holds the group's role in the project, the role Owner included, with no event in trove's audit
log beyond the membership change that the next login writes (`group.members.changed`). This is the same position as for a
group bound to a system role (R-AUTHZ-010); the role bounds what it can do.

### H-398 · A failed project delete can leave the project without some of its policies

A project is deleted by deleting its policies one by one through the policy store, each in a transaction of its own, and then
the project (Q-AUTHZ-192). Everything a client can cause is refused before the first policy deletion (an unknown project, a
stale `If-Match`, a repository or service account that remains). A crash of the replica, or a repository assigned in the
milliseconds between the policy deletions and the closing transaction, leaves the project with fewer policies than it had; every
deletion is audited and journaled as any policy deletion, and nothing grants. The delete answers 409 or 5xx and can be
repeated; the policies are not restored.

### H-399 · A global service account and a project key created at the same instant can overlap

The creation of projects is serialised (Q-AUTHZ-194), and the creation of a global service account checks the project keys it
reads. The two checks do not share a lock: a project `web` and a global service account `web-bot` created in the same instant
can both succeed. The name is then a global account that starts with a project's prefix, which the administrator of `web` could
mistake for its own; the account has no project grant, and nothing is granted by the name. The overlap is found by
`ProjectGlobalAccountsWithPrefix` the next time a project key is created and by the listing of global service accounts.

### H-405 · A global policy that predates a project keeps the project's prefix

A new global policy takes no name that starts with a project key and a hyphen (Q-AUTHZ-200), and a project key is not checked against
the names of global policies when the project is created. A global policy `web-readers` that exists before the project `web`
therefore stays, and a delegate of `web` that creates `web-readers` meets 409 `already_exists`, which tells it a global policy of that
name exists. The same holds for a global policy and a project created in the same instant (no lock covers both). Live only for an
installation that has global policies named like a project it creates later; what makes it real is a delegate that guesses names.
Nothing is granted by the name, and the policy stays writable by the holders of `policies:write`. Renaming is not possible (the name
is immutable); the remedy is to create the policy again under another name.

### H-406 · The audit event of a refused project administrator does not name the project

A refusal by the narrowing of a project's role (a project the caller is no member of, a role too low for the operation, a repository of another
project) is recorded as `authz.denied` of the operation's capability (Q-AUTHZ-201): the event carries the permission, the reason and
the credential, not the project or the right asked for, so denials of probing across projects can be counted per caller but not
filtered per project. Two handlers answer a foreign service account on their own before the shared refusal (`groups_api.go`,
`tokens_api.go`: the account of no project or of one the caller does not see answers 404) and record nothing. Live for every
delegated refusal of those kinds; what makes it real is a delegate that probes account names across projects.

