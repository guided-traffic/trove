package nuget

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/observability"
)

// reasonUnavailable is the reason of a member that took no part in a merge because it could
// not be asked (R-NUGET-017: the merge is partial and not cached).
const reasonUnavailable = "unavailable"

// virtualMetrics are the families of R-NUGET-028 that the virtual class counts. The label
// repository is bounded by the repository limit of the metrics section.
type virtualMetrics struct {
	repos   *observability.CappedLabels
	counter *prometheus.CounterVec
}

func newVirtualMetrics(m *observability.Metrics) *virtualMetrics {
	v := &virtualMetrics{
		repos: m.Repos(),
		counter: m.NewCounterVec("trove_nuget_virtual_member_skipped_total",
			"Answers of a virtual repository that left out a member (reason unavailable: the member could not be asked, so the merge is partial and not cached).", "repo", "reason"),
	}
	v.repos.DeleteOnForget(v.counter) // the series leave with the repository (R-OPS-007)
	return v
}

// skipped counts an answer that left out a member.
func (v *virtualMetrics) skipped(repoKey, reason string) {
	if v == nil {
		return
	}
	v.counter.WithLabelValues(v.repos.Value("trove_nuget_virtual_member_skipped_total", repoKey), reason).Inc()
}
