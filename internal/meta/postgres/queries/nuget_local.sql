-- The statements of the NuGet format's local repositories that the data port needs at
-- minimum (internal/formatdata/nuget; R-NUGET-005, R-NUGET-006, R-NUGET-020). The
-- package is a domain package and imports no driver types, so a time travels as text
-- and is cast to timestamptz here, a principal id as text cast to uuid.
--
-- Every write runs in the transaction of the push or of the change, under the lock of
-- the package's generic row (packages), which the core writer took: the writes of one
-- package serialize on it, and the version and tombstone checks read only after that
-- lock (R-NUGET-006).

-- name: NuGetInsertVersion :exec
-- The NuGet row of a version whose core rows the writer inserted. search_doc is built
-- here from the parts the format hands over, with the 'simple' configuration (no
-- stemming, no stop words: package IDs and tags are not prose) and the weights of the
-- ranking: ID tokens A, title and tags B, authors and summary C, description D
-- (R-NUGET-010). id_tokens is the ID split at its separators, space separated.
INSERT INTO nuget_version (version_id, repository_id, lower_id, declared_id, original_version, sort_key,
                           is_prerelease, is_semver2, listed, published_at, package_types, metadata,
                           search_doc, origin)
VALUES (sqlc.arg(version_id)::bigint, sqlc.arg(repository_id)::bigint, sqlc.arg(lower_id)::text,
        sqlc.arg(declared_id)::text, sqlc.arg(original_version)::text, sqlc.arg(sort_key)::bytea,
        sqlc.arg(is_prerelease)::boolean, sqlc.arg(is_semver2)::boolean, true,
        sqlc.arg(published_at)::text::timestamptz, sqlc.arg(package_types)::text[],
        sqlc.arg(metadata)::text::jsonb,
        setweight(to_tsvector('simple', sqlc.arg(id_tokens)::text), 'A')
        || setweight(to_tsvector('simple', sqlc.arg(title)::text || ' ' || sqlc.arg(tags)::text), 'B')
        || setweight(to_tsvector('simple', sqlc.arg(authors)::text || ' ' || sqlc.arg(summary)::text), 'C')
        || setweight(to_tsvector('simple', sqlc.arg(description)::text), 'D'),
        sqlc.arg(origin)::text);

