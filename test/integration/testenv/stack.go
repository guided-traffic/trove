package testenv

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"io"
	"log/slog"
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
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta/postgres"
)

// The in-process harness: one Stack is an installation (a migrated database of
// its own, a Valkey keyspace and an S3 prefix of the stack's) and its replicas
// are the production composition root, app.New, started in the test process.
// Every integration suite that exercises more than one component uses it, so
// the suite runs the chain, the authentication stage and the wiring a replica
// runs in production; a suite of one component (the authorization engine, the
// coordination layer, the audit writer, the blob store) keeps its direct
// environment. docs/developer/build-and-test.md describes the harness.

// StackOptions configure a Stack.
type StackOptions struct {
	// Valkey false points every replica's Sentinel at a port nothing listens on:
	// the state "Valkey stopped" (degraded mode, R-HA-001). True uses the stack's
	// Sentinels.
	Valkey bool
	// Name is part of the database name, trove_<Name>_<random>; "it" if empty.
	Name string
	// Env is appended to the environment of every replica after the harness's own
	// settings, so it wins: the TROVE_* settings a whole suite changes.
	Env []string
}

// Stack is one installation under test.
type Stack struct {
	tb      testing.TB
	name    string
	dir     string
	base    []string
	sealed  string // the key file of the default key ring
	pg      config.Database
	valkey  bool
	adminMu sync.Mutex // the admin connection is not safe for concurrent use
	admin   *pgx.Conn
	val     *apitest.Validator

	mu   sync.Mutex
	reps []*Replica
	next atomic.Int64
}

// Quiet is a logger that discards.
func Quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// RandHex returns n random bytes in hex.
func RandHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// WriteFile writes content to name in dir (0600) and returns the path.
func WriteFile(tb testing.TB, dir, name, content string) string {
	tb.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		tb.Fatal(err)
	}
	return p
}

