//go:build e2e

package e2e

// The harness of the Bastion tests (R-BAS-049, docs/developer/e2e.md#the-bastion-tests): the main installation in the
// `bastion` profile, the chart trove-bastion installed twice in the namespace trove-bastion (deploy/dev/bastion.sh), the
// registrations and tokens through the management API, the content the clients fetch, and the counting of what the main
// installation was asked (its request log, by the address of the Bastion's pods).

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // dist.shasum is a SHA-1
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	basNPMRepo    = "bas-npm"
	basNuGetRepo  = "bas-nuget"
	basPubSA      = "e2e-bas-pub"
	basFullSA     = "e2e-bas-full"
	basPartSA     = "e2e-bas-part"
	basVolumeReg  = "partners"
	basBucketReg  = "partners-bucket"
	basDefaultNS  = "trove-bastion"
	basKeepEnvVar = "TROVE_E2E_BASTION_KEEP"
)

// basNS is the namespace of the Bastions.
func basNS() string {
	if v := os.Getenv("TROVE_E2E_BASTION_NAMESPACE"); v != "" {
		return v
	}
	return basDefaultNS
}

// basSite is one installed Bastion: a release of the chart with its registration, host and token.
type basSite struct {
	variant string // volume | bucket
	reg     string // the registration at the main installation
	release string // the Helm release and the fullname of the chart
	host    string // the host of the Bastion's base URL
	token   string // the Bastion token in the Secret bastion-token-<variant>
	tokenID string
}

func (s *basSite) selector() string { return "app.kubernetes.io/instance=" + s.release }

// baseURL is the Bastion's address as the clients see it: the edge's port is the cluster's.
func (s *basSite) baseURL(t testing.TB) string { return "https://" + s.host + ":" + edgePort(t) }

// api is a client of the Bastion through the edge from this machine: its address at the Bastion is the Docker gateway's,
// not one of the client pods'.
func (s *basSite) api(t testing.TB, token string) *api {
	t.Helper()
	a := newAPI(t)
	a.base = s.baseURL(t)
	a.token = token
	return a
}

// basEnv is what the scenarios share: created once per run.
type basEnv struct {
	vol, bkt *basSite
	adm      *api
	run      string // six hex digits, part of every package name of this run
	pub      string // token of the publisher
	full     createdToken
	part     createdToken
	// the content of this run
	npmSets map[string]*basNPMSet // by family: npm, pnpm, yarn4
	nuget   *basNuGetSet
	hidden  string // an npm package $PART must not read
	open    string // an npm package $PART may read
}

var (
	basOnce sync.Once
	basVal  *basEnv
	basErr  error
	basIPs  sync.Map // every address a Bastion pod had, for the request log of the main installation
)

func basFx(t testing.TB) *basEnv {
	t.Helper()
	basOnce.Do(func() {
		// a Fatalf inside the build ends this goroutine: the other tests must fail, not meet a nil environment
		basErr = fmt.Errorf("the environment did not finish (see the first failure)")
		basVal, basErr = buildBastionEnv(t)
	})
	if basErr != nil {
		t.Fatalf("bastion environment: %v", basErr)
	}
	return basVal
}

func (e *basEnv) sites() []*basSite { return []*basSite{e.vol, e.bkt} }

// ── namespace-aware kubectl ──────────────────────────────────────────────────

