// Package repository is the repository model (R-REPO-001 to R-REPO-013): class,
// format and key, the configuration document (R-REPO-004), validation
// (R-REPO-008), create, read, update, list and delete as domain functions, the
// include and exclude patterns (R-REPO-005), path canonicalisation (R-ARCH-005)
// and the per-replica snapshot that is rebuilt on the change area repo_config.
//
// The shared contract is in repository.go: the model types and the Reader that
// authz, the items API and the files transfer use per request. The Service is the
// write side that the management API calls after it has authorized: every write
// is one transaction holding the row, the audit event and the change-log row. The
// package does not know formats: internal/formats is above this layer, so the
// model validates against a FormatCatalog that the composition root adapts from
// the format registry. docs/developer/repositories.md describes the flows.
package repository
