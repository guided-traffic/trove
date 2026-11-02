package v1

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/auth/jws"
	"github.com/guided-traffic/trove/internal/ci"
	"github.com/guided-traffic/trove/internal/ci/reason"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// The token exchange, POST /api/v1/auth/oidc/token (R-CI-007, R-CI-008, R-CI-021), work package W4. What the
// handler does, so that the endpoint stays what the specification says:
//
//   - It requires and parses no trove credential, session or CSRF token. The authentication stage answers the
//     anonymous principal for this route whatever the request carries (internal/auth.isExchangeRoute), the
//     declaration is {public: true}, and the route belongs to the surface family ci_exchange
//     (httpserver.FamilyOf), so an endpoint serves it only when its surfaces name ci_exchange. The general API
//     rate limit skips the route (exchangePath in ratelimit.go): the exchange has the limiters of R-CI-021.
//   - ci.exchange.enabled false answers 404 as a path that does not exist, before anything else.
//   - [ci.Exchanger.CheckAddress] runs first, before the body is read; then the body limit
//     ci.exchange.max_body_bytes, the content type (form or JSON, anything else 400 unsupported_content_type), a
//     parameter sent twice (400 malformed_request, RFC 6749 §3.2), then [ci.Exchanger.Exchange].
//   - Every failure is the OAuth error response of RFC 6749 §5.2, not problem details: the status and `error` from
//     [ci.Reason.OAuth], `reason` unless ci.exchange.expose_reason is false, `requestId` equal to X-Request-Id,
//     `Retry-After` on 429 and 503, Cache-Control no-store and Pragma no-cache. The body and the log hold no
//     subject token and no trv_cit_ value; a description names parameters, never their values.

// The parameters of the exchange (R-CI-007); any other parameter is ignored (RFC 6749 §3.2).
const (
	pGrantType          = "grant_type"
	pSubjectToken       = "subject_token"
	pSubjectTokenType   = "subject_token_type"
	pRequestedTokenType = "requested_token_type"
	pScope              = "scope"
	pAudience           = "audience"
	pResource           = "resource"
	pActorToken         = "actor_token"
	pActorTokenType     = "actor_token_type"
	pCIIssuer           = "ci_issuer"
	pTTL                = "ttl"
)

// exchangeAnswer is the 200 body (R-CI-007).
type exchangeAnswer struct {
	AccessToken     string   `json:"access_token"`
	IssuedTokenType string   `json:"issued_token_type"`
	TokenType       string   `json:"token_type"`
	ExpiresIn       int64    `json:"expires_in"`
	Scope           string   `json:"scope"`
	CIIssuer        string   `json:"ci_issuer"`
	Mappings        []string `json:"mappings"`
}

// exchangeErrorBody is the OAuth error body with trove's members (R-CI-008).
type exchangeErrorBody struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description,omitempty"`
	Reason           string `json:"reason,omitempty"`
	RequestID        string `json:"requestId"`
}

// ExchangeCiToken implements POST /api/v1/auth/oidc/token.
func (s *Server) ExchangeCiToken(w http.ResponseWriter, r *http.Request) {
	cfg := s.o.CI.Exchange
	if !cfg.Enabled {
		httpserver.Problem(w, r, errs.E(errs.NotFound, errs.CodeNotFound, "The CI token exchange is switched off on this installation (ci.exchange.enabled)."))
		return
	}
	x := s.o.CI.Exchanger
	if x == nil {
		s.exchangeError(w, r, &ci.ExchangeError{Reason: reason.DatabaseUnavailable, Description: "the exchange is not available on this replica", RetryAfter: 5 * time.Second})
		return
	}
	src := httpserver.SourceFrom(r.Context())
	if err := x.CheckAddress(r.Context(), src); err != nil {
		s.exchangeError(w, r, asExchangeError(err))
		return
	}
	req, ee := decodeExchange(w, r, cfg.MaxBodyBytes)
	if ee != nil {
		x.Refused(r.Context(), src, ee)
		s.exchangeError(w, r, ee)
		return
	}
	req.Source = src
	res, err := x.Exchange(r.Context(), req)
	if err != nil {
		s.exchangeError(w, r, asExchangeError(err))
		return
	}
	// The token answer of RFC 8693 is the one place the CI token's value appears (R-CI-007, INV-8).
	b, err := json.Marshal(exchangeAnswer{ //nolint:gosec // G117: the token response itself, written once and never logged
		AccessToken: res.AccessToken, IssuedTokenType: res.IssuedTokenType, TokenType: res.TokenType, ExpiresIn: res.ExpiresIn,
		Scope: res.Scope, CIIssuer: res.CIIssuer, Mappings: append([]string{}, res.Mappings...),
	})
	if err != nil {
		// The exchange logged nothing for it: the failure is the handler's. The cause names no token.
		httpserver.Logger(r.Context()).Warn("ci exchange answer could not be written", "error", err)
		s.exchangeError(w, r, &ci.ExchangeError{Reason: reason.Internal, Description: "the answer could not be written", Err: err})
		return
	}
	writeExchange(w, http.StatusOK, b)
}

func asExchangeError(err error) *ci.ExchangeError {
	var ee *ci.ExchangeError
	if errors.As(err, &ee) {
		return ee
	}
	return &ci.ExchangeError{Reason: reason.Internal, Description: "the exchange failed; the request ID names it in the log", Err: err}
}

