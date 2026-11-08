package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/guided-traffic/trove/internal/backup/platform"
	"github.com/guided-traffic/trove/internal/journal"

	docsmanual "github.com/guided-traffic/trove/docs/manual"
	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/registrytoken"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/backup/tables"
	"github.com/guided-traffic/trove/internal/bastionmain"
	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	nugetdata "github.com/guided-traffic/trove/internal/formatdata/nuget"
	ocidata "github.com/guided-traffic/trove/internal/formatdata/oci"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/manual"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/observability/health"
	"github.com/guided-traffic/trove/internal/proxy/invalidation"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/secrets"
	"github.com/guided-traffic/trove/internal/uploads"
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
	// Vuln replaces ports of the vulnerability scanning with fakes; tests only (the integration tests of the download
	// block name the data state and the verdict of a remote miss without the sync and the evaluation). Production passes nil.
	Vuln *VulnOverride
	// Mirror replaces the host, the clock and the jitter of the mirror engine; tests only. Production passes nil.
	Mirror *MirrorOverride
	// DialOverride points the connections of the outbound clients at other addresses (outbound.WithDialOverride): a test
	// reaches a stub as the public registry's host name. Tests only. Production passes nil.
	DialOverride map[string]string
	// Database adds options to the database pools of the replica (postgres.WithQueryTracer): a test counts the transactions
	// a request runs in the replica itself (R-ARCH-027, R-VULN-028 V-5). Tests only. Production passes nil.
	Database []postgres.Option
	// SkipInfrastructure starts the HTTP shell without PostgreSQL and Valkey. The
	// shell tests use it; a production caller never sets it.
	SkipInfrastructure bool
	// UIAssets replaces the UI build embedded in the binary (webui.Options.Assets): the build the
	// replica serves and publishes to the shared asset store. Tests only: two replicas of one test
	// process stand for two releases with different builds (R-HA-020). Production passes nil.
	UIAssets fs.FS
}

// App is one running replica.
type App struct {
	o    Options
	cfg  *config.Config
	id   Identity
	sub  Subsystems
	log  *slog.Logger
	tele *telemetry
	// bastionMx are the metrics of the main installation's side of the Bastion, registered once by
	// bastionMetrics (wire_bastion.go).
	bastionMx     *bastionmain.Metrics
	bastionMxOnce sync.Once
	// backupMx are the families of the backup lane on this replica's registry (wire_backup_gauges.go).
	backupMx backupMetrics
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
	// memGuard is the memory guard every endpoint's chain shares (memory.go).
	memGuard *httpserver.MemoryGuard

	// docOnce, doc and docErr hold the parsed management API document, which the
	// authentication area and the client listener both need (apiDocument).
	docOnce sync.Once
	doc     *v1.Document
	docErr  error

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

	// The components of the infrastructure areas, mirrored here by wireInfra for
	// the domain steps that do not take them as typed inputs yet; leases is set by
	// the worker step for the registry heartbeat.
	signals *cluster.Signals
	changes *cluster.Changes
	caches  *cache.Registry
	leases  *cluster.Leases
	queue   *jobs.Queue
	blobs   *blob.Store
	// uploads is the upload session service (R-STOR-011), built with the blob store and handed to the format host.
	uploads *uploads.Service

	// plan is the wiring steps this role runs (steps.go), checked in New.
	plan []step
	// parts are the results of the wiring steps by type (steps.go). The bootstrap
	// goroutine fills it before the listeners open; everything that serves a
	// request, and the accessors after Started, only reads it.
	parts *wiring
	// The services that job handlers use are late values: the job registry is
	// built, and frozen, before the area that owns the service is wired.
	auditStore  late[*audit.Store]
	gc          late[*blob.Collector]
	repoSvc     late[*repository.Service]
	itemStore   late[*items.Store]
	tokenSvc    late[*token.Service]
	folder      late[*invalidation.Folder]
	reseal      late[resealDeps]
	host        late[formats.Host]
	semver2     late[*nugetdata.Semver2]
	ociSweep    late[*ocidata.Sweeper]
	shipper     late[journal.Shipper]
	checkBucket late[platform.Bucket]
	// registryMetrics are the counters of the registry token service, shared by every endpoint (wire_registry.go).
	registryMetrics *registrytoken.Metrics
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
	if o.SkipInfrastructure || fixtureBuild {
		sub = sub.shell()
	}
	plan, err := planSteps(wiringSteps(), sub, role)
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
		o: o, cfg: cfg, id: id, sub: sub, plan: plan, parts: &wiring{}, log: tele.log, tele: tele, budget: defaultBudget(),
		startedCh: make(chan struct{}), fatal: make(chan error, 1), bootDone: make(chan struct{}),
	}
	a.health = health.New(cfg.Health,
		health.WithLogger(a.log.With("component", "health")), health.WithIdentity(id.Instance, role),
		health.WithObserver(tele.checkObserver))
	return a, nil
}

