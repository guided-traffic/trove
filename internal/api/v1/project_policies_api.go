package v1

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/meta"
)

// The permission policies of a project (Q-AUTHZ-209): /projects/{projectKey}/policies. A project's policy is
// named within its project in the API (name, project) and stored under "<projectKey>/<name>", which is the
// name the policy store addresses it by. storedPolicyName, storedPolicy and viewPolicy are the whole mapping
// between the two forms.

// storedPolicyName is the name the policy store knows a project's policy by.
func storedPolicyName(project, name string) string { return project + "/" + name }

// storedPolicy gives a policy the project of its route and the stored form of its name.
func storedPolicy(in authz.Policy, project string) authz.Policy {
	in.Project = &project
	in.Name = storedPolicyName(project, in.Name)
	return in
}

// viewPolicy is the policy as the API shows it: the name within its project.
func viewPolicy(p authz.Policy) authz.Policy {
	if p.Project != nil {
		p.Name = strings.TrimPrefix(p.Name, *p.Project+"/")
	}
	return p
}

// projectPolicyBody checks the project member of a body written under a project: it is absent or the
// project of the route (a policy never changes its project).
func projectPolicyBody(in authz.Policy, project string) error {
	if in.Project != nil && *in.Project != project {
		return errs.E(errs.Invalid, errs.CodeValidation, "The project of the body is not the project of the path.",
			errs.WithFields(errs.FieldError{Pointer: "/project", Code: "immutable", Detail: "the policy belongs to the project of the path"}))
	}
	return nil
}

// projectExists answers 404 for a project that does not exist.
func (s *Server) projectExists(ctx context.Context, key string) error {
	svc, err := s.projects()
	if err != nil {
		return err
	}
	_, err = svc.Get(ctx, key)
	return err
}

func projectPolicyHref(r *http.Request, project, name string) string {
	return location(r, "/api/v1/projects/"+project+"/policies/"+name)
}

// ListProjectPermissionPolicies implements GET /api/v1/projects/{projectKey}/policies. The page is built from the
// project's policies the filter keeps, in name order; the cursor holds the stored name of the last one.
func (s *Server) ListProjectPermissionPolicies(w http.ResponseWriter, r *http.Request, projectKey string, _ gen.ListProjectPermissionPoliciesParams) {
	svc, err := s.policies()
	var lq ListQuery
	if err == nil {
		err = s.projectExists(r.Context(), projectKey)
	}
	if err == nil {
		lq, err = ParseList(r, ListSpec{
			Filters: []string{"principal", "expandGroups", "repository"}, Sorts: []string{"name"}, DefaultSort: "name",
		}, s.o.API.Config)
	}
	var filter authz.ListFilter
	if err == nil {
		filter, err = policyFilter(lq)
	}
	if err == nil && lq.After != nil && len(lq.After) != 1 {
		err = invalidCursor()
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	filter.Project = projectKey
	if filter.Repository != "" {
		filter.Repository = projectKey + "/" + filter.Repository
	}
	all, err := svc.List(r.Context())
	if err == nil {
		all, err = svc.Select(r.Context(), all, filter)
	}
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "authz.listProjectPolicies"))
		return
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	if lq.Desc {
		sort.Slice(all, func(i, j int) bool { return all[i].Name > all[j].Name })
	}
	rows := make([]authz.Policy, 0, lq.FetchLimit())
	for _, p := range all {
		if lq.After != nil && (lq.Desc && p.Name >= lq.After[0] || !lq.Desc && p.Name <= lq.After[0]) {
			continue
		}
		if rows = append(rows, p); len(rows) == lq.FetchLimit() {
			break
		}
	}
	page := NewPage(lq, rows, func(p authz.Policy) []string { return []string{p.Name} })
	if err := svc.Annotate(r.Context(), page.Items); err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "authz.listProjectPolicies"))
		return
	}
	for i := range page.Items {
		page.Items[i] = viewPolicy(page.Items[i])
	}
	WritePage(w, r, page)
}

