-- Queries of the revocation journal's replay handlers (R-HA-054, R-AUTHZ-031, R-AUTH-041; the handlers are the
-- journal_replay.go files of internal/auth/token, internal/auth/principal, internal/authz and internal/project, and the
-- restore's clean-up in internal/journal/replay). A replay changes the restored database only by removing or narrowing, and it
-- names every object by its ID, so these statements take IDs, never names.

-- name: JournalTokenState :many
-- The state of a token of any type (the token API's IdentityTokenRow leaves out Bastion tokens, which a replay must revoke
-- as well). No row: the token is not in the restored database.
SELECT t.id::text AS id, t.type, (t.revoked_at IS NOT NULL)::boolean AS revoked
  FROM api_token t WHERE t.id = sqlc.arg(id)::text::uuid;

-- name: JournalLimitTokenExpiry :many
-- token.rotated: the predecessor expires at the earlier of its current expiry and the journaled one. A later expiry than the
-- stored one changes nothing, so a replay never extends a token.
UPDATE api_token SET expires_at = to_timestamp(sqlc.arg(expires_us)::bigint / 1000000.0)
 WHERE id = sqlc.arg(id)::text::uuid AND revoked_at IS NULL
   AND (expires_at IS NULL OR expires_at > to_timestamp(sqlc.arg(expires_us)::bigint / 1000000.0))
RETURNING encode(secret_sha256, 'hex') AS secret_hex;

-- name: JournalUserState :many
-- The user behind a principal, to tell a disabled user from a disabled break-glass account.
SELECT p.kind, p.status, coalesce(u.is_break_glass, false)::boolean AS is_break_glass
  FROM principal p LEFT JOIN app_user u ON u.principal_id = p.id
 WHERE p.id = sqlc.arg(id)::text::uuid;

-- name: JournalRepositoryLock :many
-- A repository by its ID under its row lock, with its project; a repository being deleted is returned too and the caller
-- answers it as unknown.
SELECT r.id, r.key, r.class, r.config, r.lifecycle, r.project_id::text AS project_id, p.key AS project_key
  FROM repositories r JOIN project p ON p.id = r.project_id
 WHERE r.id = sqlc.arg(id)::bigint
   FOR UPDATE OF r;

-- name: JournalRepositorySetConfig :many
UPDATE repositories
   SET config = sqlc.arg(config)::text::jsonb, config_version = config_version + 1, updated_at = now()
 WHERE id = sqlc.arg(id)::bigint AND lifecycle = 'active'
RETURNING config_version;

-- name: JournalProjectLevelLock :many
-- A project by its ID under its row lock, with its level; no row: the project is not in the restored database.
SELECT key, visibility FROM project WHERE id = sqlc.arg(id)::text::uuid FOR UPDATE;

-- name: JournalProjectSetLevel :exec
-- The replay of authz.project.visibility_changed (R-AUTHZ-036): the level is taken to the journaled one, which the caller has
-- compared as not above the current one.
UPDATE project SET visibility = sqlc.arg(level)::text, version = version + 1, updated_at = now()
 WHERE id = sqlc.arg(id)::text::uuid;

-- name: JournalProjectLowerRepositories :many
-- The cascade of R-AUTHZ-033 rule 3: every repository of the project whose artifacts or wiki level lies above the level is
-- taken down to it; its configuration version moves on.
UPDATE repositories r
   SET visibility_artifacts = CASE WHEN visibility_rank(visibility_artifacts) > visibility_rank(sqlc.arg(level)::text) THEN sqlc.arg(level)::text ELSE visibility_artifacts END,
       visibility_wiki      = CASE WHEN visibility_rank(visibility_wiki) > visibility_rank(sqlc.arg(level)::text) THEN sqlc.arg(level)::text ELSE visibility_wiki END,
       config_version = config_version + 1, updated_at = now()
 WHERE r.project_id = sqlc.arg(project_id)::text::uuid
   AND (visibility_rank(visibility_artifacts) > visibility_rank(sqlc.arg(level)::text) OR visibility_rank(visibility_wiki) > visibility_rank(sqlc.arg(level)::text))
RETURNING r.id, r.key, r.config_version;

