package virtual_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/virtual"
	vt "github.com/guided-traffic/trove/internal/virtual/virtualtest"
)

// The numbering of the repositories matches the order of the specs of the decision point.
const (
	idLocal   = 1
	idLocal2  = 2
	idRemote  = 3
	idRemote2 = 4
)

var (
	local   = vt.LocalRepo(idLocal, "npm-local")
	local2  = vt.LocalRepo(idLocal2, "npm-local2")
	remote  = vt.RemoteRepo(idRemote, "npm-remote")
	remote2 = vt.RemoteRepo(idRemote2, "npm-remote2")
)

// world is one replica: the engine over the fakes, a real decision point, and the repositories.
type world struct {
	t      *testing.T
	rows   *vt.Rows
	remote *vt.Remote
	router *vt.Router
	arts   *vt.Artifacts
	merger *vt.Merger
	authz  *authz.Engine
	caches *cache.Registry
	reg    *prometheus.Registry
	sig    *signals
	r      *virtual.Resolver
	// v is the virtual repository npm-all over the local and the remote members, in this order.
	v repository.Repository
	// now is the engine's clock.
	now time.Time
	mu  sync.Mutex
}

// specs: the four members and the virtual repository that holds them.
func specs(fill string) []vt.RepoSpec {
	return []vt.RepoSpec{
		{Key: "npm-local", Class: "local"},
		{Key: "npm-local2", Class: "local"},
		{Key: "npm-remote", Class: "remote", Fill: fill},
		{Key: "npm-remote2", Class: "remote"},
		{Key: "npm-all", Class: "virtual", Members: []string{"npm-local", "npm-remote"}, DeployTarget: "npm-local"},
	}
}

type signals struct {
	mu sync.Mutex
	h  cluster.SignalHandler
}

func (s *signals) Subscribe(_ context.Context, _ string, h cluster.SignalHandler) (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.h = h
	return func() {}, nil
}

func (s *signals) deliver(payloads []string, coarse bool) {
	s.mu.Lock()
	h := s.h
	s.mu.Unlock()
	h(payloads, coarse)
}

// grants: alice reads everything; bob only npm-all and npm-local; carol reads everything and may
// deploy into the frozen remote; dave reads npm-all, npm-local and npm-remote.
func grants() []vt.Grant {
	everything := []string{"npm-all", "npm-local", "npm-local2", "npm-remote", "npm-remote2"}
	return []vt.Grant{
		{Repos: everything, Principal: vt.Alice, Actions: []string{"read"}},
		{Repos: []string{"npm-all", "npm-local"}, Principal: vt.Bob, Actions: []string{"read"}},
		{Repos: everything, Principal: vt.Carol, Actions: []string{"read"}},
		{Repos: []string{"npm-remote"}, Principal: vt.Carol, Actions: []string{"deploy"}},
		{Repos: []string{"npm-all", "npm-local", "npm-remote"}, Principal: "user-dave", Actions: []string{"read"}},
	}
}

type worldOpts struct {
	fill       string
	members    []repository.Repository
	ttl        int
	cacheBytes int64
	rows       *vt.Rows
	remote     *vt.Remote
	signals    bool
	grants     []vt.Grant
	repos      []vt.RepoSpec
	// >>> WP2.9 exposure
	fillRefused func(endpoint string)
	// <<< WP2.9 exposure
}

