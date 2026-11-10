-- The statements of the Ansible Galaxy format's local class (R-ANS-008 to R-ANS-012, R-ANS-029). The tables are 00068_ansible_collections and
-- 00069_ansible_import_tasks; the queries of the remote class are queries/ansible_remote.sql.

-- name: AnsibleUpsertVersion :exec
-- The row of a stored version. A row of the same number is replaced by the one the commit under the package row lock decided on; the
-- time and the principal of the first publish stay.
INSERT INTO ansible_collection_versions (repo_id, name, version, sort_key, item_id, sha256, size, requires_ansible, dependencies,
                                         collection_info, published_by, published_at)
VALUES (sqlc.arg(repo_id)::bigint, sqlc.arg(name)::text, sqlc.arg(version)::text, sqlc.arg(sort_key)::bytea, sqlc.arg(item_id)::bigint,
        sqlc.arg(sha256)::bytea, sqlc.arg(size)::bigint, sqlc.arg(requires_ansible)::text, sqlc.arg(dependencies)::jsonb,
        sqlc.arg(collection_info)::jsonb, sqlc.arg(published_by)::text, sqlc.arg(published_at))
ON CONFLICT (repo_id, name, version) DO UPDATE
   SET sort_key = EXCLUDED.sort_key, item_id = EXCLUDED.item_id, sha256 = EXCLUDED.sha256, size = EXCLUDED.size,
       requires_ansible = EXCLUDED.requires_ansible, dependencies = EXCLUDED.dependencies, collection_info = EXCLUDED.collection_info;

-- name: AnsibleRewriteVersion :execrows
-- The reindex rewrites what the archive says (R-ANS-012); the item, the digest, the time and the principal are the item's.
UPDATE ansible_collection_versions
   SET sort_key = sqlc.arg(sort_key)::bytea, requires_ansible = sqlc.arg(requires_ansible)::text,
       dependencies = sqlc.arg(dependencies)::jsonb, collection_info = sqlc.arg(collection_info)::jsonb
 WHERE item_id = sqlc.arg(item_id)::bigint
   AND (sort_key, requires_ansible, dependencies, collection_info)
       IS DISTINCT FROM (sqlc.arg(sort_key)::bytea, sqlc.arg(requires_ansible)::text, sqlc.arg(dependencies)::jsonb, sqlc.arg(collection_info)::jsonb);

-- name: AnsibleInsertVersionFromItem :exec
-- The reindex creates the row of an archive that has none from the archive's manifest and from the item: its blob, its size, its time
-- and its principal (R-ANS-012).
INSERT INTO ansible_collection_versions (repo_id, name, version, sort_key, item_id, sha256, size, requires_ansible, dependencies,
                                         collection_info, published_by, published_at)
SELECT i.repo_id, sqlc.arg(name)::text, sqlc.arg(version)::text, sqlc.arg(sort_key)::bytea, i.id, i.blob_sha256, COALESCE(i.size, 0),
       sqlc.arg(requires_ansible)::text, sqlc.arg(dependencies)::jsonb, sqlc.arg(collection_info)::jsonb,
       COALESCE(i.created_by::text, '00000000-0000-0000-0000-000000000000'), i.created_at
  FROM items i
 WHERE i.id = sqlc.arg(item_id)::bigint AND i.blob_sha256 IS NOT NULL
ON CONFLICT (repo_id, name, version) DO UPDATE
   SET sort_key = EXCLUDED.sort_key, item_id = EXCLUDED.item_id, sha256 = EXCLUDED.sha256, size = EXCLUDED.size,
       requires_ansible = EXCLUDED.requires_ansible, dependencies = EXCLUDED.dependencies, collection_info = EXCLUDED.collection_info;

-- name: AnsibleGetVersion :many
SELECT repo_id, name, version, sort_key, item_id, sha256, size, requires_ansible, dependencies, collection_info, published_by, published_at
  FROM ansible_collection_versions
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND name = sqlc.arg(name)::text AND version = sqlc.arg(version)::text;

-- name: AnsibleListVersions :many
-- The versions of a collection newest first by precedence (R-ANS-004).
SELECT repo_id, name, version, sort_key, item_id, sha256, size, requires_ansible, dependencies, collection_info, published_by, published_at
  FROM ansible_collection_versions
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND name = sqlc.arg(name)::text
 ORDER BY sort_key DESC
 LIMIT sqlc.arg(row_limit)::int OFFSET sqlc.arg(row_offset)::int;

-- name: AnsibleCountVersions :one
SELECT count(*)::bigint FROM ansible_collection_versions WHERE repo_id = sqlc.arg(repo_id)::bigint AND name = sqlc.arg(name)::text;

