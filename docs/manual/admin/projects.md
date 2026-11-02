# Projects and delegated administration

A project lets a team administer its own repositories, permission policies and service accounts without a system role. This page explains what a project is, what its administrators may and may not do, the screens for administrators of trove and for project administrators, and what to do when an action is refused.

## What a project is

A project groups repositories and gives a team's administrators a bounded set of rights over them:

- A project has a **Key**, a **Display name** and a **Description**. The key matches `^[a-z][a-z0-9-]{1,31}$` and cannot be changed. No key is another project's key followed by a hyphen (`web` and `web-app` cannot both exist), and no key is the start, up to a hyphen, of the name of a service account that belongs to no project (`project_key_overlap`).
- A repository belongs to at most one project. Its key, address and URL do not change when it is assigned.
- **Administrators** are users and groups. A service account, a CI mapping, `anonymous` and `authenticated` cannot administer a project. They exercise the right through a browser session, or through a token with the administrator scope, as for a system role.
- Two **switches** set by an administrator of trove limit what the project administrators may do: **Manage policies and service accounts** (on by default) and **Manage repositories** (off by default).

Projects are not roles: a project administrator holds no system capability. Everything it may do is limited to its project.

## What project administrators may do

| Right | Allows, in the project and nowhere else |
|---|---|
| Administration (always) | The `manage` action on every repository of the project: package owners, cache invalidation, namespace claims. Read the project with its administrators and counts, its permission policies, the full configuration of its repositories (never a secret value), its service accounts and the metadata of their tokens. Disable and delete the project's service accounts and revoke their tokens, whatever the switches say |
| **Manage policies and service accounts** | Create, change and delete the project's permission policies, granting any action to users, groups, service accounts and CI mappings, but never to `anonymous` or `authenticated`. Create and enable the project's service accounts and issue their tokens. Look up the users and groups to name in policies |
| **Manage repositories** | Create, change and delete the project's repositories of every class, local, remote and virtual. Test the upstream of a remote repository. A remote repository stays subject to the outbound address rules of the installation; the members and the deployment target of a virtual repository must be repositories of the project |

Project administrators cannot bind system roles, see global permission policies, see or change repositories outside their project, assign or unassign repositories, change switches or administrators, create users, groups or service accounts that belong to no project, or add an entry for `anonymous` or `authenticated`.

## Names carry the project key

- A repository that a project administrator creates has a key that starts with the project key and a hyphen, for example `web-libs` in project `web` (`project_prefix_required` otherwise). The key is fixed at creation.
- A permission policy of a project is named `<key>-…`, and cannot change its project.
- A service account of a project is named `<key>-…` and belongs to the project for good. A service account that belongs to no project cannot take a name that starts with the key of a project and a hyphen (`name_reserved_by_project`).

## Permission policies of a project

A project policy covers the repositories of its project only. A key selector must name a repository of the project (`repository_outside_project`; a key that does not exist answers the same); pattern and class selectors such as `@any-local` are evaluated against the project's repositories only. A repository that leaves the project stops being covered at once, and the policy shows the warning that a selector names a repository that left the project. Global policies keep applying to project repositories; the effect is the union.

The entries of a project policy may name users, groups, service accounts that belong to no project, CI mappings and the service accounts of the same project. A service account of another project is refused (`principal_outside_project`).

## Service accounts of a project

A project service account is granted only by the policies of its project. It cannot be a group member (`project_account_not_groupable`) or hold a system role, and a policy of another project or a global policy cannot name it, so no grant of a global group ever reaches it. A project administrator issues its tokens with these limits:

- The token always expires, at most after the maximum lifetime of service-account tokens (see [Managing tokens](tokens.md#lifetime-limits-and-defaults)); **Never expires** is not offered.
- The token has no administrator scope and names no system item.
- The token can be bound to endpoints, as any service-account token.

Disabling the account makes all of its tokens fail. Deleting it removes its tokens.

## The Projects screen

**Administration › Projects** lists **Key**, **Display name**, **Administrators**, **Repositories** and the **Switches** of each project, with a filter on key or name. An administrator of trove or a holder of the permission to read projects sees every project, the `auditor` read-only; a project administrator without any system role sees the projects it administers.

### Create or edit a project

Only an administrator of trove (the `admin` role) creates and changes projects. Select **New project**, or **Edit** on a row:

1. Enter the **Key** (it cannot be changed later), the **Display name** and the **Description**.
2. Set **Manage policies and service accounts** and **Manage repositories**.
3. Under **Administrators**, enter at least two characters of a user or group name in **Find a user or group** and select the match. Remove an administrator with the button beside it.
4. Under **Repositories**, choose **Add a repository**. A repository that belongs to another project is listed with that project, and the dialog shows that it moves; choosing it is the confirmation.
5. Select **Create project** or **Save project**.

Saving sends the changes one by one: the project, then its administrators, then its repositories. If a step fails, the dialog stays open on the rest and the steps that succeeded are not repeated. Changing the project itself needs a recent sign-in and carries the version you opened it with; if someone else changed it meanwhile, the conflict dialog shows both versions. Taking a switch away or removing an administrator or a repository takes effect on every replica at once.

### Delete a project

**Delete project** is offered only for a project that has no repository, in any state, and no service account. The project's permission policies are deleted with it, each through the policy store, and its administrator entries go. For a project that is not empty, the button is disabled and the page says why; a project that became non-empty after you opened the page answers `project_not_empty`.

## The project page

Select a project's key to open it. The page shows the overview, with the switches and administrators read-only for a project administrator, and three sections:

- **Repositories** lists the project's repositories. **Create repository** opens the creation wizard with the project and the key prefix filled in. It needs the permission to change repository configuration, or the switch **Manage repositories**. A repository's own screen shows its project and lets a project administrator with the switch change and delete it.
- **Permission policies** lists the project's policies. **Create policy** opens the policy editor for the project: the name starts with the key, the repositories of the project are offered as selectors, and **Find a user or group** looks up principals by the start of their name (at least two characters, at most 20 matches, kind and name only) and adds the match as a new entry. It needs the permission to change policies, or the switch **Manage policies and service accounts**.
- **Service accounts** lists the project's accounts. **Create service account** starts the name with the key. Open an account to issue its tokens, disable, enable, delete it or revoke tokens.

An action whose switch is off is shown disabled, with the reason beside it. Enabling a disabled service account needs the switch **Manage policies and service accounts**; disabling, deleting and revoking do not.

## The project filter in the top bar

When at least one project exists and you administer one or may read projects, the top bar shows a **Project filter** menu. The selection is stored as your preference and limits the repository list and the policy list to the project (`project=`). **All projects** removes it. A project that no longer exists, or that you cannot see, filters nothing.

## For a project administrator without a system role

After you sign in, **Administration** shows **Projects** only. The global lists, such as **Permission policies**, **Users** and **Service accounts**, are not offered; a project's own policies and service accounts are on its page.

## When an action is refused

| Message | What it means |
|---|---|
| This is switched off for the project | The switch of the project is off. An administrator of trove can switch it on |
| The project does not exist, or you do not administer it | A project the caller does not administer answers as if it did not exist |
| The name must start with the key of the project and a hyphen | `project_prefix_required` for a repository, policy or service account |
| A repository named here does not belong to this project, or does not exist | `repository_outside_project`: a selector, a virtual member or a deployment target |
| A service account of a project can only be granted by the policies of its own project | `principal_outside_project` |
| The project still has repositories or service accounts | `project_not_empty`: unassign the repositories and delete the service accounts first |
| The repository belongs to another project | `repository_assigned`: move it explicitly |

## Limits of this version

- Project roles, global roles, environments, repository sharing and project quotas do not exist. A project has two switches and one kind of administrator.
- CI mapping delegation is independent of projects; an administrator of trove can name a project's administrator group as the delegate of a mapping.
- The lookup of users and groups finds any name by its start, so a project administrator can enumerate names by prefix; it returns no e-mail address and no membership.
