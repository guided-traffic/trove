package enforce

import (
	"context"
	"testing"

	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/vuln"
)

// R-VULN-015, Q-VULN-75: the missing-data rule applies only to a covered policy, whatever the data state; a repository whose
// versions are never in scope (internal, not covered) is never refused for missing data, on a stored item and on a miss.
func TestR_VULN_015_MissingDataAppliesOnlyToACoveredPolicy(t *testing.T) {
	for _, tc := range []struct {
		name      string
		covered   bool
		onMissing vuln.OnMissingData
		complete  bool
		want      bool
	}{
		{"covered block incomplete", true, vuln.MissingBlock, false, true},
		{"covered block complete", true, vuln.MissingBlock, true, false},
		{"covered allow incomplete", true, vuln.MissingAllow, false, false},
		{"covered allow complete", true, vuln.MissingAllow, true, false},
		{"not covered block incomplete", false, vuln.MissingBlock, false, false},
		{"not covered block complete", false, vuln.MissingBlock, true, false},
		{"not covered allow incomplete", false, vuln.MissingAllow, false, false},
		{"not covered allow complete", false, vuln.MissingAllow, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, states{"npm": {Complete: tc.complete}}, vuln.EnforceBlocks)
			p := scanned
			p.Covered, p.OnMissingData = tc.covered, tc.onMissing
			if b := f.e.Decide(p, vuln.BlockRow{}, 7); b.Active() != tc.want {
				t.Errorf("Decide = %+v, want active %v", b, tc.want)
			}
			m := Miss{Repo: repository.Repository{Name: "npm-remote", Format: "npm"}, Package: "p", Version: "1.0.0"}
			if err := f.e.gate(context.Background(), p, m); (err != nil) != tc.want {
				t.Errorf("gate = %v, want refusal %v", err, tc.want)
			}
		})
	}
}

// R-VULN-015: a finding blocks whether or not the policy is covered; only the missing-data rule is scoped.
func TestR_VULN_015_ANotCoveredPolicyStillBlocksOnAFinding(t *testing.T) {
	f := newFixture(t, states{}, vuln.EnforceBlocks)
	p := scanned
	p.Covered, p.OnMissingData = false, vuln.MissingBlock
	if b := f.e.Decide(p, malRow(), 7); !b.Refused() || b.Reason != vuln.BlockMalicious {
		t.Errorf("a malicious finding in a not covered repository: %+v", b)
	}
}

// Q-VULN-75: PolicyOf fills Covered from the option; without the option every repository is covered (fail-closed).
func TestR_VULN_015_PolicyOfFillsCovered(t *testing.T) {
	r := repository.Repository{Name: "npm-remote", Format: "npm"}
	st := states{"npm": {}}
	if p := New(Options{Enabled: true, States: st}).PolicyOf(r); !p.Covered {
		t.Error("an enforcer without the option does not cover")
	}
	e := New(Options{Enabled: true, States: st, Covered: func(repository.Repository) bool { return false }})
	if p := e.PolicyOf(r); p.Covered {
		t.Error("the option's answer is not applied")
	}
}
