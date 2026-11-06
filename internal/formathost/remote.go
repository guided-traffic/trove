package formathost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/api/edge"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/bastionmain"
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
	Exchange(ctx context.Context, rem proxy.Remote, req *http.Request, caller proxy.CallerFacts) (*http.Response, error)
	Query(ctx context.Context, rq proxy.QueryRequest) (*proxy.QueryResponse, error)
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

// metadataSince is Metadata for the discovery of a pass: a stored document counts as current only if it was validated after
// since (the engine's restore bound, Q-NPM-13, used for the start of the pass's attempt).
func (r *remote) metadataSince(ctx context.Context, rq *formats.Request, p formats.Path, variant string, since time.Time) (*formats.RemoteDocument, error) {
	preq, err := r.request(ctx, rq, p, variant, proxy.Metadata)
	if err != nil {
		return nil, err
	}
	d, _, err := r.e.EnsureCurrent(ctx, preq, since)
	return r.document(rq, d, err)
}

// Artifact implements [formats.RemoteFetcher].
func (r *remote) Artifact(ctx context.Context, rq *formats.Request, p formats.Path, expected formats.Digests, w http.ResponseWriter) error {
	preq, err := r.request(ctx, rq, p, "", proxy.Artifact)
	if err != nil {
		return err
	}
	if expected.SHA256 == nil && expected.SHA512 == nil && expected.SHA1 == nil {
		// The format's marks and the digests its metadata announces (BeforeArtifact). A cached artifact is read first, in one
		// statement with the rows the gate decides on, so that the hit costs no second one (R-ARCH-027): the gate works on
		// those rows and the engine serves the item it read.
		hit, cached, herr := r.e.Cached(ctx, preq)
		if herr != nil && !proxy.IsLost(herr) { // a lost item is a miss here: the engine fills it again (Q-REPO-202)
			noteRemote(rq, herr)
			return herr
		}
		if cached {
			var facts formats.ServeFacts
			if expected, facts, err = r.gateWith(ctx, rq, p, hit.Facts); err != nil {
				noteRemote(rq, err)
				return err
			}
			if facts.Refreshed {
				// The gate asked the upstream after the lookup: the item, its entry and the verdict of the scanning are read
				// again by the engine before anything is served.
				preq.Hit = nil
			} else {
				hit.Facts = facts
				preq.Hit = &hit
			}
		} else if expected, err = r.gate(ctx, rq, p); err != nil {
			noteRemote(rq, err)
			return err
		}
	}
	preq.Expected = proxy.Digests{SHA256: expected.SHA256, SHA512: expected.SHA512, SHA1: expected.SHA1}
	preq.Serve = func(ctx context.Context, w http.ResponseWriter, it items.Item) error {
		fi := itemOf(rq.RepoRef(), it)
		return r.h.Items().Serve(ctx, rq, w, fi, formats.ServeStored)
	}
	// The headers edge.ServeItem gives a stored answer that do not depend on the body, for the
	// answer the engine streams while it fetches (Q-NUGET-86).
	preq.Headers = func(h http.Header) {
		h.Set("Accept-Ranges", "bytes")
		h.Set("Cache-Control", edge.CacheControl(rq.Request))
	}
	err = r.e.Artifact(ctx, preq, w, rq.Request)
	noteRemote(rq, err)
	return err
}

// noteRemote tells the marking of a Bastion request what an answer of the remote engine means (R-BAS-013): a
// NotFound the engine marked absent is a lookup that found the item missing for every subject, any other
// NotFound (not cached offline, only-if-cached, a gone package, a format's gate) is a 404 that proves nothing.
func noteRemote(rq *formats.Request, err error) {
	switch {
	case err == nil:
	case errs.IsAbsent(err):
		bastionmain.NoteMissing(rq.Context())
	case errs.KindOf(err) == errs.NotFound:
		bastionmain.NoteUnknown(rq.Context())
	}
}

