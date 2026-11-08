# Permissions

This page explains who may do what in trove. Repository content is governed by the roles of a project's members and by permission policies that give principals actions on repositories, package names and paths; administration is governed by fixed system roles that bundle capabilities and by the Owner role of a project. The page describes how a request is decided, how fast a change reaches every replica, the screens that edit policies and roles, and three typical setups.

## The model in brief

- **Deny by default.** A fresh installation has no project, no member and no policy, so nobody but an administrator can read or write content.
- **Allow only.** A role and a policy only add rights. Nothing in another policy can take them away; only an exclude pattern of the same policy, a token's scope and an endpoint's exposure rules narrow them.
- **No action implies another.** `deploy` does not include `read`, `manage` does not include `deploy`.
- **Every repository belongs to one project.** Its address names the project and the repository, `web/npm-internal`, and the key is unique within the project only. The members of a project hold a role there, which gives actions on every repository of the project ([Members and roles](projects.md#members-and-roles)).
- **Content and administration are separate.** Roles and policies decide repository actions. System roles decide the administration of the installation, such as creating projects or editing users; the Owner role of a project decides the administration of that project. The one bridge is the `admin` role, which may do every repository action as well.

## Principals

| Principal | Reference | Who it is |
|---|---|---|
| User | `user:<username>` | A person who signs in, or the break-glass account |
| Group | `group:<name>` | Every member of the group, at the moment of the request |
| Service account | `sa:<name>`; `sa:<projectKey>/<name>` for a service account of a project | The account and every token it owns |
| CI mapping | `ci:<issuer>/<mapping>`; `ci:<issuer>/<projectKey>/<mapping>` for a mapping of a project | Every CI token the mapping contributed to ([GitLab CI federation](gitlab-ci.md)) |
| All signed-in users | `authenticated` | Every user; never a service account |
| Anonymous | `anonymous` | Requests without a credential |

A request acts as its principal, its groups and, for a user, `authenticated`, with the roles the user and its groups hold in the repository's project. Groups and project memberships are looked up for every request, so a change applies without a new token. A service account, a CI token and `anonymous` hold no role in any project; they reach a project's repositories through policies. Policies store the principal's internal ID: a renamed user keeps its rights, and a user deleted and created again under the same name has none. Users, groups and service accounts are described in [Sign-in and identities](sign-in-and-identity.md).

## Resources

A policy names one resource set: repository selectors and optional package-name and path patterns. A **global policy** belongs to no project and names repositories with their project; a **project policy** belongs to one project and names the project's repositories by their bare key ([Permission policies of a project](projects.md#permission-policies-of-a-project)).

| Selector | In a global policy | In a project policy | Covers |
|---|---|---|---|
| Key | `web/npm-internal` | `npm-internal` | That repository |
| Key pattern | `web/npm-team-*`, `*/npm-*` | `npm-team-*` | Every repository whose project and key match, also ones created later |
| Class | `@any-local`, `@any-remote`, `@any-virtual`, `@any` | the same | Every repository of the class, or every repository: of every project in a global policy, of the project in a project policy |
| Format filter | `@any-local:npm`, `web/npm-*:npm` | `@any-local:npm`, `npm-*:npm` | A pattern or class restricted to one format |

`*` matches any run of characters and `?` one character; neither crosses the `/`, so `w*/npm-*` matches the keys `npm-…` of the projects `w…`. A bare key in a global policy and a selector with a project in a project policy are refused (`invalid_pattern`), and a key that names no repository answers `unknown_repository`; in a project policy the key is looked up in the project only.

Package names and paths each have an **Include** and an **Exclude** list. An empty include list is unrestricted; an exclude always wins inside the policy. In package names, `*` matches any run of characters except `/`, `**` includes `/`, `?` matches one character and `\` makes the next character literal, so `@web/*` matches `@web/button`. In paths, `*` and `?` stay within one segment, `**` as a whole segment matches any number of segments, and a path pattern does not start with `/`.

For npm and NuGet repositories, use package-name patterns: a path pattern can admit a tarball but not the package's metadata, and the editor warns about it. Paths of an npm repository are decided on the package they belong to, also on the file-transfer path. Where you do use path patterns there, they apply to the package protocol exactly as to the file-transfer path: a package's metadata is decided on the path of its package document (npm `@web/button`, NuGet `acme.core/index.json` and `acme.core/.registration/index.json`) and each file on its own path, so `@web/**` or `acme.core/**` covers a whole package, and an exclude such as `@secret/**` hides the package from downloads, metadata and search alike. A pattern finer than a package's folder, such as `@web/button/-/button-2.0.0.tgz` or `acme.core/2.0.0/**`, hides the version whose file it names: the package's metadata in a local repository, the search, the package pages and the vulnerability reports leave that version out, `latest` moves to the newest version left, and a publish, an unpublish, a push or a delete that would write or remove a file the pattern excludes is refused as a whole. Two places still show such a version: the metadata of a remote or virtual repository, and the version's other files through the file-transfer path when the pattern names only its tarball or `.nupkg` (write `@web/button/-/button-2.0.0.*` to cover the provenance bundle too). Package-name patterns, or an endpoint rule's versions, remain the simpler way to withhold versions. NuGet stores its paths in lower case, so a path pattern with an upper-case letter is refused for a policy, a token or a CI grant that covers a NuGet repository; write `acme.core/**`, not `Acme.Core/**`. The editor warns about every path pattern on an npm or NuGet repository.

## Actions

| Action | What it allows in this version |
|---|---|
| `read` | Download and list files and packages, read package metadata, see the repository. On a remote repository, also fetch uncached content when the repository's cache-fill action is `read`. On a virtual repository, use it as an address |
| `annotate` | Reserved for descriptive metadata; no operation of this version requires it |
| `deploy` | Upload new files and publish new versions; change npm dist-tags. On a remote repository whose cache-fill action is `deploy`, fetch uncached content |
| `overwrite` | Replace an existing file with different bytes, together with `deploy`. npm versions can never be replaced |
| `delete` | Delete files, folders, versions and packages (npm unpublish); evict cached content of a remote repository |
| `manage` | Change package owners and publish names owned by others; publish a first version that shadows a package of a remote member; set namespace claims; list the cache of a remote repository and invalidate the cache of a remote or virtual repository |

`manage` does not change repository configuration; that needs the capability `repositories:write-config`. The editor offers presets that expand when the policy is saved: **read** (`read`), **write** (`read`, `annotate`, `deploy`), **maintain** (adds `overwrite`, `delete`) and **full** (adds `manage`).

## How a request is decided

For every request trove checks, in this order:

1. **Anonymous access.** A request without a credential needs anonymous access switched on.
2. **Endpoint ceiling.** The exposure rules of the endpoint the request arrived on admit the repository, package or path, for every caller including administrators. See [Endpoints and exposure rules](endpoints.md).
3. **Require authentication.** A repository with **Require authentication** set refuses requests without a credential.
4. **Virtual gate.** For a virtual repository, the caller needs `read` on the virtual repository itself. Content is then decided on the member that serves it, and a write on the deployment target.
5. **Grants.** The union of the caller's role in the repository's project and of all policy entries, global and of the project, for the caller's principals that cover the repository, package or path and hold the action. The `admin` role passes this step for every action.
6. **Token scope.** A token's scope is intersected with the result; it never grants anything. See [Managing tokens](tokens.md).
7. **Checks that never grant.** Filling a remote cache needs `read` plus the repository's cache-fill action and is never done through a browser session. On a local npm repository with package ownership, a name is changed only by its owners, `manage` holders and administrators; an owner changes the owners through a token with a restricted scope only where that scope lists `manage` on the repository and the name. A first version that a remote member of a virtual repository already serves needs `manage`. See [Managing repositories](repositories.md).

The answer depends on what was refused:

| Situation | Answer |
|---|---|
| The endpoint's exposure rules refused | 404, for every caller |
| No credential, and anything else was refused | 401 |
| A read was refused | 404, the same as for a repository that does not exist |
| A write or a management operation was refused | 403, naming the action |

The 404 for refused reads keeps repository names private. For troubleshooting, set `authz.hide_existence: false` to answer 403 for repositories that exist, or override it per repository with **Hide existence from callers without access**.

## How changes reach every replica

Every replica decides from a compiled copy of all policies, project memberships and role bindings in memory. A change is written to PostgreSQL and the replica that wrote it applies it at once. The other replicas apply it at their next poll of the change log, every second by default and usually within about 100 ms when Valkey is running. A replica that cannot confirm its copy for `authz.cache.max_staleness` (10 seconds by default) stops deciding and answers 503 `authz_stale` until it can. The UI asks the next replica for the state it just wrote, so you see your own change at once.

Deleting or renaming a principal or deleting a repository updates the affected policies in the same step. A removed right stops working within these bounds; revoking a token or ending a session follows the faster bound of [Sign-in and identities](sign-in-and-identity.md#sessions).

## System roles

Roles are fixed; there are no custom roles. A role is bound to a user, a group or a service account, never to `anonymous` or `authenticated`. A browser session exercises every role of its user; a token exercises roles only as its scope allows.

| Role | Capabilities |
|---|---|
| `admin` | All capabilities, including `content:bypass`, which passes every repository action |
| `security-admin` | `identities:read`, `identities:write`, `policies:read`, `policies:write`, `roles:read`, `ci:read`, `ci:write`, `tokens:read`, `tokens:revoke`, `repositories:read-config`, `projects:read`, `settings:read`, `security-settings:write`, `audit:read`, `endpoints:read`, `endpoints:write`, `vulnerabilities:triage` |
| `auditor` | `identities:read`, `policies:read`, `roles:read`, `ci:read`, `tokens:read`, `repositories:read-config`, `projects:read`, `webhooks:read`, `settings:read`, `audit:read`, `endpoints:read`, `content:metadata-read`, `vulnerabilities:triage` |
| `repository-admin` | `repositories:read-config`, `repositories:write-config`, `projects:read`, `endpoints:read` |
| `webhook-admin` | `webhooks:read`, `webhooks:write` |

<details><summary>What each capability opens</summary>

| Capability | Opens |
|---|---|
| `content:bypass` | Every repository action on every repository, within the endpoint ceiling |
| `content:metadata-read` | The metadata of every repository, within the endpoint ceiling: the repository list, the package lists and package pages, versions, the npm package document, the NuGet version list, registration and search, and the folder listings of raw repositories. Never a download (a tarball, a `.nupkg`, a raw file) and no write |
| `vulnerabilities:triage` | Changing the status of vulnerability findings, which sets, lifts or restores download blocks (the vulnerability report is part of the vulnerability scanning, which is being built; the capability is already held by `admin`, `auditor` and `security-admin`). It takes effect together with `read` on the finding's repository or `content:metadata-read`, no repository action grants it, and a token's scope can carry it as a system item |
| `identities:read`, `identities:write` | **Users**, **Groups**, **Service accounts**: read; create, change, disable, delete, end sessions |
| `policies:read`, `policies:write` | **Permission policies**: read and the preview; create, change, delete |
| `roles:read`, `roles:write` | **System roles**: read; bind and unbind. Changing a user or group that holds a role needs `roles:write` too |
| `tokens:read`, `tokens:revoke` | All tokens; revoke the tokens of any user or service account |
| `repositories:read-config`, `repositories:write-config` | **Repositories**: read every configuration; create, change and delete the repositories of every project, and move a repository to another project |
| `endpoints:read`, `endpoints:write` | **Endpoints**: read; change exposure rules |
| `settings:read`, `settings:write` | **System status** and **Settings**: read; change the authentication settings and run maintenance tasks |
| `audit:read` | **Audit log** |
| `projects:read`, `projects:write` | **Projects**: read every project with its members, their roles and its counts; create projects and name their first Owners, change and delete projects, and add, change and remove the members of every project. Only the `admin` role holds `projects:write`. See [Projects and delegated administration](projects.md) |
| `ci:read`, `ci:write` | The CI issuers, mappings and dry run of [GitLab CI federation](gitlab-ci.md), which administers the issuers and mappings under **Admin area › CI identity** |
| `webhooks:read`, `webhooks:write`, `security-settings:write` | Reserved; no screen or operation of this version uses them |

</details>

The **Admin area** of the UI, and the **Admin** button in the top bar that leads to it, appear for anyone who holds at least one capability, and each entry only with its capability. A project's owner without a capability has no admin area: the owner administers the project inside it, from its side bar, as described in [Projects and delegated administration](projects.md). Only the `admin` role unlocks the break-glass account.

The `auditor` reads everything that is not secret and changes nothing: the configuration, users, groups, service accounts, policies, role bindings, token metadata, settings, the audit log, and the metadata of every repository. It downloads no file, publishes, tags and deletes nothing, and the exposure rules of an endpoint apply to it as to everyone. A token of an auditor carries the metadata read only when its scope lists the system item `content:metadata-read`, for example `{"inherit": true, "system": ["content:metadata-read", "repositories:read-config"]}`. To make an identity provider group the auditor from your Helm values, see [Administrators from the Helm values](sign-in-and-identity.md#administrators-from-the-helm-values).

## The Permission policies screen

**Admin area › Access › Permission policies** lists **Name**, **Description**, **Principals**, **Repositories** and **Actions**. A policy that covers no repository any more, for example because its repository was deleted, shows **Grants nothing**. Filter by **Principal** (also lists the policies of the principal's groups) or by **Repository** (the policies whose selectors cover it).

To create a policy, select **New policy**. **New policy** in a project's **Permission policies** creates a policy of that project; **New policy** in the admin area's list creates a global policy, and the editor says **Global policy — applies to every repository**:

1. Under **General**, enter a **Name** (it cannot be changed later) and a **Description**.
2. Under **Principals and actions**, select **Add principal** for each entry, enter a reference such as `group:team-web`, and tick actions or select a **Preset**.
3. Under **Repositories and patterns**, enter **Repository selectors**, one per line, and optionally **Package names** and **Paths** with **Include** and **Exclude**. **Repositories covered now** counts the matches. The **Pattern tester** checks a package name or path against the patterns.
4. Check the **Preview**. It lists errors with **The policy has problems:**, **Warnings**, and **Covered repositories**.
5. Select **Create policy**.

A policy that belongs to a project shows **Project policy of** and the project's key. Its name is unique within the project, so the same name can exist in another project and once as a global policy, and it is addressed as `/api/v1/projects/<projectKey>/policies/<name>`. The Owners of the project create and change it, within the limits of [Permission policies of a project](projects.md#permission-policies-of-a-project), as does a holder of `policies:write`. The list in the admin area names the project of each policy in a **Project** column and can be limited to one project in its filter panel. Editing an existing policy ends with **Review and save**, which shows the differences in **Save these changes?** before **Save policy**. If someone changed the policy meanwhile, a conflict dialog lets you compare and decide. **Delete policy** removes it. Saving and deleting need `policies:write` and a recent sign-in.

The editor refuses an entry for `anonymous` or `authenticated` with any action other than `read`, a policy with an `anonymous` entry that selects repositories by anything but key, and actions other than `read` and `manage` on a virtual repository named by key. It warns, without refusing, when a selector matches no repository yet, when path patterns are used on a package format, and when `anonymous` may read a remote repository, because anyone could then fill its cache from the upstream.

### Why can X do Y

The **Try it** section of the preview answers whether a principal may do an action on a repository under the draft. Enter a **Principal** such as `user:alice`, a **Repository** and an **Action**, and select **Explain**. The answer lists the steps: whether the selectors cover the repository, which entry applies (directly, through a group of the user, or through `authenticated`), and whether it holds the action. `authenticated` applies to users only, as when a request is decided, so an entry for `authenticated` never allows a service account such as `sa:ci-web`. It ends with **Allowed by this draft** or **Not granted by this draft**, and tells you whether you yourself may do this under the stored policies.

The answer evaluates only the draft. Other policies, token scopes, exposure rules and package ownership are not part of it. To answer the question for all stored policies, filter the policy list by the principal and by the repository.

## The System roles screen

**Admin area › Access › System roles** shows every role with its **Capabilities** and the principals that hold it, with **Granted**, **Granted by** and **Source**. To bind a role, enter `user:<name>`, `group:<name>` or `sa:<name>` in **Bind to** and select **Bind**; a service account of a project holds no system role (`role_not_allowed`); **Unbind** removes a holder. Both need `roles:write` and a recent sign-in. The last administrator cannot be removed (`last_admin`) while the break-glass account is disabled, and a group with auto-join cannot hold a role. A binding with the source **Configuration** is declared in `authz.role_bindings` and shows **Managed in the configuration** instead of **Unbind**; change it in your Helm values ([Administrators from the Helm values](sign-in-and-identity.md#administrators-from-the-helm-values)).

## Anonymous and all signed-in users

- `authenticated` holds `read` only. Use it for "every employee may read". Service accounts are not included.
- `anonymous` holds `read` only, on repositories named by key. It takes effect only while anonymous access is switched on and the endpoint admits anonymous requests; see [Sign-in and identities](sign-in-and-identity.md#anonymous-access). The link **Policies that name the anonymous principal** on the settings screen opens the filtered list.
- A repository with **Require authentication, even where anonymous read is granted** refuses anonymous callers whatever the policies say.
- Both appear in global policies only. A project policy refuses them, and an auto-join group, which every user is a member of (`principal_outside_project`); within a project, give users a role instead.

## Typical setups

### Everyone signed in reads everything

| Field | Value |
|---|---|
| **Name** | `everyone-reads` |
| **Repository selectors** | `@any` |
| Entry | `authenticated` with `read` |

This global policy covers the repositories of every project. Add a second one for public content: selector `web/npm-public`, entry `anonymous` with `read`, and switch anonymous access on.

### A team works in its project

An administrator creates the project `web` and names the group `web-leads` as its first Owner. An Owner then adds the group `team-web` as a member with the role `developer`: its members read every repository of `web`, publish to its local repositories and change dist-tags, and cannot delete or overwrite. No policy is needed for that. On an npm local repository the first publish of a name through the role makes the member through which the role came, here the group `team-web`, its owner, so other teams cannot publish it even with a wider grant ([Members and roles](projects.md#members-and-roles)).

A narrower grant inside the project is a project policy, which an Owner writes with `POST /api/v1/projects/web/policies`:

```json
{
  "name": "team-web-publish",
  "description": "team-web publishes @web packages to npm-internal",
  "resources": {
    "repositories": ["npm-internal"],
    "packages": {"include": ["@web/*"], "exclude": []}
  },
  "entries": [
    {"principal": "group:team-web", "actions": ["write"]}
  ]
}
```

The same grant as a global policy, which an administrator writes with `POST /api/v1/permission-policies`, names the repository with its project: `"repositories": ["web/npm-internal"]`. Members still need read on the virtual repository they install from, for example through the policy of the first setup.

### A CI service account of a project

For GitLab CI, prefer the [GitLab CI federation](gitlab-ci.md), which needs no stored token. For other automation:

1. An Owner of `web` creates the service account `ci` in the project (`sa:web/ci`) on the project's page and creates a token for it.
2. A project policy `ci-publish` of `web`: selector `npm-internal`, package include `@web/*`, entry `sa:web/ci` with `read` and `deploy`.
3. A project policy `ci-install` of `web`: selector `@any:npm`, entry `sa:web/ci` with `read`. Service accounts are no members of a project and not part of `authenticated`, so they need their own read grant, also on the members of a virtual repository and on remote repositories they fill.
4. Give the token a scope that names only these repositories and actions, with their project (`web/npm-internal`), so a leaked token can do no more.

A publish through a virtual repository is decided on its deployment target, so `deploy` on `npm-internal` is what the pipeline needs, not `deploy` on the virtual repository. A service account of a project is granted only by its project's policies: a global policy or another project's policy cannot name it (`principal_outside_project`), and it cannot join a group (`project_account_not_groupable`).
