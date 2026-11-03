//go:build integration

package vulnblock

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

const (
	mal  = "trove-test-mal"
	malA = "MAL-2026-1234"
)

// refusedEverywhere checks that GET, HEAD, a conditional request with the stored ETag and a Range request of one path are the
// same refusal: 403 with no-store (R-VULN-015 "Where", R-VULN-016).
func refusedEverywhere(t *testing.T, e *env, r *testenv.Replica, path, token, etag string) testenv.Answer {
	t.Helper()
	get := e.do(r, "GET", path, token)
	if get.Status != http.StatusForbidden {
		t.Fatalf("GET %s: %d %s", path, get.Status, get.Body)
	}
	if cc := get.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("GET %s: Cache-Control = %q, want no-store", path, cc)
	}
	head := e.do(r, "HEAD", path, token)
	if head.Status != http.StatusForbidden || head.Header.Get("Cache-Control") != "no-store" || len(head.Body) != 0 {
		t.Errorf("HEAD %s: %d Cache-Control %q body %q", path, head.Status, head.Header.Get("Cache-Control"), head.Body)
	}
	hdrs := [][]string{{"Range", "bytes=0-9"}, {"Range", "bytes=0-9", "If-Range", etag}}
	if etag != "" {
		hdrs = append(hdrs, []string{"If-None-Match", etag}, []string{"If-None-Match", "*"})
	}
	for _, h := range hdrs {
		if a := e.do(r, "GET", path, token, h...); a.Status != http.StatusForbidden {
			t.Errorf("GET %s with %v: %d, want 403 (no 304, no 206 for a blocked version)", path, h, a.Status)
		}
	}
	return get
}

// R-VULN-015, R-VULN-016: a local npm tarball with an open malicious finding is refused in the npm shape for GET, HEAD,
// conditional and Range requests; another version, a dismissal and a lifted block serve again.
func TestR_VULN_015_ALocalNpmTarballWithAnOpenMaliciousFindingIsRefused(t *testing.T) {
	e := newEnv(t, options{})
	r := e.replica("web-1")
	e.publish(r, rNPM, mal, "1.0.0")
	e.publish(r, rNPM, mal, "1.0.1")
	path := tgzPath(rNPM, mal, "1.0.0")
	ok := e.do(r, "GET", path, e.tokReader)
	if ok.Status != 200 {
		t.Fatalf("before the finding: %d %s", ok.Status, ok.Body)
	}
	etag := ok.Header.Get("ETag")
	if etag == "" {
		t.Fatal("the tarball has no ETag")
	}

	id := e.finding(rNPM, "npm", mal, "1.0.0", malA, "malicious", "needs_triage")
	a := refusedEverywhere(t, e, r, path, e.tokReader, etag)
	body := npmError(t, a)
	if body["code"] != errs.CodeVulnerabilityBlocked {
		t.Errorf("code = %q", body["code"])
	}
	for _, want := range []string{"trove blocked trove-test-mal@1.0.0 in npm-local: malicious package (" + malA + ")", "/ui/vulnerabilities/findings/" + id} {
		if !strings.Contains(body["error"], want) {
			t.Errorf("error text %q lacks %q", body["error"], want)
		}
	}
	if body["error"] != body["message"] {
		t.Errorf("error and message differ: %v", body)
	}
	// R-VULN-015: the other version of the package is another version.
	if other := e.do(r, "GET", tgzPath(rNPM, mal, "1.0.1"), e.tokReader); other.Status != 200 {
		t.Errorf("1.0.1: %d %s", other.Status, other.Body)
	}
	// The package document (metadata) is not changed (H-311).
	if doc := e.do(r, "GET", "/api/npm/"+rNPM+"/"+mal, e.tokReader); doc.Status != 200 {
		t.Errorf("document: %d", doc.Status)
	}
	// A dismissal lifts the block, and the next request serves; a re-opened finding blocks again.
	e.setStatus(id, "dismissed")
	if a := e.do(r, "GET", path, e.tokReader); a.Status != 200 {
		t.Errorf("after the dismissal: %d %s", a.Status, a.Body)
	}
	e.setStatus(id, "needs_triage")
	if a := e.do(r, "GET", path, e.tokReader); a.Status != http.StatusForbidden {
		t.Errorf("after the reopening: %d", a.Status)
	}
	e.setStatus(id, "resolved")
	if a := e.do(r, "GET", path, e.tokReader); a.Status != 200 {
		t.Errorf("a resolved finding blocks: %d", a.Status)
	}
}