-- name: NuGetVersionFacts :many
-- What the immutability and the acknowledged-retry rules read of a stored version: the
-- listing state, the SHA-512 of the .nupkg (the lead item's blob) and the publisher, on
-- PostgreSQL's clock (R-NUGET-006). No row: the version is not stored or is trashed.
SELECT nv.listed,
       b.sha512,
       COALESCE(pv.created_by::text, '')::text AS published_by,
       nv.published_at,
       clock_timestamp()::timestamptz AS now
  FROM package_versions pv
  JOIN packages p ON p.id = pv.package_id
  JOIN nuget_version nv ON nv.version_id = pv.id
  JOIN items i ON i.id = pv.lead_item_id
  JOIN blobs b ON b.sha256 = i.blob_sha256
 WHERE p.repo_id = sqlc.arg(repository_id)::bigint
   AND p.name = sqlc.arg(lower_id)::text
   AND pv.version = sqlc.arg(lower_version)::text
   AND pv.trashed_at IS NULL;

-- name: NuGetTombstone :many
-- The tombstone of a version number, if there is one: the SHA-512 of the removed (or,
-- for a reserved number, the journaled) .nupkg and its kind (R-NUGET-006).
SELECT sha512, kind
  FROM nuget_tombstone
 WHERE repository_id = sqlc.arg(repository_id)::bigint
   AND lower_id = sqlc.arg(lower_id)::text
   AND lower_version = sqlc.arg(lower_version)::text;

-- name: NuGetInsertTombstone :one
-- The tombstone of a version that is being removed, with the SHA-512 of its .nupkg
-- (R-NUGET-006). The hook that learns of the removal runs after the item rows are gone,
-- so the digest is read from the blob row by the SHA-256 the removal carries; the blob row
-- outlives the transaction (its collection is queued). It outlives the version row, the
-- item and its permanent removal. A number that is tombstoned already keeps its first
-- tombstone, and its digest comes back all the same.
INSERT INTO nuget_tombstone (repository_id, lower_id, lower_version, sha512, deleted_at, deleted_by, kind)
SELECT sqlc.arg(repository_id)::bigint, sqlc.arg(lower_id)::text, sqlc.arg(lower_version)::text, b.sha512,
       sqlc.arg(at)::text::timestamptz, sqlc.arg(by)::text::uuid, 'removed'
  FROM blobs b
 WHERE b.sha256 = sqlc.arg(sha256)::bytea
ON CONFLICT (repository_id, lower_id, lower_version) DO UPDATE SET kind = nuget_tombstone.kind
RETURNING sha512;

-- name: NuGetPromoteReserved :many
-- A delete in delete mode of a number that a restore's replay reserved: the tombstone
-- becomes a removal, so that the journaled bytes are refused as well (R-NUGET-007).
UPDATE nuget_tombstone
   SET kind = 'removed', deleted_at = clock_timestamp(), deleted_by = sqlc.arg(by)::text::uuid
 WHERE repository_id = sqlc.arg(repository_id)::bigint
   AND lower_id = sqlc.arg(lower_id)::text
   AND lower_version = sqlc.arg(lower_version)::text
   AND kind = 'reserved'
RETURNING sha512;

-- name: NuGetDeleteTombstone :execrows
-- Removes the tombstone of a version that is stored again with the SHA-512 the
-- tombstone holds: a restore from the trash can, or the identical push against a
-- reserved number (R-NUGET-006, Q-NUGET-10). The same bytes come back, never new ones.
DELETE FROM nuget_tombstone t
 USING package_versions pv, packages p, items i, blobs b
 WHERE pv.id = sqlc.arg(version_id)::bigint AND p.id = pv.package_id
   AND i.id = pv.lead_item_id AND b.sha256 = i.blob_sha256
   AND t.repository_id = p.repo_id AND t.lower_id = p.name AND t.lower_version = pv.version
   AND t.sha512 = b.sha512;

-- name: NuGetFindVersion :many
-- The live version of a number with its listing state, read under the package row lock
-- (R-NUGET-007). It starts at the package so that the unique index of (repo_id, name)
-- finds it, whatever the listing of the version is.
SELECT nv.version_id, nv.listed
  FROM packages p
  JOIN package_versions pv ON pv.package_id = p.id
  JOIN nuget_version nv ON nv.version_id = pv.id
 WHERE p.repo_id = sqlc.arg(repository_id)::bigint
   AND p.name = sqlc.arg(lower_id)::text
   AND pv.version = sqlc.arg(lower_version)::text
   AND pv.trashed_at IS NULL;

-- name: NuGetSetListed :execrows
-- Unlists or relists a version (R-NUGET-007). A version that is in the state asked for
-- already is not touched, so the count says whether anything changed. The caller bumps
-- the package revision in the same transaction.
UPDATE nuget_version
   SET listed = sqlc.arg(listed)::boolean
 WHERE version_id = sqlc.arg(version_id)::bigint
   AND listed IS DISTINCT FROM sqlc.arg(listed)::boolean;

-- name: NuGetServeState :one
-- What a GET of a file of a version decides on, in one statement: whether the repository
-- holds a live version row, whether it is listed, and whether the number is tombstoned
-- (R-NUGET-007).
WITH v AS (
    SELECT nv.listed
      FROM packages p
      JOIN package_versions pv ON pv.package_id = p.id
      JOIN nuget_version nv ON nv.version_id = pv.id
     WHERE p.repo_id = sqlc.arg(repository_id)::bigint
       AND p.name = sqlc.arg(lower_id)::text
       AND pv.version = sqlc.arg(lower_version)::text
       AND pv.trashed_at IS NULL
)
SELECT EXISTS (SELECT 1 FROM v) AS stored,
       COALESCE((SELECT v.listed FROM v LIMIT 1), false)::boolean AS listed,
       EXISTS (SELECT 1 FROM nuget_tombstone t
                WHERE t.repository_id = sqlc.arg(repository_id)::bigint
                  AND t.lower_id = sqlc.arg(lower_id)::text
                  AND t.lower_version = sqlc.arg(lower_version)::text) AS tombstoned;

-- name: NuGetVersions :many
-- Every stored version of an ID, listed or not, ascending by sort key: the version list
-- of the flat container (R-NUGET-008). A trashed version is invisible (R-NUGET-005).
SELECT nv.version_id, pv.version, pv.display_version, nv.declared_id, nv.listed,
       nv.is_prerelease, nv.is_semver2, nv.published_at
  FROM packages p
  JOIN package_versions pv ON pv.package_id = p.id
  JOIN nuget_version nv ON nv.version_id = pv.id
 WHERE p.repo_id = sqlc.arg(repository_id)::bigint
   AND p.name = sqlc.arg(lower_id)::text
   AND pv.trashed_at IS NULL
 ORDER BY nv.sort_key;

-- name: NuGetRegistration :many
-- What the registration documents of an ID render from, in one statement: the package's
-- revision and display name on every row, and each visible version with its metadata,
-- ascending by sort key (R-NUGET-009). The revision is read in the same snapshot as the
-- versions, so an ETag never names a revision the rows do not belong to.
SELECT p.rev_seq, p.display_name,
       nv.version_id, pv.version, pv.display_version, nv.declared_id, nv.listed,
       nv.is_prerelease, nv.is_semver2, nv.published_at, nv.package_types, nv.metadata::text AS metadata
  FROM packages p
  JOIN package_versions pv ON pv.package_id = p.id
  JOIN nuget_version nv ON nv.version_id = pv.id
 WHERE p.repo_id = sqlc.arg(repository_id)::bigint
   AND p.name = sqlc.arg(lower_id)::text
   AND pv.trashed_at IS NULL
 ORDER BY nv.sort_key;

-- name: NuGetInsertSymbolPackage :execrows
-- The symbol package of a version (R-NUGET-020). A repeated push of the same version
-- conflicts on the primary key, inserts nothing and leaves the stored item alone.
INSERT INTO nuget_symbol_package (version_id, item_id)
VALUES (sqlc.arg(version_id)::bigint, sqlc.arg(item_id)::bigint)
ON CONFLICT (version_id) DO NOTHING;
