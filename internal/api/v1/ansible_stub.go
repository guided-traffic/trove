package v1

import (
	"net/http"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// ansibleNotImplemented answers an operation of the Ansible Galaxy format that the local class has not built yet (the skeleton of
// ansible-work-packages.md, WP0; the file goes with the last of them): 501, or 503 on a replica that has no database, which every
// operation needs.
func (s *Server) ansibleNotImplemented(w http.ResponseWriter, r *http.Request) {
	if s.o.API.Store == nil {
		httpserver.Problem(w, r, unavailable("The Ansible Galaxy repository operations"))
		return
	}
	httpserver.Problem(w, r, errs.E(errs.NotImplemented, errs.CodeAnsibleNotImplemented, "This operation of the Ansible Galaxy repository is not built yet."))
}
