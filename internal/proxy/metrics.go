package proxy

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
)

// The metrics of the remote engine (R-OPS-009, R-REPO-047). Every one is per
// replica; the repository key is the label where the table of the specification
// has `repo`.
type metrics struct {
	lookups      *prometheus.CounterVec
	upstreamReqs *prometheus.CounterVec
	upstreamDur  *prometheus.HistogramVec
	upstreamUp   *prometheus.GaugeVec
	collapsed    *prometheus.CounterVec
	sourceNet    *prometheus.CounterVec
	bypass       *prometheus.CounterVec
	queueWait    *prometheus.HistogramVec
	integrity    *prometheus.CounterVec
	egressDenied *prometheus.CounterVec
	fillRefused  *prometheus.CounterVec
}

func newMetrics(reg prometheus.Registerer) *metrics {
	m := &metrics{
		lookups: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "trove_remote_cache_lookups_total",
			Help: "Pull-through cache outcomes of remote repositories.",
		}, []string{"repo", "kind", "result"}),
		upstreamReqs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "trove_remote_upstream_requests_total",
			Help: "Requests sent to an upstream, by status code or error.",
		}, []string{"repo", "code"}),
		upstreamDur: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "trove_remote_upstream_request_duration_seconds",
			Help:    "Upstream response time including the body.",
			Buckets: []float64{.01, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60, 120, 300},
		}, []string{"repo"}),
		upstreamUp: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "trove_remote_upstream_up",
			Help: "1 while the upstream counts as reachable, 0 while the circuit is open.",
		}, []string{"repo"}),
		collapsed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "trove_remote_collapsed_requests_total",
			Help: "Cache misses that waited for an in-flight fetch instead of fetching, on this replica or another.",
		}, []string{"repo"}),
		sourceNet: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "trove_remote_source_network_unavailable_total",
			Help: "Fetches answered 502 source_network_unavailable: this replica has no address in the remote's sourceNetwork.",
		}, []string{"repo"}),
		bypass: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "trove_flight_lock_bypass_total",
			Help: "Fetches that went ahead without the cross-replica lock.",
		}, []string{"reason"}),
		queueWait: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "trove_remote_fetch_queue_wait_seconds",
			Help:    "Time a fetch waited for a free fetch slot.",
			Buckets: []float64{.001, .01, .05, .1, .5, 1, 2.5, 5, 10},
		}, []string{"repo"}),
		integrity: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "trove_remote_integrity_failures_total",
			Help: "Upstream artifacts refused or kept apart because of a digest.",
		}, []string{"repo", "reason"}),
		egressDenied: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "trove_remote_egress_denied_total",
			Help: "Upstream requests refused by the egress address policy.",
		}, []string{"repo"}),
		fillRefused: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "trove_endpoint_cache_fill_refused_total",
			Help: "Upstream contacts the endpoint's cache-fill policy prevented.",
		}, []string{"endpoint"}),
	}
	if reg != nil {
		reg.MustRegister(m.lookups, m.upstreamReqs, m.upstreamDur, m.upstreamUp, m.collapsed, m.sourceNet,
			m.bypass, m.queueWait, m.integrity, m.egressDenied, m.fillRefused)
	}
	for _, r := range []string{bypassValkey, bypassWaitTimeout, bypassDraining} {
		m.bypass.WithLabelValues(r) // visible from the first scrape
	}
	return m
}

// The lookup results of trove_remote_cache_lookups_total.
const (
	resultHit      = "hit"
	resultMiss     = "miss"
	resultStale    = "stale"
	resultNegative = "negative_hit"
)

func (m *metrics) lookup(repo string, k Kind, result string) {
	kind := "metadata"
	if k == Artifact {
		kind = "artifact"
	}
	m.lookups.WithLabelValues(repo, kind, result).Inc()
}

// request counts an upstream request: status is the HTTP status, 0 for a transport
// error.
func (m *metrics) request(repo string, status int) {
	code := "error"
	if status > 0 {
		code = strconv.Itoa(status)
	}
	m.upstreamReqs.WithLabelValues(repo, code).Inc()
}
