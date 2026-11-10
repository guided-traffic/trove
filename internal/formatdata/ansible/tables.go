package ansible

import "github.com/guided-traffic/trove/internal/backup/tables"

// Registrations of the tables the migrations 00068_ansible_collections and 00069_ansible_import_tasks create, so that every table
// carries its export class from its migration on (R-BAK-004).
//
// All three are data (R-ANS-029): a restore brings back the version rows with their items, the tombstones that keep a deleted number
// from being published again, and the import tasks that are kept for their retention window. None carries a revision column: the
// documents are rendered from the rows and their ETag is derived from the generic package row (Q-ANS-31). The table of the digests a
// remote announces, ansible_upstream_versions, is registered with its migration by the remote class (remote_tables.go): it goes with
// the cache of a remote and is dropped by a restore without remote caches (R-BAK-008).
//
// A table registered twice panics at start-up, so the registration lives here and nowhere else. This package is linked by the
// composition root and by cmd/trove/registry.go, which `trove migrate` needs to check the migrated schema against the registry.
func init() {
	for _, t := range []string{"ansible_collection_versions", "ansible_tombstones", "ansible_import_tasks"} {
		tables.Register(t, tables.Data, tables.Options{})
	}
}
