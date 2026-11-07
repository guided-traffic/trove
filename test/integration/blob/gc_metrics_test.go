//go:build integration

package blob

import (
	"context"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/observability"
)

// gaugeValue reads a gauge without labels from the registry; present is false when the family has no series.
func gaugeValue(tb testing.TB, m *observability.Metrics, name string) (v float64, present bool) {
	tb.Helper()
	fams, err := m.Registry().Gather()
	if err != nil {
		tb.Fatal(err)
	}
	for _, f := range fams {
		if f.GetName() == name && len(f.GetMetric()) > 0 {
			return f.GetMetric()[0].GetGauge().GetValue(), true
		}
	}
	return 0, false
}

func TestGCLastSuccessMetric(t *testing.T) {
	e := newS3(t, "gcmetric")
	p := newPG(t)
	r := newReplica(t, e, p, nil)
	m := observability.NewMetrics("worker")
	c := blob.NewCollector(r.cfg.GC, r.cfg.S3.Prefix, r.obj, p.db, blob.WithCollectorLogger(quiet), blob.WithCollectorMetrics(blob.NewMetrics(m)))
	ctx := context.Background()

	t.Run("R-OPS-007 a dry run does not move trove_gc_last_success_timestamp_seconds", func(t *testing.T) {
		if _, err := c.Run(ctx, blob.RunOptions{Mode: blob.GCIncremental, DryRun: true}); err != nil {
			t.Fatal(err)
		}
		if v, _ := gaugeValue(t, m, "trove_gc_last_success_timestamp_seconds"); v != 0 {
			t.Fatalf("a dry run set the gauge to %v", v)
		}
	})

	t.Run("R-OPS-007 a run that finished sets trove_gc_last_success_timestamp_seconds to its end", func(t *testing.T) {
		before := time.Now()
		if _, err := c.Run(ctx, blob.RunOptions{Mode: blob.GCIncremental}); err != nil {
			t.Fatal(err)
		}
		v, ok := gaugeValue(t, m, "trove_gc_last_success_timestamp_seconds")
		if !ok || v < float64(before.Unix()) || v > float64(time.Now().Unix())+1 {
			t.Fatalf("gauge = %v (present %v), want a time between %d and now", v, ok, before.Unix())
		}
	})
}
