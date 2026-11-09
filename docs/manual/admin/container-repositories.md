# Container image repositories

This page explains how administrators set up repositories of the format OCI (Docker): local repositories for the images teams push, remote repositories for Docker Hub, GitHub Container Registry and other registries, virtual repositories over both, the mirror mode with its choice of tags and platforms, the Docker Engine mirror on a bound host, the configuration keys of the registry, and what this version does not support yet.

## What the format serves

The format **OCI (Docker)** serves Docker images and every other kind of OCI content (signatures, SBOMs, Helm charts pushed as OCI artifacts) with one protocol, the OCI distribution API, which the Docker CLI, Buildx, Podman, containerd and Kubernetes speak. Every class of repository is available:

| Class | What it does | Image reference of a client |
|---|---|---|
| Local | Holds what is pushed to it | `trove.example.com/<projectKey>/<key>/<name>:<tag>` |
| Remote | Caches an upstream registry, or mirrors a selection of it | `trove.example.com/<projectKey>/<key>/<upstream name>:<tag>` |
| Virtual | One address over local and remote members | `trove.example.com/<projectKey>/<key>/<name>:<tag>` |

The registry is the root of the host: the protocol lives at `https://trove.example.com/v2/<projectKey>/<key>/…`, and the token service, the version check and the catalog at `/v2/_token`, `/v2/` and `/v2/_catalog` belong to the installation, not to a repository. Repository and project keys follow the grammar of image name components, for every format, so every repository key can appear in an image reference. How users sign in, push and pull is in [Container images with Docker and Podman](../user/container-images.md).

The route family of the registry is `packages`: an endpoint whose surface holds `packages` serves `/v2/`, and the chart routes `/v2` of every host to the `packages` pods. Docker, Podman and containerd sign in through the token service with a trove token as the password of Basic authentication, so `auth.clients.basic.enabled` must stay on (it is on by default).

## Create a repository

