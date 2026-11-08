-- Statements of the vulnerability scanning, work package api (docs/developer/vulnerabilities.md). One file per work package;
-- every statement of this file is the package's. Plain Go types only (no pgtype): COALESCE nullable columns.
--
-- The caller's right is passed in as data, as in package_view.sql (R-VULN-019):
--   whole   IDs of the repositories whose every finding the caller may read (authz.ReadAll);
--   rows    one entry "<repository id>\x1f<package name>\x1f<version>" per version the caller may read in the other
--           repositories, decided by the decision point row by row (authz.ReadRows);
--   package_rows
--           one entry "<repository id>\x1f<package name>" per package the caller may read in the other repositories whose
--           decision does not depend on the version (authz.VersionIndependent, Q-VULN-81): every version of it is readable.
-- A repository in neither list contributes no row. The filters narrow what a request asks about and never widen it.
-- Repositories travel as their numeric IDs (f.repo_id = ANY(...)), never as the qualified name the statements return: a
-- predicate on (project key || '/' || repository key) cannot use the index of vuln_finding.repo_id and is evaluated for every
-- finding of the join (measured at 100,000 findings in docs/developer/vulnerabilities.md).
-- The policy of the repositories (R-VULN-015) is passed in as data too: enabled_repos are the IDs whose scanning is on, and
-- malicious_repos those that also block the malicious findings; the block of a finding is computed by the one rule of
-- vuln.Policy.BlockReasonOf, which the Go side applies to every row it returns (vuln.Verdict) and these statements mirror
-- only to filter and count.

-- name: VulnScopeCandidates :many
-- The (repository, package, version) triple of every finding of the given repositories, for the repositories whose rows the
-- decision point decides one by one; the request's filters narrow them. A triple repeats for each finding of the version, and
-- the caller decides it once: sorting the triples apart in SQL would cost more than the repeat. At most max_rows findings; the
-- caller asks for one more than it accepts.
SELECT f.repo_id, f.package_name AS name, f.version AS version
  FROM vuln_finding f
  JOIN repositories r ON r.id = f.repo_id
 WHERE f.repo_id = ANY(sqlc.arg(keys)::bigint[])
   AND (cardinality(sqlc.arg(formats)::text[]) = 0 OR r.format = ANY(sqlc.arg(formats)::text[]))
   AND (cardinality(sqlc.arg(packages)::text[]) = 0 OR f.package_name = ANY(sqlc.arg(packages)::text[])
          OR (f.ecosystem = 'NuGet' AND f.package_name = ANY(SELECT lower(x) FROM unnest(sqlc.arg(packages)::text[]) AS x)))
   AND (sqlc.arg(package_prefix)::text = '' OR starts_with(f.package_name, lower(sqlc.arg(package_prefix)::text)))
   AND (cardinality(sqlc.arg(versions)::text[]) = 0 OR f.version = ANY(sqlc.arg(versions)::text[]))
   AND (cardinality(sqlc.arg(ids)::text[]) = 0 OR f.id::text = ANY(sqlc.arg(ids)::text[]))
   AND ((cardinality(sqlc.arg(statuses)::text[]) = 0 AND f.status <> 'resolved') OR f.status = ANY(sqlc.arg(statuses)::text[]))
 LIMIT sqlc.arg(max_rows)::int;

-- name: VulnScopePackages :many
-- The (repository, package) pair of every finding of the given repositories, for the scope of a caller whose decision does not
-- depend on the version (authz.VersionIndependent): the decision point decides a package once, and the caller removes the pairs
-- that repeat for each version and each finding. The filters are those of VulnScopeCandidates and only narrow what is decided; a
-- pair whose findings the list's own filters all exclude is decided for nothing. The statement does not group: with many distinct
-- packages the planner sorts to group, and that costs more than the repeat it removes (Q-VULN-81). At most max_rows findings; the
-- caller asks for one more than it accepts.
SELECT f.repo_id, f.package_name AS name
  FROM vuln_finding f
  JOIN repositories r ON r.id = f.repo_id
 WHERE f.repo_id = ANY(sqlc.arg(keys)::bigint[])
   AND (cardinality(sqlc.arg(formats)::text[]) = 0 OR r.format = ANY(sqlc.arg(formats)::text[]))
   AND (cardinality(sqlc.arg(packages)::text[]) = 0 OR f.package_name = ANY(sqlc.arg(packages)::text[])
          OR (f.ecosystem = 'NuGet' AND f.package_name = ANY(SELECT lower(x) FROM unnest(sqlc.arg(packages)::text[]) AS x)))
   AND (sqlc.arg(package_prefix)::text = '' OR starts_with(f.package_name, lower(sqlc.arg(package_prefix)::text)))
   AND (cardinality(sqlc.arg(versions)::text[]) = 0 OR f.version = ANY(sqlc.arg(versions)::text[]))
   AND (cardinality(sqlc.arg(ids)::text[]) = 0 OR f.id::text = ANY(sqlc.arg(ids)::text[]))
   AND ((cardinality(sqlc.arg(statuses)::text[]) = 0 AND f.status <> 'resolved') OR f.status = ANY(sqlc.arg(statuses)::text[]))
 LIMIT sqlc.arg(max_rows)::int;

