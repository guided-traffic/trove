-- The statements of the generic package rows (internal/packages; R-ARCH-009,
-- R-ARCH-012, FMT-007): packages, package_versions, the links from items to
-- versions and the namespaced item properties. The package is a domain package and
-- imports no driver types, so parameters are plain Go types (a principal id travels
-- as text and is cast to uuid here, an id that may be absent as 0) and an absent id
-- comes back as 0.
--
-- packages.rev_seq is raised by BumpPackageRevision and by nothing else; the
-- package's writer calls it once per package per transaction, whatever the number
-- of writes (R-ARCH-009).

-- name: InsertPackage :execrows
-- A package that exists is left as it is; the caller locks it next. The count says
-- whether this statement created it.
INSERT INTO packages (repo_id, name, display_name)
VALUES (sqlc.arg(repo_id)::bigint, sqlc.arg(name)::text, sqlc.arg(display_name)::text)
ON CONFLICT (repo_id, name) DO NOTHING;

-- name: LockPackage :many
-- Locks the package row for the transaction: two writers of one package serialise
-- here (R-NPM-012).
SELECT id, repo_id, name, display_name, attributes, rev_seq
  FROM packages
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND name = sqlc.arg(name)::text
   FOR UPDATE;

-- name: GetPackage :many
SELECT id, repo_id, name, display_name, attributes, rev_seq
  FROM packages
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND name = sqlc.arg(name)::text;

-- name: ListLiveVersions :many
SELECT id, version, display_version, COALESCE(lead_item_id, 0)::bigint AS lead_item_id, attributes,
       created_at, COALESCE(created_by::text, '')::text AS created_by
  FROM package_versions
 WHERE package_id = sqlc.arg(package_id)::bigint AND trashed_at IS NULL
 ORDER BY id;

-- name: ListPackageRevisions :many
-- The revision of one package name in each of the given repositories; a repository
-- without the package has no row (R-ARCH-015).
SELECT repo_id, rev_seq
  FROM packages
 WHERE repo_id = ANY(sqlc.arg(repo_ids)::bigint[]) AND name = sqlc.arg(name)::text;

-- name: BumpPackageRevision :one
-- Raises the revision and reads the clock after the row lock the update takes
-- (R-NPM-012: times come from PostgreSQL after the lock).
UPDATE packages SET rev_seq = rev_seq + 1
 WHERE id = sqlc.arg(id)::bigint
RETURNING rev_seq, clock_timestamp()::timestamptz AS at;

-- name: ReadPackageRevision :one
SELECT rev_seq, clock_timestamp()::timestamptz AS at
  FROM packages
 WHERE id = sqlc.arg(id)::bigint;

-- name: SetPackageAttributes :execrows
UPDATE packages SET attributes = sqlc.arg(attributes)::text::jsonb
 WHERE id = sqlc.arg(id)::bigint;

-- name: InsertPackageVersion :many
-- A live version of the same name is left as it is and nothing comes back.
INSERT INTO package_versions (package_id, version, display_version, lead_item_id, attributes, created_by)
VALUES (sqlc.arg(package_id)::bigint, sqlc.arg(version)::text, sqlc.arg(display_version)::text,
        NULLIF(sqlc.arg(lead_item_id)::bigint, 0), sqlc.arg(attributes)::text::jsonb,
        NULLIF(sqlc.arg(created_by)::text, '')::uuid)
ON CONFLICT (package_id, version) WHERE trashed_at IS NULL DO NOTHING
RETURNING id;

-- name: TrashPackageVersion :many
-- The package id of the version that was live and is trashed now.
UPDATE package_versions SET trashed_at = now()
 WHERE id = sqlc.arg(id)::bigint AND trashed_at IS NULL
RETURNING package_id;

-- name: DeletePackageVersion :many
-- Deletes the row; the items that pointed at it keep their rows and lose the link
-- (ON DELETE SET NULL).
DELETE FROM package_versions
 WHERE id = sqlc.arg(id)::bigint
RETURNING package_id;

-- name: SetPackageVersionAttributes :many
UPDATE package_versions SET attributes = sqlc.arg(attributes)::text::jsonb
 WHERE id = sqlc.arg(id)::bigint
RETURNING package_id;

-- name: LinkItemToVersion :many
-- Attaches a live item to a version of the repository; nothing comes back when
-- either does not exist or they belong to different repositories.
UPDATE items i SET version_id = v.id
  FROM package_versions v
  JOIN packages p ON p.id = v.package_id
 WHERE i.id = sqlc.arg(item_id)::bigint AND v.id = sqlc.arg(version_id)::bigint
   AND i.repo_id = p.repo_id AND i.trashed_at IS NULL
RETURNING v.package_id;

-- name: SetLeadItem :many
UPDATE package_versions SET lead_item_id = sqlc.arg(item_id)::bigint
 WHERE id = sqlc.arg(version_id)::bigint
RETURNING package_id;

-- name: GetItemPackage :many
-- The package an item belongs to through its version; no row for an item that
-- belongs to none.
SELECT v.package_id
  FROM items i
  JOIN package_versions v ON v.id = i.version_id
 WHERE i.id = sqlc.arg(item_id)::bigint;

-- name: DeleteItemProperties :exec
-- Removes the properties of an item below a namespace prefix.
DELETE FROM item_properties
 WHERE item_id = sqlc.arg(item_id)::bigint AND starts_with(key, sqlc.arg(prefix)::text);

-- name: InsertItemProperties :exec
-- The properties as a JSON object of text values (sqlc has no multi-array unnest).
INSERT INTO item_properties (item_id, key, value)
SELECT sqlc.arg(item_id)::bigint, p.key, p.value
  FROM jsonb_each_text(sqlc.arg(props)::text::jsonb) AS p(key, value)
ON CONFLICT DO NOTHING;

-- name: ReadRestoredAt :many
-- The time of the last restore as the restore wrote it (R-BAK-022); no row before
-- the first restore. A format compares a stored document's validation time with it
-- (Q-NPM-13).
SELECT (value #>> '{}')::text AS restored_at
  FROM system_state
 WHERE key = 'restored_at';
