//go:build e2e

package e2e

// The harness of the npm tests (M2, R-NPM-045): the repositories of the installation (npm-local, npm-remote
// over the counting test upstream, npm-all over both), a publisher with a token, the control API of the test
// upstream, the pods of the clients (npm_clients_test.go) as real package managers that are driven through
// `kubectl exec`, and the matrix report. The scenarios are in npm_matrix_test.go, the replica and upstream
// criteria in npm_replicas_test.go, npm_upstream_test.go and npm_endpoint_test.go; docs/developer/e2e.md
// describes the whole.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/npmstub"
)

// ── what waits for another work package ──────────────────────────────────────

// npmPending marks a scenario that cannot pass until another work package has landed (the remote and virtual
// npm repositories of WP2.5, the endpoints of WP2.9). It is a failure, so a change cannot go green with it;
// TROVE_E2E_NPM_ALLOW_PENDING=1 reports it as a skip that names the package instead, which is what the run of
// a work package that has not merged yet uses. A scenario never calls it for a reason of its own.
func npmPending(t testing.TB, workPackage, what string) {
	t.Helper()
	msg := fmt.Sprintf("PENDING %s: %s", workPackage, what)
	if os.Getenv("TROVE_E2E_NPM_ALLOW_PENDING") == "1" {
		npmReportKind(t, "pending")
		npmKindMu.Lock()
		npmReasonOf[t.Name()] = workPackage + ": " + what
		npmKindMu.Unlock()
		t.Skip(msg)
	}
	t.Fatal(msg + " (TROVE_E2E_NPM_ALLOW_PENDING=1 reports it as pending instead of failing)")
}

// ── fixture ──────────────────────────────────────────────────────────────────

const (
	npmLocalKey  = "npm-local"
	npmRemoteKey = "npm-remote"
	npmAllKey    = "npm-all"
	npmPublisher = "e2e-npm"
)

// npmFixture is the installation the npm tests share: created once per run, idempotent over runs.
type npmFixture struct {
	adm      *api
	token    string // the publisher's token, read/deploy/delete on the three repositories
	run      string // six hex digits, part of every package name this run publishes
	stubHost string // npmstub.<ns>.svc, "" when the test upstream is not deployed

	// What this build of trove accepts: false until WP2.5 serves the classes (the repository API refuses them).
	remote, virtual bool
	whyNot          string
}

var (
	npmFxOnce sync.Once
	npmFxVal  *npmFixture
	npmFxErr  error
)

// npmFx puts the cluster into the scatter profile and creates the repositories, the publisher and its token.
func npmFx(t testing.TB) *npmFixture {
	t.Helper()
	useScatter(t)
	npmFxOnce.Do(func() { npmFxVal, npmFxErr = buildNPMFixture(t) })
	if npmFxErr != nil {
		t.Fatalf("npm fixture: %v", npmFxErr)
	}
	return npmFxVal
}

func buildNPMFixture(t testing.TB) (*npmFixture, error) {
	adm := adminAPI(t)
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	fx := &npmFixture{adm: adm, run: hex.EncodeToString(b), stubHost: os.Getenv("TROVE_E2E_NPMSTUB_HOST")}

	create := func(body map[string]any) (reply, error) {
		return adm.send(http.MethodPost, "/api/v1/repositories", body)
	}
	r, err := create(map[string]any{"key": npmLocalKey, "class": "local", "format": "npm", "requireAuthentication": true})
	if err != nil {
		return nil, err
	}
	if r.Status != 201 && r.Status != 409 {
		return nil, fmt.Errorf("create %s: %s", npmLocalKey, r)
	}
	// The classes WP2.5 adds: a refusal is recorded as the reason the scenarios that need them are pending.
	if fx.stubHost != "" {
		r, err = create(map[string]any{
			"key": npmRemoteKey, "class": "remote", "format": "npm",
			"remote": map[string]any{"url": "https://" + fx.stubHost + "/"},
		})
		if err != nil {
			return nil, err
		}
		switch r.Status {
		case 201, 409:
			fx.remote = true
		default:
			fx.whyNot = fmt.Sprintf("creating %s answered %s", npmRemoteKey, r)
		}
	} else {
		fx.whyNot = "TROVE_E2E_NPMSTUB_HOST is not set: the test upstream is not deployed"
	}
	if fx.remote {
		r, err = create(map[string]any{
			"key": npmAllKey, "class": "virtual", "format": "npm", "requireAuthentication": true,
			"virtual": map[string]any{"members": []string{npmLocalKey, npmRemoteKey}, "deployTarget": npmLocalKey},
		})
		if err != nil {
			return nil, err
		}
		switch r.Status {
		case 201, 409:
			fx.virtual = true
		default:
			fx.whyNot = fmt.Sprintf("creating %s answered %s", npmAllKey, r)
		}
	}

	ensureSA(t, adm, npmPublisher)
	repos := []string{npmLocalKey}
	if fx.remote {
		repos = append(repos, npmRemoteKey)
	}
	if fx.virtual {
		repos = append(repos, npmAllKey)
	}
	ensurePolicy(t, adm, npmPublisher, repos, [2]any{"sa:" + npmPublisher, []string{"read", "deploy", "delete"}})
	fx.token = newSAToken(t, adm, npmPublisher, 4*3600, map[string]any{"inherit": true}).Token
	return fx, nil
}

