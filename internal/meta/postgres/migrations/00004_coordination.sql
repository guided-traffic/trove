-- trove:phase expand
--
-- Coordination tables: the job queue (R-ARCH-016), the change log with per-area
-- epochs (R-ARCH-018) and the replica registry (R-OPS-003). Leases are session
-- advisory locks (R-ARCH-017) and have no table.

-- Created inside a DO block so that sqlc, which reads these files as its schema,
-- sees job_state as an unknown type and maps it to a Go string (sqlc.yaml
-- override): its generated enum helpers would import database/sql/driver, which
-- no trove package may (R-ARCH-026).
DO $$
BEGIN
    CREATE TYPE job_state AS ENUM ('queued', 'running', 'succeeded', 'failed', 'cancelling', 'cancelled');
END $$;

CREATE TABLE job (
    id             uuid        PRIMARY KEY,
    kind           text        NOT NULL,
    queue          text        NOT NULL DEFAULT 'default',
    args           jsonb       NOT NULL DEFAULT '{}',
    state          job_state   NOT NULL DEFAULT 'queued',
    priority       smallint    NOT NULL DEFAULT 100,
    run_at         timestamptz NOT NULL DEFAULT now(),
    attempt        integer     NOT NULL DEFAULT 0,
    max_attempts   integer     NOT NULL DEFAULT 10,
    dedup_key      text,
    dedup_mode     text        CHECK (dedup_mode IN ('exclusive', 'coalesce')),
    task           text,
    scheduled_for  timestamptz,
    target_kind    text,
    target_key     text,
    locked_by      uuid,
    locked_until   timestamptz,
    progress_done  bigint,
    progress_total bigint,
    progress_unit  text,
    checkpoint     jsonb,
    result         jsonb,
    error          jsonb,
    created_by     uuid,
    traceparent    text,
    created_at     timestamptz NOT NULL DEFAULT now(),
    started_at     timestamptz,
    finished_at    timestamptz,
    CHECK ((dedup_key IS NULL) = (dedup_mode IS NULL))
);
CREATE INDEX job_fetch ON job (queue, priority, run_at) WHERE state = 'queued';
CREATE INDEX job_lease ON job (locked_until) WHERE state IN ('running', 'cancelling');
CREATE INDEX job_running_key ON job (dedup_key) WHERE state IN ('running', 'cancelling');
CREATE UNIQUE INDEX job_one_waiting ON job (dedup_key)
    WHERE dedup_key IS NOT NULL AND state = 'queued';
CREATE UNIQUE INDEX job_one_active ON job (dedup_key)
    WHERE dedup_mode = 'exclusive' AND state IN ('queued', 'running', 'cancelling');
CREATE UNIQUE INDEX job_task_slot ON job (task, scheduled_for) WHERE task IS NOT NULL;
-- Retention deletes finished jobs by age; the reaper and the job API list by state.
CREATE INDEX job_finished ON job (finished_at) WHERE finished_at IS NOT NULL;

CREATE TABLE cluster_epoch (
    area       text        PRIMARY KEY,
    epoch      bigint      NOT NULL DEFAULT 0,
    changed_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE cluster_change (
    area       text        NOT NULL REFERENCES cluster_epoch (area),
    epoch      bigint      NOT NULL,
    payload    jsonb       NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (area, epoch)
);

-- Change areas that exist from the first release. A page that adds a change area
-- seeds its row in the migration that ships it (an expand step).
INSERT INTO cluster_epoch (area) VALUES ('cache');

CREATE TABLE replica (
    instance_uid         uuid        PRIMARY KEY,
    name                 text        NOT NULL,
    version              text        NOT NULL,
    revision             text        NOT NULL,
    roles                text[]      NOT NULL,
    state                text        NOT NULL CHECK (state IN ('starting', 'ready', 'unready', 'draining')),
    checks               jsonb       NOT NULL DEFAULT '{}',
    leases               jsonb       NOT NULL DEFAULT '[]',
    secret_key_ids       text[]      NOT NULL,
    active_secret_key_id text        NOT NULL,
    node                 text,
    valkey               jsonb       NOT NULL DEFAULT '{}',
    pool                 text,
    started_at           timestamptz NOT NULL,
    heartbeat_at         timestamptz NOT NULL
);
CREATE INDEX replica_heartbeat ON replica (heartbeat_at);
