-- Queries of the audit trail and the revocation journal (R-OPS-014 to R-OPS-017,
-- R-HA-054). The audit package is a domain package and imports no driver, so
-- every parameter is a plain Go type: identifiers and times travel as text and
-- are cast here, an absent optional filter is the empty string, and rows come
-- back with text identifiers and no NULLs.

-- name: InsertAuditEvents :many
-- One statement inserts a batch (a transactional event is a batch of one). The
-- batch is one JSON array of flat objects of text values, so the statement stays
-- one shape for any size. "at" is the event's time as RFC 3339 text; the empty
-- string means the transaction's now(), which is what a transactional event and
-- its journal row carry (R-HA-054). A repeated batch whose first attempt did
-- commit inserts nothing (the primary key), so a retry after an ambiguous
-- failure is harmless.
INSERT INTO audit_event (id, occurred_at, action, category, outcome, actor_type, actor_id, actor_name,
                         token_id, auth_method, source_ip, user_agent, trace_id, replica, endpoint,
                         resource_type, resource_id, repository, path, reason, details)
SELECT (e ->> 'id')::uuid, COALESCE(NULLIF(e ->> 'at', '')::timestamptz, now()), e ->> 'action',
       e ->> 'category', e ->> 'outcome', e ->> 'actor_type', NULLIF(e ->> 'actor_id', '')::uuid,
       NULLIF(e ->> 'actor_name', ''), NULLIF(e ->> 'token_id', '')::uuid, NULLIF(e ->> 'auth_method', ''),
       NULLIF(e ->> 'source_ip', '')::inet, NULLIF(e ->> 'user_agent', ''), NULLIF(e ->> 'trace_id', ''),
       NULLIF(e ->> 'replica', ''), NULLIF(e ->> 'endpoint', ''), NULLIF(e ->> 'resource_type', ''),
       NULLIF(e ->> 'resource_id', ''), NULLIF(e ->> 'repository', ''), NULLIF(e ->> 'path', ''),
       NULLIF(e ->> 'reason', ''), e -> 'details'
  FROM jsonb_array_elements(sqlc.arg(events)::text::jsonb) AS e
ON CONFLICT (occurred_at, id) DO NOTHING
RETURNING id::text AS id, occurred_at;

-- name: InsertRevocationOutbox :exec
-- The journal row of a flagged event, in the transaction of the change: the
-- event's id and occurred_at, its registry name and the producer's entry.
INSERT INTO revocation_outbox (id, at, event, entry)
VALUES (sqlc.arg(id)::text::uuid, sqlc.arg(at)::text::timestamptz, sqlc.arg(event)::text, sqlc.arg(entry)::text::jsonb);

-- name: ListAuditEventsNewest :many
-- Newest first, keyset after (cursor_at, cursor_id) when has_cursor. The empty
-- string is "no filter". actor_id is a UUID or the nil UUID when the actor was
-- given as a name; the action prefix is matched literally.
SELECT id::text AS id, occurred_at, action, category, outcome, actor_type,
       COALESCE(actor_id::text, '')::text AS actor_id, COALESCE(actor_name, '')::text AS actor_name,
       COALESCE(token_id::text, '')::text AS token_id, COALESCE(auth_method, '')::text AS auth_method,
       COALESCE(host(source_ip), '')::text AS source_ip, COALESCE(user_agent, '')::text AS user_agent,
       COALESCE(trace_id, '')::text AS trace_id, COALESCE(replica, '')::text AS replica,
       COALESCE(endpoint, '')::text AS endpoint, COALESCE(resource_type, '')::text AS resource_type,
       COALESCE(resource_id, '')::text AS resource_id, COALESCE(repository, '')::text AS repository,
       COALESCE(path, '')::text AS path, COALESCE(reason, '')::text AS reason, details
  FROM audit_event
 WHERE occurred_at >= sqlc.arg(from_at)::text::timestamptz
   AND occurred_at < sqlc.arg(to_at)::text::timestamptz
   AND (sqlc.arg(action)::text = '' OR action = sqlc.arg(action)::text)
   AND (sqlc.arg(action_prefix)::text = '' OR starts_with(action, sqlc.arg(action_prefix)::text))
   AND (sqlc.arg(category)::text = '' OR category = sqlc.arg(category)::text)
   AND (sqlc.arg(outcome)::text = '' OR outcome = sqlc.arg(outcome)::text)
   AND (sqlc.arg(actor)::text = '' OR actor_id = sqlc.arg(actor_id)::text::uuid OR actor_name = sqlc.arg(actor)::text)
   AND (sqlc.arg(repository)::text = '' OR repository = sqlc.arg(repository)::text)
   AND (sqlc.arg(resource_type)::text = '' OR resource_type = sqlc.arg(resource_type)::text)
   AND (sqlc.arg(resource_id)::text = '' OR resource_id = sqlc.arg(resource_id)::text)
   AND (sqlc.arg(trace_id)::text = '' OR trace_id = sqlc.arg(trace_id)::text)
   AND (NOT sqlc.arg(has_cursor)::bool
        OR (occurred_at, id) < (sqlc.arg(cursor_at)::text::timestamptz, sqlc.arg(cursor_id)::text::uuid))
 ORDER BY occurred_at DESC, id DESC
 LIMIT sqlc.arg(row_limit)::int;

