//go:build integration

package npm

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// race sends the requests at once, each to its replica, and returns their statuses.
func race(e *env, reqs []func() int) []int {
	out := make([]int, len(reqs))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, r := range reqs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			out[i] = r()
		}()
	}
	close(start)
	wg.Wait()
	return out
}

func publisher(e *env, r *testenv.Replica, token string, p pkg) func() int {
	b := body(e.t, p)
	return func() int { return tryPublish(r, "npm-local", token, p.Name, p.Version, b) }
}

// TestR_NPM_007_ConcurrentPublishes: two replicas publish at once. Different versions of
// one package both succeed (they serialize on the package row), two first publishes of
// one new name both succeed, and two first publishes of look-alike names end in one 201
// and one 400 npm_invalid_name, never in a 5xx.
func TestR_NPM_007_ConcurrentPublishes(t *testing.T) {
	e := newEnv(t)
	a, b := e.replica("web-a"), e.replica("web-b")
	t.Run("R-NPM-007 1.0.1 and 1.0.2 on two replicas", func(t *testing.T) {
		for round := range 5 {
			name := fmt.Sprintf("versions-%d", round)
			e.mustPublish(a, "npm-local", e.tokA, pkg{Name: name, Version: "1.0.0"})
			got := race(e, []func() int{
				publisher(e, a, e.tokA, pkg{Name: name, Version: "1.0.1"}),
				publisher(e, b, e.tokA, pkg{Name: name, Version: "1.0.2"}),
			})
			if got[0] != 201 || got[1] != 201 {
				t.Fatalf("%s: statuses %v", name, got)
			}
			if e.versions("npm-local", name) != 3 || e.revSeq("npm-local", name) != 3 {
				t.Fatalf("%s: versions %d rev_seq %d", name, e.versions("npm-local", name), e.revSeq("npm-local", name))
			}
			if tags := e.tags("npm-local", name); tags["latest"] != "1.0.2" && tags["latest"] != "1.0.1" {
				t.Fatalf("%s: tags %v", name, tags)
			}
		}
	})
	t.Run("R-NPM-007 two first publishes of one new name", func(t *testing.T) {
		for round := range 5 {
			name := fmt.Sprintf("newname-%d", round)
			got := race(e, []func() int{
				publisher(e, a, e.tokA, pkg{Name: name, Version: "1.0.0"}),
				publisher(e, b, e.tokA, pkg{Name: name, Version: "1.0.1"}),
			})
			if got[0] != 201 || got[1] != 201 || e.versions("npm-local", name) != 2 {
				t.Fatalf("%s: statuses %v versions %d", name, got, e.versions("npm-local", name))
			}
			if e.events("authz.ownership.claimed", name, "") != 1 {
				t.Fatalf("%s: the name was claimed %d times", name, e.events("authz.ownership.claimed", name, ""))
			}
		}
	})
	t.Run("R-NPM-007 acme-ui and acme_ui on two replicas", func(t *testing.T) {
		for round := range 8 {
			dash, under := fmt.Sprintf("acme-ui-%d", round), fmt.Sprintf("acme_ui_%d", round)
			got := race(e, []func() int{
				publisher(e, a, e.tokA, pkg{Name: dash}),
				publisher(e, b, e.tokA, pkg{Name: under}),
			})
			created, refused := 0, 0
			for _, s := range got {
				switch s {
				case 201:
					created++
				case 400:
					refused++
				}
			}
			if created != 1 || refused != 1 {
				t.Fatalf("round %d: statuses %v", round, got)
			}
			// the loser's transaction rolled back: no package row, no ownership
			if e.versions("npm-local", dash)+e.versions("npm-local", under) != 1 ||
				e.revSeq("npm-local", dash)+e.revSeq("npm-local", under) != 1 {
				t.Fatalf("round %d: the loser left rows", round)
			}
			loser := under
			if e.versions("npm-local", dash) == 0 {
				loser = dash
			}
			if e.owners("npm-local", loser) != "" {
				t.Fatalf("round %d: the loser %s is owned", round, loser)
			}
		}
	})
}

// resetProxy forwards connections to target and, for the first request, forwards
// the response to nowhere: it closes the client's connection with a reset as soon as
// the replica has answered, like an edge that loses the answer.
type resetProxy struct {
	ln    net.Listener
	reset sync.Once
}

