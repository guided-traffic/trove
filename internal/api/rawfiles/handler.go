// Package rawfiles serves /files/{repoKey}/{path} (R-API-025, R-STOR-007,
// R-STOR-012, R-STOR-013, R-STOR-017, R-STOR-018): the raw transfer of a repository
// without a format protocol. The handler is thin: it parses the request, calls
// internal/files, which decides and does, and turns the result into the headers
// and the status of the specification. The management API's items operations are
// in internal/api/v1.
//
// Errors are problem details; a response whose body is stored content, or a page
// rendered from it, is written through httpserver.ContentWriter, which sets the
// content headers of R-ARCH-007 and checks them again at the status line. The
// content response itself (validators, conditional requests, ranges, digests) is
// internal/api/edge's, which the format routes share. The package imports no
// entry point of the management API.
package rawfiles

import (
	"context"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/api/edge"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/bastionmain"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/files"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/repository"
)

// Prefix is the path every route of the handler lies below.
const Prefix = "/files/"

// Options configure [New].
type Options struct {
	Service *files.Service
	// Decider is the decision point a request begins its index on (R-AUTHZ-015).
	Decider authz.Decider
	// Download is storage.download: the copy buffer of a body.
	Download config.StorageDownload
	// API supplies the paging of a directory index; its page sizes are replaced by
	// the 1,000 and 10,000 of R-STOR-017.
	API config.API
}

// Handler serves /files/.
type Handler struct {
	svc     *files.Service
	decider authz.Decider
	buf     int
	api     config.API
}

// New creates the handler.
func New(o Options) *Handler {
	api := o.API
	api.PageSizeDefault, api.PageSizeMax = 1000, 10_000
	return &Handler{svc: o.Service, decider: o.Decider, buf: max(int(o.Download.BufferSize), 4096), api: api}
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
	h.serve(w, r, key, rest, 0)
}

// serve answers a request of repository key whose escaped path below the repository is rest: the transfer of /files/
// and, with maxUpload set by the content routes (content.go), the same transfer behind the limit of the UI's uploads.
// maxUpload is the largest PUT the route takes; 0 is no limit of its own.
func (h *Handler) serve(w http.ResponseWriter, r *http.Request, key, rest string, maxUpload int64) {
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
	// The marking stage of a Bastion request learns the route here, before the caller is decided or a body
	// is read, and may refuse a write or answer a probe (R-BAS-013, R-BAS-014, Q-BAS-52).
	obs := bastionmain.ObserverFrom(r.Context())
	if obs != nil {
		if match, ok := routeOf(r, key, p); ok {
			handled, err := obs.Matched(w, r, match)
			if err != nil {
				httpserver.Problem(w, r, err)
				return
			}
			if handled {
				return
			}
		}
	}
	if h.svc == nil { // a replica without PostgreSQL or the object store
		httpserver.Problem(w, r, errs.E(errs.Unavailable, errs.CodeUnavailable, "The files service is not available on this replica.", errs.WithRetryAfter(time.Second)))
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete:
		caller, err := h.caller(r, key)
		if err != nil {
			httpserver.Problem(w, r, err)
			return
		}
		if obs != nil {
			if match, ok := routeOf(r, key, p); ok {
				caller = decided(obs, r, caller, match)
			}
		}
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			h.get(w, r, caller, key, p)
		case http.MethodPut:
			h.put(w, r, caller, key, p, maxUpload)
		default:
			h.delete(w, r, caller, key, p)
		}
	default:
		httpserver.Problem(w, r, errs.E(errs.MethodNotAllowed, errs.CodeMethodNotAllowed, "The resource does not support this method.",
			errs.WithAllow(http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete)))
	}
}

// routeOf is the route a request on /files/ is, as the marking stage sees it (R-BAS-013): a GET or HEAD of a
// file is a download, of a folder (a path with a trailing slash, or the root) a listing that is like a
// document, a PUT an upload and a DELETE a delete. It reports false for a method the handler refuses.
func routeOf(r *http.Request, key string, p repository.Cleaned) (bastionmain.RouteMatch, bool) {
	m := bastionmain.RouteMatch{Format: bastionmain.FormatFiles, RepoKey: key, Method: r.Method}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		m.Name, m.Kind, m.Action = "files.get", bastionmain.KindDownload, authz.Read
		if p.Folder || p.Path == "" {
			m.Name, m.Kind = "files.list", bastionmain.KindMetadata
		}
	case http.MethodPut:
		m.Name, m.Kind, m.Action = "files.put", bastionmain.KindUpload, authz.Deploy
	case http.MethodDelete:
		m.Name, m.Kind, m.Action = "files.delete", bastionmain.KindDelete, authz.Delete
	default:
		return m, false
	}
	return m, true
}

