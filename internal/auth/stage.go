package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/session"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/errs"
)

// SessionCookie is the name of the UI session cookie (Q-AUTH-2).
const SessionCookie = "__Host-trv_session"

// failureWindow is the window of the failed-credential limiter: one minute
// (R-AUTH-038). A throttled answer carries it as Retry-After.
const failureWindow = time.Minute

// TokenValidator is what the stage needs of the token package: the contract
// validator, and the form that takes the request's throttle and source. A
// validator that only implements [token.Validator] is called without a throttle.
type TokenValidator interface {
	token.Validator
	Check(ctx context.Context, raw string, req token.Request) (token.Info, error)
}

// AnonymousSettings reads the runtime anonymous switch (R-AUTH-037, R-AUTH-040).
// *token.SettingsStore implements it.
type AnonymousSettings interface {
	Effective(ctx context.Context) (token.Effective, error)
}

// Options configure [New].
type Options struct {
	// Tokens validates Bearer and Basic credentials.
	Tokens token.Validator
	// Sessions resolves the session cookie; nil means the replica has no
	// sessions and a cookie is ignored (login area absent).
	Sessions session.Validator
	// Principals resolves the groups of the authenticated principal at request
	// time (R-AUTHZ-002).
	Principals principal.Store
	// Settings gives the runtime anonymous switch with the YAML ceiling
	// auth.anonymous.allowed folded in; nil means anonymous access is off.
	Settings AnonymousSettings
	// Limiter is the failed-credential limiter; nil disables the throttle.
	Limiter FailureLimiter
	// ExemptCIDRs are auth.rate_limit.exempt_cidrs: never throttled.
	ExemptCIDRs []netip.Prefix
	// ClientAddr returns the client address after the trusted-proxy rule
	// (httpserver.RequestInfo.Client); nil disables the throttle.
	ClientAddr func(*http.Request) netip.Addr
	// BasicEnabled is auth.clients.basic.enabled.
	BasicEnabled bool
	Metrics      *principal.Metrics
	Log          *slog.Logger
}

// Stage is the authentication stage (R-AUTH-001, R-AUTH-002): it selects the one
// credential of a request in the order of R-AUTH-001, validates it, applies the
// rules of the endpoint, and yields the principal, the anonymous principal or a
// rejection. A credential that is present but invalid is a rejection; it never
// falls through to a lower source or to anonymous.
type Stage struct {
	o   Options
	log *slog.Logger
}

var _ Authenticator = (*Stage)(nil)

// New creates the stage.
func New(o Options) *Stage {
	l := o.Log
	if l == nil {
		l = slog.Default()
	}
	return &Stage{o: o, log: l}
}

// Authenticate implements [Authenticator].
func (s *Stage) Authenticate(r *http.Request, ep EndpointRules) (Result, error) {
	ctx := r.Context()
	if isExchangeRoute(r) {
		// The ID token in the Authorization header of an exchange is not a
		// credential for this stage (R-AUTH-001).
		return anonymousResult(), nil
	}
	if raw, method, user, present := credentialHeader(r); present {
		return s.byToken(r, ep, raw, method, user)
	}
	if ep.AcceptsSession && s.o.Sessions != nil && sessionAllowed(r) {
		if c, err := r.Cookie(SessionCookie); err == nil && c.Value != "" {
			return s.bySession(r, c.Value)
		}
	}
	return s.noCredential(ctx, r, ep)
}

func anonymousResult() Result {
	return Result{
		Anonymous: true,
		Principal: principal.Principal{ID: principal.AnonymousID, Kind: principal.KindAnonymous, Name: "anonymous", Status: principal.StatusActive},
	}
}

func (s *Stage) fail(method Method, reason string, err error) (Result, error) {
	s.o.Metrics.Attempt(string(method), "failure", reason)
	return Result{}, err
}

// challenge renders the WWW-Authenticate value of the endpoint family
// (R-AUTH-002). A rejected token adds error="invalid_token" on the management API.
func challenge(path string, tokenRejected bool) string {
	if path == "/api/v1" || strings.HasPrefix(path, "/api/v1/") {
		if tokenRejected {
			return `Bearer realm="trove", error="invalid_token"`
		}
		return `Bearer realm="trove"`
	}
	return `Basic realm="trove"`
}

func unauthenticated(r *http.Request, code, detail string, tokenRejected bool) error {
	return errs.E(errs.Unauthenticated, code, detail, errs.WithChallenge(challenge(r.URL.Path, tokenRejected)))
}

