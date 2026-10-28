# CocoaPods package format

This page catalogs what a repository manager does to serve CocoaPods, the dependency manager for
Swift and Objective-C projects: hosting pods in local repositories, proxying the public specs index
and the Git providers that host pod sources in remote repositories, merging both in virtual
repositories, and the client setup that makes the `pod` command resolve through the system. The
client-side protocol is the CocoaPods *CDN source*, implemented in
[CocoaPods Core](https://github.com/CocoaPods/Core/blob/master/lib/cocoapods-core/cdn_source.rb);
where an entry derives behavior from that client code, it says so.
The podspec format is documented in the
[podspec syntax reference](https://guides.cocoapods.org/syntax/podspec.html). Repository classes in
general are described in [../repositories.md](../repositories.md), pull-through caching in
[../remote-repositories.md](../remote-repositories.md).

Throughout this page, `https://repo.example.com/pods/<repo-key>` stands for the specs-source URL
of one repository and `https://repo.example.com/files/<repo-key>/<path>` stands for raw file
access to it (both path layouts are the implementation's choice), `cocoapods-local`,
`cocoapods-remote` and `cocoapods-virtual` are example repository keys, and `${TOKEN}` is an access
token.

## Repositories and addressing

### PKG-PODS-001 · CocoaPods repositories in all three repository classes
Priority: **P2**

CocoaPods is supported as a local, a remote and a virtual repository format (REPO-006 in
[../repositories.md](../repositories.md)):

- local repositories hold an organization's own pods and compute a specs index for them
  (PKG-PODS-008, PKG-PODS-009);
- remote repositories cache the public specs index and the pod sources it points to
  (PKG-PODS-012); remote support exists only with the CDN source;
- virtual repositories aggregate local and remote members, all of which must be CDN-compatible
  (PKG-PODS-014);
- user and group permissions apply per repository, so access can follow projects or teams (IAM-019
  and IAM-020 in [../identity-and-access.md](../identity-and-access.md)).

Creating a repository requires the system administrator or a project administrator role. UI
location: administration, repositories, create repository, then the class and the CocoaPods
format (UI-031 in [../ui-inventory.md](../ui-inventory.md)).

Shared settings that do not apply to CocoaPods, per repository class:

| Setting | Excluded for |
|---|---|
| Priority resolution (REPO-018 in [../repositories.md](../repositories.md)) | Remote repositories; open whether local repositories are excluded as well |
| HEAD request bypass (RMT-024 in [../remote-repositories.md](../remote-repositories.md)) | Remote repositories |
| CDN download redirect (REPO-020) | Local and remote repositories |
| Scanner indexing (REPO-021) | Local repositories; remote repositories presumably as well (open) |

Federation: every format except the Terraform state backend can be federated, CocoaPods included
(REPL-026 in [../replication-and-federation.md](../replication-and-federation.md)).

### PKG-PODS-002 · Specs-source URL of a repository
Priority: **P2**

Every CocoaPods repository exposes a *specs-source URL* that the `pod` client registers as a CDN
source and lists in the `Podfile`. All files of PKG-PODS-003 are served relative to it. Pod
archives (PKG-PODS-004) are downloaded from URLs written into the served podspecs; that those URLs
are absolute and computed from the system's external base URL (PKG-PODS-011) is inferred from the
URL-base setting, see PKG-PODS-004.

## Specs index protocol

### PKG-PODS-003 · CDN specs index
Priority: **P2**

The `pod` client reads a CDN source as a set of static files; it never lists directories. The
client defines this CDN index structure: the file set below is what the CocoaPods client requests
(CocoaPods Core, `cdn_source.rb` and `source/metadata.rb`) and what `https://cdn.cocoapods.org`
serves.

| File (relative to the specs-source URL) | Content |
|---|---|
| `CocoaPods-version.yml` | Source metadata: `min`, `max` and `last` client versions, `last_compatible_versions`, and `prefix_lengths` (the public CDN uses `[1, 1, 1]`) |
| `all_pods.txt` | One pod name per line |
| `all_pods_versions_<a>_<b>_<c>.txt` | One line per pod of the shard: `<Name>/<version>/<version>/…` |
| `Specs/<a>/<b>/<c>/<Name>/<version>/<Name>.podspec.json` | The JSON podspec of one version |
| `deprecated_podspecs.txt` | Relative paths of deprecated podspecs the client should refresh |

- Shard: `<a>`, `<b>`, `<c>` are the leading hexadecimal characters of the MD5 digest of the pod
  name, one character per entry of `prefix_lengths`.
- Caching: the client stores each file's `ETag` and revalidates with `If-None-Match`; a 304 keeps
  the local copy. The server must emit stable ETags.
- Status handling by the client: 301 and 302 are followed; 404 means "not in this source"; 502,
  503 and 504 and network errors are retried with exponential backoff; other statuses fail.
- Credentials: the client reads `~/.netrc` for these requests (PKG-PODS-006).
- An empty shard file or a missing pod means the pod is not in this source; the client then tries
  the next source of the `Podfile`.

### PKG-PODS-004 · Pod archives and served podspecs
Priority: **P2**

- A pod deployed to a local repository is a `tar.gz` archive containing the project sources and
  exactly one `.podspec` or `.podspec.json` file at its root. Other archive formats are not
  supported in local repositories.
- The system parses the podspec into the JSON podspec it serves in the specs index
  (PKG-PODS-003). The URL base of PKG-PODS-011 is the prefix used to calculate relative URLs; the
  inferred consequence is that the `source` of a served podspec points to the archive's download
  URL on the system. The exact rewritten `source` form is
  unspecified.
- Excluded podspec features: dynamic variables in the podspec. Other attributes of the podspec
  reference are supported; syntax beyond that is untested.

## Client setup and authentication

### PKG-PODS-005 · Client setup with a CDN source
Priority: **P2**

Steps:

1. Recalculate the repository's index once (PKG-PODS-009), so the CDN index files exist.
2. Put credentials into `~/.netrc` (PKG-PODS-006).
3. Register the source with the client:

   ```shell
   pod repo add-cdn cocoapods-virtual "https://repo.example.com/pods/cocoapods-virtual"
   ```

4. Reference it in the `Podfile` and install:

   ```ruby
   source "https://repo.example.com/pods/cocoapods-virtual"
   ```

   ```shell
   pod install
   ```

The client setup instructions (UI-021 and UI-026 in [../ui-inventory.md](../ui-inventory.md))
offer these snippets with the user's token filled in.

### PKG-PODS-006 · Authentication and anonymous access
Priority: **P2**

| Method | Client configuration | Notes |
|---|---|---|
| `.netrc` | `machine repo.example.com` / `login <user>` / `password ${TOKEN}` | Basic authentication with the token as password (TOK-034 in [../access-tokens.md](../access-tokens.md)). The CDN source passes the netrc file to its HTTP library as optional. |

- Anonymous access to CocoaPods repositories is allowed by default and governed by the global
  anonymous-access switch (AUTHN-036 in
  [../authentication-providers.md](../authentication-providers.md)); turning it off makes every
  user authenticate, which is needed to trace who downloads what.
- Recommended: an encrypted password or a token rather than a clear-text password in `.netrc`
  (IAM-035 in [../identity-and-access.md](../identity-and-access.md)).

### PKG-PODS-007 · Legacy Git-based specs index with a client plugin
Priority: **Later**

Before the CDN source, the system served CocoaPods through a client plugin of its own: the
plugin was installed as a Ruby gem, a specs repository was added with the plugin's own `pod`
subcommand, had to be refreshed periodically with another plugin subcommand, and was listed in the
`Podfile` through a `plugin … :sources => [...]` entry. The plugin forced the client's HTTP calls
to read `.netrc`.

- Deprecated: from July 2025 CocoaPods repositories can be configured only for the CDN source, and
  the plugin is being retired.
- A known side effect: after removing the public master specs repository from the client, the
  CocoaPods statistics plugin can fail; the workaround is `ENV['COCOAPODS_DISABLE_STATS'] = 'true'`
  in the `Podfile` or the environment.
- A new implementation needs no equivalent; the entry exists so the inventory stays complete.

## Local repositories

### PKG-PODS-008 · Deploy pods
Priority: **P2**

The `pod` client has no command that uploads to a private source, so pods are deployed as plain
files through the UI deploy dialog or the deploy operation (ART-002 and ART-006 in
[../artifacts.md](../artifacts.md)):

```shell
curl -u "user:${TOKEN}" -T AcmeAnalytics-2.5.0.tar.gz \
  "https://repo.example.com/files/cocoapods-local/AcmeAnalytics/2.5.0/AcmeAnalytics-2.5.0.tar.gz"
```

- Example path: `<Name>/<version>/<Name>-<version>.tar.gz`; whether the system requires that
  layout or reads name and version from the podspec alone is unspecified.
- Required privilege: deploy on the repository (IAM-022 in
  [../identity-and-access.md](../identity-and-access.md)).
- Whether deployment updates the CDN index automatically or only through PKG-PODS-009 is
  unspecified; a manual recalculation is required before first use.

### PKG-PODS-009 · Recalculate the CocoaPods index
Priority: **P2**

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| Recalculate CocoaPods index | Repository key | Plain-text acknowledgement; 401, 403 or 500 on failure | Unspecified; a caller without the needed permission gets 403 |

UI location: artifact browser, context menu of the repository, "recalculate index" (REPO-009 in
[../repositories.md](../repositories.md)). Whether the operation is synchronous is unspecified.

### PKG-PODS-010 · CDN readiness check
Priority: **P2**

Users can tell whether a repository serves a CDN index by browsing it:

- local repository: a hidden index folder exists and contains files once the index has been
  calculated;
- remote repository: the hidden folder may be missing until something has been resolved; three
  or more levels of nested shard folders indicate a CDN-indexed remote.

UI location: artifact browser. The folder name is the implementation's choice.

### PKG-PODS-011 · URL base for generated URLs
Priority: **P2**

| Setting | Meaning | Default |
|---|---|---|
| URL base | Read-only display of the system's external base URL (general settings), the prefix used to compute the URLs written into served metadata | read-only, from general settings |

Shown on local and remote CocoaPods repositories (REPO-032 in
[../repositories.md](../repositories.md)). Because the URLs are absolute, changing the external
base URL invalidates previously generated index content until it is recalculated (inferred).

## Remote repositories

### PKG-PODS-012 · Remote CocoaPods repository settings
Priority: **P2**

The public CocoaPods ecosystem has two parts: the specs index (served by the CDN) and the source
archives, usually hosted by Git providers. A remote repository therefore has an upstream URL for
sources plus separate index URLs.

<details>
<summary>Format-specific remote settings</summary>

| Setting | Meaning | Default |
|---|---|---|
| Upstream URL | The Git provider that hosts pod sources, for example `https://github.com` | unspecified |
| Git provider | Which provider API the remote speaks: GitHub, Bitbucket, Bitbucket Server ("Stash"), Bitbucket Server before 5.1.0, another instance of the system (chained remote, RMT-040 in [../remote-repositories.md](../remote-repositories.md)), custom; open whether GitHub Enterprise is a further option | GitHub |
| Specs repository URL | Git repository holding the public podspec index; holds no binaries | `https://github.com/CocoaPods/Specs` |
| CDN URL | Static copy of the specs index that the remote proxies | `https://cdn.cocoapods.org` |
| URL base | PKG-PODS-011 | read-only |
| Allow external dependencies | Admits pods whose `source` is not a Git location | unspecified |

</details>

- For a private Bitbucket Server, the provider must be the Bitbucket Server option, not Bitbucket
  (cloud); the cloud API differs and resolution fails with the wrong choice.
- The CDN URL can also be changed through the repository configuration operations (REPO-054 in
  [../repositories.md](../repositories.md)).
- UI location: remote repository settings, format-specific section.

### PKG-PODS-013 · Source archives from Git providers
Priority: **P2**

When a client downloads a pod through a remote repository, the remote fetches the source from the
Git provider configured in PKG-PODS-012 and caches it (RMT-017 in
[../remote-repositories.md](../remote-repositories.md)). The exact provider call (archive by tag or
by commit) and the cached file name are unspecified.

- Git submodules are supported for pods fetched through remote repositories, and through virtual
  repositories when the pod resolves from a remote member. They are not supported in local
  repositories or in virtual repositories backed only by local repositories.

## Virtual repositories

### PKG-PODS-014 · Virtual CocoaPods repositories
Priority: **P2**

- A virtual repository aggregates local and remote CocoaPods repositories (REPO-033 in
  [../repositories.md](../repositories.md)) and serves one merged CDN index.
- Only the CDN source works with virtual repositories, and every member must be CDN-compatible
  (PKG-PODS-010).
- How shard files and `all_pods.txt` are merged, and which member wins for a pod version present
  in several members, are unspecified; REPO-035 describes the general resolution order.
- Remote CocoaPods members cannot carry priority resolution (REPO-018; see PKG-PODS-001).
- Open: whether the repository configuration schema accepts CocoaPods as a virtual-repository
  format, as this entry and PKG-PODS-001 assume, or omits it; the spec decides.

## Limits

### PKG-PODS-015 · Unsupported commands and content
Priority: **P2**

| Limit | Detail |
|---|---|
| `pod try` | Not supported |
| Dynamic podspec variables | Not supported |
| Archive format in local repositories | `tar.gz` only, exactly one podspec at the archive root |
| Git submodules | Remote, and virtual through a remote member only (PKG-PODS-013) |
| `pod trunk push` / `pod repo push` | Unspecified; deployment is by file upload (PKG-PODS-008) |

## Notes for implementers

- The CDN source cannot list directories; every pod must appear in its shard's
  `all_pods_versions_*.txt`, or the client reports it as missing even though the podspec file
  exists.
- The shard path is derived from the MD5 of the exact pod name, including case; generate it the
  same way the client does or lookups miss.
- `CocoaPods-version.yml` is fetched first and its `prefix_lengths` decide every later path; serve
  it from every repository class, including virtual ones.
- The client revalidates with `If-None-Match`; ETags that change on every request turn each
  `pod install` into a full download of the index.
- A 404 is a normal answer ("not here, try the next source"); answering 401 or 500 for unknown
  pods breaks multi-source `Podfile`s.
- Podspec `source` URLs end up in client caches and lockfiles; they must be stable and use the
  external base URL, not an internal host name.
- Served podspecs must not change once published: `Podfile.lock` records a checksum per podspec
  (`SPEC CHECKSUMS`), so regenerating a podspec with different JSON (for example after a base-URL
  change) shows up as a lockfile change for every consumer.
- The upstream index (`cdn.cocoapods.org`) and the sources (Git providers) are different hosts;
  a remote needs both reachable and may need different credentials for each.