// decided returns the caller whose first allowed decision tells the observer the subject and the index the
// request decides on. The decision itself is the service's, made while it resolves the path, so the observer
// learns it from the service's question: after the read was allowed and before the item is looked up, and never
// for a refused read, whose marking carries no Trove-Bastion-Read (R-BAS-013, Q-BAS-71).
func decided(obs bastionmain.RouteObserver, r *http.Request, caller files.Caller, m bastionmain.RouteMatch) files.Caller {
	var idx *authz.Index
	if acc, ok := caller.Access.(*edge.Access); ok {
		idx = acc.Index()
	}
	d := bastionmain.RouteDecision{Match: m, Subject: caller.Subject, Index: idx}
	caller.Access = &observedAccess{Access: caller.Access, allowed: func() { obs.Decided(r, d) }}
	return caller
}

// observedAccess calls allowed once, at the first decision that allows its request.
type observedAccess struct {
	files.Access
	once    sync.Once
	allowed func()
}

// Decide implements files.Access.
func (a *observedAccess) Decide(ctx context.Context, c authz.Coordinate) authz.Decision {
	d := a.Access.Decide(ctx, c)
	if d.Allowed() {
		a.once.Do(a.allowed)
	}
	return d
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

// caller takes the index the request decides on, with the key made known, and
// builds the caller of the generic layer (R-AUTHZ-015): one holder for every
// decision of the request. The subject carries the endpoint whose listener
// accepted it (R-EP-013). An index that cannot be confirmed is 503 authz_stale,
// an unreached Trove-Authz-Min-Epoch 412.
func (h *Handler) caller(r *http.Request, key string) (files.Caller, error) {
	ctx := r.Context()
	if _, ok := edge.AccessFrom(ctx); !ok && h.decider == nil { // a replica without the authz area
		return files.Caller{}, errs.E(errs.Unavailable, errs.CodeUnavailable, "The files service is not available on this replica.", errs.WithRetryAfter(time.Second))
	}
	acc, err := edge.Resolve(ctx, h.decider, edge.SubjectFrom(r), key)
	if err != nil {
		return files.Caller{}, err
	}
	return files.Caller{
		Access: acc, Subject: acc.Subject,
		Actor: edge.ActorFrom(httpserver.PrincipalFrom(ctx)), Source: httpserver.SourceFrom(ctx),
		// A write that waited for its body decides again on a new index (R-AUTHZ-015).
		Recheck: func(ctx context.Context, key string) (files.Access, error) {
			fresh, err := edge.Begin(ctx, acc.D, acc.Subject, key)
			if err != nil {
				return nil, err
			}
			return fresh, nil
		},
	}, nil
}

func (h *Handler) links(r *http.Request) edge.Links {
	return edge.Links{Base: httpserver.InfoFrom(r.Context()).BaseURL}
}

// get serves GET and HEAD: a file with the semantics of R-STOR-012, a folder as
// the directory index of R-STOR-017.
func (h *Handler) get(w http.ResponseWriter, r *http.Request, caller files.Caller, key string, p repository.Cleaned) {
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
	if err := edge.ServeItem(w, r, res.Item,
		func(rng *items.Range) (io.ReadCloser, error) { return h.svc.Open(r.Context(), res.Item, rng) },
		edge.ServeOptions{
			Buffer: h.buf,
			Served: func() { h.svc.Served(r.Context(), caller, res, r.URL.Query().Get("stats") != "false") },
		}); err != nil {
		// The object cannot be opened: 503 content_unavailable when it is missing, 500 when it failed verification,
		// 503 object_store_unavailable during an outage (R-STOR-012, R-STOR-036). /files/ never contacts an upstream, so
		// an item of a remote repository answers the same here; its own route fills it again (R-STOR-027).
		httpserver.Problem(w, r, err)
	}
}

func queryOf(r *http.Request) string {
	if r.URL.RawQuery == "" {
		return ""
	}
	return "?" + r.URL.RawQuery
}

// put deploys a file or creates a folder (R-STOR-007).
func (h *Handler) put(w http.ResponseWriter, r *http.Request, caller files.Caller, key string, p repository.Cleaned, maxUpload int64) {
	expect, err := edge.ParseReprDigest(r.Header)
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
	var body io.Reader = r.Body
	if maxUpload > 0 {
		body = &limitBody{r: r.Body, limit: maxUpload, declared: r.ContentLength}
	}
	res, err := h.svc.Deploy(ctx, caller, files.DeployRequest{
		Repo: key, Path: p, Body: body, ContentLength: r.ContentLength, MediaType: mt, Expect: expect,
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
	w.Header().Set("Location", l.File(key, res.Item.Path, res.Item.Kind == items.KindFolder))
	if res.Item.Kind == items.KindFile {
		w.Header().Set("ETag", res.Item.ETag())
	}
	edge.WriteJSON(w, r, status, edge.DescribeItem(res.Item, l))
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
func (h *Handler) delete(w http.ResponseWriter, r *http.Request, caller files.Caller, key string, p repository.Cleaned) {
	res, err := h.svc.Delete(r.Context(), caller, files.DeleteRequest{Repo: key, Path: p.Path, Strict: true})
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	if res.Done {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	edge.WriteJob(w, r, res.JobID, items.DeleteJobKind, res.JobState)
}
