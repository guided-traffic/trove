//go:build integration

// Package endpoints tests the endpoints area (WP1.9) against the integration
// stack with full in-process replicas: the credential rules of an endpoint
// (R-EP-010), endpoint-bound tokens (R-EP-011), exposure rules written through
// the management API and applied as the ceiling of the decision point on every
// replica (R-EP-012 to R-EP-014, R-EP-016), and the rate-limit class of an
// endpoint (R-EP-015), each with Valkey running and with Valkey unreachable. A
// dispatcher sends consecutive requests to different replicas, like the scatter
// profile. Every test creates a database of its own.
//
// The installation, its replicas and its requests come from the harness
// (test/integration/testenv); this file adds the scenario: the repositories,
// principals and tokens the tests use. The files are seeded through the real
// PUT /files/{repoKey}/{path} of the files area and read through its real GET,
// which decides on the request's context, so the endpoint's ceiling applies, and
// answers authz.NotFound for a missing file.
package endpoints

import (
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/api/v1/apitest"
	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }

// The hosts of the endpoints (R-EP-003): the default endpoint checks no host, the
// further ones check theirs.
const (
	customersHost = "packages.trove.test"
	partnersHost  = "partners.trove.test"
)

// endpointsJSON is server.endpoints of every replica: two further endpoints with
// a host each, serving the package and the files surface.
var endpointsJSON = fmt.Sprintf(`{"customers":{"listen":"127.0.0.1:0","base_url":"https://%s","surface":["packages","files"]},`+
	`"partners":{"listen":"127.0.0.1:0","base_url":"https://%s","surface":["packages","files"]}}`, customersHost, partnersHost)

// env is one installation: its database, the principals and tokens the tests
// use, and the replicas.
type env struct {
	t     *testing.T
	name  string
	stack *testenv.Stack
	val   *apitest.Validator

	reps []*replica
	next atomic.Int64

	// Seeded principals and tokens.
	opsPAT        string // a user with the admin role, admin scope, unbound: the management API
	alicePAT      string // a user's PAT
	acmeSAT       string // customer-acme, bound to customers and partners
	acmeSAT2      string // a second token of customer-acme, bound to customers
	acmeUnbound   string // customer-acme, unbound
	acmeUnbound2  string // a second unbound one, never used before the lock test
	botAdminSAT   string // ops-bot holds admin; its admin token is bound to customers
	acmeID, botID string
}

// newEnv creates the installation trove_ep_<random>, migrates it and seeds it.
// valkey false points every replica at a Sentinel that never answers: the state
// "Valkey stopped".
func newEnv(t *testing.T, valkey bool) *env {
	t.Helper()
	s := testenv.NewStack(t, testenv.StackOptions{Valkey: valkey, Name: "ep"})
	e := &env{t: t, name: s.Database(), stack: s, val: s.Validator()}
	e.seed()
	return e
}

func (e *env) exec(sql string, args ...any)        { e.t.Helper(); e.stack.Exec(sql, args...) }
func (e *env) text(sql string, args ...any) string { e.t.Helper(); return e.stack.Text(sql, args...) }

func (e *env) number(sql string, args ...any) int64 {
	e.t.Helper()
	return e.stack.Number(sql, args...)
}