// credentialHeader extracts the token of an Authorization header. present is
// true when the header carries any value, so that a header the stage cannot read
// is a rejection and not a fall-through to anonymous. user is the Basic username.
func credentialHeader(r *http.Request) (raw string, method Method, user string, present bool) {
	h := strings.TrimSpace(r.Header.Get("Authorization"))
	if h == "" {
		return "", MethodNone, "", false
	}
	scheme, rest, _ := strings.Cut(h, " ")
	rest = strings.TrimSpace(rest)
	switch {
	case strings.EqualFold(scheme, "Bearer"):
		return rest, MethodBearer, "", true
	case strings.EqualFold(scheme, "Basic"):
		dec, err := base64.StdEncoding.DecodeString(rest)
		if err != nil {
			return "", MethodBasic, "", true
		}
		u, pw, ok := strings.Cut(string(dec), ":")
		if !ok {
			return "", MethodBasic, "", true
		}
		return pw, MethodBasic, u, true
	}
	return "", MethodNone, "", true
}

func (s *Stage) byToken(r *http.Request, ep EndpointRules, raw string, method Method, user string) (Result, error) {
	ctx := r.Context()
	reject := func(reason string) (Result, error) {
		return s.fail(method, reason, unauthenticated(r, errs.CodeInvalidToken, "The token is not valid.", true))
	}
	if method == MethodNone {
		return reject("unsupported_scheme") // not a trove token: rejected without a lookup
	}
	if method == MethodBasic && !s.o.BasicEnabled {
		return reject("basic_disabled")
	}
	typ, err := token.Check(raw)
	if err != nil {
		s.countFailure(ctx, r)
		return reject("malformed")
	}
	if len(ep.TokenTypes) > 0 && !slices.Contains(ep.TokenTypes, typ) {
		ep.refused(r, Refusal{Reason: RefusedTokenType, TokenType: typ})
		return reject("type_not_accepted") // refused without a database lookup (R-EP-010)
	}
	info, err := s.validate(ctx, r, raw)
	if err != nil {
		return s.mapTokenErr(r, method, err)
	}
	if len(info.Scope.Endpoints) > 0 && !slices.Contains(info.Scope.Endpoints, ep.Name) {
		ep.refused(r, Refusal{Reason: RefusedTokenBinding, TokenType: typ, TokenID: info.ID})
		return reject("bound_elsewhere")
	}
	if ep.BoundTokensOnly && len(info.Scope.Endpoints) == 0 {
		ep.refused(r, Refusal{Reason: RefusedTokenBinding, TokenType: typ, TokenID: info.ID})
		return reject("unbound")
	}
	groups, err := s.groups(ctx, info.Owner.ID)
	if err != nil {
		return Result{}, err
	}
	sc := info.Scope
	s.o.Metrics.Attempt(string(method), "success", "")
	return Result{
		Principal: info.Owner,
		Groups:    groups,
		Credential: Credential{
			Method: method, TokenID: info.ID, TokenType: info.Type, Scope: &sc, BasicUsername: user,
		},
	}, nil
}

func (s *Stage) validate(ctx context.Context, r *http.Request, raw string) (token.Info, error) {
	tv, ok := s.o.Tokens.(TokenValidator)
	if !ok {
		return s.o.Tokens.Validate(ctx, raw)
	}
	req := token.Request{Source: sourceOf(r, s.clientAddr(r)), RetryAfter: failureWindow}
	if addr := s.clientAddr(r); s.o.Limiter != nil && addr.IsValid() && !exempt(addr, s.o.ExemptCIDRs) {
		req.Throttle = addrThrottle{l: s.o.Limiter, bucket: addr.String()}
	}
	return tv.Check(ctx, raw, req)
}

func (s *Stage) clientAddr(r *http.Request) netip.Addr {
	if s.o.ClientAddr == nil {
		return netip.Addr{}
	}
	return s.o.ClientAddr(r)
}

func sourceOf(r *http.Request, addr netip.Addr) audit.Source {
	src := audit.Source{UserAgent: r.UserAgent()}
	if addr.IsValid() {
		src.ClientAddr = addr.String()
	}
	return src
}

// countFailure counts a malformed credential, which never reaches the validator.
func (s *Stage) countFailure(ctx context.Context, r *http.Request) {
	addr := s.clientAddr(r)
	if s.o.Limiter != nil && addr.IsValid() && !exempt(addr, s.o.ExemptCIDRs) {
		s.o.Limiter.Count(ctx, addr.String())
	}
}

