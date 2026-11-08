// Package registry is the registry root of the OCI distribution API: the routes below /v2/ that belong to the core and
// not to a repository (R-OCI-003). This file is the token service (R-OCI-004 to R-OCI-006); the mount of the root, the
// version check, the catalog and the challenge of the repository routes are the registry root's own files.
package registry

import (
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/guided-traffic/trove/internal/api/edge"
	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth"
	"github.com/guided-traffic/trove/internal/auth/registrytoken"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
)

// maxFormBytes bounds the body of the OAuth 2.0 form of a token request: its fields are a user name, a token, a service
// and at most 16 scope entries of at most 255 bytes.
const maxFormBytes = 16 << 10

// APIVersionHeader and APIVersion are the header every response below /v2/ carries (R-OCI-003).
const (
	APIVersionHeader = "Docker-Distribution-API-Version"
	APIVersion       = "registry/2.0"
)

// TokenOptions configure [NewToken].
type TokenOptions struct {
	// Authenticate runs the one credential of a token request through the authentication stage of the endpoint the
	// request arrived on (R-OCI-004 step 3, auth.Stage.AuthenticateTokenRequest) and converts the result to the caller the
	// chain stores. The composition root builds it per endpoint and records the caller in the request log. Its error is
	// the stage's refusal: 401 with Basic realm="trove", 429, 503.
	Authenticate func(r *http.Request, c auth.Presented) (httpserver.Principal, error)
	// Signer issues the registry tokens (the auth area's, so that the stage verifies with the same keys and the same
	// lifetime, oci.token_lifetime).
	Signer *registrytoken.Signer
	// Decider is the decision point the grant asks (R-OCI-006).
	Decider authz.Decider
	// Repos looks up a repository the decision point allowed, to grant only repositories of the format.
	Repos repository.Reader
	// Format is the ID of the format whose repositories the registry serves ("oci").
	Format string
	// ValidName is the image-name grammar of the format (R-OCI-002).
	ValidName func(string) bool
	// Metrics counts the answers (R-OCI-034); nil counts nothing.
	Metrics *registrytoken.Metrics
	// Audit records auth.registry_token.denied (R-OCI-034); nil records nothing.
	Audit audit.Recorder
	// WriteError renders an error in the format's shape (R-OCI-003, the format's WriteError).
	WriteError func(http.ResponseWriter, *http.Request, error)
	// Bound returns the repository the request's host root is bound to (R-OCI-029, R-EP-006), false for an unbound host.
	// Nil reads httpserver.RequestInfo.RootRepository.
	Bound func(*http.Request) (qname.Repo, bool)
	// Endpoint names the endpoint the request arrived on (R-EP-002); nil reads the chain's endpoint. The stage verifies a
	// registry token against the same name.
	Endpoint func(*http.Request) string
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	Log *slog.Logger
}

// Token is the token service at GET and POST /v2/_token (R-OCI-004).
type Token struct {
	o       TokenOptions
	grantor Grantor
}

// NewToken creates the token service.
func NewToken(o TokenOptions) (*Token, error) {
	switch {
	case o.Authenticate == nil, o.Signer == nil, o.Decider == nil, o.Repos == nil, o.WriteError == nil, o.ValidName == nil:
		return nil, errors.New("registry: the token service needs an authentication stage, a signer, a decision point, a repository reader, an error writer and the name grammar")
	case o.Format == "":
		return nil, errors.New("registry: the token service needs the format of the registry")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Audit == nil {
		o.Audit = audit.Nop{}
	}
	if o.Bound == nil {
		o.Bound = func(r *http.Request) (qname.Repo, bool) {
			repo := httpserver.InfoFrom(r.Context()).RootRepository
			return repo, !repo.IsZero()
		}
	}
	if o.Endpoint == nil {
		o.Endpoint = func(r *http.Request) string { return httpserver.EndpointFrom(r.Context()).Name() }
	}
	return &Token{o: o, grantor: Grantor{Repos: o.Repos, Format: o.Format}}, nil
}

// tokenRequest is what a token request asks: the scope values, the service and the one credential, with the label of how
// it was presented for the audit event.
type tokenRequest struct {
	scopes     []string
	service    string
	credential auth.Presented
	presented  string
}

// tokenAnswer is the body of 200 (R-OCI-004 step 4): token and access_token carry the same value, so a client of the
// Docker form and one of the OAuth 2.0 form both find it. No refresh token is issued.
type tokenAnswer struct {
	Token       string `json:"token"`
	AccessToken string `json:"access_token"`
	ExpiresIn   int64  `json:"expires_in"`
	IssuedAt    string `json:"issued_at"`
}

// ServeHTTP answers a token request (R-OCI-004): read the request, authenticate its credential through the stage, grant
// per scope entry, sign. The chain's stage did not evaluate the credential of this route; this handler presents it.
func (h *Token) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(APIVersionHeader, APIVersion)
	tr, err := h.read(w, r)
	if err != nil {
		h.refuse(w, r, err)
		return
	}
	bound, ok := h.o.Bound(r)
	if !ok {
		bound = qname.Repo{}
	}
	scopes, err := registrytoken.ParseScopes(tr.scopes, bound, h.o.ValidName)
	if err != nil {
		h.refuse(w, r, invalidRequest("A token request carries at most 16 scope entries."))
		return
	}
	if _, service := registrytoken.Realm(httpserver.InfoFrom(r.Context()).BaseURL); tr.service != "" && service != "" && tr.service != service {
		h.refuse(w, r, invalidRequest("This token service serves "+service+"."))
		return
	}
	p, err := h.o.Authenticate(r, tr.credential)
	if err != nil {
		h.denied(w, r, tr, err)
		return
	}
	r = r.WithContext(httpserver.WithPrincipal(r.Context(), p))
	grant, err := h.grant(r, scopes)
	if err != nil {
		h.o.WriteError(w, r, err)
		return
	}
	h.issue(w, r, p, tr, grant)
}

