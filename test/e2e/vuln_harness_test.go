//go:build e2e

package e2e

// The setup of the vulnerability tests survives a broken port-forward (R-VULN-029, docs/developer/e2e.md "The control
// clients and a broken forward"): a call that is safe to repeat goes to a new forward after a transport error, and a
// publish to a test upstream whose answer was lost is accepted on the 409 of its repeat only when the stub lists exactly
// what was sent. The test breaks forwards of its own, so the clients the other tests share are not touched: it kills
// the forward's kubectl process after the connection to it was accepted (the request then ends on the transport, as
// when the forward's stream breaks), or after the stub answered (the answer is lost with the process).

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestVuln_SetupSurvivesABrokenForward(t *testing.T) {
	npmHost := os.Getenv("TROVE_E2E_NPMSTUB_HOST")
	if npmHost == "" {
		t.Fatal("TROVE_E2E_NPMSTUB_HOST is not set; run the vulnerability sets of deploy/dev/e2e.sh")
	}
	nugetHost, _ := nugetStubHosts(t)
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	run := hex.EncodeToString(b)
	npmControl := func(t *testing.T) *stubControl {
		c := newStubControl(t, "svc/npmstub", npmHost, "trove-npmstub", 30*time.Second)
		t.Cleanup(func() { stopForward(c.fwd) })
		return c
	}

	t.Run("a control call whose forward ends under it goes to a new forward", func(t *testing.T) {
		c := npmControl(t)
		dials := breakFirstConnection(t, c.c, c.fwd)
		c.send(t, http.MethodGet, "/-/stub/counts", nil)
		if n := dials.Load(); n != 2 {
			t.Fatalf("%d connections, want 2: the call that broke and its repeat on the new forward", n)
		}
	})

	t.Run("a publish whose forward ends before it was sent is repeated and published", func(t *testing.T) {
		c := npmControl(t)
		dials := breakFirstConnection(t, c.c, c.fwd)
		name := "e2e-fwd-" + run + "-sent"
		c.publish(t, name, "1.0.0", nil)
		if n := dials.Load(); n != 2 {
			t.Fatalf("%d connections, want 2", n)
		}
		requireListed(t, c, name, "1.0.0", nil, nil)
	})

	t.Run("a publish whose answer is lost is accepted on the 409 of its repeat when the stub lists what was sent", func(t *testing.T) {
		c := npmControl(t)
		lost := loseFirstAnswer(t, c.c, c.fwd)
		name := "e2e-fwd-" + run + "-lost"
		deps := map[string]string{"e2e-fwd-dep": "^1.0.0"}
		c.publish(t, name, "1.0.0", deps, "canary")
		if got := lost.answers(); fmt.Sprint(got) != "[204 409 200]" {
			t.Fatalf("answers %v, want the lost 204 of the publish, the 409 of its repeat and the 200 of the read-back", got)
		}
		requireListed(t, c, name, "1.0.0", deps, []string{"canary"})
	})

	t.Run("a 409 of the repeat fails when the stub lists other content", func(t *testing.T) {
		c := npmControl(t)
		name := "e2e-fwd-" + run + "-other"
		c.publish(t, name, "1.0.0", map[string]string{"e2e-fwd-dep": "^1.0.0"})
		loseFirstAnswer(t, c.c, c.fwd)
		other := map[string]string{"e2e-fwd-dep": "^2.0.0"}
		err := c.tryPublish(t, name, "1.0.0", npmPublishBody(name, "1.0.0", other, nil), npmPublished(name, "1.0.0", other, nil))
		if err == nil || !strings.Contains(err.Error(), "does not list the version that was sent") || !strings.Contains(err.Error(), "dependencies") {
			t.Fatalf("a repeat that met another version of the name: %v", err)
		}
	})

	t.Run("a 409 without a transport error fails", func(t *testing.T) {
		c := npmControl(t)
		name := "e2e-fwd-" + run + "-twice"
		c.publish(t, name, "1.0.0", nil)
		err := c.tryPublish(t, name, "1.0.0", npmPublishBody(name, "1.0.0", nil, nil), npmPublished(name, "1.0.0", nil, nil))
		if err == nil || !strings.Contains(err.Error(), "409") {
			t.Fatalf("a second publish of the version: %v", err)
		}
	})

	t.Run("a NuGet publish whose answer is lost is accepted on the 409 of its repeat", func(t *testing.T) {
		c := newStubControl(t, "svc/nugetstub", nugetHost, "trove-nugetstub", 30*time.Second)
		t.Cleanup(func() { stopForward(c.fwd) })
		lost := loseFirstAnswer(t, c.c, c.fwd)
		id := "E2e.Fwd" + run + ".Lost"
		c.nugetPublish(t, id, "1.0.0")
		if got := lost.answers(); fmt.Sprint(got) != "[204 409 200]" {
			t.Fatalf("answers %v, want [204 409 200]", got)
		}
	})

	t.Run("a GET through a pod's forward that ends under it goes to a new forward", func(t *testing.T) {
		addrs, stop := packagesForwards(t)
		defer stop()
		a := apiAtPod(t, addrs[0], "")
		dials := breakFirstConnection(t, a.c, forwardAt(addrs[0]))
		r := a.getOverForward(npmBase + npmLocalKey + "/-/ping")
		if r.Status == 0 {
			t.Fatal("no answer")
		}
		if n := dials.Load(); n != 2 {
			t.Fatalf("%d connections, want 2", n)
		}
	})
}

