//go:build integration

package vulnperf

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/osvstub"
)

func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }

// installation is the seeded stack of the run.
type installation struct {
	t    *testing.T
	st   *testenv.Stack
	stub *osvstub.Stub

	admin, grantee       testenv.Person
	tokAdmin, tokGrantee string
	// active are the states of a job that has not ended (activeStates).
	active []string
}

const (
	repoNPMLocal  = "npm-local"
	repoNPMRemote = "npm-remote"
	repoNuGet     = "nuget-local"
)

// newInstallation starts the advisory source with the dumps of the size of the real ones, creates the stack and seeds the
// 100,000 stored versions in SQL: a version is a package row and a version row, which is all the scanning reads (R-VULN-001).
// The packages are named like the stub's filler records (trove-test-filler-NNNN; trove.test.fillerNNNN, displayed as
// Trove.Test.FillerNNNN), so that the version 1.0.0 of every package is affected by one record and the others by none: 10 % of
// the stored versions get a finding, 10,000.
func newInstallation(t *testing.T) *installation {
	t.Helper()
	stub := osvstub.New(osvstub.Config{Filler: npmFiller})
	if err := stub.SetFiller("NuGet", nugetFiller); err != nil {
		t.Fatal(err)
	}
	ss, err := osvstub.NewSelfSigned("localhost", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	run, err := stub.Start("127.0.0.1:0", ss.ServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = run.Close() })
	ca := filepath.Join(t.TempDir(), "osvstub-ca.pem")
	if err := os.WriteFile(ca, ss.CAPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	st := testenv.NewStack(t, testenv.StackOptions{Name: "vulnperf", Env: []string{
		"TROVE_PROXY_OUTBOUND_TLS_CA_FILES=[" + strconv.Quote(ca) + "]",
		`TROVE_PROXY_OUTBOUND_ADDRESS_POLICY_ALLOW_CIDRS=["127.0.0.1/32"]`,
		"TROVE_VULNERABILITIES_SOURCES_OSV_BASE_URL=" + run.URL + "/",
		"TROVE_VULNERABILITIES_SOURCES_KEV_MODE=off",
		"TROVE_VULNERABILITIES_SOURCES_OSV_TIMEOUT=30m",
		// A worker enqueues the latest due slot of its tasks when it starts, and a running vuln-sync-osv makes the
		// run of V-1 answer 409 (one OSV sync at a time, R-VULN-004): the test decides when the sync runs.
		`TROVE_MAINTENANCE_TASKS={"vuln-sync-osv":{"enabled":false,"schedule":"*/15 * * * *"},"vuln-sync-osv-full":{"enabled":false,"schedule":"17 3 * * 0"},"vuln-sync-kev":{"enabled":false,"schedule":"41 */6 * * *"}}`,
	}})
	in := &installation{t: t, st: st, stub: stub, active: activeStates(t, st)}
	in.admin, in.grantee = st.User("rita"), st.User("grace")
	st.Bind("admin", in.admin)
	in.tokAdmin = st.Token(in.admin, scope.Scope{Inherit: true, Admin: true})
	in.tokGrantee = st.Token(in.grantee, scope.Default())

	st.Repository(repoNPMLocal, "local", "npm", `{"local":{"packageOwnership":"off"}}`)
	st.Repository(repoNPMRemote, "remote", "npm", remoteConfig())
	st.Repository(repoNuGet, "local", "nuget", `{}`)
	st.Policy("perf-readers", []string{"read"}, []string{repoNPMLocal, repoNPMRemote, repoNuGet}, in.grantee)
	// The grantee holds package-pattern grants (R-AUTHZ-005): the findings list narrows to them in SQL (V-7).
	pol := st.Text(`SELECT id::text FROM authz_policy WHERE name = 'perf-readers'`)
	st.Exec(`INSERT INTO authz_policy_pattern (policy_id, dimension, mode, pattern) VALUES ($1::uuid, 'package', 'include', 'trove-test-filler-0*'), ($1::uuid, 'package', 'include', 'Trove.Test.Filler00*')`, pol)

	start := time.Now()
	in.seed()
	t.Logf("seeded %d stored versions in %s", npmPackages*versionsPerPkg+nugetPackages*versionsPerPkg, time.Since(start).Round(time.Millisecond))
	return in
}

// remoteConfig is a remote repository document that points at the public registry; the run never requests it (nothing is
// cached or fetched), the repository exists so that the stored versions belong to a remote repository, which the evaluation
// matches by the host of its upstream (R-VULN-010).
func remoteConfig() string {
	remote := map[string]any{
		"url": "https://registry.npmjs.org/", "extraHosts": []any{}, "authMode": "none", "username": "", "password": nil,
		"clientCertificate": nil, "headers": []any{}, "forwardAuthOnRedirect": false, "outboundProxy": map[string]any{"mode": "direct"},
		"connectTimeoutMs": 5000, "readTimeoutMs": 15000, "sourceNetwork": nil, "cacheFillAction": "read",
		"cache": map[string]any{
			"storeArtifacts": true, "metadataTtlSeconds": 3600, "metadataRefreshTimeoutSeconds": 10, "notFoundTtlSeconds": 60,
			"maxStaleSeconds": 0, "unusedRetentionHours": 0,
		},
		"integrity":    map[string]any{"checksumPolicy": "verify-if-available", "blockUnexpectedContentTypes": true, "allowedContentTypes": []any{}},
		"availability": map[string]any{"mode": "online", "failureThreshold": 3, "circuitOpenSeconds": 30},
		"limits":       map[string]any{"maxArtifactBytes": nil, "maxMetadataBytes": nil, "maxConcurrentFetches": 16},
	}
	b, _ := json.Marshal(map[string]any{"remote": remote})
	return string(b)
}

// seed inserts the packages and versions: npm-local gets the filler names 0 to 4,499, npm-remote 4,500 to 8,999, nuget-local
// the NuGet filler names 0 to 999. A package row is what a push writes: the name is the format's canonical name, which for
// NuGet is the lower-cased ID (R-NUGET-003, as the push's EnsureLocked(lowerID, declaredID) stores it), and the display name
// is the name as declared. The NuGet adapter lower-cases an advisory's name the same way, so the 1,000 NuGet packages match.
func (in *installation) seed() {
	st := in.st
	pkgs := func(repo, prefix string, from, to int) {
		st.Exec(`INSERT INTO packages (repo_id, name, display_name)
			SELECT r.id, CASE WHEN r.format = 'nuget' THEN lower($2::text || lpad(g::text, 4, '0')) ELSE $2::text || lpad(g::text, 4, '0') END,
			       $2::text || lpad(g::text, 4, '0')
			  FROM repositories r, generate_series($3::int, $4::int) g WHERE r.key = $1`, repo, prefix, from, to-1)
	}
	pkgs(repoNPMLocal, "trove-test-filler-", 0, npmPackages/2)
	pkgs(repoNPMRemote, "trove-test-filler-", npmPackages/2, npmPackages)
	pkgs(repoNuGet, "Trove.Test.Filler", 0, nugetPackages)
	st.Exec(`INSERT INTO package_versions (package_id, version, display_version)
		SELECT p.id, '1.' || v || '.0', '1.' || v || '.0' FROM packages p, generate_series(0, $1::int) v`, versionsPerPkg-1)
}

// web and worker start the replicas of the run.
func (in *installation) web(name string, env ...string) *testenv.Replica {
	in.t.Helper()
	return in.st.Start(in.t, testenv.ReplicaOptions{Name: name, Env: env})
}

func (in *installation) worker(name string) *testenv.Replica {
	in.t.Helper()
	return in.st.Start(in.t, testenv.ReplicaOptions{Name: name, Role: "worker"})
}

func (in *installation) runTask(r *testenv.Replica, task string) {
	in.t.Helper()
	if a := in.st.Do(r, "POST", "/api/v1/system/tasks/"+task+"/run", in.tokAdmin, nil); a.Status != 202 {
		in.t.Fatalf("run %s: %d %s", task, a.Status, a.Body)
	}
}

// activeStates are the states of a job that has not ended: every value of the schema's type job_state that the job package
// does not count as terminal (jobs.Job.Terminal). Read from the type, so that a state added to it later counts as not ended
// instead of as settled.
func activeStates(t *testing.T, st *testenv.Stack) []string {
	t.Helper()
	var active []string
	for _, s := range strings.Split(st.Text(`SELECT string_agg(s::text, ',' ORDER BY s) FROM unnest(enum_range(NULL::job_state)) AS s`), ",") {
		if !(jobs.Job{State: s}).Terminal() {
			active = append(active, s)
		}
	}
	if !slices.Contains(active, jobs.StateQueued) || !slices.Contains(active, jobs.StateRunning) {
		t.Fatalf("the states of a job that has not ended are %v; queued and running must be among them", active)
	}
	return active
}

// settled reports whether the scanning has nothing left to do: no sync or evaluation job that has not ended (queued, also
// while it waits for a retry, running, or being cancelled) and no advisory or ecosystem waiting for evaluation.
func (in *installation) settled() bool {
	return in.st.Number(`SELECT count(*) FROM job WHERE kind LIKE 'vuln-%' AND state::text = ANY($1::text[])`, in.active) == 0 &&
		in.st.Number(`SELECT count(*) FROM vuln_pending_evaluation`) == 0 &&
		in.st.Number(`SELECT count(*) FROM vuln_source_state WHERE full_evaluation_requested_at IS NOT NULL`) == 0
}

// jobEnded returns a check for wait: once the newest job of the kind has ended in a state that is not one of fine, it names
// the job and that state; it is empty while the job has not ended, when it ended in one of fine, and while there is none.
func (in *installation) jobEnded(kind string, fine ...string) func() string {
	return func() string {
		s := in.st.Text(`SELECT COALESCE((SELECT state::text FROM job WHERE kind = $1 ORDER BY created_at DESC LIMIT 1), '')`, kind)
		if s == "" || slices.Contains(in.active, s) || slices.Contains(fine, s) {
			return ""
		}
		return "the job " + kind + " ended " + s
	}
}

// wait polls done every 20 ms and returns how long it took to hold. It gives up when within has passed, or earlier when ended
// (nil: never) names what the scenario waits for as ended without done; ended is asked once a second, so that the polling
// adds little to the database's load. When it gives up it logs what the scanning's tables and the replicas' logs say,
// records the row id as not finished with the time it waited, and ends the subtest.
func (in *installation) wait(t *testing.T, rep *report, id, what string, within time.Duration, done func() bool, ended func() string) time.Duration {
	t.Helper()
	start, asked := time.Now(), time.Now()
	for {
		if done() {
			return time.Since(start)
		}
		took := time.Since(start)
		note := ""
		switch {
		case took >= within:
			note = fmt.Sprintf("its wait ran out after %s", within)
		case ended != nil && time.Since(asked) >= time.Second:
			asked = time.Now()
			if why := ended(); why != "" {
				if done() { // what ended wrote the result before it ended
					return time.Since(start)
				}
				note = fmt.Sprintf("%s after %s", why, took.Round(time.Second))
			}
		}
		if note == "" {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		in.diagnose(t, id)
		rep.notFinished(id, &took, note)
		t.Fatalf("%s: %s did not happen: %s", id, what, note)
	}
}

// diagnose logs what the run needs to explain a wait that gave up: the advisory source state of each ecosystem, every job of
// the scanning with its attempt, its next run, its lease, its checkpoint and its error, the rows queued for evaluation and
// touched by an unfinished sync, and the lines of the replicas' logs that say why a sync or a job ended. It writes every
// replica's whole log so far to .out/perf-vuln-logs, which the workflow uploads, so that the window of the failure is kept and
// not only the tail a replica prints when it stops.
func (in *installation) diagnose(t *testing.T, id string) {
	t.Helper()
	t.Logf("%s: the advisory source state:\n%s", id, in.st.Text(`SELECT COALESCE(string_agg(format(
		'%s %s: state %s, failures %s, last_error %s, attempted_at %s, synced_at %s, full_synced_at %s, records %s, data_epoch %s, full evaluation requested at %s',
		source, COALESCE(NULLIF(ecosystem, ''), '-'), state, failures, COALESCE(last_error::text, '-'), COALESCE(attempted_at::text, '-'),
		COALESCE(synced_at::text, '-'), COALESCE(full_synced_at::text, '-'), records, data_epoch, COALESCE(full_evaluation_requested_at::text, '-')),
		E'\n' ORDER BY source, ecosystem), '(no row)') FROM vuln_source_state`))
	t.Logf("%s: the jobs of the scanning (the database's clock is %s):\n%s", id, in.st.Text(`SELECT now()::text`), in.st.Text(`SELECT COALESCE(string_agg(format(
		'%s %s: state %s, attempt %s of %s, run_at %s, locked_until %s, started_at %s, finished_at %s, progress %s of %s %s, checkpoint %s, error %s',
		kind, id, state, attempt, max_attempts, run_at, COALESCE(locked_until::text, '-'), COALESCE(started_at::text, '-'),
		COALESCE(finished_at::text, '-'), COALESCE(progress_done::text, '-'), COALESCE(progress_total::text, '-'), COALESCE(progress_unit, ''),
		COALESCE(checkpoint::text, '-'), COALESCE(error::text, '-')), E'\n' ORDER BY created_at), '(no job)') FROM job WHERE kind LIKE 'vuln-%'`))
	t.Logf("%s: per ecosystem, advisories %s; queued for evaluation %s; names touched by an unfinished sync %s", id,
		in.st.Text(`SELECT COALESCE(string_agg(ecosystem || ' ' || n, ', ' ORDER BY ecosystem), 'none') FROM (SELECT ecosystem, count(*) AS n FROM vuln_advisory GROUP BY ecosystem) c`),
		in.st.Text(`SELECT COALESCE(string_agg(ecosystem || ' ' || n, ', ' ORDER BY ecosystem), 'none') FROM (SELECT ecosystem, count(*) AS n FROM vuln_pending_evaluation GROUP BY ecosystem) c`),
		in.st.Text(`SELECT COALESCE(string_agg(ecosystem || ' ' || n, ', ' ORDER BY ecosystem), 'none') FROM (SELECT ecosystem, count(*) AS n FROM vuln_sync_touched GROUP BY ecosystem) c`))
	dir := in.saveLogs(t)
	for _, r := range in.st.Replicas() {
		t.Logf("%s: the log of %s, WARN and ERROR lines and those of the sync and the jobs, the last 80 (the whole log is %s):\n%s",
			id, r.Name, filepath.Join(dir, r.Name+".log"), logExcerpt(r.Logs.String(), 80))
	}
}

// saveLogs writes every replica's log so far to .out/perf-vuln-logs/<replica>.log and returns the directory. A later call
// writes a log again with everything the replica wrote since, so the last call holds the most.
func (in *installation) saveLogs(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(repoRoot(), ".out", "perf-vuln-logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Errorf("the replicas' logs: %v", err)
		return dir
	}
	for _, r := range in.st.Replicas() {
		if err := os.WriteFile(filepath.Join(dir, r.Name+".log"), []byte(r.Logs.String()), 0o644); err != nil { //nolint:gosec // the log of a test replica, not a secret
			t.Errorf("the log of %s: %v", r.Name, err)
		}
	}
	return dir
}

// TestR_VULN_028_PerformanceRun is the run of R-VULN-028: V-1 to V-9 in order, each recorded in .out/perf-vuln.json.
// Without TROVE_PERF_GATE=1 it reports; with it a value above its target fails the test (the nightly workflow sets it). A
// scenario that does not finish fails the test either way and is recorded as not finished. V-2 and V-9 need V-1's import;
// every later scenario needs V-1 and V-2, because it measures an installation that is imported and evaluated: a scenario
// whose need did not finish is recorded as not measured and does not run.
func TestR_VULN_028_PerformanceRun(t *testing.T) {
	in := newInstallation(t)
	rep := &report{t: t}
	defer rep.write()

	web := in.web("web-1")
	in.worker("worker-1")
	t.Cleanup(func() {
		if t.Failed() {
			in.saveLogs(t)
		}
	})
	imported, evaluated := []string{"V-1"}, []string{"V-1", "V-2"}

	// V-1: the full import of the npm archive into empty tables. The clock starts at the task's request and stops when
	// the ecosystem's state is complete. The job imports NuGet first (the ecosystems run in sorted order) and ends with npm;
	// when it ends without npm complete, nothing else will complete it (the scheduled tasks are off), and the wait gives up.
	rep.scenario(t, "V-1 full import of the npm archive", []string{"V-1"}, nil, func(t *testing.T) {
		start := time.Now()
		in.runTask(web, "vuln-sync-osv-full")
		in.wait(t, rep, "V-1", "the first import of npm", 40*time.Minute, func() bool {
			return in.st.Text(`SELECT COALESCE((SELECT state FROM vuln_source_state WHERE source = 'osv' AND ecosystem = 'npm'), 'none')`) == "complete"
		}, in.jobEnded("vuln-sync-osv-full"))
		d := time.Since(start)
		rep.record("V-1", d.Minutes(), fmt.Sprintf("%d records", in.st.Number(`SELECT count(*) FROM vuln_advisory WHERE ecosystem = 'npm'`)))
	})

	// V-2: the full evaluation of the 100,000 versions, which the first complete import requests (Q-VULN-18). The clock
	// starts when the import has completed.
	rep.scenario(t, "V-2 full evaluation of 100,000 versions", []string{"V-2"}, imported, func(t *testing.T) {
		start := time.Now()
		in.wait(t, rep, "V-2", "the full evaluation", 30*time.Minute, in.settled, nil)
		rep.record("V-2", time.Since(start).Minutes(), fmt.Sprintf("%d findings", in.st.Number(`SELECT count(*) FROM vuln_finding`)))
	})

	// V-9: the advisory tables with their indexes, npm and NuGet.
	rep.scenario(t, "V-9 size of the advisory tables", []string{"V-9"}, imported, func(t *testing.T) {
		b := in.st.Number(`SELECT pg_total_relation_size('vuln_advisory') + pg_total_relation_size('vuln_affected')`)
		rep.record("V-9", float64(b)/(1<<30),
			fmt.Sprintf("%d advisories, %d affected entries", in.st.Number(`SELECT count(*) FROM vuln_advisory`), in.st.Number(`SELECT count(*) FROM vuln_affected`)))
	})

	// V-3: an incremental sync of 500 changed records, then their evaluation. The clock starts at the task's start. The sync
	// is incremental only on a complete ecosystem; after an import that did not finish it would be a full one.
	rep.scenario(t, "V-3 incremental sync of 500 changed records", []string{"V-3"}, evaluated, func(t *testing.T) {
		for i := range changedRecords {
			id := fmt.Sprintf("GHSA-fill-%04d-0000", i)
			raw, ok := in.stub.Record("npm", id)
			if !ok {
				t.Fatalf("the test source has no record %s", id)
			}
			var rec map[string]any
			if err := json.Unmarshal(raw, &rec); err != nil {
				t.Fatal(err)
			}
			rec["summary"] = fmt.Sprintf("Filler record %04d, changed", i)
			body, _ := json.Marshal(rec)
			if _, err := in.stub.Upsert("npm", body); err != nil {
				t.Fatal(err)
			}
		}
		start := time.Now()
		in.runTask(web, "vuln-sync-osv")
		in.wait(t, rep, "V-3", "the incremental sync and its evaluation", 15*time.Minute, func() bool {
			return in.st.Number(`SELECT count(*) FROM job WHERE kind = 'vuln-sync-osv' AND state = 'succeeded'`) >= 1 && in.settled()
		}, in.jobEnded("vuln-sync-osv", jobs.StateSucceeded))
		rep.record("V-3", time.Since(start).Minutes(), "")
	})

	// V-4: the evaluation added to a publish. 300 versions are published to npm-local with data present. The replica's
	// histogram of the trigger publish observes each evaluation once, inside the publish; scraped before and after every
	// publish, its _sum and _count move by that one call's duration, so the p99 is read from 300 exact samples and not from
	// a bucket (F-43). The histogram stays what production reads; its bucket bound is noted beside the exact value.
	rep.scenario(t, "V-4 evaluation on publish", []string{"V-4"}, evaluated, func(t *testing.T) {
		const metric, series = "trove_vuln_evaluation_duration_seconds", `trigger="publish"`
		sum, count, _ := histogramTotals(web.Metrics(t), metric, series)
		var samples []time.Duration
		repeated := 0
		for i := range publishes {
			name := fmt.Sprintf("trove-test-filler-%04d", 100000+i) // not in the dump: the common case of a publish
			if i%10 == 0 {
				name = fmt.Sprintf("trove-test-filler-%04d", i) // one in ten has an advisory (version 1.0.0 is affected)
			}
			publish(t, in, web, repoNPMLocal, name, fmt.Sprintf("1.%d.0", 20+i))
			nextSum, nextCount, ok := histogramTotals(web.Metrics(t), metric, series)
			if !ok || nextCount == count {
				t.Fatalf("publish %d was not evaluated: %s{%s} did not move", i, metric, series)
			}
			if nextCount-count > 1 {
				repeated++ // a transaction that ran again observed more than once: its sample is the sum of the runs
			}
			samples = append(samples, time.Duration((nextSum-sum)*float64(time.Second)))
			sum, count = nextSum, nextCount
		}
		bound := histogramBound(web.Metrics(t), metric, series, 0.99)
		rep.record("V-4", ms(percentile(samples, 0.99)),
			fmt.Sprintf("%d publishes, median %.2f ms, max %.2f ms, %d evaluated more than once, histogram bucket bound of the p99 %.0f ms",
				publishes, ms(percentile(samples, 0.5)), ms(percentile(samples, 1)), repeated, bound*1000))
	})

	// V-5: the block check of a download of a stored item. One replica has the scanning on, one off; the same stored
	// item is requested alternately from both, so that the difference is the block check. The queries are the commits
	// and rollbacks of the database over the run, minus the idle rate of the same time on the same stack.
	rep.scenario(t, "V-5 block check on a cached download", []string{"V-5a", "V-5b"}, evaluated, func(t *testing.T) {
		off := in.web("web-off", "TROVE_VULNERABILITIES_ENABLED=false")
		name := "perf-cached"
		publish(t, in, web, repoNPMLocal, name, "1.0.0")
		path := fmt.Sprintf("/api/npm/%s/%s/-/%s-1.0.0.tgz", repoNPMLocal, name, name)
		get := func(r *testenv.Replica) time.Duration {
			start := time.Now()
			if a := in.st.Do(r, "GET", path, in.tokAdmin, nil); a.Status != 200 {
				t.Fatalf("download from %s: %d %s", r.Name, a.Status, a.Body)
			}
			return time.Since(start)
		}
		for range 50 { // warm both caches
			get(web)
			get(off)
		}
		xacts := func() int64 {
			return in.st.Number(`SELECT xact_commit + xact_rollback FROM pg_stat_database WHERE datname = current_database()`)
		}
		var on, offS []time.Duration
		x0, began := xacts(), time.Now()
		for range cachedDownloads {
			on = append(on, get(web))
			offS = append(offS, get(off))
		}
		x1, loaded := xacts(), time.Since(began)
		// The idle rate: the same wall time without requests.
		idle0 := xacts()
		time.Sleep(loaded)
		idle1 := xacts()
		queries := float64((x1-x0)-(idle1-idle0)) / float64(2*cachedDownloads)
		rep.record("V-5a", ms(percentile(on, 0.99))-ms(percentile(offS, 0.99)),
			fmt.Sprintf("p99 on %.2f ms, off %.2f ms", ms(percentile(on, 0.99)), ms(percentile(offS, 0.99))))
		rep.record("V-5b", max(queries, 0),
			"the target is 0 added queries; the bound allows one background transaction per request of noise on a shared stack, read the value")
	})

	// V-6: the local audit of a lockfile of 1,000 packages: 1,000 names with one version each, half of them with an advisory.
	rep.scenario(t, "V-6 npm local audit of a 1,000-package lockfile", []string{"V-6"}, evaluated, func(t *testing.T) {
		body := map[string][]string{}
		for i := range auditPackages {
			body[fmt.Sprintf("trove-test-filler-%04d", i)] = []string{"1.0.0"}
		}
		raw, _ := json.Marshal(body)
		var samples []time.Duration
		for range auditRepeats {
			start := time.Now()
			a := in.st.Do(web, "POST", "/api/npm/"+repoNPMLocal+"/-/npm/v1/security/advisories/bulk", in.tokAdmin, raw)
			if a.Status != 200 {
				t.Fatalf("audit: %d %s", a.Status, a.Body)
			}
			samples = append(samples, time.Since(start))
		}
		rep.record("V-6", ms(percentile(samples, 0.99)), "")
	})

	// V-7 and V-8 need the findings of a completed installation at their size, and the NuGet cache.
	rep.scenario(t, "V-7 first page of the findings list with 100,000 findings", []string{"V-7a", "V-7b"}, evaluated, func(t *testing.T) {
		in.fillFindings()
		list := func(tok string) []time.Duration {
			var samples []time.Duration
			for range listRepeats {
				start := time.Now()
				a := in.st.Do(web, "GET", "/api/v1/vulnerabilities/findings?limit=50", tok, nil)
				if a.Status != 200 {
					t.Fatalf("list findings: %d %s", a.Status, a.Body)
				}
				samples = append(samples, time.Since(start))
			}
			return samples
		}
		rep.record("V-7a", ms(percentile(list(in.tokAdmin), 0.99)), fmt.Sprintf("%d findings", in.st.Number(`SELECT count(*) FROM vuln_finding`)))
		rep.record("V-7b", ms(percentile(list(in.tokGrantee), 0.99)), "")
	})

	rep.scenario(t, "V-8 NuGet vulnerability page from the replica's cache", []string{"V-8"}, evaluated, func(t *testing.T) {
		index := in.st.Do(web, "GET", "/api/nuget/"+repoNuGet+"/v3/vulnerabilities/index.json", in.tokAdmin, nil)
		if index.Status != 200 {
			t.Fatalf("the vulnerability index: %d %s", index.Status, index.Body)
		}
		var entries []struct {
			ID string `json:"@id"`
		}
		if err := json.Unmarshal(index.Body, &entries); err != nil || len(entries) == 0 {
			t.Fatalf("the vulnerability index has no page: %v %s", err, index.Body)
		}
		page := entries[0].ID
		if i := strings.Index(page, "/api/"); i >= 0 {
			page = page[i:]
		}
		in.st.Do(web, "GET", page, in.tokAdmin, nil) // the first request fills the cache
		var samples []time.Duration
		for range nugetPageRepeats {
			start := time.Now()
			if a := in.st.Do(web, "GET", page, in.tokAdmin, nil); a.Status != 200 {
				t.Fatalf("the vulnerability page: %d %s", a.Status, a.Body)
			}
			samples = append(samples, time.Since(start))
		}
		rep.record("V-8", ms(percentile(samples, 0.99)), "")
	})
}

// fillFindings brings the findings to 100,000, one per stored version, by SQL: the findings the evaluation made are kept,
// the rest are inserted with advisory identifiers of their own, so that the list has the size of V-7 whatever the filler
// records matched.
func (in *installation) fillFindings() {
	in.st.Exec(`INSERT INTO vuln_finding (id, repo_id, ecosystem, package_name, version, advisory_id, package_id, version_id, kind, severity,
			severity_rank, cvss_score, fix_available, fixed_versions, status, advisory_modified)
		SELECT gen_random_uuid(), p.repo_id, CASE r.format WHEN 'nuget' THEN 'NuGet' ELSE 'npm' END, p.name, pv.version, 'GHSA-perf-' || pv.id, p.id, pv.id,
		       'vulnerability', (ARRAY['critical','high','medium','low'])[1 + pv.id % 4], 5 - (pv.id % 4)::int, 5.0, true, '{9.9.9}', 'needs_triage', now()
		  FROM package_versions pv JOIN packages p ON p.id = pv.package_id JOIN repositories r ON r.id = p.repo_id
		 WHERE NOT EXISTS (SELECT 1 FROM vuln_finding f WHERE f.version_id = pv.id)`)
}

// publish publishes name@version to a local npm repository through the replica as the administrator and requires 201.
func publish(t *testing.T, in *installation, r *testenv.Replica, repo, name, version string) {
	t.Helper()
	a := in.st.Do(r, "PUT", "/api/npm/"+repo+"/"+name, in.tokAdmin, publishDoc(t, name, version))
	if a.Status != 201 {
		t.Fatalf("publish %s@%s: %d %s", name, version, a.Status, a.Body)
	}
}
