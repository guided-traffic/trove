package v1

import (
	"net/http"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
)

// ReindexRepository implements POST /api/v1/projects/{projectKey}/repositories/{repoKey}/reindex (R-REPO-016): the full rebuild of a
// repository's derived metadata. It answers 501 deb_not_implemented until the work package of the index builder builds it
// (apt-work-packages.md, WP4), which replaces this file with the endpoint for every format that has a job-built index and the 405
// not_applicable for the others.
func (s *Server) ReindexRepository(w http.ResponseWriter, r *http.Request, _ gen.ProjectKey, _ gen.RepoKey, _ gen.ReindexRepositoryParams) {
	s.debNotImplemented(w, r)
}
