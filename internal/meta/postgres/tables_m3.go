package postgres

import "github.com/guided-traffic/trove/internal/backup/tables"

// Registrations of the tables the M3 migration creates (00017_nuget), so that every
// table carries its export class from its migration on (R-BAK-004) and the migration
// test passes before the package that owns them exists. Every one of them is data
// and NuGet has no transient table (R-NUGET-033): a restore must bring back the
// version rows, above all the tombstones, which exist to keep a removed version
// number from returning (Q-NUGET-6), and the symbol rows.
//
// The owning package takes its registrations over when it exists and deletes the
// lines here in the same change; a table registered twice panics at start-up, so a
// forgotten line is found at once.
func init() {
	// The NuGet format (00017_nuget, R-NUGET-005, R-NUGET-006, R-NUGET-020,
	// R-NUGET-021, R-NUGET-033).
	tables.Register("nuget_version", tables.Data, tables.Options{})
	tables.Register("nuget_tombstone", tables.Data, tables.Options{})
	tables.Register("nuget_symbol_package", tables.Data, tables.Options{})
	tables.Register("nuget_symbol_file", tables.Data, tables.Options{})
}
