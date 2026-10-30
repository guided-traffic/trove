package formathost

import (
	"context"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/proxy"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/virtual"
)

// RemotesEngine is the part of the remote engine the virtual engine's seam uses;
// *proxy.Engine implements it.
type RemotesEngine interface {
	Metadata(ctx context.Context, rq proxy.Request) (*proxy.Document, error)
	Cached(ctx context.Context, rq proxy.Request) (items.Item, bool, error)
	EntryState(ctx context.Context, repoID int64, path string) (proxy.EntryState, error)
	Peek(ctx context.Context, rem proxy.Remote, path, variant string) (proxy.Peek, error)
	Probe(ctx context.Context, rem proxy.Remote, path string, a proxy.Adapter) (proxy.ProbeResult, error)
	CachedArtifacts(ctx context.Context, repoID int64, prefix string) ([]string, error)
}

var _ RemotesEngine = (*proxy.Engine)(nil)

// VirtualRemotesOptions configure [NewVirtualRemotes].
type VirtualRemotesOptions struct {
	Engine RemotesEngine
	// Repos finds a remote repository by its id (the seam's EntryState names ids).
	Repos repository.Reader
	// Decider records the populate event of a fill made for a virtual request.
	Decider authz.Decider
	Deps    Deps
}

// NewVirtualRemotes binds the remote engine to the virtual engine's seam (virtual.Remotes,
// docs/developer/virtual.md "Binding the proxy engine"). Each call builds the engine's
// request for the member: the format's adapter, the caller's facts and a populate check
// that answers the MayFill the virtual engine decided with the decision point.
func NewVirtualRemotes(o VirtualRemotesOptions) (virtual.Remotes, error) {
	if o.Engine == nil || o.Repos == nil || o.Decider == nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "formathost: the virtual remotes need the remote engine, the repository reader and the decision point")
	}
	return &vremotes{o: o}, nil
}

type vremotes struct{ o VirtualRemotesOptions }

var _ virtual.Remotes = (*vremotes)(nil)

// request builds the engine's request for a call; the kind follows the format's
// classification of the path.
func (v *vremotes) request(rc virtual.RemoteCall) (proxy.Request, error) {
	rem, err := proxy.NewRemote(rc.Remote)
	if err != nil {
		return proxy.Request{}, errs.E(errs.Internal, errs.CodeInternal, "The member is not a usable remote repository.", errs.WithCause(err))
	}
	f, ok := v.o.Deps.lookup(rc.Remote.Format)
	if !ok || f.Remote() == nil {
		return proxy.Request{}, errs.E(errs.Internal, errs.CodeInternal, "The format has no remote adapter.")
	}
	info := f.Classify(formats.Path{Path: rc.Path})
	kind := proxy.Metadata
	if info.Kind.RequestKind() == formats.KindArtifact {
		kind = proxy.Artifact
	}
	repo := rc.Remote
	pkg := rc.Package
	if pkg == "" {
		pkg = info.Package
	}
	ref := formats.RepoRef{ID: rem.ID(), Key: repo.Key, Class: formats.Remote, Format: formats.ID(repo.Format)}
	return proxy.Request{
		Remote: rem, Path: rc.Path, Variant: rc.Variant, Kind: kind, Package: pkg,
		Adapter:      newAdapter(f, &repo, ref, v.o.Deps),
		AlwaysVerify: f.Descriptor().VerifyAnnouncedDigests, MaxBytes: f.Descriptor().MaxDocumentBytes,
		Populate: &callPopulate{d: v.o.Decider, rc: rc, info: info},
		Caller:   proxy.CallerFacts{Principal: rc.Subject.ID, Endpoint: rc.Endpoint},
	}, nil
}

// Cached implements [virtual.Remotes].
func (v *vremotes) Cached(ctx context.Context, rc virtual.RemoteCall) (bool, error) {
	rq, err := v.request(rc)
	if err != nil {
		return false, err
	}
	if rq.Kind == proxy.Artifact {
		_, ok, err := v.o.Engine.Cached(ctx, rq)
		return ok, err
	}
	st, err := v.o.Engine.EntryState(ctx, rq.Remote.ID(), rc.Path)
	return st.Stored && st.GoneAt.IsZero(), err
}

