-- Queries of projects (R-AUTHZ-026). Identifiers are read as text and written
-- through text::uuid, so the domain package never imports the driver types
-- (R-ARCH-002). Statements that return at most one row are :many, so the caller
-- never meets the driver's no-rows error. Every write runs on the caller's
-- meta.Tx, in which it notifies the area authz (R-AUTHZ-028).

-- name: ProjectList :many
-- Every project with the counts the overview shows. A repository being deleted
-- still counts: it keeps its row until its content is gone (R-REPO-013). logical_bytes is
-- the project's charge (Q-STOR-155): the sum of the folded repository_usage rows of its
-- repositories, read by primary key; no item is scanned.
SELECT p.id::text AS id, p.key, p.display_name, p.description, p.version, p.visibility,
       (extract(epoch FROM p.created_at) * 1000000)::bigint AS created_us,
       (extract(epoch FROM p.updated_at) * 1000000)::bigint AS updated_us,
       COALESCE(cb.kind, '')::text AS created_by_kind, COALESCE(cb.name, '')::text AS created_by_name,
       COALESCE(ub.kind, '')::text AS updated_by_kind, COALESCE(ub.name, '')::text AS updated_by_name,
       (SELECT count(*) FROM repositories r WHERE r.project_id = p.id)::bigint AS repositories,
       (SELECT count(*) FROM service_account s WHERE s.project_id = p.id)::bigint AS service_accounts,
       (SELECT count(*) FROM authz_policy a WHERE a.project_id = p.id)::bigint AS policies,
       (SELECT count(*) FROM ci_mappings c WHERE c.project_id = p.id)::bigint AS ci_mappings,
       (SELECT COALESCE(sum(u.logical_bytes), 0) FROM repositories r JOIN repository_usage u ON u.repo_id = r.id
         WHERE r.project_id = p.id)::bigint AS logical_bytes,
       (SELECT count(*) FROM project_member m WHERE m.project_id = p.id)::bigint AS members
  FROM project p
  LEFT JOIN principal cb ON cb.id = p.created_by
  LEFT JOIN principal ub ON ub.id = p.updated_by
 ORDER BY p.key;

-- name: ProjectGet :many
SELECT p.id::text AS id, p.key, p.display_name, p.description, p.version, p.visibility,
       (extract(epoch FROM p.created_at) * 1000000)::bigint AS created_us,
       (extract(epoch FROM p.updated_at) * 1000000)::bigint AS updated_us,
       COALESCE(cb.kind, '')::text AS created_by_kind, COALESCE(cb.name, '')::text AS created_by_name,
       COALESCE(ub.kind, '')::text AS updated_by_kind, COALESCE(ub.name, '')::text AS updated_by_name,
       (SELECT count(*) FROM repositories r WHERE r.project_id = p.id)::bigint AS repositories,
       (SELECT count(*) FROM service_account s WHERE s.project_id = p.id)::bigint AS service_accounts,
       (SELECT count(*) FROM authz_policy a WHERE a.project_id = p.id)::bigint AS policies,
       (SELECT count(*) FROM ci_mappings c WHERE c.project_id = p.id)::bigint AS ci_mappings,
       (SELECT COALESCE(sum(u.logical_bytes), 0) FROM repositories r JOIN repository_usage u ON u.repo_id = r.id
         WHERE r.project_id = p.id)::bigint AS logical_bytes,
       (SELECT count(*) FROM project_member m WHERE m.project_id = p.id)::bigint AS members
  FROM project p
  LEFT JOIN principal cb ON cb.id = p.created_by
  LEFT JOIN principal ub ON ub.id = p.updated_by
 WHERE p.key = sqlc.arg(key)::text;

-- name: ProjectLock :many
-- Serialises the writers of one project and of its members: a delete counts
-- repositories and accounts under this lock; a move into or out of the project and
-- every change of the members take it before they write (the last-Owner rule counts
-- under it, Q-AUTHZ-217). A repository create takes it FOR SHARE. No row: no
-- project.
SELECT id::text AS id FROM project WHERE key = sqlc.arg(key)::text FOR UPDATE;

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

-- name: ProjectPolicyNames :many
-- The policies of a project by their stored name (<project key>/<name>,
-- Q-AUTHZ-209), for the delete that removes them through the policy store so that
-- each deletion is audited and journaled. The version comes with the name: the
-- delete passes it as If-Match, so a policy that changed since this read is not
-- removed (Q-AUTHZ-204).
SELECT name, version FROM authz_policy WHERE project_id = sqlc.arg(id)::text::uuid ORDER BY name;

