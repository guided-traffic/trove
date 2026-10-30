package npm

import (
	"context"
	"net/http"

	"github.com/guided-traffic/trove/internal/formats"
)

// Unpublish (R-NPM-017). The handlers are built with the documents; until then the
// routes answer 501. They check ownership with f.claim(..., opUnpublish, ...) under
// the package row lock, compare the revision there, and remove versions through the
// store's Tombstone and the core writer.

func (f *Format) handleUnpublishPut(context.Context, *formats.Request, http.ResponseWriter) error {
	return pendingHandler("Unpublishing a version")
}

func (f *Format) handleUnpublishPackage(context.Context, *formats.Request, http.ResponseWriter) error {
	return pendingHandler("Unpublishing a package")
}

func (f *Format) handleUnpublishTarball(context.Context, *formats.Request, http.ResponseWriter) error {
	return pendingHandler("Removing a tarball")
}
