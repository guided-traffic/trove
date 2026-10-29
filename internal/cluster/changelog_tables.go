package cluster

import "github.com/guided-traffic/trove/internal/backup/tables"

// cluster_epoch is data, so the area rows that migrations created survive a
// restore; cluster_change is transient and empty after a restore (R-ARCH-018,
// R-BAK-004). The revision-column registrations of both stay with the table
// registry of internal/meta/postgres.
func init() {
	tables.Register("cluster_epoch", tables.Data, tables.Options{})
	tables.Register("cluster_change", tables.Transient, tables.Options{})
}
