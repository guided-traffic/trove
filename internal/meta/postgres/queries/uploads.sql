-- The statements of the multi-request upload sessions (internal/uploads, the collection run's session step; R-STOR-011).
-- The packages that run them are domain and infrastructure packages without driver types, so an id and a writer token travel as
-- text and are cast to uuid here, a hash state and a digest as bytea, and every time is the database's own clock: the lease of a
-- claim and the age of a session are judged by one clock on every replica, never by the replica's.

-- name: UploadInsertSession :execrows
-- A session starts open, with the hash state of nothing and no multipart upload (it is created with the first part). It starts only
-- while the principal holds fewer than max_open sessions on the repository: zero rows is a start the cap refuses (Q-STOR-176). The
-- count and the insert are one statement, and the caller holds the lock of the repository row (UploadLockRepository), so that two starts at
-- once cannot both count below the cap: the second one waits for the lock and then sees the first insert (READ COMMITTED takes a new
-- snapshot per statement). A principal that is none counts as the all-zero uuid, which the expression index on the count shares.
INSERT INTO upload_sessions (id, repo_id, addressed_repo_id, binding, principal_id, hash_state, part_size, s3_upload_id, state)
SELECT sqlc.arg(id)::text::uuid, sqlc.arg(repo_id)::bigint, sqlc.arg(addressed_repo_id)::bigint, sqlc.arg(binding)::text,
        NULLIF(sqlc.arg(principal_id)::text, '')::uuid, sqlc.arg(hash_state)::bytea, sqlc.arg(part_size)::bigint,
        sqlc.arg(s3_upload_id)::text, 'open'
WHERE (SELECT count(*) FROM upload_sessions o
        WHERE o.repo_id = sqlc.arg(repo_id)::bigint AND COALESCE(o.principal_id, '00000000-0000-0000-0000-000000000000'::uuid) = COALESCE(NULLIF(sqlc.arg(principal_id)::text, '')::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
          AND o.state IN ('open', 'completing', 'assembled')) < sqlc.arg(max_open)::bigint;

-- name: UploadLockRepository :exec
-- The lock that serializes the starts of one repository (Q-STOR-176): FOR NO KEY UPDATE conflicts with itself and not with the key share
-- the foreign keys of the insert take.
SELECT 1 FROM repositories WHERE id = sqlc.arg(repo_id)::bigint FOR NO KEY UPDATE;

-- name: UploadLockAddressed :exec
-- The key share on the repository the client addressed, taken before the lock on the target (Q-STOR-176). The configuration update of a
-- virtual repository locks the virtual row, then its members; a start through it holds the target and needs a key share on the virtual row
-- for the foreign key of its insert. Asking for the share first gives both the same order and no cycle.
SELECT 1 FROM repositories WHERE id = sqlc.arg(repo_id)::bigint FOR KEY SHARE;

-- name: UploadGetSession :one
-- The row of a session as a request judges it: the scope columns, the recorded state and whether another request holds an
-- unexpired claim or the session is past its maximum age.
SELECT id::text AS id, repo_id, addressed_repo_id, COALESCE(binding, '')::text AS binding,
       COALESCE(principal_id::text, '')::text AS principal_id, received, hash_state, part_size, s3_upload_id, tail_size,
       COALESCE(writer::text, '')::text AS writer, COALESCE(item_id, 0)::bigint AS item_id,
       (writer IS NOT NULL AND writer_until > now())::bool AS claimed, digest, state,
       (created_at < now() - make_interval(secs => sqlc.arg(max_age_seconds)::float8))::bool AS too_old,
       (SELECT count(*) FROM upload_session_parts p WHERE p.session_id = s.id)::bigint AS parts
FROM upload_sessions s
WHERE id = sqlc.arg(id)::text::uuid;

-- name: UploadClaim :execrows
-- The claim of R-STOR-011 step 2: one writer at a time, only on an open session that is not past its maximum age, and only when
-- the request's first byte is the received count (a negative offset means "where the session is"). Zero rows is a request the
-- session does not take; the caller reads the row to say which.
UPDATE upload_sessions
   SET writer = sqlc.arg(writer)::text::uuid, writer_until = now() + make_interval(secs => sqlc.arg(lease_seconds)::float8),
       last_activity_at = now()
 WHERE id = sqlc.arg(id)::text::uuid AND state = 'open'
   AND (sqlc.arg(claim_offset)::bigint < 0 OR received = sqlc.arg(claim_offset)::bigint)
   AND (writer IS NULL OR writer_until <= now())
   AND created_at >= now() - make_interval(secs => sqlc.arg(max_age_seconds)::float8);

-- name: UploadRecordPart :execrows
-- A completed part: the new received count and hash state, no tail (its segments are inside the part; the caller deletes their
-- rows in the same transaction), a renewed lease, and the id of the multipart upload when this request created it (the upload is
-- created with the first part, and only the first record sets the id). Conditional on the request's writer token; zero rows is a
-- request that lost its claim.
UPDATE upload_sessions
   SET received = sqlc.arg(received)::bigint, hash_state = sqlc.arg(hash_state)::bytea, tail_size = 0,
       s3_upload_id = CASE WHEN s3_upload_id = '' THEN sqlc.arg(upload_id)::text ELSE s3_upload_id END,
       writer_until = now() + make_interval(secs => sqlc.arg(lease_seconds)::float8), last_activity_at = now()
 WHERE id = sqlc.arg(id)::text::uuid AND state = 'open' AND writer = sqlc.arg(writer)::text::uuid;

-- name: UploadRenew :execrows
-- The renewal of a claim right before a part is sent: the part upload gets a deadline below the renewed lease, so it cannot outlive
-- the claim it was started under. Zero rows is a request that lost its claim.
UPDATE upload_sessions SET writer_until = now() + make_interval(secs => sqlc.arg(lease_seconds)::float8), last_activity_at = now()
 WHERE id = sqlc.arg(id)::text::uuid AND state = 'open' AND writer = sqlc.arg(writer)::text::uuid
   AND created_at >= now() - make_interval(secs => sqlc.arg(max_age_seconds)::float8);

-- name: UploadClaimCompleting :execrows
-- The short claim of a finalize for the step that sends the tail as the last part: one continuation does it, another waits for the
-- lease or the release. Only a completing session, and only when no other claim is unexpired.
UPDATE upload_sessions
   SET writer = sqlc.arg(writer)::text::uuid, writer_until = now() + make_interval(secs => sqlc.arg(lease_seconds)::float8),
       last_activity_at = now()
 WHERE id = sqlc.arg(id)::text::uuid AND state = 'completing' AND (writer IS NULL OR writer_until <= now());

-- name: UploadListTails :many
SELECT seq, writer::text AS writer, size, sha256 FROM upload_session_tails
 WHERE session_id = sqlc.arg(id)::text::uuid ORDER BY seq;

-- name: UploadInsertTail :exec
INSERT INTO upload_session_tails (session_id, seq, writer, size, sha256)
VALUES (sqlc.arg(session_id)::text::uuid, sqlc.arg(seq)::int, sqlc.arg(writer)::text::uuid, sqlc.arg(size)::bigint, sqlc.arg(sha256)::bytea);

-- name: UploadDeleteTails :exec
DELETE FROM upload_session_tails WHERE session_id = sqlc.arg(id)::text::uuid;

-- name: UploadInsertPart :execrows
-- The row of an uploaded part. A part number that is recorded already is the same part sent again (a finalize repeated on
-- another replica), never another one.
INSERT INTO upload_session_parts (session_id, part_number, size, etag, sha256)
VALUES (sqlc.arg(session_id)::text::uuid, sqlc.arg(part_number)::int, sqlc.arg(size)::bigint, sqlc.arg(etag)::text, sqlc.arg(sha256)::bytea)
ON CONFLICT (session_id, part_number) DO NOTHING;

-- name: UploadRecordEnd :execrows
-- The end of a request's body: the received count, the hash state and the size of the tail after it (its segment rows are the
-- caller's, written in the same transaction), and the release of the claim, conditional on the writer token. A finalize that passed
-- the digest check sets the state completing and records the digest in the same statement.
UPDATE upload_sessions
   SET received = sqlc.arg(received)::bigint, hash_state = sqlc.arg(hash_state)::bytea, tail_size = sqlc.arg(tail_size)::bigint,
       writer = NULL, writer_until = NULL, last_activity_at = now(),
       state = CASE WHEN sqlc.arg(completing)::bool THEN 'completing' ELSE state END,
       digest = CASE WHEN sqlc.arg(completing)::bool THEN sqlc.narg(digest)::bytea ELSE digest END
 WHERE id = sqlc.arg(id)::text::uuid AND state = 'open' AND writer = sqlc.arg(writer)::text::uuid;

-- name: UploadRelease :exec
-- A request that ends without recording anything gives its claim back at once, so that the next request does not wait for the
-- lease (a drain, a failed body, a digest that did not match).
UPDATE upload_sessions SET writer = NULL, writer_until = NULL
 WHERE id = sqlc.arg(id)::text::uuid AND writer = sqlc.arg(writer)::text::uuid;

-- name: UploadListParts :many
SELECT part_number, size, etag, sha256 FROM upload_session_parts
 WHERE session_id = sqlc.arg(id)::text::uuid ORDER BY part_number;

-- name: UploadClearTail :execrows
-- The recorded tail has become the last part: it is not a tail any more, and the short claim of that step ends with it.
-- Conditional on the claim, so that a finalize that is repeated, or runs twice at once, clears it once.
UPDATE upload_sessions SET tail_size = 0, writer = NULL, writer_until = NULL, last_activity_at = now()
 WHERE id = sqlc.arg(id)::text::uuid AND state = 'completing' AND writer = sqlc.arg(writer)::text::uuid;

-- name: UploadSetAssembled :execrows
-- The staging object exists in full: the next finalize step is the commit.
UPDATE upload_sessions SET state = 'assembled', last_activity_at = now()
 WHERE id = sqlc.arg(id)::text::uuid AND state = 'completing';

-- name: UploadMarkDone :execrows
-- Runs in the transaction of the item commit: the item and the end of the session are one commit. Zero rows is a session that was
-- aborted meanwhile, and the commit rolls back. The item is recorded with it, so that a repeated closing request is answered from
-- the record and not by a second commit.
UPDATE upload_sessions SET state = 'done', item_id = sqlc.arg(item_id)::bigint, writer = NULL, writer_until = NULL, last_activity_at = now()
 WHERE id = sqlc.arg(id)::text::uuid AND state IN ('assembled', 'done');

-- name: UploadAbort :many
-- Ends a live session and returns the id of its multipart upload as it is at this statement: a part is recorded only while the session is
-- open, so no part can be recorded after it, and the caller aborts exactly this upload.
UPDATE upload_sessions SET state = 'aborted', writer = NULL, writer_until = NULL, last_activity_at = now()
 WHERE id = sqlc.arg(id)::text::uuid AND state IN ('open', 'completing', 'assembled') RETURNING s3_upload_id;

-- name: UploadExpire :many
-- The expiry of the collection run (R-STOR-011 Expiry): a live session without a recorded request for the idle time, and an open
-- one older than the maximum age, are aborted. Rows another run holds are left to it.
UPDATE upload_sessions s SET state = 'aborted', writer = NULL, writer_until = NULL, last_activity_at = now()
 WHERE s.id IN (
       SELECT u.id FROM upload_sessions u
        WHERE u.state IN ('open', 'completing', 'assembled')
          AND (u.last_activity_at < now() - make_interval(secs => sqlc.arg(ttl_seconds)::float8)
               OR (u.state = 'open' AND u.created_at < now() - make_interval(secs => sqlc.arg(max_age_seconds)::float8)))
        ORDER BY u.last_activity_at
        LIMIT sqlc.arg(max_rows)::int
        FOR UPDATE SKIP LOCKED)
RETURNING s.id::text AS id;

-- name: UploadCountExpired :one
-- The sessions UploadExpire would abort, for a dry run.
SELECT count(*)::bigint FROM upload_sessions
 WHERE state IN ('open', 'completing', 'assembled')
   AND (last_activity_at < now() - make_interval(secs => sqlc.arg(ttl_seconds)::float8)
        OR (state = 'open' AND created_at < now() - make_interval(secs => sqlc.arg(max_age_seconds)::float8)));

-- name: UploadListAborted :many
-- The aborted sessions whose objects may still be in the bucket, oldest first (the index on state and last_activity_at orders them). A
-- session that staged nothing (received 0) or whose objects the request that ended it removed (cleaned) is the bulk delete's.
SELECT id::text AS id, s3_upload_id, state FROM upload_sessions
 WHERE state = 'aborted' AND NOT cleaned AND received > 0 ORDER BY last_activity_at LIMIT sqlc.arg(max_rows)::int;

-- name: UploadListDone :many
-- The finished sessions whose replay window has passed and whose objects the request did not remove.
SELECT id::text AS id, s3_upload_id, state FROM upload_sessions
 WHERE state = 'done' AND NOT cleaned AND received > 0 AND last_activity_at < now() - make_interval(secs => sqlc.arg(window_seconds)::float8)
 ORDER BY last_activity_at LIMIT sqlc.arg(max_rows)::int;

-- name: UploadDeleteCleanedBulk :execrows
-- The rows that need no call to the store: aborted sessions that staged nothing or were cleaned by their request, and finished ones past the
-- replay window that are cleaned or staged nothing.
DELETE FROM upload_sessions WHERE id IN (
    SELECT s.id FROM upload_sessions s
     WHERE (s.cleaned OR s.received = 0)
       AND (s.state = 'aborted' OR (s.state = 'done' AND s.last_activity_at < now() - make_interval(secs => sqlc.arg(window_seconds)::float8)))
     ORDER BY s.last_activity_at LIMIT sqlc.arg(max_rows)::int);

-- name: UploadMarkCleaned :exec
-- The request that finished the session removed its objects: the collection run deletes the row without a call to the store.
UPDATE upload_sessions SET cleaned = true WHERE id = sqlc.arg(id)::text::uuid;

-- name: UploadBumpActivity :exec
-- A session whose objects could not be removed goes to the back of the line, so that a few bad rows do not stop the cleaning.
UPDATE upload_sessions SET last_activity_at = now() WHERE id = sqlc.arg(id)::text::uuid;

-- name: UploadDeleteCleaned :execrows
-- The row of a session whose objects are gone; its part rows go with it.
DELETE FROM upload_sessions WHERE id = sqlc.arg(id)::text::uuid AND state IN ('done', 'aborted');

-- name: UploadKnownSessions :many
-- Which of the ids have a row that still stands for live objects: the sweep removes the staging objects of the others.
SELECT id::text AS id, s3_upload_id, state FROM upload_sessions WHERE id = ANY(sqlc.arg(ids)::text[]::uuid[]);

-- name: UploadRenewCompleting :execrows
-- The renewal of the short claim of a finalize's tail step, before the segments are read and before the last part is sent. Zero rows is
-- a continuation whose claim ran out and was taken (or released) meanwhile.
UPDATE upload_sessions SET writer_until = now() + make_interval(secs => sqlc.arg(lease_seconds)::float8), last_activity_at = now()
 WHERE id = sqlc.arg(id)::text::uuid AND state = 'completing' AND writer = sqlc.arg(writer)::text::uuid;

-- name: UploadSweepCursorGet :one
-- Where the sweep of the staging area stopped in an earlier run (a JSON text; empty when it finished or never ran), so that successive
-- runs go on instead of starting at the first session again.
SELECT COALESCE((SELECT value #>> '{}' FROM system_state WHERE key = 'upload_sweep_cursor'), '')::text;

-- name: UploadSweepCursorSet :exec
INSERT INTO system_state (key, value) VALUES ('upload_sweep_cursor', to_jsonb(sqlc.arg(cursor)::text))
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();
