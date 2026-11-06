//go:build integration

package overload

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

const repo = "generic-local"

// filesRoot is the file-access prefix of the repository, which lives in the default project.
const filesRoot = "/files/" + testenv.DefaultProject + "/" + repo + "/"

// install is one installation with a generic repository and a token that may
// read and deploy it.
type install struct {
	stack *testenv.Stack
	token string
}

func newInstall(t *testing.T) *install {
	t.Helper()
	s := testenv.NewStack(t, testenv.StackOptions{Valkey: true, Name: "ovl"})
	s.Repository(repo, "local", "generic", "{}")
	u := s.User("loader")
	s.Policy("loader", []string{"read", "deploy"}, []string{repo}, u)
	return &install{stack: s, token: s.Token(u, scope.Scope{Inherit: true})}
}

// replica is one running `trove serve --role web`.
type replica struct {
	client, ops string
	cmd         *exec.Cmd
	logs        *testenv.SyncBuffer
	exited      chan error
}

// start launches the binary and waits until /readyz answers 200.
func (in *install) start(t *testing.T, name string, extra ...string) *replica {
	t.Helper()
	r := &replica{client: testenv.FreeAddr(t), ops: testenv.FreeAddr(t), logs: &testenv.SyncBuffer{}, exited: make(chan error, 1)}
	r.cmd = exec.Command(binary, "serve", "--role", "web")
	r.cmd.Env = append(append(append([]string{"PATH=" + os.Getenv("PATH")}, in.stack.Environ()...),
		"TROVE_SERVER_LISTEN="+r.client, "TROVE_SERVER_OPS_LISTEN="+r.ops, "TROVE_POD_NAME="+name), extra...)
	r.cmd.Stdout, r.cmd.Stderr = r.logs, r.logs
	if err := r.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { r.exited <- r.cmd.Wait() }()
	t.Cleanup(func() {
		_ = r.cmd.Process.Kill()
		if t.Failed() {
			t.Logf("log of %s:\n%s", name, testenv.Tail(r.logs.String(), 40))
		}
	})
	for end := time.Now().Add(60 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		if code, _ := get("http://"+r.ops+"/readyz", ""); code == http.StatusOK {
			return r
		}
		select {
		case err := <-r.exited:
			t.Fatalf("%s exited before it was ready: %v\n%s", name, err, testenv.Tail(r.logs.String(), 40))
		default:
		}
		if time.Now().After(end) {
			t.Fatalf("%s not ready\n%s", name, testenv.Tail(r.logs.String(), 40))
		}
	}
}

// alive reports whether the process is still running.
func (r *replica) alive() bool {
	select {
	case err := <-r.exited:
		r.exited <- err
		return false
	default:
		return true
	}
}

func get(url, token string) (int, string) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return -1, err.Error()
	}
	req.Host = testenv.DefaultHost
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := testenv.Client.Do(req)
	if err != nil {
		return -1, err.Error()
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

// metric reads one series of the replica's /metrics; -1 if it is absent.
func (r *replica) metric(t *testing.T, series string) float64 {
	t.Helper()
	code, body := get("http://"+r.ops+"/metrics", "")
	if code != http.StatusOK {
		t.Fatalf("metrics: %d %s", code, body)
	}
	return testenv.MetricValue(body, series)
}

// deploy uploads size random bytes to path and returns their SHA-256.
func (in *install) deploy(t *testing.T, r *replica, path string, size int) [32]byte {
	t.Helper()
	data := make([]byte, size)
	_, _ = rand.Read(data)
	a := in.stack.DoAt(r.client, testenv.DefaultHost, http.MethodPut, filesRoot+path, in.token, data,
		"Content-Type", "application/octet-stream")
	if a.Status != http.StatusCreated {
		t.Fatalf("deploy %s: %d %s", path, a.Status, a.Body)
	}
	return sha256.Sum256(data)
}

// slow is one download whose client reads nothing until it is released: the
// replica's handler blocks in its write once the socket buffers are full, so it
// holds whatever a download holds for as long as the test wants.
type slow struct {
	conn net.Conn
}

// slowGet sends a GET of path over a connection with a 4 KiB receive buffer and
// reads nothing.
func slowGet(t *testing.T, addr, path, token string) *slow {
	t.Helper()
	d := net.Dialer{Timeout: 5 * time.Second, Control: func(_, _ string, c syscall.RawConn) error {
		var serr error
		if err := c.Control(func(fd uintptr) { serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, 4096) }); err != nil {
			return err
		}
		return serr
	}}
	c, err := d.DialContext(context.Background(), "tcp", addr)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if _, err := fmt.Fprintf(c, "GET %s HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\nConnection: close\r\n\r\n", path, testenv.DefaultHost, token); err != nil {
		t.Fatalf("send %s: %v", path, err)
	}
	return &slow{conn: c}
}

// result is what a released download received.
type result struct {
	status     int
	retryAfter string
	sum        [32]byte
	n          int64
	err        error
}

// finish reads the whole response.
func (s *slow) finish() result {
	_ = s.conn.SetReadDeadline(time.Now().Add(2 * time.Minute))
	res, err := http.ReadResponse(bufio.NewReaderSize(s.conn, 64<<10), nil)
	if err != nil {
		return result{err: err}
	}
	defer res.Body.Close()
	h := sha256.New()
	n, err := io.Copy(h, res.Body)
	out := result{status: res.StatusCode, retryAfter: res.Header.Get("Retry-After"), n: n, err: err}
	h.Sum(out.sum[:0])
	return out
}
