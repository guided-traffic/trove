package blob_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/blob/blobtest"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/observability"
)

func TestGuardBreaker(t *testing.T) {
	cfg := config.StorageS3Breaker{FailureThreshold: 5, OpenDuration: 10 * time.Second}
	now := time.Now()
	clock := func() time.Time { return now }
	fail5xx := func(op, _ string) error {
		return &blob.StoreError{Op: op, Status: 503, Code: "SlowDown", Kind: blob.KindTransient}
	}

	newGuard := func(t *testing.T) (*blob.Guard, *blobtest.Store, *observability.Metrics) {
		t.Helper()
		mem := blobtest.New()
		m := observability.NewMetrics("web")
		g := blob.NewGuard(mem, cfg, blob.WithGuardClock(clock), blob.WithGuardMetrics(blob.NewMetrics(m)))
		return g, mem, m
	}
	head := func(g *blob.Guard) error { _, err := g.Head(context.Background(), "k"); return err }

	t.Run("R-STOR-033 six consecutive failures open the circuit and the next call fails without reaching the store", func(t *testing.T) {
		now = time.Now()
		g, mem, m := newGuard(t)
		mem.SetFault(fail5xx)
		for i := 0; i < 5; i++ {
			if err := head(g); !blob.IsTransient(err) {
				t.Fatalf("call %d: %v", i, err)
			}
		}
		before := mem.Count("head")
		if err := head(g); !errors.Is(err, blob.ErrCircuitOpen) {
			t.Fatalf("sixth call: %v", err)
		}
		if mem.Count("head") != before || !g.Open() {
			t.Fatal("the sixth call reached the store or the circuit is closed")
		}
		if v := gauge(t, m, "trove_storage_s3_circuit_open"); v != 1 {
			t.Fatalf("trove_storage_s3_circuit_open = %v", v)
		}
	})
	t.Run("R-STOR-033 404 and other 4xx answers do not count as failures", func(t *testing.T) {
		now = time.Now()
		g, mem, _ := newGuard(t)
		mem.SetFault(func(op, _ string) error { return &blob.StoreError{Op: op, Status: 403, Kind: blob.KindConfig} })
		for i := 0; i < 20; i++ {
			_ = head(g)
		}
		if g.Open() {
			t.Fatal("403 answers opened the circuit")
		}
		mem.SetFault(nil)
		for i := 0; i < 20; i++ {
			if err := head(g); !errors.Is(err, blob.ErrNotFound) {
				t.Fatal(err)
			}
		}
		if g.Open() {
			t.Fatal("404 answers opened the circuit")
		}
	})
	t.Run("R-STOR-033 a success resets the count of consecutive failures", func(t *testing.T) {
		now = time.Now()
		g, mem, _ := newGuard(t)
		for round := 0; round < 3; round++ {
			mem.SetFault(fail5xx)
			for i := 0; i < 4; i++ {
				_ = head(g)
			}
			mem.SetFault(nil)
			_ = head(g)
		}
		if g.Open() {
			t.Fatal("non-consecutive failures opened the circuit")
		}
	})
	t.Run("R-STOR-033 after open_duration one probe request goes through and a success closes the circuit", func(t *testing.T) {
		now = time.Now()
		g, mem, m := newGuard(t)
		mem.SetFault(fail5xx)
		for i := 0; i < 5; i++ {
			_ = head(g)
		}
		now = now.Add(11 * time.Second)
		mem.SetFault(nil)
		if err := head(g); !errors.Is(err, blob.ErrNotFound) {
			t.Fatalf("probe: %v", err)
		}
		if g.Open() || gauge(t, m, "trove_storage_s3_circuit_open") != 0 {
			t.Fatal("the circuit did not close")
		}
	})
	t.Run("R-STOR-033 a failed probe opens the circuit for another open_duration", func(t *testing.T) {
		now = time.Now()
		g, mem, _ := newGuard(t)
		mem.SetFault(fail5xx)
		for i := 0; i < 5; i++ {
			_ = head(g)
		}
		now = now.Add(11 * time.Second)
		if err := head(g); !blob.IsTransient(err) {
			t.Fatalf("probe: %v", err)
		}
		if err := head(g); !errors.Is(err, blob.ErrCircuitOpen) {
			t.Fatalf("after the failed probe: %v", err)
		}
	})
	t.Run("R-STOR-033 a cancelled caller is no evidence about the store", func(t *testing.T) {
		now = time.Now()
		g, mem, _ := newGuard(t)
		mem.SetFault(func(string, string) error { return context.Canceled })
		for i := 0; i < 10; i++ {
			_ = head(g)
		}
		if g.Open() {
			t.Fatal("cancellations opened the circuit")
		}
	})
	t.Run("R-STOR-033 requests and bytes are counted per operation", func(t *testing.T) {
		now = time.Now()
		g, _, m := newGuard(t)
		data := []byte("hello world")
		if err := g.Put(context.Background(), "k", bytes.NewReader(data), int64(len(data)), blob.Checksum{}); err != nil {
			t.Fatal(err)
		}
		body, _, err := g.Get(context.Background(), "k", nil)
		if err != nil {
			t.Fatal(err)
		}
		_ = readAll(t, body)
		_ = head(g)
		if v := counter(t, m, "trove_storage_s3_requests_total", map[string]string{"operation": "put", "outcome": "ok"}); v != 1 {
			t.Fatalf("put ok = %v", v)
		}
		if v := counter(t, m, "trove_storage_s3_bytes_total", map[string]string{"direction": "up"}); v != float64(len(data)) {
			t.Fatalf("up bytes = %v", v)
		}
		if v := counter(t, m, "trove_storage_s3_bytes_total", map[string]string{"direction": "down"}); v != float64(len(data)) {
			t.Fatalf("down bytes = %v", v)
		}
		if errs := m.Check(); len(errs) > 0 {
			t.Fatalf("metric naming: %v", errs)
		}
	})
}
