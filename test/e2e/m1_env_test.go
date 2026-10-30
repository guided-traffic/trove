//go:build e2e

package e2e

// The harness of the M1 tests (E1.1 to E1.14, files e1_*_test.go): an HTTP client for the management API and
// the raw transfer through the edge, the bootstrap of an administrator the way a fresh installation does it
// (docs/developer/identity.md "Bootstrap commands"), the browser login against the test IdP, port-forwards
// for the checks that need a client address of their own, and the Valkey outage of deploy/dev.

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// ── profile ──────────────────────────────────────────────────────────────────

var (
	profileMu      sync.Mutex
	currentProfile = "default" // what the suite last switched the cluster to
)

// runUp runs deploy/dev/up.sh with the profile; the cluster name and the ports come from the environment, as
// make dev-up got them.
func runUp(profile string) error {
	dev := os.Getenv("TROVE_E2E_DEV_DIR")
	if dev == "" {
		return fmt.Errorf("TROVE_E2E_DEV_DIR is not set; run the suite through deploy/dev/e2e.sh")
	}
	cmd := exec.Command(filepath.Join(dev, "up.sh"))
	cmd.Dir = cfg.repo
	cmd.Env = append(os.Environ(), "TROVE_PROFILE="+profile)
	out, err := cmd.CombinedOutput()
	_ = os.WriteFile(filepath.Join(cfg.out, "profile-"+profile+".log"), out, 0o644)
	if err != nil {
		return fmt.Errorf("up.sh with TROVE_PROFILE=%s: %w\n%s", profile, err, tail(string(out), 2000))
	}
	profileMu.Lock()
	currentProfile = profile
	profileMu.Unlock()
	return nil
}

func switchProfile(t testing.TB, profile string) {
	t.Helper()
	if err := runUp(profile); err != nil {
		t.Fatal(err)
	}
}

var (
	scatterOnce sync.Once
	scatterErr  error
)

// useScatter puts the cluster into the scatter profile (three web replicas, every request to another one) for the
// tests of E1.8 to E1.14 and waits for the three replicas. It runs once per process; TestMain returns the cluster
// to the default profile at the end (TROVE_E2E_SCATTER_RETURN=0 keeps it).
func useScatter(t testing.TB) {
	t.Helper()
	scatterOnce.Do(func() {
		if err := runUp("scatter"); err != nil {
			scatterErr = err
			return
		}
		deadline := time.Now().Add(4 * time.Minute)
		for time.Now().Before(deadline) {
			n := 0
			for _, p := range livePods(t, selWeb) {
				if p.Ready {
					n++
				}
			}
			if n == 3 && len(livePods(t, selWeb)) == 3 {
				return
			}
			time.Sleep(2 * time.Second)
		}
		scatterErr = fmt.Errorf("three ready web pods did not appear in the scatter profile")
	})
	if scatterErr != nil {
		t.Fatal(scatterErr)
	}
}

// restoreProfile is TestMain's last step.
func restoreProfile() {
	profileMu.Lock()
	cur := currentProfile
	profileMu.Unlock()
	if cur != "default" && os.Getenv("TROVE_E2E_SCATTER_RETURN") != "0" {
		if err := runUp("default"); err != nil {
			fmt.Fprintln(os.Stderr, "e2e: returning to the default profile:", err)
		}
	}
}

// ── clients ──────────────────────────────────────────────────────────────────

var (
	caPoolOnce sync.Once
	caPoolVal  *x509.CertPool
)

func caPool(t testing.TB) *x509.CertPool {
	t.Helper()
	caPoolOnce.Do(func() {
		pem, err := os.ReadFile(cfg.caFile)
		if err != nil {
			return
		}
		caPoolVal = x509.NewCertPool()
		caPoolVal.AppendCertsFromPEM(pem)
	})
	if caPoolVal == nil {
		t.Fatalf("TROVE_E2E_CA_FILE %q is not readable: the M1 tests need the dev CA", cfg.caFile)
	}
	return caPoolVal
}

func edgePort(t testing.TB) string {
	t.Helper()
	u, err := url.Parse(cfg.edgeURL)
	if err != nil || u.Port() == "" {
		t.Fatalf("TROVE_E2E_EDGE_URL %q has no port", cfg.edgeURL)
	}
	return u.Port()
}

// route says where a connection to a name goes: the edge's published port for every name the edge serves
// (localhost, packages.trove.localhost), the port-forward for the test IdP's Service name, and the table's
// entry for a name that a test pinned (a single pod, a forward).
type route struct {
	mu   sync.Mutex
	pins map[string]string
	rr   []string
	next int
}

