// Package server is the process of the role bastion (R-BAS-001, R-BAS-043, R-BAS-044): the configuration is
// loaded and validated by internal/config, the token file and the signing key are read, the cache backend, the
// shared state, the guard, the client of the main installation with its budget, the link, the token reloader
// and the download buffer are built, the TLS material of the two listeners is loaded and reloaded, and two
// listeners serve: the client listener, which hands every request to the engine, and the ops listener with
// /healthz, /readyz and /metrics.
//
// It enables none of the main installation's subsystems: no PostgreSQL pool, no object-store client for the
// main installation's blobs, no job runner, no format route table, no management API, no UI, no login
// (R-BAS-001). The archtest rule of R-BAS-001 holds its imports to the platform packages.
//
// The server runs the loops of the pieces; the engine runs only its own janitor. Start order (R-BAS-043): the
// token file, the signing key, the CA file of the main installation, the TLS material and the dependencies are
// read when the server is built, so a defect stops startup before a port is bound; then the ops listener, then
// the client listener; readiness follows the cache backend's first check. The link (handshake, feed history,
// reachability), the token reloader, the download buffer and the cache sweeper then run in the background and
// do not gate readiness. Drain order (R-ARCH-024, R-BAS-043): readiness off, the drain delay, the client
// listener shuts down by the deadline, the sweeper and the buffer's flush loop stop, the engine drains, the
// download buffer is sent or spooled, then the link and the reloader stop, the ops listener last.
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/cache"
	"github.com/guided-traffic/trove/internal/bastion/downloads"
	"github.com/guided-traffic/trove/internal/bastion/engine"
	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastion/link"
	"github.com/guided-traffic/trove/internal/bastion/mainclient"
	bmetrics "github.com/guided-traffic/trove/internal/bastion/metrics"
	"github.com/guided-traffic/trove/internal/buildinfo"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/observability"
	"github.com/guided-traffic/trove/internal/observability/health"
	"github.com/guided-traffic/trove/internal/tlsfiles"
)

// Options configure a Bastion process.
type Options struct {
	// Loaded is the configuration of the role (config.LoadBastion).
	Loaded *config.BastionLoaded
	// Environ is the process environment; nil means os.Environ.
	Environ []string
	// LogWriter receives the JSON log; nil means os.Stdout.
	LogWriter io.Writer
	// DrainDelay replaces config.BastionShutdownDrainDelay, the time between the readiness flip and the stop of
	// the client listener; tests set it short. Production leaves it zero.
	DrainDelay time.Duration
	// Deps lets a test replace what the server builds from the configuration (the cache, the guard, the shared
	// state, the buffer) before the link is wired over the client. Production passes nil.
	Deps func(*engine.Deps)
}

// Server is one running Bastion replica.
type Server struct {
	o   Options
	cfg *config.BastionConfig

	instance string
	log      *slog.Logger
	logging  *observability.Logging
	metrics  *observability.Metrics
	bm       *bmetrics.Metrics
	tracer   *observability.Tracer
	tlsm     *tlsfiles.Metrics
	health   *health.Health
	engine   engine.Engine
	deps     engine.Deps

	clientTLS, opsTLS *httpserver.ListenerTLS
	client, ops       *httpserver.Server

	link     *link.Link
	reloader *mainclient.Reloader
	holder   *mainclient.Holder

	started    atomic.Bool
	healthStop context.CancelFunc
	engineStop context.CancelFunc
	// reportStop stops the download buffer's flush loop and sweepStop the cache sweeper (before the drain),
	// linkStop the link and the token reloader (after it).
	reportStop context.CancelFunc
	sweepStop  context.CancelFunc
	linkStop   context.CancelFunc
	bg         sync.WaitGroup
	fatal      chan error
	// closers are released at the end: the Valkey client and the guard's loop.
	closers []io.Closer
}

var errStarting = errors.New("starting")

// Run starts a replica and serves until ctx ends or a listener fails, then shuts down in the order of
// R-ARCH-024.
func Run(ctx context.Context, o Options) error {
	s, err := New(o)
	if err != nil {
		return err
	}
	if err := s.Start(ctx); err != nil {
		s.abort()
		return err
	}
	var fatal error
	select {
	case <-ctx.Done():
	case fatal = <-s.fatal:
	}
	return errors.Join(fatal, s.Shutdown(context.WithoutCancel(ctx)))
}

