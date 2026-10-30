-- Queries of the remote repository settings (R-REPO-024, R-REPO-033): the stored
-- secret fields of a repository. The repository package is a domain package and
-- imports no driver, so parameters are plain Go types: a reference travels as text
-- and is cast to jsonb here, and comes back as the generated model's raw JSON.

-- name: ListRepositorySecrets :many
-- A column that is NULL comes back as the empty text: exactly one of the two is set.
SELECT field, COALESCE(sealed, '')::text AS sealed, COALESCE(secret_ref::text, '')::text AS secret_ref
  FROM repository_secrets
 WHERE repo_id = sqlc.arg(repo_id)::bigint
 ORDER BY field;

-- name: UpsertSealedRepositorySecret :exec
-- The write of an inline value. The sealed text carries the repository id and the
-- field in its associated data, so the row cannot be moved without breaking it.
INSERT INTO repository_secrets (repo_id, field, sealed, secret_ref)
VALUES (sqlc.arg(repo_id)::bigint, sqlc.arg(field)::text, sqlc.arg(sealed)::text, NULL)
ON CONFLICT (repo_id, field) DO UPDATE
   SET sealed = EXCLUDED.sealed, secret_ref = NULL, updated_at = now();

-- name: UpsertReferenceRepositorySecret :exec
INSERT INTO repository_secrets (repo_id, field, sealed, secret_ref)
VALUES (sqlc.arg(repo_id)::bigint, sqlc.arg(field)::text, NULL, sqlc.arg(secret_ref)::text::jsonb)
ON CONFLICT (repo_id, field) DO UPDATE
   SET sealed = NULL, secret_ref = EXCLUDED.secret_ref, updated_at = now();

-- name: DeleteRepositorySecretsExcept :execrows
-- Removes the secret fields a document no longer carries. An empty list removes
-- all of them.
DELETE FROM repository_secrets
 WHERE repo_id = sqlc.arg(repo_id)::bigint
   AND NOT (field = ANY (sqlc.arg(keep)::text[]));

-- name: ListSealedRepositorySecretsAfter :many
-- One batch of the re-encryption walk (R-ARCH-021): the sealed values after a
-- (repo_id, field) cursor, locked so that a concurrent write waits.
SELECT repo_id, field, sealed::text AS sealed
  FROM repository_secrets
 WHERE sealed IS NOT NULL
   AND (repo_id, field) > (sqlc.arg(after_repo)::bigint, sqlc.arg(after_field)::text)
 ORDER BY repo_id, field
 LIMIT sqlc.arg(batch)::bigint
   FOR UPDATE;

-- name: UpdateSealedRepositorySecret :exec
UPDATE repository_secrets
   SET sealed = sqlc.arg(sealed)::text
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND field = sqlc.arg(field)::text;
