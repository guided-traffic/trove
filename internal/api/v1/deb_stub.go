package v1

import (
	"net/http"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// debNotImplemented answers an operation of the Debian format that its work package has not built (the skeleton of apt-work-packages.md,
// WP0; the file goes with the last of them): 501, or 503 on a replica that has no
// database, which every operation needs.
func (s *Server) debNotImplemented(w http.ResponseWriter, r *http.Request) {
	if s.o.API.Store == nil {
		httpserver.Problem(w, r, unavailable("The Debian repository operations"))
		return
	}
	httpserver.Problem(w, r, errs.E(errs.NotImplemented, errs.CodeDebNotImplemented, "This operation of the Debian repository is not built yet."))
}
