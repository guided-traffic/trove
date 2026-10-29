package observability

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/guided-traffic/trove/internal/config"
)

func scrapeBody(t *testing.T, m *Metrics, accept string) string {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	if accept != "" {
		r.Header.Set("Accept", accept)
	}
	w := serve(t, m.Handler(), r)
	return w.Body.String()
}

func grepLines(s, sub string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, sub) {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

func TestMetricsEndpoint(t *testing.T) {
	t.Run("R-OPS-006 /metrics exposes trove_ metrics and build info with the role", func(t *testing.T) {
		m := NewMetrics("worker")
		body := scrapeBody(t, m, "")
		if !strings.Contains(body, `trove_build_info{`) || !strings.Contains(body, `role="worker"`) {
			t.Errorf("build info missing:\n%s", grepLines(body, "build_info"))
		}
		for _, k := range []string{"version=", "revision=", "goversion="} {
			if !strings.Contains(grepLines(body, "trove_build_info{"), k) {
				t.Errorf("build info lacks %s", k)
			}
		}
		if !strings.Contains(body, "go_goroutines") || !strings.Contains(body, "process_cpu_seconds_total") {
			t.Error("runtime or process collector missing")
		}
		if strings.Contains(body, "trove_build_info{} ") {
			t.Error("empty labels")
		}
	})

	t.Run("R-OPS-006 OpenMetrics is offered on request and ends with EOF", func(t *testing.T) {
		m := NewMetrics("web")
		body := scrapeBody(t, m, "application/openmetrics-text; version=1.0.0")
		if !strings.HasSuffix(strings.TrimSpace(body), "# EOF") {
			t.Errorf("tail = %q", body[max(0, len(body)-40):])
		}
		plain := scrapeBody(t, m, "")
		if strings.Contains(plain, "# EOF") {
			t.Error("EOF in text format")
		}
	})

	t.Run("R-OPS-006 samples carry no timestamps", func(t *testing.T) {
		m := NewMetrics("web")
		for _, l := range strings.Split(scrapeBody(t, m, ""), "\n") {
			if strings.HasPrefix(l, "trove_build_info") && len(strings.Fields(l)) != 2 {
				t.Errorf("sample with extra fields: %q", l)
			}
		}
	})

	t.Run("R-OPS-006 enabled false answers 404", func(t *testing.T) {
		m := NewMetrics("web", WithMetricsConfig(config.Metrics{Enabled: false}))
		w := serve(t, m.Handler(), httptest.NewRequest(http.MethodGet, "/metrics", nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("code = %d", w.Code)
		}
	})

	t.Run("R-OPS-006 auth static requires the bearer value of the token file", func(t *testing.T) {
		dir := t.TempDir()
		f := filepath.Join(dir, "token")
		if err := os.WriteFile(f, []byte("s3cret-scrape\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		m := NewMetrics("web", WithMetricsConfig(config.Metrics{Enabled: true, Auth: "static", BearerTokenFile: f}))
		cases := []struct {
			header string
			want   int
		}{{"", 401}, {"Bearer wrong", 401}, {"Bearer s3cret-scrape", 200}, {"Basic czNjcmV0LXNjcmFwZQ==", 401}}
		for _, c := range cases {
			r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
			if c.header != "" {
				r.Header.Set("Authorization", c.header)
			}
			if got := serve(t, m.Handler(), r).Code; got != c.want {
				t.Errorf("%q: %d, want %d", c.header, got, c.want)
			}
		}
		// a rotated Secret takes effect without a restart
		if err := os.WriteFile(f, []byte("rotated"), 0o600); err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		r.Header.Set("Authorization", "Bearer rotated")
		if got := serve(t, m.Handler(), r).Code; got != 200 {
			t.Errorf("rotated token: %d", got)
		}
		// an unreadable token file refuses, never opens
		if err := os.Remove(f); err != nil {
			t.Fatal(err)
		}
		if got := serve(t, m.Handler(), r).Code; got != 401 {
			t.Errorf("missing token file: %d", got)
		}
	})
}

func TestMetricNaming(t *testing.T) {
	t.Run("R-OPS-007 every exposed trove family obeys the naming and label rules", func(t *testing.T) {
		m := NewMetrics("web")
		h := RequestLog(NewLogger(testLogCfg(), &discard{}), m)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			RequestInfoFrom(r.Context()).SetHandler("files.get")
		}))
		serve(t, h, httptest.NewRequest(http.MethodGet, "/x", nil))
		if errs := m.Check(); len(errs) != 0 {
			t.Errorf("violations: %v", errs)
		}
	})

	t.Run("R-OPS-007 the registry rejects a label outside the allowed list", func(t *testing.T) {
		m := NewMetrics("web")
		for _, tc := range []struct {
			name   string
			labels []string
		}{
			{"trove_x_total", []string{"user"}},
			{"trove_x_total", []string{"path"}},
			{"trove_y_total", []string{"trace_id"}},
			{"trove_z_total", []string{"role"}}, // role only on trove_build_info
			{"trove_w_total", []string{"ip"}},
		} {
			func() {
				defer func() {
					if recover() == nil {
						t.Errorf("%s%v accepted", tc.name, tc.labels)
					}
				}()
				m.NewCounterVec(tc.name, "h", tc.labels...)
			}()
		}
	})

	t.Run("R-OPS-007 CheckFamilies flags a family registered directly on the registry", func(t *testing.T) {
		m := NewMetrics("web")
		bad := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "trove_sneaky_total", Help: "h"}, []string{"username"})
		bad.WithLabelValues("bob").Inc()
		m.Registry().MustRegister(bad)
		other := prometheus.NewGauge(prometheus.GaugeOpts{Name: "foreign_gauge", Help: "h"})
		m.Registry().MustRegister(other)
		errs := m.Check()
		if len(errs) != 2 {
			t.Errorf("want 2 violations, got %v", errs)
		}
	})

	t.Run("R-OPS-007 names need the trove_ prefix and _total on counters only", func(t *testing.T) {
		m := NewMetrics("web")
		for _, f := range []func(){
			func() { m.NewCounterVec("other_x_total", "h") },
			func() { m.NewCounterVec("trove_x", "h") },
			func() { m.NewGaugeVec("trove_x_total", "h") },
		} {
			func() {
				defer func() {
					if recover() == nil {
						t.Error("bad name accepted")
					}
				}()
				f()
			}()
		}
	})

	t.Run("R-OPS-007 method and handler labels stay bounded", func(t *testing.T) {
		m := NewMetrics("web")
		h := RequestLog(NewLogger(testLogCfg(), &discard{}), m, WithRequestRecords(false))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			RequestInfoFrom(r.Context()).SetHandler("h-" + r.URL.Path)
		}))
		serve(t, h, httptest.NewRequest("PROPFIND", "/a", nil))
		for i := range maxHandlerLabels + 50 {
			serve(t, h, httptest.NewRequest(http.MethodGet, "/p"+strings.Repeat("x", i%7)+string(rune('a'+i%26))+string(rune('a'+i/26%26))+string(rune('a'+i/676)), nil))
		}
		body := scrapeBody(t, m, "")
		if !strings.Contains(body, `method="OTHER"`) || strings.Contains(body, "PROPFIND") {
			t.Error("non-standard method not folded")
		}
		count := strings.Count(body, "trove_http_requests_total{")
		if count > maxHandlerLabels+10 {
			t.Errorf("%d request series, handler label unbounded", count)
		}
		if !strings.Contains(body, `trove_metrics_label_overflow_total{family="trove_http_requests_total",label="handler"}`) {
			t.Error("overflow not counted")
		}
	})

	t.Run("R-OPS-007 duration histogram uses the catalog buckets and code_class", func(t *testing.T) {
		m := NewMetrics("web")
		h := RequestLog(NewLogger(testLogCfg(), &discard{}), m)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }))
		serve(t, h, httptest.NewRequest(http.MethodGet, "/x", nil))
		body := scrapeBody(t, m, "")
		for _, want := range []string{`le="0.005"`, `le="300"`, `code_class="5xx"`} {
			if !strings.Contains(body, want) {
				t.Errorf("%s missing", want)
			}
		}
	})

	t.Run("R-OPS-007 exemplars carry the trace ID of a sampled request in OpenMetrics", func(t *testing.T) {
		m := NewMetrics("web")
		tr, err := Tracing(config.Tracing{Enabled: true, SampleRatio: 1, IncomingContext: "none"}, WithSpanExporter(noopExporter{}))
		if err != nil {
			t.Fatal(err)
		}
		h := tr.Middleware()(RequestLog(NewLogger(testLogCfg(), &discard{}), m, WithRequestRecords(false))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})))
		w := serve(t, h, httptest.NewRequest(http.MethodGet, "/x", nil))
		id := w.Header().Get("X-Request-Id")
		if !strings.Contains(scrapeBody(t, m, "application/openmetrics-text; version=1.0.0"), `trace_id="`+id+`"`) {
			t.Error("exemplar missing")
		}
	})
}

