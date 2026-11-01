# Errors and troubleshooting

What trove answers when a request fails and what to do about it. The page explains the shape of an error answer and its stable error code, how to pass the request ID to an administrator, what each HTTP status means in trove, the typical problems of npm, pnpm and Yarn through trove, what happens when your session ends, and closes with a reference of the error codes you can meet.

## How trove reports an error

Every error answer carries a **status**, a stable **code** in snake_case and a sentence that explains it. The code is the part to search for and to quote: it never changes its meaning within a major version.

### The management API and file transfer

The management API (`/api/v1/…`, which the UI uses) and file transfer (`/files/…`) answer with problem details of [RFC 9457](https://www.rfc-editor.org/rfc/rfc9457) (`Content-Type: application/problem+json`). An example:

```json
{
  "type": "/api/v1/problems/not_found",
  "title": "Not found",
  "status": 404,
  "detail": "The resource does not exist.",
  "instance": "/files/generic-local/tools/app-1.2.0.tar.gz",
  "code": "not_found",
  "requestId": "4bf92f3577b34da6a3ce929d0e0e4736"
}
```

A refused field of a request comes with `errors`, each with a JSON pointer to the field. `GET /api/v1/problems/<code>` describes any code; it needs no sign-in.

### Package clients

npm repositories answer in npm's own shape, `{"error": "…", "message": "…", "code": "…"}`. npm prints the status as `E401`, `E404` and so on, followed by the text; pnpm and Yarn print the message. The code is in the body; the client may not show it.

## Give the request ID to an administrator

Every answer carries the header `X-Request-Id`, 32 hexadecimal digits; a problem body repeats it as `requestId`. An administrator finds the request in the logs with it.

- In the UI, an error whose code the UI has no sentence for shows **Request ID** with a **Copy** button and the hint "Give this ID to your administrator."
- With curl, `--include` prints the headers of the answer, `X-Request-Id` among them.
- A package client does not show the header. Give the administrator the time, the address the client called, the command and your user name instead; the request log records each request with its path, status and caller.

## Status codes

| Status | Meaning in trove | What to do |
|---|---|---|
| `401` | No credential where one is needed (`credentials_required`), a token that is malformed, unknown, expired or revoked (`invalid_token`, the answer does not say which), a disabled owner (`principal_disabled`), or an ended session | Set the token, create a new one, or sign in again |
| `403` | The credential is valid but lacks the permission for a write or an operation (`forbidden`), or a rule refuses it: package owners, the shadowing check, a read-only endpoint | Ask for the permission; the code names the rule |
| `404` | The resource does not exist **or is hidden from you**. A refused read answers like a missing resource, so that nobody learns what exists, unless an administrator turned this off. On an endpoint with exposure rules, everything the rules do not expose is missing too | Check the address and the name; ask whoever manages the repository whether you may read it |
| `405` | The repository class takes no writes: a remote repository (`npm_class_not_writable`) or a virtual repository without a deployment target (`no_deploy_target`) | Publish to a local repository or through a virtual one with a target |
| `409` | A conflict with the current state: a version that exists or was unpublished, a stale revision, a path that is a folder, the token limit | Read the code; often publish a new version or retry |
| `412` | A condition of the request does not hold: a resource changed since you loaded it (`If-Match`), or an upload with `If-None-Match: *` to a path that exists | In the UI, resolve the conflict dialog; elsewhere read again and decide |
| `421` | The host name of the request is not one the endpoint answers for (`misdirected_request`) | Use the address from the client setup or the guide, not an IP address or another host name |
| `429` | Too many requests (`rate_limited`): the rate limit of the API or the endpoint, or too many rejected credentials from your address | Wait the seconds of `Retry-After`; fix a wrong token first, because every rejected attempt counts |
| `503` | Temporarily unavailable: the replica is busy or stopping, the database or an upstream is not reachable, a check could not run. Always with `Retry-After` | Retry after that period; another replica may answer |

Further statuses you may meet: `400` and `422` for a malformed or invalid request (the answer names the field), `413` for an upload above a limit, `501` for an operation of a protocol that trove does not implement, and `502` and `504` when a remote repository's upstream fails for content that is not cached.

The UI repeats a read once by itself after `429`, waiting for `Retry-After` up to 30 seconds. npm repeats a failed `GET` and `PUT` itself.

## Typical npm problems

These apply to npm, pnpm, Yarn Berry and Yarn Classic alike. The guide's **Troubleshooting** section lists the codes of the npm protocol for your client.

### E401 although a token is configured

- `TROVE_TOKEN` is not set in the environment the client runs in, or is empty.
- The credential line does not name this repository's address exactly, with its trailing slash.
- Yarn Classic needs `always-auth=true` and Yarn Berry `npmAlwaysAuth: true` for a repository that is not open to anonymous readers.
- The token is expired, revoked, unknown or mistyped: `invalid_token`. [Create a new token](access-tokens.md#create-a-token).
- The token works on the main address but not on another endpoint, such as one for customers: further endpoints accept only service-account tokens bound to them unless configured otherwise.
- The owner of the token is disabled: `principal_disabled`.

trove never asks a client for a one-time password.

### E404 for a package that exists

A `404` with `not_found` has several causes, and trove does not say which, by design:

- The version was unpublished, or the upstream registry withdrew it; this also fails an install from a lockfile.
- A **namespace claim** reserves the name for a local repository: a virtual repository then takes the name from that local repository only and never asks its remote members, so the answer is `404` while the local repository lacks the version. A claim for the whole installation also makes every remote repository refuse the name.
- The package lives in a member of the virtual repository that you cannot read. A token limited to the virtual repository reads none of its members.
- An exposure rule of the endpoint you use hides the package or version, or the repository's name patterns refuse the name.
- A remote repository is offline and the package is not cached (`not_cached_offline`).
- The command is not implemented: `npm deprecate`, npm's token and profile commands.

### E403 when publishing

| Code | Cause | Fix |
|---|---|---|
| `forbidden` | Publishing and dist-tags need deploy, unpublishing delete | Ask for the permission, or use a token whose scope includes it |
| `package_owned_by_other_owner` | The package has owners and you are none of them | An owner or a holder of manage adds you |
| `shadows_remote_package` | The first publish of this name would replace, in every virtual repository that contains the local repository, a package that a remote member serves | A holder of manage on the package publishes it, or claims the name first; later versions are never checked |
| `endpoint_read_only` | A write on an endpoint with exposure rules | Publish through the default endpoint |
| `populate_not_permitted` | You may read the remote repository but not bring new packages into its cache | Ask for that permission, or have the package fetched once by an account that holds it |

When trove cannot ask the remote member whether it serves the name, the first publish answers `503` `shadow_check_unavailable` and stores nothing; retry later.

### E405 and E409 when publishing

`E405` means the address is a remote repository or a virtual one without a deployment target. `E409` with `npm_version_exists` or `npm_version_unpublished` means the version number is taken for good: publish a new version. `npm_stale_rev` means another change came between your client's read and its write; run the command again, and with pnpm 11 or 12 unpublish at the deployment target instead of through a virtual repository.

### EINTEGRITY, audits and upstream failures

- `EINTEGRITY` usually means the lockfile was written against a registry that served other bytes for that version.
- An audit that answers `501` `npm_audit_unavailable` went to a repository that forwards no audits, such as a local repository; audit through a remote or virtual repository.
- `502`, `503` and `504` with an `upstream_` code concern a package that is not cached while the remote repository's upstream fails; what is cached is still served. Retry later, and tell an administrator if it persists.

## Session ended and re-authentication

A browser session ends after a period without activity and after a maximum length (one and twelve hours unless an administrator set other values), or when it is ended under **Profile › Sessions**.

- **Your session has ended.** The UI shows this dialog when a request finds the session gone. **Sign in again** returns you to the same screen after the sign-in; **Not now** keeps the page open. An editor with unsaved changes also offers **Copy draft as JSON**.
- **Confirm it is you.** Some actions need a sign-in within the last hour (unless configured otherwise): creating or rotating a token, saving a permission policy, binding a role, changing an exposure rule, deleting a repository and others. The UI then shows this dialog; after **Sign in again** the sign-in page says that your entries are kept, and you repeat the action.

Tokens are not affected: a client with a valid token keeps working whatever happens to your browser session.

## Error codes

Each code with its status and meaning. `GET /api/v1/problems/<code>` gives the description of any code, including those an administrator meets.

<details>
<summary>Error codes you can meet as a user</summary>

| Code | Status | Meaning |
|---|---|---|
| `credentials_required` | 401 | No credential, and the request is not open to anonymous readers |
| `invalid_token` | 401 | The token is malformed, unknown, expired, revoked or not accepted on this endpoint |
| `principal_disabled` | 401 | The owner of the credential is disabled |
| `session_expired` | 401 | The session cookie names a session that has ended |
| `reauthentication_required` | 401 | The action needs a recent sign-in |
| `unauthenticated` | 401 | The request carries no valid credential |
| `forbidden` | 403 | The credential is valid; the operation is not permitted |
| `csrf_failed` | 403 | A browser request without the session's anti-forgery header; reload the page |
| `endpoint_read_only` | 403 | A write on an endpoint with exposure rules |
| `not_admitted` | 403 | The repository's include and exclude patterns refuse the path or name |
| `package_owned_by_other_owner` | 403 | The package has owners and you are none of them |
| `shadows_remote_package` | 403 | A first publish would replace a package a remote repository serves |
| `populate_not_permitted` | 403 | Not cached, and you may not fetch it from the upstream |
| `pat_not_allowed` | 403 | Personal tokens are restricted to members of certain groups |
| `derivation_exceeds_parent` | 403 | A token created with a token would outlive or outrank it |
| `not_found` | 404 | Does not exist, or is hidden from you |
| `repository_not_found` | 404 | No repository has this key, or you may not see it |
| `repository_disabled` | 404 | The repository is disabled |
| `not_cached_offline` | 404 | The remote repository is offline and the item is not cached |
| `npm_class_not_writable` | 405 | A write to a remote npm repository |
| `no_deploy_target` | 405 | A write to a virtual repository without a deployment target |
| `conflict` | 409 | Conflicts with the current state, for example an upload whose digest does not match |
| `path_conflict` | 409 | A file would sit where a folder is, or the other way round |
| `already_exists` | 409 | An object with this key or name exists |
| `npm_version_exists` | 409 | The version was published before |
| `npm_version_unpublished` | 409 | The version was unpublished and is never accepted again |
| `npm_stale_rev` | 409 | Another change reached the package between read and write |
| `token_limit_reached` | 409 | You hold the maximum number of active tokens |
| `idempotency_in_progress` | 409 | The same request is still running; retry after `Retry-After` |
| `precondition_failed` | 412 | A condition header did not match |
| `authz_epoch_unknown` | 412 | A permission change is not visible yet; read again |
| `payload_too_large`, `npm_too_large` | 413 | Above a size limit |
| `range_not_satisfiable` | 416 | The byte range lies beyond the end of the file |
| `misdirected_request` | 421 | The host is not one this endpoint answers for |
| `malformed_request`, `invalid_path`, `invalid_header` | 400 | The request, the path or a header is not valid |
| `npm_invalid_name`, `npm_invalid_tag`, `npm_bad_publish`, `npm_manifest_mismatch` | 400 | A name, a dist-tag or a publish request that npm's rules or trove's checks refuse |
| `invalid_scope`, `invalid_ttl`, `ttl_exceeds_maximum`, `description_required` | 400 | A token request with an invalid scope, lifetime or description |
| `validation_failed` | 422 | A field of the request is invalid; the answer names it |
| `rate_limited` | 429 | Too many requests; wait for `Retry-After` |
| `npm_not_implemented`, `npm_audit_unavailable` | 501 | An npm operation, or an audit, that trove does not offer here |
| `upstream_unreachable`, `upstream_error`, `upstream_auth_failed`, `upstream_integrity_mismatch`, `npm_invalid_upstream_document`, `egress_denied`, `foreign_host_denied` | 502 | The remote repository's upstream failed, refused or sent something trove does not accept, and nothing usable is cached |
| `server_busy`, `shutting_down` | 503 | The replica is at its limit or stopping; retry |
| `database_unavailable`, `authz_stale`, `unavailable` | 503 | A dependency is temporarily unavailable; retry |
| `upstream_assumed_offline`, `shadow_check_unavailable`, `fetch_queue_timeout` | 503 | The upstream is assumed down, or busy; retry after `Retry-After` |
| `upstream_timeout` | 504 | The upstream did not answer in time |
| `internal` | 500 | A defect; give the request ID to an administrator |

</details>
