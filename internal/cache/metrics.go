package cache

import (
	"errors"

	"github.com/prometheus/client_golang/prometheus"
)

// metrics are the cache families of R-ARCH-018: requests by result, flushes by
// reason, and the size of every cache.
type metrics struct {
	requests *prometheus.CounterVec
	flushes  *prometheus.CounterVec
	entries  *prometheus.GaugeVec
	bytes    *prometheus.GaugeVec
}

func newMetrics(reg prometheus.Registerer) (*metrics, error) {
	m := &metrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "trove_cache_requests_total", Help: "Lookups in the in-process caches by result (hit, miss, expired, fill_skipped).",
		}, []string{"cache", "result"}),
		flushes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "trove_cache_flushes_total", Help: "Whole-cache flushes by reason.",
		}, []string{"cache", "reason"}),
		entries: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "trove_cache_entries", Help: "Entries held by an in-process cache.",
		}, []string{"cache"}),
		bytes: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "trove_cache_bytes", Help: "Weighted size of an in-process cache.",
		}, []string{"cache"}),
	}
	if reg == nil {
		return m, nil
	}
	for _, c := range []prometheus.Collector{m.requests, m.flushes, m.entries, m.bytes} {
		if err := reg.Register(c); err != nil {
			var are prometheus.AlreadyRegisteredError
			if errors.As(err, &are) {
				return nil, errors.New("cache: metrics are already registered on this registerer")
			}
			return nil, err
		}
	}
	return m, nil
}
