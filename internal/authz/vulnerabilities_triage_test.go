package authz

import (
	"slices"
	"testing"
)

// R-VULN-013, R-VULN-019, Q-VULN-27: the capability vulnerabilities:triage belongs to admin and auditor in the MVP, to no other
// role, and is a system scope item (R-AUTH-025), so a token's scope can carry it (R-AUTHZ-013).
func TestQ_VULN_27_TriageBelongsToAdminAndAuditor(t *testing.T) {
	if !VulnerabilitiesTriage.Valid() || string(VulnerabilitiesTriage) != "vulnerabilities:triage" {
		t.Fatalf("not a capability: %v", Capabilities())
	}
	if !slices.Contains(Capabilities(), VulnerabilitiesTriage) {
		t.Fatal("the system scope items (Capabilities) do not list it")
	}
	want := map[Role]bool{RoleAdmin: true, RoleAuditor: true, RoleSecurityAdmin: true}
	for _, r := range Roles() {
		if got := roleCaps(r).has(VulnerabilitiesTriage); got != want[r] {
			t.Errorf("%s holds vulnerabilities:triage = %t, want %t", r, got, want[r])
		}
		if got := slices.Contains(RoleCapabilities(r), VulnerabilitiesTriage); got != want[r] {
			t.Errorf("RoleCapabilities(%s) holds it = %t, want %t", r, got, want[r])
		}
	}
}

// The auditor holds the triage next to content:metadata-read and still holds no write capability of the installation
// (Q-AUTHZ-172): the one write it has is the security metadata of R-VULN-013.
func TestQ_VULN_27_TheAuditorGainsNothingElse(t *testing.T) {
	caps := RoleCapabilities(RoleAuditor)
	for _, c := range caps {
		if c == VulnerabilitiesTriage {
			continue
		}
		if s := string(c); len(s) > 6 && s[len(s)-6:] == ":write" || c == ContentBypass || c == TokensRevoke || c == SecuritySettingsWrite {
			t.Errorf("the auditor holds %s", c)
		}
	}
}
