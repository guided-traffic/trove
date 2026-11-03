-- trove:phase expand
-- trove:no-transaction
--
-- GitLab CI OIDC (R-CI-020, R-HA-018, Q-CI-53): the index behind the issuance list without an issuer or a project
-- filter, which orders all records by (issued_at, token_id); ci_issuances_project leads with issuer_id and project_id
-- and cannot serve it. Built without blocking the exchange's inserts.
CREATE INDEX CONCURRENTLY IF NOT EXISTS ci_issuances_issued ON ci_token_issuances (issued_at DESC, token_id DESC);
