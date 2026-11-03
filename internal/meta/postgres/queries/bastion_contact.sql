-- Queries of the last contact of a Bastion replica (R-BAS-010, R-BAS-007; work package 2 writes, work package
-- 1 reads). The table bastion_contact (00032) holds one row per registration and replica.

-- name: BastionContactUpsert :one
-- Record the contact of a replica from the handshake, at most once a minute per replica: the row is written
-- when it is new, when its last write is a minute old, or when a reported value differs. written says
-- whether a row was written; changed says whether a reported value differs from the stored row (or there was
-- none), which is the condition of the asynchronous event bastion.contact (R-BAS-010).
WITH old AS (
    SELECT client_address, release, interface_version, base_url
      FROM bastion_contact
     WHERE bastion_id = sqlc.arg(bastion_id)::text::uuid AND replica = sqlc.arg(replica)::text
), up AS (
    INSERT INTO bastion_contact AS c (bastion_id, replica, contact_at, client_address, release, interface_version, base_url, token_id)
    VALUES (sqlc.arg(bastion_id)::text::uuid, sqlc.arg(replica)::text, now(), NULLIF(sqlc.arg(client_address)::text, '')::inet,
            sqlc.arg(release)::text, sqlc.arg(interface_version)::int, sqlc.arg(base_url)::text,
            NULLIF(sqlc.arg(token_id)::text, '')::uuid)
    ON CONFLICT (bastion_id, replica) DO UPDATE
       SET contact_at = excluded.contact_at, client_address = excluded.client_address, release = excluded.release,
           interface_version = excluded.interface_version, base_url = excluded.base_url, token_id = excluded.token_id
     WHERE c.contact_at <= excluded.contact_at - interval '1 minute'
        OR c.client_address IS DISTINCT FROM excluded.client_address OR c.release <> excluded.release
        OR c.interface_version <> excluded.interface_version OR c.base_url <> excluded.base_url
    RETURNING 1
)
SELECT (SELECT count(*) FROM up)::bigint AS written,
       (NOT EXISTS (SELECT 1 FROM old)
        OR EXISTS (SELECT 1 FROM old o
                    WHERE o.client_address IS DISTINCT FROM NULLIF(sqlc.arg(client_address)::text, '')::inet
                       OR o.release <> sqlc.arg(release)::text OR o.interface_version <> sqlc.arg(interface_version)::int
                       OR o.base_url <> sqlc.arg(base_url)::text))::boolean AS changed;

-- name: BastionContactList :many
-- The contacts of one registration, the most recent first (the registration's document, R-BAS-004).
SELECT replica, contact_at, coalesce(host(client_address), '')::text AS client_address, release, interface_version,
       base_url, coalesce(token_id::text, '')::text AS token_id
  FROM bastion_contact
 WHERE bastion_id = sqlc.arg(bastion_id)::text::uuid
 ORDER BY contact_at DESC, replica;

-- name: BastionContactListAll :many
-- The contacts of every registration, for the list.
SELECT bastion_id::text AS bastion_id, replica, contact_at, coalesce(host(client_address), '')::text AS client_address,
       release, interface_version, base_url, coalesce(token_id::text, '')::text AS token_id
  FROM bastion_contact
 ORDER BY bastion_id, contact_at DESC, replica;

-- name: BastionLastBaseURL :many
-- The base URL a registration reported at its most recent contact, for client-setup with bastion=<name>
-- (R-BAS-007). No row before the first contact.
SELECT c.base_url
  FROM bastion_contact c JOIN principal p ON p.id = c.bastion_id
 WHERE p.kind = 'bastion' AND p.name = sqlc.arg(name)::text AND c.base_url <> ''
 ORDER BY c.contact_at DESC
 LIMIT 1;

-- name: BastionContactPrune :execrows
-- Drop the contacts of replicas not heard for older_than_secs seconds (30 days): the pod names of old
-- rollouts. Run by the writer after an upsert that inserted a row.
DELETE FROM bastion_contact WHERE contact_at < now() - make_interval(secs => sqlc.arg(older_than_secs)::float8);
