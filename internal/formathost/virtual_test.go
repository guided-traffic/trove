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
	gotVar   string
}

func (a *virtAdapter) Strategy(formats.Path) formats.Strategy { return a.strategy }

func (a *virtAdapter) Merge(_ context.Context, _ *formats.Request, _ formats.Path, parts []formats.MemberDocument) ([]byte, error) {
	a.gotParts = parts
	return []byte("merged"), nil
}

func (a *virtAdapter) LocalDocument(_ context.Context, _ *formats.Request, m formats.RepoRef, _ formats.Path, variant string) ([]byte, formats.RevSeq, bool, error) {
	a.gotVar = variant
	return []byte("doc-of-" + m.Key), 7, true, nil
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
	return f.refuse[it.Repo.Key]
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
}

func (e *fakeEngine) Plan(_ context.Context, c virtual.Caller, v repository.Repository, d virtual.Descriptor) (*virtual.Plan, error) {
	e.caller, e.plan = c, d
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
	_, err := io.WriteString(w, "item "+it.Ref.Repo.Key)
	return err
}

type fakeRemote struct {
	formats.RemoteFetcher
	cachedFor, fetchedFor string
	cachedErr, fetchErr   error
}

func (f *fakeRemote) Cached(_ context.Context, rq *formats.Request, p formats.Path) (formats.Item, bool, error) {
	f.cachedFor = rq.Repo.Key
	return formats.Item{Ref: formats.ItemRef{Repo: formats.RepoRef{Key: rq.Repo.Key}, Path: p.Path}}, f.cachedErr == nil, f.cachedErr
}

func (f *fakeRemote) Artifact(_ context.Context, rq *formats.Request, _ formats.Path, _ formats.Digests, w http.ResponseWriter) error {
	f.fetchedFor = rq.Repo.Key
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
	r, err := NewResolver(eng, func(id string) (formats.Format, bool) { return f, id == "virtfmt" })
	if err != nil {
		t.Fatal(err)
	}
	host := fakeHost{items: &fakeItems{}, remote: &fakeRemote{}}
	v := vt.VirtualRepo(9, "all", 7200, nil)
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
	if _, err := NewResolver(nil, nil); err == nil {
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
	if w.Body.String() != "item member-local" || b.host.items.served != formats.ServeStored || b.host.items.statRepo.Key != "member-local" {
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
		if b.rq.Repo.Key != "all" {
			t.Fatalf("request repo = %s", b.rq.Repo.Key)
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
	if len(parts) != 2 || parts[0].Member.Key != "member-local" || parts[0].Tier != formats.TierLocal || parts[0].Revision.RevSeq != 7 || string(parts[0].Doc) != "doc-of-member-local" {
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
		}{{formats.FirstMatch, virtual.FirstMatch}, {formats.Merge, virtual.Merge}, {formats.Newest, virtual.Newest}} {
			b.ad.strategy = tc.in
			if got := mg.Strategy(virtual.Descriptor{}); got != tc.want {
				t.Errorf("strategy %v -> %v", tc.in, got)
			}
		}
		if got := mg.ArtifactPrefix(virtual.Descriptor{Package: "@acme/util"}); got != "@acme/util/" {
			t.Errorf("prefix = %q", got)
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
	if err != nil || got.Key != "team-local" {
		t.Fatalf("target %v err %v", got, err)
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
	if b.eng.local.Key != "team-local" || b.eng.docPath != "lodash" {
		t.Fatalf("the check ran for %q on %q: a deploy through a virtual repository is checked for its target", b.eng.local.Key, b.eng.docPath)
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
		if _, err := b.r.ShadowCheck(t.Context(), b.rq, "lodash"); err != nil || b.eng.local.Key != "team-local" {
			t.Fatalf("local %q err %v", b.eng.local.Key, err)
		}
	})

	t.Run("a virtual repository without a target refuses the write before any check", func(t *testing.T) {
		b := newBridge(t)
		if _, err := b.r.ShadowCheck(t.Context(), b.rq, "lodash"); err == nil {
			t.Fatal("checked a write that cannot happen")
		}
	})
}