func newResetProxy(t *testing.T, target string) *resetProxy {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &resetProxy{ln: ln}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go p.serve(c, target)
		}
	}()
	return p
}

func (p *resetProxy) serve(c net.Conn, target string) {
	up, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", target)
	if err != nil {
		_ = c.Close()
		return
	}
	go func() { _, _ = io.Copy(up, c) }()
	buf := make([]byte, 1)
	if _, err := up.Read(buf); err != nil { // the first byte of the response: the commit is done
		_ = c.Close()
		_ = up.Close()
		return
	}
	dropped := false
	p.reset.Do(func() {
		dropped = true
		if tc, ok := c.(*net.TCPConn); ok {
			_ = tc.SetLinger(0)
		}
		_ = c.Close()
		_ = up.Close()
	})
	if dropped {
		return
	}
	_, _ = c.Write(buf)
	_, _ = io.Copy(c, up)
	_ = c.Close()
}

// TestR_NPM_007_RetryAfterAnEdgeReset: the edge resets the client's connection of a
// committed publish; the client retries and the retry reaches another replica, which
// acknowledges it: one version, rev_seq grown by one, one npm.publish event of each
// outcome. The same request with another tarball is 409.
func TestR_NPM_007_RetryAfterAnEdgeReset(t *testing.T) {
	e := newEnv(t)
	a, b := e.replica("web-a"), e.replica("web-b")
	e.mustPublish(a, "npm-local", e.tokA, pkg{Name: "@acme/ui", Version: "1.0.2"})
	rev := e.revSeq("npm-local", "@acme/ui")
	px := newResetProxy(t, a.Addr("default"))
	p := pkg{Name: "@acme/ui", Version: "1.0.3"}
	buf := body(t, p)
	_, err := rawPutTo(px.ln.Addr().String(), pubPath("npm-local", p.Name), e.tokA, bytes.NewReader(buf), int64(len(buf)), false)
	if err == nil {
		t.Fatal("the connection was not reset")
	}
	// the publish committed on web-a although its answer never arrived
	if e.versions("npm-local", "@acme/ui") != 2 {
		t.Fatalf("versions %d", e.versions("npm-local", "@acme/ui"))
	}
	ans := e.publish(b, "npm-local", e.tokA, p)
	if ans.Status != 201 || !contains(ans.Header.Get("npm-notice"), "@acme/ui@1.0.3 was already published by you") {
		t.Fatalf("%d %s notice %q", ans.Status, ans.Body, ans.Header.Get("npm-notice"))
	}
	if e.versions("npm-local", "@acme/ui") != 2 || e.revSeq("npm-local", "@acme/ui") != rev+1 {
		t.Fatalf("versions %d rev_seq %d, want rev %d", e.versions("npm-local", "@acme/ui"), e.revSeq("npm-local", "@acme/ui"), rev+1)
	}
	if e.events("npm.publish", "@acme/ui", "") != 3 || e.events("npm.publish", "@acme/ui", "retry_acknowledged") != 1 {
		t.Fatalf("events %d, acknowledged %d", e.events("npm.publish", "@acme/ui", ""), e.events("npm.publish", "@acme/ui", "retry_acknowledged"))
	}
	other := e.publish(b, "npm-local", e.tokA, pkg{Name: "@acme/ui", Version: "1.0.3", Files: map[string]string{"x": "y"}})
	if other.Status != 409 || code(t, other) != "npm_version_exists" {
		t.Fatalf("%d %s", other.Status, other.Body)
	}
}

// TestR_NPM_007_ARetryThatOvertakesItsSlowOriginal: the retry reaches another replica
// while the original is still in its commit; both end in 201.
func TestR_NPM_007_TheSamePublishTwiceAtOnce(t *testing.T) {
	e := newEnv(t)
	a, b := e.replica("web-a"), e.replica("web-b")
	for round := range 6 {
		p := pkg{Name: fmt.Sprintf("twice-%d", round), Version: "1.0.0"}
		got := race(e, []func() int{publisher(e, a, e.tokA, p), publisher(e, b, e.tokA, p)})
		if got[0] != 201 || got[1] != 201 {
			t.Fatalf("round %d: %v", round, got)
		}
		if e.versions("npm-local", p.Name) != 1 || e.revSeq("npm-local", p.Name) != 1 {
			t.Fatalf("round %d: versions %d rev_seq %d", round, e.versions("npm-local", p.Name), e.revSeq("npm-local", p.Name))
		}
	}
}
