package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/meta"
)

// The group operations of R-AUTH-018 and R-AUTH-019, and the service accounts
// of R-AUTH-020 (below).

func toGroup(g principal.Group) gen.Group {
	roles := g.Roles
	if roles == nil {
		roles = []string{}
	}
	return gen.Group{
		Id: uuidOf(g.ID), Name: g.Name, Description: g.Description, AutoJoin: g.AutoJoin, Origin: gen.GroupOrigin(g.Origin),
		MemberCount: g.MemberCount, Roles: roles, CreatedAt: utc(g.CreatedAt),
	}
}

// ListGroups implements GET /api/v1/groups (R-AUTH-018).
func (s *Server) ListGroups(w http.ResponseWriter, r *http.Request, _ gen.ListGroupsParams) {
	if !s.identityReady(w, r) {
		return
	}
	lq, err := ParseList(r, ListSpec{Filters: []string{"q", "origin"}, Sorts: []string{"name"}, DefaultSort: "name"}, s.o.API.Config)
	if err == nil {
		err = firstErr(ascending(lq), oneOf("origin", lq.First("origin"), "admin", "oidc", "scim"))
	}
	var after string
	if err == nil {
		after, err = afterName(lq)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	rows, err := s.o.Auth.Manager.ListGroups(r.Context(), principal.GroupFilter{Query: lq.First("q"), Origin: lq.First("origin"), After: after, Limit: lq.FetchLimit()})
	if err != nil {
		failIdentity(w, r, err)
		return
	}
	page := NewPage(lq, rows, func(g principal.Group) []string { return []string{g.Name} })
	WritePage(w, r, MapPage(page, toGroup))
}

// CreateGroup implements POST /api/v1/groups (R-AUTH-018).
func (s *Server) CreateGroup(w http.ResponseWriter, r *http.Request, _ gen.CreateGroupParams) {
	if !s.identityReady(w, r) {
		return
	}
	body, err := ReadBody(w, r, MediaJSON, s.bodyLimit())
	var in gen.NewGroup
	if err == nil {
		err = StrictUnmarshal(body, &in)
	}
	if err == nil && len(deref(in.Members)) > 1000 {
		err = errs.E(errs.Invalid, errs.CodeValidation, "A group is created with at most 1000 members; add more with a member change.",
			errs.WithFields(errs.FieldError{Pointer: "/members", Code: "too_many", Detail: "at most 1000"}))
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	c := s.caller(r)
	err = s.idem.Do(w, r, body, IdempotentOp{Run: func(ctx context.Context, tx meta.Tx) (Outcome, error) {
		g, err := s.o.Auth.Manager.CreateGroup(principal.Join(ctx, tx), c, principal.NewGroup{
			Name: in.Name, Description: derefString(in.Description), AutoJoin: in.AutoJoin != nil && *in.AutoJoin, Members: deref(in.Members),
		})
		if err != nil {
			return Outcome{}, identityErr(err)
		}
		return Outcome{Status: http.StatusCreated, Body: toGroup(g), Header: map[string]string{"Location": location(r, "/api/v1/groups/"+g.Name)}}, nil
	}})
	if err != nil {
		failIdentity(w, r, err)
	}
}

// GetGroup implements GET /api/v1/groups/{name} (R-AUTH-018).
func (s *Server) GetGroup(w http.ResponseWriter, r *http.Request, name string, _ gen.GetGroupParams) {
	if !s.identityReady(w, r) {
		return
	}
	g, err := s.o.Auth.Manager.GetGroup(r.Context(), name)
	if err != nil {
		failIdentity(w, r, err)
		return
	}
	if NotModified(w, r, ETag(g.Revision, "")) {
		return
	}
	SetETag(w, g.Revision, "")
	writeJSON(w, r, http.StatusOK, toGroup(g))
}

// groupPatchBody is a merge patch of a group; the members after AutoJoin are the
// read-only ones of the group record and are ignored.
type groupPatchBody struct {
	Name        json.RawMessage `json:"name"`
	Description json.RawMessage `json:"description"`
	AutoJoin    json.RawMessage `json:"autoJoin"`

	ID          json.RawMessage `json:"id"`
	Origin      json.RawMessage `json:"origin"`
	MemberCount json.RawMessage `json:"memberCount"`
	Roles       json.RawMessage `json:"roles"`
	CreatedAt   json.RawMessage `json:"createdAt"`
}

// UpdateGroup implements PATCH /api/v1/groups/{name} (R-AUTH-018, R-AUTH-019).
func (s *Server) UpdateGroup(w http.ResponseWriter, r *http.Request, name string, _ gen.UpdateGroupParams) {
	if !s.identityReady(w, r) {
		return
	}
	pre, err := ParseIfMatch(r)
	var raw []byte
	if err == nil {
		raw, err = ReadMergePatch(w, r, s.bodyLimit())
	}
	var in groupPatchBody
	if err == nil {
		err = StrictUnmarshal(raw, &in)
	}
	p := principal.GroupPatch{Expected: expectedRevision(pre)}
	if err == nil {
		err = errorsFirst(
			func() (e error) { p.Name, e = patchRequiredString(in.Name, "/name"); return },
			func() (e error) { p.Description, e = patchString(in.Description, "/description"); return },
			func() (e error) { p.AutoJoin, e = patchBool(in.AutoJoin, "/autoJoin"); return },
		)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	g, err := s.o.Auth.Manager.UpdateGroup(r.Context(), s.caller(r), name, p)
	if err != nil {
		failIdentity(w, r, err)
		return
	}
	SetETag(w, g.Revision, "")
	writeJSON(w, r, http.StatusOK, toGroup(g))
}

// DeleteGroup implements DELETE /api/v1/groups/{name} (R-AUTH-018).
func (s *Server) DeleteGroup(w http.ResponseWriter, r *http.Request, name string) {
	if !s.identityReady(w, r) {
		return
	}
	if err := s.o.Auth.Manager.DeleteGroup(r.Context(), s.caller(r), name); err != nil {
		failIdentity(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func toMember(m principal.MemberPage) gen.GroupMember {
	kind := gen.GroupMemberKind(m.Kind)
	return gen.GroupMember{Principal: m.Member, Kind: kind, Sources: orEmptyStrings(m.Sources)}
}

// ListGroupMembers implements GET /api/v1/groups/{name}/members (R-AUTH-018).
func (s *Server) ListGroupMembers(w http.ResponseWriter, r *http.Request, name string, _ gen.ListGroupMembersParams) {
	if !s.identityReady(w, r) {
		return
	}
	lq, err := ParseList(r, ListSpec{Filters: []string{"source"}}, s.o.API.Config)
	var afterKind, afterMember string
	if err == nil {
		switch len(lq.After) {
		case 0:
		case 2:
			afterKind, afterMember = lq.After[0], lq.After[1]
		default:
			err = invalidCursor()
		}
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	rows, err := s.o.Auth.Manager.ListMemberPage(r.Context(), name, lq.First("source"), afterKind, afterMember, lq.FetchLimit())
	if err != nil {
		failIdentity(w, r, err)
		return
	}
	// The cursor holds the kind and the bare name the query compares.
	page := NewPage(lq, rows, func(m principal.MemberPage) []string {
		return []string{string(m.Kind), strings.TrimPrefix(m.Member, "sa:")}
	})
	WritePage(w, r, MapPage(page, toMember))
}

// PatchGroupMembers implements PATCH /api/v1/groups/{name}/members (R-AUTH-018).
func (s *Server) PatchGroupMembers(w http.ResponseWriter, r *http.Request, name string) {
	if !s.identityReady(w, r) {
		return
	}
	var in gen.GroupMembersChange
	if err := DecodeJSON(w, r, &in, s.bodyLimit()); err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	source := "manual"
	if in.Source != nil {
		source = string(*in.Source)
	}
	res, err := s.o.Auth.Manager.PatchMembers(r.Context(), s.caller(r), name, source, deref(in.Add), deref(in.Remove))
	if err != nil {
		failIdentity(w, r, err)
		return
	}
	items := make([]gen.GroupMember, 0, len(res))
	seen := map[string]bool{}
	for _, m := range res {
		if seen[m.Member] {
			continue
		}
		seen[m.Member] = true
		items = append(items, gen.GroupMember{Principal: m.Member, Kind: gen.GroupMemberKind(m.Kind), Sources: orEmptyStrings(m.Sources)})
	}
	writeJSON(w, r, http.StatusOK, gen.GroupMemberList{Items: items})
}

// ---- service accounts -------------------------------------------------------------------------------------

func toServiceAccount(a principal.ServiceAccount) gen.ServiceAccount {
	return gen.ServiceAccount{Id: uuidOf(a.ID), Name: a.Name, Description: a.Description, Status: gen.ServiceAccountStatus(a.Status), CreatedAt: utc(a.CreatedAt)}
}

// ListServiceAccounts implements GET /api/v1/service-accounts (R-AUTH-020).
func (s *Server) ListServiceAccounts(w http.ResponseWriter, r *http.Request, _ gen.ListServiceAccountsParams) {
	if !s.identityReady(w, r) {
		return
	}
	lq, err := ParseList(r, ListSpec{Filters: []string{"q"}, Sorts: []string{"name"}, DefaultSort: "name"}, s.o.API.Config)
	if err == nil {
		err = ascending(lq)
	}
	var after string
	if err == nil {
		after, err = afterName(lq)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	rows, err := s.o.Auth.Manager.ListServiceAccounts(r.Context(), lq.First("q"), after, lq.FetchLimit())
	if err != nil {
		failIdentity(w, r, err)
		return
	}
	page := NewPage(lq, rows, func(a principal.ServiceAccount) []string { return []string{a.Name} })
	WritePage(w, r, MapPage(page, toServiceAccount))
}

// CreateServiceAccount implements POST /api/v1/service-accounts (R-AUTH-020).
func (s *Server) CreateServiceAccount(w http.ResponseWriter, r *http.Request, _ gen.CreateServiceAccountParams) {
	if !s.identityReady(w, r) {
		return
	}
	body, err := ReadBody(w, r, MediaJSON, s.bodyLimit())
	var in gen.NewServiceAccount
	if err == nil {
		err = StrictUnmarshal(body, &in)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	c := s.caller(r)
	err = s.idem.Do(w, r, body, IdempotentOp{Run: func(ctx context.Context, tx meta.Tx) (Outcome, error) {
		a, err := s.o.Auth.Manager.CreateServiceAccount(principal.Join(ctx, tx), c, principal.NewServiceAccount{Name: in.Name, Description: derefString(in.Description)})
		if err != nil {
			return Outcome{}, identityErr(err)
		}
		return Outcome{Status: http.StatusCreated, Body: toServiceAccount(a), Header: map[string]string{"Location": location(r, "/api/v1/service-accounts/"+a.Name)}}, nil
	}})
	if err != nil {
		failIdentity(w, r, err)
	}
}

// GetServiceAccount implements GET /api/v1/service-accounts/{name}.
func (s *Server) GetServiceAccount(w http.ResponseWriter, r *http.Request, name string, _ gen.GetServiceAccountParams) {
	if !s.identityReady(w, r) {
		return
	}
	a, err := s.o.Auth.Manager.GetServiceAccount(r.Context(), name)
	if err != nil {
		failIdentity(w, r, err)
		return
	}
	if NotModified(w, r, ETag(a.Revision, "")) {
		return
	}
	SetETag(w, a.Revision, "")
	writeJSON(w, r, http.StatusOK, toServiceAccount(a))
}

// serviceAccountPatchBody is a merge patch of a service account.
type serviceAccountPatchBody struct {
	Description json.RawMessage `json:"description"`

	ID        json.RawMessage `json:"id"`
	Name      json.RawMessage `json:"name"`
	Status    json.RawMessage `json:"status"`
	CreatedAt json.RawMessage `json:"createdAt"`
}

// UpdateServiceAccount implements PATCH /api/v1/service-accounts/{name}.
func (s *Server) UpdateServiceAccount(w http.ResponseWriter, r *http.Request, name string, _ gen.UpdateServiceAccountParams) {
	if !s.identityReady(w, r) {
		return
	}
	pre, err := ParseIfMatch(r)
	var raw []byte
	if err == nil {
		raw, err = ReadMergePatch(w, r, s.bodyLimit())
	}
	var in serviceAccountPatchBody
	if err == nil {
		err = StrictUnmarshal(raw, &in)
	}
	var desc *string
	if err == nil {
		err = errorsFirst(
			func() (e error) { desc, e = patchString(in.Description, "/description"); return },
			func() error { return immutableString(in.Name, "/name", name) },
		)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	if desc == nil {
		a, err := s.o.Auth.Manager.GetServiceAccount(r.Context(), name)
		if err != nil {
			failIdentity(w, r, err)
			return
		}
		if !pre.Matches(a.Revision, "") {
			failIdentity(w, r, Failed(a.Revision))
			return
		}
		SetETag(w, a.Revision, "")
		writeJSON(w, r, http.StatusOK, toServiceAccount(a))
		return
	}
	a, err := s.o.Auth.Manager.UpdateServiceAccount(r.Context(), s.caller(r), name, *desc, expectedRevision(pre))
	if err != nil {
		failIdentity(w, r, err)
		return
	}
	SetETag(w, a.Revision, "")
	writeJSON(w, r, http.StatusOK, toServiceAccount(a))
}

// DeleteServiceAccount implements DELETE /api/v1/service-accounts/{name}.
func (s *Server) DeleteServiceAccount(w http.ResponseWriter, r *http.Request, name string) {
	if !s.identityReady(w, r) {
		return
	}
	if err := s.o.Auth.Manager.DeleteServiceAccount(r.Context(), s.caller(r), name); err != nil {
		failIdentity(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

// DisableServiceAccount implements POST /api/v1/service-accounts/{name}/disable.
func (s *Server) DisableServiceAccount(w http.ResponseWriter, r *http.Request, name string) {
	if !s.identityReady(w, r) {
		return
	}
	var in gen.DisableRequest
	if err := s.readOptionalJSON(w, r, &in); err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	if err := s.o.Auth.Manager.DisableServiceAccount(r.Context(), s.caller(r), name, in.RevokeTokens != nil && *in.RevokeTokens); err != nil {
		failIdentity(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

// EnableServiceAccount implements POST /api/v1/service-accounts/{name}/enable.
func (s *Server) EnableServiceAccount(w http.ResponseWriter, r *http.Request, name string) {
	if !s.identityReady(w, r) {
		return
	}
	if err := s.o.Auth.Manager.EnableServiceAccount(r.Context(), s.caller(r), name); err != nil {
		failIdentity(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}
