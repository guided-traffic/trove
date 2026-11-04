# Delegated administration

Status: built in beta phase 5a ([R-AUTHZ-026](../spec/authorization.md#r-authz-026--projects-for-delegated-administration)). The schema with
its guards, the engine's questions, the declaration form and the handlers are in the code ([developer/projects.md](../developer/projects.md));
the integration tests of the phase check each rule below. The open gaps are the `H-` items at the end of the pages that own the mechanism;
the one of a project administrator's remote repositories, which only this page describes, is at the end of this page.

This page says what a project administrator can reach, what bounds it, and what a stolen credential of one gains. It does not repeat
the model of permissions ([permissions.md](permissions.md)) or of tokens ([tokens-and-credentials.md](tokens-and-credentials.md)).

## Roles and trust boundaries

| Party | Trusted with | Not trusted with |
|---|---|---|
| Administrator of trove (`admin`; `projects:write`, `repositories:write-config`) | Creating projects, setting their switches and administrators, assigning repositories | — |
| Project administrator (user or member of a listed group) | Within its project and within the switches: the project's policies, repositories, service accounts and their tokens | Anything outside the project; system roles; global policies, groups and users; `anonymous` and `authenticated` grants |
| Project service account | What the project's policies grant it | Management operations (it administers nothing), groups, roles, grants of other projects |
| `auditor`, `security-admin`, `repository-admin` (`projects:read`) | Reading every project | Changing one (`projects:write` is `admin`'s) |

```
 administrator of trove ── creates ──▶ project ◀── administered by ── users, groups (identity provider)
        │                               │  switches: managePolicies, manageRepositories
        │ assigns repositories          ├── repositories  (prefix <key>-, own virtuals stay inside)
        ▼                               ├── project policies ──grants──▶ users, groups, project service accounts
   global policies ── union ──▶ ────────┘                                   │
                                        └── project service accounts ◀── tokens issued and revoked by the administrators
```

## What bounds a project administrator

Four layers, none of which depends on the one before it:

1. **The declaration.** An operation names the capability and the right a project administrator needs (`projectAdmin`); the shared
   step refuses a caller that holds the capability and administers no project before any lookup (it does not ask for the right: the handler answers a switch that is off, so the answer never depends on other projects) ([ADR-146](../decisions.md#adr-146--an-operation-admits-project-administrators-through-a-declaration-of-its-own)).
2. **The handler narrows.** A project the caller does not administer answers 404, as an unknown one does; a switch that is off
   answers 403 `project_switch_off`. A repository outside the project answers like an unknown repository.
3. **The index.** A project policy covers the repositories of its project only, whatever its stored selectors say, and the memoized
   result of a selector is keyed by the project. The administrator's own `manage` is not a grant: the decision allows it when the subject administers the project, by the rule
   of the three questions, so a service account in an administrator group and a token without `admin: true` hold none
   ([Q-AUTHZ-196](../spec/authorization.md#q-authz-196--the-manage-of-a-project-administrator-is-decided-from-the-administration-not-from-a-grant-per-repository)). A removed
   administrator, a switch turned off or a repository unassigned takes effect on every replica with the authorization epoch
   ([permissions.md](permissions.md)).
4. **The database.** Administrators are users or groups; a project service account is no group member, its name starts with the
   project key and its project never changes; the grants of a project policy cannot be written to the table of global grants, and a project service account is no principal of a role binding, of a global entry or of another project's entry
   (SQLSTATE `AI001` to `AI005`, [developer/database.md](../developer/database.md#projects-tables-00019-to-00025)).

## Credentials

- A project administrator exercises the right through a session or a token with `admin: true`; a token narrowed to repository rights
  administers nothing ([Q-AUTHZ-183](../spec/authorization.md#q-authz-183--who-administers-a-project-and-through-which-credentials)).
  A service account and a CI mapping never administer a project.
- A token a project administrator issues for a project service account is never non-expiring, lives at most as long as the
  installation allows for service-account tokens, has `admin: false` and no `system` item, and may be bound to endpoints
  ([Q-AUTHZ-177](../spec/authorization.md#q-authz-177--what-a-project-administrator-may-do-with-service-accounts-and-their-tokens)).
  The account holds repository rights only, through the project's policies. Revoking, disabling and deleting never depend on a switch.
- Such a token is not self-rotatable (422 at `/selfRotatable`,
  [Q-AUTH-192](../spec/identity-and-authentication.md#q-auth-192--a-token-a-project-administrator-issues-is-not-self-rotatable)): each
  rotation would give its successor the original lifetime, so the partner holding it could renew it for as long as it kept rotating.
  A project administrator cannot rotate it either (Q-AUTH-190).
- Creating a token requires a recent sign-in with a session (R-API-007), as for every token.

## Remote repositories of a project

A project administrator with `manageRepositories` names the upstream of its project's remote repositories and tests it
([Q-AUTHZ-173](../spec/authorization.md#q-authz-173--a-project-administrator-creates-remote-repositories)). The installation's
address rules bound where a request goes ([egress.md](egress.md)); what the request carries and which path it takes is bounded by
the repository service, in the transaction of the write, against the stored section
([delegated.go](../../internal/repository/delegated.go), `checkDelegatedRemote`):

| What | A project administrator's write and unsaved test | Why |
|---|---|---|
| a secret it types (`{"value": …}`) | accepted, sealed for this repository | it is the project administrator's own |
| a `secretRef` (a file of `secrets_dir`, a `TROVE_CAC_` variable) | 422 `secret_value_required`, except the reference the repository holds, sent back unchanged | the references are the installation's and visible in the documents of the project; one would send a partner's token to the project administrator's host ([Q-REPO-127](../spec/repositories-and-proxy.md#q-repo-127--a-project-administrator-gives-an-upstream-secret-as-a-value-and-a-stored-one-stays-with-its-hosts)) |
| a stored credential, sealed or referenced, kept while `url` changes, a `sendCredentials` host is added or `forwardAuthOnRedirect` turns on | 422 `secret_value_required` at `/remote/password`: a new value, or none | the credential an administrator of trove stored goes to the hosts it was stored for and nowhere else |
| `outboundProxy.mode` other than `default`, a `sourceNetwork` | 422 `outbound_path_restricted`, except the stored values kept while `url` and the extra hosts stay | the outbound proxy sees every delegate's traffic, and a source address a partner trusts is used only for the upstream an administrator chose ([Q-REPO-128](../spec/repositories-and-proxy.md#q-repo-128--a-project-administrators-remote-repository-takes-the-installations-outbound-path)) |
| `remote.mode: mirror`, or a change of `remote.mirror` other than `paused` | 422 `mirror_restricted`, except the stored values sent back unchanged; `paused` and the mode back to `pullThrough` are accepted ([validate_mirror.go](../../internal/repository/validate_mirror.go), `checkDelegatedMirror`) | a mirror downloads and stores everything its patterns admit without a client asking: how much of an upstream the installation stores, and how hard it pulls, is an administrator's decision ([Q-REPO-142](../spec/repositories-and-proxy.md#q-repo-142--who-may-switch-a-remote-to-mirror)) |

The saved upstream test reads the repository's project again in the service, as a replace, a patch and a delete do, so a repository that
left the project after the handler decided is not contacted (404). A delete with `detach=true` edits the project's virtual repositories only:
while one outside the project lists the repository, the delete answers 409 and names the project's own
([Q-REPO-129](../spec/repositories-and-proxy.md#q-repo-129--a-project-administrators-delete-leaves-virtual-repositories-outside-the-project-alone)).
The events `repository.created`, `.updated` and `.deleted` of a project's repository carry `project`. A holder of
`repositories:write-config` is not bound by the rules of the table ([H-281](data-and-secret-flow.md#h-281--a-holder-of-repositorieswrite-config-can-send-any-file-of-secrets_dir-to-an-upstream-of-their-choice)).

## Names

Repositories, policies and service accounts of a project are named `<key>-…`; no project key is another's dash-prefix; a global
service account never takes a project's prefix
([Q-AUTHZ-176](../spec/authorization.md#q-authz-176--names-of-a-project-are-prefixed-with-its-key-and-a-global-name-never-takes-a-project-prefix)).
A delegate therefore cannot create a name in another project's space or a global name, and a 409 on a global name does not tell it
whether one exists; a global policy that was named like that before the project existed is the exception
([H-405](../spec/authorization.md#h-405--a-global-policy-that-predates-a-project-keeps-the-projects-prefix)).

## What a delegate can test

The policy list of a project administrator answers an empty list for a principal or a repository that does not exist, as for one of
another project, and does not expand the groups of a principal (`expandGroups=true` is 403); a validation names another project's
service account like a name that does not exist ([Q-AUTHZ-197](../spec/authorization.md#q-authz-197--what-a-project-administrators-policy-list-and-validation-tell-about-other-objects)).
The names of users and groups remain readable through the lookup ([H-392](../spec/authorization.md#h-392--a-project-administrator-can-enumerate-user-and-group-names)).
A project policy names no auto-join group, which would stand in for `authenticated`. A refusal of the narrowing is recorded as
`authz.denied` of the operation's capability ([Q-AUTHZ-201](../spec/authorization.md#q-authz-201--a-project-administrators-refusals-are-audited-as-the-denial-of-the-capability),
[H-406](../spec/authorization.md#h-406--the-audit-event-of-a-refused-project-administrator-does-not-name-the-project)).

## During an upgrade from 0.9.x

The previous release builds its index without projects. The grants of project policies are stored where it does not read them, so
it sees a project policy without entries; a rewrite of one by it fails in the database
([Q-AUTHZ-174](../spec/authorization.md#q-authz-174--the-grants-of-a-project-policy-live-where-the-previous-release-does-not-read-them)).
What a rollout costs is a project's rights absent on old replicas, never a grant beyond the project
([H-395](../spec/authorization.md#h-395--during-a-rollout-from-09x-the-rights-of-a-project-are-partly-absent-and-its-name-rules-are-not-enforced)).

## What this does not cover

The gaps of these mechanisms are recorded on the spec page that describes them, and only linked here, except H-403, whose mechanism
only this page describes:

- [H-392](../spec/authorization.md#h-392--a-project-administrator-can-enumerate-user-and-group-names) — the lookup of users and
  groups lists the names of every active user and group by prefix;
- [H-393](../spec/authorization.md#h-393--a-project-administrator-with-managerepositories-probes-the-addresses-the-outbound-rules-allow) —
  the upstream test of a project administrator's remote repository (it extends [H-282](egress.md#h-282--the-upstream-test-tells-a-holder-of-repositorieswrite-config-what-it-sees-of-an-address));
- [H-394](../spec/authorization.md#h-394--whoever-controls-a-project-administrator-group-administers-the-project) — whoever controls
  the identity provider's group administers the project;
- [H-395](../spec/authorization.md#h-395--during-a-rollout-from-09x-the-rights-of-a-project-are-partly-absent-and-its-name-rules-are-not-enforced) —
  the rights of a project during a rollout from 0.9.x.
- [H-398](../spec/authorization.md#h-398--a-failed-project-delete-can-leave-the-project-without-some-of-its-policies) — a project
  delete that fails after the policy deletions leaves the project with fewer policies;
- [H-399](../spec/authorization.md#h-399--a-global-service-account-and-a-project-key-created-at-the-same-instant-can-overlap) —
  a global service account and a project key created at the same instant can overlap;
- [H-405](../spec/authorization.md#h-405--a-global-policy-that-predates-a-project-keeps-the-projects-prefix) — a global policy that
  predates a project keeps the project's prefix;
- [H-406](../spec/authorization.md#h-406--the-audit-event-of-a-refused-project-administrator-does-not-name-the-project) — the audit
  event of a refused project administrator does not name the project.

### H-403 · What a project administrator still reaches of an upstream an administrator configured

Stage: the remote repositories an administrator of trove assigned to a project, and the rules of
[the table above](#remote-repositories-of-a-project) ([delegated.go](../../internal/repository/delegated.go)). Subject: the upstream
credentials, references and outbound path an administrator configured for such a repository, and the partner networks they open.

- **Live, by design of the assignment.** While `url` and the credential hosts stay, a project administrator causes fetches and saved
  upstream tests with the stored credential, the stored reference and the stored `sourceNetwork` or `direct` mode, and reads
  `authenticated` and the status of the answer. It cannot send them elsewhere, and it cannot read a value. What makes it real: an
  administrator who assigns a repository whose upstream credential the project is not meant to use; unassign it, or move the credential
  to a repository outside the project.
- **Live, names only.** The full document shows a reference by name (`{"secretRef": {"file": "partner-token"}}`), so a project
  administrator learns the file names under `secrets_dir` and the `TROVE_CAC_` variable names its repositories use, not their content.
- **Live, one bit.** The 409 of a delete tells a project administrator that a virtual repository outside the project lists one of its
  repositories (Q-REPO-129), without the key.
- **Dormant until R-REPO-034, R-REPO-036 and R-REPO-037.** A named client certificate, a sensitive header value and a named outbound
  proxy are installation objects of the same kind as a reference; today every document that sets them is refused with `not_applicable`,
  for every writer. What makes it real: building one of them without extending `checkDelegatedRemote` to it, so that a project
  administrator could present the installation's client certificate, or send a header value an administrator stored, to a host it names.
