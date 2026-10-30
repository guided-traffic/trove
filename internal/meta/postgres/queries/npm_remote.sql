-- The statements of the npm format's remote repositories (internal/formatdata/npm;
-- R-NPM-024 to R-NPM-026): the records of the stored upstream documents and the
-- facts tarball serving reads. The package is a domain package and imports no driver
-- types, so a time travels as text and is cast to timestamptz here, and a value that
-- may be absent as the empty string.
--
-- The fill statements run in the transaction that upserts the cache entry
-- (remote_cache_entries), after the engine locked that entry, and take the lock of the
-- package's npm_remote_packages row: the records and the stored document describe the
-- same document, whichever fill commits last (R-NPM-024).

-- name: NPMRemoteInsertPackage :execrows
-- The record of a package's stored document. A package without a row inserts it, and the
-- caller has no older document to compare with; one that has a row conflicts and inserts
-- nothing. doc_modified is capped at PostgreSQL's clock in this transaction, so neither a
-- document dated in the future nor a replica with a fast clock can block every later
-- refresh (R-NPM-024).
INSERT INTO npm_remote_packages (repo_id, name, doc_sha256, doc_modified)
VALUES (sqlc.arg(repo_id)::bigint, sqlc.arg(name)::text, sqlc.arg(doc_sha256)::bytea,
        LEAST(NULLIF(sqlc.arg(doc_modified)::text, '')::timestamptz, clock_timestamp()))
ON CONFLICT (repo_id, name) DO NOTHING;

-- name: NPMRemoteLockPackage :many
-- The record of a package's stored document under its row lock: the document the stale-copy
-- rule compares with is the one stored now, not the one stored when the fetch began.
SELECT doc_sha256, doc_modified
  FROM npm_remote_packages
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND name = sqlc.arg(name)::text
   FOR UPDATE;

-- name: NPMRemoteStoredEntry :many
-- The cache entry of the package's stored document as it is now: when it was fetched and
-- whether it is marked gone. Read after the lock of the package's record, so an entry that a
-- concurrent fill has just committed is seen.
SELECT (extract(epoch FROM fetched_at) * 1000000)::bigint AS fetched_us,
       (upstream_gone_at IS NOT NULL)::boolean AS gone
  FROM remote_cache_entries
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND path = sqlc.arg(name)::text COLLATE "C"
   AND variant = 'full' AND kind = 'metadata';

-- name: NPMRemoteSetPackage :exec
UPDATE npm_remote_packages
   SET doc_sha256 = sqlc.arg(doc_sha256)::bytea,
       doc_modified = LEAST(NULLIF(sqlc.arg(doc_modified)::text, '')::timestamptz, clock_timestamp())
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND name = sqlc.arg(name)::text;

-- name: NPMRemoteUnlist :many
-- The versions the new document no longer lists (R-NPM-026): marked, not deleted. Only the
-- versions that were listed come back, so a duplicate fill finds nothing to report.
UPDATE npm_remote_versions
   SET listed = false
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND name = sqlc.arg(name)::text
   AND listed AND version <> ALL(sqlc.arg(versions)::text[])
RETURNING version, COALESCE(integrity, '')::text AS integrity;

-- name: NPMRemoteRelist :many
-- The versions the upstream lists again.
UPDATE npm_remote_versions
   SET listed = true
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND name = sqlc.arg(name)::text
   AND NOT listed AND version = ANY(sqlc.arg(versions)::text[])
RETURNING version;

-- name: NPMRemoteUpsertVersions :exec
-- The facts tarball serving needs, one row per version of the document. listed of an
-- existing row stays what Unlist and Relist made it.
INSERT INTO npm_remote_versions (repo_id, name, version, integrity, shasum, published_at, listed)
SELECT sqlc.arg(repo_id)::bigint, sqlc.arg(name)::text, (sqlc.arg(versions)::text[])[i],
       NULLIF((sqlc.arg(integrities)::text[])[i], ''), NULLIF((sqlc.arg(shasums)::text[])[i], ''),
       NULLIF((sqlc.arg(published)::text[])[i], '')::timestamptz, true
  FROM generate_series(1, cardinality(sqlc.arg(versions)::text[])) AS i
ON CONFLICT (repo_id, name, version) DO UPDATE
   SET integrity = EXCLUDED.integrity, shasum = EXCLUDED.shasum, published_at = EXCLUDED.published_at;

-- name: NPMRemoteTarballFacts :many
-- Everything the serving of one tarball reads, in one statement (R-NPM-025, R-NPM-026): the
-- version's record, the state of the package document's cache entry (stored, gone, current
-- = not expired and validated after the last restore, Q-NPM-13) and the repository's
-- serveRemovedVersions. No row: the repository does not exist.
SELECT (v.version IS NOT NULL)::boolean AS known,
       COALESCE(v.listed, false)::boolean AS listed,
       COALESCE(v.integrity, '')::text AS integrity,
       COALESCE(v.shasum, '')::text AS shasum,
       (e.repo_id IS NOT NULL)::boolean AS doc_stored,
       COALESCE((extract(epoch FROM e.upstream_gone_at) * 1000000)::bigint, 0)::bigint AS gone_us,
       COALESCE(e.expires_at > clock_timestamp()
                AND e.validated_at > COALESCE((SELECT (s.value #>> '{}')::timestamptz FROM system_state s WHERE s.key = 'restored_at'), '-infinity'::timestamptz),
                false)::boolean AS doc_current,
       COALESCE(r.config #>> '{npm,serveRemovedVersions}', '') = 'true' AS serve_removed
  FROM repositories r
  LEFT JOIN remote_cache_entries e
         ON e.repo_id = r.id AND e.path = sqlc.arg(name)::text COLLATE "C" AND e.variant = 'full' AND e.kind = 'metadata'
  LEFT JOIN npm_remote_versions v
         ON v.repo_id = r.id AND v.name = sqlc.arg(name)::text AND v.version = sqlc.arg(version)::text
 WHERE r.id = sqlc.arg(repo_id)::bigint;
