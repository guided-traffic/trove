{{if .ExposureRules}}You are using an endpoint with exposure rules, such as an endpoint for customers or partners. Its rules expose paths of a generic repository:

- A file outside the exposed paths answers `404` exactly like a file that does not exist, and a folder listing leaves it out.
- Writes are refused: upload and delete answer `403` `endpoint_read_only`. Upload through the default endpoint.
- An endpoint serves the addresses below `/files/` only when an administrator added the files surface to it; without it, every address of this guide answers `404` on this endpoint.

The addresses in this guide name the endpoint's own host. Use the token that was issued for this endpoint.
{{end}}