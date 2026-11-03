-- Statements of the vulnerability scanning, work package ops (docs/developer/vulnerabilities.md). One file per work package;
-- every statement of this file is the package's. Plain Go types only (no pgtype): COALESCE nullable columns.

-- name: VulnOpsSourceStates :many
-- The source state of every source and ecosystem as the database gauges and the vulnerabilityData facts read it
-- (R-VULN-024): the last run that ended changed or unchanged as epoch seconds (0 for a source that never had one), the
-- record count, the consecutive failures, the state and the time of the last attempt. KEV rows have the ecosystem ''.
SELECT source, ecosystem, state,
       COALESCE(extract(epoch FROM synced_at), 0)::float8 AS synced_epoch,
       (synced_at IS NOT NULL)::boolean AS has_synced,
       records::bigint AS records,
       failures::bigint AS failures
  FROM vuln_source_state
 ORDER BY source, ecosystem;

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
