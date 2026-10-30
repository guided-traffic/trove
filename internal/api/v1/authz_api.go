package v1

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/meta"
)

// The permission operations of R-AUTHZ-021 to R-AUTHZ-023: policies, role
// bindings and the checks of the caller. The handlers are thin: the rules
// (validation, version checks, the audit event, the change-log note) are in
// internal/authz, the conventions (paging, ETag, merge patch, idempotency) in
// this package.

// AuthzServices are the domain services behind the permission operations.
type AuthzServices struct {
	// Policies is the policy and role-binding service.
	Policies *authz.Service
	// Engine answers the checks of the caller.
	Engine *authz.Engine
}

func (s *Server) policies() (*authz.Service, error) {
	if s.o.Authz.Policies == nil {
		return nil, unavailable("The permission service")
	}
	return s.o.Authz.Policies, nil
}

// caller is the subject of the request, for the actor of an audit event.
func caller(r *http.Request) authz.Subject { return SubjectFrom(r.Context()) }

// setEpoch puts the epoch a write produced in Trove-Authz-Epoch (R-AUTHZ-032).
// authz.HeaderEpoch is the one name of the header and this the one setter: a write
// answers it through here, and an idempotent create through [finishEpoch].
func setEpoch(w http.ResponseWriter, epoch int64) {
	w.Header().Set(authz.HeaderEpoch, strconv.FormatInt(epoch, 10))
}

// finishEpoch is the Outcome.Finish of an idempotent create that changes
// authorization data. The change log raises the epoch in a hook just before
// COMMIT, after the operation returned its outcome; Finish runs after that hook
// and before the outcome is stored, so the stored outcome, and with it every
// replay, carries the epoch of the first answer (R-AUTHZ-032, Q-API-28). The
// write stores its epoch through *epoch.
func finishEpoch(epoch *int64) func(context.Context, meta.Tx, *Outcome) error {
	return func(_ context.Context, _ meta.Tx, out *Outcome) error {
		if out.Header == nil {
			out.Header = map[string]string{}
		}
		out.Header[authz.HeaderEpoch] = strconv.FormatInt(*epoch, 10)
		return nil
	}
}

func (s *Server) bodyLimit() int64 { return int64(s.o.API.Config.MaxRequestBodyBytes) }

func policyHref(base, name string) string {
	return base + "/api/v1/permission-policies/" + name
}

// ListPermissionPolicies implements GET /api/v1/permission-policies
// (R-AUTHZ-021). The page is built from the policies the filter keeps, in name
// order; the cursor holds the name of the last one.
func (s *Server) ListPermissionPolicies(w http.ResponseWriter, r *http.Request, _ gen.ListPermissionPoliciesParams) {
	svc, err := s.policies()
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	lq, err := ParseList(r, ListSpec{
		Filters: []string{"principal", "expandGroups", "repository"}, Sorts: []string{"name"}, DefaultSort: "name",
	}, s.o.API.Config)
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
	all, err := svc.List(r.Context())
	if err == nil {
		all, err = svc.Select(r.Context(), all, filter)
	}
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "authz.listPolicies"))
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
		httpserver.Problem(w, r, errs.Wrap(err, "authz.listPolicies"))
		return
	}
	WritePage(w, r, page)
}

// policyFilter reads the filters of the list: expandGroups is a boolean that
// defaults to true and only means something with a principal.
func policyFilter(lq ListQuery) (authz.ListFilter, error) {
	f := authz.ListFilter{Principal: lq.First("principal"), Repository: lq.First("repository"), ExpandGroups: true}
	switch v := lq.First("expandGroups"); v {
	case "":
	case "true":
	case "false":
		f.ExpandGroups = false
	default:
		return f, errs.E(errs.Invalid, errs.CodeInvalid, "The query parameter \"expandGroups\" must be true or false.")
	}
	return f, nil
}

