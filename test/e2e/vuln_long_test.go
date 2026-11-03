//go:build e2e

package e2e

// The long tests of the vulnerability scanning (R-VULN-029, E4b.1, E4b.8; owner rule of 2026-11-01: drain, failover and
// soak run in the nightly workflow, on dispatch and locally, never in Test and Release): a full synchronization drained
// mid-run, and the client tests of the short set once more with Valkey stopped.

import (
	"encoding/json"
	"net/http"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/osvstub"
)

// vulnRunSuffix names the reports of a repeated run (the matrix report of the npm clients keys its cells by run).
var vulnRunSuffix string

// vulnSample is one reading of the job of the drained synchronization.
type vulnSample struct {
	state, attempt, replica, batches string
}

// sampleJob reads the job once; ok is false when the query failed (a failover of the database moves the primary).
func sampleJob(id string) (s vulnSample, ok bool) {
	primary, err := kubectl("-n", cfg.namespace, "get", "cluster", "trove-db", "-o", "jsonpath={.status.currentPrimary}")
	if err != nil || strings.TrimSpace(primary) == "" {
		return s, false
	}
	out, err := kubectl("-n", cfg.namespace, "exec", strings.TrimSpace(primary), "-c", "postgres", "--", "psql", "-U", "postgres", "-d", "trove", "-At", "-F", "|", "-c",
		`SELECT j.state, j.attempt, coalesce(r.name, ''), coalesce(j.checkpoint->>'batches', '') FROM trove.job j LEFT JOIN trove.replica r ON r.instance_uid = j.locked_by WHERE j.id = '`+id+`'`)
	f := strings.Split(strings.TrimSpace(out), "|")
	if err != nil || len(f) != 4 {
		return s, false
	}
	return vulnSample{state: f[0], attempt: f[1], replica: f[2], batches: f[3]}, true
}

// avoidPrimary moves the PostgreSQL primary away from the node first: its PodDisruptionBudget refuses the eviction of the
// primary's pod, as the drain test of E0.14 does it.
func avoidPrimary(t *testing.T, node string) {
	t.Helper()
	if strings.TrimSpace(kn(t, "get", "pod", pgPrimary(t), "-o", "jsonpath={.spec.nodeName}")) == node {
		t.Logf("the PostgreSQL primary runs on %s: switching it over first", node)
		script(t, "failover.sh")
	}
}

