package formathost

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/api/edge"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/proxy"
	"github.com/guided-traffic/trove/internal/repository"
)

// RemoteEngine is the remote engine as the host uses it; *proxy.Engine implements it.
type RemoteEngine interface {
	Metadata(ctx context.Context, rq proxy.Request) (*proxy.Document, error)
	Artifact(ctx context.Context, rq proxy.Request, w http.ResponseWriter, r *http.Request) error
	Cached(ctx context.Context, rq proxy.Request) (items.Item, bool, error)
	EnsureCurrent(ctx context.Context, rq proxy.Request, restoredAt time.Time) (*proxy.Document, proxy.Currency, error)
	EntryState(ctx context.Context, repoID int64, path string) (proxy.EntryState, error)
}

var _ RemoteEngine = (*proxy.Engine)(nil)

// remote is the RemoteFetcher the formats see (C-4): a thin wrapper over the remote
// engine. It builds the engine's request from the format's request, adapts the
// format's RemoteAdapter to the engine's (Q-ARCH-200) and computes the populate check
// from the request's subject, index and endpoint, so a format cannot set it
// (R-AUTHZ-008, R-EP-019).
//
// The wrapper decides nothing the format has not asked for: the format authorizes
// the target with Host.Authorize first (R-REPO-025 steps 1 to 3), and the wrapper
// does not repeat it, so one request leaves one decision in the audit trail.
type remote struct {
	h *Host
	e RemoteEngine
}

var _ formats.RemoteFetcher = (*remote)(nil)

// Metadata implements [formats.RemoteFetcher].
func (r *remote) Metadata(ctx context.Context, rq *formats.Request, p formats.Path, variant string) (*formats.RemoteDocument, error) {
	preq, err := r.request(ctx, rq, p, variant, proxy.Metadata)
	if err != nil {
		return nil, err
	}
	d, err := r.e.Metadata(ctx, preq)
	return r.document(rq, d, err)
}

// Artifact implements [formats.RemoteFetcher].
func (r *remote) Artifact(ctx context.Context, rq *formats.Request, p formats.Path, expected formats.Digests, w http.ResponseWriter) error {
	preq, err := r.request(ctx, rq, p, "", proxy.Artifact)
	if err != nil {
		return err
	}
	preq.Expected = proxy.Digests{SHA256: expected.SHA256, SHA512: expected.SHA512, SHA1: expected.SHA1}
	preq.Serve = func(ctx context.Context, w http.ResponseWriter, it items.Item) error {
		fi := itemOf(rq.RepoRef(), it)
		return r.h.Items().Serve(ctx, rq, w, fi, formats.ServeStored)
	}
	return r.e.Artifact(ctx, preq, w, rq.Request)
}

// Cached implements [formats.RemoteFetcher].
func (r *remote) Cached(ctx context.Context, rq *formats.Request, p formats.Path) (formats.Item, bool, error) {
	preq, err := r.request(ctx, rq, p, "", proxy.Artifact)
	if err != nil {
		return formats.Item{}, false, err
	}
	it, ok, err := r.e.Cached(ctx, preq)
	if err != nil || !ok {
		return formats.Item{}, false, err
	}
	return itemOf(rq.RepoRef(), it), true, nil
}

// EnsureCurrent implements [formats.RemoteFetcher].
func (r *remote) EnsureCurrent(ctx context.Context, rq *formats.Request, p formats.Path, variant string) (*formats.RemoteDocument, formats.Currency, error) {
	preq, err := r.request(ctx, rq, p, variant, proxy.Metadata)
	if err != nil {
		return nil, 0, err
	}
	restored, err := r.h.RestoredAt(ctx)
	if err != nil {
		return nil, 0, err
	}
	d, cur, err := r.e.EnsureCurrent(ctx, preq, restored)
	doc, err := r.document(rq, d, err)
	if err != nil {
		return nil, 0, err
	}
	return doc, currency(cur), nil
}

// EntryState implements [formats.RemoteFetcher].
func (r *remote) EntryState(ctx context.Context, rq *formats.Request, p formats.Path) (formats.EntryState, error) {
	st, err := r.e.EntryState(ctx, rq.RepoRef().ID, p.Path)
	if err != nil {
		return formats.EntryState{}, err
	}
	return formats.EntryState{Stored: st.Stored, GoneAt: st.GoneAt}, nil
}

func currency(c proxy.Currency) formats.Currency {
	switch c {
	case proxy.Current:
		return formats.Current
	case proxy.Revalidated:
		return formats.Revalidated
	}
	return formats.Unknown
}

