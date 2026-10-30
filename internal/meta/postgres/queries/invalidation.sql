-- Queries of the cache invalidations of remote repositories (R-REPO-045), their
-- fold into the metadata entries and the cache-entry list (R-REPO-032).
--
-- Instants are read as microseconds since the epoch and written through
-- timestamptz 'epoch' + n * interval '1 microsecond' (exact, unlike
-- to_timestamp), so that the domain package never imports the driver types
-- (R-ARCH-002). A prefix covers a path when it is empty, equal to the path or
-- equal to its leading segments: the range path >= prefix || '/' AND
-- path < prefix || '0' is the set of paths below prefix/ ('0' follows '/' in the
-- byte order of the "C" collation), so '@acme' covers '@acme/util' and not
-- '@acmecorp/util', and the primary key serves the range.

-- name: InvalidationInsert :one
-- One invalidation row. created_at is taken here, last in the inserting
-- transaction, and not at its start: an entry that a fill validates while the
-- transaction runs must not escape the invalidation that commits after it.
INSERT INTO remote_cache_invalidations (repo_id, prefix, scope, created_at, created_by)
VALUES (sqlc.arg(repo_id)::bigint, sqlc.arg(prefix)::text, sqlc.arg(scope)::text, clock_timestamp(),
        NULLIF(sqlc.arg(created_by)::text, '')::uuid)
RETURNING id, (extract(epoch FROM created_at) * 1000000)::bigint AS created_us;

-- name: InvalidationCountCovered :one
-- The covered metadata entries, the count of the answer of the invalidate call.
-- Index-only on the partial index of metadata entries for the whole repository,
-- a range of the primary key for a prefix.
SELECT count(*)::bigint
  FROM remote_cache_entries
 WHERE repo_id = sqlc.arg(repo_id)::bigint
   AND kind = 'metadata'
   AND (sqlc.arg(prefix)::text = ''
        OR path = sqlc.arg(prefix)::text COLLATE "C"
        OR (path >= sqlc.arg(prefix)::text COLLATE "C" || '/' AND path < sqlc.arg(prefix)::text COLLATE "C" || '0'));

-- name: InvalidationListAll :many
-- Every row of every repository: the replica's set at start and its safety reload.
SELECT id, repo_id, prefix, scope, (extract(epoch FROM created_at) * 1000000)::bigint AS created_us
  FROM remote_cache_invalidations
 ORDER BY id;

-- name: InvalidationListRepository :many
-- The rows of one repository: the reload after the fold job deleted rows.
SELECT id, repo_id, prefix, scope, (extract(epoch FROM created_at) * 1000000)::bigint AS created_us
  FROM remote_cache_invalidations
 WHERE repo_id = sqlc.arg(repo_id)::bigint
 ORDER BY id;

-- name: InvalidationListUnfolded :many
-- The rows the fold job has not finished, oldest first.
SELECT id, repo_id, prefix, scope, (extract(epoch FROM created_at) * 1000000)::bigint AS created_us
  FROM remote_cache_invalidations
 WHERE folded_at IS NULL
 ORDER BY created_at, id
 LIMIT sqlc.arg(max_rows)::int;

