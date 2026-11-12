# Managing repositories

This page explains how administrators create, configure, edit and delete repositories. It covers the Repositories screen and its creation wizard, the settings every repository shares, the settings of generic, npm, NuGet, OCI and Debian repositories, how remote and virtual repositories are wired to their upstream and members, the protection against dependency confusion, and the file operations an administrator has.

## Formats and classes

| Format | Classes | Client address |
|---|---|---|
| Generic | local | `https://trove.example.com/files/<projectKey>/<key>/` |
| npm | local, remote, virtual | `https://trove.example.com/api/npm/<projectKey>/<key>/` |
| NuGet | local, remote, virtual | `https://trove.example.com/api/nuget/<projectKey>/<key>/v3/index.json` |
| OCI (Docker) | local, remote, virtual | `trove.example.com/<projectKey>/<key>` as the start of an image reference; the protocol is below `https://trove.example.com/v2/` ([Container image repositories](container-repositories.md)) |
| Debian (APT) | local, remote, virtual | `https://trove.example.com/api/deb/<projectKey>/<key>/` ([Debian (APT) repositories](apt-repositories.md)) |

A **local** repository holds what is uploaded or published to it. A **remote** repository fetches from an upstream registry and caches what it fetched. A **virtual** repository combines local and remote repositories behind one address. How users see the classes is described in [Repositories](../user/repositories.md).

Every repository belongs to exactly one [project](projects.md), and its key is unique within that project. Viewing configurations needs the capability `repositories:read-config`; creating, changing, deleting and moving needs `repositories:write-config`. Both come with the roles `admin` and `repository-admin` and act in every project; see [Permissions](permissions.md). The Owner of a project does the same for the project's repositories without a system role ([Projects](projects.md#what-an-owner-may-do)).

## The Repositories screen

