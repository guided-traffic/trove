-- The member names of a group-membership event to principal IDs (R-BAS-015; work package 2). The audit event of
-- group.members.changed names the removed members as the group's members are named (user and service account
-- names), while the feed carries IDs only; the entry is built from the IDs this returns. A member that no
-- longer exists returns no row.

-- name: BastionPrincipalIDsByName :many
SELECT id::text AS id, name
  FROM principal
 WHERE kind IN ('user', 'service_account') AND name = ANY(sqlc.arg(names)::text[])
 ORDER BY name, id;
