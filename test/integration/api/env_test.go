//go:build integration

// Package api tests the management API conventions against the integration
// stack, across several in-process replicas that share one database and one
// Valkey: cursor walks, ETag with If-Match, merge patch and idempotency keys on
// a test resource, the cluster-wide rate limits with Valkey running and stopped,
// the job and task operations with a web replica that enqueues and a worker
// replica that executes, and the key-ring status of replicas whose rings differ.
// Every test creates a database of its own.
//
// The replicas are the production composition root started through the harness
// (test/integration/testenv), so every request meets the real chain, the real
// authentication stage with real tokens and the real decision point. The test
// resource (the widget) is registered on the replicas' routers through
// app.Options.Extra, outside /api/v1/ because a route below it must be in the
// document.
package api

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/secrets"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }

func randHex(n int) string { return testenv.RandHex(n) }

func uuidV4() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// person is a principal of the cast with its token.
type person struct {
	testenv.Person
	token string
}

// header is the request header of a caller.
func (p person) header() []string { return []string{"Authorization", "Bearer " + p.token} }

// env is one test's installation and the cast of its principals: an
// administrator (every capability), a reader with settings:read only (the
// auditor role, a token that names the capability) and three users without a
// role. They are created
// before the first replica starts, so every replica's grant index has them.
type env struct {
	t     *testing.T
	stack *testenv.Stack

	admin, reader, alice, bob, carol person
}

// newEnv creates the installation trove_api_<random>. valkey false points every
// replica at a Sentinel that never answers.
func newEnv(t *testing.T, valkey bool, envs ...string) *env {
	t.Helper()
	e := &env{t: t, stack: testenv.NewStack(t, testenv.StackOptions{Valkey: valkey, Name: "api", Env: envs})}
	// The sealed column the key-ring tests register exists in every database,
	// because the registry is process-wide.
	e.exec("CREATE TABLE trove.vault_items (id text PRIMARY KEY, secret text)")
	e.exec("GRANT ALL ON trove.vault_items TO PUBLIC")
	mint := func(u testenv.Person, sc scope.Scope) person { return person{Person: u, token: e.stack.Token(u, sc)} }
	adm, rdr := e.stack.User("api-admin"), e.stack.User("reader")
	e.stack.Bind("admin", adm)
	e.stack.Bind("auditor", rdr)
	e.admin = mint(adm, scope.Scope{Inherit: true, Admin: true})
	// A token exercises the capabilities its scope names that the owner's roles
	// grant (R-AUTHZ-013): settings:read comes from the auditor role.
	e.reader = mint(rdr, scope.Scope{Inherit: true, System: []string{"settings:read"}})
	e.alice, e.bob, e.carol = mint(e.stack.User("alice"), scope.Default()), mint(e.stack.User("bob"), scope.Default()), mint(e.stack.User("carol"), scope.Default())
	return e
}

func (e *env) exec(sql string, args ...any) { e.t.Helper(); e.stack.Exec(sql, args...) }

func (e *env) scalar(sql string, args ...any) int64 {
	e.t.Helper()
	return e.stack.Number(sql, args...)
}

// vaultColumn is a sealed column of the test process (secrets.Register is
// process-wide and refuses registrations after Freeze, which the first replica
// of the process calls: the package registers it before any test runs).
var vaultColumn = secrets.Register("vault_items", "id", "secret")

// ringEnv is the environment of a replica with a key ring of its own: the key
// files and the active key ID.
func ringEnv(active string, files ...string) []string {
	quoted := make([]string, len(files))
	for i, f := range files {
		quoted[i] = strconv.Quote(f)
	}
	return []string{"TROVE_SECRETS_ENCRYPTION_KEY_FILES=[" + strings.Join(quoted, ",") + "]", "TROVE_SECRETS_ACTIVE_KEY_ID=" + active}
}

// keyFile writes a key of 32 times fill and returns its path.
func (e *env) keyFile(id string, fill byte) string {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = fill
	}
	return e.stack.KeyFile(id+".key", raw)
}

// nodeOpts describe one replica.
type nodeOpts struct {
	role string
	// env is appended to the replica's environment.
	env []string
	// widgets registers the test resource on the replica.
	widgets bool
}

// node is one replica of the test.
type node struct {
	*testenv.Replica
	widgets *widgetAPI
	// base is the URL of a component server (component_test.go), which has no replica.
	base string
}

// url is the base URL of the default endpoint's listener; empty for a worker.
func (n *node) url() string {
	if n.base != "" {
		return n.base
	}
	if a := n.Addr("default"); a != "" {
		return "http://" + a
	}
	return ""
}

// newNode starts a replica through app.New and waits until it is ready.
func (e *env) newNode(name string, o nodeOpts) *node {
	t := e.t
	t.Helper()
	n := &node{}
	ro := testenv.ReplicaOptions{Name: name, Role: o.role, Env: o.env}
	if o.widgets {
		cfg := apiConfig()
		db := e.openDB(name)
		n.widgets = newWidgetAPI(db, v1.NewIdempotency(db, cfg.IdempotencyRetention), cfg)
		ro.Extra = func(r *httpserver.Router) { n.widgets.register(r) }
	}
	n.Replica = e.stack.Start(t, ro)
	return n
}

// openDB opens a pool on the installation's database for the test resource,
// which runs plain statements on a table that exists only in the test database.
func (e *env) openDB(name string) *postgres.DB {
	t := e.t
	t.Helper()
	cfg := e.stack.DatabaseConfig()
	cfg.Pool.MaxConns = 24
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, cfg, name+"-widgets", "web", testenv.Quiet())
	if err != nil {
		t.Fatalf("Open(%s): %v", name, err)
	}
	t.Cleanup(db.Close)
	return db
}

func apiConfig() config.API {
	return config.API{
		PageSizeDefault: 50, PageSizeMax: 500, IdempotencyRetention: time.Hour,
		MaxRequestBodyBytes: config.MiB, MaxConfigurationBodyBytes: 4 * config.MiB,
	}
}

// rateLimitsOn turns the management API's classes on at their defaults, which
// are the budgets the rate-limit tests state (read 50/s with a burst of 200).
var rateLimitsOn = []string{"TROVE_API_RATE_LIMITS_ENABLED=true"}

func waitFor(t testing.TB, what string, within time.Duration, cond func() bool) {
	t.Helper()
	testenv.WaitFor(t, what, within, cond)
}
