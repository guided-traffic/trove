//go:build integration

package vulnfindings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
	"github.com/guided-traffic/trove/internal/vuln/evaluate"
	"github.com/guided-traffic/trove/internal/vuln/store"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// The download blocks the evaluation sets are journaled in the transaction of the change (R-VULN-012, R-HA-054, Q-VULN-130), and
// a restore replays them before the first sync (R-VULN-026, Q-VULN-131, Q-VULN-132).

const statusChanged = "vulnerability.finding.status_changed"

// outboxEntries lists the journal entries of an event in the order they were written.
func (e *env) outboxEntries(event string) []map[string]string {
	e.t.Helper()
	raw := e.st.Text(`SELECT COALESCE(jsonb_agg(entry ORDER BY at, id), '[]'::jsonb)::text FROM revocation_outbox WHERE event = $1`, event)
	var out []map[string]string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		e.t.Fatal(err)
	}
	return out
}

// blockDetails lists the details of the audit events that set a block, in order.
func (e *env) blockDetails() []map[string]string {
	e.t.Helper()
	raw := e.st.Text(`SELECT COALESCE(jsonb_agg(details ORDER BY occurred_at, id), '[]'::jsonb)::text FROM audit_event WHERE action = $1 AND details->>'block' = 'set'`, statusChanged)
	var out []map[string]string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		e.t.Fatal(err)
	}
	return out
}

// requireBlockEntry checks that an entry is the one the producers share, for the finding of the repository, package and version.
func (e *env) requireBlockEntry(entry map[string]string, repo, name, version, advisoryID, kind string) {
	e.t.Helper()
	id := e.st.Text(`SELECT f.id::text FROM vuln_finding f JOIN repositories r ON r.id = f.repo_id WHERE r.key = $1 AND f.package_name = $2 AND f.version = $3 AND f.advisory_id = $4`,
		repo, name, version, advisoryID)
	want := map[string]string{
		"finding": id, "repositoryId": fmt.Sprint(e.st.Number(`SELECT id FROM repositories WHERE key = $1`, repo)), "repository": "it/" + repo,
		"ecosystem": "npm", "package": name, "version": version, "advisory": advisoryID, "kind": kind,
		"severity": e.st.Text(`SELECT severity FROM vuln_finding WHERE id = $1::uuid`, id), "block": "set",
	}
	if fmt.Sprint(entry) != fmt.Sprint(want) {
		e.t.Errorf("entry = %v, want %v", entry, want)
	}
}

