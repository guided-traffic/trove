package oidc

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/session"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// Purposes of a login flow (oidc_login_flow.purpose).
const (
	purposeLogin  = "login"
	purposeReauth = "reauth"
)

// StartInput is a request to begin a login flow (R-AUTH-005), already parsed by
// the edge (internal/api/login).
type StartInput struct {
	ProviderID string
	// Reauth is purpose=reauth: the flow proves the identity of the live session
	// SessionCookie names (R-AUTH-011).
	Reauth        bool
	SessionCookie string
	// ReturnTo is the requested return path as the client sent it; [SafeReturnTo]
	// decides what is kept.
	ReturnTo string
	// Binding is the browser-binding cookie the request presented; one that is
	// not well formed is replaced.
	Binding    string
	ClientAddr string
}

// StartOutput is where the browser goes and the binding it must carry.
type StartOutput struct {
	// AuthorizationURL is the provider's authorization endpoint with the request.
	AuthorizationURL string
	// Binding is the value of the browser-binding cookie, valid for BindingTTL.
	Binding    string
	BindingTTL time.Duration
}

// StartFlow generates state, nonce and the PKCE verifier, stores the flow in
// PostgreSQL bound to the browser, and returns the authorization request
// (R-AUTH-005). No credential is needed; purpose=reauth needs a live session
// (R-AUTH-011). Its errors are the problems the edge writes.
func (s *Service) StartFlow(ctx context.Context, in StartInput) (StartOutput, error) {
	p, ok := s.byID[in.ProviderID]
	if !ok {
		return StartOutput{}, errs.E(errs.NotFound, errs.CodeNotFound, "No login provider has this ID.")
	}
	if err := s.allow(ctx, in.ClientAddr); err != nil {
		return StartOutput{}, err
	}
	doc := p.document()
	if doc == nil {
		return StartOutput{}, errs.E(errs.Unavailable, errs.CodeProviderUnavailable,
			"The login provider's discovery has not succeeded yet.", errs.WithRetryAfter(30*time.Second))
	}
	if s.pendingFull(ctx) {
		s.m.RateLimited("pending_flows")
		return StartOutput{}, errs.E(errs.Unavailable, errs.CodeTooManyPendingLogins, "Too many logins are pending; retry later.", errs.WithRetryAfter(30*time.Second))
	}
	purpose, reauthID := purposeLogin, ""
	if in.Reauth {
		si, err := s.o.Sessions.Validate(ctx, in.SessionCookie)
		if err != nil {
			return StartOutput{}, session.Problem(err)
		}
		purpose, reauthID = purposeReauth, si.ID
	}
	state, nonce, verifier := session.NewSecret(), session.NewSecret(), session.NewSecret()
	// An existing binding is reused, so parallel logins in several tabs do not
	// overwrite each other (R-AUTH-005).
	binding := in.Binding
	if !session.WellFormed(binding) {
		binding = session.NewSecret()
	}
	err := s.o.DB.Read(ctx, func(db q.DBTX) error {
		return q.New(db).InsertLoginFlow(ctx, q.InsertLoginFlowParams{
			StateSha256: session.Hash(state), ProviderID: p.ID, BrowserBindingSha256: session.Hash(binding),
			CodeVerifier: verifier, NonceSha256: session.Hash(nonce), Purpose: purpose, ReauthSessionID: reauthID,
			ReturnTo: SafeReturnTo(in.ReturnTo), TtlS: flowTTL.Seconds(),
		})
	})
	if err != nil {
		return StartOutput{}, postgres.Unavailable(err)
	}
	target, err := p.authorizationURL(doc, state, nonce, verifier)
	if err != nil {
		return StartOutput{}, errs.E(errs.Unavailable, errs.CodeProviderUnavailable, "The login provider's authorization endpoint is unusable.", errs.WithCause(err))
	}
	return StartOutput{AuthorizationURL: target, Binding: binding, BindingTTL: flowTTL}, nil
}

