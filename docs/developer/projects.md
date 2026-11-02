# Projects and delegated administration

Projects are the unit of delegated administration (spec [R-AUTHZ-026](../spec/authorization.md#r-authz-026--projects-for-delegated-administration),
beta phase 5a): a team's administrators manage its repositories, its policies and the service accounts of its partners without
a system administrator. The page began as the contract between the work packages that built it and now describes what was built.
The decisions behind it are [Q-AUTHZ-174 to Q-AUTHZ-201](../decisions.md) with [ADR-145](../decisions.md#adr-145--the-grants-of-a-project-policy-are-stored-where-the-previous-release-does-not-read-them)
and [ADR-146](../decisions.md#adr-146--an-operation-admits-project-administrators-through-a-declaration-of-its-own).

Status: built. The engine answers the three project questions and compiles project policies scoped to their project; the project
service and its ten handlers are wired (`project.New` in [wire_authz.go](../../internal/app/wire_authz.go)); the repository,
service-account, token and upstream-test operations narrow to the project; the UI has the admin area and the project filter.
Open gaps are the `H-` items named in the sections below. Nothing answers 501 any more and no stub is left.

## What has to be in your head

- **A project administrator is a caller with a right on a project, not a capability.** The grant index answers
  "which projects does this subject administer, with which switches" (`Engine.ProjectsOn`). Users and groups only; sessions and
  tokens with `admin: true` only (Q-AUTHZ-183).
- **Three rights.** `any` (administration), `managePolicies`, `manageRepositories`. What each allows is the table of
  R-AUTHZ-026.
- **The shared step is coarse, the handler narrows.** An operation declares `{permission: <cap>, projectAdmin: <right>}`
  ([authorize.go](../../internal/api/v1/authorize.go)): the step admits the capability holder or a caller that administers at least one
  project, whatever the right (Q-AUTHZ-180, amended: a caller's answer must not depend on its other projects). The handler then calls `requireProject`, `requireProjectOfRepository` or `delegatedScope`
  ([project_scope.go](../../internal/api/v1/project_scope.go)). A project the caller does not administer answers 404; the
  addressed project's switch off answers 403 `project_switch_off`, from the narrowing helper of every delegated operation (a
  policy body naming a project the caller does not administer is the field error 422 `unknown_project`, as the table below says).
  A caller that administers no project is refused by the step itself with the capability's 403.
- **The access behind the API must forward the project questions.** [bridgeAccess](../../internal/app/access.go) sits in front of
  the engine's access; it implements `v1.ProjectAccess` and forwards `AdministeredProjects` and `AdministersRepository`. Without
  that the step sees no project and refuses every project administrator (`TestR_AUTHZ_026_TheBridgeForwardsTheProjectQuestions`,
  `TestR_AUTHZ_026_ProjectAdministratorOverHTTP`).
- **Project policies are scoped twice.** The write refuses selectors outside the project (422 `repository_outside_project`);
  the index covers only the project's repositories, whatever the stored selectors say (Q-AUTHZ-179). The `matchCache` of the
  builder must be keyed by selector and project.
- **A project policy's grants are in `authz_project_policy_entry`**, never in `authz_policy_entry` (the previous release reads
  only that, Q-AUTHZ-174). Every statement that reads or writes entries handles both tables; the policy has `project_id`.
- **A project service account is the project's.** No group, no role, no foreign policy, no package owner outside the project
  (Q-AUTHZ-175); its name starts with `<key>-` (Q-AUTHZ-176); `service_account.project_id` never changes.
- **Removals journal.** `authz.project.updated` (a switch off), `.deleted`, `.admin_removed` and `.repository_unassigned` carry
  the journal entry (R-AUTHZ-031); the replay is not built (no replay of any event is), the entry is.

## The contracts

### Types and methods

| Where | What |
|---|---|
| [internal/project](../../internal/project/service.go) | `Project`, `Input`, `Rights`, `PrincipalRef`, `PrincipalKind`, `KeyRE`; `Service` (List, Get, CreateIn, Replace, Delete, AddAdmin, RemoveAdmin, Assign, Unassign, LookupPrincipals), `Options`, `New`; the bodies in `crud.go`, `members.go`, `store.go`. Layer: domain ([layers.go](../../internal/archtest/layers.go)) |
| [internal/authz/project.go](../../internal/authz/project.go) | `ProjectRight` (`ProjectAny`, `ProjectManagePolicies`, `ProjectManageRepositories`), `AdministeredProject{Key, ManagePolicies, ManageRepositories}`, `Engine.ProjectsOn`, `Engine.AdministersOn`, `Engine.AdministersRepositoryOn`, `Index.ProjectOf`; the same three on `authz.Decider` and on `edge.Access` (`Projects`, `Administers`, `AdministersRepository`) |
| [internal/authz/roles.go](../../internal/authz/roles.go) | `Me.Projects`, filled by `Effective` from `ProjectsOn`; `GET /authz/me` emits it |
| [internal/authz/policy.go](../../internal/authz/policy.go) | `PolicyInput.Project *string` (JSON `project`, `null` for a global policy); the project is fixed at creation (Q-AUTHZ-186) and validated in `resolve` (existence, `<key>-` name, selectors, entries) |
| [internal/authz/index.go](../../internal/authz/index.go), [source.go](../../internal/authz/source.go) | `Snapshot.Projects`, `Snapshot.ProjectAdmins` read by `pgSource.Snapshot` in its one transaction; `BuildIndex` compiles a project policy against its project's repositories only, keys `matchCache` by selector and project and skips the rows a project service account or an auto-join group may not hold (Q-AUTHZ-198, Q-AUTHZ-200). It adds no grant for the administrators |
| [internal/repository](../../internal/repository/project.go) | `Repository.ProjectID`, `Repository.ProjectKey` (filled by every reader); `Write{Project, Delegate}`; `Service.CreateIn`, `CreateTxIn`, `ReplaceIn`, `PatchIn`, `DeleteIn`, `DeleteTxIn` carry it; the project of the locked row is checked again in the write transaction by each of them; `ListFilter.Project` |
| [internal/auth/principal/serviceaccounts.go](../../internal/auth/principal/serviceaccounts.go) | `ServiceAccount.Project`, `NewServiceAccount.Project`; `bindProject` locks the project `FOR SHARE` in the creating transaction; `ListServiceAccounts(ctx, query, project, after, limit)` takes the project filter |
| [internal/auth/token](../../internal/auth/token/service.go) | `CreateRequest.Delegated`: a delegated request keeps `Admin` false, refuses `admin: true` and a `system` scope item (422 `invalid_scope`) and takes the SAT maximum lifetime (Q-AUTH-190); `token.created` and `token.revoked` carry `project` |
| [internal/meta/postgres/sqlstate.go](../../internal/meta/postgres/sqlstate.go) | `IsState(err, code)`: the domain packages turn the `AI001` to `AI004` refusals of the integrity triggers into API answers without the driver |
| [internal/cluster/locks.go](../../internal/cluster/locks.go) | `ConfigProjects`: the transaction-scoped lock every project create takes before the overlap rule (Q-AUTHZ-194) |
| [internal/api/v1/authorize.go](../../internal/api/v1/authorize.go), [access.go](../../internal/api/v1/access.go), [project_scope.go](../../internal/api/v1/project_scope.go) | `Requirement.ProjectAdmin`; `ProjectAccess` (`AdministeredProjects`, `AdministersRepository`); `DelegatedScope`, `delegatedScope`, `requireProject`, `requireProjectOfRepository`, `refuseProject` |
| [internal/api/v1/projects_api.go](../../internal/api/v1/projects_api.go) | the ten handlers; `AuthzServices.Projects`; `projects()` answers 503 without a service |

The engine rule for the three questions: a subject administers a project when its user ID or one of its group IDs is a row of
`project_admin` for that project, and the credential may exercise it (a session, or a token with `admin: true`); a service
account, a CI identity and anonymous never do. `AdministersOn` and `AdministersRepositoryOn` answer false for an unknown
project and for one the subject does not administer alike. Project administrators also hold `manage` on every repository of the
project, unrestricted in packages and paths. It is decided from that same rule, not from a grant: `grantedBy` adds the source
`project:<key>` for `manage` when `Index.administersRepo` holds, so a service account in an administrator group and a token without
`admin: true` hold none, and every consumer (decision, ownership, cache invalidation, claims, the virtual shadow check, `Effective`)
sees exactly the administrators of `ProjectsOn` (Q-AUTHZ-196). The index holds no grant per project repository and administrator.

What the project service does beyond its interface:

- `AddAdmin` and `RemoveAdmin` resolve the reference in the write's transaction with `ProjectPrincipalByRef`, not from the principal
  store's cache; a no-op (an administrator that already is one, an assignment to the project the repository already belongs to)
  records nothing, notifies nothing and answers the current epoch (Q-AUTHZ-193).
- `Assign` compares the owner the caller expects (`from`: a key, empty for none, absent for no expectation) with the one it reads
  after `lockProjects`, before the target is loaded and before the no-op check (`expectOwner`): 409 `project_owner_changed` with
  `currentProject`, nothing written (Q-AUTHZ-205). The handler refuses a `from` that is not empty and not a project key with 400.
- `Assign` and `Unassign` write `repositories.project_id` and, in the same statement (`ProjectAssignRepository`), move
  `config_version`, so the document's ETag changes; they notify `authz` and `repo_config` (Q-AUTHZ-191).
- `Delete` is not one transaction: it refuses everything a client can cause first, deletes the policies through the policy store, then
  one closing transaction locks, counts again and deletes the project (Q-AUTHZ-192; gap
  [H-398](../spec/authorization.md#h-398--a-failed-project-delete-can-leave-the-project-without-some-of-its-policies)).
  Each policy deletion carries the version `ProjectPolicyNames` read as its `If-Match` (Q-AUTHZ-204): a `412` sends the round
  again (three rounds at most, then 409), a `404` is skipped; `authz.ExpectProject` makes the policy store check the project
  under the policy lock, so a policy that took the name in between is never removed (Q-AUTHZ-199).
  `Options.Policies` is the interface `PolicyDeleter` (`*authz.Service` in the composition), so a test can act between two steps.
- `CreateIn` takes `cluster.ConfigProjects` before the overlap rule (Q-AUTHZ-194; gap
  [H-399](../spec/authorization.md#h-399--a-global-service-account-and-a-project-key-created-at-the-same-instant-can-overlap)).

### API operations

The paths are in [api/openapi.yaml](../../api/openapi.yaml) (`# >>> M5a projects`); the generated server interface is
`gen.ServerInterface`, the TypeScript types are `web/src/app/api/generated/schema.d.ts`.

| Operation | Declaration | Handler narrowing | Notes |
|---|---|---|---|
| `listProjects` `GET /projects` | `projects:read` + `projectAdmin: any` | a caller without the capability sees its administered projects | cursor paging by key; `q`, `sort` |
| `createProject` `POST /projects` | `projects:write` | — | idempotent (`s.idem.Do`); 409 `already_exists`, `project_key_overlap`; epoch |
| `getProject` `GET /projects/{key}` | `projects:read` + `projectAdmin: any` | `delegatedScope.Sees(key)` else 404 | `ETag` (the PUT version) for `If-Match`; `If-None-Match` is ignored, no 304 (Q-AUTHZ-202) |
| `replaceProject` `PUT /projects/{key}` | `projects:write`, recent auth, `x-trove-if-match: required` | — | 412, 428 from the binder; epoch; `authz.project.updated` |
| `deleteProject` `DELETE /projects/{key}` | `projects:write`, recent auth | — | `If-Match` optional; 409 `project_not_empty`; epoch |
| `addProjectAdmin` `PUT /projects/{key}/admins/{principalRef}` | `projects:write`, recent auth | — | 201 or 200; exempt from `If-Match` (ifmatch_test.go) |
| `removeProjectAdmin` `DELETE …` | `projects:write`, recent auth | — | 204; 404 |
| `assignProjectRepository` `PUT /projects/{key}/repositories/{repoKey}` | `repositories:write-config`, recent auth | — | `?move=true`, `?from=<key or empty>`; 409 `repository_assigned`, 409 `project_owner_changed` (extension `currentProject`); exempt from `If-Match` |
| `unassignProjectRepository` `DELETE …` | `repositories:write-config`, recent auth | — | 204; 404 |
| `lookupProjectPrincipals` `GET /projects/{key}/principals` | `identities:read` + `projectAdmin: managePolicies` | `requireProject(…, ProjectManagePolicies)` | `q` ≥ 2 characters, at most 20, kind and name only |

Operations of other pages that gain a project path:

| Operation | Declaration | The project administrator's case |
|---|---|---|
| `listPermissionPolicies`, `getPermissionPolicy` | `policies:read` + `projectAdmin: any` | only project policies; without the capability `project=<key>` is required on the list (403 without it), 404 for another project; an unknown principal or repository filter is an empty list, `expandGroups=true` is 403 (Q-AUTHZ-197) |
| `createPermissionPolicy`, `replacePermissionPolicy`, `patchPermissionPolicy`, `deletePermissionPolicy` | `policies:write` + `projectAdmin: managePolicies` | the policy's project must be one the caller administers with `managePolicies`; a global policy is 403 |
| `validatePermissionPolicy` | `policies:read` + `projectAdmin: managePolicies` | a policy with a project of its own |
| `createRepository`, `replaceRepository`, `updateRepository`, `deleteRepository`, `testRepositoryUpstream`, `testUpstream` | `repositories:write-config` + `projectAdmin: manageRepositories` | create: `project` in the body is required and the key starts with `<key>-`; the others: `requireProjectOfRepository(…, ProjectManageRepositories)`; `testUpstream` (no repository) needs any project with the switch; with every administered switch off the answer is `project_switch_off` |
| `listRepositories`, `getRepository` | public (the handler decides) | the project's repositories with the full document |
| `listServiceAccounts`, `getServiceAccount` | `identities:read` + `projectAdmin: any` | the project's accounts; `project=<key>` required on the list |
| `createServiceAccount`, `enableServiceAccount` | `identities:write` + `projectAdmin: managePolicies` | `project` in the body (create); the account's project (enable) |
| `updateServiceAccount`, `disableServiceAccount`, `deleteServiceAccount` | `identities:write` + `projectAdmin: any` | the account's project; never blocked by a switch |
| `createServiceAccountToken` | `authenticated` (the handler decides: administrator, the account itself, or the project administrator through `delegatedTokenAccount`) | the account's project with `managePolicies` (`project_switch_off` while it is off); the token rules of Q-AUTHZ-177, and never `selfRotatable` (Q-AUTH-192) |
| `listTokens`, `getToken`, `revokeToken` | `authenticated` | tokens of the project's accounts (`seesProject`, `visible`); `owner=sa:<name>` of an account the caller does not administer is the caller's own list (R-AUTH-028) |
| `revokeServiceAccountTokens` | `tokens:revoke` + `projectAdmin: any` | the account's project |
| `getAuthzMe` | `authenticated` | `projects` |

The contract tests that see a declaration: `TestR_API_007_DocumentDeclaresRecentAuth` (the table in
[recent_auth_test.go](../../internal/api/v1/recent_auth_test.go) lists the six project operations),
`TestR_API_012_OneMechanismForAMissingIfMatch` (the two exemptions in [ifmatch_test.go](../../internal/api/v1/ifmatch_test.go)),
and the document tests, which load every declaration through `parseRequirement`.

### Error codes

All in [codes.go](../../internal/errs/codes.go) (`>>> M5a projects`). `not_implemented` was the stubs' code and is gone with them.

| Code | Status | Where |
|---|---|---|
| `repository_outside_project` | 422 field | a project policy key selector; a virtual member or deploy target of a project administrator's write; unknown key answers the same |
| `unknown_project` | 422 field | a policy names a project that does not exist or one the caller does not administer |
| `principal_outside_project` | 422 field | a project service account in a global policy, another project's policy (to a project administrator `unknown_principal`), or as a package owner outside its project; an auto-join group in a project policy |
| `project_prefix_required` | 422 | a repository, policy or service account name without `<key>-` |
| `name_reserved_by_project` | 422 | a global service account or a new global policy named `<key>-…` of an existing project |
| `project_account_not_groupable` | 422 | a project service account as a group member |
| `project_not_empty` | 409 | delete of a project with repositories (any lifecycle) or service accounts |
| `repository_assigned` | 409 | assign without `move=true` |
| `project_owner_changed` | 409 | assign with `?from=` naming another owner than the one under the locks (Q-AUTHZ-205); `currentProject` holds the current owner or null |
| `project_key_overlap` | 409 | a key that is another's dash-prefix, or the prefix of a global service account name |
| `project_switch_off` | 403 | the addressed project's switch is off; answered by the handler's narrowing helper whatever other projects the caller administers |
| `secret_value_required` | 422 | a project administrator's remote names a reference of the installation (`secretRef`), or keeps a stored credential while `url`, a credential-receiving extra host or `forwardAuthOnRedirect` changes ([Q-REPO-127](../spec/repositories-and-proxy.md#q-repo-127--a-project-administrator-gives-an-upstream-secret-as-a-value-and-a-stored-one-stays-with-its-hosts); `checkDelegatedRemote` in [delegated.go](../../internal/repository/delegated.go)) |
| `outbound_path_restricted` | 422 | a project administrator's remote sets `outboundProxy.mode` other than `default` or a `sourceNetwork` that an administrator did not set ([Q-REPO-128](../spec/repositories-and-proxy.md#q-repo-128--a-project-administrators-remote-repository-takes-the-installations-outbound-path)) |

### Audit events

[events_projects.go](../../internal/audit/events_projects.go). All transactional, category `authz`, with the epoch in the details.
Flagged `journal: revocation` (the producer must supply the entry, R-AUTHZ-031): `authz.project.updated`, `.deleted`,
`.admin_removed`, `.repository_unassigned`. Not journaled: `.created`, `.admin_added`, `.repository_assigned`. A move records
`.repository_unassigned` and `.repository_assigned` in one transaction. Service accounts and tokens use the identity events;
their schemas carry the member `project` ([events_identity.go](../../internal/audit/events_identity.go)).

### Database

Migration `00019_projects.sql`, queries `project.sql`, `authz.sql` (extended), `identity.sql` and `repository.sql`
(`InsertRepository` takes `project_id`); the table list is in [database.md](database.md#projects-tables-00019-to-00025). New statements
for the packages: `ProjectList`, `ProjectGet`, `ProjectLock`, `ProjectInsert`, `ProjectUpdate`, `ProjectDelete`, `ProjectCountMembers`,
`ProjectPolicyNames`, `ProjectAdmins`, `ProjectAdminsOfAll`, `ProjectAddAdmin`, `ProjectRemoveAdmin`, `ProjectOfRepository`,
`ProjectAssignRepository`, `ProjectRepositoryKeys`, `ProjectServiceAccountNames`, `ProjectKeysOverlapping`, `ProjectKeysPrefixing`,
`ProjectGlobalAccountsWithPrefix`, `ProjectLookupPrincipals`, `AuthzBuildProjects`,
`AuthzBuildProjectAdmins`, `AuthzInsertProjectEntry`, `IdentityInsertProjectServiceAccount`.

## Who built what

Phase 5a was built by four parallel work packages on the contracts of `e72969d`; the order was the dependency order below. All are done.

| Work package | Commit | Files |
|---|---|---|
| contracts | `e72969d` | the migration, `internal/project` types and interface, `internal/authz/project.go` stubs, `authorize.go`, `access.go`, `project_scope.go`, `codes.go`, `events_projects.go`, the document |
| **WP-A authz engine** | `c58fb18` | `internal/authz/**`, `queries/authz.sql`, `internal/api/v1/authz_api.go`, `test/integration/authz/**` |
| **WP-B projects** | `dc809aa` | `internal/project/**`, `projects_api.go`, `queries/project.sql`, `wire_authz.go`, `test/integration/projects/**` |
| **WP-C delegated writes** | `43f9175` | `internal/repository/**`, `internal/auth/**`, `repositories_api.go`, `groups_api.go`, `tokens_api.go`, `upstream_api.go`, `test/integration/{transfer,identity}/**` |
| **WP-D UI** | `facd7f6` | `web/**`, `docs/developer/web-ui.md`, `docs/manual/**` |

## Tests

Tests named after the requirement (`TestR_AUTHZ_026_…`); the integration tier needs the stack (`make test-integration`).

| Test | Where | What it pins |
|---|---|---|
| `TestR_AUTHZ_026_TheDeclarationOfAProjectAdministrator`, `…_TheHandlersNarrowToTheProject`, `…_TheProjectOperationsAnswer503WithoutAService` | [project_scope_test.go](../../internal/api/v1/project_scope_test.go) | the step admits the capability and any administrator, refuses the rest; the helpers: 404, `project_switch_off` |
| `TestR_AUTHZ_026_RepositoryOperationsOfAProjectAdministrator`, `…_UpstreamTestsOfAProjectAdministrator`, `…_ServiceAccountOperationsOfAProjectAdministrator`, `…_ProjectMemberOfARepositoryBody` | [project_delegation_test.go](../../internal/api/v1/project_delegation_test.go) | the narrowing of each delegated operation |
| `TestR_API_047_ListProjectsNarrowsToTheCaller`, `TestR_AUTHZ_026_GetProjectIsNotFoundForAnotherProject` | [projects_api_test.go](../../internal/api/v1/projects_api_test.go) | the list and read handlers |
| `TestR_AUTHZ_026_TheBridgeForwardsTheProjectQuestions` | [access_test.go](../../internal/app/access_test.go) | the bridge forwards `ProjectAccess` |
| `TestR_AUTHZ_026_ManageOfAProjectAdministratorFollowsTheCredential` ([project_manage_test.go](../../internal/authz/project_manage_test.go)), `…_TheBuildSkipsAProjectAccountOutsideItsProject`, `…_TheBuildSkipsAnAutoJoinGroupOfAProjectPolicy`, `…_ProjectAdministratorFiltersTellNothingAboutOtherObjects`, `…_AnotherProjectsAccountAnswersAsAnUnknownPrincipalToADelegate`, `…_AGlobalPolicyDoesNotTakeAProjectsPrefix`, `…_AProjectPolicyNamesNoAutoJoinGroup` | [project_account_test.go](../../internal/authz/project_account_test.go), [project_oracle_test.go](../../internal/authz/project_oracle_test.go) | the manage rule, the fail-closed build, the oracles of the list and the validation, the reserved prefix |
| `TestR_AUTHZ_029_AProjectAdministratorsRefusalIsAudited`, `TestR_AUTHZ_026_ThePolicyListOfAProjectAdministratorDoesNotExpandGroups` | [project_scope_audit_test.go](../../internal/api/v1/project_scope_audit_test.go) | the refusal of a narrowing records `authz.denied`; the filter of a delegate |
| `TestR_AUTHZ_026_ManageOfAProjectAdministratorThroughAGroupFollowsTheCredential`, `…_ADelegatedWriteChecksTheProjectUnderTheLock`, `…_ACreateRacingAProjectDeleteIsUnknownProject`, `…_AGlobalPolicyDoesNotTakeAProjectsPrefixAgainstTheTables`, `…_AProjectPolicyNamesNoAutoJoinGroupAgainstTheTables`, `…_ADeclaredRoleOfAProjectServiceAccountIsRefused`, `…_ProjectAdministratorFiltersAndRefusalsOverHTTP` | [test/integration/authz](../../test/integration/authz/projects_fix_test.go) | the same rules against the tables and over HTTP |
| `TestR_AUTHZ_026_ProjectsOnAnswers…`, `…_ProjectAdministratorsHoldManageOnTheirRepositories`, `…_ProjectPolicyIsScopedToItsProject`, `…_MatchCacheKeyIncludesTheProject`, `…_ProjectPolicyValidation`, `…_ProjectServiceAccountTakesNoRole`, `…_ListFilterByProject` | [project_test.go](../../internal/authz/project_test.go) | the engine |
| `TestR_AUTHZ_026_EmptyProject`, `TestR_AUTHZ_031_ProjectEventsCarryTheirJournalEntries` | [service_test.go](../../internal/project/service_test.go) | the service |
| `TestR_AUTHZ_026_ProjectAdministratorWritesPolicies`, `…_ProjectSwitchOffRefusesPolicyWrites`, `…_ProjectPolicyIsInvisibleToTheOldBuild`, `…_ProjectServiceAccountIsGrantedByItsProjectOnly`, `…_AuthzMeListsAdministeredProjects`, `…_PolicyOfARepositoryThatLeftTheProjectWarns`, `TestR_AUTHZ_028_ProjectAdministratorRemovalIsRefusedOnAnotherReplica` | [test/integration/authz/projects_test.go](../../test/integration/authz/projects_test.go) | policies of a project over HTTP and on replicas |
| `TestR_AUTHZ_026_ProjectLifecycleOverHTTP`, `…_ReadIgnoresIfNoneMatch`, `…_DeleteRetriesForNewPolicies`, `…_DeleteSparesAGlobalPolicyOfTheSameName` (the last two build a project service with a wrapped policy store), `…_Administrators`, `…_AssignAndUnassign`, `…_DeleteNeedsAnEmptyProject`, `…_PrincipalLookupBounds`, `…_SwitchesNeedTheCurrentIfMatch`, `…_AProjectAdministratorSeesItsOwnProjectsOnly`, `TestR_AUTHZ_032_EveryProjectWriteRaisesTheEpochAndAnswersIt` | [test/integration/projects](../../test/integration/projects) | the project API; the last-but-one: an administrator without `projects:read` lists exactly its projects and gets 404 on another's |
| `TestR_AUTHZ_026_RepositoryProjectMember`, `…_ProjectAdministratorRepositoryWrites` (including the `DeleteIn` recheck), `…_ProjectAdministratorOverHTTP` | [test/integration/transfer/projects_test.go](../../test/integration/transfer/projects_test.go) | repositories of a project, service level and over HTTP; the replica `off` is asked before the switch is turned on, because later writes refresh its index |
| `TestR_AUTH_020_ProjectServiceAccountsOfTheDomain`, `TestR_AUTHZ_026_ProjectServiceAccountsOverHTTP`, `…_ProjectAdministratorServiceAccountsAndTokens` | [test/integration/identity/projects_test.go](../../test/integration/identity/projects_test.go) | project accounts, the tokens of a project administrator, and who sees which token of another owner |
| `TestProjectsMigration`, `TestTableRegistry`, `TestRevisionColumns` | [test/integration/postgres](../../test/integration/postgres) | the schema |
| `TestR_API_007_DocumentDeclaresRecentAuth`, `TestR_API_012_…`, `TestR_API_002_EveryPermissionOperationAnswersAsDocumented` | [recent_auth_test.go](../../internal/api/v1/recent_auth_test.go), [ifmatch_test.go](../../internal/api/v1/ifmatch_test.go), [test/integration/authz/api_test.go](../../test/integration/authz/api_test.go) | the document |

Test harness note: the identity and transfer tests use tokens with `admin: true` (Q-AUTHZ-183). After a write that changes who administers a
project, send `Trove-Authz-Min-Epoch` with the epoch of the write when another replica must see it.
