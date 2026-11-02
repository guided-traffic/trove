package nuget

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/nuget/document"
	"github.com/guided-traffic/trove/internal/formats/nuget/registration"
	"github.com/guided-traffic/trove/internal/formats/nuget/remote"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
)

// The documents and files of a remote repository (R-NUGET-008, R-NUGET-009, R-NUGET-015).
// A package file is the cached item or one fetch under the collapsing protocol of the engine. A
// document is rendered from the stored upstream documents: the version list from the stored
// list, a registration document from the stored index and the pages it does not inline, with
// the hive's SemVer 2.0.0 filter, trove's paging and trove's addresses. The stored bytes are
// never altered (R-NUGET-015); what changes is the rendering, a function of them and of the
// serving repository, so every replica renders the same bytes (R-NUGET-030).

// remoteSource is what a request resolved from the stored documents before it renders.
type remoteSource struct {
	// fingerprint is a function of the stored documents the rendering reads: the validator and
	// the key of the replica's rendition cache derive from it without a rendering.
	fingerprint string
	// status is the Cache-Status of the document the request resolved, "" for none.
	status string
	// render renders the document, found false when it does not exist (an ID without a visible
	// version in the hive, a leaf that is not listed).
	render func(ctx context.Context) (body []byte, found bool, err error)
}

// remoteSpec describes one document of an ID of a remote repository.
type remoteSpec struct {
	variant  string
	gzip     bool
	settings string
	resolve  func(ctx context.Context) (remoteSource, error)
}

