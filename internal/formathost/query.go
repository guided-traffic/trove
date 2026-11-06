package formathost

import (
	"context"
	"errors"
	"net/http"

	"github.com/guided-traffic/trove/internal/api/edge"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/proxy"
)

// Query implements [formats.RemoteFetcher]: the pass-through of a Volatile request (R-ARCH-013
// step 6). The format's adapter builds the upstream request and checks the response; the engine
// sends it through the repository's client and owns the per-replica cache, the circuit and the
// metrics. The populate check is the request's, as for an artifact, so a caller who may not fill
// the repository is served from the cache only. A format whose upstream addressing follows a
// discovery document gets it resolved, as a fetch does, before its adapter builds the request
// (Q-ARCH-282).
func (r *remote) Query(ctx context.Context, rq *formats.Request, q formats.Query) (*formats.QueryResult, error) {
	rem, err := proxy.NewRemote(*rq.Repo)
	if err != nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "The repository is not a usable remote repository.", errs.WithCause(err))
	}
	f, ok := r.h.o.Formats(rq.Repo.Format)
	if !ok || f.Remote() == nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "The format has no remote adapter.")
	}
	ra, settings := f.Remote(), formatSettings(rq.Repo.Config, f.Descriptor().ID)
	disc := formats.RemoteCapabilities(f.Descriptor().ID).Discovery
	pr := principalOf(ctx)
	res, err := r.e.Query(ctx, proxy.QueryRequest{
		Remote: rem, Path: q.Path.Path,
		Build: func(ctx context.Context, b proxy.UpstreamBase) (*http.Request, error) {
			rc := formats.RemoteContext{Repo: rq.Repo, Upstream: b.URL, Settings: settings}
			if disc != nil {
				d, err := r.discovery(ctx, rq, disc, rc)
				if err != nil {
					return nil, err
				}
				rc.Discovery = d
			}
			return ra.UpstreamQuery(ctx, rc, q)
		},
		Accept: func(path string, resp *http.Response) error {
			return ra.AcceptResponse(formats.Path{Path: path}, resp)
		},
		TTL: q.TTL, MaxBytes: f.Descriptor().MaxDocumentBytes, Mode: modeOf(rq),
		Populate: &populate{h: r.h, rq: rq, coord: populateCoordinate(rq, f.Classify(q.Path), q.Path)},
		Caller: proxy.CallerFacts{
			Principal: pr.ID, Actor: edge.ActorFrom(pr), Source: httpserver.SourceFrom(ctx), Endpoint: rq.Subject.Endpoint,
		},
	})
	if errors.Is(err, proxy.ErrOffline) {
		return nil, formats.ErrOffline
	}
	if err != nil {
		return nil, err
	}
	return &formats.QueryResult{
		ContentType: res.ContentType, Body: res.Body, FetchedAt: res.FetchedAt, Cached: res.Cached, Collapsed: res.Collapsed,
	}, nil
}

// discovery resolves the discovery document of the repository for a query, the way the engine's
// fetch resolves it before it builds an upstream request (R-NUGET-014, Q-ARCH-282): the stored
// metadata document, fresh or revalidated, read with the caller's mode and populate check, parsed
// by the format. A caller who may not fill the repository is served the stored document; when
// none is stored, the query is answered as one that may not contact the upstream (ErrOffline),
// and no refused fill is audited, because a query never brings content into the repository.
func (r *remote) discovery(ctx context.Context, rq *formats.Request, d formats.Discoverer, rc formats.RemoteContext) (any, error) {
	p, variant, ok := d.DiscoveryDocument()
	if !ok {
		return nil, nil
	}
	preq, err := r.request(ctx, rq, p, variant, proxy.Metadata)
	if err != nil {
		return nil, err
	}
	if pop, ok := preq.Populate.(*populate); ok {
		preq.Populate = quietPopulate{pop}
	}
	stored, err := r.e.Metadata(ctx, preq)
	if e, ok := errs.As(err); ok && e.Code == errs.CodePopulateNotPermitted {
		return nil, proxy.ErrOffline
	}
	doc, err := r.document(rq, stored, err)
	if err != nil {
		return nil, err
	}
	if doc.Status.Negative || doc.Bytes == nil {
		return nil, errs.E(errs.UpstreamFailed, errs.CodeUpstreamError, "The upstream of "+rq.Repo.Name+" has no discovery document, so the addresses of its resources are unknown.")
	}
	b, err := doc.Bytes(ctx)
	if err != nil {
		return nil, err
	}
	return d.ParseDiscovery(ctx, rc, b)
}

// quietPopulate is the populate check of the discovery document a query reads on its way: a
// refusal answers the query from the cache (ErrOffline), as the engine answers a query a caller
// may not fill, and is not audited as a refused fill (Q-ARCH-270).
type quietPopulate struct{ *populate }

// Deny refuses without an audit event.
func (quietPopulate) Deny(context.Context) error { return nil }
