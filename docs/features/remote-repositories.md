# Remote repositories

A remote repository is a pull-through proxy and cache for exactly one upstream package source: a
public registry such as the npm registry or nuget.org, a private registry, a repository on another
instance of the same kind of system, or a repository shared from another project on the same
instance. This page covers the remote repository class end to end: the request path from cache
miss to cached artifact, every remote setting with its default, the cache view and its
invalidation, behavior when the upstream is slow, failing or unreachable, chained instances that
proxy each other, project-scoped connections to a shared repository, parallel chunked download of
large files, and the outbound HTTP proxy definitions that remote repositories use. Settings shared
by all repository classes (key rules, descriptions, package type, environments, virtual
aggregation) are described in [repositories.md](repositories.md); per-format upstream behavior
(npm metadata rewriting, NuGet feed paths, Docker token handshakes, Go VCS lookups and so on) is
described in the format pages under [formats/README.md](formats/README.md).

## Remote repository model

### RMT-001 · Remote repository (pull-through proxy and cache)
Priority: **MVP**

A remote repository answers client requests for one upstream URL and keeps what it fetched. It is a
proxy, not a mirror: nothing is fetched ahead of time. A freshly created remote repository has an
empty cache; an item enters the cache only when a client first asks for it.

Request path for a download from a remote repository:

1. The repository must take part in resolution: it is skipped when artifact resolution is disabled
   for it (see RMT-030), and the requested path must be admitted by its include and exclude
   patterns (RMT-034).
2. If the item is cached and still valid, it is served from the cache. Binaries that never change
   (release versions) stay valid until they are removed. Metadata files that change upstream
   (for example `maven-metadata.xml`, the npm package document, a Docker `manifest.json`) are valid
   for the metadata retrieval cache period (RMT-018).
3. If the item is missing or its cached metadata has expired, the system contacts the upstream,
   unless the repository is offline (RMT-030), the whole system is offline (RMT-031), the
   repository is currently assumed offline (RMT-029), or the path is in the negative cache
   (RMT-020).
4. On success the content is checked against the checksum policy (RMT-022) and the MIME-type check
   (RMT-023), stored in the cache (unless the repository is pass-through, RMT-017) and returned to
   the client.
5. On a miss or an error the outcome follows RMT-033 (serve stale content, cache the miss, or fail).

Rules that hold for every remote repository:

- Clients cannot deploy into a remote repository. Cached items can be removed (RMT-003).
- Reading and populating are separate permissions. A principal with read permission on a remote
  repository can download only items that are already cached. Fetching an item that is not yet
  cached requires the deploy/cache permission on that remote repository. A permission policy can
  target "every remote repository", which includes remote repositories created later. A virtual
  repository has no permissions of its own; access through it uses the permissions on its member
  remote and local repositories (see [identity-and-access.md](identity-and-access.md)).
- The package type is chosen at creation and never changes. The set of package types that can be
  remote differs from the local set (for example, there is no remote repository for a Terraform
  state backend or for Vagrant boxes); the matrix lives in [formats/README.md](formats/README.md).
- Two kinds exist: an *external* remote repository points at an upstream URL; an *internal* remote
  repository points at a repository shared from another project on the same instance (RMT-047). A
  remote repository whose upstream is another instance of the same kind of system can be marked as
  a chained remote repository (RMT-040).

Naming limits specific to remote repositories: the key may be at most 58 characters (local and
virtual keys allow 64), and no repository key may end with `-cache`, because the cache view of a
remote repository is addressed as the remote key followed by `-cache` (RMT-003). The remaining key
rules are in [repositories.md](repositories.md).

Format-specific upstream settings are owned by the format pages, not by this page. They include:
separate registry or index URLs (Bower, Cargo, Composer, PyPI with its index suffix, Terraform
registry and providers URLs, CocoaPods specs and CDN URLs), Git provider selection for formats
resolved from source hosts, NuGet download and feed context paths, v3 service index URL and symbol
server URL, Docker and Helm-via-OCI foreign-layer caching with an allow list, dependency rewrite
allow lists,
a registry project identifier for some container registries, a public key for Hex, and forced
authentication flags. See [formats/README.md](formats/README.md).

UI: administration, repositories, "Remote" tab; creating one starts with "create repository",
class remote, package type, then a basic tab, an advanced tab and a replications tab (replication
is described in [replication-and-federation.md](replication-and-federation.md)). The repository
list can be filtered by upstream URL, and an "inactive" view lists remote repositories that are
offline.

Interactions: remote fetches can be intercepted by server-side hooks (before the upstream is
asked whether an item exists, for example to add a header; before and after a remote download;
substitution of the remote path; substitution of the remote content; replacement of the response
the upstream returned); see
[events-and-extensibility.md](events-and-extensibility.md). Vulnerability indexing of a remote
repository's cache and upstream curation of what may be fetched are described in
[security-scanning-and-curation.md](security-scanning-and-curation.md). Pull replication into a
remote repository's cache is described in
[replication-and-federation.md](replication-and-federation.md).

### RMT-002 · Remote repository configuration operations
Priority: **MVP**

A remote repository is one configuration document in the management API. Every setting on this
page is a field of that document. A field omitted on create takes its default. The configuration
can also be supplied declaratively (configuration as code, and a bootstrap file read at first
start); see [operations-and-monitoring.md](operations-and-monitoring.md).

<details>
<summary>Operations</summary>

| Operation | Inputs | Output | Required privilege |
|---|---|---|---|
| Create repository | Repository key; complete configuration document with class "remote" | Plain-text confirmation | Administrator |
| Read repository configuration | Repository key | The configuration document with every field, including fields that do not apply to the package type. Non-administrators receive only a partial document when they are allowed to see the repository at all; for a project-scoped remote repository the partial document still contains the shared repository key. | Administrator for the full document |
| Read repository configuration, package-type filtered | Repository key | The configuration document restricted to fields that apply to the repository's package type | Administrator for the full document |
| Update repository configuration | Repository key; partial or full document | Confirmation | Administrator |
| Delete repository | Repository key | Report with the key and the number of items deleted; on partial failure a report with a capped sample of per-item errors | Administrator |
| Read shared remote configuration (deprecated) | Repository key | The configuration of a remote repository that has configuration sharing enabled (RMT-039), with sensitive data removed for non-administrators | Any authenticated user for a shared remote repository; administrator otherwise |

</details>

Rules:

- Create fails with a client error (400) when the key already exists, the document is invalid, or
  a project-scoped remote repository names a repository that is not connectable (RMT-047).
