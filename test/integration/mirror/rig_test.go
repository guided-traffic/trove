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
	// member is the format's member of the repository document (the ansible settings of a hub); a string value holding @LOCALHOST@ is
	// completed with "https://localhost:<port>" of the stub, a second name for it that the certificate carries.
	member map[string]any
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
	doc := remoteDocAt(r.upstream, mode, o.include, o.mirror, remote)
	if o.member != nil {
		var m map[string]any
		if err := json.Unmarshal([]byte(doc), &m); err != nil {
			t.Fatal(err)
		}
		member := map[string]any{}
		for k, v := range o.member {
			if s, ok := v.(string); ok {
				v = strings.ReplaceAll(s, "@LOCALHOST@", "https://localhost:"+srv.Listener.Addr().String()[strings.LastIndex(srv.Listener.Addr().String(), ":")+1:])
			}
			member[k] = v
		}
		m[format] = member
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		doc = string(b)
	}
	st.Repository(repoKey, "remote", format, doc)
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
	return r.st.Do(rep, http.MethodGet, "/api/"+r.format+"/"+testenv.DefaultProject+"/"+repoKey+"/"+path, r.reader, nil)
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
// job is the summary of the attempt, and its creation time has the resolution that the summary's startedAt lacks. While it
// waits it samples the pass's progress, so that a timeout shows whether the pass stood still or only went slowly (F-79).
//
// The spec gives a pass no duration, so the wait is bounded by progress, not by the runner's speed (F-114): it fails when the
// pass's counters and its pending names have stood still for stall, or when the pass has run for passCap in all. A pass that
// stands still fails as soon as it did under the former total bound of the same length; a pass that only goes slowly on a
// loaded host ends.
func (r *rig) waitPass(after time.Time, kind, outcome string, stall time.Duration) {
	r.t.Helper()
	start := time.Now()
	var trail []string
	nextSample := start
	key, moved := r.progressKey(), start
	nextKey := start.Add(time.Second)
	for {
		now := time.Now()
		if now.Sub(moved) >= stall || now.Sub(start) >= passCap {
			break
		}
		n, err := r.st.TryNumber(`SELECT count(*) FROM job WHERE kind = 'mirror-pass' AND state = 'succeeded' AND created_at > $1
			AND result->>'outcome' = $2 AND ($3 = '' OR result->>'kind' = $3)`, after, outcome, kind)
		if err == nil && n >= 1 {
			if took := time.Since(start); took > stall/4 {
				r.t.Logf("the pass ended after %s, a quarter of its stall bound or more; progress, one sample per 5 s:\n%s", took.Round(time.Millisecond), strings.Join(trail, "\n"))
				r.t.Logf("the mirror's rows after the slow pass:\n%s", r.diag())
				for _, rep := range r.st.Replicas() {
					r.t.Logf("warnings and errors in the log of %s:\n%s", rep.Name, logLines(rep.Logs.String(), "warn", "error"))
				}
			}
			return
		}
		if !now.Before(nextSample) {
			trail = append(trail, r.progress("+"+now.Sub(start).Round(time.Second).String()))
			nextSample = now.Add(5 * time.Second)
		}
		if !now.Before(nextKey) {
			if k := r.progressKey(); k != key {
				key, moved = k, now
			}
			nextKey = now.Add(time.Second)
		}
		time.Sleep(100 * time.Millisecond)
	}
	r.t.Logf("progress of the pass, one sample per 5 s:\n%s", strings.Join(trail, "\n"))
	r.t.Logf("the mirror's rows at the timeout:\n%s", r.diag())
	for _, rep := range r.st.Replicas() {
		r.t.Logf("log of %s (last 60 lines):\n%s", rep.Name, testenv.Tail(rep.Logs.String(), 60))
	}
	r.t.Fatalf("a %s pass after %s did not end %s: no progress for %s, or %s in all (it ran %s)", kind, after.Format(time.TimeOnly), outcome,
		stall, passCap, time.Since(start).Round(time.Second))
}

// passCap bounds a pass that keeps making progress: far above the 27 s a full pass of the stub takes alone, below the package
// timeout of the long areas.
const passCap = 10 * time.Minute

