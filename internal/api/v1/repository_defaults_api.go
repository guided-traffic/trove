package v1

import (
	"net/http"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// The defaults of a new repository (R-REPO-063, Q-REPO-324): what a create of only key, class and format in the project would
// store, and a key that is free in the project. The creation form reads them for the values of its fields.
//
// Status: the contract of work package WP0 of docs/projektplan/repository-setup-work-packages.md. The operation, its schema and
// its parameters are in api/openapi.yaml and the generated server interface; the handler answers 503 unavailable until work
// package WP2 builds it (the contract test of R-API-002 refuses any other status of 500 or above on a GET).

// GetRepositoryDefaults implements GET /api/v1/projects/{projectKey}/repository-defaults.
func (s *Server) GetRepositoryDefaults(w http.ResponseWriter, r *http.Request, _ gen.ProjectKey, _ gen.GetRepositoryDefaultsParams) {
	httpserver.Problem(w, r, unavailable("The operation getRepositoryDefaults"))
}
