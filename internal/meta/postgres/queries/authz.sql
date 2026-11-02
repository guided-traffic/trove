-- Queries of the authorization store (R-AUTHZ-021, R-AUTHZ-022, R-AUTHZ-028).
--
-- Identifiers are read as text and written through text::uuid, and instants are
-- read as microseconds since the epoch, so that the domain package never
-- imports the driver types (R-ARCH-002). The writes run on the caller's
-- meta.Tx; the statements that read the inputs of the grant index run inside
-- the one REPEATABLE READ READ ONLY transaction of the build (R-AUTHZ-028).

-- name: AuthzBuildPolicies :many
-- project_id is empty for a global policy; a project policy covers only the
-- repositories of its project (R-AUTHZ-026).
SELECT id::text AS id, name, resource_type, COALESCE(project_id::text, '')::text AS project_id FROM authz_policy ORDER BY name;

-- name: AuthzBuildSelectors :many
SELECT policy_id::text AS policy_id, ordinal, kind, COALESCE(repository_id, 0)::bigint AS repository_id,
       COALESCE(key_pattern, '')::text AS key_pattern, COALESCE(repo_class, '')::text AS repo_class, COALESCE(format, '')::text AS format
  FROM authz_policy_selector ORDER BY policy_id, ordinal;

-- name: AuthzBuildPatterns :many
SELECT policy_id::text AS policy_id, dimension, mode, pattern
  FROM authz_policy_pattern ORDER BY policy_id, dimension, mode, pattern;

-- name: AuthzBuildEntries :many
-- The grants of global policies and, from the same statement, those of project
-- policies, which live in their own table so that the binaries of the previous
-- release, which read only authz_policy_entry, see a project policy without
-- entries (Q-AUTHZ-174).
SELECT e.policy_id::text AS policy_id, e.principal_id::text AS principal_id, p.kind AS principal_kind,
       p.name AS principal_name, e.actions
  FROM authz_policy_entry e
  JOIN principal p ON p.id = e.principal_id
UNION ALL
SELECT e.policy_id::text, e.principal_id::text, p.kind, p.name, e.actions
  FROM authz_project_policy_entry e
  JOIN principal p ON p.id = e.principal_id
 ORDER BY 1, 2;

-- name: AuthzBuildProjects :many
SELECT id::text AS id, key, manage_policies, manage_repositories FROM project ORDER BY key;

-- name: AuthzBuildProjectAdmins :many
-- The administrators of every project, users and groups (Q-AUTHZ-183).
SELECT a.project_id::text AS project_id, a.principal_id::text AS principal_id, p.kind AS principal_kind, p.name AS principal_name
  FROM project_admin a
  JOIN principal p ON p.id = a.principal_id
 ORDER BY a.project_id, a.principal_id;

-- name: AuthzProjectByKey :many
-- The ID of a project by its key; no row when it does not exist (R-AUTHZ-026).
SELECT id::text AS id FROM project WHERE key = sqlc.arg(key)::text;

-- name: AuthzServiceAccountProject :many
-- The key of the project a service account belongs to, empty for a global account;
-- no row for a principal that is no service account (Q-AUTHZ-175).
SELECT COALESCE(pr.key, '')::text AS project
  FROM service_account s
  LEFT JOIN project pr ON pr.id = s.project_id
 WHERE s.principal_id = sqlc.arg(principal_id)::text::uuid;

-- name: AuthzBuildBindings :many
SELECT role, principal_id::text AS principal_id FROM authz_role_binding ORDER BY role, principal_id;

-- name: AuthzBuildRepositories :many
-- project_id is empty for a repository that belongs to no project.
SELECT id, key, class, format, config, COALESCE(project_id::text, '')::text AS project_id
  FROM repositories WHERE lifecycle = 'active' ORDER BY id;

