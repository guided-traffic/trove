//go:build integration

// Package login tests the login area (WP1.1) against the integration stack:
// browser logins through an in-process OpenID Provider over TLS, sessions,
// CSRF, logout, re-authentication and the break-glass account, each with
// several in-process web replicas that have pools, caches and a change log of
// their own and no Valkey (the "Valkey stopped" case of R-AUTH-042). A
// dispatcher in front of them sends every request to the next replica, like
// the scatter profile. Every test creates a database of its own.
package login

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
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

	"github.com/guided-traffic/trove/internal/api/v1/apitest"
	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/oidc"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/session"
	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/outbound"
	"github.com/guided-traffic/trove/internal/tlsfiles/tlstest"
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

// env is one test's database, CA, provider and edge.
type env struct {
	t     *testing.T
	cfg   config.Database
	admin *pgx.Conn
	ca    *tlstest.CA
	caPEM string
	idp   *fakeIDP
	prov  *fakeProvisioner
	audit *spy

	// realAPI mounts the management API (internal/api/v1) behind the real
	// authentication stage on /api/v1/ instead of the two stand-in routes; set it
	// before start (api_test.go).
	realAPI    bool
	validator  *apitest.Validator
	components []apiParts
	// extend, when set, adds the services of further areas to the API handler of
	// every replica (the audit store, the policy service, the raw file path;
	// e1_5_test.go).
	extend func(c *idapi.Components, p replicaParts)

	secretFile string
	edge       *httptest.Server
	baseURL    string
	replicas   []*replica
	next       atomic.Uint64

	mu     sync.Mutex
	served []string // replica of each request, in order
}

