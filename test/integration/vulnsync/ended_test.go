//go:build integration

package vulnsync

import (
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/osvstub"
)

// The tests of a run that ends before its sync does, other than by a drain (R-VULN-004 "Failure", Q-VULN-80): a lost lease,
// a panic of the handler, an operator's cancel, and a run that lost its lease to a newer one. Each runs through the worker of
// the installation: the heartbeat that finds the lease gone or the job cancelling, and the worker's own recovery of a panic.
// The ecosystems run in sorted order, so NuGet is the first; a delay on its archive holds the run inside NuGet's sync.

// endedEnv is the environment of the tests: a lease of 3 s renewed every second, so that the heartbeat judges a job within a
// second, and the database gauges read every second.
func endedEnv(t *testing.T) *env {
	t.Helper()
	return newEnv(t, options{Env: []string{
		"TROVE_JOBS_HEARTBEAT_INTERVAL=1s", "TROVE_JOBS_LEASE_DURATION=3s", "TROVE_METRICS_DB_GAUGE_INTERVAL=1s",
	}})
}

// holdNuGet makes the source answer NuGet's archive after a minute: a run that starts waits inside NuGet's sync, after its
// attempt of the ecosystem committed.
func (e *env) holdNuGet() {
	e.t.Helper()
	if err := e.stub.SetFault("NuGet", osvstub.Fault{Scope: "archive", DelayMS: 60000}); err != nil {
		e.t.Fatal(err)
	}
}

// startHeld starts the task through the management API and returns its job once the run waits in NuGet's sync: the job is
// running and NuGet's attempted_at is no longer before, the value the row had before the run.
func (e *env) startHeld(task, before string) string {
	e.t.Helper()
	e.waitIdle(task)
	if res := e.st.Do(e.web, "POST", "/api/v1/system/tasks/"+task+"/run", e.admin, nil); res.Status != 202 {
		e.t.Fatalf("running %s: %d %s", task, res.Status, res.Body)
	}
	var id string
	within(e.t, "the run waiting in the sync of NuGet", 30*time.Second, func() bool {
		id = e.text("SELECT COALESCE((SELECT id::text FROM job WHERE kind = $1 AND state = 'running' ORDER BY created_at DESC LIMIT 1), '')", task)
		return id != "" && e.stateOf("attempted_at", "osv", "NuGet") != before
	})
	return id
}

// reap ends the job as the reaper ends one whose lease ran out on its last attempt: the worker that runs it is alive, and its
// next heartbeat no longer finds the job.
func (e *env) reap(id string) {
	e.t.Helper()
	e.st.Exec(`UPDATE job SET state = 'failed', error = '{"code":"attempts_exhausted","detail":"the lease expired on the last attempt"}'::jsonb,
		locked_by = NULL, locked_until = NULL, finished_at = now() WHERE id = $1::uuid`, id)
}

// workerCount reads a series of the worker's metrics.
func (e *env) workerCount(series string) float64 {
	e.t.Helper()
	return testenv.MetricValue(e.worker.Metrics(e.t), series)
}

// failuresGauge waits until the gauge of NuGet's consecutive failures on the web replica shows want.
func (e *env) failuresGauge(want float64) {
	e.t.Helper()
	const series = `trove_vuln_source_consecutive_failures{ecosystem="NuGet",source="osv"}`
	within(e.t, "the consecutive failures of NuGet in the gauge", 20*time.Second, func() bool {
		return testenv.MetricValue(e.web.Metrics(e.t), series) == want
	})
}