-- name: VulnPackageCandidates :many
-- The (repository, package, version) triples of one package in the given repositories, for the package views (the version
-- report names every readable repository that holds the version, with or without a finding).
SELECT p.repo_id, p.name, pv.version
  FROM package_versions pv
  JOIN packages p ON p.id = pv.package_id
  JOIN repositories r ON r.id = p.repo_id
 WHERE pv.trashed_at IS NULL
   AND p.repo_id = ANY(sqlc.arg(keys)::bigint[])
   AND r.format = sqlc.arg(format)::text
   AND p.name = sqlc.arg(name)::text
   AND (sqlc.arg(version)::text = '' OR pv.version = sqlc.arg(version)::text)
 ORDER BY p.repo_id, pv.version
 LIMIT sqlc.arg(max_rows)::int;

-- name: VulnFindingsList :many
-- One page of the findings the caller can read, in keyset order. sort is severity, detectedAt, package, advisory or status;
-- the sort key is (k_num, k_txt, id), compared bytewise, so a cursor means the same on every replica. Each sort and direction
-- is a branch that orders by a stored column or by the expression of an index of 00039 and compares the cursor against the
-- same expression, so a page reads the first rows of one index in order and stops (Q-VULN-68); a branch whose sort is not
-- asked does not run. The advisory's columns are looked up for the rows of the page only.
WITH base AS NOT MATERIALIZED (
  SELECT f.id, f.repo_id, (rp.key || '/' || r.key)::text AS repo_key, r.format, f.ecosystem, f.package_name, f.version, f.advisory_id, f.kind, f.severity,
         f.severity_rank, f.cvss_score AS cvss_raw, COALESCE(f.cvss_score, -1)::float8 AS cvss_score, f.known_exploited, f.fix_available,
         f.fixed_versions, f.status, COALESCE(f.dismissal_reason, '')::text AS dismissal_reason,
         COALESCE(f.resolved_reason, '')::text AS resolved_reason, COALESCE(f.comment, '')::text AS comment,
         f.revision, f.detected_at, f.resolved_at, f.status_changed_at, f.status_changed_by
    FROM vuln_finding f
    JOIN repositories r ON r.id = f.repo_id
    JOIN project rp ON rp.id = r.project_id
   WHERE (f.repo_id = ANY(sqlc.arg(whole)::bigint[])
          OR concat_ws(chr(31), f.repo_id::text, f.package_name, f.version) = ANY(sqlc.arg(rows)::text[])
          OR concat_ws(chr(31), f.repo_id::text, f.package_name) = ANY(sqlc.arg(package_rows)::text[]))
     AND (NOT sqlc.arg(has_repos)::boolean OR f.repo_id = ANY(sqlc.arg(repos)::bigint[]))
     AND (cardinality(sqlc.arg(formats)::text[]) = 0 OR r.format = ANY(sqlc.arg(formats)::text[]))
     AND (cardinality(sqlc.arg(packages)::text[]) = 0 OR f.package_name = ANY(sqlc.arg(packages)::text[])
          OR (f.ecosystem = 'NuGet' AND f.package_name = ANY(SELECT lower(x) FROM unnest(sqlc.arg(packages)::text[]) AS x)))
     AND (sqlc.arg(package_prefix)::text = '' OR starts_with(f.package_name, lower(sqlc.arg(package_prefix)::text)))
     AND (cardinality(sqlc.arg(versions)::text[]) = 0 OR f.version = ANY(sqlc.arg(versions)::text[]))
     AND (sqlc.arg(ecosystem)::text = '' OR f.ecosystem = sqlc.arg(ecosystem)::text)
     AND (sqlc.arg(advisory)::text = '' OR (f.ecosystem, f.advisory_id) IN (
          SELECT a.ecosystem, a.canonical_id FROM vuln_advisory a
           WHERE a.id = sqlc.arg(advisory)::text OR a.aliases @> ARRAY[sqlc.arg(advisory)::text]
          UNION
          SELECT v.ecosystem, v.advisory_id FROM vuln_finding v
           WHERE v.advisory_id = sqlc.arg(advisory)::text
             AND NOT EXISTS (SELECT 1 FROM vuln_advisory n WHERE n.id = sqlc.arg(advisory)::text OR n.aliases @> ARRAY[sqlc.arg(advisory)::text])))
     AND (cardinality(sqlc.arg(severities)::text[]) = 0 OR f.severity = ANY(sqlc.arg(severities)::text[]))
     AND ((cardinality(sqlc.arg(statuses)::text[]) = 0 AND f.status <> 'resolved') OR f.status = ANY(sqlc.arg(statuses)::text[]))
     AND (sqlc.arg(kind)::text = '' OR f.kind = sqlc.arg(kind)::text)
     AND (NOT sqlc.arg(has_known_exploited)::boolean OR f.known_exploited = sqlc.arg(known_exploited)::boolean)
     AND (NOT sqlc.arg(has_fix_available)::boolean OR f.fix_available = sqlc.arg(fix_available)::boolean)
     AND (NOT sqlc.arg(has_blocked)::boolean OR ((CASE WHEN f.status IN ('needs_triage', 'confirmed', 'blocked') AND f.repo_id = ANY(sqlc.arg(enabled_repos)::bigint[])
          THEN CASE WHEN f.kind = 'malicious' AND f.repo_id = ANY(sqlc.arg(malicious_repos)::bigint[]) THEN 'malicious'
                    WHEN f.status = 'blocked' THEN 'triage' END
     END) IS NOT NULL) = sqlc.arg(blocked)::boolean)
     AND (sqlc.arg(block_reason)::text = '' OR (CASE WHEN f.status IN ('needs_triage', 'confirmed', 'blocked') AND f.repo_id = ANY(sqlc.arg(enabled_repos)::bigint[])
          THEN CASE WHEN f.kind = 'malicious' AND f.repo_id = ANY(sqlc.arg(malicious_repos)::bigint[]) THEN 'malicious'
                    WHEN f.status = 'blocked' THEN 'triage' END
     END) = sqlc.arg(block_reason)::text)
     AND (NOT sqlc.arg(has_from)::boolean OR f.detected_at >= NULLIF(sqlc.arg(detected_from)::text, '')::timestamptz)
     AND (NOT sqlc.arg(has_to)::boolean OR f.detected_at < NULLIF(sqlc.arg(detected_to)::text, '')::timestamptz)
     AND (cardinality(sqlc.arg(ids)::text[]) = 0 OR f.id::text = ANY(sqlc.arg(ids)::text[]))
     AND (sqlc.arg(q)::text = ''
          OR position(lower(sqlc.arg(q)::text) in f.package_name) > 0
          OR position(lower(sqlc.arg(q)::text) in lower(f.advisory_id)) > 0
          OR EXISTS (SELECT 1 FROM vuln_advisory a
                      WHERE a.ecosystem = f.ecosystem AND a.canonical_id = f.advisory_id
                        AND (position(lower(sqlc.arg(q)::text) in lower(a.summary)) > 0
                             OR position(lower(sqlc.arg(q)::text) in lower(a.id)) > 0
                             OR EXISTS (SELECT 1 FROM unnest(a.aliases) al WHERE position(lower(sqlc.arg(q)::text) in lower(al)) > 0))))
), p AS (
  (SELECT b.*, (b.severity_rank::bigint * 1000 + (COALESCE(b.cvss_raw, 0) * 10)::bigint)::bigint AS k_num, ''::text AS k_txt FROM base b
    WHERE sqlc.arg(sort)::text = 'severity' AND NOT sqlc.arg(descending)::boolean
      AND (NOT sqlc.arg(has_after)::boolean OR ((b.severity_rank::bigint * 1000 + (COALESCE(b.cvss_raw, 0) * 10)::bigint), b.id) > (sqlc.arg(after_num)::bigint, sqlc.arg(after_id)::text::uuid))
    ORDER BY (b.severity_rank::bigint * 1000 + (COALESCE(b.cvss_raw, 0) * 10)::bigint) ASC, b.id ASC LIMIT sqlc.arg(max_rows)::int)
  UNION ALL
  (SELECT b.*, (extract(epoch FROM b.detected_at) * 1000000)::bigint AS k_num, ''::text AS k_txt FROM base b
    WHERE sqlc.arg(sort)::text = 'detectedAt' AND NOT sqlc.arg(descending)::boolean
      AND (NOT sqlc.arg(has_after)::boolean OR (b.detected_at, b.id) > (('epoch'::timestamptz + sqlc.arg(after_num)::bigint * interval '1 microsecond'), sqlc.arg(after_id)::text::uuid))
    ORDER BY b.detected_at ASC, b.id ASC LIMIT sqlc.arg(max_rows)::int)
  UNION ALL
  (SELECT b.*, ((CASE b.status WHEN 'needs_triage' THEN 0 WHEN 'confirmed' THEN 1 WHEN 'blocked' THEN 2 WHEN 'dismissed' THEN 3 ELSE 4 END)::bigint) AS k_num, ''::text AS k_txt FROM base b
    WHERE sqlc.arg(sort)::text = 'status' AND NOT sqlc.arg(descending)::boolean
      AND (NOT sqlc.arg(has_after)::boolean OR (((CASE b.status WHEN 'needs_triage' THEN 0 WHEN 'confirmed' THEN 1 WHEN 'blocked' THEN 2 WHEN 'dismissed' THEN 3 ELSE 4 END)::bigint), b.id) > (sqlc.arg(after_num)::bigint, sqlc.arg(after_id)::text::uuid))
    ORDER BY ((CASE b.status WHEN 'needs_triage' THEN 0 WHEN 'confirmed' THEN 1 WHEN 'blocked' THEN 2 WHEN 'dismissed' THEN 3 ELSE 4 END)::bigint) ASC, b.id ASC LIMIT sqlc.arg(max_rows)::int)
  UNION ALL
  (SELECT b.*, 0::bigint AS k_num, ((b.package_name || chr(31) || b.version) COLLATE "C")::text AS k_txt FROM base b
    WHERE sqlc.arg(sort)::text = 'package' AND NOT sqlc.arg(descending)::boolean
      AND (NOT sqlc.arg(has_after)::boolean OR (((b.package_name || chr(31) || b.version) COLLATE "C"), b.id) > (sqlc.arg(after_txt)::text COLLATE "C", sqlc.arg(after_id)::text::uuid))
    ORDER BY ((b.package_name || chr(31) || b.version) COLLATE "C") ASC, b.id ASC LIMIT sqlc.arg(max_rows)::int)
  UNION ALL
  (SELECT b.*, 0::bigint AS k_num, (b.advisory_id COLLATE "C")::text AS k_txt FROM base b
    WHERE sqlc.arg(sort)::text = 'advisory' AND NOT sqlc.arg(descending)::boolean
      AND (NOT sqlc.arg(has_after)::boolean OR ((b.advisory_id COLLATE "C"), b.id) > (sqlc.arg(after_txt)::text COLLATE "C", sqlc.arg(after_id)::text::uuid))
    ORDER BY (b.advisory_id COLLATE "C") ASC, b.id ASC LIMIT sqlc.arg(max_rows)::int)
  UNION ALL
  (SELECT b.*, (b.severity_rank::bigint * 1000 + (COALESCE(b.cvss_raw, 0) * 10)::bigint)::bigint AS k_num, ''::text AS k_txt FROM base b
    WHERE sqlc.arg(sort)::text = 'severity' AND sqlc.arg(descending)::boolean
      AND (NOT sqlc.arg(has_after)::boolean OR ((b.severity_rank::bigint * 1000 + (COALESCE(b.cvss_raw, 0) * 10)::bigint), b.id) < (sqlc.arg(after_num)::bigint, sqlc.arg(after_id)::text::uuid))
    ORDER BY (b.severity_rank::bigint * 1000 + (COALESCE(b.cvss_raw, 0) * 10)::bigint) DESC, b.id DESC LIMIT sqlc.arg(max_rows)::int)
  UNION ALL
  (SELECT b.*, (extract(epoch FROM b.detected_at) * 1000000)::bigint AS k_num, ''::text AS k_txt FROM base b
    WHERE sqlc.arg(sort)::text = 'detectedAt' AND sqlc.arg(descending)::boolean
      AND (NOT sqlc.arg(has_after)::boolean OR (b.detected_at, b.id) < (('epoch'::timestamptz + sqlc.arg(after_num)::bigint * interval '1 microsecond'), sqlc.arg(after_id)::text::uuid))
    ORDER BY b.detected_at DESC, b.id DESC LIMIT sqlc.arg(max_rows)::int)
  UNION ALL
  (SELECT b.*, ((CASE b.status WHEN 'needs_triage' THEN 0 WHEN 'confirmed' THEN 1 WHEN 'blocked' THEN 2 WHEN 'dismissed' THEN 3 ELSE 4 END)::bigint) AS k_num, ''::text AS k_txt FROM base b
    WHERE sqlc.arg(sort)::text = 'status' AND sqlc.arg(descending)::boolean
      AND (NOT sqlc.arg(has_after)::boolean OR (((CASE b.status WHEN 'needs_triage' THEN 0 WHEN 'confirmed' THEN 1 WHEN 'blocked' THEN 2 WHEN 'dismissed' THEN 3 ELSE 4 END)::bigint), b.id) < (sqlc.arg(after_num)::bigint, sqlc.arg(after_id)::text::uuid))
    ORDER BY ((CASE b.status WHEN 'needs_triage' THEN 0 WHEN 'confirmed' THEN 1 WHEN 'blocked' THEN 2 WHEN 'dismissed' THEN 3 ELSE 4 END)::bigint) DESC, b.id DESC LIMIT sqlc.arg(max_rows)::int)
  UNION ALL
  (SELECT b.*, 0::bigint AS k_num, ((b.package_name || chr(31) || b.version) COLLATE "C")::text AS k_txt FROM base b
    WHERE sqlc.arg(sort)::text = 'package' AND sqlc.arg(descending)::boolean
      AND (NOT sqlc.arg(has_after)::boolean OR (((b.package_name || chr(31) || b.version) COLLATE "C"), b.id) < (sqlc.arg(after_txt)::text COLLATE "C", sqlc.arg(after_id)::text::uuid))
    ORDER BY ((b.package_name || chr(31) || b.version) COLLATE "C") DESC, b.id DESC LIMIT sqlc.arg(max_rows)::int)
  UNION ALL
  (SELECT b.*, 0::bigint AS k_num, (b.advisory_id COLLATE "C")::text AS k_txt FROM base b
    WHERE sqlc.arg(sort)::text = 'advisory' AND sqlc.arg(descending)::boolean
      AND (NOT sqlc.arg(has_after)::boolean OR ((b.advisory_id COLLATE "C"), b.id) < (sqlc.arg(after_txt)::text COLLATE "C", sqlc.arg(after_id)::text::uuid))
    ORDER BY (b.advisory_id COLLATE "C") DESC, b.id DESC LIMIT sqlc.arg(max_rows)::int)
  UNION ALL
  (SELECT b.*, 0::bigint AS k_num, ''::text AS k_txt FROM base b
    WHERE sqlc.arg(sort)::text NOT IN ('severity', 'detectedAt', 'status', 'package', 'advisory')
      AND (NOT sqlc.arg(has_after)::boolean OR b.id > sqlc.arg(after_id)::text::uuid)
    ORDER BY b.id ASC LIMIT sqlc.arg(max_rows)::int)
)
SELECT p.id::text AS id, p.repo_id, p.repo_key, p.format, p.ecosystem, p.package_name, p.version, p.advisory_id, p.kind, p.severity,
       p.cvss_score, p.known_exploited, p.fix_available, p.fixed_versions, p.status, p.dismissal_reason, p.resolved_reason,
       p.comment, p.revision, p.detected_at, p.resolved_at, p.status_changed_at,
       COALESCE(p.status_changed_by::text, '')::text AS status_changed_by, COALESCE(pr.name, '')::text AS status_changed_by_name,
       p.k_num, p.k_txt,
       COALESCE(adv.summary, '')::text AS summary, COALESCE(adv.cvss_version, '')::text AS cvss_version,
       COALESCE(adv.cvss_vector, '')::text AS cvss_vector, COALESCE(adv.kev_added, '')::text AS kev_added,
       COALESCE(al.aliases, '{}')::text[] AS aliases
  FROM p
  LEFT JOIN principal pr ON pr.id = p.status_changed_by
  LEFT JOIN LATERAL (
    SELECT a.summary, a.cvss_version, a.cvss_vector, COALESCE(to_char(a.kev_date_added, 'YYYY-MM-DD'), '') AS kev_added
      FROM vuln_advisory a
     WHERE a.ecosystem = p.ecosystem AND a.canonical_id = p.advisory_id
     ORDER BY (a.id = p.advisory_id) DESC, a.modified DESC
     LIMIT 1) adv ON true
  LEFT JOIN LATERAL (
    SELECT array_agg(DISTINCT s.x ORDER BY s.x) AS aliases
      FROM (SELECT g.id AS x FROM vuln_advisory g WHERE g.ecosystem = p.ecosystem AND g.canonical_id = p.advisory_id
            UNION
            SELECT unnest(g.aliases) FROM vuln_advisory g WHERE g.ecosystem = p.ecosystem AND g.canonical_id = p.advisory_id) s
     WHERE s.x <> p.advisory_id) al ON true
 ORDER BY
   (CASE WHEN NOT sqlc.arg(descending)::boolean THEN p.k_num END) ASC NULLS LAST,
   (CASE WHEN sqlc.arg(descending)::boolean THEN p.k_num END) DESC NULLS LAST,
   (CASE WHEN NOT sqlc.arg(descending)::boolean THEN p.k_txt END) COLLATE "C" ASC NULLS LAST,
   (CASE WHEN sqlc.arg(descending)::boolean THEN p.k_txt END) COLLATE "C" DESC NULLS LAST,
   (CASE WHEN NOT sqlc.arg(descending)::boolean THEN p.id END) ASC NULLS LAST,
   (CASE WHEN sqlc.arg(descending)::boolean THEN p.id END) DESC NULLS LAST;

