package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/observability"
)

// Metrics are the Prometheus families of the database layer that the pools'
// own counters cannot give (R-OPS-007, R-HA-009): the retried transactions by
// kind, the commits with an unknown outcome, and the wait for a connection as a
// histogram per pool. A nil *Metrics records nothing.
type Metrics struct {
	retries       *prometheus.CounterVec
	commitUnknown prometheus.Counter
	acquire       *prometheus.HistogramVec
}

// acquireBuckets span a free pool (microseconds) to a saturated one (the
// request deadline). The alerts of R-HA-045 compare the 95th percentile with
// 0.1 s (request pool) and 1 s (control connection), which are bucket bounds.
func acquireBuckets() []float64 {
	return []float64{0.0005, 0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}
}

// NewMetrics registers the families on m. It panics, like every registration in
// internal/observability, when called twice for one registry.
func NewMetrics(m *observability.Metrics) *Metrics {
	return &Metrics{
		retries: m.NewCounterVec("trove_db_retries_total",
			"Transactions and reads repeated after a retryable error, by kind (connection, shutdown, read_only, serialization, too_many_connections).", "kind"),
		commitUnknown: m.NewCounterVec("trove_db_commit_unknown_total",
			"Commits whose connection ended during COMMIT, so that the outcome is unknown; they are never repeated.").WithLabelValues(),
		acquire: m.NewHistogramVec("trove_db_pool_acquire_duration_seconds",
			"Wait for a connection of a pool; for the control pool the headroom of the readiness-critical loops.", acquireBuckets(), "db_pool"),
	}
}

// Retry kinds, the values of the label kind of trove_db_retries_total (R-HA-009).
const (
	retryKindConnection         = "connection"
	retryKindShutdown           = "shutdown"
	retryKindReadOnly           = "read_only"
	retryKindSerialization      = "serialization"
	retryKindTooManyConnections = "too_many_connections"
)

// retryLabel names the cause of a retry for the metric.
func retryLabel(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		switch pe.Code {
		case "40001", "40P01":
			return retryKindSerialization
		case "57P01", "57P02", "57P03":
			return retryKindShutdown
		case "25006":
			return retryKindReadOnly
		case "53300":
			return retryKindTooManyConnections
		}
	}
	return retryKindConnection
}

func (m *Metrics) retried(err error) {
	if m != nil {
		m.retries.WithLabelValues(retryLabel(err)).Inc()
	}
}

func (m *Metrics) unknownCommit() {
	if m != nil {
		m.commitUnknown.Inc()
	}
}

func (m *Metrics) acquired(pool string, d time.Duration) {
	if m != nil {
		m.acquire.WithLabelValues(pool).Observe(d.Seconds())
	}
}

// Names of the pools, the values of the label db_pool.
const (
	poolRequest = "request"
	poolControl = "control"
	poolLease   = "lease"
)

type acquireStartKey struct{}

var _ pgxpool.AcquireTracer = stmtTracer{}

// TraceAcquireStart implements pgxpool.AcquireTracer.
func (t stmtTracer) TraceAcquireStart(ctx context.Context, _ *pgxpool.Pool, _ pgxpool.TraceAcquireStartData) context.Context {
	if t.metrics == nil {
		return ctx
	}
	return context.WithValue(ctx, acquireStartKey{}, time.Now())
}

// TraceAcquireEnd implements pgxpool.AcquireTracer. A failed acquisition (the
// caller's deadline, a closed pool) counts too: it is the longest wait.
func (t stmtTracer) TraceAcquireEnd(ctx context.Context, _ *pgxpool.Pool, _ pgxpool.TraceAcquireEndData) {
	if start, ok := ctx.Value(acquireStartKey{}).(time.Time); ok {
		t.metrics.acquired(t.pool, time.Since(start))
	}
}
