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
// (delete order, members and roles, move, the audit event, the change-log note, the
// epoch) are in internal/project, the conventions (paging, ETag, idempotency, the
// header Trove-Authz-Epoch) in this package.
//
// What each handler adds to its declaration (api/openapi.yaml, docs/developer/projects.md):
//
//   - listProjects: the declaration admits a member of any project (projectRole: reader);
//     a caller without projects:read sees the projects it is a member of.
//   - the member operations are the project service's (last Owner, group and user only, the audit event);
//     the handler maps the body and the status (201 added, 200 role set).
//   - the writes: the declaration is the capability or an Owner of the project. They answer
//     Trove-Authz-Epoch; createProject is idempotent, replaceProject takes If-Match.

// projects returns the project service, or the 503 of a replica without it.
func (s *Server) projects() (project.Service, error) {
	if s.o.Authz.Projects == nil {
		return nil, unavailable("The project service")
	}
	return s.o.Authz.Projects, nil
}

// toProject renders a project; role is the caller's own role in it, "" for a caller that is no member.
func toProject(p project.Project, role authz.ProjectRole) gen.Project {
	owners := make([]string, 0, len(p.Members))
	for _, m := range p.Members {
		if m.Role == authz.ProjectRoleOwner {
			owners = append(owners, m.Principal.String())
		}
	}
	out := gen.Project{
		Key: p.Key, DisplayName: p.DisplayName, Description: p.Description,
		Members: len(p.Members), Owners: owners, Repositories: p.Repositories, ServiceAccounts: p.ServiceAccounts, Policies: p.Policies,
		Version:   p.Version,
		CreatedAt: utc(p.CreatedAt), CreatedBy: p.CreatedBy, UpdatedAt: utc(p.UpdatedAt), UpdatedBy: p.UpdatedBy,
	}
	if role.Valid() {
		r := gen.ProjectRole(role.String())
		out.Role = &r
	}
	return out
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
	var roles map[string]authz.ProjectRole // the caller's memberships with its role
	var all bool                           // the caller holds projects:read and sees every project
	if err == nil {
		roles, all, err = s.memberProjects(r.Context(), "projects:read")
	}
	var list []project.Project
	if err == nil {
		list, err = svc.List(r.Context())
	}
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "projects.list"))
		return
	}
	rows := make([]project.Project, 0, lq.FetchLimit())
	for i := range list {
		p := list[i]
		if lq.Desc {
			p = list[len(list)-1-i]
		}
		if _, ok := roles[p.Key]; (!all && !ok) || !projectMatches(p, lq.First("q")) {
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
	WritePage(w, r, MapPage(page, func(p project.Project) gen.Project { return toProject(p, roles[p.Key]) }))
}

// memberProjects returns the caller's memberships with its role, and whether the caller holds the
// capability and so sees every project.
func (s *Server) memberProjects(ctx context.Context, capability string) (roles map[string]authz.ProjectRole, all bool, err error) {
	if all, err = s.holds(ctx, capability); err != nil {
		return nil, false, err
	}
	roles = map[string]authz.ProjectRole{}
	if pa, ok := s.o.Access.(ProjectAccess); ok {
		members, err := pa.Memberships(ctx)
		if err != nil {
			return nil, false, err
		}
		for _, m := range members {
			roles[m.Key] = m.Role
		}
	}
	return roles, all, nil
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

// CreateProject implements POST /api/v1/projects.
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
	}
	actor := caller(r)
	var epoch int64
	err = s.idem.Do(w, r, body, IdempotentOp{Run: func(ctx context.Context, tx meta.Tx) (Outcome, error) {
		p, err := svc.CreateIn(ctx, tx, actor, input, &epoch)
		if err != nil {
			return Outcome{}, err
		}
		return Outcome{
			Status: http.StatusCreated, Body: toProject(p, authz.ProjectRoleNone),
			Header: map[string]string{"Location": projectHref(r, p.Key), "ETag": ETag(p.Version, "")},
			Finish: finishEpoch(&epoch),
		}, nil
	}})
	if err != nil {
		httpserver.Problem(w, r, err)
	}
}