// exchangeError writes the OAuth error response. Under ci.exchange.expose_reason false every 400 is
// invalid_request without reason and with a generic description; the other statuses keep theirs. The exchange
// service logs its own failures (ExchangeService.finish), so this writes no log line.
func (s *Server) exchangeError(w http.ResponseWriter, r *http.Request, ee *ci.ExchangeError) {
	status, oauthError := ee.Reason.OAuth()
	body := exchangeErrorBody{Error: oauthError, ErrorDescription: ee.Description, Reason: string(ee.Reason), RequestID: httpserver.RequestID(r.Context())}
	if status == http.StatusBadRequest && !s.o.CI.Exchange.ExposeReason {
		body = exchangeErrorBody{Error: "invalid_request", ErrorDescription: "the exchange was refused; the request ID names the reason in the audit trail", RequestID: body.RequestID}
	}
	if ee.RetryAfter > 0 && (status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable) {
		secs := int64((ee.RetryAfter + time.Second - 1) / time.Second)
		w.Header().Set("Retry-After", strconv.FormatInt(max(secs, 1), 10))
	}
	b, _ := json.Marshal(body)
	writeExchange(w, status, b)
}

// writeExchange writes a body of the exchange with the headers of RFC 6749 §5.1 and §5.2.
func writeExchange(w http.ResponseWriter, status int, b []byte) {
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Pragma", "no-cache")
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n'))
}

// decodeExchange reads the body within the limit and decodes the form or the JSON object into the request. A
// parameter sent twice and, in JSON, a duplicate member or a value that is not a string (ttl may be a number) is
// malformed_request. Parameters the exchange does not know are ignored (RFC 6749 §3.2).
func decodeExchange(w http.ResponseWriter, r *http.Request, maxBytes int) (ci.ExchangeRequest, *ci.ExchangeError) {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || (media != "application/x-www-form-urlencoded" && media != "application/json") {
		return ci.ExchangeRequest{}, exRefuse(reason.UnsupportedContentType, "the body is application/x-www-form-urlencoded or application/json")
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, int64(maxBytes)))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return ci.ExchangeRequest{}, exRefuse(reason.MalformedRequest, "the body is larger than ci.exchange.max_body_bytes ("+strconv.Itoa(maxBytes)+" bytes)")
		}
		return ci.ExchangeRequest{}, exRefuse(reason.MalformedRequest, "the body could not be read")
	}
	values := map[string]string{}
	if media == "application/json" {
		obj, err := jws.DecodeObject(body)
		if err != nil {
			return ci.ExchangeRequest{}, exRefuse(reason.MalformedRequest, "the body is not a JSON object with unique member names")
		}
		for k, v := range obj {
			if !knownParameter(k) {
				continue
			}
			switch t := v.(type) {
			case string:
				values[k] = t
			case json.Number:
				if k != pTTL {
					return ci.ExchangeRequest{}, exRefuse(reason.MalformedRequest, "the member "+strconv.Quote(k)+" is not a string")
				}
				values[k] = t.String()
			default:
				return ci.ExchangeRequest{}, exRefuse(reason.MalformedRequest, "the member "+strconv.Quote(k)+" is not a string")
			}
		}
	} else {
		form, err := url.ParseQuery(string(body))
		if err != nil {
			return ci.ExchangeRequest{}, exRefuse(reason.MalformedRequest, "the body is not a valid form encoding")
		}
		for k, vs := range form {
			if !knownParameter(k) {
				continue
			}
			if len(vs) > 1 {
				return ci.ExchangeRequest{}, exRefuse(reason.MalformedRequest, "the parameter "+strconv.Quote(k)+" is sent more than once (RFC 6749 section 3.2)")
			}
			values[k] = vs[0]
		}
	}
	return exchangeRequestOf(values)
}

// exchangeRequestOf fills the request from the decoded parameters. A parameter without a value counts as
// omitted (RFC 6749 §3.1).
func exchangeRequestOf(v map[string]string) (ci.ExchangeRequest, *ci.ExchangeError) {
	req := ci.ExchangeRequest{
		GrantType: v[pGrantType], SubjectToken: v[pSubjectToken], SubjectTokenType: v[pSubjectTokenType],
		RequestedTokenType: v[pRequestedTokenType], Scope: v[pScope], ActorToken: v[pActorToken],
		ActorTokenType: v[pActorTokenType], CIIssuer: v[pCIIssuer],
	}
	if a := v[pAudience]; a != "" {
		req.Audience = []string{a}
	}
	if a := v[pResource]; a != "" {
		req.Resource = []string{a}
	}
	if t := strings.TrimSpace(v[pTTL]); t != "" {
		n, err := strconv.ParseInt(t, 10, 64)
		if err != nil {
			return ci.ExchangeRequest{}, exRefuse(reason.MalformedRequest, "ttl is a whole number of seconds")
		}
		req.TTLSeconds = &n
	}
	return req, nil
}

func exRefuse(r ci.Reason, description string) *ci.ExchangeError {
	return &ci.ExchangeError{Reason: r, Description: description}
}

// knownParameter reports whether a name is a parameter of the exchange; the others are ignored, and a
// description never quotes one (it is the caller's text).
func knownParameter(name string) bool {
	switch name {
	case pGrantType, pSubjectToken, pSubjectTokenType, pRequestedTokenType, pScope, pAudience, pResource,
		pActorToken, pActorTokenType, pCIIssuer, pTTL:
		return true
	}
	return false
}
