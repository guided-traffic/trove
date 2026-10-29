# Stored-secret encryption

Package [internal/secrets](../../internal/secrets) seals the secrets that trove stores in PostgreSQL to reach
other systems (upstream credentials, webhook secrets, outbound proxy passwords). Requirements: R-ARCH-021,
R-HA-027, Q-HA-8 in [architecture.md](../spec/architecture.md) and [ha-and-helm.md](../spec/ha-and-helm.md).
User-visible secrets of other kinds (tokens, passwords of principals) are hashed, not sealed, and are not part
of this package.

## Files

| File | Responsibility |
|---|---|
| [keyring.go](../../internal/secrets/keyring.go) | `Load`, `KeyRing`: `Seal`, `Open`, `Reseal`, `RowKey`/`Row`, the AAD, canary, fingerprints, `RequireKeys` |
| [registry.go](../../internal/secrets/registry.go) | `Column`, `Register`, `Freeze`, `Columns`: the sealed-column registry |
| [status.go](../../internal/secrets/status.go) | `Status`, `StatusWithCounts`, the `ValueCounter` interface |

## Key ring

`secrets.encryption_key_files` lists files; `secrets.active_key_id` names the sealing key.

- A key file holds 32 random bytes in one of two forms. Base64 text (43 or 44 characters, surrounding whitespace
  ignored), or the 32 bytes themselves, as a random-byte generator writes them (the `type: bytes` fields of
  internal-secrets-operator). A file of exactly 32 bytes is the raw form, checked before anything is trimmed, so a
  key may start or end with a white-space byte; base64 text of 32 bytes is never 32 characters long (Q-ARCH-34). A
  file of any other size that is not base64 is refused. The file name without
  `.key` is the key ID, which must match `[A-Za-z0-9][A-Za-z0-9._-]{0,63}` (no `:`; it sits in the envelope).
- Create one by hand: `head -c 32 /dev/urandom | base64 > 2026-10.key`, or as a Secret:
  `kubectl -n trove create secret generic trove-encryption-keys --from-file=2026-10.key=<(head -c 32 /dev/urandom | base64)`.
- There is no default key and no generated key. `Load` fails, and `trove serve` stops, when the list is empty,
  a file is missing, unreadable, neither 32 raw bytes nor base64 of 32 bytes, a key ID is listed twice, or the active key is
  not in the ring. The messages name the file or ID and never print key material.
- The ring is immutable after `Load`; a key change is a roll-out (the chart's pod checksum rolls the pods).

`ring.Status()` returns the key IDs, the active ID and which keys are loaded. `ring.StatusWithCounts(ctx, counter)`
adds the sealed values per key ID over every registered column; the PostgreSQL layer implements
`ValueCounter`. A key ID that values reference but the ring lacks is listed with `loaded: false`. Both the
future `trove admin secrets status` and `GET /api/v1/system/encryption` (M1) call these; neither may add key
material, fingerprints, ciphertext or plaintext. `KeyRing` implements `String` and `LogValue` with key IDs only.

## Envelope

```
v1:<key id>:<base64(nonce ‖ ciphertext ‖ tag)>
```

AES-256-GCM, random 96-bit nonce per seal. The additional authenticated data binds the value to its place:
the literal `trove/sealed/v1`, the table and the column, each length-prefixed (uvarint), then the number of
row-key parts and each part, length-prefixed. A row is named by `secrets.Row(parts...)`, one part per primary
key column in the registered order, so the parts are never joined into one string: `Row("a:b", "c")` and
`Row("a", "b:c")` are different rows with different AAD. A database writer who moves a ciphertext to another
row or column makes `Open` fail with `ErrAuthentication`. Errors: `ErrMalformed`, `ErrUnknownKey` (the
envelope names a key the ring lacks), `ErrAuthentication`, `ErrUnregistered`, `ErrRowKey` (the row key has a
different number of parts than the column has primary key columns; checked before any cryptography).

This layout replaced a single row-key string in the M0 fix round. Nothing was sealed in any database before
(no sealed column is registered in M0), so `Open` has no compatibility path for the old layout.

## Sealed-column registry

Every column that holds sealed values is registered once, at start-up, by the package that owns the table:

```go
var upstreamPassword = secrets.Register("remote_repositories", "id", "upstream_password")
var credential = secrets.Register("repository_credentials", "repository_id,name", "password") // composite key
```

`Seal` and `Open` take that `Column` handle; its fields are unexported, so a column outside the registry cannot
be sealed (`ErrUnregistered`) and there is no second list. Table, column and primary key columns must be
lower-case SQL identifiers; a composite primary key lists its columns separated by `,`
(`Column.PrimaryKeyColumns()` returns them). The per-key counts of the status and the
`secrets-reencrypt` job walk `secrets.Columns()`. `Register` panics on a duplicate and after `Freeze`, which
the composition root calls before it serves.

### How to add a sealed column

1. Add the column in the migration as `text`, and the table's primary key as usual.
2. In the owning package, register it with `secrets.Register(table, pkColumn, column)` and keep the handle.
3. Write with `ring.Seal(col, secrets.Row(id), plaintext)` inside the same transaction that writes the row;
   read with `ring.Open(col, secrets.Row(id), value)`. For a composite primary key pass one value per key
   column in the registered order: `secrets.Row(repoID, name)`. Never join the values yourself.
4. Implement the counting query for the column in the PostgreSQL layer's `ValueCounter` (group by the key ID
   prefix `split_part(col, ':', 2)`), and let the `secrets-reencrypt` job use `ring.Reseal`.
5. Add the table to the backup registry with the class that matches (the value is sealed, so `Data` is fine).
6. Hold the plaintext in an `observability.Secret` in memory and never log it.

## Rotation

Every replica must be able to open with a key before any replica seals with it. Two roll-outs, then
re-encryption (R-ARCH-021, R-HA-027):

| Step | Action | Checked with |
|---|---|---|
| 1 | add the new key file to the ring, `active_key_id` unchanged, roll out; the key is decrypt-only everywhere | `GET /api/v1/system/encryption`: the new ID in every replica's `keyIds`, `ringsCompatible: true` |
| 2 | set `active_key_id` to the new key, roll out; old and new replicas run side by side and open each other's values | |
| 3 | `POST /api/v1/system/tasks/secrets-reencrypt/run`: reseals every value not under the active key, idempotent | |
| 4 | when the status shows no values under the old ID, remove its file and roll out | |

Steps 1 to 4 of the API are M1 work; this package provides `Reseal` (`NeedsReseal`) and `Status`. The test
`TestRotation` runs the four steps against real key files.

## Canary and key mismatch

For each key ID, `secret_key_canary` holds a known plaintext sealed with that key. `ring.SealCanary(keyID)`
produces the row, `ring.CheckCanary(keyID, value)` verifies it, and `ring.RequireKeys("database", ids)` returns
`key ring does not match database: missing key <id>` (a `*MismatchError`; `"backup"` for a restore). The
table, the start-up check and the restore check are M1 and backup work; they call these functions. The
canary column is not in the registry of user secrets, so it never counts as a sealed value.

## Fingerprints

`ring.Fingerprint(value)` is HMAC-SHA-256 under a key derived with HKDF-SHA-256 from the active key (info
`trove fingerprint v1`), for configuration-as-code diffs. After a rotation of the active key every fingerprint
changes once.

## What is deliberately not here

- The `secret_key_canary` table, the start-up canary check and the replica-registry publication of key IDs
  (need PostgreSQL; M1).
- The `secrets-reencrypt` task and the `ringsCompatible` computation (need the job queue and the registry; M1).
- Key files are read once at `Load`; there is no live reload.
