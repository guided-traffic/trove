# Docker image format

This page catalogs what a repository manager does to act as a Docker registry: hosting images in
local repositories, proxying Docker Hub and other registries in remote repositories, aggregating
both behind one registry address in virtual repositories, the three ways a Docker client can
address a repository, client authentication, the registry HTTP API v2, multi-architecture images,
tag overwrite protection and tag limits, image promotion, browsing, and the legacy v1 API. The wire
protocol is owned by Docker and the Open Container Initiative, not by any repository manager: the
[OCI distribution specification](https://github.com/opencontainers/distribution-spec/blob/main/spec.md)
(derived from the Docker Registry HTTP API V2) and the
[Docker registry token authentication specification](https://distribution.github.io/distribution/spec/auth/token/)
define what clients send; entries that rely on them say so. Docker, OCI and Helm-via-OCI
repositories share one registry API surface: generic OCI behavior (endpoint reference, referrers,
non-image artifacts) is on [oci.md](oci.md), Helm charts on [helm.md](helm.md). Repository classes
in general are in [../repositories.md](../repositories.md), pull-through caching in
[../remote-repositories.md](../remote-repositories.md).

Throughout this page `repo.example.com` is the host name of the system, `docker-local`,
`docker-remote` and `docker-virtual` are example repository keys, `<image>` is an image name (what
Docker itself calls a "repository", for example `library/nginx`), and `<tag>` is a tag.

## Repositories and addressing

### PKG-DOCKER-001 · Docker registries in all three repository classes
Priority: **P1**

A Docker repository of the system is a complete registry in Docker's sense: it holds any number of
image names, each with any number of tags. The two meanings of "repository" differ: Docker calls the
set of tagged images under one name a repository; the system calls the container of many such names
a repository. The number of Docker repositories per instance is not limited, so one instance can
serve many independent registries (per team, per stage).

| Class | Purpose |
|---|---|
| Local | Images built by the organization (first and second party) |
| Remote | Pull-through cache of Docker Hub, any other registry, or a repository on another instance (PKG-DOCKER-033, PKG-DOCKER-038) |
| Virtual | One address in front of several local and remote Docker repositories (PKG-DOCKER-039) |

- Creating a Docker repository requires the system administrator or a project administrator; other
  users are not offered the action. UI location: administration, repositories, create repository,
  pick the class, pick the Docker format (UI-031 in [../ui-inventory.md](../ui-inventory.md)).
- Local form: repository key (PKG-DOCKER-002), registry API version (PKG-DOCKER-004), maximum
  unique tags and tag retention (PKG-DOCKER-028, PKG-DOCKER-029), schema-1 blocking
  (PKG-DOCKER-021).
- Remote form: the user is asked to check the upstream URL and change it if needed; the upstream
  for Docker Hub is Docker Hub's registry endpoint `https://registry-1.docker.io/`. Whether this
  value is pre-filled is unspecified.
- Virtual form: one or more local or remote Docker members and an optional default deployment
  repository (REPO-038 in [../repositories.md](../repositories.md)).
- Docker repositories are OCI-compliant: OCI clients can push and pull OCI
  images to and from a Docker repository (see [oci.md](oci.md)).

### PKG-DOCKER-002 · Repository keys usable as registry names
Priority: **P1**

A Docker repository key may end up as a DNS label (PKG-DOCKER-007) and always ends up in image
references, so it obeys stricter rules than other keys (REPO-004 in
[../repositories.md](../repositories.md)).

| Rule | Value |
|---|---|
| Underscore | not allowed: the Docker client cannot talk to a registry whose host name contains `_` (`docker_dev_local` fails; `docker-dev-local` or `docker.dev.local` work) |
| Upper-case letters | not allowed for container formats (specified for OCI keys; REPO-004 applies it to Docker, OCI and Helm-via-OCI) |
| Hard length limit | 63 characters for Docker, OCI and Helm-via-OCI keys; enforced, cannot be exceeded |
| Soft length limit | 63 − (length of the server name + 1); with a server name of 8 characters the soft limit is 54 |

- Exceeding the soft limit produces a warning when the repository is created through the UI or the
  API; creation still succeeds. In a hosted deployment that creates DNS records per repository
  (PKG-DOCKER-010), no record is then created for the subdomain, so the repository is unreachable
  by subdomain unless the operator manages DNS.
- Keys of 63 characters or more carried over from a self-managed instance can cause access
  problems after a move to such a hosted deployment.
- Both limits stem from the subdomain method of a hosted deployment. Whether a self-managed
  instance enforces the hard limit, or warns on the soft limit, is unspecified.
- 63 is also the maximum length of one DNS label; whether that is the reason for the hard limit is
  unspecified.

### PKG-DOCKER-003 · Image-name visibility for readers
Priority: **P1**

Every user with read permission on a repository can see the repository's metadata, which includes
the list of image names it holds. This holds even for users whose read grant is limited to a path
inside the repository. Downloading content still follows the permission policies (IAM-022 in
[../identity-and-access.md](../identity-and-access.md)): users see names of images they may not
pull.

- Recommended isolation pattern: one local repository per team, all aggregated by a virtual
  repository. Developers resolve through the single virtual address; each team sees only the
  metadata of the repositories it can read.
- Affects the catalog listing (PKG-DOCKER-022, SRCH-020 in
  [../search-and-query.md](../search-and-query.md)) and the package list in the UI.

Security note: path-scoped read grants do not hide image names. An implementation that wants
names hidden must filter catalog and package listings by path permission; such filtering is
unspecified.

### PKG-DOCKER-004 · Registry API version per repository
Priority: **P1**

| Setting | Meaning | Default |
|---|---|---|
| Registry API version | Protocol the repository speaks: Docker registry API v1 or v2 | v2 |

- OCI repositories do not support v1.
- The repository path addressing method (PKG-DOCKER-006) works only with v2 repositories.
- v1 behavior is described in PKG-DOCKER-049 and PKG-DOCKER-050.
- UI location: local repository form, container settings section.

### PKG-DOCKER-005 · How clients address a repository
Priority: **P1**

Two properties of the Docker client shape the addressing:

1. A registry address is a host name with an optional port, nothing else. A context path such as
   `host:port/some-prefix` is not accepted as a registry; the client always calls `/v2/` at the
   root of the host.
2. The client sends Basic credentials only to HTTPS registries, or to registries the daemon is
   configured to treat as insecure.

So the repository must be identified either by the host, by the port, or by the first segment of
the image name. The system offers three access methods:

| Method | Example image reference | Repository chosen by | Operator needs |
|---|---|---|---|
| Repository path | `repo.example.com/docker-virtual/hello-world:1.0` | First path segment of the image name | One host for all repositories; no proxy changes per repository (PKG-DOCKER-006) |
| Subdomain | `docker-virtual.repo.example.com/hello-world:1.0` | Leading DNS label of the host | Wildcard DNS and a wildcard TLS certificate; reverse proxy configured once (PKG-DOCKER-007) |
| Port | `repo.example.com:5001/hello-world:1.0` | Port the request arrives on | One port and one proxy mapping per repository (PKG-DOCKER-008) |

- The method is a system-wide setting (PKG-DOCKER-009) that holds exactly one value, so one
  method is active at a time. Subdomain and port also need a per-repository value (registry name,
  PKG-DOCKER-007; registry port, PKG-DOCKER-008).
- The method also governs how Helm charts in OCI repositories are reached, even when an instance
  serves only charts (PKG-HELM-020 in [helm.md](helm.md)).
- Address formats of a hosted deployment are out of scope (PKG-DOCKER-010).

### PKG-DOCKER-006 · Repository path method
Priority: **P1**

The repository key is the first segment of the image name. For the request
`GET /v2/docker-virtual/library/nginx/manifests/1.27` the system serves repository `docker-virtual`,
image `library/nginx`, tag `1.27`.

- Works without any reverse proxy: the system's own web server answers registry requests directly.
  When this method is selected, registry requests are handled by the application server even if a
  reverse proxy sits in front of the system for other reasons.
- UI: administration, general, HTTP settings: container access method "repository path"; for
  operation without a proxy the server provider is set to the built-in web server.
- Only for v2 repositories (PKG-DOCKER-004).
- Without a reverse proxy only plain HTTP is specified, so every client must list the registry as
  insecure in its daemon configuration and restart the daemon; importing a self-signed
  certificate into the client's trust store avoids the restart.
- Clients must not use `localhost` or `127.0.0.1` as the registry host (a restriction of the
  Docker client); use the machine's IP address. The system's own context path is
  omitted from the registry address.
- One `docker login repo.example.com` covers all repositories, because all share one host.
- A tool that parses image references must accept three components: registry, repository key,
  image (PKG-DOCKER-047).
- How the catalog (`/v2/_catalog`) is scoped to one repository under this method is unspecified.

### PKG-DOCKER-007 · Subdomain method
Priority: **P2**

The repository key is the leading label of the host: `docker-virtual.repo.example.com`.

| Setting | Scope | Meaning | Default |
|---|---|---|---|
| Registry name | Docker repository, advanced tab | Host label under which the repository answers in the subdomain method | the repository key, filled in automatically while the subdomain method is active |

- The reverse proxy is configured once; new repositories need no proxy change. The generated NGINX
  configuration matches host names with a regular expression whose leading capture is the
  repository key, and also accepts the bare server name.
- The federated repository form lists the registry name field too, also filled automatically.
- Needs a wildcard TLS certificate (for example `*.repo.example.com`) that covers the number of
  subdomain levels in use, and wildcard DNS.
- Recommended when a wildcard certificate is available: a one-time proxy effort,
  and the closest fit to how Docker expects registries to be addressed.
- Each repository is a different host for the client: `docker login` per host, and credentials
  stored per host. A companion CLI that logs in automatically to the configured base host logs in
  to the wrong host under this method (PKG-DOCKER-047).
- The soft key-length limit (PKG-DOCKER-002) exists because of this method.

### PKG-DOCKER-008 · Port method
Priority: **P2**

Each repository is bound to its own port on the public server name: `repo.example.com:5001`.

| Setting | Scope | Meaning | Default |
|---|---|---|---|
| Registry port | Docker repository, advanced tab | Port the client uses for this repository together with the public server name; unique per repository | assigned automatically according to the federated repository form; unspecified for local, remote and virtual repositories |

- Every repository to be reachable must have its port set before the proxy configuration is
  generated; each new repository needs a new proxy mapping. Firewalls may block the extra ports.
- Recommended: bind one port to a virtual repository that aggregates the others, so only one
  mapping is needed.
- When a port other than 443 is used, the proxy must forward the port in the `Host` header (for
  NGINX `proxy_set_header Host $host:<port>`). This applies to the legacy v1 setup and equally to
  any port binding.
- With HAProxy, each port rewrites `/v2/...` to the internal registry endpoint of one repository;
  one port can route `/v1/...` and `/v2/...` to different repositories.
- This method is an easy way to get started, at the cost of one proxy change per new repository.

### PKG-DOCKER-009 · Reverse-proxy configuration for registry access
Priority: **P2**

The system stores a reverse-proxy description and generates a ready configuration file for NGINX
or Apache from it. Only for self-managed deployments.

<details><summary>Reverse-proxy settings</summary>

| Setting | Meaning | Default |
|---|---|---|
| Server provider | Built-in web server (no proxy), NGINX or Apache; the API accepts NGINX and Apache | unspecified |
| Internal application context path | Context path of the application behind the proxy | unspecified |
| Public context path | Context path seen by clients | unspecified |
| Public server name | Host name clients use; must match the TLS certificate | unspecified |
| Server name expression | Wildcard host pattern used by the subdomain method | unspecified |
| Internal host name | Host the proxy forwards to; `localhost` when proxy and application share a machine | unspecified |
| Internal ports | Ports the proxy forwards to: the UI form distinguishes the application's direct port (API and downloads) and a second internal port that serves the UI and the other services; the operation exposes one port | unspecified |
| TLS certificate path | Certificate file on the proxy host | unspecified |
| TLS key path | Key file on the proxy host | unspecified |
| Container access method | Subdomain, repository path, or port per repository (PKG-DOCKER-005) | unspecified |
| HTTP enabled / HTTP port | Serve plain HTTP on this port | port 80 |
| HTTPS enabled / HTTPS port | Serve HTTPS on this port | port 443 |

</details>

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| Get proxy configuration | none | All settings above | Administrator; other users receive only the access method, the ports and the repositories they may read |
| Update proxy configuration | All settings above | Success, or 400 for an invalid configuration | Administrator |
| Get generated NGINX configuration | none | Configuration text | Administrator |

- UI: administration, general, HTTP settings; a download button produces the configuration file,
  which the operator installs in the proxy and reloads.
- NGINX needs version 1.3.9 or later (chunked transfer encoding). Apache needs the proxy, rewrite,
  HTTP proxy, headers and SSL modules among others.
- In a high-availability setup the generator adds a load-balancing section that lists several
  nodes.
- The proxy must not cap request bodies (layer uploads are large; a cap returns 413) and must pass
  chunked transfer encoding (otherwise 411). Example configurations raise the proxy read timeout
  to 900 s (legacy v1 setup) and 2,400 s (general proxy setup).
- Request buffering in the proxy (on by default in current NGINX) holds the whole upload before
  forwarding it; the resulting stall can exceed client timeouts on large layers. Recommended:
  turn buffering off and use HTTP/1.1 towards the application.
- Binary caching in the proxy is not supported: a cache layer in between causes stale metadata and
  upload failures; a chained remote repository is the alternative (RMT-040 in
  [../remote-repositories.md](../remote-repositories.md)).

### PKG-DOCKER-010 · Hosted-service address formats
Priority: **Out of scope**

An operator-run, multi-tenant hosted service can offer path, subdomain (instance name and
repository key combined in one host label) and custom-domain (CNAME) variants, create DNS records
per repository automatically, and give its tenants unauthenticated Docker Hub access through a
shared remote, nominally unlimited but subject to a limit of 100 pulls per six hours under
excessive use. None of this applies to a self-hosted system.

## Client authentication

### PKG-DOCKER-011 · Login and stored client credentials
Priority: **P1**

- `docker login repo.example.com` (host without scheme) or `podman login repo.example.com` asks for
  user name and password and stores them. The password may be an access token (TOK-034 in
  [../access-tokens.md](../access-tokens.md)).
- Manual configuration (automation, v1): the Docker client reads `~/.docker/config.json`, which
  holds an `auths` object keyed by registry address; each entry has `auth` (Base64 of
  `user:password`) and optionally `email`. One file can hold entries for several registries.
- Podman reads `${XDG_RUNTIME_DIR}/containers/auth.json` first, falls back to
  `~/.docker/config.json`, and accepts another file through `--authfile`.
- A user name is always part of a Docker login, also when the secret is a token: a companion CLI
  (PKG-DOCKER-047) fails with a "user name is empty" error when it holds only a token, and its fix
  is to supply a user name with the token as password. The registry must therefore accept Basic
  authentication with a user name and a token as password. Whether the system checks that the
  user name matches the token's owner is unspecified.
- Passing secrets on the command line is discouraged; `--password-stdin` is the recommended form.
- Logged-in or anonymous: pulls without login work when anonymous access is enabled
  (IAM-005 in [../identity-and-access.md](../identity-and-access.md), AUTHN-036 in
  [../authentication-providers.md](../authentication-providers.md)) and the anonymous user holds
  read on the repository (and deploy/cache to fetch new content through a remote, IAM-023).
- The client sends credentials only over HTTPS or to insecure-listed registries (PKG-DOCKER-005).

### PKG-DOCKER-012 · Authentication challenge on the registry API
Priority: **P1**

Specified behavior: Docker clients work natively against the system, with login or
anonymously; the system's remote repositories can authenticate to upstream registries with bearer
tokens (PKG-DOCKER-033); the system's registry has its own bearer-token endpoint, which chained
instances use for event-based replication (PKG-DOCKER-038). Which challenge scheme the system
returns on `/v2/` (Basic or Bearer) is unspecified.

What clients expect if the registry answers with a bearer challenge (Docker registry token
authentication specification):

1. The client calls a registry endpoint; the registry answers `401` with
   `WWW-Authenticate: Bearer realm="<token URL>",service="<service>",scope="repository:<name>:pull,push"`
   and an error body with code `UNAUTHORIZED`.
2. The client requests `GET <realm>?service=<service>&scope=<scope>` with Basic credentials, or
   without credentials for anonymous access. `scope` repeats once per scope entry. Optional
   parameters: `offline_token=true` (also return a refresh token) and `client_id`.
3. The token service authenticates (failed authentication: `401`) and grants the intersection of
   requested and permitted actions. A partial or empty grant is not an error; the token simply
   carries less.
4. Response: JSON with `token` and/or `access_token` (equivalent), optional `expires_in` in seconds
   (60 when omitted; never less than 60), optional `issued_at` (RFC 3339), and `refresh_token` only
   when `offline_token=true` was sent.
5. The client repeats the original request with `Authorization: Bearer <token>`.

- The realm path is the implementation's choice; the client takes it from the challenge. It must
  be an absolute URL the client can reach under every access method.
- The token is opaque to the client; its format and lifetime are unspecified.

### PKG-DOCKER-013 · Credential snippet operation
Priority: **P2**

An operation that returns a ready-to-paste client configuration fragment, for automated workflows
and for v1 clients.

| Input | Meaning |
|---|---|
| Repository key | Path parameter |
| API version | v1 or v2, path parameter |
| Credentials | Basic authentication of the calling user |

Output: a JSON fragment keyed by the system's HTTPS address, with `auth` (Base64 of the caller's
user and password) and `email`. The client setup instructions (PKG-DOCKER-046) offer the same
snippet with the token filled in.

### PKG-DOCKER-014 · Docker client login through an OAuth session
Priority: **P2**

The Docker client can authenticate through the OAuth single sign-on with the preset GitHub
provider (AUTHN-010 in [../authentication-providers.md](../authentication-providers.md)) without
further credentials: automatic user creation must be on in the OAuth settings, the user logs in to
the system through the provider once, and afterwards the Docker client detects this and uses
OAuth. How the Docker client obtains a credential from that browser session is unspecified.

### PKG-DOCKER-015 · Image pull credentials for Kubernetes
Priority: **P1**

Kubernetes pulls private images from the system with a registry secret:

```shell
kubectl create secret docker-registry regcred \
  --docker-server=repo.example.com \
  --docker-username=k8s-puller \
  --docker-password="${TOKEN}" \
  --docker-email=ops@example.com \
  --namespace my-app
```

- One secret per namespace. Attach it to the namespace's default ServiceAccount
  (`imagePullSecrets`, recommended) or to each workload's pod spec.
- The password may be the user's password or an identity token (TOK-005 in
  [../access-tokens.md](../access-tokens.md)); the token's scope can be narrowed.
