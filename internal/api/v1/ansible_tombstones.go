package v1

import (
	"net/http"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
)

// ListAnsibleTombstones implements GET /api/v1/projects/{projectKey}/repositories/{repoKey}/ansible/tombstones (R-ANS-012): the deleted
// versions whose numbers are withheld. It answers 501 ansible_not_implemented until the local class builds it.
func (s *Server) ListAnsibleTombstones(w http.ResponseWriter, r *http.Request, _ gen.ProjectKey, _ gen.RepoKey, _ gen.ListAnsibleTombstonesParams) {
	s.ansibleNotImplemented(w, r)
}

// ClearAnsibleTombstone implements DELETE /api/v1/projects/{projectKey}/repositories/{repoKey}/ansible/tombstones/{collection}/{version}
// (R-ANS-012): the clearing of one tombstone, with the journaled event ansible.tombstone.cleared. It answers 501 ansible_not_implemented
// until the local class builds it.
func (s *Server) ClearAnsibleTombstone(w http.ResponseWriter, r *http.Request, _ gen.ProjectKey, _ gen.RepoKey, _, _ string) {
	s.ansibleNotImplemented(w, r)
}
