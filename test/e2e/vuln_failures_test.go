//go:build e2e

package e2e

// The vulnerability scanning under the failure suite (E5.13, R-HA-052; owner rule of 2026-11-01: long tests run nightly, on dispatch and
// locally): a PostgreSQL switchover during an evaluation job (R-VULN-011), a malicious or blocked version that stays blocked on every
// replica while a node is drained and while the Valkey and PostgreSQL primaries change (R-VULN-015, R-VULN-027, R-HA-059), and a
// worker killed in the middle of a full synchronization (R-VULN-004, H-523). The blocks through drain variants B, C and E are in
// vuln_drain_variants_test.go, the one through variant D in vuln_pooler_test.go (the set vuln-pooler), the restore drill of E5.13 in
// vuln_restore_test.go.

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// vulnNPMBulk holds the packages the evaluation test seeds: it is created by the test and emptied by it.
const vulnNPMBulk = "npm-vuln-bulk"

// trySQL runs a statement on the current primary once; ok is false when it failed (a switchover moves the primary).
func trySQL(query string) (out, primary string, ok bool) {
	p, err := kubectl("-n", cfg.namespace, "get", "cluster", "trove-db", "-o", "jsonpath={.status.currentPrimary}")
	primary = strings.TrimSpace(p)
	if err != nil || primary == "" {
		return "", primary, false
	}
	out, err = kubectl("-n", cfg.namespace, "exec", primary, "-c", "postgres", "--", "psql", "-U", "postgres", "-d", "trove", "-At", "-F", "|", "-c", query)
	if err != nil {
		return "", primary, false
	}
	return strings.TrimSpace(out), primary, true
}

// restoreSourceAfter puts the test advisory source and the stored data back to the fixture and this run's records once the test ends
// (the shrink guard lifted once): the later tests of the process read the records of their packages.
func restoreSourceAfter(t *testing.T, e *vulnEnv, osv *stubControl) {
	t.Helper()
	t.Cleanup(func() {
		osv.send(t, http.MethodPost, "/-/stub/reset?all=true", nil)
		for _, r := range e.records {
			osv.addRecord(t, r.ecosystem, r.advisory)
		}
		e.syncFull(t, true)
	})
}

// ── the switchover during an evaluation ──────────────────────────────────────

// bulkVersions are the stored versions of every seeded package; the filler advisory of the test source (introduced 0, fixed 1.0.1)
// affects the first two.
var (
	bulkVersions = []string{"0.9.0", "1.0.0", "1.0.1", "1.1.0"}
	bulkAffected = []string{"0.9.0", "1.0.0"}
	// lpad cuts a longer text to its length: the number has at least four digits and as many more as it needs, as the source's %04d
	bulkNumber    = `lpad(i::text, greatest(4, length(i::text)), '0')`
	bulkNameQuery = `'trove-test-filler-' || ` + bulkNumber
	bulkAdvQuery  = `'GHSA-fill-' || ` + bulkNumber + ` || '-0000'`
)

// bulkSQL is the list of an SQL array literal.
func bulkSQL(v []string) string { return "ARRAY['" + strings.Join(v, "','") + "']" }

