-- Queries of the change log (R-ARCH-018) and the replica registry (R-OPS-003).

-- The change log is written through meta.Tx (cluster.Changes.Notify): the two
-- statements of R-ARCH-018 step 1 run on the writer's transaction.

-- name: ListClusterEpochs :many
SELECT area, epoch, changed_at FROM cluster_epoch WHERE area = ANY(sqlc.arg(areas)::text[]);

-- name: ListClusterChanges :many
SELECT epoch, payload, created_at FROM cluster_change
 WHERE area = sqlc.arg(area)::text AND epoch >= sqlc.arg(from_epoch)::bigint AND epoch <= sqlc.arg(to_epoch)::bigint
 ORDER BY epoch;

-- name: PruneClusterChanges :execrows
-- cluster-change-prune (R-ARCH-016): a row goes once the next row of its area is
-- older than the retention, so the newest row of every area stays. "Next" is the
-- row with the next higher epoch, whether or not the epochs are contiguous.
DELETE FROM cluster_change c
 USING (SELECT area, epoch, lead(created_at) OVER (PARTITION BY area ORDER BY epoch) AS next_created_at
          FROM cluster_change) n
 WHERE n.area = c.area AND n.epoch = c.epoch
   AND n.next_created_at < now() - make_interval(secs => sqlc.arg(retention_secs)::float8);

-- name: UpsertReplica :exec
-- Registration and heartbeat in one statement: a row deleted by the prune or by
-- a restore comes back with the next heartbeat. heartbeat_at is the database's
-- clock; replica clocks are never compared (R-HA-003).
INSERT INTO replica (instance_uid, name, version, revision, roles, state, checks, leases,
                     secret_key_ids, active_secret_key_id, node, valkey, pool, started_at, heartbeat_at)
VALUES (sqlc.arg(instance_uid)::text::uuid, sqlc.arg(name)::text, sqlc.arg(version)::text, sqlc.arg(revision)::text,
        sqlc.arg(roles)::text[], sqlc.arg(state)::text, sqlc.arg(checks)::text::jsonb, sqlc.arg(leases)::text::jsonb,
        sqlc.arg(secret_key_ids)::text[], sqlc.arg(active_secret_key_id)::text,
        NULLIF(sqlc.arg(node)::text, ''), sqlc.arg(valkey)::text::jsonb, NULLIF(sqlc.arg(pool)::text, ''),
        sqlc.arg(started_at)::timestamptz, now())
ON CONFLICT (instance_uid) DO UPDATE
   SET state = EXCLUDED.state, checks = EXCLUDED.checks, leases = EXCLUDED.leases,
       secret_key_ids = EXCLUDED.secret_key_ids, active_secret_key_id = EXCLUDED.active_secret_key_id,
       valkey = EXCLUDED.valkey, heartbeat_at = now();

-- name: SetReplicaState :execrows
UPDATE replica SET state = sqlc.arg(state)::text, heartbeat_at = now()
 WHERE instance_uid = sqlc.arg(instance_uid)::text::uuid;

-- name: DeleteReplica :exec
DELETE FROM replica WHERE instance_uid = sqlc.arg(instance_uid)::text::uuid;

-- name: ListReplicas :many
SELECT r.*, (r.heartbeat_at < now() - make_interval(secs => sqlc.arg(stale_secs)::float8))::bool AS lost
  FROM replica r
 ORDER BY r.roles, r.name;

-- name: PruneReplicas :execrows
-- replica-registry-prune (R-OPS-003): rows stale for the given time.
DELETE FROM replica WHERE heartbeat_at < now() - make_interval(secs => sqlc.arg(stale_secs)::float8);
