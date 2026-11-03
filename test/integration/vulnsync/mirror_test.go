//go:build integration

package vulnsync

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/osvstub"
)

// mirrorOptions configure the sources of mode repository.
func mirrorOptions(extra ...string) options {
	return options{Filler: 100, Env: append([]string{
		"TROVE_VULNERABILITIES_SOURCES_OSV_MODE=repository", "TROVE_VULNERABILITIES_SOURCES_OSV_REPOSITORY=" + mirrorRepo,
		"TROVE_VULNERABILITIES_SOURCES_KEV_MODE=repository", "TROVE_VULNERABILITIES_SOURCES_KEV_REPOSITORY=" + mirrorRepo,
	}, extra...)}
}

// put uploads a file to the mirror repository the way an operator does, with any client that writes to /files/.
func (e *env) put(path string, body []byte) {
	e.t.Helper()
	res := e.st.Do(e.web, "PUT", "/files/"+mirrorRepo+"/"+path, e.admin, body)
	if res.Status != 201 && res.Status != 200 {
		e.t.Fatalf("PUT %s: %d %s", path, res.Status, res.Body)
	}
}

// shrunk is the archive without its last share of entries.
func shrunk(t *testing.T, data []byte, keepPercent int) []byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	keep := len(zr.File) * keepPercent / 100
	for _, f := range zr.File[:keep] {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: f.Name, Method: zip.Deflate, Modified: f.Modified})
		if err != nil {
			t.Fatal(err)
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(w, rc); err != nil {
			t.Fatal(err)
		}
		rc.Close()
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sha(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// R-VULN-005 acceptance: with mode repository, uploading the test source's npm archive makes the next run import it, and the
// sync makes no network request; uploading the same bytes again causes no import; uploading an archive with 30 % fewer
// records fails with shrink_refused. The import's audit event names the principal that deployed the item.
func TestR_VULN_005_ARunImportsTheArchiveOfTheMirrorRepository(t *testing.T) {
	e := newEnv(t, mirrorOptions())
	// Nothing is in the repository: the run leaves every ecosystem as it is.
	j := e.mustSucceed(taskOSV, nil)
	if !strings.Contains(j.Result, "is not in the mirror repository") || e.stateOf("state", "osv", "npm") == "complete" {
		t.Errorf("a run without items: %s", j.Result)
	}

	archive, err := e.stub.Archive("npm")
	if err != nil {
		t.Fatal(err)
	}
	e.stub.ResetCounts()
	e.put("osv/npm/all.zip", archive)
	j = e.mustSucceed(taskOSV, nil)
	if got := e.stateOf("state", "osv", "npm"); got != "complete" {
		t.Fatalf("npm state %q: %s", got, j.Result)
	}
	if got := e.stateOf("state", "osv", "NuGet"); got == "complete" {
		t.Errorf("NuGet was imported without an item")
	}
	if got := e.advisories("npm"); got != int64(e.stub.Info().Ecosystems["npm"].Records) {
		t.Errorf("%d records imported, the archive has %d", got, e.stub.Info().Ecosystems["npm"].Records)
	}
	if got := e.text("SELECT encode(archive_sha256, 'hex') FROM vuln_source_state WHERE source = 'osv' AND ecosystem = 'npm'"); got != sha(archive) {
		t.Errorf("archive_sha256 %q, want %s", got, sha(archive))
	}
	if got := e.stub.Counts().Total; got != 0 {
		t.Errorf("the sync of mode repository made %d requests\n%s", got, e.requestLog())
	}
	if got := e.stateOf("full_evaluation_requested_at", "osv", "npm"); got == "" {
		t.Errorf("the first complete import did not request the full evaluation")
	}
	imported := func() int64 {
		return e.number("SELECT count(*) FROM audit_event WHERE action = 'vulnerability.source.imported' AND details->>'ecosystem' = 'npm'")
	}
	if got := imported(); got != 1 {
		t.Fatalf("%d import events, want 1", got)
	}
	if got := e.text("SELECT details->>'sha256' || ' ' || COALESCE(details->>'deployedBy', '') FROM audit_event WHERE action = 'vulnerability.source.imported' AND details->>'ecosystem' = 'npm'"); !strings.HasPrefix(got, sha(archive)+" ") || !strings.Contains(got, "rita") {
		t.Errorf("the event of the import: %q, want the digest and the deployer", got)
	}

	// The same bytes again cause no import.
	epoch := e.stateOf("data_epoch", "osv", "npm")
	e.put("osv/npm/all.zip", archive)
	j = e.mustSucceed(taskOSV, nil)
	if !strings.Contains(j.Result, `"outcome":"unchanged"`) || imported() != 1 || e.stateOf("data_epoch", "osv", "npm") != epoch {
		t.Errorf("uploading the same bytes again: %s, %d events", j.Result, imported())
	}

	// 30 % fewer records are refused, and applied with the override.
	data := e.digest("npm")
	small := shrunk(t, archive, 70)
	e.put("osv/npm/all.zip", small)
	if got := e.mustFail(taskOSV, "osv", "npm", nil); got != "shrink_refused" {
		t.Fatalf("code %q", got)
	}
	if e.digest("npm") != data {
		t.Errorf("the refused archive changed the stored data")
	}
	e.mustSucceed(taskOSV, map[string]any{"acceptShrink": true})
	if got := e.advisories("npm"); got >= int64(e.stub.Info().Ecosystems["npm"].Records) {
		t.Errorf("%d records after the shrunken archive", got)
	}
	within(t, "the second import event", 20*time.Second, func() bool { return imported() == 2 })
	if got := e.number("SELECT count(*) FROM audit_event WHERE action = 'vulnerability.source.imported' AND details->>'acceptShrink' = 'true'"); got != 1 {
		t.Errorf("%d events record the override", got)
	}
}

// R-VULN-005: the catalog of mode repository is read from kev/known_exploited_vulnerabilities.json, validated and imported once
// per digest.
func TestR_VULN_005_TheCatalogIsReadFromTheMirrorRepository(t *testing.T) {
	e := newEnv(t, mirrorOptions())
	if err := e.stub.AddKEV(osvstub.KEVEntry{CVEID: osvstub.IDAliasCVE, DateAdded: "2026-10-30"}); err != nil {
		t.Fatal(err)
	}
	resp, err := e.srv.Client().Get(e.srv.URL + kevPathName)
	if err != nil {
		t.Fatal(err)
	}
	catalog, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(catalog) == 0 {
		t.Fatalf("the catalog of the source: %d", resp.StatusCode)
	}
	e.stub.ResetCounts()
	e.put("kev/known_exploited_vulnerabilities.json", catalog)
	e.mustSucceed(taskKEV, nil)
	if got := e.number("SELECT count(*) FROM vuln_kev WHERE cve_id = $1", osvstub.IDAliasCVE); got != 1 {
		t.Errorf("the catalog was not imported")
	}
	if got := e.text("SELECT encode(archive_sha256, 'hex') FROM vuln_source_state WHERE source = 'kev'"); got != sha(catalog) {
		t.Errorf("archive_sha256 %q", got)
	}
	if e.stub.Counts().Total != 0 {
		t.Errorf("the sync of mode repository made requests\n%s", e.requestLog())
	}
	j := e.mustSucceed(taskKEV, nil)
	if !strings.Contains(j.Result, `"outcome":"unchanged"`) {
		t.Errorf("the same catalog again: %s", j.Result)
	}
	// A file that is no catalog is refused and the stored one stays.
	e.put("kev/known_exploited_vulnerabilities.json", []byte(`{"title":"no list"}`))
	if got := e.mustFail(taskKEV, "kev", "", nil); got != "upstream_error" {
		t.Errorf("code %q", got)
	}
	if got := e.number("SELECT count(*) FROM vuln_kev WHERE cve_id = $1", osvstub.IDAliasCVE); got != 1 {
		t.Errorf("a refused catalog changed the stored one")
	}
}

// R-VULN-005: the repository MUST be a generic local repository; anything else fails every run with mirror_repository_invalid.
func TestR_VULN_005_TheMirrorRepositoryMustBeAGenericLocalRepository(t *testing.T) {
	for _, tc := range []struct{ name, repo string }{
		{"a repository that does not exist", "no-such-repository"},
		{"a local repository of another format", "npm-local"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := mirrorOptions()
			o.NoWorker = true
			e := newEnv(t, o)
			e.st.Repository("npm-local", "local", "npm", "{}")
			e.worker = e.st.Start(t, testenv.ReplicaOptions{
				Name: "worker", Role: "worker",
				Env: []string{"TROVE_VULNERABILITIES_SOURCES_OSV_REPOSITORY=" + tc.repo},
			})
			if got := e.mustFail(taskOSV, "osv", "npm", nil); got != "mirror_repository_invalid" {
				t.Errorf("code %q", got)
			}
			if got := e.lastErrorCode("osv", "NuGet"); got != "mirror_repository_invalid" {
				t.Errorf("NuGet: code %q", got)
			}
			if e.stateOf("state", "osv", "npm") == "complete" {
				t.Errorf("data was imported")
			}
		})
	}
}
