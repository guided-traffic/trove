//go:build integration

// Package stack builds the access API of milestone M1 for the integration tests
// of the authorization and audit areas: one migrated database per test and any
// number of in-process web replicas on it. A replica has pools of its own, the
// change log, the authorization engine and service, the audit writer and store
// and an HTTP listener with the real chain and the generated handlers of the
// management API, so a request exercises the path a client takes: the
// authorization step of the operation's declaration, the handler, the service,
// the audit event, and the grant index of the replica that answers.
//
// What is a stand-in: authentication (a request names its person in the header
// X-Test-Person; the identity area has its own tests), and the principal and
// repository readers (they read the tables directly). Valkey is not part of the
// stack: every propagation here is the one that works with Valkey stopped
// (R-AUTHZ-028).
package stack

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/api/v1/apitest"
	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	_ "github.com/guided-traffic/trove/internal/formats/all" // registers the formats the catalog answers for
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/test/integration/idapi"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// RandHex returns n random bytes in hex.
func RandHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// Person is a principal the test created. Groups are resolved from the
// membership table at request time, as the authentication stage does.
type Person struct {
	ID, Name string
	Kind     principal.Kind
}

// Env is one test's migrated database.
type Env struct {
	T   *testing.T
	Cfg config.Database

	admin  *pgx.Conn
	mu     sync.Mutex // the admin connection is not safe for concurrent use
	people sync.Map   // name -> Person
}

// New creates a database trove_stack_<random>, migrates it (audit retention 365
// days) and drops it when the test ends.
func New(t *testing.T) *Env {
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
	name := "trove_stack_" + RandHex(5)
	maint := connect("postgres")
	if _, err := maint.Exec(context.Background(), "CREATE DATABASE "+name+" ENCODING 'UTF8' TEMPLATE template0"); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = maint.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})
	dir := t.TempDir()
	userFile, passFile := filepath.Join(dir, "username"), filepath.Join(dir, "password")
	if err := os.WriteFile(userFile, []byte(user+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(passFile, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Database{
		Host: host, Port: port, Name: name, Schema: "trove",
		UsernameFile: userFile, PasswordFile: passFile,
		SSLMode: "verify-full", SSLRootCertFile: testenv.CAFile(t),
		ConnectTimeout: 5 * time.Second, TargetSessionAttrs: "read-write",
		Pooler:        config.DatabasePooler{Mode: "none"},
		Pool:          config.DatabasePool{MaxConns: 10, MinConns: 1, MaxConnLifetime: 30 * time.Minute, MaxConnLifetimeJitter: time.Minute, MaxConnIdleTime: 5 * time.Minute, HealthCheckPeriod: 30 * time.Second},
		LeasePoolSize: 2, LeaseCheckInterval: time.Second,
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
		Instance: "it-migrate", Version: "0.1.0", Log: quiet(), AuditRetentionDays: &days,
		Migrate: config.Migrate{AdvisoryLockWait: 20 * time.Second, DDLLockTimeout: 2 * time.Second, DDLLockRetries: 3},
	}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if testenv.PgPooled(t) {
		cfg = testenv.ThroughPgBouncer(t, cfg, user, maint)
	}
	return &Env{T: t, Cfg: cfg, admin: connect(name)}
}

// Exec runs a statement as the superuser.
func (e *Env) Exec(sql string, args ...any) {
	e.T.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, err := e.admin.Exec(context.Background(), sql, args...); err != nil {
		e.T.Fatalf("%s: %v", sql, err)
	}
}

// Text returns the first column of the first row of a query.
func (e *Env) Text(sql string, args ...any) string {
	e.T.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	var v string
	if err := e.admin.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		e.T.Fatalf("%s: %v", sql, err)
	}
	return v
}

// Number returns the first column of the first row of a query.
func (e *Env) Number(sql string, args ...any) int64 {
	e.T.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	var v int64
	if err := e.admin.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		e.T.Fatalf("%s: %v", sql, err)
	}
	return v
}

func (e *Env) principalRow(kind, name string) string {
	return e.Text("INSERT INTO principal (id, kind, name) VALUES (gen_random_uuid(), $1, $2) RETURNING id::text", kind, name)
}

func (e *Env) remember(p Person) Person {
	e.people.Store(p.Name, p)
	return p
}

// User creates a user; the person can then be named in X-Test-Person.
func (e *Env) User(name string) Person {
	id := e.principalRow("user", name)
	e.Exec("INSERT INTO app_user (principal_id, origin) VALUES ($1::uuid, 'admin')", id)
	return e.remember(Person{ID: id, Name: name, Kind: principal.KindUser})
}

