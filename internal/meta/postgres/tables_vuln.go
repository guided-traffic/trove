package postgres

import (
	"github.com/guided-traffic/trove/internal/backup/tables"
	"github.com/guided-traffic/trove/internal/meta"
)

// Registrations of the tables of 00035_vulnerabilities (R-VULN-026, R-BAK-004), so that the migration test passes and
// every table carries its export class from its migration on.
//
//   - vuln_finding, vuln_finding_event: data. Triage is work people did and cannot be fetched again (Q-VULN-13); a
//     restore brings the findings back with their statuses, which keep blocking until the first full import has
//     completed (R-VULN-011, R-VULN-026).
//   - vuln_source_state, vuln_advisory, vuln_affected, vuln_kev, vuln_pending_evaluation, vuln_claimed_match: transient.
//     Public data that the next sync fetches again, or state recomputed from it; it would add hundreds of megabytes to
//     every database export. After a restore every ecosystem is empty and the next vuln-sync-osv slot runs a full sync.
func init() {
	tables.Register("vuln_finding", tables.Data, tables.Options{})
	tables.Register("vuln_finding_event", tables.Data, tables.Options{})
	tables.Register("vuln_source_state", tables.Transient, tables.Options{})
	tables.Register("vuln_advisory", tables.Transient, tables.Options{})
	tables.Register("vuln_affected", tables.Transient, tables.Options{})
	tables.Register("vuln_kev", tables.Transient, tables.Options{})
	tables.Register("vuln_pending_evaluation", tables.Transient, tables.Options{})
	tables.Register("vuln_claimed_match", tables.Transient, tables.Options{})

	// revision is the ETag of a finding (R-API-012, R-VULN-013); a restore raises it by 2^32 like every client-held
	// revision (R-BAK-022).
	meta.RegisterRevisionColumn("vuln_finding", "revision")
}
