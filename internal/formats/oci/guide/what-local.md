A local repository holds the images and other OCI content your teams push{{if .Repository}} to `{{.Repository}}`{{end}}. One repository serves Docker images and OCI artifacts alike: trove stores every manifest exactly as it was pushed and answers a pull with the same bytes, so the digest your client prints is the digest it verifies later. The repository speaks the OCI distribution API, which is a superset of the Docker registry API, so the Docker CLI, Buildx, Podman and Kubernetes all use it the same way.

### Names and references

An image reference is `<host>/<project>/<repository>/<name>[:<tag>|@<digest>]`. {{if .ImagePrefix}}For this repository it starts with `{{.ImagePrefix}}/`.{{else}}For a repository it starts with the host, the project key and the repository key.{{end}} The image name is lower case: letters and digits separated by `.`, `_`, `__` or runs of `-`, with `/` between components (`team/app`). The whole name a client sends, including the project and the repository key, is at most 255 bytes. A tag is letters, digits, `_`, `.` and `-`, at most 128 characters, and does not start with `.` or `-`.

### Tags move, digests do not

A tag points at one manifest. Pushing a tag that does not exist, or one that already points at the same manifest, needs the permission to deploy. Moving a tag to another manifest also needs the permission to overwrite; without it the push answers `403` `DENIED`. A manifest is identified by its SHA-256 digest and can always be pulled by it, whether a tag still points at it or not: a tag you move away leaves its old manifest in the repository until somebody deletes it, so a workload that pulls by digest keeps working.

### What trove checks in a push

A manifest is at most 4 MiB. trove accepts Docker image manifests (schema 2), Docker manifest lists, OCI image manifests and OCI image indexes; the old Docker schema 1 format is refused with `400` `MANIFEST_INVALID`. Every layer and config a manifest names must have been uploaded to the same image name first, or the push answers `400` `MANIFEST_BLOB_UNKNOWN`; a layer that carries `urls` (a foreign layer) is the one exception. Config and layer media types are not restricted, so artifacts of any type (signatures, SBOMs, Helm charts) are stored.

### Multi-platform images and referrers

An image index lists the manifests of the platforms it covers; push the platform manifests first, then the index. Deleting the index deletes the platform manifests that no other manifest and no tag names. A manifest with a `subject` is a referrer of that subject: `GET /v2/<project>/<repository>/<name>/referrers/<digest>` lists them, optionally filtered by `artifactType`. trove does not maintain the `sha256-<digest>` fallback tags of the older referrers scheme.

### Who may push a name

The first push of an image name records its owner. From then on only owners and holders of the manage permission push, move tags of or delete that image name; anybody else gets `403`. A repository can turn package ownership off.
