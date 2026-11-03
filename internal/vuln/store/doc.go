// Package store holds the sqlc wrappers of the tables of the vulnerability scanning (migration 00035_vulnerabilities.sql):
// vuln_source_state, vuln_advisory, vuln_affected, vuln_pending_evaluation, vuln_kev, vuln_claimed_match, vuln_finding and
// vuln_finding_event. It is a domain package that uses meta/postgres/q, as internal/ci does.
//
// # One file per owner
//
// The statements are sqlc queries, and a query file belongs to one work package, so that the packages run in parallel
// without touching each other's files (docs/developer/vulnerabilities.md):
//
//	queries/vuln_sync.sql      work package A: the source state, advisories, affected entries, pending evaluation, KEV
//	queries/vuln_evaluate.sql  work package B: findings, their events, claimed matches, the retention
//	queries/vuln_enforce.sql   work package C: what the block check and the remote miss read
//	queries/vuln_audits.sql    work package D: the advisories of the client audits
//	queries/vuln_api.sql       work package E: triage, the report, the export, the summary and the trend
//	queries/vuln_ops.sql       work package G: the gauges of R-VULN-024
//
// (the files are under internal/meta/postgres/queries). The statement of a package may read any table, and an INSERT, UPDATE
// or DELETE of a table belongs to the package whose file holds it; the table's writers are listed on the contract page.
// Run `make generate` after an edit, and commit the generated internal/meta/postgres/q/vuln_<package>.sql.go.
//
// # Conventions
//
//   - Plain Go types only (no pgtype): nullable columns are COALESCEd, an optional value is NULLIF(text, ”), or 0.
//   - A write that belongs to a finding's lifecycle runs in the transaction of the change that caused it
//     (meta.Tx), so that the commit that stored a version holds its findings (R-VULN-011).
//   - The wrapper types of this package speak the leaf types of internal/vuln/advisory and the domain types of
//     internal/vuln; a q.* row type never leaves the package.
//
// The wrappers themselves are written by the work packages, one file each (sync.go, evaluate.go, ...); the contract wave lays
// the package, the convention and the empty query files.
package store
