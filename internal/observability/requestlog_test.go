package observability

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func serve(t *testing.T, h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestRequestLog(t *testing.T) {
	newStack := func(t *testing.T, buf *bytes.Buffer, h http.Handler, opts ...RequestOption) (http.Handler, *Metrics) {
		t.Helper()
		lg := NewLogging(testLogCfg(), buf, WithIdentity("pod-1", "web"))
		m := NewMetrics("web")
		return RequestLog(lg.Logger(), m, opts...)(h), m
	}

	t.Run("R-OPS-010 one record per request with the fixed field set", func(t *testing.T) {
		var buf bytes.Buffer
		h, _ := newStack(t, &buf, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ri := RequestInfoFrom(r.Context())
			ri.SetHandler("npm.packument")
			ri.SetRepo("npm-all", "npm")
			ri.SetPrincipal("alice", "user", "u-1")
			ri.SetToken("tok-9", "bearer")
			ri.SetCache("hit")
			_, _ = w.Write([]byte("hello"))
		}))
		req := httptest.NewRequest(http.MethodGet, "/api/npm/npm-all/express?token=SECRETVAL", strings.NewReader("12345"))
		req.RemoteAddr = "10.1.2.3:5555"
		req.Header.Set("User-Agent", "npm/10")
		w := serve(t, h, req)

		ls := lines(&buf)
		if len(ls) != 1 {
			t.Fatalf("want 1 record, got %d: %v", len(ls), ls)
		}
		rec := decode(t, ls[0])
		want := []string{
			"time", "trace_id", "listener", "remote_addr", "peer_addr", "method", "path", "handler", "status",
			"request_bytes", "response_bytes", "duration_ms", "user_agent", "principal", "principal_type", "principal_id", "token_id",
			"auth_method", "repo", "format", "cache", "deny_reason", "limiter", "limit_mode", "log", "level", "replica", "role",
		}
		for _, k := range want {
			if _, ok := rec[k]; !ok {
				t.Errorf("field %q missing", k)
			}
		}
		check := map[string]any{
			"log": "request", "level": "INFO", "listener": "client", "method": "GET", "handler": "npm.packument",
			"status": float64(200), "response_bytes": float64(5), "user_agent": "npm/10", "principal": "alice",
			"principal_type": "user", "principal_id": "u-1", "token_id": "tok-9", "auth_method": "bearer",
			"repo": "npm-all", "format": "npm", "cache": "hit", "peer_addr": "10.1.2.3", "remote_addr": "10.1.2.3",
			"deny_reason": nil, "limiter": nil, "limit_mode": nil,
		}
		for k, v := range check {
			if rec[k] != v {
				t.Errorf("%s = %#v, want %#v", k, rec[k], v)
			}
		}
		if p := rec["path"].(string); !strings.HasPrefix(p, "/api/npm/npm-all/express?") || strings.Contains(p, "SECRETVAL") {
			t.Errorf("path = %q", p)
		}
		if rec["trace_id"] != w.Header().Get("X-Request-Id") || len(rec["trace_id"].(string)) != 32 {
			t.Errorf("trace_id %v vs X-Request-Id %q", rec["trace_id"], w.Header().Get("X-Request-Id"))
		}
		if rec["duration_ms"].(float64) < 0 {
			t.Error("negative duration")
		}
	})

	t.Run("R-OPS-010 anonymous defaults and unknown request length", func(t *testing.T) {
		var buf bytes.Buffer
		h, _ := newStack(t, &buf, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) }))
		req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("body"))
		req.ContentLength = -1
		serve(t, h, req)
		rec := decode(t, lines(&buf)[0])
		if rec["principal"] != "anonymous" || rec["principal_type"] != "anonymous" || rec["auth_method"] != "none" ||
			rec["principal_id"] != nil || rec["token_id"] != nil || rec["handler"] != "unmatched" {
			t.Errorf("defaults: %v", rec)
		}
		if rec["request_bytes"] != float64(-1) || rec["status"] != float64(404) {
			t.Errorf("bytes/status: %v", rec)
		}
	})

	t.Run("R-OPS-010 request body bytes are counted when the handler reads them", func(t *testing.T) {
		var buf bytes.Buffer
		h, _ := newStack(t, &buf, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			b := make([]byte, 64)
			_, _ = r.Body.Read(b)
		}))
		serve(t, h, httptest.NewRequest(http.MethodPut, "/x", strings.NewReader("abcdef")))
		if got := decode(t, lines(&buf)[0])["request_bytes"]; got != float64(6) {
			t.Errorf("request_bytes = %v", got)
		}
	})

	t.Run("R-OPS-010 a token in the path or user agent never reaches the record", func(t *testing.T) {
		var buf bytes.Buffer
		h, _ := newStack(t, &buf, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		req := httptest.NewRequest(http.MethodGet, "/f/trv_pat_ABCDEFGHIJKLMNOPQRST/x", nil)
		serve(t, h, req)
		if strings.Contains(buf.String(), "ABCDEFGHIJ") {
			t.Errorf("token leaked: %s", buf.String())
		}
	})

	t.Run("R-OPS-010 records can be switched off while metrics still count", func(t *testing.T) {
		var buf bytes.Buffer
		h, m := newStack(t, &buf, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), WithRequestRecords(false), WithListener("ops"))
		serve(t, h, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if buf.Len() != 0 {
			t.Errorf("record written: %s", buf.String())
		}
		if !strings.Contains(scrapeBody(t, m, ""), `trove_http_requests_total{code="200",handler="unmatched",listener="ops",method="GET"} 1`) {
			t.Error("request not counted")
		}
	})

	t.Run("R-OPS-018 a request ID is made up when no tracer ran", func(t *testing.T) {
		var buf bytes.Buffer
		var inner string
		h, _ := newStack(t, &buf, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { inner = RequestID(r.Context()) }))
		w := serve(t, h, httptest.NewRequest(http.MethodGet, "/", nil))
		if inner == "" || inner != w.Header().Get("X-Request-Id") {
			t.Errorf("ctx id %q header %q", inner, w.Header().Get("X-Request-Id"))
		}
	})
}

func TestRequestInfoNilSafe(t *testing.T) {
	t.Run("R-OPS-010 setters on a missing RequestInfo are no-ops", func(t *testing.T) {
		var ri *RequestInfo
		ri.SetHandler("x")
		ri.SetPrincipal("a", "user", "1")
		if RequestInfoFrom(context.Background()) != nil {
			t.Error("unexpected info")
		}
	})
}