// GetProject implements GET /api/v1/projects/{projectKey}. The declaration admits the holder of
// projects:read and a member of the project. The ETag is the version of the PUT fields; members and counts
// are in the body and do not move it (Q-AUTHZ-185), so If-None-Match is not honoured: a 304 would hide a
// member added after the read (Q-AUTHZ-202).
func (s *Server) GetProject(w http.ResponseWriter, r *http.Request, key string) {
	svc, err := s.projects()
	var p project.Project
	if err == nil {
		p, err = svc.Get(r.Context(), key)
	}
	var roles map[string]authz.ProjectRole
	if err == nil {
		roles, _, err = s.memberProjects(r.Context(), "projects:read")
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	SetETag(w, p.Version, "")
	writeJSON(w, r, http.StatusOK, toProject(p, roles[key]))
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
		if len(missing) > 0 {
			err = errs.E(errs.Invalid, errs.CodeValidation, "A replacement names displayName.", errs.WithFields(missing...))
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
	})
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	SetETag(w, p.Version, "")
	setEpoch(w, epoch)
	writeJSON(w, r, http.StatusOK, toProject(p, authz.ProjectRoleNone))
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

// ListProjectMembers implements GET /api/v1/projects/{projectKey}/members.
func (s *Server) ListProjectMembers(w http.ResponseWriter, r *http.Request, projectKey string) {
	svc, err := s.projects()
	var members []project.Member
	if err == nil {
		members, err = svc.Members(r.Context(), projectKey)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	out := gen.MemberList{Items: make([]gen.Member, 0, len(members))}
	for _, m := range members {
		out.Items = append(out.Items, toProjectMember(m))
	}
	writeJSON(w, r, http.StatusOK, out)
}

func toProjectMember(m project.Member) gen.Member {
	return gen.Member{
		Kind: gen.MemberKind(m.Principal.Kind), Name: m.Principal.Name, Principal: m.Principal.String(),
		Project: m.Project, Role: gen.ProjectRole(m.Role.String()),
	}
}

// SetProjectMember implements PUT /api/v1/projects/{projectKey}/members/{principalRef}: 201 when the
// principal became a member, 200 when its role was set.
func (s *Server) SetProjectMember(w http.ResponseWriter, r *http.Request, projectKey, principalRef string) {
	svc, err := s.projects()
	var in gen.MemberInput
	if err == nil {
		err = DecodeJSON(w, r, &in, s.bodyLimit())
	}
	var role authz.ProjectRole
	if err == nil {
		var ok bool
		if role, ok = authz.ParseProjectRole(string(in.Role)); !ok {
			err = errs.E(errs.Invalid, errs.CodeValidation, "The role is reader, developer, maintainer or owner.",
				errs.WithFields(errs.FieldError{Pointer: "/role", Code: "invalid_role", Detail: "not one of reader, developer, maintainer, owner"}))
		}
	}
	var member project.Member
	var created bool
	var epoch int64
	if err == nil {
		member, created, epoch, err = svc.AddMember(r.Context(), caller(r), projectKey, principalRef, role, s.mayBypassLastOwner(r))
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	setEpoch(w, epoch)
	writeJSON(w, r, status, toProjectMember(member))
}

// RemoveProjectMember implements DELETE /api/v1/projects/{projectKey}/members/{principalRef}.
func (s *Server) RemoveProjectMember(w http.ResponseWriter, r *http.Request, projectKey, principalRef string) {
	svc, err := s.projects()
	var epoch int64
	if err == nil {
		epoch, err = svc.RemoveMember(r.Context(), caller(r), projectKey, principalRef, s.mayBypassLastOwner(r))
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	setEpoch(w, epoch)
	w.WriteHeader(http.StatusNoContent)
}

// mayBypassLastOwner reports whether the caller holds projects:write, which may remove or demote the last
// Owner of a project (D6 of Q-AUTHZ-208).
func (s *Server) mayBypassLastOwner(r *http.Request) bool {
	held, err := s.holds(r.Context(), "projects:write")
	return err == nil && held
}

// maxLookupQuery is the longest prefix the lookup accepts (the document's maxLength).
const maxLookupQuery = 128

// LookupProjectPrincipals implements GET /api/v1/projects/{key}/principals
// (Q-AUTHZ-182): a caller with identities:read, or an Owner of the project. The project must exist (404). The answer is kind, name and
// the reference to write in a policy entry.
func (s *Server) LookupProjectPrincipals(w http.ResponseWriter, r *http.Request, key string, params gen.LookupProjectPrincipalsParams) {
	svc, err := s.projects()
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
