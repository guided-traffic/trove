//go:build integration

// Package bastioniface tests the main installation's side of the trove Bastion against the integration stack
// (R-BAS-008 to R-BAS-017): the Recognize stage and the four operations of the interface behind the real chain,
// the real authentication stage, token service, decision point and audit writer, on a database of their own.
// What is a stand-in: the TokenAuthenticator of work package 1 and the ShapeResolver of work package 3 (the
// interface seams), and the Bastion itself, which the tests play over HTTP. Every test creates a database of its
// own, so parallel tests and agents never collide.
package bastioniface

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastionmain"
	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/test/integration/idapi"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

const schemaName = "trove"

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func writeFile(t testing.TB, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// env is one test's migrated database and Valkey keyspace.
type env struct {
	t     *testing.T
	db    string
	cfg   config.Database
	admin *pgx.Conn
	ks    string
	vcfg  config.Valkey
}

func connect(t testing.TB, database string) *pgx.Conn {
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

func newEnv(t *testing.T) *env {
	t.Helper()
	port, err := strconv.Atoi(testenv.Get(t, "TROVE_TEST_PG_PORT"))
	if err != nil {
		t.Fatalf("TROVE_TEST_PG_PORT: %v", err)
	}
	host, user, secret := testenv.Get(t, "TROVE_TEST_PG_HOST"), testenv.Get(t, "TROVE_TEST_PG_USER"), testenv.Get(t, "TROVE_TEST_PG_PASSWORD")
	name := "trove_bif_" + randHex(5)
	maint := connect(t, "postgres")
	if _, err := maint.Exec(context.Background(), "CREATE DATABASE "+name+" ENCODING 'UTF8' TEMPLATE template0"); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() { _, _ = maint.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)") })
	cfg := config.Database{
		Host: host, Port: port, Name: name, Schema: schemaName,
		UsernameFile: writeFile(t, "username", user+"\n"), PasswordFile: writeFile(t, "password", secret+"\n"),
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
	admin := connect(t, name)
	if _, err := admin.Exec(context.Background(), "SET search_path = "+schemaName); err != nil {
		t.Fatal(err)
	}
	return &env{t: t, db: name, cfg: cfg, admin: admin, ks: randHex(8), vcfg: valkeyConfig(t)}
}

var keyPrefix = "trove:itb" + randHex(3) + ":"

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

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.admin.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

func (e *env) text(sql string, args ...any) string {
	e.t.Helper()
	var v string
	if err := e.admin.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return v
}

func (e *env) number(sql string, args ...any) int64 {
	e.t.Helper()
	var v int64
	if err := e.admin.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return v
}

// registration creates the principal and the subtype row of a Bastion registration, which bastion_contact
// refers to (the registration service is work package 1's).
func (e *env) registration(name string) (id string) {
	e.t.Helper()
	id = e.text("INSERT INTO principal (id, kind, name) VALUES (gen_random_uuid(), 'bastion', $1) RETURNING id::text", name)
	e.exec("INSERT INTO bastion (principal_id) VALUES ($1::uuid)", id)
	return id
}

// repository creates a repository of a format from its qualified name "<project>/<key>", and the project when
// it does not exist; virtual repositories list their members by qualified name.
func (e *env) repository(name, class string, members ...string) {
	e.t.Helper()
	project, key, _ := strings.Cut(name, "/")
	e.exec("INSERT INTO project (id, key, display_name) VALUES (gen_random_uuid(), $1, $1) ON CONFLICT (key) DO NOTHING", project)
	e.exec("INSERT INTO repositories (key, format, class, project_id) SELECT $1, 'npm', $2, id FROM project WHERE key = $3", key, class, project)
	for i, m := range members {
		e.exec(`INSERT INTO virtual_members (virtual_id, position, member_id)
			SELECT v.id, $3, m.id FROM repositories v JOIN project vp ON vp.id = v.project_id,
			       repositories m JOIN project mp ON mp.id = m.project_id
			 WHERE vp.key || '/' || v.key = $1 AND mp.key || '/' || m.key = $2`, name, m, i)
	}
}

// journaled inserts an audit event as the superuser, at the time given as an offset from the database clock.
// It returns the event's ID. The events of the real services are written by them; this is for the actions whose
// producers are not part of this suite (npm.unpublish) and for events at a chosen instant.
func (e *env) journaled(action string, ago time.Duration, resourceID, repository, details string) string {
	e.t.Helper()
	return e.text(`INSERT INTO audit_event (id, occurred_at, action, category, outcome, actor_type, resource_id, repository, details)
		VALUES (gen_random_uuid(), now() - make_interval(secs => $1), $2, 'content', 'success', 'user', NULLIF($3, ''), NULLIF($4, ''), $5::jsonb)
		RETURNING id::text`, ago.Seconds(), action, resourceID, repository, details)
}

// capture is an audit recorder that keeps every event it is given, in memory, and writes it to the table too.
type capture struct {
	mu     sync.Mutex
	events []audit.Event
	writer *audit.Writer
}

func (c *capture) RecordTx(ctx context.Context, tx meta.Tx, ev audit.Event) error {
	c.mu.Lock()
	c.events = append(c.events, ev)
	c.mu.Unlock()
	return c.writer.RecordTx(ctx, tx, ev)
}

func (c *capture) Record(ctx context.Context, ev audit.Event) {
	c.mu.Lock()
	c.events = append(c.events, ev)
	c.mu.Unlock()
	c.writer.Record(ctx, ev)
}

func (c *capture) named(name string) []audit.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []audit.Event
	for _, e := range c.events {
		if e.Type == name {
			out = append(out, e)
		}
	}
	return out
}

// fakeTokens is the TokenAuthenticator of work package 1: the registrations the test names.
type fakeTokens struct {
	mu     sync.Mutex
	tokens map[string]bastionmain.BastionToken
	calls  int
}

func (f *fakeTokens) asked() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }

func (f *fakeTokens) Authenticate(ctx context.Context, raw string, req bastionmain.AuthRequest) (bastionmain.BastionToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, held := f.tokens[raw]; !held && req.Throttle != nil && req.Throttle.Exceeded(ctx) {
		return bastionmain.BastionToken{}, bastionmain.ErrThrottled // the token service's order: the cache, then the throttle
	}
	f.calls++ // a lookup
	if t, ok := f.tokens[raw]; ok {
		return t, nil
	}
	return bastionmain.BastionToken{}, bastionmain.ErrTokenInvalid
}

func (f *fakeTokens) add(raw string, t bastionmain.BastionToken) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.tokens == nil {
		f.tokens = map[string]bastionmain.BastionToken{}
	}
	f.tokens[raw] = t
}

