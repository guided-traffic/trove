-- trove:phase expand
-- trove:no-transaction
--
-- GitLab CI OIDC (R-CI-020, R-HA-018, Q-CI-26, Q-CI-53): the index behind a project's issuance list, which finds the
-- records whose frozen bundle has a contribution of the project with a jsonb containment test on
-- grants -> 'contributions' (CiIssuanceListForProject); the expression is the one the query writes.
CREATE INDEX CONCURRENTLY IF NOT EXISTS ci_issuances_contributions ON ci_token_issuances USING gin ((grants -> 'contributions') jsonb_path_ops);
