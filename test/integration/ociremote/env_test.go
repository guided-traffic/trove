//go:build integration

// Package ociremote tests the remote class of the OCI format (R-OCI-021 to R-OCI-025) against the integration stack and the counting
// stub registry of test/ocistub, which runs in the test process on TLS with three host names (registry, token realm, blob CDN). The
// replicas reach the three names through app.Options.DialOverride, so nothing resolves a name; the egress rules apply to the loopback
// address the stub listens on, which the installation admits as an operator would.
//
// What these tests do not prove, and who does: the acceptance criteria that run `docker pull`, `crane` or six concurrent pulls on three replicas
// (R-OCI-022 digest pull by `crane`, R-OCI-023 six concurrent pulls, R-OCI-025 `docker pull nginx:1`) need a registry client or three replicas behind
// one address, which this harness is not; they are the e2e rows of WP11. The same requests are sent here over HTTP and counted at the stub. The second
// criterion of R-OCI-025 (`quota_header` in `trove_mirror_rate_limited_total`) is the mirror's, WP10.
//
// The requests go to /v2/<project>/<key>/ of a running replica, with the registry token the token service issues for the scope of the image, as a
// container client sends them: the enforcement point, the grant check and the handlers are the registry's.
package ociremote

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/scope"
	_ "github.com/guided-traffic/trove/internal/formats/all"
	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/ocistub"
)

func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }

const (
	manifestTypes = "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, " +
		"application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"
	// secretEnv holds the password of the repositories that authenticate to the stub.
	secretEnv = "TROVE_CAC_OCIREMOTE_IT_PASSWORD"
	// The hosts of the stub as Docker Hub names them.
	hubRegistry, hubRealm, hubCDN = "registry-1.docker.io", "auth.docker.io", "production.cloudfront.docker.com"
)

// The repositories of the installation.
const (
	rAnon   = "oci-anon"   // the stub's realm on its own host, no credential
	rBasic  = "oci-basic"  // authMode basic: the realm is a credential host
	rNoHost = "oci-nohost" // the realm is not an extraHosts entry
	rFast   = "oci-fast"   // a metadata period of 2 s
	rHub    = "oci-hub"    // the upstream is registry-1.docker.io (the stub answers as Docker Hub)
	rNoCDN  = "oci-nocdn"  // the CDN host is not an extraHosts entry: a redirect is followed whatever extraHosts says (R-REPO-039)
	rPrefix = "oci-prefix" // the upstream URL has a namespace prefix
	rHubX   = "oci-hubx"   // Docker Hub with an exclude pattern for the name nginx
)

// renv is one installation with the repositories above, two stubs and a reader.
type renv struct {
	t  *testing.T
	st *testenv.Stack
	// stub answers as the three loopback names, hub as Docker Hub's three.
	stub, hub *ocistub.Server
	run, hrun *ocistub.Running
	dial      map[string]string
	reader    string
	admin     string
	mu        sync.Mutex
	excl      map[string][]string
	tokens    map[string]cachedToken
}

type options struct {
	// valkey starts the replicas with the stack's Valkey.
	valkey bool
	env    []string
	// stub configures the registry stub (the page size of its lists).
	stub ocistub.Config
}

func newEnv(t *testing.T, o options) *renv {
	t.Helper()
	ss, err := ocistub.NewSelfSigned(append(append([]string(nil), ocistub.LoopbackHosts...), hubRegistry, hubRealm, hubCDN)...)
	if err != nil {
		t.Fatal(err)
	}
	e := &renv{t: t, tokens: map[string]cachedToken{}, excl: map[string][]string{rHubX: {"nginx"}}}
	if e.stub, err = ocistub.New(o.stub); err != nil {
		t.Fatal(err)
	}
	if e.hub, err = ocistub.New(ocistub.Config{PublicURL: "https://" + hubRegistry, RealmURL: "https://" + hubRealm, CDNURL: "https://" + hubCDN}); err != nil {
		t.Fatal(err)
	}
	if e.run, err = e.stub.Start("127.0.0.1:0", ss.ServerConfig()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.run.Close() })
	if e.hrun, err = e.hub.Start("127.0.0.1:0", ss.ServerConfig()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.hrun.Close() })
	e.dial = map[string]string{}
	for k, v := range e.run.DialOverride() {
		e.dial[k] = v
	}
	for k, v := range e.hrun.DialOverride() {
		e.dial[k] = v
	}

	ca := testenv.WriteFile(t, t.TempDir(), "stub-ca.pem", string(ss.CAPEM))
	e.st = testenv.NewStack(t, testenv.StackOptions{Name: "ociremote", Valkey: o.valkey, Env: append([]string{
		"TROVE_PROXY_OUTBOUND_TLS_CA_FILES=[" + strconv.Quote(filepath.ToSlash(ca)) + "]",
		`TROVE_PROXY_OUTBOUND_ADDRESS_POLICY_ALLOW_CIDRS=["127.0.0.1/32"]`,
		"TROVE_PROXY_LOCK_POLL_INTERVAL_MS=50",
		"TROVE_VULNERABILITIES_SOURCES_OSV_MODE=off",
		"TROVE_API_RATE_LIMITS_ENABLED=false",
	}, o.env...)})
	t.Setenv(secretEnv, ocistub.DefaultPassword)

	host := func(h string, credentials bool) map[string]any {
		return map[string]any{"pattern": h, "sendCredentials": credentials}
	}
	e.repo(rAnon, e.run.URL, "none", 300, host(ocistub.LoopbackRealmHost, false), host(ocistub.LoopbackCDNHost, false))
	e.repo(rBasic, e.run.URL, "basic", 300, host(ocistub.LoopbackRealmHost, true), host(ocistub.LoopbackCDNHost, false))
	e.repo(rNoHost, e.run.URL, "none", 300, host(ocistub.LoopbackCDNHost, false))
	e.repo(rFast, e.run.URL, "none", 2, host(ocistub.LoopbackRealmHost, false), host(ocistub.LoopbackCDNHost, false))
	e.repo(rHub, "https://"+hubRegistry+"/", "none", 300, host(hubRealm, true))
	e.repo(rNoCDN, e.run.URL, "none", 300, host(ocistub.LoopbackRealmHost, false))
	e.repo(rHubX, "https://"+hubRegistry+"/", "none", 300, host(hubRealm, true))
	e.repo(rPrefix, strings.TrimSuffix(e.run.URL, "/")+"/acme", "none", 300, host(ocistub.LoopbackRealmHost, false))

	rita, reader := e.st.User("rita"), e.st.User("reader")
	e.st.Bind("admin", rita)
	e.admin = e.st.Token(rita, scope.Scope{Inherit: true, Admin: true})
	e.reader = e.st.Token(reader, scope.Default())
	e.st.Policy("oci-readers", []string{"read"}, []string{rAnon, rBasic, rNoHost, rFast, rHub, rNoCDN, rPrefix, rHubX}, reader)
	return e
}

