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

A repository can belong to a [project](projects.md). The page of such a repository shows its **Project**, and the **Project filter** in the top bar, or the link **Open in the repository list** on a project's page, limits the list to the repositories of one project. Administrators of trove assign repositories in the project's dialog. The administrators of a project create, change and delete the project's repositories without a system role while the project's switch **Manage repositories** is on: the wizard they open from the project page fills in the project and starts the key with the project key and a hyphen, for example `web-libs` in project `web`, and the key is fixed from then on.

## Create a repository

1. Select **Create repository**.
2. **Format**: choose the format of the repository from the tiles, for example **Generic**, **npm** or **NuGet**. **Search by package type** narrows the tiles by name. The format comes first because it decides what the rest of the wizard can offer. With the keyboard, move to the tiles with Tab, move over them with the arrow keys and choose one with Enter or Space.
3. **Class**: choose **Local**, **Remote** or **Virtual**. A class that the chosen format does not implement is shown unavailable, with the reason, and cannot be chosen. Going back and choosing another format keeps the class when the new format has it and starts the format settings and the mirror section afresh.
4. **Basic settings**: enter the **Key**, a **Description** and **Administrator notes**. The wizard checks the key as you type and reports **The key is available.** or **A repository with this key already exists.**
5. **Access settings** (or **Access and npm settings**, or **Access, npm and mirror settings** for a remote repository of a format that can mirror): set the patterns and switches described below, the format settings and, for a remote, the [mirror](#mirror).
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
| NuGet remote | **forwardSearch** | on | Forward the search of this remote to its upstream. The forwarding is not built yet: the search of a remote answers from the packages it has cached, whatever the setting says |
| NuGet, every class | **vulnerabilityInfo** | `local` while vulnerability scanning is on and the OSV source is not off, else `off` | `local` lists the vulnerability information in the service index and serves trove's own NuGet advisory data once it has been imported, so `dotnet restore` reports known vulnerabilities (NU1901 to NU1904); `off` lists nothing. Passing the upstream's information through (`upstream`) is not built |
| npm local | **latestGuard** | on | A publish under `latest` never moves `latest` backwards or onto a pre-release in front of a release; the version is stored and `latest` stays |
| npm remote | **serveRemovedVersions** | off | Keep serving cached tarballs of versions the upstream no longer lists; the package documents never list them |
| npm, every class | **audit** › **local** | on while vulnerability scanning is on, the OSV source is not off and the repository sets no **mode**; else off | On answers `npm audit` from trove's own advisory data and sends nothing out; the answer is `503 vulnerability_data_unavailable` until the npm advisory data has been imported. With `vulnerabilities.enabled: false`, on answers like `disabled`. Off leaves the answer to **mode**. On wins over **mode** |
| npm, every class | **audit** › **mode** | `forward` (remote and virtual) or `disabled` (local) | Used while **local** is not on. `forward` answers through the first remote that forwards, with that remote's provider (remote and virtual repositories only). `disabled` answers 501 `npm_audit_unavailable`. A repository that sets **mode** and not **local** keeps it |
| npm remote | **audit** › **providerURL** | the upstream | Mode `forward` only: an `https` URL of another advisory provider |
| npm, every class | **audit** › **timeout**, **maxBodySize** | `30s`, `1MiB` | Limits of one audit request; the body limit applies in every mode, the timeout to `forward` only |
| npm virtual | **latestStrategy** | `highest-member-latest` | How `latest` is merged: the highest `latest` of the members, or `newest-published` |
| npm virtual | **distTagStrategy** | `member-order` | How other dist-tags are merged: the first member in order, `highest-semver` or `newest-published` |

## Mirror

A remote repository of a format whose upstream trove can list, for example npm, has a **Mirror** section in the access settings step of the wizard (the fourth step) and a **Mirror** tab on its page. It is shown only for such formats; a format that cannot list its upstream has none.

**Mode** is **Pull-through** (the default: fetch and store what clients ask for) or **Mirror** (also store, in background passes, every package of the upstream that the include patterns and the bounds admit). The other members show in mode **Mirror** only:

| UI label | Document key | Default | Range |
|---|---|---|---|
| **Paused** | `remote.mirror.paused` | off | No pass starts; a running pass stops before its next package |
| **Pass interval in seconds** | `remote.mirror.intervalSeconds` | `3600` | 300 to 604800 |
| **Full pass interval in seconds** | `remote.mirror.fullPassIntervalSeconds` | `604800` | `0`, or 86400 to 7776000 |
| **Discovery** | `remote.mirror.discovery` | **Automatic** | **Automatic**, **Change feed**, **Listing**, **Names list** |
| **Concurrent fetches** | `remote.mirror.concurrency` | `4` | 1 to 64 |
| **Versions per package** | `remote.mirror.maxVersionsPerPackage` | `0` (all) | 0 to 10000 |
| **Include prerelease versions** | `remote.mirror.includePrereleases` | on | |
| **Storage limit in bytes** | `remote.mirror.maxBytes` | empty (no limit of its own) | at least 1048576 |

A member left empty or at its default is not written. Under the mode, a notice says when the mirror has no bound: for the format's well-known upstream (the one the wizard uses, `https://registry.npmjs.org/` for npm) the server refuses to save the mirror unless the include patterns each start with a literal part (such as `@types/*`; not `*`, `**` or `@*/*`) or a storage limit is set; for any other upstream it saves the mirror with a warning. The server decides, and its answer appears at the mode or at the member it names.

Only a holder of `repositories:write-config` sets the mode and the members. An administrator of a project sees the section read-only, can switch **Paused** and select **Turn off** (back to pull-through), and cannot turn a remote repository into a mirror. **Not built in this release yet**: the server still answers `not_applicable` at the mode for a save with **Mirror** until the background passes exist, so the form shows the settings and a repository stays a pull-through cache.

## Settings that are set through the API

The UI edits the shared settings, the format settings and the [mirror](#mirror) of a remote repository. The upstream of a remote repository, the members of a virtual one, package ownership and namespace claims are set through the management API. The **API** tab of a repository shows the **Stored document** and, after a change in the form, **Your change as a request**.

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
| `mode` | `pullThrough` | `pullThrough` or `mirror`: a mirror also stores, in background passes, what the upstream offers within the include patterns and the bounds below. A mirror of `https://registry.npmjs.org/` needs include patterns that each start with a literal part (such as `@types/*`) or `mirror.maxBytes`; only a holder of `repositories:write-config` sets the mode, a project administrator may pause a mirror or turn it back to `pullThrough`. **Not built in this release yet**: the background passes do not run, so a remote repository stays a pull-through cache in either mode; npm accepts `mode: mirror`, a save of NuGet with it is refused with `not_applicable` until its discovery is built |
| `mirror.paused` | `false` | Pause or resume the mirror |
| `mirror.intervalSeconds`, `fullPassIntervalSeconds` | `3600`, `604800` | Time between passes; age of the last full pass after which a pass reads everything again |
| `mirror.discovery` | `auto` | `auto`, `feed`, `listing` or `names` |
| `mirror.concurrency` | `4` | Most background fetches at once; clients always come first |
| `mirror.maxVersionsPerPackage`, `includePrereleases` | `0` (all), `true` | Which versions of a package the mirror stores; the `latest` version always counts |
| `mirror.maxBytes` | none | Stop adding content when the repository holds this many bytes; clients are not limited by it |

</details>

- **Credentials.** `password` is write-only. Send `{"value": "…"}` to store it encrypted, or `{"set": true}` to keep the stored value; a read shows only `{"set": true}`. Omitting `password` from a replaced document deletes it.
- **When the upstream fails.** trove retries once only when the connection failed before anything was sent. Expired metadata that is stored is served within `maxStaleSeconds`; otherwise clients get 502 or 504, and 503 `upstream_assumed_offline` while the upstream is treated as down. In offline mode cached content is served whatever its age and uncached content answers 404 `not_cached_offline`.
- **Versions removed upstream.** A version the upstream no longer lists, and every version of a package the upstream withdrew, answer 404 even when cached, unless **serveRemovedVersions** is on. Each case is recorded as `npm.remote.version_removed`.
- **Changing the upstream.** A change of the URL, the hosts or the credentials discards all cached metadata and remembered misses at once; cached tarballs stay.
- **Testing the connection.** `POST /api/v1/repositories/<key>/upstream-test` checks the saved settings.
- **The mirror's status and a pass now.** `GET /api/v1/repositories/<key>/mirror` shows the state of the mirror (`off`, `paused`, `scheduled`, `running`, `waiting`, `bound_reached` or `failing`), the pass in progress with its counts and job, the last complete pass, the next pass, the last error, the rate-limit waits and the byte bound; it needs `repositories:read-config`, `manage` on the repository or the administration of its project. `POST /api/v1/repositories/<key>/mirror/run` asks for a pass now (`{"full": true}` for a full one) and answers `202` with its job; it answers `409` while a pass is queued or running (`mirror_pass_running`, naming the job), for a remote in mode `pullThrough` (`mirror_off`) and for a paused mirror (`mirror_paused`). Pause and resume are `remote.mirror.paused`, set with `PATCH` and `If-Match`.
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