// authorizationURL is the authorization request of OpenID Connect Core 1.0
// §3.1.2.1 with PKCE S256 (RFC 7636 §4.2) and the configured extra parameters,
// which cannot override the parameters trove sets (checked at load).
func (p *Provider) authorizationURL(doc *discovery, state, nonce, verifier string) (string, error) {
	u, err := url.Parse(doc.AuthorizationEndpoint)
	if err != nil {
		return "", err
	}
	v := u.Query()
	for k, val := range p.cfg.ExtraAuthParams {
		v.Set(k, val)
	}
	sum := sha256.Sum256([]byte(verifier))
	v.Set("response_type", "code")
	v.Set("client_id", p.cfg.ClientID)
	v.Set("redirect_uri", p.redirectURI)
	v.Set("scope", strings.Join(p.cfg.Scopes, " "))
	v.Set("state", state)
	v.Set("nonce", nonce)
	v.Set("code_challenge", base64.RawURLEncoding.EncodeToString(sum[:]))
	v.Set("code_challenge_method", "S256")
	u.RawQuery = v.Encode()
	return u.String(), nil
}

// CallbackInput is the provider's redirect back to trove (R-AUTH-006), already
// parsed by the edge.
type CallbackInput struct {
	ProviderID string
	// Query is the callback's query: state, code, error, error_description, iss.
	Query url.Values
	// Binding and SessionCookie are the browser-binding and session cookies the
	// request presented.
	Binding       string
	SessionCookie string
	ClientAddr    string
	UserAgent     string
	// Source is the audit source of the request.
	Source audit.Source
}

// CallbackOutput is a completed login: the session whose cookies the edge sets and
// the path the browser returns to.
type CallbackOutput struct {
	Session  session.Created
	ReturnTo string
}

// LoginFailure is a login that ended without a session (R-AUTH-006). The failure
// is already counted and recorded as auth.login.failed; the edge ends the browser
// on an error page.
type LoginFailure struct {
	// Reason is the machine-readable reason of the failure event and the page.
	Reason string
	// Detail is the provider's error description, truncated to 200 characters.
	Detail string
	// Provider says the identity provider is at fault (502).
	Provider bool
	// Unavailable says a dependency of trove did not answer (503).
	Unavailable bool
}

func (f *LoginFailure) Error() string { return "login failed: " + f.Reason }

// Callback runs R-AUTH-006 from the provider's redirect to the session. Any
// failure is a [*LoginFailure], except an unknown provider, which is a 404
// problem; no session is created then.
func (s *Service) Callback(ctx context.Context, in CallbackInput) (CallbackOutput, error) {
	p, ok := s.byID[in.ProviderID]
	if !ok {
		return CallbackOutput{}, errs.E(errs.NotFound, errs.CodeNotFound, "No login provider has this ID.")
	}
	done, lerr := s.callback(ctx, in, p)
	if lerr != nil {
		return CallbackOutput{}, s.failed(ctx, in, p, done.purpose, lerr)
	}
	return CallbackOutput{Session: done.created, ReturnTo: done.returnTo}, nil
}

// completed is a successful callback.
type completed struct {
	created  session.Created
	returnTo string
	purpose  string
}