// progressKey is what moves while a pass works: its counters, the pending names and the state of the mirror-pass jobs. The
// holder and updated_at are left out, since a heartbeat changes them while the pass stands still.
func (r *rig) progressKey() string {
	k, err := r.st.TryText(`SELECT format('%s/%s/%s/%s/%s/%s/%s', s.pass_names, s.pass_packages_done, s.pass_packages_failed, s.pass_items_stored,
			s.pass_discovery_done, (SELECT count(*) FROM mirror_pending),
			(SELECT string_agg(id::text || ':' || state, ',' ORDER BY id) FROM job WHERE kind = 'mirror-pass'))
		FROM mirror_state s LIMIT 1`)
	if err != nil {
		return "sample failed"
	}
	return k
}

// logLines are the lines of a log that contain one of the markers, in any case.
func logLines(log string, markers ...string) string {
	var out []string
	for _, l := range strings.Split(log, "\n") {
		for _, m := range markers {
			if strings.Contains(strings.ToLower(l), m) {
				out = append(out, l)
				break
			}
		}
	}
	return strings.Join(out, "\n")
}

// dumpOnFailure logs the mirror's rows and the progress of the pass when the test fails at any step, a wait that is not waitPass
// included (F-79). It must be called before the replicas start: the cleanups run last in, first out, so that the rows are read
// after the replicas stopped and before the stack goes.
func (r *rig) dumpOnFailure() {
	r.t.Helper()
	r.t.Cleanup(func() {
		if r.t.Failed() {
			r.t.Logf("the mirror's rows at the failure:\n%s\n%s", r.progress("at the failure:"), r.diag())
		}
	})
}

// progress is one line of the pass's progress: the counters of the state row and the number of pending names. It never fails the
// test (it samples a running system, also from inside subtests): a query that fails is written into the line.
func (r *rig) progress(label string) string {
	line, err := r.st.TryText(`SELECT format('names=%s done=%s failed=%s items=%s discovery_done=%s holder=%s failures=%s updated_at=%s pending=%s',
			pass_names, pass_packages_done, pass_packages_failed, pass_items_stored, pass_discovery_done, COALESCE(left(pass_holder::text, 8), '-'),
			consecutive_failures, updated_at, (SELECT count(*) FROM mirror_pending))
		FROM mirror_state LIMIT 1`)
	if err != nil {
		line = "sample failed: " + err.Error()
	}
	return label + " " + line
}

// diag describes the state of the mirror and of its jobs for a timeout: the state row, every mirror job with its owner, lease,
// attempts, error and times, and the pending names that carry an error count or a wait.
func (r *rig) diag() string {
	var b strings.Builder
	add := func(label, sql string) {
		err := r.st.TryQuery(sql, func(rows pgx.Rows) error {
			var s string
			if err := rows.Scan(&s); err != nil {
				return err
			}
			b.WriteString(label + ": " + s + "\n")
			return nil
		})
		if err != nil {
			b.WriteString(label + ": sample failed: " + err.Error() + "\n")
		}
	}
	add("mirror_state", `SELECT COALESCE(row_to_json(s)::text, '') FROM mirror_state s`)
	add("job", `SELECT row_to_json(x)::text FROM (SELECT id, kind, queue, state, attempt, max_attempts, run_at, locked_by, locked_until, started_at,
		finished_at, created_at, now() AS db_now, progress_done, progress_total, error, result, dedup_key FROM job
		WHERE kind LIKE 'mirror%' ORDER BY created_at) x`)
	add("pending", `SELECT format('%s names, %s with attempts > 0, %s with a wait', count(*), count(*) FILTER (WHERE attempts > 0), count(*) FILTER (WHERE not_before IS NOT NULL)) FROM mirror_pending`)
	add("pending with attempts", `SELECT format('%s attempts=%s queued_at=%s not_before=%s now=%s last_error=%s', name, attempts, queued_at, not_before, now(), last_error)
		FROM mirror_pending WHERE attempts > 0 OR not_before IS NOT NULL ORDER BY name LIMIT 20`)
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
	pairs := []string{`repo="` + repoName + `"`}
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