// TestVulnLong_R_VULN_011_SwitchoverDuringEvaluation is the criterion of R-VULN-011 and R-HA-052 for the evaluation: a PostgreSQL
// switchover while a `vuln-evaluate` job works off the queued advisories leaves no finding twice and none lost. The repository
// npm-vuln-bulk stores `names` packages with four versions each (seeded in the table, as a mirror or a long-lived repository
// would hold them) under the names of the filler records of the test source; a full synchronization imports `names` new filler
// records, which queues them all, and the job evaluates them in batches of 1,000. Once it has committed batches, failover.sh
// switches the primary over. The expected set is computed from the seeded packages and the source's filler rule.
func TestVulnLong_R_VULN_011_SwitchoverDuringEvaluation(t *testing.T) {
	e := vulnFx(t)
	osv := osvStub(t)
	const names = 20000
	e.ensureRepo(t, vulnNPMBulk, map[string]any{"key": vulnNPMBulk, "class": "local", "format": "npm", "requireAuthentication": true},
		map[string]any{"vulnerabilities": map[string]any{"matchPublicAdvisories": "always"}})
	repo := sql(t, `SELECT id FROM trove.repositories WHERE key = '`+vulnNPMBulk+`'`)
	wipe := func() {
		sql(t, `DELETE FROM trove.vuln_finding WHERE repo_id = `+repo)
		sql(t, `DELETE FROM trove.packages WHERE repo_id = `+repo)
	}
	restoreSourceAfter(t, e, osv)
	t.Cleanup(wipe) // runs before the restore above: the findings of the filler records go with the packages
	wipe()

	seeded := time.Now()
	sql(t, fmt.Sprintf(`WITH p AS (
		INSERT INTO trove.packages (repo_id, name, display_name)
		SELECT %s, %[2]s, %[2]s FROM generate_series(0, %[3]d) i RETURNING id)
		INSERT INTO trove.package_versions (package_id, version, display_version) SELECT p.id, v, v FROM p, unnest(%[4]s) v`,
		repo, bulkNameQuery, names-1, bulkSQL(bulkVersions)))
	t.Logf("seeded %d packages with %d versions in %s", names, len(bulkVersions), time.Since(seeded).Round(time.Millisecond))

	// the source holds `names` new filler records: the packages above are their packages
	osv.send(t, http.MethodPut, "/-/stub/filler/npm", map[string]any{"count": names})
	syncID, running := e.task(t, "vuln-sync-osv-full", map[string]any{"acceptShrink": true})
	if running {
		t.Fatal("a synchronization was running when the test started")
	}
	syncStart := time.Now()
	if st := e.waitJob(t, syncID, 15*time.Minute); st != "succeeded" {
		t.Fatalf("the synchronization ended %s: %s", st, sql(t, "SELECT coalesce(error::text, '') FROM trove.job WHERE id = '"+syncID+"'"))
	}
	t.Logf("the synchronization took %s", time.Since(syncStart).Round(time.Second))
	pending := sql(t, `SELECT count(*) FROM trove.vuln_pending_evaluation`)
	t.Logf("%s advisories wait for their evaluation", pending)

	var evalID string
	waitFor(t, "the evaluation to commit its first batches", 10*time.Minute, 500*time.Millisecond, func() bool {
		out, _, ok := trySQL(`SELECT id FROM trove.job WHERE kind = 'vuln-evaluate' AND state = 'running' AND progress_done > 0
			AND created_at >= (SELECT created_at FROM trove.job WHERE id = '` + syncID + `') ORDER BY created_at DESC LIMIT 1`)
		evalID = out
		return ok && out != ""
	})
	sample := func() (s [5]string, primary string, ok bool) {
		out, primary, ok := trySQL(`SELECT j.state, j.attempt, j.progress_done, j.progress_total, coalesce(r.name, '') FROM trove.job j
			LEFT JOIN trove.replica r ON r.instance_uid = j.locked_by WHERE j.id = '` + evalID + `'`)
		f := strings.Split(out, "|")
		if !ok || len(f) != 5 {
			return s, primary, false
		}
		copy(s[:], f)
		return s, primary, true
	}
	first, primary0, ok := sample()
	if !ok || first[0] != "running" {
		t.Fatalf("the evaluation is not running before the switchover: %v: the dataset is too small for the cluster's speed", first)
	}
	t.Logf("evaluation job %s runs on %s (attempt %s), %s of %s advisories done, the primary is %s", evalID, first[4], first[1], first[2], first[3], primary0)

	failDone := make(chan error, 1)
	var failOut []byte
	failStart := time.Now()
	go func() {
		cmd := exec.Command(filepath.Join(cfg.repo, "deploy/dev/failover.sh"))
		cmd.Dir = cfg.repo
		var err error
		failOut, err = cmd.CombinedOutput()
		failDone <- err
	}()
	firstAttempt, _ := strconv.Atoi(first[1])
	maxAttempt, doneAtSwitch, inFlightAfterSwitch, switched := firstAttempt, -1, false, false
	replicas := map[string]bool{first[4]: true}
	var last [5]string
	failed, deadline := false, time.Now().Add(20*time.Minute)
	for time.Now().Before(deadline) {
		if !failed {
			select {
			case err := <-failDone:
				failed = true
				t.Logf("failover.sh returned after %s (error %v):\n%s", time.Since(failStart).Round(time.Second), err, strings.TrimSpace(string(failOut)))
				if err != nil {
					t.Fatalf("failover.sh: %v", err)
				}
			default:
			}
		}
		s, primary, ok := sample()
		if !ok {
			time.Sleep(time.Second)
			continue
		}
		last = s
		if s[4] != "" {
			replicas[s[4]] = true
		}
		if n, _ := strconv.Atoi(s[1]); n > maxAttempt {
			maxAttempt = n
		}
		if primary != primary0 && !switched {
			switched = true
			doneAtSwitch, _ = strconv.Atoi(s[2])
			t.Logf("the primary is %s after %s; the job is %s, attempt %s, %s of %s advisories", primary, time.Since(failStart).Round(time.Second), s[0], s[1], s[2], s[3])
		}
		if switched && (s[0] == "running" || s[0] == "queued") {
			inFlightAfterSwitch = true
		}
		if s[0] == "succeeded" || s[0] == "failed" || s[0] == "cancelled" {
			break
		}
		time.Sleep(time.Second)
	}
	if !failed {
		if err := <-failDone; err != nil {
			t.Fatalf("failover.sh: %v\n%s", err, failOut)
		}
	}
	jobEnd := sql(t, `SELECT state || '|' || attempt || '|' || max_attempts || '|' || coalesce(error::text, '') FROM trove.job WHERE id = '`+evalID+`'`)
	t.Logf("the evaluation ended %q on %v, highest attempt seen %d, %d advisories done when the switchover took effect", jobEnd, replicas, maxAttempt, doneAtSwitch)
	if !inFlightAfterSwitch && maxAttempt == firstAttempt {
		t.Errorf("the evaluation ended before the switchover took effect (last sample %v): the dataset is too small for the cluster's speed", last)
	}

	// the job may need another attempt after the connection of its batch broke; if it ended failed, the remaining advisories
	// still wait in the table and the check below shows what is missing
	if !strings.HasPrefix(jobEnd, "succeeded|") {
		t.Errorf("the evaluation job ended %q, want succeeded (on a later attempt if the switchover cut a batch)", jobEnd)
	}
	e.awaitEvaluated(t)

	t.Run("R-VULN-011 no finding twice, none lost", func(t *testing.T) {
		expected := fmt.Sprintf(`SELECT %[1]s AS name, v AS version, %[4]s AS adv FROM generate_series(0, %[2]d) i, unnest(%[3]s) v`,
			bulkNameQuery, names-1, bulkSQL(bulkAffected), bulkAdvQuery)
		got := `SELECT package_name AS name, version, advisory_id AS adv FROM trove.vuln_finding WHERE repo_id = ` + repo + ` AND status <> 'resolved'`
		counts := sql(t, fmt.Sprintf(`SELECT (SELECT count(*) FROM (%[1]s EXCEPT %[2]s) a) || '|' || (SELECT count(*) FROM (%[2]s EXCEPT %[1]s) b) || '|' || (SELECT count(*) FROM (%[2]s) c)`, expected, got))
		want := fmt.Sprintf("0|0|%d", names*len(bulkAffected))
		if counts != want {
			t.Errorf("findings missing | findings unexpected | findings: %s, want %s", counts, want)
		}
		if all := sql(t, `SELECT count(*) FROM trove.vuln_finding WHERE repo_id = `+repo); all != strconv.Itoa(names*len(bulkAffected)) {
			t.Errorf("the repository has %s findings in all states, want %d", all, names*len(bulkAffected))
		}
		if dup := sql(t, `SELECT count(*) FROM (SELECT 1 FROM trove.vuln_finding WHERE repo_id = `+repo+` GROUP BY repo_id, ecosystem, package_name, version, advisory_id HAVING count(*) > 1) d`); dup != "0" {
			t.Errorf("%s finding identities exist more than once", dup)
		}
		// a finding detected twice would carry two `detected` events, or none when its history was lost with a batch
		if odd := sql(t, `SELECT count(*) FROM trove.vuln_finding f WHERE f.repo_id = `+repo+`
			AND (SELECT count(*) FROM trove.vuln_finding_event v WHERE v.finding_id = f.id AND v.change = 'detected') <> 1`); odd != "0" {
			t.Errorf("%s findings have other than exactly one `detected` event", odd)
		}
		if left := sql(t, `SELECT count(*) FROM trove.vuln_pending_evaluation`); left != "0" {
			t.Errorf("%s advisories still wait for their evaluation", left)
		}
		if bad := sql(t, `SELECT count(*) FROM trove.vuln_finding WHERE repo_id = `+repo+` AND (version_id IS NULL OR package_id IS NULL)`); bad != "0" {
			t.Errorf("%s findings reference no stored version", bad)
		}
	})
}

