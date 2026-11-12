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
	"github.com/guided-traffic/trove/internal/auth/registrytoken"
	"github.com/guided-traffic/trove/internal/auth/session"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/qname"
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

// HashValidator is what the stage needs of the token package to verify a registry token (R-OCI-005): the state of the
// trove token it names by the SHA-256 of its secret, from the token cache under the freshness gate (R-AUTH-023).
// *token.Service implements it. A validator without it refuses every registry token as unknown.
type HashValidator interface {
	CheckSecretHash(ctx context.Context, hash [32]byte, typ token.Type, req token.Request) (token.Info, error)
}

// UseNoter records that a token authenticated a request (R-AUTH-031). The stage
// calls it only when the whole authentication succeeded.
type UseNoter interface {
	NoteUse(tokenID, clientAddr string)
}

// Options configure [New].
type Options struct {
	// Tokens validates Bearer, Basic and API-key credentials.
	Tokens token.Validator
	// Uses records the last use of a token once the whole authentication has
	// succeeded (R-AUTH-031). It is an explicit input and not found on Tokens, so
	// a validator that wraps the token service cannot silently stop the recording;
	// nil records nothing (a replica without a database).
	Uses UseNoter
	// Sessions resolves the session cookie; nil means the replica has no
	// sessions and a cookie is ignored (login area absent).
	Sessions session.Validator
	// Principals resolves the groups of the authenticated principal at request
	// time (R-AUTHZ-002).
	Principals principal.Store
	// Limiter is the failed-credential limiter; nil disables the throttle.
	Limiter FailureLimiter
	// ExemptCIDRs are auth.rate_limit.exempt_cidrs: never throttled.
	ExemptCIDRs []netip.Prefix
	// ClientAddr returns the client address after the trusted-proxy rule
	// (httpserver.RequestInfo.Client); nil disables the throttle.
	ClientAddr func(*http.Request) netip.Addr
	// BasicEnabled is auth.clients.basic.enabled.
	BasicEnabled bool
	// APIKeyHeader is auth.clients.nuget_api_key_header: the stage examines
	// [HeaderAPIKey] on the routes of [EndpointRules.APIKeyRoute]. Off, the header
	// is never read and only Authorization applies there (R-AUTH-033).
	APIKeyHeader bool
	// Registry verifies the registry tokens of the OCI token service (R-OCI-005): a Bearer value with the prefix trv_rgt_
	// on a route below /v2/ is one. Nil refuses every registry token as a malformed credential.
	Registry *registrytoken.Signer
	// RegistryRealm names the realm and the service of the challenge below /v2/ for a request (R-OCI-004 step 1): the
	// token service under the request's response base URL (R-EP-003). Nil answers the plain Bearer challenge of R-AUTH-002.
	RegistryRealm func(*http.Request) (realm, service string)
	// RegistryScope names the scope entry of the challenge of a request below /v2/ (R-OCI-004 step 1): the action the
	// request's route needs on its image name, "" for a route that names none. The registry root learns it from the
	// dispatcher's match of the request, as APIKeyRoute does; nil adds no scope.
	RegistryScope func(*http.Request) string
	Metrics       *principal.Metrics
	Log           *slog.Logger
	// Now is the clock a registry token's expiry is checked against; nil is time.Now.
	Now func() time.Time
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
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Stage{o: o, log: l}
}