-- name: VulnFindingsCount :one
-- The number of findings a filter selects, at most cap: the export refuses a result above its limit before it streams.
SELECT count(*)::bigint AS n
  FROM (SELECT 1
          FROM vuln_finding f
          JOIN repositories r ON r.id = f.repo_id
         WHERE (f.repo_id = ANY(sqlc.arg(whole)::bigint[])
          OR concat_ws(chr(31), f.repo_id::text, f.package_name, f.version) = ANY(sqlc.arg(rows)::text[])
          OR concat_ws(chr(31), f.repo_id::text, f.package_name) = ANY(sqlc.arg(package_rows)::text[]))
           AND (NOT sqlc.arg(has_repos)::boolean OR f.repo_id = ANY(sqlc.arg(repos)::bigint[]))
     AND (cardinality(sqlc.arg(formats)::text[]) = 0 OR r.format = ANY(sqlc.arg(formats)::text[]))
     AND (cardinality(sqlc.arg(packages)::text[]) = 0 OR f.package_name = ANY(sqlc.arg(packages)::text[])
          OR (f.ecosystem = 'NuGet' AND f.package_name = ANY(SELECT lower(x) FROM unnest(sqlc.arg(packages)::text[]) AS x)))
     AND (sqlc.arg(package_prefix)::text = '' OR starts_with(f.package_name, lower(sqlc.arg(package_prefix)::text)))
     AND (cardinality(sqlc.arg(versions)::text[]) = 0 OR f.version = ANY(sqlc.arg(versions)::text[]))
     AND (sqlc.arg(ecosystem)::text = '' OR f.ecosystem = sqlc.arg(ecosystem)::text)
     AND (sqlc.arg(advisory)::text = '' OR (f.ecosystem, f.advisory_id) IN (
          SELECT a.ecosystem, a.canonical_id FROM vuln_advisory a
           WHERE a.id = sqlc.arg(advisory)::text OR a.aliases @> ARRAY[sqlc.arg(advisory)::text]
          UNION
          SELECT v.ecosystem, v.advisory_id FROM vuln_finding v
           WHERE v.advisory_id = sqlc.arg(advisory)::text
             AND NOT EXISTS (SELECT 1 FROM vuln_advisory n WHERE n.id = sqlc.arg(advisory)::text OR n.aliases @> ARRAY[sqlc.arg(advisory)::text])))
     AND (cardinality(sqlc.arg(severities)::text[]) = 0 OR f.severity = ANY(sqlc.arg(severities)::text[]))
     AND ((cardinality(sqlc.arg(statuses)::text[]) = 0 AND f.status <> 'resolved') OR f.status = ANY(sqlc.arg(statuses)::text[]))
     AND (sqlc.arg(kind)::text = '' OR f.kind = sqlc.arg(kind)::text)
     AND (NOT sqlc.arg(has_known_exploited)::boolean OR f.known_exploited = sqlc.arg(known_exploited)::boolean)
     AND (NOT sqlc.arg(has_fix_available)::boolean OR f.fix_available = sqlc.arg(fix_available)::boolean)
     AND (NOT sqlc.arg(has_blocked)::boolean OR ((CASE WHEN f.status IN ('needs_triage', 'confirmed', 'blocked') AND f.repo_id = ANY(sqlc.arg(enabled_repos)::bigint[])
          THEN CASE WHEN f.kind = 'malicious' AND f.repo_id = ANY(sqlc.arg(malicious_repos)::bigint[]) THEN 'malicious'
                    WHEN f.status = 'blocked' THEN 'triage' END
     END) IS NOT NULL) = sqlc.arg(blocked)::boolean)
     AND (sqlc.arg(block_reason)::text = '' OR (CASE WHEN f.status IN ('needs_triage', 'confirmed', 'blocked') AND f.repo_id = ANY(sqlc.arg(enabled_repos)::bigint[])
          THEN CASE WHEN f.kind = 'malicious' AND f.repo_id = ANY(sqlc.arg(malicious_repos)::bigint[]) THEN 'malicious'
                    WHEN f.status = 'blocked' THEN 'triage' END
     END) = sqlc.arg(block_reason)::text)
     AND (NOT sqlc.arg(has_from)::boolean OR f.detected_at >= NULLIF(sqlc.arg(detected_from)::text, '')::timestamptz)
     AND (NOT sqlc.arg(has_to)::boolean OR f.detected_at < NULLIF(sqlc.arg(detected_to)::text, '')::timestamptz)
     AND (cardinality(sqlc.arg(ids)::text[]) = 0 OR f.id::text = ANY(sqlc.arg(ids)::text[]))
     AND (sqlc.arg(q)::text = ''
          OR position(lower(sqlc.arg(q)::text) in f.package_name) > 0
          OR position(lower(sqlc.arg(q)::text) in lower(f.advisory_id)) > 0
          OR EXISTS (SELECT 1 FROM vuln_advisory a
                      WHERE a.ecosystem = f.ecosystem AND a.canonical_id = f.advisory_id
                        AND (position(lower(sqlc.arg(q)::text) in lower(a.summary)) > 0
                             OR position(lower(sqlc.arg(q)::text) in lower(a.id)) > 0
                             OR EXISTS (SELECT 1 FROM unnest(a.aliases) al WHERE position(lower(sqlc.arg(q)::text) in lower(al)) > 0))))
         LIMIT sqlc.arg(cap)::int) c;

