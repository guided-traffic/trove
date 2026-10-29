//go:build integration

package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/valkey-io/valkey-go"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// install is one trove installation on the stack: its own database, its own
// Valkey key prefix and key ring files.
type install struct {
	env  []string // TROVE_* settings shared by every replica
	db   string
	conn *pgx.Conn // admin connection to the installation's database
}

func write(t testing.TB, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
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

// newInstall creates a fresh database and the settings that point a replica at the stack.
func newInstall(t *testing.T) *install {
	t.Helper()
	host, port := testenv.Get(t, "TROVE_TEST_PG_HOST"), testenv.Get(t, "TROVE_TEST_PG_PORT")
	user, pass := testenv.Get(t, "TROVE_TEST_PG_USER"), testenv.Get(t, "TROVE_TEST_PG_PASSWORD")
	adminDSN := "postgres://" + user + ":" + pass + "@" + host + ":" + port + "/postgres?sslmode=disable"
	ctx := context.Background()
	maint, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Fatalf("connect to PostgreSQL: %v", err)
	}
	var b [5]byte
	_, _ = rand.Read(b[:])
	name := "trove_srv_" + hex.EncodeToString(b[:])
	if _, err := maint.Exec(ctx, "CREATE DATABASE "+name+" ENCODING 'UTF8' TEMPLATE template0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = maint.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		_ = maint.Close(context.Background())
	})
	conn, err := pgx.Connect(ctx, "postgres://"+user+":"+pass+"@"+host+":"+port+"/"+name+"?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })

	vk, s3 := testenv.ValkeyConfig(t), testenv.S3Config(t)
	dir := t.TempDir()
	var sentinels []string
	for _, s := range vk.Sentinels {
		sentinels = append(sentinels, strconv.Quote(s))
	}
	key := write(t, dir, "2026-10.key", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{42}, 32)))
	env := []string{
		"TROVE_DATABASE_HOST=" + host, "TROVE_DATABASE_PORT=" + port, "TROVE_DATABASE_NAME=" + name,
		"TROVE_DATABASE_USERNAME_FILE=" + write(t, dir, "db-user", user+"\n"), "TROVE_DATABASE_PASSWORD_FILE=" + write(t, dir, "db-pass", pass+"\n"),
		"TROVE_DATABASE_SSL_MODE=disable", "TROVE_DATABASE_ALLOW_INSECURE=true", "TROVE_DATABASE_PREFLIGHT_ALLOW_SUPERUSER=true",
		"TROVE_DATABASE_RETRY_MAX_ELAPSED=3s",
		"TROVE_VALKEY_SENTINEL_ADDRESSES=[" + strings.Join(sentinels, ",") + "]", "TROVE_VALKEY_SENTINEL_PRIMARY_NAME=" + vk.PrimaryName,
		"TROVE_VALKEY_SENTINEL_PASSWORD_FILE=" + write(t, dir, "vk-sentinel-pass", vk.SentinelPassword),
		"TROVE_VALKEY_USERNAME_FILE=" + write(t, dir, "vk-user", vk.Username), "TROVE_VALKEY_PASSWORD_FILE=" + write(t, dir, "vk-pass", vk.Password),
		"TROVE_VALKEY_TLS_CA_FILE=" + vk.CAFile, "TROVE_VALKEY_KEY_PREFIX=" + vk.KeyPrefix,
		"TROVE_STORAGE_S3_BUCKET=" + s3.BlobsBucket, "TROVE_STORAGE_S3_ENDPOINT=" + s3.Endpoint, "TROVE_STORAGE_S3_REGION=" + s3.Region,
		"TROVE_STORAGE_S3_FORCE_PATH_STYLE=true", "TROVE_STORAGE_S3_ALLOW_INSECURE_ENDPOINT=true",
		"TROVE_STORAGE_S3_CREDENTIALS_SOURCE=static",
		"TROVE_STORAGE_S3_CREDENTIALS_ACCESS_KEY_ID_FILE=" + write(t, dir, "s3-id", s3.AccessKey),
		"TROVE_STORAGE_S3_CREDENTIALS_SECRET_ACCESS_KEY_FILE=" + write(t, dir, "s3-secret", s3.SecretKey),
		"TROVE_SECRETS_ENCRYPTION_KEY_FILES=[" + strconv.Quote(key) + "]", "TROVE_SECRETS_ACTIVE_KEY_ID=2026-10",
		"TROVE_SERVER_TRUSTED_PROXIES=[\"127.0.0.0/8\"]", "TROVE_SERVER_BASE_URL=http://trove.test",
		"TROVE_SYSTEM_BOOTSTRAP_ANONYMOUS_READ=true",
		"TROVE_HEALTH_CHECK_INTERVAL=300ms", "TROVE_HEALTH_CHECK_TIMEOUT=200ms", "TROVE_HEALTH_SHUTDOWN_DRAIN_DELAY=1500ms",
		"TROVE_SERVER_SHUTDOWN_TIMEOUT=10s", "TROVE_STORAGE_SPOOL_DIR=" + filepath.Join(dir, "spool"),
		"TROVE_SYSTEM_HEARTBEAT_INTERVAL=1s", "TROVE_SYSTEM_HEARTBEAT_STALE_AFTER=3s",
		"TROVE_JOBS_SCHEDULE_TICK=1s", "TROVE_JOBS_POLL_INTERVAL=1s", "TROVE_CONFIG_FILE=" + write(t, dir, "trove.yaml", ""),
	}
	return &install{env: env, db: name, conn: conn}
}

