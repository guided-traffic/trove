package nuget

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/observability"
)

// writeMetrics are the families of R-NUGET-028 that the write side counts. The label
// repository is bounded by the repository limit of the metrics section.
type writeMetrics struct {
	repos   *observability.CappedLabels
	replays *prometheus.CounterVec
}

func newWriteMetrics(m *observability.Metrics) *writeMetrics {
	return &writeMetrics{
		repos: m.Repos(),
		replays: m.NewCounterVec("trove_nuget_push_replayed_total",
			"Pushes of the identical package of a stored version, answered 201 without a change (R-NUGET-006).", "repo"),
	}
}

// replayed counts a push that was the repeat of a push that had committed.
func (w *writeMetrics) replayed(repoKey string) {
	if w == nil {
		return
	}
	w.replays.WithLabelValues(w.repos.Value("trove_nuget_push_replayed_total", repoKey)).Inc()
}
