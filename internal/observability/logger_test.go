package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/config"
)

type stringerVal string

func (s stringerVal) String() string { return string(s) }

type nilError struct{}

func (*nilError) Error() string { panic("Error called on a nil receiver") }

func testLogCfg() config.Log {
	return config.Log{
		Format: "json", Level: "info", RingBufferRecords: 3, RingBufferBytes: config.MiB, OverrideMaxDuration: time.Hour,
		Request: config.LogRequest{RedactQueryParams: DefaultQueryParams()},
	}
}

func decode(t *testing.T, line string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("not JSON: %q: %v", line, err)
	}
	return m
}

func lines(buf *bytes.Buffer) []string {
	s := strings.TrimSpace(buf.String())
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func TestLogRecord(t *testing.T) {
	t.Run("R-OPS-009 record carries time level msg log replica role as one JSON object per line", func(t *testing.T) {
		var buf bytes.Buffer
		log := NewLogger(testLogCfg(), &buf, WithIdentity("trove-web-0", "web"))
		log.With("component", "http").Info("hello", "n", 3)
		ls := lines(&buf)
		if len(ls) != 1 {
			t.Fatalf("want one line, got %d", len(ls))
		}
		rec := decode(t, ls[0])
		for _, k := range []string{"time", "level", "msg", "log", "component", "replica", "role"} {
			if _, ok := rec[k]; !ok {
				t.Errorf("missing field %q in %v", k, rec)
			}
		}
		if rec["level"] != "INFO" || rec["log"] != "app" || rec["replica"] != "trove-web-0" || rec["role"] != "web" || rec["component"] != "http" {
			t.Errorf("unexpected values: %v", rec)
		}
	})

	t.Run("R-OPS-009 time is RFC 3339 UTC with millisecond precision", func(t *testing.T) {
		var buf bytes.Buffer
		NewLogger(testLogCfg(), &buf).Info("x")
		ts := decode(t, lines(&buf)[0])["time"].(string)
		parsed, err := time.Parse("2006-01-02T15:04:05.000Z07:00", ts)
		if err != nil || !strings.HasSuffix(ts, "Z") || parsed.IsZero() {
			t.Errorf("time %q is not RFC 3339 UTC with milliseconds: %v", ts, err)
		}
	})

	t.Run("R-OPS-009 add_source renders source as file:line", func(t *testing.T) {
		var buf bytes.Buffer
		cfg := testLogCfg()
		cfg.AddSource = true
		NewLogger(cfg, &buf).Info("x")
		src, _ := decode(t, lines(&buf)[0])["source"].(string)
		if !strings.Contains(src, "logger_test.go:") {
			t.Errorf("source = %q", src)
		}
	})

	t.Run("R-OPS-009 a stream override replaces the log field instead of duplicating it", func(t *testing.T) {
		var buf bytes.Buffer
		lg := NewLogging(testLogCfg(), &buf)
		lg.Logger().With("log", StreamAudit).Info("x")
		line := lines(&buf)[0]
		if strings.Count(line, `"log"`) != 1 || decode(t, line)["log"] != "audit" {
			t.Errorf("record = %s", line)
		}
	})

	t.Run("R-OPS-009 text format keeps the field names", func(t *testing.T) {
		var buf bytes.Buffer
		cfg := testLogCfg()
		cfg.Format = "text"
		NewLogger(cfg, &buf, WithIdentity("r", "worker")).Info("x")
		out := buf.String()
		for _, k := range []string{"level=INFO", "msg=x", "log=app", "replica=r", "role=worker"} {
			if !strings.Contains(out, k) {
				t.Errorf("%q missing in %s", k, out)
			}
		}
	})

	t.Run("R-OPS-009 ring buffer keeps the last records after redaction", func(t *testing.T) {
		var buf bytes.Buffer
		lg := NewLogging(testLogCfg(), &buf)
		for _, m := range []string{"a", "b", "c", "d"} {
			lg.Logger().Info(m, "password", "hunter2")
		}
		rec := lg.Ring().Records()
		if len(rec) != 3 {
			t.Fatalf("ring holds %d records, want 3", len(rec))
		}
		if decode(t, rec[0])["msg"] != "b" || decode(t, rec[2])["msg"] != "d" {
			t.Errorf("order wrong: %v", rec)
		}
		if strings.Contains(strings.Join(rec, ""), "hunter2") {
			t.Error("ring holds an unredacted secret")
		}
	})

	t.Run("R-OPS-009 trace_id and span_id come from the context", func(t *testing.T) {
		var buf bytes.Buffer
		tr, err := Tracing(config.Tracing{})
		if err != nil {
			t.Fatal(err)
		}
		ctx, span := tr.Start(context.Background(), "job", 0)
		defer span.End()
		NewLogger(testLogCfg(), &buf).InfoContext(ctx, "x")
		rec := decode(t, lines(&buf)[0])
		if rec["trace_id"] != RequestID(ctx) || rec["span_id"] != SpanID(ctx) || rec["trace_id"] == "" {
			t.Errorf("record = %v", rec)
		}
	})
}

func TestComponentLevels(t *testing.T) {
	t.Run("R-OPS-012 per-component level from log.levels", func(t *testing.T) {
		var buf bytes.Buffer
		cfg := testLogCfg()
		cfg.Levels = map[string]string{"proxy": "debug", "auth": "error"}
		lg := NewLogging(cfg, &buf)
		lg.Component("proxy").Debug("p")
		lg.Component("auth").Warn("a")
		lg.Component("api").Debug("d")
		lg.Component("api").Info("i")
		got := lines(&buf)
		if len(got) != 2 || decode(t, got[0])["msg"] != "p" || decode(t, got[1])["msg"] != "i" {
			t.Errorf("records = %v", got)
		}
	})

	t.Run("R-OPS-012 override changes the level without a restart and expires", func(t *testing.T) {
		var buf bytes.Buffer
		lg := NewLogging(testLogCfg(), &buf)
		proxy := lg.Component("proxy")
		proxy.Debug("before")
		if _, err := lg.SetOverride(map[string]string{"proxy": "debug"}, time.Minute); err != nil {
			t.Fatal(err)
		}
		proxy.Debug("during")
		lg.ClearOverride()
		proxy.Debug("after")
		got := lines(&buf)
		if len(got) != 1 || decode(t, got[0])["msg"] != "during" {
			t.Errorf("records = %v", got)
		}
	})

	t.Run("R-OPS-012 override rejects unknown component, level and duration", func(t *testing.T) {
		lg := NewLogging(testLogCfg(), &bytes.Buffer{})
		if _, err := lg.SetOverride(map[string]string{"nope": "debug"}, time.Minute); err == nil {
			t.Error("unknown component accepted")
		}
		if _, err := lg.SetOverride(map[string]string{"proxy": "loud"}, time.Minute); err == nil {
			t.Error("unknown level accepted")
		}
		if _, err := lg.SetOverride(map[string]string{"proxy": "debug"}, time.Second); err == nil {
			t.Error("duration below 1m accepted")
		}
		if _, err := lg.SetOverride(map[string]string{"proxy": "debug"}, 2*time.Hour); err == nil {
			t.Error("duration above override_max_duration accepted")
		}
	})
}

func TestRedaction(t *testing.T) {
	scrub := func(t *testing.T, attrs ...any) string {
		t.Helper()
		var buf bytes.Buffer
		NewLogger(testLogCfg(), &buf).Info("event", attrs...)
		return buf.String()
	}

	t.Run("R-OPS-013 key-based redaction of password secret token authorization cookie api_key credential", func(t *testing.T) {
		keys := []string{"password", "client_secret", "access_token", "Authorization", "Set-Cookie", "api_key", "apikey", "private_key", "credentials", "webhook_SECRET"}
		for _, k := range keys {
			out := scrub(t, k, "value-that-must-not-appear")
			if strings.Contains(out, "value-that-must-not-appear") {
				t.Errorf("key %q not redacted: %s", k, out)
			}
			if !strings.Contains(out, RedactedValue) {
				t.Errorf("key %q has no marker: %s", k, out)
			}
		}
	})

	t.Run("R-OPS-013 token_id and token_type are exempt", func(t *testing.T) {
		out := scrub(t, "token_id", "0190-abc", "token_type", "pat")
		if !strings.Contains(out, "0190-abc") || !strings.Contains(out, `"pat"`) {
			t.Errorf("exempt ID fields redacted: %s", out)
		}
	})

	t.Run("R-OPS-013 trove token formats are scrubbed in free text", func(t *testing.T) {
		for _, typ := range []string{"pat", "sat", "cit"} {
			tok := "trv_" + typ + "_0123456789AbCdEfGhIjKlMnOp"
			out := scrub(t, "detail", "failed for "+tok+" today")
			if strings.Contains(out, "0123456789") {
				t.Errorf("%s token leaked: %s", typ, out)
			}
			if !strings.Contains(out, "trv_"+typ+"_[REDACTED]") {
				t.Errorf("%s marker missing: %s", typ, out)
			}
		}
		if strings.Contains(scrub(t, "msgtext", "x"), "trv_") {
			t.Error("unexpected token text")
		}
	})

	t.Run("R-OPS-013 the message text is scrubbed too", func(t *testing.T) {
		var buf bytes.Buffer
		NewLogger(testLogCfg(), &buf).Info("got trv_pat_ABCDEFGHIJKLMNOP")
		if strings.Contains(buf.String(), "ABCDEFGHIJKLMNOP") {
			t.Errorf("leak in msg: %s", buf.String())
		}
	})

	t.Run("R-OPS-013 Bearer values and Basic credentials are scrubbed", func(t *testing.T) {
		out := scrub(t, "detail", "header was Bearer abc.def-ghi and Basic dXNlcjpwYXNzd29yZA==")
		if strings.Contains(out, "abc.def-ghi") || strings.Contains(out, "dXNlcjpwYXNzd29yZA") {
			t.Errorf("leak: %s", out)
		}
		if !strings.Contains(out, "Bearer [REDACTED]") {
			t.Errorf("marker missing: %s", out)
		}
	})

	t.Run("R-OPS-013 URL user information is scrubbed", func(t *testing.T) {
		out := scrub(t, "url", "https://svc:s3cr3tpw@registry.example/npm/")
		if strings.Contains(out, "s3cr3tpw") || !strings.Contains(out, "svc:[REDACTED]@registry.example") {
			t.Errorf("userinfo: %s", out)
		}
		out = scrub(t, "url", "https://onlytoken123@registry.example/")
		if strings.Contains(out, "onlytoken123") {
			t.Errorf("bare userinfo leaked: %s", out)
		}
	})

	t.Run("R-OPS-013 query parameters on the redaction list are scrubbed", func(t *testing.T) {
		out := scrub(t, "path", "/files/x?token=abc123&keep=1&X-Amz-Signature=deadbeef")
		if strings.Contains(out, "abc123") || strings.Contains(out, "deadbeef") || !strings.Contains(out, "keep=1") {
			t.Errorf("query: %s", out)
		}
	})

	t.Run("R-OPS-013 the poll secret of the npm web login is scrubbed", func(t *testing.T) {
		out := scrub(t, "path", "/auth/cli/pollsecret-9f8e7d/done")
		if strings.Contains(out, "pollsecret") || !strings.Contains(out, "/auth/cli/[REDACTED]") {
			t.Errorf("poll secret: %s", out)
		}
	})

	t.Run("R-OPS-013 error values are scrubbed", func(t *testing.T) {
		out := scrub(t, "error", errors.New("GET https://u:pw12345@up.example/x failed"))
		if strings.Contains(out, "pw12345") {
			t.Errorf("error leaked: %s", out)
		}
	})

	t.Run("R-OPS-013 secret-typed values never print their content", func(t *testing.T) {
		s := Secret("plain-value-123")
		for name, out := range map[string]string{
			"slog":   scrub(t, "innocent", s),
			"config": scrub(t, "innocent", config.Secret{Value: "cfg-value-456"}),
			"cfgptr": scrub(t, "innocent", &config.Secret{Value: "cfg-value-456"}),
		} {
			if strings.Contains(out, "plain-value-123") || strings.Contains(out, "cfg-value-456") {
				t.Errorf("%s leaked: %s", name, out)
			}
		}
		j, _ := json.Marshal(struct{ P Secret }{s})
		if strings.Contains(string(j), "plain-value") {
			t.Errorf("MarshalJSON leaked: %s", j)
		}
		if strings.Contains(s.String()+s.LogValue().String(), "plain-value") {
			t.Error("String or LogValue leaked")
		}
		if s.Reveal() != "plain-value-123" {
			t.Error("Reveal broken")
		}
	})

	t.Run("R-OPS-013 text format redacts like JSON", func(t *testing.T) {
		var buf bytes.Buffer
		cfg := testLogCfg()
		cfg.Format = "text"
		NewLogger(cfg, &buf).Info("x", "password", "pw-xyz", "u", "trv_pat_AAAABBBBCCCC")
		if strings.Contains(buf.String(), "pw-xyz") || strings.Contains(buf.String(), "AAAABBBB") {
			t.Errorf("leak: %s", buf.String())
		}
	})

	t.Run("R-OPS-013 user information is redacted up to the last @, with quotes and unencoded @ in the password", func(t *testing.T) {
		for in, leak := range map[string]string{
			"https://svc:pa'ss'word@registry.example/npm/":   "ss'word",
			"https://svc:p@ss@w0rd@registry.example/npm/":    "w0rd",
			"https://svc:p@ss@w0rd@registry.example?x=1":     "w0rd",
			`parse "http://svc:pa\"ssw0rd@registry.example"`: "ssw0rd",
			"git+ssh://bot@corp:tok3n@git.example:org/repo":  "tok3n",
		} {
			out := scrub(t, "detail", in)
			if strings.Contains(out, leak) || !strings.Contains(out, RedactedValue+"@") {
				t.Errorf("%s: %s", in, out)
			}
		}
		if out := scrub(t, "detail", "https://svc:p@ss@registry.example/npm/@scope/pkg"); !strings.Contains(out, "svc:[REDACTED]@registry.example/npm/@scope/pkg") {
			t.Errorf("an @ in the path must not widen the match: %s", out)
		}
		if out := scrub(t, "detail", "see https://registry.example/x and mail ops@example.org"); strings.Contains(out, RedactedValue) {
			t.Errorf("a URL without user information is redacted: %s", out)
		}
	})

	t.Run("R-OPS-013 values of kind Any are scrubbed: Stringer, URL, header, map, slice, struct", func(t *testing.T) {
		u, _ := url.Parse("https://svc:urlpw123@up.example/p?token=qtok123&v=1")
		h := http.Header{"Authorization": {"Bearer hdrtok123"}, "Cookie": {"sid=cookie123"}, "X-Api-Key": {"key123"}, "Accept": {"application/json"}}
		type upstream struct {
			Name     string
			URL      string
			Password string
		}
		for name, val := range map[string]any{
			"stringer": stringerVal("fetch https://svc:strpw123@up.example/ failed"),
			"url":      u,
			"urlvalue": *u,
			"header":   h,
			"map":      map[string]any{"endpoint": "https://svc:mappw123@up.example/", "client_secret": "mapsecret123", "n": 12345678901234567},
			"slice":    []any{"Bearer slicetok123", map[string]string{"api_key": "slicekey123"}},
			"strings":  []string{"trv_pat_STRINGSLICE123"},
			"struct":   upstream{Name: "npm", URL: "https://svc:structpw123@up.example/", Password: "structpw456"},
			"ptr":      &upstream{Name: "npm", Password: "ptrpw789"},
		} {
			out := scrub(t, "value", val)
			for _, leak := range []string{"urlpw123", "qtok123", "strpw123", "hdrtok123", "cookie123", "key123", "mappw123", "mapsecret123", "slicetok123", "slicekey123", "STRINGSLICE123", "structpw123", "structpw456", "ptrpw789"} {
				if strings.Contains(out, leak) {
					t.Errorf("%s: %q leaked: %s", name, leak, out)
				}
			}
			if !strings.Contains(out, RedactedValue) {
				t.Errorf("%s: no marker: %s", name, out)
			}
		}
		out := scrub(t, "value", map[string]any{"n": int64(12345678901234567), "accept": "application/json"})
		if !strings.Contains(out, `"n":12345678901234567`) || !strings.Contains(out, `"accept":"application/json"`) {
			t.Errorf("harmless content changed: %s", out)
		}
		if out := scrub(t, "value", h); !strings.Contains(out, "application/json") {
			t.Errorf("a harmless header lost its value: %s", out)
		}
		var nilErr *nilError
		if out := scrub(t, "err", nilErr); !strings.Contains(out, `"err":null`) {
			t.Errorf("typed nil error: %s", out)
		}
	})

	t.Run("R-OPS-013 grouped attributes are redacted", func(t *testing.T) {
		var buf bytes.Buffer
		NewLogger(testLogCfg(), &buf).Info("x", slog.Group("auth", slog.String("password", "gpw"), slog.String("user", "bob")))
		if strings.Contains(buf.String(), "gpw") || !strings.Contains(buf.String(), "bob") {
			t.Errorf("group: %s", buf.String())
		}
	})
}
