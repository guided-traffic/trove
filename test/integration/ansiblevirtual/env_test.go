//go:build integration

// Package ansiblevirtual tests the virtual class of the Ansible Galaxy format (R-ANS-022 to R-ANS-024) against the integration stack and the
// counting stub Galaxy of test/ansiblestub: a virtual repository over a local and a remote member, read and published to through the
// Galaxy API of the repository as `ansible-galaxy` sends it. The remote members reach the stub through app.Options.DialOverride.
//
// What these tests do not prove, and who does: the acceptance criteria that run `ansible-galaxy collection install` and `collection publish`
// need the client, which is the e2e set `ansible`; the same requests are sent here over HTTP and counted at the stub.
package ansiblevirtual

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	_ "github.com/guided-traffic/trove/internal/formats/all"
	"github.com/guided-traffic/trove/internal/formats/ansible/collection/collectiontest"
	"github.com/guided-traffic/trove/test/ansiblestub"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }

// The repositories of the installation.
const (
	rLocal   = "ans-local"     // the local member and the deployment target
	rGalaxy  = "galaxy"        // the remote member: the stub
	rVirtual = "ans"           // virtual: ans-local, galaxy; deployment target ans-local
	rReverse = "ans-rev"       // virtual: galaxy, ans-local; deployment target ans-local
	rNoLocal = "ans-ro"        // virtual: galaxy; no deployment target
	rLocalV  = "ans-localonly" // virtual: ans-local; deployment target ans-local
)

// venv is one installation with the repositories above, a stub, a reader, a writer and a caller who may read some members only.
type venv struct {
	t    *testing.T
	st   *testenv.Stack
	stub *ansiblestub.Server
	run  *ansiblestub.Running
	rep  *testenv.Replica

	// reader reads everything, writer reads, deploys and manages the local member and the virtual repositories, plain does the same
	// without manage (it meets the shadowing check), narrow reads the virtual repositories and the local member, not the remote one.
	reader, writer, plain, narrow string
	// pathed reads everything the others do, except that the archive of acme.tools 9.0.0 (a local version) and of the newest version the
	// stub holds of it (a remote one) are hidden by a path pattern of its grant (R-AUTHZ-005).
	pathed string
	// hiddenRemote is the version of other.stuff the pattern of pathed hides.
	hiddenRemote string
}

type options struct {
	// claim claims the pattern for ans-local (R-REPO-023).
	claim string
}

