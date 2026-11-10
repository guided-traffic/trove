package ansible

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/observability"
)

// virtualMetrics are the families of R-ANS-030 that the virtual class counts. The label repo is bounded by the repository limit
// of the metrics section.
type virtualMetrics struct {
	repos    *observability.CappedLabels
	conflict *prometheus.CounterVec
}

const metricDigestConflicts = "trove_ansible_virtual_digest_conflicts_total"

func newVirtualMetrics(m *observability.Metrics) *virtualMetrics {
	v := &virtualMetrics{
		repos: m.Repos(),
		conflict: m.NewCounterVec(metricDigestConflicts,
			"Versions that members of a virtual repository hold with different archive digests: the winner's entry is served and the others are counted once per merge.", "repo"),
	}
	v.repos.DeleteOnForget(v.conflict) // the series leave with the repository (R-OPS-007)
	return v
}

// conflicts counts n versions with differing digests in one merge.
func (v *virtualMetrics) conflicts(repoKey string, n int) {
	if v == nil || n <= 0 {
		return
	}
	v.conflict.WithLabelValues(v.repos.Value(metricDigestConflicts, repoKey)).Add(float64(n))
}

// virtualKey is the key of the virtual class's metrics in replica.ext (bind.go).
type virtualKey struct{}

func init() {
	registerBinder(func(r *replica, m *observability.Metrics) { r.ext[virtualKey{}] = newVirtualMetrics(m) })
}

// virtualMetricsOf finds the metrics of a request's Host; nil on a Host nobody bound with a registry.
func (f *Format) virtualMetricsOf(h formats.Host) *virtualMetrics {
	if v, ok := f.replicaOf(h).ext[virtualKey{}].(*virtualMetrics); ok {
		return v
	}
	return nil
}
