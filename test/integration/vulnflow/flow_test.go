//go:build integration

// Package vulnflow is the integration tier's end-to-end flow of the vulnerability scanning: it crosses every package of the
// scanning with no fake behind any port. The test advisory source (test/osvstub) is imported by vuln-sync-osv, started through
// the task API; the vuln-evaluate job writes the findings of a version published to a local repository and of a version cached
// through an npm remote; the findings API lists them; a triage PATCH confirms and blocks one; the download is refused in npm's
// shape on another replica and served again after the block is lifted; a malicious version is refused on a remote miss before
// any upstream tarball request; and npm's bulk audit endpoint names the advisory from trove's own data (exit criteria E4b.3
// and E4b.4 of the roadmap, R-VULN-004, R-VULN-009 to R-VULN-013, R-VULN-015 to R-VULN-017).
package vulnflow

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha1" //nolint:gosec // dist.shasum is a SHA-1
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/npmstub"
	"github.com/guided-traffic/trove/test/osvstub"
)

func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }

const (
	repoLocal  = "npm-local"
	repoHook   = "npm-hook"
	repoRemote = "npm-remote"
	repoAll    = "npm-all"

	pkgVuln = osvstub.NPMVuln
	pkgMal  = osvstub.NPMMal
	version = "1.0.0"

	bulk = "-/npm/v1/security/advisories/bulk"

	// taskOff keeps the scheduler from running the sync by itself: the test starts it through the task API.
	taskOff = `{"vuln-sync-osv":{"enabled":false,"schedule":"*/15 * * * *"},"vuln-sync-osv-full":{"enabled":false,"schedule":"17 3 * * 0"},` +
		`"vuln-sync-kev":{"enabled":false,"schedule":"41 */6 * * *"}}`
)

// flow is the installation of the test.
type flow struct {
	t    *testing.T
	st   *testenv.Stack
	osv  *osvstub.Stub
	npm  *npmstub.Server
	adm  string
	read string
	web  [2]*testenv.Replica
}