// CreateProjectPermissionPolicy implements POST /api/v1/projects/{projectKey}/policies.
func (s *Server) CreateProjectPermissionPolicy(w http.ResponseWriter, r *http.Request, projectKey string, _ gen.CreateProjectPermissionPolicyParams) {
	svc, err := s.policies()
	if err == nil && s.idem == nil {
		err = unavailable("The idempotency store")
	}
	if err == nil {
		err = s.projectExists(r.Context(), projectKey)
	}
	var body []byte
	var in authz.Policy
	if err == nil {
		body, err = ReadBody(w, r, MediaJSON, s.bodyLimit())
	}
	if err == nil {
		err = StrictUnmarshal(body, &in)
	}
	if err == nil {
		err = projectPolicyBody(in, projectKey)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	stored := storedPolicy(in, projectKey)
	actor := caller(r)
	var epoch int64
	err = s.idem.Do(w, r, body, IdempotentOp{Run: func(ctx context.Context, tx meta.Tx) (Outcome, error) {
		p, err := svc.CreateIn(ctx, tx, actor, stored.PolicyInput, &epoch)
		if err != nil {
			return Outcome{}, err
		}
		p = viewPolicy(p)
		return Outcome{
			Status: http.StatusCreated, Body: p,
			Header: map[string]string{"Location": projectPolicyHref(r, projectKey, p.Name), "ETag": ETag(p.Version, "")},
			Finish: finishEpoch(&epoch),
		}, nil
	}})
	if err != nil {
		httpserver.Problem(w, r, err)
	}
}

// GetProjectPermissionPolicy implements GET /api/v1/projects/{projectKey}/policies/{name}.
func (s *Server) GetProjectPermissionPolicy(w http.ResponseWriter, r *http.Request, projectKey, name string) {
	svc, err := s.policies()
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	p, err := svc.Get(r.Context(), storedPolicyName(projectKey, name))
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	tag := ETag(p.Version, "")
	if NotModified(w, r, tag) {
		return
	}
	w.Header().Set("ETag", tag)
	writeJSON(w, r, http.StatusOK, viewPolicy(p))
}

// ReplaceProjectPermissionPolicy implements PUT /api/v1/projects/{projectKey}/policies/{name}.
func (s *Server) ReplaceProjectPermissionPolicy(w http.ResponseWriter, r *http.Request, projectKey, name string, _ gen.ReplaceProjectPermissionPolicyParams) {
	svc, err := s.policies()
	var pre Precondition
	if err == nil {
		pre, err = ParseIfMatch(r)
	}
	var in authz.Policy
	if err == nil {
		err = DecodeJSON(w, r, &in, s.bodyLimit())
	}
	if err == nil && in.Name == "" {
		in.Name = name
	}
	if err == nil {
		err = CheckImmutable(authz.Policy{PolicyInput: authz.PolicyInput{Name: name}}, authz.Policy{PolicyInput: authz.PolicyInput{Name: in.Name}}, "name")
	}
	if err == nil {
		err = projectPolicyBody(in, projectKey)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	stored := storedPolicy(in, projectKey)
	p, epoch, err := svc.Update(r.Context(), caller(r), stored.Name, expectedRevision(pre), stored.PolicyInput)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	writePolicyWrite(w, r, viewPolicy(p), epoch)
}

// PatchProjectPermissionPolicy implements PATCH /api/v1/projects/{projectKey}/policies/{name}: a merge patch on the
// current representation, written conditional on exactly the version it was computed on, as for a policy of no
// project.
func (s *Server) PatchProjectPermissionPolicy(w http.ResponseWriter, r *http.Request, projectKey, name string, _ gen.PatchProjectPermissionPolicyParams) {
	svc, err := s.policies()
	var pre Precondition
	if err == nil {
		pre, err = ParseIfMatch(r)
	}
	var raw []byte
	if err == nil {
		raw, err = ReadMergePatch(w, r, s.bodyLimit())
	}
	var cur authz.Policy
	if err == nil {
		cur, err = svc.Get(r.Context(), storedPolicyName(projectKey, name))
	}
	if err == nil && !pre.Matches(cur.Version, "") {
		err = Failed(cur.Version)
	}
	view := viewPolicy(cur)
	var next authz.Policy
	if err == nil {
		next, err = ApplyMergePatch(view, raw)
	}
	if err == nil {
		err = CheckImmutable(view, next, "name", "project")
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	stored := storedPolicy(next, projectKey)
	p, epoch, err := svc.Update(r.Context(), caller(r), stored.Name, cur.Version, stored.PolicyInput)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	writePolicyWrite(w, r, viewPolicy(p), epoch)
}

// DeleteProjectPermissionPolicy implements DELETE /api/v1/projects/{projectKey}/policies/{name}; If-Match is
// optional.
func (s *Server) DeleteProjectPermissionPolicy(w http.ResponseWriter, r *http.Request, projectKey, name string, _ gen.DeleteProjectPermissionPolicyParams) {
	svc, err := s.policies()
	var pre Precondition
	if err == nil {
		pre, err = ParseIfMatch(r)
	}
	var epoch int64
	if err == nil {
		epoch, err = svc.Delete(r.Context(), caller(r), storedPolicyName(projectKey, name), expectedRevision(pre))
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	setEpoch(w, epoch)
	w.WriteHeader(http.StatusNoContent)
}
