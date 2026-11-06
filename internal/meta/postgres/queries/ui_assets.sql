-- The statements of the UI's shared asset store (R-HA-020, Q-HA-120 to Q-HA-122, Q-UI-142, internal/webui/shared). A row is
-- keyed by the locale directory and the path below it, and a replica's file is told by its bytes as well: the hash in a
-- file's name covers the code before the translations are inlined, so two builds can fill one name with different bytes
-- (Q-UI-140). A build's files travel as one JSON array of {"locale", "path", "sha256" (hex)} objects, because sqlc has no
-- multi-array unnest.

-- name: GetUIAsset :many
-- The lookup of a request for a hashed file the replica's own build lacks: the primary key.
SELECT blob_sha256, size FROM ui_assets
 WHERE locale = sqlc.arg(locale)::text AND path = sqlc.arg(path)::text;

-- name: UIAssetsPresent :many
-- Which of a build's files have a row with the build's own bytes whose blob is live and not marked damaged. A row with other
-- bytes, or whose object a read found missing or corrupt (R-STOR-013), counts as missing: the replica commits its file, the
-- commit repairs an object of the same bytes, and InsertUIAsset decides whether the row takes the replica's bytes.
SELECT (a.locale || '/' || a.path)::text AS name
  FROM jsonb_to_recordset(sqlc.arg(files)::text::jsonb) AS f(locale text, path text, sha256 text)
  JOIN ui_assets a ON a.locale = f.locale AND a.path = f.path AND a.blob_sha256 = decode(f.sha256, 'hex')
  JOIN blobs b ON b.sha256 = a.blob_sha256 AND b.state = 'live' AND b.content_status = 'ok';

-- name: InsertUIAsset :execrows
-- The row of a name another replica wrote first stays, with two exceptions (Q-UI-142): its blob is not live and ok any more,
-- or no replica whose build has its bytes renewed it within stale_secs. Then the row takes this replica's bytes, and the
-- update trigger queues the old blob for collection (R-STOR-003). Runs in the link transaction of the blob's commit, whose
-- guard trigger checks the new blob is live under the commit's lock (R-STOR-005).
INSERT INTO ui_assets (locale, path, blob_sha256, size, published_by)
VALUES (sqlc.arg(locale)::text, sqlc.arg(path)::text, sqlc.arg(blob_sha256)::bytea, sqlc.arg(size)::bigint, sqlc.arg(published_by)::text)
ON CONFLICT (locale, path) DO UPDATE
   SET blob_sha256 = EXCLUDED.blob_sha256, size = EXCLUDED.size, published_by = EXCLUDED.published_by,
       published_at = now(), seen_at = now()
 WHERE ui_assets.blob_sha256 <> EXCLUDED.blob_sha256
   AND (ui_assets.seen_at < now() - make_interval(secs => sqlc.arg(stale_secs)::float8)
        OR NOT EXISTS (SELECT 1 FROM blobs b
                        WHERE b.sha256 = ui_assets.blob_sha256 AND b.state = 'live' AND b.content_status = 'ok'));

-- name: TouchUIAssets :execrows
-- A replica renews the rows that hold its build's own bytes, never a row of the same name with other bytes; a row renewed
-- within fresh_secs is left alone, so that the replicas of one release do not all rewrite the same rows.
UPDATE ui_assets a SET seen_at = now()
  FROM jsonb_to_recordset(sqlc.arg(files)::text::jsonb) AS f(locale text, path text, sha256 text)
 WHERE a.locale = f.locale AND a.path = f.path AND a.blob_sha256 = decode(f.sha256, 'hex')
   AND a.seen_at < now() - make_interval(secs => sqlc.arg(fresh_secs)::float8);

-- name: PruneUIAssets :execrows
-- The rows no replica renewed within the retention (Q-HA-122). The delete trigger queues their blobs for collection.
DELETE FROM ui_assets WHERE seen_at < now() - make_interval(secs => sqlc.arg(retention_secs)::float8);
