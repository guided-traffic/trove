//go:build integration

package bastion

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/tlsfiles/tlstest"
	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/npmstub"
	"github.com/guided-traffic/trove/test/nugetstub"
)

func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }

const (
	// mainHost is the Host of the main endpoint (server.base_url of the stack's replicas).
	mainHost = testenv.DefaultHost
	// baseHost is the host the Bastion answers for; the main installation lists it in server.additional_base_urls.
	baseHost = "packages.example.com"
	baseURL  = "https://" + baseHost
)

// The repositories of the main installation.
const (
	npmLocal  = "npm-local"
	npmRemote = "npm-remote"
	npmAll    = "npm-all"
	ngLocal   = "nuget-local"
	ngRemote  = "nuget-remote"
	ngAll     = "nuget-all"
)

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

func tlsServerConfig(l *tlstest.Leaf) *tls.Config {
	return &tls.Config{Certificates: []tls.Certificate{l.TLS}, MinVersion: tls.VersionTLS12}
}

// who is a fixture caller: its token and, for the tokens the tests revoke, its identity at the main installation.
type who struct {
	name  string
	token string
	id    string // token ID
	pid   string // principal ID
}

// world is the main installation of the suite: the web role of the integration stack (PostgreSQL, Valkey with
// Sentinel, S3) in this process, with the npm and NuGet repositories of the specification's fixture, the
// fixture principals, a registration `partners` with its Bastion token, and the front the Bastion reaches it
// through. Everything a test changes at the main installation, it changes through the management API, as an
// administrator does, so that the audit trail and the revocation feed are the real ones.
type world struct {
	t     *testing.T
	st    *testenv.Stack
	main  *testenv.Replica
	front *front
	dir   string

	npmStub   *npmstub.Server
	nugetStub *nugetstub.Server
	nugetSrv  *httptest.Server

	admin            string // an administrator's token
	publisher        string
	full, part, none who
	bst              string // $BST, the Bastion token of `partners`
	bstID            string
	seq              int
}

// worldOptions are the settings a test changes at the main installation.
type worldOptions struct {
	// Env is further environment of the main replica.
	Env []string
	// Endpoints is server.endpoints of the main replica (JSON).
	Endpoints string
	// NoRecordDownloads leaves audit.record_downloads at its default (off).
	NoRecordDownloads bool
}