// ── a block that holds while the cluster changes ────────────────────────────

// blockProbe is one request path that must always be refused, with the client that asks.
type blockProbe struct {
	what, path string
	a          *api
}

// blockTally is what the probes saw: the answers by replica, bytes served (a breach of R-VULN-015) and every other answer.
type blockTally struct {
	mu        sync.Mutex
	total     int
	byReplica map[string]int
	bytes     []string
	other     map[string]int
}

func (b *blockTally) add(replica, kind, what string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.total++
	b.byReplica[replica]++
	switch kind {
	case "":
	case "bytes":
		b.bytes = append(b.bytes, what)
	default:
		b.other[what]++
	}
}

// probeOnce asks once and returns the replica and what is wrong with the answer ("" for the refusal of R-VULN-016).
func probeOnce(p blockProbe, i int) (replica, kind, what string) {
	var method string
	var h []string
	switch i % 3 {
	case 0:
		method = http.MethodGet
	case 1:
		method = http.MethodHead
	default:
		method, h = http.MethodGet, []string{"Range", "bytes=0-99"}
	}
	label := p.what + " " + method
	if len(h) > 0 {
		label += " Range"
	}
	r, err := p.a.send(method, p.path, nil, h...)
	if err != nil {
		return "-", "other", label + ": " + strings.TrimSpace(err.Error())
	}
	replica = r.Replica()
	if replica == "" {
		replica = "?"
	}
	switch {
	case r.Status == http.StatusForbidden && (method == http.MethodHead || strings.Contains(string(r.Body), "vulnerability_blocked")):
		return replica, "", ""
	case r.Status == http.StatusOK || r.Status == http.StatusPartialContent || r.Status == http.StatusNotModified:
		return replica, "bytes", fmt.Sprintf("%s: %d from %s", label, r.Status, replica)
	default:
		return replica, "other", fmt.Sprintf("%s: %s", label, r)
	}
}

