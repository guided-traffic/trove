Every error answer below `/files/` is a JSON problem (`application/problem+json`) with `status`, `title`, `detail` and `code`. With `--fail` curl prints only the status, for example `curl: (22) The requested URL returned error: 404`, and leaves the body out: send the request again without `--fail`, or with `--fail-with-body` instead (curl 7.76 and later), to read the code. The table lists what each code means in trove, by status.

| Status | Code | What it means in trove | What to do |
|---|---|---|---|
| 400 | `invalid_path` | The path has an empty, `.` or `..` segment, a NUL, a backslash or a control character, is not valid UTF-8, or is longer than 4096 bytes or has a segment longer than 255 bytes; or the request uploads to or deletes the root of the repository | Correct the path; encode a space as `%20` |
| 400 | `invalid_header` | `Content-Type` is not a media type or is longer than 255 bytes, or `Repr-Digest` is not of the form `sha-256=:<base64>:` | Correct the header |
| 400 | `malformed_request` | The body is shorter or longer than its `Content-Length`, a folder is created with a body, or the `limit` of a listing is not a number of 1 or more | Upload the file again; create a folder without a body |
| 400 | `invalid_cursor` | The `cursor` of a listing is not one trove issued | Start the listing again |
| 401 | `credentials_required` | The request carries no credential, and anonymous access is off or this endpoint takes no anonymous requests | Set `{{.TokenEnvVar}}` in the environment curl runs in, and define `trove_curl` there while it is set |
| 401 | `unauthenticated` | The request carries no credential, and the repository is not open to anonymous readers | As above |
| 401 | `invalid_token` | The token is unknown, malformed, expired or revoked, or `{{.TokenEnvVar}}` is empty; trove does not say which | Check the variable; create a new token |
| 401 | `principal_disabled` | The owner of the token is disabled | Ask an administrator |
| 403 | `forbidden` | The token is valid and lacks the permission: deploy to upload, overwrite to replace a file with other bytes, delete to delete, on every item of a folder; read, where an administrator chose not to hide the existence of repositories. A write to a repository key that does not exist gets the same answer | Ask for the permission, or use a token whose scope includes it; check the repository key |
| 403 | `not_admitted` | An upload to a path the include or exclude patterns of the repository refuse; a download of such a path answers `404` | Use a path the patterns admit, or another repository |
| 403 | `endpoint_read_only` | A write on an endpoint with exposure rules | Upload through the default endpoint |
| 404 | `not_found` | The file does not exist here or is hidden from you: you may not read it, the repository's patterns or an exposure rule of this endpoint hide it, or the repository key names no repository you may read | Check the path and the key; ask whoever manages the repository whether you may read it |
| 404 | `repository_disabled` | The repository is disabled | Ask an administrator |
| 404 | `repository_not_found` | The repository is being deleted | Use another repository |
| 405 | `method_not_allowed` | A method other than `GET`, `HEAD`, `PUT` and `DELETE` | Use one of them |
| 409 | `path_conflict` | A path names a file or a folder, never both: the upload would put a file below a file, a file where a folder is, or a folder where a file is | Choose another path, or delete what is in the way |
| 409 | `conflict` | The uploaded bytes differ from the `Repr-Digest` sent with them; nothing was stored | Upload again; when it persists, the file changed between computing the digest and the upload |
| 412 | `precondition_failed` | An upload with `If-None-Match: *` to a path that exists, or a download whose `If-Match` or `If-Unmodified-Since` does not hold | Nothing, for an upload that must not replace; else send the request without the condition |
| 413 | `payload_too_large` | The upload is larger than the limit of the installation or of the repository, 10 GiB by default | Ask an administrator about the limits |
| 416 | `range_not_satisfiable` | The `Range` starts at or beyond the end of the file; `Content-Range` names the size | Ask for a range within the file; for `--continue-at -` on a complete copy, nothing |
| 429 | `rate_limited` | Too many requests on this endpoint, or too many rejected credentials from your address | Wait for the period of `Retry-After`; fix the token if a script keeps sending a wrong one |
| 503 | `unavailable` | The replica has no room to take the upload now, or concurrent requests are changing the same path | Retry after the period of `Retry-After` |
| 503 | `content_unavailable` | The stored object of the file is missing; when it failed trove's verification instead, the status is `500` | Retry later; tell an administrator when it persists |
| 503 | `object_store_unavailable` | trove cannot reach its object store | Retry later |
| 503 | `database_unavailable` | trove cannot reach its database | Retry later |
| 503 | `authz_stale` | The replica could not confirm that its permissions are current | Retry after the period of `Retry-After` |
| 503 | `server_busy` | The replica that answered is at its concurrency limit | Retry |
| 503 | `shutting_down` | The replica that answered is stopping | Retry |
