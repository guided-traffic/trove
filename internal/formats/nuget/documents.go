package nuget

import (
	"context"
	"net/http"
	"strconv"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/nuget/document"
)

// The machinery the rendered documents share (R-NUGET-002, R-NUGET-008, R-NUGET-009): the
// version list of the flat container and the three registration documents. A document is
// rendered at request time from committed rows (R-ARCH-012). Every request reads
// packages.rev_seq with one indexed lookup, derives the weak validator and the key of the
// replica's rendition cache from it without rendering, and renders only on a miss. Nothing
// is cached under a key a commit can outlive, so no message between replicas is involved
// (R-NUGET-030). On an endpoint with exposure rules the validator is derived from the
// filtered rendering instead, so it does not move with versions the endpoint hides
// (Q-NUGET-87).

// documentStore finds the data port a request's Host bound for the format.
func documentStore(h formats.Host) (Store, error) {
	st, ok := h.FormatData(ID).(Store)
	if !ok {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "The NuGet data port is not bound.")
	}
	return st, nil
}

// docSpec describes one document of an ID.
type docSpec struct {
	// variant tells the documents of one ID apart in the validator and the key of the
	// rendition cache: "flat", or the hive with the document.
	variant string
	// gzip says the document has a gzip variant, for a client that accepts it.
	gzip bool
	// settings are the keys of the nuget section that change the bytes of the document.
	settings string
	// render reads the rows and renders the document. rev is the revision of the rows it
	// read, 0 when the render did not read one; found is false when there is no document.
	render func(ctx context.Context, st Store) (body []byte, rev int64, found bool, err error)
}

// serveDocument answers a GET or HEAD of a document of the ID lowerID from the validator,
// the rendition cache or a rendering, in that order (R-NUGET-002). The caller has
// resolved the route; the read permission on the ID is decided here, and an ID the caller
// may not read is answered like one that does not exist (R-AUTHZ-016).
func (f *Format) serveDocument(ctx context.Context, rq *formats.Request, w http.ResponseWriter, lowerID string, spec docSpec) error {
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Package: lowerID, Actions: []formats.Action{formats.ActionRead}}); err != nil {
		return err
	}
	// A package the endpoint does not expose is a missing one, decided before any lookup
	// (R-NUGET-034, R-EP-013).
	view := viewOf(rq)
	if view != nil && !view.Package(lowerID) {
		return errNoSuchResource()
	}
	st, err := documentStore(rq.Host)
	if err != nil {
		return err
	}
	repo := rq.RepoRef()
	row, found, err := rq.Host.Packages().Package(ctx, repo, lowerID)
	if err != nil {
		return err
	}
	if !found {
		return errNoSuchResource()
	}
	links := rq.Host.Links(rq)
	serving := rq.Addressed() + servingSuffix(rq)
	inputs := func(rev int64) document.Inputs {
		return document.Inputs{
			RenderVersion: RenderVersion, Repo: repo.ID, Package: int64(row.Ref.ID), Rev: rev, Variant: spec.variant,
			Serving: serving, BaseURL: links.Base(), Endpoint: rq.Endpoint.Name, Exposure: rq.Endpoint.ExposureFingerprint,
			Settings: strconv.FormatInt(rq.Repo.Version, 10) + "/" + spec.settings,
		}
	}

	key := formats.RenditionKey{
		Repo: repo, Package: lowerID, RevSeq: row.Ref.RevSeq, Variant: spec.variant + "@" + serving, Encoding: encodingOf(false),
		BaseURL: links.Base(), Endpoint: rq.Endpoint.Name, Exposure: rq.Endpoint.ExposureFingerprint,
		Settings: inputs(0).Settings, RenderVersion: RenderVersion,
	}
	if view != nil {
		return serveFiltered(ctx, rq, w, st, spec, key, inputs(0))
	}

	etag := inputs(int64(row.Ref.RevSeq)).ETag()
	if document.IfNoneMatch(rq.Header.Get("If-None-Match"), etag) {
		// A validator of this revision: the rows are the ones the client holds. No
		// rendering and no read of the rows.
		document.NotModified(w, etag, spec.gzip)
		return nil
	}

	gzip := spec.gzip && document.AcceptsGzip(rq.Header.Get("Accept-Encoding"))
	body, hit := rq.Host.Renditions().Get(withEncoding(key, gzip))
	if !hit && gzip {
		// The identity rendering of the revision is cached already: compress it.
		if plain, ok := rq.Host.Renditions().Get(key); ok {
			body, hit = document.Gzip(plain), true
			rq.Host.Renditions().Put(withEncoding(key, true), body)
		}
	}
	if !hit {
		var rev int64
		body, rev, found, err = spec.render(ctx, st)
		if err != nil {
			return err
		}
		if !found {
			return errNoSuchResource()
		}
		// The rows carry the revision they were read at, which may be newer than the
		// lookup above when a write raced it: the rendering is keyed and validated by
		// the revision of its own rows.
		if rev > 0 {
			key.RevSeq, etag = formats.RevSeq(rev), inputs(rev).ETag()
		}
		if int64(len(body)) > MaxDocumentBytes {
			return errs.E(errs.Internal, errs.CodeInternal, "The document is larger than the limit of documents held in memory.")
		}
		rq.Host.Renditions().Put(key, body)
		if gzip {
			body = document.Gzip(body)
			rq.Host.Renditions().Put(withEncoding(key, true), body)
		}
	}
	return document.Reply{ETag: etag, Body: body, Gzip: gzip, Vary: spec.gzip}.Write(w, rq.Method)
}

