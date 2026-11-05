package mirror

import (
	"context"
	"fmt"

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

// repoMetrics are the collectors of one repository, resolved once per pass.
type repoMetrics struct {
	m    *Metrics
	repo string
}

func (m *Metrics) of(repo string) repoMetrics { return repoMetrics{m: m, repo: repo} }

// touch creates the series of the repository at 0, so that the first event of a kind is an increase for a rate or an
// increase() over a counter that has existed since the pass began, and not the series' first sample (the alerts of R-OPS-046
// read increase() of trove_mirror_passes_total).
func (r repoMetrics) touch() {
	if r.m == nil {
		return
	}
	for _, kind := range []string{string(discover.PassFull), string(discover.PassIncremental)} {
		for _, outcome := range []string{OutcomeComplete, OutcomePartial, OutcomeBoundReached, OutcomeOffline, OutcomeStopped, OutcomeFailed, OutcomeDeferred} {
			r.m.Passes.WithLabelValues(r.repo, kind, outcome).Add(0)
		}
	}
	for _, result := range []string{ResultStored, ResultPresent, ResultBounded, ResultFailed, ResultPreempted, ResultRefused} {
		r.m.Items.WithLabelValues(r.repo, result).Add(0)
	}
	for _, reason := range []string{"too_many_requests", "slow_down", "retry_after", "quota_header"} {
		r.m.RateLimited.WithLabelValues(r.repo, reason).Add(0)
	}
	r.m.Bytes.WithLabelValues(r.repo).Add(0)
	r.m.RateLimitWait.WithLabelValues(r.repo).Add(0)
	r.m.Preempted.WithLabelValues(r.repo).Add(0)
}

func (r repoMetrics) pass(kind, outcome string) {
	if r.m != nil {
		r.m.Passes.WithLabelValues(r.repo, kind, outcome).Inc()
	}
}

func (r repoMetrics) item(result string) {
	if r.m != nil {
		r.m.Items.WithLabelValues(r.repo, result).Inc()
	}
}

func (r repoMetrics) bytes(n int64) {
	if r.m != nil && n > 0 {
		r.m.Bytes.WithLabelValues(r.repo).Add(float64(n))
	}
}

func (r repoMetrics) rateLimited(reason string) {
	if r.m != nil {
		r.m.RateLimited.WithLabelValues(r.repo, reason).Inc()
	}
}

func (r repoMetrics) waited(seconds float64) {
	if r.m != nil {
		r.m.RateLimitWait.WithLabelValues(r.repo).Add(seconds)
	}
}

func (r repoMetrics) window(w int) {
	if r.m != nil {
		r.m.Concurrency.WithLabelValues(r.repo).Set(float64(w))
	}
}

func (r repoMetrics) preempted() {
	if r.m != nil {
		r.m.Preempted.WithLabelValues(r.repo).Inc()
	}
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
			active := map[string]repository.RemoteSettings{}
			for _, repo := range list {
				if s, ok := repo.Remote(); ok && s.Mirrored() && repo.Lifecycle != repository.Deleting {
					active[repo.Key] = s
				}
			}
			var out []prometheus.Metric
			for key, s := range active {
				out = append(out, gauge(interval, float64(s.Mirror.IntervalSeconds), key))
			}
			disabled := map[string]bool{}
			for _, repo := range list {
				disabled[repo.Key] = repo.Disabled
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
