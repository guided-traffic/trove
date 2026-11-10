//go:build integration

// Package vulnimage is the integration tier's flow of the scanning of container images (R-VULN-033 to R-VULN-037): a push through the
// registry protocol, the job on a worker, the findings through the package API, and the block of a pull in the registry's own error shape.
// Everything runs against the integration stack except the analyser and its database, which are replaced at the seam so that the tier needs
// neither the binary nor the published database; the real binary has a test of its own (internal/vuln/imagescan).
package vulnimage

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/app"
	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/packages/view"
	"github.com/guided-traffic/trove/internal/vuln/imagescan"
	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/osvstub"
)

func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }

const (
	manifestType = "application/vnd.oci.image.manifest.v1+json"
	repoKey      = "docker-local"
	mirrorKey    = "vuln-mirror"
	cve          = "CVE-2024-3094"
	taskOff      = `{"vuln-sync-osv":{"enabled":false,"schedule":"*/15 * * * *"},"vuln-sync-osv-full":{"enabled":false,"schedule":"17 3 * * 0"},` +
		`"vuln-sync-kev":{"enabled":false,"schedule":"41 */6 * * *"},"vuln-sync-trivy-db":{"enabled":false,"schedule":"23 */6 * * *"},` +
		`"vuln-image-scan":{"enabled":false,"schedule":"*/5 * * * *"}}`
)

// fakeAnalyser is the analyser behind the seam: it counts how often the layers are read and answers one advisory for the package xz.
type fakeAnalyser struct {
	analyses, detections atomic.Int64
	// report is the JSON report the detection leaves.
	report atomic.Pointer[string]
	fail   atomic.Pointer[imagescan.ScanError]
}

func (a *fakeAnalyser) Version() string { return "0.0.0-test" }

func (a *fakeAnalyser) Analyse(_ context.Context, layout, sbom, _ string, _ imagescan.Limits) error {
	if e := a.fail.Load(); e != nil {
		return e
	}
	a.analyses.Add(1)
	if _, err := os.Stat(layout + "/index.json"); err != nil {
		return errors.New("the analyser got no OCI layout")
	}
	return os.WriteFile(sbom, []byte(`{"bomFormat":"CycloneDX","components":[{"type":"library","name":"xz"},{"type":"operating-system","name":"alpine"}]}`), 0o600)
}

func (a *fakeAnalyser) Detect(_ context.Context, _, out, _ string, db imagescan.DatabaseAccess, _ imagescan.Limits) error {
	a.detections.Add(1)
	if db.Digest == "" {
		return errors.New("the detection got no database")
	}
	return os.WriteFile(out, []byte(*a.report.Load()), 0o600)
}

// fakeDatabase is the database behind the seam; its digest changes when the test publishes a new one.
type fakeDatabase struct{ digest atomic.Pointer[string] }

func (d *fakeDatabase) Acquire(context.Context) (imagescan.DatabaseAccess, error) {
	v := d.digest.Load()
	if v == nil {
		return imagescan.DatabaseAccess{}, imagescan.ErrNoDatabase
	}
	return imagescan.DatabaseAccess{Digest: *v, Release: func() {}}, nil
}

func reportWith(fixed string) string {
	b, _ := json.Marshal(map[string]any{
		"SchemaVersion": 2,
		"Metadata":      map[string]any{"OS": map[string]any{"Family": "alpine", "Name": "3.19.1", "EOSL": false}},
		"Results": []map[string]any{{
			"Class": "os-pkgs", "Type": "alpine",
			"Vulnerabilities": []map[string]any{{
				"VulnerabilityID": cve, "PkgName": "xz", "InstalledVersion": "5.6.0-r0", "FixedVersion": fixed, "Severity": "CRITICAL", "SeveritySource": "nvd",
				"Title": "xz backdoor", "Description": "Malicious code in the upstream tarballs.", "References": []string{"https://example.org/a"},
				"CweIDs": []string{"CWE-506"}, "CVSS": map[string]any{"nvd": map[string]any{"V3Vector": "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:C/C:H/I:H/A:H", "V3Score": 10.0}},
				"PublishedDate": "2024-03-29T17:15:21Z", "LastModifiedDate": "2024-04-01T10:00:00Z",
			}},
		}},
	})
	return string(b)
}

func digestOf(b []byte) string {
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
}

