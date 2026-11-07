package app

import (
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

type funcCollector struct {
	descs   []*prometheus.Desc
	collect func(chan<- prometheus.Metric)
}

func (f funcCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range f.descs {
		ch <- d
	}
}
func (f funcCollector) Collect(ch chan<- prometheus.Metric) { f.collect(ch) }

func TestPoolCollectorValues(t *testing.T) {
	t.Run("R-OPS-007 the pool families carry the counters of pgxpool under the names of the catalogue, one series per pool", func(t *testing.T) {
		c := newPoolCollector(func() *postgres.DB { return nil })
		stats := postgres.Stats{
			Main:    postgres.PoolStats{Max: 19, Total: 10, Idle: 4, Acquired: 5, AcquireCount: 900, EmptyAcquireCount: 7, CanceledAcquireCount: 2, NewConns: 11},
			Control: postgres.PoolStats{Max: 1, Total: 1, Idle: 1, AcquireCount: 50, EmptyAcquireCount: 0, NewConns: 1},
			// no lease pool: a web replica
		}
		reg := prometheus.NewPedanticRegistry()
		reg.MustRegister(funcCollector{descs: []*prometheus.Desc{c.conns, c.maxes, c.empty, c.canceled, c.opened}, collect: func(ch chan<- prometheus.Metric) { c.collect(stats, ch) }})
		checkSeries(t, reg, map[string]float64{
			"trove_db_pool_empty_acquires_total{db_pool=\"control\"}":               0,
			"trove_db_pool_empty_acquires_total{db_pool=\"request\"}":               7,
			"trove_db_pool_canceled_acquires_total{db_pool=\"control\"}":            0,
			"trove_db_pool_canceled_acquires_total{db_pool=\"request\"}":            2,
			"trove_db_pool_connections_opened_total{db_pool=\"control\"}":           1,
			"trove_db_pool_connections_opened_total{db_pool=\"request\"}":           11,
			"trove_db_pool_connections{db_pool=\"control\",state=\"acquired\"}":     0,
			"trove_db_pool_connections{db_pool=\"control\",state=\"constructing\"}": 0,
			"trove_db_pool_connections{db_pool=\"control\",state=\"idle\"}":         1,
			"trove_db_pool_connections{db_pool=\"request\",state=\"acquired\"}":     5,
			"trove_db_pool_connections{db_pool=\"request\",state=\"constructing\"}": 1,
			"trove_db_pool_connections{db_pool=\"request\",state=\"idle\"}":         4,
			"trove_db_pool_max_connections{db_pool=\"control\"}":                    1,
			"trove_db_pool_max_connections{db_pool=\"request\"}":                    19,
		})
	})
}

func TestJobGaugeValues(t *testing.T) {
	t.Run("R-OPS-007 trove_jobs_queued and trove_jobs_oldest_queued_age_seconds carry the count and the age per kind", func(t *testing.T) {
		d := newJobDescs()
		ms := d.metrics([]q.JobQueueGaugesRow{
			{Kind: "gc", Queued: 3, OldestAgeSeconds: 912.5},
			{Kind: "mirror", Queued: 1, OldestAgeSeconds: 0},
		})
		reg := prometheus.NewPedanticRegistry()
		reg.MustRegister(funcCollector{descs: []*prometheus.Desc{d.queued, d.oldest}, collect: func(ch chan<- prometheus.Metric) {
			for _, m := range ms {
				ch <- m
			}
		}})
		checkSeries(t, reg, map[string]float64{
			"trove_jobs_queued{kind=\"gc\"}":                        3,
			"trove_jobs_queued{kind=\"mirror\"}":                    1,
			"trove_jobs_oldest_queued_age_seconds{kind=\"gc\"}":     912.5,
			"trove_jobs_oldest_queued_age_seconds{kind=\"mirror\"}": 0,
		})
	})
	t.Run("R-OPS-007 an empty queue has no series", func(t *testing.T) {
		if ms := newJobDescs().metrics(nil); len(ms) != 0 {
			t.Fatalf("%d series for an empty queue", len(ms))
		}
	})
}

// checkSeries gathers reg and requires exactly the series of want, keyed by name{label="value",...} with the labels sorted.
func checkSeries(t *testing.T, reg *prometheus.Registry, want map[string]float64) {
	t.Helper()
	fams, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]float64{}
	for _, f := range fams {
		for _, m := range f.GetMetric() {
			var ls []string
			for _, l := range m.GetLabel() {
				ls = append(ls, l.GetName()+"="+strconv.Quote(l.GetValue()))
			}
			sort.Strings(ls)
			key := f.GetName()
			if len(ls) > 0 {
				key += "{" + strings.Join(ls, ",") + "}"
			}
			if m.Gauge != nil {
				got[key] = m.Gauge.GetValue()
			} else {
				got[key] = m.Counter.GetValue()
			}
		}
	}
	for k, w := range want {
		if g, ok := got[k]; !ok || g != w {
			t.Errorf("%s = %v (present %v), want %v", k, g, ok, w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("%d series, want %d: %v", len(got), len(want), got)
	}
}
