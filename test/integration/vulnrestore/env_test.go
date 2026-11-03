//go:build integration

// Package vulnrestore tests the operations of the vulnerability scanning against the integration stack: the state a restore
// leaves (R-VULN-026) and the database gauges (R-VULN-024). The backup job is phase 7 and does not exist, so the tests
// produce the state a restore leaves by SQL: the findings and their events kept (export class data), the six transient
// tables empty, which makes every ecosystem "empty" (R-VULN-026).
package vulnrestore

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha1" //nolint:gosec // dist.shasum is a SHA-1
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/backup/tables"
	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/osvstub"
)

func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }

// The repositories of the installation: npm-local holds the dismissed finding and the open malicious one, npm-blocked a
// finding that a triager blocked.
const (
	repoLocal   = "npm-local"
	repoBlocked = "npm-blocked"
)

// env is one installation whose advisory source is the test advisory source in the test process.
type env struct {
	t    *testing.T
	st   *testenv.Stack
	stub *osvstub.Stub
	run  *osvstub.Running

	admin, reader testenv.Person
	tokAdmin      string
	tokReader     string
}

// newEnv creates the installation: two npm local repositories, an administrator and a reader, the advisory source and the
// environment that points every replica at it (the OSV source narrowed to npm, the KEV source off so that nothing leaves the
// test process, the database gauges every second).
func newEnv(t *testing.T, extra ...string) *env {
	t.Helper()
	stub := osvstub.New(osvstub.Config{})
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
	st := testenv.NewStack(t, testenv.StackOptions{Name: "vulnrestore", Env: append([]string{
		"TROVE_PROXY_OUTBOUND_TLS_CA_FILES=[" + strconv.Quote(ca) + "]",
		`TROVE_PROXY_OUTBOUND_ADDRESS_POLICY_ALLOW_CIDRS=["127.0.0.1/32"]`,
		"TROVE_VULNERABILITIES_SOURCES_OSV_BASE_URL=" + run.URL + "/",
		`TROVE_VULNERABILITIES_SOURCES_OSV_ECOSYSTEMS=["npm"]`,
		"TROVE_VULNERABILITIES_SOURCES_KEV_MODE=off",
		// A worker enqueues the latest due slot of its tasks when it starts; the test decides when the sync runs.
		`TROVE_MAINTENANCE_TASKS={"vuln-sync-osv":{"enabled":false,"schedule":"*/15 * * * *"},"vuln-sync-osv-full":{"enabled":false,"schedule":"17 3 * * 0"},"vuln-sync-kev":{"enabled":false,"schedule":"41 */6 * * *"}}`,
		"TROVE_METRICS_DB_GAUGE_INTERVAL=1s",
	}, extra...)})
	e := &env{t: t, st: st, stub: stub, run: run}
	e.admin, e.reader = st.User("rita"), st.User("reader")
	st.Bind("admin", e.admin)
	e.tokAdmin = st.Token(e.admin, scope.Scope{Inherit: true, Admin: true})
	e.tokReader = st.Token(e.reader, scope.Default())
	// always: the published names are claimed (enforce), and an auto repository matches no public advisory for an internal name (R-VULN-010).
	st.Repository(repoLocal, "local", "npm", `{"local":{"packageOwnership":"enforce"},"vulnerabilities":{"matchPublicAdvisories":"always"}}`)
	st.Repository(repoBlocked, "local", "npm", `{"local":{"packageOwnership":"enforce"},"vulnerabilities":{"matchPublicAdvisories":"always"}}`)
	st.Policy("npm-readers", []string{"read"}, []string{repoLocal, repoBlocked}, e.reader)
	return e
}

// web starts a web replica; worker starts a worker replica.
func (e *env) web(name string) *testenv.Replica {
	e.t.Helper()
	return e.st.Start(e.t, testenv.ReplicaOptions{Name: name})
}

func (e *env) worker(name string) *testenv.Replica {
	e.t.Helper()
	return e.st.Start(e.t, testenv.ReplicaOptions{Name: name, Role: "worker"})
}

// publish publishes name@version to a repository through the replica and requires 201.
func (e *env) publish(r *testenv.Replica, repo, name, version string) {
	e.t.Helper()
	tgz := tarball(e.t, name, version)
	integrity, shasum := digests(tgz)
	file := fmt.Sprintf("%s-%s.tgz", name, version)
	doc := map[string]any{
		"_id": name, "name": name, "dist-tags": map[string]string{"latest": version}, "access": nil,
		"versions": map[string]any{version: map[string]any{
			"name": name, "version": version, "_id": name + "@" + version,
			"dist": map[string]any{"integrity": integrity, "shasum": shasum, "tarball": "http://x/" + name + "/-/" + file},
		}},
		"_attachments": map[string]any{file: map[string]any{
			"content_type": "application/octet-stream", "data": base64.StdEncoding.EncodeToString(tgz), "length": len(tgz),
		}},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		e.t.Fatal(err)
	}
	a := e.st.Do(r, "PUT", "/api/npm/"+repo+"/"+strings.ReplaceAll(name, "/", "%2f"), e.tokAdmin, raw)
	if a.Status != 201 {
		e.t.Fatalf("publish %s@%s to %s: %d %s", name, version, repo, a.Status, a.Body)
	}
}

// download requests the tarball of name@version as the reader and returns the answer.
func (e *env) download(r *testenv.Replica, repo, name, version string) testenv.Answer {
	e.t.Helper()
	return e.st.Do(r, "GET", fmt.Sprintf("/api/npm/%s/%s/-/%s-%s.tgz", repo, name, name, version), e.tokReader, nil)
}

func tarball(t *testing.T, name, version string) []byte {
	t.Helper()
	pj, _ := json.Marshal(map[string]any{"name": name, "version": version})
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, f := range []struct{ n, c string }{{"package.json", string(pj)}, {"README.md", "# " + name + "\n"}} {
		if err := tw.WriteHeader(&tar.Header{Name: "package/" + f.n, Mode: 0o644, Size: int64(len(f.c)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(f.c))
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func digests(tgz []byte) (integrity, shasum string) {
	s512 := sha512.Sum512(tgz)
	s1 := sha1.Sum(tgz) //nolint:gosec // see the import
	return "sha512-" + base64.StdEncoding.EncodeToString(s512[:]), hex.EncodeToString(s1[:])
}

// transientTables returns the tables of the scanning whose export class is transient and the ones of class data, from the
// registry the backup export is built from (R-BAK-004), so that the test follows the registration and not a copy of it.
func transientTables() (transient, data []string) {
	for _, tb := range tables.All() {
		if !strings.HasPrefix(tb.Name, "vuln_") {
			continue
		}
		if tb.Class == tables.Transient {
			transient = append(transient, tb.Name)
		} else {
			data = append(data, tb.Name)
		}
	}
	return transient, data
}

// recordModified returns the modified time of a fixture record, as the finding of an earlier detection holds it.
func (e *env) recordModified(ecosystem, id string) time.Time {
	e.t.Helper()
	raw, ok := e.stub.Record(ecosystem, id)
	if !ok {
		e.t.Fatalf("the test source has no record %s/%s", ecosystem, id)
	}
	var r struct {
		Modified time.Time `json:"modified"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		e.t.Fatal(err)
	}
	return r.Modified
}