// probeDuring asks every probe in a loop of its own while the fault runs, from `before` earlier until `after` later.
func probeDuring(t *testing.T, probes []blockProbe, before, after time.Duration, fault func()) *blockTally {
	t.Helper()
	tally := &blockTally{byReplica: map[string]int{}, other: map[string]int{}}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for _, p := range probes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; ctx.Err() == nil; i++ {
				replica, kind, what := probeOnce(p, i)
				tally.add(replica, kind, what)
				time.Sleep(100 * time.Millisecond)
			}
		}()
	}
	time.Sleep(before)
	fault()
	time.Sleep(after)
	cancel()
	wg.Wait()
	return tally
}

func (b *blockTally) report(t *testing.T, minReplicas int) {
	t.Helper()
	replicas := make([]string, 0, len(b.byReplica))
	for r := range b.byReplica {
		replicas = append(replicas, fmt.Sprintf("%s:%d", r, b.byReplica[r]))
	}
	sort.Strings(replicas)
	t.Logf("%d requests, by replica %v", b.total, replicas)
	if len(b.bytes) > 0 {
		t.Errorf("R-VULN-015: %d answers served the bytes of a blocked version, first: %v", len(b.bytes), b.bytes[:min(5, len(b.bytes))])
	}
	if len(b.other) > 0 {
		t.Errorf("R-HA-059 R-HA-052: %d answers were not the refusal: %v", len(b.other), b.other)
	}
	if len(b.byReplica) < minReplicas {
		t.Errorf("the requests reached %d replicas (%v), want at least %d", len(b.byReplica), replicas, minReplicas)
	}
}