// read takes the request apart: GET reads the query and the Authorization header; POST reads the OAuth 2.0 form of the
// password grant (R-OCI-004 step 2), whose password is the credential.
func (h *Token) read(w http.ResponseWriter, r *http.Request) (tokenRequest, error) {
	switch r.Method {
	case http.MethodGet:
		q := r.URL.Query()
		c := auth.PresentedFrom(r)
		return tokenRequest{scopes: q["scope"], service: q.Get("service"), credential: c, presented: c.Kind()}, nil
	case http.MethodPost:
		return h.readForm(w, r)
	}
	return tokenRequest{}, errs.E(errs.MethodNotAllowed, errs.CodeOCIUnsupported, "The token service answers GET and POST.",
		errs.WithAllow(http.MethodGet, http.MethodPost))
}

func (h *Token) readForm(w http.ResponseWriter, r *http.Request) (tokenRequest, error) {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/x-www-form-urlencoded" {
		return tokenRequest{}, invalidRequest("The token request form is application/x-www-form-urlencoded.")
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		return tokenRequest{}, invalidRequest("The token request form cannot be read.")
	}
	f := r.PostForm
	switch gt := f.Get("grant_type"); gt {
	case "password":
	case "":
		return tokenRequest{}, invalidRequest("The token request names no grant_type.")
	default:
		// No refresh token is ever issued (R-OCI-004 step 4), so the refresh grant has nothing to refresh.
		return tokenRequest{}, oauthError{code: "unsupported_grant_type", text: "The token service serves the password grant only."}
	}
	if f.Get("password") == "" {
		return tokenRequest{}, invalidRequest("The password grant needs a password: a trove token.")
	}
	if r.Header.Get("Authorization") != "" {
		// One credential per request (R-AUTH-001): the form's.
		return tokenRequest{}, invalidRequest("A token request with a form sends its credential in the form, not in a header.")
	}
	return tokenRequest{
		scopes: f["scope"], service: f.Get("service"), credential: auth.PresentedForm(f.Get("username"), f.Get("password")), presented: "form",
	}, nil
}

// grant decides the grant on one index of the decision point (R-AUTHZ-015), as a question that writes no audit event.
func (h *Token) grant(r *http.Request, scopes []registrytoken.Scope) (registrytoken.Grant, error) {
	if len(scopes) == 0 {
		return registrytoken.Grant{}, nil
	}
	var names []qname.Repo
	for _, s := range scopes {
		if !s.Catalog {
			names = append(names, s.Repo)
		}
	}
	acc, err := edge.Begin(r.Context(), h.o.Decider, edge.SubjectFrom(r), names...)
	if err != nil {
		return registrytoken.Grant{}, err
	}
	return h.grantor.Grant(r.Context(), acc, scopes)
}

