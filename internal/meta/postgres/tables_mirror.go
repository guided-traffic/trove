package postgres

import "github.com/guided-traffic/trove/internal/backup/tables"

// Registrations of the tables of 00040_mirror (R-REPO-058, R-BAK-004, Q-REPO-147):
//
//   - mirror_state: data. The cursor of the last complete pass matches the restored cache, both being of one snapshot; a
//     restore clears the pass in progress, the request, the rate-limit wait and the bound (R-REPO-058).
//   - mirror_pending: transient. The first pass after a restore rediscovers what the interrupted pass had pending.
func init() {
	tables.Register("mirror_state", tables.Data, tables.Options{})
	tables.Register("mirror_pending", tables.Transient, tables.Options{})
}
