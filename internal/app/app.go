package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/observability/health"
	"github.com/guided-traffic/trove/internal/outbound"
	"github.com/guided-traffic/trove/internal/secrets"
	"github.com/guided-traffic/trove/internal/webui"
)

var errUnavailable = errors.New("unavailable")

const (
	// quiesceMargin is how long before the shutdown deadline running work is
	// cancelled and released (R-ARCH-024 step 4).
	quiesceMargin = 5 * time.Second
	// closeBudget bounds the flush and the close of the pools after the last
	// request (step 6). It lies inside the 5 s margin of the pod's grace period.
	closeBudget = 3 * time.Second
	// opsShutdownTimeout bounds the stop of the ops listener, which stays up until the end.
	opsShutdownTimeout = time.Second
)

// Options configure a replica.
type Options struct {
	Loaded *config.Loaded
	// Role overrides process.role when set.
	Role string
	// Environ is the process environment; nil means os.Environ.
	Environ []string
	// LogWriter receives the JSON log; nil means os.Stdout.
	LogWriter io.Writer
	// Extra registers more client-listener routes before the route table is
	// checked. Tests and the drain fixture use it; production passes nil.
	Extra func(*httpserver.Router)
	// SkipInfrastructure starts the HTTP shell without PostgreSQL and Valkey. The
	// shell tests use it; a production caller never sets it.
	SkipInfrastructure bool
}

// App is one running replica.
type App struct {
	o    Options
	cfg  *config.Config
	id   Identity
	sub  Subsystems
	log  *slog.Logger
	tele *telemetry

	health    *health.Health
	startedAt time.Time

	ops    *httpserver.Server
	client *httpserver.Server
	router *httpserver.Router

	started   atomic.Bool
	startedCh chan struct{}
	fatal     chan error
	bootDone  chan struct{}
	bootStop  context.CancelFunc

	mu    sync.Mutex
	comps []Component // started, in start order
	db    *postgres.DB
	sysf  *postgres.SystemFacts
	vk    *cluster.Valkey
	ring  *secrets.KeyRing
	out   *outbound.Factory
}

// Run runs one replica of a role until ctx ends, then shuts down in the order of
// R-ARCH-024 and returns. A nil error means a clean exit.
func Run(ctx context.Context, loaded *config.Loaded, role string) error {
	return RunWith(ctx, Options{Loaded: loaded, Role: role})
}

// RunWith is Run with options.
func RunWith(ctx context.Context, o Options) error {
	a, err := New(o)
	if err != nil {
		return err
	}
	if err := a.Start(ctx); err != nil {
		a.abort()
		return err
	}
	var fatal error
	select {
	case <-ctx.Done():
	case fatal = <-a.fatal:
	}
	err = a.Shutdown(context.WithoutCancel(ctx))
	return errors.Join(fatal, err)
}

// New builds the replica without starting anything.
func New(o Options) (*App, error) {
	cfg := o.Loaded.Config
	role := o.Role
	if role == "" {
		role = cfg.Process.Role
	}
	sub, err := SubsystemsFor(role)
	if err != nil {
		return nil, err
	}
	if o.Environ == nil {
		o.Environ = os.Environ()
	}
	id := NewIdentity(o.Environ, role)
	tele, err := newTelemetry(cfg, id, o.LogWriter)
	if err != nil {
		return nil, err
	}
	a := &App{
		o: o, cfg: cfg, id: id, sub: sub, log: tele.log, tele: tele,
		startedCh: make(chan struct{}), fatal: make(chan error, 1), bootDone: make(chan struct{}),
	}
	a.health = health.New(cfg.Health,
		health.WithLogger(a.log.With("component", "health")), health.WithIdentity(id.Instance, role),
		health.WithObserver(tele.checkObserver))
	return a, nil
}

// Started is closed when the start-up check has passed (R-ARCH-024 step 10).
func (a *App) Started() <-chan struct{} { return a.startedCh }

// Health returns the readiness registry.
func (a *App) Health() *health.Health { return a.health }

// ClientAddr returns the bound address of the client listener; nil for a worker.
func (a *App) ClientAddr() net.Addr {
	if a.client == nil {
		return nil
	}
	return a.client.Addr()
}

// OpsAddr returns the bound address of the ops listener.
func (a *App) OpsAddr() net.Addr {
	if a.ops == nil {
		return nil
	}
	return a.ops.Addr()
}