func (s *Stage) mapTokenErr(r *http.Request, method Method, err error) (Result, error) {
	var throttled *token.ThrottledError
	switch {
	case errors.As(err, &throttled):
		s.o.Metrics.RateLimited("credential_failures")
		retry := throttled.RetryAfter
		if retry <= 0 {
			retry = failureWindow
		}
		return s.fail(method, "throttled", errs.E(errs.RateLimited, errs.CodeRateLimited, "Too many rejected credentials from this address.", errs.WithRetryAfter(retry)))
	case errors.Is(err, token.ErrOwner):
		return s.fail(method, "principal_disabled", unauthenticated(r, errs.CodePrincipalDisabled, "The owner of the token is disabled.", false))
	case errors.Is(err, token.ErrMalformed), errors.Is(err, token.ErrUnknown), errors.Is(err, token.ErrRevoked), errors.Is(err, token.ErrExpired):
		// The response does not say which; the audit event does (R-AUTH-002).
		return s.fail(method, rejectionReason(err), unauthenticated(r, errs.CodeInvalidToken, "The token is not valid.", true))
	}
	if _, ok := errs.As(err); ok {
		return s.fail(method, "unavailable", err)
	}
	// A storage failure is 503, never 401, so clients keep their working credentials.
	s.log.Warn("token validation failed", "err", err)
	return s.fail(method, "unavailable", errs.E(errs.Unavailable, errs.CodeDatabaseUnavailable, "The credential cannot be checked right now.", errs.WithCause(err)))
}

func rejectionReason(err error) string {
	switch {
	case errors.Is(err, token.ErrRevoked):
		return "revoked"
	case errors.Is(err, token.ErrExpired):
		return "expired"
	case errors.Is(err, token.ErrUnknown):
		return "unknown"
	}
	return "malformed"
}

// groups resolves the principal's groups at request time from the principal
// cache (R-AUTHZ-002); a token carries no group list.
func (s *Stage) groups(ctx context.Context, id string) ([]principal.GroupRef, error) {
	g, err := s.o.Principals.Groups(ctx, id)
	if err != nil {
		if errors.Is(err, principal.ErrNotFound) {
			return nil, nil
		}
		if _, ok := errs.As(err); ok {
			return nil, err
		}
		return nil, errs.E(errs.Unavailable, errs.CodeDatabaseUnavailable, "The principal cannot be resolved right now.", errs.WithCause(err))
	}
	return g, nil
}

func (s *Stage) bySession(r *http.Request, cookie string) (Result, error) {
	ctx := r.Context()
	info, err := s.o.Sessions.Validate(ctx, cookie)
	if err != nil {
		if errors.Is(err, session.ErrInvalid) {
			return s.fail(MethodSession, "invalid", unauthenticated(r, "session_expired", "The session has ended. Sign in again.", false))
		}
		if _, ok := errs.As(err); ok {
			return s.fail(MethodSession, "unavailable", err)
		}
		return s.fail(MethodSession, "unavailable", errs.E(errs.Unavailable, errs.CodeDatabaseUnavailable, "The session cannot be checked right now.", errs.WithCause(err)))
	}
	if err := s.o.Sessions.CheckCSRF(r, info); err != nil {
		if errors.Is(err, session.ErrCSRF) {
			return s.fail(MethodSession, "csrf", errs.E(errs.Forbidden, "csrf_failed", "The request failed the CSRF check."))
		}
		return s.fail(MethodSession, "csrf", err)
	}
	groups, err := s.groups(ctx, info.Principal.ID)
	if err != nil {
		return Result{}, err
	}
	s.o.Metrics.Attempt(string(MethodSession), "success", "")
	return Result{Principal: info.Principal, Groups: groups, Credential: Credential{Method: MethodSession, SessionID: info.ID}}, nil
}

