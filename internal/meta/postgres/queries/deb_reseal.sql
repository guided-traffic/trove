-- The re-encryption walk over the sealed private keys of the Debian signing keys (R-ARCH-021, R-DEB-015). The statements of the signing
-- keys themselves are the work package of the keys (queries/deb_keys.sql).

-- name: ListSealedDebKeysAfter :many
-- One batch of the walk: the sealed private keys after an id cursor, locked so that a concurrent promotion or retirement waits. A
-- retired key has no private key and is not listed.
SELECT id::text AS id, private_key::text AS private_key
  FROM deb_signing_keys
 WHERE private_key IS NOT NULL
   AND id::text > sqlc.arg(after_id)::text
 ORDER BY id::text
 LIMIT sqlc.arg(batch)::bigint
   FOR UPDATE;

-- name: UpdateSealedDebKey :exec
UPDATE deb_signing_keys
   SET private_key = sqlc.arg(sealed)::text
 WHERE id = sqlc.arg(id)::text::uuid AND private_key IS NOT NULL;
