-- trove:phase expand
-- trove:no-transaction
--
-- GitLab CI OIDC (R-CI-020, R-HA-018, Q-CI-53): the index behind the issuance list with only an issuer filter, which
-- walks one issuer's records newest first and stops at the page size.
CREATE INDEX CONCURRENTLY IF NOT EXISTS ci_issuances_issuer_issued ON ci_token_issuances (issuer_id, issued_at DESC, token_id DESC);
