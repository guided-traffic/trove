package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/meta"
)

func TestR_OPS_017_AuditFilter(t *testing.T) {
	t.Run("R-OPS-017 the closed sets are validated", func(t *testing.T) {
		for _, bad := range []map[string]string{{"category": "nope"}, {"outcome": "nope"}, {"from": "yesterday"}, {"to": "2026-13-01T00:00:00Z"}} {
			if _, err := auditFilter(bad); err == nil {
				t.Errorf("%v was accepted", bad)
			}
		}
		f, err := auditFilter(map[string]string{
			"category": "authz", "outcome": "denied", "action": "authz.*", "actor": "bob",
			"from": "2026-10-28T12:00:00+02:00", "to": "2026-10-29T00:00:00Z",
		})
		if err != nil {
			t.Fatal(err)
		}
		if f.From.Format(time.RFC3339) != "2026-10-28T10:00:00Z" || f.Outcome != audit.Denied || f.Action != "authz.*" {
			t.Fatalf("%+v", f)
		}
	})
	t.Run("R-OPS-017 a parameter that takes one value is refused when it is repeated", func(t *testing.T) {
		vals, _ := url.ParseQuery("action=a&action=b&actor=x")
		if _, err := onceEach(vals, "action", "actor"); err == nil {
			t.Fatal("a repeated action was accepted")
		}
		got, err := onceEach(vals, "actor", "traceId")
		if err != nil || got["actor"] != "x" || len(got) != 1 {
			t.Fatalf("%v %v", got, err)
		}
	})
	t.Run("R-OPS-017 the export refuses a parameter it does not declare", func(t *testing.T) {
		vals, _ := url.ParseQuery("from=x&limit=5")
		if err := onlyParams(vals, []string{"from", "to"}); err == nil {
			t.Fatal("limit was accepted")
		}
	})
	t.Run("R-OPS-017 a cursor key is a time and an event ID", func(t *testing.T) {
		if _, err := auditCursor([]string{"2026-10-28T12:00:00.123456Z"}); err == nil {
			t.Error("one value")
		}
		if _, err := auditCursor([]string{"x", "id"}); err == nil {
			t.Error("not a time")
		}
		c, err := auditCursor([]string{"2026-10-28T12:00:00.123456Z", "01933b1e-7c2a-7f4e-9d1b-6a2f0c9e8b11"})
		if err != nil || c.OccurredAt.Nanosecond() != 123456000 {
			t.Fatalf("%+v %v", c, err)
		}
	})
}

func TestR_OPS_017_AuditEventRepresentation(t *testing.T) {
	at := time.Date(2026, 10, 28, 11, 58, 2, 417000000, time.UTC)
	ev := toAuditEvent(audit.Entry{
		ID: "01933b1e-7c2a-7f4e-9d1b-6a2f0c9e8b11", OccurredAt: at, Action: "authz.policy.created", Category: "authz", Outcome: audit.Success,
		Actor: audit.Actor{Kind: "user", Name: "alice"},
	})
	raw, _ := json.Marshal(ev)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	for _, k := range []string{"traceId", "replica", "endpoint", "repository", "path", "reason"} {
		if v, ok := m[k]; !ok || v != nil {
			t.Errorf("%s = %v (present %v), want null", k, v, ok)
		}
	}
	if m["occurredAt"] != "2026-10-28T11:58:02.417Z" || m["details"].(map[string]any) == nil {
		t.Errorf("%v", m)
	}
	actor := m["actor"].(map[string]any)
	if actor["id"] != nil || actor["name"] != "alice" || actor["tokenId"] != nil {
		t.Errorf("actor = %v", actor)
	}
}

func TestR_OPS_017_CSVEncoder(t *testing.T) {
	t.Run("R-OPS-017 a cell that a spreadsheet would run as a formula is defused", func(t *testing.T) {
		for in, want := range map[string]string{
			"=1+1": "'=1+1", "+cmd": "'+cmd", "-2": "'-2", "@sum": "'@sum", "\tx": "'\tx", "\rx": "'\rx",
			"curl/8": "curl/8", "": "", "a=b": "a=b",
		} {
			if got := csvSafe(in); got != want {
				t.Errorf("csvSafe(%q) = %q, want %q", in, got, want)
			}
		}
	})
	t.Run("R-OPS-017 the header and an event row have the same columns", func(t *testing.T) {
		var enc exportEncoder = &csvEncoder{}
		var b bytes.Buffer
		enc.header(&b)
		if err := enc.event(&b, toAuditEvent(audit.Entry{ID: "i", OccurredAt: time.Unix(0, 0), Action: "a", Details: json.RawMessage(`{"k":"v,w"}`)})); err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(b.String()), "\n")
		if len(lines) != 2 || !strings.HasPrefix(lines[0], "id,occurredAt,action,") || !strings.HasSuffix(lines[1], `"{""k"":""v,w""}"`) {
			t.Fatalf("%q", lines)
		}
	})
}

type captureRecorder struct{ events []audit.Event }

func (c *captureRecorder) RecordTx(_ context.Context, _ meta.Tx, ev audit.Event) error {
	c.events = append(c.events, ev)
	return nil
}

func (c *captureRecorder) Record(_ context.Context, ev audit.Event) { c.events = append(c.events, ev) }

func TestQ_OPS_49_RequestSourceIsAddedToEvents(t *testing.T) {
	inner := &captureRecorder{}
	rec := WithRequestSource(inner)
	router := httpserver.NewClientRouter()
	router.HandleFunc("GET /x", func(w http.ResponseWriter, r *http.Request) {
		rec.Record(r.Context(), audit.Event{Type: "authz.denied"})
		_ = rec.RecordTx(r.Context(), nil, audit.Event{Type: "authz.policy.created", Source: audit.Source{UserAgent: "curl"}})
		rec.Record(r.Context(), audit.Event{Type: "kept", Source: audit.Source{ClientAddr: "192.0.2.9", TraceID: "t"}})
	})
	res, err := httpserver.NewResolver(nil, "http://trove.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	h := httpserver.Chain{Router: router, Resolver: res}.Handler()
	req := httptest.NewRequest("GET", "/x", nil)
	req.RemoteAddr = "10.1.2.3:5555"
	req.Header.Set("User-Agent", "trove-cli/1")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	t.Run("an event from a request gets the client address and the trace ID the client received", func(t *testing.T) {
		trace := w.Header().Get("X-Request-Id")
		if trace == "" || len(inner.events) != 3 {
			t.Fatalf("trace %q, %d events", trace, len(inner.events))
		}
		if got := inner.events[0].Source; got.ClientAddr != "10.1.2.3" || got.TraceID != trace || got.UserAgent != "trove-cli/1" {
			t.Errorf("%+v", got)
		}
		if got := inner.events[1].Source; got.ClientAddr != "10.1.2.3" || got.UserAgent != "curl" {
			t.Errorf("%+v", got)
		}
	})
	t.Run("a field the producer set stays", func(t *testing.T) {
		if got := inner.events[2].Source; got.ClientAddr != "192.0.2.9" || got.TraceID != "t" {
			t.Errorf("%+v", got)
		}
	})
	t.Run("nothing is added outside a request, and a nil recorder stays nil", func(t *testing.T) {
		inner.events = nil
		rec.Record(context.Background(), audit.Event{Type: "y"})
		if got := inner.events[0].Source; got != (audit.Source{}) {
			t.Errorf("%+v", got)
		}
		if WithRequestSource(nil) != nil {
			t.Fatal("not nil")
		}
	})
}
