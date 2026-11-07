//go:build integration

package vulnfindings

import (
	"encoding/json"
	"testing"
)

// R-VULN-012, R-HA-054 (Q-VULN-114): a reopen that sets a block (a dismissed malicious finding back in needs_triage under a
// policy that blocks malicious packages) writes the journal entry of a block, so a restore to a point before it replays it; a
// reopen that sets no block writes none.
func TestR_VULN_012_AReopenThatSetsABlockIsJournaled(t *testing.T) {
	e := newEnv(t)
	e.setComplete("npm", true)
	e.addAdvisory("npm", "MAL-2026-0042", "", "malicious", "high")
	e.addAffected("npm", "MAL-2026-0042", 0, "trove-test-mal", everyVersionRange, nil, true, nil)
	e.addAdvisory("npm", "GHSA-aaaa-aaaa-0042", "", "vulnerability", "medium")
	e.addAffected("npm", "GHSA-aaaa-aaaa-0042", 0, "trove-test-plain", everyVersionRange, nil, true, nil)
	e.publish("npm-vendor", "trove-test-mal", "1.0.0")
	e.publish("npm-vendor", "trove-test-plain", "1.0.0")
	e.st.Exec(`UPDATE vuln_finding SET status = 'dismissed', dismissal_reason = 'false_positive'`)
	before := e.st.Number(`SELECT count(*) FROM revocation_outbox WHERE event = 'vulnerability.finding.reopened'`)

	e.st.Exec(`UPDATE vuln_advisory SET severity = 'critical', cvss_score = 9.8, modified = now() + interval '1 hour'`)
	e.queue("npm", "MAL-2026-0042", "GHSA-aaaa-aaaa-0042")
	e.evaluate()

	for _, name := range []string{"trove-test-mal", "trove-test-plain"} {
		if got := e.status("npm-vendor", name, "1.0.0"); got != "needs_triage" {
			t.Fatalf("%s is %q after the material change, want needs_triage (reopened)", name, got)
		}
	}
	if got := e.events("vulnerability.finding.reopened"); got != 2 {
		t.Fatalf("%d reopened events, want 2", got)
	}
	if n := e.st.Number(`SELECT count(*) FROM revocation_outbox WHERE event = 'vulnerability.finding.reopened'`) - before; n != 1 {
		t.Fatalf("%d journal rows of the reopens, want 1: the malicious one sets a block, the other none", n)
	}
	var entry map[string]string
	raw := e.st.Text(`SELECT entry::text FROM revocation_outbox WHERE event = 'vulnerability.finding.reopened'`)
	if err := json.Unmarshal([]byte(raw), &entry); err != nil {
		t.Fatal(err)
	}
	id := e.st.Text(`SELECT id::text FROM vuln_finding WHERE package_name = 'trove-test-mal'`)
	if entry["finding"] != id || entry["block"] != "set" || entry["package"] != "trove-test-mal" || entry["version"] != "1.0.0" || entry["ecosystem"] != "npm" {
		t.Errorf("entry = %s", raw)
	}
}