func (r *route) dial(t testing.TB) func(ctx context.Context, network, addr string) (net.Conn, error) {
	var d net.Dialer
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, _ := net.SplitHostPort(addr)
		r.mu.Lock()
		target, ok := r.pins[host]
		if !ok && len(r.rr) > 0 && !strings.HasPrefix(host, "trove-keycloak.") {
			target = r.rr[r.next%len(r.rr)]
			r.next++
			ok = true
		}
		r.mu.Unlock()
		switch {
		case ok:
			addr = target
		case strings.HasPrefix(host, "trove-keycloak."):
			addr = keycloakAddr(t)
		default:
			addr = net.JoinHostPort("127.0.0.1", edgePort(t))
		}
		return dialForward(ctx, &d, network, addr)
	}
}

// api is an HTTP client of the edge. It sends a new connection per request, so the scatter profile's round robin
// shows in X-Trove-Replica. Credentials: Bearer token, or the session cookie of its jar with the CSRF header.
type api struct {
	t     testing.TB
	c     *http.Client
	rt    *route
	base  string // https://localhost:<edge port>, the default endpoint
	token string
	csrf  string
}

type apiOption func(*api)

// withJar gives the client cookies, a browser.
func withJar() apiOption {
	return func(a *api) { a.c.Jar, _ = cookiejar.New(nil) }
}

// withRoute makes the client dial the way another client does, round robin position included.
func withRoute(r *route) apiOption { return func(a *api) { a.rt = r } }

// withRoundRobin sends the requests for localhost to the listed host:port addresses in turn (pod port-forwards).
func withRoundRobin(addrs ...string) apiOption {
	return func(a *api) { a.rt.rr = addrs }
}

func newAPI(t testing.TB, opts ...apiOption) *api {
	t.Helper()
	rt := &route{pins: map[string]string{}}
	a := &api{t: t, rt: rt, base: strings.TrimRight(cfg.edgeURL, "/")}
	a.c = &http.Client{
		Timeout: 3 * time.Minute,
		// A redirect is an answer under test (login, logout, an exposure 404 never is one): follow none.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			DisableKeepAlives: true,
			// the route is looked up per connection: an option may replace it (withRoute) and copies of the client
			// (withToken) share it, so a round robin goes on where the original stopped
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return a.rt.dial(t)(ctx, network, addr)
			},
			TLSClientConfig: &tls.Config{RootCAs: caPool(t), MinVersion: tls.VersionTLS12},
		},
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

// withToken returns a copy of the client that presents the token (no cookies).
func (a *api) withToken(tok string) *api {
	b := *a
	b.token = tok
	b.csrf = ""
	b.c = &http.Client{Timeout: a.c.Timeout, CheckRedirect: a.c.CheckRedirect, Transport: a.c.Transport}
	return &b
}

type reply struct {
	Status int
	Header http.Header
	Body   []byte
	Dur    time.Duration
}

func (r reply) Replica() string { return r.Header.Get("X-Trove-Replica") }

func (r reply) String() string {
	b := r.Body
	if len(b) > 400 {
		b = b[:400]
	}
	return fmt.Sprintf("%d %s", r.Status, strings.TrimSpace(string(b)))
}

// Code is the code member of a problem+json body ("" when the body is no problem).
func (r reply) Code() string {
	var p struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(r.Body, &p)
	return p.Code
}

// FieldCode is the code of the first entry of errors[]: a 422 carries the convention
// code validation_failed at the top and the resource's own code per field (R-API-009,
// Q-API-136).
func (r reply) FieldCode() string {
	var p struct {
		Errors []struct {
			Code string `json:"code"`
		} `json:"errors"`
	}
	_ = json.Unmarshal(r.Body, &p)
	if len(p.Errors) == 0 {
		return ""
	}
	return p.Errors[0].Code
}

func (r reply) JSON(t testing.TB, v any) {
	t.Helper()
	if err := json.Unmarshal(r.Body, v); err != nil {
		t.Fatalf("the body is no JSON of the expected shape (%v): %s", err, r)
	}
}

// want fails the test unless the status is one of the given.
func (r reply) want(t testing.TB, what string, statuses ...int) reply {
	t.Helper()
	for _, s := range statuses {
		if r.Status == s {
			return r
		}
	}
	t.Fatalf("%s: %s, want %v", what, r, statuses)
	return r
}

func unsafeMethod(m string) bool {
	return m == http.MethodPost || m == http.MethodPut || m == http.MethodPatch || m == http.MethodDelete
}

