package npm

import (
	"context"
	"net/http"

	"github.com/guided-traffic/trove/internal/formats"
)

// The package document and the single-version document (R-NPM-009 to R-NPM-014).
// The handlers are built with the documents; until then the routes answer 501.

func (f *Format) handleDocument(context.Context, *formats.Request, http.ResponseWriter) error {
	return pendingHandler("The package document")
}

func (f *Format) handleVersion(context.Context, *formats.Request, http.ResponseWriter) error {
	return pendingHandler("The single-version document")
}
