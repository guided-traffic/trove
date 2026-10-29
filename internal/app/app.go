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
	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/backup/tables"
	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/observability/health"
	"github.com/guided-traffic/trove/internal/outbound"
	"github.com/guided-traffic/trove/internal/secrets"
	"github.com/guided-traffic/trove/internal/webui"
)

var errUnavailable = errors.New("unavailable")

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
	// budget splits the margin after the server phase (shutdown.go); tests scale it.
	budget shutdownBudget
	// warnInterval overrides warnEvery in tests.
	warnInterval time.Duration

	health    *health.Health
	startedAt time.Time

	ops    *httpserver.Server
	client *httpserver.Server
	router *httpserver.Router
	// eps are the client endpoints this process serves, default first (R-EP-002).
	eps []*endpointRuntime
	// opsTLS and clientTLS are the listeners' TLS (nil: plain HTTP).
	opsTLS    *httpserver.ListenerTLS
	clientTLS *httpserver.ListenerTLS

	started   atomic.Bool
	startedCh chan struct{}
	fatal     chan error
	bootDone  chan struct{}
	bootStop  context.CancelFunc
	// bg tracks every goroutine the app starts itself (the check loop, the
	// listeners' Serve, the gauge evaluator); Shutdown waits for them, so nothing
	// logs after it returned. healthStop ends the check loop last of all.
	bg         sync.WaitGroup
	healthStop context.CancelFunc

	mu    sync.Mutex
	comps []Component // started, in start order
	db    *postgres.DB
	sysf  *postgres.SystemFacts
	vk    *cluster.Valkey
	ring  *secrets.KeyRing
	out   *outbound.Factory

	signals *cluster.Signals
	changes *cluster.Changes
	caches  *cache.Registry
	leases  *cluster.Leases
	queue   *jobs.Queue
	blobs   *blob.Store
	gc      *blob.Collector

	// m1 holds the components of the M1 areas (m1.go).
	m1 m1Wiring
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
		o: o, cfg: cfg, id: id, sub: sub, log: tele.log, tele: tele, budget: defaultBudget(),
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
	a.standingWarnings()

	// Step 2: the ops listener, readiness 503, liveness 200.
	a.health.AddReadiness("startup", func(context.Context) error {
		if !a.started.Load() {
			return health.WithReason(errUnavailable, "starting")
		}
		return nil
	}, false)
	if err := a.planEndpoints(); err != nil {
		return err
	}
	if err := a.startTLS(); err != nil {
		return err
	}
	opsHandler := httpserver.OpsHandler(httpserver.OpsOptions{
		Live: a.health.LiveHandler(), Ready: a.health.ReadyHandler(), Metrics: a.metricsHandler(),
		AccessLog: a.tele.opsStages(a.cfg), Log: a.log,
	})
	ops, err := a.newServer("ops", a.cfg.Server.OpsListen, opsHandler, a.opsTLS)
	if err != nil {
		return err
	}
	if err := ops.Bind(); err != nil {
		return err
	}
	a.ops = ops
	a.bg.Add(2)
	go func() {
		defer a.bg.Done()
		if err := ops.Serve(); err != nil {
			a.fail(err)
		}
	}()
	// The check loop outlives the drain (probes keep answering) and ends after the
	// listeners; it is not tied to the caller's context.
	hctx, hcancel := context.WithCancel(context.WithoutCancel(ctx))
	a.healthStop = hcancel
	go func() {
		defer a.bg.Done()
		a.health.Run(hctx)
	}()
	a.bg.Add(1)
	go func() {
		defer a.bg.Done()
		a.warnLoop(hctx, a.warnEvery())
	}()
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
		outbound.WithUpstreamLog(a.log), outbound.WithVersion(a.versionString()),
		outbound.WithLogger(a.log), outbound.WithTLSMetrics(a.tele.tls))
	a.health.AddReadiness("outbound", func(context.Context) error { return a.out.Err() }, true)

	// Steps 3 to 9: the infrastructure, in the order of docs/developer/coordination.md.
	infra := !a.o.SkipInfrastructure && !fixtureBuild
	if infra && a.sub.MainPool {
		if err := a.startComponent(ctx, &postgresComponent{a: a}); err != nil {
			return err
		}
		// Step 4: the audit partitions, then the recorder (a.m1.recorder).
		// >>> M1 audit
		if err := a.startAudit(ctx); err != nil {
			return err
		}
		// <<< M1 audit
		// Step 6: the object store.
		if err := a.startBlob(ctx); err != nil {
			return err
		}
	}
	// Step 7: Valkey in the background, then signals, the change log, the
	// replica registry and, on worker and all, the lease manager and the workers.
	if infra && a.sub.Valkey {
		if err := a.startComponent(ctx, &valkeyComponent{a: a}); err != nil {
			return err
		}
	}
	if infra && a.sub.MainPool {
		if err := a.startCoordination(ctx); err != nil {
			return err
		}
	}
	// WIRING POINT (step 8): load the snapshots of the domain packages and
	// register their caches on a.caches. Each M1 area wires its components inside
	// its own markers, in this order (audit has its own marker at step 4); an
	// area that needs another's component reads it from m1Wiring (m1.go).
	// >>> M1 repository
	if infra && a.sub.MainPool {
		if err := a.wireRepository(); err != nil {
			return err
		}
	}
	// <<< M1 repository
	//
	// >>> M1 identity
	if err := a.wireFailureLimiter(ctx); err != nil {
		return err
	}
	if err := a.wireIdentity(); err != nil {
		return err
	}
	// <<< M1 identity
	//
	// >>> M1 login
	// <<< M1 login
	//
	// >>> M1 authz
	if infra && a.sub.MainPool {
		if err := a.wireAuthz(ctx); err != nil {
			return err
		}
	}
	// <<< M1 authz
	//
	// >>> M1 files
	// <<< M1 files
	//
	// >>> M1 endpoints
	// <<< M1 endpoints
	//
	// >>> M1 api
	if err := a.wireAPI(ctx, infra); err != nil {
		return err
	}
	// <<< M1 api
	// Wiring of the M1 areas ends here.
	a.freezeRegistries()
	if infra && a.sub.MainPool {
		a.registerGauges()
	}
	a.tele.metrics.DBGauges().SetLogger(a.log)
	a.bg.Add(1)
	go func() {
		defer a.bg.Done()
		a.tele.metrics.DBGauges().Run(ctx)
	}()

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