// gate asks the format's remote adapter (BeforeArtifact) whether the request may have the artifact
// and which digests are announced for it.
func (r *remote) gate(ctx context.Context, rq *formats.Request, p formats.Path) (formats.Digests, error) {
	d, _, err := r.gateWith(ctx, rq, p, formats.ServeFacts{})
	return d, err
}

// gateWith is gate for a caller that holds the rows the lookup of the cached artifact read (have): an adapter that decides on
// them ([formats.FactsGate]) asks for nothing, and returns the facts the serve check of the artifact is to use.
func (r *remote) gateWith(ctx context.Context, rq *formats.Request, p formats.Path, have formats.ServeFacts) (formats.Digests, formats.ServeFacts, error) {
	f, ok := r.h.o.Formats(rq.Repo.Format)
	if !ok || f.Remote() == nil {
		return formats.Digests{}, have, nil
	}
	if g := formats.RemoteCapabilities(formats.ID(rq.Repo.Format)).Gate; g != nil {
		return g.BeforeArtifactWith(ctx, r.h, rq, p, have)
	}
	d, err := f.Remote().BeforeArtifact(ctx, r.h, rq, p)
	return d, have, err
}

// Gate implements [formats.RemoteFetcher]: the format's BeforeArtifact for a request addressed to
// a remote repository, the check a remote member of a virtual repository meets before its cached
// artifact is served (the second tier never asks the upstream for the artifact, but a stale
// document may have to be revalidated first, Q-NPM-13). A format without a gate allows everything.
func (r *remote) Gate(ctx context.Context, rq *formats.Request, p formats.Path) error {
	_, err := r.gate(ctx, rq, p)
	return err
}

// GateWith implements [formats.RemoteFetcher]: Gate for a cached artifact whose item was read with the rows of the format's
// serve check.
func (r *remote) GateWith(ctx context.Context, rq *formats.Request, p formats.Path, have formats.ServeFacts) (formats.ServeFacts, error) {
	_, facts, err := r.gateWith(ctx, rq, p, have)
	return facts, err
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

// Exchange implements [formats.RemoteFetcher]: one request through the repository's
// upstream client, outside the cache (R-NPM-038).
func (r *remote) Exchange(ctx context.Context, rq *formats.Request, x formats.Exchange) (*http.Response, error) {
	rem, err := proxy.NewRemote(*rq.Repo)
	if err != nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "The repository is not a usable remote repository.", errs.WithCause(err))
	}
	base := x.Base
	if base == nil {
		if base, err = url.Parse(rem.Settings.URL); err != nil {
			return nil, errs.E(errs.Internal, errs.CodeInternal, "The upstream URL of the repository does not parse.", errs.WithCause(err))
		}
	}
	req, err := http.NewRequestWithContext(ctx, x.Method, base.JoinPath(x.Path).String(), bytes.NewReader(x.Body))
	if err != nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "", errs.WithCause(err))
	}
	if x.Header != nil {
		req.Header = x.Header.Clone()
	}
	pr := principalOf(ctx)
	resp, err := r.e.Exchange(ctx, rem, req, proxy.CallerFacts{
		Principal: pr.ID, Actor: edge.ActorFrom(pr), Source: httpserver.SourceFrom(ctx), Endpoint: rq.Subject.Endpoint,
	})
	if errors.Is(err, proxy.ErrOffline) {
		return nil, formats.ErrOffline
	}
	return resp, err
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
		Remote: rem, Path: p.Path, Variant: variant, Kind: kind, Package: info.Package, Version: info.Version, Mode: modeOf(rq), Addressed: rq.Addressed(),
		Facts:        serveKeyOf(rq.RepoRef(), p),
		Adapter:      newAdapter(f, rq.Repo, rq.RepoRef(), r.h.o.Deps),
		AlwaysVerify: f.Descriptor().VerifyAnnouncedDigests,
		MaxBytes:     f.Descriptor().MaxDocumentBytes,
		Populate:     &populate{h: r.h, rq: rq, coord: populateCoordinate(rq, info, p)},
		Caller: proxy.CallerFacts{
			Principal: pr.ID, Actor: edge.ActorFrom(pr), Source: httpserver.SourceFrom(ctx), Endpoint: rq.Subject.Endpoint,
		},
	}
	if bg, ok := backgroundFrom(ctx); ok {
		// A pass's request, or a request a format makes through Host.Remote() while it works for the pass (its gate
		// revalidating a package document): a background request of the pass (mirror_host.go).
		bg.apply(&preq, rq, info, p)
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
		noteRemote(rq, err)
		return nil, err
	}
	if d != nil && d.Status.Negative {
		// The negative entry stands for an upstream 404 (R-REPO-025): the document is missing for every subject.
		bastionmain.NoteMissing(rq.Context())
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
	// caps are the optional interfaces Register found on the format's remote adapter.
	caps formats.RemoteCaps
	// disc is what the discovery document parsed to; nil until [adapter.Discover] bound it.
	disc any
}

