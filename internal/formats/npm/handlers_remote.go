package npm

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/npm/document"
	"github.com/guided-traffic/trove/internal/formats/npm/name"
	"github.com/guided-traffic/trove/internal/formats/npm/remote"
)

// The documents of remote and virtual repositories (R-NPM-024, R-NPM-025, R-NPM-027,
// R-NPM-030, flows F3 and F4). Both are served from one full document: the stored
// document of the upstream, or the merge of the members' documents. The abbreviated
// document, the single-version document and the dist-tags are derived from it, and the
// tarball addresses are rewritten onto the repository that serves the response, after
// the merged-metadata cache, from the request's base URL (R-REPO-043).

// form is the form of a document a request asks for.
type form string

const (
	formFull        form = "full"
	formAbbreviated form = "abbreviated"
	formVersion     form = "version"
	formTags        form = "tags"
)

// fullSource is the full document a response is derived from, with what identifies its
// content (the validator and the cache key of a rendition are functions of it).
type fullSource struct {
	form form
	// ref is the version number or the dist-tag of a single-version request.
	ref string
	// kind is "remote", "cache" (the cache view) or "virtual"; it is part of the key, so
	// the same package served by different roads never shares a validator.
	kind string
	// servingKey is the key the links name: the repository's, or <key>~cache.
	servingKey string
	repoID     int64
	// content is hex of what the document is a function of: the digest of the stored
	// document, or the fingerprint of the merge.
	content string
	bytes   func(ctx context.Context) ([]byte, error)
	// status is the Cache-Status of the response, "" for none.
	status string
	// rev is the synthetic _rev of a merged document.
	rev string
}

