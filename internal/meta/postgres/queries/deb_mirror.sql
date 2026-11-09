-- The statements of the Debian format's mirror pass (R-DEB-024, Mirror discovery, Q-DEB-91): the names and files its parsed indexes list in the
-- scope of a pass. The scope arrives as the keys "<dir>|<index path>" of the indexes in scope, decided in Go (rows.InScope), and only the
-- releases that a key of the current set verifies count.

-- name: DebMirrorVerifiedDirs :many
-- The release directories among dirs whose stored release is verified by a key.
SELECT DISTINCT dir
  FROM deb_remote_releases
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND dir = ANY(sqlc.arg(dirs)::text[]) AND document <> 'absent' AND verified_by IS NOT NULL
 ORDER BY dir;

-- name: DebMirrorNames :many
-- The distinct package names the indexes in scope list, in byte order after the cursor.
SELECT DISTINCT e.package
  FROM deb_remote_entries e
 WHERE e.repo_id = sqlc.arg(repo_id)::bigint
   AND (e.dir || '|' || e.index_path) = ANY(sqlc.arg(keys)::text[])
   AND e.package > sqlc.arg(after)::text
 ORDER BY e.package
 LIMIT sqlc.arg(max_rows)::integer;

-- name: DebMirrorEntries :many
-- The files the indexes in scope list for a package name.
SELECT e.dir, e.index_path, e.package, e.version, e.architecture, e.path, e.sha256, e.size
  FROM deb_remote_entries e
 WHERE e.repo_id = sqlc.arg(repo_id)::bigint
   AND (e.dir || '|' || e.index_path) = ANY(sqlc.arg(keys)::text[])
   AND e.package = sqlc.arg(name)::text
 ORDER BY e.version, e.path;