func newFlow(t *testing.T) *flow {
	t.Helper()
	dir := t.TempDir()

	osv := osvstub.New(osvstub.Config{})
	osvCert, err := osvstub.NewSelfSigned("localhost", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	osvSrv := httptest.NewUnstartedServer(osv)
	osvSrv.TLS = osvCert.ServerConfig()
	osvSrv.StartTLS()
	t.Cleanup(osvSrv.Close)

	up, err := npmstub.New(npmstub.Config{})
	if err != nil {
		t.Fatal(err)
	}
	upCert, err := npmstub.NewSelfSigned("localhost", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	run, err := up.Start("127.0.0.1:0", upCert.ServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = run.Close() })
	for _, name := range []string{pkgVuln, pkgMal} {
		if err := up.Publish(name, version, nil, "latest"); err != nil {
			t.Fatal(err)
		}
	}

	var cas []string
	for name, pem := range map[string][]byte{"osv-ca.pem": osvCert.CAPEM, "npm-ca.pem": upCert.CAPEM} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, pem, 0o600); err != nil {
			t.Fatal(err)
		}
		cas = append(cas, strconv.Quote(p))
	}

	st := testenv.NewStack(t, testenv.StackOptions{Name: "vulnflow", Env: []string{
		"TROVE_PROXY_OUTBOUND_TLS_CA_FILES=[" + strings.Join(cas, ",") + "]",
		`TROVE_PROXY_OUTBOUND_ADDRESS_POLICY_ALLOW_CIDRS=["127.0.0.1/32","::1/128"]`,
		"TROVE_VULNERABILITIES_SOURCES_OSV_BASE_URL=" + osvSrv.URL + "/",
		`TROVE_VULNERABILITIES_SOURCES_OSV_ECOSYSTEMS=["npm"]`,
		"TROVE_VULNERABILITIES_SOURCES_KEV_MODE=off",
		// The test upstream stands for the public registry: a remote repository is matched with auto when its host is listed (R-VULN-010).
		`TROVE_VULNERABILITIES_MATCHING_PUBLIC_UPSTREAMS={"npm":["127.0.0.1"]}`,
		"TROVE_MAINTENANCE_TASKS=" + taskOff,
		"TROVE_CLUSTER_EPOCH_POLL_INTERVAL=500ms", "TROVE_JOBS_POLL_INTERVAL=200ms",
		"TROVE_PROXY_LOCK_POLL_INTERVAL_MS=50",
	}})
	f := &flow{t: t, st: st, osv: osv, npm: up}
	rita, reader := st.User("rita"), st.User("reader")
	st.Bind("admin", rita)
	f.adm = st.Token(rita, scope.Scope{Inherit: true, Admin: true})
	f.read = st.Token(reader, scope.Default())

	// always: the published names are claimed, and an auto repository matches no public advisory for an internal name (R-VULN-010).
	always := `{"vulnerabilities":{"matchPublicAdvisories":"always"}}`
	st.Repository(repoLocal, "local", "npm", always)
	st.Repository(repoHook, "local", "npm", always)
	st.Repository(repoRemote, "remote", "npm", remoteDoc(run.URL))
	st.Repository(repoAll, "virtual", "npm", `{}`)
	for i, m := range []string{repoLocal, repoRemote} {
		st.Exec(`INSERT INTO virtual_members (virtual_id, position, member_id) SELECT v.id, $2, m.id FROM repositories v, repositories m WHERE v.key = $1 AND m.key = $3`,
			repoAll, i, m)
	}
	st.Policy("readers", []string{"read"}, []string{repoLocal, repoHook, repoRemote, repoAll}, reader)
	st.Policy("writers", []string{"read", "deploy", "delete"}, []string{repoLocal, repoHook}, rita)

	f.web = [2]*testenv.Replica{
		st.Start(t, testenv.ReplicaOptions{Name: "web-1", Role: "web"}),
		st.Start(t, testenv.ReplicaOptions{Name: "web-2", Role: "web"}),
	}
	return f
}

// remoteDoc is the document of a remote repository that points to the test upstream.
func remoteDoc(url string) string {
	return `{"remote":{"url":"` + url + `/","extraHosts":[],"authMode":"none","username":"","password":null,` +
		`"clientCertificate":null,"headers":[],"forwardAuthOnRedirect":false,"outboundProxy":{"mode":"direct"},` +
		`"connectTimeoutMs":5000,"readTimeoutMs":15000,"sourceNetwork":null,"cacheFillAction":"read",` +
		`"cache":{"storeArtifacts":true,"metadataTtlSeconds":3600,"metadataRefreshTimeoutSeconds":10,"notFoundTtlSeconds":60,` +
		`"maxStaleSeconds":0,"unusedRetentionHours":0},` +
		`"integrity":{"checksumPolicy":"verify-if-available","blockUnexpectedContentTypes":true,"allowedContentTypes":[]},` +
		`"availability":{"mode":"online","failureThreshold":3,"circuitOpenSeconds":30},` +
		`"limits":{"maxArtifactBytes":null,"maxMetadataBytes":null,"maxConcurrentFetches":16}}}`
}

