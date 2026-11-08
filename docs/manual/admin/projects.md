# Projects and delegated administration

Every repository belongs to a project, and a project lets a team hold and administer its own repositories, permission policies, service accounts and CI mappings without a system role. This page explains what a project is, its members and their roles, what an Owner may and may not do, how a repository moves to another project, and what to do when an action is refused.

## What a project is

- A project has a **Key**, a **Display name** and a **Description**. The key matches `^[a-z][a-z0-9]*(?:-+[a-z0-9]+)*$` (2 to 32 characters: lower-case letters, digits and hyphens, starting with a letter and ending with a letter or digit) and cannot be changed; the display name has 1 to 128 characters, the description at most 1024.
- **Every repository belongs to exactly one project** and is addressed inside it. The key of a repository is unique within its project only: projects `web` and `mobile` can each have a repository `npm-remote`. No repository is shared between projects; a public registry is proxied by a remote repository in each project that needs it, and the blob store keeps the content once.
- The project is part of every address of a repository:

| Address | Example |
|---|---|
| Package protocol | `https://trove.example.com/api/npm/web/npm-internal/` |
| Raw files | `https://trove.example.com/files/web/generic-local/docs/guide.pdf` |
| Management API | `https://trove.example.com/api/v1/projects/web/repositories/npm-internal` |

- A repository is named outside its project as `<projectKey>/<repoKey>`, for example `web/npm-internal`: in a global permission policy, a token scope, an exposure rule and the configuration. Inside its project it is named by its key alone.
- A project holds its repositories, its members, its permission policies, its service accounts and the GitLab CI mappings of the tier `project:<projectKey>`. The names of its policies, service accounts and CI mappings are unique within the project; the same name may exist in another project and once outside every project.
- A project is **charged the logical size of the files it holds**: the sizes of all files in its repositories added up, each file counted in full even when another repository or another project holds the same content. The figure is `logicalBytes` in the project's representation (`GET /api/v1/projects` and `GET /api/v1/projects/<projectKey>`), shown as **Storage** on the project's **Settings** and as a column of **Admin area › Projects**. It is updated within a few seconds of an upload or a delete, and a repository that moves to another project takes its share along. The projects' figures add up to more than the object store keeps, because the store keeps shared content once; what the store keeps is the **Stored content** on the [dashboard](dashboard.md). The figure is reported only: no upload is refused because of it.
- Only an administrator of trove (the `admin` role, which holds `projects:write`) creates and deletes projects and names their first Owners.

## Members and roles

Users and groups are the members of a project, each with one role. A service account, a CI mapping, a Bastion registration, `anonymous`, `authenticated` and a group that every user joins automatically are never members (`principal_not_allowed`): a service account reaches a project's repositories through the project's permission policies.

Each role holds what the role before it holds:

| Role | On every repository of the project | In the project |
|---|---|---|
| **Reader** | `read` | Reads the project, its members and its repositories |
| **Developer** | `read`, `annotate`, `deploy` | As Reader |
| **Maintainer** | Every action: also `overwrite`, `delete` and `manage` (package owners, cache invalidation, namespace claims, a mirror pass) | As Reader |
| **Owner** | Every action | Administers the project (next section) |

On a virtual repository a role gives `read` and `manage`; a publish through a virtual repository is decided on its deployment target.

- A user's role in a project is the highest role among its own membership and the memberships of its groups. A user that is no member and in no member group has no role there; the project and everything below it answer `404` to it, exactly like a project that does not exist. A member whose role is too low gets `403`.
- A role is decided from the membership when a request is evaluated, never stored as a grant. It adds to what global and project permission policies grant; the explanation of a decision names it as `role:<projectKey>:<role>`.
- A role acts through the member's browser session and its personal access tokens, within the token's scope. The Owner's administration needs a browser session or a token with the administrator scope (`admin: true`); through a token without it, an Owner acts as a Maintainer. Service-account and CI tokens never carry a role.
- A publish through a role makes the publishing user the package owner, as a grant does.
- A project keeps at least one Owner: removing or demoting the last one answers `409 last_owner`, unless the caller is an administrator of trove, who may leave a project without an Owner and administers it alone.

