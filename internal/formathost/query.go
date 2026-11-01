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
// the repository is served from the cache only.
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
	pr := principalOf(ctx)
	res, err := r.e.Query(ctx, proxy.QueryRequest{
		Remote: rem, Path: q.Path.Path,
		Build: func(ctx context.Context, b proxy.UpstreamBase) (*http.Request, error) {
			return ra.UpstreamQuery(ctx, formats.RemoteContext{Repo: rq.Repo, Upstream: b.URL, Settings: settings}, q)
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
