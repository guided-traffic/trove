-- The statements of the blob store (R-STOR-005, R-STOR-024, R-STOR-013). Queries
-- that may find no row are :many, so that the caller needs no driver error
-- value to tell an empty result from a failure.

-- name: LockBlobShare :many
-- The first statement of the commit protocol (R-STOR-005).
SELECT state, content_status FROM blobs WHERE sha256 = $1 FOR SHARE;

-- name: InsertLiveBlob :execrows
INSERT INTO blobs (sha256, sha1, md5, sha512, size, state)
VALUES ($1, $2, $3, $4, $5, 'live')
ON CONFLICT DO NOTHING;

-- name: BlobExists :many
SELECT sha256 FROM blobs WHERE sha256 = $1;

-- name: MarkBlobOK :exec
UPDATE blobs SET content_status = 'ok' WHERE sha256 = $1 AND state = 'live';

-- name: EnqueueBlobForGC :exec
INSERT INTO blob_gc_queue (sha256, enqueued_at) VALUES ($1, now())
ON CONFLICT (sha256) DO UPDATE SET enqueued_at = EXCLUDED.enqueued_at;

-- name: GetBlob :many
SELECT sha256, sha1, md5, sha512, size, state, content_status, created_at
  FROM blobs WHERE sha256 = $1;

-- name: MarkBlobDamaged :execrows
-- Only a live row that was ok changes, so a repair or a concurrent detection is
-- not overwritten.
UPDATE blobs SET content_status = $2
 WHERE sha256 = $1 AND state = 'live' AND content_status = 'ok';

-- name: DeletionsPaused :many
SELECT 1::int AS paused FROM system_state
 WHERE key = 'deletions_paused_at' AND value <> 'null'::jsonb;

-- name: BlobStateSize :many
SELECT state, size FROM blobs WHERE sha256 = $1;

-- name: LockBlobUpdate :many
-- Step (1) of the collection protocol: the row lock that makes a committing
-- uploader and the collector see each other.
SELECT state, size FROM blobs WHERE sha256 = $1 FOR UPDATE;

-- name: ClaimBlob :exec
-- Step (3).
UPDATE blobs SET state = 'deleting', state_changed_at = now() WHERE sha256 = $1;

-- name: DequeueBlob :exec
-- Removes the queue entry only if it is the one that was read: an entry that was
-- enqueued again in between stays.
DELETE FROM blob_gc_queue WHERE sha256 = $1 AND enqueued_at = $2;

-- name: BlobQueueCandidates :many
SELECT sha256, enqueued_at FROM blob_gc_queue
 WHERE enqueued_at < now() - make_interval(secs => @grace_secs::float8) AND sha256 > @after
 ORDER BY sha256 LIMIT @batch;

-- name: RecordBlobsDeleted :many
-- Step (5).
UPDATE blobs SET object_deleted_at = now()
 WHERE sha256 = ANY(@digests::bytea[]) AND state = 'deleting' AND object_deleted_at IS NULL
RETURNING size;

-- name: DropBlobRows :exec
-- Step (6).
DELETE FROM blobs
 WHERE sha256 = ANY(@digests::bytea[]) AND state = 'deleting'
   AND object_deleted_at < now() - make_interval(secs => @settle_secs::float8);

-- name: EligibleBlobs :many
-- The rows earlier runs left: object deleted and settled (step 6), or claimed and
-- not finished within handover_delay (steps 4 to 6).
SELECT sha256, size, (object_deleted_at IS NOT NULL)::boolean AS object_deleted FROM blobs
 WHERE state = 'deleting' AND (
       (object_deleted_at IS NULL AND state_changed_at < now() - make_interval(secs => @handover_secs::float8))
    OR (object_deleted_at IS NOT NULL AND object_deleted_at < now() - make_interval(secs => @settle_secs::float8)))
 ORDER BY state_changed_at LIMIT @batch;

-- name: TakeOverBlobs :many
-- Compare-and-set claim of rows another run left: the row is taken only while it
-- is still as old as the run saw it, and taking it renews state_changed_at, so
-- that of two runs that read the same row exactly one takes it and the other
-- finds it fresh (READ COMMITTED re-evaluates the condition after the lock wait).
UPDATE blobs SET state_changed_at = now()
 WHERE sha256 = ANY(@digests::bytea[]) AND state = 'deleting' AND object_deleted_at IS NULL
   AND state_changed_at < now() - make_interval(secs => @handover_secs::float8)
RETURNING sha256, size;

-- name: CountDeletingSoon :one
SELECT count(*) FROM blobs
 WHERE state = 'deleting' AND (object_deleted_at IS NOT NULL
    OR state_changed_at < now() - make_interval(secs => @handover_secs::float8));

-- name: HandOverBlobs :exec
UPDATE blobs SET state_changed_at = now() - make_interval(secs => @back_secs::float8)
 WHERE sha256 = ANY(@digests::bytea[]) AND state = 'deleting' AND object_deleted_at IS NULL;

-- name: CountDeletingBlobs :one
SELECT count(*) FROM blobs WHERE state = 'deleting';

-- name: BlobReferencingColumns :many
-- Every single-column foreign key to blobs (R-STOR-024 rule 3), so that a table
-- a newer release added is honoured by an older binary. The names are returned
-- unquoted; BlobRefs quotes them with pgx.Identifier.
SELECT n.nspname::text AS schema_name, cl.relname::text AS table_name, a.attname::text AS column_name
  FROM pg_catalog.pg_constraint c
  JOIN pg_catalog.pg_class cl ON cl.oid = c.conrelid
  JOIN pg_catalog.pg_namespace n ON n.oid = cl.relnamespace
  JOIN pg_catalog.pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = c.conkey[1]
 WHERE c.contype = 'f' AND c.confrelid = 'blobs'::regclass AND cardinality(c.conkey) = 1
 ORDER BY n.nspname, cl.relname, a.attname;
