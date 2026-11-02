package v1

import (
	"net/http"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
)

// The CI issuer and signing-key operations (R-CI-001 to R-CI-003, R-CI-005, Q-CI-16), work package
// W1. The declarations are in the document: ci:read for the reads and ci:write for the writes, recent
// authentication for create, replace and delete, x-trove-if-match required on the replace.
//
// What each handler must do beyond the call to [ci.Issuers], so that the declaration stays true:
//
//   - createCiIssuer: s.idem is not used (the name is the key; a repeat is 409); 201 with Location and
//     ETag; validate defaults to true; the 422 codes of R-CI-002 are field errors with a pointer.
//   - replaceCiIssuer: the ETag is the issuer's version, 428 comes from the binder, 412 carries
//     currentEtag; a configuration issuer answers 409 issuer_managed_by_configuration.
//   - deleteCiIssuer: cascade=true deletes the mappings; If-Match optional.
//   - refreshCiIssuerKeys: 200 with the key status; an unreachable issuer is 502.

func (s *Server) ciIssuers(w http.ResponseWriter, r *http.Request) {
	ciPending(w, r, s.o.CI.Issuers != nil, "The CI issuer service", "W1")
}

// ListCiIssuers implements GET /api/v1/ci/issuers.
func (s *Server) ListCiIssuers(w http.ResponseWriter, r *http.Request) { s.ciIssuers(w, r) }

// CreateCiIssuer implements POST /api/v1/ci/issuers.
func (s *Server) CreateCiIssuer(w http.ResponseWriter, r *http.Request) { s.ciIssuers(w, r) }

// GetCiIssuer implements GET /api/v1/ci/issuers/{name}.
func (s *Server) GetCiIssuer(w http.ResponseWriter, r *http.Request, _ string) { s.ciIssuers(w, r) }

// ReplaceCiIssuer implements PUT /api/v1/ci/issuers/{name}.
func (s *Server) ReplaceCiIssuer(w http.ResponseWriter, r *http.Request, _ string, _ gen.ReplaceCiIssuerParams) {
	s.ciIssuers(w, r)
}

// DeleteCiIssuer implements DELETE /api/v1/ci/issuers/{name}.
func (s *Server) DeleteCiIssuer(w http.ResponseWriter, r *http.Request, _ string, _ gen.DeleteCiIssuerParams) {
	s.ciIssuers(w, r)
}

// GetCiIssuerKeys implements GET /api/v1/ci/issuers/{name}/keys.
func (s *Server) GetCiIssuerKeys(w http.ResponseWriter, r *http.Request, _ string) { s.ciIssuers(w, r) }

// RefreshCiIssuerKeys implements POST /api/v1/ci/issuers/{name}/keys/refresh.
func (s *Server) RefreshCiIssuerKeys(w http.ResponseWriter, r *http.Request, _ string) {
	s.ciIssuers(w, r)
}
