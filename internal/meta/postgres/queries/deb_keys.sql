-- The signing keys of the Debian (APT) format's local and virtual repositories (R-DEB-015 to R-DEB-018, R-DEB-032, Q-DEB-31). The
-- store of the keys (internal/formatdata/deb/keys_store.go) is the only user. A time travels as text and is cast to timestamptz here,
-- a principal id as text cast to uuid, the way the other format statements do. The re-encryption walk is queries/deb_reseal.sql.

-- name: DebNowText :one
-- The transaction's time, to the second as RFC 3339 text: the creation time of a generated key is this minus one hour (Q-DEB-30), and
-- an import is judged against it (R-DEB-015).
SELECT to_char(now() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')::text AS now;

-- name: DebRepoRef :many
-- The repository an operation names: its reference "<project>/<key>", its format and class. No row for an unknown id.
SELECT r.id, p.key::text AS project, (p.key || '/' || r.key)::text AS ref, r.format::text AS format, r.class::text AS class
  FROM repositories r
  JOIN project p ON p.id = r.project_id
 WHERE r.id = sqlc.arg(repo_id)::bigint;

-- name: DebKeysOfRepo :many
-- Every key of a repository in every state, oldest first, without its private part: times as RFC 3339 text in UTC, an absent time
-- or principal as the empty string. The secret is DebSealedKeys, read by the build alone.
SELECT id::text AS id, repo_id, fingerprint, algorithm::int AS algorithm, bits::int AS bits,
       to_char(key_created AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')::text AS key_created, to_char(bound_since AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')::text AS bound_since, state, origin, public_key,
       (private_key IS NOT NULL)::boolean AS has_private, to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')::text AS created_at, COALESCE(created_by::text, '')::text AS created_by,
       COALESCE(to_char(promoted_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'), '')::text AS promoted_at, COALESCE(to_char(withheld_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'), '')::text AS withheld_at, COALESCE(to_char(retired_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'), '')::text AS retired_at,
       COALESCE(retired_by::text, '')::text AS retired_by
  FROM deb_signing_keys
 WHERE repo_id = sqlc.arg(repo_id)::bigint
 ORDER BY created_at, id;

-- name: DebKeysOfRepoLocked :many
-- The same rows, locked: every change of a repository's keys waits for the one before it, so the partial unique indexes are never
-- the way two requests find out about each other.
SELECT id::text AS id, repo_id, fingerprint, algorithm::int AS algorithm, bits::int AS bits,
       to_char(key_created AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')::text AS key_created, to_char(bound_since AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')::text AS bound_since, state, origin, public_key,
       (private_key IS NOT NULL)::boolean AS has_private, to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')::text AS created_at, COALESCE(created_by::text, '')::text AS created_by,
       COALESCE(to_char(promoted_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'), '')::text AS promoted_at, COALESCE(to_char(withheld_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'), '')::text AS withheld_at, COALESCE(to_char(retired_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'), '')::text AS retired_at,
       COALESCE(retired_by::text, '')::text AS retired_by
  FROM deb_signing_keys
 WHERE repo_id = sqlc.arg(repo_id)::bigint
 ORDER BY created_at, id
   FOR UPDATE;

-- name: DebSealedKeys :many
-- The sealed private keys of the keys that sign (state primary or next), primary first (R-DEB-016). A key without a private part is
-- not listed.
SELECT id::text AS id, fingerprint, state, private_key::text AS sealed
  FROM deb_signing_keys
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND state IN ('primary', 'next') AND private_key IS NOT NULL
 ORDER BY (state = 'primary') DESC, created_at, id;

-- name: DebFingerprintKnown :one
-- Whether any repository holds the fingerprint in any state: a retired or withheld key cannot come back by import (Q-DEB-31).
SELECT EXISTS (SELECT 1 FROM deb_signing_keys WHERE fingerprint = sqlc.arg(fingerprint)::text) AS known;

-- name: DebInsertKey :exec
-- A new key. A primary key is promoted at insertion; a next key is not. private_key is the sealed secret.
INSERT INTO deb_signing_keys (id, repo_id, fingerprint, algorithm, bits, key_created, bound_since, state, origin, public_key,
                              private_key, created_by, promoted_at)
VALUES (sqlc.arg(id)::text::uuid, sqlc.arg(repo_id)::bigint, sqlc.arg(fingerprint)::text, sqlc.arg(algorithm)::smallint,
        sqlc.arg(bits)::int, sqlc.arg(key_created)::text::timestamptz, sqlc.arg(bound_since)::text::timestamptz,
        sqlc.arg(state)::text, sqlc.arg(origin)::text, sqlc.arg(public_key)::text, sqlc.arg(private_key)::text,
        NULLIF(sqlc.arg(created_by)::text, '')::uuid,
        CASE WHEN sqlc.arg(state)::text = 'primary' THEN now() END);

-- name: DebSetKeyState :execrows
-- Moves a key that is not retired to a state without erasing anything: promotion (primary), reinstatement (primary or next) and the
-- replay's withholding. withheld_at is set exactly for the withheld state, promoted_at at each entry into primary.
UPDATE deb_signing_keys
   SET state = sqlc.arg(state)::text,
       promoted_at = CASE WHEN sqlc.arg(state)::text = 'primary' THEN now() ELSE promoted_at END,
       withheld_at = CASE WHEN sqlc.arg(state)::text = 'withheld' THEN now() ELSE NULL END
 WHERE id = sqlc.arg(id)::text::uuid AND state <> 'retired';

-- name: DebRetireKey :execrows
-- Retires a key: the private key is erased, the public certificate stays so that the record of which key signed which generation
-- stays readable (R-DEB-015).
UPDATE deb_signing_keys
   SET state = 'retired', private_key = NULL, retired_at = now(), withheld_at = NULL,
       retired_by = NULLIF(sqlc.arg(retired_by)::text, '')::uuid
 WHERE id = sqlc.arg(id)::text::uuid AND state <> 'retired';

-- name: DebRaiseChangeSeq :execrows
-- A key change raises change_seq of every suite of the repository, so that the next build re-signs (R-DEB-017, R-DEB-032).
UPDATE deb_suites SET change_seq = change_seq + 1 WHERE repo_id = sqlc.arg(repo_id)::bigint;

-- name: DebRepoRefLocked :many
-- The same row, locked against a concurrent change of the repository's keys: every write operation starts here, so the statements
-- after it (READ COMMITTED) see what the operation before it committed, and a request that lost the race finds the next key it
-- would have created.
SELECT r.id, p.key::text AS project, (p.key || '/' || r.key)::text AS ref, r.format::text AS format, r.class::text AS class
  FROM repositories r
  JOIN project p ON p.id = r.project_id
 WHERE r.id = sqlc.arg(repo_id)::bigint
   FOR NO KEY UPDATE OF r;
