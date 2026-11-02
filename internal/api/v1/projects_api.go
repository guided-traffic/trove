package v1

import (
	"context"
	"net/http"
	"strings"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/project"
)

// The project operations (R-AUTHZ-026, R-API-047). The handlers are thin: the rules
// (key overlap, delete order, assignment, the audit event, the change-log note, the
// epoch) are in internal/project, the conventions (paging, ETag, idempotency, the
// header Trove-Authz-Epoch) in this package.
//
// What each handler adds to its declaration (api/openapi.yaml, docs/developer/projects.md):
//
//   - listProjects, getProject: the declaration admits a project administrator
//     (projectAdmin: any); delegatedScope narrows: a caller without projects:read
//     sees the projects it administers, and any other project answers 404.
//   - lookupProjectPrincipals: requireProject(…, ProjectManagePolicies).
//   - the writes: no narrowing, the declaration is the capability. They answer
//     Trove-Authz-Epoch; createProject is idempotent, replaceProject takes If-Match.

// projects returns the project service, or the 503 of a replica without it.
func (s *Server) projects() (project.Service, error) {
	if s.o.Authz.Projects == nil {
		return nil, unavailable("The project service")
	}
	return s.o.Authz.Projects, nil
}

func toProject(p project.Project) gen.Project {
	admins := make([]string, 0, len(p.Admins))
	for _, a := range p.Admins {
		admins = append(admins, a.String())
	}
	return gen.Project{
		Key: p.Key, DisplayName: p.DisplayName, Description: p.Description,
		ManagePolicies: p.ManagePolicies, ManageRepositories: p.ManageRepositories,
		Admins: admins, Repositories: p.Repositories, ServiceAccounts: p.ServiceAccounts, Policies: p.Policies,
		Version:   p.Version,
		CreatedAt: utc(p.CreatedAt), CreatedBy: p.CreatedBy, UpdatedAt: utc(p.UpdatedAt), UpdatedBy: p.UpdatedBy,
	}
}

func projectHref(r *http.Request, key string) string { return location(r, "/api/v1/projects/"+key) }

// projectMatches is the filter q of the list: a case-insensitive substring of the
// key or the display name.
func projectMatches(p project.Project, q string) bool {
	if q == "" {
		return true
	}
	q = strings.ToLower(q)
	return strings.Contains(p.Key, q) || strings.Contains(strings.ToLower(p.DisplayName), q)
}

// ListProjects implements GET /api/v1/projects (R-AUTHZ-026). The page is built from
// the projects the caller may see, by key; the cursor holds the last key.
func (s *Server) ListProjects(w http.ResponseWriter, r *http.Request, _ gen.ListProjectsParams) {
	svc, err := s.projects()
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	lq, err := ParseList(r, ListSpec{Filters: []string{"q"}, Sorts: []string{"key"}, DefaultSort: "key"}, s.o.API.Config)
	if err == nil && lq.After != nil && len(lq.After) != 1 {
		err = invalidCursor()
	}
	var scope DelegatedScope
	if err == nil {
		scope, err = s.delegatedScope(r.Context(), "projects:read")
	}
	var all []project.Project
	if err == nil {
		all, err = svc.List(r.Context())
	}
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "projects.list"))
		return
	}
	rows := make([]project.Project, 0, lq.FetchLimit())
	for i := range all {
		p := all[i]
		if lq.Desc {
			p = all[len(all)-1-i]
		}
		if !scope.Sees(p.Key) || !projectMatches(p, lq.First("q")) {
			continue
		}
		if lq.After != nil && (lq.Desc && p.Key >= lq.After[0] || !lq.Desc && p.Key <= lq.After[0]) {
			continue
		}
		if rows = append(rows, p); len(rows) == lq.FetchLimit() {
			break
		}
	}
	page := NewPage(lq, rows, func(p project.Project) []string { return []string{p.Key} })
	WritePage(w, r, MapPage(page, toProject))
}

// projectBody is what the create and the replace read: the members a client writes,
// with the read-only ones of a representation obtained by GET ignored.
type projectBody = gen.ProjectInput

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func required(pointer string) errs.FieldError {
	return errs.FieldError{Pointer: pointer, Code: "required", Detail: "The member is required."}
}