-- name: AuthzGetPolicy :many
SELECT p.id::text AS id, p.name, p.description, p.resource_type, p.version,
       COALESCE(p.project_id::text, '')::text AS project_id, COALESCE(pr.key, '')::text AS project_key,
       (extract(epoch FROM p.created_at) * 1000000)::bigint AS created_us,
       (extract(epoch FROM p.updated_at) * 1000000)::bigint AS updated_us,
       COALESCE(cb.kind, '')::text AS created_by_kind, COALESCE(cb.name, '')::text AS created_by_name,
       COALESCE(ub.kind, '')::text AS updated_by_kind, COALESCE(ub.name, '')::text AS updated_by_name
  FROM authz_policy p
  LEFT JOIN project pr ON pr.id = p.project_id
  LEFT JOIN principal cb ON cb.id = p.created_by
  LEFT JOIN principal ub ON ub.id = p.updated_by
 WHERE p.name = sqlc.arg(name)::text;

-- name: AuthzListPolicies :many
SELECT p.id::text AS id, p.name, p.description, p.resource_type, p.version,
       COALESCE(p.project_id::text, '')::text AS project_id, COALESCE(pr.key, '')::text AS project_key,
       (extract(epoch FROM p.created_at) * 1000000)::bigint AS created_us,
       (extract(epoch FROM p.updated_at) * 1000000)::bigint AS updated_us,
       COALESCE(cb.kind, '')::text AS created_by_kind, COALESCE(cb.name, '')::text AS created_by_name,
       COALESCE(ub.kind, '')::text AS updated_by_kind, COALESCE(ub.name, '')::text AS updated_by_name
  FROM authz_policy p
  LEFT JOIN project pr ON pr.id = p.project_id
  LEFT JOIN principal cb ON cb.id = p.created_by
  LEFT JOIN principal ub ON ub.id = p.updated_by
 ORDER BY p.name;

-- name: AuthzPolicySelectors :many
SELECT s.ordinal, s.kind, COALESCE(s.repository_id, 0)::bigint AS repository_id, COALESCE(r.key, '')::text AS repository_key,
       COALESCE(s.key_pattern, '')::text AS key_pattern, COALESCE(s.repo_class, '')::text AS repo_class, COALESCE(s.format, '')::text AS format
  FROM authz_policy_selector s
  LEFT JOIN repositories r ON r.id = s.repository_id
 WHERE s.policy_id = sqlc.arg(policy_id)::text::uuid
 ORDER BY s.ordinal;

-- name: AuthzPolicyPatterns :many
SELECT dimension, mode, pattern FROM authz_policy_pattern
 WHERE policy_id = sqlc.arg(policy_id)::text::uuid
 ORDER BY dimension, mode, pattern;

-- name: AuthzPolicyEntries :many
-- The entries of a policy of either kind: a global policy has its rows in
-- authz_policy_entry, a project policy in authz_project_policy_entry.
SELECT e.principal_id::text AS principal_id, p.kind AS principal_kind, p.name AS principal_name, e.actions
  FROM authz_policy_entry e
  JOIN principal p ON p.id = e.principal_id
 WHERE e.policy_id = sqlc.arg(policy_id)::text::uuid
UNION ALL
SELECT e.principal_id::text, p.kind, p.name, e.actions
  FROM authz_project_policy_entry e
  JOIN principal p ON p.id = e.principal_id
 WHERE e.policy_id = sqlc.arg(policy_id)::text::uuid
 ORDER BY 2, 3;

-- name: AuthzInsertPolicy :many
-- No row back means the name exists (409). project_id is empty for a global policy;
-- it is never changed afterwards.
INSERT INTO authz_policy (id, name, description, resource_type, project_id, created_by, updated_by)
VALUES (sqlc.arg(id)::text::uuid, sqlc.arg(name)::text, sqlc.arg(description)::text, sqlc.arg(resource_type)::text,
        NULLIF(sqlc.arg(project_id)::text, '')::uuid, NULLIF(sqlc.arg(actor)::text, '')::uuid, NULLIF(sqlc.arg(actor)::text, '')::uuid)
ON CONFLICT (name) DO NOTHING
RETURNING version;

-- name: AuthzUpdatePolicy :many
-- The update that carries the optimistic check: a negative expected version is the
-- deliberate blind write (If-Match: *); a version that does not match returns no row.
UPDATE authz_policy
   SET description = sqlc.arg(description)::text, resource_type = sqlc.arg(resource_type)::text,
       version = version + 1, updated_at = now(), updated_by = NULLIF(sqlc.arg(actor)::text, '')::uuid
 WHERE name = sqlc.arg(name)::text
   AND (sqlc.arg(expected)::bigint < 0 OR version = sqlc.arg(expected)::bigint)
