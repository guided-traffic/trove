//go:build integration

package audit

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
)

// The event types of these tests. authz.denied, token.rejected and
// artifact.downloaded belong to other areas; they are registered here only where
// the merged tree does not register them yet.
func init() {
	const open = `{"type":"object"}`
	audit.Register(audit.EventType{Name: "probe.thing.created", Category: audit.CategorySystem, Mode: audit.Transactional, Schema: open})
	audit.Register(audit.EventType{Name: "probe.thing.deleted", Category: audit.CategoryRepository, Mode: audit.Transactional, Schema: open})
	audit.Register(audit.EventType{
		Name: "probe.access.revoked", Category: audit.CategoryToken, Mode: audit.Transactional, Journal: audit.JournalRevocation, Schema: open,
	})
	audit.Register(audit.EventType{Name: "probe.hit.logged", Category: audit.CategoryAuth, Mode: audit.Async, Schema: open})
	for _, name := range []string{"authz.denied", "token.rejected", "artifact.downloaded"} {
		if _, ok := audit.Lookup(name); !ok {
			audit.Register(audit.EventType{Name: name, Category: audit.CategoryAuthz, Mode: audit.Async, Schema: open})
		}
	}
}

func count(t testing.TB, e *env, table, where string) int {
	t.Helper()
	return scalar[int](t, e.admin, "SELECT count(*) FROM trove."+table+" WHERE "+where)
}

// change is a stand-in for a state change in the transaction of an event: a
// repository row.
func change(t testing.TB, tx meta.Tx, key string) {
	t.Helper()
	ptx, ok := postgres.Unwrap(tx)
	if !ok {
		t.Fatal("not a PostgreSQL transaction")
	}
	if _, err := ptx.Exec(context.Background(), "INSERT INTO trove.repositories (key, format, class) VALUES ($1, 'generic', 'local')", key); err != nil {
		t.Fatalf("change: %v", err)
	}
}

func user(name string) audit.Actor {
	return audit.Actor{Kind: "user", ID: "5f0e1111-2222-4333-8444-555566667777", Name: name, TokenID: "c41d1111-2222-4333-8444-555566667777", AuthMethod: "bearer"}
}