// Authenticate implements [Authenticator].
func (s *Stage) Authenticate(r *http.Request, ep EndpointRules) (Result, error) {
	p, raw := rulePaths(r, ep)
	if isExchangeRoute(r.Method, raw) || raw == registrytoken.TokenPath {
		// The ID token in the Authorization header of an exchange is not a
		// credential for this stage (R-AUTH-001). Neither is the credential of a
		// token request of the registry: the token service presents it to
		// [Stage.AuthenticateTokenRequest] itself, from the header or from the form
		// of the OAuth 2.0 request (R-OCI-004).
		return anonymousResult(), nil
	}
	route := routeAnswer{ask: ep.APIKeyRoute, r: r}
	if raw, ok := s.apiKey(r, &route); ok {
		return s.byToken(r, p, ep, raw, MethodAPIKey, "")
	}
	galaxy := routeAnswer{ask: ep.TokenSchemeRoute, r: r}
	if secret, method, user, present := credentialHeader(r, &galaxy); present {
		// A rule that opens something reads the escaped path too: /v2%2Fx is no registry route.
		if method == MethodBearer && registrytoken.Is(secret) && isRegistryPath(p) && isRegistryPath(raw) {
			return s.byRegistryToken(r, p, ep, secret)
		}
		return s.byToken(r, p, ep, secret, method, user)
	}
	if ep.AcceptsSession && s.o.Sessions != nil && sessionAllowed(r.Method, p) {
		// The cookie is never a credential on an API-key route, whatever the
		// method (R-NUGET-012, Q-AUTH-187).
		if c, err := r.Cookie(SessionCookie); err == nil && c.Value != "" && !route.is() {
			return s.bySession(r, p, c.Value)
		}
	}
	return s.noCredential(r, p, raw, ep)
}

// rulePaths are the two readings of the request path the stage's path rules take, where the process serves the route
// family of the request on its endpoint ([EndpointRules.Served]), and "" for both where it does not, which no rule
// matches. A request of an unserved family is so decided like an unknown path by every rule at once - the exchange
// routes, the path rule of the session cookie, the routes that are always open, the public operations, the hard limit on
// an unsafe anonymous request to /api/v1 and the challenge - and its answer, before and after authentication, is the one
// of GET /does-not-exist (R-EP-005, Q-ARCH-302, Q-ARCH-306).
//
// path is the unescaped path, the one ServeMux routes by (it unescapes each segment): the rules that limit a request -
// the hard limit of /api/v1, the session cookie's path rule and the challenge - read it, so an escape in the path
// (/api/v%31/…) cannot step around a limit of the family the router sends the request to. raw is the escaped path as
// received: the rules that open a route read it - the always-open routes, the npm login start and the exchange routes,
// whose repository address qname.ParseAddress reads as the dispatcher does (a "%" in a named segment is no address) -
// so an escape never opens a route (R-AUTH-037).
func rulePaths(r *http.Request, ep EndpointRules) (path, raw string) {
	if ep.Served != nil && !ep.Served(r) {
		return "", ""
	}
	return r.URL.Path, r.URL.EscapedPath()
}

// routeAnswer asks [EndpointRules.APIKeyRoute] at most once per request, and only
// when a credential depends on the answer: a well-formed token in the API-key
// header, or a session cookie the path rule would accept. A request with neither,
// which is nearly every request, costs no route match.
type routeAnswer struct {
	ask   func(*http.Request) bool
	r     *http.Request
	asked bool
	yes   bool
}

func (q *routeAnswer) is() bool {
	if !q.asked {
		q.asked = true
		q.yes = q.ask != nil && q.ask(q.r)
	}
	return q.yes
}

// apiKey returns the token of the API-key header (R-AUTH-001 item 1, R-AUTH-033)
// on a route that declares the header. A value that holds no well-formed token -
// the placeholder "az" some setups pass where no key is configured - is ignored
// and the Authorization header applies; so is the header on every other route,
// and everywhere while auth.clients.nuget_api_key_header is off. Only the first
// header line is read, as for Authorization.
func (s *Stage) apiKey(r *http.Request, route *routeAnswer) (string, bool) {
	if !s.o.APIKeyHeader {
		return "", false
	}
	raw := apiKeyToken(r.Header.Get(HeaderAPIKey))
	if raw == "" || !route.is() {
		return "", false
	}
	return raw, true
}