// serveRemote answers a GET or HEAD of a document of the ID lowerID of a remote repository:
// the read permission and the exposure of the endpoint first, then the stored documents (which
// the engine keeps fresh, refreshes or serves stale), then the validator, the rendition cache
// or a rendering, in that order. An ID the caller may not read is answered like one that does
// not exist (R-AUTHZ-016).
func (f *Format) serveRemote(ctx context.Context, rq *formats.Request, w http.ResponseWriter, lowerID string, spec remoteSpec) error {
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Package: lowerID, Actions: []formats.Action{formats.ActionRead}}); err != nil {
		return err
	}
	if view := viewOf(rq); view != nil && !view.Package(lowerID) {
		return errNoSuchResource()
	}
	src, err := spec.resolve(ctx)
	if err != nil {
		return err
	}
	repo := rq.RepoRef()
	links := rq.Host.Links(rq)
	serving := rq.Addressed() + servingSuffix(rq)
	settings := strconv.FormatInt(rq.Repo.Version, 10) + "/" + spec.settings + "/" + src.fingerprint
	etag := document.Inputs{
		RenderVersion: RenderVersion, Repo: repo.ID, Variant: "remote/" + lowerID + "/" + spec.variant, Serving: serving,
		BaseURL: links.Base(), Endpoint: rq.Endpoint.Name, Exposure: rq.Endpoint.ExposureFingerprint, Settings: settings,
	}.ETag()
	if src.status != "" {
		w.Header().Set("Cache-Status", src.status)
	}
	if document.IfNoneMatch(rq.Header.Get("If-None-Match"), etag) {
		document.NotModified(w, etag, spec.gzip)
		return nil
	}

	gzip := spec.gzip && document.AcceptsGzip(rq.Header.Get("Accept-Encoding"))
	key := formats.RenditionKey{
		Repo: repo, Package: lowerID, Variant: "remote/" + spec.variant + "@" + serving, Encoding: encodingOf(false),
		BaseURL: links.Base(), Endpoint: rq.Endpoint.Name, Exposure: rq.Endpoint.ExposureFingerprint,
		Settings: settings, RenderVersion: RenderVersion,
	}
	body, hit := rq.Host.Renditions().Get(withEncoding(key, gzip))
	if !hit && gzip {
		if plain, ok := rq.Host.Renditions().Get(key); ok {
			body, hit = document.Gzip(plain), true
			rq.Host.Renditions().Put(withEncoding(key, true), body)
		}
	}
	if !hit {
		var found bool
		if body, found, err = src.render(ctx); err != nil {
			return err
		}
		if !found {
			return errNoSuchResource()
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

// storedDocument reads a metadata document of the remote through the engine: fresh, refreshed
// or stale. The negative entry of a path the upstream does not have is the 404 of a missing
// resource.
func storedDocument(ctx context.Context, rq *formats.Request, path string) (*formats.RemoteDocument, error) {
	doc, err := rq.Host.Remote().Metadata(ctx, rq, formats.Path{Path: path}, documentVariant)
	if err != nil {
		return nil, err
	}
	if doc.Status.Negative {
		return nil, errs.E(errs.NotFound, errs.CodeNotFound, "The resource does not exist.", errs.WithHeader("Cache-Status", doc.Status.Header))
	}
	return doc, nil
}

func digestHex(d [32]byte) string { return hex.EncodeToString(d[:]) }

// remoteFlatIndex serves the version list of an ID of a remote repository (R-NUGET-008): the
// stored list, rendered in URL form and ascending by precedence.
func (f *Format) remoteFlatIndex(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	lowerID := rq.Params.Get("id")
	keep := exposes(viewOf(rq), lowerID)
	return f.serveRemote(ctx, rq, w, lowerID, remoteSpec{
		variant: "flat",
		resolve: func(ctx context.Context) (remoteSource, error) {
			doc, err := storedDocument(ctx, rq, PackageDocumentPath(lowerID))
			if err != nil {
				return remoteSource{}, err
			}
			return remoteSource{
				fingerprint: digestHex(doc.Digest), status: doc.Status.Header,
				render: func(ctx context.Context) ([]byte, bool, error) {
					raw, err := doc.Bytes(ctx)
					if err != nil {
						return nil, false, err
					}
					list, err := remote.VersionList(raw, MaxDocumentBytes)
					if err != nil {
						return nil, false, err
					}
					return renderVersionList(list, keep)
				},
			}, nil
		},
	})
}

// renderVersionList renders the version list: the versions in URL form, ascending, those keep
// accepts. found is false for a list without a version, which has no document.
func renderVersionList(list []string, keep func(lowerVersion string) bool) (body []byte, found bool, err error) {
	vs := sortedURLForms(list)
	if keep != nil {
		vs = slices.DeleteFunc(vs, func(v string) bool { return !keep(v) })
	}
	if len(vs) == 0 {
		return nil, false, nil
	}
	body, err = document.VersionList(vs)
	if err != nil {
		return nil, false, internalError(err)
	}
	return body, true, nil
}

// remoteFile serves a package file of a remote repository: the cached item, or one fetch
// under the collapsing protocol (R-NUGET-015). The permission on the version and the exposure
// of the endpoint are decided first, so a hidden version costs no fetch (R-NUGET-034).
func (f *Format) remoteFile(ctx context.Context, rq *formats.Request, w http.ResponseWriter, lowerID, ver, path, mediaType string) error {
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Package: lowerID, Version: ver, Actions: []formats.Action{formats.ActionRead}}); err != nil {
		return err
	}
	if view := viewOf(rq); view != nil && (!view.Package(lowerID) || !view.Version(lowerID, ver)) {
		return errNoSuchResource()
	}
	return rq.Host.Remote().Artifact(ctx, rq, formats.Path{Path: path}, formats.Digests{}, mediaTypeWriter{ResponseWriter: w, mediaType: mediaType})
}

// pageBound is a registration page the index does not inline, by its bounds in URL form.
type pageBound struct{ lower, upper string }

// maxPageFetches bounds the pages of one index that are fetched at the same time.
const maxPageFetches = 8