// serveFiltered answers a document on an endpoint with exposure rules (R-NUGET-034,
// Q-NUGET-87). Its validator follows the filtered rendering instead of the revision: the
// revision rises with every write of the ID, those of versions the endpoint hides included,
// and a validator that rose with it would tell a consumer polling with If-None-Match when a
// version it cannot see was pushed, unlisted, relisted or deleted. The identity rendering of
// the revision comes from the rendition cache or is rendered, so a 304 here costs a cache hit
// or a read of the rows; base holds every other input of the validator.
func serveFiltered(ctx context.Context, rq *formats.Request, w http.ResponseWriter, st Store, spec docSpec, key formats.RenditionKey, base document.Inputs) error {
	cache := rq.Host.Renditions()
	plain, hit := cache.Get(key)
	if !hit {
		body, rev, found, err := spec.render(ctx, st)
		if err != nil {
			return err
		}
		if !found {
			return errNoSuchResource()
		}
		if rev > 0 {
			key.RevSeq = formats.RevSeq(rev)
		}
		if int64(len(body)) > MaxDocumentBytes {
			return errs.E(errs.Internal, errs.CodeInternal, "The document is larger than the limit of documents held in memory.")
		}
		cache.Put(key, body)
		plain = body
	}
	base.Content = document.ContentDigest(plain)
	etag := base.ETag()
	if document.IfNoneMatch(rq.Header.Get("If-None-Match"), etag) {
		document.NotModified(w, etag, spec.gzip)
		return nil
	}
	gzip := spec.gzip && document.AcceptsGzip(rq.Header.Get("Accept-Encoding"))
	body := plain
	if gzip {
		var ok bool
		if body, ok = cache.Get(withEncoding(key, true)); !ok {
			body = document.Gzip(plain)
			cache.Put(withEncoding(key, true), body)
		}
	}
	return document.Reply{ETag: etag, Body: body, Gzip: gzip, Vary: spec.gzip}.Write(w, rq.Method)
}

func encodingOf(gzip bool) string {
	if gzip {
		return "gzip"
	}
	return "identity"
}

func withEncoding(k formats.RenditionKey, gzip bool) formats.RenditionKey {
	k.Encoding = encodingOf(gzip)
	return k
}

// internalError wraps a failure of the rendering: rows the format wrote itself that it
// cannot read back are a fault of the server.
func internalError(err error) error {
	return errs.E(errs.Internal, errs.CodeInternal, "", errs.WithCause(err))
}

// viewOf is the exposure view of the request: what the endpoint exposes of the addressed
// repository (R-NUGET-034). It is nil when the endpoint does not restrict. On an endpoint
// with rules and no view, nothing is exposed.
func viewOf(rq *formats.Request) formats.ExposureView {
	if v := rq.Endpoint.Exposure; v != nil {
		return v
	}
	if rq.Endpoint.Rules {
		return hidesAll{}
	}
	return nil
}

// hidesAll is the view that exposes nothing.
type hidesAll struct{}

func (hidesAll) Package(string) bool         { return false }
func (hidesAll) Version(string, string) bool { return false }

// exposes returns the filter of a view on the versions of one package, nil when the
// endpoint does not restrict.
func exposes(view formats.ExposureView, lowerID string) func(lowerVersion string) bool {
	if view == nil {
		return nil
	}
	return func(v string) bool { return view.Version(lowerID, v) }
}
