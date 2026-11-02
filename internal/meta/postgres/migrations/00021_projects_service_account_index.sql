-- trove:phase expand
-- trove:no-transaction
--
-- Projects (R-AUTHZ-026), expand class (R-HA-018): the index of service_account.project_id,
-- built without blocking writes.
CREATE INDEX CONCURRENTLY IF NOT EXISTS service_account_project ON service_account (project_id) WHERE project_id IS NOT NULL;
