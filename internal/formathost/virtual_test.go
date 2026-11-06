package formathost

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/qname"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/virtual"
	vt "github.com/guided-traffic/trove/internal/virtual/virtualtest"
)

// virtAdapter is the test format's virtual adapter; it records what the bridge hands it.
type virtAdapter struct {
	strategy formats.Strategy
	gotParts []formats.MemberDocument
	// dir is put before the package in the artifact prefix.
	dir    string
	gotVar string
}

func (a *virtAdapter) Strategy(formats.Path) formats.Strategy { return a.strategy }

// ArtifactPrefix: the format's own layout, a directory the test can change.
func (a *virtAdapter) ArtifactPrefix(name string) string { return a.dir + name + "/" }

func (a *virtAdapter) Merge(_ context.Context, _ *formats.Request, _ formats.Path, parts []formats.MemberDocument) ([]byte, error) {
	a.gotParts = parts
	return []byte("merged"), nil
}

func (a *virtAdapter) LocalDocument(_ context.Context, _ *formats.Request, m formats.RepoRef, _ formats.Path, variant string) ([]byte, formats.RevSeq, bool, error) {
	a.gotVar = variant
	return []byte("doc-of-" + m.Name.String()), 7, true, nil
}

// virtFormat is a format with package names whose package document is the package name
// and whose artifacts are <package>/-/<version>.tgz.
type virtFormat struct {
	formats.Base
	a *virtAdapter
	// refuse is the answer of ServeCheck per repository key.
	refuse map[string]error
}

func (f virtFormat) ServeCheck(_ context.Context, _ formats.Host, _ formats.RepoRef, it formats.ItemRef, _ formats.ServeMode) error {
	return f.refuse[it.Repo.Name.Key]
}

func (virtFormat) Descriptor() formats.Descriptor {
	return formats.Descriptor{
		Facts: catalog.Facts{
			ID: "virtfmt", Classes: []formats.Class{formats.Local, formats.Remote, formats.Virtual}, Layout: "virtfmt",
			PatternSubject: formats.SubjectName, PackageNames: true, PinsAndClaims: true,
		},
		ContractVersion: formats.ContractVersion,
	}
}
func (virtFormat) Routes() []formats.Route { return nil }
func (virtFormat) Match(m, p string) (*formats.Route, formats.Params, error) {
	return formats.MatchRoutes(nil, m, p)
}

func (f virtFormat) Virtual() formats.VirtualAdapter { return f.a }

func (virtFormat) Classify(p formats.Path) formats.PathInfo {
	for i := 0; i+3 <= len(p.Path); i++ {
		if p.Path[i:i+3] == "/-/" {
			v := p.Path[i+3:]
			return formats.PathInfo{Kind: formats.Content, Package: p.Path[:i], Version: v[:len(v)-len(".tgz")]}
		}
	}
	return formats.PathInfo{Kind: formats.PathMetadata, Package: p.Path}
}

func (virtFormat) PackageDocumentPath(name string) (formats.Path, bool) {
	return formats.Path{Path: name}, name != ""
}

// fakeEngine records the calls of the bridge.
type fakeEngine struct {
	caller   virtual.Caller
	plan     virtual.Descriptor
	merger   virtual.Merger
	tiers    virtual.Tiers
	shadowed virtual.ShadowOutcome
	local    repository.Repository
	docPath  string
	target   repository.Repository
	targetOK bool
	// plans, when set, is the plan Plan answers.
	plans *virtual.Plan
	// away answers KeepsAway per "<member key>/<term>"; gotAway records the last call.
	away    map[string]bool
	gotAway struct {
		via    *repository.Repository
		member repository.Repository
		term   string
	}
}

func (e *fakeEngine) KeepsAway(_ context.Context, via *repository.Repository, member repository.Repository, term string) (bool, error) {
	e.gotAway.via, e.gotAway.member, e.gotAway.term = via, member, term
	return e.away[member.Name.String()+"/"+term], nil
}

func (e *fakeEngine) Plan(_ context.Context, c virtual.Caller, v repository.Repository, d virtual.Descriptor) (*virtual.Plan, error) {
	e.caller, e.plan = c, d
	if e.plans != nil {
		p := *e.plans
		p.Virtual, p.Descriptor = v, d
		return &p, nil
	}
	return &virtual.Plan{Virtual: v, Descriptor: d, Admitted: true}, nil
}

