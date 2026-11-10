//go:build integration

// Package ansibleremote tests the remote class of the Ansible Galaxy format against the integration stack and the counting stub Galaxy
// of test/ansiblestub (R-ANS-016 to R-ANS-018, R-ANS-020, R-ANS-032): the API root and its prefixes, the version list stored in pages
// and cut for the client, the documents rewritten onto the repository, the archive verified against the digest of its version
// document and checked against its path, the first digest that stays, the roles by translation from the GitHub stand-in, and the
// credentials of the routes. The stub runs in the test process on TLS with five host names (the Galaxy, the GitHub stand-in, the host
// it redirects to, a host a role version must not be fetched from and the realm of a hub); the replicas reach the names through
// app.Options.DialOverride, so nothing resolves a name, and the egress rules apply to the loopback address the stub listens on,
// which the installation admits as an operator would.
//
// What these tests do not prove, and who does: that the real ansible-galaxy client accepts what is served (`collection install`,
// `role install` with the ansible-core versions of the matrix), which is the e2e set `ansible`; the same requests are sent here over
// HTTP and counted at the stub.
package ansibleremote

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	_ "github.com/guided-traffic/trove/internal/formats/all"
	"github.com/guided-traffic/trove/test/ansiblestub"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }

const project = testenv.DefaultProject

// scratchRepos is how many repositories newEnv makes for the cases that need an empty cache.
const scratchRepos = 14

// The repositories of the installation besides the scratch ones.
const (
	rRequire = "ans-require" // checksumPolicy require
	rNoRoles = "ans-noroles" // extraHosts without the GitHub stand-in
	// rNotGalaxy has an upstream URL that is no API root: it answers a collection document.
	rNotGalaxy = "ans-notgalaxy"
)

// aenv is one installation with the repositories above, a stub and two principals.
type aenv struct {
	t      *testing.T
	st     *testenv.Stack
	stub   *ansiblestub.Server
	run    *ansiblestub.Running
	ss     *ansiblestub.SelfSigned
	dial   map[string]string
	admin  string
	reader string
	mu     sync.Mutex
	used   int
}

type options struct {
	// valkey starts the replicas with the stack's Valkey, so that a fetch is one flight across replicas (R-REPO-025).
	valkey bool
	stub   ansiblestub.Config
	env    []string
}

func newEnv(t *testing.T, o options) *aenv {
	t.Helper()
	ss, err := ansiblestub.NewSelfSigned(ansiblestub.LoopbackHosts...)
	if err != nil {
		t.Fatal(err)
	}
	e := &aenv{t: t, ss: ss}
	if e.stub, err = ansiblestub.New(o.stub); err != nil {
		t.Fatal(err)
	}
	if e.run, err = e.stub.Start("127.0.0.1:0", ss.ServerConfig()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.run.Close() })
	e.dial = e.run.DialOverride()

	ca := testenv.WriteFile(t, t.TempDir(), "stub-ca.pem", string(ss.CAPEM))
	e.st = testenv.NewStack(t, testenv.StackOptions{Name: "ansibleremote", Valkey: o.valkey, Env: append([]string{
		"TROVE_PROXY_OUTBOUND_TLS_CA_FILES=[" + strconv.Quote(filepath.ToSlash(ca)) + "]",
		`TROVE_PROXY_OUTBOUND_ADDRESS_POLICY_ALLOW_CIDRS=["127.0.0.1/32"]`,
		"TROVE_PROXY_LOCK_POLL_INTERVAL_MS=50",
		"TROVE_VULNERABILITIES_SOURCES_OSV_MODE=off",
		"TROVE_API_RATE_LIMITS_ENABLED=false",
	}, o.env...)})

	roleHosts := []string{ansiblestub.DefaultGitHubHost, ansiblestub.DefaultCodeloadHost}
	for i := 1; i <= scratchRepos; i++ {
		e.repo("ans-"+strconv.Itoa(i), "verify-if-available", roleHosts...)
	}
	e.repo(rRequire, "require", roleHosts...)
	e.repo(rNoRoles, "verify-if-available")
	e.repoAt(rNotGalaxy, e.run.URL+"/api/v3/collections/acme/tools/", "verify-if-available", 300)

	rita, reader := e.st.User("rita"), e.st.User("reader")
	e.st.Bind("admin", rita)
	e.admin = e.st.Token(rita, scope.Scope{Inherit: true, Admin: true})
	e.reader = e.st.Token(reader, scope.Default())
	names := []string{rRequire, rNoRoles, rNotGalaxy}
	for i := 1; i <= scratchRepos; i++ {
		names = append(names, "ans-"+strconv.Itoa(i))
	}
	e.st.Policy("ansible-readers", []string{"read"}, names, reader)
	return e
}

// repo creates a remote repository of the format ansible whose upstream is the API root of the stub; hosts are the extra hosts.
func (e *aenv) repo(key, policy string, hosts ...string) {
	e.t.Helper()
	e.repoAt(key, e.run.APIRoot(), policy, 300, hosts...)
}

