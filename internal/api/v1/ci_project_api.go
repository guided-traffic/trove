package v1

import (
	"net/http"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/ci"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// The CI operations of a project (Q-CI-26), work packages W1 (the issuer choices), W3 (the mappings and
// the dry run) and W5 (the issuances). The declaration is {permission: ci:read | ci:write | tokens:read,
// projectAdmin: managePolicies} (ADR-146): the shared step admits the capability holder or any project
// administrator, and each handler narrows with requireProject, which answers 404 for a project the caller
// does not administer and 403 project_switch_off while managePolicies is off. The narrowing is real
// here; the work behind it is the work package's.
//
// What each handler must do beyond the narrowing, with the service of internal/ci:
//
//   - listProjectCiIssuers: Issuers.Choices, read-only (built, W1).
//   - list, get, create, replace, delete of a mapping: Mappings.ListIn, GetIn, CreateIn, ReplaceIn,
//     DeleteIn with the project from the path; the bounds of a project mapping (name prefix, no groups,
//     the project's policies and repositories) are the service's; a mapping of another tier is 404.
//   - evaluateProjectCiMappings: Mappings.EvaluateIn, the project's tier only.
//   - listProjectCiIssuances: Issuances.ListForProject.

func (s *Server) ciProject(w http.ResponseWriter, r *http.Request, capability, project string, available bool, what, workPackage string) {
	if err := s.requireProject(r.Context(), capability, project, authz.ProjectManagePolicies); err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	ciPending(w, r, available, what, workPackage)
}

// ListProjectCiIssuers implements GET /api/v1/projects/{key}/ci/issuers: the issuers read-only, name,
// kind, issuer URL and public_issuer, for a project administrator who chooses one (Q-CI-26).
func (s *Server) ListProjectCiIssuers(w http.ResponseWriter, r *http.Request, key string) {
	if err := s.requireProject(r.Context(), "ci:read", key, authz.ProjectManagePolicies); err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	svc, err := s.ciIssuerService()
	var choices []ci.IssuerChoice
	if err == nil {
		choices, err = svc.Choices(r.Context())
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	items := make([]gen.CiIssuerChoice, 0, len(choices))
	for _, c := range choices {
		items = append(items, gen.CiIssuerChoice{Name: c.Name, Kind: c.Kind, Issuer: c.Issuer, PublicIssuer: c.PublicIssuer})
	}
	writeJSON(w, r, http.StatusOK, gen.CiIssuerChoiceList{Items: items})
}

// ListProjectCiMappings implements GET /api/v1/projects/{key}/ci/mappings.
func (s *Server) ListProjectCiMappings(w http.ResponseWriter, r *http.Request, key string) {
	s.ciProject(w, r, "ci:read", key, s.o.CI.Mappings != nil, "The CI mapping service", "W3")
}

// CreateProjectCiMapping implements POST /api/v1/projects/{key}/ci/mappings.
func (s *Server) CreateProjectCiMapping(w http.ResponseWriter, r *http.Request, key string) {
	s.ciProject(w, r, "ci:write", key, s.o.CI.Mappings != nil, "The CI mapping service", "W3")
}

// GetProjectCiMapping implements GET /api/v1/projects/{key}/ci/mappings/{issuer}/{name}.
func (s *Server) GetProjectCiMapping(w http.ResponseWriter, r *http.Request, key, _, _ string) {
	s.ciProject(w, r, "ci:read", key, s.o.CI.Mappings != nil, "The CI mapping service", "W3")
}

// ReplaceProjectCiMapping implements PUT /api/v1/projects/{key}/ci/mappings/{issuer}/{name}.
func (s *Server) ReplaceProjectCiMapping(w http.ResponseWriter, r *http.Request, key, _, _ string, _ gen.ReplaceProjectCiMappingParams) {
	s.ciProject(w, r, "ci:write", key, s.o.CI.Mappings != nil, "The CI mapping service", "W3")
}

// DeleteProjectCiMapping implements DELETE /api/v1/projects/{key}/ci/mappings/{issuer}/{name}.
func (s *Server) DeleteProjectCiMapping(w http.ResponseWriter, r *http.Request, key, _, _ string, _ gen.DeleteProjectCiMappingParams) {
	s.ciProject(w, r, "ci:write", key, s.o.CI.Mappings != nil, "The CI mapping service", "W3")
}

// EvaluateProjectCiMappings implements POST /api/v1/projects/{key}/ci/issuers/{issuer}/evaluate.
func (s *Server) EvaluateProjectCiMappings(w http.ResponseWriter, r *http.Request, key, _ string) {
	s.ciProject(w, r, "ci:read", key, s.o.CI.Mappings != nil, "The CI mapping service", "W3")
}

// ListProjectCiIssuances implements GET /api/v1/projects/{key}/ci/issuances.
func (s *Server) ListProjectCiIssuances(w http.ResponseWriter, r *http.Request, key string, _ gen.ListProjectCiIssuancesParams) {
	s.ciProject(w, r, "tokens:read", key, s.o.CI.Issuances != nil, "The CI issuance service", "W5")
}
