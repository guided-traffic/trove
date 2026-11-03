// Package server is the process of the role bastion (R-BAS-001, R-BAS-043, R-BAS-044): the configuration is
// loaded and validated by internal/config, the token file is read, the TLS material of the two listeners is
// loaded and reloaded, the engine is built, and two listeners serve: the client listener, which hands every
// request to the engine, and the ops listener with /healthz, /readyz and /metrics.
//
// It enables none of the main installation's subsystems: no PostgreSQL pool, no object-store client for the
// main installation's blobs, no job runner, no format route table, no management API, no UI, no login
// (R-BAS-001). The archtest rule of R-BAS-001 holds its imports to the platform packages.
//
// Start order (R-BAS-043): the token file, the signing key, the TLS material and the engine's dependencies are
// read when the server is built, so a defect stops startup before a port is bound; then the ops listener, then
// the client listener; readiness follows. The handshake and the feed history run in the engine and do not gate
// readiness. Drain order (R-ARCH-024): readiness off, the drain delay, the client listener shuts down by the
// deadline, the engine drains (downloads sent or spooled), the ops listener last.
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

	"github.com/guided-traffic/trove/internal/bastion/answers"
	"github.com/guided-traffic/trove/internal/bastion/cache"
	"github.com/guided-traffic/trove/internal/bastion/downloads"
	"github.com/guided-traffic/trove/internal/bastion/engine"
	"github.com/guided-traffic/trove/internal/bastion/guard"
	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastion/mainclient"
	bmetrics "github.com/guided-traffic/trove/internal/bastion/metrics"
	"github.com/guided-traffic/trove/internal/bastion/shared"
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
	// Deps lets a test replace what the work packages build; the zero value uses the placeholders of the
	// contract work and the configuration's own values. Production passes nil.
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

	started    atomic.Bool
	healthStop context.CancelFunc
	engineStop context.CancelFunc
	bg         sync.WaitGroup
	fatal      chan error
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
		return nil, err
	}
	if s.engine, err = engine.New(s.deps); err != nil {
		return nil, err
	}
	if err := s.startTLS(); err != nil {
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

// buildDeps reads the token file and the signing key and assembles the engine's dependencies: the placeholders
// of the work packages that are not built, the local shared state and the configuration's own values.
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
	d := engine.Deps{
		Config: s.cfg, Log: s.log.With("component", "engine"), Metrics: s.bm, Replica: s.instance, Release: buildinfo.Get().Version,
		Main: mainclient.Stub{}, Tokens: mainclient.NewHolder(tok), Cache: cache.Stub{}, Signer: signer,
		Shared: shared.NewLocal(b.Replicas, nil), Guard: guard.Stub{}, Downloads: downloads.Stub{}, Feed: answers.NewFeedLog(),
	}
	if s.o.Deps != nil {
		s.o.Deps(&d)
	}
	return d, nil
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
	if stub := s.deps.Stubbed(); len(stub) > 0 {
		s.log.Warn("the Bastion runs with placeholders: it answers every request bastion_not_served", "placeholders", strings.Join(stub, ","))
	}
	s.health.AddReadiness("startup", func(context.Context) error {
		if !s.started.Load() {
			return health.WithReason(errStarting, "starting")
		}
		return nil
	}, false)
	s.addTLSReadiness()
	s.health.AddReadiness("engine", s.engine.Ready, true)

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
	// The start-up check turned true: first readiness does not wait for the next tick.
	s.health.Refresh(ctx)
	return nil
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
	reqLog := observability.RequestLog(s.log, s.metrics, observability.WithListener("client"),
		observability.WithRequestRecords(s.cfg.Log.Request.Enabled))
	return httpserver.Chain{
		Router: router, Log: s.log, Trace: s.tracer.MiddlewareFor(""), TraceID: observability.RequestID, AccessLog: reqLog,
		OnRequest:   func(r *http.Request) { observability.RequestInfoFrom(r.Context()).SetHandler("bastion") },
		MaxInFlight: sec.MaxInFlight, Resolver: resolver, Annotate: annotate,
		Replica: s.instance, ReplicaHeader: httpserver.ReplicaNever, Draining: s.health.Draining,
	}.Handler(), nil
}

// annotate records the resolved client address and the effective host for the request log.
func annotate(r *http.Request, info httpserver.RequestInfo) {
	ri := observability.RequestInfoFrom(r.Context())
	if info.Client.IsValid() {
		ri.SetRemoteAddr(info.Client.String())
	}
	ri.SetHost(info.Host)
}

func (s *Server) abort() {
	if s.engineStop != nil {
		s.engineStop()
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
	dctx, dcancel := context.WithDeadline(ctx, deadline.Add(config.ShutdownMargin/2))
	if err := s.engine.Drain(dctx); err != nil {
		errsOut = append(errsOut, fmt.Errorf("drain engine: %w", err))
	}
	dcancel()
	octx, ocancel := context.WithTimeout(ctx, config.ShutdownMargin/2)
	if s.ops != nil {
		_ = s.ops.Shutdown(octx)
	}
	ocancel()
	if s.healthStop != nil {
		s.healthStop()
	}
	s.bg.Wait()
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
