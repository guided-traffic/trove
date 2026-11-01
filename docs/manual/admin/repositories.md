# Managing repositories

This page explains how administrators create, configure, edit and delete repositories. It covers the Repositories screen and its creation wizard, the settings every repository shares, the settings of generic and npm repositories, how npm remote and virtual repositories are wired to their upstream and members, the protection against dependency confusion, and the file operations an administrator has.

## Formats and classes

| Format | Classes | Client address |
|---|---|---|
| Generic | local | `https://trove.example.com/files/<key>/` |
| npm | local, remote, virtual | `https://trove.example.com/api/npm/<key>/` |

A **local** repository holds what is uploaded or published to it. A **remote** repository fetches from an upstream registry and caches what it fetched. A **virtual** repository combines local and remote repositories behind one address. How users see the classes is described in [Repositories](../user/repositories.md).

Viewing configurations needs the capability `repositories:read-config`; creating, changing and deleting needs `repositories:write-config`. Both come with the roles `admin` and `repository-admin`; see [Permissions](permissions.md).

## The Repositories screen

**Administration › Repositories** lists **Key**, **Format**, **Class**, **Description**, **Client address** (with **Copy client address**) and **Last activity**. Badges mark repositories that are **disabled** or **being deleted**. **Filters** narrow the list by **Key or description**, **Class**, **Format** and **State** (**Disabled only**). **Recently viewed** shows the repositories you opened last in this browser.

Each row offers **Edit**, **Browse** and, with `repositories:write-config`, **Delete**. **Create repository** opens the wizard.

## Create a repository

1. Select **Create repository**.
2. **Class**: choose **Local**, **Remote** or **Virtual**. Only classes that an installed format implements are offered.
3. **Format**: choose **Generic** or **npm**; each choice shows its client address.
4. **Basic settings**: enter the **Key**, a **Description** and **Administrator notes**. The wizard checks the key as you type and reports **The key is available.** or **A repository with this key already exists.**
5. **Access settings** (or **Access and npm settings**): set the patterns and switches described below, and the format settings.
6. **Review**: check the summary. **Show as API request** shows the same request as a JSON body and as a curl command.
7. Select **Create repository**. The repository is usable on every replica as soon as the wizard confirms, and its page opens.

The key rules:

- 2 to 64 characters: lower-case letters, digits, `.`, `_` and `-`, starting with a letter or digit; no `..` and no trailing `.`.
- `batch-create`, `batch-update`, `batch-get`, `batch-delete` and `quick-setup` are reserved.
- A key is unique and stays taken until a deleted repository is completely removed.
- Key, class and format cannot be changed after creation.

