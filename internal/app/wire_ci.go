package app

import (
	"context"
	"fmt"

	"github.com/prometheus/client_golang/prometheus"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/ci"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/observability"
)

// The wiring of the CI federation (docs/developer/ci-oidc.md). Until the work packages of the page
// build their services, the composition root wires the stubs of internal/ci/stubs.go: every method
// answers 501 ci_not_implemented, the three tasks run and do nothing, the metrics are registered and
// stay at zero. A work package replaces its stub here and deletes its type from stubs.go.

// The kinds and tasks of the CI exchange (R-CI-030). All three are exclusive: two runs never
// overlap, and every run is idempotent.
const (
	// TaskCIKeysRefresh is the kind and task name of the scheduled key refresh (R-CI-003).
	TaskCIKeysRefresh = "ci-keys-refresh"
	// TaskCIReplayCleanup deletes the expired rows of the replay set (R-CI-018).
	TaskCIReplayCleanup = "ci-replay-cleanup"
	// TaskCIIssuanceRetention deletes the issuance records past ci.issuances.retention (R-CI-020).
	TaskCIIssuanceRetention = "ci-issuance-retention"
)

// The default schedules of the three tasks (R-CI-030, UTC); maintenance.tasks.<name> overrides them.
const (
	DefaultCIKeysRefreshSchedule       = "*/5 * * * *"
	DefaultCIReplayCleanupSchedule     = "7 * * * *"
	DefaultCIIssuanceRetentionSchedule = "40 3 * * *"
)

// ciSet is the set of CI services of the replica. A work package replaces its member.
type ciSet struct {
	issuers   ci.Issuers
	mappings  ci.Mappings
	revoker   ci.Revoker
	issuances ci.Issuances
	exchanger ci.Exchanger
}

// ciServices builds the set: the stubs until the work packages exist.
func (a *App) ciServices() ciSet {
	return ciSet{
		issuers: ci.StubIssuers{}, mappings: ci.StubMappings{}, revoker: ci.StubRevoker{},
		issuances: ci.StubIssuances{}, exchanger: ci.StubExchanger{},
	}
}

// ciAPI hands the services to the management API.
func (a *App) ciAPI() v1.CIServices {
	s := a.ciServices()
	return v1.CIServices{Issuers: s.issuers, Mappings: s.mappings, Issuances: s.issuances, Revoker: s.revoker, Exchanger: s.exchanger}
}

// ciLimiters registers the two cluster-scope limiters of the exchange (R-CI-021, Q-CI-14, R-ARCH-034):
// ci_exchange_address, checked first for every request before the body is read, and
// ci_exchange_subject, per CI issuer and subject after the subject token passed validation. Both give
// the same answer on every replica, from Valkey or from the replica's share of the budget while
// Valkey is unavailable.
func ciLimiters(ls *cluster.LimiterSet, rl rateLimitCI) (address, subject *cluster.Limiter) {
	address = ls.NewLimiter(LimiterCIExchangeAddress, cluster.ScopeCluster, float64(rl.perClientIPPerMinute)/60, rl.perClientIPBurst)
	subject = ls.NewLimiter(LimiterCIExchangeSubject, cluster.ScopeCluster, float64(rl.perSubjectPerMinute)/60, rl.perSubjectBurst)
	return address, subject
}

// The names of the exchange's limiters (R-CI-021).
const (
	LimiterCIExchangeAddress = "ci_exchange_address"
	LimiterCIExchangeSubject = "ci_exchange_subject"
)

// rateLimitCI is ci.exchange.rate_limit as the limiter set takes it.
type rateLimitCI struct {
	perClientIPPerMinute, perClientIPBurst int
	perSubjectPerMinute, perSubjectBurst   int
}

func (a *App) ciRateLimit() rateLimitCI {
	rl := a.cfg.CI.Exchange.RateLimit
	return rateLimitCI{rl.PerClientIPPerMinute, rl.PerClientIPBurst, rl.PerSubjectPerMinute, rl.PerSubjectBurst}
}