// callback runs steps 1 to 10 of R-AUTH-006 in order.
func (s *Service) callback(ctx context.Context, in CallbackInput, p *Provider) (completed, *loginError) {
	qv := in.Query
	flow, lerr := s.takeFlow(ctx, qv.Get("state"))
	if lerr != nil {
		return completed{}, lerr
	}
	done := completed{purpose: flow.Purpose, returnTo: flow.ReturnTo}
	if lerr := p.checkResponse(in, flow); lerr != nil {
		return done, lerr
	}
	idToken, access, lerr := p.exchange(ctx, qv.Get("code"), flow.CodeVerifier)
	if lerr != nil {
		return done, lerr
	}
	v, lerr := p.validateIDToken(ctx, idToken, flow.NonceSha256, time.Now())
	if lerr != nil {
		return done, lerr
	}
	userinfo, lerr := p.userinfo(ctx, access, v.subject)
	if lerr != nil {
		return done, lerr
	}
	// Step 9: the access token goes out of scope here; neither it nor a refresh
	// token is stored or accepted anywhere (INV-8).
	m, lerr := p.mapClaims(v, userinfo, s.o.Auth.BreakGlass.Username, s.log)
	if lerr != nil {
		return done, lerr
	}
	m.claims.Source = in.Source
	var reauth *session.Info
	if flow.Purpose == purposeReauth {
		si, lerr := s.reauthSession(ctx, in, flow, v)
		if lerr != nil {
			return done, lerr
		}
		reauth = &si
	}
	pr, err := s.o.Provisioner.SignIn(ctx, m.claims)
	if err != nil {
		if reason, ok := refusalReason(err); ok {
			return done, fail(reason, err)
		}
		return done, systemFailure(err)
	}
	if reauth != nil && pr.ID != reauth.Principal.ID {
		return done, fail(ReasonReauthUserMismatch, fmt.Errorf("the provisioner resolved principal %s, the session belongs to %s", pr.ID, reauth.Principal.ID))
	}
	n := session.NewSession{
		PrincipalID: pr.ID, ProviderID: p.ID, IDPSID: v.sid, IDToken: idToken,
		ClientAddr: in.ClientAddr, UserAgent: in.UserAgent,
		ReplaceCookie: in.SessionCookie,
	}
	if reauth != nil {
		n.ReplaceID = reauth.ID
	}
	created, err := s.o.Sessions.Create(ctx, n)
	if err != nil {
		if reason, ok := refusalReason(err); ok {
			return done, fail(reason, err)
		}
		return done, systemFailure(err)
	}
	done.created = created
	s.succeeded(ctx, in.Source, p, pr, created.ID, flow.Purpose)
	return done, nil
}

// refusalReason maps a refusal of the provisioner to the reason of the login's
// failure event and error page (R-AUTH-007). Any other error is a failure of
// the system.
func refusalReason(err error) (string, bool) {
	switch {
	case errors.Is(err, principal.ErrUsernameTaken):
		return ReasonUsernameTaken, true
	case errors.Is(err, principal.ErrNotProvisioned):
		return ReasonNotProvisioned, true
	case errors.Is(err, principal.ErrUserDisabled), errors.Is(err, session.ErrPrincipalNotActive):
		return ReasonUserDisabled, true
	case errors.Is(err, principal.ErrUsernameInvalid):
		return ReasonUsernameInvalid, true
	}
	return "", false
}

// systemFailure is an error of trove itself after the provider answered: a
// database that does not answer is 503, anything else 500.
func systemFailure(err error) *loginError {
	if postgres.IsUnavailable(err) {
		return &loginError{reason: ReasonUnavailable, unavailable: true, cause: err}
	}
	return &loginError{reason: ReasonProvisioningFailed, cause: err}
}

// takeFlow is step 1: DELETE ... RETURNING on its own, committed before any
// outbound call, so the flow is single-use on any replica and no transaction is
// open while the provider answers (R-ARCH-019).
func (s *Service) takeFlow(ctx context.Context, state string) (q.TakeLoginFlowRow, *loginError) {
	if !session.WellFormed(state) {
		return q.TakeLoginFlowRow{}, fail(ReasonFlowExpired, errors.New("no well-formed state parameter"))
	}
	var rows []q.TakeLoginFlowRow
	err := s.o.DB.Read(ctx, func(db q.DBTX) error {
		var err error
		rows, err = q.New(db).TakeLoginFlow(ctx, session.Hash(state))
		return err
	})
	if err != nil {
		return q.TakeLoginFlowRow{}, &loginError{reason: ReasonUnavailable, unavailable: true, cause: err}
	}
	if len(rows) == 0 || !rows[0].Live {
		return q.TakeLoginFlowRow{}, fail(ReasonFlowExpired, errors.New("the flow is unknown, used or expired"))
	}
	return rows[0], nil
}

