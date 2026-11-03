//go:build integration

package vulnrestore

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/backup/tables"
	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/osvstub"
)

// The findings the restored installation holds. The ids are fixed so that a test can name them.
const (
	fDismissed = "018f4c1e-0000-7000-8000-000000000001" // trove-test-vuln@1.0.0 in npm-local, dismissed (risk_accepted)
	fBlocked   = "018f4c1e-0000-7000-8000-000000000002" // trove-test-vuln@1.0.0 in npm-blocked, blocked by a triager
	fMalicious = "018f4c1e-0000-7000-8000-000000000003" // trove-test-mal@1.0.0 in npm-local, open, malicious
)

// seedInstallation reproduces an installation that had synced and triaged, then the state a restore leaves of it
// (R-VULN-026): the backup job is phase 7 and does not exist, so the restore is a SQL emulation of its effect on this
// feature's tables. Before: the advisory data of npm was complete, advisories, affected entries, a KEV row, a pending
// evaluation and a claimed-name match existed, and three findings with their history: one dismissed, one blocked, one
// open and malicious. After: the six transient tables are empty and the two data tables are as they were (export class
// data restores them). Three versions are published through a replica before that, so the stored items are real.
func (e *env) seedInstallation() {
	e.t.Helper()
	st := e.st
	pub := e.web("seed")
	e.publish(pub, repoLocal, osvstub.NPMVuln, "1.0.0")
	e.publish(pub, repoBlocked, osvstub.NPMVuln, "1.0.0")
	e.publish(pub, repoLocal, osvstub.NPMMal, "1.0.0")
	e.publish(pub, repoLocal, "trove-test-clean", "1.0.0")
	if err := pub.Stop(); err != nil {
		e.t.Fatal(err)
	}

	// The synced state of an earlier life.
	st.Exec(`INSERT INTO vuln_source_state (source, ecosystem, state, data_epoch, synced_at, full_synced_at, attempted_at, records)
		VALUES ('osv', 'npm', 'complete', 7, now() - interval '1 hour', now() - interval '1 hour', now() - interval '1 hour', 2)`)
	for _, id := range []string{osvstub.IDVuln, osvstub.IDMal} {
		kind, sev := "vulnerability", "high"
		if id == osvstub.IDMal {
			kind, sev = "malicious", "critical"
		}
		st.Exec(`INSERT INTO vuln_advisory (ecosystem, id, canonical_id, modified, kind, severity, severity_source, record_sha256)
			VALUES ('npm', $1, $1, $2, $3, $4, 'advisory', '\x00')`, id, e.recordModified("npm", id), kind, sev)
	}
	st.Exec(`INSERT INTO vuln_affected (ecosystem, advisory_id, ord, name, ranges, every_version, versions)
		VALUES ('npm', $1, 0, $2, '[]', false, '{1.0.0}'), ('npm', $3, 0, $4, '[]', false, '{1.0.0}')`,
		osvstub.IDVuln, osvstub.NPMVuln, osvstub.IDMal, osvstub.NPMMal)
	st.Exec(`INSERT INTO vuln_kev (cve_id, date_added) VALUES ('CVE-2026-0001', current_date)`)
	st.Exec(`INSERT INTO vuln_pending_evaluation (ecosystem, advisory_id) VALUES ('npm', $1)`, osvstub.IDVuln)
	st.Exec(`INSERT INTO vuln_claimed_match (ecosystem, advisory_id, name, kind) VALUES ('npm', $1, '@acme/ui', 'malicious')`, osvstub.IDMal)

	// The findings and their history (export class data).
	e.finding(fDismissed, repoLocal, osvstub.NPMVuln, osvstub.IDVuln, "vulnerability", "high", 4, "7.5", "dismissed")
	e.finding(fBlocked, repoBlocked, osvstub.NPMVuln, osvstub.IDVuln, "vulnerability", "high", 4, "7.5", "blocked")
	e.finding(fMalicious, repoLocal, osvstub.NPMMal, osvstub.IDMal, "malicious", "critical", 5, "", "needs_triage")
	for _, f := range []string{fDismissed, fBlocked, fMalicious} {
		st.Exec(`INSERT INTO vuln_finding_event (finding_id, at, change, to_status) VALUES ($1::uuid, now() - interval '2 days', 'detected', 'needs_triage')`, f)
	}
	st.Exec(`INSERT INTO vuln_finding_event (finding_id, at, actor, change, from_status, to_status, reason, comment)
		VALUES ($1::uuid, now() - interval '1 day', $2::uuid, 'status_changed', 'needs_triage', 'dismissed', 'risk_accepted', 'test only')`, fDismissed, e.admin.ID)
	st.Exec(`INSERT INTO vuln_finding_event (finding_id, at, actor, change, from_status, to_status)
		VALUES ($1::uuid, now() - interval '1 day', $2::uuid, 'status_changed', 'needs_triage', 'confirmed'),
		       ($1::uuid, now() - interval '1 day', $2::uuid, 'status_changed', 'confirmed', 'blocked')`, fBlocked, e.admin.ID)

	// The restore: what the export does not carry is empty.
	transient, _ := transientTables()
	st.Exec("TRUNCATE " + strings.Join(transient, ", ") + " CASCADE")
}