// CreatePermissionPolicy implements POST /api/v1/permission-policies
// (R-AUTHZ-021). The body may be a representation obtained by GET (the
// read-only members are ignored), so the request type is the response type.
func (s *Server) CreatePermissionPolicy(w http.ResponseWriter, r *http.Request, _ gen.CreatePermissionPolicyParams) {
	svc, err := s.policies()
	if err == nil && s.idem == nil {
		err = unavailable("The idempotency store")
	}
	var body []byte
	var in authz.Policy
	if err == nil {
		body, err = ReadBody(w, r, MediaJSON, s.bodyLimit())
	}
	if err == nil {
		err = StrictUnmarshal(body, &in)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	actor := caller(r)
	base := httpserver.InfoFrom(r.Context()).BaseURL
	var epoch int64
	err = s.idem.Do(w, r, body, IdempotentOp{Run: func(ctx context.Context, tx meta.Tx) (Outcome, error) {
		p, err := svc.CreateIn(ctx, tx, actor, in.PolicyInput, &epoch)
		if err != nil {
			return Outcome{}, err
		}
		return Outcome{
			Status: http.StatusCreated, Body: p,
			Header: map[string]string{"Location": policyHref(base, p.Name), "ETag": ETag(p.Version, "")},
			Finish: finishEpoch(&epoch),
		}, nil
	}})
	if err != nil {
		httpserver.Problem(w, r, err)
	}
}

// GetPermissionPolicy implements GET /api/v1/permission-policies/{name}.
func (s *Server) GetPermissionPolicy(w http.ResponseWriter, r *http.Request, name string) {
	svc, err := s.policies()
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	p, err := svc.Get(r.Context(), name)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	tag := ETag(p.Version, "")
	if NotModified(w, r, tag) {
		return
	}
	w.Header().Set("ETag", tag)
	writeJSON(w, r, http.StatusOK, p)
}

// ReplacePermissionPolicy implements PUT /api/v1/permission-policies/{name}.
func (s *Server) ReplacePermissionPolicy(w http.ResponseWriter, r *http.Request, name string, _ gen.ReplacePermissionPolicyParams) {
	svc, err := s.policies()
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	pre, err := ParseIfMatch(r)
	var in authz.Policy
	if err == nil {
		err = DecodeJSON(w, r, &in, s.bodyLimit())
	}
	if err == nil && in.Name == "" {
		in.Name = name
	}
	if err == nil {
		err = CheckImmutable(authz.Policy{PolicyInput: authz.PolicyInput{Name: name}}, in, "name")
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	expected := errs.AnyRevision
	if rev, ok := pre.Revision(""); ok {
		expected = rev
	}
	p, epoch, err := svc.Update(r.Context(), caller(r), name, expected, in.PolicyInput)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	writePolicyWrite(w, r, p, epoch)
}

// PatchPermissionPolicy implements PATCH /api/v1/permission-policies/{name}: a
// merge patch (R-API-013) on the current representation, written like a
// replacement. The patch is computed on one version and written conditional on
// exactly that version, also for If-Match: *, so a concurrent change is a 412
// and never a silently merged-over one.
func (s *Server) PatchPermissionPolicy(w http.ResponseWriter, r *http.Request, name string, _ gen.PatchPermissionPolicyParams) {
	svc, err := s.policies()
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	pre, err := ParseIfMatch(r)
	var raw []byte
	if err == nil {
		raw, err = ReadMergePatch(w, r, s.bodyLimit())
	}
	var cur authz.Policy
	if err == nil {
		cur, err = svc.Get(r.Context(), name)
	}
	if err == nil && !pre.Matches(cur.Version, "") {
		err = Failed(cur.Version)
	}
	var next authz.Policy
	if err == nil {
		next, err = ApplyMergePatch(cur, raw)
	}
	if err == nil {
		err = CheckImmutable(cur, next, "name")
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	p, epoch, err := svc.Update(r.Context(), caller(r), name, cur.Version, next.PolicyInput)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	writePolicyWrite(w, r, p, epoch)
}

func writePolicyWrite(w http.ResponseWriter, r *http.Request, p authz.Policy, epoch int64) {
	w.Header().Set("ETag", ETag(p.Version, ""))
	setEpoch(w, epoch)
	writeJSON(w, r, http.StatusOK, p)
}

// DeletePermissionPolicy implements DELETE /api/v1/permission-policies/{name};
// If-Match is optional.
func (s *Server) DeletePermissionPolicy(w http.ResponseWriter, r *http.Request, name string, _ gen.DeletePermissionPolicyParams) {
	svc, err := s.policies()
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	pre, err := ParseIfMatch(r)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	expected := errs.AnyRevision
	if rev, ok := pre.Revision(""); ok {
		expected = rev
	}
	epoch, err := svc.Delete(r.Context(), caller(r), name, expected)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	setEpoch(w, epoch)
	w.WriteHeader(http.StatusNoContent)
}

type policyValidation struct {
	Valid    bool            `json:"valid"`
	Errors   []policyProblem `json:"errors"`
	Warnings []string        `json:"warnings"`
	Covers   []string        `json:"covers"`
}

type policyProblem struct {
	Pointer string `json:"pointer"`
	Code    string `json:"code"`
	Detail  string `json:"detail"`
}

// ValidatePermissionPolicy implements POST /api/v1/authz/validate-policy.
func (s *Server) ValidatePermissionPolicy(w http.ResponseWriter, r *http.Request) {
	svc, err := s.policies()
	var in authz.Policy
	if err == nil {
		err = DecodeJSON(w, r, &in, s.bodyLimit())
	}
	var rep authz.Report
	if err == nil {
		rep, err = svc.Validate(r.Context(), in.PolicyInput)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	out := policyValidation{Valid: len(rep.Errors) == 0, Errors: []policyProblem{}, Warnings: nonNil(rep.Warnings), Covers: nonNil(rep.Covers)}
	for _, p := range rep.Errors {
		out.Errors = append(out.Errors, policyProblem{Pointer: p.Pointer, Code: p.Code, Detail: p.Detail})
	}
	writeJSON(w, r, http.StatusOK, out)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

type roleView struct {
	Name         string   `json:"name"`
	Capabilities []string `json:"capabilities"`
}

// ListRoles implements GET /api/v1/roles: the fixed set of R-AUTHZ-010.
func (s *Server) ListRoles(w http.ResponseWriter, r *http.Request) {
	items := make([]roleView, 0, len(authz.Roles()))
	for _, role := range authz.Roles() {
		caps := make([]string, 0)
		for _, c := range authz.RoleCapabilities(role) {
			caps = append(caps, string(c))
		}
		items = append(items, roleView{Name: string(role), Capabilities: caps})
	}
	writeJSON(w, r, http.StatusOK, Page[roleView]{Items: items})
}

type bindingView struct {
	Role      string    `json:"role"`
	Principal string    `json:"principal"`
	GrantedAt time.Time `json:"grantedAt"`
	GrantedBy string    `json:"grantedBy"`
}

func toBinding(b authz.Binding) bindingView {
	return bindingView{Role: string(b.Role), Principal: b.Principal, GrantedAt: b.GrantedAt.UTC(), GrantedBy: b.GrantedBy}
}

// ListRoleBindings implements GET /api/v1/role-bindings: ordered by role and
// principal, the cursor holds both of the last item.
func (s *Server) ListRoleBindings(w http.ResponseWriter, r *http.Request, _ gen.ListRoleBindingsParams) {
	svc, err := s.policies()
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	lq, err := ParseList(r, ListSpec{Filters: []string{"role", "principal"}}, s.o.API.Config)
	if err == nil && lq.After != nil && len(lq.After) != 2 {
		err = invalidCursor()
	}
	var all []authz.Binding
	if err == nil {
		all, err = svc.ListBindings(r.Context(), authz.Role(lq.First("role")), lq.First("principal"))
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Role != all[j].Role {
			return all[i].Role < all[j].Role
		}
		return all[i].Principal < all[j].Principal
	})
	rows := make([]bindingView, 0, lq.FetchLimit())
	for _, b := range all {
		if lq.After != nil && (string(b.Role) < lq.After[0] || string(b.Role) == lq.After[0] && b.Principal <= lq.After[1]) {
			continue
		}
		if rows = append(rows, toBinding(b)); len(rows) == lq.FetchLimit() {
			break
		}
	}
	WritePage(w, r, NewPage(lq, rows, func(b bindingView) []string { return []string{b.Role, b.Principal} }))
}

// BindRole implements PUT /api/v1/role-bindings/{role}/{principalRef}.
func (s *Server) BindRole(w http.ResponseWriter, r *http.Request, role gen.RoleName, principalRef string) {
	svc, err := s.policies()
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	created, epoch, err := svc.BindRole(r.Context(), caller(r), authz.Role(role), principalRef)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	setEpoch(w, epoch)
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	view := bindingView{Role: string(role), Principal: principalRef, GrantedAt: time.Now().UTC()}
	if got, err := svc.ListBindings(r.Context(), authz.Role(role), principalRef); err == nil && len(got) > 0 {
		view = toBinding(got[0])
	}
	writeJSON(w, r, status, view)
}

// UnbindRole implements DELETE /api/v1/role-bindings/{role}/{principalRef}.
func (s *Server) UnbindRole(w http.ResponseWriter, r *http.Request, role gen.RoleName, principalRef string) {
	svc, err := s.policies()
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	epoch, err := svc.UnbindRole(r.Context(), caller(r), authz.Role(role), principalRef)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	setEpoch(w, epoch)
	w.WriteHeader(http.StatusNoContent)
}

type scopeSummary struct {
	Inherit bool     `json:"inherit"`
	Admin   bool     `json:"admin"`
	System  []string `json:"system"`
	Grants  int      `json:"grants"`
}

type authzMe struct {
	Roles        []string      `json:"roles"`
	Capabilities []string      `json:"capabilities"`
	Principals   []string      `json:"principals"`
	Scope        *scopeSummary `json:"scope"`
	Epoch        int64         `json:"epoch"`
}

// GetAuthzMe implements GET /api/v1/authz/me (R-AUTHZ-022).
func (s *Server) GetAuthzMe(w http.ResponseWriter, r *http.Request) {
	if s.o.Authz.Engine == nil {
		httpserver.Problem(w, r, unavailable("The permission service"))
		return
	}
	sub := caller(r)
	me, err := s.o.Authz.Engine.Effective(r.Context(), sub)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	out := authzMe{Roles: []string{}, Capabilities: []string{}, Principals: nonNil(me.Principals), Epoch: me.Epoch}
	for _, role := range me.Roles {
		out.Roles = append(out.Roles, string(role))
	}
	for _, c := range me.Capabilities {
		out.Capabilities = append(out.Capabilities, string(c))
	}
	if sc := sub.Scope; sc != nil {
		out.Scope = &scopeSummary{Inherit: sc.Inherit, Admin: sc.Admin, System: nonNil(sc.System), Grants: len(sc.Repositories)}
	}
	writeJSON(w, r, http.StatusOK, out)
}

type checkItem struct {
	Repository string `json:"repository"`
	Package    string `json:"package"`
	Path       string `json:"path"`
	Action     string `json:"action"`
}

type checkRequest struct {
	Checks []checkItem `json:"checks"`
}

type checkResult struct {
	Allowed bool `json:"allowed"`
}

type checkResponse struct {
	Results []checkResult `json:"results"`
	Epoch   int64         `json:"epoch"`
}

// CheckPermissions implements POST /api/v1/authz/check (R-AUTHZ-023): the
// caller's own credential, up to 100 checks, one index.
func (s *Server) CheckPermissions(w http.ResponseWriter, r *http.Request) {
	if s.o.Authz.Engine == nil {
		httpserver.Problem(w, r, unavailable("The permission service"))
		return
	}
	var in checkRequest
	if err := DecodeJSON(w, r, &in, s.bodyLimit()); err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	items := make([]authz.CheckItem, len(in.Checks))
	for i, c := range in.Checks {
		items[i] = authz.CheckItem{Repository: c.Repository, Package: c.Package, Path: c.Path, Action: authz.Action(c.Action)}
	}
	got, epoch, err := s.o.Authz.Engine.Check(r.Context(), caller(r), items)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	out := checkResponse{Results: make([]checkResult, len(got)), Epoch: epoch}
	for i, ok := range got {
		out.Results[i] = checkResult{Allowed: ok}
	}
	writeJSON(w, r, http.StatusOK, out)
}
