-- Queries of the repository model (R-REPO-001 to R-REPO-013). The repository
-- package is a domain package and imports no driver, so parameters are plain Go
-- types: a principal id travels as text and is cast to uuid here, and a document
-- as text cast to jsonb. Rows come back as the generated Repository model; the
-- package reads its pgtype fields through their methods and never imports the
-- driver. Statements that return at most one row are :many, so the caller never
-- meets the driver's no-rows error.

-- name: ListRepositories :many
-- Every repository of the installation, by project key and key in byte order,
-- whatever the database's collation: the list's cursor is the last (project, key)
-- of a page compared in Go, byte by byte (R-REPO-012). Under en_US.UTF-8 the hyphen
-- weighs nothing at the first level, so `weba` sorted before `web-b` and the page
-- after `weba` skipped `web-b`.
SELECT r.*
  FROM repositories r
  JOIN project p ON p.id = r.project_id
 ORDER BY p.key COLLATE "C", r.key COLLATE "C";

-- name: ListProjectRepositories :many
-- The repositories of one project by key in byte order (Q-REPO-212).
SELECT r.*
  FROM repositories r
  JOIN project p ON p.id = r.project_id
 WHERE p.key = sqlc.arg(project)::text
 ORDER BY r.key COLLATE "C";

-- name: GetRepositoryByName :many
-- A repository is named by its project key and its key (Q-REPO-212).
SELECT r.*
  FROM repositories r
  JOIN project p ON p.id = r.project_id
 WHERE p.key = sqlc.arg(project)::text AND r.key = sqlc.arg(key)::text;

-- name: LockRepositoryByName :many
-- The row lock serialises the writers of one repository; a reader needs none.
SELECT r.*
  FROM repositories r
  JOIN project p ON p.id = r.project_id
 WHERE p.key = sqlc.arg(project)::text AND r.key = sqlc.arg(key)::text
   FOR UPDATE OF r;

-- name: GetRepositoryByID :many
SELECT *
  FROM repositories
 WHERE id = sqlc.arg(id)::bigint;

-- name: InsertRepository :many
-- A key that is taken in the project (also by a repository being deleted) conflicts
-- on the unique constraint repositories_project_key; ON CONFLICT DO NOTHING returns
-- no row and the caller answers already_exists. The project is required
-- (Q-AUTHZ-206): the repository is created in it, in this statement.
INSERT INTO repositories (key, format, class, config, project_id, created_by, updated_by)
VALUES (sqlc.arg(key)::text, sqlc.arg(format)::text, sqlc.arg(class)::text, sqlc.arg(config)::text::jsonb,
        sqlc.arg(project_id)::text::uuid, NULLIF(sqlc.arg(actor)::text, '')::uuid, NULLIF(sqlc.arg(actor)::text, '')::uuid)
ON CONFLICT (project_id, key) DO NOTHING
RETURNING *;

-- name: UpdateRepositoryConfig :many
-- The write of R-REPO-011. The caller holds the row lock and has compared the
-- version already; the version in the WHERE clause is the second guard.
UPDATE repositories
   SET config = sqlc.arg(config)::text::jsonb,
       config_version = config_version + 1,
       updated_at = now(),
       updated_by = NULLIF(sqlc.arg(actor)::text, '')::uuid
 WHERE id = sqlc.arg(id)::bigint AND config_version = sqlc.arg(version)::bigint AND lifecycle = 'active'
RETURNING *;

-- name: MarkRepositoryDeleting :many
-- The first transaction of R-REPO-013. The version moves on, because a client
-- holding the old ETag must not write over a repository being deleted.
UPDATE repositories
   SET lifecycle = 'deleting',
       config_version = config_version + 1,
       updated_at = now(),
       updated_by = NULLIF(sqlc.arg(actor)::text, '')::uuid
 WHERE id = sqlc.arg(id)::bigint AND lifecycle = 'active'
RETURNING *;

-- name: CountRepositoryItems :one
SELECT count(*)::bigint FROM items WHERE repo_id = sqlc.arg(id)::bigint;

-- name: DeleteRepositoryItemsBatch :execrows
-- One batch of the deletion job (R-REPO-013). Items go permanently, bypassing
-- the trash can; the statement trigger of 00002 queues their blobs for garbage
-- collection. Rows that other tables reference by item (properties, statistics)
-- cascade.
DELETE FROM items
 WHERE id IN (SELECT i.id FROM items i WHERE i.repo_id = sqlc.arg(id)::bigint ORDER BY i.id LIMIT sqlc.arg(batch)::int);

-- name: DeleteRepositoryRow :execrows
-- The last transaction of R-REPO-013: the key becomes free.
DELETE FROM repositories WHERE id = sqlc.arg(id)::bigint AND lifecycle = 'deleting';

-- name: RepositoryProjectKeys :many
-- Every project's id and key: the readers resolve project_id of the rows they hold
-- to the key (Q-REPO-212).
SELECT id::text AS id, key FROM project;

-- name: RepositoryProjectShare :many
-- The project a repository is created in or moved into, locked FOR SHARE: a concurrent
-- delete of the project (FOR UPDATE, which counts the repositories under that lock)
-- waits for this transaction, so the new repository is counted or the project is gone.
SELECT id::text AS id, key FROM project WHERE key = sqlc.arg(key)::text FOR SHARE;
