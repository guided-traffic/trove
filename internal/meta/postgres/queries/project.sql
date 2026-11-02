-- Queries of projects (R-AUTHZ-026). Identifiers are read as text and written
-- through text::uuid, so the domain package never imports the driver types
-- (R-ARCH-002). Statements that return at most one row are :many, so the caller
-- never meets the driver's no-rows error. Every write runs on the caller's
-- meta.Tx, in which it notifies the area authz (R-AUTHZ-028).

-- name: ProjectList :many
-- Every project with the counts the overview shows. A repository being deleted
-- still counts: it keeps its row until its content is gone (R-REPO-013).
SELECT p.id::text AS id, p.key, p.display_name, p.description, p.manage_policies, p.manage_repositories, p.version,
       (extract(epoch FROM p.created_at) * 1000000)::bigint AS created_us,
       (extract(epoch FROM p.updated_at) * 1000000)::bigint AS updated_us,
       COALESCE(cb.kind, '')::text AS created_by_kind, COALESCE(cb.name, '')::text AS created_by_name,
       COALESCE(ub.kind, '')::text AS updated_by_kind, COALESCE(ub.name, '')::text AS updated_by_name,
       (SELECT count(*) FROM repositories r WHERE r.project_id = p.id)::bigint AS repositories,
       (SELECT count(*) FROM service_account s WHERE s.project_id = p.id)::bigint AS service_accounts,
       (SELECT count(*) FROM authz_policy a WHERE a.project_id = p.id)::bigint AS policies
  FROM project p
  LEFT JOIN principal cb ON cb.id = p.created_by
  LEFT JOIN principal ub ON ub.id = p.updated_by
 ORDER BY p.key;

-- name: ProjectGet :many
SELECT p.id::text AS id, p.key, p.display_name, p.description, p.manage_policies, p.manage_repositories, p.version,
       (extract(epoch FROM p.created_at) * 1000000)::bigint AS created_us,
       (extract(epoch FROM p.updated_at) * 1000000)::bigint AS updated_us,
       COALESCE(cb.kind, '')::text AS created_by_kind, COALESCE(cb.name, '')::text AS created_by_name,
       COALESCE(ub.kind, '')::text AS updated_by_kind, COALESCE(ub.name, '')::text AS updated_by_name,
       (SELECT count(*) FROM repositories r WHERE r.project_id = p.id)::bigint AS repositories,
       (SELECT count(*) FROM service_account s WHERE s.project_id = p.id)::bigint AS service_accounts,
       (SELECT count(*) FROM authz_policy a WHERE a.project_id = p.id)::bigint AS policies
  FROM project p
  LEFT JOIN principal cb ON cb.id = p.created_by
  LEFT JOIN principal ub ON ub.id = p.updated_by
 WHERE p.key = sqlc.arg(key)::text;

-- name: ProjectLock :exec
-- Serialises the writers of one project and of its assignments: a delete counts
-- repositories and accounts under this lock, an assignment and a project service
-- account create take it before they write.
SELECT 1 FROM project WHERE key = sqlc.arg(key)::text FOR UPDATE;

-- name: ProjectInsert :many
-- No row back means the key exists (409 already_exists).
INSERT INTO project (id, key, display_name, description, manage_policies, manage_repositories, created_by, updated_by)
VALUES (sqlc.arg(id)::text::uuid, sqlc.arg(key)::text, sqlc.arg(display_name)::text, sqlc.arg(description)::text,
        sqlc.arg(manage_policies)::boolean, sqlc.arg(manage_repositories)::boolean,
        NULLIF(sqlc.arg(actor)::text, '')::uuid, NULLIF(sqlc.arg(actor)::text, '')::uuid)
ON CONFLICT (key) DO NOTHING
RETURNING id::text AS id, version;

