package replay

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/meta"
)

type memStore struct {
	objs    map[string][]byte
	listErr error
}

func (m *memStore) Put(_ context.Context, k string, b []byte) error { m.objs[k] = b; return nil }
func (m *memStore) List(_ context.Context, prefix string, fn func(string) error) error {
	if m.listErr != nil {
		return m.listErr
	}
	for k := range m.objs {
		if strings.HasPrefix(k, prefix) {
			if err := fn(k); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *memStore) Get(_ context.Context, k string) ([]byte, error) {
	if b, ok := m.objs[k]; ok {
		return b, nil
	}
	return nil, errors.New("missing")
}

const prefix = "trove"

func id(n int) string { return fmt.Sprintf("0192f0c4-0000-7000-8000-%012d", n) }

func put(t *testing.T, s *memStore, n int, at time.Time, event string) journal.Object {
	t.Helper()
	o := journal.Object{V: 1, ID: id(n), At: at, Event: event, Entry: []byte(`{}`)}
	b, err := o.Encode()
	if err != nil {
		t.Fatal(err)
	}
	s.objs[journal.Key(prefix, o)] = b
	return o
}

func TestR_HA_054_ReaderSortsByTimeAndIdAndDropsOlderEntries(t *testing.T) {
	s := &memStore{objs: map[string][]byte{}}
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	put(t, s, 3, now.Add(2*time.Second), "a.b")
	put(t, s, 2, now.Add(time.Second), "a.b")
	put(t, s, 1, now.Add(time.Second), "a.b") // same time as 2: id decides
	put(t, s, 9, now.Add(-time.Hour), "a.b")  // not newer than the point in time
	r := NewReader(s, prefix)
	r.now = func() time.Time { return now.Add(time.Minute) }
	got, bad, err := r.Entries(t.Context(), now)
	if err != nil || len(bad) != 0 {
		t.Fatalf("Entries = %v, %v, %v", got, bad, err)
	}
	if len(got) != 3 || got[0].ID != id(1) || got[1].ID != id(2) || got[2].ID != id(3) {
		t.Errorf("order = %v", got)
	}
}

func TestR_HA_054_ReaderReportsForgedObjects(t *testing.T) {
	s := &memStore{objs: map[string][]byte{}}
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	good := put(t, s, 1, now.Add(time.Second), "a.b")
	// An object under another entry's key.
	other := put(t, s, 2, now.Add(time.Second), "a.b")
	s.objs[journal.Key(prefix, other)] = s.objs[journal.Key(prefix, good)]
	// An object under the key of another day than its time.
	late := journal.Object{V: 1, ID: id(3), At: now.Add(48 * time.Hour), Event: "a.b", Entry: []byte(`{}`)}
	b, _ := late.Encode()
	s.objs[journal.ListPrefix(prefix)+"2026/10/28/"+id(3)+".json"] = b
	// Garbage and a key that is no journal key.
	s.objs[journal.ListPrefix(prefix)+"2026/10/28/"+id(4)+".json"] = []byte("{")
	s.objs[journal.ListPrefix(prefix)+"2026/10/28/readme.txt"] = []byte("x")
	r := NewReader(s, prefix)
	r.now = func() time.Time { return now.Add(time.Minute) }
	got, bad, err := r.Entries(t.Context(), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != id(1) {
		t.Errorf("readable = %v, want only the genuine entry", got)
	}
	if len(bad) != 4 {
		t.Errorf("unreadable = %v, want 4", bad)
	}
}

func TestR_HA_054_ReaderFailsOnAListingError(t *testing.T) {
	r := NewReader(&memStore{objs: map[string][]byte{}, listErr: errors.New("denied")}, prefix)
	if _, _, err := r.Entries(t.Context(), time.Now()); err == nil {
		t.Fatal("a journal that cannot be listed must fail the read, not return an empty journal")
	}
}

type fakeTx struct{ meta.Tx }

type fakeRunner struct{ begun int }

func (f *fakeRunner) InTx(_ context.Context, fn func(meta.Tx) error) error {
	f.begun++
	return fn(fakeTx{})
}

func TestR_HA_054_ReplayAppliesInOrderAndFailsClosed(t *testing.T) {
	var order []string
	journal.Register(journal.Handler{
		Domain: "test", Events: []string{"replaytest.ok", "replaytest.panic", "replaytest.fail"},
		Apply: func(_ context.Context, _ meta.Tx, _ journal.Env, o journal.Object) (journal.Result, error) {
			order = append(order, o.ID)
			switch o.Event {
			case "replaytest.panic":
				panic("forged entry")
			case "replaytest.fail":
				return journal.Result{}, errors.New("database down")
			}
			return o.Result(journal.Applied, "x"), nil
		},
	})
	s := &memStore{objs: map[string][]byte{}}
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	put(t, s, 2, now.Add(2*time.Second), "replaytest.panic")
	put(t, s, 1, now.Add(time.Second), "replaytest.ok")
	put(t, s, 3, now.Add(3*time.Second), "no.handler.here")
	put(t, s, 4, now.Add(4*time.Second), "replaytest.ok")
	rd := NewReader(s, prefix)
	rd.now = func() time.Time { return now.Add(time.Minute) }
	db := &fakeRunner{}
	var out bytes.Buffer
	sum, err := New(Options{DB: db, Reader: rd}).Replay(t.Context(), journal.ReplayOptions{RestoredTo: now, Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != id(1)+","+id(2)+","+id(4) {
		t.Errorf("handlers ran in %v", order)
	}
	if sum.Counts[journal.Applied] != 2 || sum.Counts[journal.Skipped] != 2 {
		t.Errorf("counts = %v, want 2 applied and 2 skipped (the panic and the unknown event)", sum.Counts)
	}
	if !strings.Contains(out.String(), "no.handler.here") || strings.Count(out.String(), "\n") != 4 {
		t.Errorf("one line per entry expected:\n%s", out.String())
	}

	// A handler error is the database failing: the replay stops with it.
	put(t, s, 5, now.Add(5*time.Second), "replaytest.fail")
	if _, err := New(Options{DB: db, Reader: rd}).Replay(t.Context(), journal.ReplayOptions{RestoredTo: now}); err == nil {
		t.Fatal("a failing handler must end the replay with an error")
	}
}