func newAdapter(f formats.Format, repo *repository.Repository, ref formats.RepoRef, deps Deps) *adapter {
	return &adapter{
		f: f, ra: f.Remote(), repo: repo, ref: ref, deps: deps, settings: formatSettings(repo.Config, f.Descriptor().ID),
		caps: formats.RemoteCapabilities(f.Descriptor().ID),
	}
}

var _ proxy.Adapter = (*adapter)(nil)

func (a *adapter) context(base proxy.UpstreamBase) formats.RemoteContext {
	return formats.RemoteContext{Repo: a.repo, Upstream: base.URL, Settings: a.settings, Discovery: a.disc}
}

// remoteContext is the context of a call that runs without the engine's client at hand: the
// upstream URL is the repository's own.
func (a *adapter) remoteContext() formats.RemoteContext {
	rc := formats.RemoteContext{Repo: a.repo, Settings: a.settings, Discovery: a.disc}
	if u, err := repoUpstream(a.repo); err == nil {
		rc.Upstream = u
	}
	return rc
}

func (a *adapter) UpstreamRequest(ctx context.Context, b proxy.UpstreamBase, path, variant string) (*http.Request, error) {
	return a.ra.UpstreamRequest(ctx, a.context(b), formats.Path{Path: path}, variant)
}

func (a *adapter) AcceptResponse(path string, resp *http.Response) error {
	return a.ra.AcceptResponse(formats.Path{Path: path}, resp)
}

func (a *adapter) PrepareFill(ctx context.Context, path, variant string, body io.ReaderAt, size int64) (proxy.Prepared, error) {
	p, err := a.ra.PrepareFill(ctx, a.remoteContext(), formats.Path{Path: path}, variant, body, size)
	if err != nil {
		return proxy.Prepared{}, err
	}
	out := proxy.Prepared{Data: p}
	for _, m := range p.Mappings {
		out.Mappings = append(out.Mappings, proxy.URLMapping{Path: m.Path.Path, Upstream: m.Upstream})
	}
	return out, nil
}

func (a *adapter) OnFill(ctx context.Context, tx meta.Tx, f proxy.FillEvent) (proxy.FillDecision, error) {
	w, err := a.deps.writerForEvent(tx, a.ref, a.repo.Format)
	if err != nil {
		return proxy.FillByFetchTime, err
	}
	prepared, _ := f.Prepared.Data.(formats.Prepared)
	d, err := a.ra.OnFill(ctx, tx, formats.FillEvent{
		Repo: a.ref, Path: formats.Path{Path: f.Path}, Variant: f.Variant, Digest: f.Digest, Replaces: f.Replaces,
		Prepared: prepared, StoredAt: f.StoredAt, FetchedAt: f.FetchedAt, StoredDigest: f.StoredDigest, ETag: f.ETag, LastModified: f.LastModified,
		Packages: w, Data: a.deps.Bindings.Get(a.f.Descriptor().ID),
	})
	switch d {
	case formats.FillKeepStored:
		return proxy.FillKeepStored, err
	case formats.FillReplaceStored:
		return proxy.FillReplaceStored, err
	}
	return proxy.FillByFetchTime, err
}

