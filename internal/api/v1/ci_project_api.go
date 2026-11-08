package v1

import (
	"net/http"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/ci"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// The CI operations of a project (Q-CI-26), work packages W1 (the issuer choices), W3 (the mappings, the dry
// run and the snippet) and W5 (the issuances). The declaration is {permission: ci:read | ci:write | tokens:read,
// project: path.projectKey, projectRole: owner}: the shared step admits the capability holder or an Owner of
// the project of the path.
//
// What each handler does beyond the narrowing, with the service of internal/ci:
//
//   - listProjectCiIssuers: Issuers.Choices, read-only (W1).
//   - list, get, create, replace, delete of a mapping: Mappings.ListIn, GetIn, CreateIn, ReplaceIn,
//     DeleteIn with the project from the path; the bounds of a project mapping (a name within the project, no groups,
//     the project's policies and repositories) are the service's; a mapping of another tier is 404 (W3).
//   - evaluateProjectCiMappings: Mappings.EvaluateIn, the project's tier only (W3).
//   - getProjectCiSnippet: the snippet of a mapping of the project's tier, GetIn first (W3).
//   - listProjectCiIssuances: Issuances.ListForProject (W5).

// projectMappings narrows the caller to the project and returns the mapping service.
func (s *Server) projectMappings(_ *http.Request, _, _ string) (ci.Mappings, error) {
	return s.ciMappingService()
}

// ListProjectCiIssuers implements GET /api/v1/projects/{key}/ci/issuers: the issuers read-only, name,
// kind, issuer URL and public_issuer, for a project Owner who chooses one (Q-CI-26).
func (s *Server) ListProjectCiIssuers(w http.ResponseWriter, r *http.Request, _ string) {
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

// projectMappingHref is the absolute URL of a mapping of a project.
func projectMappingHref(r *http.Request, project, issuer, name string) string {
	return location(r, "/api/v1/projects/"+project+"/ci/mappings/"+issuer+"/"+name)
}

// ListProjectCiMappings implements GET /api/v1/projects/{key}/ci/mappings.
func (s *Server) ListProjectCiMappings(w http.ResponseWriter, r *http.Request, key string) {
	svc, err := s.projectMappings(r, "ci:read", key)
	var list []ci.Mapping
	if err == nil {
		list, err = svc.ListIn(r.Context(), key)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, mappingListOut(list))
}

// CreateProjectCiMapping implements POST /api/v1/projects/{key}/ci/mappings.
func (s *Server) CreateProjectCiMapping(w http.ResponseWriter, r *http.Request, key string) {
	svc, err := s.projectMappings(r, "ci:write", key)
	var in ci.MappingInput
	if err == nil {
		in, err = s.decodeMapping(w, r)
	}
	if err == nil {
		err = s.refuseUpperCaseInline(in, key)
	}
	var res ci.MappingResult
	if err == nil {
		res, err = svc.CreateIn(r.Context(), caller(r), key, in)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	w.Header().Set("Location", projectMappingHref(r, key, res.Mapping.Issuer, res.Mapping.Name))
	SetETag(w, res.Mapping.Version, "")
	writeJSON(w, r, http.StatusCreated, mappingOut(res.Mapping, res.Warnings, true))
}

// GetProjectCiMapping implements GET /api/v1/projects/{key}/ci/mappings/{issuer}/{name}.
func (s *Server) GetProjectCiMapping(w http.ResponseWriter, r *http.Request, key, issuer, name string) {
	svc, err := s.projectMappings(r, "ci:read", key)
	var m ci.Mapping
	if err == nil {
		m, err = svc.GetIn(r.Context(), key, issuer, name)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	if NotModified(w, r, ETag(m.Version, "")) {
		return
	}
	SetETag(w, m.Version, "")
	writeJSON(w, r, http.StatusOK, mappingOut(m, nil, false))
}

// ReplaceProjectCiMapping implements PUT /api/v1/projects/{key}/ci/mappings/{issuer}/{name}.
func (s *Server) ReplaceProjectCiMapping(w http.ResponseWriter, r *http.Request, key, issuer, name string, _ gen.ReplaceProjectCiMappingParams) {
	svc, err := s.projectMappings(r, "ci:write", key)
	var pre Precondition
	if err == nil {
		pre, err = ParseIfMatch(r)
	}
	if err == nil {
		err = pre.Require()
	}
	var in ci.MappingInput
	if err == nil {
		in, err = s.decodeMapping(w, r)
	}
	if err == nil {
		err = s.refuseUpperCaseInline(in, key)
	}
	var res ci.MappingResult
	if err == nil {
		res, err = svc.ReplaceIn(r.Context(), caller(r), key, issuer, name, expectedRevision(pre), in)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	SetETag(w, res.Mapping.Version, "")
	writeJSON(w, r, http.StatusOK, mappingOut(res.Mapping, res.Warnings, true))
}

// DeleteProjectCiMapping implements DELETE /api/v1/projects/{key}/ci/mappings/{issuer}/{name}.
func (s *Server) DeleteProjectCiMapping(w http.ResponseWriter, r *http.Request, key, issuer, name string, _ gen.DeleteProjectCiMappingParams) {
	svc, err := s.projectMappings(r, "ci:write", key)
	var pre Precondition
	if err == nil {
		pre, err = ParseIfMatch(r)
	}
	if err == nil {
		err = svc.DeleteIn(r.Context(), caller(r), key, issuer, name, expectedRevision(pre))
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// EvaluateProjectCiMappings implements POST /api/v1/projects/{key}/ci/issuers/{issuer}/evaluate: the dry run
// over the project's tier only.
func (s *Server) EvaluateProjectCiMappings(w http.ResponseWriter, r *http.Request, key, issuer string) {
	svc, err := s.projectMappings(r, "ci:read", key)
	var req ci.EvaluateRequest
	if err == nil {
		req, err = s.decodeEvaluate(w, r)
	}
	var res ci.EvaluateResult
	if err == nil {
		res, err = svc.EvaluateIn(r.Context(), key, issuer, req)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, evaluateOut(res))
}

// GetProjectCiSnippet implements GET /api/v1/projects/{key}/ci/issuers/{issuer}/snippets/{kind}: the snippet
// of a mapping of the project's tier; a mapping of another tier is 404 (GetIn).
func (s *Server) GetProjectCiSnippet(w http.ResponseWriter, r *http.Request, key, issuer, kind string, params gen.GetProjectCiSnippetParams) {
	svc, err := s.projectMappings(r, "ci:read", key)
	if err == nil && params.Mapping != "" {
		_, err = svc.GetIn(r.Context(), key, issuer, params.Mapping)
	}
	var text string
	if err == nil {
		var format *string
		if params.Format != nil {
			f := string(*params.Format)
			format = &f
		}
		req := snippetRequest(kind, params.Mapping, format)
		req.Project = key
		text, err = svc.Snippet(r.Context(), issuer, req)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	writeSnippet(w, text)
}

// ListProjectCiIssuances implements GET /api/v1/projects/{key}/ci/issuances (Q-CI-26, built by W5): the records of
// tokens whose contributing mappings include a mapping of the project's tier, newest first, each showing the
// project's mappings only. A project the caller does not administer is 404 before anything is read.
func (s *Server) ListProjectCiIssuances(w http.ResponseWriter, r *http.Request, key string, _ gen.ListProjectCiIssuancesParams) {
	svc, err := s.ciIssuanceService()
	var lq ListQuery
	var f ci.IssuanceFilter
	if err == nil {
		lq, f, err = s.issuanceQuery(r, ListSpec{Filters: []string{"issuer", "jobId", "active"}})
	}
	var page ci.IssuancePage
	if err == nil {
		page, err = svc.ListForProject(r.Context(), key, f)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	WritePage(w, r, MapPage(NewPage(lq, page.Items, ci.IssuanceSortKey), issuanceBody))
}
