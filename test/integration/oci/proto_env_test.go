//go:build integration

package oci

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// The protocol tests of this package run against real listeners: several replicas of one installation (one PostgreSQL, one
// Valkey, one S3 bucket, one key ring), a plain HTTP client that picks the replica of every request, and registry tokens from the
// token service. What a replica serves is what a registry client sees; nothing of the handler is faked.
const (
	project   = testenv.DefaultProject
	repoLocal = "oci-local"
	repoOther = "oci-other"
	repoVirt  = "oci-virtual"
	// repoSecret is a repository no person but the administrator may read.
	repoSecret = "oci-secret"
	// repoOwned enforces package ownership (R-AUTHZ-018): the rows of R-AUTHZ-007 that mark ownership apply to it.
	repoOwned = "oci-owned"

	manifestType = "application/vnd.oci.image.manifest.v1+json"
	indexType    = "application/vnd.oci.image.index.v1+json"
	dockerType   = "application/vnd.docker.distribution.manifest.v2+json"
	octet        = "application/octet-stream"
)

// people are the principals of the installation and what a global policy grants each on the local and the other repository.
var people = []struct {
	name    string
	actions []string
}{
	{"reader", []string{"read"}},
	{"pusher", []string{"read", "deploy"}},
	{"deployonly", []string{"deploy"}},
	{"mover", []string{"read", "deploy", "overwrite"}},
	{"deleter", []string{"read", "delete"}},
	{"nobody", nil},
}

// proto is one installation with the repositories of the package, a trove token per person and a number of replicas.
type proto struct {
	t     *testing.T
	st    *testenv.Stack
	reps  []*testenv.Replica
	admin string
	// troveToken maps a person's name to the PAT that person logs in with.
	troveToken map[string]string
	n          int
}

type protoOptions struct {
	replicas int
	// env is added to the environment of every replica.
	env []string
	// valkeyDown starts the replicas without a reachable Valkey: their Sentinel address answers nothing, the state "Valkey stopped"
	// of R-HA-001, reached by configuration so the shared Valkey of the stack is never touched.
	valkeyDown bool
	// setup runs after the people and repositories exist and before the first replica starts: a person created later is not
	// known to the replicas' caches, so the people of a test are made here.
	setup func(*proto)
	// baseURL, when set, is the base URL and listen address of the first replica (the conformance run, whose realm must be
	// reachable from outside the process).
	baseURL, listen string
}

func newProto(t *testing.T, o protoOptions) *proto {
	t.Helper()
	if o.replicas == 0 {
		o.replicas = 3
	}
	st := testenv.NewStack(t, testenv.StackOptions{Name: "ocip", Valkey: !o.valkeyDown, Env: append([]string{
		"TROVE_VULNERABILITIES_SOURCES_OSV_MODE=off", "TROVE_API_RATE_LIMITS_ENABLED=false",
	}, o.env...)})
	p := &proto{t: t, st: st, troveToken: map[string]string{}}
	rita := st.User("rita")
	st.Bind("admin", rita)
	p.admin = st.Token(rita, scope.Scope{Inherit: true, Admin: true})
	st.Repository(repoLocal, "local", "oci", "{}")
	st.Repository(repoOther, "local", "oci", "{}")
	st.Repository(repoSecret, "local", "oci", "{}")
	st.Repository(repoOwned, "local", "oci", `{"local":{"packageOwnership":"enforce"}}`)
	st.Repository(repoVirt, "virtual", "oci", "{}")
	st.Exec(`INSERT INTO virtual_members (virtual_id, position, member_id) SELECT v.id, 0, m.id FROM repositories v, repositories m WHERE v.key = $1 AND m.key = $2`, repoVirt, repoLocal)
	st.Exec(`INSERT INTO virtual_deploy_targets (virtual_id, target_id) SELECT v.id, m.id FROM repositories v, repositories m WHERE v.key = $1 AND m.key = $2`, repoVirt, repoLocal)
	for _, who := range people {
		u := st.User(who.name)
		p.troveToken[who.name] = st.Token(u, scope.Default())
		if len(who.actions) > 0 {
			st.Policy("p-"+who.name, who.actions, []string{repoLocal, repoOther, repoVirt, repoOwned}, u)
		}
	}
	if o.setup != nil {
		o.setup(p)
	}
	for i := 0; i < o.replicas; i++ {
		ro := testenv.ReplicaOptions{Name: fmt.Sprintf("ocip-%d", i)}
		if i == 0 && o.baseURL != "" {
			ro.Env = []string{"TROVE_SERVER_BASE_URL=" + o.baseURL, "TROVE_SERVER_LISTEN=" + o.listen}
		}
		p.reps = append(p.reps, st.Start(t, ro))
	}
	return p
}