// request builds the engine's request for a path of the addressed repository.
func (r *remote) request(ctx context.Context, rq *formats.Request, p formats.Path, variant string, kind proxy.Kind) (proxy.Request, error) {
	rem, err := proxy.NewRemote(*rq.Repo)
	if err != nil {
		return proxy.Request{}, errs.E(errs.Internal, errs.CodeInternal, "The repository is not a usable remote repository.", errs.WithCause(err))
	}
	f, ok := r.h.o.Formats(rq.Repo.Format)
	if !ok || f.Remote() == nil {
		return proxy.Request{}, errs.E(errs.Internal, errs.CodeInternal, "The format has no remote adapter.")
	}
	info := f.Classify(p)
	pr := principalOf(ctx)
	preq := proxy.Request{
		Remote: rem, Path: p.Path, Variant: variant, Kind: kind, Package: info.Package, Mode: modeOf(rq),
		Adapter:      newAdapter(f, rq.Repo, rq.RepoRef(), r.h.o.Deps),
		AlwaysVerify: f.Descriptor().VerifyAnnouncedDigests,
		MaxBytes:     f.Descriptor().MaxDocumentBytes,
		Populate:     &populate{h: r.h, rq: rq, coord: populateCoordinate(rq, info, p)},
		Caller: proxy.CallerFacts{
			Principal: pr.ID, Actor: edge.ActorFrom(pr), Source: httpserver.SourceFrom(ctx), Endpoint: rq.Subject.Endpoint,
		},
	}
	return preq, nil
}

func principalOf(ctx context.Context) httpserver.Principal { return httpserver.PrincipalFrom(ctx) }

// modeOf reads how the request treats the upstream: the cache view and
// Cache-Control: only-if-cached never contact it (R-REPO-032).
func modeOf(rq *formats.Request) proxy.Mode {
	switch {
	case rq.CacheView:
		return proxy.CacheView
	case onlyIfCached(rq.Request):
		return proxy.OnlyIfCached
	}
	return proxy.Normal
}

func onlyIfCached(r *http.Request) bool {
	for _, v := range r.Header.Values("Cache-Control") {
		for _, d := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(d), "only-if-cached") {
				return true
			}
		}
	}
	return false
}

// formatSettings is the format's member of the repository document.
func formatSettings(config []byte, id formats.ID) json.RawMessage {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(config, &doc); err != nil {
		return nil
	}
	return doc[string(id)]
}

// document converts the engine's document to the format's.
func (r *remote) document(rq *formats.Request, d *proxy.Document, err error) (*formats.RemoteDocument, error) {
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "The remote engine returned no document.")
	}
	max := int64(0)
	if f, ok := r.h.o.Formats(rq.Repo.Format); ok {
		max = f.Descriptor().MaxDocumentBytes
	}
	out := &formats.RemoteDocument{
		Digest: d.Digest, Size: d.Size, FetchedAt: d.FetchedAt, ValidatedAt: d.ValidatedAt, ExpiresAt: d.ExpiresAt,
		GoneAt: d.GoneAt, Stale: d.Stale,
		Status: formats.CacheStatus{
			Hit: d.Status.Hit, Stale: d.Status.Stale, Stored: d.Status.Stored, Negative: d.Status.Negative,
			Collapsed: d.Status.Collapsed, TTL: d.Status.TTL, Detail: d.Status.Detail, Header: d.Status.Header(),
		},
	}
	if d.Bytes != nil {
		out.Bytes = func(ctx context.Context) ([]byte, error) {
			if max > 0 && d.Size > max {
				return nil, errs.E(errs.UpstreamFailed, errs.CodeUpstreamItemTooLarge, "The document is larger than the format reads into memory.")
			}
			return d.Bytes(ctx)
		}
	}
	return out, nil
}

// adapter adapts a format's RemoteAdapter to the engine's for one request.
type adapter struct {
	f        formats.Format
	ra       formats.RemoteAdapter
	repo     *repository.Repository
	ref      formats.RepoRef
	deps     Deps
	settings json.RawMessage
}

func newAdapter(f formats.Format, repo *repository.Repository, ref formats.RepoRef, deps Deps) *adapter {
	return &adapter{f: f, ra: f.Remote(), repo: repo, ref: ref, deps: deps, settings: formatSettings(repo.Config, f.Descriptor().ID)}
}

var _ proxy.Adapter = (*adapter)(nil)

func (a *adapter) context(base proxy.UpstreamBase) formats.RemoteContext {
	return formats.RemoteContext{Repo: a.repo, Upstream: base.URL, Settings: a.settings}
}

