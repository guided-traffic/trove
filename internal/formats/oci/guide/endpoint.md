{{if .ExposureRules}}This endpoint serves under exposure rules. The OCI format has no exposure adapter yet, so the endpoint does not serve container images; use an endpoint that exposes everything.{{else}}This endpoint serves the registry as described in the other sections.{{end}}

### Docker Engine mirror

On a host whose root an administrator bound to one repository, the registry API is that repository's: `https://<host>/v2/<name>/…` addresses an image name without the project and repository key, the token endpoint is the host's own, and scopes name the image without a prefix. A Docker Engine configured with `"registry-mirrors": ["https://<host>"]` in `daemon.json` pulls Docker Hub images through the bound repository. Only pulls use the mirror; a push goes to the registry named in the reference.

The catalog of a bound host lists the names of the bound repository only.
