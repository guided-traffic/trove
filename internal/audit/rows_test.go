package audit

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
)

func TestR_OPS_014_RegistryRules(t *testing.T) {
	good := EventType{Name: "rtest.thing.created", Category: CategorySystem, Mode: Transactional, Schema: `{"type":"object"}`}
	cases := []struct {
		name string
		mut  func(*EventType)
	}{
		{"entry rule without the journal flag", func(e *EventType) { e.Entry = EntryRequired }},
		{"optional entry outside the shared types", func(e *EventType) { e.Journal = JournalRevocation; e.Entry = EntryOptional }},
		{"schema is not JSON", func(e *EventType) { e.Schema = `{` }},
		{"schema is not an object", func(e *EventType) { e.Schema = `[]` }},
		{"array without maxItems", func(e *EventType) {
			e.Schema = `{"type":"object","properties":{"files":{"type":"array","items":{"type":"string"}}}}`
		}},
		{"array above 1000 items", func(e *EventType) {
			e.Schema = `{"type":"object","properties":{"files":{"type":"array","maxItems":1001}}}`
		}},
		{"nested array without maxItems", func(e *EventType) {
			e.Schema = `{"type":"object","properties":{"a":{"type":"object","properties":{"b":{"type":["null","array"]}}}}}`
		}},
	}
	for _, c := range cases {
		t.Run("R-OPS-014 "+c.name, func(t *testing.T) {
			e := good
			c.mut(&e)
			defer func() {
				if recover() == nil {
					t.Fatal("Register did not panic")
				}
			}()
			Register(e)
		})
	}
	t.Run("R-OPS-014 a flagged type defaults to a required entry and bounded arrays pass", func(t *testing.T) {
		Register(EventType{
			Name: "rtest.access.revoked", Category: CategoryToken, Mode: Transactional, Journal: JournalRevocation,
			Schema: `{"type":"object","properties":{"ids":{"type":"array","maxItems":1000}}}`,
		})
		got, ok := Lookup("rtest.access.revoked")
		if !ok || got.Entry != EntryRequired {
			t.Fatalf("entry rule = %q", got.Entry)
		}
	})
	t.Run("R-OPS-016 the audit.* types are registered", func(t *testing.T) {
		got, ok := Lookup(AuditExported)
		if !ok || got.Mode != Transactional || got.Journal != NoJournal {
			t.Fatalf("audit.exported = %+v", got)
		}
	})
}

func TestR_OPS_014_JournalEntryRules(t *testing.T) {
	required := EventType{Name: "x.y", Journal: JournalRevocation, Entry: EntryRequired}
	optional := EventType{Name: "package.published", Journal: JournalRevocation, Entry: EntryOptional}
	plain := EventType{Name: "x.z"}
	entry := json.RawMessage(`{"token":"01"}`)

	if _, err := journalEntry(required, Event{}); !errors.Is(err, ErrJournalEntryRequired) {
		t.Errorf("required without entry: %v", err)
	}
	if e, err := journalEntry(required, Event{Journal: entry}); err != nil || string(e) != string(entry) {
		t.Errorf("required with entry: %s, %v", e, err)
	}
	if e, err := journalEntry(optional, Event{}); err != nil || e != nil {
		t.Errorf("optional without entry: %s, %v", e, err)
	}
	if e, err := journalEntry(optional, Event{Journal: entry}); err != nil || e == nil {
		t.Errorf("optional with entry: %s, %v", e, err)
	}
	if _, err := journalEntry(plain, Event{Journal: entry}); !errors.Is(err, ErrJournalNotAllowed) {
		t.Errorf("entry on an unflagged type: %v", err)
	}
	if e, err := journalEntry(plain, Event{}); err != nil || e != nil {
		t.Errorf("unflagged without entry: %s, %v", e, err)
	}
	if _, err := journalEntry(required, Event{Journal: json.RawMessage(`"x"`)}); !errors.Is(err, ErrInvalidEvent) {
		t.Errorf("entry that is no object: %v", err)
	}
}

