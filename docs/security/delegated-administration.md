# Delegated administration

Status: built in beta phase 5a ([R-AUTHZ-026](../spec/authorization.md#r-authz-026--projects-for-delegated-administration)). The schema with
its guards, the engine's questions, the declaration form and the handlers are in the code ([developer/projects.md](../developer/projects.md));
the integration tests of the phase check each rule below. The open gaps are the `H-` items at the end of the pages that own the mechanism.

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
   result of a selector is keyed by the project. The administrator's own `manage` is a grant per project repository. A removed
   administrator, a switch turned off or a repository unassigned takes effect on every replica with the authorization epoch
   ([permissions.md](permissions.md)).
4. **The database.** Administrators are users or groups; a project service account is no group member, its name starts with the
   project key and its project never changes; the grants of a project policy cannot be written to the table of global grants
   (SQLSTATE `AI001` to `AI004`, [developer/database.md](../developer/database.md#projects-tables-00019)).

## Credentials

- A project administrator exercises the right through a session or a token with `admin: true`; a token narrowed to repository rights
  administers nothing ([Q-AUTHZ-183](../spec/authorization.md#q-authz-183--who-administers-a-project-and-through-which-credentials)).
  A service account and a CI mapping never administer a project.
- A token a project administrator issues for a project service account is never non-expiring, lives at most as long as the
  installation allows for service-account tokens, has `admin: false` and no `system` item, and may be bound to endpoints
  ([Q-AUTHZ-177](../spec/authorization.md#q-authz-177--what-a-project-administrator-may-do-with-service-accounts-and-their-tokens)).
  The account holds repository rights only, through the project's policies. Revoking, disabling and deleting never depend on a switch.
- Creating a token requires a recent sign-in with a session (R-API-007), as for every token.

## Names

Repositories, policies and service accounts of a project are named `<key>-…`; no project key is another's dash-prefix; a global
service account never takes a project's prefix
([Q-AUTHZ-176](../spec/authorization.md#q-authz-176--names-of-a-project-are-prefixed-with-its-key-and-a-global-name-never-takes-a-project-prefix)).
A delegate therefore cannot create a name in another project's space or a global name, and a 409 on a global name does not tell it
whether one exists.

## During an upgrade from 0.9.x

The previous release builds its index without projects. The grants of project policies are stored where it does not read them, so
it sees a project policy without entries; a rewrite of one by it fails in the database
([Q-AUTHZ-174](../spec/authorization.md#q-authz-174--the-grants-of-a-project-policy-live-where-the-previous-release-does-not-read-them)).
What a rollout costs is a project's rights absent on old replicas, never a grant beyond the project
([H-395](../spec/authorization.md#h-395--during-a-rollout-from-09x-the-rights-of-a-project-are-partly-absent-and-its-name-rules-are-not-enforced)).

## What this does not cover

The gaps of these mechanisms are recorded on the spec page that describes them, and only linked here:

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
  a global service account and a project key created at the same instant can overlap.
