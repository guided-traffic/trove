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
// The raw file routes (/files/) belong to the files area, which is built in
// parallel; until they are merged a stand-in route below decides every request
// exactly as the contract says the files area does (v1.SubjectFrom, Decide on the
// request's context, authz.NotFound for a missing file).
package endpoints

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/api/v1/apitest"
	"github.com/guided-traffic/trove/internal/app"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func writeFile(t testing.TB, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// The hosts of the endpoints (R-EP-003): the default endpoint checks no host, the
// further ones check theirs.
const (
	customersHost = "packages.trove.test"
	partnersHost  = "partners.trove.test"
)

// env is one installation: its database, the settings every replica shares, the
// principals and tokens the tests use, and the replicas.
type env struct {
	t     *testing.T
	name  string
	admin *pgx.Conn
	mu    sync.Mutex // the admin connection is not safe for concurrent use
	base  []string
	dir   string
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

// newEnv creates the database trove_ep_<random>, migrates it and seeds it. valkey
// false points every replica at a Sentinel that never answers: the state "Valkey
// stopped".
func newEnv(t *testing.T, valkey bool) *env {
	t.Helper()
	host, port := testenv.Get(t, "TROVE_TEST_PG_HOST"), testenv.Get(t, "TROVE_TEST_PG_PORT")
	user, pass := testenv.Get(t, "TROVE_TEST_PG_USER"), testenv.Get(t, "TROVE_TEST_PG_PASSWORD")
	ctx := context.Background()
	maint, err := pgx.Connect(ctx, testenv.PgDSN(t, "postgres"))
	if err != nil {
		t.Fatalf("connect to PostgreSQL: %v", err)
	}
	name := "trove_ep_" + randHex(5)
	if _, err := maint.Exec(ctx, "CREATE DATABASE "+name+" ENCODING 'UTF8' TEMPLATE template0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = maint.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		_ = maint.Close(context.Background())
	})
	dir := t.TempDir()
	userFile, passFile := writeFile(t, dir, "db-user", user+"\n"), writeFile(t, dir, "db-pass", pass+"\n")
	pgPort, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	mctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if _, err := postgres.Migrate(mctx, config.Database{
		Host: host, Port: pgPort, Name: name, Schema: "trove", UsernameFile: userFile, PasswordFile: passFile,
		SSLMode: "verify-full", SSLRootCertFile: testenv.CAFile(t), ConnectTimeout: 5 * time.Second, TargetSessionAttrs: "read-write",
		Pooler:    config.DatabasePooler{Mode: "none"},
		Pool:      config.DatabasePool{MaxConns: 4, MinConns: 1, MaxConnLifetime: 30 * time.Minute, MaxConnLifetimeJitter: time.Minute, MaxConnIdleTime: 5 * time.Minute, HealthCheckPeriod: 30 * time.Second},
		Session:   config.DatabaseSession{StatementTimeout: 30 * time.Second, LockTimeout: 5 * time.Second, IdleInTransactionSessionTimeout: 60 * time.Second},
		Retry:     config.DatabaseRetry{InitialBackoff: 50 * time.Millisecond, MaxBackoff: 200 * time.Millisecond, MaxElapsed: 2 * time.Second},
		Preflight: config.DatabasePreflight{ConnectionBudget: "enforce", ReservedConnections: 10, AllowSuperuser: true},
	}, postgres.MigrateOptions{
		Instance: "it-migrate", Version: "0.1.0", Log: quietLog(),
		Migrate: config.Migrate{AdvisoryLockWait: 20 * time.Second, DDLLockTimeout: 2 * time.Second, DDLLockRetries: 3},
	}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	conn, err := pgx.Connect(ctx, testenv.PgDSN(t, name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	if _, err := conn.Exec(ctx, "SET search_path TO trove"); err != nil {
		t.Fatal(err)
	}

	vk, s3 := testenv.ValkeyConfig(t), testenv.S3Config(t)
	sentinels := make([]string, 0, len(vk.Sentinels))
	for _, s := range vk.Sentinels {
		sentinels = append(sentinels, strconv.Quote(s))
	}
	if !valkey {
		sentinels = []string{strconv.Quote("127.0.0.1:1")}
	}
	key := writeFile(t, dir, "2026-10.key", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{42}, 32)))
	endpoints := fmt.Sprintf(`{"customers":{"listen":"127.0.0.1:0","base_url":"https://%s","surface":["packages","files"]},`+
		`"partners":{"listen":"127.0.0.1:0","base_url":"https://%s","surface":["packages","files"]}}`, customersHost, partnersHost)
	base := []string{
		"TROVE_DATABASE_HOST=" + host, "TROVE_DATABASE_PORT=" + port, "TROVE_DATABASE_NAME=" + name,
		"TROVE_DATABASE_USERNAME_FILE=" + userFile, "TROVE_DATABASE_PASSWORD_FILE=" + passFile,
		"TROVE_DATABASE_SSL_MODE=verify-full", "TROVE_DATABASE_SSL_ROOT_CERT_FILE=" + testenv.CAFile(t), "TROVE_DATABASE_PREFLIGHT_ALLOW_SUPERUSER=true",
		"TROVE_DATABASE_POOL_MAX_CONNS=6", "TROVE_DATABASE_POOL_MIN_CONNS=1", "TROVE_DATABASE_LEASE_POOL_SIZE=1",
		"TROVE_DATABASE_RETRY_MAX_ELAPSED=3s", "TROVE_HEALTH_DATABASE_OUTAGE_GRACE=2s",
		"TROVE_VALKEY_SENTINEL_ADDRESSES=[" + strings.Join(sentinels, ",") + "]", "TROVE_VALKEY_SENTINEL_PRIMARY_NAME=" + vk.PrimaryName,
		"TROVE_VALKEY_SENTINEL_PASSWORD_FILE=" + writeFile(t, dir, "vk-sentinel-pass", vk.SentinelPassword),
		"TROVE_VALKEY_USERNAME_FILE=" + writeFile(t, dir, "vk-user", vk.Username), "TROVE_VALKEY_PASSWORD_FILE=" + writeFile(t, dir, "vk-pass", vk.Password),
		"TROVE_VALKEY_TLS_CA_FILE=" + vk.CAFile, "TROVE_VALKEY_KEY_PREFIX=" + vk.KeyPrefix,
		"TROVE_STORAGE_S3_BUCKET=" + s3.BlobsBucket, "TROVE_STORAGE_S3_ENDPOINT=" + s3.Endpoint, "TROVE_STORAGE_S3_REGION=" + s3.Region,
		"TROVE_STORAGE_S3_FORCE_PATH_STYLE=true", "TROVE_STORAGE_S3_CA_FILE=" + testenv.CAFile(t),
		"TROVE_STORAGE_S3_CREDENTIALS_SOURCE=static",
		"TROVE_STORAGE_S3_CREDENTIALS_ACCESS_KEY_ID_FILE=" + writeFile(t, dir, "s3-id", s3.AccessKey),
		"TROVE_STORAGE_S3_CREDENTIALS_SECRET_ACCESS_KEY_FILE=" + writeFile(t, dir, "s3-secret", s3.SecretKey),
		"TROVE_SECRETS_ENCRYPTION_KEY_FILES=[" + strconv.Quote(key) + "]", "TROVE_SECRETS_ACTIVE_KEY_ID=2026-10",
		"TROVE_SERVER_BASE_URL=http://trove.test", "TROVE_SERVER_LISTEN=127.0.0.1:0",
		"TROVE_SERVER_ENDPOINTS=" + endpoints,
		// The management API's own classes are tested in test/integration/api; the
		// endpoint classes (server.endpoint_rate_limits) stay at their defaults.
		"TROVE_API_RATE_LIMITS_ENABLED=false",
		"TROVE_HEALTH_CHECK_INTERVAL=300ms", "TROVE_HEALTH_CHECK_TIMEOUT=200ms", "TROVE_HEALTH_SHUTDOWN_DRAIN_DELAY=200ms",
		"TROVE_SERVER_SHUTDOWN_TIMEOUT=5s", "TROVE_STORAGE_SPOOL_DIR=" + filepath.Join(dir, "spool"),
		"TROVE_SYSTEM_HEARTBEAT_INTERVAL=1s", "TROVE_SYSTEM_HEARTBEAT_STALE_AFTER=3s",
		"TROVE_CONFIG_FILE=" + writeFile(t, dir, "trove.yaml", ""),
	}
	e := &env{t: t, name: name, admin: conn, base: base, dir: dir, val: apitest.New(t, v1.EmbeddedYAML())}
	e.seed()
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

// principalRow inserts a principal; kind is user or service_account.
func (e *env) principalRow(kind, name string) string {
	id := e.text("INSERT INTO principal (id, kind, name) VALUES (gen_random_uuid(), $1, $2) RETURNING id::text", kind, name)
	if kind == "user" {
		e.exec("INSERT INTO app_user (principal_id, origin) VALUES ($1::uuid, 'admin')", id)
	} else {
		e.exec("INSERT INTO service_account (principal_id) VALUES ($1::uuid)", id)
	}
	return id
}

// tokenRow mints a token of a principal the way token.Service stores it: the
// SHA-256 of the value, never the value (R-AUTH-022).
func (e *env) tokenRow(typ token.Type, owner string, scope token.Scope) string {
	e.t.Helper()
	raw, err := token.Generate(typ)
	if err != nil {
		e.t.Fatal(err)
	}
	h := token.Hash(raw)
	expires := "now() + interval '1 day'"
	if typ == token.TypeSAT {
		expires = "NULL"
	}
	e.exec(`INSERT INTO api_token (id, type, secret_sha256, hint, principal_id, description, scope, created_via, expires_at)
		VALUES (gen_random_uuid(), $1, $2, $3, $4::uuid, 'test', $5::text::jsonb, 'admin-cli', `+expires+`)`,
		string(typ), h[:], token.Hint(raw), owner, string(scope.Marshal()))
	return raw
}

// seed writes the rows the tests need before a replica starts, so every replica
// builds its first grant index with them: repositories, principals, a read grant
// for customer-acme on generic-local and generic-other, the admin role of ops and
// ops-bot, and their tokens.
func (e *env) seed() {
	for _, key := range []string{"generic-local", "generic-other"} {
		e.exec("INSERT INTO repositories (key, class, format, config) VALUES ($1, 'local', 'generic', '{}'::jsonb)", key)
	}
	ops, alice := e.principalRow("user", "ops"), e.principalRow("user", "alice")
	e.acmeID, e.botID = e.principalRow("service_account", "customer-acme"), e.principalRow("service_account", "ops-bot")
	for _, id := range []string{ops, e.botID} {
		e.exec("INSERT INTO authz_role_binding (role, principal_id) VALUES ('admin', $1::uuid)", id)
	}
	policy := e.text("INSERT INTO authz_policy (id, name) VALUES (gen_random_uuid(), 'customers-read') RETURNING id::text")
	for i, key := range []string{"generic-local", "generic-other"} {
		e.exec("INSERT INTO authz_policy_selector (policy_id, ordinal, kind, repository_id) SELECT $1::uuid, $2, 'key', id FROM repositories WHERE key = $3", policy, i, key)
	}
	e.exec("INSERT INTO authz_policy_entry (policy_id, principal_id, actions) VALUES ($1::uuid, $2::uuid, ARRAY['read'])", policy, e.acmeID)
	e.opsPAT = e.tokenRow(token.TypePAT, ops, token.Scope{Inherit: true, Admin: true})
	e.alicePAT = e.tokenRow(token.TypePAT, alice, token.DefaultScope())
	e.acmeSAT = e.tokenRow(token.TypeSAT, e.acmeID, token.Scope{Inherit: true, Endpoints: []string{"customers", "partners"}})
	e.acmeSAT2 = e.tokenRow(token.TypeSAT, e.acmeID, token.Scope{Inherit: true, Endpoints: []string{"customers"}})
	e.acmeUnbound = e.tokenRow(token.TypeSAT, e.acmeID, token.DefaultScope())
	e.acmeUnbound2 = e.tokenRow(token.TypeSAT, e.acmeID, token.DefaultScope())
	e.botAdminSAT = e.tokenRow(token.TypeSAT, e.botID, token.Scope{Inherit: true, Admin: true, Endpoints: []string{"customers"}})
}

// syncBuffer is a log sink that is safe to read while a replica writes.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// replica is one in-process replica with its three client listeners.
type replica struct {
	name                    string
	app                     *app.App
	logs                    *syncBuffer
	def, cust, partner, ops string
}

// files is the content the stand-in file route serves; any other path is missing.
var files = map[string]string{
	"generic-local/releases/1.0/app.zip": "app 1.0",
	"generic-local/releases/2.0/app.zip": "app 2.0",
	"generic-local/internal/x.bin":       "internal",
	"generic-other/releases/1.0/app.zip": "other",
}

// standInFiles registers the stand-in of the files area's raw download and of a
// repository listing: both decide exactly as the contract says
// (docs/developer/m1-contracts.md "Request flow"): the subject from the principal
// of the chain, Decide on the request's context, which carries the endpoint, and
// authz.NotFound for a missing file, so hidden and missing are the same bytes.
func standInFiles(a *atomic.Pointer[app.App]) func(*httpserver.Router) {
	return func(r *httpserver.Router) {
		r.HandleFunc("GET /files/{repoKey}/{path...}", func(w http.ResponseWriter, req *http.Request) {
			ctx := req.Context()
			repo, path := req.PathValue("repoKey"), req.PathValue("path")
			d := a.Load().Authorizer().Decide(ctx, v1.SubjectFrom(httpserver.PrincipalFrom(ctx)), authz.Coordinate{
				Repository: repo, Path: path, Kind: authz.KindArtifact, Actions: []authz.Action{authz.Read},
			})
			if d.Outcome != authz.Allow {
				httpserver.Problem(w, req, d.Err())
				return
			}
			body, ok := files[repo+"/"+path]
			if !ok {
				httpserver.Problem(w, req, authz.NotFound())
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = io.WriteString(w, body)
		})
		r.HandleFunc("GET /files/{$}", func(w http.ResponseWriter, req *http.Request) {
			ctx := req.Context()
			keys, err := a.Load().Authorizer().Readable(ctx, v1.SubjectFrom(httpserver.PrincipalFrom(ctx)), []string{"generic-local", "generic-other"})
			if err != nil {
				httpserver.Problem(w, req, err)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(keys)
		})
	}
}

// start brings up one replica of role web and waits until it serves.
func (e *env) start(name string) *replica {
	t := e.t
	t.Helper()
	// The ops listener's address must differ from server.listen as written, so it
	// gets a free port of its own.
	environ := append(append([]string{}, e.base...), "TROVE_POD_NAME="+name, "TROVE_SERVER_OPS_LISTEN="+freeAddr(t))
	loaded, err := config.Load(config.Options{Environ: environ, Flags: map[string]string{"process.role": "web"}, Command: config.CommandServe})
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	logs := &syncBuffer{}
	var ptr atomic.Pointer[app.App]
	a, err := app.New(app.Options{Loaded: loaded, Role: "web", Environ: environ, LogWriter: logs, Extra: standInFiles(&ptr)})
	if err != nil {
		t.Fatal(err)
	}
	ptr.Store(a)
	ctx, cancel := context.WithCancel(context.Background())
	if err := a.Start(ctx); err != nil {
		cancel()
		t.Fatalf("start %s: %v\n%s", name, err, logs.String())
	}
	select {
	case <-a.Started():
	case <-time.After(90 * time.Second):
		cancel()
		t.Fatalf("%s did not start\n%s", name, logs.String())
	}
	t.Cleanup(func() {
		cancel()
		sctx, scancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer scancel()
		_ = a.Shutdown(sctx)
		if t.Failed() {
			t.Logf("log of %s:\n%s", name, tail(logs.String(), 40))
		}
	})
	r := &replica{
		name: name, app: a, logs: logs,
		def: a.EndpointAddr("default"), cust: a.EndpointAddr("customers"), partner: a.EndpointAddr("partners"), ops: a.OpsAddr().String(),
	}
	e.reps = append(e.reps, r)
	return r
}

func freeAddr(t testing.TB) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

var client = &http.Client{Transport: &http.Transport{DisableKeepAlives: true, MaxIdleConnsPerHost: -1}, Timeout: 30 * time.Second}

// answer is one response, read.
type answer struct {
	status int
	header http.Header
	body   []byte
}

func (a answer) json(t testing.TB) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(a.body, &m); err != nil {
		t.Fatalf("answer %d is not a JSON object: %v\n%s", a.status, err, a.body)
	}
	return m
}

// problem returns the problem body without its per-request members.
func (a answer) problem(t testing.TB) string {
	t.Helper()
	m := a.json(t)
	delete(m, "requestId")
	delete(m, "instance")
	b, _ := json.Marshal(m)
	return string(b)
}

// send sends one request to an address with the Host of the endpoint and an
// optional token. Answers of the management API are validated against the
// document.
func (e *env) send(addr, host, method, path, tok string, body any, hdr ...string) answer {
	e.t.Helper()
	var rd io.Reader
	var raw []byte
	switch b := body.(type) {
	case nil:
	case []byte:
		raw = b
	default:
		var err error
		if raw, err = json.Marshal(b); err != nil {
			e.t.Fatal(err)
		}
	}
	if raw != nil {
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, "http://"+addr+path, rd)
	if err != nil {
		e.t.Fatal(err)
	}
	req.Host = host
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	if raw != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	res, err := client.Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	a := answer{status: res.StatusCode, header: res.Header, body: b}
	if strings.HasPrefix(path, "/api/v1/") && host == "trove.test" {
		check, _ := http.NewRequest(method, "http://trove.test"+path, nil)
		if _, err := e.val.Check(check, a.status, a.header, a.body); err != nil {
			e.t.Errorf("response validation: %v\n%s", err, a.body)
		}
	}
	return a
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

// propagation is the bound of R-AUTHZ-028 the tests hold a rule change to: one
// cluster.epoch_poll_interval (the default, 1 s) plus one second.
const propagation = 2 * time.Second

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
