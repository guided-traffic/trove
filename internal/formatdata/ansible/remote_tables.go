package ansible

import "github.com/guided-traffic/trove/internal/backup/tables"

// ansible_upstream_versions (migration 00070_ansible_upstream_versions, R-ANS-018) is data like every table of the format: a backup
// carries the digests a remote recorded with its cached archives, so a restore keeps verifying them against the first digest. A
// restore from a backup that left the remote caches out deletes the rows with the caches (PurgeRemoteCaches, R-BAK-008). Registered
// here and nowhere else (a table registered twice panics at start-up); tables.go registers the three tables of the local class.
func init() {
	tables.Register("ansible_upstream_versions", tables.Data, tables.Options{})
}
