-- Queries of the visibility levels (R-AUTHZ-033, R-API-056, R-API-057). The installation's default is read in the transaction
-- that creates a project or a repository and cached nowhere; the cascade of a lowered project level runs under the project row
-- lock the caller holds.

-- name: VisibilityDefaultGet :many
-- The installation's default level; the migration inserts the row, so a read always finds it.
SELECT default_level, version FROM visibility_settings WHERE singleton;

-- name: VisibilityDefaultLock :many
-- The same row under its lock, for the write of the default (R-API-056).
SELECT default_level, version FROM visibility_settings WHERE singleton FOR UPDATE;

-- name: VisibilityDefaultSet :many
UPDATE visibility_settings
   SET default_level = sqlc.arg(level)::text, version = version + 1, updated_at = now(), updated_by = NULLIF(sqlc.arg(actor)::text, '')::uuid
 WHERE singleton
RETURNING version;

-- name: VisibilityPublicProjects :many
-- The projects whose level is public, in key order; one more than the cap tells the caller to set truncated.
SELECT key FROM project WHERE visibility = 'public' ORDER BY key COLLATE "C" LIMIT sqlc.arg(max)::int;

-- name: VisibilityPublicRepositories :many
-- The repositories whose artifacts are public, as <project>/<key> in key order; one being deleted is no longer served.
SELECT (p.key || '/' || r.key)::text AS name
  FROM repositories r JOIN project p ON p.id = r.project_id
 WHERE r.visibility_artifacts = 'public' AND r.lifecycle = 'active'
 ORDER BY p.key COLLATE "C", r.key COLLATE "C"
 LIMIT sqlc.arg(max)::int;

-- name: ProjectSetVisibility :exec
-- The level moves neither the version nor the ETag source (Q-AUTHZ-185): the PUT fields are display name and description.
UPDATE project
   SET visibility = sqlc.arg(level)::text, updated_at = now(), updated_by = NULLIF(sqlc.arg(actor)::text, '')::uuid
 WHERE id = sqlc.arg(id)::text::uuid;

-- name: ProjectLowerRepositories :many
-- The cascade of R-AUTHZ-033 rule 3: every repository of the project whose artifacts or wiki level lies above the level goes down
-- to it, a repository being deleted included (the database refuses a state above the project); the configuration version moves
-- on. before_* are the levels the repository had.
WITH due AS (
    SELECT r.id, r.key, r.visibility_artifacts AS before_artifacts, r.visibility_wiki AS before_wiki
      FROM repositories r
     WHERE r.project_id = sqlc.arg(project_id)::text::uuid
       AND (visibility_rank(r.visibility_artifacts) > visibility_rank(sqlc.arg(level)::text)
            OR visibility_rank(r.visibility_wiki) > visibility_rank(sqlc.arg(level)::text))
       FOR UPDATE OF r
)
UPDATE repositories r
   SET visibility_artifacts = CASE WHEN visibility_rank(r.visibility_artifacts) > visibility_rank(sqlc.arg(level)::text) THEN sqlc.arg(level)::text ELSE r.visibility_artifacts END,
       visibility_wiki      = CASE WHEN visibility_rank(r.visibility_wiki) > visibility_rank(sqlc.arg(level)::text) THEN sqlc.arg(level)::text ELSE r.visibility_wiki END,
       config_version = r.config_version + 1, updated_at = now(), updated_by = NULLIF(sqlc.arg(actor)::text, '')::uuid
  FROM due
 WHERE r.id = due.id
RETURNING r.id, r.key, r.config_version, due.before_artifacts, due.before_wiki, r.visibility_artifacts, r.visibility_wiki;

-- name: ProjectLevelByID :many
SELECT visibility FROM project WHERE id = sqlc.arg(id)::text::uuid;

-- name: ProjectLowerRepository :many
-- The lowering of a moved repository to the level of its target project (R-AUTHZ-033 rule 3); no row: nothing lay above it.
WITH due AS (
    SELECT r.id, r.visibility_artifacts AS before_artifacts, r.visibility_wiki AS before_wiki
      FROM repositories r
     WHERE r.id = sqlc.arg(id)::bigint
       AND (visibility_rank(r.visibility_artifacts) > visibility_rank(sqlc.arg(level)::text)
            OR visibility_rank(r.visibility_wiki) > visibility_rank(sqlc.arg(level)::text))
       FOR UPDATE OF r
)
UPDATE repositories r
   SET visibility_artifacts = CASE WHEN visibility_rank(r.visibility_artifacts) > visibility_rank(sqlc.arg(level)::text) THEN sqlc.arg(level)::text ELSE r.visibility_artifacts END,
       visibility_wiki      = CASE WHEN visibility_rank(r.visibility_wiki) > visibility_rank(sqlc.arg(level)::text) THEN sqlc.arg(level)::text ELSE r.visibility_wiki END,
       config_version = r.config_version + 1
  FROM due
 WHERE r.id = due.id
RETURNING r.config_version, due.before_artifacts, due.before_wiki, r.visibility_artifacts, r.visibility_wiki;