func (a *adapter) TracksVersionRemoval() bool { return a.ra.TracksVersionRemoval() }

// ExpectsType implements [proxy.ExpectedTypes] over the format's [formats.RemoteAdapter].
func (a *adapter) ExpectsType(path, mediaType string) bool {
	return a.ra.ExpectsType(formats.Path{Path: path}, mediaType)
}

var _ proxy.ExpectedTypes = (*adapter)(nil)

func (a *adapter) PackageDocumentPath(name string) (string, bool) {
	p, ok := a.f.PackageDocumentPath(name)
	return p.Path, ok
}

// OwnPath is true for a path the format classifies under the package: the URL map
// records only those (R-REPO-043).
func (a *adapter) OwnPath(pkg, path string) bool {
	return pkg != "" && a.f.Classify(formats.Path{Path: path}).Package == pkg
}

// DiscoveryDocument implements [proxy.Adapter] over the format's [formats.Discoverer].
func (a *adapter) DiscoveryDocument() (string, string, bool) {
	if a.caps.Discovery == nil {
		return "", "", false
	}
	p, variant, ok := a.caps.Discovery.DiscoveryDocument()
	return p.Path, variant, ok
}

// Discover implements [proxy.Adapter]: the adapter for the rest of the fetch carries the parsed document.
func (a *adapter) Discover(ctx context.Context, doc []byte) (proxy.Adapter, error) {
	if a.caps.Discovery == nil {
		return a, nil
	}
	d, err := a.caps.Discovery.ParseDiscovery(ctx, a.remoteContext(), doc)
	if err != nil {
		return nil, err
	}
	bound := *a
	bound.disc = d
	return &bound, nil
}

// InspectsFill implements [proxy.Adapter] over the format's [formats.FillInspector].
func (a *adapter) InspectsFill(path string) bool {
	return a.caps.Fill != nil && a.caps.Fill.InspectsFill(formats.Path{Path: path})
}

// InspectFill implements [proxy.Adapter]: the format reads the spooled artifact and its
// inspection travels to its OnCommit the way an upload's does.
func (a *adapter) InspectFill(ctx context.Context, path string, sp *items.Spooled) (any, error) {
	if a.caps.Fill == nil {
		return nil, nil
	}
	ins, err := a.caps.Fill.InspectFill(ctx, a.remoteContext(), formats.Path{Path: path}, fillSpool{sp})
	if err != nil {
		return nil, err
	}
	if ins == nil {
		return nil, nil
	}
	return &commitPayload{inspection: ins}, nil
}

// fillSpool is the spooled artifact of a fill as the format sees it.
type fillSpool struct{ s *items.Spooled }

var _ formats.Spooled = fillSpool{}

func (p fillSpool) Digests() formats.Digests {
	return formats.Digests{
		SHA256: append([]byte(nil), p.s.SHA256[:]...), SHA512: append([]byte(nil), p.s.SHA512[:]...),
		SHA1: append([]byte(nil), p.s.SHA1[:]...), MD5: append([]byte(nil), p.s.MD5[:]...),
	}
}

func (p fillSpool) Size() int64 { return p.s.Size }

func (p fillSpool) Open() (io.ReadCloser, error) { return io.NopCloser(p.s.Reader()), nil }

func (p fillSpool) ReaderAt() (io.ReaderAt, error) {
	ra, ok := p.s.Section(0, p.s.Size).(io.ReaderAt)
	if !ok {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "The spooled body cannot be read at an offset.")
	}
	return ra, nil
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
		Repository: rq.Repo.Name, Class: rq.Repo.Class, Format: rq.Repo.Format, Kind: rq.Route.Kind.RequestKind(),
		Path: p.Path, Package: info.Package, Version: info.Version, Actions: []formats.Action{formats.ActionRead},
		Addressed: rq.Addressed(),
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