// send performs a request; body is nil, []byte, string, io.Reader or a value that is sent as JSON. h holds
// header name and value pairs. A path starting with https:// is used as it is.
func (a *api) send(method, path string, body any, h ...string) (reply, error) {
	var rd io.Reader
	ctype := ""
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
	case string:
		rd = strings.NewReader(b)
	case io.Reader:
		rd = b
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			return reply{}, err
		}
		rd, ctype = bytes.NewReader(raw), "application/json"
	}
	target := path
	if !strings.HasPrefix(path, "https://") {
		target = a.base + path
	}
	req, err := http.NewRequestWithContext(context.Background(), method, target, rd)
	if err != nil {
		return reply{}, err
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}
	if a.csrf != "" && unsafeMethod(method) {
		req.Header.Set("X-CSRF-Token", a.csrf)
	}
	if unsafeMethod(method) && (a.c.Jar != nil) {
		req.Header.Set("Origin", a.base)
	}
	for i := 0; i+1 < len(h); i += 2 {
		if strings.EqualFold(h[i], "Host") {
			req.Host = h[i+1]
			continue
		}
		req.Header.Set(h[i], h[i+1])
	}
	start := time.Now()
	resp, err := a.c.Do(req)
	if err != nil {
		return reply{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return reply{}, err
	}
	return reply{Status: resp.StatusCode, Header: resp.Header, Body: raw, Dur: time.Since(start)}, nil
}

func (a *api) do(method, path string, body any, h ...string) reply {
	a.t.Helper()
	r, err := a.send(method, path, body, h...)
	if err != nil {
		a.t.Fatalf("%s %s: %v", method, path, err)
	}
	return r
}

func (a *api) get(path string, h ...string) reply {
	a.t.Helper()
	return a.do(http.MethodGet, path, nil, h...)
}