// blockedPathsLabeled builds the probes: the tarball and the .nupkg of a version blocked in triage (cached before it was blocked) and of
// a malicious one (never stored), through the repositories that hold the remote boundary of the vuln profile. The label names the
// packages of the triage versions: a test that builds its probes after another one of the process needs a label of its own, because a
// second call with the same label would publish the same name and version to the upstream again.
func (e *vulnEnv) blockedPathsLabeled(t *testing.T, label string) []blockProbe {
	t.Helper()
	e.addNPMTriage(t, label)
	e.addNuGetTriage(t, label)
	e.syncIncremental(t)
	npmA := e.adm.withToken(e.npm.token)
	nugetA := e.adm.withToken(e.nuget.token)
	nupkg := func(repo, id string) string {
		l := strings.ToLower(id)
		return nugetAddr(repo) + "/v3/flatcontainer/" + l + "/1.0.0/" + l + ".1.0.0.nupkg"
	}
	npmName, nugetID := e.npmTri[label], e.nugetTri[label]
	npmTri, nugetTri := tarballPath(npmRemoteKey, npmName), nupkg(vulnNuGetRemote, nugetID)
	npmA.get(npmTri).want(t, "the first download of the npm package fills the cache", 200)
	nugetA.get(nugetTri).want(t, "the first download of the NuGet package fills the cache", 200)
	e.block(t, e.adm, e.finding(t, npmRemoteKey, npmName, "1.0.0").ID)
	e.block(t, e.adm, e.finding(t, vulnNuGetRemote, strings.ToLower(nugetID), "1.0.0").ID)
	t.Cleanup(func() {
		e.lift(t, e.adm, e.finding(t, npmRemoteKey, npmName, "1.0.0").ID)
		e.lift(t, e.adm, e.finding(t, vulnNuGetRemote, strings.ToLower(nugetID), "1.0.0").ID)
	})
	probes := []blockProbe{
		{"npm tarball, blocked in triage", npmTri, npmA},
		{"npm tarball, malicious", tarballPath(npmRemoteKey, vulnNPMMal), npmA},
		{"NuGet package, blocked in triage", nugetTri, nugetA},
		{"NuGet package, malicious", nupkg(vulnNuGetRemote, vulnNuGetBad), nugetA},
	}
	// every replica has learned of the blocks: thirty requests of every probe reach each of the three replicas
	waitFor(t, "every replica to refuse the four versions", 2*time.Minute, time.Second, func() bool {
		for _, p := range probes {
			for i := range 30 {
				if _, kind, _ := probeOnce(p, i); kind != "" {
					return false
				}
			}
		}
		return true
	})
	return probes
}

// TestVulnLong_R_VULN_015_BlockHoldsDuringFailures is E5.13's "a malicious version stays blocked on every replica throughout
// the drain": four versions (npm and NuGet, blocked in triage and malicious) are requested through the edge by four loops,
// 10 requests a second each as GET, HEAD and Range GET, while a node of variant A of R-HA-059 is drained, while the Valkey
// primary is failed over (R-HA-057) and while the PostgreSQL primary is switched over. Every answer must be the refusal of
// R-VULN-016 from a replica; a 200, 206 or 304 is a breach of R-VULN-015, any other answer or a connection error a failed request.
// The replacement pod of the drain must refuse from its first request. Variants B, C and E are
// TestVulnLong_R_VULN_015_BlockHoldsThroughDrainVariants, variant D is TestVulnPooler_R_VULN_015_BlockHoldsThroughDrainVariantD.
func TestVulnLong_R_VULN_015_BlockHoldsDuringFailures(t *testing.T) {
	e := vulnFx(t)
	probes := e.blockedPathsLabeled(t, "fault")

	t.Run("R-HA-059 R-VULN-015 R-VULN-027 E5.13 drain variant A: every replica refuses, the replacement from its first request", func(t *testing.T) {
		troveSettled(t, 3, 3, 4)
		node := drainNodeVariantA(t)
		troveSettled(t, 3, 3, 4)
		var onNode []pod
		for _, p := range livePods(t, selTrove) {
			if p.Node == node {
				onNode = append(onNode, p)
			}
		}
		t.Logf("draining %s, which hosts %v", node, podNames(onNode))
		start := time.Now()
		tally := probeDuring(t, probes, 20*time.Second, 40*time.Second, func() { script(t, "drain.sh", node) })
		t.Logf("the probes ran %s, the drain included", time.Since(start).Round(time.Second))
		// the three packages replicas and the replacement of the one on the node
		tally.report(t, 4)
		troveSettled(t, 3, 3, 4)
	})
	t.Run("R-HA-052 R-HA-057 R-VULN-015 R-VULN-027 E5.13 Valkey planned failover (R-HA-052)", func(t *testing.T) {
		troveSettled(t, 3, 3, 4)
		tally := probeDuring(t, probes, 10*time.Second, 30*time.Second, func() { script(t, "valkey-failover.sh") })
		tally.report(t, 3)
	})
	t.Run("R-HA-052 R-VULN-015 R-VULN-027 E5.13 PostgreSQL switchover (R-HA-052)", func(t *testing.T) {
		troveSettled(t, 3, 3, 4)
		tally := probeDuring(t, probes, 10*time.Second, 30*time.Second, func() { script(t, "failover.sh") })
		tally.report(t, 3)
	})
}

// ── a worker killed in the middle of a full synchronization ─────────────────

