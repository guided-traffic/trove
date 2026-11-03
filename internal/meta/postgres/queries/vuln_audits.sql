-- Statements of the vulnerability scanning, work package audits (docs/developer/vulnerabilities.md). One file per work package;
-- every statement of this file is the package's. Plain Go types only (no pgtype): COALESCE nullable columns.

-- The client audits read the non-withdrawn advisories with their affected entries, one row per affected entry. Times are
-- epoch microseconds (0 when absent) and the score a float, so that no driver type leaves the store.

-- name: AuditAdvisoriesForNames :many
-- The advisories of an ecosystem that name any of the (normalized) names (R-VULN-017): one query for the whole body, over
-- the index vuln_affected_name. At most max_rows rows: the caller asks for one more than it accepts and refuses a body that
-- names more.
SELECT adv.id, adv.canonical_id,
       (EXTRACT(EPOCH FROM adv.modified) * 1000000)::bigint AS modified_us,
       COALESCE((EXTRACT(EPOCH FROM adv.published) * 1000000)::bigint, 0)::bigint AS published_us,
       adv.kind, adv.aliases, adv.summary, adv.severity, adv.severity_source,
       COALESCE(adv.cvss_version, '')::text AS cvss_version, COALESCE(adv.cvss_vector, '')::text AS cvss_vector,
       COALESCE(adv.cvss_score, 0)::float8 AS cvss_score, adv.known_exploited, adv.cwe_ids,
       af.ord, af.name, af.ranges, af.versions, af.every_version, af.fixed
FROM vuln_affected af
JOIN vuln_advisory adv ON adv.ecosystem = af.ecosystem AND adv.id = af.advisory_id
WHERE af.ecosystem = @ecosystem::text AND af.name = ANY(@names::text[]) AND adv.withdrawn IS NULL
ORDER BY af.name, adv.canonical_id, adv.id, af.ord
LIMIT @max_rows::int;

-- name: AuditAllAdvisories :many
-- Every non-withdrawn advisory of an ecosystem (the NuGet page, R-VULN-018).
SELECT adv.id, adv.canonical_id,
       (EXTRACT(EPOCH FROM adv.modified) * 1000000)::bigint AS modified_us,
       COALESCE((EXTRACT(EPOCH FROM adv.published) * 1000000)::bigint, 0)::bigint AS published_us,
       adv.kind, adv.aliases, adv.summary, adv.severity, adv.severity_source,
       COALESCE(adv.cvss_version, '')::text AS cvss_version, COALESCE(adv.cvss_vector, '')::text AS cvss_vector,
       COALESCE(adv.cvss_score, 0)::float8 AS cvss_score, adv.known_exploited, adv.cwe_ids,
       af.ord, af.name, af.ranges, af.versions, af.every_version, af.fixed
FROM vuln_affected af
JOIN vuln_advisory adv ON adv.ecosystem = af.ecosystem AND adv.id = af.advisory_id
WHERE af.ecosystem = @ecosystem::text AND adv.withdrawn IS NULL
ORDER BY af.name, adv.canonical_id, adv.id, af.ord;