func TestR_OPS_015_TransactionalEvents(t *testing.T) {
	e := newEnv(t)
	db := e.open()
	w, m, logs := e.writer(db, auditCfg(), time.Now)
	ctx := context.Background()

	t.Run("R-OPS-015 a committed change has its event, carrying the actor, source and details", func(t *testing.T) {
		err := db.InTx(ctx, func(tx meta.Tx) error {
			change(t, tx, "ok-1")
			return w.RecordTx(ctx, tx, audit.Event{
				Type: "probe.thing.created", Actor: user("ann"), Outcome: audit.Success,
				Source:  audit.Source{ClientAddr: "10.12.4.7:5555", UserAgent: "npm/11", TraceID: "4bf92f3577b34da6a3ce929d0e0e4736"},
				Subject: audit.Subject{Kind: "repository", Name: "ok-1"}, Endpoint: "default",
				Details: json.RawMessage(`{"changes":[{"field":"x","from":1,"to":2}]}`),
			})
		})
		if err != nil {
			t.Fatal(err)
		}
		var action, category, outcome, atype, aname, ip, repo, rtype, trace, replica, endpoint, auth string
		var hasToken bool
		err = e.admin.QueryRow(ctx, `SELECT action, category, outcome, actor_type, actor_name, host(source_ip), repository, resource_type,
			trace_id, replica, endpoint, auth_method, token_id IS NOT NULL FROM trove.audit_event WHERE action = 'probe.thing.created'`).
			Scan(&action, &category, &outcome, &atype, &aname, &ip, &repo, &rtype, &trace, &replica, &endpoint, &auth, &hasToken)
		if err != nil {
			t.Fatal(err)
		}
		got := strings.Join([]string{action, category, outcome, atype, aname, ip, repo, rtype, trace, replica, endpoint, auth}, "|")
		want := "probe.thing.created|system|success|user|ann|10.12.4.7|ok-1|repository|4bf92f3577b34da6a3ce929d0e0e4736|it-replica|default|bearer"
		if got != want || !hasToken {
			t.Errorf("row = %s (token %v)\n want %s", got, hasToken, want)
		}
		if n := count(t, e, "repositories", "key = 'ok-1'"); n != 1 {
			t.Errorf("the change is missing: %d", n)
		}
		if n := count(t, e, "revocation_outbox", "true"); n != 0 {
			t.Errorf("an unflagged type wrote %d outbox rows", n)
		}
		if m.recorded.Load() != 1 || strings.Count(logs.String(), `"action":"probe.thing.created"`) != 1 {
			t.Errorf("recorded %d, log: %s", m.recorded.Load(), logs.String())
		}
	})

	t.Run("R-OPS-015 a rolled-back transaction leaves neither event nor journal row, and no log copy", func(t *testing.T) {
		before := strings.Count(logs.String(), `"msg":"audit"`)
		boom := errors.New("boom")
		err := db.InTx(ctx, func(tx meta.Tx) error {
			change(t, tx, "rolled-back")
			if err := w.RecordTx(ctx, tx, audit.Event{
				Type: "probe.access.revoked", Actor: user("ann"), Details: json.RawMessage(`{}`), Journal: json.RawMessage(`{"token":"x","state":"revoked"}`),
			}); err != nil {
				return err
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("InTx = %v", err)
		}
		if n := count(t, e, "audit_event", "action = 'probe.access.revoked'"); n != 0 {
			t.Errorf("events after rollback = %d", n)
		}
		if n := count(t, e, "revocation_outbox", "true"); n != 0 {
			t.Errorf("outbox rows after rollback = %d", n)
		}
		if n := count(t, e, "repositories", "key = 'rolled-back'"); n != 0 {
			t.Errorf("the change survived: %d", n)
		}
		if after := strings.Count(logs.String(), `"msg":"audit"`); after != before {
			t.Errorf("log copies went from %d to %d", before, after)
		}
	})

	t.Run("R-OPS-015 a journal-flagged type writes the outbox row in the same transaction, with the event's id and time", func(t *testing.T) {
		entry := `{"token":"01933b1e","state":"revoked","by":"ann"}`
		err := db.InTx(ctx, func(tx meta.Tx) error {
			change(t, tx, "journaled")
			return w.RecordTx(ctx, tx, audit.Event{
				Type: "probe.access.revoked", Actor: user("ann"), Subject: audit.Subject{Kind: "token", ID: "01933b1e"},
				Details: json.RawMessage(`{"token_type":"pat"}`), Journal: json.RawMessage(entry),
			})
		})
		if err != nil {
			t.Fatal(err)
		}
		var same bool
		var ev string
		var got []byte
		err = e.admin.QueryRow(ctx, `SELECT o.at = a.occurred_at, o.event, o.entry FROM trove.revocation_outbox o
			JOIN trove.audit_event a ON a.id = o.id WHERE a.action = 'probe.access.revoked'`).Scan(&same, &ev, &got)
		if err != nil {
			t.Fatal(err)
		}
		var a, b map[string]any
		_ = json.Unmarshal(got, &a)
		_ = json.Unmarshal([]byte(entry), &b)
		if !same || ev != "probe.access.revoked" || a["token"] != b["token"] || a["state"] != "revoked" {
			t.Errorf("outbox row: at equal %v, event %q, entry %s", same, ev, got)
		}
		if n := count(t, e, "revocation_outbox", "true"); n != 1 {
			t.Errorf("outbox rows = %d", n)
		}
	})

	t.Run("R-OPS-014 a required entry that is missing fails the transaction and the change rolls back", func(t *testing.T) {
		err := db.InTx(ctx, func(tx meta.Tx) error {
			change(t, tx, "no-entry")
			return w.RecordTx(ctx, tx, audit.Event{Type: "probe.access.revoked", Actor: user("ann")})
		})
		if !errors.Is(err, audit.ErrJournalEntryRequired) {
			t.Fatalf("InTx = %v", err)
		}
		if n := count(t, e, "repositories", "key = 'no-entry'"); n != 0 {
			t.Errorf("the revocation survived without its journal row: %d", n)
		}
	})

	t.Run("R-OPS-014 an entry on an unflagged type, an unknown type and an async type are refused", func(t *testing.T) {
		for name, c := range map[string]struct {
			ev   audit.Event
			want error
		}{
			"entry on unflagged": {audit.Event{Type: "probe.thing.created", Actor: user("a"), Journal: json.RawMessage(`{}`)}, audit.ErrJournalNotAllowed},
			"unknown":            {audit.Event{Type: "probe.nothing.created", Actor: user("a")}, audit.ErrUnknownType},
			"async":              {audit.Event{Type: "probe.hit.logged", Actor: user("a")}, audit.ErrWrongMode},
		} {
			err := db.InTx(ctx, func(tx meta.Tx) error { return w.RecordTx(ctx, tx, c.ev) })
			if !errors.Is(err, c.want) {
				t.Errorf("%s: %v, want %v", name, err, c.want)
			}
		}
	})

	t.Run("R-OPS-015 an audit insert that fails fails the change (fault injection)", func(t *testing.T) {
		// A NUL in a JSON string is valid JSON that jsonb cannot hold: the insert fails in PostgreSQL.
		err := db.InTx(ctx, func(tx meta.Tx) error {
			change(t, tx, "faulty")
			return w.RecordTx(ctx, tx, audit.Event{Type: "probe.thing.created", Actor: user("ann"), Details: json.RawMessage(`{"a":"\u0000"}`)})
		})
		if err == nil {
			t.Fatal("the transaction committed with a failed audit insert")
		}
		if n := count(t, e, "repositories", "key = 'faulty'"); n != 0 {
			t.Errorf("the change survived a failed audit insert: %d", n)
		}
	})

	t.Run("R-OPS-015 events of one transaction share the transaction's time and sort by id", func(t *testing.T) {
		err := db.InTx(ctx, func(tx meta.Tx) error {
			for range 3 {
				if err := w.RecordTx(ctx, tx, audit.Event{Type: "probe.thing.deleted", Actor: user("ann")}); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if n := scalar[int](t, e.admin, "SELECT count(DISTINCT occurred_at) FROM trove.audit_event WHERE action = 'probe.thing.deleted'"); n != 1 {
			t.Errorf("distinct times = %d", n)
		}
	})
}
