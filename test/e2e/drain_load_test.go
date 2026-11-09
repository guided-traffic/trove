//go:build e2e

package e2e

// R-HA-059's acceptance test for every variant (A to E) under the load the caller starts, with the extras of E5.9 that a drain
// of a node adds to the clients' commands: the long-transfer probes (drain_transfers_test.go) and the storage jobs of R-STOR-046's
// drain criterion (drain_storage_test.go). One flow serves the variants, so the conditions are written once:
//
//	extras.prepare            before anything is picked: files seeded, nothing running
//	prepareVariantDrain       the node of the variant (drain_variants_test.go)
//	load starts
//	extras.begin              the probes that run through the warm-up
//	... the warm-up (before) ...
//	six probe jobs start, two workers each run one
//	extras.beforeDrain        forces the precondition of the extras right before the fault (a job in flight on the evicted worker)
//	d.Run                     the fault: deploy/dev/drain.sh [--also ...] <node>
//	... after ...
//	load.stop, every probe job ended, d.Verify (the variant's own condition and the shared ones), the load's conditions, extras.verify

import (
	"fmt"
	"maps"
	"strings"
	"testing"
	"time"
)

// drainExtra is a part of the drain test beside the load and the probe jobs.
type drainExtra interface {
	// prepare runs first, before the node is picked and before the load starts.
	prepare(t *testing.T)
	// begin runs once the load runs; started is the moment it began.
	begin(t *testing.T, d *variantDrain, started time.Time)
	// beforeDrain runs at the end of the warm-up, right before the fault. It may take a minute; the fault follows at once.
	beforeDrain(t *testing.T, d *variantDrain)
	// verify judges after the load has stopped and d.Verify has run. firstEviction is the first pod the watch saw with a deletion
	// timestamp, drainEnd the moment drain.sh returned.
	verify(t *testing.T, d *variantDrain, firstEviction, drainEnd time.Time)
}

