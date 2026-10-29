package observability

import (
	"crypto/subtle"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/guided-traffic/trove/internal/buildinfo"
	"github.com/guided-traffic/trove/internal/config"
)

// HTTPBuckets are the buckets of the HTTP duration histograms (R-OPS-008). The
// long tail exists because the duration includes body transfer.
func HTTPBuckets() []float64 {
	return []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 300}
}

// maxHandlerLabels bounds the distinct handler label values; a handler name
// beyond it counts as "unmatched" and increments the overflow counter.
const maxHandlerLabels = 1000

// MetricsOption configures NewMetrics.
type MetricsOption func(*metricsOptions)

type metricsOptions struct {
	cfg config.Metrics
}

// WithMetricsConfig applies the metrics section. Without it, metrics are
// enabled without authentication.
func WithMetricsConfig(cfg config.Metrics) MetricsOption {
	return func(o *metricsOptions) { o.cfg = cfg }
}

// Metrics owns the Prometheus registry of a replica and the families that
// every role exports: build info, HTTP, label overflow, tracing drops, the
// DB-gauge age.
type Metrics struct {
	role string
	cfg  config.Metrics
	reg  *prometheus.Registry

	requests     *prometheus.CounterVec
	duration     *prometheus.HistogramVec
	inFlight     *prometheus.GaugeVec
	recvBytes    *prometheus.CounterVec
	sentBytes    *prometheus.CounterVec
	cut          *prometheus.CounterVec
	ready        prometheus.Gauge
	overflow     *prometheus.CounterVec
	spansDropped prometheus.Counter

	mu       sync.Mutex
	handlers map[string]struct{}
	dbg      *DBGauges
	repos    *CappedLabels
	subs     *CappedLabels
}

// NewMetrics builds the registry with the Go runtime and process collectors and
// trove_build_info{version,revision,goversion,role} (R-OPS-006). The role and
// pool of every other series are target labels the scraper attaches (Q-OPS-13).
func NewMetrics(role string, opts ...MetricsOption) *Metrics {
	o := metricsOptions{cfg: config.Metrics{
		Enabled: true, Auth: "none", DBGaugeInterval: 30 * time.Second,
		LabelLimits: config.MetricsLabelLimits{Repository: 500, Subscription: 100},
	}}
	for _, f := range opts {
		f(&o)
	}
	m := &Metrics{role: role, cfg: o.cfg, reg: prometheus.NewRegistry(), handlers: map[string]struct{}{}}
	m.reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	bi := buildinfo.Get()
	info := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "trove_build_info", Help: "Build and process role of the running binary; the value is always 1.",
	}, []string{"version", "revision", "goversion", "role"})
	info.WithLabelValues(bi.Version, bi.Commit, bi.GoVersion, role).Set(1)
	m.reg.MustRegister(info)

	m.requests = m.NewCounterVec("trove_http_requests_total", "Completed HTTP requests.", "listener", "handler", "method", "code")
	m.duration = m.NewHistogramVec("trove_http_request_duration_seconds",
		"Time from first byte received to last byte sent.", HTTPBuckets(), "listener", "handler", "method", "code_class")
	m.inFlight = m.NewGaugeVec("trove_http_requests_in_flight", "HTTP requests in progress.", "listener")
	m.recvBytes = m.NewCounterVec("trove_http_received_bytes_total", "Request body bytes.", "handler")
	m.sentBytes = m.NewCounterVec("trove_http_sent_bytes_total", "Response body bytes.", "handler")
	m.cut = m.NewCounterVec("trove_http_transfers_cut_total", "Transfers cut at the shutdown deadline.", "direction")
	m.ready = prometheus.NewGauge(prometheus.GaugeOpts{Name: "trove_ready", Help: "1 while /readyz answers 200, degraded included."})
	m.overflow = m.NewCounterVec("trove_metrics_label_overflow_total", "Label values folded into _other.", "family", "label")
	m.spansDropped = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "trove_tracing_spans_dropped_total", Help: "Spans lost to a full export queue.",
	})
	m.reg.MustRegister(m.ready, m.spansDropped)
	m.dbg = newDBGauges(m)
	m.reg.MustRegister(m.dbg)
	m.repos = newCappedLabels(m, "repo", o.cfg.LabelLimits.Repository, o.cfg.RepositoryLabelInclude)
	m.subs = newCappedLabels(m, "subscription", o.cfg.LabelLimits.Subscription, nil)
	return m
}

// Role returns the process role the metrics were built for.
func (m *Metrics) Role() string { return m.role }

// Registry returns the registry. Other packages register their collectors on
// it; CheckFamilies over its Gather output enforces R-OPS-007.
func (m *Metrics) Registry() *prometheus.Registry { return m.reg }

// Check returns the violations of the naming and label rules among the
// families the registry exposes now.
func (m *Metrics) Check() []error {
	fams, err := m.reg.Gather()
	if err != nil {
		return []error{err}
	}
	return CheckFamilies(fams)
}

