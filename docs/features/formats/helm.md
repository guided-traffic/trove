# Helm chart format

This page catalogs what a repository manager does to serve Helm charts. Two flavors exist side by
side: index-based chart repositories, which follow Helm's
[chart repository protocol](https://helm.sh/docs/topics/chart_repository/) (an `index.yaml` plus
chart archives, used with `helm repo add`), and OCI-based chart repositories, which store charts as
OCI artifacts and are used with `helm registry login` and `oci://` references
([Helm OCI registries](https://helm.sh/docs/topics/registries/)). The page covers both flavors in
the local, remote and virtual classes: index calculation and its operations, upload enforcement,
dependency rewriting, the merged index of virtual repositories, OCI push and pull, settings,
promotion and build metadata. Registry-protocol behavior that Helm-via-OCI repositories share with
Docker and OCI repositories is on [docker.md](docker.md) and [oci.md](oci.md). Repository classes
in general are in [../repositories.md](../repositories.md), pull-through caching in
[../remote-repositories.md](../remote-repositories.md).

Throughout this page `https://repo.example.com/helm/<repo-key>` stands for the chart repository URL
of an index-based repository (the path layout is the implementation's choice), `repo.example.com`
is the registry host for OCI-based repositories, `helm-local`, `helm-remote`, `helm-virtual` and
`helmoci-local`, `helmoci-virtual` are example repository keys, and `${TOKEN}` is an access token.

## Repository flavors

### PKG-HELM-001 · Two flavors of Helm repositories
Priority: **P1**

| Flavor | Protocol | Client commands | Classes |
|---|---|---|---|
| Index-based | HTTP chart repository: `index.yaml` and chart archives (PKG-HELM-003) | `helm repo add`, `helm repo update`, `helm install <alias>/<chart>` | local, remote, virtual |
| OCI-based (Helm-via-OCI) | Registry API shared with Docker and OCI (PKG-HELM-017) | `helm registry login`, `helm push`, `helm pull oci://...` | local, remote, virtual |

- Helm recommends OCI registries over chart repositories; the OCI flavor avoids the scaling limits
  of one ever-growing index file.
- New Helm repositories are created in the OCI flavor by default. The creation form of a local
  repository offers a switch to the index-based ("legacy") flavor for that repository; a
  system-wide setting changes the default.

| Setting | Scope | Meaning | Default |
|---|---|---|---|
| Default new Helm repositories to the OCI flavor | System-wide, package format settings, Helm section | New Helm repositories use the OCI flavor unless the user switches | on |

- Each flavor answers only its own protocol. `helm repo add` against a Helm-via-OCI repository, or an
  `oci://` reference to an index-based repository, fails.
- A virtual repository cannot contain both flavors: one virtual repository per flavor.
- The packages view lists charts of both flavors side by side; the artifact browser shows one flavor
  at a time through a filter.
- Creating a Helm repository requires the system administrator or a project administrator. UI
  location: administration, repositories, create repository, class, Helm format (UI-031 in
  [../ui-inventory.md](../ui-inventory.md)).

### PKG-HELM-002 · Supported Helm client versions
Priority: **P1**

| Flavor | Client versions |
|---|---|
| Index-based | Helm 2.9.0 or later; relative chart URLs (PKG-HELM-008) require Helm 3; support for clients before 3.0.0 is being discontinued |
| OCI-based | Helm 3.8.0 or later; earlier Helm 3 releases only with `HELM_EXPERIMENTAL_OCI=1` set |

## Index-based chart repositories

### PKG-HELM-003 · Chart repository protocol
Priority: **P1**

Each index-based Helm repository, whatever its class, answers Helm's chart repository protocol
under its own chart repository URL:

```shell
helm repo add helm-virtual https://repo.example.com/helm/helm-virtual --username alice --password "${TOKEN}"
helm repo update
helm install my-release helm-virtual/mychart
```

Protocol (Helm chart repository guide):

- `GET <repository URL>/index.yaml` returns the index: `apiVersion: v1`, `entries` (a map from chart
  name to the list of its versions; each version holds the chart's `Chart.yaml` fields plus
  `created`, `digest` (SHA-256 of the archive) and `urls`), and `generated`.
- Chart archives (`<name>-<version>.tgz`) are downloaded from the URL listed in `urls`; provenance
  files (`<archive>.prov`) sit next to the archives.
- YAML is served as `text/yaml` or `text/x-yaml`. Helm refuses to add a repository whose index is
  missing or invalid. `helm repo update` re-downloads the index into the client's cache.
- Chart versions follow SemVer 2.
- Although `urls` is a list, the Helm client uses one URL per version.
- Credentials: Basic authentication with user name and password or token; Helm sends them only to
  the repository's own host unless `--pass-credentials` is given
  ([helm repo add](https://helm.sh/docs/helm/helm_repo_add/)). Encrypted client passwords are
  recommended (IAM-035 in [../identity-and-access.md](../identity-and-access.md)). Anonymous use
  works when anonymous access is enabled (IAM-005).

Recommendations and troubleshooting:

- Resolve charts only through a virtual repository, so that remote charts are cached correctly.
- On errors check: the flavor matches the command; the URL matches the deployment's routing (base
  path); the user or token has read on the repository or virtual repository.

### PKG-HELM-004 · Deploying charts to an index-based repository
Priority: **P1**

- Helm has no push command for chart repositories (`helm push` is for OCI registries only). Charts
  are uploaded with an HTTP `PUT` of the archive to a path in a local repository, as for any file
  (ART-002 in [../artifacts.md](../artifacts.md)), for example
  `curl -u alice:${TOKEN} -T mychart-1.0.0.tgz "<deploy URL of helm-local>/mychart-1.0.0.tgz"`.
  Deploy by checksum (ART-004) and deploy of an archive that is extracted on arrival (ART-005) work
  too.
- UI: artifact browser, deploy (ART-006, UI-013 in [../ui-inventory.md](../ui-inventory.md)).
- Charts deployed to the repository root are installable. That charts in subfolders are indexed too
  follows from the duplicate-path rule (PKG-HELM-005) but is not specified directly.
- Privilege: deploy; replacing an existing archive needs delete/overwrite (IAM-022).
- Upload checks: PKG-HELM-009 and PKG-HELM-010.

### PKG-HELM-005 · Automatic index calculation for local repositories
Priority: **P1**

- The system computes the index of charts stored in local repositories automatically from the
  charts' metadata. Whether the index is updated synchronously with a deploy or asynchronously is
  unspecified, as is the behavior on delete.
- The index has the structure of a native Helm chart repository: entries per chart name, one entry
  per version.
- The same chart name and version deployed at several paths of one repository: only the most recent
  entry stays in the index and its URL is replaced; the earlier archive stays in storage but Helm
  clients can no longer see it. PKG-HELM-009 blocks this at upload instead.

### PKG-HELM-006 · Recalculate the chart index
Priority: **P1**

| Input | Meaning |
|---|---|
| Repository key | Path parameter; local and federated repositories only |

| Status | Meaning |
|---|---|
| 200 | Recalculation done or started (plain-text body); whether it runs synchronously is unspecified |
| 401 | Invalid credentials |
| 403 | Insufficient permission (the required permission is unspecified) |
| 500 | Internal error |

- Authentication: Basic, bearer token, or an API key header.
- The operation summary speaks of "the specified path", but its only input is the repository key;
  path-scoped recalculation is PKG-HELM-007.
- Use after enabling relative URLs (PKG-HELM-008) or when the index is suspected to be wrong. The
  generic recalculation action on the repositories screen is REPO-009 in
  [../repositories.md](../repositories.md).

### PKG-HELM-007 · Partial re-indexing
Priority: **P2**

Recalculates the index entries for part of a repository only: faster and lighter than PKG-HELM-006
when individual charts are missing from the index or broken in it.

| Input | Meaning |
|---|---|
| Repository key | Path parameter |
| Path | Path parameter: a folder holding several charts, or one chart archive |

Responses: 200 (plain text), 401, 403, 500 as in PKG-HELM-006.

### PKG-HELM-008 · Relative chart URLs in the index
Priority: **P1**

- Index entries can list chart URLs relative to the repository URL instead of absolute URLs. This
  shortens the index and speeds up the client; only Helm 3 and later resolve relative URLs.
- Default: open. Either relative URLs are the default, or they are enabled by a system-level flag;
  the spec decides.
- Applies to charts deployed after the switch; a recalculation (PKG-HELM-006) converts the existing
  entries.

### PKG-HELM-009 · Prevent duplicate chart paths
Priority: **P1**

| Setting | Scope | Meaning | Default |
|---|---|---|---|
| Prevent duplicate chart paths | System-wide (package format settings, Helm section) or per index-based local repository (creation form, enforcement section) | An upload whose chart name and version already exist at another path of the same repository is rejected | unspecified (off implied) |

- Rejection: `403` with a message naming the enforcement policy and the existing chart name and
  version.
- Purpose: exactly one index entry per chart version; avoids ambiguity for replication,
  distribution of release sets and folder layouts.
- Activation rules:
  - Once enabled globally, enforcement cannot be turned off for individual repositories.
  - Per repository, enforcement can be chosen only when the repository is created, in the
    index-based creation form, by a system administrator.
  - Repositories created before the feature existed cannot use it.
- Two local members of a virtual repository with the same chart name and version: the resolution
  order decides (REPO-035 in [../repositories.md](../repositories.md)).
- Limitation: push replication, copy or move from a repository without enforcement into one with
  enforcement may leave out charts that break the rules; how the caller learns which charts were
  refused is unspecified.

### PKG-HELM-010 · Enforce chart name, version and SemVer
Priority: **P1**

| Setting | Scope | Meaning | Default |
|---|---|---|---|
| Enforce chart name and version | Same as PKG-HELM-009 (separate check box) | On upload, the name and version in the archive's file name must equal those in `Chart.yaml`, and the version must be SemVer 2 | unspecified (off implied) |

Rejected with `403` and an explanatory message:

| Upload | Reason |
|---|---|
| Version `1.0.0.0` | not SemVer 2 |
| `chart-1.0.0.tgz` whose `Chart.yaml` says `chart` `2.0.0` | file name does not match metadata |
| Archive whose metadata cannot be read or is malformed | metadata unreadable |

Activation rules and the replication limitation are those of PKG-HELM-009.

### PKG-HELM-011 · Proxying an index-based chart repository
Priority: **P1**

- Upstream: the URL of a chart repository (the location of its `index.yaml`), for example a
  community chart collection hosted on GitHub Pages. The form asks the user to check the URL.
- The system caches the index and the archives and rewrites chart locations in the index so that
  Helm downloads archives through the system (specified for the charts base URL, PKG-HELM-012).
- Which Helm files count as expiring metadata for the metadata retrieval cache period (RMT-018 in
  [../remote-repositories.md](../remote-repositories.md)) is unspecified; the index is the natural
  candidate.
- Charts in an upstream index can depend on charts elsewhere, which can depend on others again;
  clients may download components from sources nobody reviewed. PKG-HELM-013 addresses this.
- Recommended: clients resolve through a virtual repository (PKG-HELM-003).
- Privileges: read plus deploy/cache to fetch content not cached yet (IAM-023 in
  [../identity-and-access.md](../identity-and-access.md)).

### PKG-HELM-012 · Charts base URL
Priority: **P1**

| Setting | Repositories | Meaning | Default |
|---|---|---|---|
| Charts base URL | Index-based remote, basic tab | Base location of the chart archives when it differs from the location of the index; the system writes it into the cached index so that clients still fetch archives through the system | unspecified (blank) |

Example: an upstream whose index lives at `https://fluxcd-community.github.io/helm-charts` while
the archives are published under `https://github.com/fluxcd-community/helm-charts/releases/download`.

### PKG-HELM-013 · External dependency rewrite
Priority: **P2**

| Setting | Repositories | Meaning | Default |
|---|---|---|---|
| Rewrite external dependencies | Index-based remote, advanced tab | External chart URLs found in the index are rewritten so that Helm fetches them through the system, which downloads and caches them in the remote | off |
| Dependency allow list | same | Ant-style patterns of external locations that may be fetched, for example `https://github.com/**`; several patterns form a list of trusted locations | `**` (any source) |

- Rewritten form: the external URL is replaced by a URL under the virtual repository that aggregates
  the remote, made of a reserved path segment for external content followed by the original scheme,
  host and path. With the pattern `https://example.com/example-community/helm-charts/**`, every
  index URL starting with `https://example.com/example-community/helm-charts/` points into the
  virtual repository instead.
- Configured on each aggregated remote separately; the rewrite reaches clients through the virtual
  repository.
- What happens to external URLs that match no allowed pattern (left unchanged or refused) is
  unspecified.
- The general dependency rewrite settings (REPO-040 in [../repositories.md](../repositories.md))
  list npm, Bower and Go only; the Helm variant lives on the remote repository.

Security note: the allow list is the control that keeps transitive chart dependencies on reviewed
sources; the default `**` admits any source.

### PKG-HELM-014 · Merged index in virtual repositories
Priority: **P1**

The index of a virtual repository is the merge of its members' indexes, and it depends on who asks:
a caller sees only charts it may read.

On a request for the index of a virtual repository:

1. If a cached, unexpired merged index for the caller's permission set exists, return it.
2. Otherwise, if a calculation for the same permission set is running, wait until it finishes or 6
   minutes pass, whichever comes first.
3. If it finished in time, return the cached result. What happens after the wait expires is
   unspecified.

| Setting | Meaning | Default |
|---|---|---|
| Metadata retrieval cache period (virtual) | How long a merged index is reused before it is recalculated | 6 minutes; 60 s or more recommended |

- A period of 0 recalculates on every request: slow resolution and a risk of running out of memory.
- A system-level lock-wait timeout for Helm index merges (for example 120 s) restores the earlier
  behavior. The default of that timeout is unspecified beyond the 6-minute wait described above.
- The same chart version in several members: resolution order decides (REPO-035).
- Merged metadata in general: REPO-037 in [../repositories.md](../repositories.md).

### PKG-HELM-015 · Member selection by namespace in virtual repositories
Priority: **P2**

| Setting | Meaning | Default |
|---|---|---|
| Namespaced member access | A client may fetch from one specific member of a virtual Helm repository by putting the member's key in the path: `<virtual URL>/<member key>/<chart>.tgz` | off |

Set when the virtual repository is created or updated. Whether the merged index then carries
member-qualified URLs is unspecified.

### PKG-HELM-016 · Chart information views
Priority: **P2**

- Index-based: artifact browser, select a chart archive, chart information tab with the chart's
  metadata (UI-011 in [../ui-inventory.md](../ui-inventory.md)).
- OCI-based: artifact browser, select the chart; general and information tabs show its metadata.
- The exact fields shown are unspecified.

## OCI-based chart repositories

### PKG-HELM-017 · Helm-via-OCI repositories
Priority: **P1**

- A repository format of its own, available as local, remote and virtual repository (and as
  federated repository, [../replication-and-federation.md](../replication-and-federation.md)). It
  speaks the registry API shared with Docker and OCI repositories (PKG-DOCKER-016 in
  [docker.md](docker.md), PKG-OCI-006 in [oci.md](oci.md)) and nothing else.
- Repository keys follow the container rules (PKG-DOCKER-002).
- It accepts any OCI-compatible content, not only charts (PKG-OCI-008).
- Referrers (signatures and attestations for charts) work as in PKG-OCI-009.
- Charts can also be stored in plain OCI repositories (PKG-OCI-018); what the dedicated flavor adds
  beyond Helm-specific views and settings is unspecified.

### PKG-HELM-018 · Pushing and pulling charts over OCI
Priority: **P1**

```shell
helm registry login repo.example.com -u alice -p "${TOKEN}"
helm push mychart-1.0.0.tgz oci://repo.example.com/helmoci-local
helm pull oci://repo.example.com/helmoci-local/mychart --version 1.0.0
helm pull oci://repo.example.com/helmoci-local/mychart        # latest version
```

- Login takes the host only: Helm's documentation rejects a scheme or a path there.
- `helm push` accepts only archives made with `helm package`; the reference starts with `oci://`
  and names neither chart nor tag: Helm takes the image name from the chart name and the tag from
  the chart version, strictly.
- A provenance file lying next to the archive is pushed automatically as an extra layer.
- Without `--version` the latest version is pulled; how "latest" is determined is unspecified
  (presumably from the tag listing, PKG-DOCKER-022 in [docker.md](docker.md)).
- `oci://` references also work with `helm show`, `helm template`, `helm install` and
  `helm upgrade`; installing by digest (`oci://.../mychart@sha256:<digest>`) pins the exact chart.
- Dependencies: a `Chart.yaml` dependency with `repository: "oci://repo.example.com/helmoci-virtual"`
  makes `helm dependency update` fetch `oci://repo.example.com/helmoci-virtual/<name>:<version>`.
- UI deploy of a chart into a Helm-via-OCI repository: artifact browser, deploy (ART-006 in
  [../artifacts.md](../artifacts.md)).

Chart manifest as stored in the registry (Helm documentation):

| Part | Media type |
|---|---|
| Config | `application/vnd.cncf.helm.config.v1+json` |
| Chart archive layer | `application/vnd.cncf.helm.chart.content.v1.tar+gzip` |
| Provenance layer (optional) | `application/vnd.cncf.helm.chart.provenance.v1.prov` |

### PKG-HELM-019 · Helm-via-OCI repository settings
Priority: **P1**

| Setting | Repositories | Meaning | Default | Entry |
|---|---|---|---|---|
| Maximum unique tags | local (and federated) | Distinct chart versions (tags) kept per chart | 0: no limit | PKG-DOCKER-028 |
| Tag retention | local (and federated) | Revisions of an overwritten tag kept | 1: none kept | PKG-DOCKER-029 |
| Bearer token authentication | remote, basic tab | Upstream `Authorization` uses Bearer | unspecified | RMT-006 |
| Cache foreign layers | remote, advanced tab | Fetch and cache layers stored outside the upstream registry | unspecified | PKG-DOCKER-036 |
| Foreign layer allow list | remote, advanced tab | Ant-style patterns of permitted external locations | `**` | PKG-DOCKER-036 |
| Metadata retrieval cache period | remote | Re-check interval for tags | 21,600 s | RMT-018 |

- "Store artifacts locally" does not apply to Helm-via-OCI remotes (RMT-017).
- System-wide container settings apply: overwrite permission for existing tags (PKG-DOCKER-027),
  shared-digest download statistics (PKG-DOCKER-030).
- No settings specific to Helm-via-OCI virtual repositories are listed.

### PKG-HELM-020 · Container access method applies to chart registries
Priority: **P1**

The system-wide container access method (repository path, subdomain or port; PKG-DOCKER-005 to
PKG-DOCKER-009 in [docker.md](docker.md)) also decides how Helm-via-OCI repositories are reached,
even on an instance that hosts only charts. A reverse proxy or load balancer must route the
registry API for chart repositories exactly as it does for Docker repositories; the references in
`helm push` and `helm pull` follow the chosen method.

### PKG-HELM-021 · Generic content and list handling in Helm-via-OCI repositories
Priority: **P2**

- Moving, copying or deleting an index manifest with generic item operations does not include the
  images it references; both must be selected for the operation to be complete (PKG-DOCKER-023).
- Any OCI-compatible file type can be stored, despite the format's name.

### PKG-HELM-022 · Promoting charts stored as OCI
Priority: **P1**

The container image promotion operation (PKG-DOCKER-032 in [docker.md](docker.md)) also promotes
charts between Helm-via-OCI repositories, as move or copy, optionally with a new tag. Whether build
metadata follows a promoted chart is unspecified. A promotion operation for index-based
repositories is unspecified; generic copy and move (ART-032, ART-033 in
[../artifacts.md](../artifacts.md)) apply there, subject to the enforcement limitation of
PKG-HELM-009.

## Tooling

### PKG-HELM-023 · Build metadata for charts through a companion CLI
Priority: **P2**

A companion command-line tool wraps Helm so that chart operations are recorded in build metadata
([../builds-and-ci.md](../builds-and-ci.md)):

- Wrapped commands that can record build metadata: registry login, package, push, pull, dependency
  update, install, upgrade and repo management; any other Helm command is passed through. Build name
  and number must be given together; a module name and a project are optional; publishing the
  build record is a separate step.
- Package and dependency update read the dependencies from `Chart.lock`. A chart without
  dependencies has no `Chart.lock`, so collection fails; on the very first dependency update, before
  `Chart.lock` exists, collection fails although the download succeeds.
- Push to an OCI repository records the manifest and layer items as build artifacts and sets build
  name and build number properties on the chart's version folder.
- The module kind in build metadata is the Helm kind for both flavors (not a Docker kind, not a
  separate OCI-Helm kind).
- Manual publishing (native Helm plus the build API): set build name and number (optionally
  timestamp) properties on the chart version folder, use the Helm module kind, and give checksums
  and paths that match the stored OCI items; otherwise the build view lists the artifacts without a
  path.
- Registry login through the wrapper uses the configured server's credentials; a configuration with
  a token but no user name needs an explicit user name.

### PKG-HELM-024 · Client setup instructions
Priority: **P1**

The client setup dialog (UI-021 and UI-026 in [../ui-inventory.md](../ui-inventory.md)) offers, per
flavor, the matching snippets with host, repository and token filled in: for index-based
repositories `helm repo add` with credentials, `helm repo update` and an install example; for
Helm-via-OCI repositories `helm registry login`, `helm push` and `helm pull`.

### PKG-HELM-025 · Chart scanning
Priority: **Later**

The scanning engine does not scan Helm charts in either flavor; repository indexing for scanning
lists Helm as unsupported. Scanning:
[../security-scanning-and-curation.md](../security-scanning-and-curation.md).

## Notes for implementers

- An empty index-based repository must still serve a valid `index.yaml`, or `helm repo add` fails
  before the first chart is uploaded.
- Chart URLs in the index must be reachable through the system and stay on the repository's host:
  Helm sends credentials only to that host by default. Absolute URLs need the externally visible base
  URL; relative URLs need Helm 3.
- The merged index of a virtual repository is per permission set. Cache it per set, collapse
  concurrent calculations for the same set (across replicas too), and never serve one caller's
  merged index to another.
- Decide how duplicates of one chart version at different paths are handled; silently replacing the
  index entry hides the older archive from clients while it still occupies storage.
- Index generation must not hold all charts of a large repository in memory at once; frequent
  recalculation is known to cause out-of-memory problems.
- `helm push` derives name and tag from `Chart.yaml`; the registry must accept nested names under the
  repository key and tags that are chart versions.
- Helm-via-OCI repositories share the registry implementation of Docker and OCI repositories,
  including the access method; only the settings in PKG-HELM-019 differ.
- Upload enforcement (PKG-HELM-009, PKG-HELM-010) must also run for copy, move and replication
  targets, and report which items were refused instead of dropping them silently.
