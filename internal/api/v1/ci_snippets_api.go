package v1

import (
	"net/http"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
)

// The CI snippet operation (R-API-035, R-UI-035, R-CI-027), work package W3. The answer is
// text/plain (writeText) and holds no secret; the format parameter is npm or nuget.

// GetCiSnippet implements GET /api/v1/ci/issuers/{issuer}/snippets/{kind}.
func (s *Server) GetCiSnippet(w http.ResponseWriter, r *http.Request, _, _ string, _ gen.GetCiSnippetParams) {
	ciPending(w, r, s.o.CI.Mappings != nil, "The CI mapping service", "W3")
}