// freezeRegistries closes every start-up registry before the replica serves:
// sealed columns, revision columns and the table registry. The job registry is
// frozen where it is built.
func (a *App) freezeRegistries() {
	secrets.Freeze()
	audit.Freeze()
	meta.FreezeRevisionColumns()
	tables.Freeze()
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

// startClient builds, for every endpoint this process serves, the router of its
// surface and its chain, checks the route table and opens the listener (step 9,
// R-EP-002). Every listener is bound before the replica can become ready.
func (a *App) startClient() error {
	cfg := a.cfg
	doc, err := v1.LoadDocument(v1.EmbeddedYAML())
	if err != nil {
		return err
	}
	ui, err := webui.New(webui.Options{Locales: uiLocales(), DefaultLocale: uiDefaultLocale})
	if err != nil {
		return fmt.Errorf("ui: %w", err)
	}
	for _, rt := range a.eps {
		router := httpserver.NewEndpointRouter(rt.ep)
		api := v1.New(v1.Options{
			Document: doc, Identity: v1.Identity{Replica: a.id.Instance, Role: a.id.Role},
			Ready: a.health.Serving, PublicVersion: cfg.System.PublicVersion,
			Access:   a.access(),
			Replicas: replicaSource{a: a}, Facts: configFacts{cfg: cfg, a: a},
			API: a.apiServices(),
		})
		// The router drops the routes of a family the endpoint's surface does not hold.
		api.Register(router)
		// Routes outside /api/v1/ that M1 areas own: /auth/ (login) and /files/ (files).
		// Register them on router within the endpoint surfaces (CheckSurface); the route table
		// check below stops start-up for a route that breaks a rule.
		// >>> M1 login
		// <<< M1 login
		//
		// >>> M1 files
		// <<< M1 files
		router.Handle(webui.MountPath, ui)
		if a.o.Extra != nil {
			a.o.Extra(router)
		}
		registerFixture(router)
		// A route registered around the wrapper is a defect; one registered through it
		// that breaks a rule stops start-up (R-API-002, R-HA-041). The management API
		// is checked against its document where it is served in full.
		if rt.ep.Serves(config.SurfaceAPI) {
			if err := checkAPIRoutes(doc, router); err != nil {
				return fmt.Errorf("route table of endpoint %s: %w", rt.cfg.Name, err)
			}
		}
		if v := httpserver.CheckRouteTable(router.Patterns(), nil); len(v) > 0 {
			return fmt.Errorf("route table of endpoint %s: %s", rt.cfg.Name, v[0])
		}
		if v := httpserver.CheckSurface(router.Patterns(), rt.ep); len(v) > 0 {
			return fmt.Errorf("route table: %s", v[0])
		}
		if err := a.startEndpoint(rt, router); err != nil {
			return err
		}
		if a.client == nil || rt.cfg.Default {
			a.client, a.router = rt.srv, router
		}
	}
	return nil
}

// checkAPIRoutes compares the management API routes of one endpoint's router with
// the document. The routes the endpoint's surface leaves out (the ci_exchange
// route on a surface without it) count as present: the check is about the
// document and the registration agreeing, not about what this endpoint serves
// (CR2-4).
func checkAPIRoutes(doc *v1.Document, router *httpserver.Router) error {
	return doc.CheckRoutes(append(router.Patterns(), router.Dropped()...))
}

func (a *App) newServer(name, addr string, h httpHandler, tls *httpserver.ListenerTLS) (*httpserver.Server, error) {
	s := a.cfg.Server
	return httpserver.NewServer(httpserver.ServerConfig{
		Name: name, Addr: addr, Handler: h, TLS: tls, Log: a.log.With("component", "http", "listener", name),
		ReadHeaderTimeout: s.ReadHeaderTimeout, IdleTimeout: s.IdleTimeout, MaxHeaderBytes: s.MaxHeaderBytes,
		StallTimeout: s.TransferStallTimeout, OnTransferCut: a.tele.metrics.TransferCut,
	})
}

// abort tears down a half-started replica after Start failed.
func (a *App) abort() {
	if a.bootStop != nil {
		a.bootStop()
	}
	if a.healthStop != nil {
		a.healthStop()
	}
	if a.ops != nil {
		_ = a.ops.Close()
	}
	a.bg.Wait()
}

// Shutdown is the sequence of R-ARCH-024 for SIGTERM. ctx carries no deadline of
// its own; the budget comes from health.shutdown_drain_delay and
// server.shutdown_timeout, which the configuration keeps inside the pod's grace
// period (R-HA-011). Every phase works against one absolute plan (planShutdown):
// the server phase ends at start + drain + timeout, and the commit grace, the
// flush and close of the components and the ops listener share the margin after
// it, so the process exits before the kubelet's SIGKILL whatever a phase does.
func (a *App) Shutdown(ctx context.Context) error {
	var errsOut []error
	start := time.Now()
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
	var drain time.Duration
	if a.client != nil {
		drain = a.cfg.Health.ShutdownDrainDelay
	}
	plan := planShutdown(start, drain, a.cfg.Server.ShutdownTimeout, a.budget)
	if drain > 0 {
		a.log.Info("draining", "delay", drain.String())
		sleep(ctx, drain)
	}

	// Steps 3 and 4: stop accepting, let in-flight requests and running work
	// finish until the deadline; cancel what still runs 5 s before it.
	sctx, cancel := context.WithDeadline(ctx, plan.ServerBy)
	defer cancel()
	quiesce := time.AfterFunc(time.Until(plan.QuiesceAt), func() {
		for i := len(comps) - 1; i >= 0; i-- {
			if q, ok := comps[i].(Quiescer); ok {
				q.Quiesce(sctx)
			}
		}
	})
	defer quiesce.Stop()
	var wg sync.WaitGroup
	for _, srv := range a.clientServers() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := srv.ShutdownBy(sctx, plan.CommitBy); err != nil {
				a.log.Warn("client listener", "error", err)
			}
		}()
	}
	for _, rt := range a.eps {
		a.tele.metrics.EndpointListenerUp(rt.cfg.Name, false)
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

	// Step 6: flush the buffers, then stop the components (signals first, then
	// reverse start order: leases, registry row, change log, Valkey, pools), then
	// the ops listener, last. A component that ignores its context is abandoned
	// shortly after the close deadline, so the exit deadline holds.
	cctx, ccancel := context.WithDeadline(ctx, plan.CloseBy)
	defer ccancel()
	closed := make(chan []error, 1)
	go func() { closed <- a.closeComponents(cctx, comps) }()
	select {
	case e := <-closed:
		errsOut = append(errsOut, e...)
	case <-time.After(time.Until(plan.CloseBy.Add(a.budget.Abandon))):
		a.log.Error("components did not stop before the close deadline; abandoning them to keep the exit deadline")
		errsOut = append(errsOut, errors.New("close phase overran its deadline"))
	}
	octx, ocancel := context.WithDeadline(ctx, plan.ExitBy)
	defer ocancel()
	if a.ops != nil {
		_ = a.ops.Shutdown(octx)
	}
	// The check loop goes last: it waits for the checks that are running. The
	// listeners and the gauge evaluator are done by now; wait for them too, but
	// not past the exit deadline.
	if a.healthStop != nil {
		a.healthStop()
	}
	bgDone := make(chan struct{})
	go func() { a.bg.Wait(); close(bgDone) }()
	select {
	case <-bgDone:
	case <-octx.Done():
		a.log.Warn("background goroutines still running at the exit deadline")
	}
	a.tele.shutdown(octx)
	a.log.Info("stopped", "took", time.Since(start).Round(time.Millisecond).String())
	return errors.Join(errsOut...)
}

