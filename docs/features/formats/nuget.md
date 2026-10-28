# NuGet

This page covers NuGet, the package format of the .NET ecosystem, end to end: repositories of all
three classes and federated NuGet repositories, the v3 protocol and the older v2 (OData) feed, the storage layout and NuGet version
normalization, SemVer 2.0.0 handling, client authentication and API keys, symbol packages and the
symbol server, client setup for nuget.exe, the .NET CLI, Visual Studio, PowerShell package
management and Chocolatey, the companion tooling that wraps NuGet clients, and, under
[Protocol reference](#protocol-reference), every resource of the NuGet v3 server API that Microsoft
documents, with the request and response essentials a server has to implement. Settings shared by
all repositories live in [repositories.md](../repositories.md) and
[remote-repositories.md](../remote-repositories.md); the NuGet-specific local and virtual settings and
the normalization rules are summarized there as REPO-030 and REPO-031 and are refined here. The client
setup dialog is UI-025 in [ui-inventory.md](../ui-inventory.md). Cross-format behavior is collected in
[README.md](README.md).

## Format overview

### PKG-NUGET-001 · NuGet repositories in all three classes
Priority: **MVP**

The system stores NuGet packages (`.nupkg`) in local repositories, proxies and caches upstream NuGet
sources in remote repositories, and combines both behind a single address in virtual repositories
(REPO-001 in [repositories.md](../repositories.md)).

| Capability | Local | Remote | Virtual |
|---|---|---|---|
| Serve packages to NuGet clients | yes | yes | yes |
| Metadata calculated from the package manifest | yes (PKG-NUGET-006) | for cached packages; the reindex operation covers the cache (PKG-NUGET-007) | aggregated from members |
| Push from NuGet clients | yes | no | yes, into the default deployment repository (PKG-NUGET-027) |
| Symbol packages and PDB resolution | yes (PKG-NUGET-029, PKG-NUGET-030) | proxy of an upstream symbol server (PKG-NUGET-031) | yes (PKG-NUGET-032) |
| Enforced (normalized) layout | creation-time option | creation-time option | creation-time option |
| v3 package content resource (flat container) | normalized repositories only | normalized repositories only | normalized repositories only |
| v3 and v2 protocol | both (PKG-NUGET-003, PKG-NUGET-004) | both, depending on the upstream settings | both |

Creation: an administrator or project administrator opens the repository section of the
administration area, chooses "create repository", picks the class and then NuGet as package format.
A local repository needs only a key; a remote repository also needs the upstream URL
(PKG-NUGET-023); a virtual repository needs its members (PKG-NUGET-026). Users without these rights
do not see the option. Recommended practice: mark a repository created with enforced layout in its
key (for example a `-enf` suffix).

Interactions: repository creation and the operations of the repository API, REPO-053 ff. in
[repositories.md](../repositories.md); federated NuGet repositories, PKG-NUGET-056; scanner
indexing is offered for NuGet repositories (REPO-021); retention policies use `.nupkg` as the NuGet
lead file (RET-009 in [retention-and-cleanup.md](../retention-and-cleanup.md)).

### PKG-NUGET-002 · Feed addresses per protocol
Priority: **MVP**

Every NuGet repository is reached through a NuGet-specific protocol address: the system's base URL,
a fixed API prefix for the NuGet format, and the repository key (the "base feed address"). The v3
protocol is served under a v3 variant of that address.

| Address | Serves | Used by |
|---|---|---|
| Base feed address | v1 and v2 (OData) requests: search, download, upload, delete | nuget.exe and dotnet configured for v2, PowerShell package management, Chocolatey (recommended form) |
| v3 address followed by `index.json` | the v3 service index (PKG-NUGET-043) | nuget.exe, dotnet, Chocolatey (v3 form) |
| v3 address without `index.json` | the v3 service index as well (inferred, see edge cases); also the symbol server address for debuggers | Visual Studio package source, `nuget.config` entries with `protocolVersion="3"`, Visual Studio symbol settings |
| Symbols sub-address under the v3 address | symbol package push for NuGet clients older than 3.5 | nuget.exe before 3.5 (PKG-NUGET-029) |
| Base feed address followed by a folder path | push into a folder of a custom layout (PKG-NUGET-012) | `nuget push` |

Edge cases:

- The plain file address of a repository (raw file access) does not work for NuGet clients; the
  NuGet API prefix is required for every NuGet command, including install and push.
- Install and delete fail when a folder path is appended to the address, even though push accepts
  one.
- Client setup uses the v3 address both with and without a trailing `index.json`. A compatible
  implementation therefore answers the service index at both; this follows from the client setup
  and is not specified as a rule of its own.
- NuGet treats a source URL that does not end in `.json` as protocol version 2 unless the
  `nuget.config` entry carries `protocolVersion="3"`
  ([nuget.config reference](https://learn.microsoft.com/en-us/nuget/reference/nuget-config-file)).
  The Visual Studio and `nuget.config` setup for v3 therefore adds that attribute.

Interactions: repository addressing in general, REPO-005 in [repositories.md](../repositories.md);
URL placement rules for authenticated feeds, PKG-NUGET-044.

### PKG-NUGET-003 · NuGet v3 protocol
Priority: **MVP**

All three repository classes serve the v3 protocol. A remote repository proxies a v3 upstream (the
public NuGet gallery or any other v3 source) once its v3 feed URL is set (PKG-NUGET-023). v3 is the
default protocol of the system.

- Resources advertised: the full resource list of the system's service index is unspecified. Fixed
  so far: the package content resource (flat container, PKG-NUGET-045) is offered on normalized
  repositories and absent from non-normalized ones, and v3 clients can publish and index symbol
  packages (PKG-NUGET-050). Microsoft marks four resources as required: package content, package
  publish, package metadata and search (PKG-NUGET-043).
- Consequence for non-normalized repositories: their v3 service index lacks a resource that
  Microsoft lists as required. The flat container makes restore faster; which path current clients
  take without it (for example the package content URLs in the package metadata documents,
  PKG-NUGET-046) is unspecified and was not verified.
- Deprecation of OData queries on nuget.org (announced for February 2021): official NuGet clients do
  not use the deprecated queries; only third-party clients are affected.

Interactions: v2 feed PKG-NUGET-004; protocol resources PKG-NUGET-041 to PKG-NUGET-055.

### PKG-NUGET-004 · NuGet v2 (OData) and v1 feed
Priority: **P2**

The base feed address (PKG-NUGET-002) answers v1 and v2 requests: search, download, upload and
delete. v2 is the OData-based protocol of NuGet 2.x clients; Microsoft leaves it intentionally
undocumented ([API overview](https://learn.microsoft.com/en-us/nuget/api/overview)).

- Clients that speak only v2 (PowerShell package management, Chocolatey; PKG-NUGET-037,
  PKG-NUGET-038) have no use for the flat container, so they can stay on non-normalized
  repositories.
- The v2 push endpoint and the v3 package publish resource share one protocol; the v3 resource
  often points at the v2 push URL (PKG-NUGET-049).
- Push to a v2 address: unless `-NoServiceEndpoint` is given, nuget.exe appends a v2 service path to
  the source URL before pushing
  ([push](https://learn.microsoft.com/en-us/nuget/reference/cli-reference/cli-ref-push)). The
  reference spells that suffix `api/v2/packages`, while nuget.org's own v2 push endpoint is
  `api/v2/package` ([rate limits](https://learn.microsoft.com/en-us/nuget/api/rate-limits)). Which
  suffix current clients append, and whether the append also happens for a v3 source (whose push
  URL comes from the service index), is unspecified and was not verified. The system fixes only
  that the base feed address handles uploads.
- A v3 service index can point back to a v2 feed through an undocumented resource that supports
  `nuget.exe list`; Microsoft advises against depending on undocumented resources.
- Remote repositories describe a v2 upstream through the feed context path and the download context
  path (PKG-NUGET-023).
- Repositories can be configured for the v2 protocol, and clients must then be set to v2 (for
  example the v2 option of the companion client, PKG-NUGET-039), but no repository setting that
  selects the protocol is defined. Whether such a setting exists is unspecified.

Limits and edge cases:

- Priority resolution (REPO-018 in [repositories.md](../repositories.md)): open whether it excludes
  only NuGet v2 or NuGet entirely, and therefore whether NuGet v3 repositories support it; the spec
  decides. Newest-compliant-version selection works only with v3 (PKG-NUGET-040).
- nuget.org rate-limits its v1 and v2 OData endpoints but not v3 search
  ([rate limits](https://learn.microsoft.com/en-us/nuget/api/rate-limits)): v1 package collection
  1,000 per minute per IP, v1 search 3,000, v2 package collection 20,000, v2 count 100. Excess
  requests receive HTTP 429 with a JSON body carrying status code and a retry hint; quota excess
  yields 403. A remote repository that talks v2 to nuget.org is exposed to these limits.

## Package model and storage

### PKG-NUGET-005 · Package recognition and stored properties
Priority: **MVP**

When a `.nupkg` arrives in a NuGet repository, the system reads the manifest (`.nuspec`) inside the
package and annotates the stored file with NuGet properties derived from it. Package ID and version
are among them (SRCH-050 in [search-and-query.md](../search-and-query.md)); the full property set is
unspecified.

Uses of the stored properties: the package search and package views of the UI (SRCH-050, SRCH-051;
ART-048, ART-049 in [artifacts.md](../artifacts.md)), retention by package (RET-009), and the
answers of the NuGet protocol.

Data the protocol resources return, and that the system therefore has to keep per package version
(derived from PKG-NUGET-046 and PKG-NUGET-047; which of these are stored as properties is
unspecified):

<details>
<summary>Manifest-derived and server-side data per version</summary>

| Data | Origin |
|---|---|
| Package ID (original casing) and normalized version; original version string | manifest |
| Authors, title, summary, description, tags, language, release notes | manifest |
| Icon, license (expression or embedded file) and readme, or their URLs | manifest and package content (PKG-NUGET-055) |
| Project URL, license-acceptance flag, minimum client version | manifest |
| Dependency groups per target framework, each dependency with ID and version range | manifest |
| Package types declared by the author | manifest |
| Listed state, publish timestamp | server (PKG-NUGET-022) |
| Package size and package hash (nuget.org uses SHA-512, base64) | server |
| Download count per version | server |
| Deprecation and vulnerability data | server or upstream (PKG-NUGET-046) |

</details>

Interactions: the properties can be rebuilt with PKG-NUGET-007; symbol packages are indexed as well
(PKG-NUGET-029).

### PKG-NUGET-006 · Automatic metadata calculation
Priority: **MVP**

NuGet metadata is recalculated automatically whenever NuGet packages are added, moved, copied or
removed.

- The calculation starts only after the triggering action has completed and runs asynchronously.
- How long it takes depends on the load of the whole system and on how many packages the
  repository holds.
- Consequence: a package that was just pushed, copied or deleted can briefly be missing from, or
  still present in, search results and metadata documents.

Interactions: manual recalculation PKG-NUGET-007; copy, move and delete in
[artifacts.md](../artifacts.md) (ART-032 to ART-034).

### PKG-NUGET-007 · Recalculate NuGet metadata (reindex)
Priority: **P1**

Recomputes the metadata of every NuGet package in one repository and re-annotates each package's
NuGet properties from its embedded manifest. Supported on local repositories, remote repositories,
the cache view of a remote repository, and virtual repositories.

| Operation | Input | Output | Privilege |
|---|---|---|---|
| Recalculate NuGet metadata | repository key | plain-text confirmation on success; bad credentials (401), permission denied (403), server error (500) | unspecified; insufficient permissions yield 403 |

- Authentication: HTTP Basic, bearer token, or the legacy API-key header (TOK-044 in
  [access-tokens.md](../access-tokens.md)).
- UI: a reindex action on the NuGet repository.
- The operation is heavy on large repositories (REPO-009 in [repositories.md](../repositories.md)).

### PKG-NUGET-008 · Enforced layout (normalized repositories)
Priority: **MVP**

| Setting | Classes | Meaning | Default |
|---|---|---|---|
| Enforced layout | local, remote, virtual | The repository normalizes package versions on arrival and refuses versions that are duplicates under NuGet's normalization rules, so it behaves like the public NuGet gallery. Also turns on the v3 package content resource. | unspecified |

Effects:

1. The v3 package content resource (flat container, PKG-NUGET-045) becomes available. It is the only
   way to obtain that resource; non-normalized repositories never offer it.
2. Deployed package names are normalized according to NuGet's normalized version numbers
   (PKG-NUGET-009), as NuGet 3.4 and later expect.
3. Uploads of a version that is identical, after normalization, to a stored one are refused.

Rules:

- The setting is chosen when the repository is created and cannot be changed later. Existing
  non-normalized repositories reach the normalized state only through the normalization migration
  (PKG-NUGET-013).
- A system-wide switch can make every newly created NuGet repository normalized (PKG-NUGET-010).
- Enforced layout is available only when the system's newer NuGet package handling is active; what
  that precondition means for an installation is unspecified.
- Errors: an action on a package whose name violates the naming convention is answered with HTTP 409
  (Conflict).
- File name convention: open whether stored NuGet package files follow
  `{NAME}-{VERSION}-{OPTIONAL-PARAMETERS}.nupkg` or `<id>.<version>.nupkg` (the name used in client
  commands); the spec decides.

UI: NuGet section of the repository create dialog (local, remote and virtual); shown read-only
afterwards (implied by "cannot be changed").

Interactions: REPO-030 and REPO-031 in [repositories.md](../repositories.md); compatibility rules
between repositories PKG-NUGET-011.

### PKG-NUGET-009 · Version normalization and duplicate detection
Priority: **MVP**

A normalized repository rewrites every incoming version into one canonical spelling, so a release is
never stored under several names. The table states the system's rules. Microsoft's
[normalized version numbers](https://learn.microsoft.com/en-us/nuget/concepts/package-versioning)
(NuGet 3.4+) cover the leading-zero, zero-fourth-part and build-metadata rows; the padding to three
parts is the system's own rule (Microsoft's example keeps `1.00` as `1.0`).

| Rule | Example input | Normalized |
|---|---|---|
| At least three numeric parts | `2.5` | `2.5.0` |
| Leading zeros removed from every part | `04.02.07`, `1.01.1` | `4.2.7`, `1.1.1` |
| A zero fourth part is dropped | `2.5.1.0`, `1.0.01.0` | `2.5.1`, `1.0.1` |
| A non-zero fourth part is kept | `2.5.1.3` | `2.5.1.3` |
| Build metadata is removed | `2.5.1+sha.9f2c` | `2.5.1` |

Upload behavior:

- A version that is valid semantic versioning is rewritten into normalized form; build metadata is
  stripped from the stored name, not rejected.
- A version that is not semantic versioning at all is rejected.
- An upload that normalizes to a version already stored is rejected as a conflict (HTTP 409).
  Example: two uploads with versions `3.0.0+a1` and `3.0.0+b2` both normalize to `3.0.0`; the second
  is refused.
- NuGet compares pre-release labels case-insensitively (`1.0.0-beta` equals `1.0.0-Beta`), and
  `1`, `1.0`, `1.0.0` and `1.0.0.0` are the same version. A server must not host versions that NuGet
  considers equal as separate packages
  ([package versioning](https://learn.microsoft.com/en-us/nuget/concepts/package-versioning)).
  Whether the system treats case variants of a pre-release label as duplicates is unspecified.

Open: when an upload strips build metadata from a version with a zero fourth part, the stored file
name either keeps the zero fourth part or drops it as the rule table does; the spec decides.

Interactions: REPO-031 in [repositories.md](../repositories.md); protocol rules for versions in URLs,
PKG-NUGET-042.

### PKG-NUGET-010 · Global normalization for new repositories
Priority: **P2**

| Setting | Meaning | Default |
|---|---|---|
| Normalize package versions (system-wide) | Every NuGet repository created after the switch is turned on is created normalized. | unspecified |

- Not retroactive: repositories created before the switch keep their non-normalized layout.
- Repositories created while the switch is on can never be made non-normalized.
- The per-repository choice at creation remains available.

UI: administration area, system settings, package settings, NuGet section, a checkbox with save.

### PKG-NUGET-011 · Normalization compatibility between repositories
Priority: **MVP**

Normalized and non-normalized repositories must not exchange packages:

| Situation | Rule |
|---|---|
| Upload, copy or move into a normalized repository | Allowed only for packages that comply; a non-normalized package cannot be uploaded, copied or moved into a normalized repository. Copy and move are allowed only between repositories of the same kind. |
| Virtual repository membership | A normalized virtual repository may contain only normalized members. A non-normalized virtual repository may contain normalized members. |
| Chained remote repository (upstream on another instance) | A normalized chained remote may point only at a normalized local or remote repository; it must be configured with administrator credentials of the other instance, which the system uses to check the upstream's state. |
| Replication | Source and target must have the same setting. |
| Federation | Members must have the same setting. The system does not enforce this; administrators must make sure. |
| Restore of an archived repository into another repository | The target must have the same settings as the archived original; otherwise packages may be rejected. |

If one side of a pair is normalized and the other is not, the pairing stops working as intended.
Pairs are migrated together, or unpaired first (PKG-NUGET-013).

Interactions: replication and federation in
[replication-and-federation.md](../replication-and-federation.md); chained remotes PKG-NUGET-025 and
RMT-040 in [remote-repositories.md](../remote-repositories.md); archiving in
[retention-and-cleanup.md](../retention-and-cleanup.md).

### PKG-NUGET-012 · Custom layouts and folder paths
Priority: **P2**

NuGet repositories can use a custom repository layout (REPO-012, REPO-046 in
[repositories.md](../repositories.md)) so that packages are kept in a folder structure, for example
to make layout-based cleanup possible. A predefined NuGet layout is among the layouts the system
ships with; its path pattern is unspecified.

- A client places a package into a folder by appending the folder path to the base feed address as
  push source (for example a `releases` folder). The package must match the layout of the target
  repository.
- Install and delete through NuGet do not accept a folder path (PKG-NUGET-002).
- How custom layouts combine with enforced layout is unspecified.

### PKG-NUGET-013 · Normalization migration of existing repositories
Priority: **P2**

The guided migration that brings a NuGet repository created without enforced layout into the
normalized state. The general flow (migration table, dry run, report, double confirmation,
administrator or project-administrator privilege, repository unavailable while migrating) is
REPO-067 in [repositories.md](../repositories.md). NuGet specifics:

| Class | Dry run reports | Migration |
|---|---|---|
| Local | total packages, already normalized, failing normalization | Non-compliant packages are moved unchanged, under their original names, into an isolation repository that is either created automatically or chosen from the existing ones. If no package complies, the repository ends up empty. |
| Remote | as local | Completes at once: the repository is backed up and its cache is invalidated; nothing is isolated. |
| Chained remote | whether the upstream on the other instance is already normalized | The upstream is migrated first; the migration can be saved and resumed; needs the administrator credentials configured on the remote. |
| Virtual | counts of members: total, normalized, not normalized | Moves nothing. The virtual repository becomes normalized only when every member already is; otherwise nothing changes and the report lists the members to migrate. Per member the report offers: create migration, view migration, download last report. |

- After a successful migration the flat container (PKG-NUGET-045) is enabled and the normalized
  layout is enforced from then on.
- Duplicate versions (two names that normalize to the same version) are not detected by the
  migration: both land in isolation, and the conflict surfaces only on re-upload. Recommended
  practice is to decide a policy beforehand: prefer first-party over third-party packages, then the
  primary owning team's package, then verified publishers, then the most recent upload; otherwise
  keep both isolated.
- Isolated packages are re-uploaded under corrected, normalized names after the migration.

### PKG-NUGET-014 · SemVer 2.0.0 packages
Priority: **MVP**

NuGet repositories of all classes accept and serve packages whose versions use
[Semantic Versioning 2.0.0](https://semver.org/spec/v2.0.0.html) features, over v2 and v3: pre-release
labels with dot-separated identifiers (for example `4.1.0-ci.112`) and build metadata (for example
`4.1.0+commit.ab12`). When the newest version of a package is a SemVer 2.0.0 version, the system
returns it as the latest version.

Legacy-client filter:

- SemVer 2.0.0 packages are hidden from NuGet clients older than 4.3.0, matching the behavior of the
  public gallery. This is a breaking change.
- A system-level switch turns the filter off for local repositories, restoring the earlier
  behavior. By default the filter applies; the switch exists to disable it. The switch is defined
  as a search filter for local repositories; whether it also affects package metadata answers is
  unspecified.
- In the v3 protocol the filter is expressed by the `semVerLevel` query parameter of search and
  autocomplete and by the package metadata resource type (PKG-NUGET-042).

Definitions (Microsoft, [package versioning](https://learn.microsoft.com/en-us/nuget/concepts/package-versioning)):
a version is SemVer 2.0.0-specific if its pre-release label contains a dot or it carries build
metadata. nuget.org additionally treats a package as SemVer 2.0.0 if any dependency range has a
SemVer 2.0.0-specific bound. Clients that see SemVer 2.0.0 packages: NuGet 4.3.0+, Visual Studio
2017 15.3+, Visual Studio 2015 with the NuGet extension 3.6.0, .NET SDK 2.0.0+, JetBrains Rider, Paket
5.0+.

Edge case: whether the system also classifies a package as SemVer 2.0.0 by its dependency ranges is
unspecified.

### PKG-NUGET-015 · Maximum unique snapshots on NuGet repositories
Priority: **P2**

| Setting | Classes | Meaning | Default |
|---|---|---|---|
| Maximum unique snapshots | local and federated forms; the remote configuration document carries it as a generic field, the NuGet remote form does not show it | Upper bound of stored unique snapshots of one artifact; on overflow the oldest are removed. In configuration as code the field applies to Maven and NuGet only. | 0 = no limit, nothing removed |

What counts as a NuGet "snapshot" (for example pre-release versions) is unspecified.
Interactions: REPO-025 in [repositories.md](../repositories.md).

## Authentication and access

### PKG-NUGET-016 · Anonymous access and forced authentication
Priority: **MVP**

NuGet clients first send a request without credentials and look up credentials only after an HTTP
401 answer
([authenticated feeds](https://learn.microsoft.com/en-us/nuget/consume-packages/consuming-packages-authenticated-feeds)).
With anonymous access switched on globally, the system does not challenge NuGet clients for
credentials by default, so a client never sends the credentials it has configured and is served as
the anonymous user. What the system answers when the anonymous user lacks read permission on the
repository is unspecified.

| Setting | Classes | Meaning | Default |
|---|---|---|---|
| Force authentication | local, remote, virtual (and federated) | The repository demands HTTP Basic credentials before any use, including package resolution, even when anonymous access is on globally. | off |

- Anonymous access to NuGet repositories is allowed by default, governed by the global
  anonymous-access setting of the security settings (IAM-005 in
  [identity-and-access.md](../identity-and-access.md), whose default is otherwise unspecified).
- Recommendation: switch anonymous access off to keep track of who uses the repositories; users
  then configure username and password for the source in their client, which stores them in the
  credentials section of `nuget.config` (PKG-NUGET-036).
- Credential lookup order of NuGet after a 401: environment variable
  `NuGetPackageSourceCredentials_<source name>`, then `nuget.config`, then a credential provider
  plug-in.
- NuGet 6.7+ sends credentials pre-emptively to URLs in the same directory as, or below, a URL that
  was already authenticated (PKG-NUGET-044). Resources placed elsewhere cost an extra anonymous
  round trip each, and with anonymous access on they are served anonymously.

UI: NuGet section of the repository create and edit dialog.
Interactions: REPO-028 in [repositories.md](../repositories.md); UI-025 in
[ui-inventory.md](../ui-inventory.md).

### PKG-NUGET-017 · Client credentials over HTTP Basic
Priority: **MVP**

NuGet clients authenticate with HTTP Basic: a username plus either the password or an access token
as the password (TOK-034 in [access-tokens.md](../access-tokens.md)).

- A username is mandatory. A token that does not carry a username (a reference token, TOK-004) must
  be paired with the username explicitly. The companion client (PKG-NUGET-039) derives the username
  from a self-contained token; with a reference token and no username, the .NET client fails with an
  error that the username parameter is empty.
- Where clients keep credentials: the per-source credentials section of `nuget.config` (username;
  password encrypted, which works only on Windows and only for the same user on the same machine;
  or a clear-text password, optionally as an environment-variable macro), the environment variable
  `NuGetPackageSourceCredentials_<source name>` with value
  `Username=<user>;Password=<secret>[;ValidAuthenticationTypes=<types>]`, or a credential provider.
- If a server advertises NTLM or Negotiate besides Basic, clients need `basic` as valid
  authentication type to send Basic credentials (PKG-NUGET-036).

### PKG-NUGET-018 · API key for push and delete
Priority: **MVP**

The NuGet protocol carries an API key in the `X-NuGet-ApiKey` request header for push, delete and
relist (PKG-NUGET-049, PKG-NUGET-050); the key is an opaque string issued by the package source.

- The system's convention for nuget.exe `setapikey`, PowerShell's NuGet API key parameter and
  Chocolatey's `apikey`: the API key is `<username>:<secret>`, where the secret is the user's
  password or an access token.
- Open: whether a bare token without username is also accepted as API key, as a PowerShell publish
  or a .NET CLI push that passes a single value would need; the spec decides.
- Which credential the system authorizes a push with when a client sends both Basic credentials and
  an API key is unspecified. NuGet itself treats the two separately: the API key is not used to
  authenticate feed reads; source credentials are configured independently
  ([setapikey](https://learn.microsoft.com/en-us/nuget/reference/cli-reference/cli-ref-setapikey)).
- Client-side storage: the `apikeys` section of `nuget.config`, keyed by source URL and encrypted;
  since NuGet 7.6 the environment variables `NUGET_API_KEY` and `NUGET_SYMBOL_API_KEY`. Precedence:
  command-line option, then environment variable, then configuration file.
- Symbol pushes may use a separate symbol API key (PKG-NUGET-029).

### PKG-NUGET-019 · Permissions per NuGet operation
Priority: **MVP**

| Client operation | Repository | Action needed (IAM-022 in [identity-and-access.md](../identity-and-access.md)) |
|---|---|---|
| Restore, install, search, read metadata | local | read |
| Same, item already cached | remote | read |
| Same, item not yet cached | remote | read and deploy/cache (IAM-023) |
| Same | virtual | decided per member (IAM-024) |
| Push | local | deploy; replacing an existing file additionally delete/overwrite |
| Push | virtual | on the default deployment repository; exact rule unspecified (IAM-024) |
| Delete or unlist | local | delete |
| Relist | local | unspecified |
| Recalculate metadata | any | unspecified (PKG-NUGET-007) |

The companion client (PKG-NUGET-039) requires read permission, and write permission for push.

## Local repositories

### PKG-NUGET-020 · Push through the NuGet protocol
Priority: **MVP**

Clients publish packages with `nuget push`, `dotnet nuget push`, PowerShell's publish commands or
`choco push`. Push works on local and virtual repositories (PKG-NUGET-027), never on remote ones.

- Wire format: the package publish resource (PKG-NUGET-049).
- Normalized repository: a version that is already present after normalization is refused with
  409 (PKG-NUGET-009). NuGet's `-SkipDuplicate` option (NuGet 5.1+) skips versions that already
  exist; the push reference does not name the status code it reacts to, but 409 is the protocol's
  answer for an existing ID and version (PKG-NUGET-049).
- Non-normalized repository: whether pushing an existing version replaces it is unspecified;
  Microsoft allows sources other than nuget.org to support replacement.
- HTTP status of a successful push: unspecified (the protocol allows 201 or 202).
- A symbol package lying next to the pushed package is pushed with it (PKG-NUGET-029).
- Pushing into a folder: PKG-NUGET-012.
- Metadata becomes visible asynchronously (PKG-NUGET-006).

### PKG-NUGET-021 · Deploy through the UI and generic upload
Priority: **MVP**

A `.nupkg` can also be deployed without a NuGet client: in the artifact browser, open the deploy
dialog, choose the NuGet repository as target, upload the file, check the deploy options and
confirm (UI-013 in [ui-inventory.md](../ui-inventory.md), ART-006 in
[artifacts.md](../artifacts.md)). Generic deployment through the artifact API (ART-002) works the same
way. Both trigger the metadata calculation (PKG-NUGET-006); normalized repositories apply
PKG-NUGET-009.

### PKG-NUGET-022 · Delete, unlist and relist
Priority: **MVP**

- The base feed address handles delete requests (PKG-NUGET-004); v3 clients delete through the
  package publish resource (PKG-NUGET-049).
- Whether the system treats a NuGet delete as a hard delete or as unlisting is unspecified. The
  protocol leaves it to the server: nuget.org unlists, other servers delete hard, delete softly or
  unlist.
- Relisting is unspecified.
- Protocol effects of unlisting (Microsoft): an unlisted version disappears from search and
  autocomplete results, stays in the flat container's version list and stays downloadable, and is
  marked `listed: false` in package metadata. nuget.org sets the publish date of unlisted versions
  to the year 1900.
- Deleting through the artifact operations (ART-034 in [artifacts.md](../artifacts.md)) triggers
  metadata recalculation (PKG-NUGET-006) and is subject to the trash can (ART-036).

## Remote repositories

### PKG-NUGET-023 · NuGet remote repository settings
Priority: **MVP**

Besides the settings shared by all remote repositories (RMT-001 ff. in
[remote-repositories.md](../remote-repositories.md)), a NuGet remote has these fields:

| Setting | Meaning | Default |
|---|---|---|
| Upstream URL | Base URL of the upstream NuGet source. For the public gallery the URL is the site address and the download path goes into the download context path. | `https://www.nuget.org` (fixed so far only for the chained-remote set-up, PKG-NUGET-025) |
| Download context path | Path prefix under the upstream URL through which the upstream serves package downloads (v2). For nuget.org: `api/v2/package`. | `api/v2/package` (configuration schema) |
| Feed context path | Path prefix of the upstream's v2 feed (for nuget.org `api/v2`). | `api/v2` |
| v3 feed URL | URL of the upstream's v3 service index. Setting it enables v3 proxying. | `https://api.nuget.org/v3/index.json` |
| Symbol server URL | Upstream symbol server for PDB files (PKG-NUGET-031). | `https://symbols.nuget.org/download/symbols`; open whether the `http` form is the default instead |
| Force authentication | PKG-NUGET-016. | off |
| Enforced layout | PKG-NUGET-008; creation time only. | unspecified |

The NuGet remote form shows no maximum-unique-snapshots field; the remote configuration document
carries it as a generic field with default 0 (PKG-NUGET-015).

Limits and edge cases:

- Packages cannot be pushed into a remote repository; symbol packages neither.
- The metadata retrieval cache period of a NuGet remote whose upstream is the Chocolatey community
  repository (`community.chocolatey.org`) has a minimum of 14,400 seconds instead of 30 (RMT-018 in
  [remote-repositories.md](../remote-repositories.md)).
- CDN download redirection is not supported for NuGet (REPO-020 in
  [repositories.md](../repositories.md)).
- Priority resolution: open for NuGet (REPO-018, PKG-NUGET-004).

UI: NuGet section of the remote repository's basic tab.
API: the fields are part of the remote repository configuration operations (RMT-002).

### PKG-NUGET-024 · Proxying the public NuGet gallery
Priority: **MVP**

The default settings (PKG-NUGET-023) make a new NuGet remote proxy the public gallery:
v3 through `https://api.nuget.org/v3/index.json`, v2 downloads under `api/v2/package`, symbols from
the gallery's symbol server. Packages and symbol files are cached on first request.

Expectations from Microsoft's
[implementation guide](https://learn.microsoft.com/en-us/nuget/api/implementation-guide) for a server
that mirrors another source:

- Do not advertise nuget.org resources directly in the own service index unless a configuration can
  turn that off: clients in networks that block nuget.org would fail. Resources may technically live
  on another host, but that breaks restricted networks.
- Periodically mirror deprecation and vulnerability data of mirrored packages from the source; the
  catalog (PKG-NUGET-053) with a stored cursor makes this efficient for nuget.org.
- Cache or proxy vulnerability data pages (PKG-NUGET-051) rather than pointing clients at nuget.org.
- Never alter a served `.nupkg` (for example to rewrite embedded metadata): lock files and package
  signatures make clients treat a modified package as tampered. The official client also assumes
  that one ID and version always stand for the same content
  ([catalog](https://learn.microsoft.com/en-us/nuget/api/catalog-resource)).

Edge cases:

- Which NuGet documents the remote treats as expiring metadata (service index, package metadata,
  search answers, flat-container version lists) and which as immutable (`.nupkg`) is unspecified;
  RMT-018 and RMT-020 apply to whatever is classified as metadata.
- nuget.org's v1 and v2 endpoints are rate-limited, v3 search is not (PKG-NUGET-004).
- Search through a remote: whether the remote forwards search queries to the upstream or searches
  only its cache is unspecified.

### PKG-NUGET-025 · Chained remote NuGet repository
Priority: **P2**

A NuGet remote whose upstream is a NuGet repository on another instance of the same kind of system
(RMT-040 in [remote-repositories.md](../remote-repositories.md)). Set-up:

1. On the source instance, a NuGet repository exists (typically a remote proxying the public gallery).
2. On the second instance, create a NuGet remote with the upstream URL set to the source repository's
   base feed address, and credentials of the source instance (username and password, or an access
   token issued by the source instance).
3. Set the download context path to `Download` (the segment under which the source instance's v2
   feed serves package files), leave the feed context path empty, and set
   the v3 feed URL to the source repository's v3 address (without `index.json`).
4. Test the connection.

- The v3 feed URL must point at the source instance. Otherwise the chained remote fetches the service
  index from nuget.org directly: the source instance's cache is bypassed, and the chain fails where
  nuget.org is blocked.
- Normalization rules for chains and the need for administrator credentials: PKG-NUGET-011.
- Migration: the source repository first, then the chained remote (PKG-NUGET-013).

## Virtual repositories

### PKG-NUGET-026 · NuGet virtual repository
Priority: **MVP**

A NuGet virtual repository combines NuGet local, remote and virtual repositories under one address
for all NuGet clients and for the debugger (PKG-NUGET-032).

| Setting | Meaning | Default |
|---|---|---|
| Force authentication | PKG-NUGET-016. | off |
| Enforced layout | PKG-NUGET-008; a normalized virtual repository accepts only normalized members. | unspecified |
| Default deployment repository | Target of pushes to the virtual address (REPO-038). | none |

- Resolution order and latest-version resolution: REPO-035 and REPO-036 in
  [repositories.md](../repositories.md).
- How the virtual repository merges NuGet metadata and search results of its members, and whether a
  virtual metadata cache period applies to NuGet (REPO-037 does not list NuGet), is unspecified.
- The companion client's troubleshooting assumes that the resolution repository is a NuGet virtual
  repository (PKG-NUGET-039).

### PKG-NUGET-027 · Push through a virtual repository
Priority: **MVP**

Pushes to a virtual repository's address are accepted and stored in its default deployment
repository (REPO-038). Unspecified: the behavior when no default deployment repository is set, and
whether the target's normalization must match the virtual repository's.

## Federated repositories

### PKG-NUGET-056 · Federated NuGet repositories
Priority: **Later**

NuGet is one of the formats a federated repository can hold: a set of member repositories on
several instances that mirror each other's content (REPL-018 and REPL-026 in
[replication-and-federation.md](../replication-and-federation.md)). The NuGet section of the
federated repository form has two fields:

| Setting | Meaning | Default |
|---|---|---|
| Maximum unique snapshots | Upper bound of retained NuGet snapshot versions per artifact (PKG-NUGET-015). | 0 = unlimited |
| Force authentication | Every NuGet operation, package resolution included, requires credentials (PKG-NUGET-016). | off |

- The federated form has no enforced-layout field. All members must nevertheless share one layout
  variant, normalized or not; the system does not check this (PKG-NUGET-011).
- Normalizing a federated member means migrating every member in a coordinated way, or removing the
  pairing first (PKG-NUGET-013).
- What a member does with the NuGet metadata of packages it receives from another member (whether
  it recalculates it as for a local push, PKG-NUGET-006) is unspecified.

## Search

### PKG-NUGET-028 · Client search and version listing
Priority: **MVP**

NuGet clients search through the base feed address (v2) or the v3 search resource (PKG-NUGET-047),
and list versions through autocomplete (PKG-NUGET-048) and the flat container (PKG-NUGET-045).

| Behavior | Source |
|---|---|
| Unlisted versions never appear in search results; a package whose versions are all unlisted is absent from ID autocomplete | Microsoft |
| Without a query term, all packages are returned (paged); this feeds Visual Studio's browse tab | Microsoft |
| Pre-release versions are excluded unless requested | Microsoft |
| SemVer 2.0.0 versions are returned only to clients that opt in (PKG-NUGET-014) | system and Microsoft |
| Package-type filter (used by `dotnet tool search`) | Microsoft |
| Paging with skip (default 0) and take; a server may cap take; nuget.org caps skip at 3,000 and take at 1,000 | Microsoft |

- How the system tokenizes and ranks queries, which fields it matches (Microsoft expects at least
  ID, title, description and tags), and its paging caps are unspecified.
- The system's own package search in the UI is SRCH-050 and SRCH-051 in
  [search-and-query.md](../search-and-query.md).
- Upstream curation does not filter search results (PKG-NUGET-040).

## Symbol packages and symbol server

### PKG-NUGET-029 · Symbol package upload
Priority: **MVP**

Symbol packages carry the PDB files of a NuGet package. The system accepts them in NuGet local
repositories, calculates their metadata and indexes the symbol files so that debuggers can download
them (PKG-NUGET-030). A "symbol repository" is an ordinary NuGet local repository. Symbol pushes
are defined only against local repositories; that a push through a virtual repository
lands in its default deployment repository (PKG-NUGET-027) is inferred from the general push rule.

- Push a symbol package alone: push the `.snupkg` file.
- Push together: pushing `<id>.<version>.nupkg` while `<id>.<version>.snupkg` lies in the same
  directory publishes both, the package first.
- NuGet CLI 3.5 and later need no separate symbol source. Older nuget.exe versions push symbols to a
  dedicated symbols sub-address under the repository's v3 address, configured in `nuget.config`
  with `protocolVersion="3"` and with its own API key.
- The client creates `.snupkg` (or the legacy `.symbols.nupkg`); whether the system accepts the
  legacy form is unspecified.
- Wire format and the server's answers: PKG-NUGET-050. Whether the system enforces Microsoft's
  rejection cases (no matching package, symbol package not yet available, invalid symbol package) is
  unspecified.

Symbol package structure ([Microsoft](https://learn.microsoft.com/en-us/nuget/create-packages/symbol-packages-snupkg)):
same ID and version as the package; PDB files in the same folder hierarchy as the corresponding DLL
and EXE files of the package, other files left out; package type `SymbolsPackage` in the manifest;
authors, owners, license-acceptance, license and icon fields omitted. Creating one needs nuget.exe
4.9.0+ or .NET CLI 2.2.0+ (`IncludeSymbols` and `SymbolPackageFormat=snupkg`); publishing symbols
is a v3-only feature. nuget.exe's `-NoSymbols` skips the symbol push; `-SymbolSource` and
`-SymbolApiKey` address a separate symbol server.

### PKG-NUGET-030 · Symbol server for debuggers
Priority: **P1**

Local and virtual NuGet repositories serve PDB files to debuggers. Visual Studio and WinDbg can both
query symbol servers; a debugger that loads a binary from a build whose symbols are stored fetches the
matching PDB automatically.

| Aspect | Behavior |
|---|---|
| Supported PDB formats | Microsoft PDB 7 (MSF 7.00) and Portable PDB 1.0 |
| Matching | By the assembly hash stored in both the DLL and the PDB; the two must match for debugging and source stepping |
| Symbol server address | The v3 address of the local or virtual repository (PKG-NUGET-002) |
| Authentication | The debugger asks for credentials; afterwards it checks its local symbol cache, then the repository |

Visual Studio set-up: disable "Just My Code" (debugging options, general), then add the repository's
v3 address as symbol server (debugging options, symbols).

Edge cases:

- If the `.nupkg` itself contains PDB files, Visual Studio uses those from disk and does not query
  the symbol server; stepping into the code may then fail.
- The request scheme debuggers use to fetch a PDB from the symbol server is unspecified.
- nuget.org's own symbol server accepts only Portable PDBs and only the file types `.pdb`, `.nuspec`,
  `.xml`, `.psmdcp`, `.rels`, `.p7s` inside symbol packages; whether the system validates symbol
  package contents is unspecified.

### PKG-NUGET-031 · Proxying an upstream symbol server
Priority: **P1**

A NuGet remote repository proxies the symbol server named in its symbol server URL (default: the
public gallery's symbol server, PKG-NUGET-023). Symbol files are cached on demand when a debugger
requests them; cached symbol files can be removed from the cache; symbol packages cannot be pushed
into the remote.

### PKG-NUGET-032 · Symbol resolution through virtual repositories
Priority: **P1**

A NuGet virtual repository whose members include local repositories with symbol packages and remote
repositories proxying symbol servers serves both kinds of symbol files from its single v3 address.
Debuggers are pointed at the virtual address (PKG-NUGET-030).

## Client setup

### PKG-NUGET-033 · nuget.exe (NuGet CLI)
Priority: **MVP**

Supported from NuGet CLI 3.4.4. Placeholders: `<v3-index-url>` is the repository's v3 address with
`index.json`, `<v2-url>` its base feed address, `<source>` a local source name, `<token>` a password
or access token.

```shell
nuget sources Add -Name <source> -Source <v3-index-url> -username <username> -password <token>
nuget setapikey <username>:<token> -Source <source>
# v2 repositories
nuget sources Add -Name <source> -Source <v2-url> -username <username> -password <token>
# change credentials of an existing source
nuget sources update -Name <source> -UserName <username> -Password <token>
# install and push
nuget install <package-id>
nuget install <package-id> -Source <source>
nuget push <package-file>.nupkg -Source <source>
nuget push <package-file>.snupkg -Source <source>
```

- Configuration file: `%appdata%\NuGet\NuGet.Config` on Windows; on macOS and Linux the client
  setup uses `~/.config/NuGet/NuGet.Config`, while Microsoft names both
  `~/.nuget/NuGet/NuGet.Config` and `~/.config/NuGet/NuGet.Config` and notes that the user-level location varies by tool
  ([setapikey](https://learn.microsoft.com/en-us/nuget/reference/cli-reference/cli-ref-setapikey)).
  A `nuget.config` in the project directory also applies.
- A client resolves from this repository first only if the source is first in its configuration or
  is named with `-Source`.
- Push timeout default: 300 seconds; `-Timeout` changes it
  ([push](https://learn.microsoft.com/en-us/nuget/reference/cli-reference/cli-ref-push)).
- nuget.exe cannot restore `.slnx` solutions.

Interactions: client setup instructions in the UI, UI-025 in [ui-inventory.md](../ui-inventory.md).

### PKG-NUGET-034 · .NET CLI
Priority: **MVP**

```shell
dotnet nuget add source <v3-index-url> --name <source> --username <username> --password <token>
# v2 repositories
dotnet nuget add source <v2-url> --name <source> --username <username> --password <token>
dotnet restore
dotnet nuget push <package-file>.nupkg --api-key <api-key> --source <source>
```

- The .NET CLI runs non-interactively by default; credential providers may prompt only with
  `--interactive`.
- The `--api-key` value is a placeholder; whether it takes the `<username>:<secret>` form or a bare
  token is open (PKG-NUGET-018).

### PKG-NUGET-035 · Visual Studio
Priority: **MVP**

- Package source: Tools → NuGet Package Manager → Package Manager Settings → Package Sources; add a
  source with a name and the repository's v3 address (without `index.json`, PKG-NUGET-002). The base
  feed address there gives v2 (and v1) access.
- Symbol server: PKG-NUGET-030.
- Visual Studio always runs NuGet interactively and can show a login dialog through a credential
  provider.

### PKG-NUGET-036 · nuget.config elements the server relies on
Priority: **MVP**

Elements of `nuget.config`
([reference](https://learn.microsoft.com/en-us/nuget/reference/nuget-config-file)) that decide how
clients talk to the server:

<details>
<summary>Relevant sections and attributes</summary>

| Element | Purpose | Notes |
|---|---|---|
| Package sources: name, URL | The feed addresses | `<clear />` drops sources inherited from other configuration files. Source order is ignored for restore and PackageReference projects; it is honored for install and update in `packages.config` projects. |
| Package sources: `protocolVersion` | `3` for the v3 protocol | Defaults to 2 unless the URL ends in `.json` |
| Package sources: `allowInsecureConnections` | Suppresses the warning for `http` sources | NuGet 6.8+ |
| Package sources: `disableTLSCertificateValidation` | Ignores certificate errors | NuGet 6.11+ |
| Package source credentials | Username; password (encrypted, Windows only) or clear-text password; valid authentication types | Child element per source name; spaces in names are written as `_x0020_` |
| API keys | Encrypted API key per source URL | Set by `nuget setapikey` |
| Disabled package sources | Sources switched off | |
| Package source mapping | Glob patterns of package IDs per source | Affects downloads by restore, install and update, not metadata queries such as `dotnet package add` or outdated/vulnerable listings, which query every source |
| Audit sources | Sources consulted for vulnerability data during restore | NuGet 6.12+; without them, restore uses the package sources (PKG-NUGET-051) |
| Default push source | Push target when no source is given | |
| Signature validation mode and trusted signers | `require` makes restore accept only packages signed by trusted authors or repositories; a trusted repository names its service index (HTTPS) and certificate fingerprints | PKG-NUGET-052 |

</details>

Values may reference environment variables as `%NAME%` on all platforms (`$NAME` is not resolved).

### PKG-NUGET-037 · PowerShell package management
Priority: **P2**

Supported from version 1.0.5; which component the version refers to is unspecified.
PowerShell uses the v2 base feed address.

```powershell
$credential = New-Object PSCredential('<username>', $(ConvertTo-SecureString '<token>' -AsPlainText -Force))
Register-PSRepository -Name <source> -SourceLocation "<v2-url>" -PublishLocation "<v2-url>" -InstallationPolicy Trusted -Credential $credential
Register-PSRepository -Name <source> -ScriptSourceLocation "<v2-url>" -ScriptPublishLocation "<v2-url>" -InstallationPolicy Trusted -Credential $credential
Install-Module -Name <module> -Repository <source> -Credential $credential
Install-Script -Name <script> -Repository <source> -Credential $credential
Publish-Module -Path <path> -Repository <source> -NuGetApiKey <username>:<token>
Publish-Script -Path <path> -Repository <source> -NuGetApiKey <username>:<token>
```

### PKG-NUGET-038 · Chocolatey
Priority: **P2**

Supported from Chocolatey 1.2.0. The v2 base feed address is the recommended form. Open:
Chocolatey's registry either speaks only v2, or a v3 variant using the full `<v3-index-url>` works
as well; the spec decides.

```shell
choco source add -n <source> -s="<v2-url>" --priority 1 --bypass-proxy --allow-self-service
choco apikey -s "<v2-url>" -k <username>:<token>
choco install <package-id>
choco install <package-id> -Source <source>
choco push <package-file> -s <source>
```

Interactions: remote repositories proxying the Chocolatey community repository have a raised minimum
metadata cache period (PKG-NUGET-023).

## Build integration

### PKG-NUGET-039 · Companion client wrapping nuget and dotnet
Priority: **P2**

The system's companion command-line client wraps nuget.exe and the .NET CLI to resolve through the
system and to record build metadata ([builds-and-ci.md](../builds-and-ci.md)).

Configuration step, run once per project (or globally) before the first build:

| Option | Meaning | Default |
|---|---|---|
| Global | Apply the configuration to all projects | off |
| Resolution server | Server entry used for resolution | none; required for non-interactive use |
| Resolution repository | NuGet repository to restore from | none; required for non-interactive use |
| Use protocol v2 | Restore through the v2 feed instead of v3 | off |

Without both server and repository the step starts an interactive wizard, unsuitable for CI.
Without the configuration, the wrapper refuses to run and asks for the configuration step first.

Default mode: any subcommand is forwarded to the native client. For each call the wrapper injects a
temporary NuGet configuration file pointing at the system, unless the caller passes an own
configuration file or source. Subcommands that do not accept a configuration file argument fail
through the wrapper (for example adding a package reference with the .NET CLI); for `test` the
wrapper injects nothing and adds "no restore" instead.

| Build option | Meaning | Default |
|---|---|---|
| Build name and build number | Record build metadata; one requires the other | none |
| Project key | Project the build metadata belongs to | none |
| Module | Module name inside the build metadata | none |
| Allow insecure connections | Configure NuGet sources without TLS checks (testing only) | off |

Pass-through mode (opt-in through an environment switch in newer releases): the native client runs
with the user's own NuGet configuration; resolution and deployment repositories given on the command
line add a source for that call only, and nothing is rewritten. An existing per-project configuration
from the configuration step must be removed first, and no global one may exist, otherwise the
default mode stays active. Recorded data: for
nuget.exe, restore, install and update record dependencies, push and pack record artifacts; for the
.NET CLI, restore, build and add record dependencies, pack and push record artifacts.

Further capabilities: an offline dependency-tree listing for a restored solution (JSON default, or
table); an MSBuild integration through project-file targets; a CI example for GitHub Actions; build
and audit pipeline templates for NuGet and .NET in GitLab CI (BLD-029 in
[builds-and-ci.md](../builds-and-ci.md)).
Authentication: NuGet needs Basic credentials with a username, so the client needs a self-contained
token or an explicit username with a reference token (PKG-NUGET-017).

## Upstream curation

### PKG-NUGET-040 · Newest-compliant-version selection for NuGet clients
Priority: **Later**

When upstream curation blocks the newest version of a package, the system can steer clients to the
newest version that complies with the curation policies
([security-scanning-and-curation.md](../security-scanning-and-curation.md)). For NuGet this works
only with v3 repositories, not v2.

| Client command | Selection applies | Behavior when the newest version is blocked |
|---|---|---|
| `dotnet add package <id>` | yes | the newest version that passes the policies is added |
| `dotnet add package <id> --version <v>` | yes | error NU1102 when `<v>` is the blocked newest version; HTTP 403 when an older blocked version is requested |
| `dotnet restore` | yes | falls back to the next version that passes |
| `dotnet restore` with a lock file | no | pinned versions are fetched as locked; a blocked one fails with 403 |
| `dotnet package search` | no | lists the newest version even if blocked; installing that one fails with 403 |

Client semantics to keep in mind: `--version 13.0.4` means "13.0.4 or higher"; an exact version needs
`[13.0.4]`. Search results are not filtered.

## Protocol reference

The entries below describe the NuGet v3 server API as documented by Microsoft
([API overview](https://learn.microsoft.com/en-us/nuget/api/overview),
[implementation guide](https://learn.microsoft.com/en-us/nuget/api/implementation-guide)). They state
what a server must provide for official clients; the system's own behavior is described in the
entries above. `{@id}` is the resource URL from the service index; examples use nuget.org.

### PKG-NUGET-041 · Protocol conventions
Priority: **MVP**

| Topic | Rule |
|---|---|
| Entry point | The service index (PKG-NUGET-043); every other URL is discovered from it, never hard-coded by clients. |
| Encoding | Non-binary responses are JSON. New properties may appear at any time; consumers ignore properties they do not know. |
| Timestamps | ISO 8601, UTC unless stated otherwise. |
| Methods | GET reads; HEAD returns the headers of the matching GET; PUT creates or, where supported, updates; DELETE deletes or unlists. |
| Status codes | 200 with body, 201 created, 202 accepted (work continues asynchronously), 204 no body, 301/302 redirect, 400 invalid parameters, 401 invalid credentials, 403 not allowed, 404 not found, 409 conflict, 500 error, 503 temporarily unavailable. |
| Redirects | Any GET may answer 301 or 302; clients follow `Location`. |
| Retries | The official client retries three times on 5xx and on TCP or DNS errors. |
| Authentication | Defined by each server. nuget.org requires an API key only for publishing. |

Request headers:

| Header | Use |
|---|---|
| `X-NuGet-ApiKey` | API key for push, delete, relist (PKG-NUGET-018) |
| `X-NuGet-Protocol-Version` | Required by nuget.org in some cases only (push with protocol 4.1.0); other servers are not expected to implement nuget.org's verification-key protocol |
| `X-NuGet-Client-Version` | Deprecated predecessor of the protocol-version header, reserved for the official client |
| `X-NuGet-Session-Id` | Optional; NuGet 4.7+ sends one value for all requests of one PackageReference restore |

### PKG-NUGET-042 · Package IDs and versions on the wire
Priority: **MVP**

- Lowercasing: IDs and versions inside URLs are lowercased with .NET's invariant-culture rules
  (`ToLowerInvariant`).
- Versions inside URLs are normalized (PKG-NUGET-009) and carry no build metadata.
- Version strings in documents: flat-container version lists and registration page bounds are
  normalized without build metadata; registration catalog entries and search results carry the full
  normalized version, which may include build metadata.
- Ordering follows SemVer 2.0.0 precedence: a release sorts above its pre-releases; pre-release
  labels compare per dot-separated identifier, numeric identifiers numerically
  (`1.0.1-rc.10` > `1.0.1-rc.2`), others alphabetically (`1.0.1-alpha10` < `1.0.1-alpha2`).
- NuGet versions differ from plain SemVer: an optional fourth numeric part (revision); only the major
  part is required, missing parts equal zero; pre-release labels compare case-insensitively.
- SemVer levels: `semVerLevel` absent means 1.0.0. Below 2.0.0, servers must not return packages
  whose versions need SemVer 2.0.0; registration resources older than type 3.6.0 return only
  SemVer 1.0.0-compatible (or four-part) versions.

Version ranges (dependency `range` values), from
[package versioning](https://learn.microsoft.com/en-us/nuget/concepts/package-versioning):

| Notation | Meaning |
|---|---|
| `1.0` | ≥ 1.0 |
| `[1.0,)` | ≥ 1.0 |
| `(1.0,)` | > 1.0 |
| `[1.0]` | = 1.0 |
| `(,1.0]` | ≤ 1.0 |
| `(,1.0)` | < 1.0 |
| `[1.0,2.0]` | 1.0 ≤ x ≤ 2.0 |
| `(1.0,2.0)` | 1.0 < x < 2.0 |
| `[1.0,2.0)` | 1.0 ≤ x < 2.0 |
| `(1.0)` | invalid |

### PKG-NUGET-043 · Service index
Priority: **MVP**

[Service index](https://learn.microsoft.com/en-us/nuget/api/service-index): the JSON entry point of a
package source, answered on GET and HEAD.

| Property | Type | Required | Meaning |
|---|---|---|---|
| `version` | string | yes | Schema version, SemVer 2.0.0 parseable, major version 3; current value `3.0.0` (equivalent to the older `3.0.0-beta.1`) |
| `resources` | array | yes | Capabilities of the source |
| `resources[].@id` | string | yes | Absolute `http` or `https` URL of the resource |
| `resources[].@type` | string | yes | Resource type, usually `<Name>/<Version>`; clients look up the types they know |
| `resources[].comment` | string | no | Human-readable description |

Resources (Microsoft's list):

| Resource | Required | Entry |
|---|---|---|
| PackageBaseAddress | yes | PKG-NUGET-045 |
| PackagePublish | yes | PKG-NUGET-049 |
| RegistrationsBaseUrl | yes | PKG-NUGET-046 |
| SearchQueryService | yes | PKG-NUGET-047 |
| SearchAutocompleteService | no; clients disable autocomplete without it | PKG-NUGET-048 |
| SymbolPackagePublish | no | PKG-NUGET-050 |
| VulnerabilityInfo | no | PKG-NUGET-051 |
| RepositorySignatures | no | PKG-NUGET-052 |
| Catalog | no | PKG-NUGET-053 |
| PackageDetailsUriTemplate, OwnerDetailsUriTemplate, ReportAbuseUriTemplate, ReadmeUriTemplate | no | PKG-NUGET-054 |

- Several entries may share an `@id` or `@type`; the client chooses (for example round robin on
  failure).
- Each resource is versioned independently of the index schema. Because clients look up only the
  `@type` values they know, a server that wants to reach old and new clients lists each resource
  under every type and alias those clients use (for example several registration types,
  PKG-NUGET-046); this follows from the lookup rule and is not stated by Microsoft as a requirement.
- Resources on another host work but break clients in networks that allow only the feed's host.

### PKG-NUGET-044 · Resource URL placement for authenticated feeds
Priority: **MVP**

Since NuGet 6.7 (Visual Studio 17.7, .NET SDK 7.0.400), clients pre-authenticate only requests to URLs
in the same directory as, or a subdirectory of, a URL that was already authenticated
([implementation guide](https://learn.microsoft.com/en-us/nuget/api/implementation-guide)). With the
service index at `https://pkgs.example.com/nuget/v3/feed/index.json`:

| Resource URL | Pre-authenticated |
|---|---|
| `https://pkgs.example.com/nuget/v3/feed/search` | yes, same directory |
| `https://pkgs.example.com/nuget/v3/feed/registration/` | yes, subdirectory |
| `https://pkgs.example.com/nuget/v3/search` | no, not below the index's directory |
| `https://pkgs.example.com/nuget/v3/registration/` | no |
| `https://search.pkgs.example.com/nuget/v3/feed/` | no, other host |

- An alias name for a feed defeats pre-authentication for resources under the canonical name, unless
  the alias redirects to the canonical service index URL (which costs a redirect per index request).
- Consequence: every resource of a repository belongs under the directory of its service index, or
  each request first draws a 401.

### PKG-NUGET-045 · Package content (flat container)
Priority: **MVP**

[PackageBaseAddress](https://learn.microsoft.com/en-us/nuget/api/package-base-address-resource),
type `PackageBaseAddress/3.0.0`; GET and HEAD. Offered by the system only on normalized repositories
(PKG-NUGET-008).

| Request | Response |
|---|---|
| `GET {@id}/{LOWER_ID}/index.json` | 200 with `{"versions": [...]}`: every version the source holds, listed and unlisted, lowercased and normalized, no build metadata; every version in this list must be downloadable. 404 if the source has no version of the ID. |
| `GET {@id}/{LOWER_ID}/{LOWER_VERSION}/{LOWER_ID}.{LOWER_VERSION}.nupkg` | 200 with the package bytes; 404 if absent |
| `GET {@id}/{LOWER_ID}/{LOWER_VERSION}/{LOWER_ID}.nuspec` | 200 with the manifest XML from inside the package; 404 if absent |

The version strings of the list are meant to be used verbatim in the download URLs.

### PKG-NUGET-046 · Package metadata (registrations)
Priority: **MVP**

[RegistrationsBaseUrl](https://learn.microsoft.com/en-us/nuget/api/registration-base-url-resource);
GET and HEAD. Package metadata grouped by package ID; there is no way to discover IDs here.

| `@type` | Compression | SemVer 2.0.0 packages |
|---|---|---|
| `RegistrationsBaseUrl` (aliases `/3.0.0-beta`, `/3.0.0-rc`) | none | excluded |
| `RegistrationsBaseUrl/3.4.0` | gzip | excluded |
| `RegistrationsBaseUrl/3.6.0` | gzip | included |

Documents: `GET {@id}/{LOWER_ID}/index.json` returns the registration index; page and leaf URLs are
taken from it and must not be predicted by clients.

<details>
<summary>Index, page, leaf and catalog entry</summary>

Registration index: `count` (number of pages), `items` (pages).

Page object (inline in the index, or fetched from its `@id`): `@id`, `count` (leaves), `items` (leaves;
optional inline, required in a fetched page), `lower` and `upper` (inclusive SemVer 2.0.0 bounds,
normalized, no build metadata), `parent` (index URL; present when `items` is). nuget.org cuts the
leaves into pages of 64; below 128 versions every page is inlined in the index (so 65 to 127
versions give two inlined pages), from 128 versions on the pages are fetched separately. Servers
choose their own heuristic.

Leaf in a page: `@id` (leaf URL), `catalogEntry` (object), `packageContent` (URL of the `.nupkg`).

Catalog entry:

| Property | Type | Required | Notes |
|---|---|---|---|
| `@id` | string | yes | URL of the document the entry was produced from |
| `id` | string | yes | package ID |
| `version` | string | yes | full normalized version, may include build metadata |
| `authors` | string or array | no | |
| `dependencyGroups` | array | no | per target framework |
| `deprecation` | object | no | |
| `description`, `iconUrl`, `language`, `licenseUrl`, `licenseExpression`, `minClientVersion`, `projectUrl`, `readmeUrl`, `summary`, `title` | string | no | `readmeUrl` is a rendered web view |
| `listed` | boolean | no | absent means listed |
| `packageContent` | string | no | legacy duplicate of the leaf's property |
| `published` | string | no | ISO 8601; nuget.org uses year 1900 for unlisted versions |
| `requireLicenseAcceptance` | boolean | no | |
| `tags` | string or array | no | |
| `vulnerabilities` | array | no | `advisoryUrl`, `severity` as string `"0"` low to `"3"` critical |

Dependency group: `targetFramework` (optional; absent means all frameworks), `dependencies`. Dependency:
`id` (required), `range` (absent or empty means any version; `*` is not allowed; the samples carry a
string such as `[2.14.0, )`), `registration` (URL of the dependency's index).

Deprecation: `reasons` (at least one of `Legacy`, `CriticalBugs`, `Other`; case-insensitive; unknown
values ignored, only unknown values mean `Other`), `message`, `alternatePackage` (`id`, `range` or
`*`).

Registration leaf document (from a leaf's `@id`): `@id` (required), `catalogEntry` (URL), `listed`,
`packageContent`, `published`, `registration`.

</details>

- The registration resource does not carry owners, download counts or verification; those come from
  search (PKG-NUGET-047).
- A server offering only the old types hides SemVer 2.0.0 packages from every client.

### PKG-NUGET-047 · Search query service
Priority: **MVP**

[SearchQueryService](https://learn.microsoft.com/en-us/nuget/api/search-query-service-resource), types
`SearchQueryService` (aliases `/3.0.0-beta`, `/3.0.0-rc`) and `SearchQueryService/3.5.0` (adds the
package-type filter); GET and HEAD.

`GET {@id}?q=&skip=&take=&prerelease=&semVerLevel=&packageType=`

| Parameter | Meaning |
|---|---|
| `q` | Search terms; interpretation is server-defined; empty returns all packages |
| `skip` | Results to skip; default 0 |
| `take` | Results to return; greater than 0; servers may cap it |
| `prerelease` | Include pre-releases; default false |
| `semVerLevel` | `2.0.0` includes SemVer 2.0.0 packages; absent means 1.0.0 |
| `packageType` | Only packages with at least one matching author-defined package type; a name that is not a valid package type gives an empty result; empty value means no filter |

Response: `totalHits` (matches regardless of paging), `data` (one object per package ID, metadata from
the latest version).

<details>
<summary>Search result object</summary>

| Property | Required | Notes |
|---|---|---|
| `id` | yes | |
| `version` | yes | full SemVer 2.0.0 version, may include build metadata |
| `versions` | yes | versions matching the `prerelease` filter: `@id` (registration leaf URL), `version`, `downloads` |
| `packageTypes` | yes (3.5.0) | at least one `{name}` from the latest version |
| `description`, `summary`, `title`, `iconUrl`, `licenseUrl`, `projectUrl`, `registration` | no | `registration` is the absolute registration index URL |
| `authors`, `tags`, `owners` | no | string or array; `owners` are usernames of accounts allowed to publish; omit it rather than echo the manifest's owners field |
| `totalDownloads` | no | sum of the versions' downloads |
| `verified` | no | nuget.org: ID prefix reserved and owned by the uploader; Visual Studio shows a check mark |
| `deprecation` | no | of the latest version |
| `vulnerabilities` | no | of the latest version; `severity` as integer 0 to 3; empty array if none |

</details>

Unlisted versions never appear.

### PKG-NUGET-048 · Search autocomplete service
Priority: **P1**

[SearchAutocompleteService](https://learn.microsoft.com/en-us/nuget/api/search-autocomplete-service-resource),
types `SearchAutocompleteService` (aliases `/3.0.0-beta`, `/3.0.0-rc`) and `/3.5.0` (package-type
filter); GET and HEAD. Optional; clients disable autocomplete when a source lacks it.

| Request | Parameters | Response |
|---|---|---|
| ID completion `GET {@id}?q=&skip=&take=&prerelease=&semVerLevel=&packageType=` | as search (PKG-NUGET-047); `q` matched against IDs (nuget.org: prefixes of ID tokens split at camel case and symbols) | `totalHits`, `data` (array of IDs); packages with only unlisted versions are absent |
| Version listing `GET {@id}?id=&prerelease=&semVerLevel=` | `id` required | `data` (array of version strings; build metadata possible with `semVerLevel=2.0.0`); unlisted versions absent |

### PKG-NUGET-049 · Push, delete and relist (package publish)
Priority: **MVP**

[PackagePublish](https://learn.microsoft.com/en-us/nuget/api/package-publish-resource), type
`PackagePublish/2.0.0`. Same protocol as the legacy v2 push endpoint. All requests carry
`X-NuGet-ApiKey`.

| Request | Body | Responses |
|---|---|---|
| `PUT {@id}` | `multipart/form-data`; the first part is the raw `.nupkg`; further parts, file names and part headers are ignored | 201 or 202 pushed; 400 invalid package; 409 ID and version exist |
| `DELETE {@id}/{ID}/{VERSION}` | none | 204 deleted; 404 not found |
| `POST {@id}/{ID}/{VERSION}` (relist) | none | 200 listed (also if it already was); 404 not found |

- The API key is opaque; its length should fit an HTTP header.
- Delete may be implemented as hard delete, soft delete or unlist (PKG-NUGET-022).
- Replacing an existing package on push is allowed for servers other than nuget.org.

### PKG-NUGET-050 · Symbol package publish
Priority: **MVP**

[SymbolPackagePublish](https://learn.microsoft.com/en-us/nuget/api/symbol-package-publish-resource),
type `SymbolPackagePublish/4.9.0`; PUT only.

`PUT {@id}` with `X-NuGet-ApiKey` and the same multipart body as a package push, carrying a `.snupkg`.

| Status | Meaning |
|---|---|
| 201 | Symbol package pushed |
| 400 | Invalid symbol package |
| 401 | Not authorized |
| 404 | No package with the same ID and version |
| 409 | A symbol package with this ID and version was pushed but is not available yet |
| 413 | Too large |

The same symbol package may be pushed repeatedly. Symbol publishing is a v3-only feature; that a
source without this resource offers v3 clients no symbol push target is inferred from the lookup rule
of the service index, not stated by Microsoft (PKG-NUGET-029, PKG-NUGET-043).

### PKG-NUGET-051 · Vulnerability information
Priority: **P1**

[VulnerabilityInfo](https://learn.microsoft.com/en-us/nuget/api/vulnerability-info), type
`VulnerabilityInfo/6.7.0`. NuGet 6.7+ downloads it during restore to audit packages locally.

- Index (`{@id}`): JSON array of 1 to 16 objects: `@name` (unique, 1 to 32 characters of
  `A-Z a-z 0-9 - _`, cache key), `@id` (absolute URL of a page), `@updated` (ISO 8601, ideally UTC),
  `comment` (optional). A server without vulnerability data removes the resource from the service
  index.
- Page: JSON object keyed by lowercased package ID; each value is an array of `severity` (integer: 0
  low, 1 medium, 2 high, 3 critical), `url` (advisory), `versions` (vulnerable range,
  PKG-NUGET-042). Entries sorted by descending maximum version, then descending minimum version,
  then ascending URL; unbounded ends first.
- Clients merge pages additively, in any order; a page cannot retract another page's entry, so a
  changed advisory means rewriting the page that holds it.
- Suggested partitioning: a large, rarely regenerated base page and a small update page with newer
  advisories; any change or deletion regenerates the base and empties the update page.
- A server hosting only first-party packages without known vulnerabilities offers pages with empty
  content. Microsoft's text calls an empty page an empty JSON array (`[]`) although pages are objects
  otherwise.
- Instead of the resource, users can add nuget.org's vulnerability-only service index
  (`https://data.nuget.org/v3/index.json`) as an audit source (PKG-NUGET-036).

### PKG-NUGET-052 · Repository signatures
Priority: **P2**

[RepositorySignatures](https://learn.microsoft.com/en-us/nuget/api/repository-signatures-resource),
types `/4.7.0`, `/4.9.0` (NuGet 4.9+), `/5.0.0` (allows `allRepositorySigned: true`, NuGet 5.0+).
`{@id}` must be HTTPS; GET and HEAD.

| Property | Notes |
|---|---|
| `allRepositorySigned` | true only if every package carries a repository signature by a listed certificate; must be false on 4.7.0 and 4.9.0 (older clients cannot install from a source that sets it) |
| `signingCertificates[]` | `contentUrl` (HTTPS URL of the DER certificate), `fingerprints` (object with key `2.16.840.1.101.3.4.2.1` = SHA-256 fingerprint, lowercase hex), `subject`, `issuer`, `notBefore`, `notAfter` |

- Certificates are normally only appended. Removing one invalidates all signatures made with it;
  after a compromise the source re-signs the affected packages and removes the certificate.
- True with an empty certificate list makes every package invalid.
- Clients can restrict trust per repository (trusted signers, PKG-NUGET-036).

### PKG-NUGET-053 · Catalog
Priority: **P2**

[Catalog](https://learn.microsoft.com/en-us/nuget/api/catalog-resource), type `Catalog/3.0.0`; GET and
HEAD. An append-only, time-ordered log of package events (created, unlisted, relisted, deprecated,
vulnerability changes, deleted, reflowed). Not used by the official client; consumers are mirrors and
indexers.

- Index: `commitId`, `commitTimeStamp`, `count`, `items` (pages: `@id`, `commitId`,
  `commitTimeStamp`, `count`; unordered).
- Page: `commitId`, `commitTimeStamp`, `count`, `parent`, `items` (`@id`, `@type`
  `nuget:PackageDetails` or `nuget:PackageDelete`, `commitId`, `commitTimeStamp`, `nuget:id`,
  `nuget:version`). nuget.org holds at most 550 items per page. Only the newest page receives items.
- Leaf: `@type`, `catalog:commitId`, `catalog:commitTimeStamp`, `id`, `version`, `published`; detail
  leaves add the manifest fields plus required `packageHash` (base64), `packageHashAlgorithm`
  (nuget.org: SHA512) and `packageSize`, and optional `created`, `isPrerelease`, `listed`,
  `verbatimVersion`, `packageTypes`, `deprecation`, `vulnerabilities` (unknown severities count as
  low).
- Commits are strictly increasing in time; one commit per timestamp; at most one item per package
  version per commit. Consumers keep a cursor (the last processed commit timestamp, never their own
  clock), start at the minimum timestamp, and process items idempotently; dependent consumers never
  advance past the cursor of the consumer they depend on.

### PKG-NUGET-054 · URI template resources
Priority: **P2**

Optional templates that clients fill in to show links or fetch extra content.

| Resource | Type | Placeholders | Rules |
|---|---|---|---|
| [Package details page](https://learn.microsoft.com/en-us/nuget/api/package-details-template-resource) | `PackageDetailsUriTemplate/5.1.0` | `{id}`, `{version}` | absolute HTTPS URL; server accepts any casing and non-normalized versions |
| [README download](https://learn.microsoft.com/en-us/nuget/api/readme-template-resource) | `ReadmeUriTemplate/6.13.0` | `{lower_id}`, `{lower_version}` (normalized, no build metadata) | GET returns the README (200) or 404; clients build the URL and probe it to learn whether a README exists, so servers should expect many requests while users browse |
| [Owner profile page](https://learn.microsoft.com/en-us/nuget/api/owner-details-template-resource) | `OwnerDetailsUriTemplate/6.11.0` | `{owner}` (username from search `owners`) | absolute HTTPS URL; clients keep the username's casing |
| [Report abuse page](https://learn.microsoft.com/en-us/nuget/api/report-abuse-resource) | `ReportAbuseUriTemplate/3.0.0-beta`, `/3.0.0-rc` | `{id}`, `{version}` | case- and normalization-insensitive; without it the official client falls back to nuget.org's report-abuse URL |

### PKG-NUGET-055 · Embedded icon, license and README files
Priority: **P1**

Packages may embed their icon, license file and README. A server that shows package details or feeds
Visual Studio's package manager makes them reachable without downloading the whole package, either
extracted to static files or extracted on request, and puts the hosted URLs into `iconUrl`,
`licenseUrl` and `readmeUrl` of package metadata and search answers
([implementation guide](https://learn.microsoft.com/en-us/nuget/api/implementation-guide)).

- The `.nupkg` itself is never modified; lock files and signatures would flag it as tampered.
- A license is either an SPDX expression or an embedded file, never both. For an expression,
  `licenseUrl` may be `https://licenses.nuget.org/` followed by the URL-encoded expression.
- The README download template (PKG-NUGET-054) is the newer way to serve READMEs.

## Notes for implementers

- Clients send credentials only after a 401. With anonymous access on, a repository without forced
  authentication does not challenge, so every client is served as the anonymous user even when it
  has credentials configured (PKG-NUGET-016).
- Keep every resource of a repository below the directory of its service index, or NuGet 6.7+ sends
  each request unauthenticated first (PKG-NUGET-044).
- Answer the service index both with and without a trailing `index.json`; clients are configured both
  ways, and a source URL not ending in `.json` defaults to v2 unless `protocolVersion="3"` is set
  (PKG-NUGET-002).
- The API key in client setup is `<username>:<secret>`, not a bare token; accept that form for push,
  delete and relist. Whether a bare token must also work is an open decision (PKG-NUGET-018).
- IDs and versions in URLs are lowercased with invariant-culture rules and versions are normalized
  without build metadata, while documents carry full versions with build metadata (PKG-NUGET-042).
- Without `semVerLevel=2.0.0`, and for registration types below 3.6.0, SemVer 2.0.0 packages must be
  invisible; offering only old registration types hides them from every client (PKG-NUGET-014,
  PKG-NUGET-046).
- Normalization is a creation-time decision, strips build metadata instead of rejecting, rejects
  non-SemVer versions with 409, and must match across every replication, federation and chain pair
  (PKG-NUGET-008, PKG-NUGET-011).
- The flat container's version list includes unlisted versions; search and autocomplete never do
  (PKG-NUGET-022, PKG-NUGET-045).
- Severity is a string in registration and catalog vulnerabilities but an integer in search and
  vulnerability pages (PKG-NUGET-046, PKG-NUGET-047, PKG-NUGET-051).
- A remote proxy's service index must point at the system, not at nuget.org; otherwise clients and
  chained instances bypass the cache and fail in networks that block nuget.org (PKG-NUGET-024,
  PKG-NUGET-025).
