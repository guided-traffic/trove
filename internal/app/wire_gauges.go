package app

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/observability"
)

// poolCollector exports the connection pools of this replica (R-OPS-007). It
// reads the pools' counters at scrape time; a scrape opens no connection. The
// wait for a connection as a histogram is the pool tracer's
// (trove_db_pool_acquire_duration_seconds, postgres.NewMetrics).
type poolCollector struct {
	db       func() *postgres.DB
	conns    *prometheus.Desc
	maxes    *prometheus.Desc
	empty    *prometheus.Desc
	canceled *prometheus.Desc
	opened   *prometheus.Desc
}

func newPoolCollector(db func() *postgres.DB) *poolCollector {
	l := []string{"db_pool"}
	return &poolCollector{
		db:       db,
		conns:    prometheus.NewDesc("trove_db_pool_connections", "Connection state per pool of this replica.", []string{"db_pool", "state"}, nil),
		maxes:    prometheus.NewDesc("trove_db_pool_max_connections", "Pool size.", l, nil),
		empty:    prometheus.NewDesc("trove_db_pool_empty_acquires_total", "Acquisitions that had to wait for a connection.", l, nil),
		canceled: prometheus.NewDesc("trove_db_pool_canceled_acquires_total", "Acquisitions given up because the caller's deadline ended.", l, nil),
		opened:   prometheus.NewDesc("trove_db_pool_connections_opened_total", "Connections opened.", l, nil),
	}
}

func (c *poolCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{c.conns, c.maxes, c.empty, c.canceled, c.opened} {
		ch <- d
	}
}

func (c *poolCollector) Collect(ch chan<- prometheus.Metric) {
	db := c.db()
	if db == nil {
		return
	}
	c.collect(db.Stats(), ch)
}

func (c *poolCollector) collect(s postgres.Stats, ch chan<- prometheus.Metric) {
	for name, p := range map[string]postgres.PoolStats{"request": s.Main, "control": s.Control, "lease": s.Lease} {
		if p.Max == 0 {
			continue // no lease pool on a web replica
		}
		ch <- prometheus.MustNewConstMetric(c.conns, prometheus.GaugeValue, float64(p.Acquired), name, "acquired")
		ch <- prometheus.MustNewConstMetric(c.conns, prometheus.GaugeValue, float64(p.Idle), name, "idle")
		ch <- prometheus.MustNewConstMetric(c.conns, prometheus.GaugeValue, float64(p.Total-p.Acquired-p.Idle), name, "constructing")
		ch <- prometheus.MustNewConstMetric(c.maxes, prometheus.GaugeValue, float64(p.Max), name)
		ch <- prometheus.MustNewConstMetric(c.empty, prometheus.CounterValue, float64(p.EmptyAcquireCount), name)
		ch <- prometheus.MustNewConstMetric(c.canceled, prometheus.CounterValue, float64(p.CanceledAcquireCount), name)
		ch <- prometheus.MustNewConstMetric(c.opened, prometheus.CounterValue, float64(p.NewConns), name)
	}
}

// jobDescs are the two gauges of the job queue, read from PostgreSQL in the background.
type jobDescs struct{ queued, oldest *prometheus.Desc }

func newJobDescs() jobDescs {
	l := []string{"kind"}
	return jobDescs{
		queued: prometheus.NewDesc("trove_jobs_queued", "Queued jobs by kind, from PostgreSQL (aggregate with max, not sum, across replicas).", l, nil),
		oldest: prometheus.NewDesc("trove_jobs_oldest_queued_age_seconds", "Age in seconds of the oldest queued job that is due, by kind, from PostgreSQL (aggregate with max).", l, nil),
	}
}

// metrics renders the rows of the queue query; a kind without a queued job has no series.
func (d jobDescs) metrics(rows []q.JobQueueGaugesRow) []prometheus.Metric {
	var out []prometheus.Metric
	for _, r := range rows {
		out = append(out,
			prometheus.MustNewConstMetric(d.queued, prometheus.GaugeValue, float64(r.Queued), r.Kind),
			prometheus.MustNewConstMetric(d.oldest, prometheus.GaugeValue, r.OldestAgeSeconds, r.Kind))
	}
	return out
}

// registerGauges adds the pool collector and the gauges read from PostgreSQL in
// the background (Q-OPS-18): trove_jobs_queued{kind}, trove_jobs_oldest_queued_age_seconds{kind}
// and trove_valkey_primary_views.
func (a *App) registerGauges() {
	m := a.tele.metrics
	if err := a.registerCIMetrics(); err != nil {
		a.log.Error("the CI metrics are not registered", "error", err)
	}
	a.registerBackupGauges()
	m.Registry().MustRegister(newPoolCollector(a.database))
	jobs := newJobDescs()
	m.DBGauges().Add(observability.GaugeSource{
		Name: "jobs", Descs: []*prometheus.Desc{jobs.queued, jobs.oldest},
		Collect: func(ctx context.Context) ([]prometheus.Metric, error) {
			rows, err := q.New(a.database().Main()).JobQueueGauges(ctx)
			if err != nil {
				return nil, err
			}
			return jobs.metrics(rows), nil
		},
	})
	views := prometheus.NewDesc("trove_valkey_primary_views",
		"Distinct Valkey primaries named by the live replicas of the registry; above 1 means replicas write to different primaries.", nil, nil)
	stale := a.cfg.System.HeartbeatStaleAfter.Seconds()
	m.DBGauges().Add(observability.GaugeSource{
		Name: "valkey-primary-views", Descs: []*prometheus.Desc{views},
		Collect: func(ctx context.Context) ([]prometheus.Metric, error) {
			n, err := q.New(a.database().Main()).ValkeyPrimaryViews(ctx, stale)
			if err != nil {
				return nil, err
			}
			return []prometheus.Metric{prometheus.MustNewConstMetric(views, prometheus.GaugeValue, float64(n))}, nil
		},
	})
}
