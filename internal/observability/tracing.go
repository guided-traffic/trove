package observability

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/guided-traffic/trove/internal/config"
)

const tracerName = "github.com/guided-traffic/trove"

// TracingOption configures Tracing.
type TracingOption func(*tracingOptions)

type tracingOptions struct {
	trusted  []string
	instance string
	role     string
	version  string
	metrics  *Metrics
	log      *slog.Logger
	exporter sdktrace.SpanExporter
}

// WithTrustedProxies sets the peers (addresses or CIDRs, server.trusted_proxies)
// whose incoming traceparent is continued under incoming_context trusted_proxies.
func WithTrustedProxies(cidrs []string) TracingOption {
	return func(o *tracingOptions) { o.trusted = cidrs }
}

// WithTraceResource sets service.instance.id, trove.role and service.version.
func WithTraceResource(instance, role, version string) TracingOption {
	return func(o *tracingOptions) { o.instance, o.role, o.version = instance, role, version }
}

// WithTraceMetrics counts spans lost to a full export queue in m.
func WithTraceMetrics(m *Metrics) TracingOption { return func(o *tracingOptions) { o.metrics = m } }

// WithTraceLogger sets the logger for export errors.
func WithTraceLogger(l *slog.Logger) TracingOption { return func(o *tracingOptions) { o.log = l } }

// WithSpanExporter replaces the OTLP exporter and enables export. Tests use it.
func WithSpanExporter(e sdktrace.SpanExporter) TracingOption {
	return func(o *tracingOptions) { o.exporter = e }
}

// Tracer gives every request and job a W3C trace ID and, when tracing.enabled
// is set, exports spans over OTLP (R-OPS-018).
type Tracer struct {
	cfg      config.Tracing
	provider *sdktrace.TracerProvider
	tracer   trace.Tracer
	prop     propagation.TraceContext
	trusted  []netip.Prefix
	queue    *queueProcessor
	scrub    *Scrubber
}

// Tracing builds the tracer. Without tracing.enabled it still creates trace and
// span IDs (the request ID is the trace ID) but records and exports nothing.
func Tracing(cfg config.Tracing, opts ...TracingOption) (*Tracer, error) {
	var o tracingOptions
	for _, f := range opts {
		f(&o)
	}
	t := &Tracer{cfg: cfg, scrub: defaultScrubber}
	for _, c := range o.trusted {
		p, err := parsePrefix(c)
		if err != nil {
			return nil, fmt.Errorf("tracing: trusted proxy %q: %w", c, err)
		}
		t.trusted = append(t.trusted, p)
	}
	popts := []sdktrace.TracerProviderOption{sdktrace.WithSampler(sdktrace.NeverSample())}
	if cfg.Enabled || o.exporter != nil {
		exp := o.exporter
		if exp == nil {
			var err error
			if exp, err = newOTLPExporter(context.Background(), cfg); err != nil {
				return nil, err
			}
		}
		res, err := traceResource(o)
		if err != nil {
			return nil, err
		}
		var dropped func()
		if o.metrics != nil {
			dropped = o.metrics.spansDropped.Inc
		}
		t.queue = newQueueProcessor(&scrubExporter{inner: exp, scrub: t.scrub}, max(cfg.QueueSize, 1), dropped, o.log)
		popts = []sdktrace.TracerProviderOption{
			sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRatio))),
			sdktrace.WithResource(res),
			sdktrace.WithSpanProcessor(t.queue),
		}
	}
	t.provider = sdktrace.NewTracerProvider(popts...)
	t.tracer = t.provider.Tracer(tracerName)
	return t, nil
}

func traceResource(o tracingOptions) (*resource.Resource, error) {
	attrs := []attribute.KeyValue{attribute.String("service.name", "trove")}
	if o.version != "" {
		attrs = append(attrs, attribute.String("service.version", o.version))
	}
	if o.instance != "" {
		attrs = append(attrs, attribute.String("service.instance.id", o.instance))
	}
	if o.role != "" {
		attrs = append(attrs, attribute.String("trove.role", o.role))
	}
	// OTEL_RESOURCE_ATTRIBUTES carries the Kubernetes attributes of the downward API.
	res, err := resource.New(context.Background(), resource.WithFromEnv(), resource.WithAttributes(attrs...))
	if err != nil {
		return nil, fmt.Errorf("tracing: resource: %w", err)
	}
	return res, nil
}

