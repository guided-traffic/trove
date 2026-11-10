-- trove:phase expand
--
-- The tags a remote OCI repository holds, as versions the package screens list (R-OCI-036, Q-OCI-215). A remote's tag is a metadata
-- cache entry `<name>/_tags/<tag>` whose document is the manifest the tag points at (R-OCI-007), so it owns no package_versions row and
-- the fill transaction writes none. The listing reads the entries themselves: a tag is listed while its entry is stored and the upstream
-- has not marked it gone (R-REPO-029), which is exactly when the remote address serves it, and an entry that is evicted or replaced by a
-- fill of a moved tag changes the listing in the same statement that changes the cache. Nothing is written by the view and nothing is
-- kept in step.
--
-- id is the negative of the entry's id: a version of the view is told apart from a package_versions row by its sign, so the format's
-- reads of a version by id (the digest, the size, the platforms) find a tag of either kind and the two id spaces never meet.
-- The manifest row of (repository, name, digest) must exist: it is what a tag's digest, size and platforms are read from, and it is
-- written in the transaction of the fill (R-OCI-007), so an entry without one is a fill that did not commit.
CREATE VIEW oci_remote_tags AS
SELECT (-e.id)::bigint            AS id,
       p.id                     AS package_id,
       e.repo_id                AS repo_id,
       p.name                   AS name,
       m.tag::text              AS version,
       e.fetched_at             AS created_at,
       e.blob_sha256            AS manifest_sha256
  FROM remote_cache_entries e
  JOIN repositories r ON r.id = e.repo_id AND r.class = 'remote' AND r.format = 'oci'
 CROSS JOIN LATERAL (SELECT regexp_match(e.path, '^(.+)/_tags/([^/]+)$') AS parts) x
 CROSS JOIN LATERAL (SELECT x.parts[1] AS name, x.parts[2] AS tag) m
  JOIN packages p ON p.repo_id = e.repo_id AND p.name = m.name
  JOIN oci_manifests om ON om.repo_id = e.repo_id AND om.name = m.name AND om.digest = e.blob_sha256
 WHERE e.kind = 'metadata'
   AND e.variant = ''
   AND e.upstream_gone_at IS NULL
   AND e.path LIKE '%/\_tags/%'
   AND x.parts IS NOT NULL;

-- The versions the package screens list: the rows of package_versions and the tags of a remote OCI repository (R-API-029). A tag of
-- the second kind has no lead item and is never in the trash.
CREATE VIEW package_version_listing AS
SELECT pv.id, pv.package_id, pv.version, pv.created_at, pv.lead_item_id, pv.trashed_at
  FROM package_versions pv
UNION ALL
SELECT t.id, t.package_id, t.version, t.created_at, NULL::bigint, NULL::timestamptz
  FROM oci_remote_tags t;
