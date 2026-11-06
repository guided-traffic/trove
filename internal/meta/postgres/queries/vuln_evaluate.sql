-- Statements of the vulnerability scanning, work package evaluate (docs/developer/vulnerabilities.md). One file per work package;
-- every statement of this file is the package's. Plain Go types only (no pgtype): COALESCE nullable columns, a time travels as
-- microseconds since the epoch, an id of a finding as text.

-- name: VulnAffectedForNames :many
-- The affected entries of the ecosystem that name any of the names, with the advisory's facts, in one query (R-VULN-011): the
-- index vuln_affected_name serves it. Severity, kind and the known-exploited flag are those of the canonical record of the alias
-- group when it is stored (Q-VULN-35), else those of the record itself; the kind is that of the record whose entry this is (Q-VULN-65:
-- a malicious record makes a version malicious only when its own entry affects it). Withdrawn records are returned with their
-- flag: they match nothing, and tell a resolution `advisory_withdrawn` from `no_longer_affected`.
SELECT a.name::text AS name, v.id::text AS record_id, v.canonical_id::text AS canonical_id,
       (extract(epoch FROM COALESCE(c.modified, v.modified)) * 1000000)::bigint AS modified_us,
       (v.withdrawn IS NOT NULL)::boolean AS withdrawn,
       v.kind::text AS kind,
       COALESCE(c.severity, v.severity)::text AS severity,
       COALESCE(c.cvss_score, v.cvss_score, -1)::float8 AS cvss_score,
       (COALESCE(c.known_exploited, false) OR v.known_exploited)::boolean AS known_exploited,
       a.ranges AS ranges, a.versions::text[] AS versions, a.every_version::boolean AS every_version, a.fixed::text[] AS fixed
  FROM vuln_affected a
  JOIN vuln_advisory v ON v.ecosystem = a.ecosystem AND v.id = a.advisory_id
  LEFT JOIN vuln_advisory c ON c.ecosystem = v.ecosystem AND c.id = v.canonical_id
 WHERE a.ecosystem = sqlc.arg(ecosystem)::text
   AND a.name = ANY(sqlc.arg(names)::text[]);

-- name: VulnAffectedForItem :many
-- VulnAffectedForNames for the name of an item (the commit hook, Q-VULN-82): the same columns and the same severity rules.
SELECT a.name::text AS name, v.id::text AS record_id, v.canonical_id::text AS canonical_id,
       (extract(epoch FROM COALESCE(c.modified, v.modified)) * 1000000)::bigint AS modified_us,
       (v.withdrawn IS NOT NULL)::boolean AS withdrawn,
       v.kind::text AS kind,
       COALESCE(c.severity, v.severity)::text AS severity,
       COALESCE(c.cvss_score, v.cvss_score, -1)::float8 AS cvss_score,
       (COALESCE(c.known_exploited, false) OR v.known_exploited)::boolean AS known_exploited,
       a.ranges AS ranges, a.versions::text[] AS versions, a.every_version::boolean AS every_version, a.fixed::text[] AS fixed
  FROM vuln_affected a
  JOIN vuln_advisory v ON v.ecosystem = a.ecosystem AND v.id = a.advisory_id
  LEFT JOIN vuln_advisory c ON c.ecosystem = v.ecosystem AND c.id = v.canonical_id
 WHERE a.ecosystem = sqlc.arg(ecosystem)::text
   AND a.name = (SELECT p.name FROM items i
                    JOIN package_versions pv ON pv.id = i.version_id
                    JOIN packages p ON p.id = pv.package_id
                   WHERE i.id = sqlc.arg(item_id)::bigint);

