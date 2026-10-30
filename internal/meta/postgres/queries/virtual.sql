-- Queries of virtual repositories (R-REPO-017, R-REPO-021, R-REPO-023): the
-- members, the deploy target and the pin rules, which are rows and not part of
-- the configuration document (Q-REPO-104). The repository package renders them
-- into the document on read and replaces them on write; the grant index reads
-- the members and the deploy target through the AuthzBuild* statements.

-- name: ListVirtualMembers :many
SELECT m.virtual_id, m.position, m.member_id, r.key AS member_key
  FROM virtual_members m
  JOIN repositories r ON r.id = m.member_id
 WHERE m.virtual_id = ANY(sqlc.arg(ids)::bigint[])
 ORDER BY m.virtual_id, m.position;

-- name: ListVirtualDeployTargets :many
SELECT t.virtual_id, t.target_id, r.key AS target_key
  FROM virtual_deploy_targets t
  JOIN repositories r ON r.id = t.target_id
 WHERE t.virtual_id = ANY(sqlc.arg(ids)::bigint[]);

-- name: ListVirtualPinRules :many
SELECT virtual_id, position, package_patterns
  FROM virtual_pin_rules
 WHERE virtual_id = ANY(sqlc.arg(ids)::bigint[])
 ORDER BY virtual_id, position;

-- name: ListVirtualPinMembers :many
-- A rule's members come back in the order of the virtual repository's member
-- list, so that a read is stable whatever order the write named them in.
SELECT p.virtual_id, p.position, p.member_id, r.key AS member_key
  FROM virtual_pin_members p
  JOIN repositories r ON r.id = p.member_id
  LEFT JOIN virtual_members m ON m.virtual_id = p.virtual_id AND m.member_id = p.member_id
 WHERE p.virtual_id = ANY(sqlc.arg(ids)::bigint[])
 ORDER BY p.virtual_id, p.position, m.position NULLS LAST, r.key;

-- name: LockRepositoriesByKeys :many
-- Resolves the keys a virtual repository names and keeps the rows from being
-- marked for deletion before the transaction ends: FOR SHARE conflicts with the
-- update that sets lifecycle = 'deleting'. The order by id is the lock order.
SELECT id, key, class, format, lifecycle, config
  FROM repositories
 WHERE key = ANY(sqlc.arg(keys)::text[])
 ORDER BY id
   FOR SHARE;

-- name: DeleteVirtualMembers :exec
DELETE FROM virtual_members WHERE virtual_id = sqlc.arg(virtual_id)::bigint;

-- name: InsertVirtualMember :exec
INSERT INTO virtual_members (virtual_id, position, member_id)
VALUES (sqlc.arg(virtual_id)::bigint, sqlc.arg(position)::int, sqlc.arg(member_id)::bigint);

-- name: DeleteVirtualDeployTarget :exec
DELETE FROM virtual_deploy_targets WHERE virtual_id = sqlc.arg(virtual_id)::bigint;

-- name: InsertVirtualDeployTarget :exec
INSERT INTO virtual_deploy_targets (virtual_id, target_id)
VALUES (sqlc.arg(virtual_id)::bigint, sqlc.arg(target_id)::bigint);

-- name: DeleteVirtualPinRules :exec
-- The members of the rules go with them (ON DELETE CASCADE).
DELETE FROM virtual_pin_rules WHERE virtual_id = sqlc.arg(virtual_id)::bigint;

-- name: InsertVirtualPinRule :exec
INSERT INTO virtual_pin_rules (virtual_id, position, package_patterns)
VALUES (sqlc.arg(virtual_id)::bigint, sqlc.arg(position)::int, sqlc.arg(package_patterns)::text[]);

-- name: InsertVirtualPinMember :exec
INSERT INTO virtual_pin_members (virtual_id, position, member_id)
VALUES (sqlc.arg(virtual_id)::bigint, sqlc.arg(position)::int, sqlc.arg(member_id)::bigint);

-- name: ListReferencingVirtuals :many
-- The virtual repositories that list a repository as a member, a deploy target
-- or a pin-rule member (R-REPO-013). The rows are locked: a detach rewrites them.
SELECT r.id, r.key
  FROM repositories r
 WHERE r.id IN (SELECT virtual_id FROM virtual_members WHERE member_id = sqlc.arg(id)::bigint
                UNION SELECT virtual_id FROM virtual_deploy_targets WHERE target_id = sqlc.arg(id)::bigint
                UNION SELECT virtual_id FROM virtual_pin_members WHERE member_id = sqlc.arg(id)::bigint)
 ORDER BY r.key
   FOR UPDATE;

-- name: DetachMember :exec
-- The detach of R-REPO-013: the repository leaves every member list, deploy
-- target and pin rule in the transaction that marks it for deletion.
DELETE FROM virtual_members WHERE member_id = sqlc.arg(id)::bigint;

-- name: DetachDeployTarget :exec
DELETE FROM virtual_deploy_targets WHERE target_id = sqlc.arg(id)::bigint;

-- name: DetachPinMember :exec
DELETE FROM virtual_pin_members WHERE member_id = sqlc.arg(id)::bigint;

-- name: TouchConfigVersions :exec
-- A virtual repository whose member list changed under a detach is a changed
-- document: its ETag moves (R-REPO-011).
UPDATE repositories
   SET config_version = config_version + 1, updated_at = now()
 WHERE id = ANY(sqlc.arg(ids)::bigint[]);

-- name: AuthzBuildVirtualMembers :many
-- The member lists the grant index compiles (R-AUTHZ-009), in the snapshot of
-- the index build.
SELECT m.virtual_id, r.key AS member_key
  FROM virtual_members m
  JOIN repositories r ON r.id = m.member_id
 ORDER BY m.virtual_id, m.position;

-- name: AuthzBuildDeployTargets :many
SELECT t.virtual_id, r.key AS target_key
  FROM virtual_deploy_targets t
  JOIN repositories r ON r.id = t.target_id
 ORDER BY t.virtual_id;