// requireListed fails t unless the npm test upstream lists the version as a publish of these dependencies and tags makes it.
func requireListed(t *testing.T, c *stubControl, name, version string, deps map[string]string, tags []string) {
	t.Helper()
	q := "/-/stub/version?" + url.Values{"name": {name}, "version": {version}}.Encode()
	if err := npmPublished(name, version, deps, tags)(c.send(t, http.MethodGet, q, nil)); err != nil {
		t.Fatalf("%s@%s: %v", name, version, err)
	}
}

// breakFirstConnection lets the first connection of the client reach its forward and then kills the forward's kubectl
// process, so the request on that connection ends on the transport. It counts the connections the client opened.
func breakFirstConnection(t *testing.T, c *http.Client, f *forward) *atomic.Int32 {
	t.Helper()
	if f == nil {
		t.Fatal("the client goes through no supervised forward")
	}
	tr := c.Transport.(*http.Transport).Clone()
	inner := tr.DialContext
	var n atomic.Int32
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := inner(ctx, network, addr)
		if err != nil || n.Add(1) != 1 {
			return conn, err
		}
		if err := killForwardProcess(f); err != nil {
			_ = conn.Close()
			return nil, err
		}
		return conn, nil
	}
	c.Transport = tr
	return &n
}

// lostAnswer is a client whose first answer is read, dropped and replaced by the end of stream of a forward whose kubectl
// process was killed meanwhile: the request took effect, the client does not know it.
type lostAnswer struct {
	rt http.RoundTripper
	f  *forward

	mu     sync.Mutex
	status []int // every answer the stub gave, the lost one first
}

func loseFirstAnswer(t *testing.T, c *http.Client, f *forward) *lostAnswer {
	t.Helper()
	l := &lostAnswer{rt: c.Transport, f: f}
	c.Transport = l
	return l
}

func (l *lostAnswer) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := l.rt.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	l.status = append(l.status, resp.StatusCode)
	first := len(l.status) == 1
	l.mu.Unlock()
	if !first {
		return resp, nil
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if err := killForwardProcess(l.f); err != nil {
		return nil, err
	}
	return nil, io.EOF
}

func (l *lostAnswer) answers() []int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]int(nil), l.status...)
}

// killForwardProcess ends the kubectl process the forward runs now, as a broken stream ends it, and returns once the
// process is gone (so a connection it had accepted is closed); the supervisor starts the next one on the same port.
func killForwardProcess(f *forward) error {
	f.mu.Lock()
	cmd := f.cmd
	f.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return errors.New("the forward runs no kubectl process")
	}
	_ = cmd.Process.Kill()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if errors.Is(syscall.Kill(cmd.Process.Pid, 0), syscall.ESRCH) {
			return nil
		}
	}
	return fmt.Errorf("the kubectl process %d of the forward to %s did not end within 10 s", cmd.Process.Pid, f.target)
}