// New reads everything a defect of which must stop startup (R-BAS-006, R-BAS-032, R-BAS-043) and builds the
// replica; nothing is bound and nothing runs.
func New(o Options) (*Server, error) {
	if o.Loaded == nil || o.Loaded.Config == nil {
		return nil, errors.New("bastion: no configuration")
	}
	cfg := o.Loaded.Config
	environ := o.Environ
	if environ == nil {
		environ = os.Environ()
	}
	s := &Server{o: o, cfg: cfg, instance: instanceName(environ), fatal: make(chan error, 1)}
	s.logging = observability.NewLogging(cfg.Log, o.LogWriter, observability.WithIdentity(s.instance, config.BastionRole))
	s.log = s.logging.Logger()
	s.metrics = observability.NewMetrics(config.BastionRole, observability.WithMetricsConfig(cfg.MetricsSection()))
	var err error
	if s.bm, err = bmetrics.New(s.metrics.Registry()); err != nil {
		return nil, err
	}
	if s.tlsm, err = tlsfiles.NewMetrics(s.metrics.Registry()); err != nil {
		return nil, err
	}
	s.tracer, err = observability.Tracing(cfg.Tracing.Tracing(),
		observability.WithTraceResource(s.instance, config.BastionRole, buildinfo.Get().Version),
		observability.WithTraceMetrics(s.metrics), observability.WithTraceLogger(s.log))
	if err != nil {
		return nil, err
	}
	s.health = health.New(cfg.HealthSection(), health.WithLogger(s.log.With("component", "health")),
		health.WithIdentity(s.instance, config.BastionRole), health.WithObserver(s.checkObserver()))
	if s.deps, err = s.buildDeps(); err != nil {
		s.closeAll()
		return nil, err
	}
	if s.engine, err = engine.New(s.deps); err != nil {
		s.closeAll()
		return nil, err
	}
	if err := s.startTLS(); err != nil {
		s.closeAll()
		return nil, err
	}
	return s, nil
}

// instanceName is the pod name for the logs, the metrics and Trove-Bastion-Replica.
func instanceName(environ []string) string {
	env := map[string]string{}
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	for _, k := range []string{"TROVE_POD_NAME", "POD_NAME"} {
		if env[k] != "" {
			return env[k]
		}
	}
	if h, err := os.Hostname(); err == nil && h != "" {
		return fmt.Sprintf("%s-%d", h, os.Getpid())
	}
	return "trove-bastion"
}

func (s *Server) checkObserver() health.Observer {
	passing := s.metrics.NewGaugeVec("trove_health_check_passing", "1 while the readiness check passes, 0 while it fails.", "name")
	return func(name string, ok bool) {
		v := 0.0
		if ok {
			v = 1
		}
		passing.WithLabelValues(name).Set(v)
	}
}