RETURNING id::text AS id, version;

-- name: AuthzDeletePolicy :many
DELETE FROM authz_policy
 WHERE name = sqlc.arg(name)::text
   AND (sqlc.arg(expected)::bigint < 0 OR version = sqlc.arg(expected)::bigint)
RETURNING id::text AS id, version;

-- name: AuthzLockPolicy :exec
-- Locks the row before the before-state is read, so that the state the revocation
-- journal records is the one the write replaces (R-AUTHZ-031): two blind writes
-- (If-Match: *) run one after the other.
SELECT 1 FROM authz_policy WHERE name = sqlc.arg(name)::text FOR UPDATE;

-- name: AuthzPolicyVersion :many
SELECT version FROM authz_policy WHERE name = sqlc.arg(name)::text;

-- name: AuthzInsertSelector :exec
INSERT INTO authz_policy_selector (policy_id, ordinal, kind, repository_id, key_pattern, repo_class, format)
VALUES (sqlc.arg(policy_id)::text::uuid, sqlc.arg(ordinal)::smallint, sqlc.arg(kind)::text, NULLIF(sqlc.arg(repository_id)::bigint, 0),
        NULLIF(sqlc.arg(key_pattern)::text, ''), NULLIF(sqlc.arg(repo_class)::text, ''), NULLIF(sqlc.arg(format)::text, ''));

-- name: AuthzInsertProjectEntry :exec
-- The entry of a project policy; the foreign key to (policy, project) refuses a
-- project that is not the policy's.
INSERT INTO authz_project_policy_entry (policy_id, project_id, principal_id, actions)
VALUES (sqlc.arg(policy_id)::text::uuid, sqlc.arg(project_id)::text::uuid, sqlc.arg(principal_id)::text::uuid, sqlc.arg(actions)::text[]);

-- name: AuthzInsertPattern :exec
INSERT INTO authz_policy_pattern (policy_id, dimension, mode, pattern)
VALUES (sqlc.arg(policy_id)::text::uuid, sqlc.arg(dimension)::text, sqlc.arg(mode)::text, sqlc.arg(pattern)::text);

-- name: AuthzInsertEntry :exec
INSERT INTO authz_policy_entry (policy_id, principal_id, actions)
VALUES (sqlc.arg(policy_id)::text::uuid, sqlc.arg(principal_id)::text::uuid, sqlc.arg(actions)::text[]);

-- name: AuthzClearPolicyChildren :exec
-- The children of an updated policy are replaced as a whole (R-AUTHZ-021: updates are full replacements).
WITH s AS (DELETE FROM authz_policy_selector WHERE policy_id = sqlc.arg(policy_id)::text::uuid),
     p AS (DELETE FROM authz_policy_pattern WHERE policy_id = sqlc.arg(policy_id)::text::uuid),
     pe AS (DELETE FROM authz_project_policy_entry WHERE policy_id = sqlc.arg(policy_id)::text::uuid)
DELETE FROM authz_policy_entry WHERE policy_id = sqlc.arg(policy_id)::text::uuid;

-- name: AuthzInsertBinding :execrows
INSERT INTO authz_role_binding (role, principal_id, granted_by)
VALUES (sqlc.arg(role)::text, sqlc.arg(principal_id)::text::uuid, NULLIF(sqlc.arg(actor)::text, '')::uuid)
ON CONFLICT (role, principal_id) DO NOTHING;

-- name: AuthzDeleteBinding :execrows
DELETE FROM authz_role_binding WHERE role = sqlc.arg(role)::text AND principal_id = sqlc.arg(principal_id)::text::uuid;

-- name: AuthzDeleteAPIBinding :execrows
-- The API's unbind: a binding the configuration keeps stays (R-AUTHZ-010,
-- Q-AUTHZ-170), so the source is part of the condition and a concurrent trove
-- migrate that takes the binding over cannot lose it.
DELETE FROM authz_role_binding
 WHERE role = sqlc.arg(role)::text AND principal_id = sqlc.arg(principal_id)::text::uuid AND source = 'api';

-- name: AuthzBindingSource :many
-- The source of one binding; no row when it does not exist.
SELECT source FROM authz_role_binding WHERE role = sqlc.arg(role)::text AND principal_id = sqlc.arg(principal_id)::text::uuid;

