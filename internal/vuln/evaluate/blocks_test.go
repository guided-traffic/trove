package evaluate

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

// recorderFake keeps the events the evaluation writes in its transaction.
type recorderFake struct{ events []audit.Event }

func (r *recorderFake) RecordTx(_ context.Context, _ meta.Tx, ev audit.Event) error {
	r.events = append(r.events, ev)
	return nil
}
func (r *recorderFake) Record(context.Context, audit.Event) {}

func blockRun(rec audit.Recorder, enforcement vuln.Enforcement) *run {
	return &run{s: &Service{o: Options{Recorder: rec, Enabled: true, Enforcement: enforcement}}}
}

func blockView(blockMalicious bool) repoView {
	st := vuln.DefaultSettings()
	st.BlockMalicious = blockMalicious
	return repoView{ID: 12, Key: "web/npm-remote", Ecosystem: "npm", Settings: st}
}

var blockVersion = store.StoredVersion{Name: "left-pad", Version: "1.3.0"}

// R-VULN-012, Q-VULN-130: a change of the evaluation records vulnerability.finding.status_changed with block set, its cause and
// the journal entry when the finding blocks its version after the change and did not before; any other change records nothing.
func TestR_VULN_012_EveryEvaluationChangeThatSetsABlockIsJournaled(t *testing.T) {
	mal := blockFacts{ID: "0192f0c4-0000-7000-8000-000000000f01", AdvisoryID: "MAL-2026-1", Kind: advisory.KindMalicious, Severity: advisory.SeverityCritical}
	plain := blockFacts{ID: "0192f0c4-0000-7000-8000-000000000f02", AdvisoryID: "GHSA-aaaa-bbbb-cccc", Kind: advisory.KindVulnerability, Severity: advisory.SeverityHigh}
	was := func(kind advisory.Kind, status vuln.Status) *store.Finding {
		return &store.Finding{Kind: kind, Status: status}
	}
	for _, c := range []struct {
		name           string
		blockMalicious bool
		enforcement    vuln.Enforcement
		facts          blockFacts
		was            *store.Finding
		from, to       vuln.Status
		cause          string
		want           bool
	}{
		{"a malicious detection with blockMalicious on", true, vuln.EnforceBlocks, mal, nil, "", vuln.StatusNeedsTriage, causeDetected, true},
		{"a malicious detection with blockMalicious off", false, vuln.EnforceBlocks, mal, nil, "", vuln.StatusNeedsTriage, causeDetected, false},
		{"a malicious detection in the mode report", true, vuln.EnforceReport, mal, nil, "", vuln.StatusNeedsTriage, causeDetected, false},
		{"an ordinary detection", true, vuln.EnforceBlocks, plain, nil, "", vuln.StatusNeedsTriage, causeDetected, false},
		{"a kind that turned malicious", true, vuln.EnforceBlocks, mal, was(advisory.KindVulnerability, vuln.StatusNeedsTriage), vuln.StatusNeedsTriage, vuln.StatusNeedsTriage, causeKindChanged, true},
		{"a kind that turned malicious on a confirmed finding", true, vuln.EnforceBlocks, mal, was(advisory.KindVulnerability, vuln.StatusConfirmed), vuln.StatusConfirmed, vuln.StatusConfirmed, causeKindChanged, true},
		{"a kind that turned malicious with blockMalicious off", false, vuln.EnforceBlocks, mal, was(advisory.KindVulnerability, vuln.StatusNeedsTriage), vuln.StatusNeedsTriage, vuln.StatusNeedsTriage, causeKindChanged, false},
		{"a kind that turned malicious on a finding that was blocked already", true, vuln.EnforceBlocks, mal, was(advisory.KindVulnerability, vuln.StatusBlocked), vuln.StatusBlocked, vuln.StatusBlocked, causeKindChanged, false},
		{"a kind that was malicious already", true, vuln.EnforceBlocks, mal, was(advisory.KindMalicious, vuln.StatusNeedsTriage), vuln.StatusNeedsTriage, vuln.StatusNeedsTriage, causeKindChanged, false},
		{"a re-detection to blocked", true, vuln.EnforceBlocks, plain, was(advisory.KindVulnerability, vuln.StatusResolved), vuln.StatusResolved, vuln.StatusBlocked, causeRedetected, true},
		{"a re-detection of an open malicious finding", true, vuln.EnforceBlocks, mal, was(advisory.KindMalicious, vuln.StatusResolved), vuln.StatusResolved, vuln.StatusNeedsTriage, causeRedetected, true},
		{"a re-detection of an ordinary open finding", true, vuln.EnforceBlocks, plain, was(advisory.KindVulnerability, vuln.StatusResolved), vuln.StatusResolved, vuln.StatusConfirmed, causeRedetected, false},
		{"a re-detection to dismissed", true, vuln.EnforceBlocks, mal, was(advisory.KindMalicious, vuln.StatusResolved), vuln.StatusResolved, vuln.StatusDismissed, causeRedetected, false},
		{"a re-detection to blocked in the mode report", true, vuln.EnforceReport, plain, was(advisory.KindVulnerability, vuln.StatusResolved), vuln.StatusResolved, vuln.StatusBlocked, causeRedetected, false},
		{"a lift", true, vuln.EnforceBlocks, plain, was(advisory.KindVulnerability, vuln.StatusBlocked), vuln.StatusBlocked, vuln.StatusConfirmed, causeRedetected, false},
	} {
		t.Run("R-VULN-012 "+c.name, func(t *testing.T) {
			rec := &recorderFake{}
			err := blockRun(rec, c.enforcement).recordBlockSet(context.Background(), blockView(c.blockMalicious), blockVersion, c.facts, c.was, c.from, c.to, c.cause)
			if err != nil {
				t.Fatal(err)
			}
			if got := len(rec.events); got != map[bool]int{true: 1, false: 0}[c.want] {
				t.Fatalf("%d events, want %v", got, c.want)
			}
			if !c.want {
				return
			}
			ev := rec.events[0]
			if ev.Type != audit.EventVulnFindingStatusChanged || ev.Repository != "web/npm-remote" || ev.Subject.ID != c.facts.ID || ev.Actor.Kind != "system" {
				t.Errorf("event = %+v", ev)
			}
			var d map[string]string
			if err := json.Unmarshal(ev.Details, &d); err != nil {
				t.Fatal(err)
			}
			if d["block"] != "set" || d["cause"] != c.cause || d["from"] != string(c.from) || d["to"] != string(c.to) || d["finding"] != c.facts.ID ||
				d["advisory"] != c.facts.AdvisoryID || d["package"] != "left-pad" || d["version"] != "1.3.0" {
				t.Errorf("details = %v", d)
			}
			var e vuln.BlockEntry
			if err := json.Unmarshal(ev.Journal, &e); err != nil {
				t.Fatal(err)
			}
			want := vuln.NewBlockEntry(c.facts.ID, 12, "web/npm-remote", "npm", "left-pad", "1.3.0", c.facts.AdvisoryID, string(c.facts.Kind), string(c.facts.Severity))
			if e != want {
				t.Errorf("entry = %+v, want %+v", e, want)
			}
		})
	}
	t.Run("R-VULN-012 no recorder, no event", func(t *testing.T) {
		if err := blockRun(nil, vuln.EnforceBlocks).recordBlockSet(context.Background(), blockView(true), blockVersion, mal, nil, "", vuln.StatusNeedsTriage, causeDetected); err != nil {
			t.Fatal(err)
		}
	})
}