// buildDeps reads the token file and the signing key and assembles the engine's dependencies from the
// configuration: the shared state and the guard, the cache with its backend, the client of the main
// installation with its budget, the link and the download buffer (R-BAS-043).
func (s *Server) buildDeps() (engine.Deps, error) {
	b := s.cfg.Bastion
	tok, err := mainclient.ReadTokenFile(b.Main.TokenFile)
	if err != nil {
		s.bm.TokenReloads.WithLabelValues(bmetrics.ReloadRefused).Inc()
		return engine.Deps{}, fmt.Errorf("bastion.main.token_file %s: %w", b.Main.TokenFile, err)
	}
	s.bm.TokenReloads.WithLabelValues(bmetrics.ReloadLoaded).Inc()
	s.log.Info("bastion token loaded", "hint", tok.Hint)
	var signer *cache.Signer
	if b.SigningKeyFile != "" {
		key, err := os.ReadFile(b.SigningKeyFile)
		if err != nil {
			return engine.Deps{}, fmt.Errorf("bastion.signing_key_file: %w", err)
		}
		if signer, err = cache.NewSigner(key); err != nil {
			return engine.Deps{}, fmt.Errorf("bastion.signing_key_file %s: %w", b.SigningKeyFile, err)
		}
	}
	s.exportConstants()
	st, g, err := s.openShared(signer)
	if err != nil {
		return engine.Deps{}, err
	}
	c, err := cache.New(context.Background(), b.Cache, signer, cache.Options{
		Metrics: s.bm, Logger: s.log.With("component", "cache"), TempDir: os.TempDir(),
	})
	if err != nil {
		return engine.Deps{}, fmt.Errorf("bastion.cache: %w", err)
	}
	s.holder = mainclient.NewHolder(tok)
	client, err := mainclient.New(mainclient.Options{
		URL: b.Main.URL, PackagesURL: b.Main.PackagesURL, BaseURL: s.cfg.Server.BaseURL, Replica: s.instance, Release: buildinfo.Get().Version, Tokens: s.holder,
		Budget: mainclient.NewBudget(mainclient.BudgetOptions{
			MaxConcurrent: b.Main.MaxConcurrent, Replicas: b.Replicas, AnonymousShare: b.Main.AnonymousShare,
			QueueTimeout: b.Main.QueueTimeout, Metrics: s.bm,
		}),
		ConnectTimeout: b.Main.ConnectTimeout, ResponseHeaderTimeout: b.Main.ResponseHeaderTimeout, CAFile: b.Main.CAFile,
		Metrics: s.bm, Log: s.log, Tracer: s.tracer, Obs: s.metrics, TLSMetrics: s.tlsm,
	})
	if err != nil {
		return engine.Deps{}, fmt.Errorf("bastion.main: %w", err)
	}
	d := engine.Deps{
		Config: s.cfg, Log: s.log.With("component", "engine"), Metrics: s.bm, Replica: s.instance, Release: buildinfo.Get().Version,
		Main: client, Cache: c, Shared: st, Guard: g,
	}
	if s.o.Deps != nil {
		s.o.Deps(&d)
	}
	s.wireLink(&d)
	return d, nil
}

// wireLink builds the link over the client the dependencies hold, wraps that client with the link's guard (a
// refused token or an interface mismatch in any answer makes the replica unreachable, a failing forward
// alone does not, Q-BAS-44), and builds the token reloader and the download buffer (unless a test replaced
// it). The buffer follows recordDownloads of the handshake and reports while the link is reachable.
func (s *Server) wireLink(d *engine.Deps) {
	b := s.cfg.Bastion
	s.link = link.New(link.Options{Client: d.Main, Config: link.ConfigFrom(s.cfg), Metrics: s.bm, Log: s.log, Tokens: s.holder, Now: d.Now})
	d.Main = s.link.Guard(d.Main)
	d.Link = s.link
	s.reloader = mainclient.NewReloader(mainclient.ReloaderOptions{
		Path: b.Main.TokenFile, Interval: b.Main.TokenReloadInterval, Holder: s.holder, Metrics: s.bm,
		Log: s.log.With("component", "token"), OnChange: func(mainclient.Token) { s.link.TokenChanged() }, Now: d.Now,
	})
	if d.Downloads == nil {
		o := downloads.ReporterOptions{
			Config: b.Audit, Replica: s.instance, Sender: d.Main, Reachable: s.link.Reachable, Metrics: s.bm, Log: s.log, Now: d.Now,
			Spool: d.Cache.Backend(),
		}
		if d.Shared != nil && d.Shared.Flights != nil {
			o.Claim = d.Shared.Flights.TryAcquire
		}
		d.Downloads = downloads.NewReporter(o)
	}
	buf := d.Downloads
	s.link.Watch(func(st link.State) { buf.SetEnabled(st.Handshaken && st.RecordDownloads) })
}

// exportConstants sets the gauges that are configuration (R-BAS-040, R-BAS-041): the periods the alert rules
// read, the warning threshold and the Bastion's own interface version.
func (s *Server) exportConstants() {
	b := s.cfg.Bastion
	s.bm.GracePeriod.Set(b.Answers.GracePeriod.Seconds())
	s.bm.AnswerPeriod.Set(b.Answers.Period.Seconds())
	s.bm.TokenWarnBefore.Set(b.Token.WarnBefore.Seconds())
	s.bm.InterfaceVersion.WithLabelValues(bmetrics.SideBastion).Set(iface.InterfaceVersion)
}

