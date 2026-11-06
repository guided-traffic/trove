-- Queries of projects (R-AUTHZ-026). Identifiers are read as text and written
-- through text::uuid, so the domain package never imports the driver types
-- (R-ARCH-002). Statements that return at most one row are :many, so the caller
-- never meets the driver's no-rows error. Every write runs on the caller's
-- meta.Tx, in which it notifies the area authz (R-AUTHZ-028).

-- name: ProjectList :many
-- Every project with the counts the overview shows. A repository being deleted
-- still counts: it keeps its row until its content is gone (R-REPO-013).
SELECT p.id::text AS id, p.key, p.display_name, p.description, p.version,
       (extract(epoch FROM p.created_at) * 1000000)::bigint AS created_us,
       (extract(epoch FROM p.updated_at) * 1000000)::bigint AS updated_us,
       COALESCE(cb.kind, '')::text AS created_by_kind, COALESCE(cb.name, '')::text AS created_by_name,
       COALESCE(ub.kind, '')::text AS updated_by_kind, COALESCE(ub.name, '')::text AS updated_by_name,
       (SELECT count(*) FROM repositories r WHERE r.project_id = p.id)::bigint AS repositories,
       (SELECT count(*) FROM service_account s WHERE s.project_id = p.id)::bigint AS service_accounts,
       (SELECT count(*) FROM authz_policy a WHERE a.project_id = p.id)::bigint AS policies,
       (SELECT count(*) FROM ci_mappings c WHERE c.project_id = p.id)::bigint AS ci_mappings,
       (SELECT count(*) FROM project_member m WHERE m.project_id = p.id)::bigint AS members
  FROM project p
  LEFT JOIN principal cb ON cb.id = p.created_by
  LEFT JOIN principal ub ON ub.id = p.updated_by
 ORDER BY p.key;

-- name: ProjectGet :many
SELECT p.id::text AS id, p.key, p.display_name, p.description, p.version,
       (extract(epoch FROM p.created_at) * 1000000)::bigint AS created_us,
       (extract(epoch FROM p.updated_at) * 1000000)::bigint AS updated_us,
       COALESCE(cb.kind, '')::text AS created_by_kind, COALESCE(cb.name, '')::text AS created_by_name,
       COALESCE(ub.kind, '')::text AS updated_by_kind, COALESCE(ub.name, '')::text AS updated_by_name,
       (SELECT count(*) FROM repositories r WHERE r.project_id = p.id)::bigint AS repositories,
       (SELECT count(*) FROM service_account s WHERE s.project_id = p.id)::bigint AS service_accounts,
       (SELECT count(*) FROM authz_policy a WHERE a.project_id = p.id)::bigint AS policies,
       (SELECT count(*) FROM ci_mappings c WHERE c.project_id = p.id)::bigint AS ci_mappings,
       (SELECT count(*) FROM project_member m WHERE m.project_id = p.id)::bigint AS members
  FROM project p
  LEFT JOIN principal cb ON cb.id = p.created_by
  LEFT JOIN principal ub ON ub.id = p.updated_by
 WHERE p.key = sqlc.arg(key)::text;

-- name: ProjectLock :exec
-- Serialises the writers of one project and of its members: a delete counts
-- repositories and accounts under this lock; a repository create, a move into the
-- project, a project service account create and every change of the members take it
-- before they write (the last-owner rule counts under it).
SELECT 1 FROM project WHERE key = sqlc.arg(key)::text FOR UPDATE;

-- name: ProjectInsert :many
-- No row back means the key exists (409 already_exists).
INSERT INTO project (id, key, display_name, description, created_by, updated_by)
VALUES (sqlc.arg(id)::text::uuid, sqlc.arg(key)::text, sqlc.arg(display_name)::text, sqlc.arg(description)::text,
        NULLIF(sqlc.arg(actor)::text, '')::uuid, NULLIF(sqlc.arg(actor)::text, '')::uuid)
ON CONFLICT (key) DO NOTHING
RETURNING id::text AS id, version;

-- name: ProjectUpdate :many
-- The write that carries the optimistic check: a negative expected version is the
-- deliberate blind write (If-Match: *); a version that does not match returns no
-- row. The version covers the fields of the PUT only; members and
-- repositories do not move it (Q-AUTHZ-185).
UPDATE project
   SET display_name = sqlc.arg(display_name)::text, description = sqlc.arg(description)::text,
       version = version + 1, updated_at = now(), updated_by = NULLIF(sqlc.arg(actor)::text, '')::uuid
 WHERE key = sqlc.arg(key)::text
   AND (sqlc.arg(expected)::bigint < 0 OR version = sqlc.arg(expected)::bigint)
RETURNING id::text AS id, version;

-- name: ProjectDelete :many
-- The foreign keys of repositories and service_account refuse the delete while one
-- belongs to the project; the caller counts first and answers 409 project_not_empty. The
-- project's policies go with the row, after the caller deleted them one by one
-- through the policy store (Q-AUTHZ-185).
DELETE FROM project
 WHERE key = sqlc.arg(key)::text
   AND (sqlc.arg(expected)::bigint < 0 OR version = sqlc.arg(expected)::bigint)
RETURNING id::text AS id, version;

