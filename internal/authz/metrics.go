package authz

import (
	"errors"

	"github.com/prometheus/client_golang/prometheus"
)

// Triggers of an index build, the values of the trigger label of
// trove_authz_index_rebuilds_total (R-AUTHZ-028). The change log applies a Valkey
// message and a poll through the same listener call, so the two cannot be told
// apart here: both count as "poll", and "message" stays unused.
const (
	TriggerStartup  = "startup"
	TriggerPoll     = "poll"
	TriggerMessage  = "message"
	TriggerFlush    = "flush"
	TriggerSafety   = "safety"
	TriggerMinEpoch = "min_epoch"
)

// Outcomes of Trove-Authz-Min-Epoch handling, the values of the outcome label of
// trove_authz_min_epoch_total (R-AUTHZ-032).
const (
	MinEpochCurrent = "current"
	MinEpochWaited  = "waited"
	MinEpochUnknown = "unknown"
	MinEpochTimeout = "timeout"
)

// Outcomes of a wait for a stale index, the values of the outcome label of
// trove_authz_stale_waits_total (R-AUTHZ-028).
const (
	WaitDecided = "decided"
	WaitRefused = "refused"
)

type metrics struct {
	epoch     prometheus.Gauge
	rebuild   prometheus.Histogram
	rebuilds  *prometheus.CounterVec
	staleness prometheus.GaugeFunc
	waits     *prometheus.CounterVec
	eval      prometheus.Histogram
	decisions *prometheus.CounterVec
	minEpoch  *prometheus.CounterVec
	discarded prometheus.Counter
	// hidden and rules are the exposure families of R-EP-009; endpoints bounds
	// their endpoint label to the configured names (R-OPS-007).
	hidden    *prometheus.CounterVec
	rules     *prometheus.GaugeVec
	endpoints map[string]bool
}

// exposureHidden counts a request refused by exposure or an item removed from a
// listing by exposure (trove_exposure_hidden_total{endpoint,kind}).
func (m *metrics) exposureHidden(endpoint, kind string) {
	if endpoint == "" {
		return
	}
	if !m.endpoints[endpoint] {
		endpoint = "_other"
	}
	m.hidden.WithLabelValues(endpoint, kind).Inc()
}

// setExposureRules sets trove_exposure_rules from an installed index: the enabled
// rules per configured endpoint, as the build read them from PostgreSQL.
func (m *metrics) setExposureRules(x *Index) {
	for ep := range m.endpoints {
		m.rules.WithLabelValues(ep).Set(float64(x.exposedRules(ep)))
	}
}

func newMetrics(reg prometheus.Registerer, staleness func() float64, endpoints []string) (*metrics, error) {
	m := &metrics{
		epoch: prometheus.NewGauge(prometheus.GaugeOpts{Name: "trove_authz_epoch", Help: "The authz epoch of the installed grant index."}),
		rebuild: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "trove_authz_index_rebuild_seconds", Help: "Duration of a grant index build.",
			Buckets: []float64{0.005, 0.025, 0.1, 0.25, 0.5, 1, 2.5, 10},
		}),
		rebuilds: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "trove_authz_index_rebuilds_total", Help: "Grant index builds that were installed, by trigger (startup, poll, message, flush, safety, min_epoch).",
		}, []string{"trigger"}),
		staleness: prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "trove_authz_index_staleness_seconds", Help: "Seconds since the installed grant index was last confirmed current.",
		}, staleness),
		waits: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "trove_authz_stale_waits_total", Help: "Requests that waited for a stale grant index, by outcome (decided, refused).",
		}, []string{"outcome"}),
		eval: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "trove_authz_evaluation_seconds", Help: "Duration of one authorization decision on the compiled index.",
			Buckets: []float64{0.00001, 0.00005, 0.0001, 0.0005, 0.001, 0.005, 0.025, 0.1},
		}),
		decisions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "trove_authz_decisions_total", Help: "Authorization decisions by outcome, reason code and the first action asked.",
		}, []string{"outcome", "reason", "action"}),
		minEpoch: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "trove_authz_min_epoch_total", Help: "Requests that carried Trove-Authz-Min-Epoch, by outcome (current, waited, unknown, timeout).",
		}, []string{"outcome"}),
		discarded: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "trove_authz_index_builds_discarded_total", Help: "Finished builds that were not installed because their epochs were lower than the installed index's.",
		}),
		hidden: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "trove_exposure_hidden_total", Help: "Requests refused by an endpoint's exposure (kind request) and items removed from listings by it (kind item).",
		}, []string{"endpoint", "kind"}),
		rules: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "trove_exposure_rules", Help: "Enabled exposure rules per endpoint in the installed grant index; aggregate with max.",
		}, []string{"endpoint"}),
		endpoints: map[string]bool{},
	}
	for _, ep := range endpoints {
		m.endpoints[ep] = true
		m.rules.WithLabelValues(ep)
		m.hidden.WithLabelValues(ep, "request")
		m.hidden.WithLabelValues(ep, "item")
	}
	if reg == nil {
		return m, nil
	}
	cs := []prometheus.Collector{m.epoch, m.rebuild, m.rebuilds, m.staleness, m.waits, m.eval, m.decisions, m.minEpoch, m.discarded}
	if len(endpoints) > 0 {
		cs = append(cs, m.hidden, m.rules)
	}
	for _, c := range cs {
		if err := reg.Register(c); err != nil {
			var are prometheus.AlreadyRegisteredError
			if errors.As(err, &are) {
				return nil, errors.New("authz: metrics are already registered on this registerer")
			}
			return nil, err
		}
	}
	return m, nil
}
