-- trove:phase expand
--
-- Vulnerability scanning (R-VULN-020, Q-VULN-68): the indexes behind the keyset of the findings list and the export. The
-- list reads one page in the order of its sort key (docs/developer/vulnerabilities.md, section E); the statement orders by
-- exactly these expressions, so that a page reads the first rows of an index in order and stops, instead of sorting every
-- finding in the caller's scope. detectedAt is served by vuln_finding_detected (detected_at, id) of 00035.
--
-- vuln_finding is new in this release and empty when this step runs, so the indexes are built in the step's transaction;
-- a table that held rows would take them CONCURRENTLY, one statement per step (00036). vuln_finding_severity and
-- vuln_finding_package of 00035 are no longer read by the list; an expand step cannot drop an index (R-HA-018), so they
-- stay until a contract step removes them.

CREATE INDEX vuln_finding_severity_key ON vuln_finding (((severity_rank::bigint * 1000 + (COALESCE(cvss_score, 0) * 10)::bigint)), id);
CREATE INDEX vuln_finding_package_key ON vuln_finding ((((package_name || chr(31) || version) COLLATE "C")), id);
CREATE INDEX vuln_finding_advisory_key ON vuln_finding ((advisory_id COLLATE "C"), id);
CREATE INDEX vuln_finding_status_key ON vuln_finding (((CASE status WHEN 'needs_triage' THEN 0 WHEN 'confirmed' THEN 1 WHEN 'blocked' THEN 2 WHEN 'dismissed' THEN 3 ELSE 4 END)::bigint), id);
