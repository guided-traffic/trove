-- Queries of the job queue (R-ARCH-016). The jobs package is a domain package
-- and imports no driver, so every parameter is a plain Go type: identifiers
-- travel as text and are cast to uuid here, times are relative to the
-- database's clock (seconds from now()), and an optional value is the empty
-- string or zero. Statements that return at most one row are :many, so the
-- caller never meets the driver's no-rows error.

-- name: InsertJob :many
INSERT INTO job (id, kind, queue, args, priority, run_at, max_attempts, dedup_key, dedup_mode,
                 task, scheduled_for, target_kind, target_key, created_by, traceparent)
VALUES (sqlc.arg(id)::text::uuid, sqlc.arg(kind)::text, sqlc.arg(queue)::text, sqlc.arg(args)::text::jsonb,
        sqlc.arg(priority)::smallint,
        now() + make_interval(secs => sqlc.arg(delay_secs)::float8),
        sqlc.arg(max_attempts)::int,
        NULLIF(sqlc.arg(dedup_key)::text, ''), NULLIF(sqlc.arg(dedup_mode)::text, ''),
        NULLIF(sqlc.arg(task)::text, ''),
        CASE WHEN sqlc.arg(scheduled_unix)::bigint = 0 THEN NULL ELSE to_timestamp(sqlc.arg(scheduled_unix)::bigint) END,
        NULLIF(sqlc.arg(target_kind)::text, ''), NULLIF(sqlc.arg(target_key)::text, ''),
        NULLIF(sqlc.arg(created_by)::text, '')::uuid, NULLIF(sqlc.arg(traceparent)::text, ''))
ON CONFLICT DO NOTHING
RETURNING *;

-- name: FindActiveJobByKey :many
SELECT * FROM job
 WHERE dedup_key = sqlc.arg(dedup_key)::text AND state IN ('queued', 'running', 'cancelling')
 ORDER BY (state = 'queued') DESC, created_at
 LIMIT 1;

-- name: FindWaitingJobByKey :many
SELECT * FROM job
 WHERE dedup_key = sqlc.arg(dedup_key)::text AND state = 'queued'
 LIMIT 1;

-- name: GetJob :many
SELECT * FROM job WHERE id = sqlc.arg(id)::text::uuid;

-- name: ClaimJobs :many
-- The claim of R-ARCH-016, one statement for every queue the replica has a free
-- worker slot in: queues[i] has slots[i] free slots. A job whose dedup key is held
-- by a running job stays queued (jobs with one key never run together).
WITH next AS (
    SELECT picked.id
      FROM generate_series(1, cardinality(sqlc.arg(queues)::text[])) AS g(i)
     CROSS JOIN LATERAL (
        SELECT j.id FROM job j
         WHERE j.state = 'queued' AND j.queue = (sqlc.arg(queues)::text[])[g.i]
           AND j.kind = ANY(sqlc.arg(kinds)::text[])
           AND j.run_at <= now()
           AND (j.dedup_key IS NULL OR NOT EXISTS (
                  SELECT 1 FROM job r
                   WHERE r.dedup_key = j.dedup_key AND r.state IN ('running', 'cancelling')))
         ORDER BY j.priority, j.run_at
           FOR UPDATE OF j SKIP LOCKED
         LIMIT (sqlc.arg(slots)::int[])[g.i]) AS picked
)
UPDATE job
   SET state = 'running', locked_by = sqlc.arg(locked_by)::text::uuid,
       locked_until = now() + make_interval(secs => sqlc.arg(lease_secs)::float8),
       attempt = job.attempt + 1, started_at = coalesce(job.started_at, now())
  FROM next
 WHERE job.id = next.id
RETURNING job.*;

-- name: HeartbeatJobs :many
-- One statement extends every job of the replica (R-ARCH-016). The rows that
-- come back are the jobs it still owns; a running job missing from them was
-- lost, and one in state cancelling was cancelled.
UPDATE job
   SET locked_until = now() + make_interval(secs => sqlc.arg(lease_secs)::float8)
 WHERE locked_by = sqlc.arg(locked_by)::text::uuid AND state IN ('running', 'cancelling')
RETURNING id, state;