func (e *fakeEngine) Artifact(ctx context.Context, _ virtual.Caller, p *virtual.Plan, t virtual.Tiers, w http.ResponseWriter) error {
	e.tiers = t
	m := virtual.Member{Repo: vt.LocalRepo(1, "member-local"), ID: 1}
	hit, ok, err := t.Local(ctx, m, p.Descriptor)
	if err != nil || !ok {
		return errors.New("no local hit")
	}
	return hit(w)
}

func (e *fakeEngine) Metadata(ctx context.Context, _ virtual.Caller, p *virtual.Plan, m virtual.Merger) (*virtual.Merged, error) {
	e.merger = m
	local := virtual.Member{Repo: vt.LocalRepo(1, "member-local"), ID: 1, Position: 0}
	remote := virtual.Member{Repo: vt.RemoteRepo(2, "member-remote"), ID: 2, Position: 1}
	doc, rev, found, err := m.LocalDocument(ctx, local, p.Descriptor)
	if err != nil || !found {
		return nil, errors.New("no local document")
	}
	out, err := m.Merge(ctx, p.Descriptor, []virtual.Part{
		{Member: local, Tier: virtual.TierLocal, Revision: virtual.Revision{RevSeq: rev}, Doc: doc},
		{Member: remote, Tier: virtual.TierRemote, Revision: virtual.Revision{Digest: [32]byte{1}}, Doc: []byte("r"), CachedVersions: map[string]bool{"1.0.0": true}, Restricted: true, Stale: true},
	})
	return &virtual.Merged{Bytes: out, MergeFingerprint: [32]byte{9}, Partial: true, CacheStatus: "trove; detail=partial"}, err
}

func (e *fakeEngine) DeployTarget(v repository.Repository) (repository.Repository, error) {
	if !e.targetOK {
		return repository.Repository{}, errs.E(errs.MethodNotAllowed, errs.CodeNoDeployTarget, "none")
	}
	_ = v
	return e.target, nil
}

func (e *fakeEngine) ShadowCheck(_ context.Context, c virtual.Caller, local repository.Repository, name, docPath string) (virtual.ShadowOutcome, error) {
	e.caller, e.local, e.docPath = c, local, docPath
	e.shadowed.Proceed = false
	return e.shadowed, nil
}

// fakes of the Host's item store and remote fetcher.
type fakeItems struct {
	formats.ItemStore
	statRepo formats.RepoRef
	served   formats.ServeMode
}

func (f *fakeItems) Stat(_ context.Context, repo formats.RepoRef, p formats.Path) (formats.Item, bool, error) {
	f.statRepo = repo
	return formats.Item{Ref: formats.ItemRef{Repo: repo, Path: p.Path}}, true, nil
}

func (f *fakeItems) Serve(_ context.Context, _ *formats.Request, w http.ResponseWriter, it formats.Item, mode formats.ServeMode) error {
	f.served = mode
	_, err := io.WriteString(w, "item "+it.Ref.Repo.Name.String())
	return err
}

type fakeRemote struct {
	formats.RemoteFetcher
	cachedFor, fetchedFor string
	cachedErr, fetchErr   error
	// gateErr is the refusal of the format's artifact gate; gatedFor records the request it ran for.
	gateErr  error
	gatedFor string
}

func (f *fakeRemote) Gate(_ context.Context, rq *formats.Request, _ formats.Path) error {
	f.gatedFor = rq.Repo.Name.Key
	return f.gateErr
}

func (f *fakeRemote) GateWith(_ context.Context, rq *formats.Request, _ formats.Path, have formats.ServeFacts) (formats.ServeFacts, error) {
	f.gatedFor = rq.Repo.Name.Key
	return have, f.gateErr
}

func (f *fakeRemote) Cached(_ context.Context, rq *formats.Request, p formats.Path) (formats.Item, bool, error) {
	f.cachedFor = rq.Repo.Name.Key
	return formats.Item{Ref: formats.ItemRef{Repo: formats.RepoRef{Name: rq.Repo.Name}, Path: p.Path}}, f.cachedErr == nil, f.cachedErr
}

