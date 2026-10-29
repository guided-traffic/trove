package postgres

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/guided-traffic/trove/internal/config"
)

func testConfig() config.Database {
	return config.Database{
		Host: "db.example", Port: 5432, Name: "trove", Schema: "trove",
		SSLMode: "verify-full", TargetSessionAttrs: "read-write", ConnectTimeout: 5 * time.Second,
		Pool:          config.DatabasePool{MaxConns: 20, MinConns: 2},
		LeasePoolSize: 4,
		Session: config.DatabaseSession{
			StatementTimeout: 30 * time.Second, LockTimeout: 5 * time.Second,
			IdleInTransactionSessionTimeout: 60 * time.Second,
		},
		ServerKeepalive: config.DatabaseServerKeepalive{Idle: 60 * time.Second, Interval: 10 * time.Second, Count: 6},
		Retry:           config.DatabaseRetry{InitialBackoff: 100 * time.Millisecond, MaxBackoff: 2 * time.Second, MaxElapsed: 30 * time.Second},
		Preflight:       config.DatabasePreflight{ReservedConnections: 10},
		UsernameFile:    "/nonexistent/username",
	}
}

func TestApplicationName(t *testing.T) {
	t.Run("R-ARCH-019 the name is trove:<instance>:<class>", func(t *testing.T) {
		if got := ApplicationName("trove-web-7d9", "main"); got != "trove:trove-web-7d9:main" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("R-HA-015 a long instance is cut to 63 bytes and keeps its class", func(t *testing.T) {
		got := ApplicationName(strings.Repeat("p", 100), "lease")
		if len(got) != 63 || !strings.HasSuffix(got, ":lease") || !strings.HasPrefix(got, "trove:p") {
			t.Fatalf("got %q (%d)", got, len(got))
		}
	})
}

func TestRuntimeParams(t *testing.T) {
	c := testConfig()
	t.Run("R-HA-015 a direct connection sends every session parameter", func(t *testing.T) {
		p := runtimeParams(c, "pod", connKind{class: classMain})
		want := map[string]string{
			"application_name": "trove:pod:main", "TimeZone": "UTC", "statement_timeout": "30000",
			"lock_timeout": "5000", "idle_in_transaction_session_timeout": "60000", "search_path": "trove",
			"tcp_keepalives_idle": "60", "tcp_keepalives_interval": "10", "tcp_keepalives_count": "6",
		}
		for k, v := range want {
			if p[k] != v {
				t.Errorf("%s = %q, want %q", k, p[k], v)
			}
		}
	})
	t.Run("R-HA-015 behind a transaction-mode pooler only application_name and TimeZone travel", func(t *testing.T) {
		p := runtimeParams(c, "pod", connKind{class: classMain, pooled: true})
		if len(p) != 2 || p["application_name"] == "" || p["TimeZone"] != "UTC" {
			t.Fatalf("params = %v", p)
		}
	})
	t.Run("R-HA-015 migrations run without a statement timeout", func(t *testing.T) {
		p := runtimeParams(c, "pod", connKind{class: classMigrate, noLimit: true})
		if p["statement_timeout"] != "0" || p["application_name"] != "trove:pod:migrate" {
			t.Fatalf("params = %v", p)
		}
	})
}

func TestEndpoints(t *testing.T) {
	c := testConfig()
	c.SessionHost, c.SessionPort = "pg-direct", 6543
	c.ControlHost, c.ControlName = "pooler-control", "trove_ctl"
	t.Run("R-ARCH-017 session connections use the session host and port", func(t *testing.T) {
		if e := sessionEndpoint(c); e.host != "pg-direct" || e.port != 6543 || e.name != "trove" {
			t.Fatalf("%+v", e)
		}
	})
	t.Run("R-ARCH-019 the control connection uses the control endpoint, empty keys fall back", func(t *testing.T) {
		if e := controlEndpoint(c); e.host != "pooler-control" || e.port != 5432 || e.name != "trove_ctl" {
			t.Fatalf("%+v", e)
		}
		if e := controlEndpoint(testConfig()); e != mainEndpoint(testConfig()) {
			t.Fatalf("%+v", e)
		}
	})
}

func TestExecMode(t *testing.T) {
	c := testConfig()
	t.Run("R-ARCH-019 direct and session-mode pools use cache_statement", func(t *testing.T) {
		if execMode(c, false) != pgx.QueryExecModeCacheStatement {
			t.Fatal("direct")
		}
		c.Pooler.Mode = "session"
		if execMode(c, false) != pgx.QueryExecModeCacheStatement {
			t.Fatal("session")
		}
	})
	t.Run("R-ARCH-019 a transaction-mode pooler gets the exec mode", func(t *testing.T) {
		c.Pooler.Mode = "transaction"
		if execMode(c, true) != pgx.QueryExecModeExec {
			t.Fatal("transaction")
		}
		// The session classes never go through the pooler.
		if execMode(c, false) != pgx.QueryExecModeCacheStatement {
			t.Fatal("session class")
		}
	})
}

func TestCheckConfig(t *testing.T) {
	c := testConfig()
	t.Run("R-ARCH-019 max_conns below 2 names the key", func(t *testing.T) {
		c := c
		c.Pool.MaxConns = 1
		if err := checkConfig(c); err == nil || !strings.Contains(err.Error(), "database.pool.max_conns") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("R-HA-015 an insecure ssl_mode needs allow_insecure", func(t *testing.T) {
		c := c
		c.SSLMode = "disable"
		if err := checkConfig(c); err == nil || !strings.Contains(err.Error(), "allow_insecure") {
			t.Fatalf("err = %v", err)
		}
		c.AllowInsecure = true
		if err := checkConfig(c); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("R-HA-015 require without a root certificate file needs allow_insecure", func(t *testing.T) {
		c := c
		c.SSLMode, c.SSLRootCertFile = "require", ""
		if err := checkConfig(c); err == nil || !strings.Contains(err.Error(), "ssl_root_cert_file") {
			t.Fatalf("err = %v", err)
		}
		c.SSLRootCertFile = "/etc/ssl/db-ca.pem"
		if err := checkConfig(c); err != nil {
			t.Fatalf("with a CA file: %v", err)
		}
		c.SSLRootCertFile, c.AllowInsecure = "", true
		if err := checkConfig(c); err != nil {
			t.Fatalf("with allow_insecure: %v", err)
		}
	})
	t.Run("R-HA-015 verify-ca without a root certificate file is refused, also with allow_insecure", func(t *testing.T) {
		c := c
		c.SSLMode, c.SSLRootCertFile, c.AllowInsecure = "verify-ca", "", true
		if err := checkConfig(c); err == nil || !strings.Contains(err.Error(), "ssl_root_cert_file") {
			t.Fatalf("err = %v", err)
		}
		c.SSLRootCertFile = "/etc/ssl/db-ca.pem"
		if err := checkConfig(c); err != nil {
			t.Fatalf("with a CA file: %v", err)
		}
	})
	t.Run("R-HA-058 a transaction-mode pooler needs a session host", func(t *testing.T) {
		c := c
		c.Pooler.Mode = "transaction"
		if err := checkConfig(c); err == nil || !strings.Contains(err.Error(), "session_host") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("R-HA-015 a schema name that is not a plain identifier is refused", func(t *testing.T) {
		c := c
		c.Schema = `tro"ve; drop`
		if err := checkConfig(c); err == nil || !strings.Contains(err.Error(), "database.schema") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestCredentialFiles(t *testing.T) {
	dir := t.TempDir()
	user, pw := filepath.Join(dir, "username"), filepath.Join(dir, "password")
	write := func(p, v string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c := testConfig()
	c.UsernameFile, c.PasswordFile = user, pw
	write(user, "trove_app_a\n")
	write(pw, "s3cret\n")
	bc := beforeConnect(c, nil)

	t.Run("R-HA-015 user and password come from their files", func(t *testing.T) {
		cc := &pgx.ConnConfig{}
		if err := bc(context.Background(), cc); err != nil {
			t.Fatal(err)
		}
		if cc.User != "trove_app_a" || cc.Password != "s3cret" {
			t.Fatalf("user %q password %q", cc.User, cc.Password)
		}
	})
	t.Run("R-HA-017 the files are read again for every new connection", func(t *testing.T) {
		write(pw, "rotated\n")
		cc := &pgx.ConnConfig{}
		if err := bc(context.Background(), cc); err != nil {
			t.Fatal(err)
		}
		if cc.Password != "rotated" {
			t.Fatalf("password %q", cc.Password)
		}
	})
	t.Run("R-HA-015 a missing file names the key", func(t *testing.T) {
		c.PasswordFile = filepath.Join(dir, "missing")
		err := beforeConnect(c, nil)(context.Background(), &pgx.ConnConfig{})
		if err == nil || !strings.Contains(err.Error(), "database.password_file") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("R-HA-015 the connection string carries no credential", func(t *testing.T) {
		s := connString(c, mainEndpoint(c))
		if strings.Contains(s, "password") || strings.Contains(s, "user=") {
			t.Fatalf("connection string %q", s)
		}
	})
}

func TestConnectionBudget(t *testing.T) {
	t.Run("R-HA-016 the direct example of the requirement: 171", func(t *testing.T) {
		c := testConfig()
		c.Preflight.ReservedConnections = 10
		b := ConnectionBudget(c, 4, 3, 3) // web_max 4, worker_max 3, superuser_reserved_connections 3
		if b.Total != 171 {
			t.Fatalf("Total = %d (%+v)", b.Total, b)
		}
	})
	t.Run("R-HA-016 the pooled example of the requirement: 147", func(t *testing.T) {
		c := testConfig()
		c.Pooler = config.DatabasePooler{Mode: "transaction", ServerConnections: 100}
		c.Pool.MaxConns = 30
		b := ConnectionBudget(c, 16, 6, 3)
		if b.Total != 100+6*4+2+4+10+3 {
			t.Fatalf("Total = %d (%+v)", b.Total, b)
		}
	})
	t.Run("R-HA-016 a shortfall names the required and the available count", func(t *testing.T) {
		c := testConfig()
		c.Preflight.ExpectedWebReplicas, c.Preflight.ExpectedWorkerReplicas = 4, 3
		err := checkBudget(c, 100, 3, discardLogger())
		if err == nil || !strings.Contains(err.Error(), "171") || !strings.Contains(err.Error(), "100") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("R-HA-016 warn logs and continues", func(t *testing.T) {
		c := testConfig()
		c.Preflight.ConnectionBudget = "warn"
		c.Preflight.ExpectedWebReplicas, c.Preflight.ExpectedWorkerReplicas = 4, 3
		if err := checkBudget(c, 100, 3, discardLogger()); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("R-HA-016 unset replica counts skip the check", func(t *testing.T) {
		if err := checkBudget(testConfig(), 10, 3, discardLogger()); err != nil {
			t.Fatal(err)
		}
	})
}

func TestClassify(t *testing.T) {
	pg := func(code string) error { return &pgconn.PgError{Code: code} }
	cases := []struct {
		name string
		err  error
		want retryKind
	}{
		{"serialization failure", pg("40001"), retrySerialization},
		{"deadlock", pg("40P01"), retrySerialization},
		{"admin shutdown", pg("57P01"), retryFailover},
		{"cannot connect now", pg("57P03"), retryFailover},
		{"read-only transaction after a switchover", pg("25006"), retryFailover},
		{"connection exception class", pg("08006"), retryFailover},
		{"unique violation is the caller's", pg("23505"), retryNo},
		{"statement timeout is not retried", pg("57014"), retryNo},
		{"io.EOF", io.EOF, retryFailover},
		{"a closed connection", &connClosedError{err: errors.New("x")}, retryFailover},
		{"an unknown error", errors.New("boom"), retryNo},
		{"an unknown commit outcome", &outcomeUnknownError{err: io.EOF}, retryNo},
	}
	for _, c := range cases {
		t.Run("R-ARCH-019 "+c.name, func(t *testing.T) {
			if got := classify(c.err); got != c.want {
				t.Fatalf("classify = %d, want %d", got, c.want)
			}
		})
	}
}

func TestRetrier(t *testing.T) {
	cfg := config.DatabaseRetry{InitialBackoff: 100 * time.Millisecond, MaxBackoff: 400 * time.Millisecond, MaxElapsed: time.Second}
	newR := func() (*retrier, *time.Duration) {
		var slept time.Duration
		now := time.Unix(0, 0)
		r := retrier{
			cfg:   cfg,
			sleep: func(_ context.Context, d time.Duration) error { slept += d; now = now.Add(d); return nil },
			now:   func() time.Time { return now },
		}
		return &r, &slept
	}
	t.Run("R-ARCH-019 a serialization failure is repeated three times, then returned", func(t *testing.T) {
		r, _ := newR()
		calls := 0
		err := r.do(context.Background(), func() error { calls++; return pg40001() })
		if calls != 4 || classify(err) != retrySerialization {
			t.Fatalf("calls = %d, err = %v", calls, err)
		}
	})
	t.Run("R-ARCH-019 a retry that succeeds returns nil", func(t *testing.T) {
		r, _ := newR()
		calls := 0
		err := r.do(context.Background(), func() error {
			calls++
			if calls < 3 {
				return pg40001()
			}
			return nil
		})
		if err != nil || calls != 3 {
			t.Fatalf("calls = %d, err = %v", calls, err)
		}
	})
	t.Run("R-HA-015 failover errors are retried within retry.max_elapsed, then become 503", func(t *testing.T) {
		r, slept := newR()
		calls := 0
		err := r.do(context.Background(), func() error { calls++; return io.EOF })
		if !IsUnavailable(err) {
			t.Fatalf("err = %v", err)
		}
		if *slept > cfg.MaxElapsed || calls < 3 {
			t.Fatalf("slept %s in %d calls", *slept, calls)
		}
	})
	t.Run("R-ARCH-019 an error that is not retryable returns at once", func(t *testing.T) {
		r, _ := newR()
		calls := 0
		want := errors.New("constraint")
		if err := r.do(context.Background(), func() error { calls++; return want }); !errors.Is(err, want) || calls != 1 {
			t.Fatalf("calls = %d, err = %v", calls, err)
		}
	})
	t.Run("R-ARCH-019 a commit with an unknown outcome is never repeated", func(t *testing.T) {
		r, _ := newR()
		calls := 0
		err := r.do(context.Background(), func() error { calls++; return &outcomeUnknownError{err: io.EOF} })
		if calls != 1 || !IsUnavailable(err) {
			t.Fatalf("calls = %d, err = %v", calls, err)
		}
	})
	t.Run("R-ARCH-019 a cancelled context stops the retries", func(t *testing.T) {
		r, _ := newR()
		ctx, cancel := context.WithCancel(context.Background())
		calls := 0
		err := r.do(ctx, func() error { calls++; cancel(); return io.EOF })
		if calls != 1 || err == nil {
			t.Fatalf("calls = %d, err = %v", calls, err)
		}
	})
}

func pg40001() error { return &pgconn.PgError{Code: "40001"} }

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
