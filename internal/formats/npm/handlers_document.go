package npm

import (
	"context"
	"encoding/binary"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/npm/document"
	"github.com/guided-traffic/trove/internal/formats/npm/name"
	"github.com/guided-traffic/trove/internal/formats/npm/rows"
)

// The package document and the single-version document (R-NPM-009 to R-NPM-014,
// flow F2). A document is rendered at request time from committed rows (R-ARCH-012):
// every request reads packages.rev_seq with one indexed lookup, derives the
// validator and the key of the replica's rendition cache from it without rendering,
// and renders only on a miss. Nothing is cached under a key a commit can outlive, so
// no message between replicas is involved (Q-NPM-10, Q-NPM-26).

// modifiedBytes is the length of the time.modified stamp a cached rendition starts
// with: the cache holds bytes only, and Last-Modified must not cost a query.
const modifiedBytes = 8

// rendering is what a document request needs to read, key and render one form of a
// package document.
type rendering struct {
	// variant is the part of the key and the validator that tells the forms apart:
	// "full", "abbreviated" or "version/<ref>".
	variant string
	// contentType is the media type of the form.
	contentType string
	// read reads the rows; found is false when the document does not exist.
	read   func(ctx context.Context, st Store) (rows.LocalPackage, bool, error)
	render func(p *rows.LocalPackage, o document.Options) ([]byte, error)
}

func (f *Format) handleDocument(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	n, err := name.Parse(rq.Params.Get("name"))
	if err != nil {
		return err
	}
	repo := rq.RepoRef()
	if rq.URL.Query().Get("write") == "true" {
		return f.serveForWrite(ctx, rq, w, n)
	}
	v := document.Negotiate(rq.Header.Get("Accept"))
	rd := rendering{
		variant: string(v), contentType: v.ContentType(),
		read: func(ctx context.Context, st Store) (rows.LocalPackage, bool, error) {
			return st.LocalDocument(ctx, repo.ID, n.String())
		},
		render: document.FullDocument,
	}
	if v == document.Abbreviated {
		rd.render = document.AbbreviatedDocument
	}
	return f.serveRendering(ctx, rq, w, n, rd)
}

func (f *Format) handleVersion(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	n, err := name.Parse(rq.Params.Get("name"))
	if err != nil {
		return err
	}
	repo := rq.RepoRef()
	ref := rq.Params.Get("ref")
	// A number or a dist-tag (R-NPM-014): a tag is never a valid version, so the two
	// cannot be mistaken for each other.
	version, tag := "", ref
	if v, ok := canonicalVersion(ref); ok {
		version, tag = v, ""
	}
	return f.serveRendering(ctx, rq, w, n, rendering{
		variant: "version/" + ref, contentType: "application/json",
		read: func(ctx context.Context, st Store) (rows.LocalPackage, bool, error) {
			return st.LocalVersion(ctx, repo.ID, n.String(), version, tag)
		},
		render: document.VersionDocument,
	})
}

// authorizeRead decides the read of a package and finds its row. A package that does
// not exist and one the caller may not read answer alike (R-AUTHZ-016).
func (f *Format) authorizeRead(ctx context.Context, rq *formats.Request, n name.Name, version string) (Store, formats.PackageRow, error) {
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Package: n.String(), Version: version, Actions: []formats.Action{formats.ActionRead}}); err != nil {
		return nil, formats.PackageRow{}, err
	}
	st, err := storeOf(rq.Host.FormatData(ID))
	if err != nil {
		return nil, formats.PackageRow{}, err
	}
	row, found, err := rq.Host.Packages().Package(ctx, rq.RepoRef(), n.String())
	if err != nil {
		return nil, formats.PackageRow{}, err
	}
	if !found {
		return nil, formats.PackageRow{}, notFound()
	}
	return st, row, nil
}