// registerCIJobs registers the three maintenance tasks of R-CI-030 on the job registry: queue
// maintenance, exclusive, three attempts, schedules from maintenance.tasks with the defaults of the
// specification. A handler calls its service and treats the not-implemented answer of a stub as
// "nothing to do yet", so the tasks succeed until their work package exists and need no edit when it
// does.
func (a *App) registerCIJobs(r *jobs.Registry) {
	s := a.ciServices()
	tasks := []struct {
		name, schedule string
		run            func(context.Context) (map[string]int64, error)
	}{
		{TaskCIKeysRefresh, DefaultCIKeysRefreshSchedule, func(ctx context.Context) (map[string]int64, error) {
			n, err := s.issuers.RefreshDue(ctx)
			return map[string]int64{"refreshed": int64(n)}, err
		}},
		{TaskCIReplayCleanup, DefaultCIReplayCleanupSchedule, func(ctx context.Context) (map[string]int64, error) {
			n, err := s.issuances.CleanupReplay(ctx)
			return map[string]int64{"deleted": n}, err
		}},
		{TaskCIIssuanceRetention, DefaultCIIssuanceRetentionSchedule, func(ctx context.Context) (map[string]int64, error) {
			n, err := s.issuances.RetainIssuances(ctx)
			return map[string]int64{"deleted": n}, err
		}},
	}
	for _, t := range tasks {
		handler := func(ctx context.Context, run *jobs.Run) error {
			res, err := t.run(ctx)
			if e, ok := errs.As(err); ok && e.Kind == errs.NotImplemented {
				return run.SetResult(map[string]string{"pending": "not built in this release"})
			}
			if err != nil {
				return err
			}
			return run.SetResult(res)
		}
		r.RegisterKind(t.name, jobs.KindSpec{Queue: "maintenance", DedupMode: jobs.Exclusive, MaxAttempts: 3, Handler: handler})
		spec := jobs.TaskSpec{Schedule: t.schedule}
		if c, ok := a.cfg.Maintenance.Tasks[t.name]; ok && c.Schedule != "" {
			spec.Schedule, spec.Disabled = c.Schedule, !c.Enabled
		}
		r.RegisterTask(t.name, spec)
	}
}

// registerCIMetrics registers the counters and histograms of R-CI-020 and the two gauges read from
// PostgreSQL (trove_ci_signing_keys_age_seconds, trove_ci_tokens_active) as a database gauge source,
// evaluated once per metrics.db_gauge_interval and aggregated with max, never sum, across replicas
// (R-OPS-007). The counters are the replica's own; W1 and W4 increment them.
func (a *App) registerCIMetrics() error {
	m := a.tele.metrics
	if _, err := ci.NewMetrics(m.Registry()); err != nil {
		return fmt.Errorf("ci metrics: %w", err)
	}
	age := prometheus.NewDesc(ci.MetricKeysAge, "Age of the stored signing key set of a CI issuer, from fetched_at in PostgreSQL (aggregate with max, not sum, across replicas).", []string{"issuer"}, nil)
	active := prometheus.NewDesc(ci.MetricTokensActive, "Unexpired, unrevoked CI tokens by issuer, from PostgreSQL (aggregate with max, not sum, across replicas).", []string{"issuer"}, nil)
	m.DBGauges().Add(observability.GaugeSource{
		Name: "ci", Descs: []*prometheus.Desc{age, active},
		Collect: func(ctx context.Context) ([]prometheus.Metric, error) {
			var out []prometheus.Metric
			rows, err := a.database().Main().Query(ctx,
				`SELECT i.name, extract(epoch FROM now() - k.fetched_at)::float8 FROM ci_issuer_keys k JOIN ci_issuers i ON i.id = k.issuer_id`)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var name string
				var secs float64
				if err := rows.Scan(&name, &secs); err != nil {
					rows.Close()
					return nil, err
				}
				out = append(out, prometheus.MustNewConstMetric(age, prometheus.GaugeValue, secs, name))
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return nil, err
			}
			rows, err = a.database().Main().Query(ctx,
				`SELECT i.name, count(*) FROM ci_token_issuances s JOIN ci_issuers i ON i.id = s.issuer_id
				   JOIN api_token t ON t.id = s.token_id
				  WHERE t.revoked_at IS NULL AND t.expires_at > now() GROUP BY i.name`)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			for rows.Next() {
				var name string
				var n int64
				if err := rows.Scan(&name, &n); err != nil {
					return nil, err
				}
				out = append(out, prometheus.MustNewConstMetric(active, prometheus.GaugeValue, float64(n), name))
			}
			return out, rows.Err()
		},
	})
	return nil
}