// R-VULN-015 "What blocks": no severity blocks by itself; a triager's block does, in that repository only, and lifting it serves
// again. blockMalicious false in the repository turns the malicious rule off.
func TestR_VULN_015_OnlyTheTriageBlockOrAMaliciousFindingBlocks(t *testing.T) {
	e := newEnv(t, options{})
	r := e.replica("web-1")
	for _, repo := range []string{rNPM, rNPMB} {
		e.publish(r, repo, "trove-test-vuln", "1.0.0")
	}
	path, pathB := tgzPath(rNPM, "trove-test-vuln", "1.0.0"), tgzPath(rNPMB, "trove-test-vuln", "1.0.0")
	id := e.finding(rNPM, "npm", "trove-test-vuln", "1.0.0", "GHSA-aaaa-aaaa-0001", "vulnerability", "needs_triage")
	for _, status := range []string{"needs_triage", "confirmed"} {
		e.setStatus(id, status)
		if a := e.do(r, "GET", path, e.tokReader); a.Status != 200 {
			t.Fatalf("a critical %s finding blocks: %d", status, a.Status)
		}
	}
	e.setStatus(id, "blocked")
	a := e.do(r, "GET", path, e.tokReader)
	if a.Status != http.StatusForbidden || !strings.Contains(npmError(t, a)["error"], "blocked in triage (GHSA-aaaa-aaaa-0001)") {
		t.Fatalf("a blocked finding: %d %s", a.Status, a.Body)
	}
	if a := e.do(r, "GET", pathB, e.tokReader); a.Status != 200 {
		t.Errorf("the same version in another repository is refused: %d", a.Status)
	}
	e.setStatus(id, "confirmed")
	if a := e.do(r, "GET", path, e.tokReader); a.Status != 200 {
		t.Errorf("blocked to confirmed did not lift the block: %d", a.Status)
	}
	// blockMalicious false: an open malicious finding does not block, a triage block does.
	mid := e.finding(rNPMB, "npm", "trove-test-vuln", "1.0.0", malA, "malicious", "needs_triage")
	if a := e.do(r, "GET", pathB, e.tokReader); a.Status != 200 {
		t.Errorf("a malicious finding blocks in a repository with blockMalicious false: %d", a.Status)
	}
	e.setStatus(mid, "blocked")
	if a := e.do(r, "GET", pathB, e.tokReader); a.Status != http.StatusForbidden {
		t.Errorf("a triage block does not block with blockMalicious false: %d", a.Status)
	}
}

// R-VULN-015 "The check runs after authorization": a version the caller may not read stays a 404, the refusal never names a
// repository the caller cannot read.
func TestR_VULN_016_TheRefusalNeverNamesARepositoryTheCallerCannotRead(t *testing.T) {
	e := newEnv(t, options{})
	r := e.replica("web-1")
	e.publish(r, rHidden, mal, "1.0.0")
	e.publish(r, rNPM, mal, "1.0.0")
	e.finding(rHidden, "npm", mal, "1.0.0", malA, "malicious", "needs_triage")
	hidden := tgzPath(rHidden, mal, "1.0.0")
	a := e.do(r, "GET", hidden, e.tokReader)
	if a.Status != http.StatusForbidden && a.Status != http.StatusNotFound || strings.Contains(string(a.Body), "vulnerability") || strings.Contains(string(a.Body), malA) {
		t.Fatalf("a repository the reader cannot read: %d %s", a.Status, a.Body)
	}
}