// remoteRegistration serves a registration document of an ID of a remote repository
// (R-NUGET-009, R-NUGET-015): the stored index and the pages it does not inline are read
// through the engine, then the hive's document is rendered from them.
func (f *Format) remoteRegistration(ctx context.Context, rq *formats.Request, w http.ResponseWriter, kind registrationKind) error {
	hive, ok := registration.ParseHive(rq.Params.Get("hive"))
	if !ok {
		return errNoSuchResource()
	}
	lowerID := rq.Params.Get("id")
	paging := f.replicaOf(rq.Host).cfg.Registration
	pg := registration.Paging{PageSize: paging.PageSize, InlineBelow: paging.InlineBelow}
	links := rq.Host.Links(rq)
	feed := registration.Feed(links.Registry(rq.Addressed()+servingSuffix(rq)) + FeedSegment + "/")
	keep := exposes(viewOf(rq), lowerID)
	lower, upper, ver := rq.Params.Get("lower"), rq.Params.Get("upper"), rq.Params.Get("version")

	variant := string(hive)
	switch kind {
	case kindIndex:
		variant += "/index"
	case kindPage:
		variant += "/page/" + lower + "/" + upper
	case kindLeaf:
		variant += "/leaf/" + ver
	}
	return f.serveRemote(ctx, rq, w, lowerID, remoteSpec{
		variant:  variant,
		gzip:     hive.Gzipped(),
		settings: strconv.Itoa(pg.PageSize) + "/" + strconv.Itoa(pg.InlineBelow),
		resolve: func(ctx context.Context) (remoteSource, error) {
			idx, err := storedDocument(ctx, rq, RegistrationIndexPath(lowerID))
			if err != nil {
				return remoteSource{}, err
			}
			bounds, err := f.pageBounds(ctx, rq, lowerID, idx)
			if err != nil {
				return remoteSource{}, err
			}
			pages, err := storedPages(ctx, rq, lowerID, bounds, false)
			if err != nil {
				return remoteSource{}, err
			}
			h := sha256.New()
			h.Write(idx.Digest[:])
			for _, p := range pages {
				h.Write(p.Digest[:])
			}
			return remoteSource{
				fingerprint: hex.EncodeToString(h.Sum(nil)), status: idx.Status.Header,
				render: func(ctx context.Context) ([]byte, bool, error) {
					vs, err := remoteVersions(ctx, lowerID, idx, pages)
					if err != nil {
						return nil, false, err
					}
					return renderRegistration(registrationRender{
						feed: feed, hive: hive, lowerID: lowerID, paging: pg, versions: vs, keep: keep,
						kind: kind, lower: lower, upper: upper, version: ver,
					})
				},
			}, nil
		},
	})
}

// pageBounds lists the pages of the stored index that it does not inline, in its order. The
// list is a function of the stored bytes, so the replica's rendition cache holds it under their
// digest and the index is parsed once, not once per request (an index that inlines every page
// is the large one).
func (f *Format) pageBounds(ctx context.Context, rq *formats.Request, lowerID string, idx *formats.RemoteDocument) ([]pageBound, error) {
	key := formats.RenditionKey{
		Repo: rq.RepoRef(), Package: lowerID, Variant: "remote/pages", Encoding: encodingOf(false),
		Settings: digestHex(idx.Digest), RenderVersion: RenderVersion,
	}
	if cached, ok := rq.Host.Renditions().Get(key); ok {
		return decodeBounds(cached), nil
	}
	raw, err := idx.Bytes(ctx)
	if err != nil {
		return nil, err
	}
	parsed, err := remote.ParseIndex(raw, MaxDocumentBytes)
	if err != nil {
		return nil, err
	}
	out, err := boundsOf(parsed)
	if err != nil {
		return nil, err
	}
	rq.Host.Renditions().Put(key, encodeBounds(out))
	return out, nil
}

// boundsOf lists the pages of an index that it does not inline, in its order, by their bounds in
// URL form. ParseIndex has refused an index of more than remote.MaxPages pages, so the list is
// bounded (Q-NUGET-83).
func boundsOf(idx *remote.Index) ([]pageBound, error) {
	var out []pageBound
	for _, p := range idx.Pages {
		if p.Inline {
			continue
		}
		lo, ok1 := urlForm(p.Lower)
		hi, ok2 := urlForm(p.Upper)
		if !ok1 || !ok2 {
			return nil, errs.E(errs.UpstreamFailed, errs.CodeNuGetInvalidUpstreamDocument, "A page of the upstream's registration index has bounds that are no versions.")
		}
		out = append(out, pageBound{lo, hi})
	}
	return out, nil
}

func encodeBounds(bs []pageBound) []byte {
	var b strings.Builder
	for _, p := range bs {
		b.WriteString(p.lower + " " + p.upper + "\n")
	}
	return []byte(b.String())
}

func decodeBounds(b []byte) []pageBound {
	var out []pageBound
	for _, line := range strings.Split(string(b), "\n") {
		if lo, hi, ok := strings.Cut(line, " "); ok {
			out = append(out, pageBound{lo, hi})
		}
	}
	return out
}

