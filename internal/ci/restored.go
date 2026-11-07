package ci

import "time"

// The CI parts of a restore (R-CI-029 "After a restore", R-CI-018, R-BAK-004, R-BAK-022). A restore of a trove
// backup, or a point-in-time restore followed by `trove admin post-restore`, leaves the CI state as follows:
//
//   - ci_issuers, ci_mappings, ci_token_issuances and ci_subject_token_uses are `data` and come back as of the
//     snapshot (internal/meta/postgres/tables_ci.go); the issuers' version and mappings_version and the mappings'
//     version are revision columns, which the restore lifts above anything the old installation issued, so a
//     replica's copy never takes a restored row for one it already holds;
//   - ci_issuer_keys is `transient`: a restored installation fetches every key set again, so a key the issuer
//     removed after the snapshot is not revived (Q-CI-12);
//   - the disables, deletions and narrowings of issuers and mappings after the snapshot are replayed from the
//     revocation journal (journal_replay.go, R-CI-020), the token revocations with token.revoked (R-CI-017);
//   - the replay set keeps every jti used before the snapshot, and a single-use issuer refuses a subject token
//     issued up to clock_skew after the restore finished ([predatesRestore], read in the exchange's transaction from
//     the system_state key restored_at, which the restore's finishing transaction and post-restore set);
//   - the exchange's rate-limit counters live in Valkey under the keyspace the restore replaces, so they start empty.

// predatesRestore is the restore cut-off of R-CI-018 for a single-use issuer: a subject token whose iat is not later
// than restoredAt plus the issuer's clock_skew may have been exchanged at the old installation after the snapshot, a
// use the restored replay set does not hold, so the exchange refuses it (subject_token_predates_restore). The leeway
// lies on the refusing side: a token the old installation accepted before the restore finished has iat at most
// clock_skew in the future of that moment (R-CI-004 step 8). A zero restoredAt is an installation never restored.
func predatesRestore(iat, restoredAt time.Time, skew time.Duration) bool {
	return !restoredAt.IsZero() && !iat.After(restoredAt.Add(skew))
}

// restoredAtOf is the time of CiExchangeRestoredAt, which answers microseconds since the epoch and 0 before the
// first restore.
func restoredAtOf(us int64) time.Time {
	if us <= 0 {
		return time.Time{}
	}
	return time.UnixMicro(us)
}
