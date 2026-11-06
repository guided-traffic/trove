package postgres

import "github.com/guided-traffic/trove/internal/backup/tables"

// Registration of the table of 00043_ui_assets (R-HA-020, R-BAK-004, Q-HA-120):
//
//   - ui_assets: transient. The rows name the hashed files of the UI builds that replicas published; a restore leaves the
//     table empty, and every replica that serves the UI publishes its own build again at its next start or within the hour.
func init() {
	tables.Register("ui_assets", tables.Transient, tables.Options{})
}