-- name: ProjectCountMembers :one
-- What keeps a project from being deleted: repositories in every lifecycle state, service accounts
-- (Q-AUTHZ-169) and CI mappings of the project's tier (Q-CI-26).
SELECT (SELECT count(*) FROM repositories WHERE project_id = sqlc.arg(id)::text::uuid)::bigint AS repositories,
       (SELECT count(*) FROM service_account WHERE project_id = sqlc.arg(id)::text::uuid)::bigint AS service_accounts,
       (SELECT count(*) FROM ci_mappings WHERE project_id = sqlc.arg(id)::text::uuid)::bigint AS ci_mappings;

-- name: ProjectPolicyNames :many
-- The policies of a project, for the delete that removes them through the policy
-- store so that each deletion is audited and journaled.
-- The version comes with the name: the delete passes it as If-Match, so a policy that
-- changed or was replaced under the same name since this read is not removed (Q-AUTHZ-202).
SELECT name, version FROM authz_policy WHERE project_id = sqlc.arg(id)::text::uuid ORDER BY name;

-- name: ProjectMembers :many
-- The members of one project with their role (Q-AUTHZ-207).
SELECT p.id::text AS principal_id, p.kind AS principal_kind, p.name AS principal_name, m.role
  FROM project_member m
  JOIN project pr ON pr.id = m.project_id
  JOIN principal p ON p.id = m.principal_id
 WHERE pr.key = sqlc.arg(key)::text
 ORDER BY p.kind, p.name;

-- name: ProjectMembersOfAll :many
-- The members of every project, for the overview.
SELECT pr.key AS project_key, p.id::text AS principal_id, p.kind AS principal_kind, p.name AS principal_name, m.role
  FROM project_member m
  JOIN project pr ON pr.id = m.project_id
  JOIN principal p ON p.id = m.principal_id
 ORDER BY pr.key, p.kind, p.name;

-- name: ProjectMemberships :many
-- The projects a set of principals (a user and its groups) is a member of, with the
-- role, for /authz/me and the project list. One row per principal and project.
SELECT pr.key AS project_key, pr.display_name, m.principal_id::text AS principal_id, m.role
  FROM project_member m
  JOIN project pr ON pr.id = m.project_id
 WHERE m.principal_id = ANY(sqlc.arg(principal_ids)::text[]::uuid[])
 ORDER BY pr.key;

-- name: ProjectMemberAdd :execrows
-- 0 rows: the principal is a member already (409 already_exists); the caller holds
-- ProjectLock.
INSERT INTO project_member (project_id, principal_id, role)
SELECT pr.id, sqlc.arg(principal_id)::text::uuid, sqlc.arg(role)::text FROM project pr WHERE pr.key = sqlc.arg(key)::text
ON CONFLICT DO NOTHING;

-- name: ProjectMemberSetRole :execrows
-- 0 rows: the principal is no member.
UPDATE project_member
   SET role = sqlc.arg(role)::text
 WHERE project_id = (SELECT id FROM project WHERE key = sqlc.arg(key)::text)
   AND principal_id = sqlc.arg(principal_id)::text::uuid;

-- name: ProjectMemberRemove :execrows
DELETE FROM project_member
 WHERE project_id = (SELECT id FROM project WHERE key = sqlc.arg(key)::text)
   AND principal_id = sqlc.arg(principal_id)::text::uuid;

-- name: ProjectMemberRole :many
-- The role of one principal in one project; no row: no member.
SELECT m.role
  FROM project_member m
  JOIN project pr ON pr.id = m.project_id
 WHERE pr.key = sqlc.arg(key)::text AND m.principal_id = sqlc.arg(principal_id)::text::uuid;

-- name: ProjectOwnerCount :one
-- The owners of a project: the last one is neither removed nor demoted (409
-- last_owner), counted under ProjectLock.
SELECT count(*)::bigint
  FROM project_member m
  JOIN project pr ON pr.id = m.project_id
 WHERE pr.key = sqlc.arg(key)::text AND m.role = 'owner';

-- name: ProjectRepositoryKeys :many
-- The repositories of a project, every lifecycle state, by key.
SELECT r.key, r.class, r.format, r.lifecycle
  FROM repositories r
  JOIN project p ON p.id = r.project_id
 WHERE p.key = sqlc.arg(key)::text
 ORDER BY r.key;

-- name: ProjectServiceAccountNames :many
SELECT pr.name -- the stored name, <project key>/<name>
  FROM service_account s
  JOIN principal pr ON pr.id = s.principal_id
  JOIN project p ON p.id = s.project_id
 WHERE p.key = sqlc.arg(key)::text
 ORDER BY pr.name;

-- name: ProjectLookupPrincipals :many
-- Users and groups whose name starts with the prefix, for a project owner
-- who names principals as members and in policies (Q-AUTHZ-182). Active principals only; the
-- answer is the kind and the name, nothing else. kind is empty, 'user' or 'group'.
SELECT kind, name
  FROM principal
 WHERE kind IN ('user', 'group') AND status = 'active'
   AND (sqlc.arg(kind)::text = '' OR kind = sqlc.arg(kind)::text)
   AND starts_with(name, sqlc.arg(prefix)::text)
 ORDER BY kind, name
 LIMIT sqlc.arg(max_rows)::int;

-- name: ProjectPrincipalByRef :many
-- The user or group a member reference names, read in the writer's
-- transaction (the cached principal store may lag a user created a moment ago).
SELECT id::text AS id, kind, name, status
  FROM principal
 WHERE kind = sqlc.arg(kind)::text AND name = sqlc.arg(name)::text;