// registry is the repository the matrix talks to: npm-all once the virtual class exists, else npm-local, whose
// documents, tarballs and mutations are the same code (R-NPM-029 routes a write through npm-all to it).
func (fx *npmFixture) registry() string {
	if fx.virtual {
		return npmAllKey
	}
	return npmLocalKey
}

// registryURL is the base URL of a repository as the setup snippets print it (R-NPM-041): the edge's address.
func registryURL(t testing.TB, host, key string) string {
	return "https://" + host + ":" + edgePort(t) + "/api/npm/" + key + "/"
}

// pkgName is a package name of this run: unique per row and run, so a rerun on the same cluster never meets
// a version that exists.
func (fx *npmFixture) pkgName(label, role string, scoped bool) string {
	n := fmt.Sprintf("e2e-%s-%s", label, role)
	if scoped {
		return "@e2e/" + strings.TrimPrefix(n, "e2e-")
	}
	return n
}

// needRemote fails the test as pending on WP2.5 when the build does not serve remote and virtual npm repositories.
func (fx *npmFixture) needRemote(t testing.TB) {
	t.Helper()
	if !fx.virtual {
		npmPending(t, "WP2.5", "remote and virtual npm repositories: "+fx.whyNot)
	}
}

// ── the counting test upstream ───────────────────────────────────────────────

// stubControl is the client of the control API below /-/stub/ (docs/developer/e2e.md, "Control API"): a
// port-forward to the Service, the token of Secret trove-npmstub, the certificate verified for the Service name.
type stubControl struct {
	base string
	tok  string
	c    *http.Client
}

var (
	stubOnce sync.Once
	stubVal  *stubControl
)

func stub(t testing.TB) *stubControl {
	t.Helper()
	host := os.Getenv("TROVE_E2E_NPMSTUB_HOST")
	if host == "" {
		t.Fatal("TROVE_E2E_NPMSTUB_HOST is not set; run the npm sets of deploy/dev/e2e.sh")
	}
	stubOnce.Do(func() {
		addr := portForward(t, "svc/npmstub", 443).addr
		var d net.Dialer
		stubVal = &stubControl{
			base: "https://" + host,
			tok:  secretValue(t, "trove-npmstub", "control-token"),
			c: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
				DisableKeepAlives: true,
				DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
					return dialForward(ctx, &d, network, addr)
				},
				TLSClientConfig: &tls.Config{RootCAs: caPool(t), MinVersion: tls.VersionTLS12, ServerName: host},
			}},
		}
	})
	return stubVal
}