// cred is what a client presents: a registry token (bearer) or a trove token as the password of a Basic header, the direct
// credential of R-OCI-004; the zero value presents nothing.
type cred struct{ bearer, secret string }

func bearer(token string) cred { return cred{bearer: token} }

// as is the direct credential of a person.
func (p *proto) as(who string) cred {
	if who == "admin" {
		return cred{secret: p.admin}
	}
	return cred{secret: p.troveToken[who]}
}

// call is one request of a registry client; rep is the replica that receives it.
type call struct {
	rep          int
	method, path string
	cred         cred
	body         []byte
	hdr          []string
}

var noRedirect = &http.Client{
	Transport:     testenv.Client.Transport,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// expecting is the client of a request that carries Expect: 100-continue. A body over a limit is answered before it is read and the
// connection is closed, so a client that writes the whole body first sees a reset instead of the answer; one that waits for the
// 100 gets the refusal and sends nothing. curl does this for large bodies.
var expecting = &http.Client{
	Transport:     &http.Transport{DisableKeepAlives: true, ExpectContinueTimeout: 10 * time.Second},
	CheckRedirect: noRedirect.CheckRedirect,
	Timeout:       testenv.Client.Timeout,
}

func (p *proto) do(c call) testenv.Answer {
	p.t.Helper()
	var rd io.Reader
	if c.body != nil {
		rd = bytes.NewReader(c.body)
	}
	req, err := http.NewRequestWithContext(context.Background(), c.method, "http://"+p.reps[c.rep%len(p.reps)].Addr("default")+c.path, rd)
	if err != nil {
		p.t.Fatal(err)
	}
	req.Host = testenv.DefaultHost
	switch {
	case c.cred.bearer != "":
		req.Header.Set("Authorization", "Bearer "+c.cred.bearer)
	case c.cred.secret != "":
		req.SetBasicAuth("x", c.cred.secret)
	}
	for i := 0; i+1 < len(c.hdr); i += 2 {
		req.Header.Set(c.hdr[i], c.hdr[i+1])
	}
	client := noRedirect
	if req.Header.Get("Expect") != "" {
		client = expecting
	}
	res, err := client.Do(req)
	if err != nil {
		p.t.Fatalf("%s %s: %v", c.method, c.path, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return testenv.Answer{Status: res.StatusCode, Header: res.Header, Body: b}
}

// errorCode reads the code of the single error of an OCI error body.
func errorCode(a testenv.Answer) string {
	var b struct {
		Errors []struct {
			Code string `json:"code"`
		} `json:"errors"`
	}
	if json.Unmarshal(a.Body, &b) != nil || len(b.Errors) != 1 {
		return ""
	}
	return b.Errors[0].Code
}

// want fails the test unless the answer has the status and, when code is not empty, the error code.
func want(t *testing.T, a testenv.Answer, status int, code string) {
	t.Helper()
	if a.Status != status || (code != "" && errorCode(a) != code) {
		t.Fatalf("status %d code %q, want %d %q: %s", a.Status, errorCode(a), status, code, a.Body)
	}
}

// registryToken asks the token service of replica rep, with the person's trove token, for a registry token with the scopes
// ("repository:it/oci-local/app:pull,push"), the way a client does after a 401.
func (p *proto) registryToken(rep int, who string, scopes ...string) string {
	p.t.Helper()
	a := p.tokenAnswer(rep, who, scopes...)
	if a.Status != http.StatusOK {
		p.t.Fatalf("token request for %s %v: %d %s", who, scopes, a.Status, a.Body)
	}
	var b struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(a.Body, &b); err != nil || b.Token == "" {
		p.t.Fatalf("token answer %q: %v", a.Body, err)
	}
	return b.Token
}

func (p *proto) tokenAnswer(rep int, who string, scopes ...string) testenv.Answer {
	p.t.Helper()
	q := url.Values{"service": {testenv.DefaultHost}}
	for _, s := range scopes {
		q.Add("scope", s)
	}
	secret := p.troveToken[who]
	if who == "admin" {
		secret = p.admin
	}
	return p.do(call{rep: rep, method: "GET", path: "/v2/_token?" + q.Encode(), cred: cred{secret: secret}})
}

// grantOf reads the grant a registry token carries: the token's payload is readable (R-OCI-005), the MAC protects it.
func grantOf(t *testing.T, token string) map[string]int {
	t.Helper()
	raw := strings.TrimPrefix(token, "trv_rgt_")
	payload, _, _ := strings.Cut(raw, ".")
	b, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(string(b), ":", 3)
	if len(parts) != 3 {
		t.Fatalf("token payload %q", b)
	}
	var w struct {
		G []struct {
			R string `json:"r"`
			N string `json:"n"`
			A int    `json:"a"`
		} `json:"g"`
	}
	if err := json.Unmarshal([]byte(parts[2]), &w); err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for _, e := range w.G {
		out[e.R+"/"+e.N] |= e.A
	}
	return out
}

const (
	pull = 1 << iota
	push
	drop
)

func digestOf(b []byte) string {
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
}

// image is a small OCI image: a config, layers and the manifest that names them.
type image struct {
	config   []byte
	layers   [][]byte
	manifest []byte
}

func newImage(seed string, layers ...[]byte) image {
	cfg := []byte(`{"architecture":"amd64","os":"linux","seed":"` + seed + `"}`)
	if len(layers) == 0 {
		layers = [][]byte{[]byte("layer of " + seed + strings.Repeat(".", 64))}
	}
	ls := []any{}
	for _, l := range layers {
		ls = append(ls, map[string]any{"mediaType": "application/vnd.oci.image.layer.v1.tar", "digest": digestOf(l), "size": len(l)})
	}
	m, _ := json.Marshal(map[string]any{
		"schemaVersion": 2, "mediaType": manifestType,
		"config": map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": digestOf(cfg), "size": len(cfg)},
		"layers": ls,
	})
	return image{config: cfg, layers: layers, manifest: m}
}

func (i image) digest() string { return digestOf(i.manifest) }

// repo is the path of an image name below /v2/ in a repository of the package's project.
func repo(r, name string) string { return "/v2/" + project + "/" + r + "/" + name }

func scopeOf(r, name, actions string) string {
	return "repository:" + project + "/" + r + "/" + name + ":" + actions
}

// uploadBlob stores a blob under a name by a monolithic upload on replica rep.
func (p *proto) uploadBlob(rep int, c cred, r, name string, b []byte) testenv.Answer {
	p.t.Helper()
	return p.do(call{rep: rep, method: "POST", path: repo(r, name) + "/blobs/uploads/?digest=" + digestOf(b), cred: c, body: b, hdr: []string{"Content-Type", octet}})
}

// pushImage uploads the blobs of an image and puts its manifest under the reference, each request on the next replica, and
// returns the answer of the manifest PUT.
func (p *proto) pushImage(c cred, r, name, ref string, img image) testenv.Answer {
	p.t.Helper()
	for _, b := range append([][]byte{img.config}, img.layers...) {
		want(p.t, p.uploadBlob(p.next(), c, r, name, b), http.StatusCreated, "")
	}
	return p.putManifest(p.next(), c, r, name, ref, manifestType, img.manifest)
}

func (p *proto) putManifest(rep int, c cred, r, name, ref, mediaType string, body []byte, extra ...string) testenv.Answer {
	p.t.Helper()
	return p.do(call{rep: rep, method: "PUT", path: repo(r, name) + "/manifests/" + ref, cred: c, body: body, hdr: append([]string{"Content-Type", mediaType}, extra...)})
}

// next is the replica of the next request: the scatter dispatcher of the profile (R-ARCH-032), every request on another replica.
func (p *proto) next() int { p.n++; return p.n % len(p.reps) }

// scopedToken is a person who may read, deploy and overwrite in the local repository, with a token whose scope narrows that to the
// package patterns; the token is the person's credential for as(name).
func (p *proto) scopedToken(name string, include []string, exclude ...string) string {
	p.t.Helper()
	u := p.st.User(name)
	p.st.Policy("p-"+name, []string{"read", "deploy", "overwrite"}, []string{repoLocal}, u)
	tok := p.st.Token(u, scope.Scope{Repositories: []scope.Grant{{
		Repositories: []string{project + "/" + repoLocal}, Packages: scope.Patterns{Include: include, Exclude: exclude}, Actions: []string{"read", "deploy", "overwrite"},
	}}})
	p.troveToken[name] = tok
	return tok
}

// pathUser is a person with a policy on the local repository that carries path patterns, the grants of R-AUTHZ-005: the protocol
// routes decide on the canonical path of the item they act on.
func (p *proto) pathUser(name string, actions []string, include, exclude []string) {
	p.t.Helper()
	u := p.st.User(name)
	p.st.Policy("p-"+name, actions, []string{repoLocal}, u)
	for mode, patterns := range map[string][]string{"include": include, "exclude": exclude} {
		for _, pat := range patterns {
			p.st.Exec(`INSERT INTO authz_policy_pattern (policy_id, dimension, mode, pattern) SELECT id, 'path', $2, $3 FROM authz_policy WHERE name = $1`, "p-"+name, mode, pat)
		}
	}
	p.troveToken[name] = p.st.Token(u, scope.Default())
}
