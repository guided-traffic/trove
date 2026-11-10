-- The statements of the Ansible Galaxy format's remote repositories (internal/formatdata/ansible; R-ANS-018): the digests a remote's
-- version documents announce. A time travels as the column's own type here: the table is read and written by this package alone.

-- name: AnsibleUpstreamRecord :exec
-- Records the digests of version documents in the transaction that stores them. A version without a row gets one. A version
-- with a row keeps the digest recorded first while its archive is cached, a live item at the canonical path: the cached bytes were
-- verified against it, and a refreshed document that announces another one must not make them unverifiable (R-ANS-018, Q-ANS-13).
-- Without a cached archive nothing is protected, so the row follows the document: an operator who deleted the cached item (or a
-- restore that left the caches out) gets the digest the upstream announces now. names holds "<ns>.<name>".
INSERT INTO ansible_upstream_versions (repo_id, name, version, sha256, size, requires_ansible)
SELECT sqlc.arg(repo_id)::bigint, (sqlc.arg(names)::text[])[i], (sqlc.arg(versions)::text[])[i], (sqlc.arg(sha256s)::bytea[])[i],
       (sqlc.arg(sizes)::bigint[])[i], (sqlc.arg(requires)::text[])[i]
  FROM generate_series(1, cardinality(sqlc.arg(names)::text[])) AS i
ON CONFLICT (repo_id, name, version) DO UPDATE
   SET sha256 = EXCLUDED.sha256, size = EXCLUDED.size, requires_ansible = EXCLUDED.requires_ansible, recorded_at = now()
 WHERE ansible_upstream_versions.sha256 <> EXCLUDED.sha256
   AND NOT EXISTS (
         SELECT 1 FROM items i
          WHERE i.repo_id = ansible_upstream_versions.repo_id AND i.trashed_at IS NULL
            AND i.path = 'collections/' || split_part(ansible_upstream_versions.name, '.', 1) || '/' || split_part(ansible_upstream_versions.name, '.', 2)
                         || '/' || replace(ansible_upstream_versions.name, '.', '-') || '-' || ansible_upstream_versions.version || '.tar.gz');

-- name: AnsibleUpstreamRead :many
-- The recorded rows of the given versions, for the fill to tell which offered digest was not taken.
SELECT v.name, v.version, v.sha256, v.size, v.requires_ansible, v.recorded_at
  FROM generate_series(1, cardinality(sqlc.arg(names)::text[])) AS i
  JOIN ansible_upstream_versions v ON v.repo_id = sqlc.arg(repo_id)::bigint
   AND v.name = (sqlc.arg(names)::text[])[i] AND v.version = (sqlc.arg(versions)::text[])[i];

-- name: AnsibleUpstreamGet :many
SELECT name, version, sha256, size, requires_ansible, recorded_at
  FROM ansible_upstream_versions
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND name = sqlc.arg(name)::text AND version = sqlc.arg(version)::text;

-- name: AnsibleUpstreamByName :many
-- The versions recorded for one collection, newest recorded last: the virtual repository's cached-version set.
SELECT name, version, sha256, size, requires_ansible, recorded_at
  FROM ansible_upstream_versions
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND name = sqlc.arg(name)::text
 ORDER BY version
 LIMIT 10000;

-- name: AnsibleRepositoryRef :many
-- The qualified name of a repository, for the event of a digest that stayed.
SELECT (p.key || '/' || r.key)::text AS ref
  FROM repositories r
  JOIN project p ON p.id = r.project_id
 WHERE r.id = sqlc.arg(repo_id)::bigint;

-- name: BackupAnsibleDeleteUpstreamVersions :execrows
-- The recorded digests of one remote repository (R-BAK-008): they go with its cache entries and cached items.
DELETE FROM ansible_upstream_versions WHERE repo_id = sqlc.arg(repo_id)::bigint;
