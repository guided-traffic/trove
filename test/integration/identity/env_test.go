//go:build integration

// Package identity tests the identity packages against the integration stack:
// principals, groups and the provisioner, tokens with their storage, caches and
// revocation across replicas, the settings and the authentication stage, each
// with several in-process "replicas" that have pools, change logs and caches of
// their own. Every test creates a database of its own, so parallel tests and
// agents never collide.
package identity

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

const schemaName = "trove"

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type stack struct {
	host         string
	port         int
	user, secret string
}

func getStack(t testing.TB) stack {
	t.Helper()
	port, err := strconv.Atoi(testenv.Get(t, "TROVE_TEST_PG_PORT"))
	if err != nil {
		t.Fatalf("TROVE_TEST_PG_PORT: %v", err)
	}
	return stack{
		host: testenv.Get(t, "TROVE_TEST_PG_HOST"), port: port,
		user: testenv.Get(t, "TROVE_TEST_PG_USER"), secret: testenv.Get(t, "TROVE_TEST_PG_PASSWORD"),
	}
}

func (s stack) admin(t testing.TB, database string) *pgx.Conn {
	t.Helper()
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

// env is one test's database and Valkey keyspace.
type env struct {
	t     *testing.T
	st    stack
	db    string
	cfg   config.Database
	admin *pgx.Conn
	ks    string
	vcfg  config.Valkey
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st := getStack(t)
	name := "trove_ident_" + randHex(5)
	maint := st.admin(t, "postgres")
	if _, err := maint.Exec(context.Background(), "CREATE DATABASE "+name+" ENCODING 'UTF8' TEMPLATE template0"); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() { _, _ = maint.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)") })
	dir := t.TempDir()
	userFile, passFile := filepath.Join(dir, "username"), filepath.Join(dir, "password")
	if err := os.WriteFile(userFile, []byte(st.user+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(passFile, []byte(st.secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Database{
		Host: st.host, Port: st.port, Name: name, Schema: schemaName,
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
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := postgres.Migrate(ctx, cfg, postgres.MigrateOptions{
		Instance: "it-migrate", Version: "0.1.0", Log: quiet,
		Migrate: config.Migrate{AdvisoryLockWait: 20 * time.Second, DDLLockTimeout: 2 * time.Second, DDLLockRetries: 3},
	}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if testenv.PgPooled(t) {
		cfg = testenv.ThroughPgBouncer(t, cfg, st.user, maint)
	}
	admin := st.admin(t, name)
	if _, err := admin.Exec(context.Background(), "SET search_path = "+schemaName); err != nil {
		t.Fatal(err)
	}
	return &env{t: t, st: st, db: name, cfg: cfg, admin: admin, ks: randHex(8), vcfg: valkeyConfig(t)}
}

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

var keyPrefix = "trove:iti" + randHex(3) + ":"

func valkeyConfig(t *testing.T) config.Valkey {
	v := testenv.ValkeyConfig(t)
	return config.Valkey{
		Mode:         "sentinel",
		KeyPrefix:    keyPrefix,
		UsernameFile: writeFile(t, "username", v.Username+"\n"),
		PasswordFile: writeFile(t, "password", v.Password+"\n"),
		Sentinel: config.ValkeySentinel{
			Addresses: v.Sentinels, PrimaryName: v.PrimaryName,
			PasswordFile: writeFile(t, "sentinel-password", v.SentinelPassword+"\n"),
			TLS:          true, RefreshInterval: 2 * time.Second,
		},
		TLS:                  config.ValkeyTLS{Mode: "verify-full", CAFile: v.CAFile},
		ConnectTimeout:       2 * time.Second,
		CommandTimeout:       500 * time.Millisecond,
		SentinelQueryTimeout: 300 * time.Millisecond,
		MaxConnLifetime:      30 * time.Minute,
		HealthCheckInterval:  500 * time.Millisecond,
		Reconnect:            config.ValkeyReconnect{InitialBackoff: 100 * time.Millisecond, MaxBackoff: time.Second},
		CircuitBreaker:       config.ValkeyBreaker{FailureThreshold: 5, OpenDuration: 2 * time.Second},
	}
}

// deadValkey points the configuration at a Sentinel that does not answer: the
// client is constructed degraded and keeps trying, which is what a stopped
// Valkey looks like to a replica.
func deadValkey(c config.Valkey) config.Valkey {
	c.Sentinel.Addresses = []string{"127.0.0.1:1"}
	return c
}

type vkMode int

const (
	vkLive vkMode = iota
	vkDead
)

func (m vkMode) String() string {
	if m == vkDead {
		return "Valkey stopped"
	}
	return "Valkey running"
}

// capture is an audit recorder that keeps every event it is given, in memory.
type capture struct {
	mu     sync.Mutex
	events []audit.Event
}

func (c *capture) RecordTx(_ context.Context, _ meta.Tx, ev audit.Event) error {
	c.mu.Lock()
	c.events = append(c.events, ev)
	c.mu.Unlock()
	return nil
}

func (c *capture) Record(ctx context.Context, ev audit.Event) { _ = c.RecordTx(ctx, nil, ev) }

// tee records into the capture and the audit writer.
type tee struct {
	capture *capture
	writer  *audit.Writer
}

func (t tee) RecordTx(ctx context.Context, tx meta.Tx, ev audit.Event) error {
	_ = t.capture.RecordTx(ctx, tx, ev)
	return t.writer.RecordTx(ctx, tx, ev)
}

func (t tee) Record(ctx context.Context, ev audit.Event) {
	t.capture.Record(ctx, ev)
	t.writer.Record(ctx, ev)
}

func (c *capture) all() []audit.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]audit.Event(nil), c.events...)
}

func (c *capture) named(name string) []audit.Event {
	var out []audit.Event
	for _, e := range c.all() {
		if e.Type == name {
			out = append(out, e)
		}
	}
	return out
}

// stale makes a replica's change log look older than the revocation bound.
type stale struct {
	inner principal.Freshness
	off   atomic.Bool
}

func (s *stale) FreshWithin(d time.Duration) bool { return !s.off.Load() && s.inner.FreshWithin(d) }

// replicaOpts describe one in-process replica.
type replicaOpts struct {
	valkey vkMode
	tokens func(*config.AuthTokens)
	// limiter is the failed-credential limiter of the stage; nil gives a
	// per-replica one with the default budget.
	limiter auth.FailureLimiter
	// noRecent leaves out the throttle-exemption markers.
	noRecent bool
	// failureBudget, when positive, gives the stage the cluster limiter
	// auth.credential_failures of the composition root (R-AUTH-038) with that many
	// rejected credentials per minute and address, among liveReplicas replicas.
	failureBudget int
	liveReplicas  int
	// flowStarts, when positive, gives the API the cluster limiter auth.flow_starts
	// with that many requests per minute and address.
	flowStarts int
	// auditDB also writes the events to the audit_event table (the real writer),
	// next to the in-memory capture.
	auditDB bool
}

// replica is one in-process replica: pools, change log, caches and the identity
// components, built the way the composition root does.
type replica struct {
	t    *testing.T
	name string
	reg  *prometheus.Registry

	db      *postgres.DB
	vk      *cluster.Valkey
	changes *cluster.Changes
	caches  *cache.Registry
	fresh   *stale
	rec     *capture
	// recorder is what the components record into: the capture, and with
	// replicaOpts.auditDB the audit writer as well.
	recorder   audit.Recorder
	writer     *audit.Writer
	flowStarts *cluster.Limiter

	principals  *principal.PGStore
	manager     *principal.Manager
	provisioner *principal.PGProvisioner
	settings    *token.SettingsStore
	tokens      *token.Service
	stage       *auth.Stage
	tokenCfg    config.AuthTokens

	stopOnce sync.Once
}

func defaultAuthTokens() config.AuthTokens {
	return config.AuthTokens{
		CacheTTL: 15 * time.Second, RetentionAfterEnd: 2160 * time.Hour, ExpiryNotice: 168 * time.Hour,
		PAT:       config.AuthTokenPAT{DefaultTTL: 2160 * time.Hour, MaxTTL: 8760 * time.Hour},
		SAT:       config.AuthTokenSAT{DefaultTTL: 2160 * time.Hour, MaxTTL: 8760 * time.Hour},
		CIT:       config.AuthTokenCIT{MaxTTL: 6 * time.Hour},
		MaxActive: config.AuthTokenMax{PAT: 50, SAT: 20},
	}
}

const revocationBound = 2 * time.Second

func (e *env) newReplica(name string, o replicaOpts) *replica {
	t := e.t
	t.Helper()
	r := &replica{t: t, name: name, reg: prometheus.NewRegistry(), rec: &capture{}, tokenCfg: defaultAuthTokens()}
	if o.tokens != nil {
		o.tokens(&r.tokenCfg)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var err error
	r.db, err = postgres.Open(ctx, e.cfg, name, "web", quiet)
	if err != nil {
		t.Fatalf("Open(%s): %v", name, err)
	}
	t.Cleanup(r.stop)
	r.recorder = r.rec
	if o.auditDB {
		store := audit.NewStore(r.db)
		if err := store.Prepare(ctx, 365, quiet); err != nil {
			t.Fatalf("audit partitions: %v", err)
		}
		r.writer, err = audit.New(audit.Options{DB: r.db, Replica: name, Config: config.Audit{AsyncBuffer: 1000}, Log: quiet, AppLog: quiet})
		if err != nil {
			t.Fatal(err)
		}
		r.writer.Start()
		r.recorder = tee{r.rec, r.writer}
	}

	vcfg := e.vcfg
	if o.valkey == vkDead {
		vcfg = deadValkey(vcfg)
	}
	r.vk, err = cluster.NewValkey(ctx, vcfg, name, "web", quiet, prometheus.NewRegistry(), cluster.WithKeyspaceID(e.ks))
	if err != nil {
		t.Fatalf("NewValkey(%s): %v", name, err)
	}
	clu := config.Cluster{
		LeaseRetryInterval: 500 * time.Millisecond, EpochPollInterval: time.Second, EpochMaxStaleness: 20 * time.Second,
		ChangeRetention: time.Hour, SignalCoalesceWindow: 20 * time.Millisecond,
	}
	r.changes, err = cluster.NewChanges(cluster.ChangesOptions{
		DB: r.db.ControlPool(), Bus: r.vk, Config: clu, WatchdogTimeout: 30 * time.Second, Log: quiet, Metrics: r.reg,
	})
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

	metrics, err := principal.NewMetrics(r.reg)
	if err != nil {
		t.Fatal(err)
	}
	r.fresh = &stale{inner: r.changes}
	gate := principal.Gate{Fresh: r.fresh, Bound: revocationBound, Metrics: metrics}
	r.principals = principal.NewStore(principal.StoreOptions{DB: r.db, Cache: r.caches, Gate: gate, SafetyTTL: r.tokenCfg.CacheTTL})
	r.settings = token.NewSettingsStore(token.SettingsOptions{
		DB: r.db, Notifier: r.changes, Recorder: r.recorder, Cache: r.caches, Gate: gate, Tokens: r.tokenCfg, Anonymous: config.AuthAnonymous{Allowed: true},
	})
	var recent token.RecentTokens
	if !o.noRecent {
		seen, err := cluster.NewTokenSeen(r.vk, 10*time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		recent = seen
	}
	r.tokens = token.NewService(token.ServiceOptions{
		DB: principal.Joinable{DB: r.db}, Notifier: r.changes, Recorder: r.recorder, Principals: r.principals, Settings: r.settings, Cache: r.caches, Gate: gate,
		Tokens: r.tokenCfg, Recent: recent, Metrics: metrics, Log: quiet,
		Scope: token.ScopeOptions{Action: func(string) bool { return true }, SystemItem: func(s string) bool { return s == "identities:read" }},
	})
	r.manager = principal.NewManager(principal.ManagerOptions{DB: principal.Joinable{DB: r.db}, Notifier: r.changes, Recorder: r.recorder, BreakGlassName: "breakglass"})
	r.provisioner = principal.NewProvisioner(principal.ProvisionerOptions{
		DB: r.db, Notifier: r.changes, Recorder: r.recorder, BreakGlassName: "breakglass", Metrics: metrics,
	})
	if o.flowStarts > 0 {
		ls, err := cluster.NewLimiterSet(cluster.LimiterOptions{Valkey: r.vk, Registry: prometheus.NewRegistry(), LiveReplicas: func() int { return max(1, o.liveReplicas) }})
		if err != nil {
			t.Fatal(err)
		}
		r.flowStarts = ls.NewLimiter("auth.flow_starts", cluster.ScopeCluster, float64(o.flowStarts)/60, o.flowStarts)
	}
	limiter := o.limiter
	if o.failureBudget > 0 {
		live := max(1, o.liveReplicas)
		ls, err := cluster.NewLimiterSet(cluster.LimiterOptions{Valkey: r.vk, Registry: r.reg, LiveReplicas: func() int { return live }})
		if err != nil {
			t.Fatal(err)
		}
		limiter = ls.NewLimiter("auth.credential_failures", cluster.ScopeCluster, float64(o.failureBudget)/60, o.failureBudget)
	}
	if limiter == nil {
		limiter = auth.NewLocalLimiter(300, time.Minute, nil, nil)
	}
	r.stage = auth.New(auth.Options{
		Tokens: r.tokens, Principals: r.principals, Settings: r.settings, Limiter: limiter, BasicEnabled: true, Metrics: metrics,
		ClientAddr: func(*http.Request) netip.Addr { return netip.MustParseAddr("203.0.113.7") }, Log: quiet,
	})
	return r
}

func (r *replica) stop() {
	r.stopOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if r.writer != nil {
			_ = r.writer.Flush(ctx)
		}
		if r.changes != nil {
			_ = r.changes.Stop(ctx)
		}
		if r.vk != nil {
			r.vk.Close()
		}
		if r.db != nil {
			r.db.Close()
		}
	})
}

// do authenticates a GET of path with the given Authorization header on the
// replica's stage.
func (r *replica) do(authorization, path string) (auth.Result, error) {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	return r.stage.Authenticate(req, auth.EndpointRules{
		Name: "default", AcceptsSession: true, Anonymous: true,
		TokenTypes: []token.Type{token.TypePAT, token.TypeSAT, token.TypeCIT},
	})
}

func (r *replica) bearer(raw, path string) (auth.Result, error) {
	return r.do("Bearer "+raw, path)
}

func waitFor(t testing.TB, what string, within time.Duration, cond func() bool) time.Duration {
	t.Helper()
	start := time.Now()
	for time.Since(start) < within {
		if cond() {
			return time.Since(start)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s did not happen within %s", what, within)
	return 0
}

func (e *env) scalar(sql string, args ...any) (v int64) {
	e.t.Helper()
	if err := e.admin.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return v
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.admin.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

var admin = principal.Caller{Actor: audit.Actor{Kind: "admin-cli", Name: "test"}}

func (r *replica) user(name string) principal.User {
	r.t.Helper()
	u, err := r.manager.CreateUser(context.Background(), admin, principal.NewUser{Name: name, Email: name + "@example.com"})
	if err != nil {
		r.t.Fatalf("CreateUser(%s): %v", name, err)
	}
	return u
}

func (r *replica) serviceAccount(name string) principal.ServiceAccount {
	r.t.Helper()
	sa, err := r.manager.CreateServiceAccount(context.Background(), admin, principal.NewServiceAccount{Name: name})
	if err != nil {
		r.t.Fatalf("CreateServiceAccount(%s): %v", name, err)
	}
	return sa
}

// pat creates a personal access token for the user.
func (r *replica) pat(u principal.User, mutate ...func(*token.CreateRequest)) token.Created {
	r.t.Helper()
	req := token.CreateRequest{Caller: principal.Caller{Actor: audit.Actor{Kind: "user", ID: u.ID, Name: u.Name}}, Type: token.TypePAT, OwnerID: u.ID, Description: "test", Via: "api"}
	for _, m := range mutate {
		m(&req)
	}
	c, err := r.tokens.Create(context.Background(), req)
	if err != nil {
		r.t.Fatalf("Create PAT: %v", err)
	}
	return c
}

func (r *replica) sat(sa principal.ServiceAccount, mutate ...func(*token.CreateRequest)) token.Created {
	r.t.Helper()
	req := token.CreateRequest{Caller: admin, Type: token.TypeSAT, OwnerID: sa.ID, Description: "test", Via: "admin-cli", Admin: true}
	for _, m := range mutate {
		m(&req)
	}
	c, err := r.tokens.Create(context.Background(), req)
	if err != nil {
		r.t.Fatalf("Create SAT: %v", err)
	}
	return c
}
