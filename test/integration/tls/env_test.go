//go:build integration

package tls

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/tlsfiles/tlstest"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func write(t testing.TB, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// copyCA copies the stack's CA into dir, so a test can rewrite its own copy in
// place without touching the file other suites read.
func copyCA(t testing.TB, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(testenv.CAFile(t))
	if err != nil {
		t.Fatal(err)
	}
	return write(t, dir, name, string(b))
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

// newDatabase creates a database of its own for the test and drops it at the end.
func newDatabase(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	maint, err := pgx.Connect(ctx, testenv.PgDSN(t, "postgres"))
	if err != nil {
		t.Fatalf("connect to PostgreSQL: %v", err)
	}
	name := "trove_tls_" + randHex(5)
	if _, err := maint.Exec(ctx, "CREATE DATABASE "+name+" ENCODING 'UTF8' TEMPLATE template0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = maint.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		_ = maint.Close(context.Background())
	})
	return name
}

// databaseEnv are the TROVE_DATABASE_* settings of the stack's PostgreSQL with
// the given root CA file: verify-full, credentials from files.
func databaseEnv(t *testing.T, dir, name, caFile string) []string {
	t.Helper()
	return []string{
		"TROVE_DATABASE_HOST=" + testenv.Get(t, "TROVE_TEST_PG_HOST"), "TROVE_DATABASE_PORT=" + testenv.Get(t, "TROVE_TEST_PG_PORT"),
		"TROVE_DATABASE_NAME=" + name,
		"TROVE_DATABASE_USERNAME_FILE=" + write(t, dir, "db-user", testenv.Get(t, "TROVE_TEST_PG_USER")+"\n"),
		"TROVE_DATABASE_PASSWORD_FILE=" + write(t, dir, "db-pass", testenv.Get(t, "TROVE_TEST_PG_PASSWORD")+"\n"),
		"TROVE_DATABASE_SSL_MODE=verify-full", "TROVE_DATABASE_SSL_ROOT_CERT_FILE=" + caFile,
		"TROVE_DATABASE_PREFLIGHT_ALLOW_SUPERUSER=true", "TROVE_DATABASE_RETRY_MAX_ELAPSED=3s", "TROVE_HEALTH_DATABASE_OUTAGE_GRACE=2s",
	}
}

// loadConfig loads the configuration of env the way the binary does.
func loadConfig(t *testing.T, cmd config.Command, env []string) *config.Config {
	t.Helper()
	empty := write(t, t.TempDir(), "trove.yaml", "")
	l, err := config.Load(config.Options{Environ: append(env, "TROVE_CONFIG_FILE="+empty), Command: cmd})
	if err != nil {
		t.Fatal(err)
	}
	return l.Config
}

// install is one installation on the stack: its own database, key ring and
// object-store prefix; every dependency over TLS with verify-full.
type install struct {
	env []string
	dir string
}

