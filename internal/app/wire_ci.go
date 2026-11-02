package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/prometheus/client_golang/prometheus"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/ci"
	"github.com/guided-traffic/trove/internal/ci/claims"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/observability"
)

// The wiring of the CI federation (docs/developer/ci-oidc.md): the issuer and key service (W1), the mapping store
// (W3), the revoker and the issuance service (W5) on every role, the token exchange (W4) on a replica with a client
// listener, the three tasks and the metrics.

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

// AreaCI is the CI federation's services (docs/developer/ci-oidc.md). Every role builds it: a web replica
// resolves issuers and keys for the exchange, a worker replica runs the tasks.
const AreaCI Area = "ci"

// ciArea is the set of CI services of the replica.
type ciArea struct {
	issuers   ci.Issuers
	mappings  ci.Mappings
	revoker   ci.Revoker
	issuances ci.Issuances
	metrics   *ci.Metrics
}

// wireCI builds the services: the issuer and key service of work package W1 over the database, the change
// log, the cache registry and the outbound factory, the mapping store of work package W3, and the revoker and the
// issuance service of work package W5 (the revoker and the mapping store are the issuer service's collaborators).
func (a *App) wireCI(_ context.Context, in infra) (ciArea, error) {
	if err := requireInputs(AreaCI, given("database", in.db), given("change log", in.changes), given("cache registry", in.caches),
		given("outbound factory", in.out), given("audit recorder", in.rec)); err != nil {
		return ciArea{}, err
	}
	metrics, err := ci.NewMetrics(a.tele.metrics.Registry())
	if err != nil {
		return ciArea{}, fmt.Errorf("ci metrics: %w", err)
	}
	revoker, err := ci.NewRevoker(ci.RevokerOptions{DB: in.db, Changes: in.changes, Recorder: in.rec})
	if err != nil {
		return ciArea{}, err
	}
	issuances, err := ci.NewIssuances(ci.IssuanceOptions{DB: in.db, Config: a.cfg.CI})
	if err != nil {
		return ciArea{}, err
	}
	out := ciArea{revoker: revoker, issuances: issuances, metrics: metrics}
	// The mapping store of work package W3: it takes the revoker of the area and is the issuer service's
	// collaborator for the cascade of a deletion.
	mappings, err := ci.NewMappings(ci.MappingsOptions{
		DB: in.db, Changes: in.changes, Recorder: in.rec, Caches: in.caches, Revoker: out.revoker,
		Config: a.cfg.CI, BaseURL: a.cfg.Server.BaseURL, Log: a.log.With("component", "ci"),
	})
	if err != nil {
		return ciArea{}, err
	}
	out.mappings = mappings
	issuers, err := ci.New(ci.Options{
		DB: in.db, Changes: in.changes, Recorder: in.rec, Caches: in.caches, Outbound: in.out,
		Config: a.cfg.CI, OutboundConfig: a.cfg.Proxy.Outbound, EpochPollInterval: a.cfg.Cluster.EpochPollInterval,
		BaseURL: a.cfg.Server.BaseURL, Revoker: out.revoker, Mappings: out.mappings, Metrics: metrics,
		Log: a.log.With("component", "ci"),
	})
	if err != nil {
		return ciArea{}, err
	}
	out.issuers = issuers
	return out, nil
}

// CIIssuers returns the issuer and key service; nil in the shell profile. The integration tests of the key
// source call it the way the exchange does.
func (a *App) CIIssuers() ci.Issuers { return part[ciArea](a.parts).issuers }

// CIMappings returns the mapping service; nil in the shell profile. The integration tests of the mapping
// cache call it the way the exchange does.
func (a *App) CIMappings() ci.Mappings { return part[ciArea](a.parts).mappings }

// CIRevoker returns the revoker of CI tokens (R-CI-017); the issuer service and the mapping store call it in their
// transactions, and the integration tests of W5 call RevokeMappingTx the way the mapping store (W3) does.
func (a *App) CIRevoker() ci.Revoker { return part[ciArea](a.parts).revoker }

// CITokenIssuer returns what the exchange issues a CI token with: the token service, whose IssueCITx writes the
// token row in the caller's transaction (R-CI-017), and the replica's transaction runner. The integration tests
// of the use time (work package W5) issue tokens with frozen bundles through it the way the exchange does.
func (a *App) CITokenIssuer() (*token.Service, meta.Runner) {
	return part[identityArea](a.parts).tokens, a.database()
}

// AreaCIExchange is the token exchange of work package W4 (R-CI-007): only a replica with a client listener serves it,
// and it takes the two limiters of the limits area.
const AreaCIExchange Area = "ci_exchange"

// ciExchangeArea is the exchange of a replica with a client listener.
type ciExchangeArea struct{ exchanger *ci.ExchangeService }

