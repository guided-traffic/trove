//go:build integration

package mirror

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/app"
	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/mirror"
	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/npmstub"
)

// The real stack of the mirror (E6b.2 to E6b.5): replicas through the composition root with the real host (the remote engine's side,
// mirror.Host), the real npm discovery, and the counting stub of test/npmstub in the test process as the upstream, over TLS.
//
// The public registry. A mirror whose url is https://registry.npmjs.org/ takes the method feed, which reads the replication
// service at replicate.npmjs.com. The replicas reach the stub under both names through app.Options.DialOverride (the names
// connect to the stub's loopback port, certificate and Host header stay those of the names; the egress policy applies to the
// loopback address, which the stack admits as an operator would); the stub tells the two hosts apart by the Host header.

const (
	publicRegistry = "https://registry.npmjs.org/"
	publicHost     = "registry.npmjs.org"
	replicationSvc = "replicate.npmjs.com"
	// upstreamSecretEnv holds the bearer credential of a remote that has one (rigOptions.token).
	upstreamSecretEnv = "TROVE_CAC_MIRROR_IT_UPSTREAM_TOKEN"
)

// hit is one request the stub's listener received.
type hit struct {
	at                              time.Time
	host, method, path, query, auth string
}

func (h hit) replication() bool { return h.host == replicationSvc }

// name is the decoded request path.
func (h hit) name() string {
	p, err := url.PathUnescape(h.path)
	if err != nil {
		return h.path
	}
	return p
}

func (h hit) tarball() bool {
	return strings.HasSuffix(h.path, ".tgz") || strings.HasSuffix(h.path, ".nupkg")
}

// traffic records the requests of the stub with their time and counts the tarball requests in flight.
type traffic struct {
	mu          sync.Mutex
	hits        []hit
	tarballs    int
	peakTarball int
}

func authScheme(header string) string {
	scheme, _, _ := strings.Cut(header, " ")
	if scheme == "" {
		return "none"
	}
	return strings.ToLower(scheme)
}

func (tr *traffic) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/-/stub/") {
			next.ServeHTTP(w, r)
			return
		}
		host := strings.ToLower(r.Host)
		if h, _, err := splitHostPort(host); err == nil {
			host = h
		}
		h := hit{at: time.Now(), host: host, method: r.Method, path: r.URL.EscapedPath(), query: r.URL.RawQuery, auth: authScheme(r.Header.Get("Authorization"))}
		tr.mu.Lock()
		tr.hits = append(tr.hits, h)
		if h.tarball() {
			tr.tarballs++
			tr.peakTarball = max(tr.peakTarball, tr.tarballs)
		}
		tr.mu.Unlock()
		if h.tarball() {
			defer func() { tr.mu.Lock(); tr.tarballs--; tr.mu.Unlock() }()
		}
		next.ServeHTTP(w, r)
	})
}

func splitHostPort(h string) (string, string, error) {
	i := strings.LastIndex(h, ":")
	if i < 0 || strings.Contains(h[i:], "]") {
		return h, "", fmt.Errorf("no port")
	}
	return h[:i], h[i+1:], nil
}

// mark is the number of requests so far; since returns those after it.
func (tr *traffic) mark() int {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return len(tr.hits)
}

func (tr *traffic) since(mark int) []hit {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return append([]hit(nil), tr.hits[mark:]...)
}

// inflight is the number of tarball requests the stub is serving now.
func (tr *traffic) inflight() int {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return tr.tarballs
}

func (tr *traffic) peak() int {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return tr.peakTarball
}

// resetPeak starts the count of the peak afresh from what is in flight now.
func (tr *traffic) resetPeak() {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.peakTarball = tr.tarballs
}

// registryHits and replicationHits split a list of requests by host.
func registryHits(hits []hit) (out []hit) {
	for _, h := range hits {
		if !h.replication() {
			out = append(out, h)
		}
	}
	return out
}

func replicationHits(hits []hit) (out []hit) {
	for _, h := range hits {
		if h.replication() {
			out = append(out, h)
		}
	}
	return out
}

func tarballHits(hits []hit) (out []hit) {
	for _, h := range hits {
		if !h.replication() && h.tarball() {
			out = append(out, h)
		}
	}
	return out
}