// R-VULN-004 "Failure": a run whose worker loses the lease records sync_lease_lost on the ecosystem it was syncing and counts
// one more consecutive failure, which the gauge of TroveVulnerabilitySyncFailing shows. The ecosystem it had not reached keeps
// its row as it was, and the job keeps the reaper's verdict.
func TestR_VULN_004_ARunThatLosesItsLeaseRecordsAFailure(t *testing.T) {
	e := endedEnv(t)
	e.holdNuGet()
	id := e.startHeld(taskOSVFull, "")
	e.reap(id)
	within(t, "the worker ending the run it lost", 20*time.Second, func() bool {
		return e.workerCount(`trove_jobs_completed_total{kind="vuln-sync-osv-full",outcome="lost"}`) == 1
	})
	if got := e.lastErrorCode("osv", "NuGet"); got != "sync_lease_lost" {
		t.Fatalf("last_error code %q, want sync_lease_lost\nlog of the worker:\n%s", got, testenv.Tail(e.worker.Logs.String(), 20))
	}
	if got := e.stateOf("failures", "osv", "NuGet"); got != "1" {
		t.Errorf("failures %s, want 1", got)
	}
	if got := e.text("SELECT last_error->>'status' FROM vuln_source_state WHERE source = 'osv' AND ecosystem = 'NuGet'"); got != "503" {
		t.Errorf("status of the problem %q, want 503", got)
	}
	if got := e.stateOf("state", "osv", "NuGet"); got != "empty" {
		t.Errorf("state %q", got)
	}
	if got := e.stateOf("attempted_at", "osv", "npm"); got != "" {
		t.Errorf("npm, which the run had not reached, was attempted at %s", got)
	}
	if got := e.latest(taskOSVFull); got.State != "failed" || !strings.Contains(got.Error, "attempts_exhausted") {
		t.Errorf("the job is %s %s, want the reaper's failed attempts_exhausted", got.State, got.Error)
	}
	if v := e.workerCount(`trove_vuln_sync_runs_total{ecosystem="NuGet",kind="full",outcome="failed",source="osv"}`); v != 1 {
		t.Errorf("failed runs counted: %v", v)
	}
	e.failuresGauge(1)
	within(t, "the audit event of the failure", 20*time.Second, func() bool {
		return e.number("SELECT count(*) FROM audit_event WHERE action = 'vulnerability.source.failed' AND details->>'ecosystem' = 'NuGet' AND details->>'code' = 'sync_lease_lost'") == 1
	})
}

// R-VULN-004 "Failure": a run whose handler panics records sync_panicked on the ecosystem it was syncing, with no word of the
// panic for a reader, and the worker still ends the job failed.
func TestR_VULN_004_ARunWhoseHandlerPanicsRecordsAFailure(t *testing.T) {
	e := endedEnv(t)
	svc := e.worker.App.VulnSync()
	svc.SetBatchHook(func(eco string, _ int) {
		if eco == "NuGet" {
			panic("a fault the test injects after the first batch")
		}
	})
	t.Cleanup(func() { svc.SetBatchHook(nil) })
	j := e.run(taskOSVFull, nil)
	if j.State != "failed" || !strings.Contains(j.Error, "handler panicked") {
		t.Fatalf("the job ended %s %s, want failed by the worker's recovery", j.State, j.Error)
	}
	if got := e.lastErrorCode("osv", "NuGet"); got != "sync_panicked" {
		t.Fatalf("last_error code %q, want sync_panicked", got)
	}
	if got := e.stateOf("failures", "osv", "NuGet"); got != "1" {
		t.Errorf("failures %s, want 1", got)
	}
	if body := e.stateOf("last_error", "osv", "NuGet"); strings.Contains(body, "fault the test injects") {
		t.Errorf("the value of the panic reaches last_error: %s", body)
	}
	if !strings.Contains(e.worker.Logs.String(), "fault the test injects") {
		t.Error("the log of the worker does not hold the panic")
	}
	if v := e.workerCount(`trove_vuln_sync_runs_total{ecosystem="NuGet",kind="full",outcome="failed",source="osv"}`); v != 1 {
		t.Errorf("failed runs counted: %v", v)
	}
	e.failuresGauge(1)
}

