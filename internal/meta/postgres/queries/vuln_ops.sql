-- Statements of the vulnerability scanning, work package ops (docs/developer/vulnerabilities.md). One file per work package;
-- every statement of this file is the package's. Plain Go types only (no pgtype): COALESCE nullable columns.

-- name: VulnOpsFindingCounts :many
-- trove_vuln_findings (R-VULN-024): the findings by severity, status and kind.
SELECT severity, status, kind, count(*)::bigint AS n
  FROM vuln_finding
 GROUP BY severity, status, kind;

-- name: VulnOpsClaimedMatchCounts :many
-- trove_vuln_claimed_name_matches (R-VULN-024): the public advisories for claimed names, by ecosystem and kind.
SELECT ecosystem, kind, count(*)::bigint AS n
  FROM vuln_claimed_match
 GROUP BY ecosystem, kind;