- Use a dedicated, minimally privileged user rather than an administrator.
- Kubernetes usually pulls by the digest it resolved earlier rather than by tag, which matters for
  download statistics (PKG-DOCKER-030) and for permission checks on digest paths.
- Passwordless pulls through cloud identities: CIOIDC-029 in
  [../oidc-ci-integration.md](../oidc-ci-integration.md).

## Registry protocol

### PKG-DOCKER-016 · Registry HTTP API v2
Priority: **P1**

The system implements the Docker Registry API calls the Docker client needs to work natively,
and Docker v2, OCI and Helm-via-OCI repositories share one registry API surface. The
endpoint reference is the OCI distribution specification, catalogued in PKG-OCI-006 in
[oci.md](oci.md). Docker-specific additions:

| Endpoint | Source | Notes |
|---|---|---|
| `GET /v2/` | OCI and Docker | Version check and authentication probe: `200` when the caller may proceed, `401` with a challenge otherwise (PKG-DOCKER-012) |
| `GET /v2/_catalog?n=&last=` | Docker registry API | Image names of a repository (PKG-DOCKER-022) |
| `GET /v2/<name>/tags/list?n=&last=` | OCI and Docker | Tags of an image (PKG-DOCKER-022) |

- Which optional parts of the specification the system implements (single-request upload,
  cross-repository mount, push of extra tags by query parameter, `DELETE` on manifests, tags and
  blobs) is unspecified.