// serveFull answers a request for a form of the document of a package from its full
// document: validator first (a match is a 304 without a read of the document), then the
// replica's rendition cache, then a rendering. The inputs of the validator and of the
// key are the same and hold nothing that is per replica (R-NPM-013).
func (f *Format) serveFull(ctx context.Context, rq *formats.Request, w http.ResponseWriter, n name.Name, src fullSource) error {
	rep := f.replicaOf(rq.Host)
	if src.form == formTags {
		return f.serveTags(ctx, w, rq, n, src)
	}
	links := rq.Host.Links(rq)
	variant := src.kind + "/" + string(src.form)
	contentType := "application/json"
	if src.form == formAbbreviated {
		contentType = document.Abbreviated.ContentType()
	}
	if src.form == formVersion {
		variant += "/" + src.ref
	}
	settings := fmt.Sprintf("%d/%t/%s", rq.Repo.Version, rep.cfg.AbbreviatedTime, src.content)
	inputs := document.Inputs{
		Repo: src.repoID, Variant: variant, BaseURL: links.Base(), Endpoint: rq.Endpoint.Name,
		Exposure: rq.Endpoint.ExposureFingerprint, Settings: settings,
	}
	etag := inputs.ETag()
	maxAge := int64(rep.cfg.DocumentMaxAge / time.Second)
	h := w.Header()
	head := func() {
		h.Set("ETag", etag)
		h.Set("Vary", "Accept, Accept-Encoding")
		h.Set("Cache-Control", "private, max-age="+strconv.FormatInt(maxAge, 10)+", must-revalidate")
		if src.status != "" {
			h.Set("Cache-Status", src.status)
		}
	}
	if document.IfNoneMatch(rq.Header.Get("If-None-Match"), etag) {
		head()
		w.WriteHeader(http.StatusNotModified)
		return nil
	}

	gzip := document.AcceptsGzip(rq.Header.Get("Accept-Encoding"))
	key := formats.RenditionKey{
		Repo: rq.RepoRef(), Package: n.String(), Variant: variant, Encoding: encodingOf(gzip), BaseURL: links.Base(),
		Endpoint: rq.Endpoint.Name, Exposure: rq.Endpoint.ExposureFingerprint, Settings: settings, RenderVersion: document.RenderVersion,
	}
	entry, hit := rq.Host.Renditions().Get(key)
	if !hit && gzip {
		if plain, ok := rq.Host.Renditions().Get(withEncoding(key, false)); ok && len(plain) >= modifiedBytes {
			entry, hit = append(append([]byte(nil), plain[:modifiedBytes]...), document.Gzip(plain[modifiedBytes:])...), true
			rq.Host.Renditions().Put(key, entry)
		}
	}
	if !hit {
		full, err := src.bytes(ctx)
		if err != nil {
			return err
		}
		if int64(len(full)) > MaxDocumentBytes {
			return errs.E(errs.UpstreamFailed, errs.CodeUpstreamItemTooLarge, "The package document is larger than the limit of documents held in memory.")
		}
		prefix := remote.TarballPrefix(links.Registry(src.servingKey), n.String())
		p, err := f.exposedDocument(ctx, rq, n, full, prefix, src.rev)
		if err != nil {
			return err
		}
		var body []byte
		switch src.form {
		case formFull:
			body, err = p.Full(n.String(), prefix, src.rev)
		case formAbbreviated:
			body, err = p.Abbreviated(n.String(), prefix, rep.cfg.AbbreviatedTime)
		default:
			var ok bool
			if body, ok, err = p.Version(n.String(), prefix, src.ref); err == nil && !ok {
				return notFound()
			}
		}
		if err != nil {
			return err
		}
		plain := stampOrNone(p.Modified(), body)
		rq.Host.Renditions().Put(withEncoding(key, false), plain)
		entry = plain
		if gzip {
			entry = stampOrNone(p.Modified(), document.Gzip(body))
			rq.Host.Renditions().Put(key, entry)
		}
	}
	if len(entry) < modifiedBytes {
		return errs.E(errs.Internal, errs.CodeInternal, "A cached rendition is shorter than its stamp.")
	}
	head()
	if nano := int64(binary.BigEndian.Uint64(entry)); nano != 0 { //nolint:gosec // written by stampOrNone
		h.Set("Last-Modified", time.Unix(0, nano).UTC().Format(http.TimeFormat))
	}
	body := entry[modifiedBytes:]
	h.Set("Content-Type", contentType)
	if gzip {
		h.Set("Content-Encoding", "gzip")
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if rq.Method == http.MethodHead {
		return nil
	}
	_, err := w.Write(body)
	return err
}

// stampOrNone is stamp for a document that may have no time.modified: the stamp is then 0
// and no Last-Modified is sent.
func stampOrNone(modified time.Time, body []byte) []byte {
	if modified.IsZero() {
		out := make([]byte, modifiedBytes+len(body))
		copy(out[modifiedBytes:], body)
		return out
	}
	return stamp(modified, body)
}

// serveTags answers the dist-tags of a package from its full document. Like the tags of
// a local repository they are no resource a validator is made for (R-NPM-016).
func (f *Format) serveTags(ctx context.Context, w http.ResponseWriter, rq *formats.Request, n name.Name, src fullSource) error {
	full, err := src.bytes(ctx)
	if err != nil {
		return err
	}
	if view := viewOf(rq); view != nil {
		// The tags are read from the filtered document, because only that one knows where
		// latest moved (R-NPM-048).
		p, err := f.exposedDocument(ctx, rq, n, full, "", "")
		if err != nil {
			return err
		}
		if full, err = p.Full(n.String(), "", ""); err != nil {
			return err
		}
	}
	tags, err := remote.TagsOf(full)
	if err != nil {
		return err
	}
	h := w.Header()
	h.Set("Cache-Control", "private, no-cache")
	if src.status != "" {
		h.Set("Cache-Status", src.status)
	}
	return writeJSON(w, http.StatusOK, tags)
}

// remoteSource reads the stored document of a remote repository through the engine
// (R-REPO-025): fresh, refreshed or stale. A negative entry is the 404 of an unknown
// package.
func (f *Format) remoteSource(ctx context.Context, rq *formats.Request, n name.Name, fm form, ref string) (fullSource, error) {
	doc, err := rq.Host.Remote().Metadata(ctx, rq, formats.Path{Path: n.String()}, documentVariant)
	if err != nil {
		return fullSource{}, err
	}
	if doc.Status.Negative {
		return fullSource{}, errs.E(errs.NotFound, errs.CodeNotFound, "The resource does not exist.", errs.WithHeader("Cache-Status", doc.Status.Header))
	}
	src := fullSource{
		form: fm, ref: ref, kind: "remote", servingKey: rq.Repo.Key, repoID: rq.RepoRef().ID,
		content: hex.EncodeToString(doc.Digest[:]), bytes: doc.Bytes, status: doc.Status.Header,
	}
	if rq.CacheView {
		src.kind, src.servingKey = "cache", rq.Repo.Key+"~cache"
	}
	return src, nil
}

// remoteDocument serves a form of the package document of a remote repository.
func (f *Format) remoteDocument(ctx context.Context, rq *formats.Request, w http.ResponseWriter, n name.Name, fm form, ref string) error {
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Package: n.String(), Actions: []formats.Action{formats.ActionRead}}); err != nil {
		return err
	}
	src, err := f.remoteSource(ctx, rq, n, fm, ref)
	if err != nil {
		return err
	}
	return f.serveFull(ctx, rq, w, n, src)
}

