package deb

import (
	"github.com/guided-traffic/trove/internal/backup/tables"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/secrets"
)

// SigningKeys is the sealed column of the private keys of the Debian signing keys (R-DEB-015, R-ARCH-021): the key is sealed with the
// stored-secret key ring, bound to its row (the id) and its column, so a sealed key copied to another row does not open. Registering it
// is what makes `trove admin secrets status`, the start-up canary and `secrets-reencrypt` cover it; a state `retired` has none (NULL).
var SigningKeys = secrets.Register("deb_signing_keys", "id", "private_key")

// Registrations of the tables the migrations 00060_deb, 00061_deb_signing_keys and 00062_deb_remote create, so that every table
// carries its export class from its migration on (R-BAK-004).
//
// Every table is data (R-DEB-032): a restore brings back the suites with their sequence numbers, the stored files' rows and
// coordinates, the generations that by-hash retention reads, the marks of withheld files and the signing keys with their sealed private
// keys. The three tables of a remote are its cache rows: a restore without remote caches deletes them with the cache (R-BAK-008,
// R-DEB-023, H-630); the purge that does so is built with the remote class.
//
// A table registered twice panics at start-up, so the registration lives here and nowhere else. This package is linked by the
// composition root and by cmd/trove/registry.go, which `trove migrate` needs to check the migrated schema against the registry.
func init() {
	for _, t := range []string{
		"deb_suites", "deb_binaries", "deb_coordinates", "deb_generations", "deb_tombstones",
		"deb_signing_keys",
		"deb_remote_releases", "deb_remote_indexes", "deb_remote_entries",
	} {
		tables.Register(t, tables.Data, tables.Options{})
	}
	// Clients hold a suite's changeSeq for the index state read (R-DEB-014): a restore raises both numbers by 2^32 so that none
	// repeats with different content (R-ARCH-009, R-BAK-022, R-DEB-032).
	meta.RegisterRevisionColumn("deb_suites", "change_seq")
	meta.RegisterRevisionColumn("deb_suites", "built_seq")
}
