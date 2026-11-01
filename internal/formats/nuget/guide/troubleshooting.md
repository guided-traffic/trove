A NuGet client prints its own error numbers and often only the status of the answer. The text of an answer is one line, `<code>: <message>`, which `curl -i` shows; `dotnet nuget push` and `nuget push` print the status and the reason phrase (`403 (Forbidden)`) and nothing else, because a failed push has no body to print. {{if eq .Client "dotnet"}}`dotnet restore -v d` lists every request and its status.{{else if eq .Client "nuget"}}`nuget restore -Verbosity detailed` lists every request and its status.{{else}}The Package Manager Output window of Visual Studio, set to show detailed messages, lists every request and its status.{{end}} The table lists what each code means in trove, by status.

- `NU1301` (unable to load the service index) names the status of the failed request; look that status up below. A `401` or `403` for the service index of a repository means the credential, `404` an address that is no repository of trove or one with a typo.
- `NU1101` (unable to find package) means that no source of the configuration knows the ID: the ID is misspelled, the repository does not hold it, a namespace claim keeps the remote members away, or you cannot read the member that holds it.
- `NU1102` (unable to find package with version) means that the ID exists and the version or range you asked for does not: it was never pushed, it was deleted, or the endpoint you use hides it.

| Status | Code | What it means in trove | What to do |
|---|---|---|---|
| 400 | `nuget_invalid_package` | The file is not a package trove accepts: not a ZIP archive, a repeated or missing `.nuspec` at its root, an entry name or limit the archive rules refuse | Build the package again with `dotnet pack` or `nuget pack`; the message names the rule |
| 400 | `nuget_invalid_id` | The package ID breaks trove's rules for the repository. A local repository takes ASCII letters, digits and `_` in segments separated by a single `.` or `-`, at most 100 characters | Choose another ID |
| 400 | `nuget_invalid_version` | The version is not a NuGet version trove accepts: one to four numeric parts with an optional pre-release label and build metadata, at most 255 characters | Correct the version in the project |
| 401 | `credentials_required` | The request carries no credential, and the repository is not open to anonymous readers | Set `{{.TokenEnvVar}}` in the environment the client runs in; check that the source of the `nuget.config` has a credential of its own; for a push pass `--api-key` |
| 401 | `invalid_token` | The token is malformed, unknown, expired or revoked; trove does not say which | Create a new token |
| 401 | `principal_disabled` | The owner of the token is disabled | Ask an administrator |
| 403 | `forbidden` | The token is valid and lacks the permission: deploy to push or relist, delete to unlist or delete | Ask for the permission, or use a token whose scope includes it |
| 403 | `package_owned_by_other_owner` | The ID has owners and you are none of them | An owner or a holder of manage adds you as an owner |
| 403 | `shadows_remote_package` | The first push of this ID would replace a package that a remote member of a virtual repository serves | A holder of manage on the ID pushes it, or an administrator claims the ID first |
| 403 | `populate_not_permitted` | You may read the remote repository but not bring new packages into its cache, and the package is not cached yet | Ask for the permission to fill the cache, or have the package restored once by an account that holds it |
| 403 | `endpoint_read_only` | A write on an endpoint with exposure rules | Push through the default endpoint |
| 404 | `not_found` | The ID, the version or the file does not exist here or is hidden from you: it was deleted or withdrawn upstream, a namespace claim keeps the name from the remote members and the claiming local repository lacks it, it lives in a member you cannot read, or an exposure rule hides it | Check the ID and the address; ask whoever manages the repository whether you may read it |
| 404 | `nuget_v2_unsupported` | The address is no NuGet v3 resource: NuGet v2 is not served. The message names the service index of the repository | Use `{{.RegistryURL}}v3/index.json` as the source |
| 404 | `repository_not_found` | The repository key in the address does not exist, or the repository is not a NuGet repository | Correct the address; the client setup page shows it |
| 404 | `repository_disabled` | The repository is disabled | Ask an administrator |
| 404 | `not_cached_offline` | The remote repository is offline and the package is not in its cache | Wait until it is online again, or use another repository |
| 405 | `method_not_allowed` | A push, delete or relist to a remote repository, which takes no writes | Push to a local repository, or through a virtual one |
| 405 | `no_deploy_target` | A write to a virtual repository without a deployment target | Push to one of its local members |
| 409 | `nuget_version_exists` | The version exists with other bytes | Push a new version |
| 409 | `nuget_version_deleted` | The version was deleted, and its number is never accepted again | Push a new version |
| 409 | `nuget_version_reserved` | After a restore of trove, this version number is reserved for the package that had been pushed before the restore; other bytes are refused | Push a new version, or ask the administrator for the package that was lost |
| 413 | `payload_too_large` | The package is larger than the upload limit of the installation | Make the package smaller; ask an administrator about the limit |
| 429 | `rate_limited` | Too many requests on this endpoint, or too many rejected credentials from your address | Wait for the period of `Retry-After`; fix the token if your client keeps sending a wrong one |
| 501 | `nuget_not_implemented` | An operation of the NuGet v3 protocol that this build of trove does not serve | Use another operation, or ask an administrator which release serves it |
| 502 | `upstream_unreachable` | The remote repository could not connect to its upstream, and the package is not cached | Retry; tell an administrator if it persists |
| 502 | `upstream_error` | The upstream answered with a server error, and the package is not cached | Retry later |
| 502 | `upstream_auth_failed` | The upstream refused the remote repository's own credential | An administrator fixes the upstream credential of the remote repository |
| 502 | `upstream_package_mismatch` | The package the upstream sent is not the ID and version that was asked for; nothing was stored | Tell an administrator; do not work around it |
| 502 | `egress_denied` | The outbound address policy refuses the upstream's address | An administrator checks the upstream and the egress rules |
| 502 | `foreign_host_denied` | The upstream's service index points at a host that is neither the upstream nor one of the extra hosts of the remote repository | An administrator adds the host to the remote repository if it is trusted |
| 503 | `upstream_assumed_offline` | Repeated failures opened the circuit of the upstream, and the package is not cached | Wait for the period of `Retry-After`; trove probes the upstream again |
| 503 | `shadow_check_unavailable` | trove could not ask a remote member whether it serves the ID of a first push; nothing was stored | Retry later |
| 503 | `server_busy` | The replica that answered is at its concurrency limit | Retry; NuGet repeats a failed request itself |
| 503 | `shutting_down` | The replica that answered is stopping | Retry |
| 504 | `upstream_timeout` | The upstream did not answer in time, and the package is not cached | Retry later |