-- name: CompleteJob :execrows
UPDATE job
   SET state = 'succeeded', result = NULLIF(sqlc.arg(result)::text, '')::jsonb, error = NULL,
       locked_by = NULL, locked_until = NULL, finished_at = now()
 WHERE id = sqlc.arg(id)::text::uuid AND locked_by = sqlc.arg(locked_by)::text::uuid
   AND state IN ('running', 'cancelling');

-- name: EndJobCancelled :execrows
UPDATE job
   SET state = 'cancelled', error = NULLIF(sqlc.arg(error)::text, '')::jsonb,
       locked_by = NULL, locked_until = NULL, finished_at = now()
 WHERE id = sqlc.arg(id)::text::uuid AND locked_by = sqlc.arg(locked_by)::text::uuid
   AND state IN ('running', 'cancelling');

-- name: FailOrRetryJob :many
-- A handler failed. The job ends failed when the error is permanent or the
-- attempts are used up; otherwise it returns to queued after the backoff
-- min(2^attempt s, 1 h) +-20 % (R-ARCH-016). A coalesced job that would return
-- to queued while another waits ends cancelled, superseded.
WITH cur AS (
    SELECT j.id,
           CASE WHEN sqlc.arg(permanent)::bool OR j.attempt >= j.max_attempts THEN 'failed'
                WHEN j.dedup_key IS NOT NULL AND EXISTS (
                       SELECT 1 FROM job o WHERE o.dedup_key = j.dedup_key AND o.state = 'queued' AND o.id <> j.id)
                     THEN 'superseded'
                ELSE 'queued' END AS outcome
      FROM job j
     WHERE j.id = sqlc.arg(id)::text::uuid AND j.locked_by = sqlc.arg(locked_by)::text::uuid
       AND j.state IN ('running', 'cancelling')
       FOR UPDATE
)
UPDATE job
   SET state = CASE cur.outcome WHEN 'failed' THEN 'failed'::job_state WHEN 'superseded' THEN 'cancelled'::job_state
                                ELSE 'queued'::job_state END,
       error = CASE cur.outcome WHEN 'superseded' THEN '{"code":"superseded"}'::jsonb ELSE sqlc.arg(error)::text::jsonb END,
       run_at = CASE cur.outcome WHEN 'queued'
                  THEN now() + make_interval(secs => least(power(2, job.attempt)::float8, 3600) * (0.8 + 0.4 * random()))
                  ELSE job.run_at END,
       locked_by = NULL, locked_until = NULL,
       finished_at = CASE cur.outcome WHEN 'queued' THEN NULL ELSE now() END
  FROM cur
 WHERE job.id = cur.id
RETURNING job.state, job.attempt;

-- name: ReleaseJob :many
-- Release on shutdown (R-ARCH-016): the job returns to queued without counting
-- the interrupted attempt, keeping its checkpoint. A job in cancelling ends
-- cancelled instead.
WITH cur AS (
    SELECT j.id,
           CASE WHEN j.state = 'cancelling' THEN 'cancelled'
                WHEN j.dedup_key IS NOT NULL AND EXISTS (
                       SELECT 1 FROM job o WHERE o.dedup_key = j.dedup_key AND o.state = 'queued' AND o.id <> j.id)
                     THEN 'superseded'
                ELSE 'queued' END AS outcome
      FROM job j
     WHERE j.id = sqlc.arg(id)::text::uuid AND j.locked_by = sqlc.arg(locked_by)::text::uuid
       AND j.state IN ('running', 'cancelling')
       FOR UPDATE
)
UPDATE job
   SET state = CASE cur.outcome WHEN 'queued' THEN 'queued'::job_state ELSE 'cancelled'::job_state END,
       attempt = CASE cur.outcome WHEN 'queued' THEN job.attempt - 1 ELSE job.attempt END,
       error = CASE cur.outcome WHEN 'superseded' THEN '{"code":"superseded"}'::jsonb
                                WHEN 'cancelled' THEN '{"code":"cancelled"}'::jsonb ELSE job.error END,
       run_at = now(), locked_by = NULL, locked_until = NULL,
       finished_at = CASE cur.outcome WHEN 'queued' THEN NULL ELSE now() END
  FROM cur
 WHERE job.id = cur.id
