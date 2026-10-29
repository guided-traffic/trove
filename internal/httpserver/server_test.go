package httpserver

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// start binds a server on a loopback port and serves it.
func start(t *testing.T, cfg ServerConfig) (*Server, string) {
	t.Helper()
	cfg.Name, cfg.Addr = "test", "127.0.0.1:0"
	s, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Bind(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.Serve() }()
	t.Cleanup(func() { _ = s.Close() })
	return s, s.Addr().String()
}

func TestServerShutdown(t *testing.T) {
	t.Run("R-ARCH-024 an in-flight request finishes after Shutdown starts and new connections are refused", func(t *testing.T) {
		entered := make(chan struct{})
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(entered)
			time.Sleep(300 * time.Millisecond)
			_, _ = w.Write([]byte("done"))
		})
		s, addr := start(t, ServerConfig{Handler: h})
		type result struct {
			body string
			err  error
		}
		res := make(chan result, 1)
		go func() {
			resp, err := http.Get("http://" + addr + "/")
			if err != nil {
				res <- result{err: err}
				return
			}
			defer resp.Body.Close()
			b, err := io.ReadAll(resp.Body)
			res <- result{string(b), err}
		}()
		<-entered
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		shut := make(chan error, 1)
		go func() { shut <- s.Shutdown(ctx) }()

		time.Sleep(50 * time.Millisecond)
		if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
			c.Close()
			t.Fatal("a new connection was accepted after Shutdown started")
		}
		r := <-res
		if r.err != nil || r.body != "done" {
			t.Fatalf("in-flight request: %q %v", r.body, r.err)
		}
		if err := <-shut; err != nil {
			t.Fatalf("Shutdown = %v", err)
		}
	})

	t.Run("Q-ARCH-18 a transfer still running at the deadline is cut and counted", func(t *testing.T) {
		var cuts atomic.Int32
		var dir atomic.Value
		entered := make(chan struct{})
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "1000000")
			close(entered)
			for {
				if _, err := w.Write([]byte("x")); err != nil {
					return
				}
				w.(http.Flusher).Flush()
				time.Sleep(10 * time.Millisecond)
			}
		})
		s, addr := start(t, ServerConfig{
			Handler: h, CommitGrace: 50 * time.Millisecond,
			OnTransferCut: func(d string) { cuts.Add(1); dir.Store(d) },
		})
		readErr := make(chan error, 1)
		go func() {
			resp, err := http.Get("http://" + addr + "/")
			if err != nil {
				readErr <- err
				return
			}
			defer resp.Body.Close()
			_, err = io.Copy(io.Discard, resp.Body)
			readErr <- err
		}()
		<-entered
		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		defer cancel()
		start := time.Now()
		err := s.Shutdown(ctx)
		if !errors.Is(err, ErrCut) {
			t.Fatalf("Shutdown = %v, want ErrCut", err)
		}
		if time.Since(start) > 3*time.Second {
			t.Fatal("Shutdown overran its deadline")
		}
		if e := <-readErr; e == nil {
			t.Fatal("the client saw a complete response, but the transfer must be cut (Content-Length is not reached)")
		}
		if cuts.Load() != 1 || dir.Load() != DirectionDownload {
			t.Fatalf("cuts %d direction %v", cuts.Load(), dir.Load())
		}
	})

	t.Run("R-ARCH-024 a request whose body arrived completely runs on past the deadline for the commit grace", func(t *testing.T) {
		var cuts atomic.Int32
		entered := make(chan struct{})
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body) // body complete: the upload is now committing
			close(entered)
			time.Sleep(600 * time.Millisecond) // beyond the 200 ms deadline, inside the 2 s grace
			w.WriteHeader(201)
		})
		s, addr := start(t, ServerConfig{Handler: h, CommitGrace: 2 * time.Second, OnTransferCut: func(string) { cuts.Add(1) }})
		got := make(chan int, 1)
		go func() {
			resp, err := http.Post("http://"+addr+"/", "application/octet-stream", io.NopCloser(stringsReader("0123456789")))
			if err != nil {
				got <- -1
				return
			}
			resp.Body.Close()
			got <- resp.StatusCode
		}()
		<-entered
		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		defer cancel()
		if err := s.Shutdown(ctx); err != nil {
			t.Fatalf("Shutdown = %v: the committing upload must not be cut", err)
		}
		if code := <-got; code != 201 {
			t.Fatalf("status = %d, want 201", code)
		}
		if cuts.Load() != 0 {
			t.Fatalf("%d cuts", cuts.Load())
		}
	})

	t.Run("R-ARCH-024 BeginCommit lets a download-shaped request run on for the commit grace", func(t *testing.T) {
		entered := make(chan struct{})
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			BeginCommit(r.Context()) // no request body at all: only the marker says "committing"
			close(entered)
			time.Sleep(400 * time.Millisecond)
			w.WriteHeader(204)
		})
		s, addr := start(t, ServerConfig{Handler: h, CommitGrace: 2 * time.Second})
		got := make(chan int, 1)
		go func() {
			resp, err := http.Get("http://" + addr + "/")
			if err != nil {
				got <- -1
				return
			}
			resp.Body.Close()
			got <- resp.StatusCode
		}()
		<-entered
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()
		if err := s.Shutdown(ctx); err != nil {
			t.Fatalf("Shutdown = %v: the committing request must not be cut", err)
		}
		if code := <-got; code != 204 {
			t.Fatalf("status = %d", code)
		}
	})

	t.Run("R-ARCH-024 BeginCommit outside a request does nothing", func(t *testing.T) {
		BeginCommit(t.Context())
	})

	t.Run("R-ARCH-024 the commit grace never reaches past the absolute bound of ShutdownBy", func(t *testing.T) {
		entered := make(chan struct{})
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			BeginCommit(r.Context())
			close(entered)
			time.Sleep(5 * time.Second)
		})
		s, addr := start(t, ServerConfig{Handler: h, CommitGrace: 10 * time.Second})
		go func() {
			if resp, err := http.Get("http://" + addr + "/"); err == nil {
				resp.Body.Close()
			}
		}()
		<-entered
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()
		begin := time.Now()
		err := s.ShutdownBy(ctx, time.Now().Add(300*time.Millisecond))
		if !errors.Is(err, ErrCut) {
			t.Fatalf("ShutdownBy = %v, want ErrCut", err)
		}
		if took := time.Since(begin); took > 2*time.Second {
			t.Fatalf("took %v: the 10 s CommitGrace outran the bound of 300 ms", took)
		}
	})

	t.Run("Q-ARCH-18 an upload whose body is still arriving is cut at the deadline", func(t *testing.T) {
		var dir atomic.Value
		entered := make(chan struct{})
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(entered)
			_, _ = io.Copy(io.Discard, r.Body)
		})
		s, addr := start(t, ServerConfig{Handler: h, CommitGrace: 50 * time.Millisecond, OnTransferCut: func(d string) { dir.Store(d) }})
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		fmt.Fprint(c, "PUT / HTTP/1.1\r\nHost: x\r\nContent-Length: 1000\r\n\r\nabc") // 997 bytes never come
		<-entered
		ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
		defer cancel()
		if err := s.Shutdown(ctx); !errors.Is(err, ErrCut) {
			t.Fatalf("Shutdown = %v", err)
		}
		if dir.Load() != DirectionUpload {
			t.Fatalf("direction = %v", dir.Load())
		}
	})

	t.Run("R-ARCH-024 OnShutdown functions run when Shutdown starts, before it waits", func(t *testing.T) {
		stream := make(chan struct{})
		entered := make(chan struct{})
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(entered)
			<-stream // an event stream that never becomes idle on its own
		})
		s, addr := start(t, ServerConfig{Handler: h})
		s.OnShutdown(func() { close(stream) })
		go func() {
			resp, err := http.Get("http://" + addr + "/")
			if err == nil {
				resp.Body.Close()
			}
		}()
		<-entered
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		start := time.Now()
		if err := s.Shutdown(ctx); err != nil {
			t.Fatalf("Shutdown = %v", err)
		}
		if time.Since(start) > 2*time.Second {
			t.Fatal("the stream was not ended by the shutdown hook")
		}
	})
}

