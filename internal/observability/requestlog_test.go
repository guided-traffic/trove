package observability

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/config"
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
		if !strings.Contains(scrapeBody(t, m, ""), `trove_http_requests_total{code="200",endpoint="",handler="unmatched",listener="ops",method="GET"} 1`) {
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

// schemaDefault returns the text of a key's default from internal/config.
func schemaDefault(t *testing.T, path string) string {
	t.Helper()
	for _, k := range config.MustDescribe().Keys {
		if k.Path == path {
			return k.Default
		}
	}
	t.Fatalf("no key %s", path)
	return ""
}

func TestRequestLogBounds(t *testing.T) {
	t.Run("R-OPS-010 user_agent and path are cut at their limits and say so", func(t *testing.T) {
		var buf bytes.Buffer
		lg := NewLogging(testLogCfg(), &buf)
		h := RequestLog(lg.Logger(), NewMetrics("web"))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		req := httptest.NewRequest(http.MethodGet, "/x/"+strings.Repeat("p", 10_000)+"?token=SECRETVAL", nil)
		req.Header.Set("User-Agent", "npm/10 "+strings.Repeat("é", 2000))
		serve(t, h, req)
		rec := decode(t, lines(&buf)[0])
		ua, path := rec["user_agent"].(string), rec["path"].(string)
		if len(ua) > MaxUserAgent+len(TruncatedMarker) || !strings.HasSuffix(ua, TruncatedMarker) || !strings.HasPrefix(ua, "npm/10 é") || !utf8.ValidString(ua) {
			t.Errorf("user_agent: %d bytes, %q…", len(ua), ua[:min(40, len(ua))])
		}
		if len(path) > MaxPath+len(TruncatedMarker) || !strings.HasSuffix(path, TruncatedMarker) || !strings.HasPrefix(path, "/x/ppp") {
			t.Errorf("path: %d bytes", len(path))
		}
		short := httptest.NewRequest(http.MethodGet, "/short?token=SECRETVAL", nil)
		short.Header.Set("User-Agent", "pip/24")
		buf.Reset()
		serve(t, h, short)
		rec = decode(t, lines(&buf)[0])
		if rec["user_agent"] != "pip/24" || strings.Contains(rec["path"].(string), "SECRETVAL") || strings.Contains(rec["path"].(string), TruncatedMarker) {
			t.Errorf("short fields changed: %v", rec)
		}
	})

	t.Run("R-OPS-010 a secret near the end of a long field is scrubbed before the cut", func(t *testing.T) {
		got := clipField(strings.Repeat("a", 500)+" https://bob:hunter2hunter2@up.example/", MaxUserAgent, defaultScrubber)
		if strings.Contains(got, "hunter2") || strings.Contains(got, "bob:hunt") {
			t.Errorf("fragment of the secret kept: %q", got[480:])
		}
	})

	t.Run("R-OPS-009 R-OPS-010 5000 requests with a 60 KiB 0xFF User-Agent keep the ring under its byte bound", func(t *testing.T) {
		maxBytes, err := config.ParseByteSize(schemaDefault(t, "log.ring_buffer_bytes"))
		if err != nil {
			t.Fatal(err)
		}
		cfg := testLogCfg()
		cfg.RingBufferRecords, cfg.RingBufferBytes = 5000, maxBytes
		lg := NewLogging(cfg, io.Discard)
		h := RequestLog(lg.Logger(), NewMetrics("web"))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		ua := string(bytes.Repeat([]byte{0xFF}, 60<<10))
		for range 5000 {
			req := httptest.NewRequest(http.MethodGet, "/x", nil)
			req.Header.Set("User-Agent", ua)
			serve(t, h, req)
		}
		ring := lg.Ring()
		if ring.Bytes() > int64(maxBytes) || ring.Len() == 0 {
			t.Fatalf("ring holds %d bytes in %d records, bound %d", ring.Bytes(), ring.Len(), maxBytes)
		}
		var total int64
		for _, r := range ring.Records() {
			total += int64(len(r))
			if rec := decode(t, r); len(rec["user_agent"].(string)) > MaxUserAgent+len(TruncatedMarker) {
				t.Fatalf("user_agent of %d bytes in the ring", len(rec["user_agent"].(string)))
			}
		}
		if total > int64(maxBytes) {
			t.Fatalf("records sum to %d bytes, bound %d", total, maxBytes)
		}
	})
}

func TestRingBounds(t *testing.T) {
	t.Run("R-OPS-009 the ring evicts the oldest records when the byte bound is reached", func(t *testing.T) {
		r := NewRing(100, 100)
		for i := range 10 {
			_, _ = fmt.Fprintf(r, "{\"n\":%d,\"pad\":\"%s\"}\n", i, strings.Repeat("x", 10))
		}
		if r.Bytes() > 100 || r.Len() != 3 {
			t.Fatalf("%d bytes in %d records", r.Bytes(), r.Len())
		}
		if got := r.Records(); decode(t, got[0])["n"] != float64(7) || decode(t, got[2])["n"] != float64(9) {
			t.Errorf("kept %v", got)
		}
	})

	t.Run("R-OPS-009 an oversized record is kept as a valid JSON stub that names its size", func(t *testing.T) {
		r := NewRing(10, 1<<20)
		big := `{"msg":"huge","detail":"` + strings.Repeat("\x01", 100_000) + `"}` + "\n"
		_, _ = r.Write([]byte(big))
		recs := r.Records()
		if len(recs) != 1 || len(recs[0]) > MaxRingRecord {
			t.Fatalf("%d records, first %d bytes", len(recs), len(recs[0]))
		}
		stub := decode(t, recs[0])
		if stub["ring_truncated"] != true || stub["original_bytes"] != float64(len(big)) || !strings.HasPrefix(stub["head"].(string), `{"msg":"huge"`) {
			t.Errorf("stub = %v", stub)
		}
	})

	t.Run("R-OPS-009 ring_buffer_bytes 0 keeps nothing", func(t *testing.T) {
		r := NewRing(10, 0)
		_, _ = r.Write([]byte("{}\n"))
		if r.Len() != 0 {
			t.Error("kept a record")
		}
	})
}
