package v1

import (
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/auth/session"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// The login operations that are not browser routes: the options of the sign-in
// screen (R-AUTH-009), the caller's principal and credential (R-API-021) and the
// UI sessions of a user (R-AUTH-010).

// GetLoginOptions implements GET /api/v1/auth/login-options. It is public.
func (s *Server) GetLoginOptions(w http.ResponseWriter, r *http.Request) {
	if s.o.Auth.Login == nil {
		httpserver.Problem(w, r, unavailable("The login"))
		return
	}
	anonymous := false
	if s.o.Auth.Settings != nil {
		eff, err := s.o.Auth.Settings.Effective(r.Context())
		if err != nil {
			failIdentity(w, r, principalErr(err))
			return
		}
		anonymous = eff.AnonymousAccess
	}
	o := s.o.Auth.Login.LoginOptions(anonymous)
	out := gen.LoginOptions{Providers: make([]gen.LoginProvider, 0, len(o.Providers)), LocalLogin: o.LocalLogin, AnonymousAccess: o.AnonymousAccess, AutoRedirect: optString(o.AutoRedirect)}
	for _, p := range o.Providers {
		out.Providers = append(out.Providers, gen.LoginProvider{Id: p.ID, DisplayName: p.DisplayName, StartUrl: p.StartURL, Available: p.Available})
	}
	writeJSON(w, r, http.StatusOK, out)
}

// principalErr keeps an *errs.Error and makes any other error an unavailable
// service: a failed read of a runtime setting is not the caller's fault.
func principalErr(err error) error {
	if _, ok := errs.As(err); ok {
		return err
	}
	return errs.Wrap(err, "api.settings")
}

// GetWhoami implements GET /api/v1/auth/whoami (R-AUTH-028, R-API-021).
func (s *Server) GetWhoami(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := httpserver.PrincipalFrom(ctx)
	out := gen.Whoami{
		Principal: gen.WhoamiPrincipal{Kind: gen.WhoamiPrincipalKind(p.Kind), Name: p.Name, Id: uuidOf(p.ID)},
	}
	groups := make([]string, 0, len(p.Groups))
	for _, g := range p.Groups {
		groups = append(groups, g.Name)
	}
	out.Principal.Groups = &groups
	if !tokenAuthenticated(p) {
		out.Credential = gen.WhoamiCredential{Type: gen.WhoamiCredentialTypeSession}
		writeJSON(w, r, http.StatusOK, out)
		return
	}
	if s.o.Auth.Tokens == nil {
		httpserver.Problem(w, r, unavailable("The token service"))
		return
	}
	rec, err := s.o.Auth.Tokens.Get(ctx, p.Credential.TokenID)
	if err != nil {
		failIdentity(w, r, err)
		return
	}
	id := uuidOf(rec.ID)
	sc := toScope(rec.Scope)
	out.Credential = gen.WhoamiCredential{Type: gen.WhoamiCredentialType(rec.Type), TokenId: &id, Scope: &sc, ExpiresAt: optTime(deref1(rec.ExpiresAt))}
	writeJSON(w, r, http.StatusOK, out)
}

func toSession(row session.LiveSession, currentID string) gen.Session {
	return gen.Session{
		Id: uuidOf(row.ID), CreatedAt: utc(row.CreatedAt.Time), LastSeenAt: utc(row.LastSeenAt.Time), CreatedAddr: optString(row.CreatedAddr),
		UserAgent: optString(row.UserAgent), AuthMethod: gen.SessionAuthMethod(row.AuthMethod), ProviderId: optString(row.ProviderID),
		Current: currentID != "" && row.ID == currentID,
	}
}

func (s *Server) sessionsReady(w http.ResponseWriter, r *http.Request) bool {
	if s.o.Auth.Sessions == nil || s.o.Auth.Manager == nil {
		httpserver.Problem(w, r, unavailable("The session store"))
		return false
	}
	return true
}

func (s *Server) writeSessions(w http.ResponseWriter, r *http.Request, principalID, currentID string) {
	rows, err := s.o.Auth.Sessions.List(r.Context(), principalID)
	if err != nil {
		failIdentity(w, r, errs.Wrap(err, "api.sessions"))
		return
	}
	out := gen.SessionList{Items: make([]gen.Session, 0, len(rows))}
	for _, row := range rows {
		out.Items = append(out.Items, toSession(row, currentID))
	}
	writeJSON(w, r, http.StatusOK, out)
}

// ListOwnSessions implements GET /api/v1/users/me/sessions (R-AUTH-010).
func (s *Server) ListOwnSessions(w http.ResponseWriter, r *http.Request) {
	if !s.sessionsReady(w, r) {
		return
	}
	if _, err := userPrincipalName(r); err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	p := httpserver.PrincipalFrom(r.Context())
	s.writeSessions(w, r, p.ID, p.Credential.SessionID)
}

// noSession is a session ID that no row has: "keep nothing".
const noSession = "00000000-0000-0000-0000-000000000000"

// EndOwnSessions implements DELETE /api/v1/users/me/sessions (R-AUTH-010).
func (s *Server) EndOwnSessions(w http.ResponseWriter, r *http.Request, params gen.EndOwnSessionsParams) {
	if !s.sessionsReady(w, r) {
		return
	}
	if _, err := userPrincipalName(r); err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	p := httpserver.PrincipalFrom(r.Context())
	keep := noSession
	if params.KeepCurrent != nil && *params.KeepCurrent && p.Credential.SessionID != "" {
		keep = p.Credential.SessionID
	}
	if _, err := s.o.Auth.Sessions.EndExcept(r.Context(), p.ID, keep, session.EndLogout, s.caller(r).Actor); err != nil {
		failIdentity(w, r, errs.Wrap(err, "api.endSessions"))
		return
	}
	noContent(w)
}

// EndOwnSession implements DELETE /api/v1/users/me/sessions/{id} (R-AUTH-010).
func (s *Server) EndOwnSession(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	if !s.sessionsReady(w, r) {
		return
	}
	if _, err := userPrincipalName(r); err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	p := httpserver.PrincipalFrom(r.Context())
	found, err := s.o.Auth.Sessions.EndOne(r.Context(), p.ID, id.String(), session.EndLogout, s.caller(r).Actor)
	if err != nil {
		failIdentity(w, r, errs.Wrap(err, "api.endSession"))
		return
	}
	if !found {
		failIdentity(w, r, errs.E(errs.NotFound, errs.CodeNotFound, "The session does not exist."))
		return
	}
	noContent(w)
}

// ListUserSessions implements GET /api/v1/users/{username}/sessions.
func (s *Server) ListUserSessions(w http.ResponseWriter, r *http.Request, username string) {
	if !s.sessionsReady(w, r) {
		return
	}
	u, err := s.o.Auth.Manager.GetUser(r.Context(), username)
	if err != nil {
		failIdentity(w, r, err)
		return
	}
	s.writeSessions(w, r, u.ID, "")
}

// EndUserSessions implements DELETE /api/v1/users/{username}/sessions.
func (s *Server) EndUserSessions(w http.ResponseWriter, r *http.Request, username string) {
	if !s.sessionsReady(w, r) {
		return
	}
	u, err := s.o.Auth.Manager.GetUser(r.Context(), username)
	if err == nil {
		_, err = s.o.Auth.Sessions.EndPrincipalSessions(r.Context(), u.ID, s.caller(r).Actor)
	}
	if err != nil {
		failIdentity(w, r, principalErr(err))
		return
	}
	noContent(w)
}
