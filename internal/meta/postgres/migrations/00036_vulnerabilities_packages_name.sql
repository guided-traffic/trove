-- trove:phase expand
-- trove:no-transaction
--
-- Vulnerability scanning (R-VULN-011, R-HA-018, Q-VULN-24): the index behind the advisory-driven evaluation, which looks
-- stored versions up by package name across repositories. The unique key (repo_id, name) of packages does not serve it.
-- Built without blocking writes.
CREATE INDEX CONCURRENTLY IF NOT EXISTS packages_name ON packages (name);
