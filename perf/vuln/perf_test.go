//go:build integration

package vulnperf

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/scope"
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
}

const (
	repoNPMLocal  = "npm-local"
	repoNPMRemote = "npm-remote"
	repoNuGet     = "nuget-local"
)

// newInstallation starts the advisory source with the dumps of the size of the real ones, creates the stack and seeds the
// 100,000 stored versions in SQL: a version is a package row and a version row, which is all the scanning reads (R-VULN-001).
// The packages are named like the stub's filler records (trove-test-filler-NNNN, Trove.Test.FillerNNNN), so that the
// version 1.0.0 of every package is affected by one record and the others by none: 10 % of the stored versions get a finding.
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
	in := &installation{t: t, st: st, stub: stub}
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
// the NuGet filler names 0 to 999.
func (in *installation) seed() {
	st := in.st
	pkgs := func(repo, prefix string, from, to int) {
		st.Exec(`INSERT INTO packages (repo_id, name, display_name)
			SELECT r.id, $2::text || lpad(g::text, 4, '0'), $2::text || lpad(g::text, 4, '0')
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

// settled reports whether the scanning has nothing left to do: no sync or evaluation job queued or running and no advisory
// waiting for evaluation.
func (in *installation) settled() bool {
	return in.st.Number(`SELECT count(*) FROM job WHERE kind LIKE 'vuln-%' AND state IN ('queued', 'running')`) == 0 &&
		in.st.Number(`SELECT count(*) FROM vuln_pending_evaluation`) == 0 &&
		in.st.Number(`SELECT count(*) FROM vuln_source_state WHERE full_evaluation_requested_at IS NOT NULL`) == 0
}

// TestR_VULN_028_PerformanceRun is the run of R-VULN-028: V-1 to V-9 in order, each recorded in .out/perf-vuln.json.
// Without TROVE_PERF_GATE=1 it reports; with it a value above its target fails the test (the nightly workflow sets it).
func TestR_VULN_028_PerformanceRun(t *testing.T) {
	in := newInstallation(t)
	rep := &report{t: t}
	defer rep.write()

	web := in.web("web-1")
	in.worker("worker-1")

	// V-1: the full import of the npm archive into empty tables. The clock starts at the task's request and stops when
	// the ecosystem's state is complete.
	t.Run("V-1 full import of the npm archive", func(t *testing.T) {
		start := time.Now()
		in.runTask(web, "vuln-sync-osv-full")
		testenv.WaitFor(t, "the first import of npm", 40*time.Minute, func() bool {
			return in.st.Text(`SELECT COALESCE((SELECT state FROM vuln_source_state WHERE source = 'osv' AND ecosystem = 'npm'), 'none')`) == "complete"
		})
		d := time.Since(start)
		rep.record("V-1", "full import of the npm archive into empty tables", d.Minutes(), "min", 15,
			fmt.Sprintf("%d records", in.st.Number(`SELECT count(*) FROM vuln_advisory WHERE ecosystem = 'npm'`)))
	})

	// V-2: the full evaluation of the 100,000 versions, which the first complete import requests (Q-VULN-18). The clock
	// starts when the import has completed.
	t.Run("V-2 full evaluation of 100,000 versions", func(t *testing.T) {
		start := time.Now()
		testenv.WaitFor(t, "the full evaluation", 30*time.Minute, in.settled)
		rep.record("V-2", "full evaluation of the 100,000 versions", time.Since(start).Minutes(), "min", 5,
			fmt.Sprintf("%d findings", in.st.Number(`SELECT count(*) FROM vuln_finding`)))
	})

	// V-9: the advisory tables with their indexes, npm and NuGet.
	t.Run("V-9 size of the advisory tables", func(t *testing.T) {
		b := in.st.Number(`SELECT pg_total_relation_size('vuln_advisory') + pg_total_relation_size('vuln_affected')`)
		rep.record("V-9", "size of the advisory tables with indexes", float64(b)/(1<<30), "GiB", 2,
			fmt.Sprintf("%d advisories, %d affected entries", in.st.Number(`SELECT count(*) FROM vuln_advisory`), in.st.Number(`SELECT count(*) FROM vuln_affected`)))
	})

	// V-3: an incremental sync of 500 changed records, then their evaluation. The clock starts at the task's start.
	t.Run("V-3 incremental sync of 500 changed records", func(t *testing.T) {
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
		testenv.WaitFor(t, "the incremental sync and its evaluation", 15*time.Minute, func() bool {
			return in.st.Number(`SELECT count(*) FROM job WHERE kind = 'vuln-sync-osv' AND state = 'succeeded'`) >= 1 && in.settled()
		})
		rep.record("V-3", "incremental sync with 500 changed records and their evaluation", time.Since(start).Minutes(), "min", 2, "")
	})

	// V-4: the evaluation added to a publish. 300 versions are published to npm-local with data present. The replica's
	// histogram of the trigger publish observes each evaluation once, inside the publish; scraped before and after every
	// publish, its _sum and _count move by that one call's duration, so the p99 is read from 300 exact samples and not from
	// a bucket (F-43). The histogram stays what production reads; its bucket bound is noted beside the exact value.
	t.Run("V-4 evaluation on publish", func(t *testing.T) {
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
		rep.record("V-4", "evaluation added to a publish commit, p99 (exact, per call)", ms(percentile(samples, 0.99)), "ms", 5,
			fmt.Sprintf("%d publishes, median %.2f ms, max %.2f ms, %d evaluated more than once, histogram bucket bound of the p99 %.0f ms",
				publishes, ms(percentile(samples, 0.5)), ms(percentile(samples, 1)), repeated, bound*1000))
	})

	// V-5: the block check of a download of a stored item. One replica has the scanning on, one off; the same stored
	// item is requested alternately from both, so that the difference is the block check. The queries are the commits
	// and rollbacks of the database over the run, minus the idle rate of the same time on the same stack.
	t.Run("V-5 block check on a cached download", func(t *testing.T) {
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
		rep.record("V-5a", "latency added by the block check on a cached download, p99", ms(percentile(on, 0.99))-ms(percentile(offS, 0.99)), "ms", 1,
			fmt.Sprintf("p99 on %.2f ms, off %.2f ms", ms(percentile(on, 0.99)), ms(percentile(offS, 0.99))))
		rep.record("V-5b", "database transactions per cached download (both replicas)", max(queries, 0), "tx", 1.0,
			"the target is 0 added queries; the bound allows one background transaction per request of noise on a shared stack, read the value")
	})

	// V-6: the local audit of a lockfile of 1,000 packages: 1,000 names with one version each, half of them with an advisory.
	t.Run("V-6 npm local audit of a 1,000-package lockfile", func(t *testing.T) {
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
		rep.record("V-6", "npm local audit of a 1,000-package lockfile, p99", ms(percentile(samples, 0.99)), "ms", 500, "")
	})

	// V-7 and V-8 need the findings of a completed installation at their size, and the NuGet cache.
	t.Run("V-7 first page of the findings list with 100,000 findings", func(t *testing.T) {
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
		rep.record("V-7a", "first page of the findings list for an admin, p99", ms(percentile(list(in.tokAdmin), 0.99)), "ms", 300,
			fmt.Sprintf("%d findings", in.st.Number(`SELECT count(*) FROM vuln_finding`)))
		rep.record("V-7b", "first page of the findings list for a caller with package-pattern grants, p99", ms(percentile(list(in.tokGrantee), 0.99)), "ms", 300, "")
	})

	t.Run("V-8 NuGet vulnerability page from the replica's cache", func(t *testing.T) {
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
		rep.record("V-8", "NuGet vulnerability page from the replica's cache, p99", ms(percentile(samples, 0.99)), "ms", 25, "")
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
