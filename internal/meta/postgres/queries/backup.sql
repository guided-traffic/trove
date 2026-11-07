-- Queries of the backup lane (R-BAK-007, R-BAK-009, R-BAK-010, R-BAK-014, R-HA-054). The backup package is a domain
-- package and imports no driver, so identifiers travel as text and are cast here, and an optional value is the empty
-- string or zero. Statements that return at most one row are :many, so the caller never meets the driver's no-rows error.
--
-- The run rows are written by the backup pod only (the backup database role, R-BAK-015); the web and worker replicas read
-- them for the API and the gauges, and the collector reads the hold (R-STOR-024 step 2b).

-- name: AbandonExpiredBackupRuns :many
-- The first statement of the insert transaction of R-BAK-010: runs of any operation whose lease passed end 'abandoned'
-- and give up their hold. The caller deletes their pins in the same transaction.
UPDATE backup_run
   SET state = 'abandoned', hold = 'none', finished_at = now(), error = '{"code": "lease_expired"}'
 WHERE state IN ('starting', 'snapshot', 'copying', 'finalizing', 'pruning')
   AND lease_expires_at < now()
RETURNING id::text AS id;

-- name: DeleteBackupPinsOfAbandonedRuns :exec
DELETE FROM backup_blob_pin WHERE run_id IN (SELECT id FROM backup_run WHERE state = 'abandoned');

-- name: InsertBackupRun :exec
-- A unique violation on backup_run_one_active (SQLSTATE 23505) means another target-writing run holds the slot
-- (R-BAK-010). The lease is relative to the database's clock, so a pod's clock never decides.
INSERT INTO backup_run (id, operation, backup_id, state, hold, trigger, target, pod, trove_version, lease_expires_at)
VALUES (sqlc.arg(id)::text::uuid, sqlc.arg(operation)::text, NULLIF(sqlc.arg(backup_id)::text, ''), 'starting', 'none',
        sqlc.arg(trigger)::text, sqlc.arg(target)::text, sqlc.arg(pod)::text, sqlc.arg(trove_version)::text,
        now() + make_interval(secs => sqlc.arg(lease_secs)::float8));

-- name: GetBackupRun :many
SELECT * FROM backup_run WHERE id = sqlc.arg(id)::text::uuid;

-- name: ActiveTargetWritingRun :many
-- The run that holds the slot, for the exit-3 message and the wait for a stale lease (R-BAK-010).
SELECT * FROM backup_run
 WHERE operation IN ('create', 'delete') AND state IN ('starting', 'snapshot', 'copying', 'finalizing', 'pruning')
 LIMIT 1;

-- name: HeartbeatBackupRun :execrows
-- The fenced heartbeat of R-BAK-010: it matches no row when the run was abandoned or ended, which cancels the run
-- (exit 1, superseded). lease_secs is zero for a heartbeat without progress (R-BAK-013): the lease is not renewed, only
-- the liveness stamp is.
UPDATE backup_run
   SET heartbeat_at = now(),
       lease_expires_at = CASE WHEN sqlc.arg(lease_secs)::float8 > 0 THEN now() + make_interval(secs => sqlc.arg(lease_secs)::float8) ELSE lease_expires_at END,
       progress = sqlc.arg(progress)::text::jsonb,
       snapshot_at = COALESCE(NULLIF(sqlc.arg(snapshot_at)::text, '')::timestamptz, snapshot_at)
 WHERE id = sqlc.arg(id)::text::uuid AND state = sqlc.arg(expected_state)::text;

-- name: SetBackupRunState :execrows
-- A phase change of a running create or delete: fenced on the expected state. backup_id is set once it is known.
UPDATE backup_run
   SET state = sqlc.arg(state)::text, backup_id = COALESCE(NULLIF(sqlc.arg(backup_id)::text, ''), backup_id)
 WHERE id = sqlc.arg(id)::text::uuid AND state = sqlc.arg(expected_state)::text;