func (f *fakeRemote) Artifact(_ context.Context, rq *formats.Request, _ formats.Path, _ formats.Digests, w http.ResponseWriter) error {
	f.fetchedFor = rq.Repo.Name.Key
	if f.fetchErr == nil {
		_, _ = io.WriteString(w, "fetched")
	}
	return f.fetchErr
}

type fakeHost struct {
	formats.Host
	items  *fakeItems
	remote *fakeRemote
}

func (h fakeHost) Items() formats.ItemStore      { return h.items }
func (h fakeHost) Remote() formats.RemoteFetcher { return h.remote }

type bridge struct {
	r      formats.Resolver
	eng    *fakeEngine
	ad     *virtAdapter
	host   fakeHost
	rq     *formats.Request
	format virtFormat
}

func newBridge(t *testing.T) *bridge {
	t.Helper()
	ad := &virtAdapter{strategy: formats.Merge}
	f := virtFormat{a: ad, refuse: map[string]error{}}
	eng := &fakeEngine{}
	r, err := NewResolver(eng, nil, func(id string) (formats.Format, bool) { return f, id == "virtfmt" })
	if err != nil {
		t.Fatal(err)
	}
	host := fakeHost{items: &fakeItems{}, remote: &fakeRemote{}}
	v := vt.VirtualRepo(9, "all", 7200, nil, vt.LocalRepo(1, "member-local"), vt.RemoteRepo(2, "member-remote"))
	v.Format = "virtfmt"
	rq := &formats.Request{
		Request:  httptest.NewRequest(http.MethodGet, "/api/virtfmt/all/x", nil),
		Repo:     &v,
		Subject:  authz.Subject{Name: "alice", Endpoint: "customers", CacheFill: true},
		Endpoint: formats.EndpointFacts{Name: "customers", Rules: true, CacheFill: true},
		Host:     host,
	}
	return &bridge{r: r, eng: eng, ad: ad, host: host, rq: rq, format: f}
}

func TestNewResolverNeedsAnEngine(t *testing.T) {
	if _, err := NewResolver(nil, nil, nil); err == nil {
		t.Fatal("accepted no engine")
	}
}

func TestR_ARCH_015_BridgeArtifact(t *testing.T) {
	b := newBridge(t)
	w := httptest.NewRecorder()
	p := formats.Path{Path: "left-pad/-/1.0.0.tgz"}
	info := b.format.Classify(p)
	if err := b.r.Artifact(t.Context(), b.rq, p, info, w); err != nil {
		t.Fatal(err)
	}
	if w.Body.String() != "item member-local" || b.host.items.served != formats.ServeStored || b.host.items.statRepo.Name != qr("member-local") {
		t.Fatalf("body %q mode %v stat %v", w.Body.String(), b.host.items.served, b.host.items.statRepo)
	}
	c, d := b.eng.caller, b.eng.plan
	if !c.Rules || c.Subject.Endpoint != "customers" {
		t.Fatalf("caller = %+v", c)
	}
	if d.Format != "virtfmt" || d.Path != "left-pad/-/1.0.0.tgz" || d.Kind != catalog.KindArtifact || d.Package != "left-pad" || d.Version != "1.0.0" {
		t.Fatalf("descriptor = %+v", d)
	}

	t.Run("the tiers of a remote member run on a request addressed to the member", func(t *testing.T) {
		member := virtual.Member{Repo: vt.RemoteRepo(2, "member-remote"), ID: 2}
		hit, ok, err := b.eng.tiers.RemoteCached(t.Context(), member, d)
		if err != nil || !ok || b.host.remote.cachedFor != "member-remote" {
			t.Fatalf("cached for %q ok %v err %v", b.host.remote.cachedFor, ok, err)
		}
		rec := httptest.NewRecorder()
		if err := hit(rec); err != nil || rec.Body.String() != "item member-remote" {
			t.Fatalf("body %q err %v", rec.Body.String(), err)
		}
		rec = httptest.NewRecorder()
		found, err := b.eng.tiers.RemoteFetch(t.Context(), member, d, rec)
		if !found || err != nil || b.host.remote.fetchedFor != "member-remote" || rec.Body.String() != "fetched" {
			t.Fatalf("found %v err %v for %q", found, err, b.host.remote.fetchedFor)
		}
		b.host.remote.fetchErr = errs.E(errs.NotFound, errs.CodeNotFound, "no")
		if found, err = b.eng.tiers.RemoteFetch(t.Context(), member, d, httptest.NewRecorder()); found || err == nil {
			t.Fatalf("found %v err %v", found, err)
		}
		// The request of the virtual repository itself is untouched.
		if b.rq.Repo.Name != qr("all") {
			t.Fatalf("request repo = %s", b.rq.Repo.Name)
		}
	})
}