// Group creates a group.
func (e *Env) Group(name string) Person {
	id := e.principalRow("group", name)
	e.Exec("INSERT INTO user_group (principal_id, origin, auto_join) VALUES ($1::uuid, 'admin', false)", id)
	return e.remember(Person{ID: id, Name: name, Kind: principal.KindGroup})
}

// ServiceAccount creates a service account.
func (e *Env) ServiceAccount(name string) Person {
	id := e.principalRow("service_account", name)
	e.Exec("INSERT INTO service_account (principal_id) VALUES ($1::uuid)", id)
	return e.remember(Person{ID: id, Name: name, Kind: principal.KindServiceAccount})
}

// Join adds a member to a group. Do it before the replicas start or accept that
// the index follows it with the next authz change: membership is read at request
// time, not by the index.
func (e *Env) Join(group, member Person) {
	e.Exec("INSERT INTO group_membership (group_id, member_id, source) VALUES ($1::uuid, $2::uuid, 'manual')", group.ID, member.ID)
}

// Admin binds the role admin to a person straight in the table; do it before the
// replicas start.
func (e *Env) Admin(p Person) {
	e.Exec("INSERT INTO authz_role_binding (role, principal_id) VALUES ('admin', $1::uuid)", p.ID)
}

// Repo inserts a repository; do it before the replicas start.
func (e *Env) Repo(key, class, format string) {
	e.T.Helper()
	e.Exec("INSERT INTO repositories (key, class, format, config) VALUES ($1, $2, $3, '{}'::jsonb)", key, class, format)
}

// AuthzEpoch is the authorization epoch in the database.
func (e *Env) AuthzEpoch() int64 {
	return e.Number("SELECT epoch FROM cluster_epoch WHERE area = 'authz'")
}

