-- Queries of the management API conventions (M1, WP1.6): idempotency keys
-- (R-API-014), the job and task views (R-OPS-019), the key-ring status
-- (R-API-049) and the live-replica count of the rate-limit fallback
-- (R-ARCH-034). The api/v1 package is an edge package and imports no driver,
-- so every parameter is a plain Go type: identifiers and times travel as text
-- and are cast here, and an optional value is NULL or the empty string.

-- name: SetLocalLockTimeout :one
-- The wait of a duplicate idempotent request on the row of the first one
-- (R-API-014): SET LOCAL through set_config, so it ends with the transaction and
-- works behind a transaction-mode pooler (R-ARCH-019).
SELECT set_config('lock_timeout', sqlc.arg(millis)::text, true)::text AS lock_timeout;

-- name: InsertIdempotencyKey :many
-- Claims the key for this request. The row is inserted in the transaction of the
-- change; a concurrent duplicate blocks on it (primary-key conflict) until this
-- transaction ends. A row past expires_at is recycled. No row comes back when
-- the key is taken by a live row: the caller reads it with GetIdempotencyKey.
INSERT INTO api_idempotency_keys (principal_id, idem_key, method, route, request_sha256, response_status, expires_at)
VALUES (sqlc.arg(principal_id)::text::uuid, sqlc.arg(idem_key)::text, sqlc.arg(method)::text, sqlc.arg(route)::text,
        sqlc.arg(request_sha256)::bytea, 0, now() + make_interval(secs => sqlc.arg(retention_secs)::float8))
ON CONFLICT (principal_id, idem_key) DO UPDATE
   SET method = EXCLUDED.method, route = EXCLUDED.route, request_sha256 = EXCLUDED.request_sha256,
       response_status = 0, response_headers = '{}', response_body = NULL, secret_issued_id = NULL,
       created_at = now(), expires_at = EXCLUDED.expires_at
 WHERE api_idempotency_keys.expires_at <= now()
RETURNING principal_id;

-- name: GetIdempotencyKey :many
SELECT method, route, request_sha256, response_status, response_headers::text AS response_headers,
       coalesce(response_body::text, '')::text AS response_body, secret_issued_id
  FROM api_idempotency_keys
 WHERE principal_id = sqlc.arg(principal_id)::text::uuid AND idem_key = sqlc.arg(idem_key)::text
   AND expires_at > now();

-- name: CompleteIdempotencyKey :one
-- Stores the outcome in the transaction of the change. The body comes back in the
-- database's rendering, which the first answer and every replay send alike, so
-- both are byte-identical. A response that carried a secret stores no body.
UPDATE api_idempotency_keys
   SET response_status = sqlc.arg(status)::smallint,
       response_headers = sqlc.arg(headers)::text::jsonb,
       response_body = NULLIF(sqlc.arg(body)::text, '')::jsonb,
       secret_issued_id = NULLIF(sqlc.arg(secret_id)::text, '')
 WHERE principal_id = sqlc.arg(principal_id)::text::uuid AND idem_key = sqlc.arg(idem_key)::text
RETURNING COALESCE(response_body::text, '')::text AS response_body;

-- name: DeleteExpiredIdempotencyKeys :execrows
-- The hourly task idempotency-cleanup (R-API-014).
DELETE FROM api_idempotency_keys WHERE expires_at <= now();

-- name: ListJobsNewestFirst :many
-- The job list (R-OPS-019), newest first. after_at and after_id are the sort key
-- of the last item of the previous page (both empty on the first page);
-- created_by restricts the list to the jobs a caller started (R-API-015).
SELECT j.id, j.kind, j.state::text AS state, j.attempt, j.max_attempts, j.created_by, j.target_kind, j.target_key,
       j.progress_done, j.progress_total, j.progress_unit, j.result, j.error, j.created_at, j.started_at, j.finished_at,
       j.ran_by, r.name AS replica_name
  FROM job j
  LEFT JOIN replica r ON r.instance_uid = j.ran_by
 WHERE (cardinality(sqlc.arg(kinds)::text[]) = 0 OR j.kind = ANY(sqlc.arg(kinds)::text[]))
   AND (cardinality(sqlc.arg(states)::text[]) = 0 OR j.state::text = ANY(sqlc.arg(states)::text[]))
   AND (sqlc.arg(created_by)::text = '' OR j.created_by = sqlc.arg(created_by)::text::uuid)
   AND (sqlc.arg(after_at)::text = '' OR (j.created_at, j.id) < (sqlc.arg(after_at)::text::timestamptz, sqlc.arg(after_id)::text::uuid))
 ORDER BY j.created_at DESC, j.id DESC
 LIMIT sqlc.arg(row_limit)::int;

