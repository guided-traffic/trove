-- Queries of the remote engine (R-REPO-025 to R-REPO-029, R-REPO-043): the cache
-- entries, the URL map, the circuit of a repository's upstream and the gone marks.
--
-- Instants are read as microseconds since the epoch and written through
-- timestamptz 'epoch' + n * interval '1 microsecond' (exact, unlike to_timestamp),
-- so that the domain package never imports the driver types (R-ARCH-002). Every
-- freshness decision is taken by the database clock: the remaining lifetime of an
-- entry is computed here, so no replica clock decides whether a document is fresh
-- (R-REPO-027), and validated_at, which the invalidations are compared with, is
-- written with clock_timestamp() (Q-REPO-93).

-- name: ProxyClock :one
-- The database clock in microseconds: the fetched_at of a response, taken when its
-- headers arrive, so that two fetches of one document on two replicas are ordered by
-- one clock (R-REPO-026: the upsert keeps the newer fetched_at).
SELECT (extract(epoch FROM clock_timestamp()) * 1000000)::bigint AS now_us;

-- name: ProxyEntryGet :many
-- One cache entry with the size of its blob. ttl_us is the remaining lifetime of a
-- metadata entry on the database clock (negative when expired), 0 for an artifact.
SELECT e.kind,
       COALESCE(e.item_id, 0)::bigint AS item_id,
       e.blob_sha256,
       b.size AS blob_size,
       e.upstream_url,
       COALESCE(e.upstream_etag, '')::text AS upstream_etag,
       COALESCE((extract(epoch FROM e.upstream_last_modified) * 1000000)::bigint, 0)::bigint AS last_modified_us,
       (extract(epoch FROM e.fetched_at) * 1000000)::bigint AS fetched_us,
       (extract(epoch FROM e.validated_at) * 1000000)::bigint AS validated_us,
       COALESCE((extract(epoch FROM e.expires_at) * 1000000)::bigint, 0)::bigint AS expires_us,
       COALESCE((extract(epoch FROM e.upstream_gone_at) * 1000000)::bigint, 0)::bigint AS gone_us,
       COALESCE((extract(epoch FROM (e.expires_at - clock_timestamp())) * 1000000)::bigint, 0)::bigint AS ttl_us
  FROM remote_cache_entries e
  JOIN blobs b ON b.sha256 = e.blob_sha256
 WHERE e.repo_id = sqlc.arg(repo_id)::bigint
   AND e.path = sqlc.arg(path)::text COLLATE "C"
   AND e.variant = sqlc.arg(variant)::text;