// remoteDocAt is the canonical document of a remote repository at url: every member present, as the repository service stores
// it. remote members replace the defaults below by name.
func remoteDocAt(upstream, mode string, include []string, mirrorSettings settings, remote map[string]any) string {
	m := map[string]any{"concurrency": 1}
	for k, v := range mirrorSettings {
		m[k] = v
	}
	if include == nil {
		include = []string{}
	}
	rem := map[string]any{
		"url": upstream, "extraHosts": []any{}, "authMode": "none", "username": "", "password": nil,
		"clientCertificate": nil, "headers": []any{}, "forwardAuthOnRedirect": false, "outboundProxy": map[string]any{"mode": "direct"},
		"connectTimeoutMs": 5000, "readTimeoutMs": 15000, "sourceNetwork": nil, "cacheFillAction": "read",
		"cache": map[string]any{
			"storeArtifacts": true, "metadataTtlSeconds": 600, "metadataRefreshTimeoutSeconds": 10, "notFoundTtlSeconds": 60,
			"maxStaleSeconds": 0, "unusedRetentionHours": 0,
		},
		"integrity":    map[string]any{"checksumPolicy": "verify-if-available", "blockUnexpectedContentTypes": true, "allowedContentTypes": []any{}},
		"availability": map[string]any{"mode": "online", "failureThreshold": 3, "circuitOpenSeconds": 30},
		"limits":       map[string]any{"maxArtifactBytes": nil, "maxMetadataBytes": nil, "maxConcurrentFetches": 16},
		"mode":         mode, "mirror": m,
	}
	for k, v := range remote {
		rem[k] = v
	}
	b, _ := json.Marshal(map[string]any{"includePatterns": include, "excludePatterns": []string{}, "remote": rem})
	return string(b)
}

// rigOptions describe one installation of the real stack.
type rigOptions struct {
	// public points the remote at https://registry.npmjs.org/ (the stub answers as the public registry and as its replication
	// service); false points it at the stub's own address, a private registry.
	public bool
	// mode is the remote's mode; empty is mirror.
	mode    string
	include []string
	mirror  settings
	// remote replaces members of the remote section (limits, cache, availability).
	remote map[string]any
	// token is a bearer credential of the remote.
	token string
	// valkey starts the replicas with the stack's Valkey: the signal mirror_yield needs it.
	valkey bool
	// clock replaces the clock of the engines of the replicas that run passes.
	clock mirror.Clock
	// env is further environment of every replica.
	env []string
	// format is the repository's format: npm (the default, with the counting npm stub) or nuget, whose stub the test passes as
	// handler; indexPath is then the path of its service index below the stub's address.
	format    string
	handler   http.Handler
	indexPath string
}

// rig is one installation: the stack, the stub with its recorder, the remote repository npm-mirror and its people.
type rig struct {
	*env
	t      *testing.T
	format string
	stub   *npmstub.Server
	srv    *httptest.Server
	traf   *traffic
	// url is the stub's own address; upstream is what the remote's url says.
	url, upstream string
	dial          map[string]string
	override      *app.MirrorOverride
	// reader reads through the remote; admin is an administrator.
	reader, admin string
}

func newRig(t *testing.T, o rigOptions) *rig {
	t.Helper()
	format := o.format
	if format == "" {
		format = "npm"
	}
	r := &rig{t: t, format: format, traf: &traffic{}}
	handler := o.handler
	if format == "npm" {
		stub, err := npmstub.New(npmstub.Config{})
		if err != nil {
			t.Fatal(err)
		}
		r.stub, handler = stub, stub
	}
	ss, err := npmstub.NewSelfSigned("localhost", "127.0.0.1", publicHost, replicationSvc)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(r.traf.wrap(handler))
	srv.TLS = ss.ServerConfig()
	srv.StartTLS()
	t.Cleanup(srv.Close)
	r.srv, r.url = srv, srv.URL+"/"
	r.upstream = r.url + o.indexPath
	if o.public {
		r.upstream = publicRegistry
		addr := srv.Listener.Addr().String()
		r.dial = map[string]string{publicHost + ":443": addr, replicationSvc + ":443": addr}
	}
	if o.clock != nil {
		r.override = &app.MirrorOverride{Clock: o.clock}
	}

	ca := testenv.WriteFile(t, t.TempDir(), "stub-ca.pem", string(ss.CAPEM))
	st := testenv.NewStack(t, testenv.StackOptions{Name: "mirrorit", Valkey: o.valkey, Env: append(append([]string{
		"TROVE_PROXY_OUTBOUND_TLS_CA_FILES=[" + strconv.Quote(filepath.ToSlash(ca)) + "]",
		`TROVE_PROXY_OUTBOUND_ADDRESS_POLICY_ALLOW_CIDRS=["127.0.0.1/32"]`,
		"TROVE_PROXY_LOCK_POLL_INTERVAL_MS=50",
		"TROVE_VULNERABILITIES_SOURCES_OSV_MODE=off",
		"TROVE_API_RATE_LIMITS_ENABLED=false",
	}, fastJobs...), o.env...)})

	mode := o.mode
	if mode == "" {
		mode = "mirror"
	}
	remote := map[string]any{}
	for k, v := range o.remote {
		remote[k] = v
	}
	if o.token != "" {
		t.Setenv(upstreamSecretEnv, o.token)
		remote["authMode"] = "bearer"
		remote["password"] = map[string]any{"secretRef": map[string]any{"env": upstreamSecretEnv}}
	}
	st.Repository(repoKey, "remote", format, remoteDocAt(r.upstream, mode, o.include, o.mirror, remote))
	if o.token != "" {
		st.Exec(`INSERT INTO repository_secrets (repo_id, field, secret_ref) SELECT id, '/remote/password', $2::text::jsonb FROM repositories WHERE key = $1`,
			repoKey, `{"env":"`+upstreamSecretEnv+`"}`)
	}
	// The first pass starts when the test says so: until then the state row holds the next pass an hour away, so that the
	// dispatch that the scheduler runs at the start of a replica starts nothing (pass makes it due).
	st.Exec(`INSERT INTO mirror_state (repo_id, next_pass_at) SELECT id, now() + interval '1 hour' FROM repositories WHERE key = $1`, repoKey)
	reader, rita := st.User("reader"), st.User("rita")
	st.Bind("admin", rita)
	st.Policy("readers", []string{"read"}, []string{repoKey}, reader)
	r.reader = st.Token(reader, scope.Default())
	r.admin = st.Token(rita, scope.Scope{Inherit: true, Admin: true})

	db, err := postgres.Open(context.Background(), st.DatabaseConfig(), "mirror-it", "web", testenv.Quiet())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	r.env = &env{t: t, st: st, db: db, store: mirror.NewPGStore(db)}
	r.repoN = st.Number(`SELECT id FROM repositories WHERE key = $1`, repoKey)
	return r
}