// A stored item the format withholds (an unpublished version) is a miss of its tier, so the
// walk goes on to the next member that has the file (R-REPO-019, R-NPM-047).
func TestR_REPO_019_AWithheldItemIsAMissOfItsTier(t *testing.T) {
	b := newBridge(t)
	p := formats.Path{Path: "left-pad/-/1.0.0.tgz"}
	if err := b.r.Artifact(t.Context(), b.rq, p, b.format.Classify(p), httptest.NewRecorder()); err != nil {
		t.Fatal(err)
	}
	d := b.eng.plan
	local := virtual.Member{Repo: vt.LocalRepo(1, "member-local"), ID: 1}
	remote := virtual.Member{Repo: vt.RemoteRepo(2, "member-remote"), ID: 2}
	gone := errs.E(errs.NotFound, errs.CodeNotFound, "The version was unpublished.")

	b.format.refuse["member-local"], b.format.refuse["member-remote"] = gone, gone
	if _, ok, err := b.eng.tiers.Local(t.Context(), local, d); ok || err != nil {
		t.Errorf("local: ok %v err %v, want a miss", ok, err)
	}
	if _, ok, err := b.eng.tiers.RemoteCached(t.Context(), remote, d); ok || err != nil {
		t.Errorf("cached: ok %v err %v, want a miss", ok, err)
	}

	// Any other refusal ends the walk.
	broken := errs.E(errs.Internal, errs.CodeInternal, "The marks cannot be read.")
	b.format.refuse["member-local"] = broken
	if _, ok, err := b.eng.tiers.Local(t.Context(), local, d); ok || err == nil {
		t.Errorf("local: ok %v err %v, want the error", ok, err)
	}

	delete(b.format.refuse, "member-local")
	if _, ok, err := b.eng.tiers.Local(t.Context(), local, d); !ok || err != nil {
		t.Errorf("local: ok %v err %v, want a hit", ok, err)
	}
}

func TestR_ARCH_015_BridgeMetadata(t *testing.T) {
	b := newBridge(t)
	p := formats.Path{Path: "lodash"}
	m, err := b.r.Metadata(t.Context(), b.rq, p, b.format.Classify(p), "full")
	if err != nil {
		t.Fatal(err)
	}
	if string(m.Bytes) != "merged" || m.MergeFingerprint[0] != 9 || !m.Partial || m.CacheStatus != "trove; detail=partial" {
		t.Fatalf("merged = %+v", m)
	}
	if b.ad.gotVar != "full" {
		t.Fatalf("variant = %q", b.ad.gotVar)
	}
	parts := b.ad.gotParts
	if len(parts) != 2 || parts[0].Member.Name != qr("member-local") || parts[0].Tier != formats.TierLocal || parts[0].Revision.RevSeq != 7 || string(parts[0].Doc) != "doc-of-member-local" {
		t.Fatalf("local part = %+v", parts)
	}
	r := parts[1]
	if r.Tier != formats.TierRemote || r.Position != 1 || r.Revision.Digest[0] != 1 || !r.Restricted || !r.Stale || !r.CachedVersions["1.0.0"] || r.Member.Class != formats.Remote {
		t.Fatalf("remote part = %+v", r)
	}

	t.Run("strategy and the artifact prefix come from the format", func(t *testing.T) {
		mg := b.eng.merger
		for _, tc := range []struct {
			in   formats.Strategy
			want virtual.Strategy
		}{{formats.FirstMatch, virtual.FirstMatch}, {formats.Merge, virtual.Merge}} {
			b.ad.strategy = tc.in
			if got := mg.Strategy(virtual.Descriptor{}); got != tc.want {
				t.Errorf("strategy %v -> %v", tc.in, got)
			}
		}
		if got := mg.ArtifactPrefix(virtual.Descriptor{Package: "@acme/util"}); got != "@acme/util/" {
			t.Errorf("prefix = %q", got)
		}
		if got := mg.ArtifactPrefix(virtual.Descriptor{}); got != "" {
			t.Errorf("a path without a package has the prefix %q", got)
		}
		if got := mg.CachedVersion("lodash/-/4.17.21.tgz"); got != "4.17.21" {
			t.Errorf("version = %q", got)
		}
		if got := mg.CachedVersion("lodash"); got != "" {
			t.Errorf("a document is no cached artifact: %q", got)
		}
		if got := mg.CachedVersion("a/../b"); got != "" {
			t.Errorf("an invalid path names a version: %q", got)
		}
	})
}

