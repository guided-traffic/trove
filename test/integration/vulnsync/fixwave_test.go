//go:build integration

package vulnsync

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/guided-traffic/trove/internal/jobs"
	vsync "github.com/guided-traffic/trove/internal/vuln/sync"
	"github.com/guided-traffic/trove/test/osvstub"
)

const kindChangedEvent = "vulnerability.advisory.kind_changed"

func (e *env) kindChanges() int64 {
	e.t.Helper()
	return e.number("SELECT count(*) FROM audit_event WHERE action = $1", kindChangedEvent)
}

func (e *env) canonical(id string) string {
	e.t.Helper()
	return e.text("SELECT canonical_id FROM vuln_advisory WHERE ecosystem = 'npm' AND id = $1", id)
}

func (e *env) pending(id string) int64 {
	e.t.Helper()
	return e.number("SELECT count(*) FROM vuln_pending_evaluation WHERE ecosystem = 'npm' AND advisory_id = $1", id)
}

func malAliasing(id, withdrawn string) osvstub.Advisory {
	return osvstub.Advisory{
		ID: id, Aliases: []string{osvstub.IDVuln}, Summary: "malicious", Withdrawn: withdrawn,
		Affected: []osvstub.Affected{{Package: osvstub.Package{Ecosystem: "npm", Name: osvstub.NPMVuln}, Versions: []string{"1.0.0"}}},
	}
}

// R-VULN-006, Q-VULN-61 (S1 of the fix wave): a withdrawn MAL- record never becomes the canonical record of the group it aliases,
// so a malicious identifier cannot turn a vulnerability's group malicious by listing it, and the withdrawal of an active one
// gives the group back to the others; a change of the group's kind through an alias is an audit event, and the first import of an
// ecosystem writes none.
func TestR_VULN_006_AWithdrawnMalRecordNeverMakesItsAliasGroupMalicious(t *testing.T) {
	e := newEnv(t, options{})
	e.mustSucceed(taskOSV, nil)
	if got := e.kindChanges(); got != 0 {
		t.Fatalf("the first import wrote %d kind change events", got)
	}

	// A record that is withdrawn when it first arrives joins the group and does not lead it; no group of any kind existed before.
	if _, err := e.stub.UpsertAdvisory("npm", malAliasing("MAL-2026-9101", "2026-10-20T00:00:00Z")); err != nil {
		t.Fatal(err)
	}
	e.mustSucceed(taskOSV, nil)
	for _, id := range []string{osvstub.IDVuln, "MAL-2026-9101"} {
		if got := e.canonical(id); got != osvstub.IDVuln {
			t.Errorf("%s: canonical identifier %q, want %s", id, got, osvstub.IDVuln)
		}
	}
	if got := e.text("SELECT kind FROM vuln_advisory WHERE ecosystem = 'npm' AND id = $1", osvstub.IDVuln); got != "vulnerability" {
		t.Errorf("kind of the vulnerability %q", got)
	}
	if got := e.kindChanges(); got != 0 {
		t.Errorf("a withdrawn record that joined a group wrote %d kind change events", got)
	}

	// An active MAL- record takes the group over: the kind changes and the change is an event.
	e.st.Exec("DELETE FROM vuln_pending_evaluation")
	if _, err := e.stub.UpsertAdvisory("npm", malAliasing("MAL-2026-9102", "")); err != nil {
		t.Fatal(err)
	}
	e.mustSucceed(taskOSV, nil)
	for _, id := range []string{osvstub.IDVuln, "MAL-2026-9101", "MAL-2026-9102"} {
		if got := e.canonical(id); got != "MAL-2026-9102" {
			t.Errorf("%s: canonical identifier %q, want MAL-2026-9102", id, got)
		}
	}
	if got := e.kindChanges(); got != 1 {
		t.Fatalf("%d kind change events after the takeover, want 1", got)
	}
	ev := e.text(`SELECT details->>'advisory' || ' ' || (details->>'previous') || ' ' || (details->>'from') || ' ' || (details->>'to')
		FROM audit_event WHERE action = $1 ORDER BY occurred_at DESC LIMIT 1`, kindChangedEvent)
	if ev != "MAL-2026-9102 "+osvstub.IDVuln+" vulnerability malicious" {
		t.Errorf("event %q", ev)
	}
	for _, id := range []string{osvstub.IDVuln, "MAL-2026-9102"} {
		if e.pending(id) != 1 {
			t.Errorf("%s was not queued for the evaluation", id)
		}
	}

	// The withdrawal of the active record gives the group back; that is a change of kind too.
	if err := e.stub.Withdraw("npm", "MAL-2026-9102"); err != nil {
		t.Fatal(err)
	}
	e.mustSucceed(taskOSV, nil)
	for _, id := range []string{osvstub.IDVuln, "MAL-2026-9101", "MAL-2026-9102"} {
		if got := e.canonical(id); got != osvstub.IDVuln {
			t.Errorf("after the withdrawal %s: canonical identifier %q, want %s", id, got, osvstub.IDVuln)
		}
	}
	if got := e.kindChanges(); got != 2 {
		t.Errorf("%d kind change events after the withdrawal, want 2", got)
	}
	if got := e.text("SELECT severity || ' ' || severity_source FROM vuln_advisory WHERE ecosystem = 'npm' AND id = $1", osvstub.IDVuln); got != "high cvss" {
		t.Errorf("severity of the vulnerability after the group left the malicious record %q", got)
	}
}