-- name: JournalOwnership :many
-- The name's claim row, locked; no row: the name is not claimed in the restored database.
SELECT state FROM package_ownership
 WHERE domain = sqlc.arg(domain)::text AND format = sqlc.arg(format)::text AND package_name = sqlc.arg(package_name)::text
   FOR UPDATE;

-- name: JournalOwnerRemovePrincipal :execrows
DELETE FROM package_owner
 WHERE domain = sqlc.arg(domain)::text AND format = sqlc.arg(format)::text AND package_name = sqlc.arg(package_name)::text
   AND principal_id = sqlc.arg(principal_id)::text::uuid;

-- name: JournalOwnerRemoveCIProject :execrows
DELETE FROM package_owner
 WHERE domain = sqlc.arg(domain)::text AND format = sqlc.arg(format)::text AND package_name = sqlc.arg(package_name)::text
   AND ci_issuer_id = sqlc.arg(ci_issuer_id)::text::uuid AND ci_project_id = sqlc.arg(ci_project_id)::text;

-- name: JournalRoleBindingExists :one
SELECT EXISTS (SELECT 1 FROM principal WHERE id = sqlc.arg(principal_id)::text::uuid) AS principal_exists;

-- name: JournalPolicyByID :many
-- A policy by ID, locked, with the key of its project.
SELECT p.id::text AS id, p.name, p.resource_type, coalesce(pr.key, '')::text AS project_key
  FROM authz_policy p LEFT JOIN project pr ON pr.id = p.project_id
 WHERE p.id = sqlc.arg(id)::text::uuid
   FOR UPDATE OF p;

-- name: JournalPolicyDelete :execrows
-- The selectors, patterns and entries of both entry tables go with the row (ON DELETE CASCADE).
DELETE FROM authz_policy WHERE id = sqlc.arg(id)::text::uuid;

-- name: JournalPolicyTouch :exec
UPDATE authz_policy SET version = version + 1, updated_at = now() WHERE id = sqlc.arg(id)::text::uuid;

-- name: JournalPolicyClearSelectorsPatterns :exec
WITH s AS (DELETE FROM authz_policy_selector WHERE policy_id = sqlc.arg(policy_id)::text::uuid)
DELETE FROM authz_policy_pattern WHERE policy_id = sqlc.arg(policy_id)::text::uuid;

-- name: JournalPolicyEntryDelete :exec
-- An entry of the policy for the principal, from whichever table holds it.
WITH a AS (DELETE FROM authz_policy_entry WHERE policy_id = sqlc.arg(policy_id)::text::uuid AND principal_id = sqlc.arg(principal_id)::text::uuid)
DELETE FROM authz_project_policy_entry WHERE policy_id = sqlc.arg(policy_id)::text::uuid AND principal_id = sqlc.arg(principal_id)::text::uuid;

-- name: JournalPolicyEntrySetActions :exec
WITH a AS (UPDATE authz_policy_entry SET actions = sqlc.arg(actions)::text[]
            WHERE policy_id = sqlc.arg(policy_id)::text::uuid AND principal_id = sqlc.arg(principal_id)::text::uuid)
UPDATE authz_project_policy_entry SET actions = sqlc.arg(actions)::text[]
 WHERE policy_id = sqlc.arg(policy_id)::text::uuid AND principal_id = sqlc.arg(principal_id)::text::uuid;

-- name: JournalPolicyClearEntries :exec
WITH a AS (DELETE FROM authz_policy_entry WHERE policy_id = sqlc.arg(policy_id)::text::uuid)
DELETE FROM authz_project_policy_entry WHERE policy_id = sqlc.arg(policy_id)::text::uuid;

-- name: JournalProjectLock :many
-- A project by ID under its row lock, which every member change and move takes.
SELECT id::text AS id, key FROM project WHERE id = sqlc.arg(id)::text::uuid FOR UPDATE;

-- name: JournalProjectHolds :one
-- What keeps a project from being deleted: repositories, service accounts and CI mappings (R-AUTHZ-031).
SELECT (SELECT count(*) FROM repositories WHERE project_id = sqlc.arg(id)::text::uuid)::bigint AS repositories,
       (SELECT count(*) FROM service_account WHERE project_id = sqlc.arg(id)::text::uuid)::bigint AS service_accounts,
       (SELECT count(*) FROM ci_mappings WHERE project_id = sqlc.arg(id)::text::uuid)::bigint AS ci_mappings;

