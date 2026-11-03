package engine

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/mainclient"
)

// endlessBody is a response body of n zero bytes that counts its Close.
type endlessBody struct {
	left   int64
	closed atomic.Int64
}

func (b *endlessBody) Read(p []byte) (int, error) {
	if b.left <= 0 {
		return 0, io.EOF
	}
	n := min(int64(len(p)), b.left)
	b.left -= n
	return int(n), nil
}

func (b *endlessBody) Close() error { b.closed.Add(1); return nil }

// paceServer serves the engine of a harness whose main installation answers every forward with an unmarked
// pass-through response of size bytes; it reports when a request has left the engine.
func paceServer(t *testing.T, s *siteHarness, size int64) (addr string, body *endlessBody, returned chan struct{}) {
	t.Helper()
	body = &endlessBody{left: size}
	returned = make(chan struct{}, 4)
	s.main.doFn = func(mainclient.Forward) (*mainclient.Response, error) {
		r := respUnmarked(200, http.Header{"Content-Type": {"application/octet-stream"}}, "")
		r.Body = body
		return r, nil
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { returned <- struct{}{} }()
		s.e.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().String(), body, returned
}

func paceRequest(t *testing.T, s *siteHarness, addr string) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetReadBuffer(4096)
	}
	t.Cleanup(func() { _ = c.Close() })
	fmt.Fprintf(c, "GET /api/npm/npm-all/some/big HTTP/1.1\r\nHost: packages.example.com\r\nConnection: close\r\nAuthorization: Bearer %s\r\n\r\n", s.full)
	return c
}

// TestR_BAS_025_APassThroughBodyDoesNotHoldItsSlotForAClientThatDoesNotTakeIt: the body of a pass-through holds
// the budget slot until it ends or is closed. A client that takes no chunk is cut after the chunk deadline, one
// that takes a trickle after the grace period (it must average the floor rate), and the body is closed each
// time; a client that keeps up is served whole (Q-BAS-123, security finding P3).
func TestR_BAS_025_APassThroughBodyDoesNotHoldItsSlotForAClientThatDoesNotTakeIt(t *testing.T) {
	newSite := func(t *testing.T, size int64) (*siteHarness, string, *endlessBody, chan struct{}) {
		s := newSiteHarness(t, options{})
		s.e.paceChunk, s.e.paceGrace, s.e.paceMinRate = 300*time.Millisecond, 400*time.Millisecond, 1<<20
		addr, body, returned := paceServer(t, s, size)
		return s, addr, body, returned
	}
	wait := func(t *testing.T, returned chan struct{}, what string) {
		t.Helper()
		select {
		case <-returned:
		case <-time.After(15 * time.Second):
			t.Fatalf("the request did not end: %s", what)
		}
	}

	t.Run("a client that reads nothing", func(t *testing.T) {
		s, addr, body, returned := newSite(t, 1<<40)
		paceRequest(t, s, addr)
		wait(t, returned, "the client takes no byte")
		if body.closed.Load() == 0 {
			t.Fatal("the body, which holds the slot, was not closed")
		}
	})
	t.Run("a client that is slower than the floor", func(t *testing.T) {
		s, addr, body, returned := newSite(t, 1<<40)
		c := paceRequest(t, s, addr)
		go func() {
			r := bufio.NewReader(c)
			buf := make([]byte, 16<<10)
			for {
				if _, err := r.Read(buf); err != nil {
					return
				}
				time.Sleep(40 * time.Millisecond) // about 400 KB/s: every chunk in time, under the floor of 1 MiB/s
			}
		}()
		wait(t, returned, "the client takes every chunk in time but averages less than the floor")
		if body.closed.Load() == 0 {
			t.Fatal("the body, which holds the slot, was not closed")
		}
	})
	t.Run("a client that keeps up", func(t *testing.T) {
		s, addr, body, returned := newSite(t, 8<<20)
		c := paceRequest(t, s, addr)
		n, err := io.Copy(io.Discard, bufio.NewReader(c))
		if n < 8<<20 {
			t.Fatalf("%d bytes read (%v), want the whole body", n, err)
		}
		wait(t, returned, "a client that keeps up")
		if body.closed.Load() == 0 {
			t.Fatal("the body was not closed")
		}
	})
}