// R-VULN-004 (C1 of the fix wave): an advisory that is queued again takes a new enqueue time, which is what the evaluation's
// delete compares, so that an advisory that changed again while a batch was being evaluated stays queued.
func TestR_VULN_004_AnAdvisoryQueuedAgainTakesANewEnqueueTime(t *testing.T) {
	e := newEnv(t, options{})
	e.mustSucceed(taskOSV, nil)
	first := e.text("SELECT enqueued_at::text FROM vuln_pending_evaluation WHERE ecosystem = 'npm' AND advisory_id = $1", osvstub.IDVuln)
	e.touch("npm", osvstub.IDVuln)
	e.mustSucceed(taskOSV, nil)
	if got := e.number("SELECT count(*) FROM vuln_pending_evaluation WHERE ecosystem = 'npm' AND advisory_id = $1 AND enqueued_at > $2::timestamptz", osvstub.IDVuln, first); got != 1 {
		t.Errorf("the queued advisory kept its enqueue time %s (now %s)", first,
			e.text("SELECT enqueued_at::text FROM vuln_pending_evaluation WHERE ecosystem = 'npm' AND advisory_id = $1", osvstub.IDVuln))
	}
}

// R-VULN-004, R-VULN-006 (C2 and C3 of the fix wave): the write of a group's canonical identifier and the queueing of what it
// changed are one transaction, and a run that fails there leaves the names it touched, so that the next run recomputes the groups,
// raises the epoch and enqueues the evaluation although it finds nothing new to write.
func TestR_VULN_006_AFailedRunLeavesItsGroupsToTheNextRun(t *testing.T) {
	e := newEnv(t, options{})
	e.mustSucceed(taskOSV, nil)
	e.st.Exec("DELETE FROM vuln_pending_evaluation")
	epoch := e.number("SELECT data_epoch FROM vuln_source_state WHERE source = 'osv' AND ecosystem = 'npm'")
	// The queue refuses the identifier the group's recomputation queues as its canonical one, and no other.
	e.st.Exec(`CREATE FUNCTION refuse_pending() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'refused'; END $$`)
	e.st.Exec(`CREATE TRIGGER refuse_pending BEFORE INSERT OR UPDATE ON vuln_pending_evaluation FOR EACH ROW
		WHEN (NEW.advisory_id = '` + osvstub.IDVuln + `') EXECUTE FUNCTION refuse_pending()`)
	alias := osvstub.Advisory{
		ID: "A-2026-0001", Aliases: []string{osvstub.IDVuln}, Summary: "an alias of the vulnerability",
		Affected: []osvstub.Affected{{Package: osvstub.Package{Ecosystem: "npm", Name: osvstub.NPMVuln}, Versions: []string{"1.0.0"}}},
	}
	if _, err := e.stub.UpsertAdvisory("npm", alias); err != nil {
		t.Fatal(err)
	}
	e.mustFail(taskOSV, "osv", "npm", nil)
	if got := e.canonical("A-2026-0001"); got != "A-2026-0001" {
		t.Errorf("the canonical identifier was written (%q) although the queueing it belongs to failed", got)
	}
	if got := e.number("SELECT count(*) FROM vuln_sync_touched WHERE ecosystem = 'npm' AND name = 'A-2026-0001'"); got != 1 {
		t.Errorf("the names of the failed run were not kept")
	}

	e.st.Exec("DROP TRIGGER refuse_pending ON vuln_pending_evaluation")
	// The source has nothing new: the stored record is the one it serves.
	e.mustSucceed(taskOSV, nil)
	if got := e.canonical("A-2026-0001"); got != osvstub.IDVuln {
		t.Errorf("the next run left the canonical identifier %q, want %s", got, osvstub.IDVuln)
	}
	if e.pending(osvstub.IDVuln) != 1 || e.pending("A-2026-0001") != 1 {
		t.Errorf("the group's records were not queued for the evaluation")
	}
	if got := e.number("SELECT count(*) FROM vuln_sync_touched"); got != 0 {
		t.Errorf("%d touched names left after the run ended", got)
	}
	if got := e.number("SELECT data_epoch FROM vuln_source_state WHERE source = 'osv' AND ecosystem = 'npm'"); got <= epoch {
		t.Errorf("the epoch is %d, it was %d before the failed run", got, epoch)
	}
}