-- name: AnsibleCollectionSummary :one
-- What the collection document shows: the first and the newest publish, and the newest version by precedence.
SELECT count(*)::bigint AS versions,
       COALESCE((extract(epoch FROM min(published_at)) * 1000000)::bigint, 0)::bigint AS created_us,
       COALESCE((extract(epoch FROM max(published_at)) * 1000000)::bigint, 0)::bigint AS updated_us,
       COALESCE((SELECT v.version FROM ansible_collection_versions v
                  WHERE v.repo_id = sqlc.arg(repo_id)::bigint AND v.name = sqlc.arg(name)::text
                  ORDER BY v.sort_key DESC LIMIT 1), '')::text AS highest
  FROM ansible_collection_versions
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND name = sqlc.arg(name)::text;

-- name: AnsibleDeleteVersion :many
-- Deletes the row of a version and returns what it held; none when the row is gone already (the cascade of a permanent delete).
DELETE FROM ansible_collection_versions
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND name = sqlc.arg(name)::text AND version = sqlc.arg(version)::text
RETURNING sha256, item_id;

-- name: AnsibleInsertTombstone :execrows
-- The first tombstone of a number stays (R-ANS-012); the count says whether this call wrote it.
INSERT INTO ansible_tombstones (repo_id, name, version, sha256, deleted_by)
VALUES (sqlc.arg(repo_id)::bigint, sqlc.arg(name)::text, sqlc.arg(version)::text, sqlc.arg(sha256)::bytea, sqlc.arg(deleted_by)::text)
ON CONFLICT (repo_id, name, version) DO NOTHING;

-- name: AnsibleGetTombstone :many
SELECT repo_id, name, version, sha256, deleted_at, deleted_by
  FROM ansible_tombstones
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND name = sqlc.arg(name)::text AND version = sqlc.arg(version)::text;

-- name: AnsibleDeleteTombstone :execrows
DELETE FROM ansible_tombstones
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND name = sqlc.arg(name)::text AND version = sqlc.arg(version)::text;

-- name: AnsibleListTombstones :many
-- In name and version order after the cursor, which is the name and the version of the last tombstone seen.
SELECT repo_id, name, version, sha256, deleted_at, deleted_by
  FROM ansible_tombstones
 WHERE repo_id = sqlc.arg(repo_id)::bigint
   AND (name, version) > (sqlc.arg(after_name)::text, sqlc.arg(after_version)::text)
 ORDER BY name, version
 LIMIT sqlc.arg(row_limit)::int;

-- name: AnsibleInsertTask :exec
INSERT INTO ansible_import_tasks (id, repo_id, name, version, state, error_code, error_description, messages, created_by, created_at, finished_at)
VALUES (CAST(sqlc.arg(id)::text AS uuid), sqlc.arg(repo_id)::bigint, sqlc.arg(name)::text, sqlc.arg(version)::text, sqlc.arg(state)::text,
        NULLIF(sqlc.arg(error_code)::text, ''), NULLIF(sqlc.arg(error_description)::text, ''), sqlc.arg(messages)::jsonb,
        sqlc.arg(created_by)::text, sqlc.arg(created_at), sqlc.arg(finished_at));

-- name: AnsibleGetTask :many
SELECT id::text AS id, repo_id, name, version, state, COALESCE(error_code, '')::text AS error_code,
       COALESCE(error_description, '')::text AS error_description, messages, created_by, created_at, finished_at
  FROM ansible_import_tasks
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND id = CAST(sqlc.arg(id)::text AS uuid);

-- name: AnsibleListTasks :many
-- Newest first, optionally of one state, after the cursor, which is the id of the last task seen.
SELECT t.id::text AS id, t.repo_id, t.name, t.version, t.state, COALESCE(t.error_code, '')::text AS error_code,
       COALESCE(t.error_description, '')::text AS error_description, t.messages, t.created_by, t.created_at, t.finished_at
  FROM ansible_import_tasks t
 WHERE t.repo_id = sqlc.arg(repo_id)::bigint
   AND (sqlc.arg(state)::text = '' OR t.state = sqlc.arg(state)::text)
   AND (sqlc.arg(after)::text = '' OR (t.created_at, t.id) < (SELECT a.created_at, a.id FROM ansible_import_tasks a
                                                                WHERE a.repo_id = t.repo_id AND a.id = NULLIF(sqlc.arg(after)::text, '')::uuid))
 ORDER BY t.created_at DESC, t.id DESC
 LIMIT sqlc.arg(row_limit)::int;

