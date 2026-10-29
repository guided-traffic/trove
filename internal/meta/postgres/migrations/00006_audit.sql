-- trove:phase expand
--
-- Audit trail and revocation journal (M1, WP1.7). audit_event holds the audit
-- events (R-OPS-015, R-OPS-016), one partition per calendar month (UTC), and is
-- append-only for the runtime role (Q-OPS-4): partitions are created and dropped
-- only by audit_maintain_partitions(), a SECURITY DEFINER function of the schema
-- owner, which retention reads from the owner-written audit_policy.
-- revocation_outbox is the journal of access removals that the audit writer
-- inserts in the transaction of the change (R-OPS-014, R-OPS-015, R-HA-054).

CREATE TABLE audit_event (
    id             uuid        NOT NULL,
    occurred_at    timestamptz NOT NULL,
    action         text        NOT NULL,
    category       text        NOT NULL,
    outcome        text        NOT NULL CHECK (outcome IN ('success', 'denied', 'failure')),
    actor_type     text        NOT NULL,
    actor_id       uuid,
    actor_name     text,
    token_id       uuid,
    auth_method    text,
    source_ip      inet,
    user_agent     text,
    trace_id       text,
    replica        text,
    endpoint       text,
    resource_type  text,
    resource_id    text,
    repository     text,
    path           text,
    reason         text,
    details        jsonb       NOT NULL DEFAULT '{}'::jsonb,
    PRIMARY KEY (occurred_at, id)
) PARTITION BY RANGE (occurred_at);

-- No foreign keys on any column: an event outlives the principal, token or
-- repository it names. The indexes serve the filters of GET /api/v1/audit/events
-- (R-OPS-017); the export reads by (occurred_at, id), which the primary key serves.
CREATE INDEX audit_event_actor_idx      ON audit_event (actor_id, occurred_at DESC);
CREATE INDEX audit_event_action_idx     ON audit_event (action, occurred_at DESC);
CREATE INDEX audit_event_repository_idx ON audit_event (repository, occurred_at DESC) WHERE repository IS NOT NULL;
CREATE INDEX audit_event_trace_idx      ON audit_event (trace_id) WHERE trace_id IS NOT NULL;

-- Retention is owner data: trove migrate writes audit.retention_days into the
-- row on every run, and the runtime role can read it only. The row is seeded with
-- the default of audit.retention_days so that the function below works from the
-- first migration on.
CREATE TABLE audit_policy (
    id              smallint    PRIMARY KEY CHECK (id = 1),
    retention_days  integer     NOT NULL CHECK (retention_days >= 0),
    updated_at      timestamptz NOT NULL DEFAULT now()
);
INSERT INTO audit_policy (id, retention_days) VALUES (1, 365);

-- The journal of R-HA-054: a worker job ships the rows to object storage in id
-- order and deletes each one after its object was written.
CREATE TABLE revocation_outbox (
    id     uuid        PRIMARY KEY,
    at     timestamptz NOT NULL,
    event  text        NOT NULL,
    entry  jsonb       NOT NULL
);

-- Creates the monthly partition that contains the given instant, if it is
-- missing. The partition is built as a standalone table and attached, which takes
-- only SHARE UPDATE EXCLUSIVE on the parent: inserts and reads do not wait for it,
-- while CREATE TABLE ... PARTITION OF would take ACCESS EXCLUSIVE (R-OPS-015).
-- The runtime role gets no privilege on the partition; it reaches the rows
-- through the parent. Called by the two functions below only; nobody else holds
-- EXECUTE on it.
CREATE FUNCTION audit_attach_partition(instant timestamptz) RETURNS void
    LANGUAGE plpgsql AS $$
DECLARE
    lo     timestamp := date_trunc('month', instant AT TIME ZONE 'UTC');
    hi     timestamp := date_trunc('month', instant AT TIME ZONE 'UTC') + interval '1 month';
    part   text      := 'audit_event_' || to_char(date_trunc('month', instant AT TIME ZONE 'UTC'), 'YYYYMM');
    sch    text      := current_schema();
BEGIN
    IF to_regclass(format('%I.%I', sch, part)) IS NOT NULL THEN
        RETURN;
    END IF;
    EXECUTE format('CREATE TABLE %I.%I (LIKE %I.audit_event INCLUDING ALL)', sch, part, sch);
    EXECUTE format('ALTER TABLE %I.audit_event ATTACH PARTITION %I.%I FOR VALUES FROM (%L) TO (%L)',
                   sch, sch, part,
                   to_char(lo, 'YYYY-MM-DD HH24:MI:SS') || '+00',
                   to_char(hi, 'YYYY-MM-DD HH24:MI:SS') || '+00');
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'trove_app') THEN
        EXECUTE format('REVOKE ALL ON %I.%I FROM trove_app', sch, part);
    END IF;
END $$;