// R-VULN-004 (C2 of the fix wave): a full run released by a drain after some batches, taken up by a job that has no checkpoint
// for it, finds the records of those batches unchanged and must still recompute their alias groups.
func TestR_VULN_004_ARunAfterAReleaseRecomputesTheGroupsOfTheBatchesItSkips(t *testing.T) {
	e := newEnv(t, options{Filler: 1500})
	e.mustSucceed(taskOSV, nil)
	alias := osvstub.Advisory{
		ID: "A-2026-0002", Aliases: []string{osvstub.IDVuln}, Summary: "an alias of the vulnerability, first in the archive",
		Affected: []osvstub.Affected{{Package: osvstub.Package{Ecosystem: "npm", Name: osvstub.NPMVuln}, Versions: []string{"1.0.0"}}},
	}
	if _, err := e.stub.UpsertAdvisory("npm", alias); err != nil {
		t.Fatal(err)
	}
	svc := e.worker.App.VulnSync()
	rec := &batches{}
	svc.SetBatchHook(rec.hook)
	ctx, cancel := context.WithCancelCause(context.Background())
	rec.release, rec.cancel = "npm/1", cancel
	if err := svc.RunOSV(ctx, &fakeRun{}, vsync.Params{}, true); !errors.Is(err, jobs.ErrReleased) {
		t.Fatalf("release: %v", err)
	}
	cancel(nil)
	if got := e.advisories("npm"); got < 1000 || e.number("SELECT count(*) FROM vuln_advisory WHERE id = 'A-2026-0002'") != 1 {
		t.Fatalf("the first batch did not hold the alias record (%d records)", got)
	}
	if got := e.canonical("A-2026-0002"); got != "A-2026-0002" {
		t.Fatalf("the groups were recomputed before the run ended: %q", got)
	}

	// Another job, no checkpoint: the batch the release left is skipped as unchanged.
	rec.release, rec.cancel = "", nil
	if err := svc.RunOSV(context.Background(), &fakeRun{}, vsync.Params{}, true); err != nil {
		t.Fatal(err)
	}
	if got := e.canonical("A-2026-0002"); got != osvstub.IDVuln {
		t.Errorf("canonical identifier %q, want %s", got, osvstub.IDVuln)
	}
	if got := e.number("SELECT count(*) FROM vuln_sync_touched"); got != 0 {
		t.Errorf("%d touched names left", got)
	}
}

// R-VULN-007 rule 3 (C7 of the fix wave): the severity decided for a canonical record with its aliases is stored apart from the
// record's own, so that a later change of an alias vector is adopted; the record keeps its own decision in own_severity.
func TestR_VULN_007_ALaterAliasVectorChangeIsAdopted(t *testing.T) {
	e := newEnv(t, options{})
	canonical := osvstub.Advisory{
		ID: "GHSA-aaaa-zzzz-0001", Aliases: []string{"CVE-2026-7001", "CVE-2026-7002"},
		DatabaseSpecific: map[string]any{"severity": "LOW"},
		Affected:         []osvstub.Affected{{Package: osvstub.Package{Ecosystem: "npm", Name: "trove-test-rule3"}, Versions: []string{"1.0.0"}}},
	}
	lowAlias := osvstub.Advisory{
		ID: "CVE-2026-7001", Aliases: []string{"GHSA-aaaa-zzzz-0001"},
		Severity: []osvstub.Severity{{Type: "CVSS_V3", Score: "CVSS:3.1/AV:N/AC:H/PR:N/UI:R/S:U/C:L/I:N/A:N"}},
	}
	highAlias := osvstub.Advisory{
		ID: "CVE-2026-7002", Aliases: []string{"GHSA-aaaa-zzzz-0001"},
		Severity: []osvstub.Severity{{Type: "CVSS_V3", Score: "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H"}},
	}
	for _, a := range []osvstub.Advisory{canonical, lowAlias, highAlias} {
		if _, err := e.stub.UpsertAdvisory("npm", a); err != nil {
			t.Fatal(err)
		}
	}
	e.mustSucceed(taskOSV, nil)
	decided := func() string {
		return e.text(`SELECT severity || ' ' || severity_source || ' ' || COALESCE(cvss_version, '') FROM vuln_advisory WHERE ecosystem = 'npm' AND id = 'GHSA-aaaa-zzzz-0001'`)
	}
	if got := decided(); got != "critical cvss 3.1" {
		t.Fatalf("decided severity %q", got)
	}
	if got := e.text(`SELECT own_severity || ' ' || own_severity_source || ' ' || COALESCE(own_cvss_version, '') FROM vuln_advisory WHERE ecosystem = 'npm' AND id = 'GHSA-aaaa-zzzz-0001'`); got != "low advisory " {
		t.Errorf("the record's own severity %q", got)
	}

	// The critical alias is revised to a medium vector: the group's decision follows it.
	highAlias.Severity = []osvstub.Severity{{Type: "CVSS_V3", Score: "CVSS:3.1/AV:N/AC:L/PR:L/UI:N/S:U/C:L/I:L/A:N"}}
	if _, err := e.stub.UpsertAdvisory("npm", highAlias); err != nil {
		t.Fatal(err)
	}
	e.st.Exec("DELETE FROM vuln_pending_evaluation")
	e.mustSucceed(taskOSV, nil)
	if got := decided(); got != "medium cvss 3.1" {
		t.Errorf("after the alias vector changed: %q, want medium cvss 3.1", got)
	}
	if e.pending("GHSA-aaaa-zzzz-0001") != 1 {
		t.Errorf("the canonical record whose decided severity changed was not queued for the evaluation")
	}

	// The canonical record is rewritten: its decided severity is not reset, and its own one stays.
	e.touch("npm", "GHSA-aaaa-zzzz-0001")
	e.mustSucceed(taskOSV, nil)
	if got := decided(); got != "medium cvss 3.1" {
		t.Errorf("after the canonical record was rewritten: %q", got)
	}
}