-- name: ProxyEntryLock :many
-- The entry of a fill under its row lock: the fill transaction decides on what is
-- stored now (the stale-copy rule of the format, an artifact's digest), not on what
-- the request read before the transfer.
SELECT e.kind,
       COALESCE(e.item_id, 0)::bigint AS item_id,
       e.blob_sha256,
       COALESCE(e.upstream_etag, '')::text AS upstream_etag,
       (extract(epoch FROM e.fetched_at) * 1000000)::bigint AS fetched_us,
       (extract(epoch FROM e.validated_at) * 1000000)::bigint AS validated_us,
       COALESCE((extract(epoch FROM e.upstream_gone_at) * 1000000)::bigint, 0)::bigint AS gone_us
  FROM remote_cache_entries e
 WHERE e.repo_id = sqlc.arg(repo_id)::bigint
   AND e.path = sqlc.arg(path)::text COLLATE "C"
   AND e.variant = sqlc.arg(variant)::text
   FOR UPDATE OF e;

-- name: ProxyEntryState :one
-- What the entries of one path say, any variant (R-REPO-048 steps 1 and 2): how many
-- exist, how many carry a gone mark and the newest mark.
SELECT count(*)::bigint AS total,
       count(upstream_gone_at)::bigint AS gone,
       COALESCE(max((extract(epoch FROM upstream_gone_at) * 1000000)::bigint), 0)::bigint AS gone_us
  FROM remote_cache_entries
 WHERE repo_id = sqlc.arg(repo_id)::bigint
   AND path = sqlc.arg(path)::text COLLATE "C";

-- name: ProxyMetadataUpsert :many
-- The entry of a fetched document. A row that is already newer (fetched_at) is kept:
-- the statement then returns no row and the caller serves what is stored
-- (R-REPO-026: the upsert keeps the newer fetched_at). validated_at is the database
-- clock now, expires_at is that plus the period. A gone mark is cleared by a 2xx.
INSERT INTO remote_cache_entries
       (repo_id, path, variant, kind, blob_sha256, upstream_url, upstream_etag, upstream_last_modified,
        fetched_at, validated_at, expires_at, upstream_gone_at, last_used_at)
VALUES (sqlc.arg(repo_id)::bigint, sqlc.arg(path)::text, sqlc.arg(variant)::text, 'metadata',
        sqlc.arg(blob_sha256)::bytea, sqlc.arg(upstream_url)::text, NULLIF(sqlc.arg(upstream_etag)::text, ''),
        CASE WHEN sqlc.arg(last_modified_us)::bigint = 0 THEN NULL
             ELSE timestamptz 'epoch' + sqlc.arg(last_modified_us)::bigint * interval '1 microsecond' END,
        timestamptz 'epoch' + sqlc.arg(fetched_us)::bigint * interval '1 microsecond',
        clock_timestamp(),
        clock_timestamp() + sqlc.arg(ttl_seconds)::bigint * interval '1 second',
        NULL,
        timestamptz 'epoch' + sqlc.arg(fetched_us)::bigint * interval '1 microsecond')
ON CONFLICT (repo_id, path, variant) DO UPDATE
   SET blob_sha256 = EXCLUDED.blob_sha256, upstream_url = EXCLUDED.upstream_url,
       upstream_etag = EXCLUDED.upstream_etag, upstream_last_modified = EXCLUDED.upstream_last_modified,
       fetched_at = EXCLUDED.fetched_at, validated_at = EXCLUDED.validated_at, expires_at = EXCLUDED.expires_at,
       upstream_gone_at = NULL
 WHERE remote_cache_entries.kind = 'metadata'
   AND remote_cache_entries.fetched_at <= EXCLUDED.fetched_at
RETURNING (extract(epoch FROM validated_at) * 1000000)::bigint AS validated_us,
          (extract(epoch FROM expires_at) * 1000000)::bigint AS expires_us;

-- name: ProxyMetadataRenew :many
-- A 304, or a 2xx the format kept the stored document for: only the freshness moves
-- (R-REPO-027). The stored validators stay unless the response sent new ones.
UPDATE remote_cache_entries
   SET validated_at = clock_timestamp(),
       expires_at = clock_timestamp() + sqlc.arg(ttl_seconds)::bigint * interval '1 second',
       upstream_etag = COALESCE(NULLIF(sqlc.arg(upstream_etag)::text, ''), upstream_etag),
       upstream_gone_at = NULL
 WHERE repo_id = sqlc.arg(repo_id)::bigint
   AND path = sqlc.arg(path)::text COLLATE "C"
   AND variant = sqlc.arg(variant)::text
   AND kind = 'metadata'
RETURNING (extract(epoch FROM validated_at) * 1000000)::bigint AS validated_us,
          (extract(epoch FROM expires_at) * 1000000)::bigint AS expires_us;

-- name: ProxyMetadataMarkGone :execrows
-- The upstream answered 404 or 410 for a stored document (R-REPO-029): the entry is
-- marked, not deleted. A second answer changes nothing and returns 0.
UPDATE remote_cache_entries
   SET upstream_gone_at = clock_timestamp()
 WHERE repo_id = sqlc.arg(repo_id)::bigint
   AND path = sqlc.arg(path)::text COLLATE "C"
   AND variant = sqlc.arg(variant)::text
   AND kind = 'metadata'
   AND upstream_gone_at IS NULL;

-- name: ProxyArtifactInsert :execrows
-- The entry of a fetched artifact, written in the transaction of the item commit
-- after the caller locked the row and found none (an existing entry is never
-- replaced, R-REPO-041).
INSERT INTO remote_cache_entries
       (repo_id, path, variant, kind, item_id, blob_sha256, upstream_url, upstream_etag, upstream_last_modified,
        fetched_at, validated_at, expires_at, upstream_gone_at, last_used_at)
VALUES (sqlc.arg(repo_id)::bigint, sqlc.arg(path)::text, sqlc.arg(variant)::text, 'artifact',
        sqlc.arg(item_id)::bigint, sqlc.arg(blob_sha256)::bytea, sqlc.arg(upstream_url)::text,
        NULLIF(sqlc.arg(upstream_etag)::text, ''),
        CASE WHEN sqlc.arg(last_modified_us)::bigint = 0 THEN NULL
             ELSE timestamptz 'epoch' + sqlc.arg(last_modified_us)::bigint * interval '1 microsecond' END,
        clock_timestamp(), clock_timestamp(), NULL, NULL, clock_timestamp())
ON CONFLICT (repo_id, path, variant) DO NOTHING;

-- name: ProxyUrlMapGet :many
SELECT upstream_url FROM remote_url_map
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND path = sqlc.arg(path)::text COLLATE "C";

-- name: ProxyUrlMapPut :exec
INSERT INTO remote_url_map (repo_id, path, upstream_url)
VALUES (sqlc.arg(repo_id)::bigint, sqlc.arg(path)::text, sqlc.arg(upstream_url)::text)
ON CONFLICT (repo_id, path) DO UPDATE
   SET upstream_url = EXCLUDED.upstream_url, recorded_at = now()
 WHERE remote_url_map.upstream_url IS DISTINCT FROM EXCLUDED.upstream_url;

-- name: ProxyUrlMapDelete :exec
-- A document that names the default URL again: the recorded exception is dropped.
DELETE FROM remote_url_map
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND path = sqlc.arg(path)::text COLLATE "C";

-- name: ProxyGoneList :many
-- The documents of a repository that carry a gone mark: the gone packages of the
-- replica's memory (R-REPO-029). The partial index serves it.
SELECT path, variant
  FROM remote_cache_entries
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND upstream_gone_at IS NOT NULL
 ORDER BY path, variant;

-- name: ProxyGoneListAll :many
-- The same for every repository, for the replica's first load.
SELECT repo_id, path, variant
  FROM remote_cache_entries
 WHERE upstream_gone_at IS NOT NULL
 ORDER BY repo_id, path, variant;

-- name: ProxyCachedArtifacts :many
-- The cached artifact paths in the byte range [from, to) of one repository
-- (prefix covers: the caller computes the end of the prefix range), without the
-- entries of a gone mark. The primary key serves the range.
SELECT path
  FROM remote_cache_entries
 WHERE repo_id = sqlc.arg(repo_id)::bigint
   AND kind = 'artifact'
   AND path >= sqlc.arg(range_from)::text COLLATE "C"
   AND (sqlc.arg(range_to)::text = '' OR path < sqlc.arg(range_to)::text COLLATE "C")
 ORDER BY path
 LIMIT sqlc.arg(max_rows)::int;

-- name: ProxyCircuitLoad :many
-- The state of a repository's circuit. open_remaining_us is how long it stays open
-- on the database clock (0 when closed or past), so a replica converts it to its own
-- clock without trusting the two to agree.
SELECT consecutive_failures,
       (open_until IS NOT NULL)::boolean AS has_open_until,
       COALESCE(GREATEST((extract(epoch FROM (open_until - clock_timestamp())) * 1000000)::bigint, 0), 0)::bigint AS open_remaining_us
  FROM remote_upstream_state
 WHERE repo_id = sqlc.arg(repo_id)::bigint;

-- name: ProxyCircuitEnsure :exec
INSERT INTO remote_upstream_state (repo_id) VALUES (sqlc.arg(repo_id)::bigint)
ON CONFLICT (repo_id) DO NOTHING;

-- name: ProxyCircuitFailure :many
-- A counted failure, only while the circuit is closed or half open (open_until
-- absent or past): atomic, so n replicas count each failure once. At the threshold
-- the circuit opens for open_seconds (a 429 passes its Retry-After capped at the
-- period). A failure while the circuit is open matches no row, which tells the
-- caller that another replica opened it (R-REPO-029).
UPDATE remote_upstream_state
   SET consecutive_failures = consecutive_failures + 1,
       last_failure_at = clock_timestamp(),
       last_failure = left(sqlc.arg(last_failure)::text, 1024),
       open_until = CASE
           WHEN sqlc.arg(open_seconds)::bigint > 0
            AND (sqlc.arg(force_open)::boolean OR consecutive_failures + 1 >= sqlc.arg(threshold)::int)
           THEN clock_timestamp() + sqlc.arg(open_seconds)::bigint * interval '1 second'
           ELSE open_until END
 WHERE repo_id = sqlc.arg(repo_id)::bigint
   AND (open_until IS NULL OR open_until <= clock_timestamp())
RETURNING consecutive_failures,
          COALESCE(open_until IS NOT NULL AND open_until > clock_timestamp(), false)::boolean AS opened,
          COALESCE(GREATEST((extract(epoch FROM (open_until - clock_timestamp())) * 1000000)::bigint, 0), 0)::bigint AS open_remaining_us;

-- name: ProxyCircuitSuccess :many
-- A success resets the counter and closes the circuit, and writes only when there is
-- something to reset (healthy traffic causes no writes). was_open tells the caller
-- that it closed an open or half-open circuit and so must notify the other replicas.
WITH old AS (
    SELECT repo_id, open_until IS NOT NULL AS was_open
      FROM remote_upstream_state
     WHERE repo_id = sqlc.arg(repo_id)::bigint
       AND (consecutive_failures > 0 OR open_until IS NOT NULL)
       FOR UPDATE
)
UPDATE remote_upstream_state s
   SET consecutive_failures = 0, open_until = NULL, last_success_at = clock_timestamp()
  FROM old
 WHERE s.repo_id = old.repo_id
RETURNING COALESCE(old.was_open, false)::boolean AS was_open;
