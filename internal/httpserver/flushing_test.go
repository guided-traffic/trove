package httpserver

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/tlsfiles/tlstest"
)

// h2Server serves h over TLS with HTTP/2 and returns the CA the client trusts.
func h2Server(t *testing.T, h http.Handler) (*Server, string, *tlstest.CA) {
	t.Helper()
	dir := t.TempDir()
	ca := tlstest.NewCA(t, "ca")
	cert, key := ca.Issue(t, tlstest.Options{}).Files(t, dir, "srv")
	l, err := NewListenerTLS("client", config.ServerTLS{Certificates: pairOf(cert, key), ReloadInterval: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	s, addr := start(t, ServerConfig{Handler: h, TLS: l, CommitGrace: time.Second})
	return s, addr, ca
}

func flushingLen(s *Server) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.flushing)
}

func TestFlushingHTTP2(t *testing.T) {
	t.Run("R-ARCH-024 CR2-2 a connection that died during the commit does not stay in the flushing set", func(t *testing.T) {
		entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() { go func() { time.Sleep(50 * time.Millisecond); close(returned) }() }()
			_, _ = io.Copy(io.Discard, r.Body)
			BeginCommit(r.Context())
			close(entered)
			<-release
			w.WriteHeader(http.StatusCreated)
		})
		s, addr, ca := h2Server(t, h)
		var raw net.Conn
		tr := &http.Transport{
			ForceAttemptHTTP2: true,
			DialTLSContext: func(ctx context.Context, network, a string) (net.Conn, error) {
				c, err := (&tls.Dialer{Config: &tls.Config{RootCAs: ca.Pool(), NextProtos: []string{"h2"}}}).DialContext(ctx, network, a)
				raw = c
				return c, err
			},
		}
		defer tr.CloseIdleConnections()
		go func() {
			req, _ := http.NewRequest(http.MethodPost, "https://"+addr+"/upload", strings.NewReader("payload"))
			if resp, err := (&http.Client{Transport: tr}).Do(req); err == nil {
				resp.Body.Close()
			}
		}()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("handler not reached")
		}
		if p := raw.(*tls.Conn).ConnectionState().NegotiatedProtocol; p != "h2" {
			t.Fatalf("setup: negotiated %q, want h2", p)
		}
		_ = raw.Close() // the client goes away while the server commits
		time.Sleep(200 * time.Millisecond)
		close(release)
		<-returned
		time.Sleep(h2FlushWindow + 200*time.Millisecond)
		if n := flushingLen(s); n != 0 {
			t.Errorf("%d dead connection(s) stay in the flushing set; anyCommitting()=%v for ever", n, s.anyCommitting())
		}
	})

	t.Run("R-ARCH-024 CR2-2 a reset stream does not leave its idle connection in the flushing set", func(t *testing.T) {
		entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() { go func() { time.Sleep(50 * time.Millisecond); close(returned) }() }()
			_, _ = io.Copy(io.Discard, r.Body)
			close(entered)
			<-release
			w.WriteHeader(http.StatusCreated)
		})
		s, addr, ca := h2Server(t, h)
		tr := &http.Transport{ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{RootCAs: ca.Pool()}}
		defer tr.CloseIdleConnections()
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+addr+"/upload", strings.NewReader("payload"))
			if resp, err := (&http.Client{Transport: tr}).Do(req); err == nil {
				resp.Body.Close()
			}
		}()
		<-entered
		cancel()
		time.Sleep(200 * time.Millisecond)
		close(release)
		<-returned
		time.Sleep(h2FlushWindow + 200*time.Millisecond)
		if n := flushingLen(s); n != 0 {
			t.Errorf("%d idle h2 connection(s) in the flushing set after a cancelled request; anyCommitting()=%v", n, s.anyCommitting())
		}
	})

	t.Run("R-ARCH-024 CR2-2 a live committing h2 request is held while its response goes out and released after", func(t *testing.T) {
		returned := make(chan struct{})
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer close(returned)
			_, _ = io.Copy(io.Discard, r.Body)
			BeginCommit(r.Context())
			w.WriteHeader(http.StatusCreated)
		})
		s, addr, ca := h2Server(t, h)
		tr := &http.Transport{ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{RootCAs: ca.Pool()}}
		defer tr.CloseIdleConnections()
		req, _ := http.NewRequest(http.MethodPost, "https://"+addr+"/upload", strings.NewReader("payload"))
		resp, err := (&http.Client{Transport: tr}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.ProtoMajor != 2 {
			t.Fatalf("setup: proto %s", resp.Proto)
		}
		<-returned
		deadline := time.Now().Add(h2FlushWindow + 2*time.Second)
		for flushingLen(s) != 0 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if n := flushingLen(s); n != 0 {
			t.Errorf("the entry of a finished h2 request must be released, %d left", n)
		}
	})
}

func TestServeWaitsForReloader(t *testing.T) {
	t.Run("R-ARCH-035 CR2-9 Serve returns only after the listener's reloader has stopped", func(t *testing.T) {
		dir := t.TempDir()
		ca := tlstest.NewCA(t, "ca")
		cert, key := ca.Issue(t, tlstest.Options{}).Files(t, dir, "srv")
		l, err := NewListenerTLS("client", config.ServerTLS{Certificates: pairOf(cert, key), ReloadInterval: 20 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		s, err := NewServer(ServerConfig{Name: "test", Addr: "127.0.0.1:0", Handler: http.NotFoundHandler(), TLS: l})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Bind(); err != nil {
			t.Fatal(err)
		}
		served := make(chan error, 1)
		go func() { served <- s.Serve() }()
		for !l.running.Load() {
			time.Sleep(5 * time.Millisecond)
		}
		_ = s.Close()
		if err := <-served; err != nil {
			t.Fatal(err)
		}
		if l.running.Load() {
			t.Fatal("Serve returned while the reloader still ran")
		}
	})
}