// run executes a one-shot subcommand and returns its exit code and output.
func (in *install) run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = append(os.Environ(), in.env...)
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok { //nolint:errorlint // exec returns it unwrapped
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return code, o.String(), e.String()
}

// syncBuffer is a log sink that is safe to read while the process writes.
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
	name        string
	role        string
	client, ops string
	cmd         *exec.Cmd
	logs        *syncBuffer
	exited      chan error
}

// start launches a replica and waits until /readyz answers 200.
func (in *install) start(t *testing.T, role, name string, extra ...string) *replica {
	t.Helper()
	r := &replica{name: name, role: role, client: freeAddr(t), ops: freeAddr(t), logs: &syncBuffer{}, exited: make(chan error, 1)}
	r.cmd = exec.Command(binary, "serve", "--role", role)
	r.cmd.Env = append(append(append([]string{"PATH=" + os.Getenv("PATH")}, in.env...),
		"TROVE_SERVER_LISTEN="+r.client, "TROVE_SERVER_OPS_LISTEN="+r.ops, "TROVE_POD_NAME="+name), extra...)
	r.cmd.Stdout, r.cmd.Stderr = r.logs, r.logs
	if err := r.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { r.exited <- r.cmd.Wait() }()
	t.Cleanup(func() { _ = r.cmd.Process.Kill() })
	for end := time.Now().Add(30 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		if code, _ := opsGet(r.ops, "/readyz"); code == 200 {
			return r
		}
		select {
		case err := <-r.exited:
			t.Fatalf("%s exited before it was ready: %v\n%s", name, err, r.logs.String())
		default:
		}
		if time.Now().After(end) {
			t.Fatalf("%s not ready\n%s", name, r.logs.String())
		}
	}
}

var noKeepAlive = &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 5 * time.Second}

func opsGet(addr, path string) (int, string) {
	resp, err := noKeepAlive.Get("http://" + addr + path)
	if err != nil {
		return -1, err.Error()
	}
	defer resp.Body.Close()
	var b bytes.Buffer
	_, _ = b.ReadFrom(resp.Body)
	return resp.StatusCode, b.String()
}

func (r *replica) get(t testing.TB, path string, hdr ...string) (int, http.Header, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", "http://"+r.client+path, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := noKeepAlive.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	var b bytes.Buffer
	_, _ = b.ReadFrom(resp.Body)
	return resp.StatusCode, resp.Header, b.String()
}

// term sends SIGTERM and waits for the exit.
func (r *replica) term(t *testing.T) error {
	t.Helper()
	if err := r.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-r.exited:
		return err
	case <-time.After(30 * time.Second):
		t.Fatalf("%s did not exit\n%s", r.name, r.logs.String())
		return nil
	}
}

// valkeyAdmin connects to the primary data node directly as the admin user.
func valkeyAdmin(t *testing.T) valkey.Client {
	t.Helper()
	vk := testenv.ValkeyConfig(t)
	pem, err := os.ReadFile(vk.CAFile)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem)
	c, err := valkey.NewClient(valkey.ClientOption{
		InitAddress: []string{vk.Nodes[0]}, Username: vk.AdminUsername, Password: vk.AdminPassword,
		TLSConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, DisableCache: true, ForceSingleClient: true,
	})
	if err != nil {
		t.Fatalf("connect to Valkey: %v", err)
	}
	t.Cleanup(c.Close)
	return c
}

// startRaw launches a replica without waiting for readiness.
func startRaw(t *testing.T, in *install, r *replica) *exec.Cmd {
	t.Helper()
	r.cmd = exec.Command(binary, "serve", "--role", r.role)
	r.cmd.Env = append(append([]string{"PATH=" + os.Getenv("PATH")}, in.env...),
		"TROVE_SERVER_LISTEN="+r.client, "TROVE_SERVER_OPS_LISTEN="+r.ops, "TROVE_POD_NAME="+r.name)
	r.cmd.Stdout, r.cmd.Stderr = r.logs, r.logs
	if err := r.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { r.exited <- r.cmd.Wait() }()
	t.Cleanup(func() { _ = r.cmd.Process.Kill() })
	return r.cmd
}