- The `Docker-Distribution-API-Version` response header is a legacy Docker header; the OCI
  specification makes it optional and tells clients not to rely on it.
- Errors with a JSON body use `{"errors":[{"code":..., "message":..., "detail":...}]}` with the
  OCI error codes; Docker clients may also see schema-1 codes such as `TAG_INVALID`.

### PKG-DOCKER-017 · Pulling images
Priority: **P1**

- `GET` or `HEAD /v2/<name>/manifests/<tag-or-digest>` returns the manifest with its stored
  `Content-Type` and the `Docker-Content-Digest` header; `HEAD` also returns `Content-Length`.
  Clients send `Accept` with the manifest types they support. Because `HEAD` returns the digest, a
  client can resolve a tag to its digest without downloading the manifest.
- `GET /v2/<name>/blobs/<digest>` returns layers and config blobs; `Range` requests should be
  supported (resumable pulls).
- Tags match `[a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}`; image names match the OCI name grammar
  (lower case, `.`, `_`, `__`, `-` separators, `/` between components).
- Remote repository: a miss is fetched from the upstream and cached. Manifests reached by tag are
  expiring metadata, refreshed after the metadata retrieval cache period (21,600 s by default for
  Docker remotes, RMT-018 in [../remote-repositories.md](../remote-repositories.md)); content
  reached by digest does not change.