The members are read with `GET /api/v1/projects/<projectKey>/members` and written with one request per member:

```sh
# Add a member or change its role: 201 when it became a member, 200 when its role was set.
curl -X PUT -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"role": "developer"}' \
  https://trove.example.com/api/v1/projects/web/members/group:web-devs

# Remove a member: 204.
curl -X DELETE -H "Authorization: Bearer $TOKEN" \
  https://trove.example.com/api/v1/projects/web/members/user:dave
```

A member is named `user:<name>` or `group:<name>`; the role is `reader`, `developer`, `maintainer` or `owner` (`422 invalid_role` otherwise). Adding, changing and removing a member need a recent sign-in, and take effect on every replica at once.

## What an Owner may do

| Area | Allows, in the project and nowhere else |
|---|---|
| Members | Add users and groups, change their roles and remove them. Look up users and groups by the start of their name with `GET /api/v1/projects/<projectKey>/principals?q=` (at least two characters, at most 20 matches, kind and name only) |
| Repositories | Create, change and delete the project's repositories of every class: local, remote and virtual. Read their full configuration, never a secret value. Test the saved and the unsaved upstream of a remote repository. Move a repository to another project of which it is Owner too |
| Permission policies | Create, change and delete the project's policies, granting any action to users, groups, the project's service accounts, service accounts of no project and CI mappings, never to `anonymous`, `authenticated` or an automatically joined group |
| Service accounts | Create, change, disable, enable and delete the project's service accounts; issue and revoke their tokens within the limits below |
| CI mappings | Manage the GitLab CI mappings of the tier `project:<projectKey>`, dry-run them and read the issuances of tokens through them ([GitLab CI federation](gitlab-ci.md)) |
| Settings | Change the display name and the description (`PUT /api/v1/projects/<projectKey>` with `If-Match`) |

An Owner cannot create or delete projects, bind system roles, see global permission policies (unless it holds the permission to read policies), see or change anything of a project in which its role does not allow it, create users, groups or service accounts of no project, or add an entry for `anonymous` or `authenticated`.

The system roles act in every project as they do outside projects: `admin` and `repository-admin` create, change, delete and move the repositories of every project; `admin`, `security-admin`, `repository-admin` and `auditor` read every project with its members and roles. The `auditor` sees every project, its policies, service accounts and repositories, and changes nothing.

## Repositories of a project

A repository is created in its project: `POST /api/v1/projects/<projectKey>/repositories`, with the key of the repository in the body. Its key matches `^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*$` (2 to 64 characters) and is unique within the project; a key the project already holds answers `409 already_exists`. The members and the deployment target of a virtual repository are repositories of the same project, named by their bare keys; a repository of another project cannot be a member (`invalid_member`).

### Remote repositories of an Owner

An Owner without the permission to change repository configuration sets up the project's remote repositories and tests their upstream. Three things stay with the administrators of trove, so that a project's remote repository never sends a secret of the installation to a host the project chose:

- **Passwords and tokens are entered, not referenced.** The Owner types the upstream password or token itself. A reference to a file or variable of the installation (`secretRef`) is refused, in a save and in the upstream test of an unsaved configuration (`secret_value_required`). A reference that an administrator of trove set stays when the repository is saved unchanged.
- **A stored password does not follow a new address.** Changing the upstream **URL**, adding an extra host that receives credentials, or turning on forwarding credentials on redirects needs the password or token again in the same save (`secret_value_required`).
- **The outbound path is the installation's.** A project's remote repository uses the installation's outbound proxy (mode **default**) and no source network of its own (`outbound_path_restricted`).

The mirror settings of a remote repository in mode `mirror` stay as an administrator of trove set them (`mirror_restricted`); the Owner can pause and resume the mirror and turn the mode back to `pullThrough`.

### Move a repository to another project

A repository changes project only through a move:

```sh
curl -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"project": "mobile"}' \
  https://trove.example.com/api/v1/projects/web/repositories/npm-libs/move
```

