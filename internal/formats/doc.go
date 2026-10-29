// Package formats is the contract of the package formats and their registry
// (R-ARCH-008, R-ARCH-010, R-REPO-003).
//
// This is the skeleton milestone M1 needs for the generic format: the
// descriptor that the format catalog serves and the repository model validates
// against, the request descriptor that `/files/` requests are classified into,
// and the registry that formats register in at compile time
// (internal/formats/all). The routes, the storage hooks, the remote and
// virtual adapters and the conformance kit of R-ARCH-008 and R-ARCH-010 join
// the interface with the first format that has a protocol of its own (npm,
// milestone M2); each addition bumps [ContractVersion].
//
// A format imports nothing but this package and the leaf packages errs, config
// and observability (archtest, R-ARCH-008): it opens no connection, runs no SQL
// and touches no object store. In particular it does not import
// internal/repository; the paths it classifies arrive as plain strings.
package formats