// R-VULN-016: NuGet gets one plain line; the .nupkg is refused for GET, HEAD, conditional and Range requests.
func TestR_VULN_016_ANuGetPackageIsRefusedWithOnePlainLine(t *testing.T) {
	e := newEnv(t, options{})
	r := e.replica("web-1")
	e.push(r, rNuGet, "Contoso.Json.Codecs", "1.0.0")
	e.push(r, rNuGet, "Contoso.Json.Codecs", "1.0.1")
	path := nupkgPath(rNuGet, "Contoso.Json.Codecs", "1.0.0")
	ok := e.do(r, "GET", path, e.tokReader)
	if ok.Status != 200 {
		t.Fatalf("before the finding: %d %s", ok.Status, ok.Body)
	}
	id := e.finding(rNuGet, "NuGet", "Contoso.Json.Codecs", "1.0.0", "MAL-2026-1301", "malicious", "needs_triage")
	a := refusedEverywhere(t, e, r, path, e.tokReader, ok.Header.Get("ETag"))
	if ct := a.Header.Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	line := string(a.Body)
	if strings.ContainsAny(line, "\r\n") || !strings.HasPrefix(line, "vulnerability_blocked: trove blocked contoso.json.codecs@1.0.0 in nuget-local: malicious package (MAL-2026-1301). Details: ") ||
		!strings.Contains(line, "/ui/vulnerabilities/findings/"+id) {
		t.Errorf("body %q", line)
	}
	if other := e.do(r, "GET", nupkgPath(rNuGet, "Contoso.Json.Codecs", "1.0.1"), e.tokReader); other.Status != 200 {
		t.Errorf("1.0.1: %d", other.Status)
	}
	e.setStatus(id, "dismissed")
	if a := e.do(r, "GET", path, e.tokReader); a.Status != 200 {
		t.Errorf("after the dismissal: %d", a.Status)
	}
}

// R-VULN-016: /files/ answers problem details with the code, the advisory and the finding.
func TestR_VULN_016_TheRawViewAnswersProblemDetailsWithTheAdvisoryAndTheFinding(t *testing.T) {
	e := newEnv(t, options{})
	r := e.replica("web-1")
	e.publish(r, rNPM, mal, "1.0.0")
	path := "/files/" + rNPM + "/" + e.itemPath(rNPM, ".tgz")
	ok := e.do(r, "GET", path, e.tokAdmin)
	if ok.Status != 200 {
		t.Fatalf("before the finding: %d %s", ok.Status, ok.Body)
	}
	id := e.finding(rNPM, "npm", mal, "1.0.0", malA, "malicious", "needs_triage")
	a := refusedEverywhere(t, e, r, path, e.tokAdmin, ok.Header.Get("ETag"))
	if ct := a.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Errorf("Content-Type = %q", ct)
	}
	p := problem(t, a)
	if p["code"] != errs.CodeVulnerabilityBlocked || p["advisory"] != malA || p["findingId"] != id || p["status"] != float64(403) {
		t.Errorf("problem = %v", p)
	}
	if detail, _ := p["detail"].(string); !strings.Contains(detail, "trove blocked trove-test-mal@1.0.0 in npm-local") {
		t.Errorf("detail = %q", detail)
	}
}

// R-VULN-015 "Not evaluated yet": with onMissingData block a version is refused while the ecosystem's data is not complete,
// with the default it is served; stale data is not missing.
func TestR_VULN_015_OnMissingDataBlockRefusesWhileTheDataIsNotComplete(t *testing.T) {
	e := newEnv(t, options{})
	r := e.replica("web-1")
	e.publish(r, rBlockMD, mal, "1.0.0")
	e.publish(r, rNPM, mal, "1.0.0")
	e.states.set("npm", advisory.DataState{})
	a := e.do(r, "GET", tgzPath(rBlockMD, mal, "1.0.0"), e.tokReader)
	if a.Status != http.StatusForbidden {
		t.Fatalf("onMissingData block: %d %s", a.Status, a.Body)
	}
	if body := npmError(t, a); body["code"] != errs.CodeVulnerabilityDataMissing || !strings.Contains(body["error"], "trove blocked trove-test-mal@1.0.0 in npm-strict") {
		t.Errorf("body = %v", body)
	}
	if a.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", a.Header.Get("Cache-Control"))
	}
	if a := e.do(r, "GET", tgzPath(rNPM, mal, "1.0.0"), e.tokReader); a.Status != 200 {
		t.Errorf("the default allows: %d", a.Status)
	}
	e.states.set("npm", advisory.DataState{Complete: true, Stale: true})
	if a := e.do(r, "GET", tgzPath(rBlockMD, mal, "1.0.0"), e.tokReader); a.Status != 200 {
		t.Errorf("stale data counts as missing: %d", a.Status)
	}
}