// startTLS builds the TLS of the two listeners (R-BAS-044, R-ARCH-035): one reloader per listener, the ops
// listener inheriting the client listener's pairs by default. A pair that does not load holds the first
// readiness, the listener binds and refuses handshakes until the files are valid; a later rotation that does
// not load keeps the previous pair and only counts and logs.
func (s *Server) startTLS() error {
	sec := s.cfg.ServerSection()
	var err error
	if s.opsTLS, err = httpserver.NewListenerTLS("ops", sec.OpsListenerTLS(), s.tlsOptions("ops")...); err != nil {
		return err
	}
	if s.opsTLS != nil {
		s.opsTLS.UseTemporaryCertificate(s.instance, s.tlsm, s.log.With("component", "tls", "listener", "ops"))
	}
	if s.clientTLS, err = httpserver.NewListenerTLS("client", sec.TLS, s.tlsOptions("client")...); err != nil {
		return err
	}
	return nil
}

func (s *Server) tlsOptions(listener string) []tlsfiles.Option {
	return []tlsfiles.Option{tlsfiles.WithMetrics(s.tlsm), tlsfiles.WithLogger(s.log.With("component", "tls", "listener", listener))}
}

// Health returns the readiness registry.
func (s *Server) Health() *health.Health { return s.health }

// Engine returns the request flow; tests use it.
func (s *Server) Engine() engine.Engine { return s.engine }

// ClientAddr returns the bound address of the client listener; nil before Start.
func (s *Server) ClientAddr() net.Addr {
	if s.client == nil {
		return nil
	}
	return s.client.Addr()
}

// OpsAddr returns the bound address of the ops listener; nil before Start.
func (s *Server) OpsAddr() net.Addr {
	if s.ops == nil {
		return nil
	}
	return s.ops.Addr()
}

// Start binds the listeners and serves. It returns once both are bound and the replica is ready; a failure of
// a listener afterwards arrives on the channel Run watches.
func (s *Server) Start(ctx context.Context) error {
	s.log.Info("starting", "role", config.BastionRole, "instance", s.instance, "interface", iface.InterfaceVersion)
	s.health.AddReadiness("startup", func(context.Context) error {
		if !s.started.Load() {
			return health.WithReason(errStarting, "starting")
		}
		return nil
	}, false)
	s.addTLSReadiness()
	// The first check of the cache backend gates the first readiness; a later failure is reported, never unready
	// (R-BAS-043, R-BAS-030). The main installation gates nothing (Q-BAS-44).
	s.health.AddReadiness("cache", func(ctx context.Context) error {
		return health.WithReason(s.engine.Ready(ctx), "cache backend check failed")
	}, true)

	opsHandler := httpserver.OpsHandler(httpserver.OpsOptions{
		Live: s.health.LiveHandler(), Ready: s.health.ReadyHandler(), Metrics: s.metrics.Handler(),
		AccessLog: observability.RequestLog(s.log, s.metrics, observability.WithListener("ops"), observability.WithRequestRecords(s.cfg.Log.Request.OpsListener)),
		Log:       s.log,
	})
	var err error
	if s.ops, err = s.newServer("ops", s.cfg.Server.OpsListen, opsHandler, s.opsTLS); err != nil {
		return err
	}
	if err := s.ops.Bind(); err != nil {
		return err
	}
	s.serve(s.ops)
	hctx, hcancel := context.WithCancel(context.WithoutCancel(ctx))
	s.healthStop = hcancel
	s.bg.Add(1)
	go func() { defer s.bg.Done(); s.health.Run(hctx) }()
	s.log.Info("ops listener bound", "addr", s.ops.Addr().String())

	handler, err := s.clientHandler()
	if err != nil {
		return err
	}
	if s.client, err = s.newServer("client", s.cfg.Server.Listen, handler, s.clientTLS); err != nil {
		return err
	}
	ectx, ecancel := context.WithCancel(context.WithoutCancel(ctx))
	s.engineStop = ecancel
	if err := s.engine.Start(ectx); err != nil {
		return fmt.Errorf("engine: %w", err)
	}
	if err := s.client.Bind(); err != nil {
		return err
	}
	s.serve(s.client)
	s.log.Info("client listener bound", "addr", s.client.Addr().String())
	s.started.Store(true)
	s.runLink(ctx)
	// The start-up check turned true: first readiness does not wait for the next tick.
	s.health.Refresh(ctx)
	return nil
}

