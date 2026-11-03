//go:build integration

// Package vulnblock tests the download block of the vulnerability scanning (R-VULN-015, R-VULN-016) against the integration
// stack: PostgreSQL, the object store and in-process replicas started through the composition root, so the real chain, the
// dispatcher, the formats, the item store and the remote engine run. Findings are inserted as the evaluation would write them;
// the state of the advisory data and the verdict of a remote miss are named by two fakes behind the ports (app.Options.Vuln),
// because the sync and the evaluation are other packages' tests. A test starts its replicas with Valkey unreachable unless it
// asks for it, which is the state "Valkey stopped": nothing the block reads is in Valkey.
package vulnblock

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha1" //nolint:gosec // dist.shasum is a SHA-1
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/guided-traffic/trove/internal/app"
	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/npmstub"
	"github.com/guided-traffic/trove/test/npmstub/corpus"
)

func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }

// The repositories of the installation.
const (
	rNPM     = "npm-local"
	rNPMB    = "npm-local-b"
	rNuGet   = "nuget-local"
	rRemote  = "npm-remote"
	rVirtual = "npm-virtual"
	rTwin    = "npm-twin"
	rBlockMD = "npm-strict"
	// rHidden is a local repository the reader has no grant on.
	rHidden = "npm-hidden"
)

// states is the fake of the vuln.States port: the data state of an ecosystem, changed by the test.
type states struct {
	mu sync.Mutex
	m  map[string]advisory.DataState
}

func newStates() *states {
	return &states{m: map[string]advisory.DataState{"npm": {Complete: true}, "NuGet": {Complete: true}}}
}

func (s *states) State(eco string) advisory.DataState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[eco]
}

func (s *states) set(eco string, d advisory.DataState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[eco] = d
}

// env is one installation with the repositories above, a stub npm upstream and three people.
type env struct {
	t    *testing.T
	st   *testenv.Stack
	stub *npmstub.Server
	run  *npmstub.Running

	states *states
	eval   *evaluatorPort

	tokAdmin, tokReader string
	// pkg and ver are a package and version of the upstream's generated corpus: what the remote repository serves.
	pkg, ver string
}

// options are what a test changes.
type options struct {
	// Valkey: the replicas use the stack's Valkey; false is "Valkey stopped".
	Valkey bool
	// Env is further environment of every replica.
	Env []string
}

