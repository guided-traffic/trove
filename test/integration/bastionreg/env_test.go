//go:build integration

// Package bastionreg tests the Bastion registrations and the Bastion token of the main installation
// (R-BAS-004, R-BAS-005) against the integration stack: the registration service and the token service over
// PostgreSQL, the management API behind the real authentication stage and the real decision point, the
// authenticator of the Bastion interface, and the admin commands. Every test creates a database of its own,
// and every replica has pools, a change log and caches of its own; Valkey is not part of it (the change log
// propagates through the database, which is the path that works with Valkey stopped).
package bastionreg

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/qname"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/bastionmain"
	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/project"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/test/integration/idapi"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

const (
	schemaName = "trove"
	baseURL    = "http://trove.test"
	// revocationBound is auth.revocation_bound of the replicas; the change log polls every 300 ms.
	revocationBound = 3 * time.Second
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// env is one test's migrated database.
type env struct {
	t     *testing.T
	cfg   config.Database
	admin *pgx.Conn
	mu    sync.Mutex
}

func newEnv(t *testing.T) *env {
	t.Helper()
	port, err := strconv.Atoi(testenv.Get(t, "TROVE_TEST_PG_PORT"))
	if err != nil {
		t.Fatalf("TROVE_TEST_PG_PORT: %v", err)
	}
	host, user, secret := testenv.Get(t, "TROVE_TEST_PG_HOST"), testenv.Get(t, "TROVE_TEST_PG_USER"), testenv.Get(t, "TROVE_TEST_PG_PASSWORD")
	connect := func(database string) *pgx.Conn {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cfg, err := pgx.ParseConfig(testenv.PgDSN(t, database))
		if err != nil {
			t.Fatal(err)
		}
		conn, err := pgx.ConnectConfig(ctx, cfg)
		if err != nil {
			t.Fatalf("connect to %s: %v", database, err)
		}
		t.Cleanup(func() { _ = conn.Close(context.Background()) })
		return conn
	}
	name := "trove_bastion_" + randHex(5)
	maint := connect("postgres")
	if _, err := maint.Exec(context.Background(), "CREATE DATABASE "+name+" ENCODING 'UTF8' TEMPLATE template0"); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() { _, _ = maint.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)") })
	dir := t.TempDir()
	userFile, passFile := filepath.Join(dir, "username"), filepath.Join(dir, "password")
	if err := os.WriteFile(userFile, []byte(user+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(passFile, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Database{
		Host: host, Port: port, Name: name, Schema: schemaName,
		UsernameFile: userFile, PasswordFile: passFile,
		SSLMode: "verify-full", SSLRootCertFile: testenv.CAFile(t),
		ConnectTimeout: 5 * time.Second, TargetSessionAttrs: "read-write",
		Pooler:        config.DatabasePooler{Mode: "none"},
		Pool:          config.DatabasePool{MaxConns: 8, MinConns: 1, MaxConnLifetime: 30 * time.Minute, MaxConnLifetimeJitter: time.Minute, MaxConnIdleTime: 5 * time.Minute, HealthCheckPeriod: 30 * time.Second},
		LeasePoolSize: 3, LeaseCheckInterval: time.Second,
		Session: config.DatabaseSession{
			StatementTimeout: 30 * time.Second, LockTimeout: 5 * time.Second,
			IdleInTransactionSessionTimeout: 60 * time.Second,
		},
		Retry:           config.DatabaseRetry{InitialBackoff: 50 * time.Millisecond, MaxBackoff: 200 * time.Millisecond, MaxElapsed: 2 * time.Second},
		Keepalive:       config.DatabaseKeepalive{Idle: 15 * time.Second, Interval: 5 * time.Second, Count: 3, UserTimeout: 30 * time.Second},
		ServerKeepalive: config.DatabaseServerKeepalive{Idle: 60 * time.Second, Interval: 10 * time.Second, Count: 6},
		Preflight:       config.DatabasePreflight{ConnectionBudget: "enforce", ReservedConnections: 10, AllowSuperuser: true},
	}
	days := 365
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := postgres.Migrate(ctx, cfg, postgres.MigrateOptions{
		Instance: "it-migrate", Version: "0.1.0", Log: quiet, AuditRetentionDays: &days,
		Migrate: config.Migrate{AdvisoryLockWait: 20 * time.Second, DDLLockTimeout: 2 * time.Second, DDLLockRetries: 3},
	}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if testenv.PgPooled(t) {
		cfg = testenv.ThroughPgBouncer(t, cfg, user, maint)
	}
	admin := connect(name)
	if _, err := admin.Exec(context.Background(), "SET search_path = "+schemaName); err != nil {
		t.Fatal(err)
	}
	return &env{t: t, cfg: cfg, admin: admin}
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, err := e.admin.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

func (e *env) execErr(sql string, args ...any) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, err := e.admin.Exec(context.Background(), sql, args...)
	return err
}

func (e *env) number(sql string, args ...any) (v int64) {
	e.t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.admin.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return v
}

func (e *env) text(sql string, args ...any) (v string) {
	e.t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.admin.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return v
}

// actions returns the audit events of an action, newest last, as (resource_type, resource_id, details).
type auditRow struct {
	Action, ResourceType, ResourceID string
	Details                          map[string]any
}

func (e *env) events(action string) []auditRow {
	e.t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	rows, err := e.admin.Query(context.Background(),
		`SELECT action, COALESCE(resource_type, ''), COALESCE(resource_id, ''), details FROM audit_event WHERE action = $1 ORDER BY occurred_at, id`, action)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []auditRow
	for rows.Next() {
		var r auditRow
		var raw []byte
		if err := rows.Scan(&r.Action, &r.ResourceType, &r.ResourceID, &raw); err != nil {
			e.t.Fatal(err)
		}
		_ = json.Unmarshal(raw, &r.Details)
		out = append(out, r)
	}
	return out
}

// bastionAuth asks the real authenticator as the recognition stage does, for a request the Bastion sent from
// 198.51.100.20 (its own address, before its forwarded headers count, R-BAS-017).
type bastionAuth struct{ a *bastionmain.Authenticator }

func (b bastionAuth) Authenticate(ctx context.Context, raw string) (bastionmain.BastionToken, error) {
	return b.a.Authenticate(ctx, raw, bastionmain.AuthRequest{ClientAddr: "198.51.100.20", UserAgent: "trove-bastion/0.15.0"})
}

// countingDB counts the reads and transactions of the token service, so that a test can tell that a request
// was refused before the token table was asked.
type countingDB struct {
	inner          principal.DB
	reads, txCalls int64
	mu             sync.Mutex
}

func (d *countingDB) InTx(ctx context.Context, fn func(meta.Tx) error) error {
	d.mu.Lock()
	d.txCalls++
	d.mu.Unlock()
	return d.inner.InTx(ctx, fn)
}

func (d *countingDB) Read(ctx context.Context, fn func(q.DBTX) error) error {
	d.mu.Lock()
	d.reads++
	d.mu.Unlock()
	return d.inner.Read(ctx, fn)
}

func (d *countingDB) calls() int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.reads + d.txCalls
}

// noRepositories is the repository reader of a database without repositories: the policy writes of these tests
// use selectors that name none.
type noRepositories struct{}

func (noRepositories) ByName(context.Context, qname.Repo) (repository.Repository, error) {
	return repository.Repository{}, errs.E(errs.NotFound, errs.CodeRepositoryNotFound, "no such repository")
}

func (noRepositories) ByID(context.Context, int64) (repository.Repository, error) {
	return repository.Repository{}, errs.E(errs.NotFound, errs.CodeRepositoryNotFound, "no repository")
}

func (noRepositories) List(context.Context) ([]repository.Repository, error) { return nil, nil }
func (noRepositories) ListProject(context.Context, string) ([]repository.Repository, error) {
	return nil, nil
}

func tokenConfig() config.AuthTokens {
	return config.AuthTokens{
		CacheTTL: 15 * time.Second, RetentionAfterEnd: 2160 * time.Hour, ExpiryNotice: 168 * time.Hour,
		PAT:       config.AuthTokenPAT{DefaultTTL: 2160 * time.Hour, MaxTTL: 8760 * time.Hour},
		SAT:       config.AuthTokenSAT{DefaultTTL: 2160 * time.Hour, MaxTTL: 8760 * time.Hour},
		CIT:       config.AuthTokenCIT{MaxTTL: 6 * time.Hour},
		BST:       config.AuthTokenBST{DefaultTTL: 8760 * time.Hour, MaxTTL: 9480 * time.Hour},
		MaxActive: config.AuthTokenMax{PAT: 50, SAT: 20, BST: 3},
	}
}

// replica is one in-process replica: pools, change log, caches, the identity and Bastion components and an
// HTTP listener with the real chain and the real authentication stage.
type replica struct {
	t    *testing.T
	name string
	reg  *prometheus.Registry

	db      *postgres.DB
	counted *countingDB
	changes *cluster.Changes
	caches  *cache.Registry
	writer  *audit.Writer
	store   *audit.Store

	principals *principal.PGStore
	manager    *principal.Manager
	settings   *token.SettingsStore
	tokens     *token.Service
	stage      *auth.Stage
	regs       *bastionmain.Registrations
	auth       bastionAuth
	srv        *httptest.Server
	handler    http.Handler

	stopOnce sync.Once
}

type replicaOpts struct {
	// maxRegistrations is bastions.max_registrations; zero is 16.
	maxRegistrations int
	// bindings run before the replica serves: the decision point reads the role bindings when it starts.
	bindings func(e *env)
}

func (e *env) newReplica(name string, o replicaOpts) *replica {
	t := e.t
	t.Helper()
	if o.bindings != nil {
		o.bindings(e)
	}
	if o.maxRegistrations == 0 {
		o.maxRegistrations = 16
	}
	r := &replica{t: t, name: name, reg: prometheus.NewRegistry()}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var err error
	r.db, err = postgres.Open(ctx, e.cfg, name, "web", quiet)
	if err != nil {
		t.Fatalf("Open(%s): %v", name, err)
	}
	t.Cleanup(r.stop)
	r.counted = &countingDB{inner: r.db}
	r.store = audit.NewStore(r.db)
	if err := r.store.Prepare(ctx, 365, quiet); err != nil {
		t.Fatalf("audit partitions: %v", err)
	}
	r.writer, err = audit.New(audit.Options{DB: r.db, Replica: name, Config: config.Audit{AsyncBuffer: 1000}, Log: quiet, AppLog: quiet})
	if err != nil {
		t.Fatal(err)
	}
	r.writer.Start()
	rec := v1.WithRequestSource(r.writer)

	clu := config.Cluster{
		LeaseRetryInterval: 500 * time.Millisecond, EpochPollInterval: 300 * time.Millisecond, EpochMaxStaleness: 20 * time.Second,
		ChangeRetention: time.Hour, SignalCoalesceWindow: 20 * time.Millisecond,
	}
	r.changes, err = cluster.NewChanges(cluster.ChangesOptions{DB: r.db.ControlPool(), Config: clu, WatchdogTimeout: 30 * time.Second, Log: quiet, Metrics: r.reg})
	if err != nil {
		t.Fatal(err)
	}
	r.caches, err = cache.NewRegistry(r.changes, r.reg)
	if err != nil {
		t.Fatal(err)
	}
	r.changes.AddListener(r.caches)
	if err := r.changes.Start(ctx); err != nil {
		t.Fatalf("changes.Start: %v", err)
	}

	cfg := tokenConfig()
	metrics, err := principal.NewMetrics(r.reg)
	if err != nil {
		t.Fatal(err)
	}
	gate := principal.Gate{Fresh: r.changes, Bound: revocationBound, Metrics: metrics}
	r.principals = principal.NewStore(principal.StoreOptions{DB: r.db, Cache: r.caches, Gate: gate, SafetyTTL: cfg.CacheTTL})
	r.settings = token.NewSettingsStore(token.SettingsOptions{
		DB: r.db, Notifier: r.changes, Recorder: rec, Cache: r.caches, Gate: gate, Tokens: cfg,
	})
	caps := map[string]bool{}
	for _, c := range authz.Capabilities() {
		caps[string(c)] = true
	}
	sc := idapi.ScopeOptions()
	sc.Action = func(string) bool { return true }
	sc.SystemItem = func(s string) bool { return caps[s] }
	r.tokens = token.NewService(token.ServiceOptions{
		DB: r.counted, Notifier: r.changes, Recorder: rec, Principals: r.principals, Settings: r.settings, Cache: r.caches, Gate: gate,
		Tokens: cfg, Metrics: metrics, Log: quiet, Scope: sc,
	})
	r.manager = principal.NewManager(principal.ManagerOptions{DB: r.db, Notifier: r.changes, Recorder: rec, BreakGlassName: "breakglass"})
	r.regs = bastionmain.NewRegistrations(bastionmain.RegistrationOptions{
		DB: r.db, Notifier: r.changes, Recorder: rec, Tokens: r.tokens, Max: o.maxRegistrations,
	})
	r.auth = bastionAuth{bastionmain.NewAuthenticator(r.tokens)}
	r.stage = auth.New(auth.Options{
		Tokens: r.tokens, Uses: r.tokens, Principals: r.principals, Limiter: auth.NewLocalLimiter(300, time.Minute, nil, nil),
		BasicEnabled: true, APIKeyHeader: true, Metrics: metrics, ClientAddr: func(*http.Request) netip.Addr { return netip.MustParseAddr("203.0.113.7") }, Log: quiet,
	})

	r.handler = idapi.Handler(t, idapi.Components{
		Name: name, DB: r.db, Changes: r.changes, Stage: r.stage, Manager: r.manager, Tokens: r.tokens, Settings: r.settings,
		Recorder: rec, Auth: idapi.AuthConfig(cfg), BaseURL: baseURL, Log: quiet,
		Options: func(engine *authz.Engine, opts *v1.Options) {
			policies := authz.NewService(authz.ServiceOptions{DB: r.db, Changes: r.changes, Principals: r.principals, Repositories: noRepositories{}, Recorder: rec})
			opts.Authz = v1.AuthzServices{
				Policies: policies, Engine: engine,
				Projects: project.New(project.Options{DB: r.db, Changes: r.changes, Principals: r.principals, Repositories: noRepositories{}, Policies: policies, Recorder: rec}),
			}
			opts.AuditStore = r.store
			opts.Bastions = r.regs
		},
	})
	r.srv = httptest.NewServer(r.handler)
	t.Cleanup(r.srv.Close)
	return r
}

func (r *replica) stop() {
	r.stopOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if r.srv != nil {
			r.srv.Close()
		}
		if r.writer != nil {
			_ = r.writer.Flush(ctx)
		}
		if r.changes != nil {
			_ = r.changes.Stop(ctx)
		}
		if r.db != nil {
			r.db.Close()
		}
	})
}

// flush writes the buffered asynchronous audit events, so that a query sees them.
func (r *replica) flush() {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := r.writer.Flush(ctx); err != nil {
		r.t.Fatalf("audit flush: %v", err)
	}
}

// ---- the people and their tokens ---------------------------------------------------------------------------

var systemCaller = principal.Caller{Actor: audit.Actor{Kind: "admin-cli", Name: "test"}}

func (r *replica) user(name string) principal.User {
	r.t.Helper()
	u, err := r.manager.CreateUser(context.Background(), systemCaller, principal.NewUser{Name: name, Email: name + "@example.com"})
	if err != nil {
		r.t.Fatalf("CreateUser(%s): %v", name, err)
	}
	return u
}

// pat creates a personal access token of the user with the scope document (the default when empty).
func (r *replica) pat(u principal.User, scopeJSON string) string {
	r.t.Helper()
	req := token.CreateRequest{
		Caller: principal.Caller{Actor: audit.Actor{Kind: "user", ID: u.ID, Name: u.Name}}, Type: token.TypePAT, OwnerID: u.ID,
		Description: "test", Via: "api",
	}
	if scopeJSON != "" {
		req.Scope = []byte(scopeJSON)
	}
	c, err := r.tokens.Create(context.Background(), req)
	if err != nil {
		r.t.Fatalf("Create PAT: %v", err)
	}
	return c.Secret
}

func bindRole(e *env, role, userID string) {
	e.exec(`INSERT INTO authz_role_binding (role, principal_id) VALUES ($1, $2::uuid) ON CONFLICT DO NOTHING`, role, userID)
}

// cast is the people of most tests: an administrator, an auditor (endpoints:read only), a security
// administrator, and an administrator whose token is cut down to tokens:revoke.
type cast struct {
	admin, auditor, security, revokeOnly string
}

// newCast creates the people and binds their roles, then starts a replica; it returns the replica and the
// tokens. The decision point reads the bindings when it starts, so they are bound first.
func (e *env) newCast(name string, o replicaOpts) (*replica, cast) {
	e.t.Helper()
	// The people are created through a replica that is not served: users first, bindings next, then the replica.
	seed := e.newReplica(name+"-seed", replicaOpts{maxRegistrations: o.maxRegistrations})
	admin, auditor, sec, rev := seed.user("root"), seed.user("audrey"), seed.user("sam"), seed.user("rita")
	bindRole(e, "admin", admin.ID)
	bindRole(e, "auditor", auditor.ID)
	bindRole(e, "security-admin", sec.ID)
	bindRole(e, "admin", rev.ID)
	c := cast{
		admin:      seed.pat(admin, `{"inherit":true,"admin":true}`),
		auditor:    seed.pat(auditor, `{"inherit":true,"system":["endpoints:read"]}`),
		security:   seed.pat(sec, `{"inherit":true,"system":["endpoints:read","endpoints:write","tokens:revoke"]}`),
		revokeOnly: seed.pat(rev, `{"inherit":false,"system":["tokens:revoke"]}`),
	}
	seed.stop()
	return e.newReplica(name, o), c
}

// ---- HTTP ---------------------------------------------------------------------------------------------------

type reply struct {
	Status int
	Header http.Header
	Body   []byte
}

func (rp reply) json(t testing.TB) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rp.Body, &m); err != nil {
		t.Fatalf("body is not a JSON object (%d): %s", rp.Status, rp.Body)
	}
	return m
}

