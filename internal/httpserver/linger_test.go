package httpserver

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// slowPeer opens a connection whose receive window stays tiny until it reads, so
// whatever the server writes stays in the server's send queue, unacknowledged.
func slowPeer(t *testing.T, addr string) net.Conn {
	t.Helper()
	d := net.Dialer{Control: func(_, _ string, rc syscall.RawConn) error {
		var serr error
		if err := rc.Control(func(fd uintptr) {
			serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, 4096)
		}); err != nil {
			return err
		}
		return serr
	}}
	c, err := d.DialContext(t.Context(), "tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if _, err := io.WriteString(c, "GET / HTTP/1.1\r\nHost: test\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	return c
}

// bigBody answers with n bytes, written at once: they fit into the send buffer, so
// the handler returns while the peer has taken none of them.
func bigBody(n int, entered chan<- struct{}, release <-chan struct{}) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-release
		_, _ = w.Write(make([]byte, n))
	})
}

func TestLingerShutdown(t *testing.T) {
	t.Run("Q-ARCH-187 R-ARCH-024 the shutdown waits until the peer has acknowledged the bytes of a finished response", func(t *testing.T) {
		if runtime.GOOS != "linux" {
			t.Skip("the send queue is read with SIOCOUTQ, which the pods have (Linux)")
		}
		const size = 1 << 20
		entered, release := make(chan struct{}, 1), make(chan struct{})
		s, addr := start(t, ServerConfig{Handler: bigBody(size, entered, release)})
		c := slowPeer(t, addr)
		<-entered

		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		begin := time.Now()
		shut := make(chan error, 1)
		go func() { shut <- s.Shutdown(ctx) }()
		close(release)

		const hold = 400 * time.Millisecond
		time.Sleep(hold)
		select {
		case err := <-shut:
			t.Fatalf("Shutdown returned after %s with %v while the peer had read nothing", time.Since(begin), err)
		default:
		}
		// The peer takes its time, then reads everything and sees a clean end.
		br := bufio.NewReader(c)
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		n, err := io.Copy(io.Discard, resp.Body)
		if err != nil || n != size {
			t.Fatalf("read %d of %d bytes, error %v", n, size, err)
		}
		select {
		case err := <-shut:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Shutdown did not return after the peer had read the response")
		}
		if got := len(s.lingering); got != 0 {
			t.Errorf("%d connections still lingering", got)
		}
	})

	t.Run("Q-ARCH-187 R-ARCH-024 a peer that never reads is reset at the bound, not waited for", func(t *testing.T) {
		if runtime.GOOS != "linux" {
			t.Skip("the send queue is read with SIOCOUTQ, which the pods have (Linux)")
		}
		entered, release := make(chan struct{}, 1), make(chan struct{})
		close(release)
		s, addr := start(t, ServerConfig{Handler: bigBody(1<<20, entered, release)})
		c := slowPeer(t, addr)
		<-entered

		begin := time.Now()
		err := s.ShutdownBy(t.Context(), begin.Add(300*time.Millisecond))
		took := time.Since(begin)
		if err != nil {
			t.Fatal(err)
		}
		if took < 250*time.Millisecond || took > 3*time.Second {
			t.Errorf("Shutdown took %s, want about the 300 ms bound", took)
		}
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		_, err = io.Copy(io.Discard, c)
		if !errors.Is(err, syscall.ECONNRESET) {
			t.Errorf("the peer saw %v, want a reset", err)
		}
	})

	t.Run("Q-ARCH-187 R-ARCH-024 a transfer cut at the deadline is reset at once, so the peer hears of the end", func(t *testing.T) {
		if runtime.GOOS != "linux" {
			t.Skip("the reset is sent on Linux, where the pods run")
		}
		entered := make(chan struct{}, 1)
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "1073741824")
			entered <- struct{}{}
			for r.Context().Err() == nil {
				if _, err := w.Write(make([]byte, 1024)); err != nil {
					return
				}
				w.(http.Flusher).Flush()
				time.Sleep(10 * time.Millisecond)
			}
		})
		s, addr := start(t, ServerConfig{Handler: h, CommitGrace: 50 * time.Millisecond})
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		if _, err := io.WriteString(c, "GET / HTTP/1.1\r\nHost: test\r\n\r\n"); err != nil {
			t.Fatal(err)
		}
		<-entered
		resp, err := http.ReadResponse(bufio.NewReader(c), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		defer cancel()
		if err := s.Shutdown(ctx); !errors.Is(err, ErrCut) {
			t.Fatalf("Shutdown returned %v, want a cut", err)
		}
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		_, err = io.Copy(io.Discard, resp.Body)
		if !errors.Is(err, syscall.ECONNRESET) {
			t.Errorf("the peer saw %v, want a reset", err)
		}
	})
}