-- name: AuthzInsertDeclaredBinding :execrows
-- A binding of authz.role_bindings that does not exist yet (Q-AUTHZ-170). It has
-- no granting principal: the configuration granted it.
INSERT INTO authz_role_binding (role, principal_id, source)
VALUES (sqlc.arg(role)::text, sqlc.arg(principal_id)::text::uuid, 'configuration')
ON CONFLICT (role, principal_id) DO NOTHING;

-- name: AuthzAdoptBinding :execrows
-- A declared binding that the API made earlier: the configuration keeps it from
-- now on, with its grant time and granting principal (Q-AUTHZ-170).
UPDATE authz_role_binding SET source = 'configuration'
 WHERE role = sqlc.arg(role)::text AND principal_id = sqlc.arg(principal_id)::text::uuid AND source <> 'configuration';

-- name: AuthzDeclaredBindings :many
-- The bindings the configuration keeps, for trove migrate to remove the ones no
-- longer declared (Q-AUTHZ-170).
SELECT b.role, b.principal_id::text AS principal_id, p.kind AS principal_kind, p.name AS principal_name
  FROM authz_role_binding b
  JOIN principal p ON p.id = b.principal_id
 WHERE b.source = 'configuration'
 ORDER BY b.role, p.kind, p.name;

-- name: AuthzListBindings :many
SELECT b.role, b.principal_id::text AS principal_id, p.kind AS principal_kind, p.name AS principal_name,
       b.source,
       (extract(epoch FROM b.granted_at) * 1000000)::bigint AS granted_us,
       COALESCE(gb.kind, '')::text AS granted_by_kind, COALESCE(gb.name, '')::text AS granted_by_name
  FROM authz_role_binding b
  JOIN principal p ON p.id = b.principal_id
  LEFT JOIN principal gb ON gb.id = b.granted_by
 WHERE (sqlc.arg(role)::text = '' OR b.role = sqlc.arg(role)::text)
   AND (sqlc.arg(principal_id)::text = '' OR b.principal_id = NULLIF(sqlc.arg(principal_id)::text, '')::uuid)
 ORDER BY b.role, p.kind, p.name;

-- name: AuthzGroupAutoJoin :many
-- Whether a group is an auto-join group (R-AUTHZ-022: such a group takes no role);
-- no row for a principal that is not a group.
SELECT auto_join FROM user_group WHERE principal_id = sqlc.arg(principal_id)::text::uuid;

-- name: AuthzAdminBindings :one
-- The number of admin bindings and whether the break-glass administrator can sign
-- in, for the last_admin guard of R-AUTHZ-022.
SELECT (SELECT count(*) FROM authz_role_binding WHERE role = 'admin')::bigint AS admin_bindings,
       EXISTS (SELECT 1 FROM app_user u JOIN principal p ON p.id = u.principal_id
                WHERE u.is_break_glass AND p.status = 'active') AS break_glass_active;

-- name: AuthzTouchPoliciesOfPrincipal :many
-- Moves the version of every policy whose entries name the principal, so that the
-- ETag of the representation changes when the principal is renamed or deleted
-- (R-AUTHZ-028); the principal's own rows are removed or renamed by the caller.
UPDATE authz_policy SET version = version + 1
 WHERE id IN (SELECT policy_id FROM authz_policy_entry WHERE principal_id = sqlc.arg(principal_id)::text::uuid
              UNION SELECT policy_id FROM authz_project_policy_entry WHERE principal_id = sqlc.arg(principal_id)::text::uuid)
RETURNING id::text AS id;

-- name: AuthzPrincipalHasBinding :one
SELECT EXISTS (SELECT 1 FROM authz_role_binding WHERE principal_id = sqlc.arg(principal_id)::text::uuid);

-- name: AuthzTouchPoliciesOfRepository :many
-- Moves the version of every policy with a key selector on the repository, which
-- goes with the repository row (R-AUTHZ-028).
UPDATE authz_policy SET version = version + 1
 WHERE id IN (SELECT policy_id FROM authz_policy_selector WHERE repository_id = sqlc.arg(repository_id)::bigint)
RETURNING id::text AS id;
