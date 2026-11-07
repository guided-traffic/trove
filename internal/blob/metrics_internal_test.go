package blob

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/observability"
)

func valueOf(t *testing.T, g prometheus.Gauge) float64 {
	t.Helper()
	var m dto.Metric
	if err := g.Write(&m); err != nil {
		t.Fatal(err)
	}
	return m.GetGauge().GetValue()
}

func TestSpoolGauges(t *testing.T) {
	const free = uint64(10 << 30)
	t.Run("R-OPS-007 trove_spool_used_bytes is the budget held by running uploads and trove_spool_free_bytes the free space of the volume", func(t *testing.T) {
		m := NewMetrics(observability.NewMetrics("packages"))
		sp := newSpool(t, func(c *config.StorageSpool) { c.MaxBytes = 64 << 20 },
			WithSpoolMetrics(m), WithFreeBytes(func(string) (uint64, error) { return free, nil }))
		body := data(3 << 20)
		got, err := sp.Ingest(context.Background(), bytes.NewReader(body), IngestOptions{ContentLength: int64(len(body))})
		if err != nil {
			t.Fatal(err)
		}
		if v := valueOf(t, m.spoolUsed); v != float64(len(body)) {
			t.Fatalf("used while the upload runs = %v, want %d", v, len(body))
		}
		if v := valueOf(t, m.spoolFree); v != float64(free) {
			t.Fatalf("free = %v, want %d", v, free)
		}
		if err := got.Close(); err != nil {
			t.Fatal(err)
		}
		if v := valueOf(t, m.spoolUsed); v != 0 {
			t.Fatalf("used after the upload ended = %v, want 0", v)
		}
	})

	t.Run("R-OPS-007 the free bytes are read again when the budget is returned", func(t *testing.T) {
		m := NewMetrics(observability.NewMetrics("packages"))
		now := free
		sp := newSpool(t, nil, WithSpoolMetrics(m), WithFreeBytes(func(string) (uint64, error) { return now, nil }))
		body := data(3 << 20)
		got, err := sp.Ingest(context.Background(), bytes.NewReader(body), IngestOptions{ContentLength: int64(len(body))})
		if err != nil {
			t.Fatal(err)
		}
		now = free / 2
		if err := got.Close(); err != nil {
			t.Fatal(err)
		}
		if v := valueOf(t, m.spoolFree); v != float64(free/2) {
			t.Fatalf("free after the release = %v, want %d", v, free/2)
		}
	})
}

func TestGCLastSuccess(t *testing.T) {
	t.Run("R-OPS-007 trove_gc_last_success_timestamp_seconds holds the end of the last successful run in seconds", func(t *testing.T) {
		m := NewMetrics(observability.NewMetrics("worker"))
		if n := testCount(m.gcLastOK); n != 0 {
			t.Fatalf("%d series before any run, want none: an install that never ran GC must not report a time of 0", n)
		}
		at := time.Unix(1_700_000_000, 500_000_000)
		m.gcSucceeded(at)
		if v := valueOf(t, m.gcLastOK.WithLabelValues()); v != 1_700_000_000.5 {
			t.Fatalf("= %v, want 1700000000.5", v)
		}
	})
}

// testCount is the number of series a vector holds.
func testCount(v *prometheus.GaugeVec) int {
	ch := make(chan prometheus.Metric, 8)
	v.Collect(ch)
	close(ch)
	n := 0
	for range ch {
		n++
	}
	return n
}
