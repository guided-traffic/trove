//go:build integration

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/api/v1/apitest"
	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/listq"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/secrets"
)

// testAccess decides permissions from the principal the test authenticator
// built: the system capabilities of its token scope. An anonymous caller is 401.
type testAccess struct{}

func (testAccess) Require(ctx context.Context, permission string) error {
	p := httpserver.PrincipalFrom(ctx)
	if p.Anonymous {
		return errs.E(errs.Unauthenticated, errs.CodeUnauthenticated, "credentials required", errs.WithChallenge("Bearer"))
	}
	if p.Credential.Scope != nil {
		for _, c := range p.Credential.Scope.System {
			if c == permission {
				return nil
			}
		}
	}
	return errs.E(errs.Forbidden, errs.CodeForbidden, "missing "+permission)
}

// testAuth reads the caller from headers: X-Test-Principal is "<uuid>[:<token id>]",
// X-Test-Caps the comma-separated capabilities. No header is an anonymous caller.
func testAuth(r *http.Request) (httpserver.Principal, error) {
	id, _, _ := strings.Cut(r.Header.Get("X-Test-Principal"), ":")
	if id == "" {
		return httpserver.AnonymousPrincipal(), nil
	}
	token := id
	if _, t, ok := strings.Cut(r.Header.Get("X-Test-Principal"), ":"); ok {
		token = t
	}
	var caps []string
	if c := r.Header.Get("X-Test-Caps"); c != "" {
		caps = strings.Split(c, ",")
	}
	return httpserver.Principal{
		ID: id, Name: "user-" + id[:8], Kind: httpserver.KindUser,
		Credential: httpserver.Credential{Method: httpserver.MethodBearer, TokenID: token, Scope: &scope.Scope{Inherit: true, System: caps}},
	}, nil
}

// nodeOpts describe one in-process replica.
type nodeOpts struct {
	role    string
	valkey  bool
	limits  *config.APIRateLimits
	liveN   int
	ring    *secrets.KeyRing
	val     *apitest.Validator
	kinds   func(r *jobs.Registry, n *node)
	noServe bool
	// deadValkey gives the replica a Valkey client that never connects (a Sentinel
	// that does not answer, Q-HA-13).
	deadValkey bool
	// repos builds the services of the repository operations once the replica's
	// database exists; nil serves none (those operations answer 503).
	repos func(n *node) v1.RepositoryServices
}

// node is one replica: pools of its own, a job registry and queue, and for the
// roles that serve it a listener with the chain and the handlers.
type node struct {
	t    *testing.T
	e    *env
	name string
	uid  string
	role string
	reg  *prometheus.Registry

	db       *postgres.DB
	vk       *cluster.Valkey
	jobsReg  *jobs.Registry
	queue    *jobs.Queue
	worker   *jobs.Worker
	leases   *cluster.Leases
	registry *cluster.Registry
	signals  *cluster.Signals
	widgets  *widgetAPI
	srv      *httptest.Server
	ls       *cluster.LimiterSet
	api      *v1.Server
}