// drainUnderLoad is R-HA-059's acceptance test for variant v under the load start begins: two minutes of load before the drain and
// three after it (the length of the variant that runs on every change to main, Q-HA-67; the variants B to E run the same length,
// Q-HA-240). kind ("npm", "nuget", "full") names the artifacts.
func drainUnderLoad(t *testing.T, kind string, v drainVariant, start func() drainLoad, extras ...drainExtra) {
	const before, after = 2 * time.Minute, 3 * time.Minute
	const steps = 450 // probe jobs that run through the forcing of the extras and the drain: they start after the warm-up and a worker keeps a job until 44 s after its SIGTERM
	name := kind + "-ha-drain"
	logPrefix := kind + "-drain-"
	if v != variantA {
		name = fmt.Sprintf("%s-ha-drain-%s", kind, strings.ToLower(string(v)))
		logPrefix = fmt.Sprintf("%s-drain-%s-", kind, strings.ToLower(string(v)))
	}
	troveSettled(t, 3, 3, 4)
	// Variant E starts its backup in the preparation, and the Job copies what the installation holds then, at 512 KiB/s. The installation
	// holds 200 MiB for it (a copy of about 7 minutes, longer than the 4 to 5 minutes to the eviction, and the Job that starts again in
	// the second pod ends within the 15 minutes), and the 1 GiB file of the download probes is put after the Job has started, so that the
	// copy does not take it: it would take 35 minutes. The first runs of the leg lost their runner in variant E, with the 1 GiB file in the
	// backup (Q-HA-245).
	var d *variantDrain
	if v == variantE {
		d = prepareVariantDrain(t, v, func(d *variantDrain) { d.BackupFiles, d.BackupFileMiB = 10, 20 })
		for _, x := range extras {
			x.prepare(t)
		}
	} else {
		for _, x := range extras {
			x.prepare(t)
		}
		d = prepareVariantDrain(t, v)
	}
	// the pick may have moved the PostgreSQL and the Valkey primary and the backup Job may be running: the replicas are unready for
	// a few seconds after that, and the load is the drain's, not the pick's
	troveSettled(t, 3, 3, 4)
	streams := streamPodLogs(t, logPrefix, d.OnNode)

	load := start()
	for _, x := range extras {
		x.begin(t, d, load.begun())
	}
	time.Sleep(time.Until(load.begun().Add(before)))
	// the probe jobs start at the end of the warm-up, so that they are in flight at the eviction whatever the extras take to force their
	// precondition (the first run of E5.9 started them with the load, and the forcing of the collection run took 3 minutes: they had ended)
	var ids []string
	for i := 0; i < 6; i++ {
		ids = append(ids, strings.TrimSpace(driver(t, "probe", "-steps", fmt.Sprint(steps), "-interval-ms", "1000")))
	}
	waitFor(t, "both workers to run a probe job", 60*time.Second, time.Second, func() bool {
		rows := sql(t, fmt.Sprintf(`SELECT r.name FROM trove.job j JOIN trove.replica r ON r.instance_uid = j.locked_by WHERE j.kind = 'system.probe' AND j.state = 'running' AND j.progress_total = %d GROUP BY 1`, steps))
		return len(strings.Fields(rows)) >= 2
	})
	for _, x := range extras {
		x.beforeDrain(t, d)
	}

	t.Logf("variant %s: the drain of %s starts", v, d.Node)
	d.Run(t)
	drainStart := d.started
	drainEnd := d.started.Add(d.took)
	time.Sleep(after)
	res := load.stop(t)

	finished := map[string]probeJob{}
	waitFor(t, "every probe job to end", 6*time.Minute, 5*time.Second, func() bool {
		for _, id := range ids {
			if _, ok := finished[id]; ok {
				continue
			}
			var j probeJob
			if err := jsonUnmarshal(driver(t, "job", id), &j); err != nil {
				t.Fatal(err)
			}
			if j.State == "succeeded" || j.State == "failed" || j.State == "cancelled" {
				finished[id] = j
			}
		}
		return len(finished) == len(ids)
	})
	// the variant's own condition and the shared ones: no SIGKILL, nothing Pending for more than 60 s, the drain within 10 minutes,
	// the node uncordoned, the evicted dependency replaced
	d.Verify(t)
	d.watch.mu.Lock()
	firstEviction := d.watch.firstDeleted
	d.watch.mu.Unlock()
	if firstEviction.IsZero() || firstEviction.Before(drainStart) {
		t.Errorf("no eviction seen after the drain started (first deletion %v)", firstEviction)
		firstEviction = drainStart
	}

	// the pass conditions of R-HA-059
	res.allExitZero(t)
	res.loopsSpan(t, firstEviction, drainEnd)
	res.k6Holds(t)
	warm, nWarm := res.k6P99(res.k6First(), firstEviction)
	during, nDuring := res.k6P99(firstEviction, drainEnd.Add(2*time.Minute))
	t.Logf("k6 p99: warm-up %s over %d requests, from the first eviction until 2 minutes after the drain %s over %d", warm, nWarm, during, nDuring)
	if nWarm < 1000 || nDuring < 1000 {
		t.Errorf("too few requests for the latency condition: %d in the warm-up, %d in the drain window", nWarm, nDuring)
	}
	if v == variantC {
		// the switchover stalls the requests that meet it (they wait in the retry loop of R-HA-009 for the new primary); the stall is
		// bounded by database.retry.max_elapsed, and the p99 is judged on the requests outside it (Q-HA-243)
		target, switched, _ := d.poll.times()
		during = judgeSwitchoverWindow(t, res.k6Lines(), firstEviction, drainEnd.Add(2*time.Minute), target.Add(-time.Second), switched.Add(10*time.Second))
	}
	if during > 2*warm {
		t.Errorf("R-HA-059: p99 from the first eviction until 2 minutes after the drain is %s, more than twice the warm-up's %s", during, warm)
	}
	res.installPublished(t)
	res.conditions(t, firstEviction, drainEnd)

	// every job that ran on an evicted worker finishes on another one with its attempt unchanged (R-HA-011). Variant A leaves the
	// database alone, so every probe job keeps attempt 1; in the others a job that stayed on its worker may be touched by the
	// dependency's own fault (a PostgreSQL switchover ends its connections), which R-HA-059 does not ask of it.
	moved := 0
	for _, id := range ids {
		j := finished[id]
		t.Logf("job %s: %s attempt %d/%d steps %d hops %d resumed at %v", id, j.State, j.Attempt, j.MaxAttempts, j.Result.Steps, len(j.Result.Hops), j.Result.Resumes)
		if j.State != "succeeded" || j.Result.Steps != steps {
			t.Errorf("job %s ended %s with %d steps, want succeeded with %d", id, j.State, j.Result.Steps, steps)
		}
		hopped := len(j.Result.Hops) > 1
		if j.Attempt != 1 && (hopped || v == variantA) {
			t.Errorf("job %s: attempt %d, want 1 (a release at shutdown does not count an attempt)", id, j.Attempt)
		}
		if hopped {
			moved++
			if len(j.Result.Resumes) == 0 || j.Result.Resumes[0] <= 0 || j.Result.Resumes[0] >= steps {
				t.Errorf("job %s moved but did not resume from a cursor inside the run: %v", id, j.Result.Resumes)
			}
		}
	}
	if moved == 0 {
		t.Errorf("no probe job moved to the other worker: the drained worker did not release any job")
	}
	checkEndpointRemoval(t, name, streams)
	for _, x := range extras {
		x.verify(t, d, firstEviction, drainEnd)
	}
	d.watch.mu.Lock()
	pendFor := maps.Clone(d.watch.pendFor)
	d.watch.mu.Unlock()
	rec := map[string]any{
		"variant": string(v), "node": d.Node, "drain_seconds": d.took.Seconds(), "first_eviction": firstEviction, "p99_warmup_ms": warm.Milliseconds(),
		"p99_drain_ms": during.Milliseconds(), "moved_jobs": moved, "pending_pods": pendFor,
	}
	maps.Copy(rec, res.note())
	noteResult(t, name, rec)
	d.Settle(t)
}