// NewStack creates the database trove_<Name>_<random>, migrates it and drops it
// when the test ends. No replica runs yet: seed the rows the first replica must
// see (Stack.Repository, Stack.User, Stack.Token) and then Start it.
func NewStack(tb testing.TB, o StackOptions) *Stack {
	tb.Helper()
	if o.Name == "" {
		o.Name = "it"
	}
	host, port := Get(tb, "TROVE_TEST_PG_HOST"), Get(tb, "TROVE_TEST_PG_PORT")
	user, pass := Get(tb, "TROVE_TEST_PG_USER"), Get(tb, "TROVE_TEST_PG_PASSWORD")
	ctx := context.Background()
	maint, err := pgx.Connect(ctx, PgDSN(tb, "postgres"))
	if err != nil {
		tb.Fatalf("connect to PostgreSQL: %v", err)
	}
	name := "trove_" + o.Name + "_" + RandHex(5)
	if _, err := maint.Exec(ctx, "CREATE DATABASE "+name+" ENCODING 'UTF8' TEMPLATE template0"); err != nil {
		tb.Fatalf("create database: %v", err)
	}
	tb.Cleanup(func() {
		_, _ = maint.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		_ = maint.Close(context.Background())
	})
	dir := tb.TempDir()
	userFile, passFile := WriteFile(tb, dir, "db-user", user+"\n"), WriteFile(tb, dir, "db-pass", pass+"\n")
	pgPort, err := strconv.Atoi(port)
	if err != nil {
		tb.Fatalf("TROVE_TEST_PG_PORT: %v", err)
	}
	cfg := config.Database{
		Host: host, Port: pgPort, Name: name, Schema: "trove", UsernameFile: userFile, PasswordFile: passFile,
		SSLMode: "verify-full", SSLRootCertFile: CAFile(tb), ConnectTimeout: 5 * time.Second, TargetSessionAttrs: "read-write",
		Pooler:        config.DatabasePooler{Mode: "none"},
		Pool:          config.DatabasePool{MaxConns: 8, MinConns: 1, MaxConnLifetime: 30 * time.Minute, MaxConnLifetimeJitter: time.Minute, MaxConnIdleTime: 5 * time.Minute, HealthCheckPeriod: 30 * time.Second},
		LeasePoolSize: 2, LeaseCheckInterval: time.Second,
		Session:         config.DatabaseSession{StatementTimeout: 30 * time.Second, LockTimeout: 5 * time.Second, IdleInTransactionSessionTimeout: 60 * time.Second},
		Retry:           config.DatabaseRetry{InitialBackoff: 50 * time.Millisecond, MaxBackoff: 200 * time.Millisecond, MaxElapsed: 2 * time.Second},
		Keepalive:       config.DatabaseKeepalive{Idle: 15 * time.Second, Interval: 5 * time.Second, Count: 3, UserTimeout: 30 * time.Second},
		ServerKeepalive: config.DatabaseServerKeepalive{Idle: 60 * time.Second, Interval: 10 * time.Second, Count: 6},
		Preflight:       config.DatabasePreflight{ConnectionBudget: "enforce", ReservedConnections: 10, AllowSuperuser: true},
	}
	mctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if _, err := postgres.Migrate(mctx, cfg, postgres.MigrateOptions{
		Instance: "it-migrate", Version: "0.1.0", Log: Quiet(),
		Migrate: config.Migrate{AdvisoryLockWait: 20 * time.Second, DDLLockTimeout: 2 * time.Second, DDLLockRetries: 3},
	}); err != nil {
		tb.Fatalf("migrate: %v", err)
	}
	conn, err := pgx.Connect(ctx, PgDSN(tb, name))
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = conn.Close(context.Background()) })
	if _, err := conn.Exec(ctx, "SET search_path TO trove"); err != nil {
		tb.Fatal(err)
	}

	s := &Stack{tb: tb, name: name, dir: dir, pg: cfg, valkey: o.Valkey, admin: conn, val: apitest.New(tb, v1.EmbeddedYAML())}
	var pooler []string
	if PgPooled(tb) {
		// The replicas' main pool goes through PgBouncer in transaction mode; the
		// session class stays direct (R-ARCH-019, R-HA-058).
		cfg = ThroughPgBouncer(tb, cfg, user, maint)
		s.pg = cfg
		pooler = []string{
			"TROVE_DATABASE_PORT=" + strconv.Itoa(cfg.Port), "TROVE_DATABASE_SESSION_HOST=" + cfg.SessionHost,
			"TROVE_DATABASE_SESSION_PORT=" + strconv.Itoa(cfg.SessionPort),
			"TROVE_DATABASE_POOLER_MODE=transaction", "TROVE_DATABASE_POOLER_SERVER_CONNECTIONS=20",
		}
	}
	s.sealed = WriteFile(tb, dir, "2026-10.key", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{42}, 32)))
	s.base = append(s.environ(host, port, name, userFile, passFile), pooler...)
	s.base = append(s.base, o.Env...)
	return s
}