// Metadata implements [virtual.Remotes].
func (v *vremotes) Metadata(ctx context.Context, rc virtual.RemoteCall) (*virtual.RemoteDoc, error) {
	rq, err := v.request(rc)
	if err != nil {
		return nil, err
	}
	rq.Kind = proxy.Metadata
	d, err := v.o.Engine.Metadata(ctx, rq)
	if err != nil {
		return nil, err
	}
	if d.Status.Negative {
		return nil, errs.E(errs.NotFound, errs.CodeNotFound, "The member does not know the package.")
	}
	return &virtual.RemoteDoc{Digest: d.Digest, Bytes: d.Bytes, ExpiresAt: d.ExpiresAt, Stale: d.Stale, Fetched: !d.Status.Hit}, nil
}

// EntryState implements [virtual.Remotes].
func (v *vremotes) EntryState(ctx context.Context, repoID int64, path string) (virtual.EntryState, error) {
	st, err := v.o.Engine.EntryState(ctx, repoID, path)
	if err != nil {
		return virtual.EntryState{}, err
	}
	out := virtual.EntryState{Stored: st.Stored, GoneAt: st.GoneAt}
	repo, ok, err := v.byID(ctx, repoID)
	if err != nil || !ok {
		return out, err
	}
	rem, err := proxy.NewRemote(repo)
	if err != nil {
		return out, nil //nolint:nilerr // a repository without usable settings has no negative entry or circuit to report
	}
	p, err := v.o.Engine.Peek(ctx, rem, path, "")
	if err != nil {
		return out, err
	}
	out.Negative, out.Offline, out.CircuitOpenFor = p.Negative, p.Offline, p.CircuitOpenFor
	return out, nil
}

// byID finds a repository by its row id.
func (v *vremotes) byID(ctx context.Context, id int64) (repository.Repository, bool, error) {
	all, err := v.o.Repos.List(ctx)
	if err != nil {
		return repository.Repository{}, false, err
	}
	for _, r := range all {
		if repoRef(r).ID == id {
			return r, true, nil
		}
	}
	return repository.Repository{}, false, nil
}

// Probe implements [virtual.Remotes].
func (v *vremotes) Probe(ctx context.Context, rc virtual.RemoteCall) (virtual.ProbeResult, error) {
	rq, err := v.request(rc)
	if err != nil {
		return virtual.ProbeResult{}, err
	}
	res, err := v.o.Engine.Probe(ctx, rq.Remote, rc.Path, rq.Adapter)
	if err != nil {
		return virtual.ProbeResult{}, err
	}
	out := virtual.ProbeResult{RetryAfter: res.RetryAfter}
	switch res.Outcome {
	case proxy.Served:
		out.Outcome = virtual.Served
	case proxy.NotServed:
		out.Outcome = virtual.NotServed
	default:
		out.Outcome = virtual.Undetermined
	}
	return out, nil
}

// CachedArtifacts implements [virtual.Remotes].
func (v *vremotes) CachedArtifacts(ctx context.Context, repoID int64, prefix string) ([]string, error) {
	return v.o.Engine.CachedArtifacts(ctx, repoID, prefix)
}

// callPopulate is the populate check of a call made for a virtual request: the virtual
// engine decided MayFill with MemberUsable (R-AUTHZ-015); this answers it and records the
// allowed populate when the call fetches.
type callPopulate struct {
	d    authz.Decider
	rc   virtual.RemoteCall
	info formats.PathInfo
}

var _ proxy.Populate = (*callPopulate)(nil)

func (p *callPopulate) May(context.Context) bool { return p.rc.MayFill }

// Deny is the refusal of a member that may not be filled: the virtual engine counts it
// as a miss (R-AUTHZ-008).
func (p *callPopulate) Deny(context.Context) error {
	return errs.E(errs.Forbidden, errs.CodePopulateNotPermitted, "The caller may read the member but not bring new content into its cache.")
}

func (p *callPopulate) Record(ctx context.Context, tx meta.Tx) error {
	c := authz.Coordinate{
		Repository: p.rc.Remote.Key, Class: repository.Remote, Format: p.rc.Remote.Format, Kind: authz.KindWrite,
		Path: p.rc.Path, Package: p.info.Package, Version: p.info.Version, Addressed: p.rc.Via.Key,
		Actions: []formats.Action{formats.ActionRead},
	}
	return p.d.RecordAllowed(ctx, tx, p.rc.Subject, c, authz.Decision{Outcome: authz.Allow, Reason: authz.ReasonAllowGrant, Target: p.rc.Remote.Key})
}

func (p *callPopulate) Endpoint() (string, bool) {
	return p.rc.Subject.Endpoint, p.rc.Subject.CacheFill
}
