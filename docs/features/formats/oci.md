# OCI images and artifacts

This page catalogs what a repository manager does to act as an OCI registry: OCI repositories in
the local, remote and virtual classes, the endpoints of the
[OCI distribution specification](https://github.com/opencontainers/distribution-spec/blob/main/spec.md),
the manifest kinds of the [OCI image specification](https://github.com/opencontainers/image-spec),
non-image artifacts, the referrers API that links signatures, SBOMs and attestations to images, and
the OCI clients that are expected to work (Podman, Buildx, BuildKit, ORAS, wasm-to-oci, Helm).
Docker, OCI and Helm-via-OCI repositories share one registry API surface; behavior common to all
three (addressing, authentication, tag overwrite and tag limits, multi-architecture handling,
promotion, deletion) is catalogued once on [docker.md](docker.md) and
mapped in the section "Behavior shared with Docker repositories" below. Helm charts are on
[helm.md](helm.md).

Throughout this page `repo.example.com` is the host name of the system, `oci-local`, `oci-remote`
and `oci-virtual` are example repository keys, `<name>` is the image or artifact name as the
specification uses it, and the examples use the repository path addressing method
(PKG-DOCKER-006 in [docker.md](docker.md)).

## Repositories

### PKG-OCI-001 · OCI repositories in all three repository classes
Priority: **P1**

| Class | Purpose |
|---|---|
| Local | OCI images and other OCI artifacts the organization produces |
| Remote | Pull-through cache of an OCI-compliant registry or a repository on another instance (PKG-OCI-003) |
| Virtual | One address over several local and remote OCI repositories (PKG-OCI-004) |

- The OCI format is a repository format of its own, next to Docker and Helm-via-OCI; all three
  answer the same registry API. OCI images can also be pushed to and pulled from Docker
  repositories.
- OCI is also available as a federated repository; its form carries the same tag limit and tag
  retention fields as Docker (see [../replication-and-federation.md](../replication-and-federation.md)).
- Creating a repository requires the system administrator or a project administrator. UI location:
  administration, repositories, create repository, class, OCI format (UI-031 in
  [../ui-inventory.md](../ui-inventory.md)).
- Repository keys: OCI clients cannot reach a repository whose key contains an underscore or an
  upper-case letter (`oci_local` and `OCI-local` fail, `oci-local` works); length limits as for
  Docker (PKG-DOCKER-002 in [docker.md](docker.md)).
- Differences from Docker repositories: no registry API v1 (PKG-DOCKER-004), no schema-1 blocking
  setting (PKG-DOCKER-021), the "store artifacts locally" switch does not apply to OCI remotes
  (RMT-017 in [../remote-repositories.md](../remote-repositories.md)), and OCI virtual repositories
  have their own metadata cache period (PKG-OCI-004).

### PKG-OCI-002 · Local OCI repository settings
Priority: **P1**

| Setting | Meaning | Default |
|---|---|---|
| Maximum unique tags | Distinct tag names kept per image; the oldest is removed when a new one exceeds the limit | blank: all tags kept |
| Tag retention | Revisions of an overwritten tag that are kept | 1: overwritten revisions are not kept |

Behavior of both settings: PKG-DOCKER-028 and PKG-DOCKER-029 in [docker.md](docker.md). UI
location: local repository form, OCI settings section. Placement among repository settings:
REPO-027 in [../repositories.md](../repositories.md).

### PKG-OCI-003 · Proxying OCI-compliant registries
Priority: **P1**

- A remote OCI repository fetches from an OCI-compliant registry, or from a repository on another
  instance of the system (RMT-040 in [../remote-repositories.md](../remote-repositories.md)). The
  form asks the user to check the upstream URL.
- There are no OCI-specific remote settings; the general remote settings apply, including the
  bearer option for upstream authentication (RMT-006). Foreign-layer caching is specified for
  Docker and Helm-via-OCI remotes only (PKG-DOCKER-036 in [docker.md](docker.md)).
- Metadata retrieval cache period: 21,600 s by default for OCI remotes (RMT-018).
- Docker Hub as upstream: authenticating the remote is advisable to avoid rate limits and for
  compliance. A figure of 100 pulls per six hours applies to shared unauthenticated access from
  hosted (SaaS) instances under excessive use, not to a self-managed instance (PKG-DOCKER-010,
  PKG-DOCKER-034 in [docker.md](docker.md)).
- The OCI specification lets a registry act as a pull-through proxy and defines an optional `ns`
  query parameter in which clients name the source registry host; a proxy may use it to choose an
  upstream and then echoes it in the `OCI-Namespace` response header. Whether the system honors
  `ns` is unspecified.
- Privileges: read plus deploy/cache to fetch content that is not cached yet (IAM-023 in
  [../identity-and-access.md](../identity-and-access.md)).

### PKG-OCI-004 · Virtual OCI repositories
Priority: **P1**

| Setting | Meaning | Default |
|---|---|---|
| Metadata retrieval cache period | Seconds that metadata from member repositories is cached before the members are asked again; 0 disables caching | 7,200 s |

- Pulls resolve through the members in resolution order (REPO-035 in
  [../repositories.md](../repositories.md)); pushes go to the default deployment repository
  (REPO-038). The ORAS and Helm examples on this page pull from and push to a virtual OCI
  repository.
- Timestamp-based tag resolution is specified for Docker virtual repositories only
  (PKG-DOCKER-040).

### PKG-OCI-005 · How OCI clients address a repository
Priority: **P1**

OCI clients have the same constraints as the Docker client: a registry is a host with an optional
port, and the API lives at `/v2/` on that host. The three access methods of the system (repository
path, subdomain, port; PKG-DOCKER-005 to PKG-DOCKER-008 in [docker.md](docker.md)) apply unchanged.

- Login commands take only the host: `podman login repo.example.com`,
  `oras login repo.example.com`, `helm registry login repo.example.com`. Helm's documentation
  rejects a scheme or a path in the login host. Open: whether the system also accepts an
  `https://` URL passed to `helm registry login`; the spec decides.
- Under the repository path method, references look like `repo.example.com/oci-local/app:1.0`.
- Authentication and anonymous access: PKG-DOCKER-011 and PKG-DOCKER-012.

## Distribution protocol

### PKG-OCI-006 · Distribution specification endpoints
Priority: **P1**

The system supports the relevant calls of the OCI distribution specification, so OCI clients
work transparently. The specification groups its API in four categories: pull (mandatory for
conformance), push, content discovery and content management (each optional, but all-or-nothing
when claimed). Pull, push, tag listing and referrers are supported; which further optional calls the
system implements is unspecified.

<details><summary>Endpoint reference (OCI distribution specification)</summary>

| Method | Path | Category | Success | Failure | Notes |
|---|---|---|---|---|---|
| GET | `/v2/` | support check | 200 | 401, 404 | Also the authentication probe (PKG-DOCKER-012) |
| GET, HEAD | `/v2/<name>/blobs/<digest>` | pull | 200 | 404 | `Docker-Content-Digest` on success; `Range` should be supported |
| GET, HEAD | `/v2/<name>/manifests/<tag-or-digest>` | pull | 200 | 404 | `Docker-Content-Digest`; `HEAD` also `Content-Length` |
| POST | `/v2/<name>/blobs/uploads/` | push | 202 | 404 | Opens an upload session; `Location` header |
| POST | `/v2/<name>/blobs/uploads/?digest=<digest>` | push | 201, 202 | 404, 400 | Single-request upload; optional for registries (202 means "continue with PUT") |
| POST | `/v2/<name>/blobs/uploads/?digest-algorithm=<alg>` | push | 201, 202 | 404, 400 | Upload with a digest algorithm other than SHA-256 |
| POST | `/v2/<name>/blobs/uploads/?mount=<digest>&from=<other>` | push | 201, 202 | 404 | Cross-repository mount; 202 falls back to a normal upload |
| PATCH | `<upload location>` | push | 202 | 404, 416 | Chunk with `Content-Range`; chunks in order; `Range` in the reply |
| PUT | `<upload location>?digest=<digest>` | push | 201 | 404, 400, 416 | Closes the session, optionally with the last chunk |
| GET | `<upload location>` | push | 204 | 404 | Upload status (`Range`) |
| DELETE | `<upload location>` | push | 204 | 404, 400 | Cancels the session |
| PUT | `/v2/<name>/manifests/<tag-or-digest>` | push | 201 | 404, 413 | `Location` and `Docker-Content-Digest` in the reply |
| PUT | `/v2/<name>/manifests/<digest>?tag=a&tag=b` | push | 201 | 404, 413 | Optional: extra tags; accepted tags echoed in `OCI-Tag` headers |
| GET | `/v2/<name>/tags/list?n=<int>&last=<tag>` | discovery | 200 | 404 | Lexical order; optional `Link: rel="next"` |
| GET | `/v2/<name>/referrers/<digest>?artifactType=<type>` | discovery | 200 | 404, 400 | PKG-OCI-009 |
| DELETE | `/v2/<name>/manifests/<tag-or-digest>` | management | 202 | 404, 400, 405 | Deleting by tag removes the tag; by digest removes the manifest and its tags |
| DELETE | `/v2/<name>/blobs/<digest>` | management | 202 | 404, 400, 405 | — |

</details>

<details><summary>Error codes</summary>

A 4xx response may carry a JSON body `{"errors":[{"code","message","detail"}]}`; `code` is one of:
`BLOB_UNKNOWN`, `BLOB_UPLOAD_INVALID`, `BLOB_UPLOAD_UNKNOWN`, `DIGEST_INVALID`,
`MANIFEST_BLOB_UNKNOWN`, `MANIFEST_INVALID`, `MANIFEST_UNKNOWN`, `NAME_INVALID`, `NAME_UNKNOWN`,
`SIZE_INVALID`, `UNAUTHORIZED`, `DENIED`, `UNSUPPORTED`, `TOOMANYREQUESTS`. A `429` should carry
`Retry-After`. Optional informational `Warning: 299 - "<text>"` headers are limited to 4,096 bytes
in total.

</details>

Rules from the specification that clients depend on:

- Names match `[a-z0-9]+((\.|_|__|-+)[a-z0-9]+)*(\/[a-z0-9]+((\.|_|__|-+)[a-z0-9]+)*)*`; tags match
  `[a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}`. Many clients cap host, `/` and name together at 255
  characters.
- Concurrent uploads must work; each session gets its own upload location, which may be relative
  or absolute and may point to another server. Clients must use it exactly as returned.
- Any response may be a redirect (for example to object storage); clients must not forward the
  `Authorization` header to another host unless configured to.
- Deletion may be disabled entirely or per kind; then the registry answers 400 or 405. Whether the
  system accepts these deletes is unspecified; its own deletion paths are PKG-DOCKER-025 and
  PKG-DOCKER-031 in [docker.md](docker.md).

### PKG-OCI-007 · Manifest kinds and content negotiation
Priority: **P1**

- Manifest kinds: OCI image manifest (`application/vnd.oci.image.manifest.v1+json`) and OCI image
  index (`application/vnd.oci.image.index.v1+json`), besides the Docker kinds listed in
  PKG-DOCKER-020 in [docker.md](docker.md).
- Clients set `Content-Type` to the manifest's `mediaType`; the registry stores the exact bytes and
  returns the same `Content-Type` on pull, without parameters. Clients reject a manifest whose
  `mediaType` field differs from `Content-Type`.
- A manifest whose `subject` points to a manifest that does not exist (yet) must be accepted, so
  referrers can be pushed before their subject (PKG-OCI-009). A registry may reject other manifests
  that reference unknown blobs or manifests, with `MANIFEST_BLOB_UNKNOWN`.
- The registry should accept manifests of at least 4 MB and answer `413` above its limit; the
  system's limit is unspecified.
- Content negotiation: clients send `Accept` with the kinds they understand; how the system answers
  a request whose `Accept` excludes the stored kind is unspecified.

### PKG-OCI-008 · Non-image artifacts
Priority: **P1**

An OCI repository stores more than container images: any content packaged as an OCI manifest with
blobs, for example Helm charts, SBOMs, signatures and attestations, WebAssembly modules, arbitrary
files pushed with ORAS (PKG-OCI-017), and BuildKit build caches (PKG-OCI-015). The same holds
for Helm-via-OCI repositories ("any OCI-compatible file").

- No restriction on config media types or `artifactType` values is specified.
- The scanning engine handles only container images that follow the OCI or Docker image format
  (PKG-OCI-020).
- Generic item operations (browse, copy, move, delete, properties) see artifacts in the storage
  layout of PKG-DOCKER-043.

### PKG-OCI-009 · Referrers API
Priority: **P1**

The referrers API (distribution specification 1.1) links a manifest to the manifests that declare it
as their `subject`: signatures, SBOMs, attestations, scan results and similar.

- `GET /v2/<name>/referrers/<digest>` returns an OCI image index (`Content-Type:
  application/vnd.oci.image.index.v1+json`) with one descriptor per referring manifest or index in
  the same `<name>`: `mediaType`, `size`, `digest`, `artifactType` (the manifest's own, else its
  config `mediaType`; omitted for an index without one) and the referrer's `annotations`.
- No referrers: an empty index with `200`. A registry that supports the API must not answer `404`
  for a known repository; an invalid digest gives `400`.
- Long lists are paginated with `Link: <...>; rel="next"`.
- Optional filter `?artifactType=<type>`; when applied, the response carries
  `OCI-Filters-Applied: artifactType`.
- Pushing a manifest with a `subject`: the registry answers with `OCI-Subject: <subject digest>`.
  This header is the way to verify that the system processed the referrer.
- A lookup by digest or by tag through the system's own path layout also returns the list of
  everything that references the image; the layout is unspecified.
- Clients: `oras discover repo.example.com/oci-local/app:1.0` lists referrers; cosign 2.0 or later
  attaches signatures and attestations (`cosign attest`); older cosign needed
  `COSIGN_EXPERIMENTAL=1`.
- Privilege: unspecified (read on the repository is the natural requirement).

### PKG-OCI-010 · Referrer storage and the referrers index
Priority: **P1**

- A referring manifest pushed with a `subject` is stored as its own image in the folder of the
  image it refers to; its manifest item keeps the `subject` field.
- The system maintains one referrers index item per subject image, updated when the image changes
  or a new referrer arrives. Only users with the manage action (IAM-022 in
  [../identity-and-access.md](../identity-and-access.md)) can see this item. Its name and format
  are unspecified.
- This applies to OCI repositories, and Helm-via-OCI repositories use the same API. For Docker
  repositories it presumably follows from the shared registry API, but is unspecified.

### PKG-OCI-011 · Referrers tag schema
Priority: **P2**

For registries without the referrers API, the specification defines a fallback: clients keep an
image index under the tag `<algorithm>-<encoded digest>` (algorithm truncated to 32 and digest to
64 characters, disallowed characters replaced by `-`), for example
`sha256-aaaa...`, and update it themselves when pushing or deleting referrers.

- Clients fall back to the tag when the referrers endpoint answers `404`, or when a push with
  `subject` comes back without `OCI-Subject`.
- When a registry enables the referrers API, its responses must include manifests already listed in
  such tag indexes, and may include all older manifests with a `subject`.
- How the system treats existing fallback tags and older manifests with a `subject` is unspecified.

### PKG-OCI-012 · Moving referrers with their subject
Priority: **P2**

Images and their referrers can be retrieved together and transferred between repositories.
Whether promotion (PKG-DOCKER-032 in [docker.md](docker.md)) or copy and move
(ART-032 and ART-033 in [../artifacts.md](../artifacts.md)) carry referrers along, and how the
referrers index of the target is rebuilt, is unspecified.

## Behavior shared with Docker repositories

The following behavior is common to Docker, OCI and Helm-via-OCI repositories and is catalogued
once:

| Behavior | Entry |
|---|---|
| Access methods and registry addressing | PKG-DOCKER-005 to PKG-DOCKER-009 |
| Repository key constraints | PKG-DOCKER-002 |
| Client login, stored credentials and token challenge | PKG-DOCKER-011, PKG-DOCKER-012 |
| Blob upload, layer deduplication, manifest push | PKG-DOCKER-018 to PKG-DOCKER-020 |
| Catalog and tag listing | PKG-DOCKER-022 |
| Multi-architecture indexes and their deletion | PKG-DOCKER-023, PKG-DOCKER-025 |
| Overwrite permission for existing tags | PKG-DOCKER-027 |
| Maximum unique tags, tag retention | PKG-DOCKER-028, PKG-DOCKER-029 |
| Download statistics shared across tags of one digest | PKG-DOCKER-030 |
| Promotion between repositories | PKG-DOCKER-032 |
| Storage layout | PKG-DOCKER-043 |

All entries are in [docker.md](docker.md).

## Clients

### PKG-OCI-013 · Podman
Priority: **P1**

- Login: `podman login repo.example.com`, or credentials written to
  `${XDG_RUNTIME_DIR}/containers/auth.json` (fallback `~/.docker/config.json`, or a file given with
  `--authfile`) as an `auths` entry with Base64 `auth` (PKG-DOCKER-011).
- Push: `podman tag <image id> repo.example.com/oci-local/app:1.0`, then
  `podman push repo.example.com/oci-local/app:1.0 -f=oci`; `-f=oci` makes Podman push the OCI
  manifest format.
- Pull: `podman pull repo.example.com/oci-local/app:1.0`.
- Anonymous use works when anonymous access is enabled (IAM-005).

### PKG-OCI-014 · Docker Buildx
Priority: **P1**

- Authenticate with `docker login repo.example.com`.
- Single platform: `docker buildx build --platform linux/amd64 -t repo.example.com/oci-local/app:1.0 . --push`.
- Several platforms: `docker buildx create --use`, then
  `docker buildx build --platform linux/amd64,linux/arm64,linux/arm/v7,linux/s390x,linux/ppc64le -t repo.example.com/oci-local/app:1.0 . --push`;
  the result is an image index (PKG-DOCKER-023).
- Buildx has no pull command; pulls go through `docker pull`.

### PKG-OCI-015 · BuildKit and registry build caches
Priority: **P2**

- Authenticate with `docker login`; BuildKit's `buildctl` builds through the daemon and pushes:

```shell
buildctl build --frontend=dockerfile.v0 --local context=. --local dockerfile=. \
  --output type=image,name=repo.example.com/oci-local/app:1.0,push=true \
  --export-cache type=registry,ref=repo.example.com/oci-local/app:buildcache,mode=max,push=true \
  --import-cache type=registry,ref=repo.example.com/oci-local/app:buildcache
```

- The registry thus stores BuildKit cache manifests and blobs next to images and must serve them
  back on import.
- A cache export can also target the same reference as the image. A separate cache reference (as
  above) keeps the image tag from being replaced by the cache manifest; this is an observation, not
  a requirement.
- `buildctl` has no pull command; pulls go through `docker pull`.

### PKG-OCI-016 · WebAssembly modules
Priority: **P2**

[wasm-to-oci](https://github.com/engineerd/wasm-to-oci) packages WebAssembly modules as OCI
artifacts:

- Authenticate with `docker login repo.example.com`.
- `wasm-to-oci push ./module.wasm repo.example.com/oci-local/module:1.0`
- `wasm-to-oci pull repo.example.com/oci-local/module:1.0`
- Such artifacts are not scanned (PKG-OCI-020).

### PKG-OCI-017 · ORAS
Priority: **P1**

[ORAS](https://oras.land/) pushes and pulls arbitrary files as OCI artifacts.

- Login: `oras login repo.example.com` (interactive) or
  `oras login -u <user> -p <identity token> repo.example.com` (TOK-005 in
  [../access-tokens.md](../access-tokens.md)).
- Push: `oras push repo.example.com/oci-local/app:1.0 app.json`.
- Pull: `oras pull repo.example.com/oci-virtual/app:1.0` (through a virtual repository).
- Discover referrers: `oras discover` (PKG-OCI-009).

### PKG-OCI-018 · Helm charts in OCI repositories
Priority: **P1**

Helm 3.8 and later stores charts in any OCI repository, not only in the dedicated Helm-via-OCI
format (PKG-HELM-017 in [helm.md](helm.md)):

- `helm registry login repo.example.com` (or with `-u <user> -p <identity token>`).
- `helm push app-1.0.0.tgz oci://repo.example.com/oci-virtual`: Helm derives the image name from
  the chart name and the tag from the chart version ([Helm registries](https://helm.sh/docs/topics/registries/)).
- `helm pull oci://repo.example.com/oci-virtual/app --version 1.0.0`; without `--version` the latest
  version is pulled.
- Chart manifests, provenance layers and dependency resolution: PKG-HELM-018 in [helm.md](helm.md).

### PKG-OCI-019 · Client setup instructions
Priority: **P1**

The client setup dialog (UI-021 and UI-026 in [../ui-inventory.md](../ui-inventory.md)) offers
snippets for the OCI clients of this page (Podman, Buildx, BuildKit, wasm-to-oci, ORAS, Helm), with
host, repository and token filled in. The snippets follow the active access method (PKG-OCI-005).

## Limitations

### PKG-OCI-020 · Scanning limited to container images
Priority: **Later**

The scanning engine analyses only container images that follow the OCI or Docker image format.
Other content wrapped in OCI (WebAssembly modules, arbitrary ORAS payloads) and Helm charts are not
scanned. Scanning: [../security-scanning-and-curation.md](../security-scanning-and-curation.md).

## Notes for implementers

- Treat the registry as content-agnostic: accept any config media type and `artifactType`,
  including the empty config descriptor of the OCI image specification 1.1, and never assume a
  manifest describes a runnable image.
- Accept a manifest whose `subject` does not exist yet, answer `OCI-Subject` when the subject was
  processed, and never answer `404` on the referrers endpoint for an existing repository. Missing
  any of these sends clients into the tag-schema fallback (PKG-OCI-011).
- Keep the referrers list consistent under concurrent pushes: several signers can attach to one
  image at the same moment.
- Upload sessions are per request, may run in parallel for the same name, and must survive being
  served by different replicas; the upload location is opaque to the client and can carry state.
- Blob downloads may redirect to presigned object-storage URLs; clients drop `Authorization` on the
  cross-host hop, so the target URL must authorize the request on its own.
- Tag listings must be in lexical order and paginate with `n`, `last` and preferably `Link`;
  clients that count results stop early if a page is short without a `Link` header.
- Validate names and tags with the specification's grammars, and digests by algorithm; SHA-256 is
  the default, other algorithms arrive through `digest-algorithm`.
- Docker v2, OCI and Helm-via-OCI repositories share one implementation of this page; only the
  settings in PKG-OCI-001 differ.
