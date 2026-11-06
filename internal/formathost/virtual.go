package formathost

import (
	"context"
	"github.com/guided-traffic/trove/internal/qname"
	"net/http"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/packages"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/virtual"
)

// The bridge from the virtual engine (internal/virtual) to the Resolver a format meets
// (formats.Resolver, C-5). The engine knows no format and no request: this file turns a
// formats.Request into the caller and the descriptor it takes, the format's VirtualAdapter into
// its Merger, and the Host's item store and remote fetcher into its tiers.

// VirtualEngine is the part of *virtual.Resolver the bridge uses.
type VirtualEngine interface {
	Plan(ctx context.Context, c virtual.Caller, v repository.Repository, d virtual.Descriptor) (*virtual.Plan, error)
	Artifact(ctx context.Context, c virtual.Caller, p *virtual.Plan, t virtual.Tiers, w http.ResponseWriter) error
	Metadata(ctx context.Context, c virtual.Caller, p *virtual.Plan, m virtual.Merger) (*virtual.Merged, error)
	DeployTarget(v repository.Repository) (repository.Repository, error)
	ShadowCheck(ctx context.Context, c virtual.Caller, local repository.Repository, name, docPath string) (virtual.ShadowOutcome, error)
	KeepsAway(ctx context.Context, v *repository.Repository, member repository.Repository, term string) (bool, error)
}

var _ VirtualEngine = (*virtual.Resolver)(nil)

// NewResolver builds the Resolver a Host hands to formats over the virtual engine. scoper is the
// decision point as the unaudited read scope of a listing; nil makes ReadScope refuse. formats
// finds a registered format; nil is formats.Lookup.
func NewResolver(engine VirtualEngine, scoper authz.ReadScoper, find func(id string) (formats.Format, bool)) (formats.Resolver, error) {
	if engine == nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "formathost: no virtual engine")
	}
	if find == nil {
		find = formats.Lookup
	}
	return &resolver{e: engine, scoper: scoper, find: find, caps: formats.RemoteCapabilities}, nil
}

type resolver struct {
	e      VirtualEngine
	scoper authz.ReadScoper
	find   func(id string) (formats.Format, bool)
	// caps are the remote capabilities a format registered (formats.RemoteCapabilities).
	caps func(formats.ID) formats.RemoteCaps
}

var _ formats.Resolver = (*resolver)(nil)

// adapter returns the format of the request's repository and its virtual adapter.
func (r *resolver) adapter(rq *formats.Request) (formats.Format, formats.VirtualAdapter, error) {
	f, ok := r.find(rq.Repo.Format)
	if !ok || f.Virtual() == nil {
		return nil, nil, errs.E(errs.Internal, errs.CodeInternal, "The format of the repository has no virtual adapter.")
	}
	return f, f.Virtual(), nil
}

func callerOf(rq *formats.Request) virtual.Caller {
	return virtual.Caller{Subject: rq.Subject, Index: rq.Index, Rules: rq.Endpoint.Rules}
}

func descriptorOf(rq *formats.Request, p formats.Path, info formats.PathInfo, variant string) virtual.Descriptor {
	return virtual.Descriptor{
		Format: rq.Repo.Format, Path: p.Path, Kind: info.Kind.RequestKind(),
		Package: info.Package, Version: info.Version, RoutingName: info.RoutingName, Variant: variant,
	}
}

func memberRef(m virtual.Member) formats.RepoRef {
	return formats.RepoRef{ID: m.ID, Name: m.Repo.Name, Class: m.Repo.Class, Format: formats.ID(m.Repo.Format)}
}

// Artifact implements [formats.Resolver].
func (r *resolver) Artifact(ctx context.Context, rq *formats.Request, p formats.Path, info formats.PathInfo, w http.ResponseWriter) error {
	f, _, err := r.adapter(rq)
	if err != nil {
		return err
	}
	c := callerOf(rq)
	plan, err := r.e.Plan(ctx, c, *rq.Repo, descriptorOf(rq, p, info, ""))
	if err != nil {
		return err
	}
	return r.e.Artifact(ctx, c, plan, tiers{rq: rq, p: p, f: f}, w)
}

