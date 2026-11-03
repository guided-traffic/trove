//go:build integration

package vulnfindings

import (
	"testing"
)

// R-VULN-012: a finding's identity survives the removal and return of its version. A dismissed finding of a version that is
// removed resolves with version_removed (the removal hook, in the removing transaction) and keeps its earlier status; the
// version that comes back re-detects the same finding, dismissed again.
func TestR_VULN_012_AVersionRemovedAndPublishedAgainKeepsItsFinding(t *testing.T) {
	e := newEnv(t)
	e.setComplete("npm", true)
	e.addAdvisory("npm", "GHSA-aaaa-aaaa-0001", "", "vulnerability", "high")
	e.addAffected("npm", "GHSA-aaaa-aaaa-0001", 0, "trove-test-life", everyVersionRange, nil, true, nil)
	e.publish("npm-vendor", "trove-test-life", "1.0.0")
	id := e.st.Text(`SELECT id::text FROM vuln_finding`)
	e.st.Exec(`UPDATE vuln_finding SET status = 'dismissed', dismissal_reason = 'risk_accepted', comment = 'accepted' WHERE id = $1::uuid`, id)

	e.unpublish("npm-vendor", "trove-test-life", "1.0.0")
	if got := e.st.Text(`SELECT status || ' ' || resolved_reason || ' ' || status_before_resolution FROM vuln_finding WHERE id = $1::uuid`, id); got != "resolved version_removed dismissed" {
		t.Fatalf("after the removal: %q", got)
	}
	// A local npm repository never accepts an unpublished version again (R-NPM-008); a cache does: the version comes back as a
	// row the fill writes, which the next evaluation of its name finds.
	e.addVersion("npm-vendor", "trove-test-life", "1.0.0")
	e.queue("npm", "GHSA-aaaa-aaaa-0001")
	e.evaluate()
	if got := e.st.Number(`SELECT count(*) FROM vuln_finding`); got != 1 {
		t.Fatalf("%d findings after the version came back", got)
	}
	if got := e.st.Text(`SELECT id::text || ' ' || status || ' ' || COALESCE(resolved_reason, '-') || ' ' || COALESCE(dismissal_reason, '-') || ' ' || (version_id IS NOT NULL)::text
		FROM vuln_finding`); got != id+" dismissed - risk_accepted true" {
		t.Errorf("after the return: %q", got)
	}
	// The removal did not last: the pair of events is taken back and the history says what happened once (Q-VULN-66).
	if got := e.history("npm-vendor", "trove-test-life", "1.0.0"); got != "detected,redetected" {
		t.Errorf("history %q", got)
	}
}

// R-VULN-012: a material change of the advisory returns a dismissed finding to needs_triage with the event reopened, and the
// audit event; any other change keeps the dismissal and records advisory_changed.
func TestR_VULN_012_AMaterialChangeReopensADismissedFindingAndOtherChangesDoNot(t *testing.T) {
	e := newEnv(t)
	e.setComplete("npm", true)
	e.addAdvisory("npm", "GHSA-aaaa-aaaa-0005", "", "vulnerability", "medium")
	e.addAffected("npm", "GHSA-aaaa-aaaa-0005", 0, "trove-test-a", everyVersionRange, nil, true, nil)
	e.addAdvisory("npm", "GHSA-aaaa-aaaa-0006", "", "vulnerability", "medium")
	e.addAffected("npm", "GHSA-aaaa-aaaa-0006", 0, "trove-test-b", everyVersionRange, nil, true, nil)
	e.publish("npm-vendor", "trove-test-a", "1.0.0")
	e.publish("npm-vendor", "trove-test-b", "1.0.0")
	e.st.Exec(`UPDATE vuln_finding SET status = 'dismissed', dismissal_reason = 'false_positive'`)

	// The score rises from 5.3 to 9.8: the level goes from medium to critical.
	e.st.Exec(`UPDATE vuln_advisory SET severity = 'critical', cvss_score = 9.8, modified = now() + interval '1 hour' WHERE id = 'GHSA-aaaa-aaaa-0005'`)
	// Only the summary of the other record changes.
	e.st.Exec(`UPDATE vuln_advisory SET summary = 'a new summary', modified = now() + interval '1 hour' WHERE id = 'GHSA-aaaa-aaaa-0006'`)
	e.queue("npm", "GHSA-aaaa-aaaa-0005", "GHSA-aaaa-aaaa-0006")
	e.evaluate()

	if got := e.status("npm-vendor", "trove-test-a", "1.0.0"); got != "needs_triage" {
		t.Errorf("the finding of the raised advisory is %q, want needs_triage", got)
	}
	if got := e.st.Text(`SELECT COALESCE(dismissal_reason, '-') || ' ' || severity FROM vuln_finding WHERE package_name = 'trove-test-a'`); got != "- critical" {
		t.Errorf("the reopened finding stores %q", got)
	}
	if got := e.history("npm-vendor", "trove-test-a", "1.0.0"); got != "detected,reopened" {
		t.Errorf("history %q", got)
	}
	if got := e.status("npm-vendor", "trove-test-b", "1.0.0"); got != "dismissed" {
		t.Errorf("the finding of the reworded advisory is %q, want dismissed", got)
	}
	// Only the record was modified (the wording): the time is stored, no event and no new revision (Q-VULN-66).
	if got := e.history("npm-vendor", "trove-test-b", "1.0.0"); got != "detected" {
		t.Errorf("history %q", got)
	}
	if got := e.events("vulnerability.finding.reopened"); got != 1 {
		t.Errorf("%d reopened audit events, want 1", got)
	}

	// A known-exploited flag reopens as well; a lower severity does not.
	e.st.Exec(`UPDATE vuln_advisory SET known_exploited = true, severity = 'low', modified = now() + interval '2 hours' WHERE id = 'GHSA-aaaa-aaaa-0006'`)
	e.queue("npm", "GHSA-aaaa-aaaa-0006")
	e.evaluate()
	if got := e.status("npm-vendor", "trove-test-b", "1.0.0"); got != "needs_triage" {
		t.Errorf("a newly known-exploited advisory leaves %q, want needs_triage", got)
	}
}

