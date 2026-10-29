//go:build integration

// Package api tests the management API conventions against the integration
// stack, across several in-process replicas that share one database and one
// Valkey: cursor walks, ETag with If-Match, merge patch and idempotency keys on
// a test resource, the cluster-wide rate limits with Valkey running and stopped,
// the job and task operations with a web replica that enqueues and a worker
// replica that executes, and the key-ring status of replicas whose rings differ.
// Every test creates a database of its own.
package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/secrets"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

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

func writeFile(t testing.TB, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// env is one test's database and Valkey keyspace.
type env struct {
	t     *testing.T
	db    string
	cfg   config.Database
	admin *pgx.Conn
	ks    string
	vcfg  config.Valkey
}

// newEnv creates the database trove_api_<random>, migrates it and drops it when
// the test ends.
func newEnv(t *testing.T) *env {
	t.Helper()
	port, err := strconv.Atoi(testenv.Get(t, "TROVE_TEST_PG_PORT"))
	if err != nil {
		t.Fatal(err)
	}
	host, user, secret := testenv.Get(t, "TROVE_TEST_PG_HOST"), testenv.Get(t, "TROVE_TEST_PG_USER"), testenv.Get(t, "TROVE_TEST_PG_PASSWORD")
	connect := func(database string) *pgx.Conn {
		cfg, err := pgx.ParseConfig(testenv.PgDSN(t, database))
		if err != nil {
			t.Fatal(err)
		}
		conn, err := pgx.ConnectConfig(context.Background(), cfg)
		if err != nil {
			t.Fatalf("connect to %s: %v", database, err)
		}
		t.Cleanup(func() { _ = conn.Close(context.Background()) })
		return conn
	}
	name := "trove_api_" + randHex(5)
	maint := connect("postgres")
	if _, err := maint.Exec(context.Background(), "CREATE DATABASE "+name+" ENCODING 'UTF8' TEMPLATE template0"); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() { _, _ = maint.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)") })

	userFile, passFile := writeFile(t, "username", user+"\n"), writeFile(t, "password", secret+"\n")
	cfg := config.Database{
		Host: host, Port: port, Name: name, Schema: "trove",
		UsernameFile: userFile, PasswordFile: passFile,
		SSLMode: "verify-full", SSLRootCertFile: testenv.CAFile(t),
		ConnectTimeout: 5 * time.Second, TargetSessionAttrs: "read-write",
		Pooler:        config.DatabasePooler{Mode: "none"},
		Pool:          config.DatabasePool{MaxConns: 12, MinConns: 1, MaxConnLifetime: 30 * time.Minute, MaxConnLifetimeJitter: time.Minute, MaxConnIdleTime: 5 * time.Minute, HealthCheckPeriod: 30 * time.Second},
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
		cfg = testenv.ThroughPgBouncer(t, cfg, user, maint)
	}
	e := &env{t: t, db: name, cfg: cfg, admin: connect(name), ks: randHex(8), vcfg: valkeyConfig(t)}
	// The sealed column the key-ring tests register exists in every database,
	// because the registry is process-wide.
	e.exec("CREATE TABLE trove.vault_items (id text PRIMARY KEY, secret text)")
	e.exec("GRANT ALL ON trove.vault_items TO PUBLIC")
	return e
}

// vaultColumn is a sealed column of the test process (secrets.Register is
// process-wide and refuses registrations after Freeze, which no test calls).
var vaultColumn = secrets.Register("vault_items", "id", "secret")

func secretsConfig(active string, files ...string) config.Secrets {
	return config.Secrets{EncryptionKeyFiles: files, ActiveKeyID: active}
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.admin.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

func (e *env) scalar(sql string, args ...any) (v int64) {
	e.t.Helper()
	if err := e.admin.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return v
}

var keyPrefix = "trove:ita" + randHex(3) + ":"

// valkeyConfig is the configuration of a replica that talks to the stack's
// Valkey the way the chart configures it.
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

func newValkey(t *testing.T, e *env, name string) *cluster.Valkey {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	vk, err := cluster.NewValkey(ctx, e.vcfg, name, "web", quiet, prometheus.NewRegistry(), cluster.WithKeyspaceID(e.ks))
	if err != nil {
		t.Fatalf("NewValkey(%s): %v", name, err)
	}
	t.Cleanup(vk.Close)
	for end := time.Now().Add(20 * time.Second); !vk.Available(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("Valkey is not available for %s", name)
		}
	}
	return vk
}

func defaultJobs() config.Jobs {
	return config.Jobs{
		Workers:       config.JobsWorkers{Default: 4, Maintenance: 1, Index: 2, Delivery: 8},
		PollInterval:  500 * time.Millisecond,
		LeaseDuration: 6 * time.Second, HeartbeatInterval: 500 * time.Millisecond,
		ScheduleTick: time.Second, Retention: 720 * time.Hour,
	}
}

func defaultCluster() config.Cluster {
	return config.Cluster{
		LeaseRetryInterval: 500 * time.Millisecond, EpochPollInterval: time.Second, EpochMaxStaleness: 20 * time.Second,
		ChangeRetention: time.Hour, SignalCoalesceWindow: 20 * time.Millisecond,
	}
}

func defaultSystem() config.System {
	return config.System{HeartbeatInterval: 500 * time.Millisecond, HeartbeatStaleAfter: 3 * time.Second}
}

func apiConfig() config.API {
	return config.API{
		PageSizeDefault: 50, PageSizeMax: 500, IdempotencyRetention: time.Hour,
		MaxRequestBodyBytes: config.MiB, MaxConfigurationBodyBytes: 4 * config.MiB,
		RateLimits: config.APIRateLimits{
			Enabled:   true,
			Read:      config.APIRateLimitRead{RequestsPerSecond: 50, Burst: 200},
			Write:     config.APIRateLimitWrite{RequestsPerSecond: 10, Burst: 50},
			Anonymous: config.APIRateLimitAnonymous{RequestsPerSecond: 5, Burst: 20},
		},
	}
}
