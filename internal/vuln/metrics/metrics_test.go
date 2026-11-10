package metrics

import (
	"os"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/observability"
)

// R-VULN-024, R-OPS-007: the collectors register, every label is in the allowed list (which gained ecosystem and severity),
// and every name is a row of the specification.
func TestR_VULN_024_TheFamiliesFollowTheConventions(t *testing.T) {
	reg := prometheus.NewRegistry()
	m, err := New(reg)
	if err != nil {
		t.Fatal(err)
	}
	m.SyncRuns.WithLabelValues(SourceOSV, "npm", RunFull, RunChanged).Inc()
	m.SyncRecords.WithLabelValues(SourceOSV, "npm", RecordWritten).Add(3)
	m.UnevaluableRanges.WithLabelValues("NuGet").Inc()
	m.Evaluations.WithLabelValues("publish").Inc()
	m.EvaluationDuration.WithLabelValues("publish").Observe(0.002)
	m.DownloadsBlocked.WithLabelValues("npm", "malicious", ModeEnforce).Inc()
	m.AuditRequests.WithLabelValues("npm", "local", "ok").Inc()
	m.ImageScans.WithLabelValues("scanned").Inc()
	m.ImageScanDuration.Observe(2)
	fams, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	if len(fams) != 9 {
		t.Errorf("%d families gathered, want the 9 collectors", len(fams))
	}
	for _, e := range observability.CheckFamilies(fams) {
		t.Error(e)
	}
	if _, err := New(reg); err == nil {
		t.Error("a second New on the same registry registered twice")
	}
	if m2, err := New(nil); err != nil || m2 == nil {
		t.Errorf("New(nil) = %v, %v", m2, err)
	}
}

func TestR_VULN_024_TheGaugesUseAllowedLabelsAndTheNamesAreTheSpecs(t *testing.T) {
	for _, l := range []string{"ecosystem", "severity", "source", "status", "kind"} {
		if !observability.AllowedLabel(Findings, l) {
			t.Errorf("label %q is not allowed", l)
		}
	}
	if got := len(GaugeDescs()); got != 5 {
		t.Errorf("%d gauge descriptors, want 5", got)
	}
	spec, err := os.ReadFile("../../../docs/spec/vulnerability-scanning.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range Names() {
		if !strings.HasPrefix(n, "trove_vuln_") || !strings.Contains(string(spec), "`"+n+"`") {
			t.Errorf("%s is not a row of R-VULN-024", n)
		}
	}
	if len(Names()) != 14 {
		t.Errorf("%d names, want the 14 families of R-VULN-024", len(Names()))
	}
}
