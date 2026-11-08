package triage

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

// The replay of the download blocks of the revocation journal (R-HA-054, R-VULN-013, R-VULN-026). Its writes run against
// PostgreSQL in test/integration/backup/journal_vuln_replay_test.go.

func TestR_VULN_013_AReplayedBlockKeepsTheBlockAndFailsClosed(t *testing.T) {
	const version, pkg = int64(7), int64(3)
	for _, c := range []struct {
		name     string
		f        restoredFinding
		versions [2]int64
		want     journal.Outcome
	}{
		// A stored version: the shape the download's join reads is blocked and linked (Q-VULN-131 step 4).
		{"blocked and linked", restoredFinding{Status: vuln.StatusBlocked, VersionID: version, Package: pkg}, [2]int64{version, pkg}, journal.AlreadyInEffect},
		{"blocked and not linked", restoredFinding{Status: vuln.StatusBlocked}, [2]int64{version, pkg}, journal.Applied},
		{"blocked, linked to another version", restoredFinding{Status: vuln.StatusBlocked, VersionID: 9, Package: pkg}, [2]int64{version, pkg}, journal.Applied},
		// The block the entry records was set after the restore point; a status without a block is the state before it, or a lift
		// after it that the journal does not record, so the replay sets the block again (Q-VULN-114).
		{"confirmed", restoredFinding{Status: vuln.StatusConfirmed, VersionID: version, Package: pkg}, [2]int64{version, pkg}, journal.Applied},
		{"needs_triage", restoredFinding{Status: vuln.StatusNeedsTriage, VersionID: version, Package: pkg}, [2]int64{version, pkg}, journal.Applied},
		{"dismissed", restoredFinding{Status: vuln.StatusDismissed, VersionID: version, Package: pkg}, [2]int64{version, pkg}, journal.Applied},
		{"resolved", restoredFinding{Status: vuln.StatusResolved, VersionID: version, Package: pkg}, [2]int64{version, pkg}, journal.Applied},
		// A version the restored repository does not store: the shape the miss and a commit read is resolved while blocked.
		{"evicted block", restoredFinding{Status: vuln.StatusResolved, ResolvedReason: "version_removed", BeforeResolution: "blocked"}, [2]int64{}, journal.AlreadyInEffect},
		{"resolved for another reason", restoredFinding{Status: vuln.StatusResolved, ResolvedReason: "no_longer_affected", BeforeResolution: "blocked"}, [2]int64{}, journal.Applied},
		{"evicted but before confirmed", restoredFinding{Status: vuln.StatusResolved, ResolvedReason: "version_removed", BeforeResolution: "confirmed"}, [2]int64{}, journal.Applied},
		// The defect of Q-VULN-114 as it was: a blocked row without a version link is neither read by the join nor by the miss.
		{"blocked, version not stored", restoredFinding{Status: vuln.StatusBlocked}, [2]int64{}, journal.Applied},
	} {
		t.Run("R-VULN-013 a restored finding that is "+c.name, func(t *testing.T) {
			if got := blockOutcome(c.f, c.versions[0], c.versions[1]); got != c.want {
				t.Fatalf("blockOutcome = %s, want %s", got, c.want)
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
		ID: "0192f0c4-0000-7000-8000-000000000f01", RepoID: 12, Repository: "web/npm-remote", Ecosystem: "npm", Package: "left-pad", Version: "1.3.0",
		Advisory: "GHSA-aaaa-bbbb-cccc", Kind: advisory.KindVulnerability, Severity: "high", Status: vuln.StatusConfirmed,
	}
	if err := s.record(context.Background(), nil, Actor{}, l, vuln.StatusBlocked, "", "", BlockSet); err != nil {
		t.Fatal(err)
	}
	if len(rec.events) != 1 || rec.events[0].Journal == nil {
		t.Fatalf("events = %+v", rec.events)
	}
	o := journal.Object{V: 1, ID: "0192f0c4-0000-7000-8000-000000000e01", Event: audit.EventVulnFindingStatusChanged, Entry: rec.events[0].Journal}
	e, ok := readBlockEntry(o)
	if !ok || e.Finding != l.ID || e.RepositoryID != "12" || e.Repository != "web/npm-remote" || e.Ecosystem != "npm" || e.Package != "left-pad" ||
		e.Version != "1.3.0" || e.Advisory != l.Advisory || e.Kind != "vulnerability" || e.Severity != "high" {
		t.Fatalf("readBlockEntry = %+v, %v", e, ok)
	}
	if err := s.record(context.Background(), nil, Actor{}, l, vuln.StatusConfirmed, "", "", BlockLifted); err != nil || rec.events[1].Journal != nil {
		t.Fatalf("a lift is journaled: %s, %v", rec.events[1].Journal, err)
	}
}

// goodEntry is the entry every producer writes (vuln.NewBlockEntry), as the map of its members.
func goodEntry() map[string]string {
	return map[string]string{
		"finding": "0192f0c4-0000-7000-8000-000000000f01", "repositoryId": "12", "repository": "web/npm-remote", "ecosystem": "npm",
		"package": "left-pad", "version": "1.3.0", "advisory": "GHSA-aaaa-bbbb-cccc", "kind": "malicious", "severity": "high", "block": "set",
	}
}

func TestR_VULN_026_AReplayedBlockEntryIsReadStrictly(t *testing.T) {
	for name, f := range map[string]func(m map[string]string){
		"no finding ID":                       func(m map[string]string) { m["finding"] = "f-1" },
		"an upper case finding ID":            func(m map[string]string) { m["finding"] = "0192F0C4-0000-7000-8000-000000000F01" },
		"a lifted block":                      func(m map[string]string) { m["block"] = "lifted" },
		"no block":                            func(m map[string]string) { delete(m, "block") },
		"no package":                          func(m map[string]string) { delete(m, "package") },
		"no version":                          func(m map[string]string) { delete(m, "version") },
		"no ecosystem":                        func(m map[string]string) { delete(m, "ecosystem") },
		"no repository ID":                    func(m map[string]string) { delete(m, "repositoryId") },
		"a repository ID of zero":             func(m map[string]string) { m["repositoryId"] = "0" },
		"a negative repository ID":            func(m map[string]string) { m["repositoryId"] = "-4" },
		"a repository ID that is no number":   func(m map[string]string) { m["repositoryId"] = "web" },
		"no repository":                       func(m map[string]string) { delete(m, "repository") },
		"no advisory":                         func(m map[string]string) { delete(m, "advisory") },
		"no kind":                             func(m map[string]string) { delete(m, "kind") },
		"a kind that is none":                 func(m map[string]string) { m["kind"] = "worm" },
		"no severity":                         func(m map[string]string) { delete(m, "severity") },
		"a severity that is none":             func(m map[string]string) { m["severity"] = "catastrophic" },
		"a NUL in the package":                func(m map[string]string) { m["package"] = "left\x00pad" },
		"a line break in the version":         func(m map[string]string) { m["version"] = "1.0.0\n2" },
		"a control character in the advisory": func(m map[string]string) { m["advisory"] = "GHSA\x1b[0m" },
		"a package that is too long":          func(m map[string]string) { m["package"] = strings.Repeat("a", maxPackage+1) },
		"a version that is too long":          func(m map[string]string) { m["version"] = strings.Repeat("1", maxVersion+1) },
		"an ecosystem that is too long":       func(m map[string]string) { m["ecosystem"] = strings.Repeat("n", maxEcosystem+1) },
		"another member type":                 nil,
	} {
		t.Run("R-VULN-026 "+name, func(t *testing.T) {
			var raw []byte
			if f == nil {
				raw = []byte(`{"finding": 7}`)
			} else {
				m := goodEntry()
				f(m)
				raw, _ = json.Marshal(m)
			}
			if _, ok := readBlockEntry(journal.Object{Entry: raw}); ok {
				t.Fatalf("%s read as an entry", raw)
			}
		})
	}
	raw, _ := json.Marshal(goodEntry())
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