-- name: SetBackupRunHold :execrows
-- Steps 1 and 4 of the protocol of R-BAK-007 ('none' to 'all', 'all' to 'pinned'), each a compare-and-set of its own
-- transaction at READ COMMITTED.
UPDATE backup_run SET hold = sqlc.arg(hold)::text
 WHERE id = sqlc.arg(id)::text::uuid AND hold = sqlc.arg(expected_hold)::text
   AND state IN ('starting', 'snapshot', 'copying');

-- name: FenceBackupRunFinalizing :execrows
-- The fenced lease check before the manifest or a pruning deletion (R-BAK-010): one row changed means the lease still
-- has room for the write's own deadline. margin_secs is 2 x backup.finalize_timeout.
UPDATE backup_run SET state = sqlc.arg(state)::text
 WHERE id = sqlc.arg(id)::text::uuid AND state = sqlc.arg(expected_state)::text
   AND lease_expires_at > now() + make_interval(secs => sqlc.arg(margin_secs)::float8);

-- name: FinishBackupRun :execrows
-- The terminal transition. It gives up the hold and the pins stay for the caller to delete in the same transaction.
UPDATE backup_run
   SET state = sqlc.arg(state)::text, hold = 'none', finished_at = now(),
       result = NULLIF(sqlc.arg(result)::text, '')::jsonb,
       error = NULLIF(sqlc.arg(error)::text, '')::jsonb
 WHERE id = sqlc.arg(id)::text::uuid AND state IN ('starting', 'snapshot', 'copying', 'finalizing', 'pruning');

-- name: InsertBackupPins :exec
-- The to-copy set (R-BAK-006 step 2), in batches. digests and sizes are parallel arrays.
INSERT INTO backup_blob_pin (run_id, sha256, size)
SELECT sqlc.arg(run_id)::text::uuid, unnest(sqlc.arg(digests)::bytea[]), unnest(sqlc.arg(sizes)::bigint[])
ON CONFLICT DO NOTHING;

-- name: DeleteBackupPins :execrows
-- Pins are deleted in batches of up to 1,000 after their copy is confirmed (R-BAK-006).
DELETE FROM backup_blob_pin WHERE run_id = sqlc.arg(run_id)::text::uuid AND sha256 = ANY(sqlc.arg(digests)::bytea[]);

-- name: DeleteBackupPinsOfRun :execrows
DELETE FROM backup_blob_pin WHERE run_id = sqlc.arg(run_id)::text::uuid;

-- name: CountBackupPinsOfRun :one
SELECT count(*)::bigint FROM backup_blob_pin WHERE run_id = sqlc.arg(run_id)::text::uuid;

-- name: DeleteOldBackupRuns :execrows
-- backup.run_history: finished rows older than the history are deleted by the next run (R-BAK-009).
DELETE FROM backup_run
 WHERE finished_at IS NOT NULL AND finished_at < now() - make_interval(secs => sqlc.arg(history_secs)::float8);

-- name: ListBackupRuns :many
-- Newest first, keyset after (cursor_at, cursor_id) when has_cursor (R-API-050). The empty string is no filter.
SELECT * FROM backup_run
 WHERE (sqlc.arg(operation)::text = '' OR operation = sqlc.arg(operation)::text)
   AND (sqlc.arg(state)::text = '' OR state = sqlc.arg(state)::text)
   AND (sqlc.arg(trigger)::text = '' OR trigger = sqlc.arg(trigger)::text)
   AND (sqlc.arg(started_from)::text = '' OR started_at >= sqlc.arg(started_from)::text::timestamptz)
   AND (sqlc.arg(started_to)::text = '' OR started_at < sqlc.arg(started_to)::text::timestamptz)
   AND (NOT sqlc.arg(has_cursor)::boolean OR (started_at, id) < (sqlc.arg(cursor_at)::text::timestamptz, sqlc.arg(cursor_id)::text::uuid))
 ORDER BY started_at DESC, id DESC
 LIMIT sqlc.arg(row_limit)::int;

