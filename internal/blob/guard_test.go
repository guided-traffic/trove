package blob_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/blob/blobtest"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
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
	t.Run("R-STOR-033 a call that only hit the caller's deadline is no answer: it neither closes nor opens the circuit", func(t *testing.T) {
		now = time.Now()
		g, mem, _ := newGuard(t)
		mem.SetFault(fail5xx)
		for i := 0; i < 5; i++ {
			_ = head(g)
		}
		if !g.Open() {
			t.Fatal("five transient failures should open the circuit")
		}
		now = now.Add(11 * time.Second)
		mem.SetFault(func(string, string) error { return context.DeadlineExceeded })
		if err := head(g); !errors.Is(err, context.DeadlineExceeded) { // the probe
			t.Fatalf("probe: %v", err)
		}
		if !g.Open() {
			t.Error("a probe that ended on the caller's deadline closed the circuit")
		}
		// And in the closed state it does not count toward the threshold.
		g2, mem2, _ := newGuard(t)
		mem2.SetFault(func(string, string) error { return context.DeadlineExceeded })
		for i := 0; i < 20; i++ {
			_ = head(g2)
		}
		if g2.Open() {
			t.Error("caller deadlines opened the circuit")
		}
		// A deadline inside the transport arrives as a transient store error and counts.
		g3, mem3, _ := newGuard(t)
		mem3.SetFault(func(op, _ string) error {
			return &blob.StoreError{Op: op, Kind: blob.KindTransient, Err: context.DeadlineExceeded}
		})
		for i := 0; i < 5; i++ {
			_ = head(g3)
		}
		if !g3.Open() {
			t.Error("transport timeouts must count as failures")
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

// R-HA-010: while the circuit is open, a client is told to come back after its open duration, rounded up to whole seconds
// and at least five, and an upload that waits for "100 Continue" is refused before its body.
func TestGuardRejecting(t *testing.T) {
	fail := func(op, _ string) error {
		return &blob.StoreError{Op: op, Status: 503, Code: "SlowDown", Kind: blob.KindTransient}
	}
	for _, c := range []struct {
		open time.Duration
		want time.Duration
	}{{2 * time.Second, 5 * time.Second}, {10 * time.Second, 10 * time.Second}, {10500 * time.Millisecond, 11 * time.Second}} {
		now := time.Now()
		mem := blobtest.New()
		g := blob.NewGuard(mem, config.StorageS3Breaker{FailureThreshold: 2, OpenDuration: c.open}, blob.WithGuardClock(func() time.Time { return now }))
		if _, rej := g.Rejecting(); rej {
			t.Fatal("a closed circuit rejects")
		}
		mem.SetFault(fail)
		var opening error
		for i := 0; i < 2; i++ {
			_, opening = g.Head(context.Background(), "k")
		}
		after, rej := g.Rejecting()
		if !rej || after != c.want {
			t.Fatalf("open duration %s: rejecting %v, Retry-After %s, want %s", c.open, rej, after, c.want)
		}
		var e *errs.Error
		if !errors.As(blob.MapStoreErr(opening), &e) || e.RetryAfter != c.want {
			t.Fatalf("the failure that opened the circuit answered with %v, want Retry-After %s", opening, c.want)
		}
		if _, err := g.Head(context.Background(), "k"); !errors.Is(err, blob.ErrCircuitOpen) || !errors.As(blob.MapStoreErr(err), &e) || e.RetryAfter != c.want {
			t.Fatalf("a call on the open circuit answered %v", err)
		}
		now = now.Add(c.open + time.Second)
		if _, rej := g.Rejecting(); rej {
			t.Fatalf("the circuit still rejects after its open duration %s", c.open)
		}
	}
}

// R-STOR-036: a call that timed out inside the transport (a dial to a store that is gone) answers 503, not 500; a deadline
// or a cancellation of the caller's own context stays what it is.
func TestMapStoreErrTransportTimeout(t *testing.T) {
	var e *errs.Error
	timeout := &blob.StoreError{Op: "GetObject", Kind: blob.KindTransient, Err: fmt.Errorf("dial tcp 10.96.0.1:9000: i/o timeout: %w", context.DeadlineExceeded)}
	if err := blob.MapStoreErr(timeout); !errors.As(err, &e) || e.Kind != errs.Unavailable || e.RetryAfter <= 0 {
		t.Fatalf("a transport timeout mapped to %v", err)
	}
	if err := blob.MapStoreErr(context.DeadlineExceeded); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the caller's own deadline mapped to %v", err)
	}
	if err := blob.MapStoreErr(context.Canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("the caller's own cancellation mapped to %v", err)
	}
}
