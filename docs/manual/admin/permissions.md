# Permissions

This page explains who may do what in trove. Repository content is governed by permission policies that give principals actions on repositories, package names and paths; administration is governed by fixed system roles that bundle capabilities. The page describes how a request is decided, how fast a change reaches every replica, the screens that edit policies and roles, and three typical setups.

## The model in brief

- **Deny by default.** A fresh installation has no policy, so nobody but an administrator can read or write content.
- **Allow only.** A policy only adds rights. Nothing in another policy can take them away; only an exclude pattern of the same policy, a token's scope and an endpoint's exposure rules narrow them.
- **No action implies another.** `deploy` does not include `read`, `manage` does not include `deploy`.
- **Content and administration are separate.** Policies decide repository actions. System roles decide administration, such as creating repositories or editing users. The one bridge is the `admin` role, which may do every repository action as well.

## Principals

| Principal | Reference | Who it is |
|---|---|---|
| User | `user:<username>` | A person who signs in, or the break-glass account |
| Group | `group:<name>` | Every member of the group, at the moment of the request |
| Service account | `sa:<name>` | The account and every token it owns |
| All signed-in users | `authenticated` | Every user; never a service account |
| Anonymous | `anonymous` | Requests without a credential |

A request acts as its principal, its groups and, for a user, `authenticated`. Groups are looked up for every request, so a membership change applies without a new token. Policies store the principal's internal ID: a renamed user keeps its rights, and a user deleted and created again under the same name has none. Users, groups and service accounts are described in [Sign-in and identities](sign-in-and-identity.md).

## Resources

A policy names one resource set: repository selectors and optional package-name and path patterns.

| Selector | Example | Covers |
|---|---|---|
| Key | `npm-internal` | That repository |
| Key pattern | `npm-team-*` | Every repository whose key matches, also ones created later |
| Class | `@any-local`, `@any-remote`, `@any-virtual`, `@any` | Every repository of the class, or every repository |
| Format filter | `@any-local:npm`, `npm-*:npm` | A pattern or class restricted to one format |