func TestR_REPO_021_BridgeDeployTarget(t *testing.T) {
	b := newBridge(t)
	if _, err := b.r.DeployTarget(t.Context(), b.rq); err == nil {
		t.Fatal("a virtual repository without a target took a deploy")
	}
	b.eng.target, b.eng.targetOK = vt.LocalRepo(1, "team-local"), true
	got, err := b.r.DeployTarget(t.Context(), b.rq)
	if err != nil || got.Name != qr("team-local") {
		t.Fatalf("target %v err %v", got, err)
	}
}

func TestR_REPO_048_AHostWithoutTheEngineRefusesTheShadowingCheck(t *testing.T) {
	out, err := unsupportedResolver{}.ShadowCheck(t.Context(), nil, "lodash")
	if err == nil || errs.KindOf(err) != errs.Internal || out.Proceed {
		t.Fatalf("the check of a Host without the engine let a publish through: %+v, %v", out, err)
	}
}

func TestR_REPO_048_BridgeShadowCheck(t *testing.T) {
	b := newBridge(t)
	b.eng.target, b.eng.targetOK = vt.LocalRepo(1, "team-local"), true
	b.eng.shadowed = virtual.ShadowOutcome{
		Code: errs.CodeShadowsRemotePackage, Virtuals: []string{"all"}, Undetermined: true, RetryAfter: time.Second,
		Shadowed: []virtual.Exposure{{Virtual: *b.rq.Repo, Remote: vt.RemoteRepo(2, "member-remote")}},
	}
	out, err := b.r.ShadowCheck(t.Context(), b.rq, "lodash")
	if err != nil {
		t.Fatal(err)
	}
	if b.eng.local.Name != qr("team-local") || b.eng.docPath != "lodash" {
		t.Fatalf("the check ran for %q on %q: a deploy through a virtual repository is checked for its target", b.eng.local.Name, b.eng.docPath)
	}
	if out.Proceed || out.Code != errs.CodeShadowsRemotePackage || !out.Undetermined || out.RetryAfter != time.Second {
		t.Fatalf("outcome = %+v", out)
	}
	if e, ok := errs.As(out.Refusal); !ok || e.Code != errs.CodeShadowsRemotePackage || e.Kind != errs.Forbidden {
		t.Fatalf("refusal = %v", out.Refusal)
	}
	if !slices.Equal(out.Shadowed, []formats.ShadowPair{{Virtual: "all", Remote: "member-remote"}}) {
		t.Fatalf("pairs = %+v", out.Shadowed)
	}

	t.Run("a write addressed to the local repository is checked for it", func(t *testing.T) {
		local := vt.LocalRepo(1, "team-local")
		local.Format = "virtfmt"
		b.rq.Repo = &local
		if _, err := b.r.ShadowCheck(t.Context(), b.rq, "lodash"); err != nil || b.eng.local.Name != qr("team-local") {
			t.Fatalf("local %q err %v", b.eng.local.Name, err)
		}
	})

	t.Run("a virtual repository without a target refuses the write before any check", func(t *testing.T) {
		b := newBridge(t)
		if _, err := b.r.ShadowCheck(t.Context(), b.rq, "lodash"); err == nil {
			t.Fatal("checked a write that cannot happen")
		}
	})
}

// The directory of a package's cached artifacts is the virtual adapter's to name; the bridge
// derives it from no document path (WP3.0l, R-NUGET-018).
func TestR_NUGET_018_TheDirectoryOfCachedVersionsComesFromTheVirtualAdapter(t *testing.T) {
	b := newBridge(t)
	b.ad.dir = "v3-flatcontainer/"
	p := formats.Path{Path: "lodash"}
	if _, err := b.r.Metadata(t.Context(), b.rq, p, b.format.Classify(p), "full"); err != nil {
		t.Fatal(err)
	}
	if got := b.eng.merger.ArtifactPrefix(virtual.Descriptor{Package: "acme.widgets"}); got != "v3-flatcontainer/acme.widgets/" {
		t.Fatalf("prefix = %q: the bridge did not ask the adapter", got)
	}
}