-- name: VulnFindingsLock :many
-- The findings of the given ids the caller can read, locked for a status change, in id order so that two bulk changes
-- never wait for each other in a circle. The ids are UUIDs the caller validated; the array compares against the primary key.
SELECT f.id::text AS id, f.repo_id, (rp.key || '/' || r.key)::text AS repo_key, r.format, f.ecosystem, f.package_name, f.version, f.advisory_id, f.kind, f.severity,
       f.status, COALESCE(f.dismissal_reason, '')::text AS dismissal_reason, COALESCE(f.comment, '')::text AS comment,
       f.revision
  FROM vuln_finding f
  JOIN repositories r ON r.id = f.repo_id
  JOIN project rp ON rp.id = r.project_id
 WHERE f.id = ANY(sqlc.arg(ids)::text[]::uuid[])
   AND (f.repo_id = ANY(sqlc.arg(whole)::bigint[])
          OR concat_ws(chr(31), f.repo_id::text, f.package_name, f.version) = ANY(sqlc.arg(rows)::text[])
          OR concat_ws(chr(31), f.repo_id::text, f.package_name) = ANY(sqlc.arg(package_rows)::text[]))
 ORDER BY f.id
   FOR UPDATE OF f;

-- name: VulnFindingSetStatus :one
-- A status change of one locked finding. expected is the revision the caller read (negative for any); a stale revision
-- returns no row. A comment that is not sent stays as it was.
UPDATE vuln_finding
   SET status = sqlc.arg(status)::text,
       dismissal_reason = NULLIF(sqlc.arg(dismissal_reason)::text, ''),
       comment = CASE WHEN sqlc.arg(has_comment)::boolean THEN NULLIF(sqlc.arg(comment)::text, '') ELSE comment END,
       revision = revision + 1,
       status_changed_at = now(),
       status_changed_by = NULLIF(sqlc.arg(actor)::text, '')::uuid
 WHERE id = sqlc.arg(id)::text::uuid
   AND (sqlc.arg(expected)::bigint < 0 OR revision = sqlc.arg(expected)::bigint)