// repo creates a remote repository of the format oci.
func (e *renv) repo(key, upstream, authMode string, ttl int, extra ...map[string]any) {
	e.t.Helper()
	hosts := make([]any, len(extra))
	for i, h := range extra {
		hosts[i] = h
	}
	remote := map[string]any{
		"url": upstream, "extraHosts": hosts, "authMode": authMode, "username": "", "password": nil,
		"clientCertificate": nil, "headers": []any{}, "forwardAuthOnRedirect": false, "outboundProxy": map[string]any{"mode": "direct"},
		"connectTimeoutMs": 5000, "readTimeoutMs": 15000, "sourceNetwork": nil, "cacheFillAction": "read",
		"cache": map[string]any{
			"storeArtifacts": true, "metadataTtlSeconds": ttl, "metadataRefreshTimeoutSeconds": 10, "notFoundTtlSeconds": min(ttl, 60),
			"maxStaleSeconds": 0, "unusedRetentionHours": 0,
		},
		"integrity":    map[string]any{"checksumPolicy": "verify-if-available", "blockUnexpectedContentTypes": true, "allowedContentTypes": []any{}},
		"availability": map[string]any{"mode": "online", "failureThreshold": 3, "circuitOpenSeconds": 30},
		"limits":       map[string]any{"maxArtifactBytes": nil, "maxMetadataBytes": nil, "maxConcurrentFetches": 16},
	}
	if authMode == "basic" {
		remote["username"] = ocistub.DefaultUsername
		remote["password"] = map[string]any{"secretRef": map[string]any{"env": secretEnv}}
	}
	body := map[string]any{"remote": remote}
	if x := e.excl[key]; x != nil {
		body["excludePatterns"] = x
	}
	doc, err := json.Marshal(body)
	if err != nil {
		e.t.Fatal(err)
	}
	e.st.Repository(key, "remote", "oci", string(doc))
	if authMode == "basic" {
		e.st.Exec(`INSERT INTO repository_secrets (repo_id, field, secret_ref) SELECT id, '/remote/password', $2::text::jsonb FROM repositories WHERE key = $1`,
			key, `{"env":"`+secretEnv+`"}`)
	}
}

// start starts n web replicas.
func (e *renv) start(n int) []*testenv.Replica {
	e.t.Helper()
	out := make([]*testenv.Replica, n)
	for i := range out {
		out[i] = e.st.Start(e.t, testenv.ReplicaOptions{Name: "web-" + strconv.Itoa(len(e.st.Replicas())+1), DialOverride: e.dial})
	}
	return out
}

// get is a client's GET of a path below the registry URL of a repository, as the reader; headers are name/value pairs.
func (e *renv) get(r *testenv.Replica, repo, path string, hdr ...string) testenv.Answer {
	e.t.Helper()
	return e.do(r, http.MethodGet, repo, path, hdr...)
}

func (e *renv) do(r *testenv.Replica, method, repo, path string, hdr ...string) testenv.Answer {
	e.t.Helper()
	return e.doAs(r, e.reader, "pull", method, repo, path, hdr...)
}

