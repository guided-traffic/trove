package httpserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultCommitGrace is how long a request whose body has arrived completely may
// run on after the shutdown deadline (R-ARCH-024 step 5): it is storing and
// committing an upload, and cutting it between the commit and the status line
// would turn the client's retry into a conflict. The 5 s margin of the pod's
// grace period carries it, so it stays below that.
const DefaultCommitGrace = 4 * time.Second

// Transfer directions of trove_http_transfers_cut_total{direction}.
const (
	DirectionDownload = "download"
	DirectionUpload   = "upload"
)

// ServerConfig configures one listener.
type ServerConfig struct {
	// Name is "client" or "ops"; it appears in logs and errors.
	Name string
	// Addr is the listen address (server.listen, server.ops_listen). A missing
	// host binds every interface on IPv4 and IPv6.
	Addr    string
	Handler http.Handler
	TLS     *TLSFiles
	Log     *slog.Logger

	ReadHeaderTimeout time.Duration
	IdleTimeout       time.Duration
	MaxHeaderBytes    int
	// StallTimeout moves the read and write deadlines forward after every
	// successful chunk (server.transfer_stall_timeout); zero disables it.
	// http.Server.ReadTimeout and WriteTimeout stay 0, because they would cut long
	// transfers (R-ARCH-005).
	StallTimeout time.Duration

	// CommitGrace overrides DefaultCommitGrace.
	CommitGrace time.Duration
	// OnTransferCut is called once per request cut at the shutdown deadline, with
	// the direction (trove_http_transfers_cut_total).
	OnTransferCut func(direction string)
}

// Server is one HTTP listener with the lifecycle of R-ARCH-024: Bind, Serve,
// Shutdown.
type Server struct {
	cfg ServerConfig
	srv *http.Server
	ln  net.Listener

	mu     sync.Mutex
	active map[*activeRequest]struct{}
	// flushing holds the connections whose committing request has returned from
	// its handler but whose response net/http has not yet written: the connection
	// turns idle or closes once it has. Cutting before that would lose the answer
	// of a commit that went through.
	flushing map[net.Conn]struct{}
}

// activeRequest is one request between its first and its last byte.
type activeRequest struct {
	hasBody  bool
	bodyDone atomic.Bool
	// committing is set by BeginCommit: the handler announced that it stores or
	// commits now.
	committing atomic.Bool
}

// inCommit reports whether the request is past the point where cutting it
// would leave the client unsure of the outcome: the handler said so through
// BeginCommit, or, as the fallback, the whole body has arrived.
func (a *activeRequest) inCommit() bool {
	return a.committing.Load() || (a.hasBody && a.bodyDone.Load())
}

type (
	ctxKeyActive struct{}
	ctxKeyConn   struct{}
)

// connState drops a connection from the flushing set once its response is out.
func (s *Server) connState(c net.Conn, st http.ConnState) {
	switch st {
	case http.StateIdle, http.StateClosed, http.StateHijacked:
		s.mu.Lock()
		delete(s.flushing, c)
		s.mu.Unlock()
	}
}

// BeginCommit marks the request of ctx as committing: from now on the shutdown
// lets it run past the deadline for the commit grace instead of cutting it
// (R-ARCH-024 step 5). A handler calls it right before the step that must not be
// interrupted, such as the metadata commit of an upload. Without the call the
// arrival of the complete request body is the signal. Outside a request it does
// nothing.
func BeginCommit(ctx context.Context) {
	if a, ok := ctx.Value(ctxKeyActive{}).(*activeRequest); ok {
		a.committing.Store(true)
	}
}

func (a *activeRequest) direction() string {
	if a.hasBody {
		return DirectionUpload
	}
	return DirectionDownload
}

// NewServer prepares a listener; nothing is bound yet.
func NewServer(cfg ServerConfig) (*Server, error) {
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.CommitGrace == 0 {
		cfg.CommitGrace = DefaultCommitGrace
	}
	s := &Server{cfg: cfg, active: map[*activeRequest]struct{}{}, flushing: map[net.Conn]struct{}{}}
	s.srv = &http.Server{
		Handler:           s.track(cfg.Handler),
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    cfg.MaxHeaderBytes,
		ErrorLog:          slog.NewLogLogger(cfg.Log.Handler(), slog.LevelWarn),
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			return context.WithValue(ctx, ctxKeyConn{}, c)
		},
		ConnState: s.connState,
	}
	if cfg.TLS != nil && (cfg.TLS.CertFile != "" || cfg.TLS.KeyFile != "") {
		tc, err := tlsConfig(*cfg.TLS)
		if err != nil {
			return nil, fmt.Errorf("%s listener: %w", cfg.Name, err)
		}
		s.srv.TLSConfig = tc
	}
	return s, nil
}

// Bind opens the listening socket. After it returns, connections queue in the
// kernel; Serve starts accepting them.
func (s *Server) Bind() error {
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("%s listener: %w", s.cfg.Name, err)
	}
	s.ln = ln
	return nil
}

// Addr returns the bound address; nil before Bind.
func (s *Server) Addr() net.Addr {
	if s.ln == nil {
		return nil
	}
	return s.ln.Addr()
}