func (s *stubControl) send(t testing.TB, method, path string, body any) []byte {
	t.Helper()
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, s.base+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+s.tok)
	resp, err := s.c.Do(req)
	if err != nil {
		t.Fatalf("test upstream %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		t.Fatalf("test upstream %s %s: %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return raw
}

// counts reads the counters.
func (s *stubControl) counts(t testing.TB) npmstub.Counts {
	t.Helper()
	var c npmstub.Counts
	if err := json.Unmarshal(s.send(t, http.MethodGet, "/-/stub/counts", nil), &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// reset zeroes the counters; resetAll also clears the modes and the package changes.
func (s *stubControl) reset(t testing.TB) { s.send(t, http.MethodPost, "/-/stub/reset", nil) }

func (s *stubControl) resetAll(t testing.TB) {
	s.send(t, http.MethodPost, "/-/stub/reset?all=true", nil)
}

func (s *stubControl) setMode(t testing.TB, m npmstub.Mode) {
	t.Helper()
	s.send(t, http.MethodPost, "/-/stub/mode", m)
}

func (s *stubControl) publish(t testing.TB, name, version string, deps map[string]string, tags ...string) {
	t.Helper()
	s.send(t, http.MethodPost, "/-/stub/publish", map[string]any{"name": name, "version": version, "dependencies": deps, "tags": tags})
}

// ── the clients ──────────────────────────────────────────────────────────────

// npmClient is one gate row in one client pod. Everything it writes lives below /work/<tag> in the pod: its
// HOME (npm's cache, pnpm's store, .npmrc written by a login) and one directory per scenario.
type npmClient struct {
	t   testing.TB
	fx  *npmFixture
	row string
	pod int
	tag string
	// token is what ${TROVE_TOKEN} expands to; empty runs the client anonymously.
	token string
	// registryKey is the repository the snippets point the client at.
	registryKey string
	// extraEnv are added to every command (a pnpm setting through the environment, for example).
	extraEnv []string
	// published says that the row's scoped and unscoped package exist (ensurePublished).
	published bool
	// host is the name in the registry URL of the snippets: localhost, or the host of a customer endpoint.
	host string
}

// pkg is a package name that belongs to this client alone: the tag is the row, the run and the scenario group, so two
// groups of one run, and two runs on one cluster, never meet a version that exists.
func (c *npmClient) pkg(role string, scoped bool) string {
	return c.fx.pkgName(c.tag, role, scoped)
}

// hostName is the host of the registry URL the client is configured with.
func (c *npmClient) hostName() string {
	if c.host == "" {
		return "localhost"
	}
	return c.host
}

// newNPMClient returns the row's client in the pod i%pods.
func newNPMClient(t testing.TB, fx *npmFixture, row string, pod int) *npmClient {
	return &npmClient{t: t, fx: fx, row: row, pod: pod, tag: row + "-" + fx.run, token: fx.token, registryKey: fx.registry()}
}

// family is "npm", "pnpm", "yarn4" or "yarn1".
func (c *npmClient) family() string {
	switch {
	case strings.HasPrefix(c.row, "pnpm"):
		return "pnpm"
	case strings.HasPrefix(c.row, "npm"):
		return "npm"
	default:
		return c.row
	}
}

func (c *npmClient) home() string           { return "/work/" + c.tag + "/home" }
func (c *npmClient) dir(name string) string { return "/work/" + c.tag + "/" + name }

// environment is what every command of the row runs with: the credential the snippets expand, the isolation of
// the row's caches, and the switches that keep the clients quiet and deterministic. None of it changes the
// registry configuration, which comes from the snippet files alone.
func (c *npmClient) environment() []string {
	env := []string{"HOME=" + c.home(), "NODE_EXTRA_CA_CERTS=/etc/trove-ca/ca.crt"}
	if c.token != "" {
		env = append(env, "TROVE_TOKEN="+c.token)
	}
	switch c.family() {
	case "npm":
		env = append(env, "npm_config_update_notifier=false", "npm_config_fund=false", "npm_config_audit=false", "npm_config_cache="+c.home()+"/npm-cache")
	case "pnpm":
		env = append(env, "npm_config_update_notifier=false", "npm_config_store_dir="+c.home()+"/pnpm-store", "npm_config_cache_dir="+c.home()+"/pnpm-cache",
			"npm_config_manage_package_manager_versions=false")
	case "yarn4":
		env = append(env, "YARN_ENABLE_TELEMETRY=0", "YARN_ENABLE_GLOBAL_CACHE=false", "YARN_NODE_LINKER=node-modules", "YARN_ENABLE_PROGRESS_BARS=false",
			"YARN_ENABLE_IMMUTABLE_INSTALLS=false", "YARN_ENABLE_MIRROR=false", "YARN_RC_FILENAME=.yarnrc.yml",
			// Yarn 4.10 and later quarantine a version younger than npmMinimalAgeGate and refuse an add of a package
			// published a moment ago; the scenarios install what they published seconds earlier.
			"YARN_NPM_MINIMAL_AGE_GATE=0")
	case "yarn1":
		env = append(env, "YARN_CACHE_FOLDER="+c.home()+"/yarn-cache", "YARN_IGNORE_PATH=1")
	}
	return append(env, c.extraEnv...)
}

// npmRun is the result of one client command.
type npmRun struct {
	Out  string // standard output and standard error, interleaved as the client wrote them
	Code int    // the exit status; -1 when the command did not start
	Dur  time.Duration
}

// exec runs argv in dir of the row's pod and returns its output and status. A command that fails is a result,
// not an error: scenarios of the P1 answers expect failures.
func (c *npmClient) exec(dir string, extraEnv []string, argv ...string) npmRun {
	c.t.Helper()
	pod := npmClientPod(c.t, c.pod)
	args := []string{"--context", cfg.kubeContext, "-n", cfg.namespace, "exec", pod, "--", "env", "-C", dir}
	args = append(args, c.environment()...)
	args = append(args, extraEnv...)
	args = append(args, argv...)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	start := time.Now()
	err := cmd.Run()
	r := npmRun{Out: out.String(), Dur: time.Since(start)}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		r.Code = ee.ExitCode()
	default:
		r.Code = -1
		r.Out += "\n" + err.Error()
	}
	return r
}

// execPTY runs argv under a pseudo-terminal in the pod (util-linux script), as a person at a prompt would: the
// clients ask for a user name and a password only when stdin is a terminal. Each answer is typed after a pause,
// so the prompt that reads it is already there.
func (c *npmClient) execPTY(dir string, answers []string, argv ...string) npmRun {
	c.t.Helper()
	pod := npmClientPod(c.t, c.pod)
	args := []string{"--context", cfg.kubeContext, "-n", cfg.namespace, "exec", "-i", pod, "--", "env", "-C", dir}
	args = append(args, c.environment()...)
	args = append(args, "script", "-qec", strings.Join(argv, " "), "/dev/null")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	pr, pw := io.Pipe()
	cmd.Stdin = pr
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	go func() {
		time.Sleep(1500 * time.Millisecond)
		for _, a := range answers {
			if _, err := io.WriteString(pw, a+"\n"); err != nil {
				return
			}
			time.Sleep(1200 * time.Millisecond)
		}
		_ = pw.Close()
	}()
	start := time.Now()
	err := cmd.Run()
	_ = pr.Close()
	r := npmRun{Out: ansiEscape.ReplaceAllString(out.String(), ""), Dur: time.Since(start)}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		r.Code = ee.ExitCode()
	default:
		r.Code = -1
		r.Out += "\n" + err.Error()
	}
	return r
}

var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]|\r|\\\x1b\[1G`)

// run executes the row's own program (npm11, pnpm12, yarn4, ...) in dir.
func (c *npmClient) run(dir string, args ...string) npmRun {
	c.t.Helper()
	return c.exec(dir, nil, append([]string{c.row}, args...)...)
}

// must runs the row's program and fails the test unless it exits 0.
func (c *npmClient) must(dir string, args ...string) npmRun {
	c.t.Helper()
	r := c.run(dir, args...)
	if r.Code != 0 {
		c.t.Fatalf("%s %s exited %d in %s:\n%s", c.row, strings.Join(args, " "), r.Code, dir, tailLines(r.Out, 40))
	}
	return r
}

// sh runs a shell script in dir with the row's environment (file checks, cleanup).
func (c *npmClient) sh(dir, script string) npmRun {
	c.t.Helper()
	return c.exec(dir, nil, "sh", "-c", script)
}

// write puts a file into the pod, creating the directory.
func (c *npmClient) write(path, content string) {
	c.t.Helper()
	pod := npmClientPod(c.t, c.pod)
	cmd := exec.Command("kubectl", "--context", cfg.kubeContext, "-n", cfg.namespace, "exec", "-i", pod, "--", "sh", "-c",
		`mkdir -p "$(dirname "$1")" && cat > "$1"`, "sh", path)
	cmd.Stdin = strings.NewReader(content)
	if out, err := cmd.CombinedOutput(); err != nil {
		c.t.Fatalf("write %s in %s: %v: %s", path, pod, err, out)
	}
}

// read returns a file of the pod.
func (c *npmClient) read(path string) string {
	c.t.Helper()
	r := c.sh("/work", `cat "`+path+`"`)
	if r.Code != 0 {
		c.t.Fatalf("read %s: %s", path, r.Out)
	}
	return r.Out
}

// project creates a scenario directory with a package.json and returns its path. The empty yarn.lock makes
// Yarn 4 treat the directory as its own project root.
func (c *npmClient) project(name string, pkg map[string]any) string {
	c.t.Helper()
	dir := c.dir(name)
	if pkg == nil {
		pkg = map[string]any{}
	}
	if _, ok := pkg["name"]; !ok {
		pkg["name"] = "e2e-consumer-" + name
	}
	if _, ok := pkg["version"]; !ok {
		pkg["version"] = "1.0.0"
	}
	pkg["private"] = true
	raw, _ := json.MarshalIndent(pkg, "", "  ")
	c.sh("/work", `rm -rf "`+dir+`" && mkdir -p "`+dir+`"`)
	c.write(dir+"/package.json", string(raw)+"\n")
	if c.family() == "yarn4" {
		c.write(dir+"/yarn.lock", "")
	}
	return dir
}

func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = append([]string{fmt.Sprintf("... %d earlier lines omitted", len(lines)-n)}, lines[len(lines)-n:]...)
	}
	return strings.Join(lines, "\n")
}

// ── pinning a client to a replica ────────────────────────────────────────────

// pinClient makes the forwarder of client pod i relay to one web pod's listener (pod IP, port 8080) instead of
// the edge, through the target file of the forwarder (test/e2e/clients/fwd). The base URL the client uses and
// the certificate it verifies stay those of the edge: the pod's certificate names localhost too.
func pinClient(t testing.TB, i int, webPodIP string) {
	t.Helper()
	npmForwarderTarget(t, i, webPodIP+":8080")
}

// unpinClient returns the forwarder of pod i to the edge.
func unpinClient(t testing.TB, i int) { t.Helper(); npmForwarderTarget(t, i, "") }

func npmForwarderTarget(t testing.TB, i int, target string) {
	t.Helper()
	pod := npmClientPod(t, i)
	// the image has no shell built-in for this on a read-only root: /tmp is the pod's emptyDir
	kn(t, "exec", pod, "--", "sh", "-c", fmt.Sprintf("printf '%%s' '%s' > /tmp/target", target))
}

// ── the matrix report ────────────────────────────────────────────────────────

// npmReport collects the outcome of every cell of the matrix (row x scenario) and writes the counts per gate row
// at the end: passed, not applicable (with the reason), pending (with the work package) and failed.
type npmReport struct {
	mu    sync.Mutex
	cells []npmCell
}

type npmCell struct {
	Run      string `json:"run"` // the test that produced the cells ("matrix", "matrix-valkey-stopped")
	Row      string `json:"row"`
	Scenario string `json:"scenario"`
	Outcome  string `json:"outcome"` // passed, failed, not applicable, pending
	Reason   string `json:"reason,omitempty"`
}

var (
	npmRep      npmReport
	npmKindMu   sync.Mutex
	npmKindOf   = map[string]string{}
	npmReasonOf = map[string]string{}
)

// npmReportKind records why the running test ends in a skip: "pending" or "not applicable".
func npmReportKind(t testing.TB, kind string) {
	npmKindMu.Lock()
	npmKindOf[t.Name()] = kind
	npmKindMu.Unlock()
}

// npmNotApplicable ends the running scenario as not applicable for the row, with the reason the client cannot run it.
func npmNotApplicable(t testing.TB, reason string) {
	t.Helper()
	npmReportKind(t, "not applicable")
	npmKindMu.Lock()
	npmReasonOf[t.Name()] = reason
	npmKindMu.Unlock()
	t.Skip("NOT APPLICABLE: " + reason)
}

// record adds the outcome of the finished subtest t.
func (r *npmReport) record(run, row, scenario string, t *testing.T) {
	outcome, reason := "passed", ""
	npmKindMu.Lock()
	kind := npmKindOf[t.Name()]
	reason = npmReasonOf[t.Name()]
	npmKindMu.Unlock()
	switch {
	case t.Failed():
		outcome = "failed"
	case t.Skipped() && kind != "":
		outcome = kind
	case t.Skipped():
		outcome = "skipped"
	}
	r.mu.Lock()
	r.cells = append(r.cells, npmCell{Run: run, Row: row, Scenario: scenario, Outcome: outcome, Reason: reason})
	r.mu.Unlock()
}

// flush logs the table of counts per gate row and writes the cells to the artifact directory.
func (r *npmReport) flush(t testing.TB, run string) {
	r.mu.Lock()
	var mine []npmCell
	for _, c := range r.cells {
		if c.Run == run {
			mine = append(mine, c)
		}
	}
	r.mu.Unlock()
	sort.Slice(mine, func(i, j int) bool {
		if mine[i].Row != mine[j].Row {
			return mine[i].Row < mine[j].Row
		}
		return mine[i].Scenario < mine[j].Scenario
	})
	counts := map[string]map[string]int{}
	for _, c := range mine {
		if counts[c.Row] == nil {
			counts[c.Row] = map[string]int{}
		}
		counts[c.Row][c.Outcome]++
	}
	var b strings.Builder
	fmt.Fprintf(&b, "npm matrix (%s): scenarios per gate row\n%-8s %7s %7s %5s %8s\n", run, "row", "passed", "failed", "n/a", "pending")
	for _, row := range npmGateRows {
		m := counts[row]
		fmt.Fprintf(&b, "%-8s %7d %7d %5d %8d\n", row, m["passed"], m["failed"], m["not applicable"], m["pending"])
	}
	t.Log("\n" + b.String())
	raw, _ := json.MarshalIndent(mine, "", "  ")
	_ = os.WriteFile(filepath.Join(cfg.out, "npm-"+run+".json"), raw, 0o644)
}
