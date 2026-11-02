-- trove:phase expand
-- trove:no-transaction
--
-- Projects (R-AUTHZ-026), expand class (R-HA-018): the index of repositories.project_id,
-- built without blocking writes.
CREATE INDEX CONCURRENTLY IF NOT EXISTS repositories_project ON repositories (project_id) WHERE project_id IS NOT NULL;
