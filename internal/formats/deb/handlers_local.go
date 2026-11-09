package deb

import (
	"context"
	"net/http"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
)

func init() {
	registerHandler(routePoolGet, func(f *Format) formats.HandlerFunc { return f.handlePoolGet })
	registerHandler(routePoolDelete, func(f *Format) formats.HandlerFunc { return f.handlePoolDelete })
}

func errNoSuchFile() error {
	return errs.E(errs.NotFound, errs.CodeNotFound, "The repository holds no such file.")
}

// handlePoolGet is GET pool/<path>: the package file of a local repository, served with the streaming, range and conditional rules of
// the core (R-STOR-012), after the read permission on its name and version is decided. A file the replay withheld is the serve check's
// (ServeCheck), which the core runs for every item it serves. A remote or virtual repository's pool is served by the work packages of
// those classes.
func (f *Format) handlePoolGet(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	if rq.Repo.Class == formats.Virtual {
		return f.handleVirtualPool(ctx, rq, w)
	}
	if rq.Repo.Class != formats.Local {
		return notImplemented()
	}
	p := rq.Path.Path
	info := f.Classify(rq.Path)
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Package: info.Package, Version: info.Version, Path: p, Actions: []formats.Action{formats.ActionRead}}); err != nil {
		return err
	}
	// A version the caller's path grants hide is a missing one for every file of it (Q-AUTHZ-255).
	if info.Package != "" {
		cv, err := formats.CallerVersions(ctx, rq, info.Package)
		if err != nil {
			return err
		}
		if !cv.Version(info.Version) {
			return errNoSuchFile()
		}
	}
	it, found, err := rq.Host.Items().Stat(ctx, rq.RepoRef(), rq.Path)
	if err != nil {
		return err
	}
	if !found || it.Folder {
		return errNoSuchFile()
	}
	return rq.Host.Items().Serve(ctx, rq, immutableWriter{ResponseWriter: w, mediaType: MediaType}, it, formats.ServeMayRevalidate)
}

// immutableWriter gives a package file the headers of R-DEB-005: its media type and the cache rule of immutable content, private
// because the anonymous principal's read cannot be asked at response time.
type immutableWriter struct {
	http.ResponseWriter
	mediaType string
}

func (w immutableWriter) WriteHeader(code int) {
	switch code {
	case http.StatusOK, http.StatusPartialContent, http.StatusNotModified:
		h := w.Header()
		h.Set("Content-Type", w.mediaType)
		h.Set("Cache-Control", "private, max-age=31536000, immutable")
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w immutableWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// handlePoolDelete is DELETE pool/<path> (R-DEB-013): the removal of a package file. The permission and the ownership are decided
// first. The file goes through the package writer, so the hook (OnItemsRemoved) raises change_seq of its suites, removes the rows,
// writes deb.package.deleted with its journal entry and trashes or deletes the version with its last file.
func (f *Format) handlePoolDelete(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	if rq.Repo.Class != formats.Local {
		return notImplemented()
	}
	p := rq.Path.Path
	info := f.Classify(rq.Path)
	if info.Package == "" || !strings.HasSuffix(p, ".deb") {
		return errs.E(errs.NotFound, errs.CodeNotFound, "Only a package file of the repository is deleted by this route.")
	}
	target := formats.Target{Package: info.Package, Version: info.Version, Path: p, Actions: []formats.Action{formats.ActionDelete}}
	if err := rq.Host.Authorize(ctx, rq, target); err != nil {
		return err
	}
	st, err := storeOf(rq.Host.FormatData(ID))
	if err != nil {
		return err
	}
	repo := rq.RepoRef()
	b, found, err := st.Binary(ctx, repo.ID, p)
	if err != nil {
		return err
	}
	if !found {
		return errNoSuchFile()
	}
	err = rq.Host.Write(ctx, rq, func(_ formats.Tx, pw formats.PackageWriter) error {
		// The package row lock first, as every writer of the name takes it.
		if _, _, err := pw.EnsureLocked(ctx, repo, b.Package, b.Package); err != nil {
			return err
		}
		if err := pw.Claim(ctx, target, formats.OwnershipWrite{}); err != nil {
			return err
		}
		return pw.DeleteItems(ctx, repo, p)
	})
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