- The upstream URL is mandatory on create, except for a project-scoped remote repository.
- Update accepts a partial document. The repository class cannot be changed. A regular remote
  repository can be turned into a project-scoped one and back (RMT-047).
- Delete removes the configuration and every cached item. When some cached items cannot be
  deleted, the configuration is kept (multi-status answer) and the call must be repeated; the
  generic delete semantics are in [repositories.md](repositories.md).
- Clients must accept unknown fields in the document; the set of settings grows over time.
- Batch create, update, read and delete of several repositories in one call are generic repository
  operations; see [repositories.md](repositories.md) and
  [api-content-operations.md](api-content-operations.md).

<details>
<summary>Settings index (every remote setting on this page)</summary>

| Setting | Default | Entry |
|---|---|---|
| Upstream URL | none; mandatory except for project-scoped remotes | RMT-004 |
| Upstream user name | empty (anonymous) | RMT-005 |
| Upstream password or access token | empty | RMT-005 |
| Send credential as bearer token | off | RMT-006 |
| Client TLS certificate (alias) | none | RMT-007 |
| Custom HTTP headers | none | RMT-009 |
| Static query parameters | none | RMT-010 |
| Propagate client query parameters (generic format) | unspecified | RMT-011 |
| Forward credentials to redirect hosts | off | RMT-012 |
| Cookie management | off | RMT-013 |
| Disable URL normalization | off | RMT-014 |
| Socket timeout | 15,000 ms | RMT-015 |
| Source interface (local address) | none | RMT-015 |
| Outbound proxy (key) | none, or the default proxy | RMT-016 |
| No proxy | off | RMT-016 |
| Store artifacts locally | on | RMT-017 |
| Metadata retrieval cache period | 7,200 s (21,600 s for Docker, OCI, Helm via OCI) | RMT-018 |
| Metadata retrieval timeout | 60 s | RMT-019 |
| Negative cache period | 1,800 s (600 s for Go) | RMT-020 |
| Unused cached artifact cleanup period | 0 h (disabled) | RMT-021 |
| Remote checksum policy | generate if absent | RMT-022 |
| Block mismatching MIME types | on | RMT-023 |
| MIME types exempt from blocking | none | RMT-023 |
| Bypass HEAD requests | off | RMT-024 |
| Eagerly fetch jars / sources | off / off | RMT-025 |
| Download redirect | off | RMT-028 |
| Assumed-offline period | 300 s | RMT-029 |
| Offline | off | RMT-030 |
| Disable artifact resolution | off | RMT-030 |
| Hard fail | off | RMT-032 |
| Include patterns / exclude patterns | everything / nothing | RMT-034 |
| Cache layout / remote layout mapping | unspecified / none | RMT-035 |
| Handle releases / handle snapshots | on / on | RMT-036 |
| Maximum unique snapshots | 0 (unlimited) | RMT-036 |
| Suppress POM consistency checks | off | RMT-036 |
| Priority resolution | off | RMT-037 |
| List remote folder items | off | RMT-038 |
| Share configuration | off | RMT-039 |
| Chained remote (content synchronization) | off | RMT-040 |
| Report download statistics | off | RMT-041 |
| Synchronize properties | off | RMT-042 |
| Source absence detection | off | RMT-043 |
| Project-scoped upstream, shared repository key | off, none | RMT-047 |
| Property sets | none | RMT-003 |
| Allow archive content browsing | unspecified | ART-045 in [artifacts.md](artifacts.md) |
| CDN download redirect (hosted only) | off | REPO-020 in [repositories.md](repositories.md) |
| Security indexing | off | see [security-scanning-and-curation.md](security-scanning-and-curation.md) |

</details>

UI: the create and edit forms of a remote repository (basic and advanced tabs).

### RMT-003 · Cache view and cached-content management
Priority: **MVP**

Everything a remote repository has fetched lives in its cache, which behaves like a read-mostly
repository of its own.

- Two addresses: the remote key resolves through the full request path of RMT-001 and may contact
  the upstream; the remote key followed by `-cache` serves only what is already cached and never
  triggers an upstream check. Clients use the second form to avoid update checks.
- Search operations can be limited to local and cache repositories. The latest-version search
  uses local and cache repositories by default and asks upstreams only on explicit request (which
  can slow the search considerably). A latest-version token in a download path is resolved from
  local, cache and virtual repositories only; a remote repository addressed directly is not
  asked. A latest-version request through a virtual repository is different: it checks every
  member, including remote ones, even when a local or cached version exists, and fetches and
  caches a newer version if an upstream has one (see [search-and-query.md](search-and-query.md)
  and [repositories.md](repositories.md)).
- Package-type search (search by package metadata properties) does not work on the caches of
  RubyGems, Debian and PHP Composer remote repositories.
- Cached files and folders can be deleted; this requires delete permission on the remote repository
  (anonymous access counts if it has been granted).
- Properties can be added to and removed from cached items, like items in a local repository.
  Property sets selected on the remote repository constrain which properties are offered (see
  [artifacts.md](artifacts.md)).
- Cached items can be copied to a local repository, for example to keep a package that has
  disappeared upstream (RMT-043).
- File information for a cached item carries the upstream address it came from (exact content
  unspecified).
- Folder download as an archive works for local and cache repositories, not for the remote key
  itself. When the "include checksum files" option is chosen for a folder from a cache, checksum
  files are always written; the checksum-policy condition that applies to local repositories does
  not apply to caches.
- A cache can be mounted as a WebDAV share like a local repository (ART-047 in
  [artifacts.md](artifacts.md)).
- In the browser tree the repository order is virtual, local, remote, cached by default; the
  order is operator-configurable.

UI: the artifact browser shows each remote repository and its cache; items listed from the
upstream but not yet fetched are marked as not cached (RMT-038).

Interactions: cache invalidation (RMT-026) expires metadata without deleting it; unused-artifact
cleanup (RMT-021) deletes cached items nobody uses; package cleanup policies do not apply to remote
repositories
(see [retention-and-cleanup.md](retention-and-cleanup.md)).

## Upstream connection

### RMT-004 · Upstream URL
Priority: **MVP**

The base URL of the upstream source. Request paths below the remote repository are appended to it
(after optional layout translation, RMT-035).

| Setting | Meaning | Default |
|---|---|---|
| Upstream URL | Base address of the upstream. Only `http` and `https` are accepted. | none; mandatory for external remote repositories |

Edge cases:

- For many formats the upstream address is not a plain file tree but the registry's API root; the
  format page states what URL a user enters (for example nuget.org for v2 downloads, plus a
  separate v3 service index URL).
- A chained remote repository points at the package-protocol address of the source repository on
  the other instance, which for many formats includes a format-specific API path segment
  (RMT-040).
