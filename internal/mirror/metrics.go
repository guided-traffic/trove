package mirror

import (
	"context"
	"fmt"
	"sync"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/mirror/discover"
	"github.com/guided-traffic/trove/internal/observability"
	"github.com/guided-traffic/trove/internal/repository"
)

// The metrics of the mirror (repositories-and-proxy.md R-REPO-057, operations-and-observability.md R-OPS-008, Q-OPS-158):
// twelve families with the label repo and no package. The counters and trove_mirror_concurrency belong to the process that runs
// the pass; the three gauges read from PostgreSQL are exported by every process that claims the queue mirror, through the
// database gauge source of internal/observability (read once per metrics.db_gauge_interval, 30 s by default).
const (
	MetricPasses              = "trove_mirror_passes_total" //nolint:gosec // G101: a metric name, no credential
	MetricItems               = "trove_mirror_items_total"
	MetricBytes               = "trove_mirror_bytes_total"
	MetricRateLimited         = "trove_mirror_rate_limited_total"
	MetricRateLimitWait       = "trove_mirror_rate_limit_wait_seconds_total"
	MetricConcurrency         = "trove_mirror_concurrency"
	MetricPreempted           = "trove_mirror_preempted_total"
	MetricPending             = "trove_mirror_pending_packages"
	MetricLastComplete        = "trove_mirror_last_complete_pass_timestamp_seconds"
	MetricInterval            = "trove_mirror_interval_seconds"
	MetricConsecutiveFailures = "trove_mirror_consecutive_failures"
	MetricNextPass            = "trove_mirror_next_pass_timestamp_seconds" //nolint:gosec // G101: a metric name, no credential
)

// The outcomes of a pass attempt (the label outcome of trove_mirror_passes_total) and of a pass (last_pass.outcome).
const (
	OutcomeComplete     = "complete"
	OutcomePartial      = "partial"
	OutcomeBoundReached = "bound_reached"
	OutcomeOffline      = "offline"
	OutcomeStopped      = "stopped"
	OutcomeFailed       = "failed"
	OutcomeDeferred     = "deferred"
)

// The results of an artifact (the label result of trove_mirror_items_total).
const (
	ResultStored    = "stored"
	ResultPresent   = "present"
	ResultBounded   = "bounded"
	ResultFailed    = "failed"
	ResultPreempted = "preempted"
	ResultRefused   = "refused"
)

// Metrics are the counters and the window gauge of the process that runs passes. A nil *Metrics records nothing.
type Metrics struct {
	Passes        *prometheus.CounterVec
	Items         *prometheus.CounterVec
	Bytes         *prometheus.CounterVec
	RateLimited   *prometheus.CounterVec
	RateLimitWait *prometheus.CounterVec
	Concurrency   *prometheus.GaugeVec
	Preempted     *prometheus.CounterVec

	mu      sync.RWMutex
	handles map[string]map[*repoMetrics]struct{} // the passes in progress by the repository name they began with
}

// NewMetrics creates the collectors and registers them.
func NewMetrics(reg prometheus.Registerer) (*Metrics, error) {
	m := &Metrics{
		Passes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: MetricPasses,
			Help: "Pass attempts of mirrored remote repositories by how they ended (R-REPO-057).",
		}, []string{"repo", "kind", "outcome"}),
		Items: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: MetricItems,
			Help: "Artifacts of the passes' plans: stored, present, bounded, failed, pre-empted or refused (R-REPO-057).",
		}, []string{"repo", "result"}),
		Bytes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: MetricBytes,
			Help: "Bytes the passes stored (R-REPO-057).",
		}, []string{"repo"}),
		RateLimited: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: MetricRateLimited,
			Help: "Rate-limit answers to the passes (R-REPO-054).",
		}, []string{"repo", "reason"}),
		RateLimitWait: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: MetricRateLimitWait,
			Help: "Seconds the passes waited for rate limits (R-REPO-054).",
		}, []string{"repo"}),
		Concurrency: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: MetricConcurrency,
			Help: "The running pass's window in the process that runs it (R-REPO-054).",
		}, []string{"repo"}),
		Preempted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: MetricPreempted,
			Help: "Background fetches pre-empted by clients (R-REPO-055).",
		}, []string{"repo"}),
	}
	for _, c := range []prometheus.Collector{m.Passes, m.Items, m.Bytes, m.RateLimited, m.RateLimitWait, m.Concurrency, m.Preempted} {
		if err := reg.Register(c); err != nil {
			return nil, fmt.Errorf("mirror metrics: %w", err)
		}
	}
	return m, nil
}

