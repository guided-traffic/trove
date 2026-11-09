# Container images with Docker and Podman

How to sign the Docker CLI and Podman in to trove with a token, push and pull images and multi-platform images, pull through a virtual repository, pin an image by its digest, delete tags and manifests, and let a Kubernetes cluster pull with an image pull secret. The Client setup page and the How to use tab give the same commands with the addresses of your installation.

## Image references

trove serves container images and other OCI content (signatures, SBOMs, Helm charts pushed as OCI artifacts) from repositories of the format **OCI (Docker)**. An image reference names the trove host, the project, the repository and the image:

| Part | Example | Rule |
|---|---|---|
| Host | `trove.example.com` | the address of the installation, without `https://` |
| Project and repository | `web/docker` | the keys of the project and the repository |
| Image name | `team/app` | lower-case letters and digits, separated by `.`, `_`, `__` or runs of `-`, with `/` between components |
| Tag or digest | `:1.0` or `@sha256:<64 hex digits>` | a tag is letters, digits, `_`, `.` and `-`, at most 128 characters, not starting with `.` or `-` |

So `trove.example.com/web/docker/team/app:1.0` is the tag `1.0` of the image `team/app` in the repository `docker` of the project `web`. The project, the repository and the image name together are at most 255 characters. The address has no `/v2/`: that is the path of the protocol, which the client adds itself.