// R-VULN-004, Q-VULN-63 (A10 of the fix wave): a rise of the adapter version of an ecosystem makes the next run a full,
// unconditional import: the archive is asked for without If-None-Match although the run would otherwise be incremental or a 304,
// every record is written again and queued, and the version is stored; the run after it is a 304 again.
func TestR_VULN_004_ARisenAdapterVersionForcesAFullUnconditionalImport(t *testing.T) {
	e := newEnv(t, options{})
	e.mustSucceed(taskOSV, nil)
	if got := e.stateOf("adapter_version", "osv", "npm"); got != "1" {
		t.Fatalf("adapter version %q after the first import, want 1", got)
	}
	// Nothing changed: the run is incremental, it asks for the change file and not the archive.
	e.stub.ResetCounts()
	e.mustSucceed(taskOSV, nil)
	if e.stub.Counts().Kind("npm", osvstub.KindArchive) != 0 {
		t.Fatalf("an unchanged source made the run download the archive\n%s", e.requestLog())
	}

	// The state says the records were imported with an older adapter, as a release that raised the constant finds it.
	e.st.Exec("UPDATE vuln_source_state SET adapter_version = 0 WHERE source = 'osv' AND ecosystem = 'npm'")
	e.st.Exec("DELETE FROM vuln_pending_evaluation")
	epoch := e.number("SELECT data_epoch FROM vuln_source_state WHERE source = 'osv' AND ecosystem = 'npm'")
	e.stub.ResetCounts()
	j := e.mustSucceed(taskOSV, nil)
	var archive bool
	for _, r := range e.stub.Counts().Log {
		if r.Path == "/npm/all.zip" {
			archive = true
			if r.Conditional || r.Status != 200 {
				t.Errorf("the archive was asked for conditionally or answered %d: %+v", r.Status, r)
			}
		}
	}
	if !archive {
		t.Fatalf("the archive was not downloaded\n%s", e.requestLog())
	}
	var result struct {
		Ecosystems []struct {
			Ecosystem, Kind, Outcome string
			Written                  int
		} `json:"ecosystems"`
	}
	if err := json.Unmarshal([]byte(j.Result), &result); err != nil || len(result.Ecosystems) == 0 {
		t.Fatalf("result %s: %v", j.Result, err)
	}
	for _, r := range result.Ecosystems {
		if r.Ecosystem == "npm" && (r.Kind != "full" || r.Outcome != "changed" || r.Written == 0) {
			t.Errorf("result of npm %+v: want a full run that wrote records", r)
		}
	}
	if got, want := e.number("SELECT count(*) FROM vuln_pending_evaluation WHERE ecosystem = 'npm'"), e.advisories("npm"); got != want {
		t.Errorf("%d of %d records were queued: the unchanged ones were skipped by their digest", got, want)
	}
	if got := e.stateOf("adapter_version", "osv", "npm"); got != "1" {
		t.Errorf("adapter version %q after the import, want 1", got)
	}
	if got := e.number("SELECT data_epoch FROM vuln_source_state WHERE source = 'osv' AND ecosystem = 'npm'"); got <= epoch {
		t.Errorf("the epoch %d did not rise (it was %d)", got, epoch)
	}

	// With the version stored, the unchanged archive is a 304 again.
	e.stub.ResetCounts()
	e.mustSucceed(taskOSVFull, nil)
	for _, r := range e.stub.Counts().Log {
		if r.Path == "/npm/all.zip" && r.Status != 304 {
			t.Errorf("the archive answered %d after the import", r.Status)
		}
	}
}
