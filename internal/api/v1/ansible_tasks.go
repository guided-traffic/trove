package v1

import (
	"net/http"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
)

// ListAnsibleImportTasks implements GET /api/v1/projects/{projectKey}/repositories/{repoKey}/ansible/imports (R-ANS-009): the import
// tasks of a local Ansible Galaxy repository. It answers 501 ansible_not_implemented until the local class builds it.
func (s *Server) ListAnsibleImportTasks(w http.ResponseWriter, r *http.Request, _ gen.ProjectKey, _ gen.RepoKey, _ gen.ListAnsibleImportTasksParams) {
	s.ansibleNotImplemented(w, r)
}
