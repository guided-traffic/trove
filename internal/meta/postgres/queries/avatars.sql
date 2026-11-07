-- The statements of the user avatar (R-API-055, internal/avatar). A user has at most one row; its blob is the PNG the server
-- made of the upload. The blob reference triggers of the table queue a replaced or removed blob for collection (R-STOR-024).

-- name: AvatarUpsert :execrows
-- Runs in the link transaction of the blob's commit, whose guard trigger checks that the blob is live (R-STOR-005). An upload of
-- the image the user has already changes nothing and returns 0.
INSERT INTO user_avatars (user_id, blob_sha256, size)
VALUES (sqlc.arg(user_id)::text::uuid, sqlc.arg(blob_sha256)::bytea, sqlc.arg(size)::bigint)
ON CONFLICT (user_id) DO UPDATE SET blob_sha256 = EXCLUDED.blob_sha256, size = EXCLUDED.size, updated_at = now()
 WHERE user_avatars.blob_sha256 <> EXCLUDED.blob_sha256;

-- name: AvatarDelete :many
-- The digest of the removed avatar, no row when the user had none.
DELETE FROM user_avatars WHERE user_id = sqlc.arg(user_id)::text::uuid RETURNING blob_sha256;

-- name: AvatarByDigest :many
-- A digest is served only while some user's avatar is that blob: the lookup is what keeps the route from reading any other
-- blob of the store.
SELECT size FROM user_avatars WHERE blob_sha256 = sqlc.arg(blob_sha256)::bytea LIMIT 1;

-- name: AvatarDigests :many
-- The avatars of a batch of users, by user ID.
SELECT user_id::text AS user_id, blob_sha256 FROM user_avatars WHERE user_id = ANY(sqlc.arg(ids)::text[]::uuid[]);
