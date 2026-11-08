package v1

import (
	"net/http"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
)

// The signing-key operations of the Debian (APT) format (R-DEB-015, R-DEB-017, R-DEB-032). Each answers 501 deb_not_implemented
// until the work package of the signing keys builds it (apt-work-packages.md, WP2), which replaces this file. The permission of each
// is declared in api/openapi.yaml and enforced before the handler runs.

// ListDebSigningKeys implements GET /api/v1/projects/{projectKey}/repositories/{repoKey}/deb/signing-keys.
func (s *Server) ListDebSigningKeys(w http.ResponseWriter, r *http.Request, _ gen.ProjectKey, _ gen.RepoKey) {
	s.debNotImplemented(w, r)
}

// CreateDebSigningKey implements POST /api/v1/projects/{projectKey}/repositories/{repoKey}/deb/signing-keys.
func (s *Server) CreateDebSigningKey(w http.ResponseWriter, r *http.Request, _ gen.ProjectKey, _ gen.RepoKey) {
	s.debNotImplemented(w, r)
}

// PromoteDebSigningKey implements POST /api/v1/projects/{projectKey}/repositories/{repoKey}/deb/signing-keys/promote.
func (s *Server) PromoteDebSigningKey(w http.ResponseWriter, r *http.Request, _ gen.ProjectKey, _ gen.RepoKey) {
	s.debNotImplemented(w, r)
}

// RetireDebSigningKey implements DELETE /api/v1/projects/{projectKey}/repositories/{repoKey}/deb/signing-keys/{fingerprint}.
func (s *Server) RetireDebSigningKey(w http.ResponseWriter, r *http.Request, _ gen.ProjectKey, _ gen.RepoKey, _ gen.DebFingerprint) {
	s.debNotImplemented(w, r)
}

// ReinstateDebSigningKey implements POST /api/v1/projects/{projectKey}/repositories/{repoKey}/deb/signing-keys/{fingerprint}/reinstate.
func (s *Server) ReinstateDebSigningKey(w http.ResponseWriter, r *http.Request, _ gen.ProjectKey, _ gen.RepoKey, _ gen.DebFingerprint) {
	s.debNotImplemented(w, r)
}