func (e *aenv) repoAt(key, upstream, policy string, ttl int, hosts ...string) {
	e.t.Helper()
	extra := make([]any, len(hosts))
	for i, h := range hosts {
		extra[i] = map[string]any{"pattern": h, "sendCredentials": false}
	}
	remote := map[string]any{
		"url": upstream, "extraHosts": extra, "authMode": "none", "username": "", "password": nil,
		"clientCertificate": nil, "headers": []any{}, "forwardAuthOnRedirect": false, "outboundProxy": map[string]any{"mode": "direct"},
		"connectTimeoutMs": 5000, "readTimeoutMs": 15000, "sourceNetwork": nil, "cacheFillAction": "read",
		"cache": map[string]any{
			"storeArtifacts": true, "metadataTtlSeconds": ttl, "metadataRefreshTimeoutSeconds": 10, "notFoundTtlSeconds": 300,
			"maxStaleSeconds": 0, "unusedRetentionHours": 0,
		},
		"integrity":    map[string]any{"checksumPolicy": policy, "blockUnexpectedContentTypes": true, "allowedContentTypes": []any{}},
		"availability": map[string]any{"mode": "online", "failureThreshold": 3, "circuitOpenSeconds": 30},
		"limits":       map[string]any{"maxArtifactBytes": nil, "maxMetadataBytes": nil, "maxConcurrentFetches": 16},
	}
	doc, err := json.Marshal(map[string]any{"remote": remote})
	if err != nil {
		e.t.Fatal(err)
	}
	e.st.Repository(key, "remote", "ansible", string(doc))
}

// fresh returns a scratch repository of the installation that nothing has been fetched through.
func (e *aenv) fresh() string {
	e.t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	e.used++
	if e.used > scratchRepos {
		e.t.Fatal("no scratch repository left")
	}
	return "ans-" + strconv.Itoa(e.used)
}

// start starts n web replicas.
func (e *aenv) start(n int) []*testenv.Replica {
	e.t.Helper()
	out := make([]*testenv.Replica, n)
	for i := range out {
		out[i] = e.st.Start(e.t, testenv.ReplicaOptions{Name: "web-" + strconv.Itoa(len(e.st.Replicas())+1), DialOverride: e.dial})
	}
	return out
}

// baseURL is the response base URL of the default endpoint of the stack.
const baseURL = "http://" + testenv.DefaultHost

// base is the path of the protocol of a repository.
func base(repo string) string { return "/api/ansible/" + project + "/" + repo + "/" }

// get is a client's GET of a path below the repository URL, as the administrator; headers are name/value pairs.
func (e *aenv) get(r *testenv.Replica, repo, path string, hdr ...string) testenv.Answer {
	e.t.Helper()
	return e.st.Do(r, http.MethodGet, base(repo)+path, e.admin, nil, hdr...)
}

// as sends a GET with an Authorization header value ("" for none).
func (e *aenv) as(r *testenv.Replica, authorization, repo, path string) testenv.Answer {
	e.t.Helper()
	if authorization == "" {
		return e.st.Do(r, http.MethodGet, base(repo)+path, "", nil)
	}
	return e.st.Do(r, http.MethodGet, base(repo)+path, "", nil, "Authorization", authorization)
}

// basic is the Authorization value of a client that sends a trove token as the password of HTTP Basic, as netrc does.
func basic(token string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte("netrc:"+token))
}

// follow turns a link of a document into the path below the repository URL; the link is absolute on the response base URL.
func (e *aenv) follow(repo, link string) string {
	e.t.Helper()
	u, err := url.Parse(link)
	if err != nil || !strings.HasPrefix(u.Path, base(repo)) {
		e.t.Fatalf("link %q does not point into the repository %s", link, repo)
	}
	out := strings.TrimPrefix(u.Path, base(repo))
	if u.RawQuery != "" {
		out += "?" + u.RawQuery
	}
	return out
}

// digest is the stub's SHA-256 of the original archive of a version.
func (e *aenv) digest(name, version string) (sum string, size int64) {
	e.t.Helper()
	info, err := e.stub.Corpus()
	if err != nil {
		e.t.Fatal(err)
	}
	for _, c := range info.Collections {
		if c.Namespace+"."+c.Name != name {
			continue
		}
		for _, v := range c.Versions {
			if v.Version == version {
				return v.SHA256, v.Size
			}
		}
	}
	e.t.Fatalf("the stub has no %s %s", name, version)
	return "", 0
}

// requests is the number of GETs the stub saw for a path.
func (e *aenv) requests(path string) int { return e.stub.Counts().Requests(http.MethodGet, path) }

// repoID is the database id of a repository of the default project.
func (e *aenv) repoID(key string) int64 {
	e.t.Helper()
	return e.st.Number(`SELECT r.id FROM repositories r JOIN project p ON p.id = r.project_id WHERE p.key = $1 AND r.key = $2`, project, key)
}

// expire makes every stored document of a repository stale, so that the next request revalidates it.
func (e *aenv) expire(repo string) {
	e.t.Helper()
	e.st.Exec(`UPDATE remote_cache_entries SET expires_at = now() - interval '1 hour', validated_at = now() - interval '2 hours'
	             WHERE repo_id = $1 AND kind = 'metadata'`, e.repoID(repo))
}

// direct reads a path of the stub itself, as a client of the upstream would; the request is counted.
func (e *aenv) direct(rawURL string) []byte {
	e.t.Helper()
	tr := &http.Transport{TLSClientConfig: e.ss.ClientConfig(), DialContext: e.run.DialContext, DisableKeepAlives: true}
	defer tr.CloseIdleConnections()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, rawURL, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	res, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		e.t.Fatal(err)
	}
	return b
}