// R-VULN-012, Q-VULN-130: the event of the evaluation validates against the registered schema of the type, with the cause.
func TestR_VULN_012_TheEvaluationsBlockEventFitsTheRegisteredType(t *testing.T) {
	typ, ok := audit.Lookup(audit.EventVulnFindingStatusChanged)
	if !ok || typ.Journal != audit.JournalRevocation {
		t.Fatalf("type = %+v, %v", typ, ok)
	}
	for _, cause := range []string{causeDetected, causeKindChanged, causeRedetected, causeMerged} {
		ev, err := blockEvent(blockView(true), blockVersion, blockFacts{ID: "f", AdvisoryID: "A", Kind: advisory.KindMalicious, Severity: advisory.SeverityHigh}, "", vuln.StatusNeedsTriage, cause)
		if err != nil {
			t.Fatal(err)
		}
		var d map[string]any
		if err := json.Unmarshal(ev.Details, &d); err != nil {
			t.Fatal(err)
		}
		for k := range d {
			if !containsKey(typ.Schema, k) {
				t.Errorf("the schema of the type does not list the member %q", k)
			}
		}
		if d["cause"] != cause {
			t.Errorf("cause = %v", d["cause"])
		}
	}
}

func containsKey(schema, key string) bool {
	var s struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if json.Unmarshal([]byte(schema), &s) != nil {
		return false
	}
	_, ok := s.Properties[key]
	return ok
}