func kb(t testing.TB, args ...string) string {
	t.Helper()
	out, err := kubectl(append([]string{"-n", basNS()}, args...)...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func kbTry(args ...string) (string, error) {
	return kubectl(append([]string{"-n", basNS()}, args...)...)
}

// basPods are the pods of one release; their addresses are remembered.
func basPods(t testing.TB, s *basSite) []pod {
	t.Helper()
	ps := podsOfIn(t, basNS(), s.selector()+",app.kubernetes.io/component=bastion")
	for _, p := range ps {
		if p.IP != "" {
			basIPs.Store(p.IP, true)
		}
	}
	return ps
}

func basReadyPods(t testing.TB, s *basSite) []pod {
	t.Helper()
	var out []pod
	for _, p := range basPods(t, s) {
		if p.Ready && !p.Deleting {
			out = append(out, p)
		}
	}
	return out
}

// waitBastionReady waits for the release's pods: the desired number, all ready, none terminating.
func waitBastionReady(t testing.TB, s *basSite, want int) {
	t.Helper()
	waitFor(t, s.release+" to have "+strconv.Itoa(want)+" ready replicas", 5*time.Minute, 2*time.Second, func() bool {
		ps := basPods(t, s)
		if len(ps) != want {
			return false
		}
		for _, p := range ps {
			if !p.Ready || p.Deleting {
				return false
			}
		}
		return true
	})
}

// basOps reads the ops listener of a Bastion pod through the API server.
func basOps(podName, path string) (int, string) {
	code, _, body, err := httpGet(fmt.Sprintf("%s/api/v1/namespaces/%s/pods/https:%s:8081/proxy%s", cfg.proxy, basNS(), podName, path))
	if err != nil {
		return 0, err.Error()
	}
	return code, body
}

// basMetrics parses the /metrics of a Bastion pod.
func basMetrics(t testing.TB, podName string) map[string]float64 {
	t.Helper()
	code, body := basOps(podName, "/metrics")
	if code != 200 {
		t.Fatalf("%s :8081/metrics = %d %s", podName, code, body)
	}
	return parseMetrics(body)
}

func parseMetrics(body string) map[string]float64 {
	m := map[string]float64{}
	for _, l := range strings.Split(body, "\n") {
		if l == "" || l[0] == '#' {
			continue
		}
		i := strings.LastIndex(l, " ")
		if i < 0 {
			continue
		}
		if v, err := strconv.ParseFloat(l[i+1:], 64); err == nil {
			m[l[:i]] = v
		}
	}
	return m
}

// msum adds the series of a metrics map whose text starts with the prefix and holds every part.
func msum(m map[string]float64, prefix string, parts ...string) float64 {
	var sum float64
	for s, v := range m {
		if !strings.HasPrefix(s, prefix) {
			continue
		}
		ok := true
		for _, p := range parts {
			ok = ok && strings.Contains(s, p)
		}
		if ok {
			sum += v
		}
	}
	return sum
}

// basSum adds a series over every ready pod of the release.
func basSum(t testing.TB, s *basSite, prefix string, parts ...string) float64 {
	t.Helper()
	var sum float64
	for _, p := range basReadyPods(t, s) {
		sum += msum(basMetrics(t, p.Name), prefix, parts...)
	}
	return sum
}

// ── the environment ──────────────────────────────────────────────────────────

func buildBastionEnv(t testing.TB) (*basEnv, error) {
	if err := runUp("bastion"); err != nil {
		return nil, err
	}
	waitServingSettled(t)
	env := &basEnv{
		vol: &basSite{variant: "volume", reg: basVolumeReg, release: "bastion-volume", host: "bastion-volume.trove.localhost"},
		bkt: &basSite{variant: "bucket", reg: basBucketReg, release: "bastion-bucket", host: "bastion-bucket.trove.localhost"},
	}
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	env.run = hex.EncodeToString(b)
	basScript(t, "infra")
	env.adm = adminAPI(t)
	// the installed releases are restarted after the install: nothing of an earlier run (the blocks, the answers, the
	// cache of the volume backend) is in them
	pre := map[string]bool{}
	for _, s := range env.sites() {
		_, err := kbTry("get", "deployment", s.release)
		pre[s.variant] = err == nil
	}
	for _, s := range env.sites() {
		env.registerBastion(t, s)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, s := range env.sites() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := basScriptTry("install", s.variant)
			if err != nil {
				errs <- fmt.Errorf("bastion.sh install %s: %w\n%s", s.variant, err, tail(out, 1500))
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		return nil, err
	}
	if pre["bucket"] {
		basFlushValkey(t)
	}
	for _, s := range env.sites() {
		if pre[s.variant] {
			kb(t, "rollout", "restart", "deployment/"+s.release)
			kb(t, "rollout", "status", "deployment/"+s.release, "--timeout=5m")
		}
		waitBastionReady(t, s, 2)
	}
	for _, s := range env.sites() {
		env.waitContacts(t, s, 2)
	}
	env.buildContent(t)
	return env, nil
}

// basScript runs deploy/dev/bastion.sh.
func basScript(t testing.TB, args ...string) string {
	t.Helper()
	out, err := basScriptTry(args...)
	if err != nil {
		t.Fatalf("bastion.sh %s: %v\n%s", strings.Join(args, " "), err, tail(out, 2000))
	}
	return out
}

func basScriptTry(args ...string) (string, error) {
	cmd := exec.Command(filepath.Join(cfg.repo, "deploy/dev/bastion.sh"), args...)
	cmd.Dir = cfg.repo
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	_ = os.WriteFile(filepath.Join(cfg.out, "bastion-"+strings.Join(args, "-")+".log"), out, 0o644)
	return string(out), err
}

// registerBastion creates the registration (an existing one stays), revokes its tokens, issues one and writes it to the
// Secret the chart reads (R-BAS-004, R-BAS-006).
func (e *basEnv) registerBastion(t testing.TB, s *basSite) {
	t.Helper()
	e.adm.do(http.MethodPost, "/api/v1/bastions", map[string]string{"name": s.reg, "description": "end-to-end tests, release " + s.release}).
		want(t, "register "+s.reg, 201, 409)
	e.revokeAllTokens(t, s.reg)
	s.token, s.tokenID = e.issueToken(t, s.reg, 24*3600)
	basWriteToken(t, s)
}

func (e *basEnv) revokeAllTokens(t testing.TB, reg string) {
	t.Helper()
	var b struct {
		Tokens []struct {
			ID string `json:"id"`
		} `json:"tokens"`
	}
	e.adm.get("/api/v1/bastions/"+reg).want(t, "read "+reg, 200).JSON(t, &b)
	for _, tk := range b.Tokens {
		e.adm.do(http.MethodDelete, "/api/v1/bastions/"+reg+"/tokens/"+tk.ID, nil).want(t, "revoke a token of "+reg, 204, 404)
	}
}

func (e *basEnv) issueToken(t testing.TB, reg string, ttl int) (string, string) {
	t.Helper()
	var c createdToken
	e.adm.do(http.MethodPost, "/api/v1/bastions/"+reg+"/tokens", map[string]any{"ttlSeconds": ttl}).want(t, "issue a token of "+reg, 201).JSON(t, &c)
	if !strings.HasPrefix(c.Token, "trv_bst_") {
		t.Fatalf("the token of %s is no Bastion token", reg)
	}
	return c.Token, c.ID
}

// basWriteToken puts the token into the Secret of the release (the file the Bastion reloads, R-BAS-006).
func basWriteToken(t testing.TB, s *basSite) {
	t.Helper()
	manifest, _ := json.Marshal(map[string]any{
		"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "bastion-token-" + s.variant, "namespace": basNS()},
		"stringData": map[string]string{"token": s.token},
	})
	cmd := exec.Command("kubectl", "--context", cfg.kubeContext, "apply", "-f", "-")
	cmd.Stdin = bytes.NewReader(manifest)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("write the token Secret of %s: %v: %s", s.release, err, out)
	}
}

// contacts reads the last contact of the registration's replicas.
func (e *basEnv) contacts(t testing.TB, s *basSite) []struct {
	Replica, BaseURL string
	ContactAt        time.Time
	Release          string
	InterfaceVersion int
} {
	t.Helper()
	var b struct {
		Contacts []struct {
			Replica          string    `json:"replica"`
			BaseURL          string    `json:"baseUrl"`
			ContactAt        time.Time `json:"contactAt"`
			Release          string    `json:"release"`
			InterfaceVersion int       `json:"interfaceVersion"`
		} `json:"contacts"`
	}
	e.adm.get("/api/v1/bastions/"+s.reg).want(t, "read "+s.reg, 200).JSON(t, &b)
	var out []struct {
		Replica, BaseURL string
		ContactAt        time.Time
		Release          string
		InterfaceVersion int
	}
	for _, c := range b.Contacts {
		out = append(out, struct {
			Replica, BaseURL string
			ContactAt        time.Time
			Release          string
			InterfaceVersion int
		}{c.Replica, c.BaseURL, c.ContactAt, c.Release, c.InterfaceVersion})
	}
	return out
}

func (e *basEnv) waitContacts(t testing.TB, s *basSite, want int) {
	t.Helper()
	waitFor(t, s.reg+" to have the last contact of "+strconv.Itoa(want)+" replicas", 3*time.Minute, 2*time.Second, func() bool {
		live := map[string]bool{}
		for _, p := range basReadyPods(t, s) {
			live[p.Name] = true
		}
		n := 0
		for _, c := range e.contacts(t, s) {
			if live[c.Replica] && time.Since(c.ContactAt) < 90*time.Second {
				n++
			}
		}
		return n >= want
	})
}

// basFlushValkey empties the Valkey of the Bastions (the shared answers, counters and block list of an earlier run).
func basFlushValkey(t testing.TB) {
	t.Helper()
	for _, p := range []string{"bastion-valkey-0", "bastion-valkey-1"} {
		out, err := kbTry("exec", p, "-c", "valkey", "--", "sh", "-c",
			`exec valkey-cli --tls --cacert /tls/ca.crt -h 127.0.0.1 -p 16379 --no-auth-warning -a "$VALKEY_PASSWORD" flushall`)
		if err == nil && strings.Contains(out, "OK") {
			return
		}
	}
	t.Fatal("FLUSHALL reached no Valkey primary of the Bastions")
}

// ── the content of the run ───────────────────────────────────────────────────

type basNPMSet struct {
	family string   // npm | pnpm | yarn4
	root   string   // the package a client installs
	deps   []string // its dependencies
}

type basNuGetSet struct {
	ids     []string // the packages a project references, all 1.0.0
	version string
}

func (s *basNPMSet) tarballs() int { return 1 + len(s.deps) }

// npmTarball builds the tarball of a package: package/package.json, an index and a README.
func npmTarball(t testing.TB, name, version string, deps map[string]string) []byte {
	t.Helper()
	pj := map[string]any{"name": name, "version": version, "main": "index.js"}
	if len(deps) > 0 {
		pj["dependencies"] = deps
	}
	raw, _ := json.Marshal(pj)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	add := func(file, content string) {
		if err := tw.WriteHeader(&tar.Header{Name: "package/" + file, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(content))
	}
	add("package.json", string(raw))
	add("index.js", fmt.Sprintf("module.exports = %q;\n", name+"@"+version))
	add("README.md", "# "+name+"\n")
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// publishNPM publishes name@version the way libnpmpublish does (a PUT of the document with the tarball attached).
func (e *basEnv) publishNPM(t testing.TB, repo, name, version string, deps map[string]string) {
	t.Helper()
	tgz := npmTarball(t, name, version, deps)
	s512 := sha512.Sum512(tgz)
	s1 := sha1.Sum(tgz) //nolint:gosec // dist.shasum is a SHA-1
	file := name + "-" + version + ".tgz"
	man := map[string]any{
		"name": name, "version": version, "_id": name + "@" + version,
		"dist": map[string]any{
			"integrity": "sha512-" + base64.StdEncoding.EncodeToString(s512[:]), "shasum": hex.EncodeToString(s1[:]),
			"tarball": "http://x/" + name + "/-/" + file,
		},
	}
	if len(deps) > 0 {
		man["dependencies"] = deps
	}
	doc := map[string]any{
		"_id": name, "name": name, "dist-tags": map[string]string{"latest": version}, "versions": map[string]any{version: man}, "access": nil,
		"_attachments": map[string]any{file: map[string]any{"content_type": "application/octet-stream", "data": base64.StdEncoding.EncodeToString(tgz), "length": len(tgz)}},
	}
	pub := newAPI(t).withToken(e.pub)
	for range 20 {
		r := pub.do(http.MethodPut, npmBase+repo+"/"+strings.ReplaceAll(name, "/", "%2f"), doc)
		if r.Status == http.StatusTooManyRequests {
			time.Sleep(2 * time.Second)
			continue
		}
		r.want(t, "publish "+name, 200, 201, 409)
		return
	}
	t.Fatalf("publish %s: still rate limited", name)
}

// buildContent creates the repositories, the accounts and the packages of this run on the main installation.
func (e *basEnv) buildContent(t testing.TB) {
	t.Helper()
	for _, body := range []map[string]any{
		{"key": basNPMRepo, "class": "local", "format": "npm", "requireAuthentication": true},
		{"key": basNuGetRepo, "class": "local", "format": "nuget", "requireAuthentication": true},
	} {
		e.adm.do(http.MethodPost, reposRoute, body).want(t, fmt.Sprintf("create %v", body["key"]), 201, 409)
	}
	for _, sa := range []string{basPubSA, basFullSA, basPartSA} {
		ensureSA(t, e.adm, sa)
	}
	ensurePolicy(t, e.adm, basPubSA, []string{basNPMRepo, basNuGetRepo}, [2]any{"sa:" + basPubSA, []string{"read", "deploy", "delete"}})
	ensurePolicy(t, e.adm, basFullSA, []string{basNPMRepo, basNuGetRepo}, [2]any{"sa:" + basFullSA, []string{"read"}})
	// $PART reads the packages of this run whose name starts with "...-open" and nothing else of the repository
	partPolicy := map[string]any{
		"name": basPartSA, "description": "end-to-end tests",
		"resources": map[string]any{"repositories": qualifyAll([]string{basNPMRepo}), "packages": map[string]any{"include": []string{"bas" + e.run + "-open*"}}},
		"entries":   []map[string]any{{"principal": "sa:" + basPartSA, "actions": []string{"read"}}},
	}
	r := e.adm.do(http.MethodPost, "/api/v1/permission-policies", partPolicy)
	if r.Status == 409 {
		g := e.adm.get("/api/v1/permission-policies/"+basPartSA).want(t, "read the policy of $PART", 200)
		r = e.adm.do(http.MethodPut, "/api/v1/permission-policies/"+basPartSA, partPolicy, "If-Match", g.Header.Get("ETag"))
	}
	r.want(t, "write the policy of $PART", 200, 201)
	e.pub = freshSAToken(t, e.adm, basPubSA, 4*3600, map[string]any{"inherit": true}).Token
	e.full = freshSAToken(t, e.adm, basFullSA, 4*3600, map[string]any{"inherit": true})
	e.part = freshSAToken(t, e.adm, basPartSA, 4*3600, map[string]any{"inherit": true})

	e.npmSets = map[string]*basNPMSet{}
	for fam, n := range map[string]int{"npm": 19, "pnpm": 7, "yarn4": 7} {
		set := &basNPMSet{family: fam, root: fmt.Sprintf("bas%s-%s-app", e.run, fam)}
		for i := 1; i <= n; i++ {
			set.deps = append(set.deps, fmt.Sprintf("bas%s-%s-dep%02d", e.run, fam, i))
		}
		e.npmSets[fam] = set
	}
	e.open, e.hidden = fmt.Sprintf("bas%s-open-lib", e.run), fmt.Sprintf("bas%s-secret-lib", e.run)
	var wg sync.WaitGroup
	work := make(chan func(), 64)
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range work {
				f()
			}
		}()
	}
	for _, set := range e.npmSets {
		deps := map[string]string{}
		for _, d := range set.deps {
			work <- func() { e.publishNPM(t, basNPMRepo, d, "1.0.0", nil) }
			deps[d] = "1.0.0"
		}
		work <- func() { e.publishNPM(t, basNPMRepo, set.root, "1.0.0", deps) }
	}
	work <- func() { e.publishNPM(t, basNPMRepo, e.open, "1.0.0", nil) }
	work <- func() { e.publishNPM(t, basNPMRepo, e.hidden, "1.0.0", nil) }
	close(work)
	wg.Wait()
	e.buildNuGet(t)
}

// ── what the main installation was asked ─────────────────────────────────────

// mainReq is a record of the request log of the main installation.
type mainReq struct {
	Time       time.Time
	Trace      string
	Method     string
	Path       string
	Status     int
	RespBytes  int64
	Peer       string
	Remote     string
	Principal  string
	TokenID    string
	DurationMS float64
}

// kind says what a request was for: iface (the Bastion interface), artifact (a tarball or .nupkg), document (a package
// document, a service index, a registration, a flat container listing) or other.
func (m mainReq) kind() string {
	p := m.Path
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p = p[:i]
	}
	switch {
	case strings.HasPrefix(p, "/api/v1/bastion"):
		return "iface"
	case strings.HasPrefix(p, npmBase+basNPMRepo+"/") && strings.HasSuffix(p, ".tgz"):
		return "artifact"
	case strings.HasPrefix(p, nugetBase+basNuGetRepo+"/") && strings.HasSuffix(p, ".nupkg"):
		return "artifact"
	case strings.HasPrefix(p, npmBase+basNPMRepo+"/"), strings.HasPrefix(p, nugetBase+basNuGetRepo+"/"):
		return "document"
	}
	return "other"
}

// mainLog reads the request log of the api and packages replicas, keyed by trace ID.
func mainLog(t testing.TB) map[string]mainReq {
	t.Helper()
	out, err := kubectl("-n", cfg.namespace, "logs", "-l", selServing, "--since=3h", "--tail=-1", "--max-log-requests=16", "--prefix=false")
	if err != nil {
		t.Fatalf("the logs of the api and packages replicas: %v", err)
	}
	m := map[string]mainReq{}
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, `"log":"request"`) {
			continue
		}
		var r struct {
			Time          time.Time `json:"time"`
			TraceID       string    `json:"trace_id"`
			Method        string    `json:"method"`
			Path          string    `json:"path"`
			Status        int       `json:"status"`
			ResponseBytes int64     `json:"response_bytes"`
			PeerAddr      string    `json:"peer_addr"`
			RemoteAddr    string    `json:"remote_addr"`
			Principal     string    `json:"principal"`
			TokenID       *string   `json:"token_id"`
			DurationMS    float64   `json:"duration_ms"`
		}
		if json.Unmarshal([]byte(line), &r) != nil || r.TraceID == "" {
			continue
		}
		q := mainReq{
			Time: r.Time, Trace: r.TraceID, Method: r.Method, Path: r.Path, Status: r.Status, RespBytes: r.ResponseBytes,
			Peer: r.PeerAddr, Remote: r.RemoteAddr, Principal: r.Principal, DurationMS: r.DurationMS,
		}
		if r.TokenID != nil {
			q.TokenID = *r.TokenID
		}
		m[r.TraceID] = q
	}
	return m
}

