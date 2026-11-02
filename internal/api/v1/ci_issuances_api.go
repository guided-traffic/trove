package v1

import (
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
)

// The issuance operations (R-CI-020), work package W5: tokens:read for the list and the read,
// tokens:revoke for the bulk revoke (the declarations are in the document). The list pages by cursor
// (R-API-010); the bulk revoke needs `issuer` and at least one of projectId, pipelineId, jobId,
// mapping (422 otherwise) and answers {"revoked": n}.

func (s *Server) ciIssuances(w http.ResponseWriter, r *http.Request) {
	ciPending(w, r, s.o.CI.Issuances != nil, "The CI issuance service", "W5")
}

// ListCiIssuances implements GET /api/v1/ci/issuances.
func (s *Server) ListCiIssuances(w http.ResponseWriter, r *http.Request, _ gen.ListCiIssuancesParams) {
	s.ciIssuances(w, r)
}

// GetCiIssuance implements GET /api/v1/ci/issuances/{tokenId}.
func (s *Server) GetCiIssuance(w http.ResponseWriter, r *http.Request, _ openapi_types.UUID) {
	s.ciIssuances(w, r)
}

// RevokeCiIssuances implements POST /api/v1/ci/issuances/revoke.
func (s *Server) RevokeCiIssuances(w http.ResponseWriter, r *http.Request) {
	ciPending(w, r, s.o.CI.Revoker != nil, "The CI revocation service", "W5")
}
