//go:build e2e

package e2e

// The replica tests of the vulnerability scanning (R-VULN-029, E4b.8): what one replica decides, another enforces at its next
// request, with Valkey running and stopped (R-VULN-027, R-VULN-002), a version published to a local repository has its finding when
// the answer arrives (R-VULN-011), an advisory that appears late reaches a cached version and the page of the NuGet clients on every
// replica (R-VULN-011, R-VULN-018), and a claimed name is no match for public advisories (R-VULN-010).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/osvstub"
)

// maliciousRecord is a MAL- record of a package: its version 1.0.0 is malicious.
func maliciousRecord(id, name string) osvstub.Advisory {
	return osvstub.Advisory{
		SchemaVersion: osvstub.SchemaVersion, ID: id, Published: "2026-09-02T00:00:00Z",
		Summary:          "Malicious code in " + name + " (npm)",
		Details:          "Synthetic malicious-package record of an end-to-end test.",
		Affected:         []osvstub.Affected{{Package: osvstub.Package{Ecosystem: "npm", Name: name}, Versions: []string{"1.0.0"}}},
		References:       []osvstub.Reference{{Type: "WEB", URL: "https://example.org/osvstub/" + id}},
		DatabaseSpecific: map[string]any{"malicious-packages-origins": []any{}},
	}
}

// tarballPath is the path of version 1.0.0 of an unscoped package below a repository.
func tarballPath(repo, name string) string {
	return "/api/npm/" + repo + "/" + name + "/-/" + name + "-1.0.0.tgz"
}

// replicaClients are three clients of the default endpoint, each dialing one replica's pod: the administrator's (triage), the
// publisher's (downloads). Index 0 is "replica A", index 1 "replica B".
type replicaClients struct {
	admin, reader []*api
}

func (e *vulnEnv) replicaClients(t testing.TB) (replicaClients, func()) {
	t.Helper()
	addrs, stop := podForwards(t)
	var rc replicaClients
	adm := adminToken(t)
	for _, addr := range addrs {
		rc.admin = append(rc.admin, apiAtPod(t, addr, adm))
		rc.reader = append(rc.reader, apiAtPod(t, addr, e.npm.token))
	}
	return rc, stop
}

// patchRepo applies a merge patch to a repository through the client.
func patchRepo(t testing.TB, a *api, key string, patch map[string]any) {
	t.Helper()
	g := a.get("/api/v1/repositories/"+key).want(t, "read "+key, 200)
	a.do(http.MethodPatch, "/api/v1/repositories/"+key, patch, "If-Match", g.Header.Get("ETag"), "Content-Type", "application/merge-patch+json").want(t, "patch "+key, 200)
}

// wantRefused checks the refusal of R-VULN-015 on the four kinds of request that must not differ: GET, HEAD, a conditional
// request with the ETag of the cached copy and a Range request.
func wantRefused(t testing.TB, a *api, path, etag string) {
	t.Helper()
	for _, rq := range []struct {
		what, method string
		h            []string
	}{
		{"GET", http.MethodGet, nil},
		{"HEAD", http.MethodHead, nil},
		{"conditional GET", http.MethodGet, []string{"If-None-Match", etag}},
		{"Range GET", http.MethodGet, []string{"Range", "bytes=0-99"}},
	} {
		r := a.do(rq.method, path, nil, rq.h...)
		if r.Status != 403 {
			t.Fatalf("%s of a blocked version: %s, want 403", rq.what, r)
		}
		if cc := r.Header.Get("Cache-Control"); !strings.Contains(cc, "no-store") {
			t.Fatalf("%s of a blocked version: Cache-Control %q, want no-store", rq.what, cc)
		}
	}
}

