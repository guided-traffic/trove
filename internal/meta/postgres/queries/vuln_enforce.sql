-- Statements of the vulnerability scanning, work package enforce (docs/developer/vulnerabilities.md). One file per work package;
-- every statement of this file is the package's. Plain Go types only (no pgtype): COALESCE nullable columns.

-- name: GetVersionNames :many
-- The package name and version a stored version has, for the text of a refusal (R-VULN-016). It runs only when a download is
-- refused, never on a served one (R-VULN-015 keeps the one-query budget of a download).
SELECT p.name::text AS package_name, v.version::text AS version
  FROM package_versions v
  JOIN packages p ON p.id = v.package_id
 WHERE v.id = sqlc.arg(version_id)::bigint;