func parsePrefix(s string) (netip.Prefix, error) {
	if p, err := netip.ParsePrefix(s); err == nil {
		return p.Masked(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("neither an address nor a CIDR")
	}
	return netip.PrefixFrom(a, a.BitLen()), nil
}

// Enabled reports whether spans are exported.
func (t *Tracer) Enabled() bool { return t != nil && t.queue != nil }

// Shutdown flushes the queue and stops the exporter.
func (t *Tracer) Shutdown(ctx context.Context) error {
	if t == nil {
		return nil
	}
	return t.provider.Shutdown(ctx)
}

// ForceFlush exports the queued spans now.
func (t *Tracer) ForceFlush(ctx context.Context) error { return t.provider.ForceFlush(ctx) }

// peerTrusted reports whether the TCP peer of r is in server.trusted_proxies.
func (t *Tracer) peerTrusted(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	a = a.Unmap()
	for _, p := range t.trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// continues applies tracing.incoming_context to one request.
func (t *Tracer) continues(r *http.Request) bool {
	switch t.cfg.IncomingContext {
	case "all":
		return true
	case "none":
		return false
	}
	return t.peerTrusted(r)
}

// requestIDKey carries a request ID generated by RequestLog when no tracer ran.
type requestIDKey struct{}

// RequestID returns the trace ID of the request in ctx, the value the client
// receives as X-Request-Id and the logs carry as trace_id. It is empty outside
// a request or job.
func RequestID(ctx context.Context) string {
	if sc := trace.SpanContextFromContext(ctx); sc.HasTraceID() {
		return sc.TraceID().String()
	}
	if id, ok := ctx.Value(requestIDKey{}).(string); ok {
		return id
	}
	return ""
}

// SpanID returns the span ID of the span in ctx, or "".
func SpanID(ctx context.Context) string {
	if sc := trace.SpanContextFromContext(ctx); sc.HasSpanID() {
		return sc.SpanID().String()
	}
	return ""
}

// Sampled reports whether the span in ctx is sampled.
func Sampled(ctx context.Context) bool {
	return trace.SpanContextFromContext(ctx).IsSampled()
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Middleware starts the server span of each request, keeps or replaces the
// incoming trace context according to tracing.incoming_context, and sends the
// trace ID as X-Request-Id. A malformed traceparent is ignored.
func (t *Tracer) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			ctx, ri := WithRequestInfo(ctx)
			var startOpts []trace.SpanStartOption
			startOpts = append(startOpts, trace.WithSpanKind(trace.SpanKindServer))
			if r.Header.Get("Traceparent") != "" {
				extracted := t.prop.Extract(context.Background(), propagation.HeaderCarrier(r.Header))
				if sc := trace.SpanContextFromContext(extracted); sc.IsValid() {
					if t.continues(r) {
						ctx = trace.ContextWithRemoteSpanContext(ctx, sc)
					} else {
						startOpts = append(startOpts, trace.WithLinks(trace.Link{SpanContext: sc}))
					}
				}
			}
			startOpts = append(startOpts, trace.WithAttributes(
				attribute.String("http.request.method", r.Method),
				attribute.String("url.path", r.URL.Path),
				attribute.String("user_agent.original", t.scrub.String(r.UserAgent())),
				attribute.String("network.peer.address", peerHost(r.RemoteAddr)),
			))
			ctx, span := t.tracer.Start(ctx, r.Method, startOpts...)
			defer span.End()
			w.Header().Set("X-Request-Id", span.SpanContext().TraceID().String())
			rec := recorderFor(w)
			next.ServeHTTP(rec, r.WithContext(ctx))

			status := rec.statusOrOK()
			facts := ri.get()
			route := facts.handler
			if route == "" {
				route = "unmatched"
			}
			span.SetName(r.Method + " " + route)
			span.SetAttributes(attribute.String("http.route", route), attribute.Int("http.response.status_code", status))
			if facts.repo != "" {
				span.SetAttributes(attribute.String("trove.repository.key", facts.repo))
			}
			if facts.format != "" {
				span.SetAttributes(attribute.String("trove.repository.format", facts.format))
			}
			if facts.principalType != "" {
				span.SetAttributes(attribute.String("trove.principal.type", facts.principalType))
			}
			if status >= 500 {
				span.SetStatus(codes.Error, strconv.Itoa(status))
			}
		})
	}
}

func peerHost(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

// Start starts a span for work that is not a request: a job, a backup run, a
// database call, an object-store call. Attribute values are scrubbed on export.
func (t *Tracer) Start(ctx context.Context, name string, kind trace.SpanKind, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return t.tracer.Start(ctx, name, trace.WithSpanKind(kind), trace.WithAttributes(attrs...))
}

// StartClient starts the client span of an outbound request and returns a
// function that ends it with the outcome. The URL is scrubbed and carries no
// query string.
func (t *Tracer) StartClient(ctx context.Context, method string, u *url.URL) (context.Context, func(status int, err error)) {
	attrs := []attribute.KeyValue{attribute.String("http.request.method", method)}
	if u != nil {
		c := *u
		c.User, c.RawQuery, c.Fragment = nil, "", ""
		attrs = append(attrs, attribute.String("url.full", c.String()), attribute.String("server.address", u.Hostname()))
	}
	ctx, span := t.tracer.Start(ctx, method, trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attrs...))
	return ctx, func(status int, err error) {
		if status > 0 {
			span.SetAttributes(attribute.Int("http.response.status_code", status))
		}
		if err != nil {
			span.RecordError(fmt.Errorf("%s", t.scrub.Error(err)))
			span.SetStatus(codes.Error, "request failed")
		} else if status >= 500 {
			span.SetStatus(codes.Error, strconv.Itoa(status))
		}
		span.End()
	}
}

// Inject writes the traceparent of ctx into h when tracing.propagate_to_upstreams
// is set. Without it, remote upstreams and webhook receivers never see the
// trace ID of an internal request.
func (t *Tracer) Inject(ctx context.Context, h http.Header) {
	if t == nil || !t.cfg.PropagateToUpstreams {
		return
	}
	t.prop.Inject(ctx, propagation.HeaderCarrier(h))
}
