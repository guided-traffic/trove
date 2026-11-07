//go:build integration

// Package vulnsync tests the advisory sync of the vulnerability scanning (R-VULN-003 to R-VULN-008) against the integration
// stack and the test advisory source of test/osvstub: a TLS server in the test process that the worker replica trusts and
// may reach, whose request log says what the sync asked of it.
//
// The replicas run with the adapters of the formats (npm and NuGet), as production does.
package vulnsync

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/osvstub"
)

// The sync tasks and the names of the repositories.
const (
	taskOSV     = "vuln-sync-osv"
	taskOSVFull = "vuln-sync-osv-full"
	taskKEV     = "vuln-sync-kev"
	mirrorRepo  = "vuln-mirror"

	// mirrorName is the qualified name the configuration takes (R-VULN-025).
	mirrorName  = testenv.DefaultProject + "/" + mirrorRepo
	kevPathName = "/sites/default/files/feeds/known_exploited_vulnerabilities.json"
)

// env is one installation with a web replica for the management API, a worker replica that runs the jobs, and the test
// advisory source.
type env struct {
	t     *testing.T
	st    *testenv.Stack
	stub  *osvstub.Stub
	srv   *httptest.Server
	admin string

	web    *testenv.Replica
	worker *testenv.Replica
}

// options are the settings a test changes.
type options struct {
	// Env is further environment of the replicas.
	Env []string
	// Filler is the number of synthetic records per ecosystem the source holds besides the fixture, so that percentages of
	// records are exact.
	Filler int
	// Valkey: the replicas use the stack's Valkey; false is "Valkey stopped".
	Valkey bool
	// NoWorker does not start the worker, for a test that builds its own.
	NoWorker bool
}

// taskOff is the configuration that keeps the scheduler from enqueueing the runs of the sync by itself: a worker enqueues the
// latest due slot of its tasks when it starts, and the tests decide when a run happens.
const taskOff = `{"vuln-sync-osv":{"enabled":false,"schedule":"*/15 * * * *"},"vuln-sync-osv-full":{"enabled":false,"schedule":"17 3 * * 0"},` +
	`"vuln-sync-kev":{"enabled":false,"schedule":"41 */6 * * *"}}`

