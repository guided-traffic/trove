package npm

import (
	"context"
	"net/http"

	"github.com/guided-traffic/trove/internal/formats"
)

// whoami and ping (R-NPM-019). The handlers are built with the documents; until then
// the routes answer 501. principalName (publish.go) is the rendering of R-AUTH-033.

func (f *Format) handlePing(context.Context, *formats.Request, http.ResponseWriter) error {
	return pendingHandler("ping")
}

func (f *Format) handleWhoami(context.Context, *formats.Request, http.ResponseWriter) error {
	return pendingHandler("whoami")
}