-- Creates the partitions for the current month and the next three, and drops the
-- partitions whose upper bound is older than audit_policy.retention_days (0 keeps
-- everything). Concurrent calls serialise on the audit_policy row; a call that
-- cannot get the row within the lock timeout leaves the work to the one that
-- holds it. A partition that cannot be locked in time (a backup's snapshot
-- transaction holds ACCESS SHARE on it) is skipped and the next call retries it.
CREATE FUNCTION audit_maintain_partitions() RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER SET lock_timeout = '2s' AS $$
DECLARE
    retention  integer;
    this_month timestamp := date_trunc('month', now() AT TIME ZONE 'UTC');
    cutoff     timestamp;
    k          integer;
    part       record;
    sch        text := current_schema();
BEGIN
    BEGIN
        SELECT retention_days INTO STRICT retention FROM audit_policy WHERE id = 1 FOR UPDATE;
    EXCEPTION
        WHEN lock_not_available THEN
            RETURN;
        WHEN no_data_found THEN
            RAISE EXCEPTION 'audit_policy has no row; run trove migrate';
    END;

    FOR k IN 0..3 LOOP
        BEGIN
            PERFORM audit_attach_partition((this_month + make_interval(months => k)) AT TIME ZONE 'UTC');
        EXCEPTION WHEN lock_not_available THEN
            NULL;
        END;
    END LOOP;

    IF retention > 0 THEN
        cutoff := (now() AT TIME ZONE 'UTC') - make_interval(days => retention);
        FOR part IN
            SELECT c.relname::text AS name
              FROM pg_inherits inh
              JOIN pg_class c ON c.oid = inh.inhrelid
             WHERE inh.inhparent = to_regclass(format('%I.audit_event', sch))
               AND c.relname ~ '^audit_event_[0-9]{6}$'
               AND to_date(substr(c.relname, 13, 6) || '01', 'YYYYMMDD') + interval '1 month' <= cutoff
             ORDER BY c.relname
        LOOP
            BEGIN
                EXECUTE format('DROP TABLE %I.%I', sch, part.name);
            EXCEPTION WHEN lock_not_available THEN
                NULL;
            END;
        END LOOP;
    END IF;
END $$;

-- The owner's companion for a restore (R-BAK-021 step 4): creates every missing
-- monthly partition that overlaps [range_from, range_to] and drops nothing, so a
-- restore never loses a loaded month to the retention step. The runtime role
-- holds no EXECUTE on it.
CREATE FUNCTION audit_create_partitions(range_from timestamptz, range_to timestamptz) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER SET lock_timeout = '2s' AS $$
DECLARE
    m     timestamp := date_trunc('month', range_from AT TIME ZONE 'UTC');
    stop  timestamp := range_to AT TIME ZONE 'UTC';
BEGIN
    IF range_to < range_from THEN
        RAISE EXCEPTION 'audit_create_partitions: the range ends before it starts';
    END IF;
    IF (extract(year FROM stop) - extract(year FROM m)) * 12 + extract(month FROM stop) - extract(month FROM m) >= 1200 THEN
        RAISE EXCEPTION 'audit_create_partitions: the range covers more than 1200 months';
    END IF;
    WHILE m <= stop LOOP
        PERFORM audit_attach_partition(m AT TIME ZONE 'UTC');
        m := m + interval '1 month';
    END LOOP;
END $$;

-- The functions run with a fixed search_path (the schema the runner put on
-- search_path), so a SECURITY DEFINER body never resolves a name through a
-- caller's path. Wrapped in a block because the schema name is configuration.
DO $$
BEGIN
    EXECUTE format('ALTER FUNCTION %I.audit_attach_partition(timestamptz) SET search_path = %I, pg_temp',
                   current_schema(), current_schema());
    EXECUTE format('ALTER FUNCTION %I.audit_maintain_partitions() SET search_path = %I, pg_temp',
                   current_schema(), current_schema());
    EXECUTE format('ALTER FUNCTION %I.audit_create_partitions(timestamptz, timestamptz) SET search_path = %I, pg_temp',
                   current_schema(), current_schema());
    EXECUTE format('REVOKE ALL ON FUNCTION %I.audit_attach_partition(timestamptz) FROM PUBLIC', current_schema());
    EXECUTE format('REVOKE ALL ON FUNCTION %I.audit_maintain_partitions() FROM PUBLIC', current_schema());
    EXECUTE format('REVOKE ALL ON FUNCTION %I.audit_create_partitions(timestamptz, timestamptz) FROM PUBLIC', current_schema());
END $$;

-- Append-only for the runtime role (Q-OPS-4): it inserts and reads audit events,
-- reads the retention, may call audit_maintain_partitions(), and can neither
-- change nor delete an event nor shorten the retention. In single-role mode the
-- role does not exist and nothing is revoked.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'trove_app') THEN
        EXECUTE format('REVOKE UPDATE, DELETE, TRUNCATE ON %I.audit_event FROM trove_app', current_schema());
        EXECUTE format('REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON %I.audit_policy FROM trove_app', current_schema());
        EXECUTE format('GRANT EXECUTE ON FUNCTION %I.audit_maintain_partitions() TO trove_app', current_schema());
    END IF;
END $$;

-- The partitions for the current month and the next three exist from the first
-- migration on; later calls come from the audit-retention task and from every
-- replica's start-up.
SELECT audit_maintain_partitions();
