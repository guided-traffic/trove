package nuget

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/observability"
)

// searchMetrics are the families of R-NUGET-028 that the forwarded search of a remote
// repository counts (R-NUGET-016). The label repo is the remote repository and is bounded by the
// repository limit of the metrics section.
type searchMetrics struct {
	repos    *observability.CappedLabels
	withheld *prometheus.CounterVec
	failures *prometheus.CounterVec
}

func newSearchMetrics(m *observability.Metrics) *searchMetrics {
	return &searchMetrics{
		repos: m.Repos(),
		withheld: m.NewCounterVec("trove_nuget_search_withheld_total",
			"Searches a remote repository answered from its cache because a term of the query names a package that an exclude pattern, a claim or a pin rule keeps away from it (R-NUGET-016).", "repo"),
		failures: m.NewCounterVec("trove_nuget_search_forward_failures_total",
			"Forwarded searches of a remote repository that the upstream did not answer usefully (a failure, an open circuit, no search resource, a document that is no search result), answered from the cache (R-NUGET-016).", "repo"),
	}
}

// withheldQuery counts a query that was not forwarded to a remote's upstream.
func (s *searchMetrics) withheldQuery(repoKey string) {
	if s == nil {
		return
	}
	s.withheld.WithLabelValues(s.repos.Value("trove_nuget_search_withheld_total", repoKey)).Inc()
}

// forwardFailed counts a forwarded search that fell back to the cache because of the upstream.
func (s *searchMetrics) forwardFailed(repoKey string) {
	if s == nil {
		return
	}
	s.failures.WithLabelValues(s.repos.Value("trove_nuget_search_forward_failures_total", repoKey)).Inc()
}