// R-VULN-015, Q-VULN-132: a blocked finding whose version is gone counts at the remote miss while the ecosystem's data is
// incomplete, and while it has not synced since the last restore; after that, and when it was never restored, the advisory data
// decides.
func TestR_VULN_015_AnEvictedBlockCountsUntilTheEcosystemHasSyncedSinceTheRestore(t *testing.T) {
	pol := vuln.PolicyFor(vuln.DefaultSettings(), "npm", true, vuln.EnforceBlocks)
	evicted := store.Finding{
		ID: "0192f0c4-0000-7000-8000-000000000f01", AdvisoryID: "GHSA-aaaa-bbbb-cccc", Kind: advisory.KindVulnerability, Severity: advisory.SeverityHigh,
		Status: vuln.StatusResolved, ResolvedReason: vuln.ResolvedVersionRemoved, BeforeResolution: vuln.StatusBlocked,
	}
	other := evicted
	other.ResolvedReason = vuln.ResolvedNoLongerAffected
	confirmed := evicted
	confirmed.BeforeResolution = vuln.StatusConfirmed
	for _, c := range []struct {
		name string
		f    store.Finding
		data advisory.DataState
		want bool
	}{
		{"incomplete data", evicted, advisory.DataState{}, true},
		{"incomplete data of a restored installation", evicted, advisory.DataState{NotSyncedSinceRestore: true}, true},
		{"complete data older than the restore", evicted, advisory.DataState{Complete: true, NotSyncedSinceRestore: true}, true},
		{"complete data that synced after the restore", evicted, advisory.DataState{Complete: true}, false},
		{"complete data of an installation never restored", evicted, advisory.DataState{Complete: true}, false},
		{"a resolution for another reason", other, advisory.DataState{Complete: true, NotSyncedSinceRestore: true}, false},
		{"a finding that was confirmed, not blocked", confirmed, advisory.DataState{Complete: true, NotSyncedSinceRestore: true}, false},
	} {
		t.Run("R-VULN-015 "+c.name, func(t *testing.T) {
			b := missVerdict(pol, missInput{existing: []store.Finding{c.f}}, c.data)
			if got := b.Refused(); got != c.want {
				t.Fatalf("verdict = %+v, refused %v, want %v", b, got, c.want)
			}
			if c.want && (b.Reason != vuln.BlockTriage || b.FindingID != c.f.ID) {
				t.Errorf("verdict = %+v", b)
			}
		})
	}
}

func TestR_VULN_015_HoldsEvictedBlocks(t *testing.T) {
	for _, c := range []struct{ complete, notSynced, want bool }{
		{false, false, true}, {false, true, true}, {true, true, true}, {true, false, false},
	} {
		if got := holdsEvictedBlocks(c.complete, c.notSynced); got != c.want {
			t.Errorf("holdsEvictedBlocks(complete %v, not synced %v) = %v", c.complete, c.notSynced, got)
		}
	}
}

// R-VULN-012 (Q-VULN-130): a merge that blocks a finding and the upsert after it that changes it again record one block, not two.
func TestR_VULN_012_ABlockOfOneFindingIsRecordedOncePerRun(t *testing.T) {
	rec := &recorderFake{}
	r := blockRun(rec, vuln.EnforceBlocks)
	mal := blockFacts{ID: "0192f0c4-0000-7000-8000-000000000f01", AdvisoryID: "MAL-2026-1", Kind: advisory.KindMalicious, Severity: advisory.SeverityCritical}
	was := &store.Finding{Kind: advisory.KindVulnerability, Status: vuln.StatusNeedsTriage}
	ev, err := blockEvent(blockView(true), blockVersion, mal, vuln.StatusNeedsTriage, vuln.StatusNeedsTriage, causeMerged)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.recordOnce(context.Background(), mal.ID, ev); err != nil { // the merge
		t.Fatal(err)
	}
	if err := r.recordBlockSet(context.Background(), blockView(true), blockVersion, mal, was, vuln.StatusNeedsTriage, vuln.StatusNeedsTriage, causeKindChanged); err != nil { // the upsert
		t.Fatal(err)
	}
	if len(rec.events) != 1 || rec.events[0].Details == nil {
		t.Fatalf("%d events, want the one of the merge", len(rec.events))
	}
	other := mal
	other.ID = "0192f0c4-0000-7000-8000-000000000f02"
	if err := r.recordBlockSet(context.Background(), blockView(true), blockVersion, other, nil, "", vuln.StatusNeedsTriage, causeDetected); err != nil || len(rec.events) != 2 {
		t.Fatalf("another finding of the run: %d events, %v", len(rec.events), err)
	}
}