RETURNING revision;

-- name: VulnFindingEventInsert :exec
-- One row of a finding's history (R-VULN-012): a status change by a person; details holds the bulk operation id.
INSERT INTO vuln_finding_event (finding_id, change, from_status, to_status, reason, comment, actor, details)
VALUES (sqlc.arg(finding_id)::text::uuid, 'status_changed', sqlc.arg(from_status)::text, sqlc.arg(to_status)::text,
        NULLIF(sqlc.arg(reason)::text, ''), NULLIF(sqlc.arg(comment)::text, ''),
        NULLIF(sqlc.arg(actor)::text, '')::uuid, NULLIF(sqlc.arg(details)::text, '')::jsonb);

-- name: VulnFindingEvents :many
-- The history of one finding, oldest first, with the name of the principal behind each change.
SELECT e.at, COALESCE(e.actor::text, '')::text AS actor_id, COALESCE(p.name, '')::text AS actor_name, e.change,
       COALESCE(e.from_status, '')::text AS from_status, COALESCE(e.to_status, '')::text AS to_status,
       COALESCE(e.reason, '')::text AS reason, COALESCE(e.comment, '')::text AS comment,
       COALESCE(e.details::text, '')::text AS details
  FROM vuln_finding_event e
  LEFT JOIN principal p ON p.id = e.actor
 WHERE e.finding_id = sqlc.arg(finding_id)::text::uuid
 ORDER BY e.seq
 LIMIT 1000;

