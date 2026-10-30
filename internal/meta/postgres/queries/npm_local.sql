-- The statements of the npm format's local repositories (internal/formatdata/npm;
-- R-NPM-006 to R-NPM-008, R-NPM-016). The package is a domain package and imports no
-- driver types, so a time travels as text and is cast to timestamptz here, a principal
-- id as text cast to uuid, and a value that may be absent as the empty string.
--
-- Every write runs in the transaction of the publish or of the change, under the lock
-- of the package's generic row (packages), which the core writer took: the writes of
-- one package serialize on it (R-NPM-007 step 5).

-- name: NPMInsertPackage :execrows
-- The npm row of a package. The unique (repo_id, name_fold) is the look-alike rule of
-- R-NPM-002: of two concurrent first publishes of acme-ui and acme_ui through
-- different replicas the second blocks on the index until the first commits, then
-- inserts nothing, and the caller finds it has no row of its own. A package that has
-- its row already conflicts on the primary key and inserts nothing as well.
INSERT INTO npm_packages (package_id, repo_id, name_fold, modified_at)
VALUES (sqlc.arg(package_id)::bigint, sqlc.arg(repo_id)::bigint, sqlc.arg(name_fold)::text, sqlc.arg(at)::text::timestamptz)
ON CONFLICT DO NOTHING;

-- name: NPMReadTags :many
-- The dist-tags of a package, locked. No row: the package has no npm row yet.
SELECT dist_tags::text AS dist_tags
  FROM npm_packages
 WHERE package_id = sqlc.arg(package_id)::bigint
   FOR UPDATE;

-- name: NPMSetTags :execrows
-- Stores the dist-tags a change produced. modified_at moves forward by at least a
-- millisecond, so two changes in one clock tick still tell a client the document
-- changed (R-NPM-012).
UPDATE npm_packages
   SET dist_tags = sqlc.arg(dist_tags)::text::jsonb,
       modified_at = greatest(modified_at + interval '1 millisecond', sqlc.arg(at)::text::timestamptz)
 WHERE package_id = sqlc.arg(package_id)::bigint;

-- name: NPMTombstoned :one
SELECT EXISTS (
    SELECT 1 FROM npm_tombstones
     WHERE repo_id = sqlc.arg(repo_id)::bigint AND name = sqlc.arg(name)::text AND version = sqlc.arg(version)::text
)::boolean AS tombstoned;

-- name: NPMInsertVersion :exec
INSERT INTO npm_versions (version_id, manifest, integrity, shasum, file_count, unpacked_size,
                          has_install_script, has_shrinkwrap, readme, readme_filename, publish_tag, published_at)
VALUES (sqlc.arg(version_id)::bigint, sqlc.arg(manifest)::text::jsonb, sqlc.arg(integrity)::text, sqlc.arg(shasum)::text,
        sqlc.arg(file_count)::integer, sqlc.arg(unpacked_size)::bigint,
        sqlc.arg(has_install_script)::boolean, sqlc.arg(has_shrinkwrap)::boolean,
        NULLIF(sqlc.arg(readme)::text, ''), NULLIF(sqlc.arg(readme_filename)::text, ''),
        sqlc.arg(publish_tag)::text, sqlc.arg(published_at)::text::timestamptz);

-- name: NPMVersionFacts :many
-- What the immutability and the acknowledged-retry rules read of a live version. The
-- clock is PostgreSQL's, read in the same statement as published_at (R-NPM-007 step 4).
SELECT nv.integrity,
       nv.publish_tag,
       COALESCE(pv.created_by::text, '')::text AS published_by,
       nv.published_at,
       clock_timestamp()::timestamptz AS now
  FROM npm_versions nv
  JOIN package_versions pv ON pv.id = nv.version_id
  JOIN packages p ON p.id = pv.package_id
 WHERE p.repo_id = sqlc.arg(repo_id)::bigint
   AND p.name = sqlc.arg(name)::text
   AND pv.version = sqlc.arg(version)::text
   AND pv.trashed_at IS NULL;

