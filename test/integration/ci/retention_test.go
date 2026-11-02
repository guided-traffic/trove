//go:build integration

package ci

// The two retention tasks of R-CI-030, ci-replay-cleanup and ci-issuance-retention, on the job queue of a worker:
// batches of at most 1,000 rows, each its own transaction, in the order of the expiry index, idempotent. The drain of
// a worker during a run over 100,000 rows is the end-to-end tier's (W7); this tier runs the tasks to the end over
// more than one batch and runs them again.

import (
	"testing"
)

// R-CI-030: ci-replay-cleanup deletes the replay rows whose subject_expires_at + the issuer's clock skew +
// ci.replay.retention_after_expiry has passed, and keeps the others; a second run deletes nothing.
func TestR_CI_030_TheReplayCleanupDeletesWhatHasPassedInBatches(t *testing.T) {
	e := newCIEnv(t, false)
	a := e.web("web-a")
	e.worker("worker-1")
	// 2,500 rows past retention (15 m after exp plus the skew of 60 s), 3 inside it, 2 not expired.
	e.exec(`INSERT INTO ci_subject_token_uses (issuer_id, jti, first_exchanged_at, last_exchanged_at, exchange_count, subject_expires_at)
	        SELECT $1::uuid, 'old-' || g, now() - interval '2 hours', now() - interval '2 hours', 1, now() - interval '1 hour' - g * interval '1 second'
	          FROM generate_series(1, 2500) g`, e.issuerID)
	e.exec(`INSERT INTO ci_subject_token_uses (issuer_id, jti, first_exchanged_at, last_exchanged_at, exchange_count, subject_expires_at)
	        SELECT $1::uuid, 'skew-' || g, now(), now(), 1, now() - interval '15 minutes' FROM generate_series(1, 3) g`, e.issuerID)
	e.exec(`INSERT INTO ci_subject_token_uses (issuer_id, jti, first_exchanged_at, last_exchanged_at, exchange_count, subject_expires_at)
	        SELECT $1::uuid, 'live-' || g, now(), now(), 1, now() + interval '5 minutes' FROM generate_series(1, 2) g`, e.issuerID)
	e.runTask(t, a, "ci-replay-cleanup")
	if n := e.number(`SELECT count(*) FROM ci_subject_token_uses WHERE jti LIKE 'old-%'`); n != 0 {
		t.Errorf("%d rows past retention remain", n)
	}
	if n := e.number(`SELECT count(*) FROM ci_subject_token_uses WHERE jti NOT LIKE 'old-%'`); n != 5 {
		t.Errorf("%d rows inside retention remain, want 5", n)
	}
	// A worker may have run the task on its own schedule before the run of the test; together they deleted every row.
	if got := e.number(`SELECT COALESCE(sum((result->>'deleted')::bigint), 0) FROM job WHERE kind = 'ci-replay-cleanup' AND state = 'succeeded'`); got != 2500 {
		t.Errorf("the runs report %d deleted", got)
	}
	e.runTask(t, a, "ci-replay-cleanup")
	if got := e.text(`SELECT result->>'deleted' FROM job WHERE kind = 'ci-replay-cleanup' AND state = 'succeeded' ORDER BY finished_at DESC LIMIT 1`); got != "0" {
		t.Errorf("a second run deleted %s", got)
	}
}

// R-CI-030, R-CI-020: ci-issuance-retention deletes the records whose expires_at is older than
// ci.issuances.retention (8760h by default), in batches, and keeps the younger ones.
func TestR_CI_030_TheIssuanceRetentionDeletesOldRecordsInBatches(t *testing.T) {
	e := newCIEnv(t, false)
	a := e.web("web-a")
	e.worker("worker-1")
	ins := func(prefix string, n int, expires string) {
		e.exec(`INSERT INTO ci_token_issuances (token_id, issuer_id, mapping_ids, mapping_names, subject_kind, project_id, job_id, claims, grants,
		                                        granted_scope, issued_at, expires_at)
		        SELECT gen_random_uuid(), $1::uuid, '{}', '{}', 'id_token', '7', $2 || g, '{}'::jsonb, '{}'::jsonb, 'read', now() - interval '400 days',
		               `+expires+` FROM generate_series(1, $3::int) g`, e.issuerID, prefix, n)
	}
	ins("old-", 1200, "now() - interval '370 days'")
	ins("young-", 4, "now() - interval '300 days'")
	e.runTask(t, a, "ci-issuance-retention")
	if n := e.number(`SELECT count(*) FROM ci_token_issuances WHERE job_id LIKE 'old-%'`); n != 0 {
		t.Errorf("%d old records remain", n)
	}
	if n := e.number(`SELECT count(*) FROM ci_token_issuances WHERE job_id LIKE 'young-%'`); n != 4 {
		t.Errorf("%d young records remain, want 4", n)
	}
	e.runTask(t, a, "ci-issuance-retention")
	if got := e.text(`SELECT result->>'deleted' FROM job WHERE kind = 'ci-issuance-retention' AND state = 'succeeded' ORDER BY finished_at DESC LIMIT 1`); got != "0" {
		t.Errorf("a second run deleted %s", got)
	}
}
