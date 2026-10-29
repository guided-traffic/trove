package oidc

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"time"
)

// Reason codes of a failed login (R-AUTH-006, R-AUTH-007, R-AUTH-008,
// R-AUTH-011). They are the reason of auth.login.failed and shown on the error
// page; the cause of each stays in the log.
const (
	ReasonFlowExpired         = "flow_expired"
	ReasonProviderMismatch    = "provider_mismatch"
	ReasonBrowserMismatch     = "browser_mismatch"
	ReasonIssuerMismatch      = "issuer_mismatch"
	ReasonIDPError            = "idp_error"
	ReasonTokenExchange       = "token_exchange_failed"
	ReasonIDTokenInvalid      = "id_token_invalid"
	ReasonIDTokenExpired      = "id_token_expired"
	ReasonNonceMismatch       = "nonce_mismatch"
	ReasonUserinfoMismatch    = "userinfo_mismatch"
	ReasonUserinfoFailed      = "userinfo_failed"
	ReasonUsernameInvalid     = "username_invalid"
	ReasonUsernameTaken       = "username_taken"
	ReasonNotProvisioned      = "not_provisioned"
	ReasonUserDisabled        = "user_disabled"
	ReasonEmailUnverified     = "email_unverified"
	ReasonNotInAllowedGroup   = "not_in_allowed_group"
	ReasonGroupsClaimMissing  = "groups_claim_missing"
	ReasonReauthUserMismatch  = "reauth_user_mismatch"
	ReasonProviderUnavailable = "provider_unavailable"
	ReasonProvisioningFailed  = "provisioning_failed"
	// ReasonUnavailable is a database that did not answer (503).
	ReasonUnavailable = "unavailable"
)

// loginError is a failed login step: the reason code, whether the IdP is at
// fault (502) or the request (400), and the cause for the log.
type loginError struct {
	reason string
	idp    bool
	// unavailable is a dependency of trove that did not answer: 503.
	unavailable bool
	cause       error
	// detail is shown to the user (the IdP's error_description, escaped and
	// truncated by the error page).
	detail string
}

func (e *loginError) Error() string {
	if e.cause != nil {
		return e.reason + ": " + e.cause.Error()
	}
	return e.reason
}

func fail(reason string, cause error) *loginError { return &loginError{reason: reason, cause: cause} }

// claims is a decoded ID token or userinfo payload.
type claims map[string]any

// decodeClaims keeps numbers as json.Number so that a large iat is not
// rounded.
func decodeClaims(b []byte) (claims, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var c claims
	if err := dec.Decode(&c); err != nil {
		return nil, err
	}
	if c == nil {
		return nil, fmt.Errorf("the payload is not a JSON object")
	}
	return c, nil
}

// validated is what step 7 of R-AUTH-006 yields: the verified claims.
type validated struct {
	claims  claims
	issuer  string
	subject string
	sid     string
}

// validateIDToken is step 7 of R-AUTH-006 (OpenID Connect Core 1.0
// §3.1.3.7), check by check in the order of the table.
func (p *Provider) validateIDToken(ctx context.Context, raw string, nonceHash []byte, now time.Time) (validated, *loginError) {
	j, err := parseJWS(raw)
	if err != nil {
		return validated{}, fail(ReasonIDTokenInvalid, err)
	}
	if _, ok := algorithm(j.header.Alg); !ok || !slices.Contains(p.cfg.AllowedSigningAlgs, j.header.Alg) {
		return validated{}, fail(ReasonIDTokenInvalid, fmt.Errorf("alg %q is not allowed (none and HS* are always refused)", j.header.Alg))
	}
	key := p.key(ctx, j.header.Kid, j.header.Alg)
	if key == nil {
		return validated{}, fail(ReasonIDTokenInvalid, fmt.Errorf("no key in the JWK Set for kid %q", j.header.Kid))
	}
	if err := verify(j, key, p.cfg.AllowedSigningAlgs); err != nil {
		return validated{}, fail(ReasonIDTokenInvalid, err)
	}
	c, err := decodeClaims(j.payload)
	if err != nil {
		return validated{}, fail(ReasonIDTokenInvalid, err)
	}
	if lerr := p.checkClaims(c, nonceHash, now); lerr != nil {
		return validated{}, lerr
	}
	sid, _ := c["sid"].(string)
	return validated{claims: c, issuer: p.cfg.Issuer, subject: c["sub"].(string), sid: sid}, nil
}

