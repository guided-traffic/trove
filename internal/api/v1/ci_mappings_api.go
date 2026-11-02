package v1

import (
	"net/http"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
)

// The identity-mapping operations and the dry run (R-CI-009, R-CI-019), work package W3. The
// declarations are in the document: ci:read for the reads and the dry run, ci:write with recent
// authentication for create, replace and delete, x-trove-if-match required on the replace.
//
// What each handler must do beyond the call to [ci.Mappings]:
//
//   - createCiMapping, replaceCiMapping: the answer carries `warnings` (R-CI-010, R-CI-011); 409
//     priority_taken; the 422 codes carry the pointer of the member; the stored forms of match and
//     grants are the wire forms (lowerCamelCase), decoded with claims.ParseCondition and claims.Spec.
//   - deleteCiMapping: If-Match optional; revokes the mapping's tokens (R-CI-017).
//   - evaluateCiMappings: takes claims, never a token (R-CI-019); it issues nothing and audits nothing.

func (s *Server) ciMappings(w http.ResponseWriter, r *http.Request) {
	ciPending(w, r, s.o.CI.Mappings != nil, "The CI mapping service", "W3")
}

// ListCiMappings implements GET /api/v1/ci/issuers/{issuer}/mappings.
func (s *Server) ListCiMappings(w http.ResponseWriter, r *http.Request, _ string) { s.ciMappings(w, r) }

// CreateCiMapping implements POST /api/v1/ci/issuers/{issuer}/mappings.
func (s *Server) CreateCiMapping(w http.ResponseWriter, r *http.Request, _ string) {
	s.ciMappings(w, r)
}

// GetCiMapping implements GET /api/v1/ci/issuers/{issuer}/mappings/{name}.
func (s *Server) GetCiMapping(w http.ResponseWriter, r *http.Request, _, _ string) {
	s.ciMappings(w, r)
}

// ReplaceCiMapping implements PUT /api/v1/ci/issuers/{issuer}/mappings/{name}.
func (s *Server) ReplaceCiMapping(w http.ResponseWriter, r *http.Request, _, _ string, _ gen.ReplaceCiMappingParams) {
	s.ciMappings(w, r)
}

// DeleteCiMapping implements DELETE /api/v1/ci/issuers/{issuer}/mappings/{name}.
func (s *Server) DeleteCiMapping(w http.ResponseWriter, r *http.Request, _, _ string, _ gen.DeleteCiMappingParams) {
	s.ciMappings(w, r)
}

// EvaluateCiMappings implements POST /api/v1/ci/issuers/{issuer}/evaluate.
func (s *Server) EvaluateCiMappings(w http.ResponseWriter, r *http.Request, _ string) {
	s.ciMappings(w, r)
}
