# Data and secret flow

Status: implemented in M0 for the credentials of every connection, the stored-secret key ring and its envelope, and the
redaction of logs, spans and error texts, and in M2 for the upstream credentials of remote repositories: their sealed rows, the
secret references and the clients that send them. Package bytes do not exist yet: the blob layer is built, but no package
writes it.

This page says where each credential lives inside the running installation, when a process reads it, what a process holds in
memory, and what is written where it could be read by someone else. The rules are the code's; the requirement pages are linked.

## Where credentials live

Credentials are files. The chart mounts each one from an existing Secret and names only the path in the configuration.
No credential is an environment variable: the chart injects only non-secret object-store settings from the provisioner's
Secret through `secretKeyRef` (`TROVE_STORAGE_S3_BUCKET`, `TROVE_STORAGE_S3_ENDPOINT`, `TROVE_STORAGE_S3_REGION`;
[_endpoints.tpl](../../deploy/helm/trove/templates/_endpoints.tpl), `trove.s3EnvFromSecret`). The chart never creates a Secret
([values.yaml](../../deploy/helm/trove/values.yaml), `secrets`).

| Credential | Where it lives | When a process reads it | Where it is not |
|---|---|---|---|
| PostgreSQL user and password | files under `/var/run/secrets/trove/database/` | on every new connection ([postgres/connect.go](../../internal/meta/postgres/connect.go), `beforeConnect`) | the connection string carries neither ([connString](../../internal/meta/postgres/connect.go)) |
| Valkey user and password, Sentinel user and password | files under `/var/run/secrets/trove/valkey/` and `valkey-sentinel/` | on every connection attempt, through `AuthCredentialsFn` ([cluster/valkey_auth.go](../../internal/cluster/valkey_auth.go)) | the process holds the values only while it authenticates |
| Object-store access key and secret | files under `/var/run/secrets/trove/s3/` (`static`), or the SDK's default chain, or a web-identity token file | the static files at most once per second ([blob/s3/credentials.go](../../internal/blob/s3/credentials.go), `fileReread`) | the default chain: see [H-74](privilege-footprint.md#h-74--the-default-object-store-credential-chain-can-reach-node-metadata) |
| Metrics bearer token | file `metrics.bearer_token_file` | on every request to `/metrics` with `auth: static` ([observability/metrics.go](../../internal/observability/metrics.go), `authorized`) | not logged |
| OTLP headers | file `tracing.headers_file`, `Name: value` per line | once, when the exporter is built ([observability/otlp.go](../../internal/observability/otlp.go)) | not in the configuration |
| Stored-secret key ring | files under `/var/run/secrets/trove/encryption/`, one per key | once, at start ([app.go](../../internal/app/app.go), `secrets.Load`) | see [H-55](../spec/architecture.md#h-55--key-files-of-the-stored-secret-key-ring-are-read-once-at-start-up) |
| Break-glass password | one file, `/var/run/secrets/trove/break-glass/password`, from `secrets.breakGlass`; plaintext (at least 20 characters) or an Argon2id hash | at start and again whenever its modification time changes, looked at every `auth.break_glass.check_interval` ([oidc/breakglass.go](../../internal/auth/oidc/breakglass.go)); read by `web` replicas only (R-AUTH-042) | not in PostgreSQL, not in `worker`, the migration Job or the backup pods; not logged |
| Upstream password or token of a remote repository | sealed in `repository_secrets.sealed`, or a reference to a file under `config_as_code.secrets_dir` or to a `TROVE_CAC_` variable | a sealed value when the repository's client is built; a reference at most every 10 s ([upstream/secret.go](../../internal/proxy/upstream/secret.go)); both only by the replica that sends a request to that upstream | not in `repositories.config` (it holds `{"set": true}` or the reference), not in an audit event, a log line or an error text, not in any API answer |
| OIDC client secrets | one file per provider, `/var/run/secrets/trove/oidc-<providerId>/client-secret`, from `secrets.oidc[]` | when the file's modification time changes ([oidc/provider.go](../../internal/auth/oidc/provider.go)); `web` replicas only | not in PostgreSQL, not in `worker`, the Job or the backup pods |

The chart mounts each Secret into the pods that need it and nowhere else ([_secrets.tpl](../../deploy/helm/trove/templates/_secrets.tpl)):

- the key ring (`encryption`) only in the `web` and `worker` pods, never in the migration Job (the migration never seals or opens a value);
- the object-store credentials only with `credentials.source: static`;
- the metrics token only with `metrics.auth: static`;
- the break-glass password and the OIDC client secrets only in the `web` pods and endpoint pools, which serve the login; the
  `worker` and the Job never get them. The Secret that holds the break-glass password is an administrator credential: whoever can
  read it through the Kubernetes API can sign in as the break-glass account, so its RBAC is the access control of that account;
- the listener certificate (`tls.crt` and `tls.key`, not `ca.crt`) only in the pods whose process serves a listener.

The Valkey and PostgreSQL files are mounted with `defaultMode: 0440`, read-only, without `subPath`, so a rotated Secret reaches
the process through the kubelet's swap and the next read (see [transport-encryption.md](transport-encryption.md)).

## Upstream credentials of remote repositories

A remote repository's password (the token with `authMode: bearer`) is a secret field ([R-REPO-033](../spec/repositories-and-proxy.md),
[remote.go](../../internal/repository/remote.go), `SecretField`). Three forms exist, and each has one place:

