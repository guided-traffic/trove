package observability

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEndpointObservability(t *testing.T) {
	stack := func(t *testing.T, buf *bytes.Buffer, m *Metrics, opts ...RequestOption) http.Handler {
		t.Helper()
		lg := NewLogging(testLogCfg(), buf, WithIdentity("pod-1", "web"))
		return RequestLog(lg.Logger(), m, opts...)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			RequestInfoFrom(r.Context()).SetHost("packages.example.com")
		}))
	}
	record := func(t *testing.T, buf *bytes.Buffer) map[string]any {
		t.Helper()
		var rec map[string]any
		for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
			var r map[string]any
			if json.Unmarshal([]byte(line), &r) == nil && r["log"] == "request" {
				rec = r
			}
		}
		if rec == nil {
			t.Fatalf("no request record in %q", buf.String())
		}
		return rec
	}

	t.Run("R-EP-009 the endpoint label and log field are the listener's, bounded by the configured names", func(t *testing.T) {
		m := NewMetrics("web", WithEndpointNames("default", "customers"))
		var a, b bytes.Buffer
		serve(t, stack(t, &a, m), httptest.NewRequest("GET", "/x", nil))
		serve(t, stack(t, &b, m, WithEndpoint("customers")), httptest.NewRequest("GET", "/x", nil))
		serve(t, stack(t, &bytes.Buffer{}, m, WithEndpoint("not-configured")), httptest.NewRequest("GET", "/x", nil))

		if r := record(t, &a); r["endpoint"] != "default" || r["host"] != "packages.example.com" {
			t.Errorf("default record = %v", r)
		}
		if r := record(t, &b); r["endpoint"] != "customers" {
			t.Errorf("customers record = %v", r)
		}
		body := scrapeBody(t, m, "")
		for _, want := range []string{
			`trove_http_requests_total{code="200",endpoint="default",handler="unmatched",listener="client",method="GET"} 1`,
			`trove_http_requests_total{code="200",endpoint="customers",handler="unmatched",listener="client",method="GET"} 1`,
			`trove_http_requests_total{code="200",endpoint="_other",handler="unmatched",listener="client",method="GET"} 1`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("missing %s in\n%s", want, grepLines(body, "trove_http_requests_total"))
			}
		}
		if errs := m.Check(); len(errs) != 0 {
			t.Errorf("label guard: %v", errs)
		}
	})

	t.Run("R-EP-009 the ops listener has no endpoint", func(t *testing.T) {
		m := NewMetrics("web")
		var buf bytes.Buffer
		serve(t, stack(t, &buf, m, WithListener("ops")), httptest.NewRequest("GET", "/readyz", nil))
		if r := record(t, &buf); r["endpoint"] != nil {
			t.Errorf("ops record endpoint = %v", r["endpoint"])
		}
	})

	t.Run("R-EP-009 the endpoint families count and export at zero", func(t *testing.T) {
		m := NewMetrics("web", WithEndpointNames("customers"))
		m.ExportEndpoints()
		m.EndpointListenerUp("customers", true)
		m.EndpointMisdirected("customers")
		m.EndpointForwardedHostIgnored("customers")
		body := scrapeBody(t, m, "")
		for _, want := range []string{
			`trove_endpoint_listener_up{endpoint="customers"} 1`,
			`trove_endpoint_misdirected_total{endpoint="customers"} 1`,
			`trove_endpoint_forwarded_host_ignored_total{endpoint="customers"} 1`,
			`trove_endpoint_misdirected_total{endpoint="default"} 0`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("missing %s", want)
			}
		}
		if errs := m.Check(); len(errs) != 0 {
			t.Errorf("label guard: %v", errs)
		}
	})
}