type stringsReaderT struct{ s string }

func stringsReader(s string) io.Reader { return &stringsReaderT{s} }
func (r *stringsReaderT) Read(p []byte) (int, error) {
	if r.s == "" {
		return 0, io.EOF
	}
	n := copy(p, r.s)
	r.s = r.s[n:]
	return n, nil
}

func TestStallTimeout(t *testing.T) {
	t.Run("R-ARCH-005 a client that stops sending has its connection closed after the stall timeout", func(t *testing.T) {
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.Copy(io.Discard, r.Body) })
		_, addr := start(t, ServerConfig{Handler: h, StallTimeout: 300 * time.Millisecond})
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		fmt.Fprint(c, "PUT / HTTP/1.1\r\nHost: x\r\nContent-Length: 100\r\n\r\nabc")
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		start := time.Now()
		_, err = bufio.NewReader(c).ReadString('\n') // the server closes (possibly after a 4xx)
		took := time.Since(start)
		if took < 250*time.Millisecond || took > 3*time.Second {
			t.Fatalf("connection ended after %v (err %v), want about the stall timeout", took, err)
		}
	})

	t.Run("R-ARCH-005 every successful chunk moves the deadline, so a slow but steady transfer completes", func(t *testing.T) {
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n, _ := io.Copy(io.Discard, r.Body)
			_, _ = fmt.Fprint(w, n)
		})
		_, addr := start(t, ServerConfig{Handler: h, StallTimeout: 300 * time.Millisecond})
		pr, pw := io.Pipe()
		go func() {
			for range 8 { // 8 x 100 ms = 800 ms in total, each gap far below the stall timeout
				_, _ = pw.Write([]byte("0123456789"))
				time.Sleep(100 * time.Millisecond)
			}
			pw.Close()
		}()
		resp, err := http.Post("http://"+addr+"/", "application/octet-stream", pr)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if string(b) != "80" {
			t.Fatalf("body = %q", b)
		}
	})
}