type discard struct{}

func (*discard) Write(p []byte) (int, error) { return len(p), nil }

func TestCappedLabels(t *testing.T) {
	t.Run("R-OPS-007 repository cap: include patterns first, then creation order, rest is _other", func(t *testing.T) {
		m := NewMetrics("web", WithMetricsConfig(config.Metrics{
			Enabled:     true,
			LabelLimits: config.MetricsLabelLimits{Repository: 3}, RepositoryLabelInclude: []string{"npm-*", "nuget-prod"},
		}))
		m.Repos().Update([]string{"a", "b", "npm-x", "nuget-prod", "npm-y", "c"})
		got := []string{}
		for _, k := range []string{"a", "b", "npm-x", "nuget-prod", "npm-y", "c"} {
			got = append(got, m.Repos().Value("trove_repository_requests_total", k))
		}
		// limit 3: npm-x, npm-y (pattern npm-*), then nuget-prod
		exp := map[string]string{"a": "_other", "b": "_other", "npm-x": "npm-x", "nuget-prod": "nuget-prod", "npm-y": "npm-y", "c": "_other"}
		for i, k := range []string{"a", "b", "npm-x", "nuget-prod", "npm-y", "c"} {
			if got[i] != exp[k] {
				t.Errorf("%s -> %s, want %s", k, got[i], exp[k])
			}
		}
		if !strings.Contains(scrapeBody(t, m, ""), `trove_metrics_label_overflow_total{family="trove_repository_requests_total",label="repo"} 3`) {
			t.Errorf("overflow:\n%s", grepLines(scrapeBody(t, m, ""), "overflow"))
		}
	})

	t.Run("R-OPS-007 two replicas with the same snapshot choose the same keys", func(t *testing.T) {
		keys := []string{}
		for i := range 700 {
			keys = append(keys, "repo-"+string(rune('a'+i%26))+string(rune('a'+i/26%26))+string(rune('a'+i/676)))
		}
		a := NewMetrics("web")
		b := NewMetrics("web")
		a.Repos().Update(keys)
		b.Repos().Update(keys)
		others := 0
		for _, k := range keys {
			va, vb := a.Repos().Value("f", k), b.Repos().Value("f", k)
			if va != vb {
				t.Fatalf("replicas differ on %s", k)
			}
			if va == OtherLabel {
				others++
			}
		}
		if others != 200 {
			t.Errorf("%d folded, want 200 (700 keys, cap 500)", others)
		}
	})
}