func (rp reply) code() string {
	var m map[string]any
	_ = json.Unmarshal(rp.Body, &m)
	c, _ := m["code"].(string)
	return c
}

// call sends a request to the replica; body is a string or a value to marshal, hdr pairs set headers ("-"
// removes one). Authorization is "Bearer <bearer>" when bearer is not empty.
func (r *replica) call(method, path, bearer string, body any, hdr ...string) reply {
	r.t.Helper()
	var rdr io.Reader
	ctype := "application/json"
	if method == http.MethodPatch {
		ctype = "application/merge-patch+json"
	}
	switch b := body.(type) {
	case nil:
	case string:
		rdr = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			r.t.Fatal(err)
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, r.srv.URL+path, rdr)
	if err != nil {
		r.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", ctype)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		if hdr[i+1] == "-" {
			req.Header.Del(hdr[i])
		} else {
			req.Header.Set(hdr[i], hdr[i+1])
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return reply{Status: resp.StatusCode, Header: resp.Header, Body: raw}
}

func want(t testing.TB, rp reply, status int, code string) {
	t.Helper()
	if rp.Status != status || (code != "" && rp.code() != code) {
		t.Fatalf("status %d code %q, want %d %q\n%s", rp.Status, rp.code(), status, code, rp.Body)
	}
}

func waitFor(t testing.TB, what string, within time.Duration, cond func() bool) time.Duration {
	t.Helper()
	start := time.Now()
	for time.Since(start) < within {
		if cond() {
			return time.Since(start)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s did not happen within %s", what, within)
	return 0
}