// Metadata implements [formats.Resolver].
func (r *resolver) Metadata(ctx context.Context, rq *formats.Request, p formats.Path, info formats.PathInfo, variant string) (*formats.Merged, error) {
	f, a, err := r.adapter(rq)
	if err != nil {
		return nil, err
	}
	c := callerOf(rq)
	plan, err := r.e.Plan(ctx, c, *rq.Repo, descriptorOf(rq, p, info, variant))
	if err != nil {
		return nil, err
	}
	m, err := r.e.Metadata(ctx, c, plan, merger{rq: rq, p: p, f: f, a: a, fill: r.caps(f.Descriptor().ID).Fill})
	if err != nil {
		return nil, err
	}
	return &formats.Merged{Bytes: m.Bytes, MergeFingerprint: m.MergeFingerprint, Partial: m.Partial, CacheStatus: m.CacheStatus}, nil
}

// Plan implements [formats.Resolver]: the engine's plan, without the engine's types.
func (r *resolver) Plan(ctx context.Context, rq *formats.Request, p formats.Path, info formats.PathInfo) (*formats.VirtualPlan, error) {
	plan, err := r.e.Plan(ctx, callerOf(rq), *rq.Repo, descriptorOf(rq, p, info, ""))
	if err != nil {
		return nil, err
	}
	out := &formats.VirtualPlan{Admitted: plan.Admitted, Members: make([]formats.PlannedMember, 0, len(plan.Members))}
	for _, m := range plan.Members {
		tier := formats.TierRemote
		if m.Local() {
			tier = formats.TierLocal
		}
		out.Members = append(out.Members, formats.PlannedMember{Member: memberRef(m), Tier: tier, Position: m.Position, MayFill: m.MayFill})
	}
	return out, nil
}

// KeepsAway implements [formats.Resolver]. The member is looked up among the members of the
// request's virtual repository, or is the request's own repository; a repository the request has
// nothing to do with is refused, so a format cannot probe the claims of another one.
func (r *resolver) KeepsAway(ctx context.Context, rq *formats.Request, member formats.RepoRef, term string) (bool, error) {
	var via *repository.Repository
	target := *rq.Repo
	if rq.Repo.Class == repository.Virtual {
		via = rq.Repo
		found := false
		for _, m := range rq.Repo.Members() {
			if m.Name == member.Name {
				target, found = m, true
				break
			}
		}
		if !found {
			return false, errs.E(errs.Internal, errs.CodeInternal, "The repository is not a member of the request's virtual repository.")
		}
	} else if member.Name != rq.Repo.Name {
		return false, errs.E(errs.Internal, errs.CodeInternal, "The repository is not the request's repository.")
	}
	return r.e.KeepsAway(ctx, via, target, term)
}

// ReadScope implements [formats.Resolver]: the decision point's unaudited read scope, with every
// row decided as a read through the repository the client addressed (rq.Addressed()). A virtual
// repository's search reads its members' rows under the virtual repository's exposure and the
// lift of a token scope that names it, as its documents do (Q-EP-11, Q-AUTHZ-163, Q-AUTHZ-168).
func (r *resolver) ReadScope(ctx context.Context, rq *formats.Request, repos []qname.Repo) (formats.ReadScope, error) {
	if r.scoper == nil {
		return nil, errNoResolver()
	}
	rs, err := r.scoper.ReadScopeOn(ctx, rq.Index, rq.Subject, rq.Addressed(), repos)
	if err != nil {
		return nil, err
	}
	return readScope{rs: rs}, nil
}

// readScope converts the decision point's shapes to the contract's.
type readScope struct{ rs authz.ReadScope }

func (s readScope) Shape(repo qname.Repo) formats.ReadShape {
	switch s.rs.Shape(repo) {
	case authz.ReadAll:
		return formats.ReadAll
	case authz.ReadRows:
		return formats.ReadRows
	}
	return formats.ReadNone
}

func (s readScope) MayRead(repo qname.Repo, pkg, version string) bool {
	return s.rs.MayRead(repo, pkg, version)
}

