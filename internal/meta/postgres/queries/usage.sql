-- The statements of the usage fold (R-STOR-003, Q-STOR-12). One iteration runs
-- FoldTakeUsageDeltas, then FoldUpsertRepositoryUsage and FoldAddPhysicalUsage in
-- one transaction.

-- name: FoldTakeUsageDeltas :many
-- Takes up to @batch of the oldest delta rows that no other fold holds and
-- returns their sums per repository, the physical ones (repo_id NULL) last, so
-- that every fold updates the counter rows in this one order and two folds never
-- wait for each other in a cycle. taken is the number of delta rows in the group. The
-- group of the physical deltas has physical set and repo 0.
WITH d AS (
    DELETE FROM storage_usage_deltas
     WHERE id IN (SELECT u.id FROM storage_usage_deltas u ORDER BY u.id LIMIT sqlc.arg(batch)::int
                  FOR UPDATE SKIP LOCKED)
    RETURNING repo_id, logical_bytes, files, folders, physical_bytes, blobs)
SELECT COALESCE(repo_id, 0)::bigint AS repo,
       (repo_id IS NULL)::bool AS physical,
       sum(logical_bytes)::bigint AS logical_bytes,
       sum(files)::bigint AS files,
       sum(folders)::bigint AS folders,
       sum(physical_bytes)::bigint AS physical_bytes,
       sum(blobs)::bigint AS blobs,
       count(*)::bigint AS taken
  FROM d GROUP BY repo_id ORDER BY repo_id NULLS LAST;

-- name: FoldUpsertRepositoryUsage :exec
-- groups is the JSON array of the repository groups of FoldTakeUsageDeltas in its order. The
-- join drops the deltas of a repository that is gone, and the key share lock
-- keeps it from going between this statement's snapshot and its insert; rows
-- are inserted, and so locked, in the order of the array.
INSERT INTO repository_usage (repo_id, logical_bytes, files, folders, updated_at)
SELECT r.id, (e.elem->>'logical_bytes')::bigint, (e.elem->>'files')::bigint, (e.elem->>'folders')::bigint, now()
  FROM jsonb_array_elements(sqlc.arg(groups)::text::jsonb) WITH ORDINALITY AS e(elem, ord)
  JOIN repositories r ON r.id = (e.elem->>'repo_id')::bigint
 ORDER BY e.ord
   FOR KEY SHARE OF r
ON CONFLICT (repo_id) DO UPDATE
   SET logical_bytes = repository_usage.logical_bytes + EXCLUDED.logical_bytes,
       files         = repository_usage.files + EXCLUDED.files,
       folders       = repository_usage.folders + EXCLUDED.folders,
       updated_at    = now();

-- name: FoldAddPhysicalUsage :exec
UPDATE storage_usage_totals
   SET physical_bytes = physical_bytes + @physical_bytes::bigint,
       blobs          = blobs + @blobs::bigint,
       updated_at     = now()
 WHERE id = 1;