func TestDBGauges(t *testing.T) {
	desc := prometheus.NewDesc("trove_jobs_queued", "Queued jobs.", []string{"kind"}, nil)

	t.Run("R-OPS-007 DB gauges are evaluated in the background and a scrape never calls the source", func(t *testing.T) {
		m := NewMetrics("web")
		calls := 0
		m.DBGauges().Add(GaugeSource{
			Name: "jobs", Descs: []*prometheus.Desc{desc},
			Collect: func(context.Context) ([]prometheus.Metric, error) {
				calls++
				return []prometheus.Metric{prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, 7, "gc")}, nil
			},
		})
		if strings.Contains(scrapeBody(t, m, ""), "trove_jobs_queued") {
			t.Error("value before the first evaluation")
		}
		m.DBGauges().Evaluate(context.Background())
		for range 20 {
			if !strings.Contains(scrapeBody(t, m, ""), `trove_jobs_queued{kind="gc"} 7`) {
				t.Fatal("value missing")
			}
		}
		if calls != 1 {
			t.Errorf("source called %d times for 20 scrapes, want 1", calls)
		}
		if errs := m.Check(); len(errs) != 0 {
			t.Error(errs)
		}
	})

	t.Run("R-OPS-007 the age gauge advances only on success and failures keep the last values", func(t *testing.T) {
		m := NewMetrics("web")
		fail := false
		m.DBGauges().Add(GaugeSource{
			Name: "jobs", Descs: []*prometheus.Desc{desc},
			Collect: func(context.Context) ([]prometheus.Metric, error) {
				if fail {
					return nil, errors.New("database down")
				}
				return []prometheus.Metric{prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, 3, "gc")}, nil
			},
		})
		age := func() string {
			return grepLines(scrapeBody(t, m, ""), "trove_metrics_db_gauges_last_success_timestamp_seconds ")
		}
		if age() != "" {
			t.Error("age present before any success")
		}
		m.DBGauges().Evaluate(context.Background())
		first := age()
		if first == "" {
			t.Fatal("age missing after success")
		}
		time.Sleep(20 * time.Millisecond)
		fail = true
		m.DBGauges().Evaluate(context.Background())
		if age() != first {
			t.Error("age advanced although the evaluation failed")
		}
		if !strings.Contains(scrapeBody(t, m, ""), `trove_jobs_queued{kind="gc"} 3`) {
			t.Error("last values dropped on failure")
		}
	})

	t.Run("R-OPS-007 a source gets a context with a deadline", func(t *testing.T) {
		m := NewMetrics("web")
		var hasDeadline bool
		m.DBGauges().Add(GaugeSource{Name: "x", Collect: func(ctx context.Context) ([]prometheus.Metric, error) {
			_, hasDeadline = ctx.Deadline()
			return nil, nil
		}})
		m.DBGauges().Evaluate(context.Background())
		if !hasDeadline {
			t.Error("no deadline")
		}
	})

	t.Run("R-OPS-007 Run evaluates at the configured interval", func(t *testing.T) {
		m := NewMetrics("web", WithMetricsConfig(config.Metrics{Enabled: true, DBGaugeInterval: time.Second}))
		m.DBGauges().interval = 20 * time.Millisecond
		n := make(chan struct{}, 100)
		m.DBGauges().Add(GaugeSource{Name: "x", Collect: func(context.Context) ([]prometheus.Metric, error) {
			n <- struct{}{}
			return nil, nil
		}})
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		m.DBGauges().Run(ctx)
		if len(n) < 3 || len(n) > 12 {
			t.Errorf("%d evaluations in 150ms at 20ms", len(n))
		}
	})
}

type noopExporter struct{}

func (noopExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error { return nil }
func (noopExporter) Shutdown(context.Context) error                             { return nil }
