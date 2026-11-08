package advisory

import "time"

// DataState is the state of one ecosystem's advisory data as a replica knows it (R-VULN-004, R-VULN-027): read from the
// per-replica cache vuln_source_state, never from a query per request.
type DataState struct {
	// Complete is true once a full import of the ecosystem has finished. Before that nothing is evaluated for it
	// (evaluation state pending_data), no finding resolves (R-VULN-011) and the audits answer 503 (R-VULN-017).
	Complete bool
	// Stale is true when SyncedAt is older than vulnerabilities.stale_after. Stale data is still used: evaluation and
	// enforcement continue and the report shows a banner (R-VULN-004).
	Stale bool
	// NotSyncedSinceRestore is true when the installation was restored (system_state restored_at) and the ecosystem has not
	// synced since: the data is as old as the restore point and older than the journal, so a block the journal replayed is
	// not yet judged by it (R-VULN-015, Q-VULN-132). False when the installation was never restored.
	NotSyncedSinceRestore bool
	// SyncedAt is the end of the last run that finished changed or unchanged; the zero time for an ecosystem that
	// was never imported.
	SyncedAt time.Time
	// Epoch is vuln_source_state.data_epoch, raised by every run that changed records. A rendering keyed by it
	// (the NuGet page, R-VULN-018) is correct while the epoch is the same.
	Epoch int64
}

// Age returns how old the data is at now; zero when the ecosystem was never imported.
func (d DataState) Age(now time.Time) time.Duration {
	if d.SyncedAt.IsZero() {
		return 0
	}
	return now.Sub(d.SyncedAt)
}