- After the URL of an existing repository is changed, metadata cached from the old upstream can
  still be served, for example when the refresh after an invalidation fails (RMT-026). Whether a
  URL change clears the cache is unspecified.
- Pointing the URL at a third-party gateway that intervenes in package-manager protocol traffic
  (custom routing or rewriting in front of the system) is unsupported and may be refused.

UI: basic tab, URL field. The UI offers a test action that checks the connection and credentials
against the upstream (result format unspecified). When the URL is recognised as another instance
of the same kind of system, the UI marks it as such (RMT-040).

### RMT-005 · Upstream credentials
Priority: **MVP**

Credentials the system presents to the upstream.

| Setting | Meaning | Default |
|---|---|---|
| User name | Account at the upstream. Empty means anonymous access. | empty |
| Password or access token | Password, or a personal access token issued by the upstream service. | empty |

Behavior:

- Credentials are sent as HTTP Basic authentication unless bearer mode is enabled (RMT-006).
- A personal access token can replace the password for any package type. When a token is used,
  the user name may be arbitrary but must not be empty; exception: the npm public registry needs
  the real npm user name together with the generated token.
- Credentials are sent only to the upstream host; forwarding them to hosts the upstream redirects
  to is a separate opt-in (RMT-012).
- For a chained remote repository the credentials are a user name and password, or an access token
  issued by the source instance.

Security: the upstream credential is a stored secret. How it is protected at rest and whether it is
masked in configuration reads is unspecified (the sensitive-header mechanism of RMT-009 is the only
known masking rule).

UI: basic tab, remote authentication section.

### RMT-006 · Bearer token authentication to the upstream
Priority: **P1**

| Setting | Meaning | Default |
|---|---|---|
| Send credential as bearer token | The `Authorization` header uses the Bearer scheme instead of Basic. For Docker and Helm-via-OCI remotes the same option enables token-based (bearer) authentication as required by Docker Hub and other token-based registries. | off |

Edge cases: for a Docker remote that is the target of both event-based and scheduled pull
replication from another instance, this option must stay off. Format details are in the Docker and
OCI format pages under [formats/README.md](formats/README.md).

UI: basic tab.

### RMT-007 · Client TLS certificate for the upstream
Priority: **P1**

Some upstreams accept only clients that present a TLS client certificate (mutual TLS). The system
keeps a store of client certificates, each with an alias, and a remote repository selects one.

| Setting | Meaning | Default |
|---|---|---|
| Client TLS certificate | Alias of a stored client certificate presented on TLS connections to the upstream | none |

Certificate store:

- Each entry is one PEM document containing a private key and its certificate. Password-protected
  PEM files are not supported.
- Operations, all administrator-only:

| Operation | Inputs | Output |
|---|---|---|
| List client certificates | none | Per certificate: alias, subject ("issued to"), issuer, issue date, expiry date, fingerprint |
| Add client certificate | Alias; PEM body | Status and confirmation message; client error (400) for an invalid format |
| Delete client certificate | Alias | Status and confirmation; not found (404) for an unknown alias |

UI: administration, security, certificates (create with alias plus pasted or dropped PEM file); the
remote repository form selects the alias and offers a test action to verify it.

Interactions: trust in the upstream's server certificate is a separate, system-wide mechanism
(RMT-008).

### RMT-008 · Trust for upstream server certificates
Priority: **P1**

The system refuses TLS connections to an upstream whose server certificate does not chain to a
trusted certificate authority. To proxy an upstream with a self-signed or privately issued
certificate, the operator adds that certificate (or its CA) to the trusted certificate set of
every service process that opens such connections. The same trust set is used for outbound
proxies with self-signed certificates (RMT-048) and other outbound TLS connections. This is system
configuration, not a per-repository setting; a per-repository "skip verification" option does not
exist.

Limits: where operators cannot change the trust set (hosted deployments), such upstreams cannot be
proxied; for a self-hosted product this is an operator task (see
[ha-and-deployment.md](ha-and-deployment.md)).

### RMT-009 · Custom HTTP headers to the upstream
Priority: **P1**

A remote repository can add fixed HTTP headers to every request it sends to its upstream URL, for
example an API key header.

| Setting | Meaning | Default |
|---|---|---|
| Custom HTTP headers | List of name, value and a "sensitive" flag. Header names are validated against a system-wide rule set (rules unspecified). A sensitive value is stored encrypted and masked in API responses. | none |

Edge cases: whether the headers are also sent to hosts the upstream redirects to is unspecified.

### RMT-010 · Static query parameters
Priority: **P2**

| Setting | Meaning | Default |
|---|---|---|
| Query parameters | A fixed query string (for example `p1=v1&p2=v2`) added to every HTTP request sent to this upstream | none |

UI: advanced tab, network settings.

### RMT-011 · Client query parameter propagation
Priority: **P2**

| Setting | Meaning | Default |
|---|---|---|
| Propagate query parameters | Query parameters that the client sent to the system are forwarded unchanged to the upstream | unspecified |

Limits: offered for generic-format remote repositories only. How propagated parameters interact
with the cache key (whether two requests that differ only in parameters share one cached item) is
unspecified.

UI: advanced tab of a generic remote repository.

### RMT-012 · Credential forwarding on cross-host redirects
Priority: **P1**

| Setting | Meaning | Default |
|---|---|---|
| Forward credentials to redirect hosts | When the upstream answers with a redirect to a different host, the repository's credentials are sent to that host too. When off, credentials go only to the configured upstream host. | off |

Security: enabling it hands the upstream credential to whichever host the upstream redirects to.
Some upstreams need it: the Oracle Maven repository, for example, works only with credentials set,
this option on and cookie management (RMT-013) on.

Rules for following upstream redirects beyond the credential question (hop limits, allowed
schemes) are unspecified.

UI: advanced tab, network settings.

### RMT-013 · Cookie management
Priority: **P2**

| Setting | Meaning | Default |
|---|---|---|
| Cookie management | The repository keeps cookies set by the upstream and returns them on later requests, for upstreams that track client state with cookies | off |

Scope of the cookie store (per repository, per node, lifetime) is unspecified.

UI: advanced tab, network settings.

### RMT-014 · URL normalization control
Priority: **P2**

By default the system normalizes outgoing upstream URLs, for example by collapsing `//` into `/`.

| Setting | Meaning | Default |
|---|---|---|
| Disable URL normalization | Send request and redirect URLs exactly as given, for upstreams whose redirect targets legitimately contain `//` | off; set automatically when a Docker remote is created for Azure Container Registry (`*.azurecr.io`), Microsoft Container Registry (`mcr.microsoft.com`) or the NavOps registry (`registry.navops.io`) |