// publish stores name@version in a local npm repository.
func (f *flow) publish(r *testenv.Replica, repo, name string) {
	f.t.Helper()
	pj, _ := json.Marshal(map[string]any{"name": name, "version": version})
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, e := range [][2]string{{"package/package.json", string(pj)}, {"package/README.md", "# " + name + "\n"}} {
		if err := tw.WriteHeader(&tar.Header{Name: e[0], Mode: 0o644, Size: int64(len(e[1])), Typeflag: tar.TypeReg}); err != nil {
			f.t.Fatal(err)
		}
		_, _ = tw.Write([]byte(e[1]))
	}
	if err := tw.Close(); err != nil {
		f.t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		f.t.Fatal(err)
	}
	tgz := buf.Bytes()
	s512, s1 := sha512.Sum512(tgz), sha1.Sum(tgz) //nolint:gosec // see the import
	file := name + "-" + version + ".tgz"
	doc := map[string]any{
		"_id": name, "name": name, "dist-tags": map[string]string{"latest": version},
		"versions": map[string]any{version: map[string]any{
			"name": name, "version": version, "_id": name + "@" + version,
			"dist": map[string]any{
				"integrity": "sha512-" + base64.StdEncoding.EncodeToString(s512[:]), "shasum": hex.EncodeToString(s1[:]),
				"tarball": "http://x/" + name + "/-/" + file,
			},
		}},
		"access": nil,
		"_attachments": map[string]any{file: map[string]any{
			"content_type": "application/octet-stream", "data": base64.StdEncoding.EncodeToString(tgz), "length": len(tgz),
		}},
	}
	if a := f.st.Do(r, "PUT", "/api/npm/it/"+repo+"/"+name, f.adm, doc); a.Status != http.StatusCreated {
		f.t.Fatalf("publish %s@%s to %s: %d %s", name, version, repo, a.Status, a.Body)
	}
}

func tgzPath(repo, name string) string {
	return "/api/npm/it/" + repo + "/" + name + "/-/" + name + "-" + version + ".tgz"
}

// finding is one row of the findings list.
type finding struct {
	ID          string  `json:"id"`
	Repository  string  `json:"repository"`
	Package     string  `json:"package"`
	Version     string  `json:"version"`
	Advisory    string  `json:"advisory"`
	Status      string  `json:"status"`
	Kind        string  `json:"kind"`
	Blocked     bool    `json:"blocked"`
	BlockReason *string `json:"blockReason"`
}

// findings lists every finding of the installation through the API of a replica.
func (f *flow) findings(r *testenv.Replica) []finding {
	f.t.Helper()
	a := f.st.Do(r, "GET", "/api/v1/vulnerabilities/findings?limit=200", f.adm, nil)
	if a.Status != http.StatusOK {
		f.t.Fatalf("list findings: %d %s", a.Status, a.Body)
	}
	var p struct {
		Items []finding `json:"items"`
	}
	if err := json.Unmarshal(a.Body, &p); err != nil {
		f.t.Fatal(err)
	}
	return p.Items
}

// find returns the finding of a package in a repository.
func (f *flow) find(r *testenv.Replica, repo, name string) (finding, bool) {
	f.t.Helper()
	for _, x := range f.findings(r) {
		if x.Repository == repo && x.Package == name && x.Version == version {
			return x, true
		}
	}
	return finding{}, false
}

// patch changes the status of a finding with the ETag of its current state, as a triager's client does.
func (f *flow) patch(r *testenv.Replica, id, body string) testenv.Answer {
	f.t.Helper()
	path := "/api/v1/vulnerabilities/findings/" + id
	cur := f.st.Do(r, "GET", path, f.adm, nil)
	if cur.Status != http.StatusOK || cur.Header.Get("ETag") == "" {
		f.t.Fatalf("read finding %s: %d %s", id, cur.Status, cur.Body)
	}
	return f.st.Do(r, "PATCH", path, f.adm, []byte(body), "Content-Type", "application/merge-patch+json", "If-Match", cur.Header.Get("ETag"))
}

// npmRefusal asserts that an answer is the 403 of a blocked version in npm's error shape and returns its body.
func npmRefusal(t *testing.T, a testenv.Answer, code string) map[string]string {
	t.Helper()
	if a.Status != http.StatusForbidden {
		t.Fatalf("answer %d, want 403: %s", a.Status, a.Body)
	}
	var b map[string]string
	if err := json.Unmarshal(a.Body, &b); err != nil {
		t.Fatalf("not an npm error body: %v\n%s", err, a.Body)
	}
	if b["error"] == "" || b["error"] != b["message"] || b["code"] != code {
		t.Fatalf("npm error body %v, want error = message and code %s", b, code)
	}
	if ct := a.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type %q", ct)
	}
	return b
}