func (e *Env) groupsOf(ctx context.Context, id string) ([]principal.GroupRef, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	rows, err := e.admin.Query(ctx, `SELECT p.id::text, p.name FROM group_membership m JOIN principal p ON p.id = m.group_id
		WHERE m.member_id = $1::uuid ORDER BY p.name`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []principal.GroupRef
	for rows.Next() {
		var g principal.GroupRef
		if err := rows.Scan(&g.ID, &g.Name); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// principals and repositories read the contract interfaces of the identity and
// repository areas straight from the tables.
type principals struct{ e *Env }

func (s principals) ByID(ctx context.Context, id string) (principal.Principal, error) {
	return s.query(ctx, "SELECT id::text, kind, name, status FROM principal WHERE id = $1::uuid", id)
}

func (s principals) ByName(ctx context.Context, kind principal.Kind, name string) (principal.Principal, error) {
	k := string(kind)
	if kind == principal.KindCI {
		k = "ci_mapping"
	}
	return s.query(ctx, "SELECT id::text, kind, name, status FROM principal WHERE kind = $1 AND name = $2", k, name)
}

func (s principals) Groups(ctx context.Context, id string) ([]principal.GroupRef, error) {
	return s.e.groupsOf(ctx, id)
}

func (s principals) query(ctx context.Context, sql string, args ...any) (principal.Principal, error) {
	s.e.mu.Lock()
	defer s.e.mu.Unlock()
	var p principal.Principal
	var kind, status string
	err := s.e.admin.QueryRow(ctx, sql, args...).Scan(&p.ID, &kind, &p.Name, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return principal.Principal{}, principal.ErrNotFound
	}
	if err != nil {
		return principal.Principal{}, err
	}
	p.Kind, p.Status = principal.Kind(kind), principal.Status(status)
	if kind == "ci_mapping" {
		p.Kind = principal.KindCI
	}
	return p, nil
}

type repositories struct{ e *Env }

func (s repositories) list(ctx context.Context, where string, args ...any) ([]repository.Repository, error) {
	s.e.mu.Lock()
	defer s.e.mu.Unlock()
	rows, err := s.e.admin.Query(ctx, "SELECT id, key, class, format, config FROM repositories "+where+" ORDER BY key", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []repository.Repository
	for rows.Next() {
		var r repository.Repository
		var id int64
		var class string
		if err := rows.Scan(&id, &r.Key, &class, &r.Format, &r.Config); err != nil {
			return nil, err
		}
		r.ID, r.Class = strconv.FormatInt(id, 10), repository.Class(class)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s repositories) ByKey(ctx context.Context, key string) (repository.Repository, error) {
	rs, err := s.list(ctx, "WHERE key = $1", key)
	if err != nil {
		return repository.Repository{}, err
	}
	if len(rs) == 0 {
		return repository.Repository{}, errs.E(errs.NotFound, errs.CodeRepositoryNotFound, "no such repository")
	}
	return rs[0], nil
}

func (s repositories) List(ctx context.Context) ([]repository.Repository, error) {
	return s.list(ctx, "")
}

// Options describe one in-process replica.
type Options struct {
	// Poll is cluster.epoch_poll_interval (default 500 ms); Staleness is
	// authz.cache.max_staleness (default 10 s).
	Poll, Staleness time.Duration
	// Audit is the audit section; the zero value takes a 1 minute denial window.
	Audit config.Audit
	// Reads is authz.audit.reads (default denied).
	Reads string
	// Validator, when set, checks every answer against the document.
	Validator *apitest.Validator
}

// LogSink collects the records of the audit log stream of a replica.
type LogSink struct {
	mu   sync.Mutex
	recs []map[string]any
}

// Handle implements slog.Handler by keeping every record as a JSON object.
func (s *LogSink) handler() slog.Handler {
	return slog.NewJSONHandler(sinkWriter{s}, &slog.HandlerOptions{Level: slog.LevelInfo})
}

type sinkWriter struct{ s *LogSink }

func (w sinkWriter) Write(p []byte) (int, error) {
	var m map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(p), &m); err == nil {
		w.s.mu.Lock()
		w.s.recs = append(w.s.recs, m)
		w.s.mu.Unlock()
	}
	return len(p), nil
}

// Has reports whether the stream holds a log: audit record of the event ID.
func (s *LogSink) Has(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.recs {
		if r["id"] == id && r["msg"] == "audit" {
			return true
		}
	}
	return false
}

// Replica is one in-process web replica.
type Replica struct {
	Name    string
	E       *Env
	DB      *postgres.DB
	Changes *cluster.Changes
	Engine  *authz.Engine
	Service *authz.Service
	// Repos is the repository service with the authorization area's hook, which
	// records authz.settings.changed (R-AUTHZ-031).
	Repos  *repository.Service
	Audit  *audit.Writer
	Store  *audit.Store
	Log    *LogSink
	Server *httptest.Server

	reg     *prometheus.Registry
	stopped sync.Once
}

// NewReplica starts a replica on the environment's database. Create the people,
// groups, repositories and administrators first: the index is built at start.
func (e *Env) NewReplica(name string, o Options) *Replica {
	t := e.T
	t.Helper()
	if o.Poll == 0 {
		o.Poll = 500 * time.Millisecond
	}
	if o.Staleness == 0 {
		o.Staleness = 10 * time.Second
	}
	if o.Reads == "" {
		o.Reads = config.AuthzReadsDenied
	}
	if o.Audit.AsyncBuffer == 0 {
		o.Audit.AsyncBuffer = 1000
	}
	if o.Audit.DenialDedupWindow == 0 {
		o.Audit.DenialDedupWindow = time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	r := &Replica{Name: name, E: e, reg: prometheus.NewRegistry(), Log: &LogSink{}}
	var err error
	if r.DB, err = postgres.Open(ctx, e.Cfg, name, "web", quiet()); err != nil {
		t.Fatalf("Open(%s): %v", name, err)
	}
	t.Cleanup(r.Stop)
	clu := config.Cluster{
		LeaseRetryInterval: 500 * time.Millisecond, EpochPollInterval: o.Poll, EpochMaxStaleness: 20 * o.Poll,
		ChangeRetention: time.Hour, SignalCoalesceWindow: 20 * time.Millisecond,
	}
	if clu.EpochMaxStaleness < 15*time.Second {
		clu.EpochMaxStaleness = 15 * time.Second
	}
	if r.Changes, err = cluster.NewChanges(cluster.ChangesOptions{
		DB: r.DB.ControlPool(), Config: clu, WatchdogTimeout: 30 * time.Second, Log: quiet(), Metrics: r.reg,
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.Changes.Start(ctx); err != nil {
		t.Fatalf("changes.Start: %v", err)
	}
	w, err := audit.New(audit.Options{
		DB: r.DB, Replica: name, Config: o.Audit, Log: slog.New(r.Log.handler()), AppLog: quiet(),
	})
	if err != nil {
		t.Fatal(err)
	}
	w.Start()
	r.Audit, r.Store = w, audit.NewStore(r.DB)
	rec := v1.WithRequestSource(w)

	r.Engine, err = authz.New(authz.Options{
		Source: authz.NewPostgresSource(r.DB, o.Staleness), Changes: r.Changes,
		Config: config.Authz{
			HideExistence: true, Cache: config.AuthzCache{MaxStaleness: o.Staleness, SafetyTTL: time.Hour},
			Audit: config.AuthzAudit{Reads: o.Reads},
		},
		Audit: o.Audit, EpochPollInterval: o.Poll, RetryBudget: 5 * time.Second,
		Recorder: rec, Replica: name, Log: quiet(), Metrics: r.reg,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Engine.Start(ctx); err != nil {
		t.Fatalf("engine.Start: %v", err)
	}
	r.Service = authz.NewService(authz.ServiceOptions{
		DB: r.DB, Changes: r.Changes, Principals: principals{e}, Repositories: repositories{e}, Recorder: rec,
	})
	if r.Repos, err = repository.NewService(repository.Options{
		Runner: r.DB, Catalog: catalog{}, Recorder: rec, Notifier: r.Changes, Hooks: authz.NewSettingsHook(rec), Log: quiet(),
	}); err != nil {
		t.Fatal(err)
	}
	r.serve(o, rec)
	return r
}

// FormatCatalog is the format catalog the composition root builds from the
// registry, for tests of other areas that build a repository reader.
func FormatCatalog() repository.FormatCatalog { return catalog{} }

// catalog is the format catalog the composition root builds from the registry.
type catalog struct{}

func (catalog) Format(name string) (repository.FormatInfo, bool) {
	f, ok := formats.Lookup(name)
	if !ok {
		return repository.FormatInfo{}, false
	}
	d := f.Descriptor()
	info := repository.FormatInfo{
		Name: string(d.ID), Layout: d.Layout, ProtocolBasePath: d.ProtocolBasePath,
		PatternSubject: repository.PatternSubject(d.PatternSubject), PackageNames: d.PackageNames,
	}
	for _, c := range d.Classes {
		info.Classes = append(info.Classes, repository.Class(c.String()))
	}
	return info, true
}

// testAuth reads the caller from X-Test-Person (a name made by User, Group or
// ServiceAccount) and applies Trove-Authz-Min-Epoch to an authenticated caller,
// as the composition root does.
func (r *Replica) testAuth(req *http.Request) (httpserver.Principal, error) {
	name := req.Header.Get("X-Test-Person")
	if name == "" {
		return httpserver.AnonymousPrincipal(), nil
	}
	v, ok := r.E.people.Load(name)
	if !ok {
		return httpserver.Principal{}, errs.E(errs.Unauthenticated, errs.CodeUnauthenticated, "unknown test person", errs.WithChallenge("Bearer"))
	}
	p := v.(Person)
	groups, err := r.E.groupsOf(req.Context(), p.ID)
	if err != nil {
		return httpserver.Principal{}, err
	}
	out := httpserver.Principal{
		ID: p.ID, Name: p.Name, Kind: httpserver.PrincipalKind(p.Kind),
		Credential: httpserver.Credential{Method: httpserver.MethodBearer, TokenID: "00000000-0000-4000-8000-" + strings.Repeat("0", 11) + "1"},
	}
	for _, g := range groups {
		out.Groups = append(out.Groups, httpserver.GroupRef{ID: g.ID, Name: g.Name})
	}
	n, err := authz.ParseMinEpoch(req.Header.Get(authz.HeaderMinEpoch))
	if err != nil {
		return httpserver.Principal{}, err
	}
	out.MinEpoch = n
	return out, nil
}

func (r *Replica) serve(o Options, rec audit.Recorder) {
	t := r.E.T
	cfg := config.API{
		PageSizeDefault: 50, PageSizeMax: 500, IdempotencyRetention: time.Hour,
		MaxRequestBodyBytes: config.ByteSize(1 << 20),
	}
	// The handler is the one the identity and login tests use (test/integration/idapi),
	// with the stand-in authentication and the engine and services of this replica.
	h := idapi.Handler(t, idapi.Components{
		Name: r.Name, DB: r.DB, Changes: r.Changes, Recorder: rec, BaseURL: "http://trove.test", Log: quiet(),
		Engine: r.Engine, Authenticate: r.testAuth, Validator: o.Validator,
		Options: func(_ *authz.Engine, o *v1.Options) {
			o.AuditStore = r.Store
			o.Authz = v1.AuthzServices{Policies: r.Service, Engine: r.Engine}
			o.API = v1.Services{Config: cfg, Store: r.DB, Audit: rec}
		},
	})
	r.Server = httptest.NewServer(h)
}

// Stop shuts the replica down: the listener, the audit buffer, the engine, the
// change log and the pools.
func (r *Replica) Stop() {
	r.stopped.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if r.Server != nil {
			r.Server.Close()
		}
		if r.Engine != nil {
			_ = r.Engine.Stop(ctx)
		}
		if r.Audit != nil {
			_ = r.Audit.Flush(ctx)
		}
		if r.Changes != nil {
			_ = r.Changes.Stop(ctx)
		}
		if r.DB != nil {
			r.DB.Close()
		}
	})
}