func (r *rig) replica(name, role string, extra ...string) *testenv.Replica {
	r.t.Helper()
	return r.st.Start(r.t, testenv.ReplicaOptions{Name: name, Role: role, Env: extra, DialOverride: r.dial, Mirror: r.override})
}

// runner is a process that claims the queue mirror (R-REPO-053, R-HA-062) and the queue maintenance, where the dispatch
// runs (Q-ARCH-321): the suite has no other worker.
func (r *rig) runner(name string, extra ...string) *testenv.Replica {
	r.t.Helper()
	return r.replica(name, "worker", append([]string{`TROVE_JOBS_QUEUES=["mirror","maintenance"]`}, extra...)...)
}

// web is a client-serving process: the package protocols and the management API, no jobs.
func (r *rig) web(name string, extra ...string) *testenv.Replica {
	r.t.Helper()
	return r.replica(name, "web", extra...)
}

// get is a client's GET of a path below the remote's registry URL.
func (r *rig) get(rep *testenv.Replica, path string) testenv.Answer {
	r.t.Helper()
	return r.st.Do(rep, http.MethodGet, "/api/"+r.format+"/"+repoKey+"/"+path, r.reader, nil)
}

func (r *rig) api(rep *testenv.Replica, method, path string, body any, hdr ...string) testenv.Answer {
	r.t.Helper()
	return r.st.Do(rep, method, path, r.admin, body, hdr...)
}

// waitAdmits waits until the replica's snapshot of the repository admits the package name.
func (r *rig) waitAdmits(rep *testenv.Replica, name string) {
	r.t.Helper()
	testenv.WaitFor(r.t, "the snapshot to admit "+name, 30*time.Second, func() bool {
		repo, err := rep.App.Repositories().ByName(context.Background(), testenvName(repoKey))
		return err == nil && repo.Admits(name, false)
	})
}

// publish publishes versions of a package to the stub.
func (r *rig) publish(name string, versions ...string) {
	r.t.Helper()
	for _, v := range versions {
		if err := r.stub.Publish(name, v, nil); err != nil {
			r.t.Fatal(err)
		}
	}
}

// publishMany publishes n packages <scope>/p000 ... with one version each.
func (r *rig) publishMany(scopeName string, n int) []string {
	r.t.Helper()
	var names []string
	for i := range n {
		name := fmt.Sprintf("%s/p%03d", scopeName, i)
		r.publish(name, "1.0.0")
		names = append(names, name)
	}
	return names
}

// setStub sets the failure and throttle modes of the stub.
func (r *rig) setStub(m npmstub.Mode) {
	r.t.Helper()
	if err := r.stub.SetMode(m); err != nil {
		r.t.Fatal(err)
	}
}

// pass starts a pass now: the next pass is made due and the dispatch task runs on the replica.
func (r *rig) pass(rep *testenv.Replica) time.Time {
	r.t.Helper()
	at := time.Now()
	r.makeDue()
	r.dispatch(rep)
	return at
}

