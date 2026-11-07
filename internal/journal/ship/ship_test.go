package ship

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/journal"
)

type fakeOutbox struct {
	rows    []journal.OutboxRow
	deleted []string
}

func (o *fakeOutbox) Claim(_ context.Context, limit int) ([]journal.OutboxRow, error) {
	return slices.Clone(o.rows[:min(limit, len(o.rows))]), nil
}

func (o *fakeOutbox) Delete(_ context.Context, ids []string) error {
	o.deleted = append(o.deleted, ids...)
	o.rows = slices.DeleteFunc(o.rows, func(r journal.OutboxRow) bool { return slices.Contains(ids, r.ID) })
	return nil
}

func (o *fakeOutbox) Pending(context.Context) (int64, time.Duration, error) {
	return int64(len(o.rows)), 0, nil
}

type fakeStore struct {
	puts   []string
	bodies map[string][]byte
	failAt int // the 1-based put that fails, 0 for none
}

func (s *fakeStore) Put(_ context.Context, key string, body []byte) error {
	if s.failAt > 0 && len(s.puts)+1 == s.failAt {
		return errors.New("store down")
	}
	s.puts = append(s.puts, key)
	if s.bodies == nil {
		s.bodies = map[string][]byte{}
	}
	s.bodies[key] = body
	return nil
}
func (s *fakeStore) List(context.Context, string, func(string) error) error { return nil }
func (s *fakeStore) Get(context.Context, string) ([]byte, error)            { return nil, errors.New("unused") }

func rows(n int) []journal.OutboxRow {
	at := time.Date(2026, 10, 28, 12, 0, 3, 0, time.UTC)
	ids := []string{"0192f0c4-0000-7000-8000-000000000001", "0192f0c4-0000-7000-8000-000000000002", "0192f0c4-0000-7000-8000-000000000003"}
	var out []journal.OutboxRow
	for i := range n {
		out = append(out, journal.OutboxRow{ID: ids[i], At: at.Add(time.Duration(i) * time.Second), Event: "token.revoked", Entry: json.RawMessage(`{"tokenId":"x"}`)})
	}
	return out
}

func TestR_HA_054_ShipWritesInIdOrderAndDeletesAfterTheObject(t *testing.T) {
	ob, st := &fakeOutbox{rows: rows(3)}, &fakeStore{}
	n, err := New(Options{Outbox: ob, Store: st, BlobPrefix: "trove"}).Ship(t.Context())
	if err != nil || n != 3 {
		t.Fatalf("Ship = %d, %v; want 3", n, err)
	}
	want := []string{
		"trove/_journal/revocations/2026/10/28/0192f0c4-0000-7000-8000-000000000001.json",
		"trove/_journal/revocations/2026/10/28/0192f0c4-0000-7000-8000-000000000002.json",
		"trove/_journal/revocations/2026/10/28/0192f0c4-0000-7000-8000-000000000003.json",
	}
	if !slices.Equal(st.puts, want) {
		t.Errorf("keys = %v, want %v", st.puts, want)
	}
	if len(ob.rows) != 0 || len(ob.deleted) != 3 {
		t.Errorf("the outbox keeps %d rows after %d deletions", len(ob.rows), len(ob.deleted))
	}
}

func TestR_HA_054_ShipStopsAtTheFirstFailureAndKeepsTheRest(t *testing.T) {
	ob, st := &fakeOutbox{rows: rows(3)}, &fakeStore{failAt: 2}
	n, err := New(Options{Outbox: ob, Store: st, BlobPrefix: "trove"}).Ship(t.Context())
	if err == nil || n != 1 {
		t.Fatalf("Ship = %d, %v; want one shipped and an error", n, err)
	}
	if len(ob.rows) != 2 || ob.rows[0].ID != "0192f0c4-0000-7000-8000-000000000002" {
		t.Errorf("the rows from the failed one on must stay: %v", ob.rows)
	}
	st.failAt = 0
	if n, err := New(Options{Outbox: ob, Store: st, BlobPrefix: "trove"}).Ship(t.Context()); err != nil || n != 2 {
		t.Fatalf("Ship after the store returned = %d, %v; want 2", n, err)
	}
}

func TestR_HA_054_ShipIsIdempotentInBytes(t *testing.T) {
	st := &fakeStore{}
	for range 2 {
		ob := &fakeOutbox{rows: rows(1)}
		if _, err := New(Options{Outbox: ob, Store: st, BlobPrefix: ""}).Ship(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if len(st.puts) != 2 || st.puts[0] != st.puts[1] || len(st.bodies) != 1 {
		t.Errorf("a row shipped twice must produce one key: %v", st.puts)
	}
}

func TestR_HA_054_DiscardDeletesWithoutObjects(t *testing.T) {
	ob := &fakeOutbox{rows: rows(2)}
	n, err := New(Options{Outbox: ob, Discard: true}).Ship(t.Context())
	if err != nil || n != 2 || len(ob.rows) != 0 {
		t.Fatalf("Ship = %d, %v, rows left %d; want the journal switched off to drain the outbox", n, err, len(ob.rows))
	}
}
