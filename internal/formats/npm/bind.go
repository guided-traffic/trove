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
	// shutdown is server.shutdown_timeout, which bounds an audit forward (R-NPM-038).
	shutdown time.Duration
	met      *metrics
}

// A storage hook meets the format without a request and so without a Host: the fill
// of a remote repository's document (OnFill) reaches its replica by the data port it is
// handed, which the composition root builds once per replica.

// Bind gives the format the npm section of the configuration and the metrics
// registry of the replica that owns the Host h (R-NPM-007 limits, R-NPM-044). It
// registers the metric families on m and must be called once per Host, before the
// replica serves.
func (f *Format) Bind(h formats.Host, cfg *config.Config, m *observability.Metrics) {
	r := &replica{cfg: cfg.NPM, shutdown: cfg.Server.ShutdownTimeout}
	if m != nil {
		r.met = newMetrics(m)
	}
	f.replicas.Store(h, r)
	if d := h.FormatData(ID); d != nil {
		f.ports.Store(d, r)
	}
}

// auditBound is the deadline of an audit forward: min(npm.audit.timeout,
// server.shutdown_timeout - 1 s), so a forward that runs when its replica starts to drain
// ends before the shutdown deadline (R-NPM-038). A Host nobody bound has no shutdown.
func (r *replica) auditBound(timeout time.Duration) time.Duration {
	if r.shutdown > 0 {
		return min(timeout, r.shutdown-time.Second)
	}
	return timeout
}

// replicaOf finds the replica of a request's Host.
func (f *Format) replicaOf(h formats.Host) *replica {
	if r, ok := f.replicas.Load(h); ok {
		return r.(*replica)
	}
	return defaultReplica()
}

// replicaOfPort finds the replica a data port was bound for.
func (f *Format) replicaOfPort(port any) *replica {
	if port != nil {
		if r, ok := f.ports.Load(port); ok {
			return r.(*replica)
		}
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
	repos       *observability.CappedLabels
	requests    *prometheus.CounterVec
	duration    *prometheus.HistogramVec
	retries     *prometheus.CounterVec
	conflicts   *prometheus.CounterVec
	removed     *prometheus.CounterVec
	stale       *prometheus.CounterVec
	revalidated *prometheus.CounterVec
	withheld    *prometheus.CounterVec
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
		conflicts: m.NewCounterVec("trove_npm_virtual_version_conflicts_total",
			"Versions that members of a virtual repository hold with different integrity (R-NPM-030).", "repo"),
		removed: m.NewCounterVec("trove_npm_removed_upstream_versions_total",
			"Versions a refreshed upstream document no longer lists (R-NPM-026).", "repo"),
		stale: m.NewCounterVec("trove_npm_stale_upstream_documents_total",
			"Fetched upstream documents that were older than the stored one and were not stored (R-NPM-024).", "repo"),
		revalidated: m.NewCounterVec("trove_npm_tarball_document_revalidations_total",
			"Tarball requests that revalidated the package document first, by outcome (R-NPM-025).", "repo", "outcome"),
		withheld: m.NewCounterVec("trove_npm_audit_names_withheld_total",
			"Names of audit requests not sent to the advisory provider, by the forwarding remote and the reason (R-NPM-038).", "repo", "reason"),
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

func (m *metrics) versionConflicts(repoKey string, n int) {
	if m != nil && n > 0 {
		m.conflicts.WithLabelValues(m.repos.Value("trove_npm_virtual_version_conflicts_total", repoKey)).Add(float64(n))
	}
}

func (m *metrics) removedVersions(repoKey string, n int) {
	if m != nil && n > 0 {
		m.removed.WithLabelValues(m.repos.Value("trove_npm_removed_upstream_versions_total", repoKey)).Add(float64(n))
	}
}

func (m *metrics) staleDocument(repoKey string) {
	if m != nil {
		m.stale.WithLabelValues(m.repos.Value("trove_npm_stale_upstream_documents_total", repoKey)).Inc()
	}
}

// revalidation counts a tarball request that revalidated the package document first:
// outcome is "revalidated" when the upstream answered, "unreachable" when it could not
// be asked and the stored marks decided (Q-NPM-13).
func (m *metrics) revalidation(repoKey, outcome string) {
	if m != nil {
		m.revalidated.WithLabelValues(m.repos.Value("trove_npm_tarball_document_revalidations_total", repoKey), outcome).Inc()
	}
}

// auditWithheld counts the names an audit withheld, by reason; repoKey is the forwarding
// remote (Q-NPM-63).
func (m *metrics) auditWithheld(repoKey string, byReason map[string]int) {
	if m == nil {
		return
	}
	for _, reason := range withheldReasons() {
		if n := byReason[reason]; n > 0 {
			m.withheld.WithLabelValues(m.repos.Value("trove_npm_audit_names_withheld_total", repoKey), reason).Add(float64(n))
		}
	}
}

// replicas is the registry of bound hosts, a field of the format and not a package
// variable. The format keeps a second one by the data port of the replica.
type replicas struct{ sync.Map }