-- name: ProjectUpdate :many
-- The write that carries the optimistic check: a negative expected version is the
-- deliberate blind write (If-Match: *); a version that does not match returns no
-- row. The version covers the fields of the PUT only; administrators and
-- assignments do not move it (Q-AUTHZ-185).
UPDATE project
   SET display_name = sqlc.arg(display_name)::text, description = sqlc.arg(description)::text,
       manage_policies = sqlc.arg(manage_policies)::boolean, manage_repositories = sqlc.arg(manage_repositories)::boolean,
       version = version + 1, updated_at = now(), updated_by = NULLIF(sqlc.arg(actor)::text, '')::uuid
 WHERE key = sqlc.arg(key)::text
   AND (sqlc.arg(expected)::bigint < 0 OR version = sqlc.arg(expected)::bigint)
RETURNING id::text AS id, version;

-- name: ProjectDelete :many
-- The foreign keys of repositories and service_account refuse the delete while one
-- is assigned; the caller counts first and answers 409 project_not_empty. The
-- project's policies go with the row, after the caller deleted them one by one
-- through the policy store (Q-AUTHZ-185).
DELETE FROM project
 WHERE key = sqlc.arg(key)::text
   AND (sqlc.arg(expected)::bigint < 0 OR version = sqlc.arg(expected)::bigint)
RETURNING id::text AS id, version;

-- name: ProjectVersion :many
SELECT version FROM project WHERE key = sqlc.arg(key)::text;

-- name: ProjectCountMembers :one
-- What keeps a project from being deleted: repositories in every lifecycle state
-- and service accounts (Q-AUTHZ-169).
SELECT (SELECT count(*) FROM repositories WHERE project_id = sqlc.arg(id)::text::uuid)::bigint AS repositories,
       (SELECT count(*) FROM service_account WHERE project_id = sqlc.arg(id)::text::uuid)::bigint AS service_accounts;

-- name: ProjectPolicyNames :many
-- The policies of a project, for the delete that removes them through the policy
-- store so that each deletion is audited and journaled.
SELECT name FROM authz_policy WHERE project_id = sqlc.arg(id)::text::uuid ORDER BY name;

-- name: ProjectAdmins :many
-- The administrators of one project.
SELECT p.id::text AS principal_id, p.kind AS principal_kind, p.name AS principal_name
  FROM project_admin a
  JOIN project pr ON pr.id = a.project_id
  JOIN principal p ON p.id = a.principal_id
 WHERE pr.key = sqlc.arg(key)::text
 ORDER BY p.kind, p.name;

-- name: ProjectAdminsOfAll :many
-- The administrators of every project, for the overview.
SELECT pr.key AS project_key, p.id::text AS principal_id, p.kind AS principal_kind, p.name AS principal_name
  FROM project_admin a
  JOIN project pr ON pr.id = a.project_id
  JOIN principal p ON p.id = a.principal_id
 ORDER BY pr.key, p.kind, p.name;

-- name: ProjectAddAdmin :execrows
-- 0 rows: the principal already administers the project.
INSERT INTO project_admin (project_id, principal_id)
SELECT pr.id, sqlc.arg(principal_id)::text::uuid FROM project pr WHERE pr.key = sqlc.arg(key)::text
ON CONFLICT DO NOTHING;

-- name: ProjectRemoveAdmin :execrows
DELETE FROM project_admin
 WHERE project_id = (SELECT id FROM project WHERE key = sqlc.arg(key)::text)
   AND principal_id = sqlc.arg(principal_id)::text::uuid;

-- name: ProjectOfRepository :many
-- The project a repository is assigned to; no row for an unknown repository, empty
-- project fields for one that belongs to no project.
SELECT r.id AS repository_id, COALESCE(p.id::text, '')::text AS project_id, COALESCE(p.key, '')::text AS project_key
  FROM repositories r
  LEFT JOIN project p ON p.id = r.project_id
 WHERE r.key = sqlc.arg(repository_key)::text;