| Form | In the request | In `repositories.config` and in every read | In `repository_secrets` |
|---|---|---|---|
| inline value | `{"value": "…"}`, through the API only | `{"set": true}` | `sealed`: AES-256-GCM under the active key, associated data `(table, column, repo_id, field)` |
| reference | `{"secretRef": {"file": "<name>"}}` or `{"env": "TROVE_CAC_<NAME>"}` | the reference | `secret_ref` |
| keep | `{"set": true}` | `{"set": true}` | unchanged; 422 `invalid_secret` when there is no sealed value |

The plaintext of a request exists in `Settings.pending` between the validation and the write, in an `observability.Secret`, and is
sealed in the transaction that writes the row ([secrets_store.go](../../internal/repository/secrets_store.go), `writeSecrets`). No JSON form
of the field can carry it (`SecretField.MarshalJSON`), the audit event of the update lists the member `remote` and nothing of its content,
and the idempotency layer stores a hash of the request body, not the body. A value that is wrong (a line break, over 8 KiB, empty) is
refused before anything is written; an error text never repeats a value.

The associated data binds a sealed value to its repository and field: copying the `sealed` text of one repository's row into another's
makes that repository's client fail with `502 upstream_secret_unavailable` and leaves every other repository working
([upstream/clients.go](../../internal/proxy/upstream/clients.go), `secretOf`). A reference is checked twice, when the document is written
and when the client reads it (`SecretRef.Valid`): a file name has no path separator, no `..` and no leading dot, a variable carries the
prefix `TROVE_CAC_`, so `TROVE_DATABASE_PASSWORD` and the files of the key ring cannot be routed to an upstream whatever a row says.

