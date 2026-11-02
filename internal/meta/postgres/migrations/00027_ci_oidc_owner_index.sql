-- trove:phase expand
-- trove:no-transaction
--
-- GitLab CI OIDC (R-AUTHZ-018, R-HA-018): the index behind package_owner_ci_issuer_id_fkey of 00026,
-- built without blocking writes. The delete of an issuer cascades to its project owners through it.
CREATE INDEX CONCURRENTLY IF NOT EXISTS package_owner_ci_issuer ON package_owner (ci_issuer_id) WHERE ci_issuer_id IS NOT NULL;
