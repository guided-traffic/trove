-- The statements of the symbol package indexing job (internal/formatdata/nuget; R-NUGET-020,
-- R-NUGET-021, Q-NUGET-11). The push's own statement, NuGetInsertSymbolPackage, is in
-- nuget_local.sql: it runs in the transaction of the push.

-- name: NuGetSymbolPackageForIndex :many
-- What the indexing job of one symbol package reads: the item, the repository and the ID
-- and version of the package it belongs to, and the indexing state. No row: the version
-- (or its symbol package) is gone, and the job has nothing to do.
SELECT sp.item_id,
       p.repo_id AS repository_id,
       p.name AS lower_id,
       pv.version AS lower_version,
       sp.indexed_at,
       COALESCE(sp.index_error, '')::text AS index_error
  FROM nuget_symbol_package sp
  JOIN package_versions pv ON pv.id = sp.version_id
  JOIN packages p ON p.id = pv.package_id
 WHERE sp.version_id = sqlc.arg(version_id)::bigint;
