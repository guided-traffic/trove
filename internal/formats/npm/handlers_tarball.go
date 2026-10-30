package npm

import (
	"context"
	"net/http"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/npm/name"
)

// immutableCacheControl is the Cache-Control of a tarball (R-NPM-013): its bytes never
// change under their number, so a client keeps them for a year. It is private
// always: whether the anonymous principal may read the package is a decision of the
// repository's policies, which a response header cannot follow (Q-NPM-28).
const immutableCacheControl = "private, max-age=31536000, immutable"

// handleTarball is GET {name}/-/{basename}-{version}.tgz (R-NPM-015, flow F2). The
// file name names the version, which must be canonical; the item at the canonical
// path is served by the core with its streaming, range and conditional rules
// (R-STOR-012), after ServeCheck has refused a tombstoned or withheld version.
func (f *Format) handleTarball(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	n, err := name.Parse(rq.Params.Get("name"))
	if err != nil {
		return err
	}
	version, ok := VersionOfFile(n, rq.Params.Get("file"), tarballSuffix)
	if !ok {
		return notFound()
	}
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Package: n.String(), Version: version, Actions: []formats.Action{formats.ActionRead}}); err != nil {
		return err
	}
	it, found, err := rq.Host.Items().Stat(ctx, rq.RepoRef(), formats.Path{Path: TarballPath(n, version)})
	if err != nil {
		return err
	}
	if !found || it.Folder {
		return notFound()
	}
	return rq.Host.Items().Serve(ctx, rq, immutableWriter{w}, it, formats.ServeMayRevalidate)
}

// immutableWriter sets the Cache-Control of a tarball on the status line of a
// response that carries or validates the file; the core's serving sets its own
// revalidation header for a stored file, which a tarball under its number
// outgrows.
type immutableWriter struct{ http.ResponseWriter }

func (w immutableWriter) WriteHeader(code int) {
	switch code {
	case http.StatusOK, http.StatusPartialContent, http.StatusNotModified:
		w.Header().Set("Cache-Control", immutableCacheControl)
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w immutableWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
