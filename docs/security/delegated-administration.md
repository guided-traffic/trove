# Projects: members, roles and delegated administration

Status: the project model of [R-AUTHZ-026](../spec/authorization.md#r-authz-026--projects-for-delegated-administration)
(Q-AUTHZ-206 to Q-AUTHZ-209, Q-AUTHZ-214 to Q-AUTHZ-217) as the authorization engine and the project service build it: members with
one of four roles, the role in the decision path, project policies named within their project, the last-Owner rule and the move of a
repository between projects ([R-REPO-060](../spec/repositories-and-proxy.md#r-repo-060--move-a-repository-to-another-project)). The
code is [internal/authz](../../internal/authz) (`project.go`, `project_role.go`, `decide.go`, `index.go`) and
[internal/project](../../internal/project) (`members.go`, `move.go`); [developer/projects.md](../developer/projects.md) is the contract.
The management API's declarations that admit members are the API's ([permissions.md](permissions.md)).

This page says what a member can reach through its role, what bounds it, what a stolen credential of one gains, and what keeps a
project's objects inside it. It does not repeat the model of permissions ([permissions.md](permissions.md)) or of tokens
([tokens-and-credentials.md](tokens-and-credentials.md)).

## Roles and trust boundaries

| Party | Trusted with | Not trusted with |
|---|---|---|
| Administrator of trove (`admin`; `projects:write`, `repositories:write-config`) | Creating and deleting projects, the members of every project (the first Owner among them), removing the last Owner, moving any repository | — |
| Owner (user, or member of a group with the role) | Every action on every repository of the project; the project's members, policies, service accounts, CI mappings, settings; moving a repository to another project it owns | Anything outside the project; system roles; global policies, users and groups; `anonymous` and `authenticated` grants; removing or demoting the last Owner |
| Maintainer | Every action on every repository of the project (`manage` included) | The administration of the project |
| Developer | `read`, `annotate`, `deploy` on every repository of the project | `overwrite`, `delete`, `manage`; the administration |
| Reader | `read` on every repository of the project, reading the project and its members | Every write |
| Project service account, CI mapping | What the project's policies grant it | Every role: it is never a member |
| `auditor`, `security-admin`, `repository-admin` (`projects:read`) | Reading every project and its members | Changing one |

```
 administrator of trove ── creates, names the first Owner ──▶ project ◀── members: users and groups with a role
                                                               │          (reader < developer < maintainer < owner)
                                                               ├── repositories (key unique in the project)
   global policies ─────────────── union ─────────────────────┤
                                                               ├── project policies (<project>/<name>) ──▶ users, groups,
                                                               │        project service accounts, CI mappings
                                                               └── project service accounts (<project>/<name>)
```

## What a role grants, and through which credential

- **The role is decided, not stored as a grant.** The index keeps every membership row by principal
  ([index.go](../../internal/authz/index.go), `addProjects`). A decision derives the subject's roles once from its user ID and its
  group IDs, the highest per project ([project.go](../../internal/authz/project.go), `projectRoles`), and looks the repository's
  project up in them ([decide.go](../../internal/authz/decide.go), `grantedBy`). The role's actions join the policy grants as one more
  source, `role:<projectKey>:<role>`, so the refusal and audit of a decision are the same as for a grant. On a virtual repository a role
  has the effect of `read` and `manage` only, as a grant does (R-AUTHZ-009).
- **Credentials.** A role reaches a user's UI session and personal access tokens; a token's scope is the ceiling over it as over a
  grant ([Q-AUTHZ-215](../spec/authorization.md#q-authz-215--a-members-role-acts-through-its-session-and-personal-tokens-within-their-scope-and-service-accounts-are-not-members)).
  The administration of an Owner needs a session or a token with `admin: true` (`mayAdminister`); through any other credential an Owner
  holds what a Maintainer holds, which is what `RoleOn` and `MembershipsOn` answer. A leaked everyday token of an Owner reads and writes
  content within its scope and changes no member, policy or setting.
- **Who is never a member.** A service account, a CI mapping, a Bastion registration and `anonymous` carry no role
  (`carriesRoles`). The member write refuses every principal but a user or a group, and an auto-join group, with 422
  `principal_not_allowed` ([members.go](../../internal/project/members.go)); the table refuses a principal of another kind
  (trigger `project_member_kind`, SQLSTATE `AI001`); the index skips a row of another kind and a group that became auto-join after it
  was made a member, so neither grants (fail closed).
- **Ownership.** A `deploy` through a role makes the member through which the role came the owner of a new name: the group whose
  membership carries the role, or the user for its own membership ([ownership.go](../../internal/authz/ownership.go), `roleClaimers`,
  R-AUTHZ-018 rule 2). A role is never an owner identity of its own, so a member who leaves the project keeps owning what it published
  through its own membership.
- **Cost.** The derivation reads a map entry per user and group ID and merges per membership; nothing depends on the number of projects
  of the installation and nothing is queried (`TestR_AUTHZ_015_ARoleDecisionDoesNotGrowWithTheProjects`).

## The members and the last Owner

- The writes run under the project's row lock ([members.go](../../internal/project/members.go), `lockProject`). A write that would take
  the role `owner` from the last membership with it (a group counts as one, whatever its members) answers 409 `last_owner` unless the
  caller holds `projects:write` ([Q-AUTHZ-217](../spec/authorization.md#q-authz-217--the-last-owner-of-a-project-is-removed-or-demoted-only-by-an-administrator-of-trove)).
  Two Owners who remove each other at once run one after the other, and the second is refused.
- Every member write notifies the change area `authz` in its transaction, so the role takes effect or ends on every replica within the
  bound of R-AUTHZ-028. An addition records `authz.project.member_added`; a change and a removal record `member_changed` and
  `member_removed` with the journal entry of R-AUTHZ-031 (the project and the principal by ID, the roles of a change), so a restore
  replays no role above the one in force. The same role written again changes nothing and records nothing (Q-AUTHZ-193).
- A group that is an auto-join group is never a member, in either order, because every user is in it and the membership would make
  every user a member. The member write reads the flag under a shared lock of the group's row and answers 422 `principal_not_allowed`
  ([members.go](../../internal/project/members.go), `memberPrincipal`, query `ProjectGroupAutoJoinShare`). Turning the flag on takes the
  same row `FOR UPDATE` and then looks for the group's project memberships; a group that is a member of a project answers 409
  `auto_join_member_conflict` and names the projects ([groups.go](../../internal/auth/principal/groups.go), `UpdateGroup`, query
  `IdentityGroupProjects`). The two checks run one after the other, so neither can pass on a state the other is changing.

## What keeps a project's objects inside it

- **Selectors.** A project policy names repositories by bare key, key pattern or class, read in the project's context
  ([selector.go](../../internal/authz/selector.go), `qname.ParseSelector`); a key is looked up in the project only, so a repository of
  another project cannot be named at all (422 `unknown_repository`), and a selector with a `/` is 422 `invalid_pattern`. The build
  covers the project's repositories only, whatever a stored selector says, and the memoized match of a selector is keyed by the
  project (`builder.covered`). A global policy, a token scope and an exposure rule name the project in every selector; a bare key there
  is refused, never read as a selector that matches nothing ([Q-AUTHZ-216](../spec/authorization.md#q-authz-216--repository-selectors-name-the-project-in-instance-contexts-and-a-bare-key-inside-a-project)).
- **Names.** A project's policy is stored as `<projectKey>/<name>` (`StoredPolicyName`), a global one without a `/`; the database
  compares the prefix with the policy's project and keeps the project fixed (SQLSTATE `AI003`). The same name may exist in two projects
  and once instance-wide; the delete of a project removes its own policies by their stored names and never another's
  ([Q-AUTHZ-209](../spec/authorization.md#q-authz-209--a-projects-policies-and-service-accounts-are-named-within-the-project-administrators-keep-instance-wide-ones)).
  No prefix rule and no overlap check exist between keys and names.
- **Principals of a project policy.** Users, groups, service accounts of no project or of the same project, CI mappings; never
  `anonymous`, `authenticated`, an auto-join group or another project's service account (422 `principal_outside_project`; to an Owner
  without a policies capability another project's account answers as an unknown one, `AsProjectOwner`). The database refuses a project
  service account in a role binding, a global entry or another project's entry (SQLSTATE `AI005`), and the grants of a project policy
  in the table of global grants (`AI004`).

## Moving a repository

A move ([move.go](../../internal/project/move.go)) takes both projects' row locks in key order and the repository's row lock, and is
refused with 409 `repository_in_use`, the member `references` listing each object by kind and name, while anything of the source
project names the repository: a virtual repository's member list, pin rule or deployment target, a key selector of a project policy, an
inline grant of a project CI mapping that names its key; a virtual repository moves only without members and deployment target. A
shared ownership domain, which R-REPO-060 counts as well, cannot be set yet (`ownershipDomain` is 422 `not_applicable`). A
target that holds the key, also by a repository being deleted, is 409 `already_exists`. Nothing is rewritten on the caller's behalf,
so no grant or resolution changes behind the back of the people who wrote those objects. The move notifies `authz` and `repo_config`
with the repository's ID and raises its `config_version`, so the old address answers 404 and every cache keyed by the configuration
version drops what it built, on every replica within the bound of R-AUTHZ-028; it records `repository.moved` with the journal entry of
R-AUTHZ-031. What holds the repository by its ID outside the project (a key selector of a global policy, an exposure rule) follows it.
Who may move is the API's decision: `repositories:write-config`, or an Owner of both projects (403 for a member with a lower role in
the target, 404 for a caller that is no member there).

## Remote repositories of a project

An Owner creates the remote repositories of its project ([Q-AUTHZ-173](../spec/authorization.md#q-authz-173--a-project-administrator-creates-remote-repositories)).
The spec bounds what an Owner without `repositories:write-config` may give a remote (a secret as a value, never a reference of the
installation; the installation's outbound path; a mirror's mode and scope as stored): Q-REPO-127, Q-REPO-128, Q-REPO-142 and
Q-REPO-193. The checks belong to the repository service's write path ([delegated.go](../../internal/repository/delegated.go),
[validate_mirror.go](../../internal/repository/validate_mirror.go)) and run against the stored section in the transaction of the
write: the create, the replace and the patch of a repository and the unsaved upstream test. The management API decides who is held
to them ([repositories_api.go](../../internal/api/v1/repositories_api.go), `writeCaller`): a caller that does not hold
`repositories:write-config` and is admitted by the role Owner alone is `Restricted`, and `MayWeakenVulnerabilities` is set for a holder of
`vulnerabilities:triage` ([Q-VULN-69](../spec/vulnerability-scanning.md#q-vuln-69--weakening-the-vulnerabilities-member-needs-the-triage-capability-or-repositoriesWrite-config)). An administrator of trove,
who holds the capability, is bound by none of the rules; the Owner's answers are 422 `secret_value_required`,
`outbound_path_restricted` and `mirror_restricted`, and 403 `vulnerability_weakening_refused`. The integration test
`TestR_AUTHZ_026_AnOwnerWritesRemoteRepositoriesWithinTheRules` ([owner_repositories_test.go](../../test/integration/projects/owner_repositories_test.go))
drives every rule over HTTP as an Owner and as an administrator.

## What this does not cover

The gaps of these mechanisms recorded on the spec pages are linked here; the ones only this page describes follow.

- [H-392](../spec/authorization.md#h-392--a-project-administrator-can-enumerate-user-and-group-names) — the lookup of users and
  groups lists the names of every active user and group by prefix, now to every Owner.
- [H-393](../spec/authorization.md#h-393--a-project-administrator-with-managerepositories-probes-the-addresses-the-outbound-rules-allow) —
  the upstream test of a project's remote repository (it extends [H-282](egress.md#h-282--the-upstream-test-tells-a-holder-of-repositorieswrite-config-what-it-sees-of-an-address)).
- [H-394](../spec/authorization.md#h-394--whoever-controls-a-project-administrator-group-administers-the-project) — whoever controls
  the identity provider's group holds the group's role, Owner included.
- [H-398](../spec/authorization.md#h-398--a-failed-project-delete-can-leave-the-project-without-some-of-its-policies) — a project
  delete that fails after the policy deletions leaves the project with fewer policies.
- [H-406](../spec/authorization.md#h-406--the-audit-event-of-a-refused-project-administrator-does-not-name-the-project) — the audit
  event of a refused declaration does not name the project.

### H-403 · What a project administrator still reaches of an upstream an administrator configured

Stage: a remote repository an administrator of trove created in a project or moved into it. Subject: the upstream credentials,
references and outbound path an administrator configured for such a repository, and the partner networks they open.

- **Live, by design of the move and of the roles.** While `url` and the credential hosts stay, an Owner or a Maintainer causes fetches
  with the stored credential, the stored reference and the stored outbound path, and reads the status of the answer; an Owner reads the
  full document, which names a reference by name (`{"secretRef": {"file": "partner-token"}}`), so it learns the file names under
  `secrets_dir` and the `TROVE_CAC_` variable names its repositories use, not their content. What makes it real: an administrator who
  puts a repository into a project whose members are not meant to use its upstream credential; keep it in another project, or move the
  credential to a repository outside the project.

### H-531 · A run can be requested as often as a pass ends

Stage: the run of a mirror ([mirror_api.go](../../internal/api/v1/mirror_api.go), `enqueueMirrorPass`). Subject: the upstream a mirror
pulls from, and the installation's egress and storage, which a pass loads.

- **Live, by design of the permission.** One pass per repository runs at a time, but nothing limits how often a pass is requested: a
  holder of `manage` on the repository, which every Maintainer and Owner of its project is, and a holder of `repositories:write-config`
  can request a run the moment the last pass ends, and `{"full": true}` makes it a full discovery. Each pass respects the mirror's
  bounds, window and rate-limit waits (R-REPO-052, R-REPO-054), so the cost is upstream requests and discovery work, not unbounded
  storage. The audit event `mirror.pass_requested` names every request and its caller.
- What makes it real: a member that is not trusted with the upstream's goodwill, on an upstream that throttles or bills by request.
- What would close it: a minimum time between requests per repository.
