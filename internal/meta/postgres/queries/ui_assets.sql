-- The statements of the UI's shared asset store (R-HA-020, Q-HA-120 to Q-HA-122, internal/webui/shared). A file is named
-- by its locale directory and its path below it; a list of files travels as one array of "<locale>/<path>" names, because
-- a locale never holds a slash and sqlc has no multi-array unnest.

-- name: GetUIAsset :many
-- The lookup of a request for a hashed file the replica's own build lacks: the primary key.
SELECT blob_sha256, size FROM ui_assets
 WHERE locale = sqlc.arg(locale)::text AND path = sqlc.arg(path)::text;

-- name: UIAssetsPresent :many
-- Which of a build's files have a row, whoever published it.
SELECT (a.locale || '/' || a.path)::text AS name
  FROM unnest(sqlc.arg(names)::text[]) AS n(name)
  JOIN ui_assets a ON a.locale = split_part(n.name, '/', 1) AND a.path = substr(n.name, strpos(n.name, '/') + 1);

-- name: InsertUIAsset :execrows
-- Write-once: a row another replica inserted first stays as it is. Runs in the link transaction of the blob's commit, whose
-- guard trigger checks the blob is live under the commit's lock (R-STOR-005).
INSERT INTO ui_assets (locale, path, blob_sha256, size, published_by)
VALUES (sqlc.arg(locale)::text, sqlc.arg(path)::text, sqlc.arg(blob_sha256)::bytea, sqlc.arg(size)::bigint, sqlc.arg(published_by)::text)
ON CONFLICT (locale, path) DO NOTHING;

-- name: TouchUIAssets :execrows
-- A replica renews the rows of its build's files; a row renewed within fresh_secs is left alone, so that the replicas of
-- one release do not all rewrite the same rows.
UPDATE ui_assets a SET seen_at = now()
  FROM unnest(sqlc.arg(names)::text[]) AS n(name)
 WHERE a.locale = split_part(n.name, '/', 1) AND a.path = substr(n.name, strpos(n.name, '/') + 1)
   AND a.seen_at < now() - make_interval(secs => sqlc.arg(fresh_secs)::float8);

-- name: PruneUIAssets :execrows
-- The rows no replica renewed within the retention (Q-HA-122). The delete trigger queues their blobs for collection.
DELETE FROM ui_assets WHERE seen_at < now() - make_interval(secs => sqlc.arg(retention_secs)::float8);
