package observability

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/guided-traffic/trove/internal/config"
)

type countingExporter struct {
	n     atomic.Int64
	calls atomic.Int64
	delay time.Duration
}

func (e *countingExporter) ExportSpans(_ context.Context, s []sdktrace.ReadOnlySpan) error {
	e.calls.Add(1)
	if e.delay > 0 {
		time.Sleep(e.delay)
	}
	e.n.Add(int64(len(s)))
	return nil
}
func (e *countingExporter) Shutdown(context.Context) error { return nil }

func TestSpanQueueUnderLoad(t *testing.T) {
	t.Run("R-OPS-018 CR2-5 every span is exported or counted as dropped and ForceFlush leaves the queue drained", func(t *testing.T) {
		for _, size := range []int{1, 3, 600, 2048} {
			m := NewMetrics("web")
			exp := &countingExporter{delay: 200 * time.Microsecond}
			var dropped atomic.Int64
			tr, err := Tracing(config.Tracing{Enabled: true, SampleRatio: 1, QueueSize: size, IncomingContext: "none"},
				WithSpanExporter(exp), WithTraceMetrics(m))
			if err != nil {
				t.Fatal(err)
			}
			tr.queue.dropped = func() { dropped.Add(1) }
			var wg sync.WaitGroup
			const per, workers = 2000, 8
			for range workers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for range per {
						_, sp := tr.Start(context.Background(), "x", trace.SpanKindInternal)
						sp.End()
					}
				}()
			}
			wg.Wait()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			if err := tr.queue.ForceFlush(ctx); err != nil {
				t.Fatalf("size %d: flush: %v", size, err)
			}
			if err := tr.Shutdown(ctx); err != nil {
				t.Fatalf("size %d: shutdown: %v", size, err)
			}
			cancel()
			if got := exp.n.Load() + dropped.Load(); got != per*workers {
				t.Errorf("size %d: exported %d + dropped %d = %d, want %d", size, exp.n.Load(), dropped.Load(), got, per*workers)
			}
		}
	})

	t.Run("R-OPS-018 CR2-5 a tick under a steady span rate exports what was queued, not an endless run of tiny batches", func(t *testing.T) {
		exp := &countingExporter{delay: 2 * time.Millisecond}
		tr, err := Tracing(config.Tracing{Enabled: true, SampleRatio: 1, QueueSize: 2048, IncomingContext: "none"},
			WithSpanExporter(exp), WithTraceMetrics(NewMetrics("web")))
		if err != nil {
			t.Fatal(err)
		}
		stop := make(chan struct{})
		go func() {
			tk := time.NewTicker(200 * time.Microsecond) // about 5000 spans/s
			defer tk.Stop()
			for {
				select {
				case <-stop:
					return
				case <-tk.C:
					_, sp := tr.Start(context.Background(), "x", trace.SpanKindInternal)
					sp.End()
				}
			}
		}()
		time.Sleep(exportDelay + 2*time.Second)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		ferr := tr.queue.ForceFlush(ctx)
		cancel()
		close(stop)
		calls := exp.calls.Load()
		sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scancel()
		_ = tr.Shutdown(sctx)
		if ferr != nil {
			t.Errorf("ForceFlush under load: %v", ferr)
		}
		if calls > 100 {
			t.Errorf("%d export calls in about 4 s: the tick drain does not end under load", calls)
		}
	})
}