// Started is closed when the start-up check has passed (R-ARCH-024 step 10).
func (a *App) Started() <-chan struct{} { return a.startedCh }

// Failed delivers the error that ends a replica whose start-up or run failed
// after Start returned (a bootstrap error such as a wiring step or a route table
// that breaks a rule). RunWith watches it; a caller that drives Start itself
// must too, or it waits for Started forever.
func (a *App) Failed() <-chan error { return a.fatal }

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
	switch a.id.Role {
	case config.RoleAll:
		a.log.Warn("process.role is all: this role exists for local development only", "role", config.RoleAll)
	case config.RoleWeb:
		// R-ARCH-001: web is the bridge of the upgrade across the cut, api and packages in one process.
		a.log.Warn("process.role is web: this role is the bridge of the upgrade to the roles api and packages and is not part of a new installation", "role", config.RoleWeb)
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
	a.memGuard = a.startMemoryGuard(hctx)
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
	// Steps 3 to 7: the key ring first (a configuration check, R-ARCH-021), then
	// the infrastructure in the order of docs/developer/coordination.md: the
	// database, the audit partitions and recorder, the object store, Valkey and
	// the coordination. They are the first steps of the list (steps.go).
	if err := a.runSteps(ctx, a.parts); err != nil {
		return err
	}

	// Step 8, the domain areas, was part of the list as well. Close the start-up
	// registries before the replica serves.
	a.freezeRegistries()
	a.startGauges(ctx)

	// Step 9: the client listener.
	if a.sub.ClientListener {
		if err := a.requireServing(); err != nil {
			return err
		}
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

// startGauges registers the gauges that read PostgreSQL and starts the loop that
// refreshes the database gauges.
func (a *App) startGauges(ctx context.Context) {
	if !a.sub.Shell {
		a.registerGauges()
	}
	a.tele.metrics.DBGauges().SetLogger(a.log)
	a.bg.Add(1)
	go func() {
		defer a.bg.Done()
		a.tele.metrics.DBGauges().Run(ctx)
	}()
}

// requireServing refuses to open the client listener when the areas it serves
// are missing: a replica that serves /files/ without the item store, or the
// management API without the decision point, never becomes ready and answers 503
// on those routes; it stops here and names what is missing. The shell profile has
// none of them by design and serves its stand-ins.
//
// What a role must have follows from the areas it enables (R-ARCH-001): the parts
// every client listener needs are required on every role, the parts of an area
// only where the role enables the area, so that api is not asked for a format
// dispatcher it must not build and packages is not asked for a login handler.
func (a *App) requireServing() error {
	if a.sub.Shell {
		return nil
	}
	w := a.parts
	ins := []input{
		given("authentication stage", part[authArea](w).stage),
		given("decision point", a.decider()),
		given("repository reader", part[repositoryArea](w).reader),
		given("files service", part[filesArea](w).service),
		given("exposure rule service", part[exposureArea](w).rules),
		given("request limits", part[apiArea](w).rateLimit),
		given("audit store", a.auditStoreForAPI()),
		given("invalidation set", part[invalidationArea](w).set),
		given("virtual engine", part[virtualEngineArea](w).engine),
		given("ownership service", part[ownershipArea](w).service),
	}
	for _, p := range []struct {
		area Area
		in   input
	}{
		{AreaLogin, given("login handler", part[loginArea](w).handler)},
		{AreaSessions, given("session store", part[sessionsArea](w).store)},
		{AreaCIExchange, given("CI exchange", part[ciExchangeArea](w).exchanger)},
		{AreaPackagesAPI, given("package services", part[packagesAPIArea](w).services.View)},
		{AreaVulnAPI, given("vulnerability report", part[vulnAPIArea](w).services.Report)},
		{AreaFormatHost, given("format dispatcher", part[formatHostArea](w).disp)},
		{AreaProxyEngine, given("remote engine", part[proxyEngineArea](w).engine)},
	} {
		if a.sub.Has(p.area) {
			ins = append(ins, p.in)
		}
	}
	return requireInputs("client listener", ins...)
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

// apiDocument parses the management API document once per process: the
// authentication area reads the public operations from it and the client listener
// checks its route table against it, and parsing it is a large part of the
// start-up (Q-OPS-157). It is not changed after it is loaded.
func (a *App) apiDocument() (*v1.Document, error) {
	a.docOnce.Do(func() { a.doc, a.docErr = v1.LoadDocument(v1.EmbeddedYAML()) })
	return a.doc, a.docErr
}

// startClient builds, for every endpoint this process serves, the router of its
// surface and its chain, checks the route table and opens the listener (step 9,
// R-EP-002). Every listener is bound before the replica can become ready.
func (a *App) startClient() error {
	cfg := a.cfg
	doc, err := a.apiDocument()
	if err != nil {
		return err
	}
	uiLog := a.log.With("component", "ui")
	ui, err := webui.New(webui.Options{
		Locales: uiLocales(), DefaultLocale: uiDefaultLocale, ImageOrigins: cfg.UI.Readme.ExternalImageOrigins,
		// The saved language of a signed-in user beats Accept-Language (R-UI-001).
		Preference: a.uiPreference(),
		Assets:     a.o.UIAssets,
		// A hashed file of the other release of a rolling upgrade (R-HA-020, Q-HA-120).
		Shared: a.uiShared(),
		OnSharedError: func(locale, path string, err error) {
			uiLog.Warn("a hashed file of another release could not be read from the shared asset store; the request is answered 503",
				"file", locale+"/"+path, "error", err)
		},
	})
	if err != nil {
		return fmt.Errorf("ui: %w", err)
	}
	// The manual is the binary's own text; the test of internal/manual loads the same
	// files, so a manual that fails here never passed CI. It is rendered when the first
	// request needs it, not before the replica is ready: rendering its pages is the
	// largest cost of the start-up (Q-OPS-157). All endpoints share the one result.
	help := sync.OnceValues(func() (*manual.Manual, error) {
		m, err := manual.Load(docsmanual.Files)
		if err != nil {
			a.log.Error("the manual could not be loaded; its operations answer 503", "error", err)
		}
		return m, err
	})
	avatars := a.avatarService()
	for _, rt := range a.eps {
		router := httpserver.NewEndpointRouter(rt.ep)
		api := v1.New(v1.Options{
			Document: doc, Identity: v1.Identity{Replica: a.id.Instance, Role: a.id.Role},
			Ready: a.health.Serving, PublicVersion: cfg.System.PublicVersion, Formats: formatNames(),
			Access:   a.access(),
			Replicas: replicaSource{a: a}, Facts: configFacts{cfg: cfg, a: a},
			API:  a.apiServices(),
			Auth: a.authServices(), Avatars: avatars, AuditStore: a.auditStoreForAPI(), Authz: a.authzServices(), CI: a.ciAPI(),
			// The endpoint and exposure-rule operations (R-EP-016, wire_exposure.go).
			Endpoints:    a.endpointServices(),
			Repositories: a.repositoryAPI(), Packages: part[packagesAPIArea](a.parts).services,
			Files: part[filesArea](a.parts).service, FilesDecider: part[filesArea](a.parts).decider,
			Cache:      a.cacheAPI(),
			Resolution: a.resolutionExplainer(),
			LoadManual: help,
			// The operations of the Bastion work answer 501 until their packages are built (wire_bastion.go).
			Bastions: a.bastionRegistrations(),
			Bastion:  a.bastionServices(rt),
			// The vulnerability scanning's operations (wire_vuln_api.go).
			Vuln: part[vulnAPIArea](a.parts).services,
			// The backup runs, the summary and the backup block of the system information (wire_backup_gauges.go).
			Backup: a.backupAPI(),
		})
		// The router drops the routes of a family the endpoint's surface does not hold.
		api.Register(router)
		// Routes outside /api/v1/ that areas own: /auth/ (login) and /files/ (files).
		// Register them on router within the endpoint surfaces (CheckSurface); the route table
		// check below stops start-up for a route that breaks a rule. The shell profile has
		// no login, and neither has the role packages; its /files/ routes have no service
		// and answer 503, because the document lists them.
		if a.sub.Has(AreaLogin) {
			a.registerLogin(router)
		}
		a.registerFiles(router)
		// The package protocols under /api/<format>/ (wire_formats.go).
		if err := a.registerFormats(router); err != nil {
			return err
		}
		// The registry root below /v2/ (wire_registry.go); it sets rt.registry, which the route table check below reads.
		if err := a.registerRegistry(rt, router); err != nil {
			return err
		}
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
		// Below /v2/ the route table admits only what was mounted through the enforcement point of the registry tokens'
		// grants (Q-OCI-56): a handler mounted around it cannot start. Below /.well-known/ it admits the discovery documents
		// formats register for a bound host (R-EP-006); no format registers one yet.
		if v := httpserver.CheckRouter(router, rt.registry.Patterns(), rt.registry.UncleanedPrefixes(), nil); len(v) > 0 {
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
	// finish until the deadline; cancel what still runs 5 s before it, and the job
	// worker early enough for the release writes of its jobs to get their whole
	// settle deadline (Q-ARCH-329).
	sctx, cancel := context.WithDeadline(ctx, plan.ServerBy)
	defer cancel()
	first, rest := quiesceGroups(comps)
	at := plan.QuiesceAt
	if len(first) > 0 {
		at = plan.ReleaseAt
	}
	quiesced := make(chan struct{})
	quiesce := time.AfterFunc(time.Until(at), func() {
		defer close(quiesced)
		for _, q := range first {
			q.Quiesce(sctx)
		}
		if len(first) > 0 && len(rest) > 0 {
			sleep(sctx, time.Until(plan.QuiesceAt))
		}
		for _, q := range rest {
			q.Quiesce(sctx)
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
	// A quiesce that is not due yet never starts; one that has started is waited for, so no
	// handler is being cancelled while the components close, and no more than until the close
	// deadline.
	if !quiesce.Stop() {
		select {
		case <-quiesced:
		case <-time.After(time.Until(plan.CloseBy)):
			a.log.Warn("a quiesce was still running at the close deadline")
		}
	}

	// Step 6: flush the buffers, then stop the components (signals first, then
	// reverse start order: leases, registry row, change log, Valkey, pools), then
	// the ops listener, last. A component that ignores its context is abandoned
	// shortly after the close deadline, so the exit deadline holds.
	cctx, ccancel := context.WithDeadline(ctx, plan.CloseBy)
	defer ccancel()
	closed := make(chan []error, 1)
	var closing atomic.Value // the step the close phase is in, for the overrun's log line
	go func() { closed <- a.closeComponents(cctx, comps, &closing) }()
	select {
	case e := <-closed:
		errsOut = append(errsOut, e...)
	case <-time.After(time.Until(plan.CloseBy.Add(a.budget.Abandon))):
		a.log.Error("components did not stop before the close deadline; abandoning them to keep the exit deadline", "at", closing.Load())
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

// quiesceOrder is the order in which the components that quiesce are asked to (R-ARCH-024 step 4): the job worker first, because its
// handlers run the passes of a mirror through the remote engine and a pass whose engine was cancelled under it would record failures
// while its context is live; then the others in reverse start order (Q-ARCH-318).
func quiesceOrder(comps []Component) []Quiescer {
	first, rest := quiesceGroups(comps)
	return append(first, rest...)
}

// quiesceGroups splits quiesceOrder into the components that quiesce first and the others.
func quiesceGroups(comps []Component) (first, rest []Quiescer) {
	for i := len(comps) - 1; i >= 0; i-- {
		switch q := comps[i].(type) {
		case QuiescesFirst:
			first = append(first, q)
		case Quiescer:
			rest = append(rest, q)
		}
	}
	return first, rest
}

// closeComponents flushes, then stops, every component. Signals stop first: the
// pending signals are published before the leases are released (R-ARCH-024 step 6).
func (a *App) closeComponents(ctx context.Context, comps []Component, closing *atomic.Value) []error {
	var errsOut []error
	for i := len(comps) - 1; i >= 0; i-- {
		if f, ok := comps[i].(Flusher); ok {
			closing.Store("flush " + comps[i].Name())
			if err := f.Flush(ctx); err != nil {
				errsOut = append(errsOut, fmt.Errorf("flush %s: %w", comps[i].Name(), err))
			}
		}
	}
	for _, c := range stopOrder(comps) {
		closing.Store("stop " + c.Name())
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

// Areas returns the wiring areas this replica built, in step order: the planned steps of its role (R-ARCH-001). The
// integration tests of the roles compare it with the role table.
func (a *App) Areas() []string {
	out := make([]string, 0, len(a.plan))
	for _, s := range a.plan {
		out = append(out, string(s.area))
	}
	return out
}

// Queue returns the job queue every role enqueues on; nil in a shell without infrastructure.
func (a *App) Queue() *jobs.Queue { a.mu.Lock(); defer a.mu.Unlock(); return a.queue }

// Caches returns the registry in-process caches register with.
func (a *App) Caches() *cache.Registry { a.mu.Lock(); defer a.mu.Unlock(); return a.caches }

// Changes returns the change log, for the writers of change areas.
func (a *App) Changes() *cluster.Changes { a.mu.Lock(); defer a.mu.Unlock(); return a.changes }

// Repositories returns the replica's repository snapshot reader; nil in the shell
// profile.
func (a *App) Repositories() repository.Reader {
	r := part[repositoryArea](a.parts).reader
	if r == nil { // a nil *SnapshotReader in the interface would not be nil
		return nil
	}
	return r
}

// RepositoryService returns the repository write side; nil in the shell profile.
func (a *App) RepositoryService() *repository.Service { return part[repositoryArea](a.parts).service }

// Authorizer returns the decision point for a caller that has no request, nil in
// the shell profile. The integration tests ask it whether a grant has reached the
// replica.
func (a *App) Authorizer() authz.Authorizer {
	e := part[authzArea](a.parts).engine
	if e == nil {
		return nil
	}
	return e
}

// AuthzService returns the policy and role-binding service; nil in the shell
// profile.
func (a *App) AuthzService() *authz.Service { return part[authzArea](a.parts).service }

// Flush writes the download statistics and the repository activity now, which
// Shutdown does at step 6. A test that reads what a replica buffered calls it
// instead of waiting for the flush interval. The audit buffer is not part of it:
// its writer flushes on its own short interval and stops for good at shutdown.
func (a *App) Flush(ctx context.Context) error {
	a.mu.Lock()
	comps := append([]Component(nil), a.comps...)
	a.mu.Unlock()
	var errsOut []error
	for _, c := range comps {
		if s, ok := c.(interface{ Sync(context.Context) error }); ok {
			if err := s.Sync(ctx); err != nil {
				errsOut = append(errsOut, fmt.Errorf("flush %s: %w", c.Name(), err))
			}
		}
	}
	return errors.Join(errsOut...)
}

// Valkey returns the Valkey client; nil in the shell profile.
func (a *App) Valkey() *cluster.Valkey { return a.valkey() }

// Signals returns the signal path.
func (a *App) Signals() *cluster.Signals { a.mu.Lock(); defer a.mu.Unlock(); return a.signals }
