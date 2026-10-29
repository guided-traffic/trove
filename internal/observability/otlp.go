package observability

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"google.golang.org/grpc/credentials"

	"github.com/guided-traffic/trove/internal/config"
)

// newOTLPExporter builds the span exporter of tracing.protocol. Unset trove keys
// fall back to the standard OTEL_EXPORTER_OTLP_* variables, which the exporters
// read themselves.
func newOTLPExporter(ctx context.Context, cfg config.Tracing) (sdktrace.SpanExporter, error) {
	var tlsCfg *tls.Config
	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("tracing: read ca_file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("tracing: ca_file %s holds no PEM certificate", cfg.CAFile)
		}
		tlsCfg = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	var headers map[string]string
	if cfg.HeadersFile != "" {
		h, err := readHeadersFile(cfg.HeadersFile)
		if err != nil {
			return nil, err
		}
		headers = h
	}
	if cfg.Protocol == "grpc" {
		var o []otlptracegrpc.Option
		if cfg.Endpoint != "" {
			o = append(o, otlptracegrpc.WithEndpointURL(cfg.Endpoint))
		}
		if tlsCfg != nil {
			o = append(o, otlptracegrpc.WithTLSCredentials(credentials.NewTLS(tlsCfg)))
		}
		if headers != nil {
			o = append(o, otlptracegrpc.WithHeaders(headers))
		}
		exp, err := otlptracegrpc.New(ctx, o...)
		if err != nil {
			return nil, fmt.Errorf("tracing: otlp grpc exporter: %w", err)
		}
		return exp, nil
	}
	var o []otlptracehttp.Option
	if cfg.Endpoint != "" {
		o = append(o, otlptracehttp.WithEndpointURL(cfg.Endpoint))
	}
	if tlsCfg != nil {
		o = append(o, otlptracehttp.WithTLSClientConfig(tlsCfg))
	}
	if headers != nil {
		o = append(o, otlptracehttp.WithHeaders(headers))
	}
	exp, err := otlptracehttp.New(ctx, o...)
	if err != nil {
		return nil, fmt.Errorf("tracing: otlp http exporter: %w", err)
	}
	return exp, nil
}

// readHeadersFile reads "Name: value" lines. The values are secrets and never
// appear in an error text.
func readHeadersFile(path string) (map[string]string, error) {
	f, err := os.Open(path) //nolint:gosec // operator-configured path
	if err != nil {
		return nil, fmt.Errorf("tracing: read headers_file: %w", err)
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("tracing: headers_file %s line %d: expected \"Name: value\"", path, n)
		}
		out[strings.TrimSpace(name)] = strings.TrimSpace(value)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("tracing: read headers_file: %w", err)
	}
	return out, nil
}

// queueProcessor hands ended spans to the exporter through a bounded queue. A
// full queue drops the span and counts it; request handling never waits for
// export (R-OPS-018).
type queueProcessor struct {
	exp     sdktrace.SpanExporter
	ch      chan sdktrace.ReadOnlySpan
	kick    chan struct{}
	flush   chan chan struct{}
	stop    chan struct{}
	done    chan struct{}
	once    sync.Once
	dropped func()
	log     *slog.Logger
	// batch is the queue length that starts an export before the next tick:
	// spans stay in ch until they are exported, so the queue size bounds every
	// span the processor holds.
	batch int
}

const (
	exportBatch   = 512
	exportDelay   = 2 * time.Second
	exportTimeout = 10 * time.Second
)

func newQueueProcessor(exp sdktrace.SpanExporter, size int, dropped func(), log *slog.Logger) *queueProcessor {
	if dropped == nil {
		dropped = func() {}
	}
	q := &queueProcessor{
		exp: exp, ch: make(chan sdktrace.ReadOnlySpan, size), kick: make(chan struct{}, 1),
		flush: make(chan chan struct{}), stop: make(chan struct{}), done: make(chan struct{}),
		dropped: dropped, log: log, batch: min(exportBatch, size),
	}
	go q.run()
	return q
}

func (q *queueProcessor) OnStart(context.Context, sdktrace.ReadWriteSpan) {}