// fromBastion keeps the requests whose peer is a pod of a Bastion (the pods of both releases, every address they had).
func fromBastion(t testing.TB, all map[string]mainReq, since map[string]mainReq) []mainReq {
	t.Helper()
	for _, s := range []*basSite{basVal.vol, basVal.bkt} {
		basPods(t, s) // remembers the addresses
	}
	var out []mainReq
	for id, r := range all {
		if _, old := since[id]; old {
			continue
		}
		if _, ok := basIPs.Load(r.Peer); ok {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out
}

// mainWindow measures what the Bastions asked the main installation between its creation and a call of done.
type mainWindow struct {
	t      testing.TB
	before map[string]mainReq
}

func openMainWindow(t testing.TB) *mainWindow {
	t.Helper()
	time.Sleep(1500 * time.Millisecond) // the log lines of the requests before are written
	return &mainWindow{t: t, before: mainLog(t)}
}

// done returns the requests of the Bastions' pods to the main installation since the window opened, interface calls apart.
func (w *mainWindow) done() []mainReq {
	w.t.Helper()
	time.Sleep(1500 * time.Millisecond)
	var out []mainReq
	for _, r := range fromBastion(w.t, mainLog(w.t), w.before) {
		if r.kind() != "iface" {
			out = append(out, r)
		}
	}
	return out
}

// split groups the requests by kind.
func splitKinds(rs []mainReq) (artifacts, documents []mainReq) {
	for _, r := range rs {
		switch r.kind() {
		case "artifact":
			artifacts = append(artifacts, r)
		case "document":
			documents = append(documents, r)
		}
	}
	return
}

func describeReqs(rs []mainReq) string {
	var b strings.Builder
	for i, r := range rs {
		if i == 12 {
			fmt.Fprintf(&b, "... %d more\n", len(rs)-12)
			break
		}
		fmt.Fprintf(&b, "%s %s %d %dB peer %s\n", r.Method, r.Path, r.Status, r.RespBytes, r.Peer)
	}
	return b.String()
}

// ── helpers ──────────────────────────────────────────────────────────────────

func keepBastions() bool { return os.Getenv(basKeepEnvVar) == "1" }

// basTeardown removes the Bastions at the end of the run, unless TROVE_E2E_BASTION_KEEP=1.
func basTeardown() {
	if basVal == nil || keepBastions() {
		return
	}
	if out, err := basScriptTry("down"); err != nil {
		fmt.Fprintln(os.Stderr, "e2e: removing the Bastions:", err, tail(out, 600))
	}
}