// runLink starts the loops of the pieces the engine uses: the link (handshake, feed), the token reloader, the
// download buffer and the cache sweeper, which takes the sweep lease from the shared state (R-BAS-031). They do
// not gate readiness (R-BAS-043).
func (s *Server) runLink(ctx context.Context) {
	lctx, lcancel := context.WithCancel(context.WithoutCancel(ctx))
	s.linkStop = lcancel
	rctx, rcancel := context.WithCancel(context.WithoutCancel(ctx))
	s.reportStop = rcancel
	cctx, ccancel := context.WithCancel(context.WithoutCancel(ctx))
	s.sweepStop = ccancel
	s.bg.Add(4)
	go func() { defer s.bg.Done(); s.link.Run(lctx) }()
	go func() { defer s.bg.Done(); s.reloader.Run(lctx) }()
	go func() { defer s.bg.Done(); s.deps.Cache.RunSweeper(cctx, s.deps.Shared) }()
	go func() {
		defer s.bg.Done()
		if err := s.deps.Downloads.Run(rctx); err != nil {
			s.log.Warn("download buffer stopped", "error", err)
		}
	}()
}

func (s *Server) addTLSReadiness() {
	type named struct {
		name string
		l    *httpserver.ListenerTLS
	}
	listeners := []named{{"ops", s.opsTLS}, {"client", s.clientTLS}}
	have := false
	for _, x := range listeners {
		have = have || x.l != nil
	}
	if !have {
		return
	}
	s.health.AddReadiness("tls", func(context.Context) error {
		var errs []error
		for _, x := range listeners {
			if x.l == nil {
				continue
			}
			if err := x.l.Loaded(); err != nil {
				errs = append(errs, fmt.Errorf("%s listener: %w", x.name, err))
			}
		}
		return health.ConfigError(errors.Join(errs...), "certificate")
	}, true)
}

func (s *Server) newServer(name, addr string, h http.Handler, tls *httpserver.ListenerTLS) (*httpserver.Server, error) {
	sec := s.cfg.ServerSection()
	return httpserver.NewServer(httpserver.ServerConfig{
		Name: name, Addr: addr, Handler: h, TLS: tls, Log: s.log.With("component", "http", "listener", name),
		ReadHeaderTimeout: sec.ReadHeaderTimeout, IdleTimeout: sec.IdleTimeout, MaxHeaderBytes: sec.MaxHeaderBytes,
		StallTimeout: sec.TransferStallTimeout, OnTransferCut: s.metrics.TransferCut,
	})
}

func (s *Server) serve(srv *httpserver.Server) {
	s.bg.Add(1)
	go func() {
		defer s.bg.Done()
		if err := srv.Serve(); err != nil {
			select {
			case s.fatal <- err:
			default:
			}
		}
	}()
}

// clientHandler is the chain of the client listener: panic recovery, trace context, request log and metrics,
// the in-flight limit, the client address (R-ARCH-005 steps 1 to 5), then the engine for every path. The
// engine decides by the path itself (R-BAS-018), so it is the router's fallback and the handler of the paths
// ServeMux would redirect. There is no authentication stage and no rate-limit stage: the engine's guard owns
// both (R-BAS-019, R-BAS-035).
func (s *Server) clientHandler() (http.Handler, error) {
	sec := s.cfg.ServerSection()
	router := httpserver.NewOpsRouter()
	router.SetFallback(s.engine)
	router.HandleUncleaned("/", s.engine)
	resolver, err := httpserver.NewResolver(sec.TrustedProxies, sec.BaseURL, nil,
		httpserver.WithForwardedHeader(httpserver.ForwardedHeader(sec.ForwardedHeader)))
	if err != nil {
		return nil, fmt.Errorf("server.trusted_proxies: %w", err)
	}
	return httpserver.Chain{
		Router: router, Log: s.log, Trace: s.tracer.MiddlewareFor(""), TraceID: observability.RequestID, AccessLog: s.requestLog(),
		OnRequest:   func(r *http.Request) { observability.RequestInfoFrom(r.Context()).SetHandler("bastion") },
		MaxInFlight: sec.MaxInFlight, Resolver: resolver, Annotate: annotate,
		Replica: s.instance, ReplicaHeader: httpserver.ReplicaNever, Draining: s.health.Draining,
	}.Handler(), nil
}

