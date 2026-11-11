-- trove:phase expand
--
-- Visibility levels (R-AUTHZ-033, Q-AUTHZ-264 to Q-AUTHZ-266, Q-AUTHZ-276): the installation's default, the level of a project
-- and the two levels of a repository, artifacts and wiki. A level is stored on its object and never inherited when a request is
-- decided; the container rules live in the database as well: a repository's artifacts are never more open than its project
-- (a deferred constraint trigger, SQLSTATE AI008) and its wiki never more open than its artifacts (a check constraint).
--
-- Every existing project and repository is private after the step, the most restrictive level, so no content opens by it.

CREATE FUNCTION visibility_rank(level text) RETURNS integer
    LANGUAGE sql IMMUTABLE STRICT
    AS $$ SELECT CASE level WHEN 'private' THEN 0 WHEN 'internal' THEN 1 WHEN 'public' THEN 2 END $$;

ALTER TABLE project ADD COLUMN visibility text NOT NULL DEFAULT 'private'
    CHECK (visibility IN ('private', 'internal', 'public'));

ALTER TABLE repositories
    ADD COLUMN visibility_artifacts text NOT NULL DEFAULT 'private'
        CHECK (visibility_artifacts IN ('private', 'internal', 'public')),
    ADD COLUMN visibility_wiki      text NOT NULL DEFAULT 'private'
        CHECK (visibility_wiki IN ('private', 'internal', 'public')),
    ADD CONSTRAINT repositories_wiki_within_artifacts
        CHECK (visibility_rank(visibility_wiki) <= visibility_rank(visibility_artifacts));

-- A repository's artifacts level lies at most at its project's level. The rule is checked at commit so that one transaction
-- may lower a project and its repositories in either order (the cascade of R-AUTHZ-033 rule 3, a move, a restore). The
-- function reads the repositories of the project the changed row names: the project itself, or the project of the
-- changed repository.
CREATE FUNCTION visibility_within_project() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    pid uuid;
    bad text;
BEGIN
    IF TG_TABLE_NAME = 'project' THEN
        pid := NEW.id;
    ELSE
        pid := NEW.project_id;
    END IF;
    SELECT r.key INTO bad
      FROM repositories r
      JOIN project p ON p.id = r.project_id
     WHERE p.id = pid AND visibility_rank(r.visibility_artifacts) > visibility_rank(p.visibility)
     LIMIT 1;
    IF bad IS NOT NULL THEN
        RAISE EXCEPTION 'the artifacts of repository % are more open than their project', bad
              USING ERRCODE = 'AI008';
    END IF;
    RETURN NULL;
END $$;

CREATE CONSTRAINT TRIGGER repositories_within_project
    AFTER INSERT OR UPDATE OF visibility_artifacts, project_id ON repositories
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION visibility_within_project();
CREATE CONSTRAINT TRIGGER project_visibility_within
    AFTER UPDATE OF visibility ON project
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION visibility_within_project();

-- The installation's default level (R-API-056): read in the transaction that creates a project or a repository. One row,
-- present from the start so that a read always finds it.
CREATE TABLE visibility_settings (
    singleton      boolean     PRIMARY KEY DEFAULT true CHECK (singleton),
    default_level  text        NOT NULL DEFAULT 'private' CHECK (default_level IN ('private', 'internal', 'public')),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    updated_by     uuid        REFERENCES principal (id) ON DELETE SET NULL
);
INSERT INTO visibility_settings (singleton) VALUES (true);

-- The repository document loses requireAuthentication (R-REPO-004): a level replaces it.
UPDATE repositories SET config = config - 'requireAuthentication' WHERE config ? 'requireAuthentication';
