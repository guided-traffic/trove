package deb

import (
	"context"
	"net/http"

	"github.com/guided-traffic/trove/internal/formats"
)

func init() {
	registerHandler(routeDists, func(f *Format) formats.HandlerFunc { return f.handleDists })
	registerHandler(routeByHash, func(f *Format) formats.HandlerFunc { return f.handleByHash })
}

// The Cache-Control values of R-DEB-005: an index or a release document is revalidated, a file by its hash never changes.
const (
	cacheRevalidate = "private, max-age=0, must-revalidate"
	cacheImmutable  = "private, max-age=31536000, immutable"
)

// handleDists is GET dists/<suite>/…: a file of the published generation of a local repository, a generated item (R-DEB-003,
// R-DEB-010). Every other path below dists/ is a 404; no path below it is an upload target. The remote and the virtual class are served
// by the work packages of those classes.
func (f *Format) handleDists(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	return f.serveIndexFile(ctx, rq, w, cacheRevalidate)
}

// handleByHash is GET dists/<suite>/<dir>/by-hash/SHA256/<hex>: an index file by its digest, in every generation that retention keeps
// (R-DEB-012).
func (f *Format) handleByHash(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	return f.serveIndexFile(ctx, rq, w, cacheImmutable)
}

// serveIndexFile serves a generated file of a local or a virtual repository; the virtual one passes the gate on its members first
// (R-DEB-027). The class that is neither is the remote's, answered by its own handler.
func (f *Format) serveIndexFile(ctx context.Context, rq *formats.Request, w http.ResponseWriter, cache string) error {
	switch rq.Repo.Class {
	case formats.Local:
	case formats.Virtual:
		if err := rq.Host.Authorize(ctx, rq, formats.Target{Path: rq.Path.Path, Actions: []formats.Action{formats.ActionRead}}); err != nil {
			return err
		}
		if err := f.virtualGate(ctx, rq, rq.Path.Path); err != nil {
			return err
		}
	default:
		return notImplemented()
	}
	return f.serveGenerated(ctx, rq, w, cache)
}

// serveGenerated authorizes the read of a path that belongs to no package (R-DEB-004: only a grant without package patterns covers an
// index), finds the generated item and serves it with the streaming, range and conditional rules of the core (R-STOR-012). The item's
// media type is the one the build stored; the cache rule is the format's.
func (f *Format) serveGenerated(ctx context.Context, rq *formats.Request, w http.ResponseWriter, cache string) error {
	p := rq.Path.Path
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Path: p, Actions: []formats.Action{formats.ActionRead}}); err != nil {
		return err
	}
	it, found, err := rq.Host.Items().Stat(ctx, rq.RepoRef(), rq.Path)
	if err != nil {
		return err
	}
	if !found || it.Folder {
		return errNoSuchFile()
	}
	return rq.Host.Items().Serve(ctx, rq, cacheWriter{ResponseWriter: w, cache: cache}, it, formats.ServeMayRevalidate)
}

// cacheWriter sets the Cache-Control of R-DEB-005 on the answers that carry content.
type cacheWriter struct {
	http.ResponseWriter
	cache string
}

func (w cacheWriter) WriteHeader(code int) {
	switch code {
	case http.StatusOK, http.StatusPartialContent, http.StatusNotModified:
		w.Header().Set("Cache-Control", w.cache)
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w cacheWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
