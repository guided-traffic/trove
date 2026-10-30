//go:build e2e

// Package e2e holds the end-to-end tests of the dev cluster: the exit criteria of M0 that need
// Kubernetes (E0.3, E0.4, E0.6, E0.7, E0.11 to E0.14). The tests drive kubectl, helm, the
// k6 container and the driver pod; deploy/dev/e2e.sh prepares all of them and calls go test.
// docs/developer/e2e.md describes the layout.
package e2e

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// settings come from deploy/dev/e2e.sh; a missing one is a failure, never a skip.
type settings struct {
	repo, kubeContext, namespace string
	edgeURL                      string // from the host: http://localhost:<port>
	k6Target, k6Image            string // from the kind Docker network: http://trove-control-plane:30080
	out                          string
	proxy                        string // kubectl proxy, to reach :8081 of a pod
	// The scheme of the pods' ops listener (probes) as the API server reaches it, a CA bundle that
	// signs the edge's certificate (empty: system roots), the host name the edge routes on and, for a
	// TLS edge that k6 must reach by name, the address behind that name. All come from deploy/dev/e2e.sh.
	opsScheme, caFile, host, k6HostsAddr string
}

var cfg settings

func TestMain(m *testing.M) {
	need := func(name string) string {
		v := os.Getenv(name)
		if v == "" {
			fmt.Fprintf(os.Stderr, "e2e: %s is not set; run the suite through deploy/dev/e2e.sh (make e2e)\n", name)
			os.Exit(2)
		}
		return v
	}
	cfg = settings{
		repo: need("TROVE_E2E_REPO"), kubeContext: need("TROVE_E2E_CONTEXT"), namespace: need("TROVE_E2E_NAMESPACE"),
		edgeURL: need("TROVE_E2E_EDGE_URL"), k6Target: need("TROVE_E2E_K6_TARGET"), k6Image: need("TROVE_E2E_K6_IMAGE"),
		out: need("TROVE_E2E_OUT"), opsScheme: os.Getenv("TROVE_E2E_OPS_SCHEME"), caFile: os.Getenv("TROVE_E2E_CA_FILE"),
		host: os.Getenv("TROVE_E2E_HOST"), k6HostsAddr: os.Getenv("TROVE_E2E_K6_HOSTS_ADDR"),
	}
	if cfg.opsScheme == "" {
		cfg.opsScheme = "http"
	}
	if cfg.host == "" {
		cfg.host = "localhost"
	}
	if cfg.caFile != "" {
		pem, err := os.ReadFile(cfg.caFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "e2e:", err)
			os.Exit(2)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			fmt.Fprintln(os.Stderr, "e2e: no certificate in", cfg.caFile)
			os.Exit(2)
		}
		httpc.Transport = &http.Transport{DisableKeepAlives: true, TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	}
	if err := os.MkdirAll(cfg.out, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		os.Exit(2)
	}
	stop, err := startProxy()
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e: kubectl proxy:", err)
		os.Exit(2)
	}
	code := m.Run()
	stopForwards()
	restoreProfile()
	stop()
	os.Exit(code)
}

// startProxy runs `kubectl proxy` on a free port: the API server forwards a request for
// /api/v1/namespaces/<ns>/pods/<pod>:8081/proxy/readyz to the pod and returns the pod's status
// code, so the tests read the probes of every pod without a port-forward that would not survive
// the pod.
func startProxy() (func(), error) {
	cmd := exec.Command("kubectl", "--context", cfg.kubeContext, "proxy", "--port=0", "--address=127.0.0.1")
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		return nil, err
	}
	m := regexp.MustCompile(`127\.0\.0\.1:(\d+)`).FindStringSubmatch(line)
	if m == nil {
		return nil, fmt.Errorf("unexpected output %q", line)
	}
	cfg.proxy = "http://127.0.0.1:" + m[1]
	go func() { _, _ = io.Copy(io.Discard, out) }()
	return func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }, nil
}

// ── kubectl ──────────────────────────────────────────────────────────────────