UI: advanced tab.

### RMT-015 · Network timeouts and source interface
Priority: **MVP**

| Setting | Meaning | Default |
|---|---|---|
| Socket timeout | Milliseconds to wait for a connection to be established and for an unanswered request before giving up. A timeout counts as a retrieval failure and puts the repository into the assumed-offline state (RMT-029). | 15,000 ms |
| Local address | On a host with several network interfaces, the local IP address used for upstream connections (for example to pass a firewall that allows one interface) | none (system chooses) |

The metadata retrieval timeout (RMT-019) is a separate, longer limit for metadata refreshes.

UI: advanced tab, network settings.

### RMT-016 · Outbound proxy selection per repository
Priority: **P1**

| Setting | Meaning | Default |
|---|---|---|
| Proxy | Key of an outbound HTTP proxy definition (RMT-048) used for this repository's upstream traffic | none; a new remote repository receives the default proxy when one is defined (RMT-049) |
| No proxy | Use no proxy for this repository, and keep this choice when system proxies are edited | off |

Behavior: when system proxy definitions change, repositories may have their proxy selection
updated automatically; a repository with "no proxy" set is excluded from such updates and never
uses a proxy. Exact update rules are unspecified.

UI: advanced tab, network settings.

## Caching policy

### RMT-017 · Store artifacts locally or pass through
Priority: **P1**

| Setting | Meaning | Default |
|---|---|---|
| Store artifacts locally | On: fetched items are written to the cache. Off: the repository streams upstream content straight to the client without keeping it (pass-through). | on |

Use: a satellite instance on a fast network next to a central instance can pass through instead of
duplicating the central cache.

Limits: the setting does not apply to Ansible, OCI, Helm-via-OCI and Hugging Face remote
repositories. A pass-through repository cannot use download redirect (RMT-028), because redirect
needs the item in storage. How metadata caching, negative caching and offline serving behave for a
pass-through repository is unspecified.

UI: advanced tab, other settings.

### RMT-018 · Metadata retrieval cache period
Priority: **MVP**

How long a cached metadata file is trusted before the upstream is asked for a newer version.
Binaries that do not change are not affected; for Maven the period matters only for snapshots,
since releases are treated as immutable. Which files count as expiring metadata is defined per
format (see [formats/README.md](formats/README.md)).

| Setting | Meaning | Default |
|---|---|---|
| Metadata retrieval cache period | Seconds a cached metadata file is served without checking the upstream | 7,200 s; 21,600 s for Docker, OCI and Helm-via-OCI remotes |

Limits:

- Minimum 30 s; for a NuGet remote whose upstream is `community.chocolatey.org` the minimum is
  14,400 s.
- Repositories that already hold a value below 30 s keep it until they are next edited.
- The declarative configuration describes 0 as "no caching", which conflicts with the UI minimum
  of 30 s. An implementation compatible with existing configurations must accept stored
  values below the minimum.

The same period controls how long remote folder listings are cached (RMT-038) and when properties
of a chained remote are re-synchronized (RMT-042). Virtual repositories of some formats have their
own metadata cache period for merged metadata (see [repositories.md](repositories.md)).

UI: advanced tab, cache settings.

### RMT-019 · Metadata retrieval timeout with stale fallback
Priority: **MVP**

| Setting | Meaning | Default |
|---|---|---|
| Metadata retrieval timeout | Seconds the system waits for a metadata refresh from the upstream. When it elapses, the previously cached metadata is returned to the client instead of an error. | 60 s |

Interactions: this is the main "serve stale" rule for metadata; see RMT-033.

UI: advanced tab, cache settings.

### RMT-020 · Negative cache period
Priority: **MVP**

| Setting | Meaning | Default |
|---|---|---|
| Negative cache period | Seconds during which an upstream "404 not found" for a path is remembered; requests for that path are answered as not found without asking the upstream | 1,800 s; 600 s for Go remotes |

A value of 0 disables negative caching: every request for a missing item goes to the upstream.

Edge cases: a package version published upstream shortly after a client asked for it stays
invisible through this repository until the entry expires (or the cache is invalidated, RMT-026).
Whether invalidation clears negative entries is unspecified.

UI: advanced tab, cache settings.

### RMT-021 · Unused cached artifact cleanup
Priority: **P1**

| Setting | Meaning | Default |
|---|---|---|
| Unused cached artifact cleanup period | Hours after which a cached artifact that has not been used is marked as a cleanup candidate; marking deletes nothing, the marked artifacts are removed by the next run of the cleanup job | 0 (disabled; cached artifacts are kept forever). An empty field in the UI means the same. |

The cleanup job is a system-wide maintenance task with its own cron schedule, set by the operator
in the maintenance settings or through configuration as code (default schedule unspecified; the
examples run it daily at noon). What counts as use is unspecified (see
[retention-and-cleanup.md](retention-and-cleanup.md) and
[operations-and-monitoring.md](operations-and-monitoring.md)).

Interactions: a chained remote repository that reports downloads back to its source (RMT-041)
keeps the source's usage data current, so that cleanup at the source does not remove items still
used downstream.

UI: advanced tab, cache settings.

### RMT-022 · Remote checksum policy
Priority: **P1**

Controls what happens when the checksum published by the upstream for an item is missing or does
not match the checksum the system computes while fetching.

| Policy | Behavior |
|---|---|
| Generate if absent (default) | Fetch the upstream checksum. If there is none, compute one and cache the item. If it does not match the computed checksum, do not cache and fail the download. |
| Fail | If the upstream checksum is missing or does not match, do not cache and fail the download. |
| Ignore and generate | Ignore upstream checksums and use only the computed one. Retrieval never fails on checksums; integrity is not verified. |
| Ignore and pass through | Keep and hand out whatever checksums the upstream publishes, matching or not; compute one only when the upstream has none. Fetching is never refused on checksum grounds and integrity is not checked; a client that checks checksums on its own side (Maven does) then rejects the mismatching file. |

The UI lists the policy for Maven, Gradle, Ivy and sbt remotes and the declarative configuration
marks it as Maven-only, while the API configuration document carries the field for every remote.
A generic-format remote can additionally fetch the SHA-256 from the upstream before the item is
cached, for tools that request the checksum first (see the generic format page under
[formats/README.md](formats/README.md)).

The UI, the configuration document, the create, read and update operations and the declarative
configuration all use the same four values listed above.

UI: basic tab of Maven-family remote repositories.

### RMT-023 · MIME-type mismatch blocking
Priority: **MVP**

Compares the content type the upstream delivers with the type that was requested.