// apiKeyToken is the trove token an API-key header value carries: the whole value,
// or its part after the last ':' (the "<user>:<token>" convention of older setup
// templates, whose username is ignored), when that passes the syntax step of
// R-AUTH-023 (prefix, length, alphabet, checksum). "" when the value carries none.
func apiKeyToken(v string) string {
	v = strings.TrimSpace(v)
	if _, err := token.Check(v); err == nil {
		return v
	}
	if i := strings.LastIndexByte(v, ':'); i >= 0 {
		if _, err := token.Check(v[i+1:]); err == nil {
			return v[i+1:]
		}
	}
	return ""
}

// metricMethod is the method label of trove_auth_attempts_total, in the vocabulary
// of the request log (R-OPS-015): the API key is nuget_apikey.
func metricMethod(m Method) string {
	if m == MethodAPIKey {
		return "nuget_apikey"
	}
	return string(m)
}

func anonymousResult() Result {
	return Result{
		Anonymous: true,
		Principal: principal.Principal{ID: principal.AnonymousID, Kind: principal.KindAnonymous, Name: "anonymous", Status: principal.StatusActive},
	}
}

func (s *Stage) fail(method Method, reason string, err error) (Result, error) {
	s.o.Metrics.Attempt(metricMethod(method), "failure", reason)
	return Result{}, err
}

// challenge renders the WWW-Authenticate value of the request (R-AUTH-002) for the
// unescaped path of [rulePaths]. A rejected token adds error="invalid_token" on the management
// API. Basic is offered only to package clients on the package and raw-file routes,
// which answer it with configured credentials, and on the registry's token service, which
// takes the trove token as the Basic password (R-OCI-004 step 3); a browser, recognised by its Fetch
// Metadata headers, would answer it with its native login dialog instead of trove's
// sign-in screen, so it gets the Bearer challenge, as does every other route
// (Q-AUTH-173), a route of a family the process does not serve included (Q-ARCH-306).
// Every other route below /v2/ gets the registry's challenge, which names the token
// service (R-OCI-004 step 1).
func (s *Stage) challenge(r *http.Request, path string, tokenRejected bool) string {
	if isManagementAPI(path) {
		if tokenRejected {
			return `Bearer realm="trove", error="invalid_token"`
		}
		return `Bearer realm="trove"`
	}
	if path == registrytoken.TokenPath {
		if fromBrowser(r) {
			return `Bearer realm="trove"`
		}
		return `Basic realm="trove"`
	}
	if isRegistryPath(path) {
		return s.registryChallenge(r, tokenRejected)
	}
	if (strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/files/")) && !fromBrowser(r) {
		return `Basic realm="trove"`
	}
	return `Bearer realm="trove"`
}

// registryChallenge is the challenge of R-OCI-004 step 1 for a request below /v2/:
// the realm and the service of the request's base URL, the scope its route needs,
// and error="invalid_token" for a rejected credential.
func (s *Stage) registryChallenge(r *http.Request, tokenRejected bool) string {
	if s.o.RegistryRealm == nil {
		return `Bearer realm="trove"`
	}
	realm, service := s.o.RegistryRealm(r)
	var scope, code string
	if s.o.RegistryScope != nil {
		scope = s.o.RegistryScope(r)
	}
	if tokenRejected {
		code = registrytoken.ErrorInvalidToken
	}
	return registrytoken.Challenge(realm, service, scope, code)
}

// isRegistryPath reports whether the unescaped path lies below the registry root
// (R-OCI-001, R-HA-041): /v2 or below /v2/.
func isRegistryPath(p string) bool { return p == "/v2" || strings.HasPrefix(p, "/v2/") }

// fromBrowser reports whether a request carries Fetch Metadata request headers,
// which browsers send and package clients do not.
func fromBrowser(r *http.Request) bool {
	return r.Header.Get("Sec-Fetch-Mode") != "" || r.Header.Get("Sec-Fetch-Dest") != ""
}

