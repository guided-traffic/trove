-- trove:phase expand
-- trove:no-transaction
--
-- Projects (R-AUTHZ-026, Q-REPO-212), expand class (R-HA-018): the unique index behind the constraint
-- repositories_project_key of 00024 (a repository key is unique within its project), built without
-- blocking writes. The constraint is attached with ADD CONSTRAINT ... USING INDEX, which needs no
-- scan. The index also serves every lookup of a project's repositories by project_id.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS repositories_project_key ON repositories (project_id, key);