| Setting | Meaning | Default |
|---|---|---|
| Block mismatching MIME types | The download fails when the requested and received content types differ and the received type is on a system-wide blocked list | on; also set automatically when a Docker remote is created for the Chainguard registry |
| MIME types exempt from blocking | Per-repository list of content types that are never blocked | none |

Edge cases: the system-wide blocked list is operator configuration (contents unspecified). An
upstream that accepts every type can override the check; the exact rule is unspecified.

UI: advanced tab, other settings.

### RMT-024 · HEAD request bypass
Priority: **P2**

Before caching an item the system normally sends a HEAD request to the upstream, then the GET.

| Setting | Meaning | Default |
|---|---|---|
| Bypass HEAD requests | Skip the HEAD request and fetch with GET directly, for upstreams that reject HEAD but allow downloads | off; set automatically for Terraform remotes created for `registry.terraform.io`, `registry.opentofu.org` or `tf.app.wiz.io` |

Limits: not supported for CocoaPods and VCS remote repositories.

UI: advanced tab, other settings.

### RMT-025 · Eager fetching of companion artifacts
Priority: **P2**

| Setting | Meaning | Default |
|---|---|---|
| Eagerly fetch jars | When a POM is requested, start fetching the matching jar in the background | off |
| Eagerly fetch sources | When a binary jar is requested, start fetching the matching sources jar in the background | off |

Applies to Maven-family remotes. The background fetch shortens the first access to the companion
artifact. See the Maven format page under [formats/README.md](formats/README.md).

UI: basic tab of Maven-family remote repositories.

### RMT-026 · Cache invalidation
Priority: **MVP**

An operation that expires cached metadata of a remote repository so that the next request for each
item refreshes it from the upstream.

Behavior:

- Only metadata is expired. Immutable binaries are untouched and nothing is deleted.
- Invalidation marks items; it does not fetch. Each item is refreshed when it is next requested.
- If the refresh fails because the upstream is unreachable or times out, the stale copy is served
  with status 200. Clients can then see an outdated package document (for npm, a "no matching
  version" error) rather than a server error. After invalidating to get rid of bad metadata,
  verify that the refresh actually happened.
- A failed refresh can put the repository into the assumed-offline state (RMT-029), during which
  stale copies keep being served.
- Invalidating a whole large repository scales with the number of cached metadata files and can
  take minutes; scoping it to a path is preferred.
- After a large invalidation, many simultaneous client requests trigger many simultaneous refreshes,
  which can overload the upstream connection. The mitigations are operational:
  spread out critical installs, retry with backoff in CI, and temporarily lower the assumed-offline
  period (RMT-029) and the negative cache period (RMT-020) so that a failed refresh has a smaller
  effect.
- Typical use: repair cached metadata that is corrupt or out of step with the upstream.

| Operation | Inputs | Output | Required privilege |
|---|---|---|---|
| Invalidate cache | Repository key and optional path inside the repository | JSON message naming the cache path that was processed (the cache view key plus the path) | Manage or delete permission on the remote repository; 403 otherwise |

UI: artifact browser, select the remote repository's cache, then "invalidate" from the context menu
or the actions menu. Whether the UI offers the path-scoped variant on a folder is unspecified; the
operation accepts a path.

Interactions: the NuGet layout migration (see the NuGet format page) backs up a remote repository
and then invalidates its cache.

### RMT-027 · Cache pre-population without client transfer
Priority: **P2**

Two deprecated operations let an operator fill a cache without moving the bytes to the caller.

| Operation | Inputs | Output | Required privilege |
|---|---|---|---|
| Fetch without content | Repository key and path; mode: return content, return nothing, or return progress marks; bytes per progress mark (default 1,024) | The artifact, nothing, or a text stream of progress marks ending in a completion line | A privileged user; anonymous if granted |
| Synchronize folder from a chained upstream | Remote repository key and folder; progress marks on or off (default on); bytes per progress mark; delete local items missing upstream (default no); overwrite policy (default: overwrite local items older than upstream; alternatives: never, force); upstream file-list timeout (default 15,000 ms) | Text stream of progress marks and a summary of errors and warnings | A privileged user; anonymous if granted; for non-administrators the number of files is capped at the system-wide per-user search result limit |

The folder synchronization fetches every item under the folder without returning content and works
only against another instance of the same kind of system (RMT-040). The "fetch without content"
operation has a successor in the regular download operation and is to be disabled by default
from 2028; no successor is specified for the folder synchronization. Populating a remote
cache through either operation follows the permission rule of RMT-001.

### RMT-028 · Download redirect for cached artifacts
Priority: **P2**

| Setting | Meaning | Default |
|---|---|---|
| Download redirect | Download requests are answered with an HTTP 302 to a signed URL of the binary in object storage instead of being streamed by the system | off |

Limits: a redirect is possible only for items that are already in storage, so a remote repository
must store artifacts locally (RMT-017); combining redirect with pass-through produces an error.
There is no system-wide switch; each repository opts in. For a virtual repository the decision
follows the member repository that supplies the item: redirect if that member has it on,
otherwise a normal streamed download. The CDN-based variant for hosted deployments is REPO-020 in
[repositories.md](repositories.md). The object-storage side (signing, minimum size, firewall) is
described in [storage-and-blobs.md](storage-and-blobs.md).

UI: advanced tab ("enable redirect download").

## Availability and failure handling

### RMT-029 · Assumed-offline period
Priority: **MVP**

After a connection error or a socket timeout (RMT-015) the repository is marked as assumed offline
and stops contacting the upstream for a while; it serves only cached content during that time.

| Setting | Meaning | Default |
|---|---|---|
| Assumed-offline period | Seconds the repository stays assumed offline after a connection error; afterwards an online check is made to clear the state | 300 s |

A value of 0 means the repository is never assumed offline: every request may go to the upstream.

Behavior detail: the system makes 10 retries before it declares the repository offline; the pause
between retries grows step by step until it reaches the assumed-offline period. The point is that a
flaky link cannot keep the repository offline for good. The offline period starts only after the
last retry has failed. How these retries relate to the client request that triggered the
first failure (whether that request waits for them) is unspecified.

UI: advanced tab, cache settings.

### RMT-030 · Repository offline mode
Priority: **MVP**

| Setting | Meaning | Default |
|---|---|---|
| Offline | The repository never contacts its upstream; only items already in the cache are served | off |
| Disable artifact resolution | The repository and its cache are left out of resolution entirely and cannot be used for downloads (applies to local repositories too) | off |

Edge cases: a request for an uncached item in an offline repository fails; the status code is
unspecified. Offline remote repositories appear in the "inactive" view of the repository list.

UI: offline on the basic tab; disable resolution on the advanced tab.

### RMT-031 · Global offline mode
Priority: **P1**

A system-wide switch for installations without outside network access: no remote repository
contacts its upstream, and all remote repositories act as caches only. Local and cached items are
served normally.

| Setting | Meaning | Default |
|---|---|---|
| Global offline mode | Disable all upstream fetching system-wide | off |

The switch overrides the offline flag of each repository: no upstream is queried whatever the
per-repository setting says. It also cuts the instance off from its federation peers;
synchronization pauses and events queue locally until the switch is turned off (see
[replication-and-federation.md](replication-and-federation.md)).

UI: administration, general settings. Also settable through configuration as code (see
[operations-and-monitoring.md](operations-and-monitoring.md)).

### RMT-032 · Hard fail
Priority: **P2**

| Setting | Meaning | Default |
|---|---|---|
| Hard fail | unspecified | off |

The setting exists in the configuration document of every remote repository; its behavior is
unspecified. (The name suggests that upstream communication errors are reported to the client as
failures instead of being absorbed by the assumed-offline and stale-serving rules; this reading is
unconfirmed.)

### RMT-033 · Behavior when the upstream fails
Priority: **MVP**

The combined failure semantics of a remote repository, from the settings above:

| Situation | Result |
|---|---|
| Upstream answers 404 | Client gets not found; the path enters the negative cache for the negative cache period (RMT-020) |
| Connection error or socket timeout | The repository becomes assumed offline (after its retries, RMT-029) and serves only cached content until the online check succeeds; what the triggering request itself returns is unspecified |
| Metadata refresh exceeds the metadata retrieval timeout | Previously cached metadata is served (RMT-019) |
| Refresh after cache invalidation fails | Stale copy served with status 200 (RMT-026) |
| Upstream checksum missing or wrong | Depends on the checksum policy; under "generate if absent" and "fail" a mismatch fails the download and nothing is cached (RMT-022) |
| Received content type differs from the requested one and is on the blocked list | Download fails (RMT-023) |
| Repository or system offline | Only cached items are served (RMT-030, RMT-031) |
| Client has read but not deploy/cache permission and the item is not cached | The item is not fetched (RMT-001); with a read-only project share the result is not found (RMT-047) |

Unspecified: how upstream 5xx and 401/403 answers are reported to the client and
whether they count toward the assumed-offline state; whether a stale binary or stale metadata is
served when a download (not a refresh) fails; the role of hard fail (RMT-032).

## Resolution scope and layout

### RMT-034 · Include and exclude patterns
Priority: **MVP**

| Setting | Meaning | Default |
|---|---|---|
| Include patterns | Comma-separated Ant-style path patterns this repository answers for | `**/*` (everything); an empty value also means everything |
| Exclude patterns | Comma-separated Ant-style path patterns subtracted from the included set | none |

A request for a path outside the admitted set is not resolved by this repository, so it is never
fetched from the upstream. Excluding the organisation's own coordinates keeps internal names from
being requested from a public upstream; a typical setup puts such an exclude pattern on a virtual
repository that aggregates all remote repositories, and the same works on a remote repository
itself.

Edge cases: administrators see all cached items regardless of the patterns; other users see only
admitted paths in browsing and search. Pattern length limits are unspecified.

UI: basic tab.

### RMT-035 · Cache layout and remote layout mapping
Priority: **P2**

| Setting | Meaning | Default |
|---|---|---|
| Repository layout | Layout the cache uses to store and recognise modules (mandatory for the cache) | unspecified (the examples use the Maven 2 layout) |
| Remote layout mapping | Layout the upstream uses, if different | none (same as the cache layout) |

When a remote layout mapping is set, every outgoing request path is translated from the cache
layout to the remote layout; for example a Maven 1 upstream can be cached and served in Maven 2
form. Layout definitions are described in [repositories.md](repositories.md).

UI: basic tab.

### RMT-036 · Release and snapshot handling
Priority: **P1**

| Setting | Meaning | Default |
|---|---|---|
| Handle releases | Release artifacts may be cached in this repository | on |
| Handle snapshots | Snapshot artifacts may be cached in this repository | on |
| Maximum unique snapshots | Upper bound on stored unique snapshots of one artifact; the oldest is removed when exceeded | 0 (no limit) |
| Suppress POM consistency checks | Accept POMs whose coordinates do not match their path (normally refused with 409) | off |

Applies to Maven-family and (for unique snapshots) VCS remotes; see the format pages.

UI: basic tab of Maven-family remote repositories.

### RMT-037 · Priority resolution
Priority: **P1**

| Setting | Meaning | Default |
|---|---|---|
| Priority resolution | When this repository is a member of a virtual repository, it is consulted ahead of the virtual repository's normal order. Metadata is merged only from members with priority resolution; members without it are merged only when the package is not found in any priority member. | off |

Effect: when a priority member has the package, metadata from the other members is not merged in,
so a same-named package in a public remote does not mix into the result.

Limits: CocoaPods, Git LFS, Opkg, Cargo, Vagrant and VCS are excluded. Open: either Chef and
Debian are excluded as well, or NuGet v2 is; the spec decides. For Docker it requires the Docker
option that resolves tags by timestamp.

Interactions: virtual resolution order is described in [repositories.md](repositories.md).

UI: advanced tab, other settings.

### RMT-038 · Remote folder listing
Priority: **P2**

| Setting | Meaning | Default |
|---|---|---|
| List remote folder items | Browsing a folder of this remote repository also lists the upstream's items that are not cached yet | off |

Behavior: the system lists upstream folder contents where the upstream supports listing; for an
upstream that publishes an HTML directory index, the index is parsed. Listings are cached for the
metadata retrieval cache period (RMT-018): the first look at a folder costs an upstream round trip
per remote, which adds up in a virtual repository with many remote members, and repeat visits
within the period come from the cached listing. Some formats need listings for
dynamic resolution (for example remote version lookups for Ivy, and P2). Listed but uncached items
are marked as not cached.

Limits: available for generic, Maven, Debian, Opkg, RPM and P2 remotes, and for chained remotes of
every format. Upstreams without a listing (Docker Hub, for example) cannot be browsed.

UI: basic tab; the artifact browser shows the listing.

### RMT-039 · Configuration sharing
Priority: **P2**

| Setting | Meaning | Default |
|---|---|---|
| Share configuration | The remote repository's configuration may be read by any authenticated user through the deprecated shared-configuration read (RMT-002); non-administrators receive a sanitised copy | off |

Any further consumer of the shared configuration beyond that read operation is unspecified.

## Chained instances

### RMT-040 · Chained remote repository
Priority: **P2**

A chained remote repository is a remote repository whose upstream is a local, remote, federated or
virtual repository on another instance of the same kind of system (including an edge instance).
Besides normal pull-through caching it can exchange extra information with the source instance:
download statistics (RMT-041), properties (RMT-042), folder listings (RMT-038), source deletions
(RMT-043), search (RMT-045), and parallel chunked transfer of large files (RMT-046).

| Setting | Meaning | Default |
|---|---|---|
| Chained remote (content synchronization) | Marks the repository as chained; enables the extra exchanges below | off |

Configuration:

- The upstream URL is the package-protocol address of the source repository on the other instance;
  for most formats this includes the format's API path segment, for others the plain repository
  address. Some formats need extra URLs pointed at the source instance too: NuGet (v3 service index
  URL, or the chain bypasses the source and goes to nuget.org), PyPI (registry URL), Terraform
  (registry and providers URLs). See the format pages under
  [formats/README.md](formats/README.md).
- The source should be a local or remote repository. A virtual source is discouraged because it can
  behave inconsistently, except for Go, where it is mandatory because Go modules resolve only
  through virtual repositories.
- Credentials: user name and password, or an access token issued by the source instance (RMT-005).
- When the URL is entered, the UI recognises that it belongs to another instance of the same kind of
  system and shows this on the field; the test action verifies the connection.

Two related options belong to
[security-scanning-and-curation.md](security-scanning-and-curation.md): one forwards the
requesting user's context (e-mail address and repository identifier) to the source for upstream
curation traceability; the other, which requires the first, lets a curation audit run from a
command-line client download packages through the chain without caching them (packages
classified as malicious stay blocked).

When the source instance is reached through the operator-run tunnel between a hosted and a
self-hosted instance (ADMAPI-105 in [api-admin-operations.md](api-admin-operations.md)), the
tunnel accepts only JWT authentication: password credentials and the bearer option (RMT-006)
cannot reach a virtual repository behind the tunnel. That tunnel is out of scope for a
self-hosted product.

UI: basic tab of a remote repository whose URL points at another instance.

### RMT-041 · Download statistics reporting
Priority: **P2**

| Setting | Meaning | Default |
|---|---|---|
| Report download statistics | Each download of a cached item from this chained remote is reported to the source instance, which counts it as a remote download | off |

The source instance records, per item, the number of downloads through chained remotes, the time
and user of the last such download, the address of the downstream instance along the chain, and the
path along the chain; these are visible to its query language (see
[search-and-query.md](search-and-query.md)). Counting starts when the option is enabled, so the two
instances' numbers can differ.

Purpose: cleanup decisions at the source (RMT-021,
[retention-and-cleanup.md](retention-and-cleanup.md)) see downstream use.

### RMT-042 · Property synchronization
Priority: **P2**

| Setting | Meaning | Default |
|---|---|---|
| Synchronize properties | Items are fetched together with their properties, and later changes of properties at the source are copied to the cached item | off |

Timing: properties are re-synchronized when the item is downloaded after the metadata retrieval
cache period (RMT-018, default 7,200 s) has passed; a shorter period gives faster propagation at
the price of more traffic.

Limits: a Docker pull by digest does not synchronize properties, because a digest can be shared by
several tags with different properties.

### RMT-043 · Source absence detection
Priority: **P2**

| Setting | Meaning | Default |
|---|---|---|
| Source absence detection | When a cached item is viewed, the UI shows whether the original has been deleted at the source instance | off |

Purpose: the operator can copy such items from the cache to a local repository to keep them
(RMT-003). Limits: not supported for generic repositories.

### RMT-044 · Uncached retrieval through a virtual repository for chained requests
Priority: **P2**

This is a setting of the virtual repository on the source instance that a chained remote points at.

| Setting | Meaning | Default |
|---|---|---|
| Chained requests may fetch from remotes | Requests that come from another instance of the same kind of system may make this virtual repository's remote members fetch from their upstreams. When off, such requests are served only from the members' caches. | off |

Limits: not offered for Swift, Terraform and Composer. See [repositories.md](repositories.md) for
the other virtual repository settings.

### RMT-045 · Search through a chained remote
Priority: **P2**

The query language (see [search-and-query.md](search-and-query.md)) can extend a search into the
source repository of a chained remote with a "transitive" modifier.

Rules:

- The upstream must be another instance of the same kind of system, and the remote repository must
  hold credentials for it.
- The query names exactly one repository with an equality condition.
- Sorting and offset are not allowed; a result limit is allowed.
- Only items can be the primary domain; included sub-domains are limited to items and item
  properties.
- Non-administrators must include repository, path and name in the output so that results can be
  permission-filtered.
- For a virtual repository, the search covers only its first five remote members; the limit is a
  system-wide setting (default 5).
- In the examples, items already in the local cache are reported with the cache view key
  (`<key>-cache`) and items found at the source with the remote key.

### RMT-046 · Parallel chunked download of large files
Priority: **P2**

When a chained remote repository fetches a file of 1 GB or more from its source, the system splits
the transfer into parts with HTTP range requests, downloads the parts concurrently, reassembles the
file and verifies its checksum before caching and serving it. No client sees the file before the
whole file has been verified, so the integrity guarantee is the same as for a single-stream fetch.
When a transfer breaks off, only the parts that failed are fetched again; the transfer does not
start over. Files below 1 GB keep using a single stream.

Settings: none; the behavior is automatic for chained remote repositories of every format. Number
of parts, concurrency and retry limits are unspecified.

Limits: only chained remote repositories; ordinary upstreams are fetched as one stream.

## Project-scoped remote repositories

### RMT-047 · Project-scoped remote repository
Priority: **P2**

An internal remote repository whose upstream is a repository shared into the project from another
project on the same instance. Requests through it carry the consuming project's identity, so the
consuming project can apply its own security and curation policies, and downloads can be attributed
to it. When a repository is shared with project-attributed access, members of receiving projects
cannot use the shared repository directly; direct requests are refused and they must go through a
project-scoped remote repository (or a virtual repository containing one).

| Setting | Meaning | Default |
|---|---|---|
| Project-scoped upstream | The repository is a project-scoped connection instead of an external one | off |
| Shared repository key | Key of the shared repository it connects to; must be connectable for the repository's project. The upstream URL is not used and reads back empty. | none; mandatory when project-scoped |
| Owning project of the shared repository | Returned on read | read-only |

Operations:

| Operation | Inputs | Output | Required privilege |
|---|---|---|---|
| List connectable repositories | Project key; optional package type, key filter, result limit | Keys of repositories shared into the project that may be used as the shared repository key | Project administrator of that project, or system administrator |
| List consumers of a repository | Repository key; optional project filter, result limit | Per connected project-scoped remote: its key, owning project key and display name, time the share was made (epoch milliseconds), environments of the share | System administrator, or project administrator when filtering by that project |
| Create, convert | Create with project-scoped upstream on and a connectable shared key (400 otherwise). Convert an existing remote by update: project-scoped on plus shared key, or off plus an upstream URL (400 if the URL is missing) | Confirmation | Administrator through the API; project administrators create them for their project through the UI connection flow |

Permissions:

- The sharing grant is a ceiling (read only, or read and write); the member's project role and
  system-wide permissions also apply; the most restrictive wins.
- Read-and-write share: members with write access on the project-scoped remote can make the shared
  repository fetch from its own external upstream. Read-only members can use only content already
  cached in the project-scoped remote or in the shared repository.
- Read-only share: nothing is fetched from the shared repository's external upstream; uncached items
  return not found.
- Deploying into a shared local repository through a project-scoped remote is not possible.

Lifecycle:

- Connection works only within one instance, not across instances or federation, and not from the
  owning project.
- The UI announces new shares with a banner; the receiving project administrator connects them
  (the proposed key is the receiving project key, a hyphen and the shared repository key),
  optionally adds the new repository to virtual repositories and sets stages. Several
  project-scoped remotes may connect to the same share. Unconnected shares
  stay in the review list for 60 days and remain connectable afterwards from the shared
  repositories view.
- After a repository is unshared, requests through connected project-scoped remotes are denied; the
  remotes remain until deleted, and the UI warns about remotes whose source is no longer
  connectable. Deleting the shared repository breaks all connected remotes.
- The shared repositories view shows per share whether a project-scoped remote is pending,
  connected, or not applicable (direct share).

Project and sharing concepts are described in [identity-and-access.md](identity-and-access.md).

UI: remote repository form, basic tab, "internal" option (offered only when the project has received
a matching share); repositories page banner and shared repositories view.

## Outbound HTTP proxies

### RMT-048 · Outbound HTTP proxy definitions
Priority: **P1**

Named definitions of corporate HTTP proxies through which the system reaches upstreams. Any number
can be defined; remote repositories select one by key (RMT-016).

<details>
<summary>Proxy definition fields</summary>

| Field | Meaning | Default |
|---|---|---|
| Key | Unique identifier of the definition | none; mandatory |
| Host | Proxy host name | none |
| Port | Proxy port | none |
| User name | Proxy user, when the proxy requires authentication | empty |
| Password | Proxy password | empty |
| NTLM host | Name of the connecting machine, for NTLM proxies | empty |
| NTLM domain | Domain or realm, for NTLM proxies | empty |
| Redirect target hosts | Comma- or newline-separated host names the proxy may redirect to; the proxy credentials are reused for requests redirected to them | empty |
| Default for | Marks the definition as the default proxy, system-wide or for named services (RMT-049) | not default |

</details>

Behavior and limits:

- NTLMv2 proxies are supported; on Linux only through an external NTLM authentication helper
  proxy (for example CNTLM).
- A proxy secured by a self-signed certificate needs that certificate in the trusted set (RMT-008).
- Proxy definitions can be created in the UI, through configuration as code, and in the bootstrap
  file read at first start. A dedicated proxy API is unspecified; proxies are part of the system
  configuration operations (see [operations-and-monitoring.md](operations-and-monitoring.md)).
- Besides remote repositories, proxies serve federated repository traffic, webhooks (overridable
  per webhook), mail, identity-provider calls and similar outbound connections.

Security: the proxy password is a stored secret, and redirect target hosts receive the proxy
credentials.

UI: administration, proxies.

### RMT-049 · Default proxy
Priority: **P1**

A proxy definition can be marked as default. The system-wide default is used for outbound
connections of all services, including remote repository traffic, and is assigned to new remote
repositories; a default can also be limited to named services. Several defaults scoped to
different services can coexist. A repository opts out with "no proxy" (RMT-016), which also stops
later changes of the default from being applied to it. Whether changing the default rewrites the
proxy of existing repositories that selected another proxy explicitly is unspecified. For
federated repositories the rule is explicit: no proxy selected and "no proxy" off means
the system default proxy is used if one exists (see
[replication-and-federation.md](replication-and-federation.md)). OIDC login providers and
external user-directory integrations use the default only when their own "use default proxy"
switch is on.

UI: the "default proxy" field of a proxy definition.

## Notes for implementers

- Metadata and binaries have different lifetimes. Only formats know which paths are metadata;
  getting this wrong either freezes package lists (metadata treated as immutable) or hammers the
  upstream (binaries re-validated). Every format page must name its expiring paths.
- Stale content is served in preference to errors: metadata timeouts, failed refreshes
  after invalidation and the assumed-offline state all return old content with status 200. Clients
  see "version not found" instead of a server error, which is the trade-off to document.
- Read permission does not populate a cache. Anonymous or read-only access to a remote repository
  only sees what someone with deploy/cache permission has already pulled; CI identities that
  resolve new dependencies need the populate permission.
- `<key>-cache` is a client-visible address. Reserve the suffix for all keys and keep remote keys
  six characters shorter so that the cache address stays within the key length limit.
- A socket timeout, once its retries are used up, takes the whole remote repository offline for
  the assumed-offline period (default 300 s). Separately, an upstream 404 is remembered for 1,800 s by default, so a version
  requested just before it was published stays invisible for up to half an hour. Both values are
  worth exposing prominently.
- Credentials leave the upstream host only on explicit opt-in (redirect forwarding per repository,
  redirect target hosts per proxy). Upstreams often redirect downloads to other hosts; with
  forwarding off, such redirects should still be followed without credentials (redirect following
  as such is unspecified).
- A new remote repository silently inherits the default proxy; "no proxy" is a positive choice that
  must survive later proxy changes.
- Configuration documents are read with unknown fields tolerated and written partially; defaults
  are applied on create and the class is immutable. Whether upstream passwords come back in a
  configuration read is unspecified; decide it explicitly, because API clients re-submit what they
  read.
- Collapse concurrent misses for the same item into one upstream fetch; parallel first requests for
  a popular package are the normal case after invalidation, and bursts of refreshes after a large
  invalidation are known to saturate the upstream connection. Request coalescing itself is not part
  of the described behavior; it is an implementation recommendation.
- Some details above are open or contradictory (priority resolution exclusions, a metadata cache
  period of 0 against the 30 s minimum); treat those points as open until decided in the
  specification.
