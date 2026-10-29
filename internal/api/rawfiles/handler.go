// Package rawfiles serves /files/{repoKey}/{path} (R-API-025, R-STOR-007,
// R-STOR-012, R-STOR-013, R-STOR-017, R-STOR-018): the raw transfer of a repository
// without a format protocol. The handler is thin: it parses the request, calls
// internal/files, which decides and does, and turns the result into the headers
// and the status of the specification. The management API's items operations are
// in internal/api/v1.
//
// Errors are problem details; a response whose body is stored content, or a page
// rendered from it, is written through httpserver.ContentWriter, which sets the
// content headers of R-ARCH-007 and checks them again at the status line.
package rawfiles

import (
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/files"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/repository"
)

// Prefix is the path every route of the handler lies below.
const Prefix = "/files/"

// Options configure [New].
type Options struct {
	Service *files.Service
	// Download is storage.download: the copy buffer of a body.
	Download config.StorageDownload
	// API supplies the paging of a directory index; its page sizes are replaced by
	// the 1,000 and 10,000 of R-STOR-017.
	API config.API
}

// Handler serves /files/.
type Handler struct {
	svc *files.Service
	buf int
	api config.API
}

// New creates the handler.
func New(o Options) *Handler {
	api := o.API
	api.PageSizeDefault, api.PageSizeMax = 1000, 10_000
	return &Handler{svc: o.Service, buf: max(int(o.Download.BufferSize), 4096), api: api}
}

// Register mounts the routes of the files family on the router: the four methods
// of R-API-025, and the whole subtree for the paths ServeMux would redirect, which
// are answered 400 invalid_path (R-GENERIC-002).
func (h *Handler) Register(r *httpserver.Router) {
	for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete} {
		r.Handle(m+" "+Prefix+"{repoKey}/{path...}", h)
	}
	r.HandleUncleaned(Prefix, h)
}

// ServeHTTP parses the request path once, from the escaped form (R-ARCH-005: the
// path is decoded exactly once, after it was split), and dispatches on the method.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key, rest, ok := splitPath(r.URL.EscapedPath())
	if !ok {
		httpserver.Problem(w, r, errs.E(errs.NotFound, errs.CodeNotFound, "No route matches the request path."))
		return
	}
	if strings.HasPrefix(rest, "/") {
		// "/files/<key>//x": CleanPath ignores one leading slash, which here is an
		// empty first segment (R-GENERIC-002).
		httpserver.Problem(w, r, errs.E(errs.Invalid, errs.CodeInvalidPath, "the path has an empty segment"))
		return
	}
	p, err := repository.CleanPath(rest)
	if err != nil { // before authorization (R-GENERIC-002)
		httpserver.Problem(w, r, err)
		return
	}
	if h.svc == nil { // a replica without PostgreSQL or the object store
		httpserver.Problem(w, r, errs.E(errs.Unavailable, errs.CodeUnavailable, "The files service is not available on this replica.", errs.WithRetryAfter(time.Second)))
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		h.get(w, r, key, p)
	case http.MethodPut:
		h.put(w, r, key, p)
	case http.MethodDelete:
		h.delete(w, r, key, p)
	default:
		httpserver.Problem(w, r, errs.E(errs.MethodNotAllowed, errs.CodeMethodNotAllowed, "The resource does not support this method.",
			errs.WithAllow(http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete)))
	}
}

// splitPath cuts "/files/<key>/<rest>" at the first slash after the key and
// decodes the key. rest stays escaped.
func splitPath(escaped string) (key, rest string, ok bool) {
	tail, found := strings.CutPrefix(escaped, Prefix)
	if !found {
		return "", "", false
	}
	rawKey, rest, _ := strings.Cut(tail, "/")
	key, err := url.PathUnescape(rawKey)
	if err != nil || key == "" {
		return "", "", false
	}
	return key, rest, true
}

func (h *Handler) links(r *http.Request) files.Links {
	return files.Links{Base: httpserver.InfoFrom(r.Context()).BaseURL}
}