-- name: VulnAdvisoryResolve :one
-- An advisory by identifier or alias with its group's aliases and affected entries (R-VULN-020): the record the identifier
-- names, else the group's canonical record. Returns no row for an unknown identifier.
WITH hit AS (
  SELECT a.canonical_id
    FROM vuln_advisory a
   WHERE a.ecosystem = sqlc.arg(ecosystem)::text
     AND (a.id = sqlc.arg(id)::text OR a.canonical_id = sqlc.arg(id)::text OR sqlc.arg(id)::text = ANY(a.aliases))
   ORDER BY (a.id = sqlc.arg(id)::text) DESC, (a.canonical_id = sqlc.arg(id)::text) DESC, a.modified DESC
   LIMIT 1
), rep AS (
  SELECT a.* FROM vuln_advisory a JOIN hit ON a.canonical_id = hit.canonical_id
   WHERE a.ecosystem = sqlc.arg(ecosystem)::text
   ORDER BY (a.id = hit.canonical_id) DESC, a.modified DESC
   LIMIT 1
)
SELECT rep.ecosystem, rep.canonical_id AS id, rep.kind, rep.summary, rep.details, rep.severity,
       COALESCE(rep.cvss_version, '')::text AS cvss_version, COALESCE(rep.cvss_vector, '')::text AS cvss_vector,
       COALESCE(rep.cvss_score, -1)::float8 AS cvss_score, rep.known_exploited,
       COALESCE(to_char(rep.kev_date_added, 'YYYY-MM-DD'), '')::text AS kev_added,
       rep."references" AS refs, rep.cwe_ids, rep.modified, rep.published, rep.withdrawn,
       COALESCE((SELECT array_agg(DISTINCT s.x ORDER BY s.x)
                   FROM (SELECT g.id AS x FROM vuln_advisory g WHERE g.ecosystem = rep.ecosystem AND g.canonical_id = rep.canonical_id
                         UNION
                         SELECT unnest(g.aliases) FROM vuln_advisory g WHERE g.ecosystem = rep.ecosystem AND g.canonical_id = rep.canonical_id) s
                  WHERE s.x <> rep.canonical_id), '{}')::text[] AS aliases,
       COALESCE((SELECT jsonb_agg(jsonb_build_object('name', x.name, 'ranges', x.ranges, 'versions', to_jsonb(x.versions),
                                                     'fixed', to_jsonb(x.fixed)) ORDER BY x.ord)
                   FROM vuln_affected x WHERE x.ecosystem = rep.ecosystem AND x.advisory_id = rep.id), '[]'::jsonb)::text AS affected
  FROM rep;

