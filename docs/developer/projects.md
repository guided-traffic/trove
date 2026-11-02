# Projects and delegated administration

Projects are the unit of delegated administration (spec [R-AUTHZ-026](../spec/authorization.md#r-authz-026--projects-for-delegated-administration),
beta phase 5a): a team's administrators manage its repositories, its policies and the service accounts of its partners without
a system administrator. This page is the contract between the work packages that build it. The decisions behind it are
[Q-AUTHZ-174 to Q-AUTHZ-185](../decisions.md) with [ADR-145](../decisions.md#adr-145--the-grants-of-a-project-policy-are-stored-where-the-previous-release-does-not-read-them)
and [ADR-146](../decisions.md#adr-146--an-operation-admits-project-administrators-through-a-declaration-of-its-own).

Status: the contracts compile and the schema, the queries, the document and the stubs exist. The engine answers "no project",
the project service and its handlers answer 501 `not_implemented`, and the other packages refuse the project forms they were
given (a policy with `project`, a service account with `project`) until their work package replaces the refusal.

## What has to be in your head

- **A project administrator is a caller with a right on a project, not a capability.** The grant index answers
  "which projects does this subject administer, with which switches" (`Engine.ProjectsOn`). Users and groups only; sessions and
  tokens with `admin: true` only (Q-AUTHZ-183).
- **Three rights.** `any` (administration), `managePolicies`, `manageRepositories`. What each allows is the table of
  R-AUTHZ-026.
- **The shared step is coarse, the handler narrows.** An operation declares `{permission: <cap>, projectAdmin: <right>}`
  ([authorize.go](../../internal/api/v1/authorize.go)): the step admits the capability holder or a caller that administers any
  project with the right. The handler then calls `requireProject`, `requireProjectOfRepository` or `delegatedScope`
  ([project_scope.go](../../internal/api/v1/project_scope.go)). A project the caller does not administer answers 404; a switch
  that is off answers 403 `project_switch_off`.
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

| Where | What | State |
|---|---|---|
| [internal/project](../../internal/project/service.go) | `Project`, `Input`, `Rights`, `PrincipalRef`, `PrincipalKind`, `KeyRE`; `Service` (List, Get, CreateIn, Replace, Delete, AddAdmin, RemoveAdmin, Assign, Unassign, LookupPrincipals), `Options`, `New`. Layer: domain ([layers.go](../../internal/archtest/layers.go)) | the stub of `New` answers 501 |
| [internal/authz/project.go](../../internal/authz/project.go) | `ProjectRight` (`ProjectAny`, `ProjectManagePolicies`, `ProjectManageRepositories`), `AdministeredProject{Key, ManagePolicies, ManageRepositories}`, `Engine.ProjectsOn`, `Engine.AdministersOn`, `Engine.AdministersRepositoryOn`, `Index.ProjectOf`; the same three on `authz.Decider` and on `edge.Access` (`Projects`, `Administers`, `AdministersRepository`) | stubs: nil, false, ("", false), "" |
| [internal/authz/roles.go](../../internal/authz/roles.go) | `Me.Projects`, filled by `Effective` from `ProjectsOn`; `GET /authz/me` emits it | follows the stub |
| [internal/authz/policy.go](../../internal/authz/policy.go) | `PolicyInput.Project *string` (JSON `project`, `null` for a global policy) | `resolve` refuses a non-nil value (422 `not_implemented` at `/project`) |
| [internal/authz/index.go](../../internal/authz/index.go) | `Snapshot.Projects`, `Snapshot.ProjectAdmins` (sqlc rows `AuthzBuildProjectsRow`, `AuthzBuildProjectAdminsRow`); `AuthzBuildPolicies` and `AuthzBuildRepositories` carry `project_id` | `BuildIndex` skips project policies; `pgSource.Snapshot` does not read the new rows |
| [internal/repository/repository.go](../../internal/repository/repository.go) | `Repository.ProjectID` (filled by every reader), `Repository.ProjectKey` (to be filled by the service readers) | `ProjectKey` empty |
| [internal/auth/principal/serviceaccounts.go](../../internal/auth/principal/serviceaccounts.go) | `ServiceAccount.Project`, `NewServiceAccount.Project`; the read queries return the project key | `checkNewServiceAccount` refuses a non-empty `Project` (501) |
| [internal/api/v1/authorize.go](../../internal/api/v1/authorize.go), [access.go](../../internal/api/v1/access.go), [project_scope.go](../../internal/api/v1/project_scope.go) | `Requirement.ProjectAdmin`; `ProjectAccess` (`AdministeredProjects`, `AdministersRepository`); `DelegatedScope`, `delegatedScope`, `requireProject`, `requireProjectOfRepository`, `refuseProject` | complete |
| [internal/api/v1/projects_api.go](../../internal/api/v1/projects_api.go) | the ten handlers; `AuthzServices.Projects`; `projects()` | 503 without a service, else 501 |

The engine rule for the three questions: a subject administers a project when its user ID or one of its group IDs is a row of
`project_admin` for that project, and the credential may exercise it (a session, or a token with `admin: true`); a service
account, a CI identity and anonymous never do. `AdministersOn` and `AdministersRepositoryOn` answer false for an unknown
project and for one the subject does not administer alike. Project administrators also hold `manage` on every repository of the
project: one grant `project:<key>` per project repository and administrator principal, unrestricted in packages and paths,
added after the policy loop of `BuildIndex`.

### API operations

The paths are in [api/openapi.yaml](../../api/openapi.yaml) (`# >>> M5a projects`); the generated server interface is
`gen.ServerInterface`, the TypeScript types are `web/src/app/api/generated/schema.d.ts`.

| Operation | Declaration | Handler narrowing | Notes |
|---|---|---|---|
| `listProjects` `GET /projects` | `projects:read` + `projectAdmin: any` | a caller without the capability sees its administered projects | cursor paging by key; `q`, `sort` |
| `createProject` `POST /projects` | `projects:write` | — | idempotent (`s.idem.Do`); 409 `already_exists`, `project_key_overlap`; epoch |
| `getProject` `GET /projects/{key}` | `projects:read` + `projectAdmin: any` | `delegatedScope.Sees(key)` else 404 | `ETag`, `If-None-Match` |
| `replaceProject` `PUT /projects/{key}` | `projects:write`, recent auth, `x-trove-if-match: required` | — | 412, 428 from the binder; epoch; `authz.project.updated` |
| `deleteProject` `DELETE /projects/{key}` | `projects:write`, recent auth | — | `If-Match` optional; 409 `project_not_empty`; epoch |
| `addProjectAdmin` `PUT /projects/{key}/admins/{principalRef}` | `projects:write`, recent auth | — | 201 or 200; exempt from `If-Match` (ifmatch_test.go) |
| `removeProjectAdmin` `DELETE …` | `projects:write`, recent auth | — | 204; 404 |
| `assignProjectRepository` `PUT /projects/{key}/repositories/{repoKey}` | `repositories:write-config`, recent auth | — | `?move=true`; 409 `repository_assigned`; exempt from `If-Match` |
| `unassignProjectRepository` `DELETE …` | `repositories:write-config`, recent auth | — | 204; 404 |
| `lookupProjectPrincipals` `GET /projects/{key}/principals` | `identities:read` + `projectAdmin: managePolicies` | `requireProject(…, ProjectManagePolicies)` | `q` ≥ 2 characters, at most 20, kind and name only |

Operations of other pages that gain a project path (declarations changed in the document; the handler work is WP-A and WP-C):

| Operation | Declaration | The project administrator's case |
|---|---|---|
| `listPermissionPolicies`, `getPermissionPolicy` | `policies:read` + `projectAdmin: any` | only project policies; without the capability `project=<key>` is required on the list (403 without it), 404 for another project |
| `createPermissionPolicy`, `replacePermissionPolicy`, `patchPermissionPolicy`, `deletePermissionPolicy` | `policies:write` + `projectAdmin: managePolicies` | the policy's project must be one the caller administers with `managePolicies`; a global policy is 403 |
| `validatePermissionPolicy` | `policies:read` + `projectAdmin: managePolicies` | a policy with a project of its own |
| `createRepository`, `replaceRepository`, `updateRepository`, `deleteRepository`, `testRepositoryUpstream`, `testUpstream` | `repositories:write-config` + `projectAdmin: manageRepositories` | create: `project` in the body is required and the key starts with `<key>-`; the others: `requireProjectOfRepository(…, ProjectManageRepositories)`; `testUpstream` (no repository) needs any project with the switch |
| `listRepositories`, `getRepository` | public (the handler decides) | the project's repositories with the full document |
| `listServiceAccounts`, `getServiceAccount` | `identities:read` + `projectAdmin: any` | the project's accounts; `project=<key>` required on the list |
| `createServiceAccount`, `enableServiceAccount` | `identities:write` + `projectAdmin: managePolicies` | `project` in the body (create); the account's project (enable) |
| `updateServiceAccount`, `disableServiceAccount`, `deleteServiceAccount` | `identities:write` + `projectAdmin: any` | the account's project; never blocked by a switch |
| `createServiceAccountToken` | `authenticated` | the account's project with `managePolicies`; the token rules of Q-AUTHZ-177 |
| `listTokens`, `getToken`, `revokeToken` | `authenticated` | tokens of the project's accounts |
| `revokeServiceAccountTokens` | `tokens:revoke` + `projectAdmin: any` | the account's project |
| `getAuthzMe` | `authenticated` | `projects` |

The contract tests that see a declaration: `TestR_API_007_DocumentDeclaresRecentAuth` (the table in
[recent_auth_test.go](../../internal/api/v1/recent_auth_test.go) lists the six project operations),
`TestR_API_012_OneMechanismForAMissingIfMatch` (the two exemptions in [ifmatch_test.go](../../internal/api/v1/ifmatch_test.go)),
and the document tests, which load every declaration through `parseRequirement`.

### Error codes

All in [codes.go](../../internal/errs/codes.go) (`>>> M5a projects`).

| Code | Status | Where |
|---|---|---|
| `not_implemented` | 501 | the stubs; removed from use when the work packages land |
| `repository_outside_project` | 422 field | a project policy key selector; a virtual member or deploy target of a project administrator's write; unknown key answers the same |
| `unknown_project` | 422 field | a policy names a project that does not exist or one the caller does not administer |
| `principal_outside_project` | 422 field | a project service account in a global policy, another project's policy, or as a package owner outside its project |
| `project_prefix_required` | 422 | a repository, policy or service account name without `<key>-` |
| `name_reserved_by_project` | 422 | a global service account named `<key>-…` of an existing project |
| `project_account_not_groupable` | 422 | a project service account as a group member |
| `project_not_empty` | 409 | delete of a project with repositories (any lifecycle) or service accounts |
| `repository_assigned` | 409 | assign without `move=true` |
| `project_key_overlap` | 409 | a key that is another's dash-prefix, or the prefix of a global service account name |
| `project_switch_off` | 403 | an operation whose switch is off |

### Audit events

[events_projects.go](../../internal/audit/events_projects.go). All transactional, category `authz`, with the epoch in the details.
Flagged `journal: revocation` (the producer must supply the entry, R-AUTHZ-031): `authz.project.updated`, `.deleted`,
`.admin_removed`, `.repository_unassigned`. Not journaled: `.created`, `.admin_added`, `.repository_assigned`. A move records
`.repository_unassigned` and `.repository_assigned` in one transaction. Service accounts and tokens use the identity events;
their schemas carry the member `project` ([events_identity.go](../../internal/audit/events_identity.go)).

### Database

Migration `00019_projects.sql`, queries `project.sql`, `authz.sql` (extended), `identity.sql` and `repository.sql`
(`InsertRepository` takes `project_id`); the table list is in [database.md](database.md#projects-tables-00019). New statements
for the packages: `ProjectList`, `ProjectGet`, `ProjectLock`, `ProjectInsert`, `ProjectUpdate`, `ProjectDelete`, `ProjectCountMembers`,
`ProjectPolicyNames`, `ProjectAdmins`, `ProjectAdminsOfAll`, `ProjectAddAdmin`, `ProjectRemoveAdmin`, `ProjectOfRepository`,
`ProjectAssignRepository`, `ProjectRepositoryKeys`, `ProjectServiceAccountNames`, `ProjectKeysOverlapping`, `ProjectKeysPrefixing`,
`ProjectGlobalAccountsWithPrefix`, `ProjectLookupPrincipals`, `ProjectsOfPrincipals`, `AuthzBuildProjects`,
`AuthzBuildProjectAdmins`, `AuthzInsertProjectEntry`, `IdentityInsertProjectServiceAccount`.

## Who owns which file

Edit only your own files; a change in another work package's file goes through a request to its owner.

| Work package | Files |
|---|---|
| **WP-A authz engine** | `internal/authz/**` (the index inputs in `source.go`, the scoped compile and the `project:<key>` grants in `index.go`, the `matchCache` key, `project.go` (the bodies), `validate.go` (project rules), `store.go`, `policy_list.go`, `bindings.go` (refuse a project service account), the ownership owner check, the hooks), `internal/meta/postgres/queries/authz.sql`, `internal/api/v1/authz_api.go` (the `project=` filter, the project-scoped list and writes, `GetAuthzMe`), `test/integration/authz/**` |
| **WP-B projects** | `internal/project/**` (service body), `internal/api/v1/projects_api.go`, `internal/meta/postgres/queries/project.sql`, `internal/app/wire_authz.go` (the `project.New` options only), `test/integration/projects/**` |
| **WP-C delegated writes** | `internal/repository/**`, `internal/auth/**`, `internal/api/v1/{repositories_api,groups_api,tokens_api,upstream_api}.go`, the queries `repository.sql` and `identity.sql`, the repository readers and `ListFilter.Project`, `test/integration/{transfer,identity}/**` for their parts |
| **WP-D UI** | `web/**`, `docs/developer/web-ui.md`, `docs/manual/**` for the screens |
| **Shared, settled by the contracts agent** | `api/openapi.yaml` and `internal/api/v1/gen/**` (change by request: the document is the contract), `internal/api/v1/{authorize,access,project_scope}.go`, `internal/errs/codes.go`, `internal/audit/events_projects.go`, the migration |

What each package still has to do, in order of dependency:

1. **WP-A first.** Read `Snapshot.Projects` and `.ProjectAdmins` in `pgSource.Snapshot`; build the project grants and the
   project-scoped compile; fill the three questions and `Me.Projects`; replace the refusal in `resolve` with the project rules
   (existence, `<key>-` name, selectors, entries; write the entries of a project policy with `AuthzInsertProjectEntry`);
   refuse a project service account in `bindable`, in global and foreign policies and as a package owner outside its project;
   add `selector_outside_project`; the `project=` filter and the project-scoped list and get.
2. **WP-B** needs only the schema and the queries, and `AuthzInsertProjectEntry` is not its business: its delete goes through
   `authz.Service.Delete` for each policy of `ProjectPolicyNames`.
3. **WP-C** needs `AdministersRepositoryOn` and `ProjectsOn` for the narrowing; it creates a repository with the project in the
   `CreateTx` transaction (`InsertRepository` has `project_id`), applies the prefix and the virtual rule of Q-AUTHZ-178, serves the
   project filter and the full document to project administrators, creates project service accounts (`IdentityInsertProjectServiceAccount`),
   and refuses a project service account as a group member.
4. **WP-D** reads `authz/me.projects` and the generated types.

## Tests

Tests named after the requirement (`TestR_AUTHZ_026_…`). Expected state of the unit tier and the integration tier after the
contracts and before the work packages:

| Test | State | Passes after |
|---|---|---|
| `go test ./internal/...`, `./cmd/...` | green | stays green |
| `TestR_API_007_DocumentDeclaresRecentAuth`, `TestR_API_012_…`, the document tests | green | stays green |
| `TestProjectsMigration` ([projects_test.go](../../test/integration/postgres/projects_test.go)), `TestTableRegistry`, `TestRevisionColumns` | green | stays green |
| `TestR_API_002_EveryPermissionOperationAnswersAsDocumented` (authz) | green; does not walk the project operations | WP-B adds them to its operation list |
| The web type check | green: the fakes carry `project: null` and `projects: []` | WP-D replaces them with fakes that model projects |
| `test/integration/transfer/repository_api_test.go` (`?project=acme` empty) | green | WP-C replaces the placeholder |

Each package adds its integration tests next to its code; the acceptance criteria of R-AUTHZ-026 are the list. The harness
`testenv` (seed.go: `User`, `ServiceAccount`, `Bind`, `Token`, `Repository`, `Policy`) needs a project variant for each; they are
shared test code, so the first package that needs one adds it and tells the others.