// R-VULN-012, R-VULN-009: a withdrawn advisory resolves its findings with advisory_withdrawn and the blocked finding stops
// blocking; the advisory published again re-detects the same finding, blocked again.
func TestR_VULN_012_WithdrawingAnAdvisoryResolvesAndPublishingItAgainRestoresTheBlock(t *testing.T) {
	e := newEnv(t)
	e.setComplete("npm", true)
	e.addAdvisory("npm", "MAL-2026-1234", "", "malicious", "critical")
	e.addAffected("npm", "MAL-2026-1234", 0, "trove-test-mal", everyVersionRange, nil, true, nil)
	e.addVersion("npm-remote", "trove-test-mal", "1.0.0")
	e.rescan("npm-remote")
	id := e.st.Text(`SELECT id::text FROM vuln_finding`)
	e.st.Exec(`UPDATE vuln_finding SET status = 'blocked' WHERE id = $1::uuid`, id)

	e.st.Exec(`UPDATE vuln_advisory SET withdrawn = now(), modified = now() + interval '1 hour'`)
	e.queue("npm", "MAL-2026-1234")
	e.evaluate()
	if got := e.st.Text(`SELECT status || ' ' || resolved_reason || ' ' || status_before_resolution FROM vuln_finding WHERE id = $1::uuid`, id); got != "resolved advisory_withdrawn blocked" {
		t.Fatalf("after the withdrawal: %q", got)
	}

	e.st.Exec(`UPDATE vuln_advisory SET withdrawn = NULL, modified = now() + interval '2 hours'`)
	e.queue("npm", "MAL-2026-1234")
	e.evaluate()
	if got := e.st.Number(`SELECT count(*) FROM vuln_finding`); got != 1 {
		t.Fatalf("%d findings", got)
	}
	if got := e.st.Text(`SELECT id::text || ' ' || status FROM vuln_finding`); got != id+" blocked" {
		t.Errorf("after the advisory came back: %q, want the same finding blocked", got)
	}
	if got := e.history("npm-remote", "trove-test-mal", "1.0.0"); got != "detected,resolved,redetected" {
		t.Errorf("history %q", got)
	}
}

