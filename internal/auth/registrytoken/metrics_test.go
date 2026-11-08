package registrytoken

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// R-OCI-034 (the token service's metric): named in the test name by WP3 once /v2/ is mounted and the grant check runs.
func TestTokenServiceMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	m, err := NewMetrics(reg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewMetrics(reg); err == nil {
		t.Fatal("a second registration succeeded")
	}
	m.Count(OutcomeIssued)
	m.Count(OutcomeIssued)
	m.Count(OutcomeDenied)
	var nilMetrics *Metrics
	nilMetrics.Count(OutcomeIssued)
	value := func(outcome string) float64 {
		var out dto.Metric
		if err := m.tokens.WithLabelValues(outcome).Write(&out); err != nil {
			t.Fatal(err)
		}
		return out.GetCounter().GetValue()
	}
	if got := value(OutcomeIssued); got != 2 {
		t.Fatalf("issued = %v", got)
	}
	if got := value(OutcomeAnonymous); got != 0 {
		t.Fatalf("anonymous = %v", got)
	}
	families, err := reg.Gather()
	if err != nil || len(families) != 1 || families[0].GetName() != "trove_oci_registry_tokens_total" || len(families[0].GetMetric()) != 3 {
		t.Fatalf("families %v %v", families, err)
	}
}
