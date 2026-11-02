package nuget

import (
	"context"
	"net/http"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/nuget/document"
)

// The flat container (R-NUGET-008): the version list of an ID and the files of a version.

const (
	nupkgMediaType  = "application/octet-stream"
	nuspecMediaType = "application/xml"
)

// handleFlatIndex is GET v3/flatcontainer/{id}/index.json: every stored version of the
// ID, listed and unlisted, SemVer 1 and 2, in URL form and ascending by precedence. An ID
// without a version, or without one the endpoint exposes, is a 404 (R-NUGET-008,
// R-NUGET-034). The list is a rendered document with a weak validator (R-NUGET-002).
func (f *Format) handleFlatIndex(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	if rq.Repo.Class == formats.Remote {
		return f.remoteFlatIndex(ctx, rq, w)
	}
	lowerID := rq.Params.Get("id")
	repoID := rq.RepoRef().ID
	keep := exposes(viewOf(rq), lowerID)
	return f.serveDocument(ctx, rq, w, lowerID, docSpec{
		variant: "flat",
		render: func(ctx context.Context, st Store) ([]byte, int64, bool, error) {
			entries, err := st.Versions(ctx, repoID, lowerID)
			if err != nil {
				return nil, 0, false, err
			}
			list := make([]string, 0, len(entries))
			for _, e := range entries {
				if keep == nil || keep(e.LowerVersion) {
					list = append(list, e.LowerVersion)
				}
			}
			if len(list) == 0 {
				return nil, 0, false, nil
			}
			body, err := document.VersionList(list)
			if err != nil {
				return nil, 0, false, internalError(err)
			}
			return body, 0, true, nil
		},
	})
}

// handleNupkg is GET v3/flatcontainer/{id}/{version}/{id}.{version}.nupkg: the package
// bytes as received (R-NUGET-008).
func (f *Format) handleNupkg(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	lowerID, ver := rq.Params.Get("id"), rq.Params.Get("version")
	return f.serveFile(ctx, rq, w, lowerID, ver, NupkgPath(lowerID, ver), nupkgMediaType)
}

// handleNuspec is GET v3/flatcontainer/{id}/{version}/{id}.nuspec: the .nuspec bytes from
// inside the package, generated at ingestion (R-NUGET-005, R-NUGET-008).
func (f *Format) handleNuspec(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	lowerID, ver := rq.Params.Get("id"), rq.Params.Get("version")
	return f.serveFile(ctx, rq, w, lowerID, ver, NuspecPath(lowerID, ver), nuspecMediaType)
}

// serveFile serves the stored item at a canonical path with the streaming, range and
// conditional rules of the core (R-STOR-012), after the read permission on the version and
// the exposure of the endpoint are decided. The tombstone and the vulnerability block are
// the serve check's (ServeCheck), which the core runs for every item it serves. A version
// that is not stored, or that the caller may not read, or that the endpoint hides, answers
// 404.
func (f *Format) serveFile(ctx context.Context, rq *formats.Request, w http.ResponseWriter, lowerID, ver, path, mediaType string) error {
	if rq.Repo.Class == formats.Remote {
		return f.remoteFile(ctx, rq, w, lowerID, ver, path, mediaType)
	}
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Package: lowerID, Version: ver, Actions: []formats.Action{formats.ActionRead}}); err != nil {
		return err
	}
	// The ceiling decided the version above; the view repeats it from the same rules, so a
	// file is served exactly when a filtered document lists its version (R-NUGET-034).
	if hiddenVersion(rq, lowerID, ver) {
		return errNoSuchResource()
	}
	it, found, err := rq.Host.Items().Stat(ctx, rq.RepoRef(), formats.Path{Path: path})
	if err != nil {
		return err
	}
	if !found || it.Folder {
		return errNoSuchResource()
	}
	return rq.Host.Items().Serve(ctx, rq, mediaTypeWriter{ResponseWriter: w, mediaType: mediaType}, it, formats.ServeMayRevalidate)
}

// mediaTypeWriter sets the Content-Type of a file on the status line of a response that
// carries it: the type of R-NUGET-008 is the protocol's, whatever the item row recorded.
type mediaTypeWriter struct {
	http.ResponseWriter
	mediaType string
}

func (w mediaTypeWriter) WriteHeader(code int) {
	switch code {
	case http.StatusOK, http.StatusPartialContent, http.StatusNotModified:
		w.Header().Set("Content-Type", w.mediaType)
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w mediaTypeWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