The plaintext lives in the replica that sends a request: in the client's credential object for as long as the client is cached, and in
the `Authorization` header of the request in flight. The credential policy of the client removes every `Authorization` header and sets it
again only for the hosts of [egress.md](egress.md#which-hosts-receive-credentials). A rotated reference reaches a replica within 10 s
without a new client; a rotated inline value moves the repository's `config_version`, which replaces the client.

## The stored-secret key ring

A key ring holds the keys with which the database's sealed columns are encrypted (R-ARCH-021). Keys are loaded from
`secrets.encryption_key_files`; the active key is `secrets.active_key_id` ([config/secrets.go](../../internal/config/secrets.go)).

- **File form.** A key file holds 32 random bytes. A file of exactly 32 bytes is the key itself. Any other file is base64 text
  that decodes to 32 bytes, standard or unpadded. The check of the exact length comes before any trimming, because a random
  key may start or end with a white-space byte ([secrets/keyring.go](../../internal/secrets/keyring.go), `readKeyFile`).
- **Key ID.** The file name without `.key`. It must match `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`, and no two files may share one
  ([keyring.go](../../internal/secrets/keyring.go), `Load`).
- **Ring.** Every listed key opens values; only the active key seals. The active ID must be one of the listed keys
  ([config/secrets.go](../../internal/config/secrets.go), `Validate`). The chart takes the first key of `secrets.encryption.keys`
  as active ([_helpers.tpl](../../deploy/helm/trove/templates/_helpers.tpl)).
- **Fingerprint.** `Fingerprint` is an HMAC-SHA-256 under a key derived with HKDF-SHA-256 from the active key, info
  `trove fingerprint v1`. It has no call site in M0.
- **Canary.** `SealCanary` and `CheckCanary` seal and open a known text in the `secret_key_canary` row of a key. No start-up
  path calls them in M0, and `RequireKeys` (which names the missing key IDs of a database or a backup) has no call site yet.
  A ring that does not match stored values is therefore not reported at start in M0. This becomes relevant with the first
  sealed column of M1.
- **Errors.** Errors name the file or the key ID, never key material. `MismatchError` lists missing key IDs only
  ([keyring.go](../../internal/secrets/keyring.go), `MismatchError`).
- **Logging.** `KeyRing` implements `String` and `LogValue` with the key IDs only ([keyring.go](../../internal/secrets/keyring.go)).

## The envelope and its additional data

A sealed value is stored as `v1:<key ID>:<base64(nonce ‖ ciphertext ‖ tag)>`:

- AES-256-GCM with a random 96-bit nonce per value, from `crypto/rand`;
- the key ID is in clear, so a value under an old key is found and re-sealed by its ID, and an unknown ID names only the ID
  (`ErrUnknownKey`);
- the additional data binds the value to its row and column. It is the count of primary-key parts, then the strings
  `trove/sealed/v1`, the table name, the column name and each primary-key part, every string prefixed with its length as
  an unsigned varint ([keyring.go](../../internal/secrets/keyring.go), `aad`). A value moved to another row or column fails
  authentication (`ErrAuthentication`), and two different row keys do not encode alike (`("a:b")` and `("a", "b")` differ);
- a row key with the wrong number of parts is refused (`ErrRowKey`), and a column that is not registered cannot be sealed or
  opened (`ErrUnregistered`).

Rotation: a value under a key other than the active one is re-sealed (`Reseal`, `NeedsReseal`), a value under the active key is
left as it is, so the re-encryption job is idempotent. The procedure in [R-ARCH-021](../spec/architecture.md#r-arch-021--encryption-of-stored-secrets-and-the-stored-secret-key)
adds the new key first (decrypt-only on every replica), makes it active, re-seals, and removes the old key last.

## Redaction

Secrets are kept out of every log record, span attribute, error text and ring-buffer record. The mechanism is in
[observability/redact.go](../../internal/observability/redact.go):

- a `Secret` type whose string forms are the marker `[REDACTED]`; only `Reveal` returns the value, and only the code that needs to
  send it calls that. `config.Secret` does the same for configuration values ([config/secret.go](../../internal/config/secret.go));
- attribute keys containing `password`, `secret`, `token`, `authorization`, `cookie`, `api_key`, `apikey`, `private_key` or
  `credential` have their value replaced (`token_id` and `token_type` are exempt);
- free text: `trv_<prefix>_<value>` tokens keep the prefix and lose the value, `Bearer` and `Basic` values are replaced, URL user
  information is replaced up to the last `@` before the authority ends, the poll secret of a `/auth/cli/` path is replaced, and
  the values of the configured query parameters (`log.request.redact_query_params`) are replaced.

The scrubber runs before a field is cut to its length, so a secret is never split into a fragment that no pattern recognises
(`clipField`, [requestlog.go](../../internal/observability/requestlog.go)). The span exporter scrubs its attributes through a
wrapper ([tracing.go](../../internal/observability/tracing.go), `scrubExporter`).

The in-memory ring buffer holds the same lines the log handler writes, bounded by record count and bytes, and a record above
16 KiB is kept as a stub ([ring.go](../../internal/observability/ring.go)). Standard output is not bounded, which is
[H-70](#h-70--the-standard-output-log-carries-every-record-in-full).

## What a process holds, and what Valkey may hold

A replica holds in memory the key ring and the credentials it has read, for as long as it uses them. The plaintext of a sealed
value exists only while a request needs it. Nothing trove writes to disk holds a credential: the spool holds bodies of running
uploads, with mode `0600` and UUID names ([spool.go](../../internal/blob/spool.go)), and its contents are not encrypted by trove
([H-2](../spec/architecture.md#h-2--the-upload-spool-is-not-encrypted-by-trove)).

Valkey holds coordination state and the negative cache of remote repositories. Keys are built from registered families with a
mandatory expiry ([valkey_keys.go](../../internal/cluster/valkey_keys.go)); their parts match `[A-Za-z0-9_-]`. Nothing trove
serves is read from Valkey and no positive authentication or authorization result is ([spec Q-ARCH-16](../spec/architecture.md#q-arch-16--what-valkey-may-hold)).
What a Valkey writer can do is on [roles-and-trust-boundaries.md](roles-and-trust-boundaries.md).

## What this does not cover

### H-70 · The standard-output log carries every record in full

Stage: every log record written to standard output, which the platform's log stack collects
([observability/ring.go](../../internal/observability/ring.go) bounds only the in-memory copy).

Dormant: the in-memory copy is bounded, but the line on standard output is the whole record. A record above 16 KiB is kept in
the ring as a stub with its head and its original size, while standard output keeps all of it. The request log clips the
user agent, path and host before they reach a record ([requestlog.go](../../internal/observability/requestlog.go)), so the
gap becomes live only with a new attribute that carries client data unclipped, or with a value that a scrubber pattern does not
match. Subject: the log storage of the platform, which keeps what standard output carries.
</content>

### H-280 · A secret that cannot be resolved is found at its first use

Stage: the repository's upstream client, built at the first request after a start or after a change of the repository
([upstream/clients.go](../../internal/proxy/upstream/clients.go), `buildSaved`).

Live: the specification says such a secret is reported at start-up ([R-REPO-033](../spec/repositories-and-proxy.md)). A replica does not
read every repository at start-up; it logs the failure when the client is built and fails that repository's requests with
`502 upstream_secret_unavailable`. Subject: a repository whose reference file is missing on one replica, or whose sealed value was
sealed under a key that replica's ring lacks, until a request or an upstream test reaches it. Other repositories are unaffected and
readiness does not depend on it.

What makes it real: a rolled-out secret file that misses a replica, or a key ring that is rolled out in the wrong order (the canary
check at start-up catches a key that the database references, not a reference file). The upstream test of the repository reports it on
demand.

### H-281 · A holder of `repositories:write-config` can send any file of `secrets_dir` to an upstream of their choice

Stage: the resolution of a `secretRef` ([upstream/secret.go](../../internal/proxy/upstream/secret.go), `resolver`) together with the
choice of the upstream URL.

Live: whoever may create or change a remote repository chooses both the URL and the reference, so a reference to a file that belongs to
another repository's upstream, with this repository's URL pointing at a host the holder controls, sends that file's content there in
the `Authorization` header. The same holds for every variable with the prefix `TROVE_CAC_`. The rules of R-OPS-029 confine the reference
to the directory and the prefix, so trove's own secrets are out of reach, but they do not separate the secrets inside them. Subject: the
upstream tokens that the operator mounts under `config_as_code.secrets_dir`, one per partner.

What makes it real: one `secrets_dir` that holds the tokens of several upstreams and a repository administrator who is not meant to
know all of them. The mitigation is operational: mount only the references that the holders of the capability may use.