// R-VULN-015 "Report mode": nothing is refused, the would-be block is counted with mode report and recorded with enforced false.
func TestR_VULN_015_TheModeReportRefusesNothingAndCountsTheWouldBeBlock(t *testing.T) {
	e := newEnv(t, options{Env: []string{"TROVE_VULNERABILITIES_ENFORCEMENT=report"}})
	r := e.replica("web-1")
	e.publish(r, rNPM, mal, "1.0.0")
	e.finding(rNPM, "npm", mal, "1.0.0", malA, "malicious", "needs_triage")
	series := `trove_vuln_downloads_blocked_total{format="npm",mode="report",reason="malicious"}`
	before := metric(t, r, series)
	for i := range 3 {
		if a := e.do(r, "GET", tgzPath(rNPM, mal, "1.0.0"), e.tokReader); a.Status != 200 {
			t.Fatalf("request %d: %d %s", i, a.Status, a.Body)
		}
	}
	if got := metric(t, r, series); got != before+3 {
		t.Errorf("%s rose from %v to %v, want 3 more", series, before, got)
	}
	testenv.WaitFor(t, "the audit event", 15*time.Second, func() bool { return e.events(audit.EventVulnDownloadBlocked) >= 1 })
	if n := e.st.Number(`SELECT count(*) FROM audit_event WHERE action = $1 AND details->>'enforced' = 'false' AND details->>'reason' = 'malicious'`, audit.EventVulnDownloadBlocked); n != 1 {
		t.Errorf("%d report-mode events for three requests, want one (deduplicated)", n)
	}
}

// R-VULN-016: the audit event vulnerability.download_blocked is recorded at most once a minute per repository, version and
// client address, and the metric counts every refusal.
func TestR_VULN_016_TheAuditEventIsRecordedOncePerMinuteAndTheMetricCountsEveryRefusal(t *testing.T) {
	e := newEnv(t, options{})
	r := e.replica("web-1")
	e.publish(r, rNPM, mal, "1.0.0")
	e.finding(rNPM, "npm", mal, "1.0.0", malA, "malicious", "needs_triage")
	path := tgzPath(rNPM, mal, "1.0.0")
	for range 5 {
		if a := e.do(r, "GET", path, e.tokReader); a.Status != http.StatusForbidden {
			t.Fatalf("%d", a.Status)
		}
	}
	series := `trove_vuln_downloads_blocked_total{format="npm",mode="enforce",reason="malicious"}`
	if got := metric(t, r, series); got != 5 {
		t.Errorf("%s = %v, want 5", series, got)
	}
	testenv.WaitFor(t, "the audit event", 15*time.Second, func() bool { return e.events(audit.EventVulnDownloadBlocked) >= 1 })
	time.Sleep(2 * time.Second)
	if n := e.events(audit.EventVulnDownloadBlocked); n != 1 {
		t.Fatalf("%d events for five refusals of one client, want 1", n)
	}
	var pkg, version, reason string
	var enforced, stored bool
	e.st.Query(`SELECT details->>'package', details->>'version', details->>'reason', (details->>'enforced')::boolean, (details->>'stored')::boolean
		FROM audit_event WHERE action = $1`, func(rows pgx.Rows) error {
		return rows.Scan(&pkg, &version, &reason, &enforced, &stored)
	}, audit.EventVulnDownloadBlocked)
	if pkg != mal || version != "1.0.0" || reason != "malicious" || !enforced || !stored {
		t.Errorf("event = %s %s %s enforced %t stored %t", pkg, version, reason, enforced, stored)
	}
}