// TestVuln_R_VULN_027_AcrossReplicasAndWithoutValkey is the criteria of R-VULN-027 and R-VULN-002 over three replicas: a block set
// through replica A refuses the next download through replica B (GET, HEAD, conditional, Range), and a dismissal through A lifts
// it for B at once, also with Valkey stopped; the switch `blockMalicious` is in force on B within 2 s of A's change; a sync
// triggered through the task API starts within `jobs.poll_interval` plus 1 s without Valkey.
func TestVuln_R_VULN_027_AcrossReplicasAndWithoutValkey(t *testing.T) {
	e := vulnFx(t)
	rc, stop := e.replicaClients(t)
	defer stop()
	a, b := 0, 1

	blockAndDismiss := func(t *testing.T, label string) {
		name := e.npmTri[label]
		path := tarballPath(npmRemoteKey, name)
		fill := rc.reader[b].get(path).want(t, "the first download fills the cache", 200)
		etag := fill.Header.Get("Etag")
		f := e.finding(t, npmRemoteKey, name, "1.0.0")
		e.block(t, rc.admin[a], f.ID)
		wantRefused(t, rc.reader[b], path, etag)
		wantRefused(t, rc.reader[2], tarballPath(npmAllKey, name), etag)
		// A dismisses the blocking finding; the very next download through B succeeds
		g := rc.admin[a].get("/api/v1/vulnerabilities/findings/"+f.ID).want(t, "read the finding", 200)
		rc.admin[a].do(http.MethodPatch, "/api/v1/vulnerabilities/findings/"+f.ID, map[string]any{"status": "dismissed", "dismissalReason": "not_exploitable", "comment": "end-to-end test"},
			"If-Match", g.Header.Get("ETag"), "Content-Type", "application/merge-patch+json").want(t, "dismiss the blocking finding", 200)
		rc.reader[b].get(path).want(t, "the download through B after A's dismissal", 200)
	}

	maliciousSwitch := func(t *testing.T, label string) {
		name := e.npmTri[label]
		path := tarballPath(npmRemoteKey, name)
		defer patchRepo(t, rc.admin[a], npmRemoteKey, map[string]any{"vulnerabilities": map[string]any{"blockMalicious": true}})
		patchRepo(t, rc.admin[a], npmRemoteKey, map[string]any{"vulnerabilities": map[string]any{"blockMalicious": false}})
		start := time.Now()
		var r reply
		waitFor(t, "the cache fill through B after blockMalicious: false through A", 2*time.Second, 100*time.Millisecond, func() bool {
			r = rc.reader[b].get(path)
			return r.Status == 200
		})
		t.Logf("blockMalicious: false was in force on B after %s", time.Since(start).Round(10*time.Millisecond))
		etag := r.Header.Get("Etag")
		f := e.finding(t, npmRemoteKey, name, "1.0.0")
		if f.Kind != "malicious" || f.Blocked {
			t.Fatalf("the finding of %s = %+v, want malicious and not blocking while blockMalicious is false", name, f)
		}
		start = time.Now()
		patchRepo(t, rc.admin[a], npmRemoteKey, map[string]any{"vulnerabilities": map[string]any{"blockMalicious": true}})
		waitFor(t, "the refusal through B after blockMalicious: true through A", 2*time.Second, 100*time.Millisecond, func() bool {
			return rc.reader[b].get(path).Status == 403
		})
		t.Logf("blockMalicious: true was in force on B after %s", time.Since(start).Round(10*time.Millisecond))
		wantRefused(t, rc.reader[b], path, etag)
		// the dismissal lifts it
		g := rc.admin[a].get("/api/v1/vulnerabilities/findings/"+f.ID).want(t, "read the finding", 200)
		rc.admin[a].do(http.MethodPatch, "/api/v1/vulnerabilities/findings/"+f.ID, map[string]any{"status": "dismissed", "dismissalReason": "not_exploitable", "comment": "end-to-end test"},
			"If-Match", g.Header.Get("ETag"), "Content-Type", "application/merge-patch+json").want(t, "dismiss the malicious finding", 200)
		rc.reader[b].get(path).want(t, "the download through B after A's dismissal", 200)
	}

	t.Run("R-VULN-027 a block set through A refuses B, a dismissal lifts it", func(t *testing.T) { blockAndDismiss(t, "replicas") })
	t.Run("R-VULN-002 R-VULN-015 blockMalicious through A is in force on B within 2 s", func(t *testing.T) { maliciousSwitch(t, "mal") })
	t.Run("R-VULN-027 with Valkey stopped as well", func(t *testing.T) {
		withValkeyStopped(t, func() {
			t.Run("a block and a dismissal", func(t *testing.T) { blockAndDismiss(t, "valkey") })
			t.Run("blockMalicious", func(t *testing.T) { maliciousSwitch(t, "malv") })
			t.Run("a sync starts within jobs.poll_interval plus 1 s", func(t *testing.T) {
				var took float64
				for range 5 {
					id, running := e.task(t, "vuln-sync-osv", nil)
					if running {
						waitFor(t, "the running synchronization to end", 5*time.Minute, time.Second, func() bool {
							return sql(t, `SELECT count(*) FROM trove.job WHERE kind LIKE 'vuln-sync-osv%' AND state IN ('queued', 'running', 'cancelling')`) == "0"
						})
						continue
					}
					e.waitJob(t, id, 5*time.Minute)
					v, err := strconv.ParseFloat(sql(t, `SELECT extract(epoch FROM started_at - created_at) FROM trove.job WHERE id = '`+id+`'`), 64)
					if err != nil {
						t.Fatal(err)
					}
					took = v
					break
				}
				t.Logf("the sync job started %.2f s after it was queued (jobs.poll_interval 2s)", took)
				if took > 3.0 {
					t.Errorf("the sync job started %.2f s after it was queued, want at most jobs.poll_interval (2 s) plus 1 s", took)
				}
			})
		})
	})
}