// wireCIExchange builds the exchange over the issuers and the mappings of the CI area, the token service (IssueCITx,
// W5), the grant index of the decision point (the scope's questions), the audit recorder and the limiters
// ci_exchange_address and ci_exchange_subject. It is a component: its stop writes the denial counts the replica
// still holds (R-CI-021).
func (a *App) wireCIExchange(ctx context.Context, in infra, cs ciArea, id identityArea, az authzArea, lim limitsArea) (ciExchangeArea, error) {
	if err := requireInputs(AreaCIExchange, given("database", in.db), given("issuer service", cs.issuers), given("mapping store", cs.mappings),
		given("token service", id.tokens), given("decision point", az.engine), given("limiter set", lim.set)); err != nil {
		return ciExchangeArea{}, err
	}
	engine := az.engine
	x, err := ci.NewExchange(ci.ExchangeOptions{
		DB: in.db, Issuers: cs.issuers, Mappings: cs.mappings, Tokens: id.tokens,
		Grants:   ci.GrantIndexFunc(func(f *claims.Frozen) ci.GrantIndex { return engine.CIScopeOf(f) }),
		Recorder: in.rec, Metrics: cs.metrics, Config: a.cfg.CI, BaseURL: a.cfg.Server.BaseURL,
		AddressLimiter: lim.ciAddress, SubjectLimiter: lim.ciSubject, Log: a.log.With("component", "ci-exchange"),
	})
	if err != nil {
		return ciExchangeArea{}, err
	}
	if err := a.startComponent(ctx, x); err != nil {
		return ciExchangeArea{}, err
	}
	return ciExchangeArea{exchanger: x}, nil
}

// ciAPI hands the services to the management API. A replica without the exchange (none serves a client listener
// without it) leaves Exchanger nil, which the handler answers with 503.
func (a *App) ciAPI() v1.CIServices {
	s := part[ciArea](a.parts)
	out := v1.CIServices{Issuers: s.issuers, Mappings: s.mappings, Issuances: s.issuances, Revoker: s.revoker, Exchange: a.cfg.CI.Exchange}
	if x := part[ciExchangeArea](a.parts).exchanger; x != nil {
		out.Exchanger = x
	}
	return out
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
	tasks := []struct {
		name, schedule string
		run            func(context.Context, ciArea) (map[string]int64, error)
	}{
		{TaskCIKeysRefresh, DefaultCIKeysRefreshSchedule, func(ctx context.Context, s ciArea) (map[string]int64, error) {
			n, err := s.issuers.RefreshDue(ctx)
			return map[string]int64{"refreshed": int64(n)}, err
		}},
		{TaskCIReplayCleanup, DefaultCIReplayCleanupSchedule, func(ctx context.Context, s ciArea) (map[string]int64, error) {
			n, err := s.issuances.CleanupReplay(ctx)
			return map[string]int64{"deleted": n}, err
		}},
		{TaskCIIssuanceRetention, DefaultCIIssuanceRetentionSchedule, func(ctx context.Context, s ciArea) (map[string]int64, error) {
			n, err := s.issuances.RetainIssuances(ctx)
			return map[string]int64{"deleted": n}, err
		}},
	}
	for _, t := range tasks {
		handler := func(ctx context.Context, run *jobs.Run) error {
			set := part[ciArea](a.parts)
			if set.issuers == nil {
				return errors.New("CI services: this replica has not wired them yet")
			}
			res, err := t.run(ctx, set)
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

// registerCIMetrics registers the two gauges of R-CI-020 that are read from PostgreSQL
// (trove_ci_signing_keys_age_seconds, trove_ci_tokens_active) as a database gauge source, evaluated once
// per metrics.db_gauge_interval and aggregated with max, never sum, across replicas (R-OPS-007). The
// counters and histograms are the replica's own, created by wireCI; the key service and the exchange increment them.
func (a *App) registerCIMetrics() error {
	m := a.tele.metrics
	age := prometheus.NewDesc(ci.MetricKeysAge, "Age of the stored signing key set of a CI issuer, from fetched_at in PostgreSQL (aggregate with max, not sum, across replicas).", []string{"issuer"}, nil)
	active := prometheus.NewDesc(ci.MetricTokensActive, "Unexpired, unrevoked CI tokens by issuer, from PostgreSQL (aggregate with max, not sum, across replicas).", []string{"issuer"}, nil)
	m.DBGauges().Add(observability.GaugeSource{
		Name: "ci", Descs: []*prometheus.Desc{age, active},
		Collect: func(ctx context.Context) ([]prometheus.Metric, error) {
			var out []prometheus.Metric
			rows, err := a.database().Main().Query(ctx,
				`SELECT i.name, extract(epoch FROM now() - k.fetched_at)::float8 FROM ci_issuer_keys k JOIN ci_issuers i ON i.id = k.issuer_id
				  WHERE k.fetched_at > to_timestamp(0)`) // an issuer whose first fetch failed has a placeholder row and no set
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
			// trove_ci_tokens_active: the issuance service's count, 0 for an issuer without a live token, so a
			// dashboard sees the series drop to zero after a revocation instead of vanishing.
			counts, err := ci.ActiveTokens(ctx, a.database())
			if err != nil {
				return nil, err
			}
			for name, n := range counts {
				out = append(out, prometheus.MustNewConstMetric(active, prometheus.GaugeValue, float64(n), name))
			}
			return out, nil
		},
	})
	return nil
}