// The virtual tiers (R-VULN-015 "Virtual repositories"): the member that stores the version decides with its own policy, and a
// block ends resolution: the next member that holds the same version is not asked.
func TestR_VULN_015_ABlockInAVirtualRepositoryEndsResolution(t *testing.T) {
	e := newEnv(t, options{})
	r := e.replica("web-1")
	e.publish(r, rNPM, mal, "1.0.0")
	e.publish(r, rNPMB, mal, "1.0.0")
	path := tgzPath(rTwin, mal, "1.0.0")
	if a := e.do(r, "GET", path, e.tokReader); a.Status != 200 {
		t.Fatalf("before the finding: %d %s", a.Status, a.Body)
	}
	e.finding(rNPM, "npm", mal, "1.0.0", malA, "malicious", "needs_triage")
	a := refusedEverywhere(t, e, r, path, e.tokReader, "")
	if body := npmError(t, a); body["code"] != errs.CodeVulnerabilityBlocked || !strings.Contains(body["error"], " in "+rTwin+": ") || strings.Contains(body["error"], rNPM) {
		t.Errorf("body = %v: the text names the addressed repository and no member", body)
	}
	// The second member serves the version on its own address: nothing blocks it there.
	if a := e.do(r, "GET", tgzPath(rNPMB, mal, "1.0.0"), e.tokReader); a.Status != 200 {
		t.Errorf("the second member: %d", a.Status)
	}
}

// The remote hit, the cache view, the raw view and the virtual tier of a cached artifact; the cross-replica dismissal. The
// artifact is cached before the finding exists (the record arrived after the fill): R-VULN-015 acceptance criterion 2.
func TestR_VULN_015_ACachedRemoteArtifactIsRefusedOnEveryRouteAndTheDismissalReachesTheOtherReplica(t *testing.T) {
	e := newEnv(t, options{Valkey: true})
	r1, r2 := e.replica("web-1"), e.replica("web-2")
	if a := e.do(r1, "GET", "/api/npm/"+rRemote+"/"+e.pkg, e.tokReader); a.Status != 200 {
		t.Fatalf("document: %d %s", a.Status, a.Body)
	}
	path := tgzPath(rRemote, e.pkg, e.ver)
	filled := e.do(r1, "GET", path, e.tokReader)
	if filled.Status != 200 {
		t.Fatalf("fill: %d %s", filled.Status, filled.Body)
	}
	if got := e.stub.Counts().Tarballs(e.pkg); got != 1 {
		t.Fatalf("%d upstream tarball requests for the fill", got)
	}
	etag := filled.Header.Get("ETag")
	e.requireLinked(rRemote, e.pkg, e.ver)
	id := e.finding(rRemote, "npm", e.pkg, e.ver, malA, "malicious", "needs_triage")
	if id == "" {
		t.Fatal("the fill recorded no version to attach a finding to")
	}
	for name, p := range map[string]string{
		"the remote hit":      path,
		"the cache view":      "/api/npm/" + rRemote + "~cache/" + e.pkg + "/-/" + e.pkg + "-" + e.ver + ".tgz",
		"the raw view":        "/files/" + rRemote + "/" + e.itemPath(rRemote, ".tgz"),
		"the virtual tier":    tgzPath(rVirtual, e.pkg, e.ver),
		"the second replica":  path,
		"the virtual, second": tgzPath(rVirtual, e.pkg, e.ver),
	} {
		rep, tok := r1, e.tokReader
		if strings.HasPrefix(name, "the second") {
			rep = r2
		}
		if strings.HasPrefix(p, "/files/") {
			tok = e.tokAdmin
		}
		t.Run(name, func(t *testing.T) { refusedEverywhere(t, e, rep, p, tok, etag) })
	}
	if got := e.stub.Counts().Tarballs(e.pkg); got != 1 {
		t.Errorf("%d upstream tarball requests after the refusals, want the one of the fill", got)
	}
	// A dismissal is read by the next request of any replica.
	e.setStatus(id, "dismissed")
	for _, rep := range []*testenv.Replica{r2, r1} {
		if a := e.do(rep, "GET", path, e.tokReader); a.Status != 200 {
			t.Errorf("%s after the dismissal: %d %s", rep.Name, a.Status, a.Body)
		}
	}
	if a := e.do(r2, "GET", tgzPath(rVirtual, e.pkg, e.ver), e.tokReader); a.Status != 200 {
		t.Errorf("virtual after the dismissal: %d", a.Status)
	}
}

