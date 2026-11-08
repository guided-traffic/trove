Pull an image with the reference of the snippet: `{{if .ImagePrefix}}{{.ImagePrefix}}{{else}}<host>/<project>/<repository>{{end}}/<name>:<tag>`, or pin it to a digest with `@sha256:<hex>` instead of the tag.{{if eq .Class "remote"}} Through a remote repository the image name is the name at the upstream; on Docker Hub `nginx` and `library/nginx` are the same image.{{end}}{{if eq .Class "virtual"}} The virtual repository answers from the first member that has the tag; see the section on what a virtual repository is for the order.{{end}}

### Multi-platform images

An image index lists one manifest per platform, and the client picks the one that matches its machine. {{if eq .Client "docker"}}`docker pull --platform linux/arm64 <reference>` pulls another platform than the host's.{{else}}`podman pull --platform linux/arm64 <reference>` pulls another platform than the host's; `podman manifest inspect <reference>` lists the platforms of an index.{{end}}

### Pin by digest

A tag can move; a digest cannot. trove serves the manifest of a digest for as long as it is stored, whether a tag still points at it or not, so a deployment that names the digest keeps working when somebody moves the tag. Kubernetes pulls by digest the same way.
