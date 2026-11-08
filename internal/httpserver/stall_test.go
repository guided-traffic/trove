package httpserver

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/tlsfiles/tlstest"
)

func TestStallDeadlineOverHTTP2(t *testing.T) {
	const stall = 300 * time.Millisecond
	client := func(ca *tlstest.CA) *http.Client {
		tr := &http.Transport{ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{RootCAs: ca.Pool()}}
		t.Cleanup(tr.CloseIdleConnections)
		return &http.Client{Transport: tr}
	}
	for _, tc := range []struct {
		name, method string
		body         io.Reader
	}{
		{"an upload whose post-body work outlasts the stall timeout", http.MethodPost, strings.NewReader("abc")},
		{"a request without a body whose first byte outlasts the stall timeout", http.MethodGet, nil},
	} {
		t.Run("R-ARCH-005 over HTTP/2 "+tc.name+" keeps its stream and its request context", func(t *testing.T) {
			type outcome struct {
				proto string
				err   error
			}
			got := make(chan outcome, 1)
			_, addr, ca := h2ServerStall(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				time.Sleep(3 * stall)
				got <- outcome{r.Proto, r.Context().Err()}
				_, _ = w.Write([]byte("ok"))
			}), stall)
			req, _ := http.NewRequest(tc.method, "https://"+addr+"/", tc.body)
			resp, err := client(ca).Do(req)
			if err != nil {
				t.Fatalf("the client saw %v, want a response", err)
			}
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			o := <-got
			if o.proto != "HTTP/2.0" || o.err != nil || resp.StatusCode != 200 || string(b) != "ok" {
				t.Fatalf("proto %s, context %v, status %d, body %q; want HTTP/2.0, a live context and 200 \"ok\"", o.proto, o.err, resp.StatusCode, b)
			}
		})
	}

	t.Run("R-ARCH-005 over HTTP/2 a client that reads nothing after the handler returned has its stream reset within the stall timeout, and the connection stays usable", func(t *testing.T) {
		// The client's stream window is 4 MiB. The handler fills it with one write and leaves 1 KiB in
		// net/http's buffer, which can only be sent once the client reads: that is the rest of a
		// response written after the handler returned. A client that reads nothing is cut by the
		// write deadline armed at the handler's return; without it the read below completes.
		const window = 4 << 20
		_, addr, ca := h2ServerStall(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/ok" {
				_, _ = w.Write([]byte("ok"))
				return
			}
			_, _ = w.Write(make([]byte, window))
			_, _ = w.Write(make([]byte, 1024))
		}), stall)
		c := client(ca)
		resp, err := c.Get("https://" + addr + "/big")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		time.Sleep(4 * stall) // reads nothing; the armed deadline expires
		n, err := io.Copy(io.Discard, resp.Body)
		if err == nil {
			t.Fatalf("read all %d bytes of a response the client did not read for %v, want the stream reset", n, 4*stall)
		}
		if !strings.Contains(err.Error(), "INTERNAL_ERROR") {
			t.Fatalf("the read ended with %v after %d bytes, want the stream reset with INTERNAL_ERROR", err, n)
		}
		resp2, err := c.Get("https://" + addr + "/ok")
		if err != nil {
			t.Fatalf("the connection is not usable after the reset: %v", err)
		}
		b, _ := io.ReadAll(resp2.Body)
		resp2.Body.Close()
		if string(b) != "ok" {
			t.Fatalf("body %q", b)
		}
	})
}

// closeConn tells when the server closes the connection.
type closeConn struct {
	net.Conn
	once   sync.Once
	closed chan struct{}
}

func (c *closeConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

type closeListener struct {
	net.Listener
	closed chan struct{}
}

func (l *closeListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &closeConn{Conn: c, closed: l.closed}, nil
}

func TestStallDeadlineCutsAClientThatNeverReads(t *testing.T) {
	t.Run("R-ARCH-005 a client that pipelines requests and reads nothing is cut after the stall timeout, also for what net/http writes after the handler returned", func(t *testing.T) {
		const stall = 300 * time.Millisecond
		s, err := NewServer(ServerConfig{Name: "test", Addr: "127.0.0.1:0", StallTimeout: stall, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(strings.Repeat("x", 1024))) // buffered by net/http, written after the handler returns
		})})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Bind(); err != nil {
			t.Fatal(err)
		}
		closed := make(chan struct{})
		s.ln = &closeListener{Listener: s.ln, closed: closed}
		go func() { _ = s.Serve() }()
		t.Cleanup(func() { _ = s.Close() })
		c, err := net.Dial("tcp", s.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		_ = c.(*net.TCPConn).SetReadBuffer(4096)
		go func() {
			w := bufio.NewWriter(c)
			for range 20000 {
				fmt.Fprint(w, "GET / HTTP/1.1\r\nHost: x\r\n\r\n")
			}
			_ = w.Flush()
		}()
		select {
		case <-closed:
		case <-time.After(5 * time.Second): // 16 stall timeouts; without the armed deadline nothing cuts it
			t.Fatal("the connection of a client that reads nothing was not closed")
		}
	})
}