func (a *App) setDB(db *postgres.DB) { a.mu.Lock(); a.db = db; a.mu.Unlock() }
func (a *App) setValkey(v *cluster.Valkey) {
	a.mu.Lock()
	a.vk = v
	a.mu.Unlock()
}
func (a *App) setSystemFacts(f postgres.SystemFacts) { a.mu.Lock(); a.sysf = &f; a.mu.Unlock() }
func (a *App) systemFacts() (postgres.SystemFacts, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sysf == nil {
		return postgres.SystemFacts{}, false
	}
	return *a.sysf, true
}
func (a *App) database() *postgres.DB  { a.mu.Lock(); defer a.mu.Unlock(); return a.db }
func (a *App) valkey() *cluster.Valkey { a.mu.Lock(); defer a.mu.Unlock(); return a.vk }

// Start runs R-ARCH-024 steps 1 to 10. It returns once the ops listener is up
// and the bootstrap runs in the background: a replica that cannot reach
// PostgreSQL stays alive and unready. A failure that retrying cannot fix arrives
// on the fatal channel that RunWith watches.
func (a *App) Start(ctx context.Context) error {
	a.startedAt = time.Now()
	bi := a.id.Role
	a.log.Info("starting", "role", bi, "instance", a.id.Instance)
	if a.id.Role == "all" {
		a.log.Warn("process.role is all: this role exists for local development only", "role", "all")
	}
	a.warnShutdownBudget()

	// Step 2: the ops listener, readiness 503, liveness 200.
	a.health.AddReadiness("startup", func(context.Context) error {
		if !a.started.Load() {
			return health.WithReason(errUnavailable, "starting")
		}
		return nil
	}, false)
	opsHandler := httpserver.OpsHandler(httpserver.OpsOptions{
		Live: a.health.LiveHandler(), Ready: a.health.ReadyHandler(), Metrics: a.metricsHandler(),
		AccessLog: a.tele.opsStages(a.cfg), Log: a.log,
	})
	ops, err := a.newServer("ops", a.cfg.Server.OpsListen, opsHandler)
	if err != nil {
		return err
	}
	if err := ops.Bind(); err != nil {
		return err
	}
	a.ops = ops
	go func() {
		if err := ops.Serve(); err != nil {
			a.fail(err)
		}
	}()
	go a.health.Run(ctx)
	a.log.Info("ops listener bound", "addr", ops.Addr().String())

	bctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	a.bootStop = cancel
	go func() {
		defer close(a.bootDone)
		if err := a.bootstrap(bctx); err != nil && !errors.Is(err, context.Canceled) {
			a.fail(err)
		}
	}()
	return nil
}

func (a *App) fail(err error) {
	select {
	case a.fatal <- err:
	default:
	}
}

// warnShutdownBudget warns when the shutdown budget cannot fit the pod's grace
// period (R-ARCH-024): drain delay + shutdown timeout + 5 s must stay below it.
func (a *App) warnShutdownBudget() {
	grace := a.cfg.Runtime.TerminationGracePeriod
	if grace <= 0 {
		return
	}
	need := a.cfg.Health.ShutdownDrainDelay + a.cfg.Server.ShutdownTimeout + quiesceMargin
	if need > grace {
		a.log.Warn("the shutdown budget exceeds the termination grace period; the kubelet would kill the process mid-drain",
			"drain_delay", a.cfg.Health.ShutdownDrainDelay.String(), "shutdown_timeout", a.cfg.Server.ShutdownTimeout.String(),
			"termination_grace_period", grace.String())
	}
}