// R-VULN-015 "Remote miss": a version the evaluation blocks is not fetched, stored or served, and the upstream is not asked for
// its tarball; the refusal names the advisory page when no finding exists. The miss through a virtual repository is refused too.
func TestR_VULN_015_ABlockedRemoteMissIsNeitherFetchedNorStored(t *testing.T) {
	e := newEnv(t, options{})
	r := e.replica("web-1")
	e.eval.block(rRemote, e.pkg, e.ver, vuln.Block{Reason: vuln.BlockMalicious, AdvisoryID: malA})
	if a := e.do(r, "GET", "/api/npm/"+rRemote+"/"+e.pkg, e.tokReader); a.Status != 200 {
		t.Fatalf("document: %d %s", a.Status, a.Body)
	}
	for _, p := range []string{tgzPath(rRemote, e.pkg, e.ver), tgzPath(rVirtual, e.pkg, e.ver)} {
		for _, method := range []string{"GET", "HEAD"} {
			a := e.do(r, method, p, e.tokReader)
			if a.Status != http.StatusForbidden || a.Header.Get("Cache-Control") != "no-store" {
				t.Fatalf("%s %s: %d %s", method, p, a.Status, a.Body)
			}
			if method == "GET" {
				body := npmError(t, a)
				if body["code"] != errs.CodeVulnerabilityBlocked || !strings.Contains(body["error"], "malicious package ("+malA+")") ||
					!strings.Contains(body["error"], "/ui/vulnerabilities/advisories/npm/"+malA) {
					t.Errorf("body = %v", body)
				}
			}
		}
	}
	if got := e.stub.Counts().Tarballs(e.pkg); got != 0 {
		t.Errorf("the upstream saw %d tarball requests for a blocked version", got)
	}
	if n := e.st.Number(`SELECT count(*) FROM items i JOIN repositories r ON r.id = i.repo_id WHERE r.key = $1 AND i.path LIKE '%.tgz'`, rRemote); n != 0 {
		t.Errorf("%d tarballs stored for a blocked version", n)
	}
	if e.eval.asked() == 0 {
		t.Error("the evaluation was not asked")
	}
	// A version the evaluation does not block is fetched as before: the gate is per version.
	e.eval.block(rRemote, e.pkg, e.ver, vuln.Block{})
	if a := e.do(r, "GET", tgzPath(rRemote, e.pkg, e.ver), e.tokReader); a.Status != 200 {
		t.Errorf("after the block was lifted: %d %s", a.Status, a.Body)
	}
}

// R-VULN-015 "Not evaluated yet": a remote miss with onMissingData block and incomplete data is refused before the fetch with
// vulnerability_data_missing; with the default it succeeds.
func TestR_VULN_015_ARemoteMissWithMissingDataIsRefusedBeforeTheFetchWhenTheRepositoryBlocks(t *testing.T) {
	e := newEnv(t, options{})
	r := e.replica("web-1")
	e.states.set("npm", advisory.DataState{})
	for _, repo := range []string{"npm-remote-strict", rRemote} {
		if a := e.do(r, "GET", "/api/npm/"+repo+"/"+e.pkg, e.tokReader); a.Status != 200 {
			t.Fatalf("document of %s: %d %s", repo, a.Status, a.Body)
		}
	}
	a := e.do(r, "GET", tgzPath("npm-remote-strict", e.pkg, e.ver), e.tokReader)
	if a.Status != http.StatusForbidden || npmError(t, a)["code"] != errs.CodeVulnerabilityDataMissing {
		t.Fatalf("strict: %d %s", a.Status, a.Body)
	}
	if got := e.stub.Counts().Tarballs(e.pkg); got != 0 {
		t.Errorf("the upstream saw %d tarball requests for a refused miss", got)
	}
	if a := e.do(r, "GET", tgzPath(rRemote, e.pkg, e.ver), e.tokReader); a.Status != 200 {
		t.Errorf("the default: %d %s", a.Status, a.Body)
	}
}