func newWorld(t *testing.T, o worldOptions) *world {
	t.Helper()
	dir := t.TempDir()
	w := &world{t: t, dir: dir}

	// The two test upstreams (counting, TLS) the remote repositories point at.
	nstub, err := npmstub.New(npmstub.Config{})
	if err != nil {
		t.Fatal(err)
	}
	nss, err := npmstub.NewSelfSigned("localhost", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	nrun, err := nstub.Start("127.0.0.1:0", nss.ServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = nrun.Close() })
	gstub, err := nugetstub.New(nugetstub.Config{})
	if err != nil {
		t.Fatal(err)
	}
	gss, err := nugetstub.NewSelfSigned("localhost", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	gsrv := httptest.NewUnstartedServer(gstub)
	gsrv.TLS = gss.ServerConfig()
	gsrv.StartTLS()
	t.Cleanup(gsrv.Close)
	w.npmStub, w.nugetStub, w.nugetSrv = nstub, gstub, gsrv
	nca, gca := filepath.Join(dir, "npm-stub-ca.pem"), filepath.Join(dir, "nuget-stub-ca.pem")
	for p, b := range map[string][]byte{nca: nss.CAPEM, gca: gss.CAPEM} {
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	env := []string{
		"TROVE_PROXY_OUTBOUND_TLS_CA_FILES=[" + strconv.Quote(nca) + "," + strconv.Quote(gca) + "]",
		`TROVE_PROXY_OUTBOUND_ADDRESS_POLICY_ALLOW_CIDRS=["127.0.0.1/32","::1/128"]`,
		"TROVE_PROXY_LOCK_POLL_INTERVAL_MS=50",
		// The Bastion's egress address and its host (R-BAS-017).
		`TROVE_SERVER_TRUSTED_PROXIES=["127.0.0.1/32"]`,
		`TROVE_SERVER_ADDITIONAL_BASE_URLS=["` + baseURL + `"]`,
	}
	if !o.NoRecordDownloads {
		env = append(env, "TROVE_AUDIT_RECORD_DOWNLOADS=true")
	}
	st := testenv.NewStack(t, testenv.StackOptions{Name: "bast", Valkey: true, Env: append(env, o.Env...)})
	w.st = st

	// The fixture of the specification's conventions.
	w.seedRepositories(nrun.URL, gsrv.URL+"/v3/index.json")
	ops := st.User("ops")
	st.Bind("admin", ops)
	w.admin = st.Token(ops, scope.Scope{Inherit: true, Admin: true})
	pub := st.ServiceAccount("publisher")
	st.Policy("publishers", []string{"read", "deploy", "delete"}, []string{npmLocal, ngLocal}, pub)
	w.publisher = st.Token(pub, scope.Default())

	mk := func(name string) who {
		p := st.ServiceAccount(name)
		return who{name: name, token: st.Token(p, scope.Default()), pid: p.ID, id: st.Text(`SELECT id::text FROM api_token WHERE principal_id = $1::uuid`, p.ID)}
	}
	w.full, w.part, w.none = mk("acme-full"), mk("acme-part"), mk("no-grant")
	all := []string{npmAll, npmLocal, npmRemote, ngAll, ngLocal, ngRemote}
	st.Policy("full-read", []string{"read"}, all, testenv.Person{ID: w.full.pid, Name: "acme-full", Kind: "service_account"})
	w.partPolicies()

	w.main = st.Start(t, testenv.ReplicaOptions{Name: "web-1", Endpoints: o.Endpoints})
	w.front = newFront(t, dir, w.main.Addr("default"))

	w.registerBastion("partners")
	return w
}

// seedRepositories creates the npm and NuGet repositories: a local, a remote over the counting upstream and
// the virtual repository over both (R-BAS conventions).
func (w *world) seedRepositories(npmURL, nugetIndex string) {
	st := w.st
	remote := func(url string, extra []any) string {
		doc := map[string]any{"remote": map[string]any{
			"url": url, "extraHosts": extra, "authMode": "none", "username": "", "password": nil,
			"clientCertificate": nil, "headers": []any{}, "forwardAuthOnRedirect": false, "outboundProxy": map[string]any{"mode": "direct"},
			"connectTimeoutMs": 5000, "readTimeoutMs": 15000, "sourceNetwork": nil, "cacheFillAction": "read",
			"cache": map[string]any{
				"storeArtifacts": true, "metadataTtlSeconds": 3600, "metadataRefreshTimeoutSeconds": 10, "notFoundTtlSeconds": 60,
				"maxStaleSeconds": 0, "unusedRetentionHours": 0,
			},
			"integrity":    map[string]any{"checksumPolicy": "verify-if-available", "blockUnexpectedContentTypes": true, "allowedContentTypes": []any{}},
			"availability": map[string]any{"mode": "online", "failureThreshold": 3, "circuitOpenSeconds": 30},
			"limits":       map[string]any{"maxArtifactBytes": nil, "maxMetadataBytes": nil, "maxConcurrentFetches": 16},
		}}
		b, err := json.Marshal(doc)
		if err != nil {
			w.t.Fatal(err)
		}
		return string(b)
	}
	st.Repository(npmLocal, "local", "npm", `{"local":{"autoClaim":true,"packageOwnership":"enforce"}}`)
	st.Repository(npmRemote, "remote", "npm", remote(npmURL+"/", []any{}))
	st.Repository(ngLocal, "local", "nuget", "{}")
	st.Repository(ngRemote, "remote", "nuget", remote(nugetIndex, []any{map[string]any{"pattern": "localhost", "sendCredentials": false}}))
	virtual := func(key, format, local, remoteKey string) {
		st.Repository(key, "virtual", format, `{}`)
		for i, m := range []string{local, remoteKey} {
			st.Exec(`INSERT INTO virtual_members (virtual_id, position, member_id)
				SELECT v.id, $2, m.id FROM repositories v, repositories m WHERE v.key = $1 AND m.key = $3`, key, i, m)
		}
		st.Exec(`INSERT INTO virtual_deploy_targets (virtual_id, target_id)
			SELECT v.id, t.id FROM repositories v, repositories t WHERE v.key = $1 AND t.key = $2`, key, local)
	}
	virtual(npmAll, "npm", npmLocal, npmRemote)
	virtual(ngAll, "nuget", ngLocal, ngRemote)
}

// partPolicies grants acme-part `read` on the virtual repositories and, on the local ones, only the packages
// `@acme/*` (npm) and `Acme.*` (NuGet): a partial reader of npm-all and nuget-all (R-BAS-012).
func (w *world) partPolicies() {
	st := w.st
	person := testenv.Person{ID: w.part.pid, Name: "acme-part", Kind: "service_account"}
	st.Policy("part-virtual", []string{"read"}, []string{npmAll, ngAll}, person)
	for _, p := range []struct{ policy, repo, pattern string }{{"part-npm", npmLocal, "@acme/*"}, {"part-nuget", ngLocal, "Acme.*"}} {
		st.Policy(p.policy, []string{"read"}, []string{p.repo}, person)
		st.Exec(`INSERT INTO authz_policy_pattern (policy_id, dimension, mode, pattern) SELECT id, 'package', 'include', $2 FROM authz_policy WHERE name = $1`, p.policy, p.pattern)
	}
}

// ---------------------------------------------------------------------------------------------------------
// The management API of the main installation.

// api sends a request to the main installation's default endpoint directly (not through the front) with a token.
func (w *world) api(method, path, token string, body any, hdr ...string) testenv.Answer {
	w.t.Helper()
	return w.st.Do(w.main, method, path, token, body, hdr...)
}

func (w *world) mustAPI(method, path string, want int, body any, hdr ...string) testenv.Answer {
	w.t.Helper()
	a := w.api(method, path, w.admin, body, hdr...)
	if a.Status != want {
		w.t.Fatalf("%s %s: %d, want %d: %s", method, path, a.Status, want, a.Body)
	}
	return a
}

// registerBastion registers a Bastion and issues its token.
func (w *world) registerBastion(name string) {
	w.t.Helper()
	w.mustAPI("POST", "/api/v1/bastions", 201, map[string]any{"name": name})
	tok := w.issueBastionToken(name)
	if name == "partners" {
		w.bst, w.bstID = tok.token, tok.id
	}
}

// issueBastionToken issues another token for the registration.
func (w *world) issueBastionToken(name string) who {
	w.t.Helper()
	a := w.mustAPI("POST", "/api/v1/bastions/"+name+"/tokens", 201, map[string]any{})
	m := a.JSON(w.t)
	tok, _ := m["token"].(string)
	id, _ := m["id"].(string)
	if tok == "" || id == "" {
		w.t.Fatalf("token answer: %s", a.Body)
	}
	return who{name: name, token: tok, id: id}
}

// newReader creates a service account with `read` on the repositories and a token (through the API, so the
// grant index and the audit trail see it as they would in production).
func (w *world) newReader(repos ...string) who {
	w.t.Helper()
	w.seq++
	name := fmt.Sprintf("reader-%d-%s", w.seq, testenv.RandHex(2))
	w.mustAPI("POST", "/api/v1/service-accounts", 201, map[string]any{"name": name})
	w.mustAPI("POST", "/api/v1/permission-policies", 201, map[string]any{
		"name": "p-" + name, "resources": map[string]any{"repositories": repos},
		"entries": []any{map[string]any{"principal": "sa:" + name, "actions": []string{"read"}}},
	})
	a := w.mustAPI("POST", "/api/v1/service-accounts/"+name+"/tokens", 201, map[string]any{"description": "it", "ttlSeconds": 3600})
	m := a.JSON(w.t)
	tok, _ := m["token"].(string)
	id, _ := m["id"].(string)
	pid := w.st.Text(`SELECT id::text FROM principal WHERE name = $1`, name)
	w.waitReads(tok, repos[0])
	return who{name: name, token: tok, id: id, pid: pid}
}

// waitReads waits until the main installation lets the token read the repository (the grant index follows a
// write through the change log).
func (w *world) waitReads(token, repo string) {
	w.t.Helper()
	path := "/api/npm/" + repo + "/plain-lib"
	if strings.HasPrefix(repo, "nuget") {
		path = "/api/nuget/" + repo + "/v3/index.json"
	}
	testenv.WaitFor(w.t, "the grant of "+repo, 20*time.Second, func() bool {
		return w.api("GET", path, token, nil).Status == 200
	})
}

// ---------------------------------------------------------------------------------------------------------
// Content.

// publishNPM publishes a package to npm-local as the publisher.
func (w *world) publishNPM(name, version string) {
	w.t.Helper()
	a := w.api("PUT", "/api/npm/"+npmLocal+"/"+strings.ReplaceAll(name, "/", "%2f"), w.publisher, npmPublishBody(w.t, name, version))
	if a.Status != 201 {
		w.t.Fatalf("npm publish %s@%s: %d %s", name, version, a.Status, a.Body)
	}
}

// publishNuGet pushes a package to nuget-local as the publisher.
func (w *world) publishNuGet(id, version string) {
	w.t.Helper()
	body, ct := nugetForm(w.t, nupkg(w.t, id, version))
	a := w.api("PUT", "/api/nuget/"+ngLocal+"/v3/package", w.publisher, body, "Content-Type", ct)
	if a.Status != 201 {
		w.t.Fatalf("nuget push %s %s: %d %s", id, version, a.Status, a.Body)
	}
}

// content is the standard content: @acme/sdk 1.0.0 and 2.0.0 and plain-lib 1.0.0 (npm), Acme.Sdk 1.0.0 and
// Other.Lib 1.0.0 (NuGet). acme-part reads the first and the fourth only.
func (w *world) content() {
	w.publishNPM("@acme/sdk", "1.0.0")
	w.publishNPM("@acme/sdk", "2.0.0")
	w.publishNPM("plain-lib", "1.0.0")
	w.publishNuGet("Acme.Sdk", "1.0.0")
	w.publishNuGet("Other.Lib", "1.0.0")
}

// anonymousPolicy lets the anonymous caller read the virtual repositories and switches anonymous access on
// or off (the runtime switch of R-AUTH-040).
func (w *world) anonymousPolicy() {
	w.t.Helper()
	w.mustAPI("POST", "/api/v1/permission-policies", 201, map[string]any{
		"name": "public", "resources": map[string]any{"repositories": []string{npmAll, npmLocal, npmRemote, ngAll, ngLocal, ngRemote}},
		"entries": []any{map[string]any{"principal": "anonymous", "actions": []string{"read"}}},
	})
}

func (w *world) setAnonymous(on bool) {
	w.t.Helper()
	cur := w.mustAPI("GET", "/api/v1/settings/auth", 200, nil)
	if got, _ := cur.JSON(w.t)["anonymousAccess"].(bool); got == on {
		return
	}
	w.mustAPI("PATCH", "/api/v1/settings/auth", 200, map[string]any{"anonymousAccess": on}, "If-Match", cur.Header.Get("ETag"), "Content-Type", "application/merge-patch+json")
	if on {
		testenv.WaitFor(w.t, "anonymous access at the main installation", 20*time.Second, func() bool {
			return w.api("GET", "/api/npm/"+npmAll+"/plain-lib", "", nil).Status == 200
		})
	} else {
		testenv.WaitFor(w.t, "anonymous access off at the main installation", 20*time.Second, func() bool {
			return w.api("GET", "/api/npm/"+npmAll+"/plain-lib", "", nil).Status == 401
		})
	}
}

// restartMain replaces the web replica by one with server.endpoints of its own (the default endpoint serving by
// exposure rules is read-only, so the content is published before). The front follows.
func (w *world) restartMain(endpoints string) {
	w.t.Helper()
	if err := w.main.Stop(); err != nil {
		w.t.Fatal(err)
	}
	w.seq++
	w.main = w.st.Start(w.t, testenv.ReplicaOptions{Name: fmt.Sprintf("web-%d", w.seq+1), Endpoints: endpoints})
	w.front.SetTarget(w.main.Addr("default"))
}
