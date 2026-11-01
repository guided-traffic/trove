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
-- The tombstone of a live version, with the SHA-512 of its .nupkg (R-NUGET-006). It
-- outlives the version row, the item and its permanent removal. A number that is
-- tombstoned already keeps its first tombstone, and its digest comes back all the same.
INSERT INTO nuget_tombstone (repository_id, lower_id, lower_version, sha512, deleted_at, deleted_by, kind)
SELECT sqlc.arg(repository_id)::bigint, p.name, pv.version, b.sha512,
       sqlc.arg(at)::text::timestamptz, sqlc.arg(by)::text::uuid, 'removed'
  FROM package_versions pv
  JOIN packages p ON p.id = pv.package_id
  JOIN items i ON i.id = pv.lead_item_id
  JOIN blobs b ON b.sha256 = i.blob_sha256
 WHERE pv.id = sqlc.arg(version_id)::bigint
ON CONFLICT (repository_id, lower_id, lower_version) DO UPDATE SET kind = nuget_tombstone.kind
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

-- name: NuGetSetListed :execrows
-- Unlists or relists a version (R-NUGET-007). The caller bumps the package revision in
-- the same transaction.
UPDATE nuget_version
   SET listed = sqlc.arg(listed)::boolean
 WHERE version_id = sqlc.arg(version_id)::bigint;

-- name: NuGetInsertSymbolPackage :execrows
-- The symbol package of a version (R-NUGET-020). A repeated push of the same version
-- conflicts on the primary key, inserts nothing and leaves the stored item alone.
INSERT INTO nuget_symbol_package (version_id, item_id)
VALUES (sqlc.arg(version_id)::bigint, sqlc.arg(item_id)::bigint)
ON CONFLICT (version_id) DO NOTHING;