// TestVulnLong_R_VULN_004_FullSyncWorkerKilledMidRun is the other half of R-VULN-004's resumption rule. A drain releases the job
// and another worker resumes it (TestVulnLong_R_VULN_004_FullSyncDrainedMidRun); a worker that dies (SIGKILL of its process, as a
// crash or an OOM kill; a deleted pod gets SIGTERM and drains) cannot release anything: the reaper ends the job `failed` with `attempts_exhausted` once its lease has run out
// (MaxAttempts 1, H-523), the previous data stays in force, and the next run imports the whole source and the evaluation catches up.
func TestVulnLong_R_VULN_004_FullSyncWorkerKilledMidRun(t *testing.T) {
	e := vulnFx(t)
	osv := osvStub(t)
	const filler = 60000
	restoreSourceAfter(t, e, osv)
	osv.send(t, http.MethodPut, "/-/stub/filler/npm", map[string]any{"count": filler})
	before := sql(t, `SELECT state || '|' || records FROM trove.vuln_source_state WHERE source = 'osv' AND ecosystem = 'npm'`)

	id, running := e.task(t, "vuln-sync-osv-full", nil)
	if running {
		t.Fatal("a synchronization was running when the test started")
	}
	var first vulnSample
	waitFor(t, "the full synchronization to commit its first batches", 10*time.Minute, time.Second, func() bool {
		s, ok := sampleJob(id)
		first = s
		return ok && s.state == "running" && s.batches != "" && s.batches != "0"
	})
	t.Logf("job %s runs on %s, attempt %s, %s batches committed; the npm state before the run is %q", id, first.replica, first.attempt, first.batches, before)
	if cur, ok := sampleJob(id); !ok || cur.state != "running" || cur.replica != first.replica {
		t.Fatalf("the job is not running on %s any more before the kill: %+v", first.replica, cur)
	}
	// A deleted pod is not a killed worker: even `--grace-period=0 --force` has the kubelet send SIGTERM, the worker drains and
	// releases the job (attempt unchanged), and another worker completes it (run 42571414393 and a local run showed exactly that:
	// succeeded, attempt 1). The container runtime's SIGKILL is the crash: nothing is released, the lease runs out.
	killed := time.Now()
	sigkill(t, pod{Name: first.replica, Node: strings.TrimSpace(kn(t, "get", "pod", first.replica, "-o", "jsonpath={.spec.nodeName}"))})

	st := e.waitJob(t, id, 6*time.Minute)
	t.Logf("the job ended %s %s after the kill", st, time.Since(killed).Round(time.Second))
	errText := sql(t, `SELECT coalesce(error::text, '') || '|' || attempt || '|' || max_attempts FROM trove.job WHERE id = '`+id+`'`)
	if st != "failed" || !strings.Contains(errText, "attempts_exhausted") {
		t.Errorf("the job of the killed worker ended %s with %q, want failed with attempts_exhausted (MaxAttempts 1, H-523)", st, errText)
	}
	if took := time.Since(killed); took > 4*time.Minute {
		t.Errorf("the reaper ended the job %s after the kill, want within the lease (60 s) plus its poll", took.Round(time.Second))
	}
	t.Logf("the npm state after the killed run is %q (job: %s)", sql(t, `SELECT state || '|' || records FROM trove.vuln_source_state WHERE source = 'osv' AND ecosystem = 'npm'`), errText)

	// the next run imports the whole source: nothing the killed run left behind is lost or doubled
	e.syncOnce(t, "vuln-sync-osv-full", nil)
	got := sql(t, `SELECT records FROM trove.vuln_source_state WHERE source = 'osv' AND ecosystem = 'npm'`)
	if n, _ := strconv.Atoi(got); n < filler {
		t.Errorf("the npm ecosystem holds %s records after the next run, want at least the %d filler records", got, filler)
	}
	if rows := sql(t, `SELECT count(*) FROM trove.vuln_advisory WHERE ecosystem = 'npm'`); rows != got {
		t.Errorf("the advisory table holds %s npm rows, the state says %s", rows, got)
	}
	if touched := sql(t, `SELECT count(*) FROM trove.vuln_sync_touched WHERE ecosystem = 'npm'`); touched != "0" {
		t.Errorf("%s touched names remain after the completed run", touched)
	}
	e.awaitEvaluated(t)
	if st := sql(t, `SELECT state FROM trove.vuln_source_state WHERE source = 'osv' AND ecosystem = 'npm'`); st != "complete" {
		t.Errorf("the npm ecosystem is %q after the next run, want complete", st)
	}
}