// NewCounterVec registers a counter family under the rules of R-OPS-007. A
// violation (name without trove_ prefix or _total, label outside the allowed
// list) is a programming error and panics at start-up.
func (m *Metrics) NewCounterVec(name, help string, labels ...string) *prometheus.CounterVec {
	must(checkName(name, true), checkLabels(name, labels))
	v := prometheus.NewCounterVec(prometheus.CounterOpts{Name: name, Help: help}, labels)
	m.reg.MustRegister(v)
	return v
}

// NewGaugeVec registers a gauge family under the rules of R-OPS-007.
func (m *Metrics) NewGaugeVec(name, help string, labels ...string) *prometheus.GaugeVec {
	must(checkName(name, false), checkLabels(name, labels))
	v := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: name, Help: help}, labels)
	m.reg.MustRegister(v)
	return v
}

// NewHistogramVec registers a histogram family under the rules of R-OPS-007.
// With metrics.native_histograms it also exposes native histograms.
func (m *Metrics) NewHistogramVec(name, help string, buckets []float64, labels ...string) *prometheus.HistogramVec {
	must(checkName(name, false), checkLabels(name, labels))
	opts := prometheus.HistogramOpts{Name: name, Help: help, Buckets: buckets}
	if m.cfg.NativeHistograms {
		opts.NativeHistogramBucketFactor = 1.1
		opts.NativeHistogramMaxBucketNumber = 100
	}
	v := prometheus.NewHistogramVec(opts, labels)
	m.reg.MustRegister(v)
	return v
}

func must(errs ...error) {
	for _, err := range errs {
		if err != nil {
			panic(err)
		}
	}
}

// SetReady sets trove_ready.
func (m *Metrics) SetReady(ready bool) {
	if ready {
		m.ready.Set(1)
	} else {
		m.ready.Set(0)
	}
}

// TransferCut counts a transfer cut at the shutdown deadline; direction is
// "upload" or "download".
func (m *Metrics) TransferCut(direction string) { m.cut.WithLabelValues(direction).Inc() }

// DBGauges returns the background evaluator of the gauges read from PostgreSQL.
func (m *Metrics) DBGauges() *DBGauges { return m.dbg }

// Repos returns the capped repository label (Q-OPS-10).
func (m *Metrics) Repos() *CappedLabels { return m.repos }

// Subscriptions returns the capped webhook subscription label.
func (m *Metrics) Subscriptions() *CappedLabels { return m.subs }

// Handler serves GET /metrics: Prometheus text format 0.0.4, or OpenMetrics when
// the Accept header asks for it. With metrics.enabled false it answers 404; with
// auth static it requires the bearer value of metrics.bearer_token_file, read
// on every scrape so a rotated Secret takes effect without a restart. A scrape
// reads the registry only; it touches neither PostgreSQL nor Valkey (Q-OPS-18).
func (m *Metrics) Handler() http.Handler {
	inner := promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{EnableOpenMetrics: true, ErrorHandling: promhttp.ContinueOnError})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !m.cfg.Enabled {
			http.NotFound(w, r)
			return
		}
		if m.cfg.Auth == "static" && !m.authorized(r) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="trove metrics"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		inner.ServeHTTP(w, r)
	})
}

func (m *Metrics) authorized(r *http.Request) bool {
	want, err := os.ReadFile(m.cfg.BearerTokenFile)
	if err != nil {
		return false
	}
	token := strings.TrimSpace(string(want))
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}

// handlerLabel bounds the handler label: an empty name is "unmatched", and
// names beyond maxHandlerLabels fold into "unmatched" too.
func (m *Metrics) handlerLabel(name string) string {
	if name == "" {
		return "unmatched"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.handlers[name]; ok {
		return name
	}
	if len(m.handlers) >= maxHandlerLabels {
		m.overflow.WithLabelValues("trove_http_requests_total", "handler").Inc()
		return "unmatched"
	}
	m.handlers[name] = struct{}{}
	return name
}

// methodLabel maps anything but the standard methods to OTHER.
func methodLabel(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodOptions, http.MethodConnect, http.MethodTrace:
		return method
	}
	return "OTHER"
}

func codeClass(status int) string {
	if status < 100 || status > 599 {
		return "5xx"
	}
	return strconv.Itoa(status/100) + "xx"
}

// observeRequest records one completed request.
func (m *Metrics) observeRequest(listener, handler, method string, status int, d time.Duration, recv, sent int64, traceID string, sampled bool) {
	if status < 100 || status > 599 {
		status = 500
	}
	h := m.handlerLabel(handler)
	me := methodLabel(method)
	m.requests.WithLabelValues(listener, h, me, strconv.Itoa(status)).Inc()
	obs := m.duration.WithLabelValues(listener, h, me, codeClass(status))
	if eo, ok := obs.(prometheus.ExemplarObserver); ok && sampled && traceID != "" {
		eo.ObserveWithExemplar(d.Seconds(), prometheus.Labels{"trace_id": traceID})
	} else {
		obs.Observe(d.Seconds())
	}
	if recv > 0 {
		m.recvBytes.WithLabelValues(h).Add(float64(recv))
	}
	if sent > 0 {
		m.sentBytes.WithLabelValues(h).Add(float64(sent))
	}
}