// R-VULN-012, R-HA-054 (Q-VULN-130): a malicious version detected for the first time blocks under blockMalicious, and the change
// records the event and the journal entry; an ordinary detection records none.
func TestR_VULN_012_AMaliciousDetectionIsJournaledInItsTransaction(t *testing.T) {
	e := newEnv(t)
	e.setComplete("npm", true)
	e.addAdvisory("npm", "MAL-2026-0101", "", "malicious", "critical")
	e.addAffected("npm", "MAL-2026-0101", 0, "trove-test-mal", everyVersionRange, nil, true, nil)
	e.addAdvisory("npm", "GHSA-aaaa-aaaa-0101", "", "vulnerability", "high")
	e.addAffected("npm", "GHSA-aaaa-aaaa-0101", 0, "trove-test-plain", everyVersionRange, nil, true, nil)
	e.publish("npm-vendor", "trove-test-plain", "1.0.0")
	if n := len(e.outboxEntries(statusChanged)); n != 0 {
		t.Fatalf("%d entries after an ordinary detection, want none", n)
	}
	e.publish("npm-vendor", "trove-test-mal", "1.0.0")

	entries := e.outboxEntries(statusChanged)
	if len(entries) != 1 {
		t.Fatalf("%d entries after a malicious detection, want 1", len(entries))
	}
	e.requireBlockEntry(entries[0], "npm-vendor", "trove-test-mal", "1.0.0", "MAL-2026-0101", "malicious")
	details := e.blockDetails()
	if len(details) != 1 || details[0]["cause"] != "detected" || details[0]["from"] != "" || details[0]["to"] != "needs_triage" || details[0]["finding"] != entries[0]["finding"] {
		t.Errorf("audit details = %v", details)
	}
	if got := e.st.Text(`SELECT repository FROM audit_event WHERE action = $1`, statusChanged); got != "it/npm-vendor" {
		t.Errorf("the event's repository is %q", got)
	}

	t.Run("R-HA-054 a rolled back evaluation journals nothing, a committed one journals once", func(t *testing.T) {
		e := newEnv(t)
		e.setComplete("npm", true)
		e.publish("npm-vendor", "trove-test-mal", "1.0.0") // no advisory yet: no finding
		e.addAdvisory("npm", "MAL-2026-0101", "", "malicious", "critical")
		e.addAffected("npm", "MAL-2026-0101", 0, "trove-test-mal", everyVersionRange, nil, true, nil)
		svc, db := e.recordingService()
		itemID := e.st.Number(`SELECT pv.lead_item_id FROM package_versions pv JOIN packages p ON p.id = pv.package_id WHERE p.name = 'trove-test-mal'`)
		repoID := e.st.Number(`SELECT id FROM repositories WHERE key = 'npm-vendor'`)
		commit := func(fail bool) {
			t.Helper()
			err := db.InTx(context.Background(), func(tx meta.Tx) error {
				if err := svc.OnVersionCommitted(context.Background(), tx, vuln.CommittedVersion{
					RepoID: repoID, Repo: qr("npm-vendor"), Format: "npm", ItemID: itemID, Trigger: vuln.TriggerPublish,
				}); err != nil {
					return err
				}
				if fail {
					return errBoom
				}
				return nil
			})
			if fail != errors.Is(err, errBoom) || (!fail && err != nil) {
				t.Fatalf("commit(fail %v) = %v", fail, err)
			}
		}
		commit(true)
		if n := len(e.outboxEntries(statusChanged)); n != 0 || e.findings("npm-vendor", "trove-test-mal", "1.0.0", "") != 0 || e.events(statusChanged) != 0 {
			t.Fatalf("the rolled back transaction left %d entries, %d findings, %d events", n, e.findings("npm-vendor", "trove-test-mal", "1.0.0", ""), e.events(statusChanged))
		}
		commit(false)
		entries := e.outboxEntries(statusChanged)
		if len(entries) != 1 || e.events(statusChanged) != 1 {
			t.Fatalf("%d entries and %d events after the commit, want 1 and 1", len(entries), e.events(statusChanged))
		}
		e.requireBlockEntry(entries[0], "npm-vendor", "trove-test-mal", "1.0.0", "MAL-2026-0101", "malicious")
	})

	for name, setup := range map[string]func(t *testing.T) *env{
		"blockMalicious off": func(t *testing.T) *env {
			e := newEnv(t)
			if a := e.patch("npm-vendor", `{"vulnerabilities":{"blockMalicious":false}}`); a.Status != 200 {
				t.Fatalf("patch: %d %s", a.Status, a.Body)
			}
			waitSnapshot(t, e, func() bool {
				r, err := e.rep.App.Repositories().ByName(context.Background(), qr("npm-vendor"))
				return err == nil && !r.VulnerabilitySettings().BlockMalicious
			})
			return e
		},
		"the mode report": func(t *testing.T) *env { return newEnv(t, "TROVE_VULNERABILITIES_ENFORCEMENT=report") },
	} {
		t.Run("R-VULN-012 "+name+" journals no detection", func(t *testing.T) {
			e := setup(t)
			e.setComplete("npm", true)
			e.addAdvisory("npm", "MAL-2026-0101", "", "malicious", "critical")
			e.addAffected("npm", "MAL-2026-0101", 0, "trove-test-mal", everyVersionRange, nil, true, nil)
			e.publish("npm-vendor", "trove-test-mal", "1.0.0")
			if e.findings("npm-vendor", "trove-test-mal", "1.0.0", "") != 1 {
				t.Fatal("the version was not detected")
			}
			if n := len(e.outboxEntries(statusChanged)); n != 0 || e.events(statusChanged) != 0 {
				t.Errorf("%d entries and %d events for a change that sets no block", n, e.events(statusChanged))
			}
		})
	}
}

var errBoom = errors.New("rolled back by the test")

