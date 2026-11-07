-- trove:phase expand
--
-- The backup writes its own audit events (R-BAK-014) through a function, not through INSERT on audit_event (R-BAK-015, Q-BAK-50).
-- A login that could insert into the audit trail could write any event under any actor; this function writes the four events of
-- the backup lane as the actor system:backup and nothing else. It runs as the schema owner (SECURITY DEFINER) with a fixed
-- search_path, and EXECUTE goes to the backup login that database.backup_role names, to the runtime role, and to no one else.

CREATE FUNCTION backup_record_event(p_action text, p_outcome text, p_replica text, p_reason text, p_details jsonb) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER AS $$
BEGIN
    IF p_action NOT IN ('system.backup.completed', 'system.backup.failed', 'system.backup.deleted', 'system.backup.target_adopted') THEN
        RAISE EXCEPTION 'backup_record_event writes backup events only, not %', p_action USING ERRCODE = '42501';
    END IF;
    IF p_outcome NOT IN ('success', 'failure') THEN
        RAISE EXCEPTION 'backup_record_event: outcome % is not success or failure', p_outcome USING ERRCODE = '42501';
    END IF;
    INSERT INTO audit_event (id, occurred_at, action, category, outcome, actor_type, actor_name, replica, reason, details)
    VALUES (gen_random_uuid(), now(), p_action, 'system', p_outcome, 'system', 'backup', NULLIF(p_replica, ''), NULLIF(p_reason, ''),
            COALESCE(p_details, '{}'::jsonb));
END $$;

DO $$
BEGIN
    EXECUTE format('ALTER FUNCTION %I.backup_record_event(text, text, text, text, jsonb) SET search_path = %I, pg_temp',
                   current_schema(), current_schema());
    EXECUTE format('REVOKE ALL ON FUNCTION %I.backup_record_event(text, text, text, text, jsonb) FROM PUBLIC', current_schema());
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'trove_app') THEN
        EXECUTE format('GRANT EXECUTE ON FUNCTION %I.backup_record_event(text, text, text, text, jsonb) TO trove_app', current_schema());
    END IF;
END $$;