-- name: ProjectAssignRepository :many
-- Sets the project of a repository. The condition makes the move explicit: the
-- row changes only when it belongs to no project or to the one in `from`
-- (empty: a repository without a project), so two concurrent assignments cannot
-- both win. No row back: the repository is unknown or belongs to another project
-- (409 repository_assigned). project_id empty unassigns. config_version moves on:
-- the project is a member of the repository's document, so a client that holds
-- the old ETag must not write over, or be answered 304 for, the other state.
UPDATE repositories
   SET project_id = NULLIF(sqlc.arg(project_id)::text, '')::uuid, updated_at = now(),
       config_version = config_version + 1,
       updated_by = NULLIF(sqlc.arg(actor)::text, '')::uuid
 WHERE key = sqlc.arg(repository_key)::text
   AND project_id IS NOT DISTINCT FROM NULLIF(sqlc.arg(from_project_id)::text, '')::uuid
RETURNING id AS repository_id, config_version;

-- name: ProjectRepositoryKeys :many
-- The repositories of a project, every lifecycle state, by key.
SELECT r.key, r.class, r.format, r.lifecycle
  FROM repositories r
  JOIN project p ON p.id = r.project_id
 WHERE p.key = sqlc.arg(key)::text
 ORDER BY r.key;

-- name: ProjectServiceAccountNames :many
SELECT pr.name
  FROM service_account s
  JOIN principal pr ON pr.id = s.principal_id
  JOIN project p ON p.id = s.project_id
 WHERE p.key = sqlc.arg(key)::text
 ORDER BY pr.name;

-- name: ProjectKeysOverlapping :many
-- The other project keys that overlap a key: one that is the other's prefix up to
-- a hyphen, either way round (`web` and `web-app`). Such keys would let the
-- administrator of `web` create `web-app-…` names that read as another project's
-- (Q-AUTHZ-176).
SELECT key FROM project
 WHERE key <> sqlc.arg(key)::text
   AND (starts_with(key, sqlc.arg(key)::text || '-') OR starts_with(sqlc.arg(key)::text, key || '-'))
 ORDER BY key;

-- name: ProjectKeysPrefixing :many
-- The keys of the projects whose key plus a hyphen starts a name: the names a
-- global service account may not take (Q-AUTHZ-176).
SELECT key FROM project
 WHERE starts_with(sqlc.arg(name)::text, key || '-')
 ORDER BY key;

-- name: ProjectGlobalAccountsWithPrefix :many
-- The global service accounts whose name starts with <key>-: a project of that key
-- is refused while one exists (Q-AUTHZ-176).
SELECT pr.name
  FROM principal pr
  JOIN service_account s ON s.principal_id = pr.id
 WHERE s.project_id IS NULL AND starts_with(pr.name, sqlc.arg(key)::text || '-')
 ORDER BY pr.name
 LIMIT 20;

-- name: ProjectLookupPrincipals :many
-- Users and groups whose name starts with the prefix, for a project administrator
-- who names principals in policies (Q-AUTHZ-182). Active principals only; the
-- answer is the kind and the name, nothing else. kind is empty, 'user' or 'group'.
SELECT kind, name
  FROM principal
 WHERE kind IN ('user', 'group') AND status = 'active'
   AND (sqlc.arg(kind)::text = '' OR kind = sqlc.arg(kind)::text)
   AND starts_with(name, sqlc.arg(prefix)::text)
 ORDER BY kind, name
 LIMIT sqlc.arg(max_rows)::int;

-- name: ProjectPrincipalByRef :many
-- The user or group an administrator reference names, read in the writer's
-- transaction (the cached principal store may lag a user created a moment ago).
SELECT id::text AS id, kind, name, status
  FROM principal
 WHERE kind = sqlc.arg(kind)::text AND name = sqlc.arg(name)::text;

-- name: ProjectsOfPrincipals :many
-- The projects administered by any of the principals (a subject and its groups),
-- for the callers that decide outside an index, such as a job.
SELECT p.key, p.manage_policies, p.manage_repositories
  FROM project p
 WHERE EXISTS (SELECT 1 FROM project_admin a
                WHERE a.project_id = p.id AND a.principal_id = ANY (sqlc.arg(principal_ids)::text[]::uuid[]))
 ORDER BY p.key;