**Admin area › Repositories** lists **Repository** (the key, with its project and the description under it), **Format and class** (the format's icon and name), **Client address** (with **Copy client address**) and **Last activity** (the date; point at it for the time; in a narrower window the column gives its room to the address). Every address field of the list has the same width; where an address is longer than its field, you scroll inside the field (or press End) to read the rest, and the copy button always copies the whole address. In a narrow window each repository is a block of its own, with the address across its full width. Badges mark repositories that are **disabled** or **being deleted**. **Filters** narrow the list by **Key or description**, **Class**, **Format** and **State** (**Disabled only**). **Recently viewed** shows the repositories you opened last in this browser.

The menu at the end of each row (**Actions for** the key) offers **Settings**, **Browse**, **Vulnerabilities** where the format is scanned and, with `repositories:write-config` or as the project's Owner, **Delete**. **Create repository** opens the wizard.

The page of a repository shows its **Project**. A repository is created in its project (`POST /api/v1/projects/<projectKey>/repositories`), and it changes project only through a move, which changes every address of it ([Move a repository to another project](projects.md#move-a-repository-to-another-project)). The list in the admin area names the project of each repository in a **Project** column, its filter panel limits the list to one project, and the project's own **Repositories** screen (`/projects/<key>/repositories`) lists the repositories of that project.

## Create a repository

1. Select **Create repository**.
2. **Format**: choose the format of the repository from the tiles, for example **Generic**, **npm**, **NuGet**, **OCI (Docker)** or **Debian (APT)**. **Search by package type** narrows the tiles by name. The format comes first because it decides what the rest of the wizard can offer. With the keyboard, move to the tiles with Tab, move over them with the arrow keys and choose one with Enter or Space.
3. **Class**: choose **Local**, **Remote** or **Virtual**. A class that the chosen format does not implement is shown unavailable, with the reason, and cannot be chosen. Going back and choosing another format keeps the class when the new format has it and starts the format settings and the mirror section afresh.
4. **Basic settings**: enter the **Key**, a **Description** and **Administrator notes**. The wizard checks the key as you type and reports **The key is available.** or **A repository with this key already exists.**
5. **Access settings** (or **Access and npm settings** for a format with settings of its own): set the patterns and switches described below and the format settings. For a remote repository the step is **Access and upstream settings**, **Access, upstream and npm settings**, or, for a format that can mirror, **Access, upstream and mirror settings** or **Access, upstream, npm and mirror settings**; it adds the [upstream](#the-upstream-of-a-remote-repository) and the [mirror](#mirror).
6. **Review**: check the summary. **Show as API request** shows the same request as a JSON body and as a curl command.
7. Select **Create repository**. The repository is usable on every replica as soon as the wizard confirms, and its page opens.

The key rules:

- 2 to 64 characters: lower-case letters and digits, separated by one `.`, one `_`, two `_` or any number of `-`. A key starts and ends with a letter or digit, so `docker-local`, `docker.hub` and `a__b` are accepted and `docker-`, `a.-b`, `a._b` and `a___b` are refused. The same grammar holds for every format, because the key is part of every container image reference.
- A project key follows the same grammar without `.` and `_`, at most 32 characters, starting with a letter.
- `batch-create`, `batch-update`, `batch-get`, `batch-delete` and `quick-setup` are reserved.
- A key is unique within its project and stays taken there until a deleted repository is completely removed; another project can have a repository of the same key.
- Key, class and format cannot be changed after creation.

The wizard creates a remote repository with the format's well-known upstream unless you enter another (`https://registry.npmjs.org/` for npm, `https://api.nuget.org/v3/index.json` for NuGet, `https://registry-1.docker.io/` for OCI, `https://deb.debian.org/debian/` for Debian) and a virtual repository without members. Configure a different upstream, the members and the deployment target afterwards as described in [Settings that are set through the API](#settings-that-are-set-through-the-api).

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

For a generic repository the patterns match paths; for npm they match package names, such as `@acme/*`; for OCI they match image names, such as `team/*`; for Debian they match binary package names, such as `acme-*`. Up to 100 patterns per list; **Pattern syntax** in the form explains `*`, `**`, `?` and `\`. Changes to **Require authentication** and **Hide existence** are recorded as `authz.settings.changed` in the audit log.

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
| OCI remote | **mirror** › **tags**, **excludeTags**, **platforms** | `latest`, none, every platform | Which tags and platforms a mirror copies; read in mode **Mirror** only ([The mirror mode](container-repositories.md#the-mirror-mode)). OCI local and virtual repositories have no settings of the format |
| Debian local | **architectures**, **compression**, **origin**, **label** | `amd64` and `arm64`, none, `trove`, the repository key | The architectures every suite's indexes are built for, `xz` beside gzip, and the `Origin` and `Label` of every `Release` ([A local repository](apt-repositories.md#a-local-repository)) |
| Debian virtual | **suites**, **components**, and the four settings of a local repository | none, `main` | The suites served besides those of the local members and the components merged ([A virtual repository](apt-repositories.md#a-virtual-repository)) |
| Debian remote | **upstreamKeys** | none | The archive's ASCII-armored public keys; with them trove checks every release before it stores it, and a mirror needs them ([Upstream keys](apt-repositories.md#upstream-keys)). The fingerprints are shown read-only below the settings |
| Debian remote | **mirror** › **suites**, **components**, **architectures**, **translations**, **sources** | none, `main`, `amd64`, `en`, off | What a mirror copies; read in mode **Mirror** only, where **suites** is required ([The mirror mode](apt-repositories.md#the-mirror-mode)) |

## The upstream of a remote repository

A remote repository has an **Upstream** section in the access settings step of the wizard (the fourth step, below the access settings) and a tab **Upstream** on its page. It sets where the repository fetches from:

| UI label | Document key | Default | Meaning |
|---|---|---|---|
| **Upstream URL** | `remote.url` | the format's well-known upstream (`https://registry.npmjs.org/` for npm, `https://api.nuget.org/v3/index.json` for NuGet, `https://registry-1.docker.io/` for OCI, `https://deb.debian.org/debian/` for Debian) | The wizard starts with it filled in; an empty field means it. A format without a well-known upstream needs an address. `http` or `https`, no user information |
| **Authentication** | `remote.authMode` | **None** | **None**, **Username and password** (`basic`) or **Bearer token** (`bearer`) |
| **Username** | `remote.username` | empty | With **Username and password** only |
| **Password or token** | `remote.password` | none | Write-only, see below |
| **Metadata cache period in seconds** | `remote.cache.metadataTtlSeconds` | the format's (npm `7200`, NuGet `300`, OCI `300`, Debian `600`), shown in the hint | How long fetched metadata is served without asking the upstream again; empty is the default |
| **Negative cache period in seconds** | `remote.cache.notFoundTtlSeconds` | the format's (npm and NuGet `1800`, OCI and Debian `300`), shown in the hint | How long an upstream "not found" is remembered; empty is the default |
| **Offline** | `remote.availability.mode` | off (`online`) | The upstream is not contacted; clients get what is already stored |

A member left empty or at its default is not written. The other members of `remote` (extra hosts, limits, integrity, the source network, the remaining cache members) are not in the form; the [API](#npm-remote-the-upstream) sets them, and a save from the form leaves them as they are.

**The password is write-only.** It is sealed when you save and never shown again, anywhere: the field is empty when you open the page, a read of the repository shows `{"set": true}`, and the request that **Show as API request** and the **API** tab display carries `<the password>` in its place. When one is stored, the page says so; leave the field empty to keep it, type one to replace it, or tick **Remove the stored password**. A reference to a secret file or variable that was set through the API shows as such and stays until you type a password. The password is not kept in the browser's storage either: a sign-in that interrupts the form restores everything but the password.

**Test connection** sends the section as it stands in the form, saved or not, to `POST /api/v1/projects/<projectKey>/upstream-tests` and shows the answer under the button, in words: **The upstream answered.**, **The upstream answered and refused the credentials.**, **The upstream answered with an error.** or **The upstream could not be reached.**, then the HTTP status, whether the credentials were **accepted** or **refused**, the duration, the certificate (subject and expiry) and, when the upstream could not be reached, the reason with its code (`dns_error`, `tls_untrusted`, `egress_denied`, `offline`, ...). The result goes when you change a setting. The test uses a password you typed; it does not use the stored one, and the page says so. With **Offline** on the answer is `offline`, because the upstream is not contacted. The permission is the one of saving the repository.

## Mirror

A remote repository of a format whose upstream trove can list (npm, NuGet, OCI and Debian) has a **Mirror** section in the access settings step of the wizard (the fourth step, below the upstream) and a **Mirror** tab on its page. It is shown only for such formats; a format that cannot list its upstream has none.

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

A member left empty or at its default is not written. Under the mode, a notice says when the mirror has no bound; it follows the **Upstream URL** you entered: for the format's well-known upstream (`https://registry.npmjs.org/` for npm, which an empty URL also means) the server refuses to save the mirror unless the include patterns each start with a literal part (such as `@types/*`; not `*`, `**` or `@*/*`) or a storage limit is set; for any other upstream it saves the mirror with a warning. The server decides, and its answer appears at the mode or at the member it names.

Only a holder of `repositories:write-config` sets the mode and the members. An Owner of the repository's project without that capability can switch **Paused** and turn the mode back to pull-through, and cannot turn a remote repository into a mirror or change its settings (`mirror_restricted`). A format that cannot list its upstream answers `not_applicable` at the mode for a save with **Mirror**.

### The status of a mirror

On the page of a remote repository that is stored in mode **Mirror**, the tab **Mirror** starts with **Mirror status**, above the settings. It shows what the server records (`GET /api/v1/projects/<projectKey>/repositories/<key>/mirror`), for everyone who can see the repository's configuration:

| State | Meaning |
|---|---|
| **Off** | The repository is in pull-through mode; no pass runs |
| **Paused** | **Paused** is on: no pass starts |
| **Scheduled** | No pass is in progress; the next one starts when it is due. Green only when the last complete pass was **Complete** |
| **Running** | A pass is in progress |
| **Waiting** | A pass is in progress and waits for its next run, after a long rate-limit wait, a failure or a release |
| **Bound reached** | A storage bound stops the additions: the mirror's own **Storage limit in bytes**, a quota of the repository or the capacity of the storage |
| **Failing** | Three or more passes in a row failed |

The state is a word with an icon; the colour only adds to it. Under it:

- **Pass in progress**: the kind (full or incremental), the discovery method, when it started, the names discovered, the packages done, pending and failed, the items and bytes stored, whether the discovery has finished, and its job. While a pass is in progress (**Running** or **Waiting**) the status is read again every 5 seconds; leaving the tab stops that.
- **Last complete pass**: the outcome (**Complete**, **Partial**, stopped at a storage bound, not run because the repository is offline, or stopped before the end), kind, method, start and end, the counts, and whether the discovery saw everything with the upstream's note when it did not.
- **Next pass**: when it is due, or that it starts at the next dispatch (within a minute), or that none is planned (**Off**, **Paused**); and when the last full pass was.
- **Last error**: the code, the message (it names hosts, never a URL or a credential), the time and the failures in a row.
- **Rate-limit waits**: how many times the passes waited for the upstream, for how many seconds, the status and time of the last answer and, while it waits, until when.
- **Bound**: the storage limit, the bytes stored, and which bound was reached.

With `repositories:write-config`, or with `manage` on the repository (the roles Maintainer and Owner of its project hold it), the status has buttons; a caller who may only read the configuration sees the status without them:

- **Pause** and **Resume** change **Paused** at once, with the version of the repository you loaded; they do not save the other edits in the form, which stay as they are. If someone else changed the repository since you opened it, the conflict dialog opens (**Overwrite** pauses anyway, **Reload** takes the stored version); if they already paused it, nothing more happens.
- **Run a pass now** asks for a pass (`POST /api/v1/projects/<projectKey>/repositories/<key>/mirror/run`) and shows its job: **A pass is queued.** with the job's id, its state and a link **Open the mirror jobs**, which opens the Jobs screen ([System status, Jobs and Audit log](operations.md)) filtered by the kind `mirror-pass`. The arrow next to the button opens **Run a full pass**, which discovers every name again instead of reading the feed from its cursor.
- While a pass is already queued or running the answer is **A pass is already queued or running.** with that job. A paused mirror answers **The mirror is paused. Resume it to run a pass.**, and a repository in pull-through mode says that there is no mirror to run.

## Settings that are set through the API

The UI edits the shared settings, the format settings, the [upstream](#the-upstream-of-a-remote-repository) and the [mirror](#mirror) of a remote repository. The other members of a remote repository (extra hosts, limits, integrity, the source network), the members of a virtual one, package ownership and namespace claims are set through the management API. The **API** tab of a repository shows the **Stored document** and, after a change in the form, **Your change as a request**.

Read the repository to get its `ETag`, then send a merge patch with `If-Match`:

```bash
# example values: the project, the key, the ETag and the token variable
curl -X PATCH https://trove.example.com/api/v1/projects/web/repositories/npm-all \
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
| `integrity.checksumPolicy` | `verify-if-available` | `verify-if-available`, `require`, `ignore` or `pass-through`. An npm remote always compares a tarball with the digest its package document announces and refuses a mismatch with `upstream_integrity_mismatch`, so `ignore` and `pass-through` are refused for it (422 `not_applicable`) |
| `integrity.blockUnexpectedContentTypes`, `allowedContentTypes` | `true`, `[]` | Refuse upstream answers of unexpected content types |
| `limits.maxArtifactBytes`, `maxMetadataBytes` | system value | Largest upstream answer |
| `limits.maxConcurrentFetches` | `64` | Budget of upstream requests of the repository in flight at once. Each replica takes `ceil(budget ÷ n)` of it for the n replicas that fill client misses, so the cluster-wide total stays within `budget + n − 1`, plus the background share B of a mirror (R-REPO-040). A fetch counts until the upstream's answer has been read, not while trove stores it |
| `mode` | `pullThrough` | `pullThrough` or `mirror`: a mirror also stores, in background passes, what the upstream offers within the include patterns and the bounds below. A mirror of `https://registry.npmjs.org/` needs include patterns that each start with a literal part (such as `@types/*`) or `mirror.maxBytes`; only a holder of `repositories:write-config` sets the mode, an Owner of the project without it may pause a mirror or turn it back to `pullThrough` |
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
- **Testing the connection.** `POST /api/v1/projects/<projectKey>/repositories/<key>/upstream-test` checks the saved settings; `POST /api/v1/projects/<projectKey>/upstream-tests` checks settings that are not saved.
- **The mirror's status and a pass now.** `GET /api/v1/projects/<projectKey>/repositories/<key>/mirror` shows the state of the mirror (`off`, `paused`, `scheduled`, `running`, `waiting`, `bound_reached` or `failing`), the pass in progress with its counts and job, the last complete pass, the next pass, the last error, the rate-limit waits and the byte bound; it needs `repositories:read-config`, `manage` on the repository or the role Owner of its project. `POST /api/v1/projects/<projectKey>/repositories/<key>/mirror/run` asks for a pass now (`{"full": true}` for a full one) and answers `202` with its job; it answers `409` while a pass is queued or running (`mirror_pass_running`, naming the job), for a remote in mode `pullThrough` (`mirror_off`) and for a paused mirror (`mirror_paused`). Pause and resume are `remote.mirror.paused`, set with `PATCH` and `If-Match`.
- **Invalidating the cache.** `POST /api/v1/projects/<projectKey>/repositories/<key>/cache/invalidate` with a path prefix and `scope` (`metadata`, `negative` or `all`) discards cached metadata or remembered misses, never tarballs. It needs `manage` on the repository and is recorded as `repository.cache_invalidated`. `GET /api/v1/projects/<projectKey>/repositories/<key>/cache/entries` lists the cache.

### npm virtual: members and the deployment target

| Key | Default | Meaning |
|---|---|---|
| `virtual.members` | `[]` | Bare keys of local and remote npm repositories of the same project, in resolution order, at most 100; a virtual repository cannot be a member, a repository of another project neither (`invalid_member`) |
| `virtual.deployTarget` | `null` | The bare key of the local member that receives publishes; `null` makes the repository read-only |
| `virtual.mergedMetadataTtlSeconds` | `7200` | How long merged package documents are cached, at most 86400; 0 caches nothing |
| `virtual.pinning.rules` | `[]` | Rules that say from which members a set of package names may come |
| `priorityMember` (in the member's own document) | `false` | Members marked so are consulted first; the others keep their order |

A tarball is looked for in the local members in order, then in the caches of the remote members, then at the upstreams of the remote members the caller may fill. A publish to the virtual repository goes to the deployment target and is decided there; without a target, clients get 405 `no_deploy_target`. A publish to a local member shows in the virtual repository at once. `POST /api/v1/projects/<projectKey>/repositories/<key>/cache/invalidate` on a virtual repository drops its merged documents on every replica.

### npm local: package ownership

`local.packageOwnership` is `enforce` by default for npm. The first publish of a name makes the principals of the granting policy entries its owners; after that, only owners, holders of `manage` and administrators may publish, change or delete it (403 `package_owned_by_other_owner`). `off` turns this off. Owners are managed with `GET`, `POST` and `DELETE` on `/api/v1/projects/<projectKey>/repositories/<key>/packages/<name>/owners`, which need a recent sign-in; see [Permissions](permissions.md).

## Protection against dependency confusion

When a virtual repository combines a local repository with a remote one, a public package of the same name as an internal one could be resolved instead. trove offers three defences:

- **Namespace claims** of a local repository. A claimed name resolves only from the claimant; the remote members are never asked. Scope `members` applies in the virtual repositories that contain the local repository, scope `global` in every npm virtual repository and, by default, refuses the name at every npm remote repository. Manage claims with `GET` and `PUT /api/v1/projects/<projectKey>/repositories/<key>/claims`; `PUT` needs `manage`, and a `global` claim needs `repositories:write-config` as well. With `local.autoClaim` (on by default for npm) the first publish of a name claims it.
- **The shadowing check.** The first version of a name in a local repository is refused with 403 `shadows_remote_package` when a remote member of a virtual repository containing it already serves that name, unless the repository claims the name. A holder of `manage` may publish anyway; this is recorded as `repository.remote_package_shadowed`. If the upstream cannot be asked, the publish answers 503 `shadow_check_unavailable` and should be retried.
- **Pin rules** of a virtual repository restrict package names to listed members.

To resolve a refusal, claim the name on the local repository first, or let a holder of `manage` decide. A virtual repository with local and remote members, no pin rule and no claims carries the warning `dependency_confusion_unprotected`.

## After a restore

A restore returns every repository to its configuration and claims at the restore point, and then re-applies the narrowings made
after it, so a name that was claimed or pinned afterwards is not served from a remote member again. What is re-applied: include
patterns narrowed, exclude patterns added, `local.autoClaim` switched on, members taken out of a virtual repository or a pin rule,
pin rules added, a stronger **vulnerabilities** setting, and every claim created or widened to `global`, the claim of a first
publish included. A claim that was created by a publish after the restore point is created again; the version itself is gone with
the restore, so the name has no package until it is published again, and a virtual repository over the claimant asks no remote
member for it meanwhile.

The restore lists each repository it re-applied as `repository.narrowed … applied` or `repository.claims_changed … applied`. A
repository that could have served more than before is **disabled** and listed as `suspended`, with the entry that caused it, in
these cases:

- the include patterns of the restored repository and of the entry share none;
- a member was taken out of a virtual repository, the member is a local repository with claims of scope `members`, and another
  member stays: the claimed names would reach the member that stays;
- a pin rule was added whose pattern overlaps a claim of the virtual repository or another rule;
- a claim was created whose pattern overlaps a claim of another repository that is in effect together with it;
- a pin rule that was reduced is missing from the restored repository.

To review one, read the printed entry (or the replay output of `trove admin post-restore`), check the repository's patterns, members,
pin rules and claims against what you intended, correct them, and enable the repository again.

A restore does not re-apply the settings of how a repository talks to its upstream or what it accepts: the hosts that receive the
credential, the checksum and content-type policy of a remote, offline mode, a local repository's client checksum policy and upload
limits, and the settings of the npm and NuGet members. See [Known limitations](limitations.md#backup-and-restore).

## Members of a virtual repository

A virtual repository has the tab **Members**. **Available repositories** lists the local and remote repositories of its own project and format that are not members yet; **Members in resolution order** lists the members in the order in which they are asked, and the first answer wins. Select repositories and use **Add**; select a member and use **Remove**, **Move up**, **Move down** or **Move to top** (or Alt with the arrow keys or Home). Everything works with the keyboard alone: Tab to a list, arrow keys to move, Space to select, Tab to a button, Enter to press it. A message under the lists announces each change, for example "npm-remote moved to position 2 of 3". **Default deployment target** names the local member that takes a publish; **None** makes the repository read-only. **Save members** stores the order; **Discard changes** goes back to the stored one. A refusal of the server, such as a member that would create a cycle, is shown on that member.

Below, **Effective resolution order** lists the stored members, and **Explain a path** shows, for a path as a package client sends it, what each member would do: serve it, not hold it, or not be asked because of a claim or a pin rule.

## Edit a repository

1. Open the repository from the list or with **Edit**.
2. Change settings in the tabs **General**, **Access** and **npm settings**. The page shows **You have unsaved changes.**
3. Select **Save changes**, or **Discard changes**.

Saving sends only your changes with the version you loaded. If someone saved in between, **Changed by someone else since you opened it** compares **Stored now** with **Yours** and offers **Copy my changes**, **Reload (discard mine)** and **Overwrite**. A repository that is being deleted cannot be changed.

## Delete a repository

1. Select **Delete** in the list or on the repository's page.
2. Confirm **Delete repository** by typing the key.
3. If the repository is a member, deployment target or pin-rule member of a virtual repository, the deletion is refused with **Other repositories still include this one.** In the list, tick **Also remove it from the virtual repositories that include it** to detach it in the same step.

Deleting needs a recent sign-in. A local or remote repository is marked **being deleted** and a background job removes its content and then the repository; the key is free again in its project only when the job has finished. A virtual repository is removed at once. Content is deleted permanently; there is no trash. Exposure rules that name the repository are removed with it.

## The How to use tab

**How to use** shows the setup for this repository, with the **Client** to choose: **npm**, **pnpm**, **Yarn Berry (2 and later)** or **Yarn Classic (1.x)** for npm, **.NET CLI (dotnet)**, **NuGet CLI (nuget.exe)** or **Visual Studio** for NuGet, **Docker CLI (with Buildx)** or **Podman** for OCI, **APT (apt, apt-get)** for Debian, **curl** for generic. It explains the class, how to point the client at the repository and sign in, and how to install, publish, manage dist-tags or transfer files, depending on the class. **Open the client setup** leads to the setup assistant described in [Connecting a package client](../user/client-setup.md).

## File operations

Administrators hold every repository action, so the browser and the file-transfer path offer them everything a policy can grant; see [Browsing and transferring files](../user/files.md).

| Operation | Where | Action needed |
|---|---|---|
| Upload a file | **Browse** › **Upload files**, `PUT /files/<projectKey>/<key>/<path>` | `deploy`; `overwrite` to replace different bytes |
| Create a folder | **Browse** › **New folder** | `deploy` |
| Download | **Browse** › **Download**, `GET /files/<projectKey>/<key>/<path>` | `read` |
| Delete a file or folder | **Browse** › **Delete**, `DELETE /files/<projectKey>/<key>/<path>` | `delete` on every item |

Uploads go to generic local repositories only; npm content is published with the npm client, NuGet content with a NuGet client, container images with a container client through the registry (a raw upload into an OCI repository answers `409`; a raw delete runs the cascade of the registry's `DELETE`), and Debian packages with `PUT …/upload/<file>` of the APT address (a raw upload answers `409`; the generated index files below `dists/` are never written or deleted this way). Deleting npm tarballs this way respects package ownership. A folder with many items is deleted by a background job. Only an administrator can remove, through `DELETE /api/v1/projects/<projectKey>/repositories/<key>/items`, items that lie outside the include patterns or in a disabled repository. Copy, move, item properties and a trash are not available in this version.
