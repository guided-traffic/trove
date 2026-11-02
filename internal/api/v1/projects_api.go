package v1

import (
	"net/http"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/project"
)

// The project operations (R-AUTHZ-026, R-API-047). WP-B fills the handlers over
// [project.Service]; until then each answers 501 not_implemented through the error
// model, so the server starts and the document is complete.
//
// What each handler must do beyond the call, so that the declaration of
// api/openapi.yaml stays true (docs/developer/projects.md lists the contract):
//
//   - listProjects, getProject: the declaration admits a project administrator
//     (projectAdmin: any); narrow with delegatedScope (project_scope.go): a caller
//     without projects:read sees the projects it administers, and a project it does
//     not administer answers 404.
//   - lookupProjectPrincipals: requireProject(ctx, "identities:read", key,
//     authz.ProjectManagePolicies).
//   - createProject, replaceProject, deleteProject, addProjectAdmin, removeProjectAdmin: the
//     declaration is projects:write, no narrowing. Writes answer Trove-Authz-Epoch
//     (setEpoch, finishEpoch); createProject is idempotent (s.idem.Do); replaceProject
//     takes If-Match (ETag, x-trove-if-match: required, 428 from the binder).
//   - assignProjectRepository, unassignProjectRepository: repositories:write-config,
//     epoch header, no If-Match.

// projects returns the project service, or the 503 of a replica without it.
func (s *Server) projects() (project.Service, error) {
	if s.o.Authz.Projects == nil {
		return nil, unavailable("The project service")
	}
	return s.o.Authz.Projects, nil
}

// notImplemented answers an operation whose handler has not been written: the 503
// of a replica without the project service first, as every handler answers it, then
// 501.
func (s *Server) notImplemented(w http.ResponseWriter, r *http.Request) {
	if _, err := s.projects(); err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	httpserver.Problem(w, r, errs.E(errs.NotImplemented, errs.CodeNotImplemented, "Projects are not available in this build yet."))
}

// ListProjects implements GET /api/v1/projects (R-AUTHZ-026).
func (s *Server) ListProjects(w http.ResponseWriter, r *http.Request, _ gen.ListProjectsParams) {
	s.notImplemented(w, r)
}

// CreateProject implements POST /api/v1/projects.
func (s *Server) CreateProject(w http.ResponseWriter, r *http.Request, _ gen.CreateProjectParams) {
	s.notImplemented(w, r)
}

// GetProject implements GET /api/v1/projects/{key}.
func (s *Server) GetProject(w http.ResponseWriter, r *http.Request, _ string, _ gen.GetProjectParams) {
	s.notImplemented(w, r)
}

// ReplaceProject implements PUT /api/v1/projects/{key}.
func (s *Server) ReplaceProject(w http.ResponseWriter, r *http.Request, _ string, _ gen.ReplaceProjectParams) {
	s.notImplemented(w, r)
}

// DeleteProject implements DELETE /api/v1/projects/{key}.
func (s *Server) DeleteProject(w http.ResponseWriter, r *http.Request, _ string, _ gen.DeleteProjectParams) {
	s.notImplemented(w, r)
}

// AddProjectAdmin implements PUT /api/v1/projects/{key}/admins/{principalRef}.
func (s *Server) AddProjectAdmin(w http.ResponseWriter, r *http.Request, _, _ string) {
	s.notImplemented(w, r)
}

// RemoveProjectAdmin implements DELETE /api/v1/projects/{key}/admins/{principalRef}.
func (s *Server) RemoveProjectAdmin(w http.ResponseWriter, r *http.Request, _, _ string) {
	s.notImplemented(w, r)
}

// AssignProjectRepository implements PUT /api/v1/projects/{key}/repositories/{repoKey}.
func (s *Server) AssignProjectRepository(w http.ResponseWriter, r *http.Request, _, _ string, _ gen.AssignProjectRepositoryParams) {
	s.notImplemented(w, r)
}

// UnassignProjectRepository implements DELETE /api/v1/projects/{key}/repositories/{repoKey}.
func (s *Server) UnassignProjectRepository(w http.ResponseWriter, r *http.Request, _, _ string) {
	s.notImplemented(w, r)
}

// LookupProjectPrincipals implements GET /api/v1/projects/{key}/principals (Q-AUTHZ-182).
func (s *Server) LookupProjectPrincipals(w http.ResponseWriter, r *http.Request, _ string, _ gen.LookupProjectPrincipalsParams) {
	s.notImplemented(w, r)
}