// storedPages reads the pages of an index through the engine, several at a time. A page the
// index lists and the upstream does not have is an upstream that contradicts itself. With
// unfillable set, a page the caller may not bring into the cache (populate_not_permitted) is
// left out, nil in the result: the virtual merge of a member the caller may not fill lists only
// cached versions anyway (R-AUTHZ-008).
func storedPages(ctx context.Context, rq *formats.Request, lowerID string, bounds []pageBound, unfillable bool) ([]*formats.RemoteDocument, error) {
	docs := make([]*formats.RemoteDocument, len(bounds))
	errsOf := make([]error, len(bounds))
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxPageFetches)
	for i, b := range bounds {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			docs[i], errsOf[i] = storedDocument(ctx, rq, RegistrationPagePath(lowerID, b.lower, b.upper))
		}()
	}
	wg.Wait()
	for i, err := range errsOf {
		if err == nil {
			continue
		}
		if e, ok := errs.As(err); ok && unfillable && e.Kind == errs.Forbidden && e.Code == errs.CodePopulateNotPermitted {
			docs[i] = nil
			continue
		}
		if e, ok := errs.As(err); ok && e.Kind == errs.NotFound {
			return nil, errs.E(errs.UpstreamFailed, errs.CodeNuGetInvalidUpstreamDocument,
				"The upstream's registration index of "+lowerID+" lists the page "+bounds[i].lower+" to "+bounds[i].upper+" and the upstream does not have it.")
		}
		return nil, err
	}
	return docs, nil
}

// remoteVersions reads the leaves of the index and of its pages and returns the versions,
// ascending, within the budget of one rendering (Q-NUGET-83).
func remoteVersions(ctx context.Context, lowerID string, idx *formats.RemoteDocument, pages []*formats.RemoteDocument) ([]remote.Version, error) {
	raw, err := idx.Bytes(ctx)
	if err != nil {
		return nil, err
	}
	b := remote.NewBudget(MaxDocumentBytes)
	parsed, err := b.Index(raw)
	if err != nil {
		return nil, err
	}
	return registrationVersions(ctx, lowerID, parsed, pages, b)
}

// registrationVersions converts the leaves of a parsed index and of the stored pages it does not
// inline into versions, ascending. The pages are read one at a time within the budget, and the
// leaves of a page are converted before the next one is read, so a rendering holds at most the
// budget's bytes of upstream documents and its leaves (Q-NUGET-83). A nil page was left out
// (storedPages).
func registrationVersions(ctx context.Context, lowerID string, idx *remote.Index, pages []*formats.RemoteDocument, b *remote.Budget) ([]remote.Version, error) {
	c := remote.NewCollector(lowerID)
	for _, p := range idx.Pages {
		c.Add(p.Leaves)
	}
	for _, d := range pages {
		if d == nil {
			continue
		}
		raw, err := d.Bytes(ctx)
		if err != nil {
			return nil, err
		}
		ls, err := b.Page(raw)
		if err != nil {
			return nil, err
		}
		c.Add(ls)
	}
	return c.Versions(), nil
}

// registrationRender is one registration document to render from the versions of a remote.
type registrationRender struct {
	feed    registration.Feed
	hive    registration.Hive
	lowerID string
	paging  registration.Paging
	// versions are the versions of the upstream, ascending, keep the exposure filter of the
	// endpoint (nil keeps all).
	versions []remote.Version
	keep     func(lowerVersion string) bool
	kind     registrationKind
	// lower and upper are the bounds of a page, version the URL form of a leaf.
	lower, upper, version string
}

// renderRegistration renders the index, a page or a leaf of the hive from the versions of an
// upstream's registration, with trove's addresses and trove's paging (R-NUGET-009). The
// versions the hive does not show (R-NUGET-004) and those the endpoint hides are left out
// before the paging; an ID with no version left has no document.
func renderRegistration(r registrationRender) (body []byte, found bool, err error) {
	rowsOf := make([]rows.RegistrationVersion, len(r.versions))
	extras := make(map[string]registration.Passthrough, len(r.versions))
	for i, v := range r.versions {
		rowsOf[i] = v.Row
		extras[v.Row.LowerVersion] = v.Extras
	}
	vs := registration.Visible(rowsOf, r.hive, r.keep)
	if len(vs) == 0 {
		return nil, false, nil
	}
	doc := registration.Doc{Feed: r.feed, Hive: r.hive, LowerID: r.lowerID, Paging: r.paging, Extras: extras, UpstreamIDs: true}
	switch r.kind {
	case kindIndex:
		body, err = doc.Index(vs)
		found = true
	case kindPage:
		body, found, err = doc.Page(vs, r.lower, r.upper)
	case kindLeaf:
		body, found, err = doc.Leaf(vs, r.version)
	}
	if err != nil {
		return nil, false, internalError(err)
	}
	return body, found, nil
}