// R-VULN-004 "Failure", Q-VULN-80: a run whose job an operator cancels records sync_cancelled and leaves the consecutive failures
// as they are, neither raised nor reset; the rate limit of the failure event is kept, and no failure event is written.
func TestR_VULN_004_ACancelledRunRecordsItsEndWithoutAFailure(t *testing.T) {
	e := endedEnv(t)
	if err := e.stub.SetFault("NuGet", osvstub.Fault{Scope: "archive", Error: 500}); err != nil {
		t.Fatal(err)
	}
	if got := e.mustFail(taskOSVFull, "osv", "NuGet", nil); got != "upstream_unexpected_status" {
		t.Fatalf("code %q", got)
	}
	eventAt := e.text("SELECT COALESCE((SELECT last_error->>'eventAt' FROM vuln_source_state WHERE source = 'osv' AND ecosystem = 'NuGet'), '')")
	e.failuresGauge(1)

	e.holdNuGet()
	id := e.startHeld(taskOSVFull, e.stateOf("attempted_at", "osv", "NuGet"))
	if res := e.st.Do(e.web, "POST", "/api/v1/system/jobs/"+id+"/cancel", e.admin, nil); res.Status != 202 {
		t.Fatalf("cancel: %d %s", res.Status, res.Body)
	}
	within(t, "the job cancelled", 20*time.Second, func() bool {
		return e.text("SELECT state::text FROM job WHERE id = $1::uuid", id) == "cancelled"
	})
	if got := e.lastErrorCode("osv", "NuGet"); got != "sync_cancelled" {
		t.Fatalf("last_error code %q, want sync_cancelled", got)
	}
	if got := e.stateOf("failures", "osv", "NuGet"); got != "1" {
		t.Errorf("failures %s after a cancel, want the 1 of the failure before it", got)
	}
	if got := e.text("SELECT COALESCE((SELECT last_error->>'eventAt' FROM vuln_source_state WHERE source = 'osv' AND ecosystem = 'NuGet'), '')"); got != eventAt || got == "" {
		t.Errorf("eventAt %q after the cancel, want the failure's %q carried forward", got, eventAt)
	}
	if v := e.workerCount(`trove_vuln_sync_runs_total{ecosystem="NuGet",kind="full",outcome="cancelled",source="osv"}`); v != 1 {
		t.Errorf("cancelled runs counted: %v", v)
	}
	if v := e.workerCount(`trove_vuln_sync_runs_total{ecosystem="NuGet",kind="full",outcome="failed",source="osv"}`); v != 1 {
		t.Errorf("failed runs counted: %v, want the 1 of the failure before the cancel", v)
	}
	// Three refreshes of the gauge after the cancel: it still shows the failure before it.
	time.Sleep(3 * time.Second)
	e.failuresGauge(1)
	within(t, "the audit event of the cancel", 20*time.Second, func() bool {
		return e.number("SELECT count(*) FROM audit_event WHERE action = 'system.job.cancelled' AND resource_id = $1", id) == 1
	})
	if got := e.number("SELECT count(*) FROM audit_event WHERE action = 'vulnerability.source.failed' AND details->>'code' = 'sync_cancelled'"); got != 0 {
		t.Errorf("%d failure events of a cancel", got)
	}
}

// R-VULN-004 "Failure", Q-VULN-80: a run that lost its lease writes no failure on a row that a newer run has attempted since:
// the newer run's state stays, and the stale run is still counted failed by its worker.
func TestR_VULN_004_ARunThatLostItsLeaseLeavesANewerRunsStateAsItIs(t *testing.T) {
	e := endedEnv(t)
	e.holdNuGet()
	id := e.startHeld(taskOSVFull, "")
	// The row first, then the job, so that the heartbeat cannot judge the job before the newer attempt is on the row: the
	// newer run's attempt is what VulnStateAttempt writes.
	e.st.Exec(`UPDATE vuln_source_state SET attempted_at = now() WHERE source = 'osv' AND ecosystem = 'NuGet'`)
	newer := e.stateOf("attempted_at", "osv", "NuGet")
	e.reap(id)
	within(t, "the worker ending the run it lost", 20*time.Second, func() bool {
		return e.workerCount(`trove_jobs_completed_total{kind="vuln-sync-osv-full",outcome="lost"}`) == 1
	})
	if got := e.stateOf("failures", "osv", "NuGet"); got != "0" {
		t.Errorf("failures %s, want the newer run's 0", got)
	}
	if got := e.stateOf("last_error", "osv", "NuGet"); got != "" {
		t.Errorf("last_error %s over the newer run's state", got)
	}
	if got := e.stateOf("attempted_at", "osv", "NuGet"); got != newer {
		t.Errorf("attempted_at %s, want the newer run's %s", got, newer)
	}
	if v := e.workerCount(`trove_vuln_sync_runs_total{ecosystem="NuGet",kind="full",outcome="failed",source="osv"}`); v != 1 {
		t.Errorf("failed runs counted: %v", v)
	}
	if !strings.Contains(e.worker.Logs.String(), "a newer run has attempted the ecosystem") {
		t.Errorf("the worker does not log the failure it did not record\n%s", testenv.Tail(e.worker.Logs.String(), 20))
	}
}
