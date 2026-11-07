# Projects, members and roles

Projects hold every repository and are the unit in which a team administers its repositories, policies, service accounts and CI
mappings (spec [R-AUTHZ-026](../spec/authorization.md#r-authz-026--projects-for-delegated-administration), the owner's decisions
Q-AUTHZ-206 to Q-AUTHZ-209, the development agent's Q-AUTHZ-214 to Q-AUTHZ-217 and Q-REPO-217,
[ADR-156](../decisions.md#adr-156--projects-own-their-repositories-members-hold-roles-names-are-scoped-to-the-project)). This page is
the contract of the authorization engine's and the project service's side; the management API's handlers and declarations are in
[http-api.md](http-api.md), the security view in [security/delegated-administration.md](../security/delegated-administration.md).

Status: built in the engine and the project service: the members with their roles and the role in the decision path, the selector
contexts, the stored names of project policies, the member writes with the last-Owner rule, the move of a repository. The rules of an Owner's
remote repositories are applied by the repository service to a `Restricted` caller, which the management API sets
(`writeCaller` in [repositories_api.go](../../internal/api/v1/repositories_api.go)).

## What has to be in your head

- **A member is a user or a group with one role.** `reader < developer < maintainer < owner` (`authz.ProjectRole`), each holding
  what the one before holds. A user's role in a project is the highest over its own row and its groups' rows. Service accounts, CI
  mappings, Bastion registrations, `anonymous`, `authenticated` and auto-join groups are never members.
- **A role is decided, never stored as a grant.** The index keeps the membership rows by principal and by project; a decision reads
  the subject's role in the repository's project only (`evalState.roleIn`, `projectInfo.roleOf`: a map lookup per user and group ID,
  no allocation; `newEvalState` notes once whether the subject is a member anywhere, `Index.isMember`), so neither a decision nor
  `ReadableOn`, which decides every key on one state, grows with the subject's memberships; `grantedBy` adds the source `role:<projectKey>:<role>` when the role's
  actions (`roleActions`: `read` and `manage` only on a virtual repository) hold the action. The token scope intersects it like a
  grant (`allowedAction`); content:metadata-read, ownership claims (`roleClaimers`) and listings (`readShape`) see it too.
- **The credential decides the administration.** `RoleOn`, `OwnsOn` and `MembershipsOn` answer the role in force for the credential:
  an Owner through a token without `admin: true` is a Maintainer there (`inForce`), so a declaration `projectRole: owner` refuses it.
  Content decisions give the Owner's and the Maintainer's actions alike. `MembershipsOn` and `RoleOn` answer a token only for the
  projects its scope reaches (`scopeReachesProject`: no scope, `inherit`, `admin: true`, the system item `projects:read`, or a
  repository element whose selector names the project, `selector.coversProject`), so every reader of the memberships (the project
  list, `projectRole: reader`, `hideProject`, `/authz/me`) narrows alike (Q-AUTHZ-215).
- **Two selector contexts.** A global policy, a token scope, the system CI tier and an exposure rule name `<project>/<key>`,
  `<projectGlob>/<keyGlob>[:format]` or a class (`@any…`); a project policy, a project CI tier, a virtual repository's members and
  deployment target a bare key, a bare key glob or a class. `authz.ValidateSelectorIn(text, project)` validates either; its error wraps
  `qname.ErrBareKeyInInstanceContext`, `qname.ErrQualifiedInProject` or `qname.ErrInvalidSelector`, so a surface answers its own
  code (policies `invalid_pattern`, token scopes `invalid_scope`, the CI exchange `scope_malformed`).
- **The stored name of a project's policy carries the project.** `PolicyInput.Name` and every `Get`, `Update`, `Delete` of the policy
  store take the stored name, `<projectKey>/<name>` (`authz.StoredPolicyName`), with `Project` set; a global policy has no `/`. The API
  shows the name within the project (`viewPolicy` in [project_policies_api.go](../../internal/api/v1/project_policies_api.go)).
- **The last Owner.** The member writes take the project's row lock and refuse to take `owner` from the last membership with it
  unless the caller holds `projects:write` (the handler passes `bypassLastOwner`).
- **A move refuses, it never rewrites.** Anything of the source project that names the repository refuses it with
  `repository_in_use` and the list of references; for a restricted caller (an Owner without `repositories:write-config`, the
  handler passes `restricted`) a key selector of a global policy as well (`global_policy`, Q-REPO-232).

## The contracts

### Types and methods

| Where | What |
|---|---|
| [internal/authz/project_role.go](../../internal/authz/project_role.go) | `ProjectRole` (`ProjectRoleNone`, `…Reader`, `…Developer`, `…Maintainer`, `…Owner`), `ParseProjectRole`, `Actions`, `AtLeast`, `Membership{Key, Role}` (JSON `key`, `role`) |
| [internal/authz/project.go](../../internal/authz/project.go) | `Engine.MembershipsOn(idx, sub) []Membership`, `Engine.RoleOn(idx, sub, project) ProjectRole`, `Engine.OwnsOn(idx, sub, project) bool`, `Index.ProjectOf`; the first three are on `authz.Decider` and `edge.Access` (`Memberships`, `Owns`); `Me.Memberships` is filled by `Effective` and `GET /authz/me` shows it as `projects` |
| [internal/authz/index.go](../../internal/authz/index.go), [source.go](../../internal/authz/source.go) | `Snapshot.Projects`, `Snapshot.ProjectMembers`; `projectInfo{members, sources}`, `Index.memberships` (by principal), `repoInfo.proj`; the build skips a member row of another kind than user or group and an auto-join group |
| [internal/authz/selector.go](../../internal/authz/selector.go) | `ValidateSelector` (instance), `ValidateSelectorIn(text, project)`, `SelectorProblemCode`; `selector.text` and `instanceText` (the CI exchange's items in the grammar of an instance context) |
| [internal/authz/policy.go](../../internal/authz/policy.go), [store.go](../../internal/authz/store.go), [validate.go](../../internal/authz/validate.go) | `StoredPolicyName`; the policy store by stored name; `AsProjectOwner(project)` (an Owner without a policies capability: another project's service account answers `unknown_principal`; a global policy 403, another project's 404 under the lock); `ExpectProject`; `ListFilter.ProjectOwner` |
| [internal/project](../../internal/project/service.go) | `Service`: `List`, `Get`, `CreateIn` (with `Input.Owners`, the first Owners), `Replace`, `Delete`, `Members`, `AddMember`, `SetRole`, `RemoveMember`, `MoveRepository`, `LookupPrincipals`; `Member`, `Role` (= `authz.ProjectRole`), `Reference{Kind, Name}` and the kinds `RefVirtualMember`, `RefPinRule`, `RefVirtualDeploymentTarget`, `RefPolicy`, `RefCIMapping`, `RefMember`, `RefDeploymentTarget`, `RefGlobalPolicy` |

The functions the management API calls, with their signatures:

```go
// The caller's projects and roles (GET /authz/me, GET /projects, the declarations).
func (e *authz.Engine) MembershipsOn(idx *authz.Index, sub authz.Subject) []authz.Membership
func (e *authz.Engine) RoleOn(idx *authz.Index, sub authz.Subject, project string) authz.ProjectRole
func (e *authz.Engine) OwnsOn(idx *authz.Index, sub authz.Subject, project string) bool

// The members (PUT, DELETE /projects/{projectKey}/members/{principalRef}, GET …/members).
Members(ctx context.Context, key string) ([]project.Member, error)
AddMember(ctx context.Context, actor authz.Subject, key, ref string, role project.Role, bypassLastOwner bool) (project.Member, bool, int64, error)
SetRole(ctx context.Context, actor authz.Subject, key, ref string, role project.Role, bypassLastOwner bool) (project.Member, int64, error)
RemoveMember(ctx context.Context, actor authz.Subject, key, ref string, bypassLastOwner bool) (int64, error)

// The move (POST /projects/{projectKey}/repositories/{repoKey}/move); the caller decided the permission first.
MoveRepository(ctx context.Context, actor authz.Subject, from qname.Repo, toProject string, restricted bool) (qname.Repo, int64, error)
```

### Error codes

All in [codes.go](../../internal/errs/codes.go).

| Code | Status | Where |
|---|---|---|
| `principal_not_allowed` | 422 field at `/principalRef` | a member that is not a user or a group, an auto-join group, a Bastion registration |
| `last_owner` | 409 | removing or demoting the last membership with the role `owner` without `projects:write` |
| `repository_in_use` | 409, member `references: [{kind, name}]` | a move while something of the source project names the repository, or a virtual repository with members or a deployment target; a restricted move while a key selector of a global policy names it (`global_policy`) |
| `already_exists` | 409 | a project key that exists; a move into a project that holds the key (also by a repository being deleted); a policy name that exists in its project or instance-wide |
| `project_not_empty` | 409 | delete of a project with repositories (any lifecycle), service accounts or CI mappings of its tier |
| `unknown_repository` | 422 field | a key selector that names no repository (in a project policy: of the project) |
| `invalid_pattern` | 422 field | a selector of the wrong context (a bare key in a global policy or an exposure rule, a `/` in a project policy) or of no grammar |
| `unknown_project` | 422 field | a policy of a project that does not exist (also one deleted between the lookup and the insert) |
| `principal_outside_project` | 422 field | a project service account in a global policy or another project's policy; `anonymous`, `authenticated` or an auto-join group in a project policy |
| `invalid_principal` | 422 field at `/principalRef` | a member reference that does not parse |
| `invalid_role` | 422 field at `/role` | a role that is none of the four |

### Audit events

[events_projects.go](../../internal/audit/events_projects.go). All transactional, with the epoch in the details and the project in
`Event.Project`.

| Event | Journal (R-AUTHZ-031) | Details |
|---|---|---|
| `authz.project.created`, `authz.project.updated` | no | project, projectId, version, before, after |
| `authz.project.deleted` | project ID and key | as above, `policiesDeleted` (stored names) |
| `authz.project.member_added` | no | project, projectId, principal, principalId, role |
| `authz.project.member_changed` | project, principal, before, after | as above with before and after |
| `authz.project.member_removed` | project, principal | as above with role |
| `repository.moved` (project: the source, repository: the former name) | repositoryId, from and to by ID and key | repository (key), repositoryId, from, fromId, to, toId, configVersion |
| `repository.moved_in` (project: the target, repository: the new name; `movedEvents` in [move.go](../../internal/project/move.go), Q-REPO-233) | no | as `repository.moved` |

The display name and the description are not access-bearing, so `authz.project.updated` is not journaled.

### Database

Migration `00019_projects.sql` (and 00020 to 00025); queries [project.sql](../../internal/meta/postgres/queries/project.sql) and
[authz.sql](../../internal/meta/postgres/queries/authz.sql); the table list is in
[database.md](database.md#projects-tables-00019-to-00025). The member writes use `ProjectLock` (returns the ID), `ProjectPrincipalByRef`
(with `auto_join`), `ProjectMemberRole`, `ProjectOwnerCount`, `ProjectMemberAdd`, `ProjectMemberSetRole`, `ProjectMemberRemove`; the
move `ProjectRepositoryLock`, `ProjectRepositoryKeyTaken`, `ProjectRepositoryReferences`, `ProjectCIMappingGrants` (the inline grants
are JSON and checked in Go with `qname.ParseSelector` in the source project's context) and `ProjectRepositoryMove`. The index reads
`AuthzBuildProjects` and `AuthzBuildProjectMembers` (with `principal_auto_join`) in its one snapshot transaction.

The `Project` carries `LogicalBytes`, the size the project is charged (R-STOR-049, Q-STOR-155): `ProjectList` and `ProjectGet` sum the
folded `repository_usage` rows of the project's repositories in the same statement as the counts; the computation is described in
[blob-store.md](blob-store.md#the-figure-of-a-project-r-stor-049-q-stor-158). It is part of the representation, so it follows the read
rules of the project (a member of the project, `projects:read`), and no write moves the project's `version`.

### How a change reaches every replica

Every write notifies the change area `authz` in its transaction; a move also `repo_config` with the repository's ID, its qualified name
(op `moved`) and the new `config_version`, so the repository snapshot and its negative cache are rebuilt and every cache keyed by the
configuration version (`repository.ChangeEviction`) drops the old renditions. The answer carries the epoch the transaction produced
(`afterEpoch`); a write that changes nothing answers the current epoch.

## Tests

Tests are named after the requirement; the integration tier needs the stack (`make test-integration`).

| Test | Where | What it pins |
|---|---|---|
| `TestR_AUTHZ_026_EachRoleGrantsItsActionsOnTheProjectsRepositories`, `…_TheHighestRoleOverTheUserAndItsGroups`, `TestR_AUTHZ_013_ATokenScopeIntersectsTheRole`, `TestQ_AUTHZ_215_AMembershipReachesATokenWithinItsScope`, `TestQ_AUTHZ_215_OnlyUsersCarryARole`, `TestR_AUTHZ_026_AdministratorsAndAuditorsSeeEveryProject`, `TestR_AUTHZ_009_ARoleOnAVirtualRepository`, `TestR_API_029_AListingReadsWhatARoleReads`, `TestR_AUTHZ_018_ADeployThroughARoleClaimsForTheMember`, `TestR_AUTHZ_015_ARoleDecisionDoesNotGrowWithTheProjects`, `TestR_AUTHZ_026_ManyMembershipsMergeToTheHighestRole`, `BenchmarkRoleDecide`; `TestR_AUTHZ_015_ARoleDecisionDoesNotGrowWithTheMemberships`, `TestR_AUTHZ_015_AListingDerivesTheSubjectOnce`, `BenchmarkRoleDecideMemberships50`, `BenchmarkReadableOnMemberships50` ([role_cost_test.go](../../internal/authz/role_cost_test.go)) | [project_roles_test.go](../../internal/authz/project_roles_test.go) | the role in the decision path |
| `TestR_AUTHZ_026_ProjectPolicyIsScopedToItsProject`, `…_MatchCacheKeyIncludesTheProject`, `…_ProjectPolicyValidation`, `…_ListFilterByProject`, `…_TheBuildSkipsAnAutoJoinGroupAsAMember` | [project_test.go](../../internal/authz/project_test.go), [project_oracle_test.go](../../internal/authz/project_oracle_test.go) | selectors, stored names, the fail-closed build |
| `TestR_AUTHZ_026_MemberReferences`, `TestR_AUTHZ_031_MemberEventsCarryTheirJournalEntries`, `TestR_REPO_060_TheMovedEvent`, `…_ACIMappingNamesTheRepositoryByKey`, `…_RefusalsOfAMove` | [service_test.go](../../internal/project/service_test.go) | the service without a database |
| `TestR_AUTHZ_026_AnOwnerWritesThePoliciesOfItsProject`, `TestR_AUTHZ_022_AuthzMeListsTheMemberships`, `TestR_AUTHZ_026_RolesDecideOnEveryReplica`, `TestR_AUTHZ_028_AMemberChangeReachesAnotherReplica`, `TestR_REPO_060_AMoveTakesTheRepositoryOutOfItsProjectsRoles`, `TestR_AUTHZ_026_AnAutoJoinGroupGrantsNothingInAProject` | [test/integration/authz/projects_test.go](../../test/integration/authz/projects_test.go) | roles, policies and moves against the tables, on two replicas and over HTTP |
| `TestR_AUTHZ_026_Members`, `TestQ_AUTHZ_217_TheLastOwner`, `TestR_REPO_060_MoveARepository`, `TestR_AUTHZ_026_AMemberSeesItsOwnProjectsOnly`, `TestR_AUTHZ_032_EveryProjectWriteRaisesTheEpochAndAnswersIt`, `TestQ_AUTHZ_209_DeleteSparesThePoliciesOfTheSameNameElsewhere` | [test/integration/projects](../../test/integration/projects) | the project API |
| `TestProjectsMigration` | [test/integration/postgres/projects_test.go](../../test/integration/postgres/projects_test.go) | the schema and its guards |

Test harness note: after a write that changes a membership, send `Trove-Authz-Min-Epoch` with the epoch of the write when another
replica must see it.
