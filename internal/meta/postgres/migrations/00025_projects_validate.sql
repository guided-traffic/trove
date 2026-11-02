-- trove:phase expand
--
-- Projects (R-AUTHZ-026), expand class (R-HA-018): the foreign keys that 00019 added NOT
-- VALID are validated here, under a lock that lets reads and writes through.

ALTER TABLE repositories VALIDATE CONSTRAINT repositories_project_id_fkey;
ALTER TABLE service_account VALIDATE CONSTRAINT service_account_project_id_fkey;
ALTER TABLE authz_policy VALIDATE CONSTRAINT authz_policy_project_id_fkey;