func newEnv(t *testing.T, o options) *env {
	t.Helper()
	stub := osvstub.New(osvstub.Config{Filler: o.Filler})
	ss, err := osvstub.NewSelfSigned("localhost", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(stub)
	srv.TLS = ss.ServerConfig()
	srv.StartTLS()
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	ca := filepath.Join(dir, "osv-ca.pem")
	if err := os.WriteFile(ca, ss.CAPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	st := testenv.NewStack(t, testenv.StackOptions{Name: "vulnsync", Valkey: o.Valkey, Env: append([]string{
		"TROVE_PROXY_OUTBOUND_TLS_CA_FILES=[" + strconv.Quote(ca) + "]",
		`TROVE_PROXY_OUTBOUND_ADDRESS_POLICY_ALLOW_CIDRS=["127.0.0.1/32","::1/128"]`,
		"TROVE_VULNERABILITIES_SOURCES_OSV_BASE_URL=" + srv.URL + "/",
		"TROVE_VULNERABILITIES_SOURCES_KEV_URL=" + srv.URL + kevPathName,
		"TROVE_MAINTENANCE_TASKS=" + taskOff,
		"TROVE_CLUSTER_EPOCH_POLL_INTERVAL=500ms", "TROVE_JOBS_POLL_INTERVAL=200ms",
		// The sync kinds run on the maintenance queue and the evaluation on the default queue: with no worker on the default
		// queue the evaluation's jobs stay queued, so that the tests see what the sync leaves for the evaluation (the pending
		// advisories, the requests of the full evaluation, the job) before anything consumes it. The evaluation's own tests
		// are test/integration/vulnfindings and vulnflow.
		"TROVE_JOBS_WORKERS_DEFAULT=0",
	}, o.Env...)})
	e := &env{t: t, st: st, stub: stub, srv: srv}
	rita := st.User("rita")
	st.Bind("admin", rita)
	e.admin = st.Token(rita, scope.Scope{Inherit: true, Admin: true})
	st.Repository(mirrorRepo, "local", "generic", "{}")
	e.web = st.Start(t, testenv.ReplicaOptions{Name: "web", Role: "web"})
	if !o.NoWorker {
		e.worker = st.Start(t, testenv.ReplicaOptions{Name: "worker", Role: "worker"})
	}
	return e
}

// number and text read PostgreSQL.
func (e *env) number(sql string, args ...any) int64 { e.t.Helper(); return e.st.Number(sql, args...) }
func (e *env) text(sql string, args ...any) string  { e.t.Helper(); return e.st.Text(sql, args...) }

// job is the newest job of a kind.
type job struct {
	State  string
	Result string
	Error  string
}

// latest reads the newest job of the kind.
func (e *env) latest(kind string) job {
	e.t.Helper()
	j := job{
		State:  e.text("SELECT COALESCE((SELECT state::text FROM job WHERE kind = $1 ORDER BY created_at DESC LIMIT 1), '')", kind),
		Result: e.text("SELECT COALESCE((SELECT result::text FROM job WHERE kind = $1 ORDER BY created_at DESC LIMIT 1), '')", kind),
		Error:  e.text("SELECT COALESCE((SELECT error::text FROM job WHERE kind = $1 ORDER BY created_at DESC LIMIT 1), '')", kind),
	}
	// jsonb prints a space after the colon; the tests compare compact JSON.
	j.Result, j.Error = compact(j.Result), compact(j.Error)
	return j
}

// compact re-encodes JSON without insignificant space; text that is no JSON is returned as it is.
func compact(s string) string {
	var buf bytes.Buffer
	if json.Compact(&buf, []byte(s)) != nil {
		return s
	}
	return buf.String()
}

// run starts a task through the management API of the web replica and waits until its job has ended.
func (e *env) run(task string, args any) job {
	e.t.Helper()
	e.waitIdle(task)
	before := e.number("SELECT count(*) FROM job WHERE kind = $1", task)
	res := e.st.Do(e.web, "POST", "/api/v1/system/tasks/"+task+"/run", e.admin, args)
	if res.Status != 202 {
		e.t.Fatalf("running %s: %d %s", task, res.Status, res.Body)
	}
	within(e.t, "the run of "+task+" ending", 60*time.Second, func() bool {
		return e.number("SELECT count(*) FROM job WHERE kind = $1", task) > before &&
			e.number("SELECT count(*) FROM job WHERE kind = $1 AND state IN ('queued', 'running')", task) == 0
	})
	return e.latest(task)
}

// mustSucceed runs a task and fails the test unless the job succeeded.
func (e *env) mustSucceed(task string, args any) job {
	e.t.Helper()
	j := e.run(task, args)
	if j.State != "succeeded" {
		e.t.Fatalf("%s ended %s: %s\nlog of the worker:\n%s", task, j.State, j.Error, testenv.Tail(e.worker.Logs.String(), 20))
	}
	return j
}

// mustFail runs a task and expects a failed job; it returns the code recorded in the ecosystem's last_error.
func (e *env) mustFail(task, source, eco string, args any) string {
	e.t.Helper()
	j := e.run(task, args)
	if j.State != "failed" {
		e.t.Fatalf("%s ended %s, want failed (result %s)", task, j.State, j.Result)
	}
	return e.lastErrorCode(source, eco)
}

// lastErrorCode is the code of the problem object a failed run left in vuln_source_state.last_error.
func (e *env) lastErrorCode(source, eco string) string {
	e.t.Helper()
	return e.text("SELECT COALESCE((SELECT last_error->>'code' FROM vuln_source_state WHERE source = $1 AND ecosystem = $2), '')", source, eco)
}

func (e *env) waitIdle(kind string) {
	e.t.Helper()
	within(e.t, "no open run of "+kind, 30*time.Second, func() bool {
		return e.number("SELECT count(*) FROM job WHERE kind = $1 AND state IN ('queued', 'running')", kind) == 0
	})
}

// within polls cond until it holds or the time is up.
func within(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	testenv.WaitFor(t, what, d, cond)
}

// advisories is the number of stored records of an ecosystem.
func (e *env) advisories(eco string) int64 {
	e.t.Helper()
	return e.number("SELECT count(*) FROM vuln_advisory WHERE ecosystem = $1", eco)
}

// stateOf reads one column of the state row of an ecosystem.
func (e *env) stateOf(col, source, eco string) string {
	e.t.Helper()
	return e.text("SELECT COALESCE((SELECT "+col+"::text FROM vuln_source_state WHERE source = $1 AND ecosystem = $2), '')", source, eco)
}

// digest is a fingerprint of the stored advisory data of an ecosystem: any change of a record, an affected entry or a
// canonical identifier changes it.
func (e *env) digest(eco string) string {
	e.t.Helper()
	return e.text(`SELECT md5(COALESCE((SELECT string_agg(a.id || a.canonical_id || a.modified::text || encode(a.record_sha256, 'hex') || a.severity || a.known_exploited::text, ',' ORDER BY a.id)
		FROM vuln_advisory a WHERE a.ecosystem = $1), '')) || md5(COALESCE((SELECT string_agg(f.advisory_id || f.ord || f.name || f.ranges::text, ',' ORDER BY f.advisory_id, f.ord)
		FROM vuln_affected f WHERE f.ecosystem = $1), ''))`, eco)
}

// requestLog lists the paths the source was asked for, for a message.
func (e *env) requestLog() string {
	var b strings.Builder
	for _, r := range e.stub.Counts().Log {
		b.WriteString(r.Method + " " + r.Path + " " + strconv.Itoa(r.Status) + "\n")
	}
	return b.String()
}