// environ is the environment every replica shares: the database, the stack's
// Valkey (or a Sentinel that never answers), the object store, the key ring and
// the timings that keep the tests quick. A replica adds its identity and its
// ops listener.
func (s *Stack) environ(host, port, name, userFile, passFile string) []string {
	tb, dir := s.tb, s.dir
	vk, s3 := ValkeyConfig(tb), S3Config(tb)
	sentinels := make([]string, 0, len(vk.Sentinels))
	for _, a := range vk.Sentinels {
		sentinels = append(sentinels, strconv.Quote(a))
	}
	if !s.valkey {
		sentinels = []string{strconv.Quote("127.0.0.1:1")}
	}
	return []string{
		"TROVE_DATABASE_HOST=" + host, "TROVE_DATABASE_PORT=" + port, "TROVE_DATABASE_NAME=" + name,
		"TROVE_DATABASE_USERNAME_FILE=" + userFile, "TROVE_DATABASE_PASSWORD_FILE=" + passFile,
		"TROVE_DATABASE_SSL_MODE=verify-full", "TROVE_DATABASE_SSL_ROOT_CERT_FILE=" + CAFile(tb), "TROVE_DATABASE_PREFLIGHT_ALLOW_SUPERUSER=true",
		"TROVE_DATABASE_POOL_MAX_CONNS=6", "TROVE_DATABASE_POOL_MIN_CONNS=1", "TROVE_DATABASE_LEASE_POOL_SIZE=1",
		"TROVE_DATABASE_RETRY_MAX_ELAPSED=3s", "TROVE_HEALTH_DATABASE_OUTAGE_GRACE=2s",
		"TROVE_VALKEY_SENTINEL_ADDRESSES=[" + strings.Join(sentinels, ",") + "]", "TROVE_VALKEY_SENTINEL_PRIMARY_NAME=" + vk.PrimaryName,
		"TROVE_VALKEY_SENTINEL_PASSWORD_FILE=" + WriteFile(tb, dir, "vk-sentinel-pass", vk.SentinelPassword),
		"TROVE_VALKEY_USERNAME_FILE=" + WriteFile(tb, dir, "vk-user", vk.Username), "TROVE_VALKEY_PASSWORD_FILE=" + WriteFile(tb, dir, "vk-pass", vk.Password),
		"TROVE_VALKEY_TLS_CA_FILE=" + vk.CAFile, "TROVE_VALKEY_KEY_PREFIX=" + vk.KeyPrefix,
		"TROVE_STORAGE_S3_BUCKET=" + s3.BlobsBucket, "TROVE_STORAGE_S3_ENDPOINT=" + s3.Endpoint, "TROVE_STORAGE_S3_REGION=" + s3.Region,
		"TROVE_STORAGE_S3_FORCE_PATH_STYLE=true", "TROVE_STORAGE_S3_CA_FILE=" + CAFile(tb),
		"TROVE_STORAGE_S3_CREDENTIALS_SOURCE=static",
		"TROVE_STORAGE_S3_CREDENTIALS_ACCESS_KEY_ID_FILE=" + WriteFile(tb, dir, "s3-id", s3.AccessKey),
		"TROVE_STORAGE_S3_CREDENTIALS_SECRET_ACCESS_KEY_FILE=" + WriteFile(tb, dir, "s3-secret", s3.SecretKey),
		// An S3 prefix of the installation keeps parallel suites apart in one bucket.
		"TROVE_STORAGE_S3_PREFIX=it/" + name,
		"TROVE_SECRETS_ENCRYPTION_KEY_FILES=[" + strconv.Quote(s.sealed) + "]", "TROVE_SECRETS_ACTIVE_KEY_ID=2026-10",
		"TROVE_SERVER_BASE_URL=http://trove.test", "TROVE_SERVER_LISTEN=127.0.0.1:0",
		// The management API's own classes are tested where they are the subject;
		// elsewhere they would only slow the suite (a suite that tests them sets
		// TROVE_API_RATE_LIMITS_ENABLED=true).
		"TROVE_API_RATE_LIMITS_ENABLED=false",
		"TROVE_HEALTH_CHECK_INTERVAL=300ms", "TROVE_HEALTH_CHECK_TIMEOUT=200ms", "TROVE_HEALTH_SHUTDOWN_DRAIN_DELAY=200ms",
		"TROVE_SERVER_SHUTDOWN_TIMEOUT=5s", "TROVE_STORAGE_SPOOL_DIR=" + filepath.Join(dir, "spool"),
		"TROVE_SYSTEM_HEARTBEAT_INTERVAL=1s", "TROVE_SYSTEM_HEARTBEAT_STALE_AFTER=3s",
		"TROVE_CONFIG_FILE=" + WriteFile(tb, dir, "trove.yaml", ""),
	}
}

// Database returns the name of the installation's database.
func (s *Stack) Database() string { return s.name }

