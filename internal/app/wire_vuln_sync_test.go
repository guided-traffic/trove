package app

import (
	"slices"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/match"
)

// R-VULN-003, R-VULN-025: the configuration validates ecosystems against a fixed list of the release (it is a leaf and cannot
// read the format registry); the registry is the source of the list, so every ecosystem a registered format serves must be in it,
// or a format's ecosystem could never be configured.
func TestR_VULN_025_TheConfigurationListCoversTheRegisteredEcosystems(t *testing.T) {
	known := config.VulnerabilityEcosystems()
	for _, e := range catalog.Default().List() {
		if e.Ecosystem != "" && !slices.Contains(known, e.Ecosystem) {
			t.Errorf("the format %s serves the ecosystem %s, which config.VulnerabilityEcosystems does not list", e.ID, e.Ecosystem)
		}
	}
}

type fakeAdapter struct{ eco string }

func (f fakeAdapter) Ecosystem() string                      { return f.eco }
func (fakeAdapter) AdvisoryName(n string) (string, bool)     { return n, true }
func (fakeAdapter) AdvisoryVersion(v string) (string, bool)  { return v, true }
func (fakeAdapter) CompareVersions(a, b string) (int, error) { return strings.Compare(a, b), nil }

// R-VULN-003: the ecosystems the configuration names may only narrow the set the formats serve; a name no format serves stops
// the start-up naming the key.
func TestR_VULN_003_EcosystemsMayOnlyNarrowTheAdapterList(t *testing.T) {
	have := map[string]match.Adapter{"npm": fakeAdapter{"npm"}, "NuGet": fakeAdapter{"NuGet"}}
	if err := checkVulnEcosystems(nil, have); err != nil {
		t.Errorf("no list: %v", err)
	}
	if err := checkVulnEcosystems([]string{"npm"}, have); err != nil {
		t.Errorf("a narrower list: %v", err)
	}
	err := checkVulnEcosystems([]string{"npm", "PyPI"}, have)
	if err == nil || !strings.Contains(err.Error(), "vulnerabilities.sources.osv.ecosystems") || !strings.Contains(err.Error(), "PyPI") ||
		!strings.Contains(err.Error(), "NuGet, npm") {
		t.Errorf("a wider list: %v", err)
	}
}

// The three kinds and their tasks of the sync are registered with the queue, the exclusive mode and the one attempt of
// R-VULN-004, the two OSV tasks under one dedup key.
func TestR_VULN_004_TheSyncKindsAreRegisteredAsTheSpecificationHasThem(t *testing.T) {
	a := &App{cfg: &config.Config{}}
	r := jobs.NewRegistry()
	a.registerVulnSyncJobs(r)
	for _, kind := range []string{vuln.KindSyncOSV, vuln.KindSyncOSVFull, vuln.KindSyncKEV} {
		spec, ok := r.Kind(kind)
		if !ok {
			t.Fatalf("kind %s is not registered", kind)
		}
		if spec.Queue != vuln.QueueVuln || spec.DedupMode != jobs.Exclusive || spec.MaxAttempts != 1 {
			t.Errorf("%s: %+v", kind, spec)
		}
	}
	tasks := r.Tasks()
	for name, key := range map[string]string{vuln.TaskSyncOSV: vuln.DedupSyncOSV, vuln.TaskSyncOSVFull: vuln.DedupSyncOSV, vuln.TaskSyncKEV: ""} {
		task, ok := tasks[name]
		if !ok || task.DedupKey != key {
			t.Errorf("task %s: %+v", name, task)
		}
	}
}
