package triage

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

// The replay of the download blocks of the revocation journal (R-HA-054, R-VULN-013, H-489). Its writes run against
// PostgreSQL in test/integration/backup/journal_vuln_replay_test.go.

func TestR_VULN_013_AReplayedBlockKeepsTheBlockAndFailsClosed(t *testing.T) {
	for status, want := range map[vuln.Status]journal.Outcome{
		vuln.StatusBlocked: journal.AlreadyInEffect,
		// The block the entry records was set after the restore point; a status without a block is the state before it,
		// or a lift after it that the journal does not record, so the replay sets the block again (Q-VULN-114).
		vuln.StatusConfirmed:   journal.Applied,
		vuln.StatusNeedsTriage: journal.Applied,
		vuln.StatusDismissed:   journal.Applied,
		vuln.StatusResolved:    journal.Applied,
	} {
		t.Run("R-VULN-013 a restored "+string(status)+" finding", func(t *testing.T) {
			if got := blockOutcome(status); got != want {
				t.Fatalf("blockOutcome(%s) = %s, want %s", status, got, want)
			}
		})
	}
}

// recorder keeps the events of the transactional writer.
type recorder struct{ events []audit.Event }

func (r *recorder) RecordTx(_ context.Context, _ meta.Tx, ev audit.Event) error {
	r.events = append(r.events, ev)
	return nil
}
func (r *recorder) Record(context.Context, audit.Event) {}

func TestR_VULN_013_TheProducersEntryIsTheOneTheReplayReads(t *testing.T) {
	rec := &recorder{}
	s := &Service{o: Options{Recorder: rec}}
	l := store.Locked{
		ID: "0192f0c4-0000-7000-8000-000000000f01", Repository: "web/npm-remote", Ecosystem: "npm", Package: "left-pad", Version: "1.3.0",
		Advisory: "GHSA-aaaa-bbbb-cccc", Kind: advisory.KindVulnerability, Status: vuln.StatusConfirmed,
	}
	if err := s.record(context.Background(), nil, Actor{}, l, vuln.StatusBlocked, "", "", BlockSet); err != nil {
		t.Fatal(err)
	}
	if len(rec.events) != 1 || rec.events[0].Journal == nil {
		t.Fatalf("events = %+v", rec.events)
	}
	o := journal.Object{V: 1, ID: "0192f0c4-0000-7000-8000-000000000e01", Event: audit.EventVulnFindingStatusChanged, Entry: rec.events[0].Journal}
	e, ok := readBlockEntry(o)
	if !ok || e.Finding != l.ID || e.Ecosystem != "npm" || e.Package != "left-pad" || e.Version != "1.3.0" {
		t.Fatalf("readBlockEntry = %+v, %v", e, ok)
	}
	if err := s.record(context.Background(), nil, Actor{}, l, vuln.StatusConfirmed, "", "", BlockLifted); err != nil || rec.events[1].Journal != nil {
		t.Fatalf("a lift is journaled: %s, %v", rec.events[1].Journal, err)
	}
}

func TestR_VULN_013_AMalformedBlockEntryIsSkipped(t *testing.T) {
	good := map[string]string{"finding": "0192f0c4-0000-7000-8000-000000000f01", "ecosystem": "npm", "package": "left-pad", "version": "1.3.0", "block": "set"}
	for name, f := range map[string]func(m map[string]string){
		"no finding ID":       func(m map[string]string) { m["finding"] = "f-1" },
		"a lifted block":      func(m map[string]string) { m["block"] = "lifted" },
		"no block":            func(m map[string]string) { delete(m, "block") },
		"no package":          func(m map[string]string) { delete(m, "package") },
		"no version":          func(m map[string]string) { delete(m, "version") },
		"no ecosystem":        func(m map[string]string) { delete(m, "ecosystem") },
		"another member type": nil,
	} {
		t.Run("R-HA-054 "+name, func(t *testing.T) {
			var raw []byte
			if f == nil {
				raw = []byte(`{"finding": 7}`)
			} else {
				m := map[string]string{}
				for k, v := range good {
					m[k] = v
				}
				f(m)
				raw, _ = json.Marshal(m)
			}
			if _, ok := readBlockEntry(journal.Object{Entry: raw}); ok {
				t.Fatalf("%s read as an entry", raw)
			}
		})
	}
	raw, _ := json.Marshal(good)
	if _, ok := readBlockEntry(journal.Object{Entry: raw}); !ok {
		t.Fatal("the complete entry must read")
	}
}

func TestR_VULN_013_TheHandlerIsRegistered(t *testing.T) {
	for _, ev := range []string{audit.EventVulnFindingStatusChanged, audit.EventVulnFindingReopened} {
		if h, ok := journal.HandlerFor(ev); !ok || h.Domain != "vuln" {
			t.Errorf("HandlerFor(%s) = %q, %v", ev, h.Domain, ok)
		}
	}
}
