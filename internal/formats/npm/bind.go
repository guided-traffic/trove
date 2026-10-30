package npm

import (
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/observability"
)

// A format is registered once per process, and a process may hold several
// replicas (the integration tests start them in one), each with its own settings
// and its own metrics registry. What belongs to a replica is therefore kept per
// Host, the one object a request carries that is the replica's: the composition
// root calls [Format.Bind] with the Host it built, before the replica serves, and
// a handler finds its replica by rq.Host. A request on a Host that was never
// bound (the conformance kit's) runs on the defaults, without metrics.

// replica is what the composition root bound for one Host.
type replica struct {
	cfg config.NPM
	met *metrics
}

// Bind gives the format the npm section of the configuration and the metrics
// registry of the replica that owns the Host h (R-NPM-007 limits, R-NPM-044). It
// registers the metric families on m and must be called once per Host, before the
// replica serves.
func (f *Format) Bind(h formats.Host, cfg *config.Config, m *observability.Metrics) {
	r := &replica{cfg: cfg.NPM}
	if m != nil {
		r.met = newMetrics(m)
	}
	f.replicas.Store(h, r)
}

// replicaOf finds the replica of a request's Host.
func (f *Format) replicaOf(h formats.Host) *replica {
	if r, ok := f.replicas.Load(h); ok {
		return r.(*replica)
	}
	return defaultReplica()
}

// defaultReplica is the replica of a Host nobody bound: the defaults of the npm
// section of the configuration.
func defaultReplica() *replica {
	return &replica{cfg: config.NPM{
		Publish: config.NPMPublish{
			MaxTarballSize: 256 * config.MiB, MaxUnpackedSize: 2 * config.GiB, MaxTarEntries: 100000,
			ManifestConsistency: "reject", RetryWindow: 10 * time.Minute,
		},
		AbbreviatedTime: true,
	}}
}

// metrics are the families of R-NPM-044 this format exports. The label that
// R-NPM-044 calls repository is "repo" in the label list of R-OPS-007, capped by
// the repository limit of the metrics section.
type metrics struct {
	repos    *observability.CappedLabels
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
	retries  *prometheus.CounterVec
}

func newMetrics(m *observability.Metrics) *metrics {
	return &metrics{
		repos: m.Repos(),
		requests: m.NewCounterVec("trove_npm_requests_total",
			"npm protocol requests by repository class, operation and status code.", "class", "operation", "status"),
		duration: m.NewHistogramVec("trove_npm_request_duration_seconds",
			"Time from the start of an npm protocol operation to the end of its handler.", observability.HTTPBuckets(), "class", "operation"),
		retries: m.NewCounterVec("trove_npm_publish_retries_acknowledged_total",
			"Publish requests acknowledged as the retry of a publish whose answer was lost (R-NPM-007 step 4).", "repo"),
	}
}

func (m *metrics) observe(class, op string, status int, d time.Duration) {
	if m == nil {
		return
	}
	m.requests.WithLabelValues(class, op, strconv.Itoa(status)).Inc()
	m.duration.WithLabelValues(class, op).Observe(d.Seconds())
}

func (m *metrics) retryAcknowledged(repoKey string) {
	if m == nil {
		return
	}
	m.retries.WithLabelValues(m.repos.Value("trove_npm_publish_retries_acknowledged_total", repoKey)).Inc()
}

// replicas is the registry of bound hosts, a field of the format and not a package
// variable.
type replicas struct{ sync.Map }
