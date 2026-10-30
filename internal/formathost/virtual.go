package formathost

import (
	"context"
	"net/http"
	"path"

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
}

var _ VirtualEngine = (*virtual.Resolver)(nil)

// NewResolver builds the Resolver a Host hands to formats over the virtual engine. formats finds a
// registered format; nil is formats.Lookup.
func NewResolver(engine VirtualEngine, find func(id string) (formats.Format, bool)) (formats.Resolver, error) {
	if engine == nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "formathost: no virtual engine")
	}
	if find == nil {
		find = formats.Lookup
	}
	return &resolver{e: engine, find: find}, nil
}

type resolver struct {
	e    VirtualEngine
	find func(id string) (formats.Format, bool)
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
	return formats.RepoRef{ID: m.ID, Key: m.Repo.Key, Class: m.Repo.Class, Format: formats.ID(m.Repo.Format)}
}

// Artifact implements [formats.Resolver].
func (r *resolver) Artifact(ctx context.Context, rq *formats.Request, p formats.Path, info formats.PathInfo, w http.ResponseWriter) error {
	if _, _, err := r.adapter(rq); err != nil {
		return err
	}
	c := callerOf(rq)
	plan, err := r.e.Plan(ctx, c, *rq.Repo, descriptorOf(rq, p, info, ""))
	if err != nil {
		return err
	}
	return r.e.Artifact(ctx, c, plan, tiers{rq: rq, p: p}, w)
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
	m, err := r.e.Metadata(ctx, c, plan, merger{rq: rq, p: p, f: f, a: a})
	if err != nil {
		return nil, err
	}
	return &formats.Merged{Bytes: m.Bytes, MergeFingerprint: m.MergeFingerprint, Partial: m.Partial, CacheStatus: m.CacheStatus}, nil
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
		res.Shadowed = append(res.Shadowed, formats.ShadowPair{Virtual: x.Virtual.Key, Remote: x.Remote.Key})
	}
	return res, nil
}

// tiers are the tiers of an artifact over the Host: a local member's item store, a remote member's
// cache and its request path. A member's calls run on a copy of the request addressed to the member,
// which is how the Host computes the populate right (R-AUTHZ-008) and finds the repository.
type tiers struct {
	rq *formats.Request
	p  formats.Path
}

var _ virtual.Tiers = tiers{}

func (t tiers) forMember(m virtual.Member) *formats.Request {
	c := *t.rq
	c.Repo = &m.Repo
	return &c
}

func (t tiers) serve(ctx context.Context, it formats.Item) virtual.Hit {
	return func(w http.ResponseWriter) error {
		return t.rq.Host.Items().Serve(ctx, t.rq, w, it, formats.ServeStored)
	}
}

func (t tiers) Local(ctx context.Context, m virtual.Member, _ virtual.Descriptor) (virtual.Hit, bool, error) {
	it, ok, err := t.rq.Host.Items().Stat(ctx, memberRef(m), t.p)
	if err != nil || !ok || it.Folder {
		return nil, false, err
	}
	return t.serve(ctx, it), true, nil
}

func (t tiers) RemoteCached(ctx context.Context, m virtual.Member, _ virtual.Descriptor) (virtual.Hit, bool, error) {
	member := t.forMember(m)
	it, ok, err := t.rq.Host.Remote().Cached(ctx, member, t.p)
	if err != nil || !ok {
		return nil, false, err
	}
	// The format's marks apply to a cached artifact as well: a stale document is
	// revalidated first and a withdrawn version is refused (Q-NPM-13, R-NPM-026).
	if g, ok := t.rq.Host.Remote().(interface {
		Gate(context.Context, *formats.Request, formats.Path) error
	}); ok {
		if err := g.Gate(ctx, member, t.p); err != nil {
			return nil, false, err
		}
	}
	return t.serve(ctx, it), true, nil
}

func (t tiers) RemoteFetch(ctx context.Context, m virtual.Member, _ virtual.Descriptor, w http.ResponseWriter) (bool, error) {
	err := t.rq.Host.Remote().Artifact(ctx, t.forMember(m), t.p, formats.Digests{}, w)
	return err == nil, err
}

// merger is the format's part of a merge for one request.
type merger struct {
	rq *formats.Request
	p  formats.Path
	f  formats.Format
	a  formats.VirtualAdapter
}

var _ virtual.Merger = merger{}

func (m merger) Strategy(virtual.Descriptor) virtual.Strategy {
	switch m.a.Strategy(m.p) {
	case formats.Merge:
		return virtual.Merge
	case formats.Newest:
		return virtual.Newest
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

// ArtifactPrefix is the prefix of the cached artifacts of a package: its document's path, or the
// directory of it when the document is an index.json (NuGet's version list), with a slash. A
// format whose artifacts live elsewhere has no cached versions in a merge and gets the restricted
// reading only for the versions it lists itself.
func (m merger) ArtifactPrefix(d virtual.Descriptor) string {
	doc, ok := m.f.PackageDocumentPath(d.Package)
	if !ok || doc.Path == "" {
		return ""
	}
	base := doc.Path
	if path.Base(base) == "index.json" {
		base = path.Dir(base)
	}
	return base + "/"
}

// CachedVersion names the version a cached artifact path belongs to, by the format's own
// classification.
func (m merger) CachedVersion(p string) string {
	c, err := repository.CleanPath(p)
	if err != nil {
		return ""
	}
	info := m.f.Classify(c)
	if info.Kind != formats.Content {
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