-- name: CountBackupRuns :one
SELECT count(*)::bigint FROM backup_run
 WHERE (sqlc.arg(operation)::text = '' OR operation = sqlc.arg(operation)::text)
   AND (sqlc.arg(state)::text = '' OR state = sqlc.arg(state)::text)
   AND (sqlc.arg(trigger)::text = '' OR trigger = sqlc.arg(trigger)::text)
   AND (sqlc.arg(started_from)::text = '' OR started_at >= sqlc.arg(started_from)::text::timestamptz)
   AND (sqlc.arg(started_to)::text = '' OR started_at < sqlc.arg(started_to)::text::timestamptz);

-- name: LastBackupRunByOutcome :many
-- The newest finished run of an operation among the given states, for the gauges and the summary (R-BAK-014). One cheap
-- query on a small table; no query counts backup_blob_pin.
SELECT * FROM backup_run
 WHERE operation = sqlc.arg(operation)::text AND state = ANY(sqlc.arg(states)::text[])
 ORDER BY finished_at DESC NULLS LAST, id DESC
 LIMIT 1;

-- name: ActiveBackupRuns :many
-- Runs in an active state, with their lease: the gauges read the valid ones, the failure time the expired ones.
SELECT * FROM backup_run
 WHERE state IN ('starting', 'snapshot', 'copying', 'finalizing', 'pruning')
 ORDER BY started_at DESC;

-- name: BackupBlobHeld :one
-- The hold check of the collector's per-blob transaction (R-STOR-024 step 2b, R-BAK-007): after the reference check,
-- before the state change, as a separate statement. True keeps the blob live.
SELECT (EXISTS (SELECT 1 FROM backup_run r
                WHERE r.hold = 'all' AND r.lease_expires_at > now())
    OR EXISTS (SELECT 1 FROM backup_blob_pin p JOIN backup_run r ON r.id = p.run_id
                WHERE p.sha256 = sqlc.arg(sha256)::bytea AND r.hold = 'pinned' AND r.lease_expires_at > now()))::boolean AS held;

-- name: BackupHeldDigests :many
-- The batch form: the digests of the batch that a pin holds. A valid 'all' hold holds every digest, which the caller
-- finds with BackupGlobalHoldValid.
SELECT p.sha256 FROM backup_blob_pin p JOIN backup_run r ON r.id = p.run_id
 WHERE p.sha256 = ANY(sqlc.arg(digests)::bytea[]) AND r.hold = 'pinned' AND r.lease_expires_at > now();

-- name: BackupGlobalHoldValid :one
-- The early exit of a collection run (R-BAK-007, "Skipping a run") and the gauge trove_backup_gc_hold{mode="all"}.
SELECT EXISTS (SELECT 1 FROM backup_run WHERE hold = 'all' AND lease_expires_at > now()) AS held;

-- name: BackupPinnedHoldValid :one
SELECT EXISTS (SELECT 1 FROM backup_run WHERE hold = 'pinned' AND lease_expires_at > now()) AS held;

-- name: CountRevocationOutbox :one
-- trove_revocation_journal_pending (R-HA-054): rows the shipper has not yet written and deleted.
SELECT count(*)::bigint FROM revocation_outbox;

-- name: OldestRevocationOutbox :many
SELECT at FROM revocation_outbox ORDER BY id LIMIT 1;

-- name: ClaimRevocationOutbox :many
-- The shipper takes rows in id order (R-HA-054). The worker job holds a singleton lease, so a plain read is enough; a row
-- whose object was written and whose deletion did not commit is shipped again under the same key.
SELECT id::text AS id, at, event, entry FROM revocation_outbox ORDER BY id LIMIT sqlc.arg(row_limit)::int;

-- name: DeleteRevocationOutboxRows :execrows
DELETE FROM revocation_outbox WHERE id = ANY(sqlc.arg(ids)::text[]::uuid[]);
