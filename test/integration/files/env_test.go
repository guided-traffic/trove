//go:build integration

// Package files tests the repository API and the raw file transfer against the
// integration stack: PostgreSQL, the S3 store and, in-process, several replicas
// that share them. A replica here is the whole request path of a web replica
// short of authentication: the PostgreSQL pools, the change log, the grant index of
// the decision point, the repository snapshot and service, the blob store with its
// spool, the files service and the HTTP handlers behind the middleware chain, served
// on a listener of its own. Authentication is a test stage that maps a bearer name to
// a principal; everything after it is the production code. Valkey is not part of
// these tests: nothing in the transfer needs it (R-ARCH-032), and every propagation
// here is the one that works with Valkey stopped.
package files

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/api/edge"
	"github.com/guided-traffic/trove/internal/api/rawfiles"
	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/api/v1/apitest"
	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/blob/blobtest"
	"github.com/guided-traffic/trove/internal/blob/s3"
	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/files"
	_ "github.com/guided-traffic/trove/internal/formats/all"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/outbound"
	"github.com/guided-traffic/trove/internal/proxy/invalidation"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

const schemaName = "trove"

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func uuidV4() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// person is a principal row of the test database and the credential that names it.
type person struct {
	ID, Name string
	Kind     principal.Kind
	Groups   []principal.GroupRef
}

// env is one test's database, object-store prefix and users.
type env struct {
	t     *testing.T
	db    string
	cfg   config.Database
	admin *pgx.Conn
	mu    sync.Mutex // the admin connection is not safe for concurrent use
	s3    *s3Env

	// anonymous is the switch of R-AUTH-037 the replicas read.
	anonymous atomic.Bool

	peopleMu sync.Mutex
	people   map[string]person // bearer name -> principal
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
	name := "trove_files_" + randHex(5)
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
		Pool:          config.DatabasePool{MaxConns: 12, MinConns: 1, MaxConnLifetime: 30 * time.Minute, MaxConnLifetimeJitter: time.Minute, MaxConnIdleTime: 5 * time.Minute, HealthCheckPeriod: 30 * time.Second},
		LeasePoolSize: 3, LeaseCheckInterval: time.Second,
		Session: config.DatabaseSession{
			StatementTimeout: 60 * time.Second, LockTimeout: 10 * time.Second,
			IdleInTransactionSessionTimeout: 120 * time.Second,
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
	e := &env{t: t, db: name, cfg: cfg, admin: connect(name), people: map[string]person{}}
	e.s3 = newS3(t)
	return e
}

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

// user creates a user (and the groups it belongs to) and registers its bearer name.
func (e *env) user(name string, groups ...string) person {
	e.t.Helper()
	id := e.text("INSERT INTO principal (id, kind, name) VALUES (gen_random_uuid(), 'user', $1) RETURNING id::text", name)
	e.exec("INSERT INTO app_user (principal_id, origin) VALUES ($1::uuid, 'admin')", id)
	p := person{ID: id, Name: name, Kind: principal.KindUser}
	for _, g := range groups {
		gid, err := e.groupID(g)
		if err != nil {
			gid = e.text("INSERT INTO principal (id, kind, name) VALUES (gen_random_uuid(), 'group', $1) RETURNING id::text", g)
			e.exec("INSERT INTO user_group (principal_id, origin, auto_join) VALUES ($1::uuid, 'admin', false)", gid)
		}
		p.Groups = append(p.Groups, principal.GroupRef{ID: gid, Name: g})
	}
	e.peopleMu.Lock()
	e.people[name] = p
	e.peopleMu.Unlock()
	return p
}

func (e *env) groupID(name string) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var id string
	err := e.admin.QueryRow(context.Background(), "SELECT id::text FROM principal WHERE kind = 'group' AND name = $1", name).Scan(&id)
	return id, err
}

// breakGlass registers the break-glass administrator under the bearer name admin: it
// holds the administrator role without a binding (R-AUTHZ-010).
func (e *env) breakGlass() {
	e.peopleMu.Lock()
	defer e.peopleMu.Unlock()
	e.people["admin"] = person{ID: uuidV4(), Name: "admin", Kind: principal.KindBreakGlass}
}