// waitPass waits until a mirror-pass job created after the time has ended with the outcome (kind "" is any): the result of the
// job is the summary of the attempt, and its creation time has the resolution that the summary's startedAt lacks.
func (r *rig) waitPass(after time.Time, kind, outcome string, within time.Duration) {
	r.t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		n, err := r.st.TryNumber(`SELECT count(*) FROM job WHERE kind = 'mirror-pass' AND state = 'succeeded' AND created_at > $1
			AND result->>'outcome' = $2 AND ($3 = '' OR result->>'kind' = $3)`, after, outcome, kind)
		if err == nil && n >= 1 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	r.t.Fatalf("a %s pass after %s did not end %s within %s\n%s", kind, after.Format(time.TimeOnly), outcome, within, r.diag())
}

// diag describes the state of the mirror and of its jobs, for a failure message.
func (r *rig) diag() string {
	var b strings.Builder
	r.st.Query(`SELECT COALESCE(row_to_json(s)::text, '') FROM mirror_state s`, func(rows pgx.Rows) error {
		var s string
		if err := rows.Scan(&s); err != nil {
			return err
		}
		b.WriteString("mirror_state: " + s + "\n")
		return nil
	})
	r.st.Query(`SELECT kind || ' ' || state::text || ' ' || COALESCE(error::text, '') || ' ' || COALESCE(result::text, '') FROM job WHERE kind LIKE 'mirror%' ORDER BY created_at`, func(rows pgx.Rows) error {
		var s string
		if err := rows.Scan(&s); err != nil {
			return err
		}
		b.WriteString("job: " + s + "\n")
		return nil
	})
	return b.String()
}

// summary is the last_pass document.
func (r *rig) summary() map[string]any {
	r.t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(r.st.Text(`SELECT COALESCE(last_pass::text, '{}') FROM mirror_state WHERE repo_id = $1`, r.repoN)), &m); err != nil {
		r.t.Fatal(err)
	}
	return m
}

func (r *rig) number(sql string, args ...any) int64 { return r.st.Number(sql, args...) }

// storedItems is the number of artifacts the repository holds.
func (r *rig) storedItems() int64 {
	return r.st.Number(`SELECT count(*) FROM items i JOIN repositories r ON r.id = i.repo_id WHERE r.key = $1 AND (i.path LIKE '%.tgz' OR i.path LIKE '%.nupkg')`, repoKey)
}

func (r *rig) storedItem(path string) bool {
	return r.st.Number(`SELECT count(*) FROM items i JOIN repositories r ON r.id = i.repo_id WHERE r.key = $1 AND i.path = $2`, repoKey, path) == 1
}

// series is the name of a metric series of the mirror repository with its labels: the text format writes them sorted by name.
func series(name string, labels ...string) string {
	pairs := []string{`repo="` + repoKey + `"`}
	for i := 0; i+1 < len(labels); i += 2 {
		pairs = append(pairs, labels[i]+`="`+labels[i+1]+`"`)
	}
	sort.Strings(pairs)
	return name + "{" + strings.Join(pairs, ",") + "}"
}

// metric reads a series of a replica's metrics, 0 when it is absent.
func (r *rig) metric(rep *testenv.Replica, name string, labels ...string) float64 {
	r.t.Helper()
	v := testenv.MetricValue(rep.Metrics(r.t), series(name, labels...))
	if v < 0 {
		return 0
	}
	return v
}

// tarballPath is the path of the tarball of a version, as the plan names it.
func tarballPath(name, version string) string {
	base := name
	if i := strings.LastIndex(name, "/"); i >= 0 {
		base = name[i+1:]
	}
	return name + "/-/" + base + "-" + version + ".tgz"
}

// fakeClock is the engine's clock with an offset on the system's time: a test moves the time of the dispatch and of the waits
// without waiting. Sleeps are real, so that the waits of a pass can be measured against the stub's own times.
type offsetClock struct {
	mu     sync.Mutex
	offset time.Duration
	// slept records every sleep the engine asked for.
	slept []time.Duration
}

func (c *offsetClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Add(c.offset)
}

func (c *offsetClock) advance(d time.Duration) {
	c.mu.Lock()
	c.offset += d
	c.mu.Unlock()
}

func (c *offsetClock) Sleep(ctx context.Context, d time.Duration) error {
	c.mu.Lock()
	c.slept = append(c.slept, d)
	c.mu.Unlock()
	return mirror.SystemClock{}.Sleep(ctx, d)
}

func (c *offsetClock) sleeps() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.slept...)
}
