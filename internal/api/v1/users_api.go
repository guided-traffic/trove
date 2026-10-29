package v1

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/auth/oidc"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/meta"
)

// The user operations of R-AUTH-016 and R-AUTH-017. The handlers are thin: the
// authorization step has run, principal.Manager does the change, records the
// audit event and notifies the change area auth in one transaction.

func toUser(u principal.User) gen.User {
	out := gen.User{
		Id: uuidOf(u.ID), Username: u.Name, Email: optString(u.Email), EmailVerified: u.EmailVerified, DisplayName: optString(u.DisplayName),
		Origin: gen.UserOrigin(u.Origin), Status: gen.UserStatus(u.Status), DisabledReason: optString(u.DisabledReason),
		IsAdmin: u.IsAdmin, AdminVia: orEmptyStrings(u.AdminVia), Identities: []gen.UserIdentity{}, Groups: []gen.UserGroupMembership{},
		LockedUntil: optTime(u.LockedUntil), LastLoginAt: optTime(u.LastLoginAt), CreatedAt: utc(u.CreatedAt),
	}
	for _, i := range u.Identities {
		out.Identities = append(out.Identities, gen.UserIdentity{Provider: i.Provider, Linked: i.Linked})
	}
	out.Groups = toMemberships(u.Groups)
	if u.GroupClaimMissing != nil {
		out.GroupClaimMissing = &gen.GroupClaimMissing{Provider: u.GroupClaimMissing.Provider, Reason: gen.GroupClaimMissingReason(u.GroupClaimMissing.Reason)}
	}
	return out
}

func toMemberships(ms []principal.Membership) []gen.UserGroupMembership {
	out := make([]gen.UserGroupMembership, 0, len(ms))
	for _, m := range ms {
		out = append(out, gen.UserGroupMembership{Name: m.Group, Source: m.Source})
	}
	return out
}

func orEmptyStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ListUsers implements GET /api/v1/users (R-AUTH-016).
func (s *Server) ListUsers(w http.ResponseWriter, r *http.Request, _ gen.ListUsersParams) {
	if !s.identityReady(w, r) {
		return
	}
	lq, err := ParseList(r, ListSpec{
		Filters: []string{"q", "status", "origin", "group", "admin"}, Sorts: []string{"username"}, DefaultSort: "username",
	}, s.o.API.Config)
	if err == nil {
		err = firstErr(ascending(lq), oneOf("status", lq.First("status"), "active", "disabled", "locked"),
			oneOf("origin", lq.First("origin"), "jit", "admin", "scim", "system"), oneOf("admin", lq.First("admin"), "true", "false"))
	}
	var after string
	if err == nil {
		after, err = afterName(lq)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	rows, err := s.o.Auth.Manager.ListUsers(r.Context(), principal.UserFilter{
		Query: lq.First("q"), Status: lq.First("status"), Origin: lq.First("origin"), Group: lq.First("group"), Admin: lq.First("admin"),
		After: after, Limit: lq.FetchLimit(),
	})
	if err != nil {
		failIdentity(w, r, err)
		return
	}
	page := NewPage(lq, rows, func(u principal.User) []string { return []string{u.Name} })
	WritePage(w, r, MapPage(page, toUser))
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// pendingIdentities turns the identities of a request into pending links. The
// subject is always null (a link is completed by the first login), and the
// provider must be configured: its issuer is part of the link (R-AUTH-007).
func (s *Server) pendingIdentities(list []gen.PendingIdentity) ([]principal.PendingIdentity, error) {
	out := make([]principal.PendingIdentity, 0, len(list))
	for i, pi := range list {
		at := "/identities/" + itoa(i)
		if pi.Subject != nil {
			return nil, errs.E(errs.Invalid, errs.CodeValidation, "A pending identity link has no subject.",
				errs.WithFields(errs.FieldError{Pointer: at + "/subject", Code: "must_be_null", Detail: "the first login at the provider completes the link"}))
		}
		p, ok := s.o.Auth.Config.OIDC.Providers[pi.Provider]
		if !ok {
			return nil, errs.E(errs.Invalid, errs.CodeValidation, "The identity provider is not configured.",
				errs.WithFields(errs.FieldError{Pointer: at + "/provider", Code: "unknown_provider", Detail: "no provider with this ID is configured"}))
		}
		out = append(out, principal.PendingIdentity{Provider: pi.Provider, Issuer: p.Issuer})
	}
	return out, nil
}

// CreateUser implements POST /api/v1/users (R-AUTH-016).
func (s *Server) CreateUser(w http.ResponseWriter, r *http.Request, _ gen.CreateUserParams) {
	if !s.identityReady(w, r) {
		return
	}
	body, err := ReadBody(w, r, MediaJSON, s.bodyLimit())
	var in gen.NewUser
	if err == nil {
		err = StrictUnmarshal(body, &in)
	}
	var pending []principal.PendingIdentity
	if err == nil {
		pending, err = s.pendingIdentities(deref(in.Identities))
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	c := s.caller(r)
	err = s.idem.Do(w, r, body, IdempotentOp{Run: func(ctx context.Context, tx meta.Tx) (Outcome, error) {
		u, err := s.o.Auth.Manager.CreateUser(principal.Join(ctx, tx), c, principal.NewUser{
			Name: in.Username, Email: derefString(in.Email), DisplayName: derefString(in.DisplayName), Identities: pending,
		})
		if err != nil {
			return Outcome{}, identityErr(err)
		}
		return Outcome{Status: http.StatusCreated, Body: toUser(u), Header: map[string]string{"Location": location(r, "/api/v1/users/"+u.Name)}}, nil
	}})
	if err != nil {
		failIdentity(w, r, err)
	}
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// userPrincipalName is the username a user-record operation of the caller
// addresses: a user or the break-glass account has a record, any other kind of
// principal answers 403.
func userPrincipalName(r *http.Request) (string, error) {
	p := httpserver.PrincipalFrom(r.Context())
	if p.Kind != httpserver.KindUser && p.Kind != httpserver.KindBreakGlass {
		return "", errs.E(errs.Forbidden, errs.CodeForbidden, "Only a user has a user record, sessions and preferences.")
	}
	return p.Name, nil
}

// GetCurrentUser implements GET /api/v1/users/me (R-AUTH-016, R-AUTH-017).
func (s *Server) GetCurrentUser(w http.ResponseWriter, r *http.Request) {
	if !s.identityReady(w, r) {
		return
	}
	name, err := userPrincipalName(r)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	u, err := s.o.Auth.Manager.GetUser(r.Context(), name)
	if err != nil {
		failIdentity(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, toUser(u))
}

// GetUser implements GET /api/v1/users/{username} (R-AUTH-016).
func (s *Server) GetUser(w http.ResponseWriter, r *http.Request, username string) {
	if !s.identityReady(w, r) {
		return
	}
	u, err := s.o.Auth.Manager.GetUser(r.Context(), username)
	if err != nil {
		failIdentity(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, toUser(u))
}

// userPatchBody is a merge patch of a user. The members after Identities are the
// read-only ones of the user record: a body obtained by GET can be sent back and
// they are ignored (R-API-013).
type userPatchBody struct {
	Email       json.RawMessage `json:"email"`
	DisplayName json.RawMessage `json:"displayName"`
	Identities  json.RawMessage `json:"identities"`

	ID                json.RawMessage `json:"id"`
	Username          json.RawMessage `json:"username"`
	EmailVerified     json.RawMessage `json:"emailVerified"`
	Origin            json.RawMessage `json:"origin"`
	Status            json.RawMessage `json:"status"`
	DisabledReason    json.RawMessage `json:"disabledReason"`
	IsAdmin           json.RawMessage `json:"isAdmin"`
	AdminVia          json.RawMessage `json:"adminVia"`
	Groups            json.RawMessage `json:"groups"`
	GroupClaimMissing json.RawMessage `json:"groupClaimMissing"`
	LockedUntil       json.RawMessage `json:"lockedUntil"`
	LastLoginAt       json.RawMessage `json:"lastLoginAt"`
	CreatedAt         json.RawMessage `json:"createdAt"`
}

// UpdateUser implements PATCH /api/v1/users/{username} (R-AUTH-016).
func (s *Server) UpdateUser(w http.ResponseWriter, r *http.Request, username string) {
	if !s.identityReady(w, r) {
		return
	}
	raw, err := ReadMergePatch(w, r, s.bodyLimit())
	var in userPatchBody
	if err == nil {
		err = StrictUnmarshal(raw, &in)
	}
	var p principal.UserPatch
	if err == nil {
		err = errorsFirst(
			func() (e error) { p.Email, e = patchString(in.Email, "/email"); return },
			func() (e error) { p.DisplayName, e = patchString(in.DisplayName, "/displayName"); return },
			func() (e error) { p.Identities, e = s.patchIdentities(in.Identities); return },
			func() error { return immutableString(in.Username, "/username", username) },
		)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	u, err := s.o.Auth.Manager.UpdateUser(r.Context(), s.caller(r), username, p)
	if err != nil {
		failIdentity(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, toUser(u))
}

// patchIdentities reads the identities member of a user patch: pending links
// replace the pending links, and an entry the user record shows as linked is
// kept as it is, so a body obtained by GET can be sent back.
func (s *Server) patchIdentities(raw json.RawMessage) (*[]principal.PendingIdentity, error) {
	if raw == nil {
		return nil, nil
	}
	var list []struct {
		Provider string `json:"provider"`
		Subject  any    `json:"subject"`
		Linked   bool   `json:"linked"`
	}
	if string(raw) != "null" {
		if err := StrictUnmarshal(raw, &list); err != nil {
			return nil, malformed("/identities", "type", "the value must be an array of identity links", err)
		}
	}
	pending := make([]gen.PendingIdentity, 0, len(list))
	for _, e := range list {
		if e.Linked {
			continue
		}
		pending = append(pending, gen.PendingIdentity{Provider: e.Provider, Subject: e.Subject})
	}
	out, err := s.pendingIdentities(pending)
	return &out, err
}

// DeleteUser implements DELETE /api/v1/users/{username} (R-AUTH-016).
func (s *Server) DeleteUser(w http.ResponseWriter, r *http.Request, username string) {
	if !s.identityReady(w, r) {
		return
	}
	if err := s.o.Auth.Manager.DeleteUser(r.Context(), s.caller(r), username); err != nil {
		failIdentity(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

// readOptionalJSON decodes an optional JSON body; no body leaves dst as it is.
func (s *Server) readOptionalJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	body, err := readOptionalBody(w, r, s.bodyLimit())
	if err != nil || body == nil {
		return err
	}
	if err := requireJSON(r, MediaJSON); err != nil {
		return err
	}
	return StrictUnmarshal(body, dst)
}

// DisableUser implements POST /api/v1/users/{username}/disable (R-AUTH-016).
func (s *Server) DisableUser(w http.ResponseWriter, r *http.Request, username string) {
	if !s.identityReady(w, r) {
		return
	}
	var in gen.DisableRequest
	if err := s.readOptionalJSON(w, r, &in); err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	revoke := in.RevokeTokens == nil || *in.RevokeTokens
	// The reason of the request is free text for the audit trail; the stored
	// disabled reason is the class (admin), which dormancy and SCIM also use.
	err := s.o.Auth.Manager.DisableUser(r.Context(), s.caller(r), username,
		principal.DisableOptions{Reason: "admin", Note: derefString(in.Reason), RevokeTokens: revoke})
	if err != nil {
		failIdentity(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

// EnableUser implements POST /api/v1/users/{username}/enable (R-AUTH-016).
func (s *Server) EnableUser(w http.ResponseWriter, r *http.Request, username string) {
	if !s.identityReady(w, r) {
		return
	}
	if err := s.o.Auth.Manager.EnableUser(r.Context(), s.caller(r), username); err != nil {
		failIdentity(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

// PatchUserGroups implements PATCH /api/v1/users/{username}/groups (R-AUTH-016).
func (s *Server) PatchUserGroups(w http.ResponseWriter, r *http.Request, username string) {
	if !s.identityReady(w, r) {
		return
	}
	var in gen.MembershipChange
	if err := DecodeJSON(w, r, &in, s.bodyLimit()); err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	ms, err := s.o.Auth.Manager.PatchUserGroups(r.Context(), s.caller(r), username, deref(in.Add), deref(in.Remove))
	if err != nil {
		failIdentity(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, gen.UserGroups{Groups: toMemberships(ms)})
}

// UnlockBreakGlass implements POST /api/v1/users/{username}/unlock (R-AUTH-014,
// R-AUTH-038). Only the break-glass account has a lockout.
func (s *Server) UnlockBreakGlass(w http.ResponseWriter, r *http.Request, username string) {
	if s.o.API.Store == nil || s.o.Auth.Config.BreakGlass.Username == "" {
		httpserver.Problem(w, r, unavailable("The break-glass unlock"))
		return
	}
	if username != s.o.Auth.Config.BreakGlass.Username {
		failIdentity(w, r, principal.NotFound("The break-glass account"))
		return
	}
	c := s.caller(r)
	if _, err := oidc.Unlock(r.Context(), s.o.API.Store, s.recorder(), c.Actor, c.Source); err != nil {
		failIdentity(w, r, principal.DBErr("api.unlock", err))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}