// authenticate is the test stage of the chain: a bearer name is a principal, no
// header is the anonymous caller, an unknown name is 401.
func (e *env) authenticate(r *http.Request) (httpserver.Principal, error) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return httpserver.AnonymousPrincipal(), nil
	}
	name := strings.TrimPrefix(h, "Bearer ")
	e.peopleMu.Lock()
	p, ok := e.people[name]
	e.peopleMu.Unlock()
	if !ok {
		return httpserver.Principal{}, errs.E(errs.Unauthenticated, errs.CodeInvalidToken, "The token is not valid.", errs.WithChallenge("Bearer"))
	}
	out := httpserver.Principal{
		Kind: httpserver.PrincipalKind(p.Kind), ID: p.ID, Name: p.Name,
		Credential: httpserver.Credential{Method: httpserver.MethodBearer, TokenID: "test-" + p.Name},
	}
	for _, g := range p.Groups {
		out.Groups = append(out.Groups, httpserver.GroupRef{ID: g.ID, Name: g.Name})
	}
	return out, nil
}

// s3Env is the object store under test: a prefix of its own in the shared bucket.
type s3Env struct {
	cfg    config.Storage
	raw    blob.ObjectStore
	prefix string
}

func newS3(t *testing.T) *s3Env {
	t.Helper()
	dir := t.TempDir()
	writeSecret := func(file, v string) string {
		p := filepath.Join(dir, file)
		if err := os.WriteFile(p, []byte(v+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cfg := blobtest.Storage(t, t.TempDir())
	cfg.S3.Endpoint = testenv.Get(t, "TROVE_TEST_S3_ENDPOINT")
	cfg.S3.Region = testenv.Get(t, "TROVE_TEST_S3_REGION")
	cfg.S3.Bucket = testenv.Get(t, "TROVE_TEST_S3_BUCKET_BLOBS")
	cfg.S3.ForcePathStyle = testenv.Get(t, "TROVE_TEST_S3_FORCE_PATH_STYLE") == "true"
	cfg.S3.CAFile = testenv.CAFile(t)
	cfg.S3.Prefix = "it-files/" + randHex(5)
	cfg.S3.Credentials = config.StorageS3Credentials{
		Source:              "static",
		AccessKeyIDFile:     writeSecret("access-key", testenv.Get(t, "TROVE_TEST_S3_ACCESS_KEY")),
		SecretAccessKeyFile: writeSecret("secret-key", testenv.Get(t, "TROVE_TEST_S3_SECRET_KEY")),
	}
	// The transfer is the thing under test: spool admission must not depend on
	// the free space of the machine running the tests.
	cfg.Spool.MinFreeBytes = 0
	f := outbound.New(config.Outbound{TLS: config.OutboundTLS{UseSystemRoots: true}})
	c, err := s3.NewFromFactory(context.Background(), cfg.S3, f)
	if err != nil {
		t.Fatalf("object store client: %v", err)
	}
	e := &s3Env{cfg: cfg, raw: c, prefix: cfg.S3.Prefix}
	t.Cleanup(e.removeAll)
	return e
}

func (e *s3Env) removeAll() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	token := ""
	for {
		page, err := e.raw.List(ctx, e.prefix+"/", blob.ListOptions{Token: token})
		if err != nil {
			return
		}
		for _, o := range page.Objects {
			_ = e.raw.Delete(ctx, o.Key)
		}
		if page.Next == "" {
			return
		}
		token = page.Next
	}
}

// countingStore counts the requests of one replica's object-store client.
type countingStore struct {
	blob.ObjectStore
	gets, puts, parts atomic.Int64
	// putDelay, in nanoseconds, delays every Put and CompleteMultipart: the tests
	// that hold an upload in its commit use it.
	putDelay atomic.Int64
}

func (c *countingStore) delay(ctx context.Context) {
	if d := time.Duration(c.putDelay.Load()); d > 0 {
		select {
		case <-time.After(d):
		case <-ctx.Done():
		}
	}
}

func (c *countingStore) CompleteMultipart(ctx context.Context, key, id string, parts []blob.Part) error {
	c.delay(ctx)
	return c.ObjectStore.CompleteMultipart(ctx, key, id, parts)
}

func (c *countingStore) Get(ctx context.Context, key string, rng *blob.ByteRange) (io.ReadCloser, blob.ObjectInfo, error) {
	c.gets.Add(1)
	return c.ObjectStore.Get(ctx, key, rng)
}

func (c *countingStore) Put(ctx context.Context, key string, body io.ReadSeeker, size int64, sum blob.Checksum) error {
	c.puts.Add(1)
	c.delay(ctx)
	return c.ObjectStore.Put(ctx, key, body, size, sum)
}

func (c *countingStore) UploadPart(ctx context.Context, key, id string, n int, body io.ReadSeeker, size int64, sum blob.Checksum) (blob.Part, error) {
	c.parts.Add(1)
	return c.ObjectStore.UploadPart(ctx, key, id, n, body, size, sum)
}

// recorder keeps the audit events of a replica that were committed or recorded.
type recorder struct {
	mu     sync.Mutex
	events []audit.Event
}

func (r *recorder) Record(_ context.Context, ev audit.Event) {
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
}

func (r *recorder) RecordTx(_ context.Context, tx meta.Tx, ev audit.Event) error {
	tx.After(func() {
		r.mu.Lock()
		r.events = append(r.events, ev)
		r.mu.Unlock()
	})
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

// sqlPrincipals reads the principal table for the policy service.
type sqlPrincipals struct{ e *env }

func (s sqlPrincipals) ByID(ctx context.Context, id string) (principal.Principal, error) {
	return s.query(ctx, "SELECT id::text, kind, name, status FROM principal WHERE id = $1::uuid", id)
}

func (s sqlPrincipals) ByName(ctx context.Context, kind principal.Kind, name string) (principal.Principal, error) {
	return s.query(ctx, "SELECT id::text, kind, name, status FROM principal WHERE kind = $1 AND name = $2", string(kind), name)
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
	return p, nil
}

func (s sqlPrincipals) Groups(context.Context, string) ([]principal.GroupRef, error) { return nil, nil }

// replicaOpts describe one in-process replica.
type replicaOpts struct {
	// worker makes the replica run jobs (the deletion jobs).
	worker bool
	// poll is cluster.epoch_poll_interval.
	poll time.Duration
	// storage mutates the storage configuration.
	storage func(*config.Storage)
	// validate wraps the handler in the response validator of the OpenAPI document.
	validate *apitest.Validator
	// hooks are the storage hooks of the replica's item store.
	hooks items.Hooks
	// recorder wraps the replica's audit recorder for the item store.
	recorder func(audit.Recorder) audit.Recorder
	// formats is the seam to the formats of the generic layer.
	formats files.Formats
	// routes adds routes to the replica's router (the format dispatcher), once the
	// item store and the generic layer exist.
	routes func(r *replica, router *httpserver.Router)
}

// replica is one in-process web replica with a listener of its own.
type replica struct {
	t       *testing.T
	e       *env
	name    string
	reg     *prometheus.Registry
	db      *postgres.DB
	changes *cluster.Changes
	signals *cluster.Signals
	engine  *authz.Engine
	reader  *repository.SnapshotReader
	repos   *repository.Service
	policy  *authz.Service
	blobs   *blob.Store
	obj     *countingStore
	items   *items.Store
	files   *files.Service
	stats   *items.Stats
	act     *repository.Activity
	rec     *recorder
	queue   *jobs.Queue
	worker  *jobs.Worker
	server  *httpserver.Server
	baseURL string
	stopped sync.Once
	drain   atomic.Bool
	// spoolDir is the replica's spool directory.
	spoolDir string
}

func apiConfig() config.API {
	return config.API{
		PageSizeDefault: 50, PageSizeMax: 500, IdempotencyRetention: time.Hour,
		MaxRequestBodyBytes: config.MiB, MaxConfigurationBodyBytes: 4 * config.MiB,
	}
}

func (e *env) newReplica(name string, o replicaOpts) *replica {
	t := e.t
	t.Helper()
	if o.poll == 0 {
		o.poll = 200 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	r := &replica{t: t, e: e, name: name, reg: prometheus.NewRegistry(), rec: &recorder{}}
	t.Cleanup(r.stop)
	var err error
	role := "web"
	if o.worker {
		role = "all"
	}
	if r.db, err = postgres.Open(ctx, e.cfg, name, role, quiet); err != nil {
		t.Fatalf("Open(%s): %v", name, err)
	}
	clu := config.Cluster{
		LeaseRetryInterval: 500 * time.Millisecond, EpochPollInterval: o.poll, EpochMaxStaleness: 20 * time.Second,
		ChangeRetention: time.Hour, SignalCoalesceWindow: 20 * time.Millisecond,
	}
	if r.signals, err = cluster.NewSignals(cluster.SignalsOptions{Config: clu, Log: quiet, Metrics: r.reg}); err != nil {
		t.Fatal(err)
	}
	if r.changes, err = cluster.NewChanges(cluster.ChangesOptions{
		DB: r.db.ControlPool(), Config: clu, WatchdogTimeout: 30 * time.Second, Log: quiet, Metrics: r.reg,
	}); err != nil {
		t.Fatal(err)
	}
	caches, err := cache.NewRegistry(r.changes, r.reg)
	if err != nil {
		t.Fatal(err)
	}
	r.changes.AddListener(caches)
	if err := r.changes.Start(ctx); err != nil {
		t.Fatalf("changes.Start: %v", err)
	}

	if r.reader, err = repository.NewReader(repository.ReaderOptions{
		Runner: r.db, Catalog: catalog.Default(), Caches: caches, Poller: r.changes,
		SafetyTTL: 5 * time.Minute, UnknownKeys: 10000, LookupWait: 5 * time.Second, Log: quiet,
	}); err != nil {
		t.Fatal(err)
	}
	r.engine, err = authz.New(authz.Options{
		Repositories: r.reader,
		Source:       authz.NewPostgresSource(r.db, 10*time.Second), Changes: r.changes,
		Config: config.Authz{
			HideExistence: true, Cache: config.AuthzCache{MaxStaleness: 10 * time.Second, SafetyTTL: time.Hour},
			Audit: config.AuthzAudit{Reads: config.AuthzReadsDenied},
		},
		EpochPollInterval: o.poll, RetryBudget: 5 * time.Second,
		AnonymousAccess: func(context.Context) (bool, error) { return e.anonymous.Load(), nil }, Recorder: r.rec, Replica: name, Log: quiet, Metrics: r.reg,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.engine.Start(ctx); err != nil {
		t.Fatalf("engine.Start: %v", err)
	}

	reg := jobs.NewRegistry()
	reg.RegisterKind(repository.DeleteJobKind, repository.DeleteJobSpec(func(ctx context.Context, run *jobs.Run) error {
		return r.repos.RunDeleteJob(ctx, run)
	}))
	reg.RegisterKind(items.DeleteJobKind, items.DeleteJobSpec(func(ctx context.Context, run *jobs.Run) error {
		return r.items.RunDeleteJob(ctx, run)
	}))
	reg.Freeze()
	r.queue = jobs.NewQueue(jobs.QueueOptions{DB: r.db.Main(), Registry: reg, Signals: r.signals, Log: quiet})
	if o.worker {
		r.worker, err = jobs.NewWorker(jobs.WorkerOptions{
			Role: role, Instance: name, UID: uuidV4(), Main: r.db.Main(), Control: r.db.ControlPool(),
			Registry: reg, Signals: r.signals, WatchdogTimeout: 30 * time.Second, Log: quiet, Metrics: r.reg,
			Config: config.Jobs{
				Workers:       config.JobsWorkers{Default: 2, Maintenance: 1, Index: 1, Delivery: 1},
				PollInterval:  200 * time.Millisecond,
				LeaseDuration: 6 * time.Second, HeartbeatInterval: 500 * time.Millisecond,
				ScheduleTick: time.Second, Retention: 720 * time.Hour,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := r.worker.Start(ctx); err != nil {
			t.Fatalf("worker.Start: %v", err)
		}
	}
	if r.repos, err = repository.NewService(repository.Options{
		Runner: r.db, Catalog: catalog.Default(), Recorder: r.rec, Notifier: r.changes, Jobs: r.queue, Log: quiet,
		DeleteBatch: 2,
	}); err != nil {
		t.Fatal(err)
	}
	r.policy = authz.NewService(authz.ServiceOptions{
		DB: r.db, Changes: r.changes, Principals: sqlPrincipals{e}, Repositories: r.reader, Recorder: r.rec,
	})

	cfg := e.s3.cfg
	cfg.Delete.MaxSyncItems, cfg.Delete.BatchSize = 10000, 2
	cfg.Spool.Dir = t.TempDir() // the spool of one replica is its own: a store empties it at start
	r.spoolDir = cfg.Spool.Dir
	if o.storage != nil {
		o.storage(&cfg)
	}
	r.obj = &countingStore{ObjectStore: e.s3.raw}
	if r.blobs, err = blob.New(cfg, r.obj, r.db, blob.WithLogger(quiet), blob.WithWaitPoll(25*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	r.stats = items.NewStats(r.db, 300*time.Millisecond, quiet)
	r.act = repository.NewActivity(r.db, 300*time.Millisecond, quiet)
	r.stats.Start(context.Background())
	r.act.Start(context.Background())
	var itemRec audit.Recorder = r.rec
	if o.recorder != nil {
		itemRec = o.recorder(r.rec)
	}
	if r.items, err = items.New(items.Options{
		Runner: r.db, Blobs: r.blobs, Recorder: itemRec, Jobs: r.queue, Hooks: o.hooks,
		Storage: cfg, RecordDownloads: true, Activity: r.act, Stats: r.stats, Log: quiet,
	}); err != nil {
		t.Fatal(err)
	}
	if r.files, err = files.New(files.Options{Items: r.items, Repos: r.reader, Formats: o.formats, Log: quiet}); err != nil {
		t.Fatal(err)
	}

	r.serve(o)
	return r
}

// caller begins the index of a request for sub, with the keys made known, as the
// entry points do, and returns the caller of the generic layer. Every decision
// of the returned caller is answered on that one index.
func (r *replica) caller(sub authz.Subject, keys ...string) files.Caller {
	r.t.Helper()
	acc, err := edge.Begin(context.Background(), r.engine, sub, keys...)
	if err != nil {
		r.t.Fatalf("begin: %v", err)
	}
	return files.Caller{
		Access: acc, Subject: sub,
		Recheck: func(ctx context.Context, key string) (files.Access, error) {
			fresh, err := edge.Begin(ctx, r.engine, sub, key)
			if err != nil {
				return nil, err
			}
			return fresh, nil
		},
	}
}

// filesOn builds a generic layer over a store of its own on this replica's
// database, with the given blob store and repository reader: a test replaces one
// of them to observe or to change what the transfer sees.
func (r *replica) filesOn(blobs items.Blobs, repos repository.Reader) *files.Service {
	r.t.Helper()
	st, err := items.New(items.Options{Runner: r.db, Blobs: blobs, Recorder: audit.Nop{}, Log: quiet})
	if err != nil {
		r.t.Fatal(err)
	}
	svc, err := files.New(files.Options{Items: st, Repos: repos, Log: quiet})
	if err != nil {
		r.t.Fatal(err)
	}
	return svc
}

// serve builds the router and the chain and starts the listener.
func (r *replica) serve(o replicaOpts) {
	t := r.t
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r.baseURL = "http://" + ln.Addr().String()
	_ = ln.Close()

	doc, err := v1.LoadDocument(v1.EmbeddedYAML())
	if err != nil {
		t.Fatal(err)
	}
	router := httpserver.NewClientRouter()
	inv, err := invalidation.NewService(invalidation.ServiceOptions{Runner: r.db, Notifier: r.changes, Recorder: r.rec})
	if err != nil {
		t.Fatal(err)
	}
	api := v1.New(v1.Options{
		Document: doc, Identity: v1.Identity{Replica: r.name, Role: "web"}, Ready: func() bool { return true },
		Access: v1.NewAccess(r.engine),
		API: v1.Services{
			Config: apiConfig(), Store: r.db, Queue: r.queue, Audit: r.rec,
		},
		Repositories: v1.RepositoryServices{Service: r.repos, Authz: r.engine, Reader: r.reader, AnonymousAccess: func(context.Context) (bool, error) { return r.e.anonymous.Load(), nil }},
		Files:        r.files,
		FilesDecider: r.engine,
		Cache:        v1.CacheServices{Invalidations: inv, Repositories: r.repos, Authz: r.engine},
	})
	api.Register(router)
	rawfiles.New(rawfiles.Options{Service: r.files, Decider: r.engine, Download: r.e.s3.cfg.Download, API: apiConfig()}).Register(router)
	if o.routes != nil {
		o.routes(r, router)
	}
	if err := doc.CheckRoutes(router.Patterns()); err != nil {
		t.Fatalf("route table: %v", err)
	}
	resolver, err := httpserver.NewResolver(nil, r.baseURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	chain := httpserver.Chain{
		Router: router, Resolver: resolver, Authenticate: r.e.authenticate, Replica: r.name,
		ReplicaHeader: httpserver.ReplicaAlways, Draining: r.drain.Load,
	}
	h := chain.Handler()
	if o.validate != nil {
		h = o.validate.Handler(t, h)
	}
	r.server, err = httpserver.NewServer(httpserver.ServerConfig{
		Name: "client", Addr: strings.TrimPrefix(r.baseURL, "http://"), Handler: h, Log: quiet,
		ReadHeaderTimeout: 10 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.server.Bind(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = r.server.Serve() }()
}

func (r *replica) stop() {
	r.stopped.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if r.server != nil {
			_ = r.server.Close()
		}
		if r.stats != nil {
			r.stats.Stop()
			_ = r.stats.Flush(ctx)
		}
		if r.act != nil {
			r.act.Stop()
			_ = r.act.Flush(ctx)
		}
		if r.worker != nil {
			r.worker.Drain(ctx)
			r.worker.Quiesce(ctx)
			_ = r.worker.Stop(ctx)
		}
		if r.engine != nil {
			_ = r.engine.Stop(ctx)
		}
		if r.changes != nil {
			_ = r.changes.Stop(ctx)
		}
		if r.signals != nil {
			r.signals.Close()
		}
		if r.db != nil {
			r.db.Close()
		}
	})
}

// A response is a request's answer, read.
type response struct {
	status int
	header http.Header
	body   []byte
}

func (r response) text() string { return string(r.body) }

func (r response) json(t testing.TB) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.body, &m); err != nil {
		t.Fatalf("answer %d is not a JSON object: %v\n%s", r.status, err, r.body)
	}
	return m
}

var client = &http.Client{
	Timeout:       10 * time.Minute,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	Transport:     &http.Transport{DisableCompression: true, MaxIdleConnsPerHost: 8},
}

// do sends a request to the replica as the named user ("" is anonymous).
func (r *replica) do(user, method, path string, body io.Reader, hdr ...string) response {
	r.t.Helper()
	res, err := r.raw(user, method, path, body, hdr...)
	if err != nil {
		r.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		r.t.Fatalf("%s %s: reading the body: %v", method, path, err)
	}
	return response{status: res.StatusCode, header: res.Header, body: b}
}

// raw sends a request and returns the unread answer.
func (r *replica) raw(user, method, path string, body io.Reader, hdr ...string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(context.Background(), method, r.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	if user != "" {
		req.Header.Set("Authorization", "Bearer "+user)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	return client.Do(req)
}

func (r *replica) put(user, path, content string, hdr ...string) response {
	r.t.Helper()
	return r.do(user, "PUT", path, strings.NewReader(content), hdr...)
}

func (r *replica) get(user, path string, hdr ...string) response {
	r.t.Helper()
	return r.do(user, "GET", path, nil, hdr...)
}

func (r *replica) postJSON(user, path string, body any, hdr ...string) response {
	r.t.Helper()
	b, _ := json.Marshal(body)
	return r.do(user, "POST", path, bytes.NewReader(b), append([]string{"Content-Type", "application/json"}, hdr...)...)
}

// createRepository creates a repository through the management API as admin.
func (r *replica) createRepository(key string, extra map[string]any) {
	r.t.Helper()
	body := map[string]any{"key": key, "class": "local", "format": "generic"}
	for k, v := range extra {
		body[k] = v
	}
	if res := r.postJSON("admin", "/api/v1/repositories", body); res.status != 201 {
		r.t.Fatalf("create %s: %d %s", key, res.status, res.body)
	}
}

// grant creates a policy that gives the entries their actions on a repository, with
// optional path patterns.
func (e *env) grant(r *replica, name string, repos []string, paths authz.PatternLists, entries map[string][]string) {
	e.t.Helper()
	in := authz.PolicyInput{Name: name, Resources: authz.Resources{Repositories: repos, Paths: paths}}
	for who, actions := range entries {
		in.Entries = append(in.Entries, authz.Entry{Principal: who, Actions: actions})
	}
	if _, _, err := r.policy.Create(context.Background(), e.actor(), in); err != nil {
		e.t.Fatalf("policy %s: %v", name, err)
	}
}

// actor is the subject that writes policies and role bindings: a user row, because
// the writer's id is kept as a reference to the principal table.
func (e *env) actor() authz.Subject {
	e.peopleMu.Lock()
	p, ok := e.people["seed-admin"]
	e.peopleMu.Unlock()
	if !ok {
		p = e.user("seed-admin")
	}
	return authz.Subject{Kind: principal.KindUser, ID: p.ID, Name: p.Name}
}

// waitFor polls cond until it holds or the time is up.
func waitFor(t testing.TB, what string, within time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s did not happen within %s", what, within)
}