// remoteTarball serves a tarball of a remote repository: the cached item, or one fetch
// under the collapsing protocol, verified against the digests the document announced,
// after the format's gate applied the marks and revalidated a stale document
// (R-NPM-025, R-NPM-026).
func (f *Format) remoteTarball(ctx context.Context, rq *formats.Request, w http.ResponseWriter, n name.Name, version string) error {
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Package: n.String(), Version: version, Actions: []formats.Action{formats.ActionRead}}); err != nil {
		return err
	}
	if err := hiddenVersion(rq, n, version); err != nil {
		return err
	}
	return rq.Host.Remote().Artifact(ctx, rq, formats.Path{Path: TarballPath(n, version)}, formats.Digests{}, immutableWriter{w})
}

// nonLocalDocument serves the package document of a remote or virtual repository in the
// form the Accept header negotiates; ?write=true on a virtual repository reads the
// deployment target (R-NPM-029), and on a remote repository changes nothing: there is
// nothing to write to.
func (f *Format) nonLocalDocument(ctx context.Context, rq *formats.Request, w http.ResponseWriter, n name.Name) error {
	if rq.Repo.Class == formats.Virtual && rq.URL.Query().Get("write") == "true" {
		if rq.Endpoint.Rules {
			return readOnly(ctx, rq, n.String())
		}
		return f.virtualWriteDocument(ctx, rq, w, n)
	}
	fm := formFull
	if document.Negotiate(rq.Header.Get("Accept")) == document.Abbreviated {
		fm = formAbbreviated
	}
	return f.nonLocalDocumentOf(ctx, rq, w, n, fm, "")
}

// nonLocalDocumentOf serves one form of the package document by the class.
func (f *Format) nonLocalDocumentOf(ctx context.Context, rq *formats.Request, w http.ResponseWriter, n name.Name, fm form, ref string) error {
	if rq.Repo.Class == formats.Virtual {
		return f.virtualDocument(ctx, rq, w, n, fm, ref)
	}
	return f.remoteDocument(ctx, rq, w, n, fm, ref)
}

// exposedDocument parses a full document for a response. On an endpoint with exposure
// rules it is rewritten for the serving repository, filtered to what the endpoint
// exposes of it (versions, time, tags, latest among the exposed releases, R-NPM-048,
// H-295) and parsed again; nothing exposed is the 404 of a missing package.
func (f *Format) exposedDocument(ctx context.Context, rq *formats.Request, n name.Name, full []byte, prefix, rev string) (*remote.Parsed, error) {
	p, err := remote.Parse(full)
	if err != nil {
		return nil, err
	}
	view := viewOf(rq)
	if view == nil {
		return p, nil
	}
	rewritten, err := p.Full(n.String(), prefix, rev)
	if err != nil {
		return nil, err
	}
	filtered, err := f.exposure.FilterDocument(ctx, formats.Path{Path: n.String()}, rewritten, view)
	if err != nil {
		return nil, err
	}
	return remote.Parse(filtered)
}

// hiddenVersion is the 404 of a version the endpoint does not expose, decided before
// any lookup or fetch (R-NPM-048, R-EP-013): a tarball is served exactly when a
// filtered document lists its version.
func hiddenVersion(rq *formats.Request, n name.Name, version string) error {
	if view := viewOf(rq); view != nil && !view.Version(n.String(), version) {
		return notFound()
	}
	return nil
}
