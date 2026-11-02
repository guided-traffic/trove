package nuget

import (
	"context"
	"encoding/hex"
	"net/http"
	"strconv"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/nuget/document"
	"github.com/guided-traffic/trove/internal/formats/nuget/merge"
	"github.com/guided-traffic/trove/internal/formats/nuget/registration"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
)

// The documents and files of virtual repositories (R-NUGET-017). The virtual engine plans
// the request and merges the documents of the members; the handler renders the answer from
// the merge for the repository the client addressed. Writes never get here: the dispatcher
// sends them to the deployment target (R-NUGET-019).

// virtualDoc describes one document of an ID of a virtual repository.
type virtualDoc struct {
	kind merge.Kind
	// variant tells the documents of one ID apart in the validator and the key of the
	// rendition cache: "flat", or the hive with the document.
	variant string
	gzip    bool
	// settings are the keys of the nuget section that change the bytes of the document.
	settings string
	// render writes the document from the merged entries; found is false when there is
	// none to show.
	render func(entries []merge.Entry) (body []byte, found bool, err error)
}

// serveVirtual answers a GET or HEAD of a document of the ID lowerID of a virtual
// repository. The read permission on the ID is decided first; the members the caller may not
// read take no part, silently (R-NUGET-013, R-AUTHZ-009). The merge comes from the engine's
// merged-metadata cache, which checks the revision of every local member on each hit, so a
// push to a member is visible at once on every replica (R-ARCH-015). A merge that left out a
// member that could not be asked is partial: it is served, and neither it nor its rendering
// is kept (R-REPO-020).
func (f *Format) serveVirtual(ctx context.Context, rq *formats.Request, w http.ResponseWriter, lowerID string, spec virtualDoc) error {
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Package: lowerID, Actions: []formats.Action{formats.ActionRead}}); err != nil {
		return err
	}
	if view := viewOf(rq); view != nil && !view.Package(lowerID) {
		return errNoSuchResource()
	}
	p, variant := formats.Path{Path: PackageDocumentPath(lowerID)}, VariantFlat
	if spec.kind == merge.Registration {
		p, variant = formats.Path{Path: registrationDocumentPath(lowerID)}, VariantRegistration
	}
	m, err := rq.Host.Resolver().Metadata(ctx, rq, p, f.Classify(p), variant)
	if err != nil {
		return err
	}
	if m.Partial {
		f.replicaOf(rq.Host).virt.skipped(rq.Repo.Key, reasonUnavailable)
	}
	links := rq.Host.Links(rq)
	serving := rq.Addressed()
	settings := strconv.FormatInt(rq.Repo.Version, 10) + "/" + spec.settings
	fingerprint := hex.EncodeToString(m.MergeFingerprint[:])
	// The engine's fingerprint names the members and their revisions, not the ID, so the
	// validator names the ID itself, as the remote's does.
	inputs := document.Inputs{
		RenderVersion: RenderVersion, Repo: rq.RepoRef().ID, Variant: "virtual/" + lowerID + "/" + spec.variant,
		Serving: serving, BaseURL: links.Base(), Endpoint: rq.Endpoint.Name, Exposure: rq.Endpoint.ExposureFingerprint, Settings: settings,
	}
	if m.CacheStatus != "" {
		w.Header().Set("Cache-Status", m.CacheStatus)
	}
	key := formats.RenditionKey{
		Repo: rq.RepoRef(), Package: lowerID, Variant: spec.variant + "@" + serving + "@" + fingerprint,
		Encoding: encodingOf(false), BaseURL: links.Base(), Endpoint: rq.Endpoint.Name, Exposure: rq.Endpoint.ExposureFingerprint,
		Settings: settings, RenderVersion: RenderVersion,
	}
	if viewOf(rq) != nil {
		return serveVirtualFiltered(rq, w, m, spec, key, inputs)
	}
	inputs.Variant += "@" + fingerprint
	etag := inputs.ETag()
	if document.IfNoneMatch(rq.Header.Get("If-None-Match"), etag) {
		document.NotModified(w, etag, spec.gzip)
		return nil
	}

	gzip := spec.gzip && document.AcceptsGzip(rq.Header.Get("Accept-Encoding"))
	cache := rq.Host.Renditions()
	body, hit := cache.Get(withEncoding(key, gzip))
	if !hit && gzip && !m.Partial {
		if plain, ok := cache.Get(key); ok {
			body, hit = document.Gzip(plain), true
			cache.Put(withEncoding(key, true), body)
		}
	}
	if !hit {
		var err error
		if body, err = renderMerged(m, spec); err != nil {
			return err
		}
		if !m.Partial {
			cache.Put(key, body)
		}
		if gzip {
			body = document.Gzip(body)
			if !m.Partial {
				cache.Put(withEncoding(key, true), body)
			}
		}
	}
	return document.Reply{ETag: etag, Body: body, Gzip: gzip, Vary: spec.gzip}.Write(w, rq.Method)
}

// serveVirtualFiltered answers a merged document on an endpoint with exposure rules
// (R-NUGET-034, Q-NUGET-87). Its validator follows the filtered rendering instead of the merge
// fingerprint, which changes with every write of a local member and every new upstream
// document, those of versions the endpoint hides included. The identity rendering comes from
// the rendition cache or is rendered from the merge, so a 304 here costs a cache hit or a
// rendering; a partial merge is rendered and never kept, as elsewhere (R-REPO-020).
func serveVirtualFiltered(rq *formats.Request, w http.ResponseWriter, m *formats.Merged, spec virtualDoc, key formats.RenditionKey, inputs document.Inputs) error {
	cache := rq.Host.Renditions()
	plain, hit := cache.Get(key)
	if !hit {
		var err error
		if plain, err = renderMerged(m, spec); err != nil {
			return err
		}
		if !m.Partial {
			cache.Put(key, plain)
		}
	}
	inputs.Content = document.ContentDigest(plain)
	etag := inputs.ETag()
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
			if !m.Partial {
				cache.Put(withEncoding(key, true), body)
			}
		}
	}
	return document.Reply{ETag: etag, Body: body, Gzip: gzip, Vary: spec.gzip}.Write(w, rq.Method)
}