// TestVuln_R_VULN_011_PublishEvaluatesInTheCommit: publishing a version of a public package to a local repository that declares
// public packages (matchPublicAdvisories: always) answers 201 and the finding exists when the answer arrives, read through any replica.
func TestVuln_R_VULN_011_PublishEvaluatesInTheCommit(t *testing.T) {
	e := vulnFx(t)
	requireScatter(t, e.npm)
	name := e.npmTri["pub"]
	c := newNPMClient(t, e.npm, "npm11", 0)
	c.publish(name, "1.0.0")
	// no waiting: the finding is written in the commit
	seen := map[string]bool{}
	for range 6 {
		r := e.adm.get("/api/v1/vulnerabilities/findings?"+url.Values{"repository": {npmLocalKey}, "package": {name}, "version": {"1.0.0"}}.Encode()).want(t, "the findings of the published version", 200)
		seen[r.Replica()] = true
		var l struct {
			Items []vulnFinding `json:"items"`
		}
		r.JSON(t, &l)
		if len(l.Items) != 1 || l.Items[0].Advisory != e.advisoryOf[name] {
			t.Fatalf("replica %s: the published version has %+v, want one finding of %s", r.Replica(), l.Items, e.advisoryOf[name])
		}
	}
	if len(seen) < 2 {
		t.Errorf("the six reads reached %d replica(s), want the scatter profile's several", len(seen))
	}
}