// fakeShapes is the ShapeResolver of work package 3: the shapes the test names per subject (the principal ID,
// "" for an anonymous caller); a key it does not name is none with the status of a missing repository.
type fakeShapes struct {
	mu     sync.Mutex
	shapes map[string]map[string]bastionmain.Shape
	asked  []authz.Subject
}

func (f *fakeShapes) set(subject, key string, sh bastionmain.Shape) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.shapes == nil {
		f.shapes = map[string]map[string]bastionmain.Shape{}
	}
	if f.shapes[subject] == nil {
		f.shapes[subject] = map[string]bastionmain.Shape{}
	}
	f.shapes[subject][key] = sh
}

func (f *fakeShapes) Shapes(_ context.Context, idx *authz.Index, subject authz.Subject, keys []qname.Repo) (map[qname.Repo]bastionmain.Shape, error) {
	if idx == nil {
		return nil, errors.New("the check asked for shapes without the request's index")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, subject)
	id := subject.ID
	if subject.Anonymous {
		id = ""
	}
	out := map[qname.Repo]bastionmain.Shape{}
	for _, k := range keys {
		if sh, ok := f.shapes[id][k.String()]; ok {
			out[k] = sh
		} else {
			out[k] = bastionmain.Shape{Read: iface.ReadNone, Status: http.StatusNotFound}
		}
	}
	return out, nil
}

// siteOpts describe the main installation of a test.
type siteOpts struct {
	// trusted are server.trusted_proxies (none by default), egress bastions.trusted_proxies (the loopback
	// addresses the tests connect from, by default) and additional server.additional_base_urls.
	trusted    []string
	egress     []string
	additional []string
	forwarded  httpserver.ForwardedHeader
	// feed overrides the feed's bounds; zero values keep the defaults of the configuration.
	lookback time.Duration
	maxPage  int
	// maxBatch and maxAge override bastions.downloads; recordDownloads is audit.record_downloads.
	maxBatch        int
	maxAge          time.Duration
	recordDownloads bool
	// endpointAnonymous is the default endpoint's own anonymous rule.
	endpointAnonymous bool
	// failures is the budget of rejected credentials per minute and address (auth.rate_limit).
	failures int
}

