package observability

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/guided-traffic/trove/internal/config"
)

const (
	incomingTrace = "4bf92f3577b34da6a3ce929d0e0e4736"
	incomingTP    = "00-" + incomingTrace + "-00f067aa0ba902b7-01"
)

func tracedStack(t *testing.T, cfg config.Tracing, buf *bytes.Buffer, opts ...TracingOption) (http.Handler, *Tracer, *tracetest.InMemoryExporter) {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	all := append([]TracingOption{WithSpanExporter(exp), WithTrustedProxies([]string{"10.0.0.0/8"})}, opts...)
	tr, err := Tracing(cfg, all...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tr.Shutdown(context.Background()) })
	lg := NewLogging(testLogCfg(), buf)
	h := tr.Middleware()(RequestLog(lg.Logger(), NewMetrics("web"))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		RequestInfoFrom(r.Context()).SetHandler("files.get")
		lg.Logger().InfoContext(r.Context(), "inside")
	})))
	return h, tr, exp
}

func reqFrom(peer, traceparent string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/files/x", nil)
	r.RemoteAddr = peer + ":4000"
	if traceparent != "" {
		r.Header.Set("Traceparent", traceparent)
	}
	return r
}

func TestTraceContext(t *testing.T) {
	cfgSampled := config.Tracing{Enabled: true, SampleRatio: 1, IncomingContext: "trusted_proxies", QueueSize: 16}

	t.Run("R-OPS-018 incoming traceparent from a trusted proxy keeps the trace ID in log and X-Request-Id", func(t *testing.T) {
		var buf bytes.Buffer
		h, _, _ := tracedStack(t, cfgSampled, &buf)
		w := serve(t, h, reqFrom("10.1.1.1", incomingTP))
		if got := w.Header().Get("X-Request-Id"); got != incomingTrace {
			t.Errorf("X-Request-Id = %q", got)
		}
		for _, l := range lines(&buf) {
			if decode(t, l)["trace_id"] != incomingTrace {
				t.Errorf("record without the trace ID: %s", l)
			}
		}
		if len(lines(&buf)) != 2 {
			t.Errorf("want app and request record, got %d", len(lines(&buf)))
		}
	})

	t.Run("R-OPS-018 a client outside the trusted proxies gets a new trace and a span link", func(t *testing.T) {
		var buf bytes.Buffer
		h, tr, exp := tracedStack(t, cfgSampled, &buf)
		w := serve(t, h, reqFrom("203.0.113.9", incomingTP))
		id := w.Header().Get("X-Request-Id")
		if id == incomingTrace || len(id) != 32 {
			t.Errorf("forged trace ID adopted: %q", id)
		}
		if err := tr.ForceFlush(context.Background()); err != nil {
			t.Fatal(err)
		}
		spans := exp.GetSpans()
		if len(spans) != 1 || len(spans[0].Links) != 1 || spans[0].Links[0].SpanContext.TraceID().String() != incomingTrace {
			t.Fatalf("spans = %+v", spans)
		}
	})

	t.Run("R-OPS-018 incoming_context all continues from every client, none from nobody", func(t *testing.T) {
		var buf bytes.Buffer
		all := cfgSampled
		all.IncomingContext = "all"
		h, _, _ := tracedStack(t, all, &buf)
		if got := serve(t, h, reqFrom("203.0.113.9", incomingTP)).Header().Get("X-Request-Id"); got != incomingTrace {
			t.Errorf("all: %q", got)
		}
		none := cfgSampled
		none.IncomingContext = "none"
		h, _, _ = tracedStack(t, none, &buf)
		if got := serve(t, h, reqFrom("10.1.1.1", incomingTP)).Header().Get("X-Request-Id"); got == incomingTrace {
			t.Error("none: continued")
		}
	})

	t.Run("R-OPS-018 a malformed traceparent is ignored", func(t *testing.T) {
		var buf bytes.Buffer
		h, _, exp := tracedStack(t, cfgSampled, &buf)
		for _, bad := range []string{"garbage", "00-xyz-00f067aa0ba902b7-01", "00-00000000000000000000000000000000-00f067aa0ba902b7-01", "ff-" + incomingTrace + "-00f067aa0ba902b7-01"} {
			w := serve(t, h, reqFrom("10.1.1.1", bad))
			id := w.Header().Get("X-Request-Id")
			if len(id) != 32 || id == incomingTrace {
				t.Errorf("%q: id %q", bad, id)
			}
		}
		_ = exp
	})

	t.Run("R-OPS-018 trace IDs exist when export is disabled", func(t *testing.T) {
		tr, err := Tracing(config.Tracing{IncomingContext: "all"})
		if err != nil {
			t.Fatal(err)
		}
		if tr.Enabled() {
			t.Fatal("export enabled by default")
		}
		h := tr.Middleware()(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		if got := serve(t, h, reqFrom("1.2.3.4", "")).Header().Get("X-Request-Id"); len(got) != 32 {
			t.Errorf("no ID: %q", got)
		}
		if got := serve(t, h, reqFrom("1.2.3.4", incomingTP)).Header().Get("X-Request-Id"); got != incomingTrace {
			t.Errorf("disabled tracer must still continue the trace: %q", got)
		}
	})

	t.Run("R-OPS-018 the server span carries http.route and trove attributes", func(t *testing.T) {
		var buf bytes.Buffer
		h, tr, exp := tracedStack(t, cfgSampled, &buf)
		serve(t, h, reqFrom("10.1.1.1", ""))
		_ = tr.ForceFlush(context.Background())
		sp := exp.GetSpans()[0]
		attrs := map[string]string{}
		for _, a := range sp.Attributes {
			attrs[string(a.Key)] = a.Value.String()
		}
		if attrs["http.route"] != "files.get" || attrs["http.request.method"] != "GET" || attrs["http.response.status_code"] != "200" {
			t.Errorf("attributes = %v", attrs)
		}
		if sp.Name != "GET files.get" || sp.SpanKind != trace.SpanKindServer {
			t.Errorf("name %q kind %v", sp.Name, sp.SpanKind)
		}
	})

	t.Run("R-OPS-018 no exported attribute carries a trove token", func(t *testing.T) {
		exp := tracetest.NewInMemoryExporter()
		tr, err := Tracing(cfgSampled, WithSpanExporter(exp))
		if err != nil {
			t.Fatal(err)
		}
		_, span := tr.Start(context.Background(), "op", trace.SpanKindInternal,
			attribute.String("note", "used trv_pat_ABCDEFGHIJKLMNOPQRST here"),
			attribute.String("authorization", "Bearer xyz"))
		span.RecordError(io.ErrUnexpectedEOF)
		span.End()
		_ = tr.ForceFlush(context.Background())
		for _, s := range exp.GetSpans() {
			for _, a := range s.Attributes {
				if strings.Contains(a.Value.String(), "ABCDEFGHIJ") || a.Value.String() == "Bearer xyz" {
					t.Errorf("leak in %s", a.Key)
				}
			}
		}
		if len(exp.GetSpans()) != 1 {
			t.Fatal("span not exported")
		}
	})

	t.Run("R-OPS-018 sample_ratio 0 exports nothing", func(t *testing.T) {
		cfg := cfgSampled
		cfg.SampleRatio = 0
		var buf bytes.Buffer
		h, tr, exp := tracedStack(t, cfg, &buf)
		serve(t, h, reqFrom("10.1.1.1", ""))
		_ = tr.ForceFlush(context.Background())
		if len(exp.GetSpans()) != 0 {
			t.Error("unsampled span exported")
		}
	})
}

type blockingExporter struct {
	mu      sync.Mutex
	release chan struct{}
	n       int
}

func (b *blockingExporter) ExportSpans(_ context.Context, s []sdktrace.ReadOnlySpan) error {
	<-b.release
	b.mu.Lock()
	b.n += len(s)
	b.mu.Unlock()
	return nil
}
func (b *blockingExporter) Shutdown(context.Context) error { return nil }

func TestSpanQueue(t *testing.T) {
	t.Run("R-OPS-018 a full export queue drops spans, counts them and never blocks", func(t *testing.T) {
		m := NewMetrics("web")
		exp := &blockingExporter{release: make(chan struct{})}
		tr, err := Tracing(config.Tracing{Enabled: true, SampleRatio: 1, QueueSize: 2, IncomingContext: "none"},
			WithSpanExporter(exp), WithTraceMetrics(m))
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() {
			for range 50 {
				_, sp := tr.Start(context.Background(), "x", trace.SpanKindInternal)
				sp.End()
			}
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("span end blocked on a stuck exporter")
		}
		if !strings.Contains(scrapeBody(t, m, ""), "trove_tracing_spans_dropped_total ") ||
			strings.Contains(scrapeBody(t, m, ""), "trove_tracing_spans_dropped_total 0\n") {
			t.Errorf("drop counter not incremented:\n%s", grepLines(scrapeBody(t, m, ""), "dropped"))
		}
		close(exp.release)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tr.Shutdown(ctx); err != nil {
			t.Fatal(err)
		}
	})
}

func TestTracingConfigErrors(t *testing.T) {
	t.Run("R-OPS-018 bad ca_file and headers_file fail at start-up with a clear message", func(t *testing.T) {
		_, err := Tracing(config.Tracing{Enabled: true, Endpoint: "http://c:4318", CAFile: "/nonexistent/ca.pem", Protocol: "http/protobuf"})
		if err == nil || !strings.Contains(err.Error(), "ca_file") {
			t.Errorf("err = %v", err)
		}
		_, err = Tracing(config.Tracing{Enabled: true, Endpoint: "http://c:4318", HeadersFile: "/nonexistent/h", Protocol: "http/protobuf"})
		if err == nil || !strings.Contains(err.Error(), "headers_file") {
			t.Errorf("err = %v", err)
		}
		if _, err := Tracing(config.Tracing{}, WithTrustedProxies([]string{"nonsense"})); err == nil {
			t.Error("bad trusted proxy accepted")
		}
	})
}