func newInstall(t *testing.T) *install {
	t.Helper()
	dir := t.TempDir()
	name := newDatabase(t)
	vk, s3 := testenv.ValkeyConfig(t), testenv.S3Config(t)
	var sentinels []string
	for _, s := range vk.Sentinels {
		sentinels = append(sentinels, strconv.Quote(s))
	}
	key := write(t, dir, "2026-10.key", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
	env := append(databaseEnv(t, dir, name, testenv.CAFile(t)),
		"TROVE_VALKEY_SENTINEL_ADDRESSES=["+strings.Join(sentinels, ",")+"]", "TROVE_VALKEY_SENTINEL_PRIMARY_NAME="+vk.PrimaryName,
		"TROVE_VALKEY_SENTINEL_PASSWORD_FILE="+write(t, dir, "vk-sentinel-pass", vk.SentinelPassword),
		"TROVE_VALKEY_USERNAME_FILE="+write(t, dir, "vk-user", vk.Username), "TROVE_VALKEY_PASSWORD_FILE="+write(t, dir, "vk-pass", vk.Password),
		"TROVE_VALKEY_TLS_CA_FILE="+vk.CAFile, "TROVE_VALKEY_KEY_PREFIX="+vk.KeyPrefix,
		"TROVE_STORAGE_S3_BUCKET="+s3.BlobsBucket, "TROVE_STORAGE_S3_ENDPOINT="+s3.Endpoint, "TROVE_STORAGE_S3_REGION="+s3.Region,
		"TROVE_STORAGE_S3_FORCE_PATH_STYLE=true", "TROVE_STORAGE_S3_CA_FILE="+testenv.CAFile(t),
		"TROVE_STORAGE_S3_PREFIX=it-tls/"+randHex(5),
		"TROVE_STORAGE_S3_CREDENTIALS_SOURCE=static",
		"TROVE_STORAGE_S3_CREDENTIALS_ACCESS_KEY_ID_FILE="+write(t, dir, "s3-id", s3.AccessKey),
		"TROVE_STORAGE_S3_CREDENTIALS_SECRET_ACCESS_KEY_FILE="+write(t, dir, "s3-secret", s3.SecretKey),
		"TROVE_SECRETS_ENCRYPTION_KEY_FILES=["+strconv.Quote(key)+"]", "TROVE_SECRETS_ACTIVE_KEY_ID=2026-10",
		"TROVE_SERVER_BASE_URL=https://localhost",
		"TROVE_HEALTH_CHECK_INTERVAL=300ms", "TROVE_HEALTH_CHECK_TIMEOUT=200ms", "TROVE_HEALTH_SHUTDOWN_DRAIN_DELAY=500ms",
		"TROVE_SERVER_SHUTDOWN_TIMEOUT=5s", "TROVE_STORAGE_SPOOL_DIR="+filepath.Join(dir, "spool"),
		"TROVE_LOG_REQUEST_ENABLED=false",
		// The rotation tests poll /api/v1/system/ping anonymously from one address, far above
		// the default anonymous class (R-API-016); the limiter stays on with a budget the polling
		// never reaches. The limits themselves are tested in test/integration/api.
		"TROVE_API_RATE_LIMITS_ANONYMOUS_REQUESTS_PER_SECOND=1000", "TROVE_API_RATE_LIMITS_ANONYMOUS_BURST=10000",
		"TROVE_CONFIG_FILE="+write(t, dir, "trove.yaml", ""),
	)
	return &install{env: env, dir: dir}
}

// run executes a one-shot subcommand and fails the test on a non-zero exit.
func (in *install) run(t *testing.T, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = append(os.Environ(), in.env...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("trove %v: %v\n%s", args, err, out)
	}
}

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

// replica is one running `trove serve`.
type replica struct {
	client, ops string // host:port
	cmd         *exec.Cmd
	logs        *syncBuffer
	exited      chan error
}

// serve starts `trove serve --role role` with the extra settings and returns at once.
func (in *install) serve(t *testing.T, role string, extra ...string) *replica {
	t.Helper()
	r := &replica{client: freeAddr(t), ops: freeAddr(t), logs: &syncBuffer{}, exited: make(chan error, 1)}
	r.cmd = exec.Command(binary, "serve", "--role", role)
	r.cmd.Env = append(append(append([]string{"PATH=" + os.Getenv("PATH")}, in.env...),
		"TROVE_SERVER_LISTEN="+r.client, "TROVE_SERVER_OPS_LISTEN="+r.ops, "TROVE_POD_NAME=trove-tls-"+randHex(3)), extra...)
	r.cmd.Stdout, r.cmd.Stderr = r.logs, r.logs
	if err := r.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { r.exited <- r.cmd.Wait() }()
	t.Cleanup(func() { _ = r.cmd.Process.Kill() })
	return r
}

// running fails the test if the process has exited.
func (r *replica) running(t *testing.T) {
	t.Helper()
	select {
	case err := <-r.exited:
		r.exited <- err
		t.Fatalf("trove exited: %v\n%s", err, r.logs.String())
	default:
	}
}

// waitReady polls GET https://ops/readyz with c until it answers 200.
func (r *replica) waitReady(t *testing.T, c *http.Client, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		if code, _, _, err := fetch(c, "https://"+r.ops+"/readyz"); err == nil && code == 200 {
			return
		}
		r.running(t)
		if time.Now().After(deadline) {
			t.Fatalf("not ready within %s\n%s", within, r.logs.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// httpsClient trusts only ca. Without keepAlive every request is a new TCP
// connection and a new TLS handshake.
func httpsClient(ca *tlstest.CA, keepAlive bool) *http.Client {
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		DisableKeepAlives: !keepAlive, MaxConnsPerHost: 1,
		TLSClientConfig: &tls.Config{RootCAs: ca.Pool(), MinVersion: tls.VersionTLS12},
	}}
}

// fetch performs a GET and returns status, body, the served certificate's
// serial (0 without TLS) and the error.
func fetch(c *http.Client, url string) (int, string, int64, error) {
	resp, err := c.Get(url)
	if err != nil {
		return 0, "", 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, "", 0, err
	}
	var serial int64
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		serial = resp.TLS.PeerCertificates[0].SerialNumber.Int64()
	}
	return resp.StatusCode, string(b), serial, nil
}

// metricLine returns the first exposition line that starts with prefix.
func metricLine(body, prefix string) string {
	for _, l := range strings.Split(body, "\n") {
		if strings.HasPrefix(l, prefix) {
			return l
		}
	}
	return ""
}

func certificatesEnv(key, certFile, keyFile string) string {
	return fmt.Sprintf(`%s=[{"cert_file":%q,"key_file":%q}]`, key, certFile, keyFile)
}

// seriesValue reads the series of a TLS family whose name label is name.
func seriesValue(t *testing.T, reg *prometheus.Registry, family, name string) float64 {
	t.Helper()
	fams, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fams {
		if f.GetName() != family {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "name" && l.GetValue() == name {
					return m.GetCounter().GetValue() + m.GetGauge().GetValue()
				}
			}
		}
	}
	t.Fatalf("no %s series for %s", family, name)
	return 0
}