-- name: ProjectMembers :many
-- The members of one project with their role (Q-AUTHZ-207).
SELECT p.id::text AS principal_id, p.kind AS principal_kind, p.name AS principal_name, m.role,
       coalesce(u.display_name, '')::text AS display_name,
       coalesce(encode(a.blob_sha256, 'hex'), '')::text AS avatar_sha256
  FROM project_member m
  JOIN project pr ON pr.id = m.project_id
  JOIN principal p ON p.id = m.principal_id
  LEFT JOIN app_user u ON u.principal_id = p.id
  LEFT JOIN user_avatars a ON a.user_id = p.id
 WHERE pr.key = sqlc.arg(key)::text
 ORDER BY p.kind, p.name;

-- name: ProjectMembersOfAll :many
-- The members of every project, for the overview.
SELECT pr.key AS project_key, p.id::text AS principal_id, p.kind AS principal_kind, p.name AS principal_name, m.role
  FROM project_member m
  JOIN project pr ON pr.id = m.project_id
  JOIN principal p ON p.id = m.principal_id
 ORDER BY pr.key, p.kind, p.name;

-- name: ProjectMemberAdd :execrows
-- Adds a user or a group (the trigger of 00019 refuses every other kind) as a
-- member with the role (Q-AUTHZ-207). The caller holds ProjectLock and read
-- ProjectMemberRole before, so 0 rows does not happen.
INSERT INTO project_member (project_id, principal_id, role)
VALUES (sqlc.arg(project_id)::text::uuid, sqlc.arg(principal_id)::text::uuid, sqlc.arg(role)::text)
ON CONFLICT DO NOTHING;

-- name: ProjectMemberSetRole :execrows
-- 0 rows: the principal is no member.
UPDATE project_member
   SET role = sqlc.arg(role)::text
 WHERE project_id = sqlc.arg(project_id)::text::uuid
   AND principal_id = sqlc.arg(principal_id)::text::uuid;

-- name: ProjectMemberRemove :execrows
-- 0 rows: the principal is no member.
DELETE FROM project_member
 WHERE project_id = sqlc.arg(project_id)::text::uuid
   AND principal_id = sqlc.arg(principal_id)::text::uuid;

-- name: ProjectMemberRole :many
-- The role of one principal in one project; no row: no member.
SELECT role
  FROM project_member
 WHERE project_id = sqlc.arg(project_id)::text::uuid AND principal_id = sqlc.arg(principal_id)::text::uuid;

-- name: ProjectOwnerCount :one
-- The membership rows of a project with the role owner, a group among them
-- whatever its members (Q-AUTHZ-217): the last one is neither removed nor demoted
-- (409 last_owner) unless the caller holds projects:write. Counted under
-- ProjectLock, so two Owners who remove each other run one after the other.
SELECT count(*)::bigint
  FROM project_member
 WHERE project_id = sqlc.arg(project_id)::text::uuid AND role = 'owner';

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
-- auto_join is true for an auto-join group, which is never a member: every user is
-- in one, so it would make every user a member (R-AUTHZ-026, 422
-- principal_not_allowed).
SELECT p.id::text AS id, p.kind, p.name, p.status, COALESCE(g.auto_join, false)::boolean AS auto_join
  FROM principal p
  LEFT JOIN user_group g ON g.principal_id = p.id
 WHERE p.kind = sqlc.arg(kind)::text AND p.name = sqlc.arg(name)::text;

-- name: ProjectGroupAutoJoinShare :many
-- The auto-join flag of a group under a shared row lock. Turning the flag on takes the
-- same row FOR UPDATE (IdentityLockGroup) and then looks for project memberships, so
-- the member write and that change run one after the other and the second sees what
-- the first left (R-AUTH-019, R-AUTHZ-026). No row for a principal that is no group.
SELECT auto_join FROM user_group WHERE principal_id = sqlc.arg(id)::text::uuid FOR SHARE;

-- name: ProjectRepositoryLock :many
-- The repository a move takes (R-REPO-060), by its project and key, under its row
-- lock: the move changes its project_id in the same transaction. Every lifecycle
-- state is returned; the caller answers a repository being deleted as missing.
SELECT r.id, r.key, r.class, r.format, r.lifecycle, r.config_version
  FROM repositories r
  JOIN project p ON p.id = r.project_id
 WHERE p.key = sqlc.arg(project)::text AND r.key = sqlc.arg(key)::text
   FOR UPDATE OF r;