// checkResponse is steps 2 to 5: the provider of the flow, the browser binding,
// the iss parameter of RFC 9207 and an error answer of the provider.
func (p *Provider) checkResponse(in CallbackInput, flow q.TakeLoginFlowRow) *loginError {
	if flow.ProviderID != p.ID {
		return fail(ReasonProviderMismatch, fmt.Errorf("the flow belongs to provider %s", flow.ProviderID))
	}
	binding := in.Binding
	if !session.WellFormed(binding) || subtle.ConstantTimeCompare(session.Hash(binding), flow.BrowserBindingSha256) != 1 {
		return fail(ReasonBrowserMismatch, errors.New("the browser binding cookie does not match the flow"))
	}
	qv := in.Query
	doc := p.document()
	iss, has := qv["iss"]
	switch {
	case has && (len(iss) != 1 || iss[0] != p.cfg.Issuer):
		return fail(ReasonIssuerMismatch, fmt.Errorf("iss %q is not the issuer", iss))
	case !has && doc != nil && doc.IssParameterSupported:
		return fail(ReasonIssuerMismatch, errors.New("the provider advertises the iss parameter, the response has none"))
	}
	if code := qv.Get("error"); code != "" {
		return &loginError{
			reason: ReasonIDPError, idp: true, cause: fmt.Errorf("the provider answered %s", code),
			detail: qv.Get("error_description"),
		}
	}
	if qv.Get("code") == "" {
		return &loginError{reason: ReasonIDPError, idp: true, cause: errors.New("the response carries neither code nor error")}
	}
	return nil
}

// tokenResponse is the part of a token response trove reads.
type tokenResponse struct {
	IDToken     string `json:"id_token"`
	AccessToken string `json:"access_token"`
	Error       string `json:"error"`
}

