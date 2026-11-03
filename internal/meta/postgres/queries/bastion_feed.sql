-- Queries of the Bastion revocation feed (R-BAS-015, Q-BAS-29, Q-BAS-49; work package 2). The feed reads the
-- journaled audit events (audit_event, keyed by (occurred_at, id)) and maps them to entries in Go
-- (internal/bastionmain.FeedMapper): the actions that carry an entry are passed as a text array, so the
-- registry of the audit package stays the one list of journaled actions. The cursor time travels as text
-- (RFC 3339 with fractions) like the audit queries', the cursor ID as a UUID text.
--
-- One poll is two reads, so that a page can never stall on the lookback: the entries behind the cursor
-- (late commits, at most the lookback window, re-sent for the Bastion to skip by ID) and the entries after
-- it, which max_rows cuts (the caller asks for the page size plus one to learn the value of more).

-- name: BastionFeedBehind :many
-- Entries within the lookback up to and including the cursor position: occurred_at > cursor_at - lookback and
-- (occurred_at, id) <= (cursor_at, cursor_id). A transaction that committed after the cursor moved past its
-- start time shows up here. max_rows is a safety bound (a multiple of the page size), not a page.
SELECT id::text AS id, occurred_at, action, COALESCE(actor_id::text, '')::text AS actor_id,
       COALESCE(resource_type, '')::text AS resource_type, COALESCE(resource_id, '')::text AS resource_id,
       COALESCE(repository, '')::text AS repository, details
  FROM audit_event
 WHERE occurred_at > sqlc.arg(cursor_at)::text::timestamptz - make_interval(secs => sqlc.arg(lookback_secs)::float8)
   AND (occurred_at, id) <= (sqlc.arg(cursor_at)::text::timestamptz, sqlc.arg(cursor_id)::text::uuid)
   AND action = ANY(sqlc.arg(actions)::text[])
 ORDER BY occurred_at, id
 LIMIT sqlc.arg(max_rows)::int;

-- name: BastionFeedAhead :many
-- Entries after the cursor position, in order. For a synthetic cursor (no ID) the caller passes the nil UUID
-- as cursor_id, which sorts before every event of that instant.
SELECT id::text AS id, occurred_at, action, COALESCE(actor_id::text, '')::text AS actor_id,
       COALESCE(resource_type, '')::text AS resource_type, COALESCE(resource_id, '')::text AS resource_id,
       COALESCE(repository, '')::text AS repository, details
  FROM audit_event
 WHERE (occurred_at, id) > (sqlc.arg(cursor_at)::text::timestamptz, sqlc.arg(cursor_id)::text::uuid)
   AND action = ANY(sqlc.arg(actions)::text[])
 ORDER BY occurred_at, id
 LIMIT sqlc.arg(max_rows)::int;

-- name: BastionFeedCursorState :one
-- What decides a reset (R-BAS-015) in one read: whether the event the cursor names exists (any action: the
-- journal is a filter, the cursor names an event that was returned), the database clock in Unix microseconds, and the audit
-- retention in days (0 keeps everything).
SELECT EXISTS (SELECT 1 FROM audit_event WHERE occurred_at = sqlc.arg(cursor_at)::text::timestamptz
                                          AND id = sqlc.arg(cursor_id)::text::uuid)::boolean AS cursor_exists,
       (extract(epoch FROM now()) * 1000000)::bigint AS db_now_us,
       COALESCE((SELECT retention_days FROM audit_policy WHERE id = 1), 0)::int AS retention_days;

-- name: BastionFeedNow :one
-- The feed position of the handshake: the newest journaled event, or none (R-BAS-010 feed.cursor). The caller
-- falls back to a synthetic cursor of the database clock when there is none.
SELECT id::text AS id, occurred_at
  FROM audit_event
 WHERE occurred_at > now() - make_interval(secs => sqlc.arg(window_secs)::float8)
   AND action = ANY(sqlc.arg(actions)::text[])
 ORDER BY occurred_at DESC, id DESC
 LIMIT 1;

-- name: BastionVirtualClosure :many
-- The repository and every virtual repository that includes it, recursively, at the time the feed is read
-- (the content entry of R-BAS-015). Unknown keys return no rows; the caller then reports the key alone.
WITH RECURSIVE up AS (
    SELECT r.id, r.key FROM repositories r WHERE r.key = ANY(sqlc.arg(keys)::text[])
  UNION
    SELECT v.id, v.key
      FROM up
      JOIN virtual_members m ON m.member_id = up.id
      JOIN repositories v ON v.id = m.virtual_id
)
SELECT key FROM up ORDER BY key;

-- name: BastionPrincipalKinds :many
-- The kinds of principals named by an event's details, for the entries that depend on the kind (a role
-- unbound from a group is grants, from a user or service account principal): ids that no longer exist return
-- no row.
SELECT id::text AS id, kind FROM principal WHERE id = ANY(sqlc.arg(ids)::text[]::uuid[]);