// Forget deletes every series of the repository name from the counters and the window gauge: the repository left the snapshot
// (deleted, or renamed by a move; R-OPS-007). A pass that still runs for the repository holds a handle under the name it began
// with; Forget invalidates the handle under the same lock that the pass records under, so the pass records nothing more under
// the old name and the series do not come back.
func (m *Metrics) Forget(name string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for h := range m.handles[name] {
		h.forgotten = true
	}
	delete(m.handles, name)
	l := prometheus.Labels{"repo": name}
	for _, c := range []interface{ DeletePartialMatch(prometheus.Labels) int }{
		m.Passes, m.Items, m.Bytes, m.RateLimited, m.RateLimitWait, m.Concurrency, m.Preempted,
	} {
		c.DeletePartialMatch(l)
	}
}

// repoMetrics is the handle of one pass for its repository's series, resolved once per pass. Forget invalidates it: from then on
// every record is dropped, until the pass releases it with close. A nil handle records nothing.
type repoMetrics struct {
	m         *Metrics
	repo      string
	forgotten bool // guarded by m.mu
}

func (m *Metrics) of(repo string) *repoMetrics {
	r := &repoMetrics{m: m, repo: repo}
	if m == nil {
		return r
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.handles == nil {
		m.handles = map[string]map[*repoMetrics]struct{}{}
	}
	if m.handles[repo] == nil {
		m.handles[repo] = map[*repoMetrics]struct{}{}
	}
	m.handles[repo][r] = struct{}{}
	return r
}

// close releases the handle at the end of the pass.
func (r *repoMetrics) close() {
	if r == nil || r.m == nil {
		return
	}
	r.m.mu.Lock()
	defer r.m.mu.Unlock()
	delete(r.m.handles[r.repo], r)
	if len(r.m.handles[r.repo]) == 0 {
		delete(r.m.handles, r.repo)
	}
}

// record runs fn, which writes series of the repository, unless the repository left the snapshot under this name. Forget takes
// the write lock, so a record either happens before the deletion (and is deleted with it) or is dropped.
func (r *repoMetrics) record(fn func(m *Metrics, repo string)) {
	if r == nil || r.m == nil {
		return
	}
	r.m.mu.RLock()
	defer r.m.mu.RUnlock()
	if r.forgotten {
		return
	}
	fn(r.m, r.repo)
}

// touch creates the series of the repository at 0, so that the first event of a kind is an increase for a rate or an
// increase() over a counter that has existed since the pass began, and not the series' first sample (the alerts of R-OPS-046
// read increase() of trove_mirror_passes_total).
func (r *repoMetrics) touch() {
	r.record(func(m *Metrics, repo string) {
		for _, kind := range []string{string(discover.PassFull), string(discover.PassIncremental)} {
			for _, outcome := range []string{OutcomeComplete, OutcomePartial, OutcomeBoundReached, OutcomeOffline, OutcomeStopped, OutcomeFailed, OutcomeDeferred} {
				m.Passes.WithLabelValues(repo, kind, outcome).Add(0)
			}
		}
		for _, result := range []string{ResultStored, ResultPresent, ResultBounded, ResultFailed, ResultPreempted, ResultRefused} {
			m.Items.WithLabelValues(repo, result).Add(0)
		}
		for _, reason := range []string{"too_many_requests", "slow_down", "retry_after", "quota_header"} {
			m.RateLimited.WithLabelValues(repo, reason).Add(0)
		}
		m.Bytes.WithLabelValues(repo).Add(0)
		m.RateLimitWait.WithLabelValues(repo).Add(0)
		m.Preempted.WithLabelValues(repo).Add(0)
	})
}

func (r *repoMetrics) pass(kind, outcome string) {
	r.record(func(m *Metrics, repo string) { m.Passes.WithLabelValues(repo, kind, outcome).Inc() })
}

func (r *repoMetrics) item(result string) {
	r.record(func(m *Metrics, repo string) { m.Items.WithLabelValues(repo, result).Inc() })
}

func (r *repoMetrics) bytes(n int64) {
	if n > 0 {
		r.record(func(m *Metrics, repo string) { m.Bytes.WithLabelValues(repo).Add(float64(n)) })
	}
}

func (r *repoMetrics) rateLimited(reason string) {
	r.record(func(m *Metrics, repo string) { m.RateLimited.WithLabelValues(repo, reason).Inc() })
}

func (r *repoMetrics) waited(seconds float64) {
	r.record(func(m *Metrics, repo string) { m.RateLimitWait.WithLabelValues(repo).Add(seconds) })
}

func (r *repoMetrics) window(w int) {
	r.record(func(m *Metrics, repo string) { m.Concurrency.WithLabelValues(repo).Set(float64(w)) })
}

func (r *repoMetrics) preempted() {
	r.record(func(m *Metrics, repo string) { m.Preempted.WithLabelValues(repo).Inc() })
}

// GaugeSource is the database gauge source of the gauges that every process exports, whatever its role (R-REPO-057,
// Q-OPS-169, Q-OPS-171): from mirror_state the pending names of the pass in progress, the end of the last complete pass, the
// failures in a row and when the mirror next acts, and from the repository snapshot the configured interval. Only a remote
// repository that is in mode mirror in the snapshot has series; the failures and the time of the next pass only while it is
// neither paused nor disabled, so that a mirror that is switched off by its operator raises no alert.
func GaugeSource(read func(ctx context.Context) ([]GaugeRow, error), repos repository.Reader) observability.GaugeSource {
	desc := func(name, help string) *prometheus.Desc { return prometheus.NewDesc(name, help, []string{"repo"}, nil) }
	pending := desc(MetricPending, "Pending names of the pass in progress, from PostgreSQL (R-REPO-057).")
	last := desc(MetricLastComplete, "End of the last complete pass, from PostgreSQL (R-REPO-057).")
	interval := desc(MetricInterval, "The configured intervalSeconds of the mirror (R-REPO-057).")
	failures := desc(MetricConsecutiveFailures, "Failed pass attempts in a row, from PostgreSQL (R-REPO-057).")
	next := desc(MetricNextPass, "When the mirror next acts: the time a due pass is due, or the last write of the pass in progress, from PostgreSQL (R-REPO-057).")
	gauge := func(d *prometheus.Desc, v float64, repo string) prometheus.Metric {
		return prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, repo)
	}
	return observability.GaugeSource{
		Name: "mirror", Descs: []*prometheus.Desc{pending, last, interval, failures, next},
		Collect: func(ctx context.Context) ([]prometheus.Metric, error) {
			rows, err := read(ctx)
			if err != nil {
				return nil, err
			}
			list, err := repos.List(ctx)
			if err != nil {
				return nil, err
			}
			// The gauge rows name a repository by its qualified name <project>/<key>.
			active := map[string]repository.RemoteSettings{}
			for _, repo := range list {
				if s, ok := repo.Remote(); ok && s.Mirrored() && repo.Lifecycle != repository.Deleting {
					active[repo.Name.String()] = s
				}
			}
			var out []prometheus.Metric
			for key, s := range active {
				out = append(out, gauge(interval, float64(s.Mirror.IntervalSeconds), key))
			}
			disabled := map[string]bool{}
			for _, repo := range list {
				disabled[repo.Name.String()] = repo.Disabled
			}
			for _, r := range rows {
				s, ok := active[r.Key]
				if !ok {
					continue
				}
				out = append(out, gauge(pending, float64(r.Pending), r.Key))
				if !r.LastComplete.IsZero() {
					out = append(out, gauge(last, float64(r.LastComplete.UnixMicro())/1e6, r.Key))
				}
				if s.Mirror.Paused || disabled[r.Key] {
					continue
				}
				out = append(out, gauge(failures, float64(r.ConsecutiveFailures), r.Key), gauge(next, float64(r.NextPass.UnixMicro())/1e6, r.Key))
			}
			return out, nil
		},
	}
}