// TestR_VULN_015_E4b3_E4b4_TheFlowFromTheAdvisorySourceToTheBlockedDownload is the flow of the phase: R-VULN-004 (the sync through
// the task API), R-VULN-009 to R-VULN-011 (the evaluation in the job and in the publish commit), R-VULN-013 (the triage),
// R-VULN-015 and R-VULN-016 (the block, the refusal in npm's shape on another replica, the remote miss without an upstream
// request) and R-VULN-017 (npm's bulk audit from trove's data).
func TestR_VULN_015_E4b3_E4b4_TheFlowFromTheAdvisorySourceToTheBlockedDownload(t *testing.T) {
	f := newFlow(t)
	web1, web2 := f.web[0], f.web[1]
	f.st.Start(t, testenv.ReplicaOptions{Name: "worker", Role: "worker"})

	// Before any advisory data exists: a version published to a local repository and one cached through the remote.
	f.publish(web1, repoLocal, pkgVuln)
	if a := f.st.Do(web1, "GET", "/api/npm/it/"+repoRemote+"/"+pkgVuln, f.read, nil); a.Status != http.StatusOK {
		t.Fatalf("remote document: %d %s", a.Status, a.Body)
	}
	if a := f.st.Do(web1, "GET", tgzPath(repoRemote, pkgVuln), f.read, nil); a.Status != http.StatusOK {
		t.Fatalf("remote fill: %d %s", a.Status, a.Body)
	}
	if n := f.st.Number(`SELECT count(*) FROM vuln_finding`); n != 0 {
		t.Fatalf("%d findings before the advisory data exists", n)
	}

	t.Run("R-VULN-004 the sync through the task API imports the test advisory source", func(t *testing.T) {
		a := f.st.Do(web1, "POST", "/api/v1/system/tasks/vuln-sync-osv/run", f.adm, nil)
		if a.Status != http.StatusAccepted {
			t.Fatalf("run vuln-sync-osv: %d %s", a.Status, a.Body)
		}
		testenv.WaitFor(t, "the first import of npm", 120*time.Second, func() bool {
			return f.st.Text(`SELECT COALESCE((SELECT state FROM vuln_source_state WHERE source = 'osv' AND ecosystem = 'npm'), 'none')`) == "complete"
		})
		if n := f.st.Number(`SELECT count(*) FROM vuln_advisory WHERE ecosystem = 'npm' AND id IN ($1, $2)`, osvstub.IDVuln, osvstub.IDMal); n != 2 {
			t.Fatalf("%d of the 2 test advisories imported", n)
		}
	})

	var local, cached finding
	t.Run("R-VULN-011 the vuln-evaluate job writes the findings of the stored versions", func(t *testing.T) {
		testenv.WaitFor(t, "the full evaluation", 120*time.Second, func() bool {
			return f.st.Number(`SELECT count(*) FROM vuln_source_state WHERE source = 'osv' AND ecosystem = 'npm' AND full_evaluation_requested_at IS NULL`) == 1 &&
				f.st.Number(`SELECT count(*) FROM job WHERE kind = 'vuln-evaluate' AND state IN ('queued', 'running')`) == 0 &&
				f.st.Number(`SELECT count(*) FROM job WHERE kind = 'vuln-evaluate' AND state = 'succeeded'`) >= 1
		})
		var ok bool
		// The findings API of another replica than the one that ran the task.
		if local, ok = f.find(web2, repoLocal, pkgVuln); !ok {
			t.Fatalf("no finding on %s@%s in %s: %+v", pkgVuln, version, repoLocal, f.findings(web2))
		}
		if cached, ok = f.find(web2, repoRemote, pkgVuln); !ok {
			t.Fatalf("no finding on %s@%s cached in %s: %+v", pkgVuln, version, repoRemote, f.findings(web2))
		}
		for _, x := range []finding{local, cached} {
			if x.Advisory != osvstub.IDVuln || x.Kind != "vulnerability" || x.Status != "needs_triage" || x.Blocked {
				t.Errorf("finding %+v, want %s, vulnerability, needs_triage, not blocked", x, osvstub.IDVuln)
			}
		}
		if n := len(f.findings(web2)); n != 2 {
			t.Errorf("%d findings, want the 2 of the vulnerable version", n)
		}
		// Findings alone block nothing (R-VULN-015).
		for _, repo := range []string{repoLocal, repoRemote} {
			if a := f.st.Do(web2, "GET", tgzPath(repo, pkgVuln), f.read, nil); a.Status != http.StatusOK {
				t.Errorf("%s with an open finding: %d %s", repo, a.Status, a.Body)
			}
		}
	})

	t.Run("R-VULN-011 a publish after the import is evaluated in its own commit", func(t *testing.T) {
		f.publish(web1, repoHook, pkgVuln)
		testenv.WaitFor(t, "the finding of the publish commit", 30*time.Second, func() bool {
			_, ok := f.find(web2, repoHook, pkgVuln)
			return ok
		})
	})

	t.Run("R-VULN-013 a triage PATCH confirms the finding and sets the block", func(t *testing.T) {
		if a := f.patch(web1, local.ID, `{"status":"confirmed","comment":"reachable from our service"}`); a.Status != http.StatusOK {
			t.Fatalf("confirm: %d %s", a.Status, a.Body)
		}
		if a := f.st.Do(web2, "GET", tgzPath(repoLocal, pkgVuln), f.read, nil); a.Status != http.StatusOK {
			t.Fatalf("a confirmed finding blocks: %d", a.Status)
		}
		a := f.patch(web1, local.ID, `{"status":"blocked","comment":"block until 1.0.1"}`)
		if a.Status != http.StatusOK || !strings.Contains(string(a.Body), `"blocked":true`) || !strings.Contains(string(a.Body), `"blockReason":"triage"`) {
			t.Fatalf("block: %d %s", a.Status, a.Body)
		}
		x, _ := f.find(web2, repoLocal, pkgVuln)
		if x.Status != "blocked" || !x.Blocked || x.BlockReason == nil || *x.BlockReason != "triage" {
			t.Errorf("the list on the other replica: %+v", x)
		}
	})

	path := tgzPath(repoLocal, pkgVuln)
	t.Run("R-VULN-016 the other replica refuses the download in npm's shape, and only in the blocked repository", func(t *testing.T) {
		body := npmRefusal(t, f.st.Do(web2, "GET", path, f.read, nil), errs.CodeVulnerabilityBlocked)
		for _, want := range []string{pkgVuln + "@" + version, repoLocal, osvstub.IDVuln} {
			if !strings.Contains(body["error"], want) {
				t.Errorf("the refusal %q does not name %s", body["error"], want)
			}
		}
		npmRefusal(t, f.st.Do(web1, "GET", tgzPath(repoAll, pkgVuln), f.read, nil), errs.CodeVulnerabilityBlocked)
		// The same version in the other repositories is another finding with another status (R-VULN-015).
		for _, repo := range []string{repoRemote, repoHook} {
			if a := f.st.Do(web2, "GET", tgzPath(repo, pkgVuln), f.read, nil); a.Status != http.StatusOK {
				t.Errorf("%s answers %d, want 200: the block is the finding's of %s only", repo, a.Status, repoLocal)
			}
		}
		testenv.WaitFor(t, "the audit events of the triage and of the refusal", 30*time.Second, func() bool {
			return f.st.Number(`SELECT count(*) FROM audit_event WHERE action = 'vulnerability.finding.status_changed'`) >= 2 &&
				f.st.Number(`SELECT count(*) FROM audit_event WHERE action = 'vulnerability.download_blocked'`) >= 1
		})
	})

	t.Run("R-VULN-013 lifting the block serves the download again on both replicas", func(t *testing.T) {
		if a := f.patch(web2, local.ID, `{"status":"confirmed","comment":"fixed in the consumer"}`); a.Status != http.StatusOK {
			t.Fatalf("lift: %d %s", a.Status, a.Body)
		}
		for _, r := range f.web {
			if a := f.st.Do(r, "GET", path, f.read, nil); a.Status != http.StatusOK {
				t.Errorf("%s after the lift: %d %s", r.Name, a.Status, a.Body)
			}
		}
	})

	t.Run("R-VULN-015 a malicious version is refused on a remote miss before any upstream tarball request", func(t *testing.T) {
		before := f.npm.Counts().Tarballs(pkgMal)
		body := npmRefusal(t, f.st.Do(web2, "GET", tgzPath(repoRemote, pkgMal), f.read, nil), errs.CodeVulnerabilityBlocked)
		if !strings.Contains(body["error"], pkgMal+"@"+version) || !strings.Contains(body["error"], osvstub.IDMal) {
			t.Errorf("the refusal %q does not name the version and the advisory", body["error"])
		}
		npmRefusal(t, f.st.Do(web1, "GET", tgzPath(repoAll, pkgMal), f.read, nil), errs.CodeVulnerabilityBlocked)
		if got := f.npm.Counts().Tarballs(pkgMal); got != before {
			t.Errorf("%d upstream tarball requests for the malicious version, want none", got-before)
		}
		if n := f.st.Number(`SELECT count(*) FROM items i JOIN repositories r ON r.id = i.repo_id WHERE r.key = $1 AND i.path LIKE '%' || $2 || '%'`, repoRemote, pkgMal); n != 0 {
			t.Errorf("the refused version was stored (%d items)", n)
		}
		if n := f.st.Number(`SELECT count(*) FROM vuln_finding WHERE package_name = $1`, pkgMal); n != 0 {
			t.Errorf("a miss wrote %d findings", n)
		}
	})

	t.Run("R-VULN-017 npm's bulk audit endpoint names the advisory from trove's data without an upstream request", func(t *testing.T) {
		asked := f.npm.Counts().Total()
		for _, repo := range []string{repoLocal, repoRemote, repoAll} {
			a := f.st.Do(web2, "POST", "/api/npm/it/"+repo+"/"+bulk, f.read, []byte(`{"`+pkgVuln+`":["1.0.0","1.0.1"],"`+pkgMal+`":["1.0.0"]}`))
			if a.Status != http.StatusOK {
				t.Fatalf("%s: audit %d %s", repo, a.Status, a.Body)
			}
			var got map[string][]struct {
				ID                 int64  `json:"id"`
				URL                string `json:"url"`
				Severity           string `json:"severity"`
				VulnerableVersions string `json:"vulnerable_versions"`
			}
			if err := json.Unmarshal(a.Body, &got); err != nil {
				t.Fatalf("%s: %v\n%s", repo, err, a.Body)
			}
			vulns := got[pkgVuln]
			if len(vulns) != 1 || vulns[0].URL != advisory.URLOf(osvstub.IDVuln, nil) || vulns[0].Severity != "high" ||
				vulns[0].ID != advisory.NPMAuditID(osvstub.IDVuln, 0) {
				t.Errorf("%s: advisories of %s = %+v, want %s (high)", repo, pkgVuln, vulns, osvstub.IDVuln)
			}
			if mal := got[pkgMal]; len(mal) != 1 || mal[0].ID != advisory.NPMAuditID(osvstub.IDMal, 0) {
				t.Errorf("%s: advisories of %s = %+v, want %s", repo, pkgMal, mal, osvstub.IDMal)
			}
		}
		if got := f.npm.Counts().Total(); got != asked {
			t.Errorf("the audits made %d upstream requests, want none", got-asked)
		}
	})
}