-- name: VulnPackageCounts :many
-- The open findings of one package the caller can read, per version and severity.
SELECT f.version, f.severity, count(*)::bigint AS n
  FROM vuln_finding f
  JOIN repositories r ON r.id = f.repo_id
 WHERE r.format = sqlc.arg(format)::text
   AND f.package_name = sqlc.arg(name)::text
   AND f.status IN ('needs_triage', 'confirmed', 'blocked')
   AND (f.repo_id = ANY(sqlc.arg(whole)::bigint[])
          OR concat_ws(chr(31), f.repo_id::text, f.package_name, f.version) = ANY(sqlc.arg(rows)::text[])
          OR concat_ws(chr(31), f.repo_id::text, f.package_name) = ANY(sqlc.arg(package_rows)::text[]))
 GROUP BY f.version, f.severity
 ORDER BY f.version, f.severity;

-- name: VulnSummaryCounts :many
-- The open findings the caller can read, per severity and kind, with the share that needs triage and the share with a fix.
SELECT f.severity, f.kind, count(*)::bigint AS total,
       (count(*) FILTER (WHERE f.status = 'needs_triage'))::bigint AS needs_triage,
       (count(*) FILTER (WHERE f.fix_available))::bigint AS fix_available
  FROM vuln_finding f
  JOIN repositories r ON r.id = f.repo_id
 WHERE f.status IN ('needs_triage', 'confirmed', 'blocked')
   AND (f.repo_id = ANY(sqlc.arg(whole)::bigint[])
          OR concat_ws(chr(31), f.repo_id::text, f.package_name, f.version) = ANY(sqlc.arg(rows)::text[])
          OR concat_ws(chr(31), f.repo_id::text, f.package_name) = ANY(sqlc.arg(package_rows)::text[]))
   AND (NOT sqlc.arg(has_repos)::boolean OR f.repo_id = ANY(sqlc.arg(repos)::bigint[]))
   AND (cardinality(sqlc.arg(formats)::text[]) = 0 OR r.format = ANY(sqlc.arg(formats)::text[]))
 GROUP BY f.severity, f.kind;