-- name: InvalidationFoldBatch :execrows
-- One batch of the fold: the metadata entries under the prefix that were validated
-- before the invalidation and expire after it get the invalidation time as their
-- expiry. LEAST never extends an expiry; an entry refreshed after the invalidation
-- has a newer validated_at and is left alone. The rows are locked in key order, so
-- two folds and a fill cannot deadlock, and not skipped: a skipped entry would stay
-- fresh in the database once the row is gone.
WITH batch AS (
    SELECT repo_id, path, variant
      FROM remote_cache_entries
     WHERE repo_id = sqlc.arg(repo_id)::bigint
       AND kind = 'metadata'
       AND validated_at < timestamptz 'epoch' + sqlc.arg(created_us)::bigint * interval '1 microsecond'
       AND expires_at > timestamptz 'epoch' + sqlc.arg(created_us)::bigint * interval '1 microsecond'
       AND (sqlc.arg(prefix)::text = ''
            OR path = sqlc.arg(prefix)::text COLLATE "C"
            OR (path >= sqlc.arg(prefix)::text COLLATE "C" || '/' AND path < sqlc.arg(prefix)::text COLLATE "C" || '0'))
     ORDER BY repo_id, path, variant
     LIMIT sqlc.arg(max_rows)::int
       FOR UPDATE
)
UPDATE remote_cache_entries e
   SET expires_at = LEAST(e.expires_at, timestamptz 'epoch' + sqlc.arg(created_us)::bigint * interval '1 microsecond')
  FROM batch b
 WHERE e.repo_id = b.repo_id AND e.path = b.path AND e.variant = b.variant;

-- name: InvalidationMarkFolded :execrows
UPDATE remote_cache_invalidations
   SET folded_at = timestamptz 'epoch' + sqlc.arg(at_us)::bigint * interval '1 microsecond'
 WHERE id = sqlc.arg(id)::bigint AND folded_at IS NULL;

-- name: InvalidationListDeletable :many
-- The folded rows that no reader needs any more: a metadata row once its grace
-- after the fold has passed, a negative or all row once 86,405 s have passed since
-- its creation (the largest notFoundTtlSeconds plus the clock allowance).
SELECT id, repo_id, prefix, scope, (extract(epoch FROM created_at) * 1000000)::bigint AS created_us
  FROM remote_cache_invalidations
 WHERE folded_at IS NOT NULL
   AND CASE scope
         WHEN 'metadata' THEN folded_at <= timestamptz 'epoch' + sqlc.arg(metadata_before_us)::bigint * interval '1 microsecond'
         ELSE created_at <= timestamptz 'epoch' + sqlc.arg(negative_before_us)::bigint * interval '1 microsecond'
       END
 ORDER BY id
 LIMIT sqlc.arg(max_rows)::int;

-- name: InvalidationDelete :many
-- Deletes the rows and returns the repositories they belonged to, for the change
-- notification.
DELETE FROM remote_cache_invalidations
 WHERE id = ANY(sqlc.arg(ids)::bigint[])
RETURNING repo_id;

-- name: InvalidationListEntries :many
-- A page of the cache entries of one repository for diagnosis (R-REPO-032), in the
-- order of the primary key; after_path and after_variant are the key of the last
-- row of the previous page. A prefix filters like a prefix of an invalidation. An
-- absent expiry (an artifact) or gone mark is 0.
SELECT path, variant, kind, upstream_url,
       (extract(epoch FROM fetched_at) * 1000000)::bigint AS fetched_us,
       (extract(epoch FROM validated_at) * 1000000)::bigint AS validated_us,
       COALESCE((extract(epoch FROM expires_at) * 1000000)::bigint, 0)::bigint AS expires_us,
       COALESCE((extract(epoch FROM upstream_gone_at) * 1000000)::bigint, 0)::bigint AS gone_us,
       (extract(epoch FROM last_used_at) * 1000000)::bigint AS last_used_us
  FROM remote_cache_entries
 WHERE repo_id = sqlc.arg(repo_id)::bigint
   AND kind = ANY(sqlc.arg(kinds)::text[])
   AND (sqlc.arg(prefix)::text = ''
        OR path = sqlc.arg(prefix)::text COLLATE "C"
        OR (path >= sqlc.arg(prefix)::text COLLATE "C" || '/' AND path < sqlc.arg(prefix)::text COLLATE "C" || '0'))
   AND (NOT sqlc.arg(has_after)::boolean
        OR (path, variant) > (sqlc.arg(after_path)::text COLLATE "C", sqlc.arg(after_variant)::text COLLATE "C"))
 ORDER BY path, variant
 LIMIT sqlc.arg(max_rows)::int;