-- name: VulnStoredVersions :many
-- The stored versions of the names in the repositories of the formats (the lookup of the advisory-driven evaluation by name,
-- served by packages_name, Q-VULN-24); repo_id restricts it to one repository, 0 is every repository. A repository being
-- deleted and a trashed version are not stored versions.
SELECT p.repo_id::bigint AS repo_id, p.id::bigint AS package_id, p.name::text AS name, pv.id::bigint AS version_id, pv.version::text AS version
  FROM packages p
  JOIN repositories r ON r.id = p.repo_id
  JOIN package_versions pv ON pv.package_id = p.id
 WHERE p.name = ANY(sqlc.arg(names)::text[])
   AND r.format = ANY(sqlc.arg(formats)::text[])
   AND r.class IN ('local', 'remote')
   AND r.lifecycle = 'active'
   AND pv.trashed_at IS NULL
   AND (sqlc.arg(repo_id)::bigint = 0 OR p.repo_id = sqlc.arg(repo_id)::bigint)
 ORDER BY p.repo_id, p.name, pv.id;

-- name: VulnPackageNamesPage :many
-- One page of the distinct package names of the repositories of the formats after a cursor, in byte order: the batches of the
-- full evaluation (1,000 names) and of a rescan (one repository when repo_id is not 0).
SELECT n.name::text AS name FROM (
    SELECT DISTINCT p.name
      FROM packages p
      JOIN repositories r ON r.id = p.repo_id
     WHERE p.name COLLATE "C" > sqlc.arg(after)::text COLLATE "C"
       AND r.format = ANY(sqlc.arg(formats)::text[])
       AND r.class IN ('local', 'remote')
       AND r.lifecycle = 'active'
       AND (sqlc.arg(repo_id)::bigint = 0 OR p.repo_id = sqlc.arg(repo_id)::bigint)
) n
 ORDER BY n.name COLLATE "C"
 LIMIT sqlc.arg(max_rows)::int;

-- name: VulnCountStoredVersions :one
-- The number of stored versions of the formats, in one repository when repo_id is not 0: the total of a job's progress.
SELECT count(*)::bigint
  FROM package_versions pv
  JOIN packages p ON p.id = pv.package_id
  JOIN repositories r ON r.id = p.repo_id
 WHERE r.format = ANY(sqlc.arg(formats)::text[])
   AND r.class IN ('local', 'remote')
   AND r.lifecycle = 'active'
   AND pv.trashed_at IS NULL
   AND (sqlc.arg(repo_id)::bigint = 0 OR p.repo_id = sqlc.arg(repo_id)::bigint);