// DatabaseConfig is the configuration a component opens the installation's
// database with, the way a replica does (through PgBouncer when
// TROVE_TEST_PG_POOLED is set). It is for the few assertions that need the
// component itself next to the replicas, such as counting the transactions of a
// repository reader.
func (s *Stack) DatabaseConfig() config.Database { return s.pg }

// Dir is the installation's scratch directory.
func (s *Stack) Dir() string { return s.dir }

// KeyFile writes a base64 key file into the scratch directory and returns its
// path: a replica whose key ring differs from the others sets
// TROVE_SECRETS_ENCRYPTION_KEY_FILES to it.
func (s *Stack) KeyFile(name string, key []byte) string {
	s.tb.Helper()
	return WriteFile(s.tb, s.dir, name, base64.StdEncoding.EncodeToString(key))
}

// Validator checks API answers against the OpenAPI document and records which
// operations the suite reached.
func (s *Stack) Validator() *apitest.Validator { return s.val }

// Exec runs a statement on the installation's database as its owner.
func (s *Stack) Exec(sql string, args ...any) {
	s.tb.Helper()
	s.adminMu.Lock()
	defer s.adminMu.Unlock()
	if _, err := s.admin.Exec(context.Background(), sql, args...); err != nil {
		s.tb.Fatalf("%s: %v", sql, err)
	}
}

// Text runs a query that returns one text value.
func (s *Stack) Text(sql string, args ...any) string {
	s.tb.Helper()
	s.adminMu.Lock()
	defer s.adminMu.Unlock()
	var v string
	if err := s.admin.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		s.tb.Fatalf("%s: %v", sql, err)
	}
	return v
}

// Number runs a query that returns one integer.
func (s *Stack) Number(sql string, args ...any) int64 {
	s.tb.Helper()
	s.adminMu.Lock()
	defer s.adminMu.Unlock()
	var v int64
	if err := s.admin.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		s.tb.Fatalf("%s: %v", sql, err)
	}
	return v
}

// TryNumber is Number for a goroutine other than the test's: it reports an error
// instead of failing the test.
func (s *Stack) TryNumber(sql string, args ...any) (int64, error) {
	s.adminMu.Lock()
	defer s.adminMu.Unlock()
	var v int64
	err := s.admin.QueryRow(context.Background(), sql, args...).Scan(&v)
	return v, err
}

// Query runs a query and hands every row to scan.
func (s *Stack) Query(sql string, scan func(pgx.Rows) error, args ...any) {
	s.tb.Helper()
	s.adminMu.Lock()
	defer s.adminMu.Unlock()
	rows, err := s.admin.Query(context.Background(), sql, args...)
	if err != nil {
		s.tb.Fatalf("%s: %v", sql, err)
	}
	defer rows.Close()
	for rows.Next() {
		if err := scan(rows); err != nil {
			s.tb.Fatalf("%s: %v", sql, err)
		}
	}
	if err := rows.Err(); err != nil {
		s.tb.Fatalf("%s: %v", sql, err)
	}
}

// Replicas returns the replicas started so far, in start order.
func (s *Stack) Replicas() []*Replica {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*Replica(nil), s.reps...)
}

// Pick returns the web replicas in turn: the dispatcher of the scatter profile,
// so that consecutive requests of a test land on different replicas (R-ARCH-032).
func (s *Stack) Pick() *Replica {
	var web []*Replica
	for _, r := range s.Replicas() {
		if r.Role != "worker" {
			web = append(web, r)
		}
	}
	if len(web) == 0 {
		s.tb.Fatal("no web replica is running")
	}
	return web[int(s.next.Add(1)-1)%len(web)]
}

// WaitFor polls cond until it holds or the time is up and returns how long it
// took.
func WaitFor(tb testing.TB, what string, within time.Duration, cond func() bool) time.Duration {
	tb.Helper()
	start := time.Now()
	for time.Since(start) < within {
		if cond() {
			return time.Since(start)
		}
		time.Sleep(20 * time.Millisecond)
	}
	tb.Fatalf("%s did not happen within %s", what, within)
	return 0
}
