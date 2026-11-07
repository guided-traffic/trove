package audit

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/meta"
)

// countingRecorder records events one RecordTx at a time and fails on the event of a given type.
type countingRecorder struct {
	Nop
	got  []string
	fail string
}

func (r *countingRecorder) RecordTx(_ context.Context, _ meta.Tx, ev Event) error {
	if ev.Type == r.fail {
		return errors.New("refused")
	}
	r.got = append(r.got, ev.Type)
	return nil
}

// plainRecorder is a Recorder that is no BatchRecorder.
type plainRecorder struct{ inner *countingRecorder }

func (p plainRecorder) RecordTx(ctx context.Context, tx meta.Tx, ev Event) error {
	return p.inner.RecordTx(ctx, tx, ev)
}
func (plainRecorder) Record(context.Context, Event) {}

// batchSpy is a BatchRecorder that counts its calls.
type batchSpy struct {
	Nop
	calls int
	n     int
}

func (b *batchSpy) RecordAllTx(_ context.Context, _ meta.Tx, evs []Event) error {
	b.calls++
	b.n += len(evs)
	return nil
}

// notPostgres is a transaction of no PostgreSQL store: the writer refuses it after its checks.
type notPostgres struct{ meta.Tx }

func (notPostgres) After(func()) {}

// TestR_OPS_015_RecordAllTxBatchesWhereTheRecorderCan: RecordAllTx hands the whole list to a BatchRecorder in one call, and
// records one event at a time, in order and stopping at the first failure, through a recorder that cannot batch (a test
// fake). The writer and the request wrapper of the API are batch recorders (R-OPS-015, R-HA-054).
func TestR_OPS_015_RecordAllTxBatchesWhereTheRecorderCan(t *testing.T) {
	evs := []Event{{Type: "a.b"}, {Type: "c.d"}, {Type: "e.f"}}
	spy := &batchSpy{}
	if err := RecordAllTx(context.Background(), spy, nil, evs); err != nil || spy.calls != 1 || spy.n != 3 {
		t.Fatalf("batch recorder: err %v, %d calls with %d events, want one call with 3", err, spy.calls, spy.n)
	}
	inner := &countingRecorder{fail: "c.d"}
	err := RecordAllTx(context.Background(), plainRecorder{inner}, nil, evs)
	if err == nil || strings.Join(inner.got, ",") != "a.b" {
		t.Fatalf("plain recorder: err %v, recorded %v; want the first event and the failure of the second", err, inner.got)
	}
	var w any = &Writer{}
	if _, ok := w.(BatchRecorder); !ok {
		t.Fatal("the writer does not batch")
	}
}

// TestR_OPS_015_RecordAllTxChecksEveryEventBeforeItWrites: one invalid event among many fails the call before any statement
// runs: the writer reaches the transaction only when every event is valid (a missing required journal entry, an unknown
// type and an asynchronous type each stop it; R-OPS-014, R-OPS-015).
func TestR_OPS_015_RecordAllTxChecksEveryEventBeforeItWrites(t *testing.T) {
	w := newWriter(Options{}, &fakeSink{})
	actor := Actor{Kind: "user", ID: "5f0e1111-2222-4333-8444-555566667777", Name: "ann"}
	good := Event{Type: EventTokenRevoked, Actor: actor, Outcome: Success, Details: json.RawMessage(`{"reason":"admin"}`), Journal: json.RawMessage(`{"tokenId":"x","reason":"admin"}`)}
	for name, c := range map[string]struct {
		bad  Event
		want error
	}{
		"a missing required entry": {Event{Type: EventTokenRevoked, Actor: actor, Outcome: Success}, ErrJournalEntryRequired},
		"an unknown type":          {Event{Type: "nothing.here.at_all", Actor: actor}, ErrUnknownType},
		"an asynchronous type":     {Event{Type: "wtest.denied", Actor: actor}, ErrWrongMode},
	} {
		t.Run("R-OPS-015 "+name, func(t *testing.T) {
			err := w.RecordAllTx(context.Background(), notPostgres{}, []Event{good, good, c.bad, good})
			if !errors.Is(err, c.want) {
				t.Fatalf("RecordAllTx = %v, want %v before any write", err, c.want)
			}
		})
	}
	t.Run("R-OPS-015 valid events reach the transaction", func(t *testing.T) {
		err := w.RecordAllTx(context.Background(), notPostgres{}, []Event{good, good})
		if err == nil || !strings.Contains(err.Error(), "not a PostgreSQL transaction") {
			t.Fatalf("RecordAllTx = %v, want the refusal of the transaction", err)
		}
		if err := w.RecordAllTx(context.Background(), notPostgres{}, nil); err != nil {
			t.Fatalf("no events: %v", err)
		}
	})
}
