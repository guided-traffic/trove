package app

import (
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

// TestR_VULN_024_VulnerabilityDataFacts: the block of the system information lists every configured source, an empty one
// for a source without a row (the state a restore leaves), and marks stale data.
func TestR_VULN_024_VulnerabilityDataFacts(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	var v config.Vulnerabilities
	v.Enabled, v.Enforcement, v.StaleAfter = true, "enforce", 24*time.Hour
	v.Sources.OSV.Mode, v.Sources.KEV.Mode = "online", "repository"
	rows := []store.SourceState{
		{Source: "osv", Ecosystem: "npm", State: "complete", SyncedAt: now.Add(-48 * time.Hour), Failures: 3},
		{Source: "osv", Ecosystem: "PyPI", State: "complete", SyncedAt: now},
	}
	got := vulnFactsFrom(v, rows, now)
	if got.Enforcement != gen.VulnerabilityDataFactsEnforcement("enforce") {
		t.Errorf("enforcement = %q", got.Enforcement)
	}
	by := map[string]vulnFactSource{}
	for _, s := range got.Sources {
		by[string(s.Source)+"/"+s.Ecosystem] = s
	}
	if len(by) != 4 {
		t.Fatalf("sources = %d, want npm, NuGet, PyPI, kev: %+v", len(by), got.Sources)
	}
	if s := by["osv/npm"]; !s.Stale || s.State != "complete" || s.Failures != 3 || s.SyncedAt == nil || s.Mode != gen.VulnerabilityDataFactsSourcesModeOnline {
		t.Errorf("npm = %+v", s)
	}
	if s := by["osv/NuGet"]; s.State != "empty" || s.Stale || s.SyncedAt != nil {
		t.Errorf("NuGet without a row = %+v", s)
	}
	if s := by["osv/PyPI"]; s.Mode != gen.VulnerabilityDataFactsSourcesModeOff {
		t.Errorf("a stored ecosystem the configuration does not name has mode %q, want off", s.Mode)
	}
	if s := by["kev/"]; s.Mode != gen.VulnerabilityDataFactsSourcesModeRepository {
		t.Errorf("kev = %+v", s)
	}
	v.Enabled = false
	for _, s := range vulnFactsFrom(v, rows, now).Sources {
		if s.Mode != gen.VulnerabilityDataFactsSourcesModeOff {
			t.Errorf("scanning disabled, %s/%s has mode %q", s.Source, s.Ecosystem, s.Mode)
		}
	}
}