func newEnv(t *testing.T, o options) *venv {
	t.Helper()
	ss, err := ansiblestub.NewSelfSigned(ansiblestub.LoopbackHosts...)
	if err != nil {
		t.Fatal(err)
	}
	e := &venv{t: t}
	if e.stub, err = ansiblestub.New(ansiblestub.Config{}); err != nil {
		t.Fatal(err)
	}
	if e.run, err = e.stub.Start("127.0.0.1:0", ss.ServerConfig()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.run.Close() })
	ca := testenv.WriteFile(t, t.TempDir(), "stub-ca.pem", string(ss.CAPEM))
	e.st = testenv.NewStack(t, testenv.StackOptions{Name: "ansiblevirtual", Env: []string{
		"TROVE_PROXY_OUTBOUND_TLS_CA_FILES=[" + strconv.Quote(filepath.ToSlash(ca)) + "]",
		`TROVE_PROXY_OUTBOUND_ADDRESS_POLICY_ALLOW_CIDRS=["127.0.0.1/32"]`,
		"TROVE_PROXY_LOCK_POLL_INTERVAL_MS=50",
		"TROVE_VULNERABILITIES_SOURCES_OSV_MODE=off",
		"TROVE_API_RATE_LIMITS_ENABLED=false",
	}})
	host := func(h string) map[string]any { return map[string]any{"pattern": h, "sendCredentials": false} }
	e.remote(rGalaxy, e.run.APIRoot(), host(ansiblestub.DefaultGitHubHost), host(ansiblestub.DefaultCodeloadHost))
	local := `{"local":{"autoClaim":false}}`
	e.st.Repository(rLocal, "local", "ansible", local)
	for key, members := range map[string][]string{
		rVirtual: {rLocal, rGalaxy}, rReverse: {rGalaxy, rLocal}, rNoLocal: {rGalaxy}, rLocalV: {rLocal},
	} {
		e.st.Repository(key, "virtual", "ansible", "{}")
		for i, m := range members {
			e.st.Exec(`INSERT INTO virtual_members (virtual_id, position, member_id)
				SELECT v.id, $3, m.id FROM repositories v, repositories m WHERE v.key = $1 AND m.key = $2`, key, m, i)
		}
	}
	for _, key := range []string{rVirtual, rReverse, rLocalV} {
		e.st.Exec(`INSERT INTO virtual_deploy_targets (virtual_id, target_id)
			SELECT v.id, m.id FROM repositories v, repositories m WHERE v.key = $1 AND m.key = $2`, key, rLocal)
	}
	if o.claim != "" {
		e.st.Exec(`INSERT INTO namespace_claims (repo_id, pattern, source, scope) SELECT id, $1, 'manual', 'members' FROM repositories WHERE key = $2`, o.claim, rLocal)
	}
	rita, reader, writer, plain, narrow := e.st.User("rita"), e.st.User("reader"), e.st.User("writer"), e.st.User("plain"), e.st.User("narrow")
	e.st.Bind("admin", rita)
	e.reader, e.writer = e.st.Token(reader, scope.Default()), e.st.Token(writer, scope.Default())
	e.plain, e.narrow = e.st.Token(plain, scope.Default()), e.st.Token(narrow, scope.Default())
	all := []string{rLocal, rGalaxy, rVirtual, rReverse, rNoLocal, rLocalV}
	e.st.Policy("ans-readers", []string{"read"}, all, reader)
	e.st.Policy("ans-writers", []string{"read", "deploy", "delete", "manage"}, []string{rLocal, rVirtual, rReverse, rLocalV, rNoLocal}, writer)
	e.st.Policy("ans-writers-read", []string{"read"}, []string{rGalaxy}, writer)
	e.st.Policy("ans-plain", []string{"read", "deploy"}, []string{rLocal, rVirtual, rReverse, rLocalV}, plain)
	e.st.Policy("ans-plain-read", []string{"read"}, []string{rGalaxy}, plain)
	e.st.Policy("ans-narrow", []string{"read"}, []string{rVirtual, rLocal}, narrow)
	pathed := e.st.User("pathed")
	e.pathed = e.st.Token(pathed, scope.Default())
	e.hiddenRemote = e.stubVersions("acme", "tools")[0].Version
	pol := e.st.Text("INSERT INTO authz_policy (id, name) VALUES (gen_random_uuid(), 'ans-pathed') RETURNING id::text")
	for i, key := range []string{rVirtual, rLocal, rGalaxy} {
		e.st.Exec(`INSERT INTO authz_policy_selector (policy_id, ordinal, kind, repository_id)
			SELECT $1::uuid, $2, 'key', r.id FROM repositories r JOIN project p ON p.id = r.project_id WHERE p.key = $3 AND r.key = $4`, pol, i, testenv.DefaultProject, key)
	}
	e.st.Exec("INSERT INTO authz_policy_entry (policy_id, principal_id, actions) VALUES ($1::uuid, $2::uuid, $3)", pol, pathed.ID, []string{"read"})
	for _, path := range []string{"collections/acme/tools/acme-tools-9.0.0.tar.gz", "collections/acme/tools/acme-tools-" + e.hiddenRemote + ".tar.gz"} {
		e.st.Exec("INSERT INTO authz_policy_pattern (policy_id, dimension, mode, pattern) VALUES ($1::uuid, 'path', 'exclude', $2)", pol, path)
	}
	e.rep = e.st.Start(t, testenv.ReplicaOptions{Name: "web-1", DialOverride: e.run.DialOverride()})
	return e
}

// remote creates a remote repository of the format ansible that reads the stub.
func (e *venv) remote(key, upstream string, extra ...map[string]any) {
	e.t.Helper()
	hosts := make([]any, len(extra))
	for i, h := range extra {
		hosts[i] = h
	}
	doc, err := json.Marshal(map[string]any{"remote": map[string]any{
		"url": upstream, "extraHosts": hosts, "authMode": "none", "username": "", "password": nil,
		"clientCertificate": nil, "headers": []any{}, "forwardAuthOnRedirect": false, "outboundProxy": map[string]any{"mode": "direct"},
		"connectTimeoutMs": 5000, "readTimeoutMs": 15000, "sourceNetwork": nil, "cacheFillAction": "read",
		"cache": map[string]any{
			"storeArtifacts": true, "metadataTtlSeconds": 3600, "metadataRefreshTimeoutSeconds": 10, "notFoundTtlSeconds": 60,
			"maxStaleSeconds": 0, "unusedRetentionHours": 0,
		},
		"integrity":    map[string]any{"checksumPolicy": "verify-if-available", "blockUnexpectedContentTypes": true, "allowedContentTypes": []any{}},
		"availability": map[string]any{"mode": "online", "failureThreshold": 3, "circuitOpenSeconds": 30},
		"limits":       map[string]any{"maxArtifactBytes": nil, "maxMetadataBytes": nil, "maxConcurrentFetches": 16},
	}})
	if err != nil {
		e.t.Fatal(err)
	}
	e.st.Repository(key, "remote", "ansible", string(doc))
}