// issue signs the token and answers 200. The lifetime is oci.token_lifetime and never beyond the expiry of the trove token
// that authenticated the request (R-OCI-005).
func (h *Token) issue(w http.ResponseWriter, r *http.Request, p httpserver.Principal, tr tokenRequest, grant registrytoken.Grant) {
	now := h.o.Now()
	iat := now.Truncate(time.Second)
	// The one source of the lifetime is the signer's (oci.token_lifetime): it also verifies against it.
	exp := iat.Add(h.o.Signer.Lifetime())
	if e := p.Credential.ExpiresAt; e != nil && e.Before(exp) {
		exp = e.Truncate(time.Second)
	}
	if !exp.After(iat) {
		// The trove token expires within this second: no registry token outlives it. A refusal like an expired one.
		h.denied(w, r, tr, errs.E(errs.Unauthenticated, errs.CodeInvalidToken, "The token is not valid.", errs.WithChallenge(`Basic realm="trove"`)))
		return
	}
	c := registrytoken.Claims{
		Anonymous: p.Anonymous, Endpoint: h.o.Endpoint(r), IssuedAt: iat, ExpiresAt: exp, Grant: grant,
	}
	if !p.Anonymous {
		c.PrincipalKind, c.PrincipalID = string(p.Kind), p.ID
		c.Binding = registrytoken.Binding{
			TokenID: p.Credential.TokenID, TokenType: string(p.Credential.TokenType), SecretSHA256: tr.credential.SecretSHA256(),
		}
	}
	tok, err := h.o.Signer.Issue(c)
	if err != nil {
		if errors.Is(err, registrytoken.ErrTooLarge) {
			h.refuse(w, r, invalidRequest("The granted scopes do not fit in one token; ask for fewer."))
			return
		}
		h.o.WriteError(w, r, errs.Wrap(err, "registry.token.issue"))
		return
	}
	if p.Anonymous {
		h.o.Metrics.Count(registrytoken.OutcomeAnonymous)
	} else {
		h.o.Metrics.Count(registrytoken.OutcomeIssued)
	}
	body, err := json.Marshal(tokenAnswer{ //nolint:gosec // G117: the answer of a token service is the token (R-OCI-004 step 4)
		Token: tok, AccessToken: tok, ExpiresIn: int64(exp.Sub(iat) / time.Second), IssuedAt: iat.UTC().Format(time.RFC3339),
	})
	if err != nil {
		h.o.WriteError(w, r, errs.Wrap(err, "registry.token.answer"))
		return
	}
	hd := w.Header()
	hd.Set("Content-Type", "application/json")
	hd.Set("X-Content-Type-Options", "nosniff")
	// A token answer is never stored by a cache on the way (RFC 6749 §5.1).
	hd.Set("Cache-Control", "no-store")
	hd.Set("Pragma", "no-cache")
	hd.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// denied answers a credential the stage refused: its 401, 429 or 503 in the format's shape. A 401 counts as denied and
// writes auth.registry_token.denied, deduplicated per client address, endpoint, reason and credential form (R-OCI-034); a 429 counts as denied, the throttle of
// R-AUTH-038 having decided already; a 503 is no answer about the credential.
func (h *Token) denied(w http.ResponseWriter, r *http.Request, tr tokenRequest, err error) {
	if e, ok := errs.As(err); ok {
		switch e.Kind {
		case errs.Unauthenticated:
			h.o.Metrics.Count(registrytoken.OutcomeDenied)
			details, _ := json.Marshal(map[string]string{"reason": deniedReason(e.Code), "credential": tr.presented})
			h.o.Audit.Record(r.Context(), audit.Event{
				Type: audit.EventRegistryTokenDenied, Outcome: audit.Failure, Reason: deniedReason(e.Code),
				Actor: audit.Actor{Kind: "anonymous"}, Source: httpserver.SourceFrom(r.Context()), Endpoint: h.o.Endpoint(r),
				Details: details,
			})
		case errs.RateLimited:
			h.o.Metrics.Count(registrytoken.OutcomeDenied)
		}
	}
	h.o.WriteError(w, r, err)
}

// deniedReason is the reason of auth.registry_token.denied for the code of the stage's 401.
func deniedReason(code string) string {
	switch code {
	case errs.CodeCredentialsRequired, errs.CodePrincipalDisabled:
		return code
	}
	return errs.CodeInvalidToken
}

// oauthError is a refusal of a malformed token request: 400 with the error list of the distribution specification
// (R-OCI-003, code UNSUPPORTED) and, in the same object, the members error and error_description of OAuth 2.0 (RFC 6749
// §5.2), so a client of either form reads it (Q-OCI-52).
type oauthError struct {
	code string
	text string
}

func (e oauthError) Error() string { return e.code + ": " + e.text }

func invalidRequest(text string) oauthError { return oauthError{code: "invalid_request", text: text} }

// refuse renders a refusal of the request itself: an [oauthError] as 400, anything else through the format's writer.
func (h *Token) refuse(w http.ResponseWriter, r *http.Request, err error) {
	var oe oauthError
	if !errors.As(err, &oe) {
		h.o.WriteError(w, r, err)
		return
	}
	body, _ := json.Marshal(struct {
		Errors []registryError `json:"errors"`
		Error  string          `json:"error"`
		Desc   string          `json:"error_description"`
	}{[]registryError{{Code: "UNSUPPORTED", Message: oe.code + ": " + oe.text}}, oe.code, oe.text})
	hd := w.Header()
	hd.Set("Content-Type", "application/json")
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Cache-Control", "no-store")
	hd.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusBadRequest)
	_, _ = w.Write(body)
}

// registryError is one entry of the error list of the distribution specification.
type registryError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  any    `json:"detail"`
}
