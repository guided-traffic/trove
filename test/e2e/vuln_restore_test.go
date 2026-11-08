//go:build e2e

package e2e

// E5.13 and R-VULN-026: the trove-backup drill restores blocks and triage and reconciles them after the first sync. Twelve packages
// of the vulnerability fixture are set into known states, a backup is taken from the installation, triage and the advisory source
// change after it, the backup is restored with the restore Job into the namespace trove-restore (deploy/dev/restore-ns.sh with
// TROVE_RESTORE_PROFILE=vuln: the restored installation reads the test advisory source and runs two packages replicas), and each
// replica is judged before the first synchronization and after it and the full evaluation (Q-VULN-125 to Q-VULN-127).
//
// The versions `cr` and `cm` are blocked by the evaluation after the snapshot (a re-detection and a first malicious finding). Their
// blocks are journaled (Q-VULN-130), so the replay restores them before the first synchronization and the drill judges them there too.

import (
	"bytes"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/osvstub"
)

// vrstPkg is one of the twelve packages of the drill.
type vrstPkg struct {
	label string
	npm   bool
	name  string // the package name (a NuGet ID as published)
	repo  string // the remote repository the version is cached through
	adv   string // the advisory ID
}

// finding is the key the findings API stores the package under (NuGet IDs are lowercase).
func (p vrstPkg) key() string { return strings.ToLower(p.name) }

func (p vrstPkg) path() string {
	if p.npm {
		return tarballPath(p.repo, p.name)
	}
	l := p.key()
	return nugetAddr(p.repo) + "/v3/flatcontainer/" + l + "/1.0.0/" + l + ".1.0.0.nupkg"
}

// vrstState is what the drill keeps of a finding at the snapshot.
type vrstState struct {
	id, status, etag, events string
	revision                 int64
}

// vrstReplica is a client of one packages pod of the restored installation.
type vrstReplica struct {
	pod        string
	npm, nuget *api
}

// podForwardsIn opens a port-forward to every ready pod of a selector in a namespace and returns the pod names, the local addresses
// (in pod order) and the function that closes them.
func podForwardsIn(t testing.TB, ns, selector string, port int) (pods, addrs []string, stop func()) {
	t.Helper()
	var fs []*forward
	for _, p := range podsOfIn(t, ns, selector) {
		if !p.Ready || p.Deleting {
			continue
		}
		f := portForwardIn(t, ns, "pod/"+p.Name, port)
		pods, addrs, fs = append(pods, p.Name), append(addrs, f.addr), append(fs, f)
	}
	return pods, addrs, func() {
		for _, f := range fs {
			stopForward(f)
		}
	}
}

// widenedRecord is the advisory of a package whose range is fixed in the given version (1.0.1 affects 1.0.0, 1.0.0 does not).
func widenedRecord(id, ecosystem, name, fixed string) osvstub.Advisory {
	a := triageRecord(id, ecosystem, name)
	a.Affected[0].Ranges[0].Events = []map[string]string{osvstub.Introduced("0"), osvstub.Fixed(fixed)}
	return a
}

// awaitEvaluatedRestored is awaitEvaluated for the restored installation's database.
func awaitEvaluatedRestored(t testing.TB) {
	t.Helper()
	waitFor(t, "the evaluation of the restored installation to finish", 10*time.Minute, 2*time.Second, func() bool {
		return krSQL(t, `SELECT (SELECT count(*) FROM trove.vuln_pending_evaluation)
			+ (SELECT count(*) FROM trove.job WHERE kind IN ('vuln-evaluate', 'vuln-rescan') AND state IN ('queued', 'running', 'cancelling'))
			+ (SELECT count(*) FROM trove.vuln_source_state WHERE full_evaluation_requested_at IS NOT NULL)`) == "0"
	})
}

// downRestored removes the second installation when the test ends: its PostgreSQL instance carries the labels of the cluster trove-db,
// which the drains of the variant tests select across namespaces.
func downRestored() {
	cmd := exec.Command(filepath.Join(cfg.repo, "deploy/dev", "restore-ns.sh"), "down")
	cmd.Dir = cfg.repo
	_ = cmd.Run()
}