-- name: ListJobsOldestFirst :many
SELECT j.id, j.kind, j.state::text AS state, j.attempt, j.max_attempts, j.created_by, j.target_kind, j.target_key,
       j.progress_done, j.progress_total, j.progress_unit, j.result, j.error, j.created_at, j.started_at, j.finished_at,
       j.ran_by, r.name AS replica_name
  FROM job j
  LEFT JOIN replica r ON r.instance_uid = j.ran_by
 WHERE (cardinality(sqlc.arg(kinds)::text[]) = 0 OR j.kind = ANY(sqlc.arg(kinds)::text[]))
   AND (cardinality(sqlc.arg(states)::text[]) = 0 OR j.state::text = ANY(sqlc.arg(states)::text[]))
   AND (sqlc.arg(created_by)::text = '' OR j.created_by = sqlc.arg(created_by)::text::uuid)
   AND (sqlc.arg(after_at)::text = '' OR (j.created_at, j.id) > (sqlc.arg(after_at)::text::timestamptz, sqlc.arg(after_id)::text::uuid))
 ORDER BY j.created_at ASC, j.id ASC
 LIMIT sqlc.arg(row_limit)::int;

-- name: GetJobView :many
SELECT j.id, j.kind, j.state::text AS state, j.attempt, j.max_attempts, j.created_by, j.target_kind, j.target_key,
       j.progress_done, j.progress_total, j.progress_unit, j.result, j.error, j.created_at, j.started_at, j.finished_at,
       j.ran_by, r.name AS replica_name
  FROM job j
  LEFT JOIN replica r ON r.instance_uid = j.ran_by
 WHERE j.id = sqlc.arg(id)::text::uuid;

-- name: LastJobPerKind :many
-- The most recent job of each of the given kinds, and whether one is running now
-- (the task list, R-OPS-019).
SELECT DISTINCT ON (j.kind)
       j.kind, j.id, j.state::text AS state, j.started_at, j.finished_at, j.ran_by, r.name AS replica_name
  FROM job j
  LEFT JOIN replica r ON r.instance_uid = j.ran_by
 WHERE j.kind = ANY(sqlc.arg(kinds)::text[])
 ORDER BY j.kind, j.created_at DESC, j.id DESC;

-- name: KindsRunningNow :many
SELECT DISTINCT kind FROM job
 WHERE kind = ANY(sqlc.arg(kinds)::text[]) AND state IN ('running', 'cancelling');

-- name: ListSecretKeyCanaries :many
-- The canary rows of the stored-secret key ring (R-ARCH-021, R-API-049).
SELECT key_id, ciphertext FROM secret_key_canary ORDER BY key_id;

-- name: InsertSecretKeyCanary :exec
-- Seeds the canary of a key ID at start-up (R-ARCH-021). The first replica that
-- holds the key writes it; later replicas open it.
INSERT INTO secret_key_canary (key_id, ciphertext)
VALUES (sqlc.arg(key_id)::text, sqlc.arg(ciphertext)::text)
ON CONFLICT (key_id) DO NOTHING;


-- name: CurrentLockTimeout :one
-- The lock_timeout of the session, to restore it after the short wait of an
-- idempotent request on the row of the first one (R-API-014).
SELECT current_setting('lock_timeout')::text AS lock_timeout;

-- name: IdempotencySavepoint :exec
-- The operation of an idempotent request runs behind this savepoint, so that a
-- refusal rolls back the change but keeps the claimed key row in the same
-- transaction (R-API-014).
SAVEPOINT idempotent_operation;

-- name: IdempotencyRollbackToSavepoint :exec
ROLLBACK TO SAVEPOINT idempotent_operation;

-- name: IdempotencyReleaseSavepoint :exec
RELEASE SAVEPOINT idempotent_operation;