// checkClaims applies the claim rows of the table of R-AUTH-006 step 7.
func (p *Provider) checkClaims(c claims, nonceHash []byte, now time.Time) *loginError {
	if iss, _ := c["iss"].(string); iss != p.cfg.Issuer {
		return fail(ReasonIDTokenInvalid, fmt.Errorf("iss %q is not the configured issuer", iss))
	}
	if err := p.checkAudience(c); err != nil {
		return fail(ReasonIDTokenInvalid, err)
	}
	skew := p.cfg.ClockSkew
	exp, ok := numericDate(c["exp"])
	if !ok {
		return fail(ReasonIDTokenInvalid, fmt.Errorf("exp is missing or not a number"))
	}
	if !exp.After(now.Add(-skew)) {
		return fail(ReasonIDTokenExpired, fmt.Errorf("expired at %s", exp.UTC().Format(time.RFC3339)))
	}
	if v, present := c["nbf"]; present {
		nbf, ok := numericDate(v)
		if !ok || nbf.After(now.Add(skew)) {
			return fail(ReasonIDTokenExpired, fmt.Errorf("not valid before %v", v))
		}
	}
	iat, ok := numericDate(c["iat"])
	if !ok {
		return fail(ReasonIDTokenInvalid, fmt.Errorf("iat is missing or not a number"))
	}
	if iat.Before(now.Add(-p.cfg.MaxIDTokenAge)) || iat.After(now.Add(skew)) {
		return fail(ReasonIDTokenInvalid, fmt.Errorf("iat %s is outside max_id_token_age and clock_skew", iat.UTC().Format(time.RFC3339)))
	}
	nonce, _ := c["nonce"].(string)
	if nonce == "" || subtle.ConstantTimeCompare(sha256Of(nonce), nonceHash) != 1 {
		return fail(ReasonNonceMismatch, fmt.Errorf("the nonce does not match the login flow"))
	}
	sub, _ := c["sub"].(string)
	if sub == "" || len(sub) > 255 {
		return fail(ReasonIDTokenInvalid, fmt.Errorf("sub is empty or longer than 255 characters"))
	}
	return nil
}

// checkAudience: aud contains client_id and every other value is trusted; azp,
// when present or when aud has several values, is client_id.
func (p *Provider) checkAudience(c claims) error {
	var aud []string
	switch v := c["aud"].(type) {
	case string:
		aud = []string{v}
	case []any:
		for _, a := range v {
			s, ok := a.(string)
			if !ok {
				return fmt.Errorf("aud holds a non-string value")
			}
			aud = append(aud, s)
		}
	default:
		return fmt.Errorf("aud is missing")
	}
	if !slices.Contains(aud, p.cfg.ClientID) {
		return fmt.Errorf("aud does not contain the client ID")
	}
	for _, a := range aud {
		if a != p.cfg.ClientID && !slices.Contains(p.cfg.TrustedAudiences, a) {
			return fmt.Errorf("aud holds the untrusted audience %q", a)
		}
	}
	azp, present := c["azp"]
	if present || len(aud) > 1 {
		if s, _ := azp.(string); s != p.cfg.ClientID {
			return fmt.Errorf("azp %v is not the client ID", azp)
		}
	}
	return nil
}

// numericDate reads a NumericDate (RFC 7519 §2): seconds, possibly fractional.
func numericDate(v any) (time.Time, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return time.Time{}, false
	}
	f, err := n.Float64()
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f > 1<<40 {
		return time.Time{}, false
	}
	sec, frac := math.Modf(f)
	return time.Unix(int64(sec), int64(frac*1e9)), true
}
