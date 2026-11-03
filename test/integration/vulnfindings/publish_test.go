//go:build integration

package vulnfindings

import (
	"testing"
)

// R-VULN-001, R-VULN-011: a publish evaluates the version in its commit, and the finding exists when the answer arrives. A
// version of the same name in a repository whose names are internal (an auto claim) has none, and a file of the generic format
// is never evaluated.
func TestR_VULN_001_PublishProducesAFindingPerRepositoryThatMatchesPublicAdvisories(t *testing.T) {
	e := newEnv(t)
	e.setComplete("npm", true)
	e.addAdvisory("npm", "GHSA-aaaa-aaaa-0001", "", "vulnerability", "high")
	e.addAffected("npm", "GHSA-aaaa-aaaa-0001", 0, "trove-test-vuln", fixedRange("SEMVER", "1.0.1"), nil, false, []string{"1.0.1"})

	e.publish("npm-vendor", "trove-test-vuln", "1.0.0")
	if got := e.findings("npm-vendor", "trove-test-vuln", "1.0.0", "GHSA-aaaa-aaaa-0001"); got != 1 {
		t.Fatalf("%d findings after the publish answered, want 1", got)
	}
	if got := e.status("npm-vendor", "trove-test-vuln", "1.0.0"); got != "needs_triage" {
		t.Errorf("status %q", got)
	}
	if got := e.st.Text(`SELECT fix_available::text || ' ' || fixed_versions::text || ' ' || severity || ' ' || severity_rank FROM vuln_finding`); got != "true {1.0.1} high 4" {
		t.Errorf("the finding stores %q", got)
	}
	if got := e.history("npm-vendor", "trove-test-vuln", "1.0.0"); got != "detected" {
		t.Errorf("history %q", got)
	}

	e.publish("npm-vendor", "trove-test-vuln", "1.0.1")
	if got := e.findings("npm-vendor", "trove-test-vuln", "1.0.1", ""); got != 0 {
		t.Errorf("the fixed version has %d findings", got)
	}

	// The name is claimed by the first publish to npm-local (an auto claim): it is internal there, whatever the advisory says.
	e.publish("npm-local", "trove-test-vuln", "1.0.0")
	if got := e.findings("npm-local", "", "", ""); got != 0 {
		t.Errorf("npm-local has %d findings for an internal name", got)
	}
}
