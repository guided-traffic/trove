-- trove:phase expand
--
-- GitLab CI OIDC (R-AUTHZ-018, R-HA-018): the foreign key that 00026 added NOT VALID is validated
-- here, under a lock that lets reads and writes through.
ALTER TABLE package_owner VALIDATE CONSTRAINT package_owner_ci_issuer_id_fkey;
