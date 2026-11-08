-- Statements of the mirror engine (internal/mirror, repositories-and-proxy.md R-REPO-053, R-REPO-056, R-REPO-058). One file per
-- work package; the status and run operations keep theirs in mirror_api.sql. Plain Go types only (no pgtype): COALESCE
-- nullable columns, times cross the boundary as microseconds since the epoch (0 for none), uuids as text.
--
-- Every write of a pass is conditional on the fence of mirror_state (pass_job_id, pass_holder: Q-ARCH-287; compared as uuids,
-- so that an instance id written without hyphens is the same holder). A statement that
-- matches no row says so by its first column (fenced = 0) and the engine stops the job without writing.

-- name: MirrorEnsureStates :exec
-- The dispatch creates the row of every remote repository in mode mirror; one that exists is left as it is.
INSERT INTO mirror_state (repo_id)
SELECT r.id FROM repositories r WHERE r.id = ANY(sqlc.arg(repo_ids)::bigint[])
ON CONFLICT (repo_id) DO NOTHING;

-- name: MirrorDue :many
-- The rows that are due: next_pass_at passed or never set, or a pass requested; and no rate-limit wait pending (R-REPO-053).
SELECT repo_id
  FROM mirror_state
 WHERE repo_id = ANY(sqlc.arg(repo_ids)::bigint[])
   AND (next_pass_at IS NULL OR next_pass_at <= to_timestamp(sqlc.arg(now_us)::bigint / 1000000.0) OR pass_requested_at IS NOT NULL)
   AND (rate_limited_until IS NULL OR rate_limited_until <= to_timestamp(sqlc.arg(now_us)::bigint / 1000000.0))
 ORDER BY repo_id;

-- name: MirrorClearInactivePasses :execrows
-- A repository that is in the snapshot and not in mode mirror any more keeps its last complete pass and loses the pass in
-- progress and its fence, so that a late write of a pass that still runs matches no row (R-REPO-053 stops, Q-REPO-148). The
-- caller names the repositories that left: an empty snapshot clears nothing.
UPDATE mirror_state
   SET pass_started_at = NULL, pass_kind = NULL, pass_method = NULL, pass_job_id = NULL, pass_holder = NULL, pass_cursor = NULL,
       pass_discovery_done = false, pass_names = 0, pass_packages_done = 0, pass_packages_failed = 0, pass_items_stored = 0,
       pass_bytes_stored = 0, pass_discovery_complete = true, pass_discovery_note = NULL, updated_at = now()
 WHERE repo_id = ANY(sqlc.arg(left_ids)::bigint[]) AND pass_started_at IS NOT NULL;

-- name: MirrorDeleteInactivePending :execrows
-- The pending names of a repository that is not in mode mirror any more: the first pass after it is turned on again
-- rediscovers them.
DELETE FROM mirror_pending WHERE repo_id = ANY(sqlc.arg(left_ids)::bigint[]);

-- name: MirrorDeleteAllPending :execrows
-- A restore: the pending names are transient (R-REPO-058).
DELETE FROM mirror_pending;

-- name: MirrorStateGet :one
SELECT repo_id,
       COALESCE(extract(epoch FROM next_pass_at) * 1000000, 0)::bigint AS next_pass_us,
       COALESCE(extract(epoch FROM pass_requested_at) * 1000000, 0)::bigint AS pass_requested_us,
       pass_requested_full,
       COALESCE(extract(epoch FROM pass_started_at) * 1000000, 0)::bigint AS pass_started_us,
       COALESCE(pass_kind, '')::text AS pass_kind,
       COALESCE(pass_method, '')::text AS pass_method,
       COALESCE(pass_job_id::text, '')::text AS pass_job_id,
       COALESCE(pass_holder::text, '')::text AS pass_holder,
       COALESCE(pass_cursor::text, '')::text AS pass_cursor,
       pass_discovery_done, pass_names, pass_packages_done, pass_packages_failed, pass_items_stored, pass_bytes_stored,
       pass_discovery_complete,
       COALESCE(pass_discovery_note, '')::text AS pass_discovery_note,
       COALESCE(feed_cursor::text, '')::text AS feed_cursor,
       COALESCE(feed_method, '')::text AS feed_method,
       COALESCE(patterns_fingerprint, ''::bytea)::bytea AS patterns_fingerprint,
       COALESCE(extract(epoch FROM last_full_pass_at) * 1000000, 0)::bigint AS last_full_pass_us,
       consecutive_failures,
       COALESCE(extract(epoch FROM rate_limited_until) * 1000000, 0)::bigint AS rate_limited_until_us,
       COALESCE(bound_reached, '')::text AS bound_reached,
       COALESCE(extract(epoch FROM bound_reached_at) * 1000000, 0)::bigint AS bound_reached_us
  FROM mirror_state
 WHERE repo_id = sqlc.arg(repo_id)::bigint;

