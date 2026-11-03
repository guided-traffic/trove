package store

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/config"
	vmetrics "github.com/guided-traffic/trove/internal/vuln/metrics"
)

type fixedCollector struct {
	descs   []*prometheus.Desc
	metrics []prometheus.Metric
}

func (f fixedCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range f.descs {
		ch <- d
	}
}

func (f fixedCollector) Collect(ch chan<- prometheus.Metric) {
	for _, m := range f.metrics {
		ch <- m
	}
}

func collect(t *testing.T, exp []ExpectedSource, eco []string, st []SourceState, f []FindingCount, c []ClaimedMatchCount) *prometheus.Registry {
	t.Helper()
	descs := vmetrics.GaugeDescs()
	reg := prometheus.NewPedanticRegistry()
	if err := reg.Register(fixedCollector{descs, gaugeMetrics(descs, exp, eco, st, f, c)}); err != nil {
		t.Fatal(err)
	}
	return reg
}

func value(t *testing.T, reg *prometheus.Registry, name string, labels map[string]string) (float64, bool) {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
	next:
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if want, ok := labels[l.GetName()]; ok && want != l.GetValue() {
					continue next
				}
			}
			return m.GetGauge().GetValue(), true
		}
	}
	return 0, false
}

func seriesOf(t *testing.T, reg *prometheus.Registry, name string) int {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() == name {
			return len(mf.GetMetric())
		}
	}
	return 0
}

// TestR_VULN_024_GaugesOfTheSources: the three source gauges carry a series for every configured source, a row of
// vuln_source_state or not, and none for a source the configuration does not sync.
func TestR_VULN_024_GaugesOfTheSources(t *testing.T) {
	synced := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	reg := collect(t,
		[]ExpectedSource{{"osv", "npm"}, {"osv", "NuGet"}, {"kev", ""}}, []string{"npm", "NuGet"},
		[]SourceState{
			{Source: "osv", Ecosystem: "npm", State: "complete", SyncedAt: synced, Records: 231000, Failures: 3},
			{Source: "osv", Ecosystem: "PyPI", State: "complete", SyncedAt: synced, Records: 5},
		}, nil, nil)

	if v, ok := value(t, reg, vmetrics.SourceLastSuccess, map[string]string{"source": "osv", "ecosystem": "npm"}); !ok || v != float64(synced.Unix()) {
		t.Errorf("last success of npm = %v, %v; want %d", v, ok, synced.Unix())
	}
	if v, _ := value(t, reg, vmetrics.SourceRecords, map[string]string{"ecosystem": "npm"}); v != 231000 {
		t.Errorf("records of npm = %v", v)
	}
	if v, _ := value(t, reg, vmetrics.SourceFailures, map[string]string{"ecosystem": "npm"}); v != 3 {
		t.Errorf("failures of npm = %v", v)
	}
	// A source without a row (a restore removed it, or it never ran) has the value 0, so the stale alert fires on it.
	for _, want := range []map[string]string{{"source": "osv", "ecosystem": "NuGet"}, {"source": "kev", "ecosystem": ""}} {
		v, ok := value(t, reg, vmetrics.SourceLastSuccess, want)
		if !ok || v != 0 {
			t.Errorf("last success of %v = %v, %v; want a series with 0", want, v, ok)
		}
	}
	if _, ok := value(t, reg, vmetrics.SourceLastSuccess, map[string]string{"ecosystem": "PyPI"}); ok {
		t.Error("a series for an ecosystem the configuration does not sync")
	}
}

// TestR_VULN_024_GaugeOfTheFindings: every severity, status and kind has a series, so a count falls to 0 instead of vanishing.
func TestR_VULN_024_GaugeOfTheFindings(t *testing.T) {
	reg := collect(t, nil, nil, nil, []FindingCount{{"high", "blocked", "malicious", 2}, {"low", "dismissed", "vulnerability", 7}}, nil)
	if v, ok := value(t, reg, vmetrics.Findings, map[string]string{"severity": "high", "status": "blocked", "kind": "malicious"}); !ok || v != 2 {
		t.Errorf("blocked malicious high = %v, %v", v, ok)
	}
	if v, ok := value(t, reg, vmetrics.Findings, map[string]string{"severity": "critical", "status": "needs_triage", "kind": "vulnerability"}); !ok || v != 0 {
		t.Errorf("an empty combination = %v, %v; want a series with 0", v, ok)
	}
	if n := seriesOf(t, reg, vmetrics.Findings); n != 6*5*2 {
		t.Errorf("series of trove_vuln_findings = %d, want 60 (bounded, R-OPS-007)", n)
	}
}

// TestR_VULN_024_GaugeOfTheClaimedNameMatches: the malicious kind has a series for every ecosystem, which the critical
// alert reads.
func TestR_VULN_024_GaugeOfTheClaimedNameMatches(t *testing.T) {
	reg := collect(t, nil, []string{"npm", "NuGet"}, nil, nil, []ClaimedMatchCount{{"npm", "malicious", 1}})
	if v, _ := value(t, reg, vmetrics.ClaimedNameMatches, map[string]string{"ecosystem": "npm", "kind": "malicious"}); v != 1 {
		t.Errorf("npm malicious = %v", v)
	}
	if v, ok := value(t, reg, vmetrics.ClaimedNameMatches, map[string]string{"ecosystem": "NuGet", "kind": "malicious"}); !ok || v != 0 {
		t.Errorf("NuGet malicious = %v, %v; want 0", v, ok)
	}
}

func TestExpectedSources(t *testing.T) {
	var v config.Vulnerabilities
	v.Enabled = true
	v.Sources.OSV.Mode, v.Sources.KEV.Mode = "online", "online"
	if got := ExpectedSources(v); len(got) != 3 || got[2] != (ExpectedSource{"kev", ""}) {
		t.Errorf("default = %v", got)
	}
	v.Sources.OSV.Ecosystems = []string{"npm"}
	v.Sources.KEV.Mode = "off"
	if got := ExpectedSources(v); len(got) != 1 || got[0] != (ExpectedSource{"osv", "npm"}) {
		t.Errorf("narrowed, kev off = %v", got)
	}
	v.Enabled = false
	if got := ExpectedSources(v); got != nil {
		t.Errorf("disabled = %v", got)
	}
}
