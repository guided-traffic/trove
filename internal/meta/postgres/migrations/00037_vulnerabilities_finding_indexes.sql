-- trove:phase expand
--
-- Vulnerability scanning (R-VULN-011, R-VULN-014, Q-VULN-64): the indexes the findings' lookups need and 00035 did not give them.
-- The two foreign keys of vuln_finding that are ON DELETE SET NULL (package_id, version_id) are looked up by every delete of a
-- package or a version, and by the removal's resolution; the retention task orders the resolved findings by resolved_at. One
-- transactional step, because the numbering gives the fix wave one file: vuln_finding is a table of the same release as 00035
-- and is written by the scanning only, so the short lock these builds take blocks nothing a replica needs at the same time.
CREATE INDEX IF NOT EXISTS vuln_finding_version_id ON vuln_finding (version_id);
CREATE INDEX IF NOT EXISTS vuln_finding_package_id ON vuln_finding (package_id);
CREATE INDEX IF NOT EXISTS vuln_finding_resolved_at ON vuln_finding (resolved_at) WHERE status = 'resolved';
