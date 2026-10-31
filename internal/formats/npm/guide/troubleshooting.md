Every error answer of the repository carries a JSON body with `error`, `message` and `code`. npm prints the `error` text after the status (`E401`, `E404`), the other clients print `message`. The table lists what each code means in trove, by status.

| Status | Code | What it means in trove | What to do |
|---|---|---|---|
| 400 | `npm_invalid_name` | The package name breaks npm's rules, or a new name differs from a name the repository holds only in `-`, `_` or `.` | Choose another name |
| 400 | `npm_bad_publish` | The publish request or its tarball is malformed or ambiguous: a second `package.json`, a link that leaves the package, data after the archive | Publish with an unmodified client from the package directory |
| 400 | `npm_manifest_mismatch` | The manifest of the request and the `package.json` in the tarball disagree on dependencies, binaries, engines or install scripts | Publish with an unmodified client from the package directory |
| 400 | `npm_invalid_tag` | A dist-tag named like a version range (`1.x`), a tag on a version the package does not have, or the removal of `latest` | Use another tag name; move `latest` instead of removing it |
| 401 | `credentials_required` | The request carries no credential, and the repository is not open to anonymous readers | Set `{{.TokenEnvVar}}` in the environment the client runs in; check that the credential line names this repository's address with its trailing slash; Yarn Classic needs `always-auth=true`, Yarn Berry `npmAlwaysAuth: true` |
| 401 | `invalid_token` | The token is malformed, unknown, expired or revoked; trove does not say which | Create a new token |
| 401 | `principal_disabled` | The owner of the token is disabled | Ask an administrator |
| 403 | `forbidden` | The token is valid and lacks the permission: deploy to publish or change a tag, delete to unpublish | Ask for the permission, or use a token whose scope includes it |
| 403 | `package_owned_by_other_owner` | The package has owners and you are none of them | An owner or a holder of manage adds you as an owner |
| 403 | `shadows_remote_package` | The first publish of this name would replace a package that a remote member of a virtual repository serves | A holder of manage on the package publishes it, or claims the name first |
| 403 | `not_admitted` | A write of a package name the include or exclude patterns of the repository refuse; a read of such a name answers `404` | Use a name the patterns admit, or another repository |
| 403 | `populate_not_permitted` | You may read the remote repository but not bring new packages into its cache, and the package is not cached yet | Ask for the permission to fill the cache, or have the package fetched once by an account that holds it |
| 403 | `endpoint_read_only` | A write on an endpoint with exposure rules | Publish through the default endpoint |
| 404 | `not_found` | `E404`: the package or version does not exist here or is hidden from you. It was unpublished or withdrawn upstream; a namespace claim keeps the name from the remote members and the claiming local repository lacks it; it lives in a member you cannot read; or an exposure rule of this endpoint hides it. trove answers the same for npm commands it does not implement yet: `npm deprecate`, `npm login --auth-type=legacy`, npm's token and profile commands | Check the name and the registry address; ask whoever manages the repository whether you may read it |
| 404 | `repository_not_found` | The repository key in the address does not exist, or the repository is not an npm repository | Correct the address; the client setup page shows it |
| 404 | `repository_disabled` | The repository is disabled | Ask an administrator |
| 404 | `not_cached_offline` | The remote repository is offline and the package is not in its cache | Wait until it is online again, or use another repository |
| 405 | `npm_class_not_writable` | A write to a remote repository | Publish to a local repository, or through a virtual one |
| 405 | `no_deploy_target` | A write to a virtual repository without a deployment target | Publish to one of its local members |
| 409 | `npm_version_exists` | The version exists already | Publish a new version |
| 409 | `npm_version_unpublished` | The version was unpublished, and its number is never accepted again | Publish a new version |
| 409 | `npm_stale_rev` | Another change reached the package between your client's read and its write; pnpm 11 and 12 also get it when they unpublish through a virtual repository | Run the command again; with pnpm, unpublish at the deployment target |
| 409 | `conflict` | The tarball does not match the integrity or the shasum the publish request announces | Publish again with an unmodified client |
| 413 | `npm_too_large` | The tarball, its unpacked size, its number of files, its `package.json` or an audit request is larger than the limit | Make the package smaller (the `files` list of `package.json` decides what is packed); ask an administrator about the limits |
| 422 | `validation_failed` | Not an answer of the npm protocol: the management API (the UI and `/api/v1/…`) answers it when a field of a request is invalid, for example a repository setting | The answer names the field |
| 429 | `rate_limited` | Too many requests on this endpoint, or too many rejected credentials from your address | Wait for the period of `Retry-After`; fix the token if your client keeps sending a wrong one |
| 501 | `npm_audit_unavailable` | The repository answers no security audit (see the audit section) | Audit through a remote or a virtual repository that forwards audits |
| 501 | `npm_not_implemented` | An operation of the npm protocol that trove does not implement, such as the web login of `npm login` and `npm publish --stage` | Configure a token instead of logging in |
| 502 | `upstream_unreachable` | The remote repository could not connect to its upstream, and the package is not cached | Retry; tell an administrator if it persists |
| 502 | `upstream_error` | The upstream answered with a server error, and the package is not cached | Retry later |
| 502 | `upstream_auth_failed` | The upstream refused the remote repository's own credential | An administrator fixes the remote repository's upstream credential |
| 502 | `upstream_integrity_mismatch` | A tarball from the upstream does not match the integrity its document announces; nothing was stored | Tell an administrator; do not work around it |
| 502 | `npm_invalid_upstream_document` | The upstream's package document failed trove's checks; the copy trove had stays in service | Retry later |
| 502 | `egress_denied` | The outbound address policy refuses the upstream's address | An administrator checks the upstream and the egress rules |
| 502 | `foreign_host_denied` | A document points at a host that is neither the upstream nor one of the remote repository's extra hosts | An administrator adds the host to the remote repository if it is trusted |
| 503 | `upstream_assumed_offline` | Repeated failures opened the circuit of the upstream, and the package is not cached | Wait for the period of `Retry-After`; trove probes the upstream again |
| 503 | `shadow_check_unavailable` | trove could not ask a remote member whether it serves the name of a first publish; nothing was stored | Retry later |
| 503 | `server_busy` | The replica that answered is at its concurrency limit | Retry; npm repeats a failed `GET` and `PUT` itself |
| 503 | `shutting_down` | The replica that answered is stopping | Retry |
| 504 | `upstream_timeout` | The upstream did not answer in time, and the package is not cached | Retry later |
