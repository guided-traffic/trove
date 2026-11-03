package maintest

import (
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// Value returns the current value of a counter or a gauge, for the assertions of the tests on the Bastion's
// metrics.
func Value(m prometheus.Metric) float64 {
	var d dto.Metric
	if err := m.Write(&d); err != nil {
		return -1
	}
	switch {
	case d.Counter != nil:
		return d.Counter.GetValue()
	case d.Gauge != nil:
		return d.Gauge.GetValue()
	}
	return -1
}