// A cached artifact of a remote member meets the format's gate through the typed path of the
// fetcher; a refusal of the gate ends the tier, so a withdrawn version is never served (R-NPM-026).
func TestR_NPM_026_ACachedArtifactOfARemoteMemberMeetsTheGate(t *testing.T) {
	b := newBridge(t)
	p := formats.Path{Path: "left-pad/-/1.0.0.tgz"}
	if err := b.r.Artifact(t.Context(), b.rq, p, b.format.Classify(p), httptest.NewRecorder()); err != nil {
		t.Fatal(err)
	}
	remote := virtual.Member{Repo: vt.RemoteRepo(2, "member-remote"), ID: 2}
	if _, ok, err := b.eng.tiers.RemoteCached(t.Context(), remote, b.eng.plan); !ok || err != nil || b.host.remote.gatedFor != "member-remote" {
		t.Fatalf("ok %v err %v gated for %q", ok, err, b.host.remote.gatedFor)
	}
	b.host.remote.gateErr = errs.E(errs.NotFound, errs.CodeNotFound, "The version is withdrawn.")
	if hit, ok, err := b.eng.tiers.RemoteCached(t.Context(), remote, b.eng.plan); ok || hit != nil || err == nil {
		t.Fatalf("a withdrawn version was served: ok %v err %v", ok, err)
	}
}

func TestR_NUGET_018_BridgePlan(t *testing.T) {
	b := newBridge(t)
	b.eng.plans = &virtual.Plan{Admitted: true, Members: []virtual.Member{
		{Repo: vt.LocalRepo(1, "member-local"), ID: 1, Position: 0},
		{Repo: vt.RemoteRepo(2, "member-remote"), ID: 2, Position: 1, MayFill: true},
	}}
	p := formats.Path{Path: "v3/search"}
	plan, err := b.r.Plan(t.Context(), b.rq, p, formats.PathInfo{Kind: formats.PathVolatile})
	if err != nil {
		t.Fatal(err)
	}
	want := []formats.PlannedMember{
		{Member: formats.RepoRef{Name: qr("member-local"), Class: formats.Local}, Tier: formats.TierLocal, Position: 0},
		{Member: formats.RepoRef{Name: qr("member-remote"), Class: formats.Remote}, Tier: formats.TierRemote, Position: 1, MayFill: true},
	}
	if !plan.Admitted || !slices.EqualFunc(plan.Members, want, func(a, b formats.PlannedMember) bool {
		return a.Member.Name == b.Member.Name && a.Member.Class == b.Member.Class && a.Tier == b.Tier && a.Position == b.Position && a.MayFill == b.MayFill
	}) {
		t.Fatalf("plan = %+v", plan)
	}
	if d := b.eng.plan; d.Kind != catalog.KindSearch || d.Path != "v3/search" {
		t.Fatalf("descriptor = %+v", d)
	}
}

func TestR_NUGET_016_BridgeKeepsAway(t *testing.T) {
	b := newBridge(t)
	remote := formats.RepoRef{ID: 2, Name: qr("member-remote"), Class: formats.Remote, Format: "virtfmt"}
	b.eng.away = map[string]bool{"member-remote/acme": true}
	t.Run("through a virtual repository the question is asked with it and the member", func(t *testing.T) {
		if away, err := b.r.KeepsAway(t.Context(), b.rq, remote, "acme"); err != nil || !away {
			t.Fatalf("away %v err %v", away, err)
		}
		if g := b.eng.gotAway; g.via == nil || g.via.Name != qr("all") || g.member.Name != qr("member-remote") || g.term != "acme" {
			t.Fatalf("asked %+v", g)
		}
		if away, _ := b.r.KeepsAway(t.Context(), b.rq, remote, "other"); away {
			t.Fatal("a term nothing protects is kept away")
		}
	})
	t.Run("a repository that is no member is refused", func(t *testing.T) {
		stranger := formats.RepoRef{ID: 9, Name: qr("stranger"), Class: formats.Remote, Format: "virtfmt"}
		if _, err := b.r.KeepsAway(t.Context(), b.rq, stranger, "acme"); err == nil {
			t.Fatal("the claims of a repository outside the request were probed")
		}
	})
	t.Run("a remote repository addressed directly is asked without a virtual repository", func(t *testing.T) {
		r := vt.RemoteRepo(2, "member-remote")
		r.Format = "virtfmt"
		rq := *b.rq
		rq.Repo = &r
		if away, err := b.r.KeepsAway(t.Context(), &rq, remote, "acme"); err != nil || !away || b.eng.gotAway.via != nil {
			t.Fatalf("away %v err %v via %v", away, err, b.eng.gotAway.via)
		}
		other := formats.RepoRef{Name: qr("elsewhere"), Class: formats.Remote}
		if _, err := b.r.KeepsAway(t.Context(), &rq, other, "acme"); err == nil {
			t.Fatal("a direct request asked about another repository")
		}
	})
}

