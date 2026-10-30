package v1

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/auth"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/auth/session"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/meta"
)

// The token operations of R-AUTH-027 to R-AUTH-030. The value of a token is in
// the answer that creates it and nowhere else: every answer that carries one is
// sent with Cache-Control: no-store (writeJSON sets it for every answer), and an
// idempotent replay of such a create answers 409 token_already_issued, because
// the value is not stored.

func toScope(sc scope.Scope) gen.TokenScope {
	grants := make([]gen.TokenScopeGrant, 0, len(sc.Repositories))
	for _, g := range sc.Repositories {
		out := gen.TokenScopeGrant{Repositories: orEmptyStrings(g.Repositories), Actions: []gen.TokenScopeGrantActions{}}
		for _, a := range g.Actions {
			out.Actions = append(out.Actions, gen.TokenScopeGrantActions(a))
		}
		if p := toPatterns(g.Packages); p != nil {
			out.Packages = p
		}
		if p := toPatterns(g.Paths); p != nil {
			out.Paths = p
		}
		grants = append(grants, out)
	}
	out := gen.TokenScope{Inherit: refTo(sc.Inherit), Admin: refTo(sc.Admin), Repositories: &grants, System: refTo(orEmptyStrings(sc.System))}
	if len(sc.Endpoints) > 0 {
		out.Endpoints = refTo(sc.Endpoints)
	}
	return out
}

func toPatterns(p scope.Patterns) *gen.TokenScopePatterns {
	if len(p.Include)+len(p.Exclude) == 0 {
		return nil
	}
	out := gen.TokenScopePatterns{}
	if len(p.Include) > 0 {
		out.Include = refTo(p.Include)
	}
	if len(p.Exclude) > 0 {
		out.Exclude = refTo(p.Exclude)
	}
	return &out
}

func toOwner(o token.Owner) gen.TokenOwner {
	return gen.TokenOwner{Kind: gen.TokenOwnerKind(o.Kind), Name: o.Name}
}

// toToken renders a token record. unknown is the endpoint predicate of the
// configuration (nil: every name is configured): the endpoint names the token is
// bound to that no configured endpoint carries are listed as unknownEndpoints,
// because the token is valid on none of them (R-EP-011).
func (s *Server) toToken(r token.Record) gen.Token {
	out := gen.Token{
		UnknownEndpoints: s.unknownEndpoints(r.Scope.Endpoints),
		Id:               uuidOf(r.ID), Type: gen.TokenType(r.Type), Hint: r.Hint, Owner: toOwner(r.Owner), Description: r.Description,
		Scope: toScope(r.Scope), CreatedAt: utc(r.CreatedAt), CreatedBy: optString(r.CreatedBy), CreatedVia: r.CreatedVia,
		ExpiresAt: optTime(deref1(r.ExpiresAt)), LastUsedAt: optTime(deref1(r.LastUsedAt)), LastUsedAddr: optString(r.LastUsedAddr),
		State: gen.TokenState(r.State), RevokedAt: optTime(deref1(r.RevokedAt)), RevokedBy: optString(r.RevokedBy),
		RevocationReason: optString(r.RevocationReason), SelfRotatable: r.SelfRotatable,
	}
	if r.RotatedFrom != "" {
		u := uuidOf(r.RotatedFrom)
		out.RotatedFrom = &u
	}
	if len(r.CIContext) > 0 && string(r.CIContext) != "null" {
		var m map[string]any
		if json.Unmarshal(r.CIContext, &m) == nil {
			out.CiContext = &m
		}
	}
	return out
}

func (s *Server) unknownEndpoints(bound []string) []string {
	out := []string{}
	if s.o.Auth.EndpointConfigured == nil {
		return out
	}
	for _, n := range bound {
		if !s.o.Auth.EndpointConfigured(n) {
			out = append(out, n)
		}
	}
	return out
}

