//go:build integration

// Package authz tests the authorization area against the integration stack: the
// policy and role-binding store, the compiled grant index of several in-process
// replicas that follow the change log, the epochs of R-AUTHZ-032 and the audit of
// R-AUTHZ-029. Every test creates a database of its own, so parallel tests and
// agents never collide. Valkey is not part of these tests: every propagation
// here is the one that works with Valkey stopped (R-AUTHZ-028).
package authz

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
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
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

const schemaName = "trove"

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// env is one test's database.
type env struct {
	t     *testing.T
	cfg   config.Database
	admin *pgx.Conn
	mu    sync.Mutex // the admin connection is not safe for concurrent use
	// anonymous is the switch of R-AUTH-037 the replicas read.
	anonymous atomic.Bool
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
	name := "trove_authz_" + randHex(5)
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
		Host: host, Port: port, Name: name, Schema: schemaName,
		UsernameFile: userFile, PasswordFile: passFile,
		SSLMode: "verify-full", SSLRootCertFile: testenv.CAFile(t),
		ConnectTimeout: 5 * time.Second, TargetSessionAttrs: "read-write",
		Pooler:        config.DatabasePooler{Mode: "none"},
		Pool:          config.DatabasePool{MaxConns: 8, MinConns: 1, MaxConnLifetime: 30 * time.Minute, MaxConnLifetimeJitter: time.Minute, MaxConnIdleTime: 5 * time.Minute, HealthCheckPeriod: 30 * time.Second},
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
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := postgres.Migrate(ctx, cfg, postgres.MigrateOptions{
		Instance: "it-migrate", Version: "0.1.0", Log: quiet,
		Migrate: config.Migrate{AdvisoryLockWait: 20 * time.Second, DDLLockTimeout: 2 * time.Second, DDLLockRetries: 3},
	}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if testenv.PgPooled(t) {
		cfg = testenv.ThroughPgBouncer(t, cfg, user, maint)
	}
	return &env{t: t, cfg: cfg, admin: connect(name)}
}

// exec runs a statement as the superuser.
func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, err := e.admin.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

func (e *env) text(sql string, args ...any) string {
	e.t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	var v string
	if err := e.admin.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return v
}

func (e *env) number(sql string, args ...any) int64 {
	e.t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	var v int64
	if err := e.admin.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return v
}

// The rows the tests need, inserted directly (the identity and repository
// areas implement the real writers in parallel).

type person struct {
	ID, Name string
	Kind     principal.Kind
}

func (e *env) principalRow(kind, name string) string {
	return e.text("INSERT INTO principal (id, kind, name) VALUES (gen_random_uuid(), $1, $2) RETURNING id::text", kind, name)
}

func (e *env) user(name string) person {
	id := e.principalRow("user", name)
	e.exec("INSERT INTO app_user (principal_id, origin) VALUES ($1::uuid, 'admin')", id)
	return person{ID: id, Name: name, Kind: principal.KindUser}
}

func (e *env) group(name string, autoJoin bool) person {
	id := e.principalRow("group", name)
	e.exec("INSERT INTO user_group (principal_id, origin, auto_join) VALUES ($1::uuid, 'admin', $2)", id, autoJoin)
	return person{ID: id, Name: name, Kind: principal.KindGroup}
}

func (e *env) serviceAccount(name string) person {
	id := e.principalRow("service_account", name)
	e.exec("INSERT INTO service_account (principal_id) VALUES ($1::uuid)", id)
	return person{ID: id, Name: name, Kind: principal.KindServiceAccount}
}

func (e *env) ciMapping(name string) person {
	return person{ID: e.principalRow("ci_mapping", name), Name: name, Kind: principal.KindCI}
}

func (e *env) repo(key, class, format, cfg string) {
	e.t.Helper()
	if cfg == "" {
		cfg = "{}"
	}
	e.exec("INSERT INTO repositories (key, class, format, config) VALUES ($1, $2, $3, $4::jsonb)", key, class, format, cfg)
}

// sub builds the subject the authentication stage would hand over.
func sub(p person, groups ...person) authz.Subject {
	s := authz.Subject{Kind: p.Kind, ID: p.ID, Name: p.Name}
	for _, g := range groups {
		s.Groups = append(s.Groups, principal.GroupRef{ID: g.ID, Name: g.Name})
	}
	return s
}

// sqlPrincipals and sqlRepositories read the contract interfaces of the identity
// and repository areas straight from the tables.
type sqlPrincipals struct{ e *env }

func (s sqlPrincipals) ByID(ctx context.Context, id string) (principal.Principal, error) {
	return s.query(ctx, "SELECT id::text, kind, name, status FROM principal WHERE id = $1::uuid", id)
}

func (s sqlPrincipals) ByName(ctx context.Context, kind principal.Kind, name string) (principal.Principal, error) {
	k := string(kind)
	if kind == principal.KindCI {
		k = "ci_mapping"
	}
	return s.query(ctx, "SELECT id::text, kind, name, status FROM principal WHERE kind = $1 AND name = $2", k, name)
}

func (s sqlPrincipals) query(ctx context.Context, sql string, args ...any) (principal.Principal, error) {
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

func (s sqlPrincipals) Groups(context.Context, string) ([]principal.GroupRef, error) { return nil, nil }

type sqlRepositories struct{ e *env }

func (s sqlRepositories) list(ctx context.Context, where string, args ...any) ([]repository.Repository, error) {
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

func (s sqlRepositories) ByKey(ctx context.Context, key string) (repository.Repository, error) {
	rs, err := s.list(ctx, "WHERE key = $1", key)
	if err != nil {
		return repository.Repository{}, err
	}
	if len(rs) == 0 {
		return repository.Repository{}, errs.E(errs.NotFound, errs.CodeRepositoryNotFound, "no such repository")
	}
	return rs[0], nil
}

func (s sqlRepositories) List(ctx context.Context) ([]repository.Repository, error) {
	return s.list(ctx, "")
}

// recorder keeps the audit events of a replica.
type recorder struct {
	mu     sync.Mutex
	events []audit.Event
}

func (r *recorder) Record(_ context.Context, ev audit.Event) {
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
}

func (r *recorder) RecordTx(_ context.Context, _ meta.Tx, ev audit.Event) error {
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
	return nil
}

func (r *recorder) ofType(name string) []audit.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []audit.Event
	for _, ev := range r.events {
		if ev.Type == name {
			out = append(out, ev)
		}
	}
	return out
}

// replicaOpts describe one in-process replica.
type replicaOpts struct {
	// poll is cluster.epoch_poll_interval; staleness is authz.cache.max_staleness.
	poll, staleness time.Duration
	reads           string
}

// replica is one in-process replica with its pools, change log, engine and
// policy service. Valkey is not part of it: the change log runs on PostgreSQL
// alone, which is the state "Valkey stopped".
type replica struct {
	name    string
	reg     *prometheus.Registry
	db      *postgres.DB
	changes *cluster.Changes
	engine  *authz.Engine
	svc     *authz.Service
	rec     *recorder
	stopped sync.Once
}

func (e *env) newReplica(name string, o replicaOpts) *replica {
	t := e.t
	t.Helper()
	if o.poll == 0 {
		o.poll = 500 * time.Millisecond
	}
	if o.staleness == 0 {
		o.staleness = 10 * time.Second
	}
	if o.reads == "" {
		o.reads = config.AuthzReadsDenied
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	r := &replica{name: name, reg: prometheus.NewRegistry(), rec: &recorder{}}
	var err error
	if r.db, err = postgres.Open(ctx, e.cfg, name, "web", quiet); err != nil {
		t.Fatalf("Open(%s): %v", name, err)
	}
	t.Cleanup(r.stop)
	clu := config.Cluster{
		LeaseRetryInterval: 500 * time.Millisecond, EpochPollInterval: o.poll, EpochMaxStaleness: 20 * o.poll,
		ChangeRetention: time.Hour, SignalCoalesceWindow: 20 * time.Millisecond,
	}
	if clu.EpochMaxStaleness < 15*time.Second {
		clu.EpochMaxStaleness = 15 * time.Second
	}
	if r.changes, err = cluster.NewChanges(cluster.ChangesOptions{
		DB: r.db.ControlPool(), Config: clu, WatchdogTimeout: 30 * time.Second, Log: quiet, Metrics: r.reg,
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.changes.Start(ctx); err != nil {
		t.Fatalf("changes.Start: %v", err)
	}
	cfg := config.Authz{
		HideExistence: true,
		Cache:         config.AuthzCache{MaxStaleness: o.staleness, SafetyTTL: time.Hour},
		Audit:         config.AuthzAudit{Reads: o.reads},
	}
	r.engine, err = authz.New(authz.Options{
		Source: authz.NewPostgresSource(r.db, o.staleness), Changes: r.changes, Config: cfg,
		EpochPollInterval: o.poll, RetryBudget: 5 * time.Second,
		AnonymousAccess: func(context.Context) (bool, error) { return e.anonymous.Load(), nil }, Recorder: r.rec, Replica: name, Log: quiet, Metrics: r.reg,
		Repositories: sqlRepositories{e},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.engine.Start(ctx); err != nil {
		t.Fatalf("engine.Start: %v", err)
	}
	r.svc = authz.NewService(authz.ServiceOptions{
		DB: r.db, Changes: r.changes, Principals: sqlPrincipals{e}, Repositories: sqlRepositories{e}, Recorder: r.rec,
	})
	return r
}

func (r *replica) stop() {
	r.stopped.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if r.engine != nil {
			_ = r.engine.Stop(ctx)
		}
		if r.changes != nil {
			_ = r.changes.Stop(ctx)
		}
		if r.db != nil {
			r.db.Close()
		}
	})
}

// waitFor polls cond until it holds or the time is up.
func waitFor(t testing.TB, what string, within time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s did not happen within %s", what, within)
}

func read(repo, pkg, path string) authz.Coordinate {
	return authz.Coordinate{Repository: repo, Package: pkg, Path: path, Kind: authz.KindArtifact, Actions: []authz.Action{authz.Read}}
}

func write(repo, pkg, path string, actions ...authz.Action) authz.Coordinate {
	return authz.Coordinate{Repository: repo, Package: pkg, Path: path, Kind: authz.KindWrite, Actions: actions}
}