// site is one replica of the main installation with the Bastion interface: pools, change log, caches, the
// identity components, the decision point, the audit writer and an HTTP listener with the real chain.
type site struct {
	t   *testing.T
	e   *env
	db  *postgres.DB
	srv *httptest.Server

	vk      *cluster.Valkey
	changes *cluster.Changes
	manager *principal.Manager
	tokens  *token.Service
	setts   *token.SettingsStore
	stage   *auth.Stage
	engine  *authz.Engine
	writer  *audit.Writer
	rec     *capture
	metrics *bastionmain.Metrics
	feed    *bastionmain.Feed

	bastion *fakeTokens
	shapes  *fakeShapes
	rules   *rulesBox
	reg     *prometheus.Registry
}

// rulesBox holds the endpoint rules the stage runs with, changeable by a test.
type rulesBox struct {
	mu    sync.Mutex
	rules auth.EndpointRules
}

func (b *rulesBox) get() auth.EndpointRules { b.mu.Lock(); defer b.mu.Unlock(); return b.rules }
func (b *rulesBox) anonymous(on bool)       { b.mu.Lock(); defer b.mu.Unlock(); b.rules.Anonymous = on }

const baseURL = "https://trove.example.com"

func (e *env) newSite(o siteOpts) *site {
	t := e.t
	t.Helper()
	name := "main-" + randHex(2)
	s := &site{t: t, e: e, bastion: &fakeTokens{}, shapes: &fakeShapes{}, reg: prometheus.NewRegistry()}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var err error
	if s.db, err = postgres.Open(ctx, e.cfg, name, "web", quiet); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(s.db.Close)

	store := audit.NewStore(s.db)
	if err := store.Prepare(ctx, 365, quiet); err != nil {
		t.Fatalf("audit partitions: %v", err)
	}
	if s.writer, err = audit.New(audit.Options{DB: s.db, Replica: name, Config: config.Audit{AsyncBuffer: 1000, RecordDownloads: o.recordDownloads}, Log: quiet, AppLog: quiet}); err != nil {
		t.Fatal(err)
	}
	s.writer.Start()
	t.Cleanup(func() {
		fctx, fcancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer fcancel()
		_ = s.writer.Flush(fctx)
	})
	s.rec = &capture{writer: s.writer}

	if s.vk, err = cluster.NewValkey(ctx, e.vcfg, name, "web", quiet, prometheus.NewRegistry(), cluster.WithKeyspaceID(e.ks)); err != nil {
		t.Fatalf("NewValkey: %v", err)
	}
	t.Cleanup(s.vk.Close)
	s.changes, err = cluster.NewChanges(cluster.ChangesOptions{
		DB: s.db.ControlPool(), Bus: s.vk, WatchdogTimeout: 30 * time.Second, Log: quiet, Metrics: prometheus.NewRegistry(),
		Config: config.Cluster{LeaseRetryInterval: 500 * time.Millisecond, EpochPollInterval: time.Second, EpochMaxStaleness: 20 * time.Second, ChangeRetention: time.Hour, SignalCoalesceWindow: 20 * time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	caches, err := cache.NewRegistry(s.changes, prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	s.changes.AddListener(caches)
	if err := s.changes.Start(ctx); err != nil {
		t.Fatalf("changes.Start: %v", err)
	}
	t.Cleanup(func() {
		sctx, scancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer scancel()
		_ = s.changes.Stop(sctx)
	})

	tokenCfg := config.AuthTokens{
		CacheTTL: 15 * time.Second, RetentionAfterEnd: 2160 * time.Hour, ExpiryNotice: 168 * time.Hour,
		PAT: config.AuthTokenPAT{DefaultTTL: 2160 * time.Hour, MaxTTL: 8760 * time.Hour}, SAT: config.AuthTokenSAT{DefaultTTL: 2160 * time.Hour, MaxTTL: 8760 * time.Hour},
		CIT: config.AuthTokenCIT{MaxTTL: 6 * time.Hour}, MaxActive: config.AuthTokenMax{PAT: 50, SAT: 20},
	}
	pm, err := principal.NewMetrics(s.reg)
	if err != nil {
		t.Fatal(err)
	}
	gate := principal.Gate{Fresh: s.changes, Bound: 2 * time.Second, Metrics: pm}
	principals := principal.NewStore(principal.StoreOptions{DB: s.db, Cache: caches, Gate: gate, SafetyTTL: tokenCfg.CacheTTL})
	s.setts = token.NewSettingsStore(token.SettingsOptions{
		DB: s.db, Notifier: s.changes, Recorder: s.rec, Cache: caches, Gate: gate, Tokens: tokenCfg, Anonymous: config.AuthAnonymous{Allowed: true},
	})
	s.tokens = token.NewService(token.ServiceOptions{
		DB: s.db, Notifier: s.changes, Recorder: s.rec, Principals: principals, Settings: s.setts, Cache: caches, Gate: gate,
		Tokens: tokenCfg, Metrics: pm, Log: quiet,
		Scope: scope.Options{
			Action: func(string) bool { return true }, SystemItem: func(s string) bool { return s == "identities:read" },
			Endpoint: func(n string) bool { return n == "default" }, EndpointAccepts: func(string, string) bool { return true },
		},
	})
	s.manager = principal.NewManager(principal.ManagerOptions{DB: s.db, Notifier: s.changes, Recorder: s.rec, BreakGlassName: "breakglass"})
	budget := o.failures
	if budget == 0 {
		budget = 300
	}
	limiter := auth.NewLocalLimiter(budget, time.Minute, nil, nil)
	s.stage = auth.New(auth.Options{
		Tokens: s.tokens, Uses: s.tokens, Principals: principals, Settings: s.setts, Limiter: limiter,
		BasicEnabled: true, Metrics: pm, Log: quiet,
		ClientAddr: func(r *http.Request) netip.Addr { return httpserver.InfoFrom(r.Context()).Client },
	})
	s.engine, err = authz.New(authz.Options{
		Source: authz.NewPostgresSource(s.db, 10*time.Second), Changes: s.changes,
		Config:            config.Authz{HideExistence: true, Cache: config.AuthzCache{MaxStaleness: 10 * time.Second, SafetyTTL: time.Hour}},
		EpochPollInterval: time.Second, RetryBudget: 5 * time.Second, Recorder: s.rec, Replica: name, Log: quiet,
		AnonymousAccess: func(ctx context.Context) (bool, error) {
			eff, err := s.setts.Effective(ctx)
			return eff.AnonymousAccess, err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.engine.Start(ctx); err != nil {
		t.Fatalf("engine.Start: %v", err)
	}
	t.Cleanup(func() {
		sctx, scancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer scancel()
		_ = s.engine.Stop(sctx)
	})

	s.rules = &rulesBox{rules: auth.EndpointRules{
		Name: "default", Anonymous: o.endpointAnonymous, TokenTypes: []token.Type{token.TypePAT, token.TypeSAT, token.TypeCIT},
		Refused: func(r *http.Request, f auth.Refusal) {
			d, _ := json.Marshal(map[string]string{"reason": "endpoint_not_accepted", "refusal": f.Reason, "tokenType": string(f.TokenType)})
			s.rec.Record(r.Context(), audit.Event{
				Type: audit.EventTokenRejected, Outcome: audit.Failure, Reason: "endpoint_not_accepted", Actor: audit.Actor{Kind: "anonymous"},
				Endpoint: "default", Source: httpserver.SourceFrom(r.Context()), Subject: audit.Subject{Kind: "token", ID: f.TokenID}, Details: d,
			})
		},
	}}

	s.metrics, err = bastionmain.NewMetrics(s.reg)
	if err != nil {
		t.Fatal(err)
	}
	if o.lookback == 0 {
		o.lookback = 5 * time.Minute
	}
	if o.maxPage == 0 {
		o.maxPage = 1000
	}
	if o.maxBatch == 0 {
		o.maxBatch = 1000
	}
	if o.maxAge == 0 {
		o.maxAge = 720 * time.Hour
	}
	s.feed = bastionmain.NewFeed(bastionmain.FeedOptions{DB: s.db, Lookback: o.lookback, MaxPage: o.maxPage, Endpoint: "default", Log: quiet})

	doc, err := v1.LoadDocument(v1.EmbeddedYAML())
	if err != nil {
		t.Fatal(err)
	}
	credential := func(r *http.Request) (httpserver.Principal, error) {
		res, err := s.stage.Authenticate(r, s.rules.get())
		if err != nil {
			return httpserver.Principal{}, err
		}
		return principalOf(res), nil
	}
	bs := v1.BastionServices{
		Config: config.Bastions{
			Check:     config.BastionsCheck{MaxRepositories: 4},
			Feed:      config.BastionsFeed{Lookback: o.lookback, MaxPage: o.maxPage},
			Downloads: config.BastionsDownloads{MaxBatch: o.maxBatch, MaxAge: o.maxAge},
		},
		Release: "0.14.0", BaseURL: baseURL,
		Anonymous: func(ctx context.Context) (bool, error) {
			if !s.rules.get().Anonymous {
				return false, nil
			}
			eff, err := s.setts.Effective(ctx)
			return eff.AnonymousAccess, err
		},
		Authenticate: credential,
		Shapes:       s.shapes,
		Feed:         s.feed,
		Contacts:     bastionmain.NewContacts(bastionmain.ContactOptions{DB: s.db, Audit: s.rec}),
		Downloads:    bastionmain.NewIntake(bastionmain.IntakeOptions{DB: s.db, Audit: s.rec, MaxBatch: o.maxBatch, MaxAge: o.maxAge, Record: o.recordDownloads}),
	}
	api := v1.New(v1.Options{
		Document: doc, Identity: v1.Identity{Replica: name, Role: "web"}, Ready: func() bool { return true },
		Access:  v1.NewAccess(s.engine),
		API:     v1.Services{Config: idapi.APIConfig(), Store: s.db, Audit: s.rec},
		Auth:    v1.AuthServices{Manager: s.manager, Tokens: s.tokens, Settings: s.setts, Config: idapi.AuthConfig(tokenCfg)},
		Bastion: bs,
	})
	router := httpserver.NewClientRouter()
	api.Register(router)
	router.Handle("/api/npm/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A stand-in for a package route: it answers with what the chain made of the request.
		p := httpserver.PrincipalFrom(r.Context())
		_ = json.NewEncoder(w).Encode(map[string]any{
			"client": httpserver.InfoFrom(r.Context()).Client.String(), "baseURL": httpserver.InfoFrom(r.Context()).BaseURL,
			"principal": p.Name, "anonymous": p.Anonymous, "authorization": r.Header.Get("Authorization"),
			"bastionAuthorization": r.Header.Get(iface.HeaderAuthorization),
		})
	}))
	egress := o.egress
	if len(egress) == 0 {
		egress = []string{"127.0.0.1/32", "::1/128"}
	}
	resolver, err := httpserver.NewResolver(o.trusted, baseURL, o.additional, httpserver.WithForwardedHeader(o.forwarded))
	if err != nil {
		t.Fatal(err)
	}
	// The re-derivation of the composition root (app.bastionForwarded, Q-BAS-116): a recognized request from a
	// listed Bastion egress is resolved again with that egress trusted.
	bastionResolver, err := httpserver.NewResolver(append(append([]string(nil), o.trusted...), egress...), baseURL, o.additional, httpserver.WithForwardedHeader(o.forwarded))
	if err != nil {
		t.Fatal(err)
	}
	egressPrefixes := (&config.Bastions{TrustedProxies: egress}).TrustedProxyPrefixes()
	forwarded := func(r *http.Request) context.Context {
		client := httpserver.InfoFrom(r.Context()).Client
		for _, p := range egressPrefixes {
			if client.IsValid() && p.Contains(client.Unmap()) {
				return httpserver.WithResolved(r.Context(), bastionResolver.Resolve(r))
			}
		}
		return r.Context()
	}
	problem := func(w http.ResponseWriter, r *http.Request, err error) {
		errs.Write(w, r, err, httpserver.RequestID(r.Context()), httpserver.Logger(r.Context()))
	}
	chain := httpserver.Chain{
		Log: quiet, Router: router, Resolver: resolver, Replica: name, ReplicaHeader: httpserver.ReplicaAlways,
		Authenticate: v1.BastionRouteAuth(credential, nil),
		Bastion: bastionmain.Stages{Recognize: bastionmain.Recognize(bastionmain.RecognizeOptions{
			Tokens: s.bastion, Metrics: s.metrics, Problem: problem, Forwarded: forwarded,
			Throttle: func(r *http.Request) bastionmain.Throttle {
				return auth.NewAddressThrottle(limiter, httpserver.InfoFrom(r.Context()).Client, nil, bastionmain.ThrottleScope)
			},
			Source: func(r *http.Request) (string, string) {
				src := httpserver.SourceFrom(r.Context())
				return src.ClientAddr, src.UserAgent
			},
		})}.Chain(),
	}.Handler()
	s.srv = httptest.NewServer(chain)
	t.Cleanup(s.srv.Close)
	return s
}

func principalOf(res auth.Result) httpserver.Principal {
	if res.Anonymous {
		return httpserver.AnonymousPrincipal()
	}
	p := httpserver.Principal{
		Kind: httpserver.PrincipalKind(res.Principal.Kind), ID: res.Principal.ID, Name: res.Principal.Name,
		Credential: httpserver.Credential{
			Method: httpserver.AuthMethod(res.Credential.Method), TokenID: res.Credential.TokenID,
			TokenType: httpserver.TokenType(res.Credential.TokenType), ExpiresAt: res.Credential.ExpiresAt, Scope: res.Credential.Scope,
		},
	}
	for _, g := range res.Groups {
		p.Groups = append(p.Groups, httpserver.GroupRef{ID: g.ID, Name: g.Name})
	}
	return p
}

// registered adds a Bastion token for a registration to the stand-in authenticator and returns the token.
func (s *site) registered(name, bastionID string) string {
	raw, err := token.Generate(token.TypeBST)
	if err != nil {
		s.t.Fatal(err)
	}
	s.bastion.add(raw, bastionmain.BastionToken{
		TokenID: "0192f1c4-0000-7000-8000-" + randHex(6), Hint: raw[8:12], BastionID: bastionID, Name: name,
		ExpiresAt: time.Now().Add(180 * 24 * time.Hour).UTC().Truncate(time.Second),
	})
	return raw
}

// call is one request to the interface or a package route.
type call struct {
	method, path string
	header       http.Header
	body         any
}

type reply struct {
	Status int
	Header http.Header
	Body   []byte
}

func (rp reply) decode(t testing.TB, v any) {
	t.Helper()
	if err := json.Unmarshal(rp.Body, v); err != nil {
		t.Fatalf("body is not JSON (%d): %s", rp.Status, rp.Body)
	}
}

func (rp reply) code() string {
	var m map[string]any
	_ = json.Unmarshal(rp.Body, &m)
	c, _ := m["code"].(string)
	return c
}

// do sends the request with the headers of a Bastion (the version, the replica) unless the call sets them.
func (s *site) do(c call) reply {
	s.t.Helper()
	var body io.Reader
	switch b := c.body.(type) {
	case nil:
	case string:
		body = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			s.t.Fatal(err)
		}
		body = strings.NewReader(string(raw))
	}
	req, err := http.NewRequest(c.method, s.srv.URL+c.path, body)
	if err != nil {
		s.t.Fatal(err)
	}
	if c.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, vs := range c.header {
		req.Header[http.CanonicalHeaderKey(k)] = vs
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatalf("%s %s: %v", c.method, c.path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return reply{Status: resp.StatusCode, Header: resp.Header, Body: raw}
}

// iface sends an interface request with the Bastion token as the bearer.
func (s *site) iface(bastionToken, method, path string, body any, extra ...string) reply {
	s.t.Helper()
	h := http.Header{
		"Authorization": {"Bearer " + bastionToken}, iface.HeaderInterface: {strconv.Itoa(iface.InterfaceVersion)},
		iface.HeaderReplica: {"trove-bastion-7d9c-x2k4q"}, "User-Agent": {"trove-bastion/0.14.0"},
	}
	for i := 0; i+1 < len(extra); i += 2 {
		if extra[i+1] == "-" {
			delete(h, http.CanonicalHeaderKey(extra[i]))
		} else {
			h.Set(extra[i], extra[i+1])
		}
	}
	return s.do(call{method: method, path: path, header: h, body: body})
}

func waitFor(t testing.TB, what string, within time.Duration, cond func() bool) {
	t.Helper()
	start := time.Now()
	for time.Since(start) < within {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s did not happen within %s", what, within)
}

var admin = principal.Caller{Actor: audit.Actor{Kind: "admin-cli", Name: "test"}}

func (s *site) serviceAccount(name string) principal.ServiceAccount {
	s.t.Helper()
	sa, err := s.manager.CreateServiceAccount(context.Background(), admin, principal.NewServiceAccount{Name: name})
	if err != nil {
		s.t.Fatalf("CreateServiceAccount(%s): %v", name, err)
	}
	return sa
}

func (s *site) sat(sa principal.ServiceAccount, mutate ...func(*token.CreateRequest)) token.Created {
	s.t.Helper()
	req := token.CreateRequest{Caller: admin, Type: token.TypeSAT, OwnerID: sa.ID, Description: "test", Via: "admin-cli", Admin: true}
	for _, m := range mutate {
		m(&req)
	}
	c, err := s.tokens.Create(context.Background(), req)
	if err != nil {
		s.t.Fatalf("Create SAT: %v", err)
	}
	return c
}
