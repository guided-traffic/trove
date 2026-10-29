package cluster

import "github.com/guided-traffic/trove/internal/backup/tables"

// The replica registry is transient: a restored installation starts with an
// empty registry (R-OPS-003, R-BAK-004).
func init() {
	tables.Register("replica", tables.Transient, tables.Options{})
}
