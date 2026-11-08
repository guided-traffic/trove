package oci

import "github.com/guided-traffic/trove/internal/backup/tables"

// Registrations of the tables migration 00055_oci creates (oci_tombstones holds the manifests the journal replay withheld after a restore, R-OCI-033), so that every table carries its export class from its migration on
// (R-BAK-004). Both are data: a restore brings back the manifest rows and the reference graph, which the referrers API, the
// deletion refusals and the sweep of unreferenced blobs read (R-OCI-033). They are derived from the items in principle, but a
// restore has no pass that rebuilds them.
//
// A table registered twice panics at start-up, so the registration lives here and nowhere else. This package is linked by the
// composition root and by cmd/trove/registry.go, which `trove migrate` needs to check the migrated schema against the registry.
func init() {
	tables.Register("oci_manifests", tables.Data, tables.Options{})
	tables.Register("oci_references", tables.Data, tables.Options{})
	tables.Register("oci_tombstones", tables.Data, tables.Options{})
}
