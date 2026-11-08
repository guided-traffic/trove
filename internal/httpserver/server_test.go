package httpserver

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

	t.Run("R-ARCH-005 every successful request and response chunk moves the read and write deadline to now plus the stall timeout through the ResponseController", func(t *testing.T) {
		// The property is "each successful chunk moves the read and the write deadline to
		// now plus the stall timeout", so the case measures the move itself. A transfer that
		// lasts several stall timeouts over a real socket also depends on the process not
		// being paused for longer than the chunk gap (net deadlines are absolute, and a read
		// fails once its deadline has passed even with the bytes in the socket), which a
		// loaded host does not guarantee. Every move is bracketed by two readings of the
		// clock taken by the same goroutine around the call, so a pause cannot break it.
		const stall = time.Hour
		s, err := NewServer(ServerConfig{StallTimeout: stall})
		if err != nil {
			t.Fatal(err)
		}
		w := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
		var bodyChunks int
		h := s.track(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec := w.(*stallWriter).ResponseWriter.(*deadlineRecorder)
			if len(rec.reads) != 1 || len(rec.writes) != 1 {
				t.Fatalf("deadlines set before the first chunk: reads %d, writes %d, want 1 each (the initial deadline)", len(rec.reads), len(rec.writes))
			}
			buf := make([]byte, 10)
			for {
				before := time.Now()
				n, err := r.Body.Read(buf)
				after := time.Now()
				if n > 0 {
					bodyChunks++
					got := rec.reads[len(rec.reads)-1]
					if got.Before(before.Add(stall)) || got.After(after.Add(stall)) {
						t.Fatalf("read chunk %d moved the read deadline to %v, want within [%v, %v]", bodyChunks, got, before.Add(stall), after.Add(stall))
					}
					if gw := rec.writes[len(rec.writes)-1]; !gw.Equal(got) {
						t.Fatalf("read chunk %d moved the write deadline to %v, want %v (the read deadline)", bodyChunks, gw, got)
					}
				}
				if err != nil {
					break
				}
			}
			for i := 1; i <= 3; i++ {
				before := time.Now()
				if _, err := w.Write([]byte("0123456789")); err != nil {
					t.Fatal(err)
				}
				after := time.Now()
				got := rec.writes[len(rec.writes)-1]
				if got.Before(before.Add(stall)) || got.After(after.Add(stall)) {
					t.Fatalf("write chunk %d moved the write deadline to %v, want within [%v, %v]", i, got, before.Add(stall), after.Add(stall))
				}
			}
		}))
		const chunks = 8
		r := httptest.NewRequest(http.MethodPost, "/", io.NopCloser(&chunkReader{chunks: chunks}))
		r.ContentLength = -1
		h.ServeHTTP(w, r)
		if bodyChunks != chunks {
			t.Fatalf("read %d chunks, want %d", bodyChunks, chunks)
		}
		if got, want := len(w.reads), 1+chunks+3; got != want {
			t.Fatalf("read deadline set %d times, want %d (initial, one per request chunk, one per response chunk)", got, want)
		}
		if got, want := len(w.writes), 1+chunks+3+1; got != want {
			t.Fatalf("write deadline set %d times, want %d (initial, one per request chunk, one per response chunk, one cleared at the end)", got, want)
		}
		if last := w.writes[len(w.writes)-1]; !last.IsZero() {
			t.Fatalf("the write deadline outlives the request: %v", last)
		}
		for i := 1; i < len(w.reads); i++ {
			if w.reads[i].Before(w.reads[i-1]) {
				t.Fatalf("read deadline %d (%v) moved back before deadline %d (%v)", i, w.reads[i], i-1, w.reads[i-1])
			}
		}
	})

	t.Run("R-ARCH-005 every successful chunk moves the deadline of the connection, so a slow but steady transfer completes", func(t *testing.T) {
		// A real socket and a real net/http stack with a stall timeout of an hour: the client
		// sends chunk i+1 only after the handler has read chunk i, so no wall-clock gap decides
		// the outcome. Every chunk the handler reads or writes must leave a deadline of now plus
		// the stall timeout on the accepted connection, which also proves that net/http does not
		// overwrite ours between chunks, and the write deadline is cleared when the request ends.
		const (
			stall  = time.Hour
			chunks = 8
		)
		type bracket struct{ before, after time.Time }
		var (
			mu             sync.Mutex
			reads, writes  []bracket
			readsInHandler int
		)
		next := make(chan struct{}, 4*chunks) // buffered: a chunk split across reads cannot deadlock
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			buf := make([]byte, 10)
			for {
				b := time.Now()
				n, err := r.Body.Read(buf)
				a := time.Now()
				if n > 0 {
					mu.Lock()
					reads = append(reads, bracket{b, a})
					readsInHandler += n
					mu.Unlock()
					next <- struct{}{}
				}
				if err != nil {
					break
				}
			}
			for range 3 {
				b := time.Now()
				_, _ = w.Write([]byte("0123456789"))
				w.(http.Flusher).Flush()
				mu.Lock()
				writes = append(writes, bracket{b, time.Now()})
				mu.Unlock()
			}
		})
		s, err := NewServer(ServerConfig{Name: "test", Addr: "127.0.0.1:0", Handler: h, StallTimeout: stall})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Bind(); err != nil {
			t.Fatal(err)
		}
		conns := make(chan *recConn, 1)
		s.ln = &recListener{Listener: s.ln, conns: conns}
		go func() { _ = s.Serve() }()
		t.Cleanup(func() { _ = s.Close() })

		pr, pw := io.Pipe()
		go func() {
			defer pw.Close()
			for range chunks {
				if _, err := pw.Write([]byte("0123456789")); err != nil {
					return
				}
				select {
				case <-next:
				case <-time.After(30 * time.Second):
					return
				}
			}
		}()
		resp, err := http.Post("http://"+s.Addr().String()+"/", "application/octet-stream", pr)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if want := strings.Repeat("0123456789", 3); string(body) != want {
			t.Fatalf("body = %q, want %q", body, want)
		}
		c := <-conns
		c.mu.Lock()
		defer c.mu.Unlock()
		mu.Lock()
		defer mu.Unlock()
		if readsInHandler != 10*chunks {
			t.Fatalf("the handler read %d bytes, want %d", readsInHandler, 10*chunks)
		}
		in := func(ts []time.Time, br bracket) bool {
			for _, d := range ts {
				if !d.Before(br.before.Add(stall)) && !d.After(br.after.Add(stall)) {
					return true
				}
			}
			return false
		}
		for i, br := range reads {
			if !in(c.reads, br) || !in(c.writes, br) {
				t.Errorf("request chunk %d moved no deadline of the connection", i)
			}
		}
		for i, br := range writes {
			if !in(c.reads, br) || !in(c.writes, br) {
				t.Errorf("response chunk %d moved no deadline of the connection", i)
			}
		}
		if len(writes) != 3 {
			t.Errorf("the handler wrote %d response chunks, want 3", len(writes))
		}
		if len(c.writes) == 0 || !c.writes[len(c.writes)-1].IsZero() {
			t.Errorf("the last write deadline of the connection is not zero: %v", c.writes)
		}
	})
}