// bootstrap runs steps 3 to 10 of R-ARCH-024.
func (a *App) bootstrap(ctx context.Context) error {
	cfg := a.cfg

	// Step 5 (key ring) first: it is a pure configuration check and cheap. A
	// missing or malformed key ring stops the process (R-ARCH-021).
	ring, err := secrets.Load(cfg.Secrets)
	if err != nil {
		return err
	}
	a.ring = ring
	// The outbound factory reports a configuration error through Err; it holds the
	// first readiness back instead of stopping the process.
	a.out = outbound.New(cfg.Proxy.Outbound, outbound.WithTracer(a.tele.tracer), outbound.WithMetrics(a.tele.metrics),
		outbound.WithUpstreamLog(a.log), outbound.WithVersion(a.versionString()))
	a.health.AddReadiness("outbound", func(context.Context) error { return a.out.Err() }, true)

	// Steps 3, 4, 6, 7: the infrastructure.
	infra := !a.o.SkipInfrastructure && !fixtureBuild
	if infra && a.sub.MainPool {
		if err := a.startComponent(ctx, &postgresComponent{a: a}); err != nil {
			return err
		}
	}
	// WIRING POINT (steps 4 and 5): audit_maintain_partitions() and the key-ring
	// canary run here once the audit package exists.
	// WIRING POINT (step 6): the object-store check registers health check
	// "objectStore" (first = !cfg.Health.RequireObjectStore) here, from the blob
	// store package.
	if infra && a.sub.Valkey {
		if err := a.startComponent(ctx, &valkeyComponent{a: a}); err != nil {
			return err
		}
	}
	// WIRING POINT (step 7): first change-log poll, the replica registry row and
	// heartbeat (replica registry package), and the lease pool on worker and all.
	// WIRING POINT (step 8): load snapshots and initialise the registered caches.
	// WIRING POINT (step 9, worker and all): start the job claim loop, the reaper
	// and the scheduler (jobs package). Each is a Component with Drain, Settle,
	// Quiesce and Stop, registered with startComponent.
	secrets.Freeze()
	a.tele.metrics.DBGauges().SetLogger(a.log)
	go a.tele.metrics.DBGauges().Run(ctx)

	// Step 9: the client listener.
	if a.sub.ClientListener {
		if err := a.startClient(); err != nil {
			return err
		}
	}

	// Step 10: the start-up check passes.
	a.started.Store(true)
	a.health.Refresh(ctx)
	close(a.startedCh)
	a.log.Info("started", "ready", a.health.Ready())
	return nil
}

func (a *App) versionString() string { return buildVersion() }

func (a *App) startComponent(ctx context.Context, c Component) error {
	if err := c.Start(ctx); err != nil {
		return fmt.Errorf("%s: %w", c.Name(), err)
	}
	a.mu.Lock()
	a.comps = append(a.comps, c)
	a.mu.Unlock()
	return nil
}

// startClient builds the client router and chain, checks the route table and
// opens the listener (steps 9).
func (a *App) startClient() error {
	cfg := a.cfg
	router := httpserver.NewClientRouter()

	doc, err := v1.LoadDocument(v1.EmbeddedYAML())
	if err != nil {
		return err
	}
	api := v1.New(v1.Options{
		Document: doc, Identity: v1.Identity{Replica: a.id.Instance, Role: a.id.Role},
		Ready: a.health.Ready, PublicVersion: cfg.System.PublicVersion,
		Access:   bootstrapAccess{anonymousRead: cfg.System.BootstrapAnonymousRead},
		Replicas: localReplicas{a: a}, Facts: configFacts{cfg: cfg, a: a},
	})
	api.Register(router)

	ui, err := webui.New(webui.Options{Locales: uiLocales(), DefaultLocale: uiDefaultLocale})
	if err != nil {
		return fmt.Errorf("ui: %w", err)
	}
	router.Handle(webui.MountPath, ui)

	if a.o.Extra != nil {
		a.o.Extra(router)
	}
	registerFixture(router)
	// A route registered around the wrapper is a defect; one registered through it
	// that breaks a rule stops start-up (R-API-002, R-HA-041).
	if err := doc.CheckRoutes(router.Patterns()); err != nil {
		return fmt.Errorf("route table: %w", err)
	}
	if v := httpserver.CheckRouteTable(router.Patterns(), nil); len(v) > 0 {
		return fmt.Errorf("route table: %s", v[0])
	}
	a.router = router

	resolver, err := httpserver.NewResolver(cfg.Server.TrustedProxies, cfg.Server.BaseURL, cfg.Server.AdditionalBaseURLs)
	if err != nil {
		return err
	}
	trace, reqLog := a.tele.clientStages(cfg)
	handler := httpserver.Chain{
		Router: router, Log: a.log, Trace: trace, TraceID: observabilityRequestID, AccessLog: reqLog,
		OnRequest: label, MaxInFlight: cfg.Server.MaxInFlight, Resolver: resolver, Annotate: annotate,
		Authenticate: bootstrapAuthenticate, Replica: a.id.Instance, ReplicaHeader: cfg.Server.ReplicaHeader,
		Draining: a.health.Draining,
	}.Handler()

	srv, err := a.newServer("client", cfg.Server.Listen, handler)
	if err != nil {
		return err
	}
	if err := srv.Bind(); err != nil {
		return err
	}
	a.client = srv
	go func() {
		if err := srv.Serve(); err != nil {
			a.fail(err)
		}
	}()
	a.log.Info("client listener bound", "addr", srv.Addr().String())
	return nil
}