// DeployTarget implements [formats.Resolver].
func (r *resolver) DeployTarget(_ context.Context, rq *formats.Request) (*repository.Repository, error) {
	t, err := r.e.DeployTarget(*rq.Repo)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// ShadowCheck implements [formats.Resolver]: the check for the local repository the write goes to,
// which is the deploy target when the request was addressed to a virtual repository. A format
// without package names has nothing to shadow. An outcome that does not proceed carries the
// refusal to answer.
func (r *resolver) ShadowCheck(ctx context.Context, rq *formats.Request, name string) (formats.ShadowOutcome, error) {
	f, ok := r.find(rq.Repo.Format)
	if !ok {
		return formats.ShadowOutcome{}, errs.E(errs.Internal, errs.CodeInternal, "The format of the repository is not registered.")
	}
	local := *rq.Repo
	var err error
	if local.Class == repository.Virtual {
		if local, err = r.e.DeployTarget(local); err != nil {
			return formats.ShadowOutcome{}, err
		}
	}
	doc, has := f.PackageDocumentPath(name)
	if !has {
		return formats.ShadowOutcome{Proceed: true}, nil
	}
	out, err := r.e.ShadowCheck(ctx, callerOf(rq), local, name, doc.Path)
	if err != nil {
		return formats.ShadowOutcome{}, err
	}
	res := formats.ShadowOutcome{
		Proceed: out.Proceed, Code: out.Code, RetryAfter: out.RetryAfter, Undetermined: out.Undetermined, Refusal: out.Err(name),
	}
	for _, x := range out.Shadowed {
		res.Shadowed = append(res.Shadowed, formats.ShadowPair{Virtual: x.Virtual.Name.String(), Remote: x.Remote.Name.String()})
	}
	return res, nil
}

// tiers are the tiers of an artifact over the Host: a local member's item store, a remote member's
// cache and its request path. A member's calls run on a copy of the request addressed to the member,
// which is how the Host computes the populate right (R-AUTHZ-008) and finds the repository.
type tiers struct {
	rq *formats.Request
	p  formats.Path
	f  formats.Format
}

var _ virtual.Tiers = tiers{}

// forMember is the request made for a member of the addressed virtual repository: the Addressed
// of its decisions stays the repository the client named (Q-AUTHZ-165, Q-ARCH-252).
func (t tiers) forMember(m virtual.Member) (*formats.Request, error) {
	return t.rq.Member(&m.Repo)
}

func (t tiers) serve(ctx context.Context, it formats.Item) virtual.Hit {
	return func(w http.ResponseWriter) error {
		return t.rq.Host.Items().Serve(ctx, t.rq, w, it, formats.ServeStored)
	}
}

// servable asks the format whether it serves the stored item of a member: a refusal that is a
// missing resource (a tombstoned or withdrawn version) is a miss of the tier, so that the walk goes
// on to the next member that has the file (R-REPO-019, R-NPM-047). Any other refusal ends it.
func (t tiers) servable(ctx context.Context, it formats.Item) (bool, error) {
	err := t.f.ServeCheck(ctx, t.rq.Host, it.Ref.Repo, it.Ref, formats.ServeStored)
	if e, ok := errs.As(err); ok && e.Kind == errs.NotFound {
		return false, nil
	}
	return err == nil, err
}

func (t tiers) Local(ctx context.Context, m virtual.Member, _ virtual.Descriptor) (virtual.Hit, bool, error) {
	it, ok, err := t.rq.Host.Items().Stat(ctx, memberRef(m), t.p)
	if err != nil || !ok || it.Folder {
		return nil, false, err
	}
	if ok, err := t.servable(ctx, it); !ok || err != nil {
		return nil, false, err
	}
	return t.serve(ctx, it), true, nil
}

func (t tiers) RemoteCached(ctx context.Context, m virtual.Member, _ virtual.Descriptor) (virtual.Hit, bool, error) {
	member, err := t.forMember(m)
	if err != nil {
		return nil, false, err
	}
	it, ok, err := t.rq.Host.Remote().Cached(ctx, member, t.p)
	if err != nil || !ok {
		return nil, false, err
	}
	// The format's marks apply to a cached artifact as well: a stale document is
	// revalidated first and a withdrawn version is refused (Q-NPM-13, R-NPM-026).
	facts, err := t.rq.Host.Remote().GateWith(ctx, member, t.p, it.Ref.Facts)
	if err != nil {
		return nil, false, err
	}
	if facts.Refreshed {
		// The gate asked the upstream after the lookup: read the artifact again, with its facts, before serving it.
		if it, ok, err = t.rq.Host.Remote().Cached(ctx, member, t.p); err != nil || !ok {
			return nil, false, err
		}
	} else {
		it.Ref.Facts = facts // the serve check below decides on the facts the gate used
	}
	if ok, err := t.servable(ctx, it); !ok || err != nil {
		return nil, false, err
	}
	return t.serve(ctx, it), true, nil
}

func (t tiers) RemoteFetch(ctx context.Context, m virtual.Member, _ virtual.Descriptor, w http.ResponseWriter) (bool, error) {
	member, err := t.forMember(m)
	if err != nil {
		return false, err
	}
	err = t.rq.Host.Remote().Artifact(ctx, member, t.p, formats.Digests{}, w)
	return err == nil, err
}

// merger is the format's part of a merge for one request.
type merger struct {
	rq *formats.Request
	p  formats.Path
	f  formats.Format
	a  formats.VirtualAdapter
	// fill is the format's fill hook, nil for a format without one.
	fill formats.FillInspector
}

var _ virtual.Merger = merger{}

func (m merger) Strategy(virtual.Descriptor) virtual.Strategy {
	switch m.a.Strategy(m.p) {
	case formats.Merge:
		return virtual.Merge
	}
	return virtual.FirstMatch
}

func (m merger) LocalDocument(ctx context.Context, mem virtual.Member, d virtual.Descriptor) ([]byte, int64, bool, error) {
	doc, rev, found, err := m.a.LocalDocument(ctx, m.rq, memberRef(mem), m.p, d.Variant)
	return doc, int64(rev), found, err
}

func (m merger) Merge(ctx context.Context, _ virtual.Descriptor, parts []virtual.Part) ([]byte, error) {
	docs := make([]formats.MemberDocument, len(parts))
	for i, p := range parts {
		docs[i] = formats.MemberDocument{
			Member: memberRef(p.Member), Tier: formats.Tier(p.Tier), Position: p.Member.Position,
			Revision: formats.Revision{RevSeq: formats.RevSeq(p.Revision.RevSeq), Digest: p.Revision.Digest},
			Doc:      p.Doc, CachedVersions: p.CachedVersions, Restricted: p.Restricted, Stale: p.Stale,
		}
	}
	return m.a.Merge(ctx, m.rq, m.p, docs)
}

// ArtifactPrefix is the prefix of the cached artifacts of a package, which the format's virtual
// adapter names: the bridge knows no layout of any format (WP3.0l).
func (m merger) ArtifactPrefix(d virtual.Descriptor) string {
	if d.Package == "" {
		return ""
	}
	return m.a.ArtifactPrefix(d.Package)
}

// CachedVersion names the version a cached artifact path belongs to, by the format's own
// classification. A version counts as cached only when the artifact that records it is: for a
// format whose remote adapter inspects what it fetches (FillInspector), the artifact it inspects,
// whose fill records the version (NuGet's .nupkg). Another file of the version (a .nuspec, a
// symbol package) is no package the second tier can serve in its place, so a caller who may not
// fill would be shown a version whose package it cannot download (R-AUTHZ-008).
func (m merger) CachedVersion(p string) string {
	c, err := repository.CleanPath(p)
	if err != nil {
		return ""
	}
	info := m.f.Classify(c)
	if info.Kind != formats.Content {
		return ""
	}
	if m.fill != nil && !m.fill.InspectsFill(c) {
		return ""
	}
	return info.Version
}

// NewVirtualLocals reads local members for the virtual engine over the package rows and the item
// store: the revision check of the merged-metadata cache, the explanation's "does the member hold
// it" and the first condition of the shadowing check.
func NewVirtualLocals(pkgs *packages.Store, it ItemOps) virtual.Locals {
	return virtualLocals{pkgs: pkgs, items: it}
}

type virtualLocals struct {
	pkgs  *packages.Store
	items ItemOps
}

func (l virtualLocals) RevSeqs(ctx context.Context, ids []int64, name string) (map[int64]int64, error) {
	return l.pkgs.RevSeqs(ctx, ids, name)
}

func (l virtualLocals) Holds(ctx context.Context, m repository.Repository, d virtual.Descriptor) (bool, error) {
	if d.Kind == formats.KindMetadata && d.Package != "" {
		return l.HoldsVersion(ctx, m, d.Package)
	}
	it, found, err := l.items.Live(ctx, items.RepoOf(m), d.Path)
	return found && it.Kind != items.KindFolder, err
}

func (l virtualLocals) HoldsVersion(ctx context.Context, m repository.Repository, name string) (bool, error) {
	id := items.RepoOf(m).ID
	vs, _, err := l.pkgs.Versions(ctx, id, name)
	return len(vs) > 0, err
}