Create it like any other ([Managing repositories](repositories.md#create-a-repository)): choose the tile **OCI (Docker)**, then the class. The key becomes part of every image reference, so choose one that reads well there, such as `docker-local`, `docker-hub` or `docker`. Or through the API:

```bash
# example values: the project, the keys and the token variable
curl -X POST https://trove.example.com/api/v1/projects/web/repositories \
  -H "Authorization: Bearer $TROVE_TOKEN" -H 'Content-Type: application/json' \
  --data '{"key": "docker-local", "class": "local", "format": "oci"}'
```

A local or virtual OCI repository has no settings of the format. What a local repository decides:

- **Package ownership** (`local.packageOwnership`) is `enforce` by default: the first push of an image name records its owners, and from then on only they, holders of `manage` and administrators push to it, move its tags or delete it.
- **Automatic claims** (`local.autoClaim`) are on by default: the first push of an image name claims it, so a virtual repository that contains the local repository never takes an image of that name from a remote member.
- **Include and exclude patterns** match image names, such as `team/*`.
- A **raw upload** through `/files/` or **Browse** is refused (`409`): manifests and tags are written only through the registry protocol. A raw delete runs the same rules as a `DELETE` of the protocol.

## A remote repository for Docker Hub

The wizard starts a remote OCI repository with the upstream `https://registry-1.docker.io/`, Docker Hub's registry; an empty **Upstream URL** means it too. For that upstream:

- **The token host.** Docker Hub hands out its tokens at `auth.docker.io`. trove adds that host to the repository's extra hosts with **send credentials** on, so the credential reaches Docker Hub's token service and nothing else; the token it receives goes to the registry only. Blob downloads that Docker Hub redirects to its CDN are followed without a credential.
- **Official images.** A one-component name is an official image: `nginx` and `library/nginx` are the same image and one stored copy. No other upstream gets this mapping.
- **Authenticate it.** Docker Hub limits the pulls of an address, and anonymous callers have the smallest allowance; every client behind trove shares the one address trove pulls from. Set **Authentication** to **Username and password** with a Docker Hub user and an access token of that user whenever more than a few pulls per hour go through the repository, and always for a mirror. A manifest `GET` counts as a pull at Docker Hub; trove asks a stale tag with a `HEAD` first and downloads the manifest again only when the tag moved, so an unchanged tag costs nothing of the allowance.
- **When Docker Hub refuses.** An exhausted allowance answers clients `503 TOOMANYREQUESTS` with `Retry-After` while trove has no stored copy; a stored copy is served.

The cache periods of an OCI remote are 300 seconds for metadata (how long a tag is served before it is asked again) and 300 seconds for a remembered "not found". **Test connection** requests `/v2/` of the registry. A registry answers that with its token challenge, which counts as reachable; whether the credentials work shows at the first pull, because the test runs no token request.

Every manifest and layer is stored only when its SHA-256 matches the digest it was asked by, or the digest the registry announced for a tag; a mismatch answers `502` and stores nothing. A manifest in the old Docker schema 1 format is refused (`502`). A remote takes no push, mount or delete through the protocol (`405 UNSUPPORTED`).

## A remote repository for GitHub Container Registry and other registries

Set the **Upstream URL** to the registry's origin, optionally with a path that every image name is requested below:

| Registry | Upstream URL | Credentials |
|---|---|---|
| GitHub Container Registry | `https://ghcr.io/`, or `https://ghcr.io/acme` to serve `app` from `ghcr.io/acme/app` | none for public images; for private ones **Username and password** with a GitHub user and a token that may read packages |
| Another registry, another trove | `https://registry.example.com/`, or with a namespace path | the registry's own; **Bearer token** sends a static token and runs no token handshake |

GitHub Container Registry hands out its tokens on its own host, so it needs no extra host. A registry whose token service lives on another host names that host in its challenge; trove asks it only when the host is one of the repository's extra hosts, and sends the credential only to an extra host with **send credentials** on. Extra hosts are set through the API (`remote.extraHosts`, [Settings that are set through the API](repositories.md#settings-that-are-set-through-the-api)); with a token service on a host that is not listed, pulls through the repository fail.

The tags list of a remote is the stored list of the image name, with the pages beyond what was stored asked from the registry; the referrers of a digest are asked from the registry and kept for the metadata period. When the registry cannot be asked, the stored part answers and referrers answer `404`.

## A virtual repository

A virtual OCI repository combines local and remote OCI repositories of its own project. The wizard creates it without members; set the members, in resolution order, and the deployment target through the API:

```bash
# example values: the project, the key, the ETag and the token variable
curl -X PATCH https://trove.example.com/api/v1/projects/web/repositories/docker \
  -H "Authorization: Bearer $TROVE_TOKEN" \
  -H 'Content-Type: application/merge-patch+json' \
  -H 'If-Match: "3"' \
  --data '{"virtual": {"members": ["docker-local", "docker-hub"], "deployTarget": "docker-local"}}'
```

- **Pulls.** A tag comes from the first local member that has it, then from the remote members in their order; a digest from the local members, then from what the remote members have stored, then from their upstreams. The answer names the member in `Trove-Resolved-From`. The tags list and the referrers are the unions of the members'. The catalog lists no names for a virtual repository; the members appear under their own names.
- **Pushes, uploads and mounts** go to the deployment target. A layer that a remote member has cached is linked into the target when an image built on it is pushed, so the client does not upload it again. Without a deployment target the repository takes no push (`403 DENIED`, and a registry token for it carries no `push`).
- **Deletes** are not routed through a virtual repository (`405 UNSUPPORTED`); delete in the member that holds the image.
- **Dependency confusion.** Claims, pin rules and the shadowing check work for image names as for npm package names ([Protection against dependency confusion](repositories.md#protection-against-dependency-confusion)). Claim the names of your own images on the local member (`PUT …/claims` with patterns such as `acme/*`), so that a public image of the same name is never served instead. The first push of an image name that a remote member already serves needs the manage permission (`403` otherwise).

## The mirror mode

A remote OCI repository in mode **Mirror** copies, in background passes, the images its settings select, so they stay pullable when the registry is slow, rate limited or gone. Set the mode in the **Mirror** section of the wizard or on the **Mirror** tab ([Mirror](repositories.md#mirror)); the selection is the format's own setting `oci.mirror`, in the format settings of the remote (**OCI (Docker) settings**, one entry per line):

| Setting | Document key | Default | Meaning |
|---|---|---|---|
| **tags** | `oci.mirror.tags` | `latest` | Tag patterns with `*` and `?` a tag must match to be copied, 1 to 64; a pattern without a wildcard names a tag that is always kept |
| **excludeTags** | `oci.mirror.excludeTags` | none | Tag patterns that are never copied, at most 64 |
| **platforms** | `oci.mirror.platforms` | every platform | `os/arch` or `os/arch/variant` of the manifests of an index to copy, at most 32; `linux/arm64` includes `linux/arm64/v8`, and an entry without a variant counts as the default variant of its architecture |

Which names a pass mirrors:

- **Discovery.** A registry that offers its catalog (`/v2/_catalog`) is listed (**Listing**) and its names are checked against the include patterns. Docker Hub and GitHub Container Registry offer no catalog, so **Automatic** uses the **Names list**: the include patterns without a wildcard, written as clients type them (`nginx`, `bitnami/redis`). There is no change feed for OCI.
- **Tags.** The tags `tags` matches and `excludeTags` does not. **Versions per package** (`remote.mirror.maxVersionsPerPackage`) keeps that many of the wildcard matches, newest first; a tag an exact pattern names is kept besides them. A tag the bound drops is not even read.
- **What is copied of a tag.** The manifest the tag points at; for an index the manifests of the selected platforms, with the attestation manifests of those platforms; then their configurations and layers. Foreign layers (with `urls`) and referrers are not copied; referrers stay pull-through.
- **A bound for Docker Hub.** A mirror of `https://registry-1.docker.io/` is refused unless every include pattern starts with a literal part (exact names do) or **Storage limit in bytes** is set; a mirror of any other registry without a bound is saved with a warning.
- **Cost.** A first pass of n kept tags costs about n manifest requests plus one per kept platform manifest; a later pass costs one `HEAD` per kept tag. Give a Docker Hub mirror an account of its own, so that it does not use up the allowance other people of the same account or address need.

Only a holder of `repositories:write-config` sets the mode and changes `oci.mirror`; an Owner of the project without it may pause the mirror or turn it back to pull-through (`422 mirror_restricted` otherwise). The status of a pass, **Run a pass now** and the pass jobs are those of every mirror ([The status of a mirror](repositories.md#the-status-of-a-mirror)).

## The Docker Engine mirror on a bound host

A Docker Engine pulls Docker Hub images through a mirror listed in `registry-mirrors` of its `daemon.json`. A mirror is addressed at the root of a host (`https://<host>/v2/library/nginx/…`), without a project and repository key, so trove serves it on a host whose root is bound to one repository:

1. Choose a host name, such as `mirror.example.com`, and give it a certificate the Docker Engines trust.
2. Bind it on an endpoint: the host goes into the endpoint's `hosts`, and `host_roots` maps it to the repository, typically a remote of Docker Hub or a virtual repository over one ([Endpoints and exposure rules](endpoints.md)). In the chart's values:

   ```yaml
   endpoints:
     default:
       hosts: [trove.example.com, mirror.example.com]   # example
       hostRoots: {mirror.example.com: web/docker-hub}  # example
   ```

3. Point the engines at it:

   ```json
   { "registry-mirrors": ["https://mirror.example.com"] }
   ```

On the bound host, `/v2/<name>/…` is the registry of that one repository with the image name as sent, the token service and the version check are the host's own, and token scopes name the image without a prefix; the catalog lists the names of the bound repository only. A binding to a repository that does not exist, or that is not an OCI repository, is no binding: the host then serves `/v2/<projectKey>/<repoKey>/…` as any other. The engine uses the mirror for pulls of Docker Hub images only; a push goes to the registry the reference names.

Not verified in this version: whether a Docker Engine presents the credential of a `docker login` to a mirror (the end-to-end row of the Docker Engine mirror reads anonymously). A bound repository that anonymous readers may read, on an endpoint that admits anonymous requests, needs no credential.

## Configuration

| Key | Default | Meaning |
|---|---|---|
| `oci.token_lifetime` | `300s` | Lifetime of a registry token, 60 s to 3600 s, never beyond the expiry of the trove token it was issued for. A longer lifetime widens the time a leaked registry token stays usable |
| `oci.unreferenced_blob_grace` | `24h` | Age a layer of an image name needs before the hourly job `oci-sweep` removes it for being referenced by no manifest; must exceed `storage.uploads.session_max_age` |
| `maintenance.tasks.oci-sweep` | `{enabled: true, schedule: "0 * * * *"}` | When the sweep runs |
| `storage.uploads.part_size` | `16MiB` | Part of a blob upload that is staged in the object store, 5 MiB to 5 GiB; clients learn it from `OCI-Chunk-Min-Length` |
| `storage.uploads.writer_lease`, `session_ttl`, `session_max_age` | `60s`, `4h`, `20h` | How long a request may hold an upload, how long an idle upload lives, and the age at which any upload ends |
| `storage.uploads.max_open_sessions` | `100` | Open uploads per principal and repository; one more answers `429` |

The complete reference is in the [full reference](https://github.com/guided-traffic/trove/blob/main/README.md#-full-reference). What the registry records: `trove_oci_registry_tokens_total{outcome}` counts the answers of the token service, `trove_oci_sweep_blobs_removed_total` the layers the sweep removed, `trove_storage_upload_sessions_total{outcome}` the uploads by how they ended; the audit log has `auth.registry_token.denied` for a refused token request, `oci.manifest.deleted` for a deleted manifest and `oci.reinstate` for a manifest an administrator put back.

## After a restore

A manifest that was deleted after the restore point may have been deleted on purpose, so a restore's replay of the revocation journal withholds it: it answers `404` on every route, and its push answers `409 DENIED`, until an administrator decides. To put it back, run `trove admin oci reinstate <projectKey>/<repoKey> <name>@sha256:<hex>`; to confirm the deletion, delete it again. The replay output lists every withheld manifest.

## Not supported in this version

- **Endpoints with exposure rules** do not serve container images: the format has no exposure adapter yet. Use an endpoint with `exposure: all` for registry clients.
- **Blob downloads are not redirected** to the object store; every layer is streamed by a replica.
- **No retention of untagged manifests.** A manifest a moved tag left behind stays until somebody deletes it.
- **Mirrors** copy no referrers and do not read an index inside an index; a tag whose manifest is refused (Docker schema 1) is skipped without a record in the status.
- **No vulnerability scanning** of images.
- **Several tags in one push** (`?tag=`) answer `400 UNSUPPORTED`; clients push one tag per request.
- **Deletes through a virtual repository** answer `405`.
- The metric `trove_oci_upstream_token_requests_total` of the handshakes of remote repositories is not counted yet.

[Known limitations](limitations.md) lists the limits of the other areas.