func (a *adapter) UpstreamRequest(ctx context.Context, b proxy.UpstreamBase, path, variant string) (*http.Request, error) {
	return a.ra.UpstreamRequest(ctx, a.context(b), formats.Path{Path: path}, variant)
}

func (a *adapter) AcceptResponse(path string, resp *http.Response) error {
	return a.ra.AcceptResponse(formats.Path{Path: path}, resp)
}

func (a *adapter) PrepareFill(ctx context.Context, path, variant string, body io.ReaderAt, size int64) (proxy.Prepared, error) {
	rc := formats.RemoteContext{Repo: a.repo, Settings: a.settings}
	if u, err := repoUpstream(a.repo); err == nil {
		rc.Upstream = u
	}
	p, err := a.ra.PrepareFill(ctx, rc, formats.Path{Path: path}, variant, body, size)
	if err != nil {
		return proxy.Prepared{}, err
	}
	out := proxy.Prepared{Data: p}
	for _, m := range p.Mappings {
		out.Mappings = append(out.Mappings, proxy.URLMapping{Path: m.Path.Path, Upstream: m.Upstream})
	}
	return out, nil
}

func (a *adapter) OnFill(ctx context.Context, tx meta.Tx, f proxy.FillEvent) (bool, error) {
	w, err := a.deps.writerFor(tx, nil, a.repo.Format)
	if err != nil {
		return false, err
	}
	prepared, _ := f.Prepared.Data.(formats.Prepared)
	return a.ra.OnFill(ctx, tx, formats.FillEvent{
		Repo: a.ref, Path: formats.Path{Path: f.Path}, Variant: f.Variant, Digest: f.Digest, Replaces: f.Replaces,
		Prepared: prepared, StoredAt: f.StoredAt, StoredDigest: f.StoredDigest, ETag: f.ETag, LastModified: f.LastModified,
		Packages: w, Data: a.deps.Bindings.Get(a.f.Descriptor().ID),
	})
}

func (a *adapter) TracksVersionRemoval() bool { return a.ra.TracksVersionRemoval() }

func (a *adapter) PackageDocumentPath(name string) (string, bool) {
	p, ok := a.f.PackageDocumentPath(name)
	return p.Path, ok
}

// OwnPath is true for a path the format classifies under the package: the URL map
// records only those (R-REPO-043).
func (a *adapter) OwnPath(pkg, path string) bool {
	return pkg != "" && a.f.Classify(formats.Path{Path: path}).Package == pkg
}

// repoUpstream parses the upstream URL of a remote repository.
func repoUpstream(r *repository.Repository) (*url.URL, error) {
	s, ok := r.Remote()
	if !ok {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "not a remote repository")
	}
	return url.Parse(s.URL)
}

// populate is the engine's populate check over the decision point (R-AUTHZ-008).
type populate struct {
	h     *Host
	rq    *formats.Request
	coord authz.Coordinate
	d     authz.Decision
	asked bool
}

var _ proxy.Populate = (*populate)(nil)

func populateCoordinate(rq *formats.Request, info formats.PathInfo, p formats.Path) authz.Coordinate {
	return authz.Coordinate{
		Repository: rq.Repo.Key, Class: rq.Repo.Class, Format: rq.Repo.Format, Kind: rq.Route.Kind.RequestKind(),
		Path: p.Path, Package: info.Package, Version: info.Version, Actions: []formats.Action{formats.ActionRead},
	}
}

// May asks the question: nothing is audited.
func (p *populate) May(ctx context.Context) bool {
	p.d = p.h.o.Decider.MayPopulateOn(ctx, p.rq.Index, p.rq.Subject, p.coord)
	p.asked = true
	return p.d.Allowed()
}

// Deny is the refusal of a request that has nothing to serve: audited as
// populate_not_permitted, or the read refusal itself.
func (p *populate) Deny(ctx context.Context) error {
	d := p.h.o.Decider.PopulateOn(ctx, p.rq.Index, p.rq.Subject, p.coord)
	if d.Allowed() {
		return nil
	}
	return d.Err()
}

// Record writes the allowed populate event in the cache-fill transaction.
func (p *populate) Record(ctx context.Context, tx meta.Tx) error {
	if !p.asked {
		p.May(ctx)
	}
	c := p.coord
	c.Kind = authz.KindWrite
	return p.h.o.Decider.RecordAllowed(ctx, tx, p.rq.Subject, c, p.d)
}

// Endpoint names the endpoint and whether it fills caches (R-EP-019).
func (p *populate) Endpoint() (string, bool) { return p.rq.Subject.Endpoint, p.rq.Subject.CacheFill }
