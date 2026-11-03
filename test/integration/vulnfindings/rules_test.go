//go:build integration

package vulnfindings

import (
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

const everyVersionRange = `[{"type":"SEMVER","events":[{"introduced":"0"}]}]`

// R-VULN-010: a name an auto claim covers is internal in a local repository: no finding, the evaluation state is internal, the
// match is recorded and the security event is written once per advisory and name, however often the evaluation runs again. A
// local repository without a claim that covers the name is matched, and a malicious record makes a malicious finding.
func TestR_VULN_010_AClaimedNameIsInternalAndAnUnclaimedOneIsMatched(t *testing.T) {
	e := newEnv(t)
	e.setComplete("npm", true)
	e.publish("npm-local", "@acme/ui", "1.0.0")
	e.publish("npm-noclaim", "left-pad", "1.0.0")
	e.addAdvisory("npm", "MAL-2026-1235", "", "malicious", "critical")
	e.addAffected("npm", "MAL-2026-1235", 0, "@acme/ui", everyVersionRange, nil, true, nil)
	e.addAdvisory("npm", "MAL-2026-1236", "", "malicious", "critical")
	e.addAffected("npm", "MAL-2026-1236", 0, "left-pad", everyVersionRange, nil, true, nil)
	e.queue("npm", "MAL-2026-1235", "MAL-2026-1236")
	e.evaluate()

	if got := e.findings("npm-local", "@acme/ui", "", ""); got != 0 {
		t.Errorf("the claimed name has %d findings in npm-local", got)
	}
	if got := e.st.Number(`SELECT count(*) FROM vuln_claimed_match WHERE ecosystem = 'npm' AND advisory_id = 'MAL-2026-1235' AND name = '@acme/ui' AND kind = 'malicious'`); got != 1 {
		t.Errorf("%d claimed-name matches, want 1", got)
	}
	testenv.WaitFor(t, "the security event", 10*time.Second, func() bool { return e.events("vulnerability.claimed_name_match") == 1 })
	if got := e.status("npm-noclaim", "left-pad", "1.0.0"); got != "needs_triage" {
		t.Errorf("the unclaimed name's finding is %q, want needs_triage", got)
	}
	if got := e.st.Text(`SELECT kind FROM vuln_finding WHERE package_name = 'left-pad'`); got != "malicious" {
		t.Errorf("kind %q", got)
	}
	if got := e.st.Number(`SELECT count(*) FROM vuln_claimed_match WHERE name = 'left-pad'`); got != 0 {
		t.Errorf("an unclaimed name has %d claimed-name matches", got)
	}

	e.queue("npm", "MAL-2026-1235")
	e.evaluate()
	time.Sleep(500 * time.Millisecond) // the event is written after the commit, asynchronously
	if got := e.events("vulnerability.claimed_name_match"); got != 1 {
		t.Errorf("the event was written %d times, want once per advisory and name", got)
	}
	if got := e.st.Text(`SELECT details->>'claimRepository' FROM audit_event WHERE action = 'vulnerability.claimed_name_match'`); got != "npm-local" {
		t.Errorf("claimRepository %q", got)
	}
}

// R-VULN-010: a repository with matchPublicAdvisories always declares that it holds public packages: its claims serve routing
// only, so the name is matched in it, and the claim of such a repository does not make the name internal elsewhere.
func TestR_VULN_010_AlwaysMatchesAndItsClaimsDoNotWithholdTheNameElsewhere(t *testing.T) {
	e := newEnv(t)
	e.setComplete("npm", true)
	e.addAdvisory("npm", "GHSA-aaaa-aaaa-0001", "", "vulnerability", "high")
	e.addAffected("npm", "GHSA-aaaa-aaaa-0001", 0, "trove-test-vuln", everyVersionRange, nil, true, nil)
	e.publish("npm-vendor", "trove-test-vuln", "1.0.0") // an auto claim of a repository with always
	e.addVersion("npm-remote", "trove-test-vuln", "1.0.0")
	e.rescan("npm-remote")
	if got := e.findings("npm-vendor", "trove-test-vuln", "1.0.0", ""); got != 1 {
		t.Errorf("npm-vendor: %d findings", got)
	}
	if got := e.findings("npm-remote", "trove-test-vuln", "1.0.0", ""); got != 1 {
		t.Errorf("npm-remote: %d findings", got)
	}
	if got := e.st.Number(`SELECT count(*) FROM vuln_claimed_match`); got != 0 {
		t.Errorf("the claim of a repository with always made the name internal: %d matches", got)
	}
}

// R-VULN-010: a remote whose upstream is an internal mirror shows not_covered until it sets always; never covers nothing.
func TestR_VULN_010_ARemoteWithAnUnrecognisedUpstreamIsNotCoveredUntilItSetsAlways(t *testing.T) {
	e := newEnv(t)
	e.setComplete("npm", true)
	e.addAdvisory("npm", "MAL-2026-1236", "", "malicious", "critical")
	e.addAffected("npm", "MAL-2026-1236", 0, "left-pad", everyVersionRange, nil, true, nil)
	e.addVersion("npm-mirror", "left-pad", "1.0.0")
	e.addVersion("npm-remote", "left-pad", "1.0.0")
	e.rescan("npm-mirror")
	e.rescan("npm-remote")
	if got := e.findings("npm-mirror", "", "", ""); got != 0 {
		t.Errorf("the mirror has %d findings before it sets always", got)
	}
	if got := e.findings("npm-remote", "left-pad", "1.0.0", ""); got != 1 {
		t.Errorf("the public registry has %d findings, want 1", got)
	}

	// The setting change enqueues the rescan in its own transaction (R-VULN-002): the finding appears without a manual rescan.
	if a := e.patch("npm-mirror", `{"vulnerabilities":{"matchPublicAdvisories":"always"}}`); a.Status != 200 {
		t.Fatalf("patch: %d %s", a.Status, a.Body)
	}
	testenv.WaitFor(t, "the finding of the mirror", 30*time.Second, func() bool { return e.findings("npm-mirror", "left-pad", "1.0.0", "") == 1 })

	// never resolves it again: the repository is then not covered.
	if a := e.patch("npm-mirror", `{"vulnerabilities":{"matchPublicAdvisories":"never"}}`); a.Status != 200 {
		t.Fatalf("patch: %d %s", a.Status, a.Body)
	}
	testenv.WaitFor(t, "the resolution", 30*time.Second, func() bool { return e.status("npm-mirror", "left-pad", "1.0.0") == "resolved" })
	if got := e.st.Text(`SELECT resolved_reason FROM vuln_finding f JOIN repositories r ON r.id = f.repo_id WHERE r.key = 'npm-mirror'`); got != "no_longer_affected" {
		t.Errorf("resolved_reason %q", got)
	}
}

// R-VULN-009: a range with the event fixed "not-a-version" produces no finding and raises the unevaluable counter; the record's
// versions[] still match.
func TestR_VULN_009_AnUnevaluableRangeMatchesNothingAndVersionsStillDo(t *testing.T) {
	e := newEnv(t)
	e.setComplete("npm", true)
	e.addAdvisory("npm", "GHSA-aaaa-aaaa-0007", "", "vulnerability", "medium")
	e.addAffected("npm", "GHSA-aaaa-aaaa-0007", 0, "trove-test-badfix", fixedRange("SEMVER", "not-a-version"), []string{"1.0.0"}, false, nil)
	e.publish("npm-vendor", "trove-test-badfix", "1.0.0")
	e.publish("npm-vendor", "trove-test-badfix", "1.0.2")
	if got := e.findings("npm-vendor", "trove-test-badfix", "1.0.0", ""); got != 1 {
		t.Errorf("versions[] gave %d findings, want 1", got)
	}
	if got := e.findings("npm-vendor", "trove-test-badfix", "1.0.2", ""); got != 0 {
		t.Errorf("the unevaluable range matched: %d findings", got)
	}
	m := e.rep.Metrics(t)
	if v := testenv.MetricValue(m, `trove_vuln_unevaluable_ranges_total{ecosystem="npm"}`); v < 2 {
		t.Errorf("trove_vuln_unevaluable_ranges_total = %v, want at least 2\n%s", v, grepLines(m, "trove_vuln_"))
	}
	if v := testenv.MetricValue(m, `trove_vuln_evaluations_total{trigger="publish"}`); v < 2 {
		t.Errorf("trove_vuln_evaluations_total{publish} = %v, want at least 2", v)
	}
}

// R-VULN-009: the canonical identifier and the alias group. A record and its alias, both of the ecosystem, are one
// vulnerability: one finding per affected version, with the canonical identifier.
func TestR_VULN_006_AnAliasGroupIsOneFindingWithTheCanonicalIdentifier(t *testing.T) {
	e := newEnv(t)
	e.setComplete("npm", true)
	e.addAdvisory("npm", "GHSA-aaaa-bbbb-cccc", "", "vulnerability", "high")
	e.st.Exec(`UPDATE vuln_advisory SET aliases = ARRAY['CVE-2026-0001'] WHERE id = 'GHSA-aaaa-bbbb-cccc'`)
	e.addAdvisory("npm", "CVE-2026-0001", "GHSA-aaaa-bbbb-cccc", "vulnerability", "high")
	e.st.Exec(`UPDATE vuln_advisory SET modified = '2026-01-01T00:00:00Z'`) // one modification time for the group
	for i, id := range []string{"GHSA-aaaa-bbbb-cccc", "CVE-2026-0001"} {
		e.addAffected("npm", id, i, "trove-test-alias", fixedRange("SEMVER", "2.1.0"), nil, false, []string{"2.1.0"})
	}
	e.publish("npm-vendor", "trove-test-alias", "2.0.0")
	if got := e.findings("npm-vendor", "trove-test-alias", "2.0.0", ""); got != 1 {
		t.Fatalf("%d findings, want one for the group", got)
	}
	if got := e.findings("npm-vendor", "trove-test-alias", "2.0.0", "GHSA-aaaa-bbbb-cccc"); got != 1 {
		t.Errorf("the finding does not carry the canonical identifier")
	}

	// The canonical identifier of the group changes (R-VULN-006): the evaluation re-keys the finding, which keeps its id,
	// its status and its history.
	id := e.st.Text(`SELECT id::text FROM vuln_finding`)
	e.st.Exec(`UPDATE vuln_finding SET status = 'confirmed' WHERE id = $1::uuid`, id)
	e.st.Exec(`UPDATE vuln_advisory SET canonical_id = 'CVE-2026-0001'`)
	e.queue("npm", "GHSA-aaaa-bbbb-cccc", "CVE-2026-0001")
	e.evaluate()
	if got := e.st.Text(`SELECT advisory_id || ' ' || status FROM vuln_finding WHERE id = $1::uuid`, id); got != "CVE-2026-0001 confirmed" {
		t.Errorf("after the re-key: %q", got)
	}
	if got := e.st.Number(`SELECT count(*) FROM vuln_finding`); got != 1 {
		t.Errorf("%d findings after the re-key", got)
	}
	if got := e.history("npm-vendor", "trove-test-alias", "2.0.0"); got != "detected,advisory_changed" {
		t.Errorf("history %q", got)
	}
}

func grepLines(text, prefix string) string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(l, prefix) {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}