// recordingService is the evaluation over its own pool with the audit writer, so that a test drives the commit hook in a
// transaction it can roll back.
func (e *env) recordingService() (*evaluate.Service, *postgres.DB) {
	e.t.Helper()
	db, err := postgres.Open(context.Background(), e.st.DatabaseConfig(), "vulnfind-rec", "web", testenv.Quiet())
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(db.Close)
	w, err := audit.New(audit.Options{DB: db, Replica: "vulnfind-rec", Config: config.Audit{AsyncBuffer: 1000}, Log: testenv.Quiet(), AppLog: testenv.Quiet()})
	if err != nil {
		e.t.Fatal(err)
	}
	svc, err := evaluate.New(evaluate.Options{
		Runner: db, Repos: e.rep.App.Repositories(), Ecosystems: ecosystems(), Enabled: true, Recorder: w, States: fixedStates{"npm": {Complete: true}},
		PublicUpstreams: map[string][]string{"npm": {"registry.npmjs.org"}},
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return svc, db
}

// R-VULN-012, R-HA-054 (Q-VULN-130): an open finding whose advisory turns malicious blocks under blockMalicious; the change records
// the event with the cause kind_changed (from equals to) and the entry.
func TestR_VULN_012_AnAdvisoryThatTurnsMaliciousIsJournaled(t *testing.T) {
	e := newEnv(t)
	e.setComplete("npm", true)
	e.addAdvisory("npm", "GHSA-aaaa-aaaa-0102", "", "vulnerability", "high")
	e.addAffected("npm", "GHSA-aaaa-aaaa-0102", 0, "trove-test-turn", everyVersionRange, nil, true, nil)
	e.publish("npm-vendor", "trove-test-turn", "1.0.0")
	if n := len(e.outboxEntries(statusChanged)); n != 0 {
		t.Fatalf("%d entries before the advisory turned malicious", n)
	}

	e.st.Exec(`UPDATE vuln_advisory SET kind = 'malicious', severity = 'critical', modified = now() + interval '1 hour' WHERE id = 'GHSA-aaaa-aaaa-0102'`)
	e.queue("npm", "GHSA-aaaa-aaaa-0102")
	e.evaluate()

	entries := e.outboxEntries(statusChanged)
	if len(entries) != 1 {
		t.Fatalf("%d entries after the kind changed, want 1", len(entries))
	}
	e.requireBlockEntry(entries[0], "npm-vendor", "trove-test-turn", "1.0.0", "GHSA-aaaa-aaaa-0102", "malicious")
	if entries[0]["severity"] != "critical" {
		t.Errorf("the entry carries severity %q, the finding's level after the change is critical", entries[0]["severity"])
	}
	details := e.blockDetails()
	if len(details) != 1 || details[0]["cause"] != "kind_changed" || details[0]["from"] != "needs_triage" || details[0]["to"] != "needs_triage" {
		t.Errorf("audit details = %v", details)
	}
	// Evaluating again changes nothing and journals nothing.
	e.queue("npm", "GHSA-aaaa-aaaa-0102")
	e.evaluate()
	if n := len(e.outboxEntries(statusChanged)); n != 1 {
		t.Errorf("%d entries after a second evaluation, want 1", n)
	}
}

// R-VULN-012, R-HA-054 (Q-VULN-130): a merge of two findings whose stronger status blocks records the event with the cause merged
// and the entry. The finding of the record that joined the group had been blocked and its version evicted; the surviving finding
// is open, so the merge is what blocks the version again.
func TestR_VULN_012_AMergeThatSetsABlockIsJournaled(t *testing.T) {
	e := newEnv(t)
	e.setComplete("npm", true)
	for _, id := range []string{"GHSA-aaaa-aaaa-2101", "GHSA-aaaa-aaaa-2102"} {
		e.addAdvisory("npm", id, "", "vulnerability", "high")
		e.addAffected("npm", id, 0, "trove-test-merged", fixedRange("SEMVER", "1.0.1"), nil, false, nil)
	}
	e.addVersion("npm-vendor", "trove-test-merged", "1.0.0")
	e.rescan("npm-vendor")
	e.st.Exec(`UPDATE vuln_finding SET status = 'resolved', status_before_resolution = 'blocked', resolved_reason = 'version_removed', resolved_at = now()
		WHERE advisory_id = 'GHSA-aaaa-aaaa-2102'`)
	before := len(e.outboxEntries(statusChanged))

	e.st.Exec(`UPDATE vuln_advisory SET canonical_id = 'GHSA-aaaa-aaaa-2101', aliases = ARRAY['GHSA-aaaa-aaaa-2101'], modified = now() + interval '1 hour'
		WHERE id = 'GHSA-aaaa-aaaa-2102'`)
	e.queue("npm", "GHSA-aaaa-aaaa-2101", "GHSA-aaaa-aaaa-2102")
	e.evaluate()

	if got := e.status("npm-vendor", "trove-test-merged", "1.0.0"); got != "blocked" {
		t.Fatalf("the merged finding is %q, want blocked", got)
	}
	entries := e.outboxEntries(statusChanged)
	if len(entries) != before+1 {
		t.Fatalf("%d entries after the merge, want %d", len(entries), before+1)
	}
	e.requireBlockEntry(entries[before], "npm-vendor", "trove-test-merged", "1.0.0", "GHSA-aaaa-aaaa-2101", "vulnerability")
	details := e.blockDetails()
	if len(details) != 1 || details[0]["cause"] != "merged" || details[0]["from"] != "needs_triage" || details[0]["to"] != "blocked" {
		t.Errorf("audit details = %v", details)
	}
}

// R-VULN-012, R-HA-054 (Q-VULN-130): a finding that was blocked, whose version was removed and comes back, is re-detected as blocked;
// the re-detection records the event with the cause redetected and the entry.
func TestR_VULN_012_ARedetectionThatSetsABlockIsJournaled(t *testing.T) {
	e := newEnv(t)
	e.setComplete("npm", true)
	e.addAdvisory("npm", "GHSA-aaaa-aaaa-0103", "", "vulnerability", "high")
	e.addAffected("npm", "GHSA-aaaa-aaaa-0103", 0, "trove-test-back", everyVersionRange, nil, true, nil)
	e.addVersion("npm-vendor", "trove-test-back", "1.0.0")
	e.rescan("npm-vendor")
	e.removedBlock("trove-test-back")
	if n := len(e.outboxEntries(statusChanged)); n != 0 {
		t.Fatalf("%d entries before the version came back", n)
	}

	// The version is back: the evaluation of its name re-detects the finding with the status it had.
	e.queue("npm", "GHSA-aaaa-aaaa-0103")
	e.evaluate()

	if got := e.status("npm-vendor", "trove-test-back", "1.0.0"); got != "blocked" {
		t.Fatalf("after the re-detection: %q", got)
	}
	entries := e.outboxEntries(statusChanged)
	if len(entries) != 1 {
		t.Fatalf("%d entries after the re-detection, want 1", len(entries))
	}
	e.requireBlockEntry(entries[0], "npm-vendor", "trove-test-back", "1.0.0", "GHSA-aaaa-aaaa-0103", "vulnerability")
	details := e.blockDetails()
	if len(details) != 1 || details[0]["cause"] != "redetected" || details[0]["from"] != "resolved" || details[0]["to"] != "blocked" {
		t.Errorf("audit details = %v", details)
	}
}

// removedBlock leaves the finding of a package as a blocked version's removal leaves it: resolved with version_removed while blocked.
func (e *env) removedBlock(name string) {
	e.t.Helper()
	e.st.Exec(`UPDATE vuln_finding SET status = 'resolved', status_before_resolution = 'blocked', resolved_reason = 'version_removed', resolved_at = now()
		WHERE package_name = $1`, name)
}

// R-VULN-026 (Q-VULN-130): the entry every producer of the evaluation writes is the one the replay reads. Each finding is removed
// from the database as a restore to a point before the change leaves it, and the entry brings its block back.
func TestR_VULN_026_TheEntryOfEveryEvaluationProducerReplaysItsBlock(t *testing.T) {
	e := newEnv(t)
	e.setComplete("npm", true)
	e.addAdvisory("npm", "MAL-2026-0201", "", "malicious", "critical")
	e.addAffected("npm", "MAL-2026-0201", 0, "trove-test-prod-det", everyVersionRange, nil, true, nil)
	e.addAdvisory("npm", "GHSA-aaaa-aaaa-0201", "", "vulnerability", "high")
	e.addAffected("npm", "GHSA-aaaa-aaaa-0201", 0, "trove-test-prod-kind", everyVersionRange, nil, true, nil)
	e.addAdvisory("npm", "GHSA-aaaa-aaaa-0202", "", "vulnerability", "high")
	e.addAffected("npm", "GHSA-aaaa-aaaa-0202", 0, "trove-test-prod-back", everyVersionRange, nil, true, nil)
	e.publish("npm-vendor", "trove-test-prod-det", "1.0.0")
	e.publish("npm-vendor", "trove-test-prod-kind", "1.0.0")
	e.addVersion("npm-vendor", "trove-test-prod-back", "1.0.0")
	e.rescan("npm-vendor")
	e.st.Exec(`UPDATE vuln_advisory SET kind = 'malicious', modified = now() + interval '1 hour' WHERE id = 'GHSA-aaaa-aaaa-0201'`)
	e.queue("npm", "GHSA-aaaa-aaaa-0201")
	e.evaluate()
	e.removedBlock("trove-test-prod-back")
	e.queue("npm", "GHSA-aaaa-aaaa-0202")
	e.evaluate()

	entries := e.outboxEntries(statusChanged)
	if len(entries) != 3 {
		t.Fatalf("%d entries, want the three of the detection, the kind change and the re-detection", len(entries))
	}
	// The restored database lacks the findings.
	e.st.Exec(`DELETE FROM vuln_finding`)
	db := e.replayDB()
	for i, entry := range entries {
		raw, _ := json.Marshal(entry)
		res := e.replay(db, fmt.Sprintf("018f4c1e-0000-7000-8000-00000000f%03d", i), raw)
		if res.Outcome != journal.Applied {
			t.Errorf("entry %d: %s", i, res.Line())
		}
	}
	if got := e.st.Text(`SELECT string_agg(package_name || ':' || status || ':' || (version_id IS NOT NULL)::text, ' ' ORDER BY package_name) FROM vuln_finding`); got !=
		"trove-test-prod-back:blocked:true trove-test-prod-det:blocked:true trove-test-prod-kind:blocked:true" {
		t.Errorf("the replayed findings: %q", got)
	}
}

// R-VULN-026, R-VULN-013 (Q-VULN-131): a replay on a restored database that lacks the finding creates it. The version the
// repository stores makes it blocked and linked, which the download's join reads: the version answers 403 before any sync. A version
// the repository does not store makes it resolved while blocked, which the remote miss counts while the data cannot judge it. A
// forged entry denies one package version and nothing else, and a second replay applies nothing.
func TestR_VULN_026_AnEvaluationBlockIsReplayedBeforeTheFirstSync(t *testing.T) {
	e := newEnv(t)
	db := e.replayDB()
	const (
		stored  = "018f4c1e-0000-7000-8000-0000000000b1"
		evicted = "018f4c1e-0000-7000-8000-0000000000b2"
	)
	e.publish("npm-vendor", "trove-test-stored", "1.0.0")
	download := func(name string) int {
		return e.st.Do(e.rep, "GET", fmt.Sprintf("/api/npm/it/npm-vendor/%s/-/%s-1.0.0.tgz", name, name), e.adm, nil).Status
	}
	if got := download("trove-test-stored"); got != 200 {
		t.Fatalf("the stored version answers %d before the replay, want 200", got)
	}

	var results []journal.Result
	apply := func() {
		results = results[:0]
		for _, c := range []struct{ id, repo, name, advisory string }{
			{stored, "npm-vendor", "trove-test-stored", "GHSA-aaaa-aaaa-0301"},
			{evicted, "npm-remote", "trove-test-evicted", "MAL-2026-0301"},
		} {
			kind := "vulnerability"
			if c.advisory[0] == 'M' {
				kind = "malicious"
			}
			results = append(results, e.replay(db, c.id[:len(c.id)-2]+"e"+c.id[len(c.id)-1:], e.blockEntryJSON(c.id, c.repo, c.name, "1.0.0", c.advisory, kind)))
		}
	}
	apply()
	for _, r := range results {
		if r.Outcome != journal.Applied {
			t.Errorf("first replay: %s", r.Line())
		}
	}
	t.Run("R-VULN-013 the stored version is blocked and linked, and the download is refused", func(t *testing.T) {
		if got := e.shape(stored); got != "blocked - - true" {
			t.Errorf("the created finding reads %q", got)
		}
		if got := download("trove-test-stored"); got != 403 {
			t.Errorf("the replayed block of a stored version answers %d, want 403", got)
		}
		if got := e.st.Text(`SELECT change || ' ' || COALESCE(actor::text, 'none') || ' ' || (details->>'journal') FROM vuln_finding_event WHERE finding_id = $1::uuid`, stored); got != "detected none "+stored[:len(stored)-2]+"e"+stored[len(stored)-1:] {
			t.Errorf("the history reads %q", got)
		}
	})
	t.Run("R-VULN-015 the version the repository does not store is blocked for the remote miss", func(t *testing.T) {
		if got := e.shape(evicted); got != "resolved version_removed blocked false" {
			t.Errorf("the created finding reads %q", got)
		}
		cold := e.service(evaluate.Options{}) // the restored installation has no advisory data
		b, err := cold.EvaluateMiss(context.Background(), vuln.Miss{Repo: qr("npm-remote"), Format: "npm", Name: "trove-test-evicted", Version: "1.0.0"})
		if err != nil || !b.Refused() || b.FindingID != evicted {
			t.Errorf("the miss is %+v, %v: the version would be fetched", b, err)
		}
	})
	t.Run("R-VULN-026 a forged entry denies the one version it names", func(t *testing.T) {
		if n := e.st.Number(`SELECT count(*) FROM vuln_finding`); n != 2 {
			t.Errorf("%d findings, want the 2 the entries name", n)
		}
		forged := e.replay(db, "018f4c1e-0000-7000-8000-0000000000f1", e.blockEntryJSON("018f4c1e-0000-7000-8000-0000000000b3", "npm-vendor", "left-pad", "9.9.9", "GHSA-forged-0001", "vulnerability"))
		if forged.Outcome != journal.Applied {
			t.Errorf("the forged entry: %s", forged.Line())
		}
		if n := e.st.Number(`SELECT count(*) FROM vuln_finding`); n != 3 || e.st.Number(`SELECT count(*) FROM vuln_finding WHERE status = 'blocked'`) != 1 {
			t.Errorf("%d findings: the forged entry must add one finding and block nothing that is stored", n)
		}
		if got := download("trove-test-stored"); got != 403 {
			t.Errorf("answers %d", got)
		}
	})
	t.Run("R-BAK-024 a second replay applies nothing again", func(t *testing.T) {
		rev := e.st.Text(`SELECT string_agg(revision::text, ',' ORDER BY id) FROM vuln_finding`)
		events := e.st.Number(`SELECT count(*) FROM vuln_finding_event`)
		apply()
		for _, r := range results {
			if r.Outcome != journal.AlreadyInEffect {
				t.Errorf("second replay: %s", r.Line())
			}
		}
		if got := e.st.Text(`SELECT string_agg(revision::text, ',' ORDER BY id) FROM vuln_finding`); got != rev || e.st.Number(`SELECT count(*) FROM vuln_finding_event`) != events {
			t.Errorf("a second replay wrote: revisions %q (was %q)", got, rev)
		}
	})
	t.Run("R-HA-054 the replay writes no audit event", func(t *testing.T) {
		if n := e.events(statusChanged); n != 0 {
			t.Errorf("%d audit events", n)
		}
	})
}

// R-VULN-026 (Q-VULN-131): the finding of the entry's ID is found by its identity; an ID that names another package version is left
// alone and the block lands by identity; a repository that cannot hold the finding is unknown.
func TestR_VULN_026_AReplayedBlockFindsItsFindingByIdentity(t *testing.T) {
	e := newEnv(t)
	db := e.replayDB()
	e.publish("npm-vendor", "trove-test-ident", "1.0.0")
	e.publish("npm-vendor", "trove-test-other", "1.0.0")
	e.st.Exec(`INSERT INTO vuln_finding (id, repo_id, ecosystem, package_name, version, advisory_id, package_id, version_id, kind, severity, severity_rank, fix_available,
			status, advisory_modified)
		SELECT x.id::uuid, r.id, 'npm', p.name, pv.version, 'GHSA-aaaa-aaaa-0401', p.id, pv.id, 'vulnerability', 'high', 4, false, 'confirmed', now()
		  FROM repositories r JOIN packages p ON p.repo_id = r.id JOIN package_versions pv ON pv.package_id = p.id,
		       (VALUES ('018f4c1e-0000-7000-8000-0000000000c1', 'trove-test-ident'), ('018f4c1e-0000-7000-8000-0000000000c2', 'trove-test-other')) AS x(id, name)
		 WHERE r.key = 'npm-vendor' AND p.name = x.name`)
	t.Run("an ID of another package version leaves that finding alone and the block lands by identity", func(t *testing.T) {
		// The entry names the ID of the finding of trove-test-other and the identity of trove-test-ident.
		res := e.replay(db, "018f4c1e-0000-7000-8000-0000000000e2", e.blockEntryJSON("018f4c1e-0000-7000-8000-0000000000c2", "npm-vendor", "trove-test-ident", "1.0.0", "GHSA-aaaa-aaaa-0401", "vulnerability"))
		if res.Outcome != journal.Applied {
			t.Fatalf("%s", res.Line())
		}
		if got := e.shape("018f4c1e-0000-7000-8000-0000000000c1"); got != "blocked - - true" {
			t.Errorf("the finding of the identity reads %q", got)
		}
		if got := e.shape("018f4c1e-0000-7000-8000-0000000000c2"); got != "confirmed - - true" {
			t.Errorf("the finding of the ID reads %q, it must be untouched", got)
		}
	})
	t.Run("an entry whose ID is another finding's and whose identity is unknown creates a finding with a new ID", func(t *testing.T) {
		res := e.replay(db, "018f4c1e-0000-7000-8000-0000000000e3", e.blockEntryJSON("018f4c1e-0000-7000-8000-0000000000c2", "npm-vendor", "trove-test-ident", "1.0.0", "GHSA-aaaa-aaaa-0402", "vulnerability"))
		if res.Outcome != journal.Applied {
			t.Fatalf("%s", res.Line())
		}
		if n := e.st.Number(`SELECT count(*) FROM vuln_finding WHERE advisory_id = 'GHSA-aaaa-aaaa-0402' AND id <> '018f4c1e-0000-7000-8000-0000000000c2'::uuid`); n != 1 {
			t.Errorf("%d findings created with a new ID", n)
		}
	})
	t.Run("a repository that cannot hold the finding is unknown", func(t *testing.T) {
		good := func() map[string]string {
			var m map[string]string
			_ = json.Unmarshal(e.blockEntryJSON("018f4c1e-0000-7000-8000-0000000000c9", "npm-vendor", "trove-test-unknown", "1.0.0", "GHSA-aaaa-aaaa-0403", "vulnerability"), &m)
			return m
		}
		for name, mutate := range map[string]func(m map[string]string){
			"a repository the database does not hold": func(m map[string]string) { m["repositoryId"] = "99999999" },
			"a repository of another ecosystem": func(m map[string]string) {
				m["repositoryId"] = fmt.Sprint(e.st.Number(`SELECT id FROM repositories WHERE key = 'nuget-local'`))
			},
		} {
			m := good()
			mutate(m)
			raw, _ := json.Marshal(m)
			if res := e.replay(db, "018f4c1e-0000-7000-8000-0000000000e4", raw); res.Outcome != journal.SubjectUnknown {
				t.Errorf("%s: %s", name, res.Line())
			}
		}
	})
	t.Run("an entry without a kind is skipped", func(t *testing.T) {
		var m map[string]string
		_ = json.Unmarshal(e.blockEntryJSON("018f4c1e-0000-7000-8000-0000000000c9", "npm-vendor", "trove-test-nokind", "1.0.0", "GHSA-aaaa-aaaa-0404", "vulnerability"), &m)
		delete(m, "kind")
		raw, _ := json.Marshal(m)
		if res := e.replay(db, "018f4c1e-0000-7000-8000-0000000000e5", raw); res.Outcome != journal.Skipped {
			t.Errorf("%s", res.Line())
		}
		if n := e.st.Number(`SELECT count(*) FROM vuln_finding WHERE package_name = 'trove-test-nokind'`); n != 0 {
			t.Errorf("%d findings from a skipped entry", n)
		}
	})
}

// restoredAt marks the installation as restored now, after the last sync of every ecosystem so far.
func (e *env) restoredAt() {
	e.t.Helper()
	e.st.Exec(`INSERT INTO system_state (key, value) VALUES ('restored_at', to_jsonb(now())) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`)
}

// evictedBlock seeds the finding a replay leaves for a version the repository does not store.
func (e *env) evictedBlock(id, repo, name, advisoryID string) {
	e.t.Helper()
	e.st.Exec(`INSERT INTO vuln_finding (id, repo_id, ecosystem, package_name, version, advisory_id, kind, severity, severity_rank, fix_available,
			status, status_before_resolution, resolved_reason, resolved_at, advisory_modified)
		SELECT $1::uuid, r.id, 'npm', $3, '1.0.0', $4, 'vulnerability', 'high', 4, false, 'resolved', 'blocked', 'version_removed', now(), now()
		  FROM repositories r WHERE r.key = $2`, id, repo, name, advisoryID)
}

// R-VULN-012, R-VULN-015 (Q-VULN-132): while the ecosystem's data is incomplete, or has not synced since the last restore, the commit
// of a version re-detects a finding that resolved with version_removed while blocked, though no advisory matches: the finding is
// blocked again, linked to the new version, its history says redetected, the change is journaled and the download is refused. After
// the first sync since the restore the advisory data decides and the finding stays resolved.
func TestR_VULN_012_ACommitBeforeTheFirstSyncAfterARestoreRedetectsAnEvictedBlock(t *testing.T) {
	const id = "018f4c1e-0000-7000-8000-0000000000d1"
	for _, tc := range []struct {
		name     string
		prepare  func(e *env)
		wantHeld bool
	}{
		{"incomplete data", func(e *env) { e.setComplete("npm", false) }, true},
		{"complete data that is older than the restore", func(e *env) { e.setComplete("npm", true); e.restoredAt() }, true},
		{"complete data that synced after the restore", func(e *env) {
			e.setComplete("npm", true)
			e.restoredAt()
			e.st.Exec(`UPDATE vuln_source_state SET synced_at = now() WHERE ecosystem = 'npm'`)
		}, false},
		{"complete data of an installation never restored", func(e *env) { e.setComplete("npm", true) }, false},
	} {
		t.Run("R-VULN-015 "+tc.name, func(t *testing.T) {
			e := newEnv(t)
			tc.prepare(e)
			e.evictedBlock(id, "npm-vendor", "trove-test-held", "GHSA-ghost-0001")

			e.publish("npm-vendor", "trove-test-held", "1.0.0")

			download := e.st.Do(e.rep, "GET", "/api/npm/it/npm-vendor/trove-test-held/-/trove-test-held-1.0.0.tgz", e.adm, nil).Status
			entries := e.outboxEntries(statusChanged)
			if !tc.wantHeld {
				if got := e.shape(id); got != "resolved version_removed blocked false" || len(entries) != 0 || download != 200 {
					t.Errorf("finding %q, %d entries, download %d: the advisory data decides, so the finding stays and the version is served", got, len(entries), download)
				}
				return
			}
			if got := e.shape(id); got != "blocked - - true" {
				t.Fatalf("the finding reads %q after the commit, want blocked and linked to the new version", got)
			}
			if got := e.history("npm-vendor", "trove-test-held", "1.0.0"); got != "redetected" {
				t.Errorf("history %q", got)
			}
			if len(entries) != 1 {
				t.Fatalf("%d entries, want 1", len(entries))
			}
			e.requireBlockEntry(entries[0], "npm-vendor", "trove-test-held", "1.0.0", "GHSA-ghost-0001", "vulnerability")
			if d := e.blockDetails(); len(d) != 1 || d[0]["cause"] != "redetected" || d[0]["from"] != "resolved" || d[0]["to"] != "blocked" {
				t.Errorf("audit details = %v", d)
			}
			if download != 403 {
				t.Errorf("the download answers %d, want 403", download)
			}
		})
	}
}

// R-VULN-015, R-VULN-027 (Q-VULN-132): the state of the ecosystem's data the replica caches says whether the ecosystem has synced
// since the last restore, read with the row and not per request.
func TestR_VULN_015_TheStateRowSaysWhetherTheEcosystemSyncedSinceTheRestore(t *testing.T) {
	e := newEnv(t)
	_, db := e.serviceDB(evaluate.Options{}, nil)
	read := func() store.StateSummary {
		t.Helper()
		var list []store.StateSummary
		if err := db.Read(context.Background(), func(c q.DBTX) (err error) {
			list, err = store.Sync{}.List(context.Background(), c, store.SourceOSV)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		for _, s := range list {
			if s.Ecosystem == "npm" {
				return s
			}
		}
		t.Fatal("no row for npm")
		return store.StateSummary{}
	}
	e.setComplete("npm", true)
	if s := read(); s.NotSyncedSinceRestore || !s.Complete {
		t.Errorf("never restored: %+v", s)
	}
	e.restoredAt()
	if s := read(); !s.NotSyncedSinceRestore {
		t.Errorf("restored, not synced since: %+v", s)
	}
	time.Sleep(10 * time.Millisecond)
	e.st.Exec(`UPDATE vuln_source_state SET synced_at = now() WHERE ecosystem = 'npm'`)
	if s := read(); s.NotSyncedSinceRestore {
		t.Errorf("restored, synced since: %+v", s)
	}
	e.st.Exec(`UPDATE vuln_source_state SET synced_at = NULL WHERE ecosystem = 'npm'`)
	if s := read(); !s.NotSyncedSinceRestore {
		t.Errorf("restored, never synced: %+v", s)
	}
}

// R-VULN-015 (Q-VULN-132): a platform restore brings the advisory data back complete but as old as the restore point, so the
// remote miss counts a blocked finding whose version is gone until the ecosystem has synced since the restore; after that the
// advisory data decides.
func TestR_VULN_015_AMissAfterAPlatformRestoreCountsAnEvictedBlockUntilTheNextSync(t *testing.T) {
	e := newEnv(t)
	e.setComplete("npm", true)
	e.restoredAt()
	const id = "018f4c1e-0000-7000-8000-0000000000d2"
	e.evictedBlock(id, "npm-remote", "trove-test-gone", "GHSA-aaaa-aaaa-0501")
	miss := func(data advisory.DataState) vuln.Block {
		t.Helper()
		b, err := e.service(evaluate.Options{States: fixedStates{"npm": data}}).EvaluateMiss(context.Background(),
			vuln.Miss{Repo: qr("npm-remote"), Format: "npm", Name: "trove-test-gone", Version: "1.0.0"})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	if b := miss(advisory.DataState{Complete: true, NotSyncedSinceRestore: true}); !b.Refused() || b.FindingID != id || b.Reason != vuln.BlockTriage {
		t.Errorf("complete data older than the restore: %+v, want the evicted block to refuse", b)
	}
	if b := miss(advisory.DataState{Complete: true}); b.Active() {
		t.Errorf("complete data that synced since the restore: %+v, want the advisory data to decide (no advisory names the version)", b)
	}
}
