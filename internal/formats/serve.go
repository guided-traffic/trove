package formats

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"runtime/debug"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver/content"
)

// ServeMode says what a stored item is served for, and so how far ServeCheck may
// go (C-8).
type ServeMode uint8

const (
	// ServeStored decides on the stored marks only and never contacts an upstream:
	// /files/, a remote's cache view, virtual tiers 1 and 2.
	ServeStored ServeMode = iota + 1
	// ServeMayRevalidate lets the format's own route revalidate a stale document
	// first (Q-NPM-13).
	ServeMayRevalidate
)

// ServeRoute runs rq.Route.Handler with the guarantees the core owes every
// route; the dispatcher and the conformance kit both call it, so the kit tests
// the code that runs in production.
//
//   - A route of kind Download, Metadata or Computed writes through the content
//     wrapper of R-ARCH-007: the headers hold whatever the handler does to them.
//     sameOrigin selects the stricter resource policy when a session cookie
//     authenticated the request (Q-ARCH-86).
//   - A panic is recovered and logged with the format and the route; the request
//     answers 500 through WriteError when no byte has left (R-ARCH-028). A
//     panic of http.ErrAbortHandler passes through, and so does the abort of a
//     connection whose status line is out.
//   - An error the handler returns before the status line is rendered by
//     [Format.WriteError], in the protocol's own shape. One it returns after the
//     status line cannot be reported any more and aborts the connection.
//   - On an endpoint with exposure rules no answer carries Cache-Status, whoever
//     set it (the format, the remote or the virtual engine, an error's headers):
//     it tells whether the addressed repository proxies an upstream and whether
//     the item was fetched lately, which the endpoint's consumers are not to see,
//     as they do not see Trove-Resolved-From (R-EP-018, Q-EP-60).
func ServeRoute(f Format, w http.ResponseWriter, rq *Request, sameOrigin bool) {
	tw := &trackWriter{ResponseWriter: w}
	if rq.Endpoint.Rules {
		tw.hide = []string{"Cache-Status"}
	}
	var out http.ResponseWriter = tw
	if rq.Route.Kind.ServesContent() {
		kind := content.Document
		if rq.Route.Kind == Download {
			kind = content.File
		}
		out = content.Wrap(tw, kind, path.Base(rq.Path.Path), sameOrigin)
	}
	log := rq.Log
	if log == nil {
		log = slog.Default()
	}
	defer func() {
		rec := recover()
		if rec == nil {
			return
		}
		if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
			panic(rec)
		}
		log.Error("panic in a format route", "format", rq.Repo.Format, "route", rq.Route.Name,
			"panic", fmt.Sprint(rec), "stack", string(debug.Stack()))
		if tw.wrote {
			panic(http.ErrAbortHandler)
		}
		renderError(f, tw, rq, errs.E(errs.Internal, errs.CodeInternal, ""))
	}()
	err := rq.Route.Handler(rq.Context(), rq, out)
	if err == nil {
		return
	}
	if tw.wrote {
		log.Error("a format route failed after its status line", "format", rq.Repo.Format, "route", rq.Route.Name, "error", err)
		panic(http.ErrAbortHandler)
	}
	renderError(f, tw, rq, err)
}

// renderError writes an error response on the plain writer: an error is no stored
// content, so it does not carry Content-Disposition.
func renderError(f Format, w http.ResponseWriter, rq *Request, err error) {
	w.Header().Del("Content-Disposition")
	f.WriteError(w, rq.Request, err)
}

// trackWriter remembers that the status line is out, and removes the headers the
// answer must not carry when it goes out.
type trackWriter struct {
	http.ResponseWriter
	wrote bool
	hide  []string
}

// scrub removes the hidden headers; the status line follows.
func (w *trackWriter) scrub() {
	for _, h := range w.hide {
		w.ResponseWriter.Header().Del(h)
	}
}

func (w *trackWriter) WriteHeader(code int) {
	w.scrub()
	if code >= 200 {
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *trackWriter) Write(p []byte) (int, error) {
	if !w.wrote {
		w.scrub()
	}
	w.wrote = true
	return w.ResponseWriter.Write(p)
}

func (w *trackWriter) Flush() {
	if !w.wrote {
		w.scrub()
	}
	w.wrote = true
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *trackWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Base gives a format the defaults for what it lacks: no-op storage hooks, no
// adapters, no setup templates, no serve-time check and the problem+json error
// shape. A format embeds it and implements Descriptor, Routes, Match
// (MatchRoutes over its routes), Classify and PackageDocumentPath itself: Base
// cannot see the routes of the type that embeds it.
type Base struct{}

// IngestOptions asks nothing of the pipeline.
func (Base) IngestOptions(*Request) IngestOptions { return IngestOptions{} }

// Inspect reads nothing: the upload is an opaque file.
func (Base) Inspect(context.Context, InspectInput) (*Inspection, error) { return nil, nil }

// OnCommit does nothing.
func (Base) OnCommit(context.Context, Tx, CommitEvent) error { return nil }

// OnItemsRemoved does nothing.
func (Base) OnItemsRemoved(context.Context, Tx, ItemsEvent) error { return nil }

// OnItemsRestored does nothing.
func (Base) OnItemsRestored(context.Context, Tx, ItemsEvent) error { return nil }

// RemoveVersion removes nothing: retention does not exist yet.
func (Base) RemoveVersion(context.Context, Tx, VersionRef) ([]ItemRef, error) { return nil, nil }

// VersionGroup puts every version in one group.
func (Base) VersionGroup(VersionRef) string { return "" }

// ServeCheck refuses nothing.
func (Base) ServeCheck(context.Context, Host, RepoRef, ItemRef, ServeMode) error { return nil }

// Remote is nil: the format has no remote class.
func (Base) Remote() RemoteAdapter { return nil }

// Virtual is nil: the format has no virtual class.
func (Base) Virtual() VirtualAdapter { return nil }

// Indexes is nil: the format has no rebuildable derived metadata.
func (Base) Indexes() IndexBuilder { return nil }

// Exposure is nil: the format is not exposable on an endpoint with rules.
func (Base) Exposure() ExposureAdapter { return nil }

// SetupTemplates is empty.
func (Base) SetupTemplates() []SetupTemplate { return nil }

// Guide is empty.
func (Base) Guide() []GuideSection { return nil }

// WriteError renders the error as problem+json (R-ARCH-023).
func (Base) WriteError(w http.ResponseWriter, r *http.Request, err error) {
	errs.Write(w, r, err, w.Header().Get("X-Request-Id"), slog.Default())
}