-- name: ProjectRepositoryKeyTaken :one
-- Whether the project holds a repository with the key in any lifecycle state: a move
-- into it answers 409 already_exists (R-REPO-060). The caller holds ProjectLock of
-- the project, which a create in it takes FOR SHARE, so the answer holds until the
-- move commits.
SELECT EXISTS (SELECT 1 FROM repositories WHERE project_id = sqlc.arg(project_id)::text::uuid AND key = sqlc.arg(key)::text);

-- name: ProjectRepositoryMove :many
-- Moves a repository to another project (R-REPO-060, Q-REPO-217): its project_id
-- changes, its id and its content do not, and config_version moves on, because the
-- addresses its documents carry change. The condition names the source project and
-- an active repository, so two concurrent moves cannot both win; the callers hold
-- ProjectLock of both projects and ProjectRepositoryLock. A target that holds the
-- key answers SQLSTATE 23505 on repositories_project_key.
UPDATE repositories
   SET project_id = sqlc.arg(to_project_id)::text::uuid,
       config_version = config_version + 1,
       updated_at = now(),
       updated_by = NULLIF(sqlc.arg(actor)::text, '')::uuid
 WHERE id = sqlc.arg(id)::bigint AND project_id = sqlc.arg(from_project_id)::text::uuid AND lifecycle = 'active'
RETURNING config_version;

-- name: ProjectRepositoryReferences :many
-- What of the repository's own project names it, and keeps it from moving (R-REPO-060,
-- 409 repository_in_use): the virtual repositories that list it as a member, pin a
-- rule to it or deploy into it, and the policies of the project with a key selector
-- on it (by stored name). A virtual repository moves only without members and
-- deployment target, which are repositories of the project: they are listed as
-- `member` and `deployment_target`. Every virtual repository is of the same project
-- (trigger AI006 of 00024). The inline grants of the project's CI mappings are a
-- JSON document; ProjectCIMappingGrants reads them for the caller.
SELECT 'virtual_member'::text AS kind, v.key::text AS name
  FROM virtual_members m JOIN repositories v ON v.id = m.virtual_id
 WHERE m.member_id = sqlc.arg(id)::bigint
UNION
SELECT 'pin_rule', v.key
  FROM virtual_pin_members m JOIN repositories v ON v.id = m.virtual_id
 WHERE m.member_id = sqlc.arg(id)::bigint
UNION
SELECT 'virtual_deployment_target', v.key
  FROM virtual_deploy_targets t JOIN repositories v ON v.id = t.virtual_id
 WHERE t.target_id = sqlc.arg(id)::bigint
UNION
SELECT 'policy', a.name
  FROM authz_policy_selector s JOIN authz_policy a ON a.id = s.policy_id
 WHERE s.repository_id = sqlc.arg(id)::bigint AND a.project_id = sqlc.arg(project_id)::text::uuid
UNION
SELECT 'member', r.key
  FROM virtual_members m JOIN repositories r ON r.id = m.member_id
 WHERE m.virtual_id = sqlc.arg(id)::bigint
UNION
SELECT 'deployment_target', r.key
  FROM virtual_deploy_targets t JOIN repositories r ON r.id = t.target_id
 WHERE t.virtual_id = sqlc.arg(id)::bigint
ORDER BY 1, 2;

-- name: ProjectRepositoryGlobalPolicies :many
-- The global policies with a key selector on the repository (Q-REPO-232): their grant
-- is held by the repository's ID and would follow it into the target project, so an
-- Owner who moves it without repositories:write-config is refused while one exists
-- (409 repository_in_use, kind global_policy). A pattern or class selector matches by
-- address and is not stored with the ID.
SELECT DISTINCT a.name
  FROM authz_policy_selector s JOIN authz_policy a ON a.id = s.policy_id
 WHERE s.repository_id = sqlc.arg(id)::bigint AND a.project_id IS NULL
 ORDER BY a.name;

-- name: ProjectCIMappingGrants :many
-- The grant documents of the CI mappings of a project's tier, for the move, which
-- refuses a repository that an inline grant names by key (R-REPO-060). name is the
-- mapping's name as stored.
SELECT name, grants::text AS grants
  FROM ci_mappings
 WHERE project_id = sqlc.arg(project_id)::text::uuid
 ORDER BY name;
