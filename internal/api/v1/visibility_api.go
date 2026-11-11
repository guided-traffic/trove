package v1

import (
	"net/http"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// The visibility operations (R-API-056, R-API-057). The document and the generated types are final; the service behind them
// lands with the writes of the levels, and until a replica holds it the operations answer 503 after the shared authorization
// step decided the caller.

func visibilityNotBuilt() error { return unavailable("The visibility service") }

// GetVisibilitySettings implements GET /api/v1/settings/visibility (R-API-056).
func (s *Server) GetVisibilitySettings(w http.ResponseWriter, r *http.Request, _ gen.GetVisibilitySettingsParams) {
	httpserver.Problem(w, r, visibilityNotBuilt())
}

// UpdateVisibilitySettings implements PATCH /api/v1/settings/visibility (R-API-056).
func (s *Server) UpdateVisibilitySettings(w http.ResponseWriter, r *http.Request, _ gen.UpdateVisibilitySettingsParams) {
	httpserver.Problem(w, r, visibilityNotBuilt())
}

// SetProjectVisibility implements PUT /api/v1/projects/{projectKey}/visibility (R-API-057).
func (s *Server) SetProjectVisibility(w http.ResponseWriter, r *http.Request, _ gen.ProjectKey, _ gen.SetProjectVisibilityParams) {
	httpserver.Problem(w, r, visibilityNotBuilt())
}
