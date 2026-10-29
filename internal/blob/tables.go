package blob

import "github.com/guided-traffic/trove/internal/backup/tables"

// The tables this package owns (R-BAK-004, R-STOR-047). Migration 00002
// creates them; the registrations moved here from internal/meta/postgres.
func init() {
	// Only rows that the snapshot references are exported.
	tables.Register("blobs", tables.Blobs, tables.Options{})
	// Candidates only; a restored database has no unreferenced blob rows.
	tables.Register("blob_gc_queue", tables.Transient, tables.Options{})
}
