//go:build integration

package vulnfindings

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/evaluate"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// R-VULN-011: 100 syncs that each change one record within a minute cause at most two concurrent or queued vuln-evaluate
// jobs: the enqueue coalesces, and the work is read from vuln_pending_evaluation, not from the arguments.
func TestR_VULN_011_ManyEnqueuesCoalesceIntoAtMostTwoJobs(t *testing.T) {
	e := newEnvRole(t, "web")
	for i := range 100 {
		id := fmt.Sprintf("GHSA-aaaa-aaaa-%04d", i)
		e.addAdvisory("npm", id, "", "vulnerability", "low")
		e.queue("npm", id)
		if _, err := e.rep.App.Queue().EnqueueNow(context.Background(), jobs.EnqueueSpec{Kind: vuln.KindEvaluate, DedupKey: vuln.DedupEvaluate}); err != nil {
			t.Fatal(err)
		}
	}
	if got := e.st.Number(`SELECT count(*) FROM job WHERE kind = 'vuln-evaluate' AND state IN ('queued', 'running')`); got > 2 {
		t.Errorf("%d vuln-evaluate jobs are queued or running", got)
	}
	if got := e.st.Number(`SELECT count(*) FROM vuln_pending_evaluation`); got != 100 {
		t.Errorf("%d advisories are queued, want all 100", got)
	}
}

// R-VULN-011: a raised matcher version (a full evaluation requested in the table) while an incremental job waits: the full
// evaluation still runs, once, by the job that was waiting.
func TestR_VULN_011_AFullEvaluationRequestedWhileAnIncrementalJobWaitsStillRuns(t *testing.T) {
	e := newEnvRole(t, "web")
	e.setComplete("npm", true)
	e.addAdvisory("npm", "GHSA-aaaa-aaaa-0001", "", "vulnerability", "high")
	e.addAffected("npm", "GHSA-aaaa-aaaa-0001", 0, "trove-test-vuln", everyVersionRange, nil, true, nil)
	e.addVersion("npm-remote", "trove-test-vuln", "1.0.0")
	q := e.rep.App.Queue()
	first, err := q.EnqueueNow(context.Background(), jobs.EnqueueSpec{Kind: vuln.KindEvaluate, DedupKey: vuln.DedupEvaluate})
	if err != nil {
		t.Fatal(err)
	}
	// The request arrives after the job was enqueued; its own enqueue returns the waiting job and drops its arguments.
	e.st.Exec(`UPDATE vuln_source_state SET full_evaluation_requested_at = now() WHERE source = 'osv' AND ecosystem = 'npm'`)
	second, err := q.EnqueueNow(context.Background(), jobs.EnqueueSpec{Kind: vuln.KindEvaluate, DedupKey: vuln.DedupEvaluate})
	if err != nil {
		t.Fatal(err)
	}
	if second.Created || second.Job.ID != first.Job.ID {
		t.Fatalf("the second enqueue created a job: %+v", second)
	}
	e.st.Start(t, testenv.ReplicaOptions{Name: "worker-a", Role: "worker", Env: []string{"TROVE_JOBS_POLL_INTERVAL=200ms"}})
	e.waitJob(first.Job.ID, vuln.KindEvaluate)
	if got := e.findings("npm-remote", "trove-test-vuln", "1.0.0", ""); got != 1 {
		t.Errorf("the full evaluation produced %d findings", got)
	}
	if got := e.st.Number(`SELECT count(*) FROM vuln_source_state WHERE full_evaluation_requested_at IS NOT NULL`); got != 0 {
		t.Errorf("the request is still set")
	}
	if got := e.st.Number(`SELECT count(*) FROM job WHERE kind = 'vuln-evaluate'`); got != 1 {
		t.Errorf("%d jobs ran", got)
	}
}