-- name: MirrorClaim :one
-- R-REPO-053 step 1: one statement makes the job the owner of the repository's pass. It starts a new pass when none is in
-- progress (the kind is the caller's decision, 'full' where a request asked for it) and takes over the one in progress
-- otherwise; the pending rows of a new pass get their attempts back; a new pass consumes the request, a pass that is taken
-- over leaves it for the pass after it (a full request survives). The rate-limit wait is not touched: a wait that lies ahead
-- is the end's to clear. claimed is 0 for a repository without a row.
WITH old AS (
    SELECT repo_id, pass_started_at IS NULL AS fresh
      FROM mirror_state
     WHERE repo_id = sqlc.arg(repo_id)::bigint
       FOR UPDATE
), u AS (
    UPDATE mirror_state s
       SET pass_job_id = sqlc.arg(job_id)::text::uuid,
           pass_holder = sqlc.arg(holder)::text::uuid,
           pass_kind = CASE WHEN NOT old.fresh THEN s.pass_kind
                            WHEN s.pass_requested_full OR sqlc.arg(want_full)::boolean THEN 'full'
                            ELSE sqlc.arg(kind_if_new)::text END,
           pass_started_at = CASE WHEN old.fresh THEN to_timestamp(sqlc.arg(now_us)::bigint / 1000000.0) ELSE s.pass_started_at END,
           pass_method = CASE WHEN old.fresh THEN NULL ELSE s.pass_method END,
           pass_cursor = CASE WHEN old.fresh THEN NULL ELSE s.pass_cursor END,
           pass_discovery_done = CASE WHEN old.fresh THEN false ELSE s.pass_discovery_done END,
           pass_names = CASE WHEN old.fresh THEN 0 ELSE s.pass_names END,
           pass_packages_done = CASE WHEN old.fresh THEN 0 ELSE s.pass_packages_done END,
           pass_packages_failed = CASE WHEN old.fresh THEN 0 ELSE s.pass_packages_failed END,
           pass_items_stored = CASE WHEN old.fresh THEN 0 ELSE s.pass_items_stored END,
           pass_bytes_stored = CASE WHEN old.fresh THEN 0 ELSE s.pass_bytes_stored END,
           pass_discovery_complete = CASE WHEN old.fresh THEN true ELSE s.pass_discovery_complete END,
           pass_discovery_note = CASE WHEN old.fresh THEN NULL ELSE s.pass_discovery_note END,
           pass_requested_at = CASE WHEN old.fresh THEN NULL ELSE s.pass_requested_at END,
           pass_requested_by = CASE WHEN old.fresh THEN NULL ELSE s.pass_requested_by END,
           pass_requested_full = CASE WHEN old.fresh THEN false ELSE s.pass_requested_full END,
           updated_at = now()
      FROM old
     WHERE s.repo_id = old.repo_id
 RETURNING 1
), r AS (
    UPDATE mirror_pending p
       SET attempts = 0, not_before = NULL
      FROM old
     WHERE p.repo_id = old.repo_id AND old.fresh AND (p.attempts <> 0 OR p.not_before IS NOT NULL)
 RETURNING 1
)
SELECT (SELECT count(*) FROM u)::bigint AS claimed;

-- name: MirrorSetMethod :execrows
-- The method the pass discovers with, and its kind: a pass whose method is not the feed of the last complete pass is full.
UPDATE mirror_state
   SET pass_method = sqlc.arg(method)::text, pass_kind = sqlc.arg(kind)::text, updated_at = now()
 WHERE repo_id = sqlc.arg(repo_id)::bigint
   AND pass_job_id = sqlc.arg(job_id)::text::uuid AND pass_holder = sqlc.arg(holder)::text::uuid;

-- name: MirrorPendingCount :one
-- Every row of the repository, the ones left after their attempts included: the bound of 10,000 counts the table (R-REPO-053
-- step 2).
SELECT count(*)::bigint AS n
  FROM mirror_pending
 WHERE repo_id = sqlc.arg(repo_id)::bigint;

-- name: MirrorDropPending :one
-- Deletes pending rows by name, fenced: the names that the current patterns or claims refuse (R-REPO-051). The counters do not
-- move.
WITH f AS (
    SELECT repo_id FROM mirror_state
     WHERE repo_id = sqlc.arg(repo_id)::bigint
       AND pass_job_id = sqlc.arg(job_id)::text::uuid AND pass_holder = sqlc.arg(holder)::text::uuid
       FOR UPDATE
), d AS (
    DELETE FROM mirror_pending p USING f
     WHERE p.repo_id = f.repo_id AND p.name = ANY(sqlc.arg(names)::text[])
 RETURNING 1
)
SELECT (SELECT count(*) FROM f)::bigint AS fenced, (SELECT count(*) FROM d)::bigint AS dropped;

-- name: MirrorCursorLength :one
-- The length of a cursor as the column holds it: the text of the jsonb value, which PostgreSQL prints with a space after
-- every colon and comma, so it is longer than the compact JSON the format wrote (the CHECK of the columns, R-REPO-053).
SELECT octet_length(sqlc.arg(cursor_json)::text::jsonb::text)::int AS n;

-- name: MirrorPendingNext :many
-- The next pending names in name order after one name: not yet left, and not waiting for a retry (R-REPO-053 step 3).
SELECT name, attempts
  FROM mirror_pending
 WHERE repo_id = sqlc.arg(repo_id)::bigint
   AND attempts < sqlc.arg(max_attempts)::int
   AND (not_before IS NULL OR not_before <= to_timestamp(sqlc.arg(now_us)::bigint / 1000000.0))
   AND name > sqlc.arg(after)::text
 ORDER BY name
 LIMIT sqlc.arg(row_limit)::int;

-- name: MirrorPendingNextWake :one
-- The earliest retry time of a name that is waiting, 0 when none is.
SELECT COALESCE(extract(epoch FROM min(not_before)) * 1000000, 0)::bigint AS wake_us
  FROM mirror_pending
 WHERE repo_id = sqlc.arg(repo_id)::bigint
   AND attempts < sqlc.arg(max_attempts)::int
   AND not_before > to_timestamp(sqlc.arg(now_us)::bigint / 1000000.0);

-- name: MirrorHeldNames :many
-- The names of the packages the repository holds (R-REPO-051: a full pass keeps them current), in name order.
SELECT name
  FROM packages
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND name > sqlc.arg(after)::text
 ORDER BY name
 LIMIT sqlc.arg(row_limit)::int;

-- name: MirrorAddNames :one
-- R-REPO-053 step 2: in one statement, the names are inserted (a name already pending is kept once) and, when progress is
-- set, the format's cursor and the discovery flags of the pass are stored. added is the number of new rows, fenced 0 when the
-- fence is not ours.
WITH f AS (
    SELECT repo_id FROM mirror_state
     WHERE repo_id = sqlc.arg(repo_id)::bigint
       AND pass_job_id = sqlc.arg(job_id)::text::uuid AND pass_holder = sqlc.arg(holder)::text::uuid
       FOR UPDATE
), i AS (
    INSERT INTO mirror_pending (repo_id, name)
    SELECT f.repo_id, n FROM f, unnest(sqlc.arg(names)::text[]) AS n
    ON CONFLICT (repo_id, name) DO NOTHING
    RETURNING 1
), u AS (
    UPDATE mirror_state s
       SET pass_names = s.pass_names + (SELECT count(*) FROM i),
           pass_cursor = CASE WHEN sqlc.arg(set_progress)::boolean THEN NULLIF(sqlc.arg(cursor_json)::text, '')::jsonb ELSE s.pass_cursor END,
           pass_discovery_done = CASE WHEN sqlc.arg(set_progress)::boolean THEN sqlc.arg(done)::boolean ELSE s.pass_discovery_done END,
           pass_discovery_complete = CASE WHEN sqlc.arg(set_progress)::boolean AND sqlc.arg(truncated)::boolean THEN false ELSE s.pass_discovery_complete END,
           pass_discovery_note = CASE WHEN sqlc.arg(set_progress)::boolean AND sqlc.arg(truncated)::boolean THEN NULLIF(sqlc.arg(note)::text, '')
                                      ELSE s.pass_discovery_note END,
           updated_at = now()
      FROM f
     WHERE s.repo_id = f.repo_id
 RETURNING 1
)
SELECT (SELECT count(*) FROM u)::bigint AS fenced, (SELECT count(*) FROM i)::bigint AS added;

-- name: MirrorPackageDone :one
-- R-REPO-053 step 3: the pending row is deleted and the counters grow, in one statement.
WITH f AS (
    SELECT repo_id FROM mirror_state
     WHERE repo_id = sqlc.arg(repo_id)::bigint
       AND pass_job_id = sqlc.arg(job_id)::text::uuid AND pass_holder = sqlc.arg(holder)::text::uuid
       FOR UPDATE
), d AS (
    DELETE FROM mirror_pending p USING f WHERE p.repo_id = f.repo_id AND p.name = sqlc.arg(name)::text RETURNING 1
), u AS (
    UPDATE mirror_state s
       SET pass_packages_done = s.pass_packages_done + 1,
           pass_items_stored = s.pass_items_stored + sqlc.arg(items)::bigint,
           pass_bytes_stored = s.pass_bytes_stored + sqlc.arg(bytes)::bigint,
           updated_at = now()
      FROM f
     WHERE s.repo_id = f.repo_id
 RETURNING 1
)
SELECT (SELECT count(*) FROM u)::bigint AS fenced;

-- name: MirrorPackageFailed :one
-- A package that failed keeps its row with one more attempt and a retry one step of the back-off later per attempt; the
-- attempt that reaches the limit leaves it to the next pass and counts it as failed (R-REPO-053 step 3). A permanent failure
-- (a tampered artifact) takes the package to the limit at once. The counters grow by what the attempt stored before it failed.
WITH f AS (
    SELECT repo_id FROM mirror_state
     WHERE repo_id = sqlc.arg(repo_id)::bigint
       AND pass_job_id = sqlc.arg(job_id)::text::uuid AND pass_holder = sqlc.arg(holder)::text::uuid
       FOR UPDATE
), p AS (
    UPDATE mirror_pending m
       SET attempts = CASE WHEN sqlc.arg(permanent)::boolean THEN GREATEST(m.attempts + 1, sqlc.arg(max_attempts)::int) ELSE m.attempts + 1 END,
           not_before = to_timestamp(sqlc.arg(now_us)::bigint / 1000000.0) + (m.attempts + 1) * make_interval(secs => sqlc.arg(retry_step_secs)::int),
           last_error = left(sqlc.arg(last_error)::text, 1024)
      FROM f
     WHERE m.repo_id = f.repo_id AND m.name = sqlc.arg(name)::text
 RETURNING m.attempts
), u AS (
    UPDATE mirror_state s
       SET pass_packages_failed = s.pass_packages_failed + CASE WHEN COALESCE((SELECT max(attempts) FROM p), 0) >= sqlc.arg(max_attempts)::int THEN 1 ELSE 0 END,
           pass_items_stored = s.pass_items_stored + sqlc.arg(items)::bigint,
           pass_bytes_stored = s.pass_bytes_stored + sqlc.arg(bytes)::bigint,
           updated_at = now()
      FROM f
     WHERE s.repo_id = f.repo_id
 RETURNING 1
)
SELECT (SELECT count(*) FROM u)::bigint AS fenced, COALESCE((SELECT max(attempts) FROM p), 0)::int AS attempts;

-- name: MirrorPackageKept :execrows
-- A package that stays pending without a failure (pre-empted, stopped by a wait or a bound): only the counters grow.
UPDATE mirror_state
   SET pass_items_stored = pass_items_stored + sqlc.arg(items)::bigint,
       pass_bytes_stored = pass_bytes_stored + sqlc.arg(bytes)::bigint,
       updated_at = now()
 WHERE repo_id = sqlc.arg(repo_id)::bigint
   AND pass_job_id = sqlc.arg(job_id)::text::uuid AND pass_holder = sqlc.arg(holder)::text::uuid;

-- name: MirrorRecordRateLimit :execrows
-- One wait of the pass (R-REPO-054, R-REPO-056); its end is kept whatever its length, so that a job that is released in the
-- middle of it does not resume at once in another process.
UPDATE mirror_state
   SET rate_limited_until = to_timestamp(sqlc.arg(until_us)::bigint / 1000000.0),
       rate_limit_waits = rate_limit_waits + 1,
       rate_limit_wait_seconds = rate_limit_wait_seconds + sqlc.arg(wait_seconds)::bigint,
       last_rate_limit_at = to_timestamp(sqlc.arg(now_us)::bigint / 1000000.0),
       last_rate_limit_status = sqlc.arg(status)::int,
       updated_at = now()
 WHERE repo_id = sqlc.arg(repo_id)::bigint
   AND pass_job_id = sqlc.arg(job_id)::text::uuid AND pass_holder = sqlc.arg(holder)::text::uuid;

-- name: MirrorDefer :execrows
-- A wait longer than 300 s: the pass stays, the next job is due when the wait is over (R-REPO-054).
UPDATE mirror_state
   SET rate_limited_until = to_timestamp(sqlc.arg(until_us)::bigint / 1000000.0),
       next_pass_at = to_timestamp(sqlc.arg(until_us)::bigint / 1000000.0),
       updated_at = now()
 WHERE repo_id = sqlc.arg(repo_id)::bigint
   AND pass_job_id = sqlc.arg(job_id)::text::uuid AND pass_holder = sqlc.arg(holder)::text::uuid;

-- name: MirrorEndPass :one
-- R-REPO-053 step 4 and the stops: the summary, the cursor for the next incremental pass (advance), the pass cleared unless it
-- is kept (a pause, offline), the pending names deleted when the mode changed, the bound, the failure count reset. fenced is 0
-- when the fence is not ours; bound_new says that the bound was not set before (the transition of R-REPO-052).
WITH f AS (
    SELECT repo_id, bound_reached IS NULL AS bound_was_null
      FROM mirror_state
     WHERE repo_id = sqlc.arg(repo_id)::bigint
       AND pass_job_id = sqlc.arg(job_id)::text::uuid AND pass_holder = sqlc.arg(holder)::text::uuid
       FOR UPDATE
), d AS (
    DELETE FROM mirror_pending p USING f WHERE p.repo_id = f.repo_id AND sqlc.arg(clear_pending)::boolean RETURNING 1
), u AS (
    UPDATE mirror_state s
       SET last_pass = sqlc.arg(last_pass)::text::jsonb,
           feed_cursor = CASE WHEN sqlc.arg(advance)::boolean THEN s.pass_cursor ELSE s.feed_cursor END,
           feed_method = CASE WHEN sqlc.arg(advance)::boolean THEN s.pass_method ELSE s.feed_method END,
           last_full_pass_at = CASE WHEN sqlc.arg(advance)::boolean AND s.pass_kind = 'full'
                                    THEN to_timestamp(sqlc.arg(now_us)::bigint / 1000000.0) ELSE s.last_full_pass_at END,
           last_complete_pass_at = CASE WHEN sqlc.arg(completed)::boolean
                                        THEN to_timestamp(sqlc.arg(now_us)::bigint / 1000000.0) ELSE s.last_complete_pass_at END,
           patterns_fingerprint = CASE WHEN sqlc.arg(advance)::boolean THEN sqlc.arg(fingerprint)::bytea ELSE s.patterns_fingerprint END,
           pass_started_at = CASE WHEN sqlc.arg(keep_pass)::boolean THEN s.pass_started_at ELSE NULL END,
           pass_kind = CASE WHEN sqlc.arg(keep_pass)::boolean THEN s.pass_kind ELSE NULL END,
           pass_method = CASE WHEN sqlc.arg(keep_pass)::boolean THEN s.pass_method ELSE NULL END,
           pass_job_id = CASE WHEN sqlc.arg(keep_pass)::boolean THEN s.pass_job_id ELSE NULL END,
           pass_holder = CASE WHEN sqlc.arg(keep_pass)::boolean THEN s.pass_holder ELSE NULL END,
           pass_cursor = CASE WHEN sqlc.arg(keep_pass)::boolean THEN s.pass_cursor ELSE NULL END,
           pass_discovery_done = CASE WHEN sqlc.arg(keep_pass)::boolean THEN s.pass_discovery_done ELSE false END,
           pass_names = CASE WHEN sqlc.arg(keep_pass)::boolean THEN s.pass_names ELSE 0 END,
           pass_packages_done = CASE WHEN sqlc.arg(keep_pass)::boolean THEN s.pass_packages_done ELSE 0 END,
           pass_packages_failed = CASE WHEN sqlc.arg(keep_pass)::boolean THEN s.pass_packages_failed ELSE 0 END,
           pass_items_stored = CASE WHEN sqlc.arg(keep_pass)::boolean THEN s.pass_items_stored ELSE 0 END,
           pass_bytes_stored = CASE WHEN sqlc.arg(keep_pass)::boolean THEN s.pass_bytes_stored ELSE 0 END,
           pass_discovery_complete = CASE WHEN sqlc.arg(keep_pass)::boolean THEN s.pass_discovery_complete ELSE true END,
           pass_discovery_note = CASE WHEN sqlc.arg(keep_pass)::boolean THEN s.pass_discovery_note ELSE NULL END,
           consecutive_failures = CASE WHEN sqlc.arg(reset_failures)::boolean THEN 0 ELSE s.consecutive_failures END,
           rate_limited_until = NULL,
           next_pass_at = to_timestamp(sqlc.arg(next_pass_us)::bigint / 1000000.0),
           bound_reached = CASE WHEN sqlc.arg(bound)::text <> '' THEN sqlc.arg(bound)::text
                                WHEN sqlc.arg(clear_bound)::boolean THEN NULL ELSE s.bound_reached END,
           bound_reached_at = CASE WHEN sqlc.arg(bound)::text <> '' THEN COALESCE(s.bound_reached_at, to_timestamp(sqlc.arg(now_us)::bigint / 1000000.0))
                                   WHEN sqlc.arg(clear_bound)::boolean THEN NULL ELSE s.bound_reached_at END,
           updated_at = now()
      FROM f
     WHERE s.repo_id = f.repo_id
 RETURNING 1
)
SELECT (SELECT count(*) FROM u)::bigint AS fenced, COALESCE((SELECT bool_and(bound_was_null) FROM f), false)::boolean AS bound_new;

-- name: MirrorFailureLock :one
-- The consecutive failures of the row, locked, when the fence is ours (R-REPO-053 errors).
SELECT consecutive_failures
  FROM mirror_state
 WHERE repo_id = sqlc.arg(repo_id)::bigint
   AND pass_job_id = sqlc.arg(job_id)::text::uuid AND pass_holder = sqlc.arg(holder)::text::uuid
   FOR UPDATE;

-- name: MirrorFailureWrite :execrows
-- A failure of the pass itself: recorded, counted, and the next pass due after the back-off. The pass in progress stays.
UPDATE mirror_state
   SET consecutive_failures = sqlc.arg(failures)::int,
       last_error_at = to_timestamp(sqlc.arg(now_us)::bigint / 1000000.0),
       last_error_code = left(sqlc.arg(code)::text, 64),
       last_error = left(sqlc.arg(message)::text, 1024),
       next_pass_at = to_timestamp(sqlc.arg(next_pass_us)::bigint / 1000000.0),
       updated_at = now()
 WHERE repo_id = sqlc.arg(repo_id)::bigint
   AND pass_job_id = sqlc.arg(job_id)::text::uuid AND pass_holder = sqlc.arg(holder)::text::uuid;

-- name: MirrorUsage :one
-- The folded counter of the repository (R-STOR-003) with the time of its last fold, and the database's clock, so that the
-- pass can tell which of its own stores the fold has not seen (R-REPO-052). A repository without a counter row has 0.
SELECT COALESCE(u.logical_bytes, 0)::bigint AS logical_bytes,
       COALESCE(extract(epoch FROM u.updated_at) * 1000000, 0)::bigint AS folded_us,
       (extract(epoch FROM clock_timestamp()) * 1000000)::bigint AS read_us
  FROM (SELECT 1) one
  LEFT JOIN repository_usage u ON u.repo_id = sqlc.arg(repo_id)::bigint;

-- name: MirrorGauges :many
-- The gauges that every process exports (R-REPO-057): the pending names of the pass in progress (the names a pass still takes,
-- attempts below the limit), the end of the last pass that ended complete, partial or at its bound (0 when none did), the
-- failures in a row, and when the mirror next acts: the time a due pass is due, or for a pass in progress the later of that and
-- its last write (a pass that is worked on moves it forward, one that nobody runs does not).
SELECT (rp.key || '/' || r.key)::text AS key,
       CASE WHEN s.pass_started_at IS NULL THEN 0
            ELSE (SELECT count(*) FROM mirror_pending p WHERE p.repo_id = s.repo_id AND p.attempts < sqlc.arg(max_attempts)::int)
       END::bigint AS pending,
       COALESCE(extract(epoch FROM s.last_complete_pass_at), 0)::float8 AS last_complete_s,
       s.consecutive_failures::int AS consecutive_failures,
       extract(epoch FROM CASE WHEN s.pass_started_at IS NULL THEN COALESCE(s.next_pass_at, s.updated_at)
                               ELSE greatest(COALESCE(s.next_pass_at, s.updated_at), s.updated_at) END)::float8 AS next_pass_s
  FROM mirror_state s
  JOIN repositories r ON r.id = s.repo_id
  JOIN project rp ON rp.id = r.project_id
 ORDER BY rp.key, r.key;

-- name: MirrorRestoreClear :execrows
-- R-REPO-058: what a restore does to the state. The pass in progress, the request, the rate-limit wait and the bound are
-- cleared and the next pass is due at the restore time; a restore without remote caches also forgets the cursor, the time of
-- the last full pass and the fingerprint, so that its first pass is full.
UPDATE mirror_state
   SET pass_started_at = NULL, pass_kind = NULL, pass_method = NULL, pass_job_id = NULL, pass_holder = NULL, pass_cursor = NULL,
       pass_discovery_done = false, pass_names = 0, pass_packages_done = 0, pass_packages_failed = 0, pass_items_stored = 0,
       pass_bytes_stored = 0, pass_discovery_complete = true, pass_discovery_note = NULL,
       pass_requested_at = NULL, pass_requested_by = NULL, pass_requested_full = false,
       rate_limited_until = NULL, bound_reached = NULL, bound_reached_at = NULL,
       next_pass_at = to_timestamp(sqlc.arg(now_us)::bigint / 1000000.0),
       feed_cursor = CASE WHEN sqlc.arg(without_remote_caches)::boolean THEN NULL ELSE feed_cursor END,
       feed_method = CASE WHEN sqlc.arg(without_remote_caches)::boolean THEN NULL ELSE feed_method END,
       last_full_pass_at = CASE WHEN sqlc.arg(without_remote_caches)::boolean THEN NULL ELSE last_full_pass_at END,
       patterns_fingerprint = CASE WHEN sqlc.arg(without_remote_caches)::boolean THEN NULL ELSE patterns_fingerprint END,
       updated_at = now();
