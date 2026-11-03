package postgres

import "github.com/guided-traffic/trove/internal/backup/tables"

// Registrations of the tables of 00032_bastion (R-BAS-004, R-BAK-004), so that the migration test passes
// and every table carries its export class from its migration on.
//
//   - bastion: data. A restore brings back the registrations with their principals and tokens.
//   - bastion_contact: transient. The last contact of a replica says what the replica reported at its last
//     handshake; a restored installation shows none until the Bastions handshake again (R-BAS-010), and a
//     copy of the old ones would show replicas that are gone.
//
// The registration has no revision column of its own: principal.revision is its ETag (00013).
func init() {
	tables.Register("bastion", tables.Data, tables.Options{})
	tables.Register("bastion_contact", tables.Transient, tables.Options{})
}
