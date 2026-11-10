-- Statements of the scanning of container images (R-VULN-033 to R-VULN-037, internal/vuln/imagescan). Plain Go types only: a digest is
-- bytea, a time travels as microseconds since the epoch.

-- name: VulnImageDiscover :execrows
-- The image manifests of active local and remote repositories of the format that have no scan row yet: a push, a fill, an image stored
-- before the scanning was switched on and a restored installation all arrive here. An index is not scanned, its children are; an artifact
-- that is no container image (a chart, a signature) is left out by its media types.
INSERT INTO vuln_image_scan (repo_id, name, digest, not_before)
SELECT m.repo_id, m.name, m.digest, now() + make_interval(secs => sqlc.arg(delay_seconds)::float8)
  FROM oci_manifests m
  JOIN repositories r ON r.id = m.repo_id
 WHERE r.format = 'oci' AND r.class IN ('local', 'remote') AND r.lifecycle = 'active'
   AND m.media_type IN ('application/vnd.oci.image.manifest.v1+json', 'application/vnd.docker.distribution.manifest.v2+json')
   AND COALESCE(m.artifact_type, '') IN ('', 'application/vnd.oci.image.config.v1+json', 'application/vnd.docker.container.image.v1+json')
   AND NOT EXISTS (SELECT 1 FROM vuln_image_scan s WHERE s.repo_id = m.repo_id AND s.name = m.name AND s.digest = m.digest)
 LIMIT sqlc.arg(max_rows)::int
ON CONFLICT DO NOTHING;

-- name: VulnImageDue :many
-- The next manifests to work on: never scanned or to retry and due, or scanned against another database than the current one.
SELECT s.repo_id::bigint AS repo_id, s.name::text AS name, s.digest AS digest, s.attempts::int AS attempts
  FROM vuln_image_scan s
 WHERE (s.state = 'pending' AND s.not_before <= now())
    OR (s.state = 'failed' AND s.not_before <= now() AND s.attempts < sqlc.arg(max_attempts)::int)
    OR (s.state = 'scanned' AND s.db_digest IS DISTINCT FROM sqlc.arg(db_digest)::text)
 ORDER BY s.not_before, s.repo_id, s.name, s.digest
 LIMIT sqlc.arg(max_rows)::int;

-- name: VulnImageDueCount :one
SELECT count(*)::bigint
  FROM vuln_image_scan s
 WHERE s.state IN ('pending', 'failed') OR (s.state = 'scanned' AND s.db_digest IS DISTINCT FROM sqlc.arg(db_digest)::text);

-- name: VulnImageScanned :exec
-- The record of a finished detection.
UPDATE vuln_image_scan
   SET state = 'scanned', db_digest = sqlc.arg(db_digest)::text, scanned_at = now(), attempts = 0, error_code = NULL, error_detail = NULL,
       os_family = NULLIF(sqlc.arg(os_family)::text, ''), os_name = NULLIF(sqlc.arg(os_name)::text, ''), os_eosl = sqlc.arg(os_eosl)::boolean,
       packages = sqlc.arg(packages)::int
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND name = sqlc.arg(name)::text AND digest = sqlc.arg(digest);

-- name: VulnImageFailed :exec
-- A scan that did not finish: the state says so, so that nobody reads the absence of findings as clean. A row that had a detection keeps
-- it, and it is due again after the back-off.
UPDATE vuln_image_scan
   SET state = 'failed', attempts = attempts + 1, not_before = now() + make_interval(secs => sqlc.arg(backoff_seconds)::float8),
       error_code = sqlc.arg(error_code)::text, error_detail = left(sqlc.arg(error_detail)::text, 1024)
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND name = sqlc.arg(name)::text AND digest = sqlc.arg(digest);

-- name: VulnImageRearm :execrows
-- A rescan: every manifest of the repository is due again, and an analysis is not repeated (the document of the analysis stays).
UPDATE vuln_image_scan
   SET state = 'pending', not_before = now(), attempts = 0
 WHERE repo_id = sqlc.arg(repo_id)::bigint;