func (a *App) newServer(name, addr string, h httpHandler) (*httpserver.Server, error) {
	s := a.cfg.Server
	var tlsFiles *httpserver.TLSFiles
	if s.TLS.CertFile != "" {
		tlsFiles = &httpserver.TLSFiles{CertFile: s.TLS.CertFile, KeyFile: s.TLS.KeyFile}
	}
	return httpserver.NewServer(httpserver.ServerConfig{
		Name: name, Addr: addr, Handler: h, TLS: tlsFiles, Log: a.log.With("component", "http", "listener", name),
		ReadHeaderTimeout: s.ReadHeaderTimeout, IdleTimeout: s.IdleTimeout, MaxHeaderBytes: s.MaxHeaderBytes,
		StallTimeout: s.TransferStallTimeout, OnTransferCut: a.tele.metrics.TransferCut,
	})
}

// abort tears down a half-started replica after Start failed.
func (a *App) abort() {
	if a.bootStop != nil {
		a.bootStop()
	}
	if a.ops != nil {
		_ = a.ops.Close()
	}
}

// Shutdown is the sequence of R-ARCH-024 for SIGTERM. ctx carries no deadline of
// its own; the budget comes from health.shutdown_drain_delay and
// server.shutdown_timeout.
func (a *App) Shutdown(ctx context.Context) error {
	var errsOut []error
	a.log.Info("shutting down")
	if a.bootStop != nil {
		a.bootStop()
		<-a.bootDone
	}

	// Step 1: readiness off at once; workers stop claiming.
	a.health.SetDraining()
	a.mu.Lock()
	comps := append([]Component(nil), a.comps...)
	a.mu.Unlock()
	for _, c := range comps {
		if d, ok := c.(Drainer); ok {
			d.Drain(ctx)
		}
	}

	// Step 2: a web replica keeps serving for the drain delay; workers have no
	// Service routing to them and skip it.
	if a.client != nil {
		if d := a.cfg.Health.ShutdownDrainDelay; d > 0 {
			a.log.Info("draining", "delay", d.String())
			sleep(ctx, d)
		}
	}

	// Steps 3 and 4: stop accepting, let in-flight requests and running work
	// finish until the deadline; cancel what still runs 5 s before it.
	timeout := a.cfg.Server.ShutdownTimeout
	sctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	quiesce := time.AfterFunc(max(timeout-quiesceMargin, 0), func() {
		for i := len(comps) - 1; i >= 0; i-- {
			if q, ok := comps[i].(Quiescer); ok {
				q.Quiesce(sctx)
			}
		}
	})
	defer quiesce.Stop()
	var wg sync.WaitGroup
	if a.client != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := a.client.Shutdown(sctx); err != nil {
				a.log.Warn("client listener", "error", err)
			}
		}()
	}
	for _, c := range comps {
		if s, ok := c.(Settler); ok {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := s.Settle(sctx); err != nil {
					a.log.Warn("settle", "component", c.Name(), "error", err)
				}
			}()
		}
	}
	wg.Wait()

	// Step 6: flush the buffers, then stop the components in reverse start order
	// (leases, registry row, pools), then the ops listener, last.
	cctx, ccancel := context.WithTimeout(ctx, closeBudget)
	defer ccancel()
	for i := len(comps) - 1; i >= 0; i-- {
		if f, ok := comps[i].(Flusher); ok {
			if err := f.Flush(cctx); err != nil {
				errsOut = append(errsOut, fmt.Errorf("flush %s: %w", comps[i].Name(), err))
			}
		}
	}
	for i := len(comps) - 1; i >= 0; i-- {
		if err := comps[i].Stop(cctx); err != nil {
			errsOut = append(errsOut, fmt.Errorf("stop %s: %w", comps[i].Name(), err))
		}
	}
	if a.ops != nil {
		octx, ocancel := context.WithTimeout(ctx, opsShutdownTimeout)
		_ = a.ops.Shutdown(octx)
		ocancel()
	}
	a.tele.shutdown(cctx)
	a.log.Info("stopped")
	return errors.Join(errsOut...)
}