// finding inserts a finding of an published version.
func (e *env) finding(id, repo, name, advisory, kind, severity string, rank int, score, status string) {
	e.t.Helper()
	var cvss any
	if score != "" {
		cvss = score
	}
	var reason any
	if status == "dismissed" {
		reason = "risk_accepted"
	}
	e.st.Exec(`INSERT INTO vuln_finding (id, repo_id, ecosystem, package_name, version, advisory_id, package_id, version_id, kind, severity,
			severity_rank, cvss_score, fix_available, fixed_versions, status, dismissal_reason, advisory_modified, detected_at, status_changed_at)
		SELECT $1::uuid, r.id, 'npm', p.name, pv.version, $4::text, p.id, pv.id, $5::text, $6::text, $7::smallint, $8::numeric, $5::text = 'vulnerability',
		       CASE WHEN $5::text = 'vulnerability' THEN '{1.0.1}'::text[] ELSE '{}'::text[] END, $9::text, $10::text, $11::timestamptz,
		       now() - interval '2 days', now() - interval '1 day'
		  FROM repositories r JOIN packages p ON p.repo_id = r.id JOIN package_versions pv ON pv.package_id = p.id
		 WHERE r.key = $2::text AND p.name = $3::text AND pv.version = '1.0.0'`,
		id, repo, name, advisory, kind, severity, rank, cvss, status, reason, e.recordModified("npm", advisory))
	if e.st.Number(`SELECT count(*) FROM vuln_finding WHERE id = $1::uuid`, id) != 1 {
		e.t.Fatalf("finding %s was not inserted: %s@1.0.0 is not stored in %s", id, name, repo)
	}
}

// snapshot is what "as before" compares: the status of every finding and the history of each, as text.
type snapshot struct {
	status  map[string]string
	history map[string]string
}

func (e *env) snapshot() snapshot {
	e.t.Helper()
	s := snapshot{status: map[string]string{}, history: map[string]string{}}
	for _, id := range []string{fDismissed, fBlocked, fMalicious} {
		s.status[id] = e.st.Text(`SELECT COALESCE((SELECT status FROM vuln_finding WHERE id = $1::uuid), 'gone')`, id)
		s.history[id] = e.st.Text(`SELECT COALESCE(string_agg(change || ':' || COALESCE(from_status, '') || '>' || COALESCE(to_status, '') || ':' ||
			COALESCE(reason, '') || ':' || COALESCE(actor::text, ''), ',' ORDER BY seq), '') FROM vuln_finding_event WHERE finding_id = $1::uuid`, id)
	}
	return s
}

// requireHeld fails when a finding changed its status or lost or changed an event it had (events may be added).
func requireHeld(t *testing.T, when string, before, after snapshot) {
	t.Helper()
	for id, st := range before.status {
		if after.status[id] != st {
			t.Errorf("%s: finding %s is %q, was %q", when, id, after.status[id], st)
		}
		if !strings.HasPrefix(after.history[id], before.history[id]) {
			t.Errorf("%s: the history of finding %s is %q, it began %q", when, id, after.history[id], before.history[id])
		}
	}
}

