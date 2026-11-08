The registry answers in the error format of the distribution specification: `{"errors":[{"code":"…","message":"…"}]}`. The code tells the cause.

| Answer | Cause and remedy |
|---|---|
| `x509: certificate signed by unknown authority` | The client does not trust the certificate of the trove host. Install the CA certificate for the host (see the section on pointing the client at the repository). |
| `401` `UNAUTHORIZED` | No credential, an expired or revoked token, or a registry token that lacks the action. Log in again; a `401` with `error="insufficient_scope"` makes the client fetch a new token on its own. |
| `403` `DENIED` | The token is valid and may not do this: no permission to deploy, to overwrite a moved tag or to delete, or the image name is owned by somebody else. |
| `404` `NAME_UNKNOWN` | The project or repository does not exist, you may not see it, or the repository holds nothing of the image name. The answer is the same for all three on purpose. |
| `404` `MANIFEST_UNKNOWN` | No manifest with this tag or digest is stored, or the manifest was withheld after a restore. |
| `404` `BLOB_UNKNOWN` | The image name holds no layer with this digest. A layer is readable through the image name it was pushed to, not through another name in the same repository. |
| `400` `MANIFEST_INVALID` | The manifest is not valid JSON of a supported type, uses the Docker schema 1 format, or its `mediaType` differs from the `Content-Type` of the request. |
| `400` `MANIFEST_BLOB_UNKNOWN` | The manifest names a layer, config or manifest that the image name does not hold. Push them first. |
| `413` `SIZE_INVALID` | A manifest above 4 MiB, a layer above the repository's limit, or the quota. |
| `409` `DENIED` | A deletion that would leave a dangling reference: a manifest that an index lists, or a blob that a manifest references. Or a push of a manifest that a restore withheld. |
| `405` `UNSUPPORTED` | A write to a remote repository, or to a virtual repository without a deployment target. |
| `429` / `503` `TOOMANYREQUESTS` | Rate limited by trove, or by the upstream of a remote repository. Wait for the time in `Retry-After`; authenticate a Docker Hub remote to raise its allowance. |
| `502`, `503`, `504` `UNAVAILABLE` | The upstream of a remote repository failed, is unreachable or its circuit is open. Images already cached are still served. |