// newEnv creates a database trove_login_<random>, migrates it, starts the fake
// provider and the edge, and drops the database when the test ends.
func newEnv(t *testing.T) *env {
	t.Helper()
	port, err := strconv.Atoi(testenv.Get(t, "TROVE_TEST_PG_PORT"))
	if err != nil {
		t.Fatalf("TROVE_TEST_PG_PORT: %v", err)
	}
	host, user, pass := testenv.Get(t, "TROVE_TEST_PG_HOST"), testenv.Get(t, "TROVE_TEST_PG_USER"), testenv.Get(t, "TROVE_TEST_PG_PASSWORD")
	name := "trove_login_" + randHex(5)
	maint := connect(t, "postgres")
	if _, err := maint.Exec(context.Background(), "CREATE DATABASE "+name+" ENCODING 'UTF8' TEMPLATE template0"); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() { _, _ = maint.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)") })
	dir := t.TempDir()
	userFile, passFile := filepath.Join(dir, "username"), filepath.Join(dir, "password")
	writeFile(t, userFile, user+"\n")
	writeFile(t, passFile, pass+"\n")
	cfg := config.Database{
		Host: host, Port: port, Name: name, Schema: schemaName, UsernameFile: userFile, PasswordFile: passFile,
		SSLMode: "verify-full", SSLRootCertFile: testenv.CAFile(t), ConnectTimeout: 5 * time.Second, TargetSessionAttrs: "read-write",
		Pooler: config.DatabasePooler{Mode: "none"},
		Pool:   config.DatabasePool{MaxConns: 8, MinConns: 1, MaxConnLifetime: 30 * time.Minute, MaxConnLifetimeJitter: time.Minute, MaxConnIdleTime: 5 * time.Minute, HealthCheckPeriod: 30 * time.Second},
		Session: config.DatabaseSession{
			StatementTimeout: 30 * time.Second, LockTimeout: 5 * time.Second, IdleInTransactionSessionTimeout: 60 * time.Second,
		},
		Retry:           config.DatabaseRetry{InitialBackoff: 50 * time.Millisecond, MaxBackoff: 200 * time.Millisecond, MaxElapsed: 2 * time.Second},
		Keepalive:       config.DatabaseKeepalive{Idle: 15 * time.Second, Interval: 5 * time.Second, Count: 3, UserTimeout: 30 * time.Second},
		ServerKeepalive: config.DatabaseServerKeepalive{Idle: 60 * time.Second, Interval: 10 * time.Second, Count: 6},
		Preflight:       config.DatabasePreflight{ConnectionBudget: "enforce", ReservedConnections: 10, AllowSuperuser: true},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	retention := 365 // the audit partitions the real audit writer (e1_5_test.go) inserts into
	if _, err := postgres.Migrate(ctx, cfg, postgres.MigrateOptions{
		Instance: "it-migrate", Version: "0.1.0", Log: quiet, AuditRetentionDays: &retention,
		Migrate: config.Migrate{AdvisoryLockWait: 20 * time.Second, DDLLockTimeout: 2 * time.Second, DDLLockRetries: 3},
	}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if testenv.PgPooled(t) {
		cfg = testenv.ThroughPgBouncer(t, cfg, user, maint)
	}
	e := &env{t: t, cfg: cfg, admin: connect(t, name), ca: tlstest.NewCA(t, "login-it"), audit: &spy{}}
	e.caPEM = e.ca.WriteCA(t, dir, "ca.crt")
	e.secretFile = filepath.Join(dir, "client-secret")
	writeFile(t, e.secretFile, "s3cret-one\n")
	e.idp = newFakeIDP(t, e.ca, "s3cret-one")
	e.edge = httptest.NewUnstartedServer(http.HandlerFunc(e.dispatch))
	e.edge.TLS = &tls.Config{Certificates: []tls.Certificate{e.ca.Issue(t, tlstest.Options{}).TLS}, MinVersion: tls.VersionTLS12}
	e.edge.StartTLS()
	t.Cleanup(e.edge.Close)
	e.baseURL = e.edge.URL
	return e
}

func connect(t *testing.T, database string) *pgx.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := pgx.Connect(ctx, testenv.PgDSN(t, database))
	if err != nil {
		t.Fatalf("connect to %s: %v", database, err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return c
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// dispatch is the edge: every request goes to the next replica (R-ARCH-032).
func (e *env) dispatch(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	reps := e.replicas
	e.mu.Unlock()
	rep := reps[int(e.next.Add(1)-1)%len(reps)]
	e.mu.Lock()
	e.served = append(e.served, rep.name)
	e.mu.Unlock()
	w.Header().Set("X-Trove-Replica", rep.name)
	rep.mux.ServeHTTP(w, r)
}

// replica is one web replica with pools, a change log and caches of its own.
type replica struct {
	name     string
	db       *postgres.DB
	changes  *cluster.Changes
	sessions *session.Store
	login    *oidc.Service
	mux      *http.ServeMux
}

// defaultAuth is the auth section of the tests: one provider "corp" against
// the fake provider, the break-glass account off.
func (e *env) defaultAuth() config.Auth {
	return config.Auth{
		OIDC:      config.AuthOIDC{Providers: map[string]config.AuthOIDCProvider{"corp": e.provider(nil)}},
		LoginPage: config.AuthLoginPage{AutoRedirect: true},
		Sessions:  config.AuthSessions{IdleTimeout: time.Hour, AbsoluteTimeout: 12 * time.Hour, MaxPerUser: 20, RecentAuthWindow: time.Hour},
		BreakGlass: config.AuthBreakGlass{
			Enabled: false, Username: "admin", IdleTimeout: 15 * time.Minute, AbsoluteTimeout: time.Hour,
			RotationHold: 2 * time.Minute, MaxConcurrentVerifications: 2, CheckInterval: time.Hour,
			Lockout: config.AuthLockout{Threshold: 5, Window: 15 * time.Minute, Duration: 15 * time.Minute},
		},
	}
}

func (e *env) provider(mut func(*config.AuthOIDCProvider)) config.AuthOIDCProvider {
	p := config.AuthOIDCProvider{
		DisplayName: "Corporate SSO", Issuer: e.idp.issuer, ClientID: "trove", ClientSecretFile: e.secretFile,
		GroupSync: config.AuthOIDCGroupSync{Rules: []config.AuthOIDCGroupRule{{MatchRegex: "trove-team-(.+)", Group: "team-$1"}}},
	}
	if mut != nil {
		mut(&p)
	}
	return p
}

// start builds n replicas with the same configuration and waits until each has
// discovered every provider.
func (e *env) start(n int, auth config.Auth) {
	e.t.Helper()
	for i := range n {
		e.addReplica(fmt.Sprintf("web-%d", i), auth)
	}
}

func (e *env) addReplica(name string, auth config.Auth) *replica {
	e.t.Helper()
	return e.newReplica(name, auth, true)
}

// addReplicaNoWait builds a replica without waiting for discovery.
func (e *env) addReplicaNoWait(name string, auth config.Auth) *replica {
	e.t.Helper()
	return e.newReplica(name, auth, false)
}

func (e *env) newReplica(name string, auth config.Auth, wait bool) *replica {
	t := e.t
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, e.cfg, name, meta.RoleWeb, quiet)
	if err != nil {
		t.Fatalf("Open(%s): %v", name, err)
	}
	reg := prometheus.NewRegistry()
	clu := config.Cluster{EpochPollInterval: 200 * time.Millisecond, EpochMaxStaleness: 20 * time.Second, ChangeRetention: time.Hour, SignalCoalesceWindow: 20 * time.Millisecond}
	changes, err := cluster.NewChanges(cluster.ChangesOptions{DB: db.ControlPool(), Config: clu, WatchdogTimeout: 30 * time.Second, Log: quiet, Metrics: reg})
	if err != nil {
		t.Fatal(err)
	}
	caches, err := cache.NewRegistry(changes, reg)
	if err != nil {
		t.Fatal(err)
	}
	changes.AddListener(caches)
	if err := changes.Start(ctx); err != nil {
		t.Fatalf("changes.Start: %v", err)
	}
	if e.prov == nil {
		e.prov = &fakeProvisioner{db: db}
	}
	origin := func(*http.Request) string { return session.NormalizeOrigin(e.baseURL) }
	addr := func(r *http.Request) string { h, _, _ := strings.Cut(r.RemoteAddr, ":"); return h }
	source := func(r *http.Request) audit.Source { return audit.Source{ClientAddr: addr(r), UserAgent: r.UserAgent()} }
	authMetrics, err := principal.NewMetrics(reg)
	if err != nil {
		t.Fatal(err)
	}
	store, err := session.New(session.Options{
		DB: db, Changes: changes, Fresh: changes, Caches: caches, Audit: e.audit, Log: quiet, Metrics: reg, AuthMetrics: authMetrics,
		Sessions: auth.Sessions, BreakGlass: auth.BreakGlass, Origin: origin, Source: source,
	})
	if err != nil {
		t.Fatal(err)
	}
	out := outbound.New(config.Outbound{TLS: config.OutboundTLS{CAFiles: []string{e.caPEM}, MinVersion: "1.2"}})
	login, err := oidc.New(ctx, oidc.Options{
		DB: db, Sessions: store, Provisioner: e.prov, Audit: e.audit, Log: quiet, Metrics: authMetrics,
		Clients: func(purpose string) *http.Client { return out.Client(purpose, outbound.WithTimeout(10*time.Second)) },
		BaseURL: e.baseURL, Auth: auth,
		Problem:    func(w http.ResponseWriter, r *http.Request, err error) { errs.Write(w, r, err, "", quiet) },
		ClientAddr: addr, Source: source, Origin: origin,
	})
	if err != nil {
		t.Fatalf("oidc.New(%s): %v", name, err)
	}
	if err := login.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if wait && len(auth.OIDC.Providers) > 0 {
		if err := login.WaitDiscovered(ctx); err != nil {
			t.Fatalf("discovery on %s: %v", name, err)
		}
	}
	rep := &replica{name: name, db: db, changes: changes, sessions: store, login: login, mux: http.NewServeMux()}
	rep.mux.HandleFunc(oidc.RouteStart, login.HandleStart)
	rep.mux.HandleFunc(oidc.RouteCallback, login.HandleCallback)
	rep.mux.HandleFunc(oidc.RouteLogout, login.HandleLogout)
	rep.mux.HandleFunc(oidc.RouteLocalLogin, login.HandleLocalLogin)
	if e.realAPI {
		h := e.apiHandler(name, db, changes, caches, reg, store, login, authMetrics, auth)
		rep.mux.Handle("/api/v1/", h)
		rep.mux.Handle("/files/", h) // mounted by e.extend when a test adds the raw file path
	} else {
		// The authentication stage of the identity area resolves the cookie through
		// session.Validator and applies CheckCSRF; these two routes stand in for it.
		rep.mux.HandleFunc("GET /api/v1/auth/whoami", func(w http.ResponseWriter, r *http.Request) {
			si, err := store.Validate(r.Context(), session.CookieValue(r, session.CookieSession))
			if err != nil {
				errs.Write(w, r, session.Problem(err), "", quiet)
				return
			}
			writeJSON(w, map[string]any{"id": si.Principal.ID, "name": si.Principal.Name, "kind": si.Principal.Kind, "session": si.ID})
		})
		rep.mux.HandleFunc("POST /api/v1/tokens", func(w http.ResponseWriter, r *http.Request) {
			si, err := store.Validate(r.Context(), session.CookieValue(r, session.CookieSession))
			if err == nil {
				err = store.CheckCSRF(r, si)
			}
			if err == nil && !si.AuthenticatedWithin(auth.Sessions.RecentAuthWindow) {
				err = session.ReauthenticationRequired()
			}
			if err != nil {
				errs.Write(w, r, session.Problem(err), "", quiet)
				return
			}
			w.WriteHeader(http.StatusCreated)
		})
	}
	t.Cleanup(func() {
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = login.Stop(sctx)
		_ = changes.Stop(sctx)
		db.Close()
	})
	e.mu.Lock()
	e.replicas = append(e.replicas, rep)
	e.mu.Unlock()
	return rep
}

// servedBy returns the replicas of the last n requests.
func (e *env) servedBy(n int) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.served[len(e.served)-n:]...)
}

// browser is one user agent with a cookie jar that follows no redirect itself.
type browser struct {
	t   *testing.T
	e   *env
	c   *http.Client
	jar *cookiejar.Jar
}

func (e *env) browser() *browser {
	jar, _ := cookiejar.New(nil)
	c := &http.Client{
		Jar: jar, Timeout: 60 * time.Second,
		Transport:     &http.Transport{TLSClientConfig: &tls.Config{RootCAs: e.ca.Pool(), MinVersion: tls.VersionTLS12}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return &browser{t: e.t, e: e, c: c, jar: jar}
}

// answer is a response whose body has been read and closed.
type answer struct {
	StatusCode int
	Header     http.Header
}

func (b *browser) do(req *http.Request) (answer, string) {
	b.t.Helper()
	resp, err := b.c.Do(req)
	if err != nil {
		b.t.Fatalf("%s %s: %v", req.Method, req.URL, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return answer{StatusCode: resp.StatusCode, Header: resp.Header}, string(body)
}

func (b *browser) get(u string) (answer, string) {
	b.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	return b.do(req)
}

// post sends a same-origin request with the CSRF header the UI sends.
func (b *browser) post(path, body string, hdr map[string]string) (answer, string) {
	b.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, b.e.baseURL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", b.e.baseURL)
	if c := b.cookie(session.CookieCSRF); c != "" {
		req.Header.Set(session.HeaderCSRF, c)
	}
	for k, v := range hdr {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	return b.do(req)
}

func (b *browser) cookie(name string) string {
	u, _ := url.Parse(b.e.baseURL)
	for _, c := range b.jar.Cookies(u) {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

func (b *browser) setCookie(name, value string) {
	u, _ := url.Parse(b.e.baseURL)
	b.jar.SetCookies(u, []*http.Cookie{{Name: name, Value: value, Path: "/", Secure: true}})
}

// startLogin opens the start route and returns the provider's authorization URL.
func (b *browser) startLogin(provider, query string) *url.URL {
	b.t.Helper()
	resp, body := b.get(b.e.baseURL + "/auth/oidc/" + provider + "/start" + query)
	if resp.StatusCode != http.StatusFound {
		b.t.Fatalf("start: %d %s", resp.StatusCode, body)
	}
	u, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		b.t.Fatal(err)
	}
	return u
}

// authorize lets the provider approve and returns the callback URL.
func (b *browser) authorize(auth *url.URL) string {
	b.t.Helper()
	resp, body := b.get(auth.String())
	if resp.StatusCode != http.StatusFound {
		b.t.Fatalf("authorize: %d %s", resp.StatusCode, body)
	}
	return resp.Header.Get("Location")
}

// login runs a whole browser login and returns the callback's response.
func (b *browser) login(provider string) (answer, string) {
	b.t.Helper()
	cb := b.authorize(b.startLogin(provider, "?return_to=/ui/repos"))
	return b.get(cb)
}

// mustLogin logs in and fails unless the callback redirected into the UI.
func (b *browser) mustLogin(provider string) {
	b.t.Helper()
	resp, body := b.login(provider)
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/ui/repos" {
		b.t.Fatalf("login: %d %s %s", resp.StatusCode, resp.Header.Get("Location"), body)
	}
}

// whoami is GET /api/v1/auth/whoami through the edge.
func (b *browser) whoami() (int, map[string]any) {
	b.t.Helper()
	resp, body := b.get(b.e.baseURL + "/api/v1/auth/whoami")
	var m map[string]any
	_ = json.Unmarshal([]byte(body), &m)
	return resp.StatusCode, m
}

// fakeProvisioner stands in for the identity area's provisioner: it links
// identities by (issuer, subject), creates users just in time and refuses a
// disabled user, writing the rows sessions reference.
type fakeProvisioner struct {
	db *postgres.DB

	mu     sync.Mutex
	claims []principal.LoginClaims
}

func (p *fakeProvisioner) SignIn(ctx context.Context, c principal.LoginClaims) (principal.Principal, error) {
	p.mu.Lock()
	p.claims = append(p.claims, c)
	p.mu.Unlock()
	var out principal.Principal
	err := p.db.InPgxTx(ctx, func(tx pgx.Tx) error {
		var id, name, status string
		err := tx.QueryRow(ctx, `SELECT p.id::text, p.name, p.status FROM user_identity i JOIN principal p ON p.id = i.user_id
		                          WHERE i.issuer = $1 AND i.subject = $2 FOR UPDATE OF p`, c.Issuer, c.Subject).Scan(&id, &name, &status)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			if !c.JIT {
				return principal.ErrNotProvisioned
			}
			id = session.NewID()
			tag, err := tx.Exec(ctx, `INSERT INTO principal (id, kind, name) VALUES ($1::uuid, 'user', $2) ON CONFLICT (kind, name) DO NOTHING`, id, c.Username)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return principal.ErrUsernameTaken
			}
			if _, err := tx.Exec(ctx, `INSERT INTO app_user (principal_id, origin, email, email_verified, display_name) VALUES ($1::uuid, 'jit', $2, $3, $4)`,
				id, c.Email, c.EmailVerified, c.DisplayName); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO user_identity (user_id, provider_id, issuer, subject, linked_at) VALUES ($1::uuid, $2, $3, $4, now())`,
				id, c.Provider, c.Issuer, c.Subject); err != nil {
				return err
			}
			name, status = c.Username, "active"
		case err != nil:
			return err
		}
		if status != "active" {
			return principal.ErrUserDisabled
		}
		out = principal.Principal{ID: id, Kind: principal.KindUser, Name: name, Status: principal.StatusActive}
		return nil
	})
	return out, err
}

func (p *fakeProvisioner) last() principal.LoginClaims {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.claims[len(p.claims)-1]
}

// spy records audit events. With next set it also hands every event on to a real
// recorder (the audit writer), so a test sees them in the audit trail and the log
// stream as well.
type spy struct {
	mu   sync.Mutex
	evs  []audit.Event
	next audit.Recorder
}

func (s *spy) RecordTx(ctx context.Context, tx meta.Tx, ev audit.Event) error {
	s.mu.Lock()
	s.evs = append(s.evs, ev)
	s.mu.Unlock()
	if s.next != nil {
		return s.next.RecordTx(ctx, tx, ev)
	}
	return nil
}

func (s *spy) Record(ctx context.Context, ev audit.Event) {
	s.mu.Lock()
	s.evs = append(s.evs, ev)
	s.mu.Unlock()
	if s.next != nil {
		s.next.Record(ctx, ev)
	}
}

// events returns the recorded events of a type.
func (s *spy) events(typ string) []audit.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []audit.Event
	for _, e := range s.evs {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

// eventually polls cond until it holds or the timeout passes.
func eventually(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return cond()
}