// newNode builds a replica the way the composition root does: web replicas serve
// and enqueue, worker replicas claim jobs.
func (e *env) newNode(name string, o nodeOpts) *node {
	t := e.t
	t.Helper()
	if o.role == "" {
		o.role = "web"
	}
	n := &node{t: t, e: e, name: name, uid: uuidV4(), role: o.role, reg: prometheus.NewRegistry()}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var err error
	n.db, err = postgres.Open(ctx, e.cfg, name, o.role, quiet)
	if err != nil {
		t.Fatalf("Open(%s): %v", name, err)
	}
	t.Cleanup(n.stop)
	switch {
	case o.valkey:
		n.vk = newValkey(t, e, name)
	case o.deadValkey:
		cfg := e.vcfg
		cfg.Sentinel.Addresses = []string{"127.0.0.1:1"}
		n.vk, err = cluster.NewValkey(ctx, cfg, name, o.role, quiet, prometheus.NewRegistry(), cluster.WithKeyspaceID(e.ks))
		if err != nil {
			t.Fatalf("NewValkey(%s): %v", name, err)
		}
		t.Cleanup(n.vk.Close)
	}
	var bus cluster.Bus
	if n.vk != nil {
		bus = n.vk
	}
	n.signals, err = cluster.NewSignals(cluster.SignalsOptions{Bus: bus, Config: defaultCluster(), Log: quiet, Metrics: n.reg})
	if err != nil {
		t.Fatal(err)
	}

	// The registry is the process registry plus the kinds this test needs.
	n.jobsReg = jobs.NewRegistry()
	for _, k := range jobs.Default.Kinds() {
		spec, _ := jobs.Default.Kind(k)
		n.jobsReg.RegisterKind(k, spec)
	}
	for name, task := range jobs.Default.Tasks() {
		n.jobsReg.RegisterTask(name, task)
	}
	n.registerGC()
	if o.kinds != nil {
		o.kinds(n.jobsReg, n)
	}
	n.jobsReg.Freeze()
	n.queue = jobs.NewQueue(jobs.QueueOptions{DB: n.db.Main(), Registry: n.jobsReg, Signals: n.signals, Log: quiet})

	ring := o.ring
	var keyIDs []string
	active := ""
	if ring != nil {
		keyIDs, active = ring.KeyIDs(), ring.ActiveKeyID()
	} else {
		keyIDs, active = []string{"k1"}, "k1"
	}
	isWorker := o.role == "worker" || o.role == "all"
	if isWorker {
		n.leases, err = cluster.NewLeases(cluster.LeaseOptions{
			Pool: n.db.LeasePool(), Config: defaultCluster(), CheckInterval: time.Second,
			WatchdogTimeout: 30 * time.Second, Log: quiet, Metrics: n.reg,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	n.registry, err = cluster.NewRegistry(cluster.RegistryOptions{
		DB: n.db.ControlPool(),
		Identity: cluster.ReplicaIdentity{
			UID: n.uid, Name: name, Version: "0.1.0", Revision: "it", Role: o.role, StartedAt: time.Now(),
			SecretKeyIDs: keyIDs, ActiveSecretKeyID: active,
		},
		Snapshot: func() cluster.ReplicaSnapshot { return cluster.ReplicaSnapshot{State: cluster.ReplicaReady} },
		Leases:   func() []cluster.HeldInfo { return nil },
		Config:   defaultSystem(), WatchdogTimeout: 30 * time.Second, Log: quiet,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.registry.Start(ctx); err != nil {
		t.Fatalf("registry.Start: %v", err)
	}
	if isWorker {
		n.worker, err = jobs.NewWorker(jobs.WorkerOptions{
			Role: o.role, Instance: name, UID: n.uid, Main: n.db.Main(), Control: n.db.ControlPool(),
			Registry: n.jobsReg, Config: defaultJobs(), Signals: n.signals, Leases: n.leases,
			WatchdogTimeout: 30 * time.Second, Log: quiet, Metrics: n.reg,
		})
		if err != nil {
			t.Fatal(err)
		}
		n.queue = n.worker.Queue()
		if err := n.worker.Start(ctx); err != nil {
			t.Fatalf("worker.Start: %v", err)
		}
		if err := n.leases.Start(ctx); err != nil {
			t.Fatalf("leases.Start: %v", err)
		}
	}
	if o.role != "worker" && !o.noServe {
		n.serve(o, ring)
	}
	return n
}

// registerGC registers the kinds gc and gc-full like the composition root does
// (exclusive, both under the dedup key gc); the handler records the replica that
// ran it, which is what the web-enqueues, worker-executes check reads.
func (n *node) registerGC() {
	handler := func(mode string) jobs.Handler {
		return func(_ context.Context, run *jobs.Run) error {
			var args struct {
				DryRun bool `json:"dryRun"`
			}
			_ = jsonUnmarshal(run.Args, &args)
			return run.SetResult(map[string]any{"mode": mode, "dryRun": args.DryRun, "ranOn": n.name, "candidates": 0, "kept": 0, "deleted": 0, "bytesReclaimed": 0, "errors": 0, "trashPurged": 0})
		}
	}
	for _, k := range []struct{ kind, sched string }{{"gc", "*/30 * * * *"}, {"gc-full", "0 2 * * 0"}} {
		n.jobsReg.RegisterKind(k.kind, jobs.KindSpec{Queue: "maintenance", DedupMode: jobs.Exclusive, Handler: handler(k.kind)})
		n.jobsReg.RegisterTask(k.kind, jobs.TaskSpec{Schedule: k.sched, DedupKey: "gc"})
	}
}

// ringSource reads the rings of the registry, like the composition root.
type ringSource struct{ n *node }

func (s ringSource) Rings(ctx context.Context) ([]v1.ReplicaRing, error) {
	var infos []cluster.ReplicaInfo
	err := s.n.db.Read(ctx, func(c q.DBTX) (err error) {
		infos, err = cluster.ListReplicas(ctx, c, defaultSystem())
		return err
	})
	if err != nil {
		return nil, err
	}
	out := make([]v1.ReplicaRing, 0, len(infos))
	for _, i := range infos {
		out = append(out, v1.ReplicaRing{Name: i.Name, Role: i.Roles[0], Lost: i.Lost, ActiveKeyID: i.ActiveSecretKeyID, KeyIDs: i.SecretKeyIDs})
	}
	return out, nil
}

type replicaLister struct{ n *node }

func (l replicaLister) Replicas(context.Context) ([]gen.Replica, error) { return nil, nil }

type noFacts struct{}

func (noFacts) Facts(context.Context) (v1.Facts, error) { return v1.Facts{}, nil }

// serve starts the listener: the chain around the router with the generated
// handlers and the test resource.
func (n *node) serve(o nodeOpts, ring *secrets.KeyRing) {
	t := n.t
	doc, err := v1.LoadDocument(v1.EmbeddedYAML())
	if err != nil {
		t.Fatal(err)
	}
	cfg := apiConfig()
	if o.limits != nil {
		cfg.RateLimits = *o.limits
	}
	svc := v1.Services{
		Config: cfg, Store: n.db, Queue: n.queue, Jobs: n.jobsReg,
		TaskArgs: map[string]func() any{"gc": func() any {
			return &struct {
				DryRun bool `json:"dryRun"`
			}{}
		}, "gc-full": func() any {
			return &struct {
				DryRun bool `json:"dryRun"`
			}{}
		}},
		KeyRing: ring, Sealed: listq.SealedCounter{DB: n.db.Main()}, Rings: ringSource{n},
	}
	var repos v1.RepositoryServices
	if o.repos != nil {
		repos = o.repos(n)
	}
	n.api = v1.New(v1.Options{
		Document: doc, Identity: v1.Identity{Replica: n.name, Role: n.role}, Ready: func() bool { return true },
		Access: testAccess{}, Replicas: replicaLister{n}, Facts: noFacts{}, API: svc, Repositories: repos,
	})
	router := httpserver.NewClientRouter()
	n.api.Register(router)
	n.widgets = newWidgetAPI(n.db, v1.NewIdempotency(n.db, cfg.IdempotencyRetention), cfg)
	n.widgets.register(router)

	var stage func(http.Handler) http.Handler
	live := func() int { return max(1, o.liveN) }
	n.ls, err = cluster.NewLimiterSet(cluster.LimiterOptions{Valkey: n.vk, Registry: n.reg, LiveReplicas: live})
	if err != nil {
		t.Fatal(err)
	}
	limit, err := v1.NewRateLimit(v1.RateLimitOptions{Config: cfg.RateLimits, Limiters: n.ls, Registry: n.reg})
	if err != nil {
		t.Fatal(err)
	}
	stage = v1.EdgeStage(v1.CORS(nil), limit)

	res, err := httpserver.NewResolver(nil, "http://trove.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	chain := httpserver.Chain{
		Router: router, Resolver: res, Authenticate: testAuth, RateLimit: stage,
		Replica: n.name, ReplicaHeader: httpserver.ReplicaAlways,
	}.Handler()
	h := chain
	if o.val != nil {
		validated := o.val.Handler(t, chain)
		// The test resource is not part of the document.
		h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/v1/widgets") {
				chain.ServeHTTP(w, r)
				return
			}
			validated.ServeHTTP(w, r)
		})
	}
	n.srv = httptest.NewServer(h)
}

func (n *node) stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if n.srv != nil {
		n.srv.Close()
	}
	if n.registry != nil {
		n.registry.Drain(ctx)
	}
	if n.worker != nil {
		n.worker.Drain(ctx)
		sctx, scancel := context.WithTimeout(ctx, time.Second)
		_ = n.worker.Settle(sctx)
		scancel()
		n.worker.Quiesce(ctx)
		_ = n.worker.Stop(ctx)
	}
	if n.leases != nil {
		_ = n.leases.Stop(ctx)
	}
	if n.registry != nil {
		_ = n.registry.Stop(ctx)
	}
	if n.signals != nil {
		n.signals.Close()
	}
	if n.vk != nil {
		n.vk.Close()
	}
	if n.db != nil {
		n.db.Close()
	}
}

func (n *node) url() string { return n.srv.URL }

// cluster is several web replicas behind a round-robin "Service": consecutive
// requests land on different replicas (R-ARCH-032).
type webCluster struct {
	nodes []*node
	next  int
	hdr   map[string]string
}

func newWebCluster(nodes ...*node) *webCluster {
	return &webCluster{nodes: nodes, hdr: map[string]string{}}
}

// do sends the request to the next replica in turn, rewriting the host, and adds
// the caller's headers.
func (c *webCluster) do(req *http.Request) (*http.Response, error) {
	n := c.nodes[c.next%len(c.nodes)]
	c.next++
	path := req.URL.RequestURI()
	out, err := http.NewRequestWithContext(req.Context(), req.Method, n.url()+path, req.Body)
	if err != nil {
		return nil, err
	}
	out.Header = req.Header.Clone()
	for k, v := range c.hdr {
		out.Header.Set(k, v)
	}
	return noKeepAlive.Do(out)
}

var noKeepAlive = &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 30 * time.Second}
