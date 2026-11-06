-- The counts of GET /api/v1/system/overview (R-OPS-004, Q-OPS-175). The handler runs the five statements on one
-- connection of the request pool and keeps the result for its cache time, so a dashboard that refreshes does not count
-- again on every request. A count is linear in the rows it counts; where a predicate matches a partial index, the
-- statement says which. The cost per count is in docs/developer/http-api.md (System overview). Plain Go types only:
-- counts are cast to int or bigint.

-- name: OverviewRepositories :many
-- The repositories per class. A repository whose deletion runs (lifecycle 'deleting') no longer counts.
SELECT class, count(*)::int AS repositories
  FROM repositories
 WHERE lifecycle = 'active'
 GROUP BY class
 ORDER BY class;

-- name: OverviewPrincipals :many
-- The principals of the kinds the overview names, disabled ones included: users, groups, service accounts and Bastion
-- registrations. Reads the unique index on (kind, name).
SELECT kind, count(*)::int AS principals
  FROM principal
 WHERE kind IN ('user', 'group', 'service_account', 'bastion')
 GROUP BY kind
 ORDER BY kind;

-- name: OverviewContent :one
-- Packages, live versions (trashed_at IS NULL, the predicate of the index package_versions_live), tokens neither revoked
-- nor expired (the predicate of IdentityCountActiveTokens; the partial index api_token_expiry_active) and projects.
SELECT (SELECT count(*) FROM packages)::bigint AS packages,
       (SELECT count(*) FROM package_versions WHERE trashed_at IS NULL)::bigint AS package_versions,
       (SELECT count(*) FROM api_token
         WHERE revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now()))::int AS active_tokens,
       (SELECT count(*) FROM project)::int AS projects;

-- name: OverviewStorage :one
-- The live blobs and their bytes as the usage fold maintains them in the single row of storage_usage_totals (R-STOR-003;
-- never a sum over blobs), and the candidates of the garbage collection's queue.
SELECT COALESCE((SELECT t.blobs FROM storage_usage_totals t WHERE t.id = 1), 0)::bigint AS blobs,
       COALESCE((SELECT t.physical_bytes FROM storage_usage_totals t WHERE t.id = 1), 0)::bigint AS bytes,
       (SELECT count(*) FROM blob_gc_queue)::bigint AS pending_deletion;

-- name: OverviewJobs :one
-- The job queue: one scalar subquery per partial index (job_fetch for queued, job_lease for running and cancelling,
-- job_finished for the failures of the last 24 hours on PostgreSQL's clock).
SELECT (SELECT count(*) FROM job WHERE state = 'queued')::int AS queued,
       (SELECT count(*) FROM job WHERE state IN ('running', 'cancelling'))::int AS running,
       (SELECT count(*) FROM job
         WHERE state = 'failed' AND finished_at > now() - interval '24 hours')::int AS failed_last_24h;
