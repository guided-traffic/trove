{{if not .Anonymous}}{{if or .PublishTarget (not .Repository)}}`trove_curl --fail --upload-file <file> "{{.RegistryURL}}<path>"` uploads a file to the path with `PUT`. trove answers `201` with the description of the stored file, its digests among them, and `200` when the path holds these bytes already; then nothing changes. The folders on the way arise with the file. A `PUT` of an address that ends in `/`, without a body, creates an empty folder.

curl sends no media type with `--upload-file`, so trove serves the file as `application/octet-stream`. To store one, add `--header "Content-Type: <type>"`: trove keeps a well-formed media type of at most 255 bytes and serves the file with it, and refuses any other value with `400` `invalid_header`.

### Uploading with a digest

trove computes the digests of every upload. To have it check the SHA-256 you computed, send it as `Repr-Digest`; when the bytes differ, trove answers `409` `conflict` and stores nothing:

```shell
trove_curl --fail --upload-file <file> \
  --header "Repr-Digest: sha-256=:$(openssl dgst -sha256 -binary <file> | base64):" \
  "{{.RegistryURL}}<path>"
```

trove checks a `sha-512` member the same way. A body shorter or longer than its `Content-Length` answers `400` and is not stored either.

### Upload once, never replace

With `--header "If-None-Match: *"` an upload to a path that exists answers `412`, whatever the path holds: a release is uploaded once, and a second run of the job does not replace it, even when its account holds overwrite.

### Limits

A file holds at most 10 GiB, unless an administrator set another limit for the installation, or a lower one for the repository; a larger upload answers `413` `payload_too_large`.
{{else}}You cannot upload to `{{.Repository}}`: you do not hold the deploy permission on it. Ask whoever manages the repository for the permission, or upload to another repository.
{{end}}{{end}}