package v1

import (
	"net/http"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// The permission policies of a project (Q-AUTHZ-209): /projects/{projectKey}/policies. The policy store
// serves the instance-wide policies only; these operations answer 501 not_implemented until the project
// policies are built (the store takes the project of the route and the name within it).

// ListProjectPermissionPolicies implements GET /api/v1/projects/{projectKey}/policies.
func (s *Server) ListProjectPermissionPolicies(w http.ResponseWriter, r *http.Request, projectKey string, _ gen.ListProjectPermissionPoliciesParams) {
	httpserver.Problem(w, r, notImplemented("listProjectPermissionPolicies"))
}

// CreateProjectPermissionPolicy implements POST /api/v1/projects/{projectKey}/policies.
func (s *Server) CreateProjectPermissionPolicy(w http.ResponseWriter, r *http.Request, projectKey string, _ gen.CreateProjectPermissionPolicyParams) {
	httpserver.Problem(w, r, notImplemented("createProjectPermissionPolicy"))
}

// GetProjectPermissionPolicy implements GET /api/v1/projects/{projectKey}/policies/{name}.
func (s *Server) GetProjectPermissionPolicy(w http.ResponseWriter, r *http.Request, projectKey, name string) {
	httpserver.Problem(w, r, notImplemented("getProjectPermissionPolicy"))
}

// ReplaceProjectPermissionPolicy implements PUT /api/v1/projects/{projectKey}/policies/{name}.
func (s *Server) ReplaceProjectPermissionPolicy(w http.ResponseWriter, r *http.Request, projectKey, name string, _ gen.ReplaceProjectPermissionPolicyParams) {
	httpserver.Problem(w, r, notImplemented("replaceProjectPermissionPolicy"))
}

// PatchProjectPermissionPolicy implements PATCH /api/v1/projects/{projectKey}/policies/{name}.
func (s *Server) PatchProjectPermissionPolicy(w http.ResponseWriter, r *http.Request, projectKey, name string, _ gen.PatchProjectPermissionPolicyParams) {
	httpserver.Problem(w, r, notImplemented("patchProjectPermissionPolicy"))
}

// DeleteProjectPermissionPolicy implements DELETE /api/v1/projects/{projectKey}/policies/{name}.
func (s *Server) DeleteProjectPermissionPolicy(w http.ResponseWriter, r *http.Request, projectKey, name string, _ gen.DeleteProjectPermissionPolicyParams) {
	httpserver.Problem(w, r, notImplemented("deleteProjectPermissionPolicy"))
}
