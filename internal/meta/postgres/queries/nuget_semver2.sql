-- The statements of the background data migration data_migration_nuget_semver2
-- (internal/formatdata/nuget/semver2.go; R-NUGET-004, R-HA-022, Q-NUGET-93): it recomputes
-- is_semver2 of the NuGet rows stored before the classification counted the dependency
-- ranges. The rule itself is Go (version.PackageIsSemver2); these statements read the rows
-- in keyset batches, write the flag under the package row lock and keep the completion in
-- system_state.

-- name: NuGetSemver2Batch :many
-- The next batch of NuGet rows after the cursor that say SemVer 1, in version id order, with
-- the package they belong to. A row that says SemVer 2 is right already: the classification
-- only ever gained a reason to say true. Trashed versions are read too, so that a version
-- restored from the trash can is right as well.
SELECT nv.version_id, nv.repository_id, nv.lower_id, nv.original_version, nv.metadata::text AS metadata,
       pv.package_id
  FROM nuget_version nv
  JOIN package_versions pv ON pv.id = nv.version_id
 WHERE nv.version_id > sqlc.arg(after)::bigint
   AND NOT nv.is_semver2
 ORDER BY nv.version_id
 LIMIT sqlc.arg(batch)::int;

-- name: NuGetSemver2LockPackage :many
-- The package row lock every writer of the package takes first (R-NUGET-006), taken before
-- the NuGet rows are written, so the recomputation and an unlist or a removal of the same ID
-- wait for each other in one order. No row: the package is gone.
SELECT id
  FROM packages
 WHERE id = sqlc.arg(id)::bigint
   FOR UPDATE;

-- name: NuGetSemver2Mark :execrows
-- Marks the versions of one package SemVer 2.0.0-specific, under its row lock. A version
-- that is gone or says SemVer 2 already is not counted, so a run that repeats a batch
-- changes nothing.
UPDATE nuget_version
   SET is_semver2 = true
 WHERE version_id = ANY(sqlc.arg(version_ids)::bigint[])
   AND NOT is_semver2;

-- name: NuGetSemver2State :many
-- The completion of the recomputation: the system_state row of its key, when it ran to the
-- end (R-OPS-024 is the table). No row: it has not.
SELECT value::text AS value
  FROM system_state
 WHERE key = sqlc.arg(key)::text;

-- name: NuGetSemver2Done :exec
-- Records the completion. A run after the key was removed (by hand, to run it once more)
-- writes it again.
INSERT INTO system_state (key, value)
VALUES (sqlc.arg(key)::text, sqlc.arg(value)::text::jsonb)
ON CONFLICT (key) DO UPDATE
   SET value = EXCLUDED.value, updated_at = now();