On a host that an administrator bound to one repository, the reference is shorter, `<host>/<name>:<tag>`; see [Container image repositories](../admin/container-repositories.md#the-docker-engine-mirror-on-a-bound-host).

## Sign in

The clients sign in with a trove token, never with your password: a [personal access token](access-tokens.md), a service-account token, or in GitLab CI the token of the [token exchange](gitlab-ci-pipelines.md). The user name is recorded and ignored; the snippets use `trove`. Put the token into `TROVE_TOKEN` without typing it on a command line your shell keeps, and pass it on standard input:

```shell
read -rs TROVE_TOKEN && export TROVE_TOKEN
printf '%s' "${TROVE_TOKEN}" | docker login trove.example.com/web/docker --username trove --password-stdin   # example
printf '%s' "${TROVE_TOKEN}" | podman login trove.example.com/web/docker --username trove --password-stdin   # example
```

What happens: the client asks `https://trove.example.com/v2/`, is told to fetch a token at `https://trove.example.com/v2/_token`, sends your trove token there, and receives a registry token that starts with `trv_rgt_`, lasts five minutes unless an administrator configured another lifetime, and names the images and actions it may use. The client fetches a new one on its own when it expires or when it needs another action. A registry token never holds more than your trove token: every request is decided again against your permissions, and revoking the trove token ends its registry tokens too.

- **Docker** keeps the credential per host: one `docker login` covers every repository of every project on this host, and a second login to the same host replaces the first. Keep one token that covers what you pull and push.
- **Podman** keeps the credential in its `auth.json` and falls back to the file of the Docker CLI.
- **Anonymous pulls.** A repository that an administrator opened to anonymous readers needs no login to pull. Pushing, moving a tag and deleting always need a token.

Which permission each operation needs:

| Operation | Permission on the repository |
|---|---|
| Pull, list tags, read referrers | read |
| Push a new tag, push by digest, upload a layer | deploy |
| Point an existing tag at another image | deploy and overwrite |
| Delete a tag, a manifest or a layer | delete |

The first push of an image name records its owners; after that only the owners, holders of the manage permission and administrators push to, move tags of or delete that name, unless the repository turned package ownership off.

### Your own certificate authority

When trove is served with a certificate of your own authority, the client that talks to the registry must trust it: for Docker that is the daemon, with the CA certificate at `/etc/docker/certs.d/<host>/ca.crt` on the machine that runs it; for Podman `/etc/containers/certs.d/<host>/ca.crt` or `~/.config/containers/certs.d/<host>/ca.crt`. Without it every command ends in `x509: certificate signed by unknown authority`.

## Push an image

Tag a local image with the trove reference and push it. Push to a local repository, or to a virtual repository that has a deployment target:

```shell
docker tag myapp:1.0 trove.example.com/web/docker-local/myapp:1.0   # example
docker push trove.example.com/web/docker-local/myapp:1.0
```

The client uploads each layer, then the manifest; trove stores the manifest exactly as sent, so the digest the client prints is the digest it verifies later. A layer the repository already holds for the same image name is not uploaded again. trove accepts Docker image manifests (schema 2), Docker manifest lists, OCI image manifests and OCI image indexes, each of at most 4 MiB; the old Docker schema 1 format is refused with `MANIFEST_INVALID`. An upload that a client splits over several requests can be continued by any replica of trove, because no replica keeps anything of it between requests.

- **Tags.** Pushing a tag that does not exist, or one that already points at the same image, needs deploy. Pointing an existing tag at another image needs overwrite as well; without it the push answers `403 DENIED`. The image the tag pointed at before stays in the repository and can still be pulled by its digest.
- **One tag per request.** Several tags in one manifest push (`?tag=`) are not supported; clients push one tag per request.
- **Other OCI content.** Artifacts of any type are pushed with the tools that produce them. A manifest with a `subject` becomes a referrer of that subject, listed by `GET /v2/<project>/<repository>/<name>/referrers/<digest>`.

## Multi-platform images

An image index lists one manifest per platform. With Docker Buildx, build and push all platforms in one step:

```shell
docker buildx build --platform linux/amd64,linux/arm64 --tag trove.example.com/web/docker-local/myapp:1.0 --push .   # example
```

`docker manifest create` and `docker manifest push` build a Docker manifest list from images that are pushed already. With Podman, build into a manifest list and push it with `--all`:

```shell
podman build --platform linux/amd64,linux/arm64 --manifest myapp .                     # example
podman manifest push --all myapp trove.example.com/web/docker-local/myapp:1.0          # example
```

Without `--all` Podman sends only the index, and trove refuses it with `MANIFEST_BLOB_UNKNOWN`, because the platform manifests it lists are not stored. A client pulls the platform that matches its machine; `docker pull --platform linux/arm64 <reference>` or `podman pull --platform linux/arm64 <reference>` pulls another one, and `podman manifest inspect <reference>` lists the platforms of an index. The package page shows them in its **Platforms** table.

## Pull an image

```shell
docker pull trove.example.com/web/docker/myapp:1.0                      # example: by tag
docker pull trove.example.com/web/docker/myapp@sha256:<64 hex digits>   # example: by digest
```

A tag can move; a digest cannot. trove serves the manifest of a digest for as long as it is stored, whether a tag still points at it or not, so a deployment that names the digest keeps starting when somebody moves the tag.

### Through a remote repository

A remote repository serves the images of an upstream registry such as Docker Hub or GitHub Container Registry. The image name is the name at the upstream; for a remote of Docker Hub, `nginx` and `library/nginx` are the same image. trove fetches what you pull the first time and serves it from its store afterwards; a tag is checked with the upstream again after five minutes, unless an administrator set another period, without counting as a pull when it has not moved. Nothing can be pushed to a remote repository (`405 UNSUPPORTED`).

### Through a virtual repository

A virtual repository gives one address for several repositories, for example your local repository and a remote of Docker Hub: `trove.example.com/web/docker/myapp:1.0` for your image and `trove.example.com/web/docker/alpine:3.20` for Docker Hub's, with one login.

- A **tag** comes from the first member that has it: the local members first, in their order, then the remote members in their order. A **digest** comes from wherever the identical bytes are.
- The **tags list** is the union of the members' tags, and the **referrers** of a digest are the union of the members' referrers.
- The answer header `Trove-Resolved-From` names the member that answered.
- You need read on the members you pull from; a member you may not read is left out without notice. A name an administrator claimed for a local member is never taken from another member.
- A **push** goes to the deployment target of the virtual repository. A layer a remote member has cached is linked into the target instead of being uploaded again. A virtual repository without a deployment target takes no push (`403 DENIED`), and nothing is deleted through a virtual repository (`405 UNSUPPORTED`).

## Kubernetes

A cluster pulls with an **image pull secret** whose password is a trove token, best a service-account token that may read the repository and nothing else. The Client setup page gives the command for the Docker CLI client; it builds the secret from the environment variable, so the token appears on no command line:

```shell
printf '{"auths":{"%s":{"username":"trove","password":"%s"}}}' trove.example.com/web/docker "${TROVE_TOKEN}" | \
  kubectl create secret generic trove-registry --type=kubernetes.io/dockerconfigjson --from-file=.dockerconfigjson=/dev/stdin   # example
```

Name the secret under `imagePullSecrets` of the pod or of its service account. The server of the secret is the image prefix `<host>/<project>/<repository>`, so the kubelet sends the credential only for the images below it. A workload pinned to a digest keeps starting after a tag has moved. When the service-account token is rotated, create the secret again.

## Delete tags and images

The Docker CLI has no command for it; use a tool that speaks the registry API (`skopeo delete`, `crane delete`, `curl -X DELETE`) with the delete permission and the ownership of the image name:

| Request | Effect |
|---|---|
| `DELETE /v2/<project>/<repository>/<name>/manifests/<tag>` | Removes the tag; the manifest stays and can still be pulled by its digest |
| `DELETE …/manifests/sha256:<hex>` | Removes the manifest, every tag of the image name that points at it, its referrers and, for an index, the platform manifests that no other manifest and no tag names |
| `DELETE …/blobs/sha256:<hex>` | Removes a layer or configuration that no manifest of the image name references |

All three answer `202`. A manifest that an index of the image name lists, and a layer a manifest still references, are refused with `409 DENIED`: delete the index or the manifest first. A layer that no manifest references any more is removed by an hourly job once it is older than the grace period (24 hours unless an administrator set another), and an image that was untagged by a moved tag stays until somebody deletes it. A remote repository is a cache, so its images are not deleted through the registry API (`405 UNSUPPORTED`). After a restore of trove from a backup, an image that was deleted after the backup answers `404` until an administrator reinstates it.

## When something goes wrong

The registry answers in the error format of the distribution specification, `{"errors":[{"code":"…","message":"…"}]}`; most clients print the code.

| Answer | Cause and remedy |
|---|---|
| `x509: certificate signed by unknown authority` | The client does not trust the certificate of the trove host; install the CA certificate as described above |
| `401 UNAUTHORIZED` | No credential, or an expired or revoked token. Log in again with a valid token. A `401` with `insufficient_scope` makes the client fetch a new registry token on its own |
| `403 DENIED` | The token is valid and may not do this: no deploy, no overwrite for a moved tag, no delete, the image name is owned by somebody else, or a push to a virtual repository without a deployment target |
| `404 NAME_UNKNOWN` | The project or repository does not exist, you may not see it, or the repository holds nothing of the image name; the answer is the same for all three on purpose |
| `404 MANIFEST_UNKNOWN`, `404 BLOB_UNKNOWN` | No manifest of this tag or digest is stored (or it was withheld after a restore); no layer of this digest under this image name |
| `400 MANIFEST_INVALID`, `400 MANIFEST_BLOB_UNKNOWN` | The manifest is no valid document of a supported type (Docker schema 1 included); the manifest names a layer, configuration or manifest the image name does not hold, so push those first |
| `413 SIZE_INVALID` | A manifest above 4 MiB, or a layer above the repository's limit |
| `405 UNSUPPORTED` | A write to a remote repository, or a delete through a virtual repository |
| `429`, `503 TOOMANYREQUESTS` | Rate limited by trove or by the upstream of a remote repository; wait for `Retry-After` |
| `502`, `503`, `504` | The upstream of a remote repository failed or is unreachable; images trove has stored are still served |

Give the `X-Request-Id` of the answer to an administrator when you need help ([Errors and troubleshooting](troubleshooting.md#give-the-request-id-to-an-administrator)).

## What is not available

- An endpoint with exposure rules (a customer endpoint) does not serve container images; use an endpoint that exposes everything.
- trove does not scan the content of images for vulnerabilities.
- `docker search` has no counterpart: the catalog lists image names (`GET /v2/_catalog`), but there is no search.
- The `sha256-<digest>` fallback tags of the older referrers scheme are not maintained for local repositories; use the referrers API.