- Virtual repository: members are searched in resolution order (REPO-035 in
  [../repositories.md](../repositories.md)) unless timestamp resolution is on (PKG-DOCKER-040).
- Privilege: read on the repository; for content not yet cached in a remote, deploy/cache too
  (IAM-023 in [../identity-and-access.md](../identity-and-access.md)).

### PKG-DOCKER-018 · Pushing image layers
Priority: **P1**

Blob upload follows the OCI specification (details in PKG-OCI-006 in [oci.md](oci.md)):

1. `POST /v2/<name>/blobs/uploads/` opens an upload session; `202` with a `Location` header.
2. Either one `PUT <location>?digest=<digest>` with the whole blob, or `PATCH <location>` chunks
   with `Content-Range` (in order; out-of-order chunks get `416`) and a closing
   `PUT <location>?digest=<digest>`; success is `201` with the blob's `Location`.
3. `GET <location>` reports the received range; `DELETE <location>` cancels.

- Before uploading, clients probe with `HEAD /v2/<name>/blobs/<digest>`; an existing blob is not
  sent again (PKG-DOCKER-019).
- A digest that does not match the content must fail (`DIGEST_INVALID`).
- Push target: a local repository, or a virtual repository with a default deployment repository
  (an end-to-end test case pushes to a virtual repository). What a push to a remote
  repository returns is unspecified.
- Privilege: deploy on the target repository; replacing an existing tag also needs overwrite when
  PKG-DOCKER-027 is active.
- Reverse proxies and ingress controllers in front of the registry must allow unlimited body size
  and chunked uploads (PKG-DOCKER-009).

### PKG-DOCKER-019 · Layer deduplication
Priority: **P1**

- Storage is checksum-based with deduplication: a layer used by many images, tags and repositories
  is stored once (content-addressed storage in
  [../storage-and-blobs.md](../storage-and-blobs.md)).
- On the protocol, clients skip uploading blobs the registry already knows (existence check by
  `HEAD`), and may ask to mount a blob from another image name of the same registry
  (`POST /v2/<name>/blobs/uploads/?mount=<digest>&from=<other>`; `201` when mounted, `202` to fall
  back to a normal upload). Whether the system mounts across image names or across repositories is
  unspecified.
- Deleting a tag or an image removes layers that no other tag or repository uses any more
  (PKG-DOCKER-031); layers shared with other tags must survive deletion, tag eviction
  (PKG-DOCKER-028) and promotion overwrites (PKG-DOCKER-032).

### PKG-DOCKER-020 · Pushing manifests and tags
Priority: **P1**

- `PUT /v2/<name>/manifests/<tag-or-digest>` with `Content-Type` set to the manifest's media type.
  The registry stores the exact bytes it received (the digest is computed over them) and answers
  `201` with `Location` and `Docker-Content-Digest`.
- Manifest kinds Docker clients push: Docker image manifest v2 schema 2
  (`application/vnd.docker.distribution.manifest.v2+json`), Docker manifest list
  (`application/vnd.docker.distribution.manifest.list.v2+json`), OCI image manifest and OCI image
  index; the legacy schema 1 unless blocked (PKG-DOCKER-021). The exact set the system accepts is
  unspecified beyond "Docker v2 and OCI".
- Image labels are extracted at push and stored as properties (PKG-DOCKER-042).
- A manifest with a `subject` field is a referrer (PKG-OCI-009 in [oci.md](oci.md)).
- The maximum accepted manifest size is unspecified; the OCI specification expects at least 4 MB to
  be accepted and `413` above the limit.
- Pushing a tag that did not exist before fires the tag-pushed event (EVT-006 in
  [../events-and-extensibility.md](../events-and-extensibility.md)); whether an overwrite of an
  existing tag fires an event is unspecified.

### PKG-DOCKER-021 · Blocking schema-1 manifests
Priority: **P1**

| Setting | Repositories | Meaning | Default |
|---|---|---|---|
| Block image manifest v2 schema 1 | Docker local, remote and federated | Pushes of images whose manifest is the legacy schema 1 are rejected | off according to the federated repository form; unspecified elsewhere |

- The setting also appears on the basic tab of remote repositories; what "push" means for a
  remote repository (for example, refusing to cache schema-1 images) is unspecified.
- The rejection status code is unspecified.
- Not available on OCI repositories.

### PKG-DOCKER-022 · Catalog and tag listing
Priority: **P1**

- `GET /v2/_catalog` lists image names; `GET /v2/<name>/tags/list` lists tags of one image; both
  support `n` (page size) and `last` (continue after this value). The OCI specification requires
  tags in lexical order and allows a `Link: <...>; rel="next"` header; clients should prefer
  `Link` over computing `last`. With `n=0` the list is empty.
- Administrative operations, required privileges and the remote-cache fallback for both listings
  are catalogued in SRCH-020 and SRCH-021 in [../search-and-query.md](../search-and-query.md).
  Remote repositories can serve both listings from their cache: a system-level switch (off by
  default, effective after a restart) together with a second system-level value naming the
  upstream registry URL whose catalog and tags are cached; the metadata retrieval cache period
  controls the refresh interval.
- Tag listings return only entries that conform to the OCI and Docker tag rules, so digest-named
  entries kept by tag retention (PKG-DOCKER-029) stay hidden; a system-wide switch turns the filter
  off (default on).
- Catalogs show every image name in a readable repository regardless of path permissions
  (PKG-DOCKER-003).

