//go:build integration

package vulnsync

import (
	"testing"

	"github.com/guided-traffic/trove/test/osvstub"
)

// R-VULN-006: the records of the live feeds, frozen in test/osvstub/testdata/real (SEMVER and ECOSYSTEM ranges, last_affected,
// versions[], withdrawn and malicious records, CVSS 3.1 and 4.0, records without a vector), all decode and validate, none is
// rejected, and the facts the specification states about them hold.
func TestR_VULN_006_TheRealRecordsOfTheLiveFeedsAreStored(t *testing.T) {
	e := newEnv(t, options{})
	if err := e.stub.Toggle(osvstub.ToggleReal, true); err != nil {
		t.Fatal(err)
	}
	e.mustSucceed(taskOSV, nil)
	for _, eco := range []string{"npm", "NuGet"} {
		if got, want := e.advisories(eco), int64(e.stub.Info().Ecosystems[eco].Records); got != want {
			t.Errorf("%s: %d records stored, the source has %d", eco, got, want)
		}
		if got := e.stateOf("rejected", "osv", eco); got != "0" {
			t.Errorf("%s: %s records rejected", eco, got)
		}
	}
	// GHSA-vr7g-88fq-vhq3 has SEMVER ranges, GHSA-473q-m89c-ghf8 ECOSYSTEM ranges.
	if got := e.text("SELECT string_agg(DISTINCT r->>'type', ',') FROM vuln_affected, jsonb_array_elements(ranges) r WHERE ecosystem = 'npm' AND advisory_id = 'GHSA-vr7g-88fq-vhq3'"); got != "SEMVER" {
		t.Errorf("ranges of GHSA-vr7g-88fq-vhq3: %q", got)
	}
	if got := e.text("SELECT string_agg(DISTINCT r->>'type', ',') FROM vuln_affected, jsonb_array_elements(ranges) r WHERE ecosystem = 'NuGet' AND advisory_id = 'GHSA-473q-m89c-ghf8'"); got != "ECOSYSTEM" {
		t.Errorf("ranges of GHSA-473q-m89c-ghf8: %q", got)
	}
	// The malicious records are critical; a withdrawn record stays stored with its time.
	if got := e.text("SELECT severity || ' ' || severity_source || ' ' || kind FROM vuln_advisory WHERE ecosystem = 'npm' AND id = 'MAL-2026-5587'"); got != "critical malicious malicious" {
		t.Errorf("MAL-2026-5587: %q", got)
	}
	if got := e.number("SELECT count(*) FROM vuln_advisory WHERE withdrawn IS NOT NULL AND id IN ('MAL-2026-5587')"); got != 1 {
		t.Errorf("the withdrawn malicious record has no withdrawn time")
	}
	// Every record that carries a vector has a level from it, never unknown.
	if got := e.number("SELECT count(*) FROM vuln_advisory WHERE severity_source = 'cvss' AND (severity = 'unknown' OR cvss_score IS NULL OR cvss_vector IS NULL)"); got != 0 {
		t.Errorf("%d records scored from a vector have no level or score", got)
	}
	if got := e.number("SELECT count(*) FROM vuln_advisory WHERE severity_source = 'cvss' AND id LIKE 'GHSA-%'"); got < 5 {
		t.Errorf("only %d GitHub advisories were scored", got)
	}
}
