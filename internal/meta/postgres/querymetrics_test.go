package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/observability"
)

// seriesOf returns the labels' value and the sample count or counter value of every series of a family, keyed by the query label.
func seriesOf(t *testing.T, m *observability.Metrics, name string) map[string]float64 {
	t.Helper()
	out := map[string]float64{}
	f := family(t, m, name)
	if f == nil {
		return out
	}
	for _, s := range f.GetMetric() {
		if len(s.GetLabel()) != 1 || s.GetLabel()[0].GetName() != "query" {
			t.Fatalf("%s: labels %v, want query alone", name, s.GetLabel())
		}
		v := s.GetCounter().GetValue()
		if h := s.GetHistogram(); h != nil {
			v = float64(h.GetSampleCount())
		}
		out[s.GetLabel()[0].GetValue()] = v
	}
	return out
}

// run drives the tracer through one statement that takes d.
func run(tr stmtTracer, sql string, d time.Duration) {
	ctx := tr.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: sql})
	time.Sleep(d)
	tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})
}

func TestR_OPS_008_TheStatementMetricsCountByTheNameOfTheQuery(t *testing.T) {
	build := func(threshold time.Duration, histograms bool) (*observability.Metrics, stmtTracer) {
		m := observability.NewMetrics("api", observability.WithMetricsConfig(config.Metrics{
			Enabled: true, SlowQueryThreshold: threshold, DBQueryHistograms: histograms,
		}))
		return m, stmtTracer{pool: poolRequest, metrics: NewMetrics(m)}
	}

	t.Run("a statement slower than the threshold counts under its query name and a faster one does not", func(t *testing.T) {
		m, tr := build(40*time.Millisecond, false)
		run(tr, "-- name: GetThing :one\nSELECT 1", 60*time.Millisecond)
		run(tr, "-- name: GetThing :one\nSELECT 1", 0)
		run(tr, "-- name: Quick :exec\nSELECT 1", 0)
		if got := seriesOf(t, m, "trove_db_slow_queries_total"); len(got) != 1 || got["GetThing"] != 1 {
			t.Fatalf("slow queries = %v, want GetThing 1 alone", got)
		}
		if f := family(t, m, "trove_db_query_duration_seconds"); f != nil {
			t.Fatal("the duration histogram is registered without metrics.db_query_histograms")
		}
	})

	t.Run("the label is a bounded name and never the text of the statement", func(t *testing.T) {
		m, tr := build(10*time.Millisecond, false)
		for _, sql := range []string{"SELECT 1 WHERE id = 1", "SELECT 1 WHERE id = 2", "  -- name:   Padded   :many\nSELECT 1"} {
			run(tr, sql, 20*time.Millisecond)
		}
		got := seriesOf(t, m, "trove_db_slow_queries_total")
		if len(got) != 2 || got[unnamedQuery] != 2 || got["Padded"] != 1 {
			t.Fatalf("slow queries = %v, want unnamed 2 and Padded 1", got)
		}
	})

	t.Run("the statements of a transaction's protocol are labelled by their verb", func(t *testing.T) {
		m, tr := build(10*time.Millisecond, false)
		for _, sql := range []string{"begin", "BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY", "commit", "Rollback;"} {
			run(tr, sql, 20*time.Millisecond)
		}
		got := seriesOf(t, m, "trove_db_slow_queries_total")
		if len(got) != 3 || got["begin"] != 2 || got["commit"] != 1 || got["rollback"] != 1 {
			t.Fatalf("slow queries = %v, want begin 2, commit 1, rollback 1", got)
		}
	})

	t.Run("a threshold of 0 counts none", func(t *testing.T) {
		m, tr := build(0, false)
		run(tr, "-- name: GetThing :one\nSELECT 1", 20*time.Millisecond)
		if got := seriesOf(t, m, "trove_db_slow_queries_total"); len(got) != 0 {
			t.Fatalf("slow queries = %v", got)
		}
	})

	t.Run("a failed statement counts like any other", func(t *testing.T) {
		m, tr := build(10*time.Millisecond, false)
		ctx := tr.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: "-- name: Hung :one\nSELECT 1"})
		time.Sleep(20 * time.Millisecond)
		tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{Err: context.DeadlineExceeded})
		if got := seriesOf(t, m, "trove_db_slow_queries_total"); got["Hung"] != 1 {
			t.Fatalf("slow queries = %v", got)
		}
	})

	t.Run("with metrics.db_query_histograms every statement is observed by its query name", func(t *testing.T) {
		m, tr := build(time.Hour, true)
		run(tr, "-- name: GetThing :one\nSELECT 1", 0)
		run(tr, "-- name: GetThing :one\nSELECT 1", 0)
		run(tr, "SELECT 2", 0)
		if got := seriesOf(t, m, "trove_db_query_duration_seconds"); len(got) != 2 || got["GetThing"] != 2 || got[unnamedQuery] != 1 {
			t.Fatalf("durations = %v", got)
		}
		if got := seriesOf(t, m, "trove_db_slow_queries_total"); len(got) != 0 {
			t.Fatalf("slow queries = %v", got)
		}
		if errs := m.Check(); len(errs) != 0 {
			t.Fatalf("the families break R-OPS-007: %v", errs)
		}
	})

	t.Run("a pipeline is one round trip, counted under the label pipeline", func(t *testing.T) {
		m, tr := build(10*time.Millisecond, false)
		ctx := tr.TraceBatchStart(context.Background(), nil, pgx.TraceBatchStartData{})
		time.Sleep(20 * time.Millisecond)
		tr.TraceBatchEnd(ctx, nil, pgx.TraceBatchEndData{})
		if got := seriesOf(t, m, "trove_db_slow_queries_total"); got[pipelineQuery] != 1 {
			t.Fatalf("slow queries = %v", got)
		}
	})

	t.Run("without Metrics the tracer records nothing", func(t *testing.T) {
		tr := stmtTracer{pool: poolRequest}
		run(tr, "-- name: GetThing :one\nSELECT 1", 0)
	})
}

var longStatement = "-- name: GetThing :one\nSELECT " + strings.Repeat("a, ", 600) + "1 FROM t WHERE x = $1"

func TestR_OPS_008_AFastStatementEndsWithoutAllocating(t *testing.T) {
	m := observability.NewMetrics("api", observability.WithMetricsConfig(config.Metrics{Enabled: true, SlowQueryThreshold: time.Hour}))
	tr := stmtTracer{pool: poolRequest, metrics: NewMetrics(m)}
	ctx := tr.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: longStatement})
	if n := testing.AllocsPerRun(100, func() { tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{}) }); n != 0 {
		t.Fatalf("TraceQueryEnd allocated %v times for a fast statement with the histogram off", n)
	}
}

func BenchmarkStatementTracer(b *testing.B) {
	m := observability.NewMetrics("api", observability.WithMetricsConfig(config.Metrics{Enabled: true, SlowQueryThreshold: time.Hour}))
	tr := stmtTracer{pool: poolRequest, metrics: NewMetrics(m)}
	b.ReportAllocs()
	b.Run("start and end", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			ctx := tr.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: longStatement})
			tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})
		}
	})
	b.Run("end", func(b *testing.B) {
		ctx := tr.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: longStatement})
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})
		}
	})
}
