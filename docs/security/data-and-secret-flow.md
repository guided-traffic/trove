# Data and secret flow

Status: implemented in M0 for the credentials of every connection, the stored-secret key ring and its envelope, and the
redaction of logs, spans and error texts. Package bytes and tokens do not exist yet: the blob layer is built, but no package
writes it, and no token is issued.

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
| Break-glass password | not built (M1, R-AUTH-014) | not applicable | not applicable |

The chart mounts each Secret into the pods that need it and nowhere else ([_secrets.tpl](../../deploy/helm/trove/templates/_secrets.tpl)):

- the key ring (`encryption`) only in the `web` and `worker` pods, never in the migration Job (the migration never seals or opens a value);
- the object-store credentials only with `credentials.source: static`;
- the metrics token only with `metrics.auth: static`;
- the listener certificate (`tls.crt` and `tls.key`, not `ca.crt`) only in the pods whose process serves a listener.

The Valkey and PostgreSQL files are mounted with `defaultMode: 0440`, read-only, without `subPath`, so a rotated Secret reaches
the process through the kubelet's swap and the next read (see [transport-encryption.md](transport-encryption.md)).

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
