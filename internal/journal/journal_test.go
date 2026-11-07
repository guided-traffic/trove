package journal

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/meta"
)

const id1 = "0192f0c4-7a1e-7c3b-9d2e-5b8f1a2c3d4e"

func row() OutboxRow {
	return OutboxRow{
		ID: id1, At: time.Date(2026, 10, 28, 23, 59, 59, 114_000_000, time.UTC), Event: "token.revoked",
		Entry: json.RawMessage(`{"tokenId":"0192f0a1"}`),
	}
}

func TestObject(t *testing.T) {
	t.Run("R-HA-054 an object wraps the outbox row and round-trips", func(t *testing.T) {
		o := NewObject(row())
		raw, err := o.Encode()
		if err != nil {
			t.Fatal(err)
		}
		again, _ := NewObject(row()).Encode()
		if string(raw) != string(again) {
			t.Fatal("shipping a row twice must produce identical bytes")
		}
		got, err := Decode(raw)
		if err != nil || got.ID != id1 || got.Event != "token.revoked" || !got.At.Equal(o.At) {
			t.Fatalf("Decode = %+v, %v", got, err)
		}
	})
	t.Run("R-HA-054 the key is the UTC day and the entry ID", func(t *testing.T) {
		k := Key("trove", NewObject(row()))
		if k != "trove/_journal/revocations/2026/10/28/"+id1+".json" {
			t.Fatalf("Key = %q", k)
		}
		if got, ok := ParseKey("trove", k); !ok || got != id1 {
			t.Fatalf("ParseKey = %q, %v", got, ok)
		}
		for _, bad := range []string{"trove/_journal/revocations/x.json", "other/_journal/revocations/2026/10/28/" + id1 + ".json", "trove/_journal/revocations/2026/10/28/nope.json"} {
			if _, ok := ParseKey("trove", bad); ok {
				t.Errorf("ParseKey accepted %q", bad)
			}
		}
		if got := Key("", NewObject(row())); !strings.HasPrefix(got, "_journal/revocations/2026/10/28/") {
			t.Fatalf("Key without prefix = %q", got)
		}
	})
	t.Run("R-HA-054 a forged or newer object is refused", func(t *testing.T) {
		for _, raw := range []string{`{"v":1,"id":"x","at":"2026-10-28T00:00:00Z","event":"a.b","entry":{}}`, `{"v":1,"id":"` + id1 + `","at":"2026-10-28T00:00:00Z","event":"a.b","entry":[]}`, `not json`} {
			if _, err := Decode([]byte(raw)); err == nil {
				t.Errorf("Decode(%s) succeeded", raw)
			}
		}
		_, err := Decode([]byte(`{"v":2,"id":"` + id1 + `","at":"2026-10-28T00:00:00Z","event":"a.b","entry":{}}`))
		if !errors.Is(err, ErrUnknownVersion) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("R-HA-054 entries replay in the order of at and id", func(t *testing.T) {
		a := Object{At: time.Unix(10, 0), ID: "b"}
		b := Object{At: time.Unix(10, 0), ID: "c"}
		c := Object{At: time.Unix(11, 0), ID: "a"}
		if !a.Less(b) || !b.Less(c) || c.Less(a) {
			t.Fatal("order")
		}
	})
	t.Run("R-HA-054 DayPrefixes covers the replay window", func(t *testing.T) {
		got := DayPrefixes("trove", time.Date(2026, 10, 28, 23, 58, 0, 0, time.UTC), time.Date(2026, 10, 30, 1, 0, 0, 0, time.UTC))
		if len(got) != 3 || got[0] != "trove/_journal/revocations/2026/10/28/" || got[2] != "trove/_journal/revocations/2026/10/30/" {
			t.Fatalf("DayPrefixes = %v", got)
		}
	})
}

// setCmp is a comparator over sets of strings: after narrows current when it is a subset.
type setCmp struct{}

func (setCmp) Narrows(current, after map[string]bool) bool {
	for k := range after {
		if !current[k] {
			return false
		}
	}
	return true
}

func TestApplyUpdate(t *testing.T) {
	set := func(ks ...string) map[string]bool {
		m := map[string]bool{}
		for _, k := range ks {
			m[k] = true
		}
		return m
	}
	var applied, suspended bool
	act := UpdateActions{
		Apply:   func() error { applied = true; return nil },
		Suspend: func() (json.RawMessage, error) { suspended = true; return json.RawMessage(`{"after":1}`), nil },
	}
	reset := func() { applied, suspended = false, false }
	t.Run("R-HA-054 case 1: the after-state narrows the current state and is applied", func(t *testing.T) {
		reset()
		out, _, err := ApplyUpdate(setCmp{}, set("a", "b"), set("a", "b"), set("a"), act)
		if err != nil || out != Applied || !applied || suspended {
			t.Fatalf("out=%s applied=%v suspended=%v err=%v", out, applied, suspended, err)
		}
	})
	t.Run("R-HA-054 case 2: a change that only added access is skipped", func(t *testing.T) {
		reset()
		out, _, err := ApplyUpdate(setCmp{}, set("a"), set("a"), set("a", "c"), act)
		if err != nil || out != Skipped || applied || suspended {
			t.Fatalf("out=%s applied=%v suspended=%v err=%v", out, applied, suspended, err)
		}
	})
	t.Run("R-HA-054 case 3: a narrowing on top of a broadening is suspended with the after-state", func(t *testing.T) {
		reset()
		// The restored state lacks "x", which the journal says was added and then partly removed: the after-state "y"
		// is not contained in current and does not contain before.
		out, detail, err := ApplyUpdate(setCmp{}, set("a"), set("a", "x"), set("y"), act)
		if err != nil || out != Suspended || !suspended || applied || string(detail) != `{"after":1}` {
			t.Fatalf("out=%s detail=%s applied=%v suspended=%v err=%v", out, detail, applied, suspended, err)
		}
	})
}

func TestRegistry(t *testing.T) {
	// The process-wide registry is filled once; the test registers events no real domain uses.
	h := Handler{
		Domain: "test", Events: []string{"test.one", "test.two"},
		Apply: func(context.Context, meta.Tx, Env, Object) (Result, error) { return Result{Outcome: Applied}, nil },
	}
	Register(h)
	t.Run("R-HA-054 a handler is found by each of its events", func(t *testing.T) {
		for _, e := range []string{"test.one", "test.two"} {
			if got, ok := HandlerFor(e); !ok || got.Domain != "test" {
				t.Errorf("HandlerFor(%s) = %+v, %v", e, got, ok)
			}
		}
		if _, ok := HandlerFor("test.three"); ok {
			t.Error("unknown event has a handler")
		}
	})
	t.Run("R-HA-054 two handlers for one event are a programming error", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatal("no panic")
			}
		}()
		Register(Handler{Domain: "other", Events: []string{"test.two"}, Apply: h.Apply})
	})
}

func TestSummary(t *testing.T) {
	var s Summary
	s.Add(Result{ID: id1, Event: "token.revoked", Outcome: Applied})
	s.Add(Result{ID: id1, Event: "token.revoked", Outcome: Applied})
	s.Add(Result{ID: id1, Event: "npm.unpublish", Outcome: Skipped, Subject: "@a/b@1"})
	if s.Counts[Applied] != 2 || s.Counts[Skipped] != 1 || !strings.Contains(s.Results[2].Line(), "skipped") {
		t.Fatalf("Summary = %+v", s)
	}
}
