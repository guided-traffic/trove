package app

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/observability"
)

// poolCollector exports the connection pools of this replica (R-OPS-007). It
// reads the pools' counters at scrape time; a scrape opens no connection.
type poolCollector struct {
	db     func() *postgres.DB
	conns  *prometheus.Desc
	maxes  *prometheus.Desc
	opened *prometheus.Desc
}

func newPoolCollector(db func() *postgres.DB) *poolCollector {
	return &poolCollector{
		db:     db,
		conns:  prometheus.NewDesc("trove_db_pool_connections", "Connection state per pool of this replica.", []string{"db_pool", "state"}, nil),
		maxes:  prometheus.NewDesc("trove_db_pool_max_connections", "Pool size.", []string{"db_pool"}, nil),
		opened: prometheus.NewDesc("trove_db_pool_empty_acquires_total", "Acquisitions that had to wait for a connection.", []string{"db_pool"}, nil),
	}
}

func (c *poolCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.conns
	ch <- c.maxes
	ch <- c.opened
}

func (c *poolCollector) Collect(ch chan<- prometheus.Metric) {
	db := c.db()
	if db == nil {
		return
	}
	s := db.Stats()
	for name, p := range map[string]postgres.PoolStats{"request": s.Main, "control": s.Control, "lease": s.Lease} {
		if p.Max == 0 {
			continue // no lease pool on a web replica
		}
		ch <- prometheus.MustNewConstMetric(c.conns, prometheus.GaugeValue, float64(p.Acquired), name, "acquired")
		ch <- prometheus.MustNewConstMetric(c.conns, prometheus.GaugeValue, float64(p.Idle), name, "idle")
		ch <- prometheus.MustNewConstMetric(c.conns, prometheus.GaugeValue, float64(p.Total-p.Acquired-p.Idle), name, "constructing")
		ch <- prometheus.MustNewConstMetric(c.maxes, prometheus.GaugeValue, float64(p.Max), name)
		ch <- prometheus.MustNewConstMetric(c.opened, prometheus.CounterValue, float64(p.AcquireCount), name)
	}
}

// registerGauges adds the pool collector and the job-queue depth, read from
// PostgreSQL in the background (Q-OPS-18): trove_jobs_queued{kind}.
func (a *App) registerGauges() {
	m := a.tele.metrics
	m.Registry().MustRegister(newPoolCollector(a.database))
	desc := prometheus.NewDesc("trove_jobs_queued", "Queued jobs by kind, from PostgreSQL (aggregate with max, not sum, across replicas).", []string{"kind"}, nil)
	m.DBGauges().Add(observability.GaugeSource{
		Name: "jobs", Descs: []*prometheus.Desc{desc},
		Collect: func(ctx context.Context) ([]prometheus.Metric, error) {
			rows, err := a.database().Main().Query(ctx, `SELECT kind, count(*) FROM job WHERE state = 'queued' GROUP BY kind`)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			var out []prometheus.Metric
			for rows.Next() {
				var kind string
				var n int64
				if err := rows.Scan(&kind, &n); err != nil {
					return nil, err
				}
				out = append(out, prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, float64(n), kind))
			}
			return out, rows.Err()
		},
	})
}
