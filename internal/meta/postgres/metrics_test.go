package postgres

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	dto "github.com/prometheus/client_model/go"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/observability"
)

func family(t *testing.T, m *observability.Metrics, name string) *dto.MetricFamily {
	t.Helper()
	fams, err := m.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fams {
		if f.GetName() == name {
			return f
		}
	}
	return nil
}

// counterOf returns the value of the series of a counter family with the label kind, 0 when it has none.
func counterOf(t *testing.T, m *observability.Metrics, name, kind string) float64 {
	t.Helper()
	f := family(t, m, name)
	if f == nil {
		return 0
	}
	for _, s := range f.GetMetric() {
		if kind == "" && len(s.GetLabel()) == 0 {
			return s.GetCounter().GetValue()
		}
		for _, l := range s.GetLabel() {
			if l.GetName() == "kind" && l.GetValue() == kind {
				return s.GetCounter().GetValue()
			}
		}
	}
	return 0
}

func TestDBMetrics(t *testing.T) {
	cfg := config.DatabaseRetry{InitialBackoff: 100 * time.Millisecond, MaxBackoff: 400 * time.Millisecond, MaxElapsed: time.Second}
	newR := func(m *Metrics) retrier {
		now := time.Unix(0, 0)
		return retrier{
			cfg:     cfg,
			sleep:   func(_ context.Context, d time.Duration) error { now = now.Add(d); return nil },
			now:     func() time.Time { return now },
			metrics: m,
		}
	}

	t.Run("R-OPS-007 every repeat is counted under the kind of the error that caused it", func(t *testing.T) {
		cases := []struct {
			name string
			err  error
			kind string
		}{
			{"a serialization failure", &pgconn.PgError{Code: "40001"}, "serialization"},
			{"a deadlock", &pgconn.PgError{Code: "40P01"}, "serialization"},
			{"a server shutting down", &pgconn.PgError{Code: "57P01"}, "shutdown"},
			{"a server starting up", &pgconn.PgError{Code: "57P03"}, "shutdown"},
			{"a demoted primary", &pgconn.PgError{Code: "25006"}, "read_only"},
			{"a server without connection slots", &pgconn.PgError{Code: "53300"}, "too_many_connections"},
			{"a connection that ended", io.EOF, "connection"},
			{"a connection error class", &pgconn.PgError{Code: "08006"}, "connection"},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				m := observability.NewMetrics("api")
				r := newR(NewMetrics(m))
				calls := 0
				// Two failures, then success: exactly two repeats.
				err := r.do(context.Background(), func() error {
					calls++
					if calls <= 2 {
						return c.err
					}
					return nil
				})
				if err != nil || calls != 3 {
					t.Fatalf("calls = %d, err = %v", calls, err)
				}
				if got := counterOf(t, m, "trove_db_retries_total", c.kind); got != 2 {
					t.Fatalf("trove_db_retries_total{kind=%q} = %v, want 2", c.kind, got)
				}
			})
		}
	})

	t.Run("R-OPS-007 a failure that is not repeated is not counted", func(t *testing.T) {
		m := observability.NewMetrics("api")
		r := newR(NewMetrics(m))
		_ = r.do(context.Background(), func() error { return &pgconn.PgError{Code: "23505"} })
		if f := family(t, m, "trove_db_retries_total"); f != nil && len(f.GetMetric()) != 0 {
			t.Fatalf("a constraint violation was counted: %v", f)
		}
	})

	t.Run("R-OPS-007 a commit with an unknown outcome is counted once and never repeated", func(t *testing.T) {
		m := observability.NewMetrics("api")
		r := newR(NewMetrics(m))
		calls := 0
		_ = r.do(context.Background(), func() error { calls++; return &outcomeUnknownError{err: io.EOF} })
		if calls != 1 {
			t.Fatalf("calls = %d", calls)
		}
		if got := counterOf(t, m, "trove_db_commit_unknown_total", ""); got != 1 {
			t.Fatalf("trove_db_commit_unknown_total = %v, want 1", got)
		}
		if got := counterOf(t, m, "trove_db_retries_total", "connection"); got != 0 {
			t.Fatalf("an unknown commit counted as a retry: %v", got)
		}
	})

	t.Run("R-OPS-007 a nil Metrics records nothing", func(t *testing.T) {
		r := newR(nil)
		calls := 0
		if err := r.do(context.Background(), func() error {
			calls++
			if calls < 2 {
				return io.EOF
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestAcquireDuration(t *testing.T) {
	var _ pgxpool.AcquireTracer = stmtTracer{}

	t.Run("R-OPS-007 the wait between the start and the end of an acquisition lands in the histogram of its pool", func(t *testing.T) {
		m := observability.NewMetrics("api")
		tr := stmtTracer{pool: poolControl, metrics: NewMetrics(m)}
		ctx := tr.TraceAcquireStart(context.Background(), nil, pgxpool.TraceAcquireStartData{})
		time.Sleep(30 * time.Millisecond)
		tr.TraceAcquireEnd(ctx, nil, pgxpool.TraceAcquireEndData{})

		f := family(t, m, "trove_db_pool_acquire_duration_seconds")
		if f == nil || len(f.GetMetric()) != 1 {
			t.Fatalf("family = %v", f)
		}
		s := f.GetMetric()[0]
		if l := s.GetLabel(); len(l) != 1 || l[0].GetName() != "db_pool" || l[0].GetValue() != "control" {
			t.Fatalf("labels = %v", l)
		}
		h := s.GetHistogram()
		if h.GetSampleCount() != 1 || h.GetSampleSum() < 0.03 || h.GetSampleSum() > 5 {
			t.Fatalf("count %d sum %v, want one wait of at least 30 ms", h.GetSampleCount(), h.GetSampleSum())
		}
		// The wait is above the 25 ms bound and below the 50 ms bound of the control alert's neighbours.
		for _, b := range h.GetBucket() {
			if b.GetUpperBound() == 0.025 && b.GetCumulativeCount() != 0 {
				t.Fatalf("a 30 ms wait is in the bucket le=0.025")
			}
		}
	})

	t.Run("R-OPS-007 the bounds of the alerts are bucket bounds", func(t *testing.T) {
		have := map[float64]bool{}
		for _, b := range acquireBuckets() {
			have[b] = true
		}
		for _, want := range []float64{0.1, 1} {
			if !have[want] {
				t.Errorf("no bucket le=%v: the quantile of TroveDatabasePoolSaturated or TroveControlConnectionWaits would interpolate across it", want)
			}
		}
	})

	t.Run("R-OPS-007 without Metrics the tracer adds nothing to the context", func(t *testing.T) {
		tr := stmtTracer{pool: poolRequest}
		ctx := tr.TraceAcquireStart(context.Background(), nil, pgxpool.TraceAcquireStartData{})
		if ctx.Value(acquireStartKey{}) != nil {
			t.Fatal("the start time is stored without a histogram")
		}
		tr.TraceAcquireEnd(ctx, nil, pgxpool.TraceAcquireEndData{})
	})
}