func kubectlCtx(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "kubectl", append([]string{"--context", cfg.kubeContext}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err != nil {
		return stdout.String(), fmt.Errorf("kubectl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func kubectl(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	return kubectlCtx(ctx, args...)
}

func kn(t testing.TB, args ...string) string {
	t.Helper()
	out, err := kubectl(append([]string{"-n", cfg.namespace}, args...)...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func script(t testing.TB, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(filepath.Join(cfg.repo, "deploy/dev", name), args...)
	cmd.Dir = cfg.repo
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	t.Logf("%s %s:\n%s", name, strings.Join(args, " "), strings.TrimSpace(buf.String()))
	if err != nil {
		t.Fatalf("%s failed: %v", name, err)
	}
	return buf.String()
}

type pod struct {
	Name, Node, Phase, UID string
	IP                     string // the pod address, for a forwarder that pins a client to the replica
	Ready, Deleting        bool
	Restarts               int
	Created                time.Time
}

const (
	selWeb    = "app.kubernetes.io/instance=trove,app.kubernetes.io/component=web,trove/serve=true"
	selWorker = "app.kubernetes.io/instance=trove,app.kubernetes.io/component=worker,trove/serve=true"
	selTrove  = "app.kubernetes.io/instance=trove,trove/serve=true,app.kubernetes.io/component in (web,worker)" // the replicas: not the driver pod, which carries the serve label for the Valkey NetworkPolicy
)

func podsOf(t testing.TB, selector string) []pod {
	t.Helper()
	raw := kn(t, "get", "pods", "-l", selector, "-o", "json")
	var l struct {
		Items []struct {
			Metadata struct {
				Name, UID         string
				DeletionTimestamp *time.Time
				CreationTimestamp time.Time
			}
			Spec   struct{ NodeName string }
			Status struct {
				Phase             string
				PodIP             string `json:"podIP"`
				ContainerStatuses []struct {
					Ready        bool
					RestartCount int
				}
			}
		}
	}
	if err := json.Unmarshal([]byte(raw), &l); err != nil {
		t.Fatal(err)
	}
	var out []pod
	for _, i := range l.Items {
		p := pod{Name: i.Metadata.Name, UID: i.Metadata.UID, Node: i.Spec.NodeName, Phase: i.Status.Phase, IP: i.Status.PodIP, Deleting: i.Metadata.DeletionTimestamp != nil, Created: i.Metadata.CreationTimestamp}
		p.Ready = len(i.Status.ContainerStatuses) > 0
		for _, c := range i.Status.ContainerStatuses {
			p.Ready = p.Ready && c.Ready
			p.Restarts += c.RestartCount
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// livePods are the pods that are not terminating.
func livePods(t testing.TB, selector string) []pod {
	var out []pod
	for _, p := range podsOf(t, selector) {
		if !p.Deleting {
			out = append(out, p)
		}
	}
	return out
}

// ── waiting ──────────────────────────────────────────────────────────────────

func waitFor(t testing.TB, what string, within time.Duration, every time.Duration, cond func() bool) time.Duration {
	t.Helper()
	start := time.Now()
	for {
		if cond() {
			return time.Since(start)
		}
		if time.Since(start) > within {
			t.Fatalf("timed out after %s waiting for %s", within, what)
		}
		time.Sleep(every)
	}
}

// ── HTTP ─────────────────────────────────────────────────────────────────────

var httpc = &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}

func httpGet(url string) (int, http.Header, string, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, "", err
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return 0, nil, "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(b), nil
}

// ops reads an endpoint of a pod's ops listener (:8081) through the API server.
func ops(podName, path string) (int, string) {
	target := podName
	if cfg.opsScheme == "https" {
		target = "https:" + podName
	}
	code, _, body, err := httpGet(fmt.Sprintf("%s/api/v1/namespaces/%s/pods/%s:8081/proxy%s", cfg.proxy, cfg.namespace, target, path))
	if err != nil {
		return 0, err.Error()
	}
	return code, body
}

func edge(path string) (int, http.Header, string, error) { return httpGet(cfg.edgeURL + path) }

type replica struct {
	Name     string   `json:"name"`
	Roles    []string `json:"roles"`
	Lost     bool     `json:"lost"`
	State    string   `json:"state"`
	Degraded []string `json:"degraded"`
	Leases   []struct {
		Name string `json:"name"`
	} `json:"leases"`
	InstanceUID string `json:"instanceUid"`
}

func replicas() ([]replica, error) {
	code, _, body, err := edge("/api/v1/system/replicas")
	if err != nil {
		return nil, err
	}
	if code != 200 {
		return nil, fmt.Errorf("GET /api/v1/system/replicas: %d %s", code, strings.TrimSpace(body))
	}
	var l struct{ Items []replica }
	if err := json.Unmarshal([]byte(body), &l); err != nil {
		return nil, err
	}
	return l.Items, nil
}

func (r replica) role() string { return strings.Join(r.Roles, ",") }

// ── PostgreSQL ───────────────────────────────────────────────────────────────

func pgPrimary(t testing.TB) string {
	t.Helper()
	return strings.TrimSpace(kn(t, "get", "cluster", "trove-db", "-o", "jsonpath={.status.currentPrimary}"))
}

// sql runs a statement as the superuser on the current primary; it retries while a failover moves it.
func sql(t testing.TB, query string) string {
	t.Helper()
	var last error
	for i := 0; i < 30; i++ {
		out, err := kubectl("-n", cfg.namespace, "exec", pgPrimary(t), "-c", "postgres", "--", "psql", "-U", "postgres", "-d", "trove", "-At", "-F", "|", "-c", query)
		if err == nil {
			return strings.TrimSpace(out)
		}
		last = err
		time.Sleep(time.Second)
	}
	t.Fatal(last)
	return ""
}

// ── driver pod ───────────────────────────────────────────────────────────────

const driverPod = "trove-e2e-driver"

func driver(t testing.TB, args ...string) string {
	t.Helper()
	out, err := kubectl(append([]string{"-n", cfg.namespace, "exec", driverPod, "--", "/driver"}, args...)...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

type stat struct {
	N   int     `json:"n"`
	P50 float64 `json:"p50_ms"`
	P99 float64 `json:"p99_ms"`
	Max float64 `json:"max_ms"`
}

type loadSummary struct {
	Bounds  map[string]int64 `json:"bounds"`
	Changes struct {
		Written       int  `json:"written"`
		WriteErrors   int  `json:"write_errors"`
		NotPropagated int  `json:"not_propagated"`
		All           stat `json:"all"`
		ValkeyUp      stat `json:"valkey_up"`
		ValkeyDown    stat `json:"valkey_down"`
		Transition    stat `json:"valkey_transition"`
	} `json:"changes"`
	Jobs struct {
		Enqueued      int  `json:"enqueued"`
		EnqueueErrors int  `json:"enqueue_errors"`
		Unfinished    int  `json:"unfinished"`
		All           stat `json:"all"`
		ValkeyUp      stat `json:"valkey_up"`
		ValkeyDown    stat `json:"valkey_down"`
	} `json:"jobs"`
	FirstErrors map[string]string `json:"first_errors"`
}

// driverLoad is a running `driver load`: a change written and a job enqueued every second.
type driverLoad struct {
	cmd  *exec.Cmd
	out  bytes.Buffer
	errb bytes.Buffer
	done chan error
	name string
}

func startDriverLoad(t testing.TB, name string, d time.Duration) *driverLoad {
	t.Helper()
	l := &driverLoad{name: name, done: make(chan error, 1)}
	l.cmd = exec.Command("kubectl", "--context", cfg.kubeContext, "-n", cfg.namespace, "exec", driverPod, "--", "/driver", "load", "-duration", d.String())
	l.cmd.Stdout, l.cmd.Stderr = &l.out, &l.errb
	if err := l.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { l.done <- l.cmd.Wait() }()
	return l
}

func (l *driverLoad) wait(t testing.TB) loadSummary {
	t.Helper()
	select {
	case err := <-l.done:
		if err != nil {
			t.Fatalf("driver load %s: %v\n%s", l.name, err, l.errb.String())
		}
	case <-time.After(5 * time.Minute):
		t.Fatalf("driver load %s did not finish", l.name)
	}
	_ = os.WriteFile(filepath.Join(cfg.out, "driver-"+l.name+".json"), l.out.Bytes(), 0o644)
	var s loadSummary
	if err := json.Unmarshal(l.out.Bytes(), &s); err != nil {
		t.Fatalf("driver load %s: %v\n%s", l.name, err, l.out.String())
	}
	t.Logf("driver %s: changes written=%d errors=%d not_propagated=%d all=%+v up=%+v down=%+v transition=%+v; jobs enqueued=%d errors=%d unfinished=%d all=%+v up=%+v down=%+v; first errors %v",
		l.name, s.Changes.Written, s.Changes.WriteErrors, s.Changes.NotPropagated, s.Changes.All, s.Changes.ValkeyUp, s.Changes.ValkeyDown, s.Changes.Transition,
		s.Jobs.Enqueued, s.Jobs.EnqueueErrors, s.Jobs.Unfinished, s.Jobs.All, s.Jobs.ValkeyUp, s.Jobs.ValkeyDown, s.FirstErrors)
	return s
}

// ── k6 ───────────────────────────────────────────────────────────────────────

type k6run struct {
	name, container string
	dir             string
	cmd             *exec.Cmd
	done            chan error
	started         time.Time
}

type failure struct {
	At      time.Time
	Status  int
	Replica string
	Path    string
}

type k6result struct {
	Total    int
	Status   map[int]int
	Replicas map[string]int
	Failures []failure
	// Interrupted are requests that were in flight when the harness stopped k6 with SIGINT (the text
	// "context canceled", started within the last two seconds of the run). They are the harness's own doing,
	// not an answer of the system, and are reported but not counted as failures.
	Interrupted []failure
	First       time.Time
	Last        time.Time
	MaxMS       int
	Summary     map[string]any
}

// startK6 runs the load of test/e2e/k6/load.js in a container on the kind Docker network, at
// 100 requests/s through the Ingress, and returns once the first responses are in.
func startK6(t testing.TB, name string, d time.Duration) *k6run {
	t.Helper()
	return startK6Script(t, name, "load.js", d, k6Opts{rate: 100, prefix: "R "})
}

// k6Opts parameterizes startK6Script: the iterations per second, the console lines that show the load runs
// (their prefix) and, for a script that needs a credential, a token that is handed over as the file /out/token
// (never as an argument).
type k6Opts struct {
	rate   int
	prefix string
	token  string
}

// startK6Script runs a script of test/e2e/k6 the way startK6 runs load.js.
func startK6Script(t testing.TB, name, scriptName string, d time.Duration, o k6Opts) *k6run {
	t.Helper()
	k := &k6run{name: name, container: fmt.Sprintf("trove-e2e-k6-%s-%d", name, os.Getpid()), dir: filepath.Join(cfg.out, "k6-"+name), done: make(chan error, 1)}
	if err := os.MkdirAll(k.dir, 0o777); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(k.dir, 0o777)
	if o.token != "" {
		if err := os.WriteFile(filepath.Join(k.dir, "token"), []byte(o.token), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	logf, err := os.Create(filepath.Join(k.dir, "console.log"))
	if err != nil {
		t.Fatal(err)
	}
	args := []string{
		"run", "--rm", "--name", k.container, "--network", "kind",
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"-v", filepath.Join(cfg.repo, "test/e2e/k6") + ":/scripts:ro", "-v", k.dir + ":/out",
		"-e", "TARGET=" + cfg.k6Target, "-e", "HOST=" + cfg.host, "-e", fmt.Sprintf("RATE=%d", o.rate), "-e", "DURATION=" + d.String(), "-e", "TIMEOUT=45s",
	}
	if cfg.k6HostsAddr != "" {
		args = append(args, "-e", "HOSTS_ADDR="+cfg.k6HostsAddr)
	}
	if cfg.caFile != "" {
		args = append(args, "-v", cfg.caFile+":/ca/ca.crt:ro", "-e", "SSL_CERT_FILE=/ca/ca.crt")
	}
	args = append(args, cfg.k6Image, "run", "--log-format", "raw", "--no-usage-report", "/scripts/"+scriptName)
	k.cmd = exec.Command("docker", args...)
	k.cmd.Stdout, k.cmd.Stderr = logf, logf
	if err := k.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	k.started = time.Now()
	go func() { k.done <- k.cmd.Wait(); logf.Close() }()
	t.Cleanup(func() { _ = exec.Command("docker", "kill", k.container).Run() })
	waitFor(t, "k6 to send its first requests", 2*time.Minute, time.Second, func() bool {
		b, _ := os.ReadFile(filepath.Join(k.dir, "console.log"))
		first := 150
		if o.rate < 50 {
			first = 10 * o.rate
		}
		return bytes.Count(b, []byte("\n"+o.prefix)) > first || bytes.HasPrefix(b, []byte(o.prefix)) && bytes.Count(b, []byte("\n")) > first
	})
	return k
}

// stop ends the load with SIGINT, which makes k6 write its summary, and parses the result.
func (k *k6run) stop(t testing.TB) k6result {
	t.Helper()
	_ = exec.Command("docker", "kill", "--signal=INT", k.container).Run()
	select {
	case <-k.done:
	case <-time.After(2 * time.Minute):
		t.Fatalf("k6 %s did not stop", k.name)
	}
	return parseK6(t, k)
}

var linePattern = regexp.MustCompile(`^R (\d+) (\d+) (\S+) (\d+) (\S+)$`)

func parseK6(t testing.TB, k *k6run) k6result {
	t.Helper()
	r := k6result{Status: map[int]int{}, Replicas: map[string]int{}}
	var all []failure
	f, err := os.Open(filepath.Join(k.dir, "console.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		m := linePattern.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		ms, _ := strconv.ParseInt(m[1], 10, 64)
		status, _ := strconv.Atoi(m[2])
		dur, _ := strconv.Atoi(m[4])
		at := time.UnixMilli(ms)
		if status == 200 {
			r.Replicas[m[3]]++
		} else {
			all = append(all, failure{At: at, Status: status, Replica: m[3], Path: m[5]})
		}
		r.Total++
		r.Status[status]++
		if r.First.IsZero() || at.Before(r.First) {
			r.First = at
		}
		if at.After(r.Last) {
			r.Last = at
		}
		r.MaxMS = max(r.MaxMS, dur)
	}
	// stop() ends k6 with SIGINT, which cancels the requests in flight at that moment (about one run in four
	// at 100 requests/s). k6 logs them as status 0 with the error text "context canceled". Only those, and only
	// from the last two seconds of the run, are set aside; every other failure, a connection error included,
	// stays a failure.
	for _, f := range all {
		if f.Status == 0 && strings.HasSuffix(f.Replica, ":context_canceled") && f.At.After(r.Last.Add(-2*time.Second)) {
			r.Interrupted = append(r.Interrupted, f)
			r.Status[0]--
			r.Total--
			continue
		}
		r.Failures = append(r.Failures, f)
	}
	if r.Status[0] == 0 {
		delete(r.Status, 0)
	}
	if b, err := os.ReadFile(filepath.Join(k.dir, "summary.json")); err == nil {
		_ = json.Unmarshal(b, &r.Summary)
	} else {
		t.Errorf("k6 %s wrote no summary.json: %v", k.name, err)
	}
	t.Logf("k6 %s: %d requests over %s, status %v, replicas %v, max %d ms, %d in flight at the stop set aside, k6 summary %v",
		k.name, r.Total, r.Last.Sub(r.First).Round(time.Second), r.Status, r.Replicas, r.MaxMS, len(r.Interrupted), r.Summary)
	return r
}

// only200 fails the test for any response that is not a 200 and for a load that fell behind: at
// 100 requests/s k6 must have sent at least 95 % of what the observed duration allows.
func (r k6result) only200(t testing.TB) {
	t.Helper()
	secs := r.Last.Sub(r.First).Seconds()
	if secs < 20 {
		t.Errorf("k6 ran for %.0f s, too short to say anything", secs)
	}
	if min := int(0.95 * 100 * secs); r.Total < min {
		t.Errorf("k6 sent %d requests in %.0f s, want at least %d", r.Total, secs, min)
	}
	if n := r.Total - r.Status[200]; n != 0 {
		first := r.Failures
		if len(first) > 5 {
			first = first[:5]
		}
		t.Errorf("%d of %d requests were not 200: %v; first: %+v", n, r.Total, r.Status, first)
	}
	if d, _ := r.Summary["dropped_iterations"].(float64); d != 0 {
		t.Errorf("k6 dropped %v iterations: the load fell behind its rate", d)
	}
}

// transferResult is the outcome of transfer.js: every request is a line `T <ms> <op> <status> <replica> <ms> <path> <ok|bad>`.
type transferResult struct {
	Total    int
	Status   map[string]int // "put 201", "get 200", ...
	Bad      int            // downloads whose SHA-256 differs from the upload
	Failures []string       // lines that are no success
	MaxMS    int
	Dropped  float64
}

var transferLine = regexp.MustCompile(`^T (\d+) (put|get) (\d+) (\S+) (\d+) (\S+) (ok|bad)$`)

// stopTransfer ends the load with SIGINT and parses its lines. As in parseK6, a request cut by the harness's own
// stop (status 0, "context canceled", in the last two seconds) is set aside.
func (k *k6run) stopTransfer(t testing.TB) transferResult {
	t.Helper()
	_ = exec.Command("docker", "kill", "--signal=INT", k.container).Run()
	select {
	case <-k.done:
	case <-time.After(2 * time.Minute):
		t.Fatalf("k6 %s did not stop", k.name)
	}
	r := transferResult{Status: map[string]int{}}
	f, err := os.Open(filepath.Join(k.dir, "console.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	type rec struct {
		at     int64
		line   string
		status int
		cancel bool
	}
	var recs []rec
	var last int64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		m := transferLine.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		ms, _ := strconv.ParseInt(m[1], 10, 64)
		status, _ := strconv.Atoi(m[3])
		dur, _ := strconv.Atoi(m[5])
		r.MaxMS = max(r.MaxMS, dur)
		last = max(last, ms)
		recs = append(recs, rec{ms, sc.Text(), status, status == 0 && strings.HasSuffix(m[4], ":context_canceled")})
		if m[7] == "bad" {
			r.Bad++
		}
	}
	for _, c := range recs {
		if c.cancel && c.at > last-2000 {
			continue
		}
		m := transferLine.FindStringSubmatch(c.line)
		r.Status[m[2]+" "+m[3]]++
		r.Total++
		ok := (m[2] == "put" && (c.status == 200 || c.status == 201)) || (m[2] == "get" && c.status == 200)
		if !ok || m[7] == "bad" {
			r.Failures = append(r.Failures, c.line)
		}
	}
	if b, err := os.ReadFile(filepath.Join(k.dir, "summary.json")); err == nil {
		var s struct {
			Dropped float64 `json:"dropped_iterations"`
		}
		_ = json.Unmarshal(b, &s)
		r.Dropped = s.Dropped
	}
	t.Logf("k6 %s: %d transfer requests %v, %d with a wrong checksum, %d failures, max %d ms, dropped iterations %v", k.name, r.Total, r.Status, r.Bad, len(r.Failures), r.MaxMS, r.Dropped)
	return r
}

func noteResult(t testing.TB, name string, v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	_ = os.WriteFile(filepath.Join(cfg.out, name+".json"), b, 0o644)
}

func jsonUnmarshal(s string, v any) error { return json.Unmarshal([]byte(s), v) }