// TestR_VULN_026_TheStateARestoreLeaves: the export classes and the emulated state (R-VULN-026). The export of a database
// carries the findings and their events and none of the six transient tables.
func TestR_VULN_026_TheStateARestoreLeaves(t *testing.T) {
	e := newEnv(t)
	e.seedInstallation()

	transient, data := transientTables()
	if got := strings.Join(sorted(transient), ","); got != "vuln_advisory,vuln_affected,vuln_claimed_match,vuln_kev,vuln_pending_evaluation,vuln_source_state" {
		t.Errorf("transient tables = %s", got)
	}
	if got := strings.Join(sorted(data), ","); got != "vuln_finding,vuln_finding_event" {
		t.Errorf("data tables = %s", got)
	}
	for _, tb := range tables.All() {
		if strings.HasPrefix(tb.Name, "vuln_") && tb.Class.Exported() != (tb.Name == "vuln_finding" || tb.Name == "vuln_finding_event") {
			t.Errorf("%s: export class %s, exported %t", tb.Name, tb.Class, tb.Class.Exported())
		}
	}

	for _, tb := range transient {
		if n := e.st.Number("SELECT count(*) FROM " + tb); n != 0 {
			t.Errorf("%s has %d rows after the restore", tb, n)
		}
	}
	if n := e.st.Number(`SELECT count(*) FROM vuln_finding`); n != 3 {
		t.Errorf("findings = %d, want 3", n)
	}
	if n := e.st.Number(`SELECT count(*) FROM vuln_finding_event`); n != 6 {
		t.Errorf("events = %d, want 6", n)
	}
	// A restore lifts nothing: no ecosystem has a source state row, so every ecosystem is "empty" (R-VULN-026).
	if n := e.st.Number(`SELECT count(*) FROM vuln_source_state`); n != 0 {
		t.Errorf("source states = %d", n)
	}
}