// TestVuln_R_VULN_010_ClaimedNamesAreInternal: @acme/ui is claimed by npm-internal, which sets no matchPublicAdvisories: the test
// source's malicious record for it makes no finding, is listed as a claimed-name match, and the package downloads.
func TestVuln_R_VULN_010_ClaimedNamesAreInternal(t *testing.T) {
	e := vulnFx(t)
	e.seedAuditContent(t)
	if got := e.findings(t, url.Values{"repository": {vulnNPMInternal}}); len(got) != 0 {
		t.Errorf("%s has findings %+v, want none: its names are internal", vulnNPMInternal, got)
	}
	r := e.adm.get("/api/v1/vulnerabilities/claimed-name-matches").want(t, "claimed-name matches", 200)
	var l struct {
		Items []struct {
			Name     string `json:"name"`
			Advisory string `json:"advisory"`
		} `json:"items"`
	}
	r.JSON(t, &l)
	found := false
	for _, it := range l.Items {
		found = found || (it.Name == vulnAcme && it.Advisory == "MAL-2026-1235")
	}
	if !found {
		t.Errorf("the claimed-name matches %s do not list %s with MAL-2026-1235", r.Body, vulnAcme)
	}
	e.adm.withToken(e.npm.token).get("/api/npm/"+vulnNPMInternal+"/"+vulnAcme+"/-/ui-1.0.0.tgz").want(t, "the download of the claimed package", 200)
}

// TestVuln_R_VULN_011_R_VULN_018_AnAdvisoryAppearsLate: the test source publishes an advisory for a version npm-remote has cached
// and one for NuGet: after the synchronization and its evaluation the finding exists, and the page of the NuGet clients has a new
// ETag on every replica; the same once more with Valkey stopped, for the page.
func TestVuln_R_VULN_011_R_VULN_018_AnAdvisoryAppearsLate(t *testing.T) {
	e := vulnFx(t)
	addrs, stop := podForwards(t)
	defer stop()
	pageETags := func(t *testing.T) []string {
		var out []string
		for _, addr := range addrs {
			a := apiAtPod(t, addr, e.nuget.token)
			r := a.get(vulnPagePath(t, a, vulnNuGetAll)).want(t, "the vulnerability page", 200)
			out = append(out, r.Header.Get("ETag"))
		}
		return out
	}
	awaitChanged := func(t *testing.T, before []string) []string {
		var now []string
		waitFor(t, "a new ETag of the page on every replica", 30*time.Second, 500*time.Millisecond, func() bool {
			now = pageETags(t)
			for i := range now {
				if now[i] == before[i] || now[i] != now[0] {
					return false
				}
			}
			return true
		})
		return now
	}

	t.Run("R-VULN-011 a cached version gets its finding", func(t *testing.T) {
		const late = osvLate
		stub(t).publish(t, late, "2.0.0", nil)
		path := "/api/npm/" + npmRemoteKey + "/" + late + "/-/" + late + "-2.0.0.tgz"
		a := e.adm.withToken(e.npm.token)
		a.get(path).want(t, "cache "+late, 200)
		if got := e.findings(t, url.Values{"repository": {npmRemoteKey}, "package": {late}}); len(got) != 0 {
			t.Fatalf("%s has findings %+v before its advisory exists", late, got)
		}
		before := pageETags(t)
		osvStub(t).send(t, http.MethodPut, "/-/stub/toggles/late", map[string]any{"on": true})
		e.syncIncremental(t)
		f := e.finding(t, npmRemoteKey, late, "2.0.0")
		if f.Advisory != "GHSA-aaaa-aaaa-0002" || f.Severity != "high" {
			t.Errorf("the late finding = %+v, want GHSA-aaaa-aaaa-0002, high", f)
		}
		t.Run("R-VULN-018 the page of the NuGet clients has a new ETag on every replica", func(t *testing.T) {
			awaitChanged(t, before)
		})
	})
	t.Run("R-VULN-018 with Valkey stopped as well", func(t *testing.T) {
		before := pageETags(t)
		id := fmt.Sprintf("GHSA-e2e-%s-late2", e.run)
		withValkeyStopped(t, func() {
			osvStub(t).addRecord(t, "NuGet", triageRecord(id, "NuGet", "E2e.Vlate"+e.run))
			e.syncIncremental(t)
			awaitChanged(t, before)
		})
	})
}

// osvLate is the npm package of the `late` toggle of the test source.
const osvLate = osvstub.NPMLate

var _ = json.Marshal