// seed writes the rows the tests need before a replica starts, so every replica
// builds its first grant index with them: repositories, principals, a read grant
// for customer-acme on generic-local and generic-other, the admin role of ops and
// ops-bot, and their tokens.
func (e *env) seed() {
	s := e.stack
	s.Repository("generic-local", "local", "generic", "{}")
	s.Repository("generic-other", "local", "generic", "{}")
	ops, alice := s.User("ops"), s.User("alice")
	acme, bot := s.ServiceAccount("customer-acme"), s.ServiceAccount("ops-bot")
	e.acmeID, e.botID = acme.ID, bot.ID
	s.Bind("admin", ops)
	s.Bind("admin", bot)
	s.Policy("customers-read", []string{"read"}, []string{"generic-local", "generic-other"}, acme)
	e.opsPAT = s.Token(ops, scope.Scope{Inherit: true, Admin: true})
	e.alicePAT = s.Token(alice, scope.Default())
	e.acmeSAT = s.Token(acme, scope.Scope{Inherit: true, Endpoints: []string{"customers", "partners"}})
	e.acmeSAT2 = s.Token(acme, scope.Scope{Inherit: true, Endpoints: []string{"customers"}})
	e.acmeUnbound = s.Token(acme, scope.Default())
	e.acmeUnbound2 = s.Token(acme, scope.Default())
	e.botAdminSAT = s.Token(bot, scope.Scope{Inherit: true, Admin: true, Endpoints: []string{"customers"}})
}

// replica is one in-process replica with its three client listeners.
type replica struct {
	name                    string
	app                     *testenv.Replica
	logs                    *testenv.SyncBuffer
	def, cust, partner, ops string
}

// files is the content seeded through the real PUT /files/{repoKey}/{path} of the
// default endpoint, by the administrator ops; every other path is missing.
var files = map[string]string{
	"generic-local/releases/1.0/app.zip": "app 1.0",
	"generic-local/releases/2.0/app.zip": "app 2.0",
	"generic-local/internal/x.bin":       "internal",
	"generic-other/releases/1.0/app.zip": "other",
}

// seedFiles deploys the files of the files map through the first replica's
// default endpoint once.
func (e *env) seedFiles(r *replica) {
	e.t.Helper()
	for path, content := range files {
		a := e.send(r.def, "trove.test", "PUT", "/files/"+path, e.opsPAT, []byte(content), "Content-Type", "application/octet-stream")
		if a.Status != 201 {
			e.t.Fatalf("seed %s: %d %s", path, a.Status, a.Body)
		}
	}
}

// start brings up one replica of role web and waits until it serves.
func (e *env) start(name string) *replica {
	e.t.Helper()
	h := e.stack.Start(e.t, testenv.ReplicaOptions{Name: name, Endpoints: endpointsJSON})
	r := &replica{
		name: name, app: h, logs: h.Logs,
		def: h.Addr("default"), cust: h.Addr("customers"), partner: h.Addr("partners"), ops: h.OpsAddr(),
	}
	if len(e.reps) == 0 {
		e.seedFiles(r)
	}
	e.reps = append(e.reps, r)
	return r
}

var client = testenv.Client

// answer is one response, read.
type answer = testenv.Answer

// send sends one request to an address with the Host of the endpoint and an
// optional token. Answers of the management API are validated against the
// document.
func (e *env) send(addr, host, method, path, tok string, body any, hdr ...string) answer {
	e.t.Helper()
	return e.stack.DoAt(addr, host, method, path, tok, body, hdr...)
}

// pick returns the replicas in turn (the dispatcher of the scatter profile).
func (e *env) pick() *replica {
	return e.reps[int(e.next.Add(1)-1)%len(e.reps)]
}

// api sends a management API request to the next replica's default endpoint.
func (e *env) api(method, path string, body any, hdr ...string) answer {
	e.t.Helper()
	return e.send(e.pick().def, "trove.test", method, path, e.opsPAT, body, hdr...)
}

// onCustomers sends a request to a replica's customers listener.
func (e *env) onCustomers(r *replica, path, tok string, hdr ...string) answer {
	e.t.Helper()
	return e.send(r.cust, customersHost, "GET", path, tok, nil, hdr...)
}

// waitFor polls cond until it holds or the time is up and returns how long it took.
func waitFor(t testing.TB, what string, within time.Duration, cond func() bool) time.Duration {
	t.Helper()
	return testenv.WaitFor(t, what, within, cond)
}

// propagation is the bound of R-AUTHZ-028 the tests hold a rule change to: one
// cluster.epoch_poll_interval (the default, 1 s) plus one second.
const propagation = 2 * time.Second
