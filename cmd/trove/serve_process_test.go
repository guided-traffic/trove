package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// buildFixture compiles the binary with the drain fixture (R-ARCH-024's
// acceptance criteria use test builds): the HTTP shell runs without PostgreSQL
// and Valkey, and /test-fixture/hold holds a request open.
func buildFixture(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "trove")
	cmd := exec.Command("go", "build", "-tags", "drainfixture", "-o", bin, ".")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

type proc struct {
	cmd         *exec.Cmd
	out         *bytes.Buffer
	client, ops string
	exited      chan error
}

func startProc(t *testing.T, bin, role string, extra ...string) *proc {
	t.Helper()
	dir := t.TempDir()
	key := filepath.Join(dir, "2026-10.key")
	if err := os.WriteFile(key, []byte(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "trove.yaml")
	if err := os.WriteFile(cfg, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	p := &proc{client: freeAddr(t), ops: freeAddr(t), out: &bytes.Buffer{}, exited: make(chan error, 1)}
	p.cmd = exec.Command(bin, "serve", "--role", role, "--config", cfg)
	p.cmd.Env = append([]string{
		"PATH=" + os.Getenv("PATH"),
		"TROVE_SERVER_LISTEN=" + p.client, "TROVE_SERVER_OPS_LISTEN=" + p.ops,
		"TROVE_HEALTH_SHUTDOWN_DRAIN_DELAY=700ms", "TROVE_SERVER_SHUTDOWN_TIMEOUT=10s",
		"TROVE_HEALTH_CHECK_INTERVAL=200ms", "TROVE_HEALTH_CHECK_TIMEOUT=100ms",
		"TROVE_DATABASE_HOST=db.example", "TROVE_DATABASE_NAME=trove",
		"TROVE_VALKEY_SENTINEL_ADDRESSES=[\"s1:26379\"]", "TROVE_VALKEY_SENTINEL_PRIMARY_NAME=trove",
		"TROVE_STORAGE_S3_BUCKET=blobs", "TROVE_SECRETS_ENCRYPTION_KEY_FILES=[\"" + key + "\"]", "TROVE_SECRETS_ACTIVE_KEY_ID=2026-10",
		"TROVE_POD_NAME=trove-" + role + "-test",
	}, extra...)
	p.cmd.Stdout, p.cmd.Stderr = p.out, p.out
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { p.exited <- p.cmd.Wait() }()
	t.Cleanup(func() { _ = p.cmd.Process.Kill() })
	for end := time.Now().Add(15 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		resp, err := http.Get("http://" + p.ops + "/readyz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return p
			}
		}
		if time.Now().After(end) {
			t.Fatalf("not ready\n%s", p.out.String())
		}
	}
}

func status(addr, path string) int {
	c := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 2 * time.Second}
	resp, err := c.Get("http://" + addr + path)
	if err != nil {
		return -1
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestServeProcess(t *testing.T) {
	bin := buildFixture(t)

	t.Run("R-ARCH-024 R-OPS-002 SIGTERM with a request in flight: readiness flips first, ping keeps answering 200 in the drain delay, the request finishes, the process exits 0", func(t *testing.T) {
		p := startProc(t, bin, "web")
		if status(p.client, "/api/v1/system/ping") != 200 {
			t.Fatal("ping is not 200 on a ready replica")
		}
		// An in-flight request: the fixture holds it for 2 s.
		conn, err := net.Dial("tcp", p.client)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		fmt.Fprint(conn, "GET /test-fixture/hold?for=2s HTTP/1.1\r\nHost: x\r\n\r\n")
		r := bufio.NewReader(conn)
		resp, err := http.ReadResponse(r, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		line, _ := bufio.NewReader(resp.Body).ReadString('\n')
		if line != "started\n" {
			t.Fatalf("first line %q", line)
		}

		if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		// readiness 503 first, liveness 200, while the listener still serves.
		flipped := false
		for end := time.Now().Add(500 * time.Millisecond); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
			if status(p.ops, "/readyz") == 503 {
				flipped = true
				break
			}
		}
		if !flipped {
			t.Fatalf("readyz did not turn 503\n%s", p.out.String())
		}
		if status(p.ops, "/healthz") != 200 {
			t.Fatal("healthz must stay 200 while draining")
		}
		// ping follows "serves", not readiness: a request that the edge still routes
		// here during the drain delay must be answered (R-OPS-002).
		for range 5 {
			if got := status(p.client, "/api/v1/system/ping"); got != 200 {
				t.Fatalf("ping during the drain delay = %d, want 200", got)
			}
			time.Sleep(20 * time.Millisecond)
		}
		// After the drain delay the listener is closed.
		refused := false
		for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(25 * time.Millisecond) {
			if status(p.client, "/api/v1/system/ping") == -1 {
				refused = true
				break
			}
		}
		if !refused {
			t.Fatal("the client listener accepts connections after the drain delay")
		}
		// The in-flight request completes.
		rest, err := io.ReadAll(resp.Body)
		if err != nil || !strings.Contains(string(rest), "finished") {
			t.Fatalf("in-flight request: %q %v", rest, err)
		}
		select {
		case err := <-p.exited:
			if err != nil {
				t.Fatalf("exit: %v\n%s", err, p.out.String())
			}
		case <-time.After(10 * time.Second):
			t.Fatal("process did not exit")
		}
	})

	t.Run("R-HA-011 the derived shutdown timeout bounds the drain: a request that never finishes is cut and the process exits before the grace period", func(t *testing.T) {
		// grace 8 s, drain 0.7 s -> server.shutdown_timeout 2.3 s (grace - drain - 5 s).
		p := startProc(t, bin, "web", "TROVE_RUNTIME_TERMINATION_GRACE_PERIOD=8s", "TROVE_SERVER_SHUTDOWN_TIMEOUT=0s")
		conn, err := net.Dial("tcp", p.client)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		fmt.Fprint(conn, "GET /test-fixture/hold?for=1h HTTP/1.1\r\nHost: x\r\n\r\n")
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if line, _ := bufio.NewReader(resp.Body).ReadString('\n'); line != "started\n" {
			t.Fatalf("first line %q", line)
		}
		begin := time.Now()
		if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-p.exited:
			took := time.Since(begin)
			if err != nil {
				t.Fatalf("exit: %v\n%s", err, p.out.String())
			}
			// drain 0.7 s + timeout 2.3 s = 3.0 s; the cut request leaves nothing to wait for after that.
			if took < 2800*time.Millisecond {
				t.Errorf("exited after %s: the request was cut before the derived timeout of 3.0 s", took)
			}
			if took >= 8*time.Second {
				t.Errorf("exited after %s, not before the grace period of 8 s", took)
			}
		case <-time.After(12 * time.Second):
			t.Fatalf("process did not exit\n%s", p.out.String())
		}
	})

	t.Run("R-ARCH-001 --role worker opens no client socket and still serves the ops listener", func(t *testing.T) {
		p := startProc(t, bin, "worker")
		if c, err := net.DialTimeout("tcp", p.client, 300*time.Millisecond); err == nil {
			c.Close()
			t.Fatal("a worker listens on the client address")
		}
		if status(p.ops, "/healthz") != 200 {
			t.Fatal("worker healthz")
		}
		_ = p.cmd.Process.Signal(syscall.SIGINT)
		if err := <-p.exited; err != nil {
			t.Fatalf("exit after SIGINT: %v\n%s", err, p.out.String())
		}
	})
}