func TestR_API_029_BridgeReadScope(t *testing.T) {
	scope := fixedScope{"all-repo": authz.ReadAll, "rows-repo": authz.ReadRows}
	t.Run("the shapes and the row decision of the decision point reach the format", func(t *testing.T) {
		b := newBridge(t)
		r, err := NewResolver(b.eng, fixedScoper{scope}, nil)
		if err != nil {
			t.Fatal(err)
		}
		got, err := r.ReadScope(t.Context(), b.rq, []qname.Repo{qr("all-repo"), qr("rows-repo"), qr("none-repo")})
		if err != nil {
			t.Fatal(err)
		}
		if got.Shape(qr("all-repo")) != formats.ReadAll || got.Shape(qr("rows-repo")) != formats.ReadRows || got.Shape(qr("none-repo")) != formats.ReadNone {
			t.Fatalf("shapes: %v %v %v", got.Shape(qr("all-repo")), got.Shape(qr("rows-repo")), got.Shape(qr("none-repo")))
		}
		if !got.MayRead(qr("rows-repo"), "acme", "1.0.0") || got.MayRead(qr("rows-repo"), "secret", "") {
			t.Fatal("the row decision did not reach the format")
		}
	})
	t.Run("a resolver without a decision point refuses", func(t *testing.T) {
		b := newBridge(t)
		if _, err := b.r.ReadScope(t.Context(), b.rq, []qname.Repo{qr("x")}); err == nil {
			t.Fatal("a listing was scoped by nothing")
		}
	})
	t.Run("Q-AUTHZ-168 the rows are decided through the repository the client addressed", func(t *testing.T) {
		b := newBridge(t)
		rec := &recordingScoper{}
		r, err := NewResolver(b.eng, rec, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.ReadScope(t.Context(), b.rq, []qname.Repo{qr("member-local")}); err != nil {
			t.Fatal(err)
		}
		if rec.addressed != "all" {
			t.Fatalf("the search of the virtual repository was scoped through %q, want all", rec.addressed)
		}
		members := b.rq.Repo.Members()
		mrq, err := b.rq.Member(&members[0])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.ReadScope(t.Context(), mrq, []qname.Repo{qr("member-local")}); err != nil {
			t.Fatal(err)
		}
		if rec.addressed != "all" {
			t.Fatalf("a request derived for a member was scoped through %q, want the outermost repository all", rec.addressed)
		}
	})
}

// recordingScoper records the addressed repository of the last call.
type recordingScoper struct{ addressed string }

func (s *recordingScoper) ReadScopeOn(_ context.Context, _ *authz.Index, _ authz.Subject, addressed qname.Repo, _ []qname.Repo) (authz.ReadScope, error) {
	s.addressed = addressed.Key
	return fixedScope{}, nil
}

type fixedScoper struct{ s fixedScope }

func (f fixedScoper) ReadScopeOn(context.Context, *authz.Index, authz.Subject, qname.Repo, []qname.Repo) (authz.ReadScope, error) {
	return f.s, nil
}

type fixedScope map[string]authz.ReadShape

func (f fixedScope) Shape(key qname.Repo) authz.ReadShape     { return f[key.Key] }
func (f fixedScope) MayRead(_ qname.Repo, pkg, _ string) bool { return pkg != "secret" }