// closeComponents flushes, then stops, every component. Signals stop first: the
// pending signals are published before the leases are released (R-ARCH-024 step 6).
func (a *App) closeComponents(ctx context.Context, comps []Component) []error {
	var errsOut []error
	for i := len(comps) - 1; i >= 0; i-- {
		if f, ok := comps[i].(Flusher); ok {
			if err := f.Flush(ctx); err != nil {
				errsOut = append(errsOut, fmt.Errorf("flush %s: %w", comps[i].Name(), err))
			}
		}
	}
	for _, c := range stopOrder(comps) {
		if err := c.Stop(ctx); err != nil {
			errsOut = append(errsOut, fmt.Errorf("stop %s: %w", c.Name(), err))
		}
	}
	return errsOut
}

// stopFirst marks a component that stops before the others, whatever its start
// position (signals).
type stopFirst interface{ stopsFirst() }

// stopOrder is the reverse start order, with the components that stop first in
// front.
func stopOrder(comps []Component) []Component {
	var first, rest []Component
	for i := len(comps) - 1; i >= 0; i-- {
		if _, ok := comps[i].(stopFirst); ok {
			first = append(first, comps[i])
		} else {
			rest = append(rest, comps[i])
		}
	}
	return append(first, rest...)
}

// Blobs returns the blob store; nil in a shell without infrastructure.
func (a *App) Blobs() *blob.Store { a.mu.Lock(); defer a.mu.Unlock(); return a.blobs }

// Queue returns the job queue every role enqueues on; nil in a shell without infrastructure.
func (a *App) Queue() *jobs.Queue { a.mu.Lock(); defer a.mu.Unlock(); return a.queue }

// Caches returns the registry in-process caches register with.
func (a *App) Caches() *cache.Registry { a.mu.Lock(); defer a.mu.Unlock(); return a.caches }

// Changes returns the change log, for the writers of change areas.
func (a *App) Changes() *cluster.Changes { a.mu.Lock(); defer a.mu.Unlock(); return a.changes }

// Signals returns the signal path.
func (a *App) Signals() *cluster.Signals { a.mu.Lock(); defer a.mu.Unlock(); return a.signals }