-- name: VulnImageOrphanFindings :many
-- The findings of images whose manifest is gone from the repository (the scan row went with it): they resolve with version_removed.
SELECT f.id::text AS id
  FROM vuln_finding f
 WHERE f.ecosystem = 'OCI' AND f.status IN ('needs_triage', 'confirmed', 'blocked', 'dismissed')
   AND NOT EXISTS (SELECT 1 FROM vuln_image_scan s
                    WHERE s.repo_id = f.repo_id AND s.name = f.package_name AND encode(s.digest, 'hex') = substr(f.version, 8))
 ORDER BY f.id
 LIMIT sqlc.arg(max_rows)::int;

-- name: VulnImageSBOMGet :many
SELECT sbom FROM vuln_image_sbom WHERE digest = sqlc.arg(digest) AND analyser = sqlc.arg(analyser)::text;

-- name: VulnImageSBOMPut :exec
INSERT INTO vuln_image_sbom (digest, analyser, sbom) VALUES (sqlc.arg(digest), sqlc.arg(analyser)::text, sqlc.arg(sbom))
ON CONFLICT (digest, analyser) DO NOTHING;

-- name: VulnImageSBOMPrune :execrows
-- The analyses no manifest refers to any more, and those of an analyser this release does not run.
DELETE FROM vuln_image_sbom b
 WHERE b.analyser <> sqlc.arg(analyser)::text
    OR NOT EXISTS (SELECT 1 FROM vuln_image_scan s WHERE s.digest = b.digest);

-- name: VulnImageDBGet :many
SELECT d.digest::text AS digest, d.blob_sha256 AS blob_sha256, d.size::bigint AS size,
       (extract(epoch FROM d.updated_at) * 1000000)::bigint AS updated_us,
       (extract(epoch FROM COALESCE(d.next_update, d.updated_at)) * 1000000)::bigint AS next_update_us
  FROM vuln_image_db d;

-- name: VulnImageDBPut :exec
INSERT INTO vuln_image_db (digest, blob_sha256, size, updated_at, next_update)
VALUES (sqlc.arg(digest)::text, sqlc.arg(blob_sha256), sqlc.arg(size)::bigint, to_timestamp(sqlc.arg(updated_us)::bigint / 1000000.0),
        to_timestamp(sqlc.arg(next_update_us)::bigint / 1000000.0))
ON CONFLICT (singleton) DO UPDATE
   SET digest = EXCLUDED.digest, blob_sha256 = EXCLUDED.blob_sha256, size = EXCLUDED.size, updated_at = EXCLUDED.updated_at,
       next_update = EXCLUDED.next_update, imported_at = now();

-- name: VulnImageUpsertAdvisory :exec
-- The advisory of an image finding, as the analyser reported it: the findings API, the detail and the filters read the same table as
-- for the package formats. No affected rows: nothing matches it by name.
INSERT INTO vuln_advisory (ecosystem, id, canonical_id, modified, published, kind, summary, details, severity, severity_source, cvss_version, cvss_vector,
                           cvss_score, "references", cwe_ids, record_sha256)
VALUES ('OCI', sqlc.arg(id)::text, sqlc.arg(id)::text, to_timestamp(sqlc.arg(modified_us)::bigint / 1000000.0),
        CASE WHEN sqlc.arg(published_us)::bigint = 0 THEN NULL ELSE to_timestamp(sqlc.arg(published_us)::bigint / 1000000.0) END,
        'vulnerability', sqlc.arg(summary)::text, sqlc.arg(details)::text, sqlc.arg(severity)::text, sqlc.arg(severity_source)::text,
        NULLIF(sqlc.arg(cvss_version)::text, ''), NULLIF(sqlc.arg(cvss_vector)::text, ''), NULLIF(sqlc.arg(cvss_score)::float8, -1)::numeric(3,1),
        sqlc.arg(refs)::jsonb, sqlc.arg(cwe_ids)::text[], sqlc.arg(record_sha256))
ON CONFLICT (ecosystem, id) DO UPDATE
   SET modified = EXCLUDED.modified, published = EXCLUDED.published, summary = EXCLUDED.summary, details = EXCLUDED.details,
       severity = EXCLUDED.severity, severity_source = EXCLUDED.severity_source, cvss_version = EXCLUDED.cvss_version,
       cvss_vector = EXCLUDED.cvss_vector, cvss_score = EXCLUDED.cvss_score, "references" = EXCLUDED."references", cwe_ids = EXCLUDED.cwe_ids,
       record_sha256 = EXCLUDED.record_sha256
 WHERE vuln_advisory.record_sha256 <> EXCLUDED.record_sha256;