-- name: AnsibleSweepTasks :execrows
-- Deletes the tasks older than the retention (R-ANS-009); the versions they describe stay.
DELETE FROM ansible_import_tasks WHERE created_at < now() - make_interval(secs => sqlc.arg(keep_seconds)::double precision);

-- name: AnsibleArchivesAfter :many
-- The live archives of a local repository after a path, in path order, with the row each has (the reindex, R-ANS-012). A file below
-- collections/ whose name ends in .tar.gz is an archive; the trash can's items are not.
SELECT i.id AS item_id, i.path, COALESCE(i.blob_sha256, ''::bytea)::bytea AS sha256, COALESCE(i.size, 0)::bigint AS size,
       (v.item_id IS NOT NULL)::boolean AS has_row
  FROM items i
  LEFT JOIN ansible_collection_versions v ON v.item_id = i.id
 WHERE i.repo_id = sqlc.arg(repo_id)::bigint AND i.kind = 'file' AND i.trashed_at IS NULL AND i.blob_sha256 IS NOT NULL
   AND i.path LIKE 'collections/%.tar.gz' AND i.path > sqlc.arg(after)::text
 ORDER BY i.path
 LIMIT sqlc.arg(row_limit)::int;

-- name: AnsibleBumpPackage :execrows
-- Raises the revision of a package whose rows the reindex changed (R-ANS-012), so the documents' validators change.
UPDATE packages SET rev_seq = rev_seq + 1 WHERE repo_id = sqlc.arg(repo_id)::bigint AND name = sqlc.arg(name)::text;

-- name: AnsibleReplayRepository :many
-- The local Ansible Galaxy repository a journal entry names; an entry about any other repository finds nothing.
SELECT r.id, (p.key || '/' || r.key)::text AS ref
  FROM repositories r
  JOIN project p ON p.id = r.project_id
 WHERE r.id = sqlc.arg(repo_id)::bigint AND r.format = 'ansible' AND r.class = 'local';

-- name: AnsibleViewRows :many
-- The rows the package screens read for the given generic versions (R-ANS-026).
SELECT pv.id AS version_id, v.name, v.version, v.requires_ansible, v.dependencies, v.collection_info, v.sha256, v.size, v.published_at
  FROM package_versions pv
  JOIN packages pk ON pk.id = pv.package_id
  JOIN ansible_collection_versions v ON v.repo_id = pk.repo_id AND v.name = pk.name AND v.version = pv.version
 WHERE pv.id = ANY(sqlc.arg(ids)::bigint[]);

-- name: AnsibleViewFind :many
-- The generic versions of the repositories (by key) whose description contains the text or whose tags contain the keyword.
SELECT pv.id AS version_id
  FROM ansible_collection_versions v
  JOIN repositories r ON r.id = v.repo_id
  JOIN project p ON p.id = r.project_id
  JOIN packages pk ON pk.repo_id = v.repo_id AND pk.name = v.name
  JOIN package_versions pv ON pv.package_id = pk.id AND pv.version = v.version AND pv.trashed_at IS NULL
 WHERE (p.key || '/' || r.key) = ANY(sqlc.arg(repos)::text[])
   AND ((sqlc.arg(text)::text <> '' AND position(lower(sqlc.arg(text)::text) in lower(COALESCE(v.collection_info->>'description', ''))) > 0)
        OR (sqlc.arg(keyword)::text <> '' AND v.collection_info->'tags' ? sqlc.arg(keyword)::text))
 ORDER BY pv.id
 LIMIT sqlc.arg(row_limit)::int;

-- name: AnsibleViewStored :many
-- The versions a local repository serves of a collection, newest first by precedence.
SELECT version FROM ansible_collection_versions
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND name = sqlc.arg(name)::text
 ORDER BY sort_key DESC LIMIT sqlc.arg(row_limit)::int;

-- name: AnsibleViewWithheld :many
-- The generic versions of the repositories (by key) whose number a tombstone withholds although the version is still stored: what a
-- restore's replay leaves (R-ANS-029).
SELECT pv.id AS version_id
  FROM ansible_tombstones t
  JOIN repositories r ON r.id = t.repo_id
  JOIN project p ON p.id = r.project_id
  JOIN packages pk ON pk.repo_id = t.repo_id AND pk.name = t.name
  JOIN package_versions pv ON pv.package_id = pk.id AND pv.version = t.version AND pv.trashed_at IS NULL
 WHERE (p.key || '/' || r.key) = ANY(sqlc.arg(repos)::text[])
 ORDER BY pv.id;
