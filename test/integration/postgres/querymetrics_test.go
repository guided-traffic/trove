//go:build integration

package postgres

import (
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/observability"
)

// A statement of the pool that runs longer than metrics.slow_query_threshold counts in trove_db_slow_queries_total under the name
// of its query (R-OPS-008).
func TestR_OPS_008_AStatementSlowerThanTheThresholdCountsUnderItsQueryName(t *testing.T) {
	st := getStack(t)
	cfg, _, _ := migrated(t, st)
	m := observability.NewMetrics(meta.RoleWeb, observability.WithMetricsConfig(config.Metrics{
		Enabled: true, SlowQueryThreshold: 200 * time.Millisecond, DBQueryHistograms: true,
	}))
	db, err := postgres.Open(ctxT(t, 30*time.Second), cfg, "pod-slow-queries", meta.RoleWeb, quiet, postgres.WithMetrics(postgres.NewMetrics(m)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	ctx := ctxT(t, 30*time.Second)

	if _, err := db.Main().Exec(ctx, "-- name: SleepFast :exec\nSELECT pg_sleep(0.01)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Main().Exec(ctx, "-- name: SleepSlow :exec\nSELECT pg_sleep(0.4)"); err != nil {
		t.Fatal(err)
	}

	tx, err := db.Main().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "-- name: InTx :exec\nSELECT 1"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	fams, err := m.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	slow, observed := map[string]float64{}, map[string]uint64{}
	for _, f := range fams {
		for _, s := range f.GetMetric() {
			if len(s.GetLabel()) != 1 || s.GetLabel()[0].GetName() != "query" {
				continue
			}
			q := s.GetLabel()[0].GetValue()
			switch f.GetName() {
			case "trove_db_slow_queries_total":
				slow[q] = s.GetCounter().GetValue()
			case "trove_db_query_duration_seconds":
				observed[q] = s.GetHistogram().GetSampleCount()
			}
		}
	}
	if slow["SleepSlow"] != 1 || slow["SleepFast"] != 0 {
		t.Errorf("slow statements = %v, want SleepSlow 1 and no SleepFast", slow)
	}
	if observed["SleepSlow"] != 1 || observed["SleepFast"] != 1 {
		t.Errorf("observed statements = %v, want one of each", observed)
	}
	if observed["begin"] < 1 || observed["commit"] < 1 || observed["unnamed"] != 0 {
		t.Errorf("observed statements = %v, want the begin and the commit of the transaction labelled by their verb and nothing unnamed", observed)
	}
	if errs := m.Check(); len(errs) != 0 {
		t.Errorf("R-OPS-007: %v", errs)
	}
}