-- name: VulnImageKnownExploited :many
-- The identifiers among the advisories of a detection that the known-exploited catalog lists (R-VULN-008).
SELECT cve_id::text AS cve_id FROM vuln_kev WHERE cve_id = ANY(sqlc.arg(ids)::text[]);

-- name: VulnImageFindings :many
-- The findings of one manifest, resolved ones too, in the shape of the evaluation's reads.
SELECT f.id::text AS id, f.repo_id::bigint AS repo_id, f.package_name::text AS package_name, f.version::text AS version,
       f.advisory_id::text AS advisory_id, COALESCE(f.package_id, 0)::bigint AS package_id, COALESCE(f.version_id, 0)::bigint AS version_id,
       f.kind::text AS kind, f.severity::text AS severity, COALESCE(f.cvss_score, -1)::float8 AS cvss_score, f.known_exploited::boolean AS known_exploited,
       f.fix_available::boolean AS fix_available, f.fixed_versions::text[] AS fixed_versions, f.status::text AS status,
       COALESCE(f.status_before_resolution, '')::text AS status_before_resolution, COALESCE(f.resolved_reason, '')::text AS resolved_reason,
       (extract(epoch FROM f.advisory_modified) * 1000000)::bigint AS advisory_modified_us, f.revision::bigint AS revision
  FROM vuln_finding f
 WHERE f.repo_id = sqlc.arg(repo_id)::bigint AND f.ecosystem = 'OCI' AND f.package_name = sqlc.arg(name)::text AND f.version = sqlc.arg(version)::text;

-- name: VulnImagePackageID :many
SELECT p.id::bigint AS id FROM packages p WHERE p.repo_id = sqlc.arg(repo_id)::bigint AND p.name = sqlc.arg(name)::text;

-- name: VulnImageSetComponents :exec
UPDATE vuln_finding SET components = sqlc.arg(components)::jsonb
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND ecosystem = 'OCI' AND package_name = sqlc.arg(name)::text AND version = sqlc.arg(version)::text
   AND advisory_id = sqlc.arg(advisory_id)::text AND components <> sqlc.arg(components)::jsonb;

-- name: VulnImageVerdict :one
-- What a pull of a manifest needs to know (R-VULN-015): the finding that blocks the manifest in the repository, if any, and how far the
-- manifest has been scanned. One statement, the finding's unique index and the scan row's key.
SELECT COALESCE(blk.id::text, '')::text AS finding_id, COALESCE(blk.advisory_id, '')::text AS advisory_id,
       COALESCE(blk.kind, '')::text AS kind, COALESCE(blk.status, '')::text AS status, COALESCE(s.state, '')::text AS scan_state,
       (s.db_digest IS NOT NULL)::boolean AS detected
  FROM (SELECT 1) AS one
  LEFT JOIN vuln_image_scan s ON s.repo_id = sqlc.arg(repo_id)::bigint AND s.name = sqlc.arg(name)::text AND s.digest = sqlc.arg(digest)
  LEFT JOIN LATERAL (
        SELECT f.id, f.advisory_id, f.kind, f.status
          FROM vuln_finding f
         WHERE f.repo_id = sqlc.arg(repo_id)::bigint AND f.ecosystem = 'OCI' AND f.package_name = sqlc.arg(name)::text
           AND f.version = sqlc.arg(version)::text
           AND f.status IN ('needs_triage', 'confirmed', 'blocked')
           AND (f.status = 'blocked' OR (f.kind = 'malicious' AND sqlc.arg(block_malicious)::boolean))
         ORDER BY (f.kind = 'malicious' AND sqlc.arg(block_malicious)::boolean) DESC, f.severity_rank DESC, f.advisory_id
         LIMIT 1) AS blk ON true;