func TestR_OPS_015_RowMapping(t *testing.T) {
	typ := EventType{Name: "repository.created", Category: CategoryRepository}
	at := time.Date(2026, 10, 29, 12, 0, 0, 123_000_000, time.UTC)
	ev := Event{
		Type: "repository.created", Actor: Actor{Kind: "break_glass", ID: "5F0E1111-2222-4333-8444-555566667777", Name: "root", TokenID: "not-a-uuid", AuthMethod: "basic"},
		Source:  Source{ClientAddr: "[2001:db8::1]:443", UserAgent: "npm/11", TraceID: "abc"},
		Subject: Subject{Kind: "repository", Name: "npm-local"},
	}
	r, err := toRow(typ, ev, "id", at, "pod-1")
	if err != nil {
		t.Fatal(err)
	}
	switch {
	case r.ActorType != "breakglass", r.ActorID != "5f0e1111-2222-4333-8444-555566667777", r.TokenID != "":
		t.Errorf("actor = %+v", r)
	case r.SourceIP != "2001:db8::1", r.Repository != "npm-local", r.ResourceID != "npm-local", r.Outcome != "success":
		t.Errorf("row = %+v", r)
	case string(r.Details) != "{}", r.At != "2026-10-29T12:00:00.123Z", r.Replica != "pod-1":
		t.Errorf("row = %+v", r)
	}
	if r, _ := toRow(typ, ev, "id", time.Time{}, ""); r.At != "" {
		t.Errorf("a zero time must stay empty so that the database decides: %q", r.At)
	}
	if _, err := toRow(typ, Event{Type: typ.Name}, "id", at, ""); !errors.Is(err, ErrInvalidEvent) {
		t.Errorf("missing actor kind: %v", err)
	}
	if _, err := toRow(typ, Event{Actor: Actor{Kind: "user"}, Outcome: "weird"}, "id", at, ""); !errors.Is(err, ErrInvalidEvent) {
		t.Errorf("bad outcome: %v", err)
	}
	if _, err := toRow(typ, Event{Actor: Actor{Kind: "user"}, Details: json.RawMessage(`"s"`)}, "id", at, ""); !errors.Is(err, ErrInvalidEvent) {
		t.Errorf("details that are no object: %v", err)
	}
}

func TestR_OPS_015_TextIsBoundedAndHasNoNUL(t *testing.T) {
	typ := EventType{Name: "a.b", Category: CategorySystem}
	r, err := toRow(typ, Event{Actor: Actor{Kind: "user", Name: "a\x00b"}, Source: Source{UserAgent: strings.Repeat("é", 5000)}}, "id", time.Now(), "")
	if err != nil {
		t.Fatal(err)
	}
	if r.ActorName != "ab" || len(r.UserAgent) > maxLongTxt || !strings.HasSuffix(r.UserAgent, "é") {
		t.Errorf("name %q, user agent of %d bytes", r.ActorName, len(r.UserAgent))
	}
}

func TestR_OPS_015_IDsAreUUIDv7AndCarryTheirTime(t *testing.T) {
	at := time.Date(2026, 10, 29, 12, 0, 0, 5_000_000, time.UTC)
	id := newID(at)
	got, ok := idTime(id)
	if !ok || !got.Equal(at) {
		t.Fatalf("idTime(%s) = %v, %v", id, got, ok)
	}
	if _, ok := idTime("5f0e1111-2222-4333-8444-555566667777"); ok {
		t.Error("a version 4 UUID carries no time")
	}
	if _, ok := idTime("nope"); ok {
		t.Error("a malformed id carries no time")
	}
	if a, b := newID(at), newID(at); a == b {
		t.Error("two IDs collide")
	}
}

func TestR_OPS_017_ExportRange(t *testing.T) {
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for name, c := range map[string]struct {
		from, to time.Time
		ok       bool
	}{
		"both bounds":    {from, from.Add(24 * time.Hour), true},
		"400 days":       {from, from.Add(MaxExportRange), true},
		"401 days":       {from, from.Add(MaxExportRange + time.Hour), false},
		"missing to":     {from, time.Time{}, false},
		"missing from":   {time.Time{}, from, false},
		"to before from": {from, from.Add(-time.Hour), false},
		"empty range":    {from, from, false},
	} {
		t.Run("R-OPS-017 "+name, func(t *testing.T) {
			err := CheckExportRange(c.from, c.to)
			if (err == nil) != c.ok {
				t.Fatalf("err = %v", err)
			}
			if err != nil && errs.KindOf(err) != errs.Invalid {
				t.Errorf("kind = %v", errs.KindOf(err))
			}
		})
	}
}