func newEnv(t *testing.T, o options) *env {
	t.Helper()
	stub, err := npmstub.New(npmstub.Config{})
	if err != nil {
		t.Fatal(err)
	}
	ss, err := npmstub.NewSelfSigned("localhost", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	run, err := stub.Start("127.0.0.1:0", ss.ServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = run.Close() })
	ca := filepath.Join(t.TempDir(), "stub-ca.pem")
	if err := os.WriteFile(ca, ss.CAPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	st := testenv.NewStack(t, testenv.StackOptions{Name: "vulnblock", Valkey: o.Valkey, Env: append([]string{
		"TROVE_PROXY_OUTBOUND_TLS_CA_FILES=[" + strconv.Quote(ca) + "]",
		`TROVE_PROXY_OUTBOUND_ADDRESS_POLICY_ALLOW_CIDRS=["127.0.0.1/32"]`,
		"TROVE_PROXY_LOCK_POLL_INTERVAL_MS=50",
	}, o.Env...)})
	e := &env{t: t, st: st, stub: stub, run: run, states: newStates(), eval: newEvaluatorPort()}
	for _, p := range corpus.Default().Packages {
		if !strings.HasPrefix(p.Name, "@") && len(p.Versions) > 0 && len(p.Versions) < 30 {
			e.pkg, e.ver = p.Name, p.Versions[0].Version
			break
		}
	}
	if e.pkg == "" {
		t.Fatal("the upstream's corpus has no plain package")
	}
	rita, reader := st.User("rita"), st.User("reader")
	st.Bind("admin", rita)
	e.tokAdmin = st.Token(rita, scope.Scope{Inherit: true, Admin: true})
	e.tokReader = st.Token(reader, scope.Default())

	st.Repository(rNPM, "local", "npm", `{}`)
	st.Repository(rNPMB, "local", "npm", `{"vulnerabilities":{"blockMalicious":false}}`)
	st.Repository(rHidden, "local", "npm", `{}`)
	st.Repository(rNuGet, "local", "nuget", `{}`)
	st.Repository(rBlockMD, "local", "npm", `{"vulnerabilities":{"onMissingData":"block"}}`)
	st.Repository(rRemote, "remote", "npm", e.remoteConfig(`{}`))
	st.Repository("npm-remote-strict", "remote", "npm", e.remoteConfig(`{"vulnerabilities":{"onMissingData":"block"}}`))
	virtual := func(key string, members ...string) {
		st.Repository(key, "virtual", "npm", `{}`)
		for i, m := range members {
			st.Exec(`INSERT INTO virtual_members (virtual_id, position, member_id)
				SELECT v.id, $2, m.id FROM repositories v, repositories m WHERE v.key = $1 AND m.key = $3`, key, i, m)
		}
	}
	virtual(rVirtual, rNPM, rRemote)
	virtual(rTwin, rNPM, rNPMB)
	st.Policy("readers", []string{"read"}, []string{rNPM, rNPMB, rNuGet, rRemote, rVirtual, rTwin, rBlockMD, "npm-remote-strict"}, reader)
	st.Policy("writers", []string{"read", "deploy", "delete"}, []string{rNPM, rNPMB, rHidden, rNuGet, rBlockMD}, rita)
	st.Policy("fillers", []string{"read"}, []string{rRemote, rVirtual, rTwin, "npm-remote-strict"}, rita)
	return e
}

// remoteConfig is the canonical document of a remote repository that points to the test upstream.
func (e *env) remoteConfig(extra string) string {
	remote := map[string]any{
		"url": e.run.URL + "/", "extraHosts": []any{}, "authMode": "none", "username": "", "password": nil,
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
	doc := map[string]any{"remote": remote}
	var more map[string]any
	if err := json.Unmarshal([]byte(extra), &more); err != nil {
		e.t.Fatal(err)
	}
	for k, v := range more {
		doc[k] = v
	}
	b, err := json.Marshal(doc)
	if err != nil {
		e.t.Fatal(err)
	}
	return string(b)
}

// replica starts a web replica with the fakes behind the scanning's ports.
func (e *env) replica(name string, env ...string) *testenv.Replica {
	e.t.Helper()
	return e.st.Start(e.t, testenv.ReplicaOptions{Name: name, Env: env, Vuln: &app.VulnOverride{States: e.states, Evaluator: e.eval}})
}

// get sends a GET (or the method given) to a path of the registry of an npm repository.
func (e *env) do(r *testenv.Replica, method, path, token string, hdr ...string) testenv.Answer {
	e.t.Helper()
	return e.st.Do(r, method, path, token, nil, hdr...)
}

func tgzPath(repo, name, version string) string {
	return "/api/npm/" + repo + "/" + name + "/-/" + name + "-" + version + ".tgz"
}

// tarball builds a package tarball.
func tarball(t testing.TB, name, version string) []byte {
	t.Helper()
	pj, _ := json.Marshal(map[string]any{"name": name, "version": version})
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, f := range [][2]string{{"package/package.json", string(pj)}, {"package/README.md", "# " + name + "\n"}} {
		if err := tw.WriteHeader(&tar.Header{Name: f[0], Mode: 0o644, Size: int64(len(f[1])), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(f[1]))
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// publish stores name@version in a local npm repository.
func (e *env) publish(r *testenv.Replica, repo, name, version string) {
	e.t.Helper()
	tgz := tarball(e.t, name, version)
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
	if a := e.st.Do(r, "PUT", "/api/npm/"+repo+"/"+name, e.tokAdmin, doc); a.Status != http.StatusCreated {
		e.t.Fatalf("publish %s@%s to %s: %d %s", name, version, repo, a.Status, a.Body)
	}
}

// nupkg builds a NuGet package.
func nupkg(t testing.TB, id, version string) []byte {
	t.Helper()
	nuspec := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://schemas.microsoft.com/packaging/2013/05/nuspec.xsd"><metadata><id>%s</id><version>%s</version><authors>Acme</authors><description>Widgets</description></metadata></package>`, id, version)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range [][2]string{{id + ".nuspec", nuspec}, {"lib/net8.0/" + id + ".dll", "MZ" + version}} {
		w, err := zw.Create(f[0])
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(f[1]))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// push stores a package in a local NuGet repository.
func (e *env) push(r *testenv.Replica, repo, id, version string) {
	e.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="package"; filename="package"`)
	h.Set("Content-Type", "application/octet-stream")
	part, err := mw.CreatePart(h)
	if err != nil {
		e.t.Fatal(err)
	}
	_, _ = part.Write(nupkg(e.t, id, version))
	_ = mw.Close()
	if a := e.st.Do(r, "PUT", "/api/nuget/"+repo+"/v3/package", e.tokAdmin, buf.Bytes(), "Content-Type", mw.FormDataContentType()); a.Status != http.StatusCreated {
		e.t.Fatalf("push %s %s to %s: %d %s", id, version, repo, a.Status, a.Body)
	}
}

func nupkgPath(repo, id, version string) string {
	id = strings.ToLower(id)
	return "/api/nuget/" + repo + "/v3/flatcontainer/" + id + "/" + version + "/" + id + "." + version + ".nupkg"
}

// finding inserts a finding of a stored version of a repository as the evaluation writes it, and returns its ID.
func (e *env) finding(repo, eco, name, version, advisoryID, kind, status string) string {
	e.t.Helper()
	return e.st.Text(`INSERT INTO vuln_finding (id, repo_id, ecosystem, package_name, version, advisory_id, package_id, version_id, kind, severity, severity_rank,
			fix_available, status, advisory_modified)
		SELECT gen_random_uuid(), r.id, $2, p.name, pv.version, $5, p.id, pv.id, $6, 'critical', 5, false, $7, now()
		  FROM repositories r JOIN packages p ON p.repo_id = r.id AND lower(p.name) = lower($3) JOIN package_versions pv ON pv.package_id = p.id AND pv.version = $4
		 WHERE r.key = $1 RETURNING id::text`, repo, eco, name, version, advisoryID, kind, status)
}

// setStatus changes a finding's status, as a triager does.
func (e *env) setStatus(id, status string) {
	e.t.Helper()
	e.st.Exec(`UPDATE vuln_finding SET status = $2, revision = revision + 1 WHERE id = $1::uuid`, id, status)
}

// npmError checks that an answer is an npm error body and returns it.
func npmError(t *testing.T, a testenv.Answer) map[string]string {
	t.Helper()
	var b map[string]string
	if err := json.Unmarshal(a.Body, &b); err != nil {
		t.Fatalf("answer %d is no npm error body: %v\n%s", a.Status, err, a.Body)
	}
	for _, k := range []string{"error", "message", "code"} {
		if b[k] == "" {
			t.Fatalf("answer %d lacks %q: %s", a.Status, k, a.Body)
		}
	}
	if ct := a.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type %q on %d", ct, a.Status)
	}
	return b
}

// problem decodes a problem document (R-API-009).
func problem(t *testing.T, a testenv.Answer) map[string]any {
	t.Helper()
	var p map[string]any
	if err := json.Unmarshal(a.Body, &p); err != nil {
		t.Fatalf("answer %d is no problem document: %v\n%s", a.Status, err, a.Body)
	}
	return p
}

// itemPath is the stored path of the first live item of a repository that ends in a suffix.
func (e *env) itemPath(repo, suffix string) string {
	e.t.Helper()
	p := e.st.Text(`SELECT COALESCE((SELECT i.path FROM items i JOIN repositories r ON r.id = i.repo_id
		WHERE r.key = $1 AND i.path LIKE '%' || $2 AND i.trashed_at IS NULL ORDER BY i.id LIMIT 1), '')`, repo, suffix)
	if p == "" {
		e.t.Fatalf("%s holds no item that ends in %s", repo, suffix)
	}
	return p
}

// events counts the audit events of a type, waiting for the buffer of the replicas to write them.
func (e *env) events(typ string) int64 {
	e.t.Helper()
	return e.st.Number(`SELECT count(*) FROM audit_event WHERE action = $1`, typ)
}

// metric reads a series of a replica's /metrics, 0 when absent.
func metric(t *testing.T, r *testenv.Replica, series string) float64 {
	t.Helper()
	if v := testenv.MetricValue(r.Metrics(t), series); v > 0 {
		return v
	}
	return 0
}

// linkRemote gives the cached tarball of a remote repository the version rows a fill records: the fill of an npm artifact does
// not create them yet (npm's OnCommit stores nothing for a fill), so the test inserts what the evaluation's trigger would find.
func (e *env) linkRemote(repo, name, version string) {
	e.t.Helper()
	e.st.Exec(`WITH r AS (SELECT id FROM repositories WHERE key = $1),
		p AS (INSERT INTO packages (repo_id, name, display_name) SELECT id, $2, $2 FROM r ON CONFLICT (repo_id, name) DO UPDATE SET name = EXCLUDED.name RETURNING id),
		v AS (INSERT INTO package_versions (package_id, version, display_version) SELECT id, $3, $3 FROM p RETURNING id)
		UPDATE items SET version_id = (SELECT id FROM v) WHERE repo_id = (SELECT id FROM r) AND path LIKE '%' || $4`, repo, name, version, name+"-"+version+".tgz")
	if n := e.st.Number(`SELECT count(*) FROM items i JOIN repositories r ON r.id = i.repo_id WHERE r.key = $1 AND i.version_id IS NOT NULL`, repo); n != 1 {
		e.t.Fatalf("%d items of %s are linked to a version, want 1", n, repo)
	}
}