Package names and paths each have an **Include** and an **Exclude** list. An empty include list is unrestricted; an exclude always wins inside the policy. In package names, `*` matches any run of characters except `/`, `**` includes `/`, `?` matches one character and `\` makes the next character literal, so `@web/*` matches `@web/button`. In paths, `*` and `?` stay within one segment, `**` as a whole segment matches any number of segments, and a path pattern does not start with `/`.

For npm repositories, use package-name patterns: a path pattern can admit a tarball but not the package's metadata, and the editor warns about it. Paths of an npm repository are decided on the package they belong to, also on the file-transfer path.

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
5. **Grants.** The union of all policy entries for the caller's principals that cover the repository, package or path and hold the action. The `admin` role passes this step for every action.
6. **Token scope.** A token's scope is intersected with the result; it never grants anything. See [Managing tokens](tokens.md).
7. **Checks that never grant.** Filling a remote cache needs `read` plus the repository's cache-fill action and is never done through a browser session. On a local npm repository with package ownership, a name is changed only by its owners, `manage` holders and administrators. A first version that a remote member of a virtual repository already serves needs `manage`. See [Managing repositories](repositories.md).

The answer depends on what was refused:

| Situation | Answer |
|---|---|
| The endpoint's exposure rules refused | 404, for every caller |
| No credential, and anything else was refused | 401 |
| A read was refused | 404, the same as for a repository that does not exist |
| A write or a management operation was refused | 403, naming the action |

The 404 for refused reads keeps repository names private. For troubleshooting, set `authz.hide_existence: false` to answer 403 for repositories that exist, or override it per repository with **Hide existence from callers without access**.

## How changes reach every replica

Every replica decides from a compiled copy of all policies and role bindings in memory. A change is written to PostgreSQL and the replica that wrote it applies it at once. The other replicas apply it at their next poll of the change log, every second by default and usually within about 100 ms when Valkey is running. A replica that cannot confirm its copy for `authz.cache.max_staleness` (10 seconds by default) stops deciding and answers 503 `authz_stale` until it can. The UI asks the next replica for the state it just wrote, so you see your own change at once.

Deleting or renaming a principal or deleting a repository updates the affected policies in the same step. A removed right stops working within these bounds; revoking a token or ending a session follows the faster bound of [Sign-in and identities](sign-in-and-identity.md#sessions).

## System roles

Roles are fixed; there are no custom roles. A role is bound to a user, a group or a service account, never to `anonymous` or `authenticated`. A browser session exercises every role of its user; a token exercises roles only as its scope allows.

| Role | Capabilities |
|---|---|
| `admin` | All capabilities, including `content:bypass`, which passes every repository action |
| `security-admin` | `identities:read`, `identities:write`, `policies:read`, `policies:write`, `roles:read`, `ci:read`, `ci:write`, `tokens:read`, `tokens:revoke`, `repositories:read-config`, `projects:read`, `settings:read`, `security-settings:write`, `audit:read`, `endpoints:read`, `endpoints:write` |
| `auditor` | `identities:read`, `policies:read`, `roles:read`, `ci:read`, `tokens:read`, `repositories:read-config`, `projects:read`, `webhooks:read`, `settings:read`, `audit:read`, `endpoints:read`, `content:metadata-read` |
| `repository-admin` | `repositories:read-config`, `repositories:write-config`, `projects:read`, `endpoints:read` |
| `webhook-admin` | `webhooks:read`, `webhooks:write` |

<details><summary>What each capability opens</summary>

| Capability | Opens |
|---|---|
| `content:bypass` | Every repository action on every repository, within the endpoint ceiling |
| `content:metadata-read` | The metadata of every repository, within the endpoint ceiling: the repository list, the package lists and package pages, versions, the npm package document, the NuGet version list, registration and search, and the folder listings of raw repositories. Never a download (a tarball, a `.nupkg`, a raw file) and no write |
| `identities:read`, `identities:write` | **Users**, **Groups**, **Service accounts**: read; create, change, disable, delete, end sessions |
| `policies:read`, `policies:write` | **Permission policies**: read and the preview; create, change, delete |
| `roles:read`, `roles:write` | **System roles**: read; bind and unbind. Changing a user or group that holds a role needs `roles:write` too |
| `tokens:read`, `tokens:revoke` | All tokens; revoke the tokens of any user or service account |
| `repositories:read-config`, `repositories:write-config` | **Repositories**: read every configuration; create, change, delete, and assign repositories to projects |
| `endpoints:read`, `endpoints:write` | **Endpoints**: read; change exposure rules |
| `settings:read`, `settings:write` | **System status** and **Settings**: read; change the authentication settings and run maintenance tasks |
| `audit:read` | **Audit log** |
| `projects:read`, `projects:write` | **Projects**: read every project with its administrators and counts; create, change and delete projects, and set their switches and administrators. Only the `admin` role holds `projects:write`. See [Projects and delegated administration](projects.md) |
| `ci:read`, `ci:write`, `webhooks:read`, `webhooks:write`, `security-settings:write` | Reserved; no screen or operation of this version uses them |

</details>

The **Administration** area of the UI appears for anyone who holds at least one capability, and each entry only with its capability. It also appears for an administrator of a project who holds no capability, with the **Projects** screen only: a project administrator is not a role but a right on one project, described in [Projects and delegated administration](projects.md). Only the `admin` role unlocks the break-glass account.

The `auditor` reads everything that is not secret and changes nothing: the configuration, users, groups, service accounts, policies, role bindings, token metadata, settings, the audit log, and the metadata of every repository. It downloads no file, publishes, tags and deletes nothing, and the exposure rules of an endpoint apply to it as to everyone. A token of an auditor carries the metadata read only when its scope lists the system item `content:metadata-read`, for example `{"inherit": true, "system": ["content:metadata-read", "repositories:read-config"]}`. To make an identity provider group the auditor from your Helm values, see [Administrators from the Helm values](sign-in-and-identity.md#administrators-from-the-helm-values).

## The Permission policies screen

**Administration › Access › Permission policies** lists **Name**, **Description**, **Principals**, **Repositories** and **Actions**. A policy that covers no repository any more, for example because its repository was deleted, shows **Grants nothing**. Filter by **Principal** (also lists the policies of the principal's groups) or by **Repository** (the policies whose selectors cover it).

To create a policy, select **New policy**:

1. Under **General**, enter a **Name** (it cannot be changed later) and a **Description**.
2. Under **Principals and actions**, select **Add principal** for each entry, enter a reference such as `group:team-web`, and tick actions or select a **Preset**.
3. Under **Repositories and patterns**, enter **Repository selectors**, one per line, and optionally **Package names** and **Paths** with **Include** and **Exclude**. **Repositories covered now** counts the matches. The **Pattern tester** checks a package name or path against the patterns.
4. Check the **Preview**. It lists errors with **The policy has problems:**, **Warnings**, and **Covered repositories**.
5. Select **Create policy**.

A policy that belongs to a project shows **Project policy of** and the project's key, and is named `<key>-…`; administrators of the project create and change it, within the limits of [Projects and delegated administration](projects.md), with the permission `policies:write` or with the project's switch **Manage policies and service accounts**. The **Project filter** in the top bar limits the list to one project. Editing an existing policy ends with **Review and save**, which shows the differences in **Save these changes?** before **Save policy**. If someone changed the policy meanwhile, a conflict dialog lets you compare and decide. **Delete policy** removes it. Saving and deleting need `policies:write` and a recent sign-in.

The editor refuses an entry for `anonymous` or `authenticated` with any action other than `read`, a policy with an `anonymous` entry that selects repositories by anything but key, and actions other than `read` and `manage` on a virtual repository named by key. It warns, without refusing, when a selector matches no repository yet, when path patterns are used on a package format, and when `anonymous` may read a remote repository, because anyone could then fill its cache from the upstream.

### Why can X do Y

The **Try it** section of the preview answers whether a principal may do an action on a repository under the draft. Enter a **Principal** such as `user:alice`, a **Repository** and an **Action**, and select **Explain**. The answer lists the steps: whether the selectors cover the repository, which entry applies (directly, through a group of the user, or through `authenticated`), and whether it holds the action. `authenticated` applies to users only, as when a request is decided, so an entry for `authenticated` never allows a service account such as `sa:ci-web`. It ends with **Allowed by this draft** or **Not granted by this draft**, and tells you whether you yourself may do this under the stored policies.

The answer evaluates only the draft. Other policies, token scopes, exposure rules and package ownership are not part of it. To answer the question for all stored policies, filter the policy list by the principal and by the repository.

## The System roles screen

**Administration › Access › System roles** shows every role with its **Capabilities** and the principals that hold it, with **Granted**, **Granted by** and **Source**. To bind a role, enter `user:<name>`, `group:<name>` or `sa:<name>` in **Bind to** and select **Bind**; **Unbind** removes a holder. Both need `roles:write` and a recent sign-in. The last administrator cannot be removed (`last_admin`) while the break-glass account is disabled, and a group with auto-join cannot hold a role. A binding with the source **Configuration** is declared in `authz.role_bindings` and shows **Managed in the configuration** instead of **Unbind**; change it in your Helm values ([Administrators from the Helm values](sign-in-and-identity.md#administrators-from-the-helm-values)).

## Anonymous and all signed-in users

- `authenticated` holds `read` only. Use it for "every employee may read". Service accounts are not included.
- `anonymous` holds `read` only, on repositories named by key. It takes effect only while anonymous access is switched on and the endpoint admits anonymous requests; see [Sign-in and identities](sign-in-and-identity.md#anonymous-access). The link **Policies that name the anonymous principal** on the settings screen opens the filtered list.
- A repository with **Require authentication, even where anonymous read is granted** refuses anonymous callers whatever the policies say.

## Typical setups

### Everyone signed in reads everything

| Field | Value |
|---|---|
| **Name** | `everyone-reads` |
| **Repository selectors** | `@any` |
| Entry | `authenticated` with `read` |

Add a second policy for public content: selector `npm-public`, entry `anonymous` with `read`, and switch anonymous access on.

### A team publishes to its own namespace

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

Members of `team-web` read, publish and change dist-tags of `@web/*` packages in `npm-internal`. On an npm local repository the first publish of a name makes the group its owner, so other teams cannot publish it even with a wider grant. Members still need read on the virtual repository they install from, for example through the policy above.

### A CI service account

1. Create the service account `ci-web` under **Administration › Identity › Service accounts** and create a token for it.
2. Create a policy `ci-web-publish`: selector `npm-internal`, package include `@web/*`, entry `sa:ci-web` with `read` and `deploy`.
3. Create a policy `ci-web-install`: selector `@any:npm`, entry `sa:ci-web` with `read`. Service accounts are not part of `authenticated`, so they need their own read grant, also on the members of a virtual repository and on remote repositories they fill.
4. Give the token a scope that names only these repositories and actions, so a leaked token can do no more.

A publish through a virtual repository is decided on its deployment target, so `deploy` on `npm-internal` is what the pipeline needs, not `deploy` on the virtual repository.
