{{if .ExposureRules}}You are using an endpoint with exposure rules, such as an endpoint for customers or partners. It serves only the packages, versions and dist-tags its rules expose:

- A hidden version is missing from `npm view` and from the package document, and installing it answers `404` (`E404`) exactly like a version that does not exist. A lockfile written through another endpoint can name such a version; an install from it then fails with `E404` here.
- `latest` points to the highest exposed release, which can be older than `latest` elsewhere, and a dist-tag whose version is hidden is left out.
- Writes are refused: publish, dist-tag changes and unpublish answer `403` `endpoint_read_only`. Publish through the default endpoint.
- The security audit answers `501` `npm_audit_unavailable` on this endpoint.
- On an endpoint that does not fill caches, a remote repository serves what it has cached already; a version it has not cached answers `404` and is not fetched from the upstream.

The addresses in this guide and in every answer of this endpoint name its own host. Use the token that was issued for this endpoint.
{{end}}