func deref1(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

func toCreated(c token.Created) gen.TokenCreated {
	r := c.Record
	out := gen.TokenCreated{
		Id: uuidOf(r.ID), Type: gen.TokenCreatedType(r.Type), Token: c.Secret, Hint: r.Hint, Owner: toOwner(r.Owner),
		Description: r.Description, Scope: toScope(r.Scope), CreatedAt: utc(r.CreatedAt), ExpiresAt: optTime(deref1(r.ExpiresAt)),
		SelfRotatable: r.SelfRotatable,
	}
	if r.RotatedFrom != "" {
		u := uuidOf(r.RotatedFrom)
		out.RotatedFrom = &u
	}
	return out
}

// tokenAuthenticated reports whether the request was authenticated by a token
// (Bearer, or Basic with a token as the password).
func tokenAuthenticated(p httpserver.Principal) bool {
	return p.Credential.TokenID != "" && (p.Credential.Method == httpserver.MethodBearer || p.Credential.Method == httpserver.MethodBasic)
}

// requireRecentAuth is the rule of R-AUTH-011: an action that mints a long-lived
// credential or widens permissions through a UI session needs an interactive
// authentication within auth.sessions.recent_auth_window (401
// reauthentication_required). The shared authorization step calls it for every
// operation that declares x-trove-recent-auth (R-API-007); no handler does. A
// caller that presents a token is not asked: the token has its own rules.
func (s *Server) requireRecentAuth(r *http.Request) error {
	p := httpserver.PrincipalFrom(r.Context())
	if p.Credential.Method != httpserver.MethodSession {
		return nil
	}
	if s.o.Auth.Sessions == nil {
		return unavailable("The session store")
	}
	info, err := s.o.Auth.Sessions.Validate(r.Context(), session.CookieValue(r, session.CookieSession))
	if err != nil {
		return session.Problem(err)
	}
	if !info.AuthenticatedWithin(s.o.Auth.Config.Sessions.RecentAuthWindow) {
		return session.ReauthenticationRequired()
	}
	return nil
}

// presenting returns the token that authenticates the request as the parent of
// a derived create (R-AUTH-027), or nil for a session.
func (s *Server) presenting(ctx context.Context, p httpserver.Principal) (*token.Info, error) {
	if !tokenAuthenticated(p) {
		return nil, nil
	}
	rec, err := s.o.Auth.Tokens.Get(ctx, p.Credential.TokenID)
	if err != nil {
		return nil, err
	}
	if rec.State != "active" {
		// Revoked or expired since this replica's cache accepted it (the bound of
		// R-AUTH-023): it mints nothing. The creating transaction checks again
		// under a lock (R-AUTH-027).
		return nil, errs.E(errs.Unauthenticated, errs.CodeInvalidToken, "The presenting token is no longer valid.",
			errs.WithChallenge("Bearer"))
	}
	return &token.Info{
		ID: rec.ID, Type: rec.Type, Scope: rec.Scope, ExpiresAt: rec.ExpiresAt,
		Owner: principal.Principal{ID: rec.Owner.ID, Kind: rec.Owner.Kind, Name: rec.Owner.Name},
	}, nil
}

func via(p httpserver.Principal) string {
	if p.Credential.Method == httpserver.MethodSession {
		return "ui"
	}
	return "api"
}

// requestTTL turns the lifetime members of a create request into the service's
// form.
func requestTTL(in gen.CreateTokenRequest) token.TTLRequest {
	var out token.TTLRequest
	if in.TtlSeconds != nil {
		out.Seconds = refTo(int64(*in.TtlSeconds))
	}
	if in.ExpiresAt != nil {
		t := in.ExpiresAt.UTC()
		out.ExpiresAt = &t
	}
	return out
}

func scopeBytes(sc *gen.TokenScope) ([]byte, error) {
	if sc == nil {
		return nil, nil
	}
	return json.Marshal(sc)
}

// tokenIssued is the answer to a replay of a create whose value was not stored.
func tokenIssued(id string) error {
	return errs.E(errs.Conflict, errs.CodeTokenAlreadyIssued,
		"The first request with this Idempotency-Key created a token whose value is shown once and not stored; create a new token with a new key.",
		errs.WithExtension("tokenId", id))
}

// issue runs a token creation behind the Idempotency-Key and answers 201 with the
// value. The secret never goes into the stored outcome.
func (s *Server) issue(w http.ResponseWriter, r *http.Request, body []byte, run func(ctx context.Context, tx meta.Tx) (token.Created, error)) {
	err := s.idem.Do(w, r, body, IdempotentOp{
		Run: func(ctx context.Context, tx meta.Tx) (Outcome, error) {
			created, err := run(ctx, tx)
			if err != nil {
				return Outcome{}, identityErr(err)
			}
			return Outcome{
				Status: http.StatusCreated, Body: toCreated(created), SecretID: created.Record.ID,
				Header: map[string]string{"Location": location(r, "/api/v1/tokens/"+created.Record.ID)},
			}, nil
		},
		SecretReplay: tokenIssued,
	})
	if err != nil {
		failIdentity(w, r, err)
	}
}

// CreateToken implements POST /api/v1/tokens (R-AUTH-027): a personal access
// token for the caller.
func (s *Server) CreateToken(w http.ResponseWriter, r *http.Request, _ gen.CreateTokenParams) {
	if !s.identityReady(w, r) {
		return
	}
	ctx := r.Context()
	p := httpserver.PrincipalFrom(ctx)
	body, err := ReadBody(w, r, MediaJSON, s.bodyLimit())
	var in gen.CreateTokenRequest
	if err == nil {
		err = StrictUnmarshal(body, &in)
	}
	var scope []byte
	if err == nil {
		scope, err = scopeBytes(in.Scope)
	}
	if err == nil {
		err = firstErr(refuseBreakGlass(p), refuseOtherOwner(in.Owner, p))
	}
	var parent *token.Info
	if err == nil {
		parent, err = s.presenting(ctx, p)
	}
	if err != nil {
		failIdentity(w, r, err)
		return
	}
	req := token.CreateRequest{
		Caller: s.caller(r), Type: token.TypePAT, OwnerID: p.ID, Description: in.Description, Scope: scope, TTL: requestTTL(in),
		SelfRotatable: in.SelfRotatable != nil && *in.SelfRotatable, Via: via(p), Parent: parent,
	}
	s.issue(w, r, body, func(ctx context.Context, tx meta.Tx) (token.Created, error) {
		return s.o.Auth.Tokens.CreateTx(ctx, tx, req)
	})
}

func refuseBreakGlass(p httpserver.Principal) error {
	if p.Kind == httpserver.KindBreakGlass {
		return errs.E(errs.Forbidden, errs.CodeBreakGlassRestricted, "The break-glass account holds no tokens.")
	}
	return nil
}

// refuseOtherOwner is the rule of R-AUTH-027: there is no impersonation, so an
// owner other than the caller is refused, administrators included.
func refuseOtherOwner(owner *string, p httpserver.Principal) error {
	if owner == nil || *owner == p.Name || (p.Kind == httpserver.KindServiceAccount && *owner == "sa:"+p.Name) {
		return nil
	}
	return errs.E(errs.Forbidden, errs.CodeForbidden, "A personal access token can be created for the caller only; there is no impersonation.")
}

// CreateServiceAccountToken implements POST /api/v1/service-accounts/{name}/tokens
// (R-AUTH-027): an administrator, or a token of the same account under the
// derivation rule.
func (s *Server) CreateServiceAccountToken(w http.ResponseWriter, r *http.Request, name string, _ gen.CreateServiceAccountTokenParams) {
	if !s.identityReady(w, r) {
		return
	}
	ctx := r.Context()
	p := httpserver.PrincipalFrom(ctx)
	body, err := ReadBody(w, r, MediaJSON, s.bodyLimit())
	var in gen.CreateTokenRequest
	if err == nil {
		err = StrictUnmarshal(body, &in)
	}
	var scope []byte
	if err == nil {
		scope, err = scopeBytes(in.Scope)
	}
	if err != nil {
		failIdentity(w, r, err)
		return
	}
	// The caller is an administrator or the account itself; anyone else gets the
	// same 403 whether the account exists or not.
	derived := p.Kind == httpserver.KindServiceAccount && p.Name == name && tokenAuthenticated(p)
	admin, err := s.holds(ctx, "role:admin")
	if err == nil && !admin && !derived {
		err = errs.E(errs.Forbidden, errs.CodeForbidden, "Only an administrator, or a token of the account itself, creates its tokens.")
	}
	var parent *token.Info
	if err == nil && derived {
		parent, err = s.presenting(ctx, p)
	}
	var sa principal.ServiceAccount
	if err == nil {
		sa, err = s.o.Auth.Manager.GetServiceAccount(ctx, name)
	}
	if err != nil {
		failIdentity(w, r, err)
		return
	}
	req := token.CreateRequest{
		Caller: s.caller(r), Type: token.TypeSAT, OwnerID: sa.ID, Description: in.Description, Scope: scope, TTL: requestTTL(in),
		SelfRotatable: in.SelfRotatable != nil && *in.SelfRotatable, Via: via(p), Admin: admin && !derived, Parent: parent,
	}
	s.issue(w, r, body, func(ctx context.Context, tx meta.Tx) (token.Created, error) {
		return s.o.Auth.Tokens.CreateTx(ctx, tx, req)
	})
}

// holdsCapability reports whether the caller holds a capability, as the
// authorization step would decide it.
func (s *Server) holdsCapability(ctx context.Context, c string) (bool, error) { return s.holds(ctx, c) }

// ListTokens implements GET /api/v1/tokens (R-AUTH-028).
func (s *Server) ListTokens(w http.ResponseWriter, r *http.Request, _ gen.ListTokensParams) {
	if !s.identityReady(w, r) {
		return
	}
	ctx := r.Context()
	p := httpserver.PrincipalFrom(ctx)
	lq, err := ParseList(r, ListSpec{Filters: []string{"owner", "type", "state", "expiresBefore", "expiresAfter", "lastUsedBefore"}}, s.o.API.Config)
	if err == nil {
		err = firstErr(oneOf("type", lq.First("type"), "pat", "sat", "cit"), oneOf("state", lq.First("state"), "active", "expired", "revoked", "all"))
	}
	f := token.ListFilter{Type: token.Type(lq.First("type")), State: lq.First("state"), Limit: lq.FetchLimit()}
	for name, dst := range map[string]*time.Time{"expiresBefore": &f.ExpiresBefore, "expiresAfter": &f.ExpiresAfter, "lastUsedBefore": &f.LastUsedBefore} {
		if err != nil || lq.First(name) == "" {
			continue
		}
		t, perr := time.Parse(time.RFC3339, lq.First(name))
		if perr != nil {
			err = errs.E(errs.Invalid, errs.CodeInvalid, "The query parameter "+name+" must be an RFC 3339 time.")
			continue
		}
		*dst = t
	}
	if err == nil && len(lq.After) > 0 {
		if len(lq.After) != 1 || !principal.IsID(lq.After[0]) {
			err = invalidCursor()
		} else {
			f.After = lq.After[0]
		}
	}
	var all bool
	if err == nil {
		all, err = s.holdsCapability(ctx, "tokens:read")
	}
	empty := false
	if err == nil {
		f.OwnerID, empty, err = s.listOwner(ctx, p, all, lq.First("owner"))
	}
	if err != nil {
		failIdentity(w, r, err)
		return
	}
	var rows []token.Record
	if !empty {
		rows, err = s.o.Auth.Tokens.List(ctx, f)
		if err != nil {
			failIdentity(w, r, err)
			return
		}
	}
	page := NewPage(lq, rows, func(t token.Record) []string { return []string{t.ID} })
	WritePage(w, r, MapPage(page, s.toToken))
}

// listOwner resolves the owner filter of the token list. A caller without
// tokens:read sees its own tokens and the filter is forced to the caller; one
// who holds it may name a user or a service account. empty is true for an owner
// that does not exist.
func (s *Server) listOwner(ctx context.Context, p httpserver.Principal, all bool, owner string) (id string, empty bool, err error) {
	if !all || owner == "me" {
		return p.ID, false, nil
	}
	switch {
	case owner == "":
		return "", false, nil
	case strings.HasPrefix(owner, "sa:"):
		sa, err := s.o.Auth.Manager.GetServiceAccount(ctx, strings.TrimPrefix(owner, "sa:"))
		return sa.ID, isNotFound(err), ignoreNotFound(err)
	}
	u, err := s.o.Auth.Manager.GetUser(ctx, owner)
	return u.ID, isNotFound(err), ignoreNotFound(err)
}

func isNotFound(err error) bool {
	e, ok := errs.As(err)
	return ok && e.Kind == errs.NotFound
}

func ignoreNotFound(err error) error {
	if isNotFound(err) {
		return nil
	}
	return err
}

// visible reports whether the caller may see a token: its owner, or a holder of
// tokens:read. A token it may not see is a 404, so that IDs cannot be
// enumerated (R-AUTH-028).
func (s *Server) visible(ctx context.Context, p httpserver.Principal, rec token.Record, capability string) (bool, error) {
	if rec.Owner.ID == p.ID && p.ID != "" {
		return true, nil
	}
	return s.holdsCapability(ctx, capability)
}

// loadVisible reads a token and answers 404 when it is missing or hidden.
func (s *Server) loadVisible(ctx context.Context, p httpserver.Principal, id, capability string) (token.Record, error) {
	rec, err := s.o.Auth.Tokens.Get(ctx, id)
	if err != nil {
		return token.Record{}, err
	}
	ok, err := s.visible(ctx, p, rec, capability)
	if err != nil {
		return token.Record{}, err
	}
	if !ok {
		return token.Record{}, principal.NotFound("The token")
	}
	return rec, nil
}

// GetToken implements GET /api/v1/tokens/{id} (R-AUTH-028).
func (s *Server) GetToken(w http.ResponseWriter, r *http.Request, id string) {
	if !s.identityReady(w, r) {
		return
	}
	rec, err := s.loadVisible(r.Context(), httpserver.PrincipalFrom(r.Context()), id, "tokens:read")
	if err != nil {
		failIdentity(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, s.toToken(rec))
}

func notAToken() error {
	return errs.E(errs.NotFound, errs.CodeNotAToken, "The request is not authenticated by a token.")
}

// GetCurrentToken implements GET /api/v1/tokens/current (R-AUTH-028).
func (s *Server) GetCurrentToken(w http.ResponseWriter, r *http.Request) {
	if !s.identityReady(w, r) {
		return
	}
	p := httpserver.PrincipalFrom(r.Context())
	if !tokenAuthenticated(p) {
		failIdentity(w, r, notAToken())
		return
	}
	rec, err := s.o.Auth.Tokens.Get(r.Context(), p.Credential.TokenID)
	if err != nil {
		failIdentity(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, s.toToken(rec))
}

func noContent(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

// RevokeToken implements DELETE /api/v1/tokens/{id} (R-AUTH-029): the owner, an
// administrator or a holder of tokens:revoke.
func (s *Server) RevokeToken(w http.ResponseWriter, r *http.Request, id string) {
	if !s.identityReady(w, r) {
		return
	}
	ctx := r.Context()
	p := httpserver.PrincipalFrom(ctx)
	rec, err := s.loadVisible(ctx, p, id, "tokens:revoke")
	if err != nil {
		failIdentity(w, r, err)
		return
	}
	reason := token.ReasonAdmin
	if rec.Owner.ID == p.ID {
		reason = token.ReasonOwner
	}
	if _, err := s.o.Auth.Tokens.Revoke(ctx, s.caller(r), rec.ID, reason); err != nil {
		failIdentity(w, r, err)
		return
	}
	noContent(w)
}

// RevokeCurrentToken implements DELETE /api/v1/tokens/current (R-AUTH-029).
func (s *Server) RevokeCurrentToken(w http.ResponseWriter, r *http.Request) {
	if !s.identityReady(w, r) {
		return
	}
	p := httpserver.PrincipalFrom(r.Context())
	if !tokenAuthenticated(p) {
		failIdentity(w, r, notAToken())
		return
	}
	if _, err := s.o.Auth.Tokens.Revoke(r.Context(), s.caller(r), p.Credential.TokenID, token.ReasonOwner); err != nil {
		failIdentity(w, r, err)
		return
	}
	noContent(w)
}

// RevokeOwnTokens implements DELETE /api/v1/users/me/tokens (R-AUTH-029).
func (s *Server) RevokeOwnTokens(w http.ResponseWriter, r *http.Request) {
	if !s.identityReady(w, r) {
		return
	}
	p := httpserver.PrincipalFrom(r.Context())
	if p.Kind != httpserver.KindUser {
		failIdentity(w, r, errs.E(errs.Forbidden, errs.CodeForbidden, "Only a user holds personal tokens."))
		return
	}
	if _, err := s.o.Auth.Tokens.RevokeAll(r.Context(), s.caller(r), p.ID, token.ReasonOwner); err != nil {
		failIdentity(w, r, err)
		return
	}
	noContent(w)
}

// RevokeUserTokens implements DELETE /api/v1/users/{username}/tokens.
func (s *Server) RevokeUserTokens(w http.ResponseWriter, r *http.Request, username string) {
	if !s.identityReady(w, r) {
		return
	}
	u, err := s.o.Auth.Manager.GetUser(r.Context(), username)
	if err == nil {
		_, err = s.o.Auth.Tokens.RevokeAll(r.Context(), s.caller(r), u.ID, token.ReasonAdmin)
	}
	if err != nil {
		failIdentity(w, r, err)
		return
	}
	noContent(w)
}

// RevokeServiceAccountTokens implements DELETE /api/v1/service-accounts/{name}/tokens.
func (s *Server) RevokeServiceAccountTokens(w http.ResponseWriter, r *http.Request, name string) {
	if !s.identityReady(w, r) {
		return
	}
	sa, err := s.o.Auth.Manager.GetServiceAccount(r.Context(), name)
	if err == nil {
		_, err = s.o.Auth.Tokens.RevokeAll(r.Context(), s.caller(r), sa.ID, token.ReasonAdmin)
	}
	if err != nil {
		failIdentity(w, r, err)
		return
	}
	noContent(w)
}

// RevokeTokenByValue implements POST /api/v1/tokens/revoke (R-AUTH-029,
// Q-AUTH-6). It is open to unauthenticated callers; the answer reveals nothing
// about the token except to an administrator.
func (s *Server) RevokeTokenByValue(w http.ResponseWriter, r *http.Request) {
	if s.o.Auth.Tokens == nil {
		httpserver.Problem(w, r, unavailable("The token service"))
		return
	}
	if l := s.o.Auth.FlowStarts; l != nil {
		if ok, retry := l.Allow(r.Context(), auth.AddressBucket(httpserver.InfoFrom(r.Context()).Client.String())); !ok {
			httpserver.Problem(w, r, errs.E(errs.RateLimited, errs.CodeRateLimited,
				"Too many requests from this address; retry later.", errs.WithRetryAfter(retry)))
			return
		}
	}
	var in gen.RevokeByValueRequest
	if err := DecodeJSON(w, r, &in, s.bodyLimit()); err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	ctx := r.Context()
	res, err := s.o.Auth.Tokens.RevokeByValue(ctx, s.caller(r), in.Token)
	if errors.Is(err, token.ErrMalformed) {
		httpserver.Problem(w, r, malformed("/token", "syntax", "the value is not a trove token", nil))
		return
	}
	if err != nil {
		failIdentity(w, r, err)
		return
	}
	if res.Found {
		if admin, err := s.holds(ctx, "role:admin"); err == nil && admin {
			writeJSON(w, r, http.StatusOK, gen.RevokedToken{Id: uuidOf(res.ID), Owner: res.Owner})
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusAccepted)
}

// rotateOverlap reads the optional body of a rotation and returns it for the
// idempotency check.
func (s *Server) rotateOverlap(w http.ResponseWriter, r *http.Request) (overlap int64, body []byte, err error) {
	body, err = readOptionalBody(w, r, s.bodyLimit())
	if err != nil || body == nil {
		return 0, nil, err
	}
	if err = requireJSON(r, MediaJSON); err != nil {
		return 0, nil, err
	}
	var in gen.RotateRequest
	if err = StrictUnmarshal(body, &in); err != nil || in.OverlapSeconds == nil {
		return 0, body, err
	}
	n := int64(*in.OverlapSeconds)
	if n < 0 || n > 86400 {
		return 0, body, errs.E(errs.Invalid, errs.CodeValidation, "overlapSeconds must be between 0 and 86400.",
			errs.WithFields(errs.FieldError{Pointer: "/overlapSeconds", Code: "out_of_range", Detail: "0 to 86400"}))
	}
	return n, body, nil
}

// RotateToken implements POST /api/v1/tokens/{id}/rotate (R-AUTH-030). A PAT is
// rotated by its owner only; a service-account token by an administrator or
// under the derivation rule by a token of the same account.
func (s *Server) RotateToken(w http.ResponseWriter, r *http.Request, id string, _ gen.RotateTokenParams) {
	if !s.identityReady(w, r) {
		return
	}
	ctx := r.Context()
	p := httpserver.PrincipalFrom(ctx)
	overlap, body, err := s.rotateOverlap(w, r)
	var rec token.Record
	if err == nil {
		err = refuseBreakGlass(p)
	}
	if err == nil {
		rec, err = s.loadVisible(ctx, p, id, "tokens:read")
	}
	var parent *token.Info
	if err == nil {
		parent, err = s.rotationParent(ctx, p, rec)
	}
	if err != nil {
		failIdentity(w, r, err)
		return
	}
	req := token.RotateRequest{Caller: s.caller(r), ID: rec.ID, OverlapSeconds: overlap, Parent: parent}
	s.issue(w, r, body, func(ctx context.Context, tx meta.Tx) (token.Created, error) {
		return s.o.Auth.Tokens.RotateTx(ctx, tx, req)
	})
}

// rotationParent decides who may rotate a token and returns the presenting token
// whose limits then apply (R-AUTH-030). Nobody rotates a token for another user:
// the new value would act as that user (Q-AUTH-7).
func (s *Server) rotationParent(ctx context.Context, p httpserver.Principal, rec token.Record) (*token.Info, error) {
	forbidden := errs.E(errs.Forbidden, errs.CodeForbidden, "Only the owner rotates a personal token; an administrator can revoke it.")
	switch rec.Type {
	case token.TypeCIT:
		return nil, errs.E(errs.Forbidden, errs.CodeCITokenRestricted, "A CI token cannot be rotated.")
	case token.TypePAT:
		if rec.Owner.ID != p.ID {
			return nil, forbidden
		}
		return s.presenting(ctx, p)
	}
	// A service-account token: its own token under the derivation rule, or an administrator.
	if p.Kind == httpserver.KindServiceAccount && p.ID == rec.Owner.ID && tokenAuthenticated(p) {
		return s.presenting(ctx, p)
	}
	admin, err := s.holds(ctx, "role:admin")
	if err != nil {
		return nil, err
	}
	if !admin {
		return nil, errs.E(errs.Forbidden, errs.CodeForbidden, "Only an administrator, or a token of the account itself, rotates a service-account token.")
	}
	return nil, nil
}

// RotateCurrentToken implements POST /api/v1/tokens/current/rotate (R-AUTH-030):
// the token rotates itself, only if it was created with selfRotatable.
func (s *Server) RotateCurrentToken(w http.ResponseWriter, r *http.Request) {
	if !s.identityReady(w, r) {
		return
	}
	p := httpserver.PrincipalFrom(r.Context())
	if !tokenAuthenticated(p) {
		failIdentity(w, r, notAToken())
		return
	}
	overlap, body, err := s.rotateOverlap(w, r)
	if err != nil {
		failIdentity(w, r, err)
		return
	}
	req := token.RotateRequest{Caller: s.caller(r), ID: p.Credential.TokenID, OverlapSeconds: overlap, Self: true}
	s.issue(w, r, body, func(ctx context.Context, tx meta.Tx) (token.Created, error) {
		return s.o.Auth.Tokens.RotateTx(ctx, tx, req)
	})
}