// R-VULN-011 "no resolution without data": while an ecosystem's data is not complete, no evaluation resolves a finding of it,
// whatever the advisory tables say; once it is complete, the next evaluation does.
func TestR_VULN_011_NoEvaluationResolvesAFindingWithoutData(t *testing.T) {
	e := newEnv(t)
	e.setComplete("npm", true)
	e.addAdvisory("npm", "GHSA-aaaa-aaaa-0001", "", "vulnerability", "high")
	e.addAffected("npm", "GHSA-aaaa-aaaa-0001", 0, "trove-test-vuln", fixedRange("SEMVER", "1.0.1"), nil, false, nil)
	e.addVersion("npm-remote", "trove-test-vuln", "1.0.0")
	e.rescan("npm-remote")
	e.st.Exec(`UPDATE vuln_finding SET status = 'blocked'`)

	// A restore leaves the advisory tables empty and every ecosystem empty (R-VULN-026).
	e.st.Exec(`DELETE FROM vuln_advisory`)
	e.setComplete("npm", false)
	e.queue("npm", "GHSA-aaaa-aaaa-0001")
	e.evaluate()
	e.rescan("npm-remote")
	if got := e.status("npm-remote", "trove-test-vuln", "1.0.0"); got != "blocked" {
		t.Fatalf("without data the finding is %q, want blocked", got)
	}

	e.setComplete("npm", true)
	e.st.Exec(`INSERT INTO vuln_source_state (source, ecosystem, state, full_evaluation_requested_at) VALUES ('osv', 'npm', 'complete', now())
		ON CONFLICT (source, ecosystem) DO UPDATE SET full_evaluation_requested_at = now()`)
	e.evaluate()
	if got := e.status("npm-remote", "trove-test-vuln", "1.0.0"); got != "resolved" {
		t.Errorf("with the data complete and the full evaluation run the finding is %q, want resolved", got)
	}
	if got := e.st.Text(`SELECT COALESCE(full_evaluation_requested_at::text, 'cleared') FROM vuln_source_state WHERE ecosystem = 'npm'`); got != "cleared" {
		t.Errorf("the request of the full evaluation is %q", got)
	}
}

// R-VULN-011: a new advisory for a version the repository has cached for a week produces the finding after the next evaluation
// job, and the evaluation never reads a blob: the stored versions here have no blob at all.
func TestR_VULN_011_ANewAdvisoryEvaluatesTheCachedVersionsOfItsName(t *testing.T) {
	e := newEnv(t)
	e.setComplete("npm", true)
	e.addVersion("npm-remote", "trove-test-late", "2.0.0")
	e.addVersion("npm-remote", "trove-test-late", "1.0.0")
	e.addAdvisory("npm", "GHSA-aaaa-aaaa-0002", "", "vulnerability", "high")
	e.addAffected("npm", "GHSA-aaaa-aaaa-0002", 0, "trove-test-late", `[{"type":"SEMVER","events":[{"introduced":"2.0.0"}]}]`, nil, false, nil)
	e.queue("npm", "GHSA-aaaa-aaaa-0002")
	j := e.evaluate()
	if got := e.findings("npm-remote", "trove-test-late", "2.0.0", ""); got != 1 {
		t.Errorf("%d findings for 2.0.0", got)
	}
	if got := e.findings("npm-remote", "trove-test-late", "1.0.0", ""); got != 0 {
		t.Errorf("%d findings for 1.0.0", got)
	}
	if got := e.st.Number(`SELECT count(*) FROM vuln_pending_evaluation`); got != 0 {
		t.Errorf("%d advisories are still queued", got)
	}
	if j.ProgressUnit != "" && j.ProgressUnit != "versions" {
		t.Errorf("progress unit %q", j.ProgressUnit)
	}
}

// R-VULN-012: a version blocked in triage that the advisory no longer names, and the evaluation of the restored data after a
// narrowing change, resolve with no_longer_affected once the data is complete.
func TestR_VULN_012_AChangedRangeThatNoLongerCoversTheVersionResolvesTheFinding(t *testing.T) {
	e := newEnv(t)
	e.setComplete("npm", true)
	e.addAdvisory("npm", "GHSA-aaaa-aaaa-0001", "", "vulnerability", "high")
	e.addAffected("npm", "GHSA-aaaa-aaaa-0001", 0, "trove-test-vuln", fixedRange("SEMVER", "1.0.1"), nil, false, nil)
	e.addVersion("npm-remote", "trove-test-vuln", "1.0.0")
	e.queue("npm", "GHSA-aaaa-aaaa-0001")
	e.evaluate()
	if got := e.status("npm-remote", "trove-test-vuln", "1.0.0"); got != "needs_triage" {
		t.Fatalf("status %q", got)
	}
	e.st.Exec(`UPDATE vuln_affected SET ranges = $1::text::jsonb WHERE advisory_id = 'GHSA-aaaa-aaaa-0001'`, fixedRange("SEMVER", "1.0.0"))
	e.queue("npm", "GHSA-aaaa-aaaa-0001")
	e.evaluate()
	if got := e.st.Text(`SELECT status || ' ' || resolved_reason FROM vuln_finding`); got != "resolved no_longer_affected" {
		t.Errorf("after the narrowing: %q", got)
	}
}