// CreateProject implements POST /api/v1/projects. managePolicies defaults to true
// and manageRepositories to false.
func (s *Server) CreateProject(w http.ResponseWriter, r *http.Request, _ gen.CreateProjectParams) {
	svc, err := s.projects()
	if err == nil && s.idem == nil {
		err = unavailable("The idempotency store")
	}
	var body []byte
	var in projectBody
	if err == nil {
		body, err = ReadBody(w, r, MediaJSON, s.bodyLimit())
	}
	if err == nil {
		err = StrictUnmarshal(body, &in)
	}
	if err == nil && in.Key == nil {
		err = errs.E(errs.Invalid, errs.CodeValidation, "The key is required.", errs.WithFields(required("/key")))
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	input := project.Input{
		Key: *in.Key, DisplayName: derefStr(in.DisplayName), Description: derefStr(in.Description),
		Rights: project.DefaultRights(),
	}
	if in.ManagePolicies != nil {
		input.ManagePolicies = *in.ManagePolicies
	}
	if in.ManageRepositories != nil {
		input.ManageRepositories = *in.ManageRepositories
	}
	actor := caller(r)
	var epoch int64
	err = s.idem.Do(w, r, body, IdempotentOp{Run: func(ctx context.Context, tx meta.Tx) (Outcome, error) {
		p, err := svc.CreateIn(ctx, tx, actor, input, &epoch)
		if err != nil {
			return Outcome{}, err
		}
		return Outcome{
			Status: http.StatusCreated, Body: toProject(p),
			Header: map[string]string{"Location": projectHref(r, p.Key), "ETag": ETag(p.Version, "")},
			Finish: finishEpoch(&epoch),
		}, nil
	}})
	if err != nil {
		httpserver.Problem(w, r, err)
	}
}

// GetProject implements GET /api/v1/projects/{key}. A caller that neither holds
// projects:read nor administers the project gets 404, also for one that does not
// exist.
func (s *Server) GetProject(w http.ResponseWriter, r *http.Request, key string, _ gen.GetProjectParams) {
	svc, err := s.projects()
	var scope DelegatedScope
	if err == nil {
		scope, err = s.delegatedScope(r.Context(), "projects:read")
	}
	if err == nil && !scope.Sees(key) {
		err = refuseProject(scope, key, authz.ProjectAny)
	}
	var p project.Project
	if err == nil {
		p, err = svc.Get(r.Context(), key)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	tag := ETag(p.Version, "")
	if NotModified(w, r, tag) {
		return
	}
	w.Header().Set("ETag", tag)
	writeJSON(w, r, http.StatusOK, toProject(p))
}

// ReplaceProject implements PUT /api/v1/projects/{key}. The binder answers 428 for a
// missing If-Match before this runs; the service answers 412 for a stale one.
func (s *Server) ReplaceProject(w http.ResponseWriter, r *http.Request, key string, _ gen.ReplaceProjectParams) {
	svc, err := s.projects()
	var pre Precondition
	if err == nil {
		pre, err = ParseIfMatch(r)
	}
	var in projectBody
	if err == nil {
		err = DecodeJSON(w, r, &in, s.bodyLimit())
	}
	var missing []errs.FieldError
	if err == nil {
		if in.DisplayName == nil {
			missing = append(missing, required("/displayName"))
		}
		if in.ManagePolicies == nil {
			missing = append(missing, required("/managePolicies"))
		}
		if in.ManageRepositories == nil {
			missing = append(missing, required("/manageRepositories"))
		}
		if len(missing) > 0 {
			err = errs.E(errs.Invalid, errs.CodeValidation, "A replacement names displayName, managePolicies and manageRepositories.", errs.WithFields(missing...))
		}
	}
	if err == nil && in.Key != nil {
		err = CheckImmutable(map[string]string{"key": key}, map[string]string{"key": *in.Key}, "key")
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	p, epoch, err := svc.Replace(r.Context(), caller(r), key, expectedRevision(pre), project.Input{
		Key: key, DisplayName: *in.DisplayName, Description: derefStr(in.Description),
		Rights: project.Rights{ManagePolicies: *in.ManagePolicies, ManageRepositories: *in.ManageRepositories},
	})
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	SetETag(w, p.Version, "")
	setEpoch(w, epoch)
	writeJSON(w, r, http.StatusOK, toProject(p))
}

// DeleteProject implements DELETE /api/v1/projects/{key}; If-Match is optional.
func (s *Server) DeleteProject(w http.ResponseWriter, r *http.Request, key string, _ gen.DeleteProjectParams) {
	svc, err := s.projects()
	var pre Precondition
	if err == nil {
		pre, err = ParseIfMatch(r)
	}
	var epoch int64
	if err == nil {
		epoch, err = svc.Delete(r.Context(), caller(r), key, expectedRevision(pre))
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	setEpoch(w, epoch)
	w.WriteHeader(http.StatusNoContent)
}

// AddProjectAdmin implements PUT /api/v1/projects/{key}/admins/{principalRef}: 201
// when the administrator was added, 200 when it already was one.
func (s *Server) AddProjectAdmin(w http.ResponseWriter, r *http.Request, key, principalRef string) {
	svc, err := s.projects()
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	admin, created, epoch, err := svc.AddAdmin(r.Context(), caller(r), key, principalRef)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	setEpoch(w, epoch)
	writeJSON(w, r, status, gen.ProjectAdmin{Project: key, Principal: admin.String()})
}

// RemoveProjectAdmin implements DELETE /api/v1/projects/{key}/admins/{principalRef}.
func (s *Server) RemoveProjectAdmin(w http.ResponseWriter, r *http.Request, key, principalRef string) {
	svc, err := s.projects()
	var epoch int64
	if err == nil {
		epoch, err = svc.RemoveAdmin(r.Context(), caller(r), key, principalRef)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	setEpoch(w, epoch)
	w.WriteHeader(http.StatusNoContent)
}

// AssignProjectRepository implements PUT /api/v1/projects/{key}/repositories/{repoKey}.
func (s *Server) AssignProjectRepository(w http.ResponseWriter, r *http.Request, key, repoKey string, params gen.AssignProjectRepositoryParams) {
	svc, err := s.projects()
	var epoch int64
	if err == nil {
		epoch, err = svc.Assign(r.Context(), caller(r), key, repoKey, params.Move != nil && *params.Move)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	setEpoch(w, epoch)
	w.WriteHeader(http.StatusNoContent)
}

// UnassignProjectRepository implements DELETE /api/v1/projects/{key}/repositories/{repoKey}.
func (s *Server) UnassignProjectRepository(w http.ResponseWriter, r *http.Request, key, repoKey string) {
	svc, err := s.projects()
	var epoch int64
	if err == nil {
		epoch, err = svc.Unassign(r.Context(), caller(r), key, repoKey)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	setEpoch(w, epoch)
	w.WriteHeader(http.StatusNoContent)
}

// maxLookupQuery is the longest prefix the lookup accepts (the document's maxLength).
const maxLookupQuery = 128

// LookupProjectPrincipals implements GET /api/v1/projects/{key}/principals
// (Q-AUTHZ-182): a caller with identities:read, or an administrator of the project
// with managePolicies. The project must exist (404). The answer is kind, name and
// the reference to write in a policy entry.
func (s *Server) LookupProjectPrincipals(w http.ResponseWriter, r *http.Request, key string, params gen.LookupProjectPrincipalsParams) {
	svc, err := s.projects()
	if err == nil {
		err = s.requireProject(r.Context(), "identities:read", key, authz.ProjectManagePolicies)
	}
	if err == nil {
		_, err = svc.Get(r.Context(), key)
	}
	var fields []errs.FieldError
	if err == nil && len(params.Q) > maxLookupQuery {
		fields = append(fields, errs.FieldError{Pointer: "/q", Code: "prefix_too_long", Detail: "The prefix has at most 128 characters."})
	}
	limit := 0
	if params.Limit != nil {
		if limit = *params.Limit; limit < 1 || limit > project.MaxLookup {
			fields = append(fields, errs.FieldError{Pointer: "/limit", Code: "invalid_limit", Detail: "The limit is 1 to 20."})
		}
	}
	if err == nil && len(fields) > 0 {
		err = errs.E(errs.Invalid, errs.CodeValidation, fields[0].Detail, errs.WithFields(fields...))
	}
	var found []project.PrincipalRef
	if err == nil {
		var kind project.PrincipalKind
		if params.Kind != nil {
			kind = project.PrincipalKind(*params.Kind)
		}
		found, err = svc.LookupPrincipals(r.Context(), params.Q, kind, limit)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	out := gen.ProjectPrincipalList{Items: make([]gen.ProjectPrincipal, 0, len(found))}
	for _, p := range found {
		out.Items = append(out.Items, gen.ProjectPrincipal{Kind: gen.ProjectPrincipalKind(p.Kind), Name: p.Name, Principal: p.String()})
	}
	writeJSON(w, r, http.StatusOK, out)
}
