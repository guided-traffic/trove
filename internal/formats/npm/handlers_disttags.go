package npm

import (
	"context"
	"net/http"

	"github.com/guided-traffic/trove/internal/formats"
)

// Dist-tags (R-NPM-016). The handlers are built with the documents; until then the
// routes answer 501. The writes check ownership with f.claim(..., opDistTag, ...)
// under the package row lock, and call the store's SetTag and DeleteTag.

func (f *Format) handleDistTags(context.Context, *formats.Request, http.ResponseWriter) error {
	return pendingHandler("The dist-tags document")
}

func (f *Format) handleDistTagSet(context.Context, *formats.Request, http.ResponseWriter) error {
	return pendingHandler("Setting a dist-tag")
}

func (f *Format) handleDistTagDelete(context.Context, *formats.Request, http.ResponseWriter) error {
	return pendingHandler("Removing a dist-tag")
}
