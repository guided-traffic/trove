{{if eq .Class "virtual"}}A push through a virtual repository is executed on its deployment target{{if .PublishTarget}}, `{{.PublishTarget}}`{{end}}; without a target it answers `405` `UNSUPPORTED`. The snippets name the repository you chose, and trove forwards the push.{{else}}Tag the image with the reference of the snippet and push it. The client uploads each layer, then the manifest; trove stores the manifest exactly as sent.{{end}}

{{if eq .Client "docker"}}### Multi-platform images

`docker buildx build --push` with several `--platform` values pushes one manifest per platform and then the index that lists them. `docker manifest create` and `docker manifest push` build a Docker manifest list from images that are pushed already.
{{else}}### Multi-platform images

`podman manifest create`, `podman build --platform linux/amd64,linux/arm64 --manifest <name> .` and `podman manifest push --all <name> <reference>` push an OCI image index together with the manifests it lists; without `--all` only the index itself is sent, and trove refuses it with `MANIFEST_BLOB_UNKNOWN` because the manifests it lists are not stored.
{{end}}
### Tags

The first push of a tag needs the permission to deploy; pushing the same image under a tag that points at it already changes nothing. Pointing an existing tag at another image also needs the permission to overwrite and answers `403` `DENIED` without it. `?tag=` on a manifest push (several tags in one request) is not supported; clients push one tag per request.

### Limits

A manifest is at most 4 MiB. A manifest in the format of Docker schema 1 is refused with `400` `MANIFEST_INVALID`. A manifest that names a layer or config the image name does not hold answers `400` `MANIFEST_BLOB_UNKNOWN`: push the blobs first, to the same image name. A layer that was uploaded and is never referenced by a manifest is removed after the grace period (24 hours by default) by a periodic job.

### Pushing other OCI content

Artifacts of any type (signatures, SBOMs, Helm charts) are pushed with the tools that produce them. A manifest with a `subject` becomes a referrer of that subject and is listed by the referrers API.