// unauthenticated is the 401 of the request whose path rules read path ([rulePaths]).
func (s *Stage) unauthenticated(r *http.Request, path, code, detail string, tokenRejected bool) error {
	return errs.E(errs.Unauthenticated, code, detail, errs.WithChallenge(s.challenge(r, path, tokenRejected)))
}

// credentialHeader extracts the token of an Authorization header. present is
// true when the header carries any value, so that a header the stage cannot read
// is a rejection and not a fall-through to anonymous. user is the Basic username.
// The scheme "Token" of the Galaxy clients is a trove token on a route that declares
// it (galaxy asks [EndpointRules.TokenSchemeRoute], at most once and only when the
// header has that scheme); on every other route it is "not a trove token" (R-ANS-006,
// Q-ANS-4).
func credentialHeader(r *http.Request, galaxy *routeAnswer) (raw string, method Method, user string, present bool) {
	h := strings.TrimSpace(r.Header.Get("Authorization"))
	if h == "" {
		return "", MethodNone, "", false
	}
	scheme, rest, _ := strings.Cut(h, " ")
	rest = strings.TrimSpace(rest)
	switch {
	case strings.EqualFold(scheme, "Bearer"):
		return rest, MethodBearer, "", true
	case strings.EqualFold(scheme, "Token") && galaxy.is():
		return rest, MethodGalaxyToken, "", true
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

func (s *Stage) byToken(r *http.Request, path string, ep EndpointRules, raw string, method Method, user string) (Result, error) {
	ctx := r.Context()
	reject := func(reason string) (Result, error) {
		return s.fail(method, reason, s.unauthenticated(r, path, errs.CodeInvalidToken, "The token is not valid.", true))
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
		return s.mapTokenErr(r, path, method, err)
	}
	return s.accept(r, ep, info, Credential{Method: method, BasicUsername: user, ExpiresAt: info.ExpiresAt}, reject)
}

// accept is what the stage does with a valid trove token, presented itself or bound to a registry token: the endpoint
// bindings of its scope (R-EP-011) and the endpoint's rule for unbound tokens, the principal's groups at request time
// (R-AUTHZ-002), the last use (R-AUTH-031) and the metric. cred carries the method and what the presentation adds (the
// Basic user name, the expiry, a registry token's grant); the token's ID, type and scope come from info. Both paths call
// it, so "as if the token were presented itself" (R-OCI-005) holds by construction.
func (s *Stage) accept(r *http.Request, ep EndpointRules, info token.Info, cred Credential, reject func(string) (Result, error)) (Result, error) {
	if len(info.Scope.Endpoints) > 0 && !slices.Contains(info.Scope.Endpoints, ep.Name) {
		ep.refused(r, Refusal{Reason: RefusedTokenBinding, TokenType: info.Type, TokenID: info.ID})
		return reject("bound_elsewhere")
	}
	if ep.BoundTokensOnly && len(info.Scope.Endpoints) == 0 {
		ep.refused(r, Refusal{Reason: RefusedTokenBinding, TokenType: info.Type, TokenID: info.ID})
		return reject("unbound")
	}
	groups, err := s.groups(r.Context(), info.Owner.ID)
	if err != nil {
		return Result{}, err
	}
	if info.Owner.External, err = s.external(r.Context(), info.Owner.ID); err != nil {
		return Result{}, err
	}
	sc := info.Scope
	if s.o.Uses != nil {
		s.o.Uses.NoteUse(info.ID, sourceOf(r, s.clientAddr(r)).ClientAddr)
	}
	s.o.Metrics.Attempt(metricMethod(cred.Method), "success", "")
	cred.TokenID, cred.TokenType, cred.Scope = info.ID, info.Type, &sc
	return Result{Principal: info.Owner, Groups: groups, CI: info.CI, Credential: cred}, nil
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

func (s *Stage) mapTokenErr(r *http.Request, path string, method Method, err error) (Result, error) {
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
		return s.fail(method, "principal_disabled", s.unauthenticated(r, path, errs.CodePrincipalDisabled, "The owner of the token is disabled.", false))
	case errors.Is(err, token.ErrMalformed), errors.Is(err, token.ErrUnknown), errors.Is(err, token.ErrRevoked), errors.Is(err, token.ErrExpired):
		// The response does not say which; the audit event does (R-AUTH-002).
		return s.fail(method, rejectionReason(err), s.unauthenticated(r, path, errs.CodeInvalidToken, "The token is not valid.", true))
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

// external is the user's external flag as the principal cache holds it (R-AUTH-044), read with the status and the groups of
// the same entry, so a change reaches the next request of a session or a personal access token within auth.revocation_bound
// (R-AUTH-023). A principal the cache does not know is not external; the kinds that carry no flag read false.
func (s *Stage) external(ctx context.Context, id string) (bool, error) {
	p, err := s.o.Principals.ByID(ctx, id)
	switch {
	case err == nil:
		return p.External, nil
	case errors.Is(err, principal.ErrNotFound):
		return false, nil
	}
	if _, ok := errs.As(err); ok {
		return false, err
	}
	return false, errs.E(errs.Unavailable, errs.CodeDatabaseUnavailable, "The principal cannot be resolved right now.", errs.WithCause(err))
}

func (s *Stage) bySession(r *http.Request, path, cookie string) (Result, error) {
	ctx := r.Context()
	info, err := s.o.Sessions.Validate(ctx, cookie)
	if err != nil {
		if errors.Is(err, session.ErrInvalid) {
			return s.fail(MethodSession, "invalid", s.unauthenticated(r, path, "session_expired", "The session has ended. Sign in again.", false))
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
	if info.Principal.External, err = s.external(ctx, info.Principal.ID); err != nil {
		return Result{}, err
	}
	s.o.Metrics.Attempt(string(MethodSession), "success", "")
	return Result{Principal: info.Principal, Groups: groups, Credential: Credential{Method: MethodSession, SessionID: info.ID}}, nil
}

// noCredential is the last source of R-AUTH-001: the request becomes anonymous if
// the endpoint allows it (R-AUTH-037), or if
// the route is one that is always reachable without a credential. An endpoint that
// admits no anonymous request turns every request without a credential into a 401,
// those routes included (R-EP-010 item 4), so an outside audience learns nothing
// from the answer about which routes the endpoint serves (R-EP-005). path and raw
// are the readings of [rulePaths]: a route of a family the process does not serve is
// neither always open nor public, and the hard limit of /api/v1 does not apply to it,
// so it is decided like an unknown path (Q-ARCH-302). The always-open routes read
// raw, the hard limit reads path.
func (s *Stage) noCredential(r *http.Request, path, raw string, ep EndpointRules) (Result, error) {
	if !ep.Anonymous {
		ep.refused(r, Refusal{Reason: RefusedAnonymous})
		return s.fail(MethodNone, "no_credential", s.unauthenticated(r, path, errs.CodeCredentialsRequired, "This endpoint requires a credential.", false))
	}
	if path != "" && (alwaysOpen(r.Method, raw) || (ep.PublicRoute != nil && ep.PublicRoute(r))) {
		return anonymousResult(), nil
	}
	// Anonymous never passes an unsafe method on /api/v1 (R-AUTH-037 hard limit). Whether it reads anything is the
	// decision point's, by the visibility levels (R-AUTHZ-003).
	if !unsafeMethod(r.Method) || !isManagementAPI(path) {
		return anonymousResult(), nil
	}
	return s.fail(MethodNone, "no_credential", s.unauthenticated(r, path, errs.CodeCredentialsRequired, "This endpoint requires a credential.", false))
}

// byRegistryToken authenticates a registry token on a route below /v2/ (R-OCI-005): the MAC with the named key, the
// expiry and the endpoint first, without any lookup, so a forged or tampered token costs no query; then the state of the
// trove token it names through the token cache of R-AUTH-023 and the rules of the endpoint, as for that token presented
// itself. The grant travels with the result as a further ceiling; the decision point decides the request as for every
// other (R-AUTHZ-015). An anonymous registry token stands for a request without a credential: it passes while the
// endpoint allows anonymous requests (R-AUTH-037).
func (s *Stage) byRegistryToken(r *http.Request, path string, ep EndpointRules, raw string) (Result, error) {
	ctx := r.Context()
	reject := func(reason string) (Result, error) {
		return s.fail(MethodBearer, reason, s.unauthenticated(r, path, errs.CodeInvalidToken, "The token is not valid.", true))
	}
	if s.o.Registry == nil {
		s.countFailure(ctx, r)
		return reject("malformed")
	}
	c, err := s.o.Registry.Verify(raw, ep.Name, s.o.Now())
	if err != nil {
		s.countFailure(ctx, r)
		return reject(registryRejection(err))
	}
	grant := c.Grant
	expires := c.ExpiresAt
	if c.Anonymous {
		return s.anonymousRegistry(r, path, ep, &grant, &expires)
	}
	typ := token.Type(c.Binding.TokenType)
	if len(ep.TokenTypes) > 0 && !slices.Contains(ep.TokenTypes, typ) {
		ep.refused(r, Refusal{Reason: RefusedTokenType, TokenType: typ})
		return reject("type_not_accepted")
	}
	hv, ok := s.o.Tokens.(HashValidator)
	if !ok {
		return reject("unknown")
	}
	req := token.Request{Source: sourceOf(r, s.clientAddr(r)), RetryAfter: failureWindow}
	if addr := s.clientAddr(r); s.o.Limiter != nil && addr.IsValid() && !exempt(addr, s.o.ExemptCIDRs) {
		req.Throttle = addrThrottle{l: s.o.Limiter, bucket: addr.String()}
	}
	info, err := hv.CheckSecretHash(ctx, c.Binding.SecretSHA256, typ, req)
	if err != nil {
		return s.mapTokenErr(r, path, MethodBearer, err)
	}
	if info.ID != c.Binding.TokenID || info.Type != typ || info.Owner.ID != c.PrincipalID {
		// Only a holder of the key ring can sign claims whose parts disagree with the token they name (H-579); the type is
		// compared too, because a cache hit does not compare it and the endpoint's token types were decided on the claim.
		return reject("binding_mismatch")
	}
	return s.accept(r, ep, info, Credential{Method: MethodBearer, ExpiresAt: &expires, Registry: &grant}, reject)
}

// anonymousRegistry accepts an anonymous registry token while the endpoint allows anonymous requests (R-AUTH-037,
// R-OCI-005); once it does not, the token is refused like a revoked one.
func (s *Stage) anonymousRegistry(r *http.Request, path string, ep EndpointRules, grant *registrytoken.Grant, expires *time.Time) (Result, error) {
	reject := func(reason string) (Result, error) {
		return s.fail(MethodBearer, reason, s.unauthenticated(r, path, errs.CodeInvalidToken, "The token is not valid.", true))
	}
	if !ep.Anonymous {
		ep.refused(r, Refusal{Reason: RefusedAnonymous})
		return reject("anonymous_off")
	}
	res := anonymousResult()
	res.Credential = Credential{Method: MethodBearer, ExpiresAt: expires, Registry: grant}
	return res, nil
}

// registryRejection is the reason label of a registry token that failed verification.
func registryRejection(err error) string {
	switch {
	case errors.Is(err, registrytoken.ErrExpired):
		return "expired"
	case errors.Is(err, registrytoken.ErrEndpoint):
		return "bound_elsewhere"
	case errors.Is(err, registrytoken.ErrSignature), errors.Is(err, registrytoken.ErrUnknownKey):
		return "signature"
	case errors.Is(err, registrytoken.ErrLifetime):
		return "lifetime"
	}
	return "malformed"
}

// Presented is the one credential of a token request of the registry's token service (R-OCI-004): the Authorization
// header of the request, or the user name and password of the OAuth 2.0 form, or none. The token service obtains it
// from [PresentedFrom] or [PresentedForm] and hands it to [Stage.AuthenticateTokenRequest]; it never reads the secret.
type Presented struct {
	method Method
	user   string
	// secret sits behind a pointer, so that even a reflective print of a struct that holds a Presented (fmt's %+v of the
	// token service's request) shows an address and never the token; Presented itself renders its kind only.
	secret *presentedSecret
}

// presentedSecret holds the presented token; it renders nothing of it.
type presentedSecret struct{ v string }

func (*presentedSecret) String() string   { return "[REDACTED]" }
func (*presentedSecret) GoString() string { return "[REDACTED]" }

// String, GoString and LogValue render the kind of the credential only, never the secret or the user name (R-AUTH-001).
func (c Presented) String() string { return "auth.Presented{" + c.Kind() + "}" }

// GoString implements fmt.GoStringer.
func (c Presented) GoString() string { return c.String() }

// LogValue implements slog.LogValuer.
func (c Presented) LogValue() slog.Value { return slog.StringValue(c.String()) }

// value is the presented secret, "" for none.
func (c Presented) value() string {
	if c.secret == nil {
		return ""
	}
	return c.secret.v
}

// methodUnreadable marks an Authorization header in a scheme the stage does not read: a credential that is rejected,
// never none.
const methodUnreadable Method = "unreadable"

// PresentedFrom is the credential of the request's Authorization header, read as the stage reads it (R-AUTH-001 item 2):
// Bearer or Basic with any user name; a header the stage cannot read is a credential that will be rejected. Without the
// header it is none.
func PresentedFrom(r *http.Request) Presented {
	raw, method, user, present := credentialHeader(r, &routeAnswer{})
	if !present {
		return Presented{}
	}
	if method == MethodNone {
		method = methodUnreadable
	}
	return Presented{method: method, user: user, secret: &presentedSecret{v: raw}}
}

// PresentedForm is the credential of the OAuth 2.0 password grant (R-OCI-004 step 2): the password is the trove token and
// the user name is recorded and ignored, as for Basic (INV-8). An empty password is no credential.
func PresentedForm(user, password string) Presented {
	if password == "" {
		return Presented{}
	}
	return Presented{method: MethodBasic, user: user, secret: &presentedSecret{v: password}}
}

// None reports whether no credential was presented.
func (c Presented) None() bool { return c.method == MethodNone }

// Kind names how the credential was presented, for an audit event: basic, bearer, none or unreadable.
func (c Presented) Kind() string {
	if c.method == MethodNone {
		return "none"
	}
	return string(c.method)
}

// SecretSHA256 is the SHA-256 of the presented secret: the binding of the registry token issued for it to the token
// cache of R-AUTH-023 (R-OCI-005). It is meaningful once [Stage.AuthenticateTokenRequest] accepted the credential.
func (c Presented) SecretSHA256() [32]byte { return token.Hash(c.value()) }

// AuthenticateTokenRequest authenticates the credential of a token request of the registry's token service (R-OCI-004
// step 3) with every rule of this stage and of the endpoint: a trove token as Bearer or Basic (Basic only while
// auth.clients.basic is on), the failed-credential throttle, the endpoint's token types and bindings, and, without a
// credential, the anonymous principal where the endpoint and R-AUTH-037 allow it. A registry token is no credential here:
// it fails the syntax step of a trove token, so a registry token cannot renew itself. The 401 carries Basic realm="trove".
func (s *Stage) AuthenticateTokenRequest(r *http.Request, ep EndpointRules, c Presented) (Result, error) {
	p, raw := rulePaths(r, ep)
	switch c.method {
	case MethodNone:
		return s.noCredential(r, p, raw, ep)
	case MethodBasic, MethodBearer:
		return s.byToken(r, p, ep, c.value(), c.method, c.user)
	}
	return s.byToken(r, p, ep, "", MethodNone, "")
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
// on /api/<format> for GET and HEAD, and ignored everywhere else. The login
// options ignore it too: an ended session's cookie would make the sign-in screen
// unreachable, and the browser cannot drop an HttpOnly cookie (Q-AUTH-177). p is
// the unescaped path of [rulePaths], so a route of a family the process does not
// serve ignores the cookie as an unknown path does, an ended session's included
// (Q-ARCH-306), and an escape does not move a request out of its family's rule.
func sessionAllowed(method, p string) bool {
	switch {
	case p == "/api/v1/auth/login-options":
		return false
	case isRegistryPath(p):
		// Never below /v2/, whatever the method: registry clients hold no session, and a
		// browser's ambient cookie must not authorize a registry request (R-OCI-004).
		return false
	case isManagementAPI(p):
		return true
	case strings.HasPrefix(p, "/files/"):
		return slices.Contains([]string{http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete}, method)
	case strings.HasPrefix(p, "/api/"):
		return method == http.MethodGet || method == http.MethodHead
	}
	return false
}

// alwaysOpen lists the routes reachable without a credential whatever the
// anonymous switches say (R-AUTH-037 "Always unauthenticated"): the login
// options, the login tree, the device and CI exchange endpoints, revocation by
// value, the npm login start, and reads of / (the redirect to the UI) and of the
// UI shell under /ui/, whose data
// comes from the management API (Q-AUTH-19). A credential sent to one of them is
// still evaluated. p is the escaped path of [rulePaths]: a route reached only through
// an escape (/api/v1/tokens/revok%65) is not open.
func alwaysOpen(method, p string) bool {
	switch {
	case p == "/" || p == "/ui" || strings.HasPrefix(p, "/ui/"):
		return !unsafeMethod(method)
	case p == "/api/v1/auth/login-options", p == "/api/v1/auth/oidc/token",
		strings.HasPrefix(p, "/api/v1/auth/device/"), strings.HasPrefix(p, "/auth/"),
		npmLoginStart(p):
		return true
	case p == "/api/v1/tokens/revoke":
		return method == http.MethodPost
	}
	return false
}

// npmLoginStart reports whether p is the npm web login start (R-AUTH-034):
// /-/v1/login, or /api/npm/<projectKey>/<repoKey>/-/v1/login, read by
// qname.ParseAddress (one segment after the format is no address, and a "%" in the
// named segments is none either). Any other path that merely ends in /-/v1/login,
// under /files/ or /api/v1/ for example, is not open.
func npmLoginStart(p string) bool {
	if p == "/-/v1/login" {
		return true
	}
	a, ok := qname.ParseAddress(p)
	return ok && a.Family == qname.FamilyAPI && a.Format == "npm" && a.Rest == "-/v1/login"
}

// isExchangeRoute reports whether a request is a POST to a route that takes a
// GitLab ID token as request input: the CI exchange and the npm trusted-publishing
// exchanges (R-AUTH-001). The stage does not evaluate the Authorization header
// of these; the handler validates the ID token. p is the escaped path of [rulePaths]:
// on a process that does not serve the exchange, the header is a credential as on
// any other path (Q-ARCH-302), and an escaped exchange path is no exchange.
func isExchangeRoute(method, p string) bool {
	if method != http.MethodPost {
		return false
	}
	if p == "/api/v1/auth/oidc/token" {
		return true
	}
	const marker = "/-/npm/v1/oidc/token/exchange/package/"
	if strings.HasPrefix(p, "/-/npm/v1/oidc/token/exchange/package/") {
		return true
	}
	a, ok := qname.ParseAddress(p)
	return ok && a.Family == qname.FamilyAPI && a.Format == "npm" && strings.HasPrefix(a.Rest, marker[1:])
}