// R-VULN-011: a rescan twice answers the same job (the API's 202 and 409), exclusive per repository.
func TestR_VULN_011_ARescanIsExclusivePerRepository(t *testing.T) {
	e := newEnvRole(t, "web")
	id := e.st.Number(`SELECT id FROM repositories WHERE key = 'npm-remote'`)
	spec := jobs.EnqueueSpec{Kind: vuln.KindRescan, Args: evaluate.RescanArgs{Repository: "npm-remote", RepositoryID: id}, DedupKey: vuln.RescanKey(id)}
	first, err := e.rep.App.Queue().EnqueueNow(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.rep.App.Queue().EnqueueNow(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created || second.Created || second.Job.ID != first.Job.ID {
		t.Fatalf("first %+v, second %+v", first, second)
	}
	other := e.st.Number(`SELECT id FROM repositories WHERE key = 'npm-mirror'`)
	third, err := e.rep.App.Queue().EnqueueNow(context.Background(), jobs.EnqueueSpec{Kind: vuln.KindRescan, Args: evaluate.RescanArgs{Repository: "npm-mirror", RepositoryID: other}, DedupKey: vuln.RescanKey(other)})
	if err != nil || !third.Created {
		t.Fatalf("another repository's rescan: %+v, %v", third, err)
	}
}

// fakeRun is the handle of a job that runs in the test: it records the checkpoints and lets the test act between two batches.
type fakeRun struct {
	mu          sync.Mutex
	checkpoints int
	progress    [][2]int64
	afterBatch  func(n int)
	result      any
}

func (f *fakeRun) LoadCheckpoint(any) (bool, error) { return false, nil }
func (f *fakeRun) SaveCheckpoint(context.Context, any) error {
	f.mu.Lock()
	f.checkpoints++
	n := f.checkpoints
	hook := f.afterBatch
	f.mu.Unlock()
	if hook != nil {
		hook(n)
	}
	return nil
}
func (f *fakeRun) SaveProgress(_ context.Context, done, total int64, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.progress = append(f.progress, [2]int64{done, total})
	return nil
}
func (f *fakeRun) SetResult(v any) error { f.result = v; return nil }

// service builds an evaluation service of its own over the replica's repository snapshot, for the tests that act between two
// batches of a job or ask the miss of a remote repository.
func (e *env) service(o evaluate.Options) *evaluate.Service {
	e.t.Helper()
	db, err := postgres.Open(context.Background(), e.st.DatabaseConfig(), "vulnfind-svc", "web", testenv.Quiet())
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(db.Close)
	o.Runner, o.Repos = db, e.rep.App.Repositories()
	if o.Ecosystems == nil {
		o.Ecosystems = ecosystems()
	}
	o.Enabled = true
	if o.PublicUpstreams == nil {
		o.PublicUpstreams = map[string][]string{"npm": {"registry.npmjs.org"}, "nuget": {"api.nuget.org", "www.nuget.org"}}
	}
	svc, err := evaluate.New(o)
	if err != nil {
		e.t.Fatal(err)
	}
	return svc
}

// R-VULN-011: a matchPublicAdvisories change while a rescan runs is applied to every version before the job ends: the job reads
// the repository's scanning inputs at its start and again after its last batch, and runs once more when they differ.
func TestR_VULN_011_ASettingChangedWhileARescanRunsIsAppliedBeforeItEnds(t *testing.T) {
	e := newEnvRole(t, "web")
	e.setComplete("npm", true)
	for i := range 5 {
		name := fmt.Sprintf("trove-test-n%d", i)
		id := fmt.Sprintf("MAL-2026-90%02d", i)
		e.addAdvisory("npm", id, "", "malicious", "critical")
		e.addAffected("npm", id, 0, name, everyVersionRange, nil, true, nil)
		e.addVersion("npm-mirror", name, "1.0.0")
	}
	svc := e.service(evaluate.Options{BatchNames: 2})
	repoID := e.st.Number(`SELECT id FROM repositories WHERE key = 'npm-mirror'`)
	args, _ := json.Marshal(evaluate.RescanArgs{Repository: "npm-mirror", RepositoryID: repoID})
	run := &fakeRun{afterBatch: func(n int) {
		if n != 1 {
			return
		}
		// The setting changes after the first batch, as a PATCH answered by the running job's enqueue would.
		e.st.Exec(`UPDATE repositories SET config = jsonb_set(config, '{vulnerabilities}', '{"matchPublicAdvisories":"always"}') WHERE key = 'npm-mirror'`)
	}}
	if err := svc.RunRescan(context.Background(), run, args); err != nil {
		t.Fatal(err)
	}
	if got := e.findings("npm-mirror", "", "", ""); got != 5 {
		t.Errorf("%d findings after the rescan, want 5: the change was not applied to every version", got)
	}
	if run.checkpoints < 3 {
		t.Errorf("%d checkpoints: the job did not run in batches", run.checkpoints)
	}
	if m, ok := run.result.(map[string]int); !ok || m["rounds"] != 2 {
		t.Errorf("result %v, want two rounds", run.result)
	}
}

// R-VULN-002, R-VULN-012: turning scanning off resolves the repository's findings with scanning_disabled; turning it on again
// re-detects them with their earlier statuses. A change of blockMalicious enqueues no rescan.
func TestR_VULN_002_DisablingScanningResolvesAndEnablingItRedetectsWithTheEarlierStatus(t *testing.T) {
	e := newEnv(t)
	e.setComplete("npm", true)
	e.addAdvisory("npm", "GHSA-aaaa-aaaa-0001", "", "vulnerability", "high")
	e.addAffected("npm", "GHSA-aaaa-aaaa-0001", 0, "trove-test-vuln", everyVersionRange, nil, true, nil)
	e.addVersion("npm-remote", "trove-test-vuln", "1.0.0")
	e.addVersion("npm-remote", "trove-test-vuln", "1.1.0")
	e.rescan("npm-remote")
	e.st.Exec(`UPDATE vuln_finding SET status = 'confirmed' WHERE version = '1.0.0'`)
	e.st.Exec(`UPDATE vuln_finding SET status = 'dismissed', dismissal_reason = 'test_only' WHERE version = '1.1.0'`)

	jobsBefore := e.st.Number(`SELECT count(*) FROM job WHERE kind = 'vuln-rescan'`)
	if a := e.patch("npm-remote", `{"vulnerabilities":{"blockMalicious":false}}`); a.Status != 200 {
		t.Fatalf("patch: %d %s", a.Status, a.Body)
	}
	if got := e.st.Number(`SELECT count(*) FROM job WHERE kind = 'vuln-rescan'`); got != jobsBefore {
		t.Errorf("a change of blockMalicious enqueued %d rescans", got-jobsBefore)
	}

	if a := e.patch("npm-remote", `{"vulnerabilities":{"enabled":false}}`); a.Status != 200 {
		t.Fatalf("patch: %d %s", a.Status, a.Body)
	}
	testenv.WaitFor(t, "the resolution", 30*time.Second, func() bool {
		return e.st.Number(`SELECT count(*) FROM vuln_finding WHERE status = 'resolved' AND resolved_reason = 'scanning_disabled'`) == 2
	})
	if a := e.patch("npm-remote", `{"vulnerabilities":{"enabled":true}}`); a.Status != 200 {
		t.Fatalf("patch: %d %s", a.Status, a.Body)
	}
	testenv.WaitFor(t, "the re-detection", 30*time.Second, func() bool {
		return e.st.Number(`SELECT count(*) FROM vuln_finding WHERE status <> 'resolved'`) == 2
	})
	if got, want := e.status("npm-remote", "trove-test-vuln", "1.0.0"), "confirmed"; got != want {
		t.Errorf("1.0.0 is %q, want %q", got, want)
	}
	if got, want := e.status("npm-remote", "trove-test-vuln", "1.1.0"), "dismissed"; got != want {
		t.Errorf("1.1.0 is %q, want %q", got, want)
	}
	if got := e.history("npm-remote", "trove-test-vuln", "1.0.0"); got != "detected,resolved,redetected" {
		t.Errorf("history %q", got)
	}
}

// R-VULN-014: with resolved_retention 1h, a finding resolved two hours ago is gone after one run of the task, together with its
// events; an open one of the same age and one resolved half an hour ago stay.
func TestR_VULN_014_TheRetentionTaskDeletesOldResolvedFindingsWithTheirEvents(t *testing.T) {
	e := newEnv(t, "TROVE_VULNERABILITIES_FINDINGS_RESOLVED_RETENTION=1h")
	e.addVersion("npm-remote", "trove-test-vuln", "1.0.0")
	insert := func(version, status, resolvedAgo string) {
		resolved := "NULL"
		if resolvedAgo != "" {
			resolved = "now() - interval '" + resolvedAgo + "'"
		}
		e.st.Exec(fmt.Sprintf(`INSERT INTO vuln_finding (id, repo_id, ecosystem, package_name, version, advisory_id, kind, severity, severity_rank,
			fix_available, status, resolved_reason, resolved_at, advisory_modified, detected_at)
			SELECT gen_random_uuid(), r.id, 'npm', 'trove-test-vuln', $2, 'GHSA-aaaa-aaaa-0001', 'vulnerability', 'high', 4, false, $3::text,
			       CASE WHEN $3::text = 'resolved' THEN 'version_removed' END, %s, now(), now() - interval '3 hours'
			  FROM repositories r WHERE r.key = $1::text`, resolved), "npm-remote", version, status)
	}
	insert("1.0.0", "resolved", "2 hours")
	insert("1.0.1", "resolved", "30 minutes")
	insert("1.0.2", "needs_triage", "")
	e.st.Exec(`INSERT INTO vuln_finding_event (finding_id, change) SELECT id, 'detected' FROM vuln_finding`)
	e.run(vuln.KindFindingsRetention, "", nil)
	if got := e.st.Text(`SELECT COALESCE(string_agg(version, ',' ORDER BY version), '') FROM vuln_finding`); got != "1.0.1,1.0.2" {
		t.Errorf("the findings left are %q, want 1.0.1,1.0.2", got)
	}
	if got := e.st.Number(`SELECT count(*) FROM vuln_finding_event`); got != 2 {
		t.Errorf("%d events left, want 2", got)
	}
}