// R-VULN-015 "Not evaluated yet", R-VULN-001, Q-VULN-75 (C6): onMissingData block applies only to versions in scope. A remote
// whose upstream is not public never has a version in scope, so its miss and its stored download are both served before the
// first import; a remote that matches public advisories refuses both with vulnerability_data_missing.
func TestR_VULN_015_MissingDataAppliesOnlyToVersionsInScope(t *testing.T) {
	e := newEnv(t, options{})
	r := e.replica("web-1")
	e.states.set("npm", advisory.DataState{})
	for _, repo := range []string{"npm-remote-strict", rPrivate} {
		if a := e.do(r, "GET", "/api/npm/"+repo+"/"+e.pkg, e.tokReader); a.Status != 200 {
			t.Fatalf("document of %s: %d %s", repo, a.Status, a.Body)
		}
	}
	// Not public: the miss is fetched and stored, the stored item is served.
	path := tgzPath(rPrivate, e.pkg, e.ver)
	for _, what := range []string{"the miss", "the stored download", "the stored download again"} {
		if a := e.do(r, "GET", path, e.tokReader); a.Status != 200 {
			t.Fatalf("%s of the not public remote: %d %s", what, a.Status, a.Body)
		}
	}
	if got := e.stub.Counts().Tarballs(e.pkg); got != 1 {
		t.Errorf("%d upstream tarball requests for the not public remote, want the one of the miss", got)
	}
	e.requireLinked(rPrivate, e.pkg, e.ver)
	// Matched: the miss is refused before the fetch, and so is a stored download once the data is incomplete again.
	strict := tgzPath("npm-remote-strict", e.pkg, e.ver)
	refused := func(what string) {
		t.Helper()
		a := e.do(r, "GET", strict, e.tokReader)
		if a.Status != http.StatusForbidden || npmError(t, a)["code"] != errs.CodeVulnerabilityDataMissing {
			t.Fatalf("%s of the matched remote: %d %s", what, a.Status, a.Body)
		}
	}
	refused("the miss")
	if got := e.stub.Counts().Tarballs(e.pkg); got != 1 {
		t.Errorf("%d upstream tarball requests after a refused miss, want the one of the other remote", got)
	}
	e.states.set("npm", advisory.DataState{Complete: true})
	if a := e.do(r, "GET", strict, e.tokReader); a.Status != 200 {
		t.Fatalf("the matched remote with complete data: %d %s", a.Status, a.Body)
	}
	e.states.set("npm", advisory.DataState{})
	refused("the stored download")
	if a := e.do(r, "GET", path, e.tokReader); a.Status != 200 {
		t.Errorf("the not public remote after the data went incomplete again: %d %s", a.Status, a.Body)
	}
}

// R-VULN-015: the installation switch off blocks nothing, whatever the findings say.
func TestR_VULN_015_TheInstallationSwitchOffBlocksNothing(t *testing.T) {
	e := newEnv(t, options{Env: []string{"TROVE_VULNERABILITIES_ENABLED=false"}})
	r := e.replica("web-1")
	e.publish(r, rNPM, mal, "1.0.0")
	e.finding(rNPM, "npm", mal, "1.0.0", malA, "malicious", "blocked")
	if a := e.do(r, "GET", tgzPath(rNPM, mal, "1.0.0"), e.tokReader); a.Status != 200 {
		t.Errorf("vulnerabilities.enabled false: %d", a.Status)
	}
}
