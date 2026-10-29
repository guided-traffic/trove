package observability

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// GaugeSource is one set of gauges read from PostgreSQL (inventory, queue,
// backup-run, registry and CI gauges, Q-OPS-18). Collect runs in the
// background, never on a scrape.
type GaugeSource struct {
	// Name identifies the source in log lines.
	Name string
	// Descs lists every descriptor Collect may return a metric for.
	Descs []*prometheus.Desc
	// Collect reads the values with the given context, which carries the
	// evaluation deadline, and returns constant metrics (prometheus.MustNewConstMetric).
	Collect func(ctx context.Context) ([]prometheus.Metric, error)
}

// DBGauges evaluates the registered sources once per metrics.db_gauge_interval
// and serves the last values to every scrape. The database load of these gauges
// thus depends on the replica count and the interval, never on the scrape rate
// (Q-OPS-18). trove_metrics_db_gauges_last_success_timestamp_seconds is their
// age.
type DBGauges struct {
	interval time.Duration
	lastDesc *prometheus.Desc
	log      *slog.Logger

	mu       sync.RWMutex
	sources  []GaugeSource
	values   map[string][]prometheus.Metric
	lastOK   time.Time
	evalLock sync.Mutex
}

func newDBGauges(m *Metrics) *DBGauges {
	interval := m.cfg.DBGaugeInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &DBGauges{
		interval: interval,
		values:   map[string][]prometheus.Metric{},
		lastDesc: prometheus.NewDesc("trove_metrics_db_gauges_last_success_timestamp_seconds",
			"End of the last successful evaluation of the gauges read from PostgreSQL.", nil, nil),
	}
}

// SetLogger sets the logger for evaluation failures.
func (g *DBGauges) SetLogger(l *slog.Logger) {
	g.mu.Lock()
	g.log = l
	g.mu.Unlock()
}

// Add registers a source. Its Descs join the registry's descriptors, so a source
// may be added at any time before the first scrape that should show it.
func (g *DBGauges) Add(src GaugeSource) {
	g.mu.Lock()
	g.sources = append(g.sources, src)
	g.mu.Unlock()
}

// Describe implements prometheus.Collector.
func (g *DBGauges) Describe(ch chan<- *prometheus.Desc) {
	ch <- g.lastDesc
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, s := range g.sources {
		for _, d := range s.Descs {
			ch <- d
		}
	}
}

// Collect implements prometheus.Collector with the last evaluated values.
func (g *DBGauges) Collect(ch chan<- prometheus.Metric) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, ms := range g.values {
		for _, m := range ms {
			ch <- m
		}
	}
	if !g.lastOK.IsZero() {
		ch <- prometheus.MustNewConstMetric(g.lastDesc, prometheus.GaugeValue, float64(g.lastOK.UnixNano())/1e9)
	}
}

// Evaluate reads every source once with a deadline of the interval (at most
// 10 s). A failing source keeps its previous values; the age gauge advances only
// when every source succeeded.
func (g *DBGauges) Evaluate(ctx context.Context) {
	g.evalLock.Lock()
	defer g.evalLock.Unlock()
	g.mu.RLock()
	sources := append([]GaugeSource(nil), g.sources...)
	log := g.log
	g.mu.RUnlock()

	timeout := min(g.interval, 10*time.Second)
	ok := true
	for _, s := range sources {
		sctx, cancel := context.WithTimeout(ctx, timeout)
		ms, err := s.Collect(sctx)
		cancel()
		if err != nil {
			ok = false
			if log != nil {
				log.Warn("database gauge evaluation failed", "component", "metrics", "source", s.Name, "error", err)
			}
			continue
		}
		g.mu.Lock()
		g.values[s.Name] = ms
		g.mu.Unlock()
	}
	if ok {
		g.mu.Lock()
		g.lastOK = time.Now()
		g.mu.Unlock()
	}
}

// Run evaluates immediately and then once per interval until ctx ends.
func (g *DBGauges) Run(ctx context.Context) {
	g.Evaluate(ctx)
	t := time.NewTicker(g.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			g.Evaluate(ctx)
		}
	}
}