## Multi-architecture images

### PKG-DOCKER-023 · Manifest lists and image indexes
Priority: **P1**

A multi-architecture image is a manifest list (Docker) or image index (OCI) whose entries point to
one image manifest per platform.

- Push with the Docker CLI: build and push each platform image, then
  `docker manifest create repo.example.com/docker-local/app:1.0 --amend <platform image> ...` (one
  `--amend` per platform) and `docker manifest push`.
- Push with Buildx in one step:
  `docker buildx create --use`, then
  `docker buildx build --platform linux/amd64,linux/arm64 -t repo.example.com/docker-local/app:1.0 . --push`.
- At upload the system records the relationship between a list and its sub-manifests. Tag limits
  (PKG-DOCKER-028), tag retention (PKG-DOCKER-029) and deletion (PKG-DOCKER-025) use these
  records to treat a list and its sub-manifests as one unit. Lists stored before relationship
  tracking existed carry no records and are not cleaned up together.
- Overwriting a list tag with a new list replaces only the list by default; the sub-manifests of
  the old list stay. A system-wide option replaces the sub-manifests as well (default off).
- Overwriting a list with a single-platform manifest (or the reverse) is a complete replacement:
  the old revision's sub-manifests are removed; with tag retention active the whole previous
  structure is kept as one revision.
- Moving, copying or deleting a list item with generic item operations does not include the
  images it references; both must be selected (specified as a limitation for Helm-via-OCI
  repositories).
- UI: the tag folder of a list shows a table of nested manifests: manifest digest, OS and
  architecture, size; manifests that are not available are shown greyed out; clicking one jumps to
  the item in the artifact browser.
- Finding the lists that reference a given single-platform manifest (for example before deleting
  it): SRCH-022 in [../search-and-query.md](../search-and-query.md).

### PKG-DOCKER-024 · Operating-system-qualified tag on platform-specific push
Priority: **P2**

When a platform-specific image is pushed, the system adds a second tag that includes the operating
system: pushing `my-app:1.0-amd64` also publishes `my-app:1.0-linux-amd64`. How the system
recognises a platform suffix in a tag, which platforms are covered, and whether the extra tag is
subject to tag limits are unspecified.

### PKG-DOCKER-025 · Delete a multi-architecture tag
Priority: **P1**

Deletes a list tag together with all platform manifests under it, in one operation.

| Input | Meaning |
|---|---|
| Repository key | Path parameter |
| Image path | Image name including namespace, for example `library/ubuntu` (JSON body) |
| Tag | Tag to delete (JSON body) |

- Authentication: bearer token (example); required permission: unspecified, presumably delete on
  the repository (IAM-022).
- Sub-manifests and layers that other tags of the same image in the same repository still use must
  not be deleted; deleting them is a known defect to guard against.
- UI: artifact browser, context menu on the tag, "delete architectures"; a dialog lists the
  platforms to be removed and asks for confirmation.
- Scope: local repositories holding Docker v2 or OCI images. This operation is the recommended way
  to remove any single tag there, list or not, because it handles lists and shared parts; the
  older registry-v1 delete calls remain only for schema-1 content.
- The tag-deleted event (EVT-006) covers tag deletion in general; that this operation fires it is
  implied but unspecified.

## Tags, overwrite and retention

### PKG-DOCKER-026 · Tag syntax validation
Priority: **P1**

- Valid tags: `[a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}` (OCI specification).
- Tag listings hide non-conforming entries (PKG-DOCKER-022).
- Retagging during promotion (PKG-DOCKER-032) validates the target tag against the specification
  by default. One system-wide switch disables both the listing filter and the promotion
  validation.
- Whether a manifest push with a non-conforming tag is rejected, and with which status, is
  unspecified.

### PKG-DOCKER-027 · Overwrite permission for existing tags
Priority: **P1**

| Setting | Scope | Meaning | Default |
|---|---|---|---|
| Require overwrite permission for existing tags | System-wide, applies to Docker, OCI and Helm-via-OCI repositories | Pushing an image under an image name and tag that already exist requires the delete/overwrite action in addition to deploy | on |

- Purpose: prevents different manifest kinds from coexisting under the same tag, and makes tags
  immutable for anyone who holds deploy but not delete/overwrite (IAM-022 in
  [../identity-and-access.md](../identity-and-access.md)). There is no per-repository
  "immutable tags" setting; immutability comes from permissions.
- The switch is transitional: it is meant to disappear and the behavior to become permanent.
- UI: administration, package format settings, section for Docker, OCI and Helm-via-OCI.
  Prerequisite to change it: administrator or project administrator.
- Whether re-pushing an identical manifest (same digest) under the same tag counts as an overwrite
  is unspecified. Docker clients commonly re-push unchanged tags, so the answer decides whether CI
  pipelines without overwrite permission fail.

### PKG-DOCKER-028 · Maximum unique tags per image
Priority: **P1**

| Setting | Repositories | Meaning | Default |
|---|---|---|---|
| Maximum unique tags | Docker, OCI and Helm-via-OCI local (and federated) repositories | Number of distinct tag names kept per image; when a push of a new tag name exceeds it, the oldest tag of that image is removed (first in, first out) | 0 or blank: no limit |

- Evaluated lazily: only when a tag of that image is pushed. No job sweeps existing images after
  the limit is lowered.
- `latest` is not exempt: pushing enough other tags removes it like any other tag.
- When a tag is evicted, all its retained revisions (PKG-DOCKER-029) go with it; the cap takes
  precedence over retention.
- A list tag is evicted with its sub-manifests as one unit (PKG-DOCKER-023). Layers and blobs that
  nothing else references are removed too; keeping them would waste storage.
- Removed tags go to the trash can if it is enabled (ART-036 in
  [../artifacts.md](../artifacts.md)); enable it before relying on tag limits.
- Settings placement: REPO-027 in [../repositories.md](../repositories.md); interaction with
  cleanup policies (keep-last-N wins): RET-010 and RET-035 in
  [../retention-and-cleanup.md](../retention-and-cleanup.md).

Example with a limit of 3: tags pushed in the order 1.0.0, 1.0.1, 2.0.0 are all kept; pushing 2.0.1
removes 1.0.0.

### PKG-DOCKER-029 · Retention of overwritten tag revisions
Priority: **P1**

| Setting | Repositories | Meaning | Default |
|---|---|---|---|
| Tag retention | Docker, OCI and Helm-via-OCI local (and federated) repositories | How many revisions of the same tag are kept when the tag is overwritten | 1: overwritten revisions are not kept |

- With 2 or more, the current revision is copied before the overwrite and the copy is renamed after
  its SHA-2 hash; the exact naming is unspecified. Beyond the limit the oldest revision is
  removed (first in, first out). Applies to every tag name, not only `latest`.