-- name: JournalProjectDelete :execrows
DELETE FROM project WHERE id = sqlc.arg(id)::text::uuid;

-- name: JournalProjectMembersRemoveAll :execrows
DELETE FROM project_member WHERE project_id = sqlc.arg(id)::text::uuid;

-- name: JournalPauseDeletions :exec
-- The deletion pause of R-HA-025: GC, the orphan sweep, retention and cap enforcement skip their batches while it is set.
INSERT INTO system_state (key, value) VALUES
    ('deletions_paused_at', to_jsonb(to_char(now() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'))),
    ('deletions_paused_reason', to_jsonb(sqlc.arg(reason)::text))
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()
 WHERE system_state.value = 'null'::jsonb;

-- name: JournalDeleteReplicaRows :execrows
-- The rows of replica describe pods that no longer exist after a platform restore.
DELETE FROM replica;

-- name: JournalSetRestoredAt :exec
-- The restore cut-off of the single-use CI issuers (R-CI-018, R-BAK-022): the time the clean-up ran, on the database's clock.
INSERT INTO system_state (key, value)
VALUES ('restored_at', to_jsonb(to_char(now() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')))
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();

-- name: JournalSuppressBinding :exec
-- A role binding that a replay removed ("<role>|<principal id>"). trove migrate does not apply a declared binding that is
-- marked until an administrator binds it again (R-AUTHZ-010, Q-AUTHZ-235).
INSERT INTO system_state (key, value) VALUES ('journal_suppressed_bindings', jsonb_build_array(sqlc.arg(entry)::text))
ON CONFLICT (key) DO UPDATE
   SET value = CASE WHEN jsonb_exists(system_state.value, EXCLUDED.value ->> 0) THEN system_state.value ELSE system_state.value || EXCLUDED.value END,
       updated_at = now();

-- name: JournalUnsuppressBinding :exec
UPDATE system_state
   SET value = COALESCE((SELECT jsonb_agg(to_jsonb(e)) FROM jsonb_array_elements_text(system_state.value) e WHERE e <> sqlc.arg(entry)::text), '[]'::jsonb),
       updated_at = now()
 WHERE key = 'journal_suppressed_bindings' AND jsonb_exists(value, sqlc.arg(entry)::text);

-- name: JournalSuppressedBindings :many
SELECT e::text AS entry FROM system_state, jsonb_array_elements_text(system_state.value) e WHERE key = 'journal_suppressed_bindings';

-- name: JournalNewestAuditEvent :one
-- The time of the newest audit event of the restored database: what the database really holds, whatever the operator says
-- the restore point is (the replay starts at the earlier of the two).
SELECT COALESCE((extract(epoch FROM max(occurred_at)) * 1000000)::bigint, 0)::bigint AS newest_us FROM audit_event;

-- name: JournalQuarantine :exec
-- An outbox row that cannot be encoded as a journal object. It leaves the outbox so that it does not hold back the rows
-- behind it, and stays visible here (post-restore prints it) and in trove_revocation_journal_quarantined_total.
INSERT INTO system_state (key, value) VALUES ('journal_quarantined', jsonb_build_array(sqlc.arg(entry)::text::jsonb))
ON CONFLICT (key) DO UPDATE SET value = system_state.value || EXCLUDED.value, updated_at = now();

-- name: JournalQuarantined :one
SELECT COALESCE((SELECT jsonb_array_length(value) FROM system_state WHERE key = 'journal_quarantined'), 0)::bigint AS n;

-- name: JournalSetPostRestoreMarker :exec
-- The marker that gates replicas while post-restore runs (key post_restore_in_progress, read by the schema check).
INSERT INTO system_state (key, value) VALUES ('post_restore_in_progress', sqlc.arg(value)::text::jsonb)
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();

-- name: JournalGetState :many
SELECT value::text AS value FROM system_state WHERE key = sqlc.arg(key)::text;

-- name: JournalSetState :exec
INSERT INTO system_state (key, value) VALUES (sqlc.arg(key)::text, sqlc.arg(value)::text::jsonb)
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();

-- name: JournalDeleteState :execrows
DELETE FROM system_state WHERE key = sqlc.arg(key)::text;