- An administrator of trove (or a holder of the permission to change repository configuration) moves to any project; an Owner moves only between two projects of which it is Owner. A target project the caller is no member of answers `404`, a target in which its role is below Owner `403`. The move needs a recent sign-in.
- The answer is `200` with the repository document at its new address (`Location: /api/v1/projects/mobile/repositories/npm-libs`).
- **Every address of the repository changes.** From the move on, `…/api/npm/web/npm-libs/` answers `404` and `…/api/npm/mobile/npm-libs/` serves the same packages; update the registry URL of every client. Content, packages, claims, package owners, findings and mirror state stay with the repository; nothing is copied.
- The roles and policies of the source project stop reaching the repository, those of the target project reach it. A global policy that names the repository by its key follows it and reads back the new name; a pattern such as `web/*` no longer matches it.
- **Nothing of the source project may name it.** While a virtual repository of the source project lists it as a member, a pin rule or its deployment target, a policy of the source project names its key, or a CI mapping of the source project names it in an inline grant, the move answers `409 repository_in_use` with the list of the referencing objects in `references`. Remove the references and move again; trove rewrites nothing for you. A virtual repository moves only without members and deployment target.
- A target project that already has the key answers `409 already_exists`, also when that repository is being deleted.

## Permission policies of a project

A project policy is addressed under its project, `/api/v1/projects/<projectKey>/policies/<name>`, and covers the repositories of its project only. Its selectors name repositories by their bare key, a key pattern or a class:

| Selector | Matches |
|---|---|
| `npm-internal` | The repository `npm-internal` of the project; a key the project does not hold answers `422 unknown_repository` |
| `npm-*`, `npm-*:npm` | The project's repositories whose key matches, optionally of one format |
| `@any-local`, `@any-remote`, `@any-virtual`, `@any` | The project's repositories of the class |

A selector with a `/`, such as `mobile/npm-internal`, answers `422 invalid_pattern`: a project policy never reaches another project. Global policies keep applying to the project's repositories, and they name them qualified, such as `web/npm-internal` or `web/npm-*`; the effect is the union of global policies, project policies and roles ([Permissions](permissions.md)).

The entries of a project policy may name users, groups, service accounts of no project, CI mappings and the service accounts of the same project. A service account of another project is refused (`principal_outside_project`), and so are `anonymous`, `authenticated` and an automatically joined group.

## Service accounts of a project

A project service account is addressed as `/api/v1/projects/<projectKey>/service-accounts/<name>` and referenced as `sa:<projectKey>/<name>`, for example `sa:web/partner` in a policy entry. Its project is fixed at creation. A service account of no project is referenced as `sa:<name>` and is created only by an administrator of trove.

A project service account is granted only by the policies of its project. It cannot be a group member (`project_account_not_groupable`), a project member (`principal_not_allowed`) or hold a system role (`role_not_allowed`), and a global policy or another project's policy cannot name it (`principal_outside_project`), so no grant of a global group ever reaches it.

An Owner issues its tokens with `POST /api/v1/projects/<projectKey>/service-accounts/<name>/tokens`, with these limits:

- The token always expires, at most after the maximum lifetime of service-account tokens (see [Managing tokens](tokens.md#lifetime-limits-and-defaults)).
- The token has no administrator scope and names no system item (`invalid_scope`).
- The token can be bound to endpoints, as any service-account token.
- The token cannot rotate itself: a request with `selfRotatable: true` is refused, because each rotation would give it its full lifetime again. Issue a new token when it nears its end.

Disabling the account makes all of its tokens fail. Deleting it removes its tokens.

## Create and delete a project

An administrator of trove creates a project with `POST /api/v1/projects`:

```json
{
  "key": "web",
  "displayName": "Web shop",
  "description": "Packages of the web shop team",
  "owners": ["group:web-leads"]
}
```

`key` and `displayName` are required; `description` defaults to the empty string. `owners` names the users and groups that become the first Owners in the same transaction; without it the project has no Owner, and an administrator of trove adds one as a member. A key that exists answers `409 already_exists`.

`GET /api/v1/projects` lists every project to a holder of the permission to read projects, and otherwise the projects the caller is a member of, each with the caller's role. `GET /api/v1/authz/me` lists the caller's memberships under `projects`, with the role in force for the credential.

Only an administrator of trove deletes a project, and only when no repository, in any state, no service account and no CI mapping belongs to it (`409 project_not_empty`). The project's permission policies are deleted with it, each through the policy store, and its member entries go.

## The Projects screen

**Admin area › Projects** lists the projects you can see, with the admin area's other lists, each with the number of repositories and the **Storage** it is charged. An administrator of trove and the holders of the permission to read projects see every project, the `auditor` read-only; a member without any system role finds the projects it is a member of on the start page and has no admin area. Select a project's key to open it.

## Where the project's screens are

The project is the context of its screens. Open it from the start page or with the project switcher at the top of any project's side bar; its address is `/projects/<key>/…`. The side bar of a project holds **Packages**, **Browse**, **Repositories**, **Vulnerabilities**, **Members**, **Permission policies**, **Service accounts**, **CI identity** and **Settings**, each only if your role or permission allows it. The top bar has no project filter. **New policy** in a project's **Permission policies** creates a policy of that project; **New policy** in the admin area's list creates a global policy. The policy editor says which one it is: **Global policy — applies to every repository**, or **Project policy of** the key. The admin area's lists (repositories, service accounts, permission policies, audit log) name the project of each item in a **Project** column.

## For a member without a system role

A member has no admin area. After you sign in you see the start page or, with one project, that project; its side bar holds the screens your role allows, among them **Members**, **Permission policies**, **Service accounts**, **CI identity** and **Settings** for an Owner. The global lists, such as **Users**, are not offered.

## When an action is refused

| Code | What it means |
|---|---|
| `404` on a project or below it | The project does not exist, or you are no member of it |
| `403` on a project you are a member of | Your role is too low for the action, or you act as Owner through a token without the administrator scope |
| `last_owner` | The member is the last Owner; add another Owner first, or ask an administrator of trove |
| `principal_not_allowed` | Only users and groups are members; a service account, a CI mapping, `anonymous`, `authenticated` or an automatically joined group is not |
| `already_exists` | The project key, a repository key in the project, or a policy name is taken; a move whose target project already has the key |
| `repository_in_use` | A move while something of the source project names the repository; `references` lists what |
| `unknown_repository` | A selector of a project policy names a key the project does not hold |
| `invalid_pattern` | A selector of the wrong form: a `/` in a project policy, a bare key in a global policy |
| `invalid_member` | A member or deployment target of a virtual repository that is not a repository of the same project |
| `principal_outside_project` | A service account of another project, or `anonymous`, `authenticated` or an automatically joined group, in a project policy; a project service account in a global policy |
| `project_account_not_groupable` | A project service account as a group member |
| `project_not_empty` | The project still has repositories, service accounts or CI mappings |
| `secret_value_required` | A reference of the installation, or a stored password kept while the URL or a credential host changes |
| `outbound_path_restricted` | A direct connection or a source network chosen by an Owner without the permission to change repository configuration |
| `mirror_restricted` | A change of the mirror settings that stays with an administrator of trove |
| `vulnerability_weakening_refused` (`403`) | Switching `vulnerabilities.enabled` off, matching fewer public advisories, turning `blockMalicious` off or `onMissingData` from `block` to `allow` lifts download blocks, which needs the permission to write configuration or the triage capability; an Owner may make the scanning stricter |
| `repository_referenced` | A delete while a virtual repository of the project still lists the repository; delete with `detach=true` to remove it from the virtual repositories in the same step |

## Limits of this version

- The four roles are fixed. Custom project roles, environments and repository sharing do not exist. A project's storage is shown, not limited: there is no project quota, and files in the trash are not counted separately because the trash can does not exist yet.
- Delegating CI mappings to a group outside a project does not exist.
- The lookup of users and groups finds any name by its start, so an Owner can enumerate names by prefix; it returns no e-mail address and no membership.