The wizard creates an npm remote repository with the upstream `https://registry.npmjs.org/` and a virtual repository without members. Configure a different upstream, the members and the deployment target afterwards as described in [Settings that are set through the API](#settings-that-are-set-through-the-api).

## Settings every repository has

| UI label | Document key | Default | Meaning |
|---|---|---|---|
| **Description** | `description` | empty | Shown to everyone who can read the repository; at most 2000 characters |
| **Administrator notes** | `adminNotes` | empty | Shown only to holders of `repositories:read-config`; at most 8000 characters |
| **Include patterns** | `includePatterns` | empty | What the repository admits; empty admits everything |
| **Exclude patterns** | `excludePatterns` | empty | What it refuses; an exclude wins over an include |
| **Disabled: takes no part in resolution, download or deploy** | `disabled` | off | The repository keeps its content but serves and accepts nothing |
| **Require authentication, even where anonymous read is granted** | `requireAuthentication` | off | Refuses requests without a credential whatever the policies say |
| **Hide existence from callers without access** | `hideExistence` | **Use the installation setting** | **Hide: answer as if it did not exist** answers 404 to refused reads, **Do not hide: answer 403** answers 403 |

For a generic repository the patterns match paths; for npm they match package names, such as `@acme/*`. Up to 100 patterns per list; **Pattern syntax** in the form explains `*`, `**`, `?` and `\`. Changes to **Require authentication** and **Hide existence** are recorded as `authz.settings.changed` in the audit log.

## Format settings

The settings of a format are drawn from the schema the format publishes, so the form shows exactly the settings that apply to the repository's class. Each setting is labelled with its name, its description and its default; a setting left at its default is not written into the repository.

| Format and class | Setting | Default | Meaning |
|---|---|---|---|
| Generic local | none | | |
| NuGet local | **deleteMode** | `unlist` | What `dotnet nuget delete` and `nuget delete` do: `unlist` hides the version from search and keeps it downloadable; `delete` removes the version for good and keeps its number blocked. A delete through the API, the UI or `/files/` always removes |
| npm local | **latestGuard** | on | A publish under `latest` never moves `latest` backwards or onto a pre-release in front of a release; the version is stored and `latest` stays |
| npm remote | **serveRemovedVersions** | off | Keep serving cached tarballs of versions the upstream no longer lists; the package documents never list them |
| npm remote | **audit** › **mode** | `forward` | `forward` answers `npm audit` through this remote; `disabled` answers 501 `npm_audit_unavailable` |
| npm remote | **audit** › **providerURL** | the upstream | An `https` URL of another advisory provider |
| npm remote | **audit** › **timeout**, **maxBodySize** | `30s`, `1MiB` | Limits of one audit request |
| npm virtual | **latestStrategy** | `highest-member-latest` | How `latest` is merged: the highest `latest` of the members, or `newest-published` |
| npm virtual | **distTagStrategy** | `member-order` | How other dist-tags are merged: the first member in order, `highest-semver` or `newest-published` |

## Settings that are set through the API

The UI edits the shared settings and the format settings. The upstream of a remote repository, the members of a virtual one, package ownership and namespace claims are set through the management API. The **API** tab of a repository shows the **Stored document** and, after a change in the form, **Your change as a request**.

Read the repository to get its `ETag`, then send a merge patch with `If-Match`:

```bash
# example values: the key, the ETag and the token variable
curl -X PATCH https://trove.example.com/api/v1/repositories/npm-all \
  -H "Authorization: Bearer $TROVE_TOKEN" \
  -H 'Content-Type: application/merge-patch+json' \
  -H 'If-Match: "3"' \
  --data '{"virtual": {"members": ["npm-internal", "npm-registry"], "deployTarget": "npm-internal"}}'
```

The token needs a scope that allows repository configuration; see [Managing tokens](tokens.md). The full document is in the [management API reference](https://github.com/guided-traffic/trove/blob/main/README.md#-full-reference).

### npm remote: the upstream

The `remote` section of an npm remote repository:

| Key | Default | Meaning |
|---|---|---|
| `url` | `https://registry.npmjs.org/` | The upstream; `http` or `https`, no user info; plain `http` is saved with a warning |
| `authMode` | `none` | `none`, `basic` or `bearer` |
| `username`, `password` | empty, none | Credentials for `basic`; `bearer` sends `password` as the token |
| `connectTimeoutMs`, `readTimeoutMs` | `10000`, `15000` | Connection and read timeouts |
| `cacheFillAction` | `read` | The action a caller needs, besides `read`, to fetch content that is not cached; `deploy` freezes the cache for everyone without it |
| `cache.metadataTtlSeconds` | `7200` | How long package metadata is fresh; 0 revalidates on every request |
| `cache.metadataRefreshTimeoutSeconds` | `60` | A slower refresh serves the stored copy |
| `cache.notFoundTtlSeconds` | `1800` | How long an upstream "not found" is remembered; 0 turns it off |
| `cache.maxStaleSeconds` | `0` | How long expired metadata may be served while the upstream fails; 0 is unbounded |
| `availability.mode` | `online` | `offline` sends no upstream request at all |
| `availability.failureThreshold`, `circuitOpenSeconds` | `5`, `300` | After this many failures the upstream is treated as down for this long, then probed once |

<details><summary>Further remote keys</summary>

| Key | Default | Meaning |
|---|---|---|
| `extraHosts` | `[]` | Further hosts that metadata or redirects may point to, at most 20, each with `sendCredentials` |
| `forwardAuthOnRedirect` | `false` | Send credentials to a redirect target |
| `outboundProxy.mode` | `default` | `default` or `direct` |
| `sourceNetwork` | none | A CIDR prefix the upstream requests come from, for your firewall rules |
| `integrity.checksumPolicy` | `verify-if-available` | `verify-if-available`, `require`, `ignore` or `pass-through` |
| `integrity.blockUnexpectedContentTypes`, `allowedContentTypes` | `true`, `[]` | Refuse upstream answers of unexpected content types |
| `limits.maxArtifactBytes`, `maxMetadataBytes` | system value | Largest upstream answer |
| `limits.maxConcurrentFetches` | `64` | Parallel upstream fetches |

</details>

- **Credentials.** `password` is write-only. Send `{"value": "…"}` to store it encrypted, or `{"set": true}` to keep the stored value; a read shows only `{"set": true}`. Omitting `password` from a replaced document deletes it.
- **When the upstream fails.** trove retries once only when the connection failed before anything was sent. Expired metadata that is stored is served within `maxStaleSeconds`; otherwise clients get 502 or 504, and 503 `upstream_assumed_offline` while the upstream is treated as down. In offline mode cached content is served whatever its age and uncached content answers 404 `not_cached_offline`.
- **Versions removed upstream.** A version the upstream no longer lists, and every version of a package the upstream withdrew, answer 404 even when cached, unless **serveRemovedVersions** is on. Each case is recorded as `npm.remote.version_removed`.
- **Changing the upstream.** A change of the URL, the hosts or the credentials discards all cached metadata and remembered misses at once; cached tarballs stay.
- **Testing the connection.** `POST /api/v1/repositories/<key>/upstream-test` checks the saved settings.
- **Invalidating the cache.** `POST /api/v1/repositories/<key>/cache/invalidate` with a path prefix and `scope` (`metadata`, `negative` or `all`) discards cached metadata or remembered misses, never tarballs. It needs `manage` on the repository and is recorded as `repository.cache_invalidated`. `GET /api/v1/repositories/<key>/cache/entries` lists the cache.

### npm virtual: members and the deployment target

| Key | Default | Meaning |
|---|---|---|
| `virtual.members` | `[]` | Keys of local and remote npm repositories, in resolution order, at most 100; a virtual repository cannot be a member |
| `virtual.deployTarget` | `null` | The local member that receives publishes; `null` makes the repository read-only |
| `virtual.mergedMetadataTtlSeconds` | `7200` | How long merged package documents are cached, at most 86400; 0 caches nothing |
| `virtual.pinning.rules` | `[]` | Rules that say from which members a set of package names may come |
| `priorityMember` (in the member's own document) | `false` | Members marked so are consulted first; the others keep their order |

A tarball is looked for in the local members in order, then in the caches of the remote members, then at the upstreams of the remote members the caller may fill. A publish to the virtual repository goes to the deployment target and is decided there; without a target, clients get 405 `no_deploy_target`. A publish to a local member shows in the virtual repository at once. `POST /api/v1/repositories/<key>/cache/invalidate` on a virtual repository drops its merged documents on every replica.

### npm local: package ownership

`local.packageOwnership` is `enforce` by default for npm. The first publish of a name makes the principals of the granting policy entries its owners; after that, only owners, holders of `manage` and administrators may publish, change or delete it (403 `package_owned_by_other_owner`). `off` turns this off. Owners are managed with `GET`, `POST` and `DELETE` on `/api/v1/repositories/<key>/packages/<name>/owners`, which need a recent sign-in; see [Permissions](permissions.md).

## Protection against dependency confusion

When a virtual repository combines a local repository with a remote one, a public package of the same name as an internal one could be resolved instead. trove offers three defences:

- **Namespace claims** of a local repository. A claimed name resolves only from the claimant; the remote members are never asked. Scope `members` applies in the virtual repositories that contain the local repository, scope `global` in every npm virtual repository and, by default, refuses the name at every npm remote repository. Manage claims with `GET` and `PUT /api/v1/repositories/<key>/claims`; `PUT` needs `manage`, and a `global` claim needs `repositories:write-config` as well. With `local.autoClaim` (on by default for npm) the first publish of a name claims it.
- **The shadowing check.** The first version of a name in a local repository is refused with 403 `shadows_remote_package` when a remote member of a virtual repository containing it already serves that name, unless the repository claims the name. A holder of `manage` may publish anyway; this is recorded as `repository.remote_package_shadowed`. If the upstream cannot be asked, the publish answers 503 `shadow_check_unavailable` and should be retried.
- **Pin rules** of a virtual repository restrict package names to listed members.

To resolve a refusal, claim the name on the local repository first, or let a holder of `manage` decide. A virtual repository with local and remote members, no pin rule and no claims carries the warning `dependency_confusion_unprotected`.

## Edit a repository

1. Open the repository from the list or with **Edit**.
2. Change settings in the tabs **General**, **Access** and **npm settings**. The page shows **You have unsaved changes.**
3. Select **Save changes**, or **Discard changes**.

Saving sends only your changes with the version you loaded. If someone saved in between, **Changed by someone else since you opened it** compares **Stored now** with **Yours** and offers **Copy my changes**, **Reload (discard mine)** and **Overwrite**. A repository that is being deleted cannot be changed.

## Delete a repository

1. Select **Delete** in the list or on the repository's page.
2. Confirm **Delete repository** by typing the key.
3. If the repository is a member, deployment target or pin-rule member of a virtual repository, the deletion is refused with **Other repositories still include this one.** In the list, tick **Also remove it from the virtual repositories that include it** to detach it in the same step.

Deleting needs a recent sign-in. A local or remote repository is marked **being deleted** and a background job removes its content and then the repository; the key is free again only when the job has finished. A virtual repository is removed at once. Content is deleted permanently; there is no trash. Exposure rules that name the repository are removed with it.

## The How to use tab

**How to use** shows the setup for this repository, with the **Client** to choose: **npm**, **pnpm**, **Yarn Berry (2 and later)** or **Yarn Classic (1.x)** for npm, **curl** for generic. It explains the class, how to point the client at the repository and sign in, and how to install, publish, manage dist-tags or transfer files, depending on the class. **Open the client setup** leads to the setup assistant described in [Connecting a package client](../user/client-setup.md).

## File operations

Administrators hold every repository action, so the browser and the file-transfer path offer them everything a policy can grant; see [Browsing and transferring files](../user/files.md).

| Operation | Where | Action needed |
|---|---|---|
| Upload a file | **Browse** › **Upload files**, `PUT /files/<key>/<path>` | `deploy`; `overwrite` to replace different bytes |
| Create a folder | **Browse** › **New folder** | `deploy` |
| Download | **Browse** › **Download**, `GET /files/<key>/<path>` | `read` |
| Delete a file or folder | **Browse** › **Delete**, `DELETE /files/<key>/<path>` | `delete` on every item |

Uploads go to generic local repositories only; npm content is published with the npm client. Deleting npm tarballs this way respects package ownership. A folder with many items is deleted by a background job. Only an administrator can remove, through `DELETE /api/v1/repositories/<key>/items`, items that lie outside the include patterns or in a disabled repository. Copy, move, item properties and a trash are not available in this version.