-- name: ListAuditEventsOldest :many
-- The same filters, oldest first (the export reads in this order).
SELECT id::text AS id, occurred_at, action, category, outcome, actor_type,
       COALESCE(actor_id::text, '')::text AS actor_id, COALESCE(actor_name, '')::text AS actor_name,
       COALESCE(token_id::text, '')::text AS token_id, COALESCE(auth_method, '')::text AS auth_method,
       COALESCE(host(source_ip), '')::text AS source_ip, COALESCE(user_agent, '')::text AS user_agent,
       COALESCE(trace_id, '')::text AS trace_id, COALESCE(replica, '')::text AS replica,
       COALESCE(endpoint, '')::text AS endpoint, COALESCE(resource_type, '')::text AS resource_type,
       COALESCE(resource_id, '')::text AS resource_id, COALESCE(repository, '')::text AS repository,
       COALESCE(path, '')::text AS path, COALESCE(reason, '')::text AS reason, details
  FROM audit_event
 WHERE occurred_at >= sqlc.arg(from_at)::text::timestamptz
   AND occurred_at < sqlc.arg(to_at)::text::timestamptz
   AND (sqlc.arg(action)::text = '' OR action = sqlc.arg(action)::text)
   AND (sqlc.arg(action_prefix)::text = '' OR starts_with(action, sqlc.arg(action_prefix)::text))
   AND (sqlc.arg(category)::text = '' OR category = sqlc.arg(category)::text)
   AND (sqlc.arg(outcome)::text = '' OR outcome = sqlc.arg(outcome)::text)
   AND (sqlc.arg(actor)::text = '' OR actor_id = sqlc.arg(actor_id)::text::uuid OR actor_name = sqlc.arg(actor)::text)
   AND (sqlc.arg(repository)::text = '' OR repository = sqlc.arg(repository)::text)
   AND (sqlc.arg(resource_type)::text = '' OR resource_type = sqlc.arg(resource_type)::text)
   AND (sqlc.arg(resource_id)::text = '' OR resource_id = sqlc.arg(resource_id)::text)
   AND (sqlc.arg(trace_id)::text = '' OR trace_id = sqlc.arg(trace_id)::text)
   AND (NOT sqlc.arg(has_cursor)::bool
        OR (occurred_at, id) > (sqlc.arg(cursor_at)::text::timestamptz, sqlc.arg(cursor_id)::text::uuid))
 ORDER BY occurred_at ASC, id ASC
 LIMIT sqlc.arg(row_limit)::int;

-- name: GetAuditEvent :many
-- One event by id. The caller bounds occurred_at around the time a UUIDv7 id
-- carries, so only the partitions of that window are read.
SELECT id::text AS id, occurred_at, action, category, outcome, actor_type,
       COALESCE(actor_id::text, '')::text AS actor_id, COALESCE(actor_name, '')::text AS actor_name,
       COALESCE(token_id::text, '')::text AS token_id, COALESCE(auth_method, '')::text AS auth_method,
       COALESCE(host(source_ip), '')::text AS source_ip, COALESCE(user_agent, '')::text AS user_agent,
       COALESCE(trace_id, '')::text AS trace_id, COALESCE(replica, '')::text AS replica,
       COALESCE(endpoint, '')::text AS endpoint, COALESCE(resource_type, '')::text AS resource_type,
       COALESCE(resource_id, '')::text AS resource_id, COALESCE(repository, '')::text AS repository,
       COALESCE(path, '')::text AS path, COALESCE(reason, '')::text AS reason, details
  FROM audit_event
 WHERE id = sqlc.arg(id)::text::uuid
   AND occurred_at >= sqlc.arg(from_at)::text::timestamptz
   AND occurred_at <= sqlc.arg(to_at)::text::timestamptz
 LIMIT 1;

-- name: MaintainAuditPartitions :exec
-- Creates the partitions of the current and the next three months and drops
-- the expired ones (R-OPS-015). The function is SECURITY DEFINER; the runtime
-- role may execute it.
SELECT audit_maintain_partitions();

-- name: CountReadyAuditPartitions :one
-- How many of the partitions of the current and the next month exist (0 to 2).
SELECT ((to_regclass('audit_event_' || to_char(date_trunc('month', now() AT TIME ZONE 'UTC'), 'YYYYMM')) IS NOT NULL)::int
      + (to_regclass('audit_event_' || to_char(date_trunc('month', now() AT TIME ZONE 'UTC') + interval '1 month', 'YYYYMM')) IS NOT NULL)::int)::int AS ready;

-- name: GetAuditRetention :one
SELECT retention_days FROM audit_policy WHERE id = 1;

-- name: SetAuditRetention :execrows
-- Writes the configured retention; only the schema owner may (Q-OPS-4). It
-- touches the row only when the value changes.
UPDATE audit_policy
   SET retention_days = sqlc.arg(retention_days)::int, updated_at = now()
 WHERE id = 1 AND retention_days IS DISTINCT FROM sqlc.arg(retention_days)::int;
