-- trove:phase expand
-- trove:no-transaction
--
-- Projects (R-AUTHZ-026), expand class (R-HA-018): the unique index behind the constraint
-- authz_policy_id_project of 00024, built without blocking writes. The constraint is
-- attached with ADD CONSTRAINT ... USING INDEX, which needs no scan.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS authz_policy_id_project ON authz_policy (id, project_id);