// fullLoadStarter is the whole load of R-HA-059's table that the two clients make, as the drain of variant v starts it. The load's name
// carries the variant: the packages the publish loops push and the files of the load are named after it, and a second variant in the
// same run would otherwise push the versions of the first again.
func fullLoadStarter(t *testing.T, h *npmHAFixture, nh *nugetHAFixture, v drainVariant) func() drainLoad {
	return func() drainLoad { return startFullLoad(t, h, nh, "drain-"+strings.ToLower(string(v))) }
}

// drainExtras are the parts of E5.9 beside the clients' load: the long-transfer probes and R-STOR-046's storage jobs.
func drainExtras() []drainExtra { return []drainExtra{newStorageJobs(), newLongTransfers()} }

// TestDrainLoad is E5.9 for the variants B, C and E of R-HA-059 under the whole load (variant A: TestNuGetHA, variant D:
// TestDrainLoadPooler in the pooler profile): eight npm ci loops, four dotnet restore loops, an npm and a NuGet publish every 10 s,
// k6 at 150 requests/s, the long-transfer probes, and R-STOR-046's gc-full running on the worker that is evicted.
func TestDrainLoad(t *testing.T) {
	h := npmHAFx(t)
	nh := nugetHAFx(t)

	for _, v := range []struct {
		variant drainVariant
		name    string
	}{
		{variantB, "R-HA-059 R-HA-052 R-HA-057 R-STOR-046 E5.9 drain variant B (the Valkey primary and a Sentinel on the node) under the full load: every npm and dotnet command exits 0, k6 only 2xx and 304, every acknowledged publish installs, a long transfer is cut at most once per evicted packages pod and completes by resume, no SIGKILL, jobs resume with their attempt, gc-full finishes on the other worker"},
		{variantC, "R-HA-059 R-HA-052 R-STOR-046 E5.9 drain variant C (the PostgreSQL primary on the node) under the full load: every npm and dotnet command exits 0, k6 only 2xx and 304, every acknowledged publish installs, a long transfer is cut at most once per evicted packages pod and completes by resume, no SIGKILL, jobs resume with their attempt, gc-full finishes on the other worker"},
		{variantE, "R-HA-059 R-HA-060 R-STOR-046 E5.9 drain variant E (a running backup pod on the node) under the full load: every npm and dotnet command exits 0, k6 only 2xx and 304, every acknowledged publish installs, a long transfer is cut at most once per evicted packages pod and completes by resume, no SIGKILL, jobs resume with their attempt, gc-full finishes on the other worker"},
	} {
		t.Run(v.name, func(t *testing.T) {
			drainUnderLoad(t, "full", v.variant, fullLoadStarter(t, h, nh, v.variant), drainExtras()...)
		})
	}
}

// TestDrainLoadPooler is E5.9 for variant D of R-HA-059 (an instance of the transaction-mode pooler on the node, R-HA-058) under the
// whole load. It needs the cluster in the pooler profile (TROVE_POOLER=1, set drain-load-pooler).
func TestDrainLoadPooler(t *testing.T) {
	// the fixtures put the cluster into the scatter profile with the poolers (up.sh reads TROVE_POOLER, which the set exports)
	h := npmHAFx(t)
	nh := nugetHAFx(t)
	requirePoolerProfile(t)

	t.Run("R-HA-059 R-HA-058 R-STOR-046 E5.9 drain variant D (an instance of the request pooler on the node) under the full load: every npm and dotnet command exits 0, k6 only 2xx and 304, every acknowledged publish installs, a long transfer is cut at most once per evicted packages pod and completes by resume, no SIGKILL, jobs resume with their attempt, gc-full finishes on the other worker", func(t *testing.T) {
		drainUnderLoad(t, "full", variantD, fullLoadStarter(t, h, nh, variantD), drainExtras()...)
	})
}

// judgeSwitchoverWindow is the p99 of the k6 requests started in [from, to) but outside [exFrom, exTo), the window of the PostgreSQL
// switchover of variant C. The requests that started inside it must have ended within database.retry.max_elapsed (30 s) and 5 s more,
// the bound of the retry loop that carries them over the switchover (R-HA-009); it logs the longest of them and the number.
func judgeSwitchoverWindow(t *testing.T, lines []npmK6Line, from, to, exFrom, exTo time.Time) time.Duration {
	t.Helper()
	var outside []time.Duration
	var longest time.Duration
	inside := 0
	for _, l := range lines {
		if l.At.Before(from) || !l.At.Before(to) {
			continue
		}
		d := time.Duration(l.MS) * time.Millisecond
		if !exFrom.IsZero() && !l.At.Before(exFrom) && l.At.Before(exTo) {
			inside++
			longest = max(longest, d)
			continue
		}
		outside = append(outside, d)
	}
	t.Logf("variant C: %d k6 requests started during the switchover window %s to %s, the longest took %s; p99 of the %d others judged", inside,
		exFrom.Format("15:04:05"), exTo.Format("15:04:05"), longest, len(outside))
	if longest > 35*time.Second {
		t.Errorf("R-HA-059: a request that met the PostgreSQL switchover took %s, more than database.retry.max_elapsed (30 s) and 5 s", longest)
	}
	if len(outside) == 0 {
		return 0
	}
	return percentile(outside, 0.99)
}
