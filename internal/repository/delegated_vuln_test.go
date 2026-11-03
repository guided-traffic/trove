package repository

import (
	"slices"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/vuln"
)

// TestR_VULN_002_AProjectAdministratorDoesNotWeakenTheScanning: manageRepositories alone changes no member that lifts a download
// block (Q-VULN-69); the triage capability, or a holder of repositories:write-config (no delegate), does.
func TestR_VULN_002_AProjectAdministratorDoesNotWeakenTheScanning(t *testing.T) {
	def := vuln.DefaultSettings()
	with := func(f func(*vuln.Settings)) *Document {
		s := def
		f(&s)
		return &Document{Vulnerabilities: &s}
	}
	strict := vuln.DefaultSettings()
	strict.OnMissingData, strict.MatchPublicAdvisories = vuln.MissingBlock, vuln.MatchAlways
	for _, c := range []struct {
		name     string
		cur, nd  *Document
		write    Write
		wantWeak []string
	}{
		{"scanning off", &Document{}, with(func(s *vuln.Settings) { s.Enabled = false }), Write{Delegate: "web"}, []string{"enabled"}},
		{"never matches", &Document{}, with(func(s *vuln.Settings) { s.MatchPublicAdvisories = vuln.MatchNever }), Write{Delegate: "web"}, []string{"matchPublicAdvisories"}},
		{"always to auto", &Document{Vulnerabilities: &strict}, with(func(s *vuln.Settings) { s.OnMissingData = vuln.MissingBlock }), Write{Delegate: "web"}, []string{"matchPublicAdvisories"}},
		{"malicious block off", &Document{}, with(func(s *vuln.Settings) { s.BlockMalicious = false }), Write{Delegate: "web"}, []string{"blockMalicious"}},
		{"missing data block off", &Document{Vulnerabilities: &strict}, with(func(s *vuln.Settings) { s.MatchPublicAdvisories = vuln.MatchAlways }), Write{Delegate: "web"}, []string{"onMissingData"}},
		{"a replace that leaves the member out resets always", &Document{Vulnerabilities: &strict}, &Document{}, Write{Delegate: "web"}, []string{"matchPublicAdvisories", "onMissingData"}},
		{"strengthening is allowed", &Document{}, with(func(s *vuln.Settings) { s.OnMissingData = vuln.MissingBlock }), Write{Delegate: "web"}, nil},
		{"the triage capability may weaken", &Document{}, with(func(s *vuln.Settings) { s.Enabled = false }), Write{Delegate: "web", MayWeakenVulnerabilities: true}, []string{"enabled"}},
		{"a holder of write-config is no delegate", &Document{}, with(func(s *vuln.Settings) { s.Enabled = false }), Write{}, []string{"enabled"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := weakenedVulnerabilities(c.cur, c.nd); !slices.Equal(got, c.wantWeak) {
				t.Fatalf("weakened = %v, want %v", got, c.wantWeak)
			}
			err := delegatedVulnerabilities(c.write, c.cur, c.nd)
			refused := len(c.wantWeak) > 0 && c.write.Delegate != "" && !c.write.MayWeakenVulnerabilities
			if refused != (err != nil) {
				t.Fatalf("refused = %v, err = %v", refused, err)
			}
			if e, _ := errs.As(err); err != nil && (e == nil || e.Code != errs.CodeVulnerabilityWeakeningRefused) {
				t.Fatalf("code of %v", err)
			}
		})
	}
}