// cookie returns a cookie of the jar for the default endpoint.
func (a *api) cookie(name string) string {
	if a.c.Jar == nil {
		return ""
	}
	u, _ := url.Parse(a.base)
	for _, c := range a.c.Jar.Cookies(u) {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

// syncCSRF takes the CSRF token the UI would read from its cookie.
func (a *api) syncCSRF() { a.csrf = a.cookie("__Host-trv_csrf") }

// ── bootstrap ────────────────────────────────────────────────────────────────

// troveAdmin runs `trove admin …` in a worker pod, as the bootstrap of a fresh installation does. stdout is
// returned; stderr (the JSON log lines of the command) is only logged on failure.
func troveAdmin(t testing.TB, stdin string, args ...string) (string, error) {
	t.Helper()
	full := append([]string{"--context", cfg.kubeContext, "-n", cfg.namespace, "exec", "-i", "deploy/trove-worker", "--", "trove", "admin"}, args...)
	cmd := exec.Command("kubectl", full...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("trove admin %s: %w: %s", strings.Join(args, " "), err, tail(errb.String(), 600))
	}
	return strings.TrimSpace(out.String()), nil
}

var (
	adminOnce sync.Once
	adminTok  string
	adminErr  error
)

// adminToken is the token of the service account `e2e-admin`, created with `trove admin service-account create
// --admin` and `trove admin token create` the way a fresh installation is bootstrapped. The account is kept
// between runs; every run mints a new token (24 h).
func adminToken(t testing.TB) string {
	t.Helper()
	adminOnce.Do(func() {
		if _, err := troveAdmin(t, "", "service-account", "create", "e2e-admin", "--description", "end-to-end tests", "--admin"); err != nil && !strings.Contains(err.Error(), "exist") {
			adminErr = err
			return
		}
		adminTok, adminErr = troveAdmin(t, `{"inherit":true,"admin":true}`, "token", "create", "--service-account", "e2e-admin", "--ttl", "24h", "--scope-file", "/dev/stdin")
		if adminErr != nil {
			return
		}
		a := newAPI(t).withToken(adminTok)
		// The command binds the role in PostgreSQL; every web replica learns of it through
		// the change log within R-AUTHZ-028's bound. Wait until the binding is in force on
		// all of them, as an administrator who read the command's result would.
		deadline := time.Now().Add(15 * time.Second)
		for inForce := 0; inForce < 6; {
			var me struct {
				Roles []string `json:"roles"`
			}
			if r := a.get("/api/v1/authz/me"); r.Status == 200 && json.Unmarshal(r.Body, &me) == nil && slices.Contains(me.Roles, "admin") {
				inForce++
				continue
			}
			inForce = 0
			if time.Now().After(deadline) {
				adminErr = fmt.Errorf("the admin binding of e2e-admin is not in force on every replica after 15 s")
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
		// Tokens of earlier runs would reach the account's limit of active tokens: revoke them.
		var cur struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(a.get("/api/v1/tokens/current").Body, &cur)
		var l struct {
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
		}
		if r := a.get("/api/v1/tokens?owner=sa:e2e-admin&state=active&limit=500"); r.Status == 200 && json.Unmarshal(r.Body, &l) == nil {
			for _, it := range l.Items {
				if it.ID != cur.ID {
					a.do(http.MethodDelete, "/api/v1/tokens/"+it.ID, nil)
				}
			}
		}
	})
	if adminErr != nil {
		t.Fatalf("bootstrap administrator: %v", adminErr)
	}
	return adminTok
}

func adminAPI(t testing.TB) *api { t.Helper(); return newAPI(t).withToken(adminToken(t)) }

// ensureRepo creates a repository of the key when it does not exist.
func ensureRepo(t testing.TB, adm *api, key string) {
	t.Helper()
	r := adm.do(http.MethodPost, "/api/v1/repositories", map[string]string{"key": key, "class": "local", "format": "generic"})
	r.want(t, "create repository "+key, 201, 409)
}

// ensureSA creates a service account and returns nothing; an existing one is fine.
func ensureSA(t testing.TB, adm *api, name string) {
	t.Helper()
	adm.do(http.MethodPost, "/api/v1/service-accounts", map[string]string{"name": name, "description": "end-to-end tests"}).want(t, "create service account "+name, 201, 409)
}

// ensurePolicy writes a policy (create or replace).
func ensurePolicy(t testing.TB, adm *api, name string, repos []string, entries ...[2]any) {
	t.Helper()
	var es []map[string]any
	for _, e := range entries {
		es = append(es, map[string]any{"principal": e[0], "actions": e[1]})
	}
	body := map[string]any{"name": name, "description": "end-to-end tests", "resources": map[string]any{"repositories": repos}, "entries": es}
	r := adm.do(http.MethodPost, "/api/v1/permission-policies", body)
	if r.Status == 409 {
		g := adm.get("/api/v1/permission-policies/"+name).want(t, "read policy "+name, 200)
		r = adm.do(http.MethodPut, "/api/v1/permission-policies/"+name, body, "If-Match", g.Header.Get("ETag"))
	}
	r.want(t, "write policy "+name, 200, 201)
}

type createdToken struct {
	ID    string `json:"id"`
	Token string `json:"token"`
}

// newSAToken mints a token of a service account through the management API. scope is the token scope document.
func newSAToken(t testing.TB, adm *api, sa string, ttl int, scope map[string]any) createdToken {
	t.Helper()
	r := adm.do(http.MethodPost, "/api/v1/service-accounts/"+sa+"/tokens", map[string]any{"description": "end-to-end test", "ttlSeconds": ttl, "scope": scope})
	r.want(t, "token of "+sa, 201)
	var c createdToken
	r.JSON(t, &c)
	return c
}

// ── secrets, pods, metrics ───────────────────────────────────────────────────

func secretValue(t testing.TB, secret, key string) string {
	t.Helper()
	out := kn(t, "get", "secret", secret, "-o", "jsonpath={.data."+strings.ReplaceAll(key, ".", "\\.")+"}")
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(out))
	if err != nil || len(raw) == 0 {
		t.Fatalf("secret %s has no field %s", secret, key)
	}
	return string(raw)
}

func readyWebPods(t testing.TB) []string {
	t.Helper()
	var out []string
	for _, p := range livePods(t, selWeb) {
		if p.Ready {
			out = append(out, p.Name)
		}
	}
	return out
}

// waitWebSettled waits until a rollout of the web Deployment is over for the edge as well: exactly the expected
// pods, all ready, none terminating (a terminating pod keeps serving during its drain, with the old configuration).
func waitWebSettled(t testing.TB) {
	t.Helper()
	want, _ := strconv.Atoi(strings.TrimSpace(kn(t, "get", "deployment", "trove-web", "-o", "jsonpath={.spec.replicas}")))
	waitFor(t, "the web pods to settle after a rollout", 5*time.Minute, 2*time.Second, func() bool {
		ps := podsOf(t, selWeb)
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
	time.Sleep(3 * time.Second) // the edge learns the endpoint changes within a second or two
}

// metricsOf parses a pod's /metrics into series (name{labels}) and values.
func metricsOf(t testing.TB, podName string) map[string]float64 {
	t.Helper()
	code, body := ops(podName, "/metrics")
	if code != 200 {
		t.Fatalf("%s :8081/metrics = %d", podName, code)
	}
	m := map[string]float64{}
	for _, l := range strings.Split(body, "\n") {
		if l == "" || l[0] == '#' {
			continue
		}
		i := strings.LastIndex(l, " ")
		if i < 0 {
			continue
		}
		v, err := strconv.ParseFloat(l[i+1:], 64)
		if err == nil {
			m[l[:i]] = v
		}
	}
	return m
}

// sumSeries adds the values of the series of every web pod whose text starts with prefix and holds every part.
func sumSeries(t testing.TB, prefix string, parts ...string) float64 {
	t.Helper()
	var sum float64
	for _, p := range readyWebPods(t) {
		for s, v := range metricsOf(t, p) {
			if !strings.HasPrefix(s, prefix) {
				continue
			}
			ok := true
			for _, part := range parts {
				ok = ok && strings.Contains(s, part)
			}
			if ok {
				sum += v
			}
		}
	}
	return sum
}

// ── Valkey ───────────────────────────────────────────────────────────────────

func devScript(t testing.TB, name string, args ...string) string {
	t.Helper()
	return script(t, name, args...)
}

// withValkeyStopped runs f while Valkey and the Sentinels are down (deploy/dev/valkey-stop.sh) and starts them
// again afterwards, whatever f does.
func withValkeyStopped(t *testing.T, f func()) {
	t.Helper()
	devScript(t, "valkey-stop.sh")
	defer func() { devScript(t, "valkey-start.sh") }()
	f()
}

// valkeyPrimaryCLI runs a command on the Valkey primary (deploy/dev/valkey-cli.sh).
func valkeyPrimaryCLI(t testing.TB, args ...string) string {
	t.Helper()
	return script(t, "valkey-cli.sh", args...)
}

// webReadyDegraded checks the /readyz of every web pod: 200 and, with want true, "valkey" among the degraded.
func webReadyDegraded(t testing.TB, want bool) {
	t.Helper()
	if want {
		// the health check needs a few of its intervals to see Valkey go (health.check_interval 5 s, two failures)
		waitFor(t, "every web pod to report valkey as degraded", 90*time.Second, 2*time.Second, func() bool {
			for _, p := range readyWebPods(t) {
				code, body := ops(p, "/readyz")
				if code != 200 || !strings.Contains(body, `"valkey"`) {
					return false
				}
			}
			return true
		})
	} else {
		waitFor(t, "every web pod to drop valkey from the degraded checks", 90*time.Second, 2*time.Second, func() bool {
			for _, p := range readyWebPods(t) {
				if code, body := ops(p, "/readyz"); code != 200 || strings.Contains(body, `"valkey"`) {
					return false
				}
			}
			return true
		})
	}
	for _, p := range readyWebPods(t) {
		code, body := ops(p, "/readyz")
		if code != 200 {
			t.Errorf("%s /readyz = %d %s", p, code, strings.TrimSpace(body))
			continue
		}
		has := strings.Contains(body, `"valkey"`) && strings.Contains(body, "degraded")
		if has != want {
			t.Errorf("%s /readyz = %s: degraded valkey is %v, want %v", p, strings.TrimSpace(body), has, want)
		}
	}
}

// ── port-forwards ────────────────────────────────────────────────────────────

// A `kubectl port-forward` ends for good on the first connection through it that fails upstream ("lost connection to
// pod": the kubelet could not reach the port inside the pod) and when its stream to the kubelet breaks, whatever the
// cause (a pod that went away, a busy node, the API server). The tests hand its local address to clients, so a forward
// is supervised: when the kubectl process ends on its own it is started again on the same local port, the end and its
// stderr go to the artifact directory, and a client that finds the port closed meanwhile (connection refused, no byte
// of its request sent) waits for the new process instead of failing (dialForward).

var (
	fwdMu    sync.Mutex
	fwds     []*forward
	fwdAddrs = map[string]*forward{} // local address -> forward
	kcOnce   sync.Once
	kcFwd    string
)

var forwardingLine = regexp.MustCompile(`127\.0\.0\.1:(\d+) ->`)

// forwardRestartWithin is how long a client waits for a forward whose kubectl process ended to listen again.
const forwardRestartWithin = 30 * time.Second

// forward is a supervised `kubectl port-forward` to one pod or Service port.
type forward struct {
	target string
	port   int
	addr   string // 127.0.0.1:<local port>, fixed after the first start

	mu       sync.Mutex
	cmd      *exec.Cmd
	stopping bool
	ups      int    // processes started so far
	lastErr  string // stderr of the last process that ended on its own, or why a restart failed
}

// start launches one kubectl process on the local port ("" lets kubectl pick one) and returns the address it reports.
func (f *forward) start(local string) (string, error) {
	cmd := exec.Command("kubectl", "--context", cfg.kubeContext, "-n", cfg.namespace, "port-forward", "--address", "127.0.0.1", f.target, local+":"+strconv.Itoa(f.port))
	out, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return "", err
	}
	exited, decided := make(chan struct{}), make(chan struct{})
	defer close(decided)
	lines := make(chan string, 1)
	go func() {
		buf := make([]byte, 512)
		n, _ := out.Read(buf)
		lines <- string(buf[:n])
		_, _ = io.Copy(io.Discard, out)
	}()
	go func() {
		// Wait closes the pipe; the reader above has taken the forwarding line by then or the process never printed it.
		_ = cmd.Wait()
		close(exited)
		<-decided // start has either taken the process as running or given it up
		f.ended(cmd, stderr.String())
	}()
	select {
	case l := <-lines:
		m := forwardingLine.FindStringSubmatch(l)
		if m == nil {
			_ = cmd.Process.Kill()
			<-exited
			return "", fmt.Errorf("unexpected output %q, stderr %q", l, tail(stderr.String(), 400))
		}
		f.mu.Lock()
		f.cmd = cmd
		f.ups++
		f.mu.Unlock()
		return "127.0.0.1:" + m[1], nil
	case <-exited:
		return "", fmt.Errorf("kubectl port-forward ended at start: %s", tail(stderr.String(), 400))
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		<-exited
		return "", fmt.Errorf("did not start within 30s")
	}
}

// ended runs when a kubectl process ended: an end the harness did not ask for is recorded and restarted.
func (f *forward) ended(cmd *exec.Cmd, stderr string) {
	f.mu.Lock()
	if f.cmd != cmd || f.stopping {
		f.mu.Unlock()
		return
	}
	f.lastErr = strings.TrimSpace(stderr)
	n := f.ups
	f.mu.Unlock()
	name := fmt.Sprintf("portforward-%s-%d.log", strings.NewReplacer("/", "_", ":", "_").Replace(f.target+"-"+f.addr), n)
	_ = os.WriteFile(filepath.Join(cfg.out, name), []byte(fmt.Sprintf("%s the kubectl port-forward to %s on %s ended (start %d): %s\n", time.Now().Format(time.RFC3339), f.target, f.addr, n, f.lastErr)), 0o644)
	go f.restart()
}

// restart starts the process again on the old local port until it listens or the forward is stopped.
func (f *forward) restart() {
	_, local, _ := net.SplitHostPort(f.addr)
	for {
		f.mu.Lock()
		stopping := f.stopping
		f.mu.Unlock()
		if stopping {
			return
		}
		_, err := f.start(local)
		if err == nil {
			return
		}
		f.mu.Lock()
		f.lastErr = err.Error()
		f.mu.Unlock()
		time.Sleep(500 * time.Millisecond)
	}
}

func (f *forward) stop() {
	f.mu.Lock()
	f.stopping = true
	cmd := f.cmd
	f.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

// portForward starts `kubectl port-forward <target> :<port>` and returns the supervised forward, whose addr is the
// local address.
func portForward(t testing.TB, target string, port int) *forward {
	t.Helper()
	f := &forward{target: target, port: port}
	addr, err := f.start("")
	if err != nil {
		t.Fatalf("port-forward %s: %v", target, err)
	}
	f.addr = addr
	fwdMu.Lock()
	fwds = append(fwds, f)
	fwdAddrs[addr] = f
	fwdMu.Unlock()
	return f
}

func stopForward(f *forward) {
	if f == nil {
		return
	}
	f.stop()
	fwdMu.Lock()
	delete(fwdAddrs, f.addr)
	fwdMu.Unlock()
}

// dialForward dials addr. When addr is a forward whose kubectl process has ended, a refused connection waits for the
// restarted process (nothing of the request was sent, so waiting changes no count); when the forward does not come
// back the error carries the stderr of the process that ended.
func dialForward(ctx context.Context, d *net.Dialer, network, addr string) (net.Conn, error) {
	fwdMu.Lock()
	f := fwdAddrs[addr]
	fwdMu.Unlock()
	deadline := time.Now().Add(forwardRestartWithin)
	for {
		c, err := d.DialContext(ctx, network, addr)
		if err == nil || f == nil || !errors.Is(err, syscall.ECONNREFUSED) {
			return c, err
		}
		if time.Now().After(deadline) {
			f.mu.Lock()
			defer f.mu.Unlock()
			return nil, fmt.Errorf("%w; the kubectl port-forward to %s (%d starts) did not come back within %s: %s", err, f.target, f.ups, forwardRestartWithin, f.lastErr)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// keycloakAddr is the local address of a port-forward to the test IdP's Service (kept for the whole run).
func keycloakAddr(t testing.TB) string {
	kcOnce.Do(func() { kcFwd = portForward(t, "svc/trove-keycloak", 8443).addr })
	return kcFwd
}

func stopForwards() {
	fwdMu.Lock()
	all := fwds
	fwds, fwdAddrs = nil, map[string]*forward{}
	fwdMu.Unlock()
	for _, f := range all {
		f.stop()
	}
}

// podForwards opens a port-forward to the client listener of every ready web pod and returns the local addresses
// (in pod order) and a function that closes them. A client built with withRoundRobin(addrs...) sends its
// requests to the pods in turn, bypassing the edge. The client address the replicas see is the one of the
// forward (127.0.0.1 inside the pod), a bucket of its own for the per-address limits, so the threshold tests do
// not spend the budget of the edge address (the host behind the edge), which every other test shares. The pods'
// certificate names localhost.
func podForwards(t testing.TB) ([]string, func()) { return podForwardsPort(t, 8080) }

// podForwardsPort is podForwards for another listener of the pods (8443 is the endpoint customers).
func podForwardsPort(t testing.TB, port int) ([]string, func()) {
	t.Helper()
	var addrs []string
	var fs []*forward
	for _, p := range readyWebPods(t) {
		f := portForward(t, "pod/"+p, port)
		addrs = append(addrs, f.addr)
		fs = append(fs, f)
	}
	if len(addrs) < 2 {
		t.Fatalf("only %d ready web pods", len(addrs))
	}
	return addrs, func() {
		for _, f := range fs {
			stopForward(f)
		}
	}
}

// ── the test IdP ─────────────────────────────────────────────────────────────

var kcForm = regexp.MustCompile(`(?s)<form[^>]*id="kc-form-login"[^>]*action="([^"]+)"`)

// browser is a client with cookies that signs in the way a browser does.
type browser struct {
	*api
}

func newBrowser(t testing.TB) *browser { return &browser{newAPI(t, withJar())} }

type loginTrace struct {
	Hops          []string
	AuthURL       *url.URL // the redirect to the IdP
	SawLoginForm  bool
	StartReply    reply // the answer of /auth/oidc/<id>/start
	CallbackReply reply // the answer of /auth/oidc/<id>/callback
	Final         reply
}

// oidcLogin walks the authorization code flow: GET start, the IdP's login form (posted with the user's
// password when it shows), the redirect to the callback, trove's redirect into the UI. A fresh browser sees
// the form; one that holds an IdP session does not.
func (b *browser) oidcLogin(user, password string) loginTrace {
	b.t.Helper()
	return b.walkLogin(b.base+"/auth/oidc/keycloak/start", user, password)
}

// walkLogin follows a flow from the URL (a start, with or without purpose=reauth) to the page it ends on.
func (b *browser) walkLogin(first, user, password string) loginTrace {
	b.t.Helper()
	var tr loginTrace
	next := first
	method, body := http.MethodGet, io.Reader(nil)
	var hdr []string
	for hop := 0; hop < 20; hop++ {
		tr.Hops = append(tr.Hops, method+" "+next)
		r, err := b.send(method, next, body, hdr...)
		if err != nil {
			b.t.Fatalf("login hop %d %s %s: %v", hop, method, next, err)
		}
		method, body, hdr = http.MethodGet, nil, nil
		cur, _ := url.Parse(next)
		if hop == 0 {
			tr.StartReply = r
		}
		if strings.Contains(next, "/auth/oidc/keycloak/callback") {
			tr.CallbackReply = r
		}
		if r.Status >= 300 && r.Status < 400 {
			loc, err := url.Parse(r.Header.Get("Location"))
			if err != nil || loc.String() == "" {
				b.t.Fatalf("login hop %d: redirect %d without a usable Location", hop, r.Status)
			}
			abs := cur.ResolveReference(loc)
			if tr.AuthURL == nil && strings.HasPrefix(abs.Host, "trove-keycloak.") {
				tr.AuthURL = abs
			}
			next = abs.String()
			continue
		}
		if m := kcForm.FindSubmatch(r.Body); m != nil {
			tr.SawLoginForm = true
			action := cur.ResolveReference(mustURL(b.t, htmlUnescape(string(m[1]))))
			form := url.Values{"username": {user}, "password": {password}, "credentialId": {""}}
			next, method, body = action.String(), http.MethodPost, strings.NewReader(form.Encode())
			hdr = []string{"Content-Type", "application/x-www-form-urlencoded"}
			continue
		}
		tr.Final = r
		b.syncCSRF()
		return tr
	}
	b.t.Fatalf("login did not finish in 20 hops: %v", tr.Hops)
	return tr
}

func mustURL(t testing.TB, s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		t.Fatalf("bad URL %q: %v", s, err)
	}
	return u
}

func htmlUnescape(s string) string {
	return strings.NewReplacer("&amp;", "&", "&#61;", "=", "&#43;", "+", "&quot;", `"`, "&#39;", "'").Replace(s)
}

// localLogin posts the break-glass credentials.
func (b *browser) localLogin(user, password string) reply {
	b.t.Helper()
	r := b.do(http.MethodPost, "/auth/local/login", map[string]string{"username": user, "password": password})
	if r.Status == 204 {
		b.syncCSRF()
	}
	return r
}

// sessionEnded checks that a session cookie is dead on every replica within auth.revocation_bound (2 s, R-API-052):
// the ending reaches the other replicas through the change log, so a request in the first moments may still be
// served from a cache that has not seen it. The loop is paced (every 401 would otherwise count against the
// failed-credential budget of R-AUTH-038) and returns when it has seen the cookie refused on every replica.
func sessionEnded(t testing.TB, cookie string) {
	t.Helper()
	stale := newAPI(t)
	start := time.Now()
	var lastOK time.Duration
	refused := map[string]bool{}
	for time.Since(start) < 4*time.Second {
		r := stale.get("/api/v1/auth/whoami", "Cookie", "__Host-trv_session="+cookie)
		switch r.Status {
		case 200:
			lastOK = time.Since(start)
		case 401:
			refused[r.Replica()] = true
		default:
			t.Fatalf("a request with the cookie of an ended session = %s", r)
		}
		if len(refused) >= 3 && time.Since(start) > lastOK+400*time.Millisecond {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Logf("the ended session: last accepted %s after the call that ended it, refused by %v", lastOK.Round(10*time.Millisecond), sortedKeys(refused))
	if lastOK > 2*time.Second {
		t.Errorf("a replica accepted the ended session %s after it ended (auth.revocation_bound is 2 s)", lastOK)
	}
	if len(refused) != 3 {
		t.Errorf("the ended session was refused by %v, want all three replicas", sortedKeys(refused))
	}
}

// whoami is the principal of the client's credential.
func (a *api) whoami() (reply, string) {
	a.t.Helper()
	r := a.get("/api/v1/auth/whoami")
	var w struct {
		Principal struct {
			Name   string   `json:"name"`
			Kind   string   `json:"kind"`
			Groups []string `json:"groups"`
		} `json:"principal"`
	}
	_ = json.Unmarshal(r.Body, &w)
	return r, w.Principal.Name
}

// ── lists, helpers ───────────────────────────────────────────────────────────

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// percentile of durations (the slice is sorted in place).
func percentile(d []time.Duration, p float64) time.Duration {
	if len(d) == 0 {
		return 0
	}
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	i := int(float64(len(d)-1) * p)
	return d[i]
}

// blast sends n requests with workers goroutines and returns the replies in completion order and the elapsed time.
func blast(a *api, n, workers int, mk func(i int) (method, path string, body any, h []string)) ([]reply, time.Duration) {
	jobs := make(chan int)
	out := make([]reply, 0, n)
	var mu sync.Mutex
	var wg sync.WaitGroup
	start := time.Now()
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				m, p, b, h := mk(i)
				r, err := a.send(m, p, b, h...)
				if err != nil {
					r = reply{Status: 0, Header: http.Header{}, Body: []byte(err.Error())}
				}
				mu.Lock()
				out = append(out, r)
				mu.Unlock()
			}
		}()
	}
	for i := 0; i < n; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return out, time.Since(start)
}

func countStatus(rs []reply) map[int]int {
	m := map[int]int{}
	for _, r := range rs {
		m[r.Status]++
	}
	return m
}

func replicaSet(rs []reply, status int) map[string]int {
	m := map[string]int{}
	for _, r := range rs {
		if r.Status == status {
			m[r.Replica()]++
		}
	}
	return m
}