-- name: NPMInsertTombstone :one
-- The tombstone of a live version, with the integrity its row holds (R-NPM-008). It
-- outlives the version row, the item and its permanent removal. A version that is
-- tombstoned already (withheld by a restore's replay, R-NPM-047) keeps its first
-- tombstone, and its integrity comes back all the same.
INSERT INTO npm_tombstones (repo_id, name, version, integrity, source, unpublished_at, unpublished_by)
SELECT sqlc.arg(repo_id)::bigint, sqlc.arg(name)::text, pv.version, nv.integrity, sqlc.arg(source)::text,
       sqlc.arg(at)::text::timestamptz, sqlc.arg(by)::text::uuid
  FROM package_versions pv
  JOIN npm_versions nv ON nv.version_id = pv.id
 WHERE pv.id = sqlc.arg(version_id)::bigint
ON CONFLICT (repo_id, name, version) DO UPDATE SET source = npm_tombstones.source
RETURNING integrity;

-- >>> M2 npm-read
-- The reads of package documents, dist-tags and tarball state (R-NPM-010, R-NPM-014,
-- R-NPM-015, R-NPM-016, R-NPM-017, R-NPM-047).

-- name: NPMLocalDocument :many
-- The rows of a package document in one statement, so the revision, the tags and the
-- versions belong to one snapshot (R-NPM-010, R-ARCH-012). A version is in the document
-- when it is neither trashed nor tombstoned: a version a restore's replay withheld is
-- stored and tombstoned (R-NPM-047). The README is read only of the version the latest
-- tag points to. A package whose versions are all gone yields one row with an empty
-- version; a package without an npm row yields none.
SELECT p.id AS package_id,
       p.rev_seq,
       p.created_at,
       n.modified_at,
       n.dist_tags::text AS dist_tags,
       COALESCE(pv.version, '')::text AS version,
       COALESCE(nv.manifest::text, '')::text AS manifest,
       COALESCE(nv.deprecated, '')::text AS deprecated,
       COALESCE(nv.published_at, p.created_at)::timestamptz AS published_at,
       CASE WHEN pv.version = n.dist_tags ->> 'latest' THEN COALESCE(nv.readme, '') ELSE '' END::text AS readme,
       CASE WHEN pv.version = n.dist_tags ->> 'latest' THEN COALESCE(nv.readme_filename, '') ELSE '' END::text AS readme_filename
  FROM packages p
  JOIN npm_packages n ON n.package_id = p.id
  LEFT JOIN package_versions pv ON pv.package_id = p.id AND pv.trashed_at IS NULL
        AND NOT EXISTS (SELECT 1 FROM npm_tombstones t WHERE t.repo_id = p.repo_id AND t.name = p.name AND t.version = pv.version)
  LEFT JOIN npm_versions nv ON nv.version_id = pv.id
 WHERE p.repo_id = sqlc.arg(repo_id)::bigint AND p.name = sqlc.arg(name)::text
 ORDER BY pv.id;

-- name: NPMLocalVersion :many
-- One version of a document, named by its number or by a dist-tag (R-NPM-014), with the
-- tags and the revision of the same snapshot. No row: the package or the version is not
-- in the document.
SELECT p.id AS package_id,
       p.rev_seq,
       p.created_at,
       n.modified_at,
       n.dist_tags::text AS dist_tags,
       pv.version,
       nv.manifest::text AS manifest,
       COALESCE(nv.deprecated, '')::text AS deprecated,
       nv.published_at
  FROM packages p
  JOIN npm_packages n ON n.package_id = p.id
  JOIN package_versions pv ON pv.package_id = p.id AND pv.trashed_at IS NULL
  JOIN npm_versions nv ON nv.version_id = pv.id
 WHERE p.repo_id = sqlc.arg(repo_id)::bigint AND p.name = sqlc.arg(name)::text
   AND (pv.version = sqlc.arg(version)::text OR pv.version = n.dist_tags ->> sqlc.arg(tag)::text)
   AND NOT EXISTS (SELECT 1 FROM npm_tombstones t WHERE t.repo_id = p.repo_id AND t.name = p.name AND t.version = pv.version);

-- name: NPMDistTags :many
-- The dist-tags of a package with its revision, and whether the document holds a
-- version. No row: the package has no npm row.
SELECT p.rev_seq,
       n.dist_tags::text AS dist_tags,
       EXISTS (
           SELECT 1 FROM package_versions pv
            WHERE pv.package_id = p.id AND pv.trashed_at IS NULL
              AND NOT EXISTS (SELECT 1 FROM npm_tombstones t WHERE t.repo_id = p.repo_id AND t.name = p.name AND t.version = pv.version)
       )::boolean AS has_versions
  FROM packages p
  JOIN npm_packages n ON n.package_id = p.id
 WHERE p.repo_id = sqlc.arg(repo_id)::bigint AND p.name = sqlc.arg(name)::text;

-- name: NPMLiveVersions :many
-- The versions of a locked package that its document holds, in publish order: not
-- trashed, not tombstoned. The unpublish and the tag writes decide on these.
SELECT pv.id, pv.version
  FROM package_versions pv
  JOIN packages p ON p.id = pv.package_id
 WHERE pv.package_id = sqlc.arg(package_id)::bigint AND pv.trashed_at IS NULL
   AND NOT EXISTS (SELECT 1 FROM npm_tombstones t WHERE t.repo_id = p.repo_id AND t.name = p.name AND t.version = pv.version)
 ORDER BY pv.id;

-- name: NPMDeleteTombstone :execrows
-- Removes the tombstone of a version that is stored again with the integrity the
-- tombstone holds (a restore from the trash can, R-NPM-008): the same bytes come back,
-- never new ones.
DELETE FROM npm_tombstones t
 USING package_versions pv, npm_versions nv
 WHERE pv.id = sqlc.arg(version_id)::bigint AND nv.version_id = pv.id
   AND t.repo_id = sqlc.arg(repo_id)::bigint AND t.name = sqlc.arg(name)::text AND t.version = pv.version
   AND t.integrity = nv.integrity;
-- <<< M2 npm-read