func layerTar(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	body := []byte("hello")
	if err := tw.WriteHeader(&tar.Header{Name: "etc/motd", Mode: 0o644, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type image struct{ config, layer, manifest []byte }

func newImage(t *testing.T) image {
	t.Helper()
	cfg := []byte(`{"architecture":"amd64","os":"linux","rootfs":{"type":"layers","diff_ids":[]}}`)
	l := layerTar(t)
	m, err := json.Marshal(map[string]any{
		"schemaVersion": 2, "mediaType": manifestType,
		"config": map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": digestOf(cfg), "size": len(cfg)},
		"layers": []any{map[string]any{"mediaType": "application/vnd.oci.image.layer.v1.tar", "digest": digestOf(l), "size": len(l)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return image{config: cfg, layer: l, manifest: m}
}

type flow struct {
	t      *testing.T
	st     *testenv.Stack
	api    *testenv.Replica
	adm    string
	an     *fakeAnalyser
	worker *testenv.Replica
	db     *fakeDatabase
}

func v2(name string) string { return "/v2/" + testenv.DefaultProject + "/" + repoKey + "/" + name }

// options are what a test changes of the installation.
type options struct {
	// trivy is the environment of the database's source; empty for the source off.
	trivy []string
	// realDatabase leaves the database seam to the client mode, reading what the sync imported.
	realDatabase bool
	// mirror creates the generic local repository of mode repository.
	mirror bool
	// workerEnv is added to the environment of the worker.
	workerEnv []string
}

func newFlow(t *testing.T) *flow { return newFlowWith(t, options{}) }

func newFlowWith(t *testing.T, o options) *flow {
	t.Helper()
	dir := t.TempDir()
	trivy := o.trivy
	if len(trivy) == 0 {
		trivy = []string{"TROVE_VULNERABILITIES_SOURCES_TRIVY_MODE=off"}
	}
	st := testenv.NewStack(t, testenv.StackOptions{Name: "vulnimage", Env: append([]string{
		"TROVE_VULNERABILITIES_SOURCES_OSV_MODE=off", "TROVE_VULNERABILITIES_SOURCES_KEV_MODE=off",
		"TROVE_VULNERABILITIES_IMAGES_SCAN_DIR=" + dir, "TROVE_VULNERABILITIES_IMAGES_SCAN_DELAY=0s",
		"TROVE_MAINTENANCE_TASKS=" + taskOff, "TROVE_CLUSTER_EPOCH_POLL_INTERVAL=500ms", "TROVE_JOBS_POLL_INTERVAL=200ms",
	}, trivy...)})
	f := &flow{t: t, st: st, an: &fakeAnalyser{}, db: &fakeDatabase{}}
	rita := st.User("rita")
	st.Bind("admin", rita)
	f.adm = st.Token(rita, scope.Scope{Inherit: true, Admin: true})
	st.Repository(repoKey, "local", "oci", `{"vulnerabilities":{"onMissingData":"allow"}}`)
	if o.mirror {
		st.Repository(mirrorKey, "local", "generic", "{}")
	}
	f.setReport(reportWith("5.6.1-r2"))
	ov := &app.VulnOverride{ImageAnalyser: f.an}
	if !o.realDatabase {
		f.setDB("sha256:db1")
		ov.ImageDatabase = f.db
	}
	f.api = st.Start(t, testenv.ReplicaOptions{Name: "web", Role: "web"})
	f.worker = st.Start(t, testenv.ReplicaOptions{Name: "worker", Role: "worker", Vuln: ov, Env: o.workerEnv})
	return f
}

func (f *flow) setDB(d string) {
	f.db.digest.Store(&d)
	// The state row the report and the resolution rule read, written as the database's sync writes it.
	f.st.Exec(`INSERT INTO vuln_source_state (source, ecosystem, state, synced_at, full_synced_at) VALUES ('trivy', 'OCI', 'complete', now(), now())
		ON CONFLICT (source, ecosystem) DO UPDATE SET state = 'complete', synced_at = now()`)
}

func (f *flow) setReport(r string) { f.an.report.Store(&r) }

func (f *flow) do(method, path string, body []byte, hdr ...string) testenv.Answer {
	f.t.Helper()
	var b any
	if body != nil {
		b = body
	}
	return f.st.Do(f.api, method, path, f.adm, b, hdr...)
}

func (f *flow) push(name, tag string, img image) {
	f.t.Helper()
	for _, b := range [][]byte{img.config, img.layer} {
		if a := f.do("POST", v2(name)+"/blobs/uploads/?digest="+digestOf(b), b, "Content-Type", "application/octet-stream"); a.Status != http.StatusCreated {
			f.t.Fatalf("blob upload: %d %s", a.Status, a.Body)
		}
	}
	if a := f.do("PUT", v2(name)+"/manifests/"+tag, img.manifest, "Content-Type", manifestType); a.Status != http.StatusCreated {
		f.t.Fatalf("manifest push: %d %s", a.Status, a.Body)
	}
}

func (f *flow) runScan() {
	f.t.Helper()
	if a := f.st.Do(f.api, "POST", "/api/v1/system/tasks/vuln-image-scan/run", f.adm, nil); a.Status != http.StatusAccepted && a.Status != http.StatusConflict {
		f.t.Fatalf("start the scan: %d %s", a.Status, a.Body)
	}
}

type versionReport struct {
	Repositories []struct {
		Evaluation string `json:"evaluation"`
		Blocked    bool   `json:"blocked"`
		Findings   []struct {
			ID            string   `json:"id"`
			Advisory      string   `json:"advisory"`
			Version       string   `json:"version"`
			Severity      string   `json:"severity"`
			Status        string   `json:"status"`
			FixedVersions []string `json:"fixedVersions"`
			Components    []struct {
				Name, Version, FixedVersion string
				Type, Class                 string
			} `json:"components"`
		} `json:"findings"`
		Images []struct {
			Digest, State string
			OsFamily      string `json:"osFamily"`
			Packages      int    `json:"packages"`
		} `json:"images"`
	} `json:"repositories"`
}

func (f *flow) versionReport(name, tag string) versionReport {
	f.t.Helper()
	a := f.do("GET", "/api/v1/packages/"+view.ID("oci", name)+"/versions/"+tag+"/vulnerabilities", nil)
	if a.Status != http.StatusOK {
		f.t.Fatalf("version report: %d %s", a.Status, a.Body)
	}
	var r versionReport
	if err := json.Unmarshal(a.Body, &r); err != nil {
		f.t.Fatal(err)
	}
	if len(r.Repositories) != 1 {
		f.t.Fatalf("%d repositories in %s", len(r.Repositories), a.Body)
	}
	return r
}

func (f *flow) waitEvaluation(name, tag, want string) versionReport {
	f.t.Helper()
	var r versionReport
	testenv.WaitFor(f.t, "the evaluation "+want, 60*time.Second, func() bool {
		r = f.versionReport(name, tag)
		return r.Repositories[0].Evaluation == want
	})
	return r
}

func TestR_VULN_034_AnImageIsScannedAfterItsPushAndItsFindingsReachThePackageAPI(t *testing.T) {
	f := newFlow(t)
	img := newImage(t)
	f.push("app", "1.0", img)

	// The commit woke the job, or the task does: both end in the same row.
	r := f.versionReport("app", "1.0")
	if ev := r.Repositories[0].Evaluation; ev != "pending_scan" && ev != "evaluated" {
		t.Fatalf("before the scan finished the evaluation is %q; no finding must not read as clean", ev)
	}
	f.runScan()
	r = f.waitEvaluation("app", "1.0", "evaluated")
	if len(r.Repositories[0].Findings) != 1 {
		t.Fatalf("findings: %+v", r.Repositories[0].Findings)
	}
	got := r.Repositories[0].Findings[0]
	if got.Advisory != cve || got.Severity != "critical" || got.Status != "needs_triage" || got.Version != digestOf(img.manifest) {
		t.Fatalf("finding: %+v", got)
	}
	if len(got.Components) != 1 || got.Components[0].Name != "xz" || got.Components[0].Version != "5.6.0-r0" || got.Components[0].FixedVersion != "5.6.1-r2" {
		t.Fatalf("components: %+v", got.Components)
	}
	if len(got.FixedVersions) != 1 || got.FixedVersions[0] != "5.6.1-r2" {
		t.Fatalf("fixed versions: %+v", got.FixedVersions)
	}
	if len(r.Repositories[0].Images) != 1 || r.Repositories[0].Images[0].OsFamily != "alpine" || r.Repositories[0].Images[0].Packages != 1 {
		t.Fatalf("images: %+v", r.Repositories[0].Images)
	}

	// The counts per tag of GET /packages/{id}/vulnerabilities, which the repository page shows.
	a := f.do("GET", "/api/v1/packages/"+view.ID("oci", "app")+"/vulnerabilities", nil)
	if a.Status != http.StatusOK || !strings.Contains(string(a.Body), `"critical":1`) {
		t.Fatalf("package counts: %d %s", a.Status, a.Body)
	}
}

func TestR_VULN_035_ANewDatabaseIsReadWithoutTheLayersAndAFixedReportResolves(t *testing.T) {
	f := newFlow(t)
	f.push("app", "1.0", newImage(t))
	f.runScan()
	f.waitEvaluation("app", "1.0", "evaluated")
	if n := f.an.analyses.Load(); n != 1 {
		t.Fatalf("%d analyses of the first scan", n)
	}

	// A new database that no longer lists the advisory: the detection runs again, the layers are not read, and the finding resolves.
	f.setReport(`{"SchemaVersion":2,"Results":[]}`)
	f.setDB("sha256:db2")
	before := f.an.detections.Load()
	f.runScan()
	testenv.WaitFor(t, "the finding resolved", 60*time.Second, func() bool {
		return f.st.Number(`SELECT count(*) FROM vuln_finding WHERE ecosystem = 'OCI' AND status = 'resolved'`) == 1
	})
	if f.an.detections.Load() <= before {
		t.Fatal("the new database was not used")
	}
	if n := f.an.analyses.Load(); n != 1 {
		t.Fatalf("the new database read the layers again: %d analyses", n)
	}
}

func TestR_VULN_015_ABlockedImageIsRefusedInTheRegistrysShape(t *testing.T) {
	f := newFlow(t)
	img := newImage(t)
	f.push("app", "1.0", img)
	f.runScan()
	r := f.waitEvaluation("app", "1.0", "evaluated")
	id := r.Repositories[0].Findings[0].ID

	if a := f.do("GET", v2("app")+"/manifests/1.0", nil, "Accept", manifestType); a.Status != http.StatusOK {
		t.Fatalf("a pull before the block: %d %s", a.Status, a.Body)
	}
	// R-VULN-012: a block is set on a confirmed finding.
	for _, status := range []string{"confirmed", "blocked"} {
		body, _ := json.Marshal(map[string]string{"status": status})
		a := f.st.Do(f.api, "PATCH", "/api/v1/vulnerabilities/findings/"+id, f.adm, body, "If-Match", etagOf(t, f, id), "Content-Type", "application/merge-patch+json")
		if a.Status != http.StatusOK {
			t.Fatalf("set %s: %d %s", status, a.Status, a.Body)
		}
	}
	for _, ref := range []string{"1.0", digestOf(img.manifest)} {
		a := f.do("GET", v2("app")+"/manifests/"+ref, nil, "Accept", manifestType)
		if a.Status != http.StatusForbidden || !strings.Contains(string(a.Body), `"code":"DENIED"`) || !strings.Contains(string(a.Body), cve) {
			t.Fatalf("a blocked pull of %s: %d %s", ref, a.Status, a.Body)
		}
		if got := a.Header.Get("Cache-Control"); got != "no-store" {
			t.Fatalf("Cache-Control of the refusal: %q", got)
		}
	}
	// A blob of the blocked image is not an image: a layer is shared and is not refused (H-811 records it).
	if a := f.do("GET", v2("app")+"/blobs/"+digestOf(img.layer), nil); a.Status != http.StatusOK {
		t.Fatalf("a layer of the blocked image: %d", a.Status)
	}
}

// R-VULN-002, R-VULN-033: a repository of images has the member `vulnerabilities`. Switching it off resolves the images' findings through
// the rescan, and switching it on again re-detects them with their status.
func TestR_VULN_002_TheScanningOfARepositoryOfImagesIsSwitchedByItsMember(t *testing.T) {
	f := newFlow(t)
	f.push("app", "1.0", newImage(t))
	f.runScan()
	f.waitEvaluation("app", "1.0", "evaluated")
	path := "/api/v1/projects/" + testenv.DefaultProject + "/repositories/" + repoKey
	patch := func(body string) testenv.Answer {
		got := f.st.Do(f.api, "GET", path, f.adm, nil)
		if got.Status != http.StatusOK {
			t.Fatalf("read the repository: %d %s", got.Status, got.Body)
		}
		return f.st.Do(f.api, "PATCH", path, f.adm, []byte(body), "If-Match", got.Header.Get("ETag"), "Content-Type", "application/merge-patch+json")
	}
	if a := patch(`{"vulnerabilities":{"enabled":false}}`); a.Status != http.StatusOK {
		t.Fatalf("switch the scanning off: %d %s", a.Status, a.Body)
	}
	testenv.WaitFor(t, "the findings to resolve with scanning_disabled", 60*time.Second, func() bool {
		return f.st.Number(`SELECT count(*) FROM vuln_finding WHERE ecosystem = 'OCI' AND status = 'resolved' AND resolved_reason = 'scanning_disabled'`) == 1
	})
	if a := patch(`{"vulnerabilities":{"enabled":true}}`); a.Status != http.StatusOK {
		t.Fatalf("switch the scanning on: %d %s", a.Status, a.Body)
	}
	f.runScan()
	testenv.WaitFor(t, "the finding to be detected again", 60*time.Second, func() bool {
		return f.st.Number(`SELECT count(*) FROM vuln_finding WHERE ecosystem = 'OCI' AND status = 'needs_triage'`) == 1
	})
	if n := f.an.analyses.Load(); n != 1 {
		t.Fatalf("switching the scanning read the layers again: %d analyses", n)
	}
}

func TestR_VULN_034_AScanThatDoesNotFinishIsNeverReportedAsClean(t *testing.T) {
	f := newFlow(t)
	f.an.fail.Store(&imagescan.ScanError{Code: imagescan.CodeImageTooLarge, Detail: "too large", Terminal: true})
	f.push("big", "1.0", newImage(t))
	f.runScan()
	r := f.waitEvaluation("big", "1.0", "scan_failed")
	if len(r.Repositories[0].Findings) != 0 {
		t.Fatalf("findings of an image that was not scanned: %+v", r.Repositories[0].Findings)
	}
	if got := r.Repositories[0].Images[0].State; got != "failed" {
		t.Fatalf("image state %q", got)
	}
}

// dbArchive is a database archive as the publisher builds it, with a database file that is not a real one: the analyser is replaced here.
func dbArchive(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range map[string]string{
		"metadata.json": `{"Version":2,"UpdatedAt":"2026-11-08T12:51:20Z","NextUpdate":"2026-11-09T12:51:20Z"}`,
		"trivy.db":      "database bytes",
	} {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestR_VULN_035_TheDatabaseIsCopiedToTheObjectStoreAndReadFromThereByAWorker goes the whole way of the client mode: the task copies the
// published artifact through the outbound client into the object store, the state of the source becomes complete, a worker unpacks the
// copy into its local cache and the detection reads that directory.
func TestR_VULN_035_TheDatabaseIsCopiedToTheObjectStoreAndReadFromThereByAWorker(t *testing.T) {
	archive := dbArchive(t)
	layer := digestOf(archive)
	var requests atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/aquasec/trivy-db/manifests/2", func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"schemaVersion": 2, "layers": []any{map[string]any{
			"mediaType": "application/vnd.aquasec.trivy.db.layer.v1.tar+gzip", "digest": layer, "size": len(archive),
		}}})
	})
	mux.HandleFunc("/v2/aquasec/trivy-db/blobs/"+layer, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write(archive)
	})
	srv := httptest.NewUnstartedServer(mux)
	cert, err := osvstub.NewSelfSigned("localhost", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	srv.TLS = cert.ServerConfig()
	srv.StartTLS()
	t.Cleanup(srv.Close)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, cert.CAPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	f := newFlowWith(t, options{realDatabase: true, trivy: []string{
		"TROVE_VULNERABILITIES_SOURCES_TRIVY_MODE=online", "TROVE_VULNERABILITIES_SOURCES_TRIVY_URL=" + srv.URL + "/v2/aquasec/trivy-db/",
		"TROVE_PROXY_OUTBOUND_TLS_CA_FILES=[" + strconv.Quote(ca) + "]", `TROVE_PROXY_OUTBOUND_ADDRESS_POLICY_ALLOW_CIDRS=["127.0.0.1/32","::1/128"]`,
	}})
	// Before an import there is no database: an image stays pending and the state says why.
	f.push("app", "1.0", newImage(t))
	f.runScan()
	testenv.WaitFor(t, "the scan without a database to end", 30*time.Second, func() bool {
		return f.st.Number(`SELECT count(*) FROM vuln_image_scan WHERE state = 'pending'`) == 1
	})
	if n := f.an.detections.Load(); n != 0 {
		t.Fatalf("%d detections without a database", n)
	}
	if ev := f.versionReport("app", "1.0").Repositories[0].Evaluation; ev != "pending_data" && ev != "pending_scan" {
		t.Fatalf("evaluation without a database: %q", ev)
	}

	if a := f.st.Do(f.web(), "POST", "/api/v1/system/tasks/vuln-sync-trivy-db/run", f.adm, nil); a.Status != http.StatusAccepted {
		t.Fatalf("start the sync: %d %s", a.Status, a.Body)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("source state: %s", f.st.Text(`SELECT COALESCE((SELECT concat_ws(' ', state, failures, last_error::text) FROM vuln_source_state WHERE source = 'trivy'), 'no row')`))
			for _, line := range strings.Split(f.worker.Logs.String(), "\n") {
				if strings.Contains(line, "trivy") || strings.Contains(line, "advisory sync") {
					t.Log(line)
				}
			}
		}
	})
	testenv.WaitFor(t, "the database imported", 60*time.Second, func() bool {
		return f.st.Text(`SELECT COALESCE((SELECT state FROM vuln_source_state WHERE source = 'trivy' AND ecosystem = 'OCI'), 'none')`) == "complete"
	})
	if got := f.st.Text(`SELECT digest FROM vuln_image_db`); got != layer {
		t.Fatalf("database in use: %q, want %q", got, layer)
	}
	// The import woke the job, and the worker read the copy from the object store.
	r := f.waitEvaluation("app", "1.0", "evaluated")
	if len(r.Repositories[0].Findings) != 1 {
		t.Fatalf("findings: %+v", r.Repositories[0].Findings)
	}

	// A second run of the task finds the same digest and does not fetch the blob again.
	before := requests.Load()
	if a := f.st.Do(f.web(), "POST", "/api/v1/system/tasks/vuln-sync-trivy-db/run", f.adm, nil); a.Status != http.StatusAccepted && a.Status != http.StatusConflict {
		t.Fatalf("start the sync: %d %s", a.Status, a.Body)
	}
	testenv.WaitFor(t, "the second sync to end", 30*time.Second, func() bool { return requests.Load() > before })
	time.Sleep(time.Second)
	if requests.Load() != before+1 {
		t.Fatalf("the second sync made %d requests, one (the manifest) is enough", requests.Load()-before)
	}
}

// TestR_VULN_035_AnAirGappedInstallationReadsTheDatabaseFromARepository is the path of R-VULN-005 for the database: the operator uploads the
// archive to a generic local repository, the task imports it without any network request, and the same bytes are not imported twice.
func TestR_VULN_035_AnAirGappedInstallationReadsTheDatabaseFromARepository(t *testing.T) {
	f := newFlowWith(t, options{realDatabase: true, mirror: true, trivy: []string{
		"TROVE_VULNERABILITIES_SOURCES_TRIVY_MODE=repository",
		"TROVE_VULNERABILITIES_SOURCES_TRIVY_REPOSITORY=" + testenv.DefaultProject + "/" + mirrorKey,
	}})
	archive := dbArchive(t)
	if a := f.do("PUT", "/files/"+testenv.DefaultProject+"/"+mirrorKey+"/"+"trivy/db.tar.gz", archive, "Content-Type", "application/gzip"); a.Status != http.StatusCreated {
		t.Fatalf("upload the database: %d %s", a.Status, a.Body)
	}
	f.push("app", "1.0", newImage(t))
	if a := f.st.Do(f.web(), "POST", "/api/v1/system/tasks/vuln-sync-trivy-db/run", f.adm, nil); a.Status != http.StatusAccepted {
		t.Fatalf("start the sync: %d %s", a.Status, a.Body)
	}
	testenv.WaitFor(t, "the database imported from the repository", 60*time.Second, func() bool {
		return f.st.Text(`SELECT COALESCE((SELECT digest FROM vuln_image_db), '')`) == digestOf(archive)
	})
	r := f.waitEvaluation("app", "1.0", "evaluated")
	if len(r.Repositories[0].Findings) != 1 {
		t.Fatalf("findings: %+v", r.Repositories[0].Findings)
	}
	imported := f.st.Text(`SELECT imported_at::text FROM vuln_image_db`)
	if a := f.st.Do(f.web(), "POST", "/api/v1/system/tasks/vuln-sync-trivy-db/run", f.adm, nil); a.Status != http.StatusAccepted && a.Status != http.StatusConflict {
		t.Fatalf("start the sync: %d %s", a.Status, a.Body)
	}
	time.Sleep(2 * time.Second)
	if got := f.st.Text(`SELECT imported_at::text FROM vuln_image_db`); got != imported {
		t.Fatalf("the same bytes were imported again: %s, then %s", imported, got)
	}
}

func (f *flow) web() *testenv.Replica { return f.api }

func etagOf(t *testing.T, f *flow, id string) string {
	t.Helper()
	a := f.st.Do(f.api, "GET", "/api/v1/vulnerabilities/findings/"+id, f.adm, nil)
	if a.Status != http.StatusOK {
		t.Fatalf("finding %s: %d %s", id, a.Status, a.Body)
	}
	return a.Header.Get("ETag")
}

// spoolEnv sets the byte budget of the spool of a worker, which accepts no uploads and so needs no room for one.
func spoolEnv(bytes int64) []string {
	return []string{"TROVE_STORAGE_SPOOL_MAX_BYTES=" + strconv.FormatInt(bytes, 10)}
}

// Q-VULN-145: the working space of a scan is admitted through the spool's budget. A budget that cannot hold the scan's margin next to
// the scan refuses it with a reason on the row, spends no attempt, never reads the layers and never reports the image as clean.
func TestQ_VULN_145_AScanThatCannotFitTheSpoolIsPutOffWithAReasonAndNeverReadAsClean(t *testing.T) {
	f := newFlowWith(t, options{workerEnv: spoolEnv(32 << 20)})
	f.push("app", "1.0", newImage(t))
	f.runScan()
	testenv.WaitFor(t, "the scan to be put off", 60*time.Second, func() bool {
		return f.st.Number(`SELECT count(*) FROM vuln_image_scan WHERE error_code = 'spool_too_small'`) == 1
	})
	if n := f.st.Number(`SELECT attempts FROM vuln_image_scan`); n != 0 {
		t.Fatalf("the refusal spent %d attempts", n)
	}
	if got := f.st.Text(`SELECT state FROM vuln_image_scan`); got != "pending" {
		t.Fatalf("state %q", got)
	}
	if detail := f.st.Text(`SELECT error_detail FROM vuln_image_scan`); !strings.Contains(detail, "spool") || !strings.Contains(detail, "max_image_size") {
		t.Fatalf("the reason does not say what to change: %q", detail)
	}
	if f.an.analyses.Load() != 0 || f.an.detections.Load() != 0 {
		t.Fatal("the layers were read although the scan was refused")
	}
	if ev := f.versionReport("app", "1.0").Repositories[0].Evaluation; ev != "pending_scan" {
		t.Fatalf("a refused scan reads as %q", ev)
	}
}

// Q-VULN-145: a budget that holds the margin of one scan and a small image scans image after image, so the room of a finished scan is
// given back; a leak would leave the second scan waiting for room that never returns.
func TestQ_VULN_145_TheRoomOfAFinishedScanIsGivenBack(t *testing.T) {
	f := newFlowWith(t, options{workerEnv: spoolEnv(72 << 20)})
	for i, name := range []string{"one", "two", "three"} {
		img := newImage(t)
		img.layer = append(img.layer, make([]byte, 1024*(i+1))...) // another digest per image
		m, err := json.Marshal(map[string]any{
			"schemaVersion": 2, "mediaType": manifestType,
			"config": map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": digestOf(img.config), "size": len(img.config)},
			"layers": []any{map[string]any{"mediaType": "application/vnd.oci.image.layer.v1.tar", "digest": digestOf(img.layer), "size": len(img.layer)}},
		})
		if err != nil {
			t.Fatal(err)
		}
		img.manifest = m
		f.push(name, "1.0", img)
		f.runScan()
		f.waitEvaluation(name, "1.0", "evaluated")
	}
	if n := f.st.Number(`SELECT count(*) FROM vuln_image_scan WHERE error_code IS NOT NULL`); n != 0 {
		t.Fatalf("%d scans were put off or failed", n)
	}
}
