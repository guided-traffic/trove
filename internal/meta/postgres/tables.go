package postgres

import "github.com/guided-traffic/trove/internal/backup/tables"

// The tables this package owns (R-BAK-004). The coordination and storage tables
// of the first migrations are registered in tables_m0.go.
func init() {
	// Recreated by the migrations of a restore (R-BAK-023).
	tables.Register("schema_migration", tables.Schema, tables.Options{})
	tables.Register("schema_state", tables.Schema, tables.Options{})
	// installation_id survives a restore; keyspace_id and incarnation are
	// replaced by it (R-BAK-022). The table is data.
	tables.Register("system_state", tables.Data, tables.Options{})
}