// exchange is step 6: the code at the token endpoint with the PKCE verifier and
// the configured client authentication, within 10 s.
func (p *Provider) exchange(ctx context.Context, code, verifier string) (idToken, access string, lerr *loginError) {
	doc := p.document()
	if doc == nil {
		return "", "", &loginError{reason: ReasonProviderUnavailable, idp: true, cause: errors.New("no discovery document")}
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {p.redirectURI}, "code_verifier": {verifier}}
	var user, pass string
	switch p.cfg.ClientAuthMethod {
	case "client_secret_basic", "client_secret_post":
		secret, err := p.secret.get()
		if err != nil {
			return "", "", &loginError{reason: ReasonTokenExchange, idp: true, cause: fmt.Errorf("client secret: %w", err)}
		}
		if p.cfg.ClientAuthMethod == "client_secret_basic" {
			// RFC 6749 §2.3.1: both parts form-encoded before the Basic encoding.
			user, pass = url.QueryEscape(p.cfg.ClientID), url.QueryEscape(secret)
		} else {
			form.Set("client_id", p.cfg.ClientID)
			form.Set("client_secret", secret)
		}
	default:
		form.Set("client_id", p.cfg.ClientID)
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, doc.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", &loginError{reason: ReasonTokenExchange, idp: true, cause: err}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return "", "", &loginError{reason: ReasonTokenExchange, idp: true, cause: err}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDocument))
	if err != nil {
		return "", "", &loginError{reason: ReasonTokenExchange, idp: true, cause: err}
	}
	var tr tokenResponse
	_ = json.Unmarshal(body, &tr)
	if resp.StatusCode != http.StatusOK || tr.IDToken == "" {
		return "", "", &loginError{reason: ReasonTokenExchange, idp: true, cause: fmt.Errorf("token endpoint: status %d, error %q", resp.StatusCode, tr.Error)}
	}
	return tr.IDToken, tr.AccessToken, nil
}

// userinfo is step 8: with use_user_info and a userinfo endpoint, the JSON
// response, whose sub must be the ID token's (OpenID Connect Core 1.0 §5.3.2).
func (p *Provider) userinfo(ctx context.Context, access, sub string) (claims, *loginError) {
	doc := p.document()
	if !*p.cfg.UseUserInfo || doc == nil || doc.UserinfoEndpoint == "" || access == "" {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, doc.UserinfoEndpoint, nil)
	if err != nil {
		return nil, &loginError{reason: ReasonUserinfoFailed, idp: true, cause: err}
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Accept", "application/json")
	resp, err := p.http.Do(req)
	if err != nil {
		return nil, &loginError{reason: ReasonUserinfoFailed, idp: true, cause: err}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDocument))
	if err != nil {
		return nil, &loginError{reason: ReasonUserinfoFailed, idp: true, cause: err}
	}
	ct := resp.Header.Get("Content-Type")
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(ct, "application/json") {
		return nil, &loginError{reason: ReasonUserinfoFailed, idp: true, cause: fmt.Errorf("userinfo: status %d, content type %q (only JSON is supported)", resp.StatusCode, ct)}
	}
	c, err := decodeClaims(body)
	if err != nil {
		return nil, &loginError{reason: ReasonUserinfoFailed, idp: true, cause: err}
	}
	if got, _ := c["sub"].(string); got != sub {
		return nil, fail(ReasonUserinfoMismatch, errors.New("the userinfo sub is not the ID token's"))
	}
	return c, nil
}

// reauthSession checks a re-authentication before anything is provisioned
// (R-AUTH-011): the flow's session is still live, belongs to the browser that
// presents it, and the ID token proves the identity linked to its user.
func (s *Service) reauthSession(ctx context.Context, in CallbackInput, flow q.TakeLoginFlowRow, v validated) (session.Info, *loginError) {
	si, err := s.o.Sessions.Validate(ctx, in.SessionCookie)
	if err != nil || si.ID != flow.ReauthSessionID {
		return session.Info{}, fail(ReasonReauthUserMismatch, errors.New("the session the re-authentication started from is not the browser's live session"))
	}
	var users []string
	err = s.o.DB.Read(ctx, func(db q.DBTX) error {
		var err error
		users, err = q.New(db).UserByIdentity(ctx, q.UserByIdentityParams{Issuer: v.issuer, Subject: v.subject})
		return err
	})
	if err != nil {
		return session.Info{}, &loginError{reason: ReasonUnavailable, unavailable: true, cause: err}
	}
	if len(users) != 1 || users[0] != si.Principal.ID {
		return session.Info{}, fail(ReasonReauthUserMismatch, errors.New("the ID token's identity is not the session's user"))
	}
	return si, nil
}

// succeeded writes auth.login.succeeded.
func (s *Service) succeeded(ctx context.Context, src audit.Source, p *Provider, pr principal.Principal, sessionID, purpose string) {
	s.m.Attempt("oidc", "success", "")
	details, _ := json.Marshal(map[string]string{"method": "oidc", "provider": p.ID, "issuer": p.cfg.Issuer, "session_id": sessionID, "purpose": purpose})
	s.o.Audit.Record(ctx, audit.Event{
		Type: audit.EventAuthLoginSucceeded, Actor: audit.Actor{Kind: "user", ID: pr.ID, Name: pr.Name}, Outcome: audit.Success,
		Source: src, Subject: audit.Subject{Kind: "user", ID: pr.ID, Name: pr.Name}, Details: details,
	})
}

// failed counts and records a failed login and returns it as the error the edge
// renders (auth.login.failed).
func (s *Service) failed(ctx context.Context, in CallbackInput, p *Provider, purpose string, lerr *loginError) *LoginFailure {
	s.m.Attempt("oidc", "failure", lerr.reason)
	s.log.Info("login failed", "provider", p.ID, "reason", lerr.reason, "error", lerr.cause)
	d := map[string]string{"method": "oidc", "provider": p.ID, "issuer": p.cfg.Issuer}
	if purpose != "" {
		d["purpose"] = purpose
	}
	detail := truncateRunes(lerr.detail, 200)
	if detail != "" {
		d["idp_error"] = detail
	}
	details, _ := json.Marshal(d)
	s.o.Audit.Record(ctx, audit.Event{
		Type: audit.EventAuthLoginFailed, Actor: audit.Actor{Kind: "anonymous"}, Outcome: audit.Failure, Reason: lerr.reason,
		Source: in.Source, Details: details,
	})
	return &LoginFailure{Reason: lerr.reason, Detail: detail, Provider: lerr.idp, Unavailable: lerr.unavailable}
}
