-- trove:phase expand
--
-- A write that names a repository by ID locks it FOR SHARE in the database (R-REPO-060, Q-REPO-234, F-68). The
-- same-project guard of a member row (AI006, 00024) read the project of the repository before the foreign key
-- of the row took its FOR KEY SHARE lock, so a move of the repository that was in flight (uncommitted) was not
-- seen: the guard passed on the old project, the foreign key then waited for the move, and the row was written
-- next to a repository of another project. The guard now locks the member repository and the virtual repository
-- FOR SHARE first and reads their projects after the lock, so a move in flight makes the write wait and then
-- refuse it (AI006), and a write in flight makes the move wait and then refuse itself (AI007). Both orders end
-- in one state whatever the Go path locked. The function serves virtual_members, virtual_pin_members and
-- virtual_deploy_targets; the two rows are locked in the order of their IDs so that two writers cannot wait on
-- each other.
--
-- The same order held for the key selector of a project policy (AI007, 00024): policy_selector_project_guard read the
-- project of the repository before the foreign key waited for a move in flight. It locks the repository FOR SHARE
-- first and reads the project after the lock; the project of a policy never changes (AI003), so its row needs no
-- lock.
CREATE OR REPLACE FUNCTION virtual_same_project_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    other uuid;
    own   uuid;
BEGIN
    PERFORM 1 FROM repositories
     WHERE id IN ((to_jsonb(NEW) ->> TG_ARGV[0])::bigint, (to_jsonb(NEW) ->> TG_ARGV[1])::bigint)
     ORDER BY id FOR SHARE;
    SELECT project_id INTO other FROM repositories WHERE id = (to_jsonb(NEW) ->> TG_ARGV[0])::bigint;
    SELECT project_id INTO own FROM repositories WHERE id = (to_jsonb(NEW) ->> TG_ARGV[1])::bigint;
    IF other IS DISTINCT FROM own THEN
        RAISE EXCEPTION '%.% must name a repository of the project of %.%',
              TG_TABLE_NAME, TG_ARGV[0], TG_TABLE_NAME, TG_ARGV[1] USING ERRCODE = 'AI006';
    END IF;
    RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION policy_selector_project_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    own   uuid;
    other uuid;
BEGIN
    IF NEW.repository_id IS NULL THEN
        RETURN NEW;
    END IF;
    SELECT project_id INTO own FROM authz_policy WHERE id = NEW.policy_id;
    IF own IS NULL THEN
        RETURN NEW;
    END IF;
    SELECT project_id INTO other FROM repositories WHERE id = NEW.repository_id FOR SHARE;
    IF other IS DISTINCT FROM own THEN
        RAISE EXCEPTION 'authz_policy_selector.repository_id must name a repository of the project of the policy'
              USING ERRCODE = 'AI007';
    END IF;
    RETURN NEW;
END $$;