// Serve accepts connections until Shutdown or Close. A normal stop returns nil.
func (s *Server) Serve() error {
	if s.ln == nil {
		return fmt.Errorf("%s listener: Serve before Bind", s.cfg.Name)
	}
	var err error
	if s.srv.TLSConfig != nil {
		err = s.srv.ServeTLS(s.ln, "", "")
	} else {
		err = s.srv.Serve(s.ln)
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// OnShutdown registers a function that runs when Shutdown starts, before it
// waits for connections to become idle. A long-lived stream ends itself here,
// because Shutdown never ends one (R-ARCH-024 step 3).
func (s *Server) OnShutdown(f func()) { s.srv.RegisterOnShutdown(f) }

// Active returns the number of requests in flight.
func (s *Server) Active() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.active)
}

// ErrCut is wrapped by the error Shutdown returns when requests were still
// running at the deadline and were cut.
var ErrCut = errors.New("requests cut at the shutdown deadline")

// Shutdown stops accepting, closes idle connections, sends GOAWAY on HTTP/2 and
// waits for in-flight requests until ctx ends. At the deadline it cuts what is
// still running by closing the connections (Q-ARCH-18), with one exception: a
// request that is committing (BeginCommit, or whose body has arrived completely)
// runs on for at most CommitGrace and never past the commitBy of ShutdownBy. The
// cut requests are counted through OnTransferCut. The error is nil when
// everything finished in time.
func (s *Server) Shutdown(ctx context.Context) error {
	by := time.Now().Add(s.cfg.CommitGrace)
	if d, ok := ctx.Deadline(); ok {
		by = d.Add(s.cfg.CommitGrace)
	}
	return s.ShutdownBy(ctx, by)
}

// ShutdownBy is Shutdown with an absolute bound for the commit grace: requests
// that are committing at the deadline of ctx run on until commitBy at most, and
// never longer than CommitGrace after the deadline. The caller derives commitBy
// from the one exit deadline of the process, so the grace cannot push the exit
// past the kubelet's kill.
func (s *Server) ShutdownBy(ctx context.Context, commitBy time.Time) error {
	err := s.srv.Shutdown(ctx)
	if err == nil {
		return nil
	}
	s.waitCommits(commitBy)
	cut := s.cutAll()
	_ = s.srv.Close()
	if cut == 0 {
		return nil
	}
	return fmt.Errorf("%s listener: %d request(s) cut: %w: %w", s.cfg.Name, cut, ErrCut, err)
}

// Close closes the listener and every connection at once.
func (s *Server) Close() error { return s.srv.Close() }

// waitCommits gives committing requests until the earlier of commitBy and
// CommitGrace from now.
func (s *Server) waitCommits(commitBy time.Time) {
	deadline := time.Now().Add(s.cfg.CommitGrace)
	if commitBy.Before(deadline) {
		deadline = commitBy
	}
	for time.Now().Before(deadline) {
		if !s.anyCommitting() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (s *Server) anyCommitting() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for a := range s.active {
		if a.inCommit() {
			return true
		}
	}
	return len(s.flushing) > 0
}

// cutAll counts the requests that are about to be cut.
func (s *Server) cutAll() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	for a := range s.active {
		if s.cfg.OnTransferCut != nil {
			s.cfg.OnTransferCut(a.direction())
		}
	}
	return len(s.active)
}

// track is the outermost handler: it registers the request for the shutdown
// accounting and implements the stall deadlines of R-ARCH-005.
func (s *Server) track(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := &activeRequest{hasBody: r.Body != nil && r.Body != http.NoBody && r.ContentLength != 0}
		r = r.WithContext(context.WithValue(r.Context(), ctxKeyActive{}, a))
		s.mu.Lock()
		s.active[a] = struct{}{}
		s.mu.Unlock()
		defer func() {
			s.mu.Lock()
			if c, ok := r.Context().Value(ctxKeyConn{}).(net.Conn); ok && a.inCommit() {
				s.flushing[c] = struct{}{}
			}
			delete(s.active, a)
			s.mu.Unlock()
		}()

		extend := func() {}
		if d := s.cfg.StallTimeout; d > 0 {
			rc := http.NewResponseController(w)
			extend = func() {
				t := time.Now().Add(d)
				_ = rc.SetReadDeadline(t)
				_ = rc.SetWriteDeadline(t)
			}
			extend()
			// net/http resets the read deadline itself before it reads the next request, and
			// it relies on ours while it drains an unread body after the handler returned.
			// The write deadline would otherwise outlive the request.
			defer func() { _ = rc.SetWriteDeadline(time.Time{}) }()
		}
		if a.hasBody {
			r.Body = &trackedBody{ReadCloser: r.Body, a: a, extend: extend}
		}
		next.ServeHTTP(&stallWriter{ResponseWriter: w, extend: extend}, r)
	})
}

// trackedBody notes the end of the request body and moves the read deadline
// after every chunk.
type trackedBody struct {
	io.ReadCloser
	a      *activeRequest
	extend func()
}

func (b *trackedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.extend()
	}
	if errors.Is(err, io.EOF) {
		b.a.bodyDone.Store(true)
	}
	return n, err
}

// stallWriter moves the write deadline after every successful chunk.
type stallWriter struct {
	http.ResponseWriter
	extend func()
}

func (w *stallWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	if n > 0 && err == nil {
		w.extend()
	}
	return n, err
}

// Flush implements http.Flusher.
func (w *stallWriter) Flush() {
	_ = http.NewResponseController(w.ResponseWriter).Flush()
	w.extend()
}

// Unwrap returns the wrapped writer for http.ResponseController.
func (w *stallWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