func apiPath(repo, path string) string {
	return "/api/ansible/" + testenv.DefaultProject + "/" + repo + "/" + path
}

// get sends a GET below the repository with the token in the form ansible-galaxy sends a configured token.
func (e *venv) get(repo, path, token string, hdr ...string) testenv.Answer {
	e.t.Helper()
	return e.st.Do(e.rep, http.MethodGet, apiPath(repo, path), "", nil, append([]string{"Authorization", "Token " + token}, hdr...)...)
}

// publish sends a collection archive to a repository as `ansible-galaxy collection publish` does: a multipart body of the parts
// sha256 and file.
func (e *venv) publish(repo, token string, spec collectiontest.Spec) (testenv.Answer, []byte) {
	e.t.Helper()
	archive := collectiontest.Build(spec)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	sum := sha256.Sum256(archive)
	if err := mw.WriteField("sha256", hex.EncodeToString(sum[:])); err != nil {
		e.t.Fatal(err)
	}
	fw, err := mw.CreateFormFile("file", spec.FileName())
	if err != nil {
		e.t.Fatal(err)
	}
	_, _ = fw.Write(archive)
	if err := mw.Close(); err != nil {
		e.t.Fatal(err)
	}
	return e.st.Do(e.rep, http.MethodPost, apiPath(repo, "v3/artifacts/collections/"), "", body.Bytes(),
		"Authorization", "Token "+token, "Content-Type", mw.FormDataContentType()), archive
}

// spec is a collection of the format the tests publish.
func spec(ns, name, version string) collectiontest.Spec {
	return collectiontest.Spec{Namespace: ns, Name: name, Version: version, Files: map[string]string{"README.md": "# " + ns + "." + name + " " + version + "\n"}}
}

type versionList struct {
	Meta  struct{ Count int }    `json:"meta"`
	Links struct{ Next *string } `json:"links"`
	Data  []struct {
		Version string `json:"version"`
		Href    string `json:"href"`
	} `json:"data"`
}

func (v versionList) versions() []string {
	out := make([]string, len(v.Data))
	for i, d := range v.Data {
		out[i] = d.Version
	}
	return out
}

func (e *venv) versions(repo, ns, name, token string, query ...string) (versionList, testenv.Answer) {
	e.t.Helper()
	path := "v3/collections/" + ns + "/" + name + "/versions/"
	if len(query) > 0 {
		path += "?" + query[0]
	}
	a := e.get(repo, path, token)
	var v versionList
	if a.Status == http.StatusOK {
		if err := json.Unmarshal(a.Body, &v); err != nil {
			e.t.Fatalf("%v: %s", err, a.Body)
		}
	}
	return v, a
}

// errorCode is the code of the first error of a Galaxy-shaped error body.
func errorCode(a testenv.Answer) string {
	var b struct {
		Errors []struct {
			Code string `json:"code"`
		} `json:"errors"`
	}
	_ = json.Unmarshal(a.Body, &b)
	if len(b.Errors) == 0 {
		return ""
	}
	return b.Errors[0].Code
}

// stubVersions are the versions the stub holds of a collection, newest first.
func (e *venv) stubVersions(ns, name string) []ansiblestub.Version {
	e.t.Helper()
	corpus, err := e.stub.Corpus()
	if err != nil {
		e.t.Fatal(err)
	}
	for _, c := range corpus.Collections {
		if c.Namespace == ns && c.Name == name {
			return c.Versions
		}
	}
	e.t.Fatalf("the stub holds no %s.%s", ns, name)
	return nil
}

func has(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func contains(a testenv.Answer, s string) bool { return strings.Contains(string(a.Body), s) }