// doAs sends a request with a registry token that the token service issues to the trove token for the actions on the image the path names.
func (e *renv) doAs(r *testenv.Replica, trove, actions, method, repo, path string, hdr ...string) testenv.Answer {
	e.t.Helper()
	tok := e.registryToken(r, trove, repo, imageOf(path), actions)
	return e.st.Do(r, method, "/v2/"+testenv.DefaultProject+"/"+repo+"/"+path, tok, nil, append([]string{"Accept", manifestTypes}, hdr...)...)
}

// imageOf is the image name of a protocol path, read from the right: a name may hold a component that is a protocol word.
func imageOf(path string) string {
	path, _, _ = strings.Cut(path, "?")
	path = strings.TrimSuffix(path, "/")
	if i := strings.Index(path, "/blobs/uploads"); i >= 0 {
		return path[:i]
	}
	segs := strings.Split(path, "/")
	n := len(segs)
	switch {
	case n >= 3 && segs[n-2] == "tags" && segs[n-1] == "list", n >= 3 && (segs[n-2] == "manifests" || segs[n-2] == "blobs" || segs[n-2] == "referrers"):
		return strings.Join(segs[:n-2], "/")
	}
	return path
}

// registryToken asks the token service of the replica for a token for the actions on an image of a repository, as `docker login` and a pull do.
// Tokens are kept for a minute.
func (e *renv) registryToken(r *testenv.Replica, trove, repo, image, actions string) string {
	e.t.Helper()
	key := trove + "|" + repo + "|" + image + "|" + actions
	e.mu.Lock()
	if c, ok := e.tokens[key]; ok && time.Since(c.at) < time.Minute {
		e.mu.Unlock()
		return c.token
	}
	e.mu.Unlock()
	q := url.Values{"scope": {"repository:" + testenv.DefaultProject + "/" + repo + "/" + image + ":" + actions}}
	a := e.st.Do(r, http.MethodGet, "/v2/_token?"+q.Encode(), trove, nil)
	var body struct {
		Token string `json:"token"`
	}
	if a.Status != http.StatusOK || json.Unmarshal(a.Body, &body) != nil || body.Token == "" {
		e.t.Fatalf("token for %s: %d %s", q.Get("scope"), a.Status, a.Body)
	}
	e.mu.Lock()
	e.tokens[key] = cachedToken{token: body.Token, at: time.Now()}
	e.mu.Unlock()
	return body.Token
}

type cachedToken struct {
	token string
	at    time.Time
}

// pull is what a client does for a tag: the manifest, and for an index the platform manifests and every blob. It returns the digest of
// the manifest the tag names.
func (e *renv) pull(r *testenv.Replica, repo, name, ref string) string {
	e.t.Helper()
	a := e.get(r, repo, name+"/manifests/"+ref)
	if a.Status != 200 {
		e.t.Fatalf("GET %s:%s: %d %s", name, ref, a.Status, a.Body)
	}
	digest := a.Header.Get("Docker-Content-Digest")
	var m struct {
		Config    *struct{ Digest string }
		Layers    []struct{ Digest string }
		Manifests []struct{ Digest string }
	}
	if err := json.Unmarshal(a.Body, &m); err != nil {
		e.t.Fatalf("%s:%s: %v", name, ref, err)
	}
	for _, c := range m.Manifests {
		e.pull(r, repo, name, c.Digest)
	}
	if m.Config != nil {
		e.blob(r, repo, name, m.Config.Digest)
	}
	for _, l := range m.Layers {
		e.blob(r, repo, name, l.Digest)
	}
	return digest
}

func (e *renv) blob(r *testenv.Replica, repo, name, digest string) {
	e.t.Helper()
	a := e.get(r, repo, name+"/blobs/"+digest)
	if a.Status != 200 {
		e.t.Fatalf("GET blob %s of %s: %d %s", digest, name, a.Status, a.Body)
	}
}

// eventually polls until the condition holds: a period of the cache is waited for by asking, never by assuming how long a loaded
// machine takes.
func eventually(t *testing.T, within time.Duration, what string, ok func() bool) {
	t.Helper()
	testenv.WaitFor(t, what, within, ok)
}

// cacheEntries counts the cache entries of a repository below a path prefix.
func (e *renv) cacheEntries(repo, prefix string) int64 {
	e.t.Helper()
	return e.st.Number(`SELECT count(*) FROM remote_cache_entries c JOIN repositories r ON r.id = c.repo_id WHERE r.key = $1 AND c.path LIKE $2`, repo, prefix+"%")
}

// itemCount counts the items of a repository below a path prefix.
func (e *renv) itemCount(repo, prefix string) int64 {
	e.t.Helper()
	return e.st.Number(`SELECT count(*) FROM items i JOIN repositories r ON r.id = i.repo_id WHERE r.key = $1 AND i.kind = 'file' AND i.path LIKE $2`, repo, prefix+"%")
}

// manifestRows counts the rows of the format's manifest table for an image name.
func (e *renv) manifestRows(repo, name string) int64 {
	e.t.Helper()
	return e.st.Number(`SELECT count(*) FROM oci_manifests m JOIN repositories r ON r.id = m.repo_id WHERE r.key = $1 AND m.name = $2`, repo, name)
}
