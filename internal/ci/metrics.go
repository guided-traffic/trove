package ci

import (
	"errors"

	"github.com/prometheus/client_golang/prometheus"
)

// The metric names of R-CI-020 (INV-16). Labels have bounded cardinality and carry no project or user
// name: issuer is the issuer's name (as many values as issuers), outcome and reason come from closed
// sets (the reasons of internal/ci/reason), trigger from [RefreshTrigger].
const (
	MetricExchanges          = "trove_ci_exchanges_total"
	MetricExchangeDuration   = "trove_ci_exchange_duration_seconds"
	MetricKeysRefresh        = "trove_ci_signing_keys_refresh_total"
	MetricKeysAge            = "trove_ci_signing_keys_age_seconds"
	MetricMappingSetStale    = "trove_ci_mapping_set_stale_total"
	MetricTokensActive       = "trove_ci_tokens_active"                     //nolint:gosec // G101: a metric name, not a credential
	MetricJobTokenLookups    = "trove_ci_job_token_lookups_total"           //nolint:gosec // G101: a metric name, not a credential
	MetricJobTokenLookupTime = "trove_ci_job_token_lookup_duration_seconds" //nolint:gosec // G101: a metric name, not a credential
)

// The outcome label of trove_ci_exchanges_total and trove_ci_signing_keys_refresh_total.
const (
	OutcomeSuccess = "success"
	OutcomeDenied  = "denied"
	OutcomeError   = "error"
)

// Metrics are the counters and histograms of the CI federation that a replica increments itself. The
// two gauges that are read from PostgreSQL, trove_ci_signing_keys_age_seconds and
// trove_ci_tokens_active, follow the rule of R-OPS-007: the composition root registers them as a
// database gauge source (internal/app/wire_ci.go), evaluated once per metrics.db_gauge_interval, so
// every replica reports the same value and dashboards aggregate with max, never sum.
type Metrics struct {
	// Exchanges counts every exchange by issuer ("" before an issuer was resolved), outcome and reason
	// ("" on success).
	Exchanges *prometheus.CounterVec
	// ExchangeDuration is the duration of an exchange by issuer.
	ExchangeDuration *prometheus.HistogramVec
	// KeysRefresh counts key set fetches by issuer, trigger (scheduled, unknown_kid, admin) and outcome.
	KeysRefresh *prometheus.CounterVec
	// MappingSetStale counts exchanges whose mapping cache lagged behind the issuer's mappings_version
	// and that evaluated again from PostgreSQL (R-CI-007); a steady rate means slow change propagation.
	MappingSetStale *prometheus.CounterVec
	// JobTokenLookups and JobTokenLookupDuration belong to the P2 fallback (R-CI-022); they are
	// registered so the names are reserved and stay at zero.
	JobTokenLookups        *prometheus.CounterVec
	JobTokenLookupDuration *prometheus.HistogramVec
}

// NewMetrics creates the metrics and registers them on reg; a nil reg creates them unregistered.
func NewMetrics(reg prometheus.Registerer) (*Metrics, error) {
	m := &Metrics{
		Exchanges: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: MetricExchanges,
			Help: "CI token exchanges by issuer, outcome (success, denied, error) and reason (R-CI-020).",
		}, []string{"issuer", "outcome", "reason"}),
		ExchangeDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: MetricExchangeDuration,
			Help: "Duration of a CI token exchange by issuer.", Buckets: []float64{0.005, 0.025, 0.1, 0.25, 0.5, 1, 2.5, 10},
		}, []string{"issuer"}),
		KeysRefresh: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: MetricKeysRefresh,
			Help: "Signing key set fetches by issuer, trigger (scheduled, unknown_kid, admin) and outcome.",
		}, []string{"issuer", "trigger", "outcome"}),
		MappingSetStale: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: MetricMappingSetStale,
			Help: "Exchanges whose mapping cache lagged behind the issuer's mappings_version and evaluated again from PostgreSQL.",
		}, []string{"issuer"}),
		JobTokenLookups: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: MetricJobTokenLookups,
			Help: "CI_JOB_TOKEN lookups at GitLab by issuer and outcome (R-CI-022, P2).",
		}, []string{"issuer", "outcome"}),
		JobTokenLookupDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: MetricJobTokenLookupTime,
			Help: "Duration of a CI_JOB_TOKEN lookup at GitLab by issuer (R-CI-022, P2).", Buckets: []float64{0.01, 0.05, 0.25, 1, 5},
		}, []string{"issuer"}),
	}
	if reg == nil {
		return m, nil
	}
	for _, c := range []prometheus.Collector{m.Exchanges, m.ExchangeDuration, m.KeysRefresh, m.MappingSetStale, m.JobTokenLookups, m.JobTokenLookupDuration} {
		if err := reg.Register(c); err != nil {
			var are prometheus.AlreadyRegisteredError
			if errors.As(err, &are) {
				return nil, errors.New("ci: metrics are already registered on this registerer")
			}
			return nil, err
		}
	}
	return m, nil
}