// serveRendering answers a document request from the validator, the rendition cache
// or a rendering, in that order (F2).
func (f *Format) serveRendering(ctx context.Context, rq *formats.Request, w http.ResponseWriter, n name.Name, rd rendering) error {
	rep := f.replicaOf(rq.Host)
	st, row, err := f.authorizeRead(ctx, rq, n, "")
	if err != nil {
		return err
	}
	repo := rq.RepoRef()
	links := rq.Host.Links(rq)
	settings := fmt.Sprintf("%d/%t", rq.Repo.Version, rep.cfg.AbbreviatedTime)
	inputs := func(rev int64) document.Inputs {
		return document.Inputs{
			Repo: repo.ID, Package: int64(row.Ref.ID), Rev: rev, Variant: rd.variant, BaseURL: links.Base(),
			Endpoint: rq.Endpoint.Name, Exposure: rq.Endpoint.ExposureFingerprint, Settings: settings,
		}
	}
	maxAge := int64(rep.cfg.DocumentMaxAge / time.Second)
	cacheControl := "private, max-age=" + strconv.FormatInt(maxAge, 10) + ", must-revalidate"
	head := func(etag string) {
		h := w.Header()
		h.Set("ETag", etag)
		h.Set("Vary", "Accept, Accept-Encoding")
		h.Set("Cache-Control", cacheControl)
	}

	etag := inputs(int64(row.Ref.RevSeq)).ETag()
	if document.IfNoneMatch(rq.Header.Get("If-None-Match"), etag) {
		// A validator of this revision: the rows are the ones the client holds. No
		// rendering and no read of the rows; If-Modified-Since is not consulted
		// (R-NPM-013).
		head(etag)
		w.WriteHeader(http.StatusNotModified)
		return nil
	}

	gzip := document.AcceptsGzip(rq.Header.Get("Accept-Encoding"))
	key := formats.RenditionKey{
		Repo: repo, Package: n.String(), RevSeq: row.Ref.RevSeq, Variant: rd.variant, Encoding: encodingOf(gzip),
		BaseURL: links.Base(), Endpoint: rq.Endpoint.Name, Exposure: rq.Endpoint.ExposureFingerprint,
		Settings: settings, RenderVersion: document.RenderVersion,
	}
	entry, hit := rq.Host.Renditions().Get(key)
	if !hit && gzip {
		// The identity rendering of the revision is cached already: compress it.
		if plain, ok := rq.Host.Renditions().Get(withEncoding(key, false)); ok && len(plain) >= modifiedBytes {
			entry, hit = append(append([]byte(nil), plain[:modifiedBytes]...), document.Gzip(plain[modifiedBytes:])...), true
			rq.Host.Renditions().Put(key, entry)
		}
	}
	if !hit {
		p, found, err := rd.read(ctx, st)
		if err != nil {
			return err
		}
		if !found {
			return notFound()
		}
		// The rows carry the revision they were read at, which may be newer than the
		// lookup above when a write raced it: the rendering is keyed and validated by
		// the revision of its own rows.
		key.RevSeq, etag = formats.RevSeq(p.RevSeq), inputs(p.RevSeq).ETag()
		body, err := rd.render(&p, f.optionsOf(rq, rep, n, links, &p))
		if err != nil {
			return errs.E(errs.Internal, errs.CodeInternal, "", errs.WithCause(err))
		}
		if int64(len(body)) > MaxDocumentBytes {
			return errs.E(errs.Internal, errs.CodeInternal, "The package document is larger than the limit of documents held in memory.")
		}
		plain := stamp(p.ModifiedAt, body)
		rq.Host.Renditions().Put(withEncoding(key, false), plain)
		entry = plain
		if gzip {
			entry = stamp(p.ModifiedAt, document.Gzip(body))
			rq.Host.Renditions().Put(key, entry)
		}
	}
	if len(entry) < modifiedBytes {
		return errs.E(errs.Internal, errs.CodeInternal, "A cached rendition is shorter than its stamp.")
	}
	modified := time.Unix(0, int64(binary.BigEndian.Uint64(entry))).UTC() //nolint:gosec // written by stamp above
	body := entry[modifiedBytes:]

	h := w.Header()
	head(etag)
	h.Set("Content-Type", rd.contentType)
	h.Set("Last-Modified", modified.Format(http.TimeFormat))
	if gzip {
		h.Set("Content-Encoding", "gzip")
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if rq.Method == http.MethodHead {
		return nil
	}
	_, err = w.Write(body)
	return err
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

// stamp prefixes a rendering with the time.modified of its rows.
func stamp(modified time.Time, body []byte) []byte {
	out := make([]byte, modifiedBytes+len(body))
	binary.BigEndian.PutUint64(out, uint64(modified.UnixNano())) //nolint:gosec // a time of this century
	copy(out[modifiedBytes:], body)
	return out
}

// optionsOf are the inputs of a rendering that are not rows: the links of the
// response base URL (C-11), the revision as clients see it and the settings.
func (f *Format) optionsOf(rq *formats.Request, rep *replica, n name.Name, links formats.LinkBuilder, p *rows.LocalPackage) document.Options {
	return document.Options{
		Name: n.String(), Basename: n.Package(), TarballPrefix: links.Path(n.String()+"/-") + "/",
		Rev:             Rev(rq.RepoRef().ID, formats.PackageID(p.PackageID), formats.RevSeq(p.RevSeq)),
		AbbreviatedTime: rep.cfg.AbbreviatedTime,
	}
}

// serveForWrite is GET {name}?write=true (R-NPM-009): the full document with its
// _rev, whatever Accept says, read from PostgreSQL inside the request and never
// cached or validated. npm deprecate and npm unpublish read it, change it and send it
// back with the _rev they got.
func (f *Format) serveForWrite(ctx context.Context, rq *formats.Request, w http.ResponseWriter, n name.Name) error {
	rep := f.replicaOf(rq.Host)
	st, _, err := f.authorizeRead(ctx, rq, n, "")
	if err != nil {
		return err
	}
	p, found, err := st.LocalDocument(ctx, rq.RepoRef().ID, n.String())
	if err != nil {
		return err
	}
	if !found {
		return notFound()
	}
	body, err := document.FullDocument(&p, f.optionsOf(rq, rep, n, rq.Host.Links(rq), &p))
	if err != nil {
		return errs.E(errs.Internal, errs.CodeInternal, "", errs.WithCause(err))
	}
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if rq.Method == http.MethodHead {
		return nil
	}
	_, err = w.Write(body)
	return err
}
