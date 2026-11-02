package postgres

import (
	"github.com/guided-traffic/trove/internal/backup/tables"
	"github.com/guided-traffic/trove/internal/meta"
)

// Registrations of the tables of 00026_ci_oidc (R-CI-001, R-BAK-004), so that the migration test
// passes and every table carries its export class from its migration on.
//
//   - ci_issuers, ci_mappings, ci_subject_token_uses, ci_token_issuances: data. A restore brings back
//     the issuers, the mappings, the replay set (a jti used before the snapshot stays used, R-CI-018)
//     and the issuance records (R-CI-029).
//   - ci_issuer_keys: transient. A restored installation starts without key sets and fetches them
//     again; a restored copy would accept a key the issuer has since removed (R-CI-003).
func init() {
	tables.Register("ci_issuers", tables.Data, tables.Options{})
	tables.Register("ci_issuer_keys", tables.Transient, tables.Options{})
	tables.Register("ci_mappings", tables.Data, tables.Options{})
	tables.Register("ci_subject_token_uses", tables.Data, tables.Options{})
	tables.Register("ci_token_issuances", tables.Data, tables.Options{})

	// version is the ETag of an issuer and of a mapping (R-CI-002, R-CI-019); mappings_version is
	// the counter the exchange compares (R-CI-007, Q-CI-15). A restore raises each by 2^32 like
	// every client-held revision (R-BAK-022).
	meta.RegisterRevisionColumn("ci_issuers", "version")
	meta.RegisterRevisionColumn("ci_issuers", "mappings_version")
	meta.RegisterRevisionColumn("ci_mappings", "version")
}
