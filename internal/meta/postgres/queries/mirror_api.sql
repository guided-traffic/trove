-- Statements of the mirror's status and run operations (R-REPO-056, work package D, docs/developer/mirror.md). The engine's
-- statements are in mirror.sql. These two touch mirror_state as the API may: the status reads it, the run writes only
-- pass_requested_at, pass_requested_by and pass_requested_full (never an engine column, so a request cannot move a fence,
-- a cursor or a counter).

-- name: MirrorStatusRead :many
-- One row for the repository, whether or not the dispatch has created its mirror_state row yet: has_state says which. The
-- pending count, the usage counters and the owning job are joined here so that the answer is one statement of one
-- snapshot, whichever replica serves it.
SELECT (s.repo_id IS NOT NULL)::boolean AS has_state,
       s.next_pass_at, s.pass_started_at, s.pass_kind, s.pass_method, s.pass_job_id, s.pass_discovery_done,
       s.pass_names, s.pass_packages_done, s.pass_packages_failed, s.pass_items_stored, s.pass_bytes_stored,
       s.last_full_pass_at, s.last_pass,
       s.consecutive_failures, s.last_error_at, s.last_error_code, s.last_error,
       s.rate_limited_until, s.rate_limit_waits, s.rate_limit_wait_seconds, s.last_rate_limit_at, s.last_rate_limit_status,
       s.bound_reached, s.bound_reached_at,
       j.kind AS job_kind, COALESCE(j.state::text, '')::text AS job_state,
       (SELECT count(*) FROM mirror_pending p WHERE p.repo_id = sqlc.arg(repo_id)::bigint)::bigint AS pending,
       COALESCE(u.logical_bytes, 0)::bigint AS used_bytes
  FROM (SELECT sqlc.arg(repo_id)::bigint AS id) r
  LEFT JOIN mirror_state s ON s.repo_id = r.id
  LEFT JOIN job j ON j.id = s.pass_job_id
  LEFT JOIN repository_usage u ON u.repo_id = r.id;

-- name: MirrorRequestPass :exec
-- Records a request of the run operation: the time, the caller (empty for a caller without a principal row) and whether the
-- pass is to be full. A row the dispatch has not created yet is created with only these columns and the defaults. A request
-- that the engine has not taken yet (pass_requested_at still set) keeps a full flag already asked for, so a later plain
-- request does not take a full one back.
INSERT INTO mirror_state (repo_id, pass_requested_at, pass_requested_by, pass_requested_full)
VALUES (sqlc.arg(repo_id)::bigint, now(), NULLIF(sqlc.arg(requested_by)::text, '')::uuid, sqlc.arg(full_pass)::boolean)
ON CONFLICT (repo_id) DO UPDATE
   SET pass_requested_full = EXCLUDED.pass_requested_full
                             OR (mirror_state.pass_requested_at IS NOT NULL AND mirror_state.pass_requested_full),
       pass_requested_at = EXCLUDED.pass_requested_at,
       pass_requested_by = EXCLUDED.pass_requested_by;
