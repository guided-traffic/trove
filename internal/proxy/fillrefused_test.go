package proxy

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// TestR_EP_019_FillRefusedCountsTheEndpoint: the counter the virtual engine reports a
// member it did not ask to is the engine's own family, labelled with the endpoint.
func TestR_EP_019_FillRefusedCountsTheEndpoint(t *testing.T) {
	reg := prometheus.NewRegistry()
	e := &Engine{m: newMetrics(reg)}
	e.FillRefused("customers")
	e.FillRefused("customers")
	count := func(endpoint string) float64 {
		var m dto.Metric
		if err := e.m.fillRefused.WithLabelValues(endpoint).Write(&m); err != nil {
			t.Fatal(err)
		}
		return m.GetCounter().GetValue()
	}
	if got := count("customers"); got != 2 {
		t.Fatalf("trove_endpoint_cache_fill_refused_total{customers} = %v", got)
	}
	if got := count("default"); got != 0 {
		t.Fatalf("another endpoint counted: %v", got)
	}
}