func (q *queueProcessor) OnEnd(s sdktrace.ReadOnlySpan) {
	if !s.SpanContext().IsSampled() {
		return
	}
	select {
	case <-q.stop:
		return
	default:
	}
	select {
	case q.ch <- s:
		if len(q.ch) >= q.batch {
			select {
			case q.kick <- struct{}{}:
			default:
			}
		}
	default:
		q.dropped()
	}
}

func (q *queueProcessor) run() {
	defer close(q.done)
	batch := make([]sdktrace.ReadOnlySpan, 0, q.batch)
	tick := time.NewTicker(exportDelay)
	defer tick.Stop()
	// export takes at most one batch out of the queue and sends it. Spans wait
	// in the bounded queue, never in a second buffer, so a stuck exporter makes
	// OnEnd drop once the queue is full (R-OPS-018).
	export := func() bool {
		batch = batch[:0]
		for len(batch) < q.batch {
			select {
			case s := <-q.ch:
				batch = append(batch, s)
				continue
			default:
			}
			break
		}
		if len(batch) == 0 {
			return false
		}
		ctx, cancel := context.WithTimeout(context.Background(), exportTimeout)
		if err := q.exp.ExportSpans(ctx, batch); err != nil && q.log != nil {
			q.log.Warn("span export failed", "component", "tracing", "error", err)
		}
		cancel()
		return true
	}
	drain := func() {
		for export() {
		}
	}
	for {
		select {
		case <-q.kick:
			for len(q.ch) >= q.batch && export() {
			}
		case <-tick.C:
			drain()
		case reply := <-q.flush:
			drain()
			close(reply)
		case <-q.stop:
			drain()
			return
		}
	}
}

func (q *queueProcessor) ForceFlush(ctx context.Context) error {
	reply := make(chan struct{})
	select {
	case q.flush <- reply:
	case <-q.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-reply:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (q *queueProcessor) Shutdown(ctx context.Context) error {
	q.once.Do(func() { close(q.stop) })
	select {
	case <-q.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	return q.exp.Shutdown(ctx)
}

// scrubExporter applies the redaction rules of R-OPS-013 to the span attributes
// right before export, so no code path that sets an attribute can leak.
type scrubExporter struct {
	inner sdktrace.SpanExporter
	scrub *Scrubber
}

func (e *scrubExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	out := make([]sdktrace.ReadOnlySpan, len(spans))
	for i, s := range spans {
		out[i] = &scrubbedSpan{ReadOnlySpan: s, scrub: e.scrub}
	}
	return e.inner.ExportSpans(ctx, out)
}

func (e *scrubExporter) Shutdown(ctx context.Context) error { return e.inner.Shutdown(ctx) }

type scrubbedSpan struct {
	sdktrace.ReadOnlySpan
	scrub *Scrubber
}

func (s *scrubbedSpan) Name() string { return s.scrub.String(s.ReadOnlySpan.Name()) }

func (s *scrubbedSpan) Status() sdktrace.Status {
	st := s.ReadOnlySpan.Status()
	st.Description = s.scrub.String(st.Description)
	return st
}

func (s *scrubbedSpan) Attributes() []attribute.KeyValue {
	return scrubAttrs(s.scrub, s.ReadOnlySpan.Attributes())
}

func (s *scrubbedSpan) Events() []sdktrace.Event {
	evs := s.ReadOnlySpan.Events()
	out := make([]sdktrace.Event, len(evs))
	for i, ev := range evs {
		ev.Name = s.scrub.String(ev.Name)
		ev.Attributes = scrubAttrs(s.scrub, ev.Attributes)
		out[i] = ev
	}
	return out
}

func scrubAttrs(sc *Scrubber, in []attribute.KeyValue) []attribute.KeyValue {
	out := make([]attribute.KeyValue, len(in))
	for i, kv := range in {
		key := string(kv.Key)
		switch {
		case SensitiveKey(key):
			out[i] = attribute.String(key, RedactedValue)
		case kv.Value.Type() == attribute.STRING:
			out[i] = attribute.String(key, sc.String(kv.Value.AsString()))
		case kv.Value.Type() == attribute.STRINGSLICE:
			vs := kv.Value.AsStringSlice()
			c := make([]string, len(vs))
			for j, v := range vs {
				c[j] = sc.String(v)
			}
			out[i] = attribute.StringSlice(key, c)
		default:
			out[i] = kv
		}
	}
	return out
}