- For a list tag the whole structure (list and sub-manifests) is kept as one revision.
- With both settings at 3: pushing a fourth unique tag removes the oldest tag and all of its
  retained revisions.
- Removed revisions go to the trash can if enabled and can be restored by their digest name.
- Open: on the federated repository form the field is either "days after which unused tags
  become eligible for cleanup" (default 0) or the revision count with default 1 that local
  repositories and the description above use; the spec decides.

### PKG-DOCKER-030 · Download statistics shared across tags of one digest
Priority: **P2**

| Setting | Scope | Meaning | Default |
|---|---|---|---|
| Share pull statistics across tags of one digest | System-wide, Docker, OCI and Helm-via-OCI | A pull by tag or by digest updates download count, last download time and last downloader on every tag that points at the same digest | unspecified (opt-in) |

- Background: when several tags share a digest, a pull is attributed to one tag path only; clients
  that pull by cached digest (Kubernetes) leave sibling tags looking unused.
- Only tags the puller may read are updated. The update is asynchronous; there can be a short
  delay. Not retroactive: earlier pulls are not backfilled.
- Prerequisite to change it: administrator or project administrator. UI: administration, package
  format settings, section for Docker, OCI and Helm-via-OCI.
- Download statistics in general: ART-029 in [../artifacts.md](../artifacts.md); cleanup policies
  that use "not downloaded since" depend on this (RET-010).

### PKG-DOCKER-031 · Deleting images and tags
Priority: **P1**

- The Docker client has no delete command. Deletion goes through the system's generic item delete
  (ART-034 in [../artifacts.md](../artifacts.md)): deleting the image folder removes the image with
  all tags, deleting a tag folder removes one tag.
- Layers that no other tag or repository uses are removed with it (PKG-DOCKER-019).
- Folders left empty are removed by a periodic pruning job (every 5 minutes by default).
- For removing a single tag PKG-DOCKER-025 is recommended, which handles sub-manifests
  and shared parts; generic folder deletion of a list tag does not include the platform images
  (PKG-DOCKER-023).
- Whether the registry protocol's `DELETE` endpoints are accepted is unspecified.
- Privilege: delete/overwrite (IAM-022). Deleted items go to the trash can if enabled.

## Promotion

### PKG-DOCKER-032 · Promote an image between repositories
Priority: **P1**

Moves or copies an image (one tag or all tags) from one repository to another without pulling and
pushing, so that the exact image built and tested in CI reaches production.

| Input | Meaning | Default |
|---|---|---|
| Source repository key | Path parameter | required |
| Target repository key | Repository to move or copy to | required |
| Image name | Image to promote | required |
| Target image name | Name in the target repository | same as the image name |
| Tag | Tag to promote | none: all tags of the image are promoted (open: either all tags or `latest`; the spec decides) |
| Target tag | New tag in the target | same tag |
| Copy | Copy instead of move | off (move) |

| Status | Meaning |
|---|---|
| 200 | Promoted (plain text body) |
| 400 | Malformed body or missing required input |
| 401 | Authentication failed |
| 403 | Insufficient permission |
| 404 | Repository or image not found |

- Privilege: a privileged user; the exact permissions on source and target are unspecified.
- Source and target may be local or federated repositories; the operation also promotes Helm charts
  stored in Helm-via-OCI repositories (PKG-HELM-022 in [helm.md](helm.md)).
- Retagging validates the target tag (PKG-DOCKER-026).
- When a promotion overwrites an existing tag in the target, layers shared with other tags of the
  same image must not be deleted (a known defect to guard against).
- Fires the tag-promoted event with target repository and target tag (EVT-006 in
  [../events-and-extensibility.md](../events-and-extensibility.md)).
- A companion CLI command wraps the operation; it requires source and target repositories to
  differ, so renaming an image within one repository is not possible through it. Whether the
  operation itself allows the same repository is unspecified.
- Build-level promotion is a separate feature in [../builds-and-ci.md](../builds-and-ci.md).

## Remote repositories

### PKG-DOCKER-033 · Proxying Docker Hub and other registries
Priority: **P1**

- Upstream URL: Docker Hub's registry endpoint `https://registry-1.docker.io/`, any other registry,
  or a repository on another instance (PKG-DOCKER-038). Upstream URL in general: RMT-004 in
  [../remote-repositories.md](../remote-repositories.md).
