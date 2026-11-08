package v1

import (
	"net/http"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
)

// PutDebCoordinates implements PUT /api/v1/projects/{projectKey}/repositories/{repoKey}/deb/coordinates (R-DEB-009): the replacement
// of the suites and components a stored package file is listed in. It answers 501 deb_not_implemented until the work package of the
// local class builds it (apt-work-packages.md, WP3), which replaces this file.
func (s *Server) PutDebCoordinates(w http.ResponseWriter, r *http.Request, _ gen.ProjectKey, _ gen.RepoKey) {
	s.debNotImplemented(w, r)
}