// TestR_VULN_026_FailSafeBeforeTheFirstSync: with empty advisory tables, the blocked and the malicious version answer 403,
// the dismissed one 200, an evaluation that runs resolves nothing and the system information, the sources gauges and
// the findings gauge show the state (R-VULN-026, R-VULN-011, R-VULN-024).
//
// Needs the work of the other packages to pass: the 403 answers need the block check (work package C), the evaluation that
// resolves nothing needs the evaluation (work package B). The facts and the gauges are this package's.
func TestR_VULN_026_FailSafeBeforeTheFirstSync(t *testing.T) {
	e := newEnv(t)
	e.seedInstallation()
	before := e.snapshot()

	web := e.web("web-1")

	t.Run("the facts of the system information say empty", func(t *testing.T) {
		a := e.st.Do(web, "GET", "/api/v1/system/info", e.tokAdmin, nil)
		if a.Status != 200 {
			t.Fatalf("system info: %d %s", a.Status, a.Body)
		}
		var info struct {
			VulnerabilityData *struct {
				Enforcement string `json:"enforcement"`
				Sources     []struct {
					Source, Ecosystem, Mode, State string
					Stale                          bool
					SyncedAt                       *time.Time
					Failures                       int
				} `json:"sources"`
			} `json:"vulnerabilityData"`
		}
		if err := json.Unmarshal(a.Body, &info); err != nil {
			t.Fatal(err)
		}
		v := info.VulnerabilityData
		if v == nil {
			t.Fatal("the system information has no vulnerabilityData block")
		}
		if v.Enforcement != "enforce" || len(v.Sources) != 2 {
			t.Fatalf("vulnerabilityData = %+v, want enforcement enforce and the sources osv/npm and kev", v)
		}
		for _, s := range v.Sources {
			if s.State != "empty" || s.Stale || s.SyncedAt != nil || s.Failures != 0 {
				t.Errorf("%s/%s = %+v, want state empty, not stale, never synced", s.Source, s.Ecosystem, s)
			}
		}
	})

	t.Run("the gauges show a source that never synced and the findings", func(t *testing.T) {
		var text string
		testenv.WaitFor(t, "the database gauges of the scanning", 20*time.Second, func() bool {
			text = web.Metrics(t)
			return testenv.MetricValue(text, `trove_vuln_findings{kind="malicious",severity="critical",status="needs_triage"}`) >= 0
		})
		// last success 0: the series exists for the configured source, so TroveVulnerabilityDataStale fires on it.
		if v := testenv.MetricValue(text, `trove_vuln_source_last_success_timestamp_seconds{ecosystem="npm",source="osv"}`); v != 0 {
			t.Errorf("last success of osv/npm = %v, want a series with 0", v)
		}
		if v := testenv.MetricValue(text, `trove_vuln_source_records{ecosystem="npm",source="osv"}`); v != 0 {
			t.Errorf("records of osv/npm = %v, want 0", v)
		}
		if v := testenv.MetricValue(text, `trove_vuln_findings{kind="malicious",severity="critical",status="needs_triage"}`); v != 1 {
			t.Errorf("open malicious critical findings = %v, want 1", v)
		}
		if v := testenv.MetricValue(text, `trove_vuln_findings{kind="vulnerability",severity="high",status="dismissed"}`); v != 1 {
			t.Errorf("dismissed high findings = %v, want 1", v)
		}
		if v := testenv.MetricValue(text, `trove_vuln_findings{kind="vulnerability",severity="high",status="blocked"}`); v != 1 {
			t.Errorf("blocked high findings = %v, want 1", v)
		}
		if strings.Contains(text, `source="kev"`) {
			t.Error("a series for the KEV source, whose mode is off")
		}
	})

	t.Run("the blocks hold and the dismissal holds (work package C)", func(t *testing.T) {
		if a := e.download(web, repoLocal, osvstub.NPMMal, "1.0.0"); a.Status != 403 {
			t.Errorf("the open malicious version answers %d, want 403: %s", a.Status, a.Body)
		}
		if a := e.download(web, repoBlocked, osvstub.NPMVuln, "1.0.0"); a.Status != 403 {
			t.Errorf("the blocked version answers %d, want 403: %s", a.Status, a.Body)
		}
		if a := e.download(web, repoLocal, osvstub.NPMVuln, "1.0.0"); a.Status != 200 {
			t.Errorf("the dismissed version answers %d, want 200: %s", a.Status, a.Body)
		}
		if a := e.download(web, repoLocal, "trove-test-clean", "1.0.0"); a.Status != 200 {
			t.Errorf("a version without a finding answers %d, want 200: %s", a.Status, a.Body)
		}
	})

	t.Run("an evaluation without data resolves nothing (work package B)", func(t *testing.T) {
		// The worker has no source (mode off), so the data stays empty. The pending row names an advisory that is not in
		// vuln_advisory: an evaluation that read the absence as "no longer affected" would resolve the findings of
		// GHSA-aaaa-aaaa-0001, which is what R-VULN-011 forbids until an import has completed.
		w := e.st.Start(t, testenv.ReplicaOptions{Name: "worker-off", Role: "worker", Env: []string{"TROVE_VULNERABILITIES_SOURCES_OSV_MODE=off"}})
		e.st.Exec(`INSERT INTO vuln_pending_evaluation (ecosystem, advisory_id) VALUES ('npm', $1)`, osvstub.IDVuln)
		e.st.Exec(`INSERT INTO job (id, kind, queue, args, dedup_key, dedup_mode, max_attempts)
			VALUES (gen_random_uuid(), 'vuln-evaluate', 'default', '{}', 'vuln-evaluate', 'coalesce', 3)`)
		testenv.WaitFor(t, "the evaluation job to end", 60*time.Second, func() bool {
			return e.st.Number(`SELECT count(*) FROM job WHERE kind = 'vuln-evaluate' AND state IN ('succeeded', 'failed')`) == 1
		})
		if n := e.st.Number(`SELECT count(*) FROM job WHERE kind = 'vuln-evaluate' AND state = 'succeeded'`); n != 1 {
			t.Errorf("the evaluation job did not succeed: %s", e.st.Text(`SELECT COALESCE(error::text, '') FROM job WHERE kind = 'vuln-evaluate'`))
		}
		requireHeld(t, "after an evaluation without data", before, e.snapshot())
		if n := e.st.Number(`SELECT count(*) FROM vuln_finding WHERE status = 'resolved'`); n != 0 {
			t.Errorf("%d findings resolved without data", n)
		}
		if err := w.Stop(); err != nil {
			t.Fatal(err)
		}
		// The pending row and its job are state of this check, not of the restore.
		e.st.Exec(`DELETE FROM vuln_pending_evaluation`)
	})

	requireHeld(t, "before the first sync", before, e.snapshot())
}