-- name: VulnItemVersion :many
-- The version an item of a commit belongs to (items.version_id, set by the format's OnCommit): the name and the normalized
-- version. No row for an item that belongs to no version, or whose version is trashed.
SELECT pv.id::bigint AS version_id, p.id::bigint AS package_id, p.name::text AS name, pv.version::text AS version,
       COALESCE(pv.lead_item_id, 0)::bigint AS lead_item_id
  FROM items i
  JOIN package_versions pv ON pv.id = i.version_id
  JOIN packages p ON p.id = pv.package_id
 WHERE i.id = sqlc.arg(item_id)::bigint
   AND pv.trashed_at IS NULL;

-- name: VulnFindingsForNames :many
-- Every finding of the names of an ecosystem in every repository, resolved ones too; vuln_finding_package serves it.
SELECT f.id::text AS id, f.repo_id::bigint AS repo_id, f.package_name::text AS package_name, f.version::text AS version,
       f.advisory_id::text AS advisory_id, COALESCE(f.package_id, 0)::bigint AS package_id, COALESCE(f.version_id, 0)::bigint AS version_id,
       f.kind::text AS kind, f.severity::text AS severity, COALESCE(f.cvss_score, -1)::float8 AS cvss_score, f.known_exploited::boolean AS known_exploited,
       f.fix_available::boolean AS fix_available, f.fixed_versions::text[] AS fixed_versions, f.status::text AS status,
       COALESCE(f.status_before_resolution, '')::text AS status_before_resolution, COALESCE(f.resolved_reason, '')::text AS resolved_reason,
       (extract(epoch FROM f.advisory_modified) * 1000000)::bigint AS advisory_modified_us, f.revision::bigint AS revision
  FROM vuln_finding f
 WHERE f.ecosystem = sqlc.arg(ecosystem)::text
   AND f.package_name = ANY(sqlc.arg(names)::text[]);

-- name: VulnFindingsOfRepoNames :many
-- The findings of the names in one repository, resolved ones too (the batches of a rescan).
SELECT f.id::text AS id, f.repo_id::bigint AS repo_id, f.package_name::text AS package_name, f.version::text AS version,
       f.advisory_id::text AS advisory_id, COALESCE(f.package_id, 0)::bigint AS package_id, COALESCE(f.version_id, 0)::bigint AS version_id,
       f.kind::text AS kind, f.severity::text AS severity, COALESCE(f.cvss_score, -1)::float8 AS cvss_score, f.known_exploited::boolean AS known_exploited,
       f.fix_available::boolean AS fix_available, f.fixed_versions::text[] AS fixed_versions, f.status::text AS status,
       COALESCE(f.status_before_resolution, '')::text AS status_before_resolution, COALESCE(f.resolved_reason, '')::text AS resolved_reason,
       (extract(epoch FROM f.advisory_modified) * 1000000)::bigint AS advisory_modified_us, f.revision::bigint AS revision
  FROM vuln_finding f
 WHERE f.repo_id = sqlc.arg(repo_id)::bigint
   AND f.ecosystem = sqlc.arg(ecosystem)::text
   AND f.package_name = ANY(sqlc.arg(names)::text[]);

-- name: VulnFindingsOfRepoItem :many
-- VulnFindingsOfRepoNames for the name of an item: the commit hook reads it in the round trip that reads the item (Q-VULN-82),
-- before it knows the name. No row for an item that belongs to no version. The statement does not look at the trash: VulnItemVersion
-- is the one liveness check, as the statements by name never had one.
SELECT f.id::text AS id, f.repo_id::bigint AS repo_id, f.package_name::text AS package_name, f.version::text AS version,
       f.advisory_id::text AS advisory_id, COALESCE(f.package_id, 0)::bigint AS package_id, COALESCE(f.version_id, 0)::bigint AS version_id,
       f.kind::text AS kind, f.severity::text AS severity, COALESCE(f.cvss_score, -1)::float8 AS cvss_score, f.known_exploited::boolean AS known_exploited,
       f.fix_available::boolean AS fix_available, f.fixed_versions::text[] AS fixed_versions, f.status::text AS status,
       COALESCE(f.status_before_resolution, '')::text AS status_before_resolution, COALESCE(f.resolved_reason, '')::text AS resolved_reason,
       (extract(epoch FROM f.advisory_modified) * 1000000)::bigint AS advisory_modified_us, f.revision::bigint AS revision
  FROM vuln_finding f
 WHERE f.repo_id = sqlc.arg(repo_id)::bigint
   AND f.ecosystem = sqlc.arg(ecosystem)::text
   AND f.package_name = (SELECT p.name FROM items i
                    JOIN package_versions pv ON pv.id = i.version_id
                    JOIN packages p ON p.id = pv.package_id
                   WHERE i.id = sqlc.arg(item_id)::bigint);

-- name: VulnFindingsByIdentity :many
-- The finding of one identity, read again after an insert that found it (a concurrent evaluation wrote it first).
SELECT f.id::text AS id, f.repo_id::bigint AS repo_id, f.package_name::text AS package_name, f.version::text AS version,
       f.advisory_id::text AS advisory_id, COALESCE(f.package_id, 0)::bigint AS package_id, COALESCE(f.version_id, 0)::bigint AS version_id,
       f.kind::text AS kind, f.severity::text AS severity, COALESCE(f.cvss_score, -1)::float8 AS cvss_score, f.known_exploited::boolean AS known_exploited,
       f.fix_available::boolean AS fix_available, f.fixed_versions::text[] AS fixed_versions, f.status::text AS status,
       COALESCE(f.status_before_resolution, '')::text AS status_before_resolution, COALESCE(f.resolved_reason, '')::text AS resolved_reason,
       (extract(epoch FROM f.advisory_modified) * 1000000)::bigint AS advisory_modified_us, f.revision::bigint AS revision
  FROM vuln_finding f
 WHERE f.repo_id = sqlc.arg(repo_id)::bigint AND f.ecosystem = sqlc.arg(ecosystem)::text AND f.package_name = sqlc.arg(package_name)::text
   AND f.version = sqlc.arg(version)::text AND f.advisory_id = sqlc.arg(advisory_id)::text;

-- name: VulnFindingByID :many
-- One finding by its id, read again after a conditional status update lost (Q-VULN-65).
SELECT f.id::text AS id, f.repo_id::bigint AS repo_id, f.package_name::text AS package_name, f.version::text AS version,
       f.advisory_id::text AS advisory_id, COALESCE(f.package_id, 0)::bigint AS package_id, COALESCE(f.version_id, 0)::bigint AS version_id,
       f.kind::text AS kind, f.severity::text AS severity, COALESCE(f.cvss_score, -1)::float8 AS cvss_score, f.known_exploited::boolean AS known_exploited,
       f.fix_available::boolean AS fix_available, f.fixed_versions::text[] AS fixed_versions, f.status::text AS status,
       COALESCE(f.status_before_resolution, '')::text AS status_before_resolution, COALESCE(f.resolved_reason, '')::text AS resolved_reason,
       (extract(epoch FROM f.advisory_modified) * 1000000)::bigint AS advisory_modified_us, f.revision::bigint AS revision
  FROM vuln_finding f
 WHERE f.id = sqlc.arg(id)::text::uuid;

-- name: VulnInsertFinding :many
-- The id comes back for the row this statement wrote and for none when the identity exists already, so that the history is
-- written for a row that exists (Q-VULN-65).
INSERT INTO vuln_finding (id, repo_id, ecosystem, package_name, version, advisory_id, package_id, version_id, kind, severity, severity_rank,
                          cvss_score, known_exploited, fix_available, fixed_versions, status, advisory_modified)
VALUES (sqlc.arg(id)::text::uuid, sqlc.arg(repo_id)::bigint, sqlc.arg(ecosystem)::text, sqlc.arg(package_name)::text, sqlc.arg(version)::text,
        sqlc.arg(advisory_id)::text, NULLIF(sqlc.arg(package_id)::bigint, 0), NULLIF(sqlc.arg(version_id)::bigint, 0), sqlc.arg(kind)::text,
        sqlc.arg(severity)::text, sqlc.arg(severity_rank)::smallint, NULLIF(sqlc.arg(cvss_score)::float8, -1)::numeric(3,1),
        sqlc.arg(known_exploited)::boolean, sqlc.arg(fix_available)::boolean, sqlc.arg(fixed_versions)::text[], 'needs_triage',
        to_timestamp(sqlc.arg(advisory_modified_us)::bigint / 1000000.0))
ON CONFLICT (repo_id, ecosystem, package_name, version, advisory_id) DO NOTHING
RETURNING id::text AS id;

-- name: VulnRefreshFinding :exec
-- The facts of the advisory as the evaluation last saw them, and the version the finding points to now (a version fetched
-- again is a new row). The status is not touched.
UPDATE vuln_finding
   SET kind = sqlc.arg(kind)::text, severity = sqlc.arg(severity)::text, severity_rank = sqlc.arg(severity_rank)::smallint,
       cvss_score = NULLIF(sqlc.arg(cvss_score)::float8, -1)::numeric(3,1), known_exploited = sqlc.arg(known_exploited)::boolean,
       fix_available = sqlc.arg(fix_available)::boolean, fixed_versions = sqlc.arg(fixed_versions)::text[],
       advisory_modified = to_timestamp(sqlc.arg(advisory_modified_us)::bigint / 1000000.0),
       package_id = NULLIF(sqlc.arg(package_id)::bigint, 0), version_id = NULLIF(sqlc.arg(version_id)::bigint, 0),
       revision = revision + 1
 WHERE id = sqlc.arg(id)::text::uuid;

-- name: VulnTouchFindingModified :exec
-- The record of an advisory was modified and nothing a finding shows changed: the time is stored, the revision and the history
-- stay (Q-VULN-66).
UPDATE vuln_finding SET advisory_modified = to_timestamp(sqlc.arg(advisory_modified_us)::bigint / 1000000.0)
 WHERE id = sqlc.arg(id)::text::uuid;

-- name: VulnSetFindingStatus :execrows
-- trove's own status change: a re-detection or a reopen, only when the finding still has the status and the revision the evaluation
-- read, so that a triage that came between is never overwritten (Q-VULN-65). The resolution is cleared. The dismissal's reason and
-- comment go when the finding moves away from dismissed (also from a resolution that came from dismissed); a triage comment on
-- any other status stays (the history keeps what went).
UPDATE vuln_finding
   SET status = sqlc.arg(status)::text, status_before_resolution = NULL, resolved_reason = NULL, resolved_at = NULL,
       dismissal_reason = CASE WHEN sqlc.arg(status)::text = 'dismissed' OR NOT (status = 'dismissed' OR (status = 'resolved' AND status_before_resolution = 'dismissed'))
                               THEN dismissal_reason END,
       comment = CASE WHEN sqlc.arg(status)::text = 'dismissed' OR NOT (status = 'dismissed' OR (status = 'resolved' AND status_before_resolution = 'dismissed'))
                      THEN comment END,
       status_changed_at = now(), status_changed_by = NULL, revision = revision + 1
 WHERE id = sqlc.arg(id)::text::uuid AND status = sqlc.arg(expect_status)::text AND revision = sqlc.arg(expect_revision)::bigint;

-- name: VulnRekeyFinding :exec
-- The canonical identifier of the alias group changed: the finding follows it and keeps its status and history.
UPDATE vuln_finding SET advisory_id = sqlc.arg(advisory_id)::text, revision = revision + 1 WHERE id = sqlc.arg(id)::text::uuid;

-- name: VulnMoveFindingEvents :exec
-- The history of a finding that merges into another one (two findings of one version whose advisories joined one alias group).
UPDATE vuln_finding_event SET finding_id = sqlc.arg(to_id)::text::uuid WHERE finding_id = sqlc.arg(from_id)::text::uuid;

-- name: VulnDeleteFinding :exec
DELETE FROM vuln_finding WHERE id = sqlc.arg(id)::text::uuid;

-- name: VulnRecentFindingEvents :many
-- The last events of a finding, newest first, with their age: the evaluation coalesces a resolution that did not last (Q-VULN-66).
SELECT seq::bigint AS seq, change::text AS change, extract(epoch FROM now() - at)::float8 AS age_seconds
  FROM vuln_finding_event
 WHERE finding_id = sqlc.arg(finding_id)::text::uuid
 ORDER BY seq DESC
 LIMIT sqlc.arg(max_rows)::int;

-- name: VulnDeleteFindingEvent :exec
DELETE FROM vuln_finding_event WHERE finding_id = sqlc.arg(finding_id)::text::uuid AND seq = sqlc.arg(seq)::bigint;

-- name: VulnResolveFindings :many
-- trove resolves findings: the previous status is kept in status_before_resolution. Only a finding that is not resolved yet
-- changes, so a batch retried after a lost commit changes nothing the second time. The old status is returned for the events.
WITH old AS (
    SELECT id, status FROM vuln_finding WHERE id = ANY(sqlc.arg(ids)::text[]::uuid[]) AND status <> 'resolved' FOR UPDATE
)
UPDATE vuln_finding f
   SET status = 'resolved', status_before_resolution = old.status, resolved_reason = sqlc.arg(reason)::text, resolved_at = now(),
       status_changed_at = now(), status_changed_by = NULL, revision = f.revision + 1
  FROM old
 WHERE f.id = old.id
RETURNING f.id::text AS id, old.status::text AS old_status;

-- name: VulnResolveRemovedVersions :many
-- Removal of versions (R-VULN-011): the findings of a version whose lead item is gone resolve with version_removed. The hook runs
-- before the format deletes the version row, in the removing transaction, when the removed items are gone already. A version
-- that keeps its lead item (a removed companion file only) keeps its findings; one without a lead item is removed when it has
-- no file left.
WITH old AS (
    SELECT f.id, f.status FROM vuln_finding f
     WHERE f.version_id = ANY(sqlc.arg(version_ids)::bigint[]) AND f.status <> 'resolved'  -- vuln_finding_version_id serves the lookup
       AND NOT EXISTS (SELECT 1 FROM package_versions pv
                        WHERE pv.id = f.version_id
                          AND (EXISTS (SELECT 1 FROM items l WHERE l.id = pv.lead_item_id)
                               OR (pv.lead_item_id IS NULL AND EXISTS (SELECT 1 FROM items i WHERE i.version_id = pv.id AND i.kind = 'file'))))
       FOR UPDATE OF f
)
UPDATE vuln_finding f
   SET status = 'resolved', status_before_resolution = old.status, resolved_reason = 'version_removed', resolved_at = now(),
       status_changed_at = now(), status_changed_by = NULL, revision = f.revision + 1
  FROM old
 WHERE f.id = old.id
RETURNING f.id::text AS id, old.status::text AS old_status;

-- name: VulnResolveRepoFindings :many
-- A repository stops being scanned (R-VULN-002): every finding that is not resolved resolves with scanning_disabled.
WITH old AS (
    SELECT id, status FROM vuln_finding WHERE repo_id = sqlc.arg(repo_id)::bigint AND status <> 'resolved' FOR UPDATE
)
UPDATE vuln_finding f
   SET status = 'resolved', status_before_resolution = old.status, resolved_reason = 'scanning_disabled', resolved_at = now(),
       status_changed_at = now(), status_changed_by = NULL, revision = f.revision + 1
  FROM old
 WHERE f.id = old.id
RETURNING f.id::text AS id, old.status::text AS old_status;

-- name: VulnInsertFindingEvent :exec
INSERT INTO vuln_finding_event (finding_id, change, from_status, to_status, reason, details)
VALUES (sqlc.arg(finding_id)::text::uuid, sqlc.arg(change)::text, NULLIF(sqlc.arg(from_status)::text, ''), NULLIF(sqlc.arg(to_status)::text, ''),
        NULLIF(sqlc.arg(reason)::text, ''), NULLIF(sqlc.arg(details)::text, '')::jsonb);

-- name: VulnDeleteResolvedFindings :execrows
-- The retention task (R-VULN-014): resolved findings older than the retention, with their events (the foreign key cascades), in
-- batches (vuln_finding_resolved_at serves the order). A finding re-detected before then is not resolved and stays.
DELETE FROM vuln_finding
 WHERE id IN (SELECT f.id FROM vuln_finding f
               WHERE f.status = 'resolved' AND f.resolved_at < now() - make_interval(secs => sqlc.arg(retention_seconds)::float8)
               ORDER BY f.resolved_at LIMIT sqlc.arg(max_rows)::int);

-- name: VulnInsertClaimedMatch :execrows
-- One row per advisory and name (R-VULN-010); the row count says whether it is new, which is when the security event is written.
INSERT INTO vuln_claimed_match (ecosystem, advisory_id, name, kind)
VALUES (sqlc.arg(ecosystem)::text, sqlc.arg(advisory_id)::text, sqlc.arg(name)::text, sqlc.arg(kind)::text)
ON CONFLICT (ecosystem, advisory_id, name) DO NOTHING;

-- name: VulnPruneClaimedMatches :exec
-- The matches of the names that the evaluation no longer finds: the claim is gone, or the advisory no longer names the name.
-- keep is the pairs that still match, each written name, a line feed, canonical identifier (a name holds no line feed); a name
-- that is in none of them loses every match.
DELETE FROM vuln_claimed_match m
 WHERE m.ecosystem = sqlc.arg(ecosystem)::text AND m.name = ANY(sqlc.arg(names)::text[])
   AND NOT ((m.name || E'\n' || m.advisory_id) = ANY(sqlc.arg(keep)::text[]));

-- name: VulnSourceFullEvaluations :many
-- The ecosystems whose full evaluation is requested (Q-VULN-18), with the value of the request.
SELECT ecosystem::text AS ecosystem, (extract(epoch FROM full_evaluation_requested_at) * 1000000)::bigint AS requested_us
  FROM vuln_source_state
 WHERE source = 'osv' AND full_evaluation_requested_at IS NOT NULL
 ORDER BY ecosystem;

-- name: VulnClearFullEvaluation :execrows
-- Clears the request only when it still holds the value read at the run's start: a request that arrived during the run stays.
UPDATE vuln_source_state
   SET full_evaluation_requested_at = NULL
 WHERE source = 'osv' AND ecosystem = sqlc.arg(ecosystem)::text
   AND (extract(epoch FROM full_evaluation_requested_at) * 1000000)::bigint = sqlc.arg(requested_us)::bigint;

-- name: VulnDataComplete :one
-- Whether the ecosystem's advisory data has been imported completely (R-VULN-011: no evaluation resolves a finding before).
SELECT EXISTS (SELECT 1 FROM vuln_source_state WHERE source = 'osv' AND ecosystem = sqlc.arg(ecosystem)::text AND state = 'complete')::boolean;

-- name: VulnPendingCount :one
-- The queued advisories of the ecosystems this build evaluates: a row of another ecosystem stays for a build that has its adapter.
SELECT count(*)::bigint FROM vuln_pending_evaluation WHERE ecosystem = ANY(sqlc.arg(ecosystems)::text[]);

-- name: VulnPendingBatch :many
SELECT ecosystem::text AS ecosystem, advisory_id::text AS advisory_id, (extract(epoch FROM enqueued_at) * 1000000)::bigint AS enqueued_us
  FROM vuln_pending_evaluation
 WHERE ecosystem = ANY(sqlc.arg(ecosystems)::text[])
 ORDER BY enqueued_at, ecosystem, advisory_id
 LIMIT sqlc.arg(max_rows)::int;

-- name: VulnDeletePending :exec
-- Deletes the rows of a batch that still hold the enqueue time read with them: an advisory that changed again meanwhile stays
-- queued.
DELETE FROM vuln_pending_evaluation p
 USING (SELECT unnest(sqlc.arg(ecosystems)::text[]) AS ecosystem, unnest(sqlc.arg(ids)::text[]) AS advisory_id,
               unnest(sqlc.arg(enqueued_us)::bigint[]) AS enqueued_us) d
 WHERE p.ecosystem = d.ecosystem AND p.advisory_id = d.advisory_id
   AND (extract(epoch FROM p.enqueued_at) * 1000000)::bigint = d.enqueued_us;

-- name: VulnGroupNames :many
-- The names the alias groups of the advisories name now: the affected entries of every record of each group.
SELECT DISTINCT a.name::text AS name
  FROM vuln_advisory v
  JOIN vuln_advisory g ON g.ecosystem = v.ecosystem AND g.canonical_id = v.canonical_id
  JOIN vuln_affected a ON a.ecosystem = g.ecosystem AND a.advisory_id = g.id
 WHERE v.ecosystem = sqlc.arg(ecosystem)::text AND v.id = ANY(sqlc.arg(ids)::text[]);

-- name: VulnCanonicalOfRecords :many
-- The canonical identifier of the records of the ids, to read what a queued advisory stood for.
SELECT id::text AS id, canonical_id::text AS canonical_id
  FROM vuln_advisory
 WHERE ecosystem = sqlc.arg(ecosystem)::text AND id = ANY(sqlc.arg(ids)::text[]);

-- name: VulnFindingNamesForAdvisories :many
-- The names that findings of the advisories (canonical identifiers) exist for: names an advisory named before.
SELECT DISTINCT package_name::text AS name
  FROM vuln_finding
 WHERE ecosystem = sqlc.arg(ecosystem)::text AND advisory_id = ANY(sqlc.arg(ids)::text[]);

-- name: VulnRekeyTargets :many
-- Where an advisory identifier that a finding carries has gone: the canonical identifier of the record that is, or aliases, the
-- identifier now (R-VULN-006: the canonical identifier of a group is recomputed, and the findings follow it).
SELECT v.id::text AS id, v.canonical_id::text AS canonical_id, v.aliases::text[] AS aliases
  FROM vuln_advisory v
 WHERE v.ecosystem = sqlc.arg(ecosystem)::text
   AND (v.id = ANY(sqlc.arg(ids)::text[]) OR v.aliases && sqlc.arg(ids)::text[]);

-- name: VulnClaimHolders :many
-- The namespace claims of the local repositories of a format that can cover a name, with the holder's scanning inputs, read in the
-- transaction of a commit: the claims rule of R-VULN-010 must not take a second connection while the commit holds one. The
-- prefilter only narrows (an exact claim, or a literal prefix of the name); a pattern with an escape is always returned and the
-- caller matches every row with the pattern semantics of the claim index.
SELECT c.repo_id::bigint AS repo_id, r.key::text AS key, r.config::text AS config,
       c.pattern::text AS pattern, c.scope::text AS scope, c.source::text AS source
  FROM namespace_claims c
  JOIN repositories r ON r.id = c.repo_id
 WHERE r.format = sqlc.arg(format)::text
   AND r.class = 'local'
   AND (c.pattern = sqlc.arg(name)::text
        OR position(chr(92) IN c.pattern) > 0
        OR (right(c.pattern, 1) = '*' AND starts_with(sqlc.arg(name)::text, rtrim(c.pattern, '*'))));

-- name: VulnClaimHoldersForItem :many
-- VulnClaimHolders for the name of an item (the commit hook, Q-VULN-82): the same prefilter, with the name read from the item.
SELECT c.repo_id::bigint AS repo_id, r.key::text AS key, r.config::text AS config,
       c.pattern::text AS pattern, c.scope::text AS scope, c.source::text AS source
  FROM namespace_claims c
  JOIN repositories r ON r.id = c.repo_id
  CROSS JOIN (SELECT p.name AS name FROM items i
                    JOIN package_versions pv ON pv.id = i.version_id
                    JOIN packages p ON p.id = pv.package_id
                   WHERE i.id = sqlc.arg(item_id)::bigint) AS n
 WHERE r.format = sqlc.arg(format)::text
   AND r.class = 'local'
   AND (c.pattern = n.name
        OR position(chr(92) IN c.pattern) > 0
        OR (right(c.pattern, 1) = '*' AND starts_with(n.name, rtrim(c.pattern, '*'))));

-- name: VulnRepositoryInputs :many
-- The scanning inputs of a repository, read from the row and not from a replica's snapshot, so that a rescan sees a change
-- committed just before it ran (R-VULN-011): the document as text.
SELECT id::bigint AS id, key::text AS key, class::text AS class, format::text AS format, lifecycle::text AS lifecycle, config::text AS config
  FROM repositories
 WHERE id = sqlc.arg(id)::bigint;

-- name: VulnUnversionedItems :many
-- The cached files of the remote repositories of the formats that no version row belongs to (the tarballs a remote repository
-- cached before the scanning recorded versions, Q-VULN-67), after a cursor, in the order of their ids; path_like narrows them to
-- the format's artifact suffix.
SELECT i.id::bigint AS item_id, i.repo_id::bigint AS repo_id, i.path::text AS path
  FROM items i
  JOIN repositories r ON r.id = i.repo_id
 WHERE i.id > sqlc.arg(after)::bigint
   AND r.class = 'remote' AND r.lifecycle = 'active' AND r.format = ANY(sqlc.arg(formats)::text[])
   AND i.version_id IS NULL AND i.kind = 'file' AND i.trashed_at IS NULL AND i.path LIKE sqlc.arg(path_like)::text
 ORDER BY i.id
 LIMIT sqlc.arg(max_rows)::int;
