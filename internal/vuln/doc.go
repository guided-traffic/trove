// Package vuln is the domain of the vulnerability scanning (docs/spec/vulnerability-scanning.md, R-VULN-001 to
// R-VULN-029; the contract page is docs/developer/vulnerabilities.md): the statuses and reasons of findings, the
// repository member `vulnerabilities` with its defaults and validation, the download policy and its verdict, the names of
// the jobs and tasks, and the three ports through which the rest of the module reaches the scanning.
//
// The package is the shared vocabulary, not the machinery. It imports the leaf packages (advisory, errs, meta) and no
// other domain package, so that internal/repository can decode and validate the member, internal/items can carry a
// block row, and every work package can import it without a cycle. Its ports therefore speak in plain values (ids, keys,
// names), never in the repository or item types. The machinery sits in sub-packages:
//
//	advisory   the normalized records, severities, source attribution, the npm audit id, the advisory URL (leaf)
//	match      the matcher over a format's version order (leaf)
//	osv        OSV and KEV decoding (leaf)
//	store      the sqlc wrappers of the vuln_* tables
//	sync       the advisory sync jobs
//	evaluate   evaluation, the lifecycle of findings, the retention task
//	triage     status changes
//	report     the report queries
package vuln
