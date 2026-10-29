package cluster

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
)

// Mechanisms are the Valkey uses of R-ARCH-030, the values of the mechanism
// label of trove_valkey_degraded_total (R-OPS-007).
const (
	MechanismFlightLock    = "flight_lock"
	MechanismNegativeCache = "negative_cache"
	MechanismRateLimit     = "rate_limit"
	MechanismChangeMessage = "change_message"
	MechanismSignal        = "signal"
	MechanismProbe         = "probe"
)

var mechanisms = map[string]bool{
	MechanismFlightLock: true, MechanismNegativeCache: true, MechanismRateLimit: true,
	MechanismChangeMessage: true, MechanismSignal: true, MechanismProbe: true,
}

// valkeyMetrics are the trove_valkey_* families of R-HA-057 and R-ARCH-031.
type valkeyMetrics struct {
	available          prometheus.Gauge
	circuitOpen        prometheus.Gauge
	sentinelsReachable prometheus.Gauge
	pubsubSubscribed   prometheus.Gauge
	primaryChanges     prometheus.Counter
	errors             *prometheus.CounterVec
	duration           *prometheus.HistogramVec
	degraded           *prometheus.CounterVec
	cacheLookups       *prometheus.CounterVec
}

func newValkeyMetrics(reg prometheus.Registerer) (*valkeyMetrics, error) {
	m := &valkeyMetrics{
		available: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "trove_valkey_available",
			Help: "1 while this replica can use Valkey, 0 while it runs its fallbacks.",
		}),
		circuitOpen: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "trove_valkey_circuit_open",
			Help: "1 while the circuit breaker around Valkey calls is open or half-open.",
		}),
		sentinelsReachable: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "trove_valkey_sentinels_reachable",
			Help: "Configured Sentinels that answered the last probe of all of them.",
		}),
		pubsubSubscribed: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "trove_valkey_pubsub_subscribed",
			Help: "1 while the Pub/Sub connection is subscribed on the current primary.",
		}),
		primaryChanges: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "trove_valkey_primary_changes_total",
			Help: "Changes of the Valkey primary this replica has followed.",
		}),
		errors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "trove_valkey_errors_total",
			Help: "Failed Valkey calls and connection attempts by kind.",
		}, []string{"kind"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "trove_valkey_command_duration_seconds",
			Help:    "Duration of Valkey calls by command.",
			Buckets: []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1},
		}, []string{"command"}),
		degraded: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "trove_valkey_degraded_total",
			Help: "Times a mechanism ran its fallback because Valkey was unavailable.",
		}, []string{"mechanism"}),
		cacheLookups: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "trove_valkey_cache_lookups_total",
			Help: "Lookups in Valkey after a miss in the in-process cache.",
		}, []string{"cache", "result"}),
	}
	for _, c := range []prometheus.Collector{
		m.available, m.circuitOpen, m.sentinelsReachable, m.pubsubSubscribed, m.primaryChanges,
		m.errors, m.duration, m.degraded, m.cacheLookups,
	} {
		if err := reg.Register(c); err != nil {
			return nil, fmt.Errorf("register valkey metrics: %w", err)
		}
	}
	return m, nil
}