- Credentials: user name and password, or a personal access token of the upstream service (for
  example a GitHub token for GitHub's registry); RMT-005.
- Bearer token authentication (RMT-006): sets the upstream `Authorization` header to Bearer instead
  of Basic; to be selected when proxying Docker Hub. Default: unspecified for container remotes
  (RMT-006 gives off).
- Metadata retrieval cache period: 21,600 s for Docker remotes (RMT-018) controls how often a tag is
  re-resolved upstream.
- "Store artifacts locally" applies to Docker remotes (it does not to OCI and Helm-via-OCI remotes,
  RMT-017).
- Docker Hub does not offer remote browsing (RMT-038).
- Settings applied automatically for particular upstreams: URL normalization is disabled
  (RMT-014) for Azure Container Registry (`*.azurecr.io`), Microsoft Container Registry
  (`https://mcr.microsoft.com/`) and the NavOps registry; MIME-type mismatch blocking is enabled
  (RMT-023) for the Chainguard registry (`cgr.dev/chainguard`).
- The cache of a remote is addressable on its own (RMT-003); cached official images appear under
  `library/...` (PKG-DOCKER-035).
- Privileges: read plus deploy/cache to pull content not yet cached (IAM-023).

### PKG-DOCKER-034 · Docker Hub rate limits
Priority: **P1**

- Docker Hub limits pulls ([Docker Hub usage and limits](https://docs.docker.com/docker-hub/usage/pulls/));
  a self-managed system's Docker Hub remote is subject to them like any client.
- Authenticating the remote with Docker Hub credentials (PKG-DOCKER-033) is recommended to avoid
  rate limits and is needed for private images. Figures for Docker Hub's own limits are
  unspecified here; the figure of 100 pulls per six hours applies only to a hosted service's
  shared access (PKG-DOCKER-010). Current figures are Docker's.
- The cache itself reduces upstream pulls; the metadata retrieval cache period bounds how often
  tags are re-checked.
- An upstream `429` maps naturally to the OCI error code `TOOMANYREQUESTS`, and the OCI
  specification asks for a `Retry-After` header on `429`. How the system surfaces an upstream rate
  limit to its own clients is unspecified.
- Unthrottled Docker Hub access through a hosted service's shared remote is out of scope
  (PKG-DOCKER-010).

### PKG-DOCKER-035 · Official-image namespace on Docker Hub
Priority: **P1**

- Docker Hub keeps official images under the `library/` namespace. The Docker client adds that
  prefix only when the registry host is Docker Hub itself: `nginx` becomes
  `docker.io/library/nginx`, while `repo.example.com/docker-remote/nginx` is sent as image `nginx`
  ([reference normalization](https://github.com/distribution/reference/blob/main/normalize.go)).
- Cached official images appear under `library/` (for example `library/nginx`,
  `library/ubuntu`). Whether the system adds `library/` itself when a client asks a Docker Hub
  remote for a single-component name is unspecified.
- Consequence: unless the remote maps single-component names to `library/<name>` towards Docker
  Hub, clients must spell `.../docker-remote/library/nginx`. If both spellings are served, they
  should resolve to one cached copy.

### PKG-DOCKER-036 · Foreign layers
Priority: **P2**

A foreign layer is a layer of an image that lives somewhere other than the registry the image is
pulled from (the manifest points to an external URL).

| Setting | Repositories | Meaning | Default |
|---|---|---|---|
| Cache foreign layers | Docker and Helm-via-OCI remote, advanced tab | The remote downloads foreign layers and caches them | unspecified (off implied) |
| Foreign layer allow list | same | Ant-style patterns (`**`, `*`, `?`) of external URLs foreign layers may be fetched from | `**` (any source) |

### PKG-DOCKER-037 · Project scope for Google registries
Priority: **P2**

| Setting | Meaning | Default |
|---|---|---|
| Project ID | Google Container Registry or Google Artifact Registry project the remote is limited to | blank: the account's default project |

UI: Docker remote, basic tab.

### PKG-DOCKER-038 · Chaining to another instance
Priority: **P2**

A Docker remote whose upstream is a Docker repository on another instance of the system (RMT-040
in [../remote-repositories.md](../remote-repositories.md)).

- Property synchronization (RMT-042) does not happen for pulls by digest: a digest carries no tag,
  so the source manifest is ambiguous when several tags with different properties share it.
- If the remote is also the target of both event-based and scheduled pull replication, bearer
  token authentication must stay off: scheduled replication lists content with a storage operation
  that accepts only Basic authentication and fails with `403` otherwise; event-based replication
  uses the registry's bearer flow. Replication: [../replication-and-federation.md](../replication-and-federation.md).

## Virtual repositories

### PKG-DOCKER-039 · Virtual Docker registry
Priority: **P1**

- One registry address for pulls and pushes over local and remote members. Pushes land in the
  default deployment repository (REPO-038 in [../repositories.md](../repositories.md)).
- Lookups follow the resolution order (REPO-035) unless timestamp resolution is on
  (PKG-DOCKER-040). Priority resolution on Docker requires timestamp resolution (REPO-018).
- Recommended single entry point under the port method (PKG-DOCKER-008) and for team isolation
  (PKG-DOCKER-003).
- A Docker-specific virtual metadata cache period is unspecified (OCI virtual repositories have
  one, PKG-OCI-004 in [oci.md](oci.md)).

### PKG-DOCKER-040 · Resolve tags by latest timestamp
Priority: **P1**

| Setting | Meaning | Default |
|---|---|---|
| Resolve tags by latest timestamp | When the same tag exists in several members, return the most recently deployed one instead of the first in resolution order | off |

- Local members are checked first, by modification time; if no local member has the tag, remote
  members are tried in resolution order.
- Use case: the same image tag built at two sites.
- Placement: REPO-043 in [../repositories.md](../repositories.md).

### PKG-DOCKER-052 · Upstream for a cloud registry's pass-through repository
Priority: **P2**

Some cloud runtimes pull images only from their provider's own registry or from Docker Hub. The
provider registry can hold a pass-through repository whose upstream is a container repository of
the system. The runtimes then pull through the provider registry, while the image stays stored and
governed in the system and no second copy has to be kept in sync. Named case: Google Artifact
Registry
[connector repositories](https://docs.cloud.google.com/artifact-registry/docs/repositories/connector-overview)
in front of Cloud Run and Vertex AI. It covers container images (Docker and OCI) only.

- Upstream address: the system's host plus the repository key as path, `https://<host>/<key>`, the
  repository path method (PKG-DOCKER-006). Typically it points at a virtual container repository
  (PKG-DOCKER-039).
- Credentials: a user name, plus a token or password that the provider keeps in its secret
  manager. Recommended: a least-privileged access token with read-only access to that repository
  (TOK-001 in [../access-tokens.md](../access-tokens.md)), rotated automatically by a
  provider-side helper.
- Recommended mapping is 1:1: one pass-through repository per runtime scope, pointing at a
  dedicated virtual repository per project or environment. This keeps auditing simple and shows
  where requests come from.
- The system needs no extra configuration. It must serve registry API v2 pulls (PKG-DOCKER-016,
  PKG-DOCKER-017) to a non-Docker client authenticating with Basic credentials (PKG-DOCKER-012)
  under the repository path method.

## Browsing and metadata

### PKG-DOCKER-041 · Image tag details
Priority: **P1**

UI location: artifact browser, select a tag, container information tab (UI-011 in
[../ui-inventory.md](../ui-inventory.md)).

| Section | Content |
|---|---|
| Tag summary | Tag name, SHA-256 digest, total image size, number of labels (clicking it shows the labels) |
| Layer history | The commands that built the image, as `docker history` shows them; per layer: layer ID, size, creation time, creating command, digest |
| Labels | Labels of the image |
| List content | For list tags: nested manifests (PKG-DOCKER-023) |

### PKG-DOCKER-042 · Image labels as searchable properties
Priority: **P1**

At push the system extracts the image's labels and sets matching properties on the image's
manifest item, so images can be found by property search (SRCH-007 in
[../search-and-query.md](../search-and-query.md)) and labels serve as extra metadata. The property
naming scheme is unspecified.

### PKG-DOCKER-043 · Storage layout of images
Priority: **P1**

Images are stored as ordinary items in the repository tree, so generic item operations (browse,
copy, move, delete, properties) work on them (names below are descriptive; the exact file names are
an implementation choice):

| Item | Location |
|---|---|
| Image | Folder path equal to the image name, for example `library/nginx/` |
| Tag | Subfolder named after the tag |
| Manifest | File in the tag folder; a manifest list is stored under a distinct file name |
| Layers and config | Files in the tag folder named after their digest (content stored once, PKG-DOCKER-019) |
| Pull by digest | Folder named after the digest |
| Retained revisions | Copies named after their digest (PKG-DOCKER-029) |

- Build metadata refers to the manifest and layer items (PKG-DOCKER-047); properties live on the
  manifest item (PKG-DOCKER-042).

## Signing

### PKG-DOCKER-044 · Docker Content Trust
Priority: **P2**

- The system hosts images signed with Docker Content Trust (Notary v1) without any configuration;
  signatures live on a separate Notary server, not in the system.
- Client: `export DOCKER_CONTENT_TRUST=1` and `export DOCKER_CONTENT_TRUST_SERVER=<notary URL>`;
  on push the client asks for the root key and repository key passphrases and has the image signed
  before the tag is assigned.
- Signatures and attestations stored in the registry itself (cosign, OCI 1.1 referrers):
  PKG-OCI-009 in [oci.md](oci.md).

## Clients and tooling

### PKG-DOCKER-045 · Supported container clients
Priority: **P1**

| Client | Operations against the system |
|---|---|
| Docker CLI | login, tag, push, pull, manifest create and push |
| Docker Buildx | multi-platform build and push |
| Podman | login, tag, push (`-f=oci` pushes in OCI format), pull |
| BuildKit, ORAS, wasm-to-oci, Helm | see [oci.md](oci.md) and [helm.md](helm.md) |
| Kaniko, OpenShift builds | push from in-cluster builds; build metadata through the companion CLI (PKG-DOCKER-047) |
| Kubernetes | pulls with image pull secrets (PKG-DOCKER-015) |

- Self-signed certificates: either list the registry as insecure in the Docker daemon configuration
  (and restart the daemon) or import the CA certificate into the Docker trust store. Otherwise the
  ping fails with a certificate validation error.
- Clients need DNS entries (or hosts-file entries) for every host name the access method uses.

### PKG-DOCKER-046 · Client setup instructions
Priority: **P1**

The client setup dialog (UI-021 and UI-026 in [../ui-inventory.md](../ui-inventory.md)) offers the
Docker CLI and Podman as clients of a Docker repository, with snippets for login, manual
credential configuration (PKG-DOCKER-011), tagging, single- and multi-platform push, and pull,
filled with the user's host, repository and token. The registry address in the snippets must follow
the active access method (PKG-DOCKER-005).

### PKG-DOCKER-047 · Build metadata for images through a companion CLI
Priority: **P2**

A companion command-line tool wraps the container client so that pushes and pulls are recorded in
build metadata ([../builds-and-ci.md](../builds-and-ci.md)):

- Build, push and pull through the wrapper record the image's layers as artifacts or dependencies
  of a named, numbered build; build name and number must be given together; publishing the build
  record is a separate step, and until then the build is not visible.
- The wrapper logs the container client in before build, push and pull, using the host of its
  configured server. Under the subdomain method that host differs from the image's registry host,
  so the wrapper offers to skip its own login (the user logs in to the right host first). A
  configuration with a token but no user name cannot log in (PKG-DOCKER-011).
- Images pushed by other tools (Kaniko, Buildx, OpenShift) are added afterwards from a reference of
  the form `<image>:<tag>@sha256:<manifest digest>`; several tags sharing one digest are given
  comma-separated. An OpenShift build wrapper reads tag and digest from the cluster itself.
- Podman wrappers require image references with three components: registry, repository path,
  image.
- Multi-platform images: each platform push is recorded separately.
- Push options include parallel transfer (default 3 threads) and digest validation during push
  (default off). The wrapper can drive Podman instead of Docker for push and pull.
- A promote command wraps PKG-DOCKER-032.

### PKG-DOCKER-048 · Local image scanning through the companion CLI
Priority: **Later**

The companion CLI can scan a locally built image against the scanning engine's policies without
logging in to the registry and without pushing; output formats include table, JSON, SARIF and
CycloneDX, with severity and fixability filters and a non-zero exit code when a blocking rule
matches. Scanning is catalogued in
[../security-scanning-and-curation.md](../security-scanning-and-curation.md).

## Legacy registry API v1

### PKG-DOCKER-049 · Docker registry API v1 repositories
Priority: **Later**

- A local repository can be created with API version v1 (PKG-DOCKER-004). v1 needs a reverse proxy
  (the client constraints of PKG-DOCKER-005 apply); the system's container API path prefix must be
  part of the proxied URL. v2 is recommended for any Docker client from version 1.6.
- Storage: one folder of flat images and one folder of repositories with their tags. Images carry
  properties for image ID and size; tags carry properties for tag name and the image ID they point
  to; a property with the image name (for example `library/ubuntu`) supports search.
- UI: an ancestry tab shows the image tree (like the old `docker images --tree`), an information tab
  the image details.
- Deletion through the v1 repository and tag delete endpoints; orphan layers are cleaned up and
  empty folders pruned as for v2.
- Credentials are configured manually in the client configuration file.
- Not offered in a hosted deployment.

### PKG-DOCKER-050 · Migrate a v1 repository to v2
Priority: **Later**

| Input | Meaning | Default |
|---|---|---|
| Source repository key | v1 repository, path parameter | required |
| Target repository key | Existing v2 repository | required |
| Image name | Image to migrate | empty: the whole repository |
| Tag | Tag to migrate | empty: all tags |

## Deployment profiles

### PKG-DOCKER-051 · Container-only deployment profile
Priority: **Out of scope**

A reduced, self-managed deployment profile limited to Docker, OCI, Helm, Helm-via-OCI and generic
repositories, without high availability, replication, federation, plugins or scanning. Such a
reduced packaging has no meaning for trove.

## Notes for implementers

- The registry API must answer at `/v2/` on the root of a host, whatever path layout the rest of
  the system uses: Docker clients do not accept a context path. Under the repository path method
  the repository key is the first component of the image name, so `/v2/<key>/<image...>/...`
  must be routed before any other path matching.
- Repository keys of container formats are DNS labels in practice: lower case, no underscore, at
  most 63 characters, shorter when the subdomain method appends the server name.
- Docker clients send Basic credentials only over HTTPS (or to insecure-listed registries) and
  always with a user name, even for tokens. Accept `user:token` Basic authentication.
- Store manifests byte for byte and serve them with the pushed `Content-Type`; compute
  `Docker-Content-Digest` over the stored bytes. `HEAD` on a manifest must return the digest and
  `Content-Length`, so clients can resolve a tag without downloading the manifest.
- Kubernetes often pulls by a digest it resolved earlier, and build-metadata tooling references
  images by digest. Permission checks, statistics (PKG-DOCKER-030) and property synchronization
  (PKG-DOCKER-038) must cope with requests that carry no tag.
- Decide whether re-pushing an unchanged manifest under an existing tag counts as an overwrite
  (PKG-DOCKER-027); CI pipelines re-push routinely.
- Track list-to-sub-manifest relationships at upload; tag eviction, retention, deletion and
  promotion depend on them and must never delete sub-manifests or layers still used by another tag.
- A Docker Hub remote needs the `library/` mapping for single-component names (PKG-DOCKER-035) and
  must handle Docker Hub's rate limiting gracefully (PKG-DOCKER-034).
- Ingress and proxy in front of the registry: no request-body limit, chunked transfer encoding on,
  long read timeouts, and the `Host` header forwarded with its port.
- The tag-list filter hides digest-named revision copies; if revisions are stored as tags, the
  listing must exclude them or clients will see them as tags.