// requestLog is the request log of the client listener. A request the guard refused as quiet (an address on
// the block list, R-BAS-037) is counted in the metrics but not logged: the guard logs one line per block, not
// one per refused request. The engine marks the request in a slot this stage puts into the context; the
// logger below skips the record when the slot says so.
func (s *Server) requestLog() func(http.Handler) http.Handler {
	logger := slog.New(quietHandler{s.log.Handler()})
	reqLog := observability.RequestLog(logger, s.metrics, observability.WithListener("client"),
		observability.WithRequestRecords(s.cfg.Log.Request.Enabled))
	return func(next http.Handler) http.Handler {
		inner := reqLog(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			inner.ServeHTTP(w, r.WithContext(engine.WithQuietSlot(r.Context())))
		})
	}
}

// quietHandler drops the records of requests the engine marked as quiet.
type quietHandler struct{ slog.Handler }

// Handle implements [slog.Handler].
func (h quietHandler) Handle(ctx context.Context, r slog.Record) error {
	if engine.Quiet(ctx) {
		return nil
	}
	return h.Handler.Handle(ctx, r)
}

// WithAttrs implements [slog.Handler].
func (h quietHandler) WithAttrs(a []slog.Attr) slog.Handler {
	return quietHandler{h.Handler.WithAttrs(a)}
}

// WithGroup implements [slog.Handler].
func (h quietHandler) WithGroup(n string) slog.Handler { return quietHandler{h.Handler.WithGroup(n)} }

// annotate records the resolved client address and the effective host for the request log.
func annotate(r *http.Request, info httpserver.RequestInfo) {
	ri := observability.RequestInfoFrom(r.Context())
	if info.Client.IsValid() {
		ri.SetRemoteAddr(info.Client.String())
	}
	ri.SetHost(info.Host)
}

func (s *Server) abort() {
	for _, stop := range []context.CancelFunc{s.engineStop, s.sweepStop, s.reportStop, s.linkStop} {
		if stop != nil {
			stop()
		}
	}
	if s.healthStop != nil {
		s.healthStop()
	}
	for _, srv := range []*httpserver.Server{s.client, s.ops} {
		if srv != nil {
			_ = srv.Close()
		}
	}
	s.bg.Wait()
	s.closeAll()
}

// Shutdown is the drain of R-BAS-043 and R-ARCH-024: readiness off, the drain delay, the client listener shuts
// down by the deadline, the engine drains, the ops listener last.
func (s *Server) Shutdown(ctx context.Context) error {
	var errsOut []error
	start := time.Now()
	s.log.Info("shutting down")
	s.health.SetDraining()
	sec := s.cfg.ServerSection()
	drain := config.BastionShutdownDrainDelay
	if s.o.DrainDelay > 0 {
		drain = s.o.DrainDelay
	}
	deadline := start.Add(drain + sec.ShutdownTimeout)
	if s.client != nil {
		s.log.Info("draining", "delay", drain.String())
		sleep(ctx, drain)
		sctx, cancel := context.WithDeadline(ctx, deadline)
		if err := s.client.ShutdownBy(sctx, deadline.Add(httpserver.DefaultCommitGrace)); err != nil {
			s.log.Warn("client listener", "error", err)
		}
		cancel()
	}
	if s.engineStop != nil {
		s.engineStop()
	}
	if s.sweepStop != nil {
		s.sweepStop()
	}
	if s.reportStop != nil {
		s.reportStop() // the flush loop stops; Drain sends what is left
	}
	dctx, dcancel := context.WithDeadline(ctx, deadline.Add(config.ShutdownMargin/2))
	if err := s.engine.Drain(dctx); err != nil {
		errsOut = append(errsOut, fmt.Errorf("drain engine: %w", err))
	}
	// The buffer is sent or spooled after the last request that could add to it (R-BAS-039).
	if err := s.deps.Downloads.Drain(dctx); err != nil {
		errsOut = append(errsOut, fmt.Errorf("drain downloads: %w", err))
	}
	dcancel()
	if s.linkStop != nil {
		s.linkStop()
	}
	octx, ocancel := context.WithTimeout(ctx, config.ShutdownMargin/2)
	if s.ops != nil {
		_ = s.ops.Shutdown(octx)
	}
	ocancel()
	if s.healthStop != nil {
		s.healthStop()
	}
	s.bg.Wait()
	s.closeAll()
	_ = s.tracer.Shutdown(ctx)
	s.log.Info("stopped", "took", time.Since(start).Round(time.Millisecond).String())
	return errors.Join(errsOut...)
}

// sleep waits for d or the end of ctx.
func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}
