package postgres

import "github.com/guided-traffic/trove/internal/backup/tables"

// vuln_sync_touched (00038_vulnerability_sync_fixes) holds the names a run of the advisory sync wrote until the run ends. It is
// transient like the advisory data it belongs to: a restore starts every ecosystem empty and the next full sync recomputes
// every alias group (R-VULN-026).
func init() {
	tables.Register("vuln_sync_touched", tables.Transient, tables.Options{})
}