// renderMerged renders the identity bytes of a document from a merge: a merge that leaves
// nothing to show is a missing document.
func renderMerged(m *formats.Merged, spec virtualDoc) ([]byte, error) {
	entries, err := merge.Decode(m.Bytes)
	if err != nil {
		return nil, err
	}
	body, found, err := spec.render(entries)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errNoSuchResource()
	}
	if int64(len(body)) > MaxDocumentBytes {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "The document is larger than the limit of documents held in memory.")
	}
	return body, nil
}

// virtualFlat is the version list of an ID through a virtual repository: the union of the
// versions of the members that remain, listed and unlisted, ascending (R-NUGET-017).
func (f *Format) virtualFlat(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	lowerID := rq.Params.Get("id")
	keep := exposes(viewOf(rq), lowerID)
	return f.serveVirtual(ctx, rq, w, lowerID, virtualDoc{
		kind: merge.Flat, variant: "flat",
		render: func(entries []merge.Entry) ([]byte, bool, error) {
			list := make([]string, 0, len(entries))
			for _, e := range entries {
				if keep == nil || keep(e.Version) {
					list = append(list, e.Version)
				}
			}
			if len(list) == 0 {
				return nil, false, nil
			}
			body, err := document.VersionList(list)
			return body, err == nil, wrapInternal(err)
		},
	})
}

// virtualRegistration is a registration document of an ID through a virtual repository:
// the union of the leaves of the members that remain, each version with the entry of the
// member tier resolution picks for its package, then the paging and the addresses of
// R-NUGET-009 for the virtual repository (R-NUGET-017).
func (f *Format) virtualRegistration(ctx context.Context, rq *formats.Request, w http.ResponseWriter, kind registrationKind) error {
	hive, ok := registration.ParseHive(rq.Params.Get("hive"))
	if !ok {
		return errNoSuchResource()
	}
	lowerID := rq.Params.Get("id")
	paging := f.replicaOf(rq.Host).cfg.Registration
	pg := registration.Paging{PageSize: paging.PageSize, InlineBelow: paging.InlineBelow}
	doc := registration.Doc{
		Feed:    registration.Feed(rq.Host.Links(rq).Registry(rq.Addressed()+servingSuffix(rq)) + FeedSegment + "/"),
		Hive:    hive,
		LowerID: lowerID,
		Paging:  pg,
	}
	keep := exposes(viewOf(rq), lowerID)
	variant := string(hive)
	lower, upper, ver := rq.Params.Get("lower"), rq.Params.Get("upper"), rq.Params.Get("version")
	switch kind {
	case kindIndex:
		variant += "/index"
	case kindPage:
		variant += "/page/" + lower + "/" + upper
	case kindLeaf:
		variant += "/leaf/" + ver
	}
	return f.serveVirtual(ctx, rq, w, lowerID, virtualDoc{
		kind: merge.Registration, variant: variant, gzip: hive.Gzipped(),
		settings: strconv.Itoa(pg.PageSize) + "/" + strconv.Itoa(pg.InlineBelow),
		render: func(entries []merge.Entry) ([]byte, bool, error) {
			all := make([]rows.RegistrationVersion, len(entries))
			doc.Extras = map[string]registration.Passthrough{}
			for i, e := range entries {
				var x registration.Passthrough
				all[i], x = versionOf(e)
				if x.Deprecation != nil || x.Vulnerabilities != nil {
					doc.Extras[e.Version] = x
				}
			}
			vs := registration.Visible(all, hive, keep)
			if len(vs) == 0 {
				return nil, false, nil
			}
			var (
				body  []byte
				found = true
				err   error
			)
			switch kind {
			case kindIndex:
				body, err = doc.Index(vs)
			case kindPage:
				body, found, err = doc.Page(vs, lower, upper)
			case kindLeaf:
				body, found, err = doc.Leaf(vs, ver)
			}
			return body, found && err == nil, wrapInternal(err)
		},
	})
}

// virtualFile serves a file of a version through a virtual repository: the tiers of
// R-REPO-019 step 6 (the local members, the caches of the remote members, then their
// upstreams), the first member that has it wins and the answer names it in
// Trove-Resolved-From. A version the endpoint hides is a missing one.
func (f *Format) virtualFile(ctx context.Context, rq *formats.Request, w http.ResponseWriter, lowerID, ver, path, mediaType string) error {
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Package: lowerID, Version: ver, Actions: []formats.Action{formats.ActionRead}}); err != nil {
		return err
	}
	if view := viewOf(rq); view != nil && (!view.Package(lowerID) || !view.Version(lowerID, ver)) {
		return errNoSuchResource()
	}
	p := formats.Path{Path: path}
	return rq.Host.Resolver().Artifact(ctx, rq, p, f.Classify(p), mediaTypeWriter{ResponseWriter: w, mediaType: mediaType})
}

// wrapInternal wraps the failure of a rendering; nil stays nil.
func wrapInternal(err error) error {
	if err == nil {
		return nil
	}
	return internalError(err)
}
