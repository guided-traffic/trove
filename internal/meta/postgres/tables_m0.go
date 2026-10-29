package postgres

import (
	"github.com/guided-traffic/trove/internal/backup/tables"
	"github.com/guided-traffic/trove/internal/meta"
)

// Registrations of the tables the first migrations create for other packages
// (00002_storage_core, 00003_storage_usage, 00004_coordination), so that every
// table carries its export class from the first migration on (R-BAK-004) and
// the migration test passes before those packages exist.
//
// The owning packages take their registrations over when they exist: internal/jobs
// registers job, internal/cluster registers cluster_epoch, cluster_change and
// replica, internal/blob registers blobs and blob_gc_queue, and the repository
// and storage packages register the rest. The package that takes one over
// deletes the line here in the same change; a table registered twice panics at
// start-up, so a forgotten line is found at once.
func init() {
	// Storage core (R-STOR-003).
	tables.Register("repositories", tables.Data, tables.Options{})
	tables.Register("packages", tables.Data, tables.Options{})
	// The cycle between package_versions and items: restore loads lead_item_id as
	// NULL and fills it afterwards.
	tables.Register("package_versions", tables.Data, tables.Options{CycleBreak: "lead_item_id"})
	tables.Register("items", tables.Data, tables.Options{})
	tables.Register("item_properties", tables.Data, tables.Options{})
	tables.Register("item_download_stats", tables.Data, tables.Options{})
	// Usage counters; a restore recomputes them (storage_usage_recount, R-STOR-047).
	tables.Register("repository_usage", tables.Data, tables.Options{})
	tables.Register("storage_usage_totals", tables.Data, tables.Options{})
	tables.Register("storage_usage_deltas", tables.Data, tables.Options{})

	// Revision columns (R-ARCH-009, R-ARCH-018, R-BAK-022): a restore raises
	// them by 2^32. packages.rev_seq is the first entry of the registry.
	meta.RegisterRevisionColumn("packages", "rev_seq")
	meta.RegisterRevisionColumn("cluster_epoch", "epoch")
	// cluster_change is transient and empty after a restore, so raising its copy
	// of the epoch changes nothing; it is registered so that the schema test of
	// R-ARCH-009 has no exception to carry.
	meta.RegisterRevisionColumn("cluster_change", "epoch")
}