-- name: VulnSummaryBlocked :one
-- The versions the caller can read whose download a finding blocks under the repositories' policy.
SELECT count(DISTINCT (f.repo_id, f.package_name, f.version))::bigint AS n
  FROM vuln_finding f
  JOIN repositories r ON r.id = f.repo_id
 WHERE (CASE WHEN f.status IN ('needs_triage', 'confirmed', 'blocked') AND f.repo_id = ANY(sqlc.arg(enabled_repos)::bigint[])
          THEN CASE WHEN f.kind = 'malicious' AND f.repo_id = ANY(sqlc.arg(malicious_repos)::bigint[]) THEN 'malicious'
                    WHEN f.status = 'blocked' THEN 'triage' END
     END) IS NOT NULL
   AND (f.repo_id = ANY(sqlc.arg(whole)::bigint[])
          OR concat_ws(chr(31), f.repo_id::text, f.package_name, f.version) = ANY(sqlc.arg(rows)::text[])
          OR concat_ws(chr(31), f.repo_id::text, f.package_name) = ANY(sqlc.arg(package_rows)::text[]))
   AND (NOT sqlc.arg(has_repos)::boolean OR f.repo_id = ANY(sqlc.arg(repos)::bigint[]))
   AND (cardinality(sqlc.arg(formats)::text[]) = 0 OR r.format = ANY(sqlc.arg(formats)::text[]));

-- name: VulnTrendDetected :many
-- The findings the caller can read detected up to the last day, per day and severity; the days before the first day of the
-- range are summed into the day before it. The trend adds the days up (R-VULN-021).
SELECT to_char(GREATEST((f.detected_at AT TIME ZONE 'UTC')::date, sqlc.arg(from_day)::text::date - 1), 'YYYY-MM-DD')::text AS day,
       f.severity, count(*)::bigint AS n
  FROM vuln_finding f
  JOIN repositories r ON r.id = f.repo_id
 WHERE (f.detected_at AT TIME ZONE 'UTC')::date <= sqlc.arg(to_day)::text::date
   AND (f.repo_id = ANY(sqlc.arg(whole)::bigint[])
          OR concat_ws(chr(31), f.repo_id::text, f.package_name, f.version) = ANY(sqlc.arg(rows)::text[])
          OR concat_ws(chr(31), f.repo_id::text, f.package_name) = ANY(sqlc.arg(package_rows)::text[]))
   AND (NOT sqlc.arg(has_repos)::boolean OR f.repo_id = ANY(sqlc.arg(repos)::bigint[]))
   AND (cardinality(sqlc.arg(formats)::text[]) = 0 OR r.format = ANY(sqlc.arg(formats)::text[]))
   AND (sqlc.arg(kind)::text = '' OR f.kind = sqlc.arg(kind)::text)
 GROUP BY 1, 2;

-- name: VulnTrendResolved :many
-- The findings the caller can read that are resolved, per day of their resolution and severity, with the days before the
-- first day of the range summed into the day before it.
SELECT to_char(GREATEST((f.resolved_at AT TIME ZONE 'UTC')::date, sqlc.arg(from_day)::text::date - 1), 'YYYY-MM-DD')::text AS day,
       f.severity, count(*)::bigint AS n
  FROM vuln_finding f
  JOIN repositories r ON r.id = f.repo_id
 WHERE f.status = 'resolved' AND f.resolved_at IS NOT NULL
   AND (f.resolved_at AT TIME ZONE 'UTC')::date <= sqlc.arg(to_day)::text::date
   AND (f.repo_id = ANY(sqlc.arg(whole)::bigint[])
          OR concat_ws(chr(31), f.repo_id::text, f.package_name, f.version) = ANY(sqlc.arg(rows)::text[])
          OR concat_ws(chr(31), f.repo_id::text, f.package_name) = ANY(sqlc.arg(package_rows)::text[]))
   AND (NOT sqlc.arg(has_repos)::boolean OR f.repo_id = ANY(sqlc.arg(repos)::bigint[]))
   AND (cardinality(sqlc.arg(formats)::text[]) = 0 OR r.format = ANY(sqlc.arg(formats)::text[]))
   AND (sqlc.arg(kind)::text = '' OR f.kind = sqlc.arg(kind)::text)
 GROUP BY 1, 2;

-- name: VulnSourceStates :many
-- The state of every source and ecosystem as the sources operation and the system information read it (R-VULN-021).
SELECT source, ecosystem, state, synced_at, full_synced_at, attempted_at, failures,
       COALESCE(last_error::text, '')::text AS last_error, records, rejected, unevaluable, full_rejected
  FROM vuln_source_state
 ORDER BY source, ecosystem;

-- name: VulnClaimedMatches :many
-- The public advisories that name an internal name (R-VULN-010), in keyset order.
SELECT ecosystem, advisory_id, name, kind, first_seen_at
  FROM vuln_claimed_match
 WHERE NOT sqlc.arg(has_after)::boolean
    OR (ecosystem COLLATE "C", advisory_id COLLATE "C", name COLLATE "C")
       > (sqlc.arg(after_ecosystem)::text COLLATE "C", sqlc.arg(after_advisory)::text COLLATE "C", sqlc.arg(after_name)::text COLLATE "C")
 ORDER BY ecosystem COLLATE "C", advisory_id COLLATE "C", name COLLATE "C"
 LIMIT sqlc.arg(max_rows)::int;