// TestVulnLong_R_VULN_026_RestoreKeepsBlocksAndTriage is the restore drill of the vulnerability data (R-VULN-026, E5.13, Q-VULN-126).
func TestVulnLong_R_VULN_026_RestoreKeepsBlocksAndTriage(t *testing.T) {
	e := vulnFx(t)
	osv := osvStub(t)
	restoreSourceAfter(t, e, osv)
	run := e.run

	// ── the twelve packages: published to the test upstreams and cached through the remote repositories before any record exists ──
	mk := func(label string, npm bool) vrstPkg {
		p := vrstPkg{label: label, npm: npm, repo: npmRemoteKey}
		if npm {
			p.name = fmt.Sprintf("e2e-vrst-%s-%s", run, label)
		} else {
			p.name, p.repo = fmt.Sprintf("E2e.Vrst%s.L%s", run, label), vulnNuGetRemote
		}
		p.adv = fmt.Sprintf("GHSA-e2e-vrst-%s-%s", run, label)
		return p
	}
	pk := map[string]vrstPkg{}
	for _, l := range []string{"rb", "rd", "rm", "jb", "jl", "js", "jd", "cw", "cr", "cn", "cm"} {
		pk[l] = mk(l, true)
	}
	pk["rn"] = mk("rn", false)
	labels := []string{"rb", "rd", "rm", "rn", "jb", "jl", "js", "jd", "cw", "cn", "cr", "cm"}
	npmA, nugetA := e.adm.withToken(e.npm.token), e.adm.withToken(e.nuget.token)
	bytesOf := map[string][]byte{}
	for _, l := range labels {
		p := pk[l]
		if p.npm {
			stub(t).publish(t, p.name, "1.0.0", nil)
			r := npmA.get(p.path()).want(t, "the first download of "+l+" fills the cache", 200)
			bytesOf[l] = r.Body
		} else {
			nugetStub(t).nugetPublish(t, p.name, "1.0.0")
			r := nugetA.get(p.path()).want(t, "the first download of "+l+" fills the cache", 200)
			bytesOf[l] = r.Body
		}
	}
	record := func(l string) osvstub.Advisory {
		p := pk[l]
		switch {
		case l == "rm" || l == "cm":
			return maliciousRecord("MAL-2026-v"+run+l, p.name)
		case p.npm:
			return triageRecord(p.adv, "npm", p.name)
		}
		return triageRecord(p.adv, "NuGet", p.name)
	}
	eco := func(l string) string {
		if pk[l].npm {
			return "npm"
		}
		return "NuGet"
	}
	for _, l := range []string{"rb", "rd", "rm", "rn", "jb", "jl", "js", "jd", "cw", "cr"} {
		e.addRecord(t, eco(l), record(l))
	}
	e.syncIncremental(t) // sync 1: the findings of the ten packages with a record
	ids := map[string]string{}
	fid := func(l string) string {
		if id, ok := ids[l]; ok {
			return id
		}
		ids[l] = e.finding(t, pk[l].repo, pk[l].key(), "1.0.0").ID
		return ids[l]
	}
	status := func(a *api, l string) string {
		var f struct {
			Finding struct {
				Status string `json:"status"`
			} `json:"finding"`
		}
		a.get("/api/v1/vulnerabilities/findings/"+fid(l)).want(t, "read finding "+l, 200).JSON(t, &f)
		return f.Finding.Status
	}
	dismiss := func(l string) {
		g := e.adm.get("/api/v1/vulnerabilities/findings/"+fid(l)).want(t, "read the finding "+l, 200)
		e.adm.do(http.MethodPatch, "/api/v1/vulnerabilities/findings/"+fid(l),
			map[string]any{"status": "dismissed", "dismissalReason": "not_exploitable", "comment": "restore drill"},
			"If-Match", g.Header.Get("ETag"), "Content-Type", "application/merge-patch+json").want(t, "dismiss "+l, 200)
	}

	// ── triage at the source, the state at the snapshot ─────────────────────────
	for _, l := range []string{"rb", "rn", "jl", "jd", "cr"} {
		e.block(t, e.adm, fid(l))
	}
	dismiss("rd")
	// cr: the record no longer affects 1.0.0, the second sync resolves the blocked finding as no_longer_affected
	e.addRecord(t, "npm", widenedRecord(pk["cr"].adv, "npm", pk["cr"].name, "1.0.0"))
	e.syncIncremental(t) // sync 2
	if got := sql(t, `SELECT status || '|' || coalesce(resolved_reason, '') || '|' || coalesce(status_before_resolution, '') FROM trove.vuln_finding WHERE id = '`+fid("cr")+`'`); got != "resolved|no_longer_affected|blocked" {
		t.Fatalf("cr = %q after the second sync, want resolved|no_longer_affected|blocked", got)
	}
	for l, want := range map[string]string{"rb": "blocked", "rd": "dismissed", "rm": "needs_triage", "rn": "blocked", "jb": "needs_triage", "jl": "blocked", "js": "needs_triage", "jd": "blocked", "cw": "needs_triage"} {
		if got := status(e.adm, l); got != want {
			t.Fatalf("%s is %s at the snapshot, want %s", l, got, want)
		}
	}
	snap := map[string]vrstState{}
	for _, l := range labels {
		if l == "cn" || l == "cm" {
			continue
		}
		id := fid(l)
		g := e.adm.get("/api/v1/vulnerabilities/findings/"+id).want(t, "read finding "+l, 200)
		var f struct {
			Finding struct {
				Status string `json:"status"`
			} `json:"finding"`
		}
		g.JSON(t, &f)
		snap[l] = vrstState{id: id, status: f.Finding.Status, etag: g.Header.Get("ETag"), revision: revisionOf(t, sql, id), events: findingEvents(sql, t, id)}
	}
	t.Logf("the state at the snapshot: %s", describeSnapshot(snap, labels))

	// ── the backup ───────────────────────────────────────────────────────────
	zero := 0
	job := "vrst-" + run
	bakCreateJob(t, bakJob{name: job, backoff: &zero})
	end := bakWaitJob(t, cfg.namespace, job, 10*time.Minute)
	if end.Condition != "Complete" {
		t.Fatalf("the backup Job ended %s:\n%s", end.Condition, tail(bakJobLogs(cfg.namespace, job), 2500))
	}
	backupID := bakBackupID(bakJobLogs(cfg.namespace, job))
	if backupID == "" {
		t.Fatalf("the backup Job names no backup:\n%s", tail(bakJobLogs(cfg.namespace, job), 1500))
	}
	t.Logf("backup %s", backupID)

	// ── after the snapshot ───────────────────────────────────────────────────
	e.block(t, e.adm, fid("jb")) // journaled
	e.lift(t, e.adm, fid("jl"))  // not journaled
	e.block(t, e.adm, fid("js")) // journaled, and lifted again below (a lift is not journaled)
	e.lift(t, e.adm, fid("js"))
	dismiss("jd") // not journaled
	osv.send(t, http.MethodPost, "/-/stub/withdraw", map[string]any{"ecosystem": "npm", "id": pk["cw"].adv})
	e.addRecord(t, "npm", record("cn"))
	e.addRecord(t, "npm", widenedRecord(pk["cr"].adv, "npm", pk["cr"].name, "1.0.1")) // the range widened again
	e.addRecord(t, "npm", record("cm"))
	e.syncIncremental(t) // sync 3
	// the reference: the source refuses cm and has cr blocked again
	waitFor(t, "the source to refuse cm and to block cr", 2*time.Minute, time.Second, func() bool {
		return status(e.adm, "cr") == "blocked" && npmA.get(pk["cm"].path()).Status == 403
	})
	if got := status(e.adm, "cw"); got != "resolved" {
		t.Fatalf("the source has cw %s after its record was withdrawn, want resolved", got)
	}
	sourceRecords := sql(t, `SELECT string_agg(ecosystem || '=' || records, ',' ORDER BY ecosystem) FROM trove.vuln_source_state WHERE source = 'osv' AND ecosystem IN ('npm', 'NuGet')`)
	t.Logf("the source's record counts: %s", sourceRecords)
	// the journal is shipped when no row is pending in the outbox (the shipper deletes what it wrote to the bucket)
	waitFor(t, "the revocation journal to be shipped", 3*time.Minute, 2*time.Second, func() bool {
		return sql(t, `SELECT count(*) FROM trove.revocation_outbox`) == "0"
	})

	// ── the restore ──────────────────────────────────────────────────────────
	t.Setenv("TROVE_RESTORE_PROFILE", "vuln")
	t.Cleanup(downRestored)
	restoreNSScript(t, "infra")
	restoreNSScript(t, "install", backupID)
	re := bakWaitJob(t, restoreNS, "trove-restore", 20*time.Minute)
	logs := bakJobLogs(restoreNS, "trove-restore")
	if re.Condition != "Complete" {
		t.Fatalf("the restore Job ended %s:\n%s", re.Condition, tail(logs, 3000))
	}
	t.Logf("the restore's replay:\n%s", tail(logs, 2500))
	restoreNSScript(t, "serve")
	rst := openRestored(t, adminToken(t))
	pods, addrs, stop := podForwardsIn(t, restoreNS, selPackages, 8080)
	defer stop()
	if len(pods) != 2 {
		t.Fatalf("the restored installation has %d ready packages pods %v, want 2", len(pods), pods)
	}
	var reps []vrstReplica
	for i, addr := range addrs {
		r := vrstReplica{pod: pods[i], npm: apiAtPod(t, addr, e.npm.token), nuget: apiAtPod(t, addr, e.nuget.token)}
		reps = append(reps, r)
	}
	for _, r := range reps {
		if got := r.npm.get(npmAddr(npmRemoteKey) + "/-/ping").Replica(); got != r.pod {
			t.Fatalf("a request through the forward of %s was answered by %q", r.pod, got)
		}
	}
	client := func(r vrstReplica, p vrstPkg) *api {
		if p.npm {
			return r.npm
		}
		return r.nuget
	}
	probe := func(r vrstReplica, p vrstPkg) blockProbe {
		return blockProbe{what: p.label, path: p.path(), a: client(r, p)}
	}
	refused := func(t *testing.T, r vrstReplica, l string) {
		t.Helper()
		for i := range 3 {
			if _, kind, what := probeOnce(probe(r, pk[l]), i); kind != "" {
				t.Errorf("%s: %s is not refused (%s): %s", r.pod, l, kind, what)
				return
			}
		}
	}
	// rfinding reads the one finding of the package's version in the restored database, resolved ones included (the API's list leaves
	// them out by default); ok is false when there is none
	rfinding := func(l string) (vulnFinding, bool) {
		out := krSQL(t, `SELECT string_agg(id || '|' || status || '|' || kind, ';') FROM trove.vuln_finding WHERE package_name = '`+pk[l].key()+`' AND version = '1.0.0'`)
		if out == "" {
			return vulnFinding{}, false
		}
		f := strings.Split(out, "|")
		if len(f) != 3 {
			t.Fatalf("%s has the findings %q on the restored installation, want one", l, out)
		}
		return vulnFinding{ID: f[0], Status: f[1], Kind: f[2]}, true
	}
	rstatus := func(l string) string {
		f, ok := rfinding(l)
		if !ok {
			return "none"
		}
		return f.Status
	}
	audit := func(a *api) reply {
		tree := map[string][]string{}
		for _, l := range labels {
			if pk[l].npm {
				tree[pk[l].name] = []string{"1.0.0"}
			}
		}
		return a.do(http.MethodPost, npmAddr(npmRemoteKey)+"/-/npm/v1/security/advisories/bulk", bulkRequest(t, tree), "Content-Type", "application/json", "Content-Encoding", "gzip")
	}
	resource := func(a *api) bool {
		si := a.get(nugetAddr(vulnNuGetLocal)+"/v3/index.json").want(t, "service index of "+vulnNuGetLocal, 200)
		return strings.Contains(string(si.Body), "VulnerabilityInfo")
	}

	t.Run("R-VULN-026 R-BAK-004 the restored installation has no advisory data: the six transient tables are empty and no ecosystem has a source state", func(t *testing.T) {
		for _, tbl := range []string{"vuln_advisory", "vuln_affected", "vuln_kev", "vuln_source_state", "vuln_pending_evaluation", "vuln_claimed_match"} {
			if n := krSQL(t, `SELECT count(*) FROM trove.`+tbl); n != "0" {
				t.Errorf("the restored table %s has %s rows, want 0", tbl, n)
			}
		}
	})

	t.Run("R-VULN-026 R-HA-054 R-VULN-013 the journal window: the blocks set after the snapshot are replayed, a lift and a dismissal after it are not", func(t *testing.T) {
		for _, l := range []string{"jb", "js", "cr", "cm"} {
			if !replayApplied(logs, pk[l].name+"@1.0.0") {
				t.Errorf("the restore's output does not list the block of %s (%s@1.0.0) as applied:\n%s", l, pk[l].name, tail(logs, 2500))
			}
		}
		for _, l := range []string{"jb", "jl", "js", "jd"} {
			if got := rstatus(l); got != "blocked" {
				t.Errorf("%s is %s on the restored installation, want blocked", l, got)
			}
		}
		if n := krSQL(t, `SELECT count(*) FROM trove.vuln_finding_event WHERE finding_id = '`+snap["js"].id+`' AND change = 'status_changed'
			AND to_status = 'blocked' AND actor IS NULL AND details ? 'journal'`); n != "1" {
			t.Errorf("js has %s history rows of a replayed block (status_changed to blocked, no principal, the entry in its details), want 1:\n%s", n, findingEvents(func(t testing.TB, q string) string { return krSQL(t, q) }, t, snap["js"].id))
		}
	})

	t.Run("R-VULN-026 R-VULN-015 R-VULN-011 before the first sync every restored block refuses on every replica, the dismissed version is served and nothing resolves", func(t *testing.T) {
		for _, r := range reps {
			for _, l := range []string{"rb", "rm", "rn", "jb", "jl", "js", "jd", "cr", "cm"} {
				refused(t, r, l)
			}
			got := client(r, pk["rd"]).get(pk["rd"].path())
			if got.Status != 200 || !bytes.Equal(got.Body, bytesOf["rd"]) {
				t.Errorf("%s: the dismissed version rd answers %s, want 200 with the cached bytes", r.pod, got)
			}
			if a := audit(r.npm); a.Status != 503 {
				t.Errorf("%s: the npm audit answers %s before the first sync, want 503", r.pod, a)
			}
			if resource(r.nuget) {
				t.Errorf("%s: the service index of %s lists the vulnerability resource before the first sync", r.pod, vulnNuGetLocal)
			}
		}
		if got := rstatus("cw"); got != "needs_triage" {
			t.Errorf("cw is %s before the first sync, want needs_triage: nothing resolves without data", got)
		}
		// cr was resolved at the snapshot and re-detected as blocked after it: the journaled block comes back (Q-VULN-114, Q-VULN-130)
		if got := rstatus("cr"); got != "blocked" {
			t.Errorf("cr is %s before the first sync, want blocked by the replay", got)
		}
		resolved := krSQL(t, `SELECT coalesce(string_agg(package_name, ',' ORDER BY package_name), '') FROM trove.vuln_finding WHERE status = 'resolved' AND package_name LIKE '%vrst%`+strings.ToLower(run)+`%'`)
		if resolved != "" {
			t.Errorf("the findings %s of the drill's packages are resolved before the first sync: nothing resolves without data", resolved)
		}
		if _, ok := rfinding("cn"); ok {
			t.Error("cn has a finding before the first sync")
		}
		// cm did not exist at the snapshot: the replay creates the blocked finding (Q-VULN-131)
		if f, ok := rfinding("cm"); !ok || f.Status != "blocked" {
			t.Errorf("cm has %+v (found %v) before the first sync, want a blocked finding created by the replay", f, ok)
		}
	})

	t.Run("R-VULN-026 R-BAK-022 the restored findings keep their IDs and history, their revisions are lifted by 2^32, and the source's ETag is stale", func(t *testing.T) {
		// rm is not among them: its detection blocked it (blockMalicious) and the journaled block is in the replay's window, which starts
		// before the snapshot, so the replay set it blocked once more and raised its revision (Q-VULN-114, Q-VULN-130)
		if got := rstatus("rm"); got != "blocked" {
			t.Errorf("rm is %s on the restored installation, want blocked by the replay", got)
		}
		for _, l := range []string{"rb", "rd", "rn"} {
			s := snap[l]
			if got := rstatus(l); got != s.status {
				t.Errorf("%s is %s on the restored installation, want %s", l, got, s.status)
			}
			rev := revisionOf(t, func(t testing.TB, q string) string { return krSQL(t, q) }, s.id)
			if rev != s.revision+(1<<32) {
				t.Errorf("%s has revision %d on the restored installation, want the source's %d plus 2^32 (%d)", l, rev, s.revision, s.revision+(1<<32))
			}
			rep := rst.api.do(http.MethodPatch, "/api/v1/vulnerabilities/findings/"+s.id, map[string]any{"status": "confirmed"},
				"If-Match", s.etag, "Content-Type", "application/merge-patch+json")
			if rep.Status != 412 {
				t.Errorf("a PATCH of %s with the source's ETag answers %s, want 412", l, rep)
			}
		}
		if got := findingEvents(func(t testing.TB, q string) string { return krSQL(t, q) }, t, snap["rd"].id); got != snap["rd"].events {
			t.Errorf("the history of rd differs:\nsource:   %s\nrestored: %s", snap["rd"].events, got)
		}
	})

	var syncStart string
	t.Run("R-VULN-026 R-VULN-004 the first vuln-sync-osv after the restore imports both ecosystems completely and requests the full evaluation", func(t *testing.T) {
		syncStart = krSQL(t, `SELECT now()`)
		r := rst.api.do(http.MethodPost, "/api/v1/system/tasks/vuln-sync-osv/run", nil).want(t, "run vuln-sync-osv on the restored installation", 202)
		var j struct {
			Job struct {
				ID string `json:"id"`
			} `json:"job"`
		}
		r.JSON(t, &j)
		var st string
		waitFor(t, "the first synchronization of the restored installation to end", 10*time.Minute, 2*time.Second, func() bool {
			st = krSQL(t, `SELECT state FROM trove.job WHERE id = '`+j.Job.ID+`'`)
			return st == "succeeded" || st == "failed" || st == "cancelled"
		})
		if st != "succeeded" {
			t.Fatalf("the first synchronization ended %s: %s", st, krSQL(t, `SELECT coalesce(error::text, '') FROM trove.job WHERE id = '`+j.Job.ID+`'`))
		}
		got := krSQL(t, `SELECT string_agg(ecosystem || '=' || state || ':' || records, ',' ORDER BY ecosystem) FROM trove.vuln_source_state WHERE source = 'osv' AND ecosystem IN ('npm', 'NuGet')`)
		var want []string
		for _, kv := range strings.Split(sourceRecords, ",") {
			want = append(want, strings.Replace(kv, "=", "=complete:", 1))
		}
		sort.Strings(want)
		if got != strings.Join(want, ",") {
			t.Errorf("the source state after the first synchronization is %q, want %q (complete, the source's record counts)", got, strings.Join(want, ","))
		}
		awaitEvaluatedRestored(t)
		if n := krSQL(t, `SELECT count(*) FROM trove.job WHERE kind = 'vuln-evaluate' AND created_at >= '`+syncStart+`'`); n == "0" {
			t.Error("the first synchronization requested no full evaluation: no vuln-evaluate job was made after it started")
		}
		if n := krSQL(t, `SELECT count(*) FROM trove.job WHERE kind IN ('vuln-evaluate', 'vuln-rescan') AND state IN ('queued', 'running', 'cancelling')`); n != "0" {
			t.Errorf("%s evaluation jobs are still queued or running", n)
		}
	})

	t.Run("R-VULN-026 R-VULN-011 R-VULN-012 after the first sync and the full evaluation blocks and triage stand on every replica and the source's changes are reconciled", func(t *testing.T) {
		want := map[string]string{"rb": "blocked", "rd": "dismissed", "rn": "blocked", "jb": "blocked", "jl": "blocked", "js": "blocked", "jd": "blocked", "cw": "resolved", "cr": "blocked"}
		for l, w := range want {
			if got := rstatus(l); got != w {
				t.Errorf("%s is %s after the first sync, want %s", l, got, w)
			}
		}
		if f, ok := rfinding("cw"); ok {
			if got := krSQL(t, `SELECT coalesce(resolved_reason, '') FROM trove.vuln_finding WHERE id = '`+f.ID+`'`); got != "advisory_withdrawn" {
				t.Errorf("cw is resolved with %q, want advisory_withdrawn", got)
			}
		}
		if got := findingEvents(func(t testing.TB, q string) string { return krSQL(t, q) }, t, snap["rd"].id); got != snap["rd"].events {
			t.Errorf("the history of rd changed by the sync:\nbefore: %s\nafter:  %s", snap["rd"].events, got)
		}
		if f, ok := rfinding("cn"); !ok || f.Status != "needs_triage" {
			t.Errorf("cn has %+v (found %v) after the first sync, want one needs_triage finding", f, ok)
		}
		if f, ok := rfinding("cm"); !ok || f.Kind != "malicious" {
			t.Errorf("cm has %+v after the first sync, want a malicious finding", f)
		}
		for l, s := range snap {
			if l == "cw" || l == "cr" {
				continue
			}
			if f, ok := rfinding(l); !ok || f.ID != s.id {
				t.Errorf("%s has the finding %+v after the first sync, want the ID %s of the source", l, f, s.id)
			}
		}
		if n := krSQL(t, `SELECT count(*) FROM (SELECT 1 FROM trove.vuln_finding GROUP BY repo_id, ecosystem, package_name, version, advisory_id HAVING count(*) > 1) d`); n != "0" {
			t.Errorf("%s finding identities exist twice", n)
		}
		for _, r := range reps {
			for _, l := range []string{"rb", "rm", "rn", "jb", "jl", "js", "jd", "cr", "cm"} {
				refused(t, r, l)
			}
			if got := client(r, pk["rd"]).get(pk["rd"].path()); got.Status != 200 || !bytes.Equal(got.Body, bytesOf["rd"]) {
				t.Errorf("%s: the dismissed version rd answers %s, want 200 with the cached bytes", r.pod, got)
			}
		}
		var etags []string
		for _, r := range reps {
			waitFor(t, r.pod+" to list the vulnerability resource", 2*time.Minute, 2*time.Second, func() bool { return resource(r.nuget) })
			if a := audit(r.npm); a.Status != 200 {
				t.Errorf("%s: the npm audit answers %s after the first sync, want 200", r.pod, a)
			} else {
				for _, l := range []string{"rb", "cm"} {
					if !strings.Contains(string(a.Body), pk[l].name) {
						t.Errorf("%s: the audit lists no advisory for %s", r.pod, pk[l].name)
					}
				}
			}
			etags = append(etags, r.nuget.get(vulnPagePath(t, r.nuget, vulnNuGetLocal)).want(t, "the vulnerability page", 200).Header.Get("ETag"))
		}
		if len(etags) == 2 && etags[0] != etags[1] {
			t.Errorf("the vulnerability page of %s has the ETags %v on the two replicas, want one", vulnNuGetLocal, etags)
		}
	})
}