// TestR_VULN_026_AfterTheSyncAndTheFullEvaluation: the first sync after a restore imports the data, the full evaluation
// follows, and the installation is as before the restore: the dismissal, the block and the open malicious finding hold, the
// history of every finding is intact and the downloads answer as before (R-VULN-026).
//
// Needs the work of the other packages: the sync (work package A, the sources of the test advisory source and the job kinds
// vuln-sync-osv), the full evaluation after the first complete import (work package B, vuln_source_state.
// full_evaluation_requested_at) and the block check (work package C).
func TestR_VULN_026_AfterTheSyncAndTheFullEvaluation(t *testing.T) {
	e := newEnv(t)
	e.seedInstallation()
	before := e.snapshot()

	web := e.web("web-1")
	e.worker("worker-1")

	a := e.st.Do(web, "POST", "/api/v1/system/tasks/vuln-sync-osv/run", e.tokAdmin, nil)
	if a.Status != 202 {
		t.Fatalf("run vuln-sync-osv: %d %s", a.Status, a.Body)
	}

	testenv.WaitFor(t, "the first import of npm to complete", 120*time.Second, func() bool {
		return e.st.Text(`SELECT COALESCE((SELECT state FROM vuln_source_state WHERE source = 'osv' AND ecosystem = 'npm'), 'none')`) == "complete"
	})
	testenv.WaitFor(t, "the full evaluation to finish", 120*time.Second, func() bool {
		return e.st.Number(`SELECT count(*) FROM vuln_source_state WHERE source = 'osv' AND ecosystem = 'npm' AND full_evaluation_requested_at IS NULL`) == 1 &&
			e.st.Number(`SELECT count(*) FROM job WHERE kind = 'vuln-evaluate' AND state IN ('queued', 'running')`) == 0 &&
			e.st.Number(`SELECT count(*) FROM job WHERE kind = 'vuln-evaluate' AND state = 'succeeded'`) >= 1
	})

	requireHeld(t, "after the sync and the full evaluation", before, e.snapshot())
	if n := e.st.Number(`SELECT count(*) FROM vuln_finding`); n != 3 {
		t.Errorf("findings = %d after the evaluation, want the 3 of before (re-detection must find the existing ones)", n)
	}
	if n := e.st.Number(`SELECT count(*) FROM vuln_finding WHERE status = 'resolved'`); n != 0 {
		t.Errorf("%d findings resolved", n)
	}
	if n := e.st.Number(`SELECT count(*) FROM vuln_advisory WHERE ecosystem = 'npm' AND id IN ($1, $2)`, osvstub.IDVuln, osvstub.IDMal); n != 2 {
		t.Errorf("the test advisories are not in vuln_advisory (%d of 2)", n)
	}

	if a := e.download(web, repoLocal, osvstub.NPMMal, "1.0.0"); a.Status != 403 {
		t.Errorf("the malicious version answers %d, want 403", a.Status)
	}
	if a := e.download(web, repoBlocked, osvstub.NPMVuln, "1.0.0"); a.Status != 403 {
		t.Errorf("the blocked version answers %d, want 403", a.Status)
	}
	if a := e.download(web, repoLocal, osvstub.NPMVuln, "1.0.0"); a.Status != 200 {
		t.Errorf("the dismissed version answers %d, want 200", a.Status)
	}

	t.Run("the gauges and the facts show the synced state", func(t *testing.T) {
		var text string
		testenv.WaitFor(t, "the last success gauge", 20*time.Second, func() bool {
			text = web.Metrics(t)
			return testenv.MetricValue(text, `trove_vuln_source_last_success_timestamp_seconds{ecosystem="npm",source="osv"}`) > 1e9
		})
		if v := testenv.MetricValue(text, `trove_vuln_source_consecutive_failures{ecosystem="npm",source="osv"}`); v != 0 {
			t.Errorf("failures = %v", v)
		}
		if v := testenv.MetricValue(text, `trove_vuln_source_records{ecosystem="npm",source="osv"}`); v < 2 {
			t.Errorf("records = %v, want at least the two test advisories", v)
		}
	})
}

func sorted(in []string) []string {
	out := append([]string(nil), in...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