func fileName(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// cacheControl is `private, no-cache` for an authenticated request and `no-cache`
// for an anonymous one; clients revalidate with the ETag (R-STOR-012).
func cacheControl(r *http.Request) string {
	if httpserver.PrincipalFrom(r.Context()).Anonymous {
		return "no-cache"
	}
	return "private, no-cache"
}

// get serves GET and HEAD: a file with the semantics of R-STOR-012, a folder as
// the directory index of R-STOR-017.
func (h *Handler) get(w http.ResponseWriter, r *http.Request, key string, p repository.Cleaned) {
	caller := v1.CallerFrom(r)
	res, err := h.svc.Stat(r.Context(), caller, key, p)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	if res.IsFolder {
		if !p.Folder { // /files/repo/folder is the folder: the relative links need the slash
			// The target is the request's own path below /files/ with a slash.
			http.Redirect(w, r, r.URL.EscapedPath()+"/"+queryOf(r), http.StatusMovedPermanently) //nolint:gosec // see above
			return
		}
		h.index(w, r, caller, key, p)
		return
	}
	h.serveFile(w, r, caller, res)
}

// serveFile answers a GET or HEAD of a file (R-STOR-012): the conditional headers
// and the range from the item row alone, the body from the object store through
// the verifying reader (R-STOR-013). The headers of the file are collected first;
// they go to the response of a status that carries the file (200, 206, 304) and
// never to a problem.
func (h *Handler) serveFile(w http.ResponseWriter, r *http.Request, caller files.Caller, res files.Resolved) {
	it := res.Item
	etag, modified := it.ETag(), it.ModifiedAt.Truncate(time.Second)
	hd := http.Header{}
	hd.Set("ETag", etag)
	hd.Set("Last-Modified", modified.UTC().Format(http.TimeFormat))
	hd.Set("Accept-Ranges", "bytes")
	hd.Set("Cache-Control", cacheControl(r))
	hd.Set("Repr-Digest", reprDigest(r.Header, it.Digest[:], it.SHA512))
	hd.Set("Content-Type", it.MediaTypeOrDefault())
	content := func() http.ResponseWriter {
		cw := httpserver.ContentWriterFor(w, r, httpserver.ContentFile, fileName(it.Path))
		for k, v := range hd {
			cw.Header()[k] = v
		}
		return cw
	}

	switch evaluate(r, etag, modified) {
	case notModified:
		content().WriteHeader(http.StatusNotModified)
		return
	case preconditionFailed:
		httpserver.Problem(w, r, errs.E(errs.PreconditionFailed, errs.CodePreconditionFailed, "A precondition of the request does not hold."))
		return
	}
	start, length, rr := byteRange(r, etag, modified, it.Size)
	status := http.StatusOK
	switch rr {
	case unsatisfiable:
		w.Header().Set("Content-Range", "bytes */"+strconv.FormatInt(it.Size, 10))
		httpserver.Problem(w, r, errs.E(errs.RangeNotSatisfiable, errs.CodeRangeNotSatisfiable, "The requested range is not satisfiable."))
		return
	case partialContent:
		status = http.StatusPartialContent
		hd.Set("Content-Range", "bytes "+strconv.FormatInt(start, 10)+"-"+strconv.FormatInt(start+length-1, 10)+"/"+strconv.FormatInt(it.Size, 10))
	default:
		start, length = 0, it.Size
	}
	hd.Set("Content-Length", strconv.FormatInt(length, 10))
	if r.Method == http.MethodHead {
		content().WriteHeader(status)
		return
	}
	var rng *files.Range
	if rr == partialContent {
		rng = &files.Range{Offset: start, Length: length}
	}
	body, err := h.svc.Open(r.Context(), it, rng)
	if err != nil { // before the status line: a problem the client can read
		httpserver.Problem(w, r, err)
		return
	}
	defer body.Close()
	cw := content()
	cw.WriteHeader(status)
	if _, err := io.CopyBuffer(cw, readerOnly{body}, make([]byte, h.buf)); err != nil {
		if r.Context().Err() != nil { // the client went away
			return
		}
		// The held-back bytes were not released, or the object store failed in the
		// middle of the body: the client sees a truncated transfer, never a complete
		// corrupted file (R-STOR-013).
		httpserver.Logger(r.Context()).Error("a download was aborted", "path", it.Path, "error", err)
		panic(http.ErrAbortHandler)
	}
	if start == 0 { // 200, or 206 from byte 0 (R-STOR-015)
		h.svc.Served(r.Context(), caller, res, r.URL.Query().Get("stats") != "false")
	}
}

// readerOnly hides WriterTo and ReaderFrom so that CopyBuffer uses the buffer.
type readerOnly struct{ io.Reader }

func queryOf(r *http.Request) string {
	if r.URL.RawQuery == "" {
		return ""
	}
	return "?" + r.URL.RawQuery
}

// put deploys a file or creates a folder (R-STOR-007).
func (h *Handler) put(w http.ResponseWriter, r *http.Request, key string, p repository.Cleaned) {
	expect, err := parseReprDigest(r.Header)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	mt, err := mediaType(r)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	ctx := r.Context()
	res, err := h.svc.Deploy(ctx, v1.CallerFrom(r), files.DeployRequest{
		Repo: key, Path: p, Body: r.Body, ContentLength: r.ContentLength, MediaType: mt, Expect: expect,
		CreateOnly:  strings.TrimSpace(r.Header.Get("If-None-Match")) == "*",
		BeginCommit: func() { httpserver.BeginCommit(ctx) },
	})
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	l := h.links(r)
	status := http.StatusOK
	if res.Created {
		status = http.StatusCreated
	}
	w.Header().Set("Location", l.File(key, res.Item.Path, res.Item.Kind == files.KindFolder))
	if res.Item.Kind == files.KindFile {
		w.Header().Set("ETag", res.Item.ETag())
	}
	v1.WriteJSON(w, r, status, res.Item.View(l))
}

// mediaType reads the Content-Type to store with the item: a well-formed media
// type, kept as sent, at most 255 bytes. An absent header stores none.
func mediaType(r *http.Request) (string, error) {
	v := strings.TrimSpace(r.Header.Get("Content-Type"))
	if v == "" {
		return "", nil
	}
	bad := errs.E(errs.Invalid, errs.CodeInvalidHeader, "Content-Type is not a media type.")
	if len(v) > 255 || strings.ContainsAny(v, "\r\n\x00") {
		return "", bad
	}
	// The value is served back as the file's Content-Type, so only a media type
	// that parses is stored, and in its normalised form (lower-case type and
	// parameter names, quoted where RFC 2045 needs it).
	mt, params, err := mime.ParseMediaType(v)
	if err != nil {
		return "", bad
	}
	out := mime.FormatMediaType(mt, params)
	if out == "" || len(out) > 255 {
		return "", bad
	}
	return out, nil
}

// delete removes a file, or a folder with its subtree (R-STOR-018).
func (h *Handler) delete(w http.ResponseWriter, r *http.Request, key string, p repository.Cleaned) {
	res, err := h.svc.Delete(r.Context(), v1.CallerFrom(r), files.DeleteRequest{Repo: key, Path: p.Path, Strict: true})
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	v1.WriteDeleteResult(w, r, res)
}