// noCredential is the last source of R-AUTH-001: the request becomes anonymous if
// the endpoint, the runtime switch and the ceiling allow it (R-AUTH-037), or if
// the route is one that is always reachable without a credential. An endpoint that
// admits no anonymous request turns every request without a credential into a 401,
// those routes included (R-EP-010 item 4), so an outside audience learns nothing
// from the answer about which routes the endpoint serves (R-EP-005).
func (s *Stage) noCredential(ctx context.Context, r *http.Request, ep EndpointRules) (Result, error) {
	if !ep.Anonymous {
		ep.refused(r, Refusal{Reason: RefusedAnonymous})
		return s.fail(MethodNone, "no_credential", unauthenticated(r, errs.CodeCredentialsRequired, "This endpoint requires a credential.", false))
	}
	if alwaysOpen(r) || (ep.PublicRoute != nil && ep.PublicRoute(r)) {
		return anonymousResult(), nil
	}
	// Anonymous never passes an unsafe method on /api/v1 (R-AUTH-037 hard limit).
	if s.o.Settings != nil && (!unsafeMethod(r.Method) || !isManagementAPI(r.URL.Path)) {
		eff, err := s.o.Settings.Effective(ctx)
		if err != nil {
			if _, ok := errs.As(err); ok {
				return Result{}, err
			}
			return Result{}, errs.E(errs.Unavailable, errs.CodeDatabaseUnavailable, "The settings cannot be read right now.", errs.WithCause(err))
		}
		if eff.AnonymousAccess {
			return anonymousResult(), nil
		}
	}
	return s.fail(MethodNone, "no_credential", unauthenticated(r, errs.CodeCredentialsRequired, "This endpoint requires a credential.", false))
}

func unsafeMethod(m string) bool {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

func isManagementAPI(p string) bool { return p == "/api/v1" || strings.HasPrefix(p, "/api/v1/") }

// sessionAllowed is the path rule of R-AUTH-001 item 3: the session cookie is
// accepted on /api/v1 for every method, on /files for GET, HEAD, PUT and DELETE,
// on /api/<format> for GET and HEAD, and ignored everywhere else.
func sessionAllowed(r *http.Request) bool {
	p := r.URL.Path
	switch {
	case isManagementAPI(p):
		return true
	case strings.HasPrefix(p, "/files/"):
		return slices.Contains([]string{http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete}, r.Method)
	case strings.HasPrefix(p, "/api/"):
		return r.Method == http.MethodGet || r.Method == http.MethodHead
	}
	return false
}

// alwaysOpen lists the routes reachable without a credential whatever the
// anonymous switches say (R-AUTH-037 "Always unauthenticated"): the login
// options, the login tree, the device and CI exchange endpoints, revocation by
// value, the npm login start, and reads of / (the redirect to the UI) and of the
// UI shell under /ui/, whose data
// comes from the management API (Q-AUTH-19). A credential sent to one of them is
// still evaluated.
func alwaysOpen(r *http.Request) bool {
	p := r.URL.Path
	switch {
	case p == "/" || p == "/ui" || strings.HasPrefix(p, "/ui/"):
		return !unsafeMethod(r.Method)
	case p == "/api/v1/auth/login-options", p == "/api/v1/auth/oidc/token",
		strings.HasPrefix(p, "/api/v1/auth/device/"), strings.HasPrefix(p, "/auth/"),
		npmLoginStart(p):
		return true
	case p == "/api/v1/tokens/revoke":
		return r.Method == http.MethodPost
	}
	return false
}

// npmLoginStart reports whether p is the npm web login start (R-AUTH-034):
// /-/v1/login, or /api/npm/<repoKey>/-/v1/login with one key segment. Any other
// path that merely ends in /-/v1/login, under /files/ or /api/v1/ for example,
// is not open.
func npmLoginStart(p string) bool {
	if p == "/-/v1/login" {
		return true
	}
	rest, ok := strings.CutPrefix(p, "/api/npm/")
	if !ok {
		return false
	}
	key, tail, ok := strings.Cut(rest, "/")
	return ok && key != "" && tail == "-/v1/login"
}

// isExchangeRoute reports whether a request is a POST to a route that takes a
// GitLab ID token as request input: the CI exchange and the npm trusted-publishing
// exchanges (R-AUTH-001). The stage does not evaluate the Authorization header
// of these; the handler validates the ID token.
func isExchangeRoute(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	p := r.URL.Path
	if p == "/api/v1/auth/oidc/token" {
		return true
	}
	const marker = "/-/npm/v1/oidc/token/exchange/package/"
	if strings.HasPrefix(p, "/-/npm/v1/oidc/token/exchange/package/") {
		return true
	}
	if rest, ok := strings.CutPrefix(p, "/api/npm/"); ok {
		_, tail, ok := strings.Cut(rest, "/")
		return ok && strings.HasPrefix(tail, marker[1:])
	}
	return false
}