func newWorld(t *testing.T, mods ...func(*worldOpts)) *world {
	t.Helper()
	o := worldOpts{fill: "read", members: []repository.Repository{local, remote}, ttl: 7200, cacheBytes: 1 << 20}
	for _, m := range mods {
		m(&o)
	}
	if o.rows == nil {
		o.rows = vt.NewRows()
	}
	if o.remote == nil {
		o.remote = vt.NewRemote()
	}
	w := &world{
		t: t, rows: o.rows, remote: o.remote, router: &vt.Router{}, arts: vt.NewArtifacts(), merger: vt.NewMerger(o.rows),
		now: time.Now(), reg: prometheus.NewRegistry(),
	}
	w.remote.Register(remote, remote2)
	var err error
	if w.caches, err = cache.NewRegistry(nil, nil); err != nil {
		t.Fatal(err)
	}
	gs, rs := grants(), specs(o.fill)
	if o.grants != nil {
		gs = o.grants
	}
	if o.repos != nil {
		rs = o.repos
	}
	w.authz = vt.NewDecider(t, rs, gs)
	target := local
	w.v = vt.VirtualRepo(5, "npm-all", o.ttl, &target, o.members...)
	opts := virtual.Options{
		Router: w.router, Decider: w.authz, Locals: w.rows, Remotes: w.remote, Caches: w.caches, CacheBytes: o.cacheBytes,
		Metrics: w.reg, Now: func() time.Time { w.mu.Lock(); defer w.mu.Unlock(); return w.now },
		// >>> WP2.9 exposure
		FillRefused: o.fillRefused,
		// <<< WP2.9 exposure
	}
	if o.signals {
		w.sig = &signals{}
		opts.Signals = w.sig
	}
	if w.r, err = virtual.New(opts); err != nil {
		t.Fatal(err)
	}
	if o.signals {
		if err := w.r.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(w.r.Stop)
	}
	return w
}

// advance moves the engine's clock.
func (w *world) advance(d time.Duration) {
	w.mu.Lock()
	w.now = w.now.Add(d)
	w.mu.Unlock()
}

func (w *world) caller(sub authz.Subject) virtual.Caller {
	w.t.Helper()
	idx, err := w.authz.Begin(context.Background(), sub, "npm-all")
	if err != nil {
		w.t.Fatal(err)
	}
	return virtual.Caller{Subject: sub, Index: idx}
}

func metadataOf(pkg string) virtual.Descriptor {
	return virtual.Descriptor{Format: vt.Format, Path: pkg, Kind: catalog.KindMetadata, Package: pkg}
}

func artifactOf(pkg, version string) virtual.Descriptor {
	return virtual.Descriptor{Format: vt.Format, Path: pkg + "/-/" + version + ".tgz", Kind: catalog.KindArtifact, Package: pkg, Version: version}
}

func (w *world) plan(sub authz.Subject, d virtual.Descriptor) (virtual.Caller, *virtual.Plan) {
	w.t.Helper()
	c := w.caller(sub)
	p, err := w.r.Plan(context.Background(), c, w.v, d)
	if err != nil {
		w.t.Fatal(err)
	}
	return c, p
}

// metadata resolves a metadata path for a subject.
func (w *world) metadata(sub authz.Subject, pkg string) (*virtual.Merged, error) {
	c, p := w.plan(sub, metadataOf(pkg))
	return w.r.Metadata(context.Background(), c, p, w.merger)
}

// artifact resolves an artifact path for a subject and returns the recorder.
func (w *world) artifact(sub authz.Subject, pkg, version string) (*httptest.ResponseRecorder, error) {
	c, p := w.plan(sub, artifactOf(pkg, version))
	rec := httptest.NewRecorder()
	err := w.r.Artifact(context.Background(), c, p, w.arts, rec)
	return rec, err
}

func kind(err error) (errs.Kind, string) {
	if e, ok := errs.As(err); ok {
		return e.Kind, e.Code
	}
	return errs.Internal, ""
}

var alice, bob, carol = subjectFor(vt.Alice), subjectFor(vt.Bob), subjectFor(vt.Carol)

func future(w *world, d time.Duration) time.Time { return w.now.Add(d) }

func counter(t *testing.T, reg *prometheus.Registry, name string, labels map[string]string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
	next:
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if want, ok := labels[l.GetName()]; ok && want != l.GetValue() {
					continue next
				}
			}
			return m.GetCounter().GetValue()
		}
	}
	return 0
}

// subjectFor is the subject of a user of the fixtures, authenticated by a bearer token. Tests may build
// one; the architecture test looks at production code only.
func subjectFor(id string) authz.Subject {
	return authz.Subject{Kind: principal.KindUser, ID: id, Name: strings.TrimPrefix(id, "user-"), TokenID: "token-" + id, AuthMethod: "bearer"}
}

func anonymousSubject() authz.Subject { return authz.Subject{Anonymous: true} }