// TestVulnLong_R_VULN_004_FullSyncDrainedMidRun is the criterion of R-VULN-004: `kubectl drain` of the node that runs a full
// synchronization. The test source holds 100,000 filler records (the most the source holds), so that the run outlasts the drain; the drain starts once the
// run has committed batches. The job is `queued` again with its attempt unchanged, another worker completes it, and the
// ecosystem holds every record at the end.
func TestVulnLong_R_VULN_004_FullSyncDrainedMidRun(t *testing.T) {
	e := vulnFx(t)
	osv := osvStub(t)
	const filler = 100000
	t.Cleanup(func() {
		// the dataset goes back to the fixture and the stored data follows it (the shrink guard lifted once)
		osv.send(t, http.MethodPost, "/-/stub/reset?all=true", nil)
		for _, r := range e.records { // the records of this run's packages go back: later tests of the process read them
			osv.addRecord(t, r.ecosystem, r.advisory)
		}
		e.syncFull(t, true)
	})
	osv.send(t, http.MethodPut, "/-/stub/filler/npm", map[string]any{"count": filler})
	var info osvstub.Info
	if err := json.Unmarshal(osv.send(t, http.MethodGet, "/-/stub/dataset", nil), &info); err != nil {
		t.Fatal(err)
	}
	want := info.Ecosystems["npm"].Records
	t.Logf("the test source holds %d npm records, an archive of %d bytes", want, info.Ecosystems["npm"].ArchiveBytes)

	id, running := e.task(t, "vuln-sync-osv-full", nil)
	if running {
		t.Fatal("a synchronization was running when the long test started")
	}
	t.Logf("job %s", id)

	// the job runs on a worker and has committed a batch
	var first vulnSample
	waitFor(t, "the full synchronization to commit its first batches", 10*time.Minute, time.Second, func() bool {
		s, ok := sampleJob(id)
		first = s
		return ok && s.state == "running" && s.batches != "" && s.batches != "0"
	})
	node := strings.TrimSpace(kn(t, "get", "pod", first.replica, "-o", "jsonpath={.spec.nodeName}"))
	t.Logf("the job runs on %s (node %s), attempt %s, %s batches committed", first.replica, node, first.attempt, first.batches)
	avoidPrimary(t, node)
	if cur, ok := sampleJob(id); !ok || cur.state != "running" {
		t.Fatalf("the job is not running any more before the drain started: %+v: the dataset is too small for the cluster's speed", cur)
	}

	// the drain runs in a goroutine, the job is read throughout
	drainDone := make(chan error, 1)
	var drainOut []byte
	drainStart := time.Now()
	go func() {
		cmd := exec.Command(filepath.Join(cfg.repo, "deploy/dev/drain.sh"), node)
		cmd.Dir = cfg.repo
		var err error
		drainOut, err = cmd.CombinedOutput()
		drainDone <- err
	}()
	// A release at shutdown does not count an attempt: the claim of the first run raised it to first.attempt, the release gives it
	// back (the queued job shows one less, R-ARCH-016), the claim of the next worker raises it again. Anything else is a drift.
	firstAttempt, _ := strconv.Atoi(first.attempt)
	firstBatches, _ := strconv.Atoi(first.batches)
	replicas := map[string]bool{first.replica: true}
	queuedAgain, drifted, drained := false, "", false
	resumedAt := -1 // the batches the checkpoint held when another worker's run showed its first sample
	deadline := time.Now().Add(20 * time.Minute)
	for time.Now().Before(deadline) {
		if !drained {
			select {
			case err := <-drainDone:
				drained = true
				t.Logf("drain.sh returned after %s (error %v):\n%s", time.Since(drainStart).Round(time.Second), err, strings.TrimSpace(string(drainOut)))
				if err != nil {
					t.Fatalf("drain.sh: %v", err)
				}
			default:
			}
		}
		s, ok := sampleJob(id)
		if !ok {
			time.Sleep(time.Second)
			continue
		}
		if s.replica != "" {
			replicas[s.replica] = true
		}
		queuedAgain = queuedAgain || s.state == "queued"
		if n, _ := strconv.Atoi(s.attempt); n != firstAttempt && n != firstAttempt-1 {
			drifted = s.attempt
		}
		if s.state == "running" && s.replica != first.replica && resumedAt < 0 {
			resumedAt, _ = strconv.Atoi(s.batches)
		}
		if s.state == "succeeded" || s.state == "failed" || s.state == "cancelled" {
			t.Logf("the job ended %s", s.state)
			if s.state != "succeeded" {
				t.Fatalf("the drained synchronization ended %s: %s", s.state, sql(t, `SELECT coalesce(error::text, '') FROM trove.job WHERE id = '`+id+`'`))
			}
			break
		}
	}
	if !drained {
		if err := <-drainDone; err != nil {
			t.Fatalf("drain.sh: %v\n%s", err, drainOut)
		}
	}
	final := sql(t, `SELECT state || '|' || attempt FROM trove.job WHERE id = '`+id+`'`)
	t.Logf("the job ran on %v, was queued again: %v, attempt before the drain %s, at the end %s", replicas, queuedAgain, first.attempt, final)
	if final != "succeeded|"+first.attempt {
		t.Errorf("the job ended %q, want succeeded with the attempt of before the drain (%s): a release at shutdown does not count an attempt", final, first.attempt)
	}
	if drifted != "" {
		t.Errorf("the job's attempt was %s at some point, %d before the drain: a release at shutdown does not count an attempt", drifted, firstAttempt)
	}
	t.Logf("the job had committed %d batches when it was drained; the first run of another worker showed %d", firstBatches, resumedAt)
	if resumedAt >= 0 && resumedAt < firstBatches {
		t.Errorf("another worker's run started at batch %d, the checkpoint held %d: the run did not resume from its checkpoint", resumedAt, firstBatches)
	}
	// A job that runs on one worker and then on another was released and queued again in between: the sampling (one read a second) may
	// miss the queued state of a worker that picks it up within a poll interval, the change of replica cannot be missed.
	t.Logf("the queued state was seen: %v", queuedAgain)
	if len(replicas) < 2 {
		t.Errorf("only %v ran the job: it was not released and another worker did not complete it", replicas)
	}
	if got := sql(t, `SELECT records FROM trove.vuln_source_state WHERE source = 'osv' AND ecosystem = 'npm'`); got != strconv.Itoa(want) {
		t.Errorf("the npm ecosystem holds %s records after the run, want %d", got, want)
	}
	e.awaitEvaluated(t)
}

// TestVulnLong_R_VULN_027_ClientsWithValkeyStopped is E4b.8: the client tests of the short set, once more with Valkey and the
// Sentinels stopped: the audits of every row through the three repositories, the endpoint's answers, the blocked installs
// (malicious, in triage), the NuGet restores with their audit warnings and the blocks, and the resource on the wire. The
// reports of the matrix carry the suffix of the run.
func TestVulnLong_R_VULN_027_ClientsWithValkeyStopped(t *testing.T) {
	vulnFx(t)
	vulnRunSuffix = "-valkey-stopped"
	defer func() { vulnRunSuffix = "" }()
	withValkeyStopped(t, func() {
		for _, c := range []struct {
			name string
			f    func(*testing.T)
		}{
			{"npm audit through every repository", TestVuln_R_VULN_017_NPMAuditThroughEveryRepository},
			{"npm audit body", TestVuln_R_VULN_017_NPMAuditBody},
			{"npm blocked install, malicious", TestVuln_R_VULN_015_NPMBlockedInstallMalicious},
			{"npm blocked install, in triage", TestVuln_R_VULN_015_NPMBlockedInstallInTriage},
			{"NuGet restore audit", TestVuln_R_VULN_018_NuGetRestoreAudit},
			{"NuGet blocked restore", TestVuln_R_VULN_015_NuGetBlockedRestoreInTriage},
			{"NuGet vulnerability information", TestVuln_R_VULN_018_NuGetVulnerabilityInfoOnTheWire},
		} {
			t.Run(c.name, c.f)
		}
	})
}