// revisionOf reads the revision of a finding.
func revisionOf(t testing.TB, q func(testing.TB, string) string, id string) int64 {
	t.Helper()
	n, err := strconv.ParseInt(q(t, `SELECT revision FROM trove.vuln_finding WHERE id = '`+id+`'`), 10, 64)
	if err != nil {
		t.Fatalf("the revision of finding %s: %v", id, err)
	}
	return n
}

// replayApplied reports whether the restore's output has a line for the subject that ends in "applied" (journal.Result.Line).
func replayApplied(logs, subject string) bool {
	for _, l := range strings.Split(logs, "\n") {
		f := strings.Fields(l)
		if len(f) > 0 && strings.Contains(l, subject) && f[len(f)-1] == "applied" {
			return true
		}
	}
	return false
}

// findingEvents is the ordered history of a finding as one comparable line per row.
func findingEvents(q func(testing.TB, string) string, t testing.TB, id string) string {
	t.Helper()
	return q(t, `SELECT coalesce(string_agg(change || ':' || coalesce(from_status, '') || '>' || coalesce(to_status, '') || '/' || coalesce(reason, '') || '/' || coalesce(comment, '') || '/' || (actor IS NOT NULL)::text, ' ; ' ORDER BY seq), '')
		FROM trove.vuln_finding_event WHERE finding_id = '`+id+`'`)
}

func describeSnapshot(snap map[string]vrstState, labels []string) string {
	var parts []string
	for _, l := range labels {
		if s, ok := snap[l]; ok {
			parts = append(parts, fmt.Sprintf("%s=%s rev %d", l, s.status, s.revision))
		}
	}
	return strings.Join(parts, ", ")
}