// recConn logs the deadlines set on an accepted connection and passes them on.
type recConn struct {
	net.Conn
	mu            sync.Mutex
	reads, writes []time.Time
}

func (c *recConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.reads = append(c.reads, t)
	c.mu.Unlock()
	return c.Conn.SetReadDeadline(t)
}

func (c *recConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	c.writes = append(c.writes, t)
	c.mu.Unlock()
	return c.Conn.SetWriteDeadline(t)
}

// recListener hands every accepted connection, wrapped in a recConn, to conns.
type recListener struct {
	net.Listener
	conns chan *recConn
}

func (l *recListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	rc := &recConn{Conn: c}
	l.conns <- rc
	return rc, nil
}

// deadlineRecorder records the deadlines http.ResponseController sets on it.
type deadlineRecorder struct {
	*httptest.ResponseRecorder
	reads, writes []time.Time
}

func (d *deadlineRecorder) SetReadDeadline(t time.Time) error {
	d.reads = append(d.reads, t)
	return nil
}

func (d *deadlineRecorder) SetWriteDeadline(t time.Time) error {
	d.writes = append(d.writes, t)
	return nil
}

// chunkReader yields ten bytes per Read, chunks times, then io.EOF.
type chunkReader struct{ chunks, done int }

func (c *chunkReader) Read(p []byte) (int, error) {
	if c.done == c.chunks {
		return 0, io.EOF
	}
	c.done++
	return copy(p, "0123456789"), nil
}
