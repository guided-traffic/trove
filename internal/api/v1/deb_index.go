package v1

import (
	"net/http"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
)

// GetDebIndexState implements GET /api/v1/projects/{projectKey}/repositories/{repoKey}/deb/index (R-DEB-014): the state of each suite,
// with the wait of waitFor. It answers 501 deb_not_implemented until the work package of the index builder builds it
// (apt-work-packages.md, WP4), which replaces this file.
func (s *Server) GetDebIndexState(w http.ResponseWriter, r *http.Request, _ gen.ProjectKey, _ gen.RepoKey, _ gen.GetDebIndexStateParams) {
	s.debNotImplemented(w, r)
}