-- name: VulnImageManifests :many
-- The manifests a tag stands for in the repositories (the tag's own and, for an index, the children it names), with how far each is
-- scanned: the version views of the report (R-VULN-020). The tag item's blob is the manifest it points at (R-OCI-007). An index has no
-- scan row of its own and is left out once it has children.
WITH tagged AS (
    SELECT p.repo_id, p.name, i.blob_sha256 AS digest, ''::text AS platform
      FROM packages p
      JOIN package_versions pv ON pv.package_id = p.id
      JOIN items i ON i.id = pv.lead_item_id
     WHERE p.repo_id = ANY(sqlc.arg(repos)::bigint[]) AND p.name = sqlc.arg(name)::text AND pv.version = sqlc.arg(tag)::text AND pv.trashed_at IS NULL
), images AS (
    SELECT * FROM tagged
    UNION
    SELECT t.repo_id, t.name, r.target, COALESCE(r.platform, '')
      FROM tagged t JOIN oci_references r ON r.repo_id = t.repo_id AND r.name = t.name AND r.manifest = t.digest AND r.kind = 'manifest'
)
SELECT g.repo_id::bigint AS repo_id, ('sha256:' || encode(g.digest, 'hex'))::text AS digest, g.platform::text AS platform,
       COALESCE(s.state, '')::text AS state, COALESCE(s.error_code, '')::text AS error_code,
       COALESCE((extract(epoch FROM s.scanned_at) * 1000000)::bigint, 0)::bigint AS scanned_us,
       COALESCE(s.os_family, '')::text AS os_family, COALESCE(s.os_name, '')::text AS os_name, COALESCE(s.os_eosl, false)::boolean AS os_eosl,
       COALESCE(s.packages, 0)::int AS packages
  FROM images g
  LEFT JOIN vuln_image_scan s ON s.repo_id = g.repo_id AND s.name = g.name AND s.digest = g.digest
 WHERE g.platform <> '' OR NOT EXISTS (SELECT 1 FROM images c WHERE c.repo_id = g.repo_id AND c.platform <> '')
 ORDER BY g.repo_id, g.platform, g.digest;

-- name: VulnImageTagCounts :many
-- The open findings of an image name the caller can read, counted per tag and severity: a finding counts for the tag whose manifest it
-- names or whose index names that manifest (R-VULN-020).
WITH tagged AS (
    SELECT p.repo_id, p.name, pv.version AS tag, i.blob_sha256 AS digest
      FROM packages p
      JOIN repositories r ON r.id = p.repo_id
      JOIN package_versions pv ON pv.package_id = p.id
      JOIN items i ON i.id = pv.lead_item_id
     WHERE r.format = 'oci' AND p.name = sqlc.arg(name)::text AND pv.trashed_at IS NULL
), images AS (
    SELECT repo_id, name, tag, digest FROM tagged
    UNION
    SELECT t.repo_id, t.name, t.tag, r.target
      FROM tagged t JOIN oci_references r ON r.repo_id = t.repo_id AND r.name = t.name AND r.manifest = t.digest AND r.kind = 'manifest'
)
SELECT g.tag::text AS version, f.severity::text AS severity, count(DISTINCT f.id)::bigint AS n
  FROM images g
  JOIN vuln_finding f ON f.repo_id = g.repo_id AND f.ecosystem = 'OCI' AND f.package_name = g.name AND f.version = ('sha256:' || encode(g.digest, 'hex'))
 WHERE f.status IN ('needs_triage', 'confirmed', 'blocked')
   AND (NOT sqlc.arg(has_repos)::boolean OR f.repo_id = ANY(sqlc.arg(repos)::bigint[]))
   AND (f.repo_id = ANY(sqlc.arg(whole)::bigint[])
          OR concat_ws(chr(31), f.repo_id::text, f.package_name, g.tag) = ANY(sqlc.arg(rows)::text[])
          OR concat_ws(chr(31), f.repo_id::text, f.package_name) = ANY(sqlc.arg(package_rows)::text[]))
 GROUP BY g.tag, f.severity
 ORDER BY g.tag, f.severity;

-- name: VulnImageComponentsOf :many
-- The packages inside the image that findings name, for the findings of an image in a page of the report (R-VULN-012).
SELECT f.id::text AS id, f.components AS components FROM vuln_finding f WHERE f.id = ANY(sqlc.arg(ids)::text[]::uuid[]) AND f.ecosystem = 'OCI';