RETURNING job.state;

-- name: ReapJobs :many
-- The reaper (R-ARCH-016): jobs whose lease ran out on the database's clock.
WITH expired AS (
    SELECT j.id,
           CASE WHEN j.state = 'cancelling' THEN 'cancelled'
                WHEN j.attempt >= j.max_attempts THEN 'failed'
                WHEN j.dedup_key IS NOT NULL AND EXISTS (
                       SELECT 1 FROM job o WHERE o.dedup_key = j.dedup_key AND o.state = 'queued' AND o.id <> j.id)
                     THEN 'superseded'
                ELSE 'queued' END AS outcome
      FROM job j
     WHERE j.state IN ('running', 'cancelling') AND j.locked_until < now()
     ORDER BY j.locked_until
       FOR UPDATE SKIP LOCKED
     LIMIT sqlc.arg(batch)::int
)
UPDATE job
   SET state = CASE expired.outcome WHEN 'queued' THEN 'queued'::job_state WHEN 'failed' THEN 'failed'::job_state
                                    ELSE 'cancelled'::job_state END,
       error = CASE expired.outcome
                 WHEN 'failed' THEN '{"code":"attempts_exhausted","detail":"the lease expired on the last attempt"}'::jsonb
                 WHEN 'superseded' THEN '{"code":"superseded"}'::jsonb
                 WHEN 'cancelled' THEN '{"code":"cancelled"}'::jsonb
                 ELSE job.error END,
       run_at = CASE expired.outcome WHEN 'queued'
                  THEN now() + make_interval(secs => least(power(2, job.attempt)::float8, 3600) * (0.8 + 0.4 * random()))
                  ELSE job.run_at END,
       locked_by = NULL, locked_until = NULL,
       finished_at = CASE expired.outcome WHEN 'queued' THEN NULL ELSE now() END
  FROM expired
 WHERE job.id = expired.id
RETURNING job.id, job.kind, job.state, job.attempt;

-- name: CancelJob :many
-- A queued job is cancelled at once; a running one is marked cancelling and its
-- handler ends it at its next checkpoint (R-ARCH-016).
UPDATE job
   SET state = CASE WHEN state = 'queued' THEN 'cancelled'::job_state ELSE 'cancelling'::job_state END,
       error = CASE WHEN state = 'queued' THEN '{"code":"cancelled"}'::jsonb ELSE error END,
       finished_at = CASE WHEN state = 'queued' THEN now() ELSE finished_at END
 WHERE id = sqlc.arg(id)::text::uuid AND state IN ('queued', 'running')
RETURNING *;

-- name: SaveJobCheckpoint :execrows
UPDATE job SET checkpoint = sqlc.arg(checkpoint)::text::jsonb
 WHERE id = sqlc.arg(id)::text::uuid AND locked_by = sqlc.arg(locked_by)::text::uuid
   AND state IN ('running', 'cancelling');

-- name: SaveJobProgress :execrows
UPDATE job SET progress_done = sqlc.arg(done)::bigint, progress_total = NULLIF(sqlc.arg(total)::bigint, 0),
               progress_unit = NULLIF(sqlc.arg(unit)::text, '')
 WHERE id = sqlc.arg(id)::text::uuid AND locked_by = sqlc.arg(locked_by)::text::uuid
   AND state IN ('running', 'cancelling');

-- name: DeleteFinishedJobs :execrows
-- Retention (R-ARCH-016): finished jobs older than jobs.retention, except the
-- job of each task's most recent slot, which keeps job_task_slot refusing it.
WITH old AS (
    SELECT j.id FROM job j
     WHERE j.finished_at < now() - make_interval(secs => sqlc.arg(retention_secs)::float8)
       AND NOT (j.task IS NOT NULL
                AND j.scheduled_for IS NOT DISTINCT FROM (SELECT max(l.scheduled_for) FROM job l WHERE l.task = j.task))
     ORDER BY j.finished_at
       FOR UPDATE SKIP LOCKED
     LIMIT sqlc.arg(batch)::int
)
DELETE FROM job USING old WHERE job.id = old.id;

-- name: DatabaseNow :one
SELECT now()::timestamptz AS now;